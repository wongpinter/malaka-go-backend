package iam

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
	CreateUser(context.Context, string, string, string) (*User, error)
	UpsertUser(context.Context, string, string, string) (*User, error)
	GetUserByEmail(context.Context, string) (*User, error)
	GetUserByID(context.Context, int64) (*User, error)
	CreateSession(context.Context, int64, string, time.Time) (*Session, error)
	GetSessionByTokenHash(context.Context, string) (*Session, error)
	RevokeSession(context.Context, string) error
}

type sqliteStore struct{ db platformdb.Executor }

func newSQLiteStore(executor platformdb.Executor) StorePort { return &sqliteStore{db: executor} }

func (s *sqliteStore) CreateUser(ctx context.Context, email, name, passwordHash string) (*User, error) {
	return s.insertUser(ctx, email, name, passwordHash)
}

func (s *sqliteStore) UpsertUser(ctx context.Context, email, name, passwordHash string) (*User, error) {
	now := platformdb.SQLiteTime(time.Now())
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO users (public_id, email, name, password_hash, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT (lower(email)) WHERE deleted_at IS NULL
		DO UPDATE SET name = excluded.name, password_hash = excluded.password_hash, updated_at = excluded.updated_at
		RETURNING id, public_id, email, name, password_hash, is_active, created_at
	`, uuid.New().String(), email, name, passwordHash, now, now)
	user, err := scanSQLiteUser(row)
	if err != nil {
		return nil, httpx.NewAppError("iam.store", "INTERNAL_ERROR", "Failed to upsert user", fmt.Errorf("upsert user: %w", err))
	}
	return user, nil
}

func (s *sqliteStore) insertUser(ctx context.Context, email, name, passwordHash string) (*User, error) {
	publicID := uuid.New()
	now := platformdb.SQLiteTime(time.Now())
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO users (public_id, email, name, password_hash, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)
		RETURNING id, public_id, email, name, password_hash, is_active, created_at
	`, publicID.String(), email, name, passwordHash, now, now)
	user, err := scanSQLiteUser(row)
	if err != nil {
		return nil, httpx.NewAppError("iam.store", "INTERNAL_ERROR", "Failed to create user", fmt.Errorf("insert user: %w", err))
	}
	return user, nil
}

func (s *sqliteStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, public_id, email, name, password_hash, is_active, created_at
		FROM users WHERE lower(email) = lower(?) AND deleted_at IS NULL
	`, email)
	user, err := scanSQLiteUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NewAppError("iam.store", "NOT_FOUND", "User not found", ErrNotFound)
	}
	if err != nil {
		return nil, httpx.NewAppError("iam.store", "INTERNAL_ERROR", "Failed to find user", fmt.Errorf("get user by email: %w", err))
	}
	return user, nil
}

func (s *sqliteStore) GetUserByID(ctx context.Context, id int64) (*User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, public_id, email, name, password_hash, is_active, created_at
		FROM users WHERE id = ? AND deleted_at IS NULL
	`, id)
	user, err := scanSQLiteUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NewAppError("iam.store", "NOT_FOUND", "User not found", ErrNotFound)
	}
	if err != nil {
		return nil, httpx.NewAppError("iam.store", "INTERNAL_ERROR", "Failed to find user", fmt.Errorf("get user by id: %w", err))
	}
	return user, nil
}

func (s *sqliteStore) CreateSession(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) (*Session, error) {
	publicID := uuid.New()
	now := platformdb.SQLiteTime(time.Now())
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO sessions (public_id, user_id, token_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?)
		RETURNING id, public_id, user_id, token_hash, expires_at, created_at, NULL
	`, publicID.String(), userID, tokenHash, platformdb.SQLiteTime(expiresAt), now)
	session, err := scanSQLiteSession(row)
	if err != nil {
		return nil, httpx.NewAppError("iam.store", "INTERNAL_ERROR", "Failed to create session", fmt.Errorf("insert session: %w", err))
	}
	return session, nil
}

func (s *sqliteStore) GetSessionByTokenHash(ctx context.Context, tokenHash string) (*Session, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, public_id, user_id, token_hash, expires_at, created_at, revoked_at
		FROM sessions WHERE token_hash = ? AND revoked_at IS NULL AND expires_at > ?
	`, tokenHash, platformdb.SQLiteTime(time.Now()))
	session, err := scanSQLiteSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NewAppError("iam.store", "UNAUTHORIZED", "Session is invalid or expired", ErrInvalidSession)
	}
	if err != nil {
		return nil, httpx.NewAppError("iam.store", "INTERNAL_ERROR", "Failed to find session", fmt.Errorf("get session: %w", err))
	}
	return session, nil
}

func (s *sqliteStore) RevokeSession(ctx context.Context, tokenHash string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`, platformdb.SQLiteTime(time.Now()), tokenHash); err != nil {
		return httpx.NewAppError("iam.store", "INTERNAL_ERROR", "Failed to revoke session", fmt.Errorf("revoke session: %w", err))
	}
	return nil
}

type scanner interface{ Scan(...any) error }

func scanSQLiteUser(row scanner) (*User, error) {
	var (
		u                   User
		publicID, createdAt string
		active              int
		passwordHash        sql.NullString
	)
	if err := row.Scan(&u.ID, &publicID, &u.Email, &u.Name, &passwordHash, &active, &createdAt); err != nil {
		return nil, err
	}
	parsedID, err := uuid.Parse(publicID)
	if err != nil {
		return nil, err
	}
	u.PublicID, u.IsActive = parsedID, active != 0
	if passwordHash.Valid {
		u.PasswordHash = passwordHash.String
	}
	u.CreatedAt, err = platformdb.ParseSQLiteTime(createdAt)
	return &u, err
}

func scanSQLiteSession(row scanner) (*Session, error) {
	var (
		s                              Session
		publicID, expiresAt, createdAt string
		revokedAt                      sql.NullString
	)
	if err := row.Scan(&s.ID, &publicID, &s.UserID, &s.TokenHash, &expiresAt, &createdAt, &revokedAt); err != nil {
		return nil, err
	}
	parsedID, err := uuid.Parse(publicID)
	if err != nil {
		return nil, err
	}
	s.PublicID = parsedID
	if s.ExpiresAt, err = platformdb.ParseSQLiteTime(expiresAt); err != nil {
		return nil, err
	}
	if s.CreatedAt, err = platformdb.ParseSQLiteTime(createdAt); err != nil {
		return nil, err
	}
	if revokedAt.Valid {
		t, err := platformdb.ParseSQLiteTime(revokedAt.String)
		if err != nil {
			return nil, err
		}
		s.RevokedAt = &t
	}
	return &s, nil
}
