# 接力 handoff — 会话请求数据存储与查询批判式复审收口

**日期**：2026-10-01
**检出**：`__DEV_HOME__/workspace/ai-native-tools/syncfield/llm-gateway-go-4`
**基线**：本会话工作树基于 `c54ab8dc1`（早于 origin/main 的 R35-N1 批）；
S4 读端迁移批次 `38d59b2eb` 的 merge-base 是 `c19baebd4`
**状态**：已提交 `65490212d`，并与 codeup `origin/main` 合并后推送

**主审计报告**：`docs/audit/2026-09-30-session-request-data-re-audit.md`
（§1 结论先行 / §5.1–§5.10 / §6 未完成 / §7 下一步 / §8 待拍板）

---

## 一、结论与根因

原目标：退役 `request_logs` 表族、只走 `session_*`，重新审计会话保存与查询，
确保前后数据一致。**存储侧结论：可用**。读侧本轮挖出 6 个此前无人知晓的缺陷。

### 1.1 存储可用性（§5.7）

35 天窗口 `genuine_loss = 0`（36,693 internal_loopback + 1,536 non_terminal）；
最近 24h `genuine_loss = 0`；`session_mirror_outbox` 0 行；视图 `request_id` 重复数 0。

641,738 的缺口**被完全解释**：603,509 无 `gw_session_id` + 38,229 按设计排除，
且 36,693 + 1,536 = 38,229 与独立 SQL 逐位相等，`unexplained = 0`。

### 1.2 根因：pre-712 内存 backlog 随重启丢失（§5.3.1）

**推翻了前一版两版结论**（「仍在持续漏写」与「存在旁路写入不触发钩子」）。
历史欠账 1,459 行已由 `mirror_outbox_backfill.sql` 全量补写，outbox 排空 0 行。

### 1.3 本轮新挖的 6 个缺陷

| # | 缺陷 | 状态 | 性质 |
|---|---|---|---|
| 1 | `''::jsonb` 让 4 个端点的语句**在解析期**失败（22P02） | 已修 | 既存，端点 100% 失败 |
| 2 | `SELECT rl.role` 引用 115 列契约里不存在的列（42703） | 已修 | 同上 |
| 3 | `language` 分桶 `\x{...}` 正则在 PG 17.10 报 2201B | 已修 | 同上 |
| 4 | `LEFT JOIN` 触发逐轮列存扫描 → summary 40.5s / compare 4.2s | 已修 | 性能债 |
| 5 | compare 静默丢掉 **17.32%** 的轮次（client_model 为 NULL） | 已修 | 既存 bug |
| 6 | `(request_id, ts)` 配对键**几乎永远配不上**（99.85% 不等） | 已识别，**未改** | 行为变更，待拍板 |
| 7 | **fallback 自毁**：turns 腿被 S4 批次改接成与主腿同源的原生源 | 已修 + 静态门守住 | 回归，潜在（今天暴露面为 0） |

#### 缺陷 7（2026-10-01 新增，优先级高于缺陷 6）

`02c93d04e`（S4 读路径迁移批次）把 `buildRequestLogsFallbackQuery` 的 turns 腿
从 `request_logs_with_current_month rl` 改成 `db.SessionFamilyTurnsForSessionSQL() rl`。
后者展开正是 `session_turns_hot UNION ALL session_turns`，**与主路径
`session_turns_with_current_month` 同源**（该视图 = hot 去重 ∪ parent）。

这条路径存在的唯一理由就是主路径读不到轮次时才被调用；改接之后它去读同一批表，
必然同样返回 0 行 → `generateSummary` 落到 `no turns found for session %s` → **HTTP 500**。

实测（近 3 天窗口，本机 PG 17.10）：

| 指标 | 值 |
|---|---|
| 触发 fallback 的会话（v1 有行、原生源无行） | 1,700 |
| 它们在 v1 里的行数 | 1,802 |
| fallback 自己的 turns 腿返回的行 | **0（1700/1700 全 0）** |
| 全体 v1 会话中触发比例 | 10.87%（1712/15749） |

#### ⚠️ 上面这张表把 internal_loopback / non_terminal 也算成了「v1 的轮次」，结论需修正

按 `db.MirrorDriftClassSQL` 把同一窗口分类后：

| 口径 | 3 天窗口 |
|---|---|
| v1 会话总数 | 15,640 |
| 其中有**业务轮次**（`genuine_loss`）的会话 | 13,980 |
| 纯 `internal_loopback` / `non_terminal`（**不该**被总结） | 1,660 |
| **有业务轮次但原生源完全没有的会话** | **0** |

抽样坐实：`gs_gw_0a0c9d0b…` 的 1,365 轮 **100% 是 `internal_loopback`**
（网关自己生成的标题/摘要 LLM 调用）。触发 fallback 的那 1,700 个会话
**全部**属于这一类。

**所以缺陷 7 是潜在缺陷，不是「10.87% 会话 500」**：fallback 确实自毁，
但今天没有用户可见损失——它触发的全是本来就不该被总结的内部调用。
仍然要修的理由：它在等一个还不存在的会话，而 2026-08-30 加这条路径的
原始理由正是那类会话；放着不管等于把一个坏掉的兜底当成可用兜底。

