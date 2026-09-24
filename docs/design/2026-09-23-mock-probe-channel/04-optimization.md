# Mock 探测通道：现状 × 需求对比 & 落地优化方案

> 起草人：ZCode (MiniMax-M3)
> 日期：2026-09-23
> 输入：上一轮《需求细化》(01-requirements.md) + 上一轮《架构与配置》(02-design.md) + 本轮《代码现状调研》(subagent 报告)
> 目的：在开始动手写代码前，把"需求 vs 现状"的每一处差异逐条钉死，把"动手方式"定成可审核的方案，而不是直接落地。

---

## 0. TL;DR（先看这段再决定要不要看下面）

- 网关已经有 9/10 需要的现成零件：provider 双源注册、chi mux、ticker 任务模板、`GatewayClient` 客户端、`IsInternal` 豁免链路、Prom 指标命名约定、slog scope 标签、`NewSSEWriter` 流式抽象。
- **唯一一处现成零件不齐**：admin UI 列 provider 走的是 PG `providers` 表，**不读内存 registry**。所以"mock-fast/mock-slow 在 admin UI 里隐藏 / 显示"的开关不能只在内存里藏，必须**双写双读双管**，否则会出现"路由表看不到 / admin UI 看得见"的撕裂。
- **真正要新写的"原创代码"只有 3 个文件**：
  1. `internal/mockprobe/server.go`：把 `tests/stress/mocks` 里 OpenAI/Anthropic mock 的核心 handler 抠出来，做成一个 `MockSupplier`（一个二进制实例同时扮演 fast 和 slow，靠构造参数切延迟/错误率）。
  2. `internal/mockprobe/probe.go`：照抄 `internal/ticker/jobs/healthcheck.go` 的 2×2 探测 job（fast×slow × 流式×非流式）。
  3. `internal/mockprobe/admin.go`：3 个 admin 接口（开关触发一次、查看最近一次状态、列出历史）。
- **真正要改的"接入点"只有 5 处**：
  1. `config/config.go` 加 `MockProbeConfig`（保持现有 YAML+env+热更新模式）。
  2. `cmd/gateway/main.go` 在 `run()` 里 start 后台 mock probe goroutine + 注入到 graceful shutdown 链路。
  3. `internal/admin/handlers.go:ListProvidersHandler` 加 `mock_probe_hide_in_admin` 过滤。
  4. `internal/admin/handlers.go:CreateProviderHandler` 末尾追加 `registry.Register(...)`，让 PG 写库后内存立刻可见（**这是顺手解决的历史问题**，不是 mock 探测专属债）。
  5. PG schema 加一张 `mock_probe_history` 表（独立 schema，独立 TTL，独立指标 scope，避免污染业务库语义）。
- **优雅停机**严格按现有顺序：mock probe ticker 停 → mock supplier server `Shutdown(ctx)` → 业务 ticker 停 → DB 关。这样不会和 R46/R47 已经定型的 252 部署形态冲突。
- **可见性收敛**：开关关掉时，**进程内不发 goroutine、不绑端口、不写日志、不写 DB、不写指标**。这是这次设计的强约束，比"开了再过滤"安全得多。

下面是逐条对比。

---

## 1. 需求 → 现状 一对一对照

> 约定：✅ 现成直接用 / 🟡 现成但要补一点小零件 / 🔴 完全没有，需要新写

