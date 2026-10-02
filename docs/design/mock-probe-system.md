# Mock 供应商 & 客户端自检系统 — 设计与实施方案

**状态**: 设计稿（待评审 → 实施）
**作者**: ZCode
**日期**: 2026-09-23
**目标仓库**: `/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4`
**里程碑**: 与现有 self-check_worker / system_health_worker / model_quality_worker 并列,作为网关内置的"端到端自检"能力

---

## 一、需求复盘与细化

### 1.1 原始需求(原文)

> /goal 系统中在部署时,会给出 2 个 mock 的供应商同步启动,并且能够响应一些测试任务,这些主要的目标是用于测试整个服务是否正常,定时通过 mock 的客户端来进行探测,检查流程是否完整。这些需要在系统中增加一个参数,是否启动 mock 操作进行探测。当这个被关闭时,这些 mock 的客户端将不会再发出任何请求,保持静默。并且 mock 的供应商也不会显示出来。
> 我们相当于增加了一个 mock 的 2x2 的测试通道,用于交叉测试网关的相关特性,确认系统运行是否正常。
> 这些探测在开关打开时是可见的,开关关闭时不可见。
> 请完善上述需求,然后对我们的代码进行对比,形成优化方案,然后继续下一步。我们需要将 mock 的供应商与客户端使用 go 实现,达成统一部署的需求。

### 1.2 需求细化(可执行的规格)

#### A. Mock 供应商(2 个)

| 名称 | 端口 | 行为模型 | 用途 |
|---|---|---|---|
| `mock-fast` | `127.0.0.1:9081` | 低延迟(20~80ms)、低错误率(1%)、无 token 抖动 | 模拟"正常路径供应商",验证快速响应的链路 |
| `mock-slow` | `127.0.0.1:9082` | 中延迟(200~600ms,带 jitter)、中错误率(3%)、偶发 5xx | 模拟"慢供应商 + 偶发故障",验证超时/重试/降级逻辑 |

**必备端点**(两个供应商都实现):
- `GET /healthz` — 返回 200 + `{"status":"ok","name":"mock-fast"}`,健康探针
- `GET /admin/state` — 返回当前 mock 的运行时状态(延迟参数、错误率、累计请求数)
- `GET /admin/stats` — 返回每个 channel(stream/non-stream × prompt size)的统计
- `POST /v1/chat/completions` — 支持 stream 与 non-stream 两种响应模式;响应格式严格符合 OpenAI Chat Completions 协议(便于网关直接复用现有 provider 客户端)

**关键约束**:
- 绑定 `127.0.0.1`,不暴露外部网络(避免被外部恶意调用)
- 启动/停止与网关主进程同生命周期
- **不**写入 `providers` 数据库表(避免污染生产数据);改用内存中的伪 provider 注册表
- 端口可通过环境变量覆盖,但默认值固定为 9081/9082

#### B. Mock 客户端(自检探针)

**触发模型**:2×2 通道矩阵
| 维度 | 取值 |
|---|---|
| 供应商 | mock-fast / mock-slow |
| 流式 | stream / non-stream |

共 **4 个探针任务**,每个任务独立运行、定时(默认 30s)轮询;通过 `mock-probe-client` 系统白名单凭证访问网关(`/v1/chat/completions`),完整走一遍认证 → 路由 → 限流 → Mock 供应商 → 流式协议解析 → 响应回写 的全链路。

**响应校验规则**:
- non-stream:必须返回合法 JSON,包含 `choices[0].message.content` 与 `usage.prompt_tokens/completion_tokens`
- stream:必须返回合法的 SSE 序列(`data: {json}\n\n` × N + `data: [DONE]\n\n`),且 chunk 数 ≥ 1,终止帧必须存在
- 任何字段缺失/格式错误 → 标记 `result=fail`,记入 `mock_probe_history`

**审计指标**:
- 4 通道分别的成功率(P50/P95 延迟、成功计数、失败计数)
- 整体健康度(4 通道全部 success 才算 healthy)
- 失败率超阈值(默认 25%,连续 3 轮)→ 写 `mock_probe_degraded` 事件,便于运维定位

#### C. 配置开关

