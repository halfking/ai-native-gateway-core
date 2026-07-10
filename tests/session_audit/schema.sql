-- 会话输出审计测试临时表
-- 用于存储审计测试结果和性能数据

-- 1. 审计测试结果表
CREATE TABLE IF NOT EXISTS audit_test_results (
    id                  BIGSERIAL PRIMARY KEY,
    test_run_id         TEXT NOT NULL,              -- 测试批次 ID
    request_id          TEXT NOT NULL,              -- 原始请求 ID
    session_id          TEXT,                       -- 会话 ID
    tenant_id           TEXT,                       -- 租户 ID
    
    -- 原始内容
    content             TEXT NOT NULL,              -- 被审计的内容
    content_length      INT NOT NULL,               -- 内容长度
    content_hash        TEXT NOT NULL,              -- 内容哈希（用于去重）
    
    -- 检测结果
    detect_score        INT NOT NULL,               -- 检测评分 0-10
    decision            TEXT NOT NULL,              -- pass/warn/block/need_approval
    reason              TEXT NOT NULL,              -- 决策原因
    
    -- 敏感词
    sensitive_words     JSONB NOT NULL DEFAULT '[]'::jsonb,  -- 命中的敏感词列表
    sensitive_count     INT NOT NULL DEFAULT 0,     -- 敏感词数量
    
    -- 威胁检测
    threats             JSONB NOT NULL DEFAULT '[]'::jsonb,  -- 威胁列表
    threat_count        INT NOT NULL DEFAULT 0,     -- 威胁数量
    has_prompt_inject   BOOLEAN NOT NULL DEFAULT FALSE,
    has_pii_leak        BOOLEAN NOT NULL DEFAULT FALSE,
    has_jailbreak       BOOLEAN NOT NULL DEFAULT FALSE,
    
    -- 性能指标
    detect_latency_ms   INT NOT NULL,               -- 检测耗时（毫秒）
    detect_latency_us   INT NOT NULL,               -- 检测耗时（微秒）
    
    -- 人工标注（用于准确率评估）
    manual_label        TEXT,                       -- 人工标注：safe/sensitive/malicious
    manual_notes        TEXT,                       -- 人工标注备注
    is_false_positive   BOOLEAN,                    -- 是否假阳性
    is_false_negative   BOOLEAN,                    -- 是否假阴性
    
    -- 元数据
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_audit_test_results_test_run ON audit_test_results(test_run_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_test_results_decision ON audit_test_results(decision, test_run_id);
CREATE INDEX IF NOT EXISTS idx_audit_test_results_latency ON audit_test_results(detect_latency_ms, test_run_id);
CREATE INDEX IF NOT EXISTS idx_audit_test_results_content_hash ON audit_test_results(content_hash);
CREATE INDEX IF NOT EXISTS idx_audit_test_results_threats ON audit_test_results USING GIN(threats);

-- 2. 测试批次元数据表
CREATE TABLE IF NOT EXISTS audit_test_runs (
    test_run_id         TEXT PRIMARY KEY,
    description         TEXT NOT NULL,
    
    -- 测试配置
    detector_config     JSONB NOT NULL,            -- 检测器配置快照
    sensitive_words     TEXT[] NOT NULL,           -- 测试使用的敏感词列表
    test_data_source    TEXT NOT NULL,             -- 数据来源描述
    
    -- 统计
    total_records       INT NOT NULL DEFAULT 0,
    completed_records   INT NOT NULL DEFAULT 0,
    failed_records      INT NOT NULL DEFAULT 0,
    
    -- 性能统计
    avg_latency_ms      NUMERIC(10,2),
    p50_latency_ms      INT,
    p95_latency_ms      INT,
    p99_latency_ms      INT,
    max_latency_ms      INT,
    min_latency_ms      INT,
    
    -- 决策分布
    decision_pass       INT NOT NULL DEFAULT 0,
    decision_warn       INT NOT NULL DEFAULT 0,
    decision_block      INT NOT NULL DEFAULT 0,
    decision_approval   INT NOT NULL DEFAULT 0,
    
    -- 威胁统计
    threat_injection    INT NOT NULL DEFAULT 0,
    threat_pii          INT NOT NULL DEFAULT 0,
    threat_jailbreak    INT NOT NULL DEFAULT 0,
    
    -- 时间
    started_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at        TIMESTAMPTZ,
    duration_seconds    INT
);

-- 3. 性能分析视图
CREATE OR REPLACE VIEW v_audit_performance_summary AS
SELECT
    test_run_id,
    COUNT(*) AS total_tests,
    
    -- 性能指标
    AVG(detect_latency_ms) AS avg_latency_ms,
    PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY detect_latency_ms) AS p50_latency_ms,
    PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY detect_latency_ms) AS p95_latency_ms,
    PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY detect_latency_ms) AS p99_latency_ms,
    MAX(detect_latency_ms) AS max_latency_ms,
    MIN(detect_latency_ms) AS min_latency_ms,
    
    -- 吞吐量（假设单核）
    CASE 
        WHEN AVG(detect_latency_ms) > 0 
        THEN 1000.0 / AVG(detect_latency_ms) 
        ELSE 0 
    END AS throughput_per_sec,
    
    -- 决策分布
    COUNT(*) FILTER (WHERE decision = 'pass') AS decision_pass,
    COUNT(*) FILTER (WHERE decision = 'warn') AS decision_warn,
    COUNT(*) FILTER (WHERE decision = 'block') AS decision_block,
    COUNT(*) FILTER (WHERE decision = 'need_approval') AS decision_approval,
    
    -- 威胁分布
    COUNT(*) FILTER (WHERE has_prompt_inject) AS has_injection,
    COUNT(*) FILTER (WHERE has_pii_leak) AS has_pii,
    COUNT(*) FILTER (WHERE has_jailbreak) AS has_jailbreak,
    
    -- 敏感词统计
    SUM(sensitive_count) AS total_sensitive_words,
    AVG(sensitive_count) AS avg_sensitive_per_test,
    
    -- 内容长度统计
    AVG(content_length) AS avg_content_length,
    MAX(content_length) AS max_content_length
    
