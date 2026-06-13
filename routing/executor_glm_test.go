package routing

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestPrepareRequestBody_GLM51_Zhipu(t *testing.T) {
	clientBody := `{"model":"glm-5.1","messages":[{"role":"user","content":"Reply OK"}],"max_tokens":8,"stream":false}`

	cand := provider.Candidate{
		CredentialID: 55,
		ProviderID:   7,
		BaseURL:      "https://open.bigmodel.cn/api/paas/v4",
		Protocol:     "openai-completions",
		CatalogCode:  "zhipu",
		RawModel:     "glm-5.1",
		Tier:         2,
	}

	params := &ExecParams{
		BodyBytes:      []byte(clientBody),
		ClientModel:    "glm-5.1",
		IsStream:       false,
		ClientProtocol: "openai-completions",
	}

	got := prepareRequestBody(params, cand)

	t.Logf("Input:  %s", clientBody)
	t.Logf("Output: %s", string(got))

	var obj map[string]interface{}
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}

	// 1. model name must be preserved
	if v, ok := obj["model"]; !ok || v != "glm-5.1" {
		t.Errorf("model field = %v, want glm-5.1", v)
	}

	// 2. messages must be preserved
	if _, ok := obj["messages"]; !ok {
		t.Error("messages field was stripped")
	}

	// 3. max_tokens must be preserved
	if _, ok := obj["max_tokens"]; !ok {
		t.Error("max_tokens field was stripped")
	}

	// 4. no unexpected fields added (except stream_options for stream=true)
	if _, ok := obj["stream_options"]; ok {
		t.Error("stream_options should NOT be injected for non-stream request")
	}

	// 5. no fields should be stripped
	var origObj map[string]interface{}
	json.Unmarshal([]byte(clientBody), &origObj)
	for k := range origObj {
		if _, ok := obj[k]; !ok {
			t.Errorf("field '%s' was unexpectedly stripped", k)
		}
	}
}

func TestPrepareRequestBody_GLM51_NvidiaRawModel(t *testing.T) {
	// nvidia-build offer has raw_model_name = "z-ai/glm-5.1"
	// If this raw name is sent to Zhipu, it will 405
	clientBody := `{"model":"glm-5.1","messages":[{"role":"user","content":"Reply OK"}],"max_tokens":8}`

	cand := provider.Candidate{
		CredentialID: 175,
		ProviderID:   7,
		BaseURL:      "https://open.bigmodel.cn/api/paas/v4",
		Protocol:     "openai-completions",
		CatalogCode:  "zhipu",
		RawModel:     "z-ai/glm-5.1", // ← THIS is the problem
		Tier:         2,
	}

	params := &ExecParams{
		BodyBytes:      []byte(clientBody),
		ClientModel:    "glm-5.1",
		IsStream:       false,
		ClientProtocol: "openai-completions",
	}

	got := prepareRequestBody(params, cand)

	t.Logf("Input:  %s", clientBody)
	t.Logf("Output: %s", string(got))

	var obj map[string]interface{}
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}

	modelVal, _ := obj["model"].(string)
	t.Logf("model sent to upstream: %q", modelVal)

	if strings.Contains(modelVal, "z-ai/") {
		t.Errorf("❌ BUG: model sent to Zhipu is %q — Zhipu does not recognize 'z-ai/' prefix and will return 405", modelVal)
	}
	if modelVal == "glm-5.1" {
		t.Logf("✅ model correctly sent as glm-5.1")
	}
}
