package db

import (
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// boot_ensure_hot_table_guard_test.go — 2026-10-21 252 PG 日志审计轮。
//
// 两条启动期 ensure 对**热路径表**每次 boot 都取 ACCESS EXCLUSIVE，
// 10-20 一天 7 次蓝绿部署 ⇒ 7 次热路径停顿（252 PG 日志 log_lock_waits 实测）：
//
//   · ensureModelsCanonicalStandardIQ：`ALTER TABLE models_canonical ADD COLUMN
//     IF NOT EXISTS` ×3 —— 三列早已在位仍取 AEL；13:00:51 等锁 4,997 ms，
//     期间 `SELECT … FROM models_canonical WHERE canonical_name = …`（路由）与
//     候选查询全部排队。
//   · ensureRouteIncidentPendingState：每次 boot 无条件 DROP CONSTRAINT /
//     DROP INDEX / 重建 —— 13:24:17→13:24:50 启动链持锁期间 21 条热路径
//     `SELECT … FOR UPDATE` 各排队 >1 s。原注释「AEL 锁窗口毫秒级」只在空载成立。
//
// 修法沿用 §10.97/§10.98 的「目录守卫」定式：形态已在位 ⇒ 整段跳过 DDL。
// 本门两条腿：
//   静态腿 —— 守卫调用必须出现在 Exec 之前；守卫清单与 DDL 体同步（少登记一列 ⇒
//            该列缺失时整段被跳过 ⇒ 这是守卫自己的翻车面，必须钉住）。
//   真库腿 —— 复用 guardEnv 的 A/B/C/D 判据（A 缺时守卫 false；D 负控：无守卫
//            的 DDL 在并发读下必被 55P03 挡住；B 齐备后守卫 true；C 决定性：
//            守卫后并发读下 ensure 立即成功）。无 TEST_DATABASE_URL 时真库腿跳过。

func TestBootEnsureHotTableGuards_Static(t *testing.T) {
	text := readDBGo(t)

	t.Run("models_canonical_standard_iq", func(t *testing.T) {
		body := functionBody(t, text, "func (d *DB) ensureModelsCanonicalStandardIQ(")
		guard := strings.Index(body, `d.columnsAllPresent(ctx, "models_canonical", modelsCanonicalStandardIQColumns)`)
		exec := strings.Index(body, "d.pool.Exec(ctx, modelsCanonicalStandardIQDDL)")
		if guard < 0 {
			t.Fatal("ensureModelsCanonicalStandardIQ 缺 columnsAllPresent 守卫 ⇒ 每次 boot 白锁 models_canonical")
		}
		if exec < 0 {
			t.Fatal("ensureModelsCanonicalStandardIQ 必须执行 modelsCanonicalStandardIQDDL 常量（真库门逐字复用）")
		}
		if guard > exec {
			t.Error("守卫必须在 Exec 之前")
		}
		// 守卫清单 == DDL 体里 ADD COLUMN IF NOT EXISTS 的列集（同步义务）。
		re := regexp.MustCompile(`ADD COLUMN IF NOT EXISTS\s+(\w+)`)
		var fromDDL []string
		for _, m := range re.FindAllStringSubmatch(modelsCanonicalStandardIQDDL, -1) {
			fromDDL = append(fromDDL, m[1])
		}
		want := append([]string(nil), modelsCanonicalStandardIQColumns...)
		sort.Strings(fromDDL)
		sort.Strings(want)
		if strings.Join(fromDDL, ",") != strings.Join(want, ",") {
			t.Errorf("守卫清单与 DDL 列集不同步：guard=%v ddl=%v", want, fromDDL)
		}
		if !strings.Contains(modelsCanonicalStandardIQDDL, "ALTER TABLE models_canonical") {
			t.Error("DDL 常量必须仍然针对 models_canonical（否则守卫表名与 DDL 表名脱钩）")
		}
	})

	t.Run("route_incidents_pending_state", func(t *testing.T) {
		body := functionBody(t, text, "func (d *DB) ensureRouteIncidentPendingState(")
		guard := strings.Index(body, "d.routeIncidentPendingStateInPlace(ctx)")
		exec := strings.Index(body, "d.pool.Exec(ctx, routeIncidentPendingStateDDL)")
		if guard < 0 {
			t.Fatal("ensureRouteIncidentPendingState 缺 routeIncidentPendingStateInPlace 守卫 ⇒ 每次 boot 对 route_incidents 取 AEL")
		}
		if exec < 0 {
			t.Fatal("ensureRouteIncidentPendingState 必须执行 routeIncidentPendingStateDDL 常量")
		}
		if guard > exec {
			t.Error("守卫必须在 Exec 之前")
		}
		probe := functionBody(t, text, "func (d *DB) routeIncidentPendingStateInPlace(")
		for _, want := range []string{
			"route_incidents_state_check",     // 探 CHECK 约束
			"uq_route_incidents_active_route", // 探部分唯一索引
			"'pending'",                       // 两者都必须放行 pending
			"indisvalid",                      // INVALID 索引不算在位（纪律⑫）
			"indisunique",
		} {
			if !strings.Contains(probe, want) {
				t.Errorf("routeIncidentPendingStateInPlace 探针缺 %q", want)
			}
		}
		// 探测失败必须 fail-open（返回 false 走原 DDL），不能把错误当「在位」。
		if !strings.Contains(probe, "return false") {
			t.Error("探针出错时必须返回 false")
		}
	})
}

// 真库腿。
func TestBootEnsureHotTableGuards_RealDB(t *testing.T) {
	env := newGuardEnv(t)
	d := env.d
	exec := func(q string) { env.exec(t, q) }

	t.Run("models_canonical_standard_iq", func(t *testing.T) {
		env.drop(t, "models_canonical")
		exec(`CREATE TABLE public.models_canonical (id INT PRIMARY KEY, canonical_name TEXT);`)

		t.Run("A_缺列时守卫必须返回false", func(t *testing.T) {
			if d.columnsAllPresent(env.ctx, "models_canonical", modelsCanonicalStandardIQColumns) {
				t.Error("三列都不存在，守卫却返回 true")
			}
		})
		t.Run("D_负控_无守卫时DDL必须被并发读阻塞", func(t *testing.T) {
			env.expectLocked(t, "models_canonical", modelsCanonicalStandardIQDDL)
		})
		t.Run("ensure_首次必须建出三列", func(t *testing.T) {
			if err := d.ensureModelsCanonicalStandardIQ(env.ctx); err != nil {
				t.Fatalf("ensureModelsCanonicalStandardIQ: %v", err)
			}
			if !d.columnsAllPresent(env.ctx, "models_canonical", modelsCanonicalStandardIQColumns) {
				t.Error("首次 ensure 之后守卫仍为 false ⇒ 列没建出来")
			}
		})
		t.Run("C_决定性_守卫后并发读下ensure必须立即成功", func(t *testing.T) {
			wait := env.holdAccessShare(t, "models_canonical", guardHoldFor)
			defer wait()
			start := time.Now()
			if err := d.ensureModelsCanonicalStandardIQ(env.ctx); err != nil {
				t.Fatalf("守卫后仍被阻塞或报错: %v", err)
			}
			if elapsed := time.Since(start); elapsed > guardLockTimout {
				t.Errorf("守卫后耗时 %v，超过负控的阻塞时长 %v ⇒ 守卫没有真正短路", elapsed, guardLockTimout)
			}
		})
		t.Run("缺任一列时守卫必须回到false", func(t *testing.T) {
			exec(`ALTER TABLE public.models_canonical DROP COLUMN standard_iq_updated_at;`)
			if d.columnsAllPresent(env.ctx, "models_canonical", modelsCanonicalStandardIQColumns) {
				t.Error("少一列守卫仍返回 true ⇒ 缺列的库永远补不上")
			}
			if err := d.ensureModelsCanonicalStandardIQ(env.ctx); err != nil {
				t.Fatalf("补列 ensure: %v", err)
			}
			if !d.columnsAllPresent(env.ctx, "models_canonical", modelsCanonicalStandardIQColumns) {
				t.Error("补列后守卫仍为 false")
			}
		})
	})

	t.Run("route_incidents_pending_state", func(t *testing.T) {
		env.drop(t, "route_incidents, schema_migrations")
		exec(`CREATE TABLE public.schema_migrations (version TEXT PRIMARY KEY, description TEXT, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());`)
		// 389 时代的旧形态：CHECK 不放行 pending、部分唯一索引不含 pending。
		exec(`CREATE TABLE public.route_incidents (
			id BIGSERIAL PRIMARY KEY,
			tenant_id TEXT NOT NULL,
			endpoint_protocol TEXT NOT NULL,
			model TEXT NOT NULL,
			provider_id BIGINT,
			credential_id BIGINT,
			state TEXT NOT NULL,
			CONSTRAINT route_incidents_state_check CHECK (state IN ('active', 'recovering', 'recovered'))
		);
		CREATE UNIQUE INDEX uq_route_incidents_active_route
		    ON public.route_incidents (tenant_id, endpoint_protocol, model, COALESCE(provider_id, 0), COALESCE(credential_id, 0))
		    WHERE state IN ('active', 'recovering');`)

		t.Run("A_旧形态时守卫必须返回false", func(t *testing.T) {
			if d.routeIncidentPendingStateInPlace(env.ctx) {
				t.Error("CHECK 与索引都还不放行 pending，守卫却返回 true")
			}
		})
		t.Run("D_负控_无守卫时DDL必须被并发读阻塞", func(t *testing.T) {
			env.expectLocked(t, "route_incidents", routeIncidentPendingStateDDL)
		})
		t.Run("ensure_首次必须升级到715形态", func(t *testing.T) {
			if err := d.ensureRouteIncidentPendingState(env.ctx); err != nil {
				t.Fatalf("ensureRouteIncidentPendingState: %v", err)
			}
			if !d.routeIncidentPendingStateInPlace(env.ctx) {
				t.Error("首次 ensure 之后守卫仍为 false ⇒ 715 形态没建出来")
			}
			var stamped int
			if err := env.pool.QueryRow(env.ctx, `SELECT count(*) FROM public.schema_migrations WHERE version='715'`).Scan(&stamped); err != nil || stamped != 1 {
				t.Errorf("715 账本 stamp 缺失 (err=%v, n=%d)", err, stamped)
			}
		})
		t.Run("C_决定性_守卫后并发读下ensure必须立即成功", func(t *testing.T) {
			wait := env.holdAccessShare(t, "route_incidents", guardHoldFor)
			defer wait()
			start := time.Now()
			if err := d.ensureRouteIncidentPendingState(env.ctx); err != nil {
				t.Fatalf("守卫后仍被阻塞或报错: %v", err)
			}
			if elapsed := time.Since(start); elapsed > guardLockTimout {
				t.Errorf("守卫后耗时 %v，超过负控的阻塞时长 %v ⇒ 守卫没有真正短路", elapsed, guardLockTimout)
			}
		})
		t.Run("缺索引时守卫必须回到false", func(t *testing.T) {
			exec(`DROP INDEX public.uq_route_incidents_active_route;`)
			if d.routeIncidentPendingStateInPlace(env.ctx) {
				t.Error("索引被删后守卫仍返回 true ⇒ 缺索引的库永远补不上")
			}
			if err := d.ensureRouteIncidentPendingState(env.ctx); err != nil {
				t.Fatalf("补索引 ensure: %v", err)
			}
			if !d.routeIncidentPendingStateInPlace(env.ctx) {
				t.Error("补索引后守卫仍为 false")
			}
		})
		t.Run("CHECK退回旧形态时守卫必须回到false", func(t *testing.T) {
			exec(`ALTER TABLE public.route_incidents DROP CONSTRAINT route_incidents_state_check;
			      ALTER TABLE public.route_incidents ADD CONSTRAINT route_incidents_state_check CHECK (state IN ('active', 'recovering', 'recovered'));`)
			if d.routeIncidentPendingStateInPlace(env.ctx) {
				t.Error("CHECK 不再放行 pending，守卫仍返回 true ⇒ 23514 回归会被静默跳过")
			}
		})
		t.Run("表不存在时守卫必须返回false", func(t *testing.T) {
			env.drop(t, "route_incidents")
			if d.routeIncidentPendingStateInPlace(env.ctx) {
				t.Error("表不存在守卫却返回 true")
			}
		})
	})
}
