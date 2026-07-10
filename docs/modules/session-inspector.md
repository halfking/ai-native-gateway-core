# 会话健康检查模块（session_inspector）

**版本**: R1.13  
**状态**: 生产就绪  
**最后更新**: 2026-07-09

---

## 1. 概述

会话健康检查（Session Inspector）是一个综合性的会话生命周期与质量监控模块，
通过 **hook 插件 + 后台 worker** 双轨协同工作，覆盖 5 个核心场景：

| 场景 | 触发点 | 默认动作 |
|------|--------|----------|
| Token 使用量监控 | hook (PreRouting) | 软/硬告警 |
| 不活跃会话检测 | hook + worker | 软关闭 |
| 高频请求检测 | hook (PreRouting) | 记录 / 429 |
| 错误率告警 | hook (PreRouting) | 记录 / 通知 |
| 租户级会话生命周期 | worker (5m 周期) | LRU 驱逐 |

### 1.1 核心目标

- **可插拔的 Inspector 框架**：每个 inspector 独立实现 `Inspector` 接口，
  通过 `BuildInspectorsFromConfig(cfg)` 一键构建全套
- **配置热更新**：所有阈值在 `settings/spec_modules.go` 注册，
  改完秒级生效（无需重启）
- **多通道告警**：IM (feishu/wechat) + Webhook + Prometheus 三通道并行
- **可恢复回收**：默认 `soft_close` 策略，仅修改 `status='closed'` 而不删除数据
- **联动已有模块**：与 `compression / cache / prompt_injection / output_compliance / session_audit` 形成完整治理闭环

### 1.2 架构图

```
┌─────────────────────────────────────────────────────────────────────┐
│                  会话健康检查 (Session Inspector)                     │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  ┌──────────────┐  Pipeline  ┌──────────────┐  Background  ┌──────┐│
│  │  PreRouting  │──Hook─────▶│  Inspectors  │             │  PG  ││
│  │  (在线实时)   │            │  (6 种)       │             │      ││
│  └──────────────┘            └──────────────┘             └──────┘│
│         │                            │                            ││
│         │                            ▼                            ││
│         │                  ┌──────────────────┐                   ││
│         │                  │   findings       │                   ││
│         │                  │   ├─ token_limit │                   ││
│         │                  │   ├─ inactive    │                   ││
│         │                  │   ├─ high_freq   │                   ││
│         │                  │   ├─ lifecycle   │                   ││
│         │                  │   ├─ error_rate  │                   ││
│         │                  │   └─ model_sw    │                   ││
│         │                  └──────────────────┘                   ││
│         │                            │                            ││
│         ▼                            ▼                            ││
│  ┌────────────────────────────────────────────────────┐          ││
│  │  响应分发                                           │          ││
│  │  ├─ env.Metadata  (供下游 hook / 业务读)            │          ││
│  │  ├─ Prometheus    (llmgw_session_inspector_*)       │          ││
│  │  ├─ EventBus      (SessionInspectorFindingEvent)   │          ││
│  │  └─ HTTP 429      (critical + 阻断动作)              │          ││
│  └────────────────────────────────────────────────────┘          ││
│                                                                     │
│  ┌────────────────────────────────────────────────────┐          ││
│  │  SessionLifecycleWorker (后台, 5min 周期)          │          ││
│  │  ├─ 闲置会话 (last_active_at < NOW() - idle.timeout)│          ││
│  │  ├─ 绝对超期 (created_at < NOW() - abs_max)         │          ││
│  │  └─ 租户超限 (active_count > max_per_tenant)       │          ││
│  │       └─ 按 eviction_policy (lru/fifo) 驱逐         │          ││
│  └────────────────────────────────────────────────────┘          ││
└─────────────────────────────────────────────────────────────────────┘
                                │
            ┌───────────────────┼───────────────────┐
            ▼                   ▼                   ▼
    ┌──────────────┐    ┌──────────────┐    ┌──────────────┐
    │ compression  │    │ cache        │    │ session_audit│
    │ 压缩管理      │    │ 会话缓存     │    │ 会话审计     │
    └──────────────┘    └──────────────┘    └──────────────┘
```

