-- ===========================================================================
-- File:          sql/migrations/startup/819_request_abandoned.sql
-- Migration:     819
-- Database:      llm_gateway
-- Purpose:       给「请求开始了却从没有终态」这件事建一个**独立落点**。

-- ---------------------------------------------------------------------------
-- ⚠ 历史档案（2026-10-04 恢复注记，审计 §R43/L8）：
--   本文件描述的 Go 写路径（markRequestAbandonedPending /
--   clearRequestAbandonedPending）已不存在——独立表方案被 820/821 线取代
--   （821 改为在 session_turns 上打 is_abandoned 标记，2026-10-03 用户拍板），生产 *.go
--   对本表零引用。文件按 docs/db-changelog.md 台账 SHA 恢复，仅为
--   checksum 门完整性保留：勿据下文把 request_abandoned 当作活落点接线，
--   「写路径刻意不受 S4 停写门控」等段落均按当时方案书写，已不成立。
-- ---------------------------------------------------------------------------
--
-- 为什么要有 819（S4 停写前必须解决，审计 §9.66）：
--   「开始了却没结束」今天**只**存在于 v1 的 request_logs 里，形态是
--   一行 request_status='in_progress' 的**永久冻结**行——
--   全仓没有任何扫描器收敛它（bg/pending_sweeper 扫的是 Redis
--   pending_response:*，与 request_logs 无关）。它本身就是唯一落点。
--
--   **S4 落地后这个落点会连同事实一起消失**，因为：
--     ① v1 侧：client.go 的 insertRequestLog / updateRequestLog 在事务内
--        快照 logsWrite := requestLogsWriteEnabled()，其后整个 request_logs
--        宽表家族被 `if logsWrite {}` 跳过 —— **连 in_progress 占位 INSERT
--        都不会再产生**；
--     ② 会话族侧：sessionv2mirror/hook.go:71 按设计只镜像终态
--        （`if !entry.Success && !isTerminalFailure(entry) { return }`）。
--   ⇒ 请求在执行中进程死掉 / 客户端断连 / 某条路径不发终态事件时，
--     S4 之后它在**任何表里都不存在**。
--
-- 这不是零信息事件（252 生产实测 2026-10-03）：
--   遗弃率 0.047%（19 / 40,585，约每天 6–7 条）；
--   **19/19 带 prompt_tokens，合计 414,168**，9/19 带 completion_tokens
--   （合计 4,047），而 cost_usd **19/19 全空**。
--   ⇒ 丢掉的是**已经真实发生的上游消耗**，且速率永久复现。
--   ⇒ 排除「进程重启造成」：19 条里只有 1 条贴近停机边界；
--     排除「某次故障窗口」：唯一密集窗口（10-02 06:27，4 条/50 秒）
--     同窗另有 320 条正常完成，遗弃率 0.055% 与整体一致。
--   ⇒ **落点必须逐请求**，「启动时扫残留」只能覆盖 19 条里的 1 条。
--
-- 表的不变式（**这是本表的全部语义，改动前先读这一句**）：
--   **表里有行 ⇔ 该请求开始了，且没有终态记录落库。**
--   终态路径**删除**该行（不是置状态位）——于是稳态下这张表**就是**
--   abandoned 集合本身，不需要「哪些是已关闭的」这种额外状态列。
--   ⇒ 稳态行数 ≈ 遗弃率 × 流量 ≈ 每天个位数。
--
-- 写路径**刻意不受 S4 停写门控**：
--   markRequestAbandonedPending / clearRequestAbandonedPending 都挂在
--   insertRequestLog / updateRequestLog 的 `if logsWrite {}` **之外**、
--   各自既有的事务**之内**。同文件的 observeSystemFingerprint 已经把这个
--   「S4 停写的契约是宽表停写、计费照常」的边界写清楚了。
--   ⇒ 迁移顺序上本表必须先于 S4 开关生效；反过来（先关 v1 再建表）会
--     造成一段「请求开始了但哪都不记」的窗口。
--
-- 错误处理刻意 best-effort（不随主事务回滚）：
--   本仓同族的 H3 正文镜像与 session_dim 维度都是显式 fail-open。这里同样
--   fail-open，理由是**部署顺序风险**：若本表不存在就让整条 request_logs
--   写入失败，那么「819 尚未应用」会直接打挂**全部**请求日志。
--   失败走 slog.Warn + metrics，不阻断主写。
-- ===========================================================================
BEGIN;

