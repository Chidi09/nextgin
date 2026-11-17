# nextgin

Run a Next.js app with a Go (Gin) backend, and deploy it to the target that
fits: your own server, Vercel, Cloudflare Workers, or a split of web and
backend across hosts.

- **Go is the front door on self-host.** Gin serves static files, prerendered
  pages, `next.config` headers/redirects and your API routes itself, and
  proxies only dynamic pages (SSR, RSC, Server Actions) to Node/Bun workers.
- **Crons are Gin routes.** `app.Cron("name", "*/5 * * * *", handler)` registers
  `/_nextgin/cron/<name>`; every target runs a job by calling that route.
- **One codebase, several targets.** The same `main.go` and Next app build for
  each target; `nextgin build` generates the platform config.

Status: pre-release (`0.1.0`), private repository, not published to npm.
Self-host, Vercel and Cloudflare (combined) were verified in production
deploys (Cloudflare before the wasm size reduction, so its production CPU
numbers are for the old 42 MB build). The split topology and the Postgres/Redis helper are verified
locally only. Details and measurements: [docs/SPEC.md](docs/SPEC.md).

## Quick start (self-host)

```js
// next.config.mjs
import { createNextGinAdapter } from '@nextgin/adapter'
export default { adapterPath: createNextGinAdapter() }
```

```go
// main.go
app, err := nextgin.New("./.nextgin", nextgin.Config{})
if err != nil { log.Fatal(err) }

api := app.Router.Group("/api")
api.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })

app.Cron("heartbeat", "* * * * *", func(c *gin.Context) { c.Status(204) })

app.MountFrontend() // everything Gin has no route for goes to Next.js
log.Fatal(app.Run(":8080"))
```

```
nextgin build   # next build, finalize .nextgin, go build -o server
./server
nextgin dev     # next dev + Go server with restart on .go changes
```

Cron syntax is 5 fields, UTC, day-of-week `0-6`. Self-host runs an in-process
scheduler; set `NEXTGIN_SCHEDULER=off` to disable it on a replica.

## Choosing a deployment

| Mode | Command | Where things run | Notes |
|---|---|---|---|
| Self-host | `nextgin build` | One Go binary + Node/Bun workers | Go serves everything it can without Node |
| Vercel (combined) | `nextgin build --target vercel` | Next.js and the Go service both on Vercel | Writes root `vercel.json` (Vercel Services); crons are Vercel crons |
| Cloudflare (combined) | `nextgin build --target cloudflare` | OpenNext web Worker + Go compiled to wasm Worker | The web Worker reaches the Go Worker through a service binding; crons are Cron Triggers |
| Split | `nextgin build --web-target vercel --backend-target cloudflare\|container --backend-url https://api.example.com` | Next.js on Vercel, Go on its own host | See below |

Project layout for the platform targets: a root with `web/` (the Next.js app)
and `backend/` (a Go module with `main.go`). Do not name a directory `api/`:
Vercel treats it as legacy functions. `--web` and `--backend` change the
directory names.

### Vercel (combined)

- `vercel.json` is generated; `--check` fails if it is out of date.
- Set `CRON_SECRET` in the Vercel project: cron requests without it get 401
  (fail closed).
- The Hobby plan only allows daily crons; `--vercel-hobby` rejects anything
  more frequent at build time.
- If the Go module is private, the Vercel build needs credentials, for example
  `GIT_CREDENTIALS` passed as a build-only env, plus `GOPRIVATE`.

### Cloudflare (combined)

- Tested on the Workers Paid plan. The example's Go wasm Worker is about 10 MB
  (2.6 MB gzipped) after the CLI's size flags; the Free plan's size limit was
  not checked.
- OpenNext runs `npm run build` of the web app, so `web/package.json` needs a
  `build` script.
- The Go instance is persistent per isolate, but there can be many isolates:
  in-memory state is per isolate, not global. Use an external store.
- Build flags: the CLI builds with `-tags nomsgpack -trimpath -ldflags="-s -w"`
  and warns above 20 MiB. Gin's `c.ShouldBind*` links a validator and adds
  about 18 MB; use `nextgin.BindJSON(c, &v)` (no tag validation) instead.
  `--go-tags=` restores msgpack if you use it.

### Split: web and backend on different hosts

Use this when you want the backend, crons and webhooks off the web platform,
and your data already lives in managed Postgres/Redis.

- `--backend-target cloudflare`: the Go backend runs as a wasm Worker on a
  custom domain (workers.dev is rejected). No server to run.
