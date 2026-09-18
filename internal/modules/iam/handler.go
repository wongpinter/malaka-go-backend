package iam

import (
	"net/http"
	"strings"

	"malaka/internal/platform/httpx"
	"malaka/internal/platform/mid"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) RegisterRoutes(mux *http.ServeMux, auth ...mid.Middleware) {
	mux.HandleFunc("POST /api/v1/auth/register", h.Register)
	mux.HandleFunc("POST /api/v1/auth/login", h.Login)
	mux.Handle("POST /api/v1/auth/logout", mid.Chain(http.HandlerFunc(h.Logout), auth...))
	mux.Handle("GET /api/v1/auth/me", mid.Chain(http.HandlerFunc(h.Me), auth...))
}

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var input RegisterInput
	if !httpx.Decode(w, r, &input) {
		return
	}
	resp, err := h.svc.Register(r.Context(), input)
	if err != nil {
		writeAuthError(w, r, err, "Unable to register user")
		return
	}
	httpx.Created(w, resp)
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var input LoginInput
	if !httpx.Decode(w, r, &input) {
		return
	}
	resp, err := h.svc.Login(r.Context(), input)
	if err != nil {
		writeAuthError(w, r, err, "Unable to log in")
		return
	}
	httpx.OK(w, resp)
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		httpx.Fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "Missing or invalid authorization header")
		return
	}
	token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	if token == "" {
		httpx.Fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "Missing or invalid authorization header")
		return
	}
	if err := h.svc.Logout(r.Context(), HashToken(token)); err != nil {
		writeAuthError(w, r, err, "Unable to log out")
		return
	}
	httpx.OK(w, map[string]string{"message": "Logged out"})
}

func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	userID := mid.GetUserID(r.Context())
	if userID == 0 {
		httpx.Fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required")
		return
	}
	user, err := h.svc.Me(r.Context(), userID)
	if err != nil {
		writeAuthError(w, r, err, "Unable to fetch user")
		return
	}
	user.PasswordHash = ""
	httpx.OK(w, user)
}

func writeAuthError(w http.ResponseWriter, r *http.Request, err error, fallback string) {
	httpx.WriteAppError(w, r, "auth", err, fallback)
}
