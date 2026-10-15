//go:build !integration

package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 全量 API 扫描轮（2026-10-14）修掉的两个 500 的形状钉桩。
// 两个都是「运行时才炸、单测链路看不见」的形态：NULL 扫描在真库有
// NULL 属主行时才触发；conn busy 在连接池归还带未关闭 rows 的连接时
// 才触发。pgxmock 层复现不了这两条（mock 无池、无真 NULL 数据），
// ⇒ 钉源码形状（与 v1_bodies_read_path 同族的做法）。

// TestUserProfileListFiltersNullOwner 钉 /api/admin/session-analytics/users
// 的 NULL 属主过滤：真库 session_owners 含 owner_user 为 NULL 的行（聚合
// 表由触发器喂养，无属主行合法存在），裸 string 扫描遇 NULL 直接
// 「rows.Err() 携带扫描错误 → 500」（实测 writeAggRowsErr:34 抛出）。
// COUNT(DISTINCT owner_user) 本就忽略 NULL ⇒ 过滤同时修 total/列表口径分叉。
func TestUserProfileListFiltersNullOwner(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("user_profile.go"))
	if err != nil {
		t.Fatalf("read user_profile.go: %v", err)
	}
	src := string(b)
	// 三层隔离的每个分支都必须带上 NULL 过滤（count 与 list 共用 baseWhere）。
	// count==4：regular / tenant_admin 带租户 / tenant_admin 默认 / super 带参
	// + super 无参 —— 拼接点分散，钉足数而不是 Contains。
	// 钉 nullOwnerFilter 的引用数（1 次构造 + 4 个隔离分支引用），
	// 而不是字面量出现数：把引用改回内联裸条件（丢过滤）必须红。
	// count==N（判据纪律）：本文件 1 处构造 + 5 处分支引用 = 6。少了
	// 任何一个分支的过滤，NULL 属主行都会让该分支 500。
	if got := strings.Count(src, "nullOwnerFilter"); got != 6 {
		t.Errorf("nullOwnerFilter 出现 %d 次（期望恰好 6：构造 1 + 分支 5）——"+
			"有分支丢了 NULL 过滤或多出了无主过滤的裸分支", got)
	}
	if !strings.Contains(src, `nullOwnerFilter := "so.owner_user IS NOT NULL"`) {
		t.Errorf("nullOwnerFilter 的定义形态变了——判据与实现要同层（改实现请同步判据）")
	}
}

// TestSessionAuditExportConsumesRowsInsideTx 钉 /api/admin/session-audit/export
// 的 rows 生命周期：旧形态把 rows 带出 withTenantTx 闭包，事务提交时连接
// 携带未关闭的 rows 归还池 ⇒ 同一连接的下一次使用 "conn busy"（本地实测
// 100% 复现 500）。正确形态 = 闭包内 Query→Scan→Close（list 处理器 :167
// 一直是这个形状）。
func TestSessionAuditExportConsumesRowsInsideTx(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("session_audit.go"))
	if err != nil {
		t.Fatalf("read session_audit.go: %v", err)
	}
	src := string(b)
	// 只钉 export 函数体：stats 处理器（:325）的 `var rows pgx.Rows` 在
	// 闭包**内**且显式 Close，是安全形态——全文件一刀切会误伤它。
	start := strings.Index(src, "func (h *Handler) handleSessionAuditExport")
	if start < 0 {
		t.Fatalf("handleSessionAuditExport 不在 session_audit.go 里——函数改名了？判据要跟着搬")
	}
	end := strings.Index(src[start:], "\nfunc ")
	body := src[start:]
	if end > 0 {
		body = body[:end]
	}
	// 旧缺陷形态的标志：闭包外声明 var rows pgx.Rows —— export 里它回来了就必须红。
	if strings.Contains(body, "var rows pgx.Rows") {
		t.Errorf("handleSessionAuditExport 仍有闭包外的 `var rows pgx.Rows` —— rows 带出事务" +
			"闭包 = 连接带未关闭 rows 归还池 = conn busy（export 500 的根因形态）")
	}
	// export 的消费必须发生在 withTenantTx 之内：闭包内 defer rows.Close()。
	if !strings.Contains(body, "defer rows.Close()") {
		t.Errorf("handleSessionAuditExport 没有闭包内 defer rows.Close() —— " +
			"rows 生命周期又跑出了事务闭包（conn busy 根因形态）")
	}
}
