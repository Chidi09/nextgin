# App Architecture & Code Patterns

How to structure a real `nextgin` application, write clean Go HTTP APIs, separate concerns without over-engineering, and keep the Next.js ↔ Go boundary crisp.

> **Reference implementation:** See [`examples/ecommerce-app/`](../examples/ecommerce-app/) for a complete working app following every pattern in this guide (`internal/domain`, `internal/store`, `internal/service`, `internal/httpapi`, `lib/api.ts`, and `httptest` API tests).

---

## 1. Directory Layout

Keep `main.go` thin (wiring only) and place application code under `internal/` so Go's compiler enforces encapsulation.

### Split Repo (`web/` + `backend/` — Recommended for Vercel + Container)

```text
my-app/
├── web/                            # Next.js App Router (UI, SSR, RSC)
│   ├── app/
│   │   ├── (store)/                # Route groups for UI pages
│   │   └── products/[id]/page.tsx
│   ├── components/                 # Client & Server UI components
│   ├── lib/
│   │   ├── api.ts                  # Typed client for Go API (browser + server)
│   │   └── types.ts                # Shared API response/request types
│   └── next.config.mjs
└── backend/                        # Pure-Go nextgin API + cron service
    ├── cmd/server/main.go          # Config loading, DI wiring, app.Run()
    ├── migrations/                 # Numbered SQL migrations (001_init.sql, ...)
    ├── internal/
    │   ├── domain/                 # Shared domain types & sentinel errors
    │   ├── store/                  # SQL queries (sqlc / database/sql / pgx)
    │   ├── service/                # Business logic & transaction boundaries
    │   ├── httpapi/                # HTTP handlers, DTOs, validation, middleware
    │   └── cronjobs/               # Scheduled job handlers
    └── go.mod
```

### Single-Repo Self-Host (`nextgin build` / `nextgin deploy`)

Same Go `internal/` layout at the repo root alongside `app/`, `components/`, and `lib/`:

```text
my-app/
├── main.go                         # Thin entrypoint (wires internal/* + app.MountFrontend())
├── internal/
│   ├── domain/
│   ├── store/
│   ├── service/
│   ├── httpapi/
│   └── cronjobs/
├── migrations/
├── app/                            # Next.js App Router
├── lib/api.ts
├── next.config.mjs
└── go.mod
```

> **Rule of thumb for small apps:** If a feature has trivial CRUD, you don't need separate `service` and `store` packages on day one — a single `internal/orders` package with `store.go` + `handler.go` is cleaner than empty pass-through layers. Introduce `service/` when an operation spans multiple tables, external APIs, or transactions (e.g., checkout, billing, auth).

---

## 2. Separation of Concerns (The 3-Layer Rule)

Each layer has **one job** and strict dependency direction: `httpapi → service → store → DB`.

| Layer | Owns | Must NOT Do |
|---|---|---|
| **`main.go`** | Reading env/config, opening DB pool, constructing stores/services/handlers, registering routes & crons, calling `app.Run`. | Contain SQL, business rules, or inline handler logic (beyond trivial health/ping). |
| **`httpapi`** | Parsing HTTP (`r.PathValue`, `ReadJSON`, cookies, headers), validating input shape, calling a service, mapping domain errors to HTTP status codes, writing JSON/cookies. | Run SQL directly, open DB transactions, or import `database/sql`. |
| **`service`** | Business rules, authorization checks ("can user X cancel order Y?"), DB transactions, calling third-party APIs (Stripe, email), idempotency. | Know about `http.Request`, `http.ResponseWriter`, status codes, or cookies. |
| **`store`** | Parameterized SQL (`sqlc` or `pgx`/`database/sql`), scanning rows into domain structs, translating DB errors (e.g., `sql.ErrNoRows` → `domain.ErrNotFound`, unique violation → `domain.ErrConflict`). | Make HTTP calls, format JSON responses, or enforce HTTP auth. |

---

## 3. Writing Clean Go API Handlers

### Struct-Based Handlers (No Package Globals)

Group related endpoints onto a handler struct that takes its dependencies explicitly via a constructor. Register routes in a single `RegisterRoutes` method:

```go
package httpapi

import (
	"net/http"

	"github.com/Chidi09/nextgin"
)

type OrderHandler struct {
	orders OrderService // narrow interface, easy to mock in unit tests
}

func NewOrderHandler(orders OrderService) *OrderHandler {
	return &OrderHandler{orders: orders}
}

func (h *OrderHandler) Register(r *nextgin.Router, requireAuth nextgin.Middleware) {
	orders := r.Group("/orders", requireAuth)
	orders.GET("", handle(h.List))
	orders.POST("", handle(h.Create))
	orders.GET("/{id}", handle(h.Get))
	orders.POST("/{id}/cancel", handle(h.Cancel))
}
```

