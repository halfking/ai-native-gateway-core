# 2026-11-23 request-logs 列表「有 count 无 items」根修——并行计划 53100 断流 + 游标中断如实返回（主档）

> 报障：`https://llm.itestu.cn/request-logs?preset=custom&from=2026-11-16T16:00:00.000Z&to=2026-11-23T15:59:59.999Z&application_code=default`
> 页面显示「数据加载异常：查询到 66235 条记录，但无法正常显示。这通常是服务端数据格式问题，请联系管理员检查后端日志。」
> owner 指令：检查 192.168.31.211 上的 log，分析原因并修正。
> 修复提交：`cb9a177bf`（rebase 链 `02bc254bc` → `bdb279dad` → `cb9a177bf`，已被 rebase 改写，以 `git log origin/main` 为准）；零迁移 / 零 schema 变更 / 零前端改动。

## 一、定案（因果链，全部实测）

**症状本质：后端返回 HTTP 200 + `count=66235` + `items=[]`** —— 前端 `RequestLogsView.vue` 的「有 count 无 items」金丝雀（2026-11-22 `e428167e5` 加的）把这归类成「服务端数据格式问题」并如实上屏。这不是格式问题，是**游标中途死亡被静默吞掉**：

1. **页查询被 planner 选成并行计划**。211 生产 EXPLAIN ANALYZE 实测：外层查询顶层是 `Gather Merge`（`Workers Planned/Launched: 2`），内层是 `Parallel Append` + `Parallel Hash Right Join` + `Parallel Seq Scan on session_turn_details_2026_11` / `Parallel Index Scan using session_turns_2026_11_ts_idx`。66k 行窗口的 top-N 排序对 planner 有并行吸引力（并行阈值随数据量演化自己走过来——与 2026-11-20 matrix 物化视图发病同机理）。
2. **worker 启动撞 DSM 上限**。acc-pg17 容器 `/dev/shm` 仅 **64MB**，常态水位 **~35-37MB**（`ls /dev/shm`：大量滞留 `PostgreSQL.*` 段，1MB/2MB/8MB/196KB 混杂，时间戳跨度数小时——并行 worker FATAL 后留下的孤儿段）。worker 启动要 resize 一个 8MB 段 ⇒ `ERROR: could not resize shared memory segment "/PostgreSQL.…" to 8388608 bytes: No space left on device (SQLSTATE 53100)`。
3. **游标死亡点在迭代中途**。pgx 的错误在 `rows.Next()` 处浮出 ⇒ `rows.Err() != nil`。旧代码这里只打一条 WARN（`admin listLogs rows.Err after iteration`）然后照常 `writeJSON` ⇒ **200 + items=[] + count**。
4. **COUNT 聚合照常成功**（串行或幸运），所以 count 是对的、items 是空的 ⇒ 前端误报「数据格式问题」。

**取证与复现时间线（全部 211 slot 日志原文）**：

| 时刻 (UTC) | 事件 | 证据 |
|---|---|---|
| 11-22 18:46:02 | owner 报障的请求打在旧槽 a（614b72e9）：rows.Err 53100，items=0，count=66235 | slot-a docker logs |
| 11-22 18:47:12 | 并行车蓝绿切到 17e98faf（含 e428167e5 前端金丝雀） | docker inspect Created |
| 11-22 18:52:47/48/48 | 本轮取证用同一窗口 3 连发：**3/3 同一条 53100**，count=66390，items=0（HTTP 200） | slot-b docker logs |
| 11-22 18:5x | psql 直跑同一 SQL（`\bind` 同参数）：**100 行 / 380ms / Gather Merge** —— SQL 本身无缺陷，纯环境性触发 | /tmp/gw211-outercount.out |
| 11-22 19:0x | 同 SQL 关并行重跑：**805ms / 0 并行节点 / Index Scan on ts_idx** —— 串行可行且远在 30s 预算内 | /tmp/gw211-serial.out |

