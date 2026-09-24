# r0924 supplier-protocol-optimization 批判式审计 Handoff

**项目**: 供应商协议多路化（multi-protocol endpoint）+ ollama-native 真实接入
**最后更新**: 2026-09-24
**状态**: ⚠️ **commit `8b94131d9` 已在 main 分支，但仅覆盖 P0+P2+P3；P1/P4/P5 仍未落地**
**负责人**: handoff 接收方
**关联 commit**: `8b94131d9`（halfking / 2026-09-24 09:30）

---

## 一、本轮交付物

### 1.1 文档（已写 + 已纠正）

| 文件 | 行数 | 状态 |
|------|------|------|
| `docs/供应商协议优化.md` | 816 | 高阶设计，已合并 commit `8b94131d9` |
| `docs/供应商协议优化-实施规划.md` | ~1500（含 §12 审计盘点） | 实施规划 + **本轮审计纠正**：① 头部 ⚠️ 引用块标注与 commit 事实不符；② 末尾 §12 追加 6 节（已落码/待办/测试结果/问题清单/矩阵/诚实报告/下一步） |

### 1.2 代码（commit `8b94131d9` 已合入 main）

15 文件 / ~1400 行新增，**不是**方案文档 §3.1 声称的 32 文件 / 3300 行。详见 [供应商协议优化-实施规划.md §12.1](../供应商协议优化-实施规划.md)。

### 1.3 测试结果

```text
internal/endpointselect   ok    12 tests green
internal/ir               ok    DetectProtocol + parse/serialize ollama golden PASS
internal/upstreamurl      ok    TestBuild + OllamaChatURL PASS
provider/catalog          ok    TestNormalizeProviderProtocol + TestProtocolsMatchExecutors + TestRecommendedProtocolForBaseURL F8 fix PASS
go build ./...            0 错误
go vet (上述四个包)        0 告警
```

---

## 二、批判式审计的核心发现

### 2.1 事实漂移（文档说代码不存在，代码实际存在）

| 文档原述 | 事实 | 严重度 |
|---------|------|--------|
| §1.1「`internal/ir/serialize_ollama.go` **完全不存在**」 | 实际 240 行已存在（含 `SerializeOllama`/`serializeOllamaMessages`/`serializeOllamaTools`/`ErrSerializeOllamaMissingModel`/`OllamaExtensionPrefix`） | 严重 — 已纠正 |
| §1.1「`parse_ollama.go` 不存在」 | 实际 166 行已存在（`ParseOllamaResponse`） | 严重 — 已纠正 |
| §3.1「需新增 32 个文件、~3300 行」 | 实际合入 15 文件、~1400 行 | 严重 — §12 已明示 |

### 2.2 设计缺陷（代码 ↔ 文档漂移）

| ID | 问题 | 决策 |
|----|------|------|
| A4 | `selector.go:205` `_ = MatchStage3` 死代码；Stage 3 永远不产生 | 保留：observability label 稳定性优先 |
| A5 | §3.5 描述 Stage 3 与代码不一致 | §12 已记录 |
| A6 | `wantFamily==""` 且 `primary.VendorNative==""` 归 Stage 1C（保守） | 不修：观察期 |
| A7 | `detect.go:140-144` 「if hasOptions && !hasMessages」前一行已 early return，是死代码 | 不修：保留供未来 `ollamaExclusive==0` 路径复用 |

### 2.3 未交付的真实差距

**最严重**：`executor_dispatch.go` **未调** `endpointselect.Select()`，即 selector 库是**孤儿代码**，运行时仍未启用多协议路由。这意味着即便 P3 的 ollama wire 已落码，dispatch 仍按 `cand.Protocol` 单值 switch —— `ollama-native` 仍走 `default → executeOpenAI`，即仍处于「静默 chat 降级」状态。

**次严重**：`provider.Candidate` **未加** `NativeEndpoints` 字段，`provider/client.go` 的 SQL 未加 `LEFT JOIN LATERAL`。

**再次**：`executor_ollama.go`（出站 `/api/chat` 执行器）**未建**。库函数齐全（`SerializeOllama` + `ParseOllamaResponse`），但无 executor 串成完整调用链。

**P5 passthrough executor** 未建。

**P1 admin UI endpoint CRUD** 未建。

---

## 三、下一轮 prompt（可直接复用）

