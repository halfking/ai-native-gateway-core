# LLM Gateway 系统审计报告（修正版）
**日期**: 2026-06-30  
**审计范围**: 错误处理、数据统计、凭据路由  
**服务器**: __HOST_71_IP__ (71服务器)  
**数据库**: llm_gateway (NOT crm - 初次审计错误)

## 执行摘要

通过对71服务器LLM Gateway的深度审计，发现了**4个关键问题**（3个已修复，1个待修复）。

**核心发现**:
1. ✅ **P0-1已修复**: `request_wal` 缺少6月分区，导致当前日期写入失败
2. ⚠️ **P0-2**: 模型别名缺失 - `minimax-m2.7-quickspeed` 不存在
3. ⚠️ **P1**: minimax-m3 空响应问题（14次 empty_response 错误）
4. ⚠️ **P2**: 字节数统计完全缺失

---

## 🔍 问题1: request_wal分区缺失 (P0-1) ✅ 已修复

### 现象
```
2026-06-30T07:35:18.149016746Z WARN request_logger: CreateInitial failed 
request_id=56c3333b3cf61a230f47c4b890a964a4 
error="ERROR: no partition of relation \"request_wal\" found for row (SQLSTATE 23514)"
```

### 根因分析
- 当前日期: **2026-06-30**
- `request_wal` 分区情况:
  - ❌ 6月分区不存在
  - ✅ 7月分区存在
- `request_logs` 有DEFAULT分区，所以6月数据能写入
- `request_wal` 无DEFAULT分区，所以6月数据写入失败

### 分区对比

| 表 | 6月分区 | 7月分区 | DEFAULT分区 |
|----|---------|---------|-------------|
| request_logs | ❌ | ✅ | ✅ |
| request_wal | ❌ | ✅ | ❌ |

### 修复措施 ✅
```sql
CREATE TABLE request_wal_2026_06 PARTITION OF request_wal
    FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00');
```

**执行时间**: 2026-06-30 07:40 UTC  
**验证结果**: 错误日志停止，写入恢复正常

### 预防措施
建议添加自动分区创建函数（参考 `deploy/sql/01-schema.sql` 中的 `ensure_request_logs_partition()`）:

```sql
CREATE OR REPLACE FUNCTION ensure_request_wal_partition(
    target_ts timestamp with time zone DEFAULT now()
) RETURNS void AS $$
DECLARE
    month_start date := date_trunc('month', target_ts)::date;
    next_month  date := (month_start + interval '1 month')::date;
    part_name   text := 'request_wal_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = part_name) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_wal FOR VALUES FROM (%L) TO (%L)',
            part_name, month_start, next_month
        );
    END IF;
END;
$$ LANGUAGE plpgsql;

-- 添加定时任务，每天检查
-- cron: 0 0 * * * psql -c "SELECT ensure_request_wal_partition(NOW() + interval '1 month')"
```

---

## 🔍 问题2: 模型别名缺失 (P0-2) ⚠️

### 现象
过去24小时内，9次 `no_candidate` 错误，全部来自 `minimax-m2.7-quickspeed`：

```sql
SELECT client_model, COUNT(*) as count, MAX(ts) as last_occurrence 
FROM request_logs 
WHERE ts >= NOW() - INTERVAL '24 hours' AND error_kind = 'no_candidate' 
GROUP BY client_model;

      client_model       | count |        last_occurrence        
-------------------------+-------+-------------------------------
 minimax-m2.7-quickspeed |     9 | 2026-06-30 06:26:56.262206+00
```

### 根因分析

**数据库中的minimax模型**:
```
MiniMax-M2.7-highspeed  → minimax-m2.7-highspeed
MiniMax-M2.7            → minimax-m2.7
minimax-m2.7            → minimax-m2.7
minimaxai/minimax-m2.7  → minimax-m2.7
```

**客户端请求的模型**: `minimax-m2.7-quickspeed`

**问题**: 数据库中没有 `quickspeed` 变体，只有 `highspeed`。

### 路由匹配流程

`provider/client.go:654-796` 的 `loadCandidatesDB()` 方法按以下顺序匹配：

1. **raw_model_name 精确匹配** (line 759)
   ```sql
   lower(mo.raw_model_name) = 'minimax-m2.7-quickspeed'  -- ❌ 不匹配
   ```

