---
audit:
  status: passed
  degraded: false
  base: origin/main
  scope: "2026-07-26 12:00 through 2026-07-27 12:00 +08:00; 128 commits observed in the rolling 24-hour history"
---

# 24h 修改审计：请求链路、路由状态与可观测性

## 需求

- 总结并交叉审查最近 24 小时的修改。
- 修正发现的逻辑、语法、并发、迁移和测试问题。
- 完成当前远端已提供但本地尚未同步的 request-flow Phase-2 剩余事项后，再提交并推送。

## 原始修改摘要

- 请求链路：arrival WAL、终态保护、请求体/遥测字段、流式断开和 keepalive 生命周期。
- 路由状态：URSM v2 Redis/LRU、credential state 并发更新、探测/恢复、quota 路由门禁。
- 协议转换：provider-specific tools、streaming tool args 修复与审计标记。
- 管理面与部署：client-perception view、迁移 458-461、健康等待超时、版本/发布文档。
- 本轮窗口共观察到 128 个提交；审计基线最终同步至 `f87d29aa` / `origin/main`。

## 双轴审查

### Standards

- Go 代码执行 `gofmt`，导出 API 和错误处理保持仓库现有风格。
- 复查并发访问、终态幂等、SQL migration 配对、schema/view SSOT 和测试契约。
- `go vet ./...` 通过；受影响模块及本轮新增回归测试通过。

### Spec

- WAL arrival 写入必须与后续 enrichment 合并到同一请求行，不能生成悬挂 pending 行。
- 终态只能按明确的成功完成契约推进，失败不能被迟到成功更新逆转。
- 探测结果必须保留稀疏状态中的真实错误/时间字段；成功 probe 清零失败计数，且不能让时间戳倒退。
- 流计数读取必须只走 atomic accessor，避免普通镜像字段的竞态。
- `periodic_exhausted` 必须在迁移、view SSOT、schema baseline 和 installer embeddata 中统一排除。
- migration 459 必须覆盖 admin 实际查询的 `virtual_client_id`，并有可执行 down migration。

## 本轮修正

- `credentialstate.UpdateFromProbe`：按 probe 有值优先、旧状态仅作缺省的规则合并；成功 probe 清零失败计数/错误，保留健康字段，采用较新的更新时间。
- `CredentialProbeV2`：`availability_state=ready` 时显式设置 `LastSuccessAt`，修复成功 probe 被误当成稀疏失败更新。
- `RequestLogger.Update`：成功/失败终态改为同步持久化，避免异步队列满时静默丢失终态。
- `request_logs_hot` terminal guard：阻止 failure→success 及终态回退，只有明确的 success/success 更新可继续 enrichment。
- 流式计数生产读取改用 atomic accessor；测试改用 `SetStreamChunkCounters`，不再直接依赖可竞态的 legacy 镜像字段。
- migration 459 补充 `virtual_client_id` 的表字段、view 重建、smoke 检查和 down migration。
- `v_routable_credential_models`、完整 schema baseline 和 installer embeddata 统一排除 `periodic_exhausted`。

## 验证结果

- 通过：`gofmt`、`git diff --check`、`bash -n scripts/deploy-seamless.sh scripts/deploy-lib/db-changelog.sh`。
- 通过：`go vet ./...`。
- 通过：`go test ./...`。
- 通过：`go test -race ./domains/credentialstate ./domains/streaming`。
- 通过：受影响模块回归（credentialstate、streaming、telemetry、bg、IR、Anthropic transform、admin）。
- 未完全通过：`go test ./...` 的仓库基线/外部依赖失败：`domains/health/TestTCPChecker_RealWorldScenario/Cloudflare_DNS` 网络超时，以及 `domains/routing/TestWeightedRouter_DynamicAdjustment_ErrorSpike` 既有权重断言失败；均不在本轮修改文件范围。
- 阻断但非本轮新增：`scripts/scan-secrets.sh --mode=strict --paths=.` 命中仓库已有 `.env*`、`deploy/phase0/optimization.env` 及 worktree 示例文件；未发现本轮差异新增秘密。
- 未执行：真实 PostgreSQL 上的 459 down/up、460/461 迁移运行和部署主机验证；当前环境未提供目标数据库/部署授权。

## 剩余风险

- credentialstate 的 Redis 异步快照写入仍可能在极端调度下乱序覆盖；应在后续改为每 key 单调版本/CAS 或串行写队列。
- URSM v2 的 tenant-aware key、Sticky/Intent TTL 语义和部署脚本全局 deadline 仍需独立集成审计。
- 版本文件仍保持发布构建的 `cdb4cf31` pin；当前提交不擅自重写 release metadata。

## 提交

- 修复提交：`0ffe1593`（rebase 后哈希；原始本地提交为 `5c40c649`）。
- 推送目标：`origin/main`，已成功推送。
