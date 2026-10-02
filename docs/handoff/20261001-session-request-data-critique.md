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

---

## 2026-10-02 追加（第三轮）：§9.20 迁移成本从「42 列」压到「4 个投影」

补上了前两轮一直缺的后半问：**这 42 个「物理表独有列」里，哪些真的有人在读？**

| 类别 | 数量 | 结论 |
|---|---:|---|
| 被 SELECT 读、且 `session_turns` 已有该列 | **4** | `origin_stage`、`token_band`、`client_forwarded_for`、`trace_events` |
| 被读但已有别的 session 落点 | 1 | `outbound_body` —— 从不从 `request_logs*` 直读，全走 bodies 视图族 / `session_bodies_unified` |
| 只写不读 | 5 | 5 个 token 指标列，只见于 INSERT/UPDATE 列清单与 Go 结构体 |
| 全仓无任何 SQL 引用 | **29** | 零迁移成本 |
| 名字撞车 | 3 | `cache_hit`→`dashboard_access_events`；`session_summary`→`approval_requests`；`task_id`→十几张任务表 |

⇒ **待决范围只剩一句话：给 710 视图补 4 个投影。** 数据已在 `session_turns`，
**不需回填、不需动写路径**，只需一条 `CREATE OR REPLACE VIEW` + 同步 pin。
今天唯一的真实消费方是 `admin/compression_stats.go:212`。

**硬前提**：补 `origin_stage` = 把 §9.18 修掉的 500 路径重新打开，必须与
「所有视图读方切到 `bg.ProbeTrafficExclusionPredicateView`」**同批提交**，
`TestNoPhysicalOnlyColumnsInViewSourcedSQL` 会在任何一处遗漏时转红。

### 方法学（这轮踩到的）

- **扫描器输出是嫌疑清单，不是结论。** 它把 `turn_writer.go:366` 的 `token_band`
  **INSERT 列清单**误判成视图读取，差点被读成「第五处 42703」。手验 5 处视图读点后
  确认全部只用身份列，无越列。**每次「抓到新缺陷」都要问：这是读方还是写方？**
- **列名撞车是真实噪声源。** 只按列名统计引用量会**高估**迁移面（`task_id` 在本仓
  十几张无关表上都有）。必须先判「这个引用绑到哪张表」——与 §9.18.8 的教训同源。
- **「无 SQL 引用」比「有几处引用」更有决策价值**：29/42 无人读，
  于是真正要迁移的只有 4 个。此前把 42 列整体当迁移面是**高估**。

---

## 2026-10-02 追加（第四轮）：§9.21 v1 退役爆炸半径 = 66 个直读方，39 个不能直接改指视图

§9.20 解决「缺哪几列」，这一轮问退役的另一半：**还有多少读方绕过视图直读 v1**。

| 分类 | 文件数 |
|---|---:|
| 绕过视图直读 v1 宽族 | **66** |
| └ 读了 session 臂恒 NULL 的补位列 ⇒ **不可直接改指视图** | **39** |
| └ 不读补位列 ⇒ 改指视图无列可用性障碍 | 27 |

**口径精度声明**：判据分不出 SELECT 与 `UPDATE...FROM`/`ON CONFLICT`，
所以 66 是**上界**（含写路径）。按量级用，不要当精确条数。

**这不是 39 个缺陷，是 39 个需要重写的读面。** 它们今天都正常（读物理 v1、列齐全）；
危险在**改指视图那一天**——session 分臂的行会从这些列拿到 NULL 而接口照样返回 200，
即 §9.18 的「修好了但变全盲」。

安全改指的判据：**读的每一列要么不在 30 列补位清单里，要么本来就带回落到 session 侧
等价列的 COALESCE。** 目前只有 2 列有 710 文档明载的等价映射
（`client_model`→`outbound_model←model`、`attachments`→`has_attachments←attachment_count`），
其余 28 列**没有已登记的等价映射**，需逐列语义裁决。

### 反直觉的一条：`id` 不能顺手加进投影

真库实测 `session_turns` **有** `id` 列。但 v1 的 `request_logs.id` 是**请求行 id**，
session 侧是 **turn id** —— 不是同一个东西。视图把它补位成 NULL 很可能就是刻意的。
⇒ **判据是「session 侧的列与 v1 侧的是同一个东西」，不是「session 侧有这个列」。**
`origin_stage`/`token_band`/`client_forwarded_for`/`trace_events` 满足，`id` 不满足。
**建议由你确认这条判断。**

### 于是 S4 退出判据是三条，缺一不可

1. 写入面：v1 写路径全并入门控，停写稳定期 ≥ 一个 hot retention 窗口（当前 8h）。
2. 读面：39 个补位列读方逐个改视图读法（去掉依赖或改 COALESCE）。
3. 历史面：`assertTaskInTenant` 依赖两条 v1 腿 ⇒ v1-only 历史须先回填进 session 族。

另：`cmd/gateway/dual_read_validator.go` 读补位列 `request_type` ⇒ 停写后两侧不可比，
同样要纳入判据。

---

## 2026-10-02 追加（第五轮）：§9.22 把 39 个读方变成**有门**的工作项

§9.21 的 66/39 来自 `/tmp` 扫描器，无回归保护。数字驱动 S4 退出判据，
**没有门的数字会随代码演进而静默过期**。已落成
`admin/v1_direct_padded_column_reader_test.go`。

口径：一条 SQL 字面量的 `FROM/JOIN` 集合含 v1 宽族、且同一字面量里没有 canonical 视图
⇒ 绕过视图直读；再读 30 列补位集的任一列 ⇒ 进登记表。**不是禁止**（这些读方在 v1
存活期间是正确代码），是「**默认未登记 = 需要有人拍板**」。登记表由**门自己的输出
生成**，登记口径与执行口径不会漂移。

### 门自己漏过一次，被变异验证逼出来

第一版只判「文件在不在表里」。给 `admin/analytics.go` 追加一个 `client_model`
过滤条件 ⇒ 门不响。不是实现 bug，是**文件级粒度看不见「已登记读方又多读了一列」**。
⇒ 登记表升级为记录**列集合**，并加两条判定：新读列 ⇒ 红；登记列不再被读到 ⇒ 判失效。

**教训**：第一版「已覆盖 39 个文件」听起来完整，实际只覆盖了「文件级」一个维度。
**覆盖率的单位要和风险的单位一致**——这里风险是「哪些列要重写」，粒度必须是列。

另记一次**变异设计错误**：最初给 `admin/analytics.go` 加的是又一个 `client_model`，
而它本来就读 `client_model`，列集合没变，门正确地不响——**是我把变异设计坏了，
不是门宽了**。换成它没读的 `gw_task_id` 后门立刻响。
**门不响时先怀疑变异，再怀疑门。**

六道变异全部被正确抓住（新读列 / 清空表 / 登记失效 / 未登记文件新增读方 / 列集合收缩 /
同时两条失效各自报对路径）。
`/tmp` 扫描器与仓库内这道门是两套独立代码路径，都报 39——数字一致（弱证据，
思路同源，但足以排除「39 是扫描器假象」）。

### 门的边界（写在门上）

分不出 SELECT 与 UPDATE...FROM ⇒ **39 是上界**；只覆盖绕过视图的读方
（已在读视图的 105 条由 s4audit 门覆盖，两门互补不重叠）；**不判断语义等价性**
（`client_model`≡`outbound_model`? `id` 与 session 侧 `id` 是不是同一个东西?），
仍需人工裁决。

### 第六道变异暴露的自身缺陷（通读时发现，不是测试发现的）

失效报告把**原因字符串**收进 slice、排序后拿**路径**去配对 ⇒ 多条失效时会把
`A` 的原因报到 `B` 头上。变异 5 只有一条失效，**侥幸是对的**——我差点把它当成
「变异通过」的证据。改为 `(路径, 原因)` 成对收集、按路径排序后用变异 6
（同时制造两条失效）验证。
**单条样本通过不构成「多组也正确」的证据。**

---

## 2026-10-02 追加（第六轮）：§9.23 第三处跨门边界检查（**并纠正了我自己旧结论**）

§9.19.1 我查完对账器就下了「其余四处都对」的结论。这次把同族扫描重新逐个核实，
**又查出一处**，并且**方向与我此前的记录相反**。

### 我此前说反了

我记录的是「`credential_recovery` 的 `EXISTS` 陈旧证据**持续放行** URSM v2 恢复写入」。
重读代码：`lookbackCandidateSQL` 是 **SELECT-only**（注释明写不写 `cmb.available`），
门控的是**候选产生**。所以停写后的失效方向是：

> 证据源冻结 → 窗口内不再有新行 → `EXISTS` 恒空 → **36h 后没有任何绑定再进候选集**
> → 降级/离线绑定在这条路径上**永久失去恢复机会**，而扫描照常返回空、无痕。

这是**静默洞**（漏恢复），与 §9.19 的假报机方向相反，**但结构完全同形**。

**教训**：我上一轮给出的是「其余四处都对」这种**扫过就没事**的结论，而没有把同族
扫描重新逐个核实。**同一个缺陷模式，扫过一次不等于扫完。**

### 逐项核实：只门控一条

| 路径 | 证据源 | 跨门 | 停写后 |
|---|---|---|---|
| `lookbackCandidateSQL` | `request_logs_hot` ∪ `request_logs` | **是** | 36h 后恒空 ⇒ 静默洞 |
| `expiredCmbRecoverySQL` | `node_probe_state` | 否 | 仍可判定 |
| `recoverFreshDegradedSQL` | `node_probe_state` | 否 | 仍可判定 |

**把后两条一起挡掉就是拿静默洞换静默洞。** 门为此专门加了一条反向断言。

六道变异全部被抓住（抽掉门 / 不 return / 不 markSkipped / reset 挪到门后 /
reset 挪进门内 / 误门控兄弟路径）。

### 门红了：先问「在验证性质还是在验证形状」

`resetSkipped()` 我放在 gate **之前**（正确），但断言要求它在 `gate.Body` 内 ⇒ 门红。
**实现对、门红、断言错**：重置若在门内，只有跳过的轮次才清空清单 ⇒ 真正执行、
真正「没找到候选」的轮次会继承上一轮的「已跳过」判决。判据已改为
「重置在门之前、且不在门内」。这是同一族教训的第三种形态。

### 分工要说清楚

本轮修的只是**门控**（停写期间「停止并明说」）。**证据源仍只在 v1** ——
`session_turns.raw_model_name` 实测 0/1,682,828 填充，搬到 session 族仍是
§8 决策 1 的范围问题，**本轮没解决**。
**门控防「不可判定被读成空」，迁移解决「证据从哪来」，两件事别混。**

---

## 2026-10-02 追加（第七轮）：§9.24 跨门边界的**第三个失效方向**

前两轮都在提醒「别只按文件名分组」，但**口径本身还是窄的**：我是沿着
「谁咨询了 `RequestLogsWriteEnabled()`」去扫的。真正中招的检查**不需要咨询门**。
按更宽的口径重扫（66 个 v1 直读方 → 13 个带周期驱动），得出第三个方向：

| 方向 | 机制 | 实例 | 危险面 |
|---|---|---|---|
| ① 两侧同期比较 | 门内冻结 vs 门外增长 | `usageCreditSQL`（§9.19） | 假报机 |
| ② 证据缺失 | 证据源冻结 ⇒ 候选集恒空 | `lookbackCandidateSQL`（§9.23） | 静默洞（漏恢复） |
| ③ **过滤臂在门内、驱动表在门外** | 驱动表继续收新行，过滤谓词恒真 ⇒ **不再过滤** | `auto_route_affinity_worker`（本轮） | **过度纳入 / 模型污染** |

方向 ③ 最隐蔽：**它不会让任何东西变空**，只会让本该剔除的数据混进来 ——
既没有「0 findings」信号，也不触发任何空结果告警。

### 实测把这条挖得更深

亲和度聚合的两条合成流量排除臂读 `request_logs_hot` / `request_logs`（都在门内），
而驱动表 `auto_route_selections` 的写方 `selection_writer.go`
**实测 0 处 `RequestLogsWriteEnabled()` 调用**。停写后过滤恒真 ⇒ 合成流量进聚合。

对 131 条合成请求逐条查 v1 覆盖：

| 覆盖来源 | 覆盖数 |
|---|---:|
| `request_logs_hot` | **0** |
| `request_logs`（母表） | **131** |

⇒ **今天唯一兜住过滤的是母表，而那正是要删的那张表**（热表臂覆盖 0 条，
这些合成流量早已 promote 出热窗口，今天就是死臂）。v1 退役后过滤覆盖率归零。

### 修法：纯增量 + 必须钉住

