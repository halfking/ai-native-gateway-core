// poc_benchmark is a standalone tool that demonstrates SpecBoost enhancement
// on a sample tool spec. It uses a MOCK LLM response (no real network call).
//
// Usage:
//
//	go run ./tools/specboost/poc_benchmark
//
// This is a PoC demonstration tool, NOT a production binary. The full
// 50-tool benchmark with real LLM calls is Q4 C1-4.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/kaixuan/llm-gateway-go/tools/specboost"
)

// sampleTool is a representative tool spec drawn from the registry pattern
// (registry.Tool), but hardcoded here to avoid a DB dependency in the PoC.
var sampleTool = specboost.ToolSpec{
	Name:        "web_search",
	Description: "Searches the web.",
	Parameters: map[string]specboost.ParamSpec{
		"query": {
			Type:     "string",
			Required: true,
		},
		"limit": {
			Type:        "integer",
			Description: "max results",
		},
	},
}

func main() {
	// Mock LLM server: returns an enhanced description for the sample tool.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"description": "Searches the public web for real-time information and returns ranked results. Use this tool when the user asks about current events, facts not in training data, or recent updates. Supports query operators and result limiting.",
			"parameters": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query. Supports boolean operators (AND, OR, NOT) and exact-phrase quotes.",
					"examples":    []any{"golang testing best practices", "\"AI gateway\" 2026"},
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of results to return (1-50). Defaults to 10.",
					"examples":    []any{5, 10},
				},
			},
			"examples": []map[string]any{
				{
					"input":  map[string]any{"query": "latest golang release", "limit": 3},
					"output": "3 search results with titles, URLs, and snippets",
				},
			},
			"diff_summary": "Expanded description (added use cases + operator support). Enriched parameter descriptions with types, constraints, and examples. Added 1 concrete usage example. Overall: richer context for function-calling accuracy.",
			"confidence":   0.88,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	fmt.Println("=== SpecBoost PoC Benchmark ===")
	fmt.Println()
	fmt.Println("Sample tool:", sampleTool.Name)
	fmt.Println("Original description length:", len(sampleTool.Description), "chars")
	fmt.Println()

	res, err := specboost.Enhance(context.Background(), sampleTool, specboost.EnhanceOptions{
		Endpoint: srv.URL,
		APIKey:   "benchmark",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Enhance failed:", err)
		os.Exit(1)
	}

	fmt.Println("--- Original ---")
	origJSON, _ := json.MarshalIndent(res.Original, "", "  ")
	fmt.Println(string(origJSON))
	fmt.Println()
	fmt.Println("--- Enhanced ---")
	enhJSON, _ := json.MarshalIndent(res.Enhanced, "", "  ")
	fmt.Println(string(enhJSON))
	fmt.Println()
	fmt.Println("--- Diff Summary (from LLM) ---")
	fmt.Println(res.DiffSummary)
	fmt.Println()
	fmt.Printf("Confidence: %.0f%%\n", res.Confidence*100)
	fmt.Printf("Template:   %s\n", res.TemplateVer)
	fmt.Println()

	origLen := len(res.Original.Description)
	enhLen := len(res.Enhanced.Description)
	fmt.Printf("Description growth: %d → %d chars (+%d%%)\n", origLen, enhLen, (enhLen-origLen)*100/max(origLen, 1))
	exampleGrowth := len(res.Enhanced.Examples) - len(res.Original.Examples)
	fmt.Printf("Examples added: +%d\n", exampleGrowth)
	fmt.Println()
	fmt.Println("Full 50-tool benchmark with real LLM + function-calling accuracy evaluation: Q4 C1-4.")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
