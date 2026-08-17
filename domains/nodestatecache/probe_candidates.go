package nodestatecache

import "time"

// RecentSuccessLookup 36h 成功回看接口（R4.4 恢复扫描复用
// request_logs[_hot] 的查询，由集成者实现）。nil 视为条件满足。
type RecentSuccessLookup interface {
	HadSuccessWithin(ref NodeRef, window time.Duration, now time.Time) bool
}

// NeedProbe 返回需自检节点列表 = Probe 位图 ∩ 36h 成功命中 ∩ 退避到期。
// 三条件缺一不产出：
//
//	① Probe 位图置位（Updater 在 FailStreak 达阈值时置位，探测/恢复后清除）；
//	② 36h 内有成功记录（RecentSuccessLookup，nil 视为命中）；
//	③ 退避到期：LastUsedUnixSec + BackoffBase·2^(FailStreak-1)（封顶
//	  BackoffCap）<= now；从未使用（LastUsed=0）视为到期。
//
// 供 R4.4 恢复扫描器与 node_probe 消费；本方法只读，不清位图
// （探测结果经 Updater 写回）。
func (c *Cache) NeedProbe(now time.Time) []NodeRef {
	var out []NodeRef
	c.bitmap.foreachSet(planeProbe, func(id int32) {
		ref, ok := c.reg.RefOf(id)
		if !ok {
			return // 已被注册表回收的 id：不产出
		}
		slot := c.stats.get(id)
		if !c.backoffExpired(slot, now) {
			return
		}
		if c.recentSuccess != nil && !c.recentSuccess.HadSuccessWithin(ref, c.opts.RecentSuccessWindow, now) {
			return
		}
		out = append(out, ref)
	})
	return out
}

// backoffExpired 退避到期判定：基于 FailStreak 的指数退避（base·2^(streak-1)，
// 封顶 cap）。streak==0（无失败）或从未使用时立即到期。
func (c *Cache) backoffExpired(slot NodeStatsSlot, now time.Time) bool {
	if slot.LastUsedUnixSec == 0 || slot.FailStreak == 0 {
		return true
	}
	shift := uint(slot.FailStreak - 1)
	if shift > 10 {
		shift = 10 // 防溢出；BackoffCap 已封顶
	}
	backoff := c.opts.BackoffBase << shift
	if backoff > c.opts.BackoffCap || backoff <= 0 {
		backoff = c.opts.BackoffCap
	}
	return now.Unix() >= int64(slot.LastUsedUnixSec)+int64(backoff/time.Second)
}
