// Package session orchestrates claude runs on top of the store and broadcasts
// live state over the ws hub. Each ccdash session owns a long-lived claude
// process (claude.Session) whose events are pumped in a dedicated goroutine, so
// multiple sessions stream and progress concurrently in the background. The
// manager also tracks pending tool-permission requests and answers them.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/robinmalmstrom/ccdash/backend/internal/claude"
	"github.com/robinmalmstrom/ccdash/backend/internal/commit"
	gitwt "github.com/robinmalmstrom/ccdash/backend/internal/git"
	"github.com/robinmalmstrom/ccdash/backend/internal/models"
	"github.com/robinmalmstrom/ccdash/backend/internal/store"
	"github.com/robinmalmstrom/ccdash/backend/internal/ws"
)

// Sentinel errors for the preview/accept flow. API handlers map these to
// specific status codes (400/409) instead of a generic 500.
var (
	// ErrPreviewAlreadyApplied means PreviewSession was called for a session
	// whose preview is already on disk.
	ErrPreviewAlreadyApplied = errors.New("preview already applied")
	// ErrPreviewNotApplied means Unpreview/Accept was called for a session
	// whose preview is not currently applied.
	ErrPreviewNotApplied = errors.New("preview not applied")
	// ErrAnotherPreviewApplied means another session in the same project owns
	// the currently applied preview; only one preview per project at a time.
	ErrAnotherPreviewApplied = errors.New("another preview is applied for this project")
	// ErrNoChanges means DiffRange returned an empty diff for the session's
	// branch — nothing to preview.
	ErrNoChanges = errors.New("no changes to preview")
)

// defaultBranch is the assumed mainline branch for DiffRange when computing a
// session's preview. Projects without a `main` branch are unusual enough that
// surfacing the git error to the user (rather than guessing) is the right
// behavior.
const defaultBranch = "main"

// Manager runs prompts and tracks in-flight runs and pending approvals.
type Manager struct {
	store  *store.Store
	hub    *ws.Hub
	runner claude.Runner

	git          gitwt.Runner            // wrapper around the git CLI; nil disables worktree isolation
	worktreeRoot string                  // base dir for per-session worktrees (e.g. $XDG_STATE_HOME/ccdash/worktrees)
	commitGen    commit.MessageGenerator // commit-message generator for Accept; nil falls back to commit.FallbackMessage

	mu   sync.Mutex
	live map[string]*liveSession // sessionID -> live process
	wg   sync.WaitGroup
}

// SetCommitGenerator overrides the commit-message generator used by
// AcceptSession. Pass nil to fall back to commit.FallbackMessage.
func (m *Manager) SetCommitGenerator(g commit.MessageGenerator) { m.commitGen = g }

// liveSession holds the per-session live process and its mutable run state.
type liveSession struct {
	id string
	cs claude.Session

	mu        sync.Mutex
	mode      models.PermissionMode  // current answering mode, mirrored for in-turn enforcement
	pending   map[string]pendingPerm // requestID -> request
	autoAllow map[string]bool        // toolName -> allow always (this session)
	thinking  strings.Builder
}

// modeAllowsTool mirrors claude's --permission-mode logic on our side so a
// mid-turn mode change takes effect on the in-flight turn: the CLI honors
// set_permission_mode only at the next turn boundary, so until then it keeps
// emitting can_use_tool for the running turn. Auto allows every tool;
// acceptEdits allows the file-editing tools; plan/default fall through to ask.
func modeAllowsTool(mode models.PermissionMode, toolName string) bool {
	switch mode {
	case models.ModeAuto:
		return true
	case models.ModeAcceptEdits:
		switch toolName {
		case "Write", "Edit", "MultiEdit", "NotebookEdit":
			return true
		}
	}
	return false
}

type pendingPerm struct {
	req   models.PermissionRequest
	input json.RawMessage
}

// New constructs a Manager without worktree isolation: sessions run in their
// project's path, mirroring legacy behavior.
func New(s *store.Store, h *ws.Hub, r claude.Runner) *Manager {
	return &Manager{store: s, hub: h, runner: r, live: make(map[string]*liveSession)}
}