> 给接手者的精确 prompt：
>
> **任务**：完成 r0924 supplier-protocol-optimization 的 **P4 dispatch 接线** + **P4+ executor_ollama 落码**。代码现状见 commit `8b94131d9`（已在 main），selector 库就绪但未接入运行时。
>
> **必读前置**：
> 1. `docs/供应商协议优化-实施规划.md` §12（实际落地与待办盘点，含审计结论与文件级差异）
> 2. `docs/vendor-formats/ollama.md`（Ollama 原生 wire 形态）
> 3. `internal/endpointselect/selector.go`（决策算法，4 stage + Stage 3 死代码占位）
> 4. `internal/ir/serialize_ollama.go` + `parse_ollama.go`（已落码的库函数）
> 5. `internal/upstreamurl/upstreamurl.go` `OllamaChatURL`（URL 构造 SSOT）
>
> **本轮硬性要求**：
>
> 1. **`provider/client.go`**：`Candidate` 加 `NativeEndpoints []CandidateEndpoint` 字段；候选查询 SQL 加 `LEFT JOIN LATERAL (SELECT jsonb_agg(...) FROM provider_endpoint_protocols WHERE provider_id=p.id AND enabled=true) ep ON true`；JSONB 反序列化填入字段。
>
> 2. **`domains/streaming/executors/executor_dispatch.go`**：在 `switch cand.Protocol` 之前调用 `endpointselect.Select(candLite, params.ClientProtocol, wantFamily)`；把 `Decision.Protocol` + `Decision.BaseURL` 透传给执行器；新增 `case providercatalog.ProtocolOllamaNative` 路由到新 executor；保留旧 `default → executeOpenAI` 作为 fallback（`FF_ENDPOINT_SELECTOR=false` 时行为 100% 等价旧版）。
>
> 3. **`domains/streaming/executors/executor_ollama.go`**（新建）：复用 `SerializeOllama` 出站，URL 用 `OllamaChatURL(execParams.EndpointBaseURL())`；非流响应 `ParseOllamaResponse` + 包装成 OpenAI Chat wire；流响应 `ParseOllamaStreamChunk`（已存在）+ NDJSON→SSE 转换；`Content-Type: application/json`，`Authorization` 仅在 `apiKey!=""` 时设。
>
> 4. **FF flag 默认 off**：`FF_ENDPOINT_SELECTOR=false`（dispatch 旧 switch），`FF_OLLAMA_NATIVE=false`（即使 selector 命中 ollama endpoint 也走 default）。
>
> 5. **测试**：
>    - `provider/client.go` 候选加载 SQL LATERAL 的 SQL 单元测试（用 in-memory pgx 或 sqlmock）
>    - `executor_dispatch.go` 新增 `TestDispatchRouteOllamaNative_*` 表驱动测试（FF on/off 两条路径）
>    - `executor_ollama.go` 端到端：mock 上游验证 byte-level 透传
>    - 跑 `go test -count=1 ./...` 必须全绿
>
> 6. **不要做**：
>    - 不动 P1 admin UI（下一轮做）
>    - 不做 P5 passthrough（最后做）
>    - 不引入新依赖
>    - 不删 `_ = MatchStage3`（保留 observability 稳定性）
>    - 不改 `provider_endpoint_protocols.sql` DDL
>
> **交付物**：
> - 1 个 commit（`feat(r0924-supplier-protocol-optimization): P4 dispatch wiring + ollama-native executor`）
> - 新增 `internal/upstreamurl.Build` 测试 + `executor_ollama_test.go` + `executor_dispatch_test.go` 增量
> - 更新 `docs/供应商协议优化-实施规划.md` §12 把 P4 项标记 ✅
> - 输出最终交付报告：commit hash / 测试结果 / 行为变更点 / 灰度回滚手段

---

## 四、关键文件指向（Quick Links）

| 关注点 | 路径 |
|--------|------|
| Selector 决策库（已落码） | [internal/endpointselect/selector.go](../../internal/endpointselect/selector.go) |
| Selector 测试（已落码） | [internal/endpointselect/selector_test.go](../../internal/endpointselect/selector_test.go) |
| Ollama 序列化库（已落码） | [internal/ir/serialize_ollama.go](../../internal/ir/serialize_ollama.go) |
| Ollama 响应解析库（已落码） | [internal/ir/parse_ollama.go](../../internal/ir/parse_ollama.go) |
| Ollama 流解析库（已落码） | [internal/ir/parse_ollama_stream.go](../../internal/ir/parse_ollama_stream.go) |
| URL SSOT（已落码） | [internal/upstreamurl/upstreamurl.go](../../internal/upstreamurl/upstreamurl.go) |
| Detect Ollama 分支（已落码） | [internal/ir/detect.go](../../internal/ir/detect.go) line 100-144 |
| Schema（已落码） | [sql/objects/tables/provider_endpoint_protocols.sql](../../sql/objects/tables/provider_endpoint_protocols.sql) |
| Migration V800（已落码） | [deploy/sql/migrations/V800__provider_endpoint_protocols.sql](../../deploy/sql/migrations/V800__provider_endpoint_protocols.sql) |
| **Candidate 改造（待办）** | [provider/client.go](../../provider/client.go) |
| **Dispatch 接线（待办）** | [domains/streaming/executors/executor_dispatch.go](../../domains/streaming/executors/executor_dispatch.go) line 839 |
| **Ollama executor（待办）** | `domains/streaming/executors/executor_ollama.go` |
| **Passthrough executor（待办）** | `domains/streaming/executors/passthrough.go` |
| **Admin endpoint CRUD（待办）** | `admin/admin_endpoint.go` |
| F6 协议矩阵审计 | [docs/audit/2026-09-23-r59-s3-protocol-findings.md](../audit/2026-09-23-r59-s3-protocol-findings.md) |
| R0924 实施规划盘点 | [docs/供应商协议优化-实施规划.md §12](../供应商协议优化-实施规划.md) |

---

## 五、不要做的事（hardening）

1. 不要把「commit `8b94131d9` 已落地」当成「全部完成」向运营汇报。selector 库是孤儿代码，运行时仍走旧 dispatch。
2. 不要直接删除 `selector.go` 的 `_ = MatchStage3` 占位常量。observability label 在 metric/trace 里已固化。
3. 不要改 `provider_endpoint_protocols.sql` 的 `vendor_native` 列为枚举类型。SSOT 是 `discovery.vendorCanonicalFamilies`（自由字符串），未来加新厂商 family 不需要 ALTER 表。
4. 不要在 `executor_ollama.go` 里再做 NDJSON→SSE 字节透传 —— 那是 P5 passthrough 的事，本轮走 IR 桥（`ParseOllamaResponse` → `SerializeOpenAIChat` 路线）即可。
5. 不要把 `OllamaChatURL` 改造以适配 `/v1/chat/completions` 兼容层 —— 当前需求是 native `/api/chat`，不要扩散职责。