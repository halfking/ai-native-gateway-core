# S3 波 4（第二片）：存储治理面 bodies 读源 + 一个系统性数据差异（jsonb null）

> 轮次：2026-10-14 凌晨，紧接同日的第一片
> （`2026-10-14-s3-wave4-bodies-per-point-first-shard.md`）。本片处理第一片
> 盘点出的 C 类站点中的两族：`admin/data_lifecycle*` 与
> `admin/quality_correlations.go`，外加**一个比这两族都重要的发现**。

---

## 0. 一句话结论

会话族与 v1 对「这一列没有正文」用了**两种不同的表达**：v1 用 SQL `NULL`，
会话族用 jsonb `null`。任何直接照抄 v1 判据（`IS NOT NULL`）到会话族的读法
都是**恒真**——不是少算，是**多算**，而且多出来的那部分在 turns 行里有
独立证据否证它。

---

## 1. 核心发现：jsonb `null` 让「有出站正文」判据恒真

### 1.1 症状

第一片修完 `compression_sessions` 后，两处存储治理面仍在大面积低报：

| 面 | v1-only 口径 | 跨族忠实口径 | 低报 |
|---|---:|---:|---:|
| 存储增长趋势 `compressed`（7 天窗） | 7,076 | 21,431 | **68%** |
| blob 清理预览 `EstimatedFreedBytes` | 13.9 MB | 100.9 MB | **86 MB** |

第二项是**运维决策面**：preview 是「要不要花钱做清理」的依据。

### 1.2 量具：会话族的 jsonb-null 比例（真库实测 2026-10-14）

| 列 | v1：SQL NULL | v1：jsonb null | 会话族：SQL NULL | 会话族：jsonb null |
|---|---:|---:|---:|---:|
| `outbound_body` / `sb.outbound_body` | 28,430 | **0** | **0** | **11,420** |
| `request_body` / `sb.request_delta` | 36 | **0** | **0** | **1,882** |

⇒ `WHERE sb.outbound_body IS NOT NULL` 在 20,643 行里**通过 20,643 行**。
判据等于没写。

### 1.3 多算出来的那部分被独立证据否证

2026-10-12（纯停写前、两侧都有副本）的分桶交叉表：

| 桶 | 行数 | turn 行 `outbound_msg_count` 非空占比 |
|---|---:|---:|
| 两族都判有 | 4,844 | 52.1% |
| **只有会话族判有** | **4,285** | **0.0%** |
| 只有 v1 判有 | 561 | **97.0%** |
| 两族都判无 | 18,086 | 0.0% |

⇒ `session_only` 那 4,285 行，turn 行自己的元数据说**没有**出站消息数。
多算 69%。

### 1.4 会话族里没有任何列能表达这个信号

| 会话侧候选列 | 2026-10-12 非空数 | 能替代 v1 的 5,405 吗 |
|---|---:|---|
| `sb.outbound_body IS NOT NULL` | 9,129 | ❌ 恒真 |
| `sb.outbound_body <> 'null'::jsonb` | 4,204 | ✅ 见 §2 |
| `turns.compression_strategy` / `tokens_saved` / `meta` | 9,129（全表恒非空） | ❌ 无信息 |
| `turns.compression_applied = true` | **0** | ❌ 从未使用 |
| `details.outbound_msg_count` | 3,067 | ❌ 是 v1 的**真子集** |

### 1.5 固化：db 包新增 `SessionBodiesHasOutboundPredicate(alias)`

```go
alias + `.outbound_body IS NOT NULL AND ` + alias + `.outbound_body <> 'null'::jsonb`
```

三处消费点（compression_sessions 顶层谓词 / 其 LATERAL 会话腿 /
data_lifecycle 带租户那一支）统一走它，注释里记全部实测数字。

---

## 2. 「确保数据在更改前后一致」的可验证形态

判据加上 jsonb-null 排除之后，跨族 OR 谓词在**停写前的历史日**上
**逐字复现 v1 基线**：

2026-10-12（纯停写前）：

| 口径 | 值 |
|---|---:|
| v1 | 5,405 |
| 会话族（忠实） | 4,204 |
| **并集（跨族 OR）** | **5,405** |
| 会话族独有（v1 没有的） | **0** |
| v1 独有 | 1,201 |

跨冻结点那天（2026-10-13）也能分解：

| 段 | 值 |
|---|---:|
| v1 段（ts ≤ 冻结点） | **1,671** ← 与老口径全天值**完全相同** |
| 会话段（ts > 冻结点） | 3,552 |
| 新口径合计 | 5,223 |

⇒ 历史部分**一位不差**，新增的全部来自停写后。切分用
`db.V1FreezeCutoffSQL`，不只是 `COUNT(DISTINCT request_id)`——后者能消双生，
消不掉**跨午夜的日桶分裂**（镜像两侧 ts 差 ~15s）。

---

## 3. 逐点处置

### 3.1 `admin/data_lifecycle.go`（2 处）

- **无租户那一支**：`bodies` CTE 由 v1-only 改跨族 + 冻结点切分；
  会话腿带 jsonb-null 排除。
