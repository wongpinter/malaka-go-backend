package iam

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidToken = errors.New("iam: invalid jwt token")
	ErrTokenExpired = errors.New("iam: jwt token has expired")
)

type JWTClaims struct {
	JTI       string `json:"jti"`
	Sub       string `json:"sub"`
	UID       int64  `json:"uid"`
	Email     string `json:"email"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

type TokenManager struct {
	secret []byte
	ttl    time.Duration
}

func NewTokenManager(secret string, ttl time.Duration) *TokenManager {
	if ttl == 0 {
		ttl = 15 * time.Minute
	}
	return &TokenManager{secret: []byte(secret), ttl: ttl}
}

func (tm *TokenManager) GenerateAccessToken(uid int64, publicID uuid.UUID, email string) (string, string, time.Time, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(tm.ttl)
	jti := uuid.NewString()

	claims := JWTClaims{
		JTI:       jti,
		Sub:       publicID.String(),
		UID:       uid,
		Email:     email,
		IssuedAt:  now.Unix(),
		ExpiresAt: expiresAt.Unix(),
	}

	headerJSON, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to marshal jwt header: %w", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to marshal jwt claims: %w", err)
	}

	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJSON)
	encodedClaims := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signingInput := encodedHeader + "." + encodedClaims
	mac := hmac.New(sha256.New, tm.secret)
	mac.Write([]byte(signingInput))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return signingInput + "." + signature, jti, expiresAt, nil
}

func (tm *TokenManager) VerifyAccessToken(tokenString string) (*JWTClaims, error) {
	parts := strings.Split(tokenString, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidToken
	}
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, ErrInvalidToken
	}
	if header.Alg != "HS256" || header.Typ != "JWT" {
		return nil, ErrInvalidToken
	}

	signingInput := parts[0] + "." + parts[1]
	providedSignature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrInvalidToken
	}

	mac := hmac.New(sha256.New, tm.secret)
	mac.Write([]byte(signingInput))
	if subtle.ConstantTimeCompare(providedSignature, mac.Sum(nil)) != 1 {
		return nil, ErrInvalidToken
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidToken
	}

	var claims JWTClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return nil, ErrInvalidToken
	}

	if time.Now().UTC().Unix() > claims.ExpiresAt {
		return nil, ErrTokenExpired
	}
	return &claims, nil
}

func HashToken(rawToken string) string {
	hash := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(hash[:])
}
