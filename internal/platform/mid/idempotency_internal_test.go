package mid

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memClaimStore struct {
	mu      sync.Mutex
	nextID  int64
	records map[string]*claimRecord
}

func newMemClaimStore() *memClaimStore {
	return &memClaimStore{records: make(map[string]*claimRecord)}
}

func scopeID(s claimScope) string {
	return s.Key + "|" + fmt.Sprint(s.UserID) + "|" + s.Path + "|" + s.Method
}

func (m *memClaimStore) Claim(_ context.Context, c claimRequest) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := scopeID(c.Scope)
	if _, exists := m.records[k]; exists {
		return 0, false, nil
	}
	m.nextID++
	m.records[k] = &claimRecord{ID: m.nextID, Status: "processing", RequestHash: c.Hash, ExpiresAt: c.ExpiresAt}
	return m.nextID, true, nil
}

func (m *memClaimStore) Get(_ context.Context, s claimScope) (*claimRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[scopeID(s)]
	if !ok {
		return nil, nil
	}
	cp := *rec
	return &cp, nil
}

func (m *memClaimStore) ReclaimExpired(_ context.Context, id int64, c claimRequest) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.findByID(id)
	if rec == nil || rec.ExpiresAt.After(time.Now()) {
		return 0, false, nil
	}
	rec.Status = "processing"
	rec.RequestHash = c.Hash
	rec.ExpiresAt = c.ExpiresAt
	rec.ResponseStatus = 0
	rec.ResponseBody = nil
	rec.ResponseContentType = ""
	return rec.ID, true, nil
}

func (m *memClaimStore) ReclaimFailed(_ context.Context, id int64, c claimRequest) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.findByID(id)
	if rec == nil || rec.Status != "failed" || rec.RequestHash != c.Hash {
		return 0, false, nil
	}
	rec.Status = "processing"
	rec.ExpiresAt = c.ExpiresAt
	return rec.ID, true, nil
}

func (m *memClaimStore) Finalize(_ context.Context, id int64, res claimResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.findByID(id)
	if rec == nil {
		return fmt.Errorf("unknown claim %d", id)
	}
	if res.Failed {
		rec.Status = "failed"
		return nil
	}
	rec.Status = "completed"
	rec.ResponseStatus = res.StatusCode
	rec.ResponseBody = append([]byte(nil), res.Body...)
	rec.ResponseContentType = res.ContentType
	rec.ExpiresAt = res.ExpiresAt
	return nil
}

func (m *memClaimStore) findByID(id int64) *claimRecord {
	for _, rec := range m.records {
		if rec.ID == id {
			return rec
		}
	}
	return nil
}

