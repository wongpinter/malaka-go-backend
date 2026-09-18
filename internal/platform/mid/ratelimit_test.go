package mid_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"malaka/internal/platform/mid"
)

func TestRateLimit_TokenBucket(t *testing.T) {
	handler := mid.RateLimit(mid.RateLimitConfig{Rate: 1, Burst: 3})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ok, limited := 0, 0
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		switch rec.Code {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	if ok != 3 {
		t.Errorf("expected 3 OK from burst 3, got %d", ok)
	}
	if limited != 2 {
		t.Errorf("expected 2 rate-limited, got %d", limited)
	}
}

func TestRateLimit_SkipPaths(t *testing.T) {
	handler := mid.RateLimit(mid.RateLimitConfig{Rate: 0.001, Burst: 1, SkipPaths: []string{"/healthz", "/metrics"}})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.RemoteAddr = "10.0.0.3:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("skip path /healthz should always pass, got %d on iter %d", rec.Code, i)
		}
	}
}

func TestRateLimit_TrustProxyOff(t *testing.T) {
	handler := mid.RateLimit(mid.RateLimitConfig{Rate: 0.001, Burst: 2, TrustProxy: false})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ok, limited := 0, 0
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.4:1234"
		req.Header.Set("X-Forwarded-For", "1.2.3."+string(rune('0'+i)))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		switch rec.Code {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	if ok != 2 || limited != 3 {
		t.Errorf("XFF must be ignored when TrustProxy=false, want ok=2 limited=3 got ok=%d limited=%d", ok, limited)
	}
}

func TestSecurityHeaders(t *testing.T) {
	handler := mid.SecurityHeaders(mid.DefaultSecurityHeaders())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Content-Security-Policy"); got == "" {
		t.Error("missing CSP header")
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("expected X-Frame-Options=DENY, got %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("expected X-Content-Type-Options=nosniff, got %q", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got == "" {
		t.Error("missing Referrer-Policy header")
	}
}

func TestSecurityHeaders_HSTSOnlyOnTLS(t *testing.T) {
	handler := mid.SecurityHeaders(mid.DefaultSecurityHeaders())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS must be empty on plain HTTP, got %q", got)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("X-Forwarded-Proto", "https")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if got := rec2.Header().Get("Strict-Transport-Security"); got == "" {
		t.Error("HSTS must be set when X-Forwarded-Proto=https")
	}
}
