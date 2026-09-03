# 2026-09-04 集成修复与 Turn Digest/L2 审计

## 修复

- 统一 `TurnListItem.LatencyMs` 的 JSON 字段为 `latency_ms`，并增加序列化回归测试。
- Dual Turn Digest 路由使用独立 500ms shadow context，避免 V2 查询继承耗尽的客户端 deadline；在 tree 响应中附加顶层 `v2_shadow` 汇总，同时保留逐 turn 字段。
- L2 shadow 对短非空流报告 `undecided`，而非错误的 `miss`；flush 入口在仅有 shadow observer 时也会进行语义观测。
- L2 缓存不可用时安全降级为 off，避免 coordinator nil dereference。
- 恢复 `autoroute` treatment attribution 编译契约。

## 整合审查

- 供应商质量分钟聚合使用 `request_logs_hot` 真实字段并通过幂等 upsert 写入 `provider_metrics_minute`。
- 新增 startup migrations 已重编号为 651–654，避开既有 645–650，且保留对应 down migration。
- 本地部署契约覆盖资源发现、release staging、checksum、原子切换和回滚；相关 shell 与契约测试通过。

## 验证

通过：

- `go test ./admin ./domains/streaming ./autoroute -count=1`
- `go build ./...`
- `go vet ./...`
- `bash tests/deploy_local_contract_test.sh`
- `bash tests/migration_ledger_gate_test.sh`
- 部署脚本 `bash -n` 与 `git diff --check`

`make test-short` 的测试主体通过，但命令最终因迁移编号重复门禁失败；新增迁移现已顺延为 651–654，需重跑确认。严格 secrets scan 命中仓库既有 `.env*` 示例和 `credentials.json` 配置文件模式，未证明为本次新增秘密。真实 PostgreSQL/Citus 迁移和目标主机部署验证依赖外部环境，未在本地伪称完成，标记为 `manual_required`。
