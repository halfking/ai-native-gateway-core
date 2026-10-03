//go:build integration

package sessionv2mirror

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// TestSearchTextLandsInSessionTurns_RealDB is the real-database half of the
// §9.98 chain gate (search_text_chain_test.go).
//
// The three chain gates prove the *linkage*: SearchText is mapped in
// applyStorageS1AFields, carried on ProcessedRequest, and bound in the writer's
// INSERT. None of them proves the value *reaches storage*. §9.97 measured
// session_turns.search_text at 0% fill on 252 while the column existed and the
// INSERT was already bound — the linkage was present and the value still was
// not there. This test is what distinguishes those two worlds.
//
// Anti-vacuity, deliberately: the obvious assertion here is
//
//	stored == *telemetry.SearchText(entry)
//
// which is a tautology whenever both sides are empty — precisely the pre-§9.98
// world, where the whole defect was "both sides are empty". So this test never
// asserts equality alone. It first pins that the pure function produced real
// content, then asserts each distinctive token is present in the value read
// back out of the database. An empty-on-both-sides state is red here.
//
// Requires TEST_DB_URL pointing at a DISPOSABLE database carrying the current
// schema (repo convention, same knob as hook_integration_test.go).
//
//	TEST_DB_URL='postgres://llm_gateway:***@127.0.0.1:55432/gw_fresh_test' \
//	  go test -tags=integration \
//	    -run TestSearchTextLandsInSessionTurns_RealDB \
//	    ./internal/sessionv2mirror/ -v -count=1
func TestSearchTextLandsInSessionTurns_RealDB(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	// One unique token per field that telemetry.searchText concatenates, so a
	// partial write (some fields dropped) is distinguishable from a full one.
	// The prefix is per-run, so a stale row from an earlier run can never make
	// this pass by accident.
	run := fmt.Sprintf("%d", time.Now().UnixNano())
	sessionID := "ses_" + run // doubles as the GwSessionID search_text token
	gwTaskID := "tsk_" + run
	clientModel := "clm_" + run
	outboundModel := "omb_" + run
	clientProfile := "clp_" + run
	requestMode := "rmd_" + run
	apiKeyPrefix := "akp_" + run
	apiKeyOwnerUser := "aku_" + run
	applicationCode := "app_" + run

	tokens := map[string]string{
		"ClientModel":     clientModel,
		"OutboundModel":   outboundModel,
		"ClientProfile":   clientProfile,
		"RequestMode":     requestMode,
		"GwSessionID":     sessionID,
		"GwTaskID":        gwTaskID,
		"APIKeyPrefix":    apiKeyPrefix,
		"APIKeyOwnerUser": apiKeyOwnerUser,
		"ApplicationCode": applicationCode,
	}

	tenantID := "test_tenant"
	requestID := fmt.Sprintf("st_e2e_req_%d", time.Now().UnixNano())
	defer cleanupTestV2Data(t, db, sessionID, tenantID)

	now := time.Now()
	body := `{"messages":[{"role":"user","content":"search text e2e"}]}`

	entry := &telemetry.RequestLogEntry{
		RequestID:       requestID,
		GwSessionID:     &sessionID,
		GwTaskID:        &gwTaskID,
		TenantID:        tenantID,
		ClientModel:     &clientModel,
		OutboundModel:   &outboundModel,
		ClientProfile:   &clientProfile,
		RequestMode:     &requestMode,
		APIKeyPrefix:    &apiKeyPrefix,
		APIKeyOwnerUser: &apiKeyOwnerUser,
		ApplicationCode: &applicationCode,
		ProviderID:      intPtr(36),
		CredentialID:    intPtr(42),
		Success:         true,
		EventAt:         &now,
		LatencyMs:       intPtr(500),
		PromptTokens:    intPtr(100),
		RequestBody:     &body,
	}

	// Control #1 — the pure function produced real content. Without this, every
	// downstream assertion could be satisfied by two empty strings.
	expected := telemetry.SearchText(entry)
	require.NotNil(t, expected, "telemetry.SearchText must not return nil for a populated entry")
	require.NotEmpty(t, strings.TrimSpace(*expected),
		"telemetry.SearchText returned empty; every storage assertion below would be vacuous")

	turnWriter := v2.NewTurnWriter(db)
	bodiesWriter := v2.NewSessionBodiesWriter(db)
	aggregator := v2.NewSessionAggregator(db)
	turnLogsWriter := v2.NewTurnLogsWriter(db)
	writer := v2.NewSessionWriterV2(turnWriter, bodiesWriter, aggregator, turnLogsWriter)

	req := entryToProcessedRequest(entry, sessionID)
	require.NotNil(t, req)
	require.Equal(t, *expected, req.SearchText,
		"mirror-side mapping must reproduce the v1 pure function byte for byte")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, writer.Write(ctx, req), "writer.Write should succeed")

	// Read back out of storage. session_turns_hot is an independent storage
	// plane, not a partition of session_turns (see §9.59), and the mirror only
	// writes hot — rows reach the session_turns parent asynchronously via the
	// promotion job (on 252 the parent lags hot by hours). So the parent is
	// deliberately NOT asserted here: asserting it would encode a false
	// expectation about a periodic job, not about §9.98. What must carry the
	// value immediately is hot, plus the consumer-facing projection.
	var hotText string
	err := db.QueryRow(ctx,
		`SELECT COALESCE(search_text, '') FROM public.session_turns_hot
		 WHERE request_id = $1 AND tenant_id = $2`,
		requestID, tenantID).Scan(&hotText)
	require.NoError(t, err, "expected the mirrored turn in session_turns_hot")

	// The consumer-facing projection is NOT asserted, and that omission is
	// deliberate and load-bearing: session_turns_with_current_month projects 55
	// curated columns and does not include search_text at all (verified on both
	// a fresh install and 252). Search today still runs against the v1 family —
	// admin/logs.go selects rl.search_text with rl = request_logs_hot — so the
	// write side being fixed does NOT mean retrieval works on the session side.
	// Adding a view assertion here would either fail for a reason that has
	// nothing to do with §9.98, or (worse) pin the gap in as expected behaviour.
	// The view projection is tracked as the still-open read-side half of the
	// v1 retirement blocker.
	var viewHasSearchText bool
	err = db.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name   = 'session_turns_with_current_month'
			  AND column_name  = 'search_text')`).Scan(&viewHasSearchText)
	require.NoError(t, err)
	t.Logf("session_turns_with_current_month exposes search_text: %v (read-side migration still open)", viewHasSearchText)

	for _, s := range []struct{ surface, value string }{
		{"session_turns_hot", hotText},
	} {
		require.NotEmpty(t, s.value,
			"%s.search_text is empty — §9.98 is not in effect end to end", s.surface)
		require.Equal(t, *expected, s.value,
			"%s.search_text differs from the v1 pure-function value", s.surface)
		for field, token := range tokens {
			require.Contains(t, s.value, token,
				"%s.search_text is missing the %s token (partial write?)", s.surface, field)
		}
	}

	t.Logf("search_text persisted to session_turns_hot: %q", *expected)
}
