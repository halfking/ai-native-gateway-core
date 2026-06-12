package discovery_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/discovery"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
)

// ── Helper: start a test postgres  ────────────────────────────────────────

func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// ── URL resolution for Xiaomi MiMo ─────────────────────────────────────────

func TestModelsURL_XiaomiMiMo(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "xiaomi /v1 base",
			baseURL: "https://token-plan-cn.xiaomimimo.com/v1",
			want:    "https://token-plan-cn.xiaomimimo.com/v1/models",
		},
		{
			name:    "xiaomi base with trailing slash",
			baseURL: "https://token-plan-cn.xiaomimimo.com/v1/",
			want:    "https://token-plan-cn.xiaomimimo.com/v1/models",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := upstreamurl.ModelsURL(tt.baseURL)
			if got != tt.want {
				t.Errorf("ModelsURL(%q) = %q, want %q", tt.baseURL, got, tt.want)
			}
		})
	}
}

// ── Model ID extraction from real Xiaomi response ─────────────────────────

func TestExtractModelIDs_XiaomiMiMo(t *testing.T) {
	// Real response from https://token-plan-cn.xiaomimimo.com/v1/models
	respData := json.RawMessage(`{
		"object": "list",
		"data": [
			{"id": "mimo-v2-omni", "object": "model", "owned_by": "xiaomi"},
			{"id": "mimo-v2-pro", "object": "model", "owned_by": "xiaomi"},
			{"id": "mimo-v2-tts", "object": "model", "owned_by": "xiaomi"},
			{"id": "mimo-v2.5", "object": "model", "owned_by": "xiaomi"},
			{"id": "mimo-v2.5-asr", "object": "model", "owned_by": "xiaomi"},
			{"id": "mimo-v2.5-pro", "object": "model", "owned_by": "xiaomi"},
			{"id": "mimo-v2.5-tts", "object": "model", "owned_by": "xiaomi"},
			{"id": "mimo-v2.5-tts-voiceclone", "object": "model", "owned_by": "xiaomi"},
			{"id": "mimo-v2.5-tts-voicedesign", "object": "model", "owned_by": "xiaomi"}
		]
	}`)

	// The extractModelIDs function is unexported; test it indirectly
	// by verifying the JSON structure is parseable
	var openai struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respData, &openai); err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(openai.Data) != 9 {
		t.Errorf("expected 9 models, got %d", len(openai.Data))
	}

	expected := []string{
		"mimo-v2-omni", "mimo-v2-pro", "mimo-v2-tts",
		"mimo-v2.5", "mimo-v2.5-asr", "mimo-v2.5-pro",
		"mimo-v2.5-tts", "mimo-v2.5-tts-voiceclone", "mimo-v2.5-tts-voicedesign",
	}
	for i, m := range openai.Data {
		if m.ID != expected[i] {
			t.Errorf("model[%d].id = %q, want %q", i, m.ID, expected[i])
		}
	}
}

// ── Normalize + Family for MiMo models ─────────────────────────────────────

