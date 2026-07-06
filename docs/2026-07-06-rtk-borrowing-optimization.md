# 借鉴 rtk 优化提示词缓存/压缩/转发/拼接模块

> 日期：2026-07-06
> 分支：`feat/rtk-borrowing-optimization`（worktree `llm-gateway-go-rtk-opt`）
> 状态：已实现 + 已本地部署 + 已功能验证
> 约束遵守：未调整架构、未改接口签名、未改中间件链；全部改动为加法（新字段/方法/调用/env 开关）

## 一、rtk 学习结论

### 1.1 rtk 是什么

rtk（Rust Token Killer，`/Users/xutaohuang/workspace/ai/rtk`）是 **CLI 命令代理**，不是 HTTP/LLM 代理。它拦截 LLM 编码 agent 调用的 shell 工具（git/cargo/grep…），在工具输出**进入 LLM 上下文窗口之前**将其压缩 60-90%。

**关键澄清**：rtk 与我们的网关不在同一层 —— rtk 压缩的是"工具输出"，我们网关压缩的是"请求 prompt"。但 rtk 的**安全契约与压缩哲学**可直接借鉴。

### 1.2 rtk 可借鉴机制（已逐一核实源码）

| rtk 机制 | 源码位置 | 价值 | 本轮处置 |
|---|---|---|---|
| **never_worse 守卫** | `src/core/guard.rs`、`src/core/runner.rs`：过滤后输出若 ≥ 原始长度则丢弃、回退原始 | **高** | ✅ 新增 `compression/guard.go` |
| **Lossiness 枚举** | `src/core/toml_filter.rs:503`：区分"尾部截断(可恢复)" vs "整体摘要(不可恢复)" | **高** | ✅ `PrepareResult.Lossiness` + 指标 |
| 分层 cap 常量 | `src/core/truncate.rs`：CAP_ERRORS=20 / CAP_LIST=20 | 中 | 仅文档化，我们已有 sliding-window |
| chars/4 token 估算 | `src/core/tracking.rs:1284` | 中 | 不改（我们用 chars/3.5，更准） |
| 流式过滤抽象 | `src/core/stream.rs`：StreamFilter/BlockHandler | 中 | 不改（我们 SSE bridge 更成熟） |
| 声明式 TOML DSL | `src/core/toml_filter.rs` 8 阶管道 | 低 | 不移植（rtk 压工具输出，我们不压） |

### 1.3 rtk 的空白 = 我们的机会

rtk **完全没有缓存**（每次调用重新压缩），**没有跨请求拼接**。而我们：

- ✅ 已有完整的 `cache/` 包树（prefix/kv/delta/semantic），全部单元测试通过 —— **但没有任何活跃代码 import 它**（grep 确认）。
- ✅ `domains/session/cache_injector.go` 的 `CacheInjector` 完整实现了 —— **但 `NewCacheInjector` 从未被实例化**（死代码）。

**这是本轮最高收益、最低风险的优化点**：把已设计好、已测试好的模块接入线上转发流程。

### 1.4 我们现状的关键事实（已逐一核实）

1. 活跃路径 = v1 `streaming.ChatHandler`（`domains/streaming/handler.go:558`）。v2 pipeline **默认关闭**（`v2UsePipeline()` 需 `LLM_GATEWAY_USE_V2_PIPELINE=1`），即使开启也只是 v1 的前置包装。
2. `sessionCompressor.Prepare` 已在 `handler.go:1450` 调用（压缩已接入）。
3. **缺失**：`prefix.Stabilize`（前缀稳定化）、`CacheInjector`（注入 cache_control）都未接入。
4. **缺失**：`SessionCache` L1 是 `sync.Map` + "first-seen-first-evict" 伪 LRU（`session_cache.go:54-58` 自承认）。
5. **缺失**：压缩输出无 `never_worse` 守卫。

---

## 二、优化方案（5 个改动）

### 改动 1：`prefix.Stabilize` 接入热路径（核心，默认开）

**位置**：`handler.go`，session compression（:1450）之后、WAL 初始日志之前。

