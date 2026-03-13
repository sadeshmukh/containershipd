package store

import "time"

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
