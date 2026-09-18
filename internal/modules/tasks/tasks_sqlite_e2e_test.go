package tasks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"malaka/internal/modules/tasks"
	"malaka/internal/platform/audit"
	"malaka/internal/platform/db"
	"malaka/internal/platform/mid"
)

func setupSQLiteAPI(t *testing.T) (http.Handler, *db.DB, int64, int64) {
	t.Helper()
	database, err := db.OpenSQLite(t.TempDir()+"/api.db", db.PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	ctx := context.Background()
	const ownerID, assigneeID = 1, 2
	for _, email := range []string{"owner@example.com", "assignee@example.com"} {
		if _, err := database.ExecContext(ctx, `
			INSERT INTO users (public_id, email, name, password_hash, created_at, updated_at)
			VALUES (?, ?, ?, 'hash', ?, ?)
		`, uuid.NewString(), email, email, db.SQLiteTime(time.Now()), db.SQLiteTime(time.Now())); err != nil {
			t.Fatal(err)
		}
	}

	service := tasks.NewService(database, audit.NewWriter(database))
	handler := tasks.NewHandler(service)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux, asUser(ownerID), mid.Idempotency(database, time.Hour))
	stack := mid.Chain(mux, mid.Correlation, mid.Recover, mid.Logger)
	return stack, database, ownerID, assigneeID
}

func asUser(userID int64) mid.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := mid.UserContext{ID: userID, PublicID: uuid.New(), Email: fmt.Sprintf("user-%d@example.com", userID)}
			next.ServeHTTP(w, r.WithContext(mid.WithUser(r.Context(), &user)))
		})
	}
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Status    int    `json:"status"`
	Timestamp string `json:"timestamp"`
}

type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *errorBody      `json:"error"`
}

type taskPayload struct {
	ID         uuid.UUID `json:"id"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	AssigneeID *int64    `json:"assignee_id"`
}

func call(t *testing.T, handler http.Handler, method, path, body string, headers map[string]string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var env envelope
	if raw := rec.Body.Bytes(); len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("%s %s: response is not an envelope: %v (%s)", method, path, err, raw)
		}
	}
	return rec, env
}

func createdTask(t *testing.T, env envelope) taskPayload {
	t.Helper()
	var task taskPayload
	if err := json.Unmarshal(env.Data, &task); err != nil {
		t.Fatalf("decode task: %v (%s)", err, env.Data)
	}
	return task
}

func countRows(t *testing.T, database *db.DB, table string) int {
	t.Helper()
	var n int
	if err := database.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTasksSQLiteE2E_AssignWritesHistoryInOneTransaction(t *testing.T) {
	handler, database, _, assigneeID := setupSQLiteAPI(t)

	_, env := call(t, handler, http.MethodPost, "/api/v1/tasks", `{"title":"Assignment subject","status":"pending"}`, nil)
	task := createdTask(t, env)

	rec, _ := call(t, handler, http.MethodPost, "/api/v1/tasks/"+task.ID.String()+"/assign", fmt.Sprintf(`{"assignee_id":%d}`, assigneeID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("assign: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	if n := countRows(t, database, "task_logs"); n != 1 {
		t.Fatalf("want one task log, got %d", n)
	}
	if n := countRows(t, database, "outbox_events"); n < 2 {
		t.Fatalf("want create and assign outbox events, got %d", n)
	}
	if n := countRows(t, database, "audit_logs"); n != 2 {
		t.Fatalf("want create and assign audit rows, got %d", n)
	}

	var hashChain []struct{ Prev, Curr string }
	rows, err := database.QueryContext(context.Background(), `SELECT prev_hash, curr_hash FROM audit_logs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var pair struct{ Prev, Curr string }
		if err := rows.Scan(&pair.Prev, &pair.Curr); err != nil {
			t.Fatal(err)
		}
		hashChain = append(hashChain, pair)
	}
	if hashChain[0].Prev != audit.GenesisHash {
		t.Fatalf("first audit row must link to the genesis hash, got %q", hashChain[0].Prev)
	}
	if hashChain[1].Prev != hashChain[0].Curr {
		t.Fatalf("audit chain is not linked: %q != %q", hashChain[1].Prev, hashChain[0].Curr)
	}

	if rec, _ := call(t, handler, http.MethodPost, "/api/v1/tasks/"+task.ID.String()+"/assign", `{"assignee_id":999}`, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown assignee: want 422, got %d", rec.Code)
	}
	if rec, _ := call(t, handler, http.MethodPost, "/api/v1/tasks/"+uuid.NewString()+"/assign", `{"assignee_id":1}`, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown task: want 404, got %d", rec.Code)
	}
}

func TestTasksSQLiteE2E_IdempotencySequentialAndConcurrent(t *testing.T) {
	handler, database, _, _ := setupSQLiteAPI(t)
	key := uuid.NewString()
	body := `{"title":"Idempotent task","status":"pending"}`
	headers := map[string]string{"Idempotency-Key": key}

	first, _ := call(t, handler, http.MethodPost, "/api/v1/tasks", body, headers)
	if first.Code != http.StatusCreated {
		t.Fatalf("first request: want 201, got %d (%s)", first.Code, first.Body.String())
	}

	second, _ := call(t, handler, http.MethodPost, "/api/v1/tasks", body, headers)
	if second.Code != http.StatusCreated {
		t.Fatalf("replay: want 201, got %d (%s)", second.Code, second.Body.String())
	}
	if second.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("replay must be marked in the response header")
	}
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatalf("replay body differs:\n%s\n%s", first.Body.String(), second.Body.String())
	}
	if n := countRows(t, database, "tasks"); n != 1 {
		t.Fatalf("replay created a task: %d rows", n)
	}

	if rec, env := call(t, handler, http.MethodPost, "/api/v1/tasks", `{"title":"Different payload","status":"pending"}`, headers); rec.Code != http.StatusUnprocessableEntity || env.Error.Code != "IDEMPOTENCY_KEY_MISMATCH" {
		t.Fatalf("mismatched payload: want 422 IDEMPOTENCY_KEY_MISMATCH, got %d %+v", rec.Code, env.Error)
	}

	concurrentKey := uuid.NewString()
	concurrentBody := `{"title":"Concurrent task","status":"pending"}`
	const goroutines = 10
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		missing int
		bodies  [][]byte
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, _ := call(t, handler, http.MethodPost, "/api/v1/tasks", concurrentBody, map[string]string{"Idempotency-Key": concurrentKey})
			mu.Lock()
			defer mu.Unlock()
			if rec.Code == http.StatusCreated {
				bodies = append(bodies, rec.Body.Bytes())
			} else {
				missing++
			}
		}()
	}
	wg.Wait()

	if len(bodies) == 0 {
		t.Fatal("no concurrent request succeeded")
	}
	for _, b := range bodies {
		if !bytes.Equal(bodies[0], b) {
			t.Fatal("successful concurrent responses differ")
		}
	}
	if n := countRows(t, database, "tasks"); n != 2 {
		t.Fatalf("concurrent reuse must create exactly one extra task, got %d", n)
	}
}
