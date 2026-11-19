package domain

import "errors"

var (
	ErrNotFound         = errors.New("not found")
	ErrUnauthorized     = errors.New("unauthorized")
	ErrInvalidPassword  = errors.New("invalid password")
	ErrEmptyCart        = errors.New("cart is empty")
	ErrInvalidSignature = errors.New("invalid webhook signature")
)

type ValidationError struct {
	Fields map[string]string
}

func (e ValidationError) Error() string { return "validation failed" }
