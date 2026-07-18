# request_logs Schema v2 设计文档

> **版本**: v2.0  
> **日期**: 2026-07-18  
> **状态**: 📋 待审查  
> **目的**: 整合四个优化方向的字段需求，统一扩展 request_logs 表

---

## 1. 背景

`request_logs` 是 LLM Gateway 的核心日志表，记录每个 API 请求的完整生命周期。四个优化方向（价格/基础/号池/衰减）均需要在此表中新增字段以支持其功能。

---

## 2. 四个方向的字段需求汇总

### 2.1 价格优化 (Phase 2) - 5 个字段

| 字段 | 类型 | 说明 |
|------|------|------|
| `compute_pool_id` | INT | 使用的 GPU 资源池 ID |
| `pricing_tier` | TEXT | 定价层级 (reserved / on_demand / spot) |
| `cost_breakdown` | JSONB | 成本分解 `{gpu, memory, network, shared}` |
| `unit_price_usd` | NUMERIC(12,6) | 单价 (USD/token) |
| `total_cost_usd` | NUMERIC(12,6) | 总成本 (USD) |

### 2.2 基础优化 (Phase 1) - 6 个字段

| 字段 | 类型 | 说明 |
|------|------|------|
| `content_safety_score` | NUMERIC(3,2) | 内容安全评分 (0.00-1.00) |
| `sensitive_words_matched` | TEXT[] | 匹配的敏感词列表 |
| `circuit_breaker_triggered` | BOOLEAN | 是否触发熔断 |
| `circuit_breaker_reason` | TEXT | 熔断原因 |
| `adapter_version` | TEXT | Unified Adapter 版本 |
| `upstream_provider` | TEXT | 实际调用的上游提供商 |

### 2.3 号池优化 (Phase 1) - 5 个字段

| 字段 | 类型 | 说明 |
|------|------|------|
| `credential_pool_id` | INT | 使用的凭据池 ID |
| `credential_id` | INT | 具体使用的凭据 ID |
| `credential_pool_strategy` | TEXT | 调度策略 (round_robin / wrr / least_loaded) |
| `credential_wait_time_ms` | INT | 等待凭据的时间 (ms) |
| `credential_reuse_count` | INT | 凭据当前复用次数 |

### 2.4 衰减优化 (Phase 3) - 6 个字段

| 字段 | 类型 | 说明 |
|------|------|------|
| `network_latency_ms` | INT | 网络往返延迟 (不含处理时间) |
| `streaming_first_token_ms` | INT | 流式响应首 token 延迟 (ms) |
| `edge_node_id` | TEXT | 边缘节点 ID |
| `connection_reused` | BOOLEAN | 是否复用 HTTP 连接 |
| `protocol_version` | TEXT | HTTP 协议版本 (http/1.1 / h2 / h3) |
| `tls_version` | TEXT | TLS 版本 (TLS 1.2 / TLS 1.3) |

---

## 3. 分阶段迁移方案

### Phase 1 (Week 2-5): 基础优化 + 号池优化

```sql
-- deploy/sql/migrations/V002__add_phase1_columns.sql
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS content_safety_score NUMERIC(3,2);
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS sensitive_words_matched TEXT[];
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS circuit_breaker_triggered BOOLEAN DEFAULT FALSE;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS circuit_breaker_reason TEXT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS adapter_version TEXT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS upstream_provider TEXT;

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS credential_pool_id INT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS credential_id INT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS credential_pool_strategy TEXT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS credential_wait_time_ms INT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS credential_reuse_count INT;
```

### Phase 2 (Week 6-15): 价格优化

```sql
-- deploy/sql/migrations/V003__add_phase2_columns.sql
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS compute_pool_id INT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS pricing_tier TEXT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS cost_breakdown JSONB;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS unit_price_usd NUMERIC(12,6);
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS total_cost_usd NUMERIC(12,6);
```

### Phase 3 (Week 16+): 衰减优化

```sql
-- deploy/sql/migrations/V004__add_phase3_columns.sql
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS network_latency_ms INT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS streaming_first_token_ms INT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS edge_node_id TEXT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS connection_reused BOOLEAN;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS protocol_version TEXT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS tls_version TEXT;
```

---

## 4. 索引策略

按需创建索引（在各 Phase 实施时添加）：

```sql
-- 价格优化索引
CREATE INDEX idx_request_logs_compute_pool ON request_logs(compute_pool_id) WHERE compute_pool_id IS NOT NULL;
CREATE INDEX idx_request_logs_cost_date ON request_logs(DATE(created_at), tenant_id);

-- 基础优化索引
CREATE INDEX idx_request_logs_circuit_breaker ON request_logs(circuit_breaker_triggered) WHERE circuit_breaker_triggered = TRUE;

-- 号池优化索引
CREATE INDEX idx_request_logs_credential_pool ON request_logs(credential_pool_id) WHERE credential_pool_id IS NOT NULL;

-- 衰减优化索引
CREATE INDEX idx_request_logs_network_latency ON request_logs(network_latency_ms) WHERE network_latency_ms IS NOT NULL;
```

---

## 5. 相关文档

- [Phase 0 实施计划](Phase0-实施计划.md)
- [Prometheus Metrics 命名规范](prometheus_metrics_naming.md)
- [价格优化设计](价格优化/02-动态价格引擎-设计.md)

---

**作者**: Infrastructure Team  
**下次复审**: Phase 1 开始前 (2026-07-22)
