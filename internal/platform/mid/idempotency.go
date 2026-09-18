package mid

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	platformdb "malaka/internal/platform/db"
	"malaka/internal/platform/httpx"
)

type captureResponseWriter struct {
	http.ResponseWriter
	statusCode int
	body       bytes.Buffer
}

func (w *captureResponseWriter) WriteHeader(code int) {
	if w.statusCode != 0 {
		return
	}
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *captureResponseWriter) Write(b []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

const fallbackContentType = "application/json; charset=utf-8"

type claimScope struct {
	Key    string
	UserID int64
	Path   string
	Method string
}

type claimRequest struct {
	Scope     claimScope
	Hash      string
	ExpiresAt time.Time
}

type claimRecord struct {
	ID                  int64
	Status              string
	RequestHash         string
	ResponseStatus      int
	ResponseBody        []byte
	ResponseContentType string
	ExpiresAt           time.Time
}

type claimResult struct {
	Failed      bool
	StatusCode  int
	Body        []byte
	ContentType string
	ExpiresAt   time.Time
}

type claimStore interface {
	Claim(ctx context.Context, c claimRequest) (id int64, ok bool, err error)
	Get(ctx context.Context, s claimScope) (*claimRecord, error)
	ReclaimExpired(ctx context.Context, id int64, c claimRequest) (claimedID int64, ok bool, err error)
	ReclaimFailed(ctx context.Context, id int64, c claimRequest) (claimedID int64, ok bool, err error)
	Finalize(ctx context.Context, id int64, res claimResult) error
}

func Idempotency(executor platformdb.Executor, ttl time.Duration) Middleware {
	return idempotency(dbClaimStore{db: executor, driver: platformdb.DriverOf(executor)}, ttl)
}

func idempotency(store claimStore, ttl time.Duration) Middleware {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("Idempotency-Key")
			if key == "" || (r.Method != http.MethodPost && r.Method != http.MethodPatch && r.Method != http.MethodPut) {
				next.ServeHTTP(w, r)
				return
			}

			if _, err := uuid.Parse(key); err != nil {
				httpx.Fail(w, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key must be a valid UUID")
				return
			}

			userID := GetUserID(r.Context())
			if userID == 0 {
				httpx.Fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required for idempotent requests")
				return
			}

			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				var maxErr *http.MaxBytesError
				if errors.As(err, &maxErr) {
					httpx.Fail(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body exceeds the allowed size")
					return
				}
				httpx.Fail(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Failed to read request body")
				return
			}
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

			hasher := sha256.New()
			hasher.Write([]byte(r.Method))
			hasher.Write([]byte("\n"))
			hasher.Write([]byte(r.URL.RequestURI()))
			hasher.Write([]byte("\n"))
			hasher.Write(bodyBytes)
			reqHash := hex.EncodeToString(hasher.Sum(nil))

			expiresAt := time.Now().Add(ttl)
			claim := claimRequest{
				Scope:     claimScope{Key: key, UserID: userID, Path: r.URL.Path, Method: r.Method},
				Hash:      reqHash,
				ExpiresAt: expiresAt,
			}

			claimedID, fresh, err := store.Claim(r.Context(), claim)
			if err != nil {
				slogErrorType(r.Context(), "idempotency claim failed", err)
				httpx.Fail(w, http.StatusInternalServerError, "INTERNAL_ERROR", "An internal server error occurred")
				return
			}

			if !fresh {
				id, state, err := resolveConflict(w, r, store, claim)
				if err != nil {
					slogErrorType(r.Context(), "idempotency resolution failed", err)
					httpx.Fail(w, http.StatusInternalServerError, "INTERNAL_ERROR", "An internal server error occurred")
					return
				}
				switch state {
				case resolveHandled:
					return
				case resolveVanished:
					claimedID, fresh, err = store.Claim(r.Context(), claim)
					if err != nil {
						slogErrorType(r.Context(), "idempotency claim failed", err)
						httpx.Fail(w, http.StatusInternalServerError, "INTERNAL_ERROR", "An internal server error occurred")
						return
					}
					if !fresh {
						httpx.Fail(w, http.StatusConflict, "CONCURRENT_REQUEST", "A request with this idempotency key is currently processing")
						return
					}
				case resolveReclaimed:
					claimedID = id
				}
			}

			recorder := &captureResponseWriter{ResponseWriter: w}
			func() {
				defer func() {
					if v := recover(); v != nil {
						finalCtx := context.WithoutCancel(r.Context())
						if err := store.Finalize(finalCtx, claimedID, claimResult{Failed: true, ExpiresAt: expiresAt}); err != nil {
							slogErrorType(finalCtx, "idempotency finalize failed", err)
						}
						panic(v)
					}
				}()
				next.ServeHTTP(recorder, r)
			}()
			if recorder.statusCode == 0 {
				recorder.statusCode = http.StatusOK
			}

			finalCtx := context.WithoutCancel(r.Context())
			res := claimResult{
				Failed:      recorder.statusCode >= 500,
				StatusCode:  recorder.statusCode,
				Body:        recorder.body.Bytes(),
				ContentType: recorder.Header().Get("Content-Type"),
				ExpiresAt:   expiresAt,
			}
			if err := store.Finalize(finalCtx, claimedID, res); err != nil {
				slogErrorType(finalCtx, "idempotency finalize failed", err)
			}
		})
	}
}

