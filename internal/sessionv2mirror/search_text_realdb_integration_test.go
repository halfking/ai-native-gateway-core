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

	// The consumer-facing projection is NOT asserted here, and that omission is
	// deliberate — but NOT for the reason the note below used to give.
	//
	// ⚠️ 2026-10-04 订正：这段注释原先写「session_turns_with_current_month
	// 投影 55 列且不含 search_text …… 该视图投影是 v1 退役阻断项的读侧半边」。
	// **前半句是真的**（真库重核：55 列、其中 0 个 search_text），
	// **后半句的推论是错的，会把人引向不必要的改动。**
	//
	// 灰度开关并不读那个视图。admin/logs.go 的 FROM 由
	// admin/logs_turns_source.go:logsSourceFromSQL() 选出，打开
	// storage.admin_logs_native_turns_read 时替换成
	// db.SessionFamilyTurnsSourceSQL() —— 那是对 session_turns_hot /
	// session_turns 的**内联投影**，不是对 session_turns_with_current_month
	// 的遍历。该投影由 canonicalColumnOrderV2（115 个名字）经
	// projectionExprByColumn 渲染，而 canonicalColumnOrderV2[21] ==
	// "search_text" 对应 "t.search_text"（projectionExprsV2[21]），
	// **逐位对齐、列存在**。
	//
	// 真正阻断搜索迁移的是**行覆盖率**，不是 schema：见
	// admin/logs_turns_source.go —— 视图里 27.6% 的 request_id 在原生源查不到
	// （无会话头流量按设计不镜像），而给视图加宽这个视图**一行都补不上**。
	// 那是关于非会话流量的业务决策，且已按「谓词形态」登记在
	// admin/session_view_dependency_risk_test.go。
	//
	// ⇒ 因此这里既不断言该视图缺 search_text（那会造一条假约束：
	// 未来合法地加宽该视图会无理由地转红），也不去动它。
	// 该守的正向性质在 TestSearchTextRoundTripsThroughTheNativeReadSource_RealDB。
	var viewHasSearchText bool
	err = db.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name   = 'session_turns_with_current_month'
			  AND column_name  = 'search_text')`).Scan(&viewHasSearchText)
	require.NoError(t, err)
	t.Logf("session_turns_with_current_month exposes search_text: %v "+
		"(informational only — 该视图不在灰度开关的读路径上，见上方订正)", viewHasSearchText)

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
