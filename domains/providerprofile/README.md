# 供应商画像系统集成指南

## 概述

供应商画像系统通过7个维度持续监控和评估供应商质量，为智能路由和供应商管理提供数据支持。

## Phase 1 完成情况

✅ **基础设施层** (2026-07-26)
- 数据模型和存储接口
- 4维度评分算法（网络延迟、可用性、稳定性、规模）
- 轻量级采集器（并发）
- 每日聚合器（时段分析）
- 定时任务调度器
- PostgreSQL存储实现
- 网关适配器
- 后台任务包装器

## Phase 2 完成情况

✅ **告警与自动处理** (2026-07-26)
- 告警类型/级别/配置（`AlertConfig`，5种告警类型）
- 纯规则引擎 `EvaluateAlerts`（score_drop / trend_drop / dimension_low / auto_disabled / auto_enabled）
- `PGCredentialActor`：通过 `lifecycle_status` + `auto_*` 审计列实现禁用/启用，白名单检查，`provider_events` 记录
- `PGAlertStore`：按 (credential, date, type) 去重的告警持久化
- `AlertEngine`：评估→执行→持久化的编排器（白名单抑制禁用、`manual_disabled` 阻止自动恢复）
- `PGProfileSource` + 修复 `pg_profile_store` 的 NULL 安全扫描（可空 score/timeslot 列）
- 聚合器首次运行修复：启动即聚合当天，不再等到第2天才有 `provider_profile_daily` 数据
- `ProfileAlertWorker`：第4个后台任务，每日评估活跃凭证（禁用候选）+ 已自动禁用凭证（恢复候选）
- 端到端集成测试：自动禁用→恢复→启用完整循环 + 白名单抑制

## 集成步骤

### 1. 部署数据库（必需）

在目标数据库执行迁移脚本：

```bash
# 252服务器
psql -h 192.168.1.252 -U postgres -d llm_gateway \
  -f deploy/sql/migrations/2026-07-26-provider-profile-system.sql

# 本地Docker
cat deploy/sql/migrations/2026-07-26-provider-profile-system.sql | \
  docker exec -i distribution-bc-pg17 psql -U maintain -d llm_gateway
```

**验证部署**：
```sql
SELECT tablename FROM pg_tables 
WHERE schemaname = 'public' AND tablename LIKE 'provider_profile%'
ORDER BY tablename;
```

应该看到6个表：
- `provider_cost_reconciliation`
- `provider_credibility_tests`
- `provider_profile_alerts`
- `provider_profile_daily`
- `provider_profile_metrics`
- `provider_profile_whitelist`

### 2. 启用系统（配置）

在平台配置中添加：

```yaml
provider_profile:
  enabled: true
  collection_interval: 2h      # 轻量级采集频率
  aggregation_interval: 24h    # 每日聚合频率
  cleanup_interval: 168h       # 清理频率（7天）
```

### 3. 集成到网关（代码）

在 `cmd/gateway/main.go` 中添加：

**启动阶段**（dbConn != nil 块内）：
```go
// Provider Profile System (Phase 1, 2026-07-26)
var profileWorkers *ProviderProfileWorkers
if dbConn != nil {
    profileWorkers = initProviderProfile(dbConn.Pool())
}
```

**关闭阶段**（shutdown 序列中）：
```go
// Stop provider profile workers
stopProviderProfile(profileWorkers)
```

### 4. 适配器实现（已完成）

`domains/providerprofile/adapters.go` 已针对真实表结构实现并验证：

- `GatewayNetworkProber`: 解密 credential 密钥后，对 provider 的
  `/v1/models` 端点发起真实 GET 探测（复用 `internal/providercap` 的
  URL 解析和鉴权头逻辑，与 `bg/credential_probe_v2.go` 一致）。需要
  `fernetKey`/`keyring`（与 `cmd/gateway/main.go` 中派生凭证解密密钥的
  方式相同）。
- `GatewayRequestAnalyzer`: 查询 `request_logs_hot` 表（0-7天热数据），
  使用 `success`/`upstream_status_code`/`stream_first_chunk_ms`/
  `latency_ms`/`ts` 等真实列名（不是 `status_code`/`created_at`）。
