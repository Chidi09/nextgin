# nextgin multi-target spec

Status: authoritative for all work on the `multi-target` branch. Dispatch
instructions cite sections of this file by number (e.g. "SPEC §3.2").

## 1. Goal

One codebase — a Next.js frontend plus a Gin backend with API routes and
scheduled jobs — deploys to three targets:

| Target | HTTP front door | Next.js | Gin backend | Cron trigger |
|---|---|---|---|---|
| `selfhost` (done, v0) | Go | UDS worker pool behind Go | same process | in-process scheduler (§3.4) |
| `vercel` | Vercel router | Vercel's native Next.js | Vercel Service, `framework: go`, native | `vercel.json` `crons` → cron route |
| `cloudflare` | Next Worker (OpenNext) | OpenNext Cloudflare | separate Worker, Go→wasm, persistent instance (§5) | Worker Cron Trigger → cron route in-process |

Split topologies (web and backend on different hosts) are specified in §9.

Design rule: **a cron job is a Gin route.** Every target executes a job by
sending a request through the same Gin engine and middleware chain as API
traffic. Targets differ only in what triggers that request.

## 2. Build split (hard invariant)

- Files that use the worker pool, `os/exec`, Unix sockets or process
  signals carry `//go:build !js`. Today: `worker_pool.go`, `sysprocattr_*.go`,
  and the Frontend/selfhost parts of `nextgin.go` (to be moved into
  `selfhost.go`, `//go:build !js`).
- Portable files (no build tag): `manifest.go`, `handler.go`, `cron.go`,
  `cron_route.go`, `export.go`, `target.go`.
- Cloudflare runtime: `cloudflare_js.go` with `//go:build js && wasm`.
- `GOOS=js GOARCH=wasm go build ./...` MUST succeed. It is part of the
  verification gate (§8).

## 3. Cron jobs (portable core)

### 3.1 Canonical schedule syntax

Exactly 5 space-separated fields, UTC, Vercel-compatible numeric subset:

| Field | Range |
|---|---|
| minute | 0–59 |
| hour | 0–23 |
| day of month | 1–31 |
| month | 1–12 |
| day of week | 0–6 (0 = Sunday) |

Each field is a comma-separated list of items. Allowed item forms: `*`,
`N`, `N-M`, `*/S`, `N-M/S`. N, M and S are decimal integers. `S >= 1`.
`N <= M`. All values within the field's range.

Rejected (error at parse time): names (`MON`, `JAN`), `?`, `L`, `W`, `#`,
`@hourly`-style macros, 6-field (seconds) expressions, empty items, and
any expression where **both** day-of-month and day-of-week are not `*`
(Vercel restriction; keeps semantics identical on every platform).

### 3.2 `cron.go` API (portable)

```go
// Schedule is a parsed canonical cron expression.
type Schedule struct { /* unexported: sets of allowed values per field */ }

func ParseSchedule(expr string) (*Schedule, error)
func (s *Schedule) String() string            // canonical input, whitespace-normalized
func (s *Schedule) Next(after time.Time) time.Time // first matching minute strictly after `after`, UTC, seconds=0
func (s *Schedule) RunsPerDayMax() int        // len(minutes) * len(hours); >1 means sub-daily
func (s *Schedule) CloudflareExpr() string    // §3.3
```

`Next` must terminate for every valid schedule (e.g. `0 0 31 2 *` never
matches: return the zero `time.Time` after scanning at most 5 years).

### 3.3 Cloudflare translation

Cloudflare day-of-week is 1–7 with 1 = Sunday. `CloudflareExpr()` returns
the canonical expression with only the day-of-week field rewritten:
`*` stays `*`; anything else is expanded to the explicit sorted list of
canonical values, each +1, comma-joined (e.g. `1-5` → `2,3,4,5,6`,
`*/2` → `1,3,5,7`). Other fields are emitted unchanged.

### 3.4 Registration and routes (`cron_route.go`)

```go
func (a *App) Cron(name, schedule string, handlers ...gin.HandlerFunc)
```

- `name` must match `^[a-z0-9][a-z0-9_-]{0,62}$` and be unique; schedule
  must parse. Violations **panic** at registration (same as Gin route
  conflicts) with a message naming the job.
- Registers `GET` and `POST` on `/_nextgin/cron/<name>` with
  `cronAuth` middleware first, then the user's handlers.
