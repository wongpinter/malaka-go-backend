package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"malaka/internal/config"
	"malaka/internal/modules/iam"
	"malaka/internal/platform/db"
)

func main() {
	cfg := config.Get()
	if err := cfg.Validate(); err != nil {
		slog.Error("invalid seed configuration", "error", err)
		os.Exit(1)
	}
	database, err := db.Open(db.Driver(cfg.DBDriver), cfg.DBURL, db.PoolConfig{
		MaxOpenConns: 5,
		MaxIdleConns: 2,
	})
	if err != nil {
		slog.Error("db connection failed", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	ctx := context.Background()
	n, err := seedUsers(ctx, database)
	if err != nil {
		slog.Error("seed failed", "error", err)
		os.Exit(1)
	}
	fmt.Printf("Seeded %d user(s).\n", n)
}

type seedUser struct {
	email    string
	name     string
	password string
}

func seedUsers(ctx context.Context, executor db.Executor) (int, error) {
	users := []seedUser{
		{email: "sugeng@example.com", name: "Sugeng Widodo", password: "Password123!"},
		{email: "anita@example.com", name: "Anita Rahayu", password: "Password123!"},
		{email: "adit@example.com", name: "Aditya Pratama", password: "Password123!"},
	}

	store := iam.NewStore(executor)
	count := 0
	for _, u := range users {
		hash, err := iam.HashPassword(u.password)
		if err != nil {
			return count, fmt.Errorf("hash password for %s: %w", u.email, err)
		}
		user, err := store.UpsertUser(ctx, u.email, u.name, hash)
		if err != nil {
			return count, fmt.Errorf("upsert user %s: %w", u.email, err)
		}
		slog.Info("seeded user", "email", user.Email, "id", user.PublicID)
		count++
	}
	return count, nil
}
