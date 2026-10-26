package testdb_test

// R99 轮 · 段十二（§19.20.68）新增：**信号路径的端到端真机验证**。
//
// ── 这道门补的是哪个缺口 ───────────────────────────────────────────────────
//
// 段九（§19.20.65）给 `internal/testdb` 加了「进程收到 SIGTERM ⇒ 把自建的一次性库
// 全部 DROP 掉、然后重发信号让进程仍死于信号」的处理。但那轮**只验了「注册调用存在」**
// ⇒ ⇒ 语义是靠**读代码**保证的 ⇒ ⇒ **端到端没跑过** ⇒ 那轮自己把这条写进了诚实边界。
//
// ★ 本门把它变成**可复现的读数**：父进程 spawn 一个子进程（子进程真的建库、真的阻塞），
// 父进程给子进程发信号，然后查 `pg_database` 看那个库还在不在。
//
// ── 为什么必须带【对照轮】────────────────────────────────────────────────────
//
// 只测「SIGTERM 之后库没了」是**不充分**的：库没销也可能是「根本没建起来」。
// ⇒ ★ 所以本门跑**两轮**，用**同一段代码**、只换信号：
//
//	① SIGTERM 轮（被测路径）⇒ 库**必须消失**
//	② SIGKILL 轮（对照）  ⇒ 库**必须还在** ← SIGKILL 不可捕获，Go 运行时不会跑 defer
//	                        ⇒ 这正是「信号处理路径存在」的证据
//	                        ⇒ 且这轮结束后由父进程手动清掉，不留残留
//
// ⇒ ⇒ ★ 两轮合起来 ⇒「库是被信号路径清掉的」这件事**可判定**；只跑 ① 则不可判定。
//
// ── 跑法 ───────────────────────────────────────────────────────────────────
//
//	TEST_DATABASE_URL=postgres://user:pass@127.0.0.1:5432/postgres?sslmode=disable \
//	  go test ./internal/testdb/ -run 'TestSignalCleanup' -v
//
// ★ 没有 TEST_DATABASE_URL / TEST_DB_URL ⇒ **跳过**（不是红）：跑不了就是没跑，
//   不把「环境不具备」变成假红（沿用 `admin/scratch_db_test.go` 的同一口径）。
// ★ 角色需要 CREATEDB（testdb.Create 自己建的库）⇒ 没有则跳过并说明。

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/testdb"
)

const (
	// scEnvChild 置 1 ⇒ 本进程是「子进程」，跑建库 + 阻塞，等父进程发信号。
	scEnvChild = "LLM_GATEWAY_TESTDB_SIGNAL_CHILD"
	// scChildTimeout 给子进程自己的测试超时：父进程若没发到信号，子进程会在
	// 2 分钟后自己超时退出 ⇒ 父进程据此判「信号没送到」而不是无限等。
	scChildTimeout = "2m"
	// scDropWait 是父进程等待「库消失」的上限（DROP 在子进程里同步完成，
	// 通常 < 1s；给 30s 是为了容忍被踢后端的收尾延迟）。
	scDropWait = 30 * time.Second
)

// ── 子进程 ──────────────────────────────────────────────────────────────────

// TestSignalCleanupChildProcess 是**子进程专用**的测试：真建一个库，然后阻塞到被信号打死。
//
// ★ 它不是「给人看」的测试 ⇒ 直接跑会被 skip。
func TestSignalCleanupChildProcess(t *testing.T) {
	if os.Getenv(scEnvChild) != "1" {
		t.Skip("子进程专用 —— 只由父进程带 " + scEnvChild + "=1 启动")
	}
	baseDSN := scBaseDSN(t)
	dsn := testdb.Create(t, baseDSN)
	name := scDBName(t, dsn)
	// ★ 用一个可被父进程 grep 的标记把库名交出去。
	fmt.Printf("CHILD_DB=%s\n", name)
	_ = os.Stdout.Sync()
	// ★ 阻塞到父进程发信号。
	// ★★ 注意：**不能用 `select {}`** —— Go 的死锁检测器会在所有 goroutine 都阻塞时
	//   直接 panic（信号处理 goroutine 也阻塞在 channel 上 ⇒ 它也算「睡着」）
	//   ⇒ 那会让子进程在收到信号**之前**就自己炸掉 ⇒ 读数变成「子进程异常退出」，
	//   而不是一个干净的「被信号杀死」。
	//   ⇒ 用定时器睡眠：sleeping 的 goroutine **不计入**死锁判定。
	for {
		time.Sleep(20 * time.Second)
	}
}