| 参数 | 类型 | 默认 | 含义 |
|---|---|---|---|
| `mock_probe.enabled` | bool | `true`(开发)/ `false`(生产) | 总开关:启用 Mock 供应商 + 探针 |
| `mock_probe.hide_in_admin` | bool | `false` | 是否在 `/admin/providers` 与 Web UI 中隐藏 mock-* 供应商 |
| `mock_probe.interval_seconds` | int | `30` | 探针轮询间隔 |
| `mock_probe.failure_threshold` | float | `0.25` | 触发 degraded 事件的失败率阈值 |
| `mock_probe.client_system_key` | string | `mock-probe-client` | 系统白名单凭证名(绕过用户配额) |

**当 `enabled=false` 时**:
- Mock 供应商 HTTP 服务**不启动**(端口不被占用,节省资源 + 暴露面最小化)
- 探针 ticker **完全不启动**(`Start()` 在配置检查处直接 return)
- 数据库 `mock_probe_history` 表**不写入任何新行**(保留历史数据,但不增新行)
- `/admin/providers` 中**完全不显示** mock-fast / mock-slow(若 `hide_in_admin=true` 或 `enabled=false`)
- Web UI / 监控面板的 mock 探针卡片**直接隐藏**(前端读同一开关)

**当 `enabled=true` 时**:
- 启动两个 HTTP 服务,端口监听 9081/9082
- 启动探针 ticker,每 30s 跑 4 个探针
- 写入 `mock_probe_history` 表
- 探针卡片在 Web UI 中显示,最近 24h 成功率实时刷新

### 1.3 隔离性边界(关键安全约束)

**生产系统不能感知 mock 的存在**:
1. **计费**:Mock 探针走 `mock-probe-client` 系统白名单凭证,在认证层标记 `is_system=true`,计费模块看到该标记**直接跳过**(已知实现:`internal/auth/middleware.go` 中的 `IsSystemRequest()` 检查)
2. **审计**:`request_logs` 表的 `scope` 字段标记为 `mock_probe`,审计查询默认按 `scope != 'mock_probe'` 过滤
3. **监控**:`request_logs` 与 Prometheus 指标的 `labels` 中标记 `scope="mock_probe"`,dashboard 默认隐藏该 scope
4. **路由表**:`providers` 表**不**写入 mock-fast/slow;网关内部用一个独立的 `mockProviders` 内存注册表供 mock 探针专用,不污染生产路由
5. **限流**:`mock-probe-client` 在限流器白名单中,**不受用户级 RPS 限制**

### 1.4 优雅停机

停机序列(由 `main.go` 的 signal handler 编排):
1. 收到 SIGTERM/SIGINT
2. 停止 HTTP 服务器(主网关),不再接受新请求
3. **停止 mock 探针 ticker**(`Stop()`,等待 in-flight 探针完成,最多 5s 超时)
4. **优雅关闭 mock 供应商 HTTP 服务**(`http.Server.Shutdown(ctx)`,5s 超时)
5. 关闭 DB 连接池、trace flush、其他后台 worker

---

## 二、与现有代码的对比分析

通过扫描 `/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4/cmd/gateway/main.go` 与 `bg/`、`internal/loopback/` 等目录,得到以下事实:

### 2.1 已有的可复用资产

| 现有组件 | 位置 | 可复用点 |
|---|---|---|
| `loopback.GatewayBase()` | `internal/loopback/loopback.go:111` | 返回 `http://127.0.0.1:8787`(默认网关端口),探针可作为 baseURL 默认值 |
| `SelfCheckWorker` | `bg/self_check_worker.go` | 周期性自检 worker 的样板代码:startOnce/stopOnce/lifecycleMu/wg,触发通道,日志结构。可作为 mock 探针 worker 的模板 |
| `SystemHealthWorker` | `bg/system_health_worker.go:4555` | 30s 窗口化健康监控,如何收集每个通道的成功率、失败率。可参考其统计模式 |
| `settings.Global.EffectiveValue(scope, key, default)` | `cmd/gateway/main.go:4563` | 配置读取的标准入口,支持 scope + key 的两级 namespace。`mock_probe.*` 直接接入 |
| `shouldStartNewProbeWorkers(selfCheckAPIKey)` | `cmd/gateway/main.go:4551` | 新探针 worker 的总闸门,MQ/SH/SC 都汇入此处 |
| `armor.NewMockJudge(0.0, "mock")` | `cmd/gateway/main.go:5701/5706` | 已存在的 mock 模式工厂(在 armor 包内);证明项目已认可"mock 作为兜底"的模式 |
| `httptest.NewServer` 习惯 | 项目内多处测试 | mock HTTP server 的惯用法,可用 `httptest` 或更轻量的 `net/http` 自建 |
| `request_logs` 表的 `scope` 字段 | `internal/persistence/migrations/` | 已有 scope 维度,直接复用 |

