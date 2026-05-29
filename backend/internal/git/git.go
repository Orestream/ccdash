// Package git is a thin wrapper around the `git` CLI used by the session
// manager to set up and tear down per-session worktrees. Each session in a
// git-backed project gets its own worktree + branch so parallel runs can't
// clobber each other's working tree.
//
// The package is intentionally minimal: it shells out to git rather than
// reaching for a Go git library so ccdash inherits whatever git the user
// already has (worktree semantics, hooks, ignore rules) and so tests can run
// against a real binary.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// stdinRunner is an optional capability: Runners that can pipe a stdin payload
// (for `git apply`) implement this so callers don't have to construct a custom
// command. The exec Runner satisfies it; tests can leave it unimplemented and
// rely on a real git binary or skip patch-applying tests.
type stdinRunner interface {
	RunStdin(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error)
}

// Runner runs a `git` subcommand from a working directory and returns its
// combined stdout. It is an interface so tests can stub git out.
type Runner interface {
	Run(ctx context.Context, dir string, args ...string) ([]byte, error)
}

// ExecRunner is the real-process implementation of Runner.
type ExecRunner struct {
	// Bin is the git binary path; "git" if empty.
	Bin string
}

// NewExecRunner returns a Runner that invokes the git binary on PATH.
func NewExecRunner() ExecRunner { return ExecRunner{} }

// Run implements Runner by spawning `git` with the given args. Stderr is
// captured into the returned error on non-zero exit; stdout is returned on
// success. A 30s default deadline guards against hangs when the caller did
// not supply one.
func (r ExecRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return r.RunStdin(ctx, dir, nil, args...)
}

// RunStdin is like Run but pipes stdin into the git process. Used by commands
// like `git apply` that read a patch from stdin.
func (r ExecRunner) RunStdin(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	bin := r.Bin
	if bin == "" {
		bin = "git"
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		stderrTxt := strings.TrimSpace(stderr.String())
		if stderrTxt != "" {
			return nil, fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), stderrTxt, err)
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// ExitCode returns the exit code of err if it wraps an *exec.ExitError, or -1.
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// IsRepo reports whether path is inside a git work tree. On success it returns
// the absolute path of the repo root (`git rev-parse --show-toplevel`). A
// non-repo path returns ok=false with err=nil; only unexpected failures (git
// binary missing, permission denied) return an error.
func IsRepo(ctx context.Context, r Runner, path string) (repoRoot string, ok bool, err error) {
	out, runErr := r.Run(ctx, path, "rev-parse", "--show-toplevel")
	if runErr != nil {
		// git returns 128 when run outside a repo. Treat that as "not a repo"
		// rather than a hard error so callers can fall back cleanly.
		if exitCode(runErr) == 128 {
			return "", false, nil
		}
		return "", false, runErr
	}
	return strings.TrimSpace(string(out)), true, nil
}

// HeadCommit returns the current commit hash of repoRoot's HEAD.
func HeadCommit(ctx context.Context, r Runner, repoRoot string) (string, error) {
	out, err := r.Run(ctx, repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// AddWorktree creates a new worktree at dest off baseCommit, on a new branch.
// The branch must not already exist; collisions surface as the underlying git
// error.
func AddWorktree(ctx context.Context, r Runner, repoRoot, dest, branch, baseCommit string) error {
	if dest == "" || branch == "" || baseCommit == "" {
		return fmt.Errorf("git AddWorktree: dest, branch, and baseCommit are required")
	}
	_, err := r.Run(ctx, repoRoot, "worktree", "add", "-b", branch, dest, baseCommit)
	return err
}

// RemoveWorktree removes a worktree directory and its administrative metadata.
// If force is true, removal proceeds even with uncommitted changes in the
// worktree. If the worktree directory is already gone from disk, RemoveWorktree
// falls back to `worktree prune` so the registration is still cleaned up.
func RemoveWorktree(ctx context.Context, r Runner, repoRoot, dest string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, dest)
	if _, err := r.Run(ctx, repoRoot, args...); err != nil {
		// Worktree dir already gone, or never registered: prune and call it good.
		msg := err.Error()
		if strings.Contains(msg, "is not a working tree") || strings.Contains(msg, "does not exist") {
			_, pruneErr := r.Run(ctx, repoRoot, "worktree", "prune")
			return pruneErr
		}
		return err
	}
	return nil
}

// DeleteBranch deletes a branch from repoRoot. If force is true, the branch is
// removed even if it is not merged.
func DeleteBranch(ctx context.Context, r Runner, repoRoot, branch string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err := r.Run(ctx, repoRoot, "branch", flag, branch)
	return err
}

// DiffRange returns the patch produced by `git diff from...to` (three-dot:
// changes on `to` since it diverged from `from`). Returns (nil, nil) when the
// diff is empty so callers can treat "no changes" as a normal outcome.
func DiffRange(ctx context.Context, r Runner, repoRoot, from, to string) ([]byte, error) {
	if repoRoot == "" || from == "" || to == "" {
		return nil, fmt.Errorf("git DiffRange: repoRoot, from, and to are required")
	}
	out, err := r.Run(ctx, repoRoot, "diff", from+"..."+to)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	return out, nil
}

// ApplyPatch applies a patch to repoRoot by piping it into `git apply`. A
// dry-run via `--check` runs first so conflicts surface as a clear error
// containing git's stderr (not silently leaving the working tree wedged).
func ApplyPatch(ctx context.Context, r Runner, repoRoot string, patch []byte) error {
	if len(bytes.TrimSpace(patch)) == 0 {
		return fmt.Errorf("git ApplyPatch: empty patch")
	}
	sr, ok := r.(stdinRunner)
	if !ok {
		return fmt.Errorf("git ApplyPatch: runner does not support stdin")
	}
	if _, err := sr.RunStdin(ctx, repoRoot, patch, "apply", "--check"); err != nil {
		return fmt.Errorf("patch does not apply cleanly: %w", err)
	}
	if _, err := sr.RunStdin(ctx, repoRoot, patch, "apply"); err != nil {
		return fmt.Errorf("git apply: %w", err)
	}
	return nil
}

// ReversePatch reverses a previously-applied patch. New files added by the
// original patch are deleted on reverse (git apply --reverse handles this).
func ReversePatch(ctx context.Context, r Runner, repoRoot string, patch []byte) error {
	if len(bytes.TrimSpace(patch)) == 0 {
		return fmt.Errorf("git ReversePatch: empty patch")
	}
	sr, ok := r.(stdinRunner)
	if !ok {
		return fmt.Errorf("git ReversePatch: runner does not support stdin")
	}
	if _, err := sr.RunStdin(ctx, repoRoot, patch, "apply", "--reverse", "--check"); err != nil {
		return fmt.Errorf("reverse patch does not apply cleanly: %w", err)
	}
	if _, err := sr.RunStdin(ctx, repoRoot, patch, "apply", "--reverse"); err != nil {
		return fmt.Errorf("git apply --reverse: %w", err)
	}
	return nil
}

// CommitAll stages every change in repoRoot's working tree and creates a
// commit with the given message. Returns nil (treating it as a no-op) when
// there is nothing to commit so callers don't have to special-case clean
// trees.
func CommitAll(ctx context.Context, r Runner, repoRoot, message string) error {
	if _, err := r.Run(ctx, repoRoot, "add", "-A"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	// Detect a clean tree via `diff --cached --quiet`: exit 0 = nothing staged.
	out, err := r.Run(ctx, repoRoot, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil
	}
	if _, err := r.Run(ctx, repoRoot, "commit", "-m", message); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}
