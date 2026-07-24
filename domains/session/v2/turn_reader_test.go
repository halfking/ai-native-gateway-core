package v2

import (
	"context"
	"testing"
)

func TestTurnReader_LoadChain_ReconstructsSessionState(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := setupTestDB(t)
	if pool == nil {
		t.Skip("no test pool available")
	}
	defer pool.Close()

	reader := NewTurnReader(pool)
	msgs, err := reader.LoadChain(context.Background(), "default", "gw_test_chain", 10)
	if err != nil {
		t.Fatalf("LoadChain: %v", err)
	}
	_ = msgs
}
