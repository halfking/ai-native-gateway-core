# D12 — 海外模型代理

> 域知识库：[docs/audit/playbook/domains/D12-egress-proxy.md](../../../docs/audit/playbook/domains/D12-egress-proxy.md)
> R73 改动面：provider candidate/failover、Ollama local endpoint 与出口 header 边界。
> 状态：静态复核；代理订阅/真实出口未验证。

## 1. 审计要点

- 核对健康探测、失效切换、地区优先级和供应商具体排除项；不得把本地 Ollama 与海外代理语义混同。
- F03/F04/F06 只影响请求上下文、压缩和错误分类，不新增代理凭据或绕过出口选择。
- 未覆盖：真实代理订阅、地区网络和供应商探测。

## 2. 业务测试

- [ ] B-01：本轮未复核完整候选 URL/协议调用链；需记录具体输入、选择输出和对应代码路径后再验收

## 3. 数据测试

- [ ] D-01：真实出口健康探测（未验证）

## 4. 压力测试

- [ ] S-01：地区切换压力（未验证）

## 5. 安全测试

- [x] SF-01：API key 仅进入 Authorization，scope 标记不转发

## 6. 验收门

```bash
go build ./...
go vet ./...
go test -race -timeout 60s ./tests/48h-audit/D12-egress-proxy/...
```

## 7. 与方案文档的对齐

- RFC：docs/...
- R73：`domains/streaming/executors/executor_ollama.go`；`ratelimit/scope.go`

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D12-egress-proxy/）。
知识库入口：docs/audit/playbook/domains/D12-egress-proxy.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
