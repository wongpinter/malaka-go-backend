package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"malaka/internal/platform/audit"
	"malaka/internal/platform/db"
	"malaka/internal/platform/event"
	"malaka/internal/platform/httpx"
)

func taskWrap(message string, err error) error { return httpx.Wrap(tasksLayer, err, message) }

const tasksLayer = "tasks.service"

type Service struct {
	db    db.Database
	store StorePort
	audit *audit.Writer
}

func NewService(database db.Database, auditWriter *audit.Writer) *Service {
	return &Service{db: database, store: NewStore(database), audit: auditWriter}
}

func (s *Service) List(ctx context.Context, userID int64, filter ListFilter) ([]Task, error) {
	result, err := s.store.List(ctx, userID, filter)
	if err != nil {
		return nil, taskWrap("Failed to list tasks", err)
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, userID int64, publicID uuid.UUID) (*Task, error) {
	result, err := s.store.Find(ctx, userID, publicID)
	if err != nil {
		return nil, taskWrap("Failed to find task", err)
	}
	return result, nil
}

func (s *Service) Create(ctx context.Context, userID int64, input Input) (*Task, error) {
	input = input.Normalize()
	if msgs := input.Validate(); len(msgs) > 0 {
		return nil, httpx.NewAppError("tasks.service", "VALIDATION_ERROR", "Invalid task data", fmt.Errorf("validation: %s", msgs[0]))
	}
	var created *Task
	err := s.db.ExecTx(ctx, func(tx db.Tx) error {
		t, err := s.store.Create(ctx, tx, userID, input)
		if err != nil {
			return err
		}
		if err := event.Add(ctx, tx, "task.created", t); err != nil {
			return err
		}
		if err := s.logAudit(ctx, tx, userID, "CREATE", t, nil, t); err != nil {
			return err
		}
		created = t
		return nil
	})
	if err != nil {
		return nil, taskWrap("Failed to create task", err)
	}
	return created, nil
}

func (s *Service) Update(ctx context.Context, userID int64, publicID uuid.UUID, input Input) (*Task, error) {
	input = input.Normalize()
	if msgs := input.Validate(); len(msgs) > 0 {
		return nil, httpx.NewAppError("tasks.service", "VALIDATION_ERROR", "Invalid task data", fmt.Errorf("validation: %s", msgs[0]))
	}
	var updated *Task
	err := s.db.ExecTx(ctx, func(tx db.Tx) error {
		old, err := s.store.FindTx(ctx, tx, userID, publicID)
		if err != nil {
			return err
		}
		t, err := s.store.Update(ctx, tx, userID, publicID, input)
		if err != nil {
			return err
		}
		if err := event.Add(ctx, tx, "task.updated", t); err != nil {
			return err
		}
		if err := s.logAudit(ctx, tx, userID, "UPDATE", t, old, t); err != nil {
			return err
		}
		updated = t
		return nil
	})
	if err != nil {
		return nil, taskWrap("Failed to update task", err)
	}
	return updated, nil
}

func (s *Service) Assign(ctx context.Context, actorUserID int64, publicID uuid.UUID, assigneeID int64) (*Task, error) {
	if assigneeID == 0 {
		return nil, httpx.NewAppError("tasks.service", "VALIDATION_ERROR", "Assignee ID is required", nil)
	}
	var assigned *Task
	err := s.db.ExecTx(ctx, func(tx db.Tx) error {
		task, err := s.store.FindTx(ctx, tx, actorUserID, publicID)
		if err != nil {
			return err
		}
		if err := s.store.AssertActiveUser(ctx, tx, assigneeID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return httpx.NewAppError("tasks.service", "VALIDATION_ERROR", "Assignee must be an active registered user", err)
			}
			return err
		}
		oldAssignee := task.AssigneeID
		updated, err := s.store.Assign(ctx, tx, actorUserID, publicID, assigneeID)
		if err != nil {
			return err
		}
		var actor *int64
		if actorUserID != 0 {
			actor = &actorUserID
		}
		if err := s.store.InsertLog(ctx, tx, updated.ID, updated.UserID, "assign", oldAssignee, updated.AssigneeID, actor); err != nil {
			return err
		}
		if err := event.Add(ctx, tx, "task.assigned", map[string]any{"task_id": publicID, "assignee_id": assigneeID}); err != nil {
			return err
		}
		if err := s.logAudit(ctx, tx, actorUserID, "ASSIGN", updated, task, updated); err != nil {
			return err
		}
		slog.InfoContext(ctx, "task assigned notification", "task_id", publicID, "assignee_id", assigneeID, "actor", actorUserID)
		assigned = updated
		return nil
	})
	if err != nil {
		return nil, taskWrap("Failed to assign task", err)
	}
	return assigned, nil
}

func (s *Service) Delete(ctx context.Context, userID int64, publicID uuid.UUID) error {
	err := s.db.ExecTx(ctx, func(tx db.Tx) error {
		old, err := s.store.FindTx(ctx, tx, userID, publicID)
		if err != nil {
			return err
		}
		if err := s.store.Delete(ctx, tx, userID, publicID); err != nil {
			return err
		}
		if err := event.Add(ctx, tx, "task.deleted", map[string]any{"id": publicID}); err != nil {
			return err
		}
		return s.logAudit(ctx, tx, userID, "DELETE", old, old, nil)
	})
	if err != nil {
		return taskWrap("Failed to delete task", err)
	}
	return nil
}

func (s *Service) logAudit(ctx context.Context, tx db.Tx, userID int64, action string, task, oldValue, newValue *Task) error {
	if s.audit == nil {
		return nil
	}
	oldJSON, _ := json.Marshal(oldValue)
	newJSON, _ := json.Marshal(newValue)
	var actor *int64
	if userID != 0 {
		actor = &userID
	}
	return s.audit.LogTx(ctx, tx, audit.AuditEntry{
		UserID:         actor,
		Action:         action,
		EntityType:     "task",
		EntityID:       task.ID,
		EntityPublicID: task.PublicID,
		OldValues:      oldJSON,
		NewValues:      newJSON,
	})
}
