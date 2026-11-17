//go:build !js

package nextgin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ReadyLine is printed by the worker once it is listening on its socket.
const ReadyLine = "NEXT_WORKER_READY"

// maxSocketPath stays under the smallest sun_path limit (104 on macOS/BSD).
const maxSocketPath = 103

const (
	reprobeInterval = 100 * time.Millisecond
	reprobeTimeout  = 5 * time.Second
)

// PoolConfig configures the worker supervisor.
type PoolConfig struct {
	// Runtime is the JS runtime binary: "node" (default) or "bun", or a path.
	Runtime string
	// Workers is the number of worker processes.
	Workers int
	// Dir is the worker's working directory (the standalone app dir).
	Dir string
	// Entry is the absolute path of nextgin-worker.cjs.
	Entry string
	// Env is appended to the inherited environment of each worker.
	Env []string
	// SocketDir holds the Unix sockets. Defaults to os.TempDir().
	SocketDir string
	// StartupTimeout bounds how long Start waits for every worker to be ready.
	StartupTimeout time.Duration
	// ShutdownGrace is how long workers get to finish in-flight requests.
	ShutdownGrace time.Duration
	// TrustForwardedHeaders forwards inbound X-Forwarded-* unchanged (use only
	// behind a trusted proxy); otherwise they are set from the connection.
	TrustForwardedHeaders bool
	Logger                *slog.Logger

	// command builds the worker process; overridable in tests.
	command func(ctx context.Context, sock string) *exec.Cmd
}

// Pool supervises N headless Next.js workers, each on its own Unix socket,
// and load-balances requests across the ready ones. It implements
// http.Handler as a streaming reverse proxy.
type Pool struct {
	cfg     PoolConfig
	workers []*worker
	rr      atomic.Uint64
	closing atomic.Bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	readyMu sync.Mutex
	readyCh chan struct{} // closed & replaced whenever a worker becomes ready
}

type worker struct {
	id      int
	sock    string
	proxy   *httputil.ReverseProxy
	tr      *http.Transport
	ready   atomic.Bool
	probing atomic.Bool

	mu  sync.Mutex
	cmd *exec.Cmd
}

