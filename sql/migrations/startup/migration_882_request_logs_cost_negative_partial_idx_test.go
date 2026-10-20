package startup

// migration_882_request_logs_cost_negative_partial_idx_test.go —— 迁移 882 的契约门。
//
// # ★ 2026-10-20（890 真删 v1 族）：这道门改了题
//
// 882 给 `_request_logs`（860 改名的 request_logs 分区父表）建
// `WHERE cost_usd < 0` 的部分索引，让 bg/routing_health_checks.go 的
// `recorded_cost_is_negative` 检查从顺序扫全表变成索引探针。
//
// **890 把整个 v1 族删了**（`DROP TABLE public._request_logs`）⇒
// 882 的索引**级联随之消亡** ⇒ 「882 索引 ↔ 检查谓词」这条不变量的
// **主体已退役**。
//
// 按仓库的「测试处置三分法」：
//
//	(a) 主体还在、形状变了   ⇒ 反转断言；
//	(b) 主体真退役且无等价物 ⇒ 删测试 + 留下「退役了什么/为什么/现在由什么承担」；
//	(c) 主体退役但有等价不变量 ⇒ **换成守新不变量的门**。
//
// 这里走 (c)：等价不变量是「负成本检查的谓词有一个能服务它的部分索引」，
// 它在**会话族**（迁移 891）身上继续活着 ⇒
//
//	· ① 形态门（**保留**）：882 的迁移文件自身没被改坏。它仍会跑——
//	  新装路径：基线建出 v1 族 → 882 建索引 → 890 删表删索引。
//	· ② down 门（**保留**）：down 删同名索引。
//	· ③ cross-pin 门（**反转**）：原门钉「检查侧读 `public._request_logs r`」，
//	  现在检查侧已迁会话族 ⇒ 改为**负向**断言「不再读 v1 族」+ **正向**
//	  断言「读会话族窄源、谓词仍是 `cost_usd < 0` / 30 天」。
//	· ④ 新增：v1 族真删 ⇒ 882 的索引**必然**随表消亡（钉住这个事实，
//	  免得以后有人以为「索引还在，只是表没了」）。
//	· ⑤ 新增：会话族必须有自己的 `cost_usd < 0` 部分索引（等价不变量本体）。
//	· ⑥ embeddata 两副本字节一致（**保留**）。
//
// # 判据纪律（本文件自己踩过的坑）
//
//	· Go 侧的「SQL 里有没有提到某个关系」必须走 `go/ast` 取**字符串字面量**
//	  （`goStringLiterals`，见 repair_529_test.go）：按行剥 `//` 会把字面量
//	  截断，而源码里的注释又会把「退役说明」顶成读点。
//	· 判「提到关系」必须按**标识符精确匹配**，不能 `strings.Contains`：
//	  `strings.Contains(s, "public.request_logs")` 会命中
//	  `public.request_logs_with_current_month`（canonical 视图名）⇒ 假阳性。
//	  本文件用「剥掉注释与字符串字面量后按标识符切分」来判定。
//	· 负向断言必须配**阳性对照**（同一个判据必须能对真的 v1 读点报红）
//	  与**阴性对照**（必须对 canonical 视图名**不**报红）——
//	  否则「判据恒不命中」和「确实没有读点」长得一模一样。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	mig882Name     = "882_request_logs_cost_negative_partial_idx.sql"
	mig882DownName = "882_request_logs_cost_negative_partial_idx.down.sql"
)

func mig882Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// <root>/sql/migrations/startup → 上溯三级到 <root>
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig882Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// ── 判据工具 ────────────────────────────────────────────────────────────

// mig882V1Relations 是 v1 族的关系名清单（890 之后全部不存在）。
var mig882V1Relations = map[string]bool{
	"request_logs":             true,
	"_request_logs":            true,
	"request_logs_hot":         true,
	"_request_logs_hot":        true,
	"request_logs_bodies":      true,
	"_request_logs_bodies":     true,
	"request_logs_bodies_hot":  true,
	"_request_logs_bodies_hot": true,
	"request_logs_archive":     true,
	"_request_logs_archive":    true,
	"request_logs_id_seq":      true,
	"_request_logs_id_seq":     true,
	// ⚠ `request_logs_with_current_month` **不在**这张表里：它是**活着的**
	// canonical 视图（890 之后是会话族两臂体），不是 v1 裸表。
	// 把视图名塞进「v1 关系」清单，正是本文件阴性对照要防的那类假阳性。
}

// mig882StripNoise 去掉 SQL 注释（`--` 与 `/* */`）与单引号字符串字面量，
// 只留下**真正下发给 PostgreSQL 的标识符**。
//
// 为什么连字符串字面量一起去：SQL 里 `'...'` 里的内容不是关系名；
// 留着它会让判据把「某个字符串里恰好写了 request_logs」判成读点。
func mig882StripNoise(sqlText string) string {
	// 块注释
	sqlText = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(sqlText, " ")
	// 行注释
	sqlText = regexp.MustCompile(`--[^\n]*`).ReplaceAllString(sqlText, " ")
	// 单引号字符串字面量（SQL 里的 '' 转义成两个单引号）
	sqlText = regexp.MustCompile(`'(?:[^']|'')*'`).ReplaceAllString(sqlText, " ")
	return sqlText
}

