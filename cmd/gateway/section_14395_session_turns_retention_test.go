//go:build !integration

package main

// §143.95 的门：★ 本轮是【session_turns 族留存归属 + hot 表排水归属】轮
// ⇒ ⇒ 产出是：
//   ① 「PG 侧 session_turns 的行级留存」= **无**（§143.94 标「未测」，本轮测了）
//   ② 「session_turns_hot 由谁清、何时清」= **有**（promote，1h 周期 / 8h 保留窗），
//      但**自 2026-10-23 08:47 起每 tick 必败** ⇒ 不是「没人管」，是「管了但每 tick 都死」
//   ③ 根因：promote 的去重键 (tenant_id, request_id) 与父表部分唯一索引的键
//      (tenant_id, session_id, partition_date) WHERE is_final_success **不是同一个键**
//      ⇒ 冲突看不见 ⇒ INSERT 撞 23505 ⇒ **整批回滚、零行搬走**
//
// ★★★★★★ 这个门要守的是【四件不许被无声改掉的事】：
//
//	① ★★★★★★ 【两个「冲突」必须分开量】—— ★ promote 自己的去重键与父表
//	    唯一索引键是**两个不同的键** ⇒ ★ 混用 ⇒ 阴性结论整个翻转
//	    （本轮第一次就查错了：按 (tenant_id, request_id) 查冲突恒得 0）
//	② ★★★★★★ 【排水判据不许被改写】—— ★ `h.ts < statement_timestamp() - p_retention`
//	    + Go 侧 8h 保留窗 + 5000 行批 ⇒ 少了任一条 ⇒ 「hot 由谁清、何时清」无从复算
//	③ ★★★★★★ 【父表饿死的判据不许被删】—— ★ `n_tup_ins` / `n_tup_upd` 的对照
//	    ⇒ 这是「promote 是父表的唯一写入路径」的唯一可复算证据
//	④ ★★★★ 【修法样板不许消失】—— ★ 迁移 695（final_success 自愈）在位
//	    ⇒ ★ 它护的 v1 族已被 890 退役 ⇒ ⇒ 样板在、但**保护对象没了**
//	    ⇒ ⇒ 会话族（真正有问题的那个）没有自愈 ⇒ ★★ 这就是本轮的根因形状
//
// ★ ★★ 纪律提醒：★ 本门是【登记门 + 工具形态门】，★ **不是**端到端门。
// ★ ★★ 判据一律查【可执行面】（★ 剥掉 `--` 注释与 `\echo` 元命令）⇒ ⇒
// ★ ★★ 否则「在注释里写出那行」就会假绿。
// ★ ★★★ **本门不钉「活数字」**（hot 的行数/体积每小时都在涨 ⇒ 钉它等于钉一个
// ★ ★★★ 明天就不一样的数字）⇒ ⇒ 只钉【稳定标识】+【结构形态】+【文档登记】。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	section14395Runbook     = "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md"
	section14395Lessons     = "LESSONS.md"
	section14395Agents      = "AGENTS.md"
	section14395Script      = "scripts/audit/verify_session_turns_retention_ownership.sql"
	section14395Partition   = "bg/partition_manager.go"
	section14395PromoteBase = "sql/migrations/startup/869_promote_session_turns_trace_events.sql"
	section14395SelfHeal    = "sql/migrations/startup/695_request_logs_promote_final_success_self_heal.sql"
	section14395V1Retire    = "sql/migrations/startup/890_drop_request_logs_v1_family.sql"
	section14395UniqueIdx   = "sql/migrations/startup/707_session_turns_s1a.sql"
)

