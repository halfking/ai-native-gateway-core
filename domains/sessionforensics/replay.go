package sessionforensics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

// ReplayOptions 控制回放参数。
//
//	TenantID    — 多租户场景下指定 tenant；空 → "default"
//	ContextWindow — 给 SessionCompressor 的目标模型 context window（0 = 未知）
//	ForceProtocol — "openai" / "anthropic-messages"
//	ModelOverride — 覆盖每轮消息中携带的 model（验证模型替换的一致性）
//	Logger      — 自定义日志 handler；nil → slog.Default()
type ReplayOptions struct {
	TenantID        string
	ContextWindow   int
	ForceProtocol   string
	ModelOverride   string
	Logger          *slog.Logger
	ResetPerSession bool
}

func (o ReplayOptions) validate() ReplayOptions {
	if o.TenantID == "" {
		o.TenantID = "default"
	}
	if o.ForceProtocol == "" {
		o.ForceProtocol = "openai"
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// Replayer 把 *SessionPack 喂给本地的 SessionCompressor。
//
// 默认使用 in-memory L1/L2/L3 缓存（不连真实 Redis/DB）。你可以用
// NewReplayerWithBackends 注入更真实的 backend。
type Replayer struct {
	cache  *compression.SessionCache
	sc     *compression.SessionCompressor
	logger *slog.Logger
}

// NewReplayer 默认用 in-memory mock backend 创建 Replayer（兼容 tests/session_replay）。
func NewReplayer() *Replayer {
	return NewReplayerWithBackends(newTestL2Backend(), newTestL3DB(), nil)
}

// NewReplayerWithBackends 让你显式注入压缩器的依赖：
//
//   - l2: compression.SessionCacheBackend  (Redis Hash 抽象)
//   - l3: compression.SessionCacheDB       (PostgreSQL LastOutboundForSession 抽象)
//   - deps: compression.Dependencies       (Memora + Provider client for LLM summary)
//
// l2 或 l3 传 nil → 只走 L1。deps 传 nil → 不触发 LLM summary。
func NewReplayerWithBackends(
	l2 compression.SessionCacheBackend,
	l3 compression.SessionCacheDB,
	deps *compression.Dependencies,
) *Replayer {
	cache := compression.NewSessionCache(l2, l3)
	sc := compression.NewSessionCompressor(compression.SessionCompressorDeps{
		Cache:          cache,
		CompactionDeps: deps,
	})
	return &Replayer{cache: cache, sc: sc, logger: slog.Default()}
}

// ReplaySingle 个轮回放（便于在 CI 中做白盒断言）。
func (r *Replayer) ReplaySingle(ctx context.Context, pack *SessionPack, turn int, opt ReplayOptions) (ReplayStep, error) {
	opt = opt.validate()
	if pack == nil || turn < 1 || turn > len(pack.Messages) {
		return ReplayStep{}, fmt.Errorf("sessionforensics: invalid turn=%d / turns=%d",
			turn, len(pack.Messages))
	}
	msg := pack.Messages[turn-1]
	body := json.RawMessage(msg.Content)
	if len(body) == 0 {
		return ReplayStep{}, fmt.Errorf("sessionforensics: turn %d has empty content", turn)
	}
	if opt.ModelOverride != "" {
		body = rewriteModel(body, opt.ModelOverride)
	}
	t0 := time.Now()
	state, _, _ := r.cache.GetOrLoad(ctx, opt.TenantID, pack.SessionMeta.ID)
	cacheLat := time.Since(t0)
	tier := "MISS"
	if state != nil {
		tier = "L1"
	}
	res := r.sc.Prepare(ctx, []byte(body), opt.TenantID, pack.SessionMeta.ID,
		opt.ForceProtocol, opt.ContextWindow, false)
	out := len(body)
	if res.OutboundBody != nil {
		out = len(res.OutboundBody)
	}
	step := ReplayStep{
		Turn:                 turn,
		CompressionStrategy:  res.CompressionStrategy,
		WindowTriggered:      res.WindowTriggered,
		SummaryMarker:        res.SummaryMarker,
		Degraded:             res.Degraded,
		Lossiness:            res.Lossiness,
		CacheTier:            tier,
		CacheLatencyMicrosec: cacheLat.Microseconds(),
		BytesBefore:          len(body),
		BytesAfter:           out,
		OutboundBodySize:     out,
	}
	return step, nil
}

// Replay 把整个 pack 按 turn 顺序回放，产出 *ReplayReport。
func (r *Replayer) Replay(ctx context.Context, pack *SessionPack, opt ReplayOptions) *ReplayReport {
	opt = opt.validate()
	rep := &ReplayReport{
		SessionID: pack.SessionMeta.ID,
		Label:     pack.SessionMeta.Label(),
		TenantID:  opt.TenantID,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Steps:     make([]ReplayStep, 0, len(pack.Messages)),
	}
	for i, msg := range pack.Messages {
		if len(msg.Content) == 0 {
			continue
		}
		body := json.RawMessage(msg.Content)
		if opt.ModelOverride != "" {
			body = rewriteModel(body, opt.ModelOverride)
		}
		t0 := time.Now()
		state, _, _ := r.cache.GetOrLoad(ctx, opt.TenantID, pack.SessionMeta.ID)
		cacheLat := time.Since(t0)
		tier := "MISS"
		if state != nil {
			tier = "L1"
		}
		res := r.sc.Prepare(ctx, []byte(body), opt.TenantID, pack.SessionMeta.ID,
			opt.ForceProtocol, opt.ContextWindow, false)
		out := len(body)
		if res.OutboundBody != nil {
			out = len(res.OutboundBody)
		}
		rep.Steps = append(rep.Steps, ReplayStep{
			Turn:                 i + 1,
			MsgCountIn:           countMessagesInBody(body),
			MsgCountOut:          res.MsgCount,
			CompressionStrategy:  res.CompressionStrategy,
			WindowTriggered:      res.WindowTriggered,
			SummaryMarker:        res.SummaryMarker,
			Degraded:             res.Degraded,
			Lossiness:            res.Lossiness,
			CacheTier:            tier,
			CacheLatencyMicrosec: cacheLat.Microseconds(),
			BytesBefore:          len(body),
			BytesAfter:           out,
			OutboundBodySize:     out,
			ToolsCachedHit:       hasToolsCachedMarker(body),
		})
	}
	rep.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	rep.Aggregate = aggregateOf(rep.Steps)
	return rep
}

// SessionMeta.Label 是一个便捷方法（无 -> 用 ID 兜底）。
func (m SessionMeta) Label() string { return "session " + m.ID }

func countMessagesInBody(body []byte) int {
	var raw struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0
	}
	return len(raw.Messages)
}

func rewriteModel(body []byte, newModel string) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	b, _ := json.Marshal(newModel)
	raw["model"] = b
	out, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	return out
}

