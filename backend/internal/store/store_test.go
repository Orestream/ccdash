package store

import (
	"testing"

	"github.com/robinmalmstrom/ccdash/backend/internal/models"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestProjectCRUD(t *testing.T) {
	s := newTestStore(t)

	p, err := s.CreateProject("demo", "/tmp/demo")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == "" || p.CreatedAt.IsZero() {
		t.Fatalf("expected id and created_at, got %+v", p)
	}

	got, err := s.GetProject(p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "demo" || got.Path != "/tmp/demo" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	list, err := s.ListProjects()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: err=%v len=%d", err, len(list))
	}

	if err := s.DeleteProject(p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetProject(p.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestGetMissingProject(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetProject("nope"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("demo", "/tmp/demo")

	sess, err := s.CreateSession(p.ID, "task", "claude-opus-4-7", models.ModeDefault)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if sess.Status != models.StatusIdle {
		t.Fatalf("expected idle, got %s", sess.Status)
	}

	if err := s.UpdateSessionStatus(sess.ID, models.StatusProcessing); err != nil {
		t.Fatalf("update status: %v", err)
	}
	if err := s.UpdateSessionClaudeID(sess.ID, "claude-abc"); err != nil {
		t.Fatalf("update claude id: %v", err)
	}

	got, _ := s.GetSession(sess.ID)
	if got.Status != models.StatusProcessing || got.ClaudeSessionID != "claude-abc" {
		t.Fatalf("unexpected session state: %+v", got)
	}

	byProject, _ := s.ListProjectSessions(p.ID)
	if len(byProject) != 1 {
		t.Fatalf("expected 1 project session, got %d", len(byProject))
	}
}

func TestSessionModePersistence(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("demo", "/tmp/demo")

	sess, _ := s.CreateSession(p.ID, "task", "m", models.ModeAcceptEdits)
	if sess.PermissionMode != models.ModeAcceptEdits {
		t.Fatalf("expected acceptEdits on create, got %s", sess.PermissionMode)
	}
	// Empty mode defaults to ModeDefault.
	def, _ := s.CreateSession(p.ID, "task2", "m", "")
	if def.PermissionMode != models.ModeDefault {
		t.Fatalf("expected default mode, got %s", def.PermissionMode)
	}

	if err := s.UpdateSessionMode(sess.ID, models.ModeAuto); err != nil {
		t.Fatalf("update mode: %v", err)
	}
	got, _ := s.GetSession(sess.ID)
	if got.PermissionMode != models.ModeAuto {
		t.Fatalf("expected auto after update, got %s", got.PermissionMode)
	}
	if err := s.UpdateSessionMode("ghost", models.ModeAuto); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestCreateSessionUnknownProject(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateSession("ghost", "t", "m", models.ModeDefault); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMessagesAndUsage(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("demo", "/tmp/demo")
	sess, _ := s.CreateSession(p.ID, "task", "claude-opus-4-7", models.ModeDefault)

	if _, err := s.AddMessage(sess.ID, "user", "hello"); err != nil {
		t.Fatalf("add user msg: %v", err)
	}
	if _, err := s.AddMessage(sess.ID, "assistant", "hi there"); err != nil {
		t.Fatalf("add assistant msg: %v", err)
	}
	msgs, _ := s.ListMessages(sess.ID)
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}

	if _, err := s.AddUsage(sess.ID, "claude-opus-4-7", 100, 50, 0.01); err != nil {
		t.Fatalf("add usage: %v", err)
	}
	if _, err := s.AddUsage(sess.ID, "claude-opus-4-7", 200, 70, 0.02); err != nil {
		t.Fatalf("add usage 2: %v", err)
	}

	summary, err := s.UsageSummary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.TotalInputTokens != 300 || summary.TotalOutputTokens != 120 {
		t.Fatalf("unexpected token totals: %+v", summary)
	}
	if summary.TotalCostUSD < 0.0299 || summary.TotalCostUSD > 0.0301 {
		t.Fatalf("unexpected cost total: %v", summary.TotalCostUSD)
	}
	if len(summary.BySession) != 1 {
		t.Fatalf("expected 1 session in summary, got %d", len(summary.BySession))
	}
}

func TestDeleteProjectCascades(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("demo", "/tmp/demo")
	sess, _ := s.CreateSession(p.ID, "task", "m", models.ModeDefault)

	if err := s.DeleteProject(p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetSession(sess.ID); err != ErrNotFound {
		t.Fatalf("expected session cascade-deleted, got %v", err)
	}
}
