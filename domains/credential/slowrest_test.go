package credential

// slowrest_test.go — P0-4 的门：首字节超时的归责契约。
//
// # 这道门守住哪块缺口
//
// 2026-10-24 的 magpie 刷新轮（docs/research/2026-10-24-magpie-v0.1.1367-…md
// §6-P0-4）把 magpie 对「慢启动」的契约拆成四条**可断言**的判据：
//
//	① **首两次超时不进 breaker OPEN**；
//	② 反复到阈值才 rest，且 **rest 的粒度是模型不是凭据**；
//	③ **退避随次数递增**；
//	④ provider 可整体关掉（magpie 的 RestCap=Off）。
//
// ★ **审计订正（计数）**：计划书写的是「③ **第三次**触发 rest」，
//   但 magpie 源码的 `slowRests = 2`（internal/gateway/routing.go:198-199）
//   配合 `if run.n < slowRests { return Rest{}, false }`（:1146-1148）
//   ⇒ **第 2 次**慢启动就 rest（n=1 放过、n=2 休息 1min、n=3 休息 2min）。
//   ⇒ 本门按 **magpie 源码**（第 2 次）落地，不按计划书的「第三次」。
//   ⇒ 差别记在《2026-10-24 magpie v0.1.1367 改造落地审计》§审计订正。
//
// ★ ④ 的 provider 级开关本仓**没有对应物**（本仓无 provider 级 rest cap），
//   用全局开关 `LLM_GATEWAY_SLOW_REST_DISABLE` 代替（见 slowrest.go 文件头差异①）。
//   ⇒ 本门断言全局开关生效，并把「没有 provider 级开关」记为**已知局限**。

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/pashagolub/pgxmock/v4"
)

// newTestSlowRest 造一个**可控时钟**的休息器，并把它装成进程级默认。
//
// ★ 每个用例都要重置进程级默认：休息器是**有状态**的，跨用例残留会让
//
//	"第一次放过"在第二个用例里变成"第二次休息"⇒ 门自己会假绿/假红。
func newTestSlowRest(t *testing.T) (*SlowRestTracker, *time.Time) {
	t.Helper()
	clock := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	tracker := NewSlowRestTracker()
	tracker.SetClock(func() time.Time { return clock })
	prev := defaultSlowRest
	defaultSlowRest = tracker
	t.Cleanup(func() { defaultSlowRest = prev })
	return tracker, &clock
}

// TestSlowRestFirstOccurrenceIsForgiven 断言①：**第一次慢启动被放过**。
//
// ★ 这是 magpie 契约里最容易被违反的一条：「长 prompt 哪儿都慢」⇒
//
//	第一次不该罚任何人。
func TestSlowRestFirstOccurrenceIsForgiven(t *testing.T) {
	tracker, _ := newTestSlowRest(t)
	key := SlowRestKey(42, "minimax-m3")

	d := tracker.Observe(key)
	if d.Rest {
		t.Fatalf("第 1 次慢启动 rest = true，want false（magpie: once, nobody rests）")
	}
	if d.Count != 1 {
		t.Fatalf("计数 = %d，want 1", d.Count)
	}
	if !d.Until.IsZero() {
		t.Fatalf("放过时 Until 应为零值，got %v", d.Until)
	}
}

