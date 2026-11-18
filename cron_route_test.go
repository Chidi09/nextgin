package nextgin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestApp() *App {
	return &App{
		Router: NewRouter(),
		cfg:    Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
	}
}

func TestDetectTarget(t *testing.T) {
	cases := []struct {
		name       string
		targetEnv  string
		vercelEnv  string
		want       TargetKind
		wantErrStr string
	}{
		{name: "default selfhost", want: TargetSelfhost},
		{name: "explicit selfhost", targetEnv: "selfhost", want: TargetSelfhost},
		{name: "explicit vercel", targetEnv: "vercel", want: TargetVercel},
		{name: "explicit cloudflare", targetEnv: "cloudflare", want: TargetCloudflare},
		{name: "explicit backend", targetEnv: "backend", want: TargetBackend},
		{name: "vercel env", vercelEnv: "1", want: TargetVercel},
		{name: "NEXTGIN_TARGET overrides VERCEL", targetEnv: "selfhost", vercelEnv: "1", want: TargetSelfhost},
		{name: "VERCEL non-1 ignored", vercelEnv: "0", want: TargetSelfhost},
		{name: "invalid NEXTGIN_TARGET", targetEnv: "fly", wantErrStr: `nextgin: unknown NEXTGIN_TARGET "fly"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(TargetEnv, tc.targetEnv)
			t.Setenv("VERCEL", tc.vercelEnv)

			got, err := DetectTarget()
			if tc.wantErrStr != "" {
				if err == nil || err.Error() != tc.wantErrStr {
					t.Fatalf("DetectTarget() err = %v, want %q", err, tc.wantErrStr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DetectTarget() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("DetectTarget() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCronRegistrationPanics(t *testing.T) {
	noop := func(w http.ResponseWriter, r *http.Request) {}
	cases := []struct {
		name     string
		jobName  string
		schedule string
		handler  http.HandlerFunc
		setup    func(a *App)
	}{
		{name: "uppercase", jobName: "Bad", schedule: "* * * * *", handler: noop},
		{name: "empty", jobName: "", schedule: "* * * * *", handler: noop},
		{name: "leading hyphen", jobName: "-x", schedule: "* * * * *", handler: noop},
		{name: "64 chars", jobName: strings.Repeat("a", 64), schedule: "* * * * *", handler: noop},
		{
			name:     "duplicate name",
			jobName:  "dup",
			schedule: "* * * * *",
			handler:  noop,
			setup:    func(a *App) { a.Cron("dup", "* * * * *", noop) },
		},
		{name: "bad schedule", jobName: "bad_sched", schedule: "not a cron", handler: noop},
		{name: "nil handler", jobName: "no_handler", schedule: "* * * * *", handler: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp()
			if tc.setup != nil {
				tc.setup(app)
			}
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic, got nil")
				}
				msg, ok := r.(string)
				if !ok {
					t.Fatalf("expected string panic, got %T (%v)", r, r)
				}
				if !strings.HasPrefix(msg, "nextgin: cron job") {
					t.Fatalf("panic %q does not start with %q", msg, "nextgin: cron job")
				}
				if !strings.Contains(msg, tc.jobName) {
					t.Fatalf("panic %q does not contain job name %q", msg, tc.jobName)
				}
			}()
			app.Cron(tc.jobName, tc.schedule, tc.handler)
		})
	}
}

func TestCronJobsCopyAndOrder(t *testing.T) {
	noop := func(w http.ResponseWriter, r *http.Request) {}
	app := newTestApp()
	app.Cron("first-job", "0 1 * * *", noop)
	app.Cron("second_job", "0 2 * * *", noop)

	jobs := app.CronJobs()
	if len(jobs) != 2 || jobs[0].Name != "first-job" || jobs[1].Name != "second_job" {
		t.Fatalf("unexpected jobs: %+v", jobs)
	}
	jobs[0].Name = "mutated"
	if app.CronJobs()[0].Name != "first-job" {
		t.Fatal("CronJobs() did not return a defensive copy")
	}
}

func TestCronAuth(t *testing.T) {
	app := newTestApp()
	app.Cron("cleanup", "*/5 * * * *", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	reqDo := func(method, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/_nextgin/cron/cleanup", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		app.Router.ServeHTTP(rec, req)
		return rec
	}

	assertUnauthorized := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid json body %q: %v", rec.Body.String(), err)
		}
		if body["error"] != "unauthorized" {
			t.Fatalf("body = %v, want error=unauthorized", body)
		}
	}

	t.Run("no header", func(t *testing.T) {
		assertUnauthorized(t, reqDo("POST", ""))
	})

	t.Run("wrong token", func(t *testing.T) {
		assertUnauthorized(t, reqDo("POST", "Bearer wrong"))
	})

	t.Run("internal token GET and POST", func(t *testing.T) {
		tok := "Bearer " + getInternalToken()
		for _, method := range []string{"GET", "POST"} {
			rec := reqDo(method, tok)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s with internal token: status = %d, want 200", method, rec.Code)
			}
		}
	})

	t.Run("CRON_SECRET match", func(t *testing.T) {
		t.Setenv(CronSecretEnv, "s3")
		rec := reqDo("GET", "Bearer s3")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("empty CRON_SECRET rejects empty bearer", func(t *testing.T) {
		t.Setenv(CronSecretEnv, "")
		assertUnauthorized(t, reqDo("POST", "Bearer "))
	})
}

func TestInvokeJob(t *testing.T) {
	app := newTestApp()
	var seenHeader string
	app.Cron("job-204", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
		seenHeader = r.Header.Get("X-Nextgin-Cron")
		w.WriteHeader(http.StatusNoContent)
	})
	app.Cron("job-500", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	jobs := app.CronJobs()
	if status := app.invokeJob(context.Background(), jobs[0]); status != http.StatusNoContent {
		t.Fatalf("invokeJob(job-204) = %d, want 204", status)
	}
	if seenHeader != "job-204" {
		t.Fatalf("X-Nextgin-Cron = %q, want %q", seenHeader, "job-204")
	}
	if status := app.invokeJob(context.Background(), jobs[1]); status != http.StatusInternalServerError {
		t.Fatalf("invokeJob(job-500) = %d, want 500", status)
	}
}

func TestExport(t *testing.T) {
	noop := func(w http.ResponseWriter, r *http.Request) {}
	app := newTestApp()
	app.Router.POST("/api/todos", noop)
	app.Router.GET("/api/todos", noop)
	app.Router.GET("/api/health", noop)
	app.Router.GET("/_nextgin/internal", noop)
	app.Cron("weekday-report", "0 9 * * 1-5", noop)

	exp := app.Export()
	if exp.Version != ExportVersion {
		t.Fatalf("Version = %d, want %d", exp.Version, ExportVersion)
	}
	wantRoutes := []ExportRoute{
		{Method: "GET", Path: "/api/health"},
		{Method: "GET", Path: "/api/todos"},
		{Method: "POST", Path: "/api/todos"},
	}
	if len(exp.Routes) != len(wantRoutes) {
		t.Fatalf("Routes = %+v, want %+v", exp.Routes, wantRoutes)
	}
	for i := range wantRoutes {
		if exp.Routes[i] != wantRoutes[i] {
			t.Fatalf("Routes[%d] = %+v, want %+v", i, exp.Routes[i], wantRoutes[i])
		}
	}
	if len(exp.Crons) != 1 {
		t.Fatalf("Crons = %+v, want 1 item", exp.Crons)
	}
	gotCron := exp.Crons[0]
	if gotCron.Name != "weekday-report" ||
		gotCron.Schedule != "0 9 * * 1-5" ||
		gotCron.Cloudflare != "0 9 * * 2,3,4,5,6" ||
		gotCron.Path != "/_nextgin/cron/weekday-report" ||
		gotCron.RunsPerDayMax != 1 {
		t.Fatalf("unexpected Cron export: %+v", gotCron)
	}
}

func TestExportEmptyEncodesArrays(t *testing.T) {
	app := newTestApp()
	var buf bytes.Buffer
	if err := app.writeExport(&buf); err != nil {
		t.Fatalf("writeExport: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"routes": []`) {
		t.Fatalf("expected empty routes array in:\n%s", out)
	}
	if !strings.Contains(out, `"crons": []`) {
		t.Fatalf("expected empty crons array in:\n%s", out)
	}
}

func TestSchedulerRunsAndOverlap(t *testing.T) {
	origNow := schedulerNow
	origNext := schedulerNext
	t.Cleanup(func() {
		schedulerNow = origNow
		schedulerNext = origNext
	})

	schedulerNext = func(_ *Schedule, after time.Time) time.Time {
		return after.Add(30 * time.Millisecond)
	}

	t.Run("fires repeatedly", func(t *testing.T) {
		app := newTestApp()
		var runs atomic.Int32
		app.Cron("tick", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
			runs.Add(1)
			w.WriteHeader(http.StatusOK)
		})

		ctx, cancel := context.WithCancel(context.Background())
		wait := app.startScheduler(ctx)
		time.Sleep(200 * time.Millisecond)
		cancel()
		wait(time.Second)

		if got := runs.Load(); got < 2 {
			t.Fatalf("runs = %d, want >= 2", got)
		}
	})

	t.Run("skips overlapping runs", func(t *testing.T) {
		app := newTestApp()
		var entered atomic.Int32
		started := make(chan struct{})
		release := make(chan struct{})

		app.Cron("slow", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
			if entered.Add(1) == 1 {
				close(started)
			}
			<-release
			w.WriteHeader(http.StatusOK)
		})

		ctx, cancel := context.WithCancel(context.Background())
		wait := app.startScheduler(ctx)

		<-started
		// Wait long enough for several 30ms ticks to be skipped while blocked.
		time.Sleep(150 * time.Millisecond)
		if got := entered.Load(); got != 1 {
			close(release)
			cancel()
			wait(time.Second)
			t.Fatalf("entered = %d while blocked, want 1", got)
		}

		cancel()
		close(release)
		waitDone := make(chan struct{})
		go func() {
			wait(time.Second)
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-time.After(time.Second):
			t.Fatal("wait did not return after releasing blocked job")
		}
	})
}

