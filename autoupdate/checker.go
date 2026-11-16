package autoupdate

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// AlertSink 是「有新版本」的告警出口。
//
// 与提醒行（upgrade_logs）不同，告警是给**运维中心**看的：它能被列出、
// 被确认（acknowledge）、被消解（resolve）、被抑制（suppress）。
// 提醒行是给**升级流程**看的：它是安装任务的日志行。
// 两者同源生成、不同面消费。
type AlertSink interface {
	// RaiseUpdateAvailableAlert 发布一条「有新版」告警。
	// 返回 (告警 ID, 是否新建, error)。
	RaiseUpdateAvailableAlert(ctx context.Context, instanceID, version, notes, severity string) (int64, bool, error)
}

// Checker 周期性地向远端发布源检索最新版本，并把结论落到本地：
//
//  1. 发现有新版 ⇒ 写入本地 releases 表（并置为已发布），
//     于是既有的读面（/api/system/upgrade/status）立刻能看到「有更新」。
//  2. 发现有新版 ⇒ 在 upgrade_logs 留一条 status=pending 的行，
//     这条行就是「提醒」本身：它同时是提醒记录、也是安装任务的日志行。
//  3. 发现有新版 ⇒（若配置了告警出口）向运维告警面发布一条「有新版」告警。
//
// 为什么复用 upgrade_logs 而不是新开一张通知表：提醒与安装日志在语义上是
// 同一件事的两个阶段（待处理 → 执行中 → 成功/失败），分成两张表会立刻出现
// 「提醒已读但没有对应任务」或「任务在跑但提醒被重复生成」的对不齐。
type Checker struct {
	store      Store
	source     RemoteSource
	version    VersionProvider
	channel    Channel
	interval   time.Duration
	instanceID string
	alertSink  AlertSink
	// throttle 是「复用最近结论」的时间窗（供无鉴权的公开检查端点用）。
	throttle time.Duration

	mu      sync.RWMutex
	last    *CheckResult
	lastErr string
	lastAt  time.Time
	running bool
	nowFn   func() time.Time
}

// CheckResult 是一次检索的结论。
type CheckResult struct {
	CurrentVersion  string         `json:"current_version"`
	CurrentBuildSeq int            `json:"current_build_seq"`
	Latest          *RemoteRelease `json:"latest,omitempty"`
	UpToDate        bool           `json:"up_to_date"`
	CheckedAt       time.Time      `json:"checked_at"`
	// Notified=true 表示本次检索为该版本生成了提醒行。
	Notified bool `json:"notified"`
	// Error 非空表示**检索本身失败**（远端不可达 / 未配置源）⇒ 此时 Latest 不可信，
	// 界面必须显示「状态未知」，绝不能显示「已是最新」。
	Error string `json:"error,omitempty"`
	// Warning 非空表示**检索成功但收尾有瑕疵**（例如本地落库失败）。
	// 它不改变「有没有新版」的结论，只提示后续读面可能看不到这条版本。
	Warning string `json:"warning,omitempty"`
}

// CheckerOption 用于覆盖默认行为。
type CheckerOption func(*Checker)

// WithCheckInterval 设置轮询间隔。
func WithCheckInterval(d time.Duration) CheckerOption {
	return func(c *Checker) { c.interval = d }
}

// WithInstanceID 设置实例标识（提醒行的 instance_id）。
func WithInstanceID(id string) CheckerOption {
	return func(c *Checker) { c.instanceID = id }
}

// WithCheckerClock 覆盖时钟（测试用）。
func WithCheckerClock(now func() time.Time) CheckerOption {
	return func(c *Checker) { c.nowFn = now }
}

// WithAlertSink 注入告警出口（运维中心的「有新版」告警）。
func WithAlertSink(sink AlertSink) CheckerOption {
	return func(c *Checker) { c.alertSink = sink }
}

// WithCheckThrottle 设置「复用最近结论」的时间窗。
func WithCheckThrottle(d time.Duration) CheckerOption {
	return func(c *Checker) { c.throttle = d }
}

