package persist

// 2026-10-18：快照**写入者**跨实例互斥的判据（§10.110.61）。
//
// # 为什么必须有这道门（生产实测，§10.110.60）
//
//	ursm_node_snapshot_min 实测有**三个**并发写入者，各跑各的 release：
//	245@8781(2719, :16) / 154@8782(2699, :32) / 245@8782(2718 残留, :38)。
//	近 60 分钟成功率：245@8781 17%、245@8782 15%、154@8782 100%。
//	⇒ 跑两个进程那台机恰好就是失败率 85% 那台，且丢的是整轮 1,802 行。
//
// # 这组判据的形状，直接照抄兄弟文件 retention_peer_mutex_test.go
//
//	那边最要紧的一条是**阴性对照**（RealDropErrorStillDegrades）：
//	没有它，「把任意错误都归类成良性跳过」这种过度放宽的变异能同时骗过
//	让位用例与 42P01 用例。本文件的对应物是两条：
//
//	① TestWriterGuardHoldingRunsBothStagesAndReleasesOnce —— **正向**。
//	   「让位时两阶段都没跑」这条判据，**单独存在时会被「守卫压根没接线」
//	   同时满足**（什么都不做 ⇒ 什么都不跑）。必须配一条证明持锁时
//	   Collect/Flush 确实都跑过。
//	② TestWriterGuardFailsOpenWhenTheLockCannotBeTaken —— 真错误仍然写入。
//	   把守卫错误也归类成良性跳过，会把一次连接抖动放大成「审计数据永久停摆」。
//
// # 与留存锁的关键差异（别把两条注释合并引用）
//
//	留存 worker 的临界区就是**一个事务** ⇒ 用 pg_try_advisory_xact_lock。
//	persist 一轮 = Collect(Redis) + Flush(**自带独立事务**)
//	⇒ 事务级锁会在 Flush 提交那刻释放，罩不住整轮；这里必须是
//	   **会话级锁 + pool.Acquire() 显式持有的专用连接**。

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"
)

// guardFakeStages 记录两个阶段各自被调用了几次。
type guardFakeStages struct {
	collectCalls int
	flushCalls   int
	rows         []Row
	collectErr   error
	flushErr     error
}

func (f *guardFakeStages) Collect(ctx context.Context) ([]Row, error) {
	f.collectCalls++
	if f.collectErr != nil {
		return nil, f.collectErr
	}
	if f.rows == nil {
		return []Row{{TenantID: "default", CredentialID: 1}}, nil
	}
	return f.rows, nil
}

func (f *guardFakeStages) Flush(ctx context.Context, rows []Row) error {
	f.flushCalls++
	return f.flushErr
}

// guardFakeLocker 是可脚本化的 WriterLocker。
type guardFakeLocker struct {
	held   bool
	err    error
	keys   []int64
	locked int // 取锁尝试次数

	releaseCalls  int
	releaseCtxErr error // release 被调用那一刻 ctx.Err() 的值
	releaseErr    error
}

func (f *guardFakeLocker) TryLock(ctx context.Context, key int64) (func(context.Context) error, bool, error) {
	f.locked++
	f.keys = append(f.keys, key)
	if f.err != nil {
		return func(context.Context) error { return nil }, false, f.err
	}
	if !f.held {
		return func(context.Context) error { return nil }, false, nil
	}
	return func(relCtx context.Context) error {
		f.releaseCalls++
		f.releaseCtxErr = relCtx.Err()
		return f.releaseErr
	}, true, nil
}

func guardTimeouts() CycleTimeouts { return DefaultCycleTimeouts() }

