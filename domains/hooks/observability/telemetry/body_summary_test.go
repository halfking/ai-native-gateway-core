package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// ★ 2026-10-20（迁移 890，v1 族退役）：本文件整体**改指**。
//
// 被改指的对象：「v1 正文族（_request_logs_bodies_hot）的写入路径」——
// upsertRequestLogBodies 随 890 整条删除 ⇒ 本包**不再有任何 PG 正文写入**，
// 正文落点迁到会话族（session_bodies / session_bodies_hot，由
// internal/sessionv2mirror 承接）。
//
// 改指后本文件守的两条不变式（都还有对象、都有牙）：
//
//	① 写路径**不写正文**——本包发出的 SQL 里不得出现任何正文表/正文列；
//	   且正文的有无/大小不得改变写路径的语句集合。
//	② 摘要信封（digest envelope）本身的格式与空值语义不变——
//	   summarizeBodyJSON 仍被 admin 侧的历史行读取方依赖，它对
//	   "null" / "{}" / "" 三个无载荷哨兵必须原样透传，对非空正文必须产出
//	   摘要信封（且不泄露尾部）。
//
// ⚠ 旧门断言的是「摘要信封被写进 _request_logs_bodies_hot」——那个落点已经
// 不存在了，继续对着它断言只会得到「门钉着一个不存在的东西」。
//
// bodySummaryEnvelope is the observer-side view of the digest envelope that
// summary mode writes into _request_logs_bodies_hot in place of the full
// body. Tests decode the persisted payload into this shape instead of
// calling the production summarizer (no circular assertions).
type bodySummaryEnvelope struct {
	Mode          string `json:"mode"`
	Bytes         int    `json:"bytes"`
	SHA256        string `json:"sha256"`
	Head          string `json:"head"`
	HeadTruncated bool   `json:"head_truncated"`
}

type bodySummaryEnvelopeOuter struct {
	Summary bodySummaryEnvelope `json:"_gw_body_summary"`
}

// bodySummaryMatcher asserts that the value is a digest envelope of rawBody
// (and NOT the full body itself).
type bodySummaryMatcher struct {
	rawBody string
}

func (m bodySummaryMatcher) Match(value interface{}) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	var outer bodySummaryEnvelopeOuter
	if err := json.Unmarshal([]byte(s), &outer); err != nil {
		return false
	}
	sum := outer.Summary
	if sum.Mode != "digest" {
		return false
	}
	if sum.Bytes != len(m.rawBody) {
		return false
	}
	digest := sha256.Sum256([]byte(m.rawBody))
	if sum.SHA256 != hex.EncodeToString(digest[:]) {
		return false
	}
	if !strings.HasPrefix(m.rawBody, sum.Head) {
		return false
	}
	if sum.HeadTruncated && len(sum.Head) >= len(m.rawBody) {
		return false
	}
	// The whole point of summary mode: the persisted payload must not
	// contain the tail of a large body.
	if len(m.rawBody) > len(sum.Head) {
		tail := m.rawBody[len(m.rawBody)-64:]
		if strings.Contains(s, tail) {
			return false
		}
	}
	return true
}

// longBodyForSummary builds a valid JSON body large enough to exceed the
// retained head window, with a unique tail marker so tests can prove the
// tail is dropped.
func longBodyForSummary(tailMarker string) string {
	var sb strings.Builder
	sb.WriteString(`{"model":"glm-5","messages":[{"role":"user","content":"`)
	sb.WriteString(strings.Repeat("summarize-me-", 512))
	sb.WriteString(`"}],"note":"`)
	sb.WriteString(tailMarker)
	sb.WriteString(`"}`)
	return sb.String()
}

func withBodiesSummaryMode(t *testing.T, enabled bool) {
	t.Helper()
	t.Setenv("LLM_GATEWAY_BODY_DIGEST_CANARY_APPLICATIONS", "")
	prev, hadPrev := requestBodiesSummaryEnabledFn()
	setRequestBodiesSummaryEnabledForTest(func() bool { return enabled })
	t.Cleanup(func() {
		if hadPrev {
			setRequestBodiesSummaryEnabledForTest(prev)
		}
	})
}