// NewWithGit constructs a Manager that creates a per-session git worktree for
// any project whose path is inside a git repo. Worktrees are placed under
// worktreeRoot/<session-id>. Pass git=nil or worktreeRoot="" to disable
// isolation (equivalent to New).
func NewWithGit(s *store.Store, h *ws.Hub, r claude.Runner, git gitwt.Runner, worktreeRoot string) *Manager {
	return &Manager{
		store:        s,
		hub:          h,
		runner:       r,
		git:          git,
		worktreeRoot: worktreeRoot,
		live:         make(map[string]*liveSession),
	}
}

// InboundImage is a decoded image pasted onto a user turn.
type InboundImage struct {
	Name      string
	MediaType string
	Data      []byte
}

// SendMessage persists the user message (and any pasted images), ensures a live
// claude process exists, forwards the turn, and flips the session to processing.
// Assistant output, usage, and permission requests arrive asynchronously over
// the hub.
func (m *Manager) SendMessage(sessionID, content string, images []InboundImage) (models.Message, error) {
	sess, err := m.store.GetSession(sessionID)
	if err != nil {
		return models.Message{}, err
	}

	msg, err := m.store.AddMessage(sessionID, "user", content)
	if err != nil {
		return models.Message{}, err
	}
	// Persist pasted images as attachments and collect them for the runner.
	var runnerImages []claude.Image
	for _, img := range images {
		att, aerr := m.store.AddAttachment(msg.ID, sessionID, img.Name, img.MediaType, img.Data)
		if aerr != nil {
			return models.Message{}, aerr
		}
		msg.Attachments = append(msg.Attachments, att)
		runnerImages = append(runnerImages, claude.Image{
			Name:      img.Name,
			MediaType: img.MediaType,
			Data:      img.Data,
		})
	}
	m.hub.Broadcast("session.message", msg)

	// Auto-name from the first user message. This only fires while the title is
	// still blank, so a manual rename (which sets a non-empty title) is never
	// clobbered by later turns.
	if strings.TrimSpace(sess.Title) == "" {
		if title := titleFromMessage(content); title != "" {
			if uerr := m.store.UpdateSessionTitle(sessionID, title); uerr == nil {
				if updated, gerr := m.store.GetSession(sessionID); gerr == nil {
					sess = updated
					m.hub.Broadcast("session.status", updated)
				}
			}
		}
	}

	ls, err := m.ensureLive(sess)
	if err != nil {
		m.fail(&sess, err)
		return msg, nil
	}
	if err := ls.cs.Send(content, runnerImages); err != nil {
		m.fail(&sess, err)
		return msg, nil
	}
	m.setStatus(&sess, models.StatusProcessing)
	return msg, nil
}

// RespondPermission answers a pending tool-permission request. allow approves
// it; always (with allow) auto-approves the same tool for the rest of the
// session; message is shown to claude on deny. answers carries user-supplied
// values for tools whose result is collected through the permission dialog
// (notably AskUserQuestion) — when non-empty and allowed, they are merged into
// the tool's updatedInput so the SDK forwards them to the model.
func (m *Manager) RespondPermission(sessionID, requestID string, allow, always bool, message string, answers map[string]string) error {
	m.mu.Lock()
	ls := m.live[sessionID]
	m.mu.Unlock()
	if ls == nil {
		return store.ErrNotFound
	}

	ls.mu.Lock()
	pp, ok := ls.pending[requestID]
	if !ok {
		ls.mu.Unlock()
		return store.ErrNotFound
	}
	delete(ls.pending, requestID)
	if always && allow {
		ls.autoAllow[pp.req.ToolName] = true
	}
	remaining := len(ls.pending)
	ls.mu.Unlock()

	dec := claude.DecisionDeny
	input := pp.input
	if allow {
		dec = claude.DecisionAllow
		if len(answers) > 0 {
			input = mergeAnswers(pp.input, answers)
		}
	}
	if err := ls.cs.Respond(requestID, dec, input, message); err != nil {
		return err
	}

	resolved := "deny"
	if allow && always {
		resolved = "allow_always"
	} else if allow {
		resolved = "allow"
	}
	m.hub.Broadcast("session.permission_resolved", map[string]string{
		"sessionId": sessionID,
		"requestId": requestID,
		"decision":  resolved,
	})

	// If nothing else is pending, the run resumes.
	if remaining == 0 {
		if sess, err := m.store.GetSession(sessionID); err == nil {
			m.setStatus(&sess, models.StatusProcessing)
		}
	}
	return nil
}

