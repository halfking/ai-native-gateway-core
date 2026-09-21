# R15 部署轮收尾 — 695 自愈上线 + P4 persist 修复 + 迁移撞号根因（694 被共享库占用）

日期：2026-09-12 03:10–04:00 CST · 两节点现运行 `2086-5c58bf34`（245 切换 03:46 / 154 切换 03:51，均一次即成）· 本轮含三次部署：2085-494ff4df（P4 persist 修复先行上线，发现自愈未生效）→ 排查撞号 → 2086-5c58bf34（重编号 695 + 并行会话 e3ff2e45f whitelist 同批）· cred 41 未使用 force_enable

## A 撞号发现与根因（本轮主事件）

**现象**：2085 部署后（P4 的 persist 已恢复）promote 仍 23505——`journalctl` 03:25:07/03:26:58 hot cron 重试耗尽，DB 函数体无 demote 块，而 `schema_migrations` 却已记录 694（applied_at **2026-09-11 21:28**，早于 2084 部署）。

**根因链**：
1. 共享 252 PG 的迁移账本里 694 号已被**另一个项目**的 `694_partition_ensure_timezone.sql` 占用（`llm_gateway_migration_checksums` 与 schema_migrations 双记录 09-11 21:28）；
2. P5 并行会话建号 694 时只做了仓内查重（"≥492 全局唯一"纪律覆盖面不含共享库）；
3. deploy 的 pending 判定按 schema_migrations 记录跳过 → 自愈 SQL 从未执行 → 函数体保持 688 版（无 demote）；
4. `migrate` 子命令只跑 db.go 的 Go ensure 链，SQL 文件通道仅在 deploy db-changelog 流程—— Hence 仓内文件存在≠库里生效（46e3bc7e2 已为此补 sequence 脚本入口，本次补编号）。

**处置**：文件/测试/sequence 入口整体重编号 **694→695**（账本 690–694 已占、695 空闲），随 2086 部署 pre-switch 生效。教训入 changelog 补记：建号前还须查 `llm_gateway_migration_checksums` 实际占用（跨项目共享库）。

## B 部署后观测（全部达成）

| 项 | R14 基线 | 2086 后实测 |
|---|---|---|
| conflict_pairs（hot TRUE ∩ 分区 TRUE） | 7 | **0** |
| request_logs_hot 积压 | 96,069 | **10,6xx**（retention 窗口内正常驻留；03:44:46 起连续 5000 行批次排空 8.5 万） |
| 父表 max(ts) | 09-09 19:35 | **09-11 19:45**（= now-8h 线，追平） |
| `persist collect failed` | 每 tick 必败（154=34/245=37/40min） | **0**（双节点，03:43 起） |
| ursm_node_snapshot_min max(snapshot_ts) | 09-11 21:16（冻结 5.7h+） | **03:53 持续推进**（1,167,409 → 1,215,839 行，+48,430） |
| promote batch | 含冲突行首批恒败 | 恢复（usage_ledger/request_wal/request_logs 等全表正常批次） |

- demote 的 `RAISE WARNING 'demoted N ...'` 未在 journald 抓到（pgx 默认不透传 server WARNING； demote 生效以 DB 层 conflict_pairs=0 + hot 排空为准，观测口径今后以 conflict_pairs 为准）。
- 注：demote 的实际执行者是 154 侧 2085 进程（695 于 03:44 应用后共享函数即刻生效），245 的 2086 进程首 tick（03:44:46）起 promote 全绿。
- 并行会话产物 e3ff2e45f（operator whitelist，'free' 伪模型治理）随 2086 同批上线。

## C cred 18/19/8 100% 故障归因（只读）

provider 18（nvidia integrate.api.nvidia.com）三凭据 6h 失败面（candidate_failure_logs_hot，46 个 distinct raw_model）：

- **主力 404 transient**（8:47/18:35/19:27 次）：`{"status":404,"detail":"Function '<uuid>': Not found for account ..."}` —— 账号侧 NIM function 已下线；
- **410 model_deprecated**（8:7/18:6/19:11 次）：`The model 'z-ai/glm-5.1' has reached its end of life on 2026-07-02` —— 显式 EOL；
- 次要：timeout（19:12）/canceled（客户端取消，噪音）/503/500；
- **无任何 401/403** → 密钥本身有效。**归因：nvidia 目录/账号级模型下线（EOL+function 注销），非密钥失效**。auto-cool 2min 短周期循环是对真实故障的正确反应；治理方向是清理该批长尾模型的路由（运营决策，本轮未动）。

cred 41（hzx/provider 14）：`quota_permanent`，探针在位，upstream 402 `insufficient_balance (1008)`——等充值，**严禁 force_enable**（维持）。cred 42 healthy（此前的 force_enable 为管理员历史操作）。

## 验证分类（如实）

- **通过**：P4 persist 修复（守卫测试负检 + 线上 0 失败 + 快照水位推进）；695 自愈（conflict_pairs=0、hot 排空、父表追平）；双节点部署一次即成 ×2 轮。
- **环境受限**：demoted WARNING 的 journal 可见性（不透传，改用 DB 口径）。
- **未验证**：404 "Function not found" 是否可由 nvidia 侧重新注册 function 恢复（需 nvidia 账号运营介入）。

## 遗留风险

1. 共享 252 PG 的迁移账本被多项目写入，695 也可能被他项目抢占——建号纪律升级：查仓 + 查 `llm_gateway_migration_checksums`。
2. cred 8/18/19 挂靠的 46 个长尾模型持续 404/410，每 5min 烧失败尝试（auto-cool 循环）——建议运营清理模型目录或禁用 provider 18 长尾路由。
3. hot 表剩余 ~1.06 万行为窗口内正常驻留，明晨可复核无回积。

## 关联

- 前置：docs/audit/2026-09-12-r14-observation-p5p4-readonly.md（P4 方案 C 节 / P5 复核）
- changelog：docs/changelogs/2026-09-12-request-logs-promote-final-success-self-heal.md（含重编号补记）
- P4 修复：494ff4df4（persist.Collect 跳过 request_dedup + 守卫测试）；重编号：5c58bf346
