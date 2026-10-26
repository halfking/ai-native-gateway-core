package db

// §143.16 的代码门：`session_summaries` 的「裸唯一索引」必须**条件性**存在。
//
// 背景（runbook §143.16，docs/06-deployment/04-runbooks/runbooks/
// 2026-10-04-ursm-write-amplification-measurement.md）：
//
//	生产 `session_summaries` 上有**两份键集逐字相同**的唯一索引：
//	  - 主键 `session_summaries_pkey` = PRIMARY KEY (session_key)
//	  - 裸索引 `session_summaries_session_key_uidx` = UNIQUE (session_key)
//	  两者的 `pg_index.indkey` **都是 1** ⇒ 生产实测两份各 ~67MiB，
//	  而该表持续写入 ⇒ 每次写都多维护一份 B-tree ⇒ 裸 uidx 是纯冗余。
//
//	但它**不是恒冗余**：memora 最小 shape（表被覆盖成自己的结构）时表上
//	**没有主键**，而 `ON CONFLICT (session_key)` 需要**非部分唯一索引**才能仲裁
//	（本文件头注释与 ensure 的 EXCEPTION 分支都写明了这一点）
//	⇒ 裸 uidx 在那种部署里是唯一的仲裁索引 ⇒ 不能无条件删、也不能无条件建。
//
//	⇒ 判据 =「session_key 上是否已有可仲裁的非部分唯一索引」，
//	  抽成一个常量、**两处共用**（bootstrap 的 CREATE 守卫 + canonical 短路的缺失判定）。
//
// ★ 这道门钉的是**判据的形状**与**两处共用的结构**；
//   判据的语义正确性由真库探针验证过（PostgreSQL 17.11 实测：
//   只有主键 ⇒ 1 个仲裁；只有裸 uidx ⇒ 1 个仲裁；
//   部分唯一索引 ⇒ 0 个；两列唯一索引 ⇒ 0 个；什么都没有 ⇒ 0 个；
//   主键 + 裸 uidx ⇒ 2 个 ⇒ 删掉裸 uidx 后主键接管仲裁、冲突语义不变），
//   探针脚本与复跑命令见 runbook §143.16.8。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	arbiterConstName = "sessionSummariesSessionKeyArbiterSQL"
	arbiterIndexName = "session_summaries_session_key_uidx"
)

// arbiterConstRe 抓出判据常量的**字面量**（原文，不是重新描述）。
var arbiterConstRe = regexp.MustCompile(
	`(?s)const ` + arbiterConstName + ` = ` + "`" + `(.*?)` + "`")

// readDBSource 读 db 包下的源文件；门只钉源码文本，不连库（可在 CI / 开发机跑）。
func readDBSource(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读 %s 失败：%v", name, err)
	}
	return string(raw)
}

// arbiterRefRe 匹配「引用判据常量」的**形态**：反引号 + 可选空白 + `+` + 常量名
// + 可选空白 + `+` + 反引号。
//
// ★ 为什么用正则而不是字面串：**gofmt 会改这个形态** ——
// ` + sessionSummariesSessionKeyArbiterSQL + ` 会被压成
// `+sessionSummariesSessionKeyArbiterSQL+`（它是更大表达式的一部分）。
// 门若钉住字面形态 ⇒ **跑一次 gofmt 就假红**，而代码语义一点没变
// ⇒ 这正是「过度声明一条脆弱判据比没有它更坏」的形态。
// ⇒ 门钉的是**语义**（两处确实共用同一份判据），不是**排版**。
var arbiterRefRe = regexp.MustCompile("`\\s*\\+\\s*" + arbiterConstName + "\\s*\\+\\s*`")

// arbiterRefs 数出「引用判据常量」的次数。
func arbiterRefs(src string) int {
	return len(arbiterRefRe.FindAllString(src, -1))
}

// TestSessionSummariesArbiterPredicateShape：钉住判据谓词的**三个必要条件**。
//
// 为什么这三个缺一不可：
//   - `indisunique`：非唯一索引不能仲裁 ON CONFLICT；
//   - `indpred IS NULL`：**部分**索引没有仲裁资格（PG 只在冲突目标自带同样的
//     WHERE 谓词时才认部分索引；本仓的冲突目标没有）；
//   - `indnkeyatts = 1`：键必须恰好是 session_key 一列 —— (session_key, tenant_id)
//     这样的唯一索引**不能**仲裁 `ON CONFLICT (session_key)`。
//
// 少一个，判据就会把「不是仲裁索引的东西」当成仲裁索引 ⇒
// 裸 uidx 被当成不需要 ⇒ memora 最小 shape 的部署会失去仲裁索引 ⇒
// `ON CONFLICT (session_key)` 直接失败。
func TestSessionSummariesArbiterPredicateShape(t *testing.T) {
	src := readDBSource(t, "session_summaries_schema.go")
	m := arbiterConstRe.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("找不到判据常量 %s：判据形状变了，请同步更新这条门", arbiterConstName)
	}
	pred := m[1]

	for _, want := range []string{
		"i.indisunique",
		"i.indpred IS NULL",
		"i.indnkeyatts = 1",
		"a.attname = 'session_key'",
	} {
		if !strings.Contains(pred, want) {
			t.Errorf("判据常量缺少 %q：\n%s", want, pred)
		}
	}
	// 这段 SQL 会被 fmt.Sprintf 拼进 unnest(ARRAY[%s]) ⇒ 出现 % 会被当格式符。
	if strings.Contains(pred, "%") {
		t.Errorf("判据常量里出现了 %%：它会被 fmt.Sprintf 当格式符 ⇒ 运行时报错。\n%s", pred)
	}
}

