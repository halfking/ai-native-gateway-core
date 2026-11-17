-- Migration: 971_pi_error_analysis_system
-- Purpose: PI 智能体错误自动分析系统
-- Phase: 1 - 基础设施（错误队列和分析结果表）
-- Author: System
-- Date: 2026-11-17
--
-- ★ 收编记录（2026-11-17 同步并行车 + 部署验证轮）：
--   本件原以 641_pi_error_analysis_system.sql 落在 sql/migrations/domain/。
--   ① **domain/ 是死文件区**：该目录 95 个文件**无一件**登记进 installer
--      StartupFiles / deploy 通道 files 数组 ⇒ 没有任何执行器会应用它们
--      （本仓的 db/migrations/ README 亦以同样口径把「无执行器接线」的目录
--      标为冻结通道，纪律是「新的 schema 变更一律走 sql/migrations/startup/」）。
--   ② 落进 domain/ ⇒ 它**逃过了本仓两道投递门**：
--      internal/sqlguard 的三通道覆盖门只扫 sql/migrations/startup/；
--      installer 的 embed 强制门只对 ≥849 的 canonical 件要求 embed 副本。
--      ⇒ 两道门都看不见它 ⇒ 「没登记」这件事不会变红。
--   ③ **编号改 641 → 971**（而非沿用 641）：
--      - 641 < 849 ⇒ 即便搬进 startup/ 也**不在 embed 强制门的覆盖内**；
--      - 641 远离 maxNum（970）⇒ **不在三通道覆盖门的近邻窗口**（maxNum-19..maxNum）
--        ⇒ 仍无门保护，等于「登记对不对全靠人记」。
--      971 同时满足 ≥849 与「= maxNum」⇒ **两道门自动接管这条迁移**，
--      今后任何漏登记/漏 embed 都会在 CI 里变红。
--   ④ **本件改为幂等**（原 641 件不是）：部署通道按内容指纹台账
--      （public.gateway_db_revision_sequences）跳过已应用文件，但**内容一旦
--      被加固（sha 变化）就会重放** ⇒ 非幂等的 CREATE INDEX / CREATE TRIGGER
--      会在重放时 42P07/42710 ⇒ 通道失败 ⇒ 部署 die。
--      故：索引一律 IF NOT EXISTS、触发器先 DROP TRIGGER IF EXISTS、
--      视图/函数用 CREATE OR REPLACE、账本自登记 ON CONFLICT DO UPDATE。
--
-- ── 超时预算（门 TestHeavyDDLModernMigrationsDeclareMeasuredBudget）──────
--   本文件含 9 条**非并发 CREATE INDEX** ⇒ 命中「全表扫描类 DDL」判据 ⇒
--   预算必须按目标环境实测后抬够（761 先例：实测 18.9s ⇒ 抬 10min；
--   914 反例：拍 120s ⇒ 245 部署中止）。
--   实测（目标环境 = 211，acc-pg17 / PostgreSQL 17.10 / arm64 Docker，
--   scratch 库 llm_971_measure，2026-11-17）：
--     · 空库跑完整脚本（2 表 + 9 索引 + 视图 + 触发器 + 函数 + 自登记）：
--       **99ms**（RC=0）
--     · 同一脚本**二次执行**（幂等复核）：**85ms**（RC=0），**ERROR 行数 = 0**
--       ⇒ 幂等来源可点名：索引全 IF NOT EXISTS、触发器先 DROP IF EXISTS、
--         视图/函数 CREATE OR REPLACE、自登记 ON CONFLICT DO UPDATE
--         （这一项是本轮把它从「非幂等原件」改过来的原因：不改则二次执行
--          必 42P07/42710 ⇒ 通道重放失败 ⇒ 部署 die）
--     · 最坏形态（表已存在 + 灌 **500000** 行存量 + 9 条索引全部缺失，
--       迫使 CREATE INDEX 真去扫存量）：**1163ms**（RC=0，500000 行仍在表里）
--       ⇒ 10min 预算（600000ms）是它的 **516 倍** ⇒ 差三个数量级。
--   ⇒ 结论：本迁移的真实成本是**锁窗**而非扫描时长（见下）。
--   锁面（另一道门 TestStartupMigrationsAboveCutoffHaveLockGuard）：
--   本文件 11 条强锁 DDL 全部建在**本文件刚 CREATE 的两张空表**上
--   ⇒ 不触碰任何既有大表 ⇒ 锁窗是毫秒级；仍配 lock_timeout=5s 快速失败
--   （等锁超 5s 即报错退出，而不是堵住全库）。
--
-- ============================================================================
-- 0. 事务与超时预算
-- ============================================================================
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- ============================================================================
-- 1. 错误分析队列表（error_analysis_queue）
-- ============================================================================
-- 用途：收集待分析的 PI 检测错误，支持优先级排序和去重
CREATE TABLE IF NOT EXISTS public.error_analysis_queue (
    id BIGSERIAL PRIMARY KEY,

    -- 错误标识和分类
    error_type TEXT NOT NULL,                    -- 错误类型（如 'llm_detection_failed', 'rule_false_positive'）
    error_hash TEXT NOT NULL,                    -- 错误指纹（基于核心特征的 hash，用于去重）
    priority INTEGER NOT NULL DEFAULT 5,         -- 优先级 1-10（10=最高）

    -- 错误上下文
    tenant_id TEXT NOT NULL,                     -- 租户 ID
    session_id TEXT,                             -- 会话 ID（可选）
    request_id TEXT,                             -- 请求 ID（可选）

    -- 错误详情
    input_text TEXT,                             -- 触发错误的输入文本（可能被截断）
    input_hash TEXT,                             -- 完整输入的 hash（用于追溯）
    error_message TEXT,                          -- 错误消息
    error_details JSONB,                         -- 详细错误信息（JSON 格式）

    -- 检测上下文
    detection_layers JSONB,                      -- 已执行的检测层信息
    matched_rules JSONB,                         -- 已匹配的规则（如果有）
    policy_config JSONB,                         -- 当时的策略配置快照

    -- 处理状态
    status TEXT NOT NULL DEFAULT 'pending',      -- 状态：pending, processing, analyzed, failed, skipped
    retry_count INTEGER NOT NULL DEFAULT 0,      -- 重试次数
    max_retries INTEGER NOT NULL DEFAULT 3,      -- 最大重试次数

    -- 时间戳
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,                      -- 开始分析时间
    completed_at TIMESTAMPTZ,                    -- 完成分析时间

    -- 元数据
    metadata JSONB,                              -- 扩展元数据

    -- 索引字段
    CONSTRAINT error_analysis_queue_status_check
        CHECK (status IN ('pending', 'processing', 'analyzed', 'failed', 'skipped'))
);

