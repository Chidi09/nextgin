package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Chidi09/nextgin"
)

type Product struct {
	ID          int    `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	PriceCents  int    `json:"priceCents"`
	Stock       int    `json:"stock"`
	Description string `json:"description"`
	Badge       string `json:"badge,omitempty"`
}

type User struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type CartItem struct {
	ProductID  int    `json:"productId"`
	Name       string `json:"name"`
	PriceCents int    `json:"priceCents"`
	Qty        int    `json:"qty"`
}

type Cart struct {
	Items      []CartItem `json:"items"`
	TotalCents int        `json:"totalCents"`
}

type Order struct {
	ID         string     `json:"id"`
	UserEmail  string     `json:"userEmail"`
	Items      []CartItem `json:"items"`
	TotalCents int        `json:"totalCents"`
	Status     string     `json:"status"` // pending | paid
	CreatedAt  time.Time  `json:"createdAt"`
	PaidAt     *time.Time `json:"paidAt,omitempty"`
}

type CronStats struct {
	Ticks        int64      `json:"ticks"`
	ExpiredCarts int64      `json:"expiredCarts"`
	LastRunAt    *time.Time `json:"lastRunAt,omitempty"`
}

func main() {
	port := envOr("PORT", "8080")
	dbPath, err := filepath.Abs(envOr("SQLITE_PATH", "./store.db"))
	if err != nil {
		log.Fatalf("resolve sqlite path: %v", err)
	}
	webhookSecret := envOr("PAYMENT_WEBHOOK_SECRET", "whsec_local_demo_secret")
	insecureCookies := os.Getenv("COOKIE_INSECURE") != "0"

	store, err := openStore(dbPath)
	if err != nil {
		log.Fatalf("open sqlite %s: %v", dbPath, err)
	}
	defer store.Close()

	app, err := nextgin.New("./.nextgin", nextgin.Config{
		Workers: 2,
		Runtime: envOr("NEXTGIN_RUNTIME", "node"),
		Port:    port,
		Env:     []string{"SQLITE_PATH=" + dbPath},
	})
	if err != nil {
		log.Fatal(err)
	}

	cookieOpts := nextgin.CookieOptions{
		MaxAge:   7 * 86400,
		Insecure: insecureCookies,
	}

	getSession := func(r *http.Request) (User, string, bool) {
		c, err := r.Cookie("sid")
		if err != nil || c.Value == "" {
			return User{}, "", false
		}
		u, ok := store.getSession(c.Value)
		return u, c.Value, ok
	}

	getOrCreateCartKey := func(w http.ResponseWriter, r *http.Request) string {
		if u, _, ok := getSession(r); ok {
			return "user:" + u.Email
		}
		if c, err := r.Cookie("cid"); err == nil && c.Value != "" {
			return "guest:" + c.Value
		}
		cid := randomToken(16)
		nextgin.SetSessionCookie(w, "cid", cid, cookieOpts)
		return "guest:" + cid
	}

	// 1-minute cron: sweeps abandoned carts older than 15m in SQLite.
	app.Cron("expire-carts", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
		if _, err := store.expireOldCarts(15 * time.Minute); err != nil {
			nextgin.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	api := app.Router.Group("/api")

	api.GET("/health", func(w http.ResponseWriter, r *http.Request) {
		nextgin.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "engine": "pure-go+sqlite"})
	})

	api.GET("/cron/stats", func(w http.ResponseWriter, r *http.Request) {
		stats, err := store.cronStats()
		if err != nil {
			nextgin.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		nextgin.WriteJSON(w, http.StatusOK, stats)
	})

	// Products (backed by SQLite)
	api.GET("/products", func(w http.ResponseWriter, r *http.Request) {
		products, err := store.listProducts()
		if err != nil {
			nextgin.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		nextgin.WriteJSON(w, http.StatusOK, products)
	})

	api.GET("/products/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			nextgin.WriteError(w, http.StatusBadRequest, "invalid product id")
			return
		}
		p, ok, err := store.findProduct(id)
		if err != nil {
			nextgin.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			nextgin.WriteError(w, http.StatusNotFound, "product not found")
			return
		}
		nextgin.WriteJSON(w, http.StatusOK, p)
	})

	// Auth (SQLite users + sessions + rate limiting)
	authLimit := nextgin.RateLimit(5, 10)
	api.POST("/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := nextgin.ReadJSON(r, &body); err != nil {
			nextgin.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		email := strings.TrimSpace(strings.ToLower(body.Email))
		if email == "" || !strings.Contains(email, "@") || len(body.Password) < 4 {
			nextgin.WriteError(w, http.StatusBadRequest, "valid email and 4+ char password required")
			return
		}
		u, sid, err := store.loginOrRegister(email, body.Password)
		if err != nil {
			nextgin.WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if c, err := r.Cookie("cid"); err == nil && c.Value != "" {
			store.mergeCart("guest:"+c.Value, "user:"+u.Email)
		}
		nextgin.SetSessionCookie(w, "sid", sid, cookieOpts)
		nextgin.WriteJSON(w, http.StatusOK, u)
	}, authLimit)

	api.POST("/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if _, sid, ok := getSession(r); ok {
			store.deleteSession(sid)
		}
		nextgin.ClearSessionCookie(w, "sid", cookieOpts)
		w.WriteHeader(http.StatusNoContent)
	})

	api.GET("/auth/me", func(w http.ResponseWriter, r *http.Request) {
		u, _, ok := getSession(r)
		if !ok {
			nextgin.WriteJSON(w, http.StatusOK, map[string]any{"user": nil})
			return
		}
		nextgin.WriteJSON(w, http.StatusOK, map[string]any{"user": u})
	})

	// Cart (SQLite cart_items)
	api.GET("/cart", func(w http.ResponseWriter, r *http.Request) {
		key := getOrCreateCartKey(w, r)
		cart, err := store.getCart(key)
		if err != nil {
			nextgin.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		nextgin.WriteJSON(w, http.StatusOK, cart)
	})

	api.POST("/cart/items", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProductID int `json:"productId"`
			Qty       int `json:"qty"`
		}
		if err := nextgin.ReadJSON(r, &body); err != nil {
			nextgin.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		key := getOrCreateCartKey(w, r)
		cart, err := store.addToCart(key, body.ProductID, body.Qty)
		if err != nil {
			nextgin.WriteError(w, http.StatusNotFound, err.Error())
			return
		}
		nextgin.WriteJSON(w, http.StatusOK, cart)
	})

	api.PATCH("/cart/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		var body struct {
			Qty int `json:"qty"`
		}
		if err != nil || nextgin.ReadJSON(r, &body) != nil {
			nextgin.WriteError(w, http.StatusBadRequest, "bad request")
			return
		}
		key := getOrCreateCartKey(w, r)
		cart, err := store.updateCartQty(key, id, body.Qty)
		if err != nil {
			nextgin.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		nextgin.WriteJSON(w, http.StatusOK, cart)
	})

	api.DELETE("/cart/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			nextgin.WriteError(w, http.StatusBadRequest, "invalid id")
			return
		}
		key := getOrCreateCartKey(w, r)
		cart, err := store.updateCartQty(key, id, 0)
		if err != nil {
			nextgin.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		nextgin.WriteJSON(w, http.StatusOK, cart)
	})

	// Checkout & Orders (transactional SQLite)
	api.POST("/checkout", func(w http.ResponseWriter, r *http.Request) {
		u, _, ok := getSession(r)
		if !ok {
			nextgin.WriteError(w, http.StatusUnauthorized, "please log in to checkout")
			return
		}
		order, err := store.checkout("user:"+u.Email, u)
		if err != nil {
			nextgin.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		nextgin.WriteJSON(w, http.StatusCreated, order)
	})

	api.GET("/orders", func(w http.ResponseWriter, r *http.Request) {
		u, _, ok := getSession(r)
		if !ok {
			nextgin.WriteJSON(w, http.StatusOK, []*Order{})
			return
		}
		orders, err := store.listOrders(u.ID)
		if err != nil {
			nextgin.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		nextgin.WriteJSON(w, http.StatusOK, orders)
	})

	// Payment webhook: verifies HMAC-SHA256 over raw body bytes using nextgin.ReadRawBody.
	api.POST("/webhooks/pay", func(w http.ResponseWriter, r *http.Request) {
		raw, err := nextgin.ReadRawBody(w, r, 256<<10)
		if err != nil {
			nextgin.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		sig := r.Header.Get("X-Pay-Signature")
		mac := hmac.New(sha256.New, []byte(webhookSecret))
		mac.Write(raw)
		expected := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(sig), []byte(expected)) {
			nextgin.WriteError(w, http.StatusUnauthorized, "invalid webhook signature")
			return
		}
		var evt struct {
			OrderID string `json:"orderId"`
		}
		if err := json.Unmarshal(raw, &evt); err != nil || evt.OrderID == "" {
			nextgin.WriteError(w, http.StatusBadRequest, "missing orderId")
			return
		}
		order, ok, err := store.markOrderPaid(evt.OrderID)
		if err != nil {
			nextgin.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			nextgin.WriteError(w, http.StatusNotFound, "order not found")
			return
		}
		nextgin.WriteJSON(w, http.StatusOK, order)
	})

	// Demo helper: signs a webhook payload server-side and invokes /api/webhooks/pay.
	api.POST("/demo/simulate-payment/{id}", func(w http.ResponseWriter, r *http.Request) {
		orderID := r.PathValue("id")
		payload, _ := json.Marshal(map[string]string{"orderId": orderID})
		mac := hmac.New(sha256.New, []byte(webhookSecret))
		mac.Write(payload)
		sig := hex.EncodeToString(mac.Sum(nil))

		req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "/api/webhooks/pay", strings.NewReader(string(payload)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Pay-Signature", sig)
		app.Router.ServeHTTP(w, req)
	})

	app.MountFrontend()

	if err := app.Run(":" + port); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