func TestSchedulerNoDoubleFireOnClockSlew(t *testing.T) {
	origNow := schedulerNow
	origNext := schedulerNext
	t.Cleanup(func() {
		schedulerNow = origNow
		schedulerNext = origNext
	})

	minute := time.Date(2026, 1, 1, 12, 1, 0, 0, time.UTC)
	var calls atomic.Int32
	schedulerNow = func() time.Time {
		if calls.Add(1) <= 2 {
			return minute.Add(-10 * time.Millisecond)
		}
		return minute.Add(-1 * time.Millisecond)
	}
	schedulerNext = func(s *Schedule, after time.Time) time.Time { return s.Next(after) }

	app := newTestApp()
	var runs atomic.Int32
	firstRun := make(chan struct{})
	app.Cron("slew", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
		if runs.Add(1) == 1 {
			close(firstRun)
		}
		w.WriteHeader(http.StatusOK)
	})

	ctx, cancel := context.WithCancel(context.Background())
	wait := app.startScheduler(ctx)

	select {
	case <-firstRun:
	case <-time.After(time.Second):
		cancel()
		wait(time.Second)
		t.Fatal("timed out waiting for first cron fire")
	}

	// Give the loop plenty of time to re-fire if it were vulnerable to 1ms slew.
	time.Sleep(50 * time.Millisecond)
	cancel()
	wait(time.Second)

	if got := runs.Load(); got != 1 {
		t.Fatalf("runs = %d, want 1 (same minute fired more than once)", got)
	}
}

