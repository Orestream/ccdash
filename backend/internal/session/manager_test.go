package session

import (
	"context"
	"errors"
	"testing"

	"github.com/robinmalmstrom/ccdash/backend/internal/claude"
	"github.com/robinmalmstrom/ccdash/backend/internal/models"
	"github.com/robinmalmstrom/ccdash/backend/internal/store"
	"github.com/robinmalmstrom/ccdash/backend/internal/ws"
)

// fakeRunner emits a fixed sequence of events, then closes the channel.
type fakeRunner struct {
	events   []claude.Event
	startErr error
	lastReq  claude.RunRequest
}

func (f *fakeRunner) Run(_ context.Context, req claude.RunRequest) (<-chan claude.Event, error) {
	f.lastReq = req
	if f.startErr != nil {
		return nil, f.startErr
	}
	ch := make(chan claude.Event, len(f.events))
	for _, e := range f.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func setup(t *testing.T, runner claude.Runner) (*Manager, *store.Store, models.Session) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p, _ := st.CreateProject("demo", "/tmp/demo")
	sess, _ := st.CreateSession(p.ID, "task", "claude-opus-4-7")
	return New(st, ws.NewHub(), runner), st, sess
}

func TestSendMessageSuccess(t *testing.T) {
	runner := &fakeRunner{events: []claude.Event{
		{Kind: claude.KindSystem, ClaudeSessionID: "claude-xyz", Model: "claude-opus-4-7"},
		{Kind: claude.KindAssistant, Text: "Hi there"},
		{Kind: claude.KindResult, InputTokens: 10, OutputTokens: 5, CostUSD: 0.001},
	}}
	mgr, st, sess := setup(t, runner)

	if _, err := mgr.SendMessage(sess.ID, "hello"); err != nil {
		t.Fatalf("send: %v", err)
	}
	mgr.Wait()

	got, _ := st.GetSession(sess.ID)
	if got.Status != models.StatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %s", got.Status)
	}
	if got.ClaudeSessionID != "claude-xyz" {
		t.Fatalf("expected claude id captured, got %q", got.ClaudeSessionID)
	}

	msgs, _ := st.ListMessages(sess.ID)
	if len(msgs) != 2 {
		t.Fatalf("expected user+assistant messages, got %d", len(msgs))
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "Hi there" {
		t.Fatalf("unexpected assistant message: %+v", msgs[1])
	}

	usage, _ := st.ListSessionUsage(sess.ID)
	if len(usage) != 1 || usage[0].InputTokens != 10 || usage[0].OutputTokens != 5 {
		t.Fatalf("unexpected usage: %+v", usage)
	}

	// The runner should have been asked to resume the captured claude session.
	if runner.lastReq.Cwd != "/tmp/demo" {
		t.Fatalf("expected cwd from project, got %q", runner.lastReq.Cwd)
	}
}

func TestSendMessageRunnerError(t *testing.T) {
	runner := &fakeRunner{startErr: errors.New("spawn failed")}
	mgr, st, sess := setup(t, runner)

	if _, err := mgr.SendMessage(sess.ID, "hello"); err != nil {
		t.Fatalf("send returned error too early: %v", err)
	}
	mgr.Wait()

	got, _ := st.GetSession(sess.ID)
	if got.Status != models.StatusError {
		t.Fatalf("expected error status, got %s", got.Status)
	}
}

func TestSendMessageEventError(t *testing.T) {
	runner := &fakeRunner{events: []claude.Event{
		{Kind: claude.KindError, Err: errors.New("boom")},
	}}
	mgr, st, sess := setup(t, runner)

	_, _ = mgr.SendMessage(sess.ID, "hello")
	mgr.Wait()

	got, _ := st.GetSession(sess.ID)
	if got.Status != models.StatusError {
		t.Fatalf("expected error status, got %s", got.Status)
	}
}

func TestSendMessageUnknownSession(t *testing.T) {
	mgr, _, _ := setup(t, &fakeRunner{})
	if _, err := mgr.SendMessage("ghost", "hi"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
