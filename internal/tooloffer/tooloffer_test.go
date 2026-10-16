package tooloffer

import (
	"context"
	"testing"
)

func TestFromRequestOnlyExecutableDefinitions(t *testing.T) {
	names := FromRequest([]byte(`{"tools":[
  {"type":"function","function":{"name":"nested"}},
  {"type":"function","name":"flat"},
  {"name":"anthropic","input_schema":{}},
  {"type":"custom_tool","name":"custom_anthropic","input_schema":{}},
  {"type":"custom","custom":{"name":"custom_chat"}},
  {"type":"custom","name":"custom_responses"},
  {"type":"web_search","name":"built_in","function":{"name":"fake_function"}},
  {"type":"namespace","name":"functions","tools":[{"type":"function","name":"member"}]},
  {"name":" padded "}, {"type":"function","function":{"name":""}}
 ],"tool_ids":["not_a_definition"],"messages":[{"tool_calls":[{"function":{"name":"historical"}}]}]}`))
	for _, name := range []string{"nested", "flat", "anthropic", "custom_anthropic"} {
		if !names.Has(name) {
			t.Errorf("missing real offered name %q", name)
		}
	}
	for _, name := range []string{"built_in", "fake_function", "functions", "member", "not_a_definition", "historical", "custom_chat", "custom_responses", "Nested", " padded ", ""} {
		if names.Has(name) {
			t.Errorf("unauthorized name %q", name)
		}
	}
	if names.Len() != 4 {
		t.Fatalf("count=%d", names.Len())
	}
	ctx := WithNames(context.Background(), names)
	if !FromContext(ctx).Has("nested") || FromContext(context.Background()).Len() != 0 {
		t.Fatal("request-local context set mismatch")
	}
}

func TestMalformedAndMissingOffersAreEmpty(t *testing.T) {
	for _, body := range []string{`{`, `null`, `{}`, `{"tools":null}`, `{"tools":{}}`, `{"tools":["name"]}`, `{"tools":[{"type":"function","function":{"name":7}}]}`} {
		if got := FromRequest([]byte(body)); got.Len() != 0 {
			t.Errorf("invalid offer %s produced names", body)
		}
	}
}
