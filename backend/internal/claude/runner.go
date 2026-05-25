// Package claude integrates with the `claude` CLI over its streaming-JSON
// protocol. A Session is a long-lived `claude` process that we feed user turns
// and permission decisions on stdin and read normalized events from on stdout:
//
//	claude -p --input-format stream-json --output-format stream-json \
//	       --include-partial-messages --verbose [--model X] \
//	       [--permission-mode M] [--resume ID]
//
// Keeping the process alive lets sessions stream partial output ("live
// thinking"), accept follow-up messages, and answer interactive tool-permission
// requests (the control protocol). The Runner interface lets the session
// manager inject fakes in tests.
//
// NOTE: the exact stdin/stdout shapes for the control protocol below are based
// on the documented streaming-JSON format and may need adjustment after testing
// against a specific claude version. They are isolated to parseLine / Send /
// Respond so adjustments stay local.
package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// Kind classifies a normalized event from a claude run.
type Kind string

const (
	// KindSystem is the init event carrying the claude session id and model.
	KindSystem Kind = "system"
	// KindText is a streaming assistant text delta.
	KindText Kind = "text"
	// KindThinking is a streaming reasoning ("thinking") delta.
	KindThinking Kind = "thinking"
	// KindToolUse signals the assistant started using a tool.
	KindToolUse Kind = "tool_use"
	// KindAssistant is a complete assistant text message for a turn.
	KindAssistant Kind = "assistant"
	// KindPermission is a tool-permission request (can_use_tool) awaiting a decision.
	KindPermission Kind = "permission"
	// KindResult is the per-turn terminal event carrying usage and cost.
	KindResult Kind = "result"
	// KindError signals the run failed.
	KindError Kind = "error"
)

// Decision is a permission outcome sent back to claude.
type Decision string

const (
	// DecisionAllow approves a tool request.
	DecisionAllow Decision = "allow"
	// DecisionDeny rejects a tool request.
	DecisionDeny Decision = "deny"
)

// StartRequest configures a new claude session process.
type StartRequest struct {
	Cwd             string
	Model           string
	ResumeSessionID string // claude-side session id to resume, if any
	PermissionMode  string // claude --permission-mode value (default/acceptEdits/plan/bypassPermissions)
}

// Event is one normalized item from a claude run stream.
type Event struct {
	Kind            Kind
	Text            string
	ClaudeSessionID string
	Model           string

	// Permission request fields (KindPermission / KindToolUse).
	RequestID   string
	ToolName    string
	ToolInput   json.RawMessage
	Suggestions []string

	// Result fields (KindResult).
	InputTokens  int
	OutputTokens int
	CostUSD      float64

	Err error
}

// Session is a live claude process.
type Session interface {
	// Send queues a user turn.
	Send(text string) error
	// Respond answers a pending permission request. updatedInput should echo the
	// tool's original input on allow; message is shown to claude on deny.
	Respond(requestID string, decision Decision, updatedInput json.RawMessage, message string) error
	// SetMode changes the permission mode mid-session (best-effort).
	SetMode(mode string) error
	// Events returns the event stream; it is closed when the process exits.
	Events() <-chan Event
	// Close terminates the process.
	Close() error
}

// Runner starts claude sessions.
type Runner interface {
	Start(ctx context.Context, req StartRequest) (Session, error)
}

// CLIRunner starts real `claude` processes.
type CLIRunner struct {
	Bin string
}

// NewCLIRunner returns a runner for the given binary ("claude" if empty).
func NewCLIRunner(bin string) *CLIRunner {
	if bin == "" {
		bin = "claude"
	}
	return &CLIRunner{Bin: bin}
}

// Start implements Runner by spawning a streaming claude process.
func (r *CLIRunner) Start(ctx context.Context, req StartRequest) (Session, error) {
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.PermissionMode != "" {
		args = append(args, "--permission-mode", req.PermissionMode)
	}
	if req.ResumeSessionID != "" {
		args = append(args, "--resume", req.ResumeSessionID)
	}

	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	s := &cliSession{
		cmd:    cmd,
		stdin:  stdin,
		cancel: cancel,
		ctx:    ctx,
		events: make(chan Event, 32),
	}
	cmd.Stderr = &s.stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start claude: %w", err)
	}
	go s.readLoop(stdout)
	return s, nil
}

type cliSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel context.CancelFunc
	ctx    context.Context
	events chan Event
	stderr bytes.Buffer

	writeMu   sync.Mutex
	closeOnce sync.Once
}

func (s *cliSession) Events() <-chan Event { return s.events }

func (s *cliSession) Send(text string) error {
	return s.writeJSON(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": text,
		},
	})
}

func (s *cliSession) Respond(requestID string, decision Decision, updatedInput json.RawMessage, message string) error {
	var result map[string]any
	if decision == DecisionAllow {
		if updatedInput == nil {
			updatedInput = json.RawMessage(`{}`)
		}
		result = map[string]any{"behavior": "allow", "updatedInput": updatedInput}
	} else {
		result = map[string]any{"behavior": "deny", "message": message}
	}
	return s.writeJSON(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   result,
		},
	})
}

