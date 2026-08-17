package nodestatecache

import "context"

// AuthorityRecord 权威快照中的单节点记录（URSM / Governor / fpslot 的
// 派生视图）。State 取本包状态枚举。
type AuthorityRecord struct {
	Ref   NodeRef
	Avail bool
	State uint8
	// 资源镜像（源 = dispatch Governor 与 credentialfpslot）。
	ConcUsed, ConcLimit uint16
	FPUsed, FPLimit     uint16
	RPMUsed, RPMLimit   uint16
}

// AuthoritativeSnapshot 权威快照注入接口（喂入路径③）：Reconcile 以其为
// 准全量校准。集成者从 URSM Redis / Governor / fpslot 聚合实现；本包不
// 发起任何 Redis 调用。
type AuthoritativeSnapshot interface {
	Snapshot(ctx context.Context) []AuthorityRecord
}

// ReconcileReport 对账结果摘要。
type ReconcileReport struct {
	Checked           int // 快照内记录数
	Registered        int // 新注册（此前缓存未知）的节点数
	CorrectedBitmap   int // Avail 位被权威值覆盖的节点数
	CorrectedState    int // State 被权威值覆盖的节点数
	CorrectedResource int // 资源镜像被权威值覆盖的节点数
}

// Reconcile 以注入的权威快照校正漂移（缓存值 ≠ 权威 → 覆盖）。
// 周期对账循环（默认 30s，可配）在 ticker 中调用本方法并传入
// Options.Authoritative；也可手动调用（单测/运维）。
//
// 注意：本方法只读权威、写缓存（覆盖式收敛），永不写权威（禁止双写）。
func (c *Cache) Reconcile(ctx context.Context, snapshot AuthoritativeSnapshot) ReconcileReport {
	var rep ReconcileReport
	if snapshot == nil {
		return rep
	}
	now := c.clock()
	for _, rec := range snapshot.Snapshot(ctx) {
		rep.Checked++
		id, isNew, err := c.reg.Register(rec.Ref)
		if err != nil {
			continue // 注册表满且无可回收 id：跳过该记录
		}
		if isNew {
			rep.Registered++
		}

		// 位图漂移校正：以权威 Avail 为准。
		if rec.Avail != c.bitmap.TestAvail(id) {
			if rec.Avail {
				c.bitmap.MarkAvailable(id)
			} else {
				c.bitmap.ClearAvail(id)
			}
			rep.CorrectedBitmap++
		}
		// 状态漂移校正。
		if rec.State != c.stats.get(id).State {
			c.stats.setState(id, rec.State)
			rep.CorrectedState++
		}
		// 资源镜像漂移校正（含上限注入）。
		cur := c.res.load(id)
		if cur.ConcUsed != rec.ConcUsed || cur.ConcLimit != rec.ConcLimit ||
			cur.FPUsed != rec.FPUsed || cur.FPLimit != rec.FPLimit ||
			cur.RPMUsed != rec.RPMUsed || cur.RPMLimit != rec.RPMLimit {
			want := NodeResourceSlot{
				ConcUsed: rec.ConcUsed, ConcLimit: rec.ConcLimit,
				FPUsed: rec.FPUsed, FPLimit: rec.FPLimit,
				RPMUsed: rec.RPMUsed, RPMLimit: rec.RPMLimit,
				WindowID: cur.WindowID,
				Flags:   cur.Flags, // 保留缓存侧满载标志，由 Override 内重算联动 Full 位图
			}
			c.res.Override(id, want, now)
			rep.CorrectedResource++
		}
	}
	return rep
}

// reconcileTick 是周期对账循环的一跳（由 New 启动的 ticker 驱动，
// Options.Authoritative 为空则空转）。
func (c *Cache) reconcileTick(ctx context.Context) ReconcileReport {
	if c.opts.Authority == nil {
		return ReconcileReport{}
	}
	return c.Reconcile(ctx, c.opts.Authority)
}
