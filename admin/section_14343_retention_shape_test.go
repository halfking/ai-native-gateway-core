package admin

// section_14343_retention_shape_test.go —— §143.43 门。
//
// 背景（§143.43，2026-10-21）：本轮**推翻了上一轮自己的三处判词**——
//
//	「快照族走行级 DELETE ⇒ 分区文件永不收缩 ⇒ 占用 O(总天数)」
//
// 生产实测否掉了它：快照族的留存是**双模**的
// （domains/ursm/v2/persist/retention_partition.go，形态由 pg_class.relkind 自答）：
//
//	分区父表（relkind='p'）⇒ 逐分区 DROP ⇒ 占用 O(保留天数)，空间真归还
//	普通 heap（relkind='r'）⇒ 行级 DELETE ⇒ 行数有界，但空间不归还
//
// 两实例实测：252 = 分区父表（生产日志 mode=partition-drop，2026-10-21T00:01 回收 293,322,752 B）；
// 211 = 普通表（生产日志 mode=row-delete，每小时 DELETE 约 38k 行）。
//
// 本门守四件事：
//  1. 决策表本身（10 例自测，含"回滚锚点在 0 行时也不可回收"这条语义分支）
//  2. 登记册逐行被决策表**重算** + 绝对条数与绝对字节钉住
//  3. **缺口双向钉**（§143.45 改造）：仓内已闭合（§143.44 加了 TTL 登记），
//     **生产未闭合**（未部署 ⇒ settings_kv 里仍无该键）⇒ 门同时盯住两侧：
//     仓内那侧不许退回（否则缺口没人管），生产那侧不许谎报已闭合
//  4. **证据字符串钉住**：本轮所有结论都引用了日志里的 mode= 值与 runbook 横幅，
//     它们一旦被改写，结论就不可复现 ⇒ 门盯住这些字符串
//
// ★ 本门**不需要数据库**（纯静态 + 纯函数）⇒ 不会 SKIP 成假绿。
//
// ★ ★ §143.45（2026-10-21）本门被**自己的上一轮提交**打红过一次，值得记下来：
//   §143.43 的缺口打桩是**单向**的（"usage_facts 必须不在 stateTableTTLSpecs 里"），
//   而 §143.44 正是**为了填这个缺口**才加了登记 ⇒ ⇒ 提交即自红。
//   ⇒ ★ 通用式：**「缺口打桩」必须是可翻转的**——缺口被填上时，
//     钉子要从「缺口存在」翻成「缺口已闭合且登记册已同步」，
//     而不是让门一直红到有人删掉证据为止。
//   ⇒ 本次翻转后的形状是**双向钉**（仓内闭合 + 生产未闭合各有一根钉），
//     正好覆盖「仓内闭合 ≠ 生产闭合」这个 §143.44 才出现的区分。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	tsvPath14343 = "../scripts/audit/retention_shape_findings.tsv"
	// runbook14343 是 §143.43 所在的 runbook（§143.39~§143.43 同一条线）。
	runbook14343 = "../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	// retentionImpl14343 是双模留存的实现文件（本轮全部结论的源头）。
	retentionImpl14343 = "../domains/ursm/v2/persist/retention_partition.go"
	// partitionMgr14343 是分区调度器（缺口打桩要看它的注册表）。
	partitionMgr14343 = "../bg/partition_manager.go"
)