// PendingPermissions returns the currently pending requests for a session.
func (m *Manager) PendingPermissions(sessionID string) []models.PermissionRequest {
	m.mu.Lock()
	ls := m.live[sessionID]
	m.mu.Unlock()
	out := []models.PermissionRequest{}
	if ls == nil {
		return out
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	for _, pp := range ls.pending {
		out = append(out, pp.req)
	}
	return out
}

// SetMode changes the answering mode, persisting it and applying it to a live
// process if one exists. The CLI honors set_permission_mode only on the next
// turn, so we also mirror the mode on our side: any pending permission requests
// the new mode auto-allows are resolved immediately, and handlePermission
// auto-allows further in-turn requests under the same rule.
func (m *Manager) SetMode(sessionID string, mode models.PermissionMode) (models.Session, error) {
	if err := m.store.UpdateSessionMode(sessionID, mode); err != nil {
		return models.Session{}, err
	}
	m.mu.Lock()
	ls := m.live[sessionID]
	m.mu.Unlock()

	var resumeRun bool
	if ls != nil {
		ls.mu.Lock()
		ls.mode = mode
		// Snapshot pending requests the new mode allows, drop them from pending,
		// and remember whether the queue is now empty so we can resume the turn.
		var flushed []pendingPerm
		for id, pp := range ls.pending {
			if modeAllowsTool(mode, pp.req.ToolName) {
				flushed = append(flushed, pp)
				delete(ls.pending, id)
			}
		}
		resumeRun = len(flushed) > 0 && len(ls.pending) == 0
		ls.mu.Unlock()

		_ = ls.cs.SetMode(mode.CLIPermissionMode())

		// Respond + broadcast outside the lock so a slow ws subscriber can't
		// block the manager.
		for _, pp := range flushed {
			_ = ls.cs.Respond(pp.req.ID, claude.DecisionAllow, pp.input, "")
			m.hub.Broadcast("session.permission_resolved", map[string]string{
				"sessionId": sessionID,
				"requestId": pp.req.ID,
				"decision":  "allow",
			})
		}
	}

	sess, err := m.store.GetSession(sessionID)
	if err != nil {
		return models.Session{}, err
	}
	if resumeRun {
		m.setStatus(&sess, models.StatusProcessing)
	} else {
		m.hub.Broadcast("session.status", sess)
	}
	return sess, nil
}

// Rename sets a session's title explicitly (manual rename). The title is
// trimmed; callers should reject empty titles before reaching here.
func (m *Manager) Rename(sessionID, title string) (models.Session, error) {
	if err := m.store.UpdateSessionTitle(sessionID, strings.TrimSpace(title)); err != nil {
		return models.Session{}, err
	}
	sess, err := m.store.GetSession(sessionID)
	if err != nil {
		return models.Session{}, err
	}
	m.hub.Broadcast("session.status", sess)
	return sess, nil
}

// CreateSession provisions a new session row, allocating a git worktree first
// when the project is in a git repo (and the manager was built with git
// support). For non-git projects (or when git support is disabled) the session
// runs in the project path directly and the worktree fields stay empty.
//
// Failure ordering matters: the worktree is created before the row is inserted
// so a half-provisioned session row can never point at a missing worktree.
func (m *Manager) CreateSession(projectID, title, model string, mode models.PermissionMode) (models.Session, error) {
	project, err := m.store.GetProject(projectID)
	if err != nil {
		return models.Session{}, err
	}

	init := store.SessionInit{ID: uuid.NewString()}
	cleanup := func() {} // run on later failure to roll back worktree creation

	if m.git != nil && m.worktreeRoot != "" && project.GitMode != models.GitModeDefault {
		ctx := context.Background()
		repoRoot, ok, repoErr := gitwt.IsRepo(ctx, m.git, project.Path)
		if repoErr != nil {
			return models.Session{}, fmt.Errorf("check git repo: %w", repoErr)
		}
		if ok {
			base, headErr := gitwt.HeadCommit(ctx, m.git, repoRoot)
			if headErr != nil {
				return models.Session{}, fmt.Errorf("read HEAD: %w", headErr)
			}
			branch := "ccdash/" + init.ID[:8]
			dest := filepath.Join(m.worktreeRoot, init.ID)
			if mkErr := os.MkdirAll(m.worktreeRoot, 0o755); mkErr != nil {
				return models.Session{}, fmt.Errorf("ensure worktree root: %w", mkErr)
			}
			if addErr := gitwt.AddWorktree(ctx, m.git, repoRoot, dest, branch, base); addErr != nil {
				return models.Session{}, fmt.Errorf("create worktree: %w", addErr)
			}
			init.WorktreePath = dest
			init.Branch = branch
			init.BaseCommit = base
			cleanup = func() {
				_ = gitwt.RemoveWorktree(context.Background(), m.git, repoRoot, dest, true)
				_ = gitwt.DeleteBranch(context.Background(), m.git, repoRoot, branch, true)
			}
		}
	}

	sess, err := m.store.CreateSession(projectID, title, model, mode, init)
	if err != nil {
		cleanup()
		return models.Session{}, err
	}
	return sess, nil
}

// DeleteSession terminates any live process, removes the session's worktree if
// one was provisioned, optionally deletes the branch, then removes the row.
// Missing worktrees / branches are tolerated so manual cleanup outside ccdash
// doesn't wedge the API.
func (m *Manager) DeleteSession(sessionID string, deleteBranch bool) error {
	sess, err := m.store.GetSession(sessionID)
	if err != nil {
		return err
	}
	_ = m.Stop(sessionID) // also returns to idle if not running; safe to ignore err

	if sess.WorktreePath != "" && m.git != nil {
		project, perr := m.store.GetProject(sess.ProjectID)
		if perr == nil {
			ctx := context.Background()
			if repoRoot, ok, _ := gitwt.IsRepo(ctx, m.git, project.Path); ok {
				if rmErr := gitwt.RemoveWorktree(ctx, m.git, repoRoot, sess.WorktreePath, true); rmErr != nil {
					// Best-effort: log via hub? Silent for now — DeleteSession should
					// still proceed so the row doesn't outlive the worktree.
					_ = rmErr
				}
				if deleteBranch && sess.Branch != "" {
					_ = gitwt.DeleteBranch(ctx, m.git, repoRoot, sess.Branch, true)
				}
			}
		}
	}

	if err := m.store.DeleteSession(sessionID); err != nil {
		return err
	}
	m.hub.Broadcast("session.deleted", sess)
	return nil
}

// DeleteProject cleans up worktrees for every session under the project, then
// deletes the project row (FK cascades remove the session rows).
func (m *Manager) DeleteProject(projectID string) error {
	project, err := m.store.GetProject(projectID)
	if err != nil {
		return err
	}

	sessions, err := m.store.ListProjectSessions(projectID)
	if err != nil {
		return err
	}

	var repoRoot string
	var haveRoot bool
	if m.git != nil {
		ctx := context.Background()
		root, ok, _ := gitwt.IsRepo(ctx, m.git, project.Path)
		repoRoot = root
		haveRoot = ok
	}

	for _, sess := range sessions {
		_ = m.Stop(sess.ID)
		if sess.WorktreePath != "" && haveRoot {
			ctx := context.Background()
			_ = gitwt.RemoveWorktree(ctx, m.git, repoRoot, sess.WorktreePath, true)
			// Leave the branch by default; project-level cleanup deleting all
			// branches risks losing user work that may already be merged or
			// pushed elsewhere.
		}
	}

	if err := m.store.DeleteProject(projectID); err != nil {
		return err
	}
	m.hub.Broadcast("project.deleted", project)
	return nil
}

// Stop terminates a session's live process and returns it to idle, ready for the
// next prompt (a fresh process is started on the next Send).
func (m *Manager) Stop(sessionID string) error {
	sess, err := m.store.GetSession(sessionID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	ls := m.live[sessionID]
	m.mu.Unlock()
	if ls != nil {
		_ = ls.cs.Close()
	}
	m.setStatus(&sess, models.StatusIdle)
	return nil
}

// Running reports whether a session currently has a live process.
func (m *Manager) Running(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.live[sessionID]
	return ok
}

// Wait blocks until all live processes finish. Intended for tests and shutdown.
func (m *Manager) Wait() { m.wg.Wait() }

func (m *Manager) ensureLive(sess models.Session) (*liveSession, error) {
	m.mu.Lock()
	if ls, ok := m.live[sess.ID]; ok {
		m.mu.Unlock()
		return ls, nil
	}
	m.mu.Unlock()

	project, err := m.store.GetProject(sess.ProjectID)
	if err != nil {
		return nil, err
	}
	cwd := project.Path
	if sess.WorktreePath != "" {
		cwd = sess.WorktreePath
	}
	cs, err := m.runner.Start(context.Background(), claude.StartRequest{
		Cwd:             cwd,
		Model:           sess.Model,
		ResumeSessionID: sess.ClaudeSessionID,
		PermissionMode:  sess.PermissionMode.CLIPermissionMode(),
	})
	if err != nil {
		return nil, err
	}
	ls := &liveSession{
		id:        sess.ID,
		cs:        cs,
		mode:      sess.PermissionMode,
		pending:   make(map[string]pendingPerm),
		autoAllow: make(map[string]bool),
	}
	m.mu.Lock()
	m.live[sess.ID] = ls
	m.mu.Unlock()

	m.wg.Add(1)
	go m.pump(ls)
	return ls, nil
}

func (m *Manager) pump(ls *liveSession) {
	defer m.wg.Done()
	defer func() {
		m.mu.Lock()
		delete(m.live, ls.id)
		m.mu.Unlock()
	}()

	for ev := range ls.cs.Events() {
		sess, err := m.store.GetSession(ls.id)
		if err != nil {
			continue
		}
		switch ev.Kind {
		case claude.KindSystem:
			m.captureClaudeID(&sess, ev.ClaudeSessionID)

		case claude.KindText:
			m.hub.Broadcast("session.delta", delta(ls.id, "text", ev.Text))

		case claude.KindThinking:
			ls.mu.Lock()
			ls.thinking.WriteString(ev.Text)
			ls.mu.Unlock()
			m.hub.Broadcast("session.delta", delta(ls.id, "thinking", ev.Text))

		case claude.KindToolUse:
			m.flushThinking(ls)
			line := summarize(ev.ToolName, ev.ToolInput)
			if msg, aerr := m.store.AddMessage(ls.id, "tool", line); aerr == nil {
				m.hub.Broadcast("session.message", msg)
			}

		case claude.KindAssistant:
			m.flushThinking(ls)
			if msg, aerr := m.store.AddMessage(ls.id, "assistant", ev.Text); aerr == nil {
				m.hub.Broadcast("session.message", msg)
			}

		case claude.KindPermission:
			m.handlePermission(ls, &sess, ev)

		case claude.KindResult:
			m.flushThinking(ls)
			if rec, uerr := m.store.AddUsage(ls.id, pick(ev.Model, sess.Model), ev.InputTokens, ev.OutputTokens, ev.CostUSD); uerr == nil {
				m.hub.Broadcast("session.usage", rec)
			}
			// Turn finished with nothing pending: back to idle and ready for the
			// next prompt. (A turn that needs an answer pauses on awaiting_approval
			// instead and never reaches a result here.)
			m.setStatus(&sess, models.StatusIdle)

		case claude.KindError:
			m.fail(&sess, ev.Err)
		}
	}
}

func (m *Manager) handlePermission(ls *liveSession, sess *models.Session, ev claude.Event) {
	ls.mu.Lock()
	auto := ls.autoAllow[ev.ToolName] || modeAllowsTool(ls.mode, ev.ToolName)
	ls.mu.Unlock()

	if auto {
		_ = ls.cs.Respond(ev.RequestID, claude.DecisionAllow, ev.ToolInput, "")
		return
	}

	req := models.PermissionRequest{
		ID:          ev.RequestID,
		SessionID:   ls.id,
		ToolName:    ev.ToolName,
		Input:       ev.ToolInput,
		Summary:     summarize(ev.ToolName, ev.ToolInput),
		Suggestions: ev.Suggestions,
		CreatedAt:   time.Now().UTC(),
	}
	if len(req.Suggestions) == 0 {
		req.Suggestions = []string{"allow", "allow_always", "deny"}
	}
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	ls.mu.Lock()
	ls.pending[req.ID] = pendingPerm{req: req, input: ev.ToolInput}
	ls.mu.Unlock()

	m.hub.Broadcast("session.permission", req)
	m.setStatus(sess, models.StatusAwaitingApproval)
}

func (m *Manager) flushThinking(ls *liveSession) {
	ls.mu.Lock()
	text := strings.TrimSpace(ls.thinking.String())
	ls.thinking.Reset()
	ls.mu.Unlock()
	if text == "" {
		return
	}
	if msg, err := m.store.AddMessage(ls.id, "thinking", text); err == nil {
		m.hub.Broadcast("session.message", msg)
	}
}

func (m *Manager) captureClaudeID(sess *models.Session, claudeID string) {
	if claudeID == "" || sess.ClaudeSessionID == claudeID {
		return
	}
	sess.ClaudeSessionID = claudeID
	_ = m.store.UpdateSessionClaudeID(sess.ID, claudeID)
}

func (m *Manager) setStatus(sess *models.Session, status models.SessionStatus) {
	if err := m.store.UpdateSessionStatus(sess.ID, status); err != nil {
		return
	}
	if updated, err := m.store.GetSession(sess.ID); err == nil {
		*sess = updated
		m.hub.Broadcast("session.status", updated)
	}
}

func (m *Manager) fail(sess *models.Session, cause error) {
	if cause != nil {
		if msg, err := m.store.AddMessage(sess.ID, "system", "run error: "+cause.Error()); err == nil {
			m.hub.Broadcast("session.message", msg)
		}
	}
	m.setStatus(sess, models.StatusError)
}

func delta(sessionID, kind, text string) map[string]string {
	return map[string]string{"sessionId": sessionID, "kind": kind, "text": text}
}

func pick(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// summarize builds a short human-readable line for a tool call, e.g.
// "Bash: git status" or "Edit: internal/api/api.go".
func summarize(tool string, input json.RawMessage) string {
	if len(input) == 0 {
		return tool
	}
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return tool
	}
	for _, key := range []string{"command", "file_path", "path", "pattern", "url", "description", "prompt"} {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return fmt.Sprintf("%s: %s", tool, truncate(s, 120))
			}
		}
	}
	return tool
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// mergeAnswers folds user-supplied answers into a tool's original input under
// the "answers" key. Used to ship AskUserQuestion responses back to the SDK via
// the can_use_tool channel: when behavior=allow's updatedInput carries
// {questions, answers: {<question>: <selected label>}}, the SDK forwards those
// answers to the model as the tool result. The original input shape is
// preserved; if it is not a JSON object we fall back to raw {"answers": …} so
// the answer is at least present (even though the model will see a partial
// input).
func mergeAnswers(original json.RawMessage, answers map[string]string) json.RawMessage {
	var obj map[string]any
	if len(original) > 0 {
		if err := json.Unmarshal(original, &obj); err != nil {
			obj = nil
		}
	}
	if obj == nil {
		obj = map[string]any{}
	}
	obj["answers"] = answers
	out, err := json.Marshal(obj)
	if err != nil {
		return original
	}
	return out
}

// PreviewSession applies the worktree's diff (computed against the project's
// default branch) onto the project's main checkout as uncommitted edits so the
// user can rebuild and try the change live before deciding whether to keep it.
// Errors:
//   - ErrNotFound if the session does not exist;
//   - "worktree isolation disabled" if the manager has no git runner;
//   - "session has no worktree" for non-worktree sessions;
//   - "preview already applied" if this session's preview is already on disk;
//   - "another preview is applied for this project" (409) if a sibling
//     session in the same project owns the active preview;
//   - "no changes to preview" when the diff is empty;
//   - the git error otherwise (likely conflicts).
func (m *Manager) PreviewSession(id string) (*models.Session, error) {
	sess, err := m.store.GetSession(id)
	if err != nil {
		return nil, err
	}
	if m.git == nil {
		return nil, fmt.Errorf("worktree isolation disabled")
	}
	if sess.WorktreePath == "" || sess.Branch == "" {
		return nil, fmt.Errorf("session has no worktree")
	}
	if sess.PreviewState == "applied" {
		return nil, ErrPreviewAlreadyApplied
	}

	project, err := m.store.GetProject(sess.ProjectID)
	if err != nil {
		return nil, err
	}

	n, err := m.store.CountAppliedPreviews(sess.ProjectID)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, ErrAnotherPreviewApplied
	}

	ctx := context.Background()
	patch, err := gitwt.DiffRange(ctx, m.git, project.Path, defaultBranch, sess.Branch)
	if err != nil {
		return nil, fmt.Errorf("diff worktree: %w", err)
	}
	if patch == nil {
		return nil, ErrNoChanges
	}
	if err := gitwt.ApplyPatch(ctx, m.git, project.Path, patch); err != nil {
		return nil, err
	}
	if err := m.store.SetSessionPreview(id, "applied", patch); err != nil {
		// Best-effort rollback so the working tree doesn't diverge from our state.
		_ = gitwt.ReversePatch(ctx, m.git, project.Path, patch)
		return nil, err
	}
	updated, err := m.store.GetSession(id)
	if err != nil {
		return nil, err
	}
	m.hub.Broadcast("session.status", updated)
	return &updated, nil
}

