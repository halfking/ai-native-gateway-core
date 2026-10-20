// data_lifecycle_cron_probe_test.go — 2026-10-21（§19.20.24）：
// hot cron 的**行为层**门 —— 把「能力探针 / panic 兜底 / 无 DB 句柄」三条
// 此前**没有任何行为覆盖**的路径钉住。
//
// ★ 为什么需要这一层（缺陷本体）：
// ① 探针契约此前只有一道**静态**门（data_lifecycle_v1_retirement_test.go 的
// TestHotCron_HasPromoteFunctionCapabilityProbe）⇒ 它读的是**源码文本**
// （strings.Contains("promote function absent")）⇒ ★★ 把那条 INFO 改成
// ERROR 它**照样绿** ⇒ 它守的是「源码里有这几个字」，不是「运行时是 INFO」。
// 而这条不变量恰是 890 退役轮的核心（噪声不得冒充故障）。
// ② panic 兜底此前只有一条「靠 nil 解引用 panic 出来」的用例 ⇒ 它测到的只是
// 「panic 被 recover 兜住了」，而且每次跑都往证据通道里塞一条假 ERROR。
// ③ 无 DB 句柄（构造出错）此前表现为 nil 解引用 panic ⇒ 只留一条不带位置的
// ERROR，定位不到根因。
//
// ★ 为什么现在能测了：HotCronScheduler 新增窄接口字段 db（hotCronDB），
// 生产上由 NewHotCronScheduler 从 h.db 接线，单测可注入假实现 ⇒
// 这条路径**第一次**可以被行为驱动地覆盖。
package admin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// syncLogBuffer 是并发安全的日志缓冲。
//
// 为什么必须并发安全：hot cron 的真实执行路径是 maybeRun → **detached
// goroutine** → runOnce ⇒ 日志是在**另一个 goroutine** 里写的 ⇒ 若测试用
// bytes.Buffer 直接读，会与写入方数据竞争（-race 下直接报）。
type syncLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureHotCronLogs 接管 slog 默认 logger，把日志收进并发安全的缓冲。
//
// 用 TextHandler：级别字段是 "level=ERROR" ⇒ 断言级别时用字面量即可。
func captureHotCronLogs(t *testing.T) *syncLogBuffer {
	t.Helper()
	buf := &syncLogBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// hotCronWaitFor 轮询直到 cond 为真或超时。
//
// 用在 detached goroutine 路径上：maybeRun 会立刻返回 ⇒ 必须等 runOnce
// 自己跑完（lastRun 被置位）才能读它的日志。
func hotCronWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 %s 超时（5s）", what)
}

// fakeHotCronRow 是 pgx.Row 的最小假实现。
type fakeHotCronRow struct {
	value any
	err   error
}

func (r fakeHotCronRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) == 0 {
		return errors.New("fakeHotCronRow: Scan 没有目标参数")
	}
	switch d := dest[0].(type) {
	case *bool:
		b, ok := r.value.(bool)
		if !ok {
			return fmt.Errorf("fakeHotCronRow: 期望 bool，得到 %T", r.value)
		}
		*d = b
	case *int64:
		i, ok := r.value.(int64)
		if !ok {
			return fmt.Errorf("fakeHotCronRow: 期望 int64，得到 %T", r.value)
		}
		*d = i
	default:
		return fmt.Errorf("fakeHotCronRow: 不支持的目标类型 %T", dest[0])
	}
	return nil
}

// fakeHotCronDB 是 hotCronDB 的假实现：按 SQL 形状区分「能力探针」与
// 「promote 调用」，并记录调用序列供断言。
type fakeHotCronDB struct {
	mu sync.Mutex

	probePresent bool   // 能力探针返回「函数存在」
	probeErr     error  // 能力探针返回的错误
	promoteRows  int64  // promote 调用搬走的行数
	promoteErr   error  // promote 调用的错误
	panicOnProbe bool   // 能力探针处注入 panic
	panicNow     string // panic 的来源标记（用于在堆栈里定位）

	probeCalls   []string // 探针查过的函数名
	promoteCalls []string // promote 调用过的函数名
}

func (f *fakeHotCronDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	f.mu.Lock()
	defer f.mu.Unlock()

	if strings.Contains(sql, "FROM pg_proc") {
		if f.panicOnProbe {
			f.panicNowNow()
		}
		f.probeCalls = append(f.probeCalls, fmt.Sprint(args...))
		return fakeHotCronRow{value: f.probePresent, err: f.probeErr}
	}
	f.promoteCalls = append(f.promoteCalls, sql)
	return fakeHotCronRow{value: f.promoteRows, err: f.promoteErr}
}

