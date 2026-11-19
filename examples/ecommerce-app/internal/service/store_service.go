package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"example.com/ecommerce-app/internal/domain"
)

// Repository defines the persistence operations required by StoreService.
type Repository interface {
	EnsureUser(ctx context.Context, email, name, passwordHash string) error
	ListProducts(ctx context.Context) ([]domain.Product, error)
	FindProduct(ctx context.Context, id int) (domain.Product, error)
	FindUserByEmail(ctx context.Context, email string) (domain.User, string, error)
	CreateUser(ctx context.Context, email, name, passwordHash string) (domain.User, error)
	CreateSession(ctx context.Context, token string, userID int64, expiresAt time.Time) error
	ResolveSession(ctx context.Context, token string) (domain.User, error)
	DeleteSession(ctx context.Context, token string) error
	MergeCart(ctx context.Context, fromKey, toKey string) error
	GetCart(ctx context.Context, cartKey string) (domain.Cart, error)
	AddCartItem(ctx context.Context, cartKey string, productID, qty int) (domain.Cart, error)
	SetCartItemQty(ctx context.Context, cartKey string, productID, qty int) (domain.Cart, error)
	CheckoutTx(ctx context.Context, orderID, cartKey string, u domain.User) (*domain.Order, error)
	MarkOrderPaid(ctx context.Context, orderID string) (*domain.Order, error)
	ListOrders(ctx context.Context, userID int64) ([]*domain.Order, error)
	ExpireOldCarts(ctx context.Context, ttl time.Duration) (int64, error)
	CronStats(ctx context.Context) (domain.CronStats, error)
}

type StoreService struct {
	repo          Repository
	webhookSecret []byte
	sessionTTL    time.Duration
}

func NewStoreService(repo Repository, webhookSecret string) *StoreService {
	return &StoreService{
		repo:          repo,
		webhookSecret: []byte(webhookSecret),
		sessionTTL:    7 * 24 * time.Hour,
	}
}

func (s *StoreService) EnsureDemoUser(ctx context.Context) error {
	return s.repo.EnsureUser(ctx, "demo@nextgin.dev", "demo", hashPassword("demo1234"))
}

func (s *StoreService) ListProducts(ctx context.Context) ([]domain.Product, error) {
	return s.repo.ListProducts(ctx)
}

func (s *StoreService) GetProduct(ctx context.Context, id int) (domain.Product, error) {
	return s.repo.FindProduct(ctx, id)
}

func (s *StoreService) LoginOrRegister(ctx context.Context, email, password, guestCartID string) (domain.User, string, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	u, hash, err := s.repo.FindUserByEmail(ctx, email)
	if errors.Is(err, domain.ErrNotFound) {
		name, _, _ := strings.Cut(email, "@")
		u, err = s.repo.CreateUser(ctx, email, name, hashPassword(password))
		if err != nil {
			return domain.User{}, "", err
		}
	} else if err != nil {
		return domain.User{}, "", err
	} else if !verifyPassword(password, hash) {
		return domain.User{}, "", domain.ErrInvalidPassword
	}

	token := RandomToken(24)
	if err := s.repo.CreateSession(ctx, token, u.ID, time.Now().UTC().Add(s.sessionTTL)); err != nil {
		return domain.User{}, "", err
	}
	if guestCartID != "" {
		_ = s.repo.MergeCart(ctx, "guest:"+guestCartID, "user:"+u.Email)
	}
	return u, token, nil
}

func (s *StoreService) ResolveSession(ctx context.Context, token string) (domain.User, error) {
	return s.repo.ResolveSession(ctx, token)
}

func (s *StoreService) Logout(ctx context.Context, token string) error {
	return s.repo.DeleteSession(ctx, token)
}

func (s *StoreService) GetCart(ctx context.Context, cartKey string) (domain.Cart, error) {
	return s.repo.GetCart(ctx, cartKey)
}

func (s *StoreService) AddToCart(ctx context.Context, cartKey string, productID, qty int) (domain.Cart, error) {
	if qty <= 0 {
		qty = 1
	}
	return s.repo.AddCartItem(ctx, cartKey, productID, qty)
}

func (s *StoreService) UpdateCartQty(ctx context.Context, cartKey string, productID, qty int) (domain.Cart, error) {
	return s.repo.SetCartItemQty(ctx, cartKey, productID, qty)
}

func (s *StoreService) Checkout(ctx context.Context, u domain.User) (*domain.Order, error) {
	orderID := "ORD-" + strings.ToUpper(RandomToken(4))
	return s.repo.CheckoutTx(ctx, orderID, "user:"+u.Email, u)
}

func (s *StoreService) ListOrders(ctx context.Context, userID int64) ([]*domain.Order, error) {
	return s.repo.ListOrders(ctx, userID)
}

func (s *StoreService) ProcessPaymentWebhook(ctx context.Context, rawBody []byte, signature string) (*domain.Order, error) {
	expected := s.sign(rawBody)
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return nil, domain.ErrInvalidSignature
	}
	var evt struct {
		OrderID string `json:"orderId"`
	}
	if err := json.Unmarshal(rawBody, &evt); err != nil || strings.TrimSpace(evt.OrderID) == "" {
		return nil, domain.ValidationError{Fields: map[string]string{"orderId": "required"}}
	}
	return s.repo.MarkOrderPaid(ctx, evt.OrderID)
}

func (s *StoreService) SignPaymentEvent(orderID string) ([]byte, string) {
	payload, _ := json.Marshal(map[string]string{"orderId": orderID})
	return payload, s.sign(payload)
}

func (s *StoreService) ExpireAbandonedCarts(ctx context.Context, ttl time.Duration) (int64, error) {
	return s.repo.ExpireOldCarts(ctx, ttl)
}

func (s *StoreService) CronStats(ctx context.Context) (domain.CronStats, error) {
	return s.repo.CronStats(ctx)
}

func (s *StoreService) sign(payload []byte) string {
	mac := hmac.New(sha256.New, s.webhookSecret)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func RandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func hashPassword(pw string) string {
	salt := RandomToken(16)
	sum := sha256.Sum256([]byte(salt + ":" + pw))
	return salt + "$" + hex.EncodeToString(sum[:])
}

func verifyPassword(pw, stored string) bool {
	salt, hashHex, ok := strings.Cut(stored, "$")
	if !ok {
		return false
	}
	sum := sha256.Sum256([]byte(salt + ":" + pw))
	got := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(hashHex)) == 1
}
