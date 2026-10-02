# Mock Probe 通道——现状清单

> 范围：用户提的 8 项需求 vs 当前代码真实状态
> 实地取证日期：2026-09-23
> 路径基准：仓库根 `/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4`

## 一、逐项对照

| # | 需求项 | 真实状态 | 证据 / 路径 |
|---|--------|----------|------------|
| 1 | `mock-fast` / `mock-slow` 供应商实现 | ❌ **零命中** | 全仓 Go 源码无 `mock-fast` 字样；`internal/providers/` 目录不存在；`provider/catalog` 仅注册真实供应商（`default-openai` 等），无 mock 子包 |
| 2 | Mock 客户端定时探测（2x2 通道） | ❌ **零命中** | `mock_probe` / `MockProbe` / `ProbeRunner` 全仓零命中；无 2x2 调度框架 |
| 3 | 配置 `mock_probe_enabled` 等四项 | ❌ **零命中** | 四项 grep 全仓零命中；**Config 结构体实际在 `config/config.go:14`（仓库根），不在 `internal/config/` 也不在 `pkg/config/`** |
| 4 | 凭证白名单 `mock-probe-client` | ❌ **零命中** | `internal/auth/` 目录下仅 `tenant_otel_anchor.go`，**没有 API key 校验文件**；`api_keys` 表由 gateway-v2 进程持有，未见现有 `kind=` 字段可借力 |
| 5 | `mock_probe_history` 表 | ❌ **零命中** | `migrations/` 最新两条：`035_add_heatmap_indexes.sql`、`orphan-in-progress-cleanup-20260827.sql`；无历史记录表 |
| 6 | 指标 `scope="mock_probe"` | ❌ **零命中** | `scope=` 全仓零命中；`internal/observability/` 下只有 `tenant.go` + `tracer.go`，**没有 `metrics.go`**；`internal/metrics/` 目录不存在 |
| 7 | Admin UI 过滤 Mock 供应商 | ⚠️ **半成品** | `admin/providers.go:679-684` 有 `health_status` 过滤分支；`listProviders` 在 `admin/providers.go:536`；**但没有按 provider kind/code 区分 mock 的逻辑** |
| 8 | 优雅停机 hook 框架 | ✅ **找到** | `internal/shutdown/manager.go`：`type Kind uint8`（行 10）、`const Stream/NonStream`（行 12）、`type Snapshot`（行 17）、`type Manager`（行 23）；`Shutdown(ctx, nonStreamTimeout, streamTimeout)` 内部"先 non-stream 后 stream"，零超时跳过该阶段 |
| 9 | Mock 供应商路由方式（基于 providerID） | ❌ **不成立**（详见 §三） | `cmd/gateway-v2/main.go:335` 注释"4-layer Limiter keyed by int (providerID, credentialID)" 是**限流器**键，不是供应商路由；`cmd/gateway-v2/main.go:343` `ProviderID: "default-openai"` 是限流器配置示例 |
| 10 | Mock HTTP 监听路径（主 mux） | ✅ **找到** | `cmd/gateway-v2/main.go:400` `mux := http.NewServeMux()`；已注册 `/healthz`(401) `/v1/chat`(461) `/v1/chat/completions`(461) `/v1/messages`(572) `/v1/responses`(684) `/v1/completions`(802) `/v1/models`(886) `/v1/models/`(928) `/metrics`(990) 等 |

## 二、易混淆现有包（务必区分）

| 包路径 | 用途 | 关键证据 |
|--------|------|---------|
| `internal/probemode/` | `LLM_GATEWAY_USE_NEW_PROBE_MODE` 开关下的**真实上游 LLM**探测（credential_selfcheck / node_probe / system_health） | `probemode/probemode.go:25` `func Enabled() bool`；`probemode/probemode.go:47` `func GuardStateTable() string` |
| `internal/reqprobe/` | OpenAI 请求"剥离可疑参数"工具（与 mock 无关） | `strip.go` / `coordinator.go` / `diagnose.go` 等 |
| `internal/probe/` | 真实上游追踪包（与 mock 无关） | `trace.go` 单文件 |
| `internal/probeutil/` | 真实探测工具：HTTP 客户端、超时、重试 | `endpoint_id.go` / `retry.go` |

