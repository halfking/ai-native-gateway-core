-- recon-stats-verify-211.sql —— 对账模块统计口径的可复现断言集（2026-11-17 审计轮）。
--
-- 用法（只读，凭据不落盘）：
--   bash scripts/audit/q-211-readonly-sql.sh -f scripts/audit/recon-stats-verify-211.sql
--
-- 主档：docs/audit/2026-11-17-对账模块统计口径复审-主档.md
-- ★ 报告里的每个数字都对应下面某一条 ⇒ 复核对不上时，**先怀疑自己的口径**，
--   再怀疑数据变了（本轮复核轮 6 处订正里 5 处是初版自己的口径/抄写问题）。
--
-- 区间口径：2026-11-10 ~ 2026-11-15（真库当前唯一有流量的一段，UTC 日窗）。
-- ★ 日窗是 **UTC 日**（快照按 UTC 日聚合；usage_facts 的物理分区是上海日历日，
--   两者差 8 小时——见 reportrollup.RollupDay 的 R69 注记）。
-- ★ 第 1 段输出里那一行「d 为空、truth_req 有值」（实测 21,235）是 **当天还没跑快照**：
--   日聚合 worker 每日 02:00 UTC 跑（settings 键 reports.daily_rollup.hour），
--   跑之前当天只有 usage_facts 原始行 ⇒ FULL JOIN 出现「只有真相源」的一行。
--   **不是丢数**，也不是快照漏跑——隔一天再跑同一条 SQL 那行就消失了。

\echo '=== 1. 快照 vs 真相源：daily_grain 逐日 vs usage_facts（同 UTC 日窗）==='
-- 期望：req / succ / 四类 token 全部 d_* = 0；cost 列允许有分位差（见 §5 F4）。
WITH snap AS (
  SELECT report_date::date d,
         SUM(request_count) req, SUM(success_count) succ,
         SUM(input_tokens) itok, SUM(output_tokens) otok,
         SUM(cache_read_tokens) crt, SUM(cache_write_tokens) cwt,
         SUM(estimated_cost_cents) cost
    FROM report_snapshots WHERE scope='daily_grain' GROUP BY 1
), truth AS (
  SELECT (occurred_at AT TIME ZONE 'UTC')::date d,
         COUNT(*) req, COUNT(*) FILTER (WHERE status='success') succ,
         COALESCE(SUM(prompt_tokens),0) itok, COALESCE(SUM(completion_tokens),0) otok,
         COALESCE(SUM(cache_read_tokens),0) crt, COALESCE(SUM(cache_write_tokens),0) cwt,
         COALESCE(ROUND(SUM(cost_amount)*100),0) cost
    FROM usage_facts GROUP BY 1
)
SELECT s.d, s.req AS snap_req, t.req AS truth_req, (s.req-t.req) AS d_req,
       (s.succ-t.succ) AS d_succ, (s.itok-t.itok) AS d_itok, (s.otok-t.otok) AS d_otok,
       (s.crt-t.crt) AS d_crt, (s.cwt-t.cwt) AS d_cwt, (s.cost-t.cost) AS d_cost
  FROM snap s FULL JOIN truth t USING (d) ORDER BY d;

\echo '=== 2. 读面不变式：各维度分组求和 == 总计（daily_grain）==='
-- 期望：所有维度的 req 都等于总计（123,367）。
WITH f AS (
  SELECT * FROM report_snapshots
   WHERE scope='daily_grain' AND report_date BETWEEN '2026-11-10' AND '2026-11-15'
), agg AS (
  SELECT '总计' AS dim, request_count, success_count, estimated_cost_cents FROM f
  UNION ALL SELECT '按供应商', SUM(request_count), SUM(success_count), SUM(estimated_cost_cents) FROM f GROUP BY provider_id
  UNION ALL SELECT '按模型',   SUM(request_count), SUM(success_count), SUM(estimated_cost_cents) FROM f GROUP BY raw_model_name
  UNION ALL SELECT '按凭据',   SUM(request_count), SUM(success_count), SUM(estimated_cost_cents) FROM f GROUP BY credential_id
  UNION ALL SELECT '按租户',   SUM(request_count), SUM(success_count), SUM(estimated_cost_cents) FROM f GROUP BY tenant_id
  UNION ALL SELECT '按用户',   SUM(request_count), SUM(success_count), SUM(estimated_cost_cents) FROM f GROUP BY tenant_id, person
  UNION ALL SELECT '按apikey', SUM(request_count), SUM(success_count), SUM(estimated_cost_cents) FROM f GROUP BY api_key_id
  UNION ALL SELECT '按天',     SUM(request_count), SUM(success_count), SUM(estimated_cost_cents) FROM f GROUP BY report_date
)
SELECT dim, SUM(request_count) req, SUM(success_count) succ, SUM(estimated_cost_cents) cost
  FROM agg GROUP BY dim ORDER BY dim;

