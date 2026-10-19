package ursm_test

// §143.18 的文档门：全库分区索引「全族全图」、**唯一索引 0 扫描的读数假象**、
// **构造性死**、**新档位 T5**、以及「最大杠杆是保留期」这五条结论必须在场。
//
// 背景（runbook §143.18）：
// §143.16 把「按体积排 DROP」升级成「逐条实测读数 + 读方证据 + 分档 T0~T3」；
// §143.17 把同一套量具推到 `session_turns` 一族；
// §143.18 把它推广到**全库 32 个分区父表 / 170 条索引声明 / 5671 MB**，
// 产出 5 条新证据 + 1 条判词修正：
//   ① 96 条声明「全族 0 扫描」＝ 1135 MB（占全库索引 20.0%）；
//   2 ★★★ 其中 292 MB（13 条 UNIQUE）是**唯一性检查不可见**造成的**读数假象**
//      ⇒ 不得进 T3/T4（这是本节最贵的发现，判错了会误删 292 MB）；
//   ③ ★★★ 最大的杠杆不是索引，是**保留期**：`ursm_node_snapshot_min` 族 8.4 GB / 7 天；
//   ④ ★★ 「构造性死」：索引列恒 NULL / 恒单值 / 部分索引恒空 ⇒ 任何查询都用不上；
//   ⑤ ★★ 新档位 **T5「读方存在但规划器不认」**（110 MB）—— T3/T4 都套不进去。
//
// # 为什么「文档也要有门」
//
// ① **「0 扫描」这一句话现在有两种完全相反的含义**，必须让文档把它们分开：
//    对**非唯一**索引，0 扫描多半意味着「没人用」；
//    对**唯一**索引，0 扫描**什么都不能证明**（唯一性检查不进 `index_beginscan`）。
//    ⇒ 一旦被简化成「96 条 0 扫描 = 1135MB 白占空间，删掉」，
//    下一步就会直接 DROP 掉那 292 MB 的唯一索引 —— 而其中 249 MB 那条
//    **正是 `usage_ledger` 唯一的去重机制**（写入侧不带 `ON CONFLICT`）。
//
// ② **「列没有选择性」很容易被读成「索引是死的」**。实测反例：
//    `sessions.status` 恒为 `'active'`（398,414 行、1 个取值），
//    但 `idx_sessions_status` **被规划器选中**（真实形态 R4）⇒ 它是 T4，不是死索引。
//    ⇒ 这道门钉住「恒 NULL ⇒ 死」与「恒单值 ⇒ 只废掉过滤、不废掉排序」这半句区别。
//
// ③ **本节修正了 §143.17 的一条判词**（`uq_session_turns_final_success` 的 T2 口径）。
//    跨节订正必须**就地**留横幅 + 指向 §143.18.2 ⇒ 这道门钉住横幅在场。
//
// # 这道门测什么
//
// 门断言的是**文档结构**（小节在场、关键量在、关键结论在、待拍板清单在、订正横幅在），
// 不连库、不跑 SQL ⇒ CI 与开发机都能跑。
// 判据的**语义**正确性由 §143.18 记录的生产实测背书（脚本见 §143.18.9.4）。

import (
	"os"
	"strings"
	"testing"
)

const runbook14318 = "../../docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"

