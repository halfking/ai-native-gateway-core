# 自检（Self-Check）功能 — 详细设计文档

> **作者**: OpenCode AI Agent
> **日期**: 2026-07-11
> **目标**: 在 llm-gateway-go 内部建立"系统自检模块"，定期对若干关键模型做 ping + 3 轮工具调用会话测试，验证 gateway 的可用性与路由正确性。失败时用 provider 原始凭据直连上游做故障隔离。所有结果落地到独立表，并提供可视化页面。

---

## 一、背景与目标

### 1.1 痛点

1. **被动感知故障**: 当前只有用户报错才能感知模型不可用
2. **定位困难**: 不知道是 gateway 自身问题，还是上游 provider 问题
3. **工具调用未自检**: 工具解析的正确性无定期验证
4. **Top 模型无监控**: 没有按业务重要性排序的实时状态看板

### 1.2 目标

- **主动自检**: 每 60 秒对关键模型跑 1 次 ping + 3 轮对话（含 1 次工具调用）
- **故障隔离**: 失败时自动用 provider 原始凭据直连上游，区分 gateway / 上游 故障
- **可视化**: Dashboard 新增"系统监测"Tab，展示成功率、延迟、错误分类、详细日志
- **可控**: 开关、周期、模型列表均可配置
- **低成本**: 单次会话 token ≤ 100K，周期可调，故障时自动加密周期

---

## 二、架构概览

```
┌─────────────────────────────────────────────────────────────────┐
│                llm-gateway-go (k3s, kaixuan-1)                   │
│                                                                 │
│   ┌────────────────────────────────────┐                        │
│   │  SelfCheckWorker (bg/)             │                        │
│   │  ─────────────────────────────     │                        │
│   │  • 模型选择器                       │                        │
│   │    (Top10 + 特色模型 → 去重)        │                        │
│   │  • 周期调度器                       │                        │
│   │    (60s normal / 30s fault)         │                        │
│   │  • 会话生成器                       │                        │
│   │    (3轮对话 + 1次工具)              │                        │
│   │  • 故障隔离器                       │                        │
│   │    (provider原始凭据直连上游)       │                        │
│   └─────────────┬──────────────────────┘                        │
│                 │                                               │
│                 ▼                                               │
│   ┌────────────────────────────────────┐                        │
│   │  SelfCheckAdminHandler (admin/)    │                        │
│   │  ─────────────────────────────     │                        │
│   │  GET    /api/self-check/results    │                        │
│   │  GET    /api/self-check/runs/:id   │                        │
│   │  GET    /api/self-check/settings   │                        │
│   │  PUT    /api/self-check/settings   │                        │
│   │  POST   /api/self-check/trigger    │                        │
│   │  GET    /api/self-check/models     │                        │
│   └─────────────┬──────────────────────┘                        │
│                 │                                               │
│                 ▼                                               │
│   ┌────────────────────────────────────┐                        │
│   │  PostgreSQL (252)                  │                        │
│   │  ─────────────────────────────     │                        │
│   │  • self_check_runs                 │                        │
│   │  • self_check_round_results        │                        │
│   │  • self_check_settings             │                        │
│   │  • self_check_models               │ (可选，特色模型)       │
│   └─────────────┬──────────────────────┘                        │
│                 │                                               │
└─────────────────┼───────────────────────────────────────────────┘
                  │
                  ▼
   ┌─────────────────────────────────────────────┐
   │  Frontend (Vue 3 + TS)                      │
   │  ─────────────────────────────────          │
   │  DashboardView → 新 Tab "系统监测"          │
   │  • 实时状态卡片（每模型）                   │
   │  • 成功率趋势图（折线）                     │
   │  • 延迟分布图（柱状）                       │
   │  • 错误分类统计（饼图）                     │
   │  • 详细日志表格（可展开）                   │
   └─────────────────────────────────────────────┘
```

---

## 三、数据库设计（252）

### 3.1 `self_check_runs` — 每次完整测试运行

