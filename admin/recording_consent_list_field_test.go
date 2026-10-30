package admin

// recording_consent_list_field_test.go — 三路 turns 列表（v2/unified/tree）必须
// 透出 recording_consent 字段（24h 审计第 9 轮批判式审计补）。
//
// 背景：W1.3 给三路列表都加了 `"recording_consent": settings.RecordingConsentEnabled(...)`
//（session_turns_v2.go / session_turns_unified.go / session_turns_tree.go），
// 前端 SessionDetailPage 以它为 consent-off 横幅的第一信号源——但**此前零
// 后端测试覆盖**：任何一路重构时删掉这个键，前端横幅就静默退化为「等
// /detail 的 409 兜底」，且无任何门会红。
//
// 三路的钉法（按可测性如实分层，不假装一致）：
//   - v2 / unified：有 *DB 缝（serveSessionTurnsListDB / serveSessionTurnsUnifiedDB），
//     pgxmock 直调 wire 响应，断言字段存在与取值（override false/true 两态）；
//   - tree：Handler.db 是具体 *pgxpool.Pool（handler.go:61 注释明文「无法在
//     单测里注入 pgxmock」）⇒ wire 级不可测 ⇒ 退到**源码级断言**（与
//     TestRecordingConsentSurfacesCallRequire 同款）：handleSessionTurnsTree
//     的 writeJSON 组装必须含 recording_consent 键。源码门是下位替代
//    （重构改名会误报/漏报），已在测试名与注释里如实标注。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// assertTurnsListConsentField 从 200 响应里抽出 recording_consent 并与期望比对。
func assertTurnsListConsentField(t *testing.T, rr *httptest.ResponseRecorder, want bool, route string) {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("%s: status = %d, body = %s", route, rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: decode: %v body=%s", route, err, rr.Body.String())
	}
	got, ok := body["recording_consent"]
	if !ok {
		// ★ 缺键比值错更隐蔽：前端拿到 undefined ⇒ 永远等 409 兜底，
		// 横幅首屏永远不亮。这里把「字段被删」钉成红。
		t.Fatalf("%s: response missing recording_consent field (front-end first signal); body keys: %v", route, body)
	}
	b, ok := got.(bool)
	if !ok {
		t.Fatalf("%s: recording_consent = %#v, want bool", route, got)
	}
	if b != want {
		t.Fatalf("%s: recording_consent = %v, want %v", route, b, want)
	}
}

func withConsentOverride(t *testing.T, on bool) {
	t.Helper()
	prev := settings.TestingConsentOverride
	settings.TestingConsentOverride = &on
	t.Cleanup(func() { settings.TestingConsentOverride = prev })
}

// emptyTurnListRowsForMock 构造零行结果集：字段在 handler 组装层，
// 与行数无关 ⇒ 空页即最小可用（列名占位即可，Scan 不会被调用）。
func emptyTurnListRowsForMock() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"turn_no"})
}

func TestTurnsListV2_ExposesRecordingConsent(t *testing.T) {
	withConsentOverride(t, false)
	mock := newDigestMockPool(t)
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("tenant-a", "gw_abc", int(^uint(0)>>1), 51).
		WillReturnRows(emptyTurnListRowsForMock())

	req := newDigestAuthRequest(http.MethodGet, "/api/admin/sessions/gw_abc/turns", "tenant_admin", "tenant-a")
	rr := httptest.NewRecorder()
	serveSessionTurnsListDB(mock, "test-secret", rr, req, "gw_abc")
	assertTurnsListConsentField(t, rr, false, "v2 list")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTurnsListV2_ConsentOverrideTrue(t *testing.T) {
	withConsentOverride(t, true)
	mock := newDigestMockPool(t)
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("tenant-a", "gw_abc", int(^uint(0)>>1), 51).
		WillReturnRows(emptyTurnListRowsForMock())

	req := newDigestAuthRequest(http.MethodGet, "/api/admin/sessions/gw_abc/turns", "tenant_admin", "tenant-a")
	rr := httptest.NewRecorder()
	serveSessionTurnsListDB(mock, "test-secret", rr, req, "gw_abc")
	assertTurnsListConsentField(t, rr, true, "v2 list (override on)")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTurnsListUnified_ExposesRecordingConsent(t *testing.T) {
	withConsentOverride(t, false)
	mock := newDigestMockPool(t)
	// unified 主查询；空页 0 行即可（字段在 handler 组装层，与行数无关）。
	// 参数与 v2 同形（同 cursor 语义、同 defaultTurnsListLimit=50 ⇒ limit+1=51）。
	mock.ExpectQuery(`FROM public\.session_turns_with_current_month`).
		WithArgs("tenant-a", "gw_abc", int(^uint(0)>>1), 51).
		WillReturnRows(emptyTurnListRowsForMock())

	req := newDigestAuthRequest(http.MethodGet, "/api/admin/sessions/gw_abc/turns", "tenant_admin", "tenant-a")
	rr := httptest.NewRecorder()
	serveSessionTurnsUnifiedDB(mock, "test-secret", rr, req, "gw_abc")
	assertTurnsListConsentField(t, rr, false, "unified list")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestTurnsListTree_HandlerAssemblesConsentField — tree 路的源码级断言
//（Handler.db 具体类型不可注入，见文件头）。断言 handleSessionTurnsTree
// 的响应组装含 recording_consent 键——键被删即红。
func TestTurnsListTree_HandlerAssemblesConsentField(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "session_turns_tree.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `"recording_consent": settings.RecordingConsentEnabled(tenantID)`) {
		t.Fatal("handleSessionTurnsTree no longer assembles recording_consent — " +
			"front-end consent-off banner loses its primary signal (tree is the default turns route)")
	}
}
