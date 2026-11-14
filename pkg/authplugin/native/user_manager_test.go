package native

import (
	"context"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

func TestNativeUserManager_SupportsFeature(t *testing.T) {
	m := &NativeUserManager{}
	tests := []struct {
		feature authplugin.UserFeature
		want    bool
	}{
		{authplugin.FeatureUserSearch, true},
		{authplugin.FeatureUserGroups, true},
		{authplugin.FeatureUserAttributes, false},
		{authplugin.FeatureBulkOperations, false},
	}
	for _, tt := range tests {
		if got := m.SupportsFeature(tt.feature); got != tt.want {
			t.Errorf("SupportsFeature(%v) = %v, want %v", tt.feature, got, tt.want)
		}
	}
}

func TestNativeUserManager_Name(t *testing.T) {
	m := &NativeUserManager{}
	if got := m.Name(); got != "native" {
		t.Errorf("Name() = %v, want %v", got, "native")
	}
}

func TestNativeUserManager_GetUserByExternalID_InvalidFormat(t *testing.T) {
	m := &NativeUserManager{}
	ctx := context.Background()

	_, err := m.GetUserByExternalID(ctx, "invalid")
	if err != authplugin.ErrNotFound {
		t.Errorf("GetUserByExternalID(invalid) should return ErrNotFound")
	}
}

func TestNativeUserManager_CreateUser_Validation(t *testing.T) {
	m := &NativeUserManager{}
	ctx := context.Background()

	_, err := m.CreateUser(ctx, &authplugin.CreateUserRequest{
		Username: "",
		Password: "password123",
	})
	if err == nil {
		t.Errorf("CreateUser with empty username should fail")
	}

	_, err = m.CreateUser(ctx, &authplugin.CreateUserRequest{
		Username: "testuser",
		Password: "short",
	})
	if err == nil {
		t.Errorf("CreateUser with short password should fail")
	}
}

// F-A1 钉测（2026-11-14 24h 审计轮）：NativeUserManager 密码入口必须与
// admin/users.go 同一复杂度策略（≥8 位 + 大写 + 小写 + 数字）——首版只查
// len>=8，「保留所有原有逻辑」的提交声明与事实不符。变异臂：把
// user_manager.go 三处 ValidatePasswordComplexity 摘回 len>=8 ⇒ 本测试红。
func TestNativeUserManager_PasswordComplexityGuard(t *testing.T) {
	m := &NativeUserManager{}
	ctx := context.Background()

	weak := []string{
		"alllowercase1",    // 无大写
		"ALLUPPERCASE1",    // 无小写
		"NoDigitsHere",     // 无数字
		"short1A",          // <8 位（原首版即拒，同口径保留）
	}
	for _, pw := range weak {
		if _, err := m.CreateUser(ctx, &authplugin.CreateUserRequest{Username: "u", Password: pw}); err == nil {
			t.Fatalf("CreateUser 弱密码 %q 必须被复杂度策略拒绝", pw)
		}
		if err := m.ResetPassword(ctx, 1, pw); err == nil {
			t.Fatalf("ResetPassword 弱密码 %q 必须被复杂度策略拒绝", pw)
		}
		if err := m.ChangePassword(ctx, 1, "old", pw); err == nil {
			t.Fatalf("ChangePassword 弱密码 %q 必须被复杂度策略拒绝", pw)
		}
	}
}

// F-A1 钉测②：密码复杂度拒门发生在任何 DB 访问之前（m.db 为 nil ⇒ 若
// 走到 DB 路径必然 panic/空指针；弱密码必须先被策略拒绝而非依赖 DB 态）。
func TestNativeUserManager_WeakPasswordRejectedBeforeDB(t *testing.T) {
	m := &NativeUserManager{} // db=nil: 复杂度拒门必须先于 QueryRow
	ctx := context.Background()
	if err := m.ResetPassword(ctx, 1, "weakpass"); err == nil || err.Error() == "" {
		// "weakpass" 无大写 ⇒ 必须被复杂度策略拒绝（不是 DB 错误）
		t.Fatalf("ResetPassword 弱密码必须由复杂度策略拒绝, got=%v", err)
	} else if !strings.Contains(err.Error(), "password") {
		t.Fatalf("拒因必须是密码策略而非 DB 故障, got=%v", err)
	}
}
