# 02 网关错误策略 — 结果

日期：2026-09-08

| 层 | 命令 | 结果 |
|---|---|---|
| L1 errorsx | `go test ./errorsx -count=1` | pass |
| L1 requestflow | `go test ./internal/requestflow -count=1` | pass |
| L1 vendorstrip | `go test ./internal/vendorstrip -count=1` | pass |
| L1 streaming | `go test ./domains/streaming -count=1` | pass (72s) |
| L1 executors | `go test ./domains/streaming/executors -count=1` | pass (23s) |

P0/P1 用例 C01–C11 均由上述单测覆盖。本地 `~/kaixuan` 服务未更新。

## 154 部署

- 脚本：`bash scripts/deploy-154.sh --no-frontend`
- 结果：`2054-57cbfb81`，active_port=8782，handoff 完成
- 外网：`https://llm.kxpms.cn/healthz` → `status=ok` `build_seq=2054` `git_sha=57cbfb81`
