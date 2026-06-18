package pending

import (
	"testing"
	"time"
)

// TestSplitEntryKey_Valid pins the inverse of entryKey. The
// sweeper relies on this to identify which key is an entry
// hash vs. the ZSET index key.
func TestSplitEntryKey_Valid(t *testing.T) {
	tests := []struct {
		key       string
		wantSid   string
		wantRid   string
		wantOk    bool
	}{
		{"pending_response:sess_abc:req_xyz", "sess_abc", "req_xyz", true},
		{"pending_response:index:sess_abc", "", "", false}, // index key
		{"other_prefix:foo:bar", "", "", false},
		// Empty sid or rid is rejected — the sweeper should not
		// see these in production. We document the rejection
		// rather than accept the corners.
		{"pending_response::req", "", "", false},
		{"pending_response:sess:", "", "", false},
		{"pending_response:", "", "", false},
		{"pending_response:::", "", "", false},
	}
	for _, tt := range tests {
		gotSid, gotRid, gotOk := splitEntryKey(tt.key)
		if gotSid != tt.wantSid || gotRid != tt.wantRid || gotOk != tt.wantOk {
			t.Errorf("splitEntryKey(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.key, gotSid, gotRid, gotOk, tt.wantSid, tt.wantRid, tt.wantOk)
		}
	}
}

// TestListStaleInProgress_NilStore pins the graceful-degrade
// contract. The sweeper must skip a tick cleanly when the
// store is unavailable rather than panic.
func TestListStaleInProgress_NilStore(t *testing.T) {
	s := NewStore(nil, 0)
	_, err := s.ListStaleInProgress(nil, time.Now().Add(-10*time.Minute), 100)
	if err != ErrUnavailable {
		t.Errorf("got %v, want ErrUnavailable", err)
	}
}
