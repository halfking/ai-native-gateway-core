# Mock Probe 通道——优化方案（v2）

> 范围：基于 `02-current-code.md` 的真实差距，调整后的实施蓝图
> 适用版本：gateway-v2 主线
> **协议范围（已锁定）**：本轮只支持 OpenAI Chat Completions（`POST /v1/chat/completions`），其它协议走 follow-up
> **矩阵维度（已锁定）**：2x2 = (mock-fast, mock-slow) × (stream, non-stream)，单轮 4 次探测；协议不再做变量
> 设计原则：**走主 mux + 鉴权旁路**（贴合现状，最小侵入）、**进程内注册**（不另起子进程，便于运维）、**默认关闭**（生产安全）、**mock 数据全在内存**（不污染 `providers` 表）

## 一、总体架构

```
┌─────────────────────────────────────────────────────────────┐
│                  cmd/gateway-v2/main.go : main mux          │
│  ┌───────────────────────────────────────────────────────┐  │
│  │ 鉴权中间件 (Bearer API key)                          │  │
│  │   ├─ 命中 mock-probe-client 白名单 → mock 旁路       │  │
│  │   └─ 其它 → 现有 api_keys + 限流器 → 真实路由          │  │
│  └───────────────────────────────────────────────────────┘  │
│  ┌────────────────┬────────────────┬────────────────────┐  │
│  │ /v1/chat/      │ /v1/messages   │ /v1/responses      │ … │
│  │ completions    │ (Anthropic)    │ (OpenAI Response)  │   │
│  └────────────────┴────────────────┴────────────────────┘   │
│  ┌─────────────────────┬──────────────────────────────────┐│
│  │ /mock/v1/chat/      │ /mock/v1/messages                 ││ ← Mock 端点（仅 mock-probe-client 可达）│
│  │ completions/{fast,  │ /{fast,slow}                       ││
│  │ slow}               │                                    ││
│  └─────────────────────┴──────────────────────────────────┘│
└─────────────────────────────────────────────────────────────┘
                ▲                                ▲
                │ HTTP (Bearer mock-probe-client)│
                │                                │
┌───────────────────────────────────────────────────────────────┐
│  internal/mockprobe/runner.go (后台 goroutine)               │
│  ┌──────────────────────────────────────────────────────┐   │
│  │ ticker := time.NewTicker(MockProbeInterval)          │   │
│  │ for {                                                 │   │
│  │   for _, sup := range []string{"mock-fast","mock-slow"}│  │
│  │     for _, stream := range []bool{false, true}        │   │
│  │       runOneProbe(sup, stream)                       │   │
│  │   }                                                   │   │
│  │   <-ticker.C                                          │   │
│  │ }                                                     │   │
│  └──────────────────────────────────────────────────────┘   │
│  注册到 internal/shutdown.Manager.KindNonStream              │
└───────────────────────────────────────────────────────────────┘
        │
        ▼
┌───────────────────────────────────────────────────────────────┐
│  数据落地：                                                   │
│   - 指标：internal/observability/metrics.go                    │
│      mock_probe_request_total{scope="mock_probe",             │
│        supplier, stream, status}                              │
│      mock_probe_latency_seconds_bucket{scope="mock_probe",    │
│        supplier, stream}                                      │
│   - 历史：migrations/036_mock_probe_history.sql                │
│      走 request_logs 的旁路，不入 request_logs                │
└───────────────────────────────────────────────────────────────┘
```

## 二、关键设计决策

| 决策点 | 选择 | 理由 |
|--------|------|------|
| Mock 供应商落点 | **主 mux 上注册专用 `/mock/v1/*` 端点**（不注册到 provider catalog） | Provider Store 无 Invoke 契约，注册 catalog 等于造伪数据；主 mux 是主入口可验全链路 |
| Mock 客户端走哪 | **走网关自身入口**（`127.0.0.1:<gateway port>/mock/v1/...`） | 验整条 HTTP 链路；不走 loopback 反而验不出限流器/中间件 |
| 鉴权旁路 | **新增 `mock-probe-client` 系统白名单**，`Authorization: Bearer mock-probe-client` 且 `provider_code ∈ {mock-fast, mock-slow}` 才放行；其它走原 api_keys 校验 | 防止外部嗅探使用 mock 端点 |
| 进程模型 | **进程内 goroutine**（不另起子进程） | 用户明确要求"go 实现统一部署" |
| 存储 | **内存缓存 + `mock_probe_history` 表**（双写：指标 + 表） | 指标实时拉取，表用于历史回溯/审计 |
| 默认开关 | **`MockProbeEnabled=false`** | 生产环境安全 |
| Mock 供应商是否在 Admin 列表显示 | 由 `MockProbeHideInAdmin=true`（默认 true）控制；true 时 listProviders 自动过滤 `code LIKE 'mock-%'` | 满足"关闭时不可见"需求 |
| 优雅停机 | mock probe runner 注册为 `shutdown.KindNonStream`，停机顺序：先停 runner → 关 mux | 复用现有 hook 框架 |

## 三、数据契约

### 3.1 配置项（写入 `config/config.go:14` 的 `Config` 结构体）

