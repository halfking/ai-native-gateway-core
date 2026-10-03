# 206 号 · R89-DQ —— 还 `DEBT(R47)` 第二笔：`admin/memora_handlers.go` 的**假 404**，外加一族分类器的**假阳性**

> 变更面：`admin/memora_handlers.go`（2 行 SQL + 说明注释）、`admin/request_logs_stop_write_classification_test.go`（**重写一条分类的理由**）、`internal/sqlreadguard/guard_test.go`（摘 2 条登记）。
> 债务存量：21 → **20**。**零新增测试、零新增门**。
> 本轮还**抓到一个已量化的既有分类器缺陷**：改完之后我自己被它误判了一次，于是有了可复现的证据。

---

## 〇、起手：205 号清掉了一个「跳过理由」，本轮就把这条债捡回来

205 号 §七·2 更正了 203 的一处错误标注：203 把 `admin/memora_handlers.go:615` 判成「`ORDER BY … LIMIT` 跨 `UNION ALL` 需合并排序」形态。实测**不成立**：

```sql
-- admin/memora_handlers.go:612-617（改前）
SELECT
    COUNT(*),
    (SELECT client_model FROM request_logs <where> ORDER BY ts DESC LIMIT 1)
FROM request_logs <where>
```

`ORDER BY … LIMIT` 在**标量**子查询内部，外层没有 `ORDER BY` ⇒ 不存在跨臂排序问题。
且部署体的 v1 臂本身就是 `request_logs_hot ∪ request_logs`（`db/request_logs_view_schema.go:811-817`）⇒ **换视图不引入任何新的排序语义**。

⇒ 这条债从「被错误理由挡住」变成「可安全偿还」。它也是同文件**形态不一致**最刺眼的一处：`:522` 已双腿、`:615-616` 仍拼接裸读。

---

## 一、🔴 F1：`$2 ≤ 8` 时这条腿**结构恒空**，且直接变成一个**错误的 404**

`sessionLogsWhere`（`admin/session_scope.go:39`）：

```go
clause = `WHERE gw_task_id = $1 AND ts > NOW() - INTERVAL '1 hour' * $2`
```

`$2` = `sc.Hours`，**默认 24、clamp 1..168**（`session_scope.go:18/22-25`）。

把它与 hot 保留期对齐（`R = 8h`）：

| `$2`（小时） | 窗口相对 `R` | 裸母表读的结果 |
|---|---|---|
| **1 … 8** | `W ≤ R` ⇒ 窗口 **100% 落在盲区** | **恒 0 行** |
| 9 … 168 | `W > R` ⇒ 只盲最近 8h | 部分盲（8/24 ≈ 1/3 @ 默认值） |

`R` 是**实测且确认被调度**的：`promote_request_logs_hot_to_partition_interval_integer.sql:5` 的 `p_retention interval DEFAULT '8 hours'`，且 `bg/partition_manager.go:1343` 的 `promoteSpecs()` 确实注册了它（第一项）。

**恒 0 行 ⇒ 什么后果**：`requestCount == 0` ⇒ `:621`/`:625` 抛
`404 "task not found: <task_id>"` —— 而**那个 task 是存在的**，只是它的流量全部落在最近 8h 内。

⇒ 这不是「少显示几行」，是**对存在的东西回答「不存在」**。`requireSessionTaskAccess`（`:605`）在更早一步已经放行了，说明权限是过的、内容确实不存在 ⇒ 用户看到的是自相矛盾的答案。

### 1.1 附带的第二个症状：`latest_model` **系统性**滞后，且不是随机误差

`(SELECT client_model FROM … ORDER BY ts DESC LIMIT 1)`：排序方向 `ts DESC` 意味着**最新的行排在最前**，而盲区恰好是**最新那 8h**。

⇒ 这个子查询返回的永远是「**可见范围内最新**」的行，而可见范围的边界就在 8h 前 ⇒ **`latest_model` 按构造至少滞后 8h**。
⚠️ 要点：这不是抽样误差，是**端点截断** —— 盲区与排序方向重合 ⇒ 误差方向恒定、不随时间抖动。找「受害面」时优先找这种**重合**，误差会系统性地往一个方向偏。

