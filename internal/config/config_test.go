package config

import "testing"

func TestValidateProductionSecrets(t *testing.T) {
	base := &Config{
		Env:       "production",
		DBDriver:  "postgres",
		DBURL:     "postgres://user:pass@db/malaka",
		JWTSecret: "a-long-random-jwt-secret-that-is-32-bytes",
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid production config rejected: %v", err)
	}

	for _, secret := range []string{
		"super-secret-default-malaka-jwt-key-change-in-production",
		"replace-with-random-jwt-secret",
	} {
		base.JWTSecret = secret
		if err := base.Validate(); err == nil {
			t.Fatalf("insecure JWT secret accepted in production: %q", secret)
		}
	}

	base.JWTSecret = "a-long-random-jwt-secret-that-is-32-bytes"
	base.CORS.AllowCredentials = true
	base.CORS.AllowedOrigins = []string{"*"}
	if err := base.Validate(); err == nil {
		t.Fatal("wildcard credentialed CORS accepted in production")
	}
}
