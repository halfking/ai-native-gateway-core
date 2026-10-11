-- 03 — 252 生产侧修正（对账文档 README.md §4）
-- 原则：分区父表（session_turns/request_logs 生产均为 relkind='p'）只做 NOT VALID
--       CHECK（随父表向子表传播、零扫描）；索引仅在 plain 表上 CONCURRENTLY 建；
--       全部幂等可重放；不触碰数据。
-- 执行：psql -X -v ON_ERROR_STOP=1 -f 03-prod-fix.sql "$PROD_DB"
--      （CONCURRENTLY 不能在事务内；本脚本无显式 BEGIN，逐语句自动提交。
--       若 CONCURRENTLY 失败会留下 INVALID 索引：DROP 后重跑即可。）
\set ON_ERROR_STOP on

-- ── P1. session_turns 附件/尝试号检查（creator 474；生产缺；分区父表→仅 NOT VALID）─
DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='session_turns_attachment_count_check') THEN
  ALTER TABLE public.session_turns ADD CONSTRAINT session_turns_attachment_count_check
    CHECK (((attachment_count IS NULL) OR (attachment_count >= 0))) NOT VALID;
END IF; END$$;
DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='session_turns_attachment_total_bytes_check') THEN
  ALTER TABLE public.session_turns ADD CONSTRAINT session_turns_attachment_total_bytes_check
    CHECK (((attachment_total_bytes IS NULL) OR (attachment_total_bytes >= 0))) NOT VALID;
END IF; END$$;
DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='session_turns_attempt_no_check') THEN
  ALTER TABLE public.session_turns ADD CONSTRAINT session_turns_attempt_no_check
    CHECK ((attempt_no >= 0)) NOT VALID;
END IF; END$$;

-- ── P2. 探针队列认领索引（生产缺；plain 表 CONCURRENTLY，不锁 probe worker 写入）──
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_credential_probe_queue_automatic_claim
  ON public.credential_probe_queue USING btree (automatic, priority DESC, next_run_at, id)
  WHERE (status = 'ready'::text);

-- ── P3. token_band 检查（816/824 内容加固重放缺口；分区父表→仅 NOT VALID）───────
DO $$BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='chk_request_logs_token_band') THEN
  ALTER TABLE public.request_logs ADD CONSTRAINT chk_request_logs_token_band
    CHECK (((token_band IS NULL) OR (token_band = ANY (ARRAY['below'::text, 'preliminary'::text, 'forced'::text])))) NOT VALID;
END IF; END$$;

-- 延后项（锁窗口原因不在本轮执行，登记为遗留）：
--   * idx_request_logs_token_band_ts：分区父表，需逐分区 CONCURRENTLY + parent ONLY
--     的维护窗口操作。（idx_session_turns_attachment_count_gw 生产实测已存在，无需处理。）
--   * candidate_failure_logs_hot.ts SET NOT NULL（392 canonical，生产 nullable）：热表锁风险。
--   * self_check_runs_error_type_check 的 NOT VALID 旗标差：值域一致，无行为差异。
