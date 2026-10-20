package admin

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// ★ 2026-10-20（迁移 890，v1 族真删）：本文件的两道门**改名并反转**。
//
// # 退役了什么
//
// `resetCredentialSuccessRateRows` 原本做的事是：在一个设了 super_admin GUC 的
// 事务里 `DELETE FROM _request_logs_hot WHERE credential_id = ? AND …`，把某个
// (凭据, 模型) 对的**失败请求日志行**删掉，好让成功率统计立刻重算。
//
// 890 删掉了 v1 请求日志族 ⇒ **没有可删的行**了。
//
// # 为什么没有等价物（这是 (b) 而不是 (c) 的分界）
//
// 会话族（session_turns）是**会话本体**，不是派生日志：删一行 = 少一轮对话
// = 丢掉用户可见的历史。而「重置成功率」这个运维动作的语义是「让统计立刻
// 重算」，不是「销毁对话」。
//
// ⇒ 所以**没有**一个等价物可以承接它：删 v1 行（已无对象）／删会话行（语义
// 不对，会毁数据）。
//
// ⇒ 处置：本端点退化为「**不删任何行**、照常回读当前成功率」
// （resetCredentialSuccessRateRows 返回 (0, nil)）。
//
// # 门为什么必须留下
//
// 「不删」这件事本身需要被钉住，否则下一次有人「顺手把它改成删会话族」
// 是零成本的，而代价是**静默销毁用户对话历史**。⇒ 换成守新不变量的门：
//
//	① 它不得发起**任何** DB 调用（无 Begin、无 Exec）；
//	② 它的源码里不得出现任何已退役 v1 族关系名；
//	③ 它的源码里不得出现针对**会话族**的 DELETE（会话本体不可删）。
//
// ⚠ 原门守的是「DELETE 必须跑在 super_admin GUC 事务内」——那是 v1 表还在
// 时的**正确**要求。现在没有 DELETE 了 ⇒ 那条要求**不再有对象**
// （留着一条「必须跑在 GUC 事务内」的门，会逼着下一个人把 DELETE 加回来）。
//
// 原门名与历史登记（docs/audit/2026-09-17-r38-rls-design-ensure-index-root-round.md
// 的 PASS 清单）保留在历史文档里作为**当时的记录**；本轮的改名记在
// docs/audit/2026-10-20-request-logs-session-turns-复核轮.md。

// TestResetCredentialSuccessRateRowsIsANoOpAfterV1Retirement 钉住「它什么都不做」。
//
// 判据打在**行为**上：pgxmock 零期望 ⇒ 生产侧一旦发起 Begin/Exec，pgxmock
// 返回「未预期的调用」错误 ⇒ 本门立刻红。
func TestResetCredentialSuccessRateRowsIsANoOpAfterV1Retirement(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	// ⚠ 零期望：任何 Begin / Exec / Query 都是「未预期的调用」。
	deleted, err := resetCredentialSuccessRateRows(context.Background(), mock, 17, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want 0 —— v1 族已随 890 退役，没有任何可删的行", deleted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("resetCredentialSuccessRateRows 不得发起任何 DB 调用（v1 族已退役）：%v", err)
	}
}

// TestResetCredentialSuccessRateRowsNeverTouchesTheSessionFamily 钉住
// 「不得去删会话族」。
//
// ⚠ 这是本文件里**唯一有牙齿**的那条：v1 行删不掉之后，最省事的「修复」是
// 改成删会话族行（会话族确实有同名列）⇒ 而那会**静默销毁用户对话历史**。
// ⇒ 这条门必须能抓住它。
//
// 阳性对照（就地）：同一把尺子必须能认出「删会话族」的形态，
// 否则上面那条负向断言恒绿。
func TestResetCredentialSuccessRateRowsNeverTouchesTheSessionFamily(t *testing.T) {
	src, err := os.ReadFile("credential_success_rate.go")
	if err != nil {
		t.Fatalf("read credential_success_rate.go: %v", err)
	}
	body := resetCredentialSuccessRateRowsBody(string(src))

	// ① 不得出现已退役 v1 族关系名（它已经没有可删的对象了）。
	if hits := retiredV1RelationsInSQL(body); len(hits) > 0 {
		t.Errorf("resetCredentialSuccessRateRows 仍引用已退役 v1 族关系名 %v —— v1 族已随 890 被 DROP", hits)
	}
	// ② 不得对**会话族**执行 DELETE（会话本体不可删）。
	for _, forbidden := range []string{"DELETE FROM session_turns", "DELETE FROM public.session_turns",
		"DELETE FROM session_bodies", "DELETE FROM public.session_bodies"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Errorf("resetCredentialSuccessRateRows 不得删会话族（%q）—— 会话族是会话本体，"+
				"删一行 = 少一轮对话 = 静默销毁用户可见的历史；「重置成功率」没有等价物", forbidden)
		}
	}

	// 就地阳性对照：同一把尺子必须能认出「删 v1」与「删会话族」两种形态。
	if hits := retiredV1RelationsInSQL("DELETE FROM _request_logs_hot WHERE credential_id = $1"); len(hits) == 0 {
		t.Error("retiredV1RelationsInSQL 认不出 v1 形态 —— 负向断言已恒绿，判据坏了")
	}
	if !strings.Contains(strings.ToLower("DELETE FROM session_turns WHERE credential_id = $1"), "delete from session_turns") {
		t.Error("阳性对照自身写错了")
	}
}

// resetCredentialSuccessRateRowsBody 取该函数体的源码文本（去注释）。
func resetCredentialSuccessRateRowsBody(src string) string {
	i := strings.Index(src, "func resetCredentialSuccessRateRows")
	if i < 0 {
		return ""
	}
	rest := src[i:]
	if j := strings.Index(rest[1:], "\nfunc "); j >= 0 {
		rest = rest[:j+1]
	}
	return stripGoComments(rest)
}
