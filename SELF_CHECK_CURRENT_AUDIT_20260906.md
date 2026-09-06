# 自检与供应商节点状态：当前审计与需求基线

> 审计日期：2026-09-06  
> 适用仓库：`llm-gateway-go-3`  
> 目的：统一自检相关设计文档的有效结论，核对代码与数据库迁移，修复已确认缺陷，并区分源码验证与真实环境验证证据。

## 1. 特性需求基线

### 1.1 状态恢复闭环

系统必须覆盖三条互补路径：

1. **热路径恢复**：成功请求完成后，按 `(credential_id, raw_model_name)` 恢复绑定状态；模型名存在多个候选时不能让整个恢复事务因歧义失败，必须记录告警并作确定性选择。
2. **后台恢复**：周期扫描凭据级可用性、周期配额和绑定级冷却状态；不能因单个 `broken_confirmed` 模型阻塞同凭据的健康模型。
3. **探测驱动恢复**：探测必须进入持久队列，具备跨实例去重、并发限制、失败重试和退避；只有提交成功后才能推进调度时间。

### 1.2 周期性配额自检

- 适用于所有供应商，不绑定单一供应商实现。
- `default_probe_model` 使用上游可接受的 `outbound_model_name` 或 `raw_model_name`，不得写 `standardized_name`。
- 到期恢复必须尊重 5 小时窗口下限和 weekly/monthly 窗口守卫，避免“探测模型成功但业务配额未恢复”的死循环。
- 没有默认探测模型的凭据由扫描器自动补齐。

### 1.3 可观测性与诊断

诊断必须能发现：

- 模型绑定歧义；
- 不可用绑定的 NULL `unavailable_recover_at`；
- 到期仍未恢复的凭据；
- 缺少 `node_probe_state` 的绑定；
- `broken_confirmed` 造成的恢复阻塞；
- `model_offers` 与 binding 的状态不一致，包括 NULL/缺失行；
- 长时间未执行的探测；
- 当前状态分布。

### 1.4 迁移一致性

startup 迁移、installer embedded 迁移和 deploy 迁移必须接受运行代码实际写入的 `self_check_runs` taxonomy：

- `error_type`：历史 `http_*` 与 canonical errorsx 类别；
- `selection_strategy`：历史值、`fallback_%` 和当前 `featured/recent/common_7d/failed_model/no_eligible_model`。

## 2. 文档审计结论

### 2.1 当前有效结论

以下设计结论仍有效：

- `AUDIT_PERIODIC_QUOTA_SELFCHECK_20260831.md`：周期配额 guard、默认探测模型和上游名称约束。
- `ANALYSIS_NODE_STATE_SYNC_GAP_20260902.md`：流量绕行、NULL recovery time、模型级/凭据级守卫、探测提交时序等根因。
- `NODE_STATE_SYNC_GAP_FINAL_AUDIT_20260902.md`：队列积压、异步窗口和 RestoreOnSuccess 歧义的运行时证据。
- `LOCAL_NODE_STATE_SYNC_DIAGNOSIS_20260903.md`：缺失 probe state、过期探测和队列提交失败的本地数据证据。
- `docs/changelogs/2026-08-26-recharge-selfcheck-design.md`：充值恢复是凭据级扇出恢复，不是单模型恢复。

### 2.2 已发现的文档漂移

`SELFCHECK_OPTIMIZATION_RECOMMENDATIONS_20260906.md` 仍将 P0.3 和 P2.3 描述为未实施，但当前代码已经：

- 在 `modelbinding/resolver.go` 的 exact/normalized 两条路径选择第一个候选并记录 WARN；
- 在 `bg/credential_recovery.go` 的三个 nil hook 入口记录 ERROR。

因此该文档的待办部分属于历史计划，不可作为当前实现状态的单一依据。本文件作为当前审计基线，后续变更应更新这里或明确引用新的状态记录。

### 2.3 不应误判为已完成的事项

以下事项仍缺少真实运行或长期观测证据：

- 最新代码在生产/测试容器中的端到端恢复成功率；
- 33 个历史逾期探测和 13 个缺失 probe state 的数据清理结果；
- P2 优先级队列、快速通道、预热机制；
- 状态一致性 Prometheus/Grafana 告警；
- 充值恢复的 mock-upstream 端到端 happy path；
- startup 与 deploy 两条迁移路径在所有历史数据库上的实际升级验证。

上游 429、410、网络 reset 等结果应归类为真实上游故障，不应被统计为恢复代码缺陷。

## 3. 本次审计发现与修复

### 3.1 诊断 SQL/schema 错误

**问题**：诊断 SQL 和 shell 脚本引用 `providers.name`，但当前 schema 只有 `providers.display_name`，导致详情查询失败。

