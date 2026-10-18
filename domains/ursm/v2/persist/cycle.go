package persist

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// 本文件提供 persist 一轮 Collect+Flush 的**预算编排 + 观测**。
//
// ── 背景（2026-10-18 §10.110.54/§10.110.55，245 与 154 双机持续丢数据）──
//
//	cmd/gateway/main.go:1358  ctx, cancel := context.WithTimeout(persistCtx, 30*time.Second)
//	                           rows, err := persistWriter.Collect(ctx)   ← 同一 ctx
//	                           …          persistWriter.Flush(ctx, rows)    ← 仍是同一 ctx
//
// `Collect` 对约 1,800 个 key 各调一次 `redissafe.SafeHGetAll`，而它是
// TYPE + HGETALL **两次串行往返**（internal/redis/safe_operations.go:123-140），
// 即每轮 3,600~3,800 次串行往返。临界延迟 30,000ms ÷ 3,800 ≈ **7.9 ms/往返**；
// 实测往返 8.8ms（154 视角）/ 26.1ms（245 视角）⇒ Collect 吃满 30 秒
// ⇒ Flush 只剩几百毫秒 ⇒ 只插进几十行 ⇒ 单事务回滚，**整轮 1,800 行全丢**。
//
// ── ⚠️ 为什么默认**不**预切（这是实测结论，不是保守取舍）──
//
// 直觉上「给 Flush 独立预算」像显然的修复。实测证明**单独做会让写入量变少**：
// 1:1000 缩放下让 Collect 需要 25s（生产口径 25s）、批 1,800 行、Flush 1 行/ms：
//
//	生产形态（共用 30s ctx）      Flush 拿到 3ms   ⇒ 写入 2 行
//	预切（Collect 20s + Flush 10s）Flush 根本没被调用 ⇒ 写入 0 行   ← 严格更差
//	Collect 不设上限 + Flush 10s   Flush 拿到 10ms  ⇒ 写入 8 行
//
// 根因：`Collect` 超时时是 `return nil, err`（writer.go:218）——**一行都不交出来**。
// 预切让 Collect 更早超时，于是「还能写 2 行」变成「写 0 行」。
//
// ⇒ 预切只有在**`Collect` 失败时仍交出已读到的行**之后才有意义（§55 方案 B）。
// 在那之前默认 Preset=false，保持与生产完全一致的旧行为，只产出观测数据。
// 观测本身是真需求：§40 §四要「Flush 各段耗时」，至今任何日志里都没有这个数。
type CycleTimeouts struct {
	// Collect / Flush 是各阶段的天花板。仅在 Preset=true 时生效。
	Collect time.Duration
	Flush   time.Duration
	// Preset=true ⇒ 按阶段预切（各自独立计时，从父 ctx 派生）。
	// Preset=false ⇒ 两阶段共用一个 (Collect+Flush) 的 ctx，即历史行为。
	Preset bool
}

const (
	// DefaultTotalCycleBudget preserves the historical 30s ceiling exactly.
	DefaultTotalCycleBudget = 30 * time.Second
	DefaultFlushTimeout     = 10 * time.Second
)

// DefaultCycleTimeouts is what the gateway uses when nothing is configured:
// identical to the pre-existing single-ctx behaviour, plus observability.
func DefaultCycleTimeouts() CycleTimeouts {
	return CycleTimeouts{
		Collect: DefaultTotalCycleBudget,
		Flush:   DefaultFlushTimeout,
		Preset:  false,
	}
}

// CycleTimeoutsFromEnv reads optional overrides.
//
//	URSM_V2_PERSIST_CYCLE_PRESET=1            enable per-stage budgeting
//	URSM_V2_PERSIST_COLLECT_TIMEOUT_MS=...    per-stage ceiling (Preset mode)
//	URSM_V2_PERSIST_TOTAL_BUDGET_MS=...       shared ceiling (default mode)
//
// A non-positive or unparseable value keeps the default rather than dropping
// the ceiling: an accidentally empty var must never restore the unbounded wait.
func CycleTimeoutsFromEnv() CycleTimeouts {
	t := DefaultCycleTimeouts()
	t.Preset = os.Getenv("URSM_V2_PERSIST_CYCLE_PRESET") == "1"
	if v, ok := durationFromEnv("URSM_V2_PERSIST_TOTAL_BUDGET_MS"); ok {
		t.Collect = v
	}
	if v, ok := durationFromEnv("URSM_V2_PERSIST_COLLECT_TIMEOUT_MS"); ok {
		t.Collect = v
	}
	if v, ok := durationFromEnv("URSM_V2_PERSIST_FLUSH_TIMEOUT_MS"); ok {
		t.Flush = v
	}
	if !t.Preset {
		// Shared mode spends one budget; Flush's ceiling is only meaningful
		// once the stages are separated.
		t.Flush = DefaultFlushTimeout
	}
	return t
}

