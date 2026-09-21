package dbx

import (
	"fmt"
	"hash/fnv"
	"strings"
	"time"
)

// QueryFact is the sanitized observability record for one CRUD statement.
// It structurally carries no parameter values: raw args, JSON payloads,
// secrets and request bodies can never be attached to it.
type QueryFact struct {
	// QueryID is the fingerprint of the normalized SQL text.
	QueryID string
	// Op is one of insert/select/update/delete.
	Op string
	// Duration is the wall-clock statement time.
	Duration time.Duration
	// Rows is the affected (write) or scanned (read) row count.
	Rows int64
	// SQLState is the pgconn.PgError code when the statement failed, else "".
	SQLState string
	// Table is the manifest table name.
	Table string
}

// Observer receives query facts. Host bridges this to slog/Prometheus/OTel;
// the bridge lives outside the module.
type Observer interface {
	ObserveQuery(fact QueryFact)
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(QueryFact)

// ObserveQuery implements Observer.
func (f ObserverFunc) ObserveQuery(fact QueryFact) { f(fact) }

// Fingerprint derives a stable QueryID from SQL text: runs of whitespace
// collapse to one space and bind placeholders ($1, $2, ...) normalize to $?
// so two statements with the same shape share a fingerprint regardless of
// parameter count or values.
func Fingerprint(sql string) string {
	norm := normalizePlaceholderNumbers(normalizeWhitespace(sql))
	h := fnv.New64a()
	_, _ = h.Write([]byte(norm))
	return fmt.Sprintf("%016x", h.Sum64())
}

func normalizePlaceholderNumbers(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '$' && i+1 < len(s) && isDigit(s[i+1]) {
			b.WriteString("$?")
			i++
			for i < len(s) && isDigit(s[i]) {
				i++
			}
			i-- // compensate for loop increment
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func normalizeWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			inSpace = true
			continue
		}
		if inSpace && b.Len() > 0 {
			b.WriteByte(' ')
		}
		inSpace = false
		b.WriteRune(r)
	}
	return b.String()
}