```go
type Config struct {
    // ... 现有字段 ...

    // Mock Probe 通道（默认全关）
    MockProbeEnabled         bool          `yaml:"mock_probe_enabled"`          // 默认 false
    MockProbeHideInAdmin     bool          `yaml:"mock_probe_hide_in_admin"`   // 默认 true
    MockProbeInterval        time.Duration `yaml:"mock_probe_interval"`         // 默认 30s
    MockProbeFailureThreshold int          `yaml:"mock_probe_failure_threshold"` // 默认 3
}
```

环境变量前缀沿用 `LLM_GATEWAY_*`，具体映射交给现有 YAML loader（不在本轮扩 loader）。

### 3.2 `migrations/036_mock_probe_history.sql` 表

```sql
CREATE TABLE IF NOT EXISTS mock_probe_history (
    id              BIGSERIAL,
    probe_time      TIMESTAMPTZ NOT NULL DEFAULT now(),
    channel         TEXT NOT NULL,            -- e.g. 'mock-fast:stream'
    supplier        TEXT NOT NULL,            -- 'mock-fast' | 'mock-slow'
    stream          BOOLEAN NOT NULL,
    protocol        TEXT NOT NULL,            -- 'openai' | 'anthropic' | 'response' | 'gemini'
    latency_ms      INTEGER NOT NULL,
    status_code     INTEGER NOT NULL,
    error_code      TEXT,                     -- nullable，e.g. 'timeout' / 'http_500'
    request_id      TEXT,                     -- 来自上游 mock 响应
    failure_streak  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (id, probe_time)
) PARTITION BY RANGE (probe_time);

-- 默认分区兜底
CREATE TABLE IF NOT EXISTS mock_probe_history_default
    PARTITION OF mock_probe_history DEFAULT;

-- 按天分区函数（沿用 035 的写法）
CREATE OR REPLACE FUNCTION mock_probe_history_daily_partition()
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    d DATE := current_date;
    pname TEXT := format('mock_probe_history_%s', to_char(d, 'YYYYMMDD'));
BEGIN
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS %I PARTITION OF mock_probe_history FOR VALUES FROM (%L) TO (%L)',
        pname, d::timestamptz, (d + INTERVAL '1 day')::timestamptz
    );
END $$;

SELECT mock_probe_history_daily_partition();

-- 索引
CREATE INDEX IF NOT EXISTS idx_mock_probe_history_supplier_time
    ON mock_probe_history (supplier, probe_time DESC);
CREATE INDEX IF NOT EXISTS idx_mock_probe_history_channel_time
    ON mock_probe_history (channel, probe_time DESC);
```

### 3.3 指标（新增 `internal/observability/metrics.go`）

```go
package observability

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

var (
    MockProbeRequestTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "mock_probe_request_total",
            Help: "Total number of mock probe requests dispatched.",
        },
        []string{"scope", "supplier", "stream", "status"},
    )
    MockProbeLatencySeconds = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "mock_probe_latency_seconds_bucket",
            Help:    "Mock probe request latency in seconds.",
            Buckets: prometheus.DefBuckets,
        },
        []string{"scope", "supplier", "stream"},
    )
)

// ScopeMockProbe 是 scope 标签的统一常量
const ScopeMockProbe = "mock_probe"
```

> 所有 mock probe 写入指标时强制 `scope = ScopeMockProbe`，避免误用其它 scope 名。

### 3.4 API

新增 2 个 HTTP 端点（仅 mock-probe-client 可达）：

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/mock/v1/chat/completions/{fast\|slow}` | OpenAI Chat Completions 协议 mock |
| POST | `/mock/v1/messages/{fast\|slow}` | Anthropic Messages 协议 mock |
| POST | `/mock/v1/responses/{fast\|slow}` | OpenAI Response 协议 mock |
| POST | `/mock/v1beta/models/{fast\|slow}:generateContent` | Gemini 协议 mock |

请求体处理：
- 提取 `messages` / `contents` / `input` 字段
- 校验 token 计数（粗略：len(text)/4）
- 响应：`fast` 立即返回；`slow` 模拟 800ms±200ms 抖动；流式按 chunk 写出
- 图片附件：若请求体含 `image_url` / `source`（Anthropic base64），回显一个简短的"已接收图片 <size> 字节"字符串

### 3.5 内部模块

```
internal/providers/mock/
    fast.go              # mock-fast 共享延迟与响应构造
    slow.go              # mock-slow
    fast_test.go
    slow_test.go

internal/mockprobe/
    client.go            # mock-probe-client 凭证定义 + 探测 HTTP 客户端
    runner.go            # 2x2 调度 + 指标 + 历史写入
    runner_test.go

internal/auth/
    mockprobe_bypass.go      # 鉴权旁路（Bearer mock-probe-client → 放行 mock 端点）
    mockprobe_bypass_test.go

internal/observability/
    metrics.go           # mock_probe_* 指标注册 + scope 常量
    metrics_test.go
