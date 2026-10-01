-- =====================================================================
-- 审计诊断脚本：计费列的四种失效形态（104 号 / 103 号 / 100 号）
-- =====================================================================
-- 用途：在【生产库】上一次性量化「计费到底准不准」，定位到具体模型。
-- 性质：纯只读（全部 SELECT），不写库、不加锁、不改数据。
--
-- 背景：这四类失效的共同点是「列本身不携带状态信息」，
--       所以它们在费用面板上都表现为「金额偏低」，无法互相区分。
--       本脚本就是为了把它们区分开。
--
-- 使用方法：
--   docker exec -i <pg-container> psql -U postgres -d llm_gateway -f - < 本文件
--   （不要加 -h 127.0.0.1；用容器内 socket）
--
-- 读法：
--   * 每节都先打印分母（样本量），再看比例 —— 比例脱离分母没有意义。
--   * 样本量 < 1000 的结论只能当「方向性提示」，不要当生产结论。
--   * 若某节返回 0 行 ≠ 无问题，先确认该节的分母是不是 0。
-- =====================================================================

\pset border 2
\echo '==== 0. 规模基线（先量上界，后面所有比例都以此为分母）===='

SELECT 'usage_ledger_hot rows'          AS metric, count(*)::text AS value FROM usage_ledger_hot
UNION ALL SELECT 'request_logs_hot rows',  count(*)::text FROM request_logs_hot
UNION ALL SELECT 'model_offers rows',      count(*)::text FROM model_offers
UNION ALL SELECT 'pricing_plans rows',     count(*)::text FROM pricing_plans;

SELECT 'usage_ledger_hot ts range' AS metric,
       COALESCE(min(ts)::text,'-')||' .. '||COALESCE(max(ts)::text,'-') AS value
FROM usage_ledger_hot;

-- =====================================================================
\echo ''
\echo '==== 1. [待裁决 46] 无定价 ⇒ cost_usd 为 NULL —— 计费列第二种静默失效 ===='
\echo '     期望：unpriced 占比接近 0。占比高 ⇒ 大批请求从未被计费。'

SELECT
    left(COALESCE(raw_model_name,'(null)'), 28) AS model,
    count(*)                                             AS success_rows,
    count(*) FILTER (WHERE cost_usd IS NULL)           AS unpriced,
    round(100.0 * count(*) FILTER (WHERE cost_usd IS NULL)
          / NULLIF(count(*),0), 1)                      AS unpriced_pct,
    count(*) FILTER (WHERE prompt_tokens IS NULL
                       AND completion_tokens IS NULL)   AS no_token_at_all
FROM usage_ledger_hot
WHERE success
GROUP BY 1
HAVING count(*) FILTER (WHERE cost_usd IS NULL) > 0
ORDER BY unpriced DESC
LIMIT 25;

\echo '--- 1b. 关键鉴别：这些行的 token 在不在？（token 在 ⇒ 是「没配价」而非「没抓到」）---'
SELECT
    CASE WHEN prompt_tokens IS NULL AND completion_tokens IS NULL
         THEN 'token 也缺（可能上游未报 usage 且估算失败）'
         ELSE 'token 齐全 ⇒ 缺的是价格，不是数据' END AS diagnosis,
    count(*) AS rows
FROM usage_ledger_hot
WHERE success AND cost_usd IS NULL
GROUP BY 1;

-- =====================================================================
\echo ''
\echo '==== 2. [103 号] 约定错配 ⇒ cost_usd 算成负数 ===='
\echo '     期望：0 行。若有负数 ⇒ CalcCost 对 Anthropic 形状 usage 少算 cached×priceIn。'

SELECT left(COALESCE(raw_model_name,'(null)'),28) AS model,
       count(*) AS neg_rows,
       round(min(cost_usd)::numeric, 8) AS worst_cost_usd
FROM usage_ledger_hot
WHERE cost_usd < 0
GROUP BY 1
ORDER BY neg_rows DESC
LIMIT 25;

-- =====================================================================
\echo ''
\echo '==== 3. [100 号 / 待裁决 42] 台账行缺幂等 ⇒ 同一 request_id 多行 ===='
\echo '     期望：0 行。>0 行 ⇒ 费用聚合会翻倍（视图是裸 UNION ALL，不去重）。'
\echo '     注意：100 号已证触发前提已收窄为「歧义提交」，本节应为低频。'

