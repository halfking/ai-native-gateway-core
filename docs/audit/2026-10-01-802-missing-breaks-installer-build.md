# 合并 origin/main 后发现：origin/main 自身编译不过（802 迁移缺失）

**日期**：2026-10-01
**状态**：**只报告，未修** —— 该 802 是并发会话的在途工作，不属本审计改动范围。
**发现路径**：round 43 收尾时合并 `origin/main`（8 ahead / 0 behind，无冲突），
按纪律「合并前的绿结论全部作废」重跑全量，随即暴露。

## 一、现象：installer 无法构建

```
$ (cd installer && go build ./...)
cmd/llm-gw-installer/main.go:585:12: pattern embeddata/startup/802_session_turn_details_gw_task_id_index.sql: no matching files found
```

`//go:embed` 指向一个**不存在的文件**。这不是环境问题，也不是本轮引入：
在合并前的 `origin/main` 树上即可复现。

## 二、802 被注册在 4 处，文件却是 0 份

| 位置 | 内容 |
|---|---|
| `installer/cmd/llm-gw-installer/main.go:585` | `//go:embed embeddata/startup/802_…sql` |
| `installer/cmd/llm-gw-installer/main.go:586` | `var sessionTurnDetailsGwTaskIDIndexMigration802 []byte` |
| `installer/cmd/llm-gw-installer/main.go:782` | `embeddedSQLFiles["startup/802_…sql"]` |
| `installer/internal/dbinit/runner.go:391` | `StartupFiles` 条目 |
| `installer/cmd/llm-gw-installer/stats_migrations_test.go:273` | 测试断言该 map 含此键 |

而文件本身：

- `installer/cmd/llm-gw-installer/embeddata/startup/802_*` → **0 命中**
- `sql/migrations/startup/802_*`（canonical）→ **0 命中**
- 兄弟迁移 `801_session_turn_details_duplicate_drain.sql` → canonical 与 embeddata **齐备**

`git log -S` 定位到引入 commit：`fc10b91c5 feat(storage): 迁移 765 —— request_logs_bodies 月分区列存化…`。
`//go:embed` 的 pattern 缺失是**编译期**错误，因此
`installer/cmd/llm-gw-installer` 整包测试直接 `setup failed`，跑不起来。

## 三、既有守卫生效

`installer/internal/dbinit` 里已有的 `TestStartupFilesExistInEmbeddata` 报红
（`StartupFiles` 里有 802、embeddata 里没有该文件）——**这正是上一轮为
「注册了却没落产物」这一族建立的守卫**，它按预期抓到了同类问题。
本轮新增的 `TestStartupFilesAreAllEmbedded`（round 42 遗产）同样覆盖。

## 四、注释的前提已被实测推翻（这是不能顺手补写的原因）

`runner.go:386` 与 `stats_migrations_test.go:271` 的注释都写着：

> 802 (2026-09-30, 会话存储解耦 v3 S4 前置): session_turn_details 族
> (tenant_id, gw_task_id) 部分索引。跨租户访问门 assertTaskInTenant 的
> session 族母表腿依赖它——缺索引时 EXISTS 判定退化为 167 万行顺序扫描。

**实测（local live 库）**：

1. `assertTaskInTenant`（`admin/session_tenant.go`）实际查的是
   `request_logs_hot ∪ request_logs`，**不是** `session_turn_details`。
   注释把两个族混为一谈。
2. 该查询需要的索引**已经存在**：
   ```sql
   idx_request_logs_tenant_task_ts
     ON ONLY public.request_logs USING btree (tenant_id, gw_task_id, ts DESC)
        WHERE (gw_task_id IS NOT NULL AND gw_task_id <> '')
   ```
3. `session_turn_details` 一侧也已有 **4 个**含 `gw_task_id` 的索引：
   `idx_session_turn_details_tenant_gw_task_id`（父表）、`…_hot_tenant_gw_task_id`，
   以及 `2026_09` / `2026_10` 两个月份分区上的
   `…_tenant_id_gw_task_id_idx`。

⇒ 「缺索引 → 167 万行顺序扫描」的前提**不成立**。按注释意图补写 802，
等于新增一组冗余索引；而 802 又完全不在 canonical 迁移目录里，
与「canonical 为单一事实源」的既有约定冲突。

## 五、两条修法各自的后果

| 方案 | 动作 | 风险 |
|---|---|---|
| A. 补写 802 | 新建 embeddata + canonical 两份 802 | 与已存在的索引重复；且 canonical 侧会引入一个与 embeddata 语义可能漂移的第三份表示 |
| B. 撤注册 | 删 main.go 三处 + runner.go 一处 + 测试断言那一行 | 可能删掉并发会话尚未落盘的意图；且会改写别人的 commit 语义 |

**本轮选择：不动。** 理由：并发会话当时正在改 `761_stats_inbox_sync_status_backfill.sql`
（工作树里有未提交改动），802 很可能是同一条工作流里尚未落盘的产物。
由本审计代为裁决「建索引」还是「撤注册」，是在替别人做设计决定。

## 六、交接动作

修法取决于一个本审计无法从代码回答的问题：**802 到底要服务于哪个查询。**
若目标就是 `assertTaskInTenant`，则索引已存在 → 方案 B。
若目标是别的 `session_turn_details` 读面（"会话存储解耦 v3 S4"），
则该读面的 SQL 尚未落地 → 需等它一起提交 → 方案 A，且 canonical 侧必须同步。

在两者确定前，`origin/main` 处于**编译不过**状态。这一点应优先于本审计
其余任何 fresh-install 议题处理。
