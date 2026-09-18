package slogx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"malaka/pkg/slogx"
)

func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	return slogx.New(slogx.Config{Level: slog.LevelInfo, Format: "json", Output: buf})
}

func TestRedactsSensitiveKeys(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf)

	logger.InfoContext(context.Background(), "login attempt",
		slog.String("username", "admin"),
		slog.String("password", "superSecret123!"),
		slog.String("token", "jwt.token.here"),
		slog.String("api_key", "key-123"),
	)

	var logged map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logged); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if logged["username"] != "admin" {
		t.Errorf("non-sensitive field was altered: %v", logged["username"])
	}
	for _, key := range []string{"password", "token", "api_key"} {
		if logged[key] != slogx.DefaultMask {
			t.Errorf("%s must be masked, got %v", key, logged[key])
		}
	}
}

func TestRedactsNestedStructures(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf)

	payload := map[string]any{
		"title": "quarterly report",
		"owner": map[string]any{"email": "owner@example.com", "password": "p@ssw0rd"},
		"items": []any{map[string]any{"id": 1, "token": "abc"}, map[string]any{"id": 2}},
	}
	logger.InfoContext(context.Background(), "payload", slog.Any("payload", payload))

	var logged map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logged); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	root, ok := logged["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload was not expanded: %T", logged["payload"])
	}
	if root["title"] != "quarterly report" {
		t.Errorf("non-sensitive value altered: %v", root["title"])
	}
	owner := root["owner"].(map[string]any)
	if owner["password"] != slogx.DefaultMask || owner["email"] != "owner@example.com" {
		t.Errorf("nested map redaction failed: %v", owner)
	}
	items := root["items"].([]any)
	if items[0].(map[string]any)["token"] != slogx.DefaultMask {
		t.Errorf("slice element redaction failed: %v", items[0])
	}
}

func TestKeepsErrorValues(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf)

	logger.ErrorContext(context.Background(), "failed", slog.Any("cause", errors.New("connection refused")))

	if !strings.Contains(buf.String(), "connection refused") {
		t.Fatalf("error values must stay readable: %s", buf.String())
	}
}

func TestEnrichesFromContext(t *testing.T) {
	var buf bytes.Buffer
	logger := slogx.New(slogx.Config{
		Level:  slog.LevelInfo,
		Format: "json",
		Output: &buf,
		Extractors: []slogx.ContextExtractor{
			slogx.ExtractorFunc(func(ctx context.Context) []slog.Attr {
				if v, ok := ctx.Value(correlationKey{}).(string); ok {
					return []slog.Attr{slog.String("cid", v)}
				}
				return nil
			}),
		},
	})

	ctx := context.WithValue(context.Background(), correlationKey{}, "cid-123")
	logger.InfoContext(ctx, "handled request", slog.String("path", "/api/v1/tasks"))

	var logged map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logged); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if logged["cid"] != "cid-123" || logged["path"] != "/api/v1/tasks" {
		t.Fatalf("context enrichment failed: %v", logged)
	}
}

type correlationKey struct{}

func TestRedactJSONMaskedFieldsAndKeepsOthers(t *testing.T) {
	input := []byte(`{"user":"admin","password":"secret","nested":{"token":"jwt","title":"Quarterly report"},"tags":["a","b"]}`)

	redacted, err := slogx.RedactJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(redacted, &m); err != nil {
		t.Fatal(err)
	}
	if m["password"] != slogx.DefaultMask || m["user"] != "admin" {
		t.Fatalf("top-level redaction failed: %v", m)
	}
	nested := m["nested"].(map[string]any)
	if nested["token"] != slogx.DefaultMask || nested["title"] != "Quarterly report" {
		t.Fatalf("nested redaction failed: %v", nested)
	}
	if len(m["tags"].([]any)) != 2 {
		t.Fatalf("non-object values were dropped: %v", m["tags"])
	}

	if out, err := slogx.RedactJSON(nil); err != nil || out != nil {
		t.Fatalf("empty input must pass through: %v %v", out, err)
	}
	if _, err := slogx.RedactJSON([]byte("not json")); err == nil {
		t.Fatal("invalid JSON must report an error")
	}
}

func TestErrorTypeDoesNotLeakMessage(t *testing.T) {
	attr := slogx.ErrorType(errors.New("password=hunter2"))
	if strings.Contains(attr.Value.String(), "hunter2") {
		t.Fatalf("ErrorType leaked the message: %s", attr.Value.String())
	}
	if attr.Value.String() != "*errors.errorString" {
		t.Fatalf("unexpected error type: %s", attr.Value.String())
	}
}