**推论（比修法本身更重要）**：缺陷 7 在当前数据下**不可能被任何依赖数据的门
发现**。正控实测——把 turns 腿退回原生源形态后，重定基线的真库门**仍然绿**，
因为没有任何一个有业务轮次的会话缺失于原生源，两个源对每个真实会话都一致。
**真正守住它的只有静态门** `TestSessionSummaryV2FallbackTurnsLegReadsV1`
（无需 `TEST_PG_URL`，钉 FROM 来源 + 谓词方向 + 排除谓词来源）。

**既有等价性门抓不到，两道结构性盲区**：
1. 它的「legacy 查询」在 `02c93d04e` 里与生产代码**被同一提交一起改成**同一个
   原生源——等价性只证明两阶段拆分 ≡ 单查询，两边共享的换源对它不可见；
2. 它的探针会话**全部取自 `session_turns`**（「有正文」那组甚至用
   `JOIN session_turns ON t.request_id=b.request_id AND t.ts=b.ts`），
   只能看见原生源有的会话——正是缺陷 7 所服务那批会话的反面。

**新门** `admin/session_summary_v2_fallback_source_integration_test.go`
（`TestSessionSummaryV2FallbackServesSessionsNativeSourceLacks`）双向验证过：
当前代码 **红**（0/5，会话有 1365/345/310/299/266 轮却返回 0）；
把 FROM 改回 `request_logs_with_current_month rl WHERE rl.gw_session_id = $1`
后 **绿**（5/5，2585 轮在范围内）。门当前以红态留在工作树，**未提交**。

**两处修法必须同时做**（只做 turns 腿会把 500 换成「空正文总结」——
88.8% 的轮次仍无正文，LLM 在空文本上编摘要，比诚实的 500 更坏）：

| 修法 | 命中（实测） |
|---|---|
| turns 腿回 v1 视图，配对键仍用 `(request_id, ts)` | 11.207%（1839/16409） |
| turns 腿回 v1 视图，配对键切 `request_id` 单键 | **100.000%**（16409/16409） |

`request_id` 单键安全性已实测：`request_logs_bodies_with_current_month`
2,220,507 行 = 2,220,507 个不同 `request_id`，无重键。

**token 账单**（fallback 人群，3 天窗口，raw body 上限，实际 prompt 是抽取后的子集）：
15 MB / 9,524 B 每轮 ≈ **4.1M tokens / 3 天**（约 1.4M tokens/天）。

#### 排除谓词的方向：`genuine_loss` 是要**保留**的那一类

`db.MirrorDriftClassSQL`（原为 `cmd/gateway/dual_read_validator.go` 的
`package main` 常量，本轮提到 `db` 包做 SSOT）把 v1 行分三类：

| class | 3 天窗口行数 | 含义 |
|---|---|---|
| `genuine_loss` | 14,546 | **镜像钩子本来会写**的行 = 本端点要服务的那批 |
| `internal_loopback` | 1,721 | 钩子按设计不写（标题/摘要生成器自己的 LLM 调用） |
| `non_terminal` | 108 | 钩子按设计不写（in_progress 占位） |

谓词必须写 `= 'genuine_loss'`（保留）。本轮第一版写成 `<> 'genuine_loss'`，
**不报错、不空、端点仍 200**，但留下 1,829 行 —— 恰好 = 1,721 + 108，
正好是该丢的那批，而 14,546 行业务轮次全被扔掉。
唯一抓住它的是分类直方图，不是断言。

由此修正命中率口径：先前报的 11.207% 是在**未加排除**的混合总体上测的；
按类拆开后真相是 `internal_loopback` / `non_terminal` 的 ts **100% 相等**，
而 `genuine_loss`（即真正要服务的业务轮次）只有 **0.007%（1/14,546）**。
所以配对键切换确实必要，先前结论成立。

**教训**：复用一个*诊断口径*的分类表达式做*保留/排除*决策前，先确认标签方向；
方向取反不报错也不空。判方向靠**数**（`GROUP BY cls` 出直方图），不靠标签名。
两个互相矛盾的实测值同时出现时，先怀疑自己刚改的那一处。



缺陷 1–3 的共同根因：**mock 不解析 SQL**。pgxmock 匹配查询**字符串**、
从不把 SQL 交给 PostgreSQL；既有测试只覆盖纯函数（`bucketIndex()`、鉴权、租户），
所以这些路由早已注册、各自失败数周却零告警。

### 1.4 本轮最值得记的一件事：合并不是「无冲突 = 正确」（§5.10）

`git merge` 报「无冲突」，只说明没有**文本**冲突。本轮在合并 S4 批次时：

- **语义混合体**：`session_panorama_handler.go` 把「S4 的 `loadSessionTimelineInTx`
  重构」与「origin/main 给内联版本补的 `rows.Err()`」拼在一起 → `undefined: rows`，
  **origin/main 一度无法编译**。（远端另一会话独立发现并修了同一处，修法逐字节一致。）