「为什么时好时坏」：DSM 水位瞬时有余时并行也能成（psql 直跑那次就成了）；水位高时确定性爆。**不能拿单次成功当豁免证据**（matrix 发病前一天也没发病）。

## 二、修复（admin/logs.go，两件事）

### 1. listLogs 三条语句全走「串行计划事务」

`h.db.Begin(ctx)` → `SELECT set_config('max_parallel_workers_per_gather', '0', true)` → **COUNT+SUM 聚合 / by_model 分组 / 分页页查询全部改走 tx** → `tx.Commit`。

- **为什么是事务内 SET LOCAL**：pgxpool 连接复用，会话级 SET 会泄漏给下一个租用者（FIXME-14377 实测全局关并行对部分大会话查询 2.4× 变慢 ⇒ 不能全局化）；SET LOCAL 在 commit/rollback 时自动还原，池连接归还时并行度恢复默认。仓内先例：`turn_writer.go`（advisory-lock 事务同款 set_config）、`materialized_view_refresher.go` serialRefreshViews（357 族）。
- **串行代价实测可接受**：同 SQL 380ms → 805ms（缓存热），预算 30s。拿 ~0.4s 换掉确定性生产故障。
- **by_model 游标改显式 `Close()`**：pgx 事务持有单连接，`defer Close`（池时代形态）会把游标拖到函数尾 ⇒ 分页页查询没法定义在同一条连接上。
- 聚合失败退回单独 COUNT 的兜底、by_model 失败省略、rows.Err 检查等既有语义全部保留（`TestListLogs_ScansRowSetOnceAndNumbersUnchanged` 在 acc-pg17 生产库通过 ⇒ COUNT/SUM 数值零漂移）。

### 2. 游标中断如实报错（错误如实返回纪律）

`rows.Err() != nil` ⇒ **503** + 事实/性质/处置三要素文案：
`"request log list query interrupted before completion: received N of M counted rows; storage-side error, retrying may help"`
驱动原文只进日志（`internal_error_guard` 纪律：err.Error() 不上 wire；文案静态形态有门钉住）。前端 `surfaceError` 走 `errors.byStatus.503`（「服务暂时不可用，请稍后重试」）+ 原因后缀，8 语种由 parity 门保证。**部分行已读也报错**——静默截断的第 37/100 行比错误横幅更骗人。

e428167e5 的旧闸（全行扫描失败 ⇒ 500 `data scanning failed`）保留，与新 503 并存。

## 三、门（admin/logs_serial_plan_test.go，全部变异自证）

| 门 | 形态 | 变异自证 |
|---|---|---|
| 真库 tracer：set_config 恰一次、`'0', true)` 字节形态、先于每条日志源语句、COUNT/SUM 仍合并为 1 条 | TEST_PG_URL + pgx QueryTracer（沿用 logs_single_scan_test.go 基建），对 **acc-pg17 生产库**实跑 | M1 `true`→`false`：真库门 + 静态门**全红** |
| 静态接线：listLogs 体内无 `h.db.Query`、有 Begin/Commit、503 接线、旧 500 闸并存、byModelRows 显式 Close | 源码扫描（logs_probe_lane_test.go 同款字符串门） | M2 页查询 tx→h.db：**红**；M3 摘 503：**红**；还原后**全绿** |
| skip 纪律 | 未配 TEST_PG_URL 时 skip 文案自述「跳过不构成证据」 | — |

## 四、验证与部署终态

- `go build ./admin/ ./cmd/gateway/` ✅；`go vet ./admin/` ✅；gofmt 经手文件零漂移。
- `go test ./admin/` 全包：**ok（224.7s，终树全绿）**。注：rebase 前的基线（16621a446）上 `TestGetDimensionColumn` 存量红，被上游 Top10 轮（`c07277cf8` 族）在新尖收口——终树无此红，非本轮处理面。
- 部署：【部署终态见文末「终态回填」】。
- 线上行为级验证：【终态回填】。

## 五、登记（L 面）

