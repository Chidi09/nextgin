package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"example.com/ecommerce-app/internal/domain"
)

type SQLiteStore struct {
	db *sql.DB
}

func OpenSQLite(path string) (*SQLiteStore, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	s := &SQLiteStore{db: db}
	if err := s.migrateAndSeedProducts(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) migrateAndSeedProducts(ctx context.Context) error {
	const schema = `
	CREATE TABLE IF NOT EXISTS products (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		slug TEXT UNIQUE NOT NULL,
		name TEXT NOT NULL,
		category TEXT NOT NULL,
		price_cents INTEGER NOT NULL,
		stock INTEGER NOT NULL,
		badge TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT UNIQUE NOT NULL,
		name TEXT NOT NULL,
		password_hash TEXT NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS sessions (
		token TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS cart_items (
		cart_key TEXT NOT NULL,
		product_id INTEGER NOT NULL REFERENCES products(id) ON DELETE CASCADE,
		qty INTEGER NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (cart_key, product_id)
	);
	CREATE TABLE IF NOT EXISTS orders (
		id TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id),
		user_email TEXT NOT NULL,
		total_cents INTEGER NOT NULL,
		status TEXT NOT NULL,
		created_at TEXT NOT NULL,
		paid_at TEXT
	);
	CREATE TABLE IF NOT EXISTS order_items (
		order_id TEXT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
		product_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		price_cents INTEGER NOT NULL,
		qty INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS cron_runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		job_name TEXT NOT NULL,
		expired_carts INTEGER NOT NULL,
		ran_at TEXT NOT NULL
	);`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM products`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	seed := []domain.Product{
		{Slug: "mechanical-keyboard", Name: "Keychron Q1 Pro", Category: "Keyboards", PriceCents: 19900, Stock: 14, Badge: "Popular", Description: "CNC aluminum 75% wireless mechanical keyboard with hot-swappable switches."},
		{Slug: "studio-headphones", Name: "Sony MDR-M1 Studio", Category: "Audio", PriceCents: 24900, Stock: 9, Badge: "New", Description: "Closed-back reference studio monitor headphones tuned for long sessions."},
		{Slug: "usb-c-dock", Name: "CalDigit TS4 Dock", Category: "Accessories", PriceCents: 37900, Stock: 6, Description: "18-port Thunderbolt 4 dock with 98W host charging and 2.5GbE."},
		{Slug: "ergo-mouse", Name: "Logitech MX Master 3S", Category: "Mice", PriceCents: 9900, Stock: 22, Badge: "Best Value", Description: "8K DPI sensor, quiet clicks, and MagSpeed electromagnetic scrolling."},
		{Slug: "desk-lamp", Name: "BenQ ScreenBar Halo", Category: "Lighting", PriceCents: 16900, Stock: 11, Description: "Asymmetric monitor light bar with wireless puck controller and backlight."},
		{Slug: "nvme-enclosure", Name: "OWC Express 1M2", Category: "Storage", PriceCents: 11900, Stock: 18, Description: "USB4 fanless aluminum NVMe SSD enclosure sustaining 3151 MB/s."},
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO products (slug, name, category, price_cents, stock, badge, description) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, p := range seed {
		if _, err := stmt.ExecContext(ctx, p.Slug, p.Name, p.Category, p.PriceCents, p.Stock, p.Badge, p.Description); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) EnsureUser(ctx context.Context, email, name, passwordHash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO users (email, name, password_hash, created_at) VALUES (?, ?, ?, ?)`,
		email, name, passwordHash, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (s *SQLiteStore) ListProducts(ctx context.Context) ([]domain.Product, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, slug, name, category, price_cents, stock, badge, description FROM products ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.Product, 0)
	for rows.Next() {
		var p domain.Product
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Category, &p.PriceCents, &p.Stock, &p.Badge, &p.Description); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) FindProduct(ctx context.Context, id int) (domain.Product, error) {
	var p domain.Product
	err := s.db.QueryRowContext(ctx,
		`SELECT id, slug, name, category, price_cents, stock, badge, description FROM products WHERE id = ?`, id,
	).Scan(&p.ID, &p.Slug, &p.Name, &p.Category, &p.PriceCents, &p.Stock, &p.Badge, &p.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Product{}, domain.ErrNotFound
	}
	return p, err
}

func (s *SQLiteStore) FindUserByEmail(ctx context.Context, email string) (domain.User, string, error) {
	var u domain.User
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, email, name, password_hash FROM users WHERE email = ?`, email,
	).Scan(&u.ID, &u.Email, &u.Name, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, "", domain.ErrNotFound
	}
	return u, hash, err
}

func (s *SQLiteStore) CreateUser(ctx context.Context, email, name, passwordHash string) (domain.User, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (email, name, password_hash, created_at) VALUES (?, ?, ?, ?)`,
		email, name, passwordHash, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return domain.User{}, err
	}
	id, _ := res.LastInsertId()
	return domain.User{ID: id, Email: email, Name: name}, nil
}