func durationFromEnv(key string) (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, false
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return 0, false
	}
	return time.Duration(ms) * time.Millisecond, true
}

// CycleStages is the Collect/Flush pair one persistence cycle drives.
// *Writer satisfies it; tests substitute a fake that can be made slow.
type CycleStages interface {
	Collect(ctx context.Context) ([]Row, error)
	Flush(ctx context.Context, rows []Row) error
}

// CycleOutcome is the per-cycle observability record §40 §四 asked for and
// never got: how long each stage actually took, and what Flush was granted.
type CycleOutcome struct {
	Rows          int
	CollectErr    error
	FlushErr      error
	CollectTook   time.Duration
	FlushTook     time.Duration
	FlushBudget   time.Duration // 0 when Flush did not run
	PresetApplied bool

	// ── 写入者守卫字段（§10.110.61，详见 writerguard.go）──
	//
	// 三态**互斥且有序**，判读顺序固定为 GuardErr → YieldedToPeer → GuardHeld：
	//
	//	GuardErr != nil  取锁出错，本轮照写但没互斥（fail-open）
	//	YieldedToPeer    别人持锁，本轮**什么都没做**
	//	GuardHeld        本轮持锁并真的跑了
	//
	// ★ 三者必须能区分，不能压成一个 bool：
	//	「让位」「取锁失败」「正常写入」的运维含义完全不同，
	//	压成一个字段后，"这 40 分钟一行都没写进去" 和 "这 40 分钟有别人在写"
	//	在日志上长得一模一样 —— 那正是 §10.110.60 里 streak 被单次成功清零
	//	制造假阳性的同一种形状。
	GuardErr      error
	YieldedToPeer bool
	GuardHeld     bool
}

// Err reports the error the caller should log or count as a failure streak.
func (o CycleOutcome) Err() error {
	if o.CollectErr != nil {
		return o.CollectErr
	}
	return o.FlushErr
}

func (o CycleOutcome) String() string {
	return fmt.Sprintf("rows=%d collect=%s flush=%s flush_budget=%s preset=%v guard=%s",
		o.Rows,
		o.CollectTook.Round(time.Millisecond),
		o.FlushTook.Round(time.Millisecond),
		o.FlushBudget.Round(time.Millisecond),
		o.PresetApplied,
		o.GuardState())
}

// RunCycle drives one Collect-then-Flush cycle and reports what happened.
//
// Preset=false reproduces the historical behaviour byte for byte: a single
// (Collect+Flush) context handed to both stages. That is the default because
// pre-cutting the budget on its own *reduces* the number of rows written
// (see the file comment and §55) — it only pays off once Collect can hand
// back the rows it already read when it times out.
//
// Preset=true is the per-stage shape: each stage's timer is derived from the
// caller's ctx, never from the other stage's, so a Collect that exhausts its
// own slice cannot rob Flush of its deadline.
func RunCycle(ctx context.Context, s CycleStages, t CycleTimeouts) CycleOutcome {
	if t.Collect <= 0 {
		t.Collect = DefaultTotalCycleBudget
	}
	if t.Flush <= 0 {
		t.Flush = DefaultFlushTimeout
	}

	out := CycleOutcome{PresetApplied: t.Preset}

	cStart := time.Now()
	var rows []Row
	var collectErr error

	if t.Preset {
		collectCtx, cancelCollect := context.WithTimeout(ctx, t.Collect)
		rows, collectErr = s.Collect(collectCtx)
		// Release the Collect timer *before* Flush so the stage that is about
		// to do the durable write is the only one holding a live deadline.
		cancelCollect()
		out.CollectTook = time.Since(cStart)
		out.Rows = len(rows)
		out.CollectErr = collectErr
		if len(rows) == 0 {
			return out
		}
		out.FlushBudget = t.Flush
		fStart := time.Now()
		flushCtx, cancelFlush := context.WithTimeout(ctx, t.Flush)
		out.FlushErr = s.Flush(flushCtx, rows)
		cancelFlush()
		out.FlushTook = time.Since(fStart)
		return out
	}

	// Shared budget — the pre-existing production shape.
	cycleCtx, cancelCycle := context.WithTimeout(ctx, t.Collect)
	defer cancelCycle()
	rows, collectErr = s.Collect(cycleCtx)
	out.CollectTook = time.Since(cStart)
	out.Rows = len(rows)
	out.CollectErr = collectErr
	if len(rows) == 0 {
		return out
	}
	out.FlushBudget = t.Collect - out.CollectTook
	if out.FlushBudget < 0 {
		out.FlushBudget = 0
	}
	fStart := time.Now()
	out.FlushErr = s.Flush(cycleCtx, rows)
	out.FlushTook = time.Since(fStart)
	return out
}
