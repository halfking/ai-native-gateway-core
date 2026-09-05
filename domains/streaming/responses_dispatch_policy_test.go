package streaming

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

func TestResponsesDispatchOptionsPreserveProviderAndModelSwitchPolicy(t *testing.T) {
	previous := dispatch.IsModelChangeEnabled()
	t.Cleanup(func() { dispatch.SetModelChangeEnabled(previous) })

	dispatch.SetModelChangeEnabled(true)
	allowProvider, allowModel, alternatives := responsesDispatchOptions(
		&RequestLogContext{IsAutoRequest: true, AutoFallbackModels: []string{"model-b", "model-c"}},
		true,
	)
	if !allowProvider || !allowModel {
		t.Fatalf("switch policy = provider:%v model:%v, want both enabled", allowProvider, allowModel)
	}
	if len(alternatives) != 2 || alternatives[0] != "model-b" || alternatives[1] != "model-c" {
		t.Fatalf("model alternatives = %v, want [model-b model-c]", alternatives)
	}

	allowProvider, allowModel, alternatives = responsesDispatchOptions(nil, true)
	if !allowProvider || allowModel || alternatives != nil {
		t.Fatalf("non-auto policy = provider:%v model:%v alternatives:%v", allowProvider, allowModel, alternatives)
	}

	dispatch.SetModelChangeEnabled(false)
	allowProvider, allowModel, alternatives = responsesDispatchOptions(
		&RequestLogContext{IsAutoRequest: true, AutoFallbackModels: []string{"model-b"}},
		false,
	)
	if allowProvider || allowModel || alternatives != nil {
		t.Fatalf("disabled model gate policy = provider:%v model:%v alternatives:%v", allowProvider, allowModel, alternatives)
	}
}
