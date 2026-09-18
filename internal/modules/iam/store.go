package iam

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	sq "malaka/internal/modules/iam/sqlc"
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

func (s *Store) CreateUser(ctx context.Context, email, name, passwordHash string) (*User, error) {
	r, err := s.q.CreateUser(ctx, sq.CreateUserParams{
		Email:        email,
		Name:         name,
		PasswordHash: sql.NullString{String: passwordHash, Valid: true},
	})
	if err != nil {
		return nil, httpx.NewAppError("iam.store", "INTERNAL_ERROR", "Failed to create user", fmt.Errorf("create user: %w", err))
	}
	return &User{ID: r.ID, PublicID: r.PublicID, Email: r.Email, Name: r.Name, IsActive: r.IsActive, CreatedAt: r.CreatedAt}, nil
}

func (s *Store) UpsertUser(ctx context.Context, email, name, passwordHash string) (*User, error) {
	r, err := s.q.UpsertUser(ctx, sq.UpsertUserParams{
		Email:        email,
		Name:         name,
		PasswordHash: sql.NullString{String: passwordHash, Valid: true},
	})
	if err != nil {
		return nil, httpx.NewAppError("iam.store", "INTERNAL_ERROR", "Failed to upsert user", fmt.Errorf("upsert user: %w", err))
	}
	return &User{ID: r.ID, PublicID: r.PublicID, Email: r.Email, Name: r.Name, IsActive: r.IsActive, CreatedAt: r.CreatedAt}, nil
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	r, err := s.q.GetUserByEmail(ctx, email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NewAppError("iam.store", "NOT_FOUND", "User not found", ErrNotFound)
	}
	if err != nil {
		return nil, httpx.NewAppError("iam.store", "INTERNAL_ERROR", "Failed to find user", fmt.Errorf("get user by email: %w", err))
	}
	u := &User{ID: r.ID, PublicID: r.PublicID, Email: r.Email, Name: r.Name, IsActive: r.IsActive, CreatedAt: r.CreatedAt}
	if r.PasswordHash.Valid {
		u.PasswordHash = r.PasswordHash.String
	}
	return u, nil
}

func (s *Store) GetUserByID(ctx context.Context, id int64) (*User, error) {
	r, err := s.q.GetUserByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NewAppError("iam.store", "NOT_FOUND", "User not found", ErrNotFound)
	}
	if err != nil {
		return nil, httpx.NewAppError("iam.store", "INTERNAL_ERROR", "Failed to find user", fmt.Errorf("get user by id: %w", err))
	}
	return &User{ID: r.ID, PublicID: r.PublicID, Email: r.Email, Name: r.Name, IsActive: r.IsActive, CreatedAt: r.CreatedAt}, nil
}

func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) (*Session, error) {
	r, err := s.q.CreateSession(ctx, sq.CreateSessionParams{
		UserID:    userID,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, httpx.NewAppError("iam.store", "INTERNAL_ERROR", "Failed to create session", fmt.Errorf("create session: %w", err))
	}
	return &Session{
		ID: r.ID, PublicID: r.PublicID, UserID: r.UserID,
		TokenHash: r.TokenHash, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt,
	}, nil
}

func (s *Store) GetSessionByTokenHash(ctx context.Context, tokenHash string) (*Session, error) {
	r, err := s.q.GetSessionByTokenHash(ctx, tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, httpx.NewAppError("iam.store", "UNAUTHORIZED", "Session is invalid or expired", ErrInvalidSession)
	}
	if err != nil {
		return nil, httpx.NewAppError("iam.store", "INTERNAL_ERROR", "Failed to find session", fmt.Errorf("get session: %w", err))
	}
	sess := &Session{
		ID: r.ID, PublicID: r.PublicID, UserID: r.UserID,
		TokenHash: r.TokenHash, ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt,
	}
	if r.RevokedAt.Valid {
		sess.RevokedAt = &r.RevokedAt.Time
	}
	return sess, nil
}

func (s *Store) RevokeSession(ctx context.Context, tokenHash string) error {
	if err := s.q.RevokeSession(ctx, tokenHash); err != nil {
		return httpx.NewAppError("iam.store", "INTERNAL_ERROR", "Failed to revoke session", fmt.Errorf("revoke session: %w", err))
	}
	return nil
}