- `cronAuth` (fail closed): accept iff the `Authorization` header equals
  `Bearer <token>` (constant-time compare) where token is either the
  process-internal token (32 random bytes, hex, generated once per
  process) or the value of env `CRON_SECRET` when that is non-empty.
  Otherwise `401` with JSON `{"error":"unauthorized"}` and abort.
- The App keeps an ordered registry of jobs: `{Name, Schedule, Path}`.

### 3.5 Self-host scheduler

Started by `App.Run` on the selfhost target only (never on vercel/cloudflare,
never in export mode). One goroutine per job:

- Sleep until `Schedule.Next(now)`; then invoke the job by calling
  `a.Router.ServeHTTP` in-process with a synthetic `POST` request to the
  job path, header `Authorization: Bearer <internal token>`, header
  `X-Nextgin-Cron: <name>`, and a context with timeout `Config.CronTimeout`
  (default 5 minutes).
- Overlap guard: if the previous run of the same job is still executing
  when the next fire time arrives, skip that run and log a warning.
- Log each run at Info with name, status code and duration; status >= 400
  logs at Error.
- Stops when Run's context is cancelled (graceful shutdown waits for
  in-flight job runs up to `ShutdownGrace`).

## 4. Targets and export

### 4.1 Target detection (`target.go`)

`Target()` returns, in order: env `NEXTGIN_TARGET` if set (`selfhost`,
`vercel`, `cloudflare`; anything else → error at `New`); else `vercel`
if env `VERCEL == "1"`; else `cloudflare` when built for `js/wasm`; else
`selfhost`.

Behavior of `New`/`Run` per target:

- `selfhost`: unchanged v0 behavior (workers, frontend) + scheduler (§3.5).
- `vercel`: no manifest, no workers, no frontend, no scheduler. `Run`
  listens on `:$PORT` (default `3000`) serving `a.Router` only.
- `cloudflare`: §5.
- **Export mode** (env `NEXTGIN_EXPORT=1`, any target): `New` must not read
  the manifest, start workers or touch disk. `Run` writes the export JSON
  (§4.2) to the file named by `NEXTGIN_EXPORT_FILE` (stdout when unset) and
  exits with status 0. The CLI always sets the file: gin prints debug output
  to stdout before `nextgin.New` runs when the user builds the router
  themselves, so stdout cannot be trusted to carry only JSON.

### 4.2 Export JSON (`export.go`)

```json
{
  "version": 1,
  "routes": [{"method": "GET", "path": "/api/todos"}],
  "crons": [{"name": "cleanup", "schedule": "*/5 * * * *",
             "cloudflare": "*/5 * * * *", "path": "/_nextgin/cron/cleanup",
             "runsPerDayMax": 288}]
}
```

`routes` comes from `gin.Engine.Routes()`, excluding `/_nextgin/` paths,
sorted by path then method. The CLI derives platform config from this
file; Go code is the single source of truth.

## 5. Cloudflare runtime (option 1: wasm, persistent instance)

Measured (local `wrangler dev`, p50): workers-go default glue boots a new
Go runtime per request (44.6 ms trivial endpoint vs 12.6 ms plain JS);
keeping one instance alive gives 13.8 ms. Therefore:

- Go side (`cloudflare_js.go`): `workers.ServeNonBlock(a.Router)`;
  `cron.ScheduleTaskNonBlock(task)` where `task` reads `cron.NewEvent(ctx)`,
  looks up every job whose `CloudflareExpr()` equals `event.Cron`, and
  invokes each in-process exactly as §3.5 does (internal token); then
  `workers.Ready()` and block forever (`select {}`).
- JS side: the CLI patches the workers-go generated `build/worker.mjs` so
  `fetch` and `scheduled` share one lazily created instance (one `run()`
  promise) instead of calling `run()` per event.
- Failure handling (verified under `wrangler dev`):
  - workers-go v0.36.0 panics when the scheduled task returns an error, which
    would kill the persistent instance. The task therefore always returns nil;
    an unmatched expression or a job answering >= 400 is recorded on
    `globalThis.__nextginCronErrors[event.cron]` and the glue's `scheduled()`
    throws it, so Cloudflare reports the invocation as failed while the Go
    instance keeps serving.
  - If the Go program exits (unrecovered panic in a goroutine), the glue sees
    `go.exited` on the next event and boots a fresh instance. Exit cannot be
    observed through `go.run()`'s promise: workerd does not run continuations
    of a promise resolved from a different request context.
  - In-memory Go state does not survive a reboot or isolate eviction; keep
    durable state in a binding (KV/D1/R2) or an external store.