### 2.2 现有代码中**没有**的能力(本设计要补齐)

| 缺口 | 现状 | 本设计补齐方式 |
|---|---|---|
| **生产网关上自带的 mock HTTP server** | 完全缺失;`armor.NewMockJudge` 只在 armor 包内做兜底,不暴露 HTTP | 新增 `bg/mockprovider/server.go`:在网关进程内启动 2 个 `http.Server`,监听 127.0.0.1 |
| **2×2 探针调度器** | `SelfCheckWorker` 是单模型、单通道(只 ping + tool-call);`SystemHealthWorker` 是被动统计 | 新增 `bg/mockprobe/probe_worker.go`:周期性跑 4 个探针任务,写入 `mock_probe_history` |
| **mock 探针的独立审计表** | `request_logs` 表虽可用,但被 `scope` 标记后,SQL 过滤效率低且与生产日志混表 | 新增 `mock_probe_history` 表(独立 schema),4 通道 × N 行历史 |
| **mock 探针的 degraded 事件** | 无 | 新增 `mock_probe_degraded` 表(或复用 events 表的 kind 列) |
| **mock 供应商在 admin 的可见性开关** | 无 | 通过 `mock_probe.hide_in_admin` + `/admin/providers` SQL 的 WHERE 过滤实现 |
| **mock 探针的 Web UI 卡片** | 无 | 新增前端组件 `MockProbeCard.vue`,从 `/admin/mock_probe/status` 拉数据 |

### 2.3 现有代码的"反面教训"(避免重蹈)

从最近的 fix commits 总结出的纪律:

1. **不要把监控探针的失败计入生产降级**(`fix(streaming): monitor 包装层吞掉 GateWriter 复用`)→ mock 探针的所有日志/指标必须打 `scope="mock_probe"`,并在 dashboard 与告警规则中**显式排除**
2. **flush 延迟要避开监控窗口**(`fix(trace): FlushToPG 首试延迟 250ms`)→ mock 探针的 batch flush 不要和 `request_logs` 的 200ms 窗口对齐,避免抢锁
3. **批处理串行化要算上 row lock**(`fix(trace): FlushToPG 250ms→1.2s`)→ `mock_probe_history` 的批量插入要估算 `api_keys` 行锁的串行化开销,避免在主路径上被拖累
4. **wrap 层不能吞掉复用语义**(`fix(streaming): 终态闩落错 gate`)→ mock 供应商的 SSE 响应必须经过与生产相同的 `streaming.Monitor`,不能绕过;这样才能真正测到生产路径的 bug
5. **真库回归测试必须落地**(`fix(streaming): 双终态帧 wire 级回归测试闭环`)→ mock 探针的每个通道必须有 wire-level 集成测试,不能只看返回 200 就过

### 2.4 与现有 self_check_worker / system_health_worker 的边界

| 关注点 | self_check_worker | system_health_worker | **本设计的 mock_probe_worker** |
|---|---|---|---|
| 触发 | 30s 周期 + 手动 trigger | 被动(其他 worker 写入 metrics) | **30s 周期 + 启动时跑一轮** |
| 目标 | 验证模型可用性 + tool-call 解析 | 监控 GDRT H badge | **验证 mock 通道全链路通畅** |
| 调用方式 | HTTP → 网关 `/v1/chat/completions` | 不直接调用 | **HTTP → 网关 `/v1/chat/completions` → mock 供应商** |
| 输出 | log + memory state | Prometheus gauge | **`mock_probe_history` 表 + Prometheus gauge(`scope=mock_probe`)** |
| 失败处理 | 软失败,记 fault | 触发 GDRT 降级 | **写 `mock_probe_degraded` 事件** |
| 生产开关 | 默认开启 | 默认开启 | **默认关闭(生产) / 默认开启(开发)** |

