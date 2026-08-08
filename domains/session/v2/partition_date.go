package v2

import "time"

// calendarDate returns a midnight UTC timestamp suitable for a PostgreSQL DATE
// partition key. Truncate(24*time.Hour) aligns to the absolute Unix epoch and
// can produce the wrong calendar day for non-UTC timestamps.
func calendarDate(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
