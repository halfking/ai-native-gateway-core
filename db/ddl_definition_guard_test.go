package db

// ddl_definition_guard_test.go —— R60 修正轮（S6-1）纯函数单测：不需要真库。
//
// 用例中的 "stored" 侧文本全部取自本机 PG17（llm-gateway-pg, PG 17.10）
// 真实 pg_policies.qual / pg_get_triggerdef(oid) 渲染输出（2026-09-23 抓取），
// 保证规范化规则与 ruleutils 实际行为逐字对齐，而不是凭记忆假设。

import (
	"strings"
	"testing"
)

// ─────────────────────────── POLICY 侧 ───────────────────────────

// 来源：db.go ensureRequestJourneyObservationSchema（期望 SQL 原文，含换行缩进）。
const testOutboxTenantPolicyDDL = `CREATE POLICY request_journey_observation_outbox_tenant_isolation
	ON request_journey_observation_outbox
	USING (tenant_id = current_setting('app.current_tenant', true)::TEXT)
	WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::TEXT)`

// 来源：本机 PG17 pg_policies 同名策略的 qual 渲染。
const testOutboxTenantStoredQual = `(tenant_id = current_setting('app.current_tenant'::text, true))`

// 来源：db.go（期望）。
const testUsersPolicyDDL = `CREATE POLICY tenant_isolation_users ON public.users
  USING ((tenant_id)::text = (public.get_current_tenant())::text);`

// 来源：本机 PG17 pg_policies —— 注意 ruleutils 保留 LHS varchar→text 的
// RelabelType (::text)、丢弃 RHS 同型 text→text 转换、省略 public. 限定符。
const testUsersStoredQual = `((tenant_id)::text = get_current_tenant())`

// 来源：db.go ensureSupplementalRLS tool_registry（期望）。
const testToolRegistryPolicyDDL = `CREATE POLICY tenant_isolation_tool_registry ON public.tool_registry
    USING ((tenant_id)::text = (public.get_current_tenant())::text
           OR (tenant_id) IS NULL OR (tenant_id) = 'default')`

// 来源：本机 PG17 pg_policies。
const testToolRegistryStoredQual = `(((tenant_id)::text = get_current_tenant()) OR (tenant_id IS NULL) OR ((tenant_id)::text = 'default'::text))`

// 来源：db.go tenant_model_policies_audit（期望）。
const testTmpAuditPolicyDDL = `CREATE POLICY tenant_isolation_tmp_audit ON public.tenant_model_policies_audit
    USING ((tenant_id)::text = (public.get_current_tenant())::text
           OR (tenant_id) IS NULL)`

// 来源：本机 PG17 pg_policies —— audit 表 tenant_id 是 TEXT，LHS RelabelType
// 被丢弃（与 users 的 VARCHAR 情形对照，验证剥文本族转换的必要性）。
const testTmpAuditStoredQual = `((tenant_id = get_current_tenant()) OR (tenant_id IS NULL))`

// 来源：db.go ensureCredentialKeysSchema（期望，USING + WITH CHECK 三连 OR）。
const testCredKeysPolicyDDL = `CREATE POLICY tenant_isolation_credential_keys ON public.credential_keys
    USING (
        tenant_id = public.get_current_tenant()
        OR current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    )
    WITH CHECK (
        tenant_id = public.get_current_tenant()
        OR current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    )`

// 来源：本机 PG17 pg_policies（qual 与 with_check 同文）。
const testCredKeysStoredQual = `((tenant_id = get_current_tenant()) OR (current_setting('app.current_role'::text, true) = 'super_admin'::text) OR (current_setting('app.bypass_rls'::text, true) = 'true'::text))`

// 来源：db.go ensureAnalysisEventsRLS（期望，仅 USING 无 WITH CHECK）。
const testAnalysisTenantPolicyDDL = `CREATE POLICY tenant_isolation_analysis_events ON public.analysis_events
    USING ((tenant_id)::text = (public.get_current_tenant())::text);`

