-- ============================================================
-- 迁移 1006: drop the orphaned v1-family promote function resurrected by 528
-- 目的：528（2026-11-28 00:29 台账漂移重放）在 890 之后 CREATE OR REPLACE 了
--       promote_request_logs_bodies_hot_to_partition —— 其函数体引用的
--       request_logs_bodies / request_logs_bodies_hot 已被 890 物理删除 ⇒
--       函数成为孤儿。data-lifecycle hot cron 的能力探针（按 pg_proc 存在性
--       判族缺席）见函数在 ⇒ 放行逐批调用 ⇒ 每批 SQLSTATE 42P01。
-- 实证（2026-11-28 211 真机）：s 槽旧镜像 crash-loop 期间每轮 restart 触发
--       hot cron，PG 日志 10 分钟 29 条 ERROR（全部 relation
--       "request_logs_bodies_hot" does not exist）；函数体 md5=
--       a762484acad3f8f6503842d5bb65dbba ＝ 528 体。
-- 语义：DROP FUNCTION IF EXISTS ⇒ 已清库幂等无操作；回滚（down）按 528 up
--       原体 CREATE OR REPLACE 还原（链路自洽，890.down 同理不依赖本件）。
--       request_logs_with_current_month canonical 视图（~40 个读方）不动。
-- 依赖：528（退役对象即 528 复活的函数）、890（v1 族真删）。
-- 日期：2026-11-28
-- 影响：public.promote_request_logs_bodies_hot_to_partition(interval,int) 移除
-- ============================================================

-- 锁守卫（startup_ddl_lock_guard_gate_test.go：>870 且含强锁 DDL ⇒ 必须自带
--   lock_timeout）。DROP FUNCTION 属元数据级操作；孤儿函数无依赖对象
--   （890 已先删它的表）⇒ 实测空转 <0.05s 量级，无 914 型「拍小了中止部署」风险。
SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

DROP FUNCTION IF EXISTS public.promote_request_logs_bodies_hot_to_partition(interval, int);

-- 台账自登记（≥952 双账本纪律：492/633/890/983k/1004 同款守卫式）
INSERT INTO public.schema_migrations (version, description)
VALUES ('1006', 'drop the orphaned v1-family promote function resurrected by 528 (body references tables dropped by 890; hot cron capability probe saw it present and every batch failed 42P01)')
ON CONFLICT (version) DO NOTHING;

COMMIT;
