package nextgin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"runtime/debug"
	"slices"
	"sync"
)

// CronPathPrefix is the URL path prefix under which all cron job routes are registered.
const CronPathPrefix = "/_nextgin/cron/"

// CronSecretEnv is the environment variable checked by cronAuth for external cron triggers.
const CronSecretEnv = "CRON_SECRET"

// CronJob describes a registered scheduled job.
type CronJob struct {
	Name     string
	Schedule *Schedule
	Path     string
}

var cronNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

var (
	internalTokenOnce sync.Once
	internalTokenVal  string
)

func getInternalToken() string {
	internalTokenOnce.Do(func() {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic(fmt.Sprintf("nextgin: failed to generate internal cron token: %v", err))
		}
		internalTokenVal = hex.EncodeToString(b[:])
	})
	return internalTokenVal
}

// Cron registers a scheduled job on the App's Router under /_nextgin/cron/<name>.
// It panics if name is invalid or already registered, if schedule fails to parse,
// or if handler is nil.
func (a *App) Cron(name, schedule string, handler http.HandlerFunc, mw ...Middleware) {
	if !cronNameRe.MatchString(name) {
		panic(fmt.Sprintf("nextgin: cron job %q: invalid name (must match %s)", name, cronNameRe.String()))
	}
	if a.jobNames[name] {
		panic(fmt.Sprintf("nextgin: cron job %q: duplicate job name", name))
	}
	sched, err := ParseSchedule(schedule)
	if err != nil {
		panic(fmt.Sprintf("nextgin: cron job %q: %v", name, err))
	}
	if handler == nil {
		panic(fmt.Sprintf("nextgin: cron job %q: handler is required", name))
	}

	path := CronPathPrefix + name
	chain := make([]Middleware, 0, 1+len(mw))
	chain = append(chain, cronAuth)
	chain = append(chain, mw...)

	a.Router.GET(path, handler, chain...)
	a.Router.POST(path, handler, chain...)

	if a.jobNames == nil {
		a.jobNames = make(map[string]bool)
	}
	a.jobNames[name] = true
	a.jobs = append(a.jobs, CronJob{
		Name:     name,
		Schedule: sched,
		Path:     path,
	})
}

// CronJobs returns a copy of the registered cron jobs in registration order.
func (a *App) CronJobs() []CronJob {
	return slices.Clone(a.jobs)
}

func cronAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if constantTimeBearerMatch(auth, getInternalToken()) {
			next.ServeHTTP(w, r)
			return
		}
		if secret := os.Getenv(CronSecretEnv); secret != "" && constantTimeBearerMatch(auth, secret) {
			next.ServeHTTP(w, r)
			return
		}
		WriteError(w, http.StatusUnauthorized, "unauthorized")
	})
}

func constantTimeBearerMatch(header, token string) bool {
	expected := "Bearer " + token
	return subtle.ConstantTimeCompare([]byte(header), []byte(expected)) == 1
}

func (a *App) invokeJob(ctx context.Context, job CronJob) (status int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.Path, nil)
	if err != nil {
		return http.StatusInternalServerError
	}
	req.Header.Set("Authorization", "Bearer "+getInternalToken())
	req.Header.Set("X-Nextgin-Cron", job.Name)

	w := &discardResponseWriter{
		header: make(http.Header),
		status: http.StatusOK,
	}
	defer func() {
		if rec := recover(); rec != nil {
			a.cfg.loggerOrDefault().Error("nextgin: cron job panicked",
				"name", job.Name,
				"panic", rec,
				"stack", string(debug.Stack()),
			)
			status = http.StatusInternalServerError
		}
	}()
	a.Router.ServeHTTP(w, req)
	return w.status
}

type discardResponseWriter struct {
	header      http.Header
	status      int
	wroteHeader bool
}

func (w *discardResponseWriter) Header() http.Header {
	return w.header
}

func (w *discardResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return len(b), nil
}

// Flush is a no-op so handlers that type-assert http.Flusher work cleanly.
func (w *discardResponseWriter) Flush() {}

func (w *discardResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.status = statusCode
	w.wroteHeader = true
}
