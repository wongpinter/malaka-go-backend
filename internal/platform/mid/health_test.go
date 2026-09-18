package mid

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthLiveness(t *testing.T) {
	rec := httptest.NewRecorder()
	HealthLiveness().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHealthReadiness_NoDB(t *testing.T) {
	rec := httptest.NewRecorder()
	HealthReadiness(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for a missing database, got %d", rec.Code)
	}
}

type failingPinger struct{}

func (failingPinger) Ping(context.Context) error { return errors.New("down") }
