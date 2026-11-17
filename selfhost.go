//go:build !js

package nextgin

import (
	"context"
	"errors"
	"fmt"
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
)

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

func initFrontend(dir string, cfg Config, target TargetKind) (*Frontend, error) {
	switch target {
	case TargetSelfhost:
		return NewFrontend(dir, cfg)
	case TargetVercel, TargetBackend:
		// Vercel serves Next.js natively and routes Go traffic directly to this service;
		// backend runs API-only with Next.js deployed elsewhere.
		return nil, nil
	default:
		return nil, errors.New("nextgin: the cloudflare target requires GOOS=js GOARCH=wasm")
	}
}

func (a *App) runTarget(addr string) error {
	if a.target == TargetVercel || a.target == TargetBackend {
		if p := os.Getenv("PORT"); p != "" {
			addr = ":" + p
		} else if addr == "" {
			addr = ":3000"
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var waitScheduler func(time.Duration)
	if a.schedulerEnabled() {
		waitScheduler = a.startScheduler(ctx)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           a.Router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		a.cfg.loggerOrDefault().Info("nextgin: listening", "addr", displayAddr(addr))
		errCh <- srv.ListenAndServe()
	}()

	grace := a.cfg.ShutdownGrace
	if grace <= 0 {
		grace = 10 * time.Second
	}

	select {
	case err := <-errCh:
		stop()
		if waitScheduler != nil {
			waitScheduler(grace)
		}
		_ = a.Close()
		return err
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	err := srv.Shutdown(shutCtx)
	if waitScheduler != nil {
		waitScheduler(grace)
	}
	if cerr := a.Close(); err == nil {
		err = cerr
	}
	return err
}

func displayAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	return "http://" + addr
}
