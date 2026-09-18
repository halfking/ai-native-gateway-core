//go:build integration

// e2e_loop_integration_test.go — the taskprofile module's dedicated AUTO
// closed-loop test on a real PostgreSQL (repo lesson: SQL-facing behaviour is
// only proven against a real DB).
//
// Loop under test (design doc §一#6, §五):
//
//	classifier assumption        human correction            system feedback
//	--------------------        ----------------            ----------------
//	documentation (tier-c)  →   6 corrections, 4 corrected  → 1. Suggest escalates
//	                            to architecture                documentation c→b
//	                                                        → 2. PostClassify damps
//	                                                           its confidence (LLM
//	                                                           fallback fires earlier)
//	                                                        → 3. Prometheus feedback
//	                                                           recorder saw 6 verdicts
//	                                                        → 4. admin endpoints serve
//	                                                           the consolidated view
//
// Run:
//
//	go test -tags=integration -timeout 5m -count=1 ./taskprofile
package taskprofile

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Compile-time: *autoroute.ClassificationFeedbackAggregator must keep
// satisfying taskprofile.FeedbackRecorder — the cmd/gateway wiring relies on
// the structural match (taskprofile never imports autoroute in production).
var _ FeedbackRecorder = autoroute.NewClassificationFeedbackAggregator()

// dispatchPostgresContainer mirrors bg's private helper: TEST_PG_URL bypass,
// 30-attempt connect retry, caller-supplied DDL. Private to package
// taskprofile so the production package keeps zero testcontainers dependency.
func dispatchPostgresContainer(t *testing.T, ctx context.Context, extraSchema string) (*pgxpool.Pool, func()) {
	t.Helper()
	openPool := func(dsn string) (*pgxpool.Pool, error) {
		var (
			pool *pgxpool.Pool
			err  error
		)
		for attempt := 0; attempt < 30; attempt++ {
			pool, err = pgxpool.New(ctx, dsn)
			if err == nil {
				pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
				pingErr := pool.Ping(pingCtx)
				pingCancel()
				if pingErr == nil {
					return pool, nil
				}
				pool.Close()
				err = pingErr
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
			}
		}
		return nil, err
	}

	if dsn := os.Getenv("TEST_PG_URL"); dsn != "" {
		pool, err := openPool(dsn)
		if err != nil {
			t.Fatalf("connect TEST_PG_URL: %v", err)
		}
		if extraSchema != "" {
			if _, err := pool.Exec(ctx, extraSchema); err != nil {
				pool.Close()
				t.Fatalf("exec schema: %v", err)
			}
		}
		return pool, func() { pool.Close() }
	}

	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("taskprofile_e2e"),
		postgres.WithUsername("taskprofile_e2e"),
		postgres.WithPassword("taskprofile_e2e"),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("connection string: %v", err)
	}
	pool, err := openPool(dsn)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("pgxpool.New: %v", err)
	}
	if _, err := pool.Exec(ctx, extraSchema); err != nil {
		pool.Close()
		_ = container.Terminate(ctx)
		t.Fatalf("exec schema: %v", err)
	}
	cleanup := func() {
		pool.Close()
		terminateCtx, terminateCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer terminateCancel()
		if err := container.Terminate(terminateCtx); err != nil {
			t.Errorf("terminate postgres: %v", err)
		}
	}
	return pool, cleanup
}

