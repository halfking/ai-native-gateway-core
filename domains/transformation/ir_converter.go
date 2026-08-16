package transformation

import (
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/internal/ir"
	"github.com/kaixuan/llm-gateway-go/internal/irconv"
	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
)

// IRConverterAdapter is the dependency-free conversion contract shared with
// callers. It is an alias so provider-scoped return types remain exact across
// packages.
type IRConverterAdapter = irconv.Converter

// ErrConverterCircuitOpen is returned when the transport converter circuit
// is open due to repeated conversion failures. Callers should treat this as
// a signal to fall back to legacy conversion.
var ErrConverterCircuitOpen = errors.New("transport: converter circuit open")

// TransportIRConverter bridges the transport package into the real request
// pipeline by implementing streaming.IRConverter (via structural typing).
//
// It wraps an inner converter (typically irAdapter → internal/ir) and adds:
//   - ExtensionsBag Extract (during Parse) / Restore (during Serialize) for
//     lossless cross-protocol round-trip of non-standard fields.
//   - CircuitBreaker for fast-fail on persistent conversion errors.
//
// Wiring (cmd/gateway/main.go):
//
//	if os.Getenv("LLM_GATEWAY_TRANSPORT_IR") == "true" {
//	    routingExec.IR = transport.NewTransportIRConverter(&irAdapter{})
//	}
//
// Safety: when LLM_GATEWAY_TRANSPORT_IR is unset, routingExec.IR stays as the
// plain irAdapter — zero behavior change, no production risk.
type TransportIRConverter struct {
	inner     IRConverterAdapter
	extractor *IRExtensionExtractor
	restorer  *IRExtensionRestorer

	// cbs is a sync.Map[int]*StreamCircuitBreaker keyed by providerID.
	// Lazy-initialized on first access via breakerFor(providerID).
	// Added 2026-08-09 to replace the single process-wide cb field.
	cbs sync.Map

	// defaultCB is used when providerID is 0 (unknown/unscoped).
	// Provides backward compatibility for call sites that haven't been
	// migrated to WithProviderScope yet.
	defaultCB *StreamCircuitBreaker

	contextMu sync.RWMutex
	context   *domain.TransportContext // optional; used for catalog-aware restoration
}

// NewTransportIRConverter creates a converter that wraps inner with
// ExtensionsBag round-trip and circuit-breaker protection.
func NewTransportIRConverter(inner IRConverterAdapter) *TransportIRConverter {
	return &TransportIRConverter{
		inner:     inner,
		extractor: NewIRExtensionExtractor(),
		restorer:  NewIRExtensionRestorer(),
		defaultCB: NewStreamCircuitBreaker(),
	}
}

// SetCircuitBreaker replaces the circuit breaker for a specific providerID
// (testing/injection). Pass providerID=0 to set the default breaker.
func (c *TransportIRConverter) SetCircuitBreaker(providerID int, cb *StreamCircuitBreaker) {
	if cb == nil {
		return
	}
	if providerID == 0 {
		c.defaultCB = cb
	} else {
		c.cbs.Store(providerID, cb)
	}
}

// WithProviderScope returns a scoped converter that binds a specific providerID
// to this converter for the duration of the request. All Parse/Serialize calls
// on the returned scopedConverter will use the circuit breaker for that provider.
//
// Usage:
//
//	scoped := e.IR.WithProviderScope(cand.ProviderID)
//	req, err := scoped.ParseOpenAI(body)
//
// The returned scopedConverter is lightweight (holds only a providerID int and
// a pointer to the parent TransportIRConverter) and should not be reused across
// requests for different providers.
//
// Added 2026-08-09 to enable per-provider circuit breaker isolation.
func (c *TransportIRConverter) WithProviderScope(providerID int) irconv.Converter {
	return &scopedConverter{
		parent:     c,
		providerID: providerID,
	}
}

