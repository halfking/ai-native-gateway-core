package admin

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

// W1.3 surfaces that must refuse with recording_unavailable when consent=off.
// Source-level gate: a new body-returning handler without the call fails here
// before a silent-empty regression ships.
func TestRecordingConsentSurfacesCallRequire(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	adminDir := filepath.Dir(thisFile)

	mustContain := []string{
		"session_summary_v2.go",
		"session_detail_v2.go",
		"session_turns_v2.go",
	}
	for _, name := range mustContain {
		raw, err := os.ReadFile(filepath.Join(adminDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		src := string(raw)
		if !strings.Contains(src, "requireRecordingConsent(w, tenantID)") {
			t.Errorf("%s missing requireRecordingConsent — body/replay/summary must gate consent", name)
		}
	}
	turnsRaw, err := os.ReadFile(filepath.Join(adminDir, "session_turns_v2.go"))
	if err != nil {
		t.Fatal(err)
	}
	if c := strings.Count(string(turnsRaw), "requireRecordingConsent(w, tenantID)"); c < 3 {
		t.Errorf("session_turns_v2.go: want ≥3 requireRecordingConsent sites (detail/bodies/instant-summary), got %d", c)
	}
}

func TestRequireRecordingConsent_OffReturnsStructuredConflict(t *testing.T) {
	prev := settings.TestingConsentOverride
	off := false
	settings.TestingConsentOverride = &off
	t.Cleanup(func() { settings.TestingConsentOverride = prev })

	rec := httptest.NewRecorder()
	if requireRecordingConsent(rec, "tenant-x") {
		t.Fatal("expected false when consent off")
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("missing error object: %s", rec.Body.String())
	}
	if got, _ := errObj["code"].(string); got != RecordingUnavailableCode {
		t.Fatalf("error.code = %q, want %q", got, RecordingUnavailableCode)
	}
}

func TestSessionDetailV2_ConsentOffUnavailable(t *testing.T) {
	prev := settings.TestingConsentOverride
	off := false
	settings.TestingConsentOverride = &off
	t.Cleanup(func() { settings.TestingConsentOverride = prev })

	// ★ 24h 审计第 9 轮订正后的契约：consent 门在 **resolveSessionID 成功之后、
	// 正文查询（LEFT JOIN LATERAL）之前** —— 404（不存在/跨租户）与 500
	// （resolve/query 故障）必须先于 409 裁决（否则「不存在 ⇒ 409」既遮蔽
	// 存在性语义、又给调用方一个 consent 探针；session_identity_contract
	// 四用例实抓）。本用例相应改为：resolve 允许查（direct hit 一行），
	// consent=off ⇒ 409 recording_unavailable，且**正文查询从不执行**
	//（pgxmock 无后续期望 ⇒ 若门没拦住，下一个查询报错 ⇒ 500 ≠ 409 ⇒ 红）。
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery(`SELECT session_id FROM public\.sessions`).
		WithArgs("s1", "t1").
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow("s1"))

	api := NewSessionDetailV2APIWithDB(mock)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/detail?session_id=s1&tenant=t1", nil)
	req = SetAuthContext(req, &AuthContext{Role: "super_admin", TenantID: "t1"})
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), RecordingUnavailableCode) {
		t.Fatalf("body missing %s: %s", RecordingUnavailableCode, rec.Body.String())
	}
	// 正文查询（LEFT JOIN LATERAL）必须没跑：若跑了，上面的 409 不可能成立
	//（pgxmock 遇到未期望的查询返回错误 ⇒ 处理器走 500）。这里再核对一次
	// 已满足的期望只有 resolve 那一条。
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected query pattern: %v", err)
	}
}
