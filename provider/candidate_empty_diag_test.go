// provider/candidate_empty_diag_test.go —— 「候选为空」归因的**判据**测试（2026-11-12）
//
// ★ 这道测试钉的是**归因结论**（Summarize 的输出），不是 SQL 本身：
// ★ SQL 只能在真库上验（realdb 腿），而**结论分类**是纯函数 ⇒ 可以且应该被机器执行。
//
// ★ 为什么值得钉：★ 归因的**价值**全在「结论是否指向正确的那道闸」——
// ★ 一旦把「模型没登记」和「被状态闸滤空」搞混，运营就会朝错的方向查半天。
package provider

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestEmptyCandidateBreakdownSummarize(t *testing.T) {
	cases := []struct {
		name    string
		in      EmptyCandidateBreakdown
		must    []string
		mustNot []string
	}{
		{
			name: "模型未登记 ⇒ 结论指向登记而不是状态",
			in:   EmptyCandidateBreakdown{},
			must: []string{"模型未登记", "登记模型"},
			// ★ 关键：不能说成「被状态闸滤掉」⇒ 运维会去查凭据，方向错。
			mustNot: []string{"credentials.status", "lifecycle_status"},
		},
		{
			name: "遗留 v1 status 闸 ⇒ 结论必须点名 credentials.status（211 真机实测形态）",
			in: EmptyCandidateBreakdown{
				OffersTotal: 3, BlockedByCredentialStatus: 2,
			},
			must:    []string{"模型已登记", "credentials.status", "3 offer", "2 条"},
			mustNot: []string{"模型未登记"},
		},
		{
			name: "生命周期闸 ⇒ 不能与遗留 status 闸混说",
			in: EmptyCandidateBreakdown{
				OffersTotal: 1, BlockedByCredentialLifecycle: 1,
			},
			must:    []string{"lifecycle_status"},
			mustNot: []string{"credentials.status"},
		},
		{
			name: "凭据缺失 ⇒ 结论指向改绑",
			in: EmptyCandidateBreakdown{
				OffersTotal: 2, BlockedByCredentialMissing: 1,
			},
			must:    []string{"缺失", "改绑"},
			mustNot: []string{"credentials.status"},
		},
		{
			name: "配额闸",
			in: EmptyCandidateBreakdown{
				OffersTotal: 1, BlockedByCredentialQuota: 1,
			},
			must: []string{"配额"},
		},
		{
			name: "供应商停用闸",
			in: EmptyCandidateBreakdown{
				OffersTotal: 4, BlockedByProviderDisabled: 4,
			},
			must: []string{"供应商"},
		},
		{
			name: "offer 人工下线闸",
			in: EmptyCandidateBreakdown{
				OffersTotal: 1, BlockedByUnavailableReason: 1,
			},
			must: []string{"unavailable_reason"},
		},
		{
			name: "★ 状态闸全开 ⇒ 必须明说「状态不是原因」（免得运维继续朝状态查）",
			in: EmptyCandidateBreakdown{
				OffersTotal: 5,
			},
			must:    []string{"状态不是原因"},
			mustNot: []string{"credentials.status", "lifecycle_status", "模型未登记"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.Summarize()
			for _, m := range tc.must {
				if !strings.Contains(got, m) {
					t.Errorf("归因结论缺少 %q\n实际：%s", m, got)
				}
			}
			for _, m := range tc.mustNot {
				if strings.Contains(got, m) {
					t.Errorf("归因结论不应出现 %q（会把运维引到错误的闸上）\n实际：%s", m, got)
				}
			}
		})
	}
}

