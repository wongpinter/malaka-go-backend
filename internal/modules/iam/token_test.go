package iam

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func newTestTokenManager() *TokenManager {
	return NewTokenManager("test-secret-at-least-32-chars!!", 15*time.Minute)
}

func TestToken_RoundTrip(t *testing.T) {
	tm := newTestTokenManager()
	publicID := uuid.New()
	token, jti, expiresAt, err := tm.GenerateAccessToken(42, publicID, "user@example.com")
	if err != nil {
		t.Fatalf("GenerateAccessToken failed: %v", err)
	}
	if jti == "" {
		t.Fatal("expected non-empty JTI")
	}
	claims, err := tm.VerifyAccessToken(token)
	if err != nil {
		t.Fatalf("VerifyAccessToken failed: %v", err)
	}
	if claims.UID != 42 || claims.Sub != publicID.String() || claims.Email != "user@example.com" || claims.JTI != jti {
		t.Fatalf("claims mismatch: %+v", claims)
	}
	if !expiresAt.After(time.Now()) {
		t.Fatal("expected future expiry")
	}
}

func TestToken_TamperedSignature(t *testing.T) {
	tm := newTestTokenManager()
	token, _, _, err := tm.GenerateAccessToken(1, uuid.New(), "a@b.c")
	if err != nil {
		t.Fatalf("GenerateAccessToken failed: %v", err)
	}
	tampered := token[:len(token)-3] + "xyz"
	if _, err := tm.VerifyAccessToken(tampered); err == nil {
		t.Fatal("expected tampered token to be rejected")
	}
}

func TestToken_WrongSecret(t *testing.T) {
	tm := newTestTokenManager()
	token, _, _, err := tm.GenerateAccessToken(1, uuid.New(), "a@b.c")
	if err != nil {
		t.Fatalf("GenerateAccessToken failed: %v", err)
	}
	other := NewTokenManager("another-secret-also-32-chars-long!", 15*time.Minute)
	if _, err := other.VerifyAccessToken(token); err == nil {
		t.Fatal("expected token signed with different secret to be rejected")
	}
}

func TestToken_Expired(t *testing.T) {
	tm := NewTokenManager("test-secret-at-least-32-chars!!", -1*time.Minute)
	token, _, _, err := tm.GenerateAccessToken(1, uuid.New(), "a@b.c")
	if err != nil {
		t.Fatalf("GenerateAccessToken failed: %v", err)
	}
	if _, err := tm.VerifyAccessToken(token); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestToken_Malformed(t *testing.T) {
	tm := newTestTokenManager()
	for _, tok := range []string{"", "not-a-token", "a.b", "a.b.c.d"} {
		if _, err := tm.VerifyAccessToken(tok); err == nil {
			t.Fatalf("expected %q to be rejected", tok)
		}
	}
}
