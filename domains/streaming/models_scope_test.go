package streaming

// models_scope_test.go —— 模型列表端点的「口径」（scope）契约。
//
// 2026-10-29：/v1/models 与 /v1beta/models 默认只输出特色模型
// （routing_policy.featured_models），全量清单改为显式参数才输出。
//
// 本文件钉三件事：
//
//  1. 口径的**来源与优先级**（请求参数 > 运维默认；非法值必须报错而不是
//     静默回落）；
//  2. 口径**怎么落到 SQL**（$1 参数，且两个查询常量都带同一份过滤）；
//  3. 精选口径筛不出模型时的**回退**（退回全量，而不是交一张空清单）。
//
// ★ 第 2 条里有一个真实的坑（本文件有专门的判据）：PostgreSQL 的
//   set-returning function 在 target list 里**先于 LIMIT 展开**，所以
//   `SELECT LOWER(unnest(featured_models)) ... ORDER BY id LIMIT 1` 返回的是
//   数组的**第一个元素**，不是整份清单 ⇒ 精选过滤会静默只认一个模型。
//   正确写法是先把行钉住（policy），再展开数组（featured）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pgxmock "github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// ─────────────────────────── 口径解析 ───────────────────────────

// TestResolveModelsScope_DefaultsToFeatured pins that an unqualified request
// gets the curated list. This is the whole point of the change: the
// discovery endpoint used to answer with every routable model (600+) and a
// client could not tell which of those the gateway actually recommends.
func TestResolveModelsScope_DefaultsToFeatured(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	got, err := resolveModelsScope(r)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != modelsScopeFeatured {
		t.Fatalf("default scope = %q, want %q", got, modelsScopeFeatured)
	}
}

// TestResolveModelsScope_ExplicitParams pins every accepted spelling of the
// opt-in.
func TestResolveModelsScope_ExplicitParams(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"/v1/models?scope=featured", modelsScopeFeatured},
		{"/v1/models?scope=all", modelsScopeAll},
		{"/v1/models?scope=ALL", modelsScopeAll},
		{"/v1/models?scope=Featured", modelsScopeFeatured},
		{"/v1/models?all=true", modelsScopeAll},
		{"/v1/models?all=1", modelsScopeAll},
		{"/v1/models?all=yes", modelsScopeAll},
		{"/v1/models?all=on", modelsScopeAll},
		{"/v1/models?all=false", modelsScopeFeatured},
		{"/v1/models?all=0", modelsScopeFeatured},
		{"/v1/models?all=no", modelsScopeFeatured},
		{"/v1/models?all=off", modelsScopeFeatured},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, c.url, nil)
		got, err := resolveModelsScope(r)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.url, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: scope = %q, want %q", c.url, got, c.want)
		}
	}
}

// TestResolveModelsScope_EmptyIsAbsent pins that an EMPTY parameter is
// treated as "not given" rather than as an error — a client that builds its
// query string by appending a possibly-empty value must not get a 400.
func TestResolveModelsScope_EmptyIsAbsent(t *testing.T) {
	for _, url := range []string{"/v1/models?scope=", "/v1/models?all=", "/v1/models?scope=&all="} {
		r := httptest.NewRequest(http.MethodGet, url, nil)
		got, err := resolveModelsScope(r)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", url, err)
			continue
		}
		if got != modelsScopeFeatured {
			t.Errorf("%s: scope = %q, want the default %q", url, got, modelsScopeFeatured)
		}
	}
}

// TestResolveModelsScope_InvalidValueIsRejected pins that a malformed scope
// is a 400, NOT a silent fallback.
//
// ★ The reason this matters: a client that asked for the full list and
// received the curated one (or the reverse) draws a wrong conclusion about
// what the gateway can serve — and it draws it SILENTLY. Reporting the
// mistake is strictly better than answering a different question than the
// one that was asked.
func TestResolveModelsScope_InvalidValueIsRejected(t *testing.T) {
	for _, url := range []string{"/v1/models?scope=bogus", "/v1/models?scope=all,featured", "/v1/models?all=maybe"} {
		r := httptest.NewRequest(http.MethodGet, url, nil)
		if _, err := resolveModelsScope(r); err == nil {
			t.Errorf("%s: expected an error for a malformed scope, got none", url)
		}
	}
}

// TestResolveModelsScope_ScopeWinsOverAll pins the precedence between the
// two parameter spellings: the explicit scope wins.
func TestResolveModelsScope_ScopeWinsOverAll(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/models?scope=featured&all=true", nil)
	got, err := resolveModelsScope(r)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != modelsScopeFeatured {
		t.Fatalf("scope = %q, want %q (scope must win over the all= shorthand)", got, modelsScopeFeatured)
	}
}

