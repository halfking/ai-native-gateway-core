package admin

// s3_wave3_read_sources_test.go — S3 波 3（2026-10-13，停写切换读侧第二
// 分片）形状钉桩。
//
// 本轮迁移的三个读点之一在本包：admin/tenants.go getTenantStats 的
// logsTable。判据与波 2 同一纪律：**断言驱动包级 SQL 常量/变量的文本**
// ——它们就是发给 PostgreSQL 的语句本体。
//
// 另两个读点分别在自己的包：
//   - admin/routing.go popularModelsHotSQL → routing_popular_models_test.go
//     （波 3 重钉）；
//   - bg/auto_index_refresher.go rollupCredentialModelIndexSQL →
//     bg/auto_index_refresher_s3_wave3_test.go。

import (
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/db"
)

// tenantStatsLogsTable 提取 getTenantStats 内拼接后的 logsTable 文本。
// 赋值右值由多个反引号段经 `+ db.V1FreezeCutoffSQL +` 拼成，终止于
// sqlreadguard 行注释前的最后一个反引号；这里取整段后把拼接表达式
// 还原成运行时文本。
func tenantStatsLogsTable(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("tenants.go")
	if err != nil {
		t.Fatalf("read tenants.go: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "logsTable := `")
	if start == -1 {
		t.Fatal("logsTable assignment not found in tenants.go")
	}
	rest := body[start:]
	end := strings.Index(rest, "\n\tusageTable :=")
	if end == -1 {
		t.Fatal("logsTable assignment end (usageTable follows) not found")
	}
	segment := rest[:end]
	// 段尾的 Go 反引号收笔（含内联的 -- sqlreadguard SQL 注释）不属于 SQL 文本。
	segment = strings.TrimSuffix(strings.TrimRight(segment, " \t\n"), "`")
	return strings.ReplaceAll(segment, "` + db.V1FreezeCutoffSQL + `", db.V1FreezeCutoffSQL)
}

// TestTenantStatsLogsTable_ServesSessionFamily pins the S3-wave-3 shape:
// stats aggregates must read a four-arm union — two session-family legs
// (freeze-guarded + twin-excluded) ahead of the two untouched v1 legs.
//
// 为什么必须有会话腿：S4 停写后 v1 双腿只剩冻结历史，新流量对 stats
// 渐进全盲（audit §5 登记债，本波清偿）。
func TestTenantStatsLogsTable_ServesSessionFamily(t *testing.T) {
	sql := tenantStatsLogsTable(t)
	for _, leg := range []string{"FROM session_turns_hot t", "FROM session_turns t"} {
		if !strings.Contains(sql, leg) {
			t.Fatalf("logsTable must carry session leg %q; got:\n%s", leg, sql)
		}
	}
	// 冻结点守卫：没有它，双写期（回滚/生产门开）两族并存会整窗双计。
	// 两条会话腿都必须带（count==2，只查在场会被「删一条留一条」的变异骗过）。
	if got := strings.Count(sql, "t.ts > "+db.V1FreezeCutoffSQL); got != 2 {
		t.Fatalf("logsTable session legs must be freeze-guarded by db.V1FreezeCutoffSQL on BOTH legs (found %d); got:\n%s", got, sql)
	}
	// 双子排除：镜像 ts 是落库侧时刻（实测差 ~15s），翻转边界存在
	// 「v1 双子 + 晚落镜像」行，没有 NOT EXISTS 会双计（真库实测 1 例）。
	for _, probe := range []string{
		"NOT EXISTS (SELECT 1 FROM _request_logs_hot h WHERE h.request_id = t.request_id)",
		"NOT EXISTS (SELECT 1 FROM _request_logs p WHERE p.request_id = t.request_id)",
	} {
		if strings.Count(sql, probe) != 2 {
			t.Fatalf("logsTable must twin-exclude BOTH session legs with %q (found %d); got:\n%s",
				probe, strings.Count(sql, probe), sql)
		}
	}
}

// TestTenantStatsLogsTable_V1LegsUntouched pins the R36-A1 adjudicated v1
// shape: the two v1 legs stay as bare table scans (hot ∪ parent) — the
// 2026-09-30 252-dev 30s-timeout adjudication forbids hanging the view /
// anti-join off the v1 side, and this shard must not quietly re-add it.
func TestTenantStatsLogsTable_V1LegsUntouched(t *testing.T) {
	sql := tenantStatsLogsTable(t)
	if strings.Contains(sql, "request_logs_with_current_month") {
		t.Fatalf("logsTable must not read the canonical view (R36-A1 30s timeout); got:\n%s", sql)
	}
	// v1 腿不承载任何谓词：去重责任全在会话腿。检查剥掉 `--` 行注释后
	// 的纯 SQL——内联的 sqlreadguard 注释文本本身含有「NOT EXISTS」字样，
	// 不剥会误触。
	v1Part := sql
	if idx := strings.Index(v1Part, "FROM _request_logs_hot\n"); idx > 0 {
		v1Part = v1Part[idx:]
	}
	var bare strings.Builder
	for _, line := range strings.Split(v1Part, "\n") {
		if cut := strings.Index(line, "--"); cut >= 0 {
			line = line[:cut]
		}
		bare.WriteString(line)
		bare.WriteByte('\n')
	}
	v1SQL := bare.String()
	for _, banned := range []string{"NOT EXISTS", "V1FreezeCutoff", "WHERE"} {
		if strings.Contains(v1SQL, banned) {
			t.Fatalf("v1 legs must stay bare (freeze/dedup lives on the session legs only); found %q in:\n%s", banned, v1SQL)
		}
	}
	// 列契约：union 四臂的投影列名必须齐（credits/daily/byModel/byApp 四个
	// 聚合都吃这张表）。缺列 = 某个聚合 42703。
	for _, col := range []string{"tenant_id", "application_id", "outbound_model", "client_model",
		"success", "total_tokens", "prompt_tokens", "completion_tokens",
		"credits_charged", "cost_usd", "latency_ms", "cache_read_tokens", "cache_write_tokens"} {
		if !strings.Contains(sql, col) {
			t.Fatalf("logsTable projection must keep column %q; got:\n%s", col, sql)
		}
	}
}
