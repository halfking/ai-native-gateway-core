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
//  1. 决策表本身（8 例自测，含"回滚锚点在 0 行时也不可回收"这条语义分支）
//  2. 登记册逐行被决策表**重算** + 绝对条数与绝对字节钉住
//  3. **缺口打桩**：usage_facts 仍无任何留存登记 —— 一旦有人加了 TTL 登记，
//     门红，逼着同时更新登记册与 runbook（缺口若无人守着就会悄悄消失）
//  4. **证据字符串钉住**：本轮所有结论都引用了日志里的 mode= 值与 runbook 横幅，
//     它们一旦被改写，结论就不可复现 ⇒ 门盯住这些字符串
//
// ★ 本门**不需要数据库**（纯静态 + 纯函数）⇒ 不会 SKIP 成假绿。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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
			},
			want: VerdictPartitionDropActive,
		},
		{
			name: "普通 heap ⇒ 行级 DELETE 型（有可回收字节）",
			in: RetentionShapeInput{
				Scope: ScopeSnapshotFamily, Relkind: "r",
				Rows: 6042257, TotalBytes: 2411266048, ReclaimableBytes: 367839785,
				TTLRegistered: true,
			},
			want: VerdictRowDeleteBloat,
		},
		{
			// ★ 承重例：0 行的回滚锚点仍然不可回收。
			in: RetentionShapeInput{
				Scope: ScopeSnapshotObject, Relkind: "r",
				Rows: 0, TotalBytes: 4096, IsRollbackAnchor: true,
			},
			want: VerdictNotReclaimable,
		},
		{
			name: "有行的普通对象 ⇒ 不可回收",
			in: RetentionShapeInput{
				Scope: ScopeSnapshotObject, Relkind: "r",
				Rows: 2614886, TotalBytes: 893845504,
			},
			want: VerdictNotReclaimable,
		},
		{
			name: "0 行但占空间 ⇒ 立即可回收",
			in: RetentionShapeInput{
				Scope: ScopeUsageFactsObject, Relkind: "r",
				Rows: 0, TotalBytes: 83492864, ReclaimableBytes: 83476480,
				TTLRegistered: true,
			},
			want: VerdictReclaimableNow,
		},
		{
			name: "0 行且不占空间 ⇒ 无事可做",
			in: RetentionShapeInput{
				Scope: ScopeUsageFactsObject, Relkind: "r",
				Rows: 0, TotalBytes: 0, TTLRegistered: true,
			},
			want: VerdictNotReclaimable,
		},
		{
			name: "usage_facts 族无 TTL 登记 ⇒ 唯一无界增长族",
			in: RetentionShapeInput{
				Scope: ScopeUsageFactsFamily, Relkind: "p",
				Rows: 275828, TotalBytes: 373678080, TTLRegistered: false,
			},
			want: VerdictGapNoRetention,
		},
		{
			name: "机制缺口 ⇒ 没有自动生效的路径",
			in: RetentionShapeInput{
				Scope: ScopeStructuralGap, Relkind: "-", TTLRegistered: false,
			},
			want: VerdictStructuralGapNoAutoPath,
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
	raw, err := os.ReadFile(tsvPath14343)
	if err != nil {
		t.Fatalf("读取登记册 %s：%v", tsvPath14343, err)
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

	// 绝对条数：登记册被删行 ⇒ 红。
	if len(rows) != 9 {
		t.Fatalf("登记册行数 = %d，期望 9（少一行 = 一个缺口被悄悄抹掉）", len(rows))
	}

	// 逐行重算 verdict。
	for _, row := range rows {
		got, reason := ClassifyRetentionShape(RetentionShapeInputOf(row))
		if got != row.Verdict {
			t.Errorf("%s/%s：登记册 verdict = %q，决策表重算 = %q（%s）",
				row.Instance, row.Object, row.Verdict, got, reason)
		}
	}

	// 绝对字节：关键读数被改动 ⇒ 红（防止「为了好看」调数字）。
	// ★ 键是 instance|object —— **同名对象在两个实例上都有**（快照族两实例同名但形态不同），
	//   只按 object 钉会把其中一个的读数错配到另一个身上。
	wantBytes := map[string]int64{
		"211|public.ursm_node_snapshot_min":         2411266048,  // 211 快照族（普通表 / row-delete）
		"252|public.ursm_node_snapshot_min":         10033619520, // 252 快照族（分区父表 / partition-drop）
		"252|public.ursm_node_snapshot_min_legacy":  893845504,   // 252 回滚锚点
		"252|public.usage_facts_default":            83492864,    // 252 纯索引膨胀
		"252|public.usage_facts_20260926..20261005": 36249600,    // 252 空日分区包
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

	// 两实例的形态分叉必须在册（这是本轮的中心结论）。
	var sawPartitioned, sawPlain bool
	for _, row := range rows {
		if row.Scope == ScopeSnapshotFamily && row.Relkind == "p" {
			sawPartitioned = true
		}
		if row.Scope == ScopeSnapshotFamily && row.Relkind == "r" {
			sawPlain = true
		}
	}
	if !sawPartitioned || !sawPlain {
		t.Errorf("登记册必须同时含分区形态与普通表形态的快照族行（分叉是本轮中心结论）：partitioned=%v plain=%v",
			sawPartitioned, sawPlain)
	}

	// usage_facts 的缺口必须在册且**两个实例都在**（只登记一个实例 = 缺口没被真正发现）。
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

// TestSection14343UsageFactsGapIsPinned 是**缺口打桩**。
//
// 为什么要有这道门：`usage_facts` 是两实例上**唯一**没有任何留存的族
// （无 TTL 键、无 worker、无 DROP 登记）。缺口若无人守着就会悄悄消失
// ——而它消失的方式恰恰是"有人加了 TTL 但只改了代码、没改文档/登记册"。
//
// ★ 判据用 **go/ast** 而不是文本匹配：stateTableTTLSpecs() 的函数体里
//
//	**有注释**，文本匹配会被注释喂饱（§143.42 的 M3 教训：判据一旦能在
//	注释里被满足就不是判据）。AST 只看真实的复合字面量项。
//
// ⚠ 门红 = 有人改了语义（加了 TTL 登记）⇒ 请同时更新
//
//	登记册 scripts/audit/retention_shape_findings.tsv 与 runbook §143.43。
func TestSection14343UsageFactsGapIsPinned(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, partitionMgr14343, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("解析 %s：%v", partitionMgr14343, err)
	}

	var parents []string
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "stateTableTTLSpecs" || fn.Body == nil {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "parent" {
				return true
			}
			if lit, ok := kv.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					parents = append(parents, s)
				}
			}
			return true
		})
	}
	if !found {
		t.Fatalf("%s 里找不到 stateTableTTLSpecs() —— 缺口打桩的前提没了", partitionMgr14343)
	}

	for _, p := range parents {
		if strings.Contains(p, "usage_facts") {
			t.Errorf("stateTableTTLSpecs() 里出现了 %q —— usage_facts 的留存已被登记；"+
				"请同时更新登记册 %s 与 runbook §143.43（缺口被填上是好事，但证据链必须同步）",
				p, tsvPath14343)
		}
	}
	if len(parents) < 7 {
		t.Errorf("stateTableTTLSpecs() 只解析出 %d 个 parent（期望 ≥7）—— 解析面可能漏了，"+
			"缺口打桩会变成恒绿门", len(parents))
	}
}

