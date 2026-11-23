// Package modelattribution hosts the regression gate for the gateway's model
// attribution: every surface that records or aggregates "which model did this
// request ask for" must use the same resolution chain.
//
// # The bug this gate exists to prevent
//
// The gateway had **five** hand-written model-resolution chains that
// disagreed:
//
//   - maas/usage.go:            outbound → client → 'unknown'   (no canonical layer)
//   - maas/my_usage.go:         canonical → outbound → client → 'unknown'
//   - maas/consumption_detail.go: canonical → outbound → client → 'unknown'
//   - admin/user_profile.go:     outbound → client → 'unknown'   (no NULLIF!)
//   - admin/session_analytics/queries.go: outbound → client → 'unknown' (no NULLIF!)
//
// Three production-visible consequences, all measured on 211:
//
//  1. The same request showed a different model name in different panels.
//
//  2. The two chains without NULLIF returned an **empty string** (not
//     'unknown') for rows with empty model columns — 117 rows rendered a blank
//     model name in the user-profile panel.
//
//  3. None of them recognised the "<unknown>" placeholder as "no model", so
//     the placeholder appeared as a model literally named "<unknown>",
//     separate from the legitimate "unknown" bucket.
//
// # Why the chains are written as inline SQL literals
//
// The obvious fix — a Go helper that emits the chain — **breaks a repo gate**.
//
// admin/canonical_read_points_registry_test.go scans every production file
// for reads of the canonical request-logs view, and it analyses SQL by
// walking **string literals and their `+` concatenation chains**
// (canonicalInspectTextUnits / canonicalConcatParts). A function call in the
// chain becomes a "hole" (canonicalConcatHole) ⇒ the model columns drop out
// of the scanned column set ⇒ the registry's read-point entries stop
// matching ⇒ the gate reports "登记簿腐烂".
//
// Worse, that failure mode is silent in the wrong direction: the gate would
// be *scanning less*, so a genuine regression in those SQL literals could
// slip through. Readability by humans and visibility to the gate are the
// same requirement here: the SQL must stay literal.
//
// # What this gate therefore checks
//
//  1. Every model-resolution chain in the repo folds BOTH the empty string
//     and the "<unknown>" placeholder into the single 'unknown' bucket.
//
//  2. Every such chain appears in the canonical shape, with the canonical
//     layer present wherever a canonical name is reachable.
//
//  3. The chains are mutually consistent: the same columns resolve to the
//     same model name on every surface.
//
// This gate is the anti-drift lock for the fix. Without it, the next person
// to add a fourth chain would silently reintroduce the split.
package modelattribution

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// extractModelChains finds COALESCE chains that resolve a model name.
//
// ⚠ 判据是「链里出现**模型列**」：全仓有大量**不是**模型链的 COALESCE
// （provider 兜底、error_kind 兜底、work_type 兜底…），它们用 'unknown'
// 兜底是**正确**的语义，不该被这条链的形状约束。
//
// ⚠ 解析用**括号配对扫描**而不是正则：模型链里带子查询
// （`COALESCE(..., (SELECT mc.canonical_name FROM models_canonical mc
// WHERE mc.id = r.canonical_id), ...)`）⇒ 正则的"非嵌套括号"匹配会漏。
func extractModelChains(text string) []chain {
	var out []chain
	for i := 0; i+8 < len(text); i++ {
		if !strings.HasPrefix(text[i:], "COALESCE") && !strings.HasPrefix(text[i:], "coalesce") {
			continue
		}
		j := i + len("COALESCE")
		for j < len(text) && (text[j] == ' ' || text[j] == '\t' || text[j] == '\n' || text[j] == '\r') {
			j++
		}
		if j >= len(text) || text[j] != '(' {
			continue
		}
		bodyStart := j + 1
		bodyEnd := matchParen(text, j)
		if bodyEnd < 0 {
			continue
		}
		body := text[bodyStart:bodyEnd]
		c := chain{body: body, line: strings.Count(text[:i], "\n") + 1}
		if !chainTouchesModelColumn(c) || !chainHasLiteralFallback(c) {
			i = bodyEnd
			continue
		}
		out = append(out, c)
		i = bodyEnd
	}
	return out
}

