package nodestatecache

// ErrKind 错误分类枚举（NodeUseRecord.Packed bit8-15 与 Updater 的 errKind）。
// 与 journey attempt_* 事件的 ErrorKind 字符串映射固定（record_test.go 钉死）。
const (
	ErrKindNone      uint8 = 0
	ErrKindNetwork   uint8 = 1 // journey "network_error"
	ErrKindTimeout   uint8 = 2 // journey "deadline_exceeded"
	ErrKindAuth      uint8 = 3 // journey "auth_error"
	ErrKindRateLimit uint8 = 4 // journey "rate_limit"（429）
	ErrKindOverflow  uint8 = 5 // journey "overflow"
	ErrKindUpstream  uint8 = 6 // journey "upstream_error"
	ErrKindClient    uint8 = 7 // journey "client_error"
	ErrKindCanceled  uint8 = 8 // journey "canceled"
	ErrKindUnknown   uint8 = 255
)

// UpdaterFunc 状态/统计更新闭包类型（R10.5，签名照抄规格）。
type UpdaterFunc func(nodeID int32, success bool, errKind uint8, latencyMS int32)

// Update 状态/统计更新（R10.5）：RecordRequest 效应链尾部与探测结果的
// 挂接点（executors/executor_nodehealth.go 效应链尾部旁路更新）。
//
// 行为：成功 → Succ1h+1、FailStreak 清零、State=available；若节点冷却中
// （Avail=0）则恢复 Avail、清 Probe。失败 → Fail1h+1、FailStreak 饱和递增；
// errKind=429 → RPM 镜像视为本分钟满载（Full 位图排除，分钟窗翻转后回归）；
// FailStreak 达阈值（默认 3，对齐 URSM FailStreakLimit）→ 清 Avail、置
// Probe（待自检）、State=probing。
//
// latencyMS 仅入参保留（精确延迟遥测在 URSM 窗口 ZSET，不在此层重复）。
func (c *Cache) Update(nodeID int32, success bool, errKind uint8, latencyMS int32) {
	slot, _ := c.stats.recordOutcome(nodeID, success, c.clock())
	if success {
		if !c.bitmap.TestAvail(nodeID) {
			c.bitmap.MarkAvailable(nodeID) // 冷却中恢复：置 Avail、清 Probe
		}
		return
	}
	if errKind == ErrKindRateLimit {
		c.res.ForceRateLimited(nodeID, c.clock())
	}
	if slot.FailStreak >= c.opts.FailStreakLimit {
		c.bitmap.MarkProbing(nodeID) // 清 Avail、置 Probe
		c.stats.setState(nodeID, StateProbing)
	}
}

// JourneyErrorKindName 返回 errKind 枚举对应的 requestjourney
// attempt_* 事件 ErrorKind 词表字符串（dispatch classifyError /
// requestjourney contract 的冻结词表）。映射固定，由 record_test.go 钉死。
func JourneyErrorKindName(errKind uint8) string {
	switch errKind {
	case ErrKindNone:
		return ""
	case ErrKindNetwork:
		return "network_error"
	case ErrKindTimeout:
		return "deadline_exceeded"
	case ErrKindAuth:
		return "auth_error"
	case ErrKindRateLimit:
		return "rate_limit"
	case ErrKindOverflow:
		return "overflow"
	case ErrKindUpstream:
		return "upstream_error"
	case ErrKindClient:
		return "client_error"
	case ErrKindCanceled:
		return "canceled"
	default:
		return "upstream_error"
	}
}
