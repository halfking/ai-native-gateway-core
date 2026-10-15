package streaming

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestIsQuotaHeldKind(t *testing.T) {
	for _, kind := range []errorsx.ErrorKind{
		errorsx.KindQuota,
		errorsx.KindQuotaBalance,
		errorsx.KindQuotaPeriodic,
		errorsx.KindQuotaPermanent,
	} {
		if !isQuotaHeldKind(kind) {
			t.Fatalf("isQuotaHeldKind(%q) = false, want true", kind)
		}
	}
	if isQuotaHeldKind(errorsx.KindAuth) {
		t.Fatal("auth must not count as quotaHeld")
	}
	if isQuotaHeldKind(errorsx.KindRateLimit) {
		t.Fatal("rate_limit must not count as quotaHeld")
	}
}

func TestQuotaHeldClientSurface(t *testing.T) {
	status, retryAfter, ok := quotaHeldClientSurface(0, errorsx.KindQuota, 0)
	if !ok || status != http.StatusTooManyRequests || retryAfter != 30 {
		t.Fatalf("quotaHeld surface = (%d,%d,%v), want (429,30,true) for hint=0", status, retryAfter, ok)
	}
	// P0-D：Retry-After 对齐 earliest resetAt（秒提示透传）。
	status, retryAfter, ok = quotaHeldClientSurface(0, errorsx.KindQuota, 19*60)
	if !ok || status != http.StatusTooManyRequests || retryAfter != 19*60 {
		t.Fatalf("quotaHeld surface = (%d,%d,%v), want (429,1140,true)", status, retryAfter, ok)
	}
	// 前提在场：真上游试过（Tried>0）不改写。
	if _, _, ok := quotaHeldClientSurface(3, errorsx.KindQuota, 60); ok {
		t.Fatal("Tried>0 must not remapped as quotaHeld")
	}
	if _, _, ok := quotaHeldClientSurface(0, errorsx.KindAuth, 60); ok {
		t.Fatal("auth must not remapped as quotaHeld")
	}
}

// TestApplyQuotaHeldHTTPHeadersSetsRetryAfter 同层驱动生产入口
// applyQuotaHeldHTTPHeaders（handler 错误路径调用点），断言 HTTP 头而非仅映射函数。
func TestApplyQuotaHeldHTTPHeadersSetsRetryAfter(t *testing.T) {
	rec := httptest.NewRecorder()
	status, retryAfter, ok := applyQuotaHeldHTTPHeaders(rec, 0, errorsx.KindQuota, 19*60)
	if !ok || status != http.StatusTooManyRequests || retryAfter != 19*60 {
		t.Fatalf("apply = (%d,%d,%v), want (429,1140,true)", status, retryAfter, ok)
	}
	if got := rec.Header().Get("Retry-After"); got != "1140" {
		t.Fatalf("Retry-After = %q, want 1140", got)
	}
	// 负控制：Tried>0 不得写头。
	rec2 := httptest.NewRecorder()
	if _, _, ok := applyQuotaHeldHTTPHeaders(rec2, 2, errorsx.KindQuota, 60); ok {
		t.Fatal("Tried>0 must not apply headers")
	}
	if got := rec2.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q, want empty when not applied", got)
	}
}

func TestHandlerQuotaHeldPathUsesApplyHelper(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(file), "handler.go"))
	if err != nil {
		t.Fatal(err)
	}
	// 必须钉生产错误路径调用（带 execErrTyped），不能只数函数定义——
	// 定义 alone 会让「改回 inline Header.Set」变异假绿。
	needle := "applyQuotaHeldHTTPHeaders(w, execErrTyped.Tried, execErrTyped.LastKind, execErrTyped.RetryAfterSeconds)"
	if !strings.Contains(string(src), needle) {
		t.Fatalf("handler.go missing production call %q", needle)
	}
}
