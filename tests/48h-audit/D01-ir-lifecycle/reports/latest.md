# R56 · D01 IR 生命周期 · 48h 审计结论

> 时间：2026-09-22  
> 改动面：1 commit（Wave5 fix 8b498918f：`internal/ir/parse_ollama_stream.go` 新增 + 多份 _test.go 调整）  
> 复核：主代理亲读 internal/ir/{class,serialize_openai,serialize_gemini,parse_openai,parse_anthropic,parse_responses,types}.go

## 1. 发现与处置

| 级别 | 描述 | commit | 钉桩测试 |
|---|---|---|---|
| — | 无 P0-P3 新发现 | — | — |

### 已确认的不变式

- ✓ IR 必填字段（Model / Messages / Stream / MaxTokens）在 4 协议下 serialize/parse 后守恒
- ✓ Gemini 协议不携带 model 到 body（URL-path convention，已在测试中标注）
- ✓ OpenAI `stream: false` 走 `omitempty` 省略，属预期（默认即 false）
- ✓ InternalRequest 内部字段（request_class / transport_ctx / internal_id 等）未泄漏到任何协议的对外 body
- ✓ 4 协议 parser 在空 / 半截 / null / 类型错 4 类畸形输入下不 panic
- ✓ 50 并发 × 5000 req 下 OpenAI serialize p99=580µs（mock loopback，远低于 10ms 阈值）

## 2. 核实为健康的关键面

| 项 | 验证手段 | 结果 |
|---|---|---|
| 4 协议 roundtrip | business/ir_field_roundtrip_test.go | 4/4 PASS |
| 字段不漂移 | data/ir_field_golden_test.go | 5/5 PASS |
| 畸形不 panic | safety/ir_malformed_test.go | 18/18 PASS |
| 并发 P99 | stress/serialize_p99_test.go | 1/1 PASS |

## 3. 遗留登记

- [ ] (本轮无遗留；下轮关注 48h 内是否有 IR Extensions 落库 schema 变更)

## 4. 测试与验证

```
$ go test -race -timeout 60s ./tests/48h-audit/D01-ir-lifecycle/business/...
ok  	.../business	1.451s

$ go test -race -timeout 60s ./tests/48h-audit/D01-ir-lifecycle/data/...
ok  	.../data	1.439s

$ go test -race -timeout 60s ./tests/48h-audit/D01-ir-lifecycle/safety/...
ok  	.../safety	1.441s

$ go test -race -timeout 240s ./tests/48h-audit/D01-ir-lifecycle/stress/...
ok  	.../stress	1.262s
```

## 5. 下一轮提示

- 关注 Wave5 的 3 个 GAP 修复（system array join / tool_choice 命名 / 空 text 抑制）是否已延伸覆盖到 Responses / Ollama 协议
- InternalRequest 新增字段（如 R55+ 的 ExtensionStore、CompressionMeta）需做同步的 golden 校验