package mid_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"malaka/internal/platform/mid"
)

type mockAuthenticator struct {
	userCtx *mid.UserContext
	err     error
}

func (m *mockAuthenticator) Authenticate(ctx context.Context, token string) (*mid.UserContext, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.userCtx, nil
}

func TestAuthenticate_Middleware(t *testing.T) {
	t.Run("missing authorization header", func(t *testing.T) {
		auth := &mockAuthenticator{}
		handler := mid.Authenticate(auth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("invalid token error", func(t *testing.T) {
		auth := &mockAuthenticator{err: errors.New("token expired")}
		handler := mid.Authenticate(auth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer invalid-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("non bearer scheme returns 401", func(t *testing.T) {
		auth := &mockAuthenticator{
			userCtx: &mid.UserContext{ID: 101},
		}
		handler := mid.Authenticate(auth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("valid token sets context", func(t *testing.T) {
		expiresAt := time.Now().Add(15 * time.Minute).UTC().Truncate(time.Second)
		auth := &mockAuthenticator{
			userCtx: &mid.UserContext{
				ID:        101,
				PublicID:  uuid.New(),
				Email:     "sugeng@example.com",
				JTI:       "jti-abc-123",
				ExpiresAt: expiresAt,
			},
		}

		var (
			capturedUID    int64
			capturedJTI    string
			capturedExpiry time.Time
		)
		handler := mid.Authenticate(auth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedUID = mid.GetUserID(r.Context())
			capturedJTI = mid.GetJTI(r.Context())
			capturedExpiry = mid.GetTokenExpiresAt(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer valid-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if capturedUID != 101 {
			t.Errorf("expected user ID 101, got %d", capturedUID)
		}
		if capturedJTI != "jti-abc-123" {
			t.Errorf("expected JTI 'jti-abc-123', got %q", capturedJTI)
		}
		if !capturedExpiry.Equal(expiresAt) {
			t.Errorf("expected expiry %v, got %v", expiresAt, capturedExpiry)
		}
	})
}
