package streaming

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// r1027 (2026-10-14): /v1/models must derive liveness from the same surface
// the router routes on. The legacy query keyed on model_offers.available — a
// table the failure ladders never touch — so balance-dead / protocol-dead
// credentials kept their models advertised (154: grok-4.7 sat behind a
// credential with 88 consecutive INSUFFICIENT_BALANCE failures while
// /v1/models still listed it; opencode kept its fixed auxiliary request on
// zero-catalog minimax-m2.7-quickspeed and ate 400s forever).

// TestModelsRoutableQueryIsUsedAndShaped pins that the handler queries the
// binding/probe-state surface (not model_offers) by default, and that the
// rows it gets back flow through to the response.
func TestModelsRoutableQueryIsUsedAndShaped(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// context_window is NULL here on purpose: the handler scans it into **int
	// (real pgx supports multi-level pointers for nullable columns; pgxmock
	// only honours them for NULL). The column's presence in the projection is
	// pinned by TestModelsRoutableSQLGatePredicates.
	//
	// The SQL shape is the contract: binding availability gate + probe-state
	// gate + credential/provider liveness, joined via credential_model_bindings.
	rows := pgxmock.NewRows([]string{"canonical_name", "family", "modality", "context_window"}).
		AddRow("grok-4.6", "grok", "text", nil)
	mock.ExpectQuery(`credential_model_bindings cmb`).
		WillReturnRows(rows)

	h := NewModelsHandler()
	h.SetModelsDB(mock)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data []modelEntry `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Data) != 1 || payload.Data[0].ID != "grok-4.6" {
		t.Fatalf("data = %+v, want [grok-4.6]", payload.Data)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("query expectations: %v", err)
	}
}

// TestModelsRoutableSQLGatePredicates pins the load-bearing predicates of
// modelsRoutableSQL. Dropping any of these gates resurrects exactly the
// dead-model advertisement r1027 removes (string-level contract in the
// style of bg/probe_rollback_contract_test.go — the SQL semantics against a
// live catalog were verified against production during the round).
func TestModelsRoutableSQLGatePredicates(t *testing.T) {
	q := strings.Join(strings.Fields(modelsRoutableSQL), " ")
	must := []string{
		// Router-side liveness source — NOT model_offers.
		"credential_model_bindings cmb",
		"cmb.available = TRUE",
		// Probe-state gate: a credential with consecutive failures stays out
		// even while its binding flaps through auto-recovery (balance-dead
		// credentials oscillate available=TRUE between probe rounds).
		"node_probe_state nps",
		"COALESCE(nps.consecutive_failures, 0) = 0",
		// Credential / provider liveness, same as healthyCandidateSQL.
		// availability_state / quota_state are the CREDENTIAL-level liveness
		// columns: auth_failed + permanently_exhausted credentials keep
		// available=TRUE bindings (grok-4.7 sat behind five such dead
		// credentials — the binding-only gate still advertised the model,
		// caught by the 403→127 production recount on 2026-10-14).
		"COALESCE(c.status, 'active') = 'active'",
		"COALESCE(c.lifecycle_status, 'active') = 'active'",
		"COALESCE(c.availability_state, 'ready') = 'ready'",
		// quota_state 有三个耗尽态，只排 1/3 就是把 balance_exhausted /
		// periodic_exhausted 的凭据重新挂回发现列表（这三态在 Go 里都有
		// 活跃写入路径，不是纸面枚举）。availability_state='ready' 不蕴含
		// quota_state 非耗尽——两列各有独立 CHECK 约束。
		"COALESCE(c.quota_state, 'ok') <> ALL (ARRAY['permanently_exhausted','balance_exhausted','periodic_exhausted'])",
		// health_status 的 CHECK 允许 4 值，其中 warning / unreachable
		// 均被判不可路由（视图的 unavailable_reason 有 recent_probe_unreachable 分支）。
		"COALESCE(c.health_status, 'unknown') IN ('healthy', 'unknown')",
		// 模型级下线开关：pm.available / pm.unavailable_reason 在路由侧
		// 与视图侧都有，发现侧漏掉会让运维的模型级下线对 /v1/models 失效。
		"pm.available = TRUE",
		"COALESCE(pm.unavailable_reason, '') NOT LIKE 'manual%'",
		"COALESCE(p.enabled, TRUE) = TRUE",
		"NOT LIKE 'manual%'",
		"COALESCE(cmb.admin_protected, FALSE) = FALSE",
		// Canonical projection + deterministic order preserved.
		"provider_models pm ON pm.canonical_id = mc.id",
		"ORDER BY family, mc.canonical_name",
	}
	for _, m := range must {
		if !strings.Contains(q, m) {
			t.Fatalf("modelsRoutableSQL lost required predicate %q; the discovery list would advertise models the router cannot serve", m)
		}
	}
	if strings.Contains(q, "model_offers") {
		t.Fatal("modelsRoutableSQL must not read model_offers — that liveness source is exactly the r1027 bug")
	}
	// The escape hatch keeps the legacy shape so flipping
	// models.list_only_routable=false is a real fallback, not a no-op.
	l := strings.Join(strings.Fields(modelsLegacySQL), " ")
	if !strings.Contains(l, "model_offers mo ON mo.canonical_id = mc.id") ||
		!strings.Contains(l, "mo.available = TRUE") {
		t.Fatal("modelsLegacySQL lost the legacy model_offers semantics")
	}
}

// routableViewGate holds the credential/model-level liveness gates that both
// the router's SSOT view (v_routable_credential_models) and the discovery
// endpoint must agree on. Each entry is [viewFragment, discoveryFragment]:
// the two sides spell the same predicate differently (SQL-standard vs Go
// idiom, different COALESCE defaults) but must not disagree semantically.
var routableViewGate = [][2]string{
	{"c.quota_state <> ALL (ARRAY['permanently_exhausted'::text, 'balance_exhausted'::text, 'periodic_exhausted'::text])",
		"COALESCE(c.quota_state, 'ok') <> ALL (ARRAY['permanently_exhausted','balance_exhausted','periodic_exhausted'])"},
	{"COALESCE(c.health_status, 'unknown'::text) = ANY (ARRAY['healthy'::text, 'unknown'::text])",
		"COALESCE(c.health_status, 'unknown') IN ('healthy', 'unknown')"},
	{"pm.available = true", "pm.available = TRUE"},
	{"c.availability_state = 'ready'::text", "COALESCE(c.availability_state, 'ready') = 'ready'"},
	{"c.lifecycle_status = 'active'::text", "COALESCE(c.lifecycle_status, 'active') = 'active'"},
	{"cmb.available = true", "cmb.available = TRUE"},
	{"c.manual_disabled, false) = false", "COALESCE(c.manual_disabled, FALSE) = FALSE"},
}

// TestModelsRoutableAgreesWithRoutableView is the structural gate that was
// missing when pm.available / two of three quota states / health_status were
// all missing from modelsRoutableSQL while the string pins above still went
// green — those pins only assert "the query has the predicates we remembered
// to write", never "the predicates match the router". This compares against
// the view the router itself documents as its SSOT, so a gate added to one
// side and forgotten on the other turns the suite red.
//
// The router-side inline SQL in autoroute/recommend_v2.go is not exported, so
// the view file on disk is used as the comparison anchor; the two are kept in
// sync by the view's own migration history (460).
func TestModelsRoutableAgreesWithRoutableView(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "sql", "objects", "views", "v_routable_credential_models.sql"))
	if err != nil {
		t.Fatalf("read routable view SSOT: %v", err)
	}
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	view := norm(string(raw))
	discovery := norm(modelsRoutableSQL)

	for _, g := range routableViewGate {
		if !strings.Contains(view, g[0]) {
			t.Fatalf("anchor drifted: view no longer contains %q — update routableViewGate to match the new SSOT spelling before trusting this gate", g[0])
		}
		if !strings.Contains(discovery, g[1]) {
			t.Fatalf("discovery/router drift: modelsRoutableSQL lacks the gate %q that v_routable_credential_models enforces; /v1/models would advertise models the router refuses to serve", g[1])
		}
	}
	// The view's own unavailable_reason CASE enumerates the reason each gate
	// produces; if a new gate appears there it must have a discovery-side peer.
	for _, reason := range []string{"quota_", "recent_probe_unreachable", "model_unavailable"} {
		if strings.Contains(view, reason) && !strings.Contains(discovery, "quota_state") && reason == "quota_" {
			t.Fatalf("view enumerates %q but discovery dropped its quota_state gate", reason)
		}
	}
}

// TestModelsRoutableKeyIsRegisteredInSettingsSpecs pins that the kill-switch
// key is declared as a settings spec — GetPlatformBool falls back silently
// when a key has no Spec, so an unregistered key would make the toggle
// unflippable (the switch would read the default forever).
func TestModelsRoutableKeyIsRegisteredInSettingsSpecs(t *testing.T) {
	found := false
	for _, sp := range settings.ModelsSpecs() {
		if sp.Key == modelsListOnlyRoutableKey {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("settings key %q is not declared in ModelsSpecs(); GetPlatformBool would silently fall back to the default and the kill switch would never engage", modelsListOnlyRoutableKey)
	}
}

// TestModelsQueryForKillSwitch pins the escape-hatch branch: the gate key
// defaults to true (router-aligned query); flipping models.list_only_routable
// to false must hand back the legacy model_offers query without a deploy.
func TestModelsQueryForKillSwitch(t *testing.T) {
	if got := modelsQueryFor(true); got != modelsRoutableSQL {
		t.Fatal("gate on must serve modelsRoutableSQL")
	}
	if got := modelsQueryFor(false); got != modelsLegacySQL {
		t.Fatal("gate off must serve modelsLegacySQL (the documented escape hatch)")
	}
	if strings.Contains(modelsRoutableSQL, "model_offers") {
		t.Fatal("routable query must not fall back to model_offers")
	}
}
