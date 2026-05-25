#!/usr/bin/env bash
# Stop hook: when an agent finishes a turn, run the test + lint suites and, if
# everything is green AND there are changes, stage and commit them. Never pushes.
#
# Design notes:
#   - Always exits 0 so it never blocks the agent from stopping.
#   - A no-op when there is nothing to commit.
#   - Skips entirely if CCDASH_AUTOCOMMIT=0 (escape hatch).
#   - Logs to .claude/auto-commit.log for after-the-fact inspection.
#   - Only touches the working tree of THIS repo.
set -uo pipefail

ROOT="${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null)}"
[ -z "$ROOT" ] && exit 0
cd "$ROOT" || exit 0

LOG="$ROOT/.claude/auto-commit.log"
log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >>"$LOG"; }

[ "${CCDASH_AUTOCOMMIT:-1}" = "0" ] && exit 0

# Nothing to do if not a git repo or no changes.
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || exit 0
if [ -z "$(git status --porcelain)" ]; then
  exit 0
fi

log "changes detected; running checks"
FAILED=""

# --- Backend checks ---
if [ -f "$ROOT/backend/go.mod" ]; then
  ( cd "$ROOT/backend" && go test ./... ) >>"$LOG" 2>&1 || FAILED="$FAILED go-test"
  if command -v golangci-lint >/dev/null 2>&1; then
    ( cd "$ROOT/backend" && golangci-lint run ./... ) >>"$LOG" 2>&1 || FAILED="$FAILED go-lint"
  fi
fi

# --- Frontend checks (only if deps are installed) ---
if [ -f "$ROOT/frontend/package.json" ] && [ -d "$ROOT/frontend/node_modules" ]; then
  ( cd "$ROOT/frontend" && npm run -s lint ) >>"$LOG" 2>&1 || FAILED="$FAILED fe-lint"
  ( cd "$ROOT/frontend" && npm run -s test ) >>"$LOG" 2>&1 || FAILED="$FAILED fe-test"
fi

if [ -n "$FAILED" ]; then
  log "checks FAILED:$FAILED — skipping commit"
  exit 0
fi

# All green: commit everything.
git add -A
COUNT=$(git diff --cached --name-only | wc -l | tr -d ' ')
if [ "$COUNT" = "0" ]; then
  exit 0
fi
MSG="chore: auto-commit ($COUNT files, tests green)

Committed automatically by the ccdash Stop hook after a passing test + lint run.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
git commit -m "$MSG" >>"$LOG" 2>&1 && log "committed $COUNT files"
exit 0