\echo '=== 3. 错误分布合计 == 失败数（gap 必须 0）==='
WITH f AS (SELECT * FROM report_snapshots WHERE scope='daily_grain'
            AND report_date BETWEEN '2026-11-10' AND '2026-11-15')
SELECT SUM(request_count-success_count) AS err_count,
       SUM((SELECT COALESCE(SUM((e.value)::numeric),0) FROM jsonb_each_text(error_kind_breakdown) e)) AS breakdown_sum,
       SUM(request_count-success_count)
         - SUM((SELECT COALESCE(SUM((e.value)::numeric),0) FROM jsonb_each_text(error_kind_breakdown) e)) AS gap
  FROM f;

\echo '=== 4. 未落定桶（NULL 行）的分布 —— F1 的量级来源 ==='
-- 期望：provider/credential 的 NULL 行是**同一批**（探针/限流/超时这类没走到
--       供应商的失败流量）；api_key 的 NULL 桶很小。
SELECT COUNT(*) FILTER (WHERE provider_id   IS NULL AND credential_id IS NULL) AS both_null,
       COUNT(*) FILTER (WHERE provider_id   IS NULL AND credential_id IS NOT NULL) AS only_provider_null,
       COUNT(*) FILTER (WHERE provider_id   IS NOT NULL AND credential_id IS NULL) AS only_cred_null,
       SUM(request_count) FILTER (WHERE provider_id IS NULL)     AS provider_null_reqs,
       SUM(request_count) FILTER (WHERE credential_id IS NULL)   AS credential_null_reqs,
       SUM(request_count) FILTER (WHERE api_key_id IS NULL)     AS apikey_null_reqs
  FROM report_snapshots WHERE scope='daily_grain';

\echo '=== 5. 成本取整口径差（F4）：grain（按组取整后求和）vs daily_total（整日取整一次）==='
-- 期望：daily_grain 的和比 daily_total 的和略小（每组各自取整的尾差累积）。
SELECT g.report_date::date d, g.grain_cost, t.total_cost, g.grain_cost - t.total_cost AS diff_cents
  FROM (SELECT report_date, SUM(estimated_cost_cents) grain_cost
          FROM report_snapshots WHERE scope='daily_grain' GROUP BY 1) g
  JOIN (SELECT report_date, SUM(estimated_cost_cents) total_cost
          FROM report_snapshots WHERE scope='daily_total' GROUP BY 1) t ON t.report_date = g.report_date
 ORDER BY 1;

\echo '=== 6. 旧口径 scope 的维度列是不是全 NULL（F2 的根因）==='
-- 期望：daily_total / internal_tenant 的 provider/credential/api_key 列**全 NULL**
--       ⇒ 它们的 NULL 含义是「这个 scope 不分解该维度」，不是「这批请求没落到该维度」。
--       把它们当成「未落定」⇒ 旧口径那天的整天总量被算进未落定桶（实测 277 vs 200）。
SELECT scope,
       COUNT(*) rows,
       COUNT(*) FILTER (WHERE provider_id   IS NULL) pid_null,
       COUNT(*) FILTER (WHERE credential_id IS NULL) cred_null,
       COUNT(*) FILTER (WHERE api_key_id    IS NULL) key_null,
       COUNT(*) FILTER (WHERE tenant_id     IS NOT NULL) tenant_set,
       COUNT(*) FILTER (WHERE raw_model_name <> '') model_set
  FROM report_snapshots
 WHERE scope IN ('daily_total','internal_tenant','daily_grain')
 GROUP BY scope ORDER BY scope;

\echo '=== 7. 旧口径日期的流量（为什么 F2 在线上隐形、在隔离 PG 真库腿暴露）==='
SELECT report_date::date d, scope, SUM(request_count) req
  FROM report_snapshots
 WHERE scope IN ('daily_total','daily_grain')
   AND report_date < '2026-11-10'
 GROUP BY 1,2 ORDER BY 1,2;