```sql
CREATE TABLE self_check_runs (
    id BIGSERIAL PRIMARY KEY,
    model_name TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    duration_ms INT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'running',        -- running/success/partial/failed
    rounds_total INT NOT NULL DEFAULT 3,
    rounds_success INT NOT NULL DEFAULT 0,
    had_tool_call BOOLEAN NOT NULL DEFAULT FALSE,
    total_tokens INT NOT NULL DEFAULT 0,
    avg_latency_ms INT NOT NULL DEFAULT 0,
    error_type TEXT,                                -- http_000/http_502/http_503/timeout/upstream_fail/none
    error_detail TEXT,
    upstream_tested BOOLEAN NOT NULL DEFAULT FALSE, -- 失败时是否测了上游
    upstream_result TEXT,                            -- upstream result: success/failed/timeout
    upstream_latency_ms INT,
    upstream_error TEXT,
    tenant_id TEXT NOT NULL DEFAULT 'default',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_self_check_runs_model ON self_check_runs(model_name);
CREATE INDEX idx_self_check_runs_started ON self_check_runs(started_at DESC);
CREATE INDEX idx_self_check_runs_status ON self_check_runs(status);
```

### 3.2 `self_check_round_results` — 每轮详情

```sql
CREATE TABLE self_check_round_results (
    id BIGSERIAL PRIMARY KEY,
    run_id BIGINT NOT NULL REFERENCES self_check_runs(id) ON DELETE CASCADE,
    round_index INT NOT NULL,                       -- 1/2/3
    is_tool_call BOOLEAN NOT NULL DEFAULT FALSE,
    is_ping BOOLEAN NOT NULL DEFAULT FALSE,         -- 第0轮 ping
    latency_ms INT NOT NULL DEFAULT 0,
    prompt_tokens INT NOT NULL DEFAULT 0,
    completion_tokens INT NOT NULL DEFAULT 0,
    total_tokens INT NOT NULL DEFAULT 0,
    success BOOLEAN NOT NULL DEFAULT FALSE,
    http_code INT,
    error_message TEXT,
    request_body TEXT,                              -- 截断到 4K
    response_preview TEXT,                          -- 前 500 字符
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_self_check_rounds_run ON self_check_round_results(run_id);
```

### 3.3 `self_check_settings` — 配置（单行）

```sql
CREATE TABLE self_check_settings (
    id INT PRIMARY KEY DEFAULT 1 CHECK (id=1),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    normal_interval_seconds INT NOT NULL DEFAULT 60,
    fault_interval_seconds INT NOT NULL DEFAULT 30,
    model_source TEXT NOT NULL DEFAULT 'both',      -- top10/featured/both
    max_models INT NOT NULL DEFAULT 10,
    max_tokens_per_run INT NOT NULL DEFAULT 100000,
    featured_model_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by TEXT
);

-- 默认开启 + 注入默认特色模型
INSERT INTO self_check_settings (id) VALUES (1) ON CONFLICT DO NOTHING;
```

### 3.4 迁移文件命名

- `/sql/migrations/domain/XXX_self_check.sql`（XXX 为下一个编号）

---

## 四、Worker 设计 (`bg/self_check_worker.go`)

### 4.1 核心结构

```go
package bg

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "sync"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
)

type SelfCheckWorker struct {
    db           *pgxpool.Pool
    apiKey       string  // 系统级 apikey
    baseURL      string  // 默认 "https://llm.kxpms.cn/v1"
    cancel       context.CancelFunc
    done         chan struct{}
    faultModels  map[string]time.Time  // 故障模型 + 故障开始时间
    faultMu      sync.RWMutex
}

type runParams struct {
    modelName    string
    rounds       int  // 3
    hasToolCall  bool // true
    maxTokens    int  // 100000
}

func NewSelfCheckWorker(db *pgxpool.Pool, apiKey, baseURL string) *SelfCheckWorker
func (w *SelfCheckWorker) Start(ctx context.Context)
func (w *SelfCheckWorker) Stop()
```

### 4.2 启动流程（在 `cmd/gateway/main.go` 中）

```go
// 1. 拿系统级 apikey
sysAPIKey, err := ensureSystemAPIKey(db, slog.Default())
if err != nil {
    slog.Warn("self-check disabled: no system apikey", "error", err)
    return
}

// 2. 启动 worker
sc := bg.NewSelfCheckWorker(db, sysAPIKey, "https://llm.kxpms.cn/v1")
sc.Start(context.Background())
slog.Info("self-check worker started")
```

### 4.3 调度循环

