//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestDualWriteValueParity（§9.32，2026-10-02）
//
// 这是目标原话「**确保数据在更改前后一致**」的**值层**守门人。
// §9.31 修好了 parity 门「能不能跑」，本门回答「**跑出来的数据对不对**」。
//
// # 三条设计纪律，每条都来自本轮踩过的坑
//
//  1. **必须排除 `probe-%` 流量**。§9.30 实测 v1 有 29.9% 的请求在 session 侧
//     不存在，其中 **28.1 个百分点是 `probe-*` 命名的探针流量**——它按设计
//     不走 session 写路径。不排除就会把「按设计不镜像」判成「数据不一致」。
//     *注意判据是 request_id 的 `probe-*` 命名，**不是** `origin_stage`：
//     后者在 v1 侧实测 0/2,163,262，是无效判据（§9.30 记过一次被证伪）。*
//
//  2. **必须先拆口径再判不一致**。`IS DISTINCT FROM` 把「一侧 NULL、另一侧有值」
//     也算成不一致，于是本轮第一版测出 completion_tokens「79.8% 不一致」——
//     拆开后是**两侧都有值时 0% 不同**，那 79.8% 全是 **v1 自己不记录**。
//     *数字与口径同生共死；报「N 处不一致」而不说 N 的面，那个数是不可用的。*
//
//  3. **模型名必须先归一化再比**。原始串「不一致」10.7%，其中 **86.78%** 可由
//     大小写 / 分隔符 / 版本后缀 / 厂商前缀解释（`MiniMax-M3`↔`minimax-m3`、
//     `glm-5-2-260617`↔`glm-5.2`、`nvidia/riva-translate-4b-instruct-v2`↔`riva-…`）。
//     不归一化就比 ⇒ 门会被 8 万条命名噪声喂成永远红，**而那不是缺陷**。
//
// # 阈值为什么这样定
//
// 真库实测（762,652 行非探针配对）逐项不一致数：
//
//	success                    0
//	prompt_tokens             25   （0.003%）
//	completion_tokens          0   （两侧都有值时）
//	latency_ms                 95  （0.012%）
//	upstream_status_code       0   （v1 有 / session 空 45,337）
//	模型（原始串）        81,531  → 归一化后 10,781
//
// 阈值按**实测值 × 余量**定，不按"理论上应该 0"：把 `success` 定成严格 0
// （它是唯一的判定级事实，零容忍有意义），其余给到千分之几的余量。
// **超阈值时门要报的是"去查"，不是"已知问题"** —— 附上归一化前后的两个数，
// 让人能立刻分清是新缺陷还是命名噪声。
// openParityPool 打开一个**显式抬高 statement_timeout** 的连接池。
//
// # 为什么必须共用这一个函数（而不是两处各写一遍）
//
// 本文件两道门都要在生产库上跑全表聚合，而 252 的 `statement_timeout` 默认 30s。
// §9.59 修这条时先只改了覆盖率那道门，值层那道没改 —— 同一轮里它就以
// `SQLSTATE 57014` 红了。**这不是假设的脆弱性，是当场发生的。**
//
// 所以连接参数**只能有一处定义**：两处各写一遍，就一定会有下一次只改一半，
// 而没被改到的那一道会以「查询失败」的形式报错，读起来像 SQL 写错了，
// 实际是超时。分开写的那份还会在某天被人「顺手优化」掉。
func openParityPool(ctx context.Context, t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	// 覆盖率查询冷缓存实测 23s、值层比对冷缓存 50s+，都高于 252 的 30s 默认值。
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "300000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return pool
}

