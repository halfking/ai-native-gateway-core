package routing

import (
	"testing"
)

// TestIsBenignEOFStreamingInterruption documents the heuristic used by
// the executor to distinguish a MiniMax-style "stream completed without
// [DONE]" from a real concurrent-overload event. Benign eofs with chunks
// already sent must NOT trigger credential cooling.
func TestIsBenignEOFStreamingInterruption(t *testing.T) {
	tests := []struct {
		name       string
		reason     string
		chunkCount int
		want       bool
	}{
		{"benign: eof without [DONE] with chunks", "eof_without_done", 5, true},
		{"benign: single chunk eof", "eof_without_done", 1, true},
		{"not benign: zero chunks", "eof_without_done", 0, false},
		{"not benign: different reason with chunks", "stream_timeout", 5, false},
		{"not benign: client cancel", "client_cancel", 3, false},
		{"not benign: explicit overload", "concurrent limit exceeded", 5, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.reason == "eof_without_done" && tc.chunkCount > 0
			if got != tc.want {
				t.Errorf("reason=%q chunkCount=%d: got %v, want %v",
					tc.reason, tc.chunkCount, got, tc.want)
			}
		})
	}
}