// 来源：db.go ensureResponseFormatAnomaliesSchema（期望）。
const testRFAPolicyDDL = `CREATE POLICY response_format_anomalies_tenant_isolation ON public.response_format_anomalies
	USING (tenant_id = public.get_current_tenant())
	WITH CHECK (tenant_id = public.get_current_tenant())`

func mustPolicy(t *testing.T, ddl string) policyDef {
	t.Helper()
	def, err := parsePolicyDDL(ddl)
	if err != nil {
		t.Fatalf("parsePolicyDDL(%q): %v", ddl, err)
	}
	return def
}

func TestParsePolicyDDLBasics(t *testing.T) {
	def := mustPolicy(t, testOutboxTenantPolicyDDL)
	if def.Name != "request_journey_observation_outbox_tenant_isolation" {
		t.Errorf("name = %q", def.Name)
	}
	if def.Table != "request_journey_observation_outbox" || def.Schema != "public" {
		t.Errorf("table/schema = %q/%q", def.Table, def.Schema)
	}
	// 无 FOR/TO/AS → 默认 ALL/{public}/PERMISSIVE（与 pg_policies 实测一致）
	if def.Cmd != "ALL" || def.Permissive != "PERMISSIVE" {
		t.Errorf("cmd/permissive = %q/%q", def.Cmd, def.Permissive)
	}
	if len(def.Roles) != 1 || def.Roles[0] != "public" {
		t.Errorf("roles = %v", def.Roles)
	}
	if def.Using != def.WithCheck || !strings.Contains(def.Using, "current_setting('app.current_tenant', true)") {
		t.Errorf("using = %q", def.Using)
	}
}

