-- 921: 拦截审核工作台（证据 + 标注 + 自动优化）（2026-10-29 输出合规误报轮）。
--
-- # 背景
--
-- 211 生产实测（24h）：6 个请求被「输出合规」拦掉，每个已流出 3806~10529 个
-- chunk，客户端最终只拿到 801 字节；而 21 条审核记录（迁移 917 的
-- sanitize_block_reviews）的 rule_id / reason / field_path / payload_digest
-- **全部为空**。
--
-- ⇒ 审核员面对一张**空白清单**：说不出「因为什么规则被拦」，也看不到「当时
-- 拦了什么内容」⇒ 没法判断「这是规则太宽还是确实违规」⇒ 规则优化没有输入。
-- ⇒ 批准按钮也打不开问题：放行键是 (租户, 规则)，规则为空 ⇒ 批准等于什么
-- 都没做 ⇒ 同一个误报无限复发。
--
-- # 本迁移
--
--   ① sanitize_block_reviews 追加五列（全部可空/有默认 ⇒ 幂等）：
--        session_id     被拦请求的会话 ID（原始会话内容的入口）
--        label          人工标注（误报/真命中/规则太宽/缺规则）
--        label_comment  标注说明
--        labeled_by     标注人
--        labeled_at     标注时间
--      ★ 追加在**末尾**：既有列序不变（列序变更会静默改错 scan 的对应关系）。
--   ② sanitize_block_evidence（新表）：被拦内容的**有界摘录**（原始会话内容），
--      带 7 天 TTL。
--      ★ **单独一张表** —— 迁移 917 的契约是「审核记录表不含载荷原文」，
--        证据表是另一张表、另一套生命周期（TTL），不破坏那条契约。
--   ③ sanitize_egress_releases.source 的 CHECK 扩一个取值
--      （'auto_label'：误报标注累计到阈值后的自动放行）——与人工批准
--      **同键同语义**（同表、同 TTL、同撤销路径）。
--
-- 全部幂等（ADD COLUMN IF NOT EXISTS / CREATE TABLE IF NOT EXISTS /
-- CREATE INDEX IF NOT EXISTS / DO $$ 块重建 CHECK），纯增量、不改既有数据。

-- 锁守卫（startup_ddl_lock_guard_gate_test.go）：本文件含 CREATE INDEX（非
-- CONCURRENTLY，拿 ShareLock）⇒ 必须自带 lock_timeout，避免等锁期间堵住全库
-- （参见 §44；912/913/917 补锁守卫同款）。幂等 DDL 本身在锁等待前就会跳过
-- 已存在对象。
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '30s';

-- ① 审核记录：会话 ID + 人工标注。
ALTER TABLE sanitize_block_reviews
    ADD COLUMN IF NOT EXISTS session_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS label TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS label_comment TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS labeled_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS labeled_at TIMESTAMPTZ;

-- 标注闭集在列级兜底（与迁移 917 的 status 约束同一条线：状态机不只靠
-- Go 校验，DDL 层也拒非法值）。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'sanitize_block_reviews_label_check'
          AND conrelid = 'public.sanitize_block_reviews'::regclass
    ) THEN
        ALTER TABLE sanitize_block_reviews
            ADD CONSTRAINT sanitize_block_reviews_label_check
            CHECK(label IN ('','false_positive','true_positive','rule_too_broad','needs_rule'));
    END IF;
END
$$;

-- 规则级聚合读数（按规则统计标注）的读路径。
CREATE INDEX IF NOT EXISTS sanitize_block_reviews_rule_label_idx
 ON sanitize_block_reviews(tenant_id, rule_id, label);

-- ② 证据表：被拦内容的有界摘录（原始会话内容）。
--
-- ★ TTL 是这条表的**核心**：摘录可能含敏感内容 ⇒ 7 天后自动失效
-- （清理由运营侧按 expires_at 跑；读侧也会跳过过期行）。
-- ★ 与审核记录一对一（review_id 主键）⇒ 一条拦截最多一段摘录。
CREATE TABLE IF NOT EXISTS sanitize_block_evidence (
    review_id BIGINT PRIMARY KEY REFERENCES sanitize_block_reviews(id) ON DELETE CASCADE,
    tenant_id TEXT NOT NULL DEFAULT '',
    -- kind 说明摘录代表什么：tool_argument / assistant_text / guard_capacity。
    kind TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    content_bytes INT NOT NULL DEFAULT 0,
    truncated BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + INTERVAL '7 days')
);
CREATE INDEX IF NOT EXISTS sanitize_block_evidence_expires_idx
 ON sanitize_block_evidence(expires_at);

-- ③ 放行来源扩一个取值：自动优化（误报标注累计到阈值）产生的放行。
--
-- ★ 与人工批准同键（tenant_id, scope, rule_id, payload_digest）⇒ 自动放行
--   与人工放行互相覆盖、互相撤销、同样 7 天过期 ⇒ 没有第二套语义。
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'sanitize_egress_releases_source_check'
          AND conrelid = 'public.sanitize_egress_releases'::regclass
    ) THEN
        ALTER TABLE sanitize_egress_releases
            DROP CONSTRAINT sanitize_egress_releases_source_check;
    END IF;
    ALTER TABLE sanitize_egress_releases
        ADD CONSTRAINT sanitize_egress_releases_source_check
        CHECK(source IN ('manual','review','auto_label'));
END
$$;