2. **standardized_name 匹配** (line 768)
   ```sql
   lower(mo.standardized_name) = 'minimax-m2.7-quickspeed'  -- ❌ 不匹配
   ```

3. **model_aliases 匹配** (line 770-778)
   ```sql
   -- 查询 model_aliases 表
   SELECT * FROM model_aliases 
   WHERE lower(raw_name) = 'minimax-m2.7-quickspeed';  -- ❌ 不存在
   ```

### 修复方案

**方案1: 添加模型别名** (推荐)
```sql
-- 查找 minimax-m2.7 的 canonical_id
SELECT id, canonical_name FROM models_canonical 
WHERE lower(canonical_name) = 'minimax-m2.7';

-- 假设 canonical_id = 123
INSERT INTO model_aliases (raw_name, canonical_id, status)
VALUES ('minimax-m2.7-quickspeed', 123, 'active');
```

**方案2: 客户端修正**
- 将客户端请求的模型名从 `minimax-m2.7-quickspeed` 改为 `minimax-m2.7-highspeed` 或 `minimax-m2.7`

**方案3: 添加model_offer**
```sql
-- 如果 quickspeed 是真实的不同模型，需要添加 offer
INSERT INTO model_offers (credential_id, raw_model_name, standardized_name, ...)
SELECT credential_id, 'minimax-m2.7-quickspeed', 'minimax-m2.7-quickspeed', ...
FROM model_offers WHERE raw_model_name = 'MiniMax-M2.7-highspeed';
```

### 影响范围
- 9次请求失败（过去24小时）
- 错误率: 9/189 = 4.76%
- 用户看到: HTTP 503 Service Unavailable

---

## 🔍 问题3: minimax-m3 空响应 (P1) ⚠️

### 现象
过去24小时内，14次 `empty_response` 错误，全部来自 `minimax-m3`：

```sql
SELECT client_model, COUNT(*) as count 
FROM request_logs 
WHERE ts >= NOW() - INTERVAL '24 hours' AND error_kind = 'empty_response' 
GROUP BY client_model;

 client_model | count 
--------------+-------
 minimax-m3   |    14
```

### 详细分析

查看最近3条错误记录：

```
request_id: 5255b2d0934ea31119c175de1dc389d6
ts: 2026-06-30 06:41:12
outbound_model: minimaxai/minimax-m3
credential_id: 19
provider_id: 18
prompt_tokens: 71979
completion_tokens: 0          ← 问题：0个输出token
stream_chunk_count: 3         ← 但收到了3个chunk
stream_chunks_sent: 0         ← 但没有发送给客户端
upstream_finish_reason: NULL  ← 上游没有返回finish_reason
```

### 根因推测

**可能原因1**: 上游返回了3个SSE事件，但没有实际内容
- chunk 1: `data: {"choices": []}`
- chunk 2: `data: {"choices": []}`
- chunk 3: `data: [DONE]`

**可能原因2**: 质量过滤器过滤掉了所有内容
- 代码位置: `domains/streaming/stream.go`
- 检查 `quality_flags` 和 `quality_fix_actions` 字段

**可能原因3**: 上游模型确实返回空响应（上下文过长或其他原因）
- `prompt_tokens: 71979` (约72K tokens)
- 可能超过了模型的有效上下文窗口

### 诊断步骤

1. **检查quality_flags**:
   ```sql
   SELECT request_id, quality_flags, quality_fix_actions 
   FROM request_logs 
   WHERE request_id = '5255b2d0934ea31119c175de1dc389d6';
   ```

2. **检查request_body大小**:
   ```sql
   SELECT request_id, 
          jsonb_array_length(request_body->'messages') as msg_count,
          length(request_body::text) as body_size
   FROM request_logs 
   WHERE request_id = '5255b2d0934ea31119c175de1dc389d6';
   ```

3. **检查凭据19的健康状态**:
   ```sql
   SELECT id, availability_state, circuit_state, quota_state, consecutive_failures
   FROM credentials WHERE id = 19;
   ```

### 修复建议

**短期**:
1. 检查该凭据的配置（base_url、API key）
2. 查看上游提供商的错误日志
3. 如果是上下文过长，启用自动压缩（已有fallback机制）

