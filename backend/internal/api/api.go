// Package api wires the HTTP surface: a chi router exposing the REST endpoints
// from docs/API.md plus a WebSocket endpoint that streams live hub events.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
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

// Server holds the dependencies shared by all handlers.
type Server struct {
	store    *store.Store
	mgr      *session.Manager
	hub      *ws.Hub
	version  string
	upgrader websocket.Upgrader
}

// NewServer constructs a Server.
func NewServer(st *store.Store, mgr *session.Manager, hub *ws.Hub, version string) *Server {
	return &Server{
		store:   st,
		mgr:     mgr,
		hub:     hub,
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
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:10000", "http://127.0.0.1:10000"},
		AllowedMethods:   []string{"GET", "POST", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", s.handleHealth)

		r.Route("/projects", func(r chi.Router) {
			r.Get("/", s.handleListProjects)
			r.Post("/", s.handleCreateProject)
			r.Get("/{id}", s.handleGetProject)
			r.Delete("/{id}", s.handleDeleteProject)
			r.Get("/{id}/sessions", s.handleListProjectSessions)
			r.Post("/{id}/sessions", s.handleCreateSession)
		})

		r.Route("/sessions", func(r chi.Router) {
			r.Get("/", s.handleListSessions)
			r.Get("/{id}", s.handleGetSession)
			r.Get("/{id}/messages", s.handleListMessages)
			r.Post("/{id}/messages", s.handleSendMessage)
			r.Post("/{id}/stop", s.handleStopSession)
			r.Patch("/{id}/mode", s.handleSetMode)
			r.Get("/{id}/permissions", s.handleListPermissions)
			r.Post("/{id}/permissions/{requestId}", s.handleRespondPermission)
			r.Get("/{id}/usage", s.handleSessionUsage)
		})

		r.Get("/usage", s.handleUsageSummary)
	})

	r.Get("/ws", s.handleWS)
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
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Name == "" || body.Path == "" {
		writeErr(w, http.StatusBadRequest, "name and path are required")
		return
	}
	p, err := s.store.CreateProject(body.Name, body.Path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.hub.Broadcast("project.created", p)
	writeJSON(w, http.StatusCreated, p)
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
	p, err := s.store.GetProject(id)
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	if err := s.store.DeleteProject(id); err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	s.hub.Broadcast("project.deleted", p)
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
	sess, err := s.store.CreateSession(id, body.Title, body.Model, body.PermissionMode)
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	s.hub.Broadcast("session.status", sess)
	writeJSON(w, http.StatusCreated, sess)
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

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Content string `json:"content"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Content == "" {
		writeErr(w, http.StatusBadRequest, "content is required")
		return
	}
	msg, err := s.mgr.SendMessage(id, body.Content)
	if err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, msg)
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
	if err := s.mgr.RespondPermission(id, requestID, allow, always, body.Message); err != nil {
		writeErr(w, statusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