func TestPolicyMatchesRealCatalogRendering(t *testing.T) {
	cases := []struct {
		note   string
		ddl    string
		stored storedPolicyRow
		want   bool
	}{
		{
			note: "outbox tenant isolation: literal ::text cast + dropped explicit cast",
			ddl:  testOutboxTenantPolicyDDL,
			stored: storedPolicyRow{
				Cmd: "ALL", Permissive: "PERMISSIVE", Roles: []string{"public"},
				Qual: strPtr(testOutboxTenantStoredQual), WithCheck: strPtr(testOutboxTenantStoredQual),
			},
			want: true,
		},
		{
			note: "users: LHS relabel kept / RHS same-type cast dropped / qualifier stripped",
			ddl:  testUsersPolicyDDL,
			stored: storedPolicyRow{
				Cmd: "ALL", Permissive: "PERMISSIVE", Roles: []string{"public"},
				Qual: strPtr(testUsersStoredQual), WithCheck: nil,
			},
			want: true,
		},
		{
			note: "tool_registry: triple OR + IS NULL + literal cast",
			ddl:  testToolRegistryPolicyDDL,
			stored: storedPolicyRow{
				Cmd: "ALL", Permissive: "PERMISSIVE", Roles: []string{"public"},
				Qual: strPtr(testToolRegistryStoredQual), WithCheck: nil,
			},
			want: true,
		},
		{
			note: "tmp_audit: text column relabel fully dropped by PG",
			ddl:  testTmpAuditPolicyDDL,
			stored: storedPolicyRow{
				Cmd: "ALL", Permissive: "PERMISSIVE", Roles: []string{"public"},
				Qual: strPtr(testTmpAuditStoredQual), WithCheck: nil,
			},
			want: true,
		},
		{
			note: "credential_keys: USING + WITH CHECK, triple OR with literal casts",
			ddl:  testCredKeysPolicyDDL,
			stored: storedPolicyRow{
				Cmd: "ALL", Permissive: "PERMISSIVE", Roles: []string{"public"},
				Qual: strPtr(testCredKeysStoredQual), WithCheck: strPtr(testCredKeysStoredQual),
			},
			want: true,
		},
		{
			note: "analysis_events tenant isolation (USING only, WC NULL)",
			ddl:  testAnalysisTenantPolicyDDL,
			stored: storedPolicyRow{
				Cmd: "ALL", Permissive: "PERMISSIVE", Roles: []string{"public"},
				Qual: strPtr("(tenant_id = get_current_tenant())"), WithCheck: nil,
			},
			want: true,
		},
		{
			note: "qual drift must NOT match (policy evolution lands on existing install)",
			ddl:  testRFAPolicyDDL,
			stored: storedPolicyRow{
				Cmd: "ALL", Permissive: "PERMISSIVE", Roles: []string{"public"},
				Qual:      strPtr("(tenant_id = get_current_tenant() AND true)"),
				WithCheck: strPtr("(tenant_id = get_current_tenant())"),
			},
			want: false,
		},
		{
			note: "with_check drift must NOT match",
			ddl:  testRFAPolicyDDL,
			stored: storedPolicyRow{
				Cmd: "ALL", Permissive: "PERMISSIVE", Roles: []string{"public"},
				Qual:      strPtr("(tenant_id = get_current_tenant())"),
				WithCheck: nil, // 期望有 WITH CHECK，存储缺 → 必须重放
			},
			want: false,
		},
		{
			note: "cmd drift must NOT match",
			ddl:  testRFAPolicyDDL,
			stored: storedPolicyRow{
				Cmd: "SELECT", Permissive: "PERMISSIVE", Roles: []string{"public"},
				Qual:      strPtr("(tenant_id = get_current_tenant())"),
				WithCheck: strPtr("(tenant_id = get_current_tenant())"),
			},
			want: false,
		},
		{
			note: "roles drift must NOT match",
			ddl:  testRFAPolicyDDL,
			stored: storedPolicyRow{
				Cmd: "ALL", Permissive: "PERMISSIVE", Roles: []string{"public", "app_admin"},
				Qual:      strPtr("(tenant_id = get_current_tenant())"),
				WithCheck: strPtr("(tenant_id = get_current_tenant())"),
			},
			want: false,
		},
		{
			note: "permissive drift (AS RESTRICTIVE) must NOT match",
			ddl:  testRFAPolicyDDL,
			stored: storedPolicyRow{
				Cmd: "ALL", Permissive: "RESTRICTIVE", Roles: []string{"public"},
				Qual:      strPtr("(tenant_id = get_current_tenant())"),
				WithCheck: strPtr("(tenant_id = get_current_tenant())"),
			},
			want: false,
		},
		{
			note: "unparseable stored qual → fail-open to replay",
			ddl:  testRFAPolicyDDL,
			stored: storedPolicyRow{
				Cmd: "ALL", Permissive: "PERMISSIVE", Roles: []string{"public"},
				Qual:      strPtr("(tenant_id IN ('a','b'))"),
				WithCheck: strPtr("(tenant_id = get_current_tenant())"),
			},
			want: false,
		},
	}
	for _, tc := range cases {
		def := mustPolicy(t, tc.ddl)
		if got := policyMatches(def, tc.stored); got != tc.want {
			t.Errorf("%s: policyMatches = %v, want %v", tc.note, got, tc.want)
		}
	}
}

func TestParsePolicyDDLDropSQL(t *testing.T) {
	def := mustPolicy(t, testUsersPolicyDDL)
	got := dropPolicySQL(def)
	want := "DROP POLICY IF EXISTS tenant_isolation_users ON public.users"
	if got != want {
		t.Errorf("dropPolicySQL = %q, want %q", got, want)
	}
}

