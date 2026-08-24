package streaming

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/kaixuan/llm-gateway-go/internal/liveactions"
)

// action_bridge.go — 会话优化 v4 R3.2（FR-3 操作事件思考帧桥接，T4）
//
// ActionBridge forwards request-scoped liveactions events (等待/连接上游/
// 重试/切换节点/切换模型/解析/脱敏/安全检查 …) to the CLIENT stream as
// thinking frames, through the connection registry (R1.6):
//
//   - 默认 SSE 注释帧 `: thinking: <json-string>`，与现有 handler.go 的
//     pre-stream thinking 注释格式逐字对齐（Zod 严格客户端安全，UT-SK-06）；
//   - reasoning_content / thinking 语义帧仅对运营白名单 client type 开启
//     （默认关；unknown 一律回退注释，UT-SK-07）；
//   - 运营整体开关 Enabled=false 时不发任何思考帧（UT-SK-06）；
//   - 旁路异步：有界 channel，满即丢弃并计数（不阻塞事件源）；
//   - 不污染最终 answer：桥只写注释帧（transport frames，不进语义
//     capture）或独立的 reasoning/thinking 语义帧，绝不写 content 增量
//     （UT-SK-08 帧分类断言）。
//
// liveactions 事件安全红线照旧：Detail 只含 id/标签/计数，本桥原样转发，
// 不添加正文或密钥。

// ActionSource is the subscription seam for request-scoped liveactions
// events. internal/liveactions.Emitter.Subscribe (2026-08-24, in-process
// fanout, drop-on-full) adapts onto ActionSource via EmitterActionSource
// (below); the Redis-backed source remains the cross-process option.
type ActionSource interface {
	// Events returns the subscription channel. The channel is closed when
	// the source shuts down.
	Events() <-chan liveactions.ActionEvent
}

// EmitterActionSource adapts *liveactions.Emitter onto ActionSource: each
// Events() call takes one bounded subscription (bufferSize <= 0 → 256;
// 满即丢，与 admin 直播集线器同款语义). The emitter's own Close reaps the
// subscription and closes the channel.
type EmitterActionSource struct {
	Emitter    *liveactions.Emitter
	BufferSize int
}

// Events implements ActionSource with one bounded subscription.
func (s EmitterActionSource) Events() <-chan liveactions.ActionEvent {
	if s.Emitter == nil {
		ch := make(chan liveactions.ActionEvent)
		close(ch)
		return ch
	}
	return s.Emitter.Subscribe(s.BufferSize)
}

// HotConfigSource is the runtime settings_kv read surface for the 运营
// 开关 (mirrors ursm/v2.HotConfigSource; *hotconfig.Config satisfies it).
type HotConfigSource interface {
	GetBool(key string, defaultValue bool) bool
	GetString(key string, defaultValue string) string
}

// ActionBridge 热配置键（settings_kv，30s 轮询生效）：
//
//	llmgw_action_bridge_enabled          bool    整体开关（默认 false 灰阶）
//	llmgw_action_bridge_semantic_clients string  语义帧 client type 白名单
//	                                             （逗号分隔；空 → 仅注释帧）
const (
	HotKeyActionBridgeEnabled         = "llmgw_action_bridge_enabled"
	HotKeyActionBridgeSemanticClients = "llmgw_action_bridge_semantic_clients"
)

// ChannelActionSource is the in-process liveactions tap: Emit fans an event
// out to every subscriber through bounded per-subscriber channels; a full
// subscriber buffer drops the event (never blocks the emit site).
type ChannelActionSource struct {
	mu       sync.RWMutex
	subs     []chan liveactions.ActionEvent
	closed   bool
	dropped  atomic.Uint64
	bufSize  int
	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewChannelActionSource builds the tap. bufSize <= 0 → 256.
func NewChannelActionSource(bufSize int) *ChannelActionSource {
	if bufSize <= 0 {
		bufSize = 256
	}
	return &ChannelActionSource{bufSize: bufSize, stopCh: make(chan struct{})}
}

// Emit publishes one event to all current subscribers (non-blocking, drop +
// count on full buffers).
func (s *ChannelActionSource) Emit(ev liveactions.ActionEvent) {
	if s == nil {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	for _, ch := range s.subs {
		select {
		case ch <- ev:
		default:
			s.dropped.Add(1)
		}
	}
}

// Events implements ActionSource: subscribes a new bounded channel.
func (s *ChannelActionSource) Events() <-chan liveactions.ActionEvent {
	ch := make(chan liveactions.ActionEvent, s.bufSize)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		close(ch)
		return ch
	}
	s.subs = append(s.subs, ch)
	go func(stop <-chan struct{}) {
		// Unsubscribe on stop so abandoned subscriptions are reaped.
		<-stop
		s.mu.Lock()
		for i, c := range s.subs {
			if c == ch {
				s.subs = append(s.subs[:i], s.subs[i+1:]...)
				close(ch)
				break
			}
		}
		s.mu.Unlock()
	}(s.stopCh)
	return ch
}

// Close stops the tap and closes all subscription channels. Idempotent.
func (s *ChannelActionSource) Close() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		subs := s.subs
		s.subs = nil
		s.mu.Unlock()
		close(s.stopCh)
		for _, ch := range subs {
			close(ch)
		}
	})
}

