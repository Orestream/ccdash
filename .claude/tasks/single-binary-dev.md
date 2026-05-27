# Task: single-binary / single-port dev (with proxied HMR + unified logging)

## Goal

Make ccdash a **single Go binary on a single port** in production, while keeping
**fast live updates in dev** (frontend HMR + backend auto-restart, no full
rebuilds) — also on one port. Plus: unify dev logs so both processes stream to
stdout with `[frontend]`/`[backend]` prefixes and tee into two per-project log
files agents can read.

## Core idea: flip the proxy direction

Today Vite owns the port (`:10000`) and proxies `/api` + `/ws` back to Go
(`:10001`). Reverse that: **Go owns the single port (`:10001`)** and decides how
to serve the frontend based on a build tag.

- **Default build (no tag) = dev:** Go reverse-proxies every non-`/api`, non-`/ws`
  request to the Vite dev server on an internal port (`:5173`). Vite still
  provides HMR; it's just no longer user-facing. The browser only ever talks to
  `:10001`.
- **`-tags prod` = release:** Go serves the embedded `frontend/dist` via
  `//go:embed`, with SPA fallback to `index.html`. `go build -tags prod` →
  one self-contained binary, one port.

Why dev is the *default* (untagged) build: `make test` / `go build ./...` /
the auto-commit hook all run **without** tags, so they compile the proxy path
and **never need a built `dist/` to exist**. The embed only compiles under
`-tags prod`, after `npm run build` has produced `dist/`. This keeps the suite
green on a fresh checkout.

## What you get

| Goal | Mechanism |
|------|-----------|
| Single binary (prod) | `//go:embed all:dist` + SPA fallback |
| One port (dev too) | Go binds `:10001`; Vite is internal, proxied through |
| Live frontend, no rebuild | Vite HMR tunnels through Go's reverse proxy |
| Live backend, no full rebuild | `watchexec` restarts `go run`; Go compiles incrementally |
| Easy logs | one script prefixes + tees both streams to files |

---

## Implementation steps

### 1. Backend: build-tagged frontend handler

Add to `backend/internal/api/`:

**`spa.go`** (no build tag — reusable + unit-testable):

```go
package api

import (
	"io/fs"
	"net/http"
)

// spaHandler serves static files from fsys, falling back to index.html for
// any path that doesn't resolve (client-side routing).
func spaHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(fsys, p); err != nil {
			// Unknown path -> serve the SPA shell.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}
```

**`frontend_prod.go`** (`//go:build prod`):

```go
//go:build prod

package api

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:dist
var distFS embed.FS

func (s *Server) frontendHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err) // embed is fixed at build time; a failure here is a build bug
	}
	return spaHandler(sub)
}
```

> Note: `//go:embed dist` resolves **relative to this source file**, so the
> embed dir is `backend/internal/api/dist`. Either (a) point the embed at a
> path that exists by copying/symlinking the built bundle there during
> `make build`, or (b) move the embed file to a package whose directory can
> hold `dist` (e.g. a new `backend/internal/web` package) and have the build
> copy `frontend/dist` into it. Pick one and wire it in the Makefile (step 4).
> Simplest: `make build` does `cp -r frontend/dist backend/internal/web/dist`
> before `go build -tags prod`, and `dist/` there is gitignored.

**`frontend_dev.go`** (`//go:build !prod`):

```go
//go:build !prod

package api

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// viteTarget is where the Vite dev server listens (internal only).
func (s *Server) frontendHandler() http.Handler {
	target, _ := url.Parse("http://localhost:5173")
	return httputil.NewSingleHostReverseProxy(target)
}
```

`httputil.ReverseProxy` forwards WebSocket upgrades (Go 1.12+), so Vite's HMR
socket tunnels through automatically.

### 2. Wire the handler into the router

In `api.go` `Router()`, after the `/api` and `/ws` routes, send everything else
to the frontend handler:

```go
r.NotFound(s.frontendHandler().ServeHTTP)
```

Considerations:
- **Logger noise:** in dev the proxy serves every JS module / HMR request, and
  `middleware.Logger` (api.go:63) will log each one, drowning the `[backend]`
  logs. Mount the frontend handler so it bypasses the request logger — e.g.
  build a separate sub-router for `/api` + `/ws` that has the logger, and attach
  the frontend handler at the top level without it. Keep `Recoverer`.
- **CORS:** once Go serves the page, the app calls `/api` and `/ws` same-origin
  (`:10001`), so the CORS block (api.go:65) is no longer exercised in normal
  use. Leave it or simplify; not required for this task.
- Confirm the REST client (`src/api/`) and `useWebSocket` use **relative** URLs
  (`/api`, `/ws`). They should already; no change expected. If any hardcodes
  `:10001`/`:10000`, make it relative.

### 3. Vite config

`frontend/vite.config.ts` — Vite no longer needs to proxy anything (the browser
hits Go directly); it just needs a fixed internal port and HMR pointed back
through Go's port:

```ts
server: {
  port: 5173,
  strictPort: true,
  hmr: { clientPort: 10001 }, // browser loads from :10001, so HMR connects there
},
```