// TestSessionSummariesArbiterPredicateSharedByBothCallSites：判据必须**只有一份**、
// 且**两处都在用**。
//
// 两处：① bootstrap 的 CREATE 守卫（session_summaries_schema.go）
//
//	② canonical 短路的「缺索引」判定（db_session_summaries_shortcircuit.go）
//
// 为什么不容许第三份拷贝：两处一旦各写各的，就会出现「bootstrap 认为有仲裁索引
// 而短路认为没有」（⇒ 每次开机都走 DDL 路径 ⇒ 对热表申请 ACCESS EXCLUSIVE ⇒
// 2026-10-11 的 2530 部署事故）或反之（⇒ memora 形态拿不到仲裁索引）。
func TestSessionSummariesArbiterPredicateSharedByBothCallSites(t *testing.T) {
	schemaSrc := readDBSource(t, "session_summaries_schema.go")
	shortcircuitSrc := readDBSource(t, "db_session_summaries_shortcircuit.go")

	// 只数**引用形态**，不去数注释里提到的名字 ——
	// 注释里出现名字是正常的（说明「为什么共用一份」），不是一次拷贝。
	if n := arbiterRefs(schemaSrc); n != 1 {
		t.Errorf("判据常量在 session_summaries_schema.go 里被引用 %d 次（期望 1 次）："+
			"判据被复制成多份就会漂移（bootstrap 与 canonical 必须共用同一段 SQL）", n)
	}
	if n := arbiterRefs(shortcircuitSrc); n != 1 {
		t.Errorf("判据常量在 db_session_summaries_shortcircuit.go 里被引用 %d 次（期望 1 次）："+
			"canonical 短路必须复用同一份判据，不能另写一份", n)
	}
	if !strings.Contains(schemaSrc, "const "+arbiterConstName+" = ") {
		t.Errorf("判据常量的定义不在 session_summaries_schema.go：两处引用会编译不过")
	}
}

// TestSessionSummariesCreateIsGuardedByArbiter：裸 uidx 的创建必须**被判据守卫**。
//
// 判据形状：CREATE 语句前面必须紧跟 `IF NOT EXISTS (` + 判据常量 + `)`。
// 若守卫被去掉（回到「无条件创建」），生产上主键在位时每次开机都会重建一份
// ~67MiB 的重复索引 —— 而这正是本次要消除的写放大。
func TestSessionSummariesCreateIsGuardedByArbiter(t *testing.T) {
	src := readDBSource(t, "session_summaries_schema.go")
	// 守卫形态：IF NOT EXISTS ( <引用判据常量> ) THEN
	// ★ 用正则而不是字面串：gofmt 会把 ` + 常量 + ` 压成 `+常量+`
	//   ⇒ 门若钉字面形态，跑一次 gofmt 就假红，而语义一点没变。
	guardRe := regexp.MustCompile(`IF NOT EXISTS \(\s*` +
		"`\\s*\\+\\s*" + arbiterConstName + "\\s*\\+\\s*`" + `\s*\) THEN`)
	guardLoc := guardRe.FindStringIndex(src)
	createLoc := strings.Index(src, "CREATE UNIQUE INDEX "+arbiterIndexName)
	if createLoc < 0 {
		t.Fatalf("ensure 里找不到 CREATE UNIQUE INDEX %s："+
			"memora 最小 shape（表无主键）时 ON CONFLICT (session_key) 会失去仲裁索引", arbiterIndexName)
	}
	if guardLoc == nil {
		t.Errorf("裸 uidx 的创建没有被判据守卫（期望形如 IF NOT EXISTS (` + %s + `) THEN）："+
			"主键在位时无条件创建 = 每次开机重建一份重复索引", arbiterConstName)
		return
	}
	if guardLoc[0] > createLoc {
		t.Errorf("判据守卫出现在 CREATE 之后（守卫偏移 %d > CREATE 偏移 %d）：守卫无效",
			guardLoc[0], createLoc)
	}
}

// TestSessionSummariesCanonicalExemptsUidxWhenArbiterExists：canonical 短路必须
// 承认「裸 uidx 的必要性是条件性的」。
//
// 若缺这一条：生产上裸 uidx 被 DROP 之后，canonical 判定会认为「清单里的索引缺了」
// ⇒ 每次开机都走 ensure 的 DDL 路径 ⇒ 对这张热表申请 ACCESS EXCLUSIVE
// ⇒ 2026-10-11 的 2530 部署事故（boot 卡 3 分钟、healthz 起不来、部署回滚）会复现。
func TestSessionSummariesCanonicalExemptsUidxWhenArbiterExists(t *testing.T) {
	src := readDBSource(t, "db_session_summaries_shortcircuit.go")
	if !strings.Contains(src, "want.name = '"+arbiterIndexName+"'") {
		t.Errorf("canonical 短路没有对 %s 做条件豁免：裸 uidx 被 DROP 之后守卫会一直"+
			"判定「缺索引」⇒ 每次开机走 DDL 路径 ⇒ 热表被 ACCESS EXCLUSIVE 锁住", arbiterIndexName)
	}
	// 豁免条件必须用**同一份判据**（同样用正则，对 gofmt 的排版变化不敏感）。
	exemptRe := regexp.MustCompile(`AND EXISTS \(\s*` +
		"`\\s*\\+\\s*" + arbiterConstName + "\\s*\\+\\s*`" + `\s*\)`)
	if !exemptRe.MatchString(src) {
		t.Errorf("canonical 短路的豁免条件没有用判据常量（期望 EXISTS (` + %s + `)）："+
			"豁免条件与 bootstrap 的创建守卫必须是同一份判据", arbiterConstName)
	}
}