// TestDefaultModelsScope_FallsBackToFeaturedOnUnknownStoredValue pins the
// operator-default resolution: a stored value the gateway does not recognise
// degrades to "featured" (the curated list), not to "all".
//
// ★ The asymmetry is deliberate: "featured" advertises what the gateway
// recommends; "all" advertises everything, including models the operator
// never put in front of a client. A typo in a setting must not silently
// widen the public catalog.
func TestDefaultModelsScope_FallsBackToFeaturedOnUnknownStoredValue(t *testing.T) {
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })
	settings.Global = settings.NewRegistry()
	settings.Global.MustRegisterSpec(&settings.Spec{
		Key:     modelsListScopeKey,
		Type:    settings.TypeEnum,
		Scope:   settings.ScopePlatform,
		Default: "bogus-value",
		Options: []string{"featured", "all"},
	})

	if got := defaultModelsScope(); got != modelsScopeFeatured {
		t.Fatalf("unrecognised stored scope must degrade to %q, got %q", modelsScopeFeatured, got)
	}
}

// TestModelsListScopeKeyIsRegisteredInSettingsSpecs pins that the operator
// escape hatch is declared as a settings spec.
//
// ★ GetPlatformString falls back silently when a key has no Spec, so an
// unregistered key would make the toggle unflippable — the switch would
// read the default forever, and the "restore the old behaviour without a
// deploy" promise would be a lie.
func TestModelsListScopeKeyIsRegisteredInSettingsSpecs(t *testing.T) {
	var sp *settings.Spec
	for _, s := range settings.ModelsSpecs() {
		if s.Key == modelsListScopeKey {
			sp = s
			break
		}
	}
	if sp == nil {
		t.Fatalf("settings key %q is not declared in ModelsSpecs(); GetPlatformString would silently fall back and the escape hatch would never engage", modelsListScopeKey)
	}
	if sp.Default != modelsScopeFeatured {
		t.Errorf("default scope = %v, want %q", sp.Default, modelsScopeFeatured)
	}
	if sp.Type != settings.TypeEnum {
		t.Errorf("type = %v, want %v", sp.Type, settings.TypeEnum)
	}
	if len(sp.Options) != 2 || sp.Options[0] != modelsScopeFeatured || sp.Options[1] != modelsScopeAll {
		t.Errorf("options = %v, want [%s %s]", sp.Options, modelsScopeFeatured, modelsScopeAll)
	}
	if !sp.HotReload {
		t.Error("the scope default must be hot-reloadable — it is the escape hatch for a public-contract change")
	}
}

// ─────────────────────── 口径怎么落到 SQL ───────────────────────

// TestModelsFeaturedCTEDoesNotTruncateTheList is the structural pin for the
// LIMIT/unnest trap.
//
// ★ In PostgreSQL a set-returning function in the target list is expanded
// BEFORE LIMIT is applied. The one-liner
// `SELECT LOWER(unnest(featured_models)) ... ORDER BY id LIMIT 1`
// therefore returns the array's FIRST element, not the list — and the
// featured filter would silently match exactly one model while looking
// perfectly correct. The two-CTE shape (policy pins the row, featured
// expands the array) is the only order that yields the whole list.
func TestModelsFeaturedCTEDoesNotTruncateTheList(t *testing.T) {
	norm := strings.Join(strings.Fields(modelsFeaturedCTE), " ")

	// The truncating one-liner must not appear in any form.
	for _, forbidden := range []string{
		"unnest(featured_models)",
		"unnest(COALESCE(featured_models",
		"unnest(featured_models",
	} {
		if strings.Contains(norm, forbidden) {
			t.Fatalf("modelsFeaturedCTE contains %q — PostgreSQL expands set-returning functions in the target list BEFORE LIMIT, so this shape returns only the first featured name", forbidden)
		}
	}

	policyEnd := strings.Index(norm, "featured AS (")
	if policyEnd < 0 {
		t.Fatalf("modelsFeaturedCTE must have a policy CTE and a featured CTE; got: %s", norm)
	}
	policy := norm[:policyEnd]
	featured := norm[policyEnd:]

	if !strings.Contains(policy, "LIMIT 1") {
		t.Errorf("the policy CTE must pin the routing_policy row with LIMIT 1; got: %s", policy)
	}
	if strings.Contains(policy, "unnest") {
		t.Errorf("the policy CTE must NOT expand the array (that is what makes LIMIT truncate the list); got: %s", policy)
	}
	if !strings.Contains(featured, "unnest") {
		t.Errorf("the featured CTE must expand the array; got: %s", featured)
	}
	if strings.Contains(featured, "LIMIT") {
		t.Errorf("the featured CTE must not carry a LIMIT (it would truncate the expanded names); got: %s", featured)
	}
}

