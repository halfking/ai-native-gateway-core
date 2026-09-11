# 2026-09-12 request_logs 冷迁移停滞修复——claim 守卫接线根除 + promote 自愈（迁移 694，后重编号 695）

## 概要

P2 冷迁移停滞（审计 docs/audit/2026-09-11-node-degrade-false-positive-fix.md §7/§9）根因闭环：promote worker 自 2026-09-10 01:54 起每 tick 撞 `uq_request_logs_2026_09_final_success_session`（23505）整批回滚，父表 max(ts) 冻结 2.5 天、hot 表积压 84,610 行。根因是 2026-08-26 merge `d2cbaf88b` 丢失 `telemetry.SetClaimClient` 接线且恢复提交漏补，promoted 分区守卫从未生效，长活会话（>8h）跨 promote 边界产生第二条 `is_final_success=TRUE` 毒化 promote。

## 修复

1. **claim 守卫接线根除**（`domains/hooks/observability/telemetry/client.go`）
   - `claimSessionFinalSuccess` 增加 `c *Client` 参数，两个调用点（insertRequestLog/updateRequestLog）直传接收者，删除包级 holder（`SetClaimClient`/`loadClientForClaim`/`claimClient`）。
   - wiring-gap 从"依赖人记得接 main.go"变为"编译器强制的参数传递"，同族缺口不可复发。
2. **promote 自愈**（`sql/migrations/startup/695_request_logs_promote_final_success_self_heal.sql`，原编号 694）
   - 重装 `promote_request_logs_hot_to_partition`（承 688 函数体），原子 CTE 前对 retention 外、目标 heap 分区已有同会话 TRUE 行的 hot 行 demote 为 superseded（FALSE），WARNING 计数。已毒化数据自动排空，无需人工修数。
   - `migration_695_test.go` 形状断言钉住三列清单一致与 demote 谓词范围。

## 测试

- 真库验证：一次性 PG17 容器 + 最小夹具（135 列同构 + 部分唯一索引 + ensure 桩），生产同款毒行 demote 入分区、新鲜冲突行不误降、幂等、不变式成立。
- `go test ./domains/hooks/observability/telemetry/ ./sql/migrations/startup/ ./bg/` 全绿。

## 同场还债（Local 门禁 3 项历史失败）

- `admin.TestModelOfferSuggestions_ConfidentMatch`：断言 ID 与 mock 目录数据写反（7=haiku/8=opus，matcher 正确返回 8）。
- `admin.TestUpdateModelOffer_ClearCanonical_UsesBindingJoin`：pgxmock 反射 Scan 不支持裸值 → 指针字段目标，mock 行改传 `*int`/`*string`。
- `ursm.TestScriptSizes`：lua 尺寸钉值更新为 11af45216 后实际值（12303/11227）。

## 部署跟进

与 e04197ec8、d812e1a53 同批上 245/154。部署后观测：journalctl "demoted N hot final-success claim(s)"（预期 N=7）→ promote batch 恢复 → 父表 max(ts) 追平 now-8h。

## 重编号补记（2026-09-12 R15，撞号实测）

首次部署（2085-494ff4df，双节点）后自愈未生效：promote 仍 23505、函数体无 demote 块。排查定案——共享 252 PG 的迁移账本里 694 号已被**其他项目**的 `694_partition_ensure_timezone.sql` 占用（schema_migrations + llm_gateway_migration_checksums 双双记录于 2026-09-11 21:28）；deploy 的 pending 判定按 `schema_migrations` 记录跳过，本迁移 SQL 从未执行。P5 会话建号时撞上跨项目占用（"≥492 全局唯一"纪律此前只覆盖仓内，未覆盖共享库）。

处置：迁移文件/测试/sequence 脚本入口整体重编号 **694 → 695**（账本 690–694 已占、695 空闲），随 R15 第二次部署（2086）pre-switch 生效。教训：建号前除仓内查重外，还须查 `llm_gateway_migration_checksums` 账本实际占用。
