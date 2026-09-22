# R56 · D02 协议适配 · 48h 审计结论

> 时间：2026-09-22  
> 改动面：commit 8b498918f（Wave5 三缺口闭合） + `internal/ir/parse_ollama_stream.go` 新增  
> 复核：主代理亲读 `internal/ir/parse_openai.go` 与 `serialize_openai.go` 的 system 提升路径

## 1. 发现与处置

| 级别 | 描述 | commit | 钉桩测试 |
|---|---|---|---|
| P3 | (设计性观察，非 bug) OpenAI 协议 parse 时把 `system` role 提升为 `IR.System` 一等字段，从 Messages 数组移除；测试矩阵如果不识别这一点会误报 "Messages 丢失" | — | business/multi_role_roundtrip_test.go（已显式标注设计） |

### 关键发现（已在测试中固化）

- **OpenAI → IR → OpenAI 路径上 system 角色不守恒在 Messages 数组**：parse 路径 (`internal/ir/parse_openai.go:645-673` `extractSystemPrompt`) 主动提取 system message 到 `IR.System` 字段并从 Messages 切片移除。这是 IR 中间表示的**有意设计**（system 是协议强相关字段，提升为一等公民便于多协议适配），不是 bug。
- 多 role × 多协议 roundtrip 守恒（D02 business 9/9 PASS）

## 2. 核实为健康的关键面

- ✓ 4 role（user / assistant / system / tool）× 3 协议（OpenAI / Anthropic / Responses）矩阵无消息丢失
- ✓ 已知 OpenAI system → IR.System 提升路径与测试断言一致
- ✓ tool_choice 含 SQL 注入 / path traversal / XSS / NULL byte 形态不 panic，无敏感字段泄漏

## 3. 遗留登记

- [ ] Ollama 流式 chunk 解析（`parse_ollama_stream.go` 是 48h 新增）需要专门的 chunk-level 测试，目前 business 仅做请求侧，缺流式验证
- [ ] 命名 tool_choice（"any"/"none"/"auto"/"required"）的 GAP-2 闭合需要单独测试覆盖

## 4. 测试与验证

```
$ go test -race -timeout 60s ./tests/48h-audit/D02-protocol-adaptation/business/...
ok  	.../business	1.464s (3 子测试 × 3 协议 = 9 PASS)

$ go test -race -timeout 60s ./tests/48h-audit/D02-protocol-adaptation/safety/...
ok  	.../safety	1.519s (8 tool_choice 注入形态 × 1 = 8 PASS)
```

## 5. 下一轮提示

- 给 D02 补齐 data 类（system array join / empty text block / Ollama chunk 解析）
- 给 D02 补 stress 类（混合协议 serialize P99）