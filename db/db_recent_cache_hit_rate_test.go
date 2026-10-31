package db

import (
	"context"
	"strings"
	"testing"
	"time"
)

// db_recent_cache_hit_rate_test.go —— recent_cache_hit_rate（P1-B-③，迁移 915）
// 的两层判据：函数体形状（静态）+ boot ensure 真库 CREATE/调用（真库层）。
//
// # 背景（2026-10-28 211 routing_schema_error 事故）
//
// 候选查询以 CROSS JOIN LATERAL 调用本函数。当晚两层叠加：
//  1. 部署序列漏登记 915 ⇒ 函数从未建 ⇒ 候选查询 42883 ⇒ 100% 流量 500；
//  2. 首版 915 文件读的是 request_logs_hot——该表已随 890 退役；LANGUAGE sql
//     在 CREATE 时校验表引用 ⇒ 在族已退役的库上这条迁移本身 42P01 建不出来。
//
// 本门钉的是修后形态：**只读会话族热表** session_turns_hot（credential_id 是
// TEXT，须数字守卫）、模型键走 model/canonical_model 回退链（旧列名
// outbound_model/client_model 在会话族不存在）、探针排除与 rsr 逐字一致、
// 空窗 ⇒ rate NULL + samples 0（调用方 fail-open 中性）。
//
// 判据同层：驱动的是 boot 链上真正调用的 db.ensureRecentCacheHitRate，
// 断言的是 recentCacheHitRateEnsureSQL（静态）与 pg_proc.prosrc（真库）。

// TestRecentCacheHitRateBodyReadsSessionFamilyOnly 钉住 SSOT 函数体的形状。
//
// 负向断言（v1 族引用不得在场）配阳性对照——否则「不在场」与「判据没跑」
// 不可区分（量具按标识符边界判，request_logs 是 request_logs_hot 的前缀）。
func TestRecentCacheHitRateBodyReadsSessionFamilyOnly(t *testing.T) {
	body := recentCacheHitRateEnsureSQL

	// ---- 会话腿必须在场 ----
	if !strings.Contains(body, "FROM public.session_turns_hot") {
		t.Error("recent_cache_hit_rate 函数体缺少会话族热腿 session_turns_hot：" +
			"prompt-cache 亲和信号将永远供不出样本（且 rsr 同款前置已保证该表在 boot 时在场）")
	}

	// ---- v1 族引用必须不在场（890 之后 CREATE 即 42P01，boot 中止）----
	for _, unwanted := range []string{
		"FROM _request_logs_hot", "FROM _request_logs",
		"FROM request_logs_hot", "FROM request_logs",
		// 旧 request_logs 的模型列名——在会话族里不存在，CREATE 时 42703。
		"outbound_model", "client_model",
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("recent_cache_hit_rate 函数体仍含 %q —— v1 族已被迁移 890 整体 DROP，"+
				"该函数经 CROSS JOIN LATERAL 被候选查询调用，CREATE 即失败或首调 42P01 ⇒ "+
				"选不出任何上游凭据 ⇒ 100%% 流量失败（2026-10-28 211 事故第二层根因）。", unwanted)
		}
	}

	// 会话腿 credential_id 是 TEXT（707 S1a）：数字守卫 CAST 必须在——
	// 非数字凭据行直接 ::bigint 会 22P02 炸掉整条函数。
	if !strings.Contains(body, "credential_id ~ '^[0-9]+$'") {
		t.Error("会话腿 credential_id 缺数字守卫 CAST（TEXT 列直接 ::bigint 会 22P02）")
	}
	// 模型键回退链必须在场（model NULL 时回落 canonical_model）。
	if !strings.Contains(body, "lower(COALESCE(model, canonical_model)) = lower(p_raw_model)") {
		t.Error("模型键谓词必须是 lower(COALESCE(model, canonical_model))（会话族列名 + 大小写不敏感）")
	}
	// 探针/自检排除谓词必须与 rsr 逐字一致（探针轮次不镜像，但谓词是防御腿）。
	if !strings.Contains(body, "NOT IN ('self_check', 'node_probe', 'system_health')") {
		t.Error("探针排除谓词丢失：探针/自检行会污染 cache 命中率样本")
	}
	if !strings.Contains(body, "request_id NOT LIKE 'probe-%'") {
		t.Error("probe-% 前缀排除谓词丢失（与 rsr 对齐）")
	}
	// 取样口径：最新 p_sample_n 条；只计 prompt_tokens > 0 的行。
	if !strings.Contains(body, "ORDER BY ts DESC") || !strings.Contains(body, "LIMIT p_sample_n") {
		t.Error("必须按 ts 取最新 p_sample_n 条再聚合（取样口径）")
	}
	if !strings.Contains(body, "prompt_tokens IS NOT NULL AND prompt_tokens > 0") {
		t.Error("prompt_tokens ≤ 0 / NULL 的行必须整行排除（分子分母都不参与）")
	}
	// 逐行 clamp（cached 可能超 prompt 的供应商怪值不许抬高分子）与零除兜底。
	if !strings.Contains(body, "LEAST(GREATEST(COALESCE(cache_read_tokens, 0), 0), prompt_tokens)") {
		t.Error("分子缺逐行 clamp：cache_read 超过 prompt 的怪值会造出 >1 的命中率")
	}
	if !strings.Contains(body, "NULLIF(SUM(prompt_tokens), 0)") {
		t.Error("分母缺 NULLIF 兜底：零和分母会 22012 而不是 NULL")
	}
	// 签名与默认值不变：调用点 (c.id, mo.raw_model_name, 50) 逐位对应。
	if !strings.Contains(body, "p_sample_n integer DEFAULT 50") ||
		!strings.Contains(body, "p_window_hours integer DEFAULT 3") {
		t.Error("函数签名/默认值漂移会打碎候选查询的 CROSS JOIN LATERAL 调用点")
	}

	// ---- 阳性对照：证明负向判据分得清 ----
	const v1Sample = "SELECT prompt_tokens FROM request_logs_hot WHERE outbound_model = p_raw_model"
	for _, probe := range []string{"FROM request_logs_hot", "outbound_model"} {
		if !strings.Contains(v1Sample, probe) {
			t.Errorf("阳性对照失效：%q 没有命中含 v1 引用的样本 —— 对应负向断言恒绿", probe)
		}
	}
}