// breakerFor returns the circuit breaker for the given providerID, creating it
// lazily on first access. Returns defaultCB when providerID is 0.
func (c *TransportIRConverter) breakerFor(providerID int) *StreamCircuitBreaker {
	if providerID == 0 {
		return c.defaultCB
	}
	if v, ok := c.cbs.Load(providerID); ok {
		return v.(*StreamCircuitBreaker)
	}
	cb := NewStreamCircuitBreaker()
	actual, _ := c.cbs.LoadOrStore(providerID, cb)
	return actual.(*StreamCircuitBreaker)
}

// SetContext injects TransportContext for catalog-aware extension restoration.
func (c *TransportIRConverter) SetContext(ctx *domain.TransportContext) {
	c.contextMu.Lock()
	c.context = cloneTransportContext(ctx)
	c.contextMu.Unlock()
}

func cloneTransportContext(ctx *domain.TransportContext) *domain.TransportContext {
	if ctx == nil {
		return nil
	}
	copy := *ctx
	return &copy
}

func (c *TransportIRConverter) contextSnapshot() *domain.TransportContext {
	c.contextMu.RLock()
	defer c.contextMu.RUnlock()
	return cloneTransportContext(c.context)
}

func (c *TransportIRConverter) circuitCheck() error {
	if c.defaultCB != nil && c.defaultCB.ShouldFallback() {
		return ErrConverterCircuitOpen
	}
	return nil
}

func (c *TransportIRConverter) recordErr() {
	if c.defaultCB != nil {
		c.defaultCB.RecordError()
	}
}

func (c *TransportIRConverter) recordOK() {
	if c.defaultCB != nil {
		c.defaultCB.RecordSuccess()
	}
}

// scopedConverter is a lightweight wrapper that binds a providerID to a
// TransportIRConverter for the duration of a request. All Parse/Serialize
// calls on this wrapper use the per-provider circuit breaker.
//
// Created via TransportIRConverter.WithProviderScope(providerID).
// Added 2026-08-09 for per-provider circuit breaker isolation.
type scopedConverter struct {
	parent     *TransportIRConverter
	providerID int
	context    *domain.TransportContext
}

// SetContext binds request metadata to this scope without mutating the parent.
func (s *scopedConverter) SetContext(ctx *domain.TransportContext) {
	s.context = cloneTransportContext(ctx)
}

func (s *scopedConverter) circuitCheck() error {
	cb := s.parent.breakerFor(s.providerID)
	if cb != nil && cb.ShouldFallback() {
		return ErrConverterCircuitOpen
	}
	return nil
}

func (s *scopedConverter) recordErr() {
	if cb := s.parent.breakerFor(s.providerID); cb != nil {
		cb.RecordError()
	}
}

func (s *scopedConverter) recordOK() {
	if cb := s.parent.breakerFor(s.providerID); cb != nil {
		cb.RecordSuccess()
	}
}

func (s *scopedConverter) ParseOpenAI(body []byte) (*ir.InternalRequest, error) {
	if err := s.circuitCheck(); err != nil {
		return nil, err
	}
	req, err := s.parent.inner.ParseOpenAI(body)
	if err != nil {
		s.recordErr()
		return nil, err
	}
	s.parent.extractRequestExtensions(body, req)
	s.recordOK()
	return req, nil
}

func (s *scopedConverter) ParseAnthropic(body []byte) (*ir.InternalRequest, error) {
	if err := s.circuitCheck(); err != nil {
		return nil, err
	}
	req, err := s.parent.inner.ParseAnthropic(body)
	if err != nil {
		s.recordErr()
		return nil, err
	}
	s.parent.extractRequestExtensions(body, req)
	s.recordOK()
	return req, nil
}

func (s *scopedConverter) ParseResponses(body []byte) (*ir.InternalRequest, error) {
	if err := s.circuitCheck(); err != nil {
		return nil, err
	}
	req, err := s.parent.inner.ParseResponses(body)
	if err != nil {
		s.recordErr()
		return nil, err
	}
	s.parent.extractRequestExtensions(body, req)
	s.recordOK()
	return req, nil
}

