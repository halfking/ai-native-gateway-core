package admin

// §143.37 / P20 的门：**看板概览计数的日志回退必须被门控**，不能无条件执行。
//
// 背景（runbook §143.37）：
// ① §143.36 已证：P12 那条语句是**看板降级读面**，不是「请求热路径」；
//    它的根因是「同一份派生表被扫两遍」，改单遍后 buffers 50.4%、exec −87.7%。
// ② ★★★★ **但没人问「它为什么会被调用」**。本轮顺着这条问下去，发现了
//    `queryOverviewCounts` 里的**无条件 fall-through**：
//
//    	if err := h.queryOverviewCountsMinute(...); err != nil {
//    	    if !IsMissingRelationError(err) { return err }
//    	}
//    	return h.fillOverviewCountsFromLogs(...)   // ★★ 落在 if 块【外面】
//
//    ⇒ **分钟路径成功时也照跑日志回退**。来历可查：`272e1bf93` 引入分钟路径时
//    把收尾的 `return` 留在了 `if` 之外（意图显然是「分钟路径当主、缺失时回退」）。
// ③ ★★★★ 而 `fillOverviewCountsFromLogs` 只在计数为 0 时才采纳它的结果：
//
//    	if *models == 0 && logModels > 0 { *models = logModels }
//    	if *providers == 0 && logProviders > 0 { *providers = logProviders }
//
//    ⇒ 分钟路径给出**两个非零**计数时，那一整扫的结果**被逐字丢弃**
//    ⇒ **扫了、付了钱、什么也没换到**。
// ④ ★★ 生产实测（§143.37.3，只读）：分钟路径在 1/7/30 天 × 平台/租户
//    **6 个组合里全部给出两个非零计数**（模型 79/165/488、供应商 8/10/19）
//    ⇒ 6/6 组合下这一扫都是纯浪费；而它 2 天跑了 **412 次**、读了
//    **21.8 GB** 物理读、占了 **1,108.7 s** 的库时间。
//
// # 为什么必须钉门
//
// ① ★★★★★ 这条门挡的是**性能无声退化**：把门控删掉**不会报错**、
//    载荷值也**一模一样**（因为结果本来就被丢弃）⇒ 没有门的话，
//    一次「顺手简化一下控制流」的改动就能把 21.8 GB / 2 天放回去。
// ② ★★★★ 门必须同时钉住**门控的前提**：`fillOverviewCountsFromLogs`
//    只在计数为 0 时采纳结果 —— 这是「跳过是零语义风险」的**唯一依据**。
//    ⇒ 一旦有人把它改成「总是覆盖」，前提消失、门必须红。
// ③ ★★★ 门必须钉住**错误面**：改前在「两计数非零」时可能把日志查询的错误
//    返回给调用方，改后返回 nil ⇒ 之所以可判定为「调用点不可观测」，
//    是因为两个调用点都用 `_ =` 丢弃了它 ⇒ 这条必须钉住。
// ④ ★★★ §143.36.8 的待办「其他包未扫」在 §143.37 闭合 ⇒ 门要把它钉住：
//    **产品代码里不得再出现「同一份重型源在同一 SQL 模板里被引用 ≥2 次」**。
//
// # 量具纪律（本轮又一次踩到子串计数坑）
//
// ★★ 普查的第一版（§143.37.2）报「9 处命中」，其中 3 处是**子串误计**：
//   `FROM public.session_turns` 是 `FROM public.session_turns_hot` 的【子串】
//   ⇒ 朴素 count 把 hot 腿也计进 parent 腿 ⇒ 假命中。
//   ⇒ 修正口径：**parent 计数 = count(parent) − count(parent_hot)**（同理 _request_logs）。
//   ⇒ 修正后：命中 6 处，**全部在同一个测试文件里**，**产品代码 0 处**。
//   ⇒ ★ 与 §143.36.8 的「`AS r` 计数 50」是**同一族坑**（子串计数）。
//
// ★ 本门自身含这些字面量 ⇒ 扫描时必须**排除自己**（§143.36 已踩过）。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	boardQueriesSrc14337  = "dashboard_board_queries.go"
	boardFallbackSrc14337 = "dashboard_board_fallback.go"
	boardGateSelf14337    = "section_14337_overview_counts_gate_test.go"
)

