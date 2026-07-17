-- 421_task_default_routing.down.sql
-- 回滚 421：删除 task_default_routing 及审计表。
-- 安全：本表只影响 auto 路由（model=auto），不影响显式 model 请求。
-- 回滚后 Decider 回退到隐式 tag 评分（RoutingSource=implicit_tag）。

BEGIN;

DROP TABLE IF EXISTS public.task_default_routing_audit;
DROP TABLE IF EXISTS public.task_default_routing;

COMMIT;