// ── 父进程 ──────────────────────────────────────────────────────────────────

func TestSignalCleanupDropsDatabaseOnSIGTERM(t *testing.T) {
	baseDSN := scBaseDSN(t)
	if !scCanCreateDB(t, baseDSN) {
		t.Skip("当前角色没有 CREATEDB ⇒ 本次**没有**跑信号路径的真机验证（跑不了就是没跑，不做假红）")
		return
	}

	t.Run("SIGTERM_轮_被测路径", func(t *testing.T) {
		name, exitDesc, out := scRunChild(t, baseDSN, syscall.SIGTERM)
		if name == "" {
			t.Skipf("子进程没能建库（退出：%s）⇒ 本次没跑信号路径的真机验证\n子进程输出尾部：\n%s",
				exitDesc, scTail(out))
			return
		}
		// ★ 子进程是**先 DROP 再重发信号** ⇒ 它死 ⇒ DROP 已完成。
		//   但 DROP 要等被踢后端收尾（实测可达数十秒）⇒ 轮询而不是查一次。
		gone, waited := scWaitGone(baseDSN, name, scDropWait)
		if !gone {
			t.Errorf("★ 信号路径没起作用：库 %s 在子进程被 SIGTERM 杀死后 %s 仍然存在\n"+
				"  ⇒ 子进程退出：%s\n"+
				"  ⇒ ⇒ 「信号 ⇒ 清理一次性库」这条路径**没成立** ⇒ 段九那轮的语义保证落空",
				name, waited, exitDesc)
			scForceDrop(t, baseDSN, name)
			return
		}
		t.Logf("✔ SIGTERM：库 %s 在 %s 内消失（子进程退出形态：%s）", name, waited.Round(time.Millisecond), exitDesc)
		// ★★ 退出形态的契约（★ 这条是本门对「重发信号」那一段的牙）：
		//   信号路径的语义是「重发信号、让进程仍**死于信号**」。
		//   ★ 实测（2026-10-21）：`Process.Signal` 异步投递 ⇒ 若不等待，
		//     兜底 os.Exit(143) 总是赢得竞争 ⇒ 实际形态是「退出码 143」⇒ 失实。
		//   ⇒ 现在重发之后有等待窗口 ⇒ 形态必须是 died-by-signal。
		if !strings.HasPrefix(exitDesc, "died-by-signal") {
			t.Errorf("★ 子进程不是**死于信号**，而是：%s\n"+
				"  ⇒ ⇒ 「重发信号让进程仍死于信号」这条语义**没兑现**\n"+
				"  ⇒ ⇒ 典型成因：`Process.Signal` 是异步投递，紧跟着的 os.Exit(143)\n"+
				"  ⇒ ⇒   总是赢得竞争 ⇒ 重发的信号从未被处理\n"+
				"  ⇒ ⇒ 修法：重发之后加一个有界的等待窗口（signalReraiseWindow）",
				exitDesc)
		}
		if exitDesc == "exit=0" {
			t.Errorf("★ 子进程退出码是 0（%s）⇒ 被杀的 run 看起来像跑成功 ⇒ 绝不能这样", exitDesc)
		}
	})

	t.Run("SIGKILL_轮_对照", func(t *testing.T) {
		name, exitDesc, out := scRunChild(t, baseDSN, syscall.SIGKILL)
		if name == "" {
			t.Skipf("子进程没能建库（退出：%s）⇒ 对照轮没跑\n子进程输出尾部：\n%s",
				exitDesc, scTail(out))
			return
		}
		// ★★ 对照的**关键断言**：SIGKILL 不可捕获 ⇒ Go 运行时不会跑 defer
		//   ⇒ 库**必须还在** ⇒ 这才证明上一轮「库没了」是信号路径干的，不是别的巧合。
		present := scDBExists(baseDSN, name)
		if !present {
			t.Errorf("★ 对照轮前提被打破：SIGKILL 之后库 %s **消失了**\n"+
				"  ⇒ ⇒ 说明「库没了」这件事与信号路径无关 ⇒ 上一轮的断言不成立 ⇒ 请核环境\n"+
				"  ⇒ 子进程退出：%s", name, exitDesc)
		} else {
			t.Logf("✔ 对照成立：SIGKILL 之后库 %s 仍在 ⇒ 「SIGTERM 让它消失」确实是信号路径的功劳", name)
		}
		// ★ 对照轮结束后由父进程清掉 ⇒ 不留残留。
		scForceDrop(t, baseDSN, name)
	})
}

