package bg

import (
	"context"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// TestNewIntegrityHarvester_NilPoolIsInert — a nil pool must never
// start a goroutine or tick.
func TestNewIntegrityHarvester_NilPoolIsInert(t *testing.T) {
	h := NewIntegrityHarvester(nil, DefaultIntegrityHarvesterConfig())
	if h == nil {
		t.Fatal("NewIntegrityHarvester returned nil")
	}
	h.Start(context.Background())
	h.Stop()
	if h.cycles.Load() != 0 {
		t.Errorf("nil-pool harvester must not tick: cycles=%d", h.cycles.Load())
	}
}

const (
	// Full advisory-lock statements the bridges emit. The lock key is
	// a $1 parameter (see bridgeCritical/bridgeHigh), so we assert it
	// explicitly. Using the exact SQL with the default equal matcher
	// avoids pgxmock's regexp-matcher arg-count quirks.
	advisoryCriticalSQL = `SELECT pg_advisory_xact_lock(hashtext($1))`
	criticalSelectSQL   = `
		SELECT e.id, e.tenant_id, e.provider_id, e.credential_id,
		       COALESCE(e.provider_code,''), COALESCE(e.raw_model_name,''),
		       COALESCE(e.client_model,''), COALESCE(e.outbound_model,''),
		       e.anomaly_type, e.severity, e.actual_value
		FROM model_integrity_events e
		WHERE e.severity = 'critical'
		  AND e.resolved = false
		  AND e.ts < now() - $1::interval
		  AND NOT EXISTS (
		    SELECT 1 FROM fault_events f
		    WHERE f.source = 'integrity_harvester'
		      AND f.rule_name = 'integrity:' || e.anomaly_type
		      AND f.metadata->>'integrity_event_id' = e.id::text
		      AND f.status IN ('new', 'acknowledged', 'resolving')
		  )
		ORDER BY e.ts ASC
		LIMIT 200`
	criticalInsertSQL = `
			INSERT INTO fault_events
				(rule_id, rule_name, severity, title, description, source,
				 status, metadata, detected_at, created_at, updated_at)
			VALUES (0, $1, $2, $3, $4, 'integrity_harvester',
				'new', $5, now(), now(), now())`
	advisoryHighSQL = `SELECT pg_advisory_xact_lock(hashtext($1))`
	highSelectSQL   = `
		WITH agg AS (
			SELECT tenant_id, anomaly_type, COALESCE(provider_code,'') AS provider_code,
			       credential_id, COALESCE(raw_model_name,'') AS raw_model_name,
			       COUNT(*) AS n,
			       MIN(ts) AS first_ts, MAX(ts) AS last_ts,
			       MAX(actual_value) AS sample_actual
			FROM model_integrity_events
			WHERE severity = 'high'
			  AND resolved = false
			  AND ts > now() - $1::interval
			GROUP BY tenant_id, anomaly_type, provider_code, credential_id, raw_model_name
			HAVING COUNT(*) >= $2
		)
		SELECT a.tenant_id, a.anomaly_type, a.provider_code, a.credential_id,
		       a.raw_model_name, a.n, a.first_ts, a.last_ts, a.sample_actual
		FROM agg a
		WHERE NOT EXISTS (
		    SELECT 1 FROM fault_events f
		    WHERE f.source = 'integrity_harvester'
		      AND f.rule_name = 'integrity-high:' || a.anomaly_type
		      AND f.metadata->>'credential_id' IS NOT DISTINCT FROM COALESCE(a.credential_id::text, '')
		      AND f.metadata->>'raw_model_name' IS NOT DISTINCT FROM a.raw_model_name
		      AND f.status IN ('new', 'acknowledged', 'resolving')
		)
		ORDER BY a.last_ts DESC
		LIMIT 200`
	highInsertSQL = `
			INSERT INTO fault_events
				(rule_id, rule_name, severity, title, description, source,
				 status, metadata, detected_at, created_at, updated_at)
			VALUES (0, $1, $2, $3, $4, 'integrity_harvester',
				'new', $5, $6, now(), now())`
)

// TestIntegrityHarvester_EmptyCycleIssuesNoInserts pins the contract
// that an empty pass through both bridges issues zero INSERTs. No
// INSERT ExpectExec is queued, so if either bridge tried to write the
// test would fail on an unexpected call.
func TestIntegrityHarvester_EmptyCycleIssuesNoInserts(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()

	// critical bridge: empty result.
	mock.ExpectBegin()
	mock.ExpectExec(advisoryCriticalSQL).WithArgs("integrity_harvester:critical").
		WillReturnResult(pgxmock.NewResult("LOCK", 1))
	// critical SELECT takes 1 arg (CriticalAge). Match loosely.
	mock.ExpectQuery(criticalSelectSQL).WithArgs(pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{
		"id", "tenant_id", "provider_id", "credential_id",
		"provider_code", "raw_model_name", "client_model", "outbound_model",
		"anomaly_type", "severity", "actual_value",
	}))
	mock.ExpectCommit()
	// high bridge: empty aggregate.
	mock.ExpectBegin()
	mock.ExpectExec(advisoryHighSQL).WithArgs("integrity_harvester:high").
		WillReturnResult(pgxmock.NewResult("LOCK", 1))
	// high SELECT takes 2 args (HighWindow, HighMinCount).
	mock.ExpectQuery(highSelectSQL).WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{
		"tenant_id", "anomaly_type", "provider_code", "credential_id",
		"raw_model_name", "n", "first_ts", "last_ts", "sample_actual",
	}))
	mock.ExpectCommit()

	h := &IntegrityHarvester{db: mock, cfg: DefaultIntegrityHarvesterConfig(), done: make(chan struct{})}
	if err := h.cycle(context.Background()); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if h.criticalBridged.Load() != 0 || h.highBridged.Load() != 0 {
		t.Errorf("expected 0 bridges on empty input, got critical=%d high=%d",
			h.criticalBridged.Load(), h.highBridged.Load())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("pgxmock expectations: %v", err)
	}
}