// panicNowNow 是**具名**的 panic 注入点。
//
// 为什么要具名：panic 兜底门要断言「日志里带堆栈」⇒ 堆栈里必须有一个
// 稳定的、可断言的标记 ⇒ 这里用一个具名函数，断言就钉在它的名字上
// （而不是钉在行号/文件路径这种会漂的东西上）。
func (f *fakeHotCronDB) panicNowNow() {
	panic("injected hot cron panic (test fixture)")
}

func (f *fakeHotCronDB) snapshot() (probeCalls, promoteCalls []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.probeCalls...), append([]string(nil), f.promoteCalls...)
}

// newHotCronForTest 造一个注入假 DB 的调度器（不走 NewHotCronScheduler，
// 因为后者会从 h.db 接线、而单测没有真连接池）。
func newHotCronForTest(db hotCronDB) *HotCronScheduler {
	return &HotCronScheduler{
		h:   &Handler{hotJobMgr: newHotJobManager()},
		db:  db,
		cfg: HotCronConfig{RunAtHour: 2, RunAtMinute: 0, RetentionHours: 24, BatchSize: 100},
	}
}

// TestHotCron_ProbeAbsentIsInfoSkipNotError 钉住 890 退役轮的核心不变量：
// 「promote 函数不存在」必须是 **INFO 的 SKIP**，不能是 ERROR。
//
// ★ 这道门与既有静态门（TestHotCron_HasPromoteFunctionCapabilityProbe）
// **互补而非重复**：静态门读源码文本（源码里有 "promote function absent"
// 这几个字就绿）⇒ 把那条 INFO 改成 ERROR 它照样绿；本门读**运行时行为**。
func TestHotCron_ProbeAbsentIsInfoSkipNotError(t *testing.T) {
	logs := captureHotCronLogs(t)
	db := &fakeHotCronDB{probePresent: false}
	s := newHotCronForTest(db)

	s.runOnce(context.Background())

	text := logs.String()
	probeCalls, promoteCalls := db.snapshot()

	// ① 每个在册族都被探过一遍（探针不是装饰）。
	if len(probeCalls) != len(hotPromoteTableMap) {
		t.Fatalf("能力探针应覆盖清单里的每个族：want %d 次探针，got %d", len(hotPromoteTableMap), len(probeCalls))
	}
	// ② 函数不存在 ⇒ 记 INFO 的 SKIP。
	if !strings.Contains(text, "promote function absent") {
		t.Fatalf("promote 函数不存在时必须记一条 SKIP（INFO）；日志：\n%s", text)
	}
	// ③ ★ 核心：整轮**一条 ERROR 都没有**（噪声不得冒充故障）。
	if strings.Contains(text, "level=ERROR") {
		t.Fatalf("hot cron 整轮不该出现任何 ERROR（噪声不得冒充故障）；日志：\n%s", text)
	}
	// ④ 函数不存在 ⇒ 绝不真去调 promote。
	if len(promoteCalls) != 0 {
		t.Fatalf("函数不存在时不得调用 promote 函数，却调了 %d 次：%v", len(promoteCalls), promoteCalls)
	}
	// ⑤ 本轮记为成功执行（跳过的族不算失败）。
	stats := s.Stats()
	if stats.LastError != "" {
		t.Fatalf("跳过不等于失败：lastError 应为空，got %q", stats.LastError)
	}
}

// TestHotCron_ProbeErrorIsWarnSkipNotError 钉住探针自身失败时的降级语义：
// 记 **WARN** 并跳过该表，不许升级成 ERROR。
func TestHotCron_ProbeErrorIsWarnSkipNotError(t *testing.T) {
	logs := captureHotCronLogs(t)
	db := &fakeHotCronDB{probeErr: errors.New("probe boom")}
	s := newHotCronForTest(db)

	s.runOnce(context.Background())

	text := logs.String()
	probeCalls, promoteCalls := db.snapshot()

	if !strings.Contains(text, "promote function probe failed") {
		t.Fatalf("探针失败必须记一条 WARN 的跳过；日志：\n%s", text)
	}
	if strings.Contains(text, "level=ERROR") {
		t.Fatalf("探针失败不得升级成 ERROR；日志：\n%s", text)
	}
	if len(probeCalls) != len(hotPromoteTableMap) {
		t.Fatalf("探针失败后应继续探下一张表：want %d 次探针，got %d", len(hotPromoteTableMap), len(probeCalls))
	}
	if len(promoteCalls) != 0 {
		t.Fatalf("探针失败时不得调用 promote 函数，却调了 %d 次：%v", len(promoteCalls), promoteCalls)
	}
}

