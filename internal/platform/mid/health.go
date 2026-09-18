package mid

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"malaka/internal/platform/httpx"
	"malaka/pkg/version"
)

type Pinger interface {
	Ping(context.Context) error
}

func HealthLiveness() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, map[string]string{
			"status":  "up",
			"version": version.Version,
		})
	})
}

func HealthReadiness(db Pinger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if db == nil || db.Ping(ctx) != nil {
			httpx.Fail(w, http.StatusServiceUnavailable, "DB_UNAVAILABLE", "Database connection failed")
			return
		}

		httpx.OK(w, map[string]string{
			"status":   "ready",
			"database": "connected",
		})
	})
}

func Metrics(droppedFn func() int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "# HELP event_bus_dropped_total Events dropped due to buffer saturation\n")
		_, _ = fmt.Fprintf(w, "# TYPE event_bus_dropped_total counter\n")
		_, _ = fmt.Fprintf(w, "event_bus_dropped_total %d\n", droppedFn())
	})
}
