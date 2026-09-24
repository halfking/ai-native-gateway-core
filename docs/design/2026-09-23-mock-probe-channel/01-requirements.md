# 内置 Mock 2×2 探测通道（Self-Probe Channel）— 需求完善

> 文档：`docs/design/2026-09-23-mock-probe-channel/01-requirements.md`
> 状态：草案（用户原话 + 关键补完）
> 适用范围：`llm-gateway-go` 主二进制（`cmd/gateway`）同一部署单元
> 关联既有资产：`tests/local/mocks`、`tests/stress/mocks`、`scripts/mocks/*`（Python，独立进程）、`cmd/routing-test-client`、`cmd/scenario_driver`、`internal/probemode`（已存在的另一种"探测栈"开关，注意区分）

---

## 0. 用户原话（事实复述）

> 系统中在部署时，会给出 **2 个 mock 的供应商** 同步启动，并且能够响应一些测试任务，这些主要的目标是用于测试整个服务是否正常，**定时通过 mock 的客户端** 来进行探测，检查流程是否完整。这些需要在系统中增加一个 **参数**，是否启动 mock 操作进行探测。当这个被关闭时，这些 mock 的客户端将不会再发出任何请求，保持静默。并且 mock 的供应商也不会显示出来。
> 我们相当于增加了一个 mock 的 2×2 的测试通道，用于交叉测试网关的相关特性，确认系统运行是否正常。
> 这些探测在开关打开时是可见的，开关关闭时不可见。
> 请完善上述需求，然后对我们的代码进行对比，形成优化方案，然后继续下一步。我们需要将 mock 的供应商与客户端使用 go 实现，达成统一部署的需求。

---

## 1. 术语（先把名词钉死）

| 术语 | 定义 | 备注 |
|------|------|------|
| **Self-Probe Channel**（自探测通道） | 网关二进制进程内嵌的、用于自检端到端链路的最小流量源 | 名词固定，后续章节沿用 |
| **Mock Provider**（MP） | 进程内嵌的 HTTP 兼容 OpenAI Chat Completions 的供应商实现，**不是出网** | 命名沿用 `MockProvider`，但重命名为 `embedded.MockUpstream` 以避免与 `tests/*` 中 *外部进程* 的 `MockProvider` 混淆 |
| **Mock Client**（MC） | 进程内嵌的、按节拍向 *自己* 网关 `/v1/chat/completions` 发请求的客户端 | 名词对应 "mock 的客户端" |
| **2×2 通道** | **2 个 MP × 2 个 MC** 共 4 条交叉路径（MP-A↔MC-1、MP-A↔MC-2、MP-B↔MC-1、MP-B↔MC-2），用于交叉覆盖 | 名词沿用用户原话 |
| **Probe Switch** | 控制 Self-Probe Channel 是否启停的总开关；下文统称 `MOCK_PROBE_ENABLED` | 见 §3 |
| **可见性 (visibility)** | 在 `/admin`、`/metrics`、web UI、provider 列表、模型列表中 **是否能看到** Self-Probe Channel 的对象与请求 | 见 §5 |

---

## 2. 目标与非目标

### 2.1 目标（必须达成）

1. **同一二进制、同一部署单元**：Mock Provider/Mock Client 全部以 Go 实现并入网关主进程，**不依赖外部进程**、**不引入 Python**、**不增加 Dockerfile/CI 步骤**。已部署网关通过配置即可启用。
2. **部署时同步启动**：开关打开时，网关启动完成（passes `/healthz` ready）前，2 个 MP 必须监听就绪、2 个 MC 的探测循环已派发。
3. **可一键关闭**：关闭后，
   - 2 个 MC **立即停止**发出请求（"保持静默"）；
   - 2 个 MP 进程内 HTTP server 关闭监听；
   - 2 个 MP **不出现在 provider 列表、模型列表、admin 端点、metrics 自描述中**（"不会显示出来"）。
4. **节拍可配置**：探测周期、并发、payload 大小、单次超时均为可调参数，默认轻量。
5. **可见性**：开关打开时，自探测的请求/响应/统计在 `/admin` 与 `/metrics` 中 **可见**，但与真实业务流量 **可区分**（标签 + 命名空间），不污染业务指标。
6. **跨特性覆盖**：4 条路径天然覆盖：①路由、②凭据/限流、③streaming 与 non-streaming、④失败注入（mode 切换）→ 用于验证网关各特性的健康面。
7. **不影响生产语义**：当开关关闭时，对外业务行为、SLA、指标、日志与"没有该通道"完全一致。