- **`warnRowSkip` 丢失**：`session_compare.go` 的 R35-N1 跳行留痕，**两边各丢一次**——
  38d59b2eb 改写 `loadCompareData` 时删掉了它；我 checkout 自己 stash 时因基线更早又抹掉一次。
  唯一发现它的是纯文本守卫 `TestAggReadGuard_MigratedCallersWired`，
  当时编译 / vet / 其余单测**全绿**。

---

## 二、改动文件与关键行为

`65490212d`（14 文件，+1276 / −164）：

| 文件 | 改动 |
|---|---|
| `admin/session_bodies_batch.go`（新） | 批量半连接（`= ANY($1::text[])` / 元组 `IN (SELECT unnest)`）；`sessionBodyQuerier` 让 `*pgxpool.Pool` 与 `pgx.Tx` 共用；`rawToStringPtr`、`strPtrBytes` |
| `admin/session_compare.go` | 两段式拆分（4,183→267 ms）；`*string` + `derefOrEmpty` 修 17.32% 丢失；**补回 3 处 `warnRowSkip` + 2 处 `rows.Err()`** |
| `admin/session_summary_v2.go` | 两段式拆分（40,483→~50 ms）；改用共享 `querySessionBodiesByRequestIDAndTS` + `sessionBody`；`strconv.Itoa` 取代 `Sprintf` 格式串拼接 |
| `admin/session_export.go` | SQL 抽为 `sessionExportMessagesSQL()`；`''::jsonb`→`'{}'`；`rl.role`→CASE 推导 |
| `domains/sessionforensics/export.go` | 同上两处修复；抽 `forensicsExportMessagesSQL` / `…SQLAlt` |
| `admin/quality_correlations.go` | `:183,194` `''::jsonb`→`'{}'`；`:194` language 正则改程序生成的字面量区间 |
| `admin/session_panorama_handler.go` | 删悬空 `rows.Err()` 块 + 无引用 `fmt` import（与远端修法逐字节一致） |
| `docs/audit/2026-09-30-session-request-data-re-audit.md` | 扩至 1,334 行，新增 §5.10 合并实录；§8 第 6 项作废 |

**合并 `317f28556`**：39 文件，+3888 / −404（S4 会话族读路径迁移批次）。
**合并 origin/main**：`907d67b85` 等 8 提交（含 802 回填形态改回递归、routing 审计补洞、
admin ingest 镜像补接 S4 停写门）。802 五点同步经合并后复核仍完整
（两份 SQL md5 一致 + go:embed + StartupFiles + sequence 脚本 + stats parity map）。

### 关键行为变化（都需知悉，不是缺陷）

1. **compare 会多给约 17% 轮次**（原先被静默丢弃的那部分）。
2. `session_export` / `sessionforensics` 的 `role` 由视图列改为 CASE 推导，
   JSON 契约不变。
3. `quality_correlations` 的 `images` / `code_block` 两个分桶从「必炸」变为可用。
4. summary / compare 两条端点的耗时分别降 800× / 16×。

---

## 三、测试命令与结果

```bash
cd __DEV_HOME__/workspace/ai-native-tools/syncfield/llm-gateway-go-4

go build ./...                                                  # ✅
go vet ./...                                                    # ✅
go vet -tags=integration ./admin/ ./domains/sessionforensics/   # ✅
go test ./admin/ ./db/ ./cmd/gateway/ ./internal/sessionv2mirror/ \
        ./internal/sqlreadguard/ ./domains/sessionforensics/ \
        ./domains/sessionsummary/ -count=1                      # ✅ 全绿
cd installer && go build ./... && go test ./... -count=1        # ✅ 13 包全绿
```

7 个真库门（`TEST_PG_URL` 指向本机 `llm-gateway-pg` / `llm_gateway`，PG 17.10）：

```bash
PW=$(docker exec llm-gateway-pg printenv POSTGRES_PASSWORD)
TEST_PG_URL="postgres://llm_gateway:${PW}@127.0.0.1:5432/llm_gateway" \
  go test -tags=integration ./admin/ ./domains/sessionforensics/ -count=1 -timeout 25m -v
```

| 门 | 结果 | 耗时 |
|---|---|---|
| `TestSessionSummaryV2FallbackMatchesLegacyQueryOnRealRows` | ✅ | 172.5s（19 组逐轮对撞） |
| `TestSessionCompareSplitMatchesLegacyQuery` | ✅ | 80.9s（4 组探针） |
| `TestQualityBreakdownQueries_ExecuteOnRealDatabase` | ✅ | 3.9s（5 维度） |
| `TestSessionExportMessagesSQL_ExecutesOnRealDatabase` | ✅ | 0.5s |
| `TestSessionForensicsExportSQL_ExecutesOnRealDatabase` | ✅ | 0.9s |
| `TestSessionSummaryV2FallbackBodiesStaysOnIndexPath` | ✅ | 1.2s（判 EXPLAIN 计划） |
| `TestForensicsExportSQLVariantsStayInSync` | ✅ | 0.0s |

