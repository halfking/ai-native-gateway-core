package bg

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// ── REFRESH 的会话画像分族（work_mem + jit）──────────────────────────────
//
// 缺陷形态（两个轴，生产只读实测）：
//
//	① work_mem：PG 侧 work_mem=8MB，而 session_task_stats 单次 REFRESH 实测溢写
//	   136MB 临时文件。work_mem 8MB ⇒ 必然落盘，是算术后果不是异常。
//
//	② jit：252 的 PG 库级配置 jit=on / jit_above_cost=100000，而
//	   routing_analytics_7d 的**定义查询**计划代价 193,601 ⇒ 超过阈值 1.93×
//	   ⇒ 每次刷新都付 JIT 编译代价（EXPLAIN 实测 JIT Functions:91），
//	   而整个查询只跑 ~2.4s ⇒ 编译代价与执行代价同量级。
//
// ★★ 2026-10-29（R175）把这两个轴从「全族共用的一个常量」改成**按族分族**，
//   与 statement_timeout 同一套接线方式（refreshView 的参数）。
//
// 为什么必须分族（而不是继续用一个全局值）：
//   · work_mem 的**下界**只能用各族自己的实测来钉：session 族实测峰值
//     ~120MB + 历史溢写 136MB；routing 族实测峰值 ~13MB、8MB 下 temp 恒 0。
//     共用一个值时，下界只能取全族最大值 ⇒ 小族的下界永远没被钉住。
//   · jit 的**结论**不能跨族照抄：routing 族计划代价 193,601（离阈值只 1.93×）
//     ⇒ 关掉是净赚；session 族计划代价高一个量级 ⇒ 必须实测，不能推断。
//
// 判据纪律（本文件必须自查的三点）：
//
//  1. **断言前剥整行注释**。本仓的源码文本断言有过「注释里写了目标字符串
//     照样全绿」的先例——注释里若出现 SET work_mem，未剥离的 Contains
//     恒真。所以下面一律对 blankWholeLineComments 处理过的源码断言。
//  2. **置空而非删行**。本仓共享的 stripFullLineComments 是删行，会让后续
//     报错行号上移（§98 踩过两次）。这里用自己的置空版本。
//  3. **常数必须被真的引用**。本仓有过「指标族零调用死代码」的先例，
//     常量定义得漂亮但没人用，等于没做。

// blankWholeLineComments 把「整行都是注释」的行替换成空行（保留行号），
// 行内注释不动——本文件的断言不依赖行内注释的位置。
func blankWholeLineComments(src string) string {
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "//") {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// mvRefresherSource 读并剥掉整行注释后的源码，供下面所有文本断言使用。
// 刻意不复用 auto_index_refresher_mutex_test.go 里的同名 helper：那一个服务
// 另一个文件、且刻意不剥注释，两个门共用 helper 会互相污染。
func mvRefresherSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("materialized_view_refresher.go")
	if err != nil {
		t.Fatalf("读源文件：%v", err)
	}
	return blankWholeLineComments(string(b))
}

// mvFuncBody 抽取某个方法体（从方法签名到下一个顶格 "}"）。
func mvFuncBody(t *testing.T, src, sig string) string {
	t.Helper()
	at := strings.Index(src, sig)
	if at < 0 {
		t.Fatalf("没找到 %s", sig)
	}
	end := strings.Index(src[at:], "\n}\n")
	if end < 0 {
		t.Fatalf("%s 抽取失败", sig)
	}
	return src[at : at+end]
}

// TestRefreshView_UsesThePassedWorkMemNotAGlobalConstant：
// refreshView 必须用**传入的** workMem 去 SET，且不得写死任何一个族的常量。
//
// ★ 这是最容易出现的「假修复」：加了参数、却仍然写死常量 ⇒ 调用点传什么都没用
//   （与 R75 的 statement_timeout 分族门同款判据）。
func TestRefreshView_UsesThePassedWorkMemNotAGlobalConstant(t *testing.T) {
	body := mvFuncBody(t, mvRefresherSource(t), "func (r *MaterializedViewRefresher) refreshView(")

	if !strings.Contains(body, "SET work_mem = '\"+workMem+\"'") {
		t.Fatal("refreshView 没有用传入的 workMem 去 SET —— 参数是摆设")
	}
	if strings.Contains(body, "mvRefreshWorkMem") {
		t.Fatal("refreshView 仍然写死 routing 族的 work_mem 常量 —— session 族会被共用值处理")
	}
	if strings.Contains(body, "sessionViewsWorkMem") {
		t.Fatal("refreshView 仍然写死 session 族的 work_mem 常量 —— routing 族会被共用值处理")
	}
	if !strings.Contains(body, "RESET work_mem") {
		t.Fatal("refreshView 没有 RESET work_mem —— 归还连接池后会污染后续所有语句")
	}
}