- `--backend-target container`: writes `backend/Dockerfile` for the API-only
  `backend` target; run it on any host that runs containers. Run one instance,
  or set `NEXTGIN_SCHEDULER=off` on replicas so crons fire once.
- `--api-mode rewrite` (default): `web/vercel.json` rewrites your API prefixes to
  the backend URL, so the browser stays same-origin. `--api-mode direct`:
  the browser calls the backend URL itself (`NEXT_PUBLIC_NEXTGIN_API_URL`);
  add `nextgin.CORS("https://app.example.com")` and share cookies on the parent
  domain.
- The backend is public in this mode. Protect server-to-server routes with
  `nextgin.RequireInternal()` and set the same `NEXTGIN_INTERNAL_SECRET` on
  both sides; it rejects everything when the secret is unset.
- Set the Vercel project's Root Directory to the web directory, and set
  `NEXTGIN_INTERNAL_URL` to the backend URL.
- Crons run on the backend (scheduler or Cron Triggers), not on Vercel.

The split topology removes API, cron and webhook work from the web platform.
It does not reduce SSR or Server Action rendering on it, and whether
`rewrite` mode saves anything depends on how the platform bills external
rewrites, which has not been checked.

### Postgres and Redis from the Cloudflare backend

pgx and go-redis work in the wasm backend over Workers TCP sockets. Use the
same code on every target:

```go
cfg, _ := pgx.ParseConfig(os.Getenv("DATABASE_URL"))
cfg.DialFunc = nextgin.DialContext
cfg.LookupFunc = nextgin.LookupHost

rdb := redis.NewClient(&redis.Options{Addr: addr, Dialer: nextgin.DialContext})
```

Limits, found by testing under `wrangler dev` against local Postgres and Redis:

- A connection cannot be reused across requests on Cloudflare (the second
  request hangs). Open one per request and close it; put a pooler (PgBouncer,
  your provider's pooler) in front.
- Let the driver do TLS (`sslmode=require`); this works in wasm.
- pgx plus go-redis add roughly 15 MB of wasm.
- Not yet tested against managed providers or under load.

## Deploy to a VPS

```
nextgin deploy --host root@203.0.113.7 --domain shop.example.com
```

One command builds the app on your machine, ships it to the server over SSH
and starts it with Docker Compose: the app (Go front door plus Node workers),
Postgres and Redis when your Go module uses them, and Caddy with automatic
HTTPS when you pass `--domain`. Later runs are just `nextgin deploy` (the
host, name and domain are saved to `nextgin.deploy.json`, which never holds
secrets).

- **Databases:** Postgres and Redis are detected from your `go.mod`. The first
  deploy asks once per detected service whether to create it on the server
  (Enter = yes); answering no asks for the external URL (hidden input). Created
  services are wired into the app as `DATABASE_URL` and `REDIS_URL` using
  Docker service names, so there is nothing to configure and no database port
  is published.
- **Secrets:** generated once into `/opt/nextgin/<name>/.env` (mode 600) on the
  server and never regenerated; redeploys keep every existing value.
- **Server requirements:** Debian/Ubuntu family, root or passwordless sudo.
  Docker is installed with Docker's official script if missing
  (`--no-install-docker` to refuse).
- **On the server itself:** `nextgin deploy --local` does the same on the
  machine you are on; `--dir`, `--http-port` and `--https-port` change where
  it installs and which host ports it uses. `--dry-run` writes the generated
  files to `.nextgin/deploy/` without touching anything.
- **Limits:** one server, one app instance, a short interruption on each
  deploy, no rollback command, and **no database backups**. Behind Caddy, set
  `TrustForwardedHeaders: true` in your `nextgin.Config`.

Status: verified with `--local` on a Linux machine (first deploy, redeploy with
data and passwords preserved, external database, Caddy HTTPS). Not yet run
against a real VPS over SSH.

## Security notes

- Cron routes fail closed: no token, no run. The internal scheduler uses a
  generated per-process token; external triggers use `CRON_SECRET`.
- `RequireInternal` and `CORS` reject misconfiguration rather than defaulting
  to open (`CORS` panics on an empty or `*` allow-list).
- Gin runs in release mode on the vercel, cloudflare and backend targets
  unless `GIN_MODE` is set.

## Development

```
gofmt -l .
go vet ./...
go test -race -count=1 ./...
GOOS=js GOARCH=wasm go build ./...
(cd packages/adapter && npx tsc -p tsconfig.json && node --test test/)
npm run build -w fullstack-app && scripts/e2e.sh node && scripts/e2e.sh bun
```

`docs/SPEC.md` is the design record: target behaviour, measured numbers,
verification results and open items.