func (s *scopedConverter) SerializeOpenAI(req *ir.InternalRequest) ([]byte, error) {
	if err := s.circuitCheck(); err != nil {
		return nil, err
	}
	body, err := s.parent.inner.SerializeOpenAI(req)
	if err != nil {
		s.recordErr()
		return nil, err
	}
	body = s.parent.restoreRequestExtensionsWithContext(body, req, ir.ProtocolOpenAIChat, s.context)
	s.recordOK()
	return body, nil
}

func (s *scopedConverter) SerializeAnthropic(req *ir.InternalRequest) ([]byte, error) {
	if err := s.circuitCheck(); err != nil {
		return nil, err
	}
	body, err := s.parent.inner.SerializeAnthropic(req)
	if err != nil {
		s.recordErr()
		return nil, err
	}
	body = s.parent.restoreRequestExtensionsWithContext(body, req, ir.ProtocolAnthropicMessages, s.context)
	s.recordOK()
	return body, nil
}

func (s *scopedConverter) ParseOpenAIResponse(body []byte) (*ir.InternalResponse, error) {
	if err := s.circuitCheck(); err != nil {
		return nil, err
	}
	resp, err := s.parent.inner.ParseOpenAIResponse(body)
	if err != nil {
		s.recordErr()
		return nil, err
	}
	s.parent.extractResponseExtensions(body, resp)
	s.recordOK()
	return resp, nil
}

func (s *scopedConverter) ParseAnthropicResponse(body []byte) (*ir.InternalResponse, error) {
	if err := s.circuitCheck(); err != nil {
		return nil, err
	}
	resp, err := s.parent.inner.ParseAnthropicResponse(body)
	if err != nil {
		s.recordErr()
		return nil, err
	}
	s.parent.extractResponseExtensions(body, resp)
	s.recordOK()
	return resp, nil
}

func (s *scopedConverter) SerializeOpenAIResponse(r *ir.InternalResponse, clientModel string) ([]byte, error) {
	if err := s.circuitCheck(); err != nil {
		return nil, ErrConverterCircuitOpen
	}
	out, err := s.parent.inner.SerializeOpenAIResponse(r, clientModel)
	if err != nil {
		s.recordErr()
		return nil, err
	}
	out = s.parent.restoreExtensions(out, r.Extensions)
	s.recordOK()
	return out, nil
}

func (s *scopedConverter) SerializeAnthropicResponse(r *ir.InternalResponse, clientModel string) ([]byte, error) {
	if err := s.circuitCheck(); err != nil {
		return nil, ErrConverterCircuitOpen
	}
	out, err := s.parent.inner.SerializeAnthropicResponse(r, clientModel)
	if err != nil {
		s.recordErr()
		return nil, err
	}
	out = s.parent.restoreExtensions(out, r.Extensions)
	s.recordOK()
	return out, nil
}

func (s *scopedConverter) SerializeResponses(chunk *ir.StreamChunk, itemID string) string {
	if err := s.circuitCheck(); err != nil {
		return ""
	}
	out := s.parent.inner.SerializeResponses(chunk, itemID)
	s.recordOK()
	return out
}

func (s *scopedConverter) SerializeResponsesResponse(r *ir.InternalResponse, clientModel string) ([]byte, error) {
	if err := s.circuitCheck(); err != nil {
		return nil, ErrConverterCircuitOpen
	}
	out, err := s.parent.inner.SerializeResponsesResponse(r, clientModel)
	if err != nil {
		s.recordErr()
		return nil, err
	}
	out = s.parent.restoreExtensions(out, r.Extensions)
	s.recordOK()
	return out, nil
}

func (s *scopedConverter) WithProviderScope(providerID int) irconv.Converter {
	return &scopedConverter{
		parent:     s.parent,
		providerID: providerID,
		context:    cloneTransportContext(s.context),
	}
}

// ─── Request direction: Parse (Extract extensions) ───

// ParseOpenAI parses an OpenAI request body and extracts non-standard
// fields into req.Extensions for lossless round-trip.
func (c *TransportIRConverter) ParseOpenAI(body []byte) (*ir.InternalRequest, error) {
	if err := c.circuitCheck(); err != nil {
		return nil, err
	}
	req, err := c.inner.ParseOpenAI(body)
	if err != nil {
		c.recordErr()
		return nil, err
	}
	c.extractRequestExtensions(body, req)
	return req, nil
}