// ① 让位：别人持锁 ⇒ 本轮**一个字节都不写**，且不报错。
func TestWriterGuardYieldsToPeerWithoutTouchingEitherStage(t *testing.T) {
	stages := &guardFakeStages{}
	locker := &guardFakeLocker{held: false}

	out := RunCycleGuarded(context.Background(), stages, locker, guardTimeouts())

	if out.Err() != nil {
		t.Fatalf("standing down must NOT surface as an error; got %v", out.Err())
	}
	if !out.YieldedToPeer {
		t.Fatal("YieldedToPeer = false, want true — the round must announce that it stood down")
	}
	if out.GuardHeld {
		t.Fatal("GuardHeld = true on a yielding round — we never held the lock")
	}
	if out.GuardErr != nil {
		t.Fatalf("GuardErr = %v, want nil — a refusal is not an error", out.GuardErr)
	}
	// ★ 最要紧的一条：让位绝不能碰 Redis 或 PG。
	// 每次让位若仍然跑一遍 Collect（生产 ~20 秒、3,600 次往返），
	// 守卫就从「省事」变成「把拥塞原样保留」，反而更糟。
	if stages.collectCalls != 0 || stages.flushCalls != 0 {
		t.Fatalf("yielding round touched the stages: collect=%d flush=%d, want 0/0",
			stages.collectCalls, stages.flushCalls)
	}
	if out.Rows != 0 {
		t.Fatalf("Rows = %d on a yielding round, want 0", out.Rows)
	}
	if locker.locked != 1 {
		t.Fatalf("lock attempts = %d, want 1", locker.locked)
	}
	if locker.releaseCalls != 0 {
		t.Fatalf("release called %d times on a yielding round, want 0 — we never held it",
			locker.releaseCalls)
	}
}

// ①的**正向对照**：持锁时两阶段确实都跑了，且锁被释放。
//
// 没有这条，上面那条「两阶段都没跑」会被「守卫压根没接线」同时满足 ——
// 判据全绿而功能不存在。
func TestWriterGuardHoldingRunsBothStagesAndReleasesOnce(t *testing.T) {
	stages := &guardFakeStages{rows: make([]Row, 42)}
	locker := &guardFakeLocker{held: true}

	out := RunCycleGuarded(context.Background(), stages, locker, guardTimeouts())

	if out.YieldedToPeer {
		t.Fatal("YieldedToPeer = true although the lock was granted")
	}
	if !out.GuardHeld {
		t.Fatal("GuardHeld = false although the lock was granted")
	}
	if stages.collectCalls != 1 || stages.flushCalls != 1 {
		t.Fatalf("stages ran collect=%d flush=%d, want 1/1 — the guard must wrap the WHOLE cycle",
			stages.collectCalls, stages.flushCalls)
	}
	if out.Rows != 42 {
		t.Fatalf("Rows = %d, want 42", out.Rows)
	}
	if locker.releaseCalls != 1 {
		t.Fatalf("release calls = %d, want exactly 1", locker.releaseCalls)
	}
}

// ② 真错误（连接抖动/权限/锁超时）⇒ **fail-open：照写**。
//
// 这是这道门的安全边界：它是审计快照，重复一行 << 漏掉整轮 1,802 行。
func TestWriterGuardFailsOpenWhenTheLockCannotBeTaken(t *testing.T) {
	boom := errors.New("connection reset")
	stages := &guardFakeStages{rows: make([]Row, 7)}
	locker := &guardFakeLocker{err: boom}

	out := RunCycleGuarded(context.Background(), stages, locker, guardTimeouts())

	if !errors.Is(out.GuardErr, boom) {
		t.Fatalf("GuardErr = %v, want %v", out.GuardErr, boom)
	}
	// ★ fail-open 的核心断言：不能因为守卫出错就不写。
	if stages.collectCalls != 1 || stages.flushCalls != 1 {
		t.Fatalf("stages ran collect=%d flush=%d, want 1/1 — a lock error must NOT stop the snapshot",
			stages.collectCalls, stages.flushCalls)
	}
	if out.YieldedToPeer {
		t.Fatal("YieldedToPeer = true on a lock error — we did not stand down, we failed to find out")
	}
	if out.GuardHeld {
		t.Fatal("GuardHeld = true although no lock was taken — that would be a lie in the log")
	}
	if out.Rows != 7 {
		t.Fatalf("Rows = %d, want 7", out.Rows)
	}
}