单元守卫（全部经变异验证）：`TestNoEmptyStringCastToJSONInSQLLiterals`（含模式自检）、
`TestSessionScopedReadersUseNativeSource`、`TestSessionBodyPairingKeysMatchTheirCallers`、
`TestSessionBodiesBatchSQLIsNotALefiJoin`、`TestCompareKeepsTurnsWithNullClientModel`、
`TestFallbackTurnKeyIsLocationIndependent`、`TestAggReadGuard_MigratedCallersWired`。

---

## 四、教训

1. **「无冲突」只覆盖文本层**。按「两侧都改过」筛风险区：
   `comm -12 <(git diff --name-only P1 M|sort) <(git diff --name-only P2 M|sort)`。
   本轮 88 个变更文件中只有 5 个是语义混合体候选 —— 但其中 2 个真出了问题。
2. **编译通过是最低门槛，不是正确性证据**。这次运气好，残留的是未定义变量而不是
   一个恰好能编译的错误表达式。
3. **「重构顺手清理」是可观测性的头号杀手**。`warnRowSkip` 在两个互不相干的分支上
   各被删一次；纯文本守卫是唯一抓住它的机制。
4. **「等价」不等于「正确」**。等价性门只能证明「新旧一致」，证明不了「旧的键本身
   对不对」——summary 与 legacy 用了同一个坏键（99.85% 配不上），门照样绿。
   收紧关联键前必须先量命中率。
5. **别用慢查询当测试夹具却不设轮次上限**。夹具成本就是测试预算天花板：
   旧查询 0.8 s/轮时，200 轮就是 15 分钟超时。
6. **外部产出的 SQL 绝不能拼进 `fmt.Sprintf` 的格式串**——`LIKE 'sys:%'` 之类会吃掉
   `%d`，生成 `%!(MISSING)` 残渣。判据要打在**产物的失败特征**上。
7. **守卫写宽会误伤正确代码**：`''::text` 合法（只有 json/jsonb 非法），
  守卫范围必须收窄，并给守卫本身配模式自检测试。
8. **纯文本守卫在「引用是否存在」上可靠**，只是无法区分「在正确位置」与「在注释里」。

---

## 五、遗留风险 / 未做

- **缺陷 7 已修**（本轮）：`session_summary_v2` 的 fallback turns 腿回 v1 视图。
  修法需与缺陷 6 的配对键切换**同时**落地，否则 500 会变成空正文总结。待拍板。
- **⚠️ `session_summary_v2` 长期用几乎全空的正文做 LLM 总结**（§8 第 7 项）。
  改 `request_id` 口径是一行改动，但会改变总结结果与 token 计费 —— **未擅自改**。
- ~~timeline 迭代错误传播无测试覆盖~~ → **已补**（`3834d12f5`，6 条门 + 7 个变异验证）。
- `request_logs_bodies_hot` 上两个功能重复的 `(request_id)` 索引未删
  （已实测事务内 DROP+ROLLBACK 3.940 ms 无回退；需新开 803 走五点同步）。
- 会话内读（class A）已全迁原生源；P1 组仍有 9 个文件走视图，S4 可扛、**S6 时无开关可回切**。
- `session_list.go:140/167` 属 ⚠️ 半等价类，迁过去会让计数变小 2.5%。
- 本轮**未做部署冒烟**；真库门只验证 SQL 与等价性，不代表运行态灰度已验证。

---

## 六、下一轮提示词

