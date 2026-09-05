# Phase A Extensions 实施结果

**日期**: 2026-07-12
**状态**: 方案 A 与方案 B 均完成

## 1. 实施范围

本轮处理 Extensions 方向隔离和 `image_url.detail` 嵌套保真，不包含 Responses 原生 IR 或新 provider adapter。

## 2. 方案 A：双字段兼容

完成内容：

- `TransportContext` 新增 `RequestExtensions` 和 `ResponseExtensions`。
- 请求解析只填充 `RequestExtensions`。
- 同协议请求在发送上游前恢复未知请求字段。
- 跨协议请求不注入未知字段。
- 响应解析前单独提取 `ResponseExtensions`。
- 客户端响应只恢复 `ResponseExtensions`，禁止请求字段进入响应。
- 方案 A 的方向字段测试通过后立即进入方案 B；旧字段未进入生产调用链。

TDD 证据：

- RED：方向字段不存在，新增测试按预期编译失败。
- GREEN：方向隔离用例、同协议恢复、跨协议阻止和响应防泄漏用例通过。
- 包级测试：`go test ./domain ./domains/transformation -count=1` 通过。

## 3. 方案 B：删除旧字段

方案 A 测试通过后执行：

- 删除 `TransportContext.Extensions`。
- 删除兼容镜像写入逻辑。
- 所有 transport 层调用方和测试迁移到 `RequestExtensions`。
- `InternalRequest.Extensions`/`InternalResponse.Extensions` 未删除；它们属于 IR converter 层，不是本次 TransportContext 旧字段。
- `TransportIRConverter.extractResponseExtensions` 改用 response 专用 extractor，并新增完整 parse->serialize 响应扩展 round-trip 测试。
- 新增 `LossReport`；跨协议省略只保存不可逆字段名摘要和原因，不保存字段名或字段值。
- 新增双向 golden fixture、`_fixture_meta.json` 加载与必填字段校验。

## 4. 行为变化

| 场景 | 旧行为 | 新行为 |
|------|--------|--------|
| OpenAI -> OpenAI unknown request field | IR 可能丢失 | 恢复到上游请求 |
| OpenAI -> Anthropic unknown request field | 提取但不明确处理 | 不注入上游 |
| upstream unknown response field | 可能丢失 | 恢复到客户端响应 |
| request unknown field -> client response | 错误回写 | 明确禁止 |
| OpenAI `image_url.detail` | parser 丢弃 | IR 保存并在 OpenAI 输出恢复 |
| OpenAI detail -> Anthropic | 无明确规则 | 不输出无等价字段 |
| 跨协议 extension loss | 仅日志计数 | `LossReport` 保存不可逆字段名摘要和原因 |
| golden fixture | 无统一格式 | 元数据校验 + JSON 语义回放 |

## 5. 验证命令

```bash
go test ./domain -count=1
go test ./domains/transformation -count=1
go test ./domains/streaming/... -count=1
go test ./internal/ir/... -count=1
go test ./cmd/gateway -run '^$' -count=1
go vet ./domain ./domains/transformation/...
go test ./... -count=1
```

验证结果：以上测试和 vet 均通过。`govulncheck` 在当前环境未安装，因此依赖漏洞扫描未执行；本次 diff 已完成密钥泄漏和响应字段方向的静态安全审查。

## 6. 后续工作

1. 将允许恢复的 unknown request 字段收敛到 ProviderProfile allowlist。
2. 将 `RequestExtensions.Headers` 接入上游 request builder，执行 header allowlist。
3. 将允许恢复的 unknown request 字段收敛到 ProviderProfile allowlist。
4. 将 `RequestExtensions.Headers` 接入上游 request builder，执行 header allowlist。
5. 增加脱敏真实 capture，覆盖 DeepSeek、GLM、Qwen、MiniMax compatibility profile。