// 三条退出路径都必须恰好释放一次。漏释放 = 该实例永久失去写入资格，
// 而症状是「别的实例一直在写」，没有任何告警指向漏释放。
func TestWriterGuardReleasesOnEveryExitPath(t *testing.T) {
	cases := []struct {
		name         string
		stages       *guardFakeStages
		wantFlushRun int
	}{
		{"正常一轮", &guardFakeStages{rows: make([]Row, 10)}, 1},
		{"Collect 报错（RunCycle 提前返回，Flush 从未调用）",
			&guardFakeStages{collectErr: errors.New("redis down")}, 0},
		{"Collect 零行（同样提前返回）", &guardFakeStages{rows: []Row{}}, 0},
		{"Flush 报错", &guardFakeStages{rows: make([]Row, 10), flushErr: errors.New("insert timeout")}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locker := &guardFakeLocker{held: true}
			RunCycleGuarded(context.Background(), tc.stages, locker, guardTimeouts())

			if locker.releaseCalls != 1 {
				t.Fatalf("release calls = %d, want exactly 1 on every exit path",
					locker.releaseCalls)
			}
			if got := tc.stages.flushCalls; got != tc.wantFlushRun {
				t.Fatalf("flush calls = %d, want %d", got, tc.wantFlushRun)
			}
		})
	}
}

// ★ 解锁必须用一个**活的** ctx。
//
// RunCycle 内部 `defer cancelCycle()`，它返回时那 30 秒 ctx 已被取消；
// 而生产里它经常是**超时**取消（Collect 吃满预算）。
// 若解锁直接复用它，`pg_advisory_unlock` 会带着一个死 ctx 发出去，
// 解锁失败 ⇒ 连接被归还时仍带着锁 ⇒ 该实例此后**永远**拿不到锁。
// 这是「一处 ctx 复用」导致永久静默停写，症状完全指向别处。
func TestWriterGuardReleasesWithALiveContext(t *testing.T) {
	stages := &guardFakeStages{rows: make([]Row, 3)}
	locker := &guardFakeLocker{held: true}

	// 父 ctx 在周期开始前就已被取消 —— 比生产里更苛刻。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	RunCycleGuarded(ctx, stages, locker, guardTimeouts())

	if locker.releaseCalls != 1 {
		t.Fatalf("release calls = %d, want 1", locker.releaseCalls)
	}
	if locker.releaseCtxErr != nil {
		t.Fatalf("release ran with an already-cancelled ctx (ctx.Err()=%v); "+
			"the unlock must detach from the cycle ctx", locker.releaseCtxErr)
	}
}

// 锁键不得与本仓其它 advisory lock 相撞。
// 撞了 ⇒ 留存 DROP 与快照写入互相让位，两者都能静默停摆，
// 而且症状（分区不回收 / 快照不更新）完全指不到对方身上。
func TestWriterGuardLockKeyIsDistinctFromEveryOtherAdvisoryLock(t *testing.T) {
	if snapshotWriterLockKey == snapshotRetentionLockKey {
		t.Fatalf("writer lock key %d collides with the retention lock key", snapshotWriterLockKey)
	}
	// MV 刷新用的 632_2026_08_31 在 bg 包里，这里钉住字面量以防被改回去。
	const mvRefreshLockKey int64 = 632_2026_08_31
	if snapshotWriterLockKey == mvRefreshLockKey {
		t.Fatalf("writer lock key %d collides with the MV refresh lock key", snapshotWriterLockKey)
	}
	if snapshotWriterLockKey == 0 {
		t.Fatal("writer lock key must not be 0 — 0 is a valid key other subsystems could reach")
	}
}

