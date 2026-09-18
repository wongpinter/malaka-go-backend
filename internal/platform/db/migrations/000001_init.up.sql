
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE OR REPLACE FUNCTION public.uuid_generate_v7()
RETURNS uuid AS $$
DECLARE
  v_time timestamp with time zone := clock_timestamp();
  v_secs bigint := EXTRACT(EPOCH FROM v_time);
  v_msec bigint := EXTRACT(MILLISECONDS FROM v_time)::bigint % 1000;
  v_unix_time_ms bigint := (v_secs * 1000) + v_msec;
  v_bytes bytea := gen_random_bytes(16);
BEGIN
  v_bytes := set_byte(v_bytes, 0, ((v_unix_time_ms >> 40) & 255)::int);
  v_bytes := set_byte(v_bytes, 1, ((v_unix_time_ms >> 32) & 255)::int);
  v_bytes := set_byte(v_bytes, 2, ((v_unix_time_ms >> 24) & 255)::int);
  v_bytes := set_byte(v_bytes, 3, ((v_unix_time_ms >> 16) & 255)::int);
  v_bytes := set_byte(v_bytes, 4, ((v_unix_time_ms >> 8)  & 255)::int);
  v_bytes := set_byte(v_bytes, 5, (v_unix_time_ms         & 255)::int);
  v_bytes := set_byte(v_bytes, 6, ((get_byte(v_bytes, 6) & 15) | 112)::int);
  v_bytes := set_byte(v_bytes, 8, ((get_byte(v_bytes, 8) & 63) | 128)::int);
  RETURN encode(v_bytes, 'hex')::uuid;
END;
$$ LANGUAGE plpgsql VOLATILE;

CREATE SCHEMA IF NOT EXISTS iam;

CREATE TABLE IF NOT EXISTS iam.users (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id     UUID NOT NULL DEFAULT public.uuid_generate_v7(),
    email         VARCHAR(255) NOT NULL,
    name          VARCHAR(255) NOT NULL,
    password_hash VARCHAR(255),
    is_active     BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at    TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_iam_users_public_id   ON iam.users(public_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_iam_users_email_live  ON iam.users(lower(email)) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS iam.sessions (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id  UUID NOT NULL DEFAULT public.uuid_generate_v7(),
    user_id    BIGINT NOT NULL REFERENCES iam.users(id) ON DELETE CASCADE,
    token_hash VARCHAR(64) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_iam_sessions_token_hash ON iam.sessions(token_hash);
CREATE INDEX        IF NOT EXISTS idx_iam_sessions_user       ON iam.sessions(user_id, expires_at) WHERE revoked_at IS NULL;
CREATE INDEX        IF NOT EXISTS idx_iam_sessions_expires   ON iam.sessions(expires_at);

CREATE TABLE IF NOT EXISTS public.idempotency_keys (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    idempotency_key UUID NOT NULL,
    user_id        BIGINT NOT NULL REFERENCES iam.users(id) ON DELETE CASCADE,
    request_path   VARCHAR(255) NOT NULL,
    request_method VARCHAR(8) NOT NULL DEFAULT 'POST',
    request_hash   VARCHAR(64) NOT NULL,
    status         VARCHAR(20) NOT NULL DEFAULT 'processing',
    response_status INT,
    response_body  TEXT,
    response_content_type TEXT,
    expires_at     TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_idempotency_key_user_path
    ON public.idempotency_keys(idempotency_key, user_id, request_path, request_method);
CREATE INDEX IF NOT EXISTS idx_idempotency_purge
    ON public.idempotency_keys(expires_at) WHERE status IN ('completed','failed');

CREATE TABLE IF NOT EXISTS public.outbox_events (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id         BIGINT REFERENCES iam.users(id) ON DELETE SET NULL,
    event_type      VARCHAR(100) NOT NULL,
    payload         JSONB NOT NULL,
    correlation_id  VARCHAR(36),
    status          VARCHAR(20) NOT NULL DEFAULT 'pending',
    attempts        INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ DEFAULT now(),
    published_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_outbox_status_next ON public.outbox_events(status, next_attempt_at)
    WHERE status IN ('pending','failed','processing');

CREATE TABLE IF NOT EXISTS public.audit_logs (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id       UUID NOT NULL DEFAULT public.uuid_generate_v7(),
    user_id         BIGINT REFERENCES iam.users(id) ON DELETE SET NULL,
    action          VARCHAR(50) NOT NULL,
    entity_type     VARCHAR(50) NOT NULL,
    entity_id       BIGINT NOT NULL,
    entity_public_id UUID NOT NULL,
    old_values      TEXT,
    new_values      TEXT,
    prev_hash       VARCHAR(64),
    curr_hash       VARCHAR(64) NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_audit_entity ON public.audit_logs(entity_type, entity_id, id ASC);

CREATE TABLE IF NOT EXISTS public.tasks (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id   UUID NOT NULL DEFAULT public.uuid_generate_v7(),
    user_id     BIGINT NOT NULL REFERENCES iam.users(id) ON DELETE CASCADE,
    title       VARCHAR(255) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status      VARCHAR(32) NOT NULL DEFAULT 'pending',
    assignee_id BIGINT REFERENCES iam.users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_tasks_public_id     ON public.tasks(public_id);
CREATE INDEX        IF NOT EXISTS idx_tasks_user_created ON public.tasks(user_id, created_at DESC);
CREATE INDEX        IF NOT EXISTS idx_tasks_assignee     ON public.tasks(assignee_id);

CREATE TABLE IF NOT EXISTS public.task_logs (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    task_id         BIGINT NOT NULL REFERENCES public.tasks(id) ON DELETE CASCADE,
    user_id         BIGINT NOT NULL REFERENCES iam.users(id) ON DELETE CASCADE,
    action          VARCHAR(64) NOT NULL,
    old_assignee_id BIGINT,
    new_assignee_id BIGINT,
    actor_user_id   BIGINT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_task_logs_task ON public.task_logs(task_id, created_at DESC);