---

## 2. 核心组件

### 2.1 6 种 Inspector

| Inspector | 触发条件 | 默认严重度 | 阻断条件 |
|-----------|----------|------------|----------|
| `TokenLimit` | token > `max_total` | Critical | `warn_action=block` |
| `TokenLimit` (软) | token > `soft_threshold` | Warning | 否（仅记录） |
| `Inactive` | idle > `idle.timeout` | Warning | 否 |
| `Inactive` (绝对) | age > `absolute_max_lifetime` | Error | 是（强制 429） |
| `HighFrequency` | req > `rpm_limit` | Error/Warning | `!observe_only` |
| `HighFrequency` (burst) | burst > `burst_limit` | Critical | `!observe_only` |
| `HighFrequency` (并发) | concurrent > `max_concurrent` | Error | 否 |
| `SessionLifecycle` | tenant_active > `max_per_tenant` | Warning | 否 |
| `ErrorRate` | err_rate ≥ 0.5 (50%) | Error | 否 |
| `ErrorRate` (软) | err_rate ≥ 0.2 (20%) | Warning | 否 |
| `ModelSwitch` | switch_count > 5 | Warning | 否 |

### 2.2 SessionLifecycleWorker

后台 worker，每 `cleanup_interval`（默认 5m）扫描一次 `session_dim` 表，
按以下顺序处理：

1. **闲置会话**：`status='active' AND last_active_at < NOW() - idle.timeout`
2. **绝对超期**：`status='active' AND first_request_at < NOW() - absolute_max_lifetime`
3. **租户超限**：每租户 active 计数 > `max_sessions_per_tenant` → 按 `eviction_policy` 驱逐

每个候选会话按 `recycle_action` 决策：

| recycle_action | DB 操作 | 事件发布 |
|----------------|---------|----------|
| `soft_close` (默认) | `UPDATE session_dim SET status='closed', closed_at=NOW(), stop_reason=$1` | 是 |
| `notify_only` | 无 DB 修改 | 是（仅通知） |

**Prometheus 指标**：
- `llmgw_session_lifecycle_recycled_total{reason}`  按回收原因
- `llmgw_session_lifecycle_soft_closed_total`     软关闭总数
- `llmgw_session_lifecycle_evicted_total{policy}`  按策略驱逐数
- `llmgw_session_lifecycle_scan_duration_seconds`  扫描耗时
- `llmgw_session_lifecycle_scan_errors_total`      错误数

### 2.3 EventBus 事件

3 类事件类型，统一命名 `session_inspector.<kind>`：

| Event | Type | 触发场景 |
|-------|------|----------|
| `SessionInspectorFindingEvent` | `session_inspector.finding` | 任意 inspector 命中 |
| `SessionInspectorRecycleEvent` | `session_inspector.recycle` | worker 软关闭 / 通知 |
| `SessionInspectorStatsEvent` | `session_inspector.stats` | 每次 worker 扫描后 |

订阅者可按 Type 过滤消费。

---

## 3. 配置项

完整配置清单（共 23 个键），通过 admin `/admin/modules/session_inspector` 页面或
`PUT /api/admin/settings/{key}` 修改：

### 3.1 Token 使用量（5 项）

| Key | Type | Default | 说明 |
|-----|------|---------|------|
| `session_inspector.token.max_total` | int | 100000 | 硬上限 |
| `session_inspector.token.soft_warning_pct` | int | 80 | 软警告百分比 |
| `session_inspector.token.warn_action` | enum | log | log/metadata/block |
| `session_inspector.token.include_output` | bool | true | 计 output |
| `session_inspector.token.reset_cycle` | enum | never | never/hourly/daily/weekly |

### 3.2 不活跃检测与回收（5 项）

