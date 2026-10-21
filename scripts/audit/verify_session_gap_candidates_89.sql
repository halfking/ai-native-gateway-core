-- ═══════════════════════════════════════════════════════════════════════════
-- verify_session_gap_candidates_89.sql
--
-- 用途（§143.48，2026-10-21）：定性 §143.47 留下的**唯一真缺口候选** ——
--       252 上 **89 行 / 27 个会话**：v1 `_request_logs` 有这行（成功、非探针），
--       v2 `session_turns` 的**分区表**里没有这个 request_id，
--       但**同一个 session_id 在 v2 里存在**（⇒ 不是「整个会话没进 v2」）。
--
-- ★★★ 最可能的解释（先查它，因为它最便宜且可证伪）：
--   v2 的写路径是 **hot → promote → 分区**（domains/session/v2 的 bodies/turn writer
--   + bg/partition_manager.go 的 promote）。⇒⇒ 若 promote 滞后或失败，
--   这 89 行应当**躺在 `session_turns_hot` 里** ⇒ ⇒ 那就不是「没写」，
--   而是「写了但没搬到分区」⇒ ⇒ **两者的处置完全不同**（前者是延迟，后者是丢失）。
--
-- ★★ 本脚本**只读**：全部语句为 SELECT，无 DDL/DML。
-- 用法：bash ~/.mavis-tools/lgwp-prod.sh <本文件> 900
-- ═══════════════════════════════════════════════════════════════════════════

SET statement_timeout = '240s';
SET lock_timeout = '5s';
SET work_mem = '64MB';
SET jit = off;

\echo '### P1. ★★ 决定性判据：这 89 行在 session_turns_hot 里吗（★ 「没写」vs「没搬」）'
WITH gap AS (
  SELECT l.request_id, l.gw_session_id, l.ts, l.success, l.application_id,
         l.client_model, l.error_kind, l.request_status
    FROM public._request_logs l
   WHERE l.success IS TRUE
     AND l.request_id NOT LIKE 'probe-%'
     AND l.gw_session_id IS NOT NULL AND l.gw_session_id <> ''
     AND NOT EXISTS (SELECT 1 FROM public.session_turns t WHERE t.request_id = l.request_id)
     AND EXISTS (SELECT 1 FROM public.session_turns t2 WHERE t2.session_id = l.gw_session_id)
)
SELECT count(*) AS gap_rows,
       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM public.session_turns_hot h WHERE h.request_id = g.request_id)) AS in_hot,
       count(*) FILTER (WHERE NOT EXISTS (SELECT 1 FROM public.session_turns_hot h WHERE h.request_id = g.request_id)) AS nowhere
  FROM gap g;

\echo '### P2. ★ 那 27 个会话在 v2 里的轮次全貌（★ 看时间跨度与 request_id 形态）'
WITH gap_sess AS (
  SELECT DISTINCT l.gw_session_id AS session_id
    FROM public._request_logs l
   WHERE l.success IS TRUE
     AND l.request_id NOT LIKE 'probe-%'
     AND l.gw_session_id IS NOT NULL AND l.gw_session_id <> ''
     AND NOT EXISTS (SELECT 1 FROM public.session_turns t WHERE t.request_id = l.request_id)
     AND EXISTS (SELECT 1 FROM public.session_turns t2 WHERE t2.session_id = l.gw_session_id)
)
SELECT t.session_id,
       count(*) AS turns,
       min(t.ts) AS first_ts,
       max(t.ts) AS last_ts,
       min(t.turn_no) AS min_turn_no,
       max(t.turn_no) AS max_turn_no,
       (SELECT count(*) FROM public.session_bodies b WHERE b.session_id = t.session_id) AS bodies_rows,
       (SELECT count(*) FROM public.session_turn_details d WHERE d.session_id = t.session_id) AS details_rows
  FROM public.session_turns t
  JOIN gap_sess g ON g.session_id = t.session_id
 GROUP BY t.session_id
 ORDER BY turns DESC, t.session_id
 LIMIT 30;

\echo '### P3. ★ 89 行的逐行画像（★ 与该会话在 v2 的时间跨度对拍）'
WITH gap AS (
  SELECT l.request_id, l.gw_session_id, l.ts, l.application_id, l.client_model,
         l.error_kind, l.request_status, l.latency_ms, l.total_tokens
    FROM public._request_logs l
   WHERE l.success IS TRUE
     AND l.request_id NOT LIKE 'probe-%'
     AND l.gw_session_id IS NOT NULL AND l.gw_session_id <> ''
     AND NOT EXISTS (SELECT 1 FROM public.session_turns t WHERE t.request_id = l.request_id)
     AND EXISTS (SELECT 1 FROM public.session_turns t2 WHERE t2.session_id = l.gw_session_id)
)
SELECT g.request_id, g.ts, g.application_id, g.client_model, g.error_kind, g.request_status,
       g.latency_ms, g.total_tokens,
       t.min_ts AS session_first_turn, t.max_ts AS session_last_turn, t.turns AS session_turns
  FROM gap g
  JOIN (SELECT session_id, min(ts) AS min_ts, max(ts) AS max_ts, count(*) AS turns
          FROM public.session_turns GROUP BY session_id) t
    ON t.session_id = g.gw_session_id
 ORDER BY g.ts
 LIMIT 30;

\echo '### P4. ★ session_turns_hot 的整体水位（★ promote 是否滞后）'
SELECT count(*) AS hot_rows,
       min(ts) AS min_ts,
       max(ts) AS max_ts,
       count(DISTINCT session_id) AS hot_sessions
  FROM public.session_turns_hot;
