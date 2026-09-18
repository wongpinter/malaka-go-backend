package tasks

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"malaka/internal/platform/httpx"
	"malaka/internal/platform/mid"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) RegisterRoutes(mux *http.ServeMux, middlewares ...mid.Middleware) {
	mux.Handle("GET /api/v1/tasks", mid.Chain(http.HandlerFunc(h.List), middlewares...))
	mux.Handle("GET /api/v1/tasks/statuses", mid.Chain(http.HandlerFunc(h.Statuses), middlewares...))
	mux.Handle("POST /api/v1/tasks", mid.Chain(http.HandlerFunc(h.Create), middlewares...))
	mux.Handle("GET /api/v1/tasks/{id}", mid.Chain(http.HandlerFunc(h.Get), middlewares...))
	mux.Handle("PUT /api/v1/tasks/{id}", mid.Chain(http.HandlerFunc(h.Update), middlewares...))
	mux.Handle("DELETE /api/v1/tasks/{id}", mid.Chain(http.HandlerFunc(h.Delete), middlewares...))
	mux.Handle("POST /api/v1/tasks/{id}/assign", mid.Chain(http.HandlerFunc(h.Assign), middlewares...))
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	filter, ok := parseListQuery(w, r)
	if !ok {
		return
	}
	tasks, err := h.service.List(r.Context(), mid.GetUserID(r.Context()), filter)
	if err != nil {
		writeServiceError(w, r, err, "Failed to fetch tasks")
		return
	}
	httpx.OK(w, tasks)
}

func (h *Handler) Statuses(w http.ResponseWriter, r *http.Request) {
	httpx.OK(w, StatusOptions())
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	task, err := h.service.Get(r.Context(), mid.GetUserID(r.Context()), id)
	if err != nil {
		writeServiceError(w, r, err, "Failed to fetch task")
		return
	}
	httpx.OK(w, task)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var input Input
	if !decodeInput(w, r, &input) {
		return
	}
	task, err := h.service.Create(r.Context(), mid.GetUserID(r.Context()), input)
	if err != nil {
		writeServiceError(w, r, err, "Failed to create task")
		return
	}
	httpx.Created(w, task)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var input Input
	if !decodeInput(w, r, &input) {
		return
	}
	task, err := h.service.Update(r.Context(), mid.GetUserID(r.Context()), id, input)
	if err != nil {
		writeServiceError(w, r, err, "Failed to update task")
		return
	}
	httpx.OK(w, task)
}

func (h *Handler) Assign(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var body struct {
		AssigneeID int64 `json:"assignee_id"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	if body.AssigneeID == 0 {
		httpx.Fail(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "Assignee ID is required")
		return
	}
	task, err := h.service.Assign(r.Context(), mid.GetUserID(r.Context()), id, body.AssigneeID)
	if err != nil {
		writeServiceError(w, r, err, "Failed to assign task")
		return
	}
	httpx.OK(w, task)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := h.service.Delete(r.Context(), mid.GetUserID(r.Context()), id); err != nil {
		writeServiceError(w, r, err, "Failed to delete task")
		return
	}
	httpx.NoContent(w)
}

func parseID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, "INVALID_ID", "Invalid ID format")
		return uuid.Nil, false
	}
	return id, true
}

func decodeInput(w http.ResponseWriter, r *http.Request, input *Input) bool {
	return httpx.Decode(w, r, input)
}

func parseListQuery(w http.ResponseWriter, r *http.Request) (ListFilter, bool) {
	q := ListFilter{Limit: DefaultListLimit}
	params := r.URL.Query()

	if status := strings.TrimSpace(params.Get("status")); status != "" {
		if !IsValidStatus(status) {
			httpx.Fail(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "status must be pending, in_progress, or done")
			return q, false
		}
		q.Status = status
	}

	if search := strings.TrimSpace(params.Get("search")); search != "" {
		if len(search) > MaxSearchLength {
			httpx.Fail(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "search must be 255 characters or fewer")
			return q, false
		}
		q.Search = search
	}

	if raw := params.Get("limit"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || v < 1 || v > MaxListLimit {
			httpx.Fail(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "limit must be between 1 and 100")
			return q, false
		}
		q.Limit = int32(v)
	}

	if raw := params.Get("page"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v < 1 {
			httpx.Fail(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "page must be 1 or greater")
			return q, false
		}
		offset := (v - 1) * int64(q.Limit)
		if offset > MaxListOffset {
			httpx.Fail(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "page is too deep")
			return q, false
		}
		q.Offset = int32(offset)
		return q, true
	}

	if raw := params.Get("offset"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || v < 0 {
			httpx.Fail(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "offset must be 0 or greater")
			return q, false
		}
		if v > MaxListOffset {
			httpx.Fail(w, http.StatusUnprocessableEntity, "VALIDATION_ERROR", "offset is too large")
			return q, false
		}
		q.Offset = int32(v)
	}
	return q, true
}

func writeServiceError(w http.ResponseWriter, r *http.Request, err error, fallback string) {
	httpx.WriteAppError(w, r, "tasks", err, fallback)
}
