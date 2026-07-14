# 2026-07-14 — 网关丢失多模态内容（merge consecutive messages）

## 背景

用户报告：通过网关调用 minimax-m3 时，不支持多模态数据（如图片），
但直连 minimax-m3 接口可以正常收到图片。

经过代码追踪定位到 bug 位于 OpenAI Chat Completions 请求体的
归一化环节，而非 IR/serialize 阶段。

## 根因

`domains/transformation/sanitizer.go::dedupConsecutive`（在
`MergeConsecutiveMessages` 内被调用、`prepareRequestBody` 内
无条件触发）做了两段类型断言：

```go
prevContent, _ := result[len(result)-1]["content"].(string)
curContent, _ := msgs[i]["content"].(string)
result[len(result)-1]["content"] = prevContent + "\n" + curContent
```

OpenAI 多模态消息的 `content` 字段是数组形式
（`[{"type":"text",...},{"type":"image_url",...}]`），类型断言失败，
`ok` 被丢弃，于是左右两侧都被当作 `""`，合并后只剩 `"\n"`，**所有
`image_url` / `image` / `input_audio` / `file` 块全部静默丢失**。

触发条件（任一）：

1. 客户端连续发送多个 `user` 或 `assistant` message，且至少一个
   `content` 是数组形式（多张图片、同会话多次传图、图片+文本混合
   等场景）
2. 消息体内出现 `role=user` + 数组 `content` 的组合

直连 minimax-m3 接口不经过 `domains/transformation/` 链路，保留了
OpenAI 原始 wire format，因此可见图。

## 修复

把 `dedupConsecutive` 改为"形状感知"的合并器：

- **string + string**：`"a\nb"`（与原行为一致，不回归）
- **array + array**  ：拼接两个内容数组
- **array + string** ：在数组尾部追加 `{"type":"text","text":s}`
- **string + array** ：在数组头部插入 `{"type":"text","text":s}`

任何非 string / `[]any` 的 content（例如 `tool_calls` 旁挂字段）
直接拒绝合并，保留下游不变量。

新增 helper `mergeContents` 和 `toContentParts` 做形状归一化。

## 测试

新增 `domains/transformation/sanitizer_merge_multimodal_test.go`
5 个 case：

| 场景 | 期望 |
|---|---|
| 两连续 user，第一条带 image_url | 合并后仍含 image_url |
| 单条 user 只有 image_url | 完全不变（无合并必要） |
| user/assistant/user 交替 | 不合并 |
| 两连续 user 全文本 | `"a\nb"`（回归保护） |
| array(text+image) + string | 合并后仍是 array 含 image |

修复前 5 个用例中 2 个失败：

- `TestMergeConsecutiveMessages_PreservesImageURL` — 合并后
  `content` 是 `"再描述一下细节"`，image_url 块被丢弃
- `TestMergeConsecutiveMessages_TextIntoArrayDoesNotCoerceAwayImage`
  — 合并后 `content` 退化成字符串，image_url 块被丢弃

修复后全 PASS。

## 影响范围

- 受影响：所有 `cand.Protocol == "openai-completions"` 的 upstream
  （即网关默认分发路径），覆盖 minimax-m3、deepseek、其他任何走
  OpenAI 协议的上游。
- 不受影响：`anthropic-messages` 协议（`prepareAnthropicRequestBody`
  不调用 `MergeConsecutiveMessages`）。
- 性能：新增一次类型 switch + 最多一次切片拼接，开销可忽略。

## 验证

- `go test ./domains/transformation/...` PASS
- `go test ./domains/streaming/...` PASS
- `go test ./internal/...` PASS
- `go test ./cmd/gateway-v2/...` PASS
- `go build ./...` clean
- `go vet ./domains/transformation/... ./domains/streaming/...` clean
- `golangci-lint run ./domains/transformation/...` 0 issues
