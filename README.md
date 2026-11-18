# nextgin

Run a Next.js app with a **pure-Go (`net/http`) API and cron backend** — zero web-framework overhead (~7.5 MB native binary) — and deploy it either as a lightweight split sidecar (`Vercel web + Go container`) or all-in-one on your own VPS (`nextgin deploy`).

- **Pure Go `net/http` router:** `Router` wraps Go 1.22+ `http.ServeMux` with `Group`, `With`, `Middleware`, panic recovery, JSON/raw-body helpers (`ReadJSON`, `ReadRawBody`, `WriteJSON`), per-IP `RateLimit`, and secure `SetSessionCookie` / `ClearSessionCookie`.
- **Crons are routes:** `app.Cron("name", "*/5 * * * *", handler)` registers `/_nextgin/cron/<name>` and runs in-process on `backend` / `selfhost` (no Vercel Hobby daily-cron limits), with graceful shutdown cancellation and an optional `CronLocker` hook for rolling deploys.
- **Go is the front door on self-host:** Serves static assets, prerendered pages, `next.config` headers/redirects, and your API routes natively, proxying only dynamic pages (SSR, RSC, Server Actions) to Node/Bun workers.
- **Built-in liveness probe:** `GET /_nextgin/health` answers `200 {"status":"ok"}` without touching Next.js or appearing in exported API routes.

Status: pre-release (`0.1.0`), private repository, not published to npm. Details and measurements: [docs/SPEC.md](docs/SPEC.md).

## Recommended: Split Topology (Next.js on Vercel + Tiny Go API/Cron Container)

Keep Next.js on Vercel for UI/SSR while a tiny distroless Go container (on Fly.io, Cloud Run, Railway, or a $4–5/mo VPS) handles auth, cart, payments, webhooks, and sub-minute crons:

```go
// backend/main.go
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

	// Browser calls https://api.example.com directly (default --api-mode direct).
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
		// Verify credentials, issue session token...
		nextgin.SetSessionCookie(w, "sid", "token", nextgin.CookieOptions{
			Domain: ".example.com", MaxAge: 7 * 86400,
		})
		nextgin.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}, authLimit)

	api.POST("/logout", func(w http.ResponseWriter, r *http.Request) {
		nextgin.ClearSessionCookie(w, "sid", nextgin.CookieOptions{Domain: ".example.com"})
		w.WriteHeader(http.StatusNoContent)
	})

	api.POST("/webhooks/pay", func(w http.ResponseWriter, r *http.Request) {
		raw, err := nextgin.ReadRawBody(w, r, 256<<10)
		if err != nil {
			nextgin.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		_ = raw // verify HMAC signature with your payment provider
		w.WriteHeader(http.StatusOK)
	})

	// Unlimited crons — runs in Go, zero Vercel Cron invocations.
	app.Cron("expire-carts", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
		// r.Context() is cancelled on SIGTERM for graceful shutdown.
		w.WriteHeader(http.StatusNoContent)
	})

	app.MountFrontend() // no-op on NEXTGIN_TARGET=backend
	log.Fatal(app.Run(":" + os.Getenv("PORT")))
}
```

Build the split config (`web/vercel.json` + `backend/Dockerfile`):

```bash
nextgin build --web-target vercel --backend-target container --backend-url https://api.example.com
```

- `--api-mode direct` (default): `web/vercel.json` has no external rewrites; the browser calls `NEXT_PUBLIC_NEXTGIN_API_URL` directly so API traffic bypasses Vercel compute/bandwidth. Use `nextgin.CORS(...)` and parent-domain cookies (`Domain: ".example.com"`).
- `--api-mode rewrite`: opt-in same-origin mode that writes external rewrites into `web/vercel.json`.
- Protect server-to-server routes called from Next.js SSR/Server Actions with `nextgin.RequireInternal()` and `NEXTGIN_INTERNAL_SECRET`.
- For rolling deploys or multiple replicas, plug a Postgres advisory lock or Redis `SET NX` into `Config.CronLocker` (or set `NEXTGIN_SCHEDULER=off` on replicas).

## Quick start (self-host / VPS)

```js
// next.config.mjs
import { createNextGinAdapter } from '@nextgin/adapter'
export default { adapterPath: createNextGinAdapter() }
```

```bash
nextgin build   # next build, finalize .nextgin, go build -o server
./server
nextgin dev     # next dev + Go server with restart on .go changes
```

Or ship everything (Go + Node workers + Postgres/Redis + Caddy HTTPS) to a single VPS:

```bash
nextgin deploy --host root@203.0.113.7 --domain shop.example.com
```

- **Databases:** Postgres and Redis are detected from `go.mod`. First deploy asks once per service whether to run it on the server (Enter = yes) or use an external URL; secrets are generated once into `/opt/nextgin/<name>/.env` (mode 600) and preserved across redeploys.
- **Local / dry-run:** `nextgin deploy --local` runs on the current machine; `--dry-run` previews generated files in `.nextgin/deploy/`.

## All Deployment Targets

| Mode | Command | Where things run | Notes |
|---|---|---|---|
| Split (recommended) | `nextgin build --web-target vercel --backend-target container --backend-url https://api.example.com` | Next.js on Vercel, Go container on your host | Zero Vercel API/cron invocations; full DB pooling |
| Self-host / VPS | `nextgin build` / `nextgin deploy` | One Go binary + Node/Bun workers | Go serves static/prerendered/API/crons natively |
| Vercel (combined) | `nextgin build --target vercel` | Next.js + Go service both on Vercel | Generates root `vercel.json`; Hobby allows daily crons only (`--vercel-hobby`) |

## Security & Hardening Defaults

- **Body limits:** `ReadJSON` caps reads at 1 MiB (`DefaultMaxBodyBytes`) and rejects trailing data; use `ReadJSONLimit` or `ReadRawBody` for custom caps.
- **Server timeouts:** `backend`/`vercel` targets default to `ReadHeaderTimeout: 10s`, `ReadTimeout: 15s`, `WriteTimeout: 30s`, `IdleTimeout: 60s` (`selfhost` leaves `WriteTimeout: 0` so SSR streaming is never cut off).
- **Fail-closed auth:** Cron routes require the process-internal bearer token or `CRON_SECRET`; `RequireInternal` rejects all traffic when `NEXTGIN_INTERNAL_SECRET` is unset; `CORS` panics on empty or `*` allow-lists.

## Development

```bash
gofmt -l .
go vet ./...
go test -race -count=1 ./...
(cd packages/adapter && npx tsc -p tsconfig.json && node --test test/)
npm run build -w fullstack-app && scripts/e2e.sh node && scripts/e2e.sh bun
```

[docs/SPEC.md](docs/SPEC.md) is the design record with measurements and verification logs.
