// Package nextgin serves a Next.js application behind a Go HTTP server.
//
// Go owns the HTTP layer: your API routes, build assets and fully static
// pages are answered natively, while SSR, React Server Components, Server
// Actions, ISR and middleware are streamed from a pool of headless Next.js
// workers over Unix domain sockets.
//
// The core is a plain http.Handler (see Frontend / Handler); App adds an
// ergonomic Gin integration on top.
package nextgin

import (
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
)

// DevURLEnv points nextgin at a running `next dev` server. When set (as
// `nextgin dev` does), no build output or workers are needed and every
// frontend request — including HMR websockets — is proxied to it.
const DevURLEnv = "NEXTGIN_DEV_URL"

// InternalURLEnv is set for workers (and by `nextgin dev` for next dev) to the
// URL where the Go server is reachable, so server-side React code can call
// Go API routes.
const InternalURLEnv = "NEXTGIN_INTERNAL_URL"

// Config configures a Frontend / App.
type Config struct {
	// Workers is the number of Next.js worker processes.
	// Default: runtime.NumCPU(), capped at 4 (each worker holds a full
	// Next.js server in memory; raise it explicitly for bigger machines).
	Workers int
	// Runtime is "node" (default), "bun", or a path to a JS runtime binary.
	Runtime string
	// Assets overrides where the manifest, static files and prerendered pages
	// are read from, e.g. an embed.FS sub-tree for single-binary deploys. The
	// worker's server bundle is always read from the on-disk directory.
	Assets fs.FS
	// DisablePrerendered sends all page requests to Next.js. See
	// HandlerOptions.DisablePrerendered.
	DisablePrerendered bool
	// DebugHeaders adds X-Nextgin-Source to responses. Defaults to
	// $NEXTGIN_DEBUG_HEADERS == "1".
	DebugHeaders bool
	// TrustForwardedHeaders passes inbound X-Forwarded-* through to Next.js.
	// Enable only behind a trusted reverse proxy / load balancer.
	TrustForwardedHeaders bool
	// SocketDir holds worker sockets. Default os.TempDir().
	SocketDir string
	// StartupTimeout bounds worker boot. Default 30s.
	StartupTimeout time.Duration
	// ShutdownGrace bounds in-flight requests on Close. Default 10s.
	ShutdownGrace time.Duration
	// CronTimeout is the per-run timeout for scheduled jobs on the selfhost
	// target. Default 5m.
	CronTimeout time.Duration
	// Env is extra environment for workers ("KEY=value").
	Env []string
	// Port is passed to workers as PORT, used by Next.js for self-referencing
	// URLs. Defaults to $PORT. Set it to the port you pass to Run.
	Port string
	// DevURL proxies everything to a `next dev` server instead of using the
	// build output. Defaults to $NEXTGIN_DEV_URL.
	DevURL string
	// Router is an existing Gin engine to use. Default gin.Default().
	Router *gin.Engine
	// Logger receives runtime and worker logs. Default slog.Default().
	Logger *slog.Logger
}

// App is the Gin integration: register API routes on Router, then call
// MountFrontend so every unmatched request falls through to Next.js.
type App struct {
	Router   *gin.Engine
	Frontend *Frontend
	cfg      Config
	target   TargetKind
	jobs     []CronJob
	jobNames map[string]bool
}

// New creates an App serving the .nextgin output in dir.
func New(dir string, cfg Config) (*App, error) {
	if cfg.Port == "" {
		cfg.Port = os.Getenv("PORT")
	}
	target, err := DetectTarget()
	if err != nil {
		return nil, err
	}
	var f *Frontend
	if exportMode() {
		// Export mode inspects Go routes and jobs without touching disk or
		// booting workers. stdout must carry only the export JSON, so send
		// gin's debug output (route listing, mode warnings) to stderr.
		gin.DefaultWriter = os.Stderr
	} else {
		f, err = initFrontend(dir, cfg, target)
		if err != nil {
			return nil, err
		}
	}
	r := cfg.Router
	if r == nil {
		r = gin.Default()
	}
	return &App{Router: r, Frontend: f, cfg: cfg, target: target}, nil
}

// Handler returns the engine-agnostic http.Handler for use with net/http or
// other routers (e.g. chi: r.NotFound(app.Handler().ServeHTTP)).
func (a *App) Handler() http.Handler {
	if a.Frontend == nil {
		return http.NotFoundHandler()
	}
	return a.Frontend
}

// MountFrontend routes every request Gin has no route for to Next.js, so
// routes you register on Router (e.g. /api/*) always take priority.
func (a *App) MountFrontend() {
	if a.Frontend == nil {
		return
	}
	a.Router.NoRoute(gin.WrapH(a.Frontend))
}

// Run serves on addr until SIGINT/SIGTERM, then drains in-flight requests
// and stops the workers.
func (a *App) Run(addr string) error {
	if exportMode() {
		if err := a.writeExport(os.Stdout); err != nil {
			return err
		}
		os.Exit(0)
	}
	return a.runTarget(addr)
}

// Close stops the Next.js workers.
func (a *App) Close() error {
	if a.Frontend == nil {
		return nil
	}
	return a.Frontend.Close()
}

func (c Config) loggerOrDefault() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}