// e2eSchema mirrors migration 724 plus the minimal auto_route_selections_all
// projection the correction lookup reads (task_type / confidence / profile).
const e2eSchema = `
CREATE TABLE public.auto_route_selections_hot (
	id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	request_id text,
	task_type text,
	confidence double precision,
	profile text,
	ts timestamptz NOT NULL DEFAULT NOW()
);
CREATE OR REPLACE VIEW public.auto_route_selections_all AS
	SELECT id, request_id, task_type, confidence, profile, ts
	FROM public.auto_route_selections_hot;
CREATE TABLE public.task_type_corrections (
	id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	request_id text NOT NULL UNIQUE,
	auto_task_type text NOT NULL,
	human_task_type text NOT NULL,
	agrees boolean NOT NULL,
	classifier_confidence double precision,
	profile text,
	annotator text NOT NULL,
	reason text NOT NULL,
	created_at timestamptz NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_task_type_corrections_auto_type
	ON public.task_type_corrections (auto_task_type, created_at DESC);
CREATE TABLE public.task_type_tier_config (
	id SERIAL PRIMARY KEY,
	task_type TEXT NOT NULL,
	preferred_tier TEXT NOT NULL CHECK (preferred_tier IN ('tier-a', 'tier-b', 'tier-c')),
	fallback_tiers TEXT[] DEFAULT ARRAY[]::TEXT[],
	min_confidence DECIMAL(3,2) DEFAULT 0.70 CHECK (min_confidence >= 0 AND min_confidence <= 1),
	-- R43 (2026-09-18): 对齐真实部署形态（deploy V370）——tenant_id BIGINT、
	-- 唯一索引 0 哨兵 COALESCE；此前测试自建 TEXT/'' 形态，让 ApplySuggestions
	-- 的 ON CONFLICT 在真表上 42P10 的缺陷测不出来。
	tenant_id BIGINT,
	enabled BOOLEAN DEFAULT TRUE,
	description TEXT,
	created_at TIMESTAMPTZ DEFAULT NOW(),
	updated_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE UNIQUE INDEX uq_task_type_tier_config
	ON public.task_type_tier_config (task_type, COALESCE(tenant_id, 0));
`

// countingRecorder captures FeedbackRecorder verdicts.
type countingRecorder struct {
	verdicts []struct {
		taskType string
		correct  bool
	}
}

func (r *countingRecorder) RecordFeedback(taskType string, correct bool) {
	r.verdicts = append(r.verdicts, struct {
		taskType string
		correct  bool
	}{taskType, correct})
}