保留原两条 v1 臂（双写期行为不变），新增第三条指向 `session_turns` 的臂。
**正因为它今天是 no-op（本地 564 条 selections 三条臂都是 564 通过），
它最可能被下一个人当冗余删掉** —— 所以配套的门是改动的一部分，不是附加品。
四道变异全部抓住（删 session 族臂 / actor 集合漂移 / 顺手删 v1 母表臂 / 驱动表改写别处）。

### 顺带一条证伪

扫描把 `bg/lite_retention_worker.go` 列进 v1 直读方（它有 `DELETE FROM request_logs`），
但那是 **SQLite 本地库**（`rowid` + `?` 占位符 + 注释里的「持 SQLite 写锁」），
与 PG 侧的 S4 门无关。**关系名相同不代表同一个存储面** —— 与 §9.20 的
「列名撞车」同族。已排除，未改动。

---

## 2026-10-02 追加（第八轮）：§9.25 剩余周期检查定性 —— **多数不该门控**（纯文档，零代码改动）

§9.24 重扫留下 11 个「带周期驱动 + 绕过视图直读 v1」的检查，2 个已修。本轮逐个定性。

**结论先说：这 11 个里只有 1 个真该门控，其余 10 个门控会造出新的静默洞。
所以本节不动代码，只交付分类与理由。**

可复用的分诊判据：

> 该门控 ⟺ 停写后这条检查会**做出错误结论**（假报机 / 漏判 / 过度纳入）。
> **不该**门控 ⟺ 停写后它只是**基于陈旧输入继续给出保守结论**，
> 或者它服务的**目的在停写后自动变得平凡为真**。
> 前者门控是**止错**，后者门控是**止对**。

把前三个已修的门控当模板套到其余 10 个上，会制造 **5 个新静默洞**：
自检、today_success_probe、model_tier、model_probe 一旦被门控，
停写后**探针与自检这两条「主动发现故障」的能力会整体消失**，
而它们本来不依赖被冻结的证据（探测结果写在 `node_probe_state`，不受门管）。
**停写是为了停日志写入，不是为了停故障发现。**

三档分诊结果：
- **无缺陷**：`auto_index_refresher`（冻结 ⇒「索引是当前的」变成**真命题**）、
  `popularity_tracker`（dormant）、`lite_retention_worker`（SQLite，不在范围）。
- **陈旧但保守 ⇒ 不门控**：`recentUsageModels`、`today_success_probe`、
  `model_tier` / `model_probe` usage scan、`anomaly_harvester` 的富化回填。
- **陈旧但仍在继续 ⇒ 该加陈旧标记而非门控**：`auto_route_settle_worker`
  （驱动表 `auto_route_selections` 不受门管继续收新行，基线来自 v1 会冻结）。
  **这比「整条停掉」更难察觉。** 本轮只定性未实现——加标记要改 HTTP 响应契约，
  属需拍板的范围变更。

**唯一该做而未做**：`credential_selfcheck` 的 `last_error_at` 臂。
它的 `recentUsageModels` 臂只是范围陈旧（不该门控），但 `last_error_at` 臂是
「该凭据最近是否报错」的**唯一信号源**，只来自 v1 ⇒ 停写后**新发生的真实失败
不会被检测到**（错误本身会进 `session_turns`，但这条查询不去那里读）。
与 §9.23 同形、修法同形，但需先决定「停写后错误检测的证据源」——正是 §8 决策 1
同族。**门控只能让它可见，迁移才能让它正确。**

## 2026-10-02 追加（第九轮）：§9.26 `credential_selfcheck` 错误臂补 session 族（**收口第八轮唯一遗留**）

第八轮定性说「11 个里只有 1 个该门控」，并把 `credential_selfcheck` 的错误臂留作
唯一待办。本轮给出那个决定并实施。

**结论先说：不门控 worker，给错误证据臂补一条 `session_turns` 臂。**

### 上一轮的判据在这里被自己修正了一次

第八轮我写的是「该门控」。本轮实做时发现**「止错」不等于「必须门控」**：
判据问的是「停写后它会不会做出错误结论」，不是「它有没有跨门读 v1」。
`credential_selfcheck` 里两条 v1 读性质**不同**——错误臂是漏判（止错），
`recentUsageModels` 是范围陈旧（止对）。对 worker 整体门控会把后者一起停掉，
而真正的故障发现（`node_probe` / `node_probe_state`）**不依赖被冻结的证据**。
**为了让一条臂止错而关掉另外两条有效的臂，是把降级的捷径误当成停摆的能力。**

### 证据源的决定，以及放弃的是什么

真库 24h 窗口按凭据去重：v1 侧有报错 41、session 侧 15、两侧都有 15、
**只有 v1 有 26**；那 26 个按 `origin_stage` 拆**全部是 `node_probe`**。

⇒ 业务失败两族 1:1 重合，session 臂能覆盖；
⇒ 缺口全是探针流量，而**探针流量按设计不走 session 写路径，无法被端口**。

放弃的是「探针最近失败 ⇒ 现在去复检」这条**取证捷径**，不是探针能力本身。
这条残余**仍然存在**，要等 §8 决策 1 把探针流量纳入 session 写路径才自然消解。

### 改动是三处接线，少一处都是假修复

`INNER→LEFT`（不改则 session 臂永远轮不上）、新增 session 臂、
`ORDER BY` 接 `COALESCE`（PostgreSQL `DESC` 默认 **NULLS FIRST**，
不接会把 session-only 群体全部挤到最前，「最近报错优先」反转）。
纯增量，今天 `session_only = 0`，不改变任何现有结果。

### 真库硬证据（不是推断）

`PREPARE` + `EXECUTE` 通过，返回 `id=12`。**模拟停写**（v1 臂窗口设为 0）：

| 形状 | 停写后选中数 |
|---|---|
| 旧形状 | **0** ← 自检静默 |
| 新形状 | **1** |

仅靠 session 臂可救的凭据 = **15**。

### 这轮最该记住的：我的守卫返工了两次才立住

| 版本 | 写法 | 漏抓 |
|---|---|---|
| v1 | 全文件子串 | 被另外两条 LATERAL 喂饱 |
| v2 | 限定函数体子串 | 仍被同函数内另外两条 LATERAL 喂饱 |
| v3 | **按臂定位**（别名）+ **剥 SQL `--` 注释** | 无 |

第三版的两个要素各对应一个真实陷阱：这段 SQL 的**注释正文里就含**
`COALESCE(e.last_error_at, se.last_error_at)`；三条臂必须**按别名**定位，
用「第几个 LATERAL」会因调序静默改判。**守卫被无关的同名字符串喂饱，
比门不存在更危险**——它让人以为这一处已被守住。

### 连带修正：一个方向相反的既有门

`TestPickDueCredentialRotatesLeastRecentlyChecked` 钉的是 **ORDER BY 整行字面量**，
被我拆行后打红。这类守卫与上面**方向相反**：它把**纯排版变化**报成缺陷（假警报），
同时对**语义退化不敏感**。已改写为按**排序键先后次序**判定，并补 R1–R3 三个变异。
**重写门之后必须重新变异验证**——这是本轮的硬顺序。

### 变异 10/10 全部断言命中

M1–M7（S4 门）+ R1–R3（轮转门）。M7 是**对照实验**：删掉真实接线、只在注释里补
同样字样，门仍红 ⇒ 剥注释真的生效。

*两次误报都是**量具**错，不是门错：判定「是否断言命中」时拿文件**路径**匹配，
而 Go 输出**基名**；以及加 R 组后忘了把另一个测试文件计入白名单。
**量具错了先修量具再下结论**——与「真库数字先确认写入方身份」同族。*

### 下一轮从哪开始

本轮已把 §9.25 定性表里唯一待办做掉。跨门边界检查的**代码修复**到此收口。
接下来是**待拍板**的三件事（都不阻塞继续审计）：

1. **710 视图补 4 个投影**（`origin_stage`/`token_band`/`client_forwarded_for`/`trace_events`）。
   硬前提：必须与「所有视图读方切到 `bg.ProbeTrafficExclusionPredicateView`」同批。
   **不要加 `id`**（语义不同，见 §9.20）。
2. **30 列补位集里其余 28 列**的 session 侧等价映射是否现在逐列裁决。
   目前只有 `client_model`→`outbound_model←model`、
   `attachments`→`has_attachments←attachment_count` 两条有 710 文档明载。
   **模糊匹配不作数。**
3. **`RawModelName` 补齐 + `credential_recovery` 恢复 SQL 端口范围**：(a) 补数据源+端口 SQL
   / (b) 只补数据源 / (c) 改用门控。实测 `session_turns.raw_model_name` 填充率
   **0/1,682,828**。

**未做、已定性、需拍板**：`auto_route_settle_worker` 的陈旧基线标记
（要改 HTTP 响应契约）。

### 本轮交付状态（2026-10-02 收尾）

- §9.26 已提交并推送 `origin/main`：`05926d177`（父 `c11cf41e9` = 并行会话的
  `final_full` 存储收益证伪，`e4854c993` 重放后的等价提交）。
- 推送时远端已被并行会话推进 5 个提交（`936b12266` 等）。**用临时 worktree
  cherry-pick + 逐文件 blob 逐字节校验 + `update-ref` + mixed `git reset -q`
  重新落地**，全程未写工作区。
- **`admin.TestStopWriteEffectAgreesWithSourceFamily` 失败与 §9.26 无关**，已做差值归因：
  在一个**只含并行会话未提交改动、完全不含本轮改动**的基线 worktree 上该测试
  同样失败；且本轮改动全部在 `bg/` 包内，而该测试按登记表逐文件读 `admin/`+`domains/`、
  不扫目录。触发源是并行会话改过的
  `admin/request_logs_stop_write_classification_test.go` /
  `admin/view_source_columns_contract.go` / `db/request_logs_view_schema.go`。
- ⚠️ **工作区有 12 个文件相对新 HEAD 呈陈旧**（远端那 5 个提交动过、
  本地工作区仍是旧内容）：`docs/audit/2026-10-02-usage-trend-model-lines.md`、
  `docs/handoff/20261002-view-contract-815-handoff.md`、`tests/deploy_sops_test.sh`、
  `web/src/locales/{ar-SA,de-DE,en-US,es-ES,fr-FR,ja-JP,zh-CN,zh-TW}/usageTrend.ts`、
  `web/src/views/admin/UsageTrendExplorer.vue`。
  **内容与旧 HEAD 逐字节一致（无丢失），但需对方自行 sync**。我未做任何清理。

---

## 2026-10-02 追加（第十轮）：§9.27 读端静默退化量化 —— **并撤回我此前给用户的两个口径**

本节起于一个未处置的缺口（读端静默退化）。量化之后，第一个结论是
**推翻我自己前几轮给出的数字和一条待拍板事项**。

### 撤回一：「30 列补位集 / 39 个读方被阻塞」是错的

生效投影是 `buildSessionProjectionExprs(..., withDetails=true)`——**734 的
details 层已经把那 30 列顶掉了**。真库实测**恒 NULL 只有 6 列**：
`id` / `test_col` / `test_tab_indent` / `provider_model` /
`credits_rate_multiplier` / `client_ip`。

我错在**只数了 710 的占位，没数 734 已经顶掉它们**。逐列真库裁决后，
**待拍板从 28 列收缩到 2 列**：

- `id`：v1 是请求行 id、session 侧是 turn id（1,515,984 组配对命中 0）⇒ 不可投影，只能改读法；
- `test_col`：v1 满值（100%），但**全仓无任何 SQL 读它**（命中只在视图列清单与注释里）
  ⇒ 已有裁决 `verdictRetireWithV1`，认同；
- `test_tab_indent` / `provider_model`：v1 也是 0 ⇒ 死列，无可投影（已有裁决）；
- `credits_rate_multiplier`：v1 也是 0，`maas/credits_sql.go` 读它并
  `COALESCE(...,1.0)` ⇒ **恒按 1x 计费口径**。已有裁决 `verdictNoSessionSource`
  （会话族没有「这一行按什么倍率计价」这个事实）——**剩下的不是投影问题，
  是「会话族要不要记录计价倍率」，归 §8 决策 1**；
- `client_ip`：**我第一版把理由写窄了**。不是「text vs inet 的类型取舍」，
  真库按 `request_id` 配对 202,014 行实测：session `client_ip` 与本表
  `client_forwarded_for` 相同 **202,014/202,014**、与 v1 `request_logs.client_ip`
  相同 **0**、与 v1 `client_forwarded_for` 相同 **202,014/202,014**
  ⇒ 它是**写在 `client_ip` 名下的转发头副本**，直映它等于把 `X-Forwarded-For`
  当对端 IP。740 从 v1 侧 lateral 供真源 inet 的取舍**正确**。