| Key | Type | Default | 说明 |
|-----|------|---------|------|
| `session_inspector.idle.timeout` | duration | 30m | 不活跃超时 |
| `session_inspector.idle.absolute_max_lifetime` | duration | 168h | 绝对最长生 |
| `session_inspector.idle.cleanup_interval` | duration | 5m | worker 扫描间隔 |
| `session_inspector.idle.cleanup_batch_size` | int | 500 | 单次批量 |
| `session_inspector.idle.recycle_action` | enum | soft_close | soft_close/notify_only |

### 3.3 高频请求检测（6 项）

| Key | Type | Default | 说明 |
|-----|------|---------|------|
| `session_inspector.rate.rpm_limit` | int | 60 | RPM 上限 |
| `session_inspector.rate.burst_limit` | int | 100 | 突发上限 |
| `session_inspector.rate.burst_window_seconds` | int | 5 | 突发窗口 |
| `session_inspector.rate.max_concurrent` | int | 4 | 单会话并发 |
| `session_inspector.rate.strategy` | enum | sliding | fixed/sliding/token_bucket |
| `session_inspector.rate.observe_only` | bool | false | 仅观察不阻断 |

### 3.4 会话生命周期（3 项）

| Key | Type | Default | 说明 |
|-----|------|---------|------|
| `session_inspector.lifecycle.auto_extend_on_activity` | bool | true | 活跃自动续期 |
| `session_inspector.lifecycle.max_sessions_per_tenant` | int | 1000 | 单租户最大 |
| `session_inspector.lifecycle.eviction_policy` | enum | lru | lru/fifo/none |

### 3.5 告警与可观测性（4 项）

| Key | Type | Default | 说明 |
|-----|------|---------|------|
| `session_inspector.alert.enabled` | bool | true | 启用告警 |
| `session_inspector.alert.notify_channels` | string(JSON) | ["feishu","wechat"] | IM 渠道 |
| `session_inspector.alert.webhook_urls` | string(JSON) | [] | Webhook 列表 |
| `session_inspector.alert.prometheus_enabled` | bool | true | Prometheus |

---

## 4. Admin API

| Method | Path | 权限 | 说明 |
|--------|------|------|------|
| GET | `/api/admin/modules/session_inspector` | admin | 完整配置 + 状态 |
| PUT | `/api/admin/modules/session_inspector/toggle` | admin | 启/禁用 |
| GET | `/api/admin/sessions/<id>/health` | admin | 已有：健康评分 |
| POST | `/api/admin/sessions/<id>/recompute-health` | super_admin | 已有：重算 |
| GET | `/api/admin/sessions/<id>/inspector-findings` | admin | **NEW** - 单会话 findings |
| GET | `/api/admin/sessions/inspector-stats` | admin | **NEW** - 平台统计 |
| POST | `/api/admin/sessions/<id>/recycle` | super_admin | **NEW** - 手动软关闭 |

### 4.1 响应示例

`GET /api/admin/sessions/inspector-stats`:
```json
{
  "active_sessions": 342,
  "idle_sessions": 89,
  "closed_sessions": 1024,
  "total_tokens": 8742156,
  "avg_health_score": 78.5,
  "recycled_today": 47,
  "findings_last_1h": 12,
  "generated_at": "2026-07-09T12:34:56Z"
}
```

`GET /api/admin/sessions/<id>/inspector-findings`:
```json
{
  "gw_session_id": "sess-abc123",
  "tenant_id": "tenant-9",
  "findings": [
    {
      "inspector_name": "token_limit",
      "severity": "warning",
      "code": "TOKEN_SOFT_WARNING",
      "message": "session token count 85000 reached soft threshold 80000 (80%)",
      "source": "computed",
      "detected_at": "2026-07-09T12:34:56Z"
    }
  ],
  "count": 1,
  "generated_at": "2026-07-09T12:34:56Z"
}
```

---

## 5. 模块依赖

