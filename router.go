package nextgin

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
)

// Middleware wraps an http.Handler to add cross-cutting behavior such as CORS,
// authentication, rate limiting, or panic recovery.
type Middleware func(http.Handler) http.Handler

// Router is a lightweight, zero-dependency HTTP router built on Go's standard
// net/http.ServeMux. It supports route groups, middleware, and route
// introspection for App.Export without pulling in external frameworks.
type Router struct {
	mux         *http.ServeMux
	root        *Router
	prefix      string
	middlewares []Middleware
	routes      *[]ExportRoute
	hasFallback bool
}

// NewRouter creates a new Router with no middleware attached.
func NewRouter() *Router {
	routes := []ExportRoute{}
	r := &Router{
		mux:    http.NewServeMux(),
		routes: &routes,
	}
	r.root = r
	return r
}

// Use appends middleware to the router.
//
// On the root Router, middleware runs on every request (including CORS
// preflights and fallback requests). On a Group or With sub-router, middleware
// wraps routes registered through that sub-router.
func (r *Router) Use(mw ...Middleware) {
	r.middlewares = append(r.middlewares, mw...)
}

// Group creates a sub-router sharing the underlying ServeMux with an added
// path prefix and optional middleware.
func (r *Router) Group(prefix string, mw ...Middleware) *Router {
	var combined []Middleware
	if r != r.root {
		combined = append(combined, r.middlewares...)
	}
	combined = append(combined, mw...)
	return &Router{
		mux:         r.mux,
		root:        r.root,
		prefix:      joinPath(r.prefix, prefix),
		middlewares: combined,
		routes:      r.routes,
	}
}

// With creates a sub-router at the same prefix with additional middleware.
func (r *Router) With(mw ...Middleware) *Router {
	return r.Group("", mw...)
}

// Handle registers an http.Handler for method and path with optional per-route
// middleware. Method may be empty to match all HTTP methods.
func (r *Router) Handle(method, path string, h http.Handler, mw ...Middleware) {
	fullPath := joinPath(r.prefix, path)
	if fullPath == "" || fullPath[0] != '/' {
		panic(fmt.Sprintf("nextgin: route path %q must start with '/'", fullPath))
	}

	wrapped := h
	// Apply per-route middleware innermost, then sub-router middleware.
	// Root middleware runs in ServeHTTP so it also covers preflights and fallback.
	wrapped = applyMiddleware(wrapped, mw)
	if r != r.root {
		wrapped = applyMiddleware(wrapped, r.middlewares)
	}

	pattern := muxPattern(method, fullPath)
	r.mux.Handle(pattern, wrapped)

	*r.routes = append(*r.routes, ExportRoute{
		Method: method,
		Path:   fullPath,
	})
}

// HandleFunc registers an http.HandlerFunc for method and path.
func (r *Router) HandleFunc(method, path string, h http.HandlerFunc, mw ...Middleware) {
	r.Handle(method, path, h, mw...)
}

func (r *Router) GET(path string, h http.HandlerFunc, mw ...Middleware) {
	r.Handle(http.MethodGet, path, h, mw...)
}

func (r *Router) POST(path string, h http.HandlerFunc, mw ...Middleware) {
	r.Handle(http.MethodPost, path, h, mw...)
}

func (r *Router) PUT(path string, h http.HandlerFunc, mw ...Middleware) {
	r.Handle(http.MethodPut, path, h, mw...)
}

func (r *Router) PATCH(path string, h http.HandlerFunc, mw ...Middleware) {
	r.Handle(http.MethodPatch, path, h, mw...)
}

func (r *Router) DELETE(path string, h http.HandlerFunc, mw ...Middleware) {
	r.Handle(http.MethodDelete, path, h, mw...)
}

func (r *Router) OPTIONS(path string, h http.HandlerFunc, mw ...Middleware) {
	r.Handle(http.MethodOptions, path, h, mw...)
}

// Routes returns a copy of all routes registered on the router.
func (r *Router) Routes() []ExportRoute {
	if r == nil || r.routes == nil {
		return nil
	}
	return slices.Clone(*r.routes)
}

// ServeHTTP dispatches the request through root middlewares and the underlying
// ServeMux.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	root := r.root
	h := applyMiddleware(root.mux, root.middlewares)
	h.ServeHTTP(w, req)
}

// setFallback mounts a catch-all handler on "/" without recording it in Routes.
func (r *Router) setFallback(h http.Handler) {
	root := r.root
	if root.hasFallback || h == nil {
		return
	}
	root.hasFallback = true
	root.mux.Handle("/", h)
}

// Recovery returns middleware that recovers from panics in downstream handlers,
// logs the panic and stack trace, and responds with 500 Internal Server Error.
func Recovery(log *slog.Logger) Middleware {
	if log == nil {
		log = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("nextgin: panic recovered",
						"method", r.Method,
						"path", r.URL.Path,
						"panic", rec,
						"stack", string(debug.Stack()),
					)
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func applyMiddleware(h http.Handler, mws []Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		if mws[i] != nil {
			h = mws[i](h)
		}
	}
	return h
}

func joinPath(prefix, path string) string {
	if prefix == "" {
		return path
	}
	if path == "" {
		return prefix
	}
	p := strings.TrimRight(prefix, "/")
	s := strings.TrimLeft(path, "/")
	if s == "" {
		if p == "" {
			return "/"
		}
		return p
	}
	return p + "/" + s
}

func muxPattern(method, path string) string {
	// In Go 1.22+ ServeMux, "GET /" matches every GET path. Use "/{$}" for exact root.
	p := path
	if p == "/" && method != "" {
		p = "/{$}"
	}
	if method == "" {
		return p
	}
	return method + " " + p
}
