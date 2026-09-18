package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type Response[T any] struct {
	Success bool       `json:"success"`
	Data    T          `json:"data"`
	Error   *ErrorBody `json:"error,omitempty"`
}

type errorResponse struct {
	Success bool       `json:"success"`
	Error   *ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code      string       `json:"code"`
	Message   string       `json:"message"`
	Fields    []FieldError `json:"fields,omitempty"`
	Status    int          `json:"status,omitempty"`
	Timestamp string       `json:"timestamp,omitempty"`
}

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

func Decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	err := json.NewDecoder(r.Body).Decode(dst)
	if err == nil {
		return true
	}
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		Fail(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body exceeds the allowed size")
		return false
	}
	Fail(w, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON payload")
	return false
}

func JSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func OK[T any](w http.ResponseWriter, data T) {
	JSON(w, http.StatusOK, Response[T]{
		Success: true,
		Data:    data,
	})
}

func Created[T any](w http.ResponseWriter, data T) {
	JSON(w, http.StatusCreated, Response[T]{
		Success: true,
		Data:    data,
	})
}

func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

func Fail(w http.ResponseWriter, status int, code, message string) {
	JSON(w, status, errorResponse{
		Success: false,
		Error: &ErrorBody{
			Code:      code,
			Message:   message,
			Status:    status,
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		},
	})
}

func FailFromErr(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if appErr, ok := err.(*AppError); ok {
		Fail(w, appErr.HTTPStatus(), appErr.Code, appErr.Message)
		return
	}
	Fail(w, http.StatusInternalServerError, "INTERNAL_ERROR", "An internal server error occurred")
}
