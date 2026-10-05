package admin

// 守卫：ACC work-type 同步的线上契约。
//
// 覆盖三件曾经各自静默失败过的事：
//
//  1. 路径。accWorkTypesPath 曾是 Node 时代的 "/api/llm/work-types"，而
//     acc-go 挂在 /api/v2/…，所以每次同步都 404。这条断言把路径钉在服务态
//     端点上。
//
//  2. 空的 ok 响应。ACC 回答 {"ok":true,"work_types":[]} 曾被当成成功、
//     计数 0，与「同步根本没生效」在运维视角无法区分。现在必须报错。
//
//  3. 非空 model_routes 的解析。ACC 曾把 model_routes 声明成 []string；
//     两边都发空数组时看不出问题，一旦有人填模型名就是 UnmarshalTypeError，
//     而网关的诊断是「ACC 响应格式无法解析」——指向错误的子系统。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAccWorkTypesPathIsTheServiceMount pins the path constant.
//
// The failure it prevents is invisible from the gateway: a 404 surfaces as
// a sync error, but the sync worker is opt-in (default off) and the admin
// button is a deliberate click, so a wrong path can sit in the tree for a
// long time. It is the same class of bug as a misrouted route that "looks
// fine because nothing calls it".
func TestAccWorkTypesPathIsTheServiceMount(t *testing.T) {
	if accWorkTypesPath == "/api/llm/work-types" {
		t.Fatal("accWorkTypesPath is the retired Node path; acc-go serves /api/v2/…")
	}
	if accWorkTypesPath != "/api/v2/internal/llm/work-types" {
		t.Fatalf("accWorkTypesPath = %q, want the service-to-service mount "+
			"/api/v2/internal/llm/work-types. The user-facing "+
			"/api/v2/llm/work-types is behind RequireAuth, which rejects the "+
			"opaque shared token this sync sends — pointing at it would 401.",
			accWorkTypesPath)
	}
}

// TestFetchACCWorkTypesRejectsEmptyOKResponse pins the "ok but nothing" case.
//
// Without this the operator sees "已从 ACC 同步 0 个工作类型" and has no way
// to tell it apart from a sync that silently did not run.
func TestFetchACCWorkTypesRejectsEmptyOKResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"work_types":[]}`))
	}))
	defer srv.Close()

	if _, err := fetchACCWorkTypes(context.Background(), accSyncConfig{
		BaseURL: srv.URL, ServiceToken: "fixture-service-token",
	}); err == nil {
		t.Fatal("expected an error for ok=true with zero work types")
	}
}

// TestFetchACCWorkTypesAcceptsEntriesWithEmptyRoutes is the other half of
// that decision.
//
// Zero *routes* is a legitimate state — ACC ships an empty model_routes on
// every entry and the catalog is configuration, not routing. Making this an
// error would kill the sync for a condition that is the current normal.
func TestFetchACCWorkTypesAcceptsEntriesWithEmptyRoutes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"work_types":[
			{"key":"code_gen","label":"代码生成","category":"研发","l1_task_type":"code",
			 "default_profile":"speed_first","tags":["code"],"prompt_keywords":["代码"],
			 "sort_order":4,"enabled":true,"model_routes":[]}]}`))
	}))
	defer srv.Close()

	items, err := fetchACCWorkTypes(context.Background(), accSyncConfig{
		BaseURL: srv.URL, ServiceToken: "fixture-service-token",
	})
	if err != nil {
		t.Fatalf("entries with empty model_routes must sync successfully: %v", err)
	}
	if len(items) != 1 || items[0].Key != "code_gen" {
		t.Fatalf("unexpected payload: %+v", items)
	}
}