---

## 二、改动与列/谓词安全核对

```diff
 	err := h.db.QueryRow(ctx, `
 		SELECT
 			COUNT(*),
-			(SELECT client_model FROM request_logs `+where+` ORDER BY ts DESC LIMIT 1)
-		FROM request_logs `+where+`
+			(SELECT client_model FROM request_logs_with_current_month `+where+` ORDER BY ts DESC LIMIT 1)
+		FROM request_logs_with_current_month `+where+`
 	`, whereArgs...).Scan(&requestCount, &latestModel)
```

**换视图会不会落进「行级有、谓词级空」？** 权威清单在 `db/request_logs_view_padded_columns.go`（由 `db/request_logs_view_padded_columns_test.go` 强制）：

会话臂**恒 NULL** 的只有 **6 列**（`RequestLogsViewPaddedSessionColumns()` 的机械输出）：`id` / `test_col` / `test_tab_indent` / `provider_model` / `credits_rate_multiplier`（`client_ip` 已于 816 撤销为有源投影）。

- `gw_task_id`：**不在**补位表里 —— 它由 734 的 details 层供给（`d.gw_task_id`）；
- `client_model`：同样由 details 供给（734 把 710 的 `NULL::` 占位换成 `d.client_model`），真库 details **行级**覆盖 99.9996%（hot 1,344/1,344 无缺失，parent 1,683,104/1,683,098）。

⇒ 两条腿都不踩补位列，换过去**不会**让结果集恒空。

⚠️ 但 `gw_task_id` 的**列填充**要单独看：本仓已两次因为用「全历史 NULL 率」判错而记录教训（`request_logs_stop_write_classification_test.go:1136-1138` 与 `:380`）。实测按天是 **09-29 的 4.77% → 09-30 的 37.88% → 10-01 的 97.72% → 10-02 的 98.51%** ⇒ details 写入链在 09-30 前后修好，**近期行带着真值** ⇒ session 臂供数成立。**若按全历史 94.1% 去判，会得出「停写后恒 0 行」这个相反的结论。**

---

## 三、⚠️ 改代码要问的是**理由**还成不成立，不是怎么让断言变绿

`admin/request_logs_stop_write_classification_test.go` 的 `Evidence` 是**逐字断言**（`:1035` `strings.Contains(raw, c.Evidence)`），所以改代码必然让它红。关键是**那条分类的理由**：

| | 改前 | 改后 |
|---|---|---|
| `handleSessionMessages(:795-796)` | 读 710 + `LEFT JOIN …bodies` ⇒ bodies 腿硬失败 ⇒ **degraded** | 不变 |
| `handleMemoraContext(:615-616)` | 裸母表 ⇒ 停写后 `requestCount` 恒 0 ⇒ **errors_out（404）** | 读 710 ⇒ session 臂供数 ⇒ **不再 errors_out** |
| 文件档位 | `degraded_content`，依据是「**同一文件取更危险档**」 | `degraded_content`，依据变成「**只有 bodies 那条腿**」 |

⇒ **`Effect` 不变，`理由` 必须重写**。若只把 `Evidence` 换成新字符串而不改 Note，就会把「取更危险档」这个**已不成立的依据**焊死进测试——下一个读它的人会以为这个文件还有一条会炸成 404 的腿。已按此重写，并在 Note 里显式登记：

- 为什么换视图是对的（details 供给 + 不在补位表 + **近期**填充率）；
- ⚠️ 本条只覆盖**停写（S4）**场景；8h 恒空是**另一个独立根因**，停写后窗口再宽也照样盲，**不要**用它解释本表档位。

---

## 四、🔴 F2：改完之后，**我自己被族分类器误判了一次**（可复现的既有缺陷）

`TestRequestLogsStopWriteSourceFamilyCoversInventory` 的族计数实测位移：

| 族 | 改前 | 改后 |
|---|---|---|
| `bodies_plus_other` | 25 | **24** |
| `view_with_null_padded` | 13 | **14** |