// TestSection14343DecisionTableSelfTest 是决策表的自测。
//
// ★ 每一例都必须**先想清楚「如果判据写错，会红吗」**：一个恒绿的自测
//
//	比没有自测更坏（它给读者一个"已验证"的错觉）。
//	第 3 例是本轮唯一承重的语义分支：回滚锚点**即使 0 行**也不可回收
//	（删了它就断了 830 的唯一退路）—— 它必须排在"0 行 ⇒ 可回收"之前。
func TestSection14343DecisionTableSelfTest(t *testing.T) {
	cases := []struct {
		name string
		in   RetentionShapeInput
		want RetentionVerdict
	}{
		{
			name: "分区父表 ⇒ DROP 型留存生效",
			in: RetentionShapeInput{
				Scope: ScopeSnapshotFamily, Relkind: "p",
				Rows: 28969382, TotalBytes: 10033619520, TTLRegistered: true,
				MechanismState: MechanismStateActive,
			},
			want: VerdictPartitionDropActive,
		},
		{
			name: "普通 heap ⇒ 行级 DELETE 型（有可回收字节）",
			in: RetentionShapeInput{
				Scope: ScopeSnapshotFamily, Relkind: "r",
				Rows: 6042257, TotalBytes: 2411266048, ReclaimableBytes: 367839785,
				TTLRegistered:  true,
				MechanismState: MechanismStateActive,
			},
			want: VerdictRowDeleteBloat,
		},
		{
			// ★ 承重例：0 行的回滚锚点仍然不可回收。
			in: RetentionShapeInput{
				Scope: ScopeSnapshotObject, Relkind: "r",
				Rows: 0, TotalBytes: 4096, IsRollbackAnchor: true,
				MechanismState: MechanismStateOutOfScope,
			},
			want: VerdictNotReclaimable,
		},
		{
			name: "有行的普通对象 ⇒ 不可回收",
			in: RetentionShapeInput{
				Scope: ScopeSnapshotObject, Relkind: "r",
				Rows: 2614886, TotalBytes: 893845504,
				MechanismState: MechanismStateOutOfScope,
			},
			want: VerdictNotReclaimable,
		},
		{
			name: "0 行但占空间 ⇒ 立即可回收",
			in: RetentionShapeInput{
				Scope: ScopeUsageFactsObject, Relkind: "r",
				Rows: 0, TotalBytes: 83492864, ReclaimableBytes: 83476480,
				TTLRegistered:  true,
				MechanismState: MechanismStateActive,
			},
			want: VerdictReclaimableNow,
		},
		{
			name: "0 行且不占空间 ⇒ 无事可做",
			in: RetentionShapeInput{
				Scope: ScopeUsageFactsObject, Relkind: "r",
				Rows: 0, TotalBytes: 0, TTLRegistered: true,
				MechanismState: MechanismStateActive,
			},
			want: VerdictNotReclaimable,
		},
		{
			name: "usage_facts 族无 TTL 登记 ⇒ 唯一无界增长族",
			in: RetentionShapeInput{
				Scope: ScopeUsageFactsFamily, Relkind: "p",
				Rows: 275828, TotalBytes: 373678080, TTLRegistered: false,
				MechanismState: MechanismStateNone,
			},
			want: VerdictGapNoRetention,
		},
		{
			// ★ §143.45 新增：仓内登记了 TTL 之后，这一族与快照族**同构**
			//   ⇒ 按形态判，而不是仍按"缺口"判。
			//   ⚠ 这一例是"仓内闭合"那一侧的形状；"生产未部署"那一侧
			//   仍由登记册的 ttl_registered=no 表达（见缺口双向钉）。
			name: "usage_facts 族已登记 TTL ⇒ 与快照族同构（仓内闭合后的形态）",
			in: RetentionShapeInput{
				Scope: ScopeUsageFactsFamily, Relkind: "p",
				Rows: 276190, TotalBytes: 254402560, TTLRegistered: true,
				MechanismState: MechanismStateActive,
			},
			want: VerdictPartitionDropActive,
		},
		{
			// ★ §143.45 新增：211 在 §143.44 分区化之后与 252 **同款**。
			//   §143.43 的中心结论是「两实例形态分叉」（252=p / 211=r），
			//   这一例把"分叉已闭合"钉进决策表自测。
			name: "211 现状（§143.44 分区化后）⇒ 与 252 同款",
			in: RetentionShapeInput{
				Scope: ScopeSnapshotFamily, Relkind: "p",
				Rows: 6083147, TotalBytes: 2029412736, TTLRegistered: true,
				MechanismState: MechanismStateActive,
			},
			want: VerdictPartitionDropActive,
		},
		{
			name: "机制缺口 ⇒ 没有自动生效的路径",
			in: RetentionShapeInput{
				Scope: ScopeStructuralGap, Relkind: "-", TTLRegistered: false,
				MechanismState: MechanismStateOutOfScope,
			},
			want: VerdictStructuralGapNoAutoPath,
		},

		// ── §143.59 新增：无回收路径对象的四种失效形态 ────────────────────
		//
		// ★ 为什么这四例必须分开写：同一个结论（"当前不回收"）背后是
		//
		//	四种**完全不同的处置**（等它到期 / 接线 / 改归档口径 / 不该由它管）
		//	⇒ 混成一句话就会导致"归因错了、动作也就错了"。
		{
			name: "形态 B：机制实现完整但零生产调用方 ⇒ 当前不回收",
			in: RetentionShapeInput{
				Scope: ScopeRetentionGapObject, Relkind: "r",
				Rows: 1832998, TotalBytes: 1213685760,
				MechanismState: MechanismStateNotWired,
			},
			want: VerdictMechanismNotWired,
		},
		{
			name: "形态 C：机制在跑但目标集恒空 ⇒ 实际不回收",
			in: RetentionShapeInput{
				Scope: ScopeRetentionGapObject, Relkind: "r",
				Rows: 832953, TotalBytes: 866000896,
				MechanismState: MechanismStateTargetEmpty,
			},
			want: VerdictMechanismTargetEmpty,
		},
		{
			name: "形态 A：机制在跑但当前无对象到期 ⇒ 暂无可回收",
			in: RetentionShapeInput{
				Scope: ScopeRetentionGapObject, Relkind: "r",
				Rows: 215915, TotalBytes: 154738688,
				MechanismState: MechanismStateNotYetDue,
			},
			want: VerdictMechanismNotYetDue,
		},
		{
			name: "形态 D：按设计不在机制范围内",
			in: RetentionShapeInput{
				Scope: ScopeRetentionGapObject, Relkind: "r",
				Rows: 100, TotalBytes: 4096,
				MechanismState: MechanismStateOutOfScope,
			},
			want: VerdictMechanismOutOfScope,
		},
		{
			name: "压根没有机制 ⇒ 无界增长",
			in: RetentionShapeInput{
				Scope: ScopeRetentionGapObject, Relkind: "r",
				Rows: 100, TotalBytes: 4096,
				MechanismState: MechanismStateNone,
			},
			want: VerdictGapNoRetention,
		},
		{
			// ★ 承重的**自相矛盾**例：scope 与 mechanism_state 不相容
			//   ⇒ 决策表必须说"登记册坏了"，而不是照着矛盾数据算结论。
			name: "登记册自相矛盾（scope 不接受该 mechanism_state）⇒ 判为数据坏了",
			in: RetentionShapeInput{
				// ★ 快照族的机制只能是 ACTIVE（逐分区 DROP）；
				//   给它记 NOT_WIRED 就是自相矛盾 ⇒ 决策表必须说"数据坏了"，
				//   而不是照着矛盾数据算出"分区 DROP 生效"这种看似合理的结论。
				Scope: ScopeSnapshotFamily, Relkind: "p",
				Rows: 30355931, TotalBytes: 9508798464, TTLRegistered: true,
				MechanismState: MechanismStateNotWired,
			},
			want: VerdictRegistryInconsistent,
		},
		{
			name: "参照基准行（库级总量）⇒ 不是留存形态",
			in: RetentionShapeInput{
				Scope: ScopeReferenceTotal, Relkind: "-",
				Rows: 0, TotalBytes: 31848617651,
				MechanismState: MechanismStateOutOfScope,
			},
			want: VerdictReferenceTotal,
		},
	}

	for _, tc := range cases {
		got, reason := ClassifyRetentionShape(tc.in)
		if got != tc.want {
			t.Errorf("%s：verdict = %q，期望 %q（理由：%s）", tc.name, got, tc.want, reason)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s：判定理由为空 —— 理由是给人读的，空理由等于没判", tc.name)
		}
	}
}