func (s *SQLiteStore) CreateSession(ctx context.Context, token string, userID int64, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		token, userID, expiresAt.UTC().Format(time.RFC3339),
	)
	return err
}

func (s *SQLiteStore) ResolveSession(ctx context.Context, token string) (domain.User, error) {
	var u domain.User
	var expStr string
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.email, u.name, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token = ?`, token,
	).Scan(&u.ID, &u.Email, &u.Name, &expStr)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, domain.ErrUnauthorized
	}
	if err != nil {
		return domain.User{}, err
	}
	if exp, err := time.Parse(time.RFC3339, expStr); err == nil && time.Now().UTC().After(exp) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
		return domain.User{}, domain.ErrUnauthorized
	}
	return u, nil
}

func (s *SQLiteStore) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
	return err
}

func (s *SQLiteStore) MergeCart(ctx context.Context, fromKey, toKey string) error {
	if fromKey == "" || fromKey == toKey {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO cart_items (cart_key, product_id, qty, updated_at)
		SELECT ?, product_id, qty, ? FROM cart_items WHERE cart_key = ?
		ON CONFLICT(cart_key, product_id) DO UPDATE SET qty = cart_items.qty + excluded.qty, updated_at = excluded.updated_at`,
		toKey, now, fromKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cart_items WHERE cart_key = ?`, fromKey); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) GetCart(ctx context.Context, cartKey string) (domain.Cart, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ci.product_id, p.name, p.price_cents, ci.qty
		FROM cart_items ci JOIN products p ON p.id = ci.product_id
		WHERE ci.cart_key = ? ORDER BY ci.product_id`, cartKey)
	if err != nil {
		return domain.Cart{}, err
	}
	defer rows.Close()

	cart := domain.Cart{Items: []domain.CartItem{}}
	for rows.Next() {
		var it domain.CartItem
		if err := rows.Scan(&it.ProductID, &it.Name, &it.PriceCents, &it.Qty); err != nil {
			return domain.Cart{}, err
		}
		cart.TotalCents += it.PriceCents * it.Qty
		cart.Items = append(cart.Items, it)
	}
	return cart, rows.Err()
}

