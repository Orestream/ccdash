// Package models defines the core domain types shared across the backend.
// JSON tags use camelCase to match the API contract in docs/API.md.
package models

import (
	"encoding/json"
	"time"
)

// SessionStatus is the lifecycle state of a session as surfaced to the UI.
type SessionStatus string

const (
	// StatusIdle means no prompt is running: the session was just created, or it
	// finished its last turn and is ready for the next message.
	StatusIdle SessionStatus = "idle"
	// StatusProcessing means claude is actively working on a prompt.
	StatusProcessing SessionStatus = "processing"
	// StatusAwaitingApproval means claude paused on a tool needing a permission decision.
	StatusAwaitingApproval SessionStatus = "awaiting_approval"
	// StatusAwaitingInput means claude paused waiting for the user to answer an
	// interactive dialog (not a permission prompt — that is StatusAwaitingApproval).
	// A normally completed turn goes to StatusIdle, not here.
	StatusAwaitingInput SessionStatus = "awaiting_input"
	// StatusDone means the session ended / last run completed and was closed.
	StatusDone SessionStatus = "done"
	// StatusError means the last run failed.
	StatusError SessionStatus = "error"
)

// PermissionMode is the "answering mode" for a session, mirroring the claude CLI
// --permission-mode flag.
type PermissionMode string

const (
	// ModeDefault asks for every tool that needs permission (interactive menu).
	ModeDefault PermissionMode = "default"
	// ModeAcceptEdits auto-approves file edits, still asks for other tools.
	ModeAcceptEdits PermissionMode = "acceptEdits"
	// ModePlan lets claude plan without executing changes.
	ModePlan PermissionMode = "plan"
	// ModeAuto never asks (maps to claude bypassPermissions).
	ModeAuto PermissionMode = "auto"
)

// ValidPermissionMode reports whether m is a known mode.
func ValidPermissionMode(m PermissionMode) bool {
	switch m {
	case ModeDefault, ModeAcceptEdits, ModePlan, ModeAuto:
		return true
	default:
		return false
	}
}

// CLIPermissionMode maps a ccdash mode to the claude CLI --permission-mode value.
func (m PermissionMode) CLIPermissionMode() string {
	if m == ModeAuto {
		return "bypassPermissions"
	}
	return string(m)
}

// Project is a working directory that sessions are launched against.
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"createdAt"`
}

// Session is a single claude conversation tied to a project. Multiple sessions
// can run concurrently and continue in the background.
//
// WorktreePath, Branch, and BaseCommit are populated when the project is in a
// git repo: on session creation the backend runs `git worktree add` against
// the project's repo root and the claude CLI is launched with WorktreePath as
// its working directory so parallel sessions on one repo can't clobber each
// other. For non-git projects all three fields are empty and claude runs in
// the project path directly.
type Session struct {
	ID              string         `json:"id"`
	ProjectID       string         `json:"projectId"`
	ClaudeSessionID string         `json:"claudeSessionId"`
	Title           string         `json:"title"`
	Status          SessionStatus  `json:"status"`
	Model           string         `json:"model"`
	PermissionMode  PermissionMode `json:"permissionMode"`
	WorktreePath    string         `json:"worktreePath"`
	Branch          string         `json:"branch"`
	BaseCommit      string         `json:"baseCommit"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

// PermissionRequest is a pending tool-permission decision surfaced to the UI.
// Pending requests live in backend memory for the life of a run.
type PermissionRequest struct {
	ID          string          `json:"id"`
	SessionID   string          `json:"sessionId"`
	ToolName    string          `json:"toolName"`
	Input       json.RawMessage `json:"input"`
	Summary     string          `json:"summary"`
	Suggestions []string        `json:"suggestions"`
	CreatedAt   time.Time       `json:"createdAt"`
}

// Message is one entry in a session transcript.
type Message struct {
	ID          string       `json:"id"`
	SessionID   string       `json:"sessionId"`
	Role        string       `json:"role"`
	Content     string       `json:"content"`
	CreatedAt   time.Time    `json:"createdAt"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Attachment is an image the user pasted onto a message. The raw bytes are
// served separately (GET /api/attachments/{id}); Data is never JSON-encoded.
type Attachment struct {
	ID        string    `json:"id"`
	MessageID string    `json:"messageId"`
	SessionID string    `json:"sessionId"`
	Name      string    `json:"name"`
	MediaType string    `json:"mediaType"`
	CreatedAt time.Time `json:"createdAt"`
	Data      []byte    `json:"-"`
}

// UsageRecord captures token/cost usage for a single claude run.
type UsageRecord struct {
	ID           string    `json:"id"`
	SessionID    string    `json:"sessionId"`
	Model        string    `json:"model"`
	InputTokens  int       `json:"inputTokens"`
	OutputTokens int       `json:"outputTokens"`
	CostUSD      float64   `json:"costUsd"`
	CreatedAt    time.Time `json:"createdAt"`
}

// SessionUsage is the per-session aggregate used in a UsageSummary.
type SessionUsage struct {
	SessionID    string  `json:"sessionId"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd"`
}

// UsageSummary is the dashboard-wide usage rollup.
type UsageSummary struct {
	TotalInputTokens  int            `json:"totalInputTokens"`
	TotalOutputTokens int            `json:"totalOutputTokens"`
	TotalCostUSD      float64        `json:"totalCostUsd"`
	BySession         []SessionUsage `json:"bySession"`
}

// UsageWindow is one rate-limit window from the Claude subscription /usage view:
// how much of the limit is consumed and when it resets.
type UsageWindow struct {
	UsedPercent float64    `json:"usedPercent"`
	ResetsAt    *time.Time `json:"resetsAt,omitempty"`
}

// Utilization mirrors what the `claude` CLI shows via /usage for a Pro/Max
// subscription: the session (5-hour) and weekly limit windows. Windows the
// account does not have (e.g. a separate Opus limit) are nil.
type Utilization struct {
	Session   *UsageWindow `json:"session,omitempty"`  // five_hour
	Week      *UsageWindow `json:"week,omitempty"`     // seven_day (all models)
	WeekOpus  *UsageWindow `json:"weekOpus,omitempty"` // seven_day_opus
	FetchedAt time.Time    `json:"fetchedAt"`
}
