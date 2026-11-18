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

Status: Go side (§9.2, §9.3 CORS, §9.5) and CLI (§9.1, §9.4, §9.6) implemented and verified locally; not yet deployed to real Vercel/Cloudflare; §9.7 spiked locally (works, with limits). The combined targets (§1) run the Go
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

### 9.7 External data stores from wasm (spiked locally 2026-11-17)

Spike: pgx v5.13.0 and go-redis v9.23.0 in the Go/wasm backend, using
workers-go v0.36.0 `cloudflare/sockets` (a `net.Conn` over Workers
`connect()`), under `wrangler dev` against local Postgres 16 and Redis 7.
workers-go has no Hyperdrive binding.

Works:
- Postgres: `cfg.DialFunc` = a dialer calling `sockets.Connect`, and
  `cfg.LookupFunc` returning the host unchanged (Go wasm has no resolver).
  Plaintext and TLS both work; with `sslmode=require` pgx does the TLS
  handshake itself (crypto/tls in wasm; `show ssl` = on). Connect + query
  ~32-46 ms warm, 139-181 ms first request (local network).
- Redis: go-redis `Options.Dialer` = the same dialer; INCR works.

Required, found by ablation:
- The dialer must call `sockets.Connect(context.WithoutCancel(ctx), ...)`.
  workers-go ties the socket's lifetime to the context passed to `Connect`,
  and go-redis cancels its dial context as soon as the dial returns, so the
  first read fails with `i/o timeout`. Not needed for pgx's default path, but
  harmless. (A wrapper mapping zero-time deadlines to the far future was also
  tried and is NOT needed.)

Hard limit:
- A connection cannot be reused across requests. The second request that
  touches a connection opened by an earlier request hangs and the runtime
  cancels it ("Worker's code had hung"); the next request then runs on a
  fresh instance. Open one connection per request and close it. For
  production, put a pooler in front (Neon/Supabase pooler, PgBouncer) so a
  per-request connection is cheap.

Not verified: socket-native TLS (`SecureTransportOn`; it failed against a
self-signed cert locally, so it says nothing about real certificates, and
would avoid TLS CPU in wasm), real managed providers, behaviour under load,
Upstash-style REST clients.

Size: the spike (nextgin + pgx + go-redis, `-tags nomsgpack`, stripped) is
25.2 MB (the 10.0 MB example baseline plus ~15 MB), above the CLI's 20 MiB
warning. How much is pgx vs go-redis was not measured, and the cold-start
CPU of a 25 MB module was not measured.

Helper (implemented): `nextgin.DialContext` and `nextgin.LookupHost`
(`dial*.go`). On js/wasm `DialContext` is `sockets.Connect` with the
detached context; elsewhere it is a plain `net.Dialer`. Same user code on
every target: `cfg.DialFunc = nextgin.DialContext`, `cfg.LookupFunc =
nextgin.LookupHost` (pgx), `Options.Dialer = nextgin.DialContext`
(go-redis). Verified: native tests (echo, cancel-after-dial, rejection of
non-tcp networks) and the wasm spike re-run on the helper under
`wrangler dev` (Postgres insert+count, Redis INCR, counters matched).

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

### 9.10 Verified locally (2026-11-17)

- `--backend-target container`: generated Dockerfile builds (golang:1.27
  from go.mod, distroless nonroot, 27.6 MB image); the container serves the
  API, answers the cron route 401 without and 204 with `CRON_SECRET`, 404 on
  `/` (no frontend), no `[GIN-debug]` output. Limitation: the Dockerfile runs
  `go mod download` before `COPY . .`, so a local `replace` directive must be
  copied first (the CLI prints this); published modules are unaffected.
- `--backend-target cloudflare`: 10.0 MB wasm, wrangler config has
  `routes: [{pattern, custom_domain: true}]`, `workers_dev: false`.
- Combined `--target cloudflare` outputs are byte-identical to before.
- Not yet verified: a real split deploy (Vercel web + Workers or container
  backend), Vercel external-rewrite billing, `direct` mode cookies/CORS in a
  browser, Postgres/Redis against managed providers and under load (§9.7).

## 10. `nextgin deploy` (single VPS, everything included)

