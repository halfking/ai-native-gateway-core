// R89-DU（209 号）：`sql/fixes/` + `sql/audit/` 的**族级**清单与门。
//
// 208 号为 `sql/fixes/2026-10-02-db-storage-reclaim.sql` 单点建了门，并把它
// 列为下一轮顺位第 1 条。本轮动手时**第一件事就是验「能不能泛化」**，结果是
// **不能一刀切**，而这件事本身是最该记下来的：
//
//	208 号那条判据是「每个含 DROP/TRUNCATE 的动作必须带**空表**门禁」。它对
//	**回收类**脚本完全正确，但 `sql/fixes/2026-09-20-canonical-dedup-cleanup.sql`
//	合法地 **DELETE 非空行**（归并 canonical 重复项就是它的目的）⇒ 直接套用会
//	立刻变成一台**误报机器**。
//
// ⇒ 族级判据必须取**真正的公分母**：不管脚本做什么，它**失败时能不能中止**。
// 这一点可由文本判定，且对「回收 / 归并 / 重定向」三类都成立。
//
// 第二条判据来自本轮的真实缺陷：验证性写入的清理必须回到「写入发生的那一层」
// （见 fix-request-logs-bodies-partition-v2.sql 的 R89-DU 更正）。
package schema

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// destructiveRes 匹配会改数据/改结构的动作。
//
// ⚠️ **210 号修：`UPDATE` 的别名盲区。** 原第 4 条是
// `\bUPDATE\s+[a-z_][a-z0-9_.]*\s+SET\b`，它要求表名与 `SET` **紧邻**。
// 而 PostgreSQL 允许表别名：`UPDATE model_aliases ma SET …` —— 中间多了一个
// 标识符 ⇒ **不匹配**。实测：
//
//	UPDATE work_type_model_route w SET …   → false
//	UPDATE model_aliases ma SET …          → false
//	UPDATE models_canonical mc SET …       → false
//	UPDATE model_aliases SET …             → true
//
// 后果不是「少报一条」而是**整份清单缺失**：`2026-09-21-unmapped-cn-rename.sql`
// 是本族**唯一**真的去改 `models_canonical` / `model_aliases` 的脚本，而它的两处
// 写操作**都带别名** ⇒ 判据数出 0 处破坏性动作 ⇒ 脚本**根本没进清单**，
// 而清单仍然报告「8/8 可中止」并通过下限。
// ⇒ 这正是 playbook §122/§126 的老形态：**下限被满足了，而下限度量的那个集合
// 本身是残缺的**。下限钉在**过滤后的**集合上是本轮该被否掉的写法。
//
// ⇒ 修法两处：(1) 这里容忍可选别名；(2) **覆盖下限改钉在「含任意 DML 动词」的
// 宽集合上**（见 hasAnyDML / TestHandRunSqlScriptsInventory 的下限断言），
// 让「宽集合非空」与「过滤后非空」**不再可能是同一件事**。
var destructiveRes = mustCompiles(
	`(?i)\bDROP\s+(TABLE|INDEX|VIEW|SCHEMA|SUBSCRIPTION)`,
	`(?i)\bTRUNCATE\b`,
	`(?i)\bDELETE\s+FROM\s+`,
	// 容忍可选表别名：`UPDATE [ONLY] tbl [alias] SET`
	`(?i)\bUPDATE\s+(?:only\s+)?[a-z_][a-z0-9_.]*(?:\s+(?:as\s+)?[a-z_][a-z0-9_]*)?\s+SET\b`,
	// 210 号扩宽：**任何** `ALTER TABLE` 都算破坏性。原式要求同句内再出现 DROP，
	// 于是 `ADD CONSTRAINT` / `ATTACH PARTITION` 全被漏掉 —— 而这两类在生产上
	// 恰恰是最需要「能不能中止」的那类：`ALTER TABLE` 取 **ACCESS EXCLUSIVE**，
	// `ATTACH PARTITION` 还会逐分区扫全表。实测漏掉的两个脚本：
	// `fix-request-logs-hot-unique-constraint.sql`（ADD CONSTRAINT）与
	// `fix-request-logs-bodies-reattach-partitions.sql`（ATTACH PARTITION）。
	`(?i)\bALTER\s+TABLE\b`,
	// 非 CONCURRENTLY 的建索引会阻塞写；20M 行表上就是一次分钟级停写。
	`(?i)\bCREATE\s+(?:UNIQUE\s+)?INDEX\b`,
)