type resolveState int

const (
	resolveHandled resolveState = iota
	resolveVanished
	resolveReclaimed
)

func resolveConflict(w http.ResponseWriter, r *http.Request, store claimStore, c claimRequest) (int64, resolveState, error) {
	rec, err := store.Get(r.Context(), c.Scope)
	if err != nil {
		return 0, resolveHandled, err
	}
	if rec == nil {
		return 0, resolveVanished, nil
	}

	if !rec.ExpiresAt.After(time.Now()) {
		claimedID, ok, err := store.ReclaimExpired(r.Context(), rec.ID, c)
		if err != nil {
			return 0, resolveHandled, err
		}
		if !ok {
			httpx.Fail(w, http.StatusConflict, "CONCURRENT_REQUEST", "A request with this idempotency key is currently processing")
			return 0, resolveHandled, nil
		}
		return claimedID, resolveReclaimed, nil
	}

	if rec.RequestHash != c.Hash {
		httpx.Fail(w, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_MISMATCH", "Idempotency key was previously used with a different payload")
		return 0, resolveHandled, nil
	}
	if rec.Status == "processing" {
		httpx.Fail(w, http.StatusConflict, "CONCURRENT_REQUEST", "A request with this idempotency key is currently processing")
		return 0, resolveHandled, nil
	}
	if rec.Status == "completed" && rec.ResponseStatus != 0 {
		ct := fallbackContentType
		if rec.ResponseContentType != "" {
			ct = rec.ResponseContentType
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Idempotency-Replayed", "true")
		w.WriteHeader(rec.ResponseStatus)
		_, _ = w.Write(rec.ResponseBody)
		return 0, resolveHandled, nil
	}
	if rec.Status != "failed" {
		httpx.Fail(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Conflict acquiring idempotency key lock")
		return 0, resolveHandled, nil
	}
	claimedID, ok, err := store.ReclaimFailed(r.Context(), rec.ID, c)
	if err != nil {
		return 0, resolveHandled, err
	}
	if !ok {
		httpx.Fail(w, http.StatusConflict, "CONCURRENT_REQUEST", "A request with this idempotency key is currently processing")
		return 0, resolveHandled, nil
	}
	return claimedID, resolveReclaimed, nil
}

type dbClaimStore struct {
	db     platformdb.Executor
	driver platformdb.Driver
}

func (s dbClaimStore) Claim(ctx context.Context, c claimRequest) (int64, bool, error) {
	if s.driver == platformdb.DriverSQLite {
		result, err := s.db.ExecContext(ctx, `
			INSERT INTO idempotency_keys
				(idempotency_key, user_id, request_path, request_method, request_hash, status, expires_at, created_at)
			VALUES (?, ?, ?, ?, ?, 'processing', ?, ?)
			ON CONFLICT (idempotency_key, user_id, request_path, request_method) DO NOTHING
		`, c.Scope.Key, c.Scope.UserID, c.Scope.Path, c.Scope.Method, c.Hash, platformdb.SQLiteTime(c.ExpiresAt), platformdb.SQLiteTime(time.Now()))
		if err != nil {
			return 0, false, err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return 0, false, err
		}
		id, err := result.LastInsertId()
		return id, true, err
	}

	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO public.idempotency_keys (
			idempotency_key, user_id, request_path, request_method, request_hash, status, expires_at
		) VALUES ($1, $2, $3, $4, $5, 'processing', $6)
		ON CONFLICT (idempotency_key, user_id, request_path, request_method) DO NOTHING
		RETURNING id
	`, c.Scope.Key, c.Scope.UserID, c.Scope.Path, c.Scope.Method, c.Hash, c.ExpiresAt).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return 0, false, err
}

func (s dbClaimStore) Get(ctx context.Context, k claimScope) (*claimRecord, error) {
	var (
		rec          claimRecord
		responseCode sql.NullInt32
		contentType  sql.NullString
		expiresAt    string
	)
	query := `
		SELECT id, status, request_hash, response_status, response_body, response_content_type, expires_at
		FROM public.idempotency_keys
		WHERE idempotency_key = $1 AND user_id = $2 AND request_path = $3 AND request_method = $4
	`
	if s.driver == platformdb.DriverSQLite {
		query = `SELECT id, status, request_hash, response_status, response_body, response_content_type, expires_at FROM idempotency_keys WHERE idempotency_key = ? AND user_id = ? AND request_path = ? AND request_method = ?`
	}
	if s.driver == platformdb.DriverSQLite {
		err := s.db.QueryRowContext(ctx, query, k.Key, k.UserID, k.Path, k.Method).Scan(
			&rec.ID, &rec.Status, &rec.RequestHash, &responseCode, &rec.ResponseBody, &contentType, &expiresAt,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		expiry, err := platformdb.ParseSQLiteTime(expiresAt)
		if err != nil {
			return nil, err
		}
		rec.ExpiresAt = expiry
	} else {
		var expires time.Time
		err := s.db.QueryRowContext(ctx, query, k.Key, k.UserID, k.Path, k.Method).Scan(
			&rec.ID, &rec.Status, &rec.RequestHash, &responseCode, &rec.ResponseBody, &contentType, &expires,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		rec.ExpiresAt = expires
	}
	if responseCode.Valid {
		rec.ResponseStatus = int(responseCode.Int32)
	}
	if contentType.Valid {
		rec.ResponseContentType = contentType.String
	}
	return &rec, nil
}

func (s dbClaimStore) ReclaimExpired(ctx context.Context, id int64, c claimRequest) (int64, bool, error) {
	if s.driver == platformdb.DriverSQLite {
		result, err := s.db.ExecContext(ctx, `
			UPDATE idempotency_keys SET status = 'processing', request_hash = ?, expires_at = ?,
				response_status = NULL, response_body = NULL, response_content_type = NULL
			WHERE id = ? AND expires_at <= ?
		`, c.Hash, platformdb.SQLiteTime(c.ExpiresAt), id, platformdb.SQLiteTime(time.Now()))
		if err != nil {
			return 0, false, err
		}
		n, err := result.RowsAffected()
		return id, n == 1, err
	}

	var claimedID int64
	err := s.db.QueryRowContext(ctx, `
		UPDATE public.idempotency_keys
		SET status = 'processing', request_hash = $1, expires_at = $2,
		    response_status = NULL, response_body = NULL, response_content_type = NULL
		WHERE id = $3 AND expires_at <= now()
		RETURNING id
	`, c.Hash, c.ExpiresAt, id).Scan(&claimedID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return claimedID, true, nil
}

func (s dbClaimStore) ReclaimFailed(ctx context.Context, id int64, c claimRequest) (int64, bool, error) {
	if s.driver == platformdb.DriverSQLite {
		result, err := s.db.ExecContext(ctx, `
			UPDATE idempotency_keys SET status = 'processing', expires_at = ?
			WHERE id = ? AND status = 'failed' AND request_hash = ?
		`, platformdb.SQLiteTime(c.ExpiresAt), id, c.Hash)
		if err != nil {
			return 0, false, err
		}
		n, err := result.RowsAffected()
		return id, n == 1, err
	}

	var claimedID int64
	err := s.db.QueryRowContext(ctx, `
		UPDATE public.idempotency_keys
		SET status = 'processing', expires_at = $1
		WHERE id = $2 AND status = 'failed' AND request_hash = $3
		RETURNING id
	`, c.ExpiresAt, id, c.Hash).Scan(&claimedID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return claimedID, true, nil
}

func (s dbClaimStore) Finalize(ctx context.Context, id int64, res claimResult) error {
	if s.driver == platformdb.DriverSQLite {
		if res.Failed {
			_, err := s.db.ExecContext(ctx, `UPDATE idempotency_keys SET status = 'failed' WHERE id = ?`, id)
			return err
		}
		_, err := s.db.ExecContext(ctx, `
			UPDATE idempotency_keys SET status = 'completed', response_status = ?, response_body = ?,
				response_content_type = ?, expires_at = ? WHERE id = ?
		`, res.StatusCode, res.Body, res.ContentType, platformdb.SQLiteTime(res.ExpiresAt), id)
		return err
	}
	if res.Failed {
		_, err := s.db.ExecContext(ctx, `
			UPDATE public.idempotency_keys
			SET status = 'failed'
			WHERE id = $1
		`, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE public.idempotency_keys
		SET status = 'completed',
		    response_status = $1,
		    response_body = $2,
		    response_content_type = $3,
		    expires_at = $4
		WHERE id = $5
	`, res.StatusCode, res.Body, res.ContentType, res.ExpiresAt, id)
	return err
}

func slogErrorType(ctx context.Context, msg string, err error) {
	slog.ErrorContext(ctx, msg, "error_type", fmt.Sprintf("%T", err))
}
