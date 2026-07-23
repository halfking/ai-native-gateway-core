# LLM Gateway Go — 系统架构（当前态）

> 适用版本：**v2.4.8+**（48h 审计：2026-07-22 ~ 2026-07-24 期间合并）
>
> 上一版 V3 提案（Python 控制面 + Go 数据面）是早期路线图，现行代码已演进为
> 单 Go 数据面 + Admin Web (Vue) + 离线 Installer 三件套；本文档按现行实现重写。

---

## 0. 执行摘要

网关是一个 Go 1.25 单进程数据面：
- **`cmd/gateway`**：进程入口，负责装配 200+ 业务包、启动 HTTP/SSE、信号处理。
- **数据面**：`domains/streaming/*` + `domains/streaming/executors/*`，处理 OpenAI / Anthropic / Responses 协议的请求转流，含路由、重试、限流、审计、凭据健康。
- **后台 workers**：`bg/*` 约 50 个常驻 goroutine；探测、打标、数据治理、计费聚合、TTL 清理等。
- **Admin API**：`admin/` 提供 ~163 个 handler，覆盖仪表盘、路由配置、审计、SSE 实时流。
- **管理面板**：`web/` Vue 3 + TS，双主题 + 实时请求流多维过滤器 + Resolve 页行级管理。
- **离线安装 / 升级**：`installer/`（独立 go.mod）提供 CLI 工具与版本管理 API。

提交 252 的主控端 `llm.kxpms.cn` 依然负责 license / 实例心跳 / 更新分发
（在 README「双仓库策略 / 部署」一节），但本仓库不再有 Python 控制面组件。

---

## 1. 请求生命周期（数据面）

```
                            Client
                              │
                              ▼
┌──────────── /v1/chat /v1/messages /v1/responses ────────────┐
│  HTTP handler（adapter/unified + domains/streaming）        │
└──────────┬─────────────────────────────────────┬────────────┘
           │                                     │
           │ pre-hook 链（domains/hooks/*）       │
           ▼                                     │
  Tenant / Key / Profile 解析                    │
           │                                     │
           ▼                                     │
   ┌── 路由评分（composite） ──┐                 │
   │  P2C 选 best-of-2 候选   │                 │
   └────────────────────────┘                  │
           │                                     │
           ▼                                     │
   ┌── 凭据 / 连接池 / 健康 ──┐                 │
   │  adaptive probe + RPS    │                 │
   └────────────────────────┘                  │
           │                                     │
           ▼                                     │
   流式中继（SSE transform + 错误分类）         │
           │                                     │
           ▼                                     │
   Goal 重试（cost-mode preset + EffectiveMaxRetries）
           │                                     │
           ▼                                     │
  post-hook 链 → 审计 DLQ → OTel → Prometheus  │
           │                                     │
           └─────────────── 实时流 ──────────────▼──► /admin/live-stream SSE
```

---

## 2. 路由：延迟感知 + P2C 评分

### 2.1 Composite 方向不变性

`domains/streaming/executors/router_scoring.go:calculateLoadScore` 是复合评分：

```
composite =
    concurrencyScore * w_concurrency   // pressure, 0~1，越大越差
  + identityScore    * w_identity      // pressure, 0~1，越大越差
  + latencyPenalty   * w_latency       // 1 - latencyScore（health）
  + qualityScore     * w_quality       // 1 - successRate
  + headroomPenalty  * w_headroom      // 1 - headroom（health）
```

P2C 在 `router.go` 取 **min**，故每一项都是 **penalty**（越大越差）。

> **审计修正（2026-07-24）**：提交 60809965 把 `latencyScore` 重写为「健康度
>」（< 800ms → 1.0），但仍以正向相加；与 P2C min 方向相反，等于「快/有
> headroom 的凭据反而被 P2C 惩罚」。修复 = 用 `1-x` 转 penalty。

### 2.2 延迟评分（health-table）

| amplified p95 (ms) | latency_score |
|---|---|
| < 800 | 1.00（feels instant） |
| [800, 1500) | 1.00 → 0.85 |
| [1500, 3000) | 0.85 → 0.65 |
| [3000, 10000) | 0.65 → 0.30 |
| [10000, 30000) | 0.30 → 0.05 |
| ≥ 30000 | 0（hard block） |

并发压力放大：`observed = p95 × (1 + α · max(0, pressure − knee)^β)`
（α=1.2 / β=1.8 / knee=0.6，可由 `LLM_GATEWAY_PRESSURE_*` 覆盖。）

详见 `docs/design/2026-07-20-latency-aware-routing.md` §2.1.

### 2.3 Goal 重试不变量

`GoalRetryPolicy.EffectiveMaxRetries()` 在一处收敛：
`Enabled=false ⇒ 0`（仅一次执行）；handler 统一通过 helper 取值，避免
下游直接读 `MaxRetries` 绕过关闭开关。

---

## 3. 系统监测（bg/systemmonitor）

### 3.1 Redis FIFO 队列

```
llmgw:monitor:queue          LIST   FIFO
llmgw:monitor:inflight:{cred}:{model}  STRING  (30s dedup)
llmgw:monitor:running        SET    inflight + claimed task 的 worker_id 视图
llmgw:monitor:tasks:{id}     HASH   任务完整定义
llmgw:monitor:tasks:counter  STRING INCR 自增任务 id
llmgw:monitor:workers        SET    在线 worker
llmgw:monitor:events         PUB    SSE 推送事件
```

### 3.2 原子抢占（lua/claim.lua）

- **入 KEYS**：`queue`；**ARGV**：`worker_id`, `inflight_ttl_seconds`。
- **inflight key 在脚本内从任务 JSON 的 `credential_id`/`raw_model` 构造**，
  解码前无需（也无法）由调用方提供。