var (
	reRaiseException = regexp.MustCompile(`raise\s+exception`)
	reBeginSemi      = regexp.MustCompile(`(?m)^\s*begin\s*;`)
	reCommitSemi     = regexp.MustCompile(`(?i)commit\s*;`)
	reDryRunGate     = regexp.MustCompile(`dry[-_ ]?run|apply\s*=\s*'off'`)
	reInsertIntoTbl  = regexp.MustCompile(`(?i)insert\s+into\s+(?:[a-z_][a-z0-9_]*\.)?([a-z_][a-z0-9_]*)`)
	reDeleteFromTbl  = regexp.MustCompile(`(?i)delete\s+from\s+(?:[a-z_][a-z0-9_]*\.)?([a-z_][a-z0-9_]*)`)

	// reAnyDML 宽集合判据：**只看动词**，不看语法结构。
	// 它对别名/格式串/EXECUTE 包装统统不敏感 —— 这正是它能当覆盖下限的原因。
	reAnyDML = regexp.MustCompile(
		`(?i)\b(insert\s+into|update|delete\s+from|truncate|drop\s+(table|index|view|schema|subscription)|alter\s+table|create\s+(unique\s+)?index)\b`)
)

func mustCompiles(exprs ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(exprs))
	for _, e := range exprs {
		out = append(out, regexp.MustCompile(e))
	}
	return out
}

func countDestructive(low string) int {
	n := 0
	for _, re := range destructiveRes {
		n += len(re.FindAllStringIndex(low, -1))
	}
	return n
}

// probeTokens 是「自插探针」的识别特征。验证性 INSERT 会用这类明显的合成值，
// 它们必须被清掉。选它们而不是「所有 INSERT」是为了**不误伤**普通脚本
// （普通脚本 INSERT 一张日志表 + DELETE 一张垃圾表是完全正常的形态）。
//
// ⚠️ `'probe-` 在本仓有**相反**含义：migrations 里的
// `request_id NOT LIKE 'probe-%'` 是**过滤**探针、不是插探针
// （`649_routing_analytics_probe_filter.sql` 等 6 处）。它仍列在这里只因为
// 本判据只把 token 绑到 **INSERT 的目标表**上，`NOT LIKE` 落在 DELETE/WHERE
// 里不会触发绑定；但若将来把 token 判据放宽到文件级，它就会变成一台误报机。
var probeTokens = []string{"fix-verification-", "'__probe", "'selftest", "'probe-"}

// scriptFacts 是一份手跑/采集脚本的**结构化摘要**。
type scriptFacts struct {
	path              string
	destructive       int
	hasRaiseException bool
	hasOnErrorStop    bool
	hasTx             bool
	hasDryRunGate     bool
	insertTables      map[string]bool
	probeTables       map[string]bool
	deletesFrom       map[string]bool
}

// abortable 能否在失败时中止（族级公分母判据）。
func (f scriptFacts) abortable() bool {
	return f.hasRaiseException || f.hasOnErrorStop || f.hasTx || f.hasDryRunGate
}

// probeLeaks 探针插入的表没有被**同表** DELETE 清理 ⇒ 每次运行泄漏一行。
// 只报「插了却没在同表删」这一个方向；**不**反向判断（多删一张表是允许的）。
func (f scriptFacts) probeLeaks() []string {
	var out []string
	for tbl := range f.probeTables {
		if !f.deletesFrom[tbl] {
			out = append(out, tbl)
		}
	}
	sort.Strings(out)
	return out
}

