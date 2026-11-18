package nextgin

import (
	"crypto/subtle"
	"net/http"
	"os"
)

// InternalSecretEnv names the shared secret between the web app and the Go backend.
const InternalSecretEnv = "NEXTGIN_INTERNAL_SECRET"

// InternalHeader carries it on server-to-server requests.
const InternalHeader = "X-Nextgin-Internal"

// RequireInternal returns middleware that responds with 401 unless the request carries
// InternalHeader equal to $NEXTGIN_INTERNAL_SECRET (constant-time compare).
// It fails closed: the secret is read when RequireInternal is called, and if it is empty
// every request is rejected (401), never allowed.
func RequireInternal() Middleware {
	secret := []byte(os.Getenv(InternalSecretEnv))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(secret) == 0 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			got := r.Header.Get(InternalHeader)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), secret) != 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
