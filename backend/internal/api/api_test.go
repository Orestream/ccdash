package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/robinmalmstrom/ccdash/backend/internal/claude"
	"github.com/robinmalmstrom/ccdash/backend/internal/models"
	"github.com/robinmalmstrom/ccdash/backend/internal/session"
	"github.com/robinmalmstrom/ccdash/backend/internal/store"
	"github.com/robinmalmstrom/ccdash/backend/internal/ws"
)

// nopSession is a live session that emits nothing and accepts everything; enough
// to exercise the HTTP surface without a real claude process.
type nopSession struct{}

func (nopSession) Send(string, []claude.Image) error { return nil }
func (nopSession) Respond(string, claude.Decision, json.RawMessage, string) error {
	return nil
}
func (nopSession) SetMode(string) error { return nil }
func (nopSession) Events() <-chan claude.Event {
	ch := make(chan claude.Event)
	close(ch)
	return ch
}
func (nopSession) Close() error { return nil }

type nopRunner struct{}

func (nopRunner) Start(context.Context, claude.StartRequest) (claude.Session, error) {
	return nopSession{}, nil
}

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	hub := ws.NewHub()
	mgr := session.New(st, hub, nopRunner{})
	return NewServer(st, mgr, hub, fakeUtil{}, "test").Router()
}

// fakeUtil is a stub UtilizationFetcher for the HTTP tests.
type fakeUtil struct {
	u   models.Utilization
	err error
}

func (f fakeUtil) Fetch(context.Context) (models.Utilization, error) {
	return f.u, f.err
}

