package startup

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigration839_AutovacCurrentMonthHeapHandoff pins migration 839, which
// hands the CURRENT month's heap partitions back to autovacuum and tunes their
// analyze scale factor.
//
// What it fixes (252 production, runbook §10.93.5 / §10.99):
//
//	① The hourly analyze_llm_gateway_table_stats pass is the #1 consumer of
//	   database time (81.0%, ~56 min/day). The current-month heap partitions
//	   alone are 40.2% of that pass.
//	② The manual pass ANALYZEs hourly, which resets n_mod_since_analyze — so
//	   autovacuum never takes over. Measured: 22/22 relations have
//	   n_mod_since_analyze = 0.
//	③ With the handoff, the trigger is 50 + scale_factor×rows = 2,402 against
//	   a measured 834 rows/hour, so the worst-case staleness is 6h/11h
//	   (current/month-end) — NOT the 55 hours §10.93.5 assumed. Lowering the
//	   current-month scale factor to 0.005 pins that to 3h/4h, and autoanalyze's
//	   per-run cost is independent of scale_factor (it samples
//	   300×default_statistics_target).
//
// Two categories must stay in the manual pass, or the change breaks them:
//   - COLUMNAR partitions: production shows autoanalyze_count = 0 on them;
//     autovacuum does not gather statistics for columnar, so handing them over
//     means nobody ever analyses them again.
//   - NEVER-ANALYZED relations (no pg_statistic rows): skipping those would
//     permanently leave a never-analysed table unanalysed.
//
// This file asserts TEXT only. Its BEHAVIOUR — that the plpgsql really skips
// the current-month heap partition and really keeps the other two categories —
// is verified against a real PostgreSQL by
// scripts/.verify-839-handoff-behavior.sh (scenarios B/C/D/E plus negative
// control F).
func TestMigration839_AutovacCurrentMonthHeapHandoff(t *testing.T) {
	up, err := os.ReadFile("839_autovac_current_month_heap_handoff.sql")
	require.NoError(t, err)
	mig := string(up)

	down, err := os.ReadFile("839_autovac_current_month_heap_handoff.down.sql")
	require.NoError(t, err)
	downSQL := string(down)

	t.Run("当月堆分区的交接判据", func(t *testing.T) {
		// The predicate must be a disjunction of the two categories that stay
		// manual, NOT "m = 0" (which would analyse every current-month
		// partition, i.e. no change at all).
		require.NotContains(t, mig,
			"AND (m = 0 OR NOT EXISTS (SELECT 1 FROM pg_statistic",
			"839 必须移除 838 的 m = 0 无条件分析判据，否则等于没改")
		require.Contains(t, mig,
			"NOT EXISTS (SELECT 1 FROM pg_statistic s WHERE s.starelid = c.oid)",
			"首次覆盖（从未分析过）必须保留")
		require.Contains(t, mig,
			"AND c.relam <> (SELECT oid FROM pg_am WHERE amname = 'heap')",
			"当月的非堆（列存）分区必须保留在手工 pass —— 生产实测列存 autoanalyze_count = 0")
		require.Contains(t, mig, "m = 0",
			"列存分支必须限定在当月（往月列存按 838 只需补首次覆盖）")
	})

	t.Run("当月堆分区按 pg_am=heap 限定", func(t *testing.T) {
		// The scale-factor sweep must be limited to real partitions of the
		// known parents, of the current month, on heap. Applying it to the
		// parent or to older months would change behaviour that 838 relies on.
		require.Contains(t, mig, "heap_oid   constant oid   := (SELECT oid FROM pg_am WHERE amname = 'heap')")
		require.Contains(t, mig, "AND c.relam = heap_oid")
		require.Contains(t, mig, "JOIN pg_inherits i ON i.inhrelid = c.oid")
		require.Contains(t, mig, "AND c.relname ~ ('_' || cur_suffix || '$')",
			"必须按当前月后缀限定，否则往月分区也会被改")
		require.Contains(t, mig, "'request_logs'",
			"必须覆盖 request_logs 的月分区")
	})

	t.Run("scale_factor 取 0.005 且带参数可回滚", func(t *testing.T) {
		require.Contains(t, mig, "p_scale_factor numeric DEFAULT 0.005")
		require.Contains(t, mig, "apply_llm_gateway_current_month_analyze_scale_factor(0.005)")
		require.Contains(t, downSQL, "apply_llm_gateway_current_month_analyze_scale_factor(0.02)",
			"回滚必须把 scale_factor 交还给 404 的 0.02")
	})

	t.Run("down 恢复 838 的函数体", func(t *testing.T) {
		require.Contains(t, downSQL,
			"AND (m = 0 OR NOT EXISTS (SELECT 1 FROM pg_statistic s WHERE s.starelid = c.oid))",
			"down 必须恢复 838 的判据，否则回滚后当月分区仍不会被分析")
		require.Contains(t, downSQL,
			"DROP FUNCTION IF EXISTS public.apply_llm_gateway_current_month_analyze_scale_factor(numeric)",
			"down 必须删掉 839 新建的函数")
		// And must NOT keep the new predicate.
		require.NotContains(t, downSQL,
			"c.relam <> (SELECT oid FROM pg_am WHERE amname = 'heap')",
			"down 里不该残留 839 的新判据")
	})

	t.Run("四处函数正本同步", func(t *testing.T) {
		// A fresh install builds its schema from these files, not from the
		// migrations. If they drift, new deployments get the old behaviour and
		// the migration is a no-op there.
		for _, f := range []string{
			"../../objects/functions/analyze_llm_gateway_table_stats_integer.sql",
			"../../schema/01-schema.sql",
			"../../../installer/cmd/llm-gw-installer/embeddata/01-schema.sql",
			"../../../deploy/sql/schemas/baseline/01-schema.sql",
		} {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			s := string(b)
			if !strings.Contains(s, "analyze_llm_gateway_table_stats") {
				t.Errorf("%s 不含 analyze_llm_gateway_table_stats，跳过", f)
				continue
			}
			require.NotContains(t, s,
				"AND (m = 0 OR NOT EXISTS (SELECT 1 FROM pg_statistic",
				f+" 仍是 838 的旧判据，未与 839 同步")
			require.Contains(t, s,
				"AND c.relam <> (SELECT oid FROM pg_am WHERE amname = 'heap')",
				f+" 缺少 839 的交接判据")
		}
	})

	t.Run("已接线到 installer 与台账", func(t *testing.T) {
		const name = "839_autovac_current_month_heap_handoff.sql"
		runner, err := os.ReadFile("../../../installer/internal/dbinit/runner.go")
		require.NoError(t, err)
		require.Contains(t, string(runner), `"`+name+`"`,
			"839 必须登记进 Runner.StartupFiles，否则安装器不会执行它")

		mainGo, err := os.ReadFile("../../../installer/cmd/llm-gw-installer/main.go")
		require.NoError(t, err)
		require.Contains(t, string(mainGo), "//go:embed embeddata/startup/"+name)
		require.Contains(t, string(mainGo), `"startup/`+name+`"`)

		tsv, err := os.ReadFile("../../schema/installed_startup_migrations.tsv")
		require.NoError(t, err)
		require.Contains(t, string(tsv), name, "839 必须登记进 installed_startup_migrations.tsv")

		embed, err := os.ReadFile("../../../installer/cmd/llm-gw-installer/embeddata/startup/" + name)
		require.NoError(t, err, "embeddata 里必须有 839 的副本")
		require.Equal(t, string(up), string(embed),
			"embeddata 副本必须与 sql/migrations/startup 正本逐字节一致")
	})
}