| 依赖模块 | Required | 用途 |
|----------|----------|------|
| `compression` (会话压缩) | ✓ | 提供 token 用量基线、idle 检测数据 |
| `cache` (会话缓存) | ✓ | 提供 last_active_at 实时数据 |
| `prompt_injection` (提示词注入检测) | ✓ | 注入命中会话影响健康评分 |
| `output_compliance` (输出安全检测) | ✓ | PII/毒性输出影响错误率 |
| `session_audit` (会话审计与审批) | ✓ | 高风险会话审批通过后方可放行 |

依赖校验：admin 页面会提示"需先启用依赖模块"，并通过 `requiredDependencyBlockReason`
阻止单独启用。

---

## 6. 配置决策树

### 6.1 轻量级（适合 demo / 开发）

```yaml
session_inspector.idle.timeout: 1h
session_inspector.idle.recycle_action: notify_only
session_inspector.rate.observe_only: true
session_inspector.alert.notify_channels: []    # 关闭 IM
```

### 6.2 中度（推荐生产环境初始配置）

```yaml
session_inspector.token.max_total: 100000
session_inspector.token.soft_warning_pct: 80
session_inspector.token.warn_action: log
session_inspector.idle.timeout: 30m
session_inspector.idle.absolute_max_lifetime: 168h
session_inspector.idle.recycle_action: soft_close
session_inspector.rate.rpm_limit: 60
session_inspector.rate.observe_only: false
session_inspector.alert.enabled: true
session_inspector.alert.notify_channels: ["feishu"]
```

### 6.3 严格（高敏感场景）

```yaml
session_inspector.token.max_total: 50000
session_inspector.token.warn_action: block
session_inspector.idle.timeout: 15m
session_inspector.idle.recycle_action: soft_close
session_inspector.rate.rpm_limit: 30
session_inspector.rate.burst_limit: 50
session_inspector.rate.max_concurrent: 2
session_inspector.lifecycle.max_sessions_per_tenant: 200
session_inspector.alert.notify_channels: ["feishu","wechat"]
```

---

## 7. 已知限制

- **依赖元数据驱动**：inspector 依赖 `env.Metadata` 中的字段（request_count / token_count / last_active_at），
  上游需要在 PreRouting 之前把这些字段填入 PipelineRequest（已由 cache_lookup hook 部分覆盖，
  缺失时 inspector 返回 nil 跳过）
- **Worker 单实例**：当前实现假设 gateway 单实例部署，多实例时需要分布式锁
- **告警投递**：IM 通知依赖 `feishu_bot / wechat_bot` 模块启用且 `notify_channels` 命中对应值
- **冷启动延迟**：worker 启动时立即执行一次扫描，可能与冷启动其他 worker 竞争 DB

---

## 8. 使用指南

### 8.1 快速开始

1. 启用模块（默认已开启）：
   ```bash
   PUT /api/admin/modules/session_inspector/toggle
   { "enabled": true }
   ```

2. 确认依赖模块已启用（admin 页面有提示）

3. 保持默认配置即可（生产推荐配置见 6.2）

4. 启动后日志中可见：
   ```
   session lifecycle worker started (5m interval, soft_close default)
   ```

### 8.2 调优建议

- **高并发场景**：调高 `idle.timeout` 到 2h，降低 worker 频率到 30m
- **敏感合规场景**：调低 `max_per_tenant`，开启 `eviction_policy=lru`
- **开发调试**：开启 `rate.observe_only=true`，关闭所有 IM 渠道

---

## 9. 相关文档

- [安全检测引擎](/docs/modules/security-engine.md) - 互补模块，提供意图/威胁分析
- [会话压缩](/docs/architecture/migration-guide.md) - 联动提供 token 用量基线
- [Session Health API](/admin/session_health_api.go) - API 实现
- [Settings Registry](/settings/spec_modules.go) - 22 个配置项定义

---

**维护者**: Official-Deploy Team
**联系方式**: See [CODEOWNERS](../../CODEOWNERS)