- Known v1 limitation (document, do not work around): the runtime context
  (env/ctx) is captured from the first event; `waitUntil` of later events
  is not wired.
- Requires the Workers Paid plan in practice (free plan CPU limit 10 ms).
- Measured (local `wrangler dev`, warm, p50 of 60): persistent glue with
  `gin.Default()` 12.2 ms, with a logger-free `gin.New()` 8.6 ms — gin's
  per-request logger writes through console.log. The stock workers-go glue
  cannot serve this runtime at all (the Go side blocks forever by design).
- Limits that apply (developers.cloudflare.com/workers/platform/limits):
  64 MiB uncompressed Worker size, no compressed limit; 128 MB memory per
  isolate; 1 s global-scope startup. See wasm size below.
- Wasm size (measured on the example): 41.9 MB default; 28.4 MB with -tags
  nomsgpack; 10.0 MB with nomsgpack, -trimpath -ldflags=-s -w and no gin
  binding (nextgin.BindJSON). The CLI builds with -tags nomsgpack (override:
  --go-tags) and warns above 20 MiB. wasm-opt -Oz was measured at a further 8%
  for 148 s of build time and is not used. Per-isolate cost (V8
  WebAssembly.compile in Node, median of 5): 107 ms -> 33 ms; whole-process
  CPU 304 ms -> 122 ms. Production CPU after the change is not yet measured.

## 6. CLI targets (`nextgin build --target ...`)

Project layout for multi-target apps: a root with `web/` (Next.js) and
`backend/` (Go module with `main.go`). Never name a directory `api/`
(Vercel treats it as legacy functions).

- `vercel`: run export; write root `vercel.json` (Vercel Services, beta;
  schema verified against vercel.com/docs/services, /routing, /bindings):
  - `services.web = {"root": "web/", "framework": "nextjs", "bindings":
    [{"type": "service", "service": "backend", "format": "url", "env":
    "NEXTGIN_INTERNAL_URL"}]}` — bindings are declared by the caller; Go
    services cannot declare bindings, but Next calling Go is the direction
    we need.
  - `services.backend = {"root": "backend/", "framework": "go"}` — the
    `go` slug is not in the docs but is in the CLI's framework table
    (detectors: go.mod + main.go). It is required: with `root` only,
    `vercel dev` did not detect the service and the rewrite answered 502
    "Cannot route to service `backend`: it is not running".
  - `rewrites`, in order: `/<prefix>/:path*` → `{"service": "backend"}` for
    each distinct static first path segment of exported routes; then
    `/_nextgin/:path*` → backend; then `/(.*)` → `{"service": "web"}`.
    Services receive the original path; routing into a service is final
    (no fall-through), so a prefix belongs wholly to Go.
  - `crons`: `{"path", "schedule"}` per exported job.
  `--check` fails if the committed file differs. `--vercel-hobby` fails the
  build when any job has `runsPerDayMax > 1`.
- `cloudflare`: OpenNext build of `web/`; `GOOS=js GOARCH=wasm` build of
  `backend/` + glue patch (§5); generated wrangler configs with the backend
  service binding and Cron Triggers from `cloudflare` expressions.
- The Next adapter is a no-op (returns `undefined`) unless the target is
  `selfhost`. Vercel builds set `VERCEL=1`; the CLI passes
  `NEXTGIN_TARGET=cloudflare` to the OpenNext build. Anyone running
  `opennextjs-cloudflare build` directly must set it too.
- The web worker forwards only the exported Go prefixes to the backend;
  `/_nextgin/*` is not publicly routed on Cloudflare (Cron Triggers reach the
  backend worker directly).

Verified locally (2026-11-16): Cloudflare web + backend workers together
under `wrangler dev` (multi-config) with OpenNext 1.20.17 — Go API via the
service binding, prerendered pages, SSR fetching `NEXTGIN_INTERNAL_URL`,
proxy.ts, next.config headers/redirects, Suspense streaming, Server Actions
persisting through Go, Cron Triggers running Go jobs in-process.