// TestSection14343RegistryIsRecomputable 让登记册**不可能自说自话**：
// 每行的 verdict 必须等于决策表重算出来的结果，且绝对条数与绝对字节钉死。
func TestSection14343RegistryIsRecomputable(t *testing.T) {
	rows := readRetentionShapeRows(t, tsvPath14343)

	// 绝对条数：登记册被删行 ⇒ 红。
	// ★ §143.59：10 → 16 行（新增 2 个参照基准行 + 4 个无回收路径对象的行）。
	if len(rows) != 16 {
		t.Fatalf("登记册行数 = %d，期望 16（少一行 = 一个缺口被悄悄抹掉）", len(rows))
	}

	// 逐行重算 verdict。
	for _, row := range rows {
		got, reason := ClassifyRetentionShape(RetentionShapeInputOf(row))
		if got != row.Verdict {
			t.Errorf("%s/%s：登记册 verdict = %q，决策表重算 = %q（%s）",
				row.Instance, row.Object, row.Verdict, got, reason)
		}
	}

	// 绝对字节：**只钉静态对象**。
	//
	// ★ §143.59 订正（这是本轮最重要的一处门改造）：
	//
	//	对**按天增长的族**钉绝对字节数是**错误的钉法** —— 实测 252 快照族
	//	10,193,199,104 B（§143.58 00:23）→ 9,508,798,464 B（§143.59 01:54）
	//	⇒ **1.5 小时内 −6.7%**（7 天窗口的日分区到期被删，方向还是**下降**）
	//	⇒ 那种钉法在 12 小时内就会过期，而且**过期与出错长得一模一样**。
	//
	//	⇒ 增长的族（快照族、usage_facts 族）改钉 **量级档位 + 相对量（族 ÷ 库）**
	//	   —— 见 admin/section_14359_retention_registry_test.go。
	//	⇒ **静态对象**（legacy 锚点、已回收的空分区、纯索引膨胀）**继续钉绝对值**：
	//	   它们**不增长**，对它们钉绝对值是对的（§143.58 复读：两侧 legacy
	//	   的行数与字节与 §143.43/§143.45 的读数**逐字相同**）。
	wantBytes := map[string]int64{
		"211|public.ursm_node_snapshot_min_legacy":  1902911488, // 211 回滚锚点（静态）
		"252|public.ursm_node_snapshot_min_legacy":  893845504,  // 252 回滚锚点（静态）
		"252|public.usage_facts_default":            57344,      // 252 纯索引膨胀（§143.44 已回收到索引最小尺寸）
		"211|public.usage_facts_20261011":           65536,      // 211 空日分区
		"252|public.usage_facts_20260926..20261005": 0,          // §143.44 已 DROP ⇒ 对象已不存在
	}
	seen := map[string]bool{}
	for _, row := range rows {
		key := row.Instance + "|" + row.Object
		if want, ok := wantBytes[key]; ok {
			seen[key] = true
			if row.TotalBytes != want {
				t.Errorf("%s：total_bytes = %d，期望 %d", key, row.TotalBytes, want)
			}
		}
	}
	for key := range wantBytes {
		if !seen[key] {
			t.Errorf("登记册缺少 %s —— 它是本轮的关键读数，缺行等于缺证据", key)
		}
	}

	// ★ §143.45 订正：**形态分叉已闭合**。
	//   §143.43 的中心结论是「两实例形态分叉」（252 = 分区父表 / 211 = 普通 heap），
	//   §143.44 把 211 也分区化了 ⇒ 两实例现已**同款**（都是 relkind='p'）。
	//   ⇒ 本门改钉「已闭合」这个**当前事实**；但**不能**因为分叉消失
	//   就把「普通 heap 形态」这条能力从决策表里删掉（它仍可能是新装库的样子），
	//   那条能力由决策表自测用例承载。
	var sawPartitioned int
	var sawPlainFamily bool
	for _, row := range rows {
		if row.Scope == ScopeSnapshotFamily && row.Relkind == "p" {
			sawPartitioned++
		}
		if row.Scope == ScopeSnapshotFamily && row.Relkind == "r" {
			sawPlainFamily = true
		}
	}
	if sawPartitioned != 2 {
		t.Errorf("两个实例的快照族都必须是分区父表（partitioned 计数 = %d，期望 2）—— "+
			"§143.44 已把 211 也分区化；若某一侧退回普通表，那是一次**新的形态分叉**，"+
			"必须重新定性（而不是沿用本轮结论）", sawPartitioned)
	}
	if sawPlainFamily {
		t.Error("登记册里还有 relkind=r 的快照族行 —— 211 已分区化（§143.44），" +
			"普通表形态的读数是 §143.43 的旧快照")
	}

	// ★ §143.45 新增：**族总量必须 > 0**。
	//   pg_total_relation_size 对**分区父表返回 0**（父表自己没有存储）
	//   ⇒ 若有人把父表函数的返回值直接抄进登记册，族总量会写成 0 ——
	//   而 0 看起来像"很干净"，于是错值反而更不容易被察觉。
	//   ⇒ 正确口径是**逐分区求和**，登记册里已是求和值。
	for _, row := range rows {
		if row.Scope != ScopeSnapshotFamily && row.Scope != ScopeUsageFactsFamily {
			continue
		}
		if row.TotalBytes <= 0 {
			t.Errorf("%s/%s：分区父表的族总量 = %d —— 这正是 `pg_total_relation_size(父表)=0` "+
				"那个量具坑的形状（族总量必须逐分区求和）", row.Instance, row.Object, row.TotalBytes)
		}
	}

	// usage_facts 的缺口必须在册且**两个实例都在**（只登记一个实例 = 缺口没被真正发现）。
	// ★ §143.45：**生产侧**的缺口仍然成立（仓内已登记但**未部署** ⇒ settings_kv 无该键）
	//   ⇒ 这一根钉盯的是"生产仍无界"，不是"仓内没有登记"（那是 §143.44 的门）。
	gapInstances := map[string]bool{}
	for _, row := range rows {
		if row.Verdict == VerdictGapNoRetention {
			gapInstances[row.Instance] = true
		}
	}
	if !gapInstances["211"] || !gapInstances["252"] {
		t.Errorf("GAP_NO_RETENTION 必须在 211 与 252 两个实例上都登记，实际：%v", gapInstances)
	}
}

