package tasks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"malaka/internal/modules/tasks"
	"malaka/internal/platform/audit"
	"malaka/internal/platform/httpx"
	"malaka/internal/platform/mid"
	"malaka/internal/platform/testutil"
)

func setupTasksE2E(t *testing.T) (*testutil.TestDatabase, http.Handler, int64, int64) {
	t.Helper()
	tdb := testutil.NewTestDB(t)
	ctx := context.Background()

	var actorID, assigneeID int64
	if err := tdb.DB.Pool.QueryRowContext(ctx, `INSERT INTO iam.users (email, name, password_hash) VALUES ('actor@example.local','Actor','x') RETURNING id`).Scan(&actorID); err != nil {
		t.Fatalf("actor: %v", err)
	}
	if err := tdb.DB.Pool.QueryRowContext(ctx, `INSERT INTO iam.users (email, name, password_hash) VALUES ('assignee@example.local','Assignee','x') RETURNING id`).Scan(&assigneeID); err != nil {
		t.Fatalf("assignee: %v", err)
	}

	auditWriter := audit.NewWriter(tdb.DB.Pool)
	svc := tasks.NewService(tdb.DB, auditWriter)
	hdl := tasks.NewHandler(svc)

	mux := http.NewServeMux()
	authMid := testutil.AuthMiddleware(actorID)
	idemMid := mid.Idempotency(tdb.DB.Pool, 24*time.Hour)
	hdl.RegisterRoutes(mux, authMid, idemMid)

	handler := mid.Chain(mux, mid.Correlation, mid.Recover, mid.Logger)

	return tdb, handler, actorID, assigneeID
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var resp httpx.Response[T]
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode failed %d body %s: %v", rec.Code, rec.Body.String(), err)
	}
	return resp.Data
}