⇒ **6 列逐列复核后全部认同已有裁决，「补位集」这项不需要你拍板。**

### 撤回二：「710 补 4 个投影」早已完成

真库：该视图现为 **118 列**（冻结 113 + 738 credits_rate_multiplier +
740 client_ip + **813 origin_stage/token_band/client_forwarded_for**），
三列均有值（1,591,290 / 158,277 / 227,195，总行 2,333,495）；
`trace_events` **不在视图里是刻意的**（镜像从不写，投影即净数据损失）。

我此前基于 §9.18/§9.20 的「视图缺这 3 列」是**读了当时的结论而没回真库复核**。
连带发现 `admin/credential_monitor_heatmap.go` 有一段注释的两项断言已成假
（「origin_stage 不在视图契约内、真库 0 列」「视图是冻结 113 列」）——
代码本身一直是对的，错的只是描述，已改写并附新数字。

> 通用形态：**注释里的「真库实测」是带时间戳的断言**，迁移一动就过期，
> 而读注释的人不会核它是否还成立。**这类风险不能靠门防**——
> 为注释数字写机械门必然带假阳性；能做的是把断言与它的失效条件写在一起。

### 新产出：effect × sourceFamily 交叉表（首次计算）

登记 **74** 个读点（我口头说过的「105」是另一口径）。**静默 50/74，会响的只有 8 个。**

最有决策价值的一行——`silently_empty` 15 个里 **0 个走 710 视图**，
11 个纯基表直读、4 个混 bodies；而 `silently_degraded_content` 18 个里
**0 个纯基表、7 个正是 `reads_view_with_null_padded_predicate`**。

⇒ 两类退化的解法**天然不同**，此前被「改指视图」一个口号盖住了：
- `silently_empty` ⇒ 主因是绕过视图直读 v1，**改指视图**可解；
- `silently_degraded_content` ⇒ 行照常出、**某列内容变空**，改指视图**解决不了**
  （典型是 bodies 腿无 session 兜底）。**视图读方本身也可能是静默退化源。**

### 待拍板清单（**已收缩，取代前几轮的版本**）

**只剩一件**（原三件全部已完成或已作废）：

1. **`RawModelName` 补齐 + `credential_recovery` 恢复 SQL 端口范围**：
   (a) 补数据源+端口 SQL / (b) 只补数据源 / (c) 改用门控。
   实测 `session_turns.raw_model_name` 填充率 **0/1,682,828**。
   （`test_col` 那条也已撤销：全仓无读方，且已有 `verdictRetireWithV1` 裁决。）

**已作废、不必再拍板**：
- ~~710 补 `origin_stage`/`token_band`/`client_forwarded_for`~~ → 813 已补（真库 118 列有数）；
- ~~710 补 `trace_events`~~ → 刻意不投影，投影即净数据损失；
- ~~30 列补位集其余 28 列逐列裁决~~ → 真恒 NULL 只有 6 列，3 列还是死列。

### 下一块最值得做的地

`silently_degraded_content` 的 18 个读点，**本轮一行代码没动**：
其中 7 个是**已经走视图**却仍静默降级的（`reads_view_with_null_padded_predicate`），
7 个混 bodies（bodies 无 session 兜底）。这一组既不是「改指视图」能解的，
也不是门能防的（门只能守形状，守不出「接口 200 但某列是空的」）。

---

## 2026-10-02 追加（第十一轮）：§9.28 bodies 端口可行性 + **修掉我自己 §9.24/§9.26 引入的缺陷**

### 一半结论：bodies 不是「无 session 兜底」，是「没去用」

登记表备注写的「bodies 腿无 session 兜底」对**当前 SQL** 成立，
作为**数据可得性**判断是错的。真库 `session_bodies`：

- `turn_delta` **1,683,104 行**，`request_delta` / `response_delta` /
  `outbound_body` / `request_attachments` **四项 100% 非空**，09-03 → 实时
  （`session_bodies_2026_09` 分区 8.6 GB）；
- v1 `request_logs_bodies_hot` 的 1,434 行非探针数据，**1,392 行（97.1%）**
  能按 `request_id` 配对。

**但内容不等价**：v1 `request_body` 是完整载荷
`{"model","messages","max_tokens","stream",…}`，session `request_delta`
**只有 messages 数组**（含 `model` 键的行数 **0/1,683,104**）。
配对的 1,392 行里三列**无一相同**。

`model` 另有来源（`session_turns.model` **100% 非空**、与 v1 payload 96.6% 一致），
但 `max_tokens`（v1 覆盖 96.9%）、`stream`（21.4%）**在会话族里根本不存在这个事实**。

⇒ **10 个 bodies 腿读点不能无损改指**。根因是**一个**：
**轮次写入器只持久化了 message delta，没持久化完整请求载荷**——
这同时解释了 `session_turns.raw_model_name` 填充率 **0/1,682,828**。
修它要给 session writer 加一列完整载荷（**动热写入路径 + 需历史回填**），
属需拍板范围变更，**本轮未擅自做**。

### 另一半：**查出并修掉我自己在 §9.24/§9.26 引入的缺陷**

会话族是**两个存储面**：写方只写 `session_turns_hot`，
冷行由 `promote_session_turns_hot_to_partition` 搬到分区父表，
**边界随 promote 节奏移动**。本机实测：父表停在 **2026-10-02 06:06:31**、
`_hot` 从 06:07:14 接到 **14:50:12**（实时）⇒ **父表落后 8.7 小时**。
`session_bodies` 父表也是 06:06:31（同一时刻）。

710 视图用的是 `session_turns_hot UNION ALL session_turns`，**直读方必须照做**。
而我自己的两条臂都没做：

| 位置 | 原读法 | 缺陷 |
|---|---|---|
| `bg/credential_selfcheck.go`（§9.26） | `FROM session_turns` | 对最新轮次盲 |
| `bg/auto_route_affinity_worker.go`（§9.24） | `FROM session_turns` | 漏最新合成流量 |

**危害实测**（自检臂，24h 窗口内父表覆盖不到的那段）：
**旧形状 0 个失败轮次 / 两面合并 761 个。**
这条臂存在的意义正是抓最新失败，单面读法让它**对自己的目标完全失明**。

已修（两处改两面 UNION + 写明理由），并把 §9.26.2 的数字改对：
只读父表时测得 15 / 26，**两面合并后是 19 / 22**（结论不变，数字当时错了）。

### 为什么前两轮的门没抓住

§9.26 的门验的是「臂存在 + 三处接线 + 显式转换」，**没有一条关心它读哪个面**。
**门的形状与缺陷的形状不匹配**——我按「补了一条臂」写门，
而缺陷是「那条臂读漏了一半数据」。这类缺陷只能靠**按语义钉**发现。

亲和度那条门原本钉 `FROM session_turns st` —— **它把缺陷形状本身钉成了标准**。
已改写为按语义钉两面，并加一条**否定式**断言专门挡退回单面。

**变异 7/7 全部断言命中**，含 **N3「只删 hot 那一行」**（语法仍合法、关键字仍在，
专门验门不是子串喂饱）。

### 量具前提

本地库无 `cron.job`，promote 节奏由**外部实例**决定 ⇒
「父表落后 8.7 小时」是**本机环境值，不是设计承诺**。
但结构性结论与节奏无关：**单面读法必然漏**，漏多少随节奏变、漏不漏不变。

### 待拍板（仍是那两件，未变）

1. **轮次写入器要不要持久化完整请求载荷**——本轮新证据把它从「raw_model_name 那一列」
   升级成「10 个 bodies 腿读点 + raw_model_name 的共同根因」。属动热写入路径 + 需回填。
2. `raw_model_name` 是否随该载荷一并解决（若 (1) 成立，它自动解决）。

---

## 2026-10-02 追加（第十二轮）：§9.29 全仓普查「只读父表」+ 修掉标注工作台的**线上 100% 失明**

### 普查结论：PG 侧只剩 1 处（且已修）

| 类别 | 数量 |
|---|---|
| 走合并视图 `session_turns_with_current_month` / `session_bodies_unified` | 绝大多数读点（真库已核实两视图**都 UNION 两面**） |
| 直读基表但 UNION 两面 | 12 个文件（含上轮修的 2 处） |
| 只读 `_hot` | 3 个——**正是写方** |
| SQLite 同名表 | 4 个——**不同存储面** |
| **只读 PG 父表** | **1 处 → 已修** |

两批并行手验（18 个文件逐个读 SQL 上下文 + 行号 + 判定）**独立得出与我一致的结论**。

### 已修：`admin/annotation_handler.go:673`

`firstTurnFromClause` 直读基表找**每个会话的首轮**。真库实测（当天窗口）：

| 形状 | 当天有首轮的会话 |
|---|---|
| 旧形状 | **671** |
| 新形状 | **2,077** |
| 新捞回 | **1,406** |

⇒ **旧读法对当天会话可见率只有 32.3%**，且 `COUNT(*)` 用同一条子查询 ⇒
列表与分页 total **同时偏小**。另一口径：首轮只在 `_hot` 的会话 1,397 个、父表 0 命中，
**全部是今天新建的**（对照：首轮在父表的 832,627 个）。

已改为两面 UNION，并把 `turn_no`/`partition_date` 显式带出（UNION 后外层谓词依赖它们）。
**刻意不用** `session_turns_with_current_month` 视图——它虽已含两面，
但 ft 还要按 `partition_date` 二次下推。

### 门：`admin/session_family_two_surface_test.go`（默认拒绝 + 具名登记，变异 4/4）

**判据钉在「一条 SQL」而不是「一个文件」**——一个文件可能同时有正确的 union 查询
和一条漏读的单面查询。反向自检：登记了却不再命中的要报红。

**这门返工了三次，每次都是跑出来才发现**（这是本轮最该记住的）：
1. 钉「文件里出现过裸父表读」⇒ **门宽了**：`UNION ALL` 的父表那一支本来就合法，
   却把 12 个文件全判红。*每次假阳性都是「门宽了」不是「代码错了」。*
2. 重写时把「前置必须是 FROM/JOIN/INTO/UPDATE」一起删掉 ⇒ 又宽回去，
   `cmd/gateway/main.go:2659`（`slog.Info` **日志文案**里列表名）与
   `test_helpers.go:48`（**表名清单**）变假阳性。
3. 定稿：逐条 SQL 判 + 必须作为语句来源出现，两个条件缺一不可。

**扫描器本身也返工三次**：60 → 18 → 4 → **2**。
错因依次是：没剥 Go 注释、基表名后用 `\b` 排除不了视图名（`_` 属单词字符）、
以及把「文件级」当成「查询级」。

**剩余盲区**（写在门上）：SQL 由字符串**拼接**在运行时组装的形状。
两条登记（710 视图体拼装器、表名切片遍历）属这类。这类形状只能靠**真库执行门**兜底。

### 顺带查出（未修，需拍板）：约束名被当成了视图名

`cmd/tools/validate_sessions_v2/loader.go:328` 读 `public.session_bodies_with_current_month`。
真库核实：该名字 relkind = **`i`（索引/约束）**，不是视图；真正的合并视图叫
`session_bodies_unified`（relkind = `v`）。全仓确认它**只作为 UNIQUE 约束名**
出现在迁移 614/645，**从无 `CREATE VIEW`** ⇒ 该工具运行时会
`relation does not exist`。

**未修**：注释明说「有意与 legacy `session_bodies_unified` 分开，后者的列与当月语义
不足以作为发布证据」——改指过去会**改变这个 parity 门的判定口径**，属需负责人拍板。

⇒ 「同名不代表同一个东西」的又一例：**一个约束名冒充了视图名**。

### 待拍板（累计三件，均不阻塞继续审计）

1. **轮次写入器要不要持久化完整请求载荷**（`request_payload` 列）——
   同时决定 10 个 bodies 腿读点与 `raw_model_name` 填充率 0/1,682,828。
2. **`cmd/tools/validate_sessions_v2` 的 bodies 视图该指向哪个**——
   改成 `session_bodies_unified` 会改变 parity 门判定口径。
3. `auto_route_settle_worker` 陈旧基线标记（需改 HTTP 响应契约）。

---

## 2026-10-02 追加（第十三轮）：§9.30 **第一次**做「确认存储可用性」——并改变退役的理由

前面十二节都在改读法，**没有一节回答过目标原文那句「确认数据的存储可用性」**。
本节做真库体检。

### 结论：容量不是退役的理由

| 族 | 表数 | 体积 | 单位成本 |
|---|---|---|---|
| `session_*` | 73 | **19.3 GB** | **9.33 KB/轮** |
| `request_logs*` | 16 | **8.2 GB** | **3.95 KB/请求** |

整库 56 GB。⇒ **session 族单位成本是 v1 的 2.36×**，且**体积已经更大**。

