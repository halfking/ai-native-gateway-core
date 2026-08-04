package v2

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

func TestNodeInvalidationChannel(t *testing.T) {
	if got := store.NodeInvalidationChannel("ursm:v2:"); got != "ursm:v2:meta:node_invalidation" {
		t.Fatalf("channel=%q", got)
	}
}