// section14318 取出 §143.18 这一节的正文。
//
// ★ 为什么必须**按节切**再断言：本 runbook 有 35,000+ 行，
// 同一个量（例如 "78 MB"、"83 MB"、"322 MB"）在别的节里出现过十几次
// ⇒ 整篇断言等于**断言不到东西**（判据钉不住要钉的那一段）。
// ⇒ 判据的单位必须与被测对象的单位一致。
func section14318(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(runbook14318)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.18 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.18 的小节标题：§143.18 的结论整节丢了")
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

// TestRunbook14318HasFamilyWideReadings：10 个小节 + 关键实测读数必须在场。
//
// 关键量是**实测读数**，不是修辞：
//   - 5671 MB（全库叶分区索引体量）
//   - 1135 MB（96 条全族 0 扫描的合计，占全库 20.0%）
//   - 292 MB（13 条 UNIQUE = 唯一性检查不可见 ⇒ 读数假象桶）
//   - 170（父表声明条数）/ 32（分区父表数）
//   - 8,395,956,224（`ursm_node_snapshot_min` 族总字节 = 8.4 GB）
//   - 149,964（`session_memora` pkey 的仲裁扫描数 = 插入次数 1:1）
//   - 276,116（`usage_ledger` 全生命周期插入数，而其 UNIQUE 是 0 扫描）
//   - 265,494（`usage_facts.canonical_id` 全量行数，且 100% 为 NULL）
//   - 168h0m0s（245/154 两台的生效留存窗口）
func TestRunbook14318HasFamilyWideReadings(t *testing.T) {
	sec := section14318(t)

	for i := 0; i <= 9; i++ {
		mark := "§143.18." + itoa14318(i)
		if !strings.Contains(sec, mark) {
			t.Errorf("§143.18 缺少小节 %s：本节的证据链是分层展开的，缺一节就会丢掉那一层的量", mark)
		}
	}
	for _, fact := range []string{
		"5671 MB", "1135 MB", "292 MB", "170", "32",
		"8,395,956,224",
		"149,964", "276,116", "265,494",
		"168h0m0s",
	} {
		if !strings.Contains(sec, fact) {
			t.Errorf("§143.18 缺少实测读数 %q：清单的量必须是实测值，不能只剩结论", fact)
		}
	}
}

// TestRunbook14318StatesUniqueZeroScanIsArtifact：★ 本节最贵的一条结论必须在场。
//
// 「唯一索引的 0 扫描是**读数假象**」这条，如果被简化掉，
// 后果是 **292 MB 的唯一索引被当成「没人用」而 DROP** —— 其中 249 MB 那条
// （`usage_ledger_partitioned_request_id_ts_key`）**正是 `usage_ledger` 唯一的去重机制**
// （写入侧 `INSERT INTO usage_ledger_hot` **不带 `ON CONFLICT`**）。
//
// ⇒ 判据钉三样东西同时在场：
//
//	① **机制**（唯一性检查走插入路径、不经 `index_beginscan` ⇒ 不可见）；
//	② **对照**（沙箱里「UNIQUE + 200 次重复键被拒」与「完全没人用的普通索引」读数**完全一样**）；
//	③ **生产两证**（`usage_ledger` 276,116 写 vs 0 扫描；`session_memora` 同表两唯一索引 1:1 对照）。
func TestRunbook14318StatesUniqueZeroScanIsArtifact(t *testing.T) {
	sec := section14318(t)

	for _, phrase := range []string{
		"index_beginscan",                  // 机制
		"ON CONFLICT",                      // 唯一可见的那种用法
		"读数假象",                             // 结论的定性
		"276,116",                          // 生产证 A
		"149,964",                          // 生产证 B（同表同批行两唯一索引对照）
		"ON CONFLICT (id, partition_date)", // 706 promote 的仲裁键原文
	} {
		if !strings.Contains(sec, phrase) {
			t.Errorf("§143.18 缺 %q：\n"+
				"⇒ 「唯一索引的 0 扫描是读数假象」这条结论若被简化掉，\n"+
				"   292 MB 的唯一索引会被当成「没人用」而 DROP ——\n"+
				"   其中 usage_ledger 那 249 MB **正是它唯一的去重机制**", phrase)
		}
	}
	// 假象桶的**体量与条数**必须在场（清单要能对账）
	for _, fact := range []string{"13 条 / 292 MB", "假象桶"} {
		if !strings.Contains(sec, fact) {
			t.Errorf("§143.18 缺 %q：假象桶的条数与体量必须在场，否则无法与 96 条 / 1135 MB 对账", fact)
		}
	}
}

// TestRunbook14318RecordsConstructiveDeathAndItsLimit：★ 「构造性死」必须在场，
// 且**必须连着它的边界**一起在。
//
// 这一档比「0 扫描」强：它证明索引在任何查询形态下都不可能有用。
// 但它**有一半不能一刀切**：
//
//	· 列**恒 NULL**（`usage_facts.canonical_id` 265,494/265,494、
//	  `system_probe_runs.provider_id` 36,878/36,878、
//	  `sessions.parent_session_id` 398,414/398,414）⇒ 死；
//	· 列**恒单值**（`sessions.status` = `'active'`、`sessions.agent_role` = `'main'`）
//	  ⇒ 只废掉「过滤」能力，**不废掉「排序」能力**
//	  ⇒ 实测 R4 证明 `idx_sessions_status` **确实被规划器选中** ⇒ 它是 T4，不是死索引。
//
// ⇒ 判据钉住两边：恒 NULL 的直证读数 + 「恒单值 ≠ 死索引」这半句。
func TestRunbook14318RecordsConstructiveDeathAndItsLimit(t *testing.T) {
	sec := section14318(t)

	for _, fact := range []string{
		"构造性死",
		"265,494", // usage_facts.canonical_id 全量行数
		"36,878",  // system_probe_runs.provider_id 全量行数
		"398,414", // sessions.parent_session_id 全量行数
		"恒 NULL",  // 结论
		"恒单值",     // 另一半形态
		"只废掉「过滤」", // 边界：恒单值不废掉排序
	} {
		if !strings.Contains(sec, fact) {
			t.Errorf("§143.18 缺 %q：\n"+
				"⇒ 「构造性死」必须连着它的边界一起在：\n"+
				"   恒 NULL ⇒ 死；恒单值 ⇒ 只废掉过滤，**仍能服务排序**（实测 R4 选了 idx_sessions_status）\n"+
				"   ⇒ 少了边界就会把 T4 的索引误判成死索引", fact)
		}
	}
}

// TestRunbook14318HasNewT5Class：★ 新档位 T5 必须在场。
//
// T3 = 仓内无读方 **且** EXPLAIN 不选；T4 = 有读方 **且** sargable **且** EXPLAIN 会选。
// T5 = **有读方，但谓词形态不可 sargable / 选择性过低 ⇒ 实测规划器不选**
// —— 这是前两档都套不进去的第三种形态（110 MB / 5 条）。
//
// ⇒ 少了 T5，读方存在的那 5 条会被错放进 T3（"没人用"）
// 或 T4（"能力取舍"），两个都不是。
func TestRunbook14318HasNewT5Class(t *testing.T) {
	sec := section14318(t)

	for _, phrase := range []string{
		"T5",
		"读方存在但规划器不认",
		"110 MB",
		"BitmapOr", // 为什么 model 那条任何查询都用不上
	} {
		if !strings.Contains(sec, phrase) {
			t.Errorf("§143.18 缺 %q：\n"+
				"⇒ 新档位 T5 必须在场：有读方但形态用不上 ⇒ 实测规划器不选。\n"+
				"   少了它，这 5 条 / 110 MB 会被错放进 T3（没人用）或 T4（能力取舍），两个都不是", phrase)
		}
	}
	// T5 的 5 条名单必须逐条在
	for _, idx := range []string{
		"idx_routing_decision_log_part_model",
		"idx_routing_decision_log_part_success",
		"idx_routing_decision_log_part_tenant_ts",
		"idx_supplier_errors_part_supplier_credential",
		"idx_supplier_errors_part_tenant",
	} {
		if !strings.Contains(sec, idx) {
			t.Errorf("§143.18 的 T5 名单里没有 %s：名单必须逐条在，"+
				"少一条就分不清是「没写」还是「已经被人动过了」", idx)
		}
	}
}

// TestRunbook14318StatesRetentionIsTheLever：★ 「最大杠杆是保留期」必须在场。
//
// `ursm_node_snapshot_min` 族 8.4 GB / 7 天 / 日增 1.2~1.5 GB ⇒ 折 30 天 ≈ 36 GB，
// 而设计文档估计 17 GB（**低约 2 倍**）。
// 而它的主索引 `ursm_node_snapshot_min_pkey`（2114 MB）**扫描 27,585,296 次**
// ⇒ 是留存 worker 的承重访问路径 ⇒ **不可删**；死页率仅 0.13% ⇒ **没有 REINDEX 空间**。
//
// ⇒ 少了这条，下一个人会继续在「删索引」上找空间，而真正的杠杆在「留几天」。
//
// 同时「7 天是设计值、不是回落」这半句必须在场
// （`retention.go:40` 明写「默认值由 30 天改为 7 天（2026-10-14）」；
//
//	两台实测 `168h0m0s` + `source=env` ⇒ 无档位漂移）。
func TestRunbook14318StatesRetentionIsTheLever(t *testing.T) {
	sec := section14318(t)

	for _, fact := range []string{
		"8.4 GB",
		"36 GB",
		"27,585,296",
		"0.13%",
		"168h0m0s",
		"7 天是设计值",
	} {
		if !strings.Contains(sec, fact) {
			t.Errorf("§143.18 缺 %q：\n"+
				"⇒ 「杠杆是保留期不是索引」必须在场（含 7 天是设计值、两台无漂移），\n"+
				"   否则下一个人会继续在「删索引」上找空间，而真正的杠杆在「留几天」", fact)
		}
	}
	for _, fact := range []string{"1.2 ~ 1.5 GB/天", "低约 2 倍"} {
		if !strings.Contains(sec, fact) {
			t.Errorf("§143.18 缺 %q：保留期的收益计算必须带**实测速率**与**估算偏差**，否则改窗口是盲改", fact)
		}
	}
}

// TestRunbook14318HasOwnerDecisionTable：待拍板清单必须在场。
//
// §143.18 的产出**不是**可以直接执行的动作，而是一组要属主拍板的取舍
// （A 判词修正 / B 保留期 / C T3 清单 / D T5 新档 / E 死页 / F 退役族 /
//
//	G 父表零声明 / H 连接池 / I 878 论据）⇒ 这张表是本节的**交付物**。
func TestRunbook14318HasOwnerDecisionTable(t *testing.T) {
	sec := section14318(t)

	for _, item := range []string{
		"| **A** |", "| **B** |", "| **C** |", "| **D** |", "| **E** |",
		"| **F** |", "| **G** |", "| **H** |", "| **I** |",
	} {
		if !strings.Contains(sec, item) {
			t.Errorf("§143.18 的待拍板清单里缺 %s：本节的交付物是这张表，读数本身只是观测", item)
		}
	}
}

// TestRunbook14318StatesReadOnlyBoundary：只读边界必须在场。
//
// 本节对生产**零写入**（无 DROP / REINDEX / VACUUM / ANALYZE / 建索引 / 应用迁移）。
// 少了这条，读者无法知道这一节只是只读观测，误以为可以照着它的建议直接上生产。
//
// 同时「诚实边界」小节必须在场：全族全图**只覆盖父声明派生的 170 条**、
// 26 条孤儿叶索引在图外、`_request_logs` 退役族 23 条未逐列闭环。
func TestRunbook14318StatesReadOnlyBoundary(t *testing.T) {
	sec := section14318(t)

	if !strings.Contains(sec, "零写入") {
		t.Error("§143.18 丢了「零写入」这条边界声明：\n" +
			"⇒ 读者无法知道这一节只是只读观测，误以为可以照着它的建议直接上生产")
	}
	for _, fact := range []string{
		"孤儿叶索引",
		"26 条",
		"未逐列闭环",
	} {
		if !strings.Contains(sec, fact) {
			t.Errorf("§143.18 的诚实边界里缺 %q：\n"+
				"⇒ 全族全图只覆盖「父声明派生」的 170 条；孤儿叶索引在图外；\n"+
				"   退役族未逐列闭环 —— 这些不写出来就成了「已全覆盖」", fact)
		}
	}
}

// TestRunbook14318CorrectionBannerIn14317：★ 跨节订正必须**就地**留横幅。
//
// §143.18 修正了 §143.17 的一条判词（`uq_session_turns_final_success` 的「T2 口径」）。
// 跨节的订正如果只写在 §143.18 里，**只读 §143.17 的人会继续照原话执行**。
//
// ⇒ 判据：§143.17 节内必须有指向 §143.18.2 的订正横幅。
func TestRunbook14318CorrectionBannerIn14317(t *testing.T) {
	raw, err := os.ReadFile(runbook14318)
	if err != nil {
		t.Fatalf("读 runbook 失败：%v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(ln, "## §143.17 ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("runbook 里找不到 §143.17 的小节标题")
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	sec14317 := strings.Join(lines[start:end], "\n")

	if !strings.Contains(sec14317, "订正横幅") {
		t.Error("§143.17 里没有「订正横幅」：\n" +
			"⇒ §143.18 已推翻该节一条判词（唯一索引 0 扫描 ⇒ T2 口径）的**前提**。\n" +
			"   跨节订正只写在 §143.18 里的话，只读 §143.17 的人会继续照原话执行")
	}
	if !strings.Contains(sec14317, "§143.18.2") {
		t.Error("§143.17 的订正横幅没有指向 §143.18.2：读者必须能直接跳到证据所在")
	}
	// ★ 判据**不钉**「唯一索引 ⇒ T2 口径」这句话的消失 ——
	// 横幅必须**引用**原话才能订正它（订正纪律：保留原文 + 就地横幅）。
	if !strings.Contains(sec14317, "uq_session_turns_final_success") {
		t.Error("§143.17 里原表行不见了：订正要「保留原文 + 就地横幅」，不能把原文删掉")
	}
}

// itoa14318：把 0~9 转成字符串（避免为断言引入 strconv）。
func itoa14318(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
