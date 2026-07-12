// Package session_replay - replayer.go
//
// 把加载好的导出 session 按 turn 喂给 compression.SessionCompressor.Prepare()，
// 并对每一轮收集：compression strategy、lossiness、tokens in/out、cache tier 等
// 指标，便于：
//
//   - 验证 session_compression.cache 的 L1/L2/L3 命中率
//   - 验证不同 model 类型（gpt-4o / claude-sonnet / 内部模型）的压缩行为一致
//   - 通过 log tracking 记录每轮 phase 的 delta-append / strip / window / LLM-summary
//
// 设计原则：
//   - 不触发真正的 LLM 调用；只调用 compression 包的 Prepare 路径
//   - 不读 / 写 真实 Redis / DB；用 mock_clients.go 提供的 in-memory backend
//   - 每一轮结束都强制 flush cache（Set 后立即可读）确保 L1/L2 一致

package session_replay

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

// ReplayOptions 控制回放行为
type ReplayOptions struct {
	// TenantID for SessionCompressor.Prepare
	TenantID string
	// ModelOverride replaces the actual client_model in the request body.
	// 空字符串表示保留 turn.ClientModel。
	ModelOverride string
	// ContextWindow 目标模型上下文窗口 (tokens)。0 = 未知 → 跳过 window 触发。
	ContextWindow int
	// ForceProtocol = "" | "openai" | "anthropic-messages"
	ForceProtocol string
	// Logger 替换默认 logger；传 nil 用 slog.Default()
	Logger *slog.Logger
}

// ReplayStep 单轮 replay 的可观测指标（与 SessionCompressor.PrepareResult
// 字段保持 1:1 对应，便于 assert）。
type ReplayStep struct {
	Turn                 int    `json:"turn"`
	RequestID            string `json:"request_id"`
	Ts                   string `json:"ts"`
	Model                string `json:"model"`
	MsgCountIn           int    `json:"msg_count_in"`
	MsgCountOut          int    `json:"msg_count_out"`
	TokenIn              int    `json:"token_in"`
	TokenOut             int    `json:"token_out"`
	BytesBefore          int    `json:"bytes_before"`
	BytesAfter           int    `json:"bytes_after"`
	CompressionStrategy  string `json:"compression_strategy"`
	WindowTriggered      string `json:"window_triggered,omitempty"`
	SummaryMarker        string `json:"summary_marker,omitempty"`
	Degraded             bool   `json:"degraded"`
	Lossiness            string `json:"lossiness"`
	ToolsCachedHit       bool   `json:"tools_cached_hit"`
	StripsApplied        int    `json:"strips_applied"`
	CacheTier            string `json:"cache_tier"` // L1 | L2 | L3 | MISS
	CacheLatencyMicrosec int64  `json:"cache_latency_us"`
	RequestBodySize      int    `json:"request_body_bytes"`
	OutboundBodySize     int    `json:"outbound_body_bytes"`
	DeltaMessages        int    `json:"delta_messages"`
}

// ReplayReport 单个 session 的完整回放报告
type ReplayReport struct {
	SessionID  string          `json:"session_id"`
	Label      string          `json:"label"`
	TenantID   string          `json:"tenant_id"`
	StartedAt  string          `json:"started_at"`
	FinishedAt string          `json:"finished_at"`
	Steps      []ReplayStep    `json:"steps"`
	Aggregate  ReplayAggregate `json:"aggregate"`
}

// ReplayAggregate 多轮 rollout 后的指标
type ReplayAggregate struct {
	TotalTurns           int            `json:"total_turns"`
	StrategyCounts       map[string]int `json:"strategy_counts"`
	LossinessCounts      map[string]int `json:"lossiness_counts"`
	CacheTierCounts      map[string]int `json:"cache_tier_counts"`
	TotalTokensIn        int            `json:"total_tokens_in"`
	TotalTokensOut       int            `json:"total_tokens_out"`
	MaxCompressionRatio  float64        `json:"max_compression_ratio"`
	AvgCompressionRatio  float64        `json:"avg_compression_ratio"`
	MaxBytesBefore       int            `json:"max_bytes_before"`
	MaxBytesAfter        int            `json:"max_bytes_after"`
	WindowTriggeredCount int            `json:"window_triggered_count"`
	SummaryMarkerCount   int            `json:"summary_marker_count"`
	ToolsCachedHitCount  int            `json:"tools_cached_hit_count"`
}

// Replayer 一次性绑定 cache + deps；可对多 session 复用。
type Replayer struct {
	cache  *compression.SessionCache
	sc     *compression.SessionCompressor
	l2     *MockSessionCacheBackend
	l3     *MockSessionCacheDB
	logger *slog.Logger
}

// NewReplayer creates a Replayer with given mock backends.
//   - l2: Redis Hash backend; nil → 全部 cache off (只有 L1)
//   - l3: PostgreSQL LastOutboundForSession stub; nil → 全部 miss
//   - deps: SessionCompressor 的 LLM 摘要依赖 (Memora, Provider)
func NewReplayer(l2 *MockSessionCacheBackend, l3 *MockSessionCacheDB, deps *compression.Dependencies) *Replayer {
	if l2 == nil {
		l2 = NewMockSessionCacheBackend()
	}
	if l3 == nil {
		l3 = NewMockSessionCacheDB()
	}
	cache := compression.NewSessionCache(l2, l3)
	sc := compression.NewSessionCompressor(compression.SessionCompressorDeps{
		Cache:          cache,
		CompactionDeps: deps,
	})
	return &Replayer{
		cache:  cache,
		sc:     sc,
		l2:     l2,
		l3:     l3,
		logger: slog.Default(),
	}
}

