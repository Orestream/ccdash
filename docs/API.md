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
  "gitMode": "worktree",
  "createdAt": "2026-05-25T12:00:00Z"
}
```

`gitMode` controls how new sessions provision their working tree:

- `worktree` (default) — each session gets its own `git worktree add -b ccdash/<id8>`
  off the project's HEAD; `claude` runs in that worktree's path. Multiple
  sessions on one repo can't clobber each other and the user can preview/accept
  a session's diff via the preview endpoints below.
- `default` — sessions skip worktree provisioning entirely and `claude` runs
  directly in `project.path`. The session's `worktreePath`, `branch`, and
  `baseCommit` stay empty; the preview/accept endpoints reject the session.

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
  "worktreePath": "/home/robin/.local/state/ccdash/worktrees/<session-id>",
  "branch": "ccdash/abcd1234",
  "baseCommit": "0123456789abcdef…",
  "previewState": "",
  "createdAt": "2026-05-25T12:00:00Z",
  "updatedAt": "2026-05-25T12:01:00Z"
}
```

`previewState` is `""` when the session has no preview, or `"applied"` when
the session's diff (`git diff main...<branch>`) has been applied as uncommitted
edits onto `project.path` so the user can rebuild and test it. Only one
session per project can be in the `applied` state at a time. The stored patch
itself is a DB-only blob and is not returned in the API.