```
继续 llm-gateway-go 会话请求数据存储审计接力（ai-native-tools 检出）。

【第一步必做】
git fetch origin && git log --oneline HEAD..origin/main
—— 并行会话活跃（本轮已与 907d67b85 合并），动手前重新锚定，不盲 pull。

【已完成，不要重做】
- 存储可用性复核：genuine_loss=0，641,738 缺口完全解释（§5.7）
- 1,459 行历史欠账回填，outbox 排空
- 四个端点的必错 SQL（''::jsonb / rl.role / language 正则）已修 + 6 个真库门
- summary 40.5s→50ms、compare 4.2s→267ms（两段式批量半连接）
- compare 17.32% 轮次静默丢失已修（*string + derefOrEmpty）
- S4 批次已合并，合并期挖出的 2 处语义缺陷已修（§5.10）
- 会话内读 class A 已全部迁原生源

【下一轮优先】
0. ~~缺陷 7 与 §8 第 7 项~~ → **本轮已拍板并落地**（`09419da13`，已入 origin/main）：
   turns 腿回 v1 视图 + 排除谓词（方向 `= 'genuine_loss'`）+ 配对键切
   request_id 单键。真库门实测方向性收益：169 轮里元组键只配上 1 轮、
   单键配上 169 轮。
1. 等 §8 其余五项拍板。
2. ~~补 timeline 迭代错误传播的钉测~~ → **已完成**（`3834d12f5`）。
3. 若获批 S4 真机灰度：开 storage.request_logs_write_enabled=false，
   用 GET /api/admin/sessions/dual-read-drift 盯 s4_ready；
   灰度期必须保留 734 视图的 v1 冻结分支。
4. **本轮留下的一个开放项**：`session_summary_v2` 的 fallback 现在能服务
   业务轮次了，但今天这样的会话实测为 0（见 §1.3 的修正表）。也就是说
   这条路径的**行为变更（正文内容会变、token 账单会涨）尚无真实流量验证**。
   若要继续观察，最省事的办法是等一个 v1-only 但有业务轮次的会话出现，
   再看 `GET /api/admin/sessions/{id}/summary` 的 `turns_analyzed` 与正文。

【本轮（第二轮）已完成】
- §8 第 7 项 + 缺陷 7：已拍板落地（`09419da13`）。
- §8 第 2 项：loadSessions 迁原生源（`4baa94dfb`）。消失的 1,428 条全是内部调用。
- §8 第 5 项：已由迁移 807 落地（保留 phase 2 依赖的唯一索引）。
- §8.3 雪崩止血：正文取数并发闸（容量 4，饱和快速失败 503）。
- §8 第 1 项（S4 真机灰度）：**本轮再次维持待批**。
- §8 第 3 项（analytics/dashboard 口径）、第 4 项（641,452 无会话头 request_id
  处置）：仍等决定。

【§8.3 未修的 P0，下一轮优先】
正文取数（`sessionBodiesByRequestIDSQL`）实测 **17~19 秒/会话**，计划落在
2026_09 列存分区的 ColumnarScan 上。连带发现 **session_compare 的 10s 预算
已经超时**（10.0s 只取到 104/171 行）。连接池只有 16。
本轮只加了并发闸（4 并发，饱和 503）；**查询形态本身没动**，4 个候选修法
（分区/索引、拆查询、超时熔断、正文换源）及其代价在审计报告 §8.3。

> 🕘 **2026-10-02 订正：上面这段已经过时。** §8.3 的 P0 由 `7d6cfcbd8`（10-01 10:26）
> 闭合——`= ANY(数组)` 换成 `IN (SELECT unnest)`，根因是前者让规划器放弃主键
> （N=1/2/4/…/64 全部 ColumnarScan），修后 15.9~17.4s → 2.2~4.2s。
> 本 handoff 定稿于 10-01 07:27，早于该提交，故保留了「未修」的旧陈述。
> **教训**：同一份文档里「未修」与「已修」只能有一个是对的；跨会话推进时，
> 引用上一轮的开放项前必须重新核提交历史，不能照抄（这与 §三「登记不修」里的
> 断言也要现勘是同一条）。

【本轮新增纪律】
- **别拿上一轮的口头性能数字当事实**：`phase 2 7~501ms`、`compare 267ms` 都是
  迁移 765 把 2026_09 转列存**之前**的测值。用 pgx 绑定参数跑生产 SQL 重测是
  17~19 秒。**类比：影响面数字同样要按分类口径重测**（`session_list` 的
  「计数变小 2.5%」实为「1,428 条纯内部会话消失」）。
- **断言的锚点字符串必须真实出现在产物里**。性能门守着「phase 2 不得列存全扫」，
  判据找 `"ColumnarScan on request_logs_bodies"`，而真实节点行是
  `Custom Scan (ColumnarScan) on request_logs_bodies_2026_09` —— 中间有括号。
  **用那个子串在它自己的计划输出里 grep 命中 0 次**，所以这道门自诞生起恒绿。
  配套教训：它还跑的是裸 `EXPLAIN`（不执行），于是「这条分支多贵」从没被问过。
- **闸（限流/信号量）默认语义应当是快速失败，不是排队**。排队 = 把慢放大成雪崩。
  门要钉「饱和时 N ms 内返回错误」，否则实现改成排队时门会挂死并报红。
- **迁移类的门要钉方向性不变式，不要钉等价性**。`session_list` 迁移本来就
  不等价（旧源多 9.46% 的内部会话），钉等价会让门永远红；钉「少掉的必须是
  内部调用，出现真业务轮次即报红」才对。
- **`db.MirrorDriftClassSQL` 硬编码别名 `rl`**，调用方 FROM 别名不一致会报
  42P01。本轮踩了两次（admin 的真库门里）。
- 测试里「中途释放 + defer 兜底释放」同一个槽位会**双重释放**并永久阻塞在
  `<-chan`。本轮挂了一次 120s 超时才发现。release 若可能被多次调用，包一层
  `sync.Once`。

【原有纪律（仍然有效）】
- **复用一个「诊断口径」的分类表达式做「保留/排除」前，先确认标签方向。**
  `genuine_loss` 读起来像坏行，实际是「镜像钩子本来会写」= 要保留的那批。
  写反成 `<>` 不报错、不空、端点仍 200，但把 14,546 行业务轮次全换成
  1,829 行内部调用。判方向靠数（`GROUP BY cls` 出直方图），不靠标签名。
- **影响面数字必须按分类口径报。** 本轮先报了「10.87% 会话 500」，
  按 `genuine_loss` 拆开后是 **0**。两个数字都"实测"过，错的那个口径
  把 internal_loopback 当成了 v1 的业务轮次。
- **正控「没红」要当真信号。** 把 turns 腿退回缺陷 7 形态后真库门仍然绿，
  因为两个源对每个真实会话都一致 ⇒ 该缺陷在当前数据下不可能被依赖数据的
  门发现。守住它的是静态门 `TestSessionSummaryV2FallbackTurnsLegReadsV1`。
  这个结论已写进门注释，别让后人以为真库门在守它。
- **探针的选样条件必须和被测代码的过滤条件同口径**，否则会挑中一个「代码
  正确地什么都不返回」的人群，让门对正确行为报红（本轮踩过两次）。
- **测试「发现」阶段也烧预算**：不带时间窗的全量 GROUP BY 实测 5 分半未完成，
  客户端超时后**服务端查询仍在跑**（要 `pg_cancel_backend`）。夹具查询要自己
  量成本，并且探针按轮数设上限（legacy 单查询约 0.8 秒/轮）。
- 改动关联键前先量命中率（一条 join + count FILTER 足够），不要推断。
- 合并后必须重跑全量门禁再提交；编译全绿不等于合并正确。
- 守卫改动必须做变异验证；判据打在产物上，不打在被测对象之外的文本上。
- 性能数字与数据前提分别验证，不接受「实测如此」的口头结论。
- **照抄上一轮的开放项之前必须重新核提交历史**（§8.3 那段就是这么失真的）。
- **一个「给出许可」的门，必须能区分「验证通过」与「没验证」**。恒假的门
  （§8.8 的 `ZeroDrift`）和恒真的门（`s4_ready`）同样无用，而且都要命：
  恒真会放行不可逆操作，恒假会让 spec 的退出条件永远无法宣告完成。
  形状固定为 `ready ⇔ (前提1) ∧ (前提2) ∧ (结论)`，并另设 `void` 字段 +
  机器可读 reason；把两者折成同一个 false 等于把「不知道」伪装成「不安全」。
- **空扫描不是「零发现」**。判据里要有「这次扫到东西了吗」这一条，
  且它要**数据无关**地总被求值（§8.8 的 I5c）。
- **禁子串这种守卫写法在 Go 里几乎没有安全形态**：`sum.S4Ready =` 会命中自己
  修好的 `sum.S4Ready = verdict.Ready`。正确形态是**数赋值点 + 核对右值**。
  被自己的门拦下是好事（说明它在跑），但要立刻把守卫改精确，而不是放宽它。
- **全仓扫描型守卫要把基准路径当断言钉住**；第一次跑出的「违规清单」先核对
  目录归属（是否在本仓库内），再决定改守卫还是改代码。
- **`gofmt -w <目录>` 会扫到你没碰过的文件**。本轮为格式化自己新写的文件
  执行了 `gofmt -w cmd/gateway/`，把该目录下 **12 个**与本任务无关的文件
  一并重排（其中几个是并行会话的在途改动）。提交前 `git status` 才暴露出来。
  正确做法：`gofmt -w <精确文件列表>`，或 `gofmt -l .` 只做检查。
  **判据**：`git status --porcelain` 里出现你**没打算改**的文件，就是批量命令
  的范围写宽了——这与「批量还原事故」同源，一个方向误伤源文件，一个方向误伤格式。
```
- **门控只覆盖系统的一部分时，去找跨越边界的交互**（§9.9）。S4 的门在**写侧**，
  于是「读 v1 去决定写什么」的读点天然在门外——读端五档（200/500、空不空、
  冻不冻）**全绿也不代表停写安全**。`bg/credential_recovery.go` 填成
  `silently_empty` 甚至不算错，但它丢掉的是凭据恢复写入的授权来源。
  推论：**新增一道门时先问它守的是哪个坐标系，以及门外还有什么坐标系。**
  只补条目不补坐标，缺口会在下一轮换个名字重新出现。
