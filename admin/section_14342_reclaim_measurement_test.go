package admin

// section_14342_reclaim_measurement_test.go —— §143.42 的门：
// **「可回收对象」的量具纪律**必须被固化在仓内，否则同一类事故会再发生。
//
// # 这道门补的是哪个盲点
//
// 2026-10-20（§143.40 轮）交付了一个「P0 零语义风险回收包 1,293 MB」：
// 4 个对象被判定为「orphan + 0 行 ⇒ 任何读面都碰不到 ⇒ 零风险」。
// 该清单**已推送到 main**，并登记在 AGENTS.md 的待拍板表里。
//
// 2026-10-21（§143.42 轮）逐条复核后：
//
//	- `ursm_node_snapshot_min_legacy`  实际 **2,836,620 行**（10-06~10-07 的唯一副本 + 迁移 830 的回滚锚点）
//	- `session_title_states`            实际 **175,975 行**（迁移 550 建的活跃业务表）
//	- 9 月「空分区包」11 个              实际 **11/11 全部非空**
//	- 而所谓「orphan」                   实际 **relispartition 全为 f** ⇒ 全是普通表
//
// ⇒ ⇒ **照单执行会跨 17 张表不可逆销毁 300 万+ 行。**
//
// ⇒ ⇒ 根因不是「判断错了」，是**量具错了**：
//
//	① 用 `pg_stat_user_tables.n_live_tup` 判空 ⇒ 它对「长期无写入 + 从未 analyze」
//	   的表**恒为 0** ⇒ 系统性假空；
//	② 用「`pg_inherits` 里没有父」判孤儿 ⇒ 它对**普通表恒真** ⇒ 空命题。
//
// # 本门的三条职责
//
//	① **决策表自测**：把 §143.42 的判定规则逐条钉住（纯 Go、恒跑、无 DB）。
//	② **量具纪律静态断言**：判定脚本必须用 EXISTS 判空、必须用 relispartition 判孤儿、
//	   **不得**出现 n_live_tup ⇒ 「下一次有人再犯同样的错」在提交时就被拦住。
//	③ **登记表不可自说自话**：登记册每一行的 verdict 都由**同一套决策表**重算 ⇒
//	   改事实必须改判定，改判定必须改测试。
//
// # ★ 为什么判据要「剥注释」再断言
//
// §143.42 的纪律里有一条：**判据一旦能在注释里被满足就不是判据**。
// 若直接对脚本文本 grep `n_live_tup`，脚本头里那段「禁用 n_live_tup」的说明
// 就会把断言**喂饱** ⇒ 门恒绿 ⇒ 零守卫力。
// ⇒ 本门用一个**状态机**剥掉注释（含 `--` / `/* */` / 嵌套），再对剥完的文本断言。
import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	reclaimScriptPath   = "../scripts/audit/verify_reclaim_candidates.sql"
	reclaimRegistryPath = "../scripts/audit/reclaim_candidates.tsv"
	reclaimRunbookPath  = "../docs/06-deployment/04-runbooks/runbooks/" +
		"2026-10-04-ursm-write-amplification-measurement.md"
)

