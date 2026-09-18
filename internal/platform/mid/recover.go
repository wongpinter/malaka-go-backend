package mid

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"malaka/internal/platform/httpx"
)

const maxLoggedStackBytes = 4096

func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				stack := debug.Stack()
				if len(stack) > maxLoggedStackBytes {
					stack = stack[:maxLoggedStackBytes]
				}

				slog.ErrorContext(r.Context(), "panic recovered in http handler",
					"cid", GetCorrelationID(r.Context()),
					"panic_type", fmt.Sprintf("%T", rec),
					"stack", string(stack),
				)

				httpx.Fail(w, http.StatusInternalServerError, "INTERNAL_ERROR", "An internal server error occurred")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