```
┌─ Tick (every min) ─────────────────────────────────────┐
│                                                          │
│ 1. 读取 self_check_settings                              │
│ 2. enabled=false → skip                                  │
│ 3. 计算本轮待测模型列表                                  │
│    - featured_model_ids ∪ Top10(by recent requests)     │
│ 4. 对每个模型：                                          │
│    - 如果在 faultModels 中 → 用 fault_interval          │
│    - 否则 → 用 normal_interval                          │
│    - 如果上次测试距今 < 该模型当前 interval → skip       │
│ 5. 并发跑所有该测的模型（限 5 并发）                     │
│ 6. 每个 run = 1 ping + 3 轮对话（含 1 工具）             │
│ 7. 失败 → 启动故障隔离器（provider直连上游）            │
│ 8. 把结果写入 self_check_runs + round_results           │
│ 9. 故障模型写入 faultModels，连续成功 3 次后移除         │
└──────────────────────────────────────────────────────────┘
```

### 4.4 会话生成器

**Ping 轮（round 0）**：
```json
{
  "model": "gpt-5.6-luna",
  "messages": [{"role": "user", "content": "ping"}],
  "max_tokens": 10
}
```

**3 轮对话（含 1 工具）**：

工具定义（统一）：
```json
{
  "type": "function",
  "function": {
    "name": "get_current_time",
    "description": "获取当前时间",
    "parameters": {
      "type": "object",
      "properties": {
        "timezone": {"type": "string", "description": "时区，如 Asia/Shanghai"}
      },
      "required": ["timezone"]
    }
  }
}
```

**轮 1**（用户简单问）:
```
User: "现在几点？"
Tools: [get_current_time]
```

**轮 2**（模型调用工具，gateway 注入工具结果）:
```
Assistant: tool_call(get_current_time, {"timezone": "Asia/Shanghai"})
Tool: "2026-07-11T20:00:00+08:00"
```

**轮 3**（最终回答）:
```
User: "好的，谢谢"
Assistant: "不客气，现在是 2026-07-11 20:00 (北京时间)。"
```

每次会话的 prompt 设计为：
- 用户消息总长度 ≤ 200 tokens
- 模型预期响应 ≤ 200 tokens
- 工具调用结果 = 固定字符串（30 tokens）
- 单轮总 token ≤ 500
- 4 轮（1 ping + 3 round）总 token ≤ 2000，远低于 100K 上限

### 4.5 故障隔离器

当 gateway 测试失败（任意一轮 HTTP 错误或 000）时：

```go
func (w *SelfCheckWorker) isolateUpstream(ctx context.Context, runID int64, modelName string) {
    // 1. 查 providers + credentials 表
    var baseURL, apiKey string
    err := w.db.QueryRow(ctx, `
        SELECT p.base_url, c.api_key_encrypted
        FROM credentials c
        JOIN providers p ON p.id = c.provider_id
        JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
        JOIN provider_models pm ON pm.id = cmb.provider_model_id
        WHERE pm.raw_model_name = $1 AND c.lifecycle_status = 'active'
        LIMIT 1`, modelName).Scan(&baseURL, &apiKey)

    if err != nil {
        // 没找到凭据，无法隔离
        w.updateRunUpstream(ctx, runID, "no_credential", 0, "无法找到该模型的provider凭据")
        return
    }

    // 2. 解密 api_key
    plainKey, err := secretDecrypt(apiKey)

    // 3. 用相同的请求体（ping）直接 POST 到 baseURL
    body := `{"model":"` + modelName + `","messages":[{"role":"user","content":"ping"}],"max_tokens":10}`
    req, _ := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions",
        strings.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+plainKey)
    req.Header.Set("Content-Type", "application/json")

    start := time.Now()
    resp, err := httpClient.Do(req)
    latency := time.Since(start).Milliseconds()
    if err != nil {
        w.updateRunUpstream(ctx, runID, "timeout", latency, err.Error())
        return
    }
    defer resp.Body.Close()

    if resp.StatusCode == 200 {
        w.updateRunUpstream(ctx, runID, "success", latency, "")
    } else {
        w.updateRunUpstream(ctx, runID, "failed", latency, fmt.Sprintf("HTTP %d", resp.StatusCode))
    }
}
```

**故障判定逻辑**:
- gateway 失败 + upstream 成功 → **gateway 自身问题**
- gateway 失败 + upstream 失败 → **上游 provider 问题**
- gateway 失败 + upstream timeout → **网络/上游问题**

把判定结果写入 `error_detail`：
```
error_type=upstream_fail
error_detail=Gateway 失败 (HTTP 502)，直连上游 [apiclaude.cc] 成功 (latency=1200ms)。→ 判定: 上游正常，gateway 路由问题。
```

