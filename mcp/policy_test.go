package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"memory__search", "memory__search", true},
		{"memory__search", "memory__add", false},
		{"memory__*", "memory__search", true},
		{"memory__*", "other__search", false},
		{"*", "anything", true},
	}
	for _, c := range cases {
		if got := matchPattern(c.pattern, c.name); got != c.want {
			t.Fatalf("matchPattern(%q,%q)=%v want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestKeyAllowsToolDenyWins(t *testing.T) {
	k := &Key{AllowedTools: []string{"memora__*"}, DeniedTools: []string{"memora__delete_memory"}}
	if !keyAllowsTool(k, "memora__search") {
		t.Fatalf("expected allowed")
	}
	if keyAllowsTool(k, "memora__delete_memory") {
		t.Fatalf("expected denied")
	}
	if !keyAllowsTool(&Key{}, "anything") {
		t.Fatalf("empty allow list should allow all")
	}
}

func TestBindingEnabledDefault(t *testing.T) {
	if !bindingEnabled(&Key{}, "memora") {
		t.Fatalf("unregistered backend should be enabled")
	}
	if !bindingEnabled(&Key{Bindings: map[string]bool{}}, "memora") {
		t.Fatalf("unregistered backend should be enabled")
	}
	if bindingEnabled(&Key{Bindings: map[string]bool{"memora": false}}, "memora") {
		t.Fatalf("explicitly disabled backend should be blocked")
	}
}

func TestToolVisibleByVisibility(t *testing.T) {
	teamTool := &Tool{Visibility: VisibilityTeam, Teams: []string{"org-1"}}
	orgID := &Identity{OrgID: "org-1", TenantID: "t"}
	otherOrg := &Identity{OrgID: "org-2", TenantID: "t"}
	if !toolVisible(teamTool, orgID) {
		t.Fatalf("team member should see team tool")
	}
	if toolVisible(teamTool, otherOrg) {
		t.Fatalf("non-member should not see team tool")
	}
	pub := &Tool{Visibility: VisibilityPublic}
	if !toolVisible(pub, otherOrg) {
		t.Fatalf("public tool should be visible")
	}
	priv := &Tool{Visibility: VisibilityPrivate, TenantID: "tenant-a"}
	if toolVisible(priv, &Identity{TenantID: "tenant-b"}) {
		t.Fatalf("private tool of other tenant must not be visible")
	}
	if !toolVisible(priv, &Identity{TenantID: "tenant-a"}) {
		t.Fatalf("private tool should be visible in own tenant")
	}
}

func TestValidateParamsWhitelistAndSchema(t *testing.T) {
	tool := &Tool{AllowedParams: []string{"q"}}
	if err := validateParams(tool, map[string]any{"q": "x"}); err != nil {
		t.Fatalf("whitelisted param rejected: %v", err)
	}
	if err := validateParams(tool, map[string]any{"q": "x", "sql": "drop"}); err == nil {
		t.Fatalf("non-whitelisted param accepted")
	}

	schemaTool := &Tool{InputSchema: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]}`)}
	if err := validateParams(schemaTool, map[string]any{"n": 1}); err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
	if err := validateParams(schemaTool, map[string]any{"n": "x"}); err == nil {
		t.Fatalf("invalid args accepted")
	}
	if err := validateParams(schemaTool, map[string]any{}); err == nil {
		t.Fatalf("missing required arg accepted")
	}
}

func TestClassOf(t *testing.T) {
	if classOf(&Tool{Sensitivity: SensitivityRead}) != ClassRead {
		t.Fatalf("read sensitivity should map to read class")
	}
	if classOf(&Tool{Sensitivity: SensitivityWrite}) != ClassWrite {
		t.Fatalf("write sensitivity should map to write class")
	}
	if classOf(&Tool{Sensitivity: SensitivityHigh}) != ClassWrite {
		t.Fatalf("high sensitivity should map to write class")
	}
	if classOf(&Tool{Class: ClassLLM}) != ClassLLM {
		t.Fatalf("explicit class should win")
	}
}

func TestLimiterTokenBucket(t *testing.T) {
	l := NewLimiter()
	// 3 次/分钟 ⇒ 桶容量 3。
	for i := 0; i < 3; i++ {
		ok, _ := l.Allow("k", 3)
		if !ok {
			t.Fatalf("call %d should be admitted", i)
		}
	}
	ok, wait := l.Allow("k", 3)
	if ok {
		t.Fatalf("4th call should be rejected")
	}
	if wait <= 0 {
		t.Fatalf("retry-after should be positive, got %v", wait)
	}
	// 不限流恒放行。
	if ok, _ := l.Allow("free", 0); !ok {
		t.Fatalf("limit=0 should allow")
	}
	l.Reset("k")
	if ok, _ := l.Allow("k", 3); !ok {
		t.Fatalf("after reset should be admitted")
	}
}

func TestCircuitBreakerLifecycle(t *testing.T) {
	c := NewCircuitBreaker(2, time.Minute)
	c.Failure("a")
	if !c.Allow("a") {
		t.Fatalf("breaker should stay closed after 1 failure")
	}
	c.Failure("a")
	if c.Allow("a") {
		t.Fatalf("breaker should open after threshold")
	}
	if c.State("a") != circuitOpen {
		t.Fatalf("state=%s", c.State("a"))
	}
	c.Success("a")
	if c.State("a") != circuitClosed {
		t.Fatalf("state should reset after success")
	}
}

func TestCapabilityRouterCatalogAndClassification(t *testing.T) {
	r := NewCapabilityRouter()
	if len(r.AllCapabilities()) == 0 {
		t.Fatalf("capability catalog is empty")
	}
	policy := r.ToolPolicy("search_memory")
	if policy.Classification != "read" {
		t.Fatalf("search_memory classification=%s want read", policy.Classification)
	}
	if policy.RateLimit != 120 {
		t.Fatalf("read policy rateLimit=%d want 120", policy.RateLimit)
	}
	unknown := r.ToolPolicy("no_such_tool")
	if unknown.Classification != "unknown" {
		t.Fatalf("unknown classification=%s", unknown.Classification)
	}
	stats := r.CapabilityStats()
	if stats["total"].(int) == 0 {
		t.Fatalf("stats total should be > 0")
	}
}

func TestCapabilityRouterRecommend(t *testing.T) {
	r := NewCapabilityRouter()
	recs := r.RecommendTools("搜索记忆", 3)
	if len(recs) == 0 {
		t.Fatalf("expected recommendations for 搜索记忆")
	}
	if recs[0].Score <= 0 {
		t.Fatalf("recommendation score should be positive")
	}
}

func TestCapabilityToolsMapping(t *testing.T) {
	if !ValidCapability("memory") {
		t.Fatalf("memory should be a valid capability")
	}
	if ValidCapability("not-a-capability") {
		t.Fatalf("unknown capability should be invalid")
	}
	if len(CapabilityTools("code")) == 0 {
		t.Fatalf("code capability should map tools")
	}
}

func TestLocalLedgerLookup(t *testing.T) {
	store := NewMemoryStore()
	ledger := NewLocalLedger(store)
	ctx := context.Background()

	secret := "sk-mcp-test"
	now := time.Now()
	if err := store.PutKey(ctx, Key{
		ID: "k1", TenantID: "t1", Hash: HashSecret(secret), Status: StatusActive,
		AllowedTools: []string{"a__b"}, CreatedAt: now,
	}); err != nil {
		t.Fatalf("put key: %v", err)
	}
	rec, err := ledger.Lookup(ctx, secret)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if rec == nil || !rec.Found || !rec.Active {
		t.Fatalf("expected found+active, got %+v", rec)
	}
	missing, err := ledger.Lookup(ctx, "other")
	if err != nil {
		t.Fatalf("lookup missing: %v", err)
	}
	if missing.Found {
		t.Fatalf("unknown key must not be found")
	}
}

func TestRemoteLedgerFailOpenOnUnreachable(t *testing.T) {
	ledger := NewRemoteLedger("http://127.0.0.1:1", "token", time.Millisecond, 50*time.Millisecond)
	rec, err := ledger.Lookup(context.Background(), "sk-mcp-x")
	if err == nil {
		t.Fatalf("expected error for unreachable ledger")
	}
	// fail-open：查不到就放行（交由后端校验）。
	if rec.Found {
		t.Fatalf("fail-open must not report found")
	}
}

func TestRemoteLedgerReadsPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"found": true, "active": true, "id": "k9", "tenant_id": "t1",
			"allowed_tools": []string{"x__y"}, "tool_allow_enforce": true,
		})
	}))
	defer srv.Close()

	ledger := NewRemoteLedger(srv.URL, "tok", time.Second, time.Second)
	rec, err := ledger.Lookup(context.Background(), "sk-mcp-x")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !rec.Found || !rec.Active || rec.ID != "k9" || !rec.Enforce {
		t.Fatalf("unexpected record: %+v", rec)
	}
}