| 需求点 | 现状 | 评级 | 处理方式 |
|---|---|---|---|
| 部署时启动 mock-fast / mock-slow 两个实例 | `cmd/gateway/main.go:run()` 启动 + chi mux 注册路由；`internal/server/http.go:startHTTPServer` 监听 `:8080` | 🟡 | 不开新进程，复用同一个 `chi.Mux`，注册到独立子路由 `/mock/fast` 和 `/mock/slow`（不是真开放给客户端的 `/v1/...`） |
| mock 端点：`/healthz` `/admin/state` `/admin/stats` `/v1/chat/completions`（流/非流） | `tests/stress/mocks/openai.go`、`anthropic.go` 已经有完整实现 | ✅ | 把 `mocks` 包代码提到 `internal/mockprobe/server.go`，抽象成 `MockSupplier.HandleChatCompletions(w, r)`，避免重复实现 |
| 模拟延迟（fast: 20-80ms / slow: 400-800ms）和错误率 | mocks 里已有 `WithLatencyJitter` / `WithErrorRate` 选项 | ✅ | 复用 |
| 客户端定期 2×2 探测 | `internal/ticker/ticker.go` + `internal/ticker/jobs/healthcheck.go` 是现成模板 | ✅ | 照抄 healthcheck job，间隔从 `cfg.MockProbe.Interval` 读 |
| 客户端封装（流/非流） | `internal/client/gateway_client.go:GatewayClient` 已有 `ChatCompletions` 和 `ChatCompletionsBlocking` | ✅ | 直接复用，调 `X-Mock-Probe: 1` 头区分探测流量 |
| 探测请求豁免计费 / 审计 | `api_keys.is_internal=true` + `billing.ShouldCharge` / `audit.ShouldAudit` 短路 | ✅ | 用一个内置 `is_internal=true` 的 system api key（启动时 create-or-skip） |
| 探测结果写表 | 无现成"自检表" | 🔴 | 新增 `mock_probe_history` 表（独立 schema，TTL 24h） |
| 探测失败阈值告警 | 无现成"自检告警"通道 | 🔴 | 复用 `internal/metrics/metrics.go` 暴露 `llm_gateway_mock_probe_failures_total`，告警由 ops 配（不在本次范围） |
| 开关 `mock_probe_enabled`（YAML + env + 热更新） | `config.Config.Features.*` 已有 enabled 字段模式；`/admin/config/reload` 已支持热更新 | ✅ | 加 `Config.MockProbe.Enabled bool`，沿用 `OVERRIDE_MOCK_PROBE__ENABLED` env 覆盖 |
| `mock_probe_hide_in_admin` 控制 UI 可见性 | `internal/admin/handlers.go:ListProvidersHandler` 走 PG `providers` 表 | 🟡 | 改 handler 加 `WHERE name NOT LIKE 'mock-%' OR $hide_admin = false` 过滤；mock supplier 注册时 `name` 统一前缀 `mock-`（这是命名的关键约定） |
| 开关关掉时静默（不绑端口 / 不发请求 / 不写日志指标 DB） | 现有 ticker job 是"起了再 skip"，不是"按开关决定起不起" | 🟡 | 改造 `startHTTPServer` 注入 mock supplier：`if cfg.MockProbe.Enabled { mux.Mount(...) ; go startProbe(...) }`，**整块逻辑按开关决定是否加载**，不是局部 skip |
| 优雅停机 | `cmd/gateway/main.go:run()` 中已有 `server.Shutdown(ctx)` → `ticker.Stop()` → `db.Close()` | 🟡 | mock supplier server 也是 `http.Server`，单独 Shutdown，排在业务 ticker 停之前 |
| 日志 scope 隔离 | `slog.Default().With("scope", "mock_probe")` 是现有惯例 | ✅ | mock probe 模块启动时拿一个 scoped logger，所有日志自带 `scope=mock_probe` |
| 指标命名规范 | `llm_gateway_<area>_<metric>` 前缀 | ✅ | `llm_gateway_mock_probe_total`、`llm_gateway_mock_probe_latency_seconds` |
| 绑定本地回环（127.0.0.1） | 现有 `HTTPAddr` 默认 `:8080` 是全网卡 | ✅ | mock supplier 用独立的 `MockProbeAddr` 配置项，**默认 127.0.0.1**，业务网关端口保持不变 |
| 不引入第三方 mock 库 | `tests/stress/mocks` 用的是 `net/http` + `encoding/json` | ✅ | 照搬，纯标准库 |

---

## 2. 现状中"差一点"的零件补完方案

### 2.1 内存 provider registry 与 PG `providers` 表撕裂（这是历史债，不是 mock 专属债）

**现状**：
- `internal/provider/registry.go:ProviderRegistry` 是进程内内存表，路由匹配用它。
- `internal/admin/handlers.go:ListProvidersHandler` 直接 `SELECT * FROM providers` 给 admin UI。
- admin UI 的 `CreateProviderHandler` 只写 PG，**不调 registry.Register**。结果是：admin 加了 provider，admin UI 立刻看得见，但实际路由**要等下次 ticker reload** 才生效。

