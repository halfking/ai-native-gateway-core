# 环境验收、数据闭环与发布硬化阶段 Handoff

> 日期：2026-08-30
> 仓库：`services/llm-gateway-go`
> 分支：`main`
> HEAD：`e10c8425e52045fdecc1426462d150ccfe44cbc2`
> 结论：**NO-GO for release / GO_WITH_LIMITATIONS for local code baseline**

## 1. 执行范围

本轮完成：

- 必读环境、部署、审计和下一阶段提示词文档读取。
- 开工前 git 状态、HEAD、分支、reflog、`origin/main` fetch。
- 协作者 WIP、stash、worktree、未跟踪文件和已修改文件清单冻结。
- Wave 0 只读核对：Session V2、环境部署矩阵、tool-call 链路、指标/告警、双端口发布边界。
- 本地 Go 构建、vet、受影响包测试、race 测试、shell 语法、K8s YAML、migration/Prometheus Go 契约测试。
- 前端 `vue-tsc` 和完整 `npm test`。

本轮未执行：

- 真实 PostgreSQL migration、backfill、RLS、10→100 会话对账。
- 真实 Redis、Prometheus scrape/alert firing、provider/SDK live traffic。
- 245/154 canary、SSE drain 演练、rollback drill、生产部署。
- commit、push、merge、reset、restore、stash、clean、真实迁移或远程重启。

## 2. 协作者 WIP 与 ownership

开工时主工作区存在以下协作者内容，均视为禁止覆盖范围：

- 已修改文件：`.github/workflows/sessionforensics-ci.yml`、`Makefile`、`VERSION`、`admin/providers.go`、`admin/stats.go`、`bg/partition_manager.go`、`bg/provider_error_aggregator.go`、`bg/provider_error_aggregator_contract_test.go`、`docs/06-deployment/04-runbooks/journal-provider-next-phase.md`、`docs/db-changelog.md`、`domains/hooks/observability/telemetry/client.go`、`domains/hooks/observability/telemetry/client_test.go`、`version.json`、`web/public/menu-config.json`、`web/public/version.json`。
- 未跟踪文件：`admin/providers_schema_contract_test.go`、`admin/stats_schema_contract_test.go`、`bg/provider_error_aggregator_integration_test.go`、`domains/hooks/observability/telemetry/body_upsert_schema_contract_test.go`、`domains/requestjourney/journal_snapshot_receipt_integration_test.go`。
- stash：19 个，包含 blue-green、stream lifecycle、database/receipt、credential contract 等并行 WIP。
- 其他 worktree：包含 `agent/next-journal-observability-20260830`、`agent/next-session-v2-20260830`、`agent/next-stream-lifecycle-20260830`、`feat/journal-provider-nextphase`、`fix/audit-data-integrity` 等。

本轮未修改、未接管、未纳入提交上述协作者内容。

## 3. Wave 0 汇总

### W0-A：Session V2

状态：`blocked` / `manual_required` for staging。

已确认：

- `validate_sessions_v2` 当前采用两步读取：先按 tenant/session 读取 `request_logs` 元数据，再按 `request_id + ts` 从 `request_logs_bodies_hot` 与 `request_logs_bodies` 关联正文，避免历史大 JOIN。
- `backfill_session_v2_turns` 仍由 `sql/scripts/backfill_sessions_v2_v2.sql` 安装，包含 `ON CONFLICT (request_id, partition_date)`。
- migration 476 已将权威唯一约束收敛为 tenant-scoped：`(tenant_id, request_id, partition_date)` 和 `(tenant_id, session_id, turn_no, partition_date)`。
- staging 报告记录的 `session_turns` `ON CONFLICT` 失配和 `request_logs` 查询超时仍需真实目标库确认；本地静态核对不能替代 staging 证据。
- 10/100 会话回填、双读、差异对账、前端抽样尚未取得真实依赖证据。

### W0-B：环境/部署矩阵

状态：`IMPLEMENTED` / `LOCAL_VERIFIED` for code and docs; environments remain `PARTIAL` or `NOT_RELEASE_READY`。

已确认：

- `/healthz` 是 liveness；`/readyz` 是严格 DB+Redis readiness；`/version` 是 release identity。
- `deploy/k8s/llm-gateway-go-deployment.yaml` 绑定 `pms-test`，镜像使用固定 tag，但不是 production manifest。
- 客户 Windows host/Docker 仍为 `NOT_RELEASE_READY`。
- 当前版本元数据在 `VERSION`、`version.json`、`web/public/version.json` 已同步到 `e10c8425` / build sequence 1818；这些是现有 WIP，未由本轮修改。

### W0-C：tool-call 链路

状态：`IMPLEMENTED` / `LOCAL_VERIFIED`。

已确认：

- 已存在 Anthropic tool-call validator、malformed SSE 处理和 `incomplete_tool_call` 原因。
- 已有 interrupted/after-done 语义与测试 fixture。
- 245/154 的真实 tool-use 开始率、tool-result 完成率和 incomplete rate 尚无真实基线；需要目标环境指标或脱敏日志。

### W0-D：指标/告警

状态：`IMPLEMENTED` / `LOCAL_VERIFIED`。

已确认：

- 已存在 `llm_gateway_incomplete_tool_call_total`、`llm_gateway_malformed_sse_frame_total`、`gateway_success_empty_response_total`、JournalSnapshot stored/applied/deduplicated 指标。
- 已存在 hot-table promote failure/contention/slow 告警和 Go 契约测试。
- `promtool` 本机不可用；Prometheus 实际加载、scrape、告警触发仍为 `manual_required`。
- `has_response_body` 与 success-empty-response 的双计数边界仍需运行时观察和指标命名冻结证据。