// TestRefreshView_WorkMemResetIsDeferred：RESET 必须挂在 defer 上。
//
// 失败分支（SET 失败、REFRESH 失败、advisory lock 失败）都会走 defer；
// 若 RESET 不在 defer 里，只有成功路径才会复位，错误路径会把抬高后的
// work_mem 留在连接池里，被下一条无关语句继承。
func TestRefreshView_WorkMemResetIsDeferred(t *testing.T) {
	body := mvFuncBody(t, mvRefresherSource(t), "func (r *MaterializedViewRefresher) refreshView(")

	// ⚠ 这条判据被重写过两次，两次都是**假绿**，共同根因是「用字符串位置
	//   推断代码结构」：
	//   第一版：只断言 RESET 出现在某个 defer 之后 —— 把 RESET 改成裸调用
	//     （defer 块之后）照样通过。
	//   第二版：用 LastIndex/Index 推边界 —— closeAt 在整个函数体剩余部分搜
	//     "}()"，后面任意一个 defer 都能满足它；且 body[d:resetAt] 会把 d 处
	//     那个 "defer func() {" 自己包含进来，恒真。
	// ⇒ 第三版不再推断结构，直接**钉精确形态**：deferred 的复位必须原样写成
	//   下面这段，且全函数体只能出现一次 RESET work_mem（多一次=裸调用混入）。
	const wantDeferred = "defer func() {\n\t\t_, _ = conn.Exec(context.WithoutCancel(ctx), \"RESET work_mem\")\n\t}()"
	if !strings.Contains(body, wantDeferred) {
		t.Fatal("refreshView 里没有「defer 包住 RESET work_mem」的形态 —— 错误路径不会复位，" +
			"抬高的 work_mem 会随连接归还而污染后续语句")
	}
	if n := strings.Count(body, "RESET work_mem"); n != 1 {
		t.Fatalf("refreshView 里 RESET work_mem 出现 %d 次，应恰好 1 次 —— 多出来的是裸调用，错误路径不复位", n)
	}
}

// TestRefreshCallSites_PassTheRightFamilyWorkMem：两个族的调用点必须各传各的。
//
// ★ 不要求两个**取值**不同（实测两族都取 256MB，见 refreshView 注释），
//   但要求两个**标识符**不同 —— 否则「分族」是摆设，改一族会连坐另一族。
func TestRefreshCallSites_PassTheRightFamilyWorkMem(t *testing.T) {
	src := mvRefresherSource(t)

	routingBody := mvFuncBody(t, src, "func (r *MaterializedViewRefresher) refreshAll(")
	if !strings.Contains(routingBody, "mvRefreshWorkMem") {
		t.Fatal("refreshAll 没有给 routing 族传 mvRefreshWorkMem")
	}
	if strings.Contains(routingBody, "sessionViewsWorkMem") {
		t.Fatal("refreshAll 传了 session 族的 work_mem —— 两族被绑在一起了")
	}

	sessionBody := mvFuncBody(t, src, "func (r *MaterializedViewRefresher) refreshSessionViews(")
	if !strings.Contains(sessionBody, "sessionViewsWorkMem") {
		t.Fatal("refreshSessionViews 没有给 session 族传 sessionViewsWorkMem")
	}
	if strings.Contains(sessionBody, "mvRefreshWorkMem") {
		t.Fatal("refreshSessionViews 传了 routing 族的 work_mem —— 两族被绑在一起了")
	}
}

