package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	sq "malaka/internal/modules/tasks/sqlc"
	platformdb "malaka/internal/platform/db"
	"malaka/internal/platform/httpx"
)

type Store struct{ q *sq.Queries }

func NewStore(executor platformdb.Executor) StorePort {
	if d, ok := executor.(interface{ Driver() platformdb.Driver }); ok && d.Driver() == platformdb.DriverSQLite {
		return newSQLiteStore(executor)
	}
	return &Store{q: sq.New(executor)}
}

func (s *Store) withTx(tx platformdb.Executor) *sq.Queries { return sq.New(tx) }

func makeTask(id int64, publicID uuid.UUID, userID int64, title, description, status string, assigneeID sql.NullInt64, createdAt, updatedAt time.Time) Task {
	t := Task{
		ID: id, PublicID: publicID, UserID: userID,
		Title: title, Description: description, Status: status,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
	if assigneeID.Valid {
		t.AssigneeID = &assigneeID.Int64
	}
	return t
}

func nullInt64(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

type ListFilter struct {
	Status string
	Search string
	Limit  int32
	Offset int32
}

func escapeLike(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\\' || r == '%' || r == '_' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *Store) List(ctx context.Context, userID int64, f ListFilter) ([]Task, error) {
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

	params := sq.ListTasksParams{UserID: userID, LimitRows: limit, OffsetRows: offset}
	if f.Status != "" {
		params.StatusFilter = sql.NullString{String: f.Status, Valid: true}
	}
	if f.Search != "" {
		params.SearchPattern = sql.NullString{String: "%" + escapeLike(f.Search) + "%", Valid: true}
	}

	rows, err := s.q.ListTasks(ctx, params)
	if err != nil {
		return nil, httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to list tasks", fmt.Errorf("list tasks: %w", err))
	}
	tasks := make([]Task, len(rows))
	for i, r := range rows {
		tasks[i] = makeTask(r.ID, r.PublicID, r.UserID, r.Title, r.Description, r.Status, r.AssigneeID, r.CreatedAt, r.UpdatedAt)
	}
	return tasks, nil
}

func (s *Store) Find(ctx context.Context, userID int64, publicID uuid.UUID) (*Task, error) {
	return s.find(ctx, s.q, userID, publicID)
}

func (s *Store) FindTx(ctx context.Context, tx platformdb.Tx, userID int64, publicID uuid.UUID) (*Task, error) {
	return s.find(ctx, s.withTx(tx), userID, publicID)
}

func (s *Store) find(ctx context.Context, q *sq.Queries, userID int64, publicID uuid.UUID) (*Task, error) {
	r, err := q.FindTask(ctx, sq.FindTaskParams{UserID: userID, PublicID: publicID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NewAppError("tasks.store", "NOT_FOUND", "Task not found", ErrNotFound)
	}
	if err != nil {
		return nil, httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to find task", fmt.Errorf("find task: %w", err))
	}
	t := makeTask(r.ID, r.PublicID, r.UserID, r.Title, r.Description, r.Status, r.AssigneeID, r.CreatedAt, r.UpdatedAt)
	return &t, nil
}

func (s *Store) Create(ctx context.Context, tx platformdb.Tx, userID int64, input Input) (*Task, error) {
	status := input.Status
	if status == "" {
		status = "pending"
	}
	r, err := s.withTx(tx).CreateTask(ctx, sq.CreateTaskParams{
		UserID:      userID,
		Title:       input.Title,
		Description: input.Description,
		Status:      status,
	})
	if err != nil {
		return nil, httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to create task", fmt.Errorf("create task: %w", err))
	}
	t := makeTask(r.ID, r.PublicID, r.UserID, r.Title, r.Description, r.Status, r.AssigneeID, r.CreatedAt, r.UpdatedAt)
	return &t, nil
}

func (s *Store) Update(ctx context.Context, tx platformdb.Tx, userID int64, publicID uuid.UUID, input Input) (*Task, error) {
	status := input.Status
	if status == "" {
		status = "pending"
	}
	r, err := s.withTx(tx).UpdateTask(ctx, sq.UpdateTaskParams{
		UserID:      userID,
		PublicID:    publicID,
		Title:       input.Title,
		Description: input.Description,
		Status:      status,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NewAppError("tasks.store", "NOT_FOUND", "Task not found", ErrNotFound)
	}
	if err != nil {
		return nil, httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to update task", fmt.Errorf("update task: %w", err))
	}
	t := makeTask(r.ID, r.PublicID, r.UserID, r.Title, r.Description, r.Status, r.AssigneeID, r.CreatedAt, r.UpdatedAt)
	return &t, nil
}

func (s *Store) Delete(ctx context.Context, tx platformdb.Tx, userID int64, publicID uuid.UUID) error {
	count, err := s.withTx(tx).DeleteTask(ctx, sq.DeleteTaskParams{UserID: userID, PublicID: publicID})
	if err != nil {
		return httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to delete task", fmt.Errorf("delete task: %w", err))
	}
	if count == 0 {
		return httpx.NewAppError("tasks.store", "NOT_FOUND", "Task not found", ErrNotFound)
	}
	return nil
}

func (s *Store) AssertActiveUser(ctx context.Context, tx platformdb.Tx, userID int64) error {
	if _, err := s.withTx(tx).FindActiveUser(ctx, userID); errors.Is(err, sql.ErrNoRows) {
		return httpx.NewAppError("tasks.store", "NOT_FOUND", "User not found", ErrNotFound)
	} else if err != nil {
		return httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to find assignee", fmt.Errorf("find assignee: %w", err))
	}
	return nil
}

func (s *Store) Assign(ctx context.Context, tx platformdb.Tx, userID int64, publicID uuid.UUID, assigneeID int64) (*Task, error) {
	r, err := s.withTx(tx).AssignTask(ctx, sq.AssignTaskParams{
		UserID:     userID,
		PublicID:   publicID,
		AssigneeID: sql.NullInt64{Int64: assigneeID, Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NewAppError("tasks.store", "NOT_FOUND", "Task not found", ErrNotFound)
	}
	if err != nil {
		return nil, httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to assign task", fmt.Errorf("assign task: %w", err))
	}
	t := makeTask(r.ID, r.PublicID, r.UserID, r.Title, r.Description, r.Status, r.AssigneeID, r.CreatedAt, r.UpdatedAt)
	return &t, nil
}

func (s *Store) InsertLog(ctx context.Context, tx platformdb.Tx, taskID, userID int64, action string, oldAssignee, newAssignee, actor *int64) error {
	if err := s.withTx(tx).InsertTaskLog(ctx, sq.InsertTaskLogParams{
		TaskID:        taskID,
		UserID:        userID,
		Action:        action,
		OldAssigneeID: nullInt64(oldAssignee),
		NewAssigneeID: nullInt64(newAssignee),
		ActorUserID:   nullInt64(actor),
	}); err != nil {
		return httpx.NewAppError("tasks.store", "INTERNAL_ERROR", "Failed to record task log", fmt.Errorf("insert task log: %w", err))
	}
	return nil
}