退役 v1 一次性省 8.2 GB，相当于：

| 日轮次 | session 增长 | 8.2 GB 相当于 |
|---|---|---|
| 384,411 | 3.42 GB/天 | **2.4 天** |
| 100,000 | 0.89 GB/天 | **9.2 天** |
| 20,000 | 0.18 GB/天 | **45.8 天** |

⇒ **纯从容量看，退役省的量会被 session 族增长在几天到几周内吃掉。**
**退役的正当理由必须是架构一致性/可维护性，不是容量。**

### 覆盖差 29.9%，其中 28.1 个百分点是探针流量

v1 有而 session 无的请求（按 `request_id` 两面反连接）：

| 类别 | 请求数 | 占 v1 |
|---|---|---|
| **`probe-*` 命名** | **608,890** | **28.1%** |
| 有会话头但无 session_turn | 38,231 | 1.8% |
| 无会话头 | 64 | 0.0% |
| 合计 | 647,185 | 29.9% |

探针流量按设计不进 session 写路径 ⇒ **这部分 v1 成本不可迁移**，
退役实际能省的比 8.2 GB 更少。等覆盖率折算后 session 单位成本 ≈ 6.5 KB/轮，
仍是 v1 的 **1.66×**。

### 一个被证伪的假设（写在这里防止重犯）

我先查 `origin_stage='probe'` 想确认探针占比，实测 **0 / 2,163,262** ——
v1 侧该列没被这样填充，**这个判据不成立**。真正的判据是 **`request_id` 的
`probe-*` 命名**。*假设被数据推翻时要说出来，不要改口径让它看起来成立。*

### ⚠️ 容量规划**不能**用本机日序列

| 日 | 轮次数 |
|---|---|
| 09-24 | 384,411 |
| 09-26 | 138,958 |
| 09-29 | 4,490 |
| 10-02 | 2,111 |

**9 天内掉 99.5%**。这不是业务信号，是**量具信号**——本地库由外部实例写入
（写入方身份未确认）。⇒ 上面三档投影只能当**敏感性区间**，
要得到可用结论必须换一个**写入方已知**的环境（245 / 154 / 252）重测。

### 本轮第三次量具返工

第一版把 v1 族写成 **36 表 / 14 GB**——错的：用了 `LIKE 'request\_%'`，
把 `request_*` 下的其它表也算进去。精确口径是 `LIKE 'request\_logs%'`（16 表 / 8.2 GB）。
**错因与前两次同族：用宽 LIKE 代替精确定义。**

### 待拍板（累计三件，未变）

1. 轮次写入器要不要持久化完整请求载荷（同时决定 10 个 bodies 腿读点 +
   `raw_model_name` 填充率 0/1,682,828）。**本节数据不支持靠省空间来论证它。**
2. `cmd/tools/validate_sessions_v2/loader.go:328` 的 bodies 视图指向（改口径需拍板）。
3. `auto_route_settle_worker` 陈旧基线标记。

### 建议的下一步（因为容量不是理由，理由得重新找）

若仍要推进退役，正当理由是**一致性**而非容量：
读面已全部收敛到 session 族（§9.29：只读父表的真缺陷 3 处已全部修完），
剩下的都是**控制面**（S4 退出判据三条：写入面 / 读面 / 历史面）。
其中**历史面**值得单独一查：本机实测 **两族同起点**（v1 最早 2026-09-03、
session 族最早也是 2026-09-03）⇒ 在**本机这个库里**历史面看不出缺口。
但这**不能推广**：本库写入方身份未确认，v1 之所以没有更早的数据，可能只是
保留期或环境本身如此。**「历史面无缺口」必须在写入方已知的环境（245/154/252）
重测才能下结论**，本机结论只够说明「这里查不出问题」。

---

## 2026-10-02 追加（第十四轮）：§9.31 修掉 parity 门 —— 它一直在**产出零证据**

### 为什么这节优先级高于它看起来的样子

用户目标原话是「**确保数据在更改前后一致**」。负责执行这句话的就是
`cmd/tools/validate_sessions_v2` 这个 parity 门，而它的 `CanonicalV2BodiesView`
指向 `public.session_bodies_with_current_month` —— 该关系在真库
**relkind = 'i'（索引/约束），不是可查关系**（真正的合并视图是
`session_bodies_unified`，relkind = 'v'）。全仓确认它**只作为 UNIQUE 约束名**
出现在迁移 614/645，**从无 CREATE VIEW**。

⇒ **每次运行都 `relation does not exist`，parity 门一直在产出零证据。**
而同包 `loader_test.go` 的源码契约串**照样绿**——它只证明「源码里写着这个名字」。
**这比「门红了」更坏：门红会被人看见，跑不起来只会安静地没有输出。**

⇒ 也意味着：前面十三节所有「实测两族一致 / 覆盖 97.1%」的结论
**都是我一次性手查的**，不是可持续的自动门。

### 已改指，依据是逐条核实而非推断

原注释反对用 `session_bodies_unified`（「column 与 current-month 语义不足」）：

| 反对理由 | 核实结果 |
|---|---|
| 「column 不足」 | **不成立**：装载查询要的十个列**全部具备** |
| 「current-month 语义不足」 | 覆盖**全保留期**（2026-09-03→实时，177 万行）而非仅当月——对**完整性/parity**门是**优点** |

⚠️ **这改变了门的判定口径（当月 → 全保留期），属语义变更，请负责人复核。**
若真需要「仅当月」，正确做法是**新建一个视图**，而不是继续引用不存在的名字。

### 新增真库执行门（源码门看不见运行时形状）

`parity_bodies_relation_integration_test.go`（`-tags=integration` + `TEST_PG_URL`）：
关系存在且 relkind 可查 / 十个必需列齐备 / **真跑一次装载查询形状**
（参数取自真库真实 tenant/session，不是我编的）。

**门自己返工了一次，是变异验证逼出来的**：第一版把关系名**硬编码在门里**，
把 `loader.go` 的常量改回那个不存在的名字时**门照样绿**——
它证明的是「session_bodies_unified 存在」，而它要回答的是
「parity 门用的那个关系存不存在」。改成同包引用 `CanonicalV2BodiesView` 后 4/4 抓全。

*变异脚本自身也错了两次（M1 改的字符串只出现在守卫分支 = 空变异；
M3 一次改两个变量）。门不响时先怀疑变异、再怀疑门——这次两者都有问题。*

### 待拍板（累计两件，第 2 件从三件里划掉）

1. **轮次写入器要不要持久化完整请求载荷**（同时决定 10 个 bodies 腿读点 +
   `raw_model_name` 0/1,682,828）。**§9.30 的数据不支持用「省空间」论证它。**
2. **parity 门判定口径变更复核**（当月 → 全保留期）。若不接受，改为新建当月视图。
3. `auto_route_settle_worker` 陈旧基线标记（需改 HTTP 响应契约）。

---

## 2026-10-02 追加（第十五轮）：§9.32 目标原话「**确保数据在更改前后一致**」的正面回答

§9.31 修好了 parity 门「能不能跑」。本节回答它**没回答**的问题：**跑出来的数据对不对**。

### 结论：在两族共有的事实上，两族**完全一致**

样本 **762,652 行非探针配对**（必须排除 `probe-%`，否则把「按设计不镜像」
判成「不一致」）。判据是**两侧都有值且不同**：

| 字段 | 真不一致 | 某一侧单独记录（**不参与判定**） |
|---|---|---|
| `success` | **0** | 0 |
| `prompt_tokens` / `completion_tokens` / `latency_ms` | **0** | session 有 / v1 空 **608,706**（completion_tokens） |
| `upstream_status_code` | **0** | v1 有 / session 空 **45,337** |
| 模型（原始串） | 81,531 | session 有 / v1 空 **575,302** |

⇒ **所有差异都是「某一侧不记录」，不是「两族记成了不同的东西」。**

### 模型：86% 的「不一致」是命名口径

归一化后残余从 81,531 降到 **2,913（0.38%）**。真实成对写法（取自真库）：
`MiniMax-M3`↔`minimax-m3`（3.9 万）、`glm-5-2-260617`↔`glm-5.2`、
`nvidia/riva-…`↔`riva-…`、`moonshotai/kimi-k3`↔`kimi-k3`。

**不归一化就比 ⇒ 门被 8 万条命名噪声喂成永远红，而那不是缺陷。**

⚠️ 残余里有一类值得人看：session 侧有时记的是**被截断的模型名**
（`claude-sonnet-5` vs `sonnet-5`、`grok-4.6` vs `4.6`）——不是命名风格，是**信息丢失**。
另有个别疑似真分歧（`glm-5-2-260617` → `glm-5.1`），量级小，未定性。

### 门又抓到了我自己：过度归一化

`TestNormalizeModelNameKeepsGenuineDivergence` 第一版就把 `gpt-4o` vs
`gpt-4o-mini` 判成相同（变体表含 `mini`）。**过度归一化比不归一化更坏：
它把真缺陷洗成一致。**已把 `mini`/`pro`/`max` 移除，并单独固化「真分歧必须仍不等」。

### 新增两道门

| 门 | 类型 | 回答什么 |
|---|---|---|
| `dual_write_value_parity_integration_test.go` | 真库 | 值层一致性；阈值按**实测值×余量**，`success` 零容忍 |
| `model_name_normalize_test.go` | **纯单测**（无需 DB） | 归一化既不能漏也不能过 |

归一化门变异 4/4 断言命中（M1 掏空 / M2 重引 mini / M3 不剥前缀 / M4 不剥后缀）。
*M3 第一版写成 `if false {` 导致编译失败——**崩溃不是证据**，那一版变异无效，
已改成仍能编译的恒假条件。*

### 一个必须记住的口径教训

第一版口径写错了，而且**是门自己抓出来的**：注释写了「先拆口径」，
SQL 却还在用 `IS DISTINCT FROM`（把「一侧 NULL、另一侧有值」也算不一致），
于是测出 `completion_tokens` **79.8% 不一致** —— 拆开后真不一致是 **0**。
⇒ **注释里写的纪律，SQL 没照做；只有真跑一遍才会发现。**

### 本节的边界

- 数字全部来自**本机库**，写入方身份未确认（§9.30.4）。
- 只比了**两侧都有值**的事实。那 60.9 万 / 4.5 万 / 57.5 万「一侧未记录」
  **决定了退役 v1 后会丢什么** —— 与 §9.28 的 bodies 腿、
  `raw_model_name` 0% 填充是**同一张账**。
- 门需要 `TEST_PG_URL`，默认跳过；**跳过不构成证据**（已写进门的提示）。

### 待拍板（三件未变）

1. 轮次写入器要不要持久化完整请求载荷 —— **这 60.9 万行正是它的直接价值**：
   v1 退役后 `completion_tokens` 就只剩 session 侧那份。
2. parity 门判定口径复核（当月 → 全保留期）。
3. `auto_route_settle_worker` 陈旧基线标记。

---

## 第十六轮（§9.33）：把 45,337 追到底 —— **证伪**，并撤回一份错误的读方清单

上一轮留下一个数字：「仅 v1 记录 `upstream_status_code` 45,337 行」。
本轮追到底，**结论是它既不是缺陷，也不是退役 v1 的代价**。

### 三步

1. **先确认 NULL 本身不是异常**：视图 94.40% NULL，v1 基表 91.88%，
   session 93.50% —— 两族基线都 ~9 成。该列只在错误路径记。
   ⇒ **不立「非 NULL 率」门**，它会以 94% 基线报红，是假警报机器。
2. **定位真洞**：按 `request_id` 配对，v1 有值 176,134 → 也在 session 的 155,282
   → session 侧也有值的 109,945 **与 v1 零分歧**；为 NULL 的 **45,337**。
   710 视图 v1 臂的反连接去重把 v1 的 200 挡在门外，session 臂出场带 NULL。
   三臂**都**投影了这一列 ⇒ 洞在取值不在投影。
3. **成因是写方上线时点**：session 侧填充率 09-13 前 ≤2.9% → 09-14 31.2%
   → **09-15 起至今 100%**。45,337 全部落在部署前，**今天零缺失**。
   构成印证无价值：45,323 是 `success=true`+`200`，14 是 `false`+`200`。

### 撤回：上一轮的「受影响读方清单」是错的

按「**同时**引用 710 视图 **且**用该列」求交集，非测试 Go 文件只有 2 个：
`db/request_logs_view_schema.go`（视图定义本身）、`bg/candidate_failure_monitor.go`
（`:258` 在 `candidate_failure_logs` 族，它读 710 视图的 `:333` 不取该列）。