// ParseAnthropic parses an Anthropic request body and extracts non-standard
// fields into req.Extensions for lossless round-trip.
func (c *TransportIRConverter) ParseAnthropic(body []byte) (*ir.InternalRequest, error) {
	if err := c.circuitCheck(); err != nil {
		return nil, err
	}
	req, err := c.inner.ParseAnthropic(body)
	if err != nil {
		c.recordErr()
		return nil, err
	}
	c.extractRequestExtensions(body, req)
	return req, nil
}

// ParseResponses parses an OpenAI Responses API request body and extracts
// non-standard fields into req.Extensions for lossless round-trip.
//
// Spec §7.1 IR main-path extension (2026-08-02): the Responses API input
// direction, mirroring ParseOpenAI/ParseAnthropic.
func (c *TransportIRConverter) ParseResponses(body []byte) (*ir.InternalRequest, error) {
	if err := c.circuitCheck(); err != nil {
		return nil, err
	}
	req, err := c.inner.ParseResponses(body)
	if err != nil {
		c.recordErr()
		return nil, err
	}
	c.extractRequestExtensions(body, req)
	return req, nil
}

// extractRequestExtensions populates req.Extensions with non-standard
// top-level fields from the original body.
func (c *TransportIRConverter) extractRequestExtensions(body []byte, req *ir.InternalRequest) {
	bag, extErr := c.extractor.Extract(body, nil)
	if extErr != nil {
		slog.Debug("transport: extract request extensions failed (non-fatal)", "err", extErr)
		return
	}
	if len(bag.ClientRaw) == 0 {
		return
	}
	if req.Extensions == nil {
		req.Extensions = make(map[string]json.RawMessage, len(bag.ClientRaw))
	}
	for k, v := range bag.ClientRaw {
		req.Extensions[k] = v
	}
}

// ─── Request direction: Serialize (Restore extensions) ───

// SerializeOpenAI serializes an IR request to OpenAI format and restores
// non-standard fields from req.Extensions.
func (c *TransportIRConverter) SerializeOpenAI(req *ir.InternalRequest) ([]byte, error) {
	if err := c.circuitCheck(); err != nil {
		return nil, err
	}
	out, err := c.inner.SerializeOpenAI(req)
	if err != nil {
		c.recordErr()
		return nil, err
	}
	out = c.restoreRequestExtensions(out, req, ir.ProtocolOpenAIChat)
	c.recordOK()
	return out, nil
}

// SerializeAnthropic serializes an IR request to Anthropic format and restores
// non-standard fields from req.Extensions.
func (c *TransportIRConverter) SerializeAnthropic(req *ir.InternalRequest) ([]byte, error) {
	if err := c.circuitCheck(); err != nil {
		return nil, err
	}
	out, err := c.inner.SerializeAnthropic(req)
	if err != nil {
		c.recordErr()
		return nil, err
	}
	out = c.restoreRequestExtensions(out, req, ir.ProtocolAnthropicMessages)
	c.recordOK()
	return out, nil
}

// restoreExtensions merges ext into the serialized body via IRExtensionRestorer,
// which only writes keys absent from the target (never overwrites standard fields).
func (c *TransportIRConverter) restoreExtensions(body []byte, ext map[string]json.RawMessage) []byte {
	if len(ext) == 0 {
		return body
	}
	bag := &domain.ExtensionsBag{ClientRaw: ext}
	restored, err := c.restorer.Restore(body, bag)
	if err != nil {
		slog.Debug("transport: restore extensions failed (non-fatal)", "err", err)
		return body
	}
	return restored
}

// restoreRequestExtensions 把 IR 序列化器可能未覆盖的扩展字段补齐进 body。
func (c *TransportIRConverter) restoreRequestExtensions(body []byte, req *ir.InternalRequest, targetProtocol string) []byte {
	return c.restoreRequestExtensionsWithContext(body, req, targetProtocol, c.contextSnapshot())
}