- **L1（环境，owner 决策项延续）**：acc-pg17 `/dev/shm`=64MB + 滞留 DSM 孤儿段是**全机性地雷**——任何并行计划都可能 53100。本轮只收编 listLogs 三条语句；**未收编同族**：getLog 详情路径（点查、LIMIT 1，低险）、dashboard/top-models（有 hot 腿闸）、lifecycle 大扫描、以及 **MV refresher 中 357 族之外的视图**（若它们仍可并行，水位会继续被推高）。根治选项仍是加大容器 ShmSize（需重建 PG 容器，2026-11-20 起挂 owner 决策）。新写「大窗口 + 排序/聚合」查询时应照 listLogs 定式主动关并行。
- **L2（前端金丝雀的措辞口径）**：「数据加载异常…服务端数据格式问题」的默认文案现在只会在**未知**的空列表路径触发（本缺陷的两条已知路径都改为后端显式报错）；若未来再看到它，就是新的未知路径，按金丝雀对待。
- **L3（partia-scan 静默丢行）**：页内部分行 Scan 失败仍是「WARN + 少几行」静默形态（2026-07-16 attachments 修复后未再见实例）——同属诚实性家族，本轮未动（无实例支撑、改动面波及行语义），留待该族列车。
- **L4（非本轮）**：用户报障 URL 里的 `application_code=default` 是**展示层参数**（行渲染用），列表查询不带它——URL 语义与查询语义的这点错位无害，登记备查。

## 六、终态回填

- **部署路径（并行车代发，验收按「现役树 ⊇ 我的提交」）**：本轮三次自行发车均撞锁（本地/远端部署锁被并行车连续部署占用）——期间另一台部署机（niuerdeMacBook-Pro.local）把 **origin/main 尖 `30185a126`** 发上 211（seq 3223，槽 b @8783）。`git merge-base --is-ancestor cb9a177bf 30185a126` 实测通过 ⇒ **现役树包含本修复**（仅多一笔无关的 usage-trends 深链）。按 2026-11-18/19 既有定式：「自己再部署一遍」不是义务，「现役树 ⊇ 我方提交 ＋ 行为级证据」才是义务。现役身份 `2.6.1-30185a12-20261123-211`（git_sha 干净无 -dirty）。
- **API 行为级验证（同窗口 5 连发，间隔数秒）**：HTTP 200 × 5，**items=100 × 5**（修复前同窗口同条件 3/3 items=0），count 67520→67538 随活流量正常增长，响应 ~290KB（修复前 2.9KB 空列表），延迟 1.26~1.47s（串行计划 + 全量载荷，30s 预算内）。
- **页面行为级验证（用户报障原 URL 实开）**：`/request-logs?preset=custom&from=2026-11-16T16:00:00.000Z&to=2026-11-23T15:59:59.999Z&application_code=default` —— 列表渲染完整（「67,615 total · Page 1 of 1353」+ 数据行 + 「共 67615 条」徽标），**零错误横幅**；页面实际查询 `page_size=50` 原样重放 count=67669 / items=50 / 200。截图证据 `docs/audit/evidence/2026-11-23-211-request-logs-fixed.png`。注：初次截图捕到列表查询返回前的瞬态空态（「无记录」），属加载时序非缺陷——数据到达后即渲染完整列表。
- **日志零复发**：验证窗口内 `admin listLogs` 相关 WARN/ERROR **0 条**（修复前同窗口 3/3 必出 53100 断流 WARN）。
- **串行免疫对照**：验证全程 acc-pg17 `/dev/shm` 水位不变（64MB 上限、35MB 使用、55%）——列表正常返回并非 shm 水位缓解，是串行计划绕开了 DSM 分配。
- **同族活证据**：验证窗口内 `dashboardapi` 仍中一条 53100（4MB resize）——L1 登记的未收编面是真实在发的雷，不是纸面推断。
- 文档与本主档随 `docs` 提交进仓（运行时修复主体为 `cb9a177bf`）。
