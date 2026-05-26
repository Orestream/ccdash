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
	"github.com/robinmalmstrom/ccdash/backend/internal/session"
	"github.com/robinmalmstrom/ccdash/backend/internal/store"
	"github.com/robinmalmstrom/ccdash/backend/internal/utilization"
	"github.com/robinmalmstrom/ccdash/backend/internal/ws"
)

// version is overridable at build time via -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	addr := envOr("CCDASH_ADDR", ":10001")
	dbPath := envOr("CCDASH_DB", "ccdash.db")
	claudeBin := envOr("CCDASH_CLAUDE_BIN", "claude")
	credPath := envOr("CCDASH_CRED_PATH", defaultCredPath())

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	hub := ws.NewHub()
	mgr := session.New(st, hub, claude.NewCLIRunner(claudeBin))
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

	// Graceful shutdown on SIGINT/SIGTERM.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
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