// reVarDecl 抓 `name TEXT := …` 形式的变量声明。探针 token 常常就挂在
// 这样一个变量的初值上（`'fix-verification-' || extract(epoch …)`），
// 真正带 token 的 INSERT 只引用**变量名** ⇒ 必须两跳。
var reVarDecl = regexp.MustCompile(
	`(?i)([a-z_][a-z0-9_]*)\s+(?:text|varchar\s*\([^)]*\)|character\s+varying\s*\([^)]*\))\s*:=`)

// factsFromSQL 是 collectScriptFacts 的纯函数内核：吃一段已剥注释的 SQL 小写文本，
// 吐一份 scriptFacts。拆出来是为了让反向对照能直接喂构造文本。
//
// ⚠️ 三版都空过/误报过，空法各不相同，逐条记在这里因为**每一版的失败形态
// 都长得像「判据太严」，实际是「判据指错了对象」**：
//
//	① 按表名匹配 token —— token 在 VALUES/变量初值里，不在表名里 ⇒ 恒空。
//	② 按 `;` 切语句、要求语句内同时含 INSERT 与 token —— token 落在
//	   `DECLARE … test_request_id TEXT := 'fix-verification-'` 那一条里，
//	   在 INSERT **之前**就已被切走 ⇒ 仍恒空。
//	③ 改成**文件级** token 判定后判据过宽：要求「文件里所有 INSERT 的目标表」
//	   都出现在 DELETE 目标表集合里，于是把 `bak_20260920_`（`EXECUTE format`
//	   造出的备份表）、`merge_map`（**永久**记录归并关系的表）、`request_wal_hot`
//	   等正常写入全判成泄漏 —— 实测 3 个脚本 4 处误报，**这三个文件里根本没有任何
//	   探针 token**。更糟的是③的实现连 `hasProbeToken` 都没判，等于对每个脚本
//	   都跑这条判据，注释里写的「不含探针的普通脚本根本不会被审视」与代码不符。
//
// ⇒ 正确形态（三跳）：token 出现在文件里 → 绑定到 `TEXT :=` 该 token 的**变量名**
// → 该变量名出现在哪条 INSERT 的 VALUES 里 → 那条 INSERT 的**目标表**才是探针表。
// 这样备份表/临时表/永久映射表都不会被误判，因为它们既不携带变量也不携带 token。
func factsFromSQL(low string) scriptFacts {
	f := scriptFacts{
		insertTables:      map[string]bool{},
		probeTables:       map[string]bool{},
		deletesFrom:       map[string]bool{},
		hasRaiseException: reRaiseException.MatchString(low),
		hasOnErrorStop:    strings.Contains(low, "on_error_stop"),
		hasTx:             reBeginSemi.MatchString(low) && reCommitSemi.MatchString(low),
		hasDryRunGate:     reDryRunGate.MatchString(low),
	}
	f.destructive = countDestructive(low)

	// 第一跳：token 在哪。
	hasToken := containsAnyShim(low, probeTokens)

	// 第二跳：token 挂在哪个变量上（`;` 切分只用于**定位声明**，
	// 不再要求 token 与 INSERT 同段 —— 那正是②空掉的原因）。
	probeVars := map[string]bool{}
	if hasToken {
		for _, stmt := range strings.Split(low, ";") {
			if !containsAnyShim(stmt, probeTokens) {
				continue
			}
			for _, m := range reVarDecl.FindAllStringSubmatch(stmt, -1) {
				probeVars[m[1]] = true
			}
		}
	}

	// 第三跳：哪条 INSERT 真的携带了 token 或探针变量 ⇒ 那条的目标表才是探针表。
	for _, stmt := range strings.Split(low, ";") {
		m := reInsertIntoTbl.FindStringSubmatch(stmt)
		if m == nil {
			continue
		}
		tbl := m[1]
		f.insertTables[tbl] = true

		carries := containsAnyShim(stmt, probeTokens)
		if !carries {
			for v := range probeVars {
				if containsWord(stmt, v) {
					carries = true
					break
				}
			}
		}
		if carries {
			f.probeTables[tbl] = true
		}
	}

	for _, m := range reDeleteFromTbl.FindAllStringSubmatch(low, -1) {
		f.deletesFrom[m[1]] = true
	}
	return f
}

