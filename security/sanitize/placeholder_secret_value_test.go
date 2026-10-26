package sanitize

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// The YAML regex must see the assignment before semantic marker filtering.
// Exercise actual captured bytes, without tool JSON, restoration or SSE.
func TestSedMaskWithProductionYAML(t *testing.T) {
	for _, managed := range []bool{false, true} {
		d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if managed {
			m, err := sanitizerules.NewManager(sanitizerules.NewMemoryStore(), d.RuleConfig(), sanitizerules.Options{})
			if err != nil {
				t.Fatal(err)
			}
			d.SetRuleManager(m)
			if err := m.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		for _, quote := range []string{"'", "\""} {
			command := "sed " + quote + "s/PASSWORD=.*/PASSWORD=[REDACTED:secret]/; s/SECRET=.*/SECRET=[MASKED]/" + quote + " .env"
			seen := false
			for _, p := range d.patterns {
				if p.sType != TypeSecret || p.kind != "credential_declaration" {
					continue
				}
				for _, match := range p.regex.FindAllStringSubmatch(command, -1) {
					seen = true
					if match[p.group] == "" {
						t.Fatal("empty YAML capture")
					}
				}
			}
			if !seen {
				t.Fatal("YAML did not see mask assignment")
			}
			encoded, _ := json.Marshal(map[string]string{"command": command})
			for _, command := range []string{command, "请调用工具：\n" + string(encoded)} {
				for _, scope := range []DetectionScope{ScopeInputDeclarations, ScopeRedact, ScopeEgress} {
					fragments, err := d.DetectScope(context.Background(), command, scope)
					if err != nil || len(fragments) != 0 {
						t.Fatalf("managed=%v scope=%v: %v %v", managed, scope, fragments, err)
					}
					literal := command + "; password=TrulySensitive987!"
					fragments, err = d.DetectScope(context.Background(), literal, scope)
					if err != nil {
						t.Fatal(err)
					}
					found := false
					for _, f := range fragments {
						found = found || f.Type == TypeSecret && strings.Contains(f.Value, "TrulySensitive987!")
					}
					if !found {
						t.Fatalf("real credential hidden: managed=%v scope=%v", managed, scope)
					}
					for _, literal := range []string{"password=[REDACTED]TrulySensitive987!", "password=[MASKED]TrulySensitive987!", "password=[REDACTED:secret]/TrulySensitive987!", command + "; password=[REDACTED]/TrulySensitive987!", `password='[REDACTED]'TrulySensitive987!`, `password="[MASKED]"TrulySensitive987!`, `password=[REDACTED]"TrulySensitive987!"`, `password='****'TrulySensitive987!`, `password=[MASKED]" TrulySensitive987!"`, `password=[REDACTED]";TrulySensitive987!"`, `password=[REDACTED],TrulySensitive987!`, `password='[REDACTED]',TrulySensitive987!`} {
						fragments, err := d.DetectScope(context.Background(), literal, scope)
						if err != nil || len(fragments) == 0 {
							t.Fatalf("glued literal hidden: %q managed=%v scope=%v err=%v", literal, managed, scope, err)
						}
					}
				}
			}
		}
	}
}
