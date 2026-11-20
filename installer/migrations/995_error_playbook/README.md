# Migration 995: Error Handling Playbook

## 目的

错误处理手册（Error Handling Playbook）——「错误类型 → 预定执行方案」的结构化
手册（错误处理手册轮，2026-11-20，owner 需求：手册内错误走标准执行；未知错误
由 PI 智能体 log + 上下文分析后落手册，供后续以标准模式执行）。

建出手册的两个载体：

1. **error_playbook_entries** —— 手册本体：错误类型（+ 可选正文匹配模式）→
   预定执行方案。方案三种形态（plan_kind 闭集）：`script`（白名单原语的
   有序步骤，任意代码执行被有意排除）/ `api`（管理面 API 调用模板）/
   `prompt`（AI 执行提示词，不直接改状态）。
2. **error_playbook_executions** —— 执行留痕：每次匹配/执行/跳过记一行，
   script/api 步骤的旧值快照进 steps JSONB ⇒ 可按快照回滚（与 979
   applied_actions 同纪律）。

## 影响范围

- **新表**: `error_playbook_entries`、`error_playbook_executions`
- **函数**: 匹配/计数辅助函数（随迁移创建，DROP IF EXISTS 幂等形态）
- **台账**: 迁移自携带 `schema_migrations` 自登记（≥952 双账本纪律）
- **种子条目**: source='seed' 全部 `plan_kind='prompt'` 且 `auto_execute=FALSE`

## 依赖

- 无前置迁移硬依赖（独立新表族）；errorsx.ErrorKind 闭集（929/930）为语义参照

## 风险评估

- **风险等级**: 低（纯新增表族，不改动既有表/视图；幂等形态可重放）
- **数据安全**: 不触碰既有数据；auto_execute 默认 FALSE（fail-closed，
  PI 分析落库的条目仅非变更方案允许自动执行）

## 验证步骤

```sql
SELECT to_regclass('public.error_playbook_entries'),
       to_regclass('public.error_playbook_executions');
SELECT count(*) FROM error_playbook_entries WHERE source='seed';
SELECT version, applied_at IS NOT NULL FROM schema_migrations WHERE version=995;
```

## 回滚

见同目录 down.sql（撤两表 + 函数 + 台账行）。

## 登记面

canonical（sql/migrations/startup/）与 installer embeddata、dbinit runner、
tsv、docker 通道序列清单等九面见
`internal/sqlguard/migration_995_delivery_test.go`；本目录为离线升级通道
（installer/internal/upgrader/offline_apply.go）投递副本——与 993/994 同族
（2026-11-21 审计轮补齐，此前该面缺位）。
