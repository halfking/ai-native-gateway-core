package startup

import (
	"os"
	"strings"
	"testing"
)

// Migration 763 (R14 批判式复审轮, 2026-09-30) canonicalizes the
// provider_events contract alignment that lived as the hand-run
// deploy/sql/migrations/2026-07-26-provider-events-local.sql parity file
// (round11 D16 登记 → round14 部署窗手工执行). The contract pins:
//
//	C1  Fresh-install shape matches the parity file column-for-column —
//	    credential_id/event_kind MUST stay nullable
//	    (pg_reconciliation_store.go:287 writes credential_id=NULL).
//	C2  Top-level DDL only (758 lesson: DDL wrapped in DO $$ … EXECUTE
//	    $ddl$ … $$ is silently ineffective).
//	C3  PK via conname guard (replay-safe), not a bare ADD CONSTRAINT.
//	C4  Sequence + OWNED BY + SET DEFAULT present.
//	C5  setval guard never regresses the sequence (is_called-aware) —
//	    a regressing setval after row deletes would make the default
//	    hand out colliding ids.
//	C6  Registration: apply-db-revision-sequence.sh carries the 763 row.
//	C7  Parity-file channel consistency: the parity file still carries the
//	    same core statements, so the two channels cannot silently drift.
func TestMigration763ProviderEventsContract(t *testing.T) {
	upBytes, err := os.ReadFile("763_provider_events_contract.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	upCode := normalizeSQL(stripSQLComments(up))

	// ── C1: fresh-install shape, credential_id nullable ─────────────────
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS PUBLIC.PROVIDER_EVENTS",
		"ID BIGINT NOT NULL",
		"CREDENTIAL_ID BIGINT,",
		"EVENT_KIND TEXT,",
		"TS TIMESTAMP WITH TIME ZONE DEFAULT NOW() NOT NULL",
	} {
		if !strings.Contains(upCode, want) {
			t.Errorf("763 fresh-shape must contain %q — it pins the parity-file column contract", want)
		}
	}
	if strings.Contains(upCode, "CREDENTIAL_ID BIGINT NOT NULL") {
		t.Error("763 must NOT tighten credential_id to NOT NULL — pg_reconciliation_store.go writes credential_id=NULL and a fresh install would reject it")
	}

	// ── C2: top-level DDL, no $ddl$ EXECUTE wrapper ─────────────────────
	if strings.Contains(upCode, "$DDL$") {
		t.Error("763 must not wrap DDL in DO $$ … EXECUTE $ddl$ … $$ — 758 measured that form as silently ineffective")
	}
	if !strings.Contains(upCode, "ALTER TABLE PUBLIC.PROVIDER_EVENTS ALTER COLUMN ID SET DEFAULT NEXTVAL(") {
		t.Error("763 must carry the top-level SET DEFAULT statement")
	}

	// ── C3: PK via conname guard ────────────────────────────────────────
	if !strings.Contains(upCode, "CONNAME = 'PROVIDER_EVENTS_PKEY'") {
		t.Error("763 PK must be conname-guarded so replay on an already-fixed store is a no-op")
	}

	// ── C4: sequence ownership + default wiring ─────────────────────────
	for _, want := range []string{
		"CREATE SEQUENCE IF NOT EXISTS PUBLIC.PROVIDER_EVENTS_ID_SEQ",
		"OWNED BY PUBLIC.PROVIDER_EVENTS.ID",
		"CREATE INDEX IF NOT EXISTS IDX_PROVIDER_EVENTS_CREDENTIAL_TS",
	} {
		if !strings.Contains(upCode, want) {
			t.Errorf("763 must contain %q", want)
		}
	}

	// ── C5: non-regressing setval ───────────────────────────────────────
	if !strings.Contains(upCode, "IS_CALLED") || !strings.Contains(upCode, "PERFORM SETVAL(") {
		t.Error("763 setval must be is_called-aware and conditional — an unconditional setval(max(id)) would regress the sequence after row deletes and hand out colliding ids")
	}

	// ── C6: revision-sequence registration ──────────────────────────────
	seqBytes, err := os.ReadFile("../../../scripts/apply-db-revision-sequence.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(seqBytes), "763_provider_events_contract.sql") {
		t.Error("apply-db-revision-sequence.sh must carry migration 763_provider_events_contract.sql — upgrade-database deployments would never apply it otherwise")
	}

	// ── C7: parity-file channel consistency ─────────────────────────────
	parityBytes, err := os.ReadFile("../../../deploy/sql/migrations/2026-07-26-provider-events-local.sql")
	if err != nil {
		t.Fatal(err)
	}
	parity := normalizeSQL(stripSQLComments(string(parityBytes)))
	for _, want := range []string{
		"PROVIDER_EVENTS_PKEY",
		"PROVIDER_EVENTS_ID_SEQ",
		"IDX_PROVIDER_EVENTS_CREDENTIAL_TS",
	} {
		if !strings.Contains(parity, want) {
			t.Errorf("parity file drift: %q missing from deploy/sql/migrations/2026-07-26-provider-events-local.sql — align both channels", want)
		}
	}
}
