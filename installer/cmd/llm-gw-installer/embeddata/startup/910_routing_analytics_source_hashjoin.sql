-- Migration 910: `routing_analytics_source` 父表腿从 LATERAL 逐行回定位
-- 换成「普通 LEFT JOIN + partition_date 日界下推」⇒ details 侧可被规划成
-- Hash Join，消掉 907 形态残留的 ~3 万次/刷新 逐行探针。
--
-- ═══════════════════════════════════════════════════════════════════════
-- 为什么需要它（★ 全部为 2026-10-27 生产 252 只读实测，结论可复算）
-- ═══════════════════════════════════════════════════════════════════════
--
-- 【背景：907 已消掉 45 万行顺序扫，但 REFRESH 仍是刷新链最大头】
--   pgss（10-18 22:03 reset 起）：907 应用（10-24 23:07+08）前
--   routing_analytics_7d REFRESH = 293 次 / 61.0s 均值 / 4.97h；
--   907 后增量 = 158 次 / 均值 40.6s（61.0 → 40.6，1.5x）。
--   ⇒ 907 方向正确（R95 的 45 万行顺序扫确实消失），但 REFRESH 仍是
--   REFRESH 族单 stmt 最大 DB 时间（对比 matrix 三兄弟均值 ~60s/次但
--   每天只有 ~30 次；本视图 ~48 次/天）。
--
-- 【★ 907 残留的残留：逐行探针对负载敏感】
--   EXPLAIN ANALYZE BUFFERS（252，2026-10-27，TIMING ON）：
--     · 父表腿 LATERAL 探针 Limit/Append 节点 actual time=0.366ms x
--       27,486 loops ≈ **10.1s**（57%）；hot 腿 0.249ms x 3,208 ≈ 0.8s。
--     · 同一形态 R96 轮实测探针只要 ~3us/次（10-24，负载低）⇒
--       探针单次成本随宿主负载漂 100x+ ⇒ 907 形态把 REFRESH 的墙钟
--       **乘法式**暴露在宿主负载上（252 load 23/4 核）。
--   结构读数（TIMING OFF，多轮稳定）：整计划 **~147,000 buffers**，
--   其中父表腿 133,827（探针 110,104 = 27,526 次 x 4 页 btree 下降）。
--
-- 【★ 修法：父表腿普通 LEFT JOIN + 日界下推】
--   `LEFT JOIN session_turn_details dd ON dd.tenant_id = t.tenant_id
--      AND dd.request_id = t.request_id AND dd.partition_date = t.partition_date
--      AND dd.partition_date >= (now() - interval '8 days')::date`
--   ⇒ 日界谓词让 details 的分区裁剪在执行期只留窗口内分区
--     （实测 details_2026_09 虽 0 行但堆 383MB —— 没有日界下推就会被全扫）；
--   ⇒ details 侧从「27,526 次 x 4 页随机下降」变成「一次 pkey 范围扫」，
--     规划器可选 Hash Right Join（实测 Batches: 1，内存 3.8MB）。
--   ⇒ hot 腿**原样保留 LATERAL (... LIMIT 1)**：details_hot 的
--     idx_session_turn_details_hot_request 是**非唯一** request 索引，
--     907 的唯一键等价性证明只覆盖分区父表（catalog 实测
--     session_turn_details_request_id_key = UNIQUE (tenant_id, request_id,
--     partition_date)），不覆盖 hot 表 ⇒ LIMIT 1 在 hot 腿仍是必需的。
--
-- 【★ 口径等价（252 生产实测，同一事务同一快照）—— 承诺范围 = 带 ts 窗的读】
--   · 普通_LEFT_JOIN ≡ LATERAL(... LIMIT 1)：UNIQUE (tenant_id, request_id,
--     partition_date) 建在分区父表上并按分区传播 ⇒ 同键最多 1 行
--     （窗口 66,838 行实测：join 后行数 66,838 = 窗口行数，无行增殖）。
--   · 日界下推 ≡ 无日界：t.ts >= now()-7d 且 dd.partition_date =
--     t.partition_date ⇒ dd.partition_date >= now()-8d 是被蕴含的
--     （8 天留跨日边界余量；252 server TZ = Asia/Shanghai，1 天余量可吸收
--     ≤±14h 的时区口径偏移）；错位行实测 = 0
--     （count(*) FROM session_turns WHERE ts >= now()-7d
--       AND partition_date < (now()-8d)::date  = 0）。
--   · 窗口内父表腿双向 EXCEPT ALL（同一事务同一快照，66,838 行）：
--     only_current=**0** / only_candidate=**0**。
--   ★★ 等价范围边界（批判式审计轮 2026-10-27 补钉）：**全源等价不再成立** ——
--     对 ts 早于 8 天的旧行，本形态返回行的 details 三列为 NULL
--     （907/canonical 形态返回真实值）。这是**设计内边界**：本视图的等价
--     承诺只覆盖「带 ts 窗的读」；全仓 13 处消费方逐一实测
--     （matview ×2 / bg/mv_consistency ×2 / admin/analytics ×6 /
--     admin/auto_route ×4）**全部带 7d 窗或为 request_id 存在性探针**
--     （LEFT JOIN 保行存在性 ⇒ 探针不受影响）⇒ 今日零受影响消费方；
--     夹具级边界由 TestRoutingAnalyticsSourceDayRangeBoundaryOnRealDB
--     从两侧钉住。未来新增无窗消费方时必须重审本边界。
--
-- 【★ 实测收益（252，交错 A/B + 负载态对照）】
--   结构（多轮稳定）：整计划 buffers 146,423~147,079 → **51,425~52,664**（-64%）；
--   父表腿 133,827 → 38,101+1,239read。
--   墙钟（§143.28 口径：只作时点，不承诺秒数）：暖态低负载 8,531/7,354ms →
--   7,183/6,064ms；高负载态 27,397ms → 4,970ms（探针单次成本被负载放大时
--   收益最大）。收益随宿主负载浮动，**buffers -64% 是可承诺的部分**。
--
-- 【★ matview 不需要重建】
--   CREATE OR REPLACE VIEW 保留 OID ⇒ 两个 matview 依赖不动 ⇒ 下一次
--   REFRESH 自动采用新定义。db.go 的 stale-definition gate 特征串认
--   'session_turn_details' 关系名（新体仍直引它）⇒ 两形都算新，无启动期重建。
--
-- 【幂等】CREATE OR REPLACE VIEW 本身幂等。
-- 【锁与时长】与 907 同量级（252 实测 CREATE OR REPLACE VIEW ≈245ms；
--   SET LOCAL lock_timeout=60s / statement_timeout=2min，903 系同款）。
-- 【Down】把视图体换回 907 的 LATERAL 形态（applied 文件内容冻结，不回改 907）。
-- ⚠️ 注释里不许出现反引号：本段 SQL 在 Go raw string 里，反引号会提前终止字符串。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '2min';

		-- Keep analytics isolated from the frozen request-log wrapper view. The
		-- narrow source has stable types across hot and parent partitions and
		-- explicitly exposes origin_stage for probe filtering.
		-- CREATE OR REPLACE (not DROP+CREATE): the routing matviews depend on
		-- this view, so a plain DROP fails with SQLSTATE 2BP01 whenever this
		-- batch runs on the index-repair path (views current, ukey missing).
		-- 2026-10-13 S3 波 2（停写切换）：源从 _request_logs_hot UNION ALL
		-- _request_logs 换成 canonical 视图 request_logs_with_current_month。
		-- 2026-10-24 R96 轮（迁移 907）：源换成会话族两腿 + LATERAL 回定位。
		-- 2026-10-27 本轮（迁移 910）：父表腿 LATERAL 改普通 LEFT JOIN +
		-- partition_date 日界下推（details 侧分区裁剪 + Hash Join 可选），
		-- 消掉 ~3 万次/刷新 的逐行探针；hot 腿保留 LATERAL（details_hot
		-- 无唯一键证明）。日界下推被 t.ts 窗口蕴含（错位行实测 0）。
		-- 15 列全部在 118 列契约内、列名/类型/次序与旧体逐位一致（CREATE
		-- OR REPLACE 的列契约；auto_profile 仍居末位）。
		-- ⚠️ 注释里不许出现反引号：本段 SQL 在 Go raw string 里，反引号会提前终止字符串。
		CREATE OR REPLACE VIEW routing_analytics_source AS
		SELECT
		  t.ts,
		  t.task_type::text AS task_type,
		  t.model::text AS outbound_model,
		  d.client_model::text AS client_model,
		  t.work_type::text AS work_type,
		  d.provider_id::bigint AS provider_id,
		  (CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint ELSE NULL::bigint END)::bigint AS credential_id,
		  t.is_auto_request::boolean AS is_auto_request,
		  t.tenant_id::text AS tenant_id,
		  t.request_id::text AS request_id,
		  t.success::boolean AS success,
		  t.latency_ms::numeric AS latency_ms,
		  t.cost_usd::numeric AS cost_usd,
		  t.origin_stage::text AS origin_stage,
		  d.auto_profile::text AS auto_profile
		FROM public.session_turns_hot t
		LEFT JOIN LATERAL (
		  SELECT dd.client_model, dd.provider_id, dd.auto_profile
		  FROM public.session_turn_details_hot dd
		  WHERE dd.tenant_id = t.tenant_id
		    AND dd.request_id = t.request_id
		    AND dd.partition_date = t.partition_date
		  LIMIT 1
		) d ON TRUE
		UNION ALL
		SELECT
		  t.ts,
		  t.task_type::text AS task_type,
		  t.model::text AS outbound_model,
		  dd.client_model::text AS client_model,
		  t.work_type::text AS work_type,
		  dd.provider_id::bigint AS provider_id,
		  (CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint ELSE NULL::bigint END)::bigint AS credential_id,
		  t.is_auto_request::boolean AS is_auto_request,
		  t.tenant_id::text AS tenant_id,
		  t.request_id::text AS request_id,
		  t.success::boolean AS success,
		  t.latency_ms::numeric AS latency_ms,
		  t.cost_usd::numeric AS cost_usd,
		  t.origin_stage::text AS origin_stage,
		  dd.auto_profile::text AS auto_profile
		FROM public.session_turns t
		LEFT JOIN public.session_turn_details dd
		  ON dd.tenant_id = t.tenant_id
		 AND dd.request_id = t.request_id
		 AND dd.partition_date = t.partition_date
		 AND dd.partition_date >= (now() - interval '8 days')::date;

COMMIT;

-- POST_CONDITION:
--   SELECT pg_get_viewdef('public.routing_analytics_source'::regclass, true);
--   -- 预期：FROM public.session_turns_hot t LEFT JOIN LATERAL (...)
--   --       UNION ALL FROM public.session_turns t
--   --       LEFT JOIN public.session_turn_details dd ON ...（普通 JOIN，非 LATERAL）
--   --       且父表腿 ON 里有 dd.partition_date >= (now() - '8 days')::date
--   -- 列契约（CREATE OR REPLACE 只能加列，不能改名列/次序/类型）：
--   SELECT attname, format_type(atttypid, atttypmod)
--     FROM pg_attribute
--    WHERE attrelid = 'public.routing_analytics_source'::regclass
--      AND attnum > 0 AND NOT attisdropped
--    ORDER BY attnum;
--   -- 预期 15 列，auto_profile 仍在末位。
