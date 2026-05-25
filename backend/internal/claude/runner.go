// Package claude integrates with the `claude` CLI. The Runner interface lets the
// session manager spawn real CLI runs in production and inject fakes in tests.
// Production runs use `claude -p <prompt> --output-format stream-json --verbose`
// and parse the streaming JSON events into a typed channel.
package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// Kind classifies a streamed event from a claude run.
type Kind string

const (
	// KindSystem is the init event carrying the claude session id and model.
	KindSystem Kind = "system"
	// KindAssistant carries assistant text output.
	KindAssistant Kind = "assistant"
	// KindResult is the terminal event carrying usage and cost.
	KindResult Kind = "result"
	// KindError signals the run failed.
	KindError Kind = "error"
)

// RunRequest describes a single claude invocation.
type RunRequest struct {
	Prompt          string
	Cwd             string
	Model           string
	ResumeSessionID string // claude-side session id to resume, if any
}

// Event is one normalized item from a claude run stream.
type Event struct {
	Kind            Kind
	Text            string
	ClaudeSessionID string
	Model           string
	InputTokens     int
	OutputTokens    int
	CostUSD         float64
	Err             error
}

// Runner abstracts execution of a claude prompt.
type Runner interface {
	// Run starts a prompt and returns a channel of events that is closed when
	// the run finishes. Cancelling ctx terminates the underlying process.
	Run(ctx context.Context, req RunRequest) (<-chan Event, error)
}

// CLIRunner runs the real `claude` binary.
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

// Run implements Runner by spawning the claude CLI in streaming-JSON mode.
func (r *CLIRunner) Run(ctx context.Context, req RunRequest) (<-chan Event, error) {
	args := []string{"-p", req.Prompt, "--output-format", "stream-json", "--verbose"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.ResumeSessionID != "" {
		args = append(args, "--resume", req.ResumeSessionID)
	}

	cmd := exec.CommandContext(ctx, r.Bin, args...)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	out := make(chan Event, 16)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			if ev, ok := parseLine(scanner.Bytes()); ok {
				out <- ev
			}
		}
		waitErr := cmd.Wait()
		if waitErr != nil && ctx.Err() == nil {
			msg := stderr.String()
			if msg == "" {
				msg = waitErr.Error()
			}
			out <- Event{Kind: KindError, Err: fmt.Errorf("claude run failed: %s", msg)}
		}
	}()
	return out, nil
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

type rawMessage struct {
	Model   string       `json:"model"`
	Content []rawContent `json:"content"`
	Usage   *rawUsage    `json:"usage"`
}

type rawEvent struct {
	Type         string      `json:"type"`
	Subtype      string      `json:"subtype"`
	SessionID    string      `json:"session_id"`
	Model        string      `json:"model"`
	Message      *rawMessage `json:"message"`
	Result       string      `json:"result"`
	TotalCostUSD float64     `json:"total_cost_usd"`
	Usage        *rawUsage   `json:"usage"`
	IsError      bool        `json:"is_error"`
}

// parseLine converts one stream-json line into an Event. The bool is false for
// lines that carry no event we care about (blank lines, tool echoes, etc.).
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
		return Event{
			Kind:            KindAssistant,
			Text:            text,
			Model:           raw.Message.Model,
			ClaudeSessionID: raw.SessionID,
		}, true
	case "result":
		if raw.IsError {
			return Event{Kind: KindError, Err: fmt.Errorf("claude result error: %s", raw.Result)}, true
		}
		ev := Event{
			Kind:            KindResult,
			Text:            raw.Result,
			ClaudeSessionID: raw.SessionID,
			Model:           raw.Model,
			CostUSD:         raw.TotalCostUSD,
		}
		if raw.Usage != nil {
			ev.InputTokens = raw.Usage.InputTokens
			ev.OutputTokens = raw.Usage.OutputTokens
		}
		return ev, true
	default:
		return Event{}, false
	}
}