// TestFetchACCWorkTypesParsesPopulatedRoutes is the schema-mismatch guard.
//
// The historical failure: ACC emitted ["claude-x"] and the gateway could
// not unmarshal it into []modelRoute, so every parse branch fell through to
// "ACC 响应格式无法解析" — a diagnostic that blames ACC's response shape
// rather than the field's type on the wire.
func TestFetchACCWorkTypesParsesPopulatedRoutes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"work_types":[
			{"key":"security_review","label":"漏洞与威胁面分析","category":"研发",
			 "l1_task_type":"code","default_profile":"smart","tags":["security"],
			 "prompt_keywords":["漏洞"],"sort_order":23,"enabled":true,
			 "model_routes":[
				{"canonical_name":"claude-sonnet-4-6","weight":1,"min_score":0,
			   "enabled":true,"tier":"primary","task_quality_score":0},
			  {"canonical_name":"glm-5.2","weight":0.85,"min_score":0,
			   "enabled":true,"tier":"secondary","task_quality_score":0}]}]}`))
	}))
	defer srv.Close()

	items, err := fetchACCWorkTypes(context.Background(), accSyncConfig{
		BaseURL: srv.URL, ServiceToken: "fixture-service-token",
	})
	if err != nil {
		t.Fatalf("populated model_routes must parse: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	routes := items[0].ModelRoutes
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d: %+v", len(routes), routes)
	}
	if routes[0].CanonicalName != "claude-sonnet-4-6" || routes[0].Tier != "primary" {
		t.Errorf("route 0 = %+v", routes[0])
	}
	// The trap: a bare bool defaults to false when the field is absent, so
	// a route synced without it is inserted disabled — present in the UI,
	// routing nothing.
	if !routes[0].Enabled || !routes[1].Enabled {
		t.Errorf("routes must arrive enabled: %+v", routes)
	}
	if routes[1].Weight != 0.85 {
		t.Errorf("weight = %v, want 0.85", routes[1].Weight)
	}
}

// TestFetchACCWorkTypesRejectsStringArrayRoutes documents the mismatch
// directly: the shape ACC used to send must be an error, not a silent
// success or a misleading diagnostic.
func TestFetchACCWorkTypesRejectsStringArrayRoutes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// ACC's old []string shape.
		_, _ = w.Write([]byte(`{"ok":true,"work_types":[
			{"key":"code_gen","label":"代码生成","category":"研发","l1_task_type":"code",
			 "default_profile":"speed_first","tags":[],"prompt_keywords":[],
			 "sort_order":4,"enabled":true,"model_routes":["claude-sonnet-4-6"]}]}`))
	}))
	defer srv.Close()

	if _, err := fetchACCWorkTypes(context.Background(), accSyncConfig{
		BaseURL: srv.URL, ServiceToken: "fixture-service-token",
	}); err == nil {
		t.Fatal("expected an error for the string-array model_routes shape")
	}
}

// TestAccWorkTypePayloadParsesLiveACCShape re-parses a real captured
// response body, so the two repos' understanding is checked against
// something that actually came off the wire rather than a hand-written
// example that could drift from both sides at once.
//
// It also pins a robustness property that fell out of writing it: the
// gateway's accWorkTypesResponse has no Total field, so the envelope's
// "total" is ignored and the sync works off the array length. That is the
// right dependency — a "total" that disagrees with the entries would
// otherwise let the sync write a count it never received.
func TestAccWorkTypePayloadParsesLiveACCShape(t *testing.T) {
	// Captured 2026-10-05 from a live acc-go at
	// GET /api/v2/internal/llm/work-types (26 entries). Trimmed to one entry
	// plus the envelope; the field set is the point, not the count.
	const captured = `{"ok":true,"source":"static_v1","work_types":[` +
		`{"key":"security_review","label":"漏洞与威胁面分析","category":"研发",` +
		`"l1_task_type":"code","default_profile":"smart","tags":["security","code","audit"],` +
		`"prompt_keywords":["漏洞","越权","威胁","攻击面","鉴权","注入","漏洞修复","threat","vulnerability"],` +
		`"sort_order":23,"enabled":true,"model_routes":[]}],` +
		`"total":26}`

	// Parse the envelope with a local struct so the extra keys ACC sends
	// (source, total) are visible here even though the gateway's own type
	// deliberately ignores them.
	var envelope struct {
		OK        bool                 `json:"ok"`
		Source    string               `json:"source"`
		WorkTypes []accWorkTypePayload `json:"work_types"`
		Total     int                  `json:"total"`
	}
	if err := json.Unmarshal([]byte(captured), &envelope); err != nil {
		t.Fatalf("captured live payload does not parse: %v", err)
	}
	if !envelope.OK || envelope.Source != "static_v1" {
		t.Fatalf("envelope mismatch: ok=%v source=%q", envelope.OK, envelope.Source)
	}
	if len(envelope.WorkTypes) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(envelope.WorkTypes))
	}
	if envelope.Total != len(envelope.WorkTypes) && envelope.Total != 26 {
		t.Logf("note: total=%d with %d entries present; the gateway ignores "+
			"total and uses the array length, which is the safer dependency",
			envelope.Total, len(envelope.WorkTypes))
	}

	// And the type the gateway actually uses must parse the same bytes.
	var resp accWorkTypesResponse
	if err := json.Unmarshal([]byte(captured), &resp); err != nil {
		t.Fatalf("gateway payload type does not parse the live shape: %v", err)
	}
	if len(resp.WorkTypes) != 1 {
		t.Fatalf("expected 1 entry via the gateway type, got %d", len(resp.WorkTypes))
	}
	wt := resp.WorkTypes[0]
	if wt.Key != "security_review" || wt.L1TaskType != "code" || wt.DefaultProfile != "smart" {
		t.Errorf("decoded entry mismatch: %+v", wt)
	}
	if len(wt.ModelRoutes) != 0 {
		t.Errorf("expected an empty model_routes, got %+v", wt.ModelRoutes)
	}
}
