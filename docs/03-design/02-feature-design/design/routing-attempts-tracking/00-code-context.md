# 路由尝试追踪增强 - 代码上下文

## 问题背景

### 用户痛点
用户在监控面板看到：
- 供应商泳道：显示"火山方舟 普通版"
- 请求链路详情上游：显示 `https://integrate.api.nvidia.com/v1`

产生困惑：为什么供应商和 upstream URL 不一致？

### 根因
系统存在**路由回退机制**，会尝试多个候选供应商：
1. 计划候选序列（来自 routing_decision_log）
2. 实际执行时逐个尝试（在 executor_chat.go）
3. 最终只记录**成功的候选**或**最后失败的候选**到 request_logs_hot
4. **中间尝试的详情丢失**

案例 `f50149e5cbc576295141145ba05f7ef2`:
- 计划了 5 个候选（包括 provider_id=18 NVIDIA）
- 实际尝试了其中几个
- 最终记录 provider_id=35 (火山方舟)
- 用户看到的 NVIDIA URL 是某次尝试留下的痕迹，但未持久化

## 核心数据证据

### routing_decision_log_hot 记录
```
request_id: f50149e5-cbc5-7629-5141-145ba05f7ef2
model: glm-5.2
chosen_provider_id: NULL
chosen_credential_id: NULL
candidates_tried: 5
success: false
error_class: model_not_found
failure_stage: NULL
failure_detail_code: model_not_found
decision_trace: {
  "failure_reason": "sync_retry_exhausted",
  "planned_candidates": [
    {"tier": 2, "raw_model": "glm-5.2", "provider_id": 35, "credential_id": 12},
    {"tier": 2, "raw_model": "glm-5-2-260617", "provider_id": 34, "credential_id": 11},
    {"tier": 2, "raw_model": "glm-5.2", "provider_id": 35, "credential_id": 12},
    {"tier": 2, "raw_model": "z-ai/glm-5.2", "provider_id": 18, "credential_id": 23},
    {"tier": 2, "raw_model": "glm-5.2", "provider_id": 24, "credential_id": 25},
    ...
  ]
}
```

### request_logs_hot 记录
```
request_id: f50149e5cbc576295141145ba05f7ef2
provider_id: 35 (火山方舟 普通版)
credential_id: 12
client_model: glm-5.2
success: false
error_kind: canceled
failure_stage: upstream
failure_detail_code: canceled
latency_ms: 120193
upstream_status_code: NULL
upstream_endpoint: NULL
```

### 供应商信息
- Provider 35: 火山方舟 普通版, base_url=https://ark.cn-beijing.volces.com/api/v3
- Provider 18: NVIDIA NIM, base_url=https://integrate.api.nvidia.com/v1

## 目标

**增强可观测性**：完整记录每次路由尝试的详情，包括：
- 尝试序号
- 供应商 ID/名称
- 凭据 ID
- 原始模型名
- 上游 URL
- 结果（成功/取消/超时/404/错误）
- 耗时
- HTTP 状态码
- 错误消息

**展示方式**：
- 数据库：新增 `routing_attempts` (JSONB) 和 `routing_summary` (TEXT) 字段
- 前端：时间线展示，清晰显示"尝试路径"

## 涉及模块

### 1. executor_chat.go (执行层)
- 路径: `domains/streaming/executors/executor_chat.go`
- 职责: 实际发起 HTTP 请求到上游供应商
- 当前行为: 有 `attemptLog` 日志记录（行 472-499），但仅写到 slog，未持久化到数据库
- **改动**: 增加 RoutingAttemptsTracker，记录到请求上下文

### 2. telemetry/client.go (观测层)
- 路径: `domains/hooks/observability/telemetry/client.go`
- 职责: 写入 request_logs_hot 表
- 当前行为: INSERT 83 个字段（行 710-793）
- **改动**: 增加 routing_attempts ($84) 和 routing_summary ($85)

### 3. request_logs_hot 表 (存储层)
- 路径: `deploy/sql/objects/tables/request_logs.sql` + hot 表
- 当前字段: 已有 provider_id, credential_id, upstream_status_code 等
- **改动**: 新增 routing_attempts (JSONB), routing_summary (TEXT)

### 4. 前端展示 (UI 层)
- 路径: 前端项目 (pms-web / llm-gateway-admin)
- 当前: 请求详情页展示单一 provider_id
- **改动**: 新增"路由决策"卡片，时间线展示所有尝试

## 设计约束

### 1. 性能
- routing_attempts 可能很大（每个候选 200-300 字节，10 个候选 = 3KB）
- 只在失败请求或多次尝试时才有价值
- **优化**: 成功且首次尝试成功的请求，routing_attempts = null

### 2. 兼容性
- 新字段必须 nullable，不影响现有数据
- 前端需要兼容旧数据（routing_attempts 为 null）

### 3. 存储
- JSONB 支持索引和查询
- routing_summary 为纯文本，便于快速浏览

## 相关代码位置

```
domains/streaming/executors/executor_chat.go:472-499    # attemptLog 函数
domains/hooks/observability/telemetry/client.go:710-793 # INSERT INTO request_logs_hot
deploy/sql/objects/tables/request_logs.sql               # 表定义
```

## 数据示例

### routing_attempts (JSONB)
```json
{
  "attempts": [
    {
      "seq": 1,
      "provider_id": 35,
      "provider_name": "火山方舟 普通版",
      "credential_id": 12,
      "raw_model": "glm-5.2",
      "upstream_url": "https://ark.cn-beijing.volces.com/api/v3/chat/completions",
      "result": "model_not_found",
      "latency_ms": 1523,
      "http_status": 404
    },
    {
      "seq": 2,
      "provider_id": 18,
      "provider_name": "NVIDIA NIM",
      "credential_id": 23,
      "raw_model": "z-ai/glm-5.2",
      "upstream_url": "https://integrate.api.nvidia.com/v1/chat/completions",
      "result": "canceled",
      "latency_ms": 120000
    }
  ]
}
```

### routing_summary (TEXT)
```
候选1: 火山方舟(35) 模型未找到 1.5s → 候选2: NVIDIA(18) 取消 120s
```

## 验证方案

### 单元测试
- `executor_chat_test.go`: 模拟多次尝试，验证 tracker 累积
- `client_test.go`: 验证 JSONB 序列化正确

### 集成测试
- 构造 5 个候选，前 4 个失败，第 5 个成功
- 查询 request_logs_hot，验证 routing_attempts 包含 5 条记录

### 手动测试
- 发起一个请求到不存在的模型
- 触发路由回退
- 前端查看"路由决策"卡片

## 回滚方案

### 数据库回滚
```sql
ALTER TABLE request_logs_hot DROP COLUMN IF EXISTS routing_attempts;
ALTER TABLE request_logs_hot DROP COLUMN IF EXISTS routing_summary;
ALTER TABLE request_logs DROP COLUMN IF EXISTS routing_attempts;
ALTER TABLE request_logs DROP COLUMN IF EXISTS routing_summary;
```

### 代码回滚
- Git revert 对应 commit
- 前端移除"路由决策"卡片组件

## 实施优先级

- P0: 数据库迁移 + executor_chat.go tracker
- P1: telemetry/client.go 写入
- P2: 前端展示

## 预期收益

1. **消除用户困惑**: 清晰看到每次路由尝试和失败原因
2. **加速问题诊断**: 快速定位是哪个供应商/凭据出问题
3. **优化路由策略**: 分析哪些候选经常失败，调整优先级
