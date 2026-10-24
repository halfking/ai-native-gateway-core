# R79 · `/api/admin/data-lifecycle/degradation/status`（≥120s）与 `/api/admin/db-status`（~31s）—— 两个「无 deadline 的重活」端点

> 会话：`mvs_bbd1346d211a4eb3ac41b552c82eaecf`（延续 R76~R78 线）
> 日期：2026-10-24
> 生产：154（网关，active 2825-8fbabe33） / 252（PG 宿主 + cron 宿主）
> **对生产的写入 = 0**
> 段标记：`P11-R79`

---

## §0 一句话结论

**两个端点慢的根因是同一个形状：把「无 deadline 的 `r.Context()`」交给「Redis 全量 SCAN」+「gzip 逐文件解压」。**

| 端点 | 旧实测（build 2825） | 根因 | 修法 |
|---|---|---|---|
| `/api/admin/data-lifecycle/degradation/status` | **≥120s**（curl 超时打满） | 备份摘要**每个文件解压两遍**（第二遍不受缓存保护）＋ **4 次** Redis 全量 SCAN | **摘要整体缓存**（指纹=name+size+mtime）＋ **SCAN 4→2**（前缀蕴含，口径不变）＋ **5s ctx 预算** |
| `/api/admin/db-status` | **30.6~33.7s** | **4 次** Redis 全量 SCAN（无 deadline） | 同上（SCAN 4→2 ＋ ctx 预算） |

---

## §1 生产实测（端点族全貌，154 build 2825，REPEAT=3）

| 端点 | 状态 | min | 中位 | max |
|---|---|---|---|---|
| **data-lifecycle/degradation/status** | **000×3（120s 打满）** | 120.0s | 120.0s | 120.0s |
| **turns/sessions** | **500×3** | 30.0s | 30.0s | 30.3s |
| turns/sessions?page=1&page_size=50 | **500×3** | 30.0s | 30.0s | 30.0s |
| **db-status** | 200×3 | 30.6s | 31.6s | 33.7s |
| pending-responses/stats | 200×3 | 4.3s | 4.6s | 5.0s |
| sessions/inspector-stats | 500,200,200 | 2.1s | 8.3s | 10.0s |
| dashboard/session-overview | 200×3 | 4.7s | 5.6s | 6.5s |
| turns/sessions/filter-options | 200×3 | 10.1s | 10.4s | 12.2s |
| **data-lifecycle/stats** | 200×3 | 17.7s | 21.0s | 30.0s |
| data-lifecycle/metrics | 200×3 | 1.5s | 1.6s | 1.9s |
| data-lifecycle/partitions | 200×3 | 0.7s | 0.8s | 3.3s |

⇒ 本轮**只动**前两个（同一根因）；`turns/sessions`（500@30s）、`data-lifecycle/stats`（21s）、
`filter-options`（10.4s）**根因不同，留待后续轮次**（见 §6）。

---

## §2 根因一：备份摘要**每个文件解压两遍**

`domains/dbdegradation/file_reader.go` 的 `GetBackupSummary`：

```go
detailed, err := fr.GetFileSummary(ctx, file.Filename)   // ← 第一遍：解压（有 per-file 缓存）
...
fr.ReadRecords(ctx, file.Filename, func(record BackupRecord) error {   // ← 第二遍：又解压同一个文件
    allSessions[record.SessionID] = struct{}{}
    return nil
})
```

* `GetFileSummary` **有** per-file 缓存（5min TTL），并已算出 `RecordCount` 与 **`SessionCount`**；
* 第二遍存在的唯一理由是「**跨文件全局唯一**的 session 数」不能由各文件 `SessionCount` 相加得到；
* ★ 但第二遍**完全不受 per-file 缓存保护** ⇒ ⇒ **端点每次请求都把全部备份重新解压一遍**。

⇒ **修法**：给 `GetBackupSummary` 加**整体结果缓存**，键 = **备份文件集指纹**
（`文件名 + size + mtime` 的 sha256）⇒ 命中即零 IO；文件增/改/删/被覆盖写 ⇒ 指纹变 ⇒ 自动失效。
★ **口径不变**：`TotalSessions` 仍是**跨文件全局唯一**的精确值（不是各文件之和）。

★ **诚实边界**：指纹是 stat 型（name+size+mtime）⇒ **同名同大小同 mtime 的改写不会被发现**。
这是 stat 型缓存的通用边界，**门把它显式钉住**（见 §4.3），而不是假装它不存在。