Status: implemented; verified with `--local` (§10.10) and over real SSH into a Debian 12 target container (§10.11); not yet on a real internet-facing VPS. Goal: `nextgin deploy` takes a project to a running VPS with
no hand-written Docker, proxy or env files. The app runs in **self-host mode**
(Go front door + Node workers, §4.1), not the API-only `backend` target.
Postgres and Redis are created on the server and wired into the app's env
automatically; the user is only asked for connection details when a database
is external. (The command is `nextgin deploy`; the npm bin is `nextgin`.)

### 10.1 Command, config, flags

`nextgin deploy [--host user@host | --local] [--name app] [--domain
example.com] [--postgres internal|external|none] [--redis
internal|external|none] [--reconfigure] [--dry-run] [--no-install-docker]
[--ssh-port 22] [--identity ~/.ssh/key] [--dir /opt/nextgin] [--http-port 80]
[--https-port 443]`.

`--local` deploys to the machine the command runs on instead of over SSH: the
same steps (build, image, `.env` merge, compose up, health check) with local
process execution as the transport, no SSH, no OS check, no Docker install
(Docker must already work for the current user). It exists for running
`nextgin deploy` on the server itself and for testing without a VPS. `--dir`
is the base directory (default `/opt/nextgin`, the app lives in
`<dir>/<name>/`); `--http-port` / `--https-port` are the host ports compose
publishes (defaults 80 / 443) so a test run does not collide with other
services. `--local` and `--host` are mutually exclusive; one is required
unless `nextgin.deploy.json` supplies `host`.

Non-secret choices (`host`, `name`, `domain`, ssh port) are saved to
`nextgin.deploy.json` in the project root after the first run, so later runs
are just `nextgin deploy`. It never contains passwords or connection URLs.
`--dry-run` writes the generated files to `.nextgin/deploy/` and prints the
plan; it does not touch the network.

### 10.2 Project layout and build

Two layouts, detected in this order: (a) split layout: `web/package.json` and
`backend/go.mod` exist; (b) root layout: `package.json` and `go.mod` in the
project root (as `examples/fullstack-app`). Otherwise fail with a message
naming both.

The CLI builds on the developer machine, not on the VPS and not in Docker
(nextgin is private, a Go build in Docker fails on local `replace`
directives and needs credentials; `next build` needs RAM small VPSes lack):
1. `ssh <host> uname -m` -> arch (`x86_64` -> `amd64`, `aarch64` -> `arm64`,
   else fail).
2. Next build with the selfhost adapter in the web dir (split layout: cwd
   `web/`; root layout: cwd root), producing `.nextgin/`.
3. `CGO_ENABLED=0 GOOS=linux GOARCH=<arch> go build -trimpath -ldflags="-s -w"
   -o server .` in the backend dir.
4. If `.nextgin/server` contains any `*.node` native module and the build
   machine's arch differs from the VPS arch, fail with an explanation (no
   cross-arch native builds in v1).

### 10.3 Image

Runtime-only Dockerfile (generated, first line is a `# generated by nextgin`
marker, written to a temp build context with `server` and `.nextgin/`):
`FROM node:22-bookworm-slim`, `WORKDIR /app`, `COPY server .nextgin` into it,
`ENV PORT=8080 GIN_MODE=release`, `USER node`, `EXPOSE 8080`, `HEALTHCHECK`
via `node -e fetch(...)` to `http://127.0.0.1:8080/` (healthy when status <
500), `ENTRYPOINT ["/app/server"]`. Built locally with
`docker build --platform linux/<arch>` and shipped without a registry:
`docker save | gzip | ssh <host> "gunzip | docker load"`. Tag
`nextgin-<name>:<UTC yyyymmddHHMMSS>` plus `:latest`; the remote keeps the
last 3 tags and prunes older ones of this name only.

### 10.4 Stack (`docker-compose.yml`, generated, marker line first)

Remote dir `/opt/nextgin/<name>/` holds `docker-compose.yml`, `.env` (mode
600) and, with a domain, `Caddyfile`. Services:
- `app`: the image, `env_file: .env`, `restart: unless-stopped`, `depends_on`
  each internal database with `condition: service_healthy`. Published as
  `<http-port>:8080` (default 80) when there is no domain; not published when Caddy is present.
- `postgres` (only when internal): `postgres:16`, named volume, env
  `POSTGRES_USER=nextgin`, `POSTGRES_DB=<name with non-[a-z0-9_] -> _>`,
  `POSTGRES_PASSWORD` from `.env`, healthcheck `pg_isready`. NO `ports:`.
