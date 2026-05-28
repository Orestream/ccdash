package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a fresh git repo with one commit, returning its root.
// The test is skipped if a git binary isn't on PATH (sandboxed CI without git).
func initRepo(t *testing.T) (string, Runner) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH; skipping integration test")
	}
	dir := t.TempDir()
	r := NewExecRunner()
	ctx := context.Background()

	// init with a fixed default branch so cross-version git behavior is stable.
	if _, err := r.Run(ctx, dir, "init", "-b", "main"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	// Local identity so commits work in a clean env.
	mustRun(t, r, dir, "config", "user.email", "test@example.com")
	mustRun(t, r, dir, "config", "user.name", "ccdash test")

	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustRun(t, r, dir, "add", "README.md")
	mustRun(t, r, dir, "commit", "-m", "init")
	return dir, r
}

func mustRun(t *testing.T, r Runner, dir string, args ...string) {
	t.Helper()
	if _, err := r.Run(context.Background(), dir, args...); err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
}

func TestIsRepoTrue(t *testing.T) {
	dir, r := initRepo(t)
	root, ok, err := IsRepo(context.Background(), r, dir)
	if err != nil {
		t.Fatalf("IsRepo err: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true for git repo")
	}
	// On macOS, /tmp is a symlink to /private/tmp; rev-parse resolves it. Just
	// require root to contain the temp dir's base name.
	if !strings.HasSuffix(root, filepath.Base(dir)) {
		t.Fatalf("expected repo root to end with %s, got %s", filepath.Base(dir), root)
	}
}

func TestIsRepoFalse(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
	dir := t.TempDir()
	r := NewExecRunner()
	_, ok, err := IsRepo(context.Background(), r, dir)
	if err != nil {
		t.Fatalf("IsRepo on non-repo dir: unexpected err %v", err)
	}
	if ok {
		t.Fatalf("expected ok=false for non-repo dir")
	}
}

func TestHeadCommit(t *testing.T) {
	dir, r := initRepo(t)
	sha, err := HeadCommit(context.Background(), r, dir)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if len(sha) != 40 {
		t.Fatalf("expected 40-char sha, got %q (%d)", sha, len(sha))
	}
}

func TestAddRemoveWorktreeAndDeleteBranch(t *testing.T) {
	repo, r := initRepo(t)
	base, err := HeadCommit(context.Background(), r, repo)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	// Worktree dir must NOT exist beforehand (git creates it).
	wt := filepath.Join(t.TempDir(), "wt-1")
	branch := "ccdash/abc12345"

	if err := AddWorktree(context.Background(), r, repo, wt, branch, base); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	// The worktree dir should now exist and be the branch's checkout.
	if st, err := os.Stat(wt); err != nil || !st.IsDir() {
		t.Fatalf("worktree dir not created: err=%v", err)
	}
	// Branch should be in `git branch` output.
	out, err := r.Run(context.Background(), repo, "branch", "--list", branch)
	if err != nil {
		t.Fatalf("branch --list: %v", err)
	}
	if !strings.Contains(string(out), branch) {
		t.Fatalf("expected branch %s, got %q", branch, out)
	}

	// Remove worktree, then delete the branch.
	if err := RemoveWorktree(context.Background(), r, repo, wt, false); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("expected worktree dir gone, got err=%v", err)
	}
	if err := DeleteBranch(context.Background(), r, repo, branch, true); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	out, _ = r.Run(context.Background(), repo, "branch", "--list", branch)
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("expected branch removed, still got %q", out)
	}
}

func TestRemoveWorktreeMissingDirPrunes(t *testing.T) {
	repo, r := initRepo(t)
	base, _ := HeadCommit(context.Background(), r, repo)
	wt := filepath.Join(t.TempDir(), "wt-gone")
	branch := "ccdash/gone1234"

	if err := AddWorktree(context.Background(), r, repo, wt, branch, base); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	// User deletes the worktree dir behind our back.
	if err := os.RemoveAll(wt); err != nil {
		t.Fatalf("rm worktree: %v", err)
	}
	// RemoveWorktree should tolerate the missing dir and fall back to prune.
	if err := RemoveWorktree(context.Background(), r, repo, wt, false); err != nil {
		t.Fatalf("RemoveWorktree on missing dir: %v", err)
	}
}

func TestAddWorktreeMissingArgs(t *testing.T) {
	// No real git invocation needed — caller-side validation.
	err := AddWorktree(context.Background(), nopRunner{}, "/tmp/repo", "", "br", "sha")
	if err == nil {
		t.Fatalf("expected validation error for empty dest")
	}
}

type nopRunner struct{}

func (nopRunner) Run(context.Context, string, ...string) ([]byte, error) { return nil, nil }
