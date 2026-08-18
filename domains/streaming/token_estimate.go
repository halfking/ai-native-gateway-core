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
