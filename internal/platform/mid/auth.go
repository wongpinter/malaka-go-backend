package mid

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"malaka/internal/platform/httpx"
)

type userContextKey struct{}
type jtiContextKey struct{}
type jtiExpContextKey struct{}

type UserContext struct {
	ID        int64
	PublicID  uuid.UUID
	Email     string
	JTI       string
	ExpiresAt time.Time
}

func WithUser(ctx context.Context, u *UserContext) context.Context {
	if u == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, userContextKey{}, u.ID)
	if u.JTI != "" {
		ctx = context.WithValue(ctx, jtiContextKey{}, u.JTI)
	}
	if !u.ExpiresAt.IsZero() {
		ctx = context.WithValue(ctx, jtiExpContextKey{}, u.ExpiresAt)
	}
	return ctx
}

func GetJTI(ctx context.Context) string {
	if v, ok := ctx.Value(jtiContextKey{}).(string); ok {
		return v
	}
	return ""
}

func GetTokenExpiresAt(ctx context.Context) time.Time {
	if v, ok := ctx.Value(jtiExpContextKey{}).(time.Time); ok {
		return v
	}
	return time.Time{}
}

func GetUserID(ctx context.Context) int64 {
	if v, ok := ctx.Value(userContextKey{}).(int64); ok {
		return v
	}
	return 0
}

func WithUserID(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, userContextKey{}, userID)
}

type Authenticator interface {
	Authenticate(ctx context.Context, token string) (*UserContext, error)
}

func Authenticate(auth Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
				httpx.Fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "Missing or invalid authorization header")
				return
			}
			token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))

			userCtx, err := auth.Authenticate(r.Context(), token)
			if err != nil {
				httpx.Fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "Session is invalid or expired")
				return
			}

			ctx := WithUser(r.Context(), userCtx)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
