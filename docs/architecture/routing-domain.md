# Routing Domain 边界定义

> **Version**: 1.0  
> **Created**: 2026-06-24  
> **Status**: Active (Phase A - 逻辑领域边界先行，物理迁移待冻结期后)

---

## 一、领域范围

**Routing Domain** 是 llm-gateway-go 中负责 `model=auto` 智能路由的完整逻辑边界，包含：

1. **任务分类** (Task Classification)：识别请求意图（code/reasoning/chat/...）
2. **候选评分** (Candidate Scoring)：对可用模型×凭据组合按 6+2 维打分
3. **路由决策** (Route Decision)：基于任务类型、用户偏好、三级偏好表选出最佳模型
4. **运行时调优** (Runtime Tuning)：关键词、阈值、权重的热加载与 A/B 测试
5. **管理界面** (Admin UI)：任务类型配置、模型偏好、评分体系、路由审计

---

## 二、物理文件清单（当前阶段 A）

### 核心包：`autoroute/`

| 文件 | 职责 | 关键类型 |
|------|------|----------|
| `classifier.go` | 启发式任务分类（8 task types，4 层优先级） | `HeuristicClassifier`, `TaskType`, `Classification` |
| `classifier_llm.go` | LLM 兜底分类器（低置信度时升级） | `LLMFallbackClassifier`, `DisabledCaller` |
| `patterns.go` | 正则模式层（捕获结构化线索如"水池问题"） | `PatternMatch`, `matchPatterns()` |
| `decision.go` | 路由决策器（统筹分类→评分→选择） | `Decider`, `Decision`, `ProfileStore` |
| `index.go` | 候选池索引（5min 滚动刷新，实时指标） | `Index`, `Candidate`, `Recommend()` |
| `scoring.go` | 8 维评分算法（price/speed/stability/match/pressure/context/version/strength） | `Score()`, `ScoringBreakdown`, `CostContext` |
| `profile.go` | 用户偏好权重（smart/speed_first/cost_first × 8 维） | `Profile`, `ProfileWeights`, `WeightsForTask()` |
| `tuning_store.go` | 运行时参数热加载（关键词/阈值/权重从 tuning_params 表） | `TuningStore`, `tuningSnapshot` |
| `override_store.go` | 管理员路由覆盖（ban/pin 特定模型） | `OverrideStore`, `RoutingOverride` |
| `feedback.go` | 路由质量反馈采集（tuning_signals 表） | `Feedback`, `SignalWriter` |
| `strategy.go` | A/B 测试策略（baseline vs pattern_layered） | `Strategy`, `strategyConfig` |
| `session_intent_cache.go` | 会话意图缓存（10min TTL，避免重复分类） | `SessionIntentCache`, `CachedIntent` |

### Admin API：`admin/`

| 文件 | 职责 | 端点示例 |
|------|------|----------|
| `auto_route.go` | 自动路由监控 API（decisions/index/audit/cost） | `GET /api/admin/auto-route/decisions` |
| `work_types.go` | 任务类型 CRUD + 三级偏好管理 | `PUT /api/admin/work-types/{key}/routes` |
| `routing_overrides.go` | 路由覆盖管理（ban/pin） | `PUT /api/admin/routing/overrides` |
| `auto_route_tuning.go` | 调优 API（反馈分析、参数推荐） | `POST /api/admin/auto-route/tuning/analyze` |
| `admin_llm_task.go` | 内部 LLM 任务（session_title/summary，复用 work_type_model_route fallback） | `callAdminLLMChat()`, `resolveAdminLLMFallbackModel()` |
| `models.go` | 标准模型管理（扩展 released_at/strengths/cost_tier/version_rank） | `PUT /api/admin/models/{id}` |

### Relay 集成：`relay/`

| 文件 | 职责 |
|------|------|
| `auto_route.go` | 信号提取 + model=auto 触发入口 + X-Gw-Auto-Decision 响应头 |

### 前端：`web/src/`

| 文件 | 职责 |
|------|------|
| `views/RoutingDashboardView.vue` | 路由总览（实时决策、候选排序、任务分布） |
| `views/DecisionsView.vue` | 历史决策审计（request_logs.auto_decision） |
| `views/WorkTypesView.vue` | 任务类型配置 + 三级偏好编辑 |
| `views/ModelsView.vue` | 标准模型管理（扩展 released_at/strengths/version_rank） |
| `views/ScoringConfigView.vue` | 评分体系配置（权重矩阵、公式系数、模型×任务覆盖分） |
| `views/RoutingOverrideView.vue` | 路由覆盖（ban/pin 特定模型） |
| `views/CorrelationsView.vue` | 路由质量相关性分析 |
| `views/TuningView.vue` | 调优建议（基于反馈信号） |
| `api-autoroute.ts` | 自动路由 API 客户端 |

---

## 三、领域边界规则

### 3.1 对内：领域内聚

- **所有任务分类逻辑必须在 `autoroute/classifier*.go`**，不得散落到 relay/admin。
- **所有评分逻辑必须在 `autoroute/scoring.go` + `profile.go`**，不得在 Index.Recommend 里硬编码。
- **所有路由决策必须经过 `Decider.Decide()`**，不得在 relay handler 里直接挑模型。
- **所有运行时参数必须经过 `TuningStore`**，不得直接读 DB 或环境变量（除构造时默认值）。

