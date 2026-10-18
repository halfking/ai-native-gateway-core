package persist

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 快照**写入者**跨实例互斥（§10.110.61）。
//
// ── 为什么需要它（2026-10-18 生产实测）──
//
//	`ursm_node_snapshot_min` 实测有**三个**并发写入者，各跑各的 release：
//
//	  245@8781（活跃槽，release 2719）  每分钟 :16
//	  154@8782（          release 2699）  每分钟 :32
//	  245@8782（**上一版残留**，2718）    每分钟 :38
//
//	网关二进制里**没有任何实例角色概念**：`ResolvePersistEnabled()` 只看
//	URSM v2 mode（off/shadow/authoritative/canary），而 `run/active-port`
//	只被 nginx 与 Prometheus file_sd 消费，二进制自己从不读它。
//	⇒ 蓝绿切流后旧槽位只要没被显式停掉，就会一直写下去。
//
//	实测后果（近 60 分钟，按单元）：
//
//	  245@8781  成功率 17%（单轮耗时 20~22s）
//	  245@8782  成功率 15%（单轮耗时 13~29s）
//	  154@8782  成功率 100%（单轮耗时 6~17s）
//
//	**跑两个进程的那台机恰好就是失败率 85% 的那台**：每个进程各自把
//	30s 预算吃到只剩几百毫秒，于是 Flush 在 `insert row ... context
//	deadline exceeded` 上整轮回滚，1,802 行全丢。
//
// ── 形态：会话级锁 + 显式持有的专用连接 ──
//
//	⚠ 与 `retention_partition.go` 的 `pg_try_advisory_xact_lock` **不同**，
//	且这个不同是必需的，不是风格差异：
//
//	留存 worker 的整个临界区就是**一个事务**，事务级锁刚好罩住。
//	而 persist 一轮 = Collect（打 Redis，不碰 PG）+ Flush（**自带独立事务**）
//	⇒ 事务级锁会在 Flush 提交的那一刻释放，罩不住 Collect 那 20 秒，
//	也根本不是一个事务，锁不住。
//
//	那段注释里「走连接池 ⇒ 会话级锁绑在哪条连接上不可靠」同样不适用：
//	它反对的是**经连接池隐式持有**（`pool.Query` 拿哪条连接不可知）。
//	这里用 `pool.Acquire()` **显式取出一条连接**并全程持有，
//	绑定关系是确定的 —— ★ 这两个前提的区别请勿再被合并引用。

// snapshotWriterLockKey 守住「同一时刻只有一个快照写入者」的 advisory lock 键。
//
// ★ 必须与本仓其它 advisory lock 不撞。现有两个：
//
//	snapshotRetentionLockKey = 632_2026_10_14（本包，留存 DROP）
//	MV 刷新                      = 632_2026_08_31
//
//	编号沿用 `<子系统>_<年>_<月>_<日>` 的既有形状。
const snapshotWriterLockKey int64 = 632_2026_10_18

// guardLockTimeout 限制「取一条连接 + 取锁」的等待。
//
//	守卫是**每轮开头**调用的，排在 persist 自己的 30s 预算之外，
//	所以它必须自己封顶：否则连接池被打满时，守卫会把一轮写入
//	拖到比没有守卫更久。
const guardLockTimeout = 2 * time.Second

// guardUnlockTimeout 限制释放路径（解锁 + 归还连接）的等待。
const guardUnlockTimeout = 2 * time.Second

// errGuardNotHeld 是释放阶段的哨兵：解锁返回 false。
//
//	意味着这条连接上本来就没有该锁 —— 通常是连接已被服务端断开
//	（进程被杀 / 网络中断）后又被复用。属于不可恢复的状态，
//	必须显式暴露，而不是当成成功。
var errGuardNotHeld = errors.New("persist writer guard: advisory lock was not held at release")

// WriterLocker 是守卫需要的最小面：取一把跨实例互斥锁。
//
// ★ 抽成接口而不是直接吃 *pgxpool.Pool，是为了让守卫**不连数据库也能测**：
//
//	pgx 的 Acquire/QueryRow 没法伪造，而「另一个写入者持锁时本实例让位」
//	这条正是本改动最需要被证明的行为。
type WriterLocker interface {
	// TryLock 尝试取锁。
	//
	// 返回的 release 在**每一条退出路径上都被调用**（defer），
	// 且必须与 ctx 解绑 —— 调用方的 ctx 此时通常已经超时或被取消。
	//
	// held=false 表示锁被别人持有：此时 release 仍会被调用，但应是 no-op，
	// 且连接已经归还。
	TryLock(ctx context.Context, key int64) (release func(context.Context) error, held bool, err error)
}

// guardUnlockOutcome 把「pg_advisory_unlock 的结果」翻译成
// 「这条连接该怎么处置」。
//
//	（destroyConn, err)：
//	  - 无错且 unlocked=true  → 归还连接池，锁确实已释放。
//	  - 无错且 unlocked=false → 归还连接池，但状态异常（见 errGuardNotHeld）。
//	    此时服务端明确回答「这条连接上没有该锁」⇒ 归还不会泄漏。
//	  - 解锁出错             → **硬关连接**，不归还。
//
// ★ 为什么出错必须硬关：会话级锁绑在连接上，而 pgxpool 归还连接时
// 只会判断 closed/busy/in-tx/expired，**从不碰 advisory lock**
// （见 vendor/github.com/jackc/pgx/v5/pgxpool/conn.go 的 Release）。
// 归还一条仍持锁的连接 = 把锁连同连接一起借给下一个使用者，于是
// 该实例此后**永远**抢不到锁 —— 症状是「别的实例一直在写」，
// 与 §10.110.61 已经防住的「拿已取消的 ctx 去解锁」是同一个终局，
// 但那条路径没有告警、也没有守卫指向它。关掉连接让服务端结束会话，
// 锁随之由服务端释放。
//
// 抽成纯函数是为了让这张决策表可单测（pgxpool.Conn 没法伪造）。
func guardUnlockOutcome(unlockErr error, unlocked bool) (destroyConn bool, err error) {
	switch {
	case unlockErr != nil:
		return true, unlockErr
	case !unlocked:
		return false, errGuardNotHeld
	default:
		return false, nil
	}
}