func (c *TransportIRConverter) restoreRequestExtensionsWithContext(body []byte, req *ir.InternalRequest, targetProtocol string, ctx *domain.TransportContext) []byte {
	if req == nil {
		return body
	}

	// 2026-08-11（P2 修复）：移除两道门禁。
	//
	// 原实现：
	//   1. SourceProtocol != targetProtocol      → 整包丢弃
	//   2. ClientCatalogCode != UpstreamCatalogCode → 整包丢弃
	//
	// 门禁 1 与 internal/ir 序列化器里的同类门禁重复。门禁 2 更严重 ——
	// 网关的存在意义就是跨 catalog 转发，所以它等价于"几乎永不还原"。
	// 两者叠加使得 Claude Code → DeepSeek 这条主链路的厂商私有参数 100% 丢失。
	//
	// 门禁的原始动机（docs/IR格式优化/06-Provider-Profile审计与收敛.md 第 5 节：
	// 避免把 A 厂商私有字段泄漏给 B 厂商上游）是正确的，手段是错的 ——
	// 正确做法是按**字段**分类判定而非整包丢弃。该职责现由 internal/paramreg
	// 承担：ir.Serialize* 内部已逐字段决策，未知字段透传、方言私有字段裁剪并
	// 上报 loss、目标硬拒绝的字段一律剔除。
	//
	// 底层 IRExtensionRestorer 只写目标不存在的键，不会覆盖 IR 已生成的标准字段。
	if !paramregEnabled() {
		// 回退路径：PARAMREG_ENABLED=false 时恢复旧的双门禁行为。
		if req.SourceProtocol != "" && req.SourceProtocol != targetProtocol {
			return body
		}
		if ctx != nil && ctx.ClientCatalogCode != "" && ctx.UpstreamCatalogCode != "" &&
			ctx.ClientCatalogCode != ctx.UpstreamCatalogCode {
			return body
		}
		return c.restoreExtensions(body, req.Extensions)
	}

	// paramreg 启用路径：transport 层的第二次还原也走注册表决策。
	//
	// 注意：internal/ir 的 serialize_* 已经对 req.Extensions 做了一遍 paramreg
	// 过滤。transport 层的这次还原处理的是 ExtensionsBag.ClientRaw（由
	// IRExtensionExtractor 从 body 里提取），它可能包含不同的字段子集。
	// 为保持一致，这里也用 paramreg.Apply 逐字段决策，不用旧的 naive restorer。
	srcDialect := paramreg.DialectForProtocol(req.SourceProtocol)
	dstDialect := paramreg.Resolve(req.TargetProvider, targetProtocol)
	if ctx != nil && dstDialect == paramreg.DialectUnknown {
		dstDialect = paramreg.DialectForCatalogCode(ctx.UpstreamCatalogCode)
		if dstDialect == paramreg.DialectUnknown {
			dstDialect = paramreg.DialectForProtocol(targetProtocol)
		}
	}

	return c.restoreExtensionsWithParamreg(body, req.Extensions, srcDialect, dstDialect)
}

// restoreExtensionsWithParamreg 按注册表策略逐字段决策并还原 Extensions 进 body。
//
// 与 internal/ir 的 restoreExtensions 逻辑一致；此函数在 transport 层独立实现，
// 避免 transport → internal/ir 的反向依赖（ir 需要 import paramreg）。
func (c *TransportIRConverter) restoreExtensionsWithParamreg(
	body []byte,
	extensions map[string]json.RawMessage,
	src, dst paramreg.Dialect,
) []byte {
	if len(extensions) == 0 {
		return body
	}

	var target map[string]json.RawMessage
	if err := json.Unmarshal(body, &target); err != nil {
		return body
	}
	if target == nil {
		target = make(map[string]json.RawMessage)
	}

	merged := false
	for key, val := range extensions {
		if _, exists := target[key]; exists {
			continue // IR 已输出该键，不覆盖
		}

		outKey, outVal, action, _ := paramreg.Apply(key, val, src, dst)
		switch action {
		case paramreg.ActionRestore, paramreg.ActionTranslate:
			if outKey == "" {
				continue
			}
			if _, exists := target[outKey]; exists {
				continue
			}
			target[outKey] = outVal
			merged = true
			// ActionDrop / ActionSkip：不写入
		}
	}

	if !merged {
		return body
	}

	out, err := json.Marshal(target)
	if err != nil {
		return body
	}
	return out
}