- **判「活的控制面依赖」之前，必须先查消费方是否真的被调用**（§9.9.3 自我更正）。
  `credentialstate/popularity_tracker.go` 结构上完全符合 live（停写后每个 tick
  把 `popularModels` 抹成空 map，探针间隔从 10s 退到 5min = 30 倍衰减），
  核实后是 **dormant**：`LLM_GATEWAY_ENABLE_POPULARITY_TRACKING` 默认 false，
  且唯一输出 `GetRecommendedProbeInterval` 全仓无生产调用方。
  **假的风险会稀释真的风险**——而 `credential_recovery` 就是真的那条。
  反向同样成立：`consumer` 存在不等于它在门内，`ursmRecoverSink` 存在且活着，
  也正因为如此才危险。
- **子代理的判定要复核，但复核的产物是「更窄的真结论」，不是「接受/否定」**。
  batch4 报「每个请求静默新开 gw_session_id」——方向对、量级错：DB finder 是
  兜底不是主路径（Redis `LastSystemSessionIndex` TTL 5min 才是主路径且不受门控），
  准确的失效条件只有两条（无 Redis 部署 / Redis 索引 miss）。
  直接接受会造出一个不存在的风险，直接否定会丢掉一个真的洞。
- **推 `main` 时不要 rebase 主工作区**。并行检出下主工作区可能有别的会话正在
  写的脏文件，rebase 会拒绝或覆盖它的编辑。安全做法：
  `git worktree add -b <tmp> /tmp/<dir> main` → 在临时 worktree 里 rebase →
  `git push origin HEAD:main` → `git update-ref refs/heads/main <new> <old>` →
  `git reset -q`（**mixed reset 不写工作区**，只同步 index）。
  逐文件核对 blob（`git rev-parse <sha>:<file>`）确认 rebase 没改变你的内容。