// recordUpdateStatements 用**录制型 DB** 跑一次 updateRequestLog，返回它
// 实际发出的全部 SQL 原文（按发生顺序）。
//
// 为什么用录制器而不是 pgxmock 的期望链：pgxmock 的匹配器只在「候选期望」上
// 被调用——某条语句若没有对应期望（严格模式下会被 mock 拒绝），匹配器**根本
// 不会被触发** ⇒ 那条语句既录不到、也不会让期望链报错。录制点选在
// requestLogDB 接口边界（见 client_test.go 的 recordingDB）才能看到**每一条**
// 语句，包括被 mock 拒掉的。
//
// 调用方约定：entry 不带 token / APIKeyID / GwSessionID ⇒ 写路径只发一条
// UPDATE（无 api_keys 增补、无 outbox 事件）。
func recordUpdateStatements(t *testing.T, entry *RequestLogEntry) []string {
	t.Helper()
	mockDB, recording, recorder := newRecordingDB(t)

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs(anyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectCommit()

	client := &Client{requestLogDB: recording}
	require.NoError(t, client.updateRequestLog(entry))
	require.NoError(t, mockDB.ExpectationsWereMet())
	return recorder.recorded()
}

// recordInsertStatements 同 recordUpdateStatements，但跑 insertRequestLog
// （只发一条 INSERT INTO usage_ledger_hot）。
func recordInsertStatements(t *testing.T, entry *RequestLogEntry) []string {
	t.Helper()
	mockDB, recording, recorder := newRecordingDB(t)

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`INSERT INTO usage_ledger_hot`).
		WithArgs(anyArgs(19)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectCommit()

	client := &Client{requestLogDB: recording}
	require.NoError(t, client.insertRequestLog(entry))
	require.NoError(t, mockDB.ExpectationsWereMet())
	return recorder.recorded()
}

// requireNoBodyStatement 断言这批语句里没有正文落点——本包不得再写正文
// （正文归会话族）。
//
// ⚠ 「没抓到语句」与「没有正文语句」必须长得不一样 ⇒ 调用方必须先断言
// statements 非空（本 helper 自己也再断言一次）。
func requireNoBodyStatement(t *testing.T, statements []string) {
	t.Helper()
	require.NotEmpty(t, statements,
		"录制器一条语句都没抓到 ⇒ 下面的否定断言是假绿（检查没跑成 ≠ 检查通过）")
	for _, statement := range statements {
		require.NotContains(t, statement, "_request_logs_bodies",
			"v1 正文族随迁移 890 退役，本包不得再写正文表")
		require.NotContains(t, statement, "request_body",
			"正文列不属于本包的写路径（正文落点归会话族）")
		require.NotContains(t, statement, "response_body",
			"正文列不属于本包的写路径（正文落点归会话族）")
		require.NotContains(t, statement, "outbound_body",
			"正文列不属于本包的写路径（正文落点归会话族）")
	}
}

// TestUpdateRequestLog_BodiesSummaryModeWritesNoBodyRow（★ 改指）：
// 摘要模式开着时，updateRequestLog **不得**发出任何正文语句。
//
// 旧门断言的是「摘要信封被写进 _request_logs_bodies_hot」——该落点随 890
// 删除 ⇒ 断言对象已不存在。
//
// 改指后的不变式（仍成立、仍有牙）：摘要模式**不得改变写路径**——
// 开/关两种模式下发出的语句集合必须完全一致，且都不含正文语句。
// 若有人把 v1 的正文写（含摘要信封）接回来，本门立刻变红。
func TestUpdateRequestLog_BodiesSummaryModeWritesNoBodyRow(t *testing.T) {
	requestBody := longBodyForSummary("REQ-TAIL-MARKER")
	responseBody := longBodyForSummary("RESP-TAIL-MARKER")

	newEntry := func() *RequestLogEntry {
		return &RequestLogEntry{
			RequestID:       "req-summary-update",
			Op:              RequestLogUpdate,
			Success:         true,
			RequestStatus:   strptr(RequestStatusSuccess),
			ApplicationCode: strptr("digest-canary"),
			RequestBody:     &requestBody,
			ResponseBody:    &responseBody,
		}
	}

	// 摘要模式 ON（canary 命中）
	withBodiesSummaryMode(t, true)
	t.Setenv("LLM_GATEWAY_BODY_DIGEST_CANARY_APPLICATIONS", "digest-canary")
	summaryStatements := recordUpdateStatements(t, newEntry())

	// 正向对照：摘要模式 OFF —— 同一 entry 的语句集合。
	withBodiesSummaryMode(t, false)
	fullStatements := recordUpdateStatements(t, newEntry())

	require.Equal(t, fullStatements, summaryStatements,
		"摘要模式不得改变写路径的语句集合（v1 正文写入退役后尤其要钉住这一点）")
	requireNoBodyStatement(t, summaryStatements)
	requireNoBodyStatement(t, fullStatements)
}

// TestUpdateRequestLog_BodiesFullModeWritesNoBodyRow（★ 改指）：full 模式
// （默认部署形态）同样不写正文——且**正文的大小/有无不得改变写路径**。
//
// 旧门（TestUpdateRequestLog_BodiesFullModeUnchanged）守的是「full 模式把
// 正文原样落库」；落点已随 890 删除 ⇒ 断言对象已不存在。
//
// 改指后保留的是它的**形状**（三档正文：超大 / 小 / 缺失）：三档必须产出
// **同一条**语句集合——正文内容不得影响本包的写路径。
func TestUpdateRequestLog_BodiesFullModeWritesNoBodyRow(t *testing.T) {
	longBody := longBodyForSummary("FULL-MODE-TAIL")
	smallBody := `{"messages":[{"role":"user","content":"hi"}]}`

	tests := []struct {
		name         string
		requestBody  *string
		responseBody *string
	}{
		{name: "large bodies", requestBody: &longBody, responseBody: &longBody},
		{name: "small bodies", requestBody: &smallBody, responseBody: &smallBody},
		{name: "missing bodies", requestBody: nil, responseBody: nil},
	}

	var baseline []string
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withBodiesSummaryMode(t, false)

			statements := recordUpdateStatements(t, &RequestLogEntry{
				RequestID:     "req-full-mode",
				Op:            RequestLogUpdate,
				Success:       true,
				RequestStatus: strptr(RequestStatusSuccess),
				RequestBody:   tc.requestBody,
				ResponseBody:  tc.responseBody,
			})
			requireNoBodyStatement(t, statements)

			if i == 0 {
				baseline = statements
				return
			}
			require.Equal(t, baseline, statements,
				"正文内容不得改变写路径的语句集合（正文落点已迁到会话族）")
		})
	}
}

