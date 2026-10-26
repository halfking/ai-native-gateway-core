package streaming

import (
	"bytes"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/settings"
)

func TestOutputMasterSwitchWriteTime(t *testing.T) {
	previous := settings.Global
	t.Cleanup(func() { settings.Global = previous })
	rows := map[string][]byte{"output_compliance.enabled": []byte(`true`)}
	r := settings.NewRegistry()
	r.RegisterBackend(settings.ScopePlatform, &r1013FakeBackend{store: rows})
	for _, sp := range settings.ModuleSpecs() {
		if err := r.RegisterSpec(sp); err != nil {
			t.Fatal(err)
		}
	}
	settings.Global = r
	body := r1013Body(t, "alice@example.com")
	fn := BuildRedactBodyFn(outputcompliance.NewBuiltinChecker(), nil, nil)
	for _, v := range []string{"true", "false", "true"} {
		rows["sanitize.output_check_enabled"] = []byte(v)
		got := fn(body, "session", "default")
		if (v == "false") != bytes.Equal(got, body) {
			t.Fatalf("write-time switch=%s output=%s", v, got)
		}
	}
}
