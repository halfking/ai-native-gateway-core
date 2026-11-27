package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// ---- ServiceJWTIssuer（ADR §7 W1：claims 最小 scope / write 默认拒）----

func TestServiceJWTIssuerRejectsEmptySecret(t *testing.T) {
	if _, err := NewServiceJWTIssuer("", "iss", "aud", 0); err == nil {
		t.Fatal("empty secret must fail (fail-closed)")
	}
}

func TestServiceJWTIssuerDefaultScopesAreReadOnly(t *testing.T) {
	iss, err := NewServiceJWTIssuer("k", "", "memora-api", 0)
	if err != nil {
		t.Fatal(err)
	}
	// write 默认拒：不带 writeGranted ⇒ scope 集不含 memory.write。
	if got := iss.ScopesFor(false); strings.Contains(strings.Join(got, " "), WriteScope) {
		t.Fatalf("default scopes must not contain %s: %v", WriteScope, got)
	}
	// 只读集合逐字对齐 memora catalog 的 8 个 read 域。
	want := "memory.read knowledge.read task.read toolcall.read capability.read fact.read project.read"
	if got := strings.Join(memoraReadOnlyScopes, " "); got != want {
		t.Fatalf("read scope set drifted: got %q want %q", got, want)
	}
}

func TestServiceJWTIssuerIssueClaims(t *testing.T) {
	iss, err := NewServiceJWTIssuer("shared-secret", "llm-gateway-mcp", "memora-api", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := iss.Issue("tenant-a", false)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.Parse(tok, func(t *jwt.Token) (any, error) {
		return []byte("shared-secret"), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		t.Fatalf("token invalid: %v", err)
	}
	claims, _ := parsed.Claims.(jwt.MapClaims)
	for _, k := range []string{"iss", "aud", "sub", "tenant_id", "scope", "exp"} {
		if _, ok := claims[k]; !ok {
			t.Fatalf("missing required claim %s (memora verifier contract)", k)
		}
	}
	if claims["tenant_id"] != "tenant-a" {
		t.Fatalf("tenant_id = %v", claims["tenant_id"])
	}
	if strings.Contains(claims["scope"].(string), WriteScope) {
		t.Fatal("write scope leaked without grant")
	}
	// 显式 write 授权 ⇒ scope 含 memory.write。
	tokW, _ := iss.Issue("tenant-a", true)
	parsedW, _ := jwt.Parse(tokW, func(*jwt.Token) (any, error) { return []byte("shared-secret"), nil })
	claimsW, _ := parsedW.Claims.(jwt.MapClaims)
	if !strings.Contains(claimsW["scope"].(string), WriteScope) {
		t.Fatal("explicit write grant not reflected")
	}
}

// ---- writeGranted（ADR §4：显式 mcp.write + 非 high + 非审批工具）----

func gatewayForWrite(t *testing.T) *Gateway {
	t.Helper()
	g, err := NewGateway(Config{Store: NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestWriteGrantedRequiresExplicitScope(t *testing.T) {
	g := gatewayForWrite(t)
	admin := &Identity{KeyID: "k1", TenantID: "t", Scopes: []string{ScopeAdmin}} // admin 不隐含 write
	tool := &Tool{Name: "x", Sensitivity: SensitivityWrite}
	if g.writeGranted(admin, tool, Adapter{}) {
		t.Fatal("mcp.admin must NOT imply write (ADR: explicit grant only)")
	}
	explicit := &Identity{KeyID: "k2", TenantID: "t", Scopes: []string{ScopeRead, ScopeMCPWrite}}
	if !g.writeGranted(explicit, tool, Adapter{}) {
		t.Fatal("explicit mcp.write should grant on non-high tool")
	}
}

func TestWriteGrantedBlockedByHighSensitivityAndApproval(t *testing.T) {
	g := gatewayForWrite(t)
	id := &Identity{Scopes: []string{ScopeMCPWrite}}
	if g.writeGranted(id, &Tool{Sensitivity: SensitivityHigh}, Adapter{}) {
		t.Fatal("high-sensitivity tool must block write")
	}
	if g.writeGranted(id, &Tool{RequireApproval: true}, Adapter{}) {
		t.Fatal("approval-required tool must block write")
	}
	if g.writeGranted(id, &Tool{}, Adapter{Sensitivity: SensitivityHigh}) {
		t.Fatal("high-sensitivity adapter must block write")
	}
}

// ---- 出站分支（ADR §4：头形状 / 缺 issuer fail-closed）----

func TestUpstreamLLMGatewayModeHeaders(t *testing.T) {
	iss, err := NewServiceJWTIssuer("sk", "llm-gateway-mcp", "memora-api", 0)
	if err != nil {
		t.Fatal(err)
	}
	var gotHeaders http.Header
	up := NewUpstreamClient(5 * time.Second)
	up.SetServiceJWTIssuer(iss)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": []any{}}})
	}))
	defer srv.Close()

	a := Adapter{Name: "memora", TenantID: "tenant-x", Endpoint: srv.URL, AuthMode: AuthModeLLMGateway}
	if _, err := up.Call(context.Background(), CallContext{
		Adapter: a, Method: "tools/list", WriteGranted: false, KeyID: "key-9", CallID: "mcp-test",
	}); err != nil {
		t.Fatal(err)
	}
	auth := gotHeaders.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		t.Fatalf("Authorization = %q", auth)
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) { return []byte("sk"), nil })
	if err != nil || !parsed.Valid {
		t.Fatalf("outbound jwt invalid: %v", err)
	}
	if gotHeaders.Get("X-MCP-Gateway-Key-ID") != "key-9" {
		t.Fatalf("X-MCP-Gateway-Key-ID = %q", gotHeaders.Get("X-MCP-Gateway-Key-ID"))
	}
	if gotHeaders.Get("X-MCP-On-Behalf-Of") != "tenant-x" {
		t.Fatalf("X-MCP-On-Behalf-Of = %q", gotHeaders.Get("X-MCP-On-Behalf-Of"))
	}
	if gotHeaders.Get("X-MCP-Call-ID") != "mcp-test" {
		t.Fatalf("X-MCP-Call-ID = %q", gotHeaders.Get("X-MCP-Call-ID"))
	}
}

