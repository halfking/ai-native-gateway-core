package main

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domains/analysis"
)

// embedAdapter keeps the autoroute package independent from the analysis
// client package while reusing the gateway's OpenAI-compatible embedding API.
type embedAdapter struct {
	oc    *analysis.OpenAIClient
	model string
}

func (a *embedAdapter) Embed(ctx context.Context, text string) ([]float32, error) {
	return a.oc.Embed(ctx, a.model, text)
}
