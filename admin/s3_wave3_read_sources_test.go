package admin

// s3_wave3_read_sources_test.go — S3 波 3（2026-10-13，停写切换读侧第二
// 分片）形状钉桩 + 2026-10-18 停写路径撤反连接。
//
// 判据直接驱动 tenantStatsLogsTableSQL——它就是发给 PostgreSQL 的语句本体。
// 旧版从 tenants.go 源码切片还原拼接表达式；抽出函数后改同层调用，避免
// 行号漂移假红，也让「停写 / 双写」两分支都能钉。

import (
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/db"
)

// TestTenantStatsLogsTable_ServesSessionFamily pins the post-890 shape:
// session-only two-arm union, freeze-guarded on both legs, and **no v1 arm**.
//
// ★ 2026-10-20（迁移 890，v1 族真删）：原来的四臂（会话 hot/父表 + v1
// hot/父表）与**双子反连接**（`NOT EXISTS (SELECT 1 FROM _request_logs_hot h
// …)` / `… _request_logs p …`，各出现在两条会话腿上）一起消失 ——
// 反连接的对象已随 v1 族退役 ⇒ 留着就是 42P01。
//
// ⇒ 本门从「必须带双子反连接」**反转**为「必须不再出现 v1 族关系名」。
// 反转后的判据仍然有牙：反连接被谁加回来（哪怕只加一条）都会红。
func TestTenantStatsLogsTable_ServesSessionFamily(t *testing.T) {
	sql := tenantStatsLogsTableSQL(true)
	for _, leg := range []string{"FROM session_turns_hot t", "FROM session_turns t"} {
		if !strings.Contains(sql, leg) {
			t.Fatalf("logsTable must carry session leg %q; got:\n%s", leg, sql)
		}
	}
	if got := strings.Count(sql, "t.ts > "+db.V1FreezeCutoffSQL); got != 2 {
		t.Fatalf("logsTable session legs must be freeze-guarded by db.V1FreezeCutoffSQL on BOTH legs (found %d); got:\n%s", got, sql)
	}
	// ★ 反转后的负向判据：v1 族关系名必须**不在场**（按标识符边界判定 ——
	// `request_logs_with_current_month` 是 canonical 视图，**不是** v1 族，
	// 它在 890 之后仍然存在，只是内容换成会话族两臂体）。
	if hits := retiredV1RelationsInSQL(sql); len(hits) > 0 {
		t.Fatalf("logsTable 不得再出现已退役 v1 族关系名 %v —— v1 族已随 890 被 DROP，读它 = 42P01；\n%s", hits, sql)
	}
	// 就地阳性对照：同一把尺子必须能认出 v1 形态，否则上面那条负向断言恒绿。
	if hits := retiredV1RelationsInSQL("NOT EXISTS (SELECT 1 FROM _request_logs_hot h WHERE h.request_id = t.request_id)"); len(hits) == 0 {
		t.Fatal("retiredV1RelationsInSQL 认不出 v1 形态 —— 负向断言已恒绿，判据坏了")
	}
}