Verified locally (2026-11-16, `vercel dev`, CLI 65.16.0, linked scratch
project): the generated services config is accepted; both services run;
Go prefixes and `/_nextgin/*` route to Go, everything else to Next (404s
from Next); the binding injects `NEXTGIN_INTERNAL_URL` (SSR and Server
Actions reach Go); proxy.ts and next.config headers run; the Go process
sees `VERCEL=1` (vercel target, no scheduler); a cron-shaped request
(GET + `Authorization: Bearer $CRON_SECRET`) through the router runs the
job, a wrong secret is 401.

Deploy-tested (2026-11-16, scratch app = the example, both targets, then
torn down):

- Vercel (Hobby, `vercel deploy --prod`): the remote Go build fetched the
  private `github.com/Chidi09/nextgin` with `GIT_CREDENTIALS` passed as a
  build-only env (`--build-env`), plus `GOPRIVATE`. Production URL: Go API,
  SSR + Server Action via the binding, proxy.ts, headers, cron route 401
  without / 204 with `CRON_SECRET`. First request 6.0 s (cold); warm Go API
  p50 ~90 ms above a static asset from the same client. Hobby rejects the
  deploy outright for any sub-daily cron (`--vercel-hobby` catches it first).
- Cloudflare (Workers Paid): `wrangler deploy` backend then web. Upload
  42.9 MB (8.8 MB gzip), reported startup 3 ms (Go boots on first event).
  Everything above works through the web worker. Production `event.cron`
  arrived as exactly `* * * * *` and ran the job in-process (204) — the
  CloudflareExpr matching assumption holds. Analytics over 79 invocations:
  CPU p50 3.3 ms, p90 279 ms, p99 552 ms; wall p50 14.6 ms, p99 1.66 s. The
  tail is Go booting: at low traffic requests spread across many isolates
  and ~1 in 4 booted a fresh instance (first request 7.0 s). Server Action
  write and read-back landed in different isolates, so in-memory state is
  per isolate (expected; see §5).

Open items:
- (Closed) Vercel's production cron invoker reaches a service path: the
  daily heartbeat fired at 23:46:10 UTC (within its 23:00 hour on Hobby) as
  GET /_nextgin/cron/heartbeat on service `backend`, Bearer CRON_SECRET
  accepted, 204.

Resolved after the first deploy (re-deployed and verified 2026-11-16):
- The generated backend wrangler.jsonc sets `workers_dev: false` and
  `preview_urls: false`. Its workers.dev URL returns 404; the web worker's
  binding (API, SSR via `NEXTGIN_INTERNAL_URL`) and Cron Triggers still work.
  Note: the first trigger fired ~5 min after the redeploy.
- vercel/cloudflare targets switch Gin to release mode unless `GIN_MODE` is
  set; production boot logs no longer show `[GIN-debug]` output.

Also: a Vercel Go build must be able to fetch
`github.com/Chidi09/nextgin` (public repo, or GOPRIVATE + token); production
Cloudflare `event.cron` matching `CloudflareExpr()` byte for byte (a mismatch
now fails loudly); real cold start and CPU cost of the ~43 MB example wasm.

## 7. Known bug to fix first

`worker_pool.go` marks a worker not-ready on any dial error, and only the
READY line sets it back. A live worker that refuses one connection (full
listen backlog under load) leaves rotation permanently. Required fix: on a
dial failure, mark not-ready and start a background re-probe for that
worker (dial its socket every 100 ms, up to 5 s); on success mark ready
again; if the probe times out while the process is still running, kill
the process so the supervisor restarts it.

## 8. Verification gate (run after every dispatch, by the reviewer)

```bash
gofmt -l .                          # must print nothing
go vet ./...
go test -race -count=1 ./...
GOOS=js GOARCH=wasm go build ./...  # from §2 onward
(cd packages/adapter && npx tsc -p tsconfig.json && node --test test/)
npm run build -w fullstack-app && scripts/e2e.sh node && scripts/e2e.sh bun
```

## 9. Split topology (web target x backend target)

Status: design, not implemented. The combined targets (§1) run the Go
backend inside the same platform as Next.js, so Go work is still metered by
that platform (on Vercel a cron or webhook is a billed function invocation
with a duration cap and no persistent state). The split topology puts Next.js
and the Go backend on independent hosts so backend work, crons, DB pooling
and webhooks leave the web platform. Rendering (SSR, Server Actions) stays on
the web platform; this does not reduce that bill.

Backend hosting is a choice, not a requirement. Users whose data lives in
managed Postgres/Redis do not want to run a server, so the first-class
no-server backend is Cloudflare Workers (existing §5 build); a persistent
host is the second option.

### 9.1 CLI

