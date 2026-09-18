package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	platformdb "malaka/internal/platform/db"
	"malaka/internal/platform/httpx"
)

type StorePort interface {
	List(context.Context, int64, ListFilter) ([]Task, error)
	Find(context.Context, int64, uuid.UUID) (*Task, error)
	FindTx(context.Context, platformdb.Tx, int64, uuid.UUID) (*Task, error)
	Create(context.Context, platformdb.Tx, int64, Input) (*Task, error)
	Update(context.Context, platformdb.Tx, int64, uuid.UUID, Input) (*Task, error)
	Delete(context.Context, platformdb.Tx, int64, uuid.UUID) error
	AssertActiveUser(context.Context, platformdb.Tx, int64) error
	Assign(context.Context, platformdb.Tx, int64, uuid.UUID, int64) (*Task, error)
	InsertLog(context.Context, platformdb.Tx, int64, int64, string, *int64, *int64, *int64) error
}

type sqliteStore struct{ db platformdb.Executor }

func newSQLiteStore(executor platformdb.Executor) StorePort { return &sqliteStore{db: executor} }

func (s *sqliteStore) List(ctx context.Context, userID int64, f ListFilter) ([]Task, error) {
	limit := f.Limit
	if limit <= 0 || limit > MaxListLimit {
		limit = DefaultListLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > MaxListOffset {
		offset = MaxListOffset
	}
	pattern := ""
	if f.Search != "" {
		pattern = "%" + escapeLike(f.Search) + "%"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, public_id, user_id, title, description, status, assignee_id, created_at, updated_at
		FROM tasks
		WHERE user_id = ?
		  AND (? = '' OR status = ?)
		  AND (? = '' OR lower(title) LIKE lower(?) ESCAPE '\')
		ORDER BY created_at DESC, id DESC
		LIMIT ? OFFSET ?
	`, userID, f.Status, f.Status, pattern, pattern, limit, offset)
	if err != nil {
		return nil, httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to list tasks", fmt.Errorf("list tasks: %w", err))
	}
	defer rows.Close()
	result := make([]Task, 0)
	for rows.Next() {
		t, err := scanSQLiteTask(rows)
		if err != nil {
			return nil, httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to list tasks", fmt.Errorf("scan task: %w", err))
		}
		result = append(result, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to list tasks", fmt.Errorf("iterate tasks: %w", err))
	}
	return result, nil
}

func (s *sqliteStore) Find(ctx context.Context, userID int64, publicID uuid.UUID) (*Task, error) {
	return s.find(ctx, s.db, userID, publicID)
}

func (s *sqliteStore) FindTx(ctx context.Context, tx platformdb.Tx, userID int64, publicID uuid.UUID) (*Task, error) {
	return s.find(ctx, tx, userID, publicID)
}

func (s *sqliteStore) find(ctx context.Context, executor platformdb.Executor, userID int64, publicID uuid.UUID) (*Task, error) {
	row := executor.QueryRowContext(ctx, `
		SELECT id, public_id, user_id, title, description, status, assignee_id, created_at, updated_at
		FROM tasks WHERE user_id = ? AND public_id = ?
	`, userID, publicID.String())
	t, err := scanSQLiteTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NewAppError("tasks.store", "NOT_FOUND", "Task not found", ErrNotFound)
	}
	if err != nil {
		return nil, httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to find task", fmt.Errorf("find task: %w", err))
	}
	return t, nil
}

func (s *sqliteStore) Create(ctx context.Context, tx platformdb.Tx, userID int64, input Input) (*Task, error) {
	status := input.Status
	if status == "" {
		status = StatusPending
	}
	now := platformdb.SQLiteTime(time.Now())
	row := tx.QueryRowContext(ctx, `
		INSERT INTO tasks (public_id, user_id, title, description, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		RETURNING id, public_id, user_id, title, description, status, assignee_id, created_at, updated_at
	`, uuid.New().String(), userID, input.Title, input.Description, status, now, now)
	t, err := scanSQLiteTask(row)
	if err != nil {
		return nil, httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to create task", fmt.Errorf("create task: %w", err))
	}
	return t, nil
}

func (s *sqliteStore) Update(ctx context.Context, tx platformdb.Tx, userID int64, publicID uuid.UUID, input Input) (*Task, error) {
	status := input.Status
	if status == "" {
		status = StatusPending
	}
	now := platformdb.SQLiteTime(time.Now())
	row := tx.QueryRowContext(ctx, `
		UPDATE tasks SET title = ?, description = ?, status = ?, updated_at = ?
		WHERE user_id = ? AND public_id = ?
		RETURNING id, public_id, user_id, title, description, status, assignee_id, created_at, updated_at
	`, input.Title, input.Description, status, now, userID, publicID.String())
	t, err := scanSQLiteTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NewAppError("tasks.store", "NOT_FOUND", "Task not found", ErrNotFound)
	}
	if err != nil {
		return nil, httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to update task", fmt.Errorf("update task: %w", err))
	}
	return t, nil
}

func (s *sqliteStore) Delete(ctx context.Context, tx platformdb.Tx, userID int64, publicID uuid.UUID) error {
	result, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE user_id = ? AND public_id = ?`, userID, publicID.String())
	if err != nil {
		return httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to delete task", fmt.Errorf("delete task: %w", err))
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return httpx.NewAppError("tasks.store", "NOT_FOUND", "Task not found", ErrNotFound)
	}
	return nil
}

func (s *sqliteStore) AssertActiveUser(ctx context.Context, tx platformdb.Tx, userID int64) error {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = ? AND is_active = 1 AND deleted_at IS NULL`, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NewAppError("tasks.store", "NOT_FOUND", "User not found", ErrNotFound)
	}
	if err != nil {
		return httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to find assignee", fmt.Errorf("find assignee: %w", err))
	}
	return nil
}

func (s *sqliteStore) Assign(ctx context.Context, tx platformdb.Tx, userID int64, publicID uuid.UUID, assigneeID int64) (*Task, error) {
	now := platformdb.SQLiteTime(time.Now())
	row := tx.QueryRowContext(ctx, `
		UPDATE tasks SET assignee_id = ?, updated_at = ?
		WHERE public_id = ? AND user_id = ?
		RETURNING id, public_id, user_id, title, description, status, assignee_id, created_at, updated_at
	`, assigneeID, now, publicID.String(), userID)
	t, err := scanSQLiteTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NewAppError("tasks.store", "NOT_FOUND", "Task not found", ErrNotFound)
	}
	if err != nil {
		return nil, httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to assign task", fmt.Errorf("assign task: %w", err))
	}
	return t, nil
}

func (s *sqliteStore) InsertLog(ctx context.Context, tx platformdb.Tx, taskID, userID int64, action string, oldAssignee, newAssignee, actor *int64) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO task_logs (task_id, user_id, action, old_assignee_id, new_assignee_id, actor_user_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, taskID, userID, action, oldAssignee, newAssignee, actor, platformdb.SQLiteTime(time.Now())); err != nil {
		return httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to record task log", fmt.Errorf("insert task log: %w", err))
	}
	return nil
}

type taskScanner interface{ Scan(...any) error }

func scanSQLiteTask(row taskScanner) (*Task, error) {
	var (
		t                              Task
		publicID, createdAt, updatedAt string
		assigneeID                     sql.NullInt64
	)
	if err := row.Scan(&t.ID, &publicID, &t.UserID, &t.Title, &t.Description, &t.Status, &assigneeID, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	parsedID, err := uuid.Parse(publicID)
	if err != nil {
		return nil, err
	}
	t.PublicID = parsedID
	if assigneeID.Valid {
		t.AssigneeID = &assigneeID.Int64
	}
	if t.CreatedAt, err = platformdb.ParseSQLiteTime(createdAt); err != nil {
		return nil, err
	}
	if t.UpdatedAt, err = platformdb.ParseSQLiteTime(updatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}
