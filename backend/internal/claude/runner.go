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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
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

// Image is an inline image attached to a user turn. Data is the raw decoded
// bytes; the runner base64-encodes them into the stream-json content block.
type Image struct {
	Name      string
	MediaType string
	Data      []byte
}

// Session is a live claude process.
type Session interface {
	// Send queues a user turn, optionally with inline images.
	Send(text string, images []Image) error
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

// buildArgs assembles the `claude` CLI arguments for a streaming session.
//
// --permission-prompt-tool stdio is what makes interactive approvals possible:
// it tells the CLI to delegate tool-permission decisions to us over the
// stream-json control channel (emitting can_use_tool control_requests) instead
// of auto-denying them, which is what -p print mode does without it. Without
// this flag every permission-gated tool is silently blocked and no approval
// request ever reaches the dashboard.
func buildArgs(req StartRequest) []string {
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
		"--permission-prompt-tool", "stdio",
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
	return args
}

// Start implements Runner by spawning a streaming claude process.
func (r *CLIRunner) Start(ctx context.Context, req StartRequest) (Session, error) {
	args := buildArgs(req)

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

func (s *cliSession) Send(text string, images []Image) error {
	return s.writeJSON(userMessage(text, images))
}

// userMessage builds a stream-json user turn. With no images, content is a plain
// string (the simple, common case). With images, content is an ordered block
// array: the text, then for each image a small text label (its name, e.g.
// "image-1.png") followed by the base64 image block — so the model can tie the
// user's "in image-1 we see…" references to the right picture.
func userMessage(text string, images []Image) map[string]any {
	var content any = text
	if len(images) > 0 {
		blocks := make([]map[string]any, 0, len(images)*2+1)
		if text != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": text})
		}
		for _, img := range images {
			if img.Name != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": img.Name})
			}
			blocks = append(blocks, map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": img.MediaType,
					"data":       base64.StdEncoding.EncodeToString(img.Data),
				},
			})
		}
		content = blocks
	}
	return map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": content},
	}
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
		for _, ev := range parseLine(scanner.Bytes()) {
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
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
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

// parseLine converts one stream-json line into zero or more Events. Most lines
// yield a single event; a finalized assistant message may yield several (text
// and tool_use blocks, in order). Blank lines, replayed user turns and anything
// we don't surface yield nil.
func parseLine(line []byte) []Event {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	var raw rawEvent
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil
	}

	switch raw.Type {
	case "system":
		return []Event{{Kind: KindSystem, ClaudeSessionID: raw.SessionID, Model: raw.Model}}

	case "stream_event":
		return parseStreamEvent(raw)

	case "assistant":
		return parseAssistant(raw)

	case "control_request":
		if raw.Request == nil || raw.Request.Subtype != "can_use_tool" {
			return nil
		}
		return []Event{{
			Kind:        KindPermission,
			RequestID:   raw.RequestID,
			ToolName:    raw.Request.ToolName,
			ToolInput:   raw.Request.Input,
			Suggestions: raw.Request.Suggestions,
		}}

	case "result":
		if raw.IsError {
			return []Event{{Kind: KindError, Err: fmt.Errorf("claude result error: %s", raw.Result)}}
		}
		ev := Event{Kind: KindResult, Text: raw.Result, ClaudeSessionID: raw.SessionID, Model: raw.Model, CostUSD: raw.TotalCostUSD}
		if raw.Usage != nil {
			ev.InputTokens = raw.Usage.InputTokens
			ev.OutputTokens = raw.Usage.OutputTokens
		}
		return []Event{ev}

	default:
		return nil
	}
}

// parseAssistant expands a finalized assistant message into ordered events. Text
// blocks coalesce into assistant events; tool_use blocks become tool events that
// carry the *complete* input — unlike the streamed content_block_start, whose
// input is still empty mid-stream. This is why tool messages are sourced here
// rather than from the partial stream.
func parseAssistant(raw rawEvent) []Event {
	if raw.Message == nil {
		return nil
	}
	var events []Event
	var text strings.Builder
	flush := func() {
		if text.Len() == 0 {
			return
		}
		events = append(events, Event{
			Kind:            KindAssistant,
			Text:            text.String(),
			Model:           raw.Message.Model,
			ClaudeSessionID: raw.SessionID,
		})
		text.Reset()
	}
	for _, c := range raw.Message.Content {
		switch c.Type {
		case "text":
			text.WriteString(c.Text)
		case "tool_use":
			flush()
			events = append(events, Event{
				Kind:      KindToolUse,
				ToolName:  c.Name,
				ToolInput: c.Input,
			})
		}
	}
	flush()
	return events
}

func parseStreamEvent(raw rawEvent) []Event {
	if raw.Event == nil {
		return nil
	}
	if raw.Event.Type == "content_block_delta" {
		switch raw.Event.Delta.Type {
		case "text_delta":
			if raw.Event.Delta.Text == "" {
				return nil
			}
			return []Event{{Kind: KindText, Text: raw.Event.Delta.Text}}
		case "thinking_delta":
			if raw.Event.Delta.Thinking == "" {
				return nil
			}
			return []Event{{Kind: KindThinking, Text: raw.Event.Delta.Thinking}}
		}
	}
	return nil
}
