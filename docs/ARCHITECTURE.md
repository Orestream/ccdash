# Architecture

ccdash is a single-port app: the Go backend owns `:10000` and serves both the
API and the React SPA — the browser only ever talks to one origin. The same Go
codebase has two build modes:

- **Default (no tag) = dev.** Non-`/api`/`/ws` requests are reverse-proxied to
  the Vite dev server on an internal port (`:10001`). HMR tunnels through the
  proxy, so the browser still gets sub-second updates without seeing Vite.
- **`-tags prod` = release.** The built `frontend/dist` is embedded via
  `//go:embed all:dist` in `backend/internal/web` and served by `spaHandler`,
  with SPA fallback to `index.html` for unknown paths. The output is a single
  self-contained binary.

Either way the API + WebSocket contract is the one in [`API.md`](./API.md).

```
            Browser (one origin: http://localhost:10000)
            ┌──────────────────┐
            │  React SPA       │
            │  Sidebar/Session │
            │  UsageBar / …    │
            └────┬─────┬───────┘
       REST /api │     │ WS /ws
                 ▼     ▼
   ┌─────────────────────────────────────────────────────────┐
   │              backend :10000 (chi router)                │
   │  ┌──────────┐   ┌───────────┐   ┌─────────┐             │
   │  │  /api    │──▶│  session  │──▶│ claude  │──▶ `claude` CLI
   │  │  /ws     │   │  Manager  │   │ Runner  │   (one process per run)
   │  └────┬─────┘   └─────┬─────┘   └─────────┘             │
   │       ▼               ▼                                 │
   │   ┌──────┐        ┌─────────┐                           │
   │   │store │        │  ws.Hub │── broadcast (status,      │
   │   │SQLite│        └─────────┘   messages, usage)        │
   │   └──────┘                                              │
   │                                                         │
   │  NotFound ──▶ frontendHandler:                          │
   │     dev  → httputil.ReverseProxy → Vite :10001 (HMR)    │
   │     prod → spaHandler over //go:embed dist/             │
   └─────────────────────────────────────────────────────────┘
```

## Backend packages

- **`models`** — domain types shared everywhere. JSON tags are camelCase to match
  the frontend.
- **`store`** — SQLite persistence using the pure-Go `modernc.org/sqlite` driver
  (no CGO). Schema is embedded from `schema.sql`. One open connection
  (`SetMaxOpenConns(1)`) because SQLite is single-writer. Timestamps are stored
  as RFC3339Nano text. Returns `ErrNotFound` for missing rows.
- **`claude`** — integration with the CLI over its streaming-JSON protocol.
  `Runner.Start` spawns a **long-lived** process: `claude -p --input-format
  stream-json --output-format stream-json --include-partial-messages --verbose
  [--model …] [--permission-mode …] [--resume <id>]`. The returned `Session`
  lets us `Send` user turns and `Respond` to permission requests on stdin, and
  exposes an `Events()` channel. `parseLine` normalizes each stdout line into a
  typed `Event` (`system` / `text`+`thinking` deltas / `tool_use` / `assistant`
  / `permission` / `result` / `error`) and is pure + unit-tested without
  spawning a process. The exact control-protocol shapes are isolated to
  `parseLine`/`Send`/`Respond` so they can be adjusted after testing.
- **`ws`** — a transport-agnostic fan-out `Hub`. Subscribers get a buffered
  channel; slow consumers are dropped rather than blocking the producer (they
  resync over REST).
- **`session`** — the orchestrator. Each ccdash session owns a live
  `claude.Session` whose events are pumped in a dedicated goroutine, so **many
  sessions stream and progress concurrently in the background**. `SendMessage`
  persists the user turn, (re)starts the live process if needed, and forwards
  the turn; the pump broadcasts streaming `session.delta`s (text/thinking),
  persists final messages + tool activity, records usage, and surfaces
  permission requests. Pending permission requests are held in memory per
  session; `RespondPermission` answers them (with `allow_always` remembering a
  tool for the session), `SetMode` changes the answering mode, and `Stop` closes
  the process. Status flows `idle → processing → awaiting_approval/awaiting_input`.
- **`api`** — chi router. REST handlers are thin wrappers over `store`/`session`;
  `/ws` upgrades to a WebSocket, subscribes to the hub, and pumps events to the
  client (with periodic pings and a reader goroutine to detect disconnects).
  The request logger is scoped to `/api` + `/ws` so the dev frontend handler
  (which proxies every JS module and HMR poll to Vite) doesn't drown backend
  logs. The frontend handler is a build-tagged file: `frontend_dev.go`
  (`!prod`) returns an `httputil.ReverseProxy` to `http://localhost:10001`;
  `frontend_prod.go` (`prod`) returns `spaHandler` over the embedded FS from
  `internal/web`. `spaHandler` is plain `fs.FS` + `http.FileServer` with a
  fallback to `index.html` for client-routed paths and is unit-tested via
  `fstest.MapFS` — no build tag, no real `dist/` required.
- **`web`** — staging dir for the production frontend. `make build` copies
  `frontend/dist` into `backend/internal/web/dist`, then `go build -tags prod`
  pulls it into `Dist embed.FS`. The `dist/` is gitignored; only the package's
  Go files (a doc file and the build-tagged embed file) are checked in.

## Frontend

- **`api/client.ts`** — typed `fetch` wrapper; uses relative `/api/...` URLs.
  Because Go now owns the user-facing port, requests go straight to the
  backend (no Vite proxy in front).
- **`hooks/useWebSocket.ts`** — single connection to `/ws`, parses `WsEvent`,
  reconnects with backoff, and fans events out to subscribers.
- **`hooks/useSessions.ts`** — loads sessions over REST and merges live
  `session.status` events so the UI is always current.
- **Components** — `Sidebar` (projects), `SessionList` + `StatusBadge` (live
  status), `SessionView` (transcript + prompt + stop), `UsageBar` (totals).

## Concurrency & lifecycle

- Each prompt runs in its own goroutine; the manager's `WaitGroup` is used by
  tests and graceful shutdown to await completion.
- The server shuts down gracefully on SIGINT/SIGTERM via `http.Server.Shutdown`.
- Cancelling a run returns the session to `awaiting_input` rather than erroring.

## Why these choices

- **SQLite (modernc)** — durable usage history and session state with zero
  external services and no CGO, so the binary stays trivially buildable.
- **chi** — idiomatic `net/http` handlers with just enough middleware.
- **Runner interface** — keeps the CLI out of unit tests and makes the streaming
  parser independently testable.
- **Hub of byte slices** — the live layer doesn't care about WebSocket specifics
  and is easy to test.
- **Default build = dev** — the proxy variant compiles without a built
  `dist/`, so `make test`, `go build ./...`, and the auto-commit hook all stay
  green on a fresh checkout. `-tags prod` (used only by `make build`) is the
  one path that requires `npm run build` + a staged `internal/web/dist`.
- **`gow` for backend reload** — incremental compile via Go's build cache, so
  edits cycle in well under a second without restarting Vite (frontend state
  survives backend restarts because the two are separate processes).

## Known gaps (skeleton)

See [`TODO.md`](../TODO.md). The streaming-JSON control protocol shapes (permission
`control_request`/`control_response`, partial-message events) are implemented
against the documented format but **not yet verified against a live claude run** —
they're isolated to `claude/runner.go` for easy adjustment. Other gaps: auth,
multi-user, persisting the WebSocket reconnect/replay cursor, and recovering
in-flight runs after a backend restart.