// DroppedTotal reports events dropped on full subscriber buffers.
func (s *ChannelActionSource) DroppedTotal() uint64 {
	if s == nil {
		return 0
	}
	return s.dropped.Load()
}

// ActionBridgeConfig bounds the FR-3 thinking-frame bridge.
type ActionBridgeConfig struct {
	// Enabled is the 运营整体开关. false (zero value) → the bridge never
	// emits any thinking frame (UT-SK-06).
	Enabled bool
	// Registry is the connection registry (R1.6); required.
	Registry *ConnectionRegistry
	// Source supplies liveactions events; required when Enabled.
	Source ActionSource
	// BufferSize bounds the internal event queue; <= 0 → 256. Full queue
	// drops + counts (旁路异步，满即丢).
	BufferSize int
	// SemanticFrameClientTypes is the 运营白名单 of client types allowed to
	// receive reasoning_content/thinking SEMANTIC frames instead of
	// comments. Empty (default) → comments only for everyone; clienttype
	// unknown is NEVER eligible (R3.2 注释兜底).
	SemanticFrameClientTypes []string
	// Hot (会话优化 v4 T4 运营开关) is the live settings_kv surface; nil →
	// the boot-time Enabled/SemanticFrameClientTypes values stay fixed.
	// Hot toggles take effect within one polling interval (30s) WITHOUT a
	// process restart: Enabled=false pauses the pump loop (events dropped,
	// counters keep running), Enabled=true resumes it. A bridge built with
	// boot Enabled=false but a Hot source can therefore be turned on later
	// at runtime — so NewActionBridge starts the pump whenever Registry is
	// wired, and the gate is evaluated per event.
	Hot HotConfigSource
}

// ActionBridge pumps liveactions events into client thinking frames.
// A nil *ActionBridge is a valid no-op for Submit/Close (wiring parity with
// liveactions.Emitter).
type ActionBridge struct {
	cfg      ActionBridgeConfig
	semantic map[string]struct{}

	events   chan liveactions.ActionEvent
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	sentTotal      atomic.Uint64
	commentTotal   atomic.Uint64
	semanticTotal  atomic.Uint64
	droppedTotal   atomic.Uint64
	writeErrTotal  atomic.Uint64
	notRoutedTotal atomic.Uint64
}

// NewActionBridge builds and starts the pump goroutine when enabled and
// fully wired; otherwise it returns an inert bridge (Submit is a no-op).
func NewActionBridge(cfg ActionBridgeConfig) *ActionBridge {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 256
	}
	b := &ActionBridge{
		cfg:      cfg,
		semantic: make(map[string]struct{}, len(cfg.SemanticFrameClientTypes)),
	}
	for _, ct := range cfg.SemanticFrameClientTypes {
		b.semantic[strings.ToLower(strings.TrimSpace(ct))] = struct{}{}
	}
	// Pump startup requires a registry (nowhere to route without one). The
	// boot-time Enabled value only matters when no Hot source is wired —
	// with Hot, the per-event gate in dispatch honors llmgw_action_bridge_enabled
	// live, so a bridge booted disabled can be flipped on without restart.
	if cfg.Registry == nil {
		return b
	}
	if !cfg.Enabled && cfg.Hot == nil {
		return b
	}
	b.events = make(chan liveactions.ActionEvent, cfg.BufferSize)
	b.stopCh = make(chan struct{})
	var srcCh <-chan liveactions.ActionEvent
	if cfg.Source != nil {
		srcCh = cfg.Source.Events()
	}
	b.wg.Add(1)
	go b.pump(srcCh)
	return b
}

