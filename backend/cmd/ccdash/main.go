// Command ccdash is the backend server for the Claude Code agent dashboard.
// It serves the REST + WebSocket API defined in docs/API.md.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/robinmalmstrom/ccdash/backend/internal/api"
	"github.com/robinmalmstrom/ccdash/backend/internal/claude"
	"github.com/robinmalmstrom/ccdash/backend/internal/commit"
	gitwt "github.com/robinmalmstrom/ccdash/backend/internal/git"
	"github.com/robinmalmstrom/ccdash/backend/internal/session"
	"github.com/robinmalmstrom/ccdash/backend/internal/store"
	"github.com/robinmalmstrom/ccdash/backend/internal/utilization"
	"github.com/robinmalmstrom/ccdash/backend/internal/ws"
)

// version is overridable at build time via -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	addr := envOr("CCDASH_ADDR", ":10000")
	dbPath := envOr("CCDASH_DB", "ccdash.db")
	claudeBin := envOr("CCDASH_CLAUDE_BIN", "claude")
	credPath := envOr("CCDASH_CRED_PATH", defaultCredPath())
	worktreeRoot := envOr("CCDASH_WORKTREE_ROOT", defaultWorktreeRoot())

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	hub := ws.NewHub()
	mgr := session.NewWithGit(st, hub, claude.NewCLIRunner(claudeBin), gitwt.NewExecRunner(), worktreeRoot)
	mgr.SetCommitGenerator(commit.ClaudeGenerator{Bin: claudeBin})
	srv := api.NewServer(st, mgr, hub, utilization.NewFetcher(credPath), version)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("ccdash %s listening on %s (db=%s)", version, addr, dbPath)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Shutdown on SIGINT/SIGTERM. We want this to feel near-instant: WebSocket
	// connections are hijacked, so http.Server.Shutdown can't drain them on its
	// own — closing the hub closes each writer's channel, which unwinds the
	// writer goroutine, which closes the underlying conn, which fails the
	// reader. http.Server.Close() then evicts anything still hanging on. A
	// second signal is treated as a hard exit so the user is never stuck.
	stop := make(chan os.Signal, 2)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down...")

	go func() {
		<-stop
		log.Println("force quit")
		os.Exit(130)
	}()

	hub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	_ = httpServer.Close()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// defaultCredPath locates the claude CLI's OAuth credentials file.
func defaultCredPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".claude", ".credentials.json")
	}
	return ""
}

// defaultWorktreeRoot returns where per-session git worktrees should live.
// Follows XDG: $XDG_STATE_HOME/ccdash/worktrees, falling back to
// $HOME/.local/state/ccdash/worktrees. Returns "" if no home is resolvable,
// in which case the manager skips worktree isolation entirely.
func defaultWorktreeRoot() string {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "ccdash", "worktrees")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "ccdash", "worktrees")
	}
	return ""
}
