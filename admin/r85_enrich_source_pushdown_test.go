package admin

// r85_enrich_source_pushdown_test.go —— R85：`/api/admin/turns/sessions` 的
// **api_key enrich 腿**把 session 谓词推进 source 内部（契约门）。
//
// # 这道门守的是什么
//
// R84 只修掉了主列表查询（32s → 8.3ms），端点仍要 ~21s。生产增量读数
// （pg_stat_statements 差分，打 3 次端点，252 只读）显示剩余时间**全在 enrich 腿**：
//
//	SELECT DISTINCT ON (t.session_id) …   每次 **20,176 ms** ← 端点的全部
//	SELECT s.session_id, s.tenant_id, …    每次      **8.3 ms**
//	SELECT t.session_id, t.turn_no, …      每次      **4.2 ms**
//
// 根因：enrich 腿的 source 是 `SessionFamilyTurnsSourceSQL()` ——
// **session_turns_hot UNION ALL session_turns 父表的全部历史分区**，
// 而 session 谓词在 source **外部** ⇒ 规划器把整条 UNION ALL 物化后再 Hash Join。
//
// 单变量 A/B（252 EXPLAIN ANALYZE，同 21 个真实 session_id）：
//	A 谓词在外部：11,478.486 ms（Hash Join，估算 1,587,817 行）
//	B 谓词推进内部：     6.369 ms（Hash Join，估算      1,070 行）
//
// # 为什么这道门不是「一个字符串断言」
//
// 这类改动的失败形态是**无声的**：
//
//	① 只推进**一条腿**（hot 推了、父表没推）⇒ UNION ALL 的另一条腿照样全扫
//	   ⇒ 端点仍然慢，但**没有任何报错**；
//	② 推进谓词时**顺手改了投影**（列集合/列序变了）⇒ 调用方的 SELECT 列表
//	   与扫描结果错位 ⇒ 可能 42703，也可能更糟：静默返回错列；
//	③ 推进谓词时**顺手改了 JOIN 条件** ⇒ 入选行集合变了 ⇒ 展示的 api_key 变了。
//
// 所以本门钉三件事：**两条腿都推**、**投影逐字同源**、**JOIN 条件不变**。
//
// # 判据纪律（本门自身）
//
// 检测器抽成纯函数，喂坏样本走同一把尺子（阳性对照），证明它有鉴别力。

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/db"
)

// r85ProjectionOf 剥出 source 的投影部分（第一个 " FROM " 之前的全部内容）。
//
// ★ 为什么用同一个尺子取投影：断言与被测对象必须共用一套切分规则，
//
//	否则「投影一致」这条断言测的是自己。
func r85ProjectionOf(src string) string {
	i := strings.Index(src, " FROM ")
	if i < 0 {
		return ""
	}
	return src[:i]
}

// r85CheckSource 校验数组版 source 的形态，返回问题描述列表（空 = 合格）。
func r85CheckSource(src string) []string {
	var problems []string

	// ① 两条腿**都**必须带 session 谓词。
	//
	// ★ 计数必须精确为 2：少了 ⇒ 只推进了一半（端点仍慢且不报错）；
	//   多了 ⇒ 说明谓词被塞进了投影里（语义已变）。
	pushRE := regexp.MustCompile(`WHERE\s+t\.session_id\s*=\s*ANY\(\$1\)`)
	if n := len(pushRE.FindAllString(src, -1)); n != 2 {
		problems = append(problems, "session 谓词 `t.session_id = ANY($1)` 应在两条腿各出现一次（实测 "+strconv.Itoa(n)+" 次）—— "+
			"少一次 = 只推进了一半 ⇒ UNION ALL 的另一条腿照样全扫 ⇒ 端点仍然慢且**不报任何错**")
	}

	// ② 谓词必须是 `= ANY($1)` 而不是 `= $1`（本 helper 是数组版）。
	if regexp.MustCompile(`WHERE\s+t\.session_id\s*=\s*\$1\b`).MatchString(src) {
		problems = append(problems, "谓词写成 `t.session_id = $1`（单值）—— 本 helper 契约是数组版（`= ANY($1)`）")
	}

	// ③ 投影必须与全量 source **逐字同源**（同一 sessionFamilyProjection(true)）。
	base := r85ProjectionOf(db.SessionFamilyTurnsSourceSQL())
	got := r85ProjectionOf(src)
	if base == "" || got == "" {
		problems = append(problems, "剥不出投影部分（source 形态漂移）")
	} else if base != got {
		problems = append(problems, "投影与 SessionFamilyTurnsSourceSQL() **不逐字一致** —— 列集合/列序变了 ⇒ "+
			"调用方的 SELECT 列表会与扫描结果错位")
	}

	// ④ 两条腿的表必须仍是 hot + 父表（不能偷偷换掉数据面）。
	if !strings.Contains(src, "FROM public.session_turns_hot t") {
		problems = append(problems, "缺 hot 腿（FROM public.session_turns_hot t）")
	}
	if !strings.Contains(src, "FROM public.session_turns t") {
		problems = append(problems, "缺父表腿（FROM public.session_turns t）")
	}
	return problems
}

