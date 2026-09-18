package mid

import (
	"log/slog"
	"net/http"
	"time"
)

type responseRecorder struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int64
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytesWritten += int64(n)
	return n, err
}

func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		next.ServeHTTP(rec, r)

		duration := time.Since(start)
		cid := GetCorrelationID(r.Context())

		attrs := []slog.Attr{
			slog.String("request_id", cid),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.statusCode),
			slog.Int64("bytes", rec.bytesWritten),
			slog.Int64("duration_ms", duration.Milliseconds()),
			slog.String("remote_ip", r.RemoteAddr),
		}

		if rec.statusCode >= 500 {
			slog.LogAttrs(r.Context(), slog.LevelError, "http request error", attrs...)
		} else if rec.statusCode >= 400 {
			slog.LogAttrs(r.Context(), slog.LevelWarn, "http request client error", attrs...)
		} else {
			slog.LogAttrs(r.Context(), slog.LevelInfo, "http request completed", attrs...)
		}
	})
}