\echo '=== 8. 候选面不变式（F1 修好后）：Σ候选 == 读面总计（daily_grain 同一区间）==='
-- ★ 复刻 domains/reportrollup/dimensions.go 的**候选源语义**（不是另写一套口径）：
--   · grain 支 legacy_row = FALSE；旧口径支（daily_total）legacy_row = TRUE；
--   · 旧口径支**只在旧 scope 真的分解该维度时并入**（daily_total 不分解任何维度
--     ⇒ 三个 id 维度的旧口径支被 `legacy_row = FALSE` 排除 ⇒ F2 的守护生效）；
--   · 三个 id 维度的键 = COALESCE(col::text,'-1') ⇒ NULL 行也出「未落定」桶（F1）。
-- 期望（211 实测）：三个 id 维度的 Σ候选 **都等于** 读面总计 123,367（gap = 0）；
--       未落定桶：provider 38,035 / credential 38,035 / api_key 16。
-- ★ 修前（F1 未修）：凭据 Σ候选 85,332（少 38,035）、apikey 123,351（少 16）
--   ⇒ 「Σ候选 ≠ 总计」，候选面与读面两套数。
WITH src AS (
  SELECT provider_id, credential_id, api_key_id, request_count, FALSE AS legacy_row
    FROM report_snapshots
   WHERE scope = 'daily_grain' AND report_date BETWEEN '2026-11-10' AND '2026-11-15'
  UNION ALL
  SELECT provider_id, credential_id, api_key_id, request_count, TRUE AS legacy_row
    FROM report_snapshots
   WHERE scope = 'daily_total' AND report_date BETWEEN '2026-11-10' AND '2026-11-15'
     AND report_date NOT IN (SELECT report_date FROM report_snapshots WHERE scope='daily_grain')
), cand AS (
  SELECT 'provider'::text AS dim, COALESCE(provider_id::text,'-1') AS k,
         SUM(request_count)::bigint AS requests
    FROM src WHERE legacy_row = FALSE GROUP BY 2
  UNION ALL
  SELECT 'credential', COALESCE(credential_id::text,'-1'), SUM(request_count)::bigint
    FROM src WHERE legacy_row = FALSE GROUP BY 2
  UNION ALL
  SELECT 'api_key', COALESCE(api_key_id::text,'-1'), SUM(request_count)::bigint
    FROM src WHERE legacy_row = FALSE GROUP BY 2
), read_total AS (
  SELECT SUM(request_count)::bigint AS req FROM report_snapshots
   WHERE scope='daily_grain' AND report_date BETWEEN '2026-11-10' AND '2026-11-15'
)
SELECT c.dim,
       COUNT(*) AS groups,
       SUM(c.requests) AS sum_candidate,
       (SELECT req FROM read_total) AS read_total,
       SUM(c.requests) - (SELECT req FROM read_total) AS gap,
       SUM(c.requests) FILTER (WHERE c.k = '-1') AS unassigned_bucket
  FROM cand c GROUP BY c.dim ORDER BY c.dim;

\echo '=== 9. 「看得见 ⇒ 筛得出」对拍：候选的未落定桶 == 读面按 IS NULL 筛出的量（F1 的核心不变式）==='
-- ★ 两边用**不同写法**算同一个数（候选＝COALESCE 后分组求和；读面＝IS NULL 直接过滤），
--   数字必须相等 —— 这正是修前断掉的那根弦（线上实测 credential_id=-1 → 0 行、
--   api_key_id=-1 → 0 行，而候选面当时根本没有这两个桶）。
WITH cand_unassigned AS (
  SELECT 'provider'::text AS dim,
         SUM(request_count)::bigint AS req
    FROM report_snapshots
   WHERE scope='daily_grain' AND report_date BETWEEN '2026-11-10' AND '2026-11-15'
     AND provider_id IS NULL
  UNION ALL
  SELECT 'credential', SUM(request_count)::bigint
    FROM report_snapshots
   WHERE scope='daily_grain' AND report_date BETWEEN '2026-11-10' AND '2026-11-15'
     AND credential_id IS NULL
  UNION ALL
  SELECT 'api_key', SUM(request_count)::bigint
    FROM report_snapshots
   WHERE scope='daily_grain' AND report_date BETWEEN '2026-11-10' AND '2026-11-15'
     AND api_key_id IS NULL
), candidate_side AS (
  -- 候选面口径：NULL 桶用哨兵 -1 出键，再按键求和
  SELECT 'provider'::text AS dim, COALESCE(provider_id::text,'-1') AS k,
         SUM(request_count)::bigint AS requests
    FROM report_snapshots
   WHERE scope='daily_grain' AND report_date BETWEEN '2026-11-10' AND '2026-11-15'
   GROUP BY 2
  UNION ALL
  SELECT 'credential', COALESCE(credential_id::text,'-1'), SUM(request_count)::bigint
    FROM report_snapshots
   WHERE scope='daily_grain' AND report_date BETWEEN '2026-11-10' AND '2026-11-15'
   GROUP BY 2
  UNION ALL
  SELECT 'api_key', COALESCE(api_key_id::text,'-1'), SUM(request_count)::bigint
    FROM report_snapshots
   WHERE scope='daily_grain' AND report_date BETWEEN '2026-11-10' AND '2026-11-15'
   GROUP BY 2
)
SELECT cu.dim,
       cu.req AS read_face_isnull,
       COALESCE(SUM(cs.requests) FILTER (WHERE cs.k = '-1'), 0) AS candidate_bucket,
       COALESCE(SUM(cs.requests) FILTER (WHERE cs.k = '-1'), 0) - cu.req AS gap
  FROM cand_unassigned cu
  JOIN candidate_side cs ON cs.dim = cu.dim
 GROUP BY cu.dim, cu.req ORDER BY cu.dim;

