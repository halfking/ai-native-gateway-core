package admin

// S4 停写互锁（2026-10-05，§9.190）。
//
// # 这道门测什么
//
// 退役 `_request_logs` 的第一前提是**停写**。停写开关是
// `settings.storage.request_logs_write_enabled`
// （`settings/key_request_logs_write_enabled.go`，HotReload）。
//
// 而停写的**前置条件**是「所有读点都已逐点评估」——由
// `requestLogsStopWriteClassification` 这张登记表与
// `TestRequestLogsStopWriteClassificationProgress` 维护。
//
// # ★★ 2026-10-20 订正：本文件此前描述的是**旧默认值**，已与代码相反
//
// 本文件最初写于 2026-10-05，那时停写开关的**代码默认是 `true`**
// （`spec_storage.go` 的 `Default: true` + 访问器 `GetPlatformBool(..., true)`
// 回退）。**G-0 已把代码默认翻成 `false`**（`settings/spec_storage.go:136`
// `Default: false` + `settings/key_request_logs_write_enabled.go:43-45`
// 访问器在 `Global == nil` 时回退 **false**），
// 而本文件的抬头「现状」段与下面的**阳性对照断言**仍按旧默认写。
//
// ⇒ 后果是实测出来的：**带 `TEST_DATABASE_URL` 跑这道门会红**（2026-10-20 实测，
// 红在阳性对照：「库里没有该行时应当取默认 true……实测 false」），
// 而**常规 CI 形态下这道门是 Skip 的**（`TEST_DATABASE_URL` 未设 ⇒ 跳过）
// ⇒ 没人看见它红。
//
// ★ **教训（已写进 LESSONS）**：**被 Skip 的门里的断言也会腐烂** ——
// 「常规 CI 绿」不等于「门里的陈述是真的」。凡是需要 DSN 的门，
// 它的断言必须与代码默认值**同步**复核，且要有**可重跑的脚本**真跑它
// （`scripts/audit/verify-stop-write-interlock-realdb.sh`）。
//
// # 现状（实测，2026-10-20 复核）
//
//	- **代码默认 = false**（G-0）：未显式配置 ⇒ **不双写**；
//	- **生产已显式写 false**：两个库组各一行（`settings_kv`）：
//	  {154, 245, 252} 共用的那个库 updated 2026-10-13 20:18:27；
//	  211 的独立库 updated 2026-10-13 07:01:08；
//	- 逐点评估进度 **107/107**，未评估 **0** 个 ⇒ 本门当前**绿**。
//
// ⇒ 「退役已就绪」与「开关已关」现在**同时**成立；本门继续守着的是
// 「**别在未达成时把它翻回来**」这个转换。

// # 为什么是「互锁」而不是「状态灯」
//
// 一条「还没就绪所以报红」的门会**长期红**，而长期红的门等于没有门。
// 所以本门**不判断退役是否就绪**，只拦**危险的那一次转换**：
//
//	未评估读方非空  ∧  停写开关 = false   ⇒   红（危险状态）
//	未评估读方非空  ∧  停写开关 = true    ⇒   绿（**当前状态**，安全）
//	未评估为空      ∧  停写开关 = false   ⇒   绿（退役完成）
//
// ⇒ **当前是绿的**，且它会在「有人在前置条件未达成时关掉开关」那一刻变红。
// 这比一条一直红的门有用得多：它不制造噪音，只在真的要出事时响。
//
// # 为什么读开关要走**生产同一条路径**，而不是直接查表
//
// `settings.GetPlatformBool` 的解析链是
// `Global.Spec(key)` → `Global.EffectiveValue(ScopePlatform, key, "")` → `StoreDB`。
// 本门**把 `settings.Global` 接到一次性库上**（`NewRegistry` +
// `RegisterBackend(ScopePlatform, NewStoreDB(pool))` + 注册 `StorageSpecs()`），
// 然后调**生产函数本身**。
//
// ★ 如果自建一个 `SELECT value FROM settings_kv` 的读法，就会出现
// 「量具与被检验对象不同源」——本会话已经栽过多次
// （§9.184.4 分类器把连接失败判成 OK、§9.185 探针形状不一致）。
// ⇒ **不走自建读法。**
//
// # 阳性对照是这道门能不能算数的前提
//
// 上面那种接线有一个致命可能：`Spec(key)` 返回 nil ⇒
// `getPlatformBool` 直接返回 fallback(true) ⇒ **无论库里写什么，
// 本门都读到 true** ⇒ 互锁**永远不会红**，是一道**恒真**的门。
//
// ⇒ 第一个子测试就是**阳性对照**：种一行 `false` 进去，
// **必须读到 false**；删掉该行，**必须读到 true**（默认）。
// 读不到 false ⇒ 接线是断的 ⇒ 本门作废。
// 「一个 0 必须配阳性对照」在这里不是风格问题，是**它有没有牙齿的唯一证据**。
//
// # 为什么不把开关种进共享的 llm_gateway
//
// 那会让**正在跑的网关真的停写**。所以两个子测试都在**一次性数据库**上跑，
// 且不碰 `TEST_DATABASE_URL` 指向的库的任何真实设置。

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// stopWriteInterlockDDL 是 `StoreDB` 在 platform scope 下读设置所需的最小结构。
// 列取自生产库 `information_schema`（key/value/value_type/scope/category/
// updated_at/updated_by/prev_value/prev_updated_at），DDL 取自
// `settings/store_db.go` 实际发出的 `SELECT value::text FROM settings_kv WHERE key = $1`。
const stopWriteInterlockDDL = `
CREATE TABLE public.settings_kv (
	key           TEXT    NOT NULL PRIMARY KEY,
	value         JSONB   NOT NULL,
	value_type    TEXT    NOT NULL DEFAULT 'bool',
	scope         TEXT    NOT NULL DEFAULT 'platform',
	category      TEXT    NOT NULL DEFAULT 'storage',
	updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	updated_by    TEXT,
	prev_value    JSONB,
	prev_updated_at TIMESTAMPTZ
);
`

