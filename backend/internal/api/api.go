// Package api wires the HTTP surface: a chi router exposing the REST endpoints
// from docs/API.md plus a WebSocket endpoint that streams live hub events.
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/gorilla/websocket"

	"github.com/robinmalmstrom/ccdash/backend/internal/models"
	"github.com/robinmalmstrom/ccdash/backend/internal/session"
	"github.com/robinmalmstrom/ccdash/backend/internal/store"
	"github.com/robinmalmstrom/ccdash/backend/internal/ws"
)

// UtilizationFetcher returns the Claude subscription rate-limit usage (the data
// behind the CLI's /usage view). Behind an interface so it can be faked in tests.
type UtilizationFetcher interface {
	Fetch(ctx context.Context) (models.Utilization, error)
}

// Server holds the dependencies shared by all handlers.
type Server struct {
	store    *store.Store
	mgr      *session.Manager
	hub      *ws.Hub
	util     UtilizationFetcher
	version  string
	upgrader websocket.Upgrader
}

// NewServer constructs a Server. util may be nil if subscription usage is
// unavailable, in which case /api/usage/limits reports it as unconfigured.
func NewServer(st *store.Store, mgr *session.Manager, hub *ws.Hub, util UtilizationFetcher, version string) *Server {
	return &Server{
		store:   st,
		mgr:     mgr,
		hub:     hub,
		util:    util,
		version: version,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			// Dev dashboard: accept any origin. Tighten for production.
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
}

// Router builds the chi router with middleware and routes.
//
// Layout: the request logger is scoped to the /api and /ws routes so the
// frontend handler (which in dev proxies every JS module and HMR poll to Vite)
// doesn't drown the backend log stream. RequestID, Recoverer, and CORS still
// apply everywhere.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:10000", "http://127.0.0.1:10000"},
		AllowedMethods:   []string{"GET", "POST", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Group(func(r chi.Router) {
		r.Use(middleware.Logger)

		r.Route("/api", func(r chi.Router) {
			r.Get("/health", s.handleHealth)

			r.Route("/projects", func(r chi.Router) {
				r.Get("/", s.handleListProjects)
				r.Post("/", s.handleCreateProject)
				r.Get("/{id}", s.handleGetProject)
				r.Patch("/{id}", s.handleUpdateProject)
				r.Delete("/{id}", s.handleDeleteProject)
				r.Get("/{id}/sessions", s.handleListProjectSessions)
				r.Post("/{id}/sessions", s.handleCreateSession)
			})

			r.Route("/sessions", func(r chi.Router) {
				r.Get("/", s.handleListSessions)
				r.Get("/{id}", s.handleGetSession)
				r.Delete("/{id}", s.handleDeleteSession)
				r.Get("/{id}/messages", s.handleListMessages)
				r.Post("/{id}/messages", s.handleSendMessage)
				r.Post("/{id}/stop", s.handleStopSession)
				r.Patch("/{id}/mode", s.handleSetMode)
				r.Patch("/{id}/title", s.handleRenameSession)
				r.Get("/{id}/permissions", s.handleListPermissions)
				r.Post("/{id}/permissions/{requestId}", s.handleRespondPermission)
				r.Get("/{id}/usage", s.handleSessionUsage)
				r.Post("/{id}/preview", s.handlePreviewSession)
				r.Delete("/{id}/preview", s.handleUnpreviewSession)
				r.Post("/{id}/accept", s.handleAcceptSession)
			})

			r.Get("/attachments/{id}", s.handleGetAttachment)

			r.Get("/usage", s.handleUsageSummary)
			r.Get("/usage/limits", s.handleUsageLimits)
		})

		r.Get("/ws", s.handleWS)
	})

	// Everything else: the frontend (dev = Vite reverse proxy, prod = embedded
	// bundle with SPA fallback). The handler is constructed once per request
	// via frontendHandler() — cheap in prod, and lets dev pick up its config
	// without restart.
	r.NotFound(s.frontendHandler().ServeHTTP)
	return r
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// statusForErr maps store errors to HTTP status codes.
func statusForErr(err error) int {
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// --- health ---

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

// --- projects ---

func (s *Server) handleListProjects(w http.ResponseWriter, _ *http.Request) {
	projects, err := s.store.ListProjects()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string         `json:"name"`
		Path    string         `json:"path"`
		GitMode models.GitMode `json:"gitMode"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Name == "" || body.Path == "" {
		writeErr(w, http.StatusBadRequest, "name and path are required")
		return
	}
	if body.GitMode != "" && !models.ValidGitMode(body.GitMode) {
		writeErr(w, http.StatusBadRequest, "invalid gitMode")
		return
	}
	p, err := s.store.CreateProject(body.Name, body.Path, body.GitMode)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.hub.Broadcast("project.created", p)
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		GitMode models.GitMode `json:"gitMode"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !models.ValidGitMode(body.GitMode) {
		writeErr(w, http.StatusBadRequest, "invalid gitMode")
		return
	}
	if err := s.store.UpdateProjectGitMode(id, body.GitMode); err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	p, err := s.store.GetProject(id)
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	s.hub.Broadcast("project.updated", p)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProject(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetProject(id); err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	// Manager.DeleteProject tears down each session's worktree (if any) before
	// the cascade and broadcasts project.deleted on success.
	if err := s.mgr.DeleteProject(id); err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListProjectSessions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetProject(id); err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	sessions, err := s.store.ListProjectSessions(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Title          string                `json:"title"`
		Model          string                `json:"model"`
		PermissionMode models.PermissionMode `json:"permissionMode"`
	}
	// Body is optional for session creation.
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.PermissionMode != "" && !models.ValidPermissionMode(body.PermissionMode) {
		writeErr(w, http.StatusBadRequest, "invalid permissionMode")
		return
	}
	// Routed through the manager so it can provision a git worktree for the
	// session when the project's path is inside a git repo. Non-git projects
	// fall through to plain row creation.
	sess, err := s.mgr.CreateSession(id, body.Title, body.Model, body.PermissionMode)
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	s.hub.Broadcast("session.status", sess)
	writeJSON(w, http.StatusCreated, sess)
}

// handleDeleteSession removes a session row, its worktree (if any), and
// optionally the worktree's branch. Query: ?deleteBranch=true|false (default
// false).
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	deleteBranch := r.URL.Query().Get("deleteBranch") == "true"
	if err := s.mgr.DeleteSession(id, deleteBranch); err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- sessions ---

func (s *Server) handleListSessions(w http.ResponseWriter, _ *http.Request) {
	sessions, err := s.store.ListSessions()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.GetSession(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetSession(id); err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	messages, err := s.store.ListMessages(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, messages)
}

// Image limits for pasted attachments.
const (
	maxImagesPerMessage = 8
	maxImageBytes       = 10 << 20  // 10 MiB decoded
	maxSendBodyBytes    = 120 << 20 // generous cap on the whole request
)

var allowedImageTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.Body = http.MaxBytesReader(w, r.Body, maxSendBodyBytes)
	var body struct {
		Content string `json:"content"`
		Images  []struct {
			Name      string `json:"name"`
			MediaType string `json:"mediaType"`
			Data      string `json:"data"` // base64 (no data: URL prefix)
		} `json:"images"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Content == "" && len(body.Images) == 0 {
		writeErr(w, http.StatusBadRequest, "content or images are required")
		return
	}
	if len(body.Images) > maxImagesPerMessage {
		writeErr(w, http.StatusBadRequest, "too many images")
		return
	}

	images := make([]session.InboundImage, 0, len(body.Images))
	for _, img := range body.Images {
		if !allowedImageTypes[img.MediaType] {
			writeErr(w, http.StatusBadRequest, "unsupported image type: "+img.MediaType)
			return
		}
		data, derr := base64.StdEncoding.DecodeString(img.Data)
		if derr != nil {
			writeErr(w, http.StatusBadRequest, "invalid image data")
			return
		}
		if len(data) == 0 || len(data) > maxImageBytes {
			writeErr(w, http.StatusBadRequest, "image too large or empty")
			return
		}
		name := strings.TrimSpace(img.Name)
		if name == "" {
			name = "image"
		}
		images = append(images, session.InboundImage{Name: name, MediaType: img.MediaType, Data: data})
	}

	msg, err := s.mgr.SendMessage(id, body.Content, images)
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, msg)
}

func (s *Server) handleGetAttachment(w http.ResponseWriter, r *http.Request) {
	att, err := s.store.GetAttachment(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	w.Header().Set("Content-Type", att.MediaType)
	// Bytes are immutable once stored, so allow aggressive client caching.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(att.Data)
}

func (s *Server) handleStopSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.mgr.Stop(id); err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	sess, err := s.store.GetSession(id)
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleSetMode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		PermissionMode models.PermissionMode `json:"permissionMode"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !models.ValidPermissionMode(body.PermissionMode) {
		writeErr(w, http.StatusBadRequest, "invalid permissionMode")
		return
	}
	sess, err := s.mgr.SetMode(id, body.PermissionMode)
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Title string `json:"title"`
	}
	if !decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		writeErr(w, http.StatusBadRequest, "title is required")
		return
	}
	sess, err := s.mgr.Rename(id, body.Title)
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleListPermissions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetSession(id); err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.mgr.PendingPermissions(id))
}

func (s *Server) handleRespondPermission(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	requestID := chi.URLParam(r, "requestId")
	var body struct {
		Decision string `json:"decision"`
		Message  string `json:"message"`
		// answers ships user selections for tools whose result is gathered
		// through the permission dialog (AskUserQuestion). Keys are question
		// text; values are the selected option label (multi-select: ", "-joined
		// by the client). Ignored unless decision is "allow".
		Answers map[string]string `json:"answers"`
	}
	if !decode(w, r, &body) {
		return
	}
	var allow, always bool
	switch body.Decision {
	case "allow":
		allow = true
	case "allow_always":
		allow, always = true, true
	case "deny":
		// allow stays false
	default:
		writeErr(w, http.StatusBadRequest, "decision must be allow, allow_always, or deny")
		return
	}
	if err := s.mgr.RespondPermission(id, requestID, allow, always, body.Message, body.Answers); err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// previewStatusForErr maps preview-flow sentinels to the right HTTP code:
// already-applied / not-applied → 400; another preview applied → 409;
// no changes → 400; not found → 404; everything else falls through to 500.
func previewStatusForErr(err error) int {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, session.ErrPreviewAlreadyApplied),
		errors.Is(err, session.ErrPreviewNotApplied),
		errors.Is(err, session.ErrNoChanges):
		return http.StatusBadRequest
	case errors.Is(err, session.ErrAnotherPreviewApplied):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) handlePreviewSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess, err := s.mgr.PreviewSession(id)
	if err != nil {
		writeErr(w, previewStatusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sess})
}

func (s *Server) handleUnpreviewSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess, err := s.mgr.UnpreviewSession(id)
	if err != nil {
		writeErr(w, previewStatusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sess})
}