// Submit enqueues one event (non-blocking). Nil-receiver and disabled-bridge
// safe; full queue → drop + count.
func (b *ActionBridge) Submit(ev liveactions.ActionEvent) {
	if b == nil || b.events == nil {
		return
	}
	select {
	case b.events <- ev:
	default:
		b.droppedTotal.Add(1)
	}
}

// Close stops the pump after draining the queue. Idempotent.
func (b *ActionBridge) Close() {
	if b == nil || b.events == nil {
		return
	}
	b.stopOnce.Do(func() { close(b.stopCh) })
	b.wg.Wait()
}

// pump consumes the configured ActionSource (src may be nil: Submit-only
// mode) plus the internal Submit queue until Close.
func (b *ActionBridge) pump(src <-chan liveactions.ActionEvent) {
	defer b.wg.Done()
	for {
		select {
		case <-b.stopCh:
			for {
				select {
				case ev := <-b.events:
					b.dispatch(ev)
				default:
					return
				}
			}
		case ev := <-src:
			b.dispatch(ev)
		case ev := <-b.events:
			b.dispatch(ev)
		}
	}
}

// enabledNow evaluates the 运营开关 for one event: hot settings_kv value
// when a Hot source is wired (defaulting to the boot value), else the boot
// value. Evaluated per event so a hot toggle lands within one poll.
func (b *ActionBridge) enabledNow() bool {
	if b.cfg.Hot == nil {
		return b.cfg.Enabled
	}
	return b.cfg.Hot.GetBool(HotKeyActionBridgeEnabled, b.cfg.Enabled)
}

// semanticWhitelistNow renders the live semantic-frame client whitelist.
// Hot key absent → boot whitelist copy (parsed identically to construction).
func (b *ActionBridge) semanticWhitelistNow() map[string]struct{} {
	if b.cfg.Hot == nil {
		return b.semantic
	}
	raw := strings.TrimSpace(b.cfg.Hot.GetString(HotKeyActionBridgeSemanticClients, ""))
	if raw == "" {
		return b.semantic
	}
	wl := make(map[string]struct{})
	for _, ct := range strings.Split(raw, ",") {
		if ct = strings.ToLower(strings.TrimSpace(ct)); ct != "" {
			wl[ct] = struct{}{}
		}
	}
	return wl
}

// dispatch filters, renders and writes one event. Errors are counted, never
// propagated (bypass channel).
func (b *ActionBridge) dispatch(ev liveactions.ActionEvent) {
	if !b.enabledNow() {
		// 运营开关关闭（UT-SK-06 的热更新形态）：事件按丢弃处理。
		b.droppedTotal.Add(1)
		return
	}
	// Request-scoped filter: node-dimension state_change events and events
	// without a request id have no client timeline to attach to.
	if ev.RequestID == "" || ev.Action == liveactions.ActionStateChange {
		return
	}
	snap, ok := b.cfg.Registry.Lookup(ev.RequestID)
	if !ok {
		// Not a live streaming client (non-stream request / already closed).
		b.notRoutedTotal.Add(1)
		return
	}
	frame, semantic := renderThinkingFrame(snap.Protocol, snap.ClientType, ev, b.semanticWhitelistNow())
	if frame == "" {
		return
	}
	if err := b.cfg.Registry.WriteFrame(ev.RequestID, frame); err != nil {
		b.writeErrTotal.Add(1)
		return
	}
	b.sentTotal.Add(1)
	if semantic {
		b.semanticTotal.Add(1)
	} else {
		b.commentTotal.Add(1)
	}
}

// renderThinkingFrame picks the frame form for one event:
//
//   - comment `: thinking: <json>` (default; unknown client types);
//   - semantic reasoning/thinking frame for whitelisted client types with a
//     known protocol.
//
// Returns "" when no frame should be emitted.
func renderThinkingFrame(protocolLabel, clientType string, ev liveactions.ActionEvent, semanticWhitelist map[string]struct{}) (frame string, semantic bool) {
	text := bridgeThinkingText(ev)
	if text == "" {
		return "", false
	}
	if _, ok := semanticWhitelist[clientType]; ok && clientType != "" {
		if f := renderSemanticThinkingFrame(parseProtocolLabel(protocolLabel), text); f != "" {
			return f, true
		}
	}
	// 注释兜底（UT-SK-06/07）：格式与 handler.go pre-stream thinking 注释
	// 逐字对齐 — `: thinking: <json-string>\n\n`，不出现任何 data: 行。
	escaped, err := json.Marshal(text)
	if err != nil {
		return "", false
	}
	return ": thinking: " + string(escaped) + "\n\n", false
}