**边界明确**:mock_probe_worker 不替代 self_check 或 system_health,而是作为**第三层独立自检**,专门覆盖"mock 通道"这一非生产路径。三个 worker 通过不同的 metrics path 输出,避免互相干扰。

---

## 三、优化方案(本设计的核心创新)

### 3.1 核心思路:**复用生产路径,而非旁路**

> 现有 mock 设计(若有)倾向于"绕开生产代码,直接调内部函数"。但这无法验证认证、限流、SSE 协议解析、监控包装等真正容易出 bug 的环节。
>
> 本设计**强制 mock 探针走完整的 HTTP → 网关 → mock 供应商**链路,这样它能捕获到的 bug 类型与生产请求**完全一致**。

具体实现:
- mock 供应商实现**真实的 OpenAI Chat Completions 协议**(包括 SSE 格式、chunk 结构、`[DONE]` 终止帧)
- mock 探针**不**直接调用 `streaming.Switch` 或 `auth.Middleware`,而是通过 HTTP client 访问 `http://127.0.0.1:8787/v1/chat/completions`
- 这样 mock 探针能捕获的 bug 包括:路由选择错误、限流器误杀、SSE chunk 解析崩溃、monitor 包装层吞数据、计费模块对系统凭证的判断错误 等

### 3.2 优化点 1:**配置粒度细化**

原始需求是"一个开关",本设计细化为 5 个开关(见 1.2 节)。这样:
- 开发环境:`enabled=true, hide_in_admin=false`,完全透明
- 生产环境:`enabled=false`,完全静默,资源开销为 0
- 灰度环境:`enabled=true, hide_in_admin=true`,探针数据可观测但不在用户视角暴露

### 3.3 优化点 2:**失败率阈值 + 滑动窗口**

不是"任一失败就告警",而是**滑动窗口内的失败率**才触发 degraded:
- 默认窗口大小 = 连续 3 轮(约 90s)
- 失败率阈值 = 25%(4 通道中只要 1 个持续失败就可能触发)
- 避免偶发抖动导致误报

### 3.4 优化点 3:**审计隔离的纵深防御**

不是"在 SQL 里加 WHERE scope != 'mock_probe'"一层过滤,而是:
1. **凭证层**:`mock-probe-client` 在 `internal/auth` 中标记 `IsSystemRequest=true`
2. **计费层**:看到该标记直接跳过
3. **审计层**:`mock_probe_history` 是**独立表**,根本不进 `request_logs`
4. **指标层**:Prometheus labels 带 `scope="mock_probe"`,dashboard 默认排除
5. **路由层**:`providers` 表**不写** mock 供应商;mock 供应商走内存中的 `mockRouter` 注册表

5 层防御,任何一层失守都不会污染生产。

### 3.5 优化点 4:**优雅停机的并行化**

mock 供应商 HTTP 关闭 与 mock 探针 worker 关闭 **并行进行**,而不是串行:
```go
shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
var wg sync.WaitGroup
wg.Add(2)
go func() { defer wg.Done(); _ = mockProbeWorker.Stop() }()
go func() { defer wg.Done(); _ = mockProviderServer.Shutdown(shutdownCtx) }()
wg.Wait()
```

这样停机时间不会变成"探针关闭 5s + 服务器关闭 5s = 10s",而是 max(5s, 5s) = 5s。

### 3.6 优化点 5:**Prometheus 指标的精确命名**

新增 metrics(全部带 `scope="mock_probe"` label):
- `mock_probe_request_total{channel, result}` — 计数器
- `mock_probe_request_duration_seconds{channel}` — histogram
- `mock_probe_channel_up{channel}` — gauge (1=up, 0=down)
- `mock_probe_degraded_total` — 计数器(degraded 事件触发次数)

dashboard 默认排除 `scope="mock_probe"`,但运维可单独打开。

---

## 四、文件结构与目录布局

