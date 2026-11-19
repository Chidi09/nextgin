# nextgin Cookbook & Best Practices

Practical recipes for deploying and hardening apps with Next.js + pure-Go `nextgin`. For application folder layout, separation of concerns (`httpapi → service → store`), handler/error patterns, and the Next.js ↔ Go client boundary, see [docs/PATTERNS.md](PATTERNS.md).

---

## 1. Choosing Your Architecture

Pick **one** of these two production layouts:

| Pattern | Best For | Hosting |
|---|---|---|
| **Split (`--web-target vercel --backend-target container`)** | Keeping Next.js on Vercel Hobby/Pro while moving auth, cart, payments, webhooks, and crons off Vercel. | Next.js on Vercel + tiny Go container on Fly.io / Cloud Run / Railway / $5 VPS. |
| **Cloudflare (Pages + Containers)** | Running both Next.js and the native Go binary on Cloudflare with zero cold-start Node API bloat. | Next.js on Cloudflare Pages + Go binary on Cloudflare Containers + Cron Triggers. |
| **All-in-One VPS (`nextgin deploy`)** | Zero platform limits, zero cross-origin cookie setup, lowest flat monthly cost. | Single $5–10/mo VPS running Go front door + Node workers + Postgres/Redis + Caddy HTTPS. |

