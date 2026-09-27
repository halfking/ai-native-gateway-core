package provider

import (
	"reflect"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/endpointselect"
)

// r0926 audit finding #4: the pre-existing candidate_query_sql_shape_test.go
// pins the SQL TEXT (that the LATERAL exists and its filter clauses are
// intact) but never checked the other side of the wire contract — that the
// jsonb_build_object keys in that SQL match the JSON tags on
// endpointselect.EndpointLite, which the scan unmarshals straight into.
//
// That gap is unusually dangerous here because the failure is silent. Rename
// 'base_url' in the struct and the shape test still passes, but every
// endpoint now decodes with BaseURL="" — so pickBest either skips the row or
// selects an endpoint with an empty upstream URL, and requests quietly fall
// back to Stage 4 (legacy primary). Nothing errors, nothing logs, the canary
// looks enabled, and the feature simply does nothing.

// TestDecodeNativeEndpoints_SQLProjectionRoundTrip is the load-bearing test:
// it builds a JSONB payload whose keys are copied from the LIVE SQL
// projection, decodes it, and asserts every field survived. If someone edits
// either side, this fails.
func TestDecodeNativeEndpoints_SQLProjectionRoundTrip(t *testing.T) {
	// Shaped exactly like jsonb_agg(jsonb_build_object(...)) emits:
	// snake_case keys, booleans as JSON bools, weight as a number.
	payload := []byte(`[
	  {"id": 41, "protocol": "ollama-native", "base_url": "http://ollama:11434",
	   "is_primary": true, "vendor_native": "ollama", "enabled": true,
	   "weight": 300, "health_status": "healthy"},
	  {"id": 42, "protocol": "openai-completions", "base_url": "http://gw:8000/v1",
	   "is_primary": false, "vendor_native": "", "enabled": true,
	   "weight": 100, "health_status": "unknown"}
	]`)

	eps, err := decodeNativeEndpoints(payload, 7)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(eps) != 2 {
		t.Fatalf("decoded %d endpoints, want 2", len(eps))
	}

	want := []endpointselect.EndpointLite{
		{ID: 41, Protocol: "ollama-native", BaseURL: "http://ollama:11434",
			IsPrimary: true, VendorNative: "ollama", Enabled: true,
			Weight: 300, HealthStatus: "healthy"},
		{ID: 42, Protocol: "openai-completions", BaseURL: "http://gw:8000/v1",
			IsPrimary: false, VendorNative: "", Enabled: true,
			Weight: 100, HealthStatus: "unknown"},
	}
	for i := range want {
		if eps[i] != want[i] {
			t.Errorf("endpoint[%d] = %+v\nwant %+v\n"+
				"(a zero BaseURL/Protocol here means the SQL keys and the struct tags drifted apart)",
				i, eps[i], want[i])
		}
	}
}

// TestDecodeNativeEndpoints_SQLKeysMatchStructTags walks the actual SQL text
// and asserts every key the projection emits has a matching JSON tag on
// EndpointLite. This is the drift alarm the round-trip test above cannot see,
// because that test hard-codes its own copy of the keys.
func TestDecodeNativeEndpoints_SQLKeysMatchStructTags(t *testing.T) {
	q := stripSQLComments(candidateQuerySQL())
	start := strings.Index(q, "jsonb_build_object(")
	if start < 0 {
		t.Fatal("jsonb_build_object projection not found in candidateQuerySQL")
	}
	end := strings.Index(q[start:], "ORDER BY")
	if end < 0 {
		t.Fatal("projection terminator not found")
	}
	projection := q[start : start+end]

	// Every key the SQL projects, in order.
	sqlKeys := extractQuotedKeys(projection)
	if len(sqlKeys) == 0 {
		t.Fatal("no keys extracted from the projection — parser is broken, not the SQL")
	}

	// The tag set the decoder can actually populate.
	tagSet := make(map[string]bool)
	for _, tag := range endpointselectEndpointLiteJSONTags() {
		tagSet[tag] = true
	}
	for _, k := range sqlKeys {
		if !tagSet[k] {
			t.Errorf("SQL projects key %q but endpointselect.EndpointLite has no matching json tag — "+
				"it would decode to the zero value and the endpoint would be silently ignored", k)
		}
	}
	if len(sqlKeys) != len(tagSet) {
		t.Errorf("projection has %d keys but EndpointLite has %d tags — "+
			"one side gained/lost a field; update both (selector.go: EndpointLite)",
			len(sqlKeys), len(tagSet))
	}
}

func TestDecodeNativeEndpoints_EmptyAndMalformed(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    int
		wantErr bool
	}{
		// A provider with no endpoint subtable rows: the LATERAL yields
		// COALESCE(...,'[]'::jsonb), and a nil scan is also tolerated.
		{"nil payload (no subtable rows)", ``, 0, false},
		{"empty array", `[]`, 0, false},
		{"json null", `null`, 0, false},
		{"single endpoint", `[{"id":1,"protocol":"ollama-native","base_url":"http://a","enabled":true,"weight":100,"is_primary":true,"vendor_native":"ollama","health_status":"unknown"}]`, 1, false},
		// Corrupt payload must fail the candidate load, not silently yield
		// zero endpoints (which would look identical to "no endpoints" and
		// quietly degrade the request to Stage 4).
		{"truncated json", `[{"id":1,`, 0, true},
		{"not an array", `{"id":1}`, 0, true},
		{"wrong type for id", `[{"id":"forty-one"}]`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eps, err := decodeNativeEndpoints([]byte(tc.payload), 99)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %d endpoints", len(eps))
				}
				// The credential id must survive into the error: candidate
				// loads are per-credential and the id is the only handle an
				// operator has on a bad row.
				if !strings.Contains(err.Error(), "99") {
					t.Errorf("error must name the credential id, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(eps) != tc.want {
				t.Errorf("decoded %d endpoints, want %d", len(eps), tc.want)
			}
		})
	}
}

// extractQuotedKeys pulls the 'key' literals out of a jsonb_build_object(...)
// projection, in source order. Deliberately a narrow lexer rather than a
// regex over the whole query: we only care about the single call's argument
// list, and a loose regex would happily match string literals from a
// neighbouring expression.
func extractQuotedKeys(projection string) []string {
	var keys []string
	inCall := false
	for i := 0; i < len(projection); i++ {
		if !inCall {
			if strings.HasPrefix(projection[i:], "jsonb_build_object(") {
				inCall = true
				i += len("jsonb_build_object(") - 1
			}
			continue
		}
		if projection[i] == '\'' {
			j := strings.IndexByte(projection[i+1:], '\'')
			if j < 0 {
				break // unterminated literal: stop rather than invent a key
			}
			// Skip empty literals. The projection contains COALESCE(pep.
			// vendor_native, '') — the fallback is a zero-length string, not
			// a column key, and treating it as one would both invent a
			// phantom mismatch and desynchronize the two counts below.
			if j > 0 {
				keys = append(keys, projection[i+1:i+1+j])
			}
			i += j + 1
		}
	}
	return keys
}

// endpointselectEndpointLiteJSONTags returns the wire keys EndpointLite can
// populate, read by reflection so the test tracks the struct automatically
// instead of freezing a copy of the tag list.
func endpointselectEndpointLiteJSONTags() []string {
	t := reflect.TypeOf(endpointselect.EndpointLite{})
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		if comma := strings.Index(tag, ","); comma >= 0 {
			tag = tag[:comma]
		}
		out = append(out, tag)
	}
	return out
}