// TestSlowRestSecondOccurrenceRests 断言②：**第 2 次慢启动触发休息**，
// 且休息时长是基准 1 分钟、粒度是模型。
//
// ★ 审计订正：计划书写「第三次」，magpie 源码是**第 2 次**（slowRests=2）。
func TestSlowRestSecondOccurrenceRests(t *testing.T) {
	tracker, clock := newTestSlowRest(t)
	key := SlowRestKey(42, "minimax-m3")

	tracker.Observe(key)
	d := tracker.Observe(key)
	if !d.Rest {
		t.Fatal("第 2 次慢启动未 rest —— magpie slowRests=2 ⇒ 第 2 次就该休息")
	}
	if d.Count != 2 {
		t.Fatalf("计数 = %d，want 2", d.Count)
	}
	if d.By != "cooldown" {
		t.Fatalf("退避阶段 = %q，want cooldown（首次休息）", d.By)
	}
	want := clock.Add(SlowFallbackCooldown)
	if !d.Until.Equal(want) {
		t.Fatalf("休息到 %v，want %v（基准 1min）", d.Until, want)
	}
	// 休息键必须**带模型** ⇒ 粒度是模型不是凭据。
	if key != "42/minimax-m3" {
		t.Fatalf("休息键 = %q，want 42/minimax-m3（模型粒度）", key)
	}
}

// TestSlowRestBackoffIncreases 断言③：**退避随次数递增**。
//
// ★ magpie: `fallbackCooldown<<min(run.n-slowRests, 10)`
//
//	⇒ n=2 → 1min、n=3 → 2min、n=4 → 4min。
func TestSlowRestBackoffIncreases(t *testing.T) {
	tracker, clock := newTestSlowRest(t)
	key := SlowRestKey(42, "minimax-m3")

	wantDur := []time.Duration{
		SlowFallbackCooldown,     // n=2
		2 * SlowFallbackCooldown, // n=3
		4 * SlowFallbackCooldown, // n=4
	}
	wantBy := []string{"cooldown", "backoff", "backoff"}

	tracker.Observe(key) // n=1 ⇒ 放过
	for i := 0; i < len(wantDur); i++ {
		// ★ 必须**跨过上一轮休息窗口**再观察：magpie 的计数条件是
		//   `if !resting || prev.Why != failSlow || run.n == 0 { run.n++ }`
		//   （internal/gateway/routing.go:1141-1143）—— **正在休息时计数被冻结**
		//   （键被挂在架上，本来就不会被再选中 ⇒ 不可能有新的慢启动）。
		//   ⇒ 退避的推进来自"**休息结束后的下一次**慢启动"，不是"同一刻连踩"。
		if i > 0 {
			*clock = clock.Add(wantDur[i-1])
		}
		d := tracker.Observe(key)
		if !d.Rest {
			t.Fatalf("第 %d 次观察未 rest", i+2)
		}
		if d.By != wantBy[i] {
			t.Fatalf("n=%d 退避阶段 = %q，want %q", i+2, d.By, wantBy[i])
		}
		want := clock.Add(wantDur[i])
		if !d.Until.Equal(want) {
			t.Fatalf("n=%d 休息到 %v，want %v（退避未递增）", i+2, d.Until, want)
		}
	}
}

// TestSlowRestCountIsFrozenWhileResting 断言 magpie 的计数冻结语义。
//
// ★ 这条不是实现细节，是**归责语义**的一部分：
//
//	一个请求自己的重试（或一次并发风暴）不该把慢启动计数推高
//	⇒ 否则一次抖动就可能把模型连续休息很久。
func TestSlowRestCountIsFrozenWhileResting(t *testing.T) {
	tracker, _ := newTestSlowRest(t)
	key := SlowRestKey(42, "minimax-m3")

	tracker.Observe(key) // n=1
	d := tracker.Observe(key)
	if !d.Rest {
		t.Fatal("第 2 次应休息")
	}
	// 休息窗口内再观察 ⇒ 计数冻结，仍是 n=2、时长不变。
	frozen := tracker.Observe(key)
	if frozen.Count != 2 {
		t.Fatalf("休息窗口内计数 = %d，want 2（magpie: resting 时不递增）", frozen.Count)
	}
	if !frozen.Until.Equal(d.Until) {
		t.Fatalf("休息窗口内休息到 %v，want %v（不应被延长）", frozen.Until, d.Until)
	}
}

