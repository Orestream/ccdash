package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/robinmalmstrom/ccdash/backend/internal/claude"
	gitwt "github.com/robinmalmstrom/ccdash/backend/internal/git"
	"github.com/robinmalmstrom/ccdash/backend/internal/models"
	"github.com/robinmalmstrom/ccdash/backend/internal/store"
	"github.com/robinmalmstrom/ccdash/backend/internal/ws"
)

// fakeSession is a controllable in-memory claude.Session. Tests emit events
// into it and inspect what was sent back.
type fakeSession struct {
	events chan claude.Event

	mu       sync.Mutex
	sent     []string
	sentImgs [][]claude.Image
	responds []respondRec
	setModes []string
}

type respondRec struct {
	id    string
	allow bool
	input string
	msg   string
}

func newFakeSession() *fakeSession {
	return &fakeSession{events: make(chan claude.Event, 32)}
}

func (f *fakeSession) Send(text string, images []claude.Image) error {
	f.mu.Lock()
	f.sent = append(f.sent, text)
	f.sentImgs = append(f.sentImgs, images)
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) Respond(id string, d claude.Decision, input json.RawMessage, msg string) error {
	f.mu.Lock()
	f.responds = append(f.responds, respondRec{id: id, allow: d == claude.DecisionAllow, input: string(input), msg: msg})
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) SetMode(mode string) error {
	f.mu.Lock()
	f.setModes = append(f.setModes, mode)
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) Events() <-chan claude.Event { return f.events }
func (f *fakeSession) Close() error                { return nil }

func (f *fakeSession) emit(ev claude.Event) { f.events <- ev }
func (f *fakeSession) done()                { close(f.events) }

func (f *fakeSession) responses() []respondRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]respondRec(nil), f.responds...)
}

type fakeRunner struct {
	sess     *fakeSession
	startErr error
	lastReq  claude.StartRequest
}

func (r *fakeRunner) Start(_ context.Context, req claude.StartRequest) (claude.Session, error) {
	r.lastReq = req
	if r.startErr != nil {
		return nil, r.startErr
	}
	return r.sess, nil
}

func setup(t *testing.T, runner claude.Runner) (*Manager, *store.Store, models.Session) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p, _ := st.CreateProject("demo", "/tmp/demo")
	sess, _ := st.CreateSession(p.ID, "task", "claude-opus-4-7", models.ModeDefault, store.SessionInit{})
	return New(st, ws.NewHub(), runner), st, sess
}

func waitForStatus(t *testing.T, st *store.Store, id string, want models.SessionStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s, err := st.GetSession(id); err == nil && s.Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, _ := st.GetSession(id)
	t.Fatalf("status never became %s (last %s)", want, got.Status)
}

