package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// ── 2026-11-27 L135 轮（断点③修复 / P0-2b）plugin-config PUT 热切换钉 ──
//
// 判据四件事：
//   ① PUT 落库语句形状：整行覆盖（四列齐发、id=1 upsert）—— 局部请求经
//      merge（空 = 保持现状）后落库，不再出现「空串撞 CHECK 约束 / 覆盖
//      其它列」的 500。
//   ② PUT 后热切换立即生效：registry.GetApprovalProvider 在**同一请求返回
//      后**就解析到新 provider —— 「不重启生效」的端到端判据。
//   ③ 无 os.Setenv 副作用：PUT 前后进程 env 不被改写 —— env 不再是配置源，
//      表是唯一事实源（重启后口径不被启动环境打回）。
//   ④ response.message 反映热切换语义（前端不再显示「restart required」）。

// stubApprovalProvider 实现 authplugin.ApprovalProvider 全接口的最小桩，
// 只携带名字。registry 的选择器语义已在 authplugin 包钉过
// （registry_hotswitch_test.go），这里只需要 Name() 能对上
// validateProviderSelection 的 List* 检查与 Get* 的解析结果。
type stubApprovalProvider struct{ name string }

func (s stubApprovalProvider) Name() string { return s.name }
func (stubApprovalProvider) GetConfig(ctx context.Context, tenantID string) (*authplugin.ApprovalConfig, error) {
	return nil, nil
}
func (stubApprovalProvider) UpdateConfig(ctx context.Context, tenantID string, config *authplugin.ApprovalConfig) error {
	return nil
}
func (stubApprovalProvider) GetApprovers(ctx context.Context, tenantID string) ([]*authplugin.Approver, error) {
	return nil, nil
}
func (stubApprovalProvider) AddApprover(ctx context.Context, tenantID string, approver *authplugin.Approver) error {
	return nil
}
func (stubApprovalProvider) UpdateApprover(ctx context.Context, tenantID, userID string, approver *authplugin.Approver) error {
	return nil
}
func (stubApprovalProvider) RemoveApprover(ctx context.Context, tenantID, userID string) error {
	return nil
}
func (stubApprovalProvider) GetRules(ctx context.Context, tenantID string) ([]*authplugin.ApprovalRule, error) {
	return nil, nil
}
func (stubApprovalProvider) AddRule(ctx context.Context, tenantID string, rule *authplugin.ApprovalRule) error {
	return nil
}
func (stubApprovalProvider) RemoveRule(ctx context.Context, tenantID, ruleName string) error {
	return nil
}
func (stubApprovalProvider) SupportsFeature(feature authplugin.ApprovalFeature) bool {
	return false
}

// newPluginConfigTestFixture 构造 registry（approval 槽位注册 native+casdoor
// 桩、选 native）与注入 pgxmock 的 handler。
func newPluginConfigTestFixture(t *testing.T) (*PluginConfigHandler, pgxmock.PgxPoolIface, *authplugin.Registry) {
	t.Helper()

	mock, err0 := pgxmock.NewPool()
	if err0 != nil {
		t.Fatalf("pgxmock.NewPool: %v", err0)
	}
	reg := authplugin.NewRegistry(&authplugin.PluginConfig{
		AuthProvider:     "native",
		UserManager:      "native",
		TenantManager:    "native",
		ApprovalProvider: "native",
	})
	for _, name := range []string{"native", "casdoor"} {
		if err := reg.RegisterApprovalProvider(stubApprovalProvider{name: name}); err != nil {
			t.Fatalf("register approval provider %s: %v", name, err)
		}
	}

	h := NewPluginConfigHandler(reg, nil)
	h.setPluginConfigDB(mock)
	return h, mock, reg
}

// ── ① 落库语句形状 + ② 热切换生效 + ④ 响应语义 ──

func TestPluginConfigPutPersistsFullRowAndHotSwitches(t *testing.T) {
	h, mock, reg := newPluginConfigTestFixture(t)

	// 落库判据：整行覆盖（四列）+ id=1 upsert。局部请求（只切 approval）
	// 经 merge 后 auth/user/tenant 保持 native —— 不再是空串。
	mock.ExpectExec("INSERT INTO plugin_config").
		WithArgs("native", "native", "native", "casdoor").
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	body := `{"approval_provider": "casdoor"}`
	req := httptest.NewRequest(http.MethodPut, "/api/admin/plugin-config", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.UpdatePluginConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s, want 200", w.Code, w.Body.String())
	}

	// ② 热切换立即生效
	ap, err := reg.GetApprovalProvider()
	if err != nil || ap.Name() != "casdoor" {
		t.Fatalf("after PUT: approval provider = %v (err=%v), want casdoor — hot-switch did not apply", ap, err)
	}
	// 其它槽位保持（merge 语义）
	if cfg := reg.Config(); cfg.AuthProvider != "native" || cfg.UserManager != "native" {
		t.Fatalf("after PUT: auth/user = %q/%q, want native/native (merge kept them)", cfg.AuthProvider, cfg.UserManager)
	}

	// ④ 响应 message 反映热切换
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not json: %v", err)
	}
	if msg, _ := resp["message"].(string); !strings.Contains(msg, "no restart required") {
		t.Fatalf("response message = %q, want it to reflect hot-switch (no restart required)", msg)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations not met: %v", err)
	}
}

