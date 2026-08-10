package modelquality

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DBStorage is a MonitorStorage implementation backed by the Postgres tables
// introduced in migration 350 (model_iq_runs + node_iq_latest).
//
// It is the primary store for the model-IQ system: every node IQ test writes
// one append-only row to model_iq_runs and upserts the node's cached latest +
// aggregate values in node_iq_latest. Reads (latest, history, list-all) query
// those tables, which is what the admin API and the provider-quality ModelIQ
// dimension consume.
//
// A FileStorage can be layered on top via WithFileBackup to keep the legacy
// JSON/JSONL artifacts as an offline mirror — the DB write is authoritative.
type DBStorage struct {
	pool *pgxpool.Pool
	// optionalBackup, when set, receives the same writes (best-effort) so the
	// file-based artifacts continue to exist for offline tooling.
	optionalBackup *FileStorage
}

// NewDBStorage constructs a DB-backed MonitorStorage. The pool must be non-nil.
func NewDBStorage(pool *pgxpool.Pool) *DBStorage {
	return &DBStorage{pool: pool}
}

// WithFileBackup layers a FileStorage that mirrors writes for offline tooling.
// Read paths always prefer the DB.
func (s *DBStorage) WithFileBackup(fs *FileStorage) *DBStorage {
	s.optionalBackup = fs
	return s
}

// SaveReport persists a full benchmark report. We do not store the per-question
// results array in the DB (model_iq_runs is a summary row); callers that need
// the full report can still get it via the optional file backup. The summary
// row + score upsert are the load-bearing writes.
func (s *DBStorage) SaveReport(ctx context.Context, report *BenchmarkReport) error {
	if s.optionalBackup != nil {
		// best-effort, do not fail the DB path on file errors
		_ = s.optionalBackup.SaveReport(ctx, report)
	}
	return nil
}

// SaveScore persists a QualityScore: inserts a model_iq_runs row and upserts
// node_iq_latest. providerID and canonicalID are derived when not set on the
// score by a best-effort lookup so the historical rows stay queryable by
// provider/canonical even when the caller only supplied (credential, model).
func (s *DBStorage) SaveScore(ctx context.Context, score *QualityScore) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("DBStorage: nil pool")
	}
	if score.CredentialID == 0 {
		// node-less (via-gateway) score: nothing to write at the node level.
		// Mirror to file backup if configured, then no-op the DB writes — the
		// node_iq_latest / model_iq_runs tables are keyed on credential_id.
		if s.optionalBackup != nil {
			_ = s.optionalBackup.SaveScore(ctx, score)
		}
		return nil
	}

	// Resolve provider_id + canonical_id from the live binding if the caller
	// did not supply them. model_iq_runs.provider_id is NOT NULL.
	providerID, canonicalID, rawName, err := s.resolveNode(ctx, score)
	if err != nil {
		return err
	}

	status := "success"
	if score.Stability < 100 {
		// Stability is (success-rate*100); below 100 means some questions
		// errored — classify as partial.
		status = "partial"
	}
	if score.Accuracy <= 0 && score.Stability <= 0 {
		status = "failed"
	}

	// 1. Append the run row.
	const insertRun = `
		INSERT INTO model_iq_runs (
			credential_id, provider_id, raw_model_name, canonical_id,
			benchmark_type, total_questions, correct_count, accuracy,
			stability, latency_p95, overall_score, grade,
			probe_kind, trigger_kind, status, error, tested_at
		) VALUES ($1,$2,$3,$4,'mmlu_lite',0,0,$5,$6,$7,$8,$9,$10,'scheduled',$11,$12,$13)`
	_, err = s.pool.Exec(ctx, insertRun,
		score.CredentialID, providerID, rawName, nullableInt64(canonicalID),
		score.Accuracy, nullableFloat(score.Stability), nullableFloat(score.Latency),
		score.OverallScore, nullableStr(score.Grade),
		stringOrDefault(string(score.ProbeKind), "direct"),
		status, nil, // error text + tested_at default
	)
	if err != nil {
		return fmt.Errorf("insert model_iq_runs: %w", err)
	}

	// 2. Upsert node_iq_latest with recomputed aggregates.
	if err := s.upsertNodeLatest(ctx, score.CredentialID, rawName, score); err != nil {
		// non-fatal: the run row is already persisted; latest-cache failure
		// should not invalidate the test result.
		slog.Warn("model_iq: upsert node_iq_latest failed", "err", err,
			"credential_id", score.CredentialID, "model", rawName)
	}

	if s.optionalBackup != nil {
		_ = s.optionalBackup.SaveScore(ctx, score)
	}
	return nil
}

