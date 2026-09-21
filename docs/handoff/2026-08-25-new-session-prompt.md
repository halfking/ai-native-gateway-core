# 新会话引导提示词 — llm-gateway-go v6 / D28-D34 续作

复制下面 ```` 块内的全部内容到一个新会话中。新会话以 ZCode CLI 启动，目标仓库为 `/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-2`。

---

```text
你是 llm-gateway-go 仓库的 v6 后端 agent。今日 2026-08-25。前一会话（D28-D34 段）已经完成 W2-8 / W2-9 两项工作并合入 main。

## 强制首步（不要跳过）
1. `cd /Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-2`
2. `git pull --ff-only`（避免覆盖远端 `88c6fbf7b`）
3. `git log --oneline -5` 确认 main 顶端 commit 包含：
   - `88c6fbf7b feat(bg,sql): pair session_module_executions_hot + dashboard_access_events_hot with PartitionManager`
   - `30eb4c34f fix(telemetry): stop writing removed request body columns`
4. 完整阅读 `docs/handoff/2026-08-25-v6-d28-d34-handoff.md`，把"已完成清单 / 待办清单 / 不可触碰 / 锁与约束"四节背下来。
5. 阅读 `docs/migrations/2026-08-24-lp5-body-storage-schema-baseline.md` 理解 LP5 schema baseline。

## 上一会话已交付（ground truth）
- W2-8 DONE — LP1 drop `request_logs_hot.body / response_body / outbound_body`：
  - schema：`sql/migrations/startup/573_drop_request_logs_body_columns.sql`
  - 停写：commit `30eb4c34f`
  - audit：`bash scripts/check-body-storage-schema.sh` 必须 exit 0
- W2-9 DONE — `bg.PartitionManager.promoteSpecs()` 注册两条新 spec：
  - 新增 `sql/migrations/startup/580_session_module_executions_hot_promote.sql`（160 行）
  - 新增 `sql/migrations/startup/579_dashboard_access_events_hot_promote.sql`（158 行）
  - commit `88c6fbf7b`
  - promote 周期默认 1h（hot-reloadable），batch 上限 5000，retention 默认 8h

## 待办清单（建议执行顺序）
| 顺序 | ID | 任务 | 关键文件 |
|---|---|---|---|
| 1 | W2-10 | titlestore 移出分区表 + Migration 562 columnar 重审 | `sql/objects/tables/session_title_states.sql`, `sql/objects/tables/session_titles.sql`, `sql/migrations/startup/562_*.sql` |
| 2 | W2-11 | backlog 收缩 + label 失效 | `internal/sessionv2mirror/backlog*.go`，`bg/cleanup*.go` |
| 3 | W2-12 | import dry-run / audit / orphan cleanup | `scripts/audit-imports*`（不存在则新建） |
| 4 | W2-13 | 前端 specialty | `web/src/components/NodeDetailAvailabilityPanel.vue` — **先协调再动手** |
| 5 | W1-9/10 | LP4 waterfall 优化 | `domains/session/queue/memory_optimization*` |
| 6 | W3-13 | nodestatecache 位宽重排 | `internal/cache/nodestate*` |
| 7 | W3-14 | 流式读取 watchdog | `internal/stream/watchdog*` |

## 必带 skills（每次开始 schema 改动 / 分区改动 / 测试时调用）
- `pg-release-schema-manager` —— 启动前 / 提交前必跑
- `pg-columnar-partition-audit` —— columnar + RANGE 分区变动前必跑
- `tdd` —— 任何 Go / SQL 行为变更必须先写 failing test 再实现

## 不可触碰红线
- `web/src/components/NodeDetailAvailabilityPanel.vue` —— 另一作者 WIP 中，**禁止触碰**；新会话如必须介入 W2-13，先在工作区 `git status` 确认其改动是否落地，未 commit 之前绝对不要 read+edit。
- `admin/bg/*` —— 预 stage 区域，禁止自动 commit；任何 untracked 文件留待人工 review。
- 任何 main 上未存在的 hot-table promote 函数，禁止新增 —— 必须先审计 `pg_class.relkind = 'r'`（hot）与 `*`（parent 分区表）才能写新 migration。

## 每次 schema 改动后的硬性验证
```bash
bash scripts/check-body-storage-schema.sh
```
非零退出即视为破坏 LP5 baseline，必须立刻修复或回滚。

## 写作/提交规范
- migration 命名：`NNN_<snake>_description.up.sql` / `.down.sql`，版本号单调递增。
- migration 内容必须包含：
  1. `SELECT pg_advisory_xact_lock(hashtextextended('llm-gateway:promote:<table>_hot:vN', 0));`（如为 promote 函数）
  2. `relkind` / 列级 attribute drift 校验
  3. CTE DELETE+INSERT，`RETURNING 1` 保留
  4. INSERT 前 `ensure_<table>_partition(v_month_value::date)`
- Promote 函数签名固定：`promote_<table>_hot_to_partition(p_retention interval, p_batch_size int) RETURNS bigint`
- 设置项：`lifecycle.<table>_hot_retention_hours` 默认 8，hot-reloadable

## 起步动作（建议）：W2-10
1. 读 `sql/migrations/startup/562_*.sql` 与 `sql/objects/tables/session_titles.sql`、`session_title_states.sql`，判断 titlestore 当前形态（独立分区表还是 in-place 列）。
2. 若为分区表：跑 `pg-columnar-partition-audit`，评估迁移到普通 heap 的代价与影响面。
3. 写 failing test 到 `sql/migrations/startup/migration_562_test.go`（如不存在），明确"移出分区表"成功条件。
4. 草拟 migration .up / .down，跑 `pg-release-schema-manager` 校验；提交前 `bash scripts/check-body-storage-schema.sh` 必须 exit 0。

## 不要做的事
- 不要修改 `bg/partition_manager.go::promoteSpecs()` 已有条目，除非是为了修复。
- 不要把任何 `admin/bg/*` 文件自动 commit。
- 不要在未读 `docs/handoff/2026-08-25-v6-d28-d34-handoff.md` 之前动手任何 W2-* 任务。
- 不要在 W2-13 上动手，除非作者已确认。
- 不要修改 LP5 baseline 文档 `docs/migrations/2026-08-24-lp5-body-storage-schema-baseline.md` 中已审计的列清单。

完工前自我复核：
- `git status` 干净（除允许的 WIP 文件外）
- `bash scripts/check-body-storage-schema.sh` exit 0
- `git log --oneline -3` 顶端是本次新增的 commit，parent 是 `88c6fbf7b`
- 新 commit 不修改 `web/src/components/NodeDetailAvailabilityPanel.vue` 与 `admin/bg/*`
```