Remove the `proxy` block. The `test` block stays as-is.

### 4. Makefile

- `build` must build the frontend, stage `dist` where the embed expects it, then
  build with `-tags prod`:

```make
build: build-frontend ## Build the single production binary (embeds frontend)
	rm -rf $(BACKEND)/internal/web/dist
	cp -r $(FRONTEND)/dist $(BACKEND)/internal/web/dist   # if using internal/web
	cd $(BACKEND) && go build -tags prod -o ccdash ./cmd/ccdash
```

- Add a one-command dev target:

```make
dev: ## Run backend + frontend behind one port (:10001) with unified logs
	./scripts/dev.sh
```

Keep `dev-backend` / `dev-frontend` for running them standalone.

### 5. Unified logging — `scripts/dev.sh`

Runs both processes, prefixes combined stdout, and tees **raw** per-project logs
into `.claude/logs/`:

```bash
#!/usr/bin/env bash
# Run backend + frontend behind a single port (:10001).
# Combined stdout is prefixed [backend]/[frontend]; raw per-project logs go to
# .claude/logs/{backend,frontend}.log for agents to read.
set -uo pipefail
cd "$(dirname "$0")/.."

LOGDIR=".claude/logs"
mkdir -p "$LOGDIR"

# Tear the whole process group down on Ctrl-C / exit.
trap 'trap - INT TERM EXIT; kill 0' INT TERM EXIT

# Backend: watchexec restarts `go run` on .go changes (incremental compile).
(
  cd backend && exec watchexec -r -e go -- go run ./cmd/ccdash
) 2>&1 | tee "$LOGDIR/backend.log" | sed -u 's/^/[backend] /' &

# Frontend: Vite dev server (HMR) on the internal port; Go proxies to it.
(
  cd frontend && exec npm run dev
) 2>&1 | tee "$LOGDIR/frontend.log" | sed -u 's/^/[frontend] /' &

wait
```

Notes:
- `tee` writes the **raw** stream to the file; `sed -u` adds the prefix only on
  the terminal. Files stay clean for agents to grep.
- `sed -u` keeps it line-buffered. If Vite/Go output still batches when piped,
  wrap the producer in `stdbuf -oL -eL` (or run under a PTY) — note this in the
  script if you hit it.
- `watchexec` is the backend file-watcher. Alternatives if unavailable: `air`,
  `wgo run ./cmd/ccdash`, or `find ... | entr -r`. Document the chosen dep in
  `make setup` / CLAUDE.md and the Nix env if pinned.
- `chmod +x scripts/dev.sh`.

### 6. `.gitignore`

Add:

```
# --- Claude / local ---
.claude/logs/
```

And ignore the staged embed dir if you use the `internal/web/dist` copy:

```
/backend/internal/web/dist/
```

(`/frontend/dist/` is already ignored.)

---

## Tests (required by CLAUDE.md — suite must be green, untagged)

- **`spa_test.go`**: unit-test `spaHandler` with an in-memory `fstest.MapFS`:
  - existing asset (e.g. `assets/app.js`) → 200 with its body;
  - unknown route (e.g. `/projects/123`) → 200 with `index.html` body (SPA
    fallback);
  - `/` → `index.html`.
  This needs no build tag and no real `dist/`, so it runs under plain
  `go test ./...`.
- Existing `api_test.go` must still pass; the untagged build uses the dev proxy
  handler, which isn't exercised by those tests.
- Frontend: `npm run lint`, `npm run build`, `npm test` stay green. Add/adjust a
  test only if you touch app code (you shouldn't need to).

## Verify by hand

1. `make dev` → open `http://localhost:10001`. App loads; no `:10000` anywhere.
2. Edit a React component → HMR updates the browser instantly (no full reload,
   no rebuild). `[frontend]` lines show the HMR update.
3. Edit a `.go` file → `[backend]` logs show watchexec restarting; the API is
   back within ~1s. Frontend state survives (separate process).
4. `tail -f .claude/logs/backend.log` and `.../frontend.log` show clean
   per-project streams.
5. `make build && ./backend/ccdash` → single binary serves the app + API on
   `:10001` with **no Vite running**. Deep-link a client route → SPA fallback
   serves it.

## Done criteria

- [ ] One user-facing port (`:10001`) in both dev and prod.
- [ ] `make build` produces one binary that serves embedded frontend + API.
- [ ] `make dev`: frontend HMR + backend auto-restart, no full-project rebuilds.
- [ ] `make dev` streams prefixed combined logs and writes
      `.claude/logs/{backend,frontend}.log`.
- [ ] `go build ./...`, `go vet ./...`, `golangci-lint run ./...`,
      `go test ./...` all green **without** `-tags prod` and **without** a built
      `dist/`.
- [ ] `npm run lint && npm run build && npm test` green.
- [ ] `docs/ARCHITECTURE.md` + `CLAUDE.md` (Ports / Commands / Workflow) updated
      to describe the single-port model, the `prod` build tag, `make dev`, and
      where logs live.
```

