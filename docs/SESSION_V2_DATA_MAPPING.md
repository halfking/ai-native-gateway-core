# 会话存储V2数据映射文档

> **版本**: v1.0  
> **日期**: 2026-07-17  
> **状态**: DESIGN  
> **作者**: llm-gateway-ops

## 1. 概述

本文档描述会话存储V1（request_logs体系）到V2（sessions体系）的数据映射关系，用于：
1. 双写实现的字段映射
2. 历史数据回填的转换逻辑
3. 数据一致性校验的对照规则

## 2. 核心设计原则

### 2.1 并行架构

```
┌─────────────────────────────────────────────────────────┐
│  V1 体系（现有，保持不变）                                │
│  ├─ request_logs (主表，全量JSONB)                       │
│  ├─ request_logs_bodies (正文外置，部分场景)              │
│  └─ request_attachments (附件)                           │
├─────────────────────────────────────────────────────────┤
│  V2 体系（新建，并行运行）                                │
│  ├─ sessions (会话快照，一条/会话)                        │
│  ├─ session_turns (轮次元数据，不含正文)                 │
│  ├─ session_bodies (正文增量，columnar)                  │
│  └─ session_turn_logs (环节日志，24h TTL)                │
└─────────────────────────────────────────────────────────┘
         ↑                           ↑
         │                           │
    通过request_id关联           Feature Flag控制
```

### 2.2 关联键

- **V1 → V2**: 通过 `request_id` 关联
- **会话聚合**: 通过 `gw_session_id` → `session_id` 映射
- **租户隔离**: 两个体系都使用 `tenant_id` + RLS

## 3. 表映射关系

### 3.1 request_logs → sessions

**映射类型**: 聚合映射（多条request_logs → 一条sessions）

| request_logs字段 | sessions字段 | 转换逻辑 | 备注 |
|-----------------|-------------|---------|------|
| `gw_session_id` | `session_id` | 直接映射 | 主键 |
| `tenant_id` | `tenant_id` | 直接映射 | 租户隔离 |
| `MIN(ts)` | `created_at` | 聚合：最早时间 | 首次请求时间 |
| `MAX(ts)` | `updated_at` | 聚合：最晚时间 | 最后一次请求时间 |
| - | `closed_at` | 业务逻辑 | 会话关闭时间（新增字段） |
| - | `status` | 业务逻辑 | active/closed/archived |
| `COUNT(*)` | `total_turns` | 聚合：计数 | 总轮次数 |
| `SUM(prompt_tokens + completion_tokens)` | `total_tokens` | 聚合：求和 | 总Token数 |
| `SUM(cost_usd)` | `total_cost_usd` | 聚合：求和 | 总成本 |
| `MAX(turn_no)` | `last_turn_no` | 聚合：最大值 | 最后轮次号 |
| `request_preview` (最后一条) | `last_request_summary` | 取最后一条 | 最后请求摘要 |
| `response_preview` (最后一条) | `last_response_summary` | 取最后一条 | 最后回复摘要 |
| `client_model` (最后一条) | `last_model` | 取最后一条 | 最后使用模型 |
| `provider_id` (最后一条) | `last_provider` | 取最后一条 | 最后使用供应商 |
| - | `task_type` | 从session_summaries提取 | 任务类型 |
| - | `client_type` | 从user_agent推断 | 客户端类型 |
| - | `topic` | 从session_summaries提取 | 主题 |
| - | `intent` | 从session_summaries提取 | 意图 |
| `request_id` (第一条) | `primary_request_id` | 取第一条 | 用于关联校验 |

