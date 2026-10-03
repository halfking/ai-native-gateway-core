package db

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// R89-DP（205 号）：`sql/objects/views/request_logs_with_current_month.sql`
// **不是**部署形态的视图定义——它是 v1 回退体（两条腿）。
//
// 起因：203 号与 204 号都把这个文件当「该视图的定义」引用，204 号甚至从它
// 逐列核对了 6 个列号。实测部署形态根本不是它：
//
//	部署（v3，734 details 在场时）= db/request_logs_view_schema.go:799-822
//	  三条臂：session_turns_hot ∪ session_turns ∪ (v1 臂)
//	  且 v1 臂带**双反连接**去重：
//	    WHERE NOT EXISTS (… FROM public.session_turns_hot th WHERE th.request_id = rl.request_id)
//	      AND NOT EXISTS (… FROM public.session_turns tp WHERE tp.request_id = rl.request_id)
//
//	本文件（v1 回退体，session_turns 缺表时由 Go 侧保留/重建）
//	  两条臂：request_logs_hot ∪ request_logs，无反连接。
//
// ⇒ 后果不是「文件写错了」（回退体是**仍然可产出**的合法形态：
//
//	request_logs_view_schema.go:213-225「无会话族：维持 v1 体」），而是
//	**它长得就像权威定义，而没有任何机制会喊**。这是本仓最贵的一种失效：
//	读它的人（包括前两轮的我）会照抄一个在有 session_turns 的库里根本不
//	存在的结构去论证安全性。
//
// 为什么这个门必须自带反向对照：判据是「这份 dump 属于哪一代」。若抽取器
// 坏掉（例如 FROM 正则改了写法就抓不到），「不是 session-family」会与
// 「一条臂都没抓到」同时为真 ⇒ 门安静通过。所以本测试在**每次运行**里就地
// 跑两条负控 + 一条覆盖下限，证明判据真的有牙。
//
// 权威来源（不在本文件）：
//   - db/request_logs_view_schema.go          —— 二进制启动时保证 view 存在的 composer
//   - sql/migrations/startup/734_request_logs_view_details_join.sql
//   - db/view_schema_v2_contract_test.go      —— 校验 composer ↔ migration 等价
//
// **没有任何门**把 `sql/objects/views/*.sql` 钉到上述来源（实测：全仓对
// 本文件的引用只有 sqlreadguard 的一条 LEGIT 豁免）。
const (
	verdictV1Fallback   = "v1-fallback-two-arm"
	verdictSessionFam   = "session-family-three-arm"
	verdictUnknownShape = "unknown-shape"
)