**中期**:
1. 添加空响应重试逻辑（使用不同凭据）
2. 在 `empty_response` 情况下记录更详细的诊断信息（实际chunk内容）

**长期**:
1. 实现智能上下文窗口检测（在发送前验证）
2. 添加模型健康度评分（空响应率 > 10% 时降权）

---

## 🔍 问题4: 字节数统计缺失 (P2) ⚠️

### 现状

**request_logs表中的统计字段**:
- ✅ `prompt_tokens` - 输入token数
- ✅ `completion_tokens` - 输出token数
- ✅ `cache_read_tokens` - 缓存读取token数
- ✅ `cache_write_tokens` - 缓存写入token数
- ❌ **无** `input_bytes` - 输入字节数
- ❌ **无** `output_bytes` - 输出字节数
- ❌ **无** `cache_read_bytes` - 缓存读取字节数
- ❌ **无** `cache_write_bytes` - 缓存写入字节数

### 影响

1. **成本分析不精确**
   - 某些提供商按字节计费（而非token）
   - 无法计算网络传输成本

2. **性能分析受限**
   - 无法分析带宽使用情况
   - 无法优化大响应的传输

3. **审计不完整**
   - 无法验证上游计费的字节数
   - 无法追溯流量异常

### 实现方案

**方案1: 在流式处理中添加字节计数器**

```go
// domains/streaming/stream.go

type byteCountingWriter struct {
    w            http.ResponseWriter
    bytesWritten int64
}

func (bcw *byteCountingWriter) Write(p []byte) (int, error) {
    n, err := bcw.w.Write(p)
    atomic.AddInt64(&bcw.bytesWritten, int64(n))
    return n, err
}

func (bcw *byteCountingWriter) BytesWritten() int64 {
    return atomic.LoadInt64(&bcw.bytesWritten)
}

// 在 StreamChatWithPendingCapture 中使用
func StreamChatWithPendingCapture(...) (outcome StreamOutcome) {
    bcw := &byteCountingWriter{w: w}
    
    // ... 流式写入到 bcw ...
    
    // 记录字节数
    if capture != nil {
        capture.OutputBytes = bcw.BytesWritten()
    }
    
    return outcome
}
```

**方案2: 在审计捕获中添加字节统计**

```go
// domains/hooks/audit/stream_capture.go

type StreamCapture struct {
    // ... 现有字段 ...
    InputBytes  int64
    OutputBytes int64
}

func (sc *StreamCapture) RecordChunk(chunk []byte) {
    sc.OutputBytes += int64(len(chunk))
    sc.chunks = append(sc.chunks, chunk)
}

// domains/hooks/observability/telemetry/telemetry_service.go

func (t *TelemetryService) EmitRequestComplete(..., inputBytes, outputBytes int64) {
    // 记录到 request_logs
    // 注意：需要先添加表字段
}
```

**方案3: 数据库Schema变更**

```sql
-- 添加字节数字段
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS input_bytes BIGINT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS output_bytes BIGINT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS cache_read_bytes BIGINT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS cache_write_bytes BIGINT;

-- 添加索引（用于成本分析查询）
CREATE INDEX IF NOT EXISTS idx_request_logs_bytes_cost 
    ON request_logs (tenant_id, ts DESC) 
    WHERE output_bytes > 1000000; -- 仅索引大响应

-- 创建字节数统计视图
CREATE OR REPLACE VIEW v_request_bytes_stats AS
SELECT 
    tenant_id,
    date_trunc('day', ts) as day,
    COUNT(*) as request_count,
    SUM(input_bytes) as total_input_bytes,
    SUM(output_bytes) as total_output_bytes,
    AVG(output_bytes) as avg_output_bytes,
    PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY output_bytes) as p95_output_bytes
FROM request_logs
WHERE ts >= NOW() - INTERVAL '30 days'
GROUP BY tenant_id, date_trunc('day', ts);
```

### 实施优先级

**阶段1** (本周):
1. 添加数据库字段（ALTER TABLE）
2. 在流式处理中添加 `byteCountingWriter`

**阶段2** (本月):
3. 在非流式处理中添加字节统计（`io.ReadAll` 返回值的 `len()`）
4. 回填历史数据（基于 `request_body` 和 `response_body` 的 `length()`）

