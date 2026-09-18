SHELL := /bin/sh

LOAD_ENV = if [ -f ./.env ]; then while IFS='=' read -r key rest || [ -n "$$key" ]; do case "$$key" in ''|"\#"*|*[!A-Za-z0-9_]*) continue ;; esac; if ! printenv "$$key" >/dev/null 2>&1; then export "$$key=$$rest"; fi; done < ./.env; fi

.PHONY: migrate seed run test test-pg check

migrate:
	@$(LOAD_ENV); go run ./cmd/migrate

seed:
	@$(LOAD_ENV); go run ./cmd/seed

run: seed
	@$(LOAD_ENV); go run ./cmd/api

test:
	go test -short ./...

test-pg:
	go test ./...

check:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.27.0 generate
	git diff --exit-code -- internal/modules/iam/sqlc internal/modules/tasks/sqlc
	go test -short ./...
	go vet ./...
	go build ./...
