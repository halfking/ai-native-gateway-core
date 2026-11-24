package sqlguard

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 本文件是「**迁移产物探针的作用域门**」—— 2026-11-17 F3 残留地平线根修轮
// 第三轮新增。
//
// # 背景（一次真实的部署失败）
//
//	2026-11-17 17:0x，211 部署被迁移 972 的产物探针打死：
//
//	  error: 台账 ↔ 产物对账失败：972_model_offers_trigger_recover_at.sql
//	         本轮按指纹认账但其产物缺失（… ⇒ f）
//	  [211] FATAL: DB 修订序列失败——直接启动会降级 lite，先修库再部署
//
//	而此刻库里**一切正常**：迁移 972 早在 16:41 就落地了（台账 applied_at
//	16:41:03、双账本 sha 齐备）、触发器函数体**确实**含新分支、存量清理
//	**确实**执行过。真正让探针判失败的，是探针自己多写的那半句：
//
//	  AND NOT EXISTS (SELECT 1 FROM public.credential_model_bindings
//	                  WHERE unavailable_recover_at IS NOT NULL
//	                    AND unavailable_at IS NULL AND available = TRUE)
//
//	⇒ 探针断言的是「**事实表当前有多少行**」——那是**运行时状态**，不是迁移产物。
//
// # 为什么必须分层（这就是本门要钉的东西）
//
//	产物探针（ledger_artifact_probe）的契约是：
//	  「本库是否已有该迁移的**产物**」（表/列/索引/约束/触发器/函数/种子行）。
//	它被用来决定「按台账认账（跳过重放）」还是「产物缺失（重放该迁移）」。
//
//	把运行时状态写进这个判据，会同时坏掉三件事：
//	  ① **判据不成立**：运行时状态由**运行中的二进制**决定，不由迁移决定。
//	     第二轮实测证伪了探针原注释的前提（「cmb 侧直写路径本就四列一起清 ⇒
//	     无第二条产生路径」）：modelcatalog/upsert.go / bg/model_probe.go /
//	     admin/credential_monitor.go 三条直写路径确实漏清第四列 ⇒ 迁移落地后
//	     残留从 0 反弹到 93 行（211 实测，凭据 99023 占 80 行）。
//	  ② **处置无效 ⇒ 毒丸**：探针失败时的处置建议是「重放本序列（迁移幂等）」，
//	     但重放消不掉由**运行中的旧二进制**重新制造的残留 ⇒ 迁移明明在位、
//	     产物明明齐备，却被一个与迁移无关的运行时量卡住部署 ⇒ 部署通道被死锁。
//	  ③ **静默的自我实现**：探针判据越写越像「迁移的效果」，于是每次为了让它
//	     通过就去改探针 ⇒ 探针逐渐变成一面写着「迁移没问题」的镜子。
//
//	⇒ 正确的分层：
//	  · **产物层**（ledger_artifact_probe）：只断言迁移自己造出来的对象。
//	     迁移 972 是**单事务**（函数替换 + 存量清理 + 台账登记同在一个
//	     BEGIN…COMMIT）⇒ 「函数体含新分支」成立即蕴含「存量清理已执行过」
//	     （原子性 ⇒ 两者同生共死，一个判据足矣）。
//	  · **写入侧层**（internal/sqlguard/credential_binding_health_tuple_gate_test.go）：
//	     全仓 AST 不变式门 ⇒ 「新的矛盾行不会被制造」由静态门保证。
//	  · **运行时层**（scripts/audit/f3-auto-check.sh 监控 +
//	     scripts/audit/f3-postdeploy-verify.sh 部署后验收）：残留归零与不再反弹
//	     由运行时判，不阻塞下一次部署。
//
// # 本门钉住什么
//
//	① 产物探针**不得**断言「运行时可变事实表的当前数据状态」
//	   （行级谓词），除非该探针上方有显式豁免注释且写明理由；
//	② 972 的探针**仍然必须断言产物**（触发器函数体含新分支）
//	   —— 防「把探针删掉/改成空断言」这种最省事的假修复；
//	③ 解析形态变化时**响亮失败**（而不是静默放行）。
//
// runtimeMutableFactTables ＝运行时可变事实表 → 理由。
//
// ★ 「事实表」＝ 记录业务事实（请求/绑定/计费）的表，写路径会持续改写它。
//
//	「产物」＝ 迁移自己创建或替换的对象（表/列/索引/约束/触发器/函数/种子行）。
var runtimeMutableFactTables = map[string]string{
	"credential_model_bindings": "节点×模型绑定的事实表：目录批量刷新、探针恢复、手工上线都会改写它 " +
		"⇒「当前有多少矛盾行」是运行时状态，不是任何迁移的产物",
}

