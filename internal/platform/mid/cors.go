package mid

import (
	"fmt"
	"net/http"
	"strings"

	"malaka/internal/config"
	"malaka/internal/platform/httpx"
)

func CORS(cfg config.CORSConfig) Middleware {
	allowedOriginsMap := make(map[string]bool, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		allowedOriginsMap[strings.ToLower(strings.TrimSpace(o))] = true
	}

	methodsStr := strings.Join(cfg.AllowedMethods, ", ")
	headersStr := strings.Join(cfg.AllowedHeaders, ", ")
	maxAgeStr := fmt.Sprintf("%d", cfg.MaxAge)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			originAllowed := false
			if origin != "" {
				lowerOrigin := strings.ToLower(origin)
				originAllowed = allowedOriginsMap[lowerOrigin]
				if allowedOriginsMap["*"] && !cfg.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Origin", "*")
					originAllowed = true
				} else if originAllowed {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
				}

				if originAllowed {
					if cfg.AllowCredentials {
						w.Header().Set("Access-Control-Allow-Credentials", "true")
					}
					w.Header().Set("Access-Control-Expose-Headers", "X-Correlation-ID, X-Request-ID, Idempotency-Replayed")
				}
			}

			if origin != "" && r.Method == http.MethodOptions {
				if !originAllowed {
					httpx.Fail(w, http.StatusForbidden, "CORS_REJECTED", "Origin is not allowed")
					return
				}
				w.Header().Set("Access-Control-Allow-Methods", methodsStr)
				w.Header().Set("Access-Control-Allow-Headers", headersStr)
				w.Header().Set("Access-Control-Max-Age", maxAgeStr)
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