上一轮点名的 `bg/provider_error_aggregator.go`（读 `candidate_failure_logs`）、
`admin/candidate_failure_handlers.go`（同族）、
`internal/quality/minute_aggregator.go`（读 **`request_logs_hot` 基表**，
且 `:36` 判据 `upstream_status_code IS NULL AND success` **本就 NULL 容错**）
—— **三个都不经过 710 视图**。

**信号**：`view_success` 实跑是 `t` ⇒ 成功信号存活，唯一有用的谓词仍然工作。

### 决策

- **不修**（回填 45,337 个 `200` 无价值；改反连接为 LEFT JOIN 要重建 2.3M 行视图）。
- **不是退役代价**：今天已经给 NULL，退役后反连接本就该消失，洞自动不存在。
- **不立门**：该列的守点已被 §9.32 值层门覆盖（两族都有值时零分歧）。
- 归入「某一侧未记录」账本的**已定性条目**，不是待办。

**教训**：grep 命中「文件里出现过这个列名」≠「这个读方从这层视图读这一列」。
跨族撞车（`candidate_failure_logs` 也叫 `upstream_status_code`）会让扫描器
产出高置信度的假受影响方。**必须求交集后再手验**（与 §9.21 同族）。

### 待拍板（仍是四件，未变）

1. 轮次写入器要不要持久化完整请求载荷（`request_payload` 列）。
2. parity 门判定口径复核（当月 → 全保留期）。
3. `auto_route_settle_worker` 陈旧基线标记（需改 HTTP 响应契约）。
4. §9.31/§9.32 两道新门是否接进 CI（需 `TEST_PG_URL`）。

---

## 第十七轮（§9.34）：两道新门在 CI 里的真实状态 —— 实测，**结论是不该接**

上一轮把「是否接进 CI」列为待拍板。本节不去猜，把 harness 真跑了一遍。

### 三个实测事实

1. **这个包已经在门禁列表里**，是 §9.31（`a3cc27f0f`）自己加的 ——
   `derive-gate-packages.sh` 派生 `./cmd/tools/validate_sessions_v2`（29 个包之一）。
   不是「要不要接」，是「已经在了，状态如何」。
2. **harness 确实注入 `TEST_PG_URL`**（`run-integration-gate.sh:586`，共 14 个 DSN 名字，
   有守卫 `TestGateInjectsEveryDBCredentialName` 防漂移）。
   ⇒ §9.32 写的「默认跳过」只在 `go test` 裸跑时成立。
3. **对着门禁库（prereqs → 基线 → 200 迁移，442 relations）两道门 SKIP 并自陈**
   「库里没有可配对的非探针样本 —— **本门不构成证据**」。

第 3 条是**设计正确**：门拒绝在无样本时成为证据，而不是 0/0 真空通过。

### 但它们在 CI 里双重失效

- workflow 的 `paths:` **不含 `cmd/**`** ⇒ 我加测试文件的提交根本不触发它。
- 即便触发：该包普通单测会 PASS ⇒ 走 `NSKIP>0` 的**警告分支**（非致命）
  ⇒ **CI 绿，而两道门零证据**。

### 决策：不接，且这是**类别性错误**而非取舍

跨 762,652 行配对样本的值层门**无法在一次性空库上成立**。要真跑只有两条路：
① 给门禁库播种双写样本 —— 制造数据来证明数据的门，播种口径本身成为新的未审计事实源；
② 对生产形态库跑 —— 那是本机用法，不是 CI 用法。
⇒ 它们是**本机生产形态库上的核对工具，不是 CI 门禁**。接进去只会制造
「绿着但什么都没证明」的条目。**保持不接，把事实写清楚。**

### 途中撞到的他处缺陷（已定位，未动）

harness 首次运行在**跑任何测试前**就死：
`01-schema.sql:18510 ERROR: relation "public.candidate_failure_logs_hot" does not exist`。

- **不是已提交代码的问题**：`d5932d26c`（并行会话，已在 origin/main）已把那行
  改回读父表；用 `git show HEAD:` 重建库 → **基线干净通过**，200 迁移全过。
- 真因是**工作区未提交的并行会话改动**（`M sql/schema/01-schema.sql`）改回了 `_hot`。
  按纪律**未做任何还原**，只用 HEAD 版绕过。
- 另有 `embeddata/startup/` 4 个未提交改动，未逐一核实。

**若据红改代码，会把一个已被修好的问题重新引入。** 共享工作区里
「跑出来红了」≠「代码是红的」。

### 待拍板（现在只剩三件 —— CI 那一件已由实测关闭）

1. 轮次写入器要不要持久化完整请求载荷（`request_payload` 列）。
2. parity 门判定口径复核（当月 → 全保留期）。
3. `auto_route_settle_worker` 陈旧基线标记（需改 HTTP 响应契约）。

---

## 第十八轮（§9.35）：三项拍板落地 —— 一件上线，两件「按证据收窄」

拍板结果：① **不加** `request_payload` 列；② parity 门**保持当月**；
③ **做** `auto_route_settle_worker` 陈旧基线标记，改响应契约。

### 已上线：`outcome_source` 响应契约块

`admin/auto_route_outcome_freshness.go`，挂三个暴露 reward/success 的端点
（`/auto-route/audit`、`/affinity/ranking`、`/affinity/selections`）。
加字段不改既有字段；`reason` 闭集 `live|no_rows|stale|absent|query_failed`；
门槛 = `settleAbandonAfter`(4h) 而非 `baselineWindow`(24h)，因为绑定约束是
abandon 视界。v1 被退役后 `42P01` 映射成 `reason:"absent"` 而不是 5xx。
`MAX(ts)` 实测 **0.275ms** Index Only Scan。

**5/5 变异全部断言命中**，红在 `:347 / :256 / :149 / :146 / :149`。
**M3 抓到真 bug**：`Stale` 初始化为 `true` 后 live 分支忘了设回 `false`
⇒ 该字段恒 true，标记退化成「永远陈旧」，等于没加。已修。

**写门时自己踩的两个坑**（已写进测试注释）：
- 接线门只认 `*ast.SelectorExpr`，而 `writeJSONOk` 是**包级函数**（`*ast.Ident`）
  ⇒ 在三个挂载齐全的文件里报**零挂载**。**把「在」报成「不在」的门比没有门更坏。**
- 镜像门按标识符**首次出现**切行，而该名字先出现在 doc comment 里 ⇒ 把散文当 Go 解析。
  **锚点要钉在声明上，不是钉在名字上。**

### 失效方向更正：不是 ③，是 ②

我此前记成「结算继续但用陈旧基线」。按代码重推：停写 8h 后热表空 ⇒
分位数 NULL ⇒ **基线塌成 0**；且 `rl.success` 恒 NULL ⇒ **每条新 selection 都被 abandon**，
`settled_at` 有值、`reward: null`，**与「正常放弃」逐字段同形**。
这是**静默洞（漏判）**，不是过度纳入。

### ⚠ 撤回我在拍板问卷里对「不加列」说错的一句

我写的是「把 10 个读点改成不再依赖该事实」。派子代理逐读点核实后**不成立**：

- 实际是 **22 文件 / 约 30 读点**，不是 10。
- **响应侧整体不可端口**（§9.28 完全没记的一条）：v1 `response_body` 是
  `{"choices":[{"message":…}]}` 信封，`session_bodies.response_delta` 是**裸消息数组**
  （`BodiesRecord.ResponseDelta []Message`）。⇒ 7 个按 `choices[].message.content`
  取值的读点**静默返回空串**。
- 另有 4 类结构性不可端口：`tools` 键、**整份 JSON 透传**（4 处，无 Go 侧解析）、
  **逐行聚合语义**（delta 只含本轮新增 ⇒ 系统性低估）、
  `system`/`instructions` 顶层键。
- 可复现的只有 **8 处**（且都是请求侧只依赖 `messages`），**且必须显式包一层
  `{"messages": <delta>}`**，否则 `len(Messages)==0` 全部返空——静默失败不是报错。

**所以本轮没有做**：①没改指那 8 处（半改比全不改更难推理）；②没建 bodies 可移植性
登记表门（既有分类门当前就红在 **31/105 未评估**，`7a6356ef0`，与本轮无关；
在它还红时另起一套 = 制造两套互相漂移的登记表）。

### 下一步的正确顺序

1. 先把既有 `request_logs_stop_write_classification_test.go` 收绿（31/105）。
2. 再在它上面加「可移植性」维度，而不是另建一张表。
3. 只有当响应侧形状对（`response_delta` 是信封而非数组）或写入器补列，
   响应侧那 7 个读点才可改指。

### 仍待拍板

**无**。原四件已全部关闭：CI 那一件由 §9.34 实测关闭（本机工具，不接 CI）；
其余三件本轮落地或按证据收窄。

### 遗留风险（未变，按危险度）

1. 既有分类门红在 31/105 ⇒ **S4 灰度前必须收绿**，否则 silently_empty /
   silently_frozen 两档会让灰度「通过之后继续给出错误答案」。
2. 响应侧 7 个读点 + 4 类结构性缺口：退役 v1 后要么改写入器，要么接受降级。
3. `auto_route_settle_worker` 的**标记**已上线，但**它的正确修法**（改读会话族）
   仍未做 ⇒ 停写后它仍会全量 abandon，只是现在**看得见了**。

---

## 第十九轮（§9.36）：S4 停写分级门**收绿** 31/106 → 0/106

上一轮定的下一步。四个子代理并行评估 31 个文件，我逐条手验。
**八道门全绿，`go test -tags s4audit ./admin/` ok 69.6s 零 FAIL。**

### 档位分布（这是灰度清单，不是「门红了」）

| 档位 | 条数 |
|---|---|
| `silently_empty` | 11 |
| `unaffected_by_stop_write` | 8 |
| `silently_degraded_content` | 4 |
| `silently_frozen` | 4 |
| `errors_out` | 3（响亮失败，可接受） |
| `validator_dual_read` | 1（§9.35 新鲜度探针） |

**⇒ 灰度前必须先处理的是 19 条**（前三个静默档）。门绿了，但风险已登记。

### 变异 3/3，各命中不同的门，都是断言命中

- M1 删一条登记（实际跨 6 条）→ 覆盖门报「**6**/106」，精确数出我删的条数
- M2 改 Evidence → 逐字门 `:939`
- M3 抽掉 null-padded 具名论证 → 族一致门 `:1328`

*M3 第一版截断字符串导致编译失败——**崩溃不是证据**，那版无效；
改成按 map 条目边界删、`go vet` 确认语法完好才重跑。*

### 三处「我原本会判错」——这一节是本轮最值钱的部分

**① 判「视图读点停写后还供不供数」必须量**近期**填充率，不能用全历史均值。**
`admin/session_extract.go` 谓词用 `gw_task_id`（视图里取自 details 的 LEFT JOIN）。
全历史口径：session 臂 94.1% 为 NULL ⇒ 看起来恒 0 行 ⇒ 判 `silently_empty`。
按天口径：09-29 的 4.77% → 09-30 的 37.88% → 10-01 的 97.72% → **今天 98.51%**，
`api_key_prefix` 自 09-26 起 100% ⇒ details 写入链已修好 ⇒ 正确档位是 `unaffected`。
**停写后果是前瞻问题，量具必须能回答「现在和以后」。**

**② 同一个量在另一个文件里方向相反：`provider_id` 在恶化。**
`session_analytics_breakdown.go` 按 `provider_id` 分组，真库缺失率
09-27 的 22.15% → 10-01 的 **68.03%**；同期 `outbound_model`/`cost_usd` 均 0.00%。
⇒ 停写后约一半新流量不再计入真实 provider ⇒ 判 `silently_degraded_content`。
**这也是一条新的运营事实**：会话族 provider 归属只有约一半填得上、且在恶化，
它是退役决策的独立输入。

**③ 我自己犯的测法错误：按 `request_id` 跨存储面猜行来源。**
我曾判 `..._without_customer_id`「70% 行来自 session」并差点据此改判。
`pg_get_viewdef` 的真库定义是纯 `request_logs_hot UNION ALL request_logs`，
**没有 session 臂**。错因：该包装视图**没有顶层 710 的 `NOT EXISTS session_*` 反连接**，
所以 v1 行的 request_id 本就与 session 行重合。
⇒ **判断视图有没有 session 臂，量具是 `pg_get_viewdef` 逐字读，不是按 id 猜来源。**
（与「列名撞车」「关系名相同不代表同一存储面」同族。）

### 顺带定案的两个子代理 UNRESOLVED（真库直接查）

- `request_logs_with_current_month_without_customer_id` 有无 session 臂 → **无**（上面 ③）。
- 线上 `recent_success_rate()` 读哪张表 → 查 `pg_proc.prosrc`：
  **直读 `request_logs_hot`、3h 窗口** ⇒ `credential_success_rate.go` 判
  `silently_empty` 成立（子代理正确）。

