package feishubot

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// Deduper 告警去重器：在指定时间窗口内，相同指纹的告警会被合并。
//
// 实现：滑动窗口近似（仅保留最近一条命中时间）。
// 内存占用 O(最近触发数)，适合告警量 < 10K/min 的常规场景。
type Deduper struct {
	mu     sync.Mutex
	window time.Duration
	seen   map[string]time.Time
}

// NewDeduper 构造 Deduper。
//
// window <= 0 时退化为「不去重」（pass-through）。
func NewDeduper(window time.Duration) *Deduper {
	d := &Deduper{
		window: window,
		seen:   make(map[string]time.Time),
	}
	if window > 0 {
		go d.gcLoop()
	}
	return d
}

// Fingerprint 计算告警指纹。
//
// 算法：sha256(category|source|severity|key)
//   - category: 告警大类（prompt_injection / rate_limit / ...）
//   - source  : 来源模块/钩子名
//   - severity: 严重度
//   - key     : 业务唯一标识（如 "tenant=t1#model=gpt-4"）
func Fingerprint(category, source, severity, key string) string {
	h := sha256.New()
	h.Write([]byte(category))
	h.Write([]byte{0})
	h.Write([]byte(source))
	h.Write([]byte{0})
	h.Write([]byte(severity))
	h.Write([]byte{0})
	h.Write([]byte(key))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// Check 判断该指纹是否处于去重窗口内，并更新最近命中时间。
//
// 返回值：
//   - duplicate=true：应跳过推送（窗口内已命中）
//   - duplicate=false：可推送，记录本次命中
func (d *Deduper) Check(fp string) (duplicate bool, lastSeen time.Time) {
	if d.window <= 0 {
		return false, time.Time{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if t, ok := d.seen[fp]; ok {
		if now.Sub(t) < d.window {
			d.seen[fp] = now // 续期窗口
			return true, t
		}
	}
	d.seen[fp] = now
	return false, time.Time{}
}

// Reset 清空去重缓存（测试 / 配置变更时使用）。
func (d *Deduper) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = make(map[string]time.Time)
}

// Size 返回当前缓存条目数（监控 / 调试用）。
func (d *Deduper) Size() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}

// gcLoop 周期性清理过期条目，避免 map 无限增长。
func (d *Deduper) gcLoop() {
	t := time.NewTicker(d.window)
	defer t.Stop()
	for range t.C {
		d.mu.Lock()
		now := time.Now()
		for fp, ts := range d.seen {
			if now.Sub(ts) > d.window*3 {
				delete(d.seen, fp)
			}
		}
		d.mu.Unlock()
	}
}

// ── 速率限制器 ─────────────────────────────────────────────────────

// RateLimiter 滑动窗口速率限制器（按 60s 窗口）。
//
// 设计：保留每分钟命中数；超过阈值时返回 true（throttled）。
// 简洁实现：仅记录窗口开始时间与计数，不做精细滑动。
type RateLimiter struct {
	mu       sync.Mutex
	window   time.Duration
	max      int
	hits     int
	winStart time.Time
}

// NewRateLimiter 构造 RateLimiter。
//
// max <= 0 时退化为不限流。
func NewRateLimiter(maxPerMinute int) *RateLimiter {
	return &RateLimiter{
		window: 60 * time.Second,
		max:    maxPerMinute,
	}
}

// Allow 检查是否允许本次推送。
//
// 返回 true 表示被限流（应聚合为节流提示而非单独发送）。
func (r *RateLimiter) Allow() bool {
	if r.max <= 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if now.Sub(r.winStart) >= r.window {
		r.winStart = now
		r.hits = 0
	}
	r.hits++
	return r.hits > r.max
}

// Reset 重置窗口（配置变更 / 测试用）。
func (r *RateLimiter) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits = 0
	r.winStart = time.Now()
}

// Stats 返回当前窗口状态。
func (r *RateLimiter) Stats() (used int, max int, winStart time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits, r.max, r.winStart
}