```
cmd/gateway/main.go                            # 在 shouldStartNewProbeWorkers 中汇入 mock 探针;在 signal handler 中汇入优雅停机
bg/mockprovider/
  ├── server.go                                # MockProviderServer:启动 2 个 HTTP server
  ├── handler.go                               # /healthz, /admin/state, /admin/stats, /v1/chat/completions
  ├── profile.go                               # Profile:配置延迟/错误率/抖动
  └── server_test.go                           # wire-level 测试
bg/mockprobe/
  ├── probe_worker.go                          # MockProbeWorker:30s 周期 × 4 通道
  ├── probe_task.go                            # 4 个探针任务的定义(channel 矩阵)
  ├── response_validator.go                    # 校验 non-stream JSON / SSE 序列
  ├── history_store.go                         # 写入 mock_probe_history 表
  └── probe_worker_test.go                     # 集成测试
internal/persistence/migrations/0007XX_mock_probe.up.sql    # 创建 mock_probe_history + mock_probe_degraded 表
internal/auth/mock_probe_client.go             # 注册系统白名单凭证 mock-probe-client
web/admin/src/components/MockProbeCard.vue    # 前端卡片
web/admin/src/api/mockProbe.ts                 # 前端 API
docs/design/mock-probe-system.md               # 本文档
docs/runbook/mock-probe-degraded.md            # degraded 事件运维手册
```

---

## 五、关键代码契约(占位,不直接落地)

### 5.1 MockProviderServer 启动契约

```go
// bg/mockprovider/server.go
type Server struct {
    fast *providerInstance  // 127.0.0.1:9081
    slow *providerInstance  // 127.0.0.1:9082
    log  *slog.Logger
}

func NewServer(cfg Config, log *slog.Logger) (*Server, error) {
    // 校验 cfg.Enabled;若 false,直接 return &Server{},不分配端口
    // 两个 providerInstance 各自监听 127.0.0.1:9081/9082
    // 失败时返回 error,不 panic
}

func (s *Server) Start(ctx context.Context) error {
    // 并发启动两个 http.Server.ListenAndServe
    // 端口冲突时返回 error(由 main.go 决定是否 fatal)
}

func (s *Server) Shutdown(ctx context.Context) error {
    // 并发关闭两个 http.Server.Shutdown
}
```

### 5.2 MockProbeWorker 调度契约

```go
// bg/mockprobe/probe_worker.go
type Worker struct {
    cfg          Config
    baseURL      string              // http://127.0.0.1:8787/v1
    apiKey       string              // mock-probe-client 的 key
    channels     [4]Channel          // fast×stream, fast×non-stream, slow×stream, slow×non-stream
    historyStore *HistoryStore
    httpClient   *http.Client
    log          *slog.Logger

    stopCh    chan struct{}
    startOnce sync.Once
    stopOnce  sync.Once
    wg        sync.WaitGroup
}

func (w *Worker) Start(parent context.Context) {
    if !w.cfg.Enabled { return }  // 关键:开关关闭时不启动
    // 启动时跑一轮(快速反馈)
    // 进入 30s ticker
}

func (w *Worker) Stop(ctx context.Context) error {
    // 关闭 stopCh,等待 wg,超时由 ctx 控制
}

func (w *Worker) probeOnce(ctx context.Context) {
    // 并发跑 4 个通道
    // 每个通道:发请求 → 解析响应 → 校验 → 写 history
    // 失败率超阈值时写 degraded 事件
}
```

### 5.3 响应校验契约

```go
// bg/mockprobe/response_validator.go
type ValidationResult struct {
    OK       bool
    Reason   string  // 当 OK=false 时
    Latency  time.Duration
    TokensIn int
    TokensOut int
}

func ValidateNonStream(body []byte) ValidationResult {
    // 解析 JSON
    // 必须包含 choices[0].message.content
    // 必须包含 usage.prompt_tokens, usage.completion_tokens
}

func ValidateStream(body []byte) ValidationResult {
    // 按 \n\n 拆 chunk
    // 至少 1 个 data: 行 + 1 个 data: [DONE]
    // 每个 data: 行解析为合法 JSON
}
```

### 5.4 数据库 schema(占位)

