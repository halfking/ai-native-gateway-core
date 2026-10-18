package admin

import (
	"os"
	"strings"
	"testing"
)

// logs_bodies_session_leg_order_test.go —— 2026-10-18 S4 停写 bodies 顺序钉子。
//
// 缺陷形态（154 生产实证）：三处 body 读路径原顺序都是「v1 hot → v1 冷扫/视图
// → session 兜底」，且 session 兜底挂在冷扫 **ErrNoRows** 分支下。停写后新请求
// 正文只落 session_bodies，v1 冷扫对它们是证明缺席——columnar 分区无 btree，
// 单 id miss 实测 59s > 20s/30s ctx，超时错误不是 ErrNoRows ⇒ session 兜底
// 永远够不着 ⇒ unified 全量 500、getLog 正文 20s 白等后吞空。
//
// 修复形态：session 腿提前到冷扫/视图之前（hot 之后）。三类行各有归宿：
// 停写后行 ms 命中；镜像期行 session 即正典读源；镜像前行 session 索引 miss
// 后照走冷扫。判据钉「顺序」：SessionFamilyBodiesSourceSQL 的查询必须出现在
// request_logs_bodies_with_current_month 之前。pgxmock 不执行 SQL，行级判据
// 对顺序是假绿，故钉源码文本（与被测对象同层）。

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

func requireSessionLegBeforeColdLeg(t *testing.T, body, funcName string) {
	t.Helper()
	// session 腿两种形态：直接内联 dbpkg.SessionFamilyBodiesSourceSQL()，
	// 或经 fetchSessionOutboundBody 助手（助手内部就是它）。匹配**代码形态**
	//（dbpkg. 前缀 + 调用括号）而不是裸类型名——注释里会裸提
	// SessionFamilyBodiesSourceSQL，裸名匹配会被留在原地的注释击穿（M7
	// 变异实测：代码块被搬走、注释留下，判据仍绿）。
	sess := strings.Index(body, "dbpkg.SessionFamilyBodiesSourceSQL()")
	if sess < 0 {
		sess = strings.Index(body, "fetchSessionOutboundBody(")
	}
	cold := strings.Index(body, "request_logs_bodies_with_current_month")
	if sess < 0 || cold < 0 {
		t.Fatalf("%s 必须同时含 session 腿（SessionFamilyBodiesSourceSQL）与 v1 冷扫腿"+
			"（request_logs_bodies_with_current_month）", funcName)
	}
	if sess > cold {
		t.Fatalf("%s 的 session 腿必须在 v1 冷扫/视图腿**之前**：停写后新请求正文只在 "+
			"session_bodies，v1 columnar 冷扫对它们是证明缺席（154 实测单 id miss 59s "+
			"> 20s ctx），session 腿排在冷扫之后会因超时错误（非 ErrNoRows）永远够不着",
			funcName)
	}
}

func TestFetchRequestOutboundBodySessionLegBeforeColdLeg(t *testing.T) {
	requireSessionLegBeforeColdLeg(t,
		funcBody(t, "logs.go", "(h *Handler) fetchRequestOutboundBody"),
		"fetchRequestOutboundBody")
}

func TestFetchRequestBodiesSessionLegBeforeColdLeg(t *testing.T) {
	requireSessionLegBeforeColdLeg(t,
		funcBody(t, "logs.go", "(h *Handler) fetchRequestBodies"),
		"fetchRequestBodies")
}

func TestUnifiedLoadOutboundBodySessionLegBeforeViewLeg(t *testing.T) {
	// loadOutboundBody 在 pgBodyReader 上；它的 session 腿直接内联
	// SessionFamilyBodiesSourceSQL。
	requireSessionLegBeforeColdLeg(t,
		funcBody(t, "unified_detail.go", "(r *pgBodyReader) loadOutboundBody"),
		"loadOutboundBody")
}
