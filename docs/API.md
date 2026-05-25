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
  "status": "idle | processing | awaiting_input | awaiting_approval | done | error",
  "model": "claude-opus-4-7",
  "permissionMode": "default | acceptEdits | plan | auto",
  "createdAt": "2026-05-25T12:00:00Z",
  "updatedAt": "2026-05-25T12:01:00Z"
}
```

`status` semantics:
- `idle` — created, no prompt running.
- `processing` — claude is actively working on a prompt.
- `awaiting_approval` — claude paused on a tool that needs a permission decision;
  see the pending `PermissionRequest`(s) for this session.
- `awaiting_input` — claude finished a turn and is waiting for the next user message.
- `done` — session ended / last run completed successfully and was closed.
- `error` — the last run failed.

`permissionMode` ("answering mode") controls how tool permissions are handled,
mirroring the claude CLI `--permission-mode`:
- `default` — ask for every tool that needs permission (interactive approval menu).
- `acceptEdits` — "Edit mode": auto-approve file edits, still ask for other tools.
- `plan` — "Plan mode": claude plans without executing changes.
- `auto` — "Auto mode": never ask (maps to claude `bypassPermissions`).

### Message
```json
{
  "id": "uuid",
  "sessionId": "uuid",
  "role": "user | assistant | thinking | tool | system",
  "content": "text",
  "createdAt": "2026-05-25T12:00:30Z"
}
```

`thinking` messages carry the model's reasoning; `tool` messages carry a
human-readable line about a tool the assistant used (e.g. `Bash: git status`).

### PermissionRequest
Emitted when a session pauses for a tool-permission decision. Pending requests
live in backend memory for the life of the run (not persisted across restarts).
```json
{
  "id": "claude request id",
  "sessionId": "uuid",
  "toolName": "Bash",
  "input": { "command": "git status" },
  "summary": "Bash: git status",
  "suggestions": ["allow", "allow_always", "deny"],
  "createdAt": "2026-05-25T12:00:40Z"
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
| POST   | `/api/projects/{id}/sessions` | `{ "title?", "model?", "permissionMode?" }` | `Session` (201) |
| GET    | `/api/sessions` | — | `Session[]` (all sessions, newest first) |
| GET    | `/api/sessions/{id}` | — | `Session` |
| GET    | `/api/sessions/{id}/messages` | — | `Message[]` |
| POST   | `/api/sessions/{id}/messages` | `{ "content" }` | `Message` (202; runs async, status flips to `processing`) |
| POST   | `/api/sessions/{id}/stop` | — | `Session` (cancels a running prompt) |
| PATCH  | `/api/sessions/{id}/mode` | `{ "permissionMode" }` | `Session` (changes answering mode) |
| GET    | `/api/sessions/{id}/permissions` | — | `PermissionRequest[]` (currently pending) |
| POST   | `/api/sessions/{id}/permissions/{requestId}` | `{ "decision": "allow"｜"allow_always"｜"deny", "message?" }` | `{ "ok": true }` |
| GET    | `/api/sessions/{id}/usage` | — | `UsageRecord[]` |
| GET    | `/api/usage` | — | `UsageSummary` |

`decision` semantics: `allow` approves this one request; `allow_always` approves
it and auto-approves further requests for the same tool in this session;
`deny` rejects it (optional `message` is shown to claude).

## WebSocket `/ws`

The client connects once and receives a stream of JSON events. The client does not
need to send anything (server → client only for now). Each event:

```json
{
  "type": "session.status | session.message | session.delta | session.permission | session.permission_resolved | session.usage | project.created | project.deleted",
  "ts": "2026-05-25T12:00:00Z",
  "payload": { }
}
```

Event payloads:
- `session.status` → a full `Session` object (status changed).
- `session.message` → a full, persisted `Message` object (a complete turn was
  appended — user, assistant, thinking, or tool).
- `session.delta` → a streaming chunk for the in-progress assistant turn, BEFORE
  the final `session.message`:
  ```json
  { "sessionId": "uuid", "kind": "text | thinking | tool", "text": "partial chunk" }
  ```
  The frontend accumulates deltas into a live bubble and replaces it with the
  final `session.message` when the turn completes.
- `session.permission` → a `PermissionRequest` that is now pending (status also
  goes to `awaiting_approval`). Render the approval menu.
- `session.permission_resolved` → `{ "sessionId": "uuid", "requestId": "...", "decision": "allow｜allow_always｜deny" }`
  (a pending request was answered; remove it from the UI).
- `session.usage` → a full `UsageRecord`.
- `project.created` / `project.deleted` → a full `Project` object.

The frontend should treat the WebSocket as the live source of truth and fall back to
REST polling if the socket drops. On (re)connect it should also
`GET /api/sessions/{id}/permissions` to recover any pending approval requests.