func TestCanonicalizeExprEquivalences(t *testing.T) {
	cases := []struct{ a, b string }{
		// ruleutils 加/丢转换、改大小写、加括号后必须与期望原文等价
		{`current_setting('app.bypass_rls', true) = 'true'`,
			`(current_setting('app.bypass_rls'::text, true) = 'true'::text)`},
		{`(a)::text = (b)::text`, `(a = b)`},       // 同型转换两侧剥除
		{`x IS NULL`, `(x IS NULL)`},               // 括号噪声
		{`A OR (B AND C)`, `(A) OR ((B) AND (C))`}, // 优先级括号
		{`NOT a = b`, `(NOT (a = b))`},             // NOT 优先级低于比较
		{`tenant_id = public.get_current_tenant()`, `tenant_id = get_current_tenant()`},
	}
	for _, tc := range cases {
		ca, err := canonicalizeExpr(tc.a)
		if err != nil {
			t.Fatalf("canonicalizeExpr(%q): %v", tc.a, err)
		}
		cb, err := canonicalizeExpr(tc.b)
		if err != nil {
			t.Fatalf("canonicalizeExpr(%q): %v", tc.b, err)
		}
		if ca != cb {
			t.Errorf("canonical forms differ: %q vs %q (from %q / %q)", ca, cb, tc.a, tc.b)
		}
	}
}

func TestCanonicalizeExprDistinguishesSemantics(t *testing.T) {
	// 优先级不能被括号剥除抹平：(a OR b) AND c ≠ a OR (b AND c)
	a, err := canonicalizeExpr("(a OR b) AND c")
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalizeExpr("a OR (b AND c)")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("precedence flattened: %q == %q", a, b)
	}
	// 非文本族转换必须保留（语义敏感）
	c, err := canonicalizeExpr("quota::int > 0")
	if err != nil {
		t.Fatal(err)
	}
	d, err := canonicalizeExpr("quota::bigint > 0")
	if err != nil {
		t.Fatal(err)
	}
	if c == d {
		t.Errorf("meaningful cast dropped: %q == %q", c, d)
	}
}

func TestCanonicalizeExprRejectsUnknownSyntax(t *testing.T) {
	for _, expr := range []string{
		"x IN ('a','b')",     // 守卫语法外 → fail-open
		"x BETWEEN 1 AND 2",  // 同上
		"CASE WHEN a THEN b", // 同上
		"a = ",               // 截断
		"(a",                 // 括号不闭合
	} {
		if _, err := canonicalizeExpr(expr); err == nil {
			t.Errorf("canonicalizeExpr(%q) expected error, got none", expr)
		}
	}
}

// ─────────────────────────── TRIGGER 侧 ───────────────────────────

// 来源：db.go ensureRouteIncidentSchema（期望原文，多行缩进）。
const testRouteIncidentsTriggerDDL = `CREATE TRIGGER route_incidents_touch
	BEFORE UPDATE ON route_incidents
	FOR EACH ROW EXECUTE FUNCTION touch_route_incidents_updated_at()`

// 来源：本机 PG17 pg_get_triggerdef 同名 trigger。
const testRouteIncidentsStoredDef = `CREATE TRIGGER route_incidents_touch BEFORE UPDATE ON public.route_incidents FOR EACH ROW EXECUTE FUNCTION touch_route_incidents_updated_at()`

// 来源：db.go ensureRoutingOverridesAudit（期望：INSERT OR UPDATE OR DELETE）。
const testRoutingOverridesTriggerDDL = `CREATE TRIGGER routing_overrides_audit_trg
	AFTER INSERT OR UPDATE OR DELETE ON routing_overrides
	FOR EACH ROW EXECUTE FUNCTION routing_overrides_audit_fn()`

// 来源：本机 PG17 pg_get_triggerdef —— 事件顺序被 ruleutils 规范为
// INSERT OR DELETE OR UPDATE。
const testRoutingOverridesStoredDef = `CREATE TRIGGER routing_overrides_audit_trg AFTER INSERT OR DELETE OR UPDATE ON public.routing_overrides FOR EACH ROW EXECUTE FUNCTION routing_overrides_audit_fn()`

// 来源：db.go ensureCredentialGovernorRevision（期望：UPDATE OF 列清单 + WHEN）。
const testGovernorUpdateTriggerDDL = `CREATE TRIGGER trg_notify_credentials_governor_revision_update
	AFTER UPDATE OF concurrency_limit, concurrency_mode, rpm_limit, tpm_limit,
		fp_slot_limit, max_queue_depth, max_queue_wait_ms
	ON public.credentials FOR EACH ROW
	WHEN (OLD.revision IS DISTINCT FROM NEW.revision)
	EXECUTE FUNCTION public.notify_credentials_governor_revision()`