★ 顺带加了一个**可直接断言的读数**：`FileReader.SummaryComputations()`
（摘要重算次数）⇒ 既做门禁的判据，也是生产侧的可观测点
（想知道端点是不是还在反复重解压，看它涨不涨）。

---

## §3 根因二：Redis **4 次全量 SCAN**，且**无 deadline**

`domains/dbdegradation/ttl_manager.go` 的 `GetTTLStats`：

```go
patterns := map[string]string{
    "sessions":      "session:*",
    "session_keys":  "session:key:*",
    "api_key_index": "session:apiKey:*",
    "ursm_cache":    "ursm:*",
}
for name, pattern := range patterns {
    count := tm.countKeys(ctx, client, pattern)   // ← 每个 pattern 走完整个 SCAN 游标环
}
```

★ **关键事实（可证明）**：`session:key:*` 与 `session:apiKey:*` **都是 `session:*` 的子集**
（前缀蕴含）⇒ ⇒ **一次** `session:*` 的 SCAN 就能逐字算出三个计数
⇒ ⇒ **SCAN 次数 4 → 2**。
★ **口径逐字不变**：`sessions_count` 仍是「匹配 `session:*` 的**全部**键数」（**含**两个子集）。

★ **第二个缺陷：没有 deadline**。旧代码把 `r.Context()`（**无 deadline**）交给 SCAN
⇒ 慢 Redis 时可以无限跑 ⇒ 端点挂到分钟级。

⇒ **修法**：
1. SCAN 4 → 2（口径不变）；
2. `countKeys` / `countSessionScopedKeys` / `GetTTLStats` **返回 error**
   ⇒ **扫描被打断必须显式报错**，不得让「部分计数」看起来像「完整计数」
   （R77 轮「拿不到就显式记账」的同款纪律）；
3. 端点给 **5s ctx 预算**（`degradationStatusBudget`），超时后 **200 + `degraded` + `degraded_reason`**
   （沿用 `admin/dashboard_board_aux.go` 的约定）。

---

## §4 门（7 条，全部断言真实产物）

### §4.1 `domains/dbdegradation/r79_degradation_status_shape_test.go`（4 条）

| 门 | 断言 |
|---|---|
| `TestR79SessionScopedKeyCountEqualsSeparateScans` | 合并扫描的三个计数与「分别扫三次」**逐字相同**（miniredis 真 Redis 语义 + 旧实现作对照） |
| `TestR79TTLStatsMustReportInterruptedScan` | ctx 取消 ⇒ **必须报错**；且**保留已统计到的部分** |
| `TestR79BackupSummaryCacheHitAvoidsRereadingAndInvalidatesOnChange` | 文件没变 ⇒ 重算次数**不增**（= 第二遍解压已消除）；指纹变 ⇒ **必须重算**且反映新内容 |
| `TestR79InvalidateCacheDropsSummaryCache` | `InvalidateCache()` ⇒ **摘要整体缓存也必须被丢掉**（重算次数 1→2）—— 文件**完全没变**时，只有显式失效能让它重算 |

### §4.2 `admin/r79_degradation_status_shape_test.go`（3 条）

| 门 | 断言 |
|---|---|
| `TestR79DegradationStatusBudgetMustBeBounded` | 预算 **>0 且 ≤10s** |
| `TestR79DegradationStatusAlwaysReportsDegraded` | `degraded` **恒发**（全成功时为 `false`） |
| `TestR79DegradationStatusMustReportInterruptedScan` | 扫描被打断 ⇒ **200 + `degraded=true` + `degraded_reason` 点名 ttl** |

### §4.3 变异（M1~M5 全红 + md5 逐字还原 + 残留自检）

| 变异 | 红的目标门 |
|---|---|
| **M1** 去掉摘要整体缓存 | `TestR79BackupSummaryCacheHitAvoidsRereadingAndInvalidatesOnChange`（重算次数 2≠1） |
| **M2** 破坏前缀分流（把所有 `session:` 键都算成 `session:key`） | `TestR79SessionScopedKeyCountEqualsSeparateScans` |
| **M3** 吞掉扫描错误 | `TestR79TTLStatsMustReportInterruptedScan` + `TestR79DegradationStatusMustReportInterruptedScan` |
| **M4** 端点不标 degraded | `TestR79DegradationStatusMustReportInterruptedScan` |
| **M5** `InvalidateCache()` 不清摘要整体缓存 | `TestR79InvalidateCacheDropsSummaryCache`（重算次数 1≠2） |

还原手段 = **文件级备份 + md5**（`git checkout --` 会恢复 HEAD、抹掉未提交改动 ⇒ LESSONS §315）。

