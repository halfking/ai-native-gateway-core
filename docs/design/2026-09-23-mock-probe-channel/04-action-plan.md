# mock 探测通道——下一步动作清单

> 文档：`docs/design/2026-09-23-mock-probe-channel/04-action-plan.md`
>
> 输入：`03-optimization`（架构方案）；输出：5 个 PR 的具体动作（开关位、注入位、验收点）。

---

## 0. 总览

| PR | 主题 | 估时 | 风险 |
|----|------|------|------|
| PR1 | 骨架 + config + 启动顺序 | 0.5d | 低（无真实流量） |
| PR2 | MockUpstream 9 mode + 业务过滤 | 1.5d | 中（router 过滤是 hot path） |
| PR3 | MockClient ticker + 静默闸 + 热重载 | 1.5d | 中（metrics hot 注册） |
| PR4 | 删 python mock，统一部署验证 | 0.5d | 低（脚本层） |
| PR5 | 测试 + 文档收口 | 0.5d | 低 |

总计 ~4.5d。每 PR 必须独立可回滚（开关位就绪后才合入）。

---

## 1. PR1 — 骨架 + config + 启动顺序

### 1.1 开关位

- 新增 `config.MockProbeConfig` 结构（见 03 §4）
- 默认 `enabled=false`，**零行为、零进程、零端口**
- `.env.example` 增加 `MOCK_PROBE_ENABLED=false` 一行
- helm/部署模板：默认值不变，注释说明何时开启

### 1.2 注入位

- `config/config.go`：新增 `MockProbeConfig` 字段
- `cmd/gateway/main.go`：
  - 解析 `cfg.MockProbe.Enabled`
  - false → 跳过下面；true → `embedded.NewManager(cfg)` 并注册到 lifecycle 的 `OnStart` / `OnStop`
- `internal/embedded/manager.go`：最小骨架
  - `Manager.Start(ctx)` / `Manager.Shutdown(ctx)` 接口
  - `enabled=false` 时 Start 直接返回（empty manager）
- `internal/embedded/manager_test.go`：
  - `TestEmptyManager_Start_NoOp`
  - `TestShutdownOrder_Invariants`（明确阶段）

### 1.3 验收点

- [ ] `MOCK_PROBE_ENABLED=false`（默认）启动后，`curl /healthz` ready=true
- [ ] `MOCK_PROBE_ENABLED=true` 启动后，healthz 仍 ready=true
- [ ] 进程内 `ss -tlnp | grep gateway` 不会因 enabled=false 多监听任何端口
- [ ] `go test ./internal/embedded/...` PASS
- [ ] `go test ./cmd/gateway/...`（既有 main 启动单测）PASS
- [ ] 无新增 metric / 新增 admin route

---

## 2. PR2 — MockUpstream 9 mode + 业务过滤

### 2.1 开关位

- `MockProbeConfig.Providers []MockProviderSpec`（默认 2 条 healthy+flaky）
- `MockProbeConfig.Models []string`（默认 2 个 model 名字）
- `MockProbeConfig.TenantTag`（默认 `__self_probe__`）

### 2.2 注入位

- `internal/embedded/mockupstream.go`：
  - 移植 `tests/stress/mocks/main.go` 的 9 个 mode（healthy/slow/flaky/server_error/timeout/empty_content/stream_chunk_drop/rate_limit/bad_request）
  - **协议**：`OpenAI Chat Completions`（POST `/v1/chat/completions`，stream 与非 stream 均支持）
  - **监听**：`127.0.0.1:port`，port 由 manager 启动时 `net.Listen("tcp", "127.0.0.1:0")` 动态分配
- `internal/registry/registry.go`：
  - `RegisterProvider` 新增 `IsSelfProbe bool` 字段
  - 存储：`(is_self_probe, ...)` 列
- `internal/router/picker.go`（或对应文件）：
  - `Pick(ctx, model)` 候选过滤：`if p.IsSelfProbe { continue }`
  - **热路径加 inline check**：避免在 hot loop 里 map lookup
- `internal/admin/handlers.go`（providers/models 列表）：
  - `GET /admin/providers` 加过滤：`is_self_probe=false`
  - `GET /admin/models` 同
- `internal/embedded/admin_state.go`：
  - `GET /admin/self_probe/state` 注册到 admin mux，独立 prefix `/admin/self_probe`
  - handler 鉴权复用 admin middleware
- `cmd/gateway/main.go`：
  - 在 `OnStart` 阶段调用 `embedded.Manager.StartMPs(ctx)`
  - 注册 MP 到 registry 时携带 `IsSelfProbe=true, TenantTag=__self_probe__`

