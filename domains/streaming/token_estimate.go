package streaming

import "github.com/kaixuan/llm-gateway-go/domains/tokenest"

// EstimateInputTokens returns the receipt-time estimate for the original client
// request body. Compression decisions deliberately use the assembled outbound
// body estimate instead; see SessionCompressor.Prepare.
func EstimateInputTokens(body []byte) int {
	return tokenest.FromChars(len(body))
}

func promptTokensEstimateFromContext(c *RequestLogContext, body []byte) *int {
	if c != nil && c.PromptTokensEstimate != nil {
		v := *c.PromptTokensEstimate
		return &v
	}
	if tokens := EstimateInputTokens(body); tokens > 0 {
		return &tokens
	}
	return nil
}

func usageSourceForEstimate(c *RequestLogContext, body []byte) *string {
	if promptTokensEstimateFromContext(c, body) == nil {
		return nil
	}
	source := UsageSourceEstimated
	return &source
}

// strPtrFromLogCtx copies the SessionCompressor's token-band classification
// into a *string suitable for telemetry.RequestLogEntry.TokenBand. Returns
// nil when no band has been recorded yet (the band is only known after
// SessionCompressor.Prepare runs, which happens after this initial INSERT).
func strPtrFromLogCtx(c *RequestLogContext) *string {
	if c == nil || c.OutboundTokenBand == "" {
		return nil
	}
	v := c.OutboundTokenBand
	return &v
}

// tokenBandFromLogCtx is the emitTelemetry-time copy that prefers the most
// recent SessionCompressor.Prepare result when available. It mirrors
// promptTokensEstimateFromContext so success-path writers can populate the
// column alongside prompt_tokens without re-implementing the nil-checks.
func tokenBandFromLogCtx(c *RequestLogContext) *string {
	if c == nil {
		return nil
	}
	if c.OutboundTokenBand != "" {
		v := c.OutboundTokenBand
		return &v
	}
	return nil
}
