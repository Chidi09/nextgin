package nextgin

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultRateLimitMaxEntries = 10000

// RateLimitConfig configures the per-IP token-bucket rate limiter middleware.
type RateLimitConfig struct {
	// RPS is the sustained requests-per-second allowed per client IP. Must be > 0.
	RPS float64
	// Burst is the maximum token bucket size per client IP. Must be >= 1.
	Burst int
	// TrustForwardedHeaders enables reading client IPs from X-Forwarded-For /
	// X-Real-IP. Enable only behind a trusted reverse proxy.
	TrustForwardedHeaders bool
	// MaxEntries caps the number of tracked client IPs in memory (default 10000).
	MaxEntries int
}

var rateLimitNow = time.Now

// RateLimit returns a per-IP token-bucket rate limiter middleware with safe defaults.
// It panics if rps <= 0 or burst < 1.
func RateLimit(rps float64, burst int) Middleware {
	return RateLimitWithConfig(RateLimitConfig{RPS: rps, Burst: burst})
}

// RateLimitWithConfig returns a per-IP token-bucket rate limiter middleware.
// It panics if cfg.RPS <= 0 or cfg.Burst < 1.
func RateLimitWithConfig(cfg RateLimitConfig) Middleware {
	if cfg.RPS <= 0 || math.IsNaN(cfg.RPS) || math.IsInf(cfg.RPS, 0) {
		panic("nextgin: RateLimit requires RPS > 0")
	}
	if cfg.Burst < 1 {
		panic("nextgin: RateLimit requires Burst >= 1")
	}
	maxEntries := cfg.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultRateLimitMaxEntries
	}

	// Evict entries that have been idle long enough to be fully refilled (at least 1m).
	idleTTL := time.Duration(float64(cfg.Burst)/cfg.RPS*float64(time.Second)) * 2
	if idleTTL < time.Minute {
		idleTTL = time.Minute
	}

	type bucket struct {
		tokens   float64
		lastSeen time.Time
	}

	var (
		mu        sync.Mutex
		buckets   = make(map[string]*bucket)
		lastSweep = rateLimitNow()
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r, cfg.TrustForwardedHeaders)
			now := rateLimitNow()

			mu.Lock()
			if now.Sub(lastSweep) >= idleTTL || len(buckets) >= maxEntries {
				for k, b := range buckets {
					if now.Sub(b.lastSeen) >= idleTTL {
						delete(buckets, k)
					}
				}
				lastSweep = now
			}

			b, ok := buckets[ip]
			if !ok {
				// If still at capacity after sweeping stale entries, evict one oldest entry.
				if len(buckets) >= maxEntries {
					var oldestKey string
					var oldestTime time.Time
					for k, v := range buckets {
						if oldestKey == "" || v.lastSeen.Before(oldestTime) {
							oldestKey = k
							oldestTime = v.lastSeen
						}
					}
					if oldestKey != "" {
						delete(buckets, oldestKey)
					}
				}
				b = &bucket{tokens: float64(cfg.Burst), lastSeen: now}
				buckets[ip] = b
			} else {
				elapsed := now.Sub(b.lastSeen).Seconds()
				if elapsed > 0 {
					b.tokens = math.Min(float64(cfg.Burst), b.tokens+elapsed*cfg.RPS)
				}
				b.lastSeen = now
			}

			if b.tokens < 1 {
				waitSec := int(math.Ceil((1 - b.tokens) / cfg.RPS))
				if waitSec < 1 {
					waitSec = 1
				}
				mu.Unlock()
				w.Header().Set("Retry-After", strconv.Itoa(waitSec))
				WriteError(w, http.StatusTooManyRequests, "too many requests")
				return
			}
			b.tokens--
			mu.Unlock()

			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request, trustForwarded bool) string {
	if trustForwarded {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first, _, _ := strings.Cut(xff, ","); strings.TrimSpace(first) != "" {
				return strings.TrimSpace(first)
			}
		}
		if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
			return xri
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}