- `redis` (only when internal): `redis:7`, `--requirepass` from `.env`,
  `--appendonly yes`, named volume, healthcheck `redis-cli ping`. NO `ports:`.
- `caddy` (only with `--domain`): `caddy:2`, publishes `<http-port>:80` and `<https-port>:443`,
  mounts `Caddyfile`, volumes for data/config. Caddyfile:
  `<domain> { reverse_proxy app:8080 }` (automatic HTTPS; the DNS record must
  already point at the VPS; print that reminder).
Docker-published ports bypass ufw, hence no database ports. Images are pinned
to a major version (a major bump breaks an existing volume). The tool never
runs `down -v`, never deletes volumes.

### 10.5 `.env` rules (remote file is the source of truth)

- Keys the app receives: `DATABASE_URL`, `REDIS_URL` (when not `none`),
  plus `NEXTGIN_POSTGRES` and `NEXTGIN_REDIS` recording `internal|external|
  none` so later runs do not ask again.
- Internal values use service names, so no host ports are needed:
  `DATABASE_URL=postgres://nextgin:<pw>@postgres:5432/<db>?sslmode=disable`,
  `REDIS_URL=redis://:<pw>@redis:6379/0`. Passwords are 48 hex chars from
  `crypto.randomBytes(24)` (URL-safe). Compose reads `POSTGRES_PASSWORD` and
  `REDIS_PASSWORD` from the same file.
- Secrets are generated ONCE. On every deploy the CLI downloads the remote
  `.env`, keeps every existing key and value, adds only missing keys, and
  uploads it with mode 600. A password is never regenerated (Postgres applies
  `POSTGRES_PASSWORD` only when it first initialises the volume).
- External: the CLI prompts for the URL (hidden input), writes it to `.env`,
  and omits that service from the compose file. URLs are never accepted as
  command-line arguments (shell history, `ps`); non-interactive runs read
  `DATABASE_URL` / `REDIS_URL` from the local environment instead and fail if
  missing.
- Switching internal -> external leaves the old volume untouched and prints a
  warning with the volume name.

### 10.6 Prompts

Only for services the app uses, detected from the backend `go.mod`: Postgres
if it requires `github.com/jackc/pgx`, `github.com/lib/pq` or
`gorm.io/driver/postgres`; Redis if it requires `github.com/redis/go-redis`
or `github.com/gomodule/redigo`. Neither detected -> no database services and
no prompts. For each detected service whose `NEXTGIN_POSTGRES|REDIS` key is
absent from the remote `.env`: `Postgres: create one on this server? [Y/n]`
(Enter = internal); on `n`, prompt for the external URL (hidden). `--postgres`
and `--redis` answer without prompting; `--reconfigure` asks again. No TTY and
no flag -> internal, with external requiring the flag plus env var.

### 10.7 Remote steps (idempotent)

