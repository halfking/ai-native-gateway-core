package native

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/admin"
)

// auth_revocation_guard_test.go —— native 插件的**令牌吊销判定**守卫。
//
// ## 背景（2026-11-17 真实网关 + 全仓 SQL 扫描实测）
//
// native 的 `isTokenRevoked` 此前查 `users.password_changed_at` ——
// **全仓没有任何迁移创建这一列**（吊销纪元实际在 admin 面的 settings_kv 行里）
// ⇒ 查询必然报错 ⇒ 旧实现按「读失败一律放行」返回 false
// ⇒ **改密之后旧 JWT 一直存活到 TTL** ⇒ 吊销在插件这条路径上形同虚设。
//
// ★ 这类缺陷的隐蔽之处：**它不报错**（fail-open 把它吃掉了），
//   而且**单测全绿**——旧单测用 pgxmock mock 了那条查询，等于把坏契约钉住。
//   ⇒ 只有「这个列到底存不存在」这种事实能揭穿它。
//
// ## 两条腿
//
//   - 静态腿：包内不得再出现 `password_changed_at`（没有任何迁移创建它）。
//   - 真库腿：native 的判定与 admin 面的判定必须**逐例一致**
//     （同一把尺子 ⇒ 口径不会漂）。

// TestAuthProvider_NoPasswordChangedAtColumn is the static leg.
//
// 判据：auth_provider.go 不得在**代码**里引用 `users.password_changed_at` ——
// 它只存在于旧实现里，而**没有任何迁移创建它** ⇒ 真机上必然查询报错。
//
// ★ user_manager.go 的 `password_changed_at` 是**写入侧**（UPDATE users SET ...），
//   属于合法用途，不拦截。
// ★ 注释中的引用不拦截（记录历史问题 + 修复原因）。
func TestAuthProvider_NoPasswordChangedAtColumn(t *testing.T) {
	files := []string{"auth_provider.go", "jwt.go"}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			// Skip comments (lines starting with //)
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(line, "password_changed_at") {
				t.Errorf("%s:%d 仍在代码中引用 password_changed_at ⇒ 全仓没有任何迁移创建这一列 "+
					"⇒ 真机上查询必然报错 ⇒ 旧实现会 fail-open 成「永不吊销」；"+
					"请改用 admin.RevokedByPasswordChange（与 admin 面共用同一把尺子）", f, i+1)
			}
		}
	}
}

// TestAuthProvider_RevocationMatchesAdminPlane is the real-DB leg.
//
// ⇒ 不抄 SQL、也不自己实现一套判定 ⇒ 直接比**两个实现**在同一批输入上的
//
//	结论：native 的 isTokenRevoked 必须与 admin 面的
//	RevokedByPasswordChange **完全一致**。
//
// ⇒ 旧实现（永远 false）在这个比对面上会与 admin 面不一致 ⇒ 判据有区分力。
func TestAuthProvider_RevocationMatchesAdminPlane(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— 本次**没有**跑真库吊销一致性验证" +
			"（跳过不构成证据）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("连不上真库（%v）⇒ 本次没跑真库吊销一致性验证", err)
	}
	t.Cleanup(pool.Close)

	provider, err := NewNativeAuthProvider(pool, "secret")
	if err != nil {
		t.Fatalf("NewNativeAuthProvider: %v", err)
	}

	now := time.Now()
	// 覆盖三种签发时刻：远早于任何纪元、就在当下、晚于任何纪元。
	issuedAts := []time.Time{now.Add(-365 * 24 * time.Hour), now, now.Add(24 * time.Hour)}
	// 用一个不存在的用户 id：只读判定，不会写入任何东西。
	const userID = 999999
	for _, issuedAt := range issuedAts {
		want := admin.RevokedByPasswordChange(ctx, pool, userID, issuedAt)
		got := provider.isTokenRevoked(ctx, userID, issuedAt)
		if got != want {
			t.Errorf("native 与 admin 面的吊销判定不一致：issuedAt=%s native=%v admin=%v "+
				"⇒ 两边口径会漂（native 侧应复用 admin.RevokedByPasswordChange）",
				issuedAt.Format(time.RFC3339), got, want)
		}
	}
}
