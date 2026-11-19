package httpapi

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Chidi09/nextgin"

	"example.com/ecommerce-app/internal/domain"
	"example.com/ecommerce-app/internal/service"
)

type Handlers struct {
	svc        *service.StoreService
	cookieOpts nextgin.CookieOptions
	router     *nextgin.Router
}

func NewHandlers(svc *service.StoreService, cookieOpts nextgin.CookieOptions, router *nextgin.Router) *Handlers {
	return &Handlers{
		svc:        svc,
		cookieOpts: cookieOpts,
		router:     router,
	}
}

func (h *Handlers) Register(api *nextgin.Router) {
	api.GET("/health", handle(h.Health))
	api.GET("/cron/stats", handle(h.CronStats))

	// Catalog
	api.GET("/products", handle(h.ListProducts))
	api.GET("/products/{id}", handle(h.GetProduct))

	// Auth (rate-limited login)
	authLimit := nextgin.RateLimit(5, 10)
	api.POST("/auth/login", handle(h.Login), authLimit)

	withOptionalAuth := api.Group("", OptionalAuth(h.svc))
	withOptionalAuth.POST("/auth/logout", handle(h.Logout))
	withOptionalAuth.GET("/auth/me", handle(h.Me))
	withOptionalAuth.GET("/orders", handle(h.ListOrders))

	// Cart (guest or signed-in)
	withOptionalAuth.GET("/cart", handle(h.GetCart))
	withOptionalAuth.POST("/cart/items", handle(h.AddCartItem))
	withOptionalAuth.PATCH("/cart/items/{id}", handle(h.UpdateCartItem))
	withOptionalAuth.DELETE("/cart/items/{id}", handle(h.DeleteCartItem))

	// Authenticated mutations
	authed := api.Group("", RequireAuth(h.svc))
	authed.POST("/checkout", handle(h.Checkout))

	// Payment webhook (raw body HMAC verification) + demo trigger
	api.POST("/webhooks/pay", handle(h.PaymentWebhook))
	api.POST("/demo/simulate-payment/{id}", h.SimulatePayment)
}

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) error {
	nextgin.WriteJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"engine": "pure-go+sqlite",
	})
	return nil
}

func (h *Handlers) CronStats(w http.ResponseWriter, r *http.Request) error {
	stats, err := h.svc.CronStats(r.Context())
	if err != nil {
		return err
	}
	nextgin.WriteJSON(w, http.StatusOK, stats)
	return nil
}

func (h *Handlers) ListProducts(w http.ResponseWriter, r *http.Request) error {
	products, err := h.svc.ListProducts(r.Context())
	if err != nil {
		return err
	}
	nextgin.WriteJSON(w, http.StatusOK, products)
	return nil
}

func (h *Handlers) GetProduct(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		return domain.ValidationError{Fields: map[string]string{"id": "must be an integer"}}
	}
	p, err := h.svc.GetProduct(r.Context(), id)
	if err != nil {
		return err
	}
	nextgin.WriteJSON(w, http.StatusOK, p)
	return nil
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (req loginRequest) Validate() error {
	fields := map[string]string{}
	email := strings.TrimSpace(strings.ToLower(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		fields["email"] = "valid email required"
	}
	if len(req.Password) < 4 {
		fields["password"] = "must be at least 4 characters"
	}
	if len(fields) > 0 {
		return domain.ValidationError{Fields: fields}
	}
	return nil
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) error {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}

	var guestID string
	if c, err := r.Cookie("cid"); err == nil {
		guestID = c.Value
	}
	u, sid, err := h.svc.LoginOrRegister(r.Context(), req.Email, req.Password, guestID)
	if err != nil {
		return err
	}
	nextgin.SetSessionCookie(w, "sid", sid, h.cookieOpts)
	nextgin.WriteJSON(w, http.StatusOK, u)
	return nil
}

func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) error {
	if sid := SessionTokenFromContext(r.Context()); sid != "" {
		_ = h.svc.Logout(r.Context(), sid)
	}
	nextgin.ClearSessionCookie(w, "sid", h.cookieOpts)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) error {
	u, ok := UserFromContext(r.Context())
	if !ok {
		nextgin.WriteJSON(w, http.StatusOK, map[string]any{"user": nil})
		return nil
	}
	nextgin.WriteJSON(w, http.StatusOK, map[string]any{"user": u})
	return nil
}

