package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。
//
// probe_schedule_audit_script_shape_test.go —— 「凭据探测记录小时审计脚本」的形状门。
//
// # 这道门守的是哪件东西
//
// `scripts/audit/probe-schedule-audit.sh` 带本仓运维脚本里**最重**的写面：
// `--fix` 会真写 credentials / node_probe_state / credential_model_bindings /
// model_offers 四张表。同目录先例（api-sweep.sh / check-credential-routability-consistency.sh）
// 都有形状门，本脚本最初没有 ⇒ 破例（第 50 增量审计 F-8）。本门钉三条最要害的静态断言：
//
//	① **fix/报告同谓词**：首版 FIX_R3 内层 SELECT 缺 eligibility+binding EXISTS、
//	   FIX_R5 缺 binding EXISTS —— 修复 SQL 与报告 SQL 谓词漂移会「修的≠报的」，
//	   甚至把 manual_disabled 行也 re-arm（首版实测形状）。
//	② **有界性**：每条 FIX 必须带 statement_timeout + LIMIT；无上界的 UPDATE 在
//	   credentials/node_probe_state 上没有护栏。
//	③ **危险词**：审计脚本永远不该出现 DELETE/DROP/TRUNCATE（修模态是「重放系统
//	   自己会做的幂等恢复」，不是清理）。
//
// ★ 与 api-sweep 形状门同款纪律：**关键字在不在**只是下限（把逻辑写反它拦不住），
// 但写面脚本的失守模式就是「有人往 FIX SQL 里加了一条不带谓词/不带界的 UPDATE」，
// 静态形状恰好钉得住这个失守模式 ⇒ 不做行为层（行为层需要真库，审计脚本自身
// 在部署机上对着真库跑，这里的静态门只守「漂移进不了仓」）。

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const probeScheduleAuditRelPath = "scripts/audit/probe-schedule-audit.sh"

func probeScheduleAuditScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(probeScheduleAuditRepoRoot(t), filepath.FromSlash(probeScheduleAuditRelPath)))
	if err != nil {
		t.Fatalf("读不到被测脚本 %s: %v", probeScheduleAuditRelPath, err)
	}
	return string(b)
}

// TestProbeScheduleAuditScriptExistsAndParses —— 脚本在位且 bash -n 过。
func TestProbeScheduleAuditScriptExistsAndParses(t *testing.T) {
	script := probeScheduleAuditScript(t)
	if !strings.Contains(script, "probe-schedule-audit.sh") {
		t.Fatalf("脚本内容不像 probe-schedule-audit（自述缺失），门可能指错了文件")
	}
	repoRoot := probeScheduleAuditRepoRoot(t)
	cmd := exec.Command("bash", "-n", filepath.Join(repoRoot, filepath.FromSlash(probeScheduleAuditRelPath)))
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n %s 失败: %v\n%s", probeScheduleAuditRelPath, err, out)
	}
}

// probeScheduleAuditRepoRoot 返回仓根（runtime.Caller 定位本测试文件所在仓）。
func probeScheduleAuditRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
}