func (s *SQLiteStore) AddCartItem(ctx context.Context, cartKey string, productID, qty int) (domain.Cart, error) {
	if _, err := s.FindProduct(ctx, productID); err != nil {
		return domain.Cart{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO cart_items (cart_key, product_id, qty, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(cart_key, product_id) DO UPDATE SET qty = cart_items.qty + excluded.qty, updated_at = excluded.updated_at`,
		cartKey, productID, qty, now); err != nil {
		return domain.Cart{}, err
	}
	return s.GetCart(ctx, cartKey)
}

func (s *SQLiteStore) SetCartItemQty(ctx context.Context, cartKey string, productID, qty int) (domain.Cart, error) {
	if qty <= 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM cart_items WHERE cart_key = ? AND product_id = ?`, cartKey, productID); err != nil {
			return domain.Cart{}, err
		}
	} else {
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := s.db.ExecContext(ctx, `UPDATE cart_items SET qty = ?, updated_at = ? WHERE cart_key = ? AND product_id = ?`, qty, now, cartKey, productID); err != nil {
			return domain.Cart{}, err
		}
	}
	return s.GetCart(ctx, cartKey)
}

func (s *SQLiteStore) CheckoutTx(ctx context.Context, orderID, cartKey string, u domain.User) (*domain.Order, error) {
	cart, err := s.GetCart(ctx, cartKey)
	if err != nil {
		return nil, err
	}
	if len(cart.Items) == 0 {
		return nil, domain.ErrEmptyCart
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO orders (id, user_id, user_email, total_cents, status, created_at) VALUES (?, ?, ?, ?, 'pending', ?)`,
		orderID, u.ID, u.Email, cart.TotalCents, nowStr,
	); err != nil {
		return nil, err
	}

	for _, it := range cart.Items {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO order_items (order_id, product_id, name, price_cents, qty) VALUES (?, ?, ?, ?, ?)`,
			orderID, it.ProductID, it.Name, it.PriceCents, it.Qty,
		); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE products SET stock = MAX(0, stock - ?) WHERE id = ?`, it.Qty, it.ProductID); err != nil {
			return nil, err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM cart_items WHERE cart_key = ?`, cartKey); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &domain.Order{
		ID:         orderID,
		UserEmail:  u.Email,
		Items:      cart.Items,
		TotalCents: cart.TotalCents,
		Status:     "pending",
		CreatedAt:  now,
	}, nil
}

func (s *SQLiteStore) MarkOrderPaid(ctx context.Context, orderID string) (*domain.Order, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`UPDATE orders SET status = 'paid', paid_at = COALESCE(paid_at, ?) WHERE id = ?`,
		now.Format(time.RFC3339), orderID,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, domain.ErrNotFound
	}

	var o domain.Order
	var createdStr string
	var paidStr sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT id, user_email, total_cents, status, created_at, paid_at FROM orders WHERE id = ?`, orderID,
	).Scan(&o.ID, &o.UserEmail, &o.TotalCents, &o.Status, &createdStr, &paidStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	o.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	if paidStr.Valid {
		if t, err := time.Parse(time.RFC3339, paidStr.String); err == nil {
			o.PaidAt = &t
		}
	}
	return &o, nil
}

func (s *SQLiteStore) ListOrders(ctx context.Context, userID int64) ([]*domain.Order, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_email, total_cents, status, created_at, paid_at FROM orders WHERE user_id = ? ORDER BY created_at DESC`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*domain.Order, 0)
	for rows.Next() {
		var o domain.Order
		var createdStr string
		var paidStr sql.NullString
		if err := rows.Scan(&o.ID, &o.UserEmail, &o.TotalCents, &o.Status, &createdStr, &paidStr); err != nil {
			return nil, err
		}
		o.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		if paidStr.Valid {
			if t, err := time.Parse(time.RFC3339, paidStr.String); err == nil {
				o.PaidAt = &t
			}
		}
		itemRows, err := s.db.QueryContext(ctx, `SELECT product_id, name, price_cents, qty FROM order_items WHERE order_id = ?`, o.ID)
		if err == nil {
			for itemRows.Next() {
				var it domain.CartItem
				if err := itemRows.Scan(&it.ProductID, &it.Name, &it.PriceCents, &it.Qty); err == nil {
					o.Items = append(o.Items, it)
				}
			}
			itemRows.Close()
		}
		out = append(out, &o)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ExpireOldCarts(ctx context.Context, ttl time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-ttl).Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `DELETE FROM cart_items WHERE updated_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	expired, _ := res.RowsAffected()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO cron_runs (job_name, expired_carts, ran_at) VALUES ('expire-carts', ?, ?)`,
		expired, time.Now().UTC().Format(time.RFC3339),
	)
	return expired, err
}

func (s *SQLiteStore) CronStats(ctx context.Context) (domain.CronStats, error) {
	var stats domain.CronStats
	var lastStr sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(expired_carts), 0), MAX(ran_at) FROM cron_runs WHERE job_name = 'expire-carts'`,
	).Scan(&stats.Ticks, &stats.ExpiredCarts, &lastStr)
	if err != nil {
		return domain.CronStats{}, err
	}
	if lastStr.Valid {
		if t, err := time.Parse(time.RFC3339, lastStr.String); err == nil {
			stats.LastRunAt = &t
		}
	}
	return stats, nil
}
