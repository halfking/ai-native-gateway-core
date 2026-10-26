package bg

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// ── REFRESH 的 work_mem 抬升（2026-10-19，252 PG temp-file 审计轮）────────────
//
// 缺陷形态：PG 侧 work_mem=8MB，而 session_task_stats 单次 REFRESH 实测溢写
// 136MB 临时文件。work_mem 8MB ⇒ 必然落盘，是算术后果不是异常。
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

// TestRefreshView_LiftsWorkMemForTheRefreshStatement：
// refreshView 必须在钉住连接上抬 work_mem，并在归还前 RESET。
//
// 这是承重判据：把 SET 删掉、把 RESET 删掉、把 SET 挪到 refreshView 之外，
// 三种改法都要各自变红。
func TestRefreshView_LiftsWorkMemForTheRefreshStatement(t *testing.T) {
	body := mvFuncBody(t, mvRefresherSource(t), "func (r *MaterializedViewRefresher) refreshView(")

	if !strings.Contains(body, "SET work_mem = '\"+mvRefreshWorkMem+\"'") {
		t.Fatal("refreshView 没有抬 work_mem —— 8MB 下 REFRESH 必然溢写临时文件")
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
	// 推断代码结构」：
	//   第一版：只断言 RESET 出现在某个 defer 之后 —— 把 RESET 改成裸调用
	//     （defer 块之后）照样通过。
	//   第二版：用 LastIndex/Index 推边界 —— closeAt 在整个函数体剩余部分搜
	//     "}()"，后面任意一个 defer 都能满足它；且 body[d:resetAt] 会把 d 处
	//     那个 "defer func() {" 自己包含进来，恒真。
	// ⇒ 第三版不再推断结构，直接**钉精确形态**：deferred 的复位必须原样写成
	// 下面这段，且全函数体只能出现一次 RESET work_mem（多一次=裸调用混入）。
	const wantDeferred = "defer func() {\n\t\t_, _ = conn.Exec(context.WithoutCancel(ctx), \"RESET work_mem\")\n\t}()"
	if !strings.Contains(body, wantDeferred) {
		t.Fatal("refreshView 里没有「defer 包住 RESET work_mem」的形态 —— 错误路径不会复位，" +
			"抬高的 work_mem 会随连接归还而污染后续语句")
	}
	if n := strings.Count(body, "RESET work_mem"); n != 1 {
		t.Fatalf("refreshView 里 RESET work_mem 出现 %d 次，应恰好 1 次 —— 多出来的是裸调用，错误路径不复位", n)
	}
}

// TestMVRefreshWorkMem_CoversLargestMeasuredSpill：常数必须覆盖实测里最大的那次溢写。
//
// 下界 136MB 来自生产只读实测（session_task_stats 单次 136MB）。
// 这个不等式是**安全方向**的：给小了不会报错，只是仍然落盘——
// 也就是「改完看起来没变」的最隐蔽形态。
func TestMVRefreshWorkMem_CoversLargestMeasuredSpill(t *testing.T) {
	const measuredMaxSpillMB = 136 // 252 生产实测，session_task_stats 单次

	gotMB := parseWorkMemMB(t, mvRefreshWorkMem)
	if gotMB < measuredMaxSpillMB {
		t.Fatalf("mvRefreshWorkMem=%s（%dMB）小于实测最大溢写 %dMB —— 仍然落盘，改动等于没做",
			mvRefreshWorkMem, gotMB, measuredMaxSpillMB)
	}
	// 上界：会话级单连接上界，>1GB 时必须先确认 PG 宿主内存余量。
	if gotMB > 1024 {
		t.Fatalf("mvRefreshWorkMem=%dMB 超过 1GB —— 本仓没有 PG 宿主内存读数，需先核内存再定",
			gotMB)
	}
}

// TestMVRefreshWorkMem_IsActuallyReferenced：常数必须被 refreshView 真的引用。
//
// 防的是「定义了一个漂亮的常数，但代码里没接」——本仓有零调用死代码的先例。
func TestMVRefreshWorkMem_IsActuallyReferenced(t *testing.T) {
	body := mvFuncBody(t, mvRefresherSource(t), "func (r *MaterializedViewRefresher) refreshView(")
	if !strings.Contains(body, "mvRefreshWorkMem") {
		t.Fatal("refreshView 没有引用 mvRefreshWorkMem —— 常数是死代码")
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
