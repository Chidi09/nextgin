package nextgin

import (
	"net/http"
	"strings"
)

const corsPanicMsg = `nextgin: CORS needs an explicit origin allow-list ("*" is not allowed with credentials)`

// CORS returns middleware for browsers that call the backend directly (SPEC §9.3 direct mode).
func CORS(origins ...string) Middleware {
	if len(origins) == 0 {
		panic(corsPanicMsg)
	}
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		trimmed := strings.TrimSuffix(o, "/")
		if o == "" || trimmed == "" || trimmed == "*" {
			panic(corsPanicMsg)
		}
		allowed[trimmed] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Add("Vary", "Origin")

			isPreflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
			_, ok := allowed[origin]
			if !ok {
				if isPreflight {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")

			if isPreflight {
				allowHeaders := r.Header.Get("Access-Control-Request-Headers")
				if allowHeaders == "" {
					allowHeaders = "Content-Type, Authorization"
				}
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", allowHeaders)
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
