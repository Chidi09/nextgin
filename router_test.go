package nextgin

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRouterGroupsMiddlewareAndParams(t *testing.T) {
	r := NewRouter()
	var trace []string

	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			trace = append(trace, "root")
			next.ServeHTTP(w, req)
		})
	})

	api := r.Group("/api", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			trace = append(trace, "api")
			next.ServeHTTP(w, req)
		})
	})

	api.With(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			trace = append(trace, "with")
			next.ServeHTTP(w, req)
		})
	}).PATCH("/items/{id}", func(w http.ResponseWriter, req *http.Request) {
		trace = append(trace, "handler:"+req.PathValue("id"))
		w.WriteHeader(http.StatusOK)
	}, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			trace = append(trace, "route")
			next.ServeHTTP(w, req)
		})
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/items/42", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	wantTrace := "root,api,with,route,handler:42"
	if got := strings.Join(trace, ","); got != wantTrace {
		t.Fatalf("trace = %q, want %q", got, wantTrace)
	}

	routes := r.Routes()
	if len(routes) != 1 || routes[0] != (ExportRoute{Method: http.MethodPatch, Path: "/api/items/{id}"}) {
		t.Fatalf("routes = %+v, want PATCH /api/items/{id}", routes)
	}
}

func TestRouterExactRootDoesNotShadowFallback(t *testing.T) {
	r := NewRouter()
	r.GET("/", func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte("root-handler"))
	})
	r.setFallback(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte("fallback:" + req.URL.Path))
	}))

	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec1.Body.String() != "root-handler" {
		t.Fatalf("GET / = %q, want root-handler", rec1.Body.String())
	}

	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/about", nil))
	if rec2.Body.String() != "fallback:/about" {
		t.Fatalf("GET /about = %q, want fallback:/about", rec2.Body.String())
	}
}

func TestRecoveryMiddleware(t *testing.T) {
	r := NewRouter()
	r.Use(Recovery(slog.New(slog.NewTextHandler(io.Discard, nil))))
	r.GET("/boom", func(w http.ResponseWriter, req *http.Request) {
		panic("kaboom")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