// 来源：本机 PG17 pg_get_triggerdef —— OLD/NEW 小写、WHEN 双层括号、
// 函数名去限定。
const testGovernorUpdateStoredDef = `CREATE TRIGGER trg_notify_credentials_governor_revision_update AFTER UPDATE OF concurrency_limit, concurrency_mode, rpm_limit, tpm_limit, fp_slot_limit, max_queue_depth, max_queue_wait_ms ON public.credentials FOR EACH ROW WHEN ((old.revision IS DISTINCT FROM new.revision)) EXECUTE FUNCTION notify_credentials_governor_revision()`

// 来源：db.go trg_notify_auto_route_creds（期望：WHEN (OLD.* IS DISTINCT FROM NEW.*)）。
const testAutoRouteTriggerDDL = `CREATE TRIGGER trg_notify_auto_route_creds
	AFTER UPDATE OF status, availability_state, quota_state, circuit_state,
		concurrency_limit, concurrency_mode, rpm_limit, tpm_limit, fp_slot_limit,
		max_queue_depth, max_queue_wait_ms, lifecycle_status, manual_disabled
	ON public.credentials FOR EACH ROW
	WHEN (OLD.* IS DISTINCT FROM NEW.*)
	EXECUTE FUNCTION public.notify_auto_route_refresh()`

// 来源：本机 PG17 pg_get_triggerdef。
const testAutoRouteStoredDef = `CREATE TRIGGER trg_notify_auto_route_creds AFTER UPDATE OF status, availability_state, quota_state, circuit_state, concurrency_limit, concurrency_mode, rpm_limit, tpm_limit, fp_slot_limit, max_queue_depth, max_queue_wait_ms, lifecycle_status, manual_disabled ON public.credentials FOR EACH ROW WHEN ((old.* IS DISTINCT FROM new.*)) EXECUTE FUNCTION notify_auto_route_refresh()`

// 来源：db.go trg_credential_keys_enforce_parent_tenant（期望）。
const testCredKeysParentTriggerDDL = `CREATE TRIGGER trg_credential_keys_enforce_parent_tenant
	BEFORE INSERT OR UPDATE OF credential_id, tenant_id ON public.credential_keys
	FOR EACH ROW EXECUTE FUNCTION public.credential_keys_enforce_parent_tenant()`

func mustTrigger(t *testing.T, ddl string) triggerDef {
	t.Helper()
	def, err := parseTriggerDDL(ddl)
	if err != nil {
		t.Fatalf("parseTriggerDDL(%q): %v", ddl, err)
	}
	return def
}

func TestParseTriggerDDLBasics(t *testing.T) {
	def := mustTrigger(t, testRouteIncidentsTriggerDDL)
	if def.Name != "route_incidents_touch" || def.Timing != "BEFORE" {
		t.Errorf("name/timing = %q/%q", def.Name, def.Timing)
	}
	if def.Table != "route_incidents" || def.ForEach != "ROW" {
		t.Errorf("table/forEach = %q/%q", def.Table, def.ForEach)
	}
	if len(def.Events) != 1 || def.Events[0].Kind != "update" || len(def.Events[0].Cols) != 0 {
		t.Errorf("events = %+v", def.Events)
	}
	if def.When != "" || def.Function != "touch_route_incidents_updated_at" || len(def.Args) != 0 {
		t.Errorf("when/function/args = %q/%q/%v", def.When, def.Function, def.Args)
	}
}