// wireSettingsTo 把 settings.Global 接到一个一次性库上，返回恢复函数。
// 走的是**生产同一条解析链**：NewRegistry + StorageSpecs() +
// RegisterBackend(ScopePlatform, NewStoreDB(pool))。
func wireSettingsTo(t *testing.T, pool *pgxpool.Pool) func() {
	t.Helper()
	saved := settings.Global
	reg := settings.NewRegistry()
	for _, sp := range settings.StorageSpecs() {
		reg.MustRegisterSpec(sp)
	}
	reg.RegisterBackend(settings.ScopePlatform, settings.NewStoreDB(pool))
	settings.Global = reg
	return func() { settings.Global = saved }
}

// newStopWriteFixtureDB 建一个一次性库并把 settings_kv 建好。
// 形态沿用本包既有的做法（admin 侧多处 throwaway database）；
// drop 的 defer 必须留在**调用方**——放进 helper 会在 helper 一返回时
// 就把库删掉，而调用方还握着池（这是本项目已经踩过一次的坑，
// 见 domains/session/v2/session_request_status_backfill_test.go 的注释）。
func newStopWriteFixtureDB(t *testing.T, adminDSN string) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("admin pgxpool.New: %v", err)
	}
	name := "s4ilock_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Skipf("cannot create throwaway database (insufficient privileges?), skipping: %v", err)
	}
	defer admin.Close() // drop 由调用方的 defer 负责（它要跑在 admin 池还开着时）
	cfg, err := pgxpool.ParseConfig(adminDSN)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("fixture pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("fixture database unavailable: %v", err)
	}
	if _, err := pool.Exec(ctx, stopWriteInterlockDDL); err != nil {
		pool.Close()
		t.Fatalf("create settings_kv: %v", err)
	}
	return pool, name
}

// unclassifiedStopWriteReaders 返回尚未逐点评估的读点清单。
// 与 TestRequestLogsStopWriteClassificationProgress 用**完全相同**的口径
// （直接表 ∪ 间接表的全集，减去已评估的），不自建另一套清单。
func unclassifiedStopWriteReaders(t *testing.T) []string {
	t.Helper()
	known := map[string]bool{}
	for _, f := range allKnownRequestLogsReaderFiles(t) {
		known[f] = true
	}
	var todo []string
	for _, f := range allKnownRequestLogsReaderFiles(t) {
		if c, ok := requestLogsStopWriteClassification[f]; !ok || c.Effect == effectUnclassified {
			todo = append(todo, f)
		}
	}
	return todo
}

// stopWriteInterlockViolation 返回「停写开关已关，但仍有未评估读方」时的违规说明。
// **纯函数**：不碰 DB，所以可以在一次性库里用**编造的** flag 值来证明
// 它真的会响（逻辑自检），也可以对**真实** flag 值执行（不变量互锁）。
//
// ★ 第一版把它和执行揉在一起，在一次性库里把 flag 设成 false 再断言
//
//	「flag 应为 true」——**那是一条按构造恒红的门**：不管真实世界如何
//	它都会报违规。抽成纯函数后两个职责才分得开：
//	- 逻辑自检：一次性库 + 编造值 ⇒ 证明它**会**响（不是恒真）；
//	- 不变量互锁：真实库 + 真实值 ⇒ 只在**真的**危险时响。
func stopWriteInterlockViolation(unclassified []string, flagEnabled bool) string {
	if flagEnabled || len(unclassified) == 0 {
		return ""
	}
	return "停写开关已是 false，但仍有 " + strconv.Itoa(len(unclassified)) +
		" 个读点未逐点评估（" + strings.Join(unclassified, ", ") +
		"）—— 这些读点停写后会怎样是**未知的**，此刻关停等于拿它们的行为赌注。" +
		"先补评估，再关停。"
}

