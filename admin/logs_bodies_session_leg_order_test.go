package admin

import (
	"os"
	"strings"
	"testing"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

// logs_bodies_session_leg_order_test.go —— 正文读路径的**取源**钉子。
//
// # 历史（2026-10-18，S4 停写 bodies 顺序）
//
// 缺陷形态（154 生产实证）：三处 body 读路径原顺序都是「v1 hot → v1 冷扫/视图
// → session 兜底」，且 session 兜底挂在冷扫 **ErrNoRows** 分支下。停写后新请求
// 正文只落 session_bodies，v1 冷扫对它们是证明缺席——columnar 分区无 btree，
// 单 id miss 实测 59s > 20s/30s ctx，超时错误不是 ErrNoRows ⇒ session 兜底
// 永远够不着 ⇒ unified 全量 500、getLog 正文 20s 白等后吞空。
//
// 修复形态：session 腿提前到冷扫/视图之前（hot 之后）。
//
// ★ 2026-10-20（迁移 890，v1 族真删）：v1 族整体退役 ⇒ **冷扫那条腿本身
// 不存在了** ⇒ 「session 腿必须在冷扫腿之前」这个不变式**没有对象了**
//（只剩一条腿，谈不上先后）。
//
// ⇒ 本门从「顺序」**反转**为「**只剩会话族一条腿**」——
//
//	① 正向：会话族腿必须在场（否则正文查不到）；
//	② 负向：v1 族关系名必须**不在场**（冷扫腿被谁加回来都会红：
//	   v1 表已 DROP ⇒ 读它 = 42P01；而冷扫那条本来就是「证明缺席」——
//	   columnar 分区无 btree，154 实测单 id miss 59s > 20s ctx，
//	   超时不是 ErrNoRows ⇒ 挂在 ErrNoRows 分支下的兜底永远够不着）。
//
// pgxmock 不执行 SQL，行级判据对顺序/取源是假绿，故钉源码文本（与被测对象同层）。

func funcBody(t *testing.T, file, name string) string {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	src := string(raw)
	start := strings.Index(src, "func "+name)
	if start < 0 {
		t.Fatalf("function %s not found in %s", name, file)
	}
	end := strings.Index(src[start+1:], "\nfunc ")
	if end < 0 {
		t.Fatalf("function %s end not found in %s", name, file)
	}
	return src[start : start+1+end]
}

// sessionLegViolations 返回「这条读路径不该出现的东西」：
//   - 会话族腿缺席（v1 腿也不在 ⇒ 什么都查不到）
//   - 已退役 v1 族关系名在场（冷扫腿复活）
//
// 抽成独立函数是为了让**阳性对照**能就地调用它（否则「负向判据」只能靠
// 真代码触发一次，判据本身坏了也没人知道）。
func sessionLegViolations(body string) []string {
	var problems []string
	code := stripGoComments(body)
	// ① 会话族腿必须在场。两种形态：直接内联
	//    dbpkg.SessionFamilyBodiesSourceSQL()，或经 fetchSessionOutboundBody
	//    助手（助手内部就是它）。
	//    ⚠ 匹配**代码形态**（dbpkg. 前缀 + 调用括号）而不是裸类型名 ——
	//    注释里会裸提 SessionFamilyBodiesSourceSQL，裸名匹配会被留在原地的
	//    注释击穿（M7 变异实测：代码块被搬走、注释留下，判据仍绿）。
	sess := strings.Index(code, "dbpkg.SessionFamilyBodiesSourceSQL()")
	if sess < 0 {
		sess = strings.Index(code, "fetchSessionOutboundBody(")
	}
	if sess < 0 {
		// 也接受**内联展开**的会话族正文源：hot 腿是这一族的标志
		// （db.SessionFamilyBodiesSourceSQL() 展开后就是这个形态）。
		sess = strings.Index(code, "session_bodies_hot")
	}
	if sess < 0 {
		problems = append(problems, "会话族正文腿缺席（既不内联 SessionFamilyBodiesSourceSQL()，也不经 fetchSessionOutboundBody() 助手，"+
			"活代码里也找不到 session_bodies_hot 这条腿）")
	}
	// ② 已退役 v1 族关系名不得在场（按标识符边界）。
	if hits := retiredV1RelationsInSQL(code); len(hits) > 0 {
		problems = append(problems, "仍引用已退役 v1 族关系名 "+strings.Join(hits, " "))
	}
	return problems
}

func requireSessionLegOnly(t *testing.T, body, funcName string) {
	t.Helper()
	if problems := sessionLegViolations(body); len(problems) > 0 {
		t.Errorf("%s 的正文读路径形状不对：%s\n"+
			"★ 2026-10-20（890）：v1 族被 DROP ⇒ 冷扫腿必须消失（读它 = 42P01；"+
			"而冷扫那条本来就是「证明缺席」——columnar 分区无 btree，154 实测单 id "+
			"miss 59s > 20s ctx，超时不是 ErrNoRows ⇒ 挂在 ErrNoRows 分支下的兜底永远够不着）；"+
			"正文只可能来自会话族。",
			funcName, strings.Join(problems, "；"))
	}
}

func TestFetchRequestOutboundBodySessionLegBeforeColdLeg(t *testing.T) {
	requireSessionLegOnly(t,
		funcBody(t, "logs.go", "(h *Handler) fetchRequestOutboundBody"),
		"fetchRequestOutboundBody")
}

func TestFetchRequestBodiesSessionLegBeforeColdLeg(t *testing.T) {
	requireSessionLegOnly(t,
		funcBody(t, "logs.go", "(h *Handler) fetchRequestBodies"),
		"fetchRequestBodies")
}

func TestUnifiedLoadOutboundBodySessionLegBeforeViewLeg(t *testing.T) {
	// loadOutboundBody 在 pgBodyReader 上；它的 session 腿直接内联
	// SessionFamilyBodiesSourceSQL。
	requireSessionLegOnly(t,
		funcBody(t, "unified_detail.go", "(r *pgBodyReader) loadOutboundBody"),
		"loadOutboundBody")
}

// TestSessionLegOnlyGuardHasTeeth 是上面三道门的**阳性对照**：
// 同一把尺子必须能抓住「v1 冷扫腿复活」与「会话族腿消失」这两种退化。
//
// ⚠ 没有这一条，上面三道门就只是「形状没变」的同义反复 —— 而形状没变
// 这件事本身不足以证明尺子有牙（判据纪律：只断言「没坏」的判据，
// 在「干脆没做」面前永远是绿的）。
func TestSessionLegOnlyGuardHasTeeth(t *testing.T) {
	// ① v1 冷扫腿复活 ⇒ 必须被抓住。
	withColdLeg := "func f() string {\n" +
		"\treturn `\n" +
		"\t\tSELECT outbound_body::text\n" +
		"\t\t  FROM request_logs_bodies_with_current_month rb\n" +
		"\t\t WHERE request_id = $1\n" +
		"\t`\n" +
		"}"
	problems := sessionLegViolations(withColdLeg)
	found := false
	for _, p := range problems {
		if strings.Contains(p, "已退役 v1 族关系名") {
			found = true
		}
	}
	if !found {
		t.Errorf("尺子没抓住 v1 冷扫腿：%v", problems)
	}
	// ② 会话族腿消失（什么也不读）⇒ 必须被抓住。
	problems = sessionLegViolations("func f() string { return \"\" }")
	found = false
	for _, p := range problems {
		if strings.Contains(p, "会话族正文腿缺席") {
			found = true
		}
	}
	if !found {
		t.Errorf("尺子没抓住「会话族腿消失」：%v", problems)
	}
	// ③ 正常形态 ⇒ 不得报任何问题（尺子不是恒红）。
	healthy := "func f() string {\n" +
		"\treturn `\n" +
		"\t\tSELECT outbound_body::text\n" +
		"\t\t  FROM " + dbpkg.SessionFamilyBodiesSourceSQL() + "\n" +
		"\t\t WHERE request_id = $1\n" +
		"\t`\n" +
		"}"
	if problems := sessionLegViolations(healthy); len(problems) > 0 {
		t.Errorf("尺子对正常形态报了问题：%v", problems)
	}
}
