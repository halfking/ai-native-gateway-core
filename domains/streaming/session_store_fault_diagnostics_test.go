package streaming

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/session"
)

// session_store_fault_diagnostics_test.go — 2026-11-11（ZCode 事故轮 ·
// 可诊断性修复）：会话存储故障的**成因**必须能在服务端日志里看到。
//
// 缺口本体（211 生产实证）：
//
//	POST /v1/messages（≥2 条消息、无客户端 session id）
//	⇒ 503 {"error":{"message":"session store unavailable",...}}（stage=ingress）
//	⇒ 该 503 在 211 上已持续 24h+（11-10 18:00 起每小时 15~30 条）且间歇
//	  命中多个 api_key；
//	⇒ 但容器日志里**一个字**都查不到原因——因为
//	  domains/streaming/session_routing.go 的 normalizeAndRegisterClientSession
//	  与 domains/streaming/large_ingress.go 的 resolveArchiveSession 在返回
//	  前把底层错误**整个丢掉**，只回一个笼统的 errClientSessionUnavailable。
//
// 本测试钉住两件事：
//  ① **可诊断**：底层成因必须出现在日志里（否则下一次仍然查不出原因）；
//  ② **语义不变**：存储故障仍然 fail-closed（不是「归属问题」403，也不是
//     放行未验证的会话 id）——本轮只补可见性，不改裁决。

// faultGetter always fails with the given error.
type faultGetter struct{ err error }

func (f faultGetter) Get(context.Context, string) (*session.Session, error) { return nil, f.err }

// captureLogs swaps the default slog logger for a buffer-backed one and
// returns the buffer plus a restore func.
func captureLogs(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf, func() { slog.SetDefault(prev) }
}

func TestNormalizeAndRegisterClientSession_StoreFaultIsDiagnosable(t *testing.T) {
	buf, restore := captureLogs(t)
	defer restore()

	const cause = "redis error reading session: dial tcp 10.0.0.9:6379: connection pool timeout"
	getter := faultGetter{err: errors.New(cause)}
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	keyInfo := &authentication.KeyInfo{ID: 7, TenantID: "tenant-diag"}

	_, _, err := normalizeAndRegisterClientSession(r, "gw_diag_case", getter, keyInfo)

	// ① 语义不变：存储故障必须 fail-closed，且不得被当成「归属问题」。
	if !errors.Is(err, errClientSessionUnavailable) {
		t.Fatalf("a store fault must still return errClientSessionUnavailable, got %v", err)
	}
	if errors.Is(err, errClientSessionForbidden) {
		t.Fatal("a store fault must not be reported as a forbidden session")
	}

	// ② 可诊断：成因必须出现在日志里。
	if !strings.Contains(buf.String(), "connection pool timeout") {
		t.Fatalf("the underlying cause must be visible in the logs, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "gw_diag_case") {
		t.Fatalf("the log must name the session id, got: %s", buf.String())
	}
}

// TestNormalizeAndRegisterClientSession_AbsentSessionIsNotAFault is the
// negative control: a merely-absent session is tolerated (not a store fault)
// and must NOT be reported as one.
func TestNormalizeAndRegisterClientSession_AbsentSessionIsNotAFault(t *testing.T) {
	buf, restore := captureLogs(t)
	defer restore()

	getter := faultGetter{err: session.ErrSessionNotFound}
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	keyInfo := &authentication.KeyInfo{ID: 7, TenantID: "tenant-diag"}

	_, _, err := normalizeAndRegisterClientSession(r, "gw_absent_case", getter, keyInfo)
	if err != nil {
		t.Fatalf("an absent session must not fail the request, got %v", err)
	}
	if strings.Contains(buf.String(), "store fault") {
		t.Fatalf("an absent session must not be reported as a store fault, got: %s", buf.String())
	}
}