// TestSection14343UsageFactsGapIsOpenInProductionAndClosedInRepo 是**缺口双向钉**
// （§143.45 改造，原名 TestSection14343UsageFactsGapIsPinned）。
//
// ── 为什么必须翻转 ────────────────────────────────────────────────────────
// §143.43 的钉是**单向**的：「usage_facts 必须不在 stateTableTTLSpecs() 里」。
// 而 §143.44 正是**为了填这个缺口**才加了 TTL 登记 ⇒ ⇒ 提交即自红。
// ⇒ ★ 通用式：**「缺口打桩」必须是可翻转的**——缺口被填上时，
//
//	钉子要从「缺口存在」翻成「缺口已闭合 **且** 登记册已同步」，
//	而不是让门一直红到有人删掉证据为止。
//
// ── 翻转后的形状：两根钉，各盯一侧 ───────────────────────────────────────
//
//	仓内那侧（§143.44 闭合）：usage_facts **必须**在 stateTableTTLSpecs() 里，
//	    且 setting/fallback/partitionUnit 三个值都对
//	    ⇒ 有人把仓内登记删了 ⇒ 红（否则这个缺口就没人管了）。
//	生产那侧（**未**闭合）：登记册里两个实例**仍须**是 GAP_NO_RETENTION
//	    且 ttl_registered=no、method=TTL_DECIDED_PENDING_DEPLOY
//	    ⇒ 有人把登记册改成"生产已闭合" ⇒ 红（在谎报生产状态）。
//
// ★ 两根钉之间的**区别**就是 §143.44 引入的那个区分：**仓内闭合 ≠ 生产闭合**
//
//	（本轮未部署：211 迁移水位落后 ⇒ 生产上仍是老二进制）。
//	⇒ 把两者混为一谈就会写出"usage_facts 已有留存"这种**半真**的话。
//
// ★ 判据用 **go/ast** 而不是文本匹配：stateTableTTLSpecs() 的函数体里
//
//	**有注释**，文本匹配会被注释喂饱（§143.42 的 M3 教训：判据一旦能在
//	注释里被满足就不是判据）。AST 只看真实的复合字面量项。
//
// ⚠ 门红 = 有人改了语义 ⇒ 请同时更新登记册
//
//	登记册 scripts/audit/retention_shape_findings.tsv 与 runbook §143.43/§143.45。
//
// ⚠ **部署之后**（生产那侧真的闭合了）本门要**翻面**：把登记册的
//
//	ttl_registered 改成 yes、verdict 改成 PARTITION_DROP_ACTIVE，
//	并把下面「生产那侧」那根钉改成「必须已闭合」——
//	★ 翻转时**两处都要动**（登记册 + 本门），只动一处会得到一个恒绿门。
func TestSection14343UsageFactsGapIsOpenInProductionAndClosedInRepo(t *testing.T) {
	// ── 仓内那侧：AST 解析 stateTableTTLSpecs() ────────────────────────────
	specs := stateTableTTLSpecsByAST(t, partitionMgr14343)

	// ★ 解析面自检：specs 为空 ⇒ 下面的断言会**恒绿**（"没找到"不等于"没有"）。
	//   ⇒ 先钉住"解析到了足够的条数"，再谈具体那条。
	if len(specs) < 8 {
		t.Fatalf("stateTableTTLSpecs() 只解析出 %d 条（期望 ≥8）—— 解析面漏了，"+
			"下面的断言会变成恒绿门", len(specs))
	}

	uf, ok := specs["usage_facts"]
	if !ok {
		t.Fatalf("stateTableTTLSpecs() 里**没有** usage_facts（解析到 %d 条：%v）—— "+
			"§143.44 已给该族登记 TTL=365 + 按日 DROP；删掉它 ⇒ 该族回到无界增长，"+
			"而生产那侧的缺口钉会误以为有人管着", len(specs), specParentNames(specs))
	}
	if uf["setting"] != "lifecycle.usage_facts_ttl_days" {
		t.Errorf("usage_facts 的 setting = %q，期望 lifecycle.usage_facts_ttl_days",
			uf["setting"])
	}
	if uf["fallback"] != "365" {
		t.Errorf("usage_facts 的 fallback = %q，期望 365（属主拍板值；漂了 ⇒ "+
			"生产行为与决策不符）", uf["fallback"])
	}
	if uf["partitionUnit"] != "day" {
		t.Errorf("usage_facts 的 partitionUnit = %q，期望 day —— "+
			"既有 SQL helper 只枚举月度分区名，monthly 单元会让 usage_facts_YYYYMMDD "+
			"一个都匹配不上 ⇒ 等于什么都没登记", uf["partitionUnit"])
	}

	// ── 生产那侧：登记册必须**如实**说"生产还没闭合" ──────────────────────
	rows := readRetentionShapeRows(t, tsvPath14343)
	gapRows := 0
	for _, row := range rows {
		if row.Scope != ScopeUsageFactsFamily {
			continue
		}
		gapRows++
		if row.TTLRegistered {
			t.Errorf("%s：登记册说 usage_facts 在生产**已有**留存登记（ttl_registered=yes）—— "+
				"§143.44 的两项代码改动**尚未部署**（211 迁移水位落后）⇒ "+
				"生产上 settings_kv 里仍无该键 ⇒ 这是谎报生产状态", row.Instance)
		}
		if row.Verdict != VerdictGapNoRetention {
			t.Errorf("%s：登记册 verdict = %q，期望 GAP_NO_RETENTION —— "+
				"生产侧缺口**未闭合**（未部署）⇒ 判词必须仍然是「缺口」",
				row.Instance, row.Verdict)
		}
		if row.Method != "TTL_DECIDED_PENDING_DEPLOY" {
			t.Errorf("%s：登记册 method = %q，期望 TTL_DECIDED_PENDING_DEPLOY —— "+
				"属主已拍板 TTL=365、仓内已登记，但**生产未部署** ⇒ "+
				"method 必须能区分「已拍板待部署」与「还没拍板」",
				row.Instance, row.Method)
		}
	}
	if gapRows != 2 {
		t.Errorf("登记册里 usage_facts 族行 = %d 条，期望 2（211 + 252 各一行）—— "+
			"只登记一个实例 = 那个实例的缺口没被发现", gapRows)
	}
}