**SQL示例**:
```sql
INSERT INTO gateway.sessions (
    session_id, tenant_id, created_at, updated_at,
    total_turns, total_tokens, total_cost_usd,
    last_turn_no, last_request_summary, last_response_summary,
    last_model, last_provider, primary_request_id,
    partition_date
)
SELECT 
    gw_session_id AS session_id,
    tenant_id,
    MIN(ts) AS created_at,
    MAX(ts) AS updated_at,
    COUNT(*) AS total_turns,
    SUM(COALESCE(prompt_tokens, 0) + COALESCE(completion_tokens, 0)) AS total_tokens,
    SUM(COALESCE(cost_usd, 0)) AS total_cost_usd,
    MAX(turn_no) AS last_turn_no,
    (ARRAY_AGG(request_preview ORDER BY ts DESC))[1] AS last_request_summary,
    (ARRAY_AGG(response_preview ORDER BY ts DESC))[1] AS last_response_summary,
    (ARRAY_AGG(client_model ORDER BY ts DESC))[1] AS last_model,
    (ARRAY_AGG(provider_id ORDER BY ts DESC))[1] AS last_provider,
    (ARRAY_AGG(request_id ORDER BY ts ASC))[1] AS primary_request_id,
    DATE_TRUNC('month', MIN(ts))::DATE AS partition_date
FROM request_logs
WHERE gw_session_id IS NOT NULL
GROUP BY gw_session_id, tenant_id
ON CONFLICT (session_id, partition_date) 
DO UPDATE SET
    updated_at = EXCLUDED.updated_at,
    total_turns = EXCLUDED.total_turns,
    total_tokens = EXCLUDED.total_tokens,
    total_cost_usd = EXCLUDED.total_cost_usd,
    last_turn_no = EXCLUDED.last_turn_no,
    last_request_summary = EXCLUDED.last_request_summary,
    last_response_summary = EXCLUDED.last_response_summary,
    last_model = EXCLUDED.last_model,
    last_provider = EXCLUDED.last_provider;
```

### 3.2 request_logs → session_turns

**映射类型**: 一对一映射（一条request_logs → 一条session_turns）

| request_logs字段 | session_turns字段 | 转换逻辑 | 备注 |
|-----------------|------------------|---------|------|
| `gw_session_id` | `session_id` | 直接映射 | 会话ID |
| - | `turn_no` | 窗口函数计算 | `ROW_NUMBER() OVER (PARTITION BY gw_session_id ORDER BY ts)` |
| `tenant_id` | `tenant_id` | 直接映射 | 租户ID |
| `request_id` | `request_id` | 直接映射 | 关联键 |
| `ts` | `ts` | 直接映射 | 时间戳 |
| - | `submit_mode` | 检测逻辑 | full/delta/snapshot/inferred_compressed |
| `compression_strategy IS NOT NULL` | `compression_applied` | 逻辑判断 | 是否压缩 |
| `compression_strategy` | `compression_strategy` | 直接映射 | 压缩策略 |
| `compression_meta` | `compression_meta` | 直接映射 | 压缩元数据 |
| `compression_meta->>'tokens_saved'` | `compression_tokens_saved` | JSON提取 | 节约Token数 |
| - | `injection_verdict` | 从compression_meta提取或默认skip | 注入检测结果 |
| - | `output_verdict` | 从compression_meta提取或默认skip | 输出检测结果 |
| `client_model` | `model` | 直接映射 | 模型 |
| `provider_id` | `provider` | 直接映射 | 供应商 |
| `credential_id` | `credential_id` | 直接映射 | 凭据ID |
| `prompt_tokens` | `prompt_tokens` | 直接映射 | 提示Token |
| `completion_tokens` | `completion_tokens` | 直接映射 | 完成Token |
| `cache_read_tokens` | `cache_read_tokens` | 直接映射 | 缓存读Token |
| `cache_write_tokens` | `cache_write_tokens` | 直接映射 | 缓存写Token |
| `cost_usd` | `cost_usd` | 直接映射 | 成本 |
| `EXTRACT(EPOCH FROM (completed_at - ts)) * 1000` | `latency_ms` | 计算 | 延迟毫秒 |
| `CASE WHEN success THEN 200 ELSE 500 END` | `status_code` | 逻辑判断 | HTTP状态码 |
| `success` | `success` | 直接映射 | 是否成功 |
| `error_kind` | `error_kind` | 直接映射 | 错误类型 |
| 'live' 或 'backfill' | `source_kind` | 上下文决定 | 数据来源 |
| 'verified' 或 'inferred' | `quality` | 上下文决定 | 数据质量 |
| `DATE_TRUNC('month', ts)` | `partition_date` | 计算 | 分区键 |