func do(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewBuffer(b)
	} else {
		rdr = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealth(t *testing.T) {
	h := newTestServer(t)
	rec := do(t, h, http.MethodGet, "/api/health", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["status"] != "ok" || resp["version"] != "test" {
		t.Fatalf("unexpected health body: %v", resp)
	}
}

func TestProjectAndSessionFlow(t *testing.T) {
	h := newTestServer(t)

	// Create a project.
	rec := do(t, h, http.MethodPost, "/api/projects", map[string]string{"name": "demo", "path": "/tmp/demo"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project status = %d body=%s", rec.Code, rec.Body.String())
	}
	var project models.Project
	_ = json.Unmarshal(rec.Body.Bytes(), &project)
	if project.ID == "" {
		t.Fatal("expected project id")
	}

	// List projects.
	rec = do(t, h, http.MethodGet, "/api/projects", nil)
	var projects []models.Project
	_ = json.Unmarshal(rec.Body.Bytes(), &projects)
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}

	// Create a session under the project.
	rec = do(t, h, http.MethodPost, "/api/projects/"+project.ID+"/sessions", map[string]string{"title": "task"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session status = %d", rec.Code)
	}
	var sess models.Session
	_ = json.Unmarshal(rec.Body.Bytes(), &sess)
	if sess.ProjectID != project.ID || sess.Status != models.StatusIdle {
		t.Fatalf("unexpected session: %+v", sess)
	}

	// Send a message: should be accepted and recorded.
	rec = do(t, h, http.MethodPost, "/api/sessions/"+sess.ID+"/messages", map[string]string{"content": "hello"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send message status = %d body=%s", rec.Code, rec.Body.String())
	}

	// Usage summary endpoint responds.
	rec = do(t, h, http.MethodGet, "/api/usage", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("usage status = %d", rec.Code)
	}
}

func TestCreateProjectValidation(t *testing.T) {
	h := newTestServer(t)
	rec := do(t, h, http.MethodPost, "/api/projects", map[string]string{"name": ""})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestGetMissingSession(t *testing.T) {
	h := newTestServer(t)
	rec := do(t, h, http.MethodGet, "/api/sessions/does-not-exist", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func createSessionForTest(t *testing.T, h http.Handler, body map[string]string) models.Session {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/projects", map[string]string{"name": "demo", "path": "/tmp/demo"})
	var project models.Project
	_ = json.Unmarshal(rec.Body.Bytes(), &project)
	rec = do(t, h, http.MethodPost, "/api/projects/"+project.ID+"/sessions", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create session status = %d body=%s", rec.Code, rec.Body.String())
	}
	var sess models.Session
	_ = json.Unmarshal(rec.Body.Bytes(), &sess)
	return sess
}

func TestCreateSessionWithMode(t *testing.T) {
	h := newTestServer(t)
	sess := createSessionForTest(t, h, map[string]string{"title": "t", "permissionMode": "acceptEdits"})
	if sess.PermissionMode != models.ModeAcceptEdits {
		t.Fatalf("expected acceptEdits, got %s", sess.PermissionMode)
	}
}

func TestDeleteSession(t *testing.T) {
	h := newTestServer(t)
	sess := createSessionForTest(t, h, map[string]string{"title": "t"})

	rec := do(t, h, http.MethodDelete, "/api/sessions/"+sess.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/api/sessions/"+sess.ID, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", rec.Code)
	}
	// Deleting twice returns 404, not 5xx.
	rec = do(t, h, http.MethodDelete, "/api/sessions/"+sess.ID, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on second delete, got %d", rec.Code)
	}
}

func TestCreateSessionInvalidMode(t *testing.T) {
	h := newTestServer(t)
	rec := do(t, h, http.MethodPost, "/api/projects", map[string]string{"name": "demo", "path": "/tmp/demo"})
	var project models.Project
	_ = json.Unmarshal(rec.Body.Bytes(), &project)
	rec = do(t, h, http.MethodPost, "/api/projects/"+project.ID+"/sessions", map[string]string{"permissionMode": "bogus"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad mode, got %d", rec.Code)
	}
}

func TestSetMode(t *testing.T) {
	h := newTestServer(t)
	sess := createSessionForTest(t, h, map[string]string{"title": "t"})

	rec := do(t, h, http.MethodPatch, "/api/sessions/"+sess.ID+"/mode", map[string]string{"permissionMode": "auto"})
	if rec.Code != http.StatusOK {
		t.Fatalf("set mode status = %d body=%s", rec.Code, rec.Body.String())
	}
	var updated models.Session
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.PermissionMode != models.ModeAuto {
		t.Fatalf("expected auto, got %s", updated.PermissionMode)
	}

	rec = do(t, h, http.MethodPatch, "/api/sessions/"+sess.ID+"/mode", map[string]string{"permissionMode": "nope"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad mode, got %d", rec.Code)
	}
}

func TestSendMessageWithImage(t *testing.T) {
	h := newTestServer(t)
	sess := createSessionForTest(t, h, map[string]string{"title": "t"})
	raw := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a}

	rec := do(t, h, http.MethodPost, "/api/sessions/"+sess.ID+"/messages", map[string]any{
		"content": "see image-1",
		"images": []map[string]string{
			{"name": "image-1.png", "mediaType": "image/png", "data": base64.StdEncoding.EncodeToString(raw)},
		},
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send status = %d body=%s", rec.Code, rec.Body.String())
	}
	var msg models.Message
	_ = json.Unmarshal(rec.Body.Bytes(), &msg)
	if len(msg.Attachments) != 1 || msg.Attachments[0].Name != "image-1.png" {
		t.Fatalf("expected 1 attachment, got %+v", msg.Attachments)
	}

	// The bytes are served back with the right content type.
	rec = do(t, h, http.MethodGet, "/api/attachments/"+msg.Attachments[0].ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get attachment status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("unexpected content-type: %q", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), raw) {
		t.Fatalf("served bytes do not match stored image")
	}

	// Unsupported media type is rejected.
	rec = do(t, h, http.MethodPost, "/api/sessions/"+sess.ID+"/messages", map[string]any{
		"images": []map[string]string{
			{"name": "a.svg", "mediaType": "image/svg+xml", "data": base64.StdEncoding.EncodeToString(raw)},
		},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unsupported media type, got %d", rec.Code)
	}
}

func TestRenameSession(t *testing.T) {
	h := newTestServer(t)
	sess := createSessionForTest(t, h, map[string]string{"title": "old"})

	rec := do(t, h, http.MethodPatch, "/api/sessions/"+sess.ID+"/title", map[string]string{"title": "Renamed"})
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d body=%s", rec.Code, rec.Body.String())
	}
	var updated models.Session
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Title != "Renamed" {
		t.Fatalf("expected title 'Renamed', got %q", updated.Title)
	}

	rec = do(t, h, http.MethodPatch, "/api/sessions/"+sess.ID+"/title", map[string]string{"title": "  "})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for blank title, got %d", rec.Code)
	}
}

func TestListPermissionsEmpty(t *testing.T) {
	h := newTestServer(t)
	sess := createSessionForTest(t, h, map[string]string{"title": "t"})
	rec := do(t, h, http.MethodGet, "/api/sessions/"+sess.ID+"/permissions", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var pending []models.PermissionRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &pending); err != nil {
		t.Fatalf("bad body: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected empty pending, got %d", len(pending))
	}
}

func TestRespondPermissionValidation(t *testing.T) {
	h := newTestServer(t)
	sess := createSessionForTest(t, h, map[string]string{"title": "t"})
	// Bad decision value → 400.
	rec := do(t, h, http.MethodPost, "/api/sessions/"+sess.ID+"/permissions/req_1", map[string]string{"decision": "maybe"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	// Valid decision but no live session/pending → 404.
	rec = do(t, h, http.MethodPost, "/api/sessions/"+sess.ID+"/permissions/req_1", map[string]string{"decision": "allow"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestUsageLimits(t *testing.T) {
	reset := time.Date(2026, 5, 29, 6, 0, 0, 0, time.UTC)
	util := fakeUtil{u: models.Utilization{
		Session:   &models.UsageWindow{UsedPercent: 3},
		Week:      &models.UsageWindow{UsedPercent: 9, ResetsAt: &reset},
		FetchedAt: time.Now(),
	}}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	hub := ws.NewHub()
	h := NewServer(st, session.New(st, hub, nopRunner{}), hub, util, "test").Router()

	rec := do(t, h, http.MethodGet, "/api/usage/limits", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got models.Utilization
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Session == nil || got.Session.UsedPercent != 3 {
		t.Errorf("session = %+v, want UsedPercent 3", got.Session)
	}
	if got.WeekOpus != nil {
		t.Errorf("weekOpus = %+v, want nil", got.WeekOpus)
	}
}

func TestUsageLimitsError(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	hub := ws.NewHub()
	util := fakeUtil{err: errors.New("token expired")}
	h := NewServer(st, session.New(st, hub, nopRunner{}), hub, util, "test").Router()

	rec := do(t, h, http.MethodGet, "/api/usage/limits", nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestSendMessageRequiresContent(t *testing.T) {
	h := newTestServer(t)
	rec := do(t, h, http.MethodPost, "/api/projects", map[string]string{"name": "demo", "path": "/tmp/demo"})
	var project models.Project
	_ = json.Unmarshal(rec.Body.Bytes(), &project)
	rec = do(t, h, http.MethodPost, "/api/projects/"+project.ID+"/sessions", map[string]string{})
	var sess models.Session
	_ = json.Unmarshal(rec.Body.Bytes(), &sess)

	rec = do(t, h, http.MethodPost, "/api/sessions/"+sess.ID+"/messages", map[string]string{"content": ""})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty content, got %d", rec.Code)
	}
}