// ─────────────────────────────────────────────────────────────────────────
// T1：判定函数的行为表（★ 本门的核心判据 —— 真代码、真输入、真输出）
// ─────────────────────────────────────────────────────────────────────────

// TestSection14337NeedsLogFallbackDecisionTable 钉住 `overviewCountsNeedsLogFallback`
// 的**全部**输入分支。
//
// ★ 为什么用行为表而不是文本判据：本门要挡的失败模式是
//
//	「门控被删掉 / 判据写反 / 只判一个计数」——
//	这三种失败在源码文本上都能长得「很像有门控」，
//	只有**真的调用判定函数**才能区分。
func TestSection14337NeedsLogFallbackDecisionTable(t *testing.T) {
	missing := &pgconn.PgError{
		Code:    "42P01",
		Message: `relation "request_stats_dim_minute" does not exist`,
	}
	timeout := &pgconn.PgError{
		Code:    "57014",
		Message: "canceling statement due to statement timeout",
	}
	other := &pgconn.PgError{Code: "23505", Message: "duplicate key value"}
	plain := errors.New("boom")

	cases := []struct {
		name             string
		err              error
		models           int
		providers        int
		wantNeedsLogPath bool
		why              string
	}{
		{
			name: "分钟路径成功且两个计数都非零 ⇒ 不回退",
			err:  nil, models: 5, providers: 3,
			wantNeedsLogPath: false,
			why:              "回退的结果在这两个计数非零时会被【逐字丢弃】⇒ 那一扫是纯浪费",
		},
		{
			name: "分钟路径成功、模型数为 0 ⇒ 回退（要补 0）",
			err:  nil, models: 0, providers: 3,
			wantNeedsLogPath: true,
			why:              "模型数为 0 时日志回退的结果会被采纳",
		},
		{
			name: "分钟路径成功、供应商数为 0 ⇒ 回退（要补 0）",
			err:  nil, models: 5, providers: 0,
			wantNeedsLogPath: true,
			why:              "供应商数为 0 时日志回退的结果会被采纳",
		},
		{
			name: "分钟路径成功、两个计数都是 0 ⇒ 回退",
			err:  nil, models: 0, providers: 0,
			wantNeedsLogPath: true,
			why:              "聚合表可能没装或没数据 ⇒ 回退是唯一的信息来源",
		},
		{
			name: "关系缺失 42P01 ⇒ 回退（这是回退存在的理由）",
			err:  missing, models: 0, providers: 0,
			wantNeedsLogPath: true,
			why:              "request_stats_dim_minute 不存在 ⇒ 只能从明细表算",
		},
		{
			name: "超时 57014 ⇒ 不回退（错误直接上抛）",
			err:  timeout, models: 0, providers: 0,
			wantNeedsLogPath: false,
			why:              "非 42P01 的错误由调用方直接返回，与改前一致",
		},
		{
			name: "其它 PG 错误 ⇒ 不回退",
			err:  other, models: 0, providers: 0,
			wantNeedsLogPath: false,
			why:              "非 42P01 的错误由调用方直接返回",
		},
		{
			name: "非 PG 错误 ⇒ 不回退",
			err:  plain, models: 0, providers: 0,
			wantNeedsLogPath: false,
			why:              "非 PG 错误不可能是 42P01",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := overviewCountsNeedsLogFallback(tc.err, tc.models, tc.providers)
			if got != tc.wantNeedsLogPath {
				t.Errorf("overviewCountsNeedsLogFallback(%v, %d, %d) = %v，期望 %v\n  理由：%s",
					tc.err, tc.models, tc.providers, got, tc.wantNeedsLogPath, tc.why)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────
// T2：钉住「门控的前提」—— 回退只在计数为 0 时采纳结果
// ─────────────────────────────────────────────────────────────────────────

// assignModelsRe14337 / assignProvidersRe14337 匹配**赋值**语句。
//
// ★★ 坑：`\*models\s*=` 也会命中 `*models == 0`（guard 那一行）
//
//	⇒ 必须排除后面紧跟第二个 `=` 的形态 ⇒ 用 `[^=]` 收尾。
var (
	assignModelsRe14337    = regexp.MustCompile(`\*models\s*=\s*[^=]`)
	assignProvidersRe14337 = regexp.MustCompile(`\*providers\s*=\s*[^=]`)
)

// TestSection14337LogFallbackOnlyFillsZeroCounts 钉住**门控的前提**：
// `fillOverviewCountsFromLogs` 只在对应计数为 0 时才写回。
//
// ★★ 这是「跳过日志回退是【零语义风险】」的**唯一依据**：
//
//	两个计数都非零 ⇒ 两个 if 都不成立 ⇒ 回退的结果**没有任何写回**
//	⇒ 跳过它与执行它**逐值等价**。
//
// ⇒ 一旦有人把它改成「总是覆盖」，本门红 ⇒ 门控的前提消失，必须重新评估。
func TestSection14337LogFallbackOnlyFillsZeroCounts(t *testing.T) {
	src, err := os.ReadFile(boardFallbackSrc14337)
	if err != nil {
		t.Fatalf("read %s: %v", boardFallbackSrc14337, err)
	}
	// ★ 剥注释后再判（判据不得被注释满足 —— 仓内已有 10+ 处同款实现）
	body := goFuncBodyStrict(stripGoCommentsKeepingStrings(string(src)),
		"func (h *Handler) fillOverviewCountsFromLogs(")
	if body == "" {
		t.Fatalf("fillOverviewCountsFromLogs not found in %s", boardFallbackSrc14337)
	}

	// 逐行跟踪「最近一条 if 条件」，赋值必须落在对应的 == 0 保护下。
	var lastGuard string
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "if ") && strings.HasSuffix(line, "{") {
			lastGuard = line
		}
		if m := assignModelsRe14337.FindString(line); m != "" {
			if !strings.Contains(lastGuard, "*models == 0") {
				t.Errorf("fillOverviewCountsFromLogs 里 %q 这一处写入**不在** `*models == 0` 保护下（最近的 if 是 %q）\n"+
					"  ⇒ 门控的前提被破坏：两计数非零时回退结果不再被丢弃 ⇒\n"+
					"    「跳过日志回退」不再是零语义风险，必须重新评估 §143.37 的改法。",
					m, lastGuard)
			}
		}
		if m := assignProvidersRe14337.FindString(line); m != "" {
			if !strings.Contains(lastGuard, "*providers == 0") {
				t.Errorf("fillOverviewCountsFromLogs 里 %q 这一处写入**不在** `*providers == 0` 保护下（最近的 if 是 %q）\n"+
					"  ⇒ 门控的前提被破坏（详见同条门里 models 的说明）。",
					m, lastGuard)
			}
		}
	}

	// 两条 guard 必须都还在（防止有人把其中一个 if 删掉）
	for _, needle := range []string{"*models == 0", "*providers == 0"} {
		if !strings.Contains(body, needle) {
			t.Errorf("fillOverviewCountsFromLogs 里找不到 %q ⇒ 回退的「只补 0」语义被改动", needle)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────
// T3：钉住调用点真的用了判定函数（门控没被绕过）
// ─────────────────────────────────────────────────────────────────────────

// TestSection14337QueryOverviewCountsIsGated 钉住 `queryOverviewCounts`：
//
//	① 必须调用判定函数 `overviewCountsNeedsLogFallback(`；
//	② 判定必须在日志回退**之前**（否则等于没门控）。
//
// ★ 判据口径：不能只判「函数体里出现过判定函数的名字」——
//
//	那能被注释满足、也能被死代码满足 ⇒ 必须判**顺序**：
//	判定出现在 `return h.fillOverviewCountsFromLogs(` 之前。
func TestSection14337QueryOverviewCountsIsGated(t *testing.T) {
	src, err := os.ReadFile(boardQueriesSrc14337)
	if err != nil {
		t.Fatalf("read %s: %v", boardQueriesSrc14337, err)
	}
	body := goFuncBodyStrict(stripGoCommentsKeepingStrings(string(src)),
		"func (h *Handler) queryOverviewCounts(")
	if body == "" {
		t.Fatalf("queryOverviewCounts not found in %s", boardQueriesSrc14337)
	}

	guardIdx := strings.Index(body, "overviewCountsNeedsLogFallback(")
	if guardIdx < 0 {
		t.Errorf("queryOverviewCounts 没有调用判定函数 overviewCountsNeedsLogFallback( ⇒ "+
			"日志回退又变回无条件执行（§143.37.2 的 272e1bf93 形态）⇒\n"+
			"  2 天 412 次 / 21.8 GB 物理读的纯浪费会回来。\n  函数体:\n%s", body)
		return
	}
	fallbackIdx := strings.Index(body, "return h.fillOverviewCountsFromLogs(")
	if fallbackIdx < 0 {
		t.Errorf("queryOverviewCounts 里找不到日志回退调用 ⇒ 概览计数没有任何来源")
		return
	}
	if guardIdx > fallbackIdx {
		t.Errorf("判定函数出现在日志回退**之后**（guard@%d > fallback@%d）⇒ 门控形同虚设。\n  函数体:\n%s",
			guardIdx, fallbackIdx, body)
	}
}

// ─────────────────────────────────────────────────────────────────────────
// T4：钉住「错误面」—— 两个调用点都必须丢弃 queryOverviewCounts 的错误
// ─────────────────────────────────────────────────────────────────────────

// TestSection14337CallSitesDiscardOverviewCountsError 钉住**诚实边界**。
//
// §143.37 的改法在「两计数都非零」时把返回值从「可能是日志查询的错误」
// 变成 nil ⇒ 这不是「零风险」，是「**调用点不可观测**」。
// ⇒ 之所以可判定为不可观测，是因为两个调用点都用 `_ =` 丢弃了它。
// ⇒ 一旦有人开始使用这个错误，必须重新评估门控（那时错误面就有意义了）。
func TestSection14337CallSitesDiscardOverviewCountsError(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read admin dir: %v", err)
	}
	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || e.Name() == boardGateSelf14337 {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, "h.queryOverviewCounts(") {
				continue
			}
			found++
			if !strings.Contains(line, "_ =") {
				t.Errorf("admin/%s:%d 的 queryOverviewCounts 调用没有丢弃错误：%q\n"+
					"  ⇒ §143.37 的门控在「两计数非零」时会把错误面从「可能返回错误」改成 nil，\n"+
					"    这个差别之所以不可观测，靠的就是调用点丢弃错误 ⇒ 这里一旦开始用错误，必须重评。",
					e.Name(), i+1, strings.TrimSpace(line))
			}
		}
	}
	if found == 0 {
		t.Errorf("没有找到任何 queryOverviewCounts 的调用点 ⇒ 判据失效（函数被改名/移走了？）")
	}
}

// ─────────────────────────────────────────────────────────────────────────
// T5：§143.36.8 待办闭合 —— 产品代码里不得再有「同一份重型源被引用 ≥2 次」
// ─────────────────────────────────────────────────────────────────────────

// heavySourceToken14337 描述一个「重型源占位符」与它需要扣除的更长形态。
//
// ★★ 子串坑：`FROM public.session_turns` 是 `FROM public.session_turns_hot`
//
//	的【子串】⇒ 朴素 count 会把 hot 腿计进 parent 腿 ⇒ **假命中**。
//	⇒ 口径：**parent 计数 = count(parent) − count(parent_hot)**。
//	⇒ 与 §143.36.8 的「`AS r` 计数 50」是同一族坑。
type heavySourceToken14337 struct {
	token  string
	longer string
}

var heavySourceTokens14337 = []heavySourceToken14337{
	{"FROM %s", ""},
	{"FROM public.session_turns_hot", ""},
	{"FROM public.session_turns", "FROM public.session_turns_hot"},
	{"FROM public._request_logs_hot", ""},
	{"FROM public._request_logs", "FROM public._request_logs_hot"},
	{"FROM request_logs_with_current_month", ""},
	{"SessionFamilyTurnsSourceSQL()", ""},
	{"SessionFamilyTurnsHotSourceSQL()", ""},
	{"SessionFamilyBodiesSourceSQL()", ""},
	{"boardRequestLogsFromClause(", ""},
	{"requestLogsFromClause(", ""},
}

// TestSection14337NoHeavySourceDoubleScanInProductCode 闭合 §143.36.8 的待办
// 「其他包未扫」。
//
// ★ 判据口径（与 §143.37.2 的普查脚本逐字同源）：
//
//	在**同一个 SQL 模板**内，某个重型源占位符被引用 ≥ 2 次。
//	⚠ 不能按【文件】计数（一个文件里多个独立查询会被算在一起 ⇒ 恒红）。
//	⚠ 不能数「FROM」/「SELECT」关键词（P12 改前改后数字巧合接近）。
//
// ★ 排除项只有三类：`_test.go` 文件、`tests/` 目录下的工具、门自己。
//
//	⇒ 当前实测：**产品代码命中 0 处**（6 处命中全在
//	  admin/columnar_surface_servable_realdb_test.go 一个测试文件里）。
func TestSection14337NoHeavySourceDoubleScanInProductCode(t *testing.T) {
	skipDirs := map[string]bool{
		".git": true, "vendor": true, "node_modules": true,
		"testdata": true, "embeddata": true,
	}
	var hits []string

	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if skipDirs[base] || (strings.HasPrefix(base, ".") && base != ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel := filepath.ToSlash(path)
		if filepath.Base(rel) == boardGateSelf14337 {
			return nil // ★ 门自己含这些字面量
		}
		if strings.HasSuffix(rel, "_test.go") || strings.Contains(rel, "/tests/") {
			return nil // 测试与工具不是产品读面
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		src := stripGoCommentsKeepingStrings(string(raw))
		for idx, tmpl := range sqlTemplates14336(src) {
			for _, tok := range heavySourceTokens14337 {
				n := strings.Count(tmpl, tok.token)
				if tok.longer != "" {
					n -= strings.Count(tmpl, tok.longer)
				}
				if n >= 2 {
					hits = append(hits, sprintf14337("%s 模板#%d: %q 被引用 %d 次", rel, idx, tok.token, n))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}

	if len(hits) > 0 {
		t.Errorf("产品代码里出现「同一份重型源在同一 SQL 模板里被引用 ≥2 次」%d 处：\n  %s\n"+
			"  ⇒ 这正是 P12 的失败形态（同一份派生表被独立扫描 N 遍 ⇒ 物理读 ×N）。\n"+
			"  ⇒ 修法方向是「合并成单遍」，不是「裁剪投影」（裁剪投影实测零收益）。",
			len(hits), strings.Join(hits, "\n  "))
	}
}

// sprintf14337 是 fmt.Sprintf 的薄包装（让本门只在一个地方依赖 fmt）。
func sprintf14337(format string, a ...any) string {
	return fmt.Sprintf(format, a...)
}

// ─────────────────────────────────────────────────────────────────────────
// T6：runbook §143.37 在场
// ─────────────────────────────────────────────────────────────────────────

func TestSection14337RunbookHasSection(t *testing.T) {
	runbook := "../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	b, err := os.ReadFile(runbook)
	if err != nil {
		t.Fatalf("read runbook: %v", err)
	}
	body := string(b)
	for _, needle := range []string{
		"§143.37",
		"overviewCountsNeedsLogFallback",
		"272e1bf93",
		"412",
		"21.8 GB",
		"无条件",
		"子串",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("runbook 缺 %q", needle)
		}
	}
}