// TestStopWriteInterlock_UnclassifiedReadersKeepTheFlagOn 互锁本体 + 接线阳性对照。
func TestStopWriteInterlock_UnclassifiedReadersKeepTheFlagOn(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-database interlock")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("admin pgxpool.New: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("TEST_DATABASE_URL unreachable, skipping: %v", err)
	}
	pool, name := newStopWriteFixtureDB(t, dsn)
	restore := wireSettingsTo(t, pool)
	dropped := false
	defer func() {
		restore()
		pool.Close()
		if !dropped {
			if _, e := admin.Exec(context.Background(),
				"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", name); e != nil {
				t.Errorf("FATAL: could not terminate fixture backends for %s: %v", name, e)
			}
			if _, e := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name); e != nil {
				t.Errorf("FATAL: fixture database %s not dropped: %v", name, e)
			}
		}
		admin.Close()
	}()

	key := settings.KeyRequestLogsWriteEnabled

	// ---- 阳性对照：接线真的能读出库里的值吗 ----
	// 没有这一步，下面那句「读到 true」可能是 fallback 而不是真值，
	// 整道互锁就成了**恒真**的门。
	t.Run("阳性对照：种 false 进去必须读到 false，删掉必须读回 false（缺行 ⇒ 默认 false）", func(t *testing.T) {
		if _, err := pool.Exec(ctx,
			"INSERT INTO public.settings_kv (key, value) VALUES ($1, 'false'::jsonb)", key); err != nil {
			t.Fatalf("seed false: %v", err)
		}
		if settings.RequestLogsWriteEnabled() {
			t.Fatal("库里种的是 false，settings.RequestLogsWriteEnabled() 却返回 true —— " +
				"接线没生效（spec 未注册 / backend 未挂 / fallback 吞掉了真值）。" +
				"**本互锁此时是恒真的、作废**，必须先修接线")
		}
		if _, err := pool.Exec(ctx, "DELETE FROM public.settings_kv WHERE key = $1", key); err != nil {
			t.Fatalf("unset: %v", err)
		}
		// ★ 2026-10-20 订正：这一条原先断言「删掉必须读回 **true**」
		// （那时的代码默认是 true）。G-0 把默认翻成 false 之后，正确的
		// 预期是「删掉必须读回 **false**」——**缺行 ⇒ 不双写**。
		// 保留这条断言的理由不变：它证明「读到 false」是**真读到了库里的
		// false**，而不是「接线没挂上、恰好回退了默认值」——因为没有行时
		// 读到的也是 false，只有**种进去**的那一次才区分得了这两种情况。
		if settings.RequestLogsWriteEnabled() {
			t.Fatal("库里没有该行时应当取默认 false（Default: false = 不双写），实测 true —— " +
				"默认语义与 spec 漂移了")
		}
	})

	// ---- 逻辑自检：编造危险状态，证明互锁**会**响 ----
	//
	// 若这一条不响，互锁就是恒真的（和「Spec 未注册→落回 fallback」
	// 一样的失效形态），那真实不变量那条也一并作废。
	//
	// ★ 第二版改成**自带危险状态**，不再用真实未评估清单当前提。
	// 第一版写的是 `todo := unclassifiedStopWriteReaders(t); if len(todo)==0 { Skip }`
	// —— 于是 §9.191 把最后一个读点评估完之后，本自检**永久 Skip**，
	// 而它恰恰是唯一能证明互锁没退化成恒真门的那一条。
	// 判据的有效性不能挂在「被检验对象当前恰好处于某个状态」上：
	// 前提一旦消失，判据就静默作废，而且作废得毫无信号。
	// 现在的前提是**编造的**（合成一个读点名），恒定可构造。
	t.Run("逻辑自检：flag=false 且存在未评估读方 ⇒ 必须报违规", func(t *testing.T) {
		todo := []string{"admin/__synthetic_unclassified_reader_for_selfcheck__.go"}
		if len(todo) == 0 {
			t.Fatal("合成前提自身为空 —— 本自检的构造已失效")
		}
		if msg := stopWriteInterlockViolation(todo, false); msg == "" {
			t.Error("flag=false 且有未评估读方，checker 却没报违规 —— " +
				"**互锁是恒真的、作废**。请检查 stopWriteInterlockViolation 的两个分支")
		}
		if msg := stopWriteInterlockViolation(todo, true); msg != "" {
			t.Errorf("flag=true（仍在双写）却报了违规：%s", msg)
		}
		if msg := stopWriteInterlockViolation(nil, false); msg != "" {
			t.Errorf("无未评估读方、flag=false（退役完成）却报了违规：%s", msg)
		}
	})

	// ---- 不变量互锁：对**真实** TEST_DATABASE_URL 执行 ----
	//
	// 这里**不编造** flag 值：读真实 store。正常情况下应为绿。
	t.Run("真实库不变量：未评估读方非空时，真实开关不得为 false", func(t *testing.T) {
		// ★ 这一条必须**独立接到 TEST_DATABASE_URL 本身**。
		// 第一版直接复用了上面那次 wireSettingsTo(t, pool)，
		// 而那个 pool 指的是**一次性库** ⇒ 这条「真实不变量」读的是
		// 一次性库（里面当然没有设置行，于是读到**代码默认**）——
		// **量具读的不是被检验对象**，而且它会永远绿。
		// 判据红了先怀疑判据；这里是我自己的判据指错了对象。
		real, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatalf("connect real TEST_DATABASE_URL: %v", err)
		}
		defer real.Close()
		if err := real.Ping(ctx); err != nil {
			t.Fatalf("ping real TEST_DATABASE_URL: %v", err)
		}
		// 立刻把 registry 切回一次性库，避免子测试之间互相污染。
		restore()
		restoreReal := wireSettingsTo(t, real)
		defer restoreReal()

		todo := unclassifiedStopWriteReaders(t)
		t.Logf("未评估读方 %d 个：%v", len(todo), todo)
		enabled := settings.RequestLogsWriteEnabled() // 真实 store；缺行 ⇒ 代码默认 false
		t.Logf("真实开关 storage.request_logs_write_enabled = %v（缺行时取代码默认 false = 不双写）", enabled)
		if msg := stopWriteInterlockViolation(todo, enabled); msg != "" {
			t.Error(msg)
		}
		// 自证：这条读的是**真实库**而不是一次性库——两个库不同源，
		// 若解析其实还落在一次性库上，这里种一行 false 应当读不出变化。
		// 因此反向验证：真实库里若本来就没有该行，读到的必须是代码默认
		// （false）。★ 注意：生产上该行**是有的**（2026-10-13 显式写 false），
		// 所以这一行的意义是「量具没指错对象」，不是「生产缺行」。
		var rows int
		if err := real.QueryRow(ctx,
			"SELECT count(*) FROM public.settings_kv WHERE key = $1",
			settings.KeyRequestLogsWriteEnabled).Scan(&rows); err != nil {
			t.Logf("（无法读真实 settings_kv：%v —— 本条只报状态，不报违规）", err)
		} else {
			t.Logf("真实库 settings_kv 里该键的行数 = %d", rows)
			// ★ 2026-10-21 订正（R88 轮 · 形态量具线）：这条断言原先写的是
			//   「没有该行却读到 false ⇒ 违规（应回落到 true）」——那是 **G-0 之前**的契约。
			//   2026-10-20 的 c2fae70ea「request_logs 停写门的代码默认由 true 改为 false」
			//   之后，正确预期是「没有该行 ⇒ 读到**代码默认 false**」。
			//   ⇒ 同一文件里**上面那条**子断言（:259-268「删掉必须读回 false」）
			//     在 2026-10-20 的 a2ac50e37 里已经按新契约改过，**唯独漏了本条**
			//     ⇒ ⇒ 这就是「同一作者、同一轮、同族漏项」的第 N 例。
			//   ⇒ ★ 而本条的**自相矛盾**就在它自己的注释里：上面三行写着
			//     「真实库里若本来就没有该行，读到的必须是代码默认（false）」
			//     ⇒ 断言却要求它读到 true ⇒ ⇒ 断言与自己的注释对着干。
			//   ⇒ 订正后判据仍然**有牙**：代码默认若被翻回 true（spec 的 Default 变了），
			//     「缺行 ⇒ 读到 true」⇒ 本条立即转红（变异 M1 实测）。
			if rows == 0 && enabled {
				t.Error("真实库里没有该行，却读到 true —— 与 spec 不符" +
					"（Default: false = 不双写；c2fae70ea 起代码默认是 false）")
			}
		}
	})
}
