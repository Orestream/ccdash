// Package session orchestrates claude runs on top of the store and broadcasts
// live state over the ws hub. Each ccdash session owns a long-lived claude
// process (claude.Session) whose events are pumped in a dedicated goroutine, so
// multiple sessions stream and progress concurrently in the background. The
// manager also tracks pending tool-permission requests and answers them.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/robinmalmstrom/ccdash/backend/internal/claude"
	"github.com/robinmalmstrom/ccdash/backend/internal/models"
	"github.com/robinmalmstrom/ccdash/backend/internal/store"
	"github.com/robinmalmstrom/ccdash/backend/internal/ws"
)

// Manager runs prompts and tracks in-flight runs and pending approvals.
type Manager struct {
	store  *store.Store
	hub    *ws.Hub
	runner claude.Runner

	mu   sync.Mutex
	live map[string]*liveSession // sessionID -> live process
	wg   sync.WaitGroup
}

// liveSession holds the per-session live process and its mutable run state.
type liveSession struct {
	id string
	cs claude.Session

	mu        sync.Mutex
	pending   map[string]pendingPerm // requestID -> request
	autoAllow map[string]bool        // toolName -> allow always (this session)
	thinking  strings.Builder
}

type pendingPerm struct {
	req   models.PermissionRequest
	input json.RawMessage
}

// New constructs a Manager.
func New(s *store.Store, h *ws.Hub, r claude.Runner) *Manager {
	return &Manager{store: s, hub: h, runner: r, live: make(map[string]*liveSession)}
}

// SendMessage persists the user message, ensures a live claude process exists,
// forwards the turn, and flips the session to processing. Assistant output,
// usage, and permission requests arrive asynchronously over the hub.
func (m *Manager) SendMessage(sessionID, content string) (models.Message, error) {
	sess, err := m.store.GetSession(sessionID)
	if err != nil {
		return models.Message{}, err
	}

	msg, err := m.store.AddMessage(sessionID, "user", content)
	if err != nil {
		return models.Message{}, err
	}
	m.hub.Broadcast("session.message", msg)

	ls, err := m.ensureLive(sess)
	if err != nil {
		m.fail(&sess, err)
		return msg, nil
	}
	if err := ls.cs.Send(content); err != nil {
		m.fail(&sess, err)
		return msg, nil
	}
	m.setStatus(&sess, models.StatusProcessing)
	return msg, nil
}

// RespondPermission answers a pending tool-permission request. allow approves
// it; always (with allow) auto-approves the same tool for the rest of the
// session; message is shown to claude on deny.
func (m *Manager) RespondPermission(sessionID, requestID string, allow, always bool, message string) error {
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
	if allow {
		dec = claude.DecisionAllow
	}
	if err := ls.cs.Respond(requestID, dec, pp.input, message); err != nil {
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
// process if one exists (best-effort).
func (m *Manager) SetMode(sessionID string, mode models.PermissionMode) (models.Session, error) {
	if err := m.store.UpdateSessionMode(sessionID, mode); err != nil {
		return models.Session{}, err
	}
	m.mu.Lock()
	ls := m.live[sessionID]
	m.mu.Unlock()
	if ls != nil {
		_ = ls.cs.SetMode(mode.CLIPermissionMode())
	}
	sess, err := m.store.GetSession(sessionID)
	if err != nil {
		return models.Session{}, err
	}
	m.hub.Broadcast("session.status", sess)
	return sess, nil
}

// Stop terminates a session's live process and returns it to awaiting_input.
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
	m.setStatus(&sess, models.StatusAwaitingInput)
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
	cs, err := m.runner.Start(context.Background(), claude.StartRequest{
		Cwd:             project.Path,
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
			m.setStatus(&sess, models.StatusAwaitingInput)

		case claude.KindError:
			m.fail(&sess, ev.Err)
		}
	}
}

func (m *Manager) handlePermission(ls *liveSession, sess *models.Session, ev claude.Event) {
	ls.mu.Lock()
	auto := ls.autoAllow[ev.ToolName]
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