func TestNewNonSelfhostAndExportSkipsDisk(t *testing.T) {
	t.Run("export mode", func(t *testing.T) {
		t.Setenv(ExportEnv, "1")
		app, err := New("/nonexistent-dir-for-export-test", Config{Router: NewRouter()})
		if err != nil {
			t.Fatalf("New in export mode failed: %v", err)
		}
		if app.Frontend != nil {
			t.Fatal("expected nil Frontend in export mode")
		}
		app.MountFrontend()
		if err := app.Close(); err != nil {
			t.Fatalf("Close() failed: %v", err)
		}
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("Handler() status = %d, want 404", rec.Code)
		}
	})

	t.Run("vercel target", func(t *testing.T) {
		t.Setenv(TargetEnv, string(TargetVercel))
		app, err := New("/nonexistent-dir-for-vercel-test", Config{Router: NewRouter()})
		if err != nil {
			t.Fatalf("New in vercel target failed: %v", err)
		}
		if app.Frontend != nil {
			t.Fatal("expected nil Frontend on vercel target")
		}
		if err := app.Close(); err != nil {
			t.Fatalf("Close() failed: %v", err)
		}
	})

	t.Run("cloudflare target on non-wasm", func(t *testing.T) {
		t.Setenv(TargetEnv, string(TargetCloudflare))
		_, err := New("/nonexistent", Config{Router: NewRouter()})
		if err == nil || !strings.Contains(err.Error(), "GOOS=js GOARCH=wasm") {
			t.Fatalf("expected cloudflare wasm error, got %v", err)
		}
	})
}