### 2.3 验收点

- [ ] enabled=true 时，`curl 127.0.0.1:<mp_port>/v1/chat/completions -d '...'` 返回 200+合法 body
- [ ] enabled=false 时，127.0.0.1 上没有这个端口（`lsof` 验证）
- [ ] 业务 `POST /v1/chat/completions` 的 provider 候选中**不出现** self_probe provider（pprof 抓 picker 路径确认）
- [ ] `GET /admin/providers` JSON 中**不出现** self_probe provider
- [ ] `GET /admin/models` JSON 中**不出现** self_probe model
- [ ] `GET /admin/self_probe/state`（带 admin token）能返回 A/B 当前 mode
- [ ] 9 个 mode 各自单测 PASS（mockupstream_test.go）
- [ ] router 过滤的 benchmark 没有性能回退（既有 `BenchmarkPick*` 不变）
- [ ] `/admin/self_probe/state` 不带 admin token 返回 401

---

## 3. PR3 — MockClient ticker + 静默闸 + 热重载

### 3.1 开关位

- `MockProbeConfig.IntervalSeconds`（默认 30）
- `MockProbeConfig.RequestTimeoutMs`（默认 5000）
- `MockProbeConfig.AdminToken`（默认空 = 禁 runtime trigger）
- runtime reload：`POST /admin/self_probe/disable` 可关闭

### 3.2 注入位

- `internal/embedded/mockclient.go`：
  - 节拍 ticker：`time.NewTicker(IntervalSeconds)`
  - 复用 `internal/routingtest.Client` 的 `Chat` / `Stream`
  - 出站 URL = MP A / B 的环回 URL（manager 启动时记录）
  - 2x2 交叉：每轮 N 次请求（A→模型1、A→模型2、B→模型1、B→模型2），覆盖 router pick 路径
  - 静默闸：`enabled=false` 时 ticker stop + 不发任何请求
- `internal/metrics/`：
  - 新增 `NewSelfProbeRegistry()`：独立 `prometheus.Registry`，避免污染业务 `gateway_*`
  - 注册到既有 promhttp mux，但**用独立 handler** 或 `/metrics` 内 namespace filter
- `internal/embedded/metric_scope.go`：
  - `self_probe_request_duration_seconds`（histogram, label: provider, model, status）
  - `self_probe_request_total`（counter, label: provider, model, status）
  - `self_probe_round_total`（counter, label: result=ok/partial/fail）
- `cmd/gateway/main.go`：
  - OnStart 调用 `embedded.Manager.StartClient(ctx)`
  - lifecycle.OnReload 处理 cfg.MockProbe.Enabled 翻转
- `internal/admin/handlers.go`：
  - `POST /admin/self_probe/trigger`：手动触发一轮
  - `POST /admin/self_probe/disable`：runtime disable（写 manager.SetEnabled(false)）

### 3.3 验收点

- [ ] enabled=true 时，MC 每 IntervalSeconds 发起 4 次请求（A/B × model1/model2），日志可见 `subsystem=self_probe`
- [ ] enabled=true 时，`/metrics` 含 `self_probe_*` series；业务 `gateway_*` 系列**没有**新增 self_probe label
- [ ] enabled=false 时，MC ticker 不再发任何请求（`pprof goroutine` + 日志验证）
- [ ] `POST /admin/self_probe/disable` 后 ticker 立即停（≤1s）
- [ ] runtime reload：`MOCK_PROBE_ENABLED=true→false`（SIGHUP 等价）后，业务路由表过滤生效，新请求不命中 mock provider
- [ ] runtime reload：`false→true` 后，MC 在 1 个 IntervalSeconds 内恢复
- [ ] 单测：`TestMC_EnabledFalse_NoTraffic`、`TestMC_EnabledTrue_TwoByTwo`、`TestMC_ShutdownStopsTicker`、`TestMC_ReloadDisables`
- [ ] 集成测试：开 10 分钟，metrics 显示 `self_probe_round_total` 增加 20（30s/轮 × 10min ≈ 20 轮）
- [ ] 关闭顺序单测：`TestShutdownOrder_StopsClientBeforeMetrics`

---

## 4. PR4 — 删 python mock，统一部署验证

### 4.1 开关位

