# D07 — 热+分区存储

> 域知识库：[docs/audit/playbook/domains/D07-hot-columnar.md](../../../docs/audit/playbook/domains/D07-hot-columnar.md)
> R73 改动面：request_logs archive cadence 与 migration 754 调用边界。
> 状态：R73 F05 已定向通过；F07 已完成源码/静态契约核验，兼容 PG 未连接。

## 1. 审计要点

- 核对 hot 保留、月分区扫描、归档表 RLS/租户字段、幂等与定时器相位。
- `archiveOldRequestLogs` 使用 30m Go context；migration 754 为单长事务、分批 INSERT、无 executable DELETE/COMMIT。
- 未覆盖：真实 role timeout、历史数据量与执行计划。

## 2. 业务测试

- [x] B-01：archive cadence 多启动相位定向测试通过

## 3. 数据测试

- [x] D-01：migration 754 function/installer/embed/caller 形状核对

## 4. 压力测试

- [ ] S-01：兼容 PG EXPLAIN/大分区实测（环境未提供）

## 5. 安全测试

- [x] SF-01：源分区直查、独立归档表、无删除的静态守卫通过

## 6. 验收门

```bash
go build ./...
go vet ./...
# R78：原门 `./tests/48h-audit/D07-hot-columnar/...` 匹配 0 个 Go 包——空包模式只打印
# `matched no packages` 警告并退出 0，门在结构上不可能失败。本域的证据本
# 来就在下面的包里（旧门只是没接到它），故门改指真实证据所在包。
hot/columnar 归档：证据在 ./bg 的 request_logs 归档 cadence 五条（每日一次闸门、分钟不敏感、闸门真被调用、statement_timeout 钉在事务内、由小时清理循环驱动）——即 migration 754 的落地路径。
go test -race -timeout 120s ./bg -run 'TestShouldRunRequestLogsArchive_|TestArchiveOldRequestLogs_' -count=1
```

## 7. 与方案文档的对齐

- RFC：docs/...
- R73：`bg/partition_manager.go`；`installer/cmd/llm-gw-installer/embeddata/startup/754_archive_request_logs_default.sql`

## 子代理派发提示词

```
你是 worker 子代理（带写权限到 tests/48h-audit/D07-hot-columnar/）。
知识库入口：docs/audit/playbook/domains/D07-hot-columnar.md
模板：tests/48h-audit/TEMPLATE-domain.md
必填：
  - 改 plan.md §1-§7
  - 在 business/data/stress/safety 至少各写 1 个 _test.go
  - reports/latest.md 留档
输出 ≤5KB。
```
