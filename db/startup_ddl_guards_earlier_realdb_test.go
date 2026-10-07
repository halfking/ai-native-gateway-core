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

	t.Run("credits_charged", func(t *testing.T) {
		// maas_pricing / maas_price_rules / maas_settings 由 EnsureMaasSchema 自己
		// 用 CREATE TABLE IF NOT EXISTS 建。夹具**不要**预建：预建会让
		// IF NOT EXISTS 变成空操作，后续引用 base_credits_per_1m 等列时
		// 报 42703 —— 那是夹具的锅，不是守卫的。
		env.drop(t, "request_logs, maas_settings, maas_pricing, maas_price_rules, "+
			"model_credit_rates, models_canonical")
		// model_credit_rates 有 REFERENCES models_canonical(id)，这张表由
		// 别的子系统建，夹具必须给个最小桩，否则 42P01。
		exec(`CREATE TABLE public.models_canonical (id INT PRIMARY KEY);`)
		exec(`CREATE TABLE public.request_logs (
			id BIGSERIAL PRIMARY KEY, tenant_id TEXT, ts TIMESTAMPTZ NOT NULL DEFAULT now());`)

		t.Run("A_守卫在缺列时必须返回false", func(t *testing.T) {
			if d.maasRequestLogsCurrent(env.ctx) {
				t.Error("credits_charged 列与索引都不存在，守卫却返回 true")
			}
		})

		t.Run("D_负控_无守卫时必须被并发读阻塞", func(t *testing.T) {
			env.expectLocked(t, "request_logs", maasRequestLogsDDL)
		})

		t.Run("ensure_首次必须建出列与索引", func(t *testing.T) {
			if err := d.EnsureMaasSchema(env.ctx); err != nil {
				t.Fatalf("EnsureMaasSchema: %v", err)
			}
			if !d.maasRequestLogsCurrent(env.ctx) {
				t.Error("首次 ensure 之后守卫仍为 false ⇒ 列或索引没建出来")
			}
		})

		t.Run("B_齐备后守卫必须返回true", func(t *testing.T) {
			if !d.maasRequestLogsCurrent(env.ctx) {
				t.Error("列与索引都已存在，守卫却返回 false ⇒ 每次启动仍白锁一次 request_logs")
			}
		})

		t.Run("C_决定性_守卫后并发读下ensure必须立即成功", func(t *testing.T) {
			wait := env.holdAccessShare(t, "request_logs", guardHoldFor)
			defer wait()
			if err := d.EnsureMaasSchema(env.ctx); err != nil {
				t.Fatalf("守卫后仍被阻塞或报错: %v", err)
			}
		})

		t.Run("缺索引时守卫必须仍为false", func(t *testing.T) {
			env.exec(t, `DROP INDEX public.idx_request_logs_credits_charged;`)
			if d.maasRequestLogsCurrent(env.ctx) {
				t.Error("索引被删后守卫仍返回 true ⇒ 缺索引的库永远补不上")
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

	t.Run("work_type_request_logs", func(t *testing.T) {
		env.drop(t, "request_logs")
		exec(`CREATE TABLE public.request_logs (
			id BIGSERIAL PRIMARY KEY, tenant_id TEXT, ts TIMESTAMPTZ NOT NULL DEFAULT now());`)

		t.Run("A_守卫在缺列时必须返回false", func(t *testing.T) {
			if d.workTypeRequestLogsCurrent(env.ctx) {
				t.Error("work_type 列与索引都不存在，守卫却返回 true")
			}
		})

		t.Run("D_负控_无守卫时必须被并发读阻塞", func(t *testing.T) {
			env.expectLocked(t, "request_logs", workTypeRequestLogsDDL)
		})

		t.Run("ensure_首次必须建出列与索引", func(t *testing.T) {
			if err := d.ensureWorkTypeSchema(env.ctx); err != nil {
				t.Fatalf("ensureWorkTypeSchema: %v", err)
			}
			if !d.workTypeRequestLogsCurrent(env.ctx) {
				t.Error("首次 ensure 之后守卫仍为 false")
			}
		})

		t.Run("B_齐备后守卫必须返回true", func(t *testing.T) {
			if !d.workTypeRequestLogsCurrent(env.ctx) {
				t.Error("列与索引都已存在，守卫却返回 false")
			}
		})

		t.Run("C_决定性_守卫后并发读下ensure必须立即成功", func(t *testing.T) {
			wait := env.holdAccessShare(t, "request_logs", guardHoldFor)
			defer wait()
			if err := d.ensureWorkTypeSchema(env.ctx); err != nil {
				t.Fatalf("守卫后仍被阻塞或报错: %v", err)
			}
		})

		t.Run("缺索引时守卫必须仍为false", func(t *testing.T) {
			env.exec(t, `DROP INDEX public.idx_request_logs_work_type;`)
			if d.workTypeRequestLogsCurrent(env.ctx) {
				t.Error("索引被删后守卫仍返回 true ⇒ 缺索引的库永远补不上")
			}
		})
	})
}