// goConstStrings 解析 Go 文件，返回「常量名 → 字符串字面量值」。
//
// ★ 为什么必须用 AST 而不是 strings.Contains：
//
//	本轮变异 M5 证明了**文本匹配会被注释喂饱** —— 把常量值改成 "rd" 之后，
//	文件里仍然有一处**注释**写着 "row-delete" ⇒ 文本断言照样绿
//	⇒ ⇒ **这正是本仓 LESSONS 里「判据一旦能在注释里被满足就不是判据」那条**，
//	而我自己写门时又犯了一次 ⇒ ⇒ **⇒ 证据类断言一律走 AST**。
func goConstStrings(t *testing.T, path string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("解析 %s：%v", path, err)
	}
	out := map[string]string{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if s, err := strconv.Unquote(lit.Value); err == nil {
					out[name.Name] = s
				}
			}
		}
	}
	return out
}

// TestSection14343EvidenceStringsArePinned 钉住本轮**全部证据字符串**。
//
// 本轮的每一条结论都引用了两类可复现的字符串：
//
//	① 生产日志里的 mode 值（partition-drop / row-delete）与形态探针（relkind = 'p'）
//	   —— 来自实现里的**常量**；
//	② runbook 的横幅编号与订正正文 —— 结论的书面载体。
//
// 它们一旦被改写，本轮结论就**不可复现**，而读者不会察觉 ⇒ 必须钉住。
//
// ★ 实现侧一律走 **go/ast**（读常量值），**不走文本匹配** ——
//
//	变异 M5 已经实证过：文本匹配会被**注释**喂饱（把常量值改掉，断言照样绿）。
func TestSection14343EvidenceStringsArePinned(t *testing.T) {
	consts := goConstStrings(t, retentionImpl14343)

	// 形态探测的判据：relkind='p' 才是「走 DROP」的依据。
	probe, ok := consts["snapshotPartitionProbeSQL"]
	if !ok {
		t.Fatalf("%s 里找不到常量 snapshotPartitionProbeSQL —— 形态探针被改名或删除，"+
			"本轮全部结论（252=DROP / 211=DELETE）随之失效", retentionImpl14343)
	}
	if !strings.Contains(probe, "relkind = 'p'") {
		t.Errorf("形态探针常量里不再有 `relkind = 'p'` —— 双模留存的判据被改了，" +
			"本轮全部结论（252=DROP / 211=DELETE）随之失效")
	}

	// 两个 mode 常量 = 日志里的两个 mode 值。
	for name, want := range map[string]string{
		"RetentionModePartitionDrop": "partition-drop",
		"RetentionModeRowDelete":     "row-delete",
	} {
		got, ok := consts[name]
		if !ok {
			t.Errorf("%s 里找不到常量 %s —— 生产日志的 mode= 证据不可复现了",
				retentionImpl14343, name)
			continue
		}
		if got != want {
			t.Errorf("常量 %s 的值 = %q，期望 %q —— 生产日志的 mode= 证据不可复现了",
				name, got, want)
		}
	}

	rb, err := os.ReadFile(runbook14343)
	if err != nil {
		t.Fatalf("读取 runbook：%v", err)
	}
	book := string(rb)

	for _, needle := range []string{
		"§143.43",
		"订正横幅二十二",
		"mode=partition-drop",
		"mode=row-delete",
		// ★ 「原文保留块」的锚点：订正必须让读者能看到**被推翻的判词原文**。
		//   只钉横幅编号是不够的 —— 横幅可以被改写，原文块被删就没人能对照了。
		"§143.43.1 上一轮的三处判词",
		// ★ §143.45 追加的第二面横幅：它把 §143.43 的**现状**更新掉
		//   （形态分叉已闭合 / usage_facts 仓内已闭合但生产未闭合 / 登记册已刷新）
		//   ⇒ 同样是「结论的书面载体」⇒ 一样要钉住。
		"订正横幅二十三",
		"§143.45",
	} {
		if !strings.Contains(book, needle) {
			t.Errorf("runbook 里缺少 %q —— §143.43 的结论或其书面载体不见了", needle)
		}
	}

	// ★ 订正正文必须**逐字保留被推翻的判词**，否则读者无法对照
	//   （这与 runbook 的既有纪律一致：订正横幅就地加、原文保留）。
	if !strings.Contains(book, "行级 DELETE ⇒ 分区文件永不收缩") {
		t.Error("runbook 里找不到被推翻的原判词「行级 DELETE ⇒ 分区文件永不收缩」—— " +
			"订正必须保留原文，否则读者看不出改了什么")
	}
	if !strings.Contains(book, "已被推翻") {
		t.Error("runbook 里找不到「已被推翻」字样 —— 订正的结论句缺失")
	}
}

