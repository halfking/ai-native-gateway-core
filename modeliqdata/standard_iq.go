// Package modeliqdata ships the reference "standard IQ" table for canonical
// models and offers name-aligned lookups.
//
// The reference values are a curated snapshot of the public Artificial Analysis
// Intelligence Index (v4.1.1, https://artificialanalysis.ai/evaluations/
// artificial-analysis-intelligence-index). The Index is a text-only English
// composite benchmark (Agents 34% / Coding 24% / Scientific 24% / General 18%)
// reported on a 0-100 accuracy-style scale — NOT a human-IQ-100 scale. We keep
// the raw upstream scale so the value is traceable and comparable to the
// source; UI labels render it as "标准智商".
//
// The dataset is embedded so the gateway has sane defaults with no network
// dependency. Operators who want fresher numbers can run
// cmd/fetch-standard-iq with an Artificial Analysis API key (AA_API_KEY) to
// overwrite models_canonical.standard_iq at runtime.
package modeliqdata

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

// SourceArtificialAnalysis is the canonical source label persisted alongside
// every standard_iq value written from this package.
const SourceArtificialAnalysis = "artificialanalysis"

// AAAPIBase is the Artificial Analysis Data API base. The free-tier language
// models endpoint returns Intelligence Index scores; it requires an x-api-key.
const AAAPIBase = "https://artificialanalysis.ai/api/v2"

//go:embed data/standard_iq.json
var standardIQJSON []byte

// ReferenceEntry is one row of the embedded reference table.
type ReferenceEntry struct {
	CanonicalName string  `json:"canonical_name"`
	StandardIQ    float64 `json:"standard_iq"`
	Family        string  `json:"family"`
}

type referenceFile struct {
	Meta struct {
		Source       string `json:"source"`
		SourceURL    string `json:"source_url"`
		SnapshotDate string `json:"snapshot_date"`
	} `json:"_meta"`
	Models []ReferenceEntry `json:"models"`
}

var (
	loadOnce  sync.Once
	loadError error
	loaded    referenceFile

	// lookup caches: exact key (canonical_name as stored) + normalized variants
	byExact map[string]float64
	byNorm  map[string]float64
)

// ReferenceVersion returns the source label + snapshot date for display.
func ReferenceVersion() (source, snapshotDate string) {
	ensureLoaded()
	return loaded.Meta.Source, loaded.Meta.SnapshotDate
}

// ReferenceEntries returns the raw embedded entries (for tooling/CLI reports).
func ReferenceEntries() []ReferenceEntry {
	ensureLoaded()
	return loaded.Models
}

func ensureLoaded() {
	loadOnce.Do(func() {
		loadError = json.Unmarshal(standardIQJSON, &loaded)
		if loadError != nil {
			return
		}
		byExact = make(map[string]float64, len(loaded.Models))
		byNorm = make(map[string]float64, len(loaded.Models)*2)
		for _, m := range loaded.Models {
			key := strings.TrimSpace(strings.ToLower(m.CanonicalName))
			if key == "" {
				continue
			}
			byExact[key] = m.StandardIQ
			// The same model appears in models_canonical under both
			// dot-separated and dash-separated version forms (e.g.
			// "claude-sonnet-4.5" vs "claude-sonnet-4-5", "gemini-2.5-pro"
			// vs "gemini-2-5-pro"). Index the dot→dash fold so either form
			// resolves to the same value without bloating the reference file.
			if folded := dashFold(key); folded != key {
				if _, exists := byExact[folded]; !exists {
					byExact[folded] = m.StandardIQ
				}
			}
			// Also index under the more aggressive NormalizeRouteKey so that
			// date-suffixed / dashed variants land on the same value.
			byNorm[modelname.NormalizeRouteKey(key)] = m.StandardIQ
		}
	})
}

// dashFold returns name with '.' replaced by '-' (version separators), used to
// bridge "claude-sonnet-4.5" and "claude-sonnet-4-5". It does NOT touch other
// characters so non-version dots (rare) are unaffected at the call site.
func dashFold(name string) string {
	return strings.ReplaceAll(name, ".", "-")
}

// LookupStandardIQ resolves a standard IQ for a canonical / raw model name.
// It tries, in order: exact lowercase match, dot→dash fold, CanonicalizeClientModel,
// then NormalizeRouteKey (strips date suffixes / collapses dashes). Returns the
// value, whether it was found, and the matched key ("" if not found).
//
// The input is typically a models_canonical.canonical_name (already canonical)
// but accepting raw names keeps callers uniform.
func LookupStandardIQ(modelName string) (iq float64, found bool, matchedKey string) {
	ensureLoaded()
	if loadError != nil || byExact == nil {
		return 0, false, ""
	}
	if modelName == "" {
		return 0, false, ""
	}
	// 1. Exact (lowercased) — canonical_name stored verbatim.
	k := strings.ToLower(strings.TrimSpace(modelName))
	if v, ok := byExact[k]; ok {
		return v, true, k
	}
	// 1b. Dot→dash fold: "claude-sonnet-4.5" -> "claude-sonnet-4-5".
	if kf := dashFold(k); kf != k {
		if v, ok := byExact[kf]; ok {
			return v, true, kf
		}
	}
	// 2. CanonicalizeClientModel (strips vendor prefix, keeps dashes/dots).
	c := modelname.CanonicalizeClientModel(modelName)
	if c != "" && c != k {
		if v, ok := byExact[c]; ok {
			return v, true, c
		}
		if cf := dashFold(c); cf != c {
			if v, ok := byExact[cf]; ok {
				return v, true, cf
			}
		}
	}
	// 3. NormalizeRouteKey (strips dates, collapses dashes) — last resort.
	n := modelname.NormalizeRouteKey(modelName)
	if n != "" {
		if v, ok := byNorm[n]; ok {
			return v, true, n
		}
	}
	return 0, false, ""
}

// --- Artificial Analysis Data API (optional live refresh) ------------------

// AALanguageModel is the subset of the AA free-tier payload we consume.
// Fields are lenient (pointers/omitempty) because the API shape evolves.
type AALanguageModel struct {
	Slug             string  `json:"slug"`
	Name             string  `json:"name"`
	IntelligenceIndex float64 `json:"intelligence_index"`
	Score            float64 `json:"score"` // some payloads use "score"
	Provider         string  `json:"provider"`
}

// FetchAAIntelligenceIndex calls the Artificial Analysis free-tier language
// models endpoint with the given API key and returns the parsed models.
// Returns an error if apiKey is empty or the request fails.
//
// This is the live-refresh path used by cmd/fetch-standard-iq; the gateway
// itself never calls it at runtime, so a missing/invalid key is a soft fail.
func FetchAAIntelligenceIndex(ctx context.Context, apiKey string) ([]AALanguageModel, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("AA_API_KEY not set")
	}
	url := AAAPIBase + "/language/models/free"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("aa api request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("aa api status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out []AALanguageModel
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode aa api response: %w", err)
	}
	return out, nil
}