### Centralized Error Handling (`handle` Wrapper)

Standard `http.HandlerFunc` forces repetitive `if err != nil { WriteError(...); return }` blocks and makes it easy to accidentally leak `sql.ErrNoRows` or raw SQL errors to clients.

Instead, define domain errors in `internal/domain/errors.go`:

```go
package domain

import "errors"

var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrInsufficientStock = errors.New("insufficient stock")
)

type ValidationError struct {
	Fields map[string]string
}

func (e ValidationError) Error() string { return "validation failed" }
```

Then write one small adapter in `internal/httpapi/respond.go`:

```go
package httpapi

import (
	"errors"
	"log"
	"net/http"

	"github.com/Chidi09/nextgin"
	"myapp/internal/domain"
)

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func handle(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			writeDomainError(w, r, err)
		}
	}
}

func writeDomainError(w http.ResponseWriter, r *http.Request, err error) {
	var valErr domain.ValidationError
	switch {
	case errors.As(err, &valErr):
		nextgin.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":  "validation failed",
			"fields": valErr.Fields,
		})
	case errors.Is(err, nextgin.ErrBodyTooLarge):
		nextgin.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
	case errors.Is(err, domain.ErrNotFound):
		nextgin.WriteError(w, http.StatusNotFound, "resource not found")
	case errors.Is(err, domain.ErrConflict):
		nextgin.WriteError(w, http.StatusConflict, "resource already exists")
	case errors.Is(err, domain.ErrUnauthorized):
		nextgin.WriteError(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, domain.ErrForbidden):
		nextgin.WriteError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, domain.ErrInsufficientStock):
		nextgin.WriteError(w, http.StatusConflict, err.Error())
	default:
		// Never leak internal or SQL errors to clients; log them server-side.
		log.Printf("http error %s %s: %v", r.Method, r.URL.Path, err)
		nextgin.WriteError(w, http.StatusInternalServerError, "internal server error")
	}
}
```

Now every handler stays linear, readable, and leak-free:

```go
func (h *OrderHandler) Get(w http.ResponseWriter, r *http.Request) error {
	user := UserFromContext(r.Context())
	id := r.PathValue("id")

	order, err := h.orders.GetForUser(r.Context(), user.ID, id)
	if err != nil {
		return err
	}
	nextgin.WriteJSON(w, http.StatusOK, toOrderResponse(order))
	return nil
}
```

### Request DTOs & Explicit Validation

Never unmarshal HTTP JSON directly into database/domain structs (prevents mass-assignment bugs like a client sending `"role": "admin"` or `"status": "paid"`). Use dedicated request structs with a `Validate()` method:

```go
type createOrderRequest struct {
	ShippingAddress string `json:"shippingAddress"`
	CouponCode      string `json:"couponCode"`
}

func (req createOrderRequest) Validate() error {
	fields := map[string]string{}
	if strings.TrimSpace(req.ShippingAddress) == "" {
		fields["shippingAddress"] = "required"
	}
	if len(req.CouponCode) > 32 {
		fields["couponCode"] = "must be 32 characters or fewer"
	}
	if len(fields) > 0 {
		return domain.ValidationError{Fields: fields}
	}
	return nil
}

func (h *OrderHandler) Create(w http.ResponseWriter, r *http.Request) error {
	var req createOrderRequest
	if err := nextgin.ReadJSON(r, &req); err != nil {
		if errors.Is(err, nextgin.ErrBodyTooLarge) {
			return err
		}
		return domain.ValidationError{Fields: map[string]string{"body": "invalid JSON"}}
	}
	if err := req.Validate(); err != nil {
		return err
	}

	user := UserFromContext(r.Context())
	order, err := h.orders.Checkout(r.Context(), user.ID, req.ShippingAddress, req.CouponCode)
	if err != nil {
		return err
	}
	nextgin.WriteJSON(w, http.StatusCreated, toOrderResponse(order))
	return nil
}
```

### Consistent Response & Pagination Shapes

Pick one JSON convention across all endpoints and stick to it:
- **Single resource:** return the object directly (`{"id": "...", "status": "paid"}`).
- **Collections:** always wrap in an object (`{"items": [...], "nextCursor": "..."}`) — never a bare top-level JSON array, so you can add pagination or metadata later without breaking clients.
- **Mutations with no body:** `w.WriteHeader(http.StatusNoContent)` (`204`).
- **Errors:** `{"error": "message"}` (from `nextgin.WriteError`) plus optional `"fields": {...}` for validation errors.
- **Money:** store and return integer minor units (`priceCents: 2999`, `currency: "USD"`), never floats.

---