- `GatewayScaleProvider`: 查询 `provider_models` 表，使用 `available`
  列（不是 `enabled`）判断模型是否可用。
- `GatewayCredentialLister`: 查询 `credentials` 表，使用
  `status = 'active' AND manual_disabled = false AND lifecycle_status
  = 'active'` 判断凭证是否活跃（该表没有 `enabled` 布尔字段，也没有
  `deleted_at` 软删除字段）。

已通过本地 Docker 数据库的真实查询验证（插入测试数据后运行三个适配器，
结果与预期一致）。

## 架构说明

### 数据流

```
轻量级采集器 (每2小时)
    ↓
provider_profile_metrics (小时级数据，保留7天)
    ↓
每日聚合器 (每天凌晨4点)
    ↓
provider_profile_daily (天级数据，保留365天)
    ↓
API / 前端展示
```

### 后台任务

| 任务 | 频率 | 功能 |
|------|------|------|
| ProfileCollector | 每2小时 | 采集网络延迟、可用性、稳定性、规模指标 |
| ProfileAggregator | 每天（启动时即跑当天） | 聚合数据，计算各维度分数和总分 |
| ProfileCleaner | 每周 | 清理7天前的小时级数据 |
| ProfileAlertWorker | 每天 | 评估告警规则，执行自动禁用/启用（Phase 2） |

## 自动处理（Phase 2）

### 触发规则

| 动作 | 条件 |
|------|------|
| 自动禁用 | 总分 < 40 连续 3 天，**或** 可用性 < 50（立即） |
| 自动恢复 | 总分 ≥ 70 连续 3 天（且未被 `manual_disabled`） |
| 仅告警 | 24h 下降 ≥ 20、7d 下降 ≥ 30、连续 3 天下降累计 ≥ 15、可用性/稳定性 < 60 |

阈值见 `DefaultAlertConfig()`（设计文档 §8.1）。

### 禁用/恢复如何生效

`credentials` 表没有 `enabled` 布尔字段——启用状态由 `lifecycle_status` 表达：
- **禁用**：`lifecycle_status='disabled'`, `availability_state='suspended'`, 记录 `auto_disabled_at`/`auto_disabled_reason`
- **恢复**：`lifecycle_status='active'`, `availability_state='ready'`, 记录 `auto_enabled_at`，清除 `auto_disabled_*`

路由层（`discovery`、`admin`）通过 `lifecycle_status NOT IN ('suspended','retired','disabled')` 过滤，所以翻转为 `disabled` 后该凭证立即不再被选中。

### 白名单

白名单中的供应商**不会被自动禁用**（仍会记录告警，`action_taken='none'`）。白名单**不影响自动恢复**。

```sql
-- 添加白名单
INSERT INTO provider_profile_whitelist (provider_id, reason, added_by)
VALUES (314, '官方OpenAI，关键供应商', 'admin')
ON CONFLICT (provider_id) DO NOTHING;

-- 查看白名单
SELECT * FROM provider_profile_whitelist;
```

### 配置

新增环境变量（在 `provider_profile.enabled=true` 前提下生效）：

| 变量 | 默认 | 说明 |
|------|------|------|
| `LLM_GATEWAY_PROVIDER_PROFILE_ALERT_INTERVAL` | 86400（秒，即每天） | 告警评估周期 |

### 监控告警

```sql
-- 未解决告警
SELECT credential_id, alert_type, alert_level, trigger_date, message, action_taken
FROM provider_profile_alerts
WHERE resolved_at IS NULL
ORDER BY created_at DESC LIMIT 50;

-- 自动禁用/恢复事件
SELECT credential_id, event_kind, payload_json, ts
FROM provider_events
WHERE event_kind IN ('profile_auto_disabled','profile_auto_enabled','profile_alert_suppressed')
ORDER BY ts DESC LIMIT 50;
```

### 注意事项

