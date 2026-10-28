package pluginruntime

import (
	"reflect"
	"testing"
)

type helloSettings struct {
	Greeting string `json:"greeting" title_key:"plugin.hello.greeting" description_key:"plugin.hello.greetingDesc"`
	Times    int    `json:"times" title:"Times" enum:"1,2,3" min:"1" max:"3"`
	Enabled  bool   `json:"enabled" title:"Enabled"`
}

func TestSchemaOfAndValidate(t *testing.T) {
	s, defs := SchemaOf(helloSettings{Greeting: "hi", Times: 1, Enabled: true})
	if !reflect.DeepEqual(s.Order, []string{"greeting", "times", "enabled"}) {
		t.Fatalf("order=%v", s.Order)
	}
	if defs["greeting"] != "hi" || defs["times"] != int64(1) || defs["enabled"] != true {
		t.Fatalf("defs=%v", defs)
	}
	ok, err := s.Validate(map[string]any{"greeting": "yo", "times": float64(2), "enabled": false})
	if err != nil {
		t.Fatal(err)
	}
	if ok["times"] != int64(2) {
		t.Fatalf("times=%v", ok["times"])
	}
	if _, err := s.Validate(map[string]any{"times": float64(9)}); err == nil {
		t.Fatal("out of range taken")
	}
}

func TestAdaptASMConfigSchema(t *testing.T) {
	cs := ConfigSchema{
		"version": 1.0,
		"groups": []any{
			map[string]any{
				"key": "sampling",
				"fields": []any{
					map[string]any{"key": "rate_pct", "type": "number", "default": 20.0, "min": 0.0, "max": 100.0, "description_key": "settings.sampling.rate"},
					map[string]any{"key": "enabled", "type": "boolean", "default": false},
					map[string]any{"key": "rule_weights", "type": "object", "default": map[string]any{"a": 1}},
				},
			},
		},
	}
	s := AdaptConfigSchema(cs)
	if s == nil || s.Properties["rate_pct"] == nil || s.Properties["enabled"] == nil {
		t.Fatalf("adapt failed: %+v", s)
	}
	if _, ok := s.Properties["rule_weights"]; ok {
		t.Fatal("nested object should be skipped")
	}
	m := Manifest{ConfigSchema: cs}
	if m.EffectiveSettingsSchema() == nil {
		t.Fatal("EffectiveSettingsSchema should adapt ConfigSchema")
	}
}
