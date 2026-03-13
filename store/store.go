package store

import (
	"errors"
	"strings"
	"time"
)

// ErrConflict is returned when an insert violates a unique constraint.
var ErrConflict = errors.New("conflict")

// isUniqueConstraint detects SQLite unique constraint violation errors.
func isUniqueConstraint(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// scanner abstracts *sql.Row and *sql.Rows for shared scan helpers.
type scanner interface {
	Scan(dest ...any) error
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, _ = time.Parse("2006-01-02 15:04:05", s)
	}
	return t
}

func nowStr() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func strPtr(s string) *string { return &s }
