//go:build !js

package nextgin

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"
)

// ---- fake worker (the test binary re-executed) ----------------------------

const fakeWorkerEnv = "NEXTGIN_FAKE_WORKER"

func TestMain(m *testing.M) {
	if os.Getenv(fakeWorkerEnv) == "1" {
		runFakeWorker()
		return
	}
	os.Exit(m.Run())
}

func runFakeWorker() {
	sock := os.Getenv("NEXT_UDS_PATH")
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<shell>")
		w.(http.Flusher).Flush()
		time.Sleep(400 * time.Millisecond)
		io.WriteString(w, "<suspended/>")
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/target", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/crash", func(w http.ResponseWriter, r *http.Request) { os.Exit(3) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "pid=%d method=%s host=%s xfh=%s xfp=%s path=%s",
			os.Getpid(), r.Method, r.Host, r.Header.Get("X-Forwarded-Host"), r.Header.Get("X-Forwarded-Proto"), r.URL.RequestURI())
	})
	srv := &http.Server{Handler: mux}
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		<-ch
		srv.Close()
		os.Remove(sock)
		os.Exit(0)
	}()
	fmt.Println("booting")
	fmt.Println(ReadyLine)
	_ = srv.Serve(ln)
}

func fakeWorkerCommand(ctx context.Context, sock string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), fakeWorkerEnv+"=1")
	return cmd
}