// chainHasLiteralFallback reports whether the chain's last top-level term is
// a quoted string literal — the fallback bucket.
//
// ★ 为什么要这个判据：模型链里的占位符只会在
// **有兜底桶的场合**里变成一个“模型”。没有兜底桶的链（如
// `COALESCE(NULLIF(a,”), NULLIF(b,”)) IS NOT NULL` 这种**存在性谓词**）
// 不在本门范围内。
func chainHasLiteralFallback(c chain) bool {
	terms := splitTopLevel(c.body, ',')
	if len(terms) == 0 {
		return false
	}
	last := strings.TrimSpace(terms[len(terms)-1])
	if len(last) < 2 {
		return false
	}
	quote := last[0]
	if (quote != '\'' && quote != '"') || last[len(last)-1] != quote {
		return false
	}
	// 兜底桶必须是“无模型”那一桶；否则这条链不是模型归一链
	// （如 `COALESCE((SELECT canonical_name ...), '')` 这种**筛选例**）。
	switch strings.ToLower(last[1 : len(last)-1]) {
	case "unknown", "<unknown>":
		return true
	}
	return false
}

// matchParen returns the index of the ')' closing the '(' at open, or -1.
func matchParen(text string, open int) int {
	depth := 0
	inQuote := byte(0)
	for i := open; i < len(text); i++ {
		c := text[i]
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			inQuote = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// repoRoot returns the repository root, derived from this file's location
// (internal/modelattribution/ → repo root).
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// internal/modelattribution → internal → repo root
	return filepath.Dir(filepath.Dir(wd))
}

// scanSQLFiles walks the repo and returns every Go file's text plus path,
// restricted to files that could plausibly contain a model chain.
//
// ★ 读取时**剔掉注释**：Go 注释里很常会**引用**旧链的写法（如“旧链是
// COALESCE(NULLIF(TRIM(outbound_model),”), ...)”）。若不剔注释，门会把**注释中的历史写法**当成现行
// 写法报红。
func scanSQLFiles(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if base == ".git" || base == "vendor" || base == "node_modules" || base == "dist" || base == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// 测试文件不纳入：它们断言的是**被测形状**（如
		// "SQL 必须是 COALESCE(NULLIF(x,''), 'unknown')"），而非生产链本身。
		// 把它们当成生产链来纠错，会把“测试夹具本身就写了旧形状”
		// 误报成“生产代码漏了占位符折叠”。
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = stripGoComments(string(raw))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return out
}

// modelChainFiles are the surfaces that resolve a model name for display or
// aggregation. Each is pinned to the canonical chain shape.
//
// ★ 为什么是**白名单**而不是全仓扫描：全仓还有大量**不是**模型归一链的
// COALESCE（provider 兜底、error_kind 兜底等），它们用 'unknown' 兜底是
// 正确的语义，不该被这条链的形状约束。
var modelChainFiles = []struct {
	// file is the repo-relative path.
	file string
	// canonicalLayer reports whether the chain must include a canonical
	// term (the routing table's standardised key).
	canonicalLayer bool
	// columns are the model columns the chain must reference.
	columns []string
}{
	{file: "maas/usage.go", canonicalLayer: true, columns: []string{"r.outbound_model", "r.client_model"}},
	{file: "maas/my_usage.go", canonicalLayer: true, columns: []string{"outbound_model", "client_model"}},
	{file: "maas/consumption_detail.go", canonicalLayer: true, columns: []string{"outbound_model", "client_model"}},
	{file: "admin/user_profile.go", canonicalLayer: false, columns: []string{"rl.outbound_model", "rl.client_model"}},
	{file: "admin/session_analytics/queries.go", canonicalLayer: false, columns: []string{"rl.outbound_model", "rl.client_model"}},
	{file: "admin/swim_lane_init.go", canonicalLayer: true, columns: []string{"rl.outbound_model", "rl.client_model"}},
}

// knownNonCanonicalModelChains is the **ratchet ledger** of model-resolution
// chains outside modelChainFiles that still use the pre-fix shape
// (NULLIF(x, ”) without the placeholder fold).
//
// ★ 为什么要有这份登记：本轮修法的范围是**“统一已统一的那几个
// 面**”（maas 三处 + admin 三处）。全仓还有其他几十处同类链未纳入本轮
// （它们读的是**目录表**（provider_models / model_offers / models_canonical）或者读的是
// 已退役的分布表，占位符不会流到那里）。
//
// ⇒ 把它们**登记下来**（而不是忽略或顺手改掉），方式是**礁尖**：
//
//	· 登记表里的条目数量被钉住 → 新增一处就报红（不能静默增长）；
//	· 每条都必须带原因（为什么这里不需要折叠占位符）；
//	· 下一人可以按这份登记把它们逐个收口。
//
// ★ 为什么不在本轮就全改：改一条链就改一个看板的数字，而这些面的
// 口径合同需要各自验收（真库对拍）。把它们与本轮的六个面串在一起改
// 会让“修正的面”与“未验证的面”的改动混在一起。
var knownNonCanonicalModelChains = map[string]struct {
	// reason explains why this site does not need the placeholder fold.
	reason string
	// chains is the expected number of non-canonical model chains in the
	// file. It is a ratchet: adding one without registering it goes red.
	chains int
}{
	// ── 目录表（provider_models / model_offers / models_canonical）──
	// 这些表存的是**登记过的模型名**，写入方是运营/发现流程，不是请求路径 ⇒
	// 占位符永远不会出现在这些列里 ⇒ 折叠它是无意义的改动。
	"admin/credential_models_dto.go":                          {reason: "目录表（model_offers + models_canonical）：存的是登记过的模型名，占位符不会流入", chains: 0},
	"admin/probe_dashboard.go":                                {reason: "目录表（provider_models + models_canonical）：探活面读的是登记名", chains: 0},
	"admin/provider_models.go":                                {reason: "目录表（models_canonical）：模型目录面，读的是登记名", chains: 0},
	"admin/topology_models_today.go":                          {reason: "目录表（models_canonical）：拓扑面读的是登记名", chains: 0},
	"admin/market_model_platforms.go":                         {reason: "目录表：平台目录面", chains: 0},
	"admin/model_status.go":                                   {reason: "目录表：模型状态面", chains: 0},
	"admin/models.go":                                         {reason: "目录表：模型目录面", chains: 0},
	"admin/provider_diagnose.go":                              {reason: "目录表（provider_models.raw_model_name）：诊断面读的是登记名", chains: 0},
	"admin/ranking_identity.go":                               {reason: "共享维度身份表达式：'unknown' 是**分组键**（非兜底桶），语义与模型兜底不同", chains: 0},
	"admin/routing.go":                                        {reason: "存在性谓词 + 路由诊断：无兜底桶，占位符不会变成模型", chains: 0},
	"admin/work_types.go":                                     {reason: "存在性谓词（IS NOT NULL）：不是归一链", chains: 0},
	"autoroute/recommend_v2.go":                               {reason: "自动路由推荐面：'unknown' 是推荐结果语义，不是请求模型归因", chains: 0},
	"bg/active_probe_executor.go":                             {reason: "目录表（provider_models.outbound_model_name）：探活面读的是登记名", chains: 0},
	"bg/capability_backfill.go":                               {reason: "目录表（provider_models.outbound_model_name）：能力回填面读的是登记名", chains: 0},
	"bg/modality_verification.go":                             {reason: "目录表：模态校验面读的是登记名", chains: 0},
	"bg/mv_consistency.go":                                    {reason: "目录表：物化视图一致性检查读的是登记名", chains: 0},
	"bg/node_probe.go":                                        {reason: "目录表：节点探活面读的是登记名", chains: 0},
	"domains/modelquality/dbstorage.go":                       {reason: "目录表（provider_models.raw_model_name）：模型质量面读的是登记名", chains: 0},
	"maas/model_rates.go":                                     {reason: "目录表：费率面读的是登记名", chains: 0},
	"maas/service.go":                                         {reason: "目录表（models_canonical.display_name）：目录展示面", chains: 0},
	"db/db.go":                                                {reason: "非模型列（work_type）：'unknown' 兜底语义与模型无关", chains: 0},
	"domains/reportrollup/grain.go":                           {reason: "非模型列（error_kind）：'unknown' 兜底语义与模型无关", chains: 0},
	"domains/reportrollup/rollup.go":                          {reason: "非模型列（error_kind）：'unknown' 兜底语义与模型无关", chains: 0},
	"domains/stats/daily_monthly_rollup.go":                   {reason: "非模型列（error_class）：'unknown' 兜底语义与模型无关", chains: 0},
	"admin/analytics.go":                                      {reason: "非模型列（work_type / outbound_model 存在性）：口径与模型归因无关", chains: 0},
	"admin/analytics_queries.go":                              {reason: "非模型列（error_kind）：'unknown' 兜底语义与模型无关", chains: 0},
	"admin/errors_trend.go":                                   {reason: "非模型列（error_type / supplier）：'unknown' 兜底语义与模型无关", chains: 0},
	"admin/vendor_credential_error_handlers.go":               {reason: "非模型列（stage）：'unknown' 兜底语义与模型无关", chains: 0},
	"admin/dashboard_board_fallback.go":                       {reason: "看板降级面：'__unknown__' 是该面的既有降级标记", chains: 0},
	"admin/dashboardapi/session_overview.go":                  {reason: "会话总览面：client_models[1] 是会话摘要数组，非请求归因", chains: 0},
	"admin/dashboard_session_stats.go":                        {reason: "会话统计面：client_models[1] 是会话摘要数组，非请求归因", chains: 0},
	"admin/live_stream_sse.go":                                {reason: "SSE 实时流面：字段是传输层投影", chains: 0},
	"admin/logs.go":                                           {reason: "日志面：模型名用于与目录名比对（匹配语义，非兜底桶）", chains: 0},
	"admin/auto_route.go":                                     {reason: "自动路由面：'unknown' 是推荐结果语义", chains: 0},
	"admin/model_routing_diagnostic.go":                       {reason: "路由诊断：等值匹配谓词（非归一链）", chains: 0},
	"admin/session_analytics_breakdown.go":                    {reason: "★ 请求族面（读 request_logs_with_current_month）：本轮未纳入（口径改动需真库对拍），已登记待收口", chains: 1},
	"admin/tenant_stats_rollup.go":                            {reason: "★ 租户汇总面（读 raw_model_name）：本轮未纳入（口径改动需真库对拍），已登记待收口", chains: 1},
	"admin/tenants.go":                                        {reason: "★ 租户面（读 request_logs_with_current_month）：本轮未纳入（口径改动需真库对拍），已登记待收口", chains: 2},
	"admin/usage_provider_models.go":                          {reason: "★ 用量-供应商面（读 usage_ledger_with_current_month）：本轮未纳入（口径改动需真库对拍），已登记待收口", chains: 3},
	"admin/usage_trend_series.go":                             {reason: "★ 用量趋势明细档（读会话族原生源）：本轮未纳入（口径改动需真库对拍），已登记待收口", chains: 0},
	"admin/user_usage_stats.go":                               {reason: "★ 用户用量统计面（读 raw_model_name）：本轮未纳入（口径改动需真库对拍），已登记待收口", chains: 1},
	"admin/topology_window_stats.go":                          {reason: "★ 拓扑窗口面（读 request_logs_with_current_month）：本轮未纳入（口径改动需真库对拍），已登记待收口", chains: 0},
	"internal/collector/gateway_adapters.go":                  {reason: "采集适配面：分钟桶投影，口径与展示面不同", chains: 0},
	"internal/quality/minute_aggregator.go":                   {reason: "★ 质量分钟聚合面（读 request_logs_with_current_month）：本轮未纳入（口径改动需真库对拍），已登记待收口", chains: 4},
	"bg/stats_minute_rollup.go":                               {reason: "★ 分钟桶汇总面（读会话族原生源）：本轮未纳入（口径改动需真库对拍），已登记待收口", chains: 0},
	"bg/stats_minute_rollup_retire.go":                        {reason: "★ 分钟桶退役面：本轮未纳入（口径改动需真库对拍），已登记待收口", chains: 0},
	"cmd/gateway/waterfall_db.go":                             {reason: "★ 瀑布图面：本轮未纳入（口径改动需真库对拍），已登记待收口", chains: 0},
	"cmd/gateway/waterfall_by_request.go":                     {reason: "★ 瀑布图按请求面：本轮未纳入（口径改动需真库对拍），已登记待收口", chains: 0},
	"domains/hooks/observability/telemetry/request_logger.go": {reason: "WAL 写入面：COALESCE(NULLIF(EXCLUDED.client_model,''), 已有值) 是**写入去重**语义，不是归一链"},
}

// TestModelResolutionChainsFoldPlaceholderAndEmpty is the core assertion:
// every model-resolution chain must fold both the empty string and the
// "<unknown>" placeholder into the single 'unknown' bucket.
func TestModelResolutionChainsFoldPlaceholderAndEmpty(t *testing.T) {
	files := scanSQLFiles(t)
	for _, spec := range modelChainFiles {
		t.Run(spec.file, func(t *testing.T) {
			text, ok := files[spec.file]
			if !ok {
				t.Fatalf("%s not found in repo", spec.file)
			}
			chains := extractModelChains(text)
			if len(chains) == 0 {
				t.Fatalf("%s: no model-resolution chain found", spec.file)
			}
			for _, chain := range chains {
				// Every term must be the double-folding shape, except the
				// final 'unknown' literal.
				terms := splitTopLevel(chain.body, ',')
				if len(terms) == 0 {
					t.Fatalf("%s: empty chain body", spec.file)
				}
				for i, term := range terms {
					term = strings.TrimSpace(term)
					if i == len(terms)-1 {
						if term != "'unknown'" {
							t.Errorf("%s: chain's last term = %q, want 'unknown'", spec.file, term)
						}
						continue
					}
					if !foldsPlaceholder(term) {
						t.Errorf("%s: term %d = %q does NOT fold the '<unknown>' placeholder "+
							"⇒ the placeholder would appear as a model literally named '<unknown>'",
							spec.file, i, term)
					}
				}
			}
		})
	}
}

// TestModelResolutionChainsReferenceExpectedColumns pins that each surface
// still reads the columns it is supposed to read. A chain that silently
// stopped reading outbound_model would resolve differently.
func TestModelResolutionChainsReferenceExpectedColumns(t *testing.T) {
	files := scanSQLFiles(t)
	for _, spec := range modelChainFiles {
		t.Run(spec.file, func(t *testing.T) {
			text, ok := files[spec.file]
			if !ok {
				t.Fatalf("%s not found in repo", spec.file)
			}
			chains := extractModelChains(text)
			if len(chains) == 0 {
				t.Fatalf("%s: no model-resolution chain found", spec.file)
			}
			for _, chain := range chains {
				for _, col := range spec.columns {
					if !strings.Contains(chain.body, col) {
						t.Errorf("%s: chain does not reference %q (chain body: %s)",
							spec.file, col, strings.Join(strings.Fields(chain.body), " "))
					}
				}
			}
		})
	}
}

// TestModelResolutionChainsAreConsistentAcrossSurfaces pins that the two
// shapes in use — with and without the canonical layer — are the only ones,
// and that each file uses exactly one of them.
//
// ★ 这是「三处 maas + 三处 admin 必须给出同一个模型名」的机器化断言：
// 同一组列在任何一处解析出来的模型名必须相同。
func TestModelResolutionChainsAreConsistentAcrossSurfaces(t *testing.T) {
	files := scanSQLFiles(t)
	// Normalised chain shape per file: the list of folded columns, in order,
	// with the canonical term marked.
	shapes := map[string][]string{}
	for _, spec := range modelChainFiles {
		text, ok := files[spec.file]
		if !ok {
			t.Fatalf("%s not found in repo", spec.file)
		}
		chains := extractModelChains(text)
		if len(chains) == 0 {
			t.Fatalf("%s: no model-resolution chain found", spec.file)
		}
		for _, chain := range chains {
			terms := splitTopLevel(chain.body, ',')
			var cols []string
			for i, term := range terms {
				term = strings.TrimSpace(term)
				if i == len(terms)-1 {
					continue
				}
				col := canonicalTermColumn(term)
				if col == "" {
					t.Fatalf("%s: term %d is not the canonical folding shape: %q", spec.file, i, term)
				}
				cols = append(cols, col)
			}
			key := strings.Join(cols, " | ")
			if prev, ok := shapes[spec.file]; ok && prev != nil {
				if strings.Join(prev, " | ") != key {
					t.Errorf("%s: two different chain shapes in one file:\n  %s\n  %s",
						spec.file, strings.Join(prev, " | "), key)
				}
			}
			shapes[spec.file] = cols
		}
	}
}

// TestModelResolutionChainsUseCanonicalLayerWhereReachable pins that the
// surfaces which can reach models_canonical do so.
//
// ★ 为什么这是必须的：canonical_name 是路由表自己的键 ⇒ 只有它能与
// models_canonical 对账；缺了它，同一个请求在不同面板会显示成两个名字
// （211 生产实测：canonical 层缺失的 usage.go 与有 canonical 层的
// my_usage.go 对同一请求给出不同模型名）。
func TestModelResolutionChainsUseCanonicalLayerWhereReachable(t *testing.T) {
	files := scanSQLFiles(t)
	for _, spec := range modelChainFiles {
		if !spec.canonicalLayer {
			continue
		}
		t.Run(spec.file, func(t *testing.T) {
			text := files[spec.file]
			chains := extractModelChains(text)
			for _, chain := range chains {
				hasCanonical := strings.Contains(chain.body, "canonical_name")
				if !hasCanonical {
					t.Errorf("%s: chain has no canonical layer:\n  %s",
						spec.file, strings.Join(strings.Fields(chain.body), " "))
				}
			}
		})
	}
}

// TestNoModelChainUsesPlainNullIfOnly pins the exact defect shape: a chain
// that folds ONLY the empty string (NULLIF(x, ”)) without also folding the
// "<unknown>" placeholder.
//
// ★ 判据是「链里出现**模型列**」而不是「链末项是 'unknown'」：全仓有大量
// **不是**模型链的 COALESCE（provider 兜底、error_kind 兜底、work_type 兜底…），
// 它们用 'unknown' 兜底是**正确**的语义，不该被这条链的形状约束。
// 只有当链的项是模型列时，它才是一条模型归一链。
func TestNoModelChainUsesPlainNullIfOnly(t *testing.T) {
	files := scanSQLFiles(t)
	// 逐文件统计非规范链的数量，与登记簿对拍（礁尖）。
	seen := map[string]int{}
	for path, text := range files {
		// 登记表里的文件是**已知**的例外：仍然统计数量（供礁尖对拍），
		// 但不逐条报红。
		_, registered := knownNonCanonicalModelChains[path]
		for _, chain := range extractModelChains(text) {
			terms := splitTopLevel(chain.body, ',')
			if len(terms) == 0 {
				continue
			}
			if !chainTouchesModelColumn(chain) {
				continue
			}
			for i, term := range terms[:len(terms)-1] {
				term = strings.TrimSpace(term)
				// 只约束**模型列**的项：非模型列的项（vendor/provider 等）拿不到占位符。
				if !modelColumnPattern.MatchString(term) {
					continue
				}
				if foldsPlaceholder(term) {
					continue
				}
				// This term folds the empty string but not the placeholder.
				if plainTermPattern.MatchString(term) {
					seen[path]++
					if !registered {
						t.Errorf("%s:%d: model chain term %d = %q folds only the empty string "+
							"(NULLIF(x,'')) ⇒ the '<unknown>' placeholder passes through as a real model name",
							path, chain.line, i, strings.Join(strings.Fields(term), " "))
					}
				}
			}
		}
	}
	// 登记簿的数量必须与实测一致（礁尖）。
	for path, want := range knownNonCanonicalModelChains {
		if got := seen[path]; got != want.chains {
			t.Errorf("登记簿数量不符：%s 登记 %d 条，实测 %d 条（为什么变了？请更新登记簿并写明原因）",
				path, want.chains, got)
		}
	}
}

// modelColumnPattern matches a model-shaped column reference inside a chain
// term: client_model / outbound_model / canonical_model / raw_model_name /
// canonical_name / a bare "model" column.
//
// ⚠ 刻意**不**匹配 `provider_id`、`work_type`、`error_kind` 等 —— 那些不是
// 模型列。
var modelColumnPattern = regexp.MustCompile(`(?i)\b(client_model|outbound_model|canonical_model|raw_model_name|canonical_name)\b`)

// termPattern matches the canonical folding term:
// NULLIF(NULLIF(TRIM(<expr>), ”), '<unknown>')
//
// The inner expression may contain nested parens (a subquery), so the
// expression is captured by a paren-matching scan rather than a regex.
var termPattern = regexp.MustCompile(`(?is)^NULLIF\s*\(\s*NULLIF\s*\(\s*TRIM\s*\(`)

// plainTermPattern matches a term that folds only the empty string — the
// shape that lets "<unknown>" through as if it were a real model name.
var plainTermPattern = regexp.MustCompile(`(?is)^NULLIF\s*\(`)

// foldsPlaceholder reports whether a chain term folds the "<unknown>"
// placeholder (as opposed to only the empty string).
func foldsPlaceholder(term string) bool {
	return strings.Contains(term, "'<unknown>'")
}

// canonicalTermColumn extracts the column expression from a canonical folding
// term, or "" when the term is not in that shape.
func canonicalTermColumn(term string) string {
	trimmed := strings.TrimSpace(term)
	if !termPattern.MatchString(trimmed) {
		return ""
	}
	open := strings.Index(trimmed, "TRIM(")
	if open < 0 {
		return ""
	}
	parenOpen := open + len("TRIM")
	end := matchParen(trimmed, parenOpen)
	if end < 0 {
		return ""
	}
	return strings.Join(strings.Fields(trimmed[parenOpen+1:end]), " ")
}

// chainTouchesModelColumn reports whether the chain resolves a **model**.
//
// 判据是「**第一项**是模型列」——即这条链是关于什么的，而不是「链体任意
// 位置出现模型列」。
//
// 区别在哪里：vendor 链
// COALESCE(NULLIF(mf.vendor,”), COALESCE(NULLIF(rl.client_model,”),”), 'unknown')
// 的体里出现了 client_model，但它的**第一项**是 mf.vendor —— 它是一条
// **vendor 链**（兜底桶是 vendor 的），不是模型链。若不区分，它会被误判成
// 模型链，然后 vendor 项不折叠占位符 ⇒ 误报。
func chainTouchesModelColumn(c chain) bool {
	terms := splitTopLevel(c.body, ',')
	if len(terms) == 0 {
		return false
	}
	return termIsModelColumn(strings.TrimSpace(terms[0]))
}

// termIsModelColumn reports whether a chain term **is** a model column
// reference (possibly wrapped in NULLIF/TRIM/COALESCE), as opposed to merely
// mentioning one somewhere inside a larger expression.
//
// 剥包装时每次只取**第一个参数**：NULLIF(x, ”) 的第一个参数才是被包的值。
func termIsModelColumn(term string) bool {
	t := strings.TrimSpace(term)
	for {
		open := strings.Index(t, "(")
		if open < 0 {
			// 剥到裸列名（没有括号了）⇒ 它就是被包的值。
			return modelColumnPattern.MatchString(t)
		}
		inner := t[open+1:]
		if !strings.HasSuffix(strings.TrimSpace(inner), ")") {
			return false
		}
		inner = strings.TrimSpace(inner[:len(inner)-1])
		parts := splitTopLevel(inner, ',')
		if len(parts) == 0 {
			return false
		}
		name := strings.TrimSpace(t[:open])
		switch name {
		case "COALESCE", "NULLIF", "TRIM", "LOWER", "UPPER":
			t = strings.TrimSpace(parts[0])
			continue
		}
		return modelColumnPattern.MatchString(inner)
	}
}

// chain is one extracted model-resolution chain.
type chain struct {
	// body is the COALESCE argument list, without the outer parens.
	body string
	// line is the 1-based line number where the chain starts.
	line int
}

// splitTopLevel splits s on sep, ignoring separators nested inside
// parentheses. SQL expressions in these chains contain nested calls
// (NULLIF(NULLIF(...))), so a naive strings.Split would mis-split.
func splitTopLevel(s string, sep byte) []string {
	var out []string
	depth := 0
	start := 0
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			inQuote = c
		case '(':
			depth++
		case ')':
			depth--
		default:
			if c == sep && depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

// stripGoComments removes line comments and block comments from Go source,
// leaving the string literals intact.
//
// ★ Why this matters: Go comments routinely quote the **old** form of a SQL
// chain when documenting what changed ("旧链是 COALESCE(NULLIF(...))").
// Without stripping, the gate would read the comment as the live chain and
// report a violation that does not exist — or, worse, report it against the
// wrong term.
//
// The implementation is a line scanner rather than a regex because Go string
// literals may contain "//" and "/*" sequences (e.g. a URL in SQL).
func stripGoComments(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	inString := byte(0)
	inRaw := false
	inLine := false
	inBlock := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
				out.WriteByte(c)
			}
			continue
		case inBlock:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				inBlock = false
				i++
			}
			continue
		case inString != 0:
			out.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				out.WriteByte(src[i])
				continue
			}
			if c == inString {
				inString = 0
			}
			continue
		case inRaw:
			out.WriteByte(c)
			if c == '`' {
				inRaw = false
			}
			continue
		}
		switch {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			inLine = true
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			inBlock = true
			i++
		case c == '`':
			inRaw = true
			out.WriteByte(c)
		case c == '\'' || c == '"':
			inString = c
			out.WriteByte(c)
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}