```

## 四、实施步骤（按依赖顺序）

### Step 1：数据层
- 写 `migrations/036_mock_probe_history.sql`
- 在 `migrations/` 的 README（如有）追加一行

### Step 2：配置层
- `config/config.go:14` 加 4 个字段
- `cmd/gateway-v2/main.go` 启动时打印 4 个值（debug log）

### Step 3：凭证旁路
- `internal/auth/mockprobe_bypass.go`：导出 `IsMockProbeClient(r *http.Request) bool`、`EnforceMockProbeScope(providerCode string) bool`
- 在 `cmd/gateway-v2/main.go:400` mux 装配点前后包装 mock 端点专用中间件

### Step 4：Mock 供应商
- `internal/providers/mock/fast.go`、`slow.go`：导出 `Handler(supplier string) http.HandlerFunc`
- `cmd/gateway-v2/main.go` 把 4 个 `/mock/v1/.../{fast,slow}` 端点注册到主 mux，挂鉴权旁路中间件

### Step 5：Mock 客户端
- `internal/mockprobe/client.go`：`type Client struct{ ... }`，方法 `Probe(ctx, supplier, stream, protocol) (latency time.Duration, status int, err error)`
- `internal/mockprobe/runner.go`：`type Runner struct{ ... }`，方法 `Start(ctx)`、`Stop(ctx)`，注册到 `internal/shutdown.Manager`

### Step 6：指标 + 历史
- `internal/observability/metrics.go` 写入 3.3
- `internal/mockprobe/runner.go` 双写：先指标，再 SQL 插入（用 `pgxpool` 从 gateway-v2 已有 pool 借）

### Step 7：Admin UI 过滤
- `admin/providers.go:679` 过滤分支前加：`if MockProbeHideInAdmin { exclude codes with prefix "mock-" }`
- `admin/providers.go:536` `listProviders` SQL 加 `AND code NOT LIKE 'mock-%'`（仅当 `MockProbeHideInAdmin=true`）

### Step 8：优雅停机
- `cmd/gateway-v2/main.go` 在 shutdown.Manager.Register 后启动 runner；停机顺序沿用现有框架

## 五、测试矩阵（每个 Step 都要补）

| Step | 测试类型 | 关键断言 |
|------|----------|----------|
| 1 | 真库回归 | `psql -d llm_gateway_test -f 036_*.sql`；insert+select 回环 |
| 2 | 单测 | `Config{}` YAML 反序列化 4 字段；env override |
| 3 | 单测 | `IsMockProbeClient` 命中/未命中；`EnforceMockProbeScope` 越权拒绝 |
| 4 | httptest | 4 协议 × fast/slow × stream/non-stream = 16 子场景；图片附件回显 |
| 5 | race detector | 2x2 调度并发；stop 信号正常 |
| 6 | 真库 + promhttp | CounterVec/HistogramVec 注册成功；历史表行数 == 调度次数 |
| 7 | 单测 | `MockProbeHideInAdmin=true/false` 切换过滤行为 |
| 8 | 端到端 | SIGTERM 后 runner 退出 + 历史表无半行 |

## 六、风险与回滚

| 风险 | 缓解 |
|------|------|
| `/mock/v1/*` 被外部嗅探 | 鉴权旁路强制 `Bearer=mock-probe-client` + provider_code 前缀校验 |
| mock probe 跑满 `request_logs` 配额 | **不走 `request_logs`**，走独立 `mock_probe_history` |
| `MockProbeInterval` 配成 0 触发风暴 | YAML loader 处加 `if interval < 1s { clamp to 30s }` 兜底 |
| PG 写入慢阻塞探测 | runner 异步 channel 写入；失败仅记日志不报错 |
| 历史表分区未及时建 | 启动时调用 `mock_probe_history_daily_partition()` 兜底 |

回滚：`MockProbeEnabled=false` 即停跑；删表/删端点都不影响真实供应商与限流器。

## 七、给下一轮主 agent 的提示词骨架

```
# 任务：实现 Mock Probe 通道子系统
# 范围：docs/design/2026-09-23-mock-probe-channel/{02-current-code.md, 03-optimization-plan.md}
# 默认分支：main
# 不允许：修改 request_logs schema、修改 api_keys 表 schema、修改 Provider Store 接口

## 强制顺序
1. migrations/036_mock_probe_history.sql
2. config/config.go:14 加 4 字段
3. internal/observability/metrics.go（scope 常量 + CounterVec/HistogramVec）
4. internal/auth/mockprobe_bypass.go
5. internal/providers/mock/{fast,slow}.go
6. cmd/gateway-v2/main.go:400 注册 4 个 mock 端点 + 鉴权旁路中间件
7. internal/mockprobe/{client,runner}.go
8. admin/providers.go mock 过滤分支
9. 真库回归（llm_gateway_test）+ httptest 业务编排 + race detector

## 验收
- MockProbeEnabled=false 时：mock 端点返回 404，runner 不启动
- MockProbeEnabled=true 时：每 MockProbeInterval 跑 4 次（fast×stream, fast×non-stream, slow×stream, slow×non-stream）
- 真库验证：mock_probe_history 行数 > 0；Prometheus 抓 /metrics 看到 scope="mock_probe"
- admin/providers 列表在 MockProbeHideInAdmin=true 时不含 mock- 前缀
```