-- 808 down: 删除 request_logs 的 DEFAULT 分区。
--
-- ⚠ 删除 DEFAULT 分区会把它承载的所有行一并删除，且会使母表重新回到
--   「ts 落在已建月分区之外 ⇒ INSERT 直接 23514」的状态。全新安装上
--   808 之前正是这个状态（Round 44 收口轮实测）。
--
-- 一般不应回退 808：DEFAULT 分区是 705 的 repair 骨架与
-- ensure_request_logs_partition / promote_request_logs_default_batch /
-- archive_request_logs_default 四个函数共同依赖的承重对象。
--
-- 仅在「明确要把 request_logs 收窄成纯月分区、且已确认无窗口期写入」
-- 时才执行，且必须先把 DEFAULT 里的行按 ts 迁到对应月分区：
--
--   INSERT INTO request_logs SELECT * FROM request_logs_default;
--   -- 逐月 ensure 后再删：
--   DROP TABLE IF EXISTS public.request_logs_default;
--
BEGIN;
DROP TABLE IF EXISTS public.request_logs_default;
COMMIT;
