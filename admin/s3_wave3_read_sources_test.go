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

// TestTenantStatsLogsTable_ServesSessionFamily pins the S3-wave-3 shape under
// dual-write: four-arm union, freeze-guarded session legs, twin exclusion.
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
	for _, probe := range []string{
		"NOT EXISTS (SELECT 1 FROM _request_logs_hot h WHERE h.request_id = t.request_id)",
		"NOT EXISTS (SELECT 1 FROM _request_logs p WHERE p.request_id = t.request_id)",
	} {
		if strings.Count(sql, probe) != 2 {
			t.Fatalf("dual-write logsTable must twin-exclude BOTH session legs with %q (found %d); got:\n%s",
				probe, strings.Count(sql, probe), sql)
		}
	}
}

// TestTenantStatsLogsTable_V1LegsUntouched pins the R36-A1 adjudicated v1
// shape on both branches: bare hot∪parent legs, no view, no WHERE on v1.
func TestTenantStatsLogsTable_V1LegsUntouched(t *testing.T) {
	for _, writeEnabled := range []bool{true, false} {
		sql := tenantStatsLogsTableSQL(writeEnabled)
		if strings.Contains(sql, "request_logs_with_current_month") {
			t.Fatalf("logsTable must not read the canonical view (R36-A1 30s timeout); writeEnabled=%v got:\n%s", writeEnabled, sql)
		}
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
				t.Fatalf("v1 legs must stay bare; found %q writeEnabled=%v in:\n%s", banned, writeEnabled, v1SQL)
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

// TestTenantStatsLogsTable_StopWriteDropsTwinAntiJoin pins the 2026-10-18
// operating point: when v1 write is off, session legs keep the freeze guard
// but must not carry the correlated NOT EXISTS that scans empty parent
// partitions per row (default tenant 504 root cause).
func TestTenantStatsLogsTable_StopWriteDropsTwinAntiJoin(t *testing.T) {
	sql := tenantStatsLogsTableSQL(false)
	if got := strings.Count(sql, "t.ts > "+db.V1FreezeCutoffSQL); got != 2 {
		t.Fatalf("stop-write path must still freeze-guard BOTH session legs (found %d)", got)
	}
	probe := "NOT EXISTS (SELECT 1 FROM _request_logs"
	if strings.Contains(sql, probe) {
		t.Fatalf("stop-write path must drop twin anti-join probe %q; got:\n%s", probe, sql)
	}
	// Mutation tooth: dual-write path still has the probes — otherwise this
	// pin could green while both branches silently lost them.
	dual := tenantStatsLogsTableSQL(true)
	if !strings.Contains(dual, "NOT EXISTS (SELECT 1 FROM _request_logs p WHERE p.request_id = t.request_id)") {
		t.Fatal("dual-write path lost parent anti-join; stop-write pin would be vacuous")
	}
}
