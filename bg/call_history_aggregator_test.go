package bg

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/credentialhealth"
)

// TestAggregateSkipsIndexKey — the recorder's index set
// (llmgw:callhist:index) matches the llmgw:callhist:* SCAN glob but is not a
// per-(credential,model) data key. The aggregator must skip it silently
// instead of WARN-failing on every tick (245 2026-09-16 audit: ~1.4k
// WARNs/day). Indirect assertion: the index key must be recognised as the
// recorder's index (so a rename on either side flips this test), and
// parseCallHistKey must reject it.
func TestAggregateSkipsIndexKey(t *testing.T) {
	indexKey := credentialhealth.CallHistoryIndexKey()
	if indexKey != "llmgw:callhist:index" {
		t.Fatalf("unexpected index key constant: %q", indexKey)
	}
	if _, _, ok := parseCallHistKey(indexKey); ok {
		t.Fatalf("index key %q must not parse as a data key", indexKey)
	}
	if _, _, ok := parseCallHistKey("llmgw:callhist:59:claude-sonnet-4-5"); !ok {
		t.Fatalf("data key must still parse")
	}
}