// TestInsertRequestLog_BodiesSummaryModeWritesNoBodyRow（★ 改指）：INSERT
// 路径同样不写正文。
//
// 旧门断言的是「首次写入也必须降采样」——那是为了让正文行**变小**；正文行
// 已不存在 ⇒ 该动机消失。改指后钉住的是「INSERT 路径也不写正文」，且
// 写路径只发一条 INSERT（正向对照：录制器抓到了语句）。
func TestInsertRequestLog_BodiesSummaryModeWritesNoBodyRow(t *testing.T) {
	withBodiesSummaryMode(t, true)
	t.Setenv("LLM_GATEWAY_BODY_DIGEST_CANARY_APPLICATIONS", "digest-canary")

	requestBody := longBodyForSummary("INSERT-REQ-TAIL")
	responseBody := longBodyForSummary("INSERT-RESP-TAIL")

	statements := recordInsertStatements(t, &RequestLogEntry{
		RequestID:       "req-summary-insert",
		Op:              RequestLogInsert,
		ApplicationCode: strptr("digest-canary"),
		RequestBody:     &requestBody,
		ResponseBody:    &responseBody,
	})

	require.Len(t, statements, 1, "insertRequestLog 应当只发一条 INSERT（正向对照：录制器抓到了语句）")
	require.Contains(t, statements[0], "INSERT INTO usage_ledger_hot")
	requireNoBodyStatement(t, statements)
}