// TestPluginConfigPutRejectsUnavailableProvider 钉守卫：请求一个未注册的
// provider（registry 里没有 ldap）⇒ 400，且**不落库、不切换**。
func TestPluginConfigPutRejectsUnavailableProvider(t *testing.T) {
	h, mock, reg := newPluginConfigTestFixture(t)

	body := `{"approval_provider": "ldap"}`
	req := httptest.NewRequest(http.MethodPut, "/api/admin/plugin-config", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.UpdatePluginConfig(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT unknown provider status = %d, want 400", w.Code)
	}
	if cfg := reg.Config(); cfg.ApprovalProvider != "native" {
		t.Fatalf("rejected PUT changed selection to %q — must stay native", cfg.ApprovalProvider)
	}
	// 不应有任何 SQL 发生（validate 先于 save）
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no SQL expected on rejected PUT, got: %v", err)
	}
}

// TestPluginConfigPutNoEnvSideEffect 钉判据③：PUT 前后 AUTH_PROVIDER 等
// 进程 env 不被改写（旧实现 os.Setenv ⇒ 重启后 env 压过表 ⇒ 口径分叉）。
func TestPluginConfigPutNoEnvSideEffect(t *testing.T) {
	h, mock, _ := newPluginConfigTestFixture(t)

	// 保留现场（测试进程可能已有这些 env —— 断言的是「PUT 不改变它们」）
	keys := []string{"AUTH_PROVIDER", "USER_MANAGER", "TENANT_MANAGER", "APPROVAL_PROVIDER"}
	before := map[string]string{}
	for _, k := range keys {
		before[k] = os.Getenv(k)
	}
	t.Cleanup(func() {
		for _, k := range keys {
			if before[k] == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, before[k])
			}
		}
	})

	mock.ExpectExec("INSERT INTO plugin_config").
		WithArgs("native", "native", "native", "casdoor").
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	body := `{"approval_provider": "casdoor"}`
	req := httptest.NewRequest(http.MethodPut, "/api/admin/plugin-config", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.UpdatePluginConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", w.Code)
	}
	for _, k := range keys {
		if after := os.Getenv(k); after != before[k] {
			t.Fatalf("env %s changed by PUT: %q -> %q — os.Setenv side effect reintroduced", k, before[k], after)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations not met: %v", err)
	}
}

// TestPluginConfigPutEmptyBodyKeepsAll 钉 merge 极端形态：全空请求 = 无操作
// 语义（四槽位都保持现状，但仍然整行落库 + 幂等切换回当前值）。旧实现这里
// 是 500（空串撞 plugin_config 的 CHECK 约束）。
func TestPluginConfigPutEmptyBodyKeepsAll(t *testing.T) {
	h, mock, reg := newPluginConfigTestFixture(t)

	mock.ExpectExec("INSERT INTO plugin_config").
		WithArgs("native", "native", "native", "native").
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	req := httptest.NewRequest(http.MethodPut, "/api/admin/plugin-config", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	h.UpdatePluginConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT {} status = %d, want 200 (old code would 500 on CHECK constraint)", w.Code)
	}
	if cfg := reg.Config(); cfg.AuthProvider != "native" || cfg.ApprovalProvider != "native" {
		t.Fatalf("empty PUT changed config: %+v", cfg)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations not met: %v", err)
	}
}

// TestPluginConfigDBNilQuerierIsNil 钉 typed-nil 守卫：db 与注入面都为 nil
// ⇒ configQuerier 返回 nil 接口（Go 经典坑：typed-nil 装进接口后 != nil，
// 判定必须在具体类型上做 —— savePluginConfig 的 "database not available"
// 分支才能被触达而不是 panic）。
func TestPluginConfigDBNilQuerierIsNil(t *testing.T) {
	reg := authplugin.NewRegistry(nil)
	h := NewPluginConfigHandler(reg, (*pgxpool.Pool)(nil))
	h.setPluginConfigDB(nil)

	if q := h.configQuerier(); q != nil {
		t.Fatalf("configQuerier with nil pool and nil override = %v, want nil (typed-nil guard)", q)
	}
}
