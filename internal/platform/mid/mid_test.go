package mid_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"malaka/internal/config"
	"malaka/internal/platform/mid"
)

func TestCorrelationMiddleware(t *testing.T) {
	handler := mid.Correlation(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cid := mid.GetCorrelationID(r.Context())
		if cid == "" {
			t.Fatal("expected non-empty correlation ID in context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Request-ID", "custom-request-id-123")

	handler.ServeHTTP(rec, req)

	respCID := rec.Header().Get("X-Correlation-ID")
	if respCID != "custom-request-id-123" {
		t.Fatalf("expected custom-request-id-123, got %s", respCID)
	}
}

func TestCORSMiddleware(t *testing.T) {
	cors := mid.CORS(config.CORSConfig{
		AllowedOrigins:   []string{"http://localhost:3000"},
		AllowedMethods:   []string{"GET", "POST"},
		AllowedHeaders:   []string{"Content-Type"},
		AllowCredentials: true,
		MaxAge:           3600,
	})

	handler := cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	req.Header.Set("Origin", "http://localhost:3000")

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for preflight, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Fatalf("expected origin reflection, got %s", rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("expected credentials true")
	}
}

func TestCORSWildcardWithCredentialsDoesNotReflect(t *testing.T) {
	cors := mid.CORS(config.CORSConfig{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET"},
		AllowedHeaders:   []string{"Content-Type"},
		AllowCredentials: true,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://attacker.example")
	cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("wildcard credentials reflected origin %q", got)
	}
}

func TestRecoverMiddleware(t *testing.T) {
	handler := mid.Recover(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("something went critically wrong")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 status code, got %d", rec.Code)
	}
}

func TestIdempotencyRequiresUserForKeyedMutation(t *testing.T) {
	called := false
	handler := mid.Idempotency(nil, 0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/incoming", nil)
	req.Header.Set("Idempotency-Key", "0191cb88-4395-7182-8495-d6d7e0078235")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without user context, got %d", rec.Code)
	}
	if called {
		t.Fatal("downstream handler must not run without user context")
	}
}

func TestIdempotencyWithoutKeyPassesThrough(t *testing.T) {
	called := false
	handler := mid.Idempotency(nil, 0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/incoming", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("expected unkeyed request passthrough, got status=%d called=%v", rec.Code, called)
	}
}