### 未修的相邻缺陷（记账）

1. **`discovery/discovery.go` 的控制面登记表已过期**：仍登记 `Gated: false`，
   而护栏是 `e52687954`（§9.12）后加的。**没有任何门把 `Gated` 与代码里的实际护栏对照。**
2. **`nullPaddedUnaffectedJustification` 已有 4 条是同一误触发的产物**：
   族分类器按词边界匹配补位集里的 `id`，而命中常来自**别的表**的 `WHERE id = $1`。
   本批的 `live_stream_sse.go` / `candidate_failure_monitor.go` 同形态。
   **根修是给补位匹配加表归属**，不是继续逐条写论证。

### 状态

**无待拍板项**。§9.35 之后所有拍板已关闭。

### 下一轮的正确顺序

1. 处理 §9.36.3 的 19 条静默档（按 `silently_empty` → `silently_frozen` → degraded 排序），
   至少先让 3 条**控制面**的有可见出口（`auto_route_settle_worker` 已做、
   `today_success_probe` 与 `diagnostics_credential` 未做）。
2. 修族分类器的 `id` 误触发（加表归属），可一次性消掉 4+2 条假论证。
3. 修 `discovery/discovery.go` 的控制面登记表过期，并**给该门加一条
   「`Gated` 必须与代码里的实际 S4 护栏对照」的断言**。
4. `auto_route_settle_worker` 的**正确修法**（改读会话族）仍未做——
   §9.35 只让它**可见**，没让它**正确**。

---

## 第二十轮（§9.37）：`Gated` 字段**从来不被验证** —— 补门 + 三条过期记录

§9.36.4 记的第 1 条相邻缺陷，查到底发现**不是孤例**。

### 洞：`Gated` 是个装饰字段

`requestLogsControlPlaneReaders` 每条有 `Gated bool`（该消费方是否被 S4 写门覆盖）。
本轮核对确认：**此前没有任何一道门验证过它**。既有门只核三件事——Evidence 非空、
Evidence 逐字存在、`Live && !Gated` 时 BlastRadius 非空。**`Gated` 自己从不被读。**

### 后果已发生三次，形状完全一样

| 条目 | 护栏引入 | 登记表最后改写 | 差 |
|---|---|---|---|
| `discovery/discovery.go` | `e52687954` 12:25 | `af4ef4b32` **12:14** | 晚 11 分 |
| `bg/credential_recovery.go` | `9b8424fd8` 14:00 | `b585c036e` **11:55** | 晚 2h05m |
| `bg/ledger_reconciliation.go` | `dfd4da2f1` 13:35 | `b585c036e` **11:55** | 晚 1h40m |

先写登记（`Gated:false` + 描述失效形态的 Note）→ 后加护栏 → **门全绿、登记表不动**。
`discovery` 的 Note 至今写着「**本表方向最危险的一条**…主动禁用仍在工作的凭据模型」，
**而那件事已被修掉**。这张表当时在对外说假话，没有任何门会发现。

**为什么没人发现**：三条都符合既有门的所有判据（Evidence 仍在、BlastRadius 仍非空），
**只有 `Gated` 这一个字段是凭记忆写的，而它是唯一不被检查的那个。**

### 补的门

`TestControlPlaneGatedFlagAgreesWithCode` —— 默认拒绝 + 具名豁免：
文件（**剥 Go 注释与 SQL 注释后**）出现 S4 门控标识符 ⇒ 必须 `Gated:true` 或具名登记。
配套 `TestGatedFlagExemptionIsNotStale` 查反方向（失效的豁免比没有豁免更坏）。

**不一刀切禁止**：「文件里有护栏」**不能**推出「登记的读点被门控」——
护栏可能在别的读点上、可能在调用方。**把「在」报成「不在」比没有门更坏。**

### 写门时我自己踩的假阳性面

第一版只剥 Go 注释，在 `bg/auto_route_affinity_worker.go` 上误报——
**那层注释藏在 raw string 里的 SQL 注释**（`-- settings.KeyRequestLogsWriteEnabled …`），
而那个文件**根本没有 Go 层护栏**。⇒ 第一版会**逼人写假豁免**。
已改三段剥离，SQL 段**复用包内已有的 `stripSQLLineComments`**（不另起同名正则，避免编译冲突）。

### 逐条裁定：3 条要豁免，3 条要订正

- **豁免**（`Gated:false` 本来就对）：`internal/trace/trace.go`（护栏在 `FlushToPG` 护写点，
  读点在独立函数 `LoadFromPG`）、`telemetry/client.go`（护栏**全在写路径**，两个登记读点都不在其中）、
  `auto_route_affinity_worker.go`（只有 SQL 注释，修好剥离后自动退出）。
- **订正为 `Gated:true`**：`ledger_reconciliation`（护栏在 `checkUsageCredit` 首行，SQL 在其后发出）、
  `credential_recovery`（`return` 在 :1933，SQL 在 :1939）、`discovery`（`staleExpiryMayRun` 消费于 :1110）。
  三条都**清空了 `BlastRadius`**——停写期间那些写入根本不会发生，留着等于声明一件不存在的事。
  并把「若护栏被回退，退化路径是什么」写进各自 Note：**护栏可被回退，Note 是那份路径的唯一记录。**

### 变异 4/4，各命中不同门

M1（复现 `af4ef4b32` 时的历史状态）红在 `:139`；M2 抽豁免红在 `:131`
（**行号上移是因豁免被删了 8 行——两次行号不同恰恰说明两次变异都生效了**）；
M3 红在 `:139`；M4 加失效豁免红在过期自检门 `:179`。

*M1 第一版**没注入成功**（gofmt 改了对齐空格数，锚点没匹配，测试照常 ok）——
**没生效的变异不是证据**，重做后才算数。*

### 遗留的真缺口（本轮发现，下一件该做）

`SkippedChecks()` 是机器可读的「本轮未执行」通道，`ledger_reconciliation` 与
`credential_recovery` 都调用了它，而**全仓消费者只有测试**（两个 `*_s4_gate_test.go`）
——没有 metric、admin 端点或告警。
⇒ **护栏把危险动作停了，但「为什么没动作」只留在 `slog` 里**；返回值 0 在计数上仍与
「扫了没发现差异」不可区分。这是 §9.36.3 的 19 条里 control-plane 三条中的第三条。

### 仍未做

`auto_route_settle_worker` 的**正确修法**（改读会话族）——§9.35 只让它可见，没让它正确。
族分类器的 `id` 误触发（需加表归属，可一次性消掉 6 条假论证）也仍未做。

---

## 第二十一轮（2026-10-02）：`SkippedChecks()` 的可观测出口 —— §9.38

### 结论

第二十轮列的「下一件该做」做完了。**护栏把危险动作停了，现在也看得见了。**

### 缺口是可证实的，不是推测

`SkippedChecks()` 全仓 grep 的消费者 = **2 个 s4_gate 测试，零生产消费者**；
`settings.RequestLogsWriteEnabled()` 本身**也没有任何指标** ⇒ S4 停写这个状态
在 `/metrics` 上完全不可见。

**既有累计计数器救不了**：`llmgw_recovery_lookback_triggers_total{outcome=
"skipped_s4_stop_write"}` 停写生效后**停止增长**，而「计数器不再增长」与
「worker 卡死」在告警侧同形。要回答的是**当前状态** ⇒ 补 gauge，不补 counter。

### 三个指标 + 三条告警（每个指标都有消费者，一个不多）

| 指标 | 告警 | 回答 |
|---|---|---|
| `llm_gateway_bg_s4_scan_skipped_last_run{worker,reason}` | `BgS4ScanSkipped`（10m） | 本轮没执行吗 |
| `llm_gateway_bg_s4_scan_last_run_unix{worker}` | `BgS4ScanStalled`（3600s/15m） | worker 还活着吗 |
| `llm_gateway_bg_s4_scan_unregistered_skip_total{worker,reason}` | `BgS4ScanUnregisteredSkipReason` | 指标本身在谎报吗 |

**刻意不加**累计型 `..._skip_total`——没有告警消费者的计数器就是装饰。
接线一律用 **`defer`**，覆盖所有 return 分支（含未来新增的）。

### 默认拒绝：新跳过源不登记就门红

- **源码侧**：按**签名**（具名结果 `(comparable bool, reason string)`）而非函数名白名单
  扫描全部可产出 reason ⇒ 新增跳过源时门不会静默放过。
- **运行时侧**：未登记 reason 落兜底计数器 + `slog.Error`，**不写进闭集 gauge**。
  理由：gauge 的标签空间必须严格等于闭集，否则告警表达式会依赖未登记标签值，
  而 gauge 停在 0 会把「跳过了」**谎报成**「跑过了」——比没指标更坏。

### 顺带修掉一个真 bug

`scanLookbackRecoveries` 的 hook 早退**原本在 `r.resetSkipped()` 之上** ⇒
「什么都没做的一轮」返回**上一轮**的 skip 列表。当时是潜在的（hook 构造后不变），
但**一旦把列表发布到 `/metrics`，潜在 bug 就升级成运维可见的谎报**。
已上移并立门钉住顺序。

**把潜在 bug 接上告警 ≠ 顺手美化**：那是把它从「没人看得见」升级成「所有人看见错误的值」。

### 我自己这道门先写错了三次（都记在审计 §9.38.5）

1. `for _, x := range someString` 迭代的是 **rune 不是行** ⇒ GW-00 守卫报
   `108 could not be applied builtin len()` ⇒ **那道守卫一行都没真正检查过**。
2. 闭集门把 comparability 成功分支的 `return true, ""` 当成了 reason。
3. `r.resetSkipped()` 的 `Fun` 是 **`*ast.SelectorExpr`**（带接收者），
   我只判了 `*ast.Ident` ⇒ 门红在一个**从未存在过**的缺陷上。
   （§9.35 记过反向版本：接线门只认 SelectorExpr 而包级函数是 Ident。**两种都要认**。）

**一个恒红或恒「误报不存在缺陷」的门比没有门更坏**——它训练读者忽略自己。

### 变异 6/6，各命中不同断言

M-D 归零循环只写 1 → `:253`；M-B `defer` 降级为普通调用 → `:152`；
M-C `resetSkipped` 挪回早退下 → `:226`；M-A 返回未登记 reason → `:102`；
M-E 抽掉兜底计数器 → `:275`（vet 干净 ⇒ 确认断言红）；M-F′ 规则数不变只换指标名
→ `s4_scan_skip_test.go:61`。

**两次「变异没生效」当场识破并重做**：
- M-C 第一版只加了标记、**位置根本没动** ⇒ 门正确地绿。若记「通过」就是拿没生效的变异当证据。
- M-E 第一版写出多余 `}` ⇒ 未使用导入 ⇒ **编译红**。编译红不是断言红。
  保留日志只抽计数器后重跑才算数。
- M-F 第一版（删整条规则）报在 `Len(...,3)` 这个结构断言上，只证明规则数变了。
  补做 M-F′ 才确认**覆盖断言**承重。

### 明确不做：admin 端点

`admin.Handler` 已持有 `credRecov`（`handler.go:78`）零接线可达，
但 `LedgerReconciler` 只是 `main.go:4838` 的局部变量、从未注入 Handler ⇒
要做就得改 `main.go`（共享工作区冲突面最大的文件）去重复 `/metrics` 已承载的事实。
**判据是新字段先问「哪道门会读它」，同理适用于新端点**：没有第二个消费方就不开这个面。

### 仍未做

- 族分类器 `id` 误触发（补位匹配加表归属，可一次性消掉 6 条假论证）。
- `auto_route_settle_worker` 的正确修法（改读会话族）——§9.35 只让它可见，没让它正确。
- 响应侧 7 个读点仍不可端口。
- §9.36 的 19 条静默档（`silently_empty` 11 / `silently_degraded_content` 4 / `silently_frozen` 4）
  才是灰度前真正要处理的，不是「门红了」。

### 下一轮提示词

> 修族分类器的 `id` 误触发：给补位匹配加**表归属**约束（`NULL::bigint AS id` 只在
> session 臂成立，v1 臂应透传真值 `SELECT rl.id,`），可一次性消掉 6 条假论证。
> 动手前先按 §9.38 的纪律做两件事：①`pg_get_viewdef` **逐字**读三条 UNION ALL 臂
> 各自对 `id` 的写法（上轮我把 L1 顶层投影和 L146 的 v1 分臂数混了，
> 真实情况是 L1/L146 补 NULL 而 **L291 是 `SELECT rl.id,` 透传真值**——
> 「`id` 永不投影」只对会话侧成立，v1-only 的行必须带着真实 `request_logs.id` 出去）；
> ②报告任何计数时写清**量的面**（迁移文件 / 活库 viewdef / Go 常量），三者数字不同。
> 另注意：`cloneTablesFrozenDDL` 只发列名+类型、不带 NOT NULL 与 DEFAULT，
> 所以「v1 臂 id 必须为 NULL」在夹具里是 NULL 对 NULL 平凡通过——
> 要它可红必须种显式值并断言**等于源表真值**（只断言非空太弱，turn id 也非空）。

