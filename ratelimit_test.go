package nextgin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimitPanicsOnInvalidConfig(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rps   float64
		burst int
	}{
		{"zero rps", 0, 1},
		{"negative rps", -1, 1},
		{"zero burst", 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic, got nil")
				}
			}()
			RateLimit(tc.rps, tc.burst)
		})
	}
}

func TestRateLimitBurstRefillAndHeaders(t *testing.T) {
	origNow := rateLimitNow
	t.Cleanup(func() { rateLimitNow = origNow })

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rateLimitNow = func() time.Time { return now }

	r := NewRouter()
	r.POST("/api/login", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, RateLimit(1, 2))

	doReq := func(ip string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/login", nil)
		req.RemoteAddr = ip + ":12345"
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	if rec := doReq("10.0.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("req 1 = %d, want 200", rec.Code)
	}
	if rec := doReq("10.0.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("req 2 = %d, want 200", rec.Code)
	}
	rec3 := doReq("10.0.0.1")
	if rec3.Code != http.StatusTooManyRequests {
		t.Fatalf("req 3 = %d, want 429", rec3.Code)
	}
	if rec3.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q, want 1", rec3.Header().Get("Retry-After"))
	}

	// Different IP is unaffected.
	if rec := doReq("10.0.0.2"); rec.Code != http.StatusOK {
		t.Fatalf("other IP = %d, want 200", rec.Code)
	}

	// Advance clock by 1s -> 1 token refilled.
	now = now.Add(time.Second)
	if rec := doReq("10.0.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("after refill = %d, want 200", rec.Code)
	}
}

func TestRateLimitTrustForwardedAndEviction(t *testing.T) {
	origNow := rateLimitNow
	t.Cleanup(func() { rateLimitNow = origNow })

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rateLimitNow = func() time.Time { return now }

	r := NewRouter()
	r.GET("/api/cart", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, RateLimitWithConfig(RateLimitConfig{
		RPS:                   1,
		Burst:                 1,
		TrustForwardedHeaders: true,
		MaxEntries:            2,
	}))

	send := func(xff string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/cart", nil)
		req.RemoteAddr = "127.0.0.1:9999"
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := send("203.0.113.1, 10.0.0.1"); code != http.StatusOK {
		t.Fatalf("ip1 first = %d, want 200", code)
	}
	if code := send("203.0.113.1"); code != http.StatusTooManyRequests {
		t.Fatalf("ip1 second = %d, want 429", code)
	}
	now = now.Add(10 * time.Millisecond)
	if code := send("203.0.113.2"); code != http.StatusOK {
		t.Fatalf("ip2 first = %d, want 200", code)
	}
	now = now.Add(10 * time.Millisecond)
	// Exceeds MaxEntries=2 -> oldest (ip1) gets evicted.
	if code := send("203.0.113.3"); code != http.StatusOK {
		t.Fatalf("ip3 first = %d, want 200", code)
	}
}
