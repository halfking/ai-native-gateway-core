# D05 — 自动压缩

> 域知识库：[docs/audit/playbook/domains/D05-auto-compression.md](../../../docs/audit/playbook/domains/D05-auto-compression.md)
> R73 改动面：Ollama candidate-aware pre-compression 与一次 provider overflow recovery。
> 状态：R73 F04 已实施；真实 Ollama/provider tokenizer 未验证。

## 1. 审计要点

- 预压缩必须保留原始入站 body；provider context-length 4xx 只允许一次内部压缩重试；取消和二次失败不得循环。
- 共享 `handleContextLengthRecovery` 负责机械/智能回退，Ollama 用 context marker 防递归。
- 未覆盖：真实供应商 tokenizer、LLM summary/Redis session cache。

## 2. 业务测试

- [x] B-01：Ollama httptest 首次 4xx、第二次 body 变小并成功

## 3. 数据测试

- [x] D-01：Ollama recovery 回归断言 `CompressionReason/Strategy/Meta`；命令见 R73 报告 §6

## 4. 压力测试

- [x] S-01：单次重试上限测试；全仓 race 待执行

## 5. 安全测试

- [ ] SF-01：未做错误日志捕获验证；httptest 仅断言响应和压缩遥测，不宣称日志安全测试通过

## 6. 验收门

```bash
go build ./...
go vet ./...
go test -race -timeout 60s ./domains/streaming/executors -run 'TestExecutor_ExecuteOllama_ContextLengthRecoveryRetriesOnceWithSmallerBody'
```

## 7. 与方案文档的对齐

- RFC：docs/...
- R73：`domains/streaming/executors/executor_ollama.go`；`executor_ollama_test.go`

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D05-auto-compression/）。
知识库入口：docs/audit/playbook/domains/D05-auto-compression.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