> **结论**：`probemode` 是真上游 LLM 的健康巡检，**不是 mock 通道**。mock probe 是一个全新的子系统，不与上述任一包共享类型或调度。

## 三、关于"现状分析（实地取证）"表中两条 ❌误判

用户原消息 §1.2 表的两条 ✅ 在真实代码里是**误判**，需要在本轮挑明：

### 3.1 "Mock 供应商路由方式（基于 providerID）✅ 已实现"

**实际不成立**。`domains/provider/types.go:28` 的 `Provider` 是**纯数据结构**（ID/Code/DisplayName/BaseURL/Protocol/Enabled…），`types.go:73` 的 `Store` 接口**只含 CRUD**，全仓 grep `Invoke(` 与 `InvokeStream(` 在 Provider 命名空间里**零命中**。

最接近"主动调用"的实现在 `admin/providers.go:284`：

```
func probeProvider(ctx context.Context, baseURL, protocol, apiKey string) (bool, error)
```

这是 admin 侧一次性健康探测，**不是通用 Provider Invoke 契约**，无法被 mock 供应商复用。

> **影响**：mock 供应商要么走"主 mux 上注册专用 HTTP handler"（最贴合现状），要么新建 `Provider` 调用层契约（工作量大，与本需求无关），**建议采用前者**。

### 3.2 "Mock HTTP 监听路径（主 mux）✅ 已实现"

这条**部分成立**。`cmd/gateway-v2/main.go:400` 确实是主 mux 装配点，但**目前所有 `/v1/*` 端点都需要带 API key 通过限流器鉴权**（参考 `main.go:335-343` 的限流器注释）。

> **影响**：mock 客户端若要走网关入口验全链路，需要为 `mock-probe-client` 这条凭证**专门走一条鉴权旁路**（白名单机制），否则会被现有鉴权拒绝。

## 四、现有可借力基础设施（✓ 可直接复用）

| 设施 | 导出 API | 用途 |
|------|----------|------|
| `internal/shutdown/manager.go` | `NewManager()`、`Register(kind, id) bool`、`Unregister(kind, id)`、`Snapshot() Snapshot`、`Shutdown(ctx, nonStreamTimeout, streamTimeout)` | mock 探测 goroutine 的优雅停机（注册为 `NonStream` kind） |
| `internal/sse/line_reader.go` | `NewLineReader(reader, limit)`、`ReadLine()`、`ReadLineWithContext(ctx, timeout, closer)`、`ErrLineTooLong` | mock 流式响应按行写出 |
| `cmd/gateway-v2/main.go:400` 主 mux | `mux.HandleFunc("/path", h)` | 注册 mock 供应商 HTTP handler |
| `migrations/035_add_heatmap_indexes.sql` | SQL 写法参考 | 仿写 `mock_probe_history` 表 |
| `admin/providers.go:679-684` | `health_status` 过滤分支结构 | 仿写 mock 供应商过滤分支 |

## 五、距离需求的总差距清单

1. **绿色字段**：`internal/providers/` 包、`internal/mockprobe/` 包、`internal/auth/mockprobe_bypass.go`、`internal/observability/metrics.go`（含 `scope="mock_probe"` 体系）、`migrations/036_mock_probe_history.sql`、`admin/providers.go` mock 过滤分支。
2. **修改字段**：`config/config.go:14` Config 结构体加 4 个字段、`cmd/gateway-v2/main.go:400` mux 加 2-4 个 mock 端点 + 鉴权旁路 + 启动 mock probe goroutine。
3. **测试字段**：`internal/providers/mock/*_test.go`、`internal/mockprobe/runner_test.go`、`internal/auth/mockprobe_bypass_test.go`。

---

**附：用户消息中提及的 `docs/design/2026-09-23-mock-probe-channel/05-implementation.md` 目前不存在**，待本轮方案敲定后写入。