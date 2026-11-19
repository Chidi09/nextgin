package httpapi

import (
	"context"
	"net/http"

	"github.com/Chidi09/nextgin"

	"example.com/ecommerce-app/internal/domain"
)

type (
	userCtxKey         struct{}
	sessionTokenCtxKey struct{}
)

type SessionResolver interface {
	ResolveSession(ctx context.Context, token string) (domain.User, error)
}

func UserFromContext(ctx context.Context) (domain.User, bool) {
	u, ok := ctx.Value(userCtxKey{}).(domain.User)
	return u, ok
}

func SessionTokenFromContext(ctx context.Context) string {
	tok, _ := ctx.Value(sessionTokenCtxKey{}).(string)
	return tok
}

// OptionalAuth attaches the authenticated user to the request context if a valid sid cookie is present.
func OptionalAuth(sessions SessionResolver) nextgin.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie("sid"); err == nil && c.Value != "" {
				if u, err := sessions.ResolveSession(r.Context(), c.Value); err == nil {
					ctx := context.WithValue(r.Context(), userCtxKey{}, u)
					ctx = context.WithValue(ctx, sessionTokenCtxKey{}, c.Value)
					r = r.WithContext(ctx)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth rejects unauthenticated requests with 401.
func RequireAuth(sessions SessionResolver) nextgin.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := UserFromContext(r.Context()); !ok {
				c, err := r.Cookie("sid")
				if err != nil || c.Value == "" {
					nextgin.WriteError(w, http.StatusUnauthorized, "please log in to continue")
					return
				}
				u, err := sessions.ResolveSession(r.Context(), c.Value)
				if err != nil {
					nextgin.WriteError(w, http.StatusUnauthorized, "please log in to continue")
					return
				}
				ctx := context.WithValue(r.Context(), userCtxKey{}, u)
				ctx = context.WithValue(ctx, sessionTokenCtxKey{}, c.Value)
				r = r.WithContext(ctx)
			}
			next.ServeHTTP(w, r)
		})
	}
}