CREATE TABLE IF NOT EXISTS public.request_abandoned (
    -- 主键即 request_id：终态路径按它 DELETE，重复 in_progress INSERT 走
    -- ON CONFLICT DO NOTHING（async retry 复用同一 request_id 的形态已存在，
    -- 见 request_logs_hot 的 ON CONFLICT 注释）。
    request_id        text PRIMARY KEY,
    tenant_id         text NOT NULL DEFAULT 'default',
    -- 留空是合法值：无会话头流量（探针/系统/匿名）不会有 gw_session_id。
    gw_session_id     text,
    started_at        timestamptz NOT NULL,
    -- 遗弃请求**确实消耗了上游 token**（§9.66 实测 19/19 带 prompt_tokens），
    -- 而 cost_usd 在 v1 侧全空 ⇒ 这里是这些消耗唯一的落点。
    prompt_tokens     bigint NOT NULL DEFAULT 0,
    completion_tokens bigint NOT NULL DEFAULT 0,
    client_model      text,
    outbound_model    text,
    -- 预留诊断位：今天这些行的 error_kind 在 v1 侧全为 NULL，写不上去。
    reason            text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now()
);

-- ===========================================================================
-- 迁移自身的守卫（**必须排在任何 COMMENT 之前**）：CREATE TABLE IF NOT EXISTS 在「表已存在但列集不对」时是
-- 静默成功的——那会让写方 INSERT 撞 "column does not exist"，而症状是
-- 每天几条 warn 日志，极难回溯到「是表结构漂了」。
--
-- **位置是被真跑出来的，不是排版偏好**：这个守卫最初写在文件末尾，
-- 而 `COMMENT ON COLUMN public.request_abandoned.reason` 在它之前。
-- 删掉 reason 列后重跑迁移，psql 在 COMMENT 那行就 ERROR 退出，
-- 守卫**根本没被执行到**——一道在自己该拦的场景里不可达的判据，
-- 和没有判据是一回事，而且更坏：它让人以为结构漂移被守着。
-- 同一道守卫在 down 里也必须重复（两个块之间没有共享状态）。
-- 同一道守卫在 down 里也必须重复（两个块之间没有共享状态）。
-- ===========================================================================
DO $$
DECLARE
  want text[] := ARRAY['request_id','tenant_id','gw_session_id','started_at',
                       'prompt_tokens','completion_tokens','client_model',
                       'outbound_model','reason','created_at'];
  have text[];
  missing text;
BEGIN
  IF to_regclass('public.request_abandoned') IS NULL THEN
    RAISE EXCEPTION 'migration 819: public.request_abandoned missing after CREATE';
  END IF;

  SELECT COALESCE(array_agg(a.attname ORDER BY a.attname), '{}')
    INTO have
    FROM pg_attribute a
   WHERE a.attrelid = 'public.request_abandoned'::regclass
     AND a.attnum > 0 AND NOT a.attisdropped;

  SELECT string_agg(w, ', ' ORDER BY w)
    INTO missing
    FROM unnest(want) AS w
   WHERE NOT (w = ANY (have));

  IF missing IS NOT NULL THEN
    RAISE EXCEPTION 'migration 819: request_abandoned exists but is missing column(s): %', missing;
  END IF;

  RAISE NOTICE 'migration 819: request_abandoned ready (% columns)', array_length(have, 1);
END $$;

COMMENT ON TABLE public.request_abandoned IS
  'S4 停写前为「请求开始了却从没有终态」建立的独立落点（审计 §9.66）。'
  '**不变式：表里有行 ⇔ 该请求开始了且无终态记录**。终态路径 DELETE 该行，'
  '因此稳态下本表即 abandoned 集合本身，无需状态列。'
  '写路径（telemetry client markRequestAbandonedPending / '
  'clearRequestAbandonedPending）刻意在 S4 停写门控之外，故本表活过 v1 退役。'
  '错误 fail-open：写失败只 slog.Warn + metrics，不回滚 request_logs 主事务'
  '（理由：819 未应用时不应打挂全部请求日志）。';

COMMENT ON COLUMN public.request_abandoned.reason IS
  '诊断位。v1 侧这些行的 error_kind 实测全为 NULL，写不上去；本列留给后续'
  '能识别出「为什么没终态」时填写。空串 = 未知，不是「无原因」。';

-- 读侧主查询是「这张表里有什么」，量极小（每天个位数），不需要额外索引；
-- 起点按 tenant 分组是运营侧最可能的第一个切面，故只建这一条。
CREATE INDEX IF NOT EXISTS idx_request_abandoned_tenant
    ON public.request_abandoned (tenant_id, started_at DESC);


COMMIT;