**SQL示例**:
```sql
INSERT INTO gateway.session_turns (
    session_id, turn_no, tenant_id, request_id, ts,
    submit_mode, compression_applied, compression_strategy, compression_meta,
    injection_verdict, output_verdict,
    model, provider, credential_id,
    prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
    latency_ms, status_code, success, error_kind,
    source_kind, quality, partition_date
)
SELECT 
    gw_session_id AS session_id,
    ROW_NUMBER() OVER (PARTITION BY gw_session_id ORDER BY ts) AS turn_no,
    tenant_id,
    request_id,
    ts,
    'full' AS submit_mode,  -- 历史数据默认full模式
    (compression_strategy IS NOT NULL) AS compression_applied,
    compression_strategy,
    COALESCE(compression_meta, '{}'::jsonb) AS compression_meta,
    COALESCE(compression_meta->>'injection_verdict', 'skip') AS injection_verdict,
    COALESCE(compression_meta->>'output_verdict', 'skip') AS output_verdict,
    client_model AS model,
    provider_id AS provider,
    credential_id,
    prompt_tokens,
    completion_tokens,
    cache_read_tokens,
    cache_write_tokens,
    cost_usd,
    EXTRACT(EPOCH FROM (completed_at - ts))::INT * 1000 AS latency_ms,
    CASE WHEN success THEN 200 ELSE 500 END AS status_code,
    success,
    error_kind,
    'backfill' AS source_kind,
    CASE 
        WHEN gw_session_id IS NOT NULL AND request_body IS NOT NULL THEN 'verified'
        ELSE 'inferred'
    END AS quality,
    DATE_TRUNC('month', ts)::DATE AS partition_date
FROM request_logs
WHERE gw_session_id IS NOT NULL
ON CONFLICT (request_id, partition_date) DO NOTHING;
```

### 3.3 request_logs → session_bodies

**映射类型**: 一对一映射（正文提取）

| request_logs字段 | session_bodies字段 | 转换逻辑 | 备注 |
|-----------------|-------------------|---------|------|
| `gw_session_id` | `session_id` | 直接映射 | 会话ID |
| 计算得出 | `turn_no` | 窗口函数 | 同session_turns |
| `tenant_id` | `tenant_id` | 直接映射 | 租户ID |
| `request_id` | `request_id` | 直接映射 | 关联键 |
| `request_body` | `request_delta` | **增量提取** | 只存本轮新增消息 |
| `response_body` | `response_delta` | 直接映射 | 本轮回复 |
| `outbound_body` | `outbound_body` | 直接映射 | 压缩后正文 |
| `attachments` | `request_attachments` | 直接映射 | 请求附件 |
| - | `response_attachments` | 提取 | 回复附件 |
| `ts` | `ts` | 直接映射 | 时间戳 |
| `DATE_TRUNC('month', ts)` | `partition_date` | 计算 | 分区键 |

**增量提取逻辑**:
```sql
-- request_delta提取逻辑（伪代码）
WITH session_messages AS (
    SELECT 
        gw_session_id,
        request_id,
        ts,
        request_body,
        LAG(outbound_body) OVER (PARTITION BY gw_session_id ORDER BY ts) AS prev_outbound
    FROM request_logs
    WHERE gw_session_id = :session_id
)
SELECT 
    -- 提取本轮新增的消息（不在prev_outbound中的消息）
    jsonb_array_elements(request_body->'messages') AS msg
    -- 过滤：只保留不在prev_outbound中的消息
    WHERE NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(prev_outbound->'messages') AS prev_msg
        WHERE prev_msg = msg
    )
```