-- 创建索引以支持高效查询（★ IF NOT EXISTS：通道会按内容指纹重放）
CREATE INDEX IF NOT EXISTS idx_error_analysis_queue_status_priority
    ON public.error_analysis_queue(status, priority DESC, created_at);

CREATE INDEX IF NOT EXISTS idx_error_analysis_queue_error_hash
    ON public.error_analysis_queue(error_hash);

CREATE INDEX IF NOT EXISTS idx_error_analysis_queue_tenant
    ON public.error_analysis_queue(tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_error_analysis_queue_error_type
    ON public.error_analysis_queue(error_type, created_at DESC);

-- ============================================================================
-- 2. 错误分析结果表（error_analysis_results）
-- ============================================================================
-- 用途：存储 LLM 分析的结果，支持追溯和统计
CREATE TABLE IF NOT EXISTS public.error_analysis_results (
    id BIGSERIAL PRIMARY KEY,

    -- 关联队列项
    queue_id BIGINT NOT NULL REFERENCES public.error_analysis_queue(id) ON DELETE CASCADE,

    -- 分析标识
    analysis_id TEXT NOT NULL UNIQUE,            -- 分析 ID（UUID）
    analyzer_model TEXT NOT NULL,                -- 使用的分析模型（如 'claude-3-5-sonnet'）
    analyzer_version TEXT,                       -- 分析器版本

    -- 分析结果
    root_cause TEXT,                             -- 根本原因分析
    error_category TEXT,                         -- 错误分类
    is_false_positive BOOLEAN,                   -- 是否误报
    confidence_score DECIMAL(3,2),               -- 置信度 0.00-1.00

    -- 建议和修复
    suggested_fix TEXT,                          -- 建议的修复方案
    rule_adjustment JSONB,                       -- 规则调整建议（JSON 格式）
    code_changes JSONB,                          -- 代码修改建议（JSON 格式）

    -- 影响评估
    severity_level TEXT,                         -- 严重程度：low, medium, high, critical
    impact_scope TEXT,                           -- 影响范围：session, tenant, global
    affected_users_estimate INTEGER,             -- 预估受影响用户数

    -- 相似案例
    similar_cases JSONB,                         -- 相似历史案例（JSON 数组）
    pattern_frequency INTEGER,                   -- 模式出现频率

    -- 详细分析
    analysis_details JSONB,                      -- 完整的分析详情（JSON 格式）
    llm_response_raw TEXT,                       -- 原始 LLM 响应

    -- 执行状态
    auto_applied BOOLEAN DEFAULT FALSE,          -- 是否已自动应用修复
    applied_at TIMESTAMPTZ,                      -- 应用时间
    applied_by TEXT,                             -- 应用者（'system' 或用户 ID）

    -- 反馈和验证
    human_verified BOOLEAN,                      -- 人工验证状态
    verification_feedback TEXT,                  -- 验证反馈
    verified_at TIMESTAMPTZ,                     -- 验证时间
    verified_by TEXT,                            -- 验证者

    -- 性能统计
    analysis_duration_ms INTEGER,                -- 分析耗时（毫秒）
    llm_tokens_used INTEGER,                     -- 消耗的 token 数

    -- 时间戳
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- 元数据
    metadata JSONB,                              -- 扩展元数据

    -- 约束
    CONSTRAINT error_analysis_results_severity_check
        CHECK (severity_level IN ('low', 'medium', 'high', 'critical')),
    CONSTRAINT error_analysis_results_impact_check
        CHECK (impact_scope IN ('session', 'tenant', 'global')),
    CONSTRAINT error_analysis_results_confidence_check
        CHECK (confidence_score >= 0.00 AND confidence_score <= 1.00)
);

-- 创建索引
CREATE INDEX IF NOT EXISTS idx_error_analysis_results_queue
    ON public.error_analysis_results(queue_id);

CREATE INDEX IF NOT EXISTS idx_error_analysis_results_category
    ON public.error_analysis_results(error_category, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_error_analysis_results_severity
    ON public.error_analysis_results(severity_level, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_error_analysis_results_false_positive
    ON public.error_analysis_results(is_false_positive, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_error_analysis_results_auto_applied
    ON public.error_analysis_results(auto_applied, created_at DESC);

-- ============================================================================
-- 3. 分析统计视图（v_error_analysis_stats）
-- ============================================================================
-- 用途：提供错误分析的统计信息
CREATE OR REPLACE VIEW public.v_error_analysis_stats AS
SELECT
    q.error_type,
    COUNT(*) as total_errors,
    COUNT(CASE WHEN q.status = 'analyzed' THEN 1 END) as analyzed_count,
    COUNT(CASE WHEN q.status = 'pending' THEN 1 END) as pending_count,
    COUNT(CASE WHEN r.is_false_positive = TRUE THEN 1 END) as false_positive_count,
    AVG(r.confidence_score) as avg_confidence,
    AVG(r.analysis_duration_ms) as avg_analysis_time_ms,
    SUM(r.llm_tokens_used) as total_tokens_used,
    MAX(q.created_at) as last_error_at
FROM public.error_analysis_queue q
LEFT JOIN public.error_analysis_results r ON q.id = r.queue_id
GROUP BY q.error_type;

-- ============================================================================
-- 4. 触发器：自动更新 updated_at
-- ============================================================================
-- ★ 先 DROP TRIGGER IF EXISTS 再建：通道按内容指纹重放时，裸 CREATE TRIGGER
--   会 42710（trigger already exists）⇒ 通道失败。
CREATE OR REPLACE FUNCTION public.update_error_analysis_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trigger_error_analysis_results_updated_at
    ON public.error_analysis_results;

CREATE TRIGGER trigger_error_analysis_results_updated_at
    BEFORE UPDATE ON public.error_analysis_results
    FOR EACH ROW
    EXECUTE FUNCTION public.update_error_analysis_updated_at();

-- ============================================================================
-- 5. 数据保留策略函数
-- ============================================================================
-- 用途：清理超过 90 天的已分析错误（保留关键统计信息）
CREATE OR REPLACE FUNCTION public.cleanup_old_error_analysis(retention_days INTEGER DEFAULT 90)
RETURNS TABLE(deleted_queue_count BIGINT, deleted_result_count BIGINT) AS $$
DECLARE
    v_deleted_results BIGINT;
    v_deleted_queue BIGINT;
BEGIN
    -- 删除超过保留期的已分析结果（保留未解决的）
    DELETE FROM public.error_analysis_results
    WHERE created_at < NOW() - (retention_days || ' days')::INTERVAL
      AND auto_applied = TRUE
      AND human_verified = TRUE
    RETURNING * INTO v_deleted_results;

    GET DIAGNOSTICS v_deleted_results = ROW_COUNT;

    -- 删除对应的队列项（级联删除）
    DELETE FROM public.error_analysis_queue
    WHERE id NOT IN (SELECT queue_id FROM public.error_analysis_results)
      AND status IN ('analyzed', 'skipped')
      AND created_at < NOW() - (retention_days || ' days')::INTERVAL;

    GET DIAGNOSTICS v_deleted_queue = ROW_COUNT;

    deleted_result_count := v_deleted_results;
    deleted_queue_count := v_deleted_queue;

    RETURN NEXT;
END;
$$ LANGUAGE plpgsql;

-- ============================================================================
-- 6. 注释和文档
-- ============================================================================
COMMENT ON TABLE public.error_analysis_queue IS 'PI 检测错误队列，存储待分析的错误';
COMMENT ON TABLE public.error_analysis_results IS 'LLM 智能分析结果，包含根因分析和修复建议';
COMMENT ON VIEW public.v_error_analysis_stats IS '错误分析统计视图，用于监控和报告';
COMMENT ON FUNCTION public.cleanup_old_error_analysis IS '清理超过指定天数的已分析错误记录';

-- ============================================================================
-- 7. 台账自登记（≥952 纪律）
-- ============================================================================
-- ★ 双账本纪律：211 的 public.schema_migrations 与 public.gateway_db_revision_sequences
--   都必须有 971 的行，否则「账本说没做」⇒ 后续按账本判断的人会踩空
--   （962 那次断层的同型，见 internal/sqlguard/migration_delivery_channel_coverage_test.go）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('971', 'pi_error_analysis_system: PI 检测错误队列与分析结果表（Phase 1 基础设施）')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