// containsWord 做词边界匹配，避免变量 `probe_id` 命中 `test_probe_id_extra`。
func containsWord(s, word string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return false
		}
		at := i + j
		beforeOK := at == 0 || !isWordByte(s[at-1])
		after := at + len(word)
		afterOK := after >= len(s) || !isWordByte(s[after])
		if beforeOK && afterOK {
			return true
		}
		i = at + 1
		if i >= len(s) {
			return false
		}
	}
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func collectScriptFacts(t *testing.T, path string) (scriptFacts, bool, bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	f := factsFromSQL(strings.ToLower(stripSQLComments(string(b))))
	f.path = filepath.ToSlash(path)
	// 两个布尔量**刻意分开返回**：
	//   destructive = 分类器认为「改了数据/结构」；
	//   hasDML      = 宽集合，只看动词，不看语法。
	// 把它们合成一个返回值，210 号那个盲区就会以「清单非空」的形式复活。
	return f, f.destructive > 0, reAnyDML.MatchString(strings.ToLower(stripSQLComments(string(b))))
}

func containsAnyShim(s string, toks []string) bool {
	for _, tk := range toks {
		if strings.Contains(s, tk) {
			return true
		}
	}
	return false
}

func yesNo(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

func itoaShim(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestHandRunSqlDestructiveClassifierControls 是「破坏性分类器」的反向对照。
//
// 为什么必须单独钉：210 号的整轮结论就是**这个分类器漏了一类形态**，而它漏的
// 方向是**静默少报**——`UPDATE <表> <别名> SET` 不匹配 ⇒ 整份脚本从清单消失 ⇒
// 清单仍然非空 ⇒ 覆盖下限仍然通过 ⇒ 门全绿。
//
// ⇒ 这里同时钉两个方向：
//
//	① **必须认出来**（别名 UPDATE / ADD CONSTRAINT / ATTACH PARTITION / 非并发建索引）；
//	② **必须不认**（**被注释掉**的 DML —— 那是模板文本，不是会执行的动作）。
//	   这一条是 ②方向的锚：若把注释也算进去，宽集合的 11 会虚高，覆盖下限
//	   就会变成一个**用注释喂饱的**数字 —— 那正是 §126「注释不是契约」的变体。
func TestHandRunSqlDestructiveClassifierControls(t *testing.T) {
	cases := []struct {
		name       string
		sql        string
		wantDestr  int  // 期望数出的破坏性动作数（0 = 不该进清单）
		wantInWide bool // 期望是否落在宽集合（含任意 DML 动词）
	}{
		{
			// 210 号那个盲区的最小复现：**带别名**的 UPDATE。
			// 旧式 `UPDATE\s+表\s+SET` 在这里不匹配 ⇒ 整份脚本被判成「无破坏性动作」。
			name:      "坏→好：带表别名的 UPDATE 必须被认出来",
			sql:       `UPDATE model_aliases ma SET status='deprecated' WHERE ma.canonical_id=1;`,
			wantDestr: 1, wantInWide: true,
		},
		{
			name:      "带 schema 限定 + 别名的 UPDATE",
			sql:       `UPDATE public.models_canonical mc SET canonical_name='x' WHERE mc.id=1;`,
			wantDestr: 1, wantInWide: true,
		},
		{
			// 210 号漏掉的第二类：ADD CONSTRAINT 在旧式 `ALTER TABLE … DROP` 下不匹配。
			name:      "ADD CONSTRAINT 必须算破坏性（ACCESS EXCLUSIVE）",
			sql:       `ALTER TABLE request_logs_hot ADD CONSTRAINT uq UNIQUE (request_id, ts);`,
			wantDestr: 1, wantInWide: true,
		},
		{
			name:      "ATTACH PARTITION 必须算破坏性（逐分区扫全表）",
			sql:       `ALTER TABLE request_logs_bodies ATTACH PARTITION p FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');`,
			wantDestr: 1, wantInWide: true,
		},
		{
			name:      "非 CONCURRENTLY 建索引必须算破坏性（阻塞写）",
			sql:       `CREATE UNIQUE INDEX idx_x ON request_logs_hot (request_id, ts);`,
			wantDestr: 1, wantInWide: true,
		},
		{
			// ② 方向：**被注释掉**的 DML 不是会执行的动作。
			// 210 号实测 `fix-glm52-alias-drift.sql` 与
			// `fix-discovery-junk-canonical-rows.sql` 的 DML **全部在注释里**
			// （两个独立方法：子代理逐行读 + 本门宽集合判据，互相印证）。
			// 若这里判成「有破坏性动作」，宽集合的 11 就是被注释喂出来的数字。
			name:      "注释掉的 DML 不得算破坏性（模板文本）",
			sql:       "-- UPDATE model_aliases ma SET status='deprecated';\n-- DELETE FROM models_canonical;",
			wantDestr: 0, wantInWide: false,
		},
		{
			name:      "纯 SELECT 采集脚本不得算破坏性",
			sql:       `SELECT count(*) FROM request_logs WHERE ts > now();`,
			wantDestr: 0, wantInWide: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stripped := strings.ToLower(stripSQLComments(tc.sql))
			if got := countDestructive(stripped); got != tc.wantDestr {
				t.Errorf("破坏性动作数 = %d，期望 %d；输入：%q", got, tc.wantDestr, tc.sql)
			}
			if got := reAnyDML.MatchString(stripped); got != tc.wantInWide {
				t.Errorf("宽集合命中 = %v，期望 %v；输入：%q", got, tc.wantInWide, tc.sql)
			}
		})
	}

	// 负控（顺序敏感）：`UPDATE` 的可选别名段**不能**吃掉 `SET` 自己。
	// `[a-z_][a-z0-9_]*` 与 `set` 词形相同，若把别名写成 `(?:as\s+)?[a-z_][a-z0-9_]*`
	// 却**没有**后面的 `\s+SET`，`UPDATE x SET SET` 这类会被误配。显式钉住
	// 正常写法仍然只数出 1 处（而不是 2 处）。
	if n := countDestructive(`update public.models_canonical mc set canonical_name='x';`); n != 1 {
		t.Errorf("正常带别名写法应恰好数出 1 处破坏性动作，实际 %d", n)
	}
}

// TestHandRunSqlScriptsInventory 列出所有含破坏性动作的手跑/采集脚本，并断言
// 它们**都有可中止机制**。这条是族级公分母，对「回收/归并/重定向」都成立。
func TestHandRunSqlScriptsInventory(t *testing.T) {
	roots := []string{"../fixes", "../audit"}
	var facts []scriptFacts
	dmlCount := 0
	for _, r := range roots {
		entries, err := os.ReadDir(r)
		if err != nil {
			t.Fatalf("read dir %s: %v", r, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
				continue
			}
			f, destructive, hasDML := collectScriptFacts(t, filepath.Join(r, e.Name()))
			if hasDML {
				dmlCount++
			}
			if destructive {
				facts = append(facts, f)
			}
		}
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].path < facts[j].path })

	// 覆盖下限（210 号改钉法）。
	//
	// ⚠️ 209 号把下限钉在**过滤后**的 `facts` 上，这是本轮被判掉的写法：
	// 只要 `destructiveRes` 漏掉某一类动作，那个脚本就**不进 `facts`**，
	// 而「清单非空」仍然成立 ⇒ **下限被满足，而下限度量的集合本身残缺**。
	// 实测：`UPDATE <表> <别名> SET` 全家不被识别 ⇒
	// `2026-09-21-unmapped-cn-rename.sql`（本族唯一真的改
	// `models_canonical`/`model_aliases` 的脚本）**整份从清单消失**，
	// 门照样报「8/8 可中止」。
	//
	// ⇒ 现在下限钉在**宽集合**（`hasAnyDML`：只看动词，不看语法结构）上。
	// 宽集合与过滤后集合**不再可能是同一件事**，所以「宽集合非空」这条断言
	// 才真的在约束覆盖面。宽集合的下限**按实测取值**，不是随手写的整数。
	// 下限按**实测取值**（210 号逐文件量过），不是随手写的整数：
	// 18 个脚本里宽集合命中 11 个 —— 7 个未命中的全部是**只读采集脚本**
	// （5 个 `db-audit-252-*`/`db-audit-collect` + 2 个 DML 全被注释的模板脚本）。
	// 取紧下限 11 的意义：任一脚本从宽集合里掉出去都会红（10 < 11）。
	if dmlCount < 11 {
		t.Fatalf("含任意 DML 动词的手跑脚本只数到 %d 个（下限 11）：宽集合抽取器已失效，"+
			"此时下面的「都有中止机制」会与「什么都没抽到」同步为真。"+
			"⚠️ 下限**必须**钉在宽集合上——钉在过滤后的集合上时，"+
			"判据一漏，整份脚本就从清单里消失，而下限照样通过（210 号的实测）。", dmlCount)
	}
	if len(facts) < 10 {
		t.Fatalf("经破坏性分类后只数到 %d 个脚本（下限 10）：分类器可能又漏了一类动作。"+
			"宽集合数到 %d 个，两者差 %d 个——差值就是被分类器漏掉的脚本。",
			len(facts), dmlCount, dmlCount-len(facts))
	}
	// **具名锚点**：比裸计数更强的一条 —— 点名「本族唯一真的改 models_canonical /
	// model_aliases 的脚本必须在清单里」。若将来有人再收窄 destructiveRes，
	// 这条会先于计数下限报出来，且报的名字是**具体的**。
	// （210 号实测它就是被漏掉的那个。）
	anchored := false
	for _, f := range facts {
		if strings.HasSuffix(f.path, "2026-09-21-unmapped-cn-rename.sql") {
			anchored = true
			break
		}
	}
	if !anchored {
		t.Errorf("`2026-09-21-unmapped-cn-rename.sql` 不在清单里：它含 " +
			"`UPDATE model_aliases ma SET …` 与 `UPDATE models_canonical mc SET …`，" +
			"两处都带**表别名**。210 号实测：旧判据要求表名与 SET 紧邻 ⇒ 全部漏掉 ⇒ " +
			"这个本族唯一真的改模型名的脚本**整份从清单消失**，而门仍报「全部可中止」。")
	}
	t.Logf("覆盖面自报：宽集合(含任意 DML)=%d，经破坏性分类=%d"+
		"（下限只保证抽取器活着，**不**代表一族逐个审过）", dmlCount, len(facts))

	// 第二条判据的覆盖下限：**必须真的检出过探针写入**。否则
	// 「没有探针泄漏」会与「探针检测器坏掉」同步为真——①/② 两版正是这么空过的。
	probeScripts := 0
	for _, f := range facts {
		if len(f.probeTables) > 0 {
			probeScripts++
		}
	}
	if probeScripts == 0 {
		t.Errorf("一个探针脚本都没检出（probeTables 全空）：探针检测器很可能已失效，" +
			"此时「没有探针泄漏」与「检不出探针」同步为真。" +
			"自检：fix-request-logs-bodies-partition-v2.sql 里应有 " +
			"`INSERT INTO request_logs_bodies (…, test_request_id, …)`")
	}
	// ⚠️ 这个下限当前就是 1，而 1 **不**足以说明「一族都查过了」：
	// `sql/fixes/` + `sql/audit/` 里目前只有 1 个脚本会自插探针。
	// 下限取 1 是为了钉住「检测器活着」，**不是**为了声称覆盖面够；
	// 覆盖面随新脚本进来而增长，本行不会自动变松。
	t.Logf("探针脚本覆盖：%d 个（当前下限 1，只保证检测器活着，不声称一族查全）", probeScripts)

	var b strings.Builder
	b.WriteString("手跑/采集脚本的破坏性动作与中止机制（族级公分母判据）\n")
	for _, f := range facts {
		b.WriteString("  " + f.path + "\n")
		b.WriteString("    破坏性动作=" + itoaShim(f.destructive) +
			"  RAISE_EXCEPTION=" + yesNo(f.hasRaiseException) +
			"  ON_ERROR_STOP=" + yesNo(f.hasOnErrorStop) +
			"  显式事务=" + yesNo(f.hasTx) +
			"  dry-run闸=" + yesNo(f.hasDryRunGate) +
			"  ⇒ 可中止=" + yesNo(f.abortable()) +
			"  探针表=" + itoaShim(len(f.probeTables)) + "\n")
		if leaks := f.probeLeaks(); len(leaks) > 0 {
			b.WriteString("    ⚠️ 探针写入未在**同一张表**清理： " + strings.Join(leaks, ", ") + "\n")
		}
	}
	t.Logf("\n%s", b.String())

	for _, f := range facts {
		if !f.abortable() {
			t.Errorf("%s 含 %d 处破坏性动作却**没有任何可中止机制**"+
				"（无 RAISE EXCEPTION / ON_ERROR_STOP / 显式事务 / dry-run 闸）："+
				"它失败时只会继续往下跑并以 0 退出，运维与 CI 都看不出来。"+
				"⚠️ 本判据是**公分母**，不要求「空表门禁」——归并/重定向类脚本合法地"+
				"删非空行（见 2026-09-20-canonical-dedup-cleanup.sql）",
				f.path, f.destructive)
		}
		for _, leak := range f.probeLeaks() {
			t.Errorf("%s：探针写入的表 %s 没有被 `DELETE FROM <同一张表>` 清理。\n"+
				"  验证性写入必须回到「写入发生的那一层」删除。只清你**期望**它去的那"+
				"一层（典型：只清 default 分区）⇒ 一旦路由被真正修好、写入落到别的分区，"+
				"就会**每次运行永久泄漏一行**，而判定同时报成「验证失败」——"+
				"**误诊 + 泄漏**，且常是非致命 WARNING ⇒ 对 CI 完全不可见。",
				f.path, leak)
		}
	}
}

