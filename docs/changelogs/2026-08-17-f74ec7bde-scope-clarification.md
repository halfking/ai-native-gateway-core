# f74ec7bde 实际作用域澄清

日期: 2026-08-17
类型: docs(changelog) (历史 commit 范围澄清，非代码改动)
影响模块: `sql/migrations/startup/migration_version_unique_test.go`

## 背景

审计发现 commit `f74ec7bde` 的提交信息为：

> fix(sql): renumber context window migration to 523

但实际 diff 只新增了一个文件：

- `sql/migrations/startup/migration_version_unique_test.go`（39 行）

并未包含任何 SQL 重命名 / 数字修改。renumber 工作已在更早的 commit `d7d544cb8`
（`fix(sql): preserve deployed 523 and renumber notify migration`，将 `524_cmb_notify_*`
改名为 `524_cmb_notify_trigger_context_window.sql`，保留 523 号位）完成。

## 决策

由于该 commit 由 `halfking` 提交且已推送 origin/main，本地 reword 会：

1. 改写已推送历史，影响其他克隆者（force-push 风险）。
2. 需要 `git rebase -i` 把后续 5 个 commit 全部重放，blast radius 大。
3. 触及他人 author 的 commit message 在团队规范上属敏感操作。

**不重写 history**，改为本 changelog 文档化澄清。

## 实际语义

`f74ec7bde` 实质作用是给 startup migration 目录加了一道版本号唯一性 guard test
（`TestNumericUpMigrationVersionsAreUnique`，跳过 < 492 的历史审计 collision）。
这是对 `d7d544cb8` renumber 工作的回归保护：后续不允许再出现版本号碰撞。

## 后续建议

如果团队希望 commit message 更准确，可在新 PR 中以 `reword` 单独处理，并
PR 描述中明确"该 commit 仅加 guard test，renumber 在 d7d544cb8"。本次会话不做
rebase-reword，避免影响 halfking 后续可能的开发活动。