// parseProtocolLabel reverses protocolMetricLabel onto ClientProtocol.
func parseProtocolLabel(label string) ClientProtocol {
	switch label {
	case "anthropic":
		return ProtocolAnthropic
	case "openai_responses":
		return ProtocolOpenAIResponses
	case "openai_chat":
		return ProtocolOpenAIChat
	default:
		return ProtocolOpenAIChat
	}
}

// renderSemanticThinkingFrame renders a protocol-native reasoning/thinking
// delta carrying the bridge text. These frames are semantic (FrameClass
// content-ish) and therefore gated behind the client-type whitelist.
func renderSemanticThinkingFrame(p ClientProtocol, text string) string {
	var b strings.Builder
	switch p {
	case ProtocolAnthropic:
		delta := map[string]string{"type": "thinking_delta", "thinking": text}
		payload, err := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": delta})
		if err != nil {
			return ""
		}
		b.WriteString("event: content_block_delta\ndata: ")
		b.Write(payload)
		b.WriteString("\n\n")
	case ProtocolOpenAIResponses:
		payload, err := json.Marshal(map[string]string{"type": "response.reasoning_text.delta", "delta": text})
		if err != nil {
			return ""
		}
		b.WriteString("event: response.reasoning_text.delta\ndata: ")
		b.Write(payload)
		b.WriteString("\n\n")
	default: // openai_chat
		payload, err := json.Marshal(map[string]any{
			"id":      "gw-thinking",
			"object":  "chat.completion.chunk",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"reasoning_content": text}, "finish_reason": ""}},
		})
		if err != nil {
			return ""
		}
		b.WriteString("data: ")
		b.Write(payload)
		b.WriteString("\n\n")
	}
	return b.String()
}

// bridgeThinkingText renders the human-readable one-line summary of an
// action event. Deterministic (detail keys sorted); no body content.
func bridgeThinkingText(ev liveactions.ActionEvent) string {
	var sb strings.Builder
	sb.WriteString(string(ev.Action))
	if ev.Model != "" {
		sb.WriteString(" model=")
		sb.WriteString(ev.Model)
	}
	if ev.Retry {
		sb.WriteString(" retry")
		if ev.RetrySeq > 0 {
			sb.WriteString("=")
			sb.WriteString(strconv.Itoa(ev.RetrySeq))
		}
	}
	if ev.ErrorKind != "" {
		sb.WriteString(" error_kind=")
		sb.WriteString(ev.ErrorKind)
	}
	if len(ev.Detail) > 0 {
		keys := make([]string, 0, len(ev.Detail))
		for k := range ev.Detail {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sb.WriteString(" ")
			sb.WriteString(k)
			sb.WriteString("=")
			sb.WriteString(ev.Detail[k])
		}
	}
	return sb.String()
}

// Counters expose the bridge observability (旁路指标).
func (b *ActionBridge) SentTotal() uint64 {
	if b == nil {
		return 0
	}
	return b.sentTotal.Load()
}

// CommentFramesTotal counts comment-form thinking frames sent.
func (b *ActionBridge) CommentFramesTotal() uint64 {
	if b == nil {
		return 0
	}
	return b.commentTotal.Load()
}

// SemanticFramesTotal counts whitelist-gated semantic thinking frames sent.
func (b *ActionBridge) SemanticFramesTotal() uint64 {
	if b == nil {
		return 0
	}
	return b.semanticTotal.Load()
}

// DroppedTotal counts events dropped on the full internal queue.
func (b *ActionBridge) DroppedTotal() uint64 {
	if b == nil {
		return 0
	}
	return b.droppedTotal.Load()
}

// WriteErrorsTotal counts registry write failures (client gone etc.).
func (b *ActionBridge) WriteErrorsTotal() uint64 {
	if b == nil {
		return 0
	}
	return b.writeErrTotal.Load()
}

// NotRoutedTotal counts events whose request had no live streaming client.
func (b *ActionBridge) NotRoutedTotal() uint64 {
	if b == nil {
		return 0
	}
	return b.notRoutedTotal.Load()
}