**为什么 mock probe 不能凑合**：
- mock supplier 启动时要走"既出现在 admin UI，又出现在路由表"——否则探测请求转发给谁都不知道。
- 而且我们的隐藏开关是"admin UI 看不到"，意味着 mock supplier 在内存里**必须存在**（要响应探测），但在 PG 表里**要么不存在，要么被 filter 掉**。

**补完方案**（**两步走**）：

**Step A：mock supplier 用纯内存注册，不写 PG `providers` 表**
- 启动时若开关开，直接 `registry.Register("mock-fast", spec)`、`registry.Register("mock-slow", spec)`。
- **不写** PG `providers` 表。
- 这样 admin UI 默认就看不到它们（因为 ListProvidersHandler 只读 PG）。

**Step B：把 `mock_probe_hide_in_admin` 实现成"反向强制显示"**
- 当 `mock_probe_hide_in_admin=false`（默认值）时，admin UI 可以看到 mock supplier。
- 实现方式：在 `ListProvidersHandler` 里 `UNION` 一段 `SELECT FROM (内存 registry WHERE name LIKE 'mock-%')`，不是写 PG。
- 当 `hide_in_admin=true`（生产）时，`ListProvidersHandler` 跳过这段 UNION。
- **比"写 PG + filter"更干净**：开关关掉时 PG 不会有遗留脏数据。

> 备注：补 Step A+B 同时也把"admin 加普通 provider 路由不立刻生效"的历史债**留着不动**——不在 mock 探测这次任务里修，避免 scope 蔓延。已在 TODO 标注给下一轮。

### 2.2 现有 ticker 是"启动后按开关 skip"，不是"按开关决定起不起"

**现状**：
`internal/ticker/ticker.go:Start(ctx)` 一进去就把所有 job goroutine 全 spawn。job 内部 `if !cfg.FeatureX { return }`。

**为什么 mock probe 要"按开关决定起不起"**：
- 强约束："开关关掉时进程内**不绑端口 / 不发请求 / 不写日志指标 DB**"。
- 如果沿用现有模式，会出现：开关关 → ticker 还在转 → 每个 tick 一次 `if !enabled { return }` 空转（虽然轻量，但日志/metric 0 输出 ≠ 真没产生 goroutine 调度开销）。
- 更关键的是：**开关关掉时 mock supplier 端口必须立刻释放**，不是"挂着不响应"。

**补完方案**：
- mock supplier 和 mock probe job 的启停**不进现有 `Ticker`**，单独写一个 `internal/mockprobe/probe.go:MOCKProbe` struct，自己管 `Start(ctx)` / `Stop()`，构造时若 `cfg.MockProbe.Enabled == false` 直接返回 nil（**完全不起**，调用方 `if probe == nil { /* 跳过整段 */ }`）。
- 这样在 `cmd/gateway/main.go:run()` 里就是：
  ```go
  var probe *mockprobe.Probe
  if cfg.MockProbe.Enabled {
      probe = mockprobe.Start(ctx, cfg, registry)
      defer probe.Stop()
  }
  ```
- 干净、显式、关掉时一个 goroutine 都不起。

### 2.3 `mock_probe_history` 表要单独 schema，不进业务库

**为什么不进业务库**：
- 业务 schema（如 `public`）下任何表都会进入 252 备份、billing 报表、admin UI 的"表浏览器"。mock 探测数据不该污染这些。
- 独立 schema `mock_probe`（或直接放 `public` 但加 `WHERE scope='mock_probe'` 前缀强过滤——但前者更稳）。

**方案**：
- 新增 PG schema `mock_probe`（migration 文件 `730_create_mock_probe_schema.up.sql` / `.down.sql`）。
- 表 `mock_probe.history`（注意表名前缀 schema 名，因为 schema 是 `mock_probe`，表名可以直接叫 `history`，调用时 `mock_probe.history`）。
- 列：`id bigserial`、`ts timestamptz default now()`、`provider text`、`mode text`（stream/nonstream）、`latency_ms int`、`status int`、`error text`、`payload_size int`。
- 索引：`(ts desc)` 做 TTL 清理；`(provider, ts desc)` 做按 provider 查询。
- TTL 24h：复用现有 `internal/jobs/retention.go`（如果存在）或单独写一个 `cleanup_job`，每天凌晨清一次（cron 形式）。
- 表大小估算：30s 一次 × 4 通道 × 86,400s/天 ≈ 11,520 行/天 × ~120B/行 ≈ 1.4MB/天 × 7 天 ≈ 10MB，可控。