// TestR85_EnrichSourcePushesSessionPredicate 钉「新 helper 的形态」。
func TestR85_EnrichSourcePushesSessionPredicate(t *testing.T) {
	src := db.SessionFamilyTurnsForSessionsSQL()
	if problems := r85CheckSource(src); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("SessionFamilyTurnsForSessionsSQL：%s", p)
		}
	}
}

// TestR85_EnrichSourceDetectorRejectsBadShapes 阳性对照：三份坏样本都必须被判红。
func TestR85_EnrichSourceDetectorRejectsBadShapes(t *testing.T) {
	good := db.SessionFamilyTurnsForSessionsSQL()
	if len(r85CheckSource(good)) != 0 {
		t.Fatal("阳性对照失败：真 helper 被判为不合格 —— 检测器坏了")
	}

	// 坏样本①：只推进一条腿（父表腿的谓词被摘掉）⇒ 只推一半。
	bad1 := strings.Replace(good,
		" AND d.partition_date = t.partition_date WHERE t.session_id = ANY($1))",
		" AND d.partition_date = t.partition_date)", 1)
	if len(r85CheckSource(bad1)) == 0 {
		t.Error("坏样本①（只推进一条腿）被判合格 —— 检测器没有鉴别力")
	}

	// 坏样本②：谓词写成单值（`= $1`）⇒ 数组语义已坏。
	bad2 := strings.Replace(good, "t.session_id = ANY($1)", "t.session_id = $1", 1)
	if len(r85CheckSource(bad2)) == 0 {
		t.Error("坏样本②（谓词写成单值）被判合格 —— 检测器没有鉴别力")
	}

	// 坏样本③：投影被改了（多投影一列）⇒ 列集合变了。
	bad3 := strings.Replace(good, "t.request_id AS request_id", "t.request_id AS request_id, t.extra_col AS extra_col", 1)
	if len(r85CheckSource(bad3)) == 0 {
		t.Error("坏样本③（投影被改）被判合格 —— 检测器没有鉴别力")
	}
}

// TestR85_TurnsSessionsEnrichUsesPushedSource 钉调用点：
// enrich 查询必须用新 helper，JOIN 条件必须不变。
func TestR85_TurnsSessionsEnrichUsesPushedSource(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("turns_sessions.go"))
	if err != nil {
		t.Fatalf("读 turns_sessions.go: %v", err)
	}
	body := string(src)

	// ★ 断言范围**限定在 enrich 查询块内**（从 DISTINCT ON 到 ORDER BY 那一段）。
	//
	// ★ 为什么不能对整文件做「不含旧 helper」的断言：本文件里还有**另一处**
	//   合法使用 —— `?api_key_id=` 过滤用的 EXISTS 子句（buildTurnsSessionWhere）。
	//   那条是**带过滤**的路径（谓词按外层行相关），不在本次修复范围，
	//   若误判成「必须一起换」⇒ 门会要求一次没有验证过的口径相关改动。
	//   ⇒ 该残留路径**如实登记**在审计档「已知边界」里。
	lo := strings.Index(body, "DISTINCT ON (t.session_id)")
	hi := strings.Index(body, "ORDER BY t.session_id, t.turn_no DESC")
	if lo < 0 || hi < 0 || hi < lo {
		t.Fatal("读不到 enrich 查询块（DISTINCT ON … / ORDER BY …）—— 端点实现形态漂移了，请人工复核")
	}
	enrich := body[lo:hi]

	if !strings.Contains(enrich, "db.SessionFamilyTurnsForSessionsSQL()") {
		t.Error("enrich 查询块没有用 db.SessionFamilyTurnsForSessionsSQL() —— " +
			"全量 source 会被整体物化（实测 20,176 ms/次）")
	}
	if strings.Contains(enrich, "db.SessionFamilyTurnsSourceSQL()") {
		t.Error("enrich 查询块仍在用 db.SessionFamilyTurnsSourceSQL()（未推进谓词的全量 source）")
	}
	// JOIN 条件不变 —— 它决定入选行集合；改了就是口径变更。
	//
	// ★ 必须用**带终止符**的正则，不能用 Contains：
	//   `strings.Contains(enrich, "rl ON rl.request_id = t.request_id")`
	//   对「后面追加了 AND rl.tenant_id = t.tenant_id」的变体**照样匹配**
	//   ⇒ ⇒ 门会**假绿**（这正是 R85 变异 M4 实测抓到的洞）。
	joinRE := regexp.MustCompile(`rl ON rl\.request_id = t\.request_id\s*\n`)
	if !joinRE.MatchString(enrich) {
		t.Error("enrich 查询的 JOIN 条件变了（应为「`rl ON rl.request_id = t.request_id` 后直接换行」）—— " +
			"入选行集合变了 ⇒ 展示的 api_key 会变")
	}
	// 外层谓词仍要在（它限定外层 t 的行数）。
	if !strings.Contains(enrich, "WHERE t.session_id = ANY($1)") {
		t.Error("enrich 查询缺外层谓词 `WHERE t.session_id = ANY($1)`")
	}
}
