package admin

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

func TestApplyRuntimeSettingModelChange(t *testing.T) {
	previous := dispatch.IsModelChangeEnabled()
	t.Cleanup(func() { dispatch.SetModelChangeEnabled(previous) })
	t.Setenv("AUTO_ROUTE_FALLBACK_ENABLED", "")

	applyRuntimeSetting(dispatch.ModelChangeGateKey, json.RawMessage("true"))
	if !dispatch.IsModelChangeEnabled() {
		t.Fatal("model change gate did not enable immediately")
	}

	applyRuntimeSetting(dispatch.ModelChangeGateKey, json.RawMessage("false"))
	if dispatch.IsModelChangeEnabled() {
		t.Fatal("model change gate did not disable immediately")
	}
}
