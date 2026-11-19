package domain

import "time"

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