// pgxWriterLocker 是基于 PG 会话级 advisory lock 的实现。
type pgxWriterLocker struct{ pool *pgxpool.Pool }

// NewPgWriterLocker returns the production locker backed by PG.
//
// pool 为 nil 时返回的 locker 取锁必然报错，调用方按 fail-open 处理。
func NewPgWriterLocker(pool *pgxpool.Pool) WriterLocker {
	return pgxWriterLocker{pool: pool}
}

func (l pgxWriterLocker) TryLock(ctx context.Context, key int64) (func(context.Context) error, bool, error) {
	if l.pool == nil {
		return func(context.Context) error { return nil }, false, errors.New("persist writer guard: nil pool")
	}

	// 专用连接：全程持有，锁与连接一一对应。
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return func(context.Context) error { return nil }, false, err
	}

	var held bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&held); err != nil {
		conn.Release()
		return func(context.Context) error { return nil }, false, err
	}

	if !held {
		// 别人在写：不持锁、不干活，连接立即归还。
		conn.Release()
		return func(context.Context) error { return nil }, false, nil
	}

	return func(relCtx context.Context) error {
		var unlocked bool
		unlockErr := conn.QueryRow(relCtx, "SELECT pg_advisory_unlock($1)", key).Scan(&unlocked)

		destroyConn, rerr := guardUnlockOutcome(unlockErr, unlocked)
		if destroyConn {
			// 会话级锁可能仍在这条连接上：硬关而不是归还，
			// 让服务端结束会话把锁带走（决策依据见 guardUnlockOutcome）。
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(relCtx), guardUnlockTimeout)
			defer cancel()
			_ = conn.Conn().Close(closeCtx)
			return rerr
		}

		conn.Release()
		return rerr
	}, true, nil
}

// RunCycleGuarded 跑一轮周期，但**先拿跨实例写入者锁**。
//
// 与 RunCycle 的关系：RunCycle 保持原样（无守卫），既有判据与
// 变异验证全部继续有效；本函数是生产入口。
//
// ── 三条刻意的设计选择 ──
//
//  1. **守卫罩住的是整轮**（Collect + Flush），不只是 Flush。
//     只锁 Flush 的话，三个进程仍会各自付 20 秒的 Collect 代价去抢 Redis，
//     而失败恰恰发生在 Redis 被挤慢之后 —— 实测丢的是 Flush，但成因在 Collect。
//
//  2. **取锁出错 ⇒ fail-open（照写），只记 warn。**
//     这是审计快照：**重复一行**的代价 << **漏掉一整轮 1,802 行**。
//     取锁失败时静默跳过，会把一次连接抖动放大成「审计数据永久停摆」。
//     ⇒ 方向选错时宁可多写，也不可不写。
//
//  3. **让位是正常状态，不是错误**：既不计入失败 streak，也不打 Warn。
//     打 Warn 会让「只有一个写入者」这个**预期行为**看起来像故障，
//     而这正是 §10.110.60 里 streak 型健康指标制造假阳性的那种形状。
func RunCycleGuarded(ctx context.Context, s CycleStages, locker WriterLocker, t CycleTimeouts) CycleOutcome {
	if locker == nil {
		// 没给守卫就退化成原行为，而不是拒绝写入。
		return RunCycle(ctx, s, t)
	}

	lockCtx, cancelLock := context.WithTimeout(ctx, guardLockTimeout)
	release, held, err := locker.TryLock(lockCtx, snapshotWriterLockKey)
	cancelLock()

	switch {
	case err != nil:
		slog.Warn("ursm.v2: persist writer guard unavailable, proceeding unguarded",
			"error", err, "key", snapshotWriterLockKey)
		out := RunCycle(ctx, s, t)
		out.GuardErr = err
		return out

	case !held:
		return CycleOutcome{YieldedToPeer: true}
	}

	// ctx 在这里多半已经因 Collect 耗尽 30s 而超时/取消，
	// 所以解锁路径必须另起一个与它无关的 ctx。
	defer func() {
		unlockCtx, cancelUnlock := context.WithTimeout(context.WithoutCancel(ctx), guardUnlockTimeout)
		defer cancelUnlock()
		if rerr := release(unlockCtx); rerr != nil {
			slog.Warn("ursm.v2: persist writer guard release failed",
				"error", rerr, "key", snapshotWriterLockKey)
		}
	}()

	out := RunCycle(ctx, s, t)
	out.GuardHeld = true
	return out
}

// GuardState 把本轮守卫状态渲染成日志字段。
func (o CycleOutcome) GuardState() string {
	switch {
	case o.GuardErr != nil:
		return "unavailable"
	case o.YieldedToPeer:
		return "yielded"
	case o.GuardHeld:
		return "held"
	default:
		return "off"
	}
}
