package v2

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSessionBodiesWriter_OutboundBody_RoundTrip closes the read/write
// loop for the V2 compression path's data source.
//
// Why this test exists: TestSessionBodiesWriter_WriteBodies writes an
// OutboundBody but never asserts it comes back, so a regression in the
// outbound_body INSERT column would pass silently. That column is what
// OutboundBuilder.BuildLatestOutbound (the V2OutboundBuilder adapter)
// reads — if it is ever NULL in production, the entire V2 read path
// degrades to "new session" without error. This test pins the contract
// end-to-end: write a known outbound body, read it back via the same
// builder the compressor uses, and assert the marker survives.
//
// Skips without TEST_DB_URL / TEST_DATABASE_URL.
func TestSessionBodiesWriter_OutboundBody_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping database test in short mode")
	}
	pool := setupTestDB(t)
	t.Cleanup(func() { pool.Close() })

	const (
		tenantID  = "test_tenant"
		sessionID = "gw_outbound_roundtrip01"
	)
	writer := NewSessionBodiesWriter(pool)

	// Outbound includes a compression marker — the exact content the V2
	// read path must preserve verbatim for delta-append to work.
	wantOutbound := []Message{
		{Role: "system", Content: "sys prompt"},
		{Role: "assistant", Content: "[smm_v1:cafef00d] prior turns summarized"},
		{Role: "user", Content: "current turn"},
	}
	rec := BodiesRecord{
		SessionID:           sessionID,
		TurnNo:              1,
		TenantID:            tenantID,
		RequestID:           "req_outbound_rt_01",
		Ts:                  time.Now(),
		RequestDelta:        []Message{{Role: "user", Content: "current turn"}},
		ResponseDelta:       []Message{{Role: "assistant", Content: "reply"}},
		OutboundBody:        wantOutbound,
		RequestAttachments:  []AttachmentRef{},
		ResponseAttachments: []AttachmentRef{},
	}
	ctx := context.Background()
	require.NoError(t, writer.WriteBodies(ctx, rec))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM gateway.session_bodies
			WHERE tenant_id=$1 AND session_id=$2`, tenantID, sessionID)
	})

	// Read via GetBodies and assert outbound_body survived the round trip.
	got, err := writer.GetBodies(ctx, tenantID, sessionID, 1)
	require.NoError(t, err)
	require.NotNil(t, got, "GetBodies must return the row we just wrote")
	if assert.Len(t, got.OutboundBody, len(wantOutbound), "outbound_body must not be NULL") {
		for i, m := range wantOutbound {
			assert.Equal(t, m.Role, got.OutboundBody[i].Role, "role mismatch at %d", i)
			assert.Equal(t, m.Content, got.OutboundBody[i].Content, "content mismatch at %d", i)
		}
		assert.Contains(t, got.OutboundBody[1].Content, "[smm_v1:",
			"compression marker must survive the write")
	}

	// And read via the builder the compressor actually uses — proving the
	// data written by the write path is consumable by the read path. This
	// is the integration contract between DualWriter and SessionCompressor.
	builder := NewOutboundBuilder(NewTurnReader(pool))
	bodyJSON, err := builder.BuildLatestOutbound(ctx, tenantID, sessionID)
	require.NoError(t, err)

	var built []Message
	require.NoError(t, json.Unmarshal(bodyJSON, &built))
	require.Len(t, built, len(wantOutbound), "builder must return the same messages we wrote")
	assert.Equal(t, "[smm_v1:cafef00d] prior turns summarized", built[1].Content,
		"builder output must match written outbound_body verbatim")
}