## 4. Middleware & Typed Request Context

### Private Context Keys

Never use raw strings (`"user"`) as `context.WithValue` keys. Define unexported struct types and typed accessors:

```go
package httpapi

import (
	"context"
	"net/http"

	"github.com/Chidi09/nextgin"
	"myapp/internal/domain"
)

type userCtxKey struct{}

func withUser(ctx context.Context, u domain.User) context.Context {
	return context.WithValue(ctx, userCtxKey{}, u)
}

func UserFromContext(ctx context.Context) domain.User {
	u, _ := ctx.Value(userCtxKey{}).(domain.User)
	return u
}

func RequireAuth(sessions SessionService) nextgin.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie("sid")
			if err != nil || c.Value == "" {
				nextgin.WriteError(w, http.StatusUnauthorized, "sign in required")
				return
			}
			user, err := sessions.Resolve(r.Context(), c.Value)
			if err != nil {
				nextgin.WriteError(w, http.StatusUnauthorized, "invalid session")
				return
			}
			next.ServeHTTP(w, r.WithContext(withUser(r.Context(), user)))
		})
	}
}
```

### Scoped Middleware Composition

Attach middleware at the **narrowest scope** that needs it:

```go
// 1. Global (every Go route): request logging, recovery, CORS (split mode only)
app.Router.Use(requestLogger)
if origin := os.Getenv("CORS_ORIGIN"); origin != "" {
	app.Router.Use(nextgin.CORS(origin))
}

// 2. Public API group
api := app.Router.Group("/api")
productHandler.Register(api)

// 3. Per-route middleware for tight rate limits
authLimit := nextgin.RateLimitWithConfig(nextgin.RateLimitConfig{
	RPS:                   2,
	Burst:                 10,
	TrustForwardedHeaders: cfg.TrustProxy,
})
api.POST("/auth/login", handle(authHandler.Login), authLimit)
api.POST("/auth/register", handle(authHandler.Register), authLimit)

// 4. Authenticated group
authed := api.Group("", RequireAuth(sessionSvc))
orderHandler.Register(authed)
cartHandler.Register(authed)

// 5. Internal server-to-server group (Next.js SSR -> Go)
internal := app.Router.Group("/internal", nextgin.RequireInternal())
internal.GET("/users/{id}/summary", handle(userHandler.InternalSummary))
```

---

## 5. Transactions, Context & Idempotency

### Always Propagate `r.Context()`

Every service and store method takes `ctx context.Context` as its first parameter and passes it to `db.QueryContext` / `db.ExecContext` / `http.NewRequestWithContext`. When a browser cancels a request or `SIGTERM` shuts down a cron job, in-flight DB queries cancel immediately instead of piling up connections.

### Keep Transactions Inside the Service/Store Layer

HTTP handlers should never call `db.BeginTx`. Wrap multi-step mutations (like checkout: verify stock → decrement inventory → insert order → clear cart) in one atomic transaction helper inside `service` or `store`:

```go
func (s *OrderService) Checkout(ctx context.Context, userID, addr, coupon string) (domain.Order, error) {
	var order domain.Order
	err := s.tx.WithinTx(ctx, func(ctx context.Context, q store.Querier) error {
		items, err := q.ListCartItems(ctx, userID)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return domain.ValidationError{Fields: map[string]string{"cart": "is empty"}}
		}
		// Decrement stock with atomic guard:
		// UPDATE products SET stock = stock - $1 WHERE id = $2 AND stock >= $1
		// ...
		order, err = q.CreateOrder(ctx, userID, addr, items)
		if err != nil {
			return err
		}
		return q.ClearCart(ctx, userID)
	})
	return order, err
}
```

### Idempotency for Payments & Webhooks

- **Webhooks:** store the provider's event ID in a `processed_webhook_events(event_id PRIMARY KEY)` table inside the same transaction as the state transition (`UPDATE orders SET status = 'paid' WHERE id = $1 AND status = 'pending'`). Duplicate deliveries become safe no-ops.
- **Checkout / mutations:** accept an optional `Idempotency-Key` header from the client on retryable `POST` endpoints (`POST /api/orders`).

---

## 6. Frontend ↔ Backend Boundary (Next.js ↔ Go)

### Where Does Each Piece Live?

