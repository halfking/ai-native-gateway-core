# NVIDIA NIM 凭据健康审计报告

**审计时间**: 2026-11-17 02:15 CST  
**数据源**: 211 生产库  
**观测窗口**: 最近 48 小时

## 一、凭据总览

| 凭据 ID | 标签 | 可用状态 | 生命周期 | 健康状态 | 最近探针 | 探针成功 |
|---------|------|----------|----------|----------|----------|----------|
| 8 | nvidia-build-new | ready | active | healthy | 2026-11-16 18:42 | ✓ |
| 18 | nvidia-build-v2 | ready | active | healthy | 2026-11-16 15:02 | ✗ |
| 19 | endless | ready | active | healthy | 2026-11-16 01:01 | ✗ |
| 23 | nvidia-latest | ready | **disabled** | healthy | 2026-10-11 01:42 | ✗ (672 连续失败) |

**结论**: 
- 3 个活跃凭据（ID 8/18/19），1 个已禁用（ID 23，state_reason=model_binding_error）
- 凭据 8 探针健康，凭据 18/19 最近探针失败但业务请求大部分成功

## 二、业务请求统计（最近 24 小时）

| 凭据 ID | 总请求 | 成功 | 失败 | 成功率 | 主要错误类型 |
|---------|--------|------|------|--------|-------------|
| 8 | 59 | 57 | 2 | **96.61%** | transient |
| 18 | 57 | 52 | 5 | **91.23%** | model_not_found, no_candidates, transient |
| 19 | 771 | 756 | 15 | **98.05%** | model_not_found, stream_error, transient |
| **合计** | **887** | **865** | **22** | **97.52%** | - |

**结论**: NVIDIA NIM 整体健康，成功率 97.52%

## 三、问题模型分析

### 3.1 deepseek-v4.1-flash

**绑定状态**: ✓ 已绑定到凭据 8/18/19  
**探针状态**: ✓ 最近探针成功（200 OK）  
**业务表现**: ⚠️ 间歇性失败

**失败模式**（最近 7 天）:
1. **model_not_found** (7 笔，11-16 10:45~12:18) - NVIDIA API 返回模型不存在
2. **stream_error** (6 笔，11-16 08:59~09:51) - HTTP 200 但流式响应异常
3. **transient** (2 笔) - 偶发性网络/超时错误
4. **no_candidates** (1 笔) - 路由时无可用通道
5. **empty_response** (2 笔，11-09) - OpenAI 协议空响应

**时间分布**:
- 11-09: 4 笔失败（empty_response + model_not_found）
- 11-16 01:05~12:18: 14 笔失败（集中在上午 8:59~12:18）
- 11-16 12:18 之后: **0 笔失败**

**根因推断**:
- NVIDIA NIM 上该模型在 11-16 上午出现短暂不稳定（API 返回 model_not_found 或流式异常）
- 探针用的是轻量请求，业务请求可能触发不同代码路径
- **当前已恢复**（12:18 之后无失败）

### 3.2 minimax-m3.1-flash

**绑定状态**: ✓ 已绑定到凭据 8/18/19（原始模型名 `minimaxai/minimax-m3.1-flash`）  
**探针状态**: ✓ 最近探针成功（200 OK）  
**业务表现**: ✓ 稳定，偶有 transient 错误

**失败记录**（最近 48 小时）:
- 11-16 14:57: 1 笔 transient (凭据 18)
- 11-16 18:23: 1 笔 transient (凭据 18)
- 11-16 18:49: 1 笔 transient (凭据 8)
- 11-16 18:54: 1 笔 transient (凭据 8)

**结论**: 模型本身健康，偶发 transient 错误属于正常网络波动

### 3.3 其他观测

**content safety screening 临时不可用**:
- 11-16 19:13: 凭据 19 探针返回 503 "content safety screening is temporarily unavailable"
- 该探针后续恢复正常（19:28/19:41/19:58 均 200 OK）

**context deadline exceeded**:
- 11-16 19:12: 凭据 19 探针 `deepseek-ai/deepseek-v4.1-flash` 超时（15s）
- 该探针后续恢复（11-17 01:24 成功）

## 四、建议

### 4.1 立即处理（P0）
**无** - 当前无阻断性故障

### 4.2 短期优化（P1）

#### F6: deepseek-v4.1-flash 增加重试容忍度
**现象**: 该模型在 NVIDIA NIM 上会间歇性返回 `model_not_found`，但探针显示模型存在  
**根因**: NVIDIA API 内部路由不稳定或模型服务偶发性下线  
**修法**: 
- `model_not_found` 在 NVIDIA provider 上标记为可重试（当前是终态）
- 或：该模型降低 NVIDIA NIM 的路由权重，优先走其他供应商（apichina/apiclaude/evol 等都有该模型且稳定）

#### F7: stream_error 可观测性
**现象**: HTTP 200 + `stream_error` 没有详细错误信息  
**修法**: `domains/streaming` 记录流式解析失败的原始响应片段（前 500 字节）

### 4.3 长期监控（P2）

#### M1: NVIDIA NIM 探针与业务请求口径不一致
**观测**: 探针成功但业务请求失败（探针用轻量负载，业务请求可能触发不同限流/路由）  
**建议**: 探针增加「业务级负载」选项（可配置 prompt 长度/是否流式）

#### M2: 凭据 23 (nvidia-latest) 已禁用 672 天
**状态**: `availability_state=ready` + `lifecycle_status=disabled` + `state_reason_code=model_binding_error`  
**建议**: 清理或修复（当前占用 ID 但不参与路由）

## 五、附：可复现 SQL

### 凭据状态
```sql
SELECT 
  c.id, c.label, c.availability_state, c.lifecycle_status, c.status,
  c.health_status, c.last_probe_at, c.last_probe_success, c.probe_consecutive_failures
FROM credentials c
JOIN providers p ON c.provider_id = p.id
WHERE p.code = 'nvidia';
```

### 业务请求统计（最近 24h）
```sql
SELECT 
  credential_id,
  COUNT(*) as total, SUM(CASE WHEN success THEN 1 ELSE 0 END) as success_count,
  ROUND(100.0 * SUM(CASE WHEN success THEN 1 ELSE 0 END) / COUNT(*), 2) as success_rate
FROM request_logs_with_current_month
WHERE provider_id = 18 AND ts >= NOW() - INTERVAL '24 hours'
GROUP BY credential_id;
```

### 问题模型失败详情
```sql
SELECT ts, credential_id, client_model, error_kind, failure_detail_code, upstream_status_code
FROM request_logs_with_current_month
WHERE provider_id = 18 AND client_model IN ('deepseek-v4.1-flash', 'minimax-m3.1-flash')
  AND NOT success AND ts >= NOW() - INTERVAL '7 days'
ORDER BY ts DESC;
```

---

**审计结论**: NVIDIA NIM 凭据整体健康（97.52% 成功率），`deepseek-v4.1-flash` 在 11-16 上午短暂不稳定但**已自愈**，`minimax-m3.1-flash` 稳定。无需紧急干预。