// TestSection14343EvidenceStringsArePinned 钉住本轮**全部证据字符串**。
//
// 本轮的每一条结论都引用了两类可复现的字符串：
//
//	① 生产日志里的 mode 值（partition-drop / row-delete）—— 来自实现里的常量；
//	② runbook 的横幅编号与订正正文 —— 结论的书面载体。
//
// 它们一旦被改写，本轮结论就**不可复现**，而读者不会察觉 ⇒ 必须钉住。
func TestSection14343EvidenceStringsArePinned(t *testing.T) {
	raw, err := os.ReadFile(retentionImpl14343)
	if err != nil {
		t.Fatalf("读取 %s：%v", retentionImpl14343, err)
	}
	impl := string(raw)

	// 形态探测：relkind='p' 才是"走 DROP"的判据（钉住形态，不是钉住关键词）。
	if !strings.Contains(impl, "c.relkind = 'p'") {
		t.Errorf("%s 里不再有 `c.relkind = 'p'` 的形态探测 —— 双模留存的判据被改了，"+
			"本轮全部结论（252=DROP / 211=DELETE）随之失效", retentionImpl14343)
	}
	// 两个 mode 常量 = 日志里的两个 mode 值。
	for _, mode := range []string{"partition-drop", "row-delete"} {
		if !strings.Contains(impl, mode) {
			t.Errorf("%s 里不再有 mode 常量 %q —— 生产日志的 mode= 证据不可复现了",
				retentionImpl14343, mode)
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
