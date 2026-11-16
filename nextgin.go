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
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
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

// Frontend is the net/http entry point: build-output serving plus the worker
// pool lifecycle. It implements http.Handler.
type Frontend struct {
	http.Handler
	Manifest *Manifest // nil in dev mode
	pool     *Pool
}

// NewFrontend loads the .nextgin build output from dir and starts workers.
func NewFrontend(dir string, cfg Config) (*Frontend, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	devURL := cfg.DevURL
	if devURL == "" {
		devURL = os.Getenv(DevURLEnv)
	}
	if devURL != "" {
		h, err := devProxy(devURL, cfg.Logger)
		if err != nil {
			return nil, err
		}
		cfg.Logger.Info("nextgin: dev mode, proxying frontend", "to", devURL)
		return &Frontend{Handler: h}, nil
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	assets := cfg.Assets
	if assets == nil {
		assets = os.DirFS(absDir)
	}
	m, err := LoadManifest(assets)
	if err != nil {
		return nil, err
	}

	workers := cfg.Workers
	if workers <= 0 {
		workers = min(runtime.NumCPU(), 4)
	}
	entry := filepath.Join(absDir, filepath.FromSlash(m.Worker.Entry))
	if _, err := os.Stat(entry); err != nil {
		return nil, fmt.Errorf("nextgin: worker entry %s missing (did the build finish? try `npx nextgin finalize`): %w", entry, err)
	}
	env := append([]string{"NODE_ENV=production"}, cfg.Env...)
	if cfg.Port != "" {
		// NEXTGIN_INTERNAL_URL lets Server Components / Actions call the Go
		// API routes on this same server.
		env = append(env, "PORT="+cfg.Port, InternalURLEnv+"=http://127.0.0.1:"+cfg.Port)
	}
	if cfg.ShutdownGrace > 0 {
		env = append(env, fmt.Sprintf("NEXTGIN_SHUTDOWN_GRACE_MS=%d", cfg.ShutdownGrace.Milliseconds()))
	}

	pool, err := StartPool(PoolConfig{
		Runtime:               cfg.Runtime,
		Workers:               workers,
		Dir:                   filepath.Join(absDir, filepath.FromSlash(m.Worker.Dir)),
		Entry:                 entry,
		Env:                   env,
		SocketDir:             cfg.SocketDir,
		StartupTimeout:        cfg.StartupTimeout,
		ShutdownGrace:         cfg.ShutdownGrace,
		TrustForwardedHeaders: cfg.TrustForwardedHeaders,
		Logger:                cfg.Logger,
	})
	if err != nil {
		return nil, err
	}
	h, err := NewHandler(m, assets, pool, HandlerOptions{
		DisablePrerendered: cfg.DisablePrerendered,
		DebugHeaders:       cfg.DebugHeaders || os.Getenv("NEXTGIN_DEBUG_HEADERS") == "1",
	})
	if err != nil {
		_ = pool.Close()
		return nil, err
	}
	return &Frontend{Handler: h, Manifest: m, pool: pool}, nil
}

// Close stops the workers. Call it after your HTTP server has drained.
func (f *Frontend) Close() error {
	if f.pool == nil {
		return nil
	}
	return f.pool.Close()
}

func devProxy(raw string, log *slog.Logger) (http.Handler, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("nextgin: invalid dev URL %q", raw)
	}
	return &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Warn("nextgin: next dev unreachable", "err", err)
			http.Error(w, "next dev server is not reachable yet", http.StatusBadGateway)
		},
	}, nil
}

// App is the Gin integration: register API routes on Router, then call
// MountFrontend so every unmatched request falls through to Next.js.
type App struct {
	Router   *gin.Engine
	Frontend *Frontend
	cfg      Config
}

// New creates an App serving the .nextgin output in dir.
func New(dir string, cfg Config) (*App, error) {
	if cfg.Port == "" {
		cfg.Port = os.Getenv("PORT")
	}
	f, err := NewFrontend(dir, cfg)
	if err != nil {
		return nil, err
	}
	r := cfg.Router
	if r == nil {
		r = gin.Default()
	}
	return &App{Router: r, Frontend: f, cfg: cfg}, nil
}

// Handler returns the engine-agnostic http.Handler for use with net/http or
// other routers (e.g. chi: r.NotFound(app.Handler().ServeHTTP)).
func (a *App) Handler() http.Handler { return a.Frontend }

// MountFrontend routes every request Gin has no route for to Next.js, so
// routes you register on Router (e.g. /api/*) always take priority.
func (a *App) MountFrontend() {
	a.Router.NoRoute(gin.WrapH(a.Frontend))
}

// Run serves on addr until SIGINT/SIGTERM, then drains in-flight requests
// and stops the workers.
func (a *App) Run(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           a.Router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		a.cfg.loggerOrDefault().Info("nextgin: listening", "addr", displayAddr(addr))
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		_ = a.Close()
		return err
	case <-ctx.Done():
	}
	grace := a.cfg.ShutdownGrace
	if grace <= 0 {
		grace = 10 * time.Second
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	err := srv.Shutdown(shutCtx)
	if cerr := a.Close(); err == nil {
		err = cerr
	}
	return err
}

// Close stops the Next.js workers.
func (a *App) Close() error { return a.Frontend.Close() }

func (c Config) loggerOrDefault() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

func displayAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	return "http://" + addr
}