// TestSlowRestForgetWindowResetsCount 断言 magpie 的 `slowForget=30min`：
// 距上次慢启动超过遗忘窗 ⇒ 计数归零 ⇒ 下一次**又是一次"第一次"**（放过）。
func TestSlowRestForgetWindowResetsCount(t *testing.T) {
	tracker, clock := newTestSlowRest(t)
	key := SlowRestKey(42, "minimax-m3")

	tracker.Observe(key)                    // n=1
	if d := tracker.Observe(key); !d.Rest { // n=2 ⇒ rest
		t.Fatal("第 2 次应休息")
	}
	// 跳到遗忘窗之后。
	*clock = clock.Add(SlowForget + time.Second)
	d := tracker.Observe(key)
	if d.Rest {
		t.Fatal("遗忘窗之后的第一次慢启动不该 rest（新一轮从 0 开始）")
	}
	if d.Count != 1 {
		t.Fatalf("遗忘窗后计数 = %d，want 1（已归零重来）", d.Count)
	}
}

// TestSlowRestIsModelScoped 断言**休息粒度是模型不是凭据**：
// 同一凭据的另一个模型、以及另一把凭据的同名模型，计数**互不影响**。
//
// ★ 这是 magpie 的 restID = restKey + "/" + model 的直接对应
//
//	（internal/gateway/fallback.go:119-124）。
func TestSlowRestIsModelScoped(t *testing.T) {
	tracker, clock := newTestSlowRest(t)

	modelA := SlowRestKey(42, "minimax-m3")
	modelB := SlowRestKey(42, "minimax-m2")
	otherCred := SlowRestKey(43, "minimax-m3")

	tracker.Observe(modelA)
	tracker.Observe(modelA)
	if d := tracker.Observe(modelB); d.Rest {
		t.Fatal("同凭据的另一个模型被连带休息了 —— 粒度必须是模型")
	}
	if d := tracker.Observe(otherCred); d.Rest {
		t.Fatal("另一把凭据的同名模型被连带休息了 —— 粒度必须是 (凭据,模型)")
	}
	// 原键仍然是第 3 次 ⇒ 退避到 2 分钟。
	// ★ 必须跨过第 2 次给出的 1 分钟休息窗口（magpie: resting 时计数冻结）。
	*clock = clock.Add(SlowFallbackCooldown)
	d := tracker.Observe(modelA)
	if !d.Rest || d.Count != 3 {
		t.Fatalf("原键计数 = %d（rest=%v），want 3", d.Count, d.Rest)
	}
}

// TestSlowRestDisabledByEnv 断言④的替代形态：全局开关关掉 ⇒ **永不休息**。
//
// ★ 本仓没有 magpie 的 provider 级 RestCap=Off ⇒ 用全局开关代替（已知局限）。
func TestSlowRestDisabledByEnv(t *testing.T) {
	t.Setenv(slowRestEnvName, "1")
	tracker := NewSlowRestTracker()
	if !tracker.Disabled() {
		t.Fatalf("%s=1 时应当是关闭态", slowRestEnvName)
	}
	key := SlowRestKey(42, "minimax-m3")
	for i := 0; i < 5; i++ {
		if d := tracker.Observe(key); d.Rest {
			t.Fatalf("关闭态下第 %d 次观察仍然休息了", i+1)
		}
	}
}

// TestSlowRestNilReceiverIsSafe 断言零值/空指针安全（调用方不该 panic）。
func TestSlowRestNilReceiverIsSafe(t *testing.T) {
	var tracker *SlowRestTracker
	if d := tracker.Observe("x"); d.Rest {
		t.Fatal("nil 休息器不应返回 rest")
	}
	if !tracker.Disabled() {
		t.Fatal("nil 休息器应视为关闭")
	}
}

