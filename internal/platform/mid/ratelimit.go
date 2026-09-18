package mid

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"malaka/internal/platform/httpx"
)

type RateLimitConfig struct {
	Rate       float64
	Burst      int
	CleanupTL  time.Duration
	TrustProxy bool
	SkipPaths  []string
}

type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	lastFill time.Time
}

type rateLimiter struct {
	cfg       RateLimitConfig
	buckets   sync.Map
	skipPaths []string
	once      sync.Once
}

func RateLimit(cfg RateLimitConfig) Middleware {
	if cfg.Rate <= 0 {
		cfg.Rate = 10
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 20
	}
	if cfg.CleanupTL <= 0 {
		cfg.CleanupTL = 10 * time.Minute
	}
	rl := &rateLimiter{cfg: cfg, skipPaths: cfg.SkipPaths}
	rl.startCleanup()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range rl.skipPaths {
				if strings.HasPrefix(r.URL.Path, p) {
					next.ServeHTTP(w, r)
					return
				}
			}
			ip := clientIP(r, rl.cfg.TrustProxy)
			if !rl.allow(ip) {
				w.Header().Set("Retry-After", "1")
				httpx.Fail(w, http.StatusTooManyRequests, "RATE_LIMITED", "Request rate limit exceeded. Try again shortly.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (rl *rateLimiter) allow(ip string) bool {
	now := time.Now()
	raw, _ := rl.buckets.LoadOrStore(ip, &tokenBucket{tokens: float64(rl.cfg.Burst), lastFill: now})
	b := raw.(*tokenBucket)
	b.mu.Lock()
	defer b.mu.Unlock()
	elapsed := now.Sub(b.lastFill).Seconds()
	b.tokens = min(float64(rl.cfg.Burst), b.tokens+elapsed*rl.cfg.Rate)
	b.lastFill = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (rl *rateLimiter) startCleanup() {
	rl.once.Do(func() {
		go func() {
			ticker := time.NewTicker(rl.cfg.CleanupTL)
			defer ticker.Stop()
			for range ticker.C {
				cutoff := time.Now().Add(-rl.cfg.CleanupTL)
				rl.buckets.Range(func(k, v any) bool {
					b := v.(*tokenBucket)
					b.mu.Lock()
					stale := b.lastFill.Before(cutoff)
					b.mu.Unlock()
					if stale {
						rl.buckets.Delete(k)
					}
					return true
				})
			}
		}()
	})
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if idx := strings.Index(xff, ","); idx >= 0 {
				return strings.TrimSpace(xff[:idx])
			}
			return strings.TrimSpace(xff)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
