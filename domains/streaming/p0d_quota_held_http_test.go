package streaming

import (
	"net/http"
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
	status, retryAfter, ok := quotaHeldClientSurface(0, errorsx.KindQuota)
	if !ok || status != http.StatusTooManyRequests || retryAfter != 30 {
		t.Fatalf("quotaHeld surface = (%d,%d,%v), want (429,30,true)", status, retryAfter, ok)
	}
	// 前提在场：真上游试过（Tried>0）不改写。
	if _, _, ok := quotaHeldClientSurface(3, errorsx.KindQuota); ok {
		t.Fatal("Tried>0 must not remapped as quotaHeld")
	}
	if _, _, ok := quotaHeldClientSurface(0, errorsx.KindAuth); ok {
		t.Fatal("auth must not remapped as quotaHeld")
	}
}
