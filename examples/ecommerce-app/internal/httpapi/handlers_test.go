package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chidi09/nextgin"

	"example.com/ecommerce-app/internal/domain"
	"example.com/ecommerce-app/internal/httpapi"
	"example.com/ecommerce-app/internal/service"
	"example.com/ecommerce-app/internal/store"
)

func newTestApp(t *testing.T) (*nextgin.App, *service.StoreService) {
	t.Helper()
	repo, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	svc := service.NewStoreService(repo, "test_webhook_secret")
	if err := svc.EnsureDemoUser(context.Background()); err != nil {
		t.Fatalf("seed demo user: %v", err)
	}

	t.Setenv("NEXTGIN_TARGET", "backend")
	app, err := nextgin.New("", nextgin.Config{})
	if err != nil {
		t.Fatalf("nextgin.New: %v", err)
	}
	httpapi.NewHandlers(svc, nextgin.CookieOptions{Insecure: true}, app.Router).Register(app.Router.Group("/api"))
	return app, svc
}

func TestEcommerceFlow_CatalogGuestCartLoginCheckoutWebhook(t *testing.T) {
	app, svc := newTestApp(t)
	h := app.Router

	// 1. Seeded catalog is served from SQLite.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/products", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/products: got %d, want 200", rec.Code)
	}
	var products []domain.Product
	if err := json.Unmarshal(rec.Body.Bytes(), &products); err != nil || len(products) == 0 {
		t.Fatalf("expected seeded products, got len=%d err=%v", len(products), err)
	}

	// 2. Guest adds an item to cart -> sets cid cookie.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/cart/items", strings.NewReader(`{"productId":1,"qty":2}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/cart/items: got %d: %s", rec.Code, rec.Body.String())
	}
	var cidCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "cid" {
			cidCookie = c
		}
	}
	if cidCookie == nil {
		t.Fatalf("expected guest cid cookie")
	}

	// 3. Unauthenticated checkout fails with 401.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/checkout", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated checkout: got %d, want 401", rec.Code)
	}

	// 4. Invalid login returns 422 with structured validation fields.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"bad","password":"12"}`)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid login: got %d, want 422", rec.Code)
	}

	// 5. Demo login merges guest cart and sets HttpOnly sid cookie.
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"demo@nextgin.dev","password":"demo1234"}`))
	loginReq.AddCookie(cidCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, loginReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: got %d: %s", rec.Code, rec.Body.String())
	}
	var sidCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "sid" {
			sidCookie = c
		}
	}
	if sidCookie == nil || !sidCookie.HttpOnly {
		t.Fatalf("expected HttpOnly sid cookie")
	}

	// 6. Authenticated checkout creates pending order from merged cart.
	coReq := httptest.NewRequest(http.MethodPost, "/api/checkout", nil)
	coReq.AddCookie(sidCookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, coReq)
	if rec.Code != http.StatusCreated {
		t.Fatalf("checkout: got %d: %s", rec.Code, rec.Body.String())
	}
	var order domain.Order
	if err := json.Unmarshal(rec.Body.Bytes(), &order); err != nil {
		t.Fatalf("decode order: %v", err)
	}
	if order.Status != "pending" || len(order.Items) != 1 || order.Items[0].Qty != 2 {
		t.Fatalf("unexpected order: %+v", order)
	}

	// 7. Payment webhook rejects invalid HMAC signature.
	payload, sig := svc.SignPaymentEvent(order.ID)
	badHook := httptest.NewRequest(http.MethodPost, "/api/webhooks/pay", bytes.NewReader(payload))
	badHook.Header.Set("X-Pay-Signature", "deadbeef")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, badHook)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad webhook signature: got %d, want 401", rec.Code)
	}

	// 8. Payment webhook accepts valid HMAC signature and marks order paid.
	goodHook := httptest.NewRequest(http.MethodPost, "/api/webhooks/pay", bytes.NewReader(payload))
	goodHook.Header.Set("X-Pay-Signature", sig)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, goodHook)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid webhook: got %d: %s", rec.Code, rec.Body.String())
	}
	var paid domain.Order
	if err := json.Unmarshal(rec.Body.Bytes(), &paid); err != nil {
		t.Fatalf("decode paid order: %v", err)
	}
	if paid.Status != "paid" || paid.PaidAt == nil {
		t.Fatalf("expected paid order, got %+v", paid)
	}
}
