# D02 — 协议适配

> 域知识库：[docs/audit/playbook/domains/D02-protocol-adaptation.md](../../../docs/audit/playbook/domains/D02-protocol-adaptation.md)  
> 48h 改动面（截至 R56）：Wave5 system array join / 命名 tool_choice 合法形态 / 空 text 块抑制（commit 8b498918f）  
> 状态：草稿

## 1. 审计要点（来自 playbook 域文档）

- IR 在上游 5+ LLM 厂商（openai / anthropic / responses / gemini / ollama / doubao / qwen / ...）和下游客户端协议（chat / message / response）之间双向无损
- 流式与非流式统一表达（chunk + 完成语义）
- 出厂参数（如 reasoning_effort、web_search、bot_setting）通过 Extensions 透传 + 缓存 TTL + 返回时回填
- 多轮对话轮次摘要 + 原始轮次关键文本抽取

## 2. 业务测试（business/）

- [ ] B-01：5 协议 × 多 message 角色（system / user / assistant / tool）矩阵 roundtrip —— `business/test_multi_role_roundtrip_test.go`
- [ ] B-02：出厂参数透传到 Extensions 并能从 Responses 还原 —— `business/test_extensions_passthrough_test.go`
- [ ] B-03：tool_choice 命名形态（"any"/"none"/"auto"/"required"/function name）合法化 —— `business/test_tool_choice_named_test.go`

## 3. 数据测试（data/）

- [ ] D-01：system message array join 到 Anthropic 单 string 字段 —— `data/test_system_array_join_test.go`
- [ ] D-02：空 text content block 不被错误地丢弃 —— `data/test_empty_text_block_test.go`
- [ ] D-03：Ollama 流式 chunk parse 正常 —— `data/test_ollama_stream_chunk_test.go`

## 4. 压力测试（stress/）

- [ ] S-01：4 协议并发混合 serialize 5000 req，P99 ≤10ms —— `stress/test_mixed_protocol_p99_test.go`

## 5. 安全测试（safety/）

- [ ] SF-01：tool_choice 含注入字符（`"; DROP --`）不 panic —— `safety/test_tool_choice_injection_test.go`
- [ ] SF-02：超长 system prompt（≥100k char）不 OOM —— `safety/test_oversized_system_test.go`

## 6. 验收门

```bash
go build ./...
go vet ./...
go test -race -timeout 120s ./tests/48h-audit/D02-protocol-adaptation/business/...
go test -race -timeout 120s ./tests/48h-audit/D02-protocol-adaptation/data/...
go test -race -timeout 240s ./tests/48h-audit/D02-protocol-adaptation/stress/...
go test -race -timeout 120s ./tests/48h-audit/D02-protocol-adaptation/safety/...
```

## 7. 与方案文档的对齐

- RFC：docs/format-conversion/（v1 落库）
- Wave5 缺口闭合：commit 8b498918f
- 上轮挂账：docs/audit/playbook/runs/R52-2026-09-22/agent-D02-clienttype.md