// TestSessionViewsWorkMem_CoversItsOwnMeasuredSpill：session 族的下限必须
// 覆盖**它自己**那份实测。
//
// 下界 136MB 来自生产只读实测（session_task_stats 单次 136MB）。
// 这个不等式是**安全方向**的：给小了不会报错，只是仍然落盘——
// 也就是「改完看起来没变」的最隐蔽形态。
func TestSessionViewsWorkMem_CoversItsOwnMeasuredSpill(t *testing.T) {
	const measuredMaxSpillMB = 136 // 252 生产实测，session_task_stats 单次

	gotMB := parseWorkMemMB(t, sessionViewsWorkMem)
	if gotMB < measuredMaxSpillMB {
		t.Fatalf("sessionViewsWorkMem=%s（%dMB）小于实测最大溢写 %dMB —— 仍然落盘，改动等于没做",
			sessionViewsWorkMem, gotMB, measuredMaxSpillMB)
	}
	// 上界：会话级单连接上界，>1GB 时必须先确认 PG 宿主内存余量。
	if gotMB > 1024 {
		t.Fatalf("sessionViewsWorkMem=%dMB 超过 1GB —— 本仓没有 PG 宿主内存读数，需先核内存再定",
			gotMB)
	}
}

// TestMVRefreshWorkMem_NotBelowTheLibraryDefault：routing 族的下界。
//
// ★ 诚实边界：这个不等式是**弱**的。252 实测 routing 族在 8MB / 64MB / 256MB
//   三档下 buffer 足迹逐字相同、temp 恒 0 ⇒ work_mem 对 routing 族**不是杠杆**。
//   ⇒它的取值沿用全局常量（连续性），下界只能钉「不低于库级默认」——
// **不能**写成「必须 ≥ 某次实测溢写」，因为 routing 族压根没有溢写。
//   ⇒ 门里写明这一点，免得后人把「routing 需要大 work_mem」当既成事实。
func TestMVRefreshWorkMem_NotBelowTheLibraryDefault(t *testing.T) {
	const libraryDefaultMB = 8 // 252 PG 配置文件 work_mem = 8MB

	gotMB := parseWorkMemMB(t, mvRefreshWorkMem)
	if gotMB < libraryDefaultMB {
		t.Fatalf("mvRefreshWorkMem=%s（%dMB）低于库级默认 %dMB —— routing 族在刷新时比不开刷新还差",
			mvRefreshWorkMem, gotMB, libraryDefaultMB)
	}
	if gotMB > 1024 {
		t.Fatalf("mvRefreshWorkMem=%dMB 超过 1GB —— 本仓没有 PG 宿主内存读数，需先核内存再定",
			gotMB)
	}
}

// TestRefreshView_UsesThePassedJITModeNotAGlobalConstant：refreshView 必须用
// 传入的 jitMode 去 SET。
func TestRefreshView_UsesThePassedJITModeNotAGlobalConstant(t *testing.T) {
	body := mvFuncBody(t, mvRefresherSource(t), "func (r *MaterializedViewRefresher) refreshView(")

	if !strings.Contains(body, "SET jit = '\"+jitMode+\"'") {
		t.Fatal("refreshView 没有用传入的 jitMode 去 SET —— 参数是摆设")
	}
	if strings.Contains(body, "mvRefreshJIT") {
		t.Fatal("refreshView 仍然写死 routing 族的 JIT 常量 —— session 族被共用值绑走")
	}
	if strings.Contains(body, "sessionViewsJIT") {
		t.Fatal("refreshView 仍然写死 session 族的 JIT 常量 —— routing 族被共用值绑走")
	}
}

// TestRefreshView_JITResetIsDeferred：JIT 的 RESET 必须挂在 defer 上。
//
// 与 work_mem 同理：只有成功路径复位的话，错误路径会把 jit=off 留在
// 连接池里，被归还后的下一条无关语句继承。
func TestRefreshView_JITResetIsDeferred(t *testing.T) {
	body := mvFuncBody(t, mvRefresherSource(t), "func (r *MaterializedViewRefresher) refreshView(")

	const wantDeferred = "defer func() {\n\t\t_, _ = conn.Exec(context.WithoutCancel(ctx), \"RESET jit\")\n\t}()"
	if !strings.Contains(body, wantDeferred) {
		t.Fatal("refreshView 里没有「defer 包住 RESET jit」的形态 —— 错误路径不会复位")
	}
	if n := strings.Count(body, "RESET jit"); n != 1 {
		t.Fatalf("refreshView 里 RESET jit 出现 %d 次，应恰好 1 次 —— 多出来的是裸调用，错误路径不复位", n)
	}
}