> 不在 `request_logs` 表里加 mock 探测行——`request_logs` 已经够重了（156 列 + 分区），mock 探测语义和业务请求完全不同，混进去会污染审计。

---

## 3. 落地优化方案（设计层）

### 3.1 新增/修改文件清单

| # | 文件 | 动作 | 说明 |
|---|---|---|---|
| 1 | `config/config.go` | 修改 | 新增 `MockProbeConfig` struct（`Enabled`、`HideInAdmin`、`FastAddr`、`SlowAddr`、`Interval`、`Timeout`、`Payload`、`HistoryTTL`），挂在 `Config.MockProbe` 下 |
| 2 | `internal/mockprobe/server.go` | 新增 | `MockSupplier` 实现：复用 `tests/stress/mocks` 的 chat completions handler（流/非流）+ `/healthz` + `/admin/state` + `/admin/stats`。构造参数切 fast/slow（延迟、错误率）。HTTP server 用 `http.Server`，绑 `127.0.0.1:<port>` |
| 3 | `internal/mockprobe/probe.go` | 新增 | `Probe` struct + `Start(ctx, cfg, registry, client) (*Probe, error)` + `Stop()`。内部启 goroutine，每 `cfg.Interval` 跑一轮 2×2 探测；结果写 `mock_probe.history` + `llm_gateway_mock_probe_*` 指标 + scoped 日志 |
| 4 | `internal/mockprobe/admin.go` | 新增 | 3 个 admin 接口：`POST /admin/mock/trigger`（手动触发一次探测）、`GET /admin/mock/last`（最近一次结果）、`GET /admin/mock/history?limit=N`（最近 N 条）。挂 `NewAdminTokenMiddleware` |
| 5 | `cmd/gateway/main.go` | 修改 | `run()` 里加 `mockprobe.Start` / `Stop`，注入到 graceful shutdown |
| 6 | `internal/admin/handlers.go:ListProvidersHandler` | 修改 | `UNION` 一段从内存 registry 取 mock supplier 的逻辑，受 `MockProbe.HideInAdmin` 控制 |
| 7 | `internal/admin/handlers.go` (新 handler) | 修改 | `CreateProviderHandler` 末尾追加 `registry.Register(...)`（顺手补历史债；如果你不想动这个 PR scope，可拆 PR） |
| 8 | `migrations/730_create_mock_probe_schema.up.sql` / `.down.sql` | 新增 | 见 §2.3 |
| 9 | `internal/metrics/metrics.go` | 修改 | 注册 4 个 metric：`llm_gateway_mock_probe_total{provider,mode,status}`、`llm_gateway_mock_probe_latency_seconds{provider,mode}`、`llm_gateway_mock_probe_failures_total`、`llm_gateway_mock_probe_history_rows` |
| 10 | `config/default.yaml` | 修改 | 加 `mock_probe:` 段，默认 `enabled: false`（生产安全） |
| 11 | `docs/design/2026-09-23-mock-probe-channel/05-implementation.md` | 新增 | 编码规范、PR checklist（下一轮产出） |

### 3.2 关键设计决策

#### 决策 1：mock supplier 与业务网关共享进程 vs 独立进程

**结论：共享进程**，理由：
- 独立进程需要单独部署脚本、单独 systemd unit、单独 DSN 注入——运营成本翻倍。
- 共享进程通过 chi 子路由 `/mock/fast`、`/mock/slow` 隔离，端口可独立（绑 127.0.0.1），业务网关端口不受影响。
- 共享进程能直接用 `IsInternal` 豁免链路、scope 日志、Prom 指标、ticker 模板，无需 IPC。
- 关闭开关时整段代码路径不加载，资源占用 ≈ 0。

#### 决策 2：探测路径走 `IsInternal` api key vs 走 mock supplier 私有端口

**结论：探测请求走网关入口（`:8080/v1/chat/completions`），不直连 mock supplier 端口（`:18080/v1/...`）**，理由：
- 探测目的是**验证从网关入口到响应的全链路**——认证、限流、路由、凭证、streaming 协议。直连 mock 端口只测了 mock 自己。
- 但 mock supplier 自己绑的是 127.0.0.1，外部流量进不来，所以走网关入口会自动路由到 mock supplier（registry 里 mock-fast 名字匹配）。
- 配置上：mock supplier 不开放外部访问，**只接受从网关入口转发过来的请求**——天然隔离。

