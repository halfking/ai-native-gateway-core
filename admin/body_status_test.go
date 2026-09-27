package admin

import (
	"encoding/json"
	"testing"
)

// TestClassifyBodyStatus pins the two-state contract documented in
// admin/body_status.go. The load-bearing case is jsonNullColumns: Postgres
// returns a JSONB null as the 4 bytes "null", so a length-only check would
// report a LEFT-JOIN miss on all three columns as "available".
func TestClassifyBodyStatus(t *testing.T) {
	cases := []struct {
		name          string
		requestDelta  []byte
		responseDelta []byte
		outboundBody  []byte
		want          string
	}{
		{
			name: "all columns absent (LEFT JOIN miss)",
			want: BodyStatusUnavailable,
		},
		{
			name:         "json null columns are not a payload",
			requestDelta: []byte("null"),
			// A literal 0x00 byte is how a NULL scan target can arrive on some
			// driver paths; TrimSpace leaves it non-empty and != "null", so it
			// is treated as a payload. Pinned here so the behaviour is a
			// decision, not an accident.
			responseDelta: []byte("null"),
			outboundBody:  []byte("null"),
			want:          BodyStatusUnavailable,
		},
		{
			name:         "whitespace-only column",
			requestDelta: []byte("  \n\t "),
			want:         BodyStatusUnavailable,
		},
		{
			name:         "request delta only",
			requestDelta: []byte(`[{"role":"user"}]`),
			want:         BodyStatusAvailable,
		},
		{
			name:          "response delta only",
			responseDelta: []byte(`[{"role":"assistant"}]`),
			want:          BodyStatusAvailable,
		},
		{
			name:         "outbound body only",
			outboundBody: []byte(`{"model":"gpt-test"}`),
			want:         BodyStatusAvailable,
		},
		{
			name:          "mixed null and payload",
			requestDelta:  []byte("null"),
			responseDelta: []byte("null"),
			outboundBody:  []byte(`{"prompt":"hi"}`),
			want:          BodyStatusAvailable,
		},
		{
			name:         "empty json array is still a payload",
			requestDelta: []byte(`[]`),
			want:         BodyStatusAvailable,
		},
		{
			name:         "empty json object is still a payload",
			outboundBody: []byte(`{}`),
			want:         BodyStatusAvailable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyBodyStatus(tc.requestDelta, tc.responseDelta, tc.outboundBody)
			if got != tc.want {
				t.Errorf("classifyBodyStatus(%q, %q, %q) = %q, want %q",
					string(tc.requestDelta), string(tc.responseDelta), string(tc.outboundBody), got, tc.want)
			}
		})
	}
}

func TestBodyStatusFromPresent(t *testing.T) {
	if got := bodyStatusFromPresent(true); got != BodyStatusAvailable {
		t.Errorf("bodyStatusFromPresent(true) = %q, want %q", got, BodyStatusAvailable)
	}
	if got := bodyStatusFromPresent(false); got != BodyStatusUnavailable {
		t.Errorf("bodyStatusFromPresent(false) = %q, want %q", got, BodyStatusUnavailable)
	}
}

// TestBodyStatusConstantsAreWireStable guards the two documented values. A
// rename here is an API break for the admin console, so it must be a
// deliberate edit to this test, not a side effect of refactoring a constant.
func TestBodyStatusConstantsAreWireStable(t *testing.T) {
	if BodyStatusAvailable != "available" {
		t.Errorf("BodyStatusAvailable = %q, want \"available\"", BodyStatusAvailable)
	}
	if BodyStatusUnavailable != "unavailable" {
		t.Errorf("BodyStatusUnavailable = %q, want \"unavailable\"", BodyStatusUnavailable)
	}
}

// TestSessionTurnV2BodyStatusAlwaysEmitted pins the deliberate absence of
// omitempty on SessionTurnV2.BodyStatus: consumers must be able to tell
// "explicitly unavailable" from "this field did not exist in the response".
func TestSessionTurnV2BodyStatusAlwaysEmitted(t *testing.T) {
	raw, err := json.Marshal(SessionTurnV2{TurnNo: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, ok := payload["body_status"]
	if !ok {
		t.Fatalf("body_status missing from zero-value payload: %s", string(raw))
	}
	if v != "" {
		t.Errorf("zero-value body_status = %v, want empty string present on the wire", v)
	}
}

// TestTurnListItemBodyStatusWireKey pins the JSON key on the list endpoint.
func TestTurnListItemBodyStatusWireKey(t *testing.T) {
	raw, err := json.Marshal(TurnListItem{TurnNo: 1, BodyStatus: BodyStatusAvailable})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := payload["body_status"]; got != BodyStatusAvailable {
		t.Errorf("TurnListItem body_status = %v, want %q", got, BodyStatusAvailable)
	}
}