// TestSection14343PinFileReferencesAreNotStale 盯住**陈旧引用**。
//
// 本轮发现：`bg/partition_825_contract_test.go` 早已改名为
// `bg/partition_830_contract_test.go`（git log：c6c78773e 删除旧路径），
// 但两处注释仍指向旧文件名 —— 读者按注释去找会找不到文件。
// 这类缺陷不改变行为，却会让「830 不进自动序列」这条决策**看起来没有 pin**。
func TestSection14343PinFileReferencesAreNotStale(t *testing.T) {
	const oldRef = "bg/partition_825_contract_test.go"
	const newRef = "bg/partition_830_contract_test.go"

	for _, path := range []string{
		"../installer/cmd/llm-gw-installer/stats_migrations_test.go",
		"../scripts/apply-db-revision-sequence_test.sh",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s：%v", path, err)
		}
		if strings.Contains(string(raw), oldRef) {
			t.Errorf("%s 仍引用已改名的 %s —— 读者按注释找不到 pin 文件", path, oldRef)
		}
		if !strings.Contains(string(raw), newRef) {
			t.Errorf("%s 没有引用现行文件名 %s —— 引用被删掉了，而不是被更新", path, newRef)
		}
	}

	// 被引用的 pin 文件必须真的存在（否则「引用正确」是空话）。
	if _, err := os.Stat(filepath.Join("..", newRef)); err != nil {
		t.Errorf("被引用的 %s 不存在：%v", newRef, err)
	}
}

