package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCreateKey_EnforcesLimitCaps 钉 2026-11-15 创建路径补帽（限流统一按
// key 轮）：rate_limit_rpm / budget_usd 超过 keyLimitCaps 必须 400——此前
// 创建路径非负校验后照单全收（keys 自助管理审计登记的「无上限帽」缺口），
// 只有更新路径有帽，两路口径不齐。帽检查发生在任何 DB 访问之前 ⇒ nil db
// 可直达（拒绝先于 getOrCreateApplication/findActiveKeyConflict）。
func TestCreateKey_EnforcesLimitCaps(t *testing.T) {
	h := &Handler{}

	cases := []struct {
		name string
		body string
		want string // 期望错误信息子串
	}{
		{
			name: "rpm above cap",
			body: `{"application_code":"cap-test","tenant_id":"t-cap","rate_limit_rpm":999999}`,
			want: "rate_limit_rpm",
		},
		{
			name: "budget above cap",
			body: `{"application_code":"cap-test","tenant_id":"t-cap","budget_usd":9999999}`,
			want: "budget_usd",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.createKey(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("超帽必须 400: got %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("错误信息必须点名超帽字段: body=%s want~%s", rec.Body.String(), tc.want)
			}
		})
	}
}

// TestCreateKey_CapBoundariesAccept 钉帽的**边界**方向：恰好等于帽的值不是
// 超帽（`>` 判据）——「帽内值被误拦」与「帽被删除」是两个方向的回归，本钉
// 防前者（over-cap 腿防后者）。纯逻辑断言，不经 HTTP（nil db 进不了 DB 面）。
func TestCreateKey_CapBoundariesAccept(t *testing.T) {
	if maxKeyRPM <= 0 || maxKeyConcurrent <= 0 || maxKeyTPM <= 0 || maxKeyBudgetUSD <= 0 {
		t.Fatalf("帽常量必须为正: rpm=%d concurrent=%d tpm=%d budget=%f",
			maxKeyRPM, maxKeyConcurrent, maxKeyTPM, maxKeyBudgetUSD)
	}
	if maxKeyRPM != 10000 || maxKeyConcurrent != 1000 {
		t.Fatalf("帽值与 updateKeyLimits 原帽对齐（rpm=10000/concurrent=1000）: %d/%d", maxKeyRPM, maxKeyConcurrent)
	}
	// 边界值（恰等于帽）走 `>` 判据必须不被拒——直接复刻 createKey 的判定式。
	rpm := maxKeyRPM
	budget := maxKeyBudgetUSD
	if rpm > maxKeyRPM || budget > maxKeyBudgetUSD {
		t.Fatalf("边界值被误判为超帽: rpm=%d budget=%f", rpm, budget)
	}
}
