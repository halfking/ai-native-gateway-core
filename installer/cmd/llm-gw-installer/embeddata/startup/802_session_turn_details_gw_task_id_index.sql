-- 802: gw_task_id 索引（session_turn_details 族）
--
-- 背景：会话存储解耦 v3 的 S4 停写门控（storage.request_logs_write_enabled）
-- 关停后，跨租户访问门 assertTaskInTenant（admin/session_tenant.go）不能再只查
-- request_logs 族 —— 那是权限判定而非数据查询，任一来源查不到行都会让语义从
-- 「阻断越权」翻转成「阻断所有人」，全体租户管理员的会话详情整体 404。
--
-- 读端已改为 session 族与 v1 族并联，但 session 族侧当时只能落两条腿：
--   - session_summaries：带部分索引 idx_session_summaries_task，可走索引；
--   - session_turn_details_hot：表小（约 1.1k 行），顺序扫描可接受。
-- 缺的第三条腿是 session_turn_details 月分区母表（约 167 万行）：733 只为该族
-- 建了 request / session / ts 三条索引，没有 gw_task_id，EXISTS 判定会退化成
-- 全表顺序扫描 —— 一个放在请求路径上的权限门不能承担这个代价。
--
-- 本迁移只补索引，不改任何数据、不改 promote 函数、不重写分区。
-- 形态对齐 525/526 已有的 (tenant_id, <col>) WHERE <col> IS NOT NULL 部分索引，
-- 也与 session_summaries.idx_session_summaries_task 保持一致，便于三腿走同一形态。
--
-- 锁：CREATE INDEX（非 CONCURRENTLY）会持有 ACCESS EXCLUSIVE 锁直到构建完成。
-- installer's applySQL 以 psql --single-transaction 执行，无法使用 CONCURRENTLY
-- （CONCURRENTLY 不能在事务块内运行）。本机 167 万行规模下单次构建在秒级，
-- 与 525 同量级；若将来母表显著变大，应改为运维侧在线建索引 + 本迁移只做
-- IF NOT EXISTS 存在性断言。
--
-- 依赖：733（建 session_turn_details 及其分区）、801（promote 函数改版，
-- 与本迁移无耦合，但同属 details 族，排在 801 之后以免未来合并时错位）。

CREATE INDEX IF NOT EXISTS idx_session_turn_details_tenant_gw_task_id
    ON public.session_turn_details (tenant_id, gw_task_id)
    WHERE gw_task_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_session_turn_details_hot_tenant_gw_task_id
    ON public.session_turn_details_hot (tenant_id, gw_task_id)
    WHERE gw_task_id IS NOT NULL;

COMMENT ON INDEX public.idx_session_turn_details_tenant_gw_task_id IS
    '802: 跨租户访问门 assertTaskInTenant 的 session 族母表腿。'
    'S4 停写后 request_logs 不可依赖，缺此索引会让 EXISTS 判定退化为 167 万行顺序扫描。';

COMMENT ON INDEX public.idx_session_turn_details_hot_tenant_gw_task_id IS
    '802: 与母表同形态，供未 promote 的近期行走索引（对齐 525/526 的 hot 腿惯例）。';