// TestRefreshCallSites_PassTheRightFamilyJIT：两个族的调用点必须各传各的。
func TestRefreshCallSites_PassTheRightFamilyJIT(t *testing.T) {
	src := mvRefresherSource(t)

	routingBody := mvFuncBody(t, src, "func (r *MaterializedViewRefresher) refreshAll(")
	if !strings.Contains(routingBody, "mvRefreshJIT") {
		t.Fatal("refreshAll 没有给 routing 族传 mvRefreshJIT")
	}
	if strings.Contains(routingBody, "sessionViewsJIT") {
		t.Fatal("refreshAll 传了 session 族的 JIT 常量 —— 两族被绑在一起了")
	}

	sessionBody := mvFuncBody(t, src, "func (r *MaterializedViewRefresher) refreshSessionViews(")
	if !strings.Contains(sessionBody, "sessionViewsJIT") {
		t.Fatal("refreshSessionViews 没有给 session 族传 sessionViewsJIT")
	}
	if strings.Contains(sessionBody, "mvRefreshJIT") {
		t.Fatal("refreshSessionViews 传了 routing 族的 JIT 常量 —— 两族被绑在一起了")
	}
}

// TestRoutingFamilyJIT_IsOff：routing 族必须关掉 JIT。
//
// 依据（252 生产只读实测，同 session 内交错 8 轮，两次批次合并）：
//
//	jit=on  : 11,338 / 2,390 / 2,836 / 2,718 / 2,598 / 2,376 / 2,275 / 1,927 ms
//	jit=off :  1,015 /   812 / 1,302 / 1,298 / 1,216 / 1,967 / 2,183 /   723 ms
//
//	⇒ 8/8 轮更快，中位 2,494 → 1,257 ms（1.98×），最坏一轮 5.2×。
//	⇒ 且两臂的 root buffer 足迹逐项相等（47,301/47,302）⇒ 这是纯 CPU 轴，
//	  不动「成本问 shared_blks_read」口径。
func TestRoutingFamilyJIT_IsOff(t *testing.T) {
	if mvRefreshJIT != "off" {
		t.Fatalf("mvRefreshJIT=%q —— routing 族的定义查询计划代价 193,601 只比库级 "+
			"jit_above_cost(100000) 高 1.93×，实测开着 JIT 时 8/8 轮都更慢（中位 1.98×）",
			mvRefreshJIT)
	}
}

// TestSessionFamilyJIT_IsOff：session 族必须关掉 JIT。
//
// 依据（252 生产只读实测，同 session 内交错 5 轮，两次批次合并）：
//
//	session_client_task_matrix  jit=on  75,471 / 35,158 / 7,185 / 6,330 / 5,324 ms
//	                          jit=off   6,401 /  4,355 / 5,791 / 5,694 / 5,797 ms
//
//	★诚实边界：同一臂两轮相差 14×（75,471 vs 5,324）⇒ 宿主负载抖动大于臂间差异 ⇒
//	  **不能**承诺倍率。能承诺的只有两件事：
//	  ① buffer 足迹不变（两臂 root buffers 逐项相等 ⇒ 不是 I/O 差异）；
//	  ② off 的**最坏轮**（6,401ms）显著低于 on 的最坏轮（75,471ms），
//	     且 off 的中位（5,791）低于 on 的中位（7,185）⇒ 关掉削的是**长尾**。
//	⇒ 取「关掉」是削尾决策，不是提速承诺（与 routing 族的 8/8 轮证据不同级）。
func TestSessionFamilyJIT_IsOff(t *testing.T) {
	if sessionViewsJIT != "off" {
		t.Fatalf("sessionViewsJIT=%q —— 实测开着 JIT 的最坏轮 75,471ms 对应关掉的 6,401ms，"+
			"且 buffer 足迹逐项相等（纯 CPU 轴）⇒ 关掉削长尾", sessionViewsJIT)
	}
}

// parseWorkMemMB 把 '256MB' / '64MB' / '512kB' / '1GB' 解析成 MB。
func parseWorkMemMB(t *testing.T, s string) int {
	t.Helper()
	v := strings.TrimSpace(s)
	var mult float64
	switch {
	case strings.HasSuffix(v, "GB"):
		mult, v = 1024, strings.TrimSuffix(v, "GB")
	case strings.HasSuffix(v, "MB"):
		mult, v = 1, strings.TrimSuffix(v, "MB")
	case strings.HasSuffix(v, "kB"):
		mult, v = 1.0/1024, strings.TrimSuffix(v, "kB")
	default:
		t.Fatalf("无法解析 work_mem 值 %q（需要 GB/MB/kB 后缀）", s)
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		t.Fatalf("解析 work_mem 数值 %q：%v", s, err)
	}
	return int(n * mult)
}