// TestModelsFeaturedSQLContract pins the shape of the filter and that BOTH
// discovery queries carry it.
//
// ★ Both queries must carry it: the scope is orthogonal to the liveness
// source, so flipping models.list_only_routable (the r1027 escape hatch)
// must not silently change which models are advertised.
func TestModelsFeaturedSQLContract(t *testing.T) {
	pred := strings.Join(strings.Fields(modelsFeaturedPredicate), " ")
	must := []string{
		"NOT $1::boolean",
		"LOWER(mc.canonical_name) = ANY (SELECT name FROM featured)",
		"provider_models pmf",
		"pmf.canonical_id = mc.id",
		"LOWER(pmf.raw_model_name) = ANY (SELECT name FROM featured)",
		"LOWER(pmf.standardized_name) = ANY (SELECT name FROM featured)",
	}
	for _, m := range must {
		if !strings.Contains(pred, m) {
			t.Errorf("modelsFeaturedPredicate lost %q", m)
		}
	}
	// A model the ROUTER treats as featured (bg/shared_pick.go matches
	// raw_model_name / standardized_name) must not be invisible to the
	// discovery list — a client cannot pick what it cannot see.
	if !strings.Contains(pred, "raw_model_name") || !strings.Contains(pred, "standardized_name") {
		t.Error("the featured predicate must match the provider-side names too, or the discovery list contradicts the routing order")
	}

	for name, sql := range map[string]string{
		"modelsRoutableSQL": modelsRoutableSQL,
		"modelsLegacySQL":   modelsLegacySQL,
	} {
		norm := strings.Join(strings.Fields(sql), " ")
		for _, m := range []string{"WITH policy AS", "featured AS", "NOT $1::boolean"} {
			if !strings.Contains(norm, m) {
				t.Errorf("%s lost %q — the scope filter must exist in exactly the same form in both discovery queries", name, m)
			}
		}
	}
}

// TestModelsQueryForScopeGateIsShared pins that the scope filter is not a
// second copy of the liveness gates: both queries keep their own gate list,
// and the featured filter is the same shared predicate in both.
func TestModelsQueryForScopeGateIsShared(t *testing.T) {
	if !strings.Contains(modelsRoutableSQL, modelsFeaturedPredicate) {
		t.Error("modelsRoutableSQL must embed modelsFeaturedPredicate verbatim")
	}
	if !strings.Contains(modelsLegacySQL, modelsFeaturedPredicate) {
		t.Error("modelsLegacySQL must embed modelsFeaturedPredicate verbatim")
	}
}

// ───────────────────── 端点行为（pgxmock） ─────────────────────

// modelsScopeRows builds the mock rows for one query result.
func modelsScopeRows(names ...string) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{"canonical_name", "family", "modality", "context_window"})
	for _, n := range names {
		rows.AddRow(n, "test", "text", nil)
	}
	return rows
}

// TestModelsDefaultScopeQueriesTheFeaturedList pins that an unqualified
// request asks the database for the FEATURED subset (the $1 argument is the
// featured-only flag).
func TestModelsDefaultScopeQueriesTheFeaturedList(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery(`credential_model_bindings cmb`).
		WithArgs(true).
		WillReturnRows(modelsScopeRows("featured-a"))

	h := NewModelsHandler()
	h.SetModelsDB(mock)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("query expectations: %v", err)
	}
}

