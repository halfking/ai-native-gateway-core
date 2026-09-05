# llm-gateway-go 全链路审计 → P0 改进实施归档（2026-07-29）

> **Session 收口文档**（rule 04 §2 + rule 08 §3.1）  
> 本文档归档 2026-07-29 一次完整的多任务实施会话，覆盖：  
> 1. P0-3 URSM v2 切流框架（依赖 4 项老板决策未拍板，本轮只交付"框架 + 测试"）  
> 2. P0-2 / P0-5 收尾（在前一会话已 commit，本轮仅补 CHANGELOG + 归档）  
> 3. 审计 / 设计 / local-deploy-test 文档归档（在前一会话由 `801689418` commit 完成）

## 1. Mission Summary

按 `docs/design/2026-07-28-llm-gateway-flow-improvements.md` §2 P0-3 推进 URSM v2 切流框架：

- `LLM_GATEWAY_URSM_V2_MODE` 模式（off / shadow / canary / authoritative）已存在（`URSM_V2_MODE` env）
- 4 项决策（切换时机 / sticky 保留层 / OTel 后端 / 数据迁移策略）未拍板，本轮不切流生产
- 交付：ShadowDoubleWrite 框架（路由不变 / 边车写 URSM v2 / metric 区分 recorded/skipped/failed）+ 一键迁移工具 + 一键回退脚本 + 4 阶段操作手册 + 影子路由锁死测试

按 rule 11 §10 完成结构化任务总结，对应 6 节。

## 2. Completed Work

### 2.1 Commits Pushed (this session)

| SHA | Subject | Files | Lines |
|---|---|---|---|
| `15d6faa0d` | `feat(ursm/v2): wire shadow-double-write metric + tests (P0-3)` | 3 | +125 |
| `aec64e562` | `test(streaming): pin shadow + canary routing backend unchanged (P0-3)` | 1 | +68 |
| `3b9b1dd8f` | `feat(migrate-ursm-v2): one-time PG→Redis migration tool (P0-3)` | 2 | +488 |
| `2177b9b04` | `feat(ursm-v2-cutover): runbook + rollback shell script (P0-3)` | 2 | +458 |
| 后续 rebase 哈希（origin/main HEAD） | `9ee5fc286` / `34532504a` / `58f9b9a39` / `62b60662a` | (同上 4 个) | (同上) |

### 2.2 Commits Previously Pushed (前一会话已落地，本轮覆盖)

| SHA | Subject | Files | Lines |
|---|---|---|---|
| `44e05a757` | `docs(operations): v2 Pipeline feature flag status confirmation (P0-5, R-5.2)` | 1 | +167 |
| `95755779c` | `feat(metrics): shadow write failure counters + alerting rules (P0-2, R-3.3/R-3.4)` | 8 | +294 -1 |
| `f9cbe7998` | `feat(recovery+systemmonitor): align M3 follow-up wiring + rollout config + admin recovery handlers` | 11 | +373 -13 |
| `801689418` | `chore: build metadata 1417 + migration 462 entry + audit/design docs + local-deploy-test script` | 8 | +2131 -10 |

### 2.3 Artifacts Produced

#### P0-3 框架（老板决策后可直接进入 Stage 1 → Stage 3）

**Code**（与已上线 `f9cbe7998` 互补，本轮新增 metric wiring + 测试）：

- `domains/ursm/v2/rollout/controller.go`：`Config.ShadowDoubleWrite` + `ShadowDoubleWrite()` 访问器；`ShouldUseV2` 在 ModeShadow 下仅当 ShadowDoubleWrite=true 返回 true
- `domains/ursm/v2/config.go`：`URSM_V2_SHADOW_DOUBLE_WRITE` env 读取（truthy 1/true/yes）
- `domains/ursm/v2/manager.go:RecordRequest`：3 处 metric（skipped / recorded / failed）
- `metrics/interface.go` + `metrics/prometheus.go`：`RecordURSMv2ShadowResult` 接口 + 实现 + `llm_gateway_ursm_v2_shadow_records_total{result}` counter

**Tests**（TDD 风格，固化 contract）：

- `domains/ursm/v2/config_test.go`（NEW，67 行）：11 个 env 解析 case + 默认 off contract
- `domains/ursm/v2/rollout/controller_test.go`（+48 行）：6 case table + 访问器 nil-safety
- `domains/streaming/executors/state_backend_test.go`（+68 行）：shadow / canary 路由 backend 不变测试
- `cmd/migrate-ursm-v2/main_test.go`（NEW，144 行）：6 个 mapRow 契约测试

**Tools**：