⇒ 文件从「读基表」族迁进了「**行级有、谓词级空**」族。

**但这个触发是假阳性。** 补位列 `id` 在本文件的全部命中（ripgrep 实测，逐条打开确认）：

| 位置 | 实际内容 | 是 SQL 谓词吗 |
|---|---|---|
| `:107-108` | 注释散文「a guessed **ID**」 | ❌ 注释 |
| `:168` | Go map 键 `"id": fact.ID`（**JSON 响应字段名**） | ❌ 响应结构 |
| `:663` | Go map 键 `"id": b.ID` | ❌ 响应结构 |

而这条读点**只**产出 `COUNT(*)` 与 `client_model`，**既不选 `id` 也不按 `id` 过滤**。

**根因**：`nullPaddedPredicateHit` 的判据是**列名出现**，无法区分「该列被用在 `WHERE`/`GROUP BY`」与「这三个字母恰好以 JSON 键或散文形式出现」。⚠️ 这个局限**不是本轮发现的** —— 该测试自己的注释已登记：「这一维是**保守近似**……实测 5 个被标记文件里 **3 真 2 假**」（`:1615-1618`）。本轮只是**又添了一个可复现的实例**，并把族的规模从 13 推到 14。

**为什么本轮不修它**（这本身是一个判断，不是回避）：

1. 该启发式的**保守方向是刻意选的**（`:1386-1387`「解析失败时退回整文件口径……宁可留在本族并**要求具名论证**，也不要把一个真触发悄悄放出族外」）⇒ 假阳性的**既定代价**就是「迫使登记方写具名论证」，而本轮已经把具名论证写进了 Note。**改掉它要先证明不会放进真触发**，那是另一个量级的改动。
2. 本轮**实测它没有造成错判**：`TestStopWriteEffectAgreesWithSourceFamily`（族 × 档位合法组合）仍然通过 ⇒ 这个错误族标签**当前不改变任何 verdict**。

⚠️ 但它是**潜伏陷阱**：族标签会被人当结论引用（「这个文件是谓词级空」），而它是错的。⇒ **登记为下一轮顺位，并附上可复现的最小复现条件**（补位表里存在 `id` 这种通用列名 + 文件里有 JSON 键或散文 ⇒ 必假阳性）。

---

## 五、验证

**负控先在未修复代码上跑**（绿是改完才拿到的）：

```
$ go test ./internal/sqlreadguard/ -run 'TestNoBareRequestLogsMotherReads|TestDebtRatchetDoesNotGrow' -v
    guard_test.go:188: 生产读面出现 2 处裸 request_logs 母表读（8h 盲区）：
        admin/memora_handlers.go:615 裸 request_logs 读（双腿化、内联 sqlreadguard:allow 或登记白名单）
        admin/memora_handlers.go:616 裸 request_logs 读（双腿化、内联 sqlreadguard:allow 或登记白名单）
--- FAIL: TestNoBareRequestLogsMotherReads (1.17s)
=== RUN   TestDebtRatchetDoesNotGrow
    guard_test.go:284: DEBT(R47) 现状：20 条（基线 21），新增 0 条
--- PASS: TestDebtRatchetDoesNotGrow (0.00s)
```

⇒ 门**点名了具体两行**；同时再次确认 202 号选定的棘轮方向：**移除自动接受**（20 < 基线 21 仍绿）。

**族迁移是实测位移、不是推论**：临时把两行退回原样重跑，得到 25/13；改回后得到 24/14（见 §四）。

**其余**：`TestStopWrite*` 5 个分类门全绿（含 `TestRequestLogsStopWriteClassificationEvidenceIsReal` 与 `TestStopWriteEffectAgreesWithSourceFamily`）；10 个已登记守卫 `ok ×10 / 0 FAIL`；`go test ./admin/` 全包 ok；本轮改动文件 gofmt/vet 干净。

---

## 六、证伪与未 settle

**证伪 3 条**

