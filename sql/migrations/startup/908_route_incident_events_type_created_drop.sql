-- ★ 取号漂移留痕（R147，2026-10-24）：本文件曾以 903 落地；rebase 时发现并行会话
--   已把 903 用于 903_sessions_updated_at_session_id_index.sql（且已上生产，水位=903）、
--   904 用于 904_inspector_stats_partial_indexes.sql ⇒ 按 LESSONS「下一个空号是会漂移的
--   活事实」纪律让号为 905；★ ★ 2026-10-25 第二次漂移：并行会话在上游于
--   10-24 19:49 / 21:44 / 23:04 又取走 905/906/907 ⇒ 本条再次让号为 **908**
--   （上游当前最大号 907 之后的第一个空号）。★ 判决内容零变化（只改文件名与登记号）。
--
-- dbinit:no-transaction —— 本文件含 DROP INDEX CONCURRENTLY，
-- PostgreSQL 不允许在事务块内执行它。installer 的 applySQL 据此对本文件走
-- 非事务通道（见 installer/internal/dbinit/runner.go noTransactionMarker）。
-- 若漏掉该标记，全新安装会失败于
--   "DROP INDEX CONCURRENTLY cannot run inside a transaction block"。
--
-- 908_route_incident_events_type_created_drop.sql
-- 2026-10-24：删除 route_incident_events 的零消费方索引
--   idx_route_incident_events_type_created (event_type, created_at DESC)。
--
-- 判决依据（252 只读 + 代码面核对，docs/audit/2026-10-24-R147-*.md §2）：
--   · 252 读数（2026-10-24）：110 MB，idx_scan = 1（窗口起点 10-18 22:03
--     stats reset，5.2 天）；表 heap 665 MB，1,897,740 行。
--   · 全仓 Go 代码**没有任何查询**以 event_type 为谓词读 route_incident_events
--     （requestjourney 的 event_type 是另一张表 request_state_transitions）。
--   · 它历史上的唯一使用者是 30 天留存 DELETE —— 迁移 871 的头注实测该计划
--     是「条件建在非首列 ⇒ 全索引扫描再过滤」的坏计划，并为此新建了
--     created_at 首列索引取代它（871 之后 DELETE 走 idx_..._created_at）。
--   · 写路径热度佐证：同表 uq_route_incident_events_idem 5.2 天 422,297 次
--     扫描（≈7.8 万次/天的 ON CONFLICT 冲突检查）⇒ 每次写入都要白维护
--     一个零读者的 110 MB btree。
--   · 不背书任何约束（pg_constraint.conindid = 0 行，252 实查）。
--
-- 配套（同批落仓，缺一不可）：
--   · db/db.go ensureRouteIncidentSchema 同步删除同名 CREATE INDEX ——
--     否则产品启动的 ensure 链会把本迁移刚删的索引原地复活
--     （718 的三项核实里「无 Go ensure 创建者」在原表不成立，故必须先拆）。
--   · baseline（sql/schema/01-schema.sql 与 deploy 副本）**刻意保留**该索引：
--     沿 718 先例，fresh-install 链是「baseline 建出 → 启动迁移 905 删掉」，
--     baseline 是时点快照，不做回收站。
--
-- 为什么 CONCURRENTLY：表在网关写路径上（failure_observed 持续写入），
--   普通 DROP 的锁形态会阻塞写入（871 同理）。
-- 为什么 SET 语句用会话级而不是 SET LOCAL：非事务通道没有事务块，
--   SET LOCAL 会告警且不生效（871 头注同款坑），结束后 RESET。
--
-- 幂等：DROP ... IF EXISTS 自然幂等；索引不存在时本文件是空操作。

SET lock_timeout = '60s';
SET statement_timeout = '10min';

DROP INDEX CONCURRENTLY IF EXISTS public.idx_route_incident_events_type_created;

RESET statement_timeout;
RESET lock_timeout;