---

## 2026-10-02 追加：§9.18 视图源越列（读端 105/105 之外的缺陷类）

### 做了什么

闭环 §9.17.4 遗留第 1 条。`admin/credential_monitor_heatmap.go` 引用 `rl.origin_stage`，
而该列不在 `request_logs_with_current_month` 的 113 列冻结契约内（真库 information_schema
实测 0 列，物理表与 `session_turns` 各 1 列）⇒ 凭据质量热图**线上 500 至少两周**
（自 R50 于 2026-09-21 引入算起），且因 `exclude_self_test` 缺省 true，裸调用与前端调用同走
必错路径。

改动：把 bg 的视图变体谓词**导出**为 `ProbeTrafficExclusionPredicateView`（全仓单一拼写），
热图删掉内联三臂改用它。新增四道门 + 42 列清单文件。详见审计文档 §9.18。

### 这一轮真正学到的东西

- **「守卫清单是包内清单」是个隐形洞。** R49 已经把「物理表谓词对视图必 42703」识别并修好，
  还写进注释；R50 又在 admin 包内联回去，而 R50 的调用面守卫文件列表只有 bg 包 7 个文件。
  ⇒ **同一个陷阱可以在同一个仓库里复发，只要它发生在守卫的扫描范围之外。**
  任何「按文件清单枚举」的守卫，都要问一句：这份清单是怎么来的、谁保证它全。
- **修一个列名不够。** 原谓词第二臂 `NOT ('probe' = ANY(quality_flags))` 缺 COALESCE，
  而视图对 session 臂的 `quality_flags` 做了 NULL 补位 ⇒ `NOT NULL` 不是 TRUE，
  **40,225 / 40,275 行 session 分臂被静默丢弃**。只补列名会得到一个「返回 200、
  但对整个已迁移数据集全盲」的热图，比 500 更难发现。
  **判据要打在效果上（判决是否依赖 NULL），不是打在拼写上。**
- **机械判据的假阳性会把它自己变成死代码。** 这次全仓 AST 门返工了四轮：
  per-decl 太粗（13 假阳性）→ 逐字面量（4 假阳性）→ 没剥 SQL `--` 注释（误伤
  `bg/shared_pick.go`，那里 `origin_stage` **只**出现在解释「不能用它」的注释里）→
  没判列绑定到哪张表（`rb.outbound_body` 绑 bodies 视图、`AS task_id` 是输出别名、
  `b2.task_id` 的 b2 是 CTE 别名）。**每次假阳性都是「门宽了」而不是「代码错了」。**
- **门自己也会被自己的注释弄红。** 新写的源码钉桩门首跑即红，因为修复注释里**引用了**
  那个坏字面量来解释它为什么错。必须先 `stripGoComments` 再匹配。
  写反例注释的代价是让门看起来像在制造噪声。
- **新门第一次运行就抓到了清单外的新真缺陷**：`admin/compression_stats.go:212` 的
  `token_band` 同样不在视图契约内，且错误被 `slog.Warn` 吞掉 ⇒ token 分带聚合长期静默空。
  它此前不在 105 条读端清单里，因为**没有任何机制会去查**。

### 变异验证（4 次，全部被门抓住）

| 变异 | 被谁抓住 | 证据 |
|---|---|---|
| 越列 `rl.origin_stage` 混入视图字面量 | AST 门 | 定位到 `credential_monitor_heatmap.go:337` |
| 清空具名豁免表 | AST 门 | `compression_stats.go:212` 重新报出 |
| 还原 R50 原始拼写（代码内） | 源码钉桩门 | 两条断言同时命中 |
| 共享常量抽掉 `COALESCE(...)` | 真库门 | **9,154** vs **41,730** 行 |

最后一条与独立量测互相印证（单独量旧谓词 7 天存活行 = 9,424，量级一致）。
**两把量具不是同一把——这是数字能被采信的前提。**

### 待你拍板（新增第 3 项）

1. **`RawModelName` 补齐 + 恢复 SQL 端口的范围**（仍未决，阻塞 `credential_recovery` 修复）
   - (a) 补数据源 + 端口 SQL（动热写入路径 + 需历史回填）
   - (b) 只补数据源，端口留到 S4 灰度前
   - (c) 改用门控（改动小、当天可落地，代价是停写期降级凭据只能等自身探针）
2. **`origin_stage` 线上 500 是否现在修** —— **已在本轮修完并推送**（含
   `compression_stats` 的 `token_band` 同族缺口登记为具名豁免）。
3. **`token_band` 怎么随迁**（新增）。它与 `raw_model_name` 同属「物理表独有列未随迁」
   缺口类：正解是把该列随迁进 `session_turns` + 710 投影；改成读物理表会丢 session 分臂，
   与 S4 方向相反。要么并入决策 1 一起做，要么接受仪表盘这一格长期为空。

