package db

import (
	"fmt"
	"time"
)

const sqliteTimeFormat = "2006-01-02T15:04:05.000000000Z"

func SQLiteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeFormat)
}

func ParseSQLiteTime(s string) (time.Time, error) {
	for _, layout := range []string{sqliteTimeFormat, time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("parse sqlite time %q", s)
}