// ── 辅助 ────────────────────────────────────────────────────────────────────

// scBaseDSN 取 base DSN；没有就跳过。
func scBaseDSN(t *testing.T) string {
	t.Helper()
	for _, key := range []string{"TEST_DATABASE_URL", "TEST_DB_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— 本次**没有**跑真库行为验证")
	return ""
}

// scCanCreateDB 预检当前角色能不能建库（testdb.Create 建库失败会 t.Fatalf）。
func scCanCreateDB(t *testing.T, baseDSN string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, baseDSN)
	if err != nil {
		t.Logf("连不上 base DSN：%v", err)
		return false
	}
	defer pool.Close()
	var can bool
	if err := pool.QueryRow(ctx,
		"select rolcreatedb or rolsuper from pg_roles where rolname = current_user").
		Scan(&can); err != nil {
		t.Logf("查角色权限失败：%v", err)
		return false
	}
	return can
}

// scDBName 从 Create 返回的 DSN 里取库名。
func scDBName(t *testing.T, dsn string) string {
	t.Helper()
	// Create 造出来的 DSN 是标准 postgres URL：scheme 为 postgres、userinfo 段带凭据、
	// **路径段是库名**、query 段带 sslmode=disable。
	// ★ 这里【刻意不写出 URL 字面量】—— 旧注释里内嵌了一份带口令的示例 DSN，
	//   secret 扫描器因此按 DB_CONNSTRING 判 BLOCK，而该口令又【不在】
	//   scripts/scan-secrets.replacements 与 scan-secrets.private.replacements 里
	//   ⇒ ⇒ 公开镜像的 filter-repo 【不会】改写它 ⇒ ⇒ 放行等于把凭据连同历史
	//   一起发到 GitHub 公开仓 ⇒ ⇒ 而 scan-secrets.baseline 的规矩正是
	//   「BLOCK 就修文件，不要加 baseline」⇒ ⇒ 改描述、不降级判据。
	// ⇒ 取最后一个 '/' 与 '?' 之间。
	i := strings.LastIndex(dsn, "/")
	if i < 0 {
		t.Fatalf("从 DSN 里取库名失败：%q", dsn)
	}
	name := dsn[i+1:]
	if j := strings.IndexAny(name, "?"); j >= 0 {
		name = name[:j]
	}
	return name
}