func TestTaskProfileE2E_CorrectionLoop_RealDB(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool, cleanup := dispatchPostgresContainer(t, ctx, e2eSchema)
	defer cleanup()

	// Classifier assumptions: 6 documentation requests + 2 coding requests.
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	for i := 1; i <= 6; i++ {
		mustExec(`INSERT INTO auto_route_selections_hot (request_id, task_type, confidence, profile)
			VALUES ($1, 'documentation', 0.92, 'cli')`, docRequestID(i))
	}
	for i := 1; i <= 2; i++ {
		mustExec(`INSERT INTO auto_route_selections_hot (request_id, task_type, confidence, profile)
			VALUES ($1, 'coding', 0.88, 'cli')`, codingRequestID(i))
	}

	recorder := &countingRecorder{}
	store := NewCorrectionStore(pool)
	store.SetRecorder(recorder)
	// The handler shares the store instance so the recorder observes exactly
	// the corrections the API writes (production wiring: cmd/gateway's
	// optimizer-owned store carries the aggregator; admin handlers carry none).
	h := &Handlers{store: store}

	// ── Phase 1: baseline suggestion (no corrections) ─────────────────────
	base := Suggest("documentation", 0.99, nil)
	if base.Tier != TierC || base.TierSource != "registry" {
		t.Fatalf("baseline suggestion = (%s, %s), want (tier-c, registry)", base.Tier, base.TierSource)
	}

	// ── Phase 2: humans correct 4 of 6 documentation requests ────────────
	// (the classifier kept calling architecture work "documentation")
	post := func(path string, body any) (*httptest.ResponseRecorder, map[string]any) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		rec := httptest.NewRecorder()
		switch path {
		case "/api/admin/task-profile/corrections":
			h.handleCreateCorrection(rec, req)
		case "/api/admin/task-profile/reload":
			h.handleReload(rec, req)
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}
	for i := 1; i <= 4; i++ {
		rec, _ := post("/api/admin/task-profile/corrections", map[string]any{
			"request_id":      docRequestID(i),
			"human_task_type": "architecture",
			"annotator":       "e2e-annotator",
			"reason":          "quality",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("correction %d: status %d body %s", i, rec.Code, rec.Body.String())
		}
	}
	// …and confirm the remaining 2.
	for i := 5; i <= 6; i++ {
		rec, _ := post("/api/admin/task-profile/corrections", map[string]any{
			"request_id":      docRequestID(i),
			"human_task_type": "documentation",
			"annotator":       "e2e-annotator",
			"reason":          "correct",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("confirmation %d: status %d body %s", i, rec.Code, rec.Body.String())
		}
	}
	// coding request confirmed once (adds an agreeing sample for coding).
	if rec, _ := post("/api/admin/task-profile/corrections", map[string]any{
		"request_id":      codingRequestID(1),
		"human_task_type": "coding",
		"annotator":       "e2e-annotator",
		"reason":          "correct",
	}); rec.Code != http.StatusOK {
		t.Fatalf("coding confirmation: status %d body %s", rec.Code, rec.Body.String())
	}

	// ── Guard rails on the write path ────────────────────────────────────
	if rec, _ := post("/api/admin/task-profile/corrections", map[string]any{
		"request_id": docRequestID(1), "human_task_type": "coding",
		"annotator": "x", "reason": "quality",
	}); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate correction = %d, want 409", rec.Code)
	}
	if rec, _ := post("/api/admin/task-profile/corrections", map[string]any{
		"request_id": "req-never-seen", "human_task_type": "coding",
		"annotator": "x", "reason": "quality",
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown request_id = %d, want 400", rec.Code)
	}
	if rec, _ := post("/api/admin/task-profile/corrections", map[string]any{
		"request_id": codingRequestID(2), "human_task_type": "not-a-type",
		"annotator": "x", "reason": "quality",
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown human_task_type = %d, want 400", rec.Code)
	}

	// ── Phase 3: suggestion escalated (tier-c → tier-b) ──────────────────
	stats, err := store.Stats(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if s := stats["documentation"]; s.Total != 6 || s.Corrected != 4 || s.Agrees != 2 {
		t.Fatalf("documentation stats = %+v, want total 6 corrected 4 agrees 2", s)
	}
	escalated := Suggest("documentation", 0.99, stats)
	if escalated.Tier != TierB || escalated.TierSource != "correction_escalation" {
		t.Fatalf("escalated suggestion = (%s, %s), want (tier-b, correction_escalation)",
			escalated.Tier, escalated.TierSource)
	}
	if escalated.MinConfidence <= 0.85 {
		t.Fatalf("escalated min_confidence = %.2f, want > 0.85", escalated.MinConfidence)
	}
	// Confirmed-heavy coding stays on its registry tier.
	if s := Suggest("coding", 0.99, stats); s.TierSource != "registry" {
		t.Fatalf("coding must stay registry-tiered, got %s", s.TierSource)
	}

	// ── Phase 4: the optimizer blend damps documentation confidence ──────
	// (routingopt cannot be imported here without a cycle in the adapter
	// shape, so the blend is exercised through the production formula with
	// the store's own numbers; unit coverage lives in routingopt.)
	stat := stats["documentation"]
	humanRate := float64(stat.Agrees) / float64(stat.Total)
	blended := (0.90*float64(50) + humanRate*2.0*float64(stat.Total)) / (50 + 2.0*float64(stat.Total))
	if blended >= 0.90 {
		t.Fatalf("correction blend must damp documentation accuracy: %.4f", blended)
	}

	// ── Phase 5: feedback recorder saw one verdict per correction ────────
	if len(recorder.verdicts) != 7 {
		t.Fatalf("recorder saw %d verdicts, want 7 (6 documentation + 1 coding)", len(recorder.verdicts))
	}
	agreeCount, disagreeCount := 0, 0
	for _, v := range recorder.verdicts {
		if v.taskType != "documentation" && v.taskType != "coding" {
			t.Fatalf("unexpected verdict task type %q", v.taskType)
		}
		if v.correct {
			agreeCount++
		} else {
			disagreeCount++
		}
	}
	if agreeCount != 3 || disagreeCount != 4 {
		t.Fatalf("verdicts = %d agree / %d disagree, want 3 / 4", agreeCount, disagreeCount)
	}

	// ── Phase 6: admin read endpoints serve the consolidated view ────────
	req := httptest.NewRequest(http.MethodGet, "/api/admin/task-profile", nil)
	rec := httptest.NewRecorder()
	h.handleProfile(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET task-profile: %d %s", rec.Code, rec.Body.String())
	}
	var view struct {
		RegistryVersion string `json:"registry_version"`
		Profiles        []struct {
			TaskProfile
			CorrectionStats *CorrectionStat `json:"correction_stats"`
			Suggestion      Suggestion      `json:"suggestion"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode task-profile view: %v", err)
	}
	if view.RegistryVersion == "" || len(view.Profiles) != 21 {
		t.Fatalf("task-profile view incomplete: version=%q profiles=%d",
			view.RegistryVersion, len(view.Profiles))
	}
	for _, p := range view.Profiles {
		if p.TaskType == "documentation" {
			if p.CorrectionStats == nil || p.CorrectionStats.Total != 6 {
				t.Fatalf("documentation view missing correction stats: %+v", p)
			}
			if p.Suggestion.Tier != TierB || p.Suggestion.TierSource != "correction_escalation" {
				t.Fatalf("documentation view suggestion = (%s, %s), want (tier-b, correction_escalation)",
					p.Suggestion.Tier, p.Suggestion.TierSource)
			}
		}
	}

	statsRec := httptest.NewRecorder()
	h.handleCorrectionStats(statsRec, httptest.NewRequest(http.MethodGet,
		"/api/admin/task-profile/corrections/stats?since_days=1&recent_limit=10", nil))
	if statsRec.Code != http.StatusOK {
		t.Fatalf("GET corrections/stats: %d %s", statsRec.Code, statsRec.Body.String())
	}
	var statsView struct {
		Stats  map[string]CorrectionStat `json:"stats"`
		Recent []Correction              `json:"recent"`
	}
	if err := json.Unmarshal(statsRec.Body.Bytes(), &statsView); err != nil {
		t.Fatalf("decode stats view: %v", err)
	}
	if len(statsView.Recent) != 7 || len(statsView.Stats) != 2 {
		t.Fatalf("stats view: %d recent / %d stat rows, want 7 / 2",
			len(statsView.Recent), len(statsView.Stats))
	}

	// ── Phase 7: reload endpoint resets to embedded defaults ─────────────
	if _, err := LoadOverlayBytes([]byte(`{"schema_version":1,"version":"e2e-overlay","profiles":{
		"coding":{"task_type":"coding","preferred_tier":"tier-a","min_confidence":0.5}}}`), "e2e"); err != nil {
		t.Fatalf("overlay setup: %v", err)
	}
	t.Cleanup(func() { _, _ = ReloadOverlay("") })
	rec, _ = post("/api/admin/task-profile/reload", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reload: %d %s", rec.Code, rec.Body.String())
	}
	if p, _ := Profile("coding"); p.PreferredTier != TierB {
		t.Fatalf("reload did not restore defaults: %+v", p)
	}

	// ── Phase 8: CSV export → import round trip ──────────────────────────
	// Export contains the 7 corrections from Phase 2; re-importing them must
	// be idempotent (all skipped). Two NEW rows (import inserts directly,
	// no auto_route_selections lookup) must import and reach the recorder.
	expRec := httptest.NewRecorder()
	h.handleExportCorrections(expRec, httptest.NewRequest(http.MethodGet,
		"/api/admin/task-profile/corrections/export?since_days=1", nil))
	if expRec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", expRec.Code, expRec.Body.String())
	}
	if ct := expRec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("export content-type = %q, want text/csv", ct)
	}
	exportedBody := expRec.Body.String()
	if !strings.HasPrefix(exportedBody, CorrectionCSVHeader) {
		t.Fatalf("export missing header: %.80s", exportedBody)
	}
	verdictsBeforeImport := len(recorder.verdicts)
	importPayload := exportedBody +
		"req-import-a,coding,testing,false,0.60,web,importer,quality,2026-09-18T12:00:00Z\n" +
		"req-import-b,chat,chat,true,,cli,importer,correct,2026-09-18T12:05:00Z\n"
	impRec := httptest.NewRecorder()
	h.handleImportCorrections(impRec, httptest.NewRequest(http.MethodPost,
		"/api/admin/task-profile/corrections/import", strings.NewReader(importPayload)))
	if impRec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", impRec.Code, impRec.Body.String())
	}
	var importView struct {
		Summary struct {
			TotalRows int `json:"total_rows"`
			Imported  int `json:"imported"`
			Skipped   int `json:"skipped"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(impRec.Body.Bytes(), &importView); err != nil {
		t.Fatalf("decode import summary: %v (body %.200s)", err, impRec.Body.String())
	}
	if importView.Summary.Imported != 2 || importView.Summary.Skipped != 7 {
		t.Fatalf("import summary = %+v, want imported 2 / skipped 7", importView.Summary)
	}
	if len(recorder.verdicts) != verdictsBeforeImport+2 {
		t.Fatalf("import verdicts not recorded: %d → %d",
			verdictsBeforeImport, len(recorder.verdicts))
	}
	// Re-import the same payload: everything skipped (idempotent).
	impRec2 := httptest.NewRecorder()
	h.handleImportCorrections(impRec2, httptest.NewRequest(http.MethodPost,
		"/api/admin/task-profile/corrections/import", strings.NewReader(importPayload)))
	var importView2 struct {
		Summary struct {
			Imported int `json:"imported"`
			Skipped  int `json:"skipped"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(impRec2.Body.Bytes(), &importView2); err != nil {
		t.Fatalf("decode re-import summary: %v", err)
	}
	if importView2.Summary.Imported != 0 || importView2.Summary.Skipped != 9 {
		t.Fatalf("re-import must be idempotent: %+v", importView2.Summary)
	}
	t.Cleanup(func() { _ = cleanupImportedRows(ctx, pool, []string{"req-import-a", "req-import-b"}) })

	// ── Phase 9: apply-tier-config writes the escalated suggestion ───────
	// documentation is the only correction-driven escalation (rate 0.667).
	applyRec := httptest.NewRecorder()
	h.handleApplyTierConfig(applyRec, httptest.NewRequest(http.MethodPost,
		"/api/admin/task-profile/apply-tier-config", strings.NewReader(`{}`)))
	if applyRec.Code != http.StatusOK {
		t.Fatalf("apply-tier-config: %d %s", applyRec.Code, applyRec.Body.String())
	}
	var applyView struct {
		Applied []AppliedTierConfig `json:"applied"`
	}
	if err := json.Unmarshal(applyRec.Body.Bytes(), &applyView); err != nil {
		t.Fatalf("decode apply view: %v", err)
	}
	if len(applyView.Applied) != 1 || applyView.Applied[0].TaskType != "documentation" ||
		applyView.Applied[0].PreferredTier != TierB || applyView.Applied[0].TierSource != "correction_escalation" {
		t.Fatalf("applied = %+v, want documentation → tier-b (correction_escalation)", applyView.Applied)
	}
	var dbTier string
	if err := pool.QueryRow(ctx, `SELECT preferred_tier
		FROM task_type_tier_config WHERE task_type='documentation'`).
		Scan(&dbTier); err != nil {
		t.Fatalf("tier config row missing after apply: %v", err)
	}
	if dbTier != TierB {
		t.Fatalf("tier config tier = %s, want tier-b", dbTier)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM task_type_tier_config WHERE description LIKE 'taskprofile%'`)
	})
}

// cleanupImportedRows removes rows created by the import phase.
func cleanupImportedRows(ctx context.Context, pool *pgxpool.Pool, ids []string) error {
	_, err := pool.Exec(ctx, `DELETE FROM task_type_corrections WHERE request_id = ANY($1)`, ids)
	return err
}

func docRequestID(i int) string    { return "req-e2e-doc-" + string(rune('0'+i)) }
func codingRequestID(i int) string { return "req-e2e-code-" + string(rune('0'+i)) }