// ─────────────────────────────────────────────────────────────────────
// 剥注释器（状态机）
//
// ★ 必须处理：'...' 字符串（含 ” 与 \' 转义）、$tag$ 美元引用、
//
//	`--` 行注释、`/* */` 块注释（可嵌套）。
//
// ★ 只剥注释，**保留字符串字面量** —— 因为脚本里 `EXISTS` 出现在
//
//	format() 的字符串里，而那正是要断言的量具。
//
// ★★ 为什么**不**复用 `admin/source_sql_local_literal_test.go` 里已有的
//
//	`stripSQLComments`：那一个**按行**剥（不是引号感知的状态机）⇒
//	字符串字面量里的 `--` 会被误当注释起点。
//	⇒ 两者的适用面不同 ⇒ 本门自带一个状态机，**不改他人文件**。
//
// ─────────────────────────────────────────────────────────────────────
func stripSQLCommentsForReclaimGate(src string) string {
	var out strings.Builder
	i := 0
	n := len(src)
	for i < n {
		c := src[i]
		switch {
		case c == '-' && i+1 < n && src[i+1] == '-':
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && src[i+1] == '*':
			depth := 0
			for i < n {
				if i+1 < n && src[i] == '/' && src[i+1] == '*' {
					depth++
					i += 2
					continue
				}
				if i+1 < n && src[i] == '*' && src[i+1] == '/' {
					depth--
					i += 2
					if depth == 0 {
						break
					}
					continue
				}
				i++
			}
		case c == '\'':
			out.WriteByte(c)
			i++
			for i < n {
				out.WriteByte(src[i])
				if src[i] == '\\' && i+1 < n {
					i++
					out.WriteByte(src[i])
					i++
					continue
				}
				if src[i] == '\'' {
					i++
					// '' 是转义的单引号
					if i < n && src[i] == '\'' {
						out.WriteByte(src[i])
						i++
						continue
					}
					break
				}
				i++
			}
		case c == '$':
			// $tag$ ... $tag$
			j := i + 1
			for j < n && (src[j] == '_' ||
				(src[j] >= 'a' && src[j] <= 'z') ||
				(src[j] >= 'A' && src[j] <= 'Z') ||
				(src[j] >= '0' && src[j] <= '9')) {
				j++
			}
			if j < n && src[j] == '$' {
				tag := src[i : j+1]
				end := strings.Index(src[j+1:], tag)
				if end < 0 {
					out.WriteString(src[i:])
					return out.String()
				}
				out.WriteString(src[i : j+1+end+len(tag)])
				i = j + 1 + end + len(tag)
				continue
			}
			out.WriteByte(c)
			i++
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

// ─────────────────────────────────────────────────────────────────────
// ① 决策表自测
//
// ★ 这段是「当场把门逼正确过 3 次」的载体：每个用例都是一条**会真实发生**
//
//	的形态（2026-10-21 实测读数），而不是抽象的构造。
//
// ─────────────────────────────────────────────────────────────────────
func TestSection14342ReclaimVerdictDecisionTable(t *testing.T) {
	cases := []struct {
		name       string
		facts      ReclaimFacts
		wantVerdic string
		wantMethod string
	}{
		{
			name: "① 有行 ⇒ 不可回收（★ §143.40 的 17 个对象全落这里）",
			facts: ReclaimFacts{
				Object:     "ursm_node_snapshot_min_legacy",
				TotalBytes: 893845504, Empty: false,
				RelIsPartition: false, HasParent: false, FuncRefs: 0,
			},
			wantVerdic: ReclaimVerdictNone, wantMethod: ReclaimMethodNone,
		},
		{
			name: "② DEFAULT 兜底分区 ⇒ 不可回收（删了打断写入路径）",
			facts: ReclaimFacts{
				Object:     "usage_facts_default",
				TotalBytes: 83492864, Empty: true,
				RelIsPartition: true, HasParent: true, IsDefaultPartition: true, FuncRefs: 1,
			},
			wantVerdic: ReclaimVerdictNone, wantMethod: ReclaimMethodNone,
		},
		{
			name: "③ 普通表 + 0 行 + 有函数引用 ⇒ 保名回收（VACUUM FULL）",
			facts: ReclaimFacts{
				Object:     "_request_logs_bodies_hot",
				TotalBytes: 304734208, Empty: true,
				RelIsPartition: false, HasParent: false, FuncRefs: 1,
			},
			wantVerdic: ReclaimVerdictTruncate, wantMethod: ReclaimMethodVacuumFull,
		},
		{
			name: "④ 普通表 + 0 行 + 多函数引用 ⇒ 仍是保名回收（不是 DROP）",
			facts: ReclaimFacts{
				Object:     "_request_logs_hot",
				TotalBytes: 61603840, Empty: true,
				RelIsPartition: false, HasParent: false, FuncRefs: 6,
			},
			wantVerdic: ReclaimVerdictTruncate, wantMethod: ReclaimMethodVacuumFull,
		},
		{
			name: "⑤ 真孤儿分区 + 0 行 + 无引用 ⇒ 可 DROP",
			facts: ReclaimFacts{
				Object:     "some_detached_partition",
				TotalBytes: 1234567, Empty: true,
				RelIsPartition: true, HasParent: false, FuncRefs: 0,
			},
			wantVerdic: ReclaimVerdictDrop, wantMethod: ReclaimMethodDrop,
		},
		{
			name: "⑥ 真孤儿分区 + 0 行 + 有引用 ⇒ 降级为保名回收",
			facts: ReclaimFacts{
				Object:     "some_detached_partition_referenced",
				TotalBytes: 1234567, Empty: true,
				RelIsPartition: true, HasParent: false, FuncRefs: 2,
			},
			wantVerdic: ReclaimVerdictTruncate, wantMethod: ReclaimMethodVacuumFull,
		},
		{
			name: "⑦ 附着在父表下的分区 + 0 行 ⇒ 保名回收（父表还在，读面还在）",
			facts: ReclaimFacts{
				Object:     "some_attached_empty_partition",
				TotalBytes: 1024, Empty: true,
				RelIsPartition: true, HasParent: true, FuncRefs: 0,
			},
			wantVerdic: ReclaimVerdictTruncate, wantMethod: ReclaimMethodVacuumFull,
		},
		{
			// ★★ 这一例是**「relispartition 条件是否承重」的唯一探针**：
			// 若把决策表里的 `f.RelIsPartition && !f.HasParent` 改成 `!f.HasParent`，
			// 本例会从「保名回收」翻成「可 DROP」⇒ 门红。
			// ⇒ 少了它，`relispartition` 这个判据就**没有被任何用例钉住**
			//    （③④ 有函数引用、⑦ 是分区 ⇒ 三例都碰不到那个分支）。
			name: "⑧ 普通表 + 0 行 + 无函数引用 ⇒ 保名回收（★ 绝不能是 DROP）",
			facts: ReclaimFacts{
				Object:     "some_ordinary_empty_table",
				TotalBytes: 4096, Empty: true,
				RelIsPartition: false, HasParent: false, FuncRefs: 0,
			},
			wantVerdic: ReclaimVerdictTruncate, wantMethod: ReclaimMethodVacuumFull,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyReclaim(tc.facts)
			if got.Verdict != tc.wantVerdic {
				t.Errorf("verdict = %q, want %q（理由：%s）", got.Verdict, tc.wantVerdic, got.Reason)
			}
			if got.Method != tc.wantMethod {
				t.Errorf("method = %q, want %q（理由：%s）", got.Method, tc.wantMethod, got.Reason)
			}
			if got.Reason == "" {
				t.Error("reason 不能为空：每个判定都要能回答「凭什么」")
			}
			// ★ 不变式：不可回收 ⇒ 手段必须是 "-"，且反之亦然。
			if got.Verdict == ReclaimVerdictNone && got.Method != ReclaimMethodNone {
				t.Errorf("NOT_RECLAIMABLE 的手段必须是 %q，got %q", ReclaimMethodNone, got.Method)
			}
			if got.Verdict != ReclaimVerdictNone && got.Method == ReclaimMethodNone {
				t.Errorf("可回收 ⇒ 手段不能是 %q", ReclaimMethodNone)
			}
			// ★ 不变式：可回收的普通表**绝不能**被判成 DROP
			//   （普通表的「不在父表下」是恒真命题，不是孤儿证据）。
			if !tc.facts.RelIsPartition && got.Verdict == ReclaimVerdictDrop {
				t.Errorf("普通表（relispartition=false）被判成 %q ⇒ "+
					"「不在父表下」被当成了孤儿证据（§143.40 的误判形态）",
					ReclaimVerdictDrop)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────
// ② 量具纪律的静态断言
// ─────────────────────────────────────────────────────────────────────
func TestSection14342ReclaimScriptUsesExactMeasurement(t *testing.T) {
	raw, err := os.ReadFile(reclaimScriptPath)
	if err != nil {
		t.Fatalf("读判定脚本 %s 失败: %v", reclaimScriptPath, err)
	}
	code := stripSQLCommentsForReclaimGate(string(raw))

	// 纪律①：判空必须用 EXISTS(SELECT 1 FROM … LIMIT 1) 这个**精确探针形态**。
	//
	// ★★ 第一版这里只断言 `strings.Contains(code, "EXISTS")` ⇒ 被**变异 M3 打红失败**
	//   （门没牙）：`COMMENT ON FUNCTION … IS '…EXISTS+LIMIT 1…'` 这条
	//   **字符串字面量**照样喂饱了断言 ⇒ ★★ **剥掉 `--` 注释还不够**：
	//   **任何字符串字面量都能喂饱「存在类」断言** ⇒ ⇒ 存在类断言必须钉住
	//   **形态**（`EXISTS (SELECT 1 FROM`）而不是关键词（`EXISTS`）。
	// ⇒ 这也是「判据一旦能在注释里被满足就不是判据」的**第二个变体**：
	//   「在注释里」只是最常见的一种，**在字符串字面量里**同样成立。
	if !strings.Contains(code, "EXISTS (SELECT 1 FROM") {
		t.Error("★ 判定脚本里没有 `EXISTS (SELECT 1 FROM` 这个精确探针形态 ⇒ " +
			"判空没有用精确探针（§143.42 纪律①：EXISTS(SELECT 1 FROM t LIMIT 1)，" +
			"禁用 n_live_tup）⇒ ★ 断言必须钉住形态而不是关键词，" +
			"否则字符串字面量就能把它喂饱")
	}
	if !strings.Contains(code, "LIMIT 1") {
		t.Error("★ 判定脚本里没有 `LIMIT 1` ⇒ 判空探针没有界（EXISTS 必须是 O(1) 的）")
	}
	// 纪律①的禁令：n_live_tup 不得出现在剥完注释的脚本里。
	if strings.Contains(code, "n_live_tup") {
		t.Error("★ 判定脚本里出现了 n_live_tup ⇒ " +
			"它对「长期无写入 + 从未 analyze」的表恒为 0 ⇒ 会系统性假空（§143.42 纪律①）")
	}
	// 纪律②：孤儿判据必须是 relispartition。
	if !strings.Contains(code, "relispartition") {
		t.Error("★ 判定脚本里没有 relispartition ⇒ 孤儿判据不是「是否曾是分区」" +
			"（§143.42 纪律②：「pg_inherits 无父」对普通表恒真）")
	}
	// 纪律②的配套：必须能认出 DEFAULT 兜底分区。
	if !strings.Contains(code, "relpartbound") {
		t.Error("★ 判定脚本里没有 relpartbound ⇒ 认不出 DEFAULT 兜底分区 " +
			"⇒ 回收清单会把「删了会打断写入路径」的对象列进去")
	}
	// 只读性：脚本不得含任何破坏性语句。
	for _, bad := range []string{"DROP ", "TRUNCATE ", "DELETE ", "UPDATE ", "INSERT ", "ALTER ", "VACUUM "} {
		if strings.Contains(code, bad) {
			t.Errorf("★ 判定脚本里出现了 %q ⇒ 它必须是只读的", bad)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────
// ③ 登记表：逐行重算 + 绝对条数钉住 + 撤回对象必须不可回收
// ─────────────────────────────────────────────────────────────────────
type reclaimRegistryRow struct {
	object     string
	totalBytes int64
	empty      bool
	relIsPart  bool
	hasParent  bool
	isDefault  bool
	funcRefs   int
	verdict    string
	method     string
	note       string
}

func parseReclaimRegistry(t *testing.T) []reclaimRegistryRow {
	t.Helper()
	raw, err := os.ReadFile(reclaimRegistryPath)
	if err != nil {
		t.Fatalf("读登记表 %s 失败: %v", reclaimRegistryPath, err)
	}
	var rows []reclaimRegistryRow
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 10 {
			t.Fatalf("登记表列数不足（%d < 10）：%q", len(cols), line)
		}
		if cols[0] == "object" { // 表头
			continue
		}
		atoi := func(s string) int {
			n := 0
			neg := false
			for i, ch := range s {
				if i == 0 && ch == '-' {
					neg = true
					continue
				}
				if ch < '0' || ch > '9' {
					t.Fatalf("不是整数字段：%q", s)
				}
				n = n*10 + int(ch-'0')
			}
			if neg {
				return -n
			}
			return n
		}
		rows = append(rows, reclaimRegistryRow{
			object:     cols[0],
			totalBytes: int64(atoi(cols[1])),
			empty:      cols[2] == "YES",
			relIsPart:  cols[3] == "t",
			hasParent:  cols[4] == "t",
			isDefault:  cols[5] == "t",
			funcRefs:   atoi(cols[6]),
			verdict:    cols[7],
			method:     cols[8],
			note:       cols[9],
		})
	}
	return rows
}

func TestSection14342ReclaimRegistryVerdictsAreDerived(t *testing.T) {
	rows := parseReclaimRegistry(t)

	// ★ 绝对条数钉住：候选集一变 ⇒ 这里立刻红 ⇒ 逼人重新定性。
	const wantRows = 20
	if len(rows) != wantRows {
		t.Fatalf("登记表行数 = %d, want %d ⇒ 候选集变了，必须重新定性并更新本门",
			len(rows), wantRows)
	}

	var reclaimableBytes int64
	reclaimable := map[string]bool{}
	for _, r := range rows {
		got := ClassifyReclaim(ReclaimFacts{
			Object: r.object, TotalBytes: r.totalBytes, Empty: r.empty,
			RelIsPartition: r.relIsPart, HasParent: r.hasParent,
			IsDefaultPartition: r.isDefault, FuncRefs: r.funcRefs,
		})
		if got.Verdict != r.verdict {
			t.Errorf("%s: 登记册 verdict = %q，但决策表重算得 %q（理由：%s）⇒ "+
				"登记册与决策表不同源", r.object, r.verdict, got.Verdict, got.Reason)
		}
		if got.Method != r.method {
			t.Errorf("%s: 登记册 method = %q，但决策表重算得 %q", r.object, r.method, got.Method)
		}
		if got.Reclaimable() {
			reclaimableBytes += r.totalBytes
			reclaimable[r.object] = true
		}
	}

	// ★ §143.40 被撤回的对象必须仍在表里，且判为不可回收
	//   （「删除证据」是最容易被静默做掉的动作 ⇒ 门要盯着它们还在）。
	for _, withdrawn := range []string{
		"ursm_node_snapshot_min_legacy", // 2,836,620 行 + 迁移 830 回滚锚点
		"session_title_states",          // 175,975 行活跃业务表
		"usage_facts_default",           // DEFAULT 兜底分区
		"usage_ledger_2026_09",          // 1,416,772 行
		"session_turn_details_2026_09",  // 786,036 行
	} {
		found := false
		for _, r := range rows {
			if r.object == withdrawn {
				found = true
				if r.verdict != ReclaimVerdictNone {
					t.Errorf("★ %s 已被撤回（§143.40 的假读数）⇒ 必须保持 NOT_RECLAIMABLE，got %q",
						withdrawn, r.verdict)
				}
			}
		}
		if !found {
			t.Errorf("★ 被撤回的对象 %s 从登记表里消失了 ⇒ "+
				"要么它已被回收（属主授权？），要么证据被删了 ⇒ 两者都需要显式说明", withdrawn)
		}
	}

	// ★ 绝对字节数钉住：可回收集合 = 两个 v1 hot 表 = 350 MB。
	const wantReclaimableBytes = 366338048 // 304734208 + 61603840
	if reclaimableBytes != wantReclaimableBytes {
		t.Errorf("可回收字节合计 = %d, want %d（两个 v1 hot 表：291 MB + 59 MB）⇒ "+
			"回收集合变了，必须重新定性", reclaimableBytes, wantReclaimableBytes)
	}
	for _, want := range []string{"_request_logs_bodies_hot", "_request_logs_hot"} {
		if !reclaimable[want] {
			t.Errorf("★ %s 应在可回收集合里", want)
		}
	}
	// ★ 「可回收」不等于「可 DROP」：252 上当前**没有任何** DROP 安全的对象。
	for _, r := range rows {
		if r.verdict == ReclaimVerdictDrop {
			t.Errorf("★ %s 被判成 DROP 安全的 ⇒ 252 上真孤儿分区为 0 个（relispartition=true 且无父），"+
				"这个结论变了就说明形态变了，必须重新定性", r.object)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────
// ④ 文档在位（订正横幅二十 + §143.42）
// ─────────────────────────────────────────────────────────────────────
func TestSection14342RunbookHasSection(t *testing.T) {
	raw, err := os.ReadFile(reclaimRunbookPath)
	if err != nil {
		t.Fatalf("读 runbook 失败: %v", err)
	}
	body := string(raw)
	for _, needle := range []string{
		"§143.42",
		"订正横幅二十",
		"订正横幅二十一",
		"reclaim_candidates.tsv",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("runbook 里找不到 %q", needle)
		}
	}
	// ★★ **两个「订正横幅」都必须保留**：二十 = 并行会话 R79 轮独立复核的那一份，
	//   二十一 = 本轮那一份 ⇒ ⇒ 两者结论一致、互为佐证
	//   ⇒ ⇒ **删掉任何一边 = 抹掉一次独立复核的证据** ⇒ ⇒ 门盯着它们都在。
	//   ⚠ 查「同号横幅」时**不要**用 `^> \*\*订正横幅` 这种行首正则
	//   （R79 那条的形态是 `> 🚨🚨🚨 **订正横幅二十…` ⇒ 本轮就是这么漏掉它的）
	//   ⇒ ⇒ **按关键词查，不按行首格式查**。
	// ★ 订正横幅必须**就地**出现在 §143.40.3 之前（原文保留 + 横幅在前），
	//   否则读 §143.40.3 的人会先读到那份已被撤回的清单。
	idx143403 := strings.Index(body, "§143.40.3")
	if idx143403 < 0 {
		t.Fatal("runbook 里找不到 §143.40.3")
	}
	for _, banner := range []string{"订正横幅二十", "订正横幅二十一"} {
		idx := strings.Index(body, banner)
		if idx < 0 {
			t.Errorf("runbook 里找不到 %q", banner)
			continue
		}
		if idx > idx143403 {
			t.Errorf("★ %s（%d）必须排在 §143.40.3（%d）之前 ⇒ "+
				"否则读 §143.40.3 的人会先读到已被撤回的清单", banner, idx, idx143403)
		}
	}
	_ = filepath.Join
}