### 4.6 API Key 获取

`ensureSystemAPIKey` 函数：
```go
func ensureSystemAPIKey(db *pgxpool.Pool) (string, error) {
    // 1. 查 api_keys 表中 scope=system 的 key
    var key string
    err := db.QueryRow(ctx, `
        SELECT api_key FROM api_keys
        WHERE scope = 'system' AND lifecycle = 'active'
        ORDER BY created_at DESC LIMIT 1`).Scan(&key)
    if err == nil {
        return key, nil
    }

    // 2. 不存在 → 申请一个新 key
    newKey := generateRandomKey("sk-selfcheck-")
    _, err = db.Exec(ctx, `
        INSERT INTO api_keys (api_key, scope, lifecycle, created_at, owner)
        VALUES ($1, 'system', 'active', now(), 'self-check-worker')
        ON CONFLICT (api_key) DO NOTHING`, newKey)
    if err != nil {
        return "", err
    }
    return newKey, nil
}
```

---

## 五、API 设计 (`admin/self_check_handlers.go`)

### 5.1 路由列表

| 方法 | 路径 | 权限 | 说明 |
|------|------|------|------|
| GET | `/api/self-check/runs` | admin | 列出最近 N 次运行（分页） |
| GET | `/api/self-check/runs/:id` | admin | 单次运行详情（含 round_results） |
| GET | `/api/self-check/settings` | admin | 读配置 |
| PUT | `/api/self-check/settings` | super_admin | 改配置 |
| POST | `/api/self-check/trigger` | super_admin | 手动触发一次全量测试 |
| GET | `/api/self-check/stats` | admin | 聚合统计（成功率、延迟、错误分类） |
| GET | `/api/self-check/models` | admin | 当前测试的模型列表 |

### 5.2 响应格式

#### `GET /api/self-check/runs?limit=50&model=gpt-5.6-luna&status=failed`

```json
{
  "items": [
    {
      "id": 12345,
      "model_name": "gpt-5.6-luna",
      "started_at": "2026-07-11T20:00:00Z",
      "completed_at": "2026-07-11T20:00:03Z",
      "duration_ms": 3000,
      "status": "failed",
      "rounds_total": 3,
      "rounds_success": 1,
      "had_tool_call": true,
      "total_tokens": 1800,
      "avg_latency_ms": 850,
      "error_type": "http_502",
      "error_detail": "Round 2 failed: HTTP 502 Bad Gateway",
      "upstream_tested": true,
      "upstream_result": "failed",
      "upstream_latency_ms": 1200,
      "upstream_error": "HTTP 502"
    }
  ],
  "total": 1234
}
```

#### `GET /api/self-check/stats?range=24h`

```json
{
  "range": "24h",
  "summary": {
    "total_runs": 1440,
    "success_runs": 1280,
    "partial_runs": 80,
    "failed_runs": 80,
    "success_rate": 0.889
  },
  "by_model": [
    {
      "model_name": "gpt-5.6-luna",
      "total": 240,
      "success": 220,
      "partial": 10,
      "failed": 10,
      "success_rate": 0.917,
      "avg_latency_ms": 850,
      "p95_latency_ms": 1500,
      "p99_latency_ms": 2800
    }
  ],
  "error_breakdown": [
    {"error_type": "http_000", "count": 45},
    {"error_type": "http_502", "count": 30},
    {"error_type": "http_503", "count": 5}
  ],
  "trend": [
    {"timestamp": "2026-07-11T19:00:00Z", "success_rate": 0.95, "total": 60},
    {"timestamp": "2026-07-11T19:05:00Z", "success_rate": 0.88, "total": 60}
  ]
}
```

---

## 六、Settings 设计 (`settings/spec_self_check.go`)

### 6.1 Platform Specs（全局）

