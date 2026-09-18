package httpx

import (
	"errors"
	"log/slog"
	"net/http"

	"malaka/pkg/slogx"
)

func Wrap(layer string, err error, fallback string) error {
	if err == nil {
		return nil
	}
	var appErr *AppError
	if errors.As(err, &appErr) {
		return NewAppError(layer, appErr.Code, appErr.Message, err)
	}
	return NewAppError(layer, "INTERNAL_ERROR", fallback, err)
}

func WriteAppError(w http.ResponseWriter, r *http.Request, scope string, err error, fallback string) {
	var appErr *AppError
	if errors.As(err, &appErr) {
		attrs := []slog.Attr{
			slogx.ErrorType(err),
			slog.String("scope", scope),
			slog.String("layer", appErr.Layer),
			slog.String("code", appErr.Code),
			slog.String("op", appErr.Op),
		}
		if appErr.HTTPStatus() >= http.StatusInternalServerError {
			slogx.Error(r.Context(), "request failed", attrs...)
		} else {
			slogx.Warn(r.Context(), "request rejected", attrs...)
		}
		Fail(w, appErr.HTTPStatus(), appErr.Code, appErr.Message)
		return
	}

	slogx.Error(r.Context(), "request failed", slog.String("scope", scope), slogx.ErrorType(err))
	Fail(w, http.StatusInternalServerError, "INTERNAL_ERROR", fallback)
}
