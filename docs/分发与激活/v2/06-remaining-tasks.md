# 剩余任务计划 · 代码核验版 · 2026-07-14

## 1. 交付策略

按“安全契约 → 数据与投递基础 → 客户执行 → 采集与告警 → 灰度发布 → 生产验收”推进。每一阶段必须有可回滚 migration、单元/集成测试和失败证据，前一阶段未通过不得扩大范围。

任务拆解必须优先遵循[业务流程标准](../00-业务流程标准.md)和[流程对齐报告](../业务流程对齐报告-2026-07-14.md)，每个任务交付一个可验收的业务垂直切片。

## 2. P0：激活业务闭环

| ID | 任务 | 主要落点 | 完成标准 | 依赖 |
|---|---|---|---|---|
| FLOW-ACT-01 | Trial 条款同意、15 天配置、Redis HA、审计事件 | `cmd/license-authority`、`licensing`、ActivationWizard、installer | 无同意不签发；同邮箱一次；Authority/Redis/browser e2e 通过 | 无 |
| FLOW-ACT-02 | Key 激活和设备上限 | `CustomerAPI`、`DeviceManager`、LicenseInfoView | 成功/过期/吊销/超限都有稳定错误码、UI 提示和审计 | FLOW-ACT-01 |
| FLOW-OFFLINE-01 | 离线请求→审批→响应导入 | offline API、Authority admin UI、installer | 文件校验、审批、导入、重复导入和失败恢复可验证 | FLOW-ACT-02 |
| SEC-BASE-01 | 清零 secrets BLOCK | `scripts/scan-secrets.sh` 命中项 | BLOCK=0；密钥全部走注入系统 | 无 |

## 3. P1：升级业务闭环

| ID | 任务 | 主要落点 | 完成标准 | 依赖 |
|---|---|---|---|---|
| FLOW-UPGRADE-01 | command envelope schema | `center/types.go`、`docs/api/` | canonical payload、Ed25519 签名、TTL、instance binding | FLOW-ACT-02 |
| FLOW-UPGRADE-02 | durable command queue | migration、`center/store_pgx.go` | `(instance_id, command_id)` 唯一；状态迁移可审计 | FLOW-UPGRADE-01 |
| FLOW-UPGRADE-03 | client receiver/executor | installer/agent + gateway route | pending/stream、断线补偿、白名单、幂等、结果回写 | FLOW-UPGRADE-02 |
| FLOW-UPGRADE-04 | approval and rollout | center admin API/UI、autoupdate | 危险命令审批；canary→batch→full；失败暂停 | FLOW-UPGRADE-03 |

## 4. P1：监控业务闭环

| ID | 任务 | 主要落点 | 完成标准 | 依赖 |
|---|---|---|---|---|
| FLOW-TELEMETRY-01 | metrics/preferences migration | 实际 migration 目录 | runtime metrics、preferences、events 有 up/down 和索引 | FLOW-ACT-01 |
| FLOW-TELEMETRY-02 | collector allowlist | `gateway/internal/collector/` | 只采集资源/版本/聚合错误；不采集 prompt、completion、密钥 | FLOW-TELEMETRY-01 |
| FLOW-TELEMETRY-03 | authenticated ingest/retry | Authority runtime API + reporter | token/signature、重放拒绝、大小限制、opt-out 删除 | FLOW-TELEMETRY-02 |
| FLOW-TELEMETRY-04 | alert lifecycle | alerts migration、rule engine、通知 | trigger/notify/ack/resolve/suppress 全链路 | FLOW-TELEMETRY-03 |

## 5. P2：运营和高级安全

- Release 上传、签名验证、发布、撤回和灰度 UI。
- 下载票据、匿名 download events 和聚合报表。
- License 审计时间线、设备迁移票据和管理员审批。
- 增强指纹漂移策略、nonce 业务集成、完整性校验。
- HSM/KMS 签发密钥迁移和密钥轮换演练。
- 独立回滚 CLI 是否保留，需根据运维入口评审后决定，不作为核心安全边界。

## 6. 统一验收门禁

```text
代码：go test ./... -race（至少相关包）
前端：npm ci && npm run build && vue-tsc --noEmit
安全：scan-secrets BLOCK=0；签名/重放/越权测试通过
数据库：up/down、唯一约束、索引、保留期验证
部署：245 验证，再允许 154；失败可一键回滚
浏览器：trial/key/offline/upgrade/rollback desktop + mobile 实测截图
运营：10 节点 canary 失败后不扩散；断线补偿和重复命令只执行一次
```

## 7. 里程碑

- M0：P0-01 至 P0-04，未完成前禁止生产试用开放。
- M1：P1-01 至 P1-05，完成一个安全的升级命令垂直切片。
- M2：P1-06 + P2-01 至 P2-05，完成升级、采集和告警闭环。
- M3：P2 高级安全、运营 UI、245/154 实测和发布文档。