// TestSummarizeBodyJSONKeepsEmptySemantics（★ 改指）：摘要信封的**空值语义**
// 不变。
//
// 旧门（TestUpdateRequestLog_BodiesSummaryModeKeepsEmptySemantics）守的是
// 「v1 正文表的行级空值语义」：nil 正文落 "null"、空正文被 sanitize 丢成
// "null"、小正文照样降采样（行级一致，不按大小分叉）。
//
// 正文表退役后，那条落点没了；但**摘要信封本身**还活着：admin 侧的历史行
// 读取方仍按这个格式解析 ⇒ 它的空值语义与行级一致性必须保留。
// 于是本门改指到 summarizeBodyJSON 本身：
//
//	① 无载荷哨兵（"" / "null" / "{}"）必须**原样透传**——摘要化不得制造新语义；
//	② 非空正文必须产出摘要信封（mode=digest + bytes + sha256 + 有界 head），
//	   且**不得**包含尾部；
//	③ 行级一致性：正文多小都照样摘要化（不按大小分叉）。
func TestSummarizeBodyJSONKeepsEmptySemantics(t *testing.T) {
	// ① 无载荷哨兵原样透传。
	for _, sentinel := range []string{"", "null", "{}"} {
		require.Equal(t, sentinel, summarizeBodyJSON(sentinel),
			"摘要化不得改变无载荷哨兵（admin 侧历史行读取方依赖这个语义）")
	}

	// ② 非空正文 → 摘要信封，且不泄露尾部。
	big := longBodyForSummary("DIGEST-TAIL-MARKER")
	digest := summarizeBodyJSON(big)
	require.True(t, bodySummaryMatcher{rawBody: big}.Match(digest),
		"非空正文必须产出摘要信封（mode/bytes/sha256/有界 head），且不得包含尾部")
	var outer bodySummaryEnvelopeOuter
	require.NoError(t, json.Unmarshal([]byte(digest), &outer))
	require.Equal(t, "digest", outer.Summary.Mode)
	require.Equal(t, len(big), outer.Summary.Bytes)
	require.True(t, outer.Summary.HeadTruncated, "超出 head 窗口的正文必须标记被截断")

	// ③ 行级一致性：再小的正文也照样摘要化（不按大小分叉）。
	small := `{"ok":true}`
	smallDigest := summarizeBodyJSON(small)
	require.True(t, bodySummaryMatcher{rawBody: small}.Match(smallDigest),
		"小正文同样必须被摘要化（行级一致，不按大小分叉）")
	require.NotEqual(t, small, smallDigest, "小正文也必须变成信封形态")
}

// fakeSettingsBackend is a minimal in-memory settings backend (same pattern
// as domains/hooks/compression tests).
type fakeSettingsBackend struct {
	store map[string][]byte
}

func (f *fakeSettingsBackend) Get(scope settings.Scope, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeSettingsBackend) Set(scope settings.Scope, key string, value any) ([]byte, error) {
	return nil, nil
}
func (f *fakeSettingsBackend) GetTenant(tenantID, key string) ([]byte, error) {
	return f.store[key], nil
}
func (f *fakeSettingsBackend) SetTenant(tenantID, key string, value any) ([]byte, error) {
	return nil, nil
}

