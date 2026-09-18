package mid

import (
	"net/http"
	"strconv"
)

type SecurityHeadersConfig struct {
	CSP             string
	HSTSMaxAgeSecs  int
	FrameOptions    string
	ReferrerPolicy  string
	PermissionsPol  string
	ContentTypeOpts string
}

func DefaultSecurityHeaders() SecurityHeadersConfig {
	return SecurityHeadersConfig{
		CSP:             "default-src 'none'; frame-ancestors 'none'",
		HSTSMaxAgeSecs:  31536000,
		FrameOptions:    "DENY",
		ReferrerPolicy:  "strict-origin-when-cross-origin",
		PermissionsPol:  "camera=(), microphone=(), geolocation=()",
		ContentTypeOpts: "nosniff",
	}
}

func SecurityHeaders(cfg SecurityHeadersConfig) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			if cfg.CSP != "" {
				h.Set("Content-Security-Policy", cfg.CSP)
			}
			if cfg.HSTSMaxAgeSecs > 0 && (r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https") {
				h.Set("Strict-Transport-Security", "max-age="+strconv.Itoa(cfg.HSTSMaxAgeSecs)+"; includeSubDomains")
			}
			if cfg.FrameOptions != "" {
				h.Set("X-Frame-Options", cfg.FrameOptions)
			}
			if cfg.ReferrerPolicy != "" {
				h.Set("Referrer-Policy", cfg.ReferrerPolicy)
			}
			if cfg.PermissionsPol != "" {
				h.Set("Permissions-Policy", cfg.PermissionsPol)
			}
			if cfg.ContentTypeOpts != "" {
				h.Set("X-Content-Type-Options", cfg.ContentTypeOpts)
			}
			next.ServeHTTP(w, r)
		})
	}
}