```go
func SelfCheckPlatformSpecs() []*settings.Spec {
    return []*settings.Spec{
        {
            Key:         "self_check.enabled",
            Type:        settings.TypeBool,
            Default:     true,
            Description: "是否启用系统自检",
            Scope:       settings.ScopePlatform,
            EnvKey:      "LLM_GATEWAY_SELF_CHECK_ENABLED",
        },
        {
            Key:         "self_check.normal_interval_seconds",
            Type:        settings.TypeInt,
            Default:     60,
            Description: "正常情况下的测试周期（秒）",
            Scope:       settings.ScopePlatform,
            EnvKey:      "LLM_GATEWAY_SELF_CHECK_NORMAL_INTERVAL",
            Min:         10,
            Max:         600,
        },
        {
            Key:         "self_check.fault_interval_seconds",
            Type:        settings.TypeInt,
            Default:     30,
            Description: "故障情况下的测试周期（秒）",
            Scope:       settings.ScopePlatform,
            EnvKey:      "LLM_GATEWAY_SELF_CHECK_FAULT_INTERVAL",
            Min:         10,
            Max:         300,
        },
        {
            Key:         "self_check.max_models",
            Type:        settings.TypeInt,
            Default:     10,
            Description: "单轮最多测试的模型数",
            Scope:       settings.ScopePlatform,
            EnvKey:      "LLM_GATEWAY_SELF_CHECK_MAX_MODELS",
            Min:         1,
            Max:         50,
        },
        {
            Key:         "self_check.max_tokens_per_run",
            Type:        settings.TypeInt,
            Default:     100000,
            Description: "每次会话最大 token 数",
            Scope:       settings.ScopePlatform,
            EnvKey:      "LLM_GATEWAY_SELF_CHECK_MAX_TOKENS",
            Min:         1000,
            Max:         1000000,
        },
    }
}
```

### 6.2 特色模型设置（DB 级别）

`self_check_settings.featured_model_ids` 存特色模型 ID 列表，由 admin UI 在 `/api/self-check/settings` 中配置。

---

## 七、前端设计

### 7.1 Dashboard 新增 Tab

修改 `/web/src/views/DashboardView.vue`（或 EnhancedDashboardView.vue）的 `seg-tabs` 部分：

```vue
<div class="seg-tabs">
  <button class="seg-tab" :class="{ active: activeTab === 'analytics' }"
          @click="activeTab = 'analytics'">数据分析</button>
  <!-- ... 现有 tabs ... -->
  <button class="seg-tab" :class="{ active: activeTab === 'selfcheck' }"
          @click="activeTab = 'selfcheck'">系统监测</button>
</div>

<!-- ═══ Tab: 系统监测 ═══ -->
<div v-if="activeTab === 'selfcheck'" class="tab-content">
  <SelfCheckPanel />
</div>
```

### 7.2 `SelfCheckPanel.vue` 子组件

```
+------------------------------------------------------------------+
| 系统监测                                       [设置] [手动触发] |
+------------------------------------------------------------------+
|  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐ ┌──────────┐ |
|  │ gpt-5.6-luna │ │ gpt-5.6-sol │ │ gpt-5.6-terra│ │ minimax-m2.7│ |
|  │   ✅ 健康    │ │   ✅ 健康    │ │   ⚠️ 故障    │ │   ✅ 健康    │ |
|  │   92% (24h) │ │   95% (24h) │ │   45% (24h) │ │   98% (24h) │ |
|  │   850ms avg │ │   720ms avg │ │   1200ms avg│ │   1500ms avg│ |
|  └─────────────┘ └─────────────┘ └─────────────┘ └──────────┘ |
|                                                                  |
|  ┌─────────────────────────┐ ┌─────────────────────────────┐ |
|  │   24h 成功率趋势         │ │   错误分类                   │ |
|  │   [折线图]               │ │   [饼图]                     │ |
|  │   ─ gpt-5.6-luna         │ │   - http_000: 45            │ |
|  │   ─ gpt-5.6-sol          │ │   - http_502: 30            │ |
|  │   ─ gpt-5.6-terra        │ │   - timeout:  5             │ |
|  └─────────────────────────┘ └─────────────────────────────┘ |
|                                                                  |
|  ┌─────────────────────────────────────────────────────────┐ |
|  │   延迟分布 (近 100 次)                                    │ |
|  │   [柱状图]                                                │ |
|  └─────────────────────────────────────────────────────────┘ |
|                                                                  |
|  ┌─────────────────────────────────────────────────────────┐ |
|  │   最近 50 次测试详情                          [展开 ▼] │ |
|  │   模型 | 时间 | 状态 | 轮次 | tokens | 延迟 | 错误        │ |
|  │   ... (可展开每轮详情、请求体、响应预览)                 │ |
|  └─────────────────────────────────────────────────────────┘ |
+------------------------------------------------------------------+
```

### 7.3 API 客户端 (`web/src/api-selfcheck.ts`)