### 2.2 非目标（明确不做）

1. **不** 替代 `internal/probemode` 的 `USE_NEW_PROBE_MODE`（那是切新旧 probe 栈，与"启用 mock 探测"无关）。
2. **不** 替代 `cmd/routing-test-client` / `cmd/scenario_driver`（它们是 *外部驱动测试工具*，与 *内置自探测* 角色不同，但能力可复用）。
3. **不** 替代任何已存在的 *真实* 凭据健康检查（`credential_selfcheck`、`node_probe` 等）。
4. **不** 为生产租户暴露 Self-Probe Channel（默认租户看不到，admin 可见）。
5. **不** 引入新的外部依赖（vendor 范围不变）。

---

## 3. 功能需求（FR）

### FR-1 开关

- **配置项**：
  - YAML：`mock_probe.enabled: bool`（默认 `false`）
  - 环境变量：`LLM_GATEWAY_MOCK_PROBE_ENABLED`，同名 env tag（env > YAML）
- **取值**：与 `envBoolOff` 一致；显式 `false/0/off/no` 才算关闭，缺省关闭（**生产安全默认值**）。
- **可热重载**：`POST /admin/config/reload` 后应能即时生效（与现有 YAML hot-reload 通道一致）；env 设置需重启进程。
- **子开关**（均为 YAML/env，附在 `mock_probe:` 下）：
  - `providers_count`：`int`，默认 `2`，范围 `[1, 4]`。≥ 1 时按顺序启用 `mock-upstream-A`、`mock-upstream-B`、`mock-upstream-C`、`mock-upstream-D`。
  - `clients_count`：`int`，默认 `2`，范围 `[1, 4]`。
  - `interval_seconds`：`duration`，默认 `30s`，范围 `[5s, 600s]`。
  - `request_timeout`：`duration`，默认 `15s`。
  - `payload_size`：`enum{small,medium,large}`，默认 `small`（≈ 64/256/1024 token prompt）。
  - `stream_probability`：`float`，默认 `0.5`，范围 `[0.0, 1.0]`。
  - `failure_injection.mode`：`enum{healthy,slow,server_error,broken_stream,flaky}`，默认 `healthy`，可热切。
  - `visibility.show_in_admin`：`bool`，默认 `true`（受总开关 gate，开关关闭时整体不显示）。
  - `visibility.show_in_metrics`：`bool`，默认 `true`。
  - `visibility.show_in_provider_list`：`bool`，默认 `true`（见 FR-5）。

### FR-2 Mock Provider（MP）

- **数量**：1~4 个（默认 2），命名固定 `mock-upstream-A` … `mock-upstream-D`。
- **协议**：OpenAI `/v1/chat/completions`（兼容 non-stream 与 SSE stream）。
- **运行模式**（与 `tests/stress/mocks/main.go` 已实现的 mode 集对齐）：
  - `healthy`：50–200 ms 抖动，返回合规 JSON / SSE；
  - `slow`：3–10 s 延时后正常返回（用于测超时/退避）；
  - `server_error`：500 + `{"error":{"type":"server_error"}}`；
  - `broken_stream`：仅 stream 模式发 1 chunk 即 EOF；
  - `flaky`：50% 命中 `server_error`。
- **监听**：HTTP，端口在网关启动时 **按内部回环地址（`127.0.0.1`）** 监听，避免对宿主机暴露。端口可用 `127.0.0.1:<auto>` 自动分配，并注册到 `/admin/self_probe/providers`。
- **注入到路由系统**：MP 必须以 *真实 provider* 形态注册到 `domains/routingstate`、`provider` 注册表与模型清单，**使得 MC 调用自身网关时像调用真供应商一样走完整流程**（路由 → 凭据选择 → 限流 → 上游调用 → 记账 → 指标）。
- **凭据**：每个 MP 在启动时 **自签一个 mock API key**（`mock-prov-<n>`），注册到凭据库（标记 `kind='mock'`，与真凭据隔离）。
- **失败注入切换**：通过 `POST /admin/self_probe/providers/{name}/state {"mode":"..."}` 立即生效（与 `tests/stress/mocks` 的 `/admin/state` 一致），但仅在总开关开时返回 200，否则返回 404。

### FR-3 Mock Client（MC）