// scRunChild 启动子进程、等到它报出库名、给它发信号、等它退出。
// 返回（库名, 退出描述, 子进程输出尾部）。
func scRunChild(t *testing.T, baseDSN string, sig syscall.Signal) (string, string, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSignalCleanupChildProcess$", "-test.timeout="+scChildTimeout)
	cmd.Env = append(os.Environ(), scEnvChild+"=1", "TEST_DATABASE_URL="+baseDSN, "TEST_DB_URL="+baseDSN)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("取子进程 stdout 失败：%v", err)
	}
	var sb strings.Builder
	cmd.Stderr = &sb
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动子进程失败：%v", err)
	}
	// ★ 无论怎么退出，都要把子进程收干净 ⇒ 绝不留一个孤儿在跑。
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	// 读库名（子进程建库成功才会打印）。
	nameCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			sb.WriteString(line + "\n")
			if v, ok := strings.CutPrefix(line, "CHILD_DB="); ok {
				select {
				case nameCh <- v:
				default:
				}
			}
		}
	}()

	var name string
	select {
	case name = <-nameCh:
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Signal(sig)
		_ = cmd.Wait()
		return "", "子进程 90s 内没报出库名", sb.String()
	}

	_ = cmd.Process.Signal(sig)
	waitErr := cmd.Wait()
	return name, scExitForm(waitErr), sb.String()
}

// scExitForm 把子进程的退出结果归成**三档可判定形态**。
// ★★ 为什么不能只看退出码：bash 的 wait 对「exit 143」与「被 SIGTERM 杀掉」
//
//	**报同一个数**（143 = 128+15）⇒ 分不出「退出码 143」与「signalled death」；
//	而 Go 的 exec 能分：exit status 143 ⇒ ExitCode()==143；
//	signalled death ⇒ ExitCode()==-1 且 WaitStatus.Signaled()==true。
//
// ⇒ ★ 这道门就是靠这个差别，把「重发信号到底生不生效」变成**可判定**的。
func scExitForm(err error) string {
	if err == nil {
		return "exit=0"
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		return "other: " + err.Error()
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return fmt.Sprintf("died-by-signal(%v)", ws.Signal())
	}
	return fmt.Sprintf("exited-with-code(%d)", ee.ExitCode())
}

// scDBExists 查库还在不在。
func scDBExists(baseDSN, name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, baseDSN)
	if err != nil {
		return false
	}
	defer pool.Close()
	var n int
	if err := pool.QueryRow(ctx,
		"select count(*) from pg_database where datname = $1", name).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// scWaitGone 等库消失，返回（是否消失, 等了多久）。