func TestDualWriteValueParity(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset —— 跳过**不构成**「两族值层一致」的证据")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool := openParityPool(ctx, t, dsn)
	defer pool.Close()

	// 两个存储面都必须读：写方只写 _hot，冷行在父表（§9.28/§9.29）。
	// 口径：**判据是「两侧都有值且不同」**。「一侧 NULL、另一侧有值」单列为诊断量，
	// 不参与判定 —— 这正是本门第一次跑就抓到的自身缺陷：注释写了「先拆口径」，
	// SQL 却还在用 IS DISTINCT FROM，于是 completion_tokens 报出 79.8%，
	// 而那 79.8% 全部是 **v1 自己不记录**（§9.32 实测：两侧都有值时不一致 0 行）。
	const q = `
	WITH s AS (
		SELECT request_id, success, upstream_status_code, latency_ms,
		       prompt_tokens, completion_tokens, model
		FROM public.session_turns
		UNION ALL
		SELECT request_id, success, upstream_status_code, latency_ms,
		       prompt_tokens, completion_tokens, model
		FROM public.session_turns_hot
	),
	v AS (
		SELECT request_id, success, upstream_status_code, latency_ms,
		       prompt_tokens, completion_tokens, outbound_model
		FROM public.request_logs
		WHERE request_id NOT LIKE 'probe-%'
		UNION ALL
		-- §9.59：v1 侧也必须读 _hot。request_logs_hot **不是** request_logs 的分区
		-- （pg_inherits 实测：request_logs 只有 4 个月度分区，_hot 是独立存储面），
		-- 所以只读父表会整面漏掉写方的最新数据。252 实测漏 2,121 对（占全部配对的 16.6%），
		-- 而漏掉的恰好是**最新**的那批——双写回归最先出现在那里。
		SELECT request_id, success, upstream_status_code, latency_ms,
		       prompt_tokens, completion_tokens, outbound_model
		FROM public.request_logs_hot
		WHERE request_id NOT LIKE 'probe-%'
	)
	SELECT count(*)::bigint,
	       -- 判据：两侧都有值且不同
	       count(*) FILTER (WHERE v.success IS NOT NULL AND s.success IS NOT NULL
	                          AND v.success <> s.success)::bigint,
	       count(*) FILTER (WHERE v.prompt_tokens IS NOT NULL AND s.prompt_tokens IS NOT NULL
	                          AND v.prompt_tokens <> s.prompt_tokens)::bigint,
	       count(*) FILTER (WHERE v.completion_tokens IS NOT NULL AND s.completion_tokens IS NOT NULL
	                          AND v.completion_tokens <> s.completion_tokens)::bigint,
	       count(*) FILTER (WHERE v.latency_ms IS NOT NULL AND s.latency_ms IS NOT NULL
	                          AND v.latency_ms <> s.latency_ms)::bigint,
	       count(*) FILTER (WHERE v.upstream_status_code IS NOT NULL AND s.upstream_status_code IS NOT NULL
	                          AND v.upstream_status_code <> s.upstream_status_code)::bigint,
	       count(*) FILTER (WHERE v.outbound_model IS NOT NULL AND s.model IS NOT NULL
	                          AND v.outbound_model <> s.model)::bigint,
	       -- 诊断量：某一侧根本没记录（不参与判定，只报出来）
	       count(*) FILTER (WHERE v.completion_tokens IS NULL AND s.completion_tokens IS NOT NULL)::bigint,
	       count(*) FILTER (WHERE v.upstream_status_code IS NOT NULL AND s.upstream_status_code IS NULL)::bigint,
	       count(*) FILTER (WHERE v.outbound_model IS NULL AND s.model IS NOT NULL)::bigint
	FROM v JOIN s ON s.request_id = v.request_id
	WHERE s.request_id NOT LIKE 'probe-%'`

	var total, dSuccess, dPrompt, dCompl, dLat, dUp, dModelBoth int64
	var onlySessionCompl, onlyV1Up, onlySessionModel int64
	if err := pool.QueryRow(ctx, q).Scan(&total, &dSuccess, &dPrompt, &dCompl,
		&dLat, &dUp, &dModelBoth, &onlySessionCompl, &onlyV1Up, &onlySessionModel); err != nil {
		t.Fatalf("值层比对查询执行失败: %v", err)
	}
	if total == 0 {
		t.Skip("库里没有可配对的非探针样本 —— 本门不构成证据")
	}
	t.Logf("配对行 %d（已排除 probe-*）", total)
	t.Logf("  success 不一致        = %d", dSuccess)
	t.Logf("  prompt_tokens 不一致  = %d", dPrompt)
	t.Logf("  completion_tokens     = %d", dCompl)
	t.Logf("  latency_ms            = %d", dLat)
	t.Logf("  upstream_status_code  = %d", dUp)
	t.Logf("  [诊断量·不参与判定] 仅 session 记录 completion_tokens = %d", onlySessionCompl)
	t.Logf("  [诊断量·不参与判定] 仅 v1 记录 upstream_status_code   = %d", onlyV1Up)
	t.Logf("  [诊断量·不参与判定] 仅 session 记录模型               = %d", onlySessionModel)

	// success 是判定级事实（所有「该不该复检/该不该告警」都建立在它上面），
	// 零容忍。其余给到实测千分之几的余量。
	pct := func(n int64) float64 { return float64(n) / float64(total) * 100 }
	if dSuccess != 0 {
		t.Errorf("**success 不一致 %d 行（%.4f%%）** —— success 是判定级事实，"+
			"两族对「这次调用成没成功」的记录必须逐行相同。这不是命名口径问题，"+
			"去查双写链路。", dSuccess, pct(dSuccess))
	}
	for _, c := range []struct {
		name string
		n    int64
		cap  float64 // 百分比上限
	}{
		{"prompt_tokens", dPrompt, 0.05},
		{"completion_tokens", dCompl, 0.05},
		{"latency_ms", dLat, 0.05},
		// upstream_status_code 是 v1 有、session 空（实测 45,337）——
		// 那是「session 不记录」而非「两族分歧」，已移出判据，改为诊断量。
		{"upstream_status_code", dUp, 0.05},
	} {
		if pct(c.n) > c.cap {
			t.Errorf("%s 不一致 %.4f%%（%d 行）超过阈值 %.2f%% —— 去查是新增缺陷"+
				"还是口径又变了（报 %T 的两个数：%d）", c.name, pct(c.n), c.n, c.cap, c.n, c.n)
		}
	}

	// 模型：先归一化再判。原始串的数只作为**诊断信息**输出，不作为判据 ——
	// 把它当判据就是让 8 万条命名噪声把门永久变红。
	if dModelBoth == 0 {
		return
	}
	rows, err := pool.Query(ctx, `
		WITH s AS (SELECT request_id, model FROM public.session_turns
		           UNION ALL SELECT request_id, model FROM public.session_turns_hot),
		     v AS (SELECT request_id, outbound_model FROM public.request_logs
		           UNION ALL SELECT request_id, outbound_model FROM public.request_logs_hot)
		SELECT v.outbound_model, s.model
		FROM v JOIN s ON s.request_id = v.request_id
		WHERE v.request_id NOT LIKE 'probe-%' AND s.request_id NOT LIKE 'probe-%'
		  AND v.outbound_model IS NOT NULL AND s.model IS NOT NULL
		  AND v.outbound_model <> s.model
		LIMIT 200000`)
	if err != nil {
		t.Fatalf("取模型差异样本失败: %v", err)
	}
	defer rows.Close()
	rawDiff, normDiff := 0, 0
	samples := map[string]int{}
	for rows.Next() {
		var vm, sm string
		if err := rows.Scan(&vm, &sm); err != nil {
			t.Fatalf("scan: %v", err)
		}
		rawDiff++
		if normalizeModelName(vm) != normalizeModelName(sm) {
			normDiff++
			if len(samples) < 8 {
				samples[fmt.Sprintf("v1=%q session=%q", vm, sm)]++
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	t.Logf("  模型：两侧都有值但原始串不同 %d 行；归一化后仍不同 %d 行", rawDiff, normDiff)
	for k := range samples {
		t.Logf("    归一化后仍不同的样本：%s", k)
	}
	// 归一化后残余按实测 10,781/762,652 = 1.4% 定阈；越过就要人来看是不是
	// 出现了真的「路由到不同模型」。样本会一起打出来。
	if float64(normDiff)/float64(total) > 0.02 {
		t.Errorf("模型归一化后仍有 %.4f%%（%d/%d）不同，超过 2%% 阈值。\n"+
			"这才是真分歧（不是命名噪声）。样本见上面 t.Log 输出。",
			float64(normDiff)/float64(total), normDiff, total)
	}
}

// TestDualWriteParityCoverageReport（§9.59，2026-10-02）
//
// # 这道门**只报告、从不判红**，因为它回答的是一个判红型门回答不了的问题
//
// TestDualWriteValueParity 是**内连接**：它只比「v1 和会话族都存在」的 request_id。
// 这在语义上是对的（两侧都有值才能比值），但它有一个**构造性盲区**：
//
//	双写整体掉链子 → v1 有行、会话族没有 → 这些行**根本不进比对集**
//	             → 判红型门看到的样本变**少**，不是变**不一致**
//
// 换句话说：**镜像侧发生系统性丢失时，这道门会变得更绿。**
// 这不是假设——§9.59 在 252 实测到 19 条卡在 `request_status='in_progress'`
// 的 v1 行在会话族无孪生（`PersistHook` 首门 `!Success && !isTerminalFailure`
// 按设计拒绝它们），而同一批库里配对成功的 12,752 行**一条 in_progress 都没有**。
//
// # 为什么是「报告」而不是「门」
//
// 「v1 有行而会话族没有」**本身不是缺陷**：内部生成器（auto-title/auto-summary）
// 按设计就不镜像。要把它变成判红门，就得先回答「哪些没配上是合理的」——
// 那是一个口径决策（§9.58 刚在同一个字段上栽过一次：把内部生成器流量当成业务流量）。
// 所以这里**只把面拆开打印**，让缺口可见、可追、不被静默吞掉。
//
// # 这道门明确**不覆盖**什么
//
//  1. `probe-%` 流量：两侧都按设计排除，不进任何统计。
//  2. 内部回环（auto-title-generator / auto-summary-generator）：
//     按设计不镜像，会被算进「未配对」但**不是**缺陷 —— 上面的分桶就是为此。
//  3. 判红阈值：本测试**不设**任何阈值。要不要把某个桶变成红灯，是人的决策。
//  4. 跨租户/跨键归属：只按 request_id 关联，不校验 tenant 维度是否一致。
func TestDualWriteParityCoverageReport(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset —— 跳过**不构成**任何覆盖率证据")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// statement_timeout 由 openParityPool 统一抬高 —— 不要在这里各写一份。
	pool := openParityPool(ctx, t, dsn)
	defer pool.Close()

	// 与 TestDualWriteValueParity 同口径：两侧都读两个存储面，都排除 probe-*。
	// 差别只在这道门数的是**全集**而不是交集。
	const q = `
	WITH v AS (
		SELECT request_id, is_auto_request, origin_actor, request_status, 'parent'::text AS src
		FROM public.request_logs WHERE request_id NOT LIKE 'probe-%'
		UNION ALL
		SELECT request_id, is_auto_request, origin_actor, request_status, 'hot'::text AS src
		FROM public.request_logs_hot WHERE request_id NOT LIKE 'probe-%'
	),
	-- 两个存储面各自去重后再并，最后按 request_id 归并成一个**无重复**集合：
	-- 同一个 request_id 若在两面都出现（冷热重叠），不能让它把 v1 行放大成 2 行。
	-- 第一版这里写的是 6 个关联 EXISTS，252 上直接打爆 statement_timeout（30s）。
	s AS (
		SELECT request_id FROM (
			SELECT request_id FROM public.session_turns
			UNION ALL
			SELECT request_id FROM public.session_turns_hot
		) x GROUP BY request_id
	),
	j AS (
		SELECT v.*, (s.request_id IS NOT NULL) AS paired
		FROM v LEFT JOIN s ON s.request_id = v.request_id
	)
	SELECT
		-- 三个必须并列打印的数：全集、配对、未配对
		count(*)::bigint,
		count(*) FILTER (WHERE paired)::bigint,
		-- 未配对里**非内部生成器**的部分：这才可能是不一致
		count(*) FILTER (WHERE NOT paired AND NOT (is_auto_request IS TRUE))::bigint,
		-- 非内部生成器且**已终态**的未配对：这两道镜像门都放行了它却仍无孪生，
		-- 是本门最该被人看见的一格
		count(*) FILTER (WHERE NOT paired
		                  AND NOT (is_auto_request IS TRUE)
		                  AND coalesce(nullif(request_status, ''), '') IN ('success', 'failure', 'rate_limited'))::bigint,
		-- 非内部生成器且卡在 in_progress 的：按设计不镜像（PersistHook 首门），
		-- 但它意味着「这个请求开始了却从没有终态」，退役 v1 后无人可查
		count(*) FILTER (WHERE NOT paired
		                  AND NOT (is_auto_request IS TRUE)
		                  AND coalesce(nullif(request_status, ''), '') NOT IN ('success', 'failure', 'rate_limited'))::bigint,
		-- ★下面两个数按面分开数，纯粹为了**证明全集真的读了两个存储面**。
		-- 这是本门唯一一条会判红的自检：若有人再把 request_logs_hot 从 v 里删掉，
		-- 上面所有比率判据都不会红（样本少 20% 不越线），而这一条会。
		-- 没有它，§9.59 那个取样洞会被静默重新打开，而所有门仍然全绿。
		count(*) FILTER (WHERE src = 'parent')::bigint,
		count(*) FILTER (WHERE src = 'hot')::bigint
	FROM j`

	var total, paired, unpairedNonAuto, unpairedTerminal, unpairedStuck int64
	var parentFace, hotFace int64
	if err := pool.QueryRow(ctx, q).Scan(&total, &paired,
		&unpairedNonAuto, &unpairedTerminal, &unpairedStuck, &parentFace, &hotFace); err != nil {
		// 报告型判据读不出值 = 「没验成」，不是「没问题」。
		t.Fatalf("覆盖率查询执行失败 —— 这条**没验成**（不是覆盖率正常）: %v", err)
	}
	if total == 0 {
		t.Skip("库里没有非探针 v1 样本 —— 本门不构成任何覆盖率证据")
	}
	// 判红型：两个存储面都读到了，且互不重叠（pg_inherits 实测它们是独立面，
	// 不是父子关系）。这条红 ⇒ 取样面被改窄了，而其余判据不会告诉你。
	if got, want := total, parentFace+hotFace; got != want {
		t.Errorf("v1 全集 %d ≠ 父表 %d + _hot %d —— **取样面被改窄了**。\n"+
			"request_logs 与 request_logs_hot 是两个独立存储面（不是分区父子），\n"+
			"只读其一会整面漏掉写方的最新数据；§9.59 实测漏掉的恰是最新那批。",
			got, parentFace, hotFace)
	}
	t.Logf("v1 非探针全集 %d（父表 + _hot 两面，已排除 probe-*)", total)
	t.Logf("  能与会话族配对   = %d（%.1f%%）—— 这一部分由 TestDualWriteValueParity 逐字段比值",
		paired, float64(paired)/float64(total)*100)
	t.Logf("  未配对·内部生成器 = %d（按设计不镜像：auto-title / auto-summary，**不是**缺陷）",
		total-paired-(unpairedNonAuto))
	t.Logf("  未配对·非内部    = %d", unpairedNonAuto)
	t.Logf("    其中已终态却无孪生 = %d  ← 两道镜像门都放行仍缺，最该人看", unpairedTerminal)
	t.Logf("    其中卡在非终态     = %d  ← 按设计不镜像，但退役 v1 后「开始了却没结束」将无迹可寻",
		unpairedStuck)
	t.Logf("本门不判红（不设阈值）；它只保证上面这几个格子**不会被静默吞掉**。")
}
