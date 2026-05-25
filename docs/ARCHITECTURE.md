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

See [`TODO.md`](../TODO.md). The streaming-JSON control protocol shapes (permission
`control_request`/`control_response`, partial-message events) are implemented
against the documented format but **not yet verified against a live claude run** —
they're isolated to `claude/runner.go` for easy adjustment. Other gaps: auth,
multi-user, persisting the WebSocket reconnect/replay cursor, and recovering
in-flight runs after a backend restart.