func repoRoot14395(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14395(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14395(t), rel))
	if err != nil {
		t.Fatalf("★ 读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

func sqlNonComment14395(t *testing.T, rel string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(readRepoFile14395(t, rel), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func sqlExecutable14395(t *testing.T, rel string) string {
	t.Helper()
	var out []string
	for _, line := range sqlNonComment14395(t, rel) {
		if strings.HasPrefix(line, `\`) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// sqlExecutableNoLiterals14395 去掉**字符串字面量**的内容后剩下的可执行面。
//
// ★★★★ 为什么需要这一层：★ 本轮的审计脚本第 9c 段要查
// 「pg_stat_statements 里有没有 DELETE/TRUNCATE session_turns 的语句」，
// 而那个关键字**只出现在字符串字面量里** ⇒ ⇒ 只读门若不剥字面量，
// 会把这条**纯 SELECT** 误判成写操作 ⇒ ⇒ ⇒ 门自己会红。
// ⇒ ⇒ 剥掉字面量后，真正的写/DDL（它们不含字面量）照样被抓住。
func sqlExecutableNoLiterals14395(t *testing.T, rel string) string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(sqlExecutable14395(t, rel), "\n") {
		out = append(out, literal14395.ReplaceAllString(line, "EMPTY"))
	}
	return strings.Join(out, "\n")
}

// literal14395 匹配单引号字符串字面量（内部成对出现的两个单引号按转义处理）。
var literal14395 = regexp.MustCompile(`'(?:[^']|'')*'`)

// TestSection14395ScriptExistsAndHasTenStages —— ★ 判据①的前置
// isIdentChar14395 判定一个字节是否算「标识符字符」。
//
// ★★ `_` 与 `-` **都**算 ⇒ ★ 因为本轮的变异形态正是「改名成后缀版」
// ★（★ `..._x` / ★ `...-x`）⇒ ⇒ ⇒ 若把它们当边界 ⇒ ⇒ 变异照样逃过。
func isIdentChar14395(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-':
		return true
	}
	return false
}

// containsToken14395 是【带边界的 token 搜索】—— ★ 治「改名成后缀版」的逃逸。
//
// ★★★★★★ 为什么必须带边界（★ ★★★ 本轮第 4 次复发：★ §143.92 M7/M9、
// ★ §143.93 M1/M2/M4/M9、★ §143.94 M1/M2/M3/M5/M6/M8、★ 本轮 M1/M8/M11/M12/M15）：
//
// ★ 判据用【裸子串】查 ⇒ ★ 变异把名字改成 `..._x` 后 ★ **原 needle 仍是它的前缀**
// ★ ⇒ ⇒ `strings.Contains` 照样命中 ⇒ ⇒ **门没有牙**。
//
// ★ ⇒ 边界形态必须与【判据实际读的那一面】一致 ⇒ ★ 前后都不能是标识符字符。
func containsToken14395(s, tok string) bool {
	if tok == "" {
		return false
	}
	for i := 0; i+len(tok) <= len(s); {
		j := strings.Index(s[i:], tok)
		if j < 0 {
			return false
		}
		p := i + j
		end := p + len(tok)
		beforeOK := p == 0 || !isIdentChar14395(s[p-1])
		afterOK := end == len(s) || !isIdentChar14395(s[end])
		if beforeOK && afterOK {
			return true
		}
		i = p + 1
	}
	return false
}

func TestSection14395ScriptExistsAndHasTenStages(t *testing.T) {
	n := 0
	for _, line := range sqlNonComment14395(t, section14395Script) {
		// ★ 只数【带编号的段头】（`\echo '=== N/M: ...'`）⇒ ★ 空分隔行（`\echo ''`）不算段。
		if strings.HasPrefix(line, `\echo '=== `) {
			n++
		}
	}
	if n != 10 {
		t.Errorf("★ %s 里有 %d 个带编号的（非注释的）`\\echo` 段（期望 10）—— "+
			"★ ★★ 本轮的可复现复算是【10 段】：① 族清单 ② hot 现状 ③ 8h 保留窗实测 "+
			"④ 两种冲突分开量 ⑤ 23505 冲突集 ⑥ hot 侧 fs 形态 "+
			"⑦ 父表/hot 累计计数 ⑧ 部分唯一索引 ⑨ 留存三件套+行级留存+活读者 ⑩ 前置自检"+
			"★ ⇒ ⇒ ★★ 少一段 ⇒ ⇒ 那个坑就没有被钉住"+
			"★ ⇒ ⇒ ★★ 且【空分隔用的 `\\echo ''` 不算一段】",
			section14395Script, n)
	}
}

// TestSection14395TwoConflictKeysAreSeparated —— ★★ 判据①（★ 本轮的核心形态）
//
// ★★★★★★ 【promote 自己的去重键 ≠ 父表唯一索引键】—— ★★★
//
//	① promote 自己的去重键：**(tenant_id, request_id)**
//	   （批次选取带 NOT EXISTS，按「同一个请求已搬过」去重）
//	② 父表部分唯一索引的键：**(tenant_id, session_id, partition_date) WHERE is_final_success**
//	   （`uq_session_turns_final_success`）
//
// ★ 本轮实测：按 ① 查冲突 = **0 行**（⇒「没有冲突」的假阴性）；
//
//	按 ② 查冲突 = **1 个键**（⇒ 23505 的真凶）。
//
// ★⇒⇒ 结论方向整个翻转 ⇒ ★★ 必须**同时**出现在脚本里，且键必须逐字不同。
func TestSection14395TwoConflictKeysAreSeparated(t *testing.T) {
	exe := sqlExecutable14395(t, section14395Script)

	// ★ ① promote 自己的去重键（必须逐字，含两列的顺序）
	if !containsToken14395(exe, "a.request_id = s.request_id AND a.tenant_id = s.tenant_id") {
		t.Errorf("★ %s 的【可执行面】里没有 promote 自己的去重键探针 "+
			"`a.request_id = s.request_id AND a.tenant_id = s.tenant_id` —— "+
			"★ ★★★★ 本轮实测：★ 按这个键查冲突恒得 **0 行**"+
			"★ ⇒ ⇒ ★★ 那不是「没有冲突」，★ 那是【查错了键】"+
			"★ ⇒ ⇒ ★★ 少了它 ⇒ ⇒ 「冲突 = 0」这个假阴性会继续被引用",
			section14395Script)
	}
	if !containsToken14395(exe, "AS dedup_key_conflict_rows;") {
		t.Errorf("★ %s 的【可执行面】里没有 `AS dedup_key_conflict_rows;` —— "+
			"★ ★★ 「promote 自己的去重键」这一路的读数必须【单独成列】，"+
			"★ ⇒ ⇒ 否则读者会把它和另一路混成一个「冲突数」",
			section14395Script)
	}

	// ★ ② 父表唯一索引键 —— 必须逐字带 is_final_success + session_id + partition_date
	if !containsToken14395(exe, "AND p.is_final_success IS TRUE") {
		t.Errorf("★ %s 的【可执行面】里没有 `AND p.is_final_success IS TRUE` —— "+
			"★ ★★★★ 【23505 的真凶】是父表的部分唯一索引 "+
			"`uq_session_turns_final_success`（tenant_id, session_id, partition_date）"+
			"★ WHERE is_final_success ⇒ ⇒ 少了它 ⇒ ⇒ 冲突集根本查不出来",
			section14395Script)
	}
	if !containsToken14395(exe, "AS parent_fs_rows") {
		t.Errorf("★ %s 的【可执行面】里没有 `AS parent_fs_rows` —— "+
			"★ ★★ 冲突键的两侧计数（hot_fs_rows / parent_fs_rows）必须成对出现，"+
			"★ ⇒ ⇒ 只数一侧 ⇒ ⇒ 读不出「两侧各有一个 mark」这个事实",
			section14395Script)
	}

	// ★★ ② 的【键的列】必须逐字在 —— ★ 这一条正是本轮【第一次查错】的地方：
	//   ★ 把父表探针的键从 (tenant_id, session_id, partition_date)
	//     改回 promote 自己的 (tenant_id, request_id) ⇒ ⇒ 冲突恒为 0。
	if !containsToken14395(exe, "AND p.session_id = h.session_id") {
		t.Errorf("★ %s 的【可执行面】里没有 `AND p.session_id = h.session_id` —— "+
			"★ ★★★★ 父表探针的键必须是 **(tenant_id, session_id, partition_date)**"+
			"★ ⇒ ⇒ ★★ 换成 `request_id` ⇒ ⇒ 查出来恒为 0（★ 本轮第一次就是这么查错的）"+
			"★ ⇒ ⇒ ★★ 少了这一列 ⇒ ⇒ 那个假阴性会回来",
			section14395Script)
	}
	if !containsToken14395(exe, "AND p.partition_date = h.partition_date") {
		t.Errorf("★ %s 的【可执行面】里没有 `AND p.partition_date = h.partition_date` —— "+
			"★ ★★★★ 父表探针的键必须带 partition_date（★ 唯一索引的第三列）"+
			"★ ⇒ ⇒ ★★ 少了它 ⇒ ⇒ 跨 UTC 日的同会话会被当成同一个键",
			section14395Script)
	}

	// ★★★ 反向禁：碰撞判据**不得**用 `success`（那是请求成功标记，不是 fs 标记）
	//   —— 本轮第一次就是用它读数（2,404 / 1,526），完全张冠李戴。
	for _, bad := range []string{
		"AND p.success)",
		"AND x.success)",
		"AS hot_success",
		"AS parent_success",
	} {
		if containsToken14395(exe, bad) {
			t.Errorf("★ %s 的【可执行面】里出现了 `%s` —— "+
				"★ ★★★★ `success`（请求成功标记）≠ `is_final_success`（该会话唯一最终成功标记）"+
				"★ ⇒ ⇒ ★★ 本轮第一次用错列，读出「2,404 / 1,526」这种完全不相干的数"+
				"★ ⇒ ⇒ ★★ ★ 碰撞判据必须写死 is_final_success",
				section14395Script, bad)
		}
	}
}

// TestSection14395DrainRulerIsPinned —— ★★ 判据②（★ hot 由谁清、何时清）
//
// ★★★★★★ 【排水判据必须逐字钉住三处】—— ★★★
//
//	① SQL 侧：`h.ts < statement_timestamp() - p_retention`（869 的函数体）
//	② Go 侧保留窗：`DefaultRetentionWindow = 8 * time.Hour`（resolvePromoteConfig 的
//	   default 分支 —— `session_turns_hot` 不在 switch 的任何 case 里 ⇒ 落这里）
//	③ Go 侧批大小：`promoteBatchSize = 5000`
//
// ★★ 这三条合起来 = 「hot 表由谁清、何时清、能清多少」的完整答案。
func TestSection14395DrainRulerIsPinned(t *testing.T) {
	// ① SQL 侧：基线迁移 869 的函数体
	base := sqlExecutable14395(t, section14395PromoteBase)
	if !containsToken14395(base, "CREATE OR REPLACE FUNCTION public.promote_session_turns_hot_to_partition(") {
		t.Errorf("★ %s 里没有 `CREATE OR REPLACE FUNCTION public.promote_session_turns_hot_to_partition(` —— "+
			"★ ★★★★ 869 是**生产现行**的 promote 函数体"+
			"★ ⇒ ⇒ ★★ 任何修复都必须以 869 为基线（★ 不是 526/636/640/688/707）",
			section14395PromoteBase)
	}
	if !containsToken14395(base, "WHERE h.ts < statement_timestamp() - p_retention") {
		t.Errorf("★ %s 的【可执行面】里没有 `WHERE h.ts < statement_timestamp() - p_retention` —— "+
			"★ ★★★★ promote 的排水判据是「ts 比保留窗老」"+
			"★ ⇒ ⇒ ★★ 判据一改 ⇒ ⇒ 「8h 不变式」的口径就变了",
			section14395PromoteBase)
	}

	// ② ③ Go 侧常量
	pm := readRepoFile14395(t, section14395Partition)
	for _, needle := range []string{
		"const DefaultRetentionWindow = 8 * time.Hour",
		"const promoteBatchSize = 5000",
	} {
		if !containsToken14395(pm, needle) {
			t.Errorf("★ %s 里没有逐字的 `%s` —— "+
				"★ ★★★★ `session_turns_hot` 不在 resolvePromoteConfig 的任何 case 里"+
				"★ ⇒ ⇒ ★★ 它落 default 分支 ⇒ 保留窗 = lifecycle.hot_retention_hours"+
				"★ ⇒ ⇒ ★★ 默认为 DefaultRetentionWindow = 8h"+
				"★ ⇒ ⇒ ★★ ★ 生产的 `settings_kv` 实测 `lifecycle.hot_retention_hours = 8`"+
				"★ ⇒ ⇒ ★★ ⇒ 这两个常量就是「清多快/清多少」的答案",
				section14395Partition, needle)
		}
	}

	// ★ 审计脚本里必须真的按 8h 量「滞留行」
	exe := sqlExecutable14395(t, section14395Script)
	if !containsToken14395(exe, "count(*) FILTER (WHERE ts <  now() - interval '8 hours') AS older_than_8h,") {
		t.Errorf("★ %s 的【可执行面】里没有 `count(*) FILTER (WHERE ts <  now() - interval '8 hours') AS older_than_8h,` —— "+
			"★ ★★★★ 「hot 表里还留着超过 8h 的行」= 排水没做完"+
			"★ ⇒ ⇒ ★★ 本轮实测 2,843 行滞留（★ 且每小时还在涨）"+
			"★ ⇒ ⇒ ★★ 少这一列 ⇒ ⇒ 「排水停摆」就没有可复算的读数",
			section14395Script)
	}
}

// TestSection14395ParentStarvationRulerIsPinned —— ★★ 判据③
//
// ★★★★★★ 【父表饿死的判据 = hot 与父表的累计写入计数对照】—— ★★★
//
// ★ n_tup_ins 是**累计计数器**（不受 `pg_stat_user_tables` 快照过期影响）——
// ★ 而 n_live_tup 是快照 ⇒ 「= 0」≠ 空表（★ §143.92 的教训）。
//
// ★ 本轮实测：hot n_tup_ins = 210,763 ≈ 父表 2026_10 n_tup_ins = 212,347
// ⇒ ⇒ **父表的唯一写入路径就是 promote** ⇒ ⇒ promote 一停 ⇒ 父表彻底饿死。
func TestSection14395ParentStarvationRulerIsPinned(t *testing.T) {
	exe := sqlExecutable14395(t, section14395Script)
	for _, needle := range []string{
		"n_tup_ins, n_tup_upd, n_tup_del, n_live_tup, idx_scan, seq_scan",
		"FROM pg_stat_user_tables",
	} {
		if !containsToken14395(exe, needle) {
			t.Errorf("★ %s 的【可执行面】里没有逐字的 `%s` —— "+
				"★ ★★★★ 「父表写入 = promote」是本轮最关键的一条因果"+
				"★ ⇒ ⇒ ★★ 它的唯一可复算证据就是【累计写入计数对照】"+
				"★ ⇒ ⇒ ★★ 少了它 ⇒ ⇒ 「父表在饿死」就退回成一个断言",
				section14395Script, needle)
		}
	}
}

// TestSection14395UniqueIndexAndSelfHealAreRegistered —— ★★ 判据④（★ 根因的形状）
//
// ★★★★★★ 【23505 的来源索引 + 修法样板 + 样板失去保护对象】三件事必须同时在位。★★★
//
//	① `uq_session_turns_final_success`（707 建，父表侧；hot 侧同构索引同键）
//	② 迁移 695 = final_success 自愈的**现成样板**（含 demote 步）
//	③ 迁移 890 = v1 request_logs 族退役 ⇒ ⇒ **695 护的对象没了**
//
// ★★★ 本轮的中心结论就是这三者的**组合**：
//
//	样板在、但它护的族已退役 ⇒ ⇒ 真正有同样病灶的会话族**没有**自愈。
func TestSection14395UniqueIndexAndSelfHealAreRegistered(t *testing.T) {
	// ① 部分唯一索引
	idx := sqlExecutable14395(t, section14395UniqueIdx)
	if !containsToken14395(idx, "uq_session_turns_final_success") {
		t.Errorf("★ %s 的【可执行面】里没有 `uq_session_turns_final_success` —— "+
			"★ ★★★★ 23505 撞的就是它"+
			"★ ⇒ ⇒ ★★ 它的键是 (tenant_id, session_id, partition_date) WHERE is_final_success",
			section14395UniqueIdx)
	}
	exe := sqlExecutable14395(t, section14395Script)
	if !containsToken14395(exe, "pg_get_indexdef(c.oid) LIKE '%is_final_success%'") {
		t.Errorf("★ %s 的【可执行面】里没有 `pg_get_indexdef(c.oid) LIKE '%%is_final_success%%'` —— "+
			"★ ★★ 审计脚本必须能**列出**那条部分唯一索引（★ 它的定义就是根因）",
			section14395Script)
	}

	// ② 修法样板：695 的 demote 步
	heal := sqlExecutable14395(t, section14395SelfHeal)
	if !containsToken14395(heal, "SET is_final_success = FALSE") {
		t.Errorf("★ %s 里没有 `SET is_final_success = FALSE` —— "+
			"★ ★★★★ 迁移 695 是【final_success 自愈】的现成样板"+
			"★ ⇒ ⇒ ★★ 本轮实测：v1 request_logs 族 2026-09-10 起的停摆，"+
			"★ ★★ 就是被同款 23505 打死的（★ 头注释逐字记录了："+
			"★ ★★ 「整批回滚零搬运」+「批次按 ts 升序 ⇒ 冲突行始终在首批 ⇒ 每个 tick 必败」）"+
			"★ ⇒ ⇒ ★★ 695 用【在原子 CTE 之前 demote 掉输家】把它修好了",
			section14395SelfHeal)
	}

	// ③ 样板失去保护对象（v1 族退役）
	v1 := sqlExecutable14395(t, section14395V1Retire)
	if !containsToken14395(v1, "request_logs") {
		t.Errorf("★ %s 里没有 `request_logs` —— "+
			"★ ★★★★ 890 是 v1 request_logs 族的退役迁移"+
			"★ ⇒ ⇒ ★★ ★ 它退役之后，695 的自愈就【没有保护对象】了"+
			"★ ⇒ ⇒ ★★ ⇒ 而会话族（真正有同样病灶的那个）没有自愈"+
			"★ ⇒ ⇒ ★★ ★★ 这就是本轮根因的形状：【copy-gap】",
			section14395V1Retire)
	}
}

// TestSection14395ScriptIsReadOnly —— ★ 判据⑤
//
// ★★★★★★ 【脚本必须只读】—— ★ 不许出现写 / DDL 关键字。
var forbidden14395 = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|alter|truncate|vacuum|analyze|reindex|cluster|refresh|merge|grant|revoke|copy|create|call|do)\b`)

func TestSection14395ScriptIsReadOnly(t *testing.T) {
	// ★★ 用【剥掉字符串字面量】的那一面：★ 第 9c 段要查
	// 「有没有 DELETE/TRUNCATE session_turns 的语句」⇒ 那个关键字**只在字面量里**
	// ⇒ ⇒ 不剥字面量 ⇒ 这条纯 SELECT 会被误判成写操作。
	exe := sqlExecutableNoLiterals14395(t, section14395Script)
	for i, line := range strings.Split(exe, "\n") {
		if forbidden14395.MatchString(line) {
			t.Errorf("★ %s 的可执行面第 %d 行出现了写 / DDL 关键字：%q —— "+
				"★ ★★★★ 本脚本是【只读复算】⇒ ⇒ 生产上重复跑它必须绝对安全"+
				"★ ⇒ ⇒ ★★ 只允许 SELECT 与 `SET statement_timeout`",
				section14395Script, i+1, line)
		}
	}
	for _, line := range strings.Split(exe, "\n") {
		if strings.HasPrefix(strings.ToUpper(line), "SET ") &&
			!strings.HasPrefix(strings.ToUpper(line), "SET STATEMENT_TIMEOUT") {
			t.Errorf("★ %s 的可执行面出现了非 `SET statement_timeout` 的 SET：%q —— "+
				"★ ★★ 只读脚本的 SET 面要收敛", section14395Script, line)
		}
	}
}

// TestSection14395ScriptStripsCommentsBeforeMatching —— ★★ 判据⑤的配套
//
// ★★★★ 【pg_stat_statements 侧匹配必须先剥注释】—— ★★★
//
// ★ 本轮踩到：凭据自检的 SQL 文本里**注释中**提到
//
//	`promote_session_turns_hot_to_partition` ⇒ 裸 ILIKE 把它当成 promote 的调用记录
//	⇒ ⇒ 读数完全张冠李戴。
//
// ⇒ ★ 判据必须落在【剥掉 `--` 注释之后】的那一面。
func TestSection14395ScriptStripsCommentsBeforeMatching(t *testing.T) {
	exe := sqlExecutable14395(t, section14395Script)
	if !containsToken14395(exe, "regexp_replace(query, '--[^\\n]*', '', 'g')") {
		t.Errorf("★ %s 的【可执行面】里没有 `regexp_replace(query, '--[^\\n]*', '', 'g')` —— "+
			"★ ★★★★ pg_stat_statements.query 里存的是**原始文本**（含注释）"+
			"★ ⇒ ⇒ ★★ 本轮实测：凭据自检语句的**注释里**就写着 "+
			"`promote_session_turns_hot_to_partition` ⇒ 裸 ILIKE 会把它算成 promote 的调用"+
			"★ ⇒ ⇒ ★★ ⇒ 匹配前必须先剥注释（★ 与仓内门「剥注释后再查」同族，只是这次在库侧）",
			section14395Script)
	}
}

// TestSection14395RunbookRegistersFindings —— ★★ 判据⑥（★ 过程可查）
//
// ★★ 只钉【稳定标识】，不钉活数字 —— ★ hot 的行数/体积每小时都在涨
// ⇒ ⇒ 钉它等于钉一个明天就不一样的数字 ⇒ ⇒ 活数字进 runbook 但不进门的硬钉。
func TestSection14395RunbookRegistersFindings(t *testing.T) {
	runbook := readRepoFile14395(t, section14395Runbook)
	i := strings.Index(runbook, "### §143.95")
	if i < 0 {
		t.Fatal("★ runbook 里找不到 §143.95 段 —— ★ 本轮的留存归属结论必须落在仓内（★ 过程可查）")
	}
	seg := runbook[i:]
	for _, needle := range []string{
		"uq_session_turns_final_success",         // ★ 23505 撞的索引
		"gw_omp-global-kxpms",                    // ★ 唯一那个冲突键的会话
		"23505",                                  // ★ 失败码
		"statement_timestamp() - p_retention",    // ★ 排水判据
		"DefaultRetentionWindow",                 // ★ 8h 保留窗的常量名
		"5000",                                   // ★ 批大小
		"695",                                    // ★ 修法样板
		"890",                                    // ★ 样板失去保护对象
		"is_final_success",                       // ★ 冲突键的列
		"promote_session_turns_hot_to_partition", // ★ 排水函数名
	} {
		if !containsToken14395(seg, needle) {
			t.Errorf("★ §143.95 段里没有 %q —— "+
				"★ ★★ 本轮的结论是【机制级】的 ⇒ ⇒ 机制名/索引名/失败码必须登记"+
				"★ ⇒ ⇒ ★★ 少了它 ⇒ ⇒ 「hot 表在涨」这个现象就会脱离它的成因",
				needle)
		}
	}
}

// TestSection14395LessonsAndAgentsAreRegistered —— ★ 判据⑥的配套
func TestSection14395LessonsAndAgentsAreRegistered(t *testing.T) {
	if !containsToken14395(readRepoFile14395(t, section14395Lessons), "## §289") {
		t.Error("★ LESSONS.md 里没有 §289 —— " +
			"★ ⇒ ⇒ 「族退役时自愈迁移必须一起迁走」与「两个冲突是不同键」" +
			"★ ⇒ ⇒ 这两条通用式必须落进去")
	}
	if !containsToken14395(readRepoFile14395(t, section14395Agents), "R139 轮") {
		t.Error("★ AGENTS.md 里没有 R139 轮 —— " +
			"★ ⇒ ⇒ 本轮「留存归属 + 排水归属 + 根因」必须落在交接段")
	}
}
