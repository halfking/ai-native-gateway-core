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
// `sql/objects/views/request_logs_with_current_month.sql` 的**世代**守卫。
//
// # 世代史（三代，每一代的判据都反过向）
//
// R89-DP（205 号）：本文件登记为「v1 回退体（两条臂）」，并明说它
// **不是**部署形态——当时的部署形态是三臂（会话两臂 ∪ v1 臂）。
// 起因：203 号与 204 号都把这个文件当「该视图的定义」引用，204 号甚至从它
// 逐列核对了 6 个列号；而它描述的结构在**有 session_turns 的库里根本不存在**。
//
// 890（2026-10-20，v1 族最终删除）：第三臂（v1 臂）连同它的双反连接去重
// 一起失去对象 ⇒ 部署形态收敛为**两条会话臂**
// （`session_turns_hot ∪ session_turns` + 734 details LEFT JOIN）。
// ⇒ 「本文件 ≠ 部署形态」这个前提**不再成立**：本文件**就是**部署形态。
//
// ⇒ 本门从「它**不是**部署形态（判它为 v1 回退体）」**反转为**
//
//	「它**就是**部署形态（判它为会话族两臂体）」。
//
// # 为什么这个门必须自带反向对照
//
// 判据是「这份 dump 属于哪一代」。若抽取器坏了（例如 FROM 正则改了写法就
// 抓不到），「是会话族」会与「一条臂都没抓到」同时为真 ⇒ 门安静通过。
// 所以本测试在**每次运行**里就地跑负控 + 一条覆盖下限，证明判据真的有牙。
//
// # 权威来源（不在本文件）
//
//   - db/request_logs_view_schema.go            —— 二进制启动时保证 view 存在的 composer
//   - sql/migrations/startup/890_drop_request_logs_v1_family.sql
//   - db/request_logs_view_schema_test.go        —— 校验 composer 的契约
//
// # 与基线副本的**有意**不一致
//
// `sql/schema/01-schema.sql` 等三份基线副本仍写 **v1 体**（基线自洽：
// `CREATE VIEW` 急切校验引用的关系，基线时点 session_turns_hot 还不存在，
// 写成会话族会让**新装环境**报 42P01）。⇒ 本文件描述的是**部署后**形态，
// 基线描述的是**基线时点**形态。两者不一致是**有意的**，不是漂移。
const (
	verdictV1Fallback   = "v1-fallback-two-arm"
	verdictSessionFam   = "session-family-two-arm"
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
	// v1 两臂：两代物理名都算（迁移 860 把 dump 之外的运行库改名 `_` 前缀；
	// 迁移 890 又把 v1 族整体删掉了 —— 这个分支现在只在**负控**里出现，
	// 它的存在是让「两代仍可分辨」这件事可被验证）。
	case (seen["request_logs"] || seen["_request_logs"]) &&
		(seen["request_logs_hot"] || seen["_request_logs_hot"]):
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
// `sql/objects/views/` dump carries, and proves the Go composer carries the
// **same** generation.
//
// ★ 2026-10-20（890）：判据反转为「dump 与 composer **都**是会话族两臂体，
// 且**都**不含 v1 族」。此前它断言的是「dump 是 v1 回退体 + composer 有
// v1 臂 + 双反连接」——那正是 890 要拆掉的东西。
//
// 拆分两半的理由（两半单独看都不够）：
//
//   - dump 变会话族而 composer 没有 ⇒ 有人的 dump 与部署形态脱节，
//     而没有任何门能验证它们等价；
//   - composer 变会话族而 dump 没有 ⇒ dump 变成**谁也产不出的死件**。
func TestRequestLogsViewDumpGeneration(t *testing.T) {
	dumpPath := filepath.Join("..", "sql", "objects", "views", "request_logs_with_current_month.sql")
	raw := readRepoFile(t, dumpPath)

	// ── 覆盖下限：先证「抽到东西了」。抽取器坏掉时空集合同样满足任何判据。
	verdict, arms := viewDumpVerdict(raw)
	if len(arms) < 2 {
		t.Fatalf("只从 %s 抽到 %d 条源表（下限 2）：抽取器很可能已失效，"+
			"此时「是会话族」会与「一条都没抽到」同步为真。抽到的：%v", dumpPath, len(arms), arms)
	}
	if verdict != verdictSessionFam {
		t.Fatalf("%s 的世代判定 = %q（源表 %v），期望 %q。\n"+
			"  890 之后 canonical 视图只有两条会话臂（session_turns_hot ∪ session_turns\n"+
			"  + 734 details LEFT JOIN），v1 第三臂已随 v1 族一起退役。\n"+
			"  权威定义是 db/request_logs_view_schema.go（composer）+ \n"+
			"  sql/migrations/startup/890_drop_request_logs_v1_family.sql。\n"+
			"  若本文件被换回 v1 体，它就会变成**谁也产不出的死件**——"+
			"读它的人会照抄一个库里根本不存在的结构（203/204/205 号都栽在这里）。",
			dumpPath, verdict, arms, verdictSessionFam)
	}

	// ── dump 侧：**不得**再有 v1 族的基表引用 ────────────────────────────
	//
	// 判据是「独立标识符」而不是朴素包含：`public.request_logs` 是
	// `public.request_logs_with_current_month` 的**前缀**，朴素包含会把
	// 「必须在场」的 canonical 视图名判成「必须不在场」。
	for _, unwanted := range []string{
		"public._request_logs", "public.request_logs",
		"_request_logs_hot", "request_logs_hot",
		"_request_logs_bodies", "request_logs_bodies",
	} {
		if containsIdentifierIn([]string{stripSQLComments(raw)}, unwanted) {
			t.Errorf("%s 仍引用 v1 族 %q —— v1 族已被迁移 890 整体 DROP，"+
				"任何提到它的语句整条 42P01（不是那条腿失败）。", dumpPath, unwanted)
		}
	}
	// v1 臂的两个反连接（去重机制）随 v1 臂一起退役：它们的作用是
	// 「同一逻辑请求在会话臂与 v1 臂各出一行」，而 v1 臂已经不存在。
	if strings.Contains(stripSQLComments(raw), "NOT EXISTS") {
		t.Errorf("%s 仍带 NOT EXISTS：v1 臂的双反连接去重随 v1 臂一起退役，"+
			"留下它只会让人以为这个视图还有第三条臂。", dumpPath)
	}

	// 指针注释必须点名权威来源——并且由门来守。注释不是契约，但**可以被
	// 契约引用**：这里断言它存在，是为了让它不会在某次重新 dump 时静默腐烂。
	if !strings.Contains(raw, "request_logs_view_schema.go") {
		t.Errorf("%s 缺少指向权威定义（db/request_logs_view_schema.go）的指针注释。"+
			"该文件长得像权威定义却不是，读它的人会照抄一个在有 session_turns 的库里"+
			"不存在的结构（203/204 号都栽在这里）。", dumpPath)
	}

	// ── composer 侧：必须与 dump **同代** ───────────────────────────────
	composer := readRepoFile(t, "request_logs_view_schema.go")

	mustHave := []struct {
		needle string
		why    string
	}{
		{"FROM public.session_turns_hot t", "canonical 会话 hot 臂"},
		{"FROM public.session_turns t", "canonical 会话 cold 臂"},
		{"session_turn_details", "734 details 特征层 LEFT JOIN"},
		{"bodyHasV1Arm", "探针必须能看出「装着的还是 v1 臂视图」并替换它"},
		{"bodyIsSessionOnly", "探针必须能看出「已是会话族体」并短路"},
	}
	for _, m := range mustHave {
		if !strings.Contains(composer, m.needle) {
			t.Errorf("db/request_logs_view_schema.go 不再包含 %q（%s）。\n"+
				"  这意味着视图体的形态集合变了：%s 的登记前提已不成立，"+
				"必须重新判定它属于哪一代（而不是让它继续自称「视图定义本体」）。",
				m.needle, m.why, dumpPath)
		}
	}

	// composer 侧同样**不得**有 v1 族与它的包装链：那些形状的对象已随 890
	// 一起消失，而 `CREATE OR REPLACE VIEW` 是**即时**校验的 ⇒ composer
	// 里留着它们会让整个 boot 失败（42P01）。
	//
	// ⚠ 量具必须是**字符串字面量集合**（go/ast），不是整份源码：
	// composer 的注记文化是**大量**在注释里讲历史（讲清楚「为什么这样改」），
	// 而那些历史名词恰好就是要禁的那些——例如
	// `// v1  _request_logs_bodies : request_id text NOT NULL, …` 是
	// **形状对照表**，不是可执行语句。
	// `stripSQLComments` 剥的是 **SQL 注释**（`--` / `/* */`），剥不掉
	// **Go 注释**（`//`）⇒ 用它当量具会把注记判成活代码（假阳性，方向是
	// 「改对代码后门反而更红」）。
	composerLits := goStringLiterals(t, "request_logs_view_schema.go")
	for _, unwanted := range []string{
		"public._request_logs", "public.request_logs",
		"_request_logs_hot", "request_logs_hot",
		"_request_logs_bodies",
		"request_logs_with_current_month_without_customer_id",
		"request_logs_with_current_month_without_request_class_due_at",
	} {
		if containsIdentifierIn(composerLits, unwanted) {
			t.Errorf("db/request_logs_view_schema.go 的 SQL 字面量里仍出现 %q —— v1 族与它的包装链已随 890 退役。",
				unwanted)
		}
	}

	// 覆盖下限（composer 侧）：上面是若干条独立断言，若某条判据退化成恒真，
	// 「都还在」会与「什么都没查」同解。给一个可观测的计数下限。
	if n := strings.Count(composer, "FROM public.session_turns"); n < 4 {
		t.Errorf("composer 里只数到 %d 处 `FROM public.session_turns`（下限 4）："+
			"会话族形态的判据很可能已失效，此时上面几条断言可能一起变成恒真。", n)
	}
}

// TestViewDumpVerdictDiscriminates 是本门的鉴别力证明，与主断言同源同函数：
// 构造两份**故意错误**的文本，断言判据会把它们判成「不是会话族体」。
// 这两条负控每次运行都跑，不需要临时改文件、不需要外部 fixture。
func TestViewDumpVerdictDiscriminates(t *testing.T) {
	t.Run("负控1_两代v1体不得被判为会话族", func(t *testing.T) {
		// 模拟「有人把 dump 换回 v1 回退体」：两条 v1 臂。
		got, arms := viewDumpVerdict(
			"CREATE VIEW public.request_logs_with_current_month AS SELECT 1 FROM public._request_logs_hot " +
				"UNION ALL SELECT 1 FROM public._request_logs")
		if got == verdictSessionFam {
			t.Fatalf("两代 v1 体被判为 %q（源表 %v）——"+
				"判据没有牙：它分不出「v1 回退体」与「会话族体」，"+
				"于是把 dump 换回 v1 体的动作会静默通过。", got, arms)
		}
		if got != verdictV1Fallback {
			t.Errorf("两代 v1 体判定 = %q，期望 %q（源表 %v）", got, verdictV1Fallback, arms)
		}
	})

	t.Run("负控2_单臂不得被判为会话族", func(t *testing.T) {
		// 判据不是「出现过 session_turns 就算数」：少一条臂必须是未知形态。
		got, arms := viewDumpVerdict("CREATE VIEW v AS SELECT 1 FROM public._request_logs;")
		if got == verdictSessionFam {
			t.Fatalf("单臂 `FROM _request_logs` 被判为 %q（源表 %v）——"+
				"判据退化成子串匹配，任意含 v1 关系的文件都会通过", got, arms)
		}
		if got != verdictUnknownShape {
			t.Errorf("单臂判定 = %q，期望 %q", got, verdictUnknownShape)
		}
	})
}