### §4.4 门自己抓到的真问题（一次）

★ 第一版的缓存门写成「改写文件内容但保持 size+mtime 不变 ⇒ 摘要必须不变」
⇒ **实测红**（「首次=2 第二次=4」）⇒ ⇒ **不是代码错，是探针前提错**：
改写内容时**文件大小也变了** ⇒ 指纹变了 ⇒ **缓存正确地失效了**。
⇒ ⇒ ★ **⇒ 改成「用重算次数当判据」**（文件没变 ⇒ 次数不增）⇒ 判据更直接、也更强。

### §4.5 门自己逼出来的第二处加固（`InvalidateCache`）

★ 写完 §4.1 的第 4 条门之后，回头看 `InvalidateCache()` 的**契约**：
它的语义是「丢掉**一切**已缓存的文件信息」⇒ **只清 per-file 缓存、留下摘要缓存是违约的**。
⇒ ⇒ 具体危害：「同名 + 同大小 + 同 mtime 的**覆盖写**」（备份回滚 / 恢复）
⇒ 指纹**不变** ⇒ 摘要缓存**命中旧数据** ⇒ 端点返回陈旧摘要而**没有任何显式信号**。
⇒ ⇒ **落地**：`InvalidateCache()` 现在**同时**清掉摘要整体缓存（`summaryCache = nil`）。
⇒ ⇒ ★ 正常路径（文件集变化）本来就会让指纹变、摘要缓存自然失效 ⇒ 这一步是**第二条独立防线**，不是唯一防线。

---

## §5 变更面

* `domains/dbdegradation/file_reader.go`：摘要整体缓存（指纹 + 计数器 + 深拷贝返回）＋ `InvalidateCache()` 同清摘要缓存；
* `domains/dbdegradation/ttl_manager.go`：SCAN 4→2、错误显式化；
* `admin/degradation_control_handlers.go`：ctx 预算 + `degraded`/`degraded_reason`；
* `admin/db_degradation_handlers.go`：同款 ctx 预算 + `degraded`；
* ★ **没有改 schema、没有改迁移、没有改任何 SQL** ⇒ 不受台账对账闸门影响（代码可入库）。

---

## §6 诚实边界与未做的

1. ★ **本轮无法上生产** ⇒ 原因不是本轮改动，而是 **台账对账闸门**（R78 §5.4 已记）：
   闸门于 2026-10-24 07:55 被父仓 `1894ab0` 修好 ⇒ **自此之后的任何发版都会被 108 条历史漂移挡住**
   ⇒ ⇒ **R79 与 R78 一样入库待发**。
2. ★ **未做的三个端点（根因不同）**：
   * `turns/sessions` **500@30s**（ctx 30s 打满；含逐行子查询 + 2 个 LATERAL + 依赖已被 876 删除的 `idx_sessions_session_id`）；
   * `data-lifecycle/stats` **17.7~30.0s**（全量历史口径）；
   * `turns/sessions/filter-options` **10.1~12.2s**（`first_request_at` 无索引 ⇒ 属主裁决项 D）。
3. ★ **端点级收益尚未在生产测得**（发版被挡）⇒ ⇒ **本轮只交付「机制正确 + 门可断言」**，
   上线后的**同窗口对照读数**（§R78 的纪律：同会话交错 A/B + 多轮中位）留给发版那一轮。
4. ★ **摘要缓存的内存代价**：只缓存**一份摘要**（不含 session 集合）⇒ 内存增量可忽略；
   ★ 这是刻意**不**缓存 per-file session 集合的选择（避免把全量 session id 留在内存里）。
5. ★ `GetTTLStats` 的**签名变了**（`(map, error)`）⇒ 仓内 2 个调用点已同步；
   仓外若有调用方需要一并改（仓内已全绿）。

---

## §7 待属主裁决（累积）

| # | 事项 | R79 的判断 |
|---|---|---|
| A | 台账 108 条漂移（**阻塞一切发版**） | 未变（R78 §9.4 已给分层） |
| B | 902 归属 | 未变 |
| C | `session_summaries(first_request_at)` 索引（惠及 filter-options / inspector-stats / session-overview） | 未变 |
| D | `turns/sessions` 的 500@30s（逐行子查询 + 2 LATERAL + 缺索引） | **新增**：与索引决策相关，宜与 C 一起做 |
| E | `data-lifecycle/stats` 的全量口径是否允许改估算 | 未变 |
| F | agg 失败是否降级成 200+degraded | 未变 |
| G | 库侧作业错峰 | 未变（方案已出，R78 §9） |