// resolveNode looks up provider_id + canonical_id + raw_model_name for a
// credential+model. The caller-supplied score.ModelName is usually the raw
// provider model name; if it doesn't match a binding we fall back to it verbatim.
func (s *DBStorage) resolveNode(ctx context.Context, score *QualityScore) (providerID, canonicalID int64, rawName string, err error) {
	rawName = score.ModelName
	const q = `
		SELECT c.provider_id,
		       COALESCE(pm.canonical_id, 0),
		       COALESCE(NULLIF(pm.raw_model_name,''), $2)
		FROM credentials c
		JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE c.id = $1 AND lower(pm.raw_model_name) = lower($2)
		LIMIT 1`
	err = s.pool.QueryRow(ctx, q, score.CredentialID, score.ModelName).Scan(&providerID, &canonicalID, &rawName)
	if err != nil {
		// Fall back: maybe the model name is slightly different; just get provider_id.
		rawName = score.ModelName
		var pid int64
		if err2 := s.pool.QueryRow(ctx, `SELECT provider_id FROM credentials WHERE id=$1`, score.CredentialID).Scan(&pid); err2 == nil {
			providerID = pid
			return providerID, 0, rawName, nil
		}
		return 0, 0, rawName, fmt.Errorf("resolve node (cred=%d model=%s): %w", score.CredentialID, score.ModelName, err)
	}
	return providerID, canonicalID, rawName, nil
}

