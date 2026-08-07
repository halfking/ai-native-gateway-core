// Package sessionarchive implements session_summaries archival (docs/omni-ref3 M5).
//
// Archives old inactive summaries to reduce active table size while preserving
// data for compliance/audit. Archived summaries (archived_at IS NOT NULL) are
// excluded from active queries but remain in the table.
package sessionarchive

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Archiver marks old inactive summaries as archived.
type Archiver struct {
	db *pgxpool.Pool
	// InactivityThreshold: summaries not accessed for this duration are eligible
	inactivityThreshold time.Duration
	// SessionEndThreshold: session must have ended this long ago
	sessionEndThreshold time.Duration
}

// NewArchiver creates a new archiver with default thresholds.
func NewArchiver(db *pgxpool.Pool) *Archiver {
	return &Archiver{
		db:                  db,
		inactivityThreshold: 30 * 24 * time.Hour, // 30 days
		sessionEndThreshold: 30 * 24 * time.Hour, // 30 days
	}
}

// SetInactivityThreshold sets the inactivity threshold (for testing).
func (a *Archiver) SetInactivityThreshold(d time.Duration) {
	a.inactivityThreshold = d
}

// SetSessionEndThreshold sets the session end threshold (for testing).
func (a *Archiver) SetSessionEndThreshold(d time.Duration) {
	a.sessionEndThreshold = d
}

// ArchiveResult contains archival statistics.
type ArchiveResult struct {
	ArchivedCount int
	Cutoff        time.Time
	Duration      time.Duration
}

// Archive marks old inactive summaries as archived.
//
// A summary is eligible for archival if:
//   - session ended (last_request_at < sessionEndThreshold ago)
//   - not accessed recently (last_accessed_at < inactivityThreshold ago OR NULL)
//   - archived_at IS NULL
//
// Returns the number of summaries archived.
func (a *Archiver) Archive(ctx context.Context) (*ArchiveResult, error) {
	if a.db == nil {
		return &ArchiveResult{}, nil
	}

	start := time.Now()
	now := time.Now()
	sessionEndCutoff := now.Add(-a.sessionEndThreshold)
	inactivityCutoff := now.Add(-a.inactivityThreshold)

	query := `
		UPDATE session_summaries
		SET archived_at = NOW()
		WHERE archived_at IS NULL
		  AND last_request_at < $1
		  AND (last_accessed_at IS NULL OR last_accessed_at < $2)
	`

	result, err := a.db.Exec(ctx, query, sessionEndCutoff, inactivityCutoff)
	if err != nil {
		return nil, err
	}

	archivedCount := int(result.RowsAffected())
	duration := time.Since(start)

	slog.InfoContext(ctx, "session_summaries archival completed",
		"archived_count", archivedCount,
		"session_end_cutoff", sessionEndCutoff.Format(time.RFC3339),
		"inactivity_cutoff", inactivityCutoff.Format(time.RFC3339),
		"duration_ms", duration.Milliseconds(),
	)

	return &ArchiveResult{
		ArchivedCount: archivedCount,
		Cutoff:        inactivityCutoff,
		Duration:      duration,
	}, nil
}

// ArchiveStats returns archival statistics.
type ArchiveStats struct {
	TotalSummaries    int64
	ArchivedSummaries int64
	ActiveSummaries   int64
	ArchivalRate      float64
}

// GetStats returns current archival statistics.
func (a *Archiver) GetStats(ctx context.Context) (*ArchiveStats, error) {
	if a.db == nil {
		return &ArchiveStats{}, nil
	}

	var total, archived int64
	err := a.db.QueryRow(ctx, `
		SELECT
			COUNT(*) AS total,
			COUNT(archived_at) AS archived
		FROM session_summaries
	`).Scan(&total, &archived)
	if err != nil {
		return nil, err
	}

	active := total - archived
	archivalRate := 0.0
	if total > 0 {
		archivalRate = float64(archived) / float64(total)
	}

	return &ArchiveStats{
		TotalSummaries:    total,
		ArchivedSummaries: archived,
		ActiveSummaries:   active,
		ArchivalRate:      archivalRate,
	}, nil
}