- `cmd/migrate-ursm-v2/main.go`（NEW，344 行）：PG node_probe_state → URSM v2 Redis hash 一键迁移；`--dry-run` 默认 / `--apply` 显式；6 类映射（healthy / in-cool / manual_hold-paused）+ tenant 过滤 + DSN 凭据脱敏
- `scripts/rollback/ursm_v2_to_legacy.sh`（NEW，203 行）：一键回退（auto-detect systemd / docker）；rule 03 §6.0 L1-L4 验证块；`--dry-run` / `--env=` whitelist

**Docs**：

- `docs/runbooks/ursm-v2-cutover.md`（NEW，254 行）：4 阶段（off → shadow 7d → canary 灰度 → authoritative）操作手册；每阶段含 env / L1-L4 / drift 计算 / 回退触发；引用 migrate + rollback 工具

#### P0-2 / P0-5（前一会话已落地）

- `docs/operations/v2-pipeline-status.md`（166 行）：v2 Pipeline flag 全环境状态
- `deploy/monitoring/grafana-alerts/shadow-write-failures.yaml`（85 行）：4 条告警
- 4 个 hook 处 metric 调用：`attachmentmirror` / `sessionv2mirror` / `ring_buffer` / `raw_data_logger`

#### 上一会话产物归档（已 commit by `801689418`）

- `docs/audit/2026-07-28-llm-gateway-flow-comprehensive-audit.md`（840 行）
- `docs/design/2026-07-28-llm-gateway-flow-improvements.md`（651 行）
- `scripts/local-deploy-test.sh`（624 行）

### 2.4 CHANGELOG 更新

- `CHANGELOG.md` 新增 `[Unreleased] - 2026-07-29` 段，覆盖本会话全部 4 大产出

## 3. Current State

### 3.1 仓内状态（origin/main）

```
9ee5fc286 feat(ursm-v2-cutover): runbook + rollback shell script (P0-3)
34532504a feat(migrate-ursm-v2): one-time PG→Redis migration tool (P0-3)
58f9b9a39 test(streaming): pin shadow + canary routing backend unchanged (P0-3)
62b60662a feat(ursm/v2): wire shadow-double-write metric + tests (P0-3)
12aebedf9 docs: add 会话优化v3 documentation
20cc6c10b Merge branch 'feat/integrity-probe-worker-closure-20260729'
537c7a4f2 fix(live_stream): close worker goroutines on idle marker
7a9b3505f chore(hotconfig): surface reload events at info level for observability
801689418 chore: build metadata 1417 + migration 462 entry + audit/design docs + local-deploy-test script
f9cbe7998 feat(recovery+systemmonitor): align M3 follow-up wiring + rollout config + admin recovery handlers
95755779c feat(metrics): shadow write failure counters + alerting rules (P0-2, R-3.3/R-3.4)
44e05a757 docs(operations): v2 Pipeline feature flag status confirmation (P0-5, R-5.2)
```

### 3.2 未提交工作区（保留不动）

```
 D docs/omni-ref/00-AUDIT-EXISTING-DOCS.md
 D docs/omni-ref/01-TS-TO-GO-FUSION-GUIDE.md
 D docs/omni-ref/README.md
```

这 3 个文件删除由其他 agent 触发，本会话不涉及。

### 3.3 验证证据

- `go build ./...` 对涉及包全 pass（其他 pre-existing build error 来自 bg/systemmonitor + recovery_gate_adapter，与本会话无关）
- `go test -race ./metrics/... ./domains/ursm/v2/... ./domains/streaming/executors/... ./cmd/migrate-ursm-v2/...` 全 pass（no cache, count=1）
- 5 个测试包通过 `go test -race -count=3` 稳定性验证
- `bash -n scripts/rollback/ursm_v2_to_legacy.sh` 通过；`--help` / `--env=bogus` 拒绝逻辑正确
- `python3 -c "import yaml; yaml.safe_load(...)"` YAML 验证通过
- `gofmt -l` 引入 0 个新 lint 问题（前一会话 stash 测试确认 baseline 36 个 issue 与本会话无关）
- Prometheus smoke 程序验证 3 个新 counter（shadow_write_failed / ringbuffer_dropped / rawaudit_write_failed）正常注册

## 4. Next Steps（依赖老板决策）

### 4.1 P0-3 Stage 1 进入条件（需老板拍板 4 项）

详见 `docs/design/2026-07-28-llm-gateway-flow-improvements.md` §10：

