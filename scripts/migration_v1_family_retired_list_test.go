package scripts

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrationV1FamilyRetiredListShape 钉住 v1 族退役清单的内容形状
// （2026-11-28 42P01 噪声收口轮）。
//
// 背景：v1 族（request_logs 族）已随 890 退役并从现役库删除，但账本漂移库
// 的 schema_migrations 停在更早位置 ⇒ SSOT 部署通道（deploy-lib
// db-changelog.sh 的 pending 扫描）会把只引用 v1 族的迁移反复投喂给现役库
// ⇒ PG 日志 42P01，最坏打断整条部署通道。清单由部署通道读取（basename
// 精确匹配），本判据钉清单本身的四条硬边界：
//
//  1. 条目格式：`NNN_name.sql`（无 down/skip/retired 变体、无路径、无注释行）；
//  2. 条目对应的 canonical 文件必须**在仓内**（清单引用不存在的文件 =
//     幽灵条目，守卫静默吃掉正常迁移）；
//  3. 关键件必须在场（420/428/449/529/547/565/649/680/688/696/725/736/837
//     —— 2026-11-28 一次性容器逐件实测的裁决集；529 正是 00:29 炸通道的
//     元凶，缺它 = 守卫漏防本来的目标）；
//  4. 守卫不拦件必须在**外**（511/717/733/802/823/835/838/839/857 ——
//     「211 形态」实测可跑；进清单 = 无故丢现役族交付）。
func TestMigrationV1FamilyRetiredListShape(t *testing.T) {
	listPath := filepath.Join("..", "scripts", "migration-v1-family-retired.list")
	f, err := os.Open(listPath)
	if err != nil {
		t.Fatalf("清单文件不在场: %v", err)
	}
	defer f.Close()

	entries := 0
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries++
		// 边界①：basename 形态 NNN_name.sql（版本号 3 位、无变体后缀、无路径）
		if !strings.HasSuffix(line, ".sql") || strings.ContainsAny(line, "/\\") {
			t.Errorf("清单条目必须是裸 basename 的 .sql 文件，得到 %q", line)
			continue
		}
		if strings.HasSuffix(line, ".down.sql") || strings.HasSuffix(line, ".skip") || strings.HasSuffix(line, ".retired") {
			t.Errorf("清单不得含 down/skip/retired 变体（守卫只管正放件），得到 %q", line)
		}
		i := strings.IndexByte(line, '_')
		if i != 3 {
			t.Errorf("清单条目版本号必须是 3 位（NNN_），得到 %q", line)
		}
		for _, c := range line[:i] {
			if c < '0' || c > '9' {
				t.Errorf("版本号段非纯数字：%q", line)
			}
		}
		if seen[line] {
			t.Errorf("清单条目重复：%q", line)
		}
		seen[line] = true

		// 边界②：canonical 文件必须在仓内
		canon := filepath.Join("..", "sql", "migrations", "startup", line)
		if _, err := os.Stat(canon); err != nil {
			t.Errorf("清单引用的文件不在仓内（幽灵条目）: %s", line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读取清单失败: %v", err)
	}
	if entries < 54 {
		t.Errorf("清单条目数 %d < 54（41 纯 v1 + 13 混合件裁决）—— 条目被误删", entries)
	}

	// 边界③：裁决集关键件必须在场
	mustHave := []string{
		// 纯 v1 代表件 + 实测炸通道/炸库件
		"420_request_logs_trace_events.sql", // PG 日志 00:16 42P01 实锤件
		"428_recent_success_rate_probe_filters.sql",
		"449_request_logs_hot_trace_events.sql",
		// 混合件 13 件（逐件实测裁决）
		"457_session_v2_owner_filter.sql",
		"529_repair_shared_pg_sticky_and_bodies_2026_07.sql", // 00:29 炸通道元凶
		"547_session_project_attribution.sql",
		"563_session_summary_trigger_on_hot.sql",
		"564_session_summary_backfill_safe.sql",
		"565_cost_usd_pricing_backfill.sql",
		"649_routing_analytics_probe_filter.sql",
		"680_request_logs_current_month_view_bootstrap.sql",
		"688_promote_default_retention_align_go_scheduler.sql", // 复活 v1 promote + 倒退 session promote 体
		"696_request_logs_view_system_fingerprint.sql",
		"725_r41_request_logs_and_tmp_super_admin_bypass.sql",
		"736_maas_rate_multiplier.sql",
		"837_routing_mv_refresh_state.sql",
	}
	for _, e := range mustHave {
		if !seen[e] {
			t.Errorf("裁决集关键件不在清单（守卫漏防）: %s", e)
		}
	}

	// 边界④：实测可跑的 9 件必须在**外**
	mustNot := []string{
		"511_state_transitions_table.sql",   // IF NOT EXISTS 全 no-op
		"717_request_logs_hot_column_alignment.sql", // 全 DO 守卫
		"733_session_turn_details.sql",      // 回填腿 to_regclass 四路守卫
		"802_session_turn_details_gw_task_id_index.sql",
		"823_session_turns_request_status.sql",
		"835_modality_verify_probe_ledger.sql",
		"838_analyze_skip_frozen_month.sql",
		"839_autovac_current_month_heap_handoff.sql",
		"857_analyze_columnar_staleness_gate.sql",
	}
	for _, e := range mustNot {
		if seen[e] {
			t.Errorf("实测可跑的件误进清单（会丢现役族交付）: %s", e)
		}
	}
}