func startFakePool(t *testing.T, n int) *Pool {
	t.Helper()
	p, err := StartPool(PoolConfig{
		Workers:        n,
		StartupTimeout: 10 * time.Second,
		ShutdownGrace:  2 * time.Second,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		command: func(ctx context.Context, sock string) *exec.Cmd {
			cmd := fakeWorkerCommand(ctx, sock)
			return cmd
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

// ---- fixtures --------------------------------------------------------------

const testManifest = `{
  "version": 1, "buildId": "b1", "nextVersion": "16.4.6", "basePath": "", "trailingSlash": false,
  "worker": {"dir": "server", "entry": "server/nextgin-worker.cjs"},
  "staticFiles": [
    {"pathname": "/_next/static/chunks/a.js", "file": "static/_next/static/chunks/a.js", "immutable": true,
     "headers": {"cache-control": "public,max-age=31536000,immutable"}},
    {"pathname": "/robots.txt", "file": "static/robots.txt", "immutable": false, "headers": {"cache-control": "public, max-age=0"}},
    {"pathname": "/mw/logo.png", "file": "static/mw/logo.png", "immutable": false, "headers": {"cache-control": "public, max-age=0"}}
  ],
  "prerendered": [
    {"pathname": "/", "file": "prerendered/index.html", "status": 200,
     "headers": {"content-type": "text/html; charset=utf-8", "cache-control": "s-maxage=31536000", "vary": "rsc, next-router-state-tree"}},
    {"pathname": "/about", "file": "prerendered/about.html", "status": 200, "headers": {"content-type": "text/html; charset=utf-8"}},
    {"pathname": "/old", "file": "prerendered/old.html", "status": 200, "headers": {"content-type": "text/html; charset=utf-8"}}
  ],
  "bypass": [
    {"kind": "headers", "source": "/about", "sourceRegex": "^\\/about(?:\\/)?$", "headers": {"x-custom": "1"}, "applicable": true},
    {"kind": "redirect", "source": "/old", "sourceRegex": "^\\/old(?:\\/)?$", "applicable": false},
    {"kind": "middleware", "source": "/mw/:path*", "sourceRegex": "^\\/mw(?:\\/.*)?$", "applicable": false},
    {"kind": "headers", "source": "/x", "sourceRegex": "^(?=lookahead)$", "headers": {"x": "1"}, "applicable": true}
  ]
}`

func testAssets() fstest.MapFS {
	return fstest.MapFS{
		"manifest.json":                   {Data: []byte(testManifest)},
		"static/_next/static/chunks/a.js": {Data: []byte("console.log(1)")},
		"static/robots.txt":               {Data: []byte("User-agent: *")},
		"static/mw/logo.png":              {Data: []byte("png")},
		"prerendered/index.html":          {Data: []byte("<h1>home</h1>")},
		"prerendered/about.html":          {Data: []byte("<h1>about</h1>")},
		"prerendered/old.html":            {Data: []byte("<h1>old</h1>")},
	}
}

type recorder struct{ hits []string }

func (u *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.hits = append(u.hits, r.Method+" "+r.URL.RequestURI())
	w.Header().Set("X-Upstream", "1")
	w.WriteHeader(http.StatusTeapot)
}

func newTestHandler(t *testing.T, opts HandlerOptions) (*Handler, *recorder) {
	t.Helper()
	assets := testAssets()
	m, err := LoadManifest(assets)
	if err != nil {
		t.Fatal(err)
	}
	up := &recorder{}
	h, err := NewHandler(m, assets, up, opts)
	if err != nil {
		t.Fatal(err)
	}
	return h, up
}

func do(h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ---- manifest ---------------------------------------------------------------

func TestManifestVersionMismatch(t *testing.T) {
	_, err := ParseManifest([]byte(`{"version": 99}`))
	if err == nil || !strings.Contains(err.Error(), "version 99") {
		t.Fatalf("want version error, got %v", err)
	}
}

func TestManifestUncompilableRegexBypasses(t *testing.T) {
	m, _ := LoadManifest(testAssets())
	if _, bypass := m.evaluate("/anything"); !bypass {
		t.Fatal("a regex Go cannot compile must force the worker path")
	}
}

// ---- handler ----------------------------------------------------------------

func TestHandlerRouting(t *testing.T) {
	// Drop the uncompilable rule so it does not bypass everything.
	assets := testAssets()
	m, _ := LoadManifest(assets)
	m.Bypass = m.Bypass[:3]
	up := &recorder{}
	h, err := NewHandler(m, assets, up, HandlerOptions{})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, method, target string
		hdr                  map[string]string
		wantStatus           int
		wantBody             string
		wantHeader           [2]string
		upstream             bool
	}{
		{name: "immutable chunk", method: "GET", target: "/_next/static/chunks/a.js", wantStatus: 200, wantBody: "console.log(1)",
			wantHeader: [2]string{"Cache-Control", "public,max-age=31536000,immutable"}},
		{name: "public file", method: "GET", target: "/robots.txt", wantStatus: 200, wantBody: "User-agent: *"},
		{name: "prerendered home", method: "GET", target: "/", wantStatus: 200, wantBody: "<h1>home</h1>",
			wantHeader: [2]string{"Content-Type", "text/html; charset=utf-8"}},
		{name: "query string ignored for static page", method: "GET", target: "/?utm=1", wantStatus: 200, wantBody: "<h1>home</h1>"},
		{name: "header rule applied by Go", method: "GET", target: "/about", wantStatus: 200, wantHeader: [2]string{"X-Custom", "1"}},
		{name: "HEAD prerendered", method: "HEAD", target: "/", wantStatus: 200},
		{name: "redirect rule -> worker", method: "GET", target: "/old", wantStatus: 418, upstream: true},
		{name: "middleware matcher on asset -> worker", method: "GET", target: "/mw/logo.png", wantStatus: 418, upstream: true},
		{name: "RSC header -> worker", method: "GET", target: "/", hdr: map[string]string{"RSC": "1"}, wantStatus: 418, upstream: true},
		{name: "_rsc query -> worker", method: "GET", target: "/?_rsc=abc", wantStatus: 418, upstream: true},
		{name: "server action -> worker", method: "POST", target: "/", hdr: map[string]string{"Next-Action": "abc"}, wantStatus: 418, upstream: true},
		{name: "plain POST -> worker", method: "POST", target: "/", wantStatus: 418, upstream: true},
		{name: "draft mode -> worker", method: "GET", target: "/", hdr: map[string]string{"Cookie": "__prerender_bypass=1"}, wantStatus: 418, upstream: true},
		{name: "revalidate -> worker", method: "GET", target: "/", hdr: map[string]string{"x-prerender-revalidate": "tok"}, wantStatus: 418, upstream: true},
		{name: "dynamic page -> worker", method: "GET", target: "/dyn/1", wantStatus: 418, upstream: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(up.hits)
			rec := do(h, tc.method, tc.target, tc.hdr)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
			if tc.wantHeader[0] != "" && rec.Header().Get(tc.wantHeader[0]) != tc.wantHeader[1] {
				t.Fatalf("%s = %q, want %q", tc.wantHeader[0], rec.Header().Get(tc.wantHeader[0]), tc.wantHeader[1])
			}
			if got := len(up.hits) > before; got != tc.upstream {
				t.Fatalf("upstream hit = %v, want %v", got, tc.upstream)
			}
		})
	}
}

func TestHandlerETag(t *testing.T) {
	h, _ := newTestHandler(t, HandlerOptions{})
	h.m.Bypass = nil
	etag := do(h, "GET", "/", nil).Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	if rec := do(h, "GET", "/", map[string]string{"If-None-Match": etag}); rec.Code != http.StatusNotModified {
		t.Fatalf("conditional GET = %d, want 304", rec.Code)
	}
}

func TestHandlerDisablePrerendered(t *testing.T) {
	h, up := newTestHandler(t, HandlerOptions{DisablePrerendered: true})
	h.m.Bypass = nil
	if rec := do(h, "GET", "/", nil); rec.Code != 418 || len(up.hits) != 1 {
		t.Fatalf("want worker, got %d (hits %v)", rec.Code, up.hits)
	}
	if rec := do(h, "GET", "/_next/static/chunks/a.js", nil); rec.Code != 200 {
		t.Fatalf("assets must still be served by Go, got %d", rec.Code)
	}
}

// ---- pool (real processes over UDS) ----------------------------------------

func TestPoolStreamsChunksImmediately(t *testing.T) {
	p := startFakePool(t, 1)
	srv := httptest.NewServer(p)
	defer srv.Close()

	start := time.Now()
	res, err := http.Get(srv.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	br := bufio.NewReader(res.Body)
	first := make([]byte, len("<shell>"))
	if _, err := io.ReadFull(br, first); err != nil {
		t.Fatal(err)
	}
	if string(first) != "<shell>" {
		t.Fatalf("first chunk %q", first)
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("first chunk took %s; streaming is being buffered", elapsed)
	}
	rest, _ := io.ReadAll(br)
	if string(rest) != "<suspended/>" {
		t.Fatalf("rest %q", rest)
	}
}

func TestPoolDoesNotFollowRedirectsAndSetsForwardedHeaders(t *testing.T) {
	p := startFakePool(t, 1)
	srv := httptest.NewServer(p)
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	res, err := client.Get(srv.URL + "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusTemporaryRedirect || res.Header.Get("Location") != "/target" {
		t.Fatalf("got %d Location=%q", res.StatusCode, res.Header.Get("Location"))
	}

	req, _ := http.NewRequest("GET", srv.URL+"/echo?x=1", nil)
	req.Host = "shop.example.com"
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	for _, want := range []string{"host=shop.example.com", "xfh=shop.example.com", "xfp=http", "path=/echo?x=1"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("missing %q in %q", want, body)
		}
	}
}

func pidOf(t *testing.T, url string) string {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return strings.Fields(string(b))[0]
}

func TestPoolRoundRobinAndRestart(t *testing.T) {
	p := startFakePool(t, 2)
	srv := httptest.NewServer(p)
	defer srv.Close()

	pids := map[string]bool{}
	for i := 0; i < 6; i++ {
		pids[pidOf(t, srv.URL+"/")] = true
	}
	if len(pids) != 2 {
		t.Fatalf("requests hit %d distinct workers, want 2", len(pids))
	}

	// Kill a worker; the pool must keep serving and bring it back.
	res, err := http.Get(srv.URL + "/crash")
	if err == nil {
		res.Body.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.readyCount() != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("crashed worker not restarted (ready=%d)", p.readyCount())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if res, err := http.Get(srv.URL + "/"); err != nil || res.StatusCode != 200 {
		t.Fatalf("pool unhealthy after restart: %v", err)
	}
}

func TestPoolRetriesWhenWorkerDiesBeforeReaped(t *testing.T) {
	p := startFakePool(t, 2)
	srv := httptest.NewServer(p)
	defer srv.Close()
	// Simulate the window between a worker dying and the supervisor noticing:
	// the worker is still marked ready but its socket refuses connections.
	dead := p.workers[0]
	dead.mu.Lock()
	dead.cmd.Process.Signal(syscall.SIGSTOP)
	dead.mu.Unlock()
	os.Remove(dead.sock)
	defer func() {
		dead.mu.Lock()
		dead.cmd.Process.Signal(syscall.SIGCONT)
		dead.mu.Unlock()
	}()
	for i := 0; i < 6; i++ {
		res, err := http.Get(srv.URL + "/")
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("request %d: %v %v", i, err, res.Status)
		}
		res.Body.Close()
	}
	if dead.ready.Load() {
		t.Fatal("unreachable worker should be marked not ready")
	}
}

func TestPoolCloseRemovesSocketsAndProcesses(t *testing.T) {
	p := startFakePool(t, 2)
	var socks []string
	var procs []*os.Process
	for _, w := range p.workers {
		socks = append(socks, w.sock)
		w.mu.Lock()
		procs = append(procs, w.cmd.Process)
		w.mu.Unlock()
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	for _, s := range socks {
		if _, err := os.Stat(s); !os.IsNotExist(err) {
			t.Fatalf("socket %s still exists", s)
		}
	}
	for _, pr := range procs {
		if err := pr.Signal(syscall.Signal(0)); err == nil {
			t.Fatalf("worker %d still running", pr.Pid)
		}
	}
}

func TestPoolStartupTimeout(t *testing.T) {
	_, err := StartPool(PoolConfig{
		Workers:        1,
		StartupTimeout: 300 * time.Millisecond,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		command: func(ctx context.Context, sock string) *exec.Cmd {
			return exec.CommandContext(ctx, "sleep", "30")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "0/1 workers ready") {
		t.Fatalf("want startup timeout error, got %v", err)
	}
}

// ---- Router integration -----------------------------------------------------

func TestRouterMountStatusAndStreaming(t *testing.T) {
	assets := testAssets()
	m, _ := LoadManifest(assets)
	m.Bypass = nil
	p := startFakePool(t, 1)
	h, err := NewHandler(m, assets, p, HandlerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Router: NewRouter(), Frontend: &Frontend{Handler: h, Manifest: m}}
	app.Router.GET("/api/health", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	app.MountFrontend()
	srv := httptest.NewServer(app.Router)
	defer srv.Close()

	check := func(path string, want int, contains string) {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want || !strings.Contains(string(b), contains) {
			t.Fatalf("%s: %d %q, want %d containing %q", path, res.StatusCode, b, want, contains)
		}
	}
	check("/api/health", 200, `"ok":true`)
	check("/", 200, "<h1>home</h1>")                   // prerendered via NoRoute must not be 404
	check("/_next/static/chunks/a.js", 200, "console") // static via NoRoute
	check("/some/dynamic", 200, "path=/some/dynamic")  // proxied via NoRoute

	// Streaming must survive gin.WrapH.
	start := time.Now()
	res, err := http.Get(srv.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf := make([]byte, len("<shell>"))
	if _, err := io.ReadFull(res.Body, buf); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Fatal("streaming buffered behind Router")
	}
}

func TestNewHTTPServerTimeouts(t *testing.T) {
	t.Run("backend target gets API timeouts", func(t *testing.T) {
		a := &App{Router: NewRouter(), target: TargetBackend}
		srv := a.newHTTPServer(":8080")
		if srv.ReadHeaderTimeout != 10*time.Second || srv.ReadTimeout != 15*time.Second ||
			srv.WriteTimeout != 30*time.Second || srv.IdleTimeout != 60*time.Second {
			t.Fatalf("unexpected backend timeouts: %+v", srv)
		}
	})

	t.Run("selfhost keeps WriteTimeout disabled for streaming", func(t *testing.T) {
		a := &App{Router: NewRouter(), target: TargetSelfhost}
		srv := a.newHTTPServer(":8080")
		if srv.ReadHeaderTimeout != 10*time.Second || srv.IdleTimeout != 60*time.Second {
			t.Fatalf("unexpected selfhost header/idle timeouts: %+v", srv)
		}
		if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
			t.Fatalf("expected 0 ReadTimeout/WriteTimeout on selfhost, got %v/%v", srv.ReadTimeout, srv.WriteTimeout)
		}
	})

	t.Run("explicit config overrides defaults", func(t *testing.T) {
		a := &App{
			Router: NewRouter(),
			target: TargetBackend,
			cfg: Config{
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       20 * time.Second,
				WriteTimeout:      45 * time.Second,
				IdleTimeout:       90 * time.Second,
			},
		}
		srv := a.newHTTPServer(":8080")
		if srv.ReadHeaderTimeout != 5*time.Second || srv.ReadTimeout != 20*time.Second ||
			srv.WriteTimeout != 45*time.Second || srv.IdleTimeout != 90*time.Second {
			t.Fatalf("unexpected overridden timeouts: %+v", srv)
		}
	})
}
