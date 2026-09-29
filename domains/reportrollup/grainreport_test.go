package reportrollup

// grainreport_test.go —— 多维读面的纯函数单测（2026-09-29 多维筛选轮）。
//
// 真库 E2E（SQL 正确性、Σ分组=总计、旧口径回落）见 grainreport_e2e_test.go；
// 这里只覆盖不碰库的纯逻辑：scope_key 编码单射性、分组集分类、参数序号、
// 错误分布与派生指标。

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func i64(v int64) *int64 { return &v }

func TestGrainScopeKey_Single(t *testing.T) {
	// 编码是 UNIQUE 四键之一：两个不同元组绝不能编出同一个串，否则 ON
	// CONFLICT 会把两天的用量互相覆盖。
	cases := []struct {
		name                         string
		provider, credential, apiKey *int64
		tenant, person               string
		wantPrefix                   string
	}{
		{"全落定", i64(1), i64(2), i64(3), "acme", "alice", "p1|c2|k3|t4:acme|u5:alice|"},
		{"provider 未落定", nil, i64(2), i64(3), "acme", "alice", "p-|c2|k3|t4:acme|u5:alice|"},
		{"凭据与 apikey 均未落定", nil, nil, nil, "default", "unknown", "p-|c-|k-|t7:default|u7:unknown|"},
		{"含分隔符的租户键", i64(9), nil, i64(4), "a|b:c", "u|v:c", "p9|c-|k4|t5:a|b:c|u5:u|v:c|"},
		{"空租户与空用户", i64(1), i64(2), i64(3), "", "", "p1|c2|k3|t0:|u0:|"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := grainScopeKey(tc.provider, tc.credential, tc.apiKey, tc.tenant, tc.person)
			if !strings.HasPrefix(got, tc.wantPrefix) {
				t.Fatalf("prefix mismatch:\n got %q\nwant prefix %q", got, tc.wantPrefix)
			}
		})
	}
}