`nextgin build --web-target vercel --backend-target <cloudflare|container>
--backend-url <https://...> [--api-mode rewrite|direct]`. (`--web` and
`--backend` already name the project directories, so the target flags are
`--web-target` and `--backend-target`.) `--target` stays the combined
in-platform mode, unchanged, and cannot be mixed with the split flags.
Phase 1 supports `--web-target vercel` only; web=cloudflare with
backend=container waits for Cloudflare Containers (option 2).

Split `vercel.json` is written to `<web dir>/vercel.json` (the Vercel project
Root Directory is the web dir; Services are not used because only the web app
deploys to Vercel). `rewrite` mode: `rewrites` with external destinations
`<backend-url>/<prefix>/:path*`. `direct` mode: `{}`. `/_nextgin/*` is never
proxied.

### 9.2 API-only backend mode (`container`)

New target `backend` (`NEXTGIN_TARGET=backend`): no manifest, no frontend,
no workers; `New(dir, cfg)` must succeed with a missing `dir`. Runs the
in-process scheduler (§3.5), listens on `:$PORT`, applies Gin release mode.
`MountFrontend` is a no-op. `nextgin build --backend-target container` emits
`<backend dir>/Dockerfile` (multi-stage Go build, `ENV NEXTGIN_TARGET=backend`)
and does not pick a host. Single instance by default; `NEXTGIN_SCHEDULER=off`
disables the scheduler on replicas (leader election is out of scope).

### 9.3 Browser routing (`--api-mode`, default `rewrite`)

- `rewrite`: `vercel.json` has `rewrites` `/<prefix>/:path*` ->
  `<backend-url>/<prefix>/:path*` for each exported Go prefix, no
  `services.backend`, no `crons`. Same-origin, no client changes. Verify how
  Vercel bills external rewrites from vercel.com/docs before the README
  claims any saving.
- `direct`: no rewrites. Browser calls `<backend-url>` itself. Requires Go
  middleware `nextgin.CORS(origins ...string)` (explicit allow-list,
  credentials on, never `*` with credentials) and documented cookie guidance
  (`Domain=` on the shared parent, `SameSite`, `Secure`). The CLI prints
  `NEXT_PUBLIC_NEXTGIN_API_URL=<backend-url>` for client code.

### 9.4 Crons

Split builds emit no `crons` in `vercel.json`. `container` runs the
scheduler; `cloudflare` uses Cron Triggers (§3.3). Vercel does zero
invocations for jobs.

### 9.5 Server-to-server auth (fail closed)

The backend is public. `NEXTGIN_INTERNAL_SECRET` is set on both sides.
`nextgin.RequireInternal()` is a Gin middleware that 401s unless
`X-Nextgin-Internal` matches (constant-time compare); with the secret unset
it rejects every request. Server-side Next code calls the backend with
`NEXTGIN_INTERNAL_URL` = `<backend-url>` and that header (small helper in
`@nextgin/adapter`). The cron route keeps its own bearer (§3.4).

### 9.6 Cloudflare backend exposed publicly

Combined mode sets `workers_dev: false` because only the service binding
calls it. In split mode the generated wrangler config instead routes the
`--backend-url` hostname as a custom domain and keeps `workers_dev: false`.

### 9.7 External data stores from wasm (spike, unverified)

workers-go v0.36.0 has `cloudflare/sockets` (`Connect` returns a
`net.Conn`, TLS optional) and no Hyperdrive binding. So Postgres/Redis from
the wasm backend need a driver with a custom dialer, or an HTTP API
(Upstash REST works over `net/http`). To verify under `wrangler dev` against
a local Postgres before claiming support: pgx and go-redis with the sockets
dialer; connection reuse across requests in the persistent instance; wasm
size (warn above 20 MiB, §6 CLI); cold-start CPU. Until verified the README
says "possible, untested".

### 9.8 Not in scope

- Go caching rendered pages behind Vercel (Vercel's own cache sits in front;
  unproven).
- Image resizing route (`images.loader`): container backend only, separate
  feature.
- Leader election for multi-instance schedulers.

### 9.9 Verification

Gate (§8) plus: API-only mode serves routes and runs a cron with no `dir`;
`RequireInternal` and `CORS` tests (missing/wrong secret, disallowed origin,
credentials); CLI tests for both `--api-mode` outputs and for no `crons` in
split `vercel.json`; Docker build of the emitted Dockerfile.