| Concern | Put It In | Why |
|---|---|---|
| Auth, sessions, RBAC, rate limiting | **Go (`httpapi` + `service`)** | Hot path, fail-closed cookies, zero Node overhead. |
| Cart, checkout, payments, webhooks | **Go (`service` + `store`)** | Transactional integrity, raw-body HMAC verification, DB pooling. |
| Scheduled jobs & cleanup | **Go (`app.Cron`)** | In-process scheduler, graceful `SIGTERM`, no Vercel Cron limits. |
| Page layout, SEO, SSR, streaming | **Next.js Server Components** | Native App Router strengths. |
| Interactive forms, optimistic UI | **Next.js Client Components** | Calls Go `/api/*` via `lib/api.ts`. |
| Form mutations that need `revalidatePath` / `revalidateTag` | **Next.js Server Action → Go `/internal/*`** | Thin Server Action forwards to Go over `NEXTGIN_INTERNAL_URL`, then revalidates Next.js cache. |

### One Typed Client Module (`lib/api.ts`)

Avoid scattering raw `fetch("/api/...")` calls across components. Centralize base URLs, cookies, internal headers, and error parsing in `lib/api.ts`:

```ts
// web/lib/api.ts
export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public fields?: Record<string, string>,
  ) {
    super(message)
  }
}

const PUBLIC_BASE = process.env.NEXT_PUBLIC_NEXTGIN_API_URL ?? ''

/** Use in Client Components (browser). Sends HttpOnly session cookies automatically. */
export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${PUBLIC_BASE}${path}`, {
    ...init,
    credentials: 'include',
    headers: {
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  })
  return parseResponse<T>(res)
}

/** Use in Server Components & Server Actions only. Calls Go directly over loopback/internal URL. */
export async function internalFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const base = process.env.NEXTGIN_INTERNAL_URL ?? 'http://127.0.0.1:8080'
  const res = await fetch(`${base}${path}`, {
    ...init,
    cache: init?.cache ?? 'no-store',
    headers: {
      'X-Nextgin-Internal': process.env.NEXTGIN_INTERNAL_SECRET ?? '',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  })
  return parseResponse<T>(res)
}

async function parseResponse<T>(res: Response): Promise<T> {
  if (res.status === 204) return undefined as T
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    throw new ApiError(res.status, data.error ?? res.statusText, data.fields)
  }
  return data as T
}
```

---

## 7. Testing Patterns

Because Go owns the API and business logic, you can test the entire backend in milliseconds with `go test -race ./...` — **no Node process or Next.js build required**.

### Handler Unit Tests (`httptest`)

Mount your handlers onto a fresh `nextgin.New("", nextgin.Config{})` (with `NEXTGIN_TARGET=backend` so no frontend build is needed) and hit `app.Router.ServeHTTP`:

```go
func TestCreateOrder_ValidationError(t *testing.T) {
	t.Setenv("NEXTGIN_TARGET", "backend")
	app, err := nextgin.New("", nextgin.Config{})
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewOrderHandler(&fakeOrderService{})
	h.Register(app.Router.Group("/api"), fakeAuthMiddleware(domain.User{ID: "u1"}))

	req := httptest.NewRequest(http.MethodPost, "/api/orders", strings.NewReader(`{"shippingAddress":""}`))
	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got status %d, want 422: %s", rec.Code, rec.Body.String())
	}
}
```

### Store & Integration Tests

- **SQLite apps:** run migrations against `file::memory:?cache=shared` (or `t.TempDir()/test.db`) per test so tests run in parallel with `-race`.
- **Postgres apps:** create an isolated schema or transaction per test that rolls back on `t.Cleanup`.
- **Cron jobs:** invoke your cron handler directly via `httptest` (or test the underlying service method) and pass a cancellable `context.WithCancel` to verify graceful shutdown.

---

## 8. Anti-Patterns to Avoid

| Don't Do This | Do This Instead |
|---|---|
| Putting all routes, SQL, and helpers in `main.go` | Keep `main.go` as wiring; split `httpapi`, `service`, `store`, `domain` under `internal/`. |
| Returning `err.Error()` directly from `sql.DB` to `WriteError` | Map `sql.ErrNoRows` / DB errors to domain sentinel errors; log unexpected errors server-side and return `"internal server error"`. |
| Unmarshaling JSON directly into DB structs | Decode into explicit request DTOs with `Validate()`. |
| Writing to the same DB tables from both Prisma/Drizzle in Next.js and SQL in Go | Give Go sole ownership of runtime queries; see [COOKBOOK.md §2](COOKBOOK.md#2-database--orm-best-practices-what-about-drizzle-or-prisma). |
| Using `app.Router.Use(authMiddleware)` globally | Scope auth middleware to authenticated route groups so public pages, webhooks, `/_nextgin/health`, and static assets stay unaffected. |
| Calling `nextgin.ReadJSON` on payment webhooks | Always use `nextgin.ReadRawBody`, verify HMAC in constant time, then `json.Unmarshal`. |
| Putting business logic inside Next.js Route Handlers (`app/api/*`) | Put hot API logic in Go; reserve Next.js for UI, SSR, RSC, and thin Server Actions that call Go. |