// ── 辅助函数（§143.45）────────────────────────────────────────────────────

// readRetentionShapeRows 读登记册并解析成行。
//
// ★ 为什么抽出来：§143.45 起有**两道门**要读同一份登记册
//
//	（决策表重算门 + 缺口双向钉），解析逻辑必须只有一份 ——
//	两份各自解析就会漂移，而漂移的方向通常是"其中一份更宽松"。
func readRetentionShapeRows(t *testing.T, path string) []RetentionShapeRow {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取登记册 %s：%v", path, err)
	}

	var rows []RetentionShapeRow
	for i, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// 表头行（instance scope object …）不是数据行。
		// ★ 刻意**显式跳过表头**而不是靠「列数对不上就忽略」——
		//   后者会把「登记册少了一列」这类真损坏一起吞掉（静默失败）。
		if strings.HasPrefix(line, "instance\t") {
			continue
		}
		row, err := ParseRetentionShapeRow(line)
		if err != nil {
			t.Fatalf("登记册第 %d 行解析失败：%v", i+1, err)
		}
		rows = append(rows, row)
	}
	return rows
}

// stateTableTTLSpecsByAST 用 go/ast 解析 bg/partition_manager.go 里的
// stateTableTTLSpecs()，返回「parent → 字段名 → 字段值」。
//
// ★ 为什么必须走 AST（§143.43/§143.44 各踩过一次）：
//
//	这个函数体里**有注释**，文本匹配会被注释喂饱 ——
//	「判据一旦能在注释里被满足就不是判据」。
//	AST 只看真实的复合字面量项（{parent: "…", setting: "…", …}）。
//
// ★ 返回值里**键是 parent 的精确值**（不是"包含 usage_facts"）：
//
//	父表名必须**精确**是 usage_facts 才会作用到那张表；
//	用 Contains 判会把 usage_facts_archive 之类也算进去（假绿）。
func stateTableTTLSpecsByAST(t *testing.T, path string) map[string]map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("解析 %s：%v", path, err)
	}

	out := map[string]map[string]string{}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "stateTableTTLSpecs" || fn.Body == nil {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			fields := map[string]string{}
			for _, elt := range cl.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				switch v := kv.Value.(type) {
				case *ast.BasicLit:
					if v.Kind == token.STRING {
						if s, err := strconv.Unquote(v.Value); err == nil {
							fields[key.Name] = s
						}
					} else {
						fields[key.Name] = v.Value
					}
				case *ast.Ident:
					fields[key.Name] = v.Name
				}
			}
			parent, ok := fields["parent"]
			if !ok || parent == "" {
				return true
			}
			out[parent] = fields
			return true
		})
	}
	if !found {
		t.Fatalf("%s 里找不到 stateTableTTLSpecs() —— 解析前提没了", path)
	}
	return out
}

// specParentNames 把 spec 表的父表名排好序返回（只为报错信息可读）。
func specParentNames(specs map[string]map[string]string) []string {
	out := make([]string, 0, len(specs))
	for p := range specs {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