func TestTriggerMatchesRealCatalogRendering(t *testing.T) {
	cases := []struct {
		note   string
		ddl    string
		stored string
		want   bool
	}{
		{
			note: "touch trigger: table qualifier + spacing normalization",
			ddl:  testRouteIncidentsTriggerDDL, stored: testRouteIncidentsStoredDef, want: true,
		},
		{
			note: "audit trigger: event order INSERT OR UPDATE OR DELETE vs INSERT OR DELETE OR UPDATE",
			ddl:  testRoutingOverridesTriggerDDL, stored: testRoutingOverridesStoredDef, want: true,
		},
		{
			note: "governor update trigger: UPDATE OF cols + WHEN + OLD/NEW case + double parens",
			ddl:  testGovernorUpdateTriggerDDL, stored: testGovernorUpdateStoredDef, want: true,
		},
		{
			note: "auto route trigger: record star OLD.* / NEW.*",
			ddl:  testAutoRouteTriggerDDL, stored: testAutoRouteStoredDef, want: true,
		},
		{
			note:   "WHEN drift must NOT match (定义演进必须落库)",
			ddl:    testGovernorUpdateTriggerDDL,
			stored: strings.Replace(testGovernorUpdateStoredDef, "old.revision IS DISTINCT FROM new.revision", "true", 1),
			want:   false,
		},
		{
			note:   "UPDATE OF column list drift must NOT match",
			ddl:    testCredKeysParentTriggerDDL,
			stored: `CREATE TRIGGER trg_credential_keys_enforce_parent_tenant BEFORE INSERT OR UPDATE OF credential_id ON public.credential_keys FOR EACH ROW EXECUTE FUNCTION credential_keys_enforce_parent_tenant()`,
			want:   false,
		},
		{
			note:   "event kind drift (BEFORE→AFTER) must NOT match",
			ddl:    testRouteIncidentsTriggerDDL,
			stored: strings.Replace(testRouteIncidentsStoredDef, "BEFORE", "AFTER", 1),
			want:   false,
		},
		{
			note:   "function drift must NOT match",
			ddl:    testRouteIncidentsTriggerDDL,
			stored: strings.Replace(testRouteIncidentsStoredDef, "touch_route_incidents_updated_at", "other_fn", 1),
			want:   false,
		},
		{
			note:   "statement-level vs row-level must NOT match",
			ddl:    testRouteIncidentsTriggerDDL,
			stored: strings.Replace(testRouteIncidentsStoredDef, "FOR EACH ROW", "FOR EACH STATEMENT", 1),
			want:   false,
		},
		{
			note: "pg_get_triggerdef parses round-trip (stored == stored)",
			ddl:  testGovernorUpdateStoredDef, stored: testGovernorUpdateStoredDef, want: true,
		},
	}
	for _, tc := range cases {
		expected := mustTrigger(t, tc.ddl)
		got, err := parseTriggerDDL(tc.stored)
		if err != nil {
			t.Fatalf("%s: parseTriggerDDL(stored): %v", tc.note, err)
		}
		if equal := triggerDefEqual(expected, got); equal != tc.want {
			t.Errorf("%s: triggerDefEqual = %v, want %v", tc.note, equal, tc.want)
		}
	}
}

func TestParseTriggerDDLDropSQL(t *testing.T) {
	def := mustTrigger(t, testRouteIncidentsTriggerDDL)
	got := dropTriggerSQL(def)
	want := "DROP TRIGGER IF EXISTS route_incidents_touch ON public.route_incidents"
	if got != want {
		t.Errorf("dropTriggerSQL = %q, want %q", got, want)
	}
}

func TestParseTriggerDDLRejectsUnknownSyntax(t *testing.T) {
	for _, ddl := range []string{
		"CREATE TRIGGER x AFTER UPDATE ON t REFERENCING NEW TABLE AS n EXECUTE FUNCTION f()",
		"CREATE TRIGGER x AFTER UPDATE ON t",                        // 缺 EXECUTE
		"CREATE TRIGGER x SOMEDAY UPDATE ON t EXECUTE FUNCTION f()", // 非法 timing
		"DROP TRIGGER x ON t",
	} {
		if _, err := parseTriggerDDL(ddl); err == nil {
			t.Errorf("parseTriggerDDL(%q) expected error, got none", ddl)
		}
	}
}

func strPtr(s string) *string { return &s }
