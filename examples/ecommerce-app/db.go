package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func openStore(path string) (*Store, error) {
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
	s := &Store{db: db}
	if err := s.migrateAndSeed(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrateAndSeed() error {
	schema := `
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
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM products`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		seed := []Product{
			{Slug: "mechanical-keyboard", Name: "Keychron Q1 Pro", Category: "Keyboards", PriceCents: 19900, Stock: 14, Badge: "Popular", Description: "CNC aluminum 75% wireless mechanical keyboard with hot-swappable switches."},
			{Slug: "studio-headphones", Name: "Sony MDR-M1 Studio", Category: "Audio", PriceCents: 24900, Stock: 9, Badge: "New", Description: "Closed-back reference studio monitor headphones tuned for long sessions."},
			{Slug: "usb-c-dock", Name: "CalDigit TS4 Dock", Category: "Accessories", PriceCents: 37900, Stock: 6, Description: "18-port Thunderbolt 4 dock with 98W host charging and 2.5GbE."},
			{Slug: "ergo-mouse", Name: "Logitech MX Master 3S", Category: "Mice", PriceCents: 9900, Stock: 22, Badge: "Best Value", Description: "8K DPI sensor, quiet clicks, and MagSpeed electromagnetic scrolling."},
			{Slug: "desk-lamp", Name: "BenQ ScreenBar Halo", Category: "Lighting", PriceCents: 16900, Stock: 11, Description: "Asymmetric monitor light bar with wireless puck controller and backlight."},
			{Slug: "nvme-enclosure", Name: "OWC Express 1M2", Category: "Storage", PriceCents: 11900, Stock: 18, Description: "USB4 fanless aluminum NVMe SSD enclosure sustaining 3151 MB/s."},
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		stmt, err := tx.Prepare(`INSERT INTO products (slug, name, category, price_cents, stock, badge, description) VALUES (?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		for _, p := range seed {
			if _, err := stmt.Exec(p.Slug, p.Name, p.Category, p.PriceCents, p.Stock, p.Badge, p.Description); err != nil {
				stmt.Close()
				_ = tx.Rollback()
				return err
			}
		}
		stmt.Close()
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	// Seed default demo user if missing.
	var userCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = ?`, "demo@nextgin.dev").Scan(&userCount); err != nil {
		return err
	}
	if userCount == 0 {
		hash := hashPassword("demo1234")
		_, err := s.db.Exec(
			`INSERT INTO users (email, name, password_hash, created_at) VALUES (?, ?, ?, ?)`,
			"demo@nextgin.dev", "demo", hash, time.Now().UTC().Format(time.RFC3339),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func hashPassword(pw string) string {
	salt := randomToken(16)
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

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) listProducts() ([]Product, error) {
	rows, err := s.db.Query(`SELECT id, slug, name, category, price_cents, stock, badge, description FROM products ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Product, 0)
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Category, &p.PriceCents, &p.Stock, &p.Badge, &p.Description); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) findProduct(id int) (Product, bool, error) {
	var p Product
	err := s.db.QueryRow(`SELECT id, slug, name, category, price_cents, stock, badge, description FROM products WHERE id = ?`, id).
		Scan(&p.ID, &p.Slug, &p.Name, &p.Category, &p.PriceCents, &p.Stock, &p.Badge, &p.Description)
	if errors.Is(err, sql.ErrNoRows) {
		return Product{}, false, nil
	}
	return p, err == nil, err
}

func (s *Store) loginOrRegister(email, password string) (User, string, error) {
	var u User
	var hash string
	err := s.db.QueryRow(`SELECT id, email, name, password_hash FROM users WHERE email = ?`, email).
		Scan(&u.ID, &u.Email, &u.Name, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		name, _, _ := strings.Cut(email, "@")
		hash = hashPassword(password)
		res, err := s.db.Exec(
			`INSERT INTO users (email, name, password_hash, created_at) VALUES (?, ?, ?, ?)`,
			email, name, hash, time.Now().UTC().Format(time.RFC3339),
		)
		if err != nil {
			return User{}, "", err
		}
		id, _ := res.LastInsertId()
		u = User{ID: id, Email: email, Name: name}
	} else if err != nil {
		return User{}, "", err
	} else if !verifyPassword(password, hash) {
		return User{}, "", errors.New("invalid password")
	}

	token := randomToken(24)
	exp := time.Now().UTC().Add(7 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := s.db.Exec(`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`, token, u.ID, exp); err != nil {
		return User{}, "", err
	}
	return u, token, nil
}

func (s *Store) getSession(token string) (User, bool) {
	var u User
	var expStr string
	err := s.db.QueryRow(`
		SELECT u.id, u.email, u.name, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token = ?`, token).Scan(&u.ID, &u.Email, &u.Name, &expStr)
	if err != nil {
		return User{}, false
	}
	if exp, err := time.Parse(time.RFC3339, expStr); err == nil && time.Now().UTC().After(exp) {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
		return User{}, false
	}
	return u, true
}

func (s *Store) deleteSession(token string) {
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
}

func (s *Store) mergeCart(fromKey, toKey string) {
	if fromKey == "" || fromKey == toKey {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = s.db.Exec(`
		INSERT INTO cart_items (cart_key, product_id, qty, updated_at)
		SELECT ?, product_id, qty, ? FROM cart_items WHERE cart_key = ?
		ON CONFLICT(cart_key, product_id) DO UPDATE SET qty = cart_items.qty + excluded.qty, updated_at = excluded.updated_at`,
		toKey, now, fromKey)
	_, _ = s.db.Exec(`DELETE FROM cart_items WHERE cart_key = ?`, fromKey)
}

func (s *Store) getCart(cartKey string) (Cart, error) {
	rows, err := s.db.Query(`
		SELECT ci.product_id, p.name, p.price_cents, ci.qty
		FROM cart_items ci JOIN products p ON p.id = ci.product_id
		WHERE ci.cart_key = ? ORDER BY ci.product_id`, cartKey)
	if err != nil {
		return Cart{}, err
	}
	defer rows.Close()
	cart := Cart{Items: []CartItem{}}
	for rows.Next() {
		var it CartItem
		if err := rows.Scan(&it.ProductID, &it.Name, &it.PriceCents, &it.Qty); err != nil {
			return Cart{}, err
		}
		cart.TotalCents += it.PriceCents * it.Qty
		cart.Items = append(cart.Items, it)
	}
	return cart, rows.Err()
}

func (s *Store) addToCart(cartKey string, productID, qty int) (Cart, error) {
	if qty <= 0 {
		qty = 1
	}
	if _, ok, err := s.findProduct(productID); err != nil {
		return Cart{}, err
	} else if !ok {
		return Cart{}, errors.New("product not found")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
		INSERT INTO cart_items (cart_key, product_id, qty, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(cart_key, product_id) DO UPDATE SET qty = cart_items.qty + excluded.qty, updated_at = excluded.updated_at`,
		cartKey, productID, qty, now)
	if err != nil {
		return Cart{}, err
	}
	return s.getCart(cartKey)
}

func (s *Store) updateCartQty(cartKey string, productID, qty int) (Cart, error) {
	if qty <= 0 {
		if _, err := s.db.Exec(`DELETE FROM cart_items WHERE cart_key = ? AND product_id = ?`, cartKey, productID); err != nil {
			return Cart{}, err
		}
	} else {
		now := time.Now().UTC().Format(time.RFC3339)
		if _, err := s.db.Exec(`UPDATE cart_items SET qty = ?, updated_at = ? WHERE cart_key = ? AND product_id = ?`, qty, now, cartKey, productID); err != nil {
			return Cart{}, err
		}
	}
	return s.getCart(cartKey)
}

func (s *Store) checkout(cartKey string, u User) (*Order, error) {
	cart, err := s.getCart(cartKey)
	if err != nil {
		return nil, err
	}
	if len(cart.Items) == 0 {
		return nil, errors.New("cart is empty")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	orderID := "ORD-" + strings.ToUpper(randomToken(4))
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	if _, err := tx.Exec(
		`INSERT INTO orders (id, user_id, user_email, total_cents, status, created_at) VALUES (?, ?, ?, ?, 'pending', ?)`,
		orderID, u.ID, u.Email, cart.TotalCents, nowStr,
	); err != nil {
		return nil, err
	}

	for _, it := range cart.Items {
		if _, err := tx.Exec(
			`INSERT INTO order_items (order_id, product_id, name, price_cents, qty) VALUES (?, ?, ?, ?, ?)`,
			orderID, it.ProductID, it.Name, it.PriceCents, it.Qty,
		); err != nil {
			return nil, err
		}
		_, _ = tx.Exec(`UPDATE products SET stock = MAX(0, stock - ?) WHERE id = ?`, it.Qty, it.ProductID)
	}

	if _, err := tx.Exec(`DELETE FROM cart_items WHERE cart_key = ?`, cartKey); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Order{
		ID:         orderID,
		UserEmail:  u.Email,
		Items:      cart.Items,
		TotalCents: cart.TotalCents,
		Status:     "pending",
		CreatedAt:  now,
	}, nil
}

func (s *Store) markOrderPaid(orderID string) (*Order, bool, error) {
	now := time.Now().UTC()
	res, err := s.db.Exec(`UPDATE orders SET status = 'paid', paid_at = COALESCE(paid_at, ?) WHERE id = ?`, now.Format(time.RFC3339), orderID)
	if err != nil {
		return nil, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, false, nil
	}
	var o Order
	var createdStr string
	var paidStr sql.NullString
	if err := s.db.QueryRow(`SELECT id, user_email, total_cents, status, created_at, paid_at FROM orders WHERE id = ?`, orderID).
		Scan(&o.ID, &o.UserEmail, &o.TotalCents, &o.Status, &createdStr, &paidStr); err != nil {
		return nil, false, err
	}
	o.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	if paidStr.Valid {
		if t, err := time.Parse(time.RFC3339, paidStr.String); err == nil {
			o.PaidAt = &t
		}
	}
	return &o, true, nil
}

func (s *Store) listOrders(userID int64) ([]*Order, error) {
	rows, err := s.db.Query(`SELECT id, user_email, total_cents, status, created_at, paid_at FROM orders WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Order, 0)
	for rows.Next() {
		var o Order
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
		itemRows, err := s.db.Query(`SELECT product_id, name, price_cents, qty FROM order_items WHERE order_id = ?`, o.ID)
		if err == nil {
			for itemRows.Next() {
				var it CartItem
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

func (s *Store) expireOldCarts(ttl time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-ttl).Format(time.RFC3339)
	res, err := s.db.Exec(`DELETE FROM cart_items WHERE updated_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	expired, _ := res.RowsAffected()
	_, err = s.db.Exec(`INSERT INTO cron_runs (job_name, expired_carts, ran_at) VALUES ('expire-carts', ?, ?)`, expired, time.Now().UTC().Format(time.RFC3339))
	return expired, err
}

func (s *Store) cronStats() (CronStats, error) {
	var stats CronStats
	var lastStr sql.NullString
	err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(expired_carts), 0), MAX(ran_at) FROM cron_runs WHERE job_name = 'expire-carts'`).
		Scan(&stats.Ticks, &stats.ExpiredCarts, &lastStr)
	if err != nil {
		return CronStats{}, err
	}
	if lastStr.Valid {
		if t, err := time.Parse(time.RFC3339, lastStr.String); err == nil {
			stats.LastRunAt = &t
		}
	}
	return stats, nil
}
