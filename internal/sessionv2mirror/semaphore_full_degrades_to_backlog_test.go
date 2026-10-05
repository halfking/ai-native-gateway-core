package sessionv2mirror

// semaphore_full_degrades_to_backlog_test.go — 2026-10-05（审计 §9.253）。
//
// # 这道门在守什么
//
// `PersistHook` 的派发是：
//
//	select {
//	case shadowWriteSema <- struct{}{}:
//	    go func(){ …; run() }()
//	default:
//	    metrics.Global().RecordShadowWriteFailure("session_v2")
//	    if !EnqueueMirrorFailure(entry, sessionID, "semaphore_full") {
//	        appendBacklog(BacklogItem{…})
//	    }
//	}
//
// `default` 分支的契约是：**8 个槽全满时，这次请求不能凭空消失**。
// 先落耐久 outbox（migration 712）给后台 reaper；落不进去时退到
// 进程内 backlog（cap 有界、重启即丢，但**至少在进程内还查得到**）。
//
// # 为什么它此前无人看守（§9.253.1 的调查结果）
//
// - `silent_drop_paths_test.go` 的 `TestSilentMirrorDropPathsAreAllDocumented`
//   扫的是 **纯丢弃 return**（分支体里只有 `return`）。`default` 分支**不是**
//   纯丢弃（它记指标 + 登记），所以不进那张清单。
// - `outbox_replay_test.go` 的 `TestEnqueueMirrorFailure_NilPoolReturnsFalse`
//   只验 `EnqueueMirrorFailure(nil pool) == false` 这个**函数**，不验
//   「信号量满时真的会走到 appendBacklog」。
//
// ⇒ **把 `appendBacklog(...)` 整段删掉，仓库里没有任何一个测试会红**：
// 槽满时的请求会既不写 turn、也不进 outbox、也不在 backlog 里 ——
// 与 §9.249.2 生产上那 1 行「既没成功也没报错」完全同形，而且这次
// **连 `V2 shadow write failed` 日志都不会有**（那条日志在 `runShadowWrite` 里，
// `default` 分支根本进不去）。
//
// # 用例互为对照
//
//   - 用例 A（阳性）：8 槽填满 ⇒ writer **零调用**、backlog **恰好 1**。
//   - 用例 B（★ 反向对照）：8 槽排空 ⇒ writer 被调用、backlog **保持 0**。
//     只测 A 的门会放过「appendBacklog 变成无条件、正常路径也往 backlog 里塞」
//     这类改动。

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// semaCountingWriter 记录 Write 被调用的次数，并给每次调用发一个信号，
// 以便异步派发时可以确定性地等待。
type semaCountingWriter struct {
	mu    sync.Mutex
	calls int
	ch    chan struct{}
}

func (w *semaCountingWriter) Write(_ context.Context, _ *v2.ProcessedRequest) error {
	w.mu.Lock()
	w.calls++
	w.mu.Unlock()
	select {
	case w.ch <- struct{}{}:
	default:
	}
	return nil
}

func (w *semaCountingWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

// fillShadowWriteSema 把 8 个槽全部占满，让派发必然走 default 分支。
func fillShadowWriteSema() {
	for i := 0; i < cap(shadowWriteSema); i++ {
		shadowWriteSema <- struct{}{}
	}
}

func drainShadowWriteSema() {
	for {
		select {
		case <-shadowWriteSema:
		default:
			return
		}
	}
}

func TestPersistHookSemaphoreFullDegradesToBacklog(t *testing.T) {
	// --- 保存并还原所有包级 seam -------------------------------------
	origAsync := shadowWriteDispatchAsync
	origPool := mirrorOutbox.Load()
	t.Cleanup(func() {
		shadowWriteDispatchAsync = origAsync
		InitMirrorOutbox(origPool)
		setShadowWriteEnabledForTest(nil)
		drainShadowWriteSema()
		resetBacklogForTest()
	})

	// 门 ①：settings.Global 在单测二进制里是 nil，默认开关读出来是 false，
	// hook 会在 shadowWriteEnabled() 那一步就 return —— 那样本门恒绿。
	setShadowWriteEnabledForTest(func() bool { return true })
	if !shadowWriteEnabled() {
		t.Fatal("前提不成立：setShadowWriteEnabledForTest 没能让开关为真 —— " +
			"本门会在 hook 提前 return 时恒绿。**不要**把恒绿当通过。")
	}

	// 门 ②：让 EnqueueMirrorFailure 返回 false，这样才会退到 appendBacklog。
	// 这一步复用了 TestEnqueueMirrorFailure_NilPoolReturnsFalse 已钉住的
	// 「nil pool ⇒ false」契约；本门验的是**调用方有没有真的用上那个 false**。
	InitMirrorOutbox(nil)
	if EnqueueMirrorFailure(terminalEntry("probe_sema_preflight"), "gw_test_session", "probe") {
		t.Fatal("前提不成立：nil pool 时 EnqueueMirrorFailure 返回了 true —— " +
			"本门走不到 appendBacklog，会恒绿")
	}

	shadowWriteDispatchAsync = true
	resetBacklogForTest()
	drainShadowWriteSema()

	writer := &semaCountingWriter{ch: make(chan struct{}, 4)}
	hook := PersistHook(writer)

	// --- 用例 A（阳性）：8 槽全满 -------------------------------------
	fillShadowWriteSema()
	if got := len(shadowWriteSema); got != cap(shadowWriteSema) {
		t.Fatalf("前提不成立：信号量只填了 %d/%d 槽，本门会走正常派发而恒绿", got, cap(shadowWriteSema))
	}

	entryA := terminalEntry("probe_sema_full")
	hook(entryA)

	pending, _ := BacklogStats()
	if pending != 1 {
		t.Errorf("8 槽全满时 backlog 里有 %d 条（应为 1）—— "+
			"这次请求既没走 run()、也没落进 backlog，等于凭空消失："+
			"没有 turn、没有 outbox 行、backlog 里也查不到",
			pending)
	}
	if n := writer.count(); n != 0 {
		t.Errorf("8 槽全满时 writer 被调用了 %d 次（应为 0）—— "+
			"派发不该绕过信号量上限", n)
	}
	if items := DrainBacklog(1); len(items) != 1 || items[0].RequestID != entryA.RequestID {
		t.Errorf("backlog 里那条的 request_id 不是本次请求：%+v", items)
	}

	// --- 用例 B（★ 反向对照）：8 槽排空 -------------------------------
	resetBacklogForTest()
	drainShadowWriteSema()
	if got := len(shadowWriteSema); got != 0 {
		t.Fatalf("前提不成立：信号量没能排空（%d），本门的对照臂无效", got)
	}

	hook(terminalEntry("probe_sema_free"))

	select {
	case <-writer.ch:
	case <-time.After(3 * time.Second):
		t.Fatal("8 槽空闲时 writer 在 3s 内没有被调用 —— " +
			"正常派发路径坏了，对照臂无法区分「default 分支没跑」与「整个 hook 没跑」")
	}
	if n := writer.count(); n != 1 {
		t.Errorf("8 槽空闲时 writer 被调用 %d 次（应为 1）", n)
	}
	pending, _ = BacklogStats()
	if pending != 0 {
		t.Errorf("8 槽空闲时 backlog 里有 %d 条（应为 0）—— "+
			"正常路径不该往 backlog 里塞；否则用例 A 的 pending==1 证明不了什么",
			pending)
	}

	t.Logf("信号量满 ⇒ writer 0 次 / backlog 1 条；信号量空 ⇒ writer 1 次 / backlog 0 条 —— 退化契约成立")
}
