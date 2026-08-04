# 一周更新与审计报告

**时间范围**：2026-07-28 11:28 至 2026-08-04 14:16（以基线 `8d5f7d16` 至 `f1c5fdfe`）
**报告生成**：2026-08-04
**审计分支**：`main`（已执行 `git pull --ff-only`，当前与 `origin/main` 对齐）

## 1. 范围与规模

- 提交：143 个（其中最近 7 个自然日的非 merge 提交 127 个）
- 文件：3,516 个
- 变更：新增 131,523 行，删除 14,033 行
- 主要变更来源：流式链路、IR 转换、会话/状态一致性、完整性监控、部署运维和数据库对象脚本

本报告关注行为变化和发布风险，不把批量生成的数据库对象脚本逐个重复列出。审计依据包括提交记录、范围 diff、相关实现及测试、`CONTRIBUTING.md` 和 URSM v2 cutover runbook。

## 2. 变更总结

### 流式请求与协议转换

- 修复 agent 多步任务经网关时的流式中断，并统一补写 `X-Request-Id`。
- 全协议启用 pre-stream keepalive，覆盖 OpenAI Completions、Anthropic Messages 和 Responses 等长思考场景。
- 完善增量流式完整性检测，覆盖 bridge、harvester、探针结果回收和模型完整性前端监控。
- 新增 OpenAI Responses 输入解析/请求序列化，补齐 Chat、Messages、Responses、Gemini 的 fixture 与集成验证。
- 完善工具参数 JSON 分片拼装、压缩 stage 翻译和 response history 处理。

### 会话与权威状态

- 加固 Session V2 的单 owner、request-level 幂等、turn/body 同事务和 aggregate lifecycle。
- 加固 URSM v2 authoritative 状态转换、并发 CAS、Redis 故障恢复 gate 和 shadow double-write 可观测性。
- 补齐 routing state source、异常分类、审计上下文和流式终态收敛。

### Redis、数据库与完整性

- 网关默认 Redis DB 从 0 调整为 2，session、live-stream、RPM limiter 和 availability cache 统一使用配置 DB，减少共享实例跨系统扫描互相影响。
- 增加 PG/Redis 故障注入、迁移工具、完整性 harvester/probe sink/planner 测试。
- 批量生成数据库约束、对象和相关脚本，扩大了 schema 可复现范围。

### 部署与运维

- 154 stderr 接入 journald drop-in 限额及独立回滚脚本；154/245 增加 stdout/stderr logrotate 和预生产磁盘清理。
- Dockerfile 镜像源参数化，增加发布构建和安装 logrotate 管理脚本。
- 一键部署、本地三路环境和 customer-instance 模拟流程得到补充，部署脚本默认启用 IR 路径。

## 3. 审计发现与处置

### 已修复：本地配置中的明文 PostgreSQL 密码

`configs/env-local.sh` 在本次范围内被改为指向 `llm-gateway-pg`，同时把可用的 `PG_PASS` 写入跟踪文件。该密码可被 `scripts/sync-from-252.sh` source 并作为 `PGPASSWORD` 使用，属于凭证暴露。

本次修复将其改为：

```bash
PG_PASS="${PG_PASS_LOCAL:?PG_PASS_LOCAL not set — inject the local PostgreSQL password before sourcing}"
```

凭证通过本地 secret injector 或 shell 环境注入，不再写入仓库。该密码已经出现在历史提交和既有历史文档示例中，仍需运维按凭证泄露流程轮换，并评估仓库历史清理；本次提交不重写历史。

### 已修复：URSM v2 迁移工具 Redis DB 不一致

网关默认 DB 已为 2，但 `cmd/migrate-ursm-v2` 原先在未指定 `REDIS_URL` 时回退到 DB 0。直接执行迁移会把状态写入网关不读取的 DB，造成迁移成功但切换后状态为空。

本次修复统一了配置解析：

- 显式 `REDIS_URL` 优先；
- 否则读取 `LLM_GATEWAY_REDIS_ADDR`、`LLM_GATEWAY_REDIS_PASSWORD`、`LLM_GATEWAY_REDIS_DB`；
- 未配置 DB 时默认使用 2；
- URL 中的显式 DB 路径由 Redis URL 解析器保留；
- 同步更新迁移工具测试和 `docs/runbooks/ursm-v2-cutover.md`。

### 观察项：批量 SQL 文件的 EOF 空白

`git diff --check` 对批量生成的 `deploy/sql/objects/constraints/` 文件报告了大量 `new blank line at EOF`。这不影响 SQL 语义，但会增加 diff 噪声并违反仓库空白检查习惯。建议后续在生成器中统一 EOF 规范，再单独提交机械清理，避免与功能变更混合。

### 观察项：部署验证仍需环境门禁

本次部署脚本改动较多。根据 `CONTRIBUTING.md`，涉及 `deploy/`、`cmd/gateway/main.go` 和 CI 脚本的改动必须在测试环境执行 `make deploy-test`，并完成 pre/post checkpoint 和 184 k3s 四步门。代码审计不能替代这些环境验证；本报告仅记录仓库内实现与测试证据，不宣称生产部署已验证。

## 4. 验证记录

以下命令在提交前执行，结果以本节最终状态为准：

- `gofmt -w cmd/migrate-ursm-v2/main.go cmd/migrate-ursm-v2/main_test.go`：通过。
- `go test ./cmd/migrate-ursm-v2/...`：通过。
- `go test ./config/... ./domains/credential/...`：通过。
- `go test ./...`：未通过。与本次改动无关的现有工作区修改导致 `domains/hooks/compression/summary_client_test.go:49` 出现 `undefined: summarymodel`；另有 `autoupdate` 的 `upgrade_logs` 外键数据错误，以及 `domains/providerprofile` 测试用户无 `provider_profile_metrics` 表权限。
- `go vet ./...`：未通过，同样在 `domains/hooks/compression/summary_client_test.go:49` 因 `undefined: summarymodel` 停止。
- `./scripts/scan-secrets.sh --mode=strict`：未通过。扫描器把仓库中既有的 `.env`/`.env.example` 文件名按 `SECRET_FILE` 阻断；目标修复文件未检出明文密码。目标路径单独复核通过。
- `git diff --check`（排除批量生成的 SQL 约束目录）：通过；全范围检查仍报告批量 SQL 文件的 EOF 空白，见上文观察项。

全量测试和 vet 的失败已保留原始错误；未将外部数据库权限或其他工作区修改冒充为本次修复回归。

## 5. 后续动作

1. 立即轮换已经进入 Git 历史的本地 PostgreSQL 密码，并确认共享环境凭证未复用。
2. 生产执行 URSM v2 迁移前，显式确认 `LLM_GATEWAY_REDIS_DB=2` 或使用带 `/2` 的 `REDIS_URL`，再按 runbook 验证 Redis key。
3. 在数据库脚本生成器中修正 EOF 空白，并重新运行 SQL lint。
4. 在具备 71/184 测试环境后完成部署 checkpoint 和 `make deploy-test`；未经明确授权不执行生产部署。