// TestEmptyCandidateBreakdownSQLMatchesGatePredicates —— ★ 口径钉：
// 归因 SQL 里出现的**状态闸关键字**必须与候选 SQL 用的是同一批，
// 否则会出现「诊断说被某闸滤掉、实际闸在别处」的假归因。
//
// ★ 这里钉的是**判据面**（关键字集合），不是逐字 SQL——★ 逐字比对会被无意义的
// ★ 空格/换行改动打碎；★ 关键字集合才是「口径没分叉」的真正不变量。
func TestEmptyCandidateBreakdownSQLMatchesGatePredicates(t *testing.T) {
	mustHave := []string{
		"credentials",        // 闸的宿主表
		"lifecycle_status",   // 生命周期闸
		"quota_state",        // 配额闸
		"status",             // 遗留 v1 健康闸
		"manual_disabled",    // 人工停用闸
		"providers",          // 供应商闸
		"base_url",           // 供应商闸（空 base_url）
		"protocol",           // 供应商闸（脏 protocol）
		"unavailable_reason", // offer 人工下线闸
		"model_aliases",      // ★ 4 路模型匹配矩阵（别名）
		"models_canonical",   // ★ 4 路模型匹配矩阵（canonical）
		"canonical_raw_name", // ★ 4 路模型匹配矩阵（offer 侧）
		"standardized_name",  // ★ 4 路模型匹配矩阵（offer 侧）
	}
	for _, k := range mustHave {
		if !strings.Contains(emptyCandidateBreakdownSQL, k) {
			t.Errorf("归因 SQL 缺少关键字 %q ⇒ 诊断口径与候选闸集合分叉了", k)
		}
	}
	// ★ 「模型未登记」与「被闸滤空」必须能被区分 ⇒ 归因 SQL 必须同时给出
	// ★ 总数与各闸计数（不是只给闸计数）。
	for _, k := range []string{"COUNT(*)::int AS offers_total", "FILTER (WHERE"} {
		if !strings.Contains(emptyCandidateBreakdownSQL, k) {
			t.Errorf("归因 SQL 缺少 %q ⇒ 无法区分「模型没登记」与「被闸滤空」", k)
		}
	}
	// ★ $2 必须是租户参数（2026-11-12 批判审计 P2：候选 SQL 限定
	// ★ p.tenant_id=$2 而诊断无租户谓词 ⇒ 计数跨租户；补齐后由本断言钉住）。
	if !strings.Contains(emptyCandidateBreakdownSQL, "$2") {
		t.Errorf("归因 SQL 缺少租户参数 $2 ⇒ 与候选 SQL 的租户闸分叉")
	}
}

