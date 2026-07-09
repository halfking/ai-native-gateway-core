package executors

import "testing"

func TestStickyHitForChosen(t *testing.T) {
	tests := []struct {
		name       string
		stickyID   *int
		chosenID   int
		wantNil    bool
		wantHitVal bool
	}{
		{name: "no sticky binding", stickyID: nil, chosenID: 1, wantNil: true},
		{name: "sticky hit", stickyID: intPtr(7), chosenID: 7, wantHitVal: true},
		{name: "sticky miss", stickyID: intPtr(7), chosenID: 9, wantHitVal: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stickyHitForChosen(tt.stickyID, tt.chosenID)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("stickyHitForChosen() = %v, want nil", *got)
				}
				return
			}
			if got == nil {
				t.Fatal("stickyHitForChosen() = nil, want non-nil")
			}
			if *got != tt.wantHitVal {
				t.Fatalf("stickyHitForChosen() = %v, want %v", *got, tt.wantHitVal)
			}
		})
	}
}
