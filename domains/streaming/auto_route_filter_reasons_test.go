package streaming

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// TestDecisionToWire_FilterReasons pins the RT-1 wire contract: gate filter
// reasons surface as filter_reasons in X-Gw-Auto-Decision /
// request_logs.auto_decision, and a gate-off decision serialises WITHOUT the
// field (omitempty) so the payload stays byte-identical to pre-RT-1.
func TestDecisionToWire_FilterReasons(t *testing.T) {
	t.Run("gate off omits the field", func(t *testing.T) {
		wire := decisionToWire(&autoroute.Decision{ChosenModel: "m"})
		b, err := json.Marshal(wire)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(b), "filter_reasons") {
			t.Errorf("gate-off wire must not contain filter_reasons, got %s", b)
		}
	})

	t.Run("gate on carries the reasons", func(t *testing.T) {
		wire := decisionToWire(&autoroute.Decision{
			ChosenModel:   "m",
			FilterReasons: []string{"standard_iq_below_min: model=x iq=3.0 min=50.0"},
		})
		if len(wire.FilterReasons) != 1 {
			t.Fatalf("FilterReasons: want 1, got %v", wire.FilterReasons)
		}
		if !strings.Contains(wire.FilterReasons[0], "standard_iq_below_min") {
			t.Errorf("unexpected reason %q", wire.FilterReasons[0])
		}
	})
}
