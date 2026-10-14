-- 864_model_aliases_bare_raw_name_active_idx.sql
-- 2026-10-15：model_aliases 裸列 (raw_name) 前导部分索引。
--
-- 缺陷（2026-10-15 252 PG SQL 日志审计轮，24h 窗实测）：
--   model_aliases 的「active 别名 → canonical_id」查询在**全部生产读点**
--   都用裸列等值 `raw_name = lower(<外列>)`（S3 波 3 引入的 alias 链：
--   bg/auto_index_refresher.go half-1 会话腿、bg/credential_selfcheck.go:650、
--   admin/routing.go:2689-2691 探针资格三连、admin/probe_history.go:542、
--   bg/taxonomy_sync.go:234），而现有两条索引都不服务这个形态：
--
--   · idx_model_aliases_lower_raw_name_status (lower(raw_name), status)：
--     表达式索引，条件列是裸 raw_name ⇒ 表达式不匹配，永远不可用；
--   · uq_model_aliases_canonical_raw (canonical_id, raw_name)：
--     raw_name 是第二列，等值条件无前导 ⇒ 规划器只能**全索引扫**（EXPLAIN
--     实测 cost 61.57 / 2,744 条目每次求值）。
--
--   在 hash join 的 Join Filter 里以关联 SubPlan 出现时（CMI_HOT rollup
--   half-1 的 `mo.canonical_id IN (SELECT ma.canonical_id …)`），每次求值
--   = 一次 model_aliases 全索引扫。EXPLAIN 实测该 join cost 34,271
--   （57k→34k 为 862 后基线），252 生产 24h 窗：rollup INSERT ≥1s 完成
--   571 条（mean 15-20s）+ **statement-timeout 击杀 1,201 条（68/h）**，
--   ON CONFLICT 每击杀整批回滚 ⇒ 每 ~2min tick 大概率白烧 30s 连接。
--   同一 SubPlan 形状还按行数放大到凭据自检（×234/17.7h）与探针资格
--   （×395/24h）两族。
--
-- 改法：补 (raw_name, canonical_id) WHERE status='active'——裸列前导 +
--   覆盖输出列（Index Only），部分谓词与全部消费方的 `status='active'`
--   精确一致。不改任何查询语义（保持 raw_name 的大小写敏感等值）；
--   lower(raw_name) 表达式索引留给真正按 lower 过滤的读方。
--
-- 上线代价：model_aliases 2,744 行 / 1.8MB，普通 CREATE INDEX 构建毫秒级；
--   ShareLock 锁窗可忽略（862 先例的 10min 门只是纪律钉，不是本表需求）。
--
-- 回滚：.down.sql 按名删索引，不动数据。

BEGIN;

SET LOCAL statement_timeout = '10min';
SET LOCAL lock_timeout = '60s';

CREATE INDEX IF NOT EXISTS idx_model_aliases_raw_name_active
    ON public.model_aliases (raw_name, canonical_id)
    WHERE status = 'active';

COMMIT;

-- r1030 教训：凡「可能不存在的对象」的 COMMENT 都在 to_regclass 判定后
--   下发，禁止顶层无条件 COMMENT ON（缺席环境 exit=3 ⇒ 迁移永不记账）。
DO $comment_864_index$
BEGIN
  IF to_regclass('public.idx_model_aliases_raw_name_active') IS NOT NULL THEN
    COMMENT ON INDEX idx_model_aliases_raw_name_active IS
      '864: model_aliases 裸列 raw_name 前导部分索引 —— S3 波 3 alias 链（raw_name = lower(外列)）与凭据自检/探针资格/taxonomy sync 的承重索引；lower(raw_name) 表达式索引不服务裸列等值，uq(canonical_id, raw_name) 无前导只能全索引扫。';
  END IF;
END
$comment_864_index$;
