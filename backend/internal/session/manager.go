// Package session orchestrates claude runs on top of the store and broadcasts
// live state over the ws hub. Each run executes in its own goroutine so multiple
// sessions progress concurrently and keep running in the background regardless
// of which session the UI is currently viewing.
package session

import (
	"context"
	"strings"
	"sync"

	"github.com/robinmalmstrom/ccdash/backend/internal/claude"
	"github.com/robinmalmstrom/ccdash/backend/internal/models"
	"github.com/robinmalmstrom/ccdash/backend/internal/store"
	"github.com/robinmalmstrom/ccdash/backend/internal/ws"
)

// Manager runs prompts and tracks in-flight runs so they can be cancelled.
type Manager struct {
	store  *store.Store
	hub    *ws.Hub
	runner claude.Runner

	mu      sync.Mutex
	cancels map[string]context.CancelFunc // sessionID -> cancel for active run

	// done is closed-per-session signalling, used by tests to await completion.
	wg sync.WaitGroup
}

// New constructs a Manager.
func New(s *store.Store, h *ws.Hub, r claude.Runner) *Manager {
	return &Manager{
		store:   s,
		hub:     h,
		runner:  r,
		cancels: make(map[string]context.CancelFunc),
	}
}

// SendMessage persists the user message, flips the session to processing and
// launches the claude run in the background. It returns the stored user message
// immediately; assistant output and usage arrive asynchronously via the hub.
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
	m.setStatus(&sess, models.StatusProcessing)

	m.wg.Add(1)
	go m.run(sess, content)
	return msg, nil
}

// Stop cancels an in-flight run for a session, if any.
func (m *Manager) Stop(sessionID string) error {
	if _, err := m.store.GetSession(sessionID); err != nil {
		return err
	}
	m.mu.Lock()
	cancel, ok := m.cancels[sessionID]
	m.mu.Unlock()
	if ok {
		cancel()
	}
	return nil
}

// Running reports whether a session currently has an active run.
func (m *Manager) Running(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.cancels[sessionID]
	return ok
}

// Wait blocks until all in-flight runs finish. Intended for tests and shutdown.
func (m *Manager) Wait() { m.wg.Wait() }

func (m *Manager) run(sess models.Session, prompt string) {
	defer m.wg.Done()

	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancels[sess.ID] = cancel
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.cancels, sess.ID)
		m.mu.Unlock()
		cancel()
	}()

	project, err := m.store.GetProject(sess.ProjectID)
	if err != nil {
		m.fail(&sess, err)
		return
	}

	events, err := m.runner.Run(ctx, claude.RunRequest{
		Prompt:          prompt,
		Cwd:             project.Path,
		Model:           sess.Model,
		ResumeSessionID: sess.ClaudeSessionID,
	})
	if err != nil {
		m.fail(&sess, err)
		return
	}

	var assistant strings.Builder
	usageModel := sess.Model
	for ev := range events {
		switch ev.Kind {
		case claude.KindSystem:
			m.captureClaudeID(&sess, ev.ClaudeSessionID)
		case claude.KindAssistant:
			assistant.WriteString(ev.Text)
			m.captureClaudeID(&sess, ev.ClaudeSessionID)
			if ev.Model != "" {
				usageModel = ev.Model
			}
		case claude.KindResult:
			m.captureClaudeID(&sess, ev.ClaudeSessionID)
			if ev.Model != "" {
				usageModel = ev.Model
			}
			if rec, uerr := m.store.AddUsage(sess.ID, usageModel, ev.InputTokens, ev.OutputTokens, ev.CostUSD); uerr == nil {
				m.hub.Broadcast("session.usage", rec)
			}
		case claude.KindError:
			m.fail(&sess, ev.Err)
			return
		}
	}

	if text := strings.TrimSpace(assistant.String()); text != "" {
		if full, aerr := m.store.AddMessage(sess.ID, "assistant", text); aerr == nil {
			m.hub.Broadcast("session.message", full)
		}
	}

	// A cancelled run returns to awaiting_input rather than erroring.
	m.setStatus(&sess, models.StatusAwaitingInput)
}

func (m *Manager) captureClaudeID(sess *models.Session, claudeID string) {
	if claudeID == "" || sess.ClaudeSessionID == claudeID {
		return
	}
	sess.ClaudeSessionID = claudeID
	_ = m.store.UpdateSessionClaudeID(sess.ID, claudeID)
}

func (m *Manager) setStatus(sess *models.Session, status models.SessionStatus) {
	sess.Status = status
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
		_, _ = m.store.AddMessage(sess.ID, "system", "run error: "+cause.Error())
	}
	m.setStatus(sess, models.StatusError)
}
