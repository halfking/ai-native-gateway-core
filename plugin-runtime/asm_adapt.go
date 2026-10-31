package pluginruntime

import (
	"fmt"
	"sort"
)

// AdaptConfigSchema converts a legacy/ASM ConfigSchema into SettingsSchema.
//
// Two shapes are recognised (single truth after adaptation):
//  1. Already JSON-Schema object: {"type":"object","properties":{...}}
//  2. ASM groups/fields: {"version":1,"groups":[{"fields":[{"key","type",...}]}]}
//
// Unsupported nested object defaults (ASM quality.rule_weights) are skipped
// with no panic — the form simply omits them until ASM migrates (see
// docs/执行日志-04 interface-change request).
func AdaptConfigSchema(cs ConfigSchema) *SettingsSchema {
	if cs == nil || len(cs) == 0 {
		return nil
	}
	if t, _ := cs["type"].(string); t == "object" {
		return adaptJSONSchemaObject(cs)
	}
	groups, ok := cs["groups"].([]any)
	if !ok || len(groups) == 0 {
		return nil
	}
	s := &SettingsSchema{Type: "object", Properties: map[string]*SettingsProperty{}}
	type fieldRow struct {
		order int
		key   string
		prop  *SettingsProperty
	}
	var rows []fieldRow
	gi := 0
	for _, g := range groups {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		fields, _ := gm["fields"].([]any)
		for fi, f := range fields {
			fm, ok := f.(map[string]any)
			if !ok {
				continue
			}
			key, _ := fm["key"].(string)
			if key == "" {
				continue
			}
			prop, err := fieldToProperty(fm)
			if err != nil {
				continue
			}
			rows = append(rows, fieldRow{order: gi*1000 + fi, key: key, prop: prop})
		}
		gi++
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].order < rows[j].order })
	for _, r := range rows {
		s.Properties[r.key] = r.prop
		s.Order = append(s.Order, r.key)
	}
	if len(s.Properties) == 0 {
		return nil
	}
	return s
}

func adaptJSONSchemaObject(cs ConfigSchema) *SettingsSchema {
	propsRaw, _ := cs["properties"].(map[string]any)
	if len(propsRaw) == 0 {
		return nil
	}
	s := &SettingsSchema{Type: "object", Properties: map[string]*SettingsProperty{}}
	order, _ := cs["x-order"].([]any)
	for _, o := range order {
		if k, ok := o.(string); ok {
			s.Order = append(s.Order, k)
		}
	}
	for k, v := range propsRaw {
		pm, ok := v.(map[string]any)
		if !ok {
			continue
		}
		prop, err := fieldToProperty(pm)
		if err != nil {
			continue
		}
		// JSON Schema uses title/description; ASM uses title_key/description_key.
		if t, _ := pm["title"].(string); t != "" {
			prop.Title = t
		}
		if d, _ := pm["description"].(string); d != "" {
			prop.Description = d
		}
		s.Properties[k] = prop
		if !containsString(s.Order, k) {
			s.Order = append(s.Order, k)
		}
	}
	if len(s.Properties) == 0 {
		return nil
	}
	return s
}

func fieldToProperty(fm map[string]any) (*SettingsProperty, error) {
	typ, _ := fm["type"].(string)
	switch typ {
	case "boolean", "integer", "number", "string":
	case "object", "array":
		return nil, fmt.Errorf("nested %s not form-drawable yet", typ)
	default:
		return nil, fmt.Errorf("unknown type %q", typ)
	}
	p := &SettingsProperty{Type: typ}
	if t, _ := fm["title"].(string); t != "" {
		p.Title = t
	}
	if tk, _ := fm["title_key"].(string); tk != "" {
		p.TitleKey = tk
	}
	if d, _ := fm["description"].(string); d != "" {
		p.Description = d
	}
	if dk, _ := fm["description_key"].(string); dk != "" {
		p.DescriptionKey = dk
	}
	if def, ok := fm["default"]; ok {
		p.Default = def
	}
	if min, ok := asFloat(fm["min"]); ok {
		p.Minimum = &min
	}
	if min, ok := asFloat(fm["minimum"]); ok {
		p.Minimum = &min
	}
	if max, ok := asFloat(fm["max"]); ok {
		p.Maximum = &max
	}
	if max, ok := asFloat(fm["maximum"]); ok {
		p.Maximum = &max
	}
	return p, nil
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