### 遗留（不阻塞本轮，但阻塞 S4）

- `credential_recovery` 写授权缺陷未修（`NOT EXISTS` 恒真那类，已修的是 `discovery`）。
- **跨字面量运行时拼接的越列仍无静态门**。静态判定天花板就在这里——热图那个 case 正是
  视图引用与谓词分属两条字面量、运行时才拼起来的。已知形状要么靠真库门覆盖，要么上真正的
  SQL 解析器。
- 读端 74/105 静默退化未处置 ⇒ S4 灰度方案必须自带对账，不能「看接口是否报错」。
- `admin/session_tenant.go` 判 unaffected 依赖的假设（三条 session 腿对新 task 是否都及时
  落行）仍未实测。

---

## 2026-10-02 追加（第二轮）：§9.19 两条待办收口

### 修了一个真的结构性误报机：S4 停写 → 对账器报假账

`usageCreditSQL()` 用 `FULL OUTER JOIN` 比对 **门内**的 `request_logs_hot.credits_charged`
与 **族外、且永不停写**的 `credit_ledger_hot`，而 `bg/ledger_reconciliation.go` **完全不咨询
S4 门**。停写一生效：usage 臂冻结、credit 臂继续增长 ⇒ 切换点之后每个请求都落进
「只有 credit」分支（charged=0 / debited>0），被写进 `maas_reconciliation_findings`。
**那些不是账务缺陷，就是停写本身**，且无上界。

同文件的 `balanceChainSQL()` 只读 `credit_ledger_hot`、不跨族 ⇒ **只挡一项**，
把它一起挡掉就是拿假报机换静默洞。

修法沿用本审计已建的 `staleExpiryMayRun` 先例：纯函数
`usageCreditComparability(logsWriteEnabled) (bool, string)` + 稳定原因键
`s4_stop_write`，在**发查询之前**短路；新增 `SkippedChecks()` 把「跳过」与
「扫了没发现」分开（两者返回的 0 在计数上无法区分）。

### 这一轮最该记住的：我自己的守卫失败了三次

1. **判据打在错误的 `return` 上** —— 用「函数体内第一个 `return 0`」判短路；删掉 skip 分支的
   return 后，它匹配到了后面查询错误处理里的那个 ⇒ 门照样绿。**变异验证救了这条命**：
   门是绿的，但变异是红的，这才暴露出判据钉在了错误的节点上。
2. **只看 `IfStmt.Cond`** —— 实际写法是 `if ok, reason := f(...); !ok {`，调用在 **Init**，
   于是门在**正确代码上**报「门不存在」。
3. **测试执行了它声称要验证的那一步** —— 跳过列表的重置测试**手工**执行了 `r.skipped = nil`，
   所以把 `RunOnce` 里的重置删掉仍然全绿。抽成具名方法 + AST 钉住调用位置与先后。
4. **崩溃被当成断言命中** —— `ast.Inspect(nil, …)` panic（`IfStmt.Init` 可为 nil），
   变异 A 的「红」其实是崩溃。差点把一次无效的变异验证当成有效证据。

五道变异（抽掉门 / skip 不 return / 门恒 true / 删重置 / 重置挪位）全部被正确抓住。

### 修正了我自己的一处判断（42 列按可修性二分）

§9.18 说 `token_band` 要「随迁进 `session_turns`」—— **错了**。真库差集：

- **A 类 5 列**（`origin_stage`、`token_band`、`client_forwarded_for`、`trace_events`、
  `upstream_protocol`）：**`session_turns` 里已经有，只是 710 视图没投影**
  ⇒ 纯 `CREATE OR REPLACE VIEW` 投影即可，无回填、不动写路径。
- **B 类 37 列**：其中 23 列已在 `session_turn_details`（733 特征层，61 列、在写）
  ⇒ 真正缺列的只剩 **19 列**。

顺带一个结构事实：**`session_turns` 根本没有 `gw_task_id`**，任务关联只存在于特征层。
⇒ 若要补 A 类那 5 列，**特征层才是对的任务关联源**，这是改 710 投影时必须先定的语义。

仍不擅自改 710：那是共享契约变更（pin 要同步），且 A 类含 `origin_stage` ——
**投影它等于把本轮刚修掉的那条越列路重新打开**，必须同时把所有视图读方切到视图变体。

### `assertTaskInTenant`：我原来问错了

遗留问的是「三条 session 腿是否都及时落行」，但那是 OR-of-EXISTS，任一命中即放行，
「三条都落」从来不是不变量。真正的问题是「**有没有 task 五条腿一条都没落**」。

实测：30 天内 `request_logs_hot` 7 个 distinct task，6 个 session 族未覆盖，
**五条腿全未覆盖 = 0**，门当前不会误拒。

**但面向终局有真约束**：门依赖 `request_logs_hot` + `request_logs` 两条 v1 腿，
而终局目标正是删掉这两张表 ⇒ v1 退役后「只在 v1 留痕」的历史任务会对**所有人** 404
（权限门翻转成阻断所有人）。**S4 退出判据必须包含「v1-only 历史已回填进 session 族」。**