func TestUpstreamLLMGatewayModeFailsWithoutIssuer(t *testing.T) {
	up := NewUpstreamClient(5 * time.Second) // 未注入 issuer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("must not reach upstream without issuer")
	}))
	defer srv.Close()
	a := Adapter{Name: "memora", Endpoint: srv.URL, AuthMode: AuthModeLLMGateway}
	if _, err := up.Call(context.Background(), CallContext{Adapter: a, Method: "tools/list"}); err == nil {
		t.Fatal("llm_gateway without issuer must fail (no silent credential-less call)")
	}
}

// ---- 控制面联邦端点（ADR §4 D2b：未启用不 404，enabled=false 可探测）----

func TestControlRegistryEndpointsWhenDisabled(t *testing.T) {
	_, srv := newTestGateway(t, nil)
	status, body, _ := adminReq(t, srv.URL, http.MethodGet, "/mcp/control/registry/status", "")
	if status != http.StatusOK || body["enabled"] != false {
		t.Fatalf("registry/status disabled: status=%d body=%v", status, body)
	}
	status, body, _ = adminReq(t, srv.URL, http.MethodGet, "/mcp/control/registry/servers", "")
	if status != http.StatusOK {
		t.Fatalf("registry/servers: status=%d", status)
	}
	if body["enabled"] != false {
		t.Fatalf("registry/servers enabled flag: %v", body)
	}
}

func TestControlRegistryEndpointsRequireAuth(t *testing.T) {
	_, srv := newTestGateway(t, nil)
	resp, err := http.Get(srv.URL + "/mcp/control/registry/status")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated registry/status = %d, want 401", resp.StatusCode)
	}
}

// ---- newCallID（ADR §6 关联键）----

func TestNewCallIDShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := newCallID()
		if !strings.HasPrefix(id, "mcp-") {
			t.Fatalf("call id %q lacks prefix", id)
		}
		if seen[id] {
			t.Fatalf("call id %q repeated", id)
		}
		seen[id] = true
	}
}

// ---- RegistrySyncer（ADR §7 W1：stdio 跳过 / 软删 / Manual 保护 / 失败沿用快照）----

func registryFixture() []registryServer {
	return []registryServer{
		{ID: "mcp-memora", Name: "memora", Transport: "http", Endpoint: "http://memora:9002",
			AuthMode: "llm_gateway", Enabled: true, Tools: []string{"memory_search"}},
		{ID: "mcp-edge-agent", Name: "edge-agent", Transport: "stdio", Endpoint: "/usr/bin/agent", AuthMode: "none", Enabled: true},
		{ID: "mcp-crm", Name: "crm", Transport: "http", Endpoint: "http://crm/mcp", AuthMode: "api_key_ref", AuthRef: "CRM_KEY", Enabled: true},
		{ID: "mcp-off", Name: "off", Transport: "http", Endpoint: "http://off/mcp", AuthMode: "llm_gateway", Enabled: false},
	}
}

func registryTestServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/mcp/servers" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("registry fetch lacks bearer token")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestRegistrySyncMappingAndSkips(t *testing.T) {
	payload := `{"servers":[` +
		`{"id":"mcp-memora","name":"memora","transport":"http","endpoint":"http://memora:9002","auth_mode":"llm_gateway","enabled":true},` +
		`{"id":"mcp-edge","name":"edge","transport":"stdio","endpoint":"/bin/x","auth_mode":"none","enabled":true},` +
		`{"id":"mcp-crm","name":"crm","transport":"http","endpoint":"http://crm","auth_mode":"api_key_ref","auth_ref":"K","enabled":true}` +
		`],"count":3,"scope":"tenant"}`
	srv := registryTestServer(t, 200, payload)
	defer srv.Close()

	store := NewMemoryStore()
	s := NewRegistrySyncer(RegistrySyncConfig{Enabled: true, URL: srv.URL, Token: "t", Interval: time.Second}, store)
	if err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	st := s.Status()
	if st.SnapshotCount != 3 || st.LastError != "" {
		t.Fatalf("status = %+v", st)
	}
	byOutcome := map[string][]string{}
	for _, e := range st.Entries {
		byOutcome[e.Outcome] = append(byOutcome[e.Outcome], e.Name)
	}
	if len(byOutcome[SyncOutcomeOK]) != 1 || byOutcome[SyncOutcomeOK][0] != "memora" {
		t.Fatalf("ok entries = %v", byOutcome)
	}
	if len(byOutcome[SyncOutcomeSkipStdio]) != 1 {
		t.Fatalf("stdio skip missing: %v", byOutcome)
	}
	if len(byOutcome[SyncOutcomeSkipAuthRef]) != 1 {
		t.Fatalf("api_key_ref skip missing: %v", byOutcome)
	}

	a, err := store.GetAdapter(context.Background(), "default", "memora")
	if err != nil || a == nil {
		t.Fatalf("adapter memora not created: %v", err)
	}
	if a.AuthMode != AuthModeLLMGateway || !a.Enabled || a.Capability != "memory" {
		t.Fatalf("adapter mapping wrong: %+v", a)
	}
	if a.Labels["source"] != "registry" {
		t.Fatalf("labels wrong: %v", a.Labels)
	}
}

func TestRegistrySyncSoftDeleteAndManualProtection(t *testing.T) {
	payloadOn := `{"servers":[{"id":"mcp-memora","name":"memora","transport":"http","endpoint":"http://m:9002","auth_mode":"llm_gateway","enabled":true}],"count":1,"scope":"tenant"}`
	payloadOff := `{"servers":[{"id":"mcp-memora","name":"memora","transport":"http","endpoint":"http://m:9002","auth_mode":"llm_gateway","enabled":false}],"count":1,"scope":"tenant"}`

	srv := registryTestServer(t, 200, payloadOn)
	store := NewMemoryStore()
	s := NewRegistrySyncer(RegistrySyncConfig{Enabled: true, URL: srv.URL, Token: "t"}, store)
	ctx := context.Background()
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ := store.GetAdapter(ctx, "default", "memora")
	if a == nil || !a.Enabled {
		t.Fatal("adapter should exist enabled")
	}
	// 人工治理：打 Manual 标 + 调整限流。
	a.Manual = true
	a.RateLimitPerMin = 5
	if err := store.PutAdapter(ctx, *a); err != nil {
		t.Fatal(err)
	}

	// registry 侧 disabled ⇒ 软删保位（存在但 Enabled=false）。
	srv2 := registryTestServer(t, 200, payloadOff)
	s2 := NewRegistrySyncer(RegistrySyncConfig{Enabled: true, URL: srv2.URL, Token: "t"}, store)
	if err := s2.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	a2, err := store.GetAdapter(ctx, "default", "memora")
	if err != nil || a2 == nil {
		t.Fatal("soft delete must keep adapter record")
	}
	if a2.Enabled {
		t.Fatal("disabled registry entry must disable adapter")
	}
	// Manual 治理字段不被覆盖。
	if a2.RateLimitPerMin != 5 || !a2.Manual {
		t.Fatalf("manual governance fields overwritten: %+v", a2)
	}
}