// TestRecentCacheHitRateEnsureRealDB 是真库层判据（r1039 对 rsr 的同款）。
//
// LANGUAGE sql 的函数体在 CREATE 时校验表引用 ⇒ 「体里引用的表都在场」
// 只有真库 CREATE 才算数；顺带用真数据钉 token 加权口径与 NULL fail-open。
// 夹具只建 session_turns_hot（族退役形态：无任何 request_logs* 表）——
// 这正是 211 事故当晚的库形态。
func TestRecentCacheHitRateEnsureRealDB(t *testing.T) {
	pool := scratchPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	d := &DB{pool: pool}

	exec := func(q string) {
		t.Helper()
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("exec failed: %v\nSQL: %s", err, q)
		}
	}
	// 夹具：只建会话族热表（TEXT credential_id + 会话族列名）。列型对齐
	// 707 S1a 的真表（prompt/cache_read 是 bigint，SUM 后 cast double）。
	exec(`DROP TABLE IF EXISTS public.request_logs_hot, public._request_logs_hot;`)
	exec(`CREATE TABLE public.session_turns_hot (
		id BIGSERIAL PRIMARY KEY, credential_id TEXT, model TEXT, canonical_model TEXT,
		prompt_tokens BIGINT, cache_read_tokens BIGINT,
		ts TIMESTAMPTZ NOT NULL DEFAULT now(),
		task_type TEXT, origin_stage TEXT, request_id TEXT);`)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DROP FUNCTION IF EXISTS public.recent_cache_hit_rate(bigint, text, int, int);`)
		_, _ = pool.Exec(bg, `DROP TABLE IF EXISTS public.session_turns_hot;`)
	})

	// ① 族退役形态下 CREATE 必须成功（当晚坏体在这里就会 42P01 中止 boot）。
	if err := d.ensureRecentCacheHitRate(ctx); err != nil {
		t.Fatalf("族退役（无 request_logs*）时 ensure 必须成功，got: %v", err)
	}
	// 幂等：第二次执行同样成功（boot 链每次启动都跑）。
	if err := d.ensureRecentCacheHitRate(ctx); err != nil {
		t.Fatalf("ensure 幂等重跑失败: %v", err)
	}

	// ② PG 里的实际函数体（prosrc）：会话腿在场、v1 引用不在场。
	var prosrc string
	if err := pool.QueryRow(ctx, `
		SELECT prosrc FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname='public' AND p.proname='recent_cache_hit_rate'`,
	).Scan(&prosrc); err != nil {
		t.Fatalf("read recent_cache_hit_rate prosrc: %v", err)
	}
	if !strings.Contains(prosrc, "session_turns_hot") {
		t.Error("库内函数体必须读 session_turns_hot")
	}
	for _, unwanted := range []string{"request_logs_hot", "_request_logs", "outbound_model", "client_model"} {
		if strings.Contains(prosrc, unwanted) {
			t.Errorf("库内函数体仍引用 %q —— 族退役库上 CREATE 时校验即 42P01", unwanted)
		}
	}

	// ③ 真数据口径：token 加权 + 逐行 clamp + 排除谓词 + 回退链 + 窗口。
	exec(`INSERT INTO public.session_turns_hot
		(credential_id, model, canonical_model, prompt_tokens, cache_read_tokens, ts, task_type, origin_stage, request_id)
		VALUES
		-- 计入：直命中 model。
		('7',  'm1', NULL,   100,  50,  now() - interval '1 minute',  NULL,   NULL,     'r1'),
		-- 计入：model 为空回落 canonical_model，且大小写不敏感。
		('7',  NULL, 'M1',   100,  1000, now() - interval '2 minute', NULL,   NULL,     'r2'),
		-- 排除：prompt_tokens = 0（分母不许为 0）。
		('7',  'm1', NULL,   0,    100,  now() - interval '3 minute', NULL,   NULL,     'r3'),
		-- 排除：prompt_tokens NULL。
		('7',  'm1', NULL,   NULL, 100,  now() - interval '4 minute', NULL,   NULL,     'r4'),
		-- 排除：探针三族。
		('7',  'm1', NULL,   999,  999,  now() - interval '5 minute', 'probe_triggered', NULL, 'r5'),
		('7',  'm1', NULL,   999,  999,  now() - interval '6 minute', NULL,   'node_probe', 'r6'),
		('7',  'm1', NULL,   999,  999,  now() - interval '7 minute', NULL,   NULL,     'probe-r7'),
		-- 排除：非数字 credential_id（TEXT 守卫）。
		('abc','m1', NULL,   999,  999,  now() - interval '8 minute', NULL,   NULL,     'r8'),
		-- 排除：窗口外（默认 3h）。
		('7',  'm1', NULL,   100,  100,  now() - interval '10 hour',  NULL,   NULL,     'r9'),
		-- 排除：别的模型。
		('7',  'm2', NULL,   999,  999,  now() - interval '9 minute', NULL,   NULL,     'r10')`)

	var rate *float64
	var samples int
	if err := pool.QueryRow(ctx,
		`SELECT rate, samples FROM public.recent_cache_hit_rate(7, 'm1', 50)`).Scan(&rate, &samples); err != nil {
		t.Fatalf("call recent_cache_hit_rate: %v", err)
	}
	// 计入 r1（50/100）与 r2（clamp 1000→100，即 100/100）：
	// rate = (50+100)/(100+100) = 0.75；samples = 2。
	if rate == nil || samples != 2 {
		t.Fatalf("want rate=0.75 samples=2, got rate=%v samples=%d", rate, samples)
	}
	if got := *rate; got < 0.7499 || got > 0.7501 {
		t.Fatalf("token-weighted rate drifted: want 0.75, got %f", got)
	}

	// ④ 空窗 fail-open：无合格行 ⇒ rate NULL + samples 0（调用方按无信号中性）。
	if err := pool.QueryRow(ctx,
		`SELECT rate, samples FROM public.recent_cache_hit_rate(424242, 'm1', 50)`).Scan(&rate, &samples); err != nil {
		t.Fatalf("call recent_cache_hit_rate on empty window: %v", err)
	}
	if rate != nil || samples != 0 {
		t.Fatalf("empty window must be (NULL, 0) for fail-open, got rate=%v samples=%d", rate, samples)
	}
}
