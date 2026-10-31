package pluginruntime

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// SettingsSchema is a plugin's settings as JSON Schema object form.
// Order preserves field order (JSON objects have none).
// Preferred over ConfigSchema for admin form rendering (see Manifest comment).
type SettingsSchema struct {
	Type       string                       `json:"type"` // "object"
	Properties map[string]*SettingsProperty `json:"properties"`
	Order      []string                     `json:"x-order,omitempty"`
}

// SettingsProperty is one setting field.
type SettingsProperty struct {
	Type           string   `json:"type"` // boolean, integer, number, string
	Title          string   `json:"title,omitempty"`
	TitleKey       string   `json:"title_key,omitempty"`
	Description    string   `json:"description,omitempty"`
	DescriptionKey string   `json:"description_key,omitempty"`
	Enum           []any    `json:"enum,omitempty"`
	Minimum        *float64 `json:"minimum,omitempty"`
	Maximum        *float64 `json:"maximum,omitempty"`
	Default        any      `json:"default,omitempty"`
}

// SchemaOf builds a SettingsSchema from a settings struct (magpie tag dialect):
//
//	type Settings struct {
//	  CallsPage int `json:"callsPage" title:"…" description:"…" enum:"10,20,50" min:"1" max:"100"`
//	}
func SchemaOf(v any) (*SettingsSchema, map[string]any) {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		panic(fmt.Sprintf("pluginruntime: settings %T is not a struct", v))
	}
	s := &SettingsSchema{Type: "object", Properties: map[string]*SettingsProperty{}}
	defaults := map[string]any{}
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Type().Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "" || name == "-" {
			continue
		}
		p := &SettingsProperty{
			Title:          f.Tag.Get("title"),
			TitleKey:       f.Tag.Get("title_key"),
			Description:    f.Tag.Get("description"),
			DescriptionKey: f.Tag.Get("description_key"),
		}
		if p.Title == "" && p.TitleKey == "" {
			panic(fmt.Sprintf("pluginruntime: setting %s has no title or title_key", name))
		}
		switch f.Type.Kind() {
		case reflect.Bool:
			p.Type, p.Default = "boolean", rv.Field(i).Bool()
		case reflect.Int, reflect.Int32, reflect.Int64:
			p.Type, p.Default = "integer", rv.Field(i).Int()
		case reflect.Float64:
			p.Type, p.Default = "number", rv.Field(i).Float()
		case reflect.String:
			p.Type, p.Default = "string", rv.Field(i).String()
		default:
			panic(fmt.Sprintf("pluginruntime: setting %s is a %s, which a form can't draw", name, f.Type))
		}
		if e := f.Tag.Get("enum"); e != "" {
			for _, w := range strings.Split(e, ",") {
				x, err := p.parse(strings.TrimSpace(w))
				if err != nil {
					panic(fmt.Sprintf("pluginruntime: setting %s's enum: %v", name, err))
				}
				p.Enum = append(p.Enum, x)
			}
		}
		for tag, to := range map[string]**float64{"min": &p.Minimum, "max": &p.Maximum} {
			if w := f.Tag.Get(tag); w != "" {
				x, err := strconv.ParseFloat(w, 64)
				if err != nil {
					panic(fmt.Sprintf("pluginruntime: setting %s's %s: %v", name, tag, err))
				}
				*to = &x
			}
		}
		if err := p.check(p.Default); err != nil {
			panic(fmt.Sprintf("pluginruntime: setting %s's default: %v", name, err))
		}
		s.Properties[name] = p
		s.Order = append(s.Order, name)
		defaults[name] = p.Default
	}
	return s, defaults
}

func (p *SettingsProperty) parse(w string) (any, error) {
	switch p.Type {
	case "integer":
		return strconv.ParseInt(w, 10, 64)
	case "number":
		return strconv.ParseFloat(w, 64)
	case "boolean":
		return strconv.ParseBool(w)
	}
	return w, nil
}

func (p *SettingsProperty) check(v any) error {
	_, err := p.value(v)
	return err
}

func (p *SettingsProperty) value(v any) (any, error) {
	var out any
	switch p.Type {
	case "boolean":
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%v is not true or false", v)
		}
		out = b
	case "string":
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%v is not text", v)
		}
		out = s
	case "integer", "number":
		var f float64
		switch n := v.(type) {
		case int:
			f = float64(n)
		case int64:
			f = float64(n)
		case float64:
			f = n
		default:
			return nil, fmt.Errorf("%v is not a number", v)
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%v is not a number", v)
		}
		if p.Minimum != nil && f < *p.Minimum || p.Maximum != nil && f > *p.Maximum {
			return nil, fmt.Errorf("%v is out of range", v)
		}
		if p.Type == "integer" {
			if f != math.Trunc(f) {
				return nil, fmt.Errorf("%v is not a whole number", v)
			}
			out = int64(f)
		} else {
			out = f
		}
	default:
		return nil, fmt.Errorf("unsupported setting type %q", p.Type)
	}
	if len(p.Enum) > 0 && !slices.Contains(p.Enum, out) {
		return nil, fmt.Errorf("%v is not one of %v", v, p.Enum)
	}
	return out, nil
}

// Validate returns config coerced to schema types, or the first refused key/value.
func (s *SettingsSchema) Validate(config map[string]any) (map[string]any, error) {
	if s == nil {
		return nil, fmt.Errorf("nil settings schema")
	}
	out := map[string]any{}
	for k, v := range config {
		p := s.Properties[k]
		if p == nil {
			return nil, fmt.Errorf("no setting %q", k)
		}
		x, err := p.value(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = x
	}
	return out, nil
}

// EffectiveSettingsSchema prefers SettingsSchema; else adapts ConfigSchema.
func (m Manifest) EffectiveSettingsSchema() *SettingsSchema {
	if m.SettingsSchema != nil && m.SettingsSchema.Type == "object" && len(m.SettingsSchema.Properties) > 0 {
		return m.SettingsSchema
	}
	if adapted := AdaptConfigSchema(m.ConfigSchema); adapted != nil {
		return adapted
	}
	return m.SettingsSchema
}