**阶段3** (下季度):
5. 创建字节数成本分析仪表板
6. 添加按字节计费的提供商支持

---

## 📊 错误统计分析（过去24小时）

### 总体情况
```
总请求数: 189
成功: 154 (81.48%)
失败: 35 (18.52%)
```

### 错误类型分布

| 错误类型 | 失败阶段 | 详细代码 | 次数 | 占比 |
|---------|---------|---------|------|------|
| empty_response | upstream_empty_response | zero_tokens_few_chunks | 14 | 40.0% |
| no_candidate | gateway | gw_no_candidate | 9 | 25.7% |
| missing_model | gateway | gw_missing_model | 4 | 11.4% |
| canceled | upstream | canceled | 3 | 8.6% |
| transient | upstream | transient | 2 | 5.7% |
| invalid_key | gateway | gw_invalid_key | 1 | 2.9% |
| provider_error | upstream | provider_error | 1 | 2.9% |
| (NULL) | (NULL) | (NULL) | 1 | 2.9% |

### 关键发现

1. **empty_response 是最大问题** (40%)
   - 全部来自 minimax-m3
   - 上游返回3个chunk但0个token
   - 需要与提供商排查

2. **no_candidate 已有明确修复路径** (25.7%)
   - 全部来自 minimax-m2.7-quickspeed
   - 添加别名即可解决

3. **missing_model 需要进一步分析** (11.4%)
   - 查询是哪些模型：
   ```sql
   SELECT client_model, COUNT(*) 
   FROM request_logs 
   WHERE error_kind = 'missing_model' 
   GROUP BY client_model;
   ```

---

## 🔧 其他发现

### 1. 缓存使用情况

过去24小时：
- 总请求: 189
- 使用缓存的请求: 7 (3.7%)
- 缓存读取token数: 1008
- 缓存写入token数: 0

**建议**: 缓存使用率偏低，检查缓存配置。

### 2. request_body/response_body完整性

- 所有请求都有 `request_body` (189/189)
- 成功请求都有 `response_body` (154/154)
- 失败请求没有 `response_body` (0/35)

**结论**: 数据完整性良好。

### 3. 数据库配置

**连接信息**:
```
Container: llm-gateway-pg-71-replica (Citus 11.3.0)
Database: llm_gateway (NOT crm)
User: llm_gateway
Port: 5432 (容器内)
Max Connections: 1000
```

**注意**: 初次审计错误地查询了 `crm` 数据库，实际应用使用的是 `llm_gateway` 数据库。

---

## ⚡ 立即执行清单

### ✅ 已完成
- [x] 创建 `request_wal_2026_06` 分区
- [x] 验证错误日志停止

### ⚠️ 待执行（按优先级）

#### P0 - 今天必须完成

1. **添加 minimax-m2.7-quickspeed 别名** (10分钟)
   ```bash
   ./scripts/add-model-alias.sh minimax-m2.7-quickspeed minimax-m2.7
   ```

2. **诊断 minimax-m3 空响应** (30分钟)
   ```bash
   ./scripts/diagnose-empty-response.sh minimax-m3 19
   ```

#### P1 - 本周完成

3. **添加字节数统计字段** (1小时)
   ```sql
   -- 在 llm_gateway 数据库执行
   ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS input_bytes BIGINT;
   ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS output_bytes BIGINT;
   ```

4. **实现byteCountingWriter** (2小时)
   - 修改 `domains/streaming/stream.go`
   - 添加单元测试

5. **添加分区自动创建函数** (1小时)
   - 创建 `ensure_request_wal_partition()`
   - 添加cron定时任务

#### P2 - 本月完成

6. **分析 missing_model 错误**
7. **优化缓存使用率**
8. **添加空响应重试逻辑**

---

## 📈 监控建议

### 新增告警规则