// Regression: cron handlers that flush (streaming / SSE) must not panic on the
// synthetic in-process writer.
func TestInvokeJobStreamingHandler(t *testing.T) {
	a := &App{Router: NewRouter()}
	a.Cron("stream", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
		w.(http.Flusher).Flush()
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if st := a.invokeJob(ctx, a.CronJobs()[0]); st != http.StatusOK {
		t.Fatalf("status = %d, want 200", st)
	}
}

// Regression: the CLI reads the export from NEXTGIN_EXPORT_FILE so that stdout
// noise cannot corrupt it.
func TestExportToFile(t *testing.T) {
	noop := func(w http.ResponseWriter, r *http.Request) {}
	a := &App{Router: NewRouter()}
	a.Router.GET("/api/x", noop)
	a.Cron("nightly", "0 3 * * *", noop)
	path := t.TempDir() + "/export.json"
	if err := a.exportTo(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Export
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("export file is not JSON: %v\n%s", err, b)
	}
	if len(got.Routes) != 1 || len(got.Crons) != 1 || got.Crons[0].Name != "nightly" {
		t.Fatalf("unexpected export: %+v", got)
	}
}

func TestSchedulerCancelsRunningJobOnShutdown(t *testing.T) {
	origNow := schedulerNow
	origNext := schedulerNext
	t.Cleanup(func() {
		schedulerNow = origNow
		schedulerNext = origNext
	})
	schedulerNext = func(_ *Schedule, after time.Time) time.Time {
		return after.Add(15 * time.Millisecond)
	}

	app := newTestApp()
	started := make(chan struct{})
	canceled := make(chan struct{})
	app.Cron("cancellable", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-r.Context().Done()
		close(canceled)
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	ctx, cancel := context.WithCancel(context.Background())
	wait := app.startScheduler(ctx)

	<-started
	cancel()

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("running cron job did not receive shutdown context cancellation")
	}
	wait(time.Second)
}

type fakeCronLocker struct {
	tryLock func(ctx context.Context, jobName string, ttl time.Duration) (func(), bool, error)
}

func (f fakeCronLocker) TryLock(ctx context.Context, jobName string, ttl time.Duration) (func(), bool, error) {
	return f.tryLock(ctx, jobName, ttl)
}

func TestSchedulerCronLocker(t *testing.T) {
	origNow := schedulerNow
	origNext := schedulerNext
	t.Cleanup(func() {
		schedulerNow = origNow
		schedulerNext = origNext
	})
	schedulerNext = func(_ *Schedule, after time.Time) time.Time {
		return after.Add(20 * time.Millisecond)
	}

	t.Run("acquires and unlocks", func(t *testing.T) {
		var unlocked atomic.Int32
		var runs atomic.Int32
		app := newTestApp()
		app.cfg.CronLocker = fakeCronLocker{
			tryLock: func(_ context.Context, name string, _ time.Duration) (func(), bool, error) {
				if name != "locked" {
					t.Errorf("jobName = %q, want locked", name)
				}
				return func() { unlocked.Add(1) }, true, nil
			},
		}
		app.Cron("locked", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
			runs.Add(1)
			w.WriteHeader(http.StatusOK)
		})

		ctx, cancel := context.WithCancel(context.Background())
		wait := app.startScheduler(ctx)
		time.Sleep(80 * time.Millisecond)
		cancel()
		wait(time.Second)

		if runs.Load() < 1 || unlocked.Load() != runs.Load() {
			t.Fatalf("runs=%d unlocked=%d, want equal and >=1", runs.Load(), unlocked.Load())
		}
	})

	t.Run("skips when contended or errored", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			ok   bool
			err  error
		}{
			{"contended", false, nil},
			{"error", false, errors.New("redis down")},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var runs atomic.Int32
				app := newTestApp()
				app.cfg.CronLocker = fakeCronLocker{
					tryLock: func(_ context.Context, _ string, _ time.Duration) (func(), bool, error) {
						return nil, tc.ok, tc.err
					},
				}
				app.Cron("skipme", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
					runs.Add(1)
					w.WriteHeader(http.StatusOK)
				})

				ctx, cancel := context.WithCancel(context.Background())
				wait := app.startScheduler(ctx)
				time.Sleep(70 * time.Millisecond)
				cancel()
				wait(time.Second)

				if runs.Load() != 0 {
					t.Fatalf("runs = %d, want 0", runs.Load())
				}
			})
		}
	})
}

func TestInvokeJobRecoversPanic(t *testing.T) {
	app := newTestApp()
	app.Cron("panicky", "* * * * *", func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	if st := app.invokeJob(context.Background(), app.CronJobs()[0]); st != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", st)
	}
}

func TestBuiltInHealthEndpoint(t *testing.T) {
	t.Setenv(TargetEnv, string(TargetBackend))
	app, err := New("/nonexistent", Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	rec := httptest.NewRecorder()
	app.Router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, HealthPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", HealthPath, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("body = %q, want status ok", rec.Body.String())
	}
	if got := app.Export().Routes; len(got) != 0 {
		t.Fatalf("health route should not appear in Export().Routes, got %+v", got)
	}
}