func TestGrainScopeKey_NoCollision(t *testing.T) {
	// 分隔符歧义回归：租户键/用户名里本来就可能含 `|` 与 `:`，长度前缀
	// 是唯一让编码保持单射的东西。
	seen := map[string]string{}
	tenants := []string{"", "a", "a|b", "a|b:c", "ab", "a:bc", "|", "a|bc:"}
	people := []string{"", "u", "u|v", "u|v:w", "uv"}
	for _, tn := range tenants {
		for _, p := range people {
			key := grainScopeKey(i64(1), i64(2), i64(3), tn, p)
			label := "tenant=" + tn + " person=" + p
			if prev, ok := seen[key]; ok {
				t.Fatalf("collision: (%s) 与 (%s) 同码 %q", label, prev, key)
			}
			seen[key] = label
		}
	}
	// NULL 与 0 也必须区分（0 是合法 id，NULL 是未落定）。
	if grainScopeKey(nil, nil, nil, "d", "u") == grainScopeKey(i64(0), i64(0), i64(0), "d", "u") {
		t.Fatal("NULL 与 0 编出同一个键")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		row  grainScanRow
		want groupKind
	}{
		{"总计", grainScanRow{gDate: true, gProvider: true, gCredential: true, gModel: true, gTenant: true, gPerson: true, gKey: true}, gkTotal},
		{"按天", grainScanRow{gProvider: true, gCredential: true, gModel: true, gTenant: true, gPerson: true, gKey: true}, gkDay},
		{"按天×模型", grainScanRow{gProvider: true, gCredential: true, gTenant: true, gPerson: true, gKey: true}, gkDayModel},
		{"按供应商", grainScanRow{gDate: true, gCredential: true, gModel: true, gTenant: true, gPerson: true, gKey: true}, gkProvider},
		{"按供应商×模型", grainScanRow{gDate: true, gCredential: true, gTenant: true, gPerson: true, gKey: true}, gkProviderModel},
		{"按凭据", grainScanRow{gDate: true, gProvider: true, gModel: true, gTenant: true, gPerson: true, gKey: true}, gkCredential},
		{"按模型", grainScanRow{gDate: true, gProvider: true, gCredential: true, gTenant: true, gPerson: true, gKey: true}, gkModel},
		{"按租户", grainScanRow{gDate: true, gProvider: true, gCredential: true, gModel: true, gPerson: true, gKey: true}, gkTenant},
		{"按用户", grainScanRow{gDate: true, gProvider: true, gCredential: true, gModel: true, gTenant: true, gKey: true}, gkPerson},
		{"按 apikey", grainScanRow{gDate: true, gProvider: true, gCredential: true, gModel: true, gTenant: true, gPerson: true}, gkAPIKey},
		{"按天×供应商", grainScanRow{gCredential: true, gModel: true, gTenant: true, gPerson: true, gKey: true}, gkDayProvider},
		{"按天×凭据", grainScanRow{gProvider: true, gModel: true, gTenant: true, gPerson: true, gKey: true}, gkDayCredential},
		{"按天×租户", grainScanRow{gProvider: true, gCredential: true, gModel: true, gPerson: true, gKey: true}, gkDayTenant},
		{"按天×用户", grainScanRow{gProvider: true, gCredential: true, gModel: true, gTenant: true, gKey: true}, gkDayPerson},
		{"按天×apikey", grainScanRow{gProvider: true, gCredential: true, gModel: true, gTenant: true, gPerson: true}, gkDayAPIKey},
	}
	for _, tc := range cases {
		if got := classify(tc.row); got != tc.want {
			t.Errorf("%s: classify = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFilterArgCount(t *testing.T) {
	// 占位符序号推错会让回落查询多绑或少绑参数（真库实测：
	// "expected 4 arguments, got 3"）。
	f := GrainFilter{}
	if got := filterArgCount(f); got != 0 {
		t.Fatalf("zero filter: %d, want 0", got)
	}
	f.ProviderID = i64(1)
	f.CredentialID = i64(2)
	f.APIKeyID = i64(3)
	f.TenantID = "t"
	f.Person = "p"
	f.Model = "m"
	if got := filterArgCount(f); got != 6 {
		t.Fatalf("full filter: %d, want 6", got)
	}
}

func TestGrainFilterEmpty(t *testing.T) {
	if !(GrainFilter{}).empty() {
		t.Fatal("零值过滤应为空")
	}
	if (GrainFilter{Model: "m"}).empty() {
		t.Fatal("带模型过滤不应为空")
	}
	if (GrainFilter{CredentialID: i64(0)}).empty() {
		t.Fatal("credential_id=0 是合法 id（哨兵是 -1），不应判空")
	}
}

func TestGrainAccumulator_AddAcrossKinds(t *testing.T) {
	// 度量只在 is_err=0 行上累加，错误分布只在对应 kind 行上累加——两者
	// 分开才不会因 jsonb explode 把请求数重复计（真库实测踩过：一度
	// 135181 条只剩 4715 条）。
	a := &grainAccumulator{}
	a.add(grainScanRow{requestCount: 100, successCount: 80, inputTokens: 1000, outputTokens: 200,
		cacheRead: 50, currency: "USD", internalCent: 12.5})
	a.add(grainScanRow{kind: "timeout", errKindCount: 20})
	a.add(grainScanRow{kind: "rate_limit_exceeded", errKindCount: 5})
	tot := a.finalize()
	if tot.RequestCount != 100 {
		t.Fatalf("RequestCount = %d, want 100", tot.RequestCount)
	}
	if tot.SuccessCount != 80 || tot.ErrorCount != 20 {
		t.Fatalf("success/error = %d/%d, want 80/20", tot.SuccessCount, tot.ErrorCount)
	}
	br := a.breakdown()
	if br["timeout"] != 20 || br["rate_limit_exceeded"] != 5 {
		t.Fatalf("breakdown = %v", br)
	}
	if tot.ErrorRate != 0.2 {
		t.Fatalf("ErrorRate = %v, want 0.2", tot.ErrorRate)
	}
	if tot.CacheHitRatio == nil || *tot.CacheHitRatio < 0.04 || *tot.CacheHitRatio > 0.05 {
		t.Fatalf("CacheHitRatio = %v, want ~0.0476", tot.CacheHitRatio)
	}
	if tot.TotalTokens != 1000+200+50 {
		t.Fatalf("TotalTokens = %d", tot.TotalTokens)
	}
	if tot.InternalCostCents != 12.5 || tot.InternalCurrency != "CNY" {
		t.Fatalf("internal = %v/%v", tot.InternalCostCents, tot.InternalCurrency)
	}
}

func TestTopErrorKind_StableTieBreak(t *testing.T) {
	kind, n := topErrorKind(map[string]int64{"b": 5, "a": 5, "c": 1})
	if kind != "a" || n != 5 {
		t.Fatalf("并列时应取字典序最小且稳定：got %q/%d", kind, n)
	}
	if kind, n := topErrorKind(nil); kind != "" || n != 0 {
		t.Fatalf("空分布应返回空：got %q/%d", kind, n)
	}
}

func TestDimKey_Unassigned(t *testing.T) {
	if got := dimKey(nil); got != UnassignedID {
		t.Fatalf("dimKey(nil) = %d, want %d", got, UnassignedID)
	}
	if got := dimKey(i64(0)); got != 0 {
		t.Fatalf("dimKey(0) = %d, want 0", got)
	}
}

func TestPersonKey_跨租户不撞(t *testing.T) {
	// internal_person 踩过的坑：跨租户同名人员在四键 UNIQUE 下互相覆盖。
	// grain 的人员分组键必须带租户前缀。
	if personKey("t1", "alice") == personKey("t2", "alice") {
		t.Fatal("不同租户的同名人员撞键")
	}
	if got, want := splitKey(personKey("t1", "alice")); got != "t1" || want != "alice" {
		t.Fatalf("splitKey = %q/%q", got, want)
	}
}

func TestGrainAggSQL_FilterPlaceholders(t *testing.T) {
	// 六个过滤条件全部开启时，排除占位符必须落在 $10（$1..$3 是 scope/
	// 起止日，$4..$9 是六个过滤）。
	f := GrainFilter{ProviderID: i64(1), CredentialID: i64(2), APIKeyID: i64(3),
		TenantID: "t", Person: "p", Model: "m"}
	sql := grainAggSQL([]string{"daily_grain"}, f, true, ScopeInternalGrain)
	for _, want := range []string{
		"provider_id = $4", "credential_id = $5", "api_key_id = $6",
		"tenant_id = $7", "person = $8", "raw_model_name = $9",
		"gd.scope = $10",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL 缺少片段 %q", want)
		}
	}
	// daily=false 时不得带「按天 × 单维度」明细分组集；但**按天**与
	// 「按天 × 模型」必须常驻——趋势图与模型堆叠图每次都要用，它们不是
	// 明细模式的产物。
	detailOnly := map[string]bool{
		"(report_date, provider_id)":   true,
		"(report_date, credential_id)": true,
		"(report_date, tenant_id)":     true,
		"(report_date, person)":        true,
		"(report_date, api_key_id)":    true,
	}
	summary := grainGroupSets(false)
	for _, s := range summary {
		if detailOnly[s] {
			t.Errorf("汇总模式不该出现明细分组集：%s", s)
		}
	}
	for _, must := range []string{"()", "(report_date)", "(report_date, raw_model_name)"} {
		found := false
		for _, s := range summary {
			if s == must {
				found = true
			}
		}
		if !found {
			t.Errorf("汇总模式必须保留图表分组集：%s", must)
		}
	}
	// 明细模式 = 汇总模式 + 上面五个「按天 × 维度」，不多不少。
	base := map[string]bool{}
	for _, s := range summary {
		base[s] = true
	}
	detail := grainGroupSets(true)
	if len(detail) != len(base)+len(detailOnly) {
		t.Fatalf("明细分组集数量 = %d，汇总 %d + 明细 %d", len(detail), len(base), len(detailOnly))
	}
	for _, s := range detail {
		if base[s] || detailOnly[s] {
			continue
		}
		t.Errorf("明细模式多出了未知分组集：%s", s)
	}
	if !strings.Contains(sql, "(report_date, provider_id)") {
		t.Error("明细模式应含按天×供应商分组集")
	}
}

// 错误类型分支与度量分支的分组集必须**逐个对应**（只在末尾多一个 kind）。
// 不对应就会静默丢层级：classify() 靠 GROUPING() 标志派发，缺一个层级
// 等于该层级没有主要错误，而两侧行数相同、查询不报错。
func TestGrainGroupSets_ErrSetsMirrorMetrics(t *testing.T) {
	for _, daily := range []bool{false, true} {
		metrics := grainGroupSets(daily)
		errs := grainErrGroupSets(daily)
		if len(metrics) != len(errs) {
			t.Fatalf("daily=%v：度量分组集 %d 个，错误分组集 %d 个", daily, len(metrics), len(errs))
		}
		for i, m := range metrics {
			inner := strings.TrimSuffix(strings.TrimPrefix(m, "("), ")")
			want := "(kind)"
			if inner != "" {
				want = "(" + inner + ", kind)"
			}
			if errs[i] != want {
				t.Errorf("daily=%v 第 %d 个不对应：度量 %s ↔ 错误 %s（期望 %s）",
					daily, i, m, errs[i], want)
			}
		}
		// 度量分组集里不应再有 kind——kind 只属于错误分支，否则 explode
		// 出来的行会跟着走完全部分组集（真库实测 826ms → 拆开后 15ms）。
		for _, m := range metrics {
			if strings.Contains(m, "kind") {
				t.Errorf("度量分组集不该带 kind：%s", m)
			}
		}
	}
}

// 错误分支只产出计数，度量列必须显式置 0——不能沿用度量分支的表达式，
// 否则 explode 行又把度量带回来了（那正是本次要拆掉的开销）。
func TestGrainAggSQL_ErrBranchZeroesMetrics(t *testing.T) {
	sql := grainAggSQL([]string{"daily_grain"}, GrainFilter{}, true, "")
	parts := strings.Split(sql, "UNION ALL")
	if len(parts) != 2 {
		t.Fatalf("SQL 应恰好由度量分支 + 错误分支两段 UNION ALL 组成，实际 %d 段", len(parts))
	}
	errBranch := parts[1]
	if !strings.Contains(errBranch, "jsonb_each_text") {
		t.Fatal("错误分支里找不到 jsonb_each_text")
	}
	for _, want := range []string{
		"0::bigint AS request_count",
		"0::bigint AS success_count",
		"0::float8 AS internal_cents",
		"SUM(kind_n), 0)::bigint AS err_kind_count",
	} {
		if !strings.Contains(errBranch, want) {
			t.Errorf("错误分支缺少 %q", want)
		}
	}
	// explode 出来的 kind 行不得再参与度量聚合。
	if strings.Contains(errBranch, "SUM(request_count") {
		t.Error("错误分支不应再对 request_count 求和（度量已由另一分支产出）")
	}
}

// 空区间（以及旧口径回落查询里「grain 已覆盖的日期全被排除」的情形）会让
// GROUPING SETS 的输入为空。PG 此时仍返回一行「幽灵总行」：分组键全 NULL、
// GROUPING() 全 1、所有 SUM 为 NULL。度量分支必须逐个 COALESCE 成 0，否则
// 扫描直接报「cannot scan NULL into *int64」，整个端点 500。
//
// 这条测试同时钉住「错误分支的字面 0」——两侧都必须是非 NULL，缺一侧就会在
// 另一种输入下炸。
func TestGrainAggSQL_EmptyInputNeverYieldsNull(t *testing.T) {
	sql := grainAggSQL([]string{"daily_grain"}, GrainFilter{}, true, "")
	metrics := []string{
		"request_count", "success_count", "input_tokens", "output_tokens",
		"cache_read_tokens", "cache_write_tokens", "cost_cents", "credits",
		"internal_cents", "p50_wsum", "p95_wsum", "wsum",
	}
	for _, m := range metrics {
		if !strings.Contains(sql, "COALESCE(SUM(") ||
			!strings.Contains(sql, ", 0)") {
			t.Fatalf("SQL 里连 COALESCE 都没有")
		}
		if !regexp.MustCompile(`COALESCE\(SUM\([^)]*\), 0\)::[a-z0-9]+ AS ` + m + `\b`).MatchString(sql) {
			t.Errorf("指标 %s 缺少 COALESCE(...,0) —— 空输入会扫描失败", m)
		}
	}
	if !regexp.MustCompile(`COALESCE\(SUM\(kind_n\), 0\)::bigint AS err_kind_count`).MatchString(sql) {
		t.Error("err_kind_count 缺少 COALESCE(...,0)")
	}
}

func TestGrainAggSQL_PartialFilterNoDanglingPlaceholder(t *testing.T) {
	// 占位符写死 $4..$9 时，条件不全会留下悬空参数，PG 报 42P18
	// 「could not determine data type of parameter $N」——真库实测踩过
	// （只开 provider+credential+tenant，$6 悬空，整条查询失败）。
	for _, f := range []GrainFilter{
		{ProviderID: i64(1), CredentialID: i64(2), TenantID: "t"},
		{Model: "m"},
		{Person: "p"},
		{APIKeyID: i64(3)},
		{CredentialID: i64(2), Model: "m"},
		{},
	} {
		sql := grainAggSQL([]string{"daily_grain"}, f, true, ScopeDailyGrain)
		used := placeholderSet(sql)
		// 参数必须从 $4 起**连号**用到 $N：出现空洞（用了 $5 却没用 $6）
		// 就是悬空占位符。N = 3（scope/起止日）+ 过滤数 + 排除 scope。
		want := 3 + filterArgCount(f)
		if excludeScope := ScopeDailyGrain; excludeScope != "" {
			want++
		}
		for i := 1; i <= want; i++ {
			if !used[i] {
				t.Errorf("filter %+v：$%d 未被使用（占位符不连号 = 悬空参数，PG 42P18）", f, i)
			}
		}
		if used[want+1] {
			t.Errorf("filter %+v：出现了超出 $%d 的占位符", f, want)
		}
	}
	// 不过滤 + 不回落时，只有 $1..$3。
	plain := placeholderSet(grainAggSQL([]string{"daily_grain"}, GrainFilter{}, false, ""))
	if len(plain) != 3 {
		t.Errorf("空过滤应只用 $1..$3，实际 %v", plain)
	}
}

func placeholderSet(sql string) map[int]bool {
	out := map[int]bool{}
	for _, m := range regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(sql, -1) {
		n, _ := strconv.Atoi(m[1])
		out[n] = true
	}
	return out
}

func TestGrainScopes_ViewMapping(t *testing.T) {
	g, legacy, err := GrainScopes(ViewProvider)
	if err != nil || g != ScopeDailyGrain || len(legacy) != 3 {
		t.Fatalf("provider: %v %v %v", g, legacy, err)
	}
	g, legacy, err = GrainScopes(ViewInternal)
	if err != nil || g != ScopeInternalGrain || len(legacy) != 3 {
		t.Fatalf("internal: %v %v %v", g, legacy, err)
	}
	if _, _, err := GrainScopes(View("bogus")); err == nil {
		t.Fatal("非法视角应报错")
	}
	if got := grainLegacyScopeForView(ViewProvider); got != ScopeDailyTotal {
		t.Fatalf("provider 回落 scope = %s", got)
	}
	if got := grainLegacyScopeForView(ViewInternal); got != ScopeInternalTenant {
		t.Fatalf("internal 回落 scope = %s", got)
	}
}