func TestSendMessageStreamingSuccess(t *testing.T) {
	fs := newFakeSession()
	runner := &fakeRunner{sess: fs}
	mgr, st, sess := setup(t, runner)

	if _, err := mgr.SendMessage(sess.ID, "hello", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	fs.emit(claude.Event{Kind: claude.KindSystem, ClaudeSessionID: "claude-xyz"})
	fs.emit(claude.Event{Kind: claude.KindText, Text: "Hi "})
	fs.emit(claude.Event{Kind: claude.KindText, Text: "there"})
	fs.emit(claude.Event{Kind: claude.KindAssistant, Text: "Hi there"})
	fs.emit(claude.Event{Kind: claude.KindResult, InputTokens: 10, OutputTokens: 5, CostUSD: 0.001})
	fs.done()
	mgr.Wait()

	got, _ := st.GetSession(sess.ID)
	if got.Status != models.StatusIdle {
		t.Fatalf("expected idle after completed turn, got %s", got.Status)
	}
	if got.ClaudeSessionID != "claude-xyz" {
		t.Fatalf("claude id not captured: %q", got.ClaudeSessionID)
	}
	msgs, _ := st.ListMessages(sess.ID)
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" || msgs[1].Content != "Hi there" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
	usage, _ := st.ListSessionUsage(sess.ID)
	if len(usage) != 1 || usage[0].InputTokens != 10 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	if fs.sent[0] != "hello" {
		t.Fatalf("expected prompt sent, got %v", fs.sent)
	}
	if runner.lastReq.Cwd != "/tmp/demo" {
		t.Fatalf("expected cwd from project, got %q", runner.lastReq.Cwd)
	}
}

func TestStopReturnsToIdle(t *testing.T) {
	fs := newFakeSession()
	mgr, st, sess := setup(t, &fakeRunner{sess: fs})

	if _, err := mgr.SendMessage(sess.ID, "hello", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForStatus(t, st, sess.ID, models.StatusProcessing)

	if err := mgr.Stop(sess.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got, _ := st.GetSession(sess.ID); got.Status != models.StatusIdle {
		t.Fatalf("expected idle after stop, got %s", got.Status)
	}

	fs.done()
	mgr.Wait()
}

func TestThinkingPersisted(t *testing.T) {
	fs := newFakeSession()
	mgr, st, sess := setup(t, &fakeRunner{sess: fs})

	_, _ = mgr.SendMessage(sess.ID, "hi", nil)
	fs.emit(claude.Event{Kind: claude.KindThinking, Text: "let me think"})
	fs.emit(claude.Event{Kind: claude.KindAssistant, Text: "answer"})
	fs.emit(claude.Event{Kind: claude.KindResult})
	fs.done()
	mgr.Wait()

	msgs, _ := st.ListMessages(sess.ID)
	// user, thinking, assistant
	if len(msgs) != 3 || msgs[1].Role != "thinking" || msgs[2].Role != "assistant" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
}

func TestPermissionAllowAlways(t *testing.T) {
	fs := newFakeSession()
	mgr, st, sess := setup(t, &fakeRunner{sess: fs})

	_, _ = mgr.SendMessage(sess.ID, "do it", nil)
	fs.emit(claude.Event{Kind: claude.KindPermission, RequestID: "req_1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)})
	waitForStatus(t, st, sess.ID, models.StatusAwaitingApproval)

	if pending := mgr.PendingPermissions(sess.ID); len(pending) != 1 || pending[0].ToolName != "Bash" {
		t.Fatalf("unexpected pending: %+v", pending)
	}

	if err := mgr.RespondPermission(sess.ID, "req_1", true, true, ""); err != nil {
		t.Fatalf("respond: %v", err)
	}
	if len(mgr.PendingPermissions(sess.ID)) != 0 {
		t.Fatal("expected no pending after respond")
	}

	// A second Bash request should now be auto-allowed without surfacing.
	fs.emit(claude.Event{Kind: claude.KindPermission, RequestID: "req_2", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"pwd"}`)})
	fs.emit(claude.Event{Kind: claude.KindResult})
	fs.done()
	mgr.Wait()

	resp := fs.responses()
	if len(resp) != 2 {
		t.Fatalf("expected 2 responses, got %d: %+v", len(resp), resp)
	}
	for _, r := range resp {
		if !r.allow {
			t.Fatalf("expected allow, got deny for %s", r.id)
		}
	}
	if got, _ := st.GetSession(sess.ID); got.Status != models.StatusIdle {
		t.Fatalf("expected idle after completed turn, got %s", got.Status)
	}
}

func TestPermissionDeny(t *testing.T) {
	fs := newFakeSession()
	mgr, st, sess := setup(t, &fakeRunner{sess: fs})

	_, _ = mgr.SendMessage(sess.ID, "do it", nil)
	fs.emit(claude.Event{Kind: claude.KindPermission, RequestID: "req_1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"rm -rf /"}`)})
	waitForStatus(t, st, sess.ID, models.StatusAwaitingApproval)

	if err := mgr.RespondPermission(sess.ID, "req_1", false, false, "nope"); err != nil {
		t.Fatalf("respond: %v", err)
	}
	fs.done()
	mgr.Wait()

	resp := fs.responses()
	if len(resp) != 1 || resp[0].allow || resp[0].msg != "nope" {
		t.Fatalf("expected single deny with message, got %+v", resp)
	}
}

func TestRespondUnknownRequest(t *testing.T) {
	fs := newFakeSession()
	mgr, _, sess := setup(t, &fakeRunner{sess: fs})
	_, _ = mgr.SendMessage(sess.ID, "x", nil)
	if err := mgr.RespondPermission(sess.ID, "ghost", true, false, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	fs.done()
	mgr.Wait()
}

func TestSetModeUpdatesLiveSession(t *testing.T) {
	fs := newFakeSession()
	mgr, st, sess := setup(t, &fakeRunner{sess: fs})

	_, _ = mgr.SendMessage(sess.ID, "x", nil)
	updated, err := mgr.SetMode(sess.ID, models.ModePlan)
	if err != nil {
		t.Fatalf("set mode: %v", err)
	}
	if updated.PermissionMode != models.ModePlan {
		t.Fatalf("expected plan mode, got %s", updated.PermissionMode)
	}
	persisted, _ := st.GetSession(sess.ID)
	if persisted.PermissionMode != models.ModePlan {
		t.Fatalf("mode not persisted: %s", persisted.PermissionMode)
	}

	fs.done()
	mgr.Wait()

	fs.mu.Lock()
	modes := fs.setModes
	fs.mu.Unlock()
	if len(modes) != 1 || modes[0] != "plan" {
		t.Fatalf("expected SetMode('plan') forwarded, got %v", modes)
	}
}

func TestAutoModeAllowsInTurnPermission(t *testing.T) {
	// Switching to auto mid-turn must auto-allow further can_use_tool requests
	// from the same turn — the CLI honors set_permission_mode only at the next
	// turn boundary, so without the manager mirroring the mode the user would
	// keep getting approval prompts even after flipping to auto.
	fs := newFakeSession()
	mgr, _, sess := setup(t, &fakeRunner{sess: fs})

	_, _ = mgr.SendMessage(sess.ID, "do it", nil)
	if _, err := mgr.SetMode(sess.ID, models.ModeAuto); err != nil {
		t.Fatalf("set mode: %v", err)
	}

	fs.emit(claude.Event{Kind: claude.KindPermission, RequestID: "req_1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)})
	fs.emit(claude.Event{Kind: claude.KindResult})
	fs.done()
	mgr.Wait()

	resp := fs.responses()
	if len(resp) != 1 || !resp[0].allow || resp[0].id != "req_1" {
		t.Fatalf("expected single allow for req_1, got %+v", resp)
	}
	if pending := mgr.PendingPermissions(sess.ID); len(pending) != 0 {
		t.Fatalf("expected no pending after auto-allow, got %+v", pending)
	}
}

func TestSetModeFlushesPendingRequests(t *testing.T) {
	// A permission request that arrived before the mode flip should also be
	// auto-resolved when the new mode allows it, and the session should go
	// back to processing (was awaiting_approval).
	fs := newFakeSession()
	mgr, st, sess := setup(t, &fakeRunner{sess: fs})

	_, _ = mgr.SendMessage(sess.ID, "do it", nil)
	fs.emit(claude.Event{Kind: claude.KindPermission, RequestID: "req_1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)})
	waitForStatus(t, st, sess.ID, models.StatusAwaitingApproval)

	if _, err := mgr.SetMode(sess.ID, models.ModeAuto); err != nil {
		t.Fatalf("set mode: %v", err)
	}
	waitForStatus(t, st, sess.ID, models.StatusProcessing)
	if pending := mgr.PendingPermissions(sess.ID); len(pending) != 0 {
		t.Fatalf("expected pending flushed, got %+v", pending)
	}

	resp := fs.responses()
	if len(resp) != 1 || !resp[0].allow || resp[0].id != "req_1" {
		t.Fatalf("expected allow forwarded for the flushed request, got %+v", resp)
	}

	fs.emit(claude.Event{Kind: claude.KindResult})
	fs.done()
	mgr.Wait()
}

func TestSetModeAcceptEditsOnlyAllowsEditTools(t *testing.T) {
	// acceptEdits should let edit tools through but still surface a Bash
	// request as a pending approval.
	fs := newFakeSession()
	mgr, st, sess := setup(t, &fakeRunner{sess: fs})

	_, _ = mgr.SendMessage(sess.ID, "do it", nil)
	if _, err := mgr.SetMode(sess.ID, models.ModeAcceptEdits); err != nil {
		t.Fatalf("set mode: %v", err)
	}

	fs.emit(claude.Event{Kind: claude.KindPermission, RequestID: "req_edit", ToolName: "Edit", ToolInput: json.RawMessage(`{"file_path":"/x"}`)})
	fs.emit(claude.Event{Kind: claude.KindPermission, RequestID: "req_bash", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)})
	waitForStatus(t, st, sess.ID, models.StatusAwaitingApproval)

	if pending := mgr.PendingPermissions(sess.ID); len(pending) != 1 || pending[0].ToolName != "Bash" {
		t.Fatalf("expected only Bash pending, got %+v", pending)
	}

	if err := mgr.RespondPermission(sess.ID, "req_bash", false, false, "no"); err != nil {
		t.Fatalf("respond: %v", err)
	}
	fs.done()
	mgr.Wait()

	resp := fs.responses()
	if len(resp) != 2 {
		t.Fatalf("expected 2 responses, got %+v", resp)
	}
	var sawAllowEdit, sawDenyBash bool
	for _, r := range resp {
		if r.id == "req_edit" && r.allow {
			sawAllowEdit = true
		}
		if r.id == "req_bash" && !r.allow {
			sawDenyBash = true
		}
	}
	if !sawAllowEdit || !sawDenyBash {
		t.Fatalf("unexpected responses: %+v", resp)
	}
}

func TestStartError(t *testing.T) {
	runner := &fakeRunner{startErr: errors.New("spawn failed")}
	mgr, st, sess := setup(t, runner)

	if _, err := mgr.SendMessage(sess.ID, "hi", nil); err != nil {
		t.Fatalf("send returned err too early: %v", err)
	}
	mgr.Wait()
	if got, _ := st.GetSession(sess.ID); got.Status != models.StatusError {
		t.Fatalf("expected error status, got %s", got.Status)
	}
}

func TestSendMessageUnknownSession(t *testing.T) {
	mgr, _, _ := setup(t, &fakeRunner{sess: newFakeSession()})
	if _, err := mgr.SendMessage("ghost", "hi", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSendMessageWithImages(t *testing.T) {
	fs := newFakeSession()
	mgr, st, sess := setup(t, &fakeRunner{sess: fs})

	imgs := []InboundImage{
		{Name: "image-1.png", MediaType: "image/png", Data: []byte{1, 2, 3}},
		{Name: "image-2.png", MediaType: "image/png", Data: []byte{4, 5}},
	}
	msg, err := mgr.SendMessage(sess.ID, "see image-1 and image-2", imgs)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	// Returned message carries persisted attachment metadata.
	if len(msg.Attachments) != 2 || msg.Attachments[0].Name != "image-1.png" {
		t.Fatalf("unexpected attachments on message: %+v", msg.Attachments)
	}
	// Persisted and re-hydrated by ListMessages.
	msgs, _ := st.ListMessages(sess.ID)
	if len(msgs[0].Attachments) != 2 {
		t.Fatalf("attachments not persisted: %+v", msgs[0])
	}
	// Forwarded to the runner with bytes intact.
	fs.mu.Lock()
	sentImgs := fs.sentImgs
	fs.mu.Unlock()
	if len(sentImgs) != 1 || len(sentImgs[0]) != 2 || sentImgs[0][0].Name != "image-1.png" {
		t.Fatalf("images not forwarded to runner: %+v", sentImgs)
	}

	fs.done()
	mgr.Wait()
}

func TestSendMessageAutoNamesBlankSession(t *testing.T) {
	fs := newFakeSession()
	mgr, st, sess := setup(t, &fakeRunner{sess: fs})
	blank, _ := st.CreateSession(sess.ProjectID, "", "claude-opus-4-7", models.ModeDefault, store.SessionInit{})

	if _, err := mgr.SendMessage(blank.ID, "Fix the login bug\nand other stuff", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got, _ := st.GetSession(blank.ID); got.Title != "Fix the login bug" {
		t.Fatalf("expected auto-name from first line, got %q", got.Title)
	}

	fs.done()
	mgr.Wait()
}

func TestSendMessageKeepsExistingTitle(t *testing.T) {
	fs := newFakeSession()
	mgr, st, sess := setup(t, &fakeRunner{sess: fs}) // sess title is "task"

	if _, err := mgr.SendMessage(sess.ID, "a different prompt", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got, _ := st.GetSession(sess.ID); got.Title != "task" {
		t.Fatalf("titled session should not be auto-renamed, got %q", got.Title)
	}

	fs.done()
	mgr.Wait()
}

func TestRenameTrimsAndPersists(t *testing.T) {
	mgr, st, sess := setup(t, &fakeRunner{sess: newFakeSession()})

	updated, err := mgr.Rename(sess.ID, "  New name  ")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if updated.Title != "New name" {
		t.Fatalf("expected trimmed title, got %q", updated.Title)
	}
	if got, _ := st.GetSession(sess.ID); got.Title != "New name" {
		t.Fatalf("rename not persisted, got %q", got.Title)
	}
}

func TestTitleFromMessage(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "a"
	}
	cases := map[string]string{
		"":                    "",
		"   \n\t ":            "",
		"hello":               "hello",
		"  first \n second  ": "first",
		long:                  long[:60] + "…",
	}
	for in, want := range cases {
		if got := titleFromMessage(in); got != want {
			t.Fatalf("titleFromMessage(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- worktree-isolation integration tests ---

// gitInitRepo creates a fresh local git repo with one commit and returns its
// path. The test is skipped if no git binary is on PATH.
func gitInitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping")
	}
	dir := t.TempDir()
	r := gitwt.NewExecRunner()
	ctx := context.Background()
	if _, err := r.Run(ctx, dir, "init", "-b", "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	mustRunGit(t, r, dir, "config", "user.email", "test@example.com")
	mustRunGit(t, r, dir, "config", "user.name", "ccdash test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustRunGit(t, r, dir, "add", "README.md")
	mustRunGit(t, r, dir, "commit", "-m", "init")
	return dir
}

func mustRunGit(t *testing.T, r gitwt.Runner, dir string, args ...string) {
	t.Helper()
	if _, err := r.Run(context.Background(), dir, args...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

func setupWithGit(t *testing.T) (*Manager, *store.Store, models.Project, string) {
	t.Helper()
	repo := gitInitRepo(t)
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	worktreeRoot := t.TempDir()
	mgr := NewWithGit(st, ws.NewHub(), &fakeRunner{sess: newFakeSession()}, gitwt.NewExecRunner(), worktreeRoot)
	p, _ := st.CreateProject("demo", repo)
	return mgr, st, p, worktreeRoot
}

func TestCreateSessionProvisionsWorktreeForGitProject(t *testing.T) {
	mgr, st, p, worktreeRoot := setupWithGit(t)

	sess, err := mgr.CreateSession(p.ID, "task", "m", models.ModeDefault)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if sess.WorktreePath == "" || sess.Branch == "" || sess.BaseCommit == "" {
		t.Fatalf("expected worktree fields populated: %+v", sess)
	}
	if filepath.Dir(sess.WorktreePath) != worktreeRoot {
		t.Fatalf("worktree not under root: %s", sess.WorktreePath)
	}
	if !strings.HasPrefix(sess.Branch, "ccdash/") {
		t.Fatalf("unexpected branch name: %s", sess.Branch)
	}
	if st, statErr := os.Stat(sess.WorktreePath); statErr != nil || !st.IsDir() {
		t.Fatalf("worktree dir missing: err=%v", statErr)
	}
	// Persisted on the row.
	got, _ := st.GetSession(sess.ID)
	if got.WorktreePath != sess.WorktreePath || got.Branch != sess.Branch {
		t.Fatalf("worktree fields not persisted: %+v", got)
	}
}

func TestCreateSessionNonGitProjectKeepsLegacyBehavior(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	plainDir := t.TempDir() // not a git repo
	mgr := NewWithGit(st, ws.NewHub(), &fakeRunner{sess: newFakeSession()}, gitwt.NewExecRunner(), t.TempDir())
	p, _ := st.CreateProject("plain", plainDir)

	sess, err := mgr.CreateSession(p.ID, "task", "m", models.ModeDefault)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if sess.WorktreePath != "" || sess.Branch != "" || sess.BaseCommit != "" {
		t.Fatalf("expected empty worktree fields for non-git project, got %+v", sess)
	}
}

func TestEnsureLiveUsesWorktreePath(t *testing.T) {
	mgr, _, p, _ := setupWithGit(t)
	// Swap in a fresh fake runner so we can inspect the cwd it received.
	fs := newFakeSession()
	runner := &fakeRunner{sess: fs}
	mgr.runner = runner

	sess, err := mgr.CreateSession(p.ID, "task", "m", models.ModeDefault)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := mgr.SendMessage(sess.ID, "hi", nil); err != nil {
		t.Fatalf("send: %v", err)
	}
	fs.done()
	mgr.Wait()

	if runner.lastReq.Cwd != sess.WorktreePath {
		t.Fatalf("expected cwd=%q (worktree), got %q", sess.WorktreePath, runner.lastReq.Cwd)
	}
}

func TestDeleteSessionRemovesWorktreeKeepsBranchByDefault(t *testing.T) {
	mgr, _, p, _ := setupWithGit(t)
	sess, err := mgr.CreateSession(p.ID, "task", "m", models.ModeDefault)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	wt := sess.WorktreePath
	branch := sess.Branch

	if err := mgr.DeleteSession(sess.ID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("expected worktree dir gone, got err=%v", err)
	}
	// Branch should still exist (default deleteBranch=false).
	repoRoot, _, _ := gitwt.IsRepo(context.Background(), gitwt.NewExecRunner(), p.Path)
	out, _ := gitwt.NewExecRunner().Run(context.Background(), repoRoot, "branch", "--list", branch)
	if !strings.Contains(string(out), branch) {
		t.Fatalf("expected branch %s preserved, got %q", branch, out)
	}
}

func TestDeleteSessionWithDeleteBranchTrueRemovesBoth(t *testing.T) {
	mgr, _, p, _ := setupWithGit(t)
	sess, err := mgr.CreateSession(p.ID, "task", "m", models.ModeDefault)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	branch := sess.Branch

	if err := mgr.DeleteSession(sess.ID, true); err != nil {
		t.Fatalf("delete: %v", err)
	}
	repoRoot, _, _ := gitwt.IsRepo(context.Background(), gitwt.NewExecRunner(), p.Path)
	out, _ := gitwt.NewExecRunner().Run(context.Background(), repoRoot, "branch", "--list", branch)
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("expected branch removed, still got %q", out)
	}
}

func TestDeleteProjectCleansUpWorktrees(t *testing.T) {
	mgr, st, p, _ := setupWithGit(t)
	s1, err := mgr.CreateSession(p.ID, "a", "m", models.ModeDefault)
	if err != nil {
		t.Fatalf("create s1: %v", err)
	}
	s2, err := mgr.CreateSession(p.ID, "b", "m", models.ModeDefault)
	if err != nil {
		t.Fatalf("create s2: %v", err)
	}

	if err := mgr.DeleteProject(p.ID); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if _, err := os.Stat(s1.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree 1 not cleaned: err=%v", err)
	}
	if _, err := os.Stat(s2.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("worktree 2 not cleaned: err=%v", err)
	}
	if _, err := st.GetProject(p.ID); err != store.ErrNotFound {
		t.Fatalf("expected project deleted, got %v", err)
	}
}