### 3.2 对外：依赖最小化

- **Routing Domain 依赖**：
  - `db/` (PostgreSQL pool)
  - `provider/` (credential 抽象，只读)
  - `settings/` (settings_kv 表，只读)
  - `telemetry/` (OTel span，write-only)
- **Routing Domain 不得依赖**：
  - `relay/` 的具体协议转换（OpenAI/Anthropic）
  - `maas/` 计费逻辑
  - `compressor/` 上下文压缩

### 3.3 数据库表所有权

| 表名 | Owner | 用途 |
|------|-------|------|
| `credential_model_index` | Routing | 5min 滚动候选池（bg/auto_index_refresher 写，Index 读） |
| `work_type_config` | Routing | 22 个任务类型定义 |
| `work_type_model_route` | Routing | 任务×模型三级偏好 + 质量覆盖分 |
| `api_key_auto_profile` | Routing | 用户粘性偏好（30min TTL） |
| `tuning_params` | Routing | 运行时参数（关键词/阈值/权重） |
| `tuning_signals` | Routing | 路由质量反馈 |
| `routing_overrides` | Routing | 管理员 ban/pin |
| `models_canonical` | **Shared** | 标准模型（Platform Ops 管理，Routing 只读+评分字段写） |
| `request_logs.{task_type,auto_decision,work_type}` | Routing (列) | 路由审计字段 |

---

## 四、阶段 B 迁移计划（2026-07-01 后）

### 4.1 物理包结构

```
routing/                          # [新] 顶层包
├── classifier.go                 # [迁移自 autoroute/]
├── classifier_llm.go
├── patterns.go
├── decision.go
├── index.go
├── scoring.go
├── profile.go
├── tuning_store.go
├── override_store.go
├── feedback.go
├── strategy.go
├── session_intent_cache.go
├── admin/                        # [新] admin handler 子包
│   ├── auto_route.go             # [迁移自 admin/]
│   ├── work_types.go
│   ├── routing_overrides.go
│   ├── tuning.go
│   └── models.go                 # [部分迁移：routing 相关逻辑]
└── relay/                        # [新] relay 集成子包
    └── signals.go                # [迁移自 relay/auto_route.go 的信号提取]
```

### 4.2 迁移步骤

1. `git mv services/llm-gateway-go/autoroute services/llm-gateway-go/routing`
2. 在 `routing/admin/` 创建子包，迁移 `admin/auto_route.go` 等
3. 全局替换 import path：`github.com/kaixuan/llm-gateway-go/autoroute` → `.../routing`
4. `admin/handler.go` 只留路由注册壳，实际 handler 在 `routing/admin/`
5. `go test ./...` + `golangci-lint run` 全绿
6. 提交 PR 标题：`refactor(routing): migrate to top-level domain package (Phase B)`

---

## 五、测试策略

### 5.1 单元测试

- **每个 scoring 维度独立测试**：`scoring_test.go` 覆盖 price/speed/stability/match/pressure/context/version/strength 8 个 `score*()` 函数
- **分类器优先级测试**：`classifier_test.go` 红→绿用例覆盖"vision > code > long_context > agent > ..."顺序
- **三级漏斗测试**：`index_test.go` 验证 primary 全在前、tier_bonus 正确、version_rank 路由

### 5.2 集成测试

- `e2e_auto_route_test.go`（新增）：从 relay handler 发 `{"model":"auto"}` 请求，验证 X-Gw-Auto-Decision 头 + 最终选中的模型符合预期

### 5.3 Lint 强制

- `lint-tenant-scope-llmgw`：所有 admin handler 走 `tenantLogsClause`
- `lint-llmgw-deploy`：SSOT lib 接入
- `lint-otel-tenant`：OTel span 带 `tenant.id` attribute

---

## 六、性能指标

| 指标 | 目标 | 当前 | 测量方法 |
|------|------|------|----------|
| 分类延迟（P95） | < 5ms | ~2ms | `otel.autoroute.classify.duration_ms` |
| 评分延迟（P95） | < 10ms | ~5ms | `otel.autoroute.score.duration_ms` |
| Index 内存占用 | < 50MB | ~20MB | `runtime.MemStats` |
| TuningStore reload | < 100ms | ~30ms | `otel.tuning_store.reload.duration_ms` |
| 决策总延迟（P95） | < 20ms | ~10ms | `otel.autoroute.decide.duration_ms` |

---

## 七、文档链接

- **用户文档**：`docs/2026-06-15-auto-route-mode.md`（SQL schema + 使用指南）
- **API 文档**：`admin/auto_route.go` 头部注释（5 个 admin 端点说明）
- **多租户审计**：`docs/llm-gateway-go/multi-tenant-2026-06-15.md`（RLS policy + 租户隔离）
- **评分算法详解**：`autoroute/scoring.go` 头部大段注释（8 维公式）

---

**Routing Domain 宣言**：任务识别准、模型选得对、配置能热调、决策可审计。