```sql
-- 0007XX_mock_probe.up.sql
CREATE TABLE mock_probe_history (
    id              BIGSERIAL,
    probed_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    channel         TEXT NOT NULL,        -- 'fast_stream', 'fast_nonstream', 'slow_stream', 'slow_nonstream'
    result          TEXT NOT NULL,        -- 'success', 'fail'
    fail_reason     TEXT,
    latency_ms      INT NOT NULL,
    tokens_in       INT,
    tokens_out      INT,
    raw_status      INT,                  -- HTTP status code
    raw_body_sha    BYTEA,                -- 响应体 SHA256(可选,debug 用)
    PRIMARY KEY (probed_at, id)
);
CREATE INDEX mock_probe_history_channel_time_idx ON mock_probe_history (channel, probed_at DESC);

CREATE TABLE mock_probe_degraded (
    id              BIGSERIAL PRIMARY KEY,
    detected_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    failure_rate    REAL NOT NULL,
    failed_channels TEXT[] NOT NULL,       -- 例如 ['slow_stream']
    window_size     INT NOT NULL,
    resolved_at     TIMESTAMPTZ
);
CREATE INDEX mock_probe_degraded_detected_at_idx ON mock_probe_degraded (detected_at DESC);
```

---

## 六、实施步骤(分阶段)

### 阶段 1:基础设施(预计 1 天)
- [ ] 新增 `internal/persistence/migrations/0007XX_mock_probe.up.sql`(两个表 + 索引)
- [ ] 在 `cmd/gateway/main.go` 的 migration 链中加入新文件
- [ ] 新增 `internal/auth/mock_probe_client.go`,注册系统白名单凭证

### 阶段 2:MOCK 供应商 HTTP 服务(预计 1.5 天)
- [ ] 新增 `bg/mockprovider/profile.go`(Profile 结构:延迟、错误率、jitter)
- [ ] 新增 `bg/mockprovider/handler.go`(4 个端点)
- [ ] 新增 `bg/mockprovider/server.go`(启动/停止)
- [ ] 新增 `bg/mockprovider/server_test.go`(wire-level:非流响应、SSE chunk 序列、错误注入)
- [ ] 在 `cmd/gateway/main.go` 中接入启动/停止

### 阶段 3:MOCK 探针 worker(预计 2 天)
- [ ] 新增 `bg/mockprobe/probe_task.go`(4 通道定义)
- [ ] 新增 `bg/mockprobe/response_validator.go`(流式/非流式校验)
- [ ] 新增 `bg/mockprobe/history_store.go`(写入 history 表)
- [ ] 新增 `bg/mockprobe/probe_worker.go`(30s ticker + 4 通道并发)
- [ ] 新增 `bg/mockprobe/probe_worker_test.go`(集成测试:mock 失败时 worker 能正确捕获)
- [ ] 在 `cmd/gateway/main.go` 中接入启动/停止 + 优雅停机编排

### 阶段 4:配置 + 可见性(预计 1 天)
- [ ] 在 settings 注册 `mock_probe.enabled / hide_in_admin / interval_seconds / failure_threshold`
- [ ] `/admin/providers` 的 SQL 查询添加 WHERE 过滤(仅在 `hide_in_admin=true` 时生效)
- [ ] `/admin/mock_probe/status` 新端点:返回 4 通道的最近一轮结果 + 24h 成功率

### 阶段 5:前端卡片(预计 1 天)
- [ ] 新增 `web/admin/src/api/mockProbe.ts`
- [ ] 新增 `web/admin/src/components/MockProbeCard.vue`(在 admin dashboard 中显示)
- [ ] 当 `enabled=false` 时,组件直接隐藏(读 `enabled` 字段)

### 阶段 6:可观测性 + 运维手册(预计 0.5 天)
- [ ] 新增 Prometheus metrics(4 个,见 3.6)
- [ ] 新增 `docs/runbook/mock-probe-degraded.md`(如何响应 degraded 事件)

### 阶段 7:集成测试 + 部署(预计 1 天)
- [ ] 端到端集成测试:`make test-integration-mock-probe`
- [ ] 在 dev 环境部署,验证 4 通道探针写入正常
- [ ] 验证 `enabled=false` 时端口未被占用、ticker 未启动

**总工作量**:约 8 个工作日(1 人)

---

## 七、风险与回滚