- 任务字段缺失 → LPOP 丢弃，避免队头卡死。
- EXPIRE 兜底：30s 后自动过期，允许多实例 worker 不会因单实例崩溃而饿死。

> **审计修正（2026-07-24）**：早期版本调用方传 `(0, "")`，被守卫拒绝 → 队列
> 永不消费 → fallback 抖动。修复后脚本内构造 inflight key，不再折叠为全局单一 token。

### 3.3 降级路径

`fallbackCh`（in-memory）接管 Redis 不可用时期；通过 `markFallback` / `clearFallback`
健康探针每 15s 切换。`scripts` 用 `atomic.Pointer[LoadedScripts]` 规避
Submit 重载路径与 worker 读路径之间的数据竞争。

---

## 4. 凭据健康（7 类错误分级）

详见 `docs/architecture/ARCHITECTURE.md` 旧版 §1.1；现行实现集中在
`domains/credential/` (Redis+memory dual-layer) 与 `domains/health/`：

| 类别 | 示例 | 恢复类型 | 重试 | 熔断 | 客户端响应 |
|---|---|---|---|---|---|
| TRANSIENT | 5xx 无明确错误 | 临时 | ✓ 退避 | cooling 60s | 503 |
| TIMEOUT | 连接/读超时 | 临时 | ✓ | cooling 60s | 504 |
| NETWORK | DNS/连接拒绝 | 临时 | ✓ | cooling 60s | 502 |
| RATE_LIMIT | 429 | 周期性 | ✓ | cooling 30s + shrink(0.7) | 429 |
| AUTH | 401/403 | 永久 | ✗ | quarantine | 502 |
| QUOTA | 402/余额不足 | 永久 | ✗ | quarantine | 402 |
| UPSTREAM_DOWN | 502/503/504 | 周期性 | ✓ | open 指数退避 | 503 |

### 4.1 Node Probe backoff ladder（2026-07-24 调整）

- 最大 backoff 24h → **6h**（commit `7bf35f19`），契合 SLA 与恢复期望。
- 恢复时不再叠加 backoff 检查，加快恢复路径。
- 配置入口：`bg/probe_backoff.go` + `bg/systemmonitor/monitor.go` 中 `computeBackoff`。

---

## 5. 分发与升级

### 5.1 Maintain /distribution API

- `GET /maintain-api/distribution/version-check?channel=&current=&platform=&arch=`
  返回 `{latest_version, mandatory, target_artifacts:[{platform,arch,filename,sha256,storage_uri}]}`
- 客户端必须按 `platform`/`arch` 选择 artifact（commit `f345c130` + 审计修正
  `client.go:CheckUpdateDistribution`）。

### 5.2 离线升级包

```
scripts/build-upgrade-package.sh        一次性打包（upgrade-pkg-{tag}.tar.gz）
installer/internal/upgrader             apply/rollback API
installer/cmd/llm-launcher              蓝绿 / 重启恢复（e2e 测试已覆盖）
```

### 5.3 install / launch 一体化

`installer` 是独立的 Go 1.22 模块，独立 vet/test 链：
```
cd installer
go vet ./... && go test -short ./...
```

---

## 6. Admin API 与 Vue 管理面板

### 6.1 Admin handlers

~163 个 handler 分布在 `admin/`：
- 仪表盘（路由 / 请求 / 数据生命周期 / 成本）
- 路由 Resolve 配置（含候选行级抽屉 + 行级管理）
- 实时请求流（多维过滤器、6 语言 i18n）
- 系统监测（推流 SSE + Vue 仪表盘切流进度）

### 6.2 /internal/release 路由的安全护栏

`internal/release/handler.go` 中管理 API（`/admin/releases/*`）必须在
`NewHandler(service, gin.HandlerFunc)` 中显式传入 admin 中间件；传 nil
→ fail-close，不挂载任何管理端点，避免「写下注释忘开认证」。
公开 API 仅做只读元数据查询 + 下载计数（`RecordDownload`）。

---

## 7. 部署与双仓库

| Remote | URL | 用途 |
|---|---|---|
| `codeup` (origin) | `https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git` | 日常开发 |
| `github` | `git@github.com:halfking/SI-LLM-Gateway.git` | 公开镜像，自动扫描 |

部署模式 M1-M4 详见 `docs/DEPLOYMENT.md`；48h 内新增的 M4 蓝绿由 `installer/cmd/llm-launcher` 守护。

---

## 8. 测试与质量保障

| 维度 | 落点 |
|---|---|
| 路由单元 / 压力测试 | `domains/routing/{routing_benchmark,stress}_test.go` |
| 流式端到端 | `domains/streaming/goal_retry_{integration,stress}_test.go` |
| Goal 重试回归 | `domains/streaming/goal_retry_policy_test.go` (含 `TestEffectiveMaxRetriesHonorsDisabledFlag`) |
| SystemMonitor | `bg/systemmonitor/{metrics_collector,types}_test.go` |
| 凭据健康 | `domains/credential/{redis_health_store,writer}_test.go` |
| Installer / Launcher | `installer/internal/{launcher,upgrader}/**/*_test.go` |

gofmt 必须在 48h 内任何变更中保持一致（CI：`gofmt -l` 必须空）。

---

## 9. 48h 审计与变更（2026-07-22 ~ 2026-07-24）

详见 [`docs/2026-07-24-48h-audit-report.md`](../2026-07-24-48h-audit-report.md)：
6 处真实 bug 修复 + 1 处功能一致化 + 1 处 fail-close 安全护栏 + 5 处 gofmt 对齐。