// TestProbeScheduleAuditFixMatchesReportPredicates —— fix/报告同谓词（断言①）。
func TestProbeScheduleAuditFixMatchesReportPredicates(t *testing.T) {
	script := probeScheduleAuditScript(t)

	// FIX_R3 必须带 eligibility（credentials×providers 的 active/enabled/manual 谓词）
	// 与 binding EXISTS —— 与 SQL_R3 同源。取 FIX_R3 那一行（单行字符串）断言。
	fixR3 := extractLineContaining(t, script, `FIX_R3="`)
	for _, want := range []string{
		"COALESCE(c.manual_disabled,FALSE)=FALSE",
		"COALESCE(p.enabled,FALSE)=TRUE",
		"COALESCE(c.lifecycle_status,'active')='active'",
		"credential_model_bindings",
		"provider_models",
		// 有界性顺手钉在这（R3 首版无 ORDER BY 的 LIMIT 50 ⇒ 批次不确定）：
		"ORDER BY",
		"LIMIT 50",
	} {
		if !strings.Contains(fixR3, want) {
			t.Errorf("FIX_R3 缺与报告同源的谓词/排序 %q —— 修复面与报告面漂移（审计 F-5）", want)
		}
	}

	// FIX_R5 必须带 binding EXISTS 且要求绑定不可用（与 SQL_R5 同源；首版缺）。
	fixR5 := extractLineContaining(t, script, `FIX_R5="`)
	for _, want := range []string{
		"credential_model_bindings",
		"COALESCE(cmb.available,FALSE)=FALSE",
		"LIMIT 50",
	} {
		if !strings.Contains(fixR5, want) {
			t.Errorf("FIX_R5 缺与报告同源的 binding 谓词 %q（审计 F-7）", want)
		}
	}

	// FIX_R4 必须是 cmb + model_offers 双面写（系统 restoreAllBindingsOnCredentialSuccess
	// 的形状；单面写会让路由解析面仍 unavailable —— 审计 F-6）。
	fixR4 := extractLineContaining(t, script, `FIX_R4="`)
	for _, want := range []string{
		"UPDATE credential_model_bindings",
		"UPDATE model_offers",
		"COALESCE(mo.admin_protected,FALSE) = FALSE",
	} {
		if !strings.Contains(fixR4, want) {
			t.Errorf("FIX_R4 缺双面写/镜像守卫 %q（审计 F-6）", want)
		}
	}

	// FIX_R2 不得复活 suspended 死谓词（首版 `(availability_state <> 'suspended' OR ...)`
	// 恒真 —— IN 列表本就不含 suspended；审计 F-7）。
	fixR2 := extractLineContaining(t, script, `FIX_R2="`)
	if strings.Contains(fixR2, "<> 'suspended'") {
		t.Errorf("FIX_R2 复活了恒真死谓词 `<> 'suspended'`（IN 列表本就不含该值）——误导读者")
	}
}

// TestProbeScheduleAuditFixesAreBounded —— 每条 FIX 带 statement_timeout+LIMIT（断言②）。
func TestProbeScheduleAuditFixesAreBounded(t *testing.T) {
	script := probeScheduleAuditScript(t)
	for _, name := range []string{"FIX_R1", "FIX_R2", "FIX_R3", "FIX_R4", "FIX_R5"} {
		line := extractLineContaining(t, script, name+`="`)
		if !strings.Contains(line, "SET LOCAL statement_timeout") {
			t.Errorf("%s 缺 SET LOCAL statement_timeout —— 写面无超时护栏", name)
		}
		if !strings.Contains(line, " LIMIT ") {
			t.Errorf("%s 缺 LIMIT 上界 —— 无界 UPDATE 没有护栏", name)
		}
		if !strings.HasPrefix(strings.TrimSpace(line), name+`="BEGIN;`) {
			t.Errorf("%s 不在显式事务（BEGIN...COMMIT）内 —— 半途失败会留下半写态", name)
		}
	}
}

// TestProbeScheduleAuditNeverDestroys —— 危险词禁入（断言③）。
func TestProbeScheduleAuditNeverDestroys(t *testing.T) {
	script := probeScheduleAuditScript(t)
	for _, bad := range []string{"DELETE FROM", "DROP TABLE", "DROP CONSTRAINT", "TRUNCATE"} {
		if strings.Contains(script, bad) {
			t.Errorf("审计脚本出现危险词 %q —— 修模态是重放幂等恢复，不是清理/重建", bad)
		}
	}
	// 默认只读：FIX 必须显式 opt-in。
	if !strings.Contains(script, "FIX=0") {
		t.Errorf("脚本缺 `FIX=0` 默认行 —— --fix 必须显式 opt-in，不能默认带写")
	}
}

// extractLineContaining 返回包含 marker 的那一整行（本脚本的 SQL 都是单行字符串）。
func extractLineContaining(t *testing.T, script, marker string) string {
	t.Helper()
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	t.Fatalf("脚本里找不到含 %q 的行", marker)
	return ""
}