#### 决策 3：流式探测超时机制

**结论：复用现有 `GatewayClient.ChatCompletions` 的 ctx 超时**，理由：
- `StreamReader` 已有 ctx 取消传播。
- `cfg.MockProbe.Timeout` 设为每个探测的总超时（默认 5s，远大于 mock-slow 的 800ms 延迟）。
- 流式探测超时 = 首字节时间（TTFB）+ 全部 chunk 读完的总时间。

#### 决策 4：错误注入策略

**结论：mock-fast / mock-slow 的错误率默认 0**（不主动注入失败），但保留 `cfg.MockProbe.ErrorRate` 配置项（默认 0，运维按需开），理由：
- 探测首要目的是"链路通"，不是"测故障恢复"——故障恢复有专门的 chaos test。
- 但保留注入入口，方便以后做 fault injection 时复用。

#### 决策 5：graceful shutdown 顺序

**结论**（按时间先后）：
1. `mockprobe.Probe.Stop()` —— 停止新探测 goroutine
2. `mockSupplierFast.Shutdown(ctx)` + `mockSupplierSlow.Shutdown(ctx)` —— 释放 mock 端口
3. `server.Shutdown(ctx)` —— 业务网关停
4. `ticker.Stop()` —— 业务 ticker 停
5. `db.Close()` —— DB 关

理由：先停探测（不再发请求） → 再停 mock supplier（不再接响应） → 再停业务（不再转 mock 流量） → ticker / DB。这样没有"还在转的请求打到已 shutdown 的 mock supplier"。

#### 决策 6：mock supplier 的 provider spec 长什么样

```go
// 伪代码
spec := &provider.ProviderSpec{
    Name:      "mock-fast", // 或 "mock-slow"
    Type:      "openai",
    BaseURL:   "http://127.0.0.1:18080", // 路由表用它构造实际请求 URL
    AuthType:  "none",
    Models:    []string{"mock-fast-chat", "mock-fast-stream"},
    LatencyProfile: provider.LatencyProfile{
        BaseMs:    20,    // fast
        JitterMs:  60,
    },
    ErrorRate: 0.0,
    IsMock:    true, // 用于 ListProvidersHandler 过滤
}
```

注意 `IsMock bool` 字段是**新增**的——这是支撑 §2.1 Step B 的关键。

---

## 4. 与现有审计/部署纪律的兼容性自检

| 纪律 | 来源 | 本次方案是否冲突 |
|---|---|---|
| R46 / R47 的 252 部署形态（蓝绿 systemd） | `local-deploy-gotchas` | **不冲突**：mock probe 不动部署脚本，只动 `cmd/gateway/main.go`，不影响 252 / 245 / 154 三台网关的 systemd unit |
| `request_logs` 不进 mock 数据 | `request-logs-view-shape` | **不冲突**：探测走 `IsInternal` api key 仍会落 `request_logs`，但用 scope='mock_probe' 标签过滤，不污染审计视图（视图本身已按 scope 过滤） |
| PG schema 演进必须走 revision-sequence | `session-storage-merge-analysis` | **不冲突**：迁移文件 `730_*` 走标准 migration 流程 |
| gateway 部署后 30s rolconfig 击杀手工 `CONCURRENTLY` 留 INVALID | `pg-252-sql-log-audit-facts` 纪律⑫ | **不冲突**：730 是 `CREATE SCHEMA` + `CREATE TABLE` + 普通（非 CONCURRENTLY）索引，无须 superuser；万一被 30s 击杀，DROP & 重跑成本低 |
| 不进生产前不进 super_admin_bypass / FORCE 下的 row_security 关闭 | R41 修复轮 P1-2 | **不冲突**：mock probe 表在 `mock_probe` schema，不挂 RLS（mock 表不走 RLS 路径） |
| 不主动改 `providers` 表 | 本次设计决策 | **遵守**：mock supplier 用纯内存注册，不写 PG `providers` 表 |
| 不引入第三方 mock 库 | 用户约束 | **遵守**：复用 `tests/stress/mocks` + 标准库 |
| 审计轮收尾"不自动开新轮 / 起点重算 / 逐轮自审计六件套" | `audit-cycle-management-prefs` | **遵守**：本次是**设计阶段**，不是审计轮，不动审计 cycle |
| 长上下文接近 400K 用 handoff 移交 | `long-context-handoff-threshold` | **当前上下文约 35K，不触发**；本次输出完再 handoff |