重排请求消息按稳定性分类（system → tools → history → tail），最大化上游 KV 前缀缓存命中。

- env `LLM_GATEWAY_PROMPT_CACHE_STABILIZE=0` 可关，默认 `1`。
- **设计决策（测试中发现并修正）**：Stabilize 是**重排**不是压缩，输出长度通常与输入相同（swap 而非 trim），因此 **不套 never_worse 守卫**（守卫的"必须严格更短"契约会误杀合法重排）。守卫只作用于 compress/inject 阶段。
- fail-open：任何无法识别的形状都原样返回（`prefix.go:111-113`）。

### 改动 2：`CacheInjector` 接入热路径（opt-in）

**位置**：`handler.go` 候选解析后、转发前。

为声明 `SupportsPromptCache` 的候选注入 cache_control 标记。`main.go:869` 后装配 `session.NewCacheInjector`（之前是死代码）。

- env `LLM_GATEWAY_PROMPT_CACHE_INJECT=1` 开启，**默认关**（依赖 candidate.CacheMode 数据准确性，先 opt-in 观察）。
- 套 never_worse 守卫。

### 改动 3：`never_worse` 守卫（新增 `compression/guard.go`）

借鉴 rtk 核心安全契约：压缩/变换**永不**输出比原始更长的结果。若 `processed ≥ raw` 则丢弃、回退原始。

- 仅作用于 compress/inject（真正的 expand 操作），不作用于 stabilize（重排）。
- 指标 `compression_regressed_total{stage}` + warn 日志。

### 改动 4：SessionCache L1 真正的 LRU

**位置**：`domains/hooks/compression/session_cache.go`。

用 `container/list` 双向链表 + map 替换 `sync.Map` + first-seen-first-evict 伪 LRU，O(1) promote/evict，容量 1024。热会话不再被误淘汰，避免 L2/L3 往返。**无新依赖**（标准库）。

### 改动 5：Lossiness 分类（借鉴 rtk Lossiness 枚举）

**位置**：`session_compressor.go` 的 `PrepareResult` + `metrics.go`。

`PrepareResult.Lossiness`（`none|tail|whole`）分类压缩牺牲了多少信息：
- `none`：delta/strip，无丢失
- `tail`：mechanical trim，中间可从缓存恢复
- `whole`：LLM summary，历史措辞**不可恢复**

指标 `compression_lossiness_total{lossiness}`，并写入 compression_meta JSONB。

---

## 三、文件改动清单

| 文件 | 类型 | 改动 |
|---|---|---|
| `domains/streaming/handler.go` | 改 | +2 字段 +3 setter，热路径插 stabilize + inject；WAL meta 加 lossiness |
| `cmd/gateway/main.go` | 改 | 装配 CacheInjector + env 读取 |
| `domains/hooks/compression/guard.go` | **新增** | NeverWorse 守卫 |
| `domains/hooks/compression/guard_test.go` | **新增** | 8 个守卫单测 |
| `domains/hooks/compression/session_cache.go` | 改 | L1 真 LRU |
| `domains/hooks/compression/session_cache_lru_test.go` | **新增** | 5 个 LRU 测试（含 -race 并发） |
| `domains/hooks/compression/session_compressor.go` | 改 | PrepareResult + Lossiness + classifyLossiness |
| `domains/hooks/compression/lossiness_test.go` | **新增** | 8 个 lossiness 测试 |
| `domains/hooks/compression/metrics.go` | 改 | +lossiness counter + Record/LossinessCount |
| `domains/streaming/prefix_guard_integration_test.go` | **新增** | 4 个集成测试 |

提交：`87e9f4b2 feat(compression): borrow rtk's never_worse guard, true L1 LRU, and wire prompt-cache optimization into the live path`（pre-commit 4 检查全过）。

---

## 四、验证报告

### 4.1 单元测试（`go test -race`）

| 包 | 结果 |
|---|---|
| `domains/hooks/compression/...` | ✅ ok 3.112s（含 guard 8 + LRU 5 + lossiness 8） |
| `domains/streaming/...` | ✅ ok 1.668s + executors 5.606s（含集成测试 4） |
| `cache/...`（prefix/kv/delta/semantic） | ✅ 全 ok |
| `cmd/gateway/...` | ✅ ok 0.593s |
| `domains/session/...` | ✅ ok 2.291s |