func countingHandler(counter *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := counter.Add(1)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"success":true,"data":{"id":"task-%d"}}`, n)
	})
}

func keyedPost(key, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func authed(req *http.Request, userID int64) *http.Request {
	return req.WithContext(WithUserID(req.Context(), userID))
}

func buildHandler(store claimStore, ttl time.Duration, next http.Handler) http.Handler {
	return idempotency(store, ttl)(next)
}

func TestIdempotency_SequentialReplay(t *testing.T) {
	store := newMemClaimStore()
	var counter atomic.Int64
	h := buildHandler(store, 24*time.Hour, countingHandler(&counter))

	key := uuid.NewString()
	body := `{"title":"task A"}`

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, authed(keyedPost(key, body), 7))

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, authed(keyedPost(key, body), 7))

	if rec1.Code != http.StatusCreated || rec2.Code != http.StatusCreated {
		t.Fatalf("expected 201 both, got %d and %d", rec1.Code, rec2.Code)
	}
	if rec1.Body.String() != rec2.Body.String() {
		t.Fatalf("sequential replay body mismatch:\nfirst: %s\nsecond: %s", rec1.Body.String(), rec2.Body.String())
	}
	if rec2.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal("expected Idempotency-Replayed header on second request")
	}
	if rec2.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("replay lost original Content-Type: %q", rec2.Header().Get("Content-Type"))
	}
	if counter.Load() != 1 {
		t.Fatalf("expected exactly 1 creation, got %d", counter.Load())
	}
}

func TestIdempotency_ConcurrentSingleCreation(t *testing.T) {
	store := newMemClaimStore()
	var counter atomic.Int64
	h := buildHandler(store, 24*time.Hour, countingHandler(&counter))

	key := uuid.NewString()
	body := `{"title":"concurrent task"}`

	const goroutines = 20
	codes := make([]int, goroutines)
	bodies := make([]string, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, authed(keyedPost(key, body), 7))
			codes[idx] = rec.Code
			bodies[idx] = rec.Body.String()
		}(i)
	}
	wg.Wait()

	if counter.Load() != 1 {
		t.Fatalf("expected exactly 1 creation, got %d (codes=%v)", counter.Load(), codes)
	}
	created := 0
	for i, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict, http.StatusUnprocessableEntity:
		default:
			t.Fatalf("unexpected status %d body %s", c, bodies[i])
		}
	}
	if created < 1 {
		t.Fatalf("expected at least one 201, got codes %v", codes)
	}
	first := ""
	for i, c := range codes {
		if c != http.StatusCreated {
			continue
		}
		if first == "" {
			first = bodies[i]
		} else if bodies[i] != first {
			t.Fatalf("concurrent 201 bodies differ: %q vs %q", first, bodies[i])
		}
	}
}

func TestIdempotency_DifferentPayloadRejected(t *testing.T) {
	store := newMemClaimStore()
	var counter atomic.Int64
	h := buildHandler(store, 24*time.Hour, countingHandler(&counter))

	key := uuid.NewString()
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, authed(keyedPost(key, `{"title":"a"}`), 7))
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first should be 201, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, authed(keyedPost(key, `{"title":"b"}`), 7))
	if rec2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("different payload should be 422, got %d body %s", rec2.Code, rec2.Body.String())
	}
	if counter.Load() != 1 {
		t.Fatalf("handler must not run twice, got %d creations", counter.Load())
	}
}

func TestIdempotency_InvalidKeyRejected(t *testing.T) {
	store := newMemClaimStore()
	var counter atomic.Int64
	h := buildHandler(store, 24*time.Hour, countingHandler(&counter))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authed(keyedPost("not-a-uuid", `{"title":"x"}`), 7))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid UUID should be 400, got %d", rec.Code)
	}
	if counter.Load() != 0 {
		t.Fatal("handler must not be called on invalid key")
	}
}

func TestIdempotency_FailedThenRetry(t *testing.T) {
	store := newMemClaimStore()
	var calls atomic.Int64
	failing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			httpxInternalFail(w)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"task-ok"}}`))
	})
	h := buildHandler(store, 24*time.Hour, failing)

	key := uuid.NewString()
	body := `{"title":"retry me"}`

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, authed(keyedPost(key, body), 7))
	if rec1.Code != http.StatusInternalServerError {
		t.Fatalf("first attempt should be 500, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, authed(keyedPost(key, body), 7))
	if rec2.Code != http.StatusCreated {
		t.Fatalf("retry after failure should be 201, got %d body %s", rec2.Code, rec2.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("expected handler to run twice (fail then succeed), got %d", calls.Load())
	}
}

func TestIdempotency_ExpiredKeyIsReclaimable(t *testing.T) {
	store := newMemClaimStore()
	var counter atomic.Int64
	h := buildHandler(store, 50*time.Millisecond, countingHandler(&counter))

	key := uuid.NewString()
	body := `{"title":"ttl window"}`

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, authed(keyedPost(key, body), 7))
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first should be 201, got %d", rec1.Code)
	}

	time.Sleep(60 * time.Millisecond)

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, authed(keyedPost(key, body), 7))
	if rec2.Code != http.StatusCreated {
		t.Fatalf("expired key should be re-claimable with 201, got %d", rec2.Code)
	}
	if rec2.Header().Get("Idempotency-Replayed") == "true" {
		t.Fatal("expired replay must not be marked as replayed")
	}
	if counter.Load() != 2 {
		t.Fatalf("expired key must create again, got %d creations", counter.Load())
	}
}

func TestIdempotency_ScopeIsolation(t *testing.T) {
	store := newMemClaimStore()
	var counter atomic.Int64
	h := buildHandler(store, 24*time.Hour, countingHandler(&counter))

	key := uuid.NewString()
	body := `{"title":"scope"}`

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, authed(keyedPost(key, body), 7))
	if rec1.Code != http.StatusCreated {
		t.Fatalf("user 7 first: expected 201, got %d", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, authed(keyedPost(key, body), 8))
	if rec2.Code != http.StatusCreated {
		t.Fatalf("user 8 same key: expected 201 (scoped per user), got %d", rec2.Code)
	}
	if rec2.Header().Get("Idempotency-Replayed") == "true" {
		t.Fatal("cross-user request must not replay another user's response")
	}
	if counter.Load() != 2 {
		t.Fatalf("expected 2 creations, got %d", counter.Load())
	}
}

func httpxInternalFail(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"success":false,"error":{"code":"INTERNAL_ERROR"}}`))
}

func TestIdempotency_PanicMarksClaimFailed(t *testing.T) {
	store := newMemClaimStore()
	var executions int
	h := buildHandler(store, time.Minute, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		executions++
		if executions == 1 {
			panic("boom")
		}
		w.WriteHeader(http.StatusCreated)
	}))

	const key = "11111111-1111-4111-8111-111111111111"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", nil)
	req.Header.Set("Idempotency-Key", key)
	req = authed(req, 7)
	rec := httptest.NewRecorder()
	func() {
		defer func() {
			v := recover()
			if v == nil {
				t.Fatal("expected the panic to propagate to the recovery middleware")
			}
			if v != "boom" {
				t.Fatalf("unexpected panic value %v", v)
			}
		}()
		h.ServeHTTP(rec, req)
	}()

	if executions != 1 {
		t.Fatalf("want exactly one execution before the panic, got %d", executions)
	}
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", nil)
	req2.Header.Set("Idempotency-Key", key)
	req2 = authed(req2, 7)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if executions != 2 {
		t.Fatalf("a failed claim must be re-claimed and re-executed, executions=%d (status %d)", executions, rec2.Code)
	}
	if rec2.Code != http.StatusCreated {
		t.Fatalf("want the retry to run the handler (201), got %d", rec2.Code)
	}
}
