package api

import "testing"

func TestSourcePriorityOrdering(t *testing.T) {
	if SourcePriorityRequest >= SourcePriorityProbe {
		t.Fatalf("probe must dominate request")
	}
	if SourcePriorityProbe >= SourcePriorityAdmin {
		t.Fatalf("admin must dominate probe")
	}
}
