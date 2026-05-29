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

// commitFile writes content to path under repo, stages it, and commits it on
// the named branch. Returns the resulting HEAD sha.
func commitFile(t *testing.T, r Runner, repo, branch, relpath, content, message string) string {
	t.Helper()
	ctx := context.Background()
	if branch != "" {
		// Checkout the branch (creating it if necessary).
		if _, err := r.Run(ctx, repo, "rev-parse", "--verify", branch); err == nil {
			mustRun(t, r, repo, "checkout", branch)
		} else {
			mustRun(t, r, repo, "checkout", "-b", branch)
		}
	}
	full := filepath.Join(repo, relpath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustRun(t, r, repo, "add", relpath)
	mustRun(t, r, repo, "commit", "-m", message)
	sha, err := HeadCommit(ctx, r, repo)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	return sha
}

func TestDiffRange(t *testing.T) {
	repo, r := initRepo(t)
	ctx := context.Background()

	// Modify README on a feature branch.
	commitFile(t, r, repo, "feature", "README.md", "hello\nfeature line\n", "edit on feature")

	patch, err := DiffRange(ctx, r, repo, "main", "feature")
	if err != nil {
		t.Fatalf("DiffRange: %v", err)
	}
	if len(patch) == 0 {
		t.Fatal("expected non-empty patch")
	}
	if !strings.Contains(string(patch), "feature line") {
		t.Fatalf("patch missing added line: %s", patch)
	}

	// Empty diff: from == to.
	mustRun(t, r, repo, "checkout", "main")
	empty, err := DiffRange(ctx, r, repo, "main", "main")
	if err != nil {
		t.Fatalf("DiffRange empty: %v", err)
	}
	if empty != nil {
		t.Fatalf("expected nil patch for empty diff, got %q", empty)
	}
}

func TestApplyAndReversePatchModifiedFile(t *testing.T) {
	repo, r := initRepo(t)
	ctx := context.Background()

	commitFile(t, r, repo, "feature", "README.md", "hello\nfeature line\n", "edit on feature")
	patch, err := DiffRange(ctx, r, repo, "main", "feature")
	if err != nil {
		t.Fatalf("DiffRange: %v", err)
	}
	mustRun(t, r, repo, "checkout", "main")

	if err := ApplyPatch(ctx, r, repo, patch); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if !strings.Contains(string(got), "feature line") {
		t.Fatalf("file not updated after apply: %s", got)
	}

	if err := ReversePatch(ctx, r, repo, patch); err != nil {
		t.Fatalf("ReversePatch: %v", err)
	}
	got, _ = os.ReadFile(filepath.Join(repo, "README.md"))
	if strings.Contains(string(got), "feature line") {
		t.Fatalf("file still modified after reverse: %s", got)
	}
}

func TestApplyAndReversePatchNewFile(t *testing.T) {
	repo, r := initRepo(t)
	ctx := context.Background()

	commitFile(t, r, repo, "feature", "added.txt", "brand new\n", "add new file on feature")
	patch, err := DiffRange(ctx, r, repo, "main", "feature")
	if err != nil {
		t.Fatalf("DiffRange: %v", err)
	}
	mustRun(t, r, repo, "checkout", "main")

	added := filepath.Join(repo, "added.txt")
	if _, err := os.Stat(added); !os.IsNotExist(err) {
		t.Fatalf("expected added.txt missing on main, got err=%v", err)
	}
	if err := ApplyPatch(ctx, r, repo, patch); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if _, err := os.Stat(added); err != nil {
		t.Fatalf("expected added.txt present after apply, got %v", err)
	}

	if err := ReversePatch(ctx, r, repo, patch); err != nil {
		t.Fatalf("ReversePatch: %v", err)
	}
	if _, err := os.Stat(added); !os.IsNotExist(err) {
		t.Fatalf("expected added.txt removed after reverse, got %v", err)
	}
}

func TestApplyPatchConflictErrors(t *testing.T) {
	repo, r := initRepo(t)
	ctx := context.Background()

	commitFile(t, r, repo, "feature", "README.md", "hello\nfeature line\n", "edit on feature")
	patch, err := DiffRange(ctx, r, repo, "main", "feature")
	if err != nil {
		t.Fatalf("DiffRange: %v", err)
	}
	mustRun(t, r, repo, "checkout", "main")
	// Wedge main so the patch can't apply cleanly.
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("totally different content\n"), 0o644); err != nil {
		t.Fatalf("wedge README: %v", err)
	}
	mustRun(t, r, repo, "add", "README.md")
	mustRun(t, r, repo, "commit", "-m", "wedge main")

	if err := ApplyPatch(ctx, r, repo, patch); err == nil {
		t.Fatalf("expected conflict error")
	}
}

func TestCommitAll(t *testing.T) {
	repo, r := initRepo(t)
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("data\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := CommitAll(ctx, r, repo, "feat: add new"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	out, err := r.Run(ctx, repo, "log", "-1", "--pretty=%s")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if strings.TrimSpace(string(out)) != "feat: add new" {
		t.Fatalf("unexpected commit subject: %q", out)
	}

	// Clean tree: no-op, no error.
	if err := CommitAll(ctx, r, repo, "noop"); err != nil {
		t.Fatalf("CommitAll on clean tree: %v", err)
	}
}