// TestModelsScopeAllQueriesTheFullList pins the opt-in: ?scope=all (and the
// ?all=true shorthand) must ask for the unfiltered list.
func TestModelsScopeAllQueriesTheFullList(t *testing.T) {
	for _, url := range []string{"/v1/models?scope=all", "/v1/models?all=true"} {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock: %v", err)
		}
		mock.ExpectQuery(`credential_model_bindings cmb`).
			WithArgs(false).
			WillReturnRows(modelsScopeRows("featured-a", "plain-b"))

		h := NewModelsHandler()
		h.SetModelsDB(mock)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body=%s", url, rec.Code, rec.Body.String())
		}
		var payload struct {
			Data []modelEntry `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s: decode: %v", url, err)
		}
		if len(payload.Data) != 2 {
			t.Errorf("%s: expected the full list (2 entries), got %d", url, len(payload.Data))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("%s: query expectations: %v", url, err)
		}
		mock.Close()
	}
}

// TestModelsInvalidScopeIsRejectedWithoutQuerying pins that a malformed
// scope is a 400 and that NO database query is issued (the request must not
// reach the catalog at all).
func TestModelsInvalidScopeIsRejectedWithoutQuerying(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	h := NewModelsHandler()
	h.SetModelsDB(mock)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models?scope=bogus", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not valid JSON: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object: %s", rec.Body.String())
	}
	if errObj["code"] != "invalid_scope" {
		t.Errorf("code = %v, want invalid_scope", errObj["code"])
	}
	if errObj["type"] != "invalid_request_error" {
		t.Errorf("type = %v, want invalid_request_error", errObj["type"])
	}
	// No query must have been issued: an unparsable request never reaches
	// the catalog.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a rejected scope must not query the database: %v", err)
	}
}

// TestModelsFeaturedScopeFallsBackToTheFullList pins the safe degradation.
//
// ★ When the curated set yields NOTHING, the gateway must serve the full
// routable list instead of an empty catalog: a discovery endpoint that
// answers "no models" breaks every client that reads it. The fallback is
// a SECOND query (the filtered one returned zero rows) — and the response
// must be the full list.
func TestModelsFeaturedScopeFallsBackToTheFullList(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	// First query: the featured filter returns nothing.
	mock.ExpectQuery(`credential_model_bindings cmb`).
		WithArgs(true).
		WillReturnRows(modelsScopeRows())
	// Second query: the unfiltered list.
	mock.ExpectQuery(`credential_model_bindings cmb`).
		WithArgs(false).
		WillReturnRows(modelsScopeRows("plain-a", "plain-b"))

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
	if len(payload.Data) != 2 {
		t.Fatalf("expected the full list after the fallback, got %d entries: %+v", len(payload.Data), payload.Data)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("query expectations: %v", err)
	}
}

// TestModelsFeaturedScopeDoesNotFallbackWhenItHasRows pins that the
// fallback is only for the EMPTY case — a populated featured list must not
// trigger a second query (that would double the load of the hottest
// discovery endpoint).
func TestModelsFeaturedScopeDoesNotFallbackWhenItHasRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`credential_model_bindings cmb`).
		WithArgs(true).
		WillReturnRows(modelsScopeRows("featured-a"))

	h := NewModelsHandler()
	h.SetModelsDB(mock)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	// ExpectationsWereMet fails if the handler issued the second query.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a non-empty featured list must not trigger the fallback query: %v", err)
	}
}

// TestServeGeminiModelsHonoursTheSameScope pins that the Gemini-native
// list obeys the same scope rules as the OpenAI-shaped one.
//
// ★ Two shapes of ONE list must not disagree about WHICH models they list —
// otherwise the gateway has two truths about what it can serve, and a
// native Gemini client and an OpenAI client would see different catalogs.
func TestServeGeminiModelsHonoursTheSameScope(t *testing.T) {
	cases := []struct {
		url       string
		wantArg   bool
		wantCount int
	}{
		{"/v1beta/models", true, 1},
		{"/v1beta/models?scope=all", false, 2},
		{"/v1beta/models?all=true", false, 2},
	}
	for _, c := range cases {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock: %v", err)
		}
		names := []string{"featured-a"}
		if c.wantCount == 2 {
			names = []string{"featured-a", "plain-b"}
		}
		mock.ExpectQuery(`credential_model_bindings cmb`).
			WithArgs(c.wantArg).
			WillReturnRows(modelsScopeRows(names...))

		h := NewModelsHandler()
		h.SetModelsDB(mock)
		rec := httptest.NewRecorder()
		h.ServeGeminiModels(rec, httptest.NewRequest(http.MethodGet, c.url, nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body=%s", c.url, rec.Code, rec.Body.String())
		}
		var payload struct {
			Models []geminiModelEntry `json:"models"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s: decode: %v", c.url, err)
		}
		if len(payload.Models) != c.wantCount {
			t.Errorf("%s: models = %d, want %d", c.url, len(payload.Models), c.wantCount)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("%s: query expectations: %v", c.url, err)
		}
		mock.Close()
	}
}

// TestServeGeminiModelsRejectsInvalidScope pins that the Gemini surface
// rejects a malformed scope with the same shape as the OpenAI one.
func TestServeGeminiModelsRejectsInvalidScope(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	h := NewModelsHandler()
	h.SetModelsDB(mock)
	rec := httptest.NewRecorder()
	h.ServeGeminiModels(rec, httptest.NewRequest(http.MethodGet, "/v1beta/models?scope=bogus", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a rejected scope must not query the database: %v", err)
	}
}
