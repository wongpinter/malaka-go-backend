package iam

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"malaka/internal/platform/db"
	"malaka/internal/platform/httpx"
	"malaka/internal/platform/mid"
)

func iamWrap(message string, err error) error { return httpx.Wrap(iamLayer, err, message) }

const iamLayer = "iam.service"

type Service struct {
	db       db.Database
	store    StorePort
	tokenMgr *TokenManager
}

func NewService(database db.Database, store StorePort, tokenMgr *TokenManager) *Service {
	return &Service{db: database, store: store, tokenMgr: tokenMgr}
}

func (s *Service) Register(ctx context.Context, input RegisterInput) (*AuthResponse, error) {
	input = input.Normalize()
	if msgs := input.Validate(); len(msgs) > 0 {
		return nil, httpx.NewAppError("iam.service", "VALIDATION_ERROR", msgs[0], nil)
	}

	_, err := s.store.GetUserByEmail(ctx, input.Email)
	if err == nil {
		return nil, httpx.NewAppError("iam.service", "EMAIL_TAKEN", "Email already registered", ErrEmailTaken)
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, iamWrap("Failed to check email", err)
	}

	hash, err := HashPassword(input.Password)
	if err != nil {
		return nil, httpx.NewAppError("iam.service", "INTERNAL_ERROR", "Failed to secure password", fmt.Errorf("hash password: %w", err))
	}
	user, err := s.store.CreateUser(ctx, input.Email, input.Name, hash)
	if err != nil {
		return nil, iamWrap("Failed to create user", err)
	}
	return s.issueToken(ctx, user)
}

func (s *Service) Login(ctx context.Context, input LoginInput) (*AuthResponse, error) {
	input = input.Normalize()
	if input.Email == "" || input.Password == "" {
		return nil, httpx.NewAppError("iam.service", "VALIDATION_ERROR", "Email and password are required", nil)
	}
	user, err := s.store.GetUserByEmail(ctx, input.Email)
	if errors.Is(err, ErrNotFound) {
		return nil, httpx.NewAppError("iam.service", "INVALID_CREDENTIALS", "Invalid email or password", ErrUnauthorized)
	}
	if err != nil {
		return nil, iamWrap("Failed to find user", err)
	}
	if !user.IsActive {
		return nil, httpx.NewAppError("iam.service", "INVALID_CREDENTIALS", "Invalid email or password", ErrUnauthorized)
	}

	ok, err := VerifyPassword(input.Password, user.PasswordHash)
	if err != nil {
		return nil, httpx.NewAppError("iam.service", "INTERNAL_ERROR", "Failed to verify password", fmt.Errorf("verify password: %w", err))
	}
	if !ok {
		return nil, httpx.NewAppError("iam.service", "INVALID_CREDENTIALS", "Invalid email or password", ErrUnauthorized)
	}
	return s.issueToken(ctx, user)
}

func (s *Service) Logout(ctx context.Context, tokenHash string) error {
	return s.store.RevokeSession(ctx, tokenHash)
}

func (s *Service) Authenticate(ctx context.Context, token string) (*mid.UserContext, error) {
	claims, err := s.tokenMgr.VerifyAccessToken(token)
	if err != nil {
		return nil, err
	}
	publicID, err := uuid.Parse(claims.Sub)
	if err != nil {
		return nil, ErrInvalidSession
	}
	tokenHash := HashToken(token)
	if _, err := s.store.GetSessionByTokenHash(ctx, tokenHash); err != nil {
		return nil, err
	}
	return &mid.UserContext{
		ID:        claims.UID,
		PublicID:  publicID,
		Email:     claims.Email,
		JTI:       claims.JTI,
		ExpiresAt: time.Unix(claims.ExpiresAt, 0).UTC(),
	}, nil
}

func (s *Service) Me(ctx context.Context, userID int64) (*User, error) {
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return nil, iamWrap("Failed to find user", err)
	}
	return user, nil
}

func (s *Service) issueToken(ctx context.Context, user *User) (*AuthResponse, error) {
	token, _, expiresAt, err := s.tokenMgr.GenerateAccessToken(user.ID, user.PublicID, user.Email)
	if err != nil {
		return nil, httpx.NewAppError("iam.service", "INTERNAL_ERROR", "Failed to generate session token", fmt.Errorf("generate token: %w", err))
	}
	tokenHash := HashToken(token)
	if _, err := s.store.CreateSession(ctx, user.ID, tokenHash, expiresAt); err != nil {
		return nil, iamWrap("Failed to create session", err)
	}
	user.PasswordHash = ""
	return &AuthResponse{Token: token, ExpiresAt: expiresAt, User: user}, nil
}