---

## 第二十二轮（2026-10-02）：族分类器的 `id` 误触发 —— 表归属 —— §9.39

### 结论

§9.36.4 记的第 2 条相邻缺陷做完了。**族从 19 收到 12，档位一条没动。**

### 根因

`np`（谓词级 NULL 补位）只判断「补位列名在**这个文件里出现过**」，
而「出现过」≠「用在这个视图上」。`id` 撞得最多，因为列同名表不同。

### 归属粒度选错两次，两个方向都踩过（这次是量出来的，不是想出来的）

1. **整文件口径**（原实现）：19 个本族文件里 **8 个**是这么带进来的。
2. **字符串字面量口径**（我第一版）：`strictOnly = 0` 看着很美，直到手验
   `bg/shared_pick.go` 发现它是**拼接 SQL**（中间夹 `<ProbeTrafficExclusionPredicateView>`
   占位）⇒ 逐字面量口径会把这条**真谓词**判成误触发。
   **一个更「精确」的判据反而更危险，因为它悄悄放走了真触发。**
3. **函数作用域 + 包级字面量**（最终）：函数体 ⊇ 单个字面量 ⇒ 口径 2 能命中的它一定命中。

### 7 个离开本族的文件，`id` 实际属于谁（逐个打开确认）

| 文件 | 属于 | 行号 |
|---|---|---|
| `admin/auto_title_generator.go` | `api_keys`（`ak.id`） | :1174-1182 |
| `admin/logs_summary.go` | `api_keys` + Go 正则字面量的 `correlation_id` | :303-308, :28 |
| `admin/session_title.go` | `t.id`，`t` 是 session_turns 别名（视图别名是 `rl`） | :328 |
| `bg/stats_minute_rollup.go` | `request_stats_rollup_cursor`（`WHERE id = 1`） | :94/:115/:131 |
| `domains/routeincident/store.go` | route_incidents 表自身 | :227/:310/:387 |
| `bg/candidate_failure_monitor.go` | `candidate_failure_logs` 腿 | :397 |
| `admin/session_turns_tree.go` | 只在投影，有非补位列 COALESCE 兜住 | :228/:321 |

### 我自己写的方向性门当场抓到了我的实现缺陷

`TestNullPaddedAttributionNeverLosesALiteralLevelHit` 断言「函数体 ⊇ 字面量」，
第一次跑就报红 `domains/sessionforensics/export.go` 与
`domains/streaming/model_alternatives.go`——两者的 SQL 都是**包级 `const`**
（`forensicsExportMessagesSQL` :37/:57、`alternativesSQL` :180），整段在函数之外。

**没有这道门，这个缺陷会一直绿着**：它只表现为「少认了几个文件」，
而少认的方向恰好是本轮要修的方向，看起来像正常进展。

### 顺带抓到的第二个自己的错：注释在说用原文、代码在用剥过的

第一版 `sourceFamilyOf` 注释写「np 用的是原始 code」，
但函数开头已把 `code` 覆盖成剥过注释的版本 ⇒ `admin/logs_summary.go`
靠「解析失败退回整文件」fallback 留在本族——**一个已离族的误触发被 fallback 悄悄请回来**。

判别它的不是读代码，是**列出每个文件的 `via` 列**：`logs_summary.go` 的 `via` 是空串
而它在族里，两个判据对不上。已加「归属命中与族归属必须一致」的一致性断言。

### ⚠ 如实说：删 5 条失效豁免是**降低了**门槛

`nullPaddedUnaffectedJustification` 删掉 5 条（`session_timeline_query` /
`session_turns_tree` / `session_turns_unified` / `candidate_failure_monitor` /
`gateway_adapters`），并补 `TestNullPaddedJustificationIsNotStale` 常驻检查。

这 5 个文件从「null_padded + unaffected ⇒ 必须有具名论证」变成
「view_only + unaffected ⇒ **无要求**」。它们的 unaffected 判断现在只靠族层面的事实
（真库实测 24h 内 36.55% 的视图行来自 session_turns），不再有逐文件书面论证。
被删论证的实质内容已抄进审计 §9.39.6，但**不再被任何门强制更新**——本轮引入的已知弱化。

### 变异 3/3

M1 塞一条失效豁免 → `attribution_test.go:77`；
M2 把归属退回整文件口径 → 同文件 `:144`，**4 个已知误触发全部被抓**；
M3 去掉包级字面量作用域 → 同文件 `:200`（vet 干净 ⇒ 确认断言红）。

*M3 第一版写出未使用变量 ⇒ **编译红**；编译红不是断言红，补 `_ = inAnySpan` 后才算数。*

### 仍未处理的已知不精确

`domains/streaming/model_alternatives.go` 的视图名只出现在**字符串字面量里的
SQL 注释**（`-- request_logs_with_current_month is a UNION of …`，:230）。
本轮**没动 `vi` 那一维**的注释处理——它是另一个维度的语义，动它会把该文件整个换族。
它因此留在本族（保守方向，安全）。本仓已有 `stripSQLLineComments`，
但用它会**同时改变 `vi`**，必须单独评估，不能顺手带进来。

### 下一轮提示词

> 处理 `domains/streaming/model_alternatives.go` 这类「视图名只出现在 SQL 注释里」的读点：
> 先量清 `vi` 维度受影响的**完整文件清单**（对每个含 `request_logs_with_current_month`
> 的文件，比较「剥 SQL 行注释前后 `vi` 是否变化」），再决定要不要用
> `stripSQLLineComments`。注意三条纪律：①**先量影响面再改判据**，
> 这次一个 6 列的判据改动就已经牵动 7 个文件的族归属；②复用已有的
> `stripSQLLineComments`（包内已有 `sqlLineCommentRE`，另起同名包级正则会编译冲突）；
> ③`vi` 与 `np` 是**两个独立维度**，动 `vi` 的注释处理会连带改变 `np`
> （同一份代码两处都读），必须分别验证。
>
> 另一件更该先做的：`auto_route_settle_worker` 的**正确修法**（改读会话族）——
> §9.35 只让它可见（`outcome_source` 块 + 陈旧基线告警），停写 8h 后仍会全量 abandon，
> 产物与「正常放弃」逐字段同形。可见 ≠ 正确。

---

## 第二十三轮（2026-10-02）：`auto_route_settle_worker` 的正确修法 —— 实测**否决**了两个方案 —— §9.40

### 结论（先说，因为它是否定的）

第二十二轮提示词里我说「下一件该做 `auto_route_settle_worker` 的正确修法」。
**我做了，结果证明我上一轮给的修法建议本身是错的。** 本节的价值在证伪，不在实现。

§9.35 和 handoff 里都写着「正确修法是改读会话族」。实测后：
**方案 A（换读 710 视图）被计划否决，方案 B（换读会话族）不是等价替换。**
三个读点**一个都没动**。

### 方案 A：710 视图 —— 不是被报错否决，是被**计划**否决

真库 EXPLAIN 实测 worker 真实的 `settleBatch` LEFT JOIN：
710 视图的 v1 臂（citus 父表 `request_logs`）被展开成 **7 个叶子分区 Seq Scan**
（request_logs_2026_07/08/default + 09/10/11 的索引扫描）。
文件顶部注释描述的 `invalid perminfoindex` 报错**没有复现**。

> **一个「没报错但计划烂掉」的替代方案比报错那个更危险**——报错会让人停下来看，
> 计划不会。门里写的是「实测到的**计划**代价」，不是复述报错。

### 方案 B：会话族 —— 99.3% 可平移，但有两处硬伤

2026-09 分区实测（1747 条 selection；`auto_route_selections_hot` **当前是空的**，
本地库没这类流量，只能用 9 月分区——这是取样妥协）：

| 维度 | v1 | 会话臂 | 判定 |
|---|---|---|---|
| `request_id` 覆盖 | 1746 | 1734（99.3%） | 可平移 |
| `success` / `latency_ms` | 100% / 99.3% | 100% / 100% | 可平移 |
| `cost_usd` | **3.6%** | **100%** | 会话臂**更好** |
| session 身份 | `gw_session_id` 100% | `session_id` 100%（0 条 `sys:%`） | 可平移 |
| `origin_actor` | **0** | **0** | **两侧都空** |
| `canonical_id` | **32.3%** | **1.9%** | **真退化** |
| `is_auto_request` | 99.9% | **83.0%** | **真退化** |

1. **`canonical_id` 会话族里根本没有来源**（`session_turns`/`session_turn_details` 都没这列，
   查 `information_schema` 确认）⇒ LATERAL `retry_count` 腿**无法平移**。
   **架构缺口，不是工程问题。**
2. **`is_auto_request` 差 17pp** ⇒ `loadTaskBaselines` 的 cohort 缩水 17%。

**顺带更正我自己一个错误**：我原本把 `origin_actor`（会话臂 100% NULL）列成移植障碍，
实测 v1 侧**同样是 0** ⇒ `SQLExcludeSyntheticActors` 对这批行**早就空转**。
它不是移植的障碍，但意味着「排除合成流量」这个假设**在本 worker 上已经不成立**
（影响现在的基线质量，与停写无关），应单独记账。

### 取样方向：第一轮测量差点得出**相反结论**

用今日 hot 测：v1 的 auto 行 **0%** 配到会话臂、会话臂 **0** 条 auto 行
⇒ 按那个数就该直接否决方案 B。

**那是错的**：今日 hot 的 2172 条 auto 行里 **2158 条是 `probe-%`**
（`origin_actor` = `active-probe-worker`/`node-probe-worker`）——探针流量，会话写方不覆盖。

> **判一个 join 腿能不能平移，取样必须是 join 的另一侧**（`auto_route_selections`），
> 不是「上游表 + 某个标签」。按标签取样会拿到另一个总体。

### 我自己那道门第一版假阳性、又太弱（两次都记在这）

- **假阳性**：对整份源码跑 `(?i)JOIN\s+request_logs\s`，命中第 15 行**注释里的散文**
  `//  3. Join request_logs for success / latency / cost.` ⇒ 改成只对 **AST 提取的
  字符串字面量**跑判据。
- **太弱**：文档门查「注释里有没有 `canonical_id`」，而这词在文件里出现十几次
  （SQL 里就 6 处）⇒ **删光整段实质文档它照样绿**。⇒ 钉到只有实质文档才有的
  **特征句**（`会话族里根本没有这一列`、`叶子分区 Seq Scan`）。

**一道删掉它所守之物之后仍然通过的判据，就是装饰。**

### 变异 2/2（N2 第一版「没生效」被当场识破）

N1 换 710 视图 → `auto_route_settle_source_gate_test.go:87`；
N2 删掉 `canonical_id` 缺口整段 → `:133`（钉特征句之后才真正承重）。
*N2 第一版只在一行末尾加标记，而那行本来就不含 `canonical_id` ⇒ 门正确地绿。*

### 落地了什么

- `bg/auto_route_settle_source_gate_test.go`（新）2 道门。
- `bg/auto_route_settle_worker.go` 文件注释写入完整实测评估（含被否决的两个方案）。
- 登记表 `bg/auto_route_settle_worker.go` 的 Note **订正**——原文写着
  「正确修法是改读会话族」，现改为带实测数字的证伪结论。

**三个读点没动**：正确修法要先决定 `canonical_id` 缺口怎么办，而那会改动
**reward 语义**；在本地 hot 分区为空、无法验证运行时行为的环境里改 reward 输入，
是拿「看起来更正确」换「无法验证」。

### 下一轮提示词

> 需要**一个明确决定**，不是更多审计：停写后 `auto_route_settle_worker` 的
> `retry_count` 怎么办？三个选项，各自的代价要先写清再选：
> ① **回填 `canonical_id` 到会话族**（`session_turns` 加列 + 写方补齐）——
>    代价是会话族多一个 v1 概念，且要处理 32.3%→100% 的历史缺口；
> ② **改写 retry_count 定义**，去掉 canonical_id 等价条件，改用别的会话内标识
>    （`outbound_model` + 时间窗？`request_checksum`？）——代价是 reward 语义变化，
>    线上已有的 reward 分布不可比；
> ③ **接受降级**：停写后 retry_count 恒 0，但把它变成**显式信号**而非静默 0
>    （复用 §9.38 的 `outcome_source` 模式，加一个 `retry_count_unavailable` 档位）。
>
> 选之前必须先量两件事：①`auto_route_selections` 里的 `canonical_id` 现在
> **实际有多少可回填**（join 到 session_turns 的命中率，**用 9 月分区**，
> 别用空的 hot）；②reward 分布对 `retry_count` 的实际敏感度——
> 如果 0 与真实值在现有 reward 函数下差异很小，选项 ③的成本最低。
>
> 另：§9.39 遗留的 `vi` 维度 SQL 注释处理仍未做（`model_alternatives.go`），
> 以及 §9.38 的告警 `for:` 阈值未在真机 Prometheus 验证过。

