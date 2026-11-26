package authplugin

import (
	"context"
	"sync"
	"testing"
)

// ── 2026-11-27 L135 轮（断点③修复）SwitchConfig 热切换语义钉 ──
//
// 背景：合并双 registry 链后，plugin-config PUT 的热切换入口就是本方法。
// 判据必须钉死三件事：
//   ① 切换后 Get*Provider 立即解析到新选择（不重启）；
//   ② 空参数 = 保持该槽位现状（局部更新语义）；
//   ③ 并发读写下不撕裂（Config() 快照与 Get* 原子性）。
//
// 注意：Registry 的 Get*Provider 按「选中名」从注册表取 provider，本测试用
// 最小 stub provider 注册 native/casdoor 两个名字，验证的是**选择器**语义，
// 不涉及任何真实 provider 构造。

// stubAuthProvider / stubUserManager / stubTenantManager / stubApprovalProvider
// 是四个接口的最小实现，只携带名字。
type stubNamed struct{ name string }

func (s stubNamed) Name() string { return s.name }

type stubAuthProvider struct{ stubNamed }

func (stubAuthProvider) Authenticate(ctx context.Context, req *AuthRequest) (*AuthResult, error) {
	return nil, ErrInvalidCredentials
}
func (stubAuthProvider) ValidateToken(ctx context.Context, token string) (*TokenClaims, error) {
	return nil, ErrInvalidToken
}
func (stubAuthProvider) RefreshToken(ctx context.Context, refreshToken string) (*AuthResult, error) {
	return nil, ErrInvalidToken
}
func (stubAuthProvider) Logout(ctx context.Context, token string) error { return nil }
func (stubAuthProvider) SupportsFeature(f AuthFeature) bool             { return false }

type stubUserManager struct{ stubNamed }

func (stubUserManager) GetUser(ctx context.Context, userID int) (*User, error) {
	return nil, ErrNotFound
}
func (stubUserManager) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	return nil, ErrNotFound
}
func (stubUserManager) GetUserByExternalID(ctx context.Context, externalID string) (*User, error) {
	return nil, ErrNotFound
}
func (stubUserManager) ListUsers(ctx context.Context, filter *UserFilter) ([]*User, error) {
	return nil, nil
}
func (stubUserManager) CreateUser(ctx context.Context, req *CreateUserRequest) (*User, error) {
	return nil, nil
}
func (stubUserManager) UpdateUser(ctx context.Context, userID int, req *UpdateUserRequest) (*User, error) {
	return nil, nil
}
func (stubUserManager) DeleteUser(ctx context.Context, userID int) error { return nil }
func (stubUserManager) ResetPassword(ctx context.Context, userID int, newPassword string) error {
	return nil
}
func (stubUserManager) ChangePassword(ctx context.Context, userID int, oldPassword, newPassword string) error {
	return nil
}
func (stubUserManager) UpdateLastLogin(ctx context.Context, userID int) error { return nil }
func (stubUserManager) SupportsFeature(feature UserFeature) bool              { return false }

type stubTenantManager struct{ stubNamed }

func (stubTenantManager) GetTenant(ctx context.Context, tenantID string) (*Tenant, error) {
	return nil, ErrNotFound
}
func (stubTenantManager) ListTenants(ctx context.Context, filter *TenantFilter) ([]*Tenant, error) {
	return nil, nil
}
func (stubTenantManager) CreateTenant(ctx context.Context, req *CreateTenantRequest) (*Tenant, error) {
	return nil, nil
}
func (stubTenantManager) UpdateTenant(ctx context.Context, tenantID string, req *UpdateTenantRequest) (*Tenant, error) {
	return nil, nil
}
func (stubTenantManager) DeleteTenant(ctx context.Context, tenantID string) error {
	return nil
}
func (stubTenantManager) EnableTenant(ctx context.Context, tenantID string) error  { return nil }
func (stubTenantManager) DisableTenant(ctx context.Context, tenantID string) error { return nil }
func (stubTenantManager) SupportsFeature(feature TenantFeature) bool               { return false }

type stubApprovalProvider struct{ stubNamed }

func (stubApprovalProvider) GetConfig(ctx context.Context, tenantID string) (*ApprovalConfig, error) {
	return nil, nil
}
func (stubApprovalProvider) UpdateConfig(ctx context.Context, tenantID string, config *ApprovalConfig) error {
	return nil
}
func (stubApprovalProvider) GetApprovers(ctx context.Context, tenantID string) ([]*Approver, error) {
	return nil, nil
}
func (stubApprovalProvider) AddApprover(ctx context.Context, tenantID string, approver *Approver) error {
	return nil
}
func (stubApprovalProvider) UpdateApprover(ctx context.Context, tenantID, userID string, approver *Approver) error {
	return nil
}
func (stubApprovalProvider) RemoveApprover(ctx context.Context, tenantID, userID string) error {
	return nil
}
func (stubApprovalProvider) GetRules(ctx context.Context, tenantID string) ([]*ApprovalRule, error) {
	return nil, nil
}
func (stubApprovalProvider) AddRule(ctx context.Context, tenantID string, rule *ApprovalRule) error {
	return nil
}
func (stubApprovalProvider) RemoveRule(ctx context.Context, tenantID, ruleName string) error {
	return nil
}
func (stubApprovalProvider) SupportsFeature(feature ApprovalFeature) bool { return false }

