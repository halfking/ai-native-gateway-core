package securitypattern

// 同步循环：定期把共享域里的安全事件取回来，聚合成本地模式快照。
//
// # 失败语义
//
// 取回失败**不覆盖**已有快照：共享域不可达时，本机继续用上一份快照
// （带 fetched_at），而不是把模式清空 —— "读不到"不等于"没有模式"。
// 失败会计数并退避。
//
// # 这是最终一致性
//
// 同步间隔就是延迟上界（默认 60s）。规划里也写了"跨实例同步可能有延迟
// （需最终一致性设计）"⇒ 本包就是这个设计：周期拉取 + 本地快照 +
// 快照带取回时间。没有做推送/订阅（memora 侧没有这个契约）。

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultInterval 是默认同步间隔。
const DefaultInterval = 60 * time.Second

// DefaultErrorBackoff 是连续失败后的退避间隔。
const DefaultErrorBackoff = 30 * time.Second

// Syncer 定期同步共享模式。
type Syncer struct {
	reader       Reader
	cache        *Cache
	interval     time.Duration
	errorBackoff time.Duration
	logger       *slog.Logger

	mu            sync.RWMutex
	pulls         uint64
	observations  uint64
	patterns      uint64
	errors        uint64
	lastSuccessAt time.Time
	lastError     string
	lastErrorAt   time.Time
}

// SyncerConfig 配置同步循环。
type SyncerConfig struct {
	// Reader 是读面。
	Reader Reader
	// Cache 是本地快照。
	Cache *Cache
	// Interval 是同步间隔。<=0 ⇒ DefaultInterval。
	Interval time.Duration
	// ErrorBackoff 是连续失败后的退避间隔。<=0 ⇒ DefaultErrorBackoff。
	ErrorBackoff time.Duration
	// Logger 是日志器。nil ⇒ slog.Default()。
	Logger *slog.Logger
}

// NewSyncer 构造同步循环。
func NewSyncer(cfg SyncerConfig) *Syncer {
	interval := cfg.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	backoff := cfg.ErrorBackoff
	if backoff <= 0 {
		backoff = DefaultErrorBackoff
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Syncer{
		reader:       cfg.Reader,
		cache:        cfg.Cache,
		interval:     interval,
		errorBackoff: backoff,
		logger:       logger,
	}
}

// Run 跑同步循环，直到 ctx 取消。
//
// 循环体：取回 → 聚合 → 替换快照。失败时按 errorBackoff 退避（连续失败
// 时退避会拉长，见 SyncOnce 的错误处理）。
func (s *Syncer) Run(ctx context.Context) {
	if s == nil || s.reader == nil || s.cache == nil {
		return
	}
	// 先立刻同步一次，别让第一个间隔空转。
	s.SyncOnce(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SyncOnce(ctx)
		}
	}
}

// SyncOnce 同步一次。
//
// 返回错误时**保留**旧快照（不覆盖），并记录错误与退避。
func (s *Syncer) SyncOnce(ctx context.Context) error {
	if s == nil || s.reader == nil || s.cache == nil {
		return nil
	}
	observations, err := s.reader.Fetch(ctx)
	if err != nil {
		s.recordError(err)
		return err
	}
	patterns := BuildPatterns(observations)
	s.cache.Replace(patterns, time.Now().UTC())

	s.mu.Lock()
	s.pulls++
	s.observations += uint64(len(observations))
	s.patterns = uint64(len(patterns))
	s.lastSuccessAt = time.Now().UTC()
	s.lastError = ""
	s.mu.Unlock()
	return nil
}

// recordError 记录一次失败。
func (s *Syncer) recordError(err error) {
	s.mu.Lock()
	s.errors++
	s.lastError = err.Error()
	s.lastErrorAt = time.Now().UTC()
	s.mu.Unlock()
	s.logger.Warn("security pattern sync failed (keeping the previous snapshot)",
		"error", err)
}

// Backoff 返回本次失败后应等待的时长。
//
// 连续失败越多等得越久（上限 10 分钟）—— 共享域长期不可达时不要反复
// 打满它。
func (s *Syncer) Backoff() time.Duration {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	errors := s.errors
	s.mu.RUnlock()
	if errors == 0 {
		return 0
	}
	wait := time.Duration(errors) * s.errorBackoff
	if wait > 10*time.Minute {
		wait = 10 * time.Minute
	}
	return wait
}

// SyncStats 是同步循环的运行计数。
type SyncStats struct {
	// Pulls 是成功取回次数。
	Pulls uint64 `json:"pulls"`
	// Observations 是累计取回的观察条数。
	Observations uint64 `json:"observations"`
	// Patterns 是当前快照里的模式数。
	Patterns uint64 `json:"patterns"`
	// Errors 是失败次数。
	Errors uint64 `json:"errors"`
	// LastSuccessAt 是最后一次成功取回的时间（零值 = 从未成功）。
	LastSuccessAt time.Time `json:"last_success_at"`
	// LastError 是最后一次错误的描述（空 = 没有错误）。
	LastError string `json:"last_error,omitempty"`
	// LastErrorAt 是最后一次错误的时间。
	LastErrorAt time.Time `json:"last_error_at"`
}

// Stats 返回当前计数。
func (s *Syncer) Stats() SyncStats {
	if s == nil {
		return SyncStats{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return SyncStats{
		Pulls:         s.pulls,
		Observations:  s.observations,
		Patterns:      s.patterns,
		Errors:        s.errors,
		LastSuccessAt: s.lastSuccessAt,
		LastError:     s.lastError,
		LastErrorAt:   s.lastErrorAt,
	}
}