// TestHandRunSqlProbeLeakCriteriaControls 是上面判据的**反向对照**。
//
// 为什么必须有它：本门在 209 号里**三次**先空过（①表名匹配 ②按 `;` 切段
// ③文件级判定），而空过的形态全部是「安静地全绿」；第三次又从空转成
// 4 处误报。**只测「能抓坏东西」的门，会在收窄与放宽之间来回摆，
// 且两次都绿。** 所以这里把「坏写法必须被抓」与「好写法必须不被抓」
// 并列钉死——好那一半就是本轮实测踩到的 4 处误报。
func TestHandRunSqlProbeLeakCriteriaControls(t *testing.T) {
	cases := []struct {
		name     string
		sql      string
		wantLeak bool   // 期望是否报出泄漏
		wantTbl  string // 期望点名的表（wantLeak=true 时）
		wantIns  int    // 期望认出的 INSERT 目标表总数
		wantProb int    // 期望认出的**探针**表数
	}{
		{
			// 负控 1：**本轮真实缺陷的最小复现**。探针写父表、只清 default 分区。
			// 这正是修复前 fix-request-logs-bodies-partition-v2.sql 的形态。
			name: "坏：探针插父表但只从分区清理",
			sql: `do $$ declare
  test_request_id text := 'fix-verification-' || extract(epoch from now())::text;
  n integer;
begin
  insert into request_logs_bodies (request_id, ts, request_body)
  values (test_request_id, now(), '{"t":1}'::jsonb);
  select count(*) into n from request_logs_bodies_default where request_id = test_request_id;
  delete from request_logs_bodies_default where request_id = test_request_id;
end $$;`,
			wantLeak: true, wantTbl: "request_logs_bodies",
			wantIns: 1, wantProb: 1,
		},
		{
			// 负控 2：修复后的正确形态 —— 清理回到父表 ⇒ 不得报。
			name: "好：探针插父表并从父表清理",
			sql: `do $$ declare
  test_request_id text := 'fix-verification-' || extract(epoch from now())::text;
begin
  insert into request_logs_bodies (request_id, ts, request_body)
  values (test_request_id, now(), '{"t":1}'::jsonb);
  delete from request_logs_bodies where request_id = test_request_id;
end $$;`,
			wantLeak: false,
			wantIns:  1, wantProb: 1,
		},
		{
			// 负控 3：**过宽判据的反向对照**。同一个探针脚本里另有一处
			// 正常写入（备份表），备份表永不被删 —— 这**不是**泄漏。
			// ③ 文件级判定就是在这里误报的（实测 bak_20260920_ / merge_map）。
			name: "好：探针脚本里的备份表写入不算泄漏",
			sql: `do $$ declare
  test_request_id text := 'fix-verification-1';
begin
  insert into request_logs_bodies (request_id, ts, request_body)
  values (test_request_id, now(), '{"t":1}'::jsonb);
  execute format('insert into public.bak_20260920_%I select * from public.%I', 't', 't');
  delete from request_logs_bodies where request_id = test_request_id;
end $$;`,
			wantLeak: false,
			wantIns:  2, wantProb: 1,
		},
		{
			// 负控 4：**完全没有探针 token** 的脚本，插了不删也不算泄漏。
			// 钉的是 ③ 漏掉 `hasProbeToken` 判断那一处 —— 实测把
			// request_wal_hot / candidate_failure_logs 判成了泄漏。
			name: "好：无探针 token 的普通写入不算泄漏",
			sql: `insert into request_wal_hot (request_id) values ('abc');
delete from request_wal_cold where request_id = 'abc';`,
			wantLeak: false,
			wantIns:  1, wantProb: 0,
		},
		{
			// 负控 5：token 挂在 DECLARE 变量上（不与 INSERT 同段）。
			// 钉 ②「按 `;` 切段要求 token 与 INSERT 同段」那个空法。
			// 这段探针没写清理 ⇒ **应当**被报为泄漏；② 会因认不出探针而静默放过。
			name: "坏：token 经 DECLARE 变量两跳绑定，且未清理",
			sql: `do $$ declare
  test_request_id text := 'fix-verification-';
begin
  insert into request_logs_bodies (request_id, ts) values (test_request_id, now());
end $$;`,
			wantLeak: true, wantTbl: "request_logs_bodies",
			wantIns: 1, wantProb: 1,
		},
		{
			// 负控 6：token 直接落在 INSERT 的 VALUES 里，没有中间变量。
			// 保证变量两跳不是**唯一**通路（`'__probe` 形态）。同样未清理 ⇒ 应报。
			name:     "坏：token 直接写在 VALUES 里，且未清理",
			sql:      `insert into request_logs_bodies (request_id, ts) values ('__probe-1', now());`,
			wantLeak: true, wantTbl: "request_logs_bodies",
			wantIns: 1, wantProb: 1,
		},
		{
			// 负控 7：变量名做**词边界**匹配。`probe_id` 不应被
			// `my_probe_id_backup` 命中，否则又把过宽的老毛病放回来。
			name: "好：变量名按词边界匹配，不被更长标识符误绑",
			sql: `do $$ declare
  test_request_id text := 'fix-verification-';
begin
  insert into audit_log (request_id) values (my_test_request_id_backup);
end $$;`,
			wantLeak: false,
			wantIns:  1, wantProb: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := factsFromSQL(strings.ToLower(tc.sql))
			if got := len(f.insertTables); got != tc.wantIns {
				t.Errorf("INSERT 目标表数 = %d，期望 %d（集合 %v）", got, tc.wantIns, keysOf(f.insertTables))
			}
			if got := len(f.probeTables); got != tc.wantProb {
				t.Errorf("探针表数 = %d，期望 %d（集合 %v）", got, tc.wantProb, keysOf(f.probeTables))
			}
			leaks := f.probeLeaks()
			if tc.wantLeak {
				if len(leaks) != 1 || leaks[0] != tc.wantTbl {
					t.Fatalf("泄漏判据未报出 %s，实际报 %v", tc.wantTbl, leaks)
				}
				return
			}
			if len(leaks) != 0 {
				t.Errorf("误报：期望无泄漏，实际报出 %v", leaks)
			}
		})
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
