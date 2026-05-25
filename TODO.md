# TODO

Roadmap for ccdash. The skeleton (✅) is in place and green; everything below ⬜
is open work. Roughly ordered by priority.

## ✅ Done (skeleton)

- [x] Repo scaffold: Go backend + React/Vite frontend, shared API contract.
- [x] SQLite store (projects, sessions, messages, usage) + tests.
- [x] `claude` CLI runner with stream-json parser (behind a `Runner` interface) + tests.
- [x] Concurrent session manager (parallel/background runs, cancel/stop) + tests.
- [x] chi REST API + WebSocket hub + tests.
- [x] React dashboard: sidebar, session list, live status badges, session view, usage bar.
- [x] WebSocket live updates with reconnect.
- [x] Linting (golangci-lint, ESLint), CI workflow, auto-commit-on-green Stop hook.

## ⬜ Backend

- [ ] **Stream assistant deltas** to the UI (`session.message` partials) instead
      of one message per turn.
- [ ] Tool-use / tool-result events surfaced as distinct message types.
- [ ] Reconcile a session's `claude_session_id` reliably across resumes.
- [ ] Configurable permission mode / allowed tools per session.
- [ ] Reattach to / recover in-flight runs after a backend restart.
- [ ] Usage: track cache-read/-write tokens and per-model rates; daily rollups.
- [ ] Endpoint + storage for run logs (stderr) for debugging failed runs.
- [ ] Auth (single-user token to start), then multi-user.
- [ ] Rate limiting and a cap on concurrent runs.
- [ ] WebSocket event replay cursor so a reconnecting client catches up.

## ⬜ Frontend

- [ ] Live-streaming transcript rendering (token-by-token) with markdown.
- [ ] Per-session usage detail view + charts (tokens/cost over time).
- [ ] Global activity view: all running sessions across projects at a glance.
- [ ] Project directory picker / validation (does the path exist?).
- [ ] Model selector per session; show which model a run used.
- [ ] Toasts/notifications when a background session needs input or finishes.
- [ ] Keyboard shortcuts; light theme.
- [ ] Optimistic UI + proper error/empty/loading states everywhere.

## ⬜ Tooling / ops

- [ ] Dockerfile + docker-compose for one-command run.
- [ ] Serve the built frontend from the Go binary in production (single artifact).
- [ ] E2E test (Playwright) hitting a real backend with a fake claude binary.
- [ ] Pre-commit hook mirroring CI locally.
- [ ] Release workflow (versioned binaries).

## ⬜ Nice-to-have

- [ ] Search across transcripts.
- [ ] Export a session transcript to markdown.
- [ ] Cost budget alerts.
