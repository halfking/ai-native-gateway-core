//go:build integration

package sessionv2mirror

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

// TestSearchTextRoundTripsThroughTheNativeReadSource_RealDB is the READ half of
// the §9.98 chain, and it closes a question the write-side test left open in a
// misleading way.
//
// # WHY THIS TEST EXISTS
//
// TestSearchTextLandsInSessionTurns_RealDB (same file's sibling) proves the
// value reaches storage. Its closing comment says:
//
//	"The consumer-facing projection is NOT asserted …
//	 session_turns_with_current_month projects 55 curated columns and does not
//	 include search_text at all … The view projection is tracked as the
//	 still-open read-side half of the v1 retirement blocker."
//
// The first sentence is TRUE and was re-verified on a real fresh install
// (55 columns, 0 of them search_text). The INFERENCE is what misleads: it reads
// as "the session side cannot serve search, and the missing column is why".
//
// It cannot. The grey-release switch does not read that view. admin/logs.go's
// FROM source is chosen by admin/logs_turns_source.go:logsSourceFromSQL(), and
// with storage.admin_logs_native_turns_read=true it substitutes
// db.SessionFamilyTurnsSourceSQL() — an INLINE projection over
// session_turns_hot/session_turns, not a traversal of
// session_turns_with_current_month. That projection is rendered from
// canonicalColumnOrderV2 (115 names) through projectionExprByColumn, and
// canonicalColumnOrderV2[21] == "search_text" maps to "t.search_text"
// (projectionExprsV2[21]). So the column is present, positionally aligned.
//
// What actually blocks migrating search to the session family is ROW
// COVERAGE, not schema — see admin/logs_turns_source.go: 27.6% of request_ids in
// the canonical view have no row in the native source, because traffic with no
// session head (probes/self-checks by design, in_progress placeholders, title
// and summary generator loops) is never mirrored. That is a business decision
// about non-session traffic, and it is already registered as such. Widening
// session_turns_with_current_month would not move that number by one row.
//
// So this test pins the thing that is actually load-bearing and could silently
// regress: that the native read source still carries search_text, and that a
// value written through the production writer comes back byte-for-byte through
// that source under the same ILIKE filter admin/logs.go uses.
//
// # WHAT IT DELIBERATELY DOES NOT ASSERT
//
// It does NOT assert that session_turns_with_current_month LACKS search_text.
// Asserting an absence would be a new false constraint: widening that view is a
// legitimate future change (it is the D1 question), and pinning its absence
// would turn that decision into a red test for no benefit. The clarification
// belongs in a comment; the guard belongs on the positive property.
//
// # ANTI-VACUITY
//
// The token is unique per run, and the test asserts non-empty BEFORE comparing.
// "stored == written" is a tautology whenever both sides are empty, which is
// exactly the pre-§9.98 failure world this whole area has been bitten by twice.
//
// Requires TEST_DB_URL pointing at a DISPOSABLE database carrying the current
// schema (repo convention, same knob as the sibling test).
//
//	TEST_DB_URL='postgres://llm_gateway:***@127.0.0.1:55432/gw_fresh_test' \
//	  go test -tags=integration \
//	    -run TestSearchTextRoundTripsThroughTheNativeReadSource_RealDB \
//	    ./internal/sessionv2mirror/ -v -count=1
func TestSearchTextRoundTripsThroughTheNativeReadSource_RealDB(t *testing.T) {
	dbPool := setupTestDB(t)
	defer dbPool.Close()

	run := fmt.Sprintf("%d", time.Now().UnixNano())
	sessionID := "ses_" + run
	gwTaskID := "tsk_" + run
	clientModel := "clm_" + run
	outboundModel := "omb_" + run
	tenantID := "test_tenant"
	requestID := fmt.Sprintf("st_rt_req_%d", time.Now().UnixNano())
	defer cleanupTestV2Data(t, dbPool, sessionID, tenantID)

	now := time.Now()
	body := `{"messages":[{"role":"user","content":"read side round trip"}]}`

	entry := &telemetry.RequestLogEntry{
		RequestID:     requestID,
		GwSessionID:   &sessionID,
		GwTaskID:      &gwTaskID,
		TenantID:      tenantID,
		ClientModel:   &clientModel,
		OutboundModel: &outboundModel,
		ProviderID:    intPtr(36),
		CredentialID:  intPtr(42),
		Success:       true,
		EventAt:       &now,
		LatencyMs:     intPtr(500),
		PromptTokens:  intPtr(100),
		RequestBody:   &body,
	}

	// Control: the pure function produced real content. Without this, a
	// write-nothing read-nothing state would satisfy every assertion below.
	expected := telemetry.SearchText(entry)
	require.NotNil(t, expected, "telemetry.SearchText must not return nil for a populated entry")
	require.NotEmpty(t, strings.TrimSpace(*expected),
		"telemetry.SearchText returned empty; the round-trip assertions would be vacuous")

	// A token that exists in NO other row of this database, so a match can only
	// come from the row this test just wrote.
	needle := "NEEDLE" + run

	// ---- write side: the production writer, not raw SQL --------------------
	turnWriter := v2.NewTurnWriter(dbPool)
	bodiesWriter := v2.NewSessionBodiesWriter(dbPool)
	aggregator := v2.NewSessionAggregator(dbPool)
	turnLogsWriter := v2.NewTurnLogsWriter(dbPool)
	writer := v2.NewSessionWriterV2(turnWriter, bodiesWriter, aggregator, turnLogsWriter)

	req := entryToProcessedRequest(entry, sessionID)
	require.NotNil(t, req)
	// Put the needle where the search filter will look for it. The writer
	// carries whatever SearchText the hook computed; appending here is how a
	// real body's distinctive text ends up in the column, and it keeps the
	// assertion tied to the READ path rather than to the pure function's
	// field-concatenation order.
	req.SearchText = needle + " " + req.SearchText

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, writer.Write(ctx, req), "writer.Write should succeed")

	// ---- read side: the production native source + the production filter ----
	//
	// Mirrors admin/logs.go: FROM db.SessionFamilyTurnsSourceSQL() rl, with
	// logs.go's `rl.search_text ILIKE $n` filter.
	//
	// The parameter is the LOGICALLY IMPORTANT part. session_turns_hot is an
	// independent storage plane, not a partition of session_turns (see §9.59),
	// and rows reach the parent only via the asynchronous promotion job. So a
	// naive `FROM session_turns` would be a flaky assertion about a periodic
	// job rather than about this chain — which is precisely the kind of
	// false-red the sibling test documents avoiding.
	nativeSrc := db.SessionFamilyTurnsSourceSQL()

	// The column contract is the thing that can regress silently: dropping
	// search_text from canonicalColumnOrderV2 would make this a 42703 at
	// runtime on the hottest admin endpoint, not a compile error.
	var viaProjection string
	err := dbPool.QueryRow(ctx,
		`SELECT search_text FROM `+nativeSrc+` WHERE search_text ILIKE $1`,
		"%"+needle+"%").Scan(&viaProjection)
	require.NoError(t, err,
		"原生读源没有按 search_text 过滤命中本测试写入的行。这有两种可能，都必须修："+
			"(a) db.SessionFamilyTurnsSourceSQL() 的列契约不再含 search_text —— "+
			"打开 storage.admin_logs_native_turns_read 时日志列表会 42703；"+
			"(b) 写入侧没落库。两者都不该被本门放行")
	require.NotEmpty(t, viaProjection, "命中了行但 search_text 是空的")
	require.Equal(t, needle, strings.Fields(viaProjection)[0],
		"读回的 search_text 开头不是本测试的 needle，说明读到了别处的噪声行")

	// The value must be the WHOLE written value, not just the token: a
	// truncated projection (e.g. someone narrowing the column) shows up here.
	require.Equal(t, req.SearchText, viaProjection,
		"读回值与写入值不一致 —— 数据在写→读之间发生了变化")

	// ---- and the same value must be findable by the log list's own filter --
	// with the session-qualified predicate form, which is the only form the
	// audit permits migrating (admin/logs_turns_source.go: session-scoped reads
	// may migrate; request_id / client_request_id lookups and full-window
	// aggregation may not).
	var viaSessionPredicate string
	err = dbPool.QueryRow(ctx,
		`SELECT search_text FROM `+nativeSrc+
			` WHERE gw_session_id = $1 AND search_text ILIKE $2`,
		sessionID, "%"+needle+"%").Scan(&viaSessionPredicate)
	require.NoError(t, err,
		"带 gw_session_id 的会话内读（本审计已判定为可迁的谓词形态）在原生源上查不到本行")
	require.Equal(t, req.SearchText, viaSessionPredicate)

	t.Logf("round trip ok: needle=%s bytes=%d", needle, len(req.SearchText))
}
