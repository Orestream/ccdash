# ccdash — Claude Code Agent Dashboard

An advanced web dashboard for driving the [`claude`](https://claude.com/claude-code)
CLI. Manage multiple projects, launch sessions, run them **in parallel in the
background**, and watch live status and token/cost usage across all of them.

> Status: **skeleton** — the full architecture is in place (REST + WebSocket API,
> SQLite persistence, concurrent session manager, live React UI) and is wired to
> the real `claude` CLI. See [`TODO.md`](./TODO.md) for what's next.

## Features

- **Multiple projects** — register working directories and group sessions under them.
- **Parallel sessions** — each session runs claude in its own goroutine; switching
  the UI to another chat does not pause the others.
- **Live status** — every session reports `idle` / `processing` / `awaiting_input`
  / `done` / `error`, pushed to the browser over a WebSocket.
- **Usage tracking** — per-session and dashboard-wide token counts and USD cost.

## Stack

| Layer | Tech |
|-------|------|
| Frontend | React 18 + TypeScript, Vite, React Router, Vitest |
| Backend | Go, [chi](https://github.com/go-chi/chi) router, [gorilla/websocket](https://github.com/gorilla/websocket) |
| Storage | SQLite via pure-Go [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) (no CGO) |
| CLI integration | spawns `claude -p --output-format stream-json` and parses the stream |

## Quick start

Prerequisites: Go 1.24+, Node 20+, and the `claude` CLI on your `PATH`.

```bash
make setup          # install frontend deps + download Go modules

# In two terminals:
make dev-backend    # http://localhost:8080
make dev-frontend   # http://localhost:10000  (proxies /api and /ws to :8080)
```

Open <http://localhost:10000>.

## Configuration (backend env vars)

| Var | Default | Meaning |
|-----|---------|---------|
| `CCDASH_ADDR` | `:8080` | listen address |
| `CCDASH_DB` | `ccdash.db` | SQLite database path |
| `CCDASH_CLAUDE_BIN` | `claude` | path to the claude binary |

## Development

```bash
make test           # backend + frontend test suites
make lint           # backend + frontend linters
```

The API contract is documented in [`docs/API.md`](./docs/API.md) and the design
in [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md). Agent workflow conventions
(including the auto-commit-on-green hook) are in [`CLAUDE.md`](./CLAUDE.md).

## Tests & CI

- Backend: `go test ./...`, `go vet`, `golangci-lint`.
- Frontend: Vitest + Testing Library, ESLint, `tsc` build.
- GitHub Actions runs all of the above on every push/PR (`.github/workflows/ci.yml`).
- A local `Stop` hook auto-commits when the suite is green — see `CLAUDE.md`.

## License

TBD.