// TestHotCron_PromoteRunsWhenFunctionPresent 钉住「探针说函数在 ⇒ 真的去
// promote」这条正路（此前零行为覆盖）。
func TestHotCron_PromoteRunsWhenFunctionPresent(t *testing.T) {
	logs := captureHotCronLogs(t)
	db := &fakeHotCronDB{probePresent: true, promoteRows: 0}
	s := newHotCronForTest(db)

	s.runOnce(context.Background())

	text := logs.String()
	probeCalls, promoteCalls := db.snapshot()

	if len(probeCalls) != len(hotPromoteTableMap) {
		t.Fatalf("want %d 次探针，got %d", len(hotPromoteTableMap), len(probeCalls))
	}
	if len(promoteCalls) != len(hotPromoteTableMap) {
		t.Fatalf("函数存在时应逐族调用 promote：want %d 次，got %d", len(hotPromoteTableMap), len(promoteCalls))
	}
	// promote 必须按清单里的函数名调用（不是随便一个名字）。
	for table, fn := range hotPromoteTableMap {
		want := fmt.Sprintf("SELECT %s($1::interval, $2::int)", fn)
		found := false
		for _, got := range promoteCalls {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("族 %q 的 promote 调用形状不对，want %q；实际调用：%v", table, want, promoteCalls)
		}
	}
	if !strings.Contains(text, "hot cron table done") {
		t.Fatalf("promote 成功后应记一条 table done；日志：\n%s", text)
	}
	if strings.Contains(text, "level=ERROR") {
		t.Fatalf("正常路径不该出现 ERROR；日志：\n%s", text)
	}
	// 成功也要写进 job 状态（前端能看到 cron 执行历史）。
	if n := len(s.h.hotJobMgr.listAll()); n != len(hotPromoteTableMap) {
		t.Fatalf("每个族都应记一条 job：want %d，got %d", len(hotPromoteTableMap), n)
	}
}

// TestHotCron_PanicIsContainedAndStackIsLogged 钉住两条：
// ① detached goroutine 里的 panic **不许**掀翻进程（进程级不变式）；
// ② 它必须被记成 ERROR **且带堆栈** ⇒ 线上真 panic 时能定位到出处。
//
// ★ 第 ② 条是本轮产品改动的验收点：此前 recover 只记 panic 值，而 panic 值
// 常常是 "invalid memory address or nil pointer dereference" 这种**不带位置**
// 的文本 ⇒ 定位不到是哪一行炸的。
func TestHotCron_PanicIsContainedAndStackIsLogged(t *testing.T) {
	logs := captureHotCronLogs(t)
	db := &fakeHotCronDB{probePresent: false, panicOnProbe: true}
	s := newHotCronForTest(db)

	// 走真实路径：maybeRun → detached goroutine → runOnce → panic → recover。
	s.maybeRun(context.Background())

	// 等日志落地（detached goroutine 是异步的）。
	hotCronWaitFor(t, "panic 日志", func() bool {
		return strings.Contains(logs.String(), "hot cron panic")
	})

	text := logs.String()
	if !strings.Contains(text, "hot cron panic") {
		t.Fatalf("panic 必须被 recover 兜住并记成 ERROR；日志：\n%s", text)
	}
	// ★ 堆栈必须带：断言注入点出现在栈里（具名标记，不钉行号）。
	if !strings.Contains(text, "panicNowNow") {
		t.Fatalf("panic 日志必须带堆栈（否则线上无法定位）；日志：\n%s", text)
	}
	// 进程没被掀翻 ⇒ 本测试能跑到这里就是证据；同时 running 必须已复位。
	hotCronWaitFor(t, "running 复位", func() bool { return !s.Stats().RunningNow })
}

// TestHotCron_NoDatabaseHandleIsErrorNotPanic 钉住「构造出错」这条路径：
// 没有 DB 句柄时必须记一条**自解释的 ERROR**并跳过，而不是 nil 解引用 panic。
//
// ★ 这道门同时覆盖构造器里的**typed-nil 陷阱**：若 NewHotCronScheduler
// 直接 s.db = h.db（h.db 是 nil 的 *pgxpool.Pool）⇒ s.db 会变成「非 nil 的
// 接口持有 nil 指针」⇒ s.db == nil 为假 ⇒ 守卫失效 ⇒ 变成 nil 解引用 panic
// ⇒ 本门红。
func TestHotCron_NoDatabaseHandleIsErrorNotPanic(t *testing.T) {
	logs := captureHotCronLogs(t)

	// h.db 为 nil（没有连接池）⇒ 构造器必须把 s.db 留成真 nil。
	s := NewHotCronScheduler(&Handler{hotJobMgr: newHotJobManager()}, HotCronConfig{})
	if s.db != nil {
		t.Fatalf("h.db 为 nil 时，s.db 必须是真 nil（不能是持有 nil 指针的接口），got %#v", s.db)
	}

	s.runOnce(context.Background())

	text := logs.String()
	if !strings.Contains(text, "no database handle") {
		t.Fatalf("没有 DB 句柄必须记一条自解释的 ERROR；日志：\n%s", text)
	}
	if !strings.Contains(text, "level=ERROR") {
		t.Fatalf("没有 DB 句柄是**真故障**（不是噪声）⇒ 必须是 ERROR；日志：\n%s", text)
	}
}