**修复**：

- `sql/diagnostics/selfcheck_diagnostics.sql` 使用 `pv.display_name`；
- `scripts/diagnose_selfcheck.sh` 使用 `pv.display_name`，并同步更新 GROUP BY。

### 3.2 状态对账漏报 NULL/缺失行

**问题**：`!= COALESCE(...)` 与普通 `!=` 在 SQL 三值逻辑下会漏报 NULL 和不存在的 `model_offers`。

**修复**：

```sql
WHERE mo.credential_id IS NULL
   OR cmb.available IS DISTINCT FROM mo.available
   OR cmb.unavailable_reason IS DISTINCT FROM mo.unavailable_reason
```

### 3.3 startup/deploy taxonomy 漂移

**问题**：deploy 的 V361 已允许 canonical `error_type`，startup/installer 的 644 只补了 `selection_strategy`，升级数据库可能继续因 CHECK constraint 拒绝运行时自检写入。

**修复**：

- startup 644 增加 definition-aware 的 `self_check_runs_error_type_check` 重建；
- 保留历史 `http_%` 与 `none` 等值；
- 使用 `NOT VALID`，避免历史脏数据阻断升级；
- installer embedded 644 与源码迁移保持字节一致。

### 3.4 迁移契约测试窗口缺陷

**问题**：`scripts/apply-db-revision-sequence_test.sh` 使用 `grep -A40` 截取迁移数组，随着数组增长会误报已存在的 `V371` 缺失。

**修复**：改为从 `files=(` 读取到闭合 `)` 的完整数组，保持 append-only 迁移序列可测试。

### 3.5 诊断脚本错误传播

**问题**：`set -e` 不能保证 `psql | tee` 的上游错误传播，诊断可能显示不完整报告后退出成功。

**修复**：使用 `set -euo pipefail`。

## 4. 验证证据

### 4.1 已通过的源码/契约验证

```text
./tests/selfcheck_diagnostics_contract_test.sh
PASS self-check diagnostics schema and NULL-safety contract passed

./scripts/apply-db-revision-sequence_test.sh
apply-db-revision-sequence contract passed

go test ./sql/migrations/startup -run 'TestMigration644|TestNumericUpMigrationVersionsAreUnique' -count=1
PASS

go test ./modelbinding/... ./bg/... ./sql/migrations/startup/... -count=1
PASS

go vet ./modelbinding/... ./bg/... ./sql/migrations/startup/...
PASS
```

### 4.2 实际数据库查询验证

在本地 PostgreSQL 容器中以 `psql -X -v ON_ERROR_STOP=1` 执行完整 `sql/diagnostics/selfcheck_diagnostics.sql`，返回码为 0；所有诊断查询和只读状态展示均可按当前 schema 解析执行。该执行不包含被注释的修复 SQL。

### 4.3 本地服务验证边界

本地网关 `http://127.0.0.1:8782/healthz` 返回 `200` 和 `ready=true`。metrics 端点在未提供认证时返回 `401`，这是认证行为，不是服务存活失败。

当前证据只能证明：

- 本地服务存活；
- 当前诊断 SQL 与数据库 schema 兼容；
- 受影响源码和契约测试通过。

它不能证明最新源码已经被当前运行容器加载，也不能证明所有历史异常数据已自动清理。生产部署和端到端探测恢复仍需在具备正确密钥/镜像和测试凭据的环境执行。

## 5. 验收与后续工作

### 本次验收

- [x] 需求和相关设计文档已整理。
- [x] 文档状态漂移已明确标注。
- [x] 诊断 SQL 不再引用不存在的 provider 列。
- [x] NULL/缺失 model offer 能被对账查询捕获。
- [x] startup/deploy self-check taxonomy 已统一，embedded 迁移同步。
- [x] 诊断和迁移契约测试通过。
- [x] 受影响 Go 测试与 vet 通过。
- [x] 本地 PostgreSQL 完整只读诊断通过。
- [ ] 生产长期观测、P2 优先级队列和状态一致性监控：不属于本次已完成范围。

### 后续优先级

1. 在隔离测试数据库执行 startup/deploy 双路径升级和约束实际插入测试。
2. 清理并复核历史缺失 `node_probe_state`；对于 `model_probe_broken`/`http_410` 不应盲目重试。
3. 为探测队列增加执行延迟和 backlog 指标，区分 duplicate、submit failure 与 upstream failure。
4. 将诊断脚本纳入受控运维任务，而不是自动执行写入型修复。
5. 再评估 P2 优先级队列和状态一致性监控，避免以异步队列积压掩盖恢复延迟。