FROM audit_test_results
GROUP BY test_run_id;

-- 4. 准确率分析视图（需要人工标注）
CREATE OR REPLACE VIEW v_audit_accuracy_analysis AS
SELECT
    test_run_id,
    COUNT(*) AS total_labeled,
    
    -- 真阳性：机器检测为敏感 + 人工确认为敏感
    COUNT(*) FILTER (
        WHERE decision IN ('warn', 'block', 'need_approval') 
        AND manual_label IN ('sensitive', 'malicious')
    ) AS true_positive,
    
    -- 假阳性：机器检测为敏感 + 人工确认为安全
    COUNT(*) FILTER (
        WHERE decision IN ('warn', 'block', 'need_approval') 
        AND manual_label = 'safe'
    ) AS false_positive,
    
    -- 真阴性：机器通过 + 人工确认为安全
    COUNT(*) FILTER (
        WHERE decision = 'pass' 
        AND manual_label = 'safe'
    ) AS true_negative,
    
    -- 假阴性：机器通过 + 人工确认为敏感
    COUNT(*) FILTER (
        WHERE decision = 'pass' 
        AND manual_label IN ('sensitive', 'malicious')
    ) AS false_negative,
    
    -- 计算指标
    ROUND(
        COUNT(*) FILTER (
            WHERE decision IN ('warn', 'block', 'need_approval') 
            AND manual_label IN ('sensitive', 'malicious')
        )::numeric / NULLIF(
            COUNT(*) FILTER (WHERE manual_label IN ('sensitive', 'malicious')), 
            0
        ), 4
    ) AS recall_rate,  -- 召回率 (TPR)
    
    ROUND(
        COUNT(*) FILTER (
            WHERE decision IN ('warn', 'block', 'need_approval') 
            AND manual_label IN ('sensitive', 'malicious')
        )::numeric / NULLIF(
            COUNT(*) FILTER (WHERE decision IN ('warn', 'block', 'need_approval')), 
            0
        ), 4
    ) AS precision_rate,  -- 精确率
    
    ROUND(
        COUNT(*) FILTER (
            WHERE decision IN ('warn', 'block', 'need_approval') 
            AND manual_label = 'safe'
        )::numeric / NULLIF(
            COUNT(*) FILTER (WHERE manual_label = 'safe'), 
            0
        ), 4
    ) AS false_positive_rate  -- 假阳性率 (FPR)
    
FROM audit_test_results
WHERE manual_label IS NOT NULL
GROUP BY test_run_id;

-- 5. 敏感词命中排行
CREATE OR REPLACE VIEW v_sensitive_words_ranking AS
SELECT
    test_run_id,
    word,
    COUNT(*) AS hit_count,
    COUNT(DISTINCT request_id) AS unique_requests
FROM audit_test_results,
     jsonb_array_elements_text(sensitive_words) AS word
GROUP BY test_run_id, word
ORDER BY test_run_id, hit_count DESC;

-- 注释
COMMENT ON TABLE audit_test_results IS '会话输出审计测试结果表';
COMMENT ON TABLE audit_test_runs IS '审计测试批次元数据表';
COMMENT ON VIEW v_audit_performance_summary IS '性能统计汇总视图';
COMMENT ON VIEW v_audit_accuracy_analysis IS '准确率分析视图（需要人工标注）';
COMMENT ON VIEW v_sensitive_words_ranking IS '敏感词命中排行榜';
