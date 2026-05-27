#!/usr/bin/env bash
# Run backend + frontend behind a single user-facing port (:10000).
#
# Combined stdout/stderr is prefixed [backend]/[frontend] for the terminal;
# raw per-project streams are tee'd to .claude/logs/{backend,frontend}.log so
# agents can grep them without sed prefixes in the way.
set -uo pipefail
cd "$(dirname "$0")/.."

LOGDIR=".claude/logs"
mkdir -p "$LOGDIR"

# Tear the whole process group down on Ctrl-C / exit.
trap 'trap - INT TERM EXIT; kill 0' INT TERM EXIT

# Backend: gow reruns `go run` on .go changes (incremental compile via build cache).
(
  cd backend && exec gow run ./cmd/ccdash
) 2>&1 | tee "$LOGDIR/backend.log" | sed -u 's/^/[backend] /' &

# Frontend: Vite dev server (HMR) on the internal port; Go proxies to it.
(
  cd frontend && exec npm run dev
) 2>&1 | tee "$LOGDIR/frontend.log" | sed -u 's/^/[frontend] /' &

wait
