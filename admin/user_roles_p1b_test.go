package admin

// users.role 多角色化判据（三级配额制 P1-b，2026-11-27）。
//
// 契约四条：
//   ① SignTokenWithRoles：Roles[0]=主角色前置写入 claim；nil roles ⇒ token
//      与旧形态逐字节同形（只写 Role）；
//   ② VerifyToken：旧 token（无 Roles claim）解析后 EffectiveRoles 回落 [Role]；
//   ③ JWTClaims → AuthContext 透传后 HasRole 按集合判；legacy（Roles 空）回落
//      Role 单值；
//   ④ effectiveLoginRoles / normalizeUserRoles：主角色前置 + 去重保序。

import (
	"testing"
)

func testJWTSecretP1b() string { return "p1b-test-secret" }

// ① 新 token：roles 全量写入 + 主角色前置。
func TestSignTokenWithRolesWritesRolesClaim(t *testing.T) {
	token, _, err := SignTokenWithRoles(7, "default", "multi", "tenant_admin", []string{"org_x", "tenant_admin"}, testJWTSecretP1b(), false)
	if err != nil {
		t.Fatalf("SignTokenWithRoles: %v", err)
	}
	claims, err := VerifyToken(token, testJWTSecretP1b())
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if claims.Role != "tenant_admin" {
		t.Fatalf("primary Role = %q, want tenant_admin (主角色前置)", claims.Role)
	}
	// 主角色恒前置（契约）：调用方传 [org_x, tenant_admin]、主角色 tenant_admin
	// ⇒ 有效集 = [tenant_admin, org_x]（主角色在前，其余保序）。
	if len(claims.Roles) != 2 || claims.Roles[0] != "tenant_admin" || claims.Roles[1] != "org_x" {
		t.Fatalf("Roles = %v, want [tenant_admin org_x]（主角色前置）", claims.Roles)
	}
}

// ①b nil roles ⇒ 与旧形态同形（只写 Role、Roles claim 不出现）。
func TestSignTokenLegacyShapeWithoutRoles(t *testing.T) {
	token, _, err := SignToken(3, "t1", "u", "tenant_admin", testJWTSecretP1b(), false)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	claims, err := VerifyToken(token, testJWTSecretP1b())
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if claims.Role != "tenant_admin" {
		t.Fatalf("Role = %q", claims.Role)
	}
	if len(claims.Roles) != 1 || claims.Roles[0] != "tenant_admin" {
		t.Fatalf("EffectiveRoles = %v, want [tenant_admin] 回落", claims.Roles)
	}
}

// ③ AuthContext.HasRole：集合判 + legacy 回落。
func TestAuthContextHasRole(t *testing.T) {
	multi := &AuthContext{Role: "tenant_admin", Roles: []string{"org_x", "tenant_admin"}}
	if !multi.HasRole("org_x") {
		t.Fatal("multi role set must contain org_x")
	}
	if !multi.HasRole("tenant_admin") {
		t.Fatal("primary role must be in set")
	}
	if multi.HasRole("super_admin") {
		t.Fatal("multi role set must not contain super_admin")
	}
	legacy := &AuthContext{Role: "tenant_admin"} // Roles 空（admin_key/legacy 形态）
	if !legacy.HasRole("tenant_admin") {
		t.Fatal("legacy must fall back to Role single value")
	}
	if legacy.HasRole("super_admin") {
		t.Fatal("legacy must not grant other roles")
	}
	var nilCtx *AuthContext
	if nilCtx.HasRole("super_admin") {
		t.Fatal("nil context grants nothing")
	}
}

// ④a effectiveLoginRoles：主角色前置 + 去重。
func TestEffectiveLoginRoles(t *testing.T) {
	if got := effectiveLoginRoles("tenant_admin", nil); len(got) != 1 || got[0] != "tenant_admin" {
		t.Fatalf("nil roles ⇒ [primary], got %v", got)
	}
	got := effectiveLoginRoles("tenant_admin", []string{"org_x", "user", "org_x", "tenant_admin"})
	if got[0] != "tenant_admin" {
		t.Fatalf("primary must be front, got %v", got)
	}
	seen := map[string]bool{}
	for _, r := range got {
		if seen[r] {
			t.Fatalf("duplicate role %q in %v", r, got)
		}
		seen[r] = true
	}
	if len(seen) != 3 {
		t.Fatalf("want 3 unique roles, got %v", got)
	}
}

// ④b normalizeUserRoles：主角色前置 + 去重保序 + 剔空。
func TestNormalizeUserRoles(t *testing.T) {
	got := normalizeUserRoles("tenant_admin", []string{"", "user", "tenant_admin", "org_x", "user"})
	want := []string{"tenant_admin", "user", "org_x"}
	if len(got) != len(want) {
		t.Fatalf("normalize = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalize[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
