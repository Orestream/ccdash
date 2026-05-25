// Package store provides SQLite-backed persistence for projects, sessions,
// messages and usage records. It uses the pure-Go modernc.org/sqlite driver
// (no CGO). Timestamps are stored as RFC3339Nano text for portability.
package store

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/robinmalmstrom/ccdash/backend/internal/models"
)

//go:embed schema.sql
var schema string

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps a *sql.DB and exposes domain-level CRUD methods.
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) a SQLite database at dsn. Use ":memory:" for tests.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite is a single-writer engine; one connection keeps an in-memory DB
	// coherent and avoids "database is locked" under concurrency.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// migrate applies idempotent column additions for databases created by an older
// schema. ADD COLUMN errors for already-present columns are ignored.
func migrate(db *sql.DB) error {
	alters := []string{
		`ALTER TABLE sessions ADD COLUMN permission_mode TEXT NOT NULL DEFAULT 'default'`,
	}
	for _, stmt := range alters {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func nowStr() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func parseTime(v string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

// --- Projects ---

// CreateProject inserts a new project and returns it.
func (s *Store) CreateProject(name, path string) (models.Project, error) {
	p := models.Project{
		ID:        uuid.NewString(),
		Name:      name,
		Path:      path,
		CreatedAt: time.Now().UTC(),
	}
	_, err := s.db.Exec(
		`INSERT INTO projects (id, name, path, created_at) VALUES (?, ?, ?, ?)`,
		p.ID, p.Name, p.Path, p.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return models.Project{}, fmt.Errorf("insert project: %w", err)
	}
	return p, nil
}

// ListProjects returns all projects, newest first.
func (s *Store) ListProjects() ([]models.Project, error) {
	rows, err := s.db.Query(`SELECT id, name, path, created_at FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query projects: %w", err)
	}
	defer rows.Close()

	projects := []models.Project{}
	for rows.Next() {
		var p models.Project
		var created string
		if err := rows.Scan(&p.ID, &p.Name, &p.Path, &created); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		p.CreatedAt = parseTime(created)
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// GetProject returns a single project or ErrNotFound.
func (s *Store) GetProject(id string) (models.Project, error) {
	var p models.Project
	var created string
	err := s.db.QueryRow(
		`SELECT id, name, path, created_at FROM projects WHERE id = ?`, id,
	).Scan(&p.ID, &p.Name, &p.Path, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Project{}, ErrNotFound
	}
	if err != nil {
		return models.Project{}, fmt.Errorf("get project: %w", err)
	}
	p.CreatedAt = parseTime(created)
	return p, nil
}

// DeleteProject removes a project (cascading to its sessions).
func (s *Store) DeleteProject(id string) error {
	res, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Sessions ---

// CreateSession inserts a new idle session for a project. An empty mode defaults
// to ModeDefault.
func (s *Store) CreateSession(projectID, title, model string, mode models.PermissionMode) (models.Session, error) {
	if _, err := s.GetProject(projectID); err != nil {
		return models.Session{}, err
	}
	if mode == "" {
		mode = models.ModeDefault
	}
	now := time.Now().UTC()
	sess := models.Session{
		ID:             uuid.NewString(),
		ProjectID:      projectID,
		Title:          title,
		Status:         models.StatusIdle,
		Model:          model,
		PermissionMode: mode,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, project_id, claude_session_id, title, status, model, permission_mode, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.ProjectID, sess.ClaudeSessionID, sess.Title, sess.Status, sess.Model, sess.PermissionMode,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return models.Session{}, fmt.Errorf("insert session: %w", err)
	}
	return sess, nil
}

func scanSession(scanner interface{ Scan(...any) error }) (models.Session, error) {
	var sess models.Session
	var status, mode, created, updated string
	if err := scanner.Scan(
		&sess.ID, &sess.ProjectID, &sess.ClaudeSessionID, &sess.Title,
		&status, &sess.Model, &mode, &created, &updated,
	); err != nil {
		return models.Session{}, err
	}
	sess.Status = models.SessionStatus(status)
	sess.PermissionMode = models.PermissionMode(mode)
	sess.CreatedAt = parseTime(created)
	sess.UpdatedAt = parseTime(updated)
	return sess, nil
}

const sessionCols = `id, project_id, claude_session_id, title, status, model, permission_mode, created_at, updated_at`

// ListSessions returns all sessions, newest first.
func (s *Store) ListSessions() ([]models.Session, error) {
	rows, err := s.db.Query(`SELECT ` + sessionCols + ` FROM sessions ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()
	return collectSessions(rows)
}

// ListProjectSessions returns sessions for one project, newest first.
func (s *Store) ListProjectSessions(projectID string) ([]models.Session, error) {
	rows, err := s.db.Query(
		`SELECT `+sessionCols+` FROM sessions WHERE project_id = ? ORDER BY created_at DESC`, projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("query project sessions: %w", err)
	}
	defer rows.Close()
	return collectSessions(rows)
}

func collectSessions(rows *sql.Rows) ([]models.Session, error) {
	sessions := []models.Session{}
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// GetSession returns a single session or ErrNotFound.
func (s *Store) GetSession(id string) (models.Session, error) {
	row := s.db.QueryRow(`SELECT `+sessionCols+` FROM sessions WHERE id = ?`, id)
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Session{}, ErrNotFound
	}
	if err != nil {
		return models.Session{}, fmt.Errorf("get session: %w", err)
	}
	return sess, nil
}

// UpdateSessionStatus sets the status and bumps updated_at.
func (s *Store) UpdateSessionStatus(id string, status models.SessionStatus) error {
	res, err := s.db.Exec(
		`UPDATE sessions SET status = ?, updated_at = ? WHERE id = ?`,
		status, nowStr(), id,
	)
	if err != nil {
		return fmt.Errorf("update session status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateSessionMode changes a session's answering (permission) mode.
func (s *Store) UpdateSessionMode(id string, mode models.PermissionMode) error {
	res, err := s.db.Exec(
		`UPDATE sessions SET permission_mode = ?, updated_at = ? WHERE id = ?`,
		mode, nowStr(), id,
	)
	if err != nil {
		return fmt.Errorf("update session mode: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateSessionTitle sets a session's display title and bumps updated_at.
func (s *Store) UpdateSessionTitle(id, title string) error {
	res, err := s.db.Exec(
		`UPDATE sessions SET title = ?, updated_at = ? WHERE id = ?`,
		title, nowStr(), id,
	)
	if err != nil {
		return fmt.Errorf("update session title: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateSessionClaudeID records the claude-side session id once known.
func (s *Store) UpdateSessionClaudeID(id, claudeID string) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET claude_session_id = ?, updated_at = ? WHERE id = ?`,
		claudeID, nowStr(), id,
	)
	if err != nil {
		return fmt.Errorf("update claude session id: %w", err)
	}
	return nil
}

// --- Messages ---

// AddMessage appends a message to a session transcript.
func (s *Store) AddMessage(sessionID, role, content string) (models.Message, error) {
	m := models.Message{
		ID:        uuid.NewString(),
		SessionID: sessionID,
		Role:      role,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	_, err := s.db.Exec(
		`INSERT INTO messages (id, session_id, role, content, created_at) VALUES (?, ?, ?, ?, ?)`,
		m.ID, m.SessionID, m.Role, m.Content, m.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return models.Message{}, fmt.Errorf("insert message: %w", err)
	}
	return m, nil
}

// ListMessages returns a session transcript in chronological order.
func (s *Store) ListMessages(sessionID string) ([]models.Message, error) {
	rows, err := s.db.Query(
		`SELECT id, session_id, role, content, created_at FROM messages
		 WHERE session_id = ? ORDER BY created_at ASC`, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	messages := []models.Message{}
	for rows.Next() {
		var m models.Message
		var created string
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &created); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		m.CreatedAt = parseTime(created)
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// --- Usage ---

// AddUsage records token/cost usage for a run.
func (s *Store) AddUsage(sessionID, model string, in, out int, cost float64) (models.UsageRecord, error) {
	rec := models.UsageRecord{
		ID:           uuid.NewString(),
		SessionID:    sessionID,
		Model:        model,
		InputTokens:  in,
		OutputTokens: out,
		CostUSD:      cost,
		CreatedAt:    time.Now().UTC(),
	}
	_, err := s.db.Exec(
		`INSERT INTO usage_records (id, session_id, model, input_tokens, output_tokens, cost_usd, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.SessionID, rec.Model, rec.InputTokens, rec.OutputTokens, rec.CostUSD,
		rec.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return models.UsageRecord{}, fmt.Errorf("insert usage: %w", err)
	}
	return rec, nil
}

// ListSessionUsage returns all usage records for a session, newest first.
func (s *Store) ListSessionUsage(sessionID string) ([]models.UsageRecord, error) {
	rows, err := s.db.Query(
		`SELECT id, session_id, model, input_tokens, output_tokens, cost_usd, created_at
		 FROM usage_records WHERE session_id = ? ORDER BY created_at DESC`, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query usage: %w", err)
	}
	defer rows.Close()

	records := []models.UsageRecord{}
	for rows.Next() {
		var r models.UsageRecord
		var created string
		if err := rows.Scan(&r.ID, &r.SessionID, &r.Model, &r.InputTokens, &r.OutputTokens, &r.CostUSD, &created); err != nil {
			return nil, fmt.Errorf("scan usage: %w", err)
		}
		r.CreatedAt = parseTime(created)
		records = append(records, r)
	}
	return records, rows.Err()
}

// UsageSummary returns a dashboard-wide rollup plus per-session aggregates.
func (s *Store) UsageSummary() (models.UsageSummary, error) {
	summary := models.UsageSummary{BySession: []models.SessionUsage{}}
	rows, err := s.db.Query(
		`SELECT session_id, COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cost_usd),0)
		 FROM usage_records GROUP BY session_id`,
	)
	if err != nil {
		return summary, fmt.Errorf("query usage summary: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var su models.SessionUsage
		if err := rows.Scan(&su.SessionID, &su.InputTokens, &su.OutputTokens, &su.CostUSD); err != nil {
			return summary, fmt.Errorf("scan usage summary: %w", err)
		}
		summary.TotalInputTokens += su.InputTokens
		summary.TotalOutputTokens += su.OutputTokens
		summary.TotalCostUSD += su.CostUSD
		summary.BySession = append(summary.BySession, su)
	}
	return summary, rows.Err()
}
