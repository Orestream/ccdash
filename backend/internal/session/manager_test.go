package session

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/robinmalmstrom/ccdash/backend/internal/claude"
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

func (f *fakeSession) Send(text string) error {
	f.mu.Lock()
	f.sent = append(f.sent, text)
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
	sess, _ := st.CreateSession(p.ID, "task", "claude-opus-4-7", models.ModeDefault)
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

	if _, err := mgr.SendMessage(sess.ID, "hello"); err != nil {
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
	if got.Status != models.StatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %s", got.Status)
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

func TestThinkingPersisted(t *testing.T) {
	fs := newFakeSession()
	mgr, st, sess := setup(t, &fakeRunner{sess: fs})

	_, _ = mgr.SendMessage(sess.ID, "hi")
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

	_, _ = mgr.SendMessage(sess.ID, "do it")
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
	if got, _ := st.GetSession(sess.ID); got.Status != models.StatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %s", got.Status)
	}
}

func TestPermissionDeny(t *testing.T) {
	fs := newFakeSession()
	mgr, st, sess := setup(t, &fakeRunner{sess: fs})

	_, _ = mgr.SendMessage(sess.ID, "do it")
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
	_, _ = mgr.SendMessage(sess.ID, "x")
	if err := mgr.RespondPermission(sess.ID, "ghost", true, false, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	fs.done()
	mgr.Wait()
}

func TestSetModeUpdatesLiveSession(t *testing.T) {
	fs := newFakeSession()
	mgr, st, sess := setup(t, &fakeRunner{sess: fs})

	_, _ = mgr.SendMessage(sess.ID, "x")
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

func TestStartError(t *testing.T) {
	runner := &fakeRunner{startErr: errors.New("spawn failed")}
	mgr, st, sess := setup(t, runner)

	if _, err := mgr.SendMessage(sess.ID, "hi"); err != nil {
		t.Fatalf("send returned err too early: %v", err)
	}
	mgr.Wait()
	if got, _ := st.GetSession(sess.ID); got.Status != models.StatusError {
		t.Fatalf("expected error status, got %s", got.Status)
	}
}

func TestSendMessageUnknownSession(t *testing.T) {
	mgr, _, _ := setup(t, &fakeRunner{sess: newFakeSession()})
	if _, err := mgr.SendMessage("ghost", "hi"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