### 7.1 风险清单

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| 端口 9081/9082 冲突(部署机器上其他服务占用) | 中 | Mock 供应商无法启动,探针 fail | 通过环境变量 `MOCK_FAST_PORT` / `MOCK_SLOW_PORT` 覆盖;启动时检测冲突并 warn |
| Mock 供应商的 SSE 实现与生产 OpenAI 不一致,误判为 fail | 中 | 探针持续 degraded | wire-level 测试覆盖 SSE chunk 格式;参考 `internal/streaming/sse.go` 的生产实现 |
| `mock-probe-client` 系统凭证被滥用(被外部攻击者用于免费调用真实供应商) | 低 | 安全风险 | mock 探针走的是 mock 供应商,真实供应商通过网关路由表过滤;且 mock 探针绑死 baseURL=http://127.0.0.1:8787/v1,无法从外部访问 |
| Mock 探针被计入生产 GDRT 降级判断 | 中 | 误降级 | Prometheus 指标全部带 `scope="mock_probe"`;GDRT 计算 SQL 加 `WHERE scope != 'mock_probe'` |
| Mock 探针的 batch flush 与 `request_logs` 抢锁 | 低 | 探针写入延迟 | mock 探针写独立的 `mock_probe_history` 表,不与 `request_logs` 共享;batch size=4(一次写满 4 通道) |
| 前端卡片暴露内部 mock 供应商名 | 低 | 信息泄露 | 前端读 `hide_in_admin` 字段,默认隐藏卡片;且 `/admin/mock_probe/status` 端点鉴权 |

### 7.2 回滚方案

- **配置回滚**:`mock_probe.enabled=false`,立即停止探针 ticker 与 HTTP 服务,无需重启
- **代码回滚**:删除新增的 6 个 Go 文件 + 1 个 SQL migration + 2 个前端文件;`cmd/gateway/main.go` 恢复原状
- **数据回滚**:`DROP TABLE mock_probe_history / mock_probe_degraded`(若有数据先归档)

### 7.3 灰度策略

- **第一周**:仅 dev 环境启用,验证 4 通道探针数据正确
- **第二周**:staging 环境启用,验证 Prometheus metrics + 前端卡片
- **第三周**:生产灰度启用(可在 1% 流量上跑),通过 `mock_probe.enabled=true` + `mock_probe.hide_in_admin=true` 控制可见性
- **稳定后**:默认生产 `enabled=true, hide_in_admin=false`(全量),但保留开关以便紧急关闭

---

## 八、与项目纪律的一致性自查

- ✅ **不污染生产数据**:mock 供应商不写 `providers` 表;mock 探针写独立 `mock_probe_history`
- ✅ **统一部署**:作为网关进程的一部分,与 self_check_worker / system_health_worker 并列
- ✅ **优雅停机**:在 signal handler 中按"停 ticker → 停 HTTP"序列关闭
- ✅ **真库回归测试**:每个组件都有 wire-level 集成测试,不仅 mock 单元测试
- ✅ **Prometheus 指标 scope 隔离**:所有指标带 `scope="mock_probe"`,dashboard 默认排除
- ✅ **配置热更新友好**:通过 `settings.Global.EffectiveValue` 接入,可热更新
- ✅ **批处理避开监控窗口**:`mock_probe_history` 写入不与 `request_logs` 的 200ms flush 对齐(独立 flush goroutine)

---

## 九、待评审问题

1. **Mock 供应商的端口默认值 9081/9082 是否合理?** 还是要改为 8081/8082 或其他?
2. **Mock 探针的轮询间隔 30s 是否合适?** 还是 60s 更稳?
3. **`mock-probe-client` 系统凭证是默认注册还是需手动创建?** 本设计倾向默认注册(零配置启动)
4. **degraded 事件的阈值 25% 是否过敏感?** 4 通道中只要 1 个持续失败就可能触发
5. **Web UI 卡片是否需要 admin 鉴权?** 还是默认隐藏 + 内部网络可访问即可?

---

**结论**:本设计在不污染生产的前提下,通过 2×2 mock 通道提供全链路的端到端自检能力,工作量约 8 个工作日,风险可控,回滚简单。

**下一步**:等待评审;通过后进入阶段 1(基础设施)实施。