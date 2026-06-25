package transformation

import (
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// IRConverterAdapter mirrors routing.IRConverter's method set without
// importing routing (avoids transport→routing dependency). The production
// irAdapter (cmd/gateway/main.go) satisfies this via Go structural typing.
//
// Method signatures must stay identical to routing.IRConverter so that
// TransportIRConverter also satisfies routing.IRConverter (structural).
type IRConverterAdapter interface {
	ParseOpenAI(body []byte) (*ir.InternalRequest, error)
	ParseAnthropic(body []byte) (*ir.InternalRequest, error)
	SerializeOpenAI(req *ir.InternalRequest) ([]byte, error)
	SerializeAnthropic(req *ir.InternalRequest) ([]byte, error)
	ParseAnthropicResponse(body []byte) (*ir.InternalResponse, error)
	ParseOpenAIResponse(body []byte) (*ir.InternalResponse, error)
	SerializeOpenAIResponse(ir *ir.InternalResponse, clientModel string) ([]byte, error)
	SerializeAnthropicResponse(ir *ir.InternalResponse, clientModel string) ([]byte, error)
}

// ErrConverterCircuitOpen is returned when the transport converter circuit
// is open due to repeated conversion failures. Callers should treat this as
// a signal to fall back to legacy conversion.
var ErrConverterCircuitOpen = errors.New("transport: converter circuit open")

// TransportIRConverter bridges the transport package into the real request
// pipeline by implementing routing.IRConverter (via structural typing).
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
	cb        *StreamCircuitBreaker
}

// NewTransportIRConverter creates a converter that wraps inner with
// ExtensionsBag round-trip and circuit-breaker protection.
func NewTransportIRConverter(inner IRConverterAdapter) *TransportIRConverter {
	return &TransportIRConverter{
		inner:     inner,
		extractor: NewIRExtensionExtractor(),
		restorer:  NewIRExtensionRestorer(),
		cb:        NewStreamCircuitBreaker(),
	}
}

// SetCircuitBreaker replaces the circuit breaker (testing/injection).
func (c *TransportIRConverter) SetCircuitBreaker(cb *StreamCircuitBreaker) {
	if cb != nil {
		c.cb = cb
	}
}

func (c *TransportIRConverter) circuitCheck() error {
	if c.cb != nil && c.cb.ShouldFallback() {
		return ErrConverterCircuitOpen
	}
	return nil
}

func (c *TransportIRConverter) recordErr() {
	if c.cb != nil {
		c.cb.RecordError()
	}
}

func (c *TransportIRConverter) recordOK() {
	if c.cb != nil {
		c.cb.RecordSuccess()
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
	out = c.restoreExtensions(out, req.Extensions)
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
	out = c.restoreExtensions(out, req.Extensions)
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
