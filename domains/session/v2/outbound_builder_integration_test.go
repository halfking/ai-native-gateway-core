package v2

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestOutboundBuilder_BuildLatestOutbound_RealDB is the integration guard
// for the V2 compression read path's data layer. It inserts a
// session_bodies row and verifies OutboundBuilder.BuildLatestOutbound
// (the method backing the compression.V2OutboundBuilder interface)
// reads it back intact, including compression markers.
//
// Skips when TEST_DB_URL is unset, matching the rest of the v2 suite.
// This is the only test that exercises the real SQL the compression
// V2 path will issue in production; the stub-based tests in the
// compression package cover only control flow.
func TestOutboundBuilder_BuildLatestOutbound_RealDB(t *testing.T) {
	pool := setupTestDB(t)
	t.Cleanup(func() { pool.Close() })

	ctx := context.Background()
	const (
		tenantID  = "test_tenant"
		sessionID = "gw_build_latest_db01"
	)
	now := time.Now().UTC()

	// One user turn and a compression-marker-bearing assistant turn — the
	// exact shape the compressor persists and BuildLatestOutbound must
	// preserve verbatim.
	outbound := []Message{
		{Role: "user", Content: "round one user msg"},
		{Role: "assistant", Content: "[smm_v1:deadbeef] prior context summarized"},
	}
	outboundJSON, err := json.Marshal(outbound)
	require.NoError(t, err)

	// Wipe any prior fixture for this session, then insert one body row.
	// We insert into session_bodies directly because that is the table
	// LoadLatestOutbound reads; session_turns is not required for this
	// read path.
	_, err = pool.Exec(ctx, `
		DELETE FROM gateway.session_bodies
		WHERE tenant_id = $1 AND session_id = $2`,
		tenantID, sessionID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO gateway.session_bodies
		    (session_id, turn_no, partition_date, tenant_id, request_id,
		     request_delta, response_delta, outbound_body, ts)
		VALUES ($1, 1, $2, $3, $4, NULL, NULL, $5, $6)`,
		sessionID, now.Format("2006-01-02"), tenantID,
		"req_build_latest_01", outboundJSON, now)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM gateway.session_bodies
			WHERE tenant_id=$1 AND session_id=$2`, tenantID, sessionID)
	})

	reader := NewTurnReader(pool)
	builder := NewOutboundBuilder(reader)

	got, err := builder.BuildLatestOutbound(ctx, tenantID, sessionID)
	require.NoError(t, err, "BuildLatestOutbound should read the fixture row")

	// Round-trip the JSON body back to messages and compare structurally.
	var gotMsgs []Message
	require.NoError(t, json.Unmarshal(got, &gotMsgs))
	require.Len(t, gotMsgs, 2, "expected both messages preserved")
	require.Equal(t, "user", gotMsgs[0].Role)
	require.Equal(t, "round one user msg", gotMsgs[0].Content)
	require.Equal(t, "assistant", gotMsgs[1].Role)
	require.Equal(t, "[smm_v1:deadbeef] prior context summarized", gotMsgs[1].Content,
		"compression marker must survive the round trip")
}

// TestOutboundBuilder_BuildLatestOutbound_RealDB_NoRows covers the
// empty-session case against a real database: a session with no body
// rows must yield an empty (but non-error) JSON array, which the
// compression layer interprets as "new session".
func TestOutboundBuilder_BuildLatestOutbound_RealDB_NoRows(t *testing.T) {
	pool := setupTestDB(t)
	t.Cleanup(func() { pool.Close() })

	ctx := context.Background()
	const (
		tenantID  = "test_tenant"
		sessionID = "gw_build_latest_empty01"
	)

	// Ensure clean slate.
	_, err := pool.Exec(ctx, `DELETE FROM gateway.session_bodies
		WHERE tenant_id=$1 AND session_id=$2`, tenantID, sessionID)
	require.NoError(t, err)

	builder := NewOutboundBuilder(NewTurnReader(pool))
	got, err := builder.BuildLatestOutbound(ctx, tenantID, sessionID)
	require.NoError(t, err, "missing rows is not an error")

	var gotMsgs []Message
	require.NoError(t, json.Unmarshal(got, &gotMsgs))
	require.Empty(t, gotMsgs, "no rows → empty message array")
}

// TestSessionTurnsReader_LoadState_RealDB_NoRows guards the regression where
// SessionTurnsReader.LoadState wrapped pgx.ErrNoRows and returned (nil, err).
// HasState → Get → LoadState relies on LoadState returning (nil, nil) for a
// brand-new session so the caller classifies it as "no prior state" (ok=true,
// fresh session) instead of a hard error that forces a V1 fallback.
func TestSessionTurnsReader_LoadState_RealDB_NoRows(t *testing.T) {
	pool := setupTestDB(t)
	t.Cleanup(func() { pool.Close() })

	ctx := context.Background()
	const (
		tenantID  = "test_tenant"
		sessionID = "gw_loadstate_norows01"
	)
	// No session_turns rows exist for this session.
	_, err := pool.Exec(ctx, `DELETE FROM gateway.session_turns
		WHERE tenant_id=$1 AND session_id=$2`, tenantID, sessionID)
	require.NoError(t, err)

	reader := NewSessionTurnsReader(pool)
	state, err := reader.LoadState(ctx, tenantID, sessionID)
	require.NoError(t, err, "ErrNoRows must surface as (nil,nil), not an error")
	require.Nil(t, state, "no prior state → nil state, nil error")
}
