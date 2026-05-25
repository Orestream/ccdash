# CLAUDE.md — working guide for ccdash

**ccdash** is an advanced dashboard for driving the `claude` CLI: it tracks
multiple projects, starts sessions, runs them **in parallel in the background**,
and shows each session's live status (processing / awaiting input / done) and
token+cost usage. React frontend, Go backend.

## Repo layout

```
backend/                Go API server (chi + SQLite, pure-Go modernc driver)
  cmd/ccdash/           main entrypoint
  internal/models/      shared domain types (JSON = camelCase)
  internal/store/       SQLite persistence (schema.sql embedded)
  internal/claude/      `claude` CLI runner (Runner interface + stream-json parser)
  internal/session/     session manager: runs prompts concurrently, broadcasts state
  internal/ws/          fan-out WebSocket hub
  internal/api/         chi router + HTTP handlers + /ws upgrade
frontend/               Vite + React + TypeScript dashboard (dev server on :10000)
  src/api/              typed REST client
  src/hooks/            useWebSocket, useSessions
  src/components/       Sidebar, SessionList, SessionView, StatusBadge, UsageBar
docs/                   API.md (the contract), ARCHITECTURE.md
.claude/                settings.json (perms + Stop hook), hooks/auto-commit.sh
```

## Ports

- Backend API: **:8080** (`CCDASH_ADDR` to override)
- Frontend dev server: **:10000** (proxies `/api` and `/ws` → :8080)

## Commands

Run from the repo root via the Makefile, or directly in each subdir.

| Task | Make | Direct |
|------|------|--------|
| Install deps | `make setup` | `cd frontend && npm install` |
| Run backend | `make dev-backend` | `cd backend && go run ./cmd/ccdash` |
| Run frontend | `make dev-frontend` | `cd frontend && npm run dev` |
| Test all | `make test` | — |
| Lint all | `make lint` | — |
| Backend test | `make test-backend` | `cd backend && go test ./...` |
| Backend lint | `make lint-backend` | `cd backend && golangci-lint run ./...` |
| Frontend test | `make test-frontend` | `cd frontend && npm test` |
| Frontend lint | `make lint-frontend` | `cd frontend && npm run lint` |
| Frontend build | — | `cd frontend && npm run build` |

## Workflow (please follow)

1. **Work on a branch**, never commit directly to `main` unless trivial.
2. **Write/extend tests** alongside any behavior change. Backend: `_test.go`
   next to the code. Frontend: `*.test.ts(x)` with Vitest.
3. Before considering work done, the suite must be green:
   - Backend: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`, `go test ./...`
   - Frontend: `npm run lint`, `npm run build`, `npm test`
4. **Auto-commit:** a `Stop` hook (`.claude/hooks/auto-commit.sh`) runs the test
   + lint suites when an agent finishes a turn and, **only if everything is
   green and there are changes**, stages and commits them. It never pushes.
   - Output is logged to `.claude/auto-commit.log`.
   - Disable for a turn with `CCDASH_AUTOCOMMIT=0`.
   - It does not run frontend checks unless `frontend/node_modules` exists.

## Conventions

- **API contract lives in `docs/API.md`** — it is the source of truth shared by
  both stacks. Change it there first, then update both sides. JSON is camelCase.
- **Go:** standard library + chi; errors wrapped with `%w`; keep packages in
  `internal/`; table-ish tests; no CGO (SQLite via `modernc.org/sqlite`).
- **TypeScript:** strict mode; types in `src/types.ts` mirror `docs/API.md`;
  components are function components; the WebSocket is the live source of truth
  with REST as fallback.
- The `claude` runner is behind the `claude.Runner` interface so it can be faked
  in tests — never call the real CLI from a unit test.

## Status model

`idle → processing → awaiting_input` per turn; `done` when closed; `error` on
failure. The session manager persists status and broadcasts `session.status`
events over the hub so every connected client updates live.
