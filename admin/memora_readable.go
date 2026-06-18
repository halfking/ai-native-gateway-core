package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kaixuan/llm-gateway-go/memora"
)

const (
	memoraPreviewMaxLen   = 120
	memoraSearchTopK      = 100
	memoraPreviewSearchK  = 5
	memoraBatchConcurrency = 8
	memoraPreviewTimeout  = 3 * time.Second
)

// readableBlock is a Memora fact formatted for human/agent consumption.
type readableBlock struct {
	ID     string   `json:"id"`
	Text   string   `json:"text"`
	Kind   string   `json:"kind"`   // "text" | "json"
	Source string   `json:"source"` // "task" | "gw-session"
	Tags   []string `json:"tags,omitempty"`
	Score  float64  `json:"score,omitempty"`
}

type memoraSearchClient interface {
	Disabled() bool
	SearchAdmin(ctx context.Context, userID, query string, topK int) ([]memora.Memory, error)
}

// formatReadableBlock classifies Memora text and pretty-prints JSON payloads.
func formatReadableBlock(text string) (kind, display string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "text", ""
	}
	trimmed := strings.TrimSpace(text)
	if (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
		(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")) {
		var parsed any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
			pretty, err := json.MarshalIndent(parsed, "", "  ")
			if err == nil {
				return "json", string(pretty)
			}
		}
	}
	return "text", text
}

func memoryToReadableBlock(m memora.Memory, source string) readableBlock {
	kind, display := formatReadableBlock(m.Text)
	return readableBlock{
		ID:     m.ID,
		Text:   display,
		Kind:   kind,
		Source: source,
		Tags:   m.Tags,
		Score:  m.Score,
	}
}

func dedupeMemories(memories []memora.Memory) []memora.Memory {
	seen := make(map[string]struct{}, len(memories))
	out := make([]memora.Memory, 0, len(memories))
	for _, m := range memories {
		key := normalizeReadableKey(m.Text)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, m)
	}
	return out
}

func normalizeReadableKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func searchMemoraFacts(ctx context.Context, client memoraSearchClient, userID string, topK int) ([]memora.Memory, error) {
	if client == nil || client.Disabled() || userID == "" {
		return nil, nil
	}
	return client.SearchAdmin(ctx, userID, "", topK)
}

// searchMergedFacts loads Memora facts from task namespace and optional gw-session namespace.
func searchMergedFacts(ctx context.Context, client memoraSearchClient, apiKeyID int, taskID, sessionID string) ([]readableBlock, error) {
	if client == nil || client.Disabled() || apiKeyID <= 0 || taskID == "" {
		return nil, nil
	}

	var merged []memora.Memory

	taskUserID := memora.UserID(apiKeyID, taskID)
	taskMem, err := searchMemoraFacts(ctx, client, taskUserID, memoraSearchTopK)
	if err != nil {
		return nil, err
	}
	for _, m := range taskMem {
		m.Tags = append([]string{"source:task"}, m.Tags...)
		merged = append(merged, m)
	}

	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" && sessionID != "[空]" {
		gwUserID := memora.UserID(apiKeyID, "gw-session:"+sessionID)
		gwMem, gwErr := searchMemoraFacts(ctx, client, gwUserID, memoraSearchTopK)
		if gwErr != nil {
			if len(merged) == 0 {
				return nil, gwErr
			}
		} else {
			for _, m := range gwMem {
				m.Tags = append([]string{"source:gw-session"}, m.Tags...)
				merged = append(merged, m)
			}
		}
	}

	merged = dedupeMemories(merged)
	blocks := make([]readableBlock, 0, len(merged))
	for _, m := range merged {
		source := "task"
		for _, t := range m.Tags {
			if t == "source:gw-session" {
				source = "gw-session"
				break
			}
		}
		blocks = append(blocks, memoryToReadableBlock(m, source))
	}
	return blocks, nil
}

func readableBlocksToMaps(blocks []readableBlock) []map[string]any {
	if len(blocks) == 0 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		entry := map[string]any{
			"id":     b.ID,
			"text":   b.Text,
			"kind":   b.Kind,
			"source": b.Source,
		}
		if len(b.Tags) > 0 {
			entry["tags"] = b.Tags
		}
		if b.Score > 0 {
			entry["score"] = b.Score
		}
		out = append(out, entry)
	}
	return out
}

func truncateMemoraPreview(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if utf8.RuneCountInString(text) <= memoraPreviewMaxLen {
		return text
	}
	runes := []rune(text)
	return string(runes[:memoraPreviewMaxLen]) + "…"
}

func firstReadablePreview(blocks []readableBlock) string {
	for _, b := range blocks {
		if p := truncateMemoraPreview(b.Text); p != "" {
			return p
		}
	}
	return ""
}

type sessionPreviewInput struct {
	Index     int
	TaskID    string
	SessionID string
	APIKeyID  int
}

type sessionPreviewResult struct {
	Index   int
	Preview string
	Status  string // ok | empty | error | skipped
}

// batchMemoraPreviews fetches short previews for list rows with bounded concurrency.
func batchMemoraPreviews(ctx context.Context, client memoraSearchClient, inputs []sessionPreviewInput) []sessionPreviewResult {
	results := make([]sessionPreviewResult, len(inputs))
	if len(inputs) == 0 {
		return results
	}
	for i, in := range inputs {
		results[i] = sessionPreviewResult{Index: in.Index, Status: "skipped"}
	}
	if client == nil || client.Disabled() {
		return results
	}

	sem := make(chan struct{}, memoraBatchConcurrency)
	var wg sync.WaitGroup

	for _, in := range inputs {
		in := in
		if in.TaskID == "" || in.TaskID == "[空]" || in.APIKeyID <= 0 {
			results[in.Index].Status = "skipped"
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			searchCtx, cancel := context.WithTimeout(ctx, memoraPreviewTimeout)
			defer cancel()

			blocks, err := searchMergedFacts(searchCtx, client, in.APIKeyID, in.TaskID, in.SessionID)
			if err != nil {
				results[in.Index] = sessionPreviewResult{Index: in.Index, Status: "error"}
				return
			}
			preview := firstReadablePreview(blocks)
			if preview == "" {
				results[in.Index] = sessionPreviewResult{Index: in.Index, Status: "empty"}
				return
			}
			results[in.Index] = sessionPreviewResult{
				Index:   in.Index,
				Preview: preview,
				Status:  "ok",
			}
		}()
	}
	wg.Wait()
	return results
}

func parseIncludeMemora(r *http.Request, limit int) bool {
	if r == nil {
		return limit <= 20
	}
	v := strings.TrimSpace(r.URL.Query().Get("include_memora"))
	if v == "0" || strings.EqualFold(v, "false") {
		return false
	}
	if v == "1" || strings.EqualFold(v, "true") {
		return true
	}
	return limit <= 20
}