// upsertNodeLatest sets the node's latest score and recomputes avg/min/max +
// sample_count from model_iq_runs so the cache stays self-consistent.
func (s *DBStorage) upsertNodeLatest(ctx context.Context, credentialID int, rawModel string, score *QualityScore) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO node_iq_latest (credential_id, raw_model_name, overall_score, grade,
		                            sample_count, avg_score, min_score, max_score,
		                            tested_at, updated_at)
		VALUES ($1, $2, $3, $4, 1, $3, $3, $3, now(), now())
		ON CONFLICT (credential_id, raw_model_name) DO UPDATE
		  SET overall_score = EXCLUDED.overall_score,
		      grade         = EXCLUDED.grade,
		      tested_at     = now(),
		      updated_at    = now(),
		      sample_count  = (SELECT count(*) FROM model_iq_runs
		                        WHERE credential_id=$1 AND raw_model_name=$2
		                          AND status IN ('success','partial')),
		      avg_score     = (SELECT COALESCE(avg(overall_score), EXCLUDED.overall_score)
		                        FROM model_iq_runs
		                        WHERE credential_id=$1 AND raw_model_name=$2
		                          AND status IN ('success','partial')),
		      min_score     = (SELECT COALESCE(min(overall_score), EXCLUDED.overall_score)
		                        FROM model_iq_runs
		                        WHERE credential_id=$1 AND raw_model_name=$2
		                          AND status IN ('success','partial')),
		      max_score     = (SELECT COALESCE(max(overall_score), EXCLUDED.overall_score)
		                        FROM model_iq_runs
		                        WHERE credential_id=$1 AND raw_model_name=$2
		                          AND status IN ('success','partial'))`,
		credentialID, rawModel, score.OverallScore, nullableStr(score.Grade))
	return err
}

// GetLatestScore returns the most recent QualityScore for a node (or the
// via-gateway aggregate when credentialID == 0). The DB path only stores
// per-node rows (credential_id > 0), so credentialID==0 falls back to file.
func (s *DBStorage) GetLatestScore(ctx context.Context, provider, modelName string, credentialID int) (*QualityScore, error) {
	if credentialID == 0 {
		if s.optionalBackup != nil {
			return s.optionalBackup.GetLatestScore(ctx, provider, modelName, credentialID)
		}
		return nil, nil
	}
	q := `SELECT overall_score::float8, COALESCE(grade,''), accuracy::float8,
	             COALESCE(stability::float8,0), COALESCE(latency_p95,0),
	             COALESCE(probe_kind,'direct'), tested_at
	        FROM model_iq_runs
	       WHERE credential_id=$1 AND lower(raw_model_name)=lower($2)
	         AND status IN ('success','partial')
	       ORDER BY tested_at DESC LIMIT 1`
	var sc QualityScore
	sc.ModelName = modelName
	sc.Provider = provider
	sc.CredentialID = credentialID
	var probeKind, grade string
	err := s.pool.QueryRow(ctx, q, credentialID, modelName).
		Scan(&sc.OverallScore, &grade, &sc.Accuracy, &sc.Stability, &sc.Latency, &probeKind, &sc.Timestamp)
	if err != nil {
		return nil, nil // not found -> nil, nil (matches FileStorage semantics)
	}
	sc.Grade = grade
	sc.ProbeKind = ProbeKind(probeKind)
	return &sc, nil
}

// GetScoreHistory returns the last `limit` runs for a node, newest first.
func (s *DBStorage) GetScoreHistory(ctx context.Context, provider, modelName string, credentialID int, limit int) ([]*QualityScore, error) {
	if limit <= 0 {
		limit = 20
	}
	if credentialID == 0 {
		if s.optionalBackup != nil {
			return s.optionalBackup.GetScoreHistory(ctx, provider, modelName, credentialID, limit)
		}
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT overall_score::float8, COALESCE(grade,''), accuracy::float8,
		       COALESCE(stability::float8,0), COALESCE(latency_p95,0),
		       COALESCE(probe_kind,'direct'), tested_at
		  FROM model_iq_runs
		 WHERE credential_id=$1 AND lower(raw_model_name)=lower($2)
		   AND status IN ('success','partial')
		 ORDER BY tested_at DESC LIMIT $3`, credentialID, modelName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*QualityScore
	for rows.Next() {
		var sc QualityScore
		sc.ModelName = modelName
		sc.Provider = provider
		sc.CredentialID = credentialID
		var probeKind, grade string
		if err := rows.Scan(&sc.OverallScore, &grade, &sc.Accuracy, &sc.Stability, &sc.Latency, &probeKind, &sc.Timestamp); err != nil {
			return nil, err
		}
		sc.Grade = grade
		sc.ProbeKind = ProbeKind(probeKind)
		out = append(out, &sc)
	}
	return out, rows.Err()
}

// ListAllScores returns recent runs across all nodes (used by CatalogModelIQ /
// NodeIQ aggregation). We bound the scan to avoid unbounded growth.
func (s *DBStorage) ListAllScores(ctx context.Context, limit int) ([]*QualityScore, error) {
	if limit <= 0 {
		limit = 5000
	}
	rows, err := s.pool.Query(ctx, `
		SELECT credential_id, raw_model_name, overall_score::float8,
		       COALESCE(grade,''), accuracy::float8, COALESCE(stability::float8,0),
		       COALESCE(latency_p95,0), COALESCE(probe_kind,'direct'), tested_at
		  FROM model_iq_runs
		 WHERE status IN ('success','partial')
		 ORDER BY tested_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*QualityScore
	for rows.Next() {
		var sc QualityScore
		var probeKind, grade string
		if err := rows.Scan(&sc.CredentialID, &sc.ModelName, &sc.OverallScore, &grade,
			&sc.Accuracy, &sc.Stability, &sc.Latency, &probeKind, &sc.Timestamp); err != nil {
			return nil, err
		}
		sc.Grade = grade
		sc.ProbeKind = ProbeKind(probeKind)
		out = append(out, &sc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Newest-first is already guaranteed by the ORDER BY; keep stable for tests.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp.After(out[j].Timestamp) })
	return out, nil
}

// --- helpers -----------------------------------------------------------------

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullableFloat(v float64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullableStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func stringOrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Compile-time assertion that DBStorage satisfies MonitorStorage.
var _ MonitorStorage = (*DBStorage)(nil)

// _ silences "imported and not used" if time becomes unused after edits.
var _ = time.Now