**注意**：增量提取是V2的核心优化，回填时需要按会话顺序处理。

### 3.4 request_logs → session_turn_logs

**映射类型**: 派生映射（从request_logs的处理过程日志提取）

| 来源 | session_turn_logs字段 | 转换逻辑 | 备注 |
|-----|---------------------|---------|------|
| `gw_session_id` | `session_id` | 直接映射 | 会话ID |
| 计算 | `turn_no` | 窗口函数 | 轮次号 |
| `tenant_id` | `tenant_id` | 直接映射 | 租户ID |
| `request_id` | `request_id` | 直接映射 | 关联键 |
| 从processing_stages提取 | `stage` | 解析 | routing/compression/... |
| 从processing_stages提取 | `stage_status` | 解析 | success/failed/... |
| 从processing_stages提取 | `event_data` | 解析 | 详细数据 |
| 从error字段提取 | `error_message` | 提取 | 错误信息 |
| `ts` | `started_at` | 映射 | 开始时间 |
| `completed_at` | `completed_at` | 映射 | 完成时间 |
| 计算 | `latency_ms` | 差值 | 延迟 |
| `ts + 24h` | `expires_at` | 计算 | 过期时间 |

**注意**：历史数据回填时，session_turn_logs通常留空或只回填关键失败记录。

## 4. 双写实现

### 4.1 双写流程

```go
// 伪代码
func (p *Pipeline) PersistRequest(ctx context.Context, req *ProcessedRequest) error {
    // 1. 主写：写入V1（request_logs）
    err := p.v1Writer.Write(ctx, req)
    if err != nil {
        return fmt.Errorf("v1 write failed: %w", err)  // 主写失败则整体失败
    }
    
    // 2. 副写：写入V2（sessions体系）
    if p.featureFlags.IsEnabled("sessions_v2_shadow_write") {
        if err := p.v2Writer.Write(ctx, req); err != nil {
            // 副写失败不影响主流程，记录日志和指标
            log.Error("v2 shadow write failed", "error", err, "request_id", req.RequestID)
            p.metrics.V2WriteFailed.Inc()
        } else {
            p.metrics.V2WriteSuccess.Inc()
        }
    }
    
    return nil
}
```

### 4.2 V2 Writer实现

```go
type SessionWriterV2 struct {
    db              *pgxpool.Pool
    turnWriter      *TurnWriter
    bodiesWriter    *SessionBodiesWriter
    sessionAggregator *SessionAggregator
}

func (w *SessionWriterV2) Write(ctx context.Context, req *ProcessedRequest) error {
    // 1. 写入session_turns（元数据）
    turnNo, err := w.turnWriter.AppendTurn(ctx, TurnRecord{
        SessionID:    req.GwSessionID,
        TenantID:     req.TenantID,
        RequestID:    req.RequestID,
        Ts:           req.Timestamp,
        SubmitMode:   DetectSubmitMode(req),
        Compression:  req.CompressionMeta,
        Model:        req.ClientModel,
        Provider:     req.ProviderID,
        Tokens:       req.Tokens,
        Cost:         req.CostUSD,
        // ...
    })
    if err != nil {
        return fmt.Errorf("append turn: %w", err)
    }
    
    // 2. 写入session_bodies（正文增量）
    err = w.bodiesWriter.Write(ctx, BodiesRecord{
        SessionID:     req.GwSessionID,
        TurnNo:        turnNo,
        TenantID:      req.TenantID,
        RequestID:     req.RequestID,
        RequestDelta:  ExtractRequestDelta(req),  // 提取增量
        ResponseDelta: req.ResponseBody,
        OutboundBody:  req.OutboundBody,
        Attachments:   req.Attachments,
    })
    if err != nil {
        return fmt.Errorf("write bodies: %w", err)
    }
    
    // 3. 更新sessions（会话快照，异步或批量）
    go w.sessionAggregator.UpdateSession(req.GwSessionID)
    
    return nil
}
```

