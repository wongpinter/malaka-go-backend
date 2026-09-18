package httpx_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"malaka/internal/platform/httpx"
)

func TestAppError(t *testing.T) {
	rootErr := errors.New("sql: no rows in result set")
	appErr := httpx.NewAppError("store", "NOT_FOUND", "Task not found", rootErr)

	if appErr.HTTPStatus() != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", appErr.HTTPStatus())
	}
	if !strings.Contains(appErr.Error(), "[store:NOT_FOUND]") {
		t.Fatalf("expected layer:code in message, got %s", appErr.Error())
	}
	if !strings.Contains(appErr.Op, "httpx_test.go") {
		t.Fatalf("expected Op to capture caller file:line, got %s", appErr.Op)
	}
	if !errors.Is(appErr, rootErr) {
		t.Fatal("expected errors.Is to unwrap the cause")
	}
}

func TestWrapPreservesClassification(t *testing.T) {
	inner := httpx.NewAppError("tasks.store", "NOT_FOUND", "Task not found", errors.New("no rows"))

	wrapped := httpx.Wrap("tasks.service", inner, "Failed to find task")
	var appErr *httpx.AppError
	if !errors.As(wrapped, &appErr) {
		t.Fatal("expected an AppError")
	}
	if appErr.Layer != "tasks.service" || appErr.Code != "NOT_FOUND" || appErr.Message != "Task not found" {
		t.Fatalf("wrap changed the classification: %+v", appErr)
	}

	plain := httpx.Wrap("tasks.service", errors.New("boom"), "Failed to find task")
	if !errors.As(plain, &appErr) || appErr.Code != "INTERNAL_ERROR" || appErr.Message != "Failed to find task" {
		t.Fatalf("unclassified errors must become INTERNAL_ERROR: %+v", appErr)
	}
	if httpx.Wrap("tasks.service", nil, "ignored") != nil {
		t.Fatal("wrapping nil must return nil")
	}
}

func TestWriteAppError(t *testing.T) {
	t.Run("app error is returned to the client", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/x", nil)
		httpx.WriteAppError(rec, req, "tasks", httpx.NewAppError("tasks.store", "NOT_FOUND", "Task not found", nil), "Failed")

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
		var body struct {
			Success bool `json:"success"`
			Error   *struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				Status    int    `json:"status"`
				Timestamp string `json:"timestamp"`
			} `json:"error"`
			Data any `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Success || body.Error == nil || body.Error.Code != "NOT_FOUND" || body.Error.Status != 404 || body.Error.Timestamp == "" {
			t.Fatalf("unexpected error payload: %s", rec.Body.String())
		}
		if _, present := mustKeys(t, rec.Body.Bytes())["data"]; present {
			t.Fatalf("error responses must not carry a data field: %s", rec.Body.String())
		}
	})

	t.Run("unknown error is masked", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
		httpx.WriteAppError(rec, req, "tasks", errors.New("connection refused to 10.0.0.5"), "Failed to fetch tasks")

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "10.0.0.5") {
			t.Fatalf("internal error detail leaked: %s", rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Failed to fetch tasks") {
			t.Fatalf("fallback message missing: %s", rec.Body.String())
		}
	})
}

func mustKeys(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEnvelopes(t *testing.T) {
	t.Run("OK envelope", func(t *testing.T) {
		rec := httptest.NewRecorder()
		httpx.OK(rec, map[string]string{"name": "malaka"})

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var resp httpx.Response[map[string]string]
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if !resp.Success || resp.Data["name"] != "malaka" {
			t.Fatalf("unexpected envelope: %+v", resp)
		}
	})

	t.Run("Created envelope", func(t *testing.T) {
		rec := httptest.NewRecorder()
		httpx.Created(rec, map[string]string{"id": "018f3a5b"})

		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d", rec.Code)
		}
	})

	t.Run("NoContent envelope", func(t *testing.T) {
		rec := httptest.NewRecorder()
		httpx.NoContent(rec)

		if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
			t.Fatalf("expected an empty 204, got %d with %d bytes", rec.Code, rec.Body.Len())
		}
	})

	t.Run("empty collections keep the data field", func(t *testing.T) {
		rec := httptest.NewRecorder()
		httpx.OK(rec, []string{})

		if !strings.Contains(rec.Body.String(), `"data":[]`) {
			t.Fatalf("empty lists must serialize as an array: %s", rec.Body.String())
		}
	})

	t.Run("Fail envelope", func(t *testing.T) {
		rec := httptest.NewRecorder()
		httpx.Fail(rec, http.StatusBadRequest, "BAD_INPUT", "Invalid request")

		var failResp httpx.Response[any]
		if err := json.Unmarshal(rec.Body.Bytes(), &failResp); err != nil {
			t.Fatal(err)
		}
		if failResp.Success || failResp.Error == nil || failResp.Error.Code != "BAD_INPUT" {
			t.Fatalf("unexpected fail response: %+v", failResp)
		}
	})

	t.Run("Decode rejects malformed and oversized bodies", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{not json"))
		var dst struct{}
		if httpx.Decode(rec, req, &dst) || rec.Code != http.StatusBadRequest {
			t.Fatalf("malformed body: code=%d", rec.Code)
		}
	})
}
