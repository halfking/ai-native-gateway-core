# 会话审计整改状态（2026-08-14）

## 范围

本轮审计覆盖：

- `domains/dispatch/state_transition_logger.go`
- `domains/streaming/handler.go`
- `internal/streamretry/wrapper.go`
- `docs/全方面测试/scenarios/run_all.sh`
- `docs/全方面测试/scenarios/_lib.sh`
- S18-S23 严格结果脚本
- `docs/全方面测试/tools/result_contract.py`
- `docs/全方面测试/tools/validation_report.py`
- `docs/全方面测试/run-fast.sh`

`deploy-quickstart.sh` 的删除是既有独立工作区改动，未纳入本轮提交。

## 已修复

1. 恢复 `StateTransition` 的独立字段声明，避免 malformed struct 导致全仓无法编译。
2. 路由状态转移日志补传 `tenantID`。
3. retry 状态日志保留空租户，避免在认证前信任客户端 `X-Tenant-ID`；可信租户 context 尚未注入该外层 wrapper。
4. 结果 contract 校验 `metrics.p99_ms` 的类型、有限性和非负性；对不适用延迟测量的场景使用 `p99_required=false`。
5. `run_all.sh` 增加缺值参数、`--gateway=...`、`--help` 处理。
6. preflight 或 mock preflight 失败时生成 `TEST_PREFLIGHT` 的 `BLOCKED_ENVIRONMENT` envelope 和报告。
7. `--allow-skipped` 不再放行 `BLOCKED_ENVIRONMENT`。
8. S19/S20/S22/S23 不再让 WARN、false check、历史 `/tmp` 日志或硬编码 true 形成 PASS；S21 使用严格 SKIPPED envelope。
9. 旧 `run-fast.sh` 改为严格 runner 的兼容 wrapper，避免旧端口、共享结果目录和吞错退出码。
10. 添加 `docs/全方面测试/tools/test_result_contract.py` 离线回归测试，并在 README 中记录 CI 环境契约与 artifact 要求。

## 验证

已通过：

- `go test -count=1 ./internal/sqlguard -run TestNoGoCommentsInSQLLiterals -v`
- `go test -race -count=1 ./domains/dispatch/... ./domains/streaming/... ./internal/streamretry/...`
- `go vet ./domains/dispatch/... ./domains/streaming/... ./internal/streamretry/...`
- `python3 -m unittest docs/全方面测试/tools/test_result_contract.py -v`
- `bash -n` 覆盖 runner、helper、S18-S23 和 `run-fast.sh`
- `run_all.sh --help` 与缺少 `--gateway` 参数返回码 `2`
- Gateway 不可达时 `run_all.sh` 返回 `1`，并生成 `TEST_PREFLIGHT.json`、`manifest.json` 和 `REPORT.md`，状态为 `BLOCKED_ENVIRONMENT`

## 未完成 / 环境阻断

- 当前环境未提供可验证的 CI Gateway 启动方式、测试数据库 secret 命名、self-hosted labels 或 60 mock supplier 运行契约，因此没有新增猜测性的 GitHub Actions workflow。
- 未在当前环境运行完整 `run_all.sh --suite functional --fast`：需要可用 Gateway、PostgreSQL schema/seed 和 60 个 mock supplier。
- S21 分支会话能力仍未实现，严格结果为 `SKIPPED`。
- retry state-transition telemetry 仍缺可信租户：`DefaultStreamExecutor` 在 ChatHandler 认证之前运行，后续应由认证层将可信 tenant context 注入 wrapper，而不是读取客户端 `X-Tenant-ID`。
- S27 resume token、S29 force-G quota、S26 精确 sticky credential/attempt 证据和 245/154 部署验证仍是后续能力边界。
- 历史报告中的 S24-S29 p99 数值未重新生成；本轮不把历史报告当作当前 run 证据。

## 工作区归属

- 本轮目标改动：上述 Go、runner、contract、场景、测试和 README 文件。
- 独立既有改动：`deploy-quickstart.sh` 删除，保持未提交。