// mig882MentionedRelations 返回 SQL 里按**标识符精确匹配**命中的关系名。
//
// 用标识符切分（而不是 Contains）是为了不把
// `request_logs_with_current_month` 判成 `request_logs`。
func mig882MentionedRelations(sqlText string) []string {
	clean := mig882StripNoise(sqlText)
	ids := regexp.MustCompile(`[A-Za-z0-9_$]+`).FindAllString(clean, -1)
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if mig882V1Relations[id] && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// mig882DetectorSelfCheck 钉住判据本身：它必须**能**对真的 v1 读点报红，
// 且**不得**对 canonical 视图名报红（`request_logs_with_current_month`
// 不是 v1 裸表）。
//
// 这对断言是本文件所有负向断言的前提：如果判据恒不命中，
// 「没有 v1 读点」与「判据坏了」会长得一模一样。
func TestMigration882_DetectorSelfCheck(t *testing.T) {
	positive := "SELECT * FROM public._request_logs WHERE cost_usd < 0"
	if got := mig882MentionedRelations(positive); len(got) == 0 {
		t.Errorf("判据自检失败：对真 v1 读点 %q 没有报出任何 v1 关系 —— 负向断言会恒绿（量具坏了）", positive)
	}
	negative := "SELECT * FROM public.request_logs_with_current_month"
	if got := mig882MentionedRelations(negative); len(got) != 0 {
		t.Errorf("判据自检失败：对 canonical 视图名 %q 误报 v1 关系 %v —— 负向断言会假红", negative, got)
	}
	// 这条阴性对照必须是**非空洞**的：canonical 视图名里确实含 `request_logs`
	// 子串 ⇒ 用 `strings.Contains(s, "public.request_logs")` 的判据会在这里假红。
	// 把这件事钉死，将来有人把判据换成 Contains 时，本门会立刻讲清为什么不行。
	if !strings.Contains(negative, "request_logs") {
		t.Errorf("阴性对照失效：%q 里已不含 `request_logs` 子串，这条对照不再能证伪 naive Contains 判据", negative)
	}
}

// TestMigration882_IndexShapeIsStable 钉 ①：迁移文件自身没被改坏。
//
// ★ 注意：这条门**不再**守「索引服务检查」这件事（索引已随 v1 族消亡），
// 只守「迁移文件与它的 down/两份副本没被人改坏」——迁移链里它仍会跑，
// 跑出来的索引随即被 890 删掉。
func TestMigration882_IndexShapeIsStable(t *testing.T) {
	up := mig882Read(t, mig882Tree(t), filepath.Join("sql", "migrations", "startup", mig882Name))

	for _, want := range []string{
		"idx_request_logs_cost_negative",
		"ON public._request_logs",
		"(ts, credential_id, outbound_model)",
		"INCLUDE (cost_usd, cache_read_tokens, prompt_tokens)",
		"WHERE cost_usd < 0",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("迁移文件缺少索引形态要素 %q —— 882 的历史形态被改动，迁移链的历史记录失真", want)
		}
	}
	// 父表级联形态：不允许出现 532 式逐分区循环（未来的分区靠父表自动继承，
	// 若改回逐分片段，新分区会长不回索引——876 同款教训）。
	if strings.Contains(up, "pg_inherits") {
		t.Errorf("882 应建在分区父表上（自动级联+继承），不应出现逐分区 pg_inherits 循环")
	}
}

// TestMigration882_DownDropsSameIndex 钉 ②：down 删的是同名索引。
func TestMigration882_DownDropsSameIndex(t *testing.T) {
	down := mig882Read(t, mig882Tree(t), filepath.Join("sql", "migrations", "startup", mig882DownName))
	if !strings.Contains(down, "DROP INDEX IF EXISTS public.idx_request_logs_cost_negative") {
		t.Errorf("down 文件必须删除父表索引 idx_request_logs_cost_negative（级联分区子索引），实得: %s", down)
	}
}