- **数量**：1~4 个（默认 2），命名固定 `mock-client-1` … `mock-client-4`。
- **目标**：每个 MC 启动一个 goroutine，循环 `interval_seconds` 向 *本地网关* `/v1/chat/completions` 发起请求，模型名 **轮询 MP-A / MP-B / …** 之一，确保 4 条路径都被打到。
- **关键行为**：
  - **静默**（开关关）：所有循环 goroutine 不再 ticker、不再发请求；已 in-flight 的请求 **允许完成**（最多 1 个 cycle），之后停止。
  - **可见性**：MC 的请求与所有真实请求走同一 HTTP / gRPC 中间件栈，但在 metrics / 日志 / request_logs 中打 `source="self_probe"` 标签。
- **限流豁免**：MC 的请求 **不计入租户配额**（`tenant_id` 固定为内部租户 `__self_probe__`），避免污染租户额度。

### FR-4 启动顺序与生命周期

1. `cmd/gateway` 启动时读取配置；
2. 若 `MOCK_PROBE_ENABLED=true`：
   1. 启动 N 个 MP HTTP server；
   2. 注册 MP 为 provider / 凭据 / 模型；
   3. 启动 M 个 MC goroutine；
   4. `/healthz` 在 MP 健康检查返回 200 后报 `ready=true`；
3. 关闭时（开关被关 / 进程退出）：
   1. 通知 MC 停止 ticker → drain in-flight；
   2. 注销 MP 的 provider / 凭据 / 模型条目；
   3. 关闭 MP HTTP server；
   4. `healthz` 不应阻塞关闭。

### FR-5 可见性矩阵

| 面 | 开关开 | 开关关 |
|----|--------|--------|
| `GET /admin/providers` | 可见（带 `is_self_probe=true`） | 不可见（被过滤） |
| `GET /admin/models` | 可见（带 `source=self_probe`） | 不可见 |
| `GET /admin/self_probe/*` | 可用 | 404 |
| `/metrics` 中 self_probe 指标 | 可用（独立 namespace） | 不暴露（promhttp 不注册 handler） |
| Web UI → provider 抽屉 | 可见（带自我标签） | 不可见 |
| 日志 / request_logs | 可见（`source=self_probe`） | 不出现（MC 静默、MP 已被注销） |

---

## 4. 非功能需求（NFR）

1. **资源占用**（默认配置下）：
   - 内存 ≤ +30 MiB；
   - 持续 CPU ≤ +1%（空闲时 ≤ +0.1%）；
   - 每秒写 DB 量 ≤ 5 行（与默认探测节拍匹配）。
2. **故障隔离**：
   - MP 崩溃不应影响网关主服务（独立 listener + recover middleware）；
   - MC 抖动不应拉低真实业务 SLO（独立 timeout，独立指标桶）。
3. **可观测性**：
   - `/metrics` 暴露 `self_probe_requests_total{mock_provider,result}`、`self_probe_inflight_gauge`、`self_probe_up{provider}`；
   - `/admin/self_probe/status` 返回 4 条路径最近一次成功 / 失败时间、错误类型。
4. **安全**：
   - MP **仅监听 `127.0.0.1`**；
   - MP 凭据 **不会** 进入 `domains/credential` 真凭据表（独立表或独立 `kind`）；
   - `__self_probe__` 内部租户 **不可被外部 API key 调用**（API key auth 中间件必须显式 deny）。

---

## 5. 边界与假设

- 开关默认 **关闭**（生产安全）；
- 不为历史已存在的 `scripts/mocks/*` 外部 mock 增加新功能；本次仅在主二进制内做；
- 与 `internal/probemode.USE_NEW_PROBE_MODE` 是 **正交** 关系：probe 栈新旧与 self-probe 是否启用互不影响。

---

## 6. 验收口径（DoD）

- [ ] `MOCK_PROBE_ENABLED=true` 时，启动日志显示 2 个 MP 端口、2 个 MC 启动成功；`/healthz` 在 MP ready 后变 ready；
- [ ] `MOCK_PROBE_ENABLED=false` 时，30 秒内 `/admin/providers` 不出现 `mock-upstream-*`，`/admin/self_probe/*` 返回 404，MC 不再产生新请求；
- [ ] `MOCK_PROBE_ENABLED=true` 时连续运行 5 分钟，`self_probe_requests_total` 计数器单调递增、4 条路径都有 ≥ 1 次成功；
- [ ] `/metrics` 中 `self_probe_*` 指标与业务指标 **命名空间无重叠**；
- [ ] 关闭开关后再次打开，开关重启 MC，4 条路径恢复；
- [ ] 单测覆盖：MP 模式切换、MC 静默、可见性过滤、metrics 命名空间隔离；