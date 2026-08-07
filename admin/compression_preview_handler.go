// Package admin — compression_preview_handler.go (docs/omni-ref3 C7)
//
// POST /api/admin/compression/preview — dry-run the compression pipeline
// against an operator-supplied body and report what WOULD happen.
//
// Why this exists: before C7 the only way to answer "why did (or didn't)
// this session compress?" was to grep gateway logs after the fact. This
// endpoint lets an operator paste a body and get the staged breakdown
// (thinking strip → media prune → window check → tool strip → trim),
// the byte/token deltas, and the lossiness class — synchronously.
//
// Safety contract (the whole reason this is a separate code path):
//
//	It calls compression.Preview, NOT SessionCompressor.Prepare.
//	Prepare writes the session cache, may spend real money on an LLM
//	summary call, and populates the C3 result memo. A preview must do
//	none of those. Preview is pure: same input → same output, no I/O,
//	no state mutation. See preview.go for the guarantee.
//
//	Consequence: the preview cannot show the actual LLM summary text.
//	It reports would_try_llm_summary=true and models the mechanical-trim
//	fallback instead. The response's `note` field states this plainly so
//	an operator never mistakes a preview for the real outcome.
//
// Admin-only: registered behind adminWrap like every other handler here.
// The request body is operator-supplied prompt content, so it is neither
// logged nor persisted — it is processed in-memory and discarded.
package admin

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression" //nolint:depguard // C7 preview reuses the compression primitives directly
)

// maxPreviewBodyBytes caps the accepted payload. Compression previews are
// for realistic conversation bodies; anything past this is either a paste
// accident or an attempt to burn admin CPU. 8 MiB comfortably exceeds the
// largest context window in tokens×chars terms.
const maxPreviewBodyBytes = 8 << 20 // 8 MiB

// CompressionPreviewHandler serves the compression dry-run endpoint.
type CompressionPreviewHandler struct{}

// NewCompressionPreviewHandler builds the handler. Stateless — it needs no
// database or cache, precisely because Preview is pure.
func NewCompressionPreviewHandler() *CompressionPreviewHandler {
	return &CompressionPreviewHandler{}
}

// RegisterRoutes wires the preview endpoint.
func (h *CompressionPreviewHandler) RegisterRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/compression/preview", adminWrap(h.handlePreview))
}

// compressionPreviewRequest is the POST payload.
//
// Body is the raw chat-completions / messages JSON to evaluate. It is
// required; everything else has a sane default so the minimal request is
// just {"body": {...}}.
type compressionPreviewRequest struct {
	// Body is the request body to preview. Accepts either an embedded JSON
	// object (the common case — paste the whole request) or a JSON string
	// containing the body.
	Body json.RawMessage `json:"body"`

	// Protocol selects the wire format: "openai" (default) or
	// "anthropic-messages". Determines which trim/strip variant applies.
	Protocol string `json:"protocol,omitempty"`

	// ContextWindow is the target model's window in tokens. 0 disables the
	// TOKEN trigger and the mechanical-trim path (matching production
	// behaviour when the window is unknown).
	ContextWindow int `json:"context_window,omitempty"`

	// Mode overrides the compression mode for this preview. Omit to use the
	// live resolved mode (settings → env → default). Accepts the numeric
	// mode values used by LLM_GATEWAY_COMPRESSION_MODE (0..5).
	Mode *int `json:"mode,omitempty"`

	// PruneMedia additionally models the A3 media-prune stage, which the
	// live Prepare path does not currently run. Off by default so the
	// preview mirrors production; turn it on to size up the opportunity.
	PruneMedia bool `json:"prune_media,omitempty"`

	// KeepMedia is the number of most recent media blocks to keep when
	// PruneMedia is set. 0 uses the A3 default.
	KeepMedia int `json:"keep_media,omitempty"`
}

// handlePreview runs the dry-run and returns the staged report.
//
// POST /api/admin/compression/preview
//
//	{
//	  "body": {"model":"gpt-4o","messages":[...]},
//	  "protocol": "openai",
//	  "context_window": 128000
//	}
//
// Response: the compression.PreviewResult plus a `note` explaining the
// LLM-summary caveat when that path would have fired.
func (h *CompressionPreviewHandler) handlePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "only POST is supported"})
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxPreviewBodyBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "failed to read request body"})
		return
	}
	if len(raw) > maxPreviewBodyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error":     "payload too large",
			"max_bytes": maxPreviewBodyBytes,
		})
		return
	}

	var req compressionPreviewRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}

	// Accept both {"body": {...}} and {"body": "{...}"} — operators pasting
	// from a log line often end up with the stringified form.
	previewBody := []byte(req.Body)
	if len(previewBody) > 0 && previewBody[0] == '"' {
		var asString string
		if err := json.Unmarshal(req.Body, &asString); err == nil {
			previewBody = []byte(asString)
		}
	}
	if len(previewBody) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "field 'body' is required"})
		return
	}

	opts := compression.PreviewOptions{
		Protocol:      req.Protocol,
		ContextWindow: req.ContextWindow,
		PruneMedia:    req.PruneMedia,
		KeepMedia:     req.KeepMedia,
	}
	if req.Mode != nil {
		m := compression.Mode(*req.Mode)
		opts.Mode = &m
	}

	result := compression.Preview(previewBody, opts)
	if result == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "preview produced no result"})
		return
	}

	// Be explicit about what a preview can and cannot tell you. Without
	// this note an operator could read `strategy: mechanical_trim` and
	// conclude the LLM summary is broken, when in fact the preview simply
	// refuses to spend money to find out.
	note := "Dry run: no session state was read or written and no LLM call was made."
	if result.WouldTryLLMSummary {
		note = "Dry run: the live path would attempt an LLM summary here. " +
			"This preview does not call the model (it would cost money and add latency), " +
			"so the reported strategy/bytes reflect the mechanical-trim fallback that applies " +
			"when the summary fails or the C1 breaker is open. Actual savings are typically larger."
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"preview": result,
		"note":    note,
	})
}
