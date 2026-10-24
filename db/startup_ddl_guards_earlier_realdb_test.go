package db

import (
	"testing"
)

// TestStartupDDLGuards_EarlierOnes_RealDB 给**更早三批**的守卫补真库行为验证
// （runbook §10.98.12）。
//
// 为什么补：§10.98.11 已经证明「文本门 + 变异全绿」不等于「语义对」——
// provider_models 那条守卫的探针把 NOT EXISTS 写成 EXISTS，读代码看不出来，
// 6 个子测试 + 10 条变异全绿，真库一跑就红。
// 同样形状、同样只靠文本门的三条：
//
//	credits_charged   （maas_schema.go，§10.97）
//	quality_fix_mode  （db.go，§10.98.3）
//	work_type         （db.go，§10.84）
//
// 「读起来对」不等于「验过」，所以一律真跑。判据与前一组完全一致（A/B/C/D），
// 复用同一个 guardEnv 脚手架，避免两边的通过/失败不可比。
func TestStartupDDLGuards_EarlierOnes_RealDB(t *testing.T) {
	env := newGuardEnv(t)
	d := env.d
	exec := func(q string) { env.exec(t, q) }

	t.Run("credits_charged_v1_leg_is_gone", func(t *testing.T) {
		// ★ 2026-10-24（v1 → session_turns 替换轮）：maas 的 v1 腿
		// （给 _request_logs 补 credits_charged 列 + idx_request_logs_credits_charged）
		// 整条删除 ⇒ **没有锁可挡，也没有闸需要挡** ⇒ 本判据从
		// 「守卫必须挡住那条 ALTER」翻成「**v1 族不在场时 ensure 必须成功，
		// 且定价表必须被建出来、不许碰 v1**」。
		//
		// 夹具刻意**不建** _request_logs：族不在场才是这条判据的场景。
		env.drop(t, "_request_logs, _request_logs_hot, maas_settings, maas_pricing, maas_price_rules, "+
			"model_credit_rates, models_canonical")
		// model_credit_rates 有 REFERENCES models_canonical(id)，这张表由
		// 别的子系统建，夹具必须给个最小桩，否则 42P01。
		exec(`CREATE TABLE public.models_canonical (id INT PRIMARY KEY);`)

		t.Run("族不在场时ensure必须成功", func(t *testing.T) {
			if err := d.EnsureMaasSchema(env.ctx); err != nil {
				t.Fatalf("EnsureMaasSchema 在 v1 族不在场时必须成功（42P01 会中止整条 boot 迁移），got: %v", err)
			}
		})

		t.Run("定价表必须被建出来", func(t *testing.T) {
			for _, tbl := range []string{"maas_settings", "model_credit_rates"} {
				var n int
				if err := env.pool.QueryRow(env.ctx,
					`SELECT count(*) FROM pg_class WHERE relname=$1`, tbl).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n == 0 {
					t.Errorf("表 %s 没被建出来 —— 删 v1 腿时把定价表一起删了", tbl)
				}
			}
		})

		t.Run("v1族必须保持不存在_ensure不许碰它", func(t *testing.T) {
			for _, tbl := range []string{"_request_logs", "_request_logs_hot"} {
				var n int
				if err := env.pool.QueryRow(env.ctx,
					`SELECT count(*) FROM pg_class WHERE relname=$1`, tbl).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Errorf("表 %s 被 ensure 建出来了 —— v1 族已退役，ensure 不许再碰它", tbl)
				}
			}
		})
	})

	t.Run("quality_fix_mode", func(t *testing.T) {
		env.drop(t, "providers, provider_quality_rollup")
		exec(`CREATE TABLE public.providers (
			id BIGSERIAL PRIMARY KEY, tenant_id TEXT, name TEXT);`)

		t.Run("A_守卫在缺列时必须返回false", func(t *testing.T) {
			if d.columnsAllPresent(env.ctx, "providers", []string{"quality_fix_mode"}) {
				t.Error("quality_fix_mode 列不存在，columnsAllPresent 却返回 true")
			}
		})

		t.Run("D_负控_无守卫时必须被并发读阻塞", func(t *testing.T) {
			env.expectLocked(t, "providers", qualityFixModeDDL)
		})

		t.Run("ensure_首次必须建出列", func(t *testing.T) {
			if err := d.ensureQualityFixModeSchema(env.ctx); err != nil {
				t.Fatalf("ensureQualityFixModeSchema: %v", err)
			}
			if !d.columnsAllPresent(env.ctx, "providers", []string{"quality_fix_mode"}) {
				t.Error("首次 ensure 之后列仍不存在")
			}
		})

		t.Run("B_齐备后守卫必须返回true", func(t *testing.T) {
			if !d.columnsAllPresent(env.ctx, "providers", []string{"quality_fix_mode"}) {
				t.Error("列已存在，columnsAllPresent 却返回 false")
			}
		})

		t.Run("C_决定性_守卫后并发读下ensure必须立即成功", func(t *testing.T) {
			// provider_quality_rollup 的建表/建索引刻意留在未守卫批次
			// （§10.98.3 的决定：一起跳过会让「有列但缺 rollup 表」的库永远补不上）。
			// 它不在 providers 上，因此持 providers 读的并发会话挡不住它。
			wait := env.holdAccessShare(t, "providers", guardHoldFor)
			defer wait()
			if err := d.ensureQualityFixModeSchema(env.ctx); err != nil {
				t.Fatalf("守卫后仍被阻塞或报错: %v", err)
			}
		})

		t.Run("rollup_必须始终被建出来_不受守卫影响", func(t *testing.T) {
			env.exec(t, `DROP TABLE IF EXISTS public.provider_quality_rollup;`)
			if err := d.ensureQualityFixModeSchema(env.ctx); err != nil {
				t.Fatalf("ensureQualityFixModeSchema: %v", err)
			}
			var n int
			if err := env.pool.QueryRow(env.ctx,
				`SELECT count(*) FROM pg_class WHERE relname='provider_quality_rollup'`).
				Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				t.Error("rollup 表没被建出来 —— 它被错误地关进了守卫分支，" +
					"「有列但缺 rollup 表」的库将永远补不上")
			}
		})
	})

	t.Run("work_type_v1_leg_is_gone", func(t *testing.T) {
		// ★ 2026-10-24（v1 → session_turns 替换轮）：ensureWorkTypeSchema 的
		// v1 腿（给 _request_logs 补 work_type 列 + idx_request_logs_work_type）
		// 整条删除 ⇒ 判据从「守卫必须挡住那条 ALTER 的锁」翻成
		// 「**v1 族不在场时 ensure 必须成功，且 work_type 配置表必须被建出来、
		// 不许碰 v1**」。
		env.drop(t, "_request_logs, _request_logs_hot, work_type_config, work_type_model_route")

		t.Run("族不在场时ensure必须成功", func(t *testing.T) {
			if err := d.ensureWorkTypeSchema(env.ctx); err != nil {
				t.Fatalf("ensureWorkTypeSchema 在 v1 族不在场时必须成功（42P01 会中止整条 boot 迁移），got: %v", err)
			}
		})

		t.Run("work_type配置表必须被建出来", func(t *testing.T) {
			for _, tbl := range []string{"work_type_config", "work_type_model_route"} {
				var n int
				if err := env.pool.QueryRow(env.ctx,
					`SELECT count(*) FROM pg_class WHERE relname=$1`, tbl).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n == 0 {
					t.Errorf("表 %s 没被建出来 —— 删 v1 腿时把配置表一起删了", tbl)
				}
			}
		})

		t.Run("v1族必须保持不存在_ensure不许碰它", func(t *testing.T) {
			for _, tbl := range []string{"_request_logs", "_request_logs_hot"} {
				var n int
				if err := env.pool.QueryRow(env.ctx,
					`SELECT count(*) FROM pg_class WHERE relname=$1`, tbl).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Errorf("表 %s 被 ensure 建出来了 —— v1 族已退役，ensure 不许再碰它", tbl)
				}
			}
		})
	})
}