### 4.2 必跑门禁（CONTRIBUTING.md）

| 检查 | 结果 |
|---|---|
| `go vet`（改动包） | ✅ clean |
| `go build ./...` | ✅ exit 0 |
| `scan-secrets.sh --mode=strict` | ✅ CLEAN（43 文件，0 发现） |
| pre-commit（go vet + SQL lint + migration + vue-tsc） | ✅ PASS=4 FAIL=0 |

### 4.3 本地部署（`docker-compose.local-r112.yml`）

- 从 worktree 构建 gateway 镜像（`r112-gateway:local`），含新代码。
- 复用已运行的 postgres/redis/mock-upstream（project `llm-gateway-go`）。
- gateway 容器健康（`r112_gateway`，端口 8781）。
- 启动日志确认新代码加载：
  ```
  INFO prompt-cache prefix stabilization enabled (cache/prefix.Stabilize)
  INFO v3 session-level compressor wired (L1 in-mem + L2 Redis + L3 PG)
  ```

### 4.4 协议级功能验证（mock upstream，localhost:8781）

| 用例 | 结果 | 备注 |
|---|---|---|
| `/healthz` | ✅ 200 | |
| `/v1/models` | ✅ 200 | |
| OpenAI 非流式 chat | ✅ 200 + choices | |
| OpenAI 流式 chat | ✅ 200 + SSE `data:` 帧 | |
| Anthropic 非流式 messages | ⚠️ 502 "模型未返回任何内容" | **预先存在**：父版本（改前代码）同样 502，mock upstream 对 anthropic 非流式路由返回空，非本次回归 |
| Anthropic 流式 messages | ✅ 200 + SSE `event:`/`data:` 帧 | 用 mock 路由模型 gpt-4o-mini |
| `X-Gw-Prefix-Stabilized` 头 | ✅ 出现（OpenAI 多消息） | 证明新功能在线触发 |
| 畸形请求体 | ✅ 400（正确拒绝） | |
| Stabilize 对 Anthropic 顶层 system | ✅ changed=false（无需重排） | 正确：system 已在最稳前缀 |

**回归确认**：Anthropic 非流式 502 通过恢复父 commit 的 handler.go+main.go 重建验证 —— 父版本同样 502，确认是 mock upstream 对该路径的限制，**非本次改动回归**。

### 4.5 新功能在线触发证据

```
$ curl -XPOST localhost:8781/v1/chat/completions -d '{...volatile-tail-first...}'
< X-Gw-Prefix-Stabilized: reordered by stability class
```

Stabilize 正确识别"尾部在前"的次优排序并重排为 system-first，最大化 KV 前缀缓存命中。

---

## 五、风险与回退

| 风险 | 缓解 |
|---|---|
| Stabilize 改变请求体导致上游异常 | fail-open（出错回退原 body）+ `LLM_GATEWAY_PROMPT_CACHE_STABILIZE=0` 一键关 |
| CacheInjector 数据不准 | **默认关**（opt-in） |
| never_worse 误杀正常压缩 | 仅当 `processed ≥ raw` 才回归（宽松），并记指标 |
| LRU 替换引入并发 bug | `container/list` + `-race` 测试通过 |
| 误把 stabilize 套守卫 | **已在测试中发现并修正**：守卫只作用于 compress/inject |

所有改动在独立 worktree（`llm-gateway-go-rtk-opt`），随时可丢弃，不影响主工作区。

---

## 六、未来工作（不在本轮范围）

- HTTP 传输层压缩（gzip/zstd/brotli）：rtk 无可借鉴，且 SSE 压缩有兼容性风险，暂不动。
- `cache/kv`、`cache/delta`、`cache/semantic` 接入热路径：本轮只接了 `prefix` + `CacheInjector`，KV key/delta 增量编码可下一轮评估。
- Anthropic 非流式空响应（mock 环境）：需排查 mock upstream 对该路由的响应，属环境问题非代码问题。
