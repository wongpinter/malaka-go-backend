-- name: ListTasks :many
SELECT id, public_id, user_id, title, description, status, assignee_id, created_at, updated_at
FROM public.tasks
WHERE user_id = $1
  AND status = COALESCE(sqlc.narg('status_filter'), status)
  AND title ILIKE COALESCE(sqlc.narg('search_pattern')::VARCHAR, title)
ORDER BY created_at DESC
LIMIT sqlc.arg('limit_rows') OFFSET sqlc.arg('offset_rows');

-- name: FindTask :one
SELECT id, public_id, user_id, title, description, status, assignee_id, created_at, updated_at
FROM public.tasks
WHERE user_id = $1 AND public_id = $2;

-- name: CreateTask :one
INSERT INTO public.tasks (user_id, title, description, status)
VALUES ($1, $2, $3, $4)
RETURNING id, public_id, user_id, title, description, status, assignee_id, created_at, updated_at;

-- name: UpdateTask :one
UPDATE public.tasks
SET title = $3, description = $4, status = $5, updated_at = now()
WHERE user_id = $1 AND public_id = $2
RETURNING id, public_id, user_id, title, description, status, assignee_id, created_at, updated_at;

-- name: AssignTask :one
UPDATE public.tasks
SET assignee_id = $2, updated_at = now()
WHERE public_id = $1 AND user_id = $3
RETURNING id, public_id, user_id, title, description, status, assignee_id, created_at, updated_at;

-- name: DeleteTask :execrows
DELETE FROM public.tasks
WHERE user_id = $1 AND public_id = $2;

-- name: InsertTaskLog :exec
INSERT INTO public.task_logs (task_id, user_id, action, old_assignee_id, new_assignee_id, actor_user_id)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: FindActiveUser :one
SELECT id
FROM iam.users
WHERE id = $1 AND is_active = true AND deleted_at IS NULL;