// probeCallRe ＝产物探针调用的**行锚**形态：
//
//	ledger_artifact_probe "<迁移文件名>" \
//	  "<产物断言 SQL>"
var probeCallRe = regexp.MustCompile(`ledger_artifact_probe\s+"([^"]+)"`)

// probeSQLLineRe ＝断言 SQL 所在行的形态（整行被一对双引号包住）。
var probeSQLLineRe = regexp.MustCompile(`^\s*"(.+)"\s*$`)

// probeExemptRe ＝豁免注释的形态：
//
//	# probe-exempt: <表名> — <理由（≥10 字）>
var probeExemptRe = regexp.MustCompile(`probe-exempt:\s*([a-zA-Z0-9_.]+)\s*[—–-]\s*(.+)`)

// probeExemptMinReason ＝豁免理由的最短长度（字）。
const probeExemptMinReason = 10

type probeCall struct {
	name    string
	sql     string
	line    int
	comment string // 调用点上方连续的注释块（供豁免检查用）
}

// parseArtifactProbes ＝把部署通道脚本里的产物探针逐个解析出来。
//
// ★ 解析失败**响亮失败**：形态变了（换行、换引号、参数顺序变）时必须让人知道，
//
//	而不是「解析不出来 ⇒ 当作没有违规」⇒ 门静默失效。
func parseArtifactProbes(t *testing.T, text string) []probeCall {
	t.Helper()
	lines := strings.Split(text, "\n")
	var calls []probeCall
	for i, line := range lines {
		// ★ 2026-11-24：**注释行里出现 probe 字样不算调用点** ——
		// 部署脚本头部的大段事故注释（看板真源统一轮）逐字引用了
		// `ledger_artifact_probe "<file>" \` 的形态 ⇒ 若不跳过注释行，
		// 解析器把它当调用点、找不到断言 SQL 行 ⇒ 整个探针门崩
		//（origin/main 上即红，先于本轮）。注释行不携带任何调用语义。
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		m := probeCallRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		// 断言 SQL 在后续某一行（形态：`\` 续行 + 一整行双引号）。
		sql := ""
		sqlLine := -1
		for j := i + 1; j < len(lines) && j <= i+4; j++ {
			if sm := probeSQLLineRe.FindStringSubmatch(lines[j]); sm != nil {
				sql = sm[1]
				sqlLine = j
				break
			}
			// 中间若出现非续行内容 ⇒ 形态变了。
			trimmed := strings.TrimSpace(lines[j])
			if trimmed != "" && !strings.HasSuffix(trimmed, `\`) && !strings.HasPrefix(trimmed, `"`) {
				break
			}
		}
		if sql == "" {
			t.Fatalf("解析产物探针 %q（第 %d 行）失败：找不到断言 SQL 行。\n"+
				"★ 判据形态变了（换行/引号/续行方式）—— 必须同步改本门，"+
				"否则门会静默失效（解析不出来 ≠ 没有违规）。", m[1], i+1)
		}
		// 调用点上方连续的注释块。
		var comments []string
		for k := i - 1; k >= 0; k-- {
			trimmed := strings.TrimSpace(lines[k])
			if strings.HasPrefix(trimmed, "#") {
				comments = append([]string{lines[k]}, comments...)
				continue
			}
			break
		}
		calls = append(calls, probeCall{
			name:    m[1],
			sql:     sql,
			line:    sqlLine + 1,
			comment: strings.Join(comments, "\n"),
		})
	}
	if len(calls) == 0 {
		t.Fatalf("在 %s 里解析不到任何 ledger_artifact_probe 调用 ⇒ 判据形态坏了", revisionSequenceScriptPath)
	}
	return calls
}