1. 撤回「203 号把 `:615` 判成跨 UNION 排序形态」—— 205 号已证不成立；本轮据此**把它从跳过名单里捡回来**。
2. 撤回「换视图会让 `gw_task_id` 谓词恒空」—— `gw_task_id` 不在 6 条恒 NULL 补位列里，且**近期**填充 98.51%。
3. 撤回「本文件属于 `view_with_null_padded`（谓词级空）」—— **假阳性**；补位列 `id` 只命中注释与 JSON 响应键。

**未 settle（如实登记）**

- **`view_with_null_padded` 这族 14 个文件里假阳性到底占多少，本轮只逐条确认了本文件 1 个**。既有实测是「5 个里 3 真 2 假」（另一时点、另一批）。⇒ 需要一次**逐文件**复核，方法已给出（对每个文件查「补位列是否真的出现在 SQL 的 `WHERE`/`GROUP BY`」，而不是出现在代码里）。**本轮不冒充已覆盖。**
- **未连 PG**：8h 恒空是**静态**结论（谓词 + 保留期 + 调度注册三处对读）；改后的实际行数、`latest_model` 的实际滞后幅度**未实测**。
- ⚠️ **`db/request_logs_view_padded_columns.go:48-49` 有一处文档漂移**：注释写「现网 **6** 列」但紧接着只列了 **5** 个名字（`client_ip` 已于 816 撤销）。**真值以 `paddedSessionColumns()` 的机械输出为准**（6 列，含未在注释里列出的那一列），注释只是过期。**本轮刻意未改** —— 改它要先确认第 6 列是谁，属另一次改动。
- `go test ./...` 全量未跑；CI 未运行；本机 `core.hooksPath` 未设置 ⇒ 本次推送未经 pre-push 门。
- 债务还剩 **20** 条。

---

## 七、下一轮顺位

1. **复核 `view_with_null_padded` 那 14 个文件**，把「3 真 2 假」的旧实测更新成当前实测。判据必须是「该列是否出现在 SQL 的 `WHERE`/`GROUP BY`」，不是「代码里有没有这个词」。⚠️ 先证判据不误伤（§106），再改分类器。
2. 修 `db/request_logs_view_padded_columns.go:48-49` 的文档漂移（先确认第 6 列是谁）。
3. 继续还债（`admin/quality_correlations.go` / `admin/provider_models.go` / `admin/session_sanitize_matches.go` 三条较小的 Go 侧债），仍逐条过五步判据。

---

## 八、playbook 新增

### §115 盲区与**排序方向重合** ⇒ 误差是**端点截断**，不是抽样噪声

判「读面盲区有多严重」时，除了 `(W, R)` 对齐，还要问一句：**盲区落在查询的哪个位置？**

- 盲区在**结果集中部**（如纯 `COUNT(*)`）⇒ 误差按比例摊薄，像抽样；
- 盲区在**排序的端点**（`ORDER BY ts DESC LIMIT 1` 取「最新」）⇒ 误差**恒定偏向一端**且**不随时间抖动** —— 那个「最新值」按构造永远至少滞后 `R`。

⇒ 找受害面时优先找**盲区与排序/分页/取极值重合**的读点：它们的用户可见症状不是「数字略小」，而是「**最新那条永远是旧的**」。
⇒ 修法也随之不同：端点截断**不能**靠调阈值掩盖，必须补腿。

### §116 分类器的**保守近似**会产生假阳性，而它的既定代价是「要求具名论证」——不要顺手改掉它

本仓的 `sourceFamilyOf` / `nullPaddedPredicateHit` 是**列名级**判据：它只问「补位列名有没有出现在这个文件里」，分不清「用在 SQL 谓词」与「以 JSON 键/散文形式出现」⇒ **必然假阳性**（`id` 是重灾区）。

⇒ 处理这类近似分类器时，**先问它的失败方向**：
  - **保守方向**（假阳性 ⇒ 逼你写具名论证）⇒ **可接受，别动**；
  - **激进方向**（假阴性 ⇒ 真触发悄悄漏出）⇒ **必须立刻修**。
⇒ 遇到假阳性实例，**不要靠改分类器来消掉它**（那会同时消掉真触发的检出），要**把具名论证补进登记**。真要改分类器，先证它不会漏真触发（§106）。
