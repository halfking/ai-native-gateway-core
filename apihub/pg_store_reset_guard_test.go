// 守卫：withTenantTx / withTenantReadOnlyTx 不得对已关闭的事务执行 RESET。
//
// 2026-07-16 曾在两处 defer 里加 `tx.Exec(ctx, "RESET app.current_tenant")`，
// 理由是「防止 GUC 跨事务污染连接池」。该理由基于一个不成立的前提：它假定
// 写入用的是 `SET LOCAL`（会话级残留），而 setTenantGUC 用的是
// `set_config('app.current_tenant', $1, true)` —— 第三个参数 true 即事务级作用域，
// PostgreSQL 在提交/回滚时自动撤销，**不会残留在连接上**。
//
// 保留它反而有害：defer 在 `return tx.Commit(ctx)` 之后执行，事务已关闭，
// tx.Exec 必返 pgx.ErrTxClosed，于是**每次成功调用都打一条 WARN**。
// 252 实测 3 小时 200,897 条（≈18.5 行/秒），/var/log/messages 涨到 1.2G。
//
// 这条测试存在的理由：删掉代码很容易，**把当时的错误理由一起删掉**才是重点。
// 2026-10-03 移除代码时，2026-07-16 的注释还留在函数头上描述着已经不存在的
// RESET —— 下一个读代码的人会据此以为该行为仍在。本测试与那条注释互为约束。
package apihub

import (
	"os"
	"strings"
	"testing"
)

func TestTenantTxNoResetOnClosedTransaction(t *testing.T) {
	src, err := os.ReadFile("pg_store.go")
	if err != nil {
		t.Fatalf("read pg_store.go: %v", err)
	}
	// 逐行扫，跳过注释行（本次修复留下的说明里也会提到这段字面量）。
	for i, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.Contains(line, `Exec(ctx, "RESET app.current_tenant"`) {
			t.Errorf("pg_store.go:%d still executes RESET on a possibly-committed tx:\n%s\n"+
				"app.current_tenant is set with set_config(..., true) (transaction-local), "+
				"so it never outlives the transaction and the RESET is both unnecessary "+
				"and guaranteed to fail on the success path (pgx.ErrTxClosed)", i+1, trimmed)
		}
	}
}