func hasToolsCachedMarker(body []byte) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	v, ok := raw["_tools_cached"]
	if !ok {
		return false
	}
	return string(v) == "true"
}

func aggregateOf(steps []ReplayStep) ReplayAggregate {
	a := ReplayAggregate{
		TotalTurns:      len(steps),
		StrategyCounts:  map[string]int{},
		LossinessCounts: map[string]int{},
		CacheTierCounts: map[string]int{},
		ReplayedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	}
	if len(steps) == 0 {
		return a
	}
	var totalRatio, maxRatio float64
	for _, s := range steps {
		a.StrategyCounts[s.CompressionStrategy]++
		a.LossinessCounts[s.Lossiness]++
		a.CacheTierCounts[s.CacheTier]++
		if s.BytesBefore > 0 && s.BytesAfter > 0 {
			ratio := float64(s.BytesBefore-s.BytesAfter) / float64(s.BytesBefore)
			if ratio > maxRatio {
				maxRatio = ratio
			}
			totalRatio += ratio
		}
		if s.BytesBefore > a.MaxBytesBefore {
			a.MaxBytesBefore = s.BytesBefore
		}
		if s.BytesAfter > a.MaxBytesAfter {
			a.MaxBytesAfter = s.BytesAfter
		}
		if s.WindowTriggered != "" {
			a.WindowTriggeredCount++
		}
		if s.SummaryMarker != "" {
			a.SummaryMarkerCount++
		}
		if s.ToolsCachedHit {
			a.ToolsCachedHitCount++
		}
	}
	a.MaxCompressionRatio = round2(maxRatio)
	a.AvgCompressionRatio = round2(totalRatio / float64(len(steps)))
	return a
}

func round2(f float64) float64 {
	return float64(int64(f*100)) / 100
}