// 三态必须渲染成三个**互不相同**的字符串。
// 压成一个字段后，「这 40 分钟一行都没写」和「这 40 分钟有别人在写」
// 在日志上长得一模一样 —— 那正是 §10.110.60 里 streak 被单次成功清零
// 制造假阳性的同一种形状。
func TestWriterGuardStateIsThreeWayDistinguishable(t *testing.T) {
	cases := []struct {
		name string
		out  CycleOutcome
		want string
	}{
		{"取锁出错", CycleOutcome{GuardErr: errors.New("x")}, "unavailable"},
		{"让位", CycleOutcome{YieldedToPeer: true}, "yielded"},
		{"持锁写入", CycleOutcome{GuardHeld: true}, "held"},
		{"守卫关闭", CycleOutcome{}, "off"},
	}
	seen := map[string]string{}
	for _, tc := range cases {
		got := tc.out.GuardState()
		if got != tc.want {
			t.Fatalf("%s: GuardState() = %q, want %q", tc.name, got, tc.want)
		}
		if prev, dup := seen[got]; dup {
			t.Fatalf("GuardState %q is shared by %q and %q — the three states must be distinguishable",
				got, prev, tc.name)
		}
		seen[got] = tc.name
	}

	// 优先级是**另一条**断言，不能混进上面的查重：fail-open 的那一轮
	// 既报错又真的跑了循环，若三个字段同时置位，必须报「取锁出错」
	// 而不是被后面的分支覆盖成「持锁写入」。
	if got := (CycleOutcome{GuardErr: errors.New("x"), YieldedToPeer: true, GuardHeld: true}).GuardState(); got != "unavailable" {
		t.Fatalf("GuardState() with all three set = %q, want %q (error takes precedence)", got, "unavailable")
	}
}

// 没有给 locker（测试模式 / DB 禁用）⇒ 退化成原行为，**不是**拒绝写入。
func TestWriterGuardWithoutALockerDegradesToRunCycle(t *testing.T) {
	stages := &guardFakeStages{rows: make([]Row, 5)}

	out := RunCycleGuarded(context.Background(), stages, nil, guardTimeouts())

	if stages.collectCalls != 1 || stages.flushCalls != 1 {
		t.Fatalf("stages ran collect=%d flush=%d, want 1/1 — a missing locker must not stop the snapshot",
			stages.collectCalls, stages.flushCalls)
	}
	if out.GuardHeld || out.YieldedToPeer || out.GuardErr != nil {
		t.Fatalf("outcome = %+v, want a plain unguarded cycle", out)
	}
	if out.GuardState() != "off" {
		t.Fatalf("GuardState() = %q, want %q", out.GuardState(), "off")
	}
}

// 取锁路径自己必须封顶：守卫排在 persist 的 30s 预算**之外**，
// 不封顶的话，连接池被打满时守卫会把一轮写入拖得比没有守卫更久。
// 这里断言生产取值是个有界的正数，而不是给具体秒数设门
// （秒数是调参，见 §10.110.57 的教训：别把不敏感的旋钮钉成契约）。
func TestGuardLockTimeoutIsBoundedAndPositive(t *testing.T) {
	if guardLockTimeout <= 0 || guardLockTimeout > 10*time.Second {
		t.Fatalf("guardLockTimeout = %v, want a bounded positive value <= 10s", guardLockTimeout)
	}
	if guardUnlockTimeout <= 0 || guardUnlockTimeout > 10*time.Second {
		t.Fatalf("guardUnlockTimeout = %v, want a bounded positive value <= 10s", guardUnlockTimeout)
	}
}