// StartPool spawns the workers and blocks until all are ready or the startup
// timeout elapses.
func StartPool(cfg PoolConfig) (*Pool, error) {
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	if cfg.Runtime == "" {
		cfg.Runtime = "node"
	}
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = 30 * time.Second
	}
	if cfg.ShutdownGrace <= 0 {
		cfg.ShutdownGrace = 10 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.command == nil {
		cfg.command = func(ctx context.Context, sock string) *exec.Cmd {
			return exec.CommandContext(ctx, cfg.Runtime, cfg.Entry)
		}
	}
	sockDir := cfg.SocketDir
	if sockDir == "" {
		sockDir = os.TempDir()
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{cfg: cfg, ctx: ctx, cancel: cancel, readyCh: make(chan struct{})}
	for i := 0; i < cfg.Workers; i++ {
		sock, err := socketPath(sockDir, i)
		if err != nil {
			cancel()
			return nil, err
		}
		p.workers = append(p.workers, p.newWorker(i, sock))
	}

	allReady := make(chan struct{})
	var once sync.Once
	for _, w := range p.workers {
		p.wg.Add(1)
		go p.supervise(w, func() {
			if p.readyCount() == len(p.workers) {
				once.Do(func() { close(allReady) })
			}
		})
	}

	select {
	case <-allReady:
		cfg.Logger.Info("nextgin: workers ready", "count", len(p.workers), "runtime", cfg.Runtime)
		return p, nil
	case <-time.After(cfg.StartupTimeout):
		_ = p.Close()
		return nil, fmt.Errorf("nextgin: %d/%d workers ready after %s (check worker logs above)", p.readyCount(), len(p.workers), cfg.StartupTimeout)
	}
}

func socketPath(dir string, i int) (string, error) {
	name := fmt.Sprintf("nextgin-%d-%d.sock", os.Getpid(), i)
	p := filepath.Join(dir, name)
	if len(p) > maxSocketPath {
		p = filepath.Join("/tmp", name)
	}
	if len(p) > maxSocketPath {
		return "", fmt.Errorf("nextgin: socket path %q exceeds %d bytes; set Config.SocketDir", p, maxSocketPath)
	}
	return p, nil
}

func (p *Pool) newWorker(id int, sock string) *worker {
	w := &worker{id: id, sock: sock}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			c, err := d.DialContext(ctx, "unix", sock)
			if err != nil && ctx.Err() == nil {
				// The process died or temporarily refused the connection:
				// stop routing to it right away and probe until it recovers
				// or times out.
				w.ready.Store(false)
				p.startReprobe(w)
			}
			return c, err
		},
		MaxIdleConns:        512,
		MaxIdleConnsPerHost: 512,
		// Shorter than the worker's keepAliveTimeout (65s) to avoid reusing a
		// connection Node is closing.
		IdleConnTimeout: 60 * time.Second,
		// Pass the worker's Content-Encoding through untouched.
		DisableCompression: true,
	}
	trust := p.cfg.TrustForwardedHeaders
	w.tr = tr
	w.proxy = &httputil.ReverseProxy{
		Transport: tr,
		// Flush every write immediately: required for React Suspense / RSC
		// streaming to reach the client chunk by chunk.
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "nextgin-worker"
			pr.Out.Host = pr.In.Host // Next uses Host for absolute URLs and Server Action origin checks.
			if trust {
				for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
					if v := pr.In.Header.Values(h); len(v) > 0 {
						pr.Out.Header[h] = v
					}
				}
				if pr.Out.Header.Get("X-Forwarded-Host") == "" || pr.Out.Header.Get("X-Forwarded-Proto") == "" {
					pr.SetXForwarded()
				}
			} else {
				pr.SetXForwarded()
			}
		},
		ErrorHandler: func(rw http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return // client went away
			}
			if isDialError(err) && replayable(r) {
				attempts, _ := r.Context().Value(attemptsKey{}).(int)
				if attempts+1 < len(p.workers) {
					p.cfg.Logger.Warn("nextgin: worker unreachable, retrying on another", "worker", id, "path", r.URL.Path)
					p.ServeHTTP(rw, r.WithContext(context.WithValue(r.Context(), attemptsKey{}, attempts+1)))
					return
				}
			}
			p.cfg.Logger.Error("nextgin: worker proxy error", "worker", id, "path", r.URL.Path, "err", err)
			rw.WriteHeader(http.StatusBadGateway)
		},
	}
	return w
}

type attemptsKey struct{}

