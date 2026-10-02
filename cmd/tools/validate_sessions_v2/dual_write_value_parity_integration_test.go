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
func TestDualWriteValueParity(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset —— 跳过**不构成**「两族值层一致」的证据")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
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
		           UNION ALL SELECT request_id, model FROM public.session_turns_hot)
		SELECT v.outbound_model, s.model
		FROM public.request_logs v JOIN s ON s.request_id = v.request_id
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