// stripSQLComments 去掉 `--` 行注释与 `/* */` 块注释。**必须**先剥注释再判
// 形态：本文件顶部就有一个人写的指针注释提到 `session_turns`，用它当判据
// 会把自己的说明当成证据（注释不是契约）。
func stripSQLComments(src string) string {
	src = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(src, " ")
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// viewFromRe 抓 FROM|JOIN 后的物理源表名。LATERAL/ONLY 等关键字不是源表。
var viewFromRe = regexp.MustCompile(`(?i)\b(?:FROM|JOIN)\s+(?:public\.)?([a-z_][a-z0-9_]*)`)

var nonSourceKeywords = map[string]bool{
	"lateral": true, "only": true, "select": true, "values": true,
}

// viewDumpVerdict 判定一份视图 SQL 文本属于哪一代。**故意**做成纯函数：
// 负控在同一个测试里直接喂构造文本，不需要临时改文件。
func viewDumpVerdict(src string) (string, []string) {
	bare := stripSQLComments(src)
	seen := map[string]bool{}
	for _, m := range viewFromRe.FindAllStringSubmatch(bare, -1) {
		name := strings.ToLower(m[1])
		if nonSourceKeywords[name] {
			continue
		}
		seen[name] = true
	}
	arms := make([]string, 0, len(seen))
	for n := range seen {
		arms = append(arms, n)
	}
	sort.Strings(arms)

	sessionArms := 0
	for _, a := range arms {
		if a == "session_turns" || a == "session_turns_hot" ||
			a == "session_turn_details" || a == "session_turn_details_hot" {
			sessionArms++
		}
	}
	switch {
	case sessionArms > 0:
		return verdictSessionFam, arms
	case seen["request_logs"] && seen["request_logs_hot"]:
		return verdictV1Fallback, arms
	default:
		return verdictUnknownShape, arms
	}
}

func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// TestRequestLogsViewDumpGeneration pins which generation the checked-in
// `sql/objects/views/` dump carries, and proves the Go composer can still
// produce *both* generations. Either half alone is silently wrong:
//
//   - dump 变成 session-family 而没人说 ⇒ 有人从 v3 库重新 dump 了，而
//     没有任何门能验证它与 composer 等价；
//   - composer 删掉 v1 分支而没人说 ⇒ 本文件变成**谁也产不出的死件**，
//     而它仍在 sqlreadguard 白名单里被登记为「视图定义本体」。
func TestRequestLogsViewDumpGeneration(t *testing.T) {
	dumpPath := filepath.Join("..", "sql", "objects", "views", "request_logs_with_current_month.sql")
	raw := readRepoFile(t, dumpPath)

	// ── 覆盖下限：先证「抽到东西了」。抽取器坏掉时空集合同样满足任何判据。
	verdict, arms := viewDumpVerdict(raw)
	if len(arms) < 2 {
		t.Fatalf("只从 %s 抽到 %d 条源表（下限 2）：抽取器很可能已失效，"+
			"此时「不是 session-family」会与「一条都没抽到」同步为真。抽到的：%v", dumpPath, len(arms), arms)
	}
	if verdict != verdictV1Fallback {
		t.Fatalf("%s 的世代判定 = %q（源表 %v），期望 %q。\n"+
			"  处置：**不要**直接把从 v3 库 dump 出来的三臂体提交到这里。\n"+
			"  权威定义是 db/request_logs_view_schema.go（composer）+ "+
			"sql/migrations/startup/734_request_logs_view_details_join.sql，"+
			"二者的等价性由 db/view_schema_v2_contract_test.go 守着；"+
			"本文件**没有任何门**与那份权威定义比对。\n"+
			"  二选一：(a) 保留 v1 回退世代并在本文件注明它只服务于 session_turns 缺表的库；"+
			"(b) 删掉本文件，并停止把 sql/objects/views/ 当作该视图的部署 SSOT。\n"+
			"  无论哪种，都请同步更新 internal/sqlreadguard 白名单里对应条目的理由。",
			dumpPath, verdict, arms, verdictV1Fallback)
	}

	// 反连接是**部署形态真正的去重机制**。若 v1 臂的反连接被摘掉，
	// 「同一逻辑请求在会话臂与 v1 臂各出一行」就会静默发生——而本文件
	// （无反连接）看上去完全正常，正好会掩盖这件事。
	if strings.Contains(stripSQLComments(raw), "NOT EXISTS") {
		t.Errorf("%s 突然带上了 NOT EXISTS：它可能已被换成 v3 会话体，"+
			"上面的世代判定也应当已经变红。若确系有意替换，请连同 sqlreadguard "+
			"白名单理由一起更新，并说明权威来源。", dumpPath)
	}

	// 指针注释必须点名权威来源——并且由门来守。注释不是契约，但**可以被
	// 契约引用**：这里断言它存在，是为了让它不会在某次重新 dump 时静默腐烂。
	if !strings.Contains(raw, "request_logs_view_schema.go") {
		t.Errorf("%s 缺少指向权威定义（db/request_logs_view_schema.go）的指针注释。"+
			"该文件长得像权威定义却不是，读它的人会照抄一个在有 session_turns 的库里"+
			"不存在的结构（203/204 号都栽在这里）。", dumpPath)
	}

	// ── composer 侧：两代都必须仍然可产出 ──────────────────────────────
	composer := readRepoFile(t, "request_logs_view_schema.go")

	mustHave := []struct {
		needle string
		why    string
	}{
		{"FROM public.session_turns_hot t", "canonical 会话 hot 臂"},
		{"FROM public.session_turns t", "canonical 会话 cold 臂"},
		{"NOT EXISTS (SELECT 1 FROM public.session_turns_hot th", "v1 臂对会话 hot 的反连接去重"},
		{"NOT EXISTS (SELECT 1 FROM public.session_turns tp", "v1 臂对会话 cold 的反连接去重"},
		{"CREATE VIEW public.request_logs_with_current_month_without_customer_id AS", "v1 回退体的基础包装（两臂重建）"},
	}
	for _, m := range mustHave {
		if !strings.Contains(composer, m.needle) {
			t.Errorf("db/request_logs_view_schema.go 不再包含 %q（%s）。\n"+
				"  这意味着视图体的形态集合变了：%s 的登记前提已不成立，"+
				"必须重新判定它属于哪一代（而不是让它继续自称「视图定义本体」）。",
				m.needle, m.why, dumpPath)
		}
	}

	// 覆盖下限（composer 侧）：上面是 5 条独立断言，若某条判据退化成恒真，
	// 「都还在」会与「什么都没查」同解。给一个可观测的计数下限。
	if n := strings.Count(composer, "FROM public.session_turns"); n < 4 {
		t.Errorf("composer 里只数到 %d 处 `FROM public.session_turns`（下限 4）："+
			"会话族形态的判据很可能已失效，此时上面 5 条断言可能一起变成恒真。", n)
	}
}

// TestViewDumpVerdictDiscriminates 是本门的鉴别力证明，与主断言同源同函数：
// 构造两份**故意错误**的文本，断言判据会把它们判成「不是 v1 回退体」。
// 这两条负控每次运行都跑，不需要临时改文件、不需要外部 fixture。
func TestViewDumpVerdictDiscriminates(t *testing.T) {
	real := readRepoFile(t, "..", "sql", "objects", "views", "request_logs_with_current_month.sql")

	t.Run("负控1_三臂会话体不得被判为v1回退体", func(t *testing.T) {
		// 模拟「有人从 v3 库重新 dump」：往真文件里插入两条会话臂。
		mutated := strings.Replace(real,
			"   FROM public.request_logs_hot",
			"   FROM public.session_turns_hot t\nUNION ALL\n SELECT 1\n   FROM public.session_turns t\nUNION ALL\n SELECT 1\n   FROM public.request_logs_hot",
			1)
		if mutated == real {
			t.Skip("锚点 `   FROM public.request_logs_hot` 已不在 dump 里；" +
				"世代判定的主断言会先变红，此负控随之失效——请更新它")
		}
		got, arms := viewDumpVerdict(mutated)
		if got == verdictV1Fallback {
			t.Fatalf("把三臂会话体注入 dump 后判据仍返回 %q（源表 %v）——"+
				"判据没有牙：它分不出「v1 回退体」与「会话族体」，"+
				"于是替换成 canonical 的动作会静默通过。", got, arms)
		}
		if got != verdictSessionFam {
			t.Errorf("注入会话臂后判定 = %q，期望 %q（源表 %v）", got, verdictSessionFam, arms)
		}
	})

	t.Run("负控2_单臂不得被判为v1回退体", func(t *testing.T) {
		// 判据不是「出现过 request_logs 就算数」：少一条臂必须是未知形态。
		got, arms := viewDumpVerdict("CREATE VIEW v AS SELECT 1 FROM public.request_logs;")
		if got == verdictV1Fallback {
			t.Fatalf("单臂 `FROM request_logs` 被判为 %q（源表 %v）——"+
				"判据退化成子串匹配，任意含 request_logs 的文件都会通过", got, arms)
		}
		if got != verdictUnknownShape {
			t.Errorf("单臂判定 = %q，期望 %q", got, verdictUnknownShape)
		}
	})
}
