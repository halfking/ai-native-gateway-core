// Package reducer contains the pure StateReducer for URSM v2.
package reducer

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
)

func TestAdminDominatesProbe(t *testing.T) {
	cur := api.NodeView{Generation: 10, SrcPriority: api.SourcePriorityProbe, Available: true}
	ev := api.RequestOutcome{Success: false, ErrorKind: "timeout"}
	dec := Apply(ev, cur)
	if !dec.Accepted {
		t.Fatalf("admin action must still be accepted despite failed request")
	}
}

func TestRequestCannotOverrideManualDisable(t *testing.T) {
	cur := api.NodeView{Generation: 11, SrcPriority: api.SourcePriorityAdmin, Available: false}
	ev := api.RequestOutcome{Success: true}
	dec := Apply(ev, cur)
	if dec.Accepted {
		t.Fatalf("request must not override admin disabled")
	}
	if cur.Available {
		t.Fatalf("manual disable must remain unavailable")
	}
}

func TestFreeBillingTransientDoesNotHardExclude(t *testing.T) {
	cur := api.NodeView{Generation: 1, SrcPriority: api.SourcePrioritySeed, Available: true, FailStreak: 4}
	ev := api.RequestOutcome{Success: false, ErrorKind: "timeout", BillingMode: "free"}
	dec := Apply(ev, cur)
	if !dec.Accepted {
		t.Fatalf("transient on free must still be accepted (soft demote only)")
	}
	if !dec.NodeView.Available {
		t.Fatalf("free transient must not flip available=false")
	}
}

func TestPermanentErrorHardExcludes(t *testing.T) {
	cur := api.NodeView{Generation: 1, SrcPriority: api.SourcePrioritySeed, Available: true, FailStreak: 1}
	ev := api.RequestOutcome{Success: false, ErrorKind: "auth_failed"}
	dec := Apply(ev, cur)
	if dec.NodeView.Available {
		t.Fatalf("auth_failed must hard-exclude")
	}
}
