package iam

import (
	"strings"
	"testing"
)

func TestHashPassword_RoundTrip(t *testing.T) {
	hash, err := HashPassword("Password123!")
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("expected argon2id PHC string, got %q", hash[:min(len(hash), 20)])
	}
	ok, err := VerifyPassword("Password123!", hash)
	if err != nil || !ok {
		t.Fatalf("expected successful verification, got ok=%v err=%v", ok, err)
	}
}

func TestVerifyPassword_WrongPassword(t *testing.T) {
	hash, err := HashPassword("Password123!")
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}
	ok, err := VerifyPassword("WrongPassword1!", hash)
	if err != nil {
		t.Fatalf("VerifyPassword returned error: %v", err)
	}
	if ok {
		t.Fatal("expected wrong password to be rejected")
	}
}

func TestVerifyPassword_TamperedHash(t *testing.T) {
	hash, err := HashPassword("Password123!")
	if err != nil {
		t.Fatalf("HashPassword failed: %v", err)
	}
	tampered := hash[:len(hash)-2] + "zz"
	ok, err := VerifyPassword("Password123!", tampered)
	if ok {
		t.Fatal("expected tampered hash to be rejected")
	}
	_ = err
}

func TestVerifyPassword_MalformedHash(t *testing.T) {
	ok, err := VerifyPassword("Password123!", "not-a-hash")
	if err == nil || ok {
		t.Fatalf("expected malformed hash to error, got ok=%v err=%v", ok, err)
	}
}

func TestHashPassword_UniqueSalts(t *testing.T) {
	h1, _ := HashPassword("Password123!")
	h2, _ := HashPassword("Password123!")
	if h1 == h2 {
		t.Fatal("expected distinct salts to produce distinct hashes")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
