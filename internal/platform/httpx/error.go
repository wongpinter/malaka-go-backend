package httpx

import (
	"fmt"
	"net/http"
	"runtime"
)

type AppError struct {
	Layer   string `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Op      string `json:"-"`
	Err     error  `json:"-"`
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[%s:%s] %s (%s): %v", e.Layer, e.Code, e.Message, e.Op, e.Err)
	}
	return fmt.Sprintf("[%s:%s] %s (%s)", e.Layer, e.Code, e.Message, e.Op)
}

func (e *AppError) Unwrap() error {
	return e.Err
}

func (e *AppError) HTTPStatus() int {
	switch e.Code {
	case "NOT_FOUND":
		return http.StatusNotFound
	case "VALIDATION", "VALIDATION_ERROR":
		return http.StatusUnprocessableEntity
	case "CONFLICT", "ALREADY_EXISTS", "EMAIL_TAKEN":
		return http.StatusConflict
	case "UNAUTHORIZED", "INVALID_CREDENTIALS":
		return http.StatusUnauthorized
	case "FORBIDDEN":
		return http.StatusForbidden
	case "BAD_REQUEST":
		return http.StatusBadRequest
	case "TOO_MANY_REQUESTS", "RATE_LIMITED":
		return http.StatusTooManyRequests
	case "PAYLOAD_TOO_LARGE":
		return http.StatusRequestEntityTooLarge
	case "INTERNAL_SERVER_ERROR":
		return http.StatusInternalServerError
	case "DB_UNAVAILABLE":
		return http.StatusServiceUnavailable
	case "IDEMPOTENCY_KEY_MISMATCH":
		return http.StatusUnprocessableEntity
	case "CONCURRENT_REQUEST", "IDEMPOTENCY_CONFLICT":
		return http.StatusConflict
	case "INVALID_IDEMPOTENCY_KEY":
		return http.StatusBadRequest
	case "TOKEN_EXPIRED":
		return http.StatusUnauthorized
	default:
		return http.StatusInternalServerError
	}
}

func NewAppError(layer, code, message string, err error) *AppError {
	_, file, line, ok := runtime.Caller(1)
	op := "unknown:0"
	if ok {
		op = fmt.Sprintf("%s:%d", file, line)
	}
	return &AppError{
		Layer:   layer,
		Code:    code,
		Message: message,
		Op:      op,
		Err:     err,
	}
}
