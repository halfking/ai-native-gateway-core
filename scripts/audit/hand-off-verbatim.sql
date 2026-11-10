-- hand-off-verbatim.sql —— ★★ handoff 的基线 SQL **原样**执行（用于留证据）
--
-- ★ 用途：handoff 的 Phase 2 任务 2.1 明确要求「**重新执行任务 0.2 的 SQL**」，
--   任务 2.2 又给了三条 grep + 一条 featured 频率 SQL。
--   ★ 本脚本**逐字**保留 handoff 的写法 ⇒ ★★ 用来证明：**其中三条在真库上必然报错**
--   （引用的列不存在）⇒ ⇒ **「原样复采」这条指令在当前 schema 上不可执行**。
--
-- ★ 执行（psql 默认不在首个错误后中止 ⇒ 所有语句都会跑，错误会逐条打印）：
--   scp -q scripts/audit/hand-off-verbatim.sql 211:/tmp/
--   ssh 211 'docker cp /tmp/hand-off-verbatim.sql acc-pg17:/tmp/ \
--            && docker exec acc-pg17 psql -U acc_super -d llm_gateway -tA -F"|" \
--               -f /tmp/hand-off-verbatim.sql'
--
-- ★★★ 预期（2026-11-10 15:0x 实测）：指标 1 成功；★ **指标 2/3/4 全部报错**；
--   任务 2.2 的 featured 频率 SQL **返回 0 行**（handoff 的示例模型名是幻觉名）。

\echo '===== handoff 指标 1：当前 unavailable 凭据数 ====='
SELECT COUNT(*) as unavailable_count
FROM credential_model_bindings
WHERE available = FALSE;

\echo '===== handoff 指标 2：免费凭据可用率（★ 原样 ⇒ 预期报错：credentials 无 billing_mode / provider 列） ====='
SELECT
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE cmb.available = TRUE) as available,
    COUNT(*) FILTER (WHERE cmb.available = TRUE) * 100.0 / COUNT(*) as available_pct
FROM credential_model_bindings cmb
JOIN credentials c ON cmb.credential_id = c.id
WHERE c.billing_mode IN ('free', 'trial')
   OR c.provider LIKE '%free%';

\echo '===== handoff 指标 2 附：先查表结构（★ 原样） ====='
\d credentials

\echo '===== handoff 指标 2 附：若无 billing_mode，先识别免费供应商（★ 原样） ====='
SELECT DISTINCT provider, notes
FROM credentials
WHERE provider LIKE '%free%' OR notes LIKE '%free%' OR notes LIKE '%trial%';

\echo '===== handoff 指标 3：探测间隔分布（★ 原样 ⇒ 预期报错：node_probe_state 无 last_probe_at 列） ====='
SELECT
    percentile_cont(0.50) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (NOW() - last_probe_at))/60) as p50_minutes,
    percentile_cont(0.95) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (NOW() - last_probe_at))/60) as p95_minutes,
    percentile_cont(0.99) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (NOW() - last_probe_at))/60) as p99_minutes
FROM node_probe_state
WHERE last_probe_at IS NOT NULL;

\echo '===== handoff 指标 4：探测覆盖率（★ 原样 ⇒ 预期报错：同上） ====='
SELECT
    COUNT(*) as total,
    COUNT(*) FILTER (WHERE last_probe_at > NOW() - INTERVAL '24 hours') as probed_24h,
    COUNT(*) FILTER (WHERE last_probe_at > NOW() - INTERVAL '24 hours') * 100.0 / COUNT(*) as coverage_pct
FROM node_probe_state;

\echo '===== handoff 任务 2.2-3：featured 模型探测频率（★ 原样 ⇒ 预期 0 行：示例名是幻觉名） ====='
SELECT raw_model, COUNT(*) as probe_count
FROM node_probe_runs
WHERE created_at > NOW() - INTERVAL '24 hours'
  AND raw_model IN ('gpt-4o-mini', 'claude-haiku-3.5', 'gemini-flash-2.0')
GROUP BY raw_model
ORDER BY probe_count DESC;

\echo '===== handoff 任务 2.2-3 的真实列名核对（node_probe_runs 实际叫 raw_model_name） ====='
SELECT COUNT(*) AS handoff_sql_rows
FROM node_probe_runs
WHERE created_at > NOW() - INTERVAL '24 hours'
  AND raw_model IN ('gpt-4o-mini', 'claude-haiku-3.5', 'gemini-flash-2.0');
