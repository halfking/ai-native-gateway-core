# D17 — 代码卫生

> 域知识库：[docs/audit/playbook/domains/D17-code-hygiene.md](../../../docs/audit/playbook/domains/D17-code-hygiene.md)
> R73 改动面：stream reader context、lite telemetry 并发实现、rate-limit scope、Ollama recovery 与 audit 文档漂移。
> 状态：R73 改动已提交并推送；D01–D17 全域覆盖、旧 MASTER_REPORT 引用复核、全仓 race 与本地部署仍未完成。

## 1. 审计要点

- 逐文件区分前序 lite telemetry 与本轮 F03/F04/F06 归属；不删除近似实现，先核消费者和行为差异。
- 已做最终 `go build ./...`、`go vet ./...`、定向测试；R73 代码和文档见提交 `b1167076e`；另有两项未暂存工作区修改不属于此提交。
- 未覆盖：旧 MASTER_REPORT 引用全量清理、全仓 race 失败复核、部署产物与 D01–D17 完整审计。

## 2. 业务测试

- [x] B-01：新增实现均有对应定向回归测试或已有共享契约测试

## 3. 数据测试

- [x] D-01：migration 754 与 installer/embed 登记引用核对

## 4. 压力测试

- [ ] S-01：全仓 race（待执行）

## 5. 安全测试

- [x] SF-01：`git diff --check` 通过；未发现新增凭据值或内部 scope 转发

## 6. 验收门

```bash
go build ./...
go vet ./...
# R78：原门 `./tests/48h-audit/D17-code-hygiene/...` 匹配 0 个 Go 包——空包模式只打印
# `matched no packages` 警告并退出 0，门在结构上不可能失败。本域的证据本
# 来就在下面的包里（旧门只是没接到它），故门改指真实证据所在包。
代码卫生的可执行面：域内钉桩测试 + 归档 cadence 形状守卫（migration 754 登记引用）。B-01/SF-01 的 `git diff --check` 与凭据值静态核对属人工检查项，门只覆盖可自动化的部分。
go test -race -timeout 120s ./bg -run 'TestShouldRunRequestLogsArchive_|TestArchiveOldRequestLogs_' -count=1
```

## 7. 与方案文档的对齐

- RFC：docs/...
- R73：`docs/audit/runs/2026-09-28/R73-remediation-plan.md`；最终 diff/status 仍是收口门

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D17-code-hygiene/）。
知识库入口：docs/audit/playbook/domains/D17-code-hygiene.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
