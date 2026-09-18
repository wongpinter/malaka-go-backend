package db

import (
	"fmt"
	"strings"
)

func MigrationStatements() ([]string, error) {
	const file = "migrations/000001_init.up.sql"
	b, err := MigrationsFS.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	return SplitSQLStatements(string(b)), nil
}

func SplitSQLStatements(sql string) []string {
	var (
		out    []string
		cur    strings.Builder
		dollar string
		quote  bool
		ident  bool
		line   bool
		block  bool
	)

	for i := 0; i < len(sql); i++ {
		c := sql[i]

		switch {
		case line:
			cur.WriteByte(c)
			if c == '\n' {
				line = false
			}
		case block:
			cur.WriteByte(c)
			if c == '*' && i+1 < len(sql) && sql[i+1] == '/' {
				cur.WriteByte('/')
				i++
				block = false
			}
		case quote:
			cur.WriteByte(c)
			if c == '\'' {
				quote = false
			}
		case ident:
			cur.WriteByte(c)
			if c == '"' {
				ident = false
			}
		case dollar != "":
			if strings.HasPrefix(sql[i:], dollar) {
				cur.WriteString(dollar)
				i += len(dollar) - 1
				dollar = ""
				continue
			}
			cur.WriteByte(c)
		default:
			switch {
			case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
				line = true
				cur.WriteByte(c)
			case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
				block = true
				cur.WriteByte(c)
			case c == '\'':
				quote = true
				cur.WriteByte(c)
			case c == '"':
				ident = true
				cur.WriteByte(c)
			case c == '$':
				if tag, n := scanDollarTag(sql[i:]); n > 0 {
					dollar = tag
					cur.WriteString(tag)
					i += n - 1
				} else {
					cur.WriteByte(c)
				}
			case c == ';':
				if s := strings.TrimSpace(cur.String()); s != "" {
					out = append(out, s)
				}
				cur.Reset()
			default:
				cur.WriteByte(c)
			}
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

func scanDollarTag(s string) (string, int) {
	if len(s) < 2 || s[0] != '$' {
		return "", 0
	}
	first := true
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case c == '$':
			return s[:i+1], i + 1
		case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			first = false
		case c >= '0' && c <= '9':
			if first {
				return "", 0
			}
		default:
			return "", 0
		}
	}
	return "", 0
}