```yaml
groups:
  - name: llm_gateway_alerts
    rules:
      # 1. 分区缺失告警
      - alert: RequestWALPartitionMissing
        expr: |
          (
            time() - (
              max(
                extract(epoch from pg_stat_get_last_analyze_time(c.oid))
              ) by (schemaname, tablename)
              from pg_class c
              join pg_namespace n on n.oid = c.relnamespace
              where c.relname like 'request_wal_%'
            )
          ) > 86400
        for: 1h
        labels:
          severity: critical
        annotations:
          summary: "request_wal可能缺少下月分区"
          
      # 2. empty_response错误率
      - alert: HighEmptyResponseRate
        expr: |
          (
            sum(rate(llmgw_requests_total{error_kind="empty_response"}[5m]))
            /
            sum(rate(llmgw_requests_total[5m]))
          ) > 0.1
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "empty_response错误率超过10%"
          
      # 3. no_candidate错误
      - alert: ModelNoCandidateError
        expr: |
          rate(llmgw_requests_total{error_kind="no_candidate"}[5m]) > 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "检测到no_candidate错误，可能是模型别名缺失"
```

### 仪表板指标

1. **错误率趋势**
   ```promql
   sum by (error_kind) (rate(llmgw_requests_total{success="false"}[5m]))
   ```

2. **分区大小监控**
   ```sql
   SELECT 
       schemaname, 
       tablename, 
       pg_size_pretty(pg_total_relation_size(schemaname||'.'||tablename)) as size
   FROM pg_tables 
   WHERE tablename LIKE 'request_%_2026_%'
   ORDER BY pg_total_relation_size(schemaname||'.'||tablename) DESC;
   ```

3. **缓存命中率**
   ```promql
   sum(rate(llmgw_cache_read_tokens_total[5m])) 
   / 
   sum(rate(llmgw_prompt_tokens_total[5m]))
   ```

---

## 🎯 下一步行动

### 今天（2026-06-30）

- [x] 修复 request_wal 分区问题 ✅
- [ ] 添加 minimax-m2.7-quickspeed 别名
- [ ] 诊断 minimax-m3 空响应问题

### 本周

- [ ] 实现字节数统计功能
- [ ] 添加分区自动创建
- [ ] 分析 missing_model 错误

### 本月

- [ ] 优化缓存使用率
- [ ] 实现空响应重试逻辑
- [ ] 添加模型健康度评分

---

## 附录A: 关键SQL查询

### 1. 查看所有minimax模型
```sql
SELECT mo.raw_model_name, mo.standardized_name, COUNT(*) as offer_count
FROM model_offers mo
WHERE lower(mo.raw_model_name) LIKE '%minimax%' 
   OR lower(mo.standardized_name) LIKE '%minimax%'
GROUP BY mo.raw_model_name, mo.standardized_name;
```

### 2. 查看特定模型的候选节点
```sql
SELECT c.id, p.catalog_code, c.availability_state, c.circuit_state
FROM model_offers mo
JOIN credentials c ON c.id = mo.credential_id
JOIN providers p ON p.id = c.provider_id
WHERE lower(mo.raw_model_name) = 'minimax-m3';
```

### 3. 查看最近的错误请求
```sql
SELECT request_id, ts, client_model, error_kind, failure_detail_code
FROM request_logs
WHERE success = false
  AND ts >= NOW() - INTERVAL '1 hour'
ORDER BY ts DESC
LIMIT 20;
```

---

## 附录B: 修正说明

### 初次审计的错误

1. **数据库名称错误**
   - 错误: 查询了 `crm` 数据库
   - 正确: 应查询 `llm_gateway` 数据库
   - 原因: `llm-gateway-go` 的 `DATABASE_URL` 指向 `llm_gateway` 数据库

2. **表不存在的结论错误**
   - 错误: 认为 `request_wal` 和 `request_logs` 完全不存在
   - 正确: 表存在，但 `request_wal` 缺少6月分区

3. **根因链错误**
   - 错误: 认为 P0-1 (表缺失) 导致 P0-2 (路由失败)
   - 正确: P0-2 是独立的模型别名问题，与分区无关

### 修正后的结论

- ✅ `request_logs` 表完整，有189条记录
- ✅ `request_wal` 表存在，但缺少6月分区（已修复）
- ⚠️ `minimax-m2.7-quickspeed` 模型别名不存在（待修复）
- ⚠️ `minimax-m3` 存在空响应问题（待诊断）

---

## 审计人员
- AI Agent (Claude Opus 4)
- 初次审计时间: 2026-06-30 14:00-15:30 CST
- 修正审计时间: 2026-06-30 15:30-16:30 CST
- 服务器: __HOST_71_IP__ (71)
- 数据库: llm_gateway @ llm-gateway-pg-71-replica
