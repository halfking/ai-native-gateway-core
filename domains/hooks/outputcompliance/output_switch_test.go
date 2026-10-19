package outputcompliance

import (
	"context"
	"github.com/kaixuan/llm-gateway-go/domain"
	"os"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	compliance "github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// Protocol/owner fixtures exercise an active optional checker. Product-default
// behavior is separately pinned in settings; keep this suite explicit.
func TestMain(m *testing.M) {
	r := settings.NewRegistry()
	r.RegisterBackend(settings.ScopePlatform, switchBackend{"output_compliance.enabled": []byte(`true`)})
	for _, sp := range settings.ModuleSpecs() {
		r.MustRegisterSpec(sp)
	}
	settings.Global = r
	os.Exit(m.Run())
}

type switchBackend map[string][]byte

func (b switchBackend) Get(_ settings.Scope, k string) ([]byte, error)        { return b[k], nil }
func (b switchBackend) Set(_ settings.Scope, _ string, _ any) ([]byte, error) { return nil, nil }
func (b switchBackend) GetTenant(_, k string) ([]byte, error)                 { return b[k], nil }
func (b switchBackend) SetTenant(_, _ string, _ any) ([]byte, error)          { return nil, nil }

type switchChecker struct{ calls int }

func (c *switchChecker) Check(context.Context, string, string) (*compliance.ComplianceResult, error) {
	c.calls++
	return &compliance.ComplianceResult{Blocked: true}, nil
}

func TestOutputMasterSwitchHotReload(t *testing.T) {
	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })
	settings.Global = nil
	if !NewMandatoryOutputComplianceInterceptor(&switchChecker{}, "default").isEnabled() {
		t.Fatal("missing settings disabled guard")
	}
	r := settings.NewRegistry()
	rows := switchBackend{"output_compliance.enabled": []byte(`true`)}
	r.RegisterBackend(settings.ScopePlatform, rows)
	for _, sp := range settings.ModuleSpecs() {
		if err := r.RegisterSpec(sp); err != nil {
			t.Fatal(err)
		}
	}
	settings.Global = r
	for _, mandatory := range []bool{false, true} {
		c := &switchChecker{}
		it := NewOutputComplianceInterceptor(c, nil)
		if mandatory {
			it = NewMandatoryOutputComplianceInterceptor(c, "switch-test")
		}
		for _, value := range []string{"true", "false", "true"} {
			rows["sanitize.output_check_enabled"] = []byte(value)
			hook := NewHook(&fakeChecker{})
			if hook.Enabled(context.Background(), &domain.PipelineRequest{FinalResponse: []byte("text")}) != (value == "true") {
				t.Fatal("pipeline switch ignored")
			}
			before := c.calls
			res, err := it.InterceptNonStream(context.Background(), &response.InterceptRequest{ResponseBody: []byte(`{"choices":[{"message":{"content":"must check"}}]}`)})
			if err != nil {
				t.Fatal(err)
			}
			if value == "false" {
				if c.calls != before || res != nil {
					t.Fatal("disabled body was inspected")
				}
			} else if c.calls == before || res == nil || !res.ShouldBlock {
				t.Fatal("enabled body not blocked")
			}
			meta := &response.StreamMeta{State: response.NewStreamState()}
			frame := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"must check\"},\"finish_reason\":\"stop\"}]}\n\n")
			before = c.calls
			chunk, err := it.InterceptStreamChunk(context.Background(), frame, meta)
			if err != nil {
				t.Fatal(err)
			}
			if value == "false" {
				if c.calls != before || chunk != nil {
					t.Fatal("disabled stream was inspected or staged")
				}
			} else if c.calls == before || chunk == nil || !chunk.ShouldBlock {
				t.Fatal("enabled stream not blocked")
			}
		}
	}
	sp := r.Spec("sanitize.output_check_enabled")
	if sp == nil || sp.Default != true || !sp.HotReload || sp.Scope != settings.ScopePlatform {
		t.Fatal("switch spec missing or unsafe default")
	}
	if sp.Validate("false") == nil {
		t.Fatal("switch accepts a string")
	}
}
