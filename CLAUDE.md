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
  internal/api/         chi router + HTTP handlers + /ws upgrade + dev/prod
                        frontend handler (Vite reverse proxy vs //go:embed)
  internal/web/         staged `dist/` for the embedded prod frontend (gitignored)
frontend/               Vite + React + TypeScript dashboard
  src/api/              typed REST client
  src/hooks/            useWebSocket, useSessions
  src/components/       Sidebar, SessionList, SessionView, StatusBadge, UsageBar
scripts/dev.sh          launches backend (via gow) + frontend behind one port
docs/                   API.md (the contract), ARCHITECTURE.md
.claude/                settings.json (perms + Stop hook), hooks/auto-commit.sh,
                        logs/{backend,frontend}.log (gitignored, written by dev.sh)
```

## Ports

ccdash is single-port. **Go always owns `:10000`** — the only port the browser
hits. In dev Go reverse-proxies non-`/api`/`/ws` requests to Vite on the
internal port; in prod Go serves the embedded bundle.

- User-facing port: **:10000** (`CCDASH_ADDR` to override). Backend API, WS, and
  the frontend all live here.
- Internal Vite dev port: **:10001** (set in `frontend/vite.config.ts`). Not
  user-facing; HMR is configured with `clientPort: 10000` so the browser only
  ever talks to `:10000`.

## Build modes

- **Default (no tag) = dev:** `go run`/`go build` produces a backend whose
  frontend handler is a reverse proxy to Vite. The suite (`go build ./...`,
  `go test ./...`) needs no built `dist/` to be present.
- **`-tags prod` = release:** `make build` builds the frontend, stages
  `frontend/dist` into `backend/internal/web/dist`, then `go build -tags prod`
  embeds it via `//go:embed all:dist`. Output: a single self-contained binary.

## Commands

Run from the repo root via the Makefile, or directly in each subdir.

| Task | Make | Direct |
|------|------|--------|
| Install deps (incl. `gow`) | `make setup` | `cd frontend && npm install` |
| Run dev (one port, unified logs) | `make dev` | `./scripts/dev.sh` |
| Run backend standalone | `make dev-backend` | `cd backend && go run ./cmd/ccdash` |
| Run Vite standalone | `make dev-frontend` | `cd frontend && npm run dev` |
| Build single prod binary | `make build` | — |
| Test all | `make test` | — |
| Lint all | `make lint` | — |
| Backend test | `make test-backend` | `cd backend && go test ./...` |
| Backend lint | `make lint-backend` | `cd backend && golangci-lint run ./...` |
| Frontend test | `make test-frontend` | `cd frontend && npm test` |
| Frontend lint | `make lint-frontend` | `cd frontend && npm run lint` |
| Frontend build | — | `cd frontend && npm run build` |

`make dev` runs `scripts/dev.sh`, which starts `gow run ./cmd/ccdash` (backend
live-reload) and `npm run dev` (frontend HMR) in parallel. The terminal gets a
combined stream prefixed `[backend]`/`[frontend]`; raw streams are tee'd to
`.claude/logs/backend.log` and `.claude/logs/frontend.log` for agents to grep.

## Workflow (please follow)

1. **Work on a branch**, never commit directly to `main` unless trivial.
2. **Write/extend tests** alongside any behavior change. Backend: `_test.go`
   next to the code. Frontend: `*.test.ts(x)` with Vitest.
3. Before considering work done, the suite must be green **without `-tags prod`
   and without a built `dist/`** (this matches what `make test` and the
   auto-commit hook run on a fresh checkout):
   - Backend: `go build ./...`, `go vet ./...`, `golangci-lint run ./...`, `go test ./...`
   - Frontend: `npm run lint`, `npm run build`, `npm test`
   For a final smoke test of the production bundle run `make build && ./backend/ccdash`.
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

Per turn: `idle → processing → idle` (a completed turn returns to `idle`, ready
for the next message). If a tool needs a permission decision the turn pauses on
`awaiting_approval` until answered, then resumes `processing`. `awaiting_input`
is reserved for a genuine interactive-dialog prompt (not permissions); `done`
when closed; `error` on failure. The session manager persists status and
broadcasts `session.status` events over the hub so every connected client
updates live.
