<div align="center">

<a href="https://next-gin.dev">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.svg" />
    <source media="(prefers-color-scheme: light)" srcset="docs/assets/logo-light.svg" />
    <img alt="nextgin logo" src="docs/assets/logo-light.svg" width="76" height="76" />
  </picture>
</a>

<h1>nextgin</h1>

<p>
  <strong>Next.js App Router on the frontend. Pure-Go (<code>net/http</code>) API &amp; cron backend.</strong>
</p>

<p>
  Zero external Go dependencies &middot; ~7.5 MB native binary &middot; Deploy split or all-in-one
</p>

<p>
  <a href="https://pkg.go.dev/github.com/Chidi09/nextgin"><img src="https://pkg.go.dev/badge/github.com/Chidi09/nextgin.svg" alt="Go Reference" /></a>
  <a href="https://www.npmjs.com/package/@nextgin/adapter"><img src="https://img.shields.io/npm/v/%40nextgin%2Fadapter?label=%40nextgin%2Fadapter&color=00ADD8" alt="npm version" /></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-black" alt="MIT License" /></a>
</p>

<p>
  <a href="https://next-gin.dev">Website</a> &middot;
  <a href="https://next-gin.dev/docs">Docs</a> &middot;
  <a href="docs/PATTERNS.md">Architecture Patterns</a> &middot;
  <a href="docs/COOKBOOK.md">Cookbook</a> &middot;
  <a href="examples/">Examples</a>
</p>

</div>

---

## Why nextgin?

Keep Next.js focused on what it does best — **UI, SSR, RSC, and Server Actions** — while a tiny, zero-dependency Go binary owns your **hot path and background jobs**: auth, sessions, rate limiting, payments, webhooks, and sub-minute crons.

- **Zero external Go dependencies:** Built purely on Go 1.22+ `net/http.ServeMux` with `Group`, `With`, `Middleware`, panic recovery, JSON/raw-body helpers (`ReadJSON`, `ReadRawBody`, `WriteJSON`), per-IP `RateLimit`, and secure `SetSessionCookie` / `ClearSessionCookie`.
- **Crons are authenticated HTTP routes:** `app.Cron("name", "*/5 * * * *", handler)` registers `/_nextgin/cron/<name>` behind fail-closed auth and runs in-process on `backend` / `selfhost` (no daily-cron limits), with graceful `SIGTERM` cancellation and an optional `CronLocker` hook for multi-replica deploys.
- **One codebase, any topology:** Ship as a split stack (Next.js on Vercel or Cloudflare Pages + Go container on Cloudflare Containers, Fly.io, Cloud Run, Railway, or a VPS) or self-host everything behind a single Go binary (`nextgin deploy`).
- **Go is the front door on self-host:** Serves static assets, prerendered pages, `next.config` headers/redirects, and API routes natively, proxying only dynamic SSR/RSC/Server Action requests to Node or Bun workers over Unix sockets.
- **Built-in liveness probe:** `GET /_nextgin/health` responds `200 {"status":"ok"}` without touching Next.js or polluting exported routes.

---

## Quick Start

Scaffold a new project with a clean 3-layer Go layout (`internal/domain`, `store`, `service`, `httpapi`) and a typed Next.js API client (`lib/api.ts`):

```bash
# Single-repo self-host layout
npx @nextgin/adapter init my-app

# Split layout (web/ + backend/) for Vercel / Cloudflare Pages + Go container
npx @nextgin/adapter init my-app --layout split
```

Or add `nextgin` to an existing Next.js app:

```bash
go get github.com/Chidi09/nextgin
npm install @nextgin/adapter
```

```js
// next.config.mjs
import { createNextGinAdapter } from '@nextgin/adapter'

export default {
  adapterPath: createNextGinAdapter(),
}
```

Run locally with live reload for both Next.js and Go:

```bash
nextgin dev     # Next.js dev server + Go backend with auto-restart on .go changes
nextgin build   # next build, finalize .nextgin manifest, go build -o server
./server
```

---

## A Complete Go Backend in One File

```go
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/Chidi09/nextgin"
)

func main() {
	app, err := nextgin.New("./.nextgin", nextgin.Config{
		TrustForwardedHeaders: true,
	})
	if err != nil {
		log.Fatal(err)
	}

	app.Router.Use(nextgin.CORS("https://example.com"))

	api := app.Router.Group("/api")
	authLimit := nextgin.RateLimitWithConfig(nextgin.RateLimitConfig{
		RPS: 2, Burst: 10, TrustForwardedHeaders: true,
	})

	api.POST("/login", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Email, Password string }
		if err := nextgin.ReadJSON(r, &body); err != nil {
			nextgin.WriteError(w, http.StatusBadRequest, "invalid json")
			return
		}
		// Verify credentials and issue a session token...
		nextgin.SetSessionCookie(w, "sid", "token", nextgin.CookieOptions{
			Domain: ".example.com", MaxAge: 7 * 86400,
		})
		nextgin.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}, authLimit)

	api.POST("/logout", func(w http.ResponseWriter, r *http.Request) {
		nextgin.ClearSessionCookie(w, "sid", nextgin.CookieOptions{Domain: ".example.com"})
		w.WriteHeader(http.StatusNoContent)
	})

	// Read raw bytes first so HMAC webhook signatures verify cleanly.
	api.POST("/webhooks/pay", func(w http.ResponseWriter, r *http.Request) {
		raw, err := nextgin.ReadRawBody(w, r, 256<<10)
		if err != nil {
			nextgin.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		_ = raw // verify signature with your payment provider
		w.WriteHeader(http.StatusOK)
	})

	// Unlimited crons — runs in-process in Go, cancelled cleanly on SIGTERM.
	app.Cron("expire-carts", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	app.MountFrontend() // no-op when NEXTGIN_TARGET=backend
	log.Fatal(app.Run(":" + os.Getenv("PORT")))
}
```