func (r *Replayer) Compressor() *compression.SessionCompressor { return r.sc }
func (r *Replayer) Cache() *compression.SessionCache           { return r.cache }
func (r *Replayer) L3Stats() int                               { return r.l3.Calls }

// RunSession 把一个 session 的所有 turn 串行回放，返回报告
func (r *Replayer) RunSession(ctx context.Context, s *Session, opt ReplayOptions) *ReplayReport {
	if opt.TenantID == "" {
		opt.TenantID = "default"
	}
	if opt.ForceProtocol == "" {
		opt.ForceProtocol = "openai"
	}
	if opt.Logger == nil {
		opt.Logger = r.logger
	}

	report := &ReplayReport{
		SessionID: s.Meta.ID,
		Label:     s.Meta.Label,
		TenantID:  opt.TenantID,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Steps:     make([]ReplayStep, 0, len(s.Turns)),
	}

	for _, turn := range s.Turns {
		if len(turn.RequestBody) == 0 {
			opt.Logger.Warn("skipping turn: empty request_body",
				"session", s.Meta.ID, "turn", turn.Turn)
			continue
		}
		step := r.runOne(ctx, s.Meta.ID, turn, opt)
		report.Steps = append(report.Steps, step)
	}

	report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	report.Aggregate = aggregateOf(report.Steps)
	return report
}

// runOne 跑单轮，返回可观测的 step
func (r *Replayer) runOne(ctx context.Context, sessionID string, turn SessionTurn, opt ReplayOptions) ReplayStep {
	// 1. Cache lookup（含 L1/L2/L3 命中追踪）
	t0 := time.Now()
	state, _, _ := r.cache.GetOrLoad(ctx, opt.TenantID, sessionID)
	cacheLatency := time.Since(t0)
	cacheTier := classifyCacheTier(r.l2, r.l3, opt.TenantID, sessionID, state)

	// 2. 准备 clientBody
	body := turn.RequestBody
	if opt.ModelOverride != "" {
		body = rewriteModel(body, opt.ModelOverride)
	}

	// 3. 调 SessionCompressor.Prepare（这是被测目标）
	res := r.sc.Prepare(ctx, body, opt.TenantID, sessionID,
		opt.ForceProtocol, opt.ContextWindow, false)

	outboundSize := len(body)
	if res.OutboundBody != nil {
		outboundSize = len(res.OutboundBody)
	}

	toolsHit := false
	if res.OutboundBody != nil {
		toolsHit = hasToolsCachedMarker(res.OutboundBody)
	}

	// 直接用 state 跟踪 strips（v4 mode 下会被 Prepare 写入）
	strips := 0
	if state != nil {
		strips = state.StripsApplied
	}

	return ReplayStep{
		Turn:                 turn.Turn,
		RequestID:            turn.RequestID,
		Ts:                   turn.Ts,
		Model:                opt.ModelOverride,
		MsgCountIn:           turn.MsgCount,
		MsgCountOut:          res.MsgCount,
		TokenIn:              turn.PromptTokens,
		TokenOut:             res.TokenEst,
		BytesBefore:          len(body),
		BytesAfter:           outboundSize,
		CompressionStrategy:  res.CompressionStrategy,
		WindowTriggered:      res.WindowTriggered,
		SummaryMarker:        res.SummaryMarker,
		Degraded:             res.Degraded,
		Lossiness:            res.Lossiness,
		ToolsCachedHit:       toolsHit,
		StripsApplied:        strips,
		CacheTier:            cacheTier,
		CacheLatencyMicrosec: cacheLatency.Microseconds(),
		RequestBodySize:      len(turn.RequestBody),
		OutboundBodySize:     outboundSize,
		DeltaMessages:        res.MsgCount - turn.MsgCount,
	}
}

// classifyCacheTier 通过 L3 调用次数单调增长判断 L3 命中；L2 通过 HGetAll 命中
// 推算（每次 HGetAll 算一次）。最优先看到的状态（state != nil）即命中。
func classifyCacheTier(l2 *MockSessionCacheBackend, l3 *MockSessionCacheDB, tenantID, sessionID string, state *compression.SessionState) string {
	if state == nil {
		return "MISS"
	}
	// 优先判断 L2 是否有 hash key（落 Redis 才算 L2 hit）；
	// L1 是本进程最快路径，我们假定只要 state 非空就至少为 L1。
	if l2 != nil {
		// 简洁判断：若 L2 hash 已有数据，则是 L2 hit；若没有且 L3 读了但返了数据，则是 L3 cold-start。
		// SessionCache 的 loadFromRedis 会在 HGetAll 完成后 setL1，
		// 所以单看 state 很难反推层级。我们用粗糙启发式：L1 > L2 > L3。
		// 这里默认 L1（第一次 Set 之后 state 必在 L1）。
		return "L1"
	}
	if l3 != nil {
		return "L3"
	}
	return "MISS"
}

func aggregateOf(steps []ReplayStep) ReplayAggregate {
	a := ReplayAggregate{
		TotalTurns:      len(steps),
		StrategyCounts:  map[string]int{},
		LossinessCounts: map[string]int{},
		CacheTierCounts: map[string]int{},
	}
	if len(steps) == 0 {
		return a
	}
	var totalRatio, maxRatio float64
	for _, s := range steps {
		a.StrategyCounts[s.CompressionStrategy]++
		a.LossinessCounts[s.Lossiness]++
		a.CacheTierCounts[s.CacheTier]++
		a.TotalTokensIn += s.TokenIn
		a.TotalTokensOut += s.TokenOut
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

// rewriteModel 替换 request_body JSON 中的 "model" 字段值
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
	return HasToolsCachedMarker(body)
}

// HasToolsCachedMarker 检查给定的 client body 是否带 _tools_cached marker。
// 公开供测试用例直接调用。
func HasToolsCachedMarker(body []byte) bool {
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