- 部署脚本：`scripts/mocks/llm-mock-upstream/`、`scripts/mocks/nginx.conf` **删除**
- 部署脚本：`scripts/mocks/start-mock-upstream.sh` 改为薄壳（仅保留引用 `internal/embedded`，供老运维习惯）
- `docker-compose.yml` / `deploy/*.yaml`：移除 `mocks/` mount、移除 `python:` container
- README：删除 "Start Python mock upstream" 一节，替换为 "Mock probe is built-in (MOCK_PROBE_ENABLED)"

### 4.2 注入位

- `scripts/mocks/`：目录删除或收缩
- `deploy/helm/*` / `deploy/docker-compose*`：grep 出所有 `llm-mock-upstream` / `mocks/` 引用并移除
- `docs/runbooks/`：把 "mock 探测" 章节改写为指向 `MOCK_PROBE_ENABLED`

### 4.3 验收点

- [ ] `grep -r "llm-mock-upstream" .`（除 `02-code-audit.md` 外）= 0
- [ ] `grep -r "scripts/mocks" .`（同上）= 0
- [ ] `scripts/deploy-local.sh` 仍可成功启动网关；`/healthz` ready
- [ ] 本机验证：`MOCK_PROBE_ENABLED=true ./scripts/deploy-local.sh`，开 5 分钟，业务接口仍正常（用既有 e2e）
- [ ] 镜像体积对比：旧镜像 vs 新镜像（删除 python + nginx 配置应有可见下降）
- [ ] `requirements.txt` / `Pipfile` 在仓库不存在（如果存在要删）

---

## 5. PR5 — 测试 + 文档收口

### 5.1 开关位

- 全部既有关开关位（合并后）
- `.env.example` 文档化所有 `MOCK_PROBE_*`
- `CHANGELOG.md` 一条 `[unreleased] feat(embedded): mock probe 2x2 channel`

### 5.2 注入位

- 单测覆盖矩阵（既已分散到 PR1~3 的 `_test.go` 里，这里只做整体 review）
- 集成测试：`tests/integration/self_probe/` 一组，矩阵：
  - enabled × {true, false} × reload × {true, false}
- 文档：
  - `docs/design/2026-09-23-mock-probe-channel/` 目录四件套收尾（01..04 + README 索引）
  - `docs/runbooks/self-probe.md`：开关、观察、reload 步骤
  - `README.md`：增加一段 "Mock probe (built-in)"
  - `CHANGELOG.md`：单条
- 关闭 issues：若 design doc 提到 R 几审计的"self_probe 观察"，迁移到 audit 侧 TODO

### 5.3 验收点

- [ ] `go test ./...` PASS
- [ ] `go vet ./...` 无警告
- [ ] `golangci-lint run`（既已 CI 强制）PASS
- [ ] 集成测试 `tests/integration/self_probe/` PASS
- [ ] `MOCK_PROBE_ENABLED=true` 灰度 7 天，观察业务指标无回退
- [ ] 文档齐：01-requirements、02-code-audit、03-optimization、04-action-plan、README 索引
- [ ] PR 合并到 main，部署到 staging 一次（部署轮不属于本审计轮范围，由 ops 拍板）

---

## 6. 风险与回滚

| 风险 | 缓解 | 回滚 |
|------|------|------|
| router hot path 加 is_self_probe 过滤引发性能回退 | benchmark 必须保留既有用例，CI 卡死阈值 | 单独 revert PR2 的 router 改动，PR1 仍可独立保留 |
| metrics 注册到 promhttp mux 引发业务 `/metrics` 体积爆炸 | 独立 registry + namespace 隔离 | PR3 revert；既有 metrics 不变 |
| 删除 python mock 后部署脚本不兼容 | 部署脚本修改先行 PR（独立），删除代码在后 | 部署脚本 revert；mock 代码保留 |
| MC ticker 失控引发请求风暴 | IntervalSeconds=30s + MaxConcurrent=2 + 5s timeout | runtime disable + reload 关闭 |
| self_probe 出现在业务路由表 | 路由过滤 inline check + 集成测试 grep | router 过滤单点修复即可 |

---

## 7. 跨阶段依赖（合并顺序约束）

- PR1 必须先合（开关位就绪）
- PR2 与 PR3 可并行（PR2 只动 MP，PR3 只动 MC；但 PR3 依赖 PR2 的 MP 注册，因为 MC 要调用 MP）
- PR4 必须在 PR2 合入后跑（删除 python 后仍能起来）
- PR5 收口在最后

如果 PR3 比 PR2 晚合入，PR3 内 MC 的单元测试用 `httptest.NewServer` 自起临时 server，不依赖 PR2 的 MP。