- 告警去重在应用层实现（`provider_profile_alerts` 无唯一约束），按 (credential_id, trigger_date, alert_type) 去重。
- `manual_disabled=true` 的凭证**永不被自动恢复**（需人工审核后由管理员手动恢复）。
- 告警评估会同时处理活跃凭证（禁用候选）和当前已自动禁用的凭证（恢复候选）。

### 评分维度（Phase 1）

| 维度 | 权重 | 数据源 | 状态 |
|------|------|--------|------|
| 网络延迟 | 10% | /v1/models 探测 | ✅ 已实现 |
| 可用性 | 20% | request_logs 聚合 | ✅ 已实现 |
| 稳定性 | 20% | request_logs 聚合 | ✅ 已实现 |
| 规模 | 5% | provider_models 查询 | ✅ 已实现 |
| 模型可信度 | 15% | 可信度测试套件 | ⏳ Phase 3 |
| 费用准确性 | 15% | 账单对账 | ⏳ Phase 6 |
| 价格 | 15% | 价格配置 | ⏳ Phase 2 |

## 测试

### 单元测试
```bash
go test ./domains/providerprofile -v
```

### 集成测试
```bash
go test ./domains/providerprofile -v -run TestIntegration
```

### 跳过集成测试
```bash
go test ./domains/providerprofile -v -short
```

## 监控

### 查看采集数据
```sql
SELECT credential_id, metric_time, time_slot,
       network_latency_p95, availability_success_requests, 
       availability_total_requests
FROM provider_profile_metrics
WHERE metric_time >= NOW() - INTERVAL '1 day'
ORDER BY metric_time DESC
LIMIT 20;
```

### 查看每日画像
```sql
SELECT credential_id, profile_date,
       network_score, availability_score, stability_score, scale_score,
       total_score, best_timeslot, worst_timeslot
FROM provider_profile_daily
WHERE profile_date >= CURRENT_DATE - 7
ORDER BY profile_date DESC, total_score DESC
LIMIT 20;
```

### 查看低分供应商
```sql
SELECT c.id, c.name, p.profile_date, p.total_score
FROM provider_profile_daily p
JOIN credentials c ON c.id = p.credential_id
WHERE p.profile_date = CURRENT_DATE - 1
  AND p.total_score < 60
ORDER BY p.total_score ASC;
```

## 日志

系统会输出以下日志：

- `provider profile collector started`: 采集器启动
- `provider profile collection completed`: 采集完成
- `provider profile aggregation completed`: 聚合完成
- `provider profile cleanup completed`: 清理完成
- `provider profile collection failed`: 采集失败（会记录错误）

## 故障排查

### 系统未启动
1. 检查配置：`provider_profile.enabled = true`
2. 检查数据库：表是否已创建
3. 检查日志：查找 "provider profile" 相关日志

### 采集失败
1. 检查 credentials 表：是否有活跃凭证
2. 检查 request_logs 表：是否有最近的请求数据
3. 检查日志：查看具体错误信息

### 数据为空
1. 等待第一次采集完成（最多2小时）
2. 检查 provider_profile_metrics 表：是否有数据
3. 等待第一次聚合完成（次日凌晨4点）

## 下一步（剩余 Phase）

- [x] Phase 2: 告警和自动处理 - 评分过低自动禁用（已合并到 Phase 2，提前完成）
- [ ] 基础评分补全 - 实现价格评分维度（原 Phase 2 内容）
- [ ] Phase 3: 可信度测试第一期 - 能力探针、成本倒推
- [ ] Phase 5: 前端展示 - API 和管理界面（含告警展示）
- [ ] Phase 6: 费用准确性 - 账单对账和差异分析
- [ ] Phase 7: 可信度测试第二期 - 标准测试集、输出指纹
- [ ] Phase 8: 优化和上线 - 性能优化、全量部署

## 参考文档

- 设计文档: `docs/供应商管理/2026-07-26-供应商画像系统设计.md`
- 数据库部署: `docs/供应商管理/数据库部署说明.md`
- 实施计划: `docs/superpowers/plans/2026-07-26-provider-profile-phase1-infrastructure.md`