---

## Deployment Targets

| Target | Command | Where it runs | Best for |
|---|---|---|---|
| **Split (Recommended)** | `nextgin build --web-target vercel --backend-target container --backend-url https://api.example.com` | Next.js on Vercel / Cloudflare Pages + Go container on Cloudflare Containers, Fly.io, Cloud Run, Railway, or VPS | Zero serverless API/cron invocations, full DB connection pooling, sub-minute crons |
| **Self-Host / VPS** | `nextgin deploy --host root@203.0.113.7 --domain shop.example.com` | Single VPS with Go binary + Node/Bun workers + Postgres/Redis + Caddy HTTPS | Predictable flat-rate hosting with one-command SSH deploys |
| **Vercel (Combined)** | `nextgin build --target vercel` | Next.js + Go service both on Vercel | Single-dashboard deploys (pass `--vercel-hobby` for daily crons) |

### Split Mode Notes
- `--api-mode direct` *(default)*: The browser calls `NEXT_PUBLIC_NEXTGIN_API_URL` directly so API traffic bypasses frontend bandwidth/compute. Pair with `nextgin.CORS(...)` and parent-domain cookies (`Domain: ".example.com"`).
- `--api-mode rewrite`: Opt-in same-origin mode that writes external rewrites into `web/vercel.json`.
- Protect server-to-server routes called from SSR or Server Actions with `nextgin.RequireInternal()` and `NEXTGIN_INTERNAL_SECRET`.
- For multi-replica deploys, plug a Postgres advisory lock or Redis `SET NX` into `Config.CronLocker` (or set `NEXTGIN_SCHEDULER=off` on replicas).

### Single-VPS Deploy Notes
- Postgres and Redis are auto-detected from `go.mod`. First deploy prompts once per service to either provision it on the VPS or use an external URL; generated secrets live in `/opt/nextgin/<name>/.env` (`0600`) and persist across redeploys.
- Use `nextgin deploy --local` to run locally, or `--dry-run` to inspect generated files in `.nextgin/deploy/`.

### Cloudflare (Pages + Containers)
You can also run the entire stack on Cloudflare (this is how `next-gin.dev` itself is deployed):
- **Backend (`NEXTGIN_TARGET=backend`):** Cross-compile a stripped `linux/amd64` Go binary (~7.5 MB) into a minimal Alpine image and deploy it to **Cloudflare Containers** (`instance_type: "lite"`, 256 MiB) behind a custom domain (`api.example.com`).
- **Frontend:** Deploy the Next.js app to **Cloudflare Pages**, either calling `https://api.example.com` directly or proxying `/api/*` through a Pages Function (`functions/api/[[path]].js`) to `NEXTGIN_BACKEND_URL`.
- **Crons:** Wire Cloudflare Worker `triggers.crons` to `POST /_nextgin/cron/<name>` with `Authorization: Bearer $CRON_SECRET` so scheduled jobs wake the container reliably even when it sleeps.

See the full copy-pasteable `Dockerfile`, `wrangler.jsonc`, and Worker wrapper in [docs/COOKBOOK.md](docs/COOKBOOK.md#7-deploying-on-cloudflare-pages--containers).

---

## Security & Hardening Defaults

- **Strict body limits:** `ReadJSON` caps request bodies at 1 MiB (`DefaultMaxBodyBytes`) and rejects trailing JSON tokens. Use `ReadJSONLimit` or `ReadRawBody` for custom limits and HMAC webhooks.
- **Streaming-safe timeouts:** `backend` and `vercel` targets enforce `ReadHeaderTimeout: 10s`, `ReadTimeout: 15s`, `WriteTimeout: 30s`, `IdleTimeout: 60s`. `selfhost` keeps `WriteTimeout: 0` so Next.js Suspense and SSE streams are never cut off.
- **Fail-closed by default:** Cron routes require the process-internal bearer token or `CRON_SECRET`; `RequireInternal` returns `401` whenever `NEXTGIN_INTERNAL_SECRET` is unset; `CORS` panics on empty or `*` origins; `SetSessionCookie` defaults to `HttpOnly: true`, `Secure: true`, `SameSite: Lax`.

---

## Examples & Documentation

| Resource | Description |
|---|---|
| [examples/fullstack-app](examples/fullstack-app) | Minimal Next.js App Router + Go API, SSR, Server Actions, and end-to-end test suite |
| [examples/ecommerce-app](examples/ecommerce-app) | Full SQLite store with auth, rate limiting, cart, transactional checkout, HMAC webhook, and 1-minute cart-expiry cron |
| [docs/PATTERNS.md](docs/PATTERNS.md) | App folder structure, `httpapi → service → store` layering, error mapping, middleware, and Next.js ↔ Go client boundary |
| [docs/COOKBOOK.md](docs/COOKBOOK.md) | Deployment topologies, database/ORM ownership (`sqlc`), auth, webhooks, and distributed cron locking |
| [docs/SPEC.md](docs/SPEC.md) | Architecture specification, design decisions, and benchmarks |
| [AGENTS.md](AGENTS.md) | Contributor and AI coding agent guide |

---

## Development

```bash
gofmt -l .
go vet ./...
go test -race -count=1 ./...
(cd packages/adapter && npx tsc -p tsconfig.json && node --test test/)
npm run build -w fullstack-app && scripts/e2e.sh node && scripts/e2e.sh bun
```

## License

[MIT](LICENSE)
