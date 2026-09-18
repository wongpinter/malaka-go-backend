package mid

import (
	"context"
	"log/slog"
	"net/http"

	"malaka/pkg/id"
)

type correlationKey struct{}

const (
	HeaderCorrelationID = "X-Correlation-ID"
	HeaderRequestID     = "X-Request-ID"
	maxCorrelationBytes = 64
)

func validCorrelationID(cid string) bool {
	if cid == "" || len(cid) > maxCorrelationBytes {
		return false
	}
	for i := 0; i < len(cid); i++ {
		c := cid[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func Correlation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cid := r.Header.Get(HeaderCorrelationID)
		if !validCorrelationID(cid) {
			cid = r.Header.Get(HeaderRequestID)
		}
		if !validCorrelationID(cid) {
			cid = id.New().String()
		}

		w.Header().Set(HeaderCorrelationID, cid)
		w.Header().Set(HeaderRequestID, cid)

		ctx := context.WithValue(r.Context(), correlationKey{}, cid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func GetCorrelationID(ctx context.Context) string {
	if v, ok := ctx.Value(correlationKey{}).(string); ok {
		return v
	}
	return ""
}

func WithCorrelationID(ctx context.Context, cid string) context.Context {
	return context.WithValue(ctx, correlationKey{}, cid)
}

func LogAttrs(ctx context.Context) []slog.Attr {
	if ctx == nil {
		return nil
	}
	var attrs []slog.Attr
	if cid := GetCorrelationID(ctx); cid != "" {
		attrs = append(attrs, slog.String("cid", cid))
	}
	if userID := GetUserID(ctx); userID != 0 {
		attrs = append(attrs, slog.Int64("user_id", userID))
	}
	return attrs
}
