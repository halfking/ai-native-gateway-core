package reqprobe

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Coordinator 是 executor / admin 与 Store 之间的门面：
//   - Record() 异步入队（有界，满则丢弃），热路径零阻塞；
//   - LearnedParams() 带 60s TTL 的进程内缓存，避免每个请求都打 Store；
//   - 学习开关 LLM_GATEWAY_REQPROBE_LEARN=off 时 LearnedParams 恒空
//     （探测重试仍工作，只是不做前置剔除）。
type Coordinator struct {
	store        Store
	ch           chan Record
	done         chan struct{}
	learnEnabled bool

	mu      sync.Mutex
	learned map[string]learnedEntry // key: providerCode|model → 参数集 + 过期时间

	// R51 审计 P3：缓存到期瞬间并发请求会各自同步回源 Store（Redis
	// HGETALL）——singleflight 把同一 key 的并发回源合并为一次。
	learnSF singleflight.Group
}

type learnedEntry struct {
	params []string
	expiry time.Time
}

// learnedCacheTTL 是学习规则的进程内缓存时长（只是缓存 TTL，规则本身的
// 有效性窗口是 learnedTTL，由 Store 判定）。
const learnedCacheTTL = 60 * time.Second

const recordQueueSize = 1024

// NewCoordinator 创建协调器并启动单个落库 worker。store 为 nil 时所有
// 方法安全降级为 no-op。
func NewCoordinator(store Store) *Coordinator {
	learn := true
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_REQPROBE_LEARN")); v != "" {
		learn = v != "off" && v != "0" && v != "false"
	}
	c := &Coordinator{
		store:        store,
		ch:           make(chan Record, recordQueueSize),
		done:         make(chan struct{}),
		learnEnabled: learn,
		learned:      map[string]learnedEntry{},
	}
	if store != nil {
		go c.worker()
	}
	return c
}

// Store 返回底层存储（admin handler 直用；可能为 nil）。
func (c *Coordinator) Store() Store { return c.store }

func (c *Coordinator) worker() {
	defer close(c.done)
	ctx := context.Background()
	for rec := range c.ch {
		if _, err := c.store.Upsert(ctx, rec); err != nil {
			slog.Warn("reqprobe upsert failed", "fingerprint", rec.Fingerprint, "error", err)
		}
	}
}

// Record 异步记录一条探测结果（Occurrences 视为 1；Recovered=true 时
// RecoveredCount 记 1）。满队列直接丢弃——观测数据不值得反压请求路径。
func (c *Coordinator) Record(rec Record) {
	if c == nil || c.store == nil {
		return
	}
	now := time.Now()
	rec.FirstSeen = now
	rec.LastSeen = now
	rec.Day = Today(now)
	rec.Occurrences = 1
	if rec.RecoveredCount > 1 {
		rec.RecoveredCount = 1
	}
	rec.ErrorSample = truncateSample(rec.ErrorSample)
	rec.Fingerprint = Fingerprint(&rec)
	select {
	case c.ch <- rec:
	default:
		slog.Debug("reqprobe record queue full, dropping", "fingerprint", rec.Fingerprint)
	}
}

// RecordTerminal 是 executor 终态 4xx 的便捷入口。
// recovered 表示"已做剔除/切换尝试且重试成功"；未尝试或尝试失败的终端
// 错误传 recovered=false，并把最终上游错误体放进 errorSample。
func (c *Coordinator) RecordTerminal(in Input, d Diagnosis, meta TerminalMeta, recovered bool) {
	if d.Trigger == "" {
		d.Trigger = TriggerUpstreamError
	}
	c.Record(Record{
		ProviderID:     meta.ProviderID,
		ProviderCode:   meta.ProviderCode,
		ClientModel:    meta.ClientModel,
		OutboundModel:  meta.OutboundModel,
		Protocol:       in.Protocol,
		Trigger:        d.Trigger,
		Param:          d.Param,
		SuggestMode:    d.SuggestMode,
		HTTPStatus:     in.HTTPStatus,
		ErrorKind:      in.ErrorKind,
		ErrorSample:    string(in.ErrorBody),
		LastRequestID:  meta.RequestID,
		RecoveredCount: boolToInt(recovered),
	})
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TerminalMeta 是 RecordTerminal 的请求侧元数据（不含正文）。
type TerminalMeta struct {
	RequestID     string
	ProviderID    int
	ProviderCode  string
	ClientModel   string
	OutboundModel string
}

// LearnedParams 返回该 (provider, model) 可前置剔除的参数（带 60s 缓存）。
// 存储异常时返回 nil（fail-open：不剔除，回到逐请求探测）。
func (c *Coordinator) LearnedParams(ctx context.Context, providerCode, outboundModel string) []string {
	if c == nil || c.store == nil || !c.learnEnabled {
		return nil
	}
	key := strings.ToLower(providerCode) + "|" + strings.ToLower(outboundModel)
	now := time.Now()
	c.mu.Lock()
	if entry, ok := c.learned[key]; ok && now.Before(entry.expiry) {
		c.mu.Unlock()
		return entry.params
	}
	c.mu.Unlock()

	// singleflight 合并同一 key 的并发回源（错过缓存的请求只有一个真正
	// 打 Store，其余共享结果）；ctx 透传调用方的，执行者被取消时共享方
	// 拿到空结果走 fail-open（不剔除），与回源失败语义一致。
	v, _, _ := c.learnSF.Do(key, func() (any, error) {
		params := c.store.LearnedParams(ctx, providerCode, outboundModel)
		c.mu.Lock()
		c.learned[key] = learnedEntry{params: params, expiry: time.Now().Add(learnedCacheTTL)}
		c.mu.Unlock()
		return params, nil
	})
	params, _ := v.([]string)
	return params
}

// InvalidateLearned 清掉学习缓存（解决记录后由 admin resolve 调用，让
// 前置剔除立即停止）。
func (c *Coordinator) InvalidateLearned() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.learned = map[string]learnedEntry{}
	c.mu.Unlock()
}

// Close 停止 worker（排空队列后退出）。生产常驻进程不调用。
func (c *Coordinator) Close() {
	if c == nil || c.store == nil {
		return
	}
	close(c.ch)
	<-c.done
	_ = c.store.Close()
}