func TestTasks_E2E_CRUD(t *testing.T) {
	if testing.Short() {
		t.Skip("skip e2e")
	}
	_, handler, _, _ := setupTasksE2E(t)

	body := `{"title":"e2e task","description":"desc","status":"pending"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", uuid.NewString())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST expected 201 got %d body %s", rec.Code, rec.Body.String())
	}
	created := decodeBody[tasks.Task](t, rec)
	if created.Title != "e2e task" || created.PublicID == uuid.Nil {
		t.Fatalf("bad created %+v", created)
	}
	id := created.PublicID.String()

	req = httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET list %d %s", rec.Code, rec.Body.String())
	}
	list := decodeBody[[]tasks.Task](t, rec)
	if len(list) != 1 {
		t.Fatalf("expected 1, got %d", len(list))
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+id, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET detail %d %s", rec.Code, rec.Body.String())
	}
	detail := decodeBody[tasks.Task](t, rec)
	if detail.PublicID != created.PublicID {
		t.Fatalf("detail mismatch")
	}

	body = `{"title":"updated","description":"new","status":"in_progress"}`
	req = httptest.NewRequest(http.MethodPut, "/api/v1/tasks/"+id, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT %d %s", rec.Code, rec.Body.String())
	}
	updated := decodeBody[tasks.Task](t, rec)
	if updated.Title != "updated" || updated.Status != "in_progress" {
		t.Fatalf("update not applied %+v", updated)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/tasks/"+id, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+id, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 got %d", rec.Code)
	}
	var errResp httpx.Response[any]
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("err decode %v", err)
	}
	if errResp.Error == nil || errResp.Error.Code != "NOT_FOUND" || errResp.Error.Status != 404 || errResp.Error.Timestamp == "" {
		t.Fatalf("structured error missing fields %+v", errResp.Error)
	}
}

func TestTasks_E2E_Idempotency_Sequential(t *testing.T) {
	if testing.Short() {
		t.Skip("skip e2e")
	}
	tdb, handler, _, _ := setupTasksE2E(t)
	ctx := context.Background()
	body := `{"title":"idem task","description":"x"}`
	key := uuid.NewString()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first 201 got %d %s", rec.Code, rec.Body.String())
	}
	firstBody := rec.Body.String()

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Idempotency-Key", key)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("second replay expected 201 got %d %s", rec2.Code, rec2.Body.String())
	}
	if rec2.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("expected Idempotency-Replayed header")
	}
	if rec2.Body.String() != firstBody {
		t.Fatalf("replay body mismatch\nfirst: %s\nsecond:%s", firstBody, rec2.Body.String())
	}

	var cnt int
	if err := tdb.DB.Pool.QueryRowContext(ctx, `SELECT COUNT(*) FROM public.tasks`).Scan(&cnt); err != nil {
		t.Fatalf("count %v", err)
	}
	if cnt != 1 {
		t.Fatalf("expected 1 task, got %d", cnt)
	}

	req3 := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(`{"title":"different"}`))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("Idempotency-Key", key)
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusUnprocessableEntity {
		t.Fatalf("different payload expected 422 got %d %s", rec3.Code, rec3.Body.String())
	}

	req4 := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req4.Header.Set("Content-Type", "application/json")
	req4.Header.Set("Idempotency-Key", "not-a-uuid")
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusBadRequest {
		t.Fatalf("invalid uuid expected 400 got %d", rec4.Code)
	}
}

func TestTasks_E2E_Idempotency_Concurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("skip e2e")
	}
	tdb, handler, _, _ := setupTasksE2E(t)
	ctx := context.Background()
	body := `{"title":"concurrent"}`
	key := uuid.NewString()
	const n = 10
	var wg sync.WaitGroup
	codes := make([]int, n)
	bodies := make([]string, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", key)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			codes[idx] = rec.Code
			bodies[idx] = rec.Body.String()
		}(i)
	}
	wg.Wait()
	var cnt int
	if err := tdb.DB.Pool.QueryRowContext(ctx, `SELECT COUNT(*) FROM public.tasks`).Scan(&cnt); err != nil {
		t.Fatalf("count %v", err)
	}
	if cnt != 1 {
		t.Fatalf("concurrent expected 1 task, got %d codes %v", cnt, codes)
	}
	created := 0
	for _, c := range codes {
		if c == http.StatusCreated {
			created++
		} else if c != http.StatusConflict {
			t.Fatalf("unexpected code %d", c)
		}
	}
	if created < 1 {
		t.Fatalf("expected at least 1 created, codes %v", codes)
	}
	var first string
	for i, c := range codes {
		if c == http.StatusCreated {
			if first == "" {
				first = bodies[i]
			} else if bodies[i] != first {
				t.Fatalf("201 bodies differ")
			}
		}
	}
}

func TestTasks_E2E_Assign_Transaction(t *testing.T) {
	if testing.Short() {
		t.Skip("skip e2e")
	}
	tdb, handler, _, assigneeID := setupTasksE2E(t)
	ctx := context.Background()

	body := `{"title":"assign me"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", uuid.NewString())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	created := decodeBody[tasks.Task](t, rec)
	id := created.PublicID.String()
	if created.AssigneeID != nil {
		t.Fatalf("initial assignee should be nil")
	}

	assignBody := fmt.Sprintf(`{"assignee_id":%d}`, assigneeID)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+id+"/assign", bytes.NewBufferString(assignBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("assign %d %s", rec.Code, rec.Body.String())
	}
	assigned := decodeBody[tasks.Task](t, rec)
	if assigned.AssigneeID == nil || *assigned.AssigneeID != assigneeID {
		t.Fatalf("assignee not set %+v", assigned)
	}

	var logCnt int
	if err := tdb.DB.Pool.QueryRowContext(ctx, `SELECT COUNT(*) FROM public.task_logs WHERE task_id=$1 AND action='assign'`, assigned.ID).Scan(&logCnt); err != nil {
		var internalID int64
		_ = tdb.DB.Pool.QueryRowContext(ctx, `SELECT id FROM public.tasks WHERE public_id=$1`, created.PublicID).Scan(&internalID)
		_ = tdb.DB.Pool.QueryRowContext(ctx, `SELECT COUNT(*) FROM public.task_logs WHERE task_id=$1`, internalID).Scan(&logCnt)
	}
	if logCnt == 0 {
		if err := tdb.DB.Pool.QueryRowContext(ctx, `SELECT COUNT(*) FROM public.task_logs`).Scan(&logCnt); err != nil {
			t.Fatalf("log count %v", err)
		}
		if logCnt == 0 {
			t.Fatalf("expected task_logs row")
		}
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+id+"/assign", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing assignee expected 422 got %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+uuid.NewString()+"/assign", bytes.NewBufferString(assignBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("not found expected 404 got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/not-a-uuid/assign", bytes.NewBufferString(assignBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid uuid expected 400 got %d", rec.Code)
	}
}

func TestTasks_E2E_StructuredErrors_And_Headers(t *testing.T) {
	if testing.Short() {
		t.Skip("skip e2e")
	}
	_, handler, _, _ := setupTasksE2E(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(`{ bad`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json 400 got %d", rec.Code)
	}
	var errResp httpx.Response[any]
	_ = json.Unmarshal(rec.Body.Bytes(), &errResp)
	if errResp.Error == nil || errResp.Error.Status != 400 || errResp.Error.Timestamp == "" || errResp.Success {
		t.Fatalf("structured error bad %+v body %s", errResp.Error, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(`{"title":""}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("validation 422 got %d %s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &errResp)
	if errResp.Error.Code != "VALIDATION_ERROR" {
		t.Fatalf("expected VALIDATION_ERROR got %+v", errResp.Error)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+uuid.NewString(), nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("404 got %d", rec.Code)
	}
	if rec.Header().Get("X-Request-ID") == "" && rec.Header().Get("X-Correlation-ID") == "" {
		t.Fatalf("expected request id header")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("content-type %s", ct)
	}
	panicMux := http.NewServeMux()
	panicMux.HandleFunc("GET /panic", func(w http.ResponseWriter, r *http.Request) { panic("boom") })
	panicHandler := mid.Chain(panicMux, mid.Correlation, mid.Recover)
	req = httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec = httptest.NewRecorder()
	panicHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic 500 got %d", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &errResp)
	if errResp.Error.Code != "INTERNAL_ERROR" {
		t.Fatalf("panic code %v", errResp.Error)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("stack")) || bytes.Contains(rec.Body.Bytes(), []byte("goroutine")) {
		t.Fatalf("leaked stack in response")
	}
	_ = time.Now()
}

func TestTasks_E2E_List_Filters_And_Pagination(t *testing.T) {
	if testing.Short() {
		t.Skip("skip e2e")
	}
	_, handler, _, _ := setupTasksE2E(t)

	create := func(title, status string) {
		t.Helper()
		body := fmt.Sprintf(`{"title":%q,"description":"","status":%q}`, title, status)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", uuid.NewString())
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %q: expected 201 got %d body %s", title, rec.Code, rec.Body.String())
		}
	}

	create("Alpha report", "pending")
	create("Beta report", "pending")
	create("Gamma note", "done")
	create("Quarterly 100 percent review", "done")

	list := func(query string) []tasks.Task {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks"+query, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("list %q: expected 200 got %d body %s", query, rec.Code, rec.Body.String())
		}
		return decodeBody[[]tasks.Task](t, rec)
	}

	got := list("?status=done")
	if len(got) != 2 {
		t.Fatalf("status=done: expected 2 got %d", len(got))
	}
	for _, task := range got {
		if task.Status != tasks.StatusDone {
			t.Fatalf("status filter leaked %q", task.Status)
		}
	}

	got = list("?search=REPORT")
	if len(got) != 2 {
		t.Fatalf("search=REPORT: expected 2 got %d", len(got))
	}
	for _, task := range got {
		if !strings.Contains(strings.ToLower(task.Title), "report") {
			t.Fatalf("search leaked %q", task.Title)
		}
	}

	got = list("?status=done&search=note")
	if len(got) != 1 || got[0].Title != "Gamma note" {
		t.Fatalf("combined filter: expected Gamma note got %+v", got)
	}

	got = list("?search=100%25")
	if len(got) != 0 {
		t.Fatalf("search=100%%: expected 0 got %d", len(got))
	}
	got = list("?search=percent")
	if len(got) != 1 || got[0].Title != "Quarterly 100 percent review" {
		t.Fatalf("search=percent: expected review got %+v", got)
	}

	page1 := list("?limit=2&page=1")
	page2 := list("?limit=2&page=2")
	page3 := list("?limit=2&page=3")
	if len(page1) != 2 || len(page2) != 2 || len(page3) != 0 {
		t.Fatalf("pages: got %d/%d/%d", len(page1), len(page2), len(page3))
	}
	seen := make(map[string]bool, 4)
	for _, task := range append(page1, page2...) {
		if seen[task.PublicID.String()] {
			t.Fatalf("duplicate task across pages: %s", task.PublicID)
		}
		seen[task.PublicID.String()] = true
	}

	byOffset := list("?limit=2&offset=2")
	if len(byOffset) != 2 || byOffset[0].PublicID != page2[0].PublicID {
		t.Fatalf("offset=2 should equal page=2: %+v vs %+v", byOffset, page2)
	}

	for _, query := range []string{
		"?status=bogus",
		"?limit=0",
		"?limit=101",
		"?limit=abc",
		"?page=0",
		"?page=abc",
		"?offset=-1",
		"?search=" + strings.Repeat("x", 256),
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks"+query, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: expected 422 got %d body %s", query, rec.Code, rec.Body.String())
		}
	}
}

func TestTasks_E2E_Statuses(t *testing.T) {
	if testing.Short() {
		t.Skip("skip e2e")
	}
	_, handler, _, _ := setupTasksE2E(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/statuses", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d body %s", rec.Code, rec.Body.String())
	}
	got := decodeBody[[]tasks.StatusOption](t, rec)
	want := []tasks.StatusOption{
		{Value: tasks.StatusPending, Label: "Pending"},
		{Value: tasks.StatusInProgress, Label: "In Progress"},
		{Value: tasks.StatusDone, Label: "Done"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses mismatch: got %+v want %+v", got, want)
	}

	for _, opt := range got {
		if !tasks.IsValidStatus(opt.Value) {
			t.Fatalf("status option %q is not a valid status", opt.Value)
		}
	}
}