| 决策 | 选项 |
|---|---|
| 切换时机 | A 245 验证后立即切 / B 再观察 1 月 / C 季度内分阶段 |
| 双层 sticky 保留层 | A 保留 executor / B 保留 handler |
| OTel 后端投入 | A 立即接入 / B 先 stdout exporter 后接 |
| URSM v2 数据迁移 | A 一次性脚本 / B 接受历史断档 / C 双轨 30 天后切换 |

老板拍板后，操作步骤详见 `docs/runbooks/ursm-v2-cutover.md` §3。

### 4.2 Stage 1 进入命令（拍板后直接执行）

```bash
# 1) 数据迁移
./bin/migrate-ursm-v2 --pg="$LLM_GATEWAY_DATABASE_URL" --redis="$REDIS_URL" --dry-run
./bin/migrate-ursm-v2 --pg="$LLM_GATEWAY_DATABASE_URL" --redis="$REDIS_URL" --apply

# 2) 启动 shadow 模式 + 双写
export URSM_V2_MODE=shadow
export URSM_V2_SHADOW_DOUBLE_WRITE=1
# 重启 gateway（systemctl 或 docker，按部署形态）

# 3) 7 天观察 llm_gateway_ursm_v2_shadow_records_total{result}
# 4) 漂移 < 1% 后进 Stage 2
```

### 4.3 未来改进（per 设计文档 §3 / §4）

- P1-1 `parallel_tool_calls` 跨协议映射
- P1-2 流式 tool_calls 累积统一到 IR 层
- P1-5 session ID 落库完整性保障
- P2-2 原始 audit JSONL 跨机同步
- P2-3 unified adapter 清理
- P2-4 OpenAI Realtime API 覆盖

## 5. Key Facts（pin to future sessions）

- **P0-3 默认 OFF**：所有环境 `URSM_V2_MODE` 未设（=off）；`URSM_V2_SHADOW_DOUBLE_WRITE` 默认 false
- **路由不变性**：`selectStateBackendWithReady` 仅在 `ModeAuthoritative && Ready` 时切到 URSM v2；Shadow / Canary 模式仍走 legacy credentialstate
- **metric 命名空间**：`llm_gateway_ursm_v2_shadow_records_total{result="recorded|skipped|failed"}` 是 P0-3 唯一新增的 counter；运营方按 result 分类聚合
- **迁移工具幂等**：HSET overwrite，重复运行结果一致；`--dry-run` 默认 / `--apply` 必须显式
- **rollback 不破坏数据**：`scripts/rollback/ursm_v2_to_legacy.sh` 不 DEL URSM v2 Redis 命名空间，便于事后定位

## 6. Blockers / Risks

- **4 项老板决策未到位**（P0-3 全部依赖项），本会话仅交付框架不切流生产
- **运维侧告警挂载未确认**：`deploy/monitoring/grafana-alerts/shadow-write-failures.yaml` 已在仓内但需运维在 Grafana provisioning 加载
- **stage_backend_test.go 与 P0-4 sticky 决策耦合**：当前 shadow/canary 路由 backend 测试假设 sticky 决策由 executor 承担；如未来 P0-4 改用 handler sticky，本测试需要更新

## 7. Suggested Skills (for future sessions touching this area)

- `verification-loop`：L1-L4 验证（rule 03 §6.0）
- `tdd`：本会话所有 metric 改动都遵循 red-green 循环
- `golang-testing`：Go 测试模式（miniredis / table-driven / subtests）
- `golang-patterns`：Go 编码惯例
- `error-handling`：metric Inc 调用与 slog 的并存惯例
- `llm-gateway-deploy-test`：245 staging 部署后端到端验证（P0-3 Stage 1 实际跑时需要）
- `deploy-245`：245 staging 部署技能
- `deploy-154`：生产部署技能（P0-3 Stage 3 切流生产时需要）

## 8. References

- **审计**：`docs/audit/2026-07-28-llm-gateway-flow-comprehensive-audit.md` §7.1 R-7.1
- **方案**：`docs/design/2026-07-28-llm-gateway-flow-improvements.md` §2 P0-3
- **Runbook**：`docs/runbooks/ursm-v2-cutover.md`
- **告警规则**：`deploy/monitoring/grafana-alerts/shadow-write-failures.yaml`（P0-2）
- **v2 Pipeline 状态**：`docs/operations/v2-pipeline-status.md`（P0-5）
- **历史审计**：`AUDIT_CONCURRENCY_HARDENING_20260727.md` / `AUDIT_URSMV2_CONCURRENCY_20260728.md` / `AUDIT_24H_SUMMARY_20260728.md`

---

**Session 收口完成时间**：2026-07-29  
**下次启动建议**：老板拍板 4 项 P0-3 决策 → 按 runbook §3 进入 Stage 1 → 7 天观察 → Stage 2/3