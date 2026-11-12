-- 963 down：只退自己那一步（drop 本迁移建的预物化表）。
-- 读面的 CASE 回落分支（admin/credential_monitor.go）在表缺席时按
-- 「本表非空 EXISTS 判定失败」走旧两臂实时计数——与 963 之前行为一致；
-- bg.CredentialRollupRefresher 对缺席表的写入会报错并按设计降级为日志 WARN。
BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

DROP TABLE IF EXISTS public.credential_total_requests_rollup;

-- 记账：down 不删记账行（与 959/960/961/962 同款口径：down 是「可回退」不是「抹掉历史」）。

COMMIT;
