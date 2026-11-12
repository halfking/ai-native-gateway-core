package admin

// monitor-summary total_requests 预物化（迁移 963，2026-11-13 24h 审计轮路径③）
// 的**静态形状门**（无 DB 依赖）。
//
// 钉住四个不变式：
//  1. 读面是双形态 CASE：表非空走 O(1) 预物化行取，空表回落旧两臂实时计数
//     ——回落分支是「口径零变化」的载体（job 未首跑/持续失败 ⇒ 行为与
//     963 之前逐字一致），被删＝963 之前的兼容面消失；
//  2. 读面回落分支的凭据谓词复用 db.SessionFamilyCredentialCountSourceSQL
//     **逐字同款**（与既有窄投影两臂门/索引同形纪律同源，不允许旁路复写）；
//  3. 读面不得回读 canonical 视图（R168 毒丸纪律延续）；
//  4. 刷新器 UPSERT 的 credential_id 表达式复用 db.SessionFamilyCredentialIDExpr
//     单点常量 + 单语句 ON CONFLICT 原子语义（空表→全量行一次可见，
//     读面 EXISTS 判定因此只有两态）。
//
// 真库等价腿（刷新后 rollup 值 == 实时两臂计数 == canonical 视图计数）在
// credential_monitor_rollup_integration_test.go（TEST_PG_URL）。
//
// 变异臂（2026-11-13 实测全红→还原绿）：删回落分支 ⇒ 红；聚合表达式退化为
// 裸 credential_id::bigint（脱离单点常量）⇒ 红；读面退回纯实时计数 ⇒ 红。

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/bg"
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

func TestMonitorSummaryTotalRequestsReadSQLShape(t *testing.T) {
	expr := dbpkg.CredentialTotalRequestsReadSQL("c.id")

	// ① 双形态：预物化分支与回落分支都必须在场。
	if !strings.Contains(expr, "EXISTS (SELECT 1 FROM public.credential_total_requests_rollup)") {
		t.Fatalf("读面缺预物化非空判定（EXISTS 门）——空表回落语义失效 ⇒ job 未首跑时端点直接读到空表 ⇒ total_requests 恒 0（口径漂移）:\n%s", expr)
	}
	if !strings.Contains(expr, "FROM public.credential_total_requests_rollup tr WHERE tr.credential_id = c.id") {
		t.Fatalf("读面缺预物化行取分支:\n%s", expr)
	}

	// ② 回落分支必须逐字包含既有窄投影两臂 source（口径等价前提①由单点承担）。
	narrow := dbpkg.SessionFamilyCredentialCountSourceSQL("c.id")
	if !strings.Contains(expr, narrow) {
		t.Fatalf("读面回落分支未复用 SessionFamilyCredentialCountSourceSQL 逐字输出——谓词旁路复写＝口径分叉（§682 同族）:\n%s", expr)
	}

	// ③ canonical 视图毒丸纪律延续（R168）。
	if strings.Contains(expr, "request_logs_with_current_month") {
		t.Fatalf("读面回读 canonical 视图 ⇒ 118 列扛回 heap scan（252 实测单凭据 14.7 s）:\n%s", expr)
	}
}

func TestCredRollupUpsertSQLShape(t *testing.T) {
	sql := bg.CredRollupUpsertSQL()

	// ④a credential_id 表达式 = SessionFamilyCredentialIDExpr 逐字（脱常量＝口径分叉）。
	if !strings.Contains(sql, dbpkg.SessionFamilyCredentialIDExpr) {
		t.Fatalf("刷新 UPSERT 的 credential_id 表达式脱离 SessionFamilyCredentialIDExpr 单点常量——与按凭据计数/索引表达式/962 函数体的同源性断裂:\n%s", sql)
	}
	// ④b 两臂都必须在场（hot ∪ 母表；缺一臂=口径砍半）。
	for _, arm := range []string{"public.session_turns_hot", "public.session_turns "} {
		if !strings.Contains(sql, arm) {
			t.Fatalf("刷新 UPSERT 缺臂 %q:\n%s", arm, sql)
		}
	}
	// ④c 单语句原子 UPSERT（空表→全量行一次可见的前提）。
	if !strings.Contains(sql, "ON CONFLICT (credential_id) DO UPDATE") {
		t.Fatalf("刷新 UPSERT 缺 ON CONFLICT 原子语义——交错/重跑会撞 duplicate key 或产生半刷新态:\n%s", sql)
	}
	if strings.Contains(sql, ";") {
		t.Fatalf("刷新面被拆成多条语句——单语句原子性被破坏（半刷新态会让读面 EXISTS 判定出第三态）:\n%s", sql)
	}
	// ④d 不允许旁路复写聚合 SQL（helper 单点）。
	if !strings.Contains(sql, dbpkg.SessionFamilyCredentialCountAggregateSQL()) {
		t.Fatalf("刷新 UPSERT 未复用 SessionFamilyCredentialCountAggregateSQL 单点聚合——聚合口径旁路:\n%s", sql)
	}
}
