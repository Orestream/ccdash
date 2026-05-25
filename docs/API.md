# ccdash API Contract

The backend (Go, chi router) listens on `:10001` by default and exposes a JSON REST
API under `/api` plus a WebSocket endpoint at `/ws` for live updates. This document
is the **source of truth** shared between the Go backend and the React frontend.

All timestamps are RFC 3339 strings. All IDs are UUID v4 strings. JSON field names
are `camelCase`.

## Conventions

- Base URL: `http://localhost:10001`
- Content type: `application/json` for request and response bodies.
- Errors use HTTP status codes with a body of `{ "error": "message" }`.
- During development the backend enables permissive CORS for the Vite dev server
  (`http://localhost:10000`).

## Data models

### Project
```json
{
  "id": "uuid",
  "name": "My App",
  "path": "/home/robin/code/my-app",
  "createdAt": "2026-05-25T12:00:00Z"
}
```

### Session
```json
{
  "id": "uuid",
  "projectId": "uuid",
  "claudeSessionId": "claude-side session id, empty until first run",
  "title": "Add auth flow",
  "status": "idle | processing | awaiting_input | done | error",
  "model": "claude-opus-4-7",
  "createdAt": "2026-05-25T12:00:00Z",
  "updatedAt": "2026-05-25T12:01:00Z"
}
```

`status` semantics:
- `idle` — created, no prompt running.
- `processing` — claude is actively working on a prompt.
- `awaiting_input` — claude finished a turn and is waiting for the next user message.
- `done` — session ended / last run completed successfully and was closed.
- `error` — the last run failed.

### Message
```json
{
  "id": "uuid",
  "sessionId": "uuid",
  "role": "user | assistant | system | tool",
  "content": "text",
  "createdAt": "2026-05-25T12:00:30Z"
}
```

### UsageRecord
```json
{
  "id": "uuid",
  "sessionId": "uuid",
  "model": "claude-opus-4-7",
  "inputTokens": 1234,
  "outputTokens": 567,
  "costUsd": 0.0421,
  "createdAt": "2026-05-25T12:00:45Z"
}
```

### UsageSummary
```json
{
  "totalInputTokens": 12000,
  "totalOutputTokens": 4300,
  "totalCostUsd": 1.23,
  "bySession": [
    { "sessionId": "uuid", "inputTokens": 100, "outputTokens": 50, "costUsd": 0.01 }
  ]
}
```

## REST endpoints

| Method | Path | Body | Returns |
|--------|------|------|---------|
| GET    | `/api/health` | — | `{ "status": "ok", "version": "..." }` |
| GET    | `/api/projects` | — | `Project[]` |
| POST   | `/api/projects` | `{ "name", "path" }` | `Project` (201) |
| GET    | `/api/projects/{id}` | — | `Project` |
| DELETE | `/api/projects/{id}` | — | 204 |
| GET    | `/api/projects/{id}/sessions` | — | `Session[]` |
| POST   | `/api/projects/{id}/sessions` | `{ "title?", "model?" }` | `Session` (201) |
| GET    | `/api/sessions` | — | `Session[]` (all sessions, newest first) |
| GET    | `/api/sessions/{id}` | — | `Session` |
| GET    | `/api/sessions/{id}/messages` | — | `Message[]` |
| POST   | `/api/sessions/{id}/messages` | `{ "content" }` | `Message` (202; runs async, status flips to `processing`) |
| POST   | `/api/sessions/{id}/stop` | — | `Session` (cancels a running prompt) |
| GET    | `/api/sessions/{id}/usage` | — | `UsageRecord[]` |
| GET    | `/api/usage` | — | `UsageSummary` |

## WebSocket `/ws`

The client connects once and receives a stream of JSON events. The client does not
need to send anything (server → client only for now). Each event:

```json
{
  "type": "session.status | session.message | session.usage | project.created | project.deleted",
  "ts": "2026-05-25T12:00:00Z",
  "payload": { }
}
```

Event payloads:
- `session.status` → a full `Session` object (status changed).
- `session.message` → a full `Message` object (new message appended; may be streamed
  in chunks where `content` is a partial delta and `role` is `assistant`).
- `session.usage` → a full `UsageRecord`.
- `project.created` / `project.deleted` → a full `Project` object.

The frontend should treat the WebSocket as the live source of truth and fall back to
REST polling if the socket drops.
