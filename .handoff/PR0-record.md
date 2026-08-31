# PR0 — 提交计划与合并决议记录（仅供内部归档）

> 本次会话在计划模式下完成的工作与最终合并决议；未执行任何数据库写操作。

## 已落地的本地改动（合并后保留）

- `docs/audit/2026-08-31-db-structure-consistency-audit.md`
  - 顶部加重声明：本文为 2026-08-31 一次运行的结果，不是当前实时事实，并列出后续重新校核步骤
  - §6.2 勘误：`handoff_logs_view_delete` / `_insert` / `promote_handoff_logs_default_batch` 并非来自 534，全仓无任何创建源，是 534 之前 heap 设计残留（死函数）
  - §6.5 二次复验后的勘误：252 部署真实根因为内嵌迁移集合硬编码精选子集不含 382/383/433/534
  - §七变更记录追加 2026-08-31 勘误条目
- `docs/design/feature-tables-orphans-2026-08-31.md`
  - 顶部加证据等级章节（R 仓库事实 / H 历史审计记录 / S 未复核历史快照 / B 业务假设）

## 合并前 PR0 计划 vs 合并后实际保留 — 关键决策

| 维度 | PR0 计划 | 合并后实际 | 决策依据 |
|------|----------|------------|----------|
| 删除 `request_logs.outbound_body` 列 + view 投影 + partial index（636 migration + db.go self-heal 调整） | ✅ 计划 | ❌ 已撤销 | remote main LP9 schema rollback audit（`71f5257ff`）保留该列；远程 view 仍投影；remote db.go 仍 ADD 该列。两个方向不可合并 |
| `573_drop_request_logs_body_columns.down.sql` 替换为 placeholder + `.bak` | ✅ 计划 | ❌ 已撤销 | remote main 上的 down 文件（274 行完整版）经 LP9 schema rollback audit 评审，是 SSOT；用 placeholder 会回归 |
| 删除 `sql/objects/views/request_logs_bodies_progress.sql` | ✅ 计划 | ❌ 已撤销 | 573 down 中需重建此 view；与远程 LP9 设计冲突 |
| installer 中嵌入 382/383/433/434/534/636 迁移（main.go embed + runner.go StartupFiles + embeddata/） | ✅ 计划 | ❌ 已撤销 | remote main 上的内嵌迁移集合是硬编码精选子集（511–626 范围），不含 382/383/433/534；636 由 PR0 命名但 remote 未接受 |
| `installer/cmd/llm-gw-installer/stats_migrations_test.go` 测试覆盖（`TestStartupFilesIncludeOutboundBodyCleanup` 等） | ✅ 计划 | ❌ 已撤销 | 636 路径放弃后无意义 |
| `configs/env-local.sh`（`PG_USER=kxuser` → `llm_gateway`） | ✅ 计划 | ⏸️ 暂不推送 | 本地开发配置，不应提交到远程 |
| `installer/llm-gw-installer` 二进制（旧 13MB → 新 14MB） | ✅ 计划 | ❌ 已恢复 | 二进制应跟随源代码；远程 main 上二进制未含 PR0 embed |

## PR1 / PR2 / PR3 / PR4 完成情况（修订）

- PR1：installer 启动迁移补齐与测试 — ❌ 合并时撤销（与远程方向冲突）
- PR2：636 migration / embeddata / db.go ensure / canonical view 调整 / bodies_progress view 删除 — ❌ 合并时撤销
- PR3：feature 表文档证据分层与 audit 文档顶部声明 — ✅ 保留（仅文档）
- PR4：installer / db / upgrader 等子包 go vet + go test — ⏸️ 未单独跑（撤销后与远程 main 一致，main CI 应已覆盖）

## 验证结果（合并前）

- `go vet ./...` 无报错（合并前 PR0 代码）。
- `go test ./installer/...`、`go test ./db/...` 全部通过（合并前 PR0 代码）。

## 不在本 PR 内（详见以下 TODO）

1. **未真正运行 `verify-db-consistency.sh` 或 `pg-table-copy.sh`**：目标库 `llm-gateway-pg` 不可访问且未在本机 rebuild，仅做了 Go-side 编译与单元测试。
2. **未连接 252**：所有改动不涉及 252 数据库。
3. **删除 `outbound_body` 列路径已延后**：与 remote main LP9 schema rollback audit 设计冲突，需作为单独 PR 重新评估。

## TODO（仅供后续会话参考）

- 重新评估"删除 request_logs.outbound_body"路径：在 LP9 schema rollback audit 的设计意图之上（保留列、保留 view 投影），是否可以走"列保留但代码层不再写入 + db.go 不再 ADD" 的更温和路径；需先与 remote main 的 632/635 提交作者对齐。
- 跑 `pg-table-copy.sh` 重新校核本地与 252 结构，验证 audit 文档中"修复后结构完全一致"的结论在远程 632/635 之后的当前状态。
- 处理 6 张 feature 表的 `gateway_run_bindings` 与 `orchestration_sessions.session_id` 等关系无 FK 的来源调查（与 `gateway_instances` 关系）— 详见 `docs/design/feature-tables-orphans-2026-08-31.md`。
- 处理 252 测试库上 `handoff_logs_view_delete` / `_insert` / `promote_handoff_logs_default_batch` 死函数的 `DROP FUNCTION`（已记录于 `docs/audit/2026-08-31-db-structure-consistency-audit.md` §6.5）。
- 评估是否要把 382/383/433/534 加入 installer 内嵌迁移集合 — 涉及部署架构决策（与 remote main 维持一致 vs 修正内嵌集合为更宽子集）。