## 5. 数据校验规则

### 5.1 基础一致性校验

```sql
-- 校验：session_turns与request_logs的请求ID应该一一对应
WITH v1_requests AS (
    SELECT request_id, gw_session_id, ts
    FROM request_logs
    WHERE gw_session_id = :session_id
),
v2_turns AS (
    SELECT request_id, session_id, ts, turn_no
    FROM gateway.session_turns
    WHERE session_id = :session_id
)
SELECT 
    COALESCE(v1.request_id, v2.request_id) AS request_id,
    CASE 
        WHEN v1.request_id IS NULL THEN 'missing_in_v1'
        WHEN v2.request_id IS NULL THEN 'missing_in_v2'
        WHEN v1.ts <> v2.ts THEN 'timestamp_mismatch'
        ELSE 'ok'
    END AS status
FROM v1_requests v1
FULL OUTER JOIN v2_turns v2 ON v1.request_id = v2.request_id;
```

### 5.2 聚合一致性校验

```sql
-- 校验：sessions的统计数据与request_logs聚合结果应该一致
WITH v1_agg AS (
    SELECT 
        gw_session_id,
        COUNT(*) AS turn_count,
        SUM(prompt_tokens + completion_tokens) AS total_tokens,
        SUM(cost_usd) AS total_cost
    FROM request_logs
    WHERE gw_session_id = :session_id
    GROUP BY gw_session_id
),
v2_session AS (
    SELECT 
        session_id,
        total_turns,
        total_tokens,
        total_cost_usd
    FROM gateway.sessions
    WHERE session_id = :session_id
)
SELECT 
    v1.gw_session_id,
    v1.turn_count AS v1_turns,
    v2.total_turns AS v2_turns,
    v1.total_tokens AS v1_tokens,
    v2.total_tokens AS v2_tokens,
    v1.total_cost AS v1_cost,
    v2.total_cost_usd AS v2_cost,
    CASE 
        WHEN v1.turn_count <> v2.total_turns THEN 'turn_count_mismatch'
        WHEN ABS(v1.total_tokens - v2.total_tokens) > 10 THEN 'token_mismatch'
        WHEN ABS(v1.total_cost - v2.total_cost_usd) > 0.01 THEN 'cost_mismatch'
        ELSE 'ok'
    END AS validation_status
FROM v1_agg v1
LEFT JOIN v2_session v2 ON v1.gw_session_id = v2.session_id;
```

### 5.3 增量正文校验

```go
// 伪代码：校验session_bodies的增量拼接是否还原原始request_body
func ValidateIncrementalBodies(sessionID string) error {
    // 1. 从V1读取完整历史
    v1Bodies := LoadFromRequestLogs(sessionID)
    
    // 2. 从V2读取增量并重建
    v2Turns := LoadFromSessionBodies(sessionID)
    reconstructed := ReconstructFromDeltas(v2Turns)
    
    // 3. 逐轮对比
    for i := range v1Bodies {
        if !jsonEqual(v1Bodies[i], reconstructed[i]) {
            return fmt.Errorf("turn %d mismatch", i+1)
        }
    }
    
    return nil
}
```

## 6. 性能优化考虑

### 6.1 批量写入优化

```go
// 批量更新sessions表，避免每次请求都更新
type SessionBatchAggregator struct {
    buffer map[string]*SessionUpdate
    mu     sync.Mutex
    timer  *time.Timer
}

func (a *SessionBatchAggregator) QueueUpdate(sessionID string, update *SessionUpdate) {
    a.mu.Lock()
    defer a.mu.Unlock()
    
    // 合并更新
    if existing, ok := a.buffer[sessionID]; ok {
        existing.Merge(update)
    } else {
        a.buffer[sessionID] = update
    }
    
    // 达到批量阈值或定时触发
    if len(a.buffer) >= 100 {
        a.Flush()
    }
}

func (a *SessionBatchAggregator) Flush() {
    // 批量执行UPDATE
    // ...
}
```