WITH dup AS (
    SELECT request_id, count(*) AS n
    FROM usage_ledger_with_current_month
    WHERE ts >= now() - interval '30 days'
    GROUP BY request_id
    HAVING count(*) > 1
)
SELECT 'duplicate request_id in last 30d' AS metric,
       count(*)::text AS dup_request_ids,
       COALESCE(sum(n)::text,'-') AS total_extra_rows
FROM dup;

\echo '--- 3b. 对照：宽表是否也有重复？（request_logs_hot PK=request_id，期望 0）---'
SELECT 'request_logs_hot duplicate request_id' AS metric, count(*)::text AS value
FROM (
    SELECT request_id FROM request_logs_hot GROUP BY request_id HAVING count(*) > 1
) d;

-- =====================================================================
\echo ''
\echo '==== 4. 定价数据本身的覆盖率 ===='
\echo '     期望：priced 占比高。占比低 ⇒ 第 1 节的 unpriced 高是配置问题而非代码问题。'

SELECT
    count(*)                                                          AS offers_total,
    count(*) FILTER (WHERE COALESCE(unit_price_in_per_1m,0) > 0
                        AND COALESCE(unit_price_out_per_1m,0) > 0)     AS fully_priced,
    count(*) FILTER (WHERE COALESCE(unit_price_in_per_1m,0) = 0
                        OR  COALESCE(unit_price_out_per_1m,0) = 0)     AS missing_or_zero_price,
    count(*) FILTER (WHERE pricing_source IS NULL)                     AS no_pricing_source,
    round(100.0 * count(*) FILTER (WHERE COALESCE(unit_price_in_per_1m,0) > 0
                     AND COALESCE(unit_price_out_per_1m,0) > 0)
          / NULLIF(count(*),0), 2)                                     AS fully_priced_pct
FROM model_offers;

\echo '--- 4b. 定价来源分布（null 占比高说明价格从未被导入/维护过）---'
SELECT COALESCE(pricing_source,'(NULL)') AS pricing_source,
       COALESCE(billing_mode,'(NULL)')   AS billing_mode,
       count(*) AS offers
FROM model_offers
GROUP BY 1,2
ORDER BY offers DESC
LIMIT 15;

-- =====================================================================
\echo ''
\echo '==== 5. 兜底链路：pricing_plans 的实际覆盖 ===='
\echo '     代码侧已接线（provider/client.go:1671-1677 的 LATERAL，'
\echo '     键是 model_canonical_id + credential 级）。若这里也很薄，则第 1 节无解。'

SELECT count(*)                                                    AS plans_total,
       count(*) FILTER (WHERE effective_to IS NULL)                AS plans_active,
       count(*) FILTER (WHERE effective_to IS NULL
                          AND COALESCE(NULLIF(plan_json->>'input_per_1m','')::float8,0) > 0)
                                                                AS plans_with_price,
       count(DISTINCT model_canonical_id)                          AS distinct_canonical_models,
       count(*) FILTER (WHERE credential_id IS NULL)               AS global_scope_plans
FROM pricing_plans;

-- =====================================================================
\echo ''
\echo '==== 6. 预算强制的实际读数（verifier.go:786 用的就是这个口径）====='
\echo '     若某 key 的 sum 明显低于其在费用面板看到的合计，说明口径已分叉。'

SELECT left(COALESCE(raw_model_name,'(null)'),24) AS model,
       count(DISTINCT api_key_id)                       AS keys,
       round(sum(COALESCE(cost_usd,0))::numeric, 6)     AS sum_cost_usd
FROM usage_ledger_hot
WHERE success AND cost_usd IS NOT NULL
GROUP BY 1
ORDER BY sum_cost_usd DESC
LIMIT 15;

\echo ''
\echo '==== 诊断结束。判定规则 ===='
\echo '  第 1 节 unpriced_pct 高 + 第 1b 判为「token 齐全」 + 第 4 节 fully_priced_pct 低'
\echo '    ⇒ 【配置问题】先补价格，代码不用改（待裁决 46 的修法只保证缺口可见）。'
\echo '  第 2 节有负数'
\echo '    ⇒ 【代码问题】CalcCost 约定错配，待裁决 45，必须修。'
\echo '  第 3 节有重复'
\echo '    ⇒ 【代码问题】台账行缺幂等，待裁决 42，必须修。'
\echo '  以上都干净 ⇒ 计费列在本库上是健康的，104 号的 96.3% 属实例配置。'
