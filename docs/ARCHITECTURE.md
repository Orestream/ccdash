# Architecture

ccdash is a two-process app: a Go backend that owns all state and talks to the
`claude` CLI, and a React SPA that renders it. They communicate over a JSON REST
API plus a WebSocket for live push. The contract is in [`API.md`](./API.md).

```
                          ┌─────────────────────────────────────────────┐
   Browser (React :10000) │                  backend :10001             │
  ┌──────────────────┐    │  ┌──────────┐   ┌───────────┐   ┌─────────┐ │
  │ Sidebar / Session│    │  │  api     │──▶│  session  │──▶│ claude  │ │──▶ `claude` CLI
  │ List / View      │◀──▶│  │ (chi)    │   │  Manager  │   │ Runner  │ │    (one process
  │ UsageBar         │    │  └────┬─────┘   └─────┬─────┘   └─────────┘ │     per run)
  └────────┬─────────┘    │       │               │                     │
           │  REST /api   │       ▼               ▼                     │
           │──────────────┼──▶ ┌──────┐      ┌─────────┐                │
           │  WS   /ws    │    │store │      │  ws.Hub │───broadcast────┤
           │◀─────────────┼────│SQLite│      └─────────┘   (status,     │
           └──────────────┘    └──────┘                     messages,   │
                          │                                 usage)      │
                          └─────────────────────────────────────────────┘
```

## Backend packages

- **`models`** — domain types shared everywhere. JSON tags are camelCase to match
  the frontend.
- **`store`** — SQLite persistence using the pure-Go `modernc.org/sqlite` driver
  (no CGO). Schema is embedded from `schema.sql`. One open connection
  (`SetMaxOpenConns(1)`) because SQLite is single-writer. Timestamps are stored
  as RFC3339Nano text. Returns `ErrNotFound` for missing rows.
- **`claude`** — integration with the CLI. `Runner` is an interface; `CLIRunner`
  spawns `claude -p <prompt> --output-format stream-json --verbose [--model …]
  [--resume <id>]` in the project's directory and parses each JSON line into a
  typed `Event` (`system`/`assistant`/`result`/`error`). The parser
  (`parseLine`) is pure and unit-tested without spawning a process.
- **`ws`** — a transport-agnostic fan-out `Hub`. Subscribers get a buffered
  channel; slow consumers are dropped rather than blocking the producer (they
  resync over REST).
- **`session`** — the orchestrator. `SendMessage` persists the user message,
  flips the session to `processing`, and launches the run in its own goroutine,
  so **many sessions advance concurrently and keep running in the background**.
  In-flight runs are tracked in a `cancels` map so `Stop` can cancel a run's
  context (which kills the CLI process). Assistant output is accumulated and
  persisted; usage is recorded; status transitions and new rows are broadcast
  over the hub.
- **`api`** — chi router. REST handlers are thin wrappers over `store`/`session`;
  `/ws` upgrades to a WebSocket, subscribes to the hub, and pumps events to the
  client (with periodic pings and a reader goroutine to detect disconnects).

## Frontend

- **`api/client.ts`** — typed `fetch` wrapper; uses relative `/api/...` URLs
  proxied by Vite to the backend.
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

## Known gaps (skeleton)

See [`TODO.md`](../TODO.md). Notably: streaming assistant deltas to the UI (today
the full assistant message is sent once per turn), auth, multi-user, and
persisting the WebSocket reconnect/replay cursor.