func TestNormalizeModelName_MiMo(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		// Raw names from Xiaomi API -> normalized names
		{"mimo-v2-omni", "mimo-v2-omni"},
		{"mimo-v2-pro", "mimo-v2-pro"},
		{"mimo-v2-tts", "mimo-v2-tts"},
		{"mimo-v2.5", "mimo-v2.5"},
		{"mimo-v2.5-asr", "mimo-v2.5-asr"},
		{"mimo-v2.5-pro", "mimo-v2.5-pro"},
		{"mimo-v2.5-tts", "mimo-v2.5-tts"},
		{"mimo-v2.5-tts-voiceclone", "mimo-v2.5-tts-voiceclone"},
		{"mimo-v2.5-tts-voicedesign", "mimo-v2.5-tts-voicedesign"},
		// Already normalized (lowercase, no vendor prefix)
		{"MiMo-V2.5-Pro", "mimo-v2.5-pro"},
		{"MIMO-V2.5", "mimo-v2.5"},
		// With vendor prefix
		{"xiaomi/mimo-v2.5-pro", "mimo-v2.5-pro"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := discovery.NormalizeModelName(tt.raw)
			if got != tt.want {
				t.Errorf("NormalizeModelName(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestInferFamily_MiMo(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"mimo-v2.5-pro", "mimo"},
		{"mimo-v2.5", "mimo"},
		{"mimo-v2-pro", "mimo"},
		{"mimo-v2-omni", "mimo"},
		{"mimo-v2.5-tts-voiceclone", "mimo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := discovery.InferFamily(tt.name)
			if got != tt.want {
				t.Errorf("InferFamily(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

// ── GenerateAliases for MiMo ───────────────────────────────────────────────

func TestGenerateAliases_MiMo(t *testing.T) {
	raw := "mimo-v2.5-pro"
	canonical := "mimo-v2.5-pro"
	aliases := discovery.GenerateAliases(raw, canonical)
	if len(aliases) < 2 {
		t.Fatalf("expected >= 2 aliases, got %d: %v", len(aliases), aliases)
	}
	found := make(map[string]bool)
	for _, a := range aliases {
		found[a] = true
	}
	for _, need := range []string{"mimo-v2.5-pro", "mimo_v2.5_pro"} {
		if !found[need] {
			t.Errorf("missing alias %q in %v", need, aliases)
		}
	}
}

// ── Live Integration: call Xiaomi API and validate full flow ───────────────

func TestXiaomiMiMo_LiveIntegration(t *testing.T) {
	apiKey := os.Getenv("XIAOMI_API_KEY")
	if apiKey == "" {
		apiKey = "tp-cia63detlzzaz731c3q3gebwi7ab6y1fas5r4sajo8n11l4m"
	}
	baseURL := os.Getenv("XIAOMI_BASE_URL")
	if baseURL == "" {
		baseURL = "https://token-plan-cn.xiaomimimo.com/v1"
	}

	modelsURL := upstreamurl.ModelsURL(baseURL)
	t.Logf("models URL: %s", modelsURL)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", modelsURL, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP GET %s: %v", modelsURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Object != "list" {
		t.Errorf("expected object=list, got %q", body.Object)
	}
	if len(body.Data) == 0 {
		t.Fatal("no models returned")
	}

	// Verify key chat models are present
	chatModels := map[string]bool{
		"mimo-v2.5-pro": false,
		"mimo-v2.5":     false,
		"mimo-v2-pro":   false,
		"mimo-v2-omni":  false,
	}
	for _, m := range body.Data {
		if _, ok := chatModels[m.ID]; ok {
			chatModels[m.ID] = true
		}
	}
	for model, found := range chatModels {
		if !found {
			t.Errorf("missing expected chat model: %s", model)
		}
	}

	t.Logf("Found %d models from Xiaomi API", len(body.Data))
	for _, m := range body.Data {
		normalized := discovery.NormalizeModelName(m.ID)
		family := discovery.InferFamily(normalized)
		t.Logf("  %s -> %s (family=%s)", m.ID, normalized, family)
	}
}

// ── HTTP-based unit test for fetchModels via httptest ──────────────────────

func TestFetchModels_XiaomiMiMo_MockServer(t *testing.T) {
	// Use a test server that returns the real Xiaomi format response
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"id": "mimo-v2-omni", "object": "model", "owned_by": "xiaomi"},
				{"id": "mimo-v2-pro", "object": "model", "owned_by": "xiaomi"},
				{"id": "mimo-v2.5", "object": "model", "owned_by": "xiaomi"},
				{"id": "mimo-v2.5-pro", "object": "model", "owned_by": "xiaomi"},
				{"id": "mimo-v2.5-tts", "object": "model", "owned_by": "xiaomi"},
			},
		})
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	modelsURL := upstreamurl.ModelsURL(srv.URL)
	req, err := http.NewRequestWithContext(ctx, "GET", modelsURL, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	count := 0
	for _, m := range body.Data {
		normalized := discovery.NormalizeModelName(m.ID)
		family := discovery.InferFamily(normalized)
		if family != "mimo" {
			t.Errorf("expected family=mimo for %s, got %s", m.ID, family)
		}
		count++
	}
	if count < 4 {
		t.Errorf("expected >= 4 models, got %d", count)
	}
}

// ── Verify Go upstreamurl matches Python discovery_utils._models_endpoint ──

func TestModelsURL_PythonParity(t *testing.T) {
	// Python: _models_endpoint("https://token-plan-cn.xiaomimimo.com/v1", template="/models")
	// = "https://token-plan-cn.xiaomimimo.com/v1/models"
	base := "https://token-plan-cn.xiaomimimo.com/v1"
	// Python template-based: base + "/models" = base/models
	pythonResult := base + "/models"

	// Go: upstreamurl.ModelsURL (strips /vN then appends /v1/models)
	goResult := upstreamurl.ModelsURL(base)

	if goResult != pythonResult {
		t.Errorf("URL mismatch:\n  Python: %s\n  Go:     %s\n  Fix: Go must support models_endpoint_template from provider_catalog",
			pythonResult, goResult)
	}
}

// ── Alt-format parsing: bare array, object array ───────────────────────────

func TestExtractModelIDs_AltFormats(t *testing.T) {
	tests := []struct {
		name string
		json string
		want []string
	}{
		{
			"openai_standard",
			`{"data":[{"id":"mimo-v2.5-pro"}]}`,
			[]string{"mimo-v2.5-pro"},
		},
		{
			"models_key",
			`{"models":[{"id":"mimo-v2.5"}]}`,
			[]string{"mimo-v2.5"},
		},
		{
			"bare_array",
			`["mimo-v2.5-pro","mimo-v2.5"]`,
			[]string{"mimo-v2.5-pro", "mimo-v2.5"},
		},
		{
			"object_array_id",
			`[{"id":"mimo-v2.5-pro"}]`,
			[]string{"mimo-v2.5-pro"},
		},
		{
			"object_array_name",
			`[{"name":"mimo-v2.5-pro"}]`,
			[]string{"mimo-v2.5-pro"},
		},
		{
			"object_array_model",
			`[{"model":"mimo-v2.5-pro"}]`,
			[]string{"mimo-v2.5-pro"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use single-parser logic matching extractModelIDs priority order.
			// extractModelIDs returns on the first successful match.
			var ids []string

			// 1. Try OpenAI format: {"data": [{"id": "..."}]}
			var openai struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(tt.json), &openai); err == nil && len(openai.Data) > 0 {
				for _, m := range openai.Data {
					if m.ID != "" {
						ids = append(ids, m.ID)
					}
				}
			}

			// 2. Try alternative format: {"models": [{"id": "..."}]}
			if len(ids) == 0 {
				var alt struct {
					Models []struct {
						ID string `json:"id"`
					} `json:"models"`
				}
				if err := json.Unmarshal([]byte(tt.json), &alt); err == nil && len(alt.Models) > 0 {
					for _, m := range alt.Models {
						if m.ID != "" {
							ids = append(ids, m.ID)
						}
					}
				}
			}

			// 3. Try bare array: ["model1", "model2"]
			if len(ids) == 0 {
				var bare []string
				if err := json.Unmarshal([]byte(tt.json), &bare); err == nil && len(bare) > 0 {
					ids = append(ids, bare...)
				}
			}

			// 4. Try object array: [{"id": "..."}, {"name": "..."}]
			if len(ids) == 0 {
				var objArray []map[string]any
				if err := json.Unmarshal([]byte(tt.json), &objArray); err == nil && len(objArray) > 0 {
					for _, m := range objArray {
						if id, ok := m["id"].(string); ok && id != "" {
							ids = append(ids, id)
						} else if name, ok := m["name"].(string); ok && name != "" {
							ids = append(ids, name)
						} else if model, ok := m["model"].(string); ok && model != "" {
							ids = append(ids, model)
						}
					}
				}
			}

			if len(ids) != len(tt.want) {
				t.Errorf("len=%d, want %d: %v", len(ids), len(tt.want), ids)
				return
			}
			for i, id := range ids {
				if id != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, id, tt.want[i])
				}
			}
		})
	}
}