// TestMigration882_IndexDiesWithTheV1Family 钉 ④：v1 族真删 ⇒ 索引必然消亡。
//
// 这条门钉住的是一个**事实**，防止以后出现「索引还在、表没了」的错觉：
// 890 的 DROP TABLE 是级联的，挂在 `_request_logs` 上的索引随之消失。
func TestMigration882_IndexDiesWithTheV1Family(t *testing.T) {
	root := mig882Tree(t)
	up885 := mig882Read(t, root, filepath.Join("sql", "migrations", "startup", "890_drop_request_logs_v1_family.sql"))

	// 890 的删表清单里必须含 `_request_logs`（动态 SQL 的名字数组）。
	if !strings.Contains(up885, "'_request_logs'") {
		t.Errorf("890 的删表清单里没有 '_request_logs' —— v1 族真删的前提不成立，882 索引的「随表消亡」结论失效")
	}
	if !strings.Contains(up885, "DROP TABLE IF EXISTS public.%I") {
		t.Errorf("890 没有用 DROP TABLE 删除 v1 族 —— 索引不会随表级联消亡")
	}
	// 反向确认：882 的索引确实挂在被删的那张表上（两条拼在一起才成结论）。
	up882 := mig882Read(t, root, filepath.Join("sql", "migrations", "startup", mig882Name))
	if !strings.Contains(up882, "ON public._request_logs") {
		t.Errorf("882 的索引已不挂在 public._request_logs 上 —— 「随表消亡」的结论需要重新评估")
	}
}

// TestMigration882_CheckSideMovedToSessionFamily 钉 ③（反转）：检查侧已迁会话族。
//
// 原门钉的是「检查必须读 `public._request_logs r`」——那是 882 索引的
// 对面。890 之后那半边没了 ⇒ 断言必须**反转**：
//
//	· 负向：检查侧**不再**读 v1 族（配判据自检作为阳性对照）；
//	· 正向：检查侧读会话族窄源（hot ∪ parent）、谓词仍是
//	  `cost_usd < 0` + 30 天窗。
func TestMigration882_CheckSideMovedToSessionFamily(t *testing.T) {
	root := mig882Tree(t)
	checkPath := filepath.Join(root, "bg", "routing_health_checks.go")
	literals := goStringLiterals(t, checkPath)

	var hits []string
	for _, lit := range literals {
		hits = append(hits, mig882MentionedRelations(lit)...)
	}
	if len(hits) > 0 {
		t.Errorf("bg/routing_health_checks.go 仍在读 v1 族关系 %v —— 890 之后这些关系已不存在 ⇒ 必然 42P01", hits)
	}

	// 正向：会话族窄源 + 原谓词。
	joined := strings.Join(literals, "\n")
	for _, want := range []string{
		"public.session_turns_hot",
		"public.session_turns",
		"cost_usd < 0",
		"interval '30 days'",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("负成本检查缺少 %q —— 检查侧迁会话族后形态变了，配套索引必须按新形态评估", want)
		}
	}
}

// TestMigration882_NegativeCostIndexNowOnSessionFamily 钉 ⑤：等价不变量本体。
//
// 882 的索引死了，但「负成本检查要有部分索引兜着」这件事还活着 ⇒
// 会话族必须有自己的 `cost_usd < 0` 部分索引。
//
// 这条门**按内容发现**（扫启动迁移目录里所有非 down 的 .sql，找
// 「建在会话族上的 `cost_usd < 0` 部分索引」），不写死文件名 ⇒
// 迁移改名/换号不会让它假红；而它一旦**整体消失**就会红。
func TestMigration882_NegativeCostIndexNowOnSessionFamily(t *testing.T) {
	root := mig882Tree(t)
	dir := filepath.Join(root, "sql", "migrations", "startup")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读 %s 失败 %v", dir, err)
	}

	createIdx := regexp.MustCompile(`(?is)CREATE\s+INDEX(?:\s+IF\s+NOT\s+EXISTS)?\s+[^\s]+\s+ON\s+[^;]{0,600}?;`)
	rel := regexp.MustCompile(`(?i)\b(session_turns|session_turns_hot)\b`)
	negPred := regexp.MustCompile(`(?i)WHERE\s+cost_usd\s*<\s*0`)

	var found []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".down.sql") {
			continue
		}
		if name == mig882Name {
			continue // 882 的索引挂在 v1 族上，不算
		}
		body := mig882StripNoise(mig882Read(t, root, filepath.Join("sql", "migrations", "startup", name)))
		for _, stmt := range createIdx.FindAllString(body, -1) {
			if rel.MatchString(stmt) && negPred.MatchString(stmt) {
				found = append(found, name)
				break
			}
		}
	}
	if len(found) == 0 {
		t.Fatalf("启动迁移里找不到「建在会话族上的 cost_usd < 0 部分索引」—— 882 的索引随 v1 族消亡后，这个不变量必须由会话族自己承担（否则负成本检查退回全表扫描）")
	}
	t.Logf("会话族负成本部分索引由这些迁移承担: %v", found)
}

// TestMigration882_EmbeddataCopyIdentical 钉 ⑥：两份字节一致。
func TestMigration882_EmbeddataCopyIdentical(t *testing.T) {
	root := mig882Tree(t)
	for _, name := range []string{mig882Name, mig882DownName} {
		canonical := mig882Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embedded := mig882Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if canonical != embedded {
			t.Errorf("%s 的 canonical 与 installer embeddata 两份不一致 —— 只改一份会让装出来的库与仓库分叉", name)
		}
	}
}
