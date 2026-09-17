package taskprofile

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// corrections.go — persistence for per-request human corrections of the
// AUTO task-type assignment (table task_type_corrections, migration 724).
//
// The store is a thin DAO over the shared pool; it holds no state, so
// cmd/gateway and the admin handlers may each construct their own instance.

// ErrUnknownRequest is returned when no auto_route_selections row exists for
// the request being corrected (nothing to correct against).
var ErrUnknownRequest = errors.New("taskprofile: no auto_route_selections row for request_id")

// ErrAlreadyCorrected is returned when the request already has a correction
// (request_id is UNIQUE; first annotation wins, mirroring
// training_human_annotations' ON CONFLICT DO NOTHING semantics).
var ErrAlreadyCorrected = errors.New("taskprofile: correction already exists for request_id")

// Correction is one stored human correction.
type Correction struct {
	ID                   int64     `json:"id"`
	RequestID            string    `json:"request_id"`
	AutoTaskType         string    `json:"auto_task_type"`
	HumanTaskType        string    `json:"human_task_type"`
	Agrees               bool      `json:"agrees"`
	ClassifierConfidence *float64  `json:"classifier_confidence"`
	Profile              *string   `json:"profile"`
	Annotator            string    `json:"annotator"`
	Reason               string    `json:"reason"`
	CreatedAt            time.Time `json:"created_at"`
}

// CorrectionStore reads/writes task_type_corrections.
type CorrectionStore struct {
	pool *pgxpool.Pool

	// recorder is the optional FeedbackRecorder (the in-process
	// classification feedback aggregator). Set once at wiring time.
	recorder FeedbackRecorder
}

// NewCorrectionStore constructs a store. A nil pool yields a store whose
// methods fail fast (construction-time nil pools are a wiring bug, but the
// handlers still need a non-panicking 503 path — they check Pool()).
func NewCorrectionStore(pool *pgxpool.Pool) *CorrectionStore {
	return &CorrectionStore{pool: pool}
}

// Pool exposes the underlying pool (nil-check for handlers).
func (s *CorrectionStore) Pool() *pgxpool.Pool { return s.pool }

// SetRecorder attaches the optional FeedbackRecorder. Safe to call before
// serving; reads are via the atomic-free "set once at startup" convention.
func (s *CorrectionStore) SetRecorder(r FeedbackRecorder) { s.recorder = r }

// CreateCorrectionInput is the validated input for Record.
type CreateCorrectionInput struct {
	RequestID     string
	HumanTaskType string
	Annotator     string
	Reason        string
}

// Record resolves the request's auto task type from auto_route_selections_all
// and inserts the correction. It returns the stored row.
//
// The auto lookup deliberately reuses the same source-of-truth view as the
// P2.1 annotation handlers (auto_route_selections_all, latest row per
// request) so the two annotation flows can never disagree about what the
// classifier originally said.
func (s *CorrectionStore) Record(ctx context.Context, in CreateCorrectionInput) (Correction, error) {
	if s.pool == nil {
		return Correction{}, errors.New("taskprofile: no DB pool")
	}

	var autoType string
	var confidence *float64
	var profile *string
	if err := s.pool.QueryRow(ctx, `
		SELECT task_type, confidence, profile
		FROM auto_route_selections_all
		WHERE request_id = $1
		ORDER BY ts DESC
		LIMIT 1
	`, in.RequestID).Scan(&autoType, &confidence, &profile); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Correction{}, ErrUnknownRequest
		}
		return Correction{}, err
	}

	agrees := autoType == in.HumanTaskType
	var out Correction
	err := s.pool.QueryRow(ctx, `
		INSERT INTO task_type_corrections
			(request_id, auto_task_type, human_task_type, agrees,
			 classifier_confidence, profile, annotator, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (request_id) DO NOTHING
		RETURNING id, request_id, auto_task_type, human_task_type, agrees,
		          classifier_confidence, profile, annotator, reason, created_at
	`, in.RequestID, autoType, in.HumanTaskType, agrees,
		confidence, profile,
		in.Annotator, in.Reason,
	).Scan(&out.ID, &out.RequestID, &out.AutoTaskType, &out.HumanTaskType, &out.Agrees,
		&out.ClassifierConfidence, &out.Profile, &out.Annotator, &out.Reason, &out.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Correction{}, ErrAlreadyCorrected
		}
		return Correction{}, err
	}

	if s.recorder != nil {
		// Wire the in-process classification feedback aggregator: every
		// correction is one classification verdict (correct = human agrees).
		s.recorder.RecordFeedback(out.AutoTaskType, out.Agrees)
	}
	return out, nil
}

// Stats aggregates corrections per auto task type since the given time.
func (s *CorrectionStore) Stats(ctx context.Context, since time.Time) (map[string]CorrectionStat, error) {
	if s.pool == nil {
		return nil, errors.New("taskprofile: no DB pool")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT auto_task_type,
		       COUNT(*),
		       SUM(CASE WHEN agrees THEN 1 ELSE 0 END)::int,
		       SUM(CASE WHEN agrees THEN 0 ELSE 1 END)::int
		FROM task_type_corrections
		WHERE created_at >= $1
		GROUP BY auto_task_type
	`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]CorrectionStat)
	for rows.Next() {
		var stat CorrectionStat
		if err := rows.Scan(&stat.TaskType, &stat.Total, &stat.Agrees, &stat.Corrected); err != nil {
			return nil, err
		}
		if stat.Total > 0 {
			stat.CorrectionRate = float64(stat.Corrected) / float64(stat.Total)
		}
		out[stat.TaskType] = stat
	}
	return out, rows.Err()
}

// correctionRow is the scan shape shared by Recent.
type correctionRow = Correction

// Recent returns the latest corrections (admin review feed).
func (s *CorrectionStore) Recent(ctx context.Context, limit int) ([]correctionRow, error) {
	if s.pool == nil {
		return nil, errors.New("taskprofile: no DB pool")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, request_id, auto_task_type, human_task_type, agrees,
		       classifier_confidence, profile, annotator, reason, created_at
		FROM task_type_corrections
		ORDER BY created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Correction, 0, limit)
	for rows.Next() {
		var c Correction
		if err := rows.Scan(&c.ID, &c.RequestID, &c.AutoTaskType, &c.HumanTaskType, &c.Agrees,
			&c.ClassifierConfidence, &c.Profile, &c.Annotator, &c.Reason, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