`worktreePath`, `branch`, and `baseCommit` are populated when the session's
project is inside a git repo: on session creation the backend runs
`git worktree add -b ccdash/<short-id> <worktreePath> <baseCommit>` against
the project's repo root and the claude CLI is launched with that path as
its working directory, so parallel sessions on one repo can't clobber each
other. `baseCommit` is the project's HEAD at the moment of session creation
— uncommitted changes in the project's main checkout are **not** propagated
into the worktree. For non-git projects all three fields are empty strings
and the session runs in the project path directly (legacy behavior).

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
  "createdAt": "2026-05-25T12:00:30Z",
  "attachments": [
    {
      "id": "uuid",
      "messageId": "uuid",
      "sessionId": "uuid",
      "name": "image-1.png",
      "mediaType": "image/png",
      "createdAt": "2026-05-25T12:00:30Z"
    }
  ]
}
```
`attachments` is omitted when the message has no images. Bytes are served from
`GET /api/attachments/{id}`.

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

### Utilization
The Claude subscription rate-limit usage behind the `claude` CLI's `/usage`
view. The backend reads the OAuth token from the local credentials file
(`CCDASH_CRED_PATH`, default `~/.claude/.credentials.json`) and queries the
undocumented `GET /api/oauth/usage` endpoint. `usedPercent` is a percentage
(0–100). Windows the account does not have (e.g. a separate Opus limit) are
omitted; `resetsAt` may be absent.
```json
{
  "session": { "usedPercent": 3.0, "resetsAt": "2026-05-26T16:50:00Z" },
  "week": { "usedPercent": 9.0, "resetsAt": "2026-05-29T06:00:00Z" },
  "weekOpus": { "usedPercent": 0.0 },
  "fetchedAt": "2026-05-26T14:56:49Z"
}
```

## REST endpoints

| Method | Path | Body | Returns |
|--------|------|------|---------|
| GET    | `/api/health` | — | `{ "status": "ok", "version": "..." }` |
| GET    | `/api/projects` | — | `Project[]` |
| POST   | `/api/projects` | `{ "name", "path", "gitMode?" }` | `Project` (201) |
| GET    | `/api/projects/{id}` | — | `Project` |
| PATCH  | `/api/projects/{id}` | `{ "gitMode" }` | `Project` (changes gitMode) |
| DELETE | `/api/projects/{id}` | — | 204 |
| GET    | `/api/projects/{id}/sessions` | — | `Session[]` |
| POST   | `/api/projects/{id}/sessions` | `{ "title?", "model?", "permissionMode?" }` | `Session` (201) |
| GET    | `/api/sessions` | — | `Session[]` (all sessions, newest first) |
| GET    | `/api/sessions/{id}` | — | `Session` |
| DELETE | `/api/sessions/{id}?deleteBranch=true｜false` | — | 204 (removes the worktree if any; deletes the branch only when `deleteBranch=true`, default `false`) |
| GET    | `/api/sessions/{id}/messages` | — | `Message[]` |
| POST   | `/api/sessions/{id}/messages` | `{ "content", "images?" }` | `Message` (202; runs async, status flips to `processing`) |
| POST   | `/api/sessions/{id}/stop` | — | `Session` (cancels a running prompt) |
| PATCH  | `/api/sessions/{id}/mode` | `{ "permissionMode" }` | `Session` (changes answering mode) |
| PATCH  | `/api/sessions/{id}/title` | `{ "title" }` | `Session` (renames; non-empty title required) |
| GET    | `/api/sessions/{id}/permissions` | — | `PermissionRequest[]` (currently pending) |
| POST   | `/api/sessions/{id}/permissions/{requestId}` | `{ "decision": "allow"｜"allow_always"｜"deny", "message?", "answers?" }` | `{ "ok": true }` |
| GET    | `/api/sessions/{id}/usage` | — | `UsageRecord[]` |
| POST   | `/api/sessions/{id}/preview` | — | `{ "session": Session }` (applies the worktree's diff to `project.path`; sets `previewState` to `"applied"`) |
| DELETE | `/api/sessions/{id}/preview` | — | `{ "session": Session }` (reverses an applied preview) |
| POST   | `/api/sessions/{id}/accept` | — | `{ "session": Session }` (commits the applied preview onto `project.path`, removes the session's worktree+branch, marks it `done`) |
| GET    | `/api/attachments/{id}` | — | raw image bytes (`Content-Type` is the stored media type) |
| GET    | `/api/usage` | — | `UsageSummary` |
| GET    | `/api/usage/limits` | — | `Utilization` (subscription /usage; 502 if unavailable, 501 if unconfigured) |

Preview/accept semantics:

- `POST /api/sessions/{id}/preview` computes `git diff main...<session-branch>`
  against the project's repo root and applies the patch to `project.path` as
  uncommitted edits. The session's `previewState` flips to `"applied"` and a
  `session.status` event fans out. Errors: 404 if the session does not exist;
  400 if the session has no worktree (`gitMode: "default"` or non-git project),
  the preview is already applied, or the diff is empty; 409 if another session
  in the same project is already applied; 500 with the git error if the patch
  doesn't apply cleanly.
- `DELETE /api/sessions/{id}/preview` reverses the stored patch on
  `project.path` and clears `previewState`. 400 if the session is not in the
  applied state.
- `POST /api/sessions/{id}/accept` generates a one-line commit message from the
  stored patch (the backend shells out to `claude -p`; on failure it falls
  back to `ccdash: applied changes from session`), commits the changes onto
  `project.path`, removes the session's worktree and branch, clears
  `worktreePath`/`branch`/`baseCommit`, sets `status` to `done`, and clears
  `previewState`.

`decision` semantics: `allow` approves this one request; `allow_always` approves
it and auto-approves further requests for the same tool in this session;
`deny` rejects it (optional `message` is shown to claude).

`answers` is used to ship back results from tools that gather user input through
the permission dialog (notably `AskUserQuestion`): a map of question text →
selected option label, merged into the tool's `updatedInput` on `allow`. For
multi-select questions the client joins selected labels with `", "` (the
matching format the claude SDK accepts). The field is ignored unless `decision`
is `allow`.

Title auto-naming: a session created with a blank `title` is named from the
first user message (its first non-empty line, truncated) when that message is
sent. Once a title exists — whether auto-derived or set via `PATCH …/title` — it
is never overwritten by later messages. Both paths broadcast `session.status`.

Images: a message may carry pasted images. Each `images[]` item is
`{ "name", "mediaType", "data" }` where `data` is base64 (no `data:` URL
prefix), `mediaType` is one of `image/png|jpeg|gif|webp`, and `name` is the
display/reference label (`image-1.png`, …). The content body may be empty when
images are present. Images are forwarded to claude as inline vision blocks
(each preceded by a text block with its name, so "in image-1 we see…" lines up)
and persisted; the resulting `Message` includes an `attachments[]` array of
`Attachment` metadata (`{ id, messageId, sessionId, name, mediaType, createdAt }`),
whose bytes are fetched from `GET /api/attachments/{id}`. Limits: ≤ 8 images per
message, ≤ 10 MiB each.

## WebSocket `/ws`

The client connects once and receives a stream of JSON events. The client does not
need to send anything (server → client only for now). Each event:

```json
{
  "type": "session.status | session.message | session.delta | session.permission | session.permission_resolved | session.usage | session.deleted | project.created | project.updated | project.deleted",
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
- `session.deleted` → a full `Session` object (the row was removed; its
  worktree, if any, has been cleaned up).
- `project.created` / `project.updated` / `project.deleted` → a full `Project` object.

Preview/accept transitions reuse `session.status` (the full session row is
broadcast on every state change), so a client that already merges
`session.status` events does not need to handle a separate event type.

The frontend should treat the WebSocket as the live source of truth and fall back to
REST polling if the socket drops. On (re)connect it should also
`GET /api/sessions/{id}/permissions` to recover any pending approval requests.