// TestBreakerNeverOpensOnFirstByteTimeout 断言①的**熔断那半**：
// 首字节超时**永远**不进 breaker OPEN，也不累计连续失败。
//
// ★ 机制：`PenaltyOf(KindFirstByteTimeout) == PenaltyNone`
//
//	⇒ Breaker.RecordFailure 在 breaker.go 的 PenaltyNone 短路处直接返回。
//	⇒ 与「流中超时」(KindStreamTimeout) 保持对照：后者仍照罚。
func TestBreakerNeverOpensOnFirstByteTimeout(t *testing.T) {
	b := New(1, 42)
	for i := 0; i < 10; i++ {
		b.RecordFailure(errorsx.KindFirstByteTimeout)
	}
	if got := b.State(); got != StateClosed {
		t.Fatalf("首字节超时 %d 次后 breaker 状态 = %v，want closed（首次放过、且不进 OPEN）", 10, got)
	}
	if got := b.ConsecutiveFailures(); got != 0 {
		t.Fatalf("连续失败计数 = %d，want 0", got)
	}
	// 对照组：流中超时**仍然**照罚（既有语义不被本轮改动破坏）。
	b2 := New(1, 42)
	b2.RecordFailure(errorsx.KindStreamTimeout)
	if got := b2.ConsecutiveFailures(); got == 0 {
		t.Fatal("对照组失效：流中超时应当照常计入连续失败")
	}
}

// TestWriteOnErrorFirstByteTimeoutFirstOccurrenceWritesNothing 断言：
// **第一次**首字节超时**不写任何库**（既不写绑定、也不写凭据）。
//
// ★ 这条是「第一次放过」在**写库侧**的落点：只断言错误矩阵不够，
//
//	因为真正伤到生产的是"写了冷却"。
func TestWriteOnErrorFirstByteTimeoutFirstOccurrenceWritesNothing(t *testing.T) {
	newTestSlowRest(t)
	mockDB := newSQLOnlyMock()
	defer mockDB.Close()

	w := &Writer{dbPool: mockDB}
	if err := w.WriteOnError(context.Background(), 42, "minimax-m3",
		Failure{Kind: errorsx.KindFirstByteTimeout}); err != nil {
		t.Fatalf("WriteOnError: %v", err)
	}
	// ★ 没有任何 ExpectExec/ExpectQuery ⇒ 任何写库都会让 pgxmock 报错。
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatalf("第一次慢启动不应有任何写库：%v", err)
	}
}

// TestWriteOnErrorFirstByteTimeoutSecondOccurrenceCoolsBindingOnly 断言②：
// 第 2 次慢启动**只冷却 (凭据,模型) 绑定**，**不动凭据级状态**。
//
// ★ 这是「罚在模型粒度而不是账号粒度」在**写库侧**的落点：
//
//	同一把凭据的其它模型必须照常服务。
func TestWriteOnErrorFirstByteTimeoutSecondOccurrenceCoolsBindingOnly(t *testing.T) {
	newTestSlowRest(t)
	mockDB := newSQLOnlyMock()
	defer mockDB.Close()

	// 第 1 次：放过（无写库）。
	w := &Writer{dbPool: mockDB}
	if err := w.WriteOnError(context.Background(), 42, "minimax-m3",
		Failure{Kind: errorsx.KindFirstByteTimeout}); err != nil {
		t.Fatalf("第 1 次 WriteOnError: %v", err)
	}

	// 第 2 次：应当写绑定。
	expectRawBindingResolution(mockDB, 42, "minimax-m3", "minimax-m3")
	mockDB.ExpectExec(`UPDATE credential_model_bindings`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := w.WriteOnError(context.Background(), 42, "minimax-m3",
		Failure{Kind: errorsx.KindFirstByteTimeout}); err != nil {
		t.Fatalf("第 2 次 WriteOnError: %v", err)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatalf("未满足的期望: %v", err)
	}
	// ★ 没有对 credentials 表的 ExpectExec ⇒ 若实现写了凭据级状态，
	//   pgxmock 会因"意外调用"而报错（测试失败）。
}