### Why Split Defaults to `--api-mode direct`
In split mode, `--api-mode direct` sends browser API calls straight to `https://api.yourdomain.com` instead of proxying them through Vercel rewrites:
- **Zero Vercel compute/bandwidth** spent on API traffic or webhooks.
- **Zero Vercel Cron invocations** (all crons run in Go's in-process scheduler).
- **Same-site cookies still work seamlessly** by setting `Domain: ".yourdomain.com"` on `nextgin.CookieOptions` and adding `nextgin.CORS("https://yourdomain.com")`.

Use `--api-mode rewrite` only if you cannot put the Go backend on a subdomain of the same root domain.

---

## 2. Database & ORM Best Practices (What About Drizzle or Prisma?)

A common question from Next.js developers: *"Should I use Drizzle or Prisma alongside Go?"*

### Short Answer
**Do not split runtime writes across two ORMs.** Having Prisma/Drizzle mutate tables in Node while Go writes SQL against the same tables causes schema drift, duplicated validation, and subtle bugs. Pick **one** of these clean patterns:

### Pattern A (Recommended): SQL Migrations + `sqlc` in Go
- **Migrations:** Plain SQL files managed by `goose`, `golang-migrate`, or `atlas`.
- **Queries:** Write `.sql` queries and generate 100% type-safe Go code with [`sqlc`](https://sqlc.dev/) (zero ORM runtime overhead, full `pgx` or SQLite speed).
- **Next.js:** Never connects to the DB directly. Server Components fetch Go over `NEXTGIN_INTERNAL_URL`; client components call Go API endpoints.
- **Why it wins:** One DB connection pool (in Go), one schema owner, ~15–20× higher API throughput than Node route handlers, and no Prisma binary bloat in Next.js bundles.

### Pattern B (Pragmatic for TypeScript Teams): Drizzle/Prisma as *Schema & Migration Tool Only*
If your team already loves Drizzle Kit (`drizzle-kit generate` / `drizzle-kit migrate` / Drizzle Studio) or Prisma Migrate:
1. Keep your schema definition in `web/db/schema.ts` (Drizzle) or `prisma/schema.prisma`.
2. Run migrations with Drizzle/Prisma in CI/deploy.
3. Let **Go own runtime API reads/writes** (using `sqlc` pointed at the generated SQL migrations, or `pgx`/`database/sql`).
4. Do **not** import the TS ORM in hot API paths.

### Pattern C (Domain Split — Only When Needed)
If you have read-heavy CMS/editorial pages in Next.js Server Components that you want to query with Drizzle directly, draw a **hard table boundary**:
- **Go owns:** `users`, `sessions`, `carts`, `orders`, `payments`, `cron_runs` (transactional, rate-limited, webhook-driven).
- **Next.js (read-only) reads:** `pages`, `posts`, `catalog` (or just calls Go over loopback `NEXTGIN_INTERNAL_URL`, which is usually simpler and avoids maintaining two DB pools).

---

## 3. Auth, Sessions & Rate Limiting

Always rate-limit `/login` and `/register`, and set `HttpOnly` + `Secure` + `SameSite=Lax` cookies via `nextgin.SetSessionCookie`:

```go
cookieOpts := nextgin.CookieOptions{
    Domain:   ".example.com", // omit on single-host selfhost
    MaxAge:   7 * 86400,
    Insecure: os.Getenv("COOKIE_INSECURE") == "1", // true ONLY on http://localhost
}

authLimit := nextgin.RateLimitWithConfig(nextgin.RateLimitConfig{
    RPS:                   2,
    Burst:                 10,
    TrustForwardedHeaders: true, // only behind Caddy / Fly / Cloud Run / Cloudflare
})

api.POST("/auth/login", func(w http.ResponseWriter, r *http.Request) {
    var body struct {
        Email    string `json:"email"`
        Password string `json:"password"`
    }
    if err := nextgin.ReadJSON(r, &body); err != nil {
        if errors.Is(err, nextgin.ErrBodyTooLarge) {
            nextgin.WriteError(w, http.StatusRequestEntityTooLarge, "body too large")
            return
        }
        nextgin.WriteError(w, http.StatusBadRequest, "invalid json")
        return
    }
    // Verify password (bcrypt/argon2), create session row in DB...
    nextgin.SetSessionCookie(w, "sid", sessionToken, cookieOpts)
    nextgin.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}, authLimit)

api.POST("/auth/logout", func(w http.ResponseWriter, r *http.Request) {
    // Delete session row in DB...
    nextgin.ClearSessionCookie(w, "sid", cookieOpts)
    w.WriteHeader(http.StatusNoContent)
})
```

---

## 4. Payment Webhooks (Stripe / Paystack / LemonSqueezy)

**Rule:** Never parse a webhook with `ReadJSON` before verifying its signature. Always read the exact raw bytes with `nextgin.ReadRawBody`, verify the HMAC in constant time, and make the handler **idempotent** (payment providers retry webhooks):

```go
api.POST("/webhooks/pay", func(w http.ResponseWriter, r *http.Request) {
    raw, err := nextgin.ReadRawBody(w, r, 256<<10) // 256 KiB cap
    if err != nil {
        nextgin.WriteError(w, http.StatusBadRequest, "invalid body")
        return
    }

    mac := hmac.New(sha256.New, []byte(os.Getenv("PAYMENT_WEBHOOK_SECRET")))
    mac.Write(raw)
    expected := hex.EncodeToString(mac.Sum(nil))
    if !hmac.Equal([]byte(r.Header.Get("X-Pay-Signature")), []byte(expected)) {
        nextgin.WriteError(w, http.StatusUnauthorized, "invalid signature")
        return
    }

    var evt struct {
        EventID string `json:"id"`
        OrderID string `json:"orderId"`
    }
    if err := json.Unmarshal(raw, &evt); err != nil {
        nextgin.WriteError(w, http.StatusBadRequest, "malformed event")
        return
    }

    // Idempotent update: UPDATE orders SET status='paid' WHERE id=$1 AND status='pending'
    w.WriteHeader(http.StatusOK)
})
```

---

## 5. Reliable Cron Jobs & Rolling-Deploy Locking

Crons run in-process on `selfhost` and `backend` targets — no Vercel Hobby daily limit and no per-invocation billing.

### Always Respect `r.Context()`
On `SIGTERM`, `nextgin` cancels the request context passed to every running cron job and waits up to `Config.ShutdownGrace` (default 10s):

```go
app.Cron("expire-carts", "*/5 * * * *", func(w http.ResponseWriter, r *http.Request) {
    if _, err := db.ExecContext(r.Context(), `DELETE FROM cart_items WHERE updated_at < NOW() - INTERVAL '15 minutes'`); err != nil {
        nextgin.WriteError(w, http.StatusInternalServerError, err.Error())
        return
    }
    w.WriteHeader(http.StatusNoContent)
})
```

### Preventing Double-Runs During Rolling Deploys (`CronLocker`)
During a zero-downtime deploy, old and new containers overlap for 10–30 seconds. Implement `nextgin.CronLocker` with a Postgres advisory lock (or Redis `SET NX PX`) so only one container runs a job at a time:

```go
type pgCronLocker struct{ db *sql.DB }

func (l pgCronLocker) TryLock(ctx context.Context, jobName string, _ time.Duration) (func(), bool, error) {
    conn, err := l.db.Conn(ctx)
    if err != nil {
        return nil, false, err
    }
    key := int64(fnvHash64("nextgin:cron:" + jobName))
    var acquired bool
    if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&acquired); err != nil {
        conn.Close()
        return nil, false, err
    }
    if !acquired {
        conn.Close()
        return nil, false, nil
    }
    return func() {
        _, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, key)
        conn.Close()
    }, true, nil
}
```

Pass it into `nextgin.Config{CronLocker: pgCronLocker{db: db}}`.

---

## 6. Server-to-Server Calls (Next.js SSR / Server Actions → Go)

When Next.js Server Components or Server Actions need private data from Go:

1. Protect the Go route group with `nextgin.RequireInternal()`:
   ```go
   internal := app.Router.Group("/internal", nextgin.RequireInternal())
   internal.GET("/orders/{id}", handleGetOrder)
   ```
2. Set the same `NEXTGIN_INTERNAL_SECRET` on both Next.js and Go.
3. Call Go from Next.js using `NEXTGIN_INTERNAL_URL`:
   ```ts
   const res = await fetch(`${process.env.NEXTGIN_INTERNAL_URL}/internal/orders/${id}`, {
     headers: { 'X-Nextgin-Internal': process.env.NEXTGIN_INTERNAL_SECRET! },
     cache: 'no-store',
   })
   ```

---

## 7. Deploying on Cloudflare (Pages + Containers)

Because `NEXTGIN_TARGET=backend` runs your pure-Go API and cron routes as a standalone ~7.5 MB HTTP server (with `app.MountFrontend()` as a no-op), it fits cleanly inside a `lite` (256 MiB) **Cloudflare Container** while your Next.js frontend lives on **Cloudflare Pages**.

### 1. Build the Linux/AMD64 Go Binary

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o cf-backend/bin/server .
```

### 2. Minimal Container `Dockerfile` (`cf-backend/Dockerfile`)

```dockerfile
FROM alpine:3.21
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY bin/server /app/server
RUN chmod +x /app/server
ENV NEXTGIN_TARGET=backend \
    PORT=8080 \
    TRUST_PROXY=1
EXPOSE 8080
CMD ["/app/server"]
```

### 3. Wrangler Config (`cf-backend/wrangler.jsonc`)

```jsonc
{
  "name": "my-app-api",
  "main": "src/index.ts",
  "compatibility_date": "2026-03-01",
  "compatibility_flags": ["nodejs_compat"],
  "containers": [
    {
      "class_name": "NextginGoBackend",
      "image": "./Dockerfile",
      "instance_type": "lite",
      "max_instances": 2
    }
  ],
  "durable_objects": {
    "bindings": [
      { "name": "NEXTGIN_GO", "class_name": "NextginGoBackend" }
    ]
  },
  "migrations": [
    { "tag": "v1", "new_sqlite_classes": ["NextginGoBackend"] }
  ],
  "routes": [
    { "pattern": "api.example.com", "custom_domain": true }
  ],
  "triggers": {
    "crons": ["*/15 * * * *"]
  }
}
```

### 4. Worker Entry + Cron Trigger (`cf-backend/src/index.ts`)

Cloudflare Containers can sleep after idle periods (`sleepAfter`). Forwarding Worker Cron Triggers to `nextgin`'s authenticated `/_nextgin/cron/<name>` route guarantees your jobs run even when the container is asleep:

```ts
import { Container, getContainer } from '@cloudflare/containers'

interface Env {
  NEXTGIN_GO: DurableObjectNamespace<NextginGoBackend>
  CRON_SECRET?: string
}

export class NextginGoBackend extends Container<Env> {
  defaultPort = 8080
  sleepAfter = '15m'

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env)
    this.envVars = {
      NEXTGIN_TARGET: 'backend',
      PORT: '8080',
      TRUST_PROXY: '1',
      ...(env.CRON_SECRET ? { CRON_SECRET: env.CRON_SECRET } : {}),
    }
  }
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    return getContainer(env.NEXTGIN_GO, 'primary').fetch(request)
  },

  async scheduled(_event: ScheduledEvent, env: Env): Promise<void> {
    const headers: Record<string, string> = {}
    if (env.CRON_SECRET) {
      headers['Authorization'] = `Bearer ${env.CRON_SECRET}`
    }
    await getContainer(env.NEXTGIN_GO, 'primary').fetch(
      new Request('http://container/_nextgin/cron/expire-carts', {
        method: 'POST',
        headers,
      }),
    )
  },
}
```

Deploy the Go container backend:

```bash
cd cf-backend
npx wrangler secret put CRON_SECRET   # optional, matches CRON_SECRET in Go
npx wrangler deploy
```

### 5. Connect the Next.js Frontend on Cloudflare Pages

You have two options for routing `/api/*` from Cloudflare Pages to your Go container:

- **Direct subdomain (recommended for high traffic):** Point `NEXT_PUBLIC_NEXTGIN_API_URL=https://api.example.com`, add `nextgin.CORS("https://example.com")`, and set `Domain: ".example.com"` on session cookies.
- **Same-origin Pages Function proxy:** Add `functions/api/[[path]].js` in your Pages project and set `NEXTGIN_BACKEND_URL=https://api.example.com` in Cloudflare Pages environment variables:

```js
// functions/api/[[path]].js
export async function onRequest(context) {
  const backend = context.env.NEXTGIN_BACKEND_URL
  if (!backend) {
    return new Response(JSON.stringify({ error: 'NEXTGIN_BACKEND_URL not set' }), {
      status: 502,
      headers: { 'content-type': 'application/json' },
    })
  }
  const incoming = new URL(context.request.url)
  const target = new URL(incoming.pathname + incoming.search, backend)
  return fetch(new Request(target.toString(), context.request))
}
```

> **Note:** Cloudflare Containers requires the Cloudflare Workers Paid plan.

---

## 8. Production Checklist

- [ ] **Reverse proxy:** Set `Config.TrustForwardedHeaders: true` when behind Caddy, Nginx, Cloudflare, Fly.io, or Cloud Run (leave `false` if exposed directly to the internet).
- [ ] **Healthcheck:** Point Docker / Fly.io / Cloud Run / Load Balancer probes at `GET /_nextgin/health`.
- [ ] **Cookies:** Leave `CookieOptions.Insecure: false` in production (HTTPS).
- [ ] **Secrets:** Set `CRON_SECRET` (if triggering crons externally) and `NEXTGIN_INTERNAL_SECRET` (for `/internal/*` routes).
- [ ] **SQLite (if used):** Enable WAL mode + `busy_timeout(5000)` + `foreign_keys(ON)` and mount a persistent volume (single-instance deploys only; use Postgres for multi-instance).
- [ ] **Backups:** Set up automated Postgres/SQLite backups (not handled by `nextgin deploy`).