func scWaitGone(baseDSN, name string, max time.Duration) (bool, time.Duration) {
	start := time.Now()
	for time.Since(start) < max {
		if !scDBExists(baseDSN, name) {
			return true, time.Since(start)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false, time.Since(start)
}

// scForceDrop 清掉残留（对照组专用；被测路径不该走到这里）。
func scForceDrop(t *testing.T, baseDSN, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, baseDSN)
	if err != nil {
		return
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
		t.Logf("清理残留库 %s 失败：%v", name, err)
	}
}

// scTail 取输出的尾部若干行（用于失败时给线索）。
func scTail(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return strings.Join(lines, "\n")
}

// ── 静态判据 + 门自检（覆盖【另一条 Go 腿】与【删对照 / 删断言】的逃逸口） ────

// scReReraiseThenExit 抓「重发信号之后紧跟着 os.Exit(...)」这个**失实形态**。
//
// ★ 为什么要有静态判据：上面的行为门只跑 `internal/testdb` 这一条腿；
//
//	`scripts/audit/fresh_schema_integration_test.go` 那条腿是**同一个机制**，
//	但它跑的是集成门禁（要真环境）⇒ ⇒ 那条腿只能靠**静态形态**守住。
//
// ★★ 必须先剥注释：这段形态在两处文件的**修复说明里被引用**
//
//	（「紧跟着的 os.Exit(143) 总是赢得竞争」）⇒ 不剥就会匹配到注释 ⇒ **假阳性**。
//
// ★ 2026-10-22 12h 审计轮：判据从「字面量 `os.Exit(143)`」放宽为
//
//	  「任何 `os.Exit(...)`」。
//
//		原因：原判据把**写法**当成了**语义** —— 退出码按实际信号取值后
//		（SIGINT⇒130 / SIGTERM⇒143，2026-10-22 审计修正 P3），
//		`os.Exit(143)` 这个字面量消失了 ⇒ 门报「找不到形态」⇒
//		**门把一次正确的改动判成回归**。
//		⇒ 判据要守的是「重发信号与退出之间**必须有投递窗口**」这件事，
//		  而不是「退出码必须写成 143」。
//
// ★ 仍然能抓住原缺陷：去掉 time.Sleep 后，`Signal(s) … os.Exit(` 依然命中
//
//	⇒ 中间没有 time.Sleep ⇒ 报错（下方那条断言）。
var scReReraiseThenExit = regexp.MustCompile(`(?s)Signal\(s\).{0,900}?os\.Exit\(`)

// scStripGoComments 剥掉整行都是注释的行（Go 的 `//` 前缀）。
//
// ★ 与仓内主门 `scqStripComments` 同一套做法（只清空「整行都是注释」的行、保留行数），
//
//	但**不能跨包 import**（它在 `scripts/ursmcheck` 的 `ursm_test` 包里）⇒ 这里本地写一份。
func scStripGoComments(src string) string {
	out := make([]string, 0, 64)
	for _, ln := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "//") {
			out = append(out, "")
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

// scSignalLegs 是「信号处理」这条机制涉及的**两条 Go 腿**。
// ★ 路径是**相对包目录**的（go test 的工作目录 = 包目录）：
//
//	同目录的腿 = "testdb.go"；跨目录的腿 = "../../scripts/audit/…"（仓库根起算两层）。
var scSignalLegs = []string{
	"testdb.go",
	"../../scripts/audit/fresh_schema_integration_test.go",
}

// TestSignalReraiseHasDeliveryWindow 静态守「重发之后必须有投递窗口」。
func TestSignalReraiseHasDeliveryWindow(t *testing.T) {
	for _, rel := range scSignalLegs {
		data, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", rel, err)
		}
		src := scStripGoComments(string(data))
		ms := scReReraiseThenExit.FindAllString(src, -1)
		if len(ms) == 0 {
			t.Errorf("★ %s 里找不到「重发信号 … os.Exit(...)」的形态\n"+
				"  ⇒ ⇒ 是不是这条腿的信号处理被改掉了？⇒ 请核", rel)
			continue
		}
		for _, m := range ms {
			if strings.Contains(m, "time.Sleep(") {
				continue
			}
			t.Errorf("★ %s 里「重发信号」与 os.Exit(...) 之间**没有投递窗口**\n"+
				"  ⇒ ⇒ `Process.Signal` 是异步投递 ⇒ 紧跟着的 os.Exit(143) 总是赢得竞争\n"+
				"  ⇒ ⇒ 重发的信号从未生效 ⇒ 进程是「退出码 143」而不是「死于信号」\n"+
				"  ⇒ ⇒ 修法：重发之后加一个有界的 `time.Sleep(...)`（signalReraiseWindow）\n"+
				"  ⇒ 命中片段：%s",
				rel, scCompact(m))
		}
	}
}

// TestSignalCleanupGateHasTeeth 是**门自己的自检**：证明这道门不是恒绿门。
//
// ★ 覆盖两类逃逸口：
//
//	① 把对照轮（SIGKILL）删掉 ⇒ 门会变弱但仍绿 ⇒ 必须能被自检抓到；
//	② 把「必须死于信号」这条断言删掉 ⇒ 同样 ⇒ 必须能被自检抓到。
//
// ★★★★ 【本自检的第一版是【恒绿门】，被变异 M3/M4 当场抓出来】——
//
//	第一版直接 `strings.Contains(整份源码, "syscall.SIGKILL")`，
//	而**那个字面量就写在自检自己的断言里** ⇒ ⇒ **删掉对照轮也照样命中** ⇒ ⇒ 恒绿。
//
// ⇒ ★★ 修法：**只搜【自检函数声明之前】的区段**（scSelfCheckRegion）
// ⇒ ⇒ 自检自身写的字面量落在被排除的区段里 ⇒ ⇒ 「删掉要守的东西」才会真的被抓到。
// ⇒ ⇒ ★ 通用式：**凡是「在自己的源码里 grep 某个 token」来自检的门，
// 必须先排除自检自身的区段** ⇒ 否则它天生恒绿。
// ⇒ ⇒ ★★ **第二版补的一条**：排除区段还不够 ⇒ **标记还必须是【唯一】的**
// ⇒ ⇒ （"syscall.SIGKILL" 在 defer 的清理里也出现、"died-by-signal" 在
// ⇒ ⇒   scExitForm 的格式化串里也出现 ⇒ ⇒ 用它们当标记 ⇒ ⇒ 删掉要守的东西也照样命中）
// ⇒ ⇒ ⇒ **通用式：自检的标记 = 「只在该处出现」的结构性片段**，不是任意 token。
func TestSignalCleanupGateHasTeeth(t *testing.T) {
	self, err := os.ReadFile("signal_cleanup_realdb_test.go")
	if err != nil {
		t.Fatalf("读门自己的源码失败: %v", err)
	}
	src := scStripGoComments(string(self))
	region := scSelfCheckRegion(src)
	if region == src {
		t.Errorf("★ 门自检失败：找不到自检函数自身的标记 ⇒ 搜索区段退化成全文\n" +
			"  ⇒ ⇒ 那样自检会因为【它自己写的字面量】而恒绿 ⇒ 门就没牙了")
		return
	}
	// ★★★ 下面三个标记必须是【唯一】的 —— ★ 这是本自检第二版才补上的：
	//   第一版用 "syscall.SIGKILL" / "died-by-signal" 这类【token】做标记
	//   ⇒ ★ 但它们在文件**别处也出现**（前者还出现在 defer 的清理里、
	//   后者还出现在 scExitForm 的格式化串里）⇒ ⇒ 删掉要守的东西也照样命中
	//   ⇒ ⇒ 变异 M3/M4 抓不到 ⇒ ⇒ **标记必须是【只在该处出现】的结构性片段**。
	// ① 对照轮必须在（标记 = 对照轮的子测试名，全文唯一）
	if !strings.Contains(region, `"SIGKILL_轮_对照"`) {
		t.Errorf("★ 门自检失败：对照轮的子测试没了 ⇒ 「库消失是信号路径的功劳」就不可判定了")
	}
	// ② 退出形态断言必须在（标记 = 断言表达式本身，全文唯一）
	if !strings.Contains(region, `strings.HasPrefix(exitDesc, "died-by-signal")`) {
		t.Errorf("★ 门自检失败：「必须死于信号」这条断言没了 ⇒ 「重发信号生效没有」就没人守了")
	}
	// ③ 静态判据必须在（标记 = 判据的**调用点**，不是它的定义）
	if !strings.Contains(region, "scReReraiseThenExit.FindAllString") {
		t.Errorf("★ 门自检失败：静态判据的调用没了 ⇒ 另一条 Go 腿（集成门禁那条）就没人守了")
	}
}

// scSelfCheckRegion 返回「自检函数声明之前」的那段源码。
//
// ★ 为什么要排除自检自身：自检要守的 token（SIGKILL / died-by-signal …）
//
//	**就写在自检自己的断言文本里** ⇒ ⇒ 搜全文 ⇒ **恒绿** ⇒ ⇒ 搜前缀才有效。
func scSelfCheckRegion(src string) string {
	const marker = "func TestSignalCleanupGateHasTeeth"
	if i := strings.Index(src, marker); i >= 0 {
		return src[:i]
	}
	return src
}

// scCompact 把片段压成一行，便于报错展示。
func scCompact(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
