// Package models defines the core domain types shared across the backend.
// JSON tags use camelCase to match the API contract in docs/API.md.
package models

import "time"

// SessionStatus is the lifecycle state of a session as surfaced to the UI.
type SessionStatus string

const (
	// StatusIdle means the session exists but has no prompt running.
	StatusIdle SessionStatus = "idle"
	// StatusProcessing means claude is actively working on a prompt.
	StatusProcessing SessionStatus = "processing"
	// StatusAwaitingInput means claude finished a turn and awaits the next message.
	StatusAwaitingInput SessionStatus = "awaiting_input"
	// StatusDone means the session ended / last run completed and was closed.
	StatusDone SessionStatus = "done"
	// StatusError means the last run failed.
	StatusError SessionStatus = "error"
)

// Project is a working directory that sessions are launched against.
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"createdAt"`
}

// Session is a single claude conversation tied to a project. Multiple sessions
// can run concurrently and continue in the background.
type Session struct {
	ID              string        `json:"id"`
	ProjectID       string        `json:"projectId"`
	ClaudeSessionID string        `json:"claudeSessionId"`
	Title           string        `json:"title"`
	Status          SessionStatus `json:"status"`
	Model           string        `json:"model"`
	CreatedAt       time.Time     `json:"createdAt"`
	UpdatedAt       time.Time     `json:"updatedAt"`
}

// Message is one entry in a session transcript.
type Message struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
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