func (s *cliSession) SetMode(mode string) error {
	return s.writeJSON(map[string]any{
		"type": "control_request",
		"request": map[string]any{
			"subtype": "set_permission_mode",
			"mode":    mode,
		},
	})
}

func (s *cliSession) Close() error {
	s.closeOnce.Do(func() {
		_ = s.stdin.Close()
		s.cancel()
	})
	return nil
}

func (s *cliSession) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.stdin.Write(data); err != nil {
		return fmt.Errorf("write to claude: %w", err)
	}
	return nil
}

func (s *cliSession) readLoop(stdout io.Reader) {
	defer close(s.events)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if ev, ok := parseLine(scanner.Bytes()); ok {
			s.events <- ev
		}
	}
	waitErr := s.cmd.Wait()
	if waitErr != nil && s.ctx.Err() == nil {
		msg := s.stderr.String()
		if msg == "" {
			msg = waitErr.Error()
		}
		s.events <- Event{Kind: KindError, Err: fmt.Errorf("claude run failed: %s", msg)}
	}
}

// --- streaming JSON parsing ---

type rawUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type rawContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type rawDelta struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}

type rawContentBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type rawStreamInner struct {
	Type         string          `json:"type"`
	Delta        rawDelta        `json:"delta"`
	ContentBlock rawContentBlock `json:"content_block"`
}

type rawMessage struct {
	Model   string       `json:"model"`
	Content []rawContent `json:"content"`
}

type rawPermission struct {
	Subtype     string          `json:"subtype"`
	ToolName    string          `json:"tool_name"`
	Input       json.RawMessage `json:"input"`
	Suggestions []string        `json:"permission_suggestions"`
}

type rawEvent struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype"`
	SessionID    string          `json:"session_id"`
	Model        string          `json:"model"`
	Message      *rawMessage     `json:"message"`
	Event        *rawStreamInner `json:"event"`
	RequestID    string          `json:"request_id"`
	Request      *rawPermission  `json:"request"`
	Result       string          `json:"result"`
	TotalCostUSD float64         `json:"total_cost_usd"`
	Usage        *rawUsage       `json:"usage"`
	IsError      bool            `json:"is_error"`
}

// parseLine converts one stream-json line into an Event. The bool is false for
// lines that carry no event we surface (blank lines, replayed user turns, etc.).
func parseLine(line []byte) (Event, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return Event{}, false
	}
	var raw rawEvent
	if err := json.Unmarshal(line, &raw); err != nil {
		return Event{}, false
	}

	switch raw.Type {
	case "system":
		return Event{Kind: KindSystem, ClaudeSessionID: raw.SessionID, Model: raw.Model}, true

	case "stream_event":
		return parseStreamEvent(raw)

	case "assistant":
		if raw.Message == nil {
			return Event{}, false
		}
		var text string
		for _, c := range raw.Message.Content {
			if c.Type == "text" {
				text += c.Text
			}
		}
		if text == "" {
			return Event{}, false
		}
		return Event{Kind: KindAssistant, Text: text, Model: raw.Message.Model, ClaudeSessionID: raw.SessionID}, true

	case "control_request":
		if raw.Request == nil || raw.Request.Subtype != "can_use_tool" {
			return Event{}, false
		}
		return Event{
			Kind:        KindPermission,
			RequestID:   raw.RequestID,
			ToolName:    raw.Request.ToolName,
			ToolInput:   raw.Request.Input,
			Suggestions: raw.Request.Suggestions,
		}, true

	case "result":
		if raw.IsError {
			return Event{Kind: KindError, Err: fmt.Errorf("claude result error: %s", raw.Result)}, true
		}
		ev := Event{Kind: KindResult, Text: raw.Result, ClaudeSessionID: raw.SessionID, Model: raw.Model, CostUSD: raw.TotalCostUSD}
		if raw.Usage != nil {
			ev.InputTokens = raw.Usage.InputTokens
			ev.OutputTokens = raw.Usage.OutputTokens
		}
		return ev, true

	default:
		return Event{}, false
	}
}

func parseStreamEvent(raw rawEvent) (Event, bool) {
	if raw.Event == nil {
		return Event{}, false
	}
	switch raw.Event.Type {
	case "content_block_delta":
		switch raw.Event.Delta.Type {
		case "text_delta":
			if raw.Event.Delta.Text == "" {
				return Event{}, false
			}
			return Event{Kind: KindText, Text: raw.Event.Delta.Text}, true
		case "thinking_delta":
			if raw.Event.Delta.Thinking == "" {
				return Event{}, false
			}
			return Event{Kind: KindThinking, Text: raw.Event.Delta.Thinking}, true
		}
	case "content_block_start":
		if raw.Event.ContentBlock.Type == "tool_use" {
			return Event{
				Kind:      KindToolUse,
				ToolName:  raw.Event.ContentBlock.Name,
				ToolInput: raw.Event.ContentBlock.Input,
			}, true
		}
	}
	return Event{}, false
}