// isDialError reports a failure to connect, i.e. nothing was sent upstream.
func isDialError(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// replayable reports whether the request body (if any) can be sent again.
func replayable(r *http.Request) bool {
	return r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0
}

// supervise runs a worker, restarting it with backoff until the pool closes.
func (p *Pool) supervise(w *worker, onReady func()) {
	defer p.wg.Done()
	backoff := 100 * time.Millisecond
	for !p.closing.Load() {
		started := time.Now()
		err := p.runOnce(w, onReady)
		w.ready.Store(false)
		w.tr.CloseIdleConnections()
		if p.closing.Load() {
			return
		}
		if time.Since(started) > 30*time.Second {
			backoff = 100 * time.Millisecond
		}
		p.cfg.Logger.Warn("nextgin: worker exited; restarting", "worker", w.id, "err", err, "in", backoff)
		select {
		case <-time.After(backoff):
		case <-p.ctx.Done():
			return
		}
		backoff = min(backoff*2, 5*time.Second)
	}
}

func (p *Pool) runOnce(w *worker, onReady func()) error {
	cmd := p.cfg.command(p.ctx, w.sock)
	cmd.Dir = p.cfg.Dir
	base := cmd.Env
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = append(append(base, p.cfg.Env...), "NEXT_UDS_PATH="+w.sock)
	cmd.SysProcAttr = workerSysProcAttr()
	// Graceful stop on context cancel; WaitDelay escalates to SIGKILL.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = p.cfg.ShutdownGrace + time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	w.mu.Lock()
	w.cmd = cmd
	w.mu.Unlock()

	log := p.cfg.Logger.With("worker", w.id)
	go p.pipeLogs(stderr, log, nil)
	p.pipeLogs(stdout, log, func() {
		w.ready.Store(true)
		p.signalReady()
		onReady()
	})
	err = cmd.Wait()
	w.mu.Lock()
	if w.cmd == cmd {
		w.cmd = nil
	}
	w.mu.Unlock()
	return err
}

func (p *Pool) startReprobe(w *worker) {
	if p.closing.Load() || p.ctx.Err() != nil {
		return
	}
	if !w.probing.CompareAndSwap(false, true) {
		return
	}
	go p.reprobe(w)
}

func (p *Pool) reprobe(w *worker) {
	defer w.probing.Store(false)

	w.mu.Lock()
	origCmd := w.cmd
	w.mu.Unlock()
	if origCmd == nil {
		return
	}

	ticker := time.NewTicker(reprobeInterval)
	defer ticker.Stop()
	deadline := time.NewTimer(reprobeTimeout)
	defer deadline.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-deadline.C:
			w.mu.Lock()
			same := w.cmd == origCmd
			w.mu.Unlock()
			if same && origCmd.Process != nil && !p.closing.Load() {
				p.cfg.Logger.Warn("nextgin: worker unresponsive to re-probe; killing", "worker", w.id)
				_ = origCmd.Process.Kill()
			}
			return
		case <-ticker.C:
			if p.closing.Load() {
				return
			}
			w.mu.Lock()
			same := w.cmd == origCmd
			w.mu.Unlock()
			if !same {
				return
			}
			if p.probeSocket(w.sock) {
				w.mu.Lock()
				same = w.cmd == origCmd && !p.closing.Load()
				if same {
					w.ready.Store(true)
				}
				w.mu.Unlock()
				if same {
					p.signalReady()
				}
				return
			}
		}
	}
}

func (p *Pool) probeSocket(sock string) bool {
	ctx, cancel := context.WithTimeout(p.ctx, reprobeInterval)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// pipeLogs forwards worker output to the logger, firing onReady on ReadyLine.
func (p *Pool) pipeLogs(r io.Reader, log *slog.Logger, onReady func()) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if onReady != nil && line == ReadyLine {
			onReady()
			continue
		}
		log.Info(line)
	}
}

func (p *Pool) signalReady() {
	p.readyMu.Lock()
	close(p.readyCh)
	p.readyCh = make(chan struct{})
	p.readyMu.Unlock()
}

func (p *Pool) readyCount() int {
	n := 0
	for _, w := range p.workers {
		if w.ready.Load() {
			n++
		}
	}
	return n
}

// pick returns the next ready worker in round-robin order, or nil.
func (p *Pool) pick() *worker {
	n := uint64(len(p.workers))
	start := p.rr.Add(1)
	for i := uint64(0); i < n; i++ {
		if w := p.workers[(start+i)%n]; w.ready.Load() {
			return w
		}
	}
	return nil
}

// ServeHTTP proxies the request to a ready worker, waiting briefly if all
// workers are restarting.
func (p *Pool) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	w := p.pick()
	if w == nil {
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for w == nil {
			p.readyMu.Lock()
			ch := p.readyCh
			p.readyMu.Unlock()
			if w = p.pick(); w != nil {
				break
			}
			select {
			case <-ch:
				w = p.pick()
			case <-deadline.C:
				http.Error(rw, "Service Unavailable", http.StatusServiceUnavailable)
				return
			case <-r.Context().Done():
				return
			}
		}
	}
	w.proxy.ServeHTTP(rw, r)
}

// Close stops all workers gracefully and removes their sockets.
func (p *Pool) Close() error {
	if !p.closing.CompareAndSwap(false, true) {
		return nil
	}
	p.cancel() // SIGTERM via cmd.Cancel, SIGKILL after WaitDelay
	p.wg.Wait()
	for _, w := range p.workers {
		w.tr.CloseIdleConnections()
		_ = os.Remove(w.sock)
	}
	return nil
}