// TestEmptyCandidateBreakdownScanOrderPinned —— §695 列序钉（2026-11-12）：
// 「SQL 终端 SELECT 列清单 ↔ Scan 目标清单」是平行数组，必须有机器序钉。
// 关键词包含判据（上面 mustHave）会被「列了但没用」骗过（§682：断言落在
// 标签词上）——本门把**末段 SELECT 的 AS 别名序**与
// EmptyCandidateBreakdown 的**字段声明序**（= Scan 传参序的规范来源）逐位
// 对齐：加列不改 Scan / 改 Scan 不加列 / 调列序，任一漂移当场红。
func TestEmptyCandidateBreakdownScanOrderPinned(t *testing.T) {
	// 取最后一段 SELECT（终端投影段）到 FROM matched 之间。
	tail := emptyCandidateBreakdownSQL[strings.LastIndex(emptyCandidateBreakdownSQL, "\nSELECT"):]
	end := strings.Index(tail, "FROM matched")
	if end < 0 {
		t.Fatalf("归因 SQL 缺少终端 FROM matched 段")
	}
	tail = tail[:end]
	re := regexp.MustCompile(`AS (\w+)`)
	matches := re.FindAllStringSubmatch(tail, -1)
	if len(matches) == 0 {
		t.Fatalf("终端 SELECT 段未解析出任何 AS 别名")
	}
	// snake_case → Go 导出字段名（与 Scan 目标的字段一一对应）。
	var got []string
	for _, m := range matches {
		parts := strings.Split(m[1], "_")
		for i, p := range parts {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
		got = append(got, strings.Join(parts, ""))
	}
	// Scan 目标序（反射解引用 emptyCandidateScanTargets 的指针切片）——
	// **Scan 传参序的机器可读规范来源**（结构体声明序 ≠ Scan 序：SQL 把
	// credential_missing 放第 2 位而结构体把它放末位）。
	b := &EmptyCandidateBreakdown{}
	typ := reflect.TypeOf(EmptyCandidateBreakdown{})
	targets := emptyCandidateScanTargets(b)
	// Scan 目标是 &b.Field 指针 ⇒ 按指针值对回结构体字段名（机器可读）。
	structFieldByPtr := map[uintptr]string{}
	for i := 0; i < typ.NumField(); i++ {
		structFieldByPtr[reflect.ValueOf(b).Elem().Field(i).Addr().Pointer()] = typ.Field(i).Name
	}
	var want []string
	for _, tgt := range targets {
		name, ok := structFieldByPtr[reflect.ValueOf(tgt).Pointer()]
		if !ok {
			t.Fatalf("Scan 目标不指向结构体字段（类型 %T）", tgt)
		}
		want = append(want, name)
	}
	if len(got) != len(want) {
		t.Fatalf("列数漂移：SQL 终端 SELECT %d 列 vs Scan %d 目标\nSQL=%v\nScan=%v",
			len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("列序漂移 @%d：SQL %q vs Scan %q（平行数组必须同序同位）",
				i, got[i], want[i])
		}
	}
}

// TestEmptyCandidateBreakdownMultiGateRanksByCount —— 多闸同时命中时，
// 归因头条必须是「挡住最多 offer 的那道闸」（2026-11-22 211 事故）。
//
// ★ 事故形态（claude-sonnet-5-5 全量 503）：11 条 offer 里凭据配额态挡住 8 条
// （上游 INSUFFICIENT_BALANCE）、供应商停用 5 条，而 credentials.manual_disabled
// 只沾到 1 条——旧实现按固定顺序报头条，日志写成「1 条被人工停用闸滤掉 ⇒
// 解除人工停用标记」，运维照着去解一个不相干的标记，模型照样 503。
func TestEmptyCandidateBreakdownMultiGateRanksByCount(t *testing.T) {
	in := EmptyCandidateBreakdown{
		OffersTotal: 11,
		// 211 实测：[candidate_diag] db_empty 的四个计数。
		BlockedByCredentialManualDisabled: 1,
		BlockedByCredentialQuota:          8,
		BlockedByProviderDisabled:         5,
		BlockedByUnavailableReason:        2,
	}
	if got := in.CauseClass(); got != CauseClassCredentialQuota {
		t.Fatalf("多闸归因类=%q，期望主因 %q（命中 8 条的配额闸）", got, CauseClassCredentialQuota)
	}
	got := in.Summarize()
	for _, m := range []string{"11 offer", "凭据配额态 8 条", "供应商停用 5 条", "unavailable_reason(manual*) 下线 2 条", "credentials.manual_disabled 闸 1 条", "主因=凭据配额态", "处置：处置配额（充值/解封）"} {
		if !strings.Contains(got, m) {
			t.Errorf("多闸归因缺少 %q\n实际：%s", m, got)
		}
	}
	// 处置方向必须是主因的，不能是人工停用的。
	if strings.Contains(got, "解除该凭据的人工停用标记") {
		t.Errorf("多闸归因把处置指向了小计数闸：%s", got)
	}
	// 列表按命中数降序。
	order := []string{"凭据配额态 8 条", "供应商停用 5 条", "unavailable_reason(manual*) 下线 2 条", "credentials.manual_disabled 闸 1 条"}
	last := -1
	for _, m := range order {
		at := strings.Index(got, m)
		if at < 0 || at < last {
			t.Fatalf("多闸归因未按命中数降序排列 %q：%s", m, got)
		}
		last = at
	}
}

// TestEmptyCandidateBreakdownMultiGateTieKeepsPriority —— 命中数打平时沿用
// 既有优先序（结构性 / 运营开关在前），不得随 map / 结构体字段顺序漂移。
func TestEmptyCandidateBreakdownMultiGateTieKeepsPriority(t *testing.T) {
	in := EmptyCandidateBreakdown{OffersTotal: 4, BlockedByCredentialManualDisabled: 2, BlockedByCredentialQuota: 2}
	if got := in.CauseClass(); got != CauseClassCredentialManualDisabled {
		t.Fatalf("打平时归因类=%q，期望按优先序取 %q", got, CauseClassCredentialManualDisabled)
	}
	got := in.Summarize()
	if !strings.Contains(got, "主因=credentials.manual_disabled 闸") {
		t.Fatalf("打平时主因应按优先序取人工停用闸：%s", got)
	}
}

// TestEmptyCandidateBreakdownMultiGateJitterKeepsClass —— 排名不变时，计数
// 抖动不改变归因类（归因类进日志冷却闸的键，不能跟着计数一起抖）。
func TestEmptyCandidateBreakdownMultiGateJitterKeepsClass(t *testing.T) {
	a := EmptyCandidateBreakdown{OffersTotal: 11, BlockedByCredentialManualDisabled: 1, BlockedByCredentialQuota: 8}
	b := EmptyCandidateBreakdown{OffersTotal: 12, BlockedByCredentialManualDisabled: 2, BlockedByCredentialQuota: 9}
	if a.CauseClass() != b.CauseClass() {
		t.Fatalf("排名不变的计数抖动改变了归因类：%q vs %q", a.CauseClass(), b.CauseClass())
	}
	if a.CauseClass() != CauseClassCredentialQuota {
		t.Fatalf("归因类=%q，期望主因 %q（配额闸命中数远多于人工停用闸）", a.CauseClass(), CauseClassCredentialQuota)
	}
}

// TestEmptyCandidateBreakdownSingleGateSentenceUnchanged —— 单闸独占时的整句
// **逐字不变**（多闸改造不得动到既有单闸文案：下游有按句子对账的运维习惯）。
func TestEmptyCandidateBreakdownSingleGateSentenceUnchanged(t *testing.T) {
	cases := []struct {
		in   EmptyCandidateBreakdown
		want string
	}{
		{EmptyCandidateBreakdown{OffersTotal: 1, BlockedByCredentialQuota: 1}, "模型已登记（1 offer），但 1 条被凭据配额态滤掉 ⇒ 处置：处置配额（充值/解封）"},
		{EmptyCandidateBreakdown{OffersTotal: 2, BlockedByCredentialManualDisabled: 1}, "模型已登记（2 offer），但 1 条被 credentials.manual_disabled 闸滤掉 ⇒ 处置：解除该凭据的人工停用标记"},
		{EmptyCandidateBreakdown{OffersTotal: 5}, "模型已登记（5 offer）且状态闸全开 ⇒ 状态不是原因 ⇒ 继续查租户/档位/模态/客户端档匹配"},
	}
	for _, tc := range cases {
		if got := tc.in.Summarize(); got != tc.want {
			t.Errorf("单闸句子漂移\n got: %s\nwant: %s", got, tc.want)
		}
	}
}

// TestCauseClassAgreesWithSummarizeMultiGate —— 多闸场景下分类与摘要的
// 主因也必须是同一道闸（TestCauseClassAgreesWithSummarize 只覆盖了单闸）。
func TestCauseClassAgreesWithSummarizeMultiGate(t *testing.T) {
	breakdowns := []EmptyCandidateBreakdown{
		{OffersTotal: 11, BlockedByCredentialManualDisabled: 1, BlockedByCredentialQuota: 8, BlockedByProviderDisabled: 5},
		{OffersTotal: 6, BlockedByCredentialStatus: 1, BlockedByProviderDisabled: 4, BlockedByUnavailableReason: 4},
		{OffersTotal: 9, BlockedByCredentialMissing: 3, BlockedByCredentialLifecycle: 3, BlockedByCredentialQuota: 3},
		{OffersTotal: 3, BlockedByProviderTenant: 1, BlockedByProviderDisabled: 2},
	}
	for _, b := range breakdowns {
		g := b.rankedGates()[0]
		if g.class != b.CauseClass() {
			t.Errorf("分类 %q 与摘要主因 %q 不是同一道闸", b.CauseClass(), g.class)
		}
		if s := b.Summarize(); !strings.Contains(s, "主因="+g.label) {
			t.Errorf("摘要未标明主因 %q：%s", g.label, s)
		}
	}
}