1. SSH check (`BatchMode=yes`, `ConnectTimeout=10`, `StrictHostKeyChecking`
   left at the user's ssh default). Host key problems are surfaced, not
   bypassed.
2. OS check: Debian or Ubuntu or a derivative (`ID` or `ID_LIKE` in
   `/etc/os-release` contains `debian` or `ubuntu`), and root or passwordless
   sudo; otherwise fail with a precise message. Skipped with `--local`.
3. Docker: if `docker compose version` fails, install with Docker's official
   convenience script (`curl -fsSL https://get.docker.com | sh`) unless
   `--no-install-docker`, which fails with instructions instead.
4. `mkdir -p /opt/nextgin/<name>`, upload compose, Caddyfile, merged `.env`
   (mode 600), load the image, `docker compose up -d --remove-orphans`.
5. Wait up to 120 s for the app container to be healthy, then print the URL
   (`https://<domain>` or `http://<host>[:<http-port>]`). On failure print the last 50 log
   lines of the unhealthy service and exit non-zero; leave the stack up.

### 10.8 Known limits (v1)

- Single host, single app instance; the scheduler (§3.5) runs in it.
- Recreate causes brief downtime; no rollback command (previous images stay).
- No Postgres backups. Documented, not solved.
- Behind Caddy, Go sees the proxy as the client: set
  `Config.TrustForwardedHeaders: true` in `main.go` (not controlled by the CLI).
- Debian/Ubuntu family only (remote); Docker only; no Windows-hosted VPS.

### 10.9 Code layout and verification

Pure generators (no I/O) in `packages/adapter/src/deploy/`: `generate.ts`
(Dockerfile, compose, Caddyfile), `env.ts` (parse, merge, secret generation),
`detect.ts` (layout, services from go.mod), `config.ts` (`nextgin.deploy.json`).
I/O in `ssh.ts` (system `ssh`/`scp`, argv arrays, no shell interpolation of
user input) and `command.ts` (flow). Verification without a VPS: unit tests of
the generators; `--dry-run`; run the generated compose stack locally under an
`ng-` project name (app route using `DATABASE_URL`/`REDIS_URL` through
`nextgin.DialContext`), then redeploy without `-v` and confirm data and
passwords survive; external mode against a separate container. A real VPS run
needs the user's explicit approval.

### 10.10 Verified locally (2026-11-17, `--local`, Docker Compose v5.7)

Test app: the example plus a route that increments a counter in Postgres
(pgx) and Redis (go-redis) from `DATABASE_URL` / `REDIS_URL`, via
`nextgin.DialContext`. Run with `--dir <scratch> --http-port 18180`.
- First deploy (no prompts, services detected from go.mod): app healthy,
  `/api/dbcheck` incremented both stores through the auto-wired URLs,
  prerendered and SSR pages served; Postgres/Redis publish no host ports
  (`5432/tcp`, `6379/tcp` only); `.env` mode 600; the app runs as the `node`
  user; `nextgin.deploy.json` holds no secrets.
- Redeploy: `.env` byte-identical, the Postgres container not recreated,
  data persisted (counters 2 -> 3), nothing asked, previous image kept.
- External Postgres (`--postgres external`, URL from `DATABASE_URL` env, the
  password containing `$`): no Postgres service generated, the URL stored
  with `$$`, the app connected to the external database.
- `--domain app.localhost` with custom ports: Caddy up, HTTPS 200 through the
  proxy, database check passed through it.
- `--dry-run`: preview files written, secrets shown as `<hidden>`, no build.
- Interactive prompts (pseudo-terminal): answering `n` switches a service to
  external and asks for a hidden URL. Input fed faster than a person types
  loses lines (readline is recreated per prompt); not an issue when typing.
- Review found and fixed: the SSH image upload ran plain `docker load` when the
  remote user needs `sudo -n docker`; `loadImage` now takes the docker command.
  The success URL now includes a non-default HTTPS port.

Not verified: a real SSH deploy (the host had no sshd and no passwordless
sudo), Docker installation on a fresh VPS, arm64 targets, apps with native
`.node` modules, `--reconfigure` end to end, large `.nextgin` trees with
symlinks, Let's Encrypt issuance.

### 10.11 Verified over SSH (2026-11-18)

Target: a `debian:12` container with `openssh-server`, run `--privileged` with a
tmpfs `/var/lib/docker` on this machine, reached as `127.0.0.1:2222`. No Docker
installed beforehand, and no systemd. Key auth only (`--identity`).
- Non-root user with passwordless sudo: the OS check passed, Docker was
  installed by `get.docker.com`, the image was uploaded with `sudo -n docker
  load`, and the stack came up. `/api/dbcheck` incremented Postgres and Redis,
  prerendered and SSR pages returned 200, `.env` is mode 600, Postgres and Redis
  publish no host ports, and `nextgin.deploy.json` holds host, name and ssh port
  only.
- Redeploy with only `--identity` (host, name and port read from the saved
  config): `.env` hash and the Postgres/Redis container ids unchanged, counters
  continued (2 -> 3), the previous image kept.
- `root@`: plain `docker` (no sudo), a second stack on another port, healthy.
- Bug found by this run and fixed: if the remote `docker load` failed (daemon not
  running), ssh exited while the local `docker save` blocked forever writing into
  a pipe nobody read, so the CLI hung without an error. The upload now fails at
  once with ssh's error; regression test `test/transport-load.test.mjs` (fails
  with a 15 s hang without the fix).
- Not covered by this run: a systemd host (the daemon was started by hand,
  because the container has no init system, so the installer's service start is
  unproven), arm64, a real network, firewalls, Let's Encrypt, and a first-time
  host-key prompt (BatchMode is on: the host key must already be in
  `known_hosts`, so connect once with plain `ssh` first).
