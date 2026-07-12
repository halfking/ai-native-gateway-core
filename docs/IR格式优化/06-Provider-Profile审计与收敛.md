# Provider Profile 审计与收敛

**日期**: 2026-07-12  
**状态**: P0 误删修复完成；完整 allowlist 等待来源 provider 注入

## 1. 审计发现

### 1.1 无条件 response strip 是数据损坏风险

原 `ChatExecutor.WriteNonStreamResponse` 没有 `Candidate`，却对所有 OpenAI-shaped 响应依次运行 MiniMax、GLM、DeepSeek 和 Doubao stripper。这会把其它 provider 的标准字段、usage detail 或 reasoning 删除。

流式回调也没有 `Candidate`，但对所有流传入 `StripMinimaxFieldsBody`，存在同一类风险。

**已修复**：

- 无 provider context 的 non-stream 公共方法不再运行任何 vendor stripper。
- 主 non-stream 执行路径只在 `cand.CatalogCode == "minimax"` 时运行 MiniMax stripper。
- `StreamHandler` 现携带 `Candidate.CatalogCode`；stream callback 使用 `StreamStripperForCatalogCode`，未知 catalog 返回 nil，只有 MiniMax 启用 MiniMax stripper。

## 2. 字段分类

| 分类 | 处理 | 示例 |
|------|------|------|
| Canonical | 映射到 IR | text、tool call、image、thinking |
| Standard compatible | 原样保留在同协议响应 | `id`、`object`、`system_fingerprint`、`usage` |
| Usage detail | 服务端保留并归一化；不作为 generic extension 转发 | cache hit/miss、reasoning token detail |
| Same-provider-only | 需 source+target provider、endpoint、model family 同时匹配 | MiniMax `reasoning_split`、GLM `web_search`、Ollama native options |
| Capture-only / sensitive | 仅在匹配 provider 的客户端投影阶段移除 | experiment id、safety score、内部 workflow id |
| Unknown | 跨协议或跨 provider drop，写 privacy-safe LossReport | 未登记顶层字段 |

## 3. 官方字段审计

| Provider | 不应 generic strip 的字段 | 仅同 provider / canonical 处理 | 未验证 capture 字段 |
|----------|-----------------------------|------------------------------|--------------------|
| DeepSeek | `object`、`system_fingerprint`、cache usage、`reasoning_content` | reasoning + cache detail | `deepseek_request_id`、`model_type`、旧 cache aliases |
| GLM | `request_id`、reasoning content、cached/reasoning usage detail、`system_fingerprint` | `thinking`、`reasoning_effort`、`web_search`、content filter | `zhipu_request_id`、retrieval docs、model version |
| Qwen | OpenAI-compatible standard surface、`stream_options.include_usage` | DashScope endpoint/model extensions | 未建立 verified capture profile |
| MiniMax | `object`、`system_fingerprint`、`service_tier`、usage/reasoning detail | `reasoning_split`、`reasoning_details` | `nvext`、`base_resp`、workflow/internal sensitivity fields |
| Ollama | native chat eval counts/durations 是 telemetry，不是 cloud billing | `format`、`options`、`think`、`keep_alive` | `context`、`raw`、`template` 不应被当作 `/api/chat` 标准字段 |

来源见 [04-协议与验证矩阵.md](./04-协议与验证矩阵.md) 的一手资料索引。

## 4. 已修改的 strip policy

下列字段不再被现有 stripper 删除：

- MiniMax：`object`、`system_fingerprint`、`service_tier`、usage detail、reasoning。
- GLM：`system_fingerprint`、`request_id`、reasoning content、usage cache/reasoning detail。
- DeepSeek：`system_fingerprint`、reasoning content、官方 cache usage/reasoning usage detail。
- Doubao：`system_fingerprint`。

仍保留的 strip 字段必须标注为 capture-only 或 safety-sensitive，且只允许在 provider 已确认时运行。

## 5. ProviderProfile 设计

```go
type ProviderProfile struct {
    ID               string
    Protocol         string
    Endpoint         string
    RequestAllow     map[string]bool
    ResponseAllow    map[string]bool
    CanonicalPaths   map[string]FieldClass
    UsagePaths       map[string]UsageMetricName
    SameProviderOnly map[string]bool
    CaptureOnly      map[string]bool
}
```

恢复 unknown request extension 的安全条件：

```text
source_provider == target_provider
AND source_protocol == target_protocol
AND endpoint matches
AND profile allows field
```

目前 `TransportContext` 只有 client/upstream protocol，`InternalRequest` 只有 `TargetProvider`，两者都不能证明 `source_provider == target_provider`。因此不能把 `TargetProvider` 当成 allowlist 的充分条件。

## 6. 后续前置任务

1. 在路由解析后，将可信 `ClientProviderProfile` 与 `UpstreamProviderProfile` 写入 transport/conversion context。
2. 每个 profile 绑定 endpoint 和 model family，不使用全局 vendor string。
3. 将 `RequestExtensions.Headers` 接入同一 profile policy。
4. 真实脱敏 capture 按 provider/profile 进入 fixture；未经一手文档或 capture 验证的字段不得加入 allowlist。
5. usage detail 进入 UsageNormalizer，不再由 stripper 决定是否保留。

## 7. 验证

- `ChatExecutor` 无 candidate 的响应方法不会调用 vendor stripper。
- 主 non-stream 路径仅在 MiniMax candidate 上调用 MiniMax stripper。
- stream callback 接收 candidate catalog code；unknown provider 不执行 strip，MiniMax 仅执行 MiniMax policy。
- vendor strip 回归测试验证 official/usage 字段被保留。