// TestTenantStatsLogsTable_ProjectionAndSourceUntouched pins the source
// family and the projection of the logsTable on both branches.
//
// ★ 2026-10-20（迁移 890）：本门**改名**（原 `…_V1LegsUntouched`）——
// 被守的「v1 腿」已经不存在了，留一个叫「V1LegsUntouched」的门会让人
// 以为还在守什么。改名后的不变式是同一条意思的延续：
//
//	原来：v1 两腿必须**保持裸形**（无 WHERE / 无反连接 / 不经视图）
//	现在：**没有 v1 腿了**；剩下的是会话族两腿，投影不许缩水、
//	      且任何一支都不得再指向已退役的 v1 族。
//
// ⚠ 投影不许缩水这条**不是形式**：少一列 = getTenantStats 的某个聚合
// 少算一个维度，而结果是「看起来正常的数字」。
func TestTenantStatsLogsTable_ProjectionAndSourceUntouched(t *testing.T) {
	for _, writeEnabled := range []bool{true, false} {
		sql := tenantStatsLogsTableSQL(writeEnabled)
		if strings.Contains(sql, "request_logs_with_current_month") {
			t.Fatalf("logsTable must not read the canonical view (R36-A1 30s timeout); writeEnabled=%v got:\n%s", writeEnabled, sql)
		}
		// ★ 反转：v1 族关系名必须不在场（按标识符边界）。
		if hits := retiredV1RelationsInSQL(sql); len(hits) > 0 {
			t.Fatalf("logsTable 不得再出现已退役 v1 族关系名 %v（writeEnabled=%v）—— v1 族已随 890 被 DROP：\n%s",
				hits, writeEnabled, sql)
		}
		// 会话族两腿必须在场（删一条腿 = 漏读一侧表 = 静默少算）。
		for _, leg := range []string{"FROM session_turns_hot t", "FROM session_turns t"} {
			if !strings.Contains(sql, leg) {
				t.Fatalf("logsTable must carry session leg %q (writeEnabled=%v); got:\n%s", leg, writeEnabled, sql)
			}
		}
		for _, col := range []string{"tenant_id", "application_id", "outbound_model", "client_model",
			"success", "total_tokens", "prompt_tokens", "completion_tokens",
			"credits_charged", "cost_usd", "latency_ms", "cache_read_tokens", "cache_write_tokens"} {
			if !strings.Contains(sql, col) {
				t.Fatalf("logsTable projection must keep column %q writeEnabled=%v; got:\n%s", col, writeEnabled, sql)
			}
		}
	}
}

// TestTenantStatsLogsTable_BothBranchesAreIdentical pins the post-890
// operating point: the two branches of the logsTable builder are **the same
// statement**.
//
// ★ 2026-10-20（迁移 890）：本门**改名**（原
// `…_StopWriteDropsTwinAntiJoin`）。原判据是「停写支必须去掉双子反连接、
// 而双写支必须保留」—— 反连接的对象（v1 族）已随 890 退役 ⇒ 两支都
// 不再带反连接 ⇒ 那条判据的两半（保留 / 去掉）同时失效。
//
// ⇒ 换成等价且更强的不变式：**`writeEnabled` 不再是语义参数**。
// 它原来控制「会话腿要不要反连接 v1 双子」，那个对象没了 ⇒ 两种取值
// 必须产出**逐字相同**的语句。若哪天有人按旧语义把它加回来（哪怕只在一支
// 上加），这里立刻红 —— 而且红的是「两支不再等价」这个更根本的事实。
func TestTenantStatsLogsTable_BothBranchesAreIdentical(t *testing.T) {
	sql := tenantStatsLogsTableSQL(false)
	if got := strings.Count(sql, "t.ts > "+db.V1FreezeCutoffSQL); got != 2 {
		t.Fatalf("stop-write path must still freeze-guard BOTH session legs (found %d)", got)
	}
	// ★ 反转后的核心判据：两支必须逐字相同。
	dual := tenantStatsLogsTableSQL(true)
	if dual != sql {
		t.Fatalf("writeEnabled 已是历史参数（v1 反连接对象随 890 退役）⇒ 两支必须产出同一条语句；\n"+
			"writeEnabled=true:\n%s\nwriteEnabled=false:\n%s", dual, sql)
	}
	// 两支都不得再指向 v1 族。
	if hits := retiredV1RelationsInSQL(sql); len(hits) > 0 {
		t.Fatalf("logsTable 不得再出现已退役 v1 族关系名 %v —— v1 族已随 890 被 DROP，读它 = 42P01：\n%s", hits, sql)
	}
	// 就地阳性对照：同一把尺子必须能认出 v1 形态。
	if hits := retiredV1RelationsInSQL("NOT EXISTS (SELECT 1 FROM _request_logs p WHERE p.request_id = t.request_id)"); len(hits) == 0 {
		t.Fatal("retiredV1RelationsInSQL 认不出 v1 形态 —— 负向断言已恒绿，判据坏了")
	}
}
