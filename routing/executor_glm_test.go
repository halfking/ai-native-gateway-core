package routing

import (
	"encoding/json"
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

	var obj map[string]interface{}
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}

	if v, ok := obj["model"]; !ok || v != "glm-5.1" {
		t.Errorf("model field = %v, want glm-5.1 (Zhipu raw offer name)", v)
	}
}

func TestPrepareRequestBody_GLM51_Nvidia(t *testing.T) {
	// Client uses standardized name; nvidia-build offer keeps vendor prefix.
	clientBody := `{"model":"glm-5.1","messages":[{"role":"user","content":"Reply OK"}],"max_tokens":8}`

	cand := provider.Candidate{
		CredentialID: 175,
		ProviderID:   49,
		BaseURL:      "https://integrate.api.nvidia.com/v1",
		Protocol:     "openai-completions",
		CatalogCode:  "nvidia",
		RawModel:     "z-ai/glm-5.1",
		Tier:         2,
	}

	params := &ExecParams{
		BodyBytes:      []byte(clientBody),
		ClientModel:    "glm-5.1",
		IsStream:       false,
		ClientProtocol: "openai-completions",
	}

	got := prepareRequestBody(params, cand)

	var obj map[string]interface{}
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}

	modelVal, _ := obj["model"].(string)
	if modelVal != "z-ai/glm-5.1" {
		t.Errorf("model sent to NVIDIA = %q, want z-ai/glm-5.1 (offer raw_model_name)", modelVal)
	}
}

func TestResolveOutboundModel_ExplicitTransformWins(t *testing.T) {
	params := &ExecParams{
		ClientModel:   "glm-5.1",
		OutboundModel: "custom-upstream-name",
	}
	cand := provider.Candidate{RawModel: "z-ai/glm-5.1"}

	got := resolveOutboundModel(params, cand)
	if got != "custom-upstream-name" {
		t.Errorf("resolveOutboundModel() = %q, want transform override", got)
	}
}