// ─── Response direction (Phase D) ───

func (c *TransportIRConverter) ParseOpenAIResponse(body []byte) (*ir.InternalResponse, error) {
	if err := c.circuitCheck(); err != nil {
		return nil, err
	}
	resp, err := c.inner.ParseOpenAIResponse(body)
	if err != nil {
		c.recordErr()
		return nil, err
	}
	c.extractResponseExtensions(body, resp)
	return resp, nil
}

func (c *TransportIRConverter) ParseAnthropicResponse(body []byte) (*ir.InternalResponse, error) {
	if err := c.circuitCheck(); err != nil {
		return nil, err
	}
	resp, err := c.inner.ParseAnthropicResponse(body)
	if err != nil {
		c.recordErr()
		return nil, err
	}
	c.extractResponseExtensions(body, resp)
	return resp, nil
}

func (c *TransportIRConverter) extractResponseExtensions(body []byte, resp *ir.InternalResponse) {
	bag, extErr := c.extractor.Extract(body, nil)
	if extErr != nil {
		slog.Debug("transport: extract response extensions failed (non-fatal)", "err", extErr)
		return
	}
	if len(bag.ClientRaw) == 0 {
		return
	}
	if resp.Extensions == nil {
		resp.Extensions = make(map[string]json.RawMessage, len(bag.ClientRaw))
	}
	for k, v := range bag.ClientRaw {
		resp.Extensions[k] = v
	}
}

func (c *TransportIRConverter) SerializeOpenAIResponse(r *ir.InternalResponse, clientModel string) ([]byte, error) {
	if err := c.circuitCheck(); err != nil {
		return nil, err
	}
	out, err := c.inner.SerializeOpenAIResponse(r, clientModel)
	if err != nil {
		c.recordErr()
		return nil, err
	}
	out = c.restoreExtensions(out, r.Extensions)
	c.recordOK()
	return out, nil
}

func (c *TransportIRConverter) SerializeAnthropicResponse(r *ir.InternalResponse, clientModel string) ([]byte, error) {
	if err := c.circuitCheck(); err != nil {
		return nil, err
	}
	out, err := c.inner.SerializeAnthropicResponse(r, clientModel)
	if err != nil {
		c.recordErr()
		return nil, err
	}
	out = c.restoreExtensions(out, r.Extensions)
	c.recordOK()
	return out, nil
}

// ─── Responses API direction (Phase E, 2026-07-01) ───
//
// No ParseResponses* method is added because no upstream speaks Responses
// API yet (gateway→gateway Responses→Responses is a future Phase). Only
// the Serialize direction is needed: it produces the wire payload the
// client receives when ClientProtocol == "openai-responses".
//
// Both methods restore non-standard Extensions fields on the output body,
// mirroring the existing OpenAI/Anthropic response serializers.

func (c *TransportIRConverter) SerializeResponses(chunk *ir.StreamChunk, itemID string) string {
	if err := c.circuitCheck(); err != nil {
		return ""
	}
	// Stream direction: extensions are not restored here (SSE line-level
	// round-trip would be lossy and the Responses API stream shape is
	// owned by the IR serializer). The bridge / orchestrator may still
	// surface extensions via a final response.completed event if needed.
	out := c.inner.SerializeResponses(chunk, itemID)
	c.recordOK()
	return out
}

func (c *TransportIRConverter) SerializeResponsesResponse(r *ir.InternalResponse, clientModel string) ([]byte, error) {
	if err := c.circuitCheck(); err != nil {
		return nil, ErrConverterCircuitOpen
	}
	out, err := c.inner.SerializeResponsesResponse(r, clientModel)
	if err != nil {
		c.recordErr()
		return nil, err
	}
	out = c.restoreExtensions(out, r.Extensions)
	c.recordOK()
	return out, nil
}