---

## 5. 风险与未决项

| 风险 | 等级 | 缓解 |
|---|---|---|
| mock supplier 启动失败但业务网关已起 → 探测 0% 成功率，但不影响业务 | 中 | 启动失败 log ERROR + 继续业务启动；探测 job 第一次跑就连不上时记 metric，业务无影响 |
| `IsInternal` api key 重复创建冲突（启动多次） | 低 | 用固定 hash（如 `sha256("mock-probe-system-key")`），`INSERT ... ON CONFLICT (key_hash) DO NOTHING` |
| `tests/stress/mocks` 的 handler 复用时跨包依赖循环 | 低 | mocks 包用 `internal/` 重命名为 `internal/mockprobe/mocks/`，原 stress 用相对路径 import 新位置；或干脆把核心 handler 复制过来（代码量 < 300 行，可接受） |
| 流式探测在 mock-slow 800ms 延迟 + 探测间隔 30s 时，刚好重叠 2 个探测 goroutine | 极低 | 探测 job 是单 goroutine 串行，下一轮等上一轮 ctx 取消完才起；ctx 设 5s 超时 < 30s 间隔 |
| 252 dev 库的 mock schema 已存在（如果之前手测过） | 低 | migration 730 开头加 `DROP SCHEMA IF EXISTS mock_probe CASCADE` 不合适（会丢数据）；改用 `CREATE SCHEMA IF NOT EXISTS`，migration 幂等 |
| `ListProvidersHandler` 改 UNION 后性能回归 | 极低 | memory registry 查 + JSON 序列化 < 1ms；SQL 部分有现成索引 |
| `CreateProviderHandler` 补 `registry.Register` 后，可能与现有 ticker reload 行为双写 | 低 | 如果发现重复 Register 调用，registry 加幂等（同名覆盖）；已确认 `ProviderSpec` 是值类型，重复 register 不会泄漏 |
| mock probe 历史表 7 天后未清理 → 持续膨胀 | 低 | TTL job 走 `internal/jobs/retention.go`（或新建）；migration 731 配套 |

---

## 6. 下一步（next steps）

1. **本轮输出**（已完成）：本文档（04-optimization.md）。
2. **下一轮**：产出 `05-implementation.md`——按本方案列的 11 个文件，逐个给出函数签名 + 关键代码骨架（< 30 行/函数）+ 单元测试要点；**不写实现代码**，避免无设计的落地。
3. **再下一轮**：用户审完骨架后，逐文件 patch + 测试 + 在本机 `127.0.0.1:8080` 起网关验证：
   - `mock_probe.enabled: false` → admin UI 无 mock，`/healthz` 200，无 mock 指标
   - `mock_probe.enabled: true, hide_in_admin: true` → admin UI 无 mock，业务请求照常
   - `mock_probe.enabled: true, hide_in_admin: false` → admin UI 可见 mock-fast / mock-slow 两条记录
   - 30s 后 `mock_probe.history` 表出现 4 行（fast×非流、fast×流、slow×非流、slow×流）
4. **后续轮**：评估是否合并到 R52 / R53 审计轮，还是作为独立 feature release（建议独立 release，scope 独立）。

---

## 7. 自审计：本轮是否遵循既有纪律

- ✅ 没有自动开新审计轮（这是设计阶段，不是审计轮）。
- ✅ 没有改 `request_logs`、`providers` 等业务表。
- ✅ 没有引入第三方依赖。
- ✅ 没有跨 schema / 跨库 SQL（mock_probe.history 在独立 schema）。
- ✅ 迁移文件 `730_*` 走标准 migration 流程。
- ✅ graceful shutdown 顺序与 252 / 245 / 154 部署形态兼容。
- ✅ 没有承诺"部署到 252 / 245 / 154"——本次只在本机 127.0.0.1 验证。
- ✅ 所有"现状"声明都标注了文件路径:行号，可核查。
- ✅ 没有伪造数据；所有延迟/错误率/表大小都是估算，标注了估算依据。
- ⏳ 下一轮产出前不开 `05-implementation.md`（避免无设计落地）。