// newHotSwitchTestRegistry builds a registry with both native and casdoor
// stubs registered in all four slots, selection = all native.
func newHotSwitchTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry(&PluginConfig{
		AuthProvider:     "native",
		UserManager:      "native",
		TenantManager:    "native",
		ApprovalProvider: "native",
	})
	for _, name := range []string{"native", "casdoor"} {
		if err := r.RegisterAuthProvider(stubAuthProvider{stubNamed{name}}); err != nil {
			t.Fatalf("register auth provider %s: %v", name, err)
		}
		if err := r.RegisterUserManager(stubUserManager{stubNamed{name}}); err != nil {
			t.Fatalf("register user manager %s: %v", name, err)
		}
		if err := r.RegisterTenantManager(stubTenantManager{stubNamed{name}}); err != nil {
			t.Fatalf("register tenant manager %s: %v", name, err)
		}
		if err := r.RegisterApprovalProvider(stubApprovalProvider{stubNamed{name}}); err != nil {
			t.Fatalf("register approval provider %s: %v", name, err)
		}
	}
	return r
}

// TestSwitchConfigHotSwitchResolvesImmediately 钉判据①：切换后 Get*Provider
// 立即返回新选中的 provider —— 这正是 plugin-config PUT「不重启生效」的
// 底层契约。
func TestSwitchConfigHotSwitchResolvesImmediately(t *testing.T) {
	r := newHotSwitchTestRegistry(t)

	// 前置：初始选择是 native
	p, err := r.GetAuthProvider()
	if err != nil || p.Name() != "native" {
		t.Fatalf("precondition: initial auth provider = %v/%v, want native", p, err)
	}

	r.SwitchConfig("casdoor", "casdoor", "casdoor", "casdoor")

	p, err = r.GetAuthProvider()
	if err != nil || p.Name() != "casdoor" {
		t.Fatalf("after SwitchConfig: auth provider = %v (err=%v), want casdoor", p, err)
	}
	u, err := r.GetUserManager()
	if err != nil || u.Name() != "casdoor" {
		t.Fatalf("after SwitchConfig: user manager = %v (err=%v), want casdoor", u, err)
	}
	tm, err := r.GetTenantManager()
	if err != nil || tm.Name() != "casdoor" {
		t.Fatalf("after SwitchConfig: tenant manager = %v (err=%v), want casdoor", tm, err)
	}
	ap, err := r.GetApprovalProvider()
	if err != nil || ap.Name() != "casdoor" {
		t.Fatalf("after SwitchConfig: approval provider = %v (err=%v), want casdoor", ap, err)
	}

	// Config() 快照也要反映新选择
	cfg := r.Config()
	if cfg.AuthProvider != "casdoor" || cfg.ApprovalProvider != "casdoor" {
		t.Fatalf("Config() after switch = %+v, want casdoor selections", cfg)
	}
}

// TestSwitchConfigEmptyKeepsCurrent 钉判据②：空参数保持现状 —— PUT 局部
// 更新（例如只切 approval_provider）不得把其它三个槽位清掉。
func TestSwitchConfigEmptyKeepsCurrent(t *testing.T) {
	r := newHotSwitchTestRegistry(t)

	// 只切 approval 一个槽位
	r.SwitchConfig("", "", "", "casdoor")

	cfg := r.Config()
	if cfg.AuthProvider != "native" {
		t.Fatalf("auth provider changed to %q by empty arg; want native (kept)", cfg.AuthProvider)
	}
	if cfg.UserManager != "native" || cfg.TenantManager != "native" {
		t.Fatalf("user/tenant manager disturbed by empty args: %q/%q", cfg.UserManager, cfg.TenantManager)
	}
	if cfg.ApprovalProvider != "casdoor" {
		t.Fatalf("approval provider = %q, want casdoor", cfg.ApprovalProvider)
	}
	ap, err := r.GetApprovalProvider()
	if err != nil || ap.Name() != "casdoor" {
		t.Fatalf("approval provider resolve = %v (err=%v), want casdoor", ap, err)
	}
}

// TestSwitchConfigConcurrentReadWrite 在 -race 下跑：并发 SwitchConfig 与
// Get*/Config 读，验证锁域完整（读写不撕裂、快照一致）。
func TestSwitchConfigConcurrentReadWrite(t *testing.T) {
	r := newHotSwitchTestRegistry(t)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if i%2 == 0 {
					r.SwitchConfig("casdoor", "", "", "casdoor")
					r.SwitchConfig("native", "", "", "native")
				} else {
					if _, err := r.GetAuthProvider(); err != nil {
						t.Errorf("GetAuthProvider under concurrent switch: %v", err)
						return
					}
					_ = r.Config()
				}
			}
		}(i)
	}
	// 让竞争跑一小会儿
	for i := 0; i < 2000; i++ {
		r.SwitchConfig("", "", "", "")
	}
	close(stop)
	wg.Wait()
}
