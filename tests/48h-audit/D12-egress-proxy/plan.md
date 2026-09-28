# D12 — 海外模型代理

> 域知识库：[docs/audit/playbook/domains/D12-egress-proxy.md](../../../docs/audit/playbook/domains/D12-egress-proxy.md)
> R73 改动面：provider candidate/failover、Ollama local endpoint 与出口 header 边界。
> 状态：静态复核；代理订阅/真实出口未验证。

## 1. 审计要点

- 核对健康探测、失效切换、地区优先级和供应商具体排除项；不得把本地 Ollama 与海外代理语义混同。
- F03/F04/F06 只影响请求上下文、压缩和错误分类，不新增代理凭据或绕过出口选择。
- 未覆盖：真实代理订阅、地区网络和供应商探测。

## 2. 业务测试

- [ ] B-01：**已复核（2026-09-28 R78），结论为缺陷而非通过**。完整调用链取证如下。
  - **输入**：`providers` 表某行 `egress_profile='proxy'`（真库现值：direct 46 / proxy 14）。
  - **设计期望**：`docs/03-design/proxy-management-design.md:153-157` 明写
    `direct` → 直连、`proxy` → **使用代理**、`auto` → 智能判断。
  - **应有输出**：派发到该 provider 的请求走 `proxy.TransportFactory.Get(subID, proxyURL)`
    产出的带代理 `http.Transport`。
  - **实际代码路径**：**不存在**。`proxy` 包全仓仅被 `admin/handler.go`、`admin/proxy.go`、
    `admin/proxy_test.go` 三个文件 import；`NewTransportFactory` 的调用点只有
    `proxy/manager.go:102`（包内）与测试；`TransportFactory.Get()` 无任何外部调用者；
    `providers.egress_profile` 的非 admin 引用仅 catalog 种子列、catalog schema 结构体字段
    与 `db/db.go` DDL，**无任何派发代码读它**。请求路径的健康探测还显式注释
    "Health probes must be direct"（`domains/health/http_checker.go:38`）。
  - **结论**：出口代理是**管理面 + 持久化**子系统，14 个标记 `proxy` 的 provider 在派发时
    实际是**直连**。这不是「缺测试」，是设计契约未接线。
  - **附带差异**：设计要求该列 `VARCHAR(20) DEFAULT 'auto'`，实现为
    `TEXT DEFAULT 'direct'`（`db/db.go:7539-7543`），且真库无任何 `'auto'` 取值——
    设计的第三种模式整体未实现。
  - **未修**：接线属路由/网络架构变更（需要决定代理失败时是回退直连还是报错、
    14 个存量 provider 是否要重新分类），不在审计轮内替 owner 定。已登记为开放缺陷。
- [x] SF-01 复核结论：成立。保留头 `X-LLM-Gateway-RateLimit-Scope`（`ratelimit/scope.go:11`）
  在三条上游路径均被过滤——`protocol_handler.go:33`、`executor_ollama.go:1198`、
  `executor_anthropic.go:1285`；API key 仅经 Authorization 发出。

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
# R78：原门 `./tests/48h-audit/D12-egress-proxy/...` 匹配 0 个 Go 包——空包模式只打印
# `matched no packages` 警告并退出 0，门在结构上不可能失败。本域的证据本
# 来就在下面的包里（旧门只是没接到它），故门改指真实证据所在包。
出口代理：证据在 ./proxy 的健康探测与负载均衡回归（域知识 §2 代码入口 proxy/）。
go test -race -timeout 120s ./proxy/... -count=1
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