// TestGuardUnlockOutcomeDestroysTheConnectionOnUnlockFailure 钉住 2026-10-18
// 12h 审计轮发现的那条静默永久停写：
//
//	解锁查询本身出错时，会话级锁**可能仍在那条连接上**。pgxpool 归还连接
//	只判 closed/busy/in-tx/expired，从不碰 advisory lock
//	（vendor/.../pgxpool/conn.go 的 Release），所以「出错也照样 Release」
//	等于把锁连同连接一起借给下一个使用者 —— 该实例此后永远抢不到锁，
//	症状只是「别的实例一直在写」，没有任何告警指向泄漏。
//	⇒ 这条分支必须硬关连接，让服务端结束会话把锁带走。
func TestGuardUnlockOutcomeDestroysTheConnectionOnUnlockFailure(t *testing.T) {
	boom := errors.New("connection reset by peer")

	destroy, err := guardUnlockOutcome(boom, false)
	if !destroy {
		t.Fatalf("unlock 出错时 destroyConn = false：连接将被归还池并带着锁一起借出，"+
			"该实例此后永久静默失去写入资格（unlockErr=%v)", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the unlock error to propagate", err)
	}
}

// 反向门：解锁「成功但返回 false」时服务端已明确回答这条连接上没有该锁，
// 此时归还连接是安全的（不硬关，避免把一次可恢复的状态异常放大成断连）。
func TestGuardUnlockOutcomeReturnsTheConnectionWhenUnlockSaidNotHeld(t *testing.T) {
	destroy, err := guardUnlockOutcome(nil, false)
	if destroy {
		t.Fatalf("unlocked=false 时 destroyConn = true：服务端已回答该连接没有锁，硬关属于过度处置")
	}
	if !errors.Is(err, errGuardNotHeld) {
		t.Fatalf("err = %v, want errGuardNotHeld", err)
	}
}

// 正常路径：解锁成功 ⇒ 归还连接、不报错。
func TestGuardUnlockOutcomeReturnsTheConnectionOnSuccess(t *testing.T) {
	destroy, err := guardUnlockOutcome(nil, true)
	if destroy {
		t.Fatalf("解锁成功时 destroyConn = true：正常路径不该断连")
	}
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

// TestWriterGuardReleaseClosureActuallyUsesTheUnlockDecision 是上面那条
// 决策表的**接线门**（AGENTS.md §55 的 M43「只测函数不测接线」同族）。
//
//	变异验证：把 TryLock 的 release 闭包改回
//	「无条件 conn.Release()、从不问 guardUnlockOutcome」后，
//	TestGuardUnlockOutcomeDestroysTheConnectionOnUnlockFailure **仍然是绿的**
//	—— 纯函数被测得很好，接线却可以完全绕过它。这条门补上那一半：
//	断言 release 闭包里真的调了 guardUnlockOutcome、真的有 Close 路径，
//	且 Release 出现在决策**之后**。
func TestWriterGuardReleaseClosureActuallyUsesTheUnlockDecision(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "writerguard.go", nil, 0)
	if err != nil {
		t.Fatalf("parse writerguard.go: %v", err)
	}

	// 找到 pgxWriterLocker.TryLock，并取它 return 出去的那个 FuncLit。
	var releaseFn *ast.FuncLit
	ast.Inspect(file, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "TryLock" {
			return true
		}
		if fd.Recv == nil || len(fd.Recv.List) == 0 {
			return true
		}
		star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
		var recvName string
		if ok {
			// 指针接收者：*pgxWriterLocker
			if ident, ok := star.X.(*ast.Ident); ok {
				recvName = ident.Name
			}
		} else if ident, ok := fd.Recv.List[0].Type.(*ast.Ident); ok {
			// 值接收者：pgxWriterLocker
			recvName = ident.Name
		}
		if recvName != "pgxWriterLocker" {
			return true
		}
		for _, stmt := range fd.Body.List {
			ret, ok := stmt.(*ast.ReturnStmt)
			if !ok || len(ret.Results) == 0 {
				continue
			}
			if lit, ok := ret.Results[0].(*ast.FuncLit); ok {
				releaseFn = lit
			}
		}
		return true
	})
	if releaseFn == nil {
		t.Fatal("未在 pgxWriterLocker.TryLock 里找到返回的 release 闭包")
	}

	// 按源码顺序收集闭包内的函数调用名。
	var calls []string
	ast.Inspect(releaseFn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			calls = append(calls, fn.Name)
		case *ast.SelectorExpr:
			calls = append(calls, fn.Sel.Name)
		}
		return true
	})

	idxOf := func(name string) int {
		for i, c := range calls {
			if c == name {
				return i
			}
		}
		return -1
	}

	decision, release, closeIdx := idxOf("guardUnlockOutcome"), idxOf("Release"), idxOf("Close")
	if decision < 0 {
		t.Fatalf("release 闭包从未调用 guardUnlockOutcome（实际调用序列 %v）—— "+
			"决策表成了死代码，锁泄漏修复没有接线", calls)
	}
	if closeIdx < 0 {
		t.Fatalf("release 闭包里没有 Close 路径（实际调用序列 %v）—— "+
			"解锁失败时无法结束会话，会话级锁会跟着连接一起回池", calls)
	}
	if release < 0 {
		t.Fatalf("release 闭包里既没有 Release 也没有 Close（实际调用序列 %v）", calls)
	}
	if decision > release {
		t.Fatalf("Release() 出现在 guardUnlockOutcome 之前（实际调用序列 %v）—— "+
			"连接在拿到处置结论之前就归还了，等于恒定归还", calls)
	}
}