// NewChecker 创建版本检查器。
func NewChecker(store Store, source RemoteSource, version VersionProvider, channel Channel, opts ...CheckerOption) *Checker {
	if channel == "" {
		channel = ChannelStable
	}
	c := &Checker{
		store:    store,
		source:   source,
		version:  version,
		channel:  channel,
		interval: 6 * time.Hour,
		throttle: 60 * time.Second,
		nowFn:    time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// Interval 返回当前轮询间隔。
func (c *Checker) Interval() time.Duration { return c.interval }

// LastResult 返回最近一次检索结论（可能是 nil）。
func (c *Checker) LastResult() *CheckResult {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.last
}

// CheckThrottled 执行一次检索，但会复用 throttle 时间窗内的最近结论。
//
// ★ 用途：**无鉴权的公开检查端点**（/api/system/upgrade/check）。
//
//	那个端点一旦真的出网，就等于给任意调用者一个「每次调用都打一次远端」的
//	外呼放大器。复用最近结论既压住了外呼频率，也不影响语义
//	（版本结论在 60 秒内本来就不会变）。
//
// 管理员面（/api/admin/releases/check）用 CheckOnce —— 它需要「立刻真查一次」。
func (c *Checker) CheckThrottled(ctx context.Context) *CheckResult {
	if cached := c.cachedResult(); cached != nil {
		return cached
	}
	return c.CheckOnce(ctx)
}

// cachedResult 返回 throttle 窗口内的最近结论（过期则返回 nil）。
func (c *Checker) cachedResult() *CheckResult {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.last == nil || c.throttle <= 0 {
		return nil
	}
	if c.nowFn().Sub(c.last.CheckedAt) >= c.throttle {
		return nil
	}
	clone := *c.last
	return &clone
}

// CheckOnce 执行一次检索。返回的 CheckResult 永远非 nil（错误进 Error 字段），
// 这样调用方不需要区分「出错」和「无结果」两种 nil 语义。
func (c *Checker) CheckOnce(ctx context.Context) *CheckResult {
	result := &CheckResult{CheckedAt: c.nowFn()}

	currentVersion, currentSeq := "dev", 0
	if c.version != nil {
		currentVersion, currentSeq = c.version()
	}
	if currentVersion == "" {
		currentVersion = "dev"
	}
	result.CurrentVersion = currentVersion
	result.CurrentBuildSeq = currentSeq

	if c.source == nil {
		result.Error = "remote source is not configured"
		c.record(result)
		return result
	}

	rel, err := c.source.Latest(ctx, currentVersion, currentSeq, c.channel)
	if err != nil {
		result.Error = err.Error()
		c.record(result)
		return result
	}
	if rel == nil {
		result.UpToDate = true
		c.record(result)
		return result
	}

	result.Latest = rel
	result.UpToDate = false

	// 落库：让既有读面（customer API / admin 列表）能看见这个版本。
	//
	// ★ 落库失败**不是检索失败**：检索结论仍然成立（Latest 有值），
	//   所以只记 Warning，不记 Error —— 否则界面会把「本地没写进去」
	//   读成「连远端都没查成」，从而把「有更新」这一事实也一起抹掉。
	if err := c.ensureRelease(ctx, rel); err != nil {
		slog.Warn("autoupdate: persist discovered release failed",
			"version", rel.Version, "error", err)
		result.Warning = "persist release: " + err.Error()
	}

	// 提醒：每个版本最多生成一次。
	notified, err := c.ensureReminder(ctx, rel, currentVersion)
	if err != nil {
		slog.Warn("autoupdate: create reminder failed", "version", rel.Version, "error", err)
	} else {
		result.Notified = notified
	}

	// 告警：把同一结论送到运维告警面（可 ack / resolve / suppress）。
	if c.alertSink != nil {
		severity := "info"
		if rel.Mandatory {
			severity = "warning"
		}
		if _, _, err := c.alertSink.RaiseUpdateAvailableAlert(ctx, c.instanceID, rel.Version, rel.ReleaseNotes, severity); err != nil {
			slog.Warn("autoupdate: raise update alert failed", "version", rel.Version, "error", err)
		}
	}

	c.record(result)
	return result
}

// record 记住最近一次结论。
func (c *Checker) record(result *CheckResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = result
	c.lastAt = result.CheckedAt
	c.lastErr = result.Error
}

// ensureRelease 把远端发现的版本写入本地 releases 表并标记为已发布。
func (c *Checker) ensureRelease(ctx context.Context, rel *RemoteRelease) error {
	if c.store == nil {
		return nil
	}
	existing, err := c.store.GetRelease(ctx, rel.Version)
	if err == nil && existing != nil {
		if existing.PublishedAt != nil {
			return nil
		}
		return c.store.UpdateReleaseStatus(ctx, existing.ID, true)
	}

	release := &Release{
		Version:     rel.Version,
		BuildSeq:    rel.BuildSeq,
		Channel:     rel.Channel,
		Title:       fmt.Sprintf("%s", rel.Version),
		Description: rel.ReleaseNotes,
		Changelog:   rel.ReleaseNotes,
		ImageTag:    rel.Version,
		ImageDigest: rel.BinarySHA256,
		Mandatory:   rel.Mandatory,
		CreatedBy:   "auto-checker",
	}
	if err := c.store.CreateRelease(ctx, release); err != nil {
		// 并发/历史写入导致的唯一键冲突不算失败：回读一次补发布标记。
		if again, gerr := c.store.GetRelease(ctx, rel.Version); gerr == nil && again != nil {
			if again.PublishedAt != nil {
				return nil
			}
			return c.store.UpdateReleaseStatus(ctx, again.ID, true)
		}
		return err
	}
	return c.store.UpdateReleaseStatus(ctx, release.ID, true)
}

// ensureReminder 为新版本生成提醒行。同一个版本上**已有未完成的尝试**时不再重复生成，
// 避免轮询把提醒列表刷成噪音。
//
// ★ 判据是「有没有未完成的尝试」，而不是「有没有任何一行」：
//
//	该版本上一轮装成功（status=success）之后，若运维又**手工回滚**到更旧的版本，
//	远端会再次报「有新版」⇒ 此时**必须重新提醒**（否则节点就永远停在旧版上、
//	再也没人知道该升一级）。所以 success 不抑制新提醒。
func (c *Checker) ensureReminder(ctx context.Context, rel *RemoteRelease, currentVersion string) (bool, error) {
	if c.store == nil {
		return false, nil
	}
	history, _, err := c.store.GetUpgradeHistory(ctx, c.instanceID, 0, 200)
	if err != nil {
		return false, err
	}
	for _, h := range history {
		if h.Version == rel.Version && h.Status != StatusSuccess {
			// 该版本上还有一次未完成的尝试（待处理 / 失败 / 已回滚 / 进行中）⇒ 不重复提醒。
			return false, nil
		}
	}
	if _, err := c.store.CreateUpgradeLog(ctx, c.instanceID, currentVersion, rel.Version); err != nil {
		return false, err
	}
	return true, nil
}

// Run 启动周期检索循环，直到 ctx 取消。
//
// 首次检索在 interval/10 之后发起（避免与进程启动抢资源），随后按 interval 周期执行。
// 单次检索失败不会中断循环，也不会让进程退出 —— 检索是「锦上添花」的能力，
// 它坏了不应该拖垮网关。
func (c *Checker) Run(ctx context.Context) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	}()

	interval := c.interval
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	first := interval / 10
	if first <= 0 || first > 30*time.Second {
		first = 30 * time.Second
	}

	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			result := c.CheckOnce(ctx)
			switch {
			case result.Error != "":
				slog.Warn("autoupdate: version check failed", "error", result.Error)
			case result.UpToDate:
				slog.Info("autoupdate: up to date", "version", result.CurrentVersion)
			default:
				slog.Info("autoupdate: update available",
					"current", result.CurrentVersion,
					"latest", result.Latest.Version,
					"build_seq", result.Latest.BuildSeq,
					"notified", result.Notified)
			}
			timer.Reset(interval)
		}
	}
}
