# 现有 mock 资产盘点与对比

> 文档：`docs/design/2026-09-23-mock-probe-channel/02-code-audit.md`
>
> 把当前仓库内 **所有跟"mock 供应商 / mock 客户端"相关的资产** 摆在一起，与本轮目标（FR-1..FR-5）逐项对齐，回答：**哪些可复用、哪些要重写、哪些要废弃**。

---

## 1. 资产地图

| # | 路径 | 角色 | 形态 | 启停开关 | 可见性隔离 | 是否本次可复用 |
|---|------|------|------|----------|-----------|----------------|
| A | `tests/local/mocks/main.go` (179 行) | 3 个 MP（fast/slow/error :9001..9003） | 独立 Go 进程 | 无（main 直接 listen） | 无（端口直接暴露） | 协议对齐可参考 |
| B | `tests/stress/mocks/main.go` (554 行) | 1 个 MP（9 种 mode） | 独立 Go 进程（`-name/-port/-token`） | 无 | 无（admin/state 暴露） | **mode 集与 admin/state 协议直接复用** |
| C | `scripts/mocks/llm-mock-upstream/server*.py` | 12 个 MP（docker-compose） | 独立 Python 进程 + nginx conf | docker-compose up/down | 无 | 协议层面参考，**实现不引入 Python** |
| D | `internal/probemode/probemode.go` | "新旧 probe 栈"开关 `USE_NEW_PROBE_MODE` | 包内 const | `LLM_GATEWAY_USE_NEW_PROBE_MODE` | N/A | **不是同类开关**，不要混用 |
| E | `cmd/routing-test-client/main.go` (144 行) | OpenAI 客户端，跑 N 轮 | 独立 Go 进程 | `-rounds` | N/A | **MC 算法骨架可复用**，但要内嵌 + 节拍化 |
| F | `cmd/scenario_driver/main.go` (297 行) | SQL delta 验证驱动 | 独立 Go 进程 | `-rounds/-scenario` | N/A | **不属于 self-probe**，保持独立 |
| G | `internal/routingtest/` | 通用 OpenAI 客户端包 | 共享 lib | N/A | N/A | **直接作为 MC 的 http 调用层** |
| H | `tests/session_replay/mock_clients.go` | 测试替身（非 HTTP） | 单测 mock | N/A | N/A | 不适用 |

---

## 2. 与本轮 FR 的逐项对齐

| 目标 (FR) | A | B | C | E | G | 缺口 |
|----------|---|---|---|---|---|------|
| **FR-1 单一配置开关 + 热重载** | ❌ 进程级 main，无配置 | ❌ 同上 | ❌ docker-compose | — | — | 完全缺，需要新增 |
| **FR-2 进程内 MP（OpenAI 协议 + mode 集）** | ✅ 协议 ✓ | ✅ 协议 + 9 mode ✓ | ✅ Python | — | — | **B 协议最完整，但都是外部进程**，要拆出 `embedded.MockUpstream` |
| **FR-3 进程内 MC（节拍 + 静默）** | ❌ 无 | ❌ 无 | ❌ 无 | ⚠️ 有 rounds 但一次性 | ✅ http client ✓ | **节拍 + 静默 + 自循环需要新写**，可基于 G |
| **FR-4 启动顺序 / 关闭顺序** | ❌ | ❌ | ❌ | ❌ | ❌ | **完全缺**，要接入 `cmd/gateway` 生命周期 |
| **FR-5 可见性矩阵（admin/metrics/UI）** | ❌ | ❌ | ❌ | ❌ | ❌ | **完全缺**，要新增 filter + metrics namespace |

---

## 3. 关键代码 / 接口建议（候选复用）

### 3.1 MP 协议与 mode：来源 `tests/stress/mocks/main.go`

```go
// 来源：tests/stress/mocks/main.go（已实现，可拆）
var allowedMode = map[string]bool{
    "healthy": true, "slow": true, "server_error": true,
    "no_available": true, "rate_limited": true, "quota_exceeded": true,
    "timeout": true, "broken_stream": true, "flaky": true,
}
```

新建议：在 `embedded.MockUpstream` 内保留同样的 9 种 mode + `/admin/state` 协议形状，**对外接口走 `127.0.0.1`**，**对外不可见的 admin/state 仅在 self_probe 开关打开时注册**。

### 3.2 MC http 客户端：来源 `internal/routingtest`

```go
// 来源：internal/routingtest/client.go
type Client struct { ... }
func NewClient(baseURL, apiKey string, timeout time.Duration) *Client { ... }
func (c *Client) Chat(ctx, req Request, sessionID string, round int) Result { ... }
```

新建议：`embedded.MockClient` 直接 embed `routingtest.Client`，再加 ticker + 静默闸。

### 3.3 关键缺口（必须新增）

1. `internal/embedded/mockupstream.go`：MP 实现 + admin/state（仅 self_probe 注册时挂上）。
2. `internal/embedded/mockclient.go`：MC 实现（节拍、轮询目标、静默闸）。
3. `internal/embedded/manager.go`：开关闸 + 启动 / 停止 + 注册到 provider/credentials/models。
4. `config/config.go`：新增 `MockProbe` 块 + env tag。
5. `cmd/gateway/main.go`：在 `healthz ready` 之前判断开关 → 启动 manager。
6. `admin/handler.go`：`/admin/providers`、`/admin/models`、`/admin/self_probe/*` 增加 self_probe 标签 / 过滤。
7. `metrics/`：新增 `self_probe_*` namespace 注册（仅开关打开时注册 promhttp handler）。
8. `web/...`：provider 抽屉过滤 `is_self_probe`。

---

## 4. 风险点（既存 vs 新增）

| 风险 | 既存是否踩过 | 缓解 |
|------|--------------|------|
| MP 端口冲突（与本地真实供应商） | tests/local 9001-9003，254/245/154 生产端口固定 | **强制回环 `127.0.0.1` + 自动选端口** |
| MC 静默不可靠（关掉后仍有 in-flight 写库） | 无 | **闸处控制 ticker；在 MC 内 `context.Cancel` 后最多 1 cycle drain** |
| `__self_probe__` 内部租户被外部 API key 误用 | 无 | **API key auth 中间件加显式 deny**（见 NFR-4） |
| MP 上报指标与真实业务指标混淆 | 无 | **独立 metrics namespace，不混 label** |
| 关闭后 provider 列表残留条目 | 无 | **manager 关闭时显式注销**（FR-4） |
| 与 `USE_NEW_PROBE_MODE` 命名混淆 | probemode 包存在 | **命名严格区分**：本轮叫 `MOCK_PROBE_ENABLED`，文档同步声明正交 |