// withSessionsV2Settings swaps settings.Global for a registry carrying the
// real sessions_v2 specs plus the given platform overrides.
func withSessionsV2Settings(t *testing.T, overrides map[string]bool) {
	t.Helper()
	prevGlobal := settings.Global
	prevSeam, hadSeam := requestBodiesSummaryEnabledFn()
	t.Cleanup(func() {
		settings.Global = prevGlobal
		if hadSeam {
			setRequestBodiesSummaryEnabledForTest(prevSeam)
		}
	})

	store := map[string][]byte{
		// v1 写门显式打开：本 helper 会**替换**掉 settings.Global，替换后的
		// registry 若不显式钉住该键，读到的就是新的代码默认 false（审计 D0），
		// 于是测 bodies 写法的用例里 v1 写路径整段被跳过。前提从隐式变显式，
		// 断言强度不变（下面 overrides 里的同名键仍然优先）。
		settings.KeyRequestLogsWriteEnabled: []byte("true"),
	}
	for key, val := range overrides {
		store[key] = []byte(strconvFormatBool(val))
	}
	registry := settings.NewRegistry()
	registry.RegisterBackend(settings.ScopePlatform, &fakeSettingsBackend{store: store})
	registry.RegisterBackend(settings.EnvBackendScope, settings.NewStoreEnv())
	for _, spec := range append(settings.StorageSpecs(), settings.SessionsV2Specs()...) {
		registry.MustRegisterSpec(spec)
	}
	settings.Global = registry
	// Pin the seam to the settings-backed default so the test exercises the
	// real flag combination rather than an override.
	setRequestBodiesSummaryEnabledForTest(defaultBodiesSummaryEnabled)
}

func strconvFormatBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestRequestBodiesSummaryEnabled_FlagCombination pins full body persistence
// regardless of historical settings_kv values. Summary writes are disabled.
func TestRequestBodiesSummaryEnabled_FlagCombination(t *testing.T) {
	tests := []struct {
		name                  string
		sessionsV2On          bool
		setBodiesFullOverride bool
		bodiesFullOn          bool
		wantSummaryOn         bool
	}{
		{name: "sessions_v2 disabled keeps full bodies", sessionsV2On: false, bodiesFullOn: false, wantSummaryOn: false},
		{name: "sessions_v2 enabled defaults to full bodies", sessionsV2On: true, wantSummaryOn: false},
		{name: "legacy explicit false still keeps full bodies", sessionsV2On: true, setBodiesFullOverride: true, bodiesFullOn: false, wantSummaryOn: false},
		{name: "explicit true keeps full bodies", sessionsV2On: true, setBodiesFullOverride: true, bodiesFullOn: true, wantSummaryOn: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			overrides := map[string]bool{"sessions_v2.enabled": tc.sessionsV2On}
			if tc.setBodiesFullOverride {
				overrides["sessions_v2.request_bodies_full"] = tc.bodiesFullOn
			}
			withSessionsV2Settings(t, overrides)

			require.Equal(t, tc.wantSummaryOn, requestBodiesSummaryEnabled())
		})
	}
}

func TestRequestBodiesSummaryEnabled_ApplicationCanary(t *testing.T) {
	withBodiesSummaryMode(t, true)

	t.Run("empty allowlist keeps application writes full", func(t *testing.T) {
		t.Setenv("LLM_GATEWAY_BODY_DIGEST_CANARY_APPLICATIONS", "")
		require.False(t, requestBodiesSummaryEnabled("any-app"))
	})

	t.Run("non-canary application retains full body", func(t *testing.T) {
		t.Setenv("LLM_GATEWAY_BODY_DIGEST_CANARY_APPLICATIONS", "canary-a, canary-b")
		require.False(t, requestBodiesSummaryEnabled("ordinary-app"))
	})

	t.Run("canary application enables digest", func(t *testing.T) {
		t.Setenv("LLM_GATEWAY_BODY_DIGEST_CANARY_APPLICATIONS", "canary-a, canary-b")
		require.True(t, requestBodiesSummaryEnabled("canary-b"))
	})
}
