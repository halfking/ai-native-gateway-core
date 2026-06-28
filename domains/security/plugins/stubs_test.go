package plugins

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
)

func TestStubs_AllAllowAndStubCoded(t *testing.T) {
	type factory struct {
		name string
		run  func(t *testing.T) (allow bool, code, direction string)
	}
	factories := []factory{
		{
			name: "sensitive_input",
			run: func(t *testing.T) (bool, string, string) {
				v, err := NewSensitiveInputChecker().Inspect(context.Background(), &domain.PipelineRequest{})
				if err != nil {
					t.Fatalf("Inspect: %v", err)
				}
				return v.Allow, v.Code, NewSensitiveInputChecker().Direction()
			},
		},
		{
			name: "sensitive_output",
			run: func(t *testing.T) (bool, string, string) {
				v, err := NewSensitiveOutputChecker().Inspect(context.Background(), &domain.PipelineRequest{})
				if err != nil {
					t.Fatalf("Inspect: %v", err)
				}
				return v.Allow, v.Code, NewSensitiveOutputChecker().Direction()
			},
		},
		{
			name: "policy_compliance",
			run: func(t *testing.T) (bool, string, string) {
				v, err := NewPolicyComplianceChecker().Inspect(context.Background(), &domain.PipelineRequest{})
				if err != nil {
					t.Fatalf("Inspect: %v", err)
				}
				return v.Allow, v.Code, NewPolicyComplianceChecker().Direction()
			},
		},
		{
			name: "tool_risk",
			run: func(t *testing.T) (bool, string, string) {
				v, err := NewToolRiskChecker().Inspect(context.Background(), &domain.PipelineRequest{})
				if err != nil {
					t.Fatalf("Inspect: %v", err)
				}
				return v.Allow, v.Code, NewToolRiskChecker().Direction()
			},
		},
		{
			name: "data_exfiltration",
			run: func(t *testing.T) (bool, string, string) {
				v, err := NewDataExfiltrationChecker().Inspect(context.Background(), &domain.PipelineRequest{})
				if err != nil {
					t.Fatalf("Inspect: %v", err)
				}
				return v.Allow, v.Code, NewDataExfiltrationChecker().Direction()
			},
		},
	}

	for _, f := range factories {
		t.Run(f.name, func(t *testing.T) {
			allow, code, direction := f.run(t)
			if !allow {
				t.Fatalf("%s stub should Allow", f.name)
			}
			if code != "stub" {
				t.Fatalf("%s code = %q, want stub", f.name, code)
			}
			_ = direction
		})
	}
}

func TestStubs_Directions(t *testing.T) {
	cases := []struct {
		name string
		dir  string
	}{
		{"sensitive_input", "input"},
		{"sensitive_output", "output"},
		{"policy_compliance", "both"},
		{"tool_risk", "input"},
		{"data_exfiltration", "both"},
	}
	checks := map[string]string{
		"sensitive_input":   NewSensitiveInputChecker().Direction(),
		"sensitive_output":  NewSensitiveOutputChecker().Direction(),
		"policy_compliance": NewPolicyComplianceChecker().Direction(),
		"tool_risk":         NewToolRiskChecker().Direction(),
		"data_exfiltration": NewDataExfiltrationChecker().Direction(),
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := checks[c.name]; got != c.dir {
				t.Fatalf("Direction = %q, want %q", got, c.dir)
			}
		})
	}
}