```typescript
export const fetchSelfCheckRuns = (params: {
  limit?: number
  model?: string
  status?: string
}) => api.get('/api/self-check/runs', { params })

export const fetchSelfCheckStats = (range: string = '24h') =>
  api.get('/api/self-check/stats', { params: { range } })

export const fetchSelfCheckSettings = () =>
  api.get('/api/self-check/settings')

export const updateSelfCheckSettings = (settings: SelfCheckSettings) =>
  api.put('/api/self-check/settings', settings)

export const triggerSelfCheck = () =>
  api.post('/api/self-check/trigger', {})

export const fetchSelfCheckRunDetail = (id: number) =>
  api.get(`/api/self-check/runs/${id}`)
```

### 7.4 图表库

使用项目已有的 **ECharts**（在 `web/package.json` 中确认；若不存在则安装）。封装为 `<EChart>` 组件复用。

---

## 八、测试计划

### 8.1 单元测试

- `bg/self_check_worker_test.go`: 模型选择器（去重、Top10 计算）
- `bg/self_check_worker_test.go`: 会话生成器（3轮+工具调用结构）
- `admin/self_check_handlers_test.go`: 5 个 handler 的 happy path + 权限

### 8.2 集成测试

- DB 迁移能在本地 252 PG 上正确跑
- Worker 启动后能在 5 分钟内产生至少 1 条 self_check_runs 记录
- 故意让一个模型失败 → 验证故障隔离器触发并写入 upstream_result

### 8.3 本地端到端验证

1. 部署到本地（用 `local-deploy-test` skill）
2. 启用自检
3. 观察前端 "系统监测" Tab 出现图表
4. 触发 50 次测试 → 验证页面刷新有数据
5. 模拟一个失败 → 验证故障隔离被触发

---

## 九、上线计划

| 步骤 | 内容 | 验证标准 |
|------|------|----------|
| 1 | 创建迁移文件 + 在 252 上 apply | 表创建成功 |
| 2 | 实现 worker + cmd 集成 | 启动日志无 panic |
| 3 | 实现 admin handler + 注册路由 | curl 5 个 API 全部 200 |
| 4 | 实现前端 SelfCheckPanel + Dashboard Tab | 页面正常渲染 |
| 5 | 本地完整端到端测试 | 50 次测试有数据，UI 显示 |
| 6 | 配置默认特色模型 | admin UI 可配置 |
| 7 | git commit + push | CI 通过 |

---

## 十、风险与限制

1. **token 成本**: 10 模型 × 60秒/次 × 2000 tokens/次 ≈ 1.2M tokens/hour，需监控成本
2. **Provider 直连凭据安全**: 需要解密 provider api_key，确保解密 key 在内存中只短暂存在
3. **并发限流**: 5 并发上限防止突发流量
4. **DB 写入压力**: 1440 rows/hour，可接受；保留 30 天后自动清理
5. **循环依赖**: worker 调用 llm.kxpms.cn 本地服务，需确保 gateway 已完全启动后再启 worker

---

## 十一、相关文件清单（实现时新建）

| 文件 | 说明 |
|------|------|
| `/sql/migrations/domain/XXX_self_check.sql` | 数据库迁移 |
| `/bg/self_check_worker.go` | 后台 worker |
| `/bg/self_check_worker_test.go` | worker 单元测试 |
| `/admin/self_check_handlers.go` | admin API |
| `/admin/self_check_handlers_test.go` | handler 单元测试 |
| `/settings/spec_self_check.go` | 配置 spec |
| `/cmd/gateway/main.go` | 集成启动代码 (修改) |
| `/admin/handler.go` | 注册新路由 (修改) |
| `/web/src/views/SelfCheckView.vue` 或 `components/SelfCheckPanel.vue` | 前端组件 |
| `/web/src/api-selfcheck.ts` | 前端 API 客户端 |
| `/web/src/views/DashboardView.vue` | 添加 Tab (修改) |
| `/web/src/router.ts` | 注册独立路由 (修改) |

---

## 十二、附录：默认特色模型

```json
[
  "minimax-m2.7",
  "glm-5.2",
  "mimo-v2.5",
  "claude-sonnet-5",
  "gpt-5.4",
  "gpt-5.6-luna",
  "deepseek-v4-pro"
]
```

Top N 自动补充：从 `provider_models` × `credential_model_bindings` JOIN 中，按
`is_routable=true` 且 `lifecycle=active` 过滤后，按最近 7 天调用量排序补充到 10 个。

---

**文档结束。等待评审后进入实现阶段。**