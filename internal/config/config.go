package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type CORSConfig struct {
	AllowedOrigins   []string
	AllowedMethods   []string
	AllowedHeaders   []string
	AllowCredentials bool
	MaxAge           int
}

type Config struct {
	Env            string
	Port           string
	DBDriver       string
	DBURL          string
	DBMaxOpenConns int
	DBMaxIdleConns int
	DBConnLifetime time.Duration
	LogLevel       string
	JWTSecret      string
	IdempotencyTTL time.Duration
	CORS           CORSConfig
}

var (
	once sync.Once
	cfg  *Config
)

func Get() *Config {
	once.Do(func() {
		driver := strings.ToLower(os.Getenv("DB_DRIVER"))
		if driver == "" {
			if strings.HasPrefix(strings.ToLower(os.Getenv("DATABASE_URL")), "postgres") {
				driver = "postgres"
			} else {
				driver = "sqlite"
			}
		}
		defaultURL := "./data/malaka.db"
		if driver == "postgres" {
			defaultURL = "postgres://postgres:postgres@localhost:5432/malaka?sslmode=disable"
		}
		cfg = &Config{
			Env:            env("ENV", "dev"),
			Port:           env("PORT", "8080"),
			DBDriver:       driver,
			DBURL:          env("DATABASE_URL", defaultURL),
			DBMaxOpenConns: envInt("DB_MAX_OPEN_CONNS", 25),
			DBMaxIdleConns: envInt("DB_MAX_IDLE_CONNS", 5),
			DBConnLifetime: envDuration("DB_CONN_LIFETIME", 5*time.Minute),
			LogLevel:       env("LOG_LEVEL", "info"),
			JWTSecret:      env("JWT_SECRET", "super-secret-default-malaka-jwt-key-change-in-production"),
			IdempotencyTTL: envDuration("IDEMPOTENCY_TTL", 24*time.Hour),
			CORS: CORSConfig{
				AllowedOrigins:   split(env("CORS_ORIGINS", "http://localhost:3000,http://localhost:5173")),
				AllowedMethods:   split(env("CORS_METHODS", "GET,POST,PUT,PATCH,DELETE,OPTIONS")),
				AllowedHeaders:   split(env("CORS_HEADERS", "Authorization,Content-Type,X-Request-ID,X-Correlation-ID,Idempotency-Key")),
				AllowCredentials: envBool("CORS_CREDENTIALS", true),
				MaxAge:           envInt("CORS_MAX_AGE", 3600),
			},
		}
	})
	return cfg
}

func (c *Config) Validate() error {
	if c.DBDriver != "sqlite" && c.DBDriver != "postgres" {
		return fmt.Errorf("DB_DRIVER must be sqlite or postgres")
	}
	if strings.EqualFold(c.Env, "production") && c.DBDriver != "postgres" {
		return fmt.Errorf("DB_DRIVER must be postgres in production")
	}
	if len(c.JWTSecret) < 32 {
		return fmt.Errorf("JWT_SECRET must be at least 32 characters")
	}
	if !strings.EqualFold(c.Env, "production") {
		return nil
	}
	if isPlaceholder(c.DBURL) || c.DBURL == "postgres://postgres:postgres@localhost:5432/malaka?sslmode=disable" {
		return fmt.Errorf("DATABASE_URL must be set in production")
	}
	if isPlaceholder(c.JWTSecret) || c.JWTSecret == "super-secret-default-malaka-jwt-key-change-in-production" {
		return fmt.Errorf("JWT_SECRET must be set in production")
	}
	if c.CORS.AllowCredentials {
		for _, origin := range c.CORS.AllowedOrigins {
			if strings.TrimSpace(origin) == "*" {
				return fmt.Errorf("CORS_ORIGINS cannot contain * when CORS_CREDENTIALS is enabled")
			}
		}
	}
	return nil
}

func isPlaceholder(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "" || strings.Contains(value, "replace-with") || strings.Contains(value, "change-in-production")
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return strings.ToLower(v) == "true" || v == "1"
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func split(s string) []string {
	parts := strings.Split(s, ",")
	res := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}