### W0-E：双端口发布边界

状态：`PARTIAL` / `NOT_RELEASE_READY`。

已确认：

- `scripts/deploy-seamless.sh` 已增加 strict readiness、release identity、失败回滚和 nginx 链路检查。
- 当前主流程仍是单端口 stop/start 或同端口切换；没有可证明的 candidate/active 双端口并行、Nginx `active-upstream` 原子切流、worker 与 traffic receiver 隔离和 SSE drain 实演证据。
- 因此不得宣称“2 秒零中断”或 blue-green 已完成。

## 4. 本地验证证据

通过：

```text
go build ./...
go vet ./...
go test -count=1 ./admin ./bg ./domains/requestjourney ./domains/dispatch ./domains/streaming ./domains/streaming/executors ./domains/hooks/observability/telemetry ./sql/migrations/startup
go test -race -count=1 ./admin ./bg ./domains/requestjourney ./domains/dispatch ./domains/streaming ./domains/streaming/executors ./domains/hooks/observability/telemetry
go test -count=1 ./sql/migrations/startup ./deploy/prometheus/rules
git diff --check
bash -n scripts/user/*.sh scripts/lifecycle/*.sh scripts/deploy-lib/*.sh scripts/deploy*.sh scripts/deploy/*.sh
Kubernetes YAML parse: 4 files parsed successfully
cd web && npx vue-tsc --noEmit
```

`cd web && npm test`：**失败**，94 个测试文件中 2 个 i18n 契约文件失败，3 个测试失败、654 个通过。当前明确缺口：

- `web/src/views/ProxyView.vue:304` 引用了不存在的 `common.never`。
- `nav.item.proxy` 未同步到 `ar-SA`、`de-DE`、`es-ES`、`fr-FR`、`ja-JP`、`zh-TW` locale。

该前端缺口未在本轮修复，以避免覆盖协作者 WIP。

Secret scan：未在报告中复述任何 secret。扫描发现仓库历史/文档中存在应由后续安全清理流程处理的疑似硬编码凭据/私钥样本；它们不属于本轮可安全删除的协作者文件，必须单独由授权 owner 处理。

## 5. 证据等级

| 能力 | 等级 | 说明 |
| --- | --- | --- |
| Go 代码构建与静态检查 | `LOCAL_VERIFIED` | build、vet、受影响测试和 race 通过 |
| Session V2 schema/backfill/validate | `IMPLEMENTED` / `blocked` | 工具和 migration 存在；staging blocker 未闭环 |
| Tool-call completeness | `IMPLEMENTED` / `LOCAL_VERIFIED` | validator、原因和本地 fixture 存在；无真实基线 |
| 指标与告警资产 | `IMPLEMENTED` / `LOCAL_VERIFIED` | Go 契约通过；无 promtool/runtime evidence |
| 客户/中心部署脚本 | `IMPLEMENTED` / `PARTIAL` | readiness/rollback 收敛；真实环境未验收 |
| 双端口 blue-green | `NOT_RELEASE_READY` | 代码和真实演练证据不足 |
| 245/154 canary | `manual_required` | 无授权窗口、备份、回滚和真实依赖证据 |
| 整体 release | **`NO-GO`** | 前端测试失败且真实依赖/回滚证据缺失 |

## 6. 当前阻塞

1. Session V2 staging：`ON CONFLICT` 与实际 unique constraint 的契约需要在目标 schema 上确认并修复；request body 查询性能需要索引/分页/timeout 证据。
2. 前端 i18n：补齐 `common.never` 与所有 locale 的 `nav.item.proxy` 后重新运行 `npm test`。
3. 真实依赖：需要授权的 TEST_DATABASE_URL/隔离 PG、Redis、Prometheus、provider、浏览器或集群，以及备份和 rollback path；当前必须返回 `manual_required`。
4. 双端口发布：需要 candidate/active 端口、Nginx 原子 upstream 切换、worker ownership、SSE drain/cancel/retry 和 rollback drill。
5. Secret hygiene：仓库中疑似凭据/私钥样本需由明确 owner 单独清理，不在本轮覆盖协作者 WIP。

## 7. 下一阶段提示词

```text
在不覆盖现有协作者 WIP 的前提下，先修复 web/src/views/ProxyView.vue:304 的 common.never，并为所有 locale 补齐 nav.item.proxy，运行 cd web && npm test。
随后由 Session V2 owner 在独立 worktree 修复 backfill SQL 的 ON CONFLICT 与实际唯一约束契约，增加 request_logs_bodies hot/partition 的 request_id+ts 关联、分页和 statement timeout 保护，并运行受影响 Go 测试。
真实环境任务保持 manual_required：只有在提供隔离目标、授权窗口、备份、脱敏和 binary/data rollback 路径后，才执行 migration 兼容性检查、10→100 会话回填对账、Redis/Prometheus/provider 验证和 245→154 canary。
在 Wave 4 证据通过前，禁止 RELEASE_READY、生产已验收和“2 秒零中断”表述。
```

## 8. 最终决策

- **代码层：GO_WITH_LIMITATIONS**。本地 Go build/vet/test/race、shell 和 SQL/alert 契约通过。
- **发布层：NO-GO**。前端完整测试失败，真实 PG/Redis/Prometheus/provider/browser/canary 未验证，双端口零中断能力未实现或未证明。
- **提交策略：本轮不 commit/push/merge**。主工作区仍包含协作者 WIP；任何后续提交必须由 owner 在独立 worktree 先完成并由协调流程显式审查。