func (s *Server) handleAcceptSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sess, err := s.mgr.AcceptSession(id)
	if err != nil {
		writeErr(w, previewStatusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": sess})
}

func (s *Server) handleSessionUsage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetSession(id); err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	records, err := s.store.ListSessionUsage(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, records)
}

func (s *Server) handleUsageSummary(w http.ResponseWriter, _ *http.Request) {
	summary, err := s.store.UsageSummary()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// handleUsageLimits returns the Claude subscription rate-limit usage (the /usage
// view). The endpoint is undocumented and best-effort: failures map to 502 so the
// dashboard can show "unavailable" without treating it as a server fault.
func (s *Server) handleUsageLimits(w http.ResponseWriter, r *http.Request) {
	if s.util == nil {
		writeErr(w, http.StatusNotImplemented, "subscription usage not configured")
		return
	}
	u, err := s.util.Fetch(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// --- websocket ---

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote an error response.
	}
	defer func() { _ = conn.Close() }()
	ch := s.hub.Subscribe()

	// Reader: drain client frames so control frames (close/ping) are handled.
	go func() {
		defer func() { _ = conn.Close() }()
		for {
			if _, _, rerr := conn.ReadMessage(); rerr != nil {
				s.hub.Unsubscribe(ch)
				return
			}
		}
	}()

	// Writer: forward hub events plus periodic pings.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case data, ok := <-ch:
			if !ok {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if werr := conn.WriteMessage(websocket.TextMessage, data); werr != nil {
				s.hub.Unsubscribe(ch)
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if werr := conn.WriteMessage(websocket.PingMessage, nil); werr != nil {
				s.hub.Unsubscribe(ch)
				return
			}
		}
	}
}