func (h *Handlers) GetCart(w http.ResponseWriter, r *http.Request) error {
	key := h.getOrCreateCartKey(w, r)
	cart, err := h.svc.GetCart(r.Context(), key)
	if err != nil {
		return err
	}
	nextgin.WriteJSON(w, http.StatusOK, cart)
	return nil
}

type addCartItemRequest struct {
	ProductID int `json:"productId"`
	Qty       int `json:"qty"`
}

func (req addCartItemRequest) Validate() error {
	if req.ProductID <= 0 {
		return domain.ValidationError{Fields: map[string]string{"productId": "must be positive"}}
	}
	return nil
}

func (h *Handlers) AddCartItem(w http.ResponseWriter, r *http.Request) error {
	var req addCartItemRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return err
	}
	key := h.getOrCreateCartKey(w, r)
	cart, err := h.svc.AddToCart(r.Context(), key, req.ProductID, req.Qty)
	if err != nil {
		return err
	}
	nextgin.WriteJSON(w, http.StatusOK, cart)
	return nil
}

type updateCartItemRequest struct {
	Qty int `json:"qty"`
}

func (h *Handlers) UpdateCartItem(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		return domain.ValidationError{Fields: map[string]string{"id": "must be an integer"}}
	}
	var req updateCartItemRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	key := h.getOrCreateCartKey(w, r)
	cart, err := h.svc.UpdateCartQty(r.Context(), key, id, req.Qty)
	if err != nil {
		return err
	}
	nextgin.WriteJSON(w, http.StatusOK, cart)
	return nil
}

func (h *Handlers) DeleteCartItem(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		return domain.ValidationError{Fields: map[string]string{"id": "must be an integer"}}
	}
	key := h.getOrCreateCartKey(w, r)
	cart, err := h.svc.UpdateCartQty(r.Context(), key, id, 0)
	if err != nil {
		return err
	}
	nextgin.WriteJSON(w, http.StatusOK, cart)
	return nil
}

func (h *Handlers) Checkout(w http.ResponseWriter, r *http.Request) error {
	u, _ := UserFromContext(r.Context())
	order, err := h.svc.Checkout(r.Context(), u)
	if err != nil {
		return err
	}
	nextgin.WriteJSON(w, http.StatusCreated, order)
	return nil
}

func (h *Handlers) ListOrders(w http.ResponseWriter, r *http.Request) error {
	u, ok := UserFromContext(r.Context())
	if !ok {
		nextgin.WriteJSON(w, http.StatusOK, []*domain.Order{})
		return nil
	}
	orders, err := h.svc.ListOrders(r.Context(), u.ID)
	if err != nil {
		return err
	}
	nextgin.WriteJSON(w, http.StatusOK, orders)
	return nil
}

func (h *Handlers) PaymentWebhook(w http.ResponseWriter, r *http.Request) error {
	raw, err := nextgin.ReadRawBody(w, r, 256<<10)
	if err != nil {
		if errors.Is(err, nextgin.ErrBodyTooLarge) {
			return err
		}
		return domain.ValidationError{Fields: map[string]string{"body": "invalid request body"}}
	}
	order, err := h.svc.ProcessPaymentWebhook(r.Context(), raw, r.Header.Get("X-Pay-Signature"))
	if err != nil {
		return err
	}
	nextgin.WriteJSON(w, http.StatusOK, order)
	return nil
}

func (h *Handlers) SimulatePayment(w http.ResponseWriter, r *http.Request) {
	payload, sig := h.svc.SignPaymentEvent(r.PathValue("id"))
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "/api/webhooks/pay", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pay-Signature", sig)
	h.router.ServeHTTP(w, req)
}

func (h *Handlers) getOrCreateCartKey(w http.ResponseWriter, r *http.Request) string {
	if u, ok := UserFromContext(r.Context()); ok {
		return "user:" + u.Email
	}
	if c, err := r.Cookie("cid"); err == nil && c.Value != "" {
		return "guest:" + c.Value
	}
	cid := service.RandomToken(16)
	nextgin.SetSessionCookie(w, "cid", cid, h.cookieOpts)
	return "guest:" + cid
}

func decodeJSON(r *http.Request, dst any) error {
	if err := nextgin.ReadJSON(r, dst); err != nil {
		if errors.Is(err, nextgin.ErrBodyTooLarge) {
			return err
		}
		return domain.ValidationError{Fields: map[string]string{"body": "invalid JSON"}}
	}
	return nil
}
