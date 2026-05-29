// Package commit produces a one-line commit message for a diff, used by the
// session manager's Accept flow to commit a previewed worktree's changes onto
// the project's main checkout. The default implementation shells out to the
// claude CLI; tests use FakeGenerator to return a canned message.
package commit

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// FallbackMessage is returned when no generator is available or the configured
// generator failed. It keeps Accept from blocking on commit-message generation.
const FallbackMessage = "ccdash: applied changes from session"

// prompt is what the default generator asks claude to do. Kept short so the
// model returns a clean one-liner (no body, no trailers).
const prompt = `Write a single-line conventional-commit-style message (~70 chars max) summarizing the diff piped to you on stdin. Output only the message; no body, no trailers, no backticks, no quotes.`

// MessageGenerator produces a commit message describing the given diff. The
// returned message should be a single line without trailing newline.
type MessageGenerator interface {
	Generate(ctx context.Context, diff []byte) (string, error)
}

// ClaudeGenerator shells out to the claude CLI to produce a message. It is
// best-effort: if the binary is missing, exits non-zero, or returns an empty
// string, callers should fall back to FallbackMessage.
type ClaudeGenerator struct {
	// Bin is the claude binary path; "claude" when empty.
	Bin string
}

// Generate runs `claude -p <prompt>` with the diff piped to stdin. A short
// timeout caps the call so Accept doesn't stall on a hung CLI. The output is
// trimmed and only its first non-empty line is returned (the model sometimes
// adds rationale).
func (g ClaudeGenerator) Generate(ctx context.Context, diff []byte) (string, error) {
	bin := g.Bin
	if bin == "" {
		bin = "claude"
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, bin, "-p", prompt)
	cmd.Stdin = bytes.NewReader(diff)
	// Prevent the Stop-hook from recursing into another claude run when this
	// invocation finishes.
	cmd.Env = append(cmd.Environ(), "CCDASH_AUTOCOMMIT=0")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		stderrTxt := strings.TrimSpace(stderr.String())
		if stderrTxt != "" {
			return "", fmt.Errorf("claude -p: %s: %w", stderrTxt, err)
		}
		return "", fmt.Errorf("claude -p: %w", err)
	}
	return firstLine(stdout.String()), nil
}

// FakeGenerator is a deterministic generator for tests. It returns Msg (or
// FallbackMessage when Msg is empty) and never errors.
type FakeGenerator struct {
	Msg string
}

// Generate implements MessageGenerator.
func (f FakeGenerator) Generate(_ context.Context, _ []byte) (string, error) {
	if f.Msg == "" {
		return FallbackMessage, nil
	}
	return f.Msg, nil
}

// GenerateOrFallback runs g and returns its result, replacing any error or
// empty output with FallbackMessage so the caller can always commit. A nil
// generator also yields the fallback.
func GenerateOrFallback(ctx context.Context, g MessageGenerator, diff []byte) string {
	if g == nil {
		return FallbackMessage
	}
	msg, err := g.Generate(ctx, diff)
	if err != nil {
		return FallbackMessage
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return FallbackMessage
	}
	return msg
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return line
	}
	return ""
}