### 6.2 分区并行回填

```sql
-- 按月分区并行回填
DO $$
DECLARE
    month_date DATE;
BEGIN
    FOR month_date IN 
        SELECT generate_series('2024-01-01'::DATE, CURRENT_DATE, '1 month'::INTERVAL)::DATE
    LOOP
        RAISE NOTICE 'Backfilling month: %', month_date;
        
        -- 每个月的数据独立回填
        PERFORM backfill_sessions_v2_for_month(month_date);
        
        COMMIT;  -- 每月提交一次
    END LOOP;
END;
$$;
```

## 7. 监控指标

### 7.1 双写指标

| 指标名称 | 类型 | 说明 |
|---------|------|------|
| `sessions_v2_write_success_total` | Counter | V2写入成功次数 |
| `sessions_v2_write_failure_total` | Counter | V2写入失败次数 |
| `sessions_v2_write_latency_seconds` | Histogram | V2写入延迟 |
| `sessions_v2_backfill_progress` | Gauge | 回填进度百分比 |

### 7.2 数据一致性指标

| 指标名称 | 类型 | 说明 |
|---------|------|------|
| `sessions_v2_validation_diff_total` | Counter | 发现的数据差异数量 |
| `sessions_v2_turn_count_mismatch_total` | Counter | 轮次数不一致数量 |
| `sessions_v2_token_sum_diff_total` | Counter | Token总数差异数量 |

## 8. 回滚策略

如果发现V2数据有问题，回滚步骤：

1. **立即停止副写**：
   ```yaml
   feature_flags:
     sessions_v2_shadow_write: false
   ```

2. **停止读取V2**：
   ```yaml
   feature_flags:
     sessions_v2_read_enabled: false
   ```

3. **评估数据**：
   - 运行数据一致性校验脚本
   - 确认差异范围和影响

4. **修复或删除**：
   - 如果可修复：运行补偿脚本
   - 如果无法修复：执行down migration删除V2表

5. **恢复服务**：
   - 确认系统完全回退到V1
   - 监控V1系统稳定性

## 9. 附录

### 9.1 常用查询

```sql
-- 查询某会话的V1和V2数据对比
SELECT 
    'v1' AS source,
    COUNT(*) AS turns,
    SUM(prompt_tokens + completion_tokens) AS tokens,
    SUM(cost_usd) AS cost
FROM request_logs
WHERE gw_session_id = :session_id
UNION ALL
SELECT 
    'v2' AS source,
    total_turns AS turns,
    total_tokens AS tokens,
    total_cost_usd AS cost
FROM gateway.sessions
WHERE session_id = :session_id;

-- 查询V2写入失败的请求
SELECT request_id, ts, error_kind
FROM request_logs
WHERE gw_session_id IS NOT NULL
  AND request_id NOT IN (
      SELECT request_id FROM gateway.session_turns
  )
ORDER BY ts DESC
LIMIT 100;
```

### 9.2 数据字典速查

**会话状态枚举**:
- `active`: 活跃会话
- `closed`: 已关闭
- `archived`: 已归档
- `deleted`: 已删除

**提交模式枚举**:
- `full`: 客户端发送完整历史
- `delta`: 客户端只发送增量
- `snapshot`: 客户端已压缩
- `inferred_compressed`: 网关推断已压缩

**数据质量枚举**:
- `verified`: 已验证准确
- `inferred`: 推断得出
- `partial`: 部分数据
- `rejected`: 数据异常

---

**文档维护**：
- 每次schema变更需要同步更新本文档
- 每次回填逻辑调整需要更新转换规则
- 每次发现新的数据差异需要补充校验规则
