package nextgin

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestCORSPanics(t *testing.T) {
	cases := []struct {
		name    string
		origins []string
	}{
		{name: "empty list", origins: nil},
		{name: "wildcard", origins: []string{"*"}},
		{name: "wildcard with valid origin", origins: []string{"https://app.example.com", "*"}},
		{name: "empty string entry", origins: []string{""}},
		{name: "empty string with valid origin", origins: []string{"https://app.example.com", ""}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic, got nil")
				}
				if r != corsPanicMsg {
					t.Fatalf("panic = %q, want %q", r, corsPanicMsg)
				}
			}()
			CORS(tc.origins...)
		})
	}
}

func TestCORS(t *testing.T) {
	cases := []struct {
		name              string
		configuredOrigins []string
		method            string
		origin            string
		reqMethod         string
		reqHeaders        string
		wantStatus        int
		wantHandlerCalled bool
		wantAllowOrigin   string
		wantAllowCreds    string
		wantAllowMethods  string
		wantAllowHeaders  string
		wantMaxAge        string
		wantVaryOrigin    bool
	}{
		{
			name:              "no Origin passes through untouched",
			configuredOrigins: []string{"https://app.example.com"},
			method:            http.MethodGet,
			wantStatus:        http.StatusOK,
			wantHandlerCalled: true,
			wantVaryOrigin:    false,
		},
		{
			name:              "allowed simple request",
			configuredOrigins: []string{"https://app.example.com"},
			method:            http.MethodGet,
			origin:            "https://app.example.com",
			wantStatus:        http.StatusOK,
			wantHandlerCalled: true,
			wantAllowOrigin:   "https://app.example.com",
			wantAllowCreds:    "true",
			wantVaryOrigin:    true,
		},
		{
			name:              "disallowed simple request continues without CORS headers",
			configuredOrigins: []string{"https://app.example.com"},
			method:            http.MethodGet,
			origin:            "https://evil.example.com",
			wantStatus:        http.StatusOK,
			wantHandlerCalled: true,
			wantVaryOrigin:    true,
		},
		{
			name:              "allowed preflight default headers",
			configuredOrigins: []string{"https://app.example.com"},
			method:            http.MethodOptions,
			origin:            "https://app.example.com",
			reqMethod:         http.MethodPost,
			wantStatus:        http.StatusNoContent,
			wantHandlerCalled: false,
			wantAllowOrigin:   "https://app.example.com",
			wantAllowCreds:    "true",
			wantAllowMethods:  "GET, POST, PUT, PATCH, DELETE, OPTIONS",
			wantAllowHeaders:  "Content-Type, Authorization",
			wantMaxAge:        "600",
			wantVaryOrigin:    true,
		},
		{
			name:              "disallowed preflight aborts 403",
			configuredOrigins: []string{"https://app.example.com"},
			method:            http.MethodOptions,
			origin:            "https://evil.example.com",
			reqMethod:         http.MethodPost,
			wantStatus:        http.StatusForbidden,
			wantHandlerCalled: false,
			wantVaryOrigin:    true,
		},
		{
			name:              "preflight echoes requested headers",
			configuredOrigins: []string{"https://app.example.com"},
			method:            http.MethodOptions,
			origin:            "https://app.example.com",
			reqMethod:         http.MethodPut,
			reqHeaders:        "X-Custom-Header, Content-Type",
			wantStatus:        http.StatusNoContent,
			wantHandlerCalled: false,
			wantAllowOrigin:   "https://app.example.com",
			wantAllowCreds:    "true",
			wantAllowMethods:  "GET, POST, PUT, PATCH, DELETE, OPTIONS",
			wantAllowHeaders:  "X-Custom-Header, Content-Type",
			wantMaxAge:        "600",
			wantVaryOrigin:    true,
		},
		{
			name:              "trailing slash in configured origin",
			configuredOrigins: []string{"https://app.example.com/"},
			method:            http.MethodGet,
			origin:            "https://app.example.com",
			wantStatus:        http.StatusOK,
			wantHandlerCalled: true,
			wantAllowOrigin:   "https://app.example.com",
			wantAllowCreds:    "true",
			wantVaryOrigin:    true,
		},
		{
			name:              "case sensitive origin comparison",
			configuredOrigins: []string{"https://app.example.com"},
			method:            http.MethodGet,
			origin:            "https://APP.example.com",
			wantStatus:        http.StatusOK,
			wantHandlerCalled: true,
			wantVaryOrigin:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRouter()
			r.Use(CORS(tc.configuredOrigins...))
			called := false
			handler := func(w http.ResponseWriter, req *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}
			r.GET("/api/test", handler)
			r.OPTIONS("/api/test", handler)

			req := httptest.NewRequest(tc.method, "/api/test", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.reqMethod != "" {
				req.Header.Set("Access-Control-Request-Method", tc.reqMethod)
			}
			if tc.reqHeaders != "" {
				req.Header.Set("Access-Control-Request-Headers", tc.reqHeaders)
			}

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if called != tc.wantHandlerCalled {
				t.Fatalf("handler called = %v, want %v", called, tc.wantHandlerCalled)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tc.wantAllowOrigin {
				t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, tc.wantAllowOrigin)
			}
			if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != tc.wantAllowCreds {
				t.Fatalf("Access-Control-Allow-Credentials = %q, want %q", got, tc.wantAllowCreds)
			}
			if got := rec.Header().Get("Access-Control-Allow-Methods"); got != tc.wantAllowMethods {
				t.Fatalf("Access-Control-Allow-Methods = %q, want %q", got, tc.wantAllowMethods)
			}
			if got := rec.Header().Get("Access-Control-Allow-Headers"); got != tc.wantAllowHeaders {
				t.Fatalf("Access-Control-Allow-Headers = %q, want %q", got, tc.wantAllowHeaders)
			}
			if got := rec.Header().Get("Access-Control-Max-Age"); got != tc.wantMaxAge {
				t.Fatalf("Access-Control-Max-Age = %q, want %q", got, tc.wantMaxAge)
			}
			varyValues := rec.Header().Values("Vary")
			hasVaryOrigin := slices.Contains(varyValues, "Origin")
			if hasVaryOrigin != tc.wantVaryOrigin {
				t.Fatalf("Vary contains Origin = %v (%v), want %v", hasVaryOrigin, varyValues, tc.wantVaryOrigin)
			}
		})
	}
}
