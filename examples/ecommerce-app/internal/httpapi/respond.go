package httpapi

import (
	"errors"
	"log"
	"net/http"

	"github.com/Chidi09/nextgin"

	"example.com/ecommerce-app/internal/domain"
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
	case errors.Is(err, domain.ErrUnauthorized):
		nextgin.WriteError(w, http.StatusUnauthorized, "please log in to continue")
	case errors.Is(err, domain.ErrInvalidPassword):
		nextgin.WriteError(w, http.StatusUnauthorized, "invalid password")
	case errors.Is(err, domain.ErrInvalidSignature):
		nextgin.WriteError(w, http.StatusUnauthorized, "invalid webhook signature")
	case errors.Is(err, domain.ErrEmptyCart):
		nextgin.WriteError(w, http.StatusBadRequest, "cart is empty")
	default:
		log.Printf("http error %s %s: %v", r.Method, r.URL.Path, err)
		nextgin.WriteError(w, http.StatusInternalServerError, "internal server error")
	}
}
