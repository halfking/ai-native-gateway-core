package modelcatalog

import "testing"

func TestPreserveManualDisable(t *testing.T) {
	manual := "manual"
	auto := "auto_probe_failed"
	deleted := "deleted"

	tests := []struct {
		name     string
		avail    bool
		reason   *string
		preserve bool
	}{
		{"manual disabled", false, &manual, true},
		{"manual prefix variant", false, strPtr("manual_admin"), true},
		{"deleted legacy soft clear", false, &deleted, false},
		{"auto disabled", false, &auto, false},
		{"available with reason", true, &manual, false},
		{"available no reason", true, nil, false},
		{"disabled no reason", false, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreserveManualDisable(tt.avail, tt.reason)
			if got != tt.preserve {
				t.Fatalf("PreserveManualDisable(%v, %v) = %v, want %v", tt.avail, tt.reason, got, tt.preserve)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
