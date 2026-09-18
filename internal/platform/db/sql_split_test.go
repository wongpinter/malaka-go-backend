package db

import (
	"strings"
	"testing"
)

func TestSplitSQLStatements(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "dollar quoted body stays one statement",
			sql:  "CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql;\nSELECT 1;",
			want: []string{
				"CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql",
				"SELECT 1",
			},
		},
		{
			name: "tagged dollar quotes",
			sql:  "DO $tag$ SELECT 1; $tag$;\nSELECT 2;",
			want: []string{"DO $tag$ SELECT 1; $tag$", "SELECT 2"},
		},
		{
			name: "numbered placeholders are not dollar quotes",
			sql:  "UPDATE t SET a = $1 WHERE id = $2; SELECT $$d; body$$ AS doc",
			want: []string{"UPDATE t SET a = $1 WHERE id = $2", "SELECT $$d; body$$ AS doc"},
		},
		{
			name: "semicolon in string literal",
			sql:  "INSERT INTO t VALUES ('a;b');\nINSERT INTO t VALUES ('c');",
			want: []string{"INSERT INTO t VALUES ('a;b')", "INSERT INTO t VALUES ('c')"},
		},
		{
			name: "semicolon in comments",
			sql:  "-- drop; everything\nSELECT 1; /* keep; me */ SELECT 2;",
			want: []string{"-- drop; everything\nSELECT 1", "/* keep; me */ SELECT 2"},
		},
		{
			name: "blank statements are dropped",
			sql:  "SELECT 1;;\n\n;SELECT 2;\n",
			want: []string{"SELECT 1", "SELECT 2"},
		},
		{
			name: "dollar without a tag is not a quote",
			sql:  "SELECT 1$; SELECT 2;",
			want: []string{"SELECT 1$", "SELECT 2"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitSQLStatements(tc.sql)
			if len(got) != len(tc.want) {
				t.Fatalf("want %d statements, got %d: %q", len(tc.want), len(got), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("statement %d:\nwant %q\ngot  %q", i, tc.want[i], got[i])
				}
			}
		})
	}
}

func TestMigrationStatementsCoverTheSchema(t *testing.T) {
	statements, err := MigrationStatements()
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) < 10 {
		t.Fatalf("expected the full migration, got %d statements", len(statements))
	}
	joined := strings.Join(statements, ";\n")
	for _, want := range []string{
		"CREATE SCHEMA IF NOT EXISTS iam",
		"CREATE TABLE IF NOT EXISTS iam.users",
		"CREATE TABLE IF NOT EXISTS public.tasks",
		"CREATE TABLE IF NOT EXISTS public.audit_logs",
		"CREATE TABLE IF NOT EXISTS public.outbox_events",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("migration is missing %q", want)
		}
	}

	var function string
	for _, s := range statements {
		if strings.Contains(s, "CREATE OR REPLACE FUNCTION") {
			function = s
		}
	}
	if function == "" {
		t.Fatal("uuid_generate_v7 definition not found")
	}
	if strings.Count(function, "$$") != 2 {
		t.Fatalf("dollar-quoted function body was split apart: %q", function)
	}
}
