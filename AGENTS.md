# AGENTS.md — nextgin

Guidance for coding agents and contributors working in this repository or building applications on `nextgin`.

## What This Project Is

`nextgin` pairs a **Next.js App Router frontend** with a **zero-dependency pure-Go (`net/http`) API and cron backend**.

- **Go core (`github.com/Chidi09/nextgin`):** Zero external dependencies in `go.mod`. Uses Go 1.22+ `net/http.ServeMux` via a lightweight `Router` wrapper (`router.go`), in-process cron scheduler (`scheduler.go`), and Unix-domain-socket Next.js worker pool (`worker_pool.go`) for self-hosting.
- **Adapter & CLI (`packages/adapter` / `@nextgin/adapter`):** Next.js build adapter, manifest generator, multi-target config builder (`selfhost`, `vercel`, split `vercel + container`), and single-VPS SSH/Docker deployer (`nextgin deploy`).
- **Examples:**
  - `examples/fullstack-app` — minimal Next.js + Go API + SSR + Server Actions + e2e suite.
  - `examples/ecommerce-app` — seeded SQLite store with auth, rate limiting, cart, transactional checkout, HMAC payment webhook, and 1-minute cart-expiry cron.

## Hard Architectural Invariants

1. **Zero external Go dependencies in the root module.**
   Keep `go.mod` stdlib-only. Do not add HTTP frameworks (Gin, Echo, Fiber), ORM libraries, or third-party routers to the root package. Examples may add their own DB drivers (e.g. `modernc.org/sqlite`, `pgx`).
2. **Go owns the hot path and scheduled jobs.**
   Put auth, sessions, carts, payments, webhooks, rate limiting, and cron jobs in Go. Let Next.js focus on UI, SSR, RSC, and Server Actions.
3. **A cron job is an authenticated HTTP route.**
   `app.Cron(name, schedule, handler)` registers `GET`/`POST /_nextgin/cron/<name>` behind `cronAuth` (fail-closed: accepts only the per-process internal bearer token or `CRON_SECRET`). Every cron handler must respect `r.Context().Done()` so `SIGTERM` shuts down cleanly.
4. **Fail closed on security boundaries.**
   - `CORS` panics on empty or `*` origins.
   - `RequireInternal()` returns `401` for every request when `NEXTGIN_INTERNAL_SECRET` is unset.
   - `ReadJSON` caps bodies at 1 MiB (`DefaultMaxBodyBytes`) and rejects trailing tokens.
   - `SetSessionCookie` defaults to `HttpOnly: true`, `Secure: true`, `SameSite: Lax`.
   - Never trust `X-Forwarded-*` unless `TrustForwardedHeaders: true` is explicitly configured behind a reverse proxy.
5. **Never break streaming on `selfhost`.**
   Keep `WriteTimeout: 0` by default on `TargetSelfhost` so Next.js Suspense/SSE streaming is not cut off mid-response. `TargetBackend` and `TargetVercel` enforce API timeouts (`10s / 15s / 30s / 60s`).
6. **Single source of truth for routes & schemas.**
   - Go routes (`App.Export()`) are the single source of truth for CLI-generated `vercel.json` configs. Internal routes under `/_nextgin/` (including `/_nextgin/health` and `/_nextgin/cron/*`) are excluded from exported public routes.
   - In user apps, avoid split-brain database ownership: prefer Go owning runtime queries (SQL + `sqlc`) rather than mutating the same tables from both Go and a TypeScript ORM. See [docs/COOKBOOK.md](docs/COOKBOOK.md).

## Key Files

| Area | Files |
|---|---|
| Core App & Targets | `nextgin.go`, `target.go`, `selfhost.go`, `export.go` |
| Pure-Go Router & JSON | `router.go`, `json.go` |
| Security & Auth Helpers | `cors.go`, `internal_auth.go`, `ratelimit.go`, `auth_helpers.go` |
| Cron Scheduler | `cron.go`, `cron_route.go`, `scheduler.go` |
| Self-Host Frontend & Pool | `handler.go`, `manifest.go`, `worker_pool.go`, `sysprocattr_*.go` |
| CLI & Deployer | `packages/adapter/src/{cli,split,vercel,adapter,manifest}.ts`, `packages/adapter/src/deploy/` |

## Verification Gate

Run before every commit or PR:

```bash
gofmt -l .                                                  # must print nothing
go vet ./...
go test -race -count=1 ./...
(cd examples/fullstack-app && go build -o /dev/null .)
(cd examples/ecommerce-app && go build -o /dev/null .)
(cd packages/adapter && npx tsc -p tsconfig.json && node --test test/)
```

For changes touching `handler.go`, `manifest.go`, `worker_pool.go`, `router.go`, or the Next.js adapter, also run the full end-to-end check:

```bash
npm run build -w fullstack-app && scripts/e2e.sh node
```

## Conventions & Pitfalls

- **App architecture & separation of concerns:** When scaffolding or growing user apps on `nextgin`, keep `main.go` thin (wiring only) and separate `internal/httpapi` (HTTP/DTOs/validation), `internal/service` (business logic & transactions), `internal/store` (SQL), and `internal/domain` (types & sentinel errors). Map domain errors centrally instead of leaking raw SQL errors; centralize Next.js fetches in `lib/api.ts`. See [docs/PATTERNS.md](docs/PATTERNS.md).
- **Route patterns:** Use Go 1.22+ `{param}` syntax (e.g. `api.PATCH("/cart/items/{id}", ...)` and `r.PathValue("id")`), not `:param`.
- **Payment webhooks:** Always use `nextgin.ReadRawBody(w, r, limit)` before verifying HMAC signatures; never pass webhook payloads through `ReadJSON` first.
- **No `api/` directory at project root:** Vercel treats top-level `api/` as legacy serverless functions. Use `web/` + `backend/` for multi-target repos.
- **Commit hygiene:** Keep commits scoped per branch/feature; never commit build artifacts (`server`, `.next/`, `.nextgin/`, `*.db*`, `packages/adapter/dist/`).