func TestRegistrySyncFailureKeepsSnapshot(t *testing.T) {
	srv := registryTestServer(t, 200, `{"servers":[{"id":"mcp-memora","name":"memora","transport":"http","endpoint":"http://m","auth_mode":"llm_gateway","enabled":true}],"count":1,"scope":"tenant"}`)
	store := NewMemoryStore()
	s := NewRegistrySyncer(RegistrySyncConfig{Enabled: true, URL: srv.URL, Token: "t"}, store)
	ctx := context.Background()
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Status().Entries == nil {
		t.Fatal("entries expected")
	}

	// 上游挂掉 ⇒ SyncOnce 报错且 Store/状态不回退为空。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer bad.Close()
	s.cfg.URL = bad.URL
	if err := s.SyncOnce(ctx); err == nil {
		t.Fatal("expected error on 503")
	}
	if _, err := store.GetAdapter(ctx, "default", "memora"); err != nil {
		t.Fatalf("store must retain last snapshot: %v", err)
	}
	if s.Status().LastError == "" {
		t.Fatal("last_error must be recorded")
	}
}

// ---- W2：指标族（ADR §6：mcp_gateway_registry_sync_total / _errors_total / _snapshot_entries）----

// gatherRegistryMetrics 从默认 registry 抓联邦三族的当前读数。
// 注意：指标是包级全局，同一测试进程里其它用例也会推高计数 ⇒ 断言一律用
// 「本轮前后的差值」，不用绝对值。
func gatherRegistryMetrics(t *testing.T) (map[string]float64, float64) {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	counters := map[string]float64{}
	var gauge float64 = -1
	for _, mf := range mfs {
		switch mf.GetName() {
		case "mcp_gateway_registry_sync_total":
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "outcome" {
						counters[lp.GetValue()] += m.GetCounter().GetValue()
					}
				}
			}
		case "mcp_gateway_registry_sync_errors_total":
			for _, m := range mf.GetMetric() {
				counters["errors"] += m.GetCounter().GetValue()
			}
		case "mcp_gateway_registry_snapshot_entries":
			for _, m := range mf.GetMetric() {
				gauge = m.GetGauge().GetValue()
			}
		}
	}
	return counters, gauge
}

func TestRegistrySyncMetricsVisible(t *testing.T) {
	payload := `{"servers":[` +
		`{"id":"mcp-memora","name":"memora","transport":"http","endpoint":"http://m","auth_mode":"llm_gateway","enabled":true},` +
		`{"id":"mcp-edge","name":"edge","transport":"stdio","endpoint":"/bin/x","auth_mode":"none","enabled":true}` +
		`],"count":2,"scope":"tenant"}`
	srv := registryTestServer(t, 200, payload)
	defer srv.Close()
	store := NewMemoryStore()
	s := NewRegistrySyncer(RegistrySyncConfig{Enabled: true, URL: srv.URL, Token: "t"}, store)
	ctx := context.Background()

	before, _ := gatherRegistryMetrics(t)
	if err := s.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncOnce(ctx); err != nil { // 第二轮：差值应为 2
		t.Fatal(err)
	}
	after, gauge := gatherRegistryMetrics(t)

	if d := after["ok"] - before["ok"]; d != 2 {
		t.Fatalf("sync_total{ok} delta = %v, want 2", d)
	}
	if d := after["skip_stdio"] - before["skip_stdio"]; d != 2 {
		t.Fatalf("sync_total{skip_stdio} delta = %v, want 2", d)
	}
	if d := after["errors"] - before["errors"]; d != 0 {
		t.Fatalf("sync_errors_total delta = %v, want 0", d)
	}
	if gauge != 2 {
		t.Fatalf("snapshot_entries = %v, want 2 (gauge is absolute)", gauge)
	}
}

func TestRegistrySyncMetricsErrorCounted(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
	}))
	defer bad.Close()
	store := NewMemoryStore()
	s := NewRegistrySyncer(RegistrySyncConfig{Enabled: true, URL: bad.URL, Token: "t"}, store)

	before, _ := gatherRegistryMetrics(t)
	if err := s.SyncOnce(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	after, _ := gatherRegistryMetrics(t)
	if d := after["errors"] - before["errors"]; d < 1 {
		t.Fatalf("sync_errors_total delta = %v, want >=1", d)
	}
}
