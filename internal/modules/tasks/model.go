package tasks

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("task not found")

const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
)

const (
	DefaultListLimit = 25
	MaxListLimit     = 100
	MaxListOffset    = 100_000
	MaxSearchLength  = 255
)

func IsValidStatus(s string) bool {
	return s == StatusPending || s == StatusInProgress || s == StatusDone
}

type StatusOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func StatusOptions() []StatusOption {
	return []StatusOption{
		{Value: StatusPending, Label: "Pending"},
		{Value: StatusInProgress, Label: "In Progress"},
		{Value: StatusDone, Label: "Done"},
	}
}

type Task struct {
	ID          int64     `json:"-"`
	PublicID    uuid.UUID `json:"id"`
	UserID      int64     `json:"-"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"`
	AssigneeID  *int64    `json:"assignee_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type TaskLog struct {
	ID            int64     `json:"id"`
	TaskID        int64     `json:"task_id"`
	UserID        int64     `json:"user_id"`
	Action        string    `json:"action"`
	OldAssigneeID *int64    `json:"old_assignee_id"`
	NewAssigneeID *int64    `json:"new_assignee_id"`
	ActorUserID   *int64    `json:"actor_user_id"`
	CreatedAt     time.Time `json:"created_at"`
}

type AssignInput struct {
	AssigneeID int64 `json:"assignee_id"`
}

type Input struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status,omitempty"`
}

func (in Input) Normalize() Input {
	in.Title = strings.TrimSpace(in.Title)
	in.Description = strings.TrimSpace(in.Description)
	in.Status = strings.TrimSpace(in.Status)
	return in
}

func (in Input) Validate() []string {
	if in.Title == "" {
		return []string{"title is required"}
	}
	if len(in.Title) > 255 {
		return []string{"title must be 255 characters or fewer"}
	}
	if len(in.Description) > 4096 {
		return []string{"description must be 4096 characters or fewer"}
	}
	if in.Status != "" && !IsValidStatus(in.Status) {
		return []string{"status must be pending, in_progress, or done"}
	}
	return nil
}
