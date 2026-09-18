-- name: CreateUser :one
INSERT INTO iam.users (public_id, email, name, password_hash, is_active)
VALUES (public.uuid_generate_v7(), $1, $2, $3, true)
RETURNING id, public_id, email, name, password_hash, is_active, created_at;

-- name: GetUserByEmail :one
SELECT id, public_id, email, name, password_hash, is_active, created_at
FROM iam.users
WHERE lower(email) = lower($1) AND deleted_at IS NULL;

-- name: GetUserByID :one
SELECT id, public_id, email, name, password_hash, is_active, created_at
FROM iam.users
WHERE id = $1 AND deleted_at IS NULL;

-- name: CreateSession :one
INSERT INTO iam.sessions (public_id, user_id, token_hash, expires_at)
VALUES (public.uuid_generate_v7(), $1, $2, $3)
RETURNING id, public_id, user_id, token_hash, expires_at, created_at;

-- name: GetSessionByTokenHash :one
SELECT id, public_id, user_id, token_hash, expires_at, created_at, revoked_at
FROM iam.sessions
WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now();

-- name: RevokeSession :exec
UPDATE iam.sessions SET revoked_at = now()
WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: UpsertUser :one
INSERT INTO iam.users (public_id, email, name, password_hash, is_active)
VALUES (public.uuid_generate_v7(), $1, $2, $3, true)
ON CONFLICT (lower(email)) WHERE deleted_at IS NULL DO UPDATE
  SET name = EXCLUDED.name, password_hash = EXCLUDED.password_hash
RETURNING id, public_id, email, name, password_hash, is_active, created_at;