---

## 第二十四轮（2026-10-02）：`retry_count` 的推导对真实数据**从未成立过** —— §9.41

### 结论

§9.40 说「下一件需要一个决定」。我去量决定所需的数字，**撞上一个与停写完全无关的
现存缺陷**：`retry_count` 的 SQL 表达式对真实数据**从来没有正确执行过一次**。

### 缺陷

`settleBatch` 的 LATERAL 原本是：

```sql
SUM(GREATEST(COALESCE(jsonb_array_length(r2.routing_attempts), 1) - 1, 0))
```

它假设该列是 JSON **数组**。写方 `ToJSONBytes` 产出的是
`map[string]interface{}{"attempts": [...]}` —— **object**。真库实测：

```
ERROR:  cannot get array length of a non-array
```

**且 `array` 形态在 `request_logs` 里从未存在过**：340,917 行、最早 2026-09-03，
全部 `object`。

### 失败范围是整条查询，不是一行

LATERAL 在主查询里，一行抛错 ⇒ 整条 `settleBatch` 中止 ⇒ **该批最多 500 条**
（`settleBatchSize = 500`）selection 全不结算；调用方只 `slog.Warn` 后 return，
下一轮重查**同一批** ⇒ 反复卡死，不是一次性丢一批。

实测影响面（2026-09 分区 1747 条）：

| 情形 | 条数 | 占比 |
|---|---|---|
| `canonical_id IS NULL` ⇒ LATERAL 恒空、**不报错** | 1183 | 67.7% |
| 有 canonical_id、匹配行全无 `routing_attempts`、不报错 | 104 | 6.0% |
| 有 canonical_id、匹配行带 `routing_attempts` ⇒ **整批中止** | **460** | **26.3%** |

### ⚠ 顺带量化一个**方向相反**的偏差（停写关闭的今天就在发生）

`RetryRatio` 只在 `*modelReqsInSes > 0` 时赋值 ⇒ 那 **67.7%** 的 `model_reqs=0`
⇒ `RetryRatio=0` ⇒ `retryScore = 1.0`（**满分**）。retry 权重 **0.10**。

> **三分之二的已结算 selection 在白拿 retry 项满分**，「没测到」被当成「测到完美」。

`ComputeRoutingReward` 注释写的「Unknown inputs resolve to neutral 0.5 rather than 0,
so 'not measured' is never mistaken for 'measured as bad'」——**这条不变量在
`RetryRatio` 上不成立**：未测得 → 0 → `1.0 - 0` = **最好**，不是中性。
这是「把缺失报成在场」族，且方向是**抬高**。

### 错误的第二处化身

`executors/routing_tracker.go` 注释原文写着
`retry_count = jsonb_array_length(routing_attempts) - 1`——**那正是消费者的 bug**。
把 bug 写进写方注释＝给下一个人发一份错误契约。已订正为
`len(routing_attempts -> 'attempts') - 1` 并立门禁止它回来。

### 修法与验证

抽成具名 `retryCountPerRowSQL(alias)`（可静态测），按 `jsonb_typeof` 分派：
`'array'` 走整列（防御性，真库从未出现，删掉它会让假想情形**静默退化成 0 重试**）；
`'object'` 走 `-> 'attempts'`（主体）；外层 `WHERE jsonb_typeof(v.a)='array'`
守卫让畸形值退化为「这行算 0」而非整条查询中止。

真库同一批数据：旧 → `ERROR`；新 → 1002 行、retry 总数 **1669**、无报错。

### 门 3 道，变异 3/3

P1 删 `-> 'attempts'` → `:50`；P2 整段退回旧裸调用 → `:50`（4 条缺失）+ `:58`
（「裸调用」专用判据）；P3 把错误契约放回写方注释 → `:101`。

**为什么是形状门不是集成测试**：`auto_route_selections_hot` 当前是空的、
本地 CI 库没这类流量 ⇒ 需要真实行的集成测试在这里**证明不了任何事**
（§9.34：空库上的真库门是绿而无证据）。所以断言可静态证明的那一侧。

### ⚠ 我自己写错了一处（已改）

§9.40 我在 worker 注释里写「每 30 秒跑一次、每次 **100** 行」——
实际 `settleBatchSize = 500`。已订正。**写注释时引用的常量要回查定义，不要凭印象。**

### 仍未修：retry 项「未测得 ⇒ 满分」

修它等于改 reward 语义（让未测得落回中性 0.5？还是按可用性开关该权重？），
与 §9.40 结尾那个「需要一个明确决定」是**同一个决定**，应一起做，不宜夹带。
已记为本轮**新发现的现存缺陷**（与停写无关）。

### 下一轮提示词

> 仍然需要**一个明确决定**，现在有两件事绑在一起，都改 reward 语义：
> **(1) `retry_count` 的数据源**（停写后 `canonical_id` 在会话族无来源 ⇒ LATERAL 腿无法平移）
> **(2) `RetryRatio` 未测得 ⇒ `retryScore=1.0` 满分**（67.7% 的 selection 现在就在白拿 0.10 权重）
>
> 三个候选，建议**一次做完**：
> ① **让 retry 项显式三态**（measured / unmeasured / unavailable），
>    `unmeasured` 落回中性 0.5 而不是满分 1.0，同时停写时给 `unavailable`
>    —— 一处改动同时解决 (1) 和 (2)，且不依赖「回填 canonical_id」这件大工程；
> ② 回填 `canonical_id` 到会话族（解决 (1)，但 (2) 仍在：没测到还是满分）；
> ③ 接受现状只加指标（最省事，但 67.7% 白拿权重这件事继续存在）。
>
> 选 ① 之前必须先量：**这 67.7% 里 `model_reqs` 本来会是多少**——
> 若它们在会话族里也能算出真实 `model_reqs`（只是 `canonical_id` 缺失），
> 那 (1) 的成本远低于「回填」，因为 LATERAL 的 `r2.canonical_id = s.canonical_id`
> 这一条可能可以去掉、改用别的会话内标识。用 9 月分区量，别用空的 hot。
>
> 另：§9.39 遗留的 `vi` 维度 SQL 注释处理（`model_alternatives.go`）仍未做；
> §9.38 的告警 `for:` 阈值未在真机 Prometheus 验证过。

---

## 第二十五轮（2026-10-02）：retry 项去掉 canonical_id 收窄 + 显式三态 —— §9.42

用户拍板「一次做完」。§9.41 结尾那两件事（retry_count 数据源、RetryRatio 三态）
一次落地。

### 拍板前的测量把成本结构翻转了

去掉 LATERAL 的 `canonical_id` 条件后（1747 条 selection）：

| 组 | 条数 | 带条件 model_reqs | 去掉条件 |
|---|---|---|---|
| A：`canonical_id IS NULL` | 1183 | **0.00** | **1.00**（1182/1183 有匹配） |
| B：有 canonical_id | 564 | 1.00 | 1.00 |

A 组分布 min 0 / 中位 1 / p99 1 / **max 1** ⇒ 会话本来就只有 1 个请求。

**跨模型污染实测只占 0.01%**（10 天、11,634 个 auto 会话：单请求 97.12%、
多请求·同模型 2.87%、多请求·**跨模型** 1 个）⇒ **代价 67.7%，收益 0.01%，净负**。

### 三态

```
measured     model_reqs > 0                 ⇒ 1 - retry/model_reqs
unmeasured   model_reqs == 0（无可数行）    ⇒ 0.5 中性
unavailable  指针 nil（LATERAL 无产出）      ⇒ 0.5 中性
```

`unavailable` 必须与 `unmeasured` 分开：前者是「读不到」（停写后 worker 永久
处于此态），后者是「读到了确实是 0」。合并就丢掉了停写时唯一能看见的信号。

`RewardInput` 新增 **`RetryMeasured bool`** 而不是 `-1` 哨兵：`RetryRatio` 是
**比值**，0 是合法实测值；`HealthComponent` 是**分数**，-1 才可安全保留为哨兵。
**把 0 复用成「未知」正是这个 bug 的成因。**

### 暴露走指标不走列

`reward_source` 有 CHECK 约束 `IN ('request','session')`（db/db.go:7764），
扩展需迁移 ⇒ 新增 `llmgw_autoroute_settle_retry_state_total{state}`（闭集三值）。

### ⚠ 两个既有测试把**错误语义写成了期望值**

- `TestComputeRoutingReward_UnknownsAreNeutralNotZero`：**测试名叫
  「UnknownsAreNeutral」，retry 项却按 `0.10*1`（满分）算**，期望 0.775。
  改 0.725。**一个把错误值钉死的「回归测试」比没有测试更危险**——它让 bug
  看起来是被保护着的。
- `TestComputeRoutingReward_Ordering`：排序断言靠改 `RetryRatio` 让 reward 变动，
  必须加 `RetryMeasured: true`，否则 retry 项中性、`retried` 与 `good` 打平。

新增 `TestComputeRoutingReward_RetryIsTriState`：未测得必须**恰好落在**两个实测
极值的中点（权重线性），且不得等于任一端。

### 门 3 道 + 1 语义门，变异 4/4

Q1 canonical_id 条件加回 → `auto_route_retry_state_test.go:42`（两条）；
Q2 三态退回旧语义 → `affinity_test.go:308`/`:318`；Q3 指标不接线 → `:86`。

① 是**删代码**，所以专门立门钉住它不在——删掉一个「看起来是防御性收窄」的条件，
下一个人很容易觉得必要而加回来，而没有任何测试能证明它不在了。

### ⚠ 残余风险

- **线上 reward 分布会位移**：约 2/3 样本的 retry 项从 1.0 变成 0.5 或实测值。
  方向朝正确，但**与历史 reward 不可比**——依赖绝对 reward 阈值的东西需一并复核。
- **0.01% 对当前流量形态成立**：数据被探针流量主导（探针天然单请求会话）。
  若日后多轮对话占比大幅上升，跨模型比例会变，收窄条件可能需以别的形式加回来
  ——那时用 `RetryMeasured` 区分「测到 0」与「没测到」，不要靠匹配不上隐式表达。
- **未经线上端到端验证**：本地 hot 无 selection 行，只在真库验了表达式本身。

### 仍未做

- §9.39 遗留：`vi` 维度 SQL 注释处理（`model_alternatives.go`）。
- §9.38 的告警 `for:` 阈值未在真机 Prometheus 验证过。
- §9.36 的 19 条静默档；`auto_route_settle_worker` 停写后仍会全量 abandon
  （`canonical_id` 在会话族无来源 ⇒ 该腿无法平移），现只靠 `outcome_source` 可见。

### 下一轮提示词

> 停写后 `settleBatch` 的 outcome join（`LEFT JOIN request_logs_hot rl ON
> rl.request_id = s.request_id`）仍会**整条落空** ⇒ `p.success == nil` ⇒
> 过 4h abandon 窗口后全量 abandon，reward 永久为 NULL。这是 §9.35/§9.40
> 一直挂着的那条，retry 项做完之后它是**最后一条**。
>
> 动手前先量清楚一件我至今没量的事：**停写后 selection 与会话臂的 request_id
> 到底能不能配上**。§9.40 测的是 2026-09 分区（99.3% 能配），但那是**停写前**
> 的双写状态。停写后 v1 停止新增、只有会话臂在写 ⇒ 理论上配得上，但需要确认
> `auto_route_selections.request_id` 与 `session_turns.request_id` 是**同一个
> 标识**（不是 selection 自己的 id）。
>
> 若能配上，正确的修法是：outcome join 改读会话族（success / latency_ms /
> cost_usd / origin_actor 实测 100% 有值，cost_usd 还会话臂更好），
> **并用 §9.42 的三态**把「配不上」与「配上了但失败」分开——否则又会退化成
> 「没测到 = 满分」那一族错误。注意 origin_actor 会话臂两侧都是 0，
> 所以 `SQLExcludeSyntheticActors` 早就在空转，别把它当成移植障碍。
>
> 顺带：`retryCountPerRowSQL` 里的 `'array'` 分支是真库 34 万行从未走到的
> 防御分支，若你确认写方不会改回数组，可以考虑删掉它并把门相应放宽——
> 但删之前先确认 `RoutingAttemptsTracker.ToJSONBytes` 没有别的调用方在产出数组。