// sqlReferencesTable ＝SQL 是否对某张表做了**行级**引用（FROM / JOIN）。
//
// ★ 只看 FROM / JOIN：这些才是「查表里的行」；而 to_regclass('x') /
//
//	pg_index / pg_proc / information_schema 都是**目录级**断言（属产物层）。
func sqlReferencesTable(sql, table string) bool {
	low := strings.ToLower(sql)
	for _, kw := range []string{"from ", "join "} {
		needle := kw + table
		for _, variant := range []string{
			needle,
			kw + "public." + table,
		} {
			if strings.Contains(low, variant) {
				// 排除 "from public.x_other" 这类更长表名误命中。
				idx := strings.Index(low, variant)
				for idx >= 0 {
					end := idx + len(variant)
					if end >= len(low) || !(isIdentByte(low[end])) {
						return true
					}
					idx = strings.Index(low[idx+1:], variant)
					if idx >= 0 {
						idx++
					}
				}
			}
		}
	}
	return false
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// TestArtifactProbesDoNotAssertRuntimeMutableFactState —— ① 作用域门（本门核心）。
//
// ★ 这条门是 2026-11-17 那次「探针把部署打死」的**防复发**门。
func TestArtifactProbesDoNotAssertRuntimeMutableFactState(t *testing.T) {
	raw, err := os.ReadFile(revisionSequenceScriptPath)
	if err != nil {
		t.Fatalf("读部署通道脚本 %s：%v", revisionSequenceScriptPath, err)
	}
	calls := parseArtifactProbes(t, string(raw))

	violations := 0
	for _, c := range calls {
		for table, reason := range runtimeMutableFactTables {
			if !sqlReferencesTable(c.sql, table) {
				continue
			}
			// 豁免检查：调用点上方必须有 probe-exempt 注释 + 表名 + 足够长的理由。
			exempt := false
			for _, em := range probeExemptRe.FindAllStringSubmatch(c.comment, -1) {
				if em[1] != table {
					continue
				}
				if len([]rune(strings.TrimSpace(em[2]))) < probeExemptMinReason {
					t.Errorf("产物探针 %q 对事实表 %q 的豁免理由太短（%d 字 < %d）：%q\n"+
						"★ 豁免必须写清「为什么这条运行时断言对这条迁移是产物级判据」。",
						c.name, table, len([]rune(strings.TrimSpace(em[2]))), probeExemptMinReason, em[2])
					continue
				}
				exempt = true
			}
			if exempt {
				continue
			}
			violations++
			t.Errorf("产物探针 %q（脚本第 %d 行）断言了**运行时可变事实表** %q 的当前数据状态。\n"+
				"★ 这不是迁移的产物，是运行中二进制的行为 ⇒ 它会随写路径变化 ⇒\n"+
				"  ① 探针判据不成立（迁移在位也可能判失败）；\n"+
				"  ② 处置无效（重放迁移消不掉运行中二进制重新制造的行）⇒ 部署被死锁；\n"+
				"  ③ 2026-11-17 211 实测已被它打死一次（FATAL：台账 ↔ 产物对账失败）。\n"+
				"  该表进登记表的理由：%s\n"+
				"  ⇒ 正确做法：产物层只断言迁移自己造出来的对象；运行时判据放运行时层\n"+
				"    （scripts/audit/f3-auto-check.sh 监控 + scripts/audit/f3-postdeploy-verify.sh\n"+
				"    部署后验收）；确需断言时在上方加 `# probe-exempt: %s — <理由≥%d字>`。\n"+
				"  探针 SQL：%s",
				c.name, c.line, table, reason, table, probeExemptMinReason, c.sql)
		}
	}
	if violations == 0 {
		t.Logf("已解析 %d 条产物探针，无作用域违规", len(calls))
	}
}

// TestM972ArtifactProbeStillAssertsTheArtifact —— ② 972 的探针仍必须断言产物。
//
// ★ 防「把探针删掉 / 改成永远为真」这种最省事的假修复：
//
//	上一轮我们把「残留行为零」那一半摘掉了（它是运行时状态），但**产物那一半**
//	必须留着 —— 否则「台账说已应用但产物不在场」这一形态就没人对账了。
func TestM972ArtifactProbeStillAssertsTheArtifact(t *testing.T) {
	raw, err := os.ReadFile(revisionSequenceScriptPath)
	if err != nil {
		t.Fatalf("读部署通道脚本 %s：%v", revisionSequenceScriptPath, err)
	}
	calls := parseArtifactProbes(t, string(raw))

	const want = "972_model_offers_trigger_recover_at.sql"
	var found *probeCall
	for i := range calls {
		if calls[i].name == want {
			found = &calls[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("部署通道里没有 %q 的产物探针 ⇒ 「台账说已应用但产物不在场」无人对账。\n"+
			"★ 摘掉的是「运行时残留数」那一半，**产物那一半不许摘**。", want)
	}
	for _, must := range []string{
		"model_offers_update_trigger",
		"unavailable_recover_at = CASE",
	} {
		if !strings.Contains(found.sql, must) {
			t.Errorf("972 的产物探针 SQL 里缺 %q ⇒ 它不再断言「触发器函数体含新分支」这一产物。\n"+
				"实际 SQL：%s", must, found.sql)
		}
	}
	// ★ 反向断言：不得再出现「库内当前无残留行」这一半（运行时状态）。
	if sqlReferencesTable(found.sql, "credential_model_bindings") {
		t.Errorf("972 的产物探针 SQL 里又出现了对 credential_model_bindings 的行级断言：\n%s\n"+
			"★ 那是运行时状态 ⇒ 会把部署死锁（2026-11-17 已实测一次）。", found.sql)
	}
}