- **带租户那一支**：`LEFT JOIN + COUNT(DISTINCT rb.request_id) FILTER` 改
  **跨族 EXISTS**。这一支要数的是"有多少轮带出站正文"——存在性问题，
  EXISTS 天然免疫镜像双生（本地实测 11,711 个双生 request_id）。
  **v1 那一腿保持裸 `IS NOT NULL`**（v1 的 jsonb-null 实测 0 行，
  加排除反而会少算那 1,201 行、并集就不再等于 v1 基线）。
- 提成包级函数 `dataLifecycleTrendSQL(tenantFilter)` 以便同层断言。

### 3.2 `admin/data_lifecycle_blobs.go`（2 处）

这里要的是 **`pg_column_size` 的列值**，EXISTS 不够用 ⇒ 必须 JOIN ⇒ 撞上双生。
新增 `dataLifecycleBodiesBytesSource`：`DISTINCT ON (request_id)` 收敛到一行，
排序键三段（先挑有 outbound 的 → 再挑有 request 的 → 再挑会话副本）。
任何一段都不能省：只按族排序会让「会话副本是 jsonb-null」压掉「v1 副本有正文」的行。

**字节数与行数故意用不同判据**：字节数回答"能省多少空间"（jsonb `null`
占 4 字节，置 SQL NULL 确实省）；行数回答"有多少行的正文会变成 NULL"
（jsonb `null` 本来就已经是 NULL，算进去会让 affected 与 v1 口径不等价）。

预览结果（真库）：`43,577 / 10,886 / 101,454,339` 字节（老口径 `35,470 / 7,076 / 13,922,642`）。

### 3.3 `admin/quality_correlations.go`（1 处）—— 删掉纯死读点

`LEFT JOIN request_logs_bodies rb ON rb.request_id = rl.request_id`：投影里
**一个 rb 列都没用**。上方注释自陈它是 2026-07-21 Ticket #11 为读
`request_body` JSONB 而加的，而当前分桶已换成**语言维度**（`request_preview ~` 正则）——
注释过期，JOIN 成了孤儿。

**它不是"能退役的读点"，是"什么都不做的读点"**：一个纯死读点没有
"退役后由什么替代"可写，**删掉本身就是评估结论**。

等价性实测（7 天窗全部桶逐行对撞）：en 22,951 行 / zh 835 行，两侧**逐字相同**。

⚠ 本文件真正的停写失效源不是这个 JOIN，而是 `FROM request_logs` 裸母表 +
`is_auto_request = TRUE`——auto/探针流量按设计不进会话族，属已登记的
「探针面族」（不迁，主拍板项）。

---

## 4. 判据与变异验证

判据 `admin/s3_wave4_lifecycle_sources_test.go`，同层驱动
`dataLifecycleTrendSQL` / `dataLifecycleBodiesBytesSource` /
`dataLifecycleBlobCleanupPreviewSQL` / `dataLifecycleCompressedExists`。
为此把 `dataLifecycleTrendSQL`、`dataLifecycleBlobCleanupPreviewSQL`
两条 SQL 的组合从 handler 提到包级。

| 变异 | 结果 |
|---|---|
| M1 忠实判据 helper 去掉 jsonb-null 排除（三处同时说谎） | ✅ 红 |
| M2 带租户那支换回 JOIN 形态 | ✅ 红 |
| M3 字节源去掉按 request_id 去重 | ✅ 红 |
| M4 去掉去重排序键的"内容优先" | ✅ 红 |
| M5 预览计数退回按字节列判 | ✅ 红 |
| M6 **只把 parent 那条会话腿改成恒真**（两条腿里一条说谎） | ✅ 红 |

### 判据纪律：这一片又踩了两次同一个形状

1. **M6「只让一条腿说谎」——Contains 不够，必须 count==N。**
   第一版写 `count(...) == 0`，于是"两条会话腿里只有 parent 那条被改成恒真"
   判据全绿（因为 hot 那条仍然忠实）。已改为
   `count == 2`（无租户那支）/ `count == 1`（带租户那支）。
   这与第一片的"删一条腿"是同一个陷阱的镜像：**只查在场时，
   剩下那条腿的坏改动不会暴露**。
2. **M5「钉了源、没钉消费方」。** 第一版判据钉"字节源投影了
   `has_request_body`"，把预览的 FILTER 改回 `request_body_bytes IS NOT NULL`
   判据依然全绿——投影还在，只是没人用它。
   ⇒ **要拦"消费方改回错判据"，判据必须落在消费方那一句上**。
   这是判据纪律第 9 条（钉性质不钉拼写）的一个新变体：
   **钉错了层**。为此把预览 SQL 也提到包级。

### 变异脚本的一条纪律

第一轮 M1 的变异脚本拼出的 Go **非法**（在 raw string 拼接中间塞了带括号的
替换），测试报 `[build failed]`，脚本把它记成"变红"。
⇒ 变异驱动必须区分 **build failed**（变异无效，判据未被检验）与
**测试断言失败**（有效）。后续所有变异均已确认是后者。

---

## 5. 门状态

- `go build ./admin/ ./db/` / `go vet` rc=0；改动文件 `gofmt -l` 零输出。
- 变异 6/6 红，还原后全绿。
- `admin` 包全量见本轮提交记录：唯一红 = `TestV1BodiesReadersAreAssessed`
  （故意红的负向闸门）。

## 6. 回滚

无 DDL、无 settings 变更、无写路径改动。回滚 = revert；
回滚后这两族面板恢复"只统计停写前历史"（低报，不崩溃）。