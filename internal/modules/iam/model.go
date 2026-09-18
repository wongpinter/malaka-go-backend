package iam

import (
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound       = errors.New("iam: not found")
	ErrUnauthorized   = errors.New("iam: unauthorized")
	ErrEmailTaken     = errors.New("iam: email already registered")
	ErrInvalidSession = errors.New("iam: session invalid or expired")
)

type User struct {
	ID           int64     `json:"-"`
	PublicID     uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	PasswordHash string    `json:"-"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
}

type Session struct {
	ID        int64      `json:"-"`
	PublicID  uuid.UUID  `json:"id"`
	UserID    int64      `json:"-"`
	TokenHash string     `json:"-"`
	ExpiresAt time.Time  `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"-"`
}

type RegisterInput struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (in RegisterInput) Validate() []string {
	var msgs []string
	switch {
	case in.Email == "":
		msgs = append(msgs, "Email, name, and password are required")
	case len(in.Email) > 255:
		msgs = append(msgs, "Email must be at most 255 characters")
	case func() bool { _, err := mail.ParseAddress(in.Email); return err != nil }():
		msgs = append(msgs, "Email format is invalid")
	}
	switch {
	case in.Name == "":
		msgs = append(msgs, "Email, name, and password are required")
	case len(in.Name) > 100:
		msgs = append(msgs, "Name must be at most 100 characters")
	}
	switch {
	case in.Password == "":
		msgs = append(msgs, "Email, name, and password are required")
	case len(in.Password) < 8:
		msgs = append(msgs, "Password must be at least 8 characters")
	case len(in.Password) > 128:
		msgs = append(msgs, "Password must be at most 128 characters")
	}
	return msgs
}

func (in RegisterInput) Normalize() RegisterInput {
	return RegisterInput{
		Email:    strings.ToLower(strings.TrimSpace(in.Email)),
		Name:     strings.TrimSpace(in.Name),
		Password: in.Password,
	}
}

type LoginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (in LoginInput) Normalize() LoginInput {
	return LoginInput{Email: strings.ToLower(strings.TrimSpace(in.Email)), Password: in.Password}
}

type AuthResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      *User     `json:"user"`
}
