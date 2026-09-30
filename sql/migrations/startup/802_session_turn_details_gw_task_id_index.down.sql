-- 802 down: 移除 gw_task_id 索引。
--
-- 只删索引，不动数据。删掉后 assertTaskInTenant 的母表腿会退化为顺序扫描
-- （约 167 万行），因此 down 之后若仍处于 S4 停写态，跨租户访问门会变慢
-- 但不会给出错误答案 —— 腿还在，只是没索引。

DROP INDEX IF EXISTS public.idx_session_turn_details_hot_tenant_gw_task_id;

DROP INDEX IF EXISTS public.idx_session_turn_details_tenant_gw_task_id;