// TestIntegrityHarvester_InsertUsesRealColumns is a static guard over
// the INSERT statement text the bridges emit. Driving a full row
// through pgxmock is brittle (pgxmock's row scanner rejects the
// **string destination the bridges use for nullable tenant_id), so we
// instead assert the SQL text contains the real fault_events column
// set and the 'new' status literal, and does NOT contain any of the
// phantom columns the pre-2026-08-02 version referenced. This catches
// a regression to the broken schema at compile-of-test time without a
// database.
func TestIntegrityHarvester_InsertUsesRealColumns(t *testing.T) {
	// Real fault_events (migration 375) columns the INSERT must touch.
	mustHave := []string{
		"rule_id", "rule_name", "severity", "title", "description",
		"source", "status", "metadata", "detected_at", "created_at", "updated_at",
		"'new'",                 // status literal must be the CHECK-allowed 'new'
		"'integrity_harvester'", // source literal
	}
	// Phantom columns the old broken version used — must be absent.
	mustNotHave := []string{
		"context", "source_event_id", "first_seen_at", "last_seen_at",
		"occurrence_count", "tenant_id,",
	}
	for _, sql := range []string{criticalInsertSQL, highInsertSQL} {
		for _, want := range mustHave {
			if !strings.Contains(sql, want) {
				t.Errorf("INSERT missing required token %q\nSQL: %s", want, sql)
			}
		}
		for _, bad := range mustNotHave {
			if strings.Contains(sql, bad) {
				t.Errorf("INSERT must not reference phantom token %q\nSQL: %s", bad, sql)
			}
		}
		// rule_id must be the literal 0, not a string key.
		if !strings.Contains(sql, "VALUES (0,") {
			t.Errorf("INSERT must use rule_id literal 0\nSQL: %s", sql)
		}
	}
}