// UnpreviewSession reverses an applied preview: the stored patch is reverse-
// applied to the project's main checkout and the preview fields are cleared.
func (m *Manager) UnpreviewSession(id string) (*models.Session, error) {
	sess, err := m.store.GetSession(id)
	if err != nil {
		return nil, err
	}
	if sess.PreviewState != "applied" {
		return nil, ErrPreviewNotApplied
	}
	if m.git == nil {
		return nil, fmt.Errorf("worktree isolation disabled")
	}
	project, err := m.store.GetProject(sess.ProjectID)
	if err != nil {
		return nil, err
	}
	patch, err := m.store.GetSessionPreview(id)
	if err != nil {
		return nil, err
	}
	if len(patch) == 0 {
		return nil, fmt.Errorf("preview state inconsistent: no stored patch")
	}
	ctx := context.Background()
	if err := gitwt.ReversePatch(ctx, m.git, project.Path, patch); err != nil {
		return nil, err
	}
	if err := m.store.SetSessionPreview(id, "", nil); err != nil {
		return nil, err
	}
	updated, err := m.store.GetSession(id)
	if err != nil {
		return nil, err
	}
	m.hub.Broadcast("session.status", updated)
	return &updated, nil
}

// AcceptSession finalizes a previewed session: it generates a commit message
// from the stored patch, commits the applied changes into the project's main
// checkout, removes the session's worktree (and its branch), clears the
// session's worktree/preview fields, and marks it done.
func (m *Manager) AcceptSession(id string) (*models.Session, error) {
	sess, err := m.store.GetSession(id)
	if err != nil {
		return nil, err
	}
	if sess.PreviewState != "applied" {
		return nil, ErrPreviewNotApplied
	}
	if m.git == nil {
		return nil, fmt.Errorf("worktree isolation disabled")
	}
	project, err := m.store.GetProject(sess.ProjectID)
	if err != nil {
		return nil, err
	}
	patch, err := m.store.GetSessionPreview(id)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	msg := commit.GenerateOrFallback(ctx, m.commitGen, patch)
	if err := gitwt.CommitAll(ctx, m.git, project.Path, msg); err != nil {
		return nil, err
	}

	if sess.WorktreePath != "" {
		if repoRoot, ok, _ := gitwt.IsRepo(ctx, m.git, project.Path); ok {
			_ = gitwt.RemoveWorktree(ctx, m.git, repoRoot, sess.WorktreePath, true)
			if sess.Branch != "" {
				_ = gitwt.DeleteBranch(ctx, m.git, repoRoot, sess.Branch, true)
			}
		}
	}

	if err := m.store.SetSessionPreview(id, "", nil); err != nil {
		return nil, err
	}
	if err := m.store.ClearSessionWorktree(id); err != nil {
		return nil, err
	}
	if err := m.store.UpdateSessionStatus(id, models.StatusDone); err != nil {
		return nil, err
	}

	updated, err := m.store.GetSession(id)
	if err != nil {
		return nil, err
	}
	m.hub.Broadcast("session.status", updated)
	return &updated, nil
}

// titleFromMessage derives a session title from a user message: its first
// non-empty line, rune-safely truncated. Returns "" if there is no text.
func titleFromMessage(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		r := []rune(line)
		if len(r) > 60 {
			return string(r[:60]) + "…"
		}
		return line
	}
	return ""
}
