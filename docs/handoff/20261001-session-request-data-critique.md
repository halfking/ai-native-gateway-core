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

---

## 第二十六轮（2026-10-02）：settleBatch 数据源按 S4 写门切换 —— §9.43

§9.40/§9.41/§9.42 把 `canonical_id` 那个阻塞拆掉之后，移植做完了。

### 先验技术可行性（三项全过）

- **无 citus / 列存问题**：`citus_tables` 里 `request_logs*` 与 `session_turn*` 都不在；
  `session_turns_hot` 是 heap。
- **计划干净**：`Nested Loop Left Join` + `Index Scan using idx_session_turns_hot_request`，
  与现状同形，**没有** §9.40 那个 7 分区展开。
- **列全齐**：`request_id / session_id / routing_attempts / success / latency_ms /
  cost_usd / origin_actor / canonical_id / tenant_id / ts` 全部存在，
  且 `routing_attempts` 形态与 v1 **一致** ⇒ §9.41 的 `-> 'attempts'` 原样可用。
  LATERAL 只需把 `gw_session_id` 换成 `session_id`。

### 为什么按门切换而不是直接换

直接换会让**停写前**也变：会话臂覆盖率 99.3%（0.7% 当场失去 outcome）、
`is_auto_request` 只有 83%（基线 cohort 缩水 17%）。目标要求「数据在更改前后一致」
⇒ 写门开着读 v1（**与今天逐字相同**），关掉后读会话族。

与 §9.35「不门控 worker」不矛盾：那条说的是**不要门控 worker 的执行**；
这里门控的是**读哪个族**，目的是让切换前行为不变。

### 三条腿必须同源

outcome join / LATERAL / `loadTaskBaselines` 由同一个 `settleSourceSpec` 驱动。
若各读各的族，p95/p75 与被它归一化的 latency 不在同一批行上算——**比缺数据更隐蔽，
因为数字都有值**。

### 默认方向 + 可观测

`settleSourceFor` **默认 v1**（`GetPlatformBool` 未初始化时返回 true = 写门开着；
若默认会话族就会在任何配置下悄悄改源）。新增
`llmgw_autoroute_settle_source_total{family}`，`init()` 预置两条序列。

### 门 4 道，变异 2/2

M1 LATERAL 腿退回硬编码表名 → `auto_route_settle_source_test.go:74` + `:81`；
M2 默认方向反转 → `:30`/`:33`/`:36`。

**门自己抓到一个真问题**：首次跑就报 `src.TurnsTable 只出现 2 次`——
`loadTaskBaselines` 写的是 `currentSettleSource().TurnsTable` 而非命名变量。
三条腿都用了规格但**形态不一致**，已统一。门抓的不是缺功能，是一致性能腐化。

*M1 第一版把 baseline 腿也硬编码 ⇒ `src` 未使用 ⇒ **编译红**；编译红不是断言红，
改成只动 LATERAL 腿后才算数。*

### ⚠ 残余风险

- **停写后的运行时行为未验证**：本地 `auto_route_selections_hot` 为空，
  会话族分支从未在真实 selection 上跑过；真库只验了计划与列齐备性。
- `is_auto_request` 在会话族 83% ⇒ 停写后基线 cohort 比 v1 期小 17%，
  p95/p75 与历史不可比（数据事实，非实现缺陷，运维应知情）。
- baseline 与 settle 是两次查询 ⇒ 若切换恰在两次之间，一轮的 baseline 来自新族、
  结果来自旧族。窗口 < 1 轮、下一轮自愈；已记录未处理。

### 下一轮提示词

> `auto_route_settle_worker` 已不再**必然**全量 abandon，但**从未在真实 selection 上
> 验证过会话族分支**。最该做的是补一个能真正跑起来的验证：
>
> ① 本地 `auto_route_selections_hot` 是空的、CI 库也没这类流量 ⇒ 集成测试证明不了
>    任何事（§9.34）。要么在 `cmd/tools/` 下加一个**本机核对工具**（像
>    `validate_sessions_v2` 那样，不进 CI），用真库造几行 selection + session turn
>    然后跑一遍 settleBatch，比对 v1 源与 session 源两条路径的 reward；
>    要么接一个 testcontainers 的一次性库。**别做成 CI 门禁**——空库上它只会
>    SKIP 并给出「绿而无证据」。
> ② 切换的那一刻要能看出来：现在只有 `llmgw_autoroute_settle_source_total` 的
>    曲线变化。考虑给 `s4_scan_skip.yml` 那种告警补一条
>    `changes(llmgw_autoroute_settle_source_total[10m]) > 0`，
>    让「源族已切换」成为一个**事件**而不是需要人去盯的曲线拐点。
> ③ 仍未做：§9.39 遗留的 `vi` 维度 SQL 注释处理（`model_alternatives.go`）；
>    §9.38 告警 `for:` 阈值未在真机 Prometheus 验证；§9.36 的 19 条静默档；
>    响应侧 7 个读点仍不可端口。

---

## 第二十七轮（§9.44）：会话族分支第一次被执行 + 基线 cohort 静默塌陷的发现

上一轮留下的 ①（补一个能真正跑起来的验证）②（切换事件告警）**本轮都做了**，
但做的过程里翻出了一个比这两项都更重要的问题，所以本轮的主线其实是第三件事。

### 结论先行

1. **会话族分支现在真的跑过了**，并有了一道证明「同一批数据在两族上 reward
   逐位相同」的集成测试（testcontainers，`//go:build integration`）。上一轮说
   它「从未执行过」——本轮之前确实从未执行过。
2. **新发现**：切到会话族会让 `auto_route_selections_hot` 的基线 cohort 变成
   **空 map**，而空 map 不是 error，于是**每条 selection 的延迟项与成本项同时
   静默塌成中性 0.5**。cohort 基线存在的唯一理由就此失效，且与「实测恰好中性」
   在输出上逐字相同。已加指标 + 告警让它变响，但**没有**从根上消除（见未决项）。
3. **诚实限定**：本地 auto 流量全部是 `probe_triggered` 探针流量，所以
   p95 偏移 15.2% 这个数字**不代表生产**。结构性的结论（换源必然换 cohort 总体）
   与本地数据无关；幅度是未知的。

### 做了什么

**抽出 SQL 纯函数**（`bg/auto_route_settle_sql.go`，新文件）
`settleBaselinesSQL(src)` / `settlePendingSQL(src)`。直接原因：`settings.RequestLogsWriteEnabled()`
只有读取器、**没有 setter**，所以只要源由全局门决定，集成测试就只能跑 v1 分支
——也就是今天线上已经在跑的那条。

**集成测试 2 道**（`bg/auto_route_settle_worker_integration_test.go`）
- `TestAutoRouteSettleSessionSourceMatchesV1OnIdenticalRows`：夹具原本**没有**
  `session_turns_hot` 表——这正是上一轮分支无法被测的原因。现在把同一批请求种进
  两族（只改会话身份列名），要求基线三元组相等、pending 逐字段相等、
  `computeSelectionReward` 输出相等。
- `TestAutoRouteSettleMissingCohortIsCounted`：证明新增护栏真的在计数。

**指标 2 个 + 告警 3 条**（`bg/auto_route_settle_baseline_metrics.go`、
`deploy/prometheus/rules/auto-route-settle-baseline.yml`）
`llmgw_autoroute_settle_baseline_cohort_rows{family}`、
`llmgw_autoroute_settle_baseline_neutral_total{term,family}`，
以及上一轮那个**没有消费者**的 `llmgw_autoroute_settle_source_total`
（`AutoRouteSettleSourceSwitched`，`changes(...[10m]) > 0`，**不带 `for:`**）。

### 门 6 道，变异 7/7

新增：`TestSettleSQLFilesStillCarrySQL`、`TestSettleWorkerDelegatesToTheSQLBuilders`、
`TestSettleBaselineCohortCountIsConsumed`、
`TestAutoRouteSettleBaselineRulesCoverEveryRegisteredMetric`、
`TestAutoRouteSettleBaselineAlertDocumentsTheRealMeasurement`；
改造 2 道既有门（见下）。

### 三个值得单独记住的教训

**① 搬动 SQL 顺手制造了一道假绿。**
把查询从 `auto_route_settle_worker.go` 搬进新文件后，两道只扫 worker 一个文件的门
行为分叉：`TestSettleLegsAllUseTheSameSource`（数 `src.TurnsTable` ≥ 3）**变红**，
而 `TestAutoRouteSettleWorkerDoesNotUseThe710View`（扫 SQL 字面量找 710 视图）
**仍然绿**——它从此对着一个不含 SQL 的文件断言「没有 710 视图」。
**只有红的那一道救了场。** 修法是引入 `settleSQLFiles` 清单供所有扫 SQL 的门共用，
再加一道门挡住「清单与实际位置脱节」。

**② 一条子串门被注释喂饱。**
`TestSettleBaselineCohortCountIsConsumed` 第一版先写
`strings.Contains(raw, "autoRouteSettleBaselineCohortRows")`。删掉那行 `Set` 之后
它**判为通过**——命中的是 `loadTaskBaselines` 文档注释里的
「see autoRouteSettleBaselineCohortRows」。已删掉该弱判据，只留要求完整调用形状
（标识符 + `WithLabelValues` + `Set(float64(cohortRows))`）的正则。

**③ 告警的 gauge 会陈旧，而陈旧的 gauge 会误报。**
`cohort_rows` 只在**活跃族**上 `Set`；切换后另一个族的序列停更并冻结在旧值。
对一个「已停更」的序列断言 `== 0`，读到的是「没在测」而不是「测出来是 0」。
所以每条 cohort 规则都带活动守卫
（`increase(source_total{family=...}[15m]) > 0`），**把「正在被使用」写进条件本身**。
变异验证：删掉守卫后门变红。

### 两个差点写错的结论（靠读定义躲掉）

- §9.40 记「`canonical_id` 会话族根本没有这列」。真库 `pg_attribute`：
  `session_turns_hot` **有** `canonical_id bigint`。那句话指的是 **710 视图的会话臂
  不投影它**。差点据此又写一节「分支一跑就崩」。
- 本地 `auto_route_selections_hot` 0 行。`pg_class.relkind='r'`、`relispartition=false`
  ⇒ 它是普通表，hot 内容由 `bg/partition_manager.go` 定期搬进分区父表。
  **hot 为空是正常的**，不是「worker 从来没结算过」。只看行数不看 relkind，
  这会是一条很难看的假发现。

### 测试

```
go build ./... && go vet ./bg/ ./autoroute/ && go vet -tags=integration ./bg/
go test ./bg/ ./autoroute/ ./deploy/prometheus/rules/ ./domains/streaming/executors/ -count=1
  → ok 25.965s / ok 4.020s / ok 0.414s / ok 33.917s
go test -tags=integration -timeout 15m -count=1 -run TestAutoRouteSettle ./bg/
  → ok 7.878s
```

### ⚠ admin 包 5 个红灯：已核实**不是**本轮引入

`TestRequestLogsControlPlaneKnownEntriesAreReal`、
`TestRequestLogsReadInventoryIsComplete`、
`TestRequestLogsStopWriteClassificationEvidenceIsReal`、
`TestRequestLogsStopWriteSourceFamilyCoversInventory`、
`TestNoUnregisteredVPaddedColumnReader`。

核实方式：在 `git worktree` 里检出 HEAD（`624a50c3b`，**不含本轮任何未提交改动**）
跑同一批测试，**5 个全部逐字复现**。HEAD 是 `075760768` 的后代，中间两个提交
`aa05e630b` / `624a50c3b` 属于并行会话，其中 `aa05e630b` 动了
`admin/v1_direct_padded_column_reader_test.go`。上一轮收尾时 admin 是 `ok 70.1s`。

**其中一条指向本轮的文件，必须点名**：
`不可归属豁免 "bg/auto_route_settle_worker.go:id" 已失效：该形状不再出现`。
根因是 §9.39 那道「v1 直读 + 读补位列」的扫描器靠**字面量表名**归属读方；
`src.TurnsTable` 化之后表名变成间接引用，**扫描器对这条读方失去了可见性**。
这不是「豁免该删」那么简单——它意味着这类读方现在**可能整体逃出检测**。
按既有纪律**没有代为修改他人登记表**（本审计记录过三次的反模式），
但这是并行会话需要知道的事实。

### 遗留风险

- **基线 cohort 跨切换不可能相等**，除非引入一个与被退役表无关的稳定 cohort
  （例如独立长期统计表）。**本轮没有实现，也认为不该由一道门或一次文档改写
  单方面「解决」**——这是需要单独排期的未决项。
- 本地会话族 `is_auto_request=TRUE` 近 24h 为 0 行，但**本地 auto 流量本身已塌**
  （09-27 起总量掉约 20 倍），**分不清是写方停写还是本地没有 auto 流量**。
  要判定需要一台仍在跑 auto 路由的实例，不做推测。
- 两道新集成测试**没有**接 CI（一次性空库上会退化，§9.34：绿而无证据）。
  正确运行方式：`go test -tags=integration ./bg/`。
- 告警的 `for:` 阈值仍未在真机 Prometheus 验证（承接 §9.38 遗留）。

### 下一轮提示词

> 本轮把 `auto_route_settle_worker` 的会话族分支**真正跑起来并证明了一致性**，
> 代价是搬动了 SQL——而搬动暴露了一个更值得处理的问题：
>
> ① **优先**：`TestNoUnregisteredVPaddedColumnReader` 报
>    `不可归属豁免 "bg/auto_route_settle_worker.go:id" 已失效`。根因不是「豁免该删」，
>    而是 §9.39 那道扫描器靠**字面量表名**归属读方，`src.TurnsTable` 化之后
>    **这类读方可能整体逃出检测**。需要判断：扫描器能不能在不接受「表名不可知」
>    的前提下仍然覆盖间接表名？（提示：§9.39 已经为「补位列名」做过一次
>    函数作用域归属的细化，方向是通的。）**注意 admin 登记表是并行会话的，
>    不要代改**——但这个判断该做。
> ② **未决项，需要拍板**：基线 cohort 跨切换不可能相等。选项是
>    (a) 接受一次**显式、有告警、有人确认**的 cohort 重新基线化；
>    (b) 引入与被退役表无关的稳定 cohort（独立长期统计表），让跨切换可比。
>    (b) 才是真正满足原始需求「确保数据在更改前后一致」的那条路，但它是新表 +
>    新写路径，不该顺手做。
> ③ 会话族 `is_auto_request` 为 0 的根因仍未判定。需要一台**仍在跑 auto 路由**的
>    实例对比两侧同日 auto 行数；本地已经不能作为证据面（流量塌了）。
> ④ 仍未做：§9.39 的 `vi` 维度 SQL 注释处理（`model_alternatives.go`）；
>    §9.38 告警 `for:` 阈值未在真机 Prometheus 验证；§9.36 的 19 条静默档；
>    响应侧 7 个读点仍不可端口。

---

## 第二十八轮（§9.45）：S4 读面门的结构性盲区 + 一个「两个真相源让门测不出差别」的实例

上一轮留下的 ① 得到了回答，而且答案比预期大：**扫描器在设计上就覆盖不到
「关系名不是字面量」的读点**，而那正是本项目为退役刻意造出来的写法。

### 结论先行

1. 删掉了我上一轮造成的 `bg/auto_route_settle_worker.go:id` 豁免（正确处置是删，
   不是改写），红灯解除。
2. **盲区是真的**：`paddedColumnsReadFromV1Direct` 靠 `fromJoinRE` 从 SQL 字面量里
   抽关系名，而 `FROM ` + logsTable + ` ` 这样的写法里**没有关系名**。全仓 60 处
   拼接点 / 36 个文件，其中 8 处解析到 v1 宽族。
3. **但当前没有活的漏网**：那 8 处逐个手验，**没有一处读补位列**（现网 6 列）。
   ⇒ §9.26 的「14 → 1 → 0 收口」方向正确，但它是站在一个**漏掉拼接式 SQL 的
   测量面**上得到的。补位读点为 0 是真的；这个 0 的支撑面比看上去窄。
4. 交付了可复现工具 `cmd/tools/sql_source_indirection_audit`（**不是门、不进 CI**）。

### 四个切换层：门的设计与退役规划方向冲突

| 切换层 | 定义 | `days <= 7` 时返回 |
|---|---|---|
| `maas.requestLogsSource` | `maas/usage.go:66` | `request_logs_hot AS r` |
| `admin.requestLogsFromClause` | `admin/usage_credits.go:91` | `request_logs_hot AS r` |
| `admin.logsSourceFromSQL` | `admin/logs_turns_source.go:62` | 视图或会话族（从不 v1） |
| `admin.boardRequestLogsFromClause` | `admin/board_time_range.go:136` | 视图 |

**规划把读法集中化以便整体端口，而门只能看见没被集中化的那些。** 这两件事方向相反。

### 工具第一版踩了三个「输出看起来很正常」的错

1. **把「不知道」报成「安全」**——`x := someFunc(...)` 没解析，未知标识符被当作
   候选表名 ⇒ 6 个真 v1 调用点一个都没报出来。
2. **变量按包级绑定**——`logsTable` 是函数局部变量，`admin` 包里三个函数各绑一次、
   返回三张不同的表；「首个胜出」让三者都拿到第一个的值 ⇒ 结论全错但格式正常。
3. **用前缀判 v1**——视图名同样以 `request_logs_` 开头 ⇒ 6 个只读视图的点被报成
   读 v1，方向相反的假阳性。

三个都是**同一个类别**：输出看起来对，没有视觉信号提示它错。

### 本轮最值得记住的一条：两个真相源让门测不出差别

`isV1Relation` 一度有三层判据（精确集合 + 视图正则 + 前缀兜底）。变异验证**连做
两次，两次都没能让门变红**：

| 变异 | 门的反应 |
|---|---|
| A：加回前缀兜底 | **仍绿**（正则已挡视图名） |
| B：删掉视图正则 | **仍绿**（精确集合里本就没有视图名） |

⇒ 行为被三个真相源同时决定，任何删掉一个的变异都测不出差别。**这与 §9.26 那条注释
「门拿错误源与错误派生式互相校验，所以它一直绿着」是同一件事，我刚在工具里重建
了它。** 收敛到唯一真相源（4 元素精确表名集合，与 `v1DirectTables` 同源）。

第三道门（`\s+$` 锚点）也是先绿后红：夹具只放了**孤立**的完整字面量，而它不在 `+`
链里，`ast.Inspect` 根本不访问它 ⇒ 锚点在不在都测不出差别。补上「完整字面量**自己
参与拼接**」后变异才生效。
⇒ **门测不出变异时，先怀疑夹具没覆盖区分点，再怀疑变异没生效。**

另：M5 第一次的 perl 替换**没命中**（标记数 0），门在未改动代码上通过——那不是证据，
已重做。

### 测试

```
go test ./cmd/tools/sql_source_indirection_audit/ -count=1   → ok 0.458s（5 道门）
go test ./admin/ -count=1 -run 'TestNoUnregisteredVPaddedColumnReader|TestNoVPaddedColumnReaderRemains'
  → PASS（0 个读方，与改动前一致）
go run ./cmd/tools/sql_source_indirection_audit -v1-only
  → 8 处 v1 / 5 个文件；合计 60 处拼接点 / 36 个文件
```

变异 5/5（M1 未知标识符、M2 包级绑定、M4 条件性判定、M5 锚点、M3 已收敛为行为门）。

### ⚠ admin / maas 当前有并行会话的未提交改动

`admin/usage_credits.go`、`admin/credential_monitor_heatmap.go`、
`admin/routing_resolve_filter.go`、`maas/` 等处于被改动状态。因此本节**只**改了
`admin/v1_direct_padded_column_reader_test.go` 里那一条豁免（我造成的红灯，
且该文件不在其当前编辑集内），**没有**实施任何结构性改动。

### 建议的下一步（属于门的所有者）

**不要**把那 8 处（或 60 处）登记进任何豁免表——它们的形状是**条件性**的
（`days <= 7` 才读 v1），登记表无法表达这种条件，且 §9.37 记录了这类表会腐烂。
更合适的形状是**在四个切换层上加门**：断言它们的返回值集合都在允许清单内。
切换层是端口的单点，守住它等于守住所有下游读点。

### 下一轮提示词

> §9.45 已把「S4 读面门看不见拼接式 SQL」这件事查清并量化（60 处拼接点，8 处解析到
> v1，当前无活的补位列读点），交付了可复现工具
> `cmd/tools/sql_source_indirection_audit`。
>
> ① **优先**：按 §9.45.6 的建议，在四个**切换层**上加门（断言返回值集合在允许清单内），
>    而不是逐个读点登记。理由：切换层是端口的单点，且读点的 v1 读取是**条件性**的
>    （`days <= 7`），登记表表达不了条件。**先确认 admin/maas 的并行会话改动已落定**，
>    否则不要动那些文件。
> ② 工具目前有 **40 处判为「不可判定」**（跨包函数 `db.SessionFamilyTurns*SQL` 与
>    struct 字段 `src.TurnsTable`）。它们多数是会话族（安全），但工具不猜。
>    若要收敛，可考虑让会话族切换层**导出表名常量**而不是导出整段 SQL——
>    这样工具就能解析，盲区会小一截。这是一个设计取舍，需要拍板。
> ③ 仍未做：§9.39 的 `vi` 维度 SQL 注释处理（`model_alternatives.go`）；
>    §9.38 告警 `for:` 阈值未在真机 Prometheus 验证；§9.36 的 19 条静默档；
>    §9.44 的基线 cohort 跨切换未决项（需要拍板 (a) 或 (b)）；
>    响应侧 7 个读点仍不可端口。

---

## 第二十九轮（§9.48）：灰度清单的那个数是过期的 —— 19 → **70**，其中 68 在生产代码

这一轮没有接着上一轮的①做，而是先核了一遍「灰度前到底要处理多少条」，因为那是
整个退役规划里唯一一个被文档称作「可执行的清单」的量化口径。

### 结论先行

1. **§9.36.3 的「19 条」是过期口径，实际 70 条**（差 3.7 倍）。那个 19 来自
   「31 条新增评估」的样本外推，文档没有标明它是外推值。
2. **68/70 在生产代码**（102 个生产读点里 67%）。「受影响的大多是离线工具」不成立。
3. `unclassified` = **0 / 106** ⇒ 评估完整，70 就是全部工作量。
4. 补 3 道门让这个数不能再悄悄过期，变异 3/3。

### 分布（`requestLogsStopWriteClassification`，106 个读点）

| 档位 | 条数 |
|---|---|
| `silently_empty` | 27 |
| `silently_degraded_content` | 22 |
| `silently_frozen` | 21 |
| **静默小计** | **70** |
| `unaffected_by_stop_write` | 24 |
| `errors_out` | 10 |
| `validator_dual_read` | 2 |
| `unclassified` | 0 |

按面切：生产代码 102 / 静默 68；`cmd/tools/` 2 / 1；`tests/` 2 / 1。

### 门 3 道（`admin/audit_silent_count_consistency_test.go`，新增）

| 门 | 变异 | 结果 |
|---|---|---|
| `TestAuditDocSilentClaimMatchesRegistry` | 文档 70→69 | 红（报「差 +1」） |
| `TestAuditDocSilentClaimIsMarkedAsMachineChecked` | 删掉一个档位名 | 红 |
| `TestStopWriteEffectValuesAreFromTheDeclaredSet` | 引入未声明档位 | 红 |

方向上刻意**不**逐个读点登记（那是 §9.37 记录过的腐烂形态），也**不用裸数字匹配**
（文档里有几十个数字）。门要求一句固定格式的话，格式本身就是契约。

### 本轮最值得记住的一条：一个注释里声称的保护，没有门兑现它

`countSilentStopWriteEffects` 的注释写着「逐个列举三个静默档，不要用取补集的写法」，
理由是补集会在新增第五种档位时把它悄悄算进静默数。

**但这个选择在今天的登记表上测不出来**：补集恰好也等于 70（106 − 10 − 24 − 2）。
⇒ 光靠计数门无法兑现那句话，所以补了第三道门让「新增档位」本身会红。
⇒ **注释里声称的保护，如果没有门兑现它，那条注释就是装饰。**

另：M3 第一次注入未声明档位时是**编译红**（`undefined: effectSomethingNew`），
不算证据；补了常量声明重做，才是断言红。

### 两条「按我自己选的规则分类」都放弃了

1. 按 Note 里的机制关键词聚类（`bodies` / `无时间下界` / `710` …）——聚类结果由
   我挑的词表决定，不是结构事实。探针写完没跑。
2. 按路径规则切「离线 vs 生产」——这条**采用了**，但规则只用 `cmd/tools/` 与
   `_test.go` 两个客观前缀，且把规则写进表格，读者可以不同意某个归类。

⇒ 报「分成 N 类」之前先问：**分类轴是谁定的。**

### `silently_frozen` 的 21 条：最该先处理，但**不做**

frozen 与另两档性质不同：它**永远不红**——查询成功、有行、有形状，内容永久停在
停写前一刻。结构上它们同属「没有时间下界的读点」。

理论上可以用一个共享原语一次覆盖 21 条（§9.35 已为 settle worker 造过一个实例）。
**本节明确不做**，理由不是工作量：

- 只造原语不接线，它就是**一个新的装饰面**（§9.37 的核心失效模式）。
  造一个没人用的 `v1DataHorizon` 比不造更坏。
- 要有价值必须逐读点接线，而那些文件正被并行会话改动。

⇒ 顺序是：**先有人确认这 21 条的处理口径（接线 / 降级提示 / 接受冻结并标注），
再造原语。** 反过来会产出一个漂亮的、没人用的 helper。

### 测试

```
go test ./admin/ -count=1 -run 'TestAuditDocSilentClaim|TestStopWriteEffectValues' -v
  → 3 道全 PASS
```
变异 3/3 全部还原（`git diff` 无输出）。

### 下一轮提示词

> §9.48 把灰度清单的数从 19 订正为 **70**（68 在生产代码，0 unclassified），
> 并加了 3 道门让这个数不能再悄悄过期。**但 70 条的成因还没有分析过**——
> §9.48.4 说明了为什么按关键词聚类不可用。
>
> ① **最该先定的**：`silently_frozen` 的 **21 条**是唯一「永远不会红」的一档。
>    本节给出了建议但没做：需要**先有人拍板这 21 条的处理口径**
>    （逐读点接线 / 加降级提示 / 接受冻结并在 UI 标注），再考虑造共享的
>    v1-horizon 原语。**顺序反了会产出一个没人用的 helper**（§9.37）。
>    拍板之后再动手。
> ② 若要继续推进 70 条整体，可行的做法是**逐条读 SQL 与消费方**确定成因，而不是
>    按关键词聚类（词表是我定的，结论就是我的偏好）。这需要决定「成因」的粒度：
>    是按「缺时间下界」「读 bodies」「Scan 吞错」这类**机制**，还是按**能一次性修掉的
>    共性改动**。后者更可执行，但需要逐条判断——建议先挑 5 条做样板。
> ③ **仍未做且需要拍板**：§9.44 基线 cohort 跨切换（(a) 显式重新基线化 /
>    (b) 引入独立稳定 cohort）。这是原始需求「确保数据在更改前后一致」的唯一
>    未闭环项。
> ④ 其余遗留未变：§9.45 建议在四个切换层上加门（需先确认 admin/maas 并行改动落定）；
>    工具的 40 处「不可判定」；§9.38 告警 `for:` 阈值未在真机 Prometheus 验证；
>    响应侧 7 个读点仍不可端口。

---

## 第三十轮（§9.49）：我前三轮对 admin 红灯的归属判断是错的 —— 4 个是我引入的

这一轮开头我准备做「70 条静默档的 5 条样板」。抽样时第一条候选就暴露了一件事，
顺着查下去发现了一个**关于我自己判定方法**的错误，比样板重要得多，所以本轮主线
换成了纠错。

### 先说结论

**`admin` 的 5 个红灯里，4 个是 §9.43 引入的。我连续三轮写成「不是本轮引入」。**

| 提交 | 5 个测试的状态 |
|---|---|
| `e154135fa`（§9.43 的父提交） | **全部通过** |
| `075760768`（我的 §9.43） | **4 个失败** |
| `b28ad0c98` 之后 | 第 5 个出现（migration 816，**并行会话**） |

本轮已把 4 个全修掉 ⇒ `admin` 现在只剩 1 个红灯，且**是他们的**。

### 我的判定方法错在哪

§9.44 那轮我做过 worktree 对照，基线取当时的 `HEAD` = `624a50c3b`，测试通过，
于是写下「不是本轮引入」。

**但 `624a50c3b` 是我 §9.43 提交 `075760768` 的后代**（`merge-base
--is-ancestor` = YES）。那次对照是「含我改动的仓库 vs 含我改动的仓库」，
当然一致。

同一份 worktree 上 `grep -c "LEFT JOIN request_logs_hot rl" bg/auto_route_settle_worker.go`
= **0**——那条证据当时就在手边，我没看。

⇒ **对照基线必须早于被怀疑的那次改动。**「当前 HEAD 不是我 ⇒ 不是我」只在 HEAD
落在我改动**之后**时成立；HEAD 被并行会话一推进，它就变成我的后代，推理方向整个
反过来。**基线要显式指定一个已知的、在我改动之前的提交，不能用 HEAD。**

### 根因：三个独立器眼，同一个未声明的前提

§9.43 把 `LEFT JOIN request_logs_hot rl` 改成 `" + src.TurnsTable + " rl`。
三个各自独立实现的器眼同时失明：

| 器眼 | 实现 | 前提 |
|---|---|---|
| `requestLogsReadInventory` | 行正则 `from\s+request_logs` | 关系名紧跟 `from` |
| `paddedColumnsReadFromV1Direct` | 从 SQL 字面量抽关系名 | 关系名是字面量 |
| `sourceFamilyOf` | 剥注释后匹配族正则 | 同上 |

**这不是三个 bug，是一个契约从未被声明。** §9.45 在补位列那道门上撞见过同一件事，
记成「结构性盲区」就收工了——**没有回头查仓里还有几个器眼共享这个前提**。
而规划的方向恰恰是增加间接性 ⇒ 器眼与规划方向相反。

### 为什么不用「加条注释让它看见」

行正则扫的是**原始行文本**，加一行注释 `// reads FROM request_logs_hot` 清单立刻
变绿、计数也对。**那是伪造测量**——门绿了，测的不是代码里真实存在的东西。
§9.45 记过同族事故。⇒ 让器眼**知道**有间接读点。

### 落地

`admin/request_logs_indirect_readers_test.go`（新）：每条必填 `Family`（器眼看不见，
只能人判）/ `ResolvesTo`（写上表名才能被 `v1DirectTables` 核对，不是断言）/
`Reason`（空理由等于没登记）。失效自检两条：文件必须**仍然**扫不到直接字面量、
不得同时出现在两张表里。三个消费方改为遍历「直接表 ∪ 间接表」。

顺带修出一个被低估的偏差：**评估进度此前报「105/105 已评估」**，那个 105 根本没算
上间接读点 ⇒ 修后 **106/106**。一个「全部完成」的数字里藏着漏项。

顺带订正两张表：停写分级表（改键到 sql.go、Effect `silently_empty` →
`silently_degraded_content`）与控制面表（新增 `EvidenceIn` 字段，表达
「消费方在 A 文件、证明读 v1 的 SQL 在 B 文件」）。

### 三条要记住的

1. **这张新表发现不了新的间接读点**（机器判不出「一个没有 v1 字面量的文件是不是
   通过 Go 表达式在读 v1」）。它覆盖已知的 + 失效自检，**不给 CI 用**。
2. **`silently_degraded_content` 被装了它原本没有的东西**：该档语义是「某一**列**
   变空」，而这里是「reward 的**分项**退化」。该文件自己的约定是「应扩档而不是塞
   回去」。本轮**没有**扩档（会改动 106 条登记的口径与灰度清单算法，需单独裁决）。
3. N1 第一次注入是**编译红**（结构体字面量混写），不算证据；整段替换重做才红。

### 测试

```
go test ./admin/ -count=1   → 仅 1 个 FAIL：TestSessionArmNullPaddedColumnsMatchMigration（并行会话的）
8 道相关测试全过；变异 3/3 全部还原
```

### 下一轮提示词

> §9.49 修掉了前三轮误判的 4 个 admin 红灯（全部由我的 §9.43 引入），并新增
> `admin/request_logs_indirect_readers_test.go` 让三个扫描器认识「间接读 v1」。
> **但那张表发现不了新的间接读点**，这是一个已知的、被写进文件头注释的缺口。
>
> ① **方法论教训已写进审计 §9.49.2，请务必沿用**：做「不是我造成的」对照时，
>    基线必须显式指定一个**早于被怀疑改动**的提交，**不能用 HEAD**——HEAD 可能
>    已经被自己的改动污染或被并行会话推进。本审计连续三轮栽在这一点上。
> ② **可做**：把 `cmd/tools/sql_source_indirection_audit` 的 40 处 `unresolved`
>    收敛。多数是跨包函数 `db.SessionFamilyTurns*SQL` 与 struct 字段
>    `src.TurnsTable`。让会话族切换层**导出表名常量**而不是整段 SQL，工具就能解析，
>    盲区会小一截。**这是设计取舍，需要拍板**（会动 `db/` 的公开 API）。
> ③ **仍未做且需要拍板**（两项，都是原始需求的收口）：
>    - §9.44 基线 cohort 跨切换：(a) 显式重新基线化 / (b) 引入独立稳定 cohort。
>      这是「确保数据在更改前后一致」的唯一未闭环项。
>    - §9.49.8 的 `silently_degraded_content` 档是否扩档。
> ④ §9.48 留下的：`silently_frozen` 的 **21 条**（唯一永不红的一档）需要先定处理
>    口径再动手；70 条静默档的成因尚未逐条分析（建议先挑 5 条做样板）。
> ⑤ 其余遗留未变：§9.38 告警 `for:` 阈值未在真机 Prometheus 验证；
>    §9.39 的 `vi` 维度 SQL 注释；响应侧 7 个读点仍不可端口。

---

## 第三十一轮（§9.50）：`IntegrityFingerprintDrift` 会在 S4 停写后永久关闭

### 结论

发现一个**安全相关检测器**在停写后**永久关闭**，且此前**完全不可见**。加了三个
Prometheus 指标 + 两条告警 + 7 道门。

更重要的是：**本轮自己写的第一版告警文案里有一个错误断言，已订正并加了门防止复发。**

### 关键事实（已逐行核实）

`bg/integrity_fingerprint_drift.go` 的短路有两条腿，**都断在同一个开关上**：

| 腿 | 机制 | 停写后 |
|---|---|---|
| 1 探针 | `probeFingerprintTraffic` 读 `request_logs_hot` / `request_logs` 的 `system_fingerprint IS NOT NULL` | v1 无新行 ⇒ 恒空 ⇒ `fingerprintScanSkip` |
| 2 进程内 arm | `fingerprintScanDecision` 的 `inProcSeen` 分支 ← `telemetry.SystemFingerprintObservedSince()` | 唯一写入方 `markSystemFingerprintObserved()` 只在 `persistSystemFingerprint()` 内调用，而后者的两个调用点（client.go:1816 / :2468）**都在 `if logsWrite {}` 块内**（`:1336`–`:1839` / `:2126`–`:2471`）；`logsWrite = requestLogsWriteEnabled() = settings.RequestLogsWriteEnabled() = "storage.request_logs_write_enabled"` |

⇒ **停写 + 任意一次重启 = 检测器永久关闭**。凭据被悄悄换掉不会被现有信号发现。

### 我这轮犯的错（必须记）

1. **文案层面的错误断言**：第一版 yml 写「逃生口这条链**不读 v1**，所以恢复与否取决于
   当前进程有没有处理过带指纹的请求」——听起来会自愈。**事实相反**。已改写为
   「逃生口也是关着的（不要指望它自愈）」。
2. **中间还错了一次**：我以为「UPDATE 匹配 0 行也返回 nil，所以仍会 arm」。**也错**——
   那个调用点在 `if logsWrite` 块**内**。判断门控范围不能靠读调用点附近的代码，
   要数括号。
3. **门自己抓到 3 个真 bug**（先红后修）：一级 selector 认不出两级 selector；
   `hasCall` 下探嵌套块导致外层 `switch` 认领内层调用；行号无法区分相邻两行。
4. **M5 逃过了门**（唯一一次）：hop-2 只数「引用次数 == 1」，wrapper 被改成
   `if <key> {return true}; return true` 后引用仍是一次、值已偷换 ⇒ 绿。
   改成断言**函数体形状**（单语句纯转发）后抓住。
   ⇒ 与 §9.44 同族：**「数量对」不等于「值对」**。

### 门只能断言能被证明的那一侧

逃生口门断言的是 **AST 词法包含 + `logsWrite` 来源三跳**；运行期由同一变量控制。
**无法证明的**（探针运行期取值）由告警覆盖，不在门里假装。门也**不替你决定该不该改**：
若有人把 `persistSystemFingerprint` 挪出门控（§9.50.4 给的正确修法之一），门会红
并要求同步改文案——**那个摩擦是故意的**。

### 正确修法（本轮未实施，需先实测）

1. 探针改读会话族（`session_turns` 的 `system_fingerprint` 覆盖**必须先在真库实测**）。
2. 把 `inProcSeen` 的 arm 移到 `if logsWrite` 之外（与写解耦：arm 是进程内观测，
   不是宽表写入的副产品）。

### 测试

```
go build ./... && go vet ./bg/ ./domains/hooks/observability/telemetry/ ./deploy/prometheus/rules/   → OK
go test ./bg/ ./domains/hooks/observability/telemetry/ ./deploy/prometheus/rules/ -count=1            → 全过
变异 8/8（M1 M2 M3 M3b M4 M5 M6 M7 M8），每次都确认是**断言命中**而非编译红，且全部还原
```

### 下一轮提示词

> §9.50 修了一个**安全检测器在停写后永久关闭**的问题（指纹漂移），并订正了自己
> 第一版告警文案里的错误断言（「逃生口不读 v1 所以会自愈」是错的）。新门用 AST 断言
> 逃生口在 `if logsWrite {}` 内，三跳验证 `logsWrite` 就是 S4 键。
>
> ① **务必沿用的方法论**：门里写「数量对」的判据（引用次数、调用次数）**不等于**
>    「值对」。M5 就是在这一条上逃过去的。任何「计数 == N ⇒ 正确」的判据都要问一句
>    「有没有一种错法能让计数不变」。
> ② **可做**（§9.50 留下的）：实施 §9.50 的两条修法——探针改读会话族 +
>    `inProcSeen` 的 arm 移出门控。**前置是实测** `session_turns` 上
>    `system_fingerprint` 的覆盖率（若会话族根本不写这一列，改读会话族是空转）。
> ③ **仍未做且需要拍板**（三项，都是原始需求的收口）：
>    - §9.44 基线 cohort 跨切换：(a) 显式重新基线化 / (b) 引入独立稳定 cohort。
>      这是「确保数据在更改前后一致」的唯一未闭环项。
>    - §9.49.8 的 `silently_degraded_content` 档是否扩档。
>    - §9.48 的 `silently_frozen` 21 条（唯一永不红的一档）的处理口径。
> ④ §9.44–§9.50 这一族改动都是「让不可见的东西变可见」，**没有一条改变了读源的
>    语义**。真正切源的收口仍然待 ③ 第一项。
> ⑤ 其余遗留未变：§9.38 告警 `for:` 阈值未在真机 Prometheus 验证；§9.39 的 `vi`
>    维度 SQL 注释；响应侧 7 个读点仍不可端口。

---

## 第三十二轮（§9.51）：真库实测推翻了 §9.50 的**因果叙述**

### 结论

去做了 §9.50 留下的那个前置实测（会话族 `system_fingerprint` 覆盖），结果
**证伪了我自己的修法建议**，并证明 §9.50 的告警诊断是错的。同一件事我连续订正了两次。

### 实测（本地真库，2026-10-02）

| 面 | 行数 | 非空指纹 |
|---|---|---|
| `request_logs`（v1 全表） | 2,164,650 | **0** |
| `session_turns`（会话族全表） | 1,683,739 | **0** |
| `model_integrity_events.context` JSONB | 7,081（2026-09-05 起） | **0** |

第三行最关键：integrity 事件走 context JSONB（`detector.go:151`），与专用列
**同源但不共表** ⇒ 排除了「列存在但写方没接」的解释。
近 14 天按天分形态也查了：只有今天有行，且今天 v1 3189 行里非空 0 行。

### 订正

**错**：§9.50 写「S4 停写后 v1 无新行 ⇒ 探针恒空」。
**对**：`system_fingerprint` 只来自上游 `X-System-Fingerprint` 响应头，
**上游从不发**（`executor_chat.go:1926` / `handler.go:6730`）⇒ 探针在停写**之前**
就已经是空的 ⇒ 检测器自 2026-09-25（D11 上线）起一直关着，**与 S4 无关**。

告警**存在**是对的（检测器确实没工作、确实零信号），**诊断**是错的。

**S4 的真实影响是前瞻性的**（§9.50 这部分说对了）：腿 2（`inProcSeen` 的进程内
arm）被同一个 `if logsWrite {}` 挡住 ⇒ **即便上游将来开始发指纹，停写 + 重启后
也永远不会恢复**。这才是那条告警该守的东西。

### 修法排序被推翻了一项

| 修法 | 结论 |
|---|---|
| 探针改读会话族 | **证伪**——会话族同样恒空。改过去只是把「因为缺数据而空」伪装成「已修好」 |
| `inProcSeen` 的 arm 移出门控 | **仍是唯一的代码修复，且不依赖上游** |
| 长期问题（新增） | 这个检测器在**任何**没有指纹数据时都无对象可检。要么让上游发指纹（产品决策），要么承认这项检测能力当前是空的并如实记录 |

### 门：+1 道，变异 3/3

`TestIntegrityFingerprintDriftAlertNamesTheRealCause` 判三件事：点名
`X-System-Fingerprint`、写明「停写之前探针就已经是空的」、**附可复现的查询**；
并禁用被撤回的因果措辞。M9/M10/M11 全红。

**这道门自己产生过一次假阳性**，值得记：撤回说明里**逐字引用**了被撤回的句子，
被自己的禁用词检查命中。处理方式是**转述**而非引用，而不是放宽判据——
放宽等于这道门没有。

### 教训

1. **写「X 停了 ⇒ 因为 Y」之前，先确认 Y 在 Y 之前就已经成立。** 我把「探针读 v1」
   当成「探针为空的**原因**」，它其实只是**载体**。载体被退役 ≠ 读数归零。
2. **「先实测」这条我自己写进 handoff 的建议救了这一轮。** 跳过它直接改读会话族，
   会引入一个**看起来已修好、实际更糟**的改动。
3. 同一个错误因果复制在**三个载体**（告警 YAML、metrics 文件头注释、审计 §9.50.1）。
   只改一处等于没改。

### 测试

```
go build ./... && go vet ./bg/ ./deploy/prometheus/rules/                        → OK
go test ./deploy/prometheus/rules/ -count=1                                        → 全过
变异 M9 M10 M11 全红，全部还原
```

### 下一轮提示词

> §9.51 用真库实测推翻了 §9.50 的因果：探针恒空是因为**上游从不返回
> `X-System-Fingerprint`**（v1 216 万行 / 会话族 168 万行 / integrity JSONB 7081 条，
> 非空均为 0），**不是** S4 停写。检测器自 2026-09-25 起就一直关着。
> 「探针改读会话族」这个修法已被**证伪**——会话族同样恒空。
>
> ① **方法论，请务必沿用**：写「X 停了 ⇒ 因为 Y」之前，先确认 Y 在 Y 之前就已经
>    成立。把「读的是哪张表」当成「为什么读数为零的**原因**」是最容易犯的一类错——
>    载体被退役 ≠ 读数归零。**这已经连续两轮由真库实测纠正了我的静态推理。**
> ② **可做且不依赖任何外部决策**：把 `inProcSeen` 的 arm 移出 `if logsWrite`。
>    这是 §9.50/§9.51 唯一成立的代码修复。做完要同步改
>    `deploy/prometheus/rules/integrity-fingerprint-drift.yml` 的
>    「逃生口也是关着的」段落（门会红，会强制你改——那是故意的摩擦），
>    以及 `bg/integrity_fingerprint_drift_metrics.go` 文件头注释与
>    `domains/hooks/observability/telemetry/fingerprint_escape_hatch_gate_test.go` 的
>    断言方向。
> ③ **需要产品决策，不由我做**：这个检测器在**任何**没有指纹数据时都无对象可检。
>    选项是「让上游开始发指纹」或「承认这项检测能力当前是空的并如实记录」。
>    在这之前，**不要**靠调大间隔把告警压下去。
> ④ **仍未做且需要拍板**（三项，都是原始需求的收口）：
>    - §9.44 基线 cohort 跨切换：(a) 显式重新基线化 / (b) 引入独立稳定 cohort。
>      这是「确保数据在更改前后一致」的唯一未闭环项。
>    - §9.49.8 的 `silently_degraded_content` 档是否扩档。
>    - §9.48 的 `silently_frozen` 21 条（唯一永不红的一档）的处理口径。
> ⑤ §9.44–§9.51 这一族改动**没有一条改变了读源的语义**，全部是「让不可见的东西变可见」
>    或「纠正错误的因果叙述」。真正切源的收口仍然待 ④ 第一项。
> ⑥ 其余遗留未变：§9.38 告警 `for:` 阈值未在真机 Prometheus 验证；响应侧 7 个读点
>    仍不可端口。

---

## 第三十三轮（§9.52）：实施修复 —— 把进程内 arm 移出 S4 停写门

### 结论

§9.51 证伪了「探针改读会话族」，剩下唯一成立的代码修复本轮做了。**门被反转过一次**，
反转过程、以及门上发生的**四次装饰断言**，是本轮最有价值的部分。

### 改了什么

`persistSystemFingerprint` 不再 arm；新增 `observeSystemFingerprint`，
在 `insertRequestLog` / `updateRequestLog` 的 `if logsWrite {` **之前**调用。

落点选择的两个理由（都不是「最优雅」）：
1. `executor_chat.go` 正被并行会话改动 190 行 —— 共享工作区里不碰；
2. 这两个函数写 `usage_ledger_hot` 的部分**本来就在门控之外**，所以会**活过 v1 退役**，
   arm 放在这里不会跟着宽表一起消失。

**诚实记账**：真正的观测点是上游响应头被读出的地方。停在
`domains/hooks/observability/telemetry` 是一个**正确的停靠点，不是最纯的**。

### 门：反转

第一版门断言「arm 在门内」——**那正是缺陷本身**，修好后立刻变红。
换成不变式：*arm 调用链上没有任何一环在 `if logsWrite {}` 内*。
文案门同样反转（`...DoesNotPromiseSelfHealing` → `...TracksTheArmFix`）。

**这个摩擦是故意留的**：文案与代码不一致时，最省事是两边都不改。

### ★四次装饰断言（本轮最有价值）

| # | 断言 | 为什么装饰 |
|---|---|---|
| 1 | 环 3 =「persist 的调用点不得自我递归」 | 量的是另一件事，几乎恒真 |
| 2 | 环 3 排在环 1/环 2 之后 | 撤销修复会先触发更严格的 ⇒ 永远轮不到 |
| 3 | 用 `isCallTo(Body, …)` | 浅匹配遇 BlockStmt 停，Body 自己就是 ⇒ 恒假 |
| 4 | 改成逐语句扫 | 调用在 `if err == nil {…}` 内，仍被跳过 ⇒ 仍恒假 |

**第 4 次才真修好**：另写深匹配 `blockCallsDeep`。
*「调用归属哪个基本块」*（浅）与 *「函数是否调用了 X」*（深）是两个问题，
**共用一个匹配器就是错的**。

⇒ 这是「浅匹配用错地方」在**同一轮内第三次**（前两次在 bg 侧那道接线门）。
**判据是否正确，和它量的是不是同一件事，是两个独立的问题。**

### 变异 M12–M14

M12 / M12b / M13 / M13b 全红，但**都不能证明环 3 有效**——前三条无论环 3 写多糟都会红。
专门构造的 **M14**（只在 persist 里 arm，删掉 update 那处 ⇒ 调用点仍是 2 个）才红在环 3。
**这是环 3 可达性的唯一证据。**

### 同一个陷阱在同一轮踩了两次

§9.51 记过「撤回说明里逐字引用被撤回的句子 → 触发自己的禁用词检查」，
§9.52 改文案时又踩了一次。仍是**转述**而非引用。
⇒ 这条纪律的真正内容：**「禁用某字面串」的判据天然无法区分「非法的断言」与
「合法的订正记录」**，所以文案里不该逐字复述被撤回的句子。

### 测试

```
go build ./... && go vet ./bg/ ./deploy/prometheus/rules/ ./domains/hooks/observability/telemetry/  → OK
go test ./bg/ ./deploy/prometheus/rules/ ./domains/hooks/observability/telemetry/ -count=1             → 全过
变异 M12 M12b M13 M13b M14 全红，全部还原
```

### 下一轮提示词

> §9.52 做了 §9.51 剩下的唯一代码修复：`inProcSeen` 的 arm 已移出 S4 停写门
> （`observeSystemFingerprint`，在 `if logsWrite {}` 之前调用）。门被反转过一次，
> 现在钉的是**不变式**而不是具体函数名。
>
> ① **本轮最该带走的一条**：一道断言要成立，需要**两个**独立的问题同时答对——
>    「判据本身对不对」和「它量的是不是我想量的那件事」。本轮同一道门上发生了
>    **四次装饰断言**，都是第二类问题。**造完门要专门造一个「只有这条断言该红、
>    其他断言都不该红」的变异**；造不出来就说明这条断言不可达。
> ② **可做**（已被 §9.52 明确指出不是最优）：把 arm 从
>    `domains/hooks/observability/telemetry` 移到 `domains/streaming` 的响应头
>    读取点（`handler.go:6730`）。需要跨包导出，且 `executor_chat.go` 正被并行会话
>    大改（190 行）——**先确认冲突窗口结束再做**。门不认具体落点，只认「不在门内」。
> ③ **仍未做且需要拍板**（三项，都是原始需求的收口）：
>    - §9.44 基线 cohort 跨切换：(a) 显式重新基线化 / (b) 引入独立稳定 cohort。
>      这是「确保数据在更改前后一致」的唯一未闭环项。
>    - §9.49.8 的 `silently_degraded_content` 档是否扩档。
>    - §9.48 的 `silently_frozen` 21 条（唯一永不红的一档）的处理口径。
> ④ **需要产品决策**：指纹漂移检测器在没有任何指纹数据时无对象可检（上游从不发
>    `X-System-Fingerprint`，真库 216 万 + 168 万行非空均为 0）。要么让上游发，
>    要么承认这项检测能力当前是空的。**不要靠调大间隔把告警压下去。**
> ⑤ §9.44–§9.52 没有一条改变读源语义；真正切源的收口仍待 ③ 第一项。
> ⑥ 其余遗留未变：§9.38 告警 `for:` 阈值未在真机 Prometheus 验证；响应侧 7 个读点
>    仍不可端口。

---

## 第三十四轮（§9.53）：为 §9.44 cohort 决策取证 —— 量具在本地不成立

### 结论

去量「会话族能不能建出与 v1 可比的 cohort」。量完的结论是：**本地开发库答不了这个
问题**，证据必须上 252。**没有拍板 (a)/(b)**——本地证据既不支持 (b)，也不足以支持 (a)。

### 量到了什么（本地 24h）

- v1 `is_auto_request=TRUE` 1,932 行；会话族 **0** 行。
- 会话族 1,299 行 `is_auto_request` **全 false**；`task_type` **全空**。
  （列 3/3 都存在，不是缺列。）
- 跨族按 `request_id`（hot ∪ parent，不带 ts）：v1 auto 行 1,938 条，
  在会话族里 **0** 条；v1 全部 3,266 条里有 1,301 条在会话族。
- **根因**：那批 auto 行的 `origin_actor` 是 `node-probe-worker`(1930) /
  `active-probe-worker`(2) / `auto-title-generator`(14)，而前两者
  **`gw_session_id` 全为 NULL**。会话族那 1,301 行也全是系统流量
  （probe-service 691 / null 565 / node-probe-worker 36 / selfcheck 14）。

⇒ **本地库的「auto 流量」是探针合成流量，不带会话键，按设计不会进 session_turns。
这里根本没有业务 auto-route 流量可供建 cohort。**「cohort 为 0」是量具失效信号，
不是缺陷信号。

### ★我中途得出的两个错误结论（都被这次测量推翻）

1. **「会话镜像丢了整个路由组」** —— 错。映射在
   `internal/sessionv2mirror/s1a_fields.go:64-99`（`applyStorageS1AFields`），
   9 个字段一个不缺。错因：**把行区间限定在错误的文件上**（只 grep 了
   `hook.go` 的一段），「查不到」被读成了「没接线」。
2. **「业务 auto 请求从未被镜像，是停写前的阻塞项」** —— 错。那 1,930 条探针行
   没有会话键，本来就不可能出现在 `session_turns`。**重叠为 0 是设计。**

⇒ 两次都是**「没查到」被当成「不存在」**。第二次尤其危险：照它下结论就会把一个
**不存在的阻塞项**写进停写前置条件清单。

### 测试

本轮**无代码改动**（取证轮），故无测试。

### 下一轮提示词

> §9.53 为 §9.44 的 cohort 决策取证，结论是**本地开发库答不了这个问题**：
> 本地的 auto 流量全是 `node-probe-worker` 探针（1930/1938），它们
> `gw_session_id` 全为 NULL，按设计不进 `session_turns`；会话族里也只有系统流量。
> **「cohort 为 0」是量具失效信号，不是缺陷信号。** 因此 (a)/(b) **未拍板**。
>
> ① **必须在 252 上跑这四条**（本地 24h 无代表性，且形态是探针）：
>    1. `SELECT is_auto_request, count(*) FROM request_logs GROUP BY 1`（近 7 天）
>    2. 同谓词在 `session_turns` 上的计数 —— **(a)/(b) 的分水岭**：
>       会话族 auto 行充足 ⇒ (a) 显式重新基线化即可；为 0 ⇒ (b) 也无从建起，
>       必须先解决「业务 auto 流量是否被镜像」。
>    3. 跨族按 `request_id`（hot ∪ parent，**不带 ts**）测 auto 行重叠率
>    4. `origin_actor` 分布：生产上是否存在非探针 auto actor，
>       以及它们是否落在 `SQLExcludeSyntheticActors` 排除名单里
> ② **本轮最该带走的一条**：「grep 不到」**不等于**「没接线」。我两次把行区间
>    限定在错误的文件/区间上，得出了两个假结论（路由组其实在 `s1a_fields.go`；
>    auto 行其实因无会话键而本就不该被镜像）。**查「某字段有没有接线」时，
>    必须先确认搜索范围覆盖了它的写方，而不是先假定它在某个文件里。**
>    库能裁决的，用库裁决——两次都是库推翻了我的静态阅读。
> ③ **仍未做且需要拍板**（三项）：§9.44 cohort（等 ① 的数据）、§9.49.8
>    `silently_degraded_content` 是否扩档、§9.48 `silently_frozen` 21 条口径。
> ④ **可做**：把 arm 从 `domains/hooks/observability/telemetry` 移到
>    `domains/streaming` 的响应头读取点（§9.52 已说明这是「正确停靠点，非最优」）。
>    需跨包导出；`executor_chat.go` 曾被并行会话大改，**先确认冲突窗口结束**。
> ⑤ §9.44–§9.53 没有一条改变读源语义；真正切源的收口仍待 ③ 第一项。

---

## 第三十五轮（§9.54）：252 生产库实测 —— **(a)/(b) 两个选项都被推翻**

用户授权后经 `env-injector inject aliyun-edge-252` + SSH 只读查询 `pg-252-pg17`
（全程只读 SELECT）。

### 结论

**§9.44 提的问题本身是错的。** 不是「cohort 换源族后不可比」，而是
**cohort 在 v1 里就算错了总体**。

### ★线上正在发生的缺陷（与切换无关）

cohort 与已结算 selection 的 task_type 几乎不相交（**无 join，纯分组对比**）：

| task_type | cohort 行数 | 30 天已结算 selection | |
|---|---:|---:|---|
| chat | **3** | 12,864 | p95 over 3 行不是分位数 |
| creative | **0** | 4,985 | **NO_BASELINE** |
| code | **10** | 3,501 | p95 over 10 行不是分位数 |
| reasoning | **0** | 1,269 | **NO_BASELINE** |
| planning | **0** | 4 | **NO_BASELINE** |
| probe_triggered | **19,252** | **0** | cohort 的 99.95% 服务 0 条 |

- **6,258 条已结算（29.3%）完全无基线** → latency/cost 走中性 0.5
  （`auto_route_settle_worker.go:615/618`）。
- **chat + code 共 16,365 条（76.6%）对着 n=3 / n=10 的「分位数」打分。**
- `probe_triggered` 几乎撑满 cohort，却服务 0 条 selection，还把 §9.44 新加的
  `llmgw_autoroute_settle_baseline_cohort_rows` 撑成一个**看起来健康**的数字。

### 根因

cohort 谓词 = `request_logs.is_auto_request IS TRUE` + `SQLExcludeSyntheticActors`；
被结算总体 = `auto_route_selections` 里 `task_type` 非空的行。**两者词表几乎不重叠。**
且 `SQLExcludeSyntheticActors`（`autoroute/shadow_actors.go:63-64`）**没有排除
`node-probe-worker` / `probe-service`**，而 `middleware/origin_mw.go:404` 明确把
`node-probe-worker` 映射到 `origin_stage = node_probe`
——**合成探针流量被算进了奖励基线**。

### (a)/(b) 的判定

- **(a) 显式重新基线化**：不可行——会话族 7 天只有 **18** 条 auto 行。
- **(b) 引入独立稳定 cohort**：不可行**且方向错**——它修「换源族不可比」，
  而实测显示 cohort 在 v1 里就已经是错的总体。换源族修不好一个定义错的总体。

⇒ **前置条件是一个此前从未出现在任何文档里的问题：cohort 的总体定义与被结算总体
不对应。** 这是下一轮该做的第一件事。

### ★我差点得出的第三个错误结论（取样假象）

查「30 天已结算 selection 有多少能在 v1 找到同 `request_id`」得到 **21,367 : 15
= 0.07%**，像灾难级丢失。**是假象**：252 上 `request_logs` **总共 32,987 行、
全在 7 天内**（`ts > now()-30d` 的 count 等于全表 ⇒ 无分区历史）。
两侧都取 7 天重做：21 条已结算、15 命中（71%），**仍有 6 条（29%）缺失**
——这个 29% 需独立复核，本轮**未做完，不给结论**。

⇒ 「99.93% 丢失」不能写进任何结论。这是「**没查到 = 不存在**」的第三次变体，
只是这次发生在**时间窗口**而非文件范围上。

### 附带实测（可能对别的审计有用）

- v1 auto 22,406 行 / 7 天；`origin_stage`：`node_probe` 19,252 / `business` 3,154。
- 跨族按 `request_id`（hot ∪ parent）：v1 auto 行只有 **15** 条在会话族，且全是 business。
  ⇒ **99.5% 的业务 auto 流量根本没进会话族。**
- 业务 auto 行的 `task_type`：**3,139 / 3,154 是 NULL**，只有 `code` 10 / `chat` 3 /
  `long_context` 2。⇒ 它们在 cohort 里全落进 `<null>` 桶，而不是 `chat`/`code`。
- 近 7 天未结算 selection：**0 条**（settle worker 当前无事可做）。
- 指纹：`system_fingerprint` 在 252 上同样需要复核（§9.51 的结论来自本地库，
  **不能外推到 252**）——这是下一轮该补的。

### 测试

本轮**无代码改动**（取证轮），故无测试。

### 下一轮提示词

> §9.54 上 252 实测，**推翻了 §9.44 的 (a)/(b) 两个选项**：不是「cohort 换源族不可比」，
> 而是 **cohort 在 v1 里就算错了总体**。线上正在发生：
> `creative`(4,985) / `reasoning`(1,269) / `planning`(4) 共 **29.3% 的已结算 selection
> 完全无基线**（走中性 0.5）；`chat`(12,864) / `code`(3,501) 共 **76.6% 拿着 n=3 / n=10
> 的「分位数」打分**；而 `probe_triggered` 贡献 cohort 的 **99.95% 行数却服务 0 条结算**。
>
> ① **下一轮该做的第一件事**（此前从未出现在任何文档里）：
>    **让 cohort 的总体与被结算总体对应**。候选做法（需要你拍板）：
>    (i) cohort 改从 `auto_route_selections` 的历史导出（与结算同源，最贴切）；
>    (ii) 保留 `request_logs` 但把 `SQLExcludeSyntheticActors` 换成
>         `origin_stage = 'business'`（消除 `node-probe-worker` 污染）——
>         注意这是**行为变更**，会改线上奖励数值；
>    (iii) 两者都做。**在此之前不要碰 (a)/(b)。**
> ② **必须补的量具缺口**：`llmgw_autoroute_settle_baseline_cohort_rows` 是**全局**计数，
>    在本次这个形态下（cohort 非空 19,252、但 29.3% 结算无基线）它**不会响**。
>    需要**按 task_type 的覆盖率**指标 + 告警（无基线的结算数 / 结算总数）。
>    这就是 §9.44 那条「cohort 归零」告警的盲区——它只测全局，不测分布。
> ③ **待复核**（本轮没做完，不许当结论用）：7 天匹配窗口内 6/21 = **29%** 的已结算
>    selection 在 v1 里找不到对应行。需确认是保留期问题还是真丢失。
> ④ **需复核**：§9.51「上游从不发 `X-System-Fingerprint`」是**本地库**结论，
>    **不能外推到 252**。252 上必须重测，否则那条告警的诊断在生产上是错的。
> ⑤ 其余遗留未变：§9.49.8 `silently_degraded_content` 是否扩档；§9.48
>    `silently_frozen` 21 条口径；§9.52 遗留的「arm 移到 domains/streaming」。

---

## 第三十六轮（§9.55）：252 补测 —— 订正 §9.54.2，并确认 §9.51 在生产成立

用户授权的 252 只读测量，本轮补完我自己在 §9.54 里标为「未做完、不给结论」的两项。

### ① §9.51 在 252 成立（此前是本地库结论，不能外推）

| 面 | 行数 | 指纹非空 | 覆盖 |
|---|---:|---:|---|
| `request_logs`（v1） | 32,987 | **0** | 全表仅 7 天 |
| `session_turns` | **804,096** | **0** | **全时段** |
| integrity JSONB | 3,829 | **0** | 09-07 → 10-02 |

⇒ 告警文案「上游从不发指纹」的诊断**在生产上是对的**。

### ② §9.54.1 ②③ 成立，但方向和我写的相反

- v1 `business` auto 行 3,154 → 会话族 **15**（0.48%）⇒ 「业务 auto 流量没进会话族」**站得住**。
- 但近 7 天已结算 selection 21 条 → 会话族 **18（86%）**、v1 **15（71%）**。
  逐条：15 条两族都有；3 条（09-29/09-30）**只在会话族**；3 条（09-25/09-26）
  两族都没有、卡在 7 天窗口边缘。
⇒ **会话族对结算行的覆盖比 v1 更好。** §9.54.4 的「29% 缺失」是
「窗口边缘」+「拿 v1 当基准」两个 artifact 叠加，不是丢失。

### ★③ 订正 §9.54.2：那些「已结算 selection」是**三周前的历史**

`auto_route_selections` 日量：09-07 = 10,933、09-08 = 11,411，09-09…09-14 = **0**，
09-15 起 0–120/天，**近两周半稳定在 0–14/天**。

**已排除「分区被删」**：`2026_08/09/10/11/default` 分区**全部存在**，
真实 `count(*) = 22,625`（最早 09-07、最晚 10-02）。⇒ **是没产出，不是被删。**

⇒ **§9.54.2 那张「29.3% 已结算无基线」的表是跨期对照**，把当周 cohort 与
三周前两天的 settlement 放在一起比——**不是当下正在发生的缺陷**。
「cohort 词表与 settlement 词表几乎不重叠」这个静态事实仍成立，
但含义变了：现在 auto 流量几乎全是探针，因为**真正的 auto 路由已两周半没产出 selection**。

### 同 base / 同库 / 同时刻的 before-after（重要，别只读结论）

用 `git worktree add --detach /tmp/wt-base origin/main` 建基线跑同一批门：

| 门 | origin/main | 本轮 |
|---|---|---|
| `TestRequestLogsReadInventoryIsComplete` | **FAIL**（我 §9.222 弄红） | **PASS** ✅ |
| `TestViewArmCutoverReadersRegistryIsConsistent` | PASS | FAIL → 已登记 → **PASS** ✅ |
| `TestV1BodiesReadersAreAssessed` | 不存在 | **FAIL（故意红）** |
| **合计** | **FAIL 6** | **FAIL 6**（−1 修好、+1 故意红、其余 5 条逐条相同） |
| `TestColumnarParentTwoSurfaceSetopShape_RealDB` | FAIL | FAIL（既有，非回归） |
| `TestReportRollup_HTTPContract` | FAIL | FAIL（既有，非回归） |
| `TestSessionFamilyTwoSurfaceUnionShapeIsExecutable` | FAIL | FAIL（既有，非回归） |
| `TestSessionFinalSuccessBacklogIsClosed` | FAIL | FAIL（既有，非回归） |
| `TestProjectTasksSkipsNullTaskID` | FAIL | FAIL（既有，非回归） |

★ **扩总体当场在另一张登记表上炸出一条真发现**：
`admin/session_online.go` 进了 inventory，`TestViewArmCutover…` 立刻报它
「不在 `viewArmCutoverReaders` 里」。已登记。
它的失效形态与同表其它条目**不同类**：v1 臂消失后那个 JOIN 取不到 `id`，
在线会话列表**整页为空**——不是缺一列，是连不上行。
⇒ 「扩总体 ⇒ 漏登记被暴露」这条链是真跑通的，不是推理。

### ⚠ 一条**假红**（记录它是因为它会再次发生）

全量 `admin` 跑到一半时我做了 `git rebase origin/main`（他人推进 main，
无文件重叠所以 rebase 干净）。而这一族门**在运行时读磁盘**
（`TestEveryV1ReaderIsAnalyzedByExtractor` 扫 2,277 个生产文件）⇒ 跑一半的
文件集与编译时不一致 ⇒ 报了一条新 FAIL。

单跑复验：`PASS（2277 文件 / 149 个 v1 引用 / 盲区 0）`。

⇒ 我**丢弃了那一整轮结果**，没有「取其中看起来没受影响的部分」——
被污染的批次里任何一条绿都不能当证据。与 §9.216「运行中的二进制不是 HEAD」
同族：**别在活动的东西上取读数，更别在活动的东西上跑一整轮测量。**

### 下一轮第一件事（本轮没查，也不猜）

**为什么 auto-route 从 2026-09-15 起不再产出 selection。** 证据不足，
任何归因都是编的。**这很可能才是 §9.44 整串问题的上游成因**：
没有 selection ⇒ 没有需要基线的结算 ⇒ cohort 退化成一个纯探针统计量。

### 方法论（本节最该带走的）

**发现一次取样假象之后，必须把它当成一类错误去搜，而不是当成孤立事故修掉。**
§9.54.4 我发现了「v1 只有 7 天保留期」这个假象，**却只怀疑了 v1 一侧**，
没回头质疑「30 天 settlement vs 7 天 cohort」这个**时间轴错配**——
它和前一个是同一类错误。三处 artifact（v1 保留期 / 窗口边缘行 / epoch 错配）
必须一起找。

### 推送状态

`6841c90a8`（§9.54）+ 本节仍未推送：合并被并行会话在
`installer/cmd/llm-gw-installer/main.go`、`installer/internal/dbinit/runner.go`、
`sql/schema/installed_startup_migrations.tsv` 的**未提交**改动挡住（mtime 22:52:33，
在我上一轮报告时仍在被写）。**不 stash、不 restore、不代为提交别人的在途工作。**
⚠️ 远端 `a0da9066d` 同时动了 6 个我的文件，且它也做了「指纹 §9.52 两载体同步」，
合并时**不能盲目选 ours/theirs**，必须逐处看。

---

## 第三十七轮（§9.56）：查 auto-route 停止产出 —— 定位到当前状态，**归因未成立**

### 能验的

**机制 (ii)「写入侧丢弃」被排除。** 252 的 `/metrics`（`127.0.0.1:8780`）返回
`llm_gateway_auto_selections_dropped_total 0`，而 `llm_gateway_auto_selections_total`
**完全不出现**——它是带标签的 CounterVec，首次 Inc() 前不导出任何样本，缺席即
「一次都没写过」。进程启动于 **2026-10-01 05:19:09**，已跑 1 天 17 小时。

⇒ **当前是机制 (i)：决策器根本没产出 selection**，不是被丢弃。

### 不能验的（本节不给 09-09 的归因）

| 想要的证据 | 可得性 |
|---|---|
| 09-08/09-09 应用日志 | ❌ journal **最早只到 2026-10-02 16:22**（7 小时）。「batch insert failed 计数 0」覆盖不到出事那天，**不构成证据** |
| `auto_selections_total` 历史值 | ❌ 252 **没有 Prometheus/Grafana**，无 TSDB |
| 文件日志 | ❌ 只有 `resource_monitor.log` / `shutdown.log`，无覆盖 09-09 的应用日志 |

**要定因需要**：覆盖 09-08/09-09 的应用日志，或 Prometheus 侧
`llm_gateway_auto_selections_total` 的历史序列。在此之前任何「部署/开关/探针」
的说法都是编的。

### ★让这件事两周半没被发现的缺口

**`llm_gateway_auto_selections_total` 没有任何告警。** §9.44 建的告警全管**读侧**，
**产出侧「写入量归零」一直没有门**。这与 §9.37「没有告警读的指标是装饰」是
**镜像形态**：这个指标**有生产者、有指标、无消费者**，于是它在监控上根本不存在。

**本轮不落这个告警**：`deploy/prometheus/rules/` 正被并行会话 `a0da9066d` 改动，
我的合并尚未解封 ⇒ 记账为下一轮第一件事。

### 下一轮提示词（接第三十七轮）

> ⚠️ **先解封合并**：`6841c90a8`(§9.54) / `0e2ea9e2f`(§9.55) / 本节 共 3 个
> 我的提交 + 3 个并行会话提交仍未推送。阻塞在 `sql/schema/01-schema.sql`、
> `sql/schema/installed_startup_migrations.tsv`、
> `deploy/sql/schemas/baseline/01-schema.sql` 的**未提交**改动。
> **不 stash、不 restore、不代为提交别人的在途工作。**
> 解封后合并要逐处看，不能盲目 ours/theirs：远端 `a0da9066d` 也做了
> 「指纹 §9.52 两载体同步」，和我的 §9.52 改同一批文件。
>
> ① **解封后立刻做**：给 `llm_gateway_auto_selections_total` 加
>    「写入量归零」告警（带活动守卫，避免 worker 未启动时误报）。
>    判据要求：必须能区分「selection 写入量为 0」与「指标未注册」——
>    后者是 CounterVec 首次 Inc() 前不导出，**直接用 increase() 会得到 no-data 而非 0**。
>    这是本条告警最容易踩的坑。
> ② **§9.56 的 09-09 归因仍未成立**。若能拿到覆盖 09-08/09-09 的日志或
>    Prometheus 历史序列，这是**最优先**的取证；在拿到之前不要写归因。
> ③ 需要拍板的三项仍未变：§9.44 cohort（**注意：§9.54/§9.55 已表明 (a)/(b) 都不是
>    杠杆，真正的前置是「cohort 总体 ≠ 被结算总体」**）、§9.49.8 是否扩档、
>    §9.48 `silently_frozen` 21 条口径。

---

## ⚠️ 第三十八轮：绕开共享工作区推送，**留下一个必须知道的分叉**

### 做了什么

`sql/schema/01-schema.sql`、`sql/schema/installed_startup_migrations.tsv`、
`deploy/sql/schemas/baseline/01-schema.sql` 三个文件一直被并行会话的**未提交**改动
占着，常规 `git merge origin/main` 被 git 拒绝。**没有 stash、没有 restore、
没有代为提交别人的在途工作。**

改用：`git worktree add` 建一个独立 worktree（分支基于 `origin/main`），
在里面只重放**我自己的**两个文档文件，然后
`git push origin mavis/push-9xx:main`。**主工作区全程未被触碰**
（那三个文件原样保留）。

### ★cherry-pick 会删除并行会话的内容——已放弃该路径

第一次在 worktree 里用 `cherry-pick` 重放我的 3 个提交，**git 报「自动合并」且无冲突**，
看起来是成功的。逐节核验时发现它**删除了 10 行**：并行会话 `a0da9066d` 新增的
`§9.62`（`client_ip` ParseIP 门的 5 行输入/输出表）与 `§9.63`
（`raw_model_name` 不需要 817 的结论）。

**原因**：cherry-pick 的 diff 是相对**我本地基**算的，而我的基不含那两节
⇒ 「我这边没有」被算成「删除」。**冲突为空 ≠ 内容无损。**

⇒ 改用：`git reset --hard origin/main` 还原，再把自己的三节**纯追加**回去。
最终提交 `3ae6c5295` 是 **623 行插入、0 删除**，§9.62/§9.63 与 R33 的笔误订正
全部保留。

### ★★留下的分叉：下次合并会撞

| | 内容 | 提交 |
|---|---|---|
| `origin/main` | §9.54/55/56 | **`3ae6c5295`**（三个文档合成一个提交） |
| 本地 `main` | §9.54/55/56 | `6841c90a8` / `0e2ea9e2f` / `d82e23e12`（三个独立提交） |

两边**内容相同、提交历史不同**。下次把 `origin/main` 合进本地 `main` 时，
审计文档的 EOF 追加**大概率冲突**（上下文不同：远端在我的三节之前有 §9.62/63）。

**处理方式需要你或下一个会话决定，本轮不代劳**——因为本地 `main` 上还挂着
并行会话自己的 4 个提交（ursm 817→818、payload 字段拆分、installer 818 同步、
INSERT 列序门），动它的历史会牵连那些提交。可选：
- 合并时**丢弃**本地那 3 个文档提交（内容已在上游），保留并行会话的 4 个；
- 或先把这 3 个文档提交的改动 `git checkout` 掉再合并。
**不要**用 rebase（会重写别人提交的哈希）。

### 清理

临时 worktree 与临时分支已删除；`git worktree list` 恢复原状。

---

## 第三十九轮（§9.57）：补上 auto-route **产出侧**的洞

### 做了什么

`llm_gateway_auto_selections_total` 此前**有生产者、有值、零消费者**——
到 §9.56 为止所有告警都建在**读侧**。本轮补两条产出侧告警 + 3 道 Go 门 +
6 个 promtool 场景。`selection_metrics.go` 注释里那句
「dropped worth alerting on rather than merely graphing」此前**没有门兑现**。

### ★核心：CounterVec 陷阱

带标签的 CounterVec 在**首次 `Inc()` 之前不导出任何序列** ⇒
`sum(increase(...)) == 0` 在「从未产出」时得到**空向量**，`empty == 0` 仍是
空向量 ⇒ **告警永远不响**，而那正是它唯一要抓的场景。

**这条断言不是靠注释声明的，是被 promtool 场景 A 证明的**：在真的没有该指标
任何序列的输入下要求规则触发；要得到 1，表达式里必须有「把空转成 0」的那一项。

### ★场景测试抓出规则本身的两个真缺陷（`check rules` 全无感）

1. `and on()` 返回**左侧**标签集，左侧无标签 ⇒ 告警**丢 instance 归属**。
2. 修成 `by (instance)` 后**仍丢 job** ⇒ 最终是 `by (job, instance)` +
   `and on(job, instance)`。

### ★我自己的 Go 门在修复面前误报了两次

第一版门用 `Contains(expr, "or vector(0)")`。第二轮把表达式改成
`or (0 * max by (job, instance) (up))`（**同一个作用**，还多保住了标签）后门红了。

⇒ **门若钉死字面串，就会在一次修复面前误报。** 改成断言**机制**（正则）。

**与 §9.52「门被反转」是同一模式的反面**：那次门在缺陷修好后红（**正确**），
这次门在缺陷修好后仍红（**不正确**）。区别在于判据锚的是**字面串**还是**机制**。

### 已知局限（已写进 yml，且有门守着「必须写下来」）

1. 假设该部署在用 auto-route；完全不用 auto 路由的部署会**永久**报红。
2. **252 上没有 Prometheus ⇒ 这组告警在 252 不生效**（适用 154 / 本地）。

### 验证

```
promtool check rules  → SUCCESS: 2 rules found
promtool test rules   → SUCCESS（6 场景）
go test ./deploy/prometheus/rules/ -count=1 → ok
变异 P1/P2/P3（promtool）+ N1/N2/N3（Go 门）全红，全部还原
```

### 下一轮提示词

> ⚠️ **仍未推送**：本节 + §9.57 三个新文件，以及 §9.54/55/56 已上远端但
> 本地 main 仍留着三个等价提交（见第三十八轮记录）。合并时需人工裁决，
> **不要用 rebase**。
>
> ① §9.57 已补上 auto-route 产出侧的洞。**剩下的真实问题不是告警，是
>    auto-route 为什么从 2026-09-15 起不产出 selection**（§9.56）——
>    证据（覆盖 09-08/09-09 的日志 / Prometheus 历史序列）本轮不具备，
>    **拿到之前不要写归因**。
> ② 需要拍板的三项仍未变：§9.49.8 `silently_degraded_content` 是否扩档、
>    §9.48 `silently_frozen` 21 条口径、以及 cohort 总体修正方案
>    （(i) 从 `auto_route_selections` 历史导出 / (ii) `origin_stage='business'`
>    取代手工 actor 名单（**会改线上奖励数值**）/ (iii) 两者都做）。
> ③ §9.57.6 的两个局限里，「本部署是否启用 auto 路由」缺少配置位，
>    导致不用 auto 路由的部署会永久报红。若要消除需新增部署级开关。

---

## 第四十轮（§9.58）：★撤回 §9.54.3 的「停写前阻塞项」

### 结论

§9.54.3 写「99.5% 的业务 auto 流量根本没进会话族」，并列为停写前阻塞项。
**前提是错的，撤回。**

我当时用 `origin_stage = 'business'` 当「真实业务流量」的代理。252 实测：

| origin_actor | origin_stage | 行数 |
|---|---|---:|
| `auto-summary-generator` | `business` | 1,924 |
| `auto-title-generator` | `business` | 1,242 |
| `<null>` | `business` | **15** |

按 `task_type` 是否为空切分：**空 3,166 条 → 进会话族 0 条；
非空 15 条 → 进会话族 15 条（100%）。**

⇒ **真正的业务 auto 流量是 15 条，且 15 条全部进了会话族。**
那 3,166 条是 auto 标题/摘要生成器，**被排除是正确的**（不是用户轮次）。

⇒ **镜像行为完全正确，是我把内部生成器误认成业务流量。**

### 根因：两份「内部 actor」名单互不相认

`IsInternalAutoEntry`（telemetry 包）认 `auto-title-generator` /
`auto-summary-generator`；而 `middleware/origin_mw.go` 的
`trustedOriginOwners` / `systemOwnerFallbackStage` /
`globalAuthStageActorPairs` **三个名单里这两串一次都没出现**
⇒ origin 中间件把它们盖成 `stage=business`。

⇒ **同一件事有两份真相源，其中一份漏了两个成员。**
（同族：§9.45 的「三个真相源让门测不出差别」、
`KeyRequestLogsWriteEnabled` 因「同一键被五处各写字面量」才被提成常量。）

### 后果

`origin_stage` 在 auto 总体上**不是**可靠的「是否内部」判据。任何用
`origin_stage='business'` 筛选的查询（含 §9.54.2 的 cohort 分析）
都会混进 3,166 条内部生成器。

### 修法（需拍板，本轮不实施）

(a) 把两个 actor 补进 origin 的系统 actor 名单 —— **会改历史行判定口径**；
(b) 新增「是否内部」的单一判定函数，所有筛选方改用它 —— 不碰历史值；
(c) 先只加门钉出差异 —— **会常红挂 CI**，应先裁决再落。

### 下一轮提示词

> ⚠️ **§9.54.3 的「99.5% 业务 auto 流量没进会话族 = 停写前阻塞项」已撤回**（§9.58）：
> 那 3,154 条里 99.5% 是 `auto-title-generator`/`auto-summary-generator`，
> 被排除出 `session_turns` 是**正确的**；真正业务 auto 只有 15 条，15 条全在会话族。
> **会话族的镜像行为没有问题。**
>
> ① 需要拍板：三份 origin actor 名单与 `IsInternalAutoEntry` 的名单不一致
>    （`auto-title-generator` / `auto-summary-generator` 缺失），选 (a)(b)(c) 哪条。
>    在裁决前，**不要**再用 `origin_stage='business'` 当「真实业务」的判据。
> ② 仍未查明：auto-route 自 2026-09-15 起不产出 selection 的原因（需覆盖
>    09-08/09-09 的日志或 Prometheus 历史序列，现有环境不具备）。**不写归因。**
> ③ 仍未拍板：cohort 总体修正方案 / §9.49.8 是否扩档 / §9.48
>    `silently_frozen` 21 条口径。注意 §9.54.2 的 cohort 分析也受本节影响。
> ④ 推送状态：§9.57 已上 origin/main（2e68487a8）；本地 main 仍有 3 个
>    等价文档提交 + 并行会话 4 个提交，合并需人工裁决，**不要用 rebase**。

---

## 第四十一轮（§9.59）：★核心目标「前后数据一致」第一次在**生产库**上被验证 —— 并发现我那道门漏了一整个存储面

### 结论

用户目标原话「**确保数据在更改前后一致**」的执行者是 §9.32 建的
`TestDualWriteValueParity`。但它**只在本机库跑过**（§9.32.5 自陈
「写入方身份未确认」），252 上验的一直只是存储可用性。
本轮在 252 上跑了**同一道门**（不重写 SQL），并修了它的一个取样洞。

### 三个根因

1. **门漏了一整个存储面（已修）**
   `request_logs_hot` 与 `request_logs` 是**独立存储面，不是分区父子**
   （`pg_inherits` 实测：`request_logs` 只有 4 个月度子分区）。
   原门 v1 侧只 `FROM public.request_logs`，而 session 侧读两面 ⇒ **口径不对称**。
   252 实测漏 **2,121 对（占全部配对的 16.5%）**，且漏的恰是**最新**那批。
   修正后配对行 10,634 → **12,798**，判据仍全绿（`success` 零差异）。

2. **内连接数的门，看不见镜像侧的丢失（已补报告门）**
   补 `TestDualWriteParityCoverageReport`：**只报告、从不判红**——
   因为「v1 有行而会话族没有」本身不是缺陷（内部生成器按设计就不镜像），
   把它变判红门需要先裁决口径（§9.58 刚在同族字段上栽过）。
   它唯一会红的一格是**自检**：`全集 == 父表 + _hot`，防止取样面被静默改窄。

3. **连接参数只改了一半（已修，抽象成 `openParityPool`）**
   252 `statement_timeout` 默认 30s，覆盖率查询冷缓存 23s。
   先只改一道门，**同一轮里另一道就以 `SQLSTATE 57014` 红了**——
   而那条报错长得像「SQL 写错了」。

### 关键实测数字（252，2026-10-02 23:2x–23:4x，活库）

| 项 | 值 |
|---|---|
| v1 非探针全集（父表 + `_hot`） | 16,550 |
| 能与会话族配对 | 12,802（**77.4%**） |
| `success` 不一致 | **0** |
| `prompt_tokens` 不一致 | 5（0.039%，阈 0.05%） |
| 模型归一化后仍不同 | 14（0.11%，阈 2%） |
| 未配对·内部生成器 | 3,722 ✅ **按设计不镜像** |
| 未配对·非内部·卡非终态 | 20 ⚠️ 按设计（`isTerminalFailure` 不认 `in_progress`） |
| 未配对·非内部·**已终态却无孪生** | **6 ❌ 未查明** |

配对成功的 12,802 行里 `in_progress` **一条都没有** ⇒ 镜像首门按设计工作。

### 我自己写了一道装饰门，靠变异删掉了

`TestTerminalGateAndIsTerminalFailureStayConsistent` 里**复刻**了一份首门判定式。
变异 M2 把 `hook.go:71` 真门改成 `if !entry.Success { return }` 时，**它全绿**。
真正抓住 M2 的是既有的 `TestPersistHook_MirrorsTerminalFailure`。
⇒ 已删除该护栏并写明原因。**门测的必须是被守的那一处。**

### 交付物

| 文件 | 改动 |
|---|---|
| `cmd/tools/validate_sessions_v2/dual_write_value_parity_integration_test.go` | v1 侧补 `_hot`（两处查询）；新增 `openParityPool`；新增覆盖率报告门（含取样面自检） |
| `internal/sessionv2mirror/terminal_failure_gate_test.go` | **新增**：`isTerminalFailure` 接受集合 10 条表驱动子用例 |

### 测试

```bash
go build ./...                      # OK
go test ./internal/sessionv2mirror/ ./cmd/tools/validate_sessions_v2/ ./cmd/gateway/ -count=1
# ok / ok / ok
# 真库（252 经隧道）：
TEST_PG_URL=postgres://…@127.0.0.1:15432/llm_gateway?sslmode=disable \
  go test ./cmd/tools/validate_sessions_v2/ -run TestDualWrite -tags=integration -count=1
# PASS（值层 12,798 配对；覆盖率 16,550 → 12,802 / 3,722 / 20 / 6）
```

变异验证：M1（`isTerminalFailure` 收 `in_progress`）红；
M2（真门改坏）由既有门红；M2 第一版 perl 未匹配=**没生效的变异，不算证据**。

### 遗留风险

1. **6 条已终态却无孪生的请求仍未查明**。已排除整族无痕 / 合成会话 /
   `IsInternalAutoEntry`；未排除异步队列丢弃、`shadowWriteEnabled` 当时为假、
   `entryToProcessedRequest` 返回 nil。**未写归因。**
2. **「哪些请求开始了却没结束」在 S4 之后将无法回答**——`in_progress` 行只存在于 v1，
   而 v1 将不再写入。这是**退役口径**问题，不是镜像 bug，需要拍板是否补落点。
3. **spec 的退出条件仍未满足**：「dual_read_validator 对账 7 天零漂移」。
   本节是**单次快照**，不满足它；且 §8.4 已记录 validator 读 v1 面、
   S4 会关掉自己的观测手段这一设计矛盾。**不要把 §9.59 当成 S4 的放行依据。**
4. 模型归一化后残余里有 `session=""`（session 侧模型为空串），
   量级 5/12,798，未定性。

### 下一轮提示词

> ① **优先**：查 §9.59.9 那 6 条「已终态却无孪生」的请求。方向：
>    镜像异步队列是否有丢弃计数、`sessions_v2.shadow_write` 在
>    2026-10-01/02 是否曾为假、`entryToProcessedRequest` 的返回 nil 条件。
>    **拿到证据前不写归因。**
> ② 需要拍板：「开始了却没结束」这个事实，退役 v1 后要不要留落点
>    （在会话族补一类状态 / 接受丢失 / 另建小表）。这是口径决策。
> ③ 仍待拍板（沿用上轮）：三份 origin actor 名单与 `IsInternalAutoEntry`
>    不一致选 (a)(b)(c)；cohort 总体修正方案；§9.49.8 扩档；
>    §9.48 `silently_frozen` 21 条口径。
> ④ 仍未查明：auto-route 自 2026-09-15 不产出 selection 的原因
>    （需覆盖 09-08/09-09 的日志或 Prometheus 历史序列）。
> ⑤ 通用纪律（本轮两次踩到）：**同族的东西要一起改**——
>    本轮修了连接参数只改一半，当场红了；**新写的门必须做变异**，
>    本轮一道门就是这么变装饰的。

## 2026-10-03 追加：§9.65 查 §9.59.9 那 6 条「已终态却无孪生」

### 一句话结论

**三个方向排掉两个，第三个不是主因。** 这 6 条**不能**归因到异步队列丢弃——
异步丢弃实测 90 次，其中 87 次（96.7%）被 outbox+reaper 补回，净残留 3 条。
剩下这 3 条走过的每条**已知**路径都被排除了，而**能区分它们的那条证据已不存在**。

### 排掉的两个

- **`shadowWriteEnabled` 曾为假 —— 排除。**
  最强的一条不是配置表，是**同窗对照**：每个缺失点 ±60s 内都有成功镜像的请求
  （13:2 / 3:2 / 5:1 / 3:1）。flag 若为假，同窗内应**全部**缺失。
  配置表那条也成立：`prev_value IS NULL` ⇒ 该行自 2026-07-21 建行起**从未被 UPDATE**
  （`store_db.go:77` 每次 UPDATE 都写 `prev_value`），且无迁移裸写该键。

- **`entryToProcessedRequest` 返回 nil —— 排除。**
  只有 `entry == nil || sessionID == ""` 两个条件；6 条 `gw_session_id` 全非空。
  顺带排掉 §9.59.9 没点名的第四道闸门 `IsProbeSyntheticSession`（首分支即 false）。

### 收窄的第三个，以及它暴露的真问题

- **§9.59.9「outbox = 0 ⇒ 没有异步丢弃」是无效推理。** `replay.go:453` 重放成功
  **即 DELETE**，它是瞬时面。实测：23:5x 抓到 pending=1，几分钟后 = 0。
- **in-process backlog 是黑洞**：`DrainBacklog` 生产代码**零调用**，
  出口只有「满 10000 淘汰」或「随进程消亡」。但 backlog 当前为 0、计数才 90
  ⇒ 淘汰不可能 ⇒ **这 3 条没进过 backlog**。
- **缺失率按进程实例分层**：pid 786438（只活 32 分钟）0.680%，是基线的 13 倍；
  6 条里 4 条落在它里面。但**这只是相关性**——这些实例的 backlog 已随进程消亡，
  排除推理只对当前进程成立。

### 6 条不是一类（§9.59.9 把它当一个桶了）

- 3 条是**多轮会话里中间某一条**没落（52 / 54 / 600 轮里缺 1）；
- 3 条是**单请求会话**，丢掉唯一一轮 ⇒ **整个会话在会话族里不存在**。

⇒ 「整个会话消失」是「这一条丢了」的退化形态，不是独立现象。
（分桶又一次是防误判的必要动作，§9.58 同族。）

### ★硬边界：这 3 条永远无法再查明

日志只留 6.75 小时（journald 最早 2026-10-02 17:13，6 条全在窗口外）；
计数器是**进程内存**，而 4/6 条正来自已死掉的上一进程；outbox 成功即删。
⇒ **「丢失」与「证据消失」由同一个重启事件同时造成。**

这与 §9.59.7「我写了道装饰门」是同一条纪律的两个方向：
那次是**门测了自己那份复刻**，这次是**唯一能作证的面被设计成用完即焚**。

### 比这 6 条更重要的问题

这 6 条是 0.035% 的噪声。真问题是**这类缺失在系统里不可检测**：
失败登记用完即焚、计数器跨不住重启、backlog 无消费者、
唯一能定位到 request_id 的面（`slog.Warn`）随 journald 消失。

⇒ **S4 停写前不解决这三条，「确保数据在更改前后一致」就仍只有一次性快照的强度。**

### 下一轮提示词

> ⚠️ **§9.65 写在本地 `main`，而 §9.59 只在 `origin/main`**（分叉 10/18，
> 4 处冲突含本文件）。合并裁决后应把 §9.65 并入 origin/main 版本的 §9.59.9，
> **不要两边各留一份**；**不用 rebase**。
>
> ① 需拍板三条判据的取舍（**本轮未实施**）：失败登记保留期 + 计数跨重启基线 +
>   backlog 淘汰落盘还是取消 backlog。
> ② §9.59.9 原措辞应更新：后两个方向**已排除**，异步丢弃**已收窄且不是主因**。
> ③ 「短命实例缺失率高 13 倍」**仍是相关性**，证成因果需要①那件事。
> ④ §9.54/§9.56/§9.58 的其余待拍板项（actor 名单 (a)(b)(c)、cohort 修正、
>    §9.49.8 扩档、§9.48 口径、auto-route 09-15 归因）**本轮未动**。

## 2026-10-03 追加（第三轮）：§9.68 auto-route 断点归因 + 撤回 §9.56 的核心推论

### ① 断点不是 09-15，是 09-09——且成因仓库里早有记录

`auto_route_selections` 日分布：09-07 = 10,933、**09-08 = 11,411**、
**09-09→09-14 = 0**、**09-15 = 9**、09-16/09-17 各 120、之后 1–14 不等。

⇒ **「自 09-15 起不产出」与数据不符**（09-15 产出 9 条）。
断点 09-09、恢复 09-15 00:17 之后。

成因（`fe2548645`，**2026-09-15 00:12:28** + `bac8b6e9e` 提交信息自述
「09-08 蓝绿降度 traffic-only 无 decider 所致，O5 修复后 00:17 自愈」）：
auto 决策引擎整体装配在 `if !bgDataPlaneOnly` 内，而
`bgDataPlaneOnly = BGMode=="data-plane" || IsTrafficOnly()`
⇒ 09-08 蓝绿降级置成 traffic-only ⇒ decider 不装配 ⇒ 09-09 起归零。

**时间线与生产数据逐点吻合。** 这是本轮唯一不靠新证据、
靠「已有仓库记录 + 生产数据对表」拿下的归因。

### ② 当前低产出不是故障

252 未设 `LLM_GATEWAY_BG_MODE` / traffic-only ⇒ decider 已装配
（`fe2548645` 确在运行二进制 `cc0e977f` 祖先链上）。
`auto_decision` 58.6–58.8% 的行是**真实决策对象** ⇒ 决策器在跑。
而 `client_model='auto'` 的请求：09-30 = 0、10-01 = 2、10-02 = 0，
那 2 条还都失败、都没产生决策。

⚠️ **`is_auto_request` 不是判据**（§9.58：内部生成器也置位它）。
真判据是 `client_model == autoRequestMagic`。

⇒ **低量是因为几乎没有客户端再发 `model="auto"`。**

### ③ ★§9.56 的核心推论被推翻

`/metrics` 里 `llm_gateway_auto_selections_total` 仍不存在，
**但 `auto_route_selections_hot` 在 10-03 00:23 / 00:27 有 2 行真实决策**
（`classifier=llm_v2`、`chosen_model=glm-5.1`、`success=t`）。

五环逐个验过，全部指向「计数器必被调用过」：
唯一写入方（`selection_writer.go:321`，`pg_trigger` 零触发器）、
`flush()` 无分支绕过、`merge-base --is-ancestor` 通过 +
读 `cc0e977f:` 文件内容**逐字一致**、二进制无版本漂移
（version.json / commit 时间 / 构建时间 / mtime 四者吻合）。

⇒ **「CounterVec 缺席 = 一次都没写过」在本环境不成立。**
⇒ **§9.56 的「机制 (i)」必须撤回。**

> **可推广**：**指标缺席**不蕴含**行为未发生**，
> 除非同时证明「它是唯一可观测面」**且**「注册/暴露链路通电」。
> §9.56 两样都没验。这是 §9.37「没有告警读的指标是装饰」的**镜像形态**：
> 那次是指标无人消费，这次是**有人消费，却把缺席读成了没发生**。

### ④ 未解的矛盾（不给归因）

计数器不存在，而上面五环全指向它必然存在。**我没有解释它。**
已排除：第二个 gateway 进程（`/usr/local/bin/gateway` 实测不存在）、
版本漂移、触发器/第二写入方、registry 未暴露（同文件 `dropped_total` 在）。

⇒ **下一轮第一件事**：钉死这条矛盾。
它决定 §9.57 那道「selection 写入量归零告警」**是否建立在正确前提上**——
若 `/metrics` 根本不导出该指标，那道告警会**恒绿**
（§9.37 的加强版：**指标连样本都没有**）。

### 下一轮提示词

> ⚠️ ③ §9.68 已撤回 §9.56 的「机制 (i) 决策器根本没产出 selection」：
> `auto_selections_total` 缺席，但 `auto_route_selections_hot` 10-03 00:23/00:27
> 有 2 行真实决策（llm_v2 / glm-5.1 / success=t），五环逐个验过。
> **「CounterVec 缺席 = 一次都没写过」在本环境不成立。**
> 断点实为 **09-09**（09-15 00:17 已自愈，当日产出 9 条），
> 成因是 `fe2548645` 修的 `!bgDataPlaneOnly` 门控；当前低量是
> 「客户端不再用 model=auto」，**两者不是同一件事**。
>
> ① **下一轮第一件事**：钉死「计数器不存在但必被调用过」这条矛盾——
>    它决定 §9.57 的告警是否恒绿。
> ② ②③ §9.67 的 `request_abandoned`（迁移 819）**未部署到 252**、
>    **未加门/告警**（表增长目前无人看）。
> ③ ③ actor 名单 (a)(b)(c)、cohort 修正、§9.49.8 扩档、§9.48 口径
>    **仍待拍板**。
> ④ 推送状态：§9.65/§9.66/§9.67/§9.68 与 819 全部**未提交**，
>    且本地 main 与 origin/main 仍分叉（10/18，4 处冲突），
>    合并需人工裁决，**不要用 rebase**。

## 2026-10-03 追加（第四轮）：§9.69 撤回 §9.68.3 / §9.68.4——共享库的一行不是某台 host 的证据

### ★两条撤回（同源：作用域错配）

**① 撤回「§9.56 被数据库证据推翻」。**
`pg_stat_activity` 实测：`llm_gateway` 库有**三个**写入方——
`172.16.2.209`=**154**（20 连接）、`172.16.2.241`=**245**（20 连接）、`172.16.2.210`=252 本机。
IP↔服务器映射来自 `envs/servers/*/metadata.yaml` 的 `internal_ip`。

⇒ `auto_route_selections` 是三台共写的表；
`llm_gateway_auto_selections_total` 是**单进程**计数器。
我用共享库里的 2 行去否定**一台特定 host** 的结论 ⇒ **错**。

**② 撤回「低量因为没人用 `model="auto"`」。**
`request_logs.client_model` 是 **auto 解析之后**的值：
实测 15 条 selection 逐条 join，`client_model = chosen_model = outbound_model`、
`is_auto_request = t`，原始的 `auto` 已被改写 ⇒ **用它数 auto 请求恒得 0**。
（与 §9.59.3 值层门「`origin_stage` 是无效判据」同族，这次是我临时选的判据。）

**正确判据**：`is_auto_request = true` **且** `origin_actor` 不在生成器名单（§9.58）。
按它量：§9.58 得「业务 auto 15 条」，同期 `auto_route_selections` **也是 15 条**
⇒ **auto-route 系统层面正常工作，数量吻合。**

### 矛盾为何消失

252 的 nginx `upstream kxpms_llm_backend = 172.16.2.209:8781`（**154**），
被 **4 个 location** 使用；配置自带注释亦写
`llmgateway.internal.example.com → 172.16.2.209:8781 (154 native)`。

⇒ **252 的 gateway 进程（`llmgo-252-dev.service`）不承接 `llmgateway.internal.example.com` 流量。**
那 2 行不是 252 写的；**252 的计数器缺席是诚实的**。

⇒ 订正后：§9.56 的**结论成立**（252 确实没产出），
但其**表述**（CounterVec 缺席「即」一次都没写过）把它写成了系统级事实，**不严谨**。

### §9.68 仍然成立的部分

- 断点 **09-09**、恢复 **09-15 00:17** ✅（但日分布是**三台合计**，「哪台断的」未知）
- 成因 = `fe2548645` 的 `!bgDataPlaneOnly` 门控 ✅（**未指名哪台 host**）
- 252 未设 `BG_MODE`/traffic-only ✅

### 新发现的硬缺口

`request_logs` / `auto_route_selections` / `usage_ledger` 等
**都没有** `node_id` / `instance_id` / `host` 列（`information_schema` 实测 0 命中）。

⇒ **三台 gateway 共用一个库，却没有任何列能区分谁写的。**
与 §9.65.8「mirror 失败不可检测」同族：**跨写入方的事实无法归因**。
记账：`auto_route_selections` 需要 `written_by`（host/pid）列。

### 下一轮提示词

> ① **最重要**：③§9.69 撤回了两条，务必以 §9.69 为准而非 §9.68：
>    「§9.56 被推翻」**不成立**（那 2 行来自 154/245，PG 三家共享）；
>    「没人用 model=auto」**不成立**（`client_model` 是改写后的值）。
> ② ④ 断点归因（09-09 + O5 门控）**仍成立**，但**「哪台 host 断的」未查明**。
> ③ ③ actor 名单 (a)(b)(c)、cohort 修正、§9.49.8 扩档、§9.48 口径**仍待拍板**。
> ④ ②§9.67 的 `request_abandoned`（819）**未部署 252**、**未加门/告警**。
> ⑤ 推送状态：§9.65–§9.69 与 819 全部**未提交**；本地 main 与 origin/main
>    仍分叉（10/18，4 处冲突），合并需人工裁决，**不要用 rebase**。

## 2026-10-03 追加（第五轮）：§9.70 ④ 收口——断档在 154，不是宕机，是 traffic-only 长驻进程

### ④ 从「仍未查明」变为「已归因」

| 问题 | 答案 |
|---|---|
| 断点 | **2026-09-09**（不是 09-15；09-15 产出 9、09-16/17 各 120） |
| **哪台 host** | **154**（instance `17a376ad` / hostname `iZbp1efbv6824518ejqh8aZ`） |
| 是否宕机 | **否**——154/245 断档期每日心跳平稳 ~1,440 次 |
| 机制 | 蓝绿降级后长驻进程以 traffic-only 运行 ⇒ decider 未装配 |
| 恢复 | 09-15 00:12 打补丁、00:17 自愈 |

**关键证据**（`instance_heartbeats` 覆盖 2026-07-15 至今、227,322 行）：

- 154 在 **09-07 有 4,700/5,936 次心跳 `uptime < 1h`**（09-06 基线 351/1,427）
  ⇒ **崩溃/重启循环**；09-08 出现 `max_uptime` 76,564s 的进程，
  到 09-09 变 120,184s ⇒ **同一进程从 09-08 凌晨长驻，却再不产出 selection**。
- 154 心跳异常曲线（5,936 / 2,718 → 1,438）与 selections
  （10,933 / 11,411 → 0）**逐日同形**；**245 全程 ~1,440/天，无异常**。

### 顺带订正 `fe2548645` 的指认

该提交根因段写「data-plane 的 **245 永久实例**从未装配 decider」，
`config/runtime_role.go:78` 注释写「traffic-only canaries (**154/245** since
the 2026-08-31 blue-green pinning)」。

⇒ **实测断档在 154，245 心跳全程平稳。** 那句注释里的 245 是举例，
不是当事实例。**归因到 154。**

⚠️ **未直读项（如实标注）**：154 当时那个进程的 `LLM_GATEWAY_RUNTIME_ROLE`
**取值无法回读**（进程已不存在）。「它是 traffic-only」是
「O5 提交自述 + 该形态下无 selection」**推断**，非直读。

### 顺带发现：三台**都没在跑** `LLM_GATEWAY_RUNTIME_ROLE`

三台 `/proc/<pid>/environ` 实测**全部未设** = `active`，
与注释里「154/245 since 2026-08-31 pinning」的长期拓扑**不符**——已回退。

⇒ 但**代码路径仍在**：`LLM_GATEWAY_RUNTIME_ROLE=traffic-only` 会**静默关掉 decider**，
而**唯一能发现它的信号就是「selection 量归零」**。
⇒ **§9.57 那道告警不是「有用」，它是这个门面的唯一探针。**

### 下一轮提示词

> ① ④ **已归因**（§9.70）：断点 09-09、host = 154、非宕机、
>    机制 = 蓝绿降级后长驻进程 traffic-only、09-15 00:12 修复。
>    不要再重查 ④，除非要补「当时 role 取值」那一条**已标注为推断**的项。
> ② **以 §9.69 为准而非 §9.68**（§9.68.3/§9.68.4 已撤回）。
> ③ ③ actor 名单 (a)(b)(c)、cohort 修正、§9.49.8 扩档、§9.48 口径**仍待拍板**。
> ④ ②§9.67 的 `request_abandoned`（819）**未部署 252**、**未加门/告警**。
> ⑤ 新增可做：§9.70.6 —— 给 `LLM_GATEWAY_RUNTIME_ROLE` 加一道
>    「置为 traffic-only 时必须同时确认 decider 存在」的启动门，
>    否则下次蓝绿降级会**静默**重演 09-09→09-14 那六天。
> ⑥ 推送状态：§9.65–§9.70 与 819 全部**未提交**；本地 main 与 origin/main
>    仍分叉（10/18，4 处冲突），合并需人工裁决，**不要用 rebase**。

## 2026-10-03 追加（第六轮）：§9.71 撤回 §9.70.6 后半段 + 把 O5 护栏从装饰变成通电

### ① 撤回：「置 traffic-only 会静默关掉 decider」不成立

§9.70.6 那句写错了。O5 修复（`fe2548645`）恰恰就是把决策引擎
**移出门控、双模式装配**。大括号深度实测 `main.go` 当前结构：

```
5156  autoroute.InitFeatureFlags()      OUTSIDE
5180  decider := autoroute.NewDecider(  OUTSIDE
5448  telemetry.StartSelectionWriter()  OUTSIDE
```

⇒ **今天设 `LLM_GATEWAY_RUNTIME_ROLE=traffic-only` 不会关掉 decider。**
我上一轮建议的「加启动门」也是多余的——那道门 09-14 就有了。

### ② ★但那道门只认一种拼写，对最自然的重犯方式无效

`TestAutoRouteWiringNotGatedOnDataPlaneMode` 的核心断言是**字面量搜索**
`strings.Contains(engineRegion, "if !bgDataPlaneOnly")`。

变异 `if bgDataPlaneOnly { } else { <engine> }`（先确认落盘 + build 通过）⇒ **门绿**。
而 §9.70 刚证明过：这个门控在生产造成过 **09-09→09-14 六天断档**。
反向门控正是任何人恢复「data-plane 不装决策引擎」时最可能写出的形式。

### ③ 修法 + 三个变异

改判**块形状**：回溯找到包含引擎语句的块的开括号，断言支配它的文本里
没有 `if`/`else`/`for`/`switch`/`select`/`case`（原文 Contains 断言保留）。

**修的过程中我自己的新门也漏了一次，被同一个变异当场抓住**：
v1 漏了 `else` —— 反向门控下支配文本是 `"\n\t\telse "`，
真正的 `if` 在一个花括号之前，已被 `LastIndex("}")` 剥掉。
**不是把同一个变异重跑一遍，这道新门会带着同一个洞被提交。**

| 变异 | v1 | v2（最终） |
|---|---|---|
| `if bgDataPlaneOnly { } else {` | ❌ 绿 | ✅ 红（`contains "else"`） |
| `if !strings.EqualFold(cfg.BGMode, "full") {` | — | ✅ 红（`contains "if "`） |
| `if !bgDataPlaneOnly {`（原拼写） | — | ✅ 红（旧断言仍在） |

⇒ **两个不同拼写都抓到 ⇒ 它是形状判据。**
每次变异后 `diff` 与基线 IDENTICAL；恢复后 `go test ./cmd/gateway/` 全绿。

### ④ 可推广的一条

**门抓不住「同一个 bug 的另一种写法」时，它和没有门对 P0 的差别只在于
让人误以为被守着。** 判据要问的不是「能抓到那个 bug 吗」，
而是「**能抓到那个 bug 家族吗**」——验证方法只有一个：
**换一个拼写再变异一次，且必须重跑同一个变异**。

### 下一轮提示词

> ① **§9.71 撤回 §9.70.6 后半段**：今天设 `LLM_GATEWAY_RUNTIME_ROLE=traffic-only`
>    **不会**关掉 decider（O5 已双模式装配，实测三处均在门控外）。
>    上一轮建议的「加启动门」作废——那道门已存在且本轮已修成形状判据。
> ② ④ **已归因**（§9.70）：断点 09-09、host=154、非宕机、traffic-only 长驻进程。
> ③ **以 §9.69 为准而非 §9.68**（§9.68.3/§9.68.4 已撤回）。
> ④ ③ actor 名单 (a)(b)(c)、cohort 修正、§9.49.8 扩档、§9.48 口径**仍待拍板**。
> ⑤ ②§9.67 的 `request_abandoned`（819）**未部署 252**、**未加门/告警**。
> ⑥ 推送状态：§9.65–§9.71 与 819 全部**未提交**；本地 main 与 origin/main
>    仍分叉（10/18，4 处冲突），合并需人工裁决，**不要用 rebase**。

## 2026-10-03 追加（第七轮）：§9.72 给 819 落点配可观测性（三条告警 + 跨文件门）

### 做了什么

`deploy/prometheus/rules/request-abandoned.yml` 三条告警 + 同名 Go 门，
指标在 `domains/hooks/observability/telemetry/request_abandoned_metrics.go`：

| # | 形态 | 告警 |
|---|---|---|
| ① | 一次都没写 | `RequestAbandonedMarkerNeverWritten` |
| ② | 写了但失败（fail-open，设计如此） | `RequestAbandonedMarkerWritesFailing` |
| ③ | **写了但删不掉**（DELETE 半边坏） | `RequestAbandonedLeaking` |

**为什么用计数器对而不是轮询表深度**：`rate(mark) − rate(clear) == 遗弃率`，
这个差值在失效当场可见；表深度是**滞后**症状（要等吸满一天行量）。
既有先例 `session_v2_mirror_outbox_pending` 是轮询 `count(*)`，本表刻意不照抄。

**③ 的阈值是比例**（`clear < 0.5 * mark`）+ `rate(mark) > 0.05` 噪声闸：
要抓的是 DELETE 半边**整体**坏掉（差值 ≈ 全流量，与规模无关），
而正常遗弃率实测 0.047% 永远达不到 50%。

**① 必须带 `or vector(0)`**：带标签 CounterVec 首次 Inc() 前不导出序列，
`== 0` 比的是**空向量** ⇒ 永不触发。**这个坑本项目已栽两次**
（`auto-route-selection-output.yml` 注释一次，§9.68/§9.69 在 252 看到
该指标连样本都没有是第二次）。

### 四次变异，三个抓住、第四个抓出我自己门的洞

| 变异 | 结果 |
|---|---|
| M1 删 ① 的 `or (0*max by…)` 兜底 | ✅ 红 |
| M2 ③ 的 `0.5 *` 改成绝对条数 `100` | ✅ 红 |
| M3 整条删掉 ③ | ✅ 红 |
| M4 删 Go 侧 `recordRequestAbandonedOp("clear_failed")` | ❌ **第一版没抓住** |

**M4 的洞**：判据写成 `strings.Contains(expr, 'op="'+op+'"')`，
而 ② 用的是 `op=~"mark_failed|clear_failed"`（正则择一）
⇒ `clear_failed` 被判成「没被规则用到」而**静默跳过**。
修法：正则解析 `op=(=|!=|=~)"…"` 并按 `|` 拆全部候选值，
**并加自指断言——解析不出任何 op 时直接判红**（"parser is broken, not the rules"）。

> ★**本轮第二个被我自己的变异抓出来的洞**（§9.71 是新判据漏 `else`）。
> 两次共同形态：**「我没检查到」被写成了「不存在」**。
> ⇒ 可推广：**任何带 `continue`/`skip` 的检查循环，都必须有一条
> 「我至少检查到 N 项」的自指断言**，否则解析失败会伪装成通过。

### 仍**未**做

- **未部署到 252**（外部动作，需另行授权）
- **未回填历史**（§9.67.6 口径待裁）
- ③ actor 名单 (a)(b)(c)、cohort 修正、§9.49.8 扩档、§9.48 口径**仍待拍板**
- 推送状态：§9.65–§9.72 + 819 全部**未提交**；本地 main 与 origin/main
  仍分叉（10/18，4 处冲突），合并需人工裁决，**不要用 rebase**

## 2026-10-03 追加（第八轮）：③ 四项裁决的实施；cohort 分族修正 + 内部流量单一事实源

### 本轮做了什么（以 §9.73 / §9.74 为准，§9.69 仍是自我撤回的样板）

③ 四项裁决已实施三项，第四项被工作区状态堆死。**四项裁决中三项是问卷超时自动采纳**，
只有「静默档 70 → 69」是用户显式回复。

| 裁决 | 状态 |
|---|---|
| cohort 分族修正 | ✅ 完成（4 变异全红 + 1 暴露自指断言，恢复 IDENTICAL，`./bg` 全量绿） |
| 单一「内部」判定函数 | ✅ 完成（5 变异全红，**其中 M3 抓出我自己门的洞**，恢复 IDENTICAL，4 包全绿） |
| §9.49.8 扩档 | ✅ 完成（静默档 70 → **69**，用户显式确认） |
| 21 条 frozen 的 UI 标注 | ❌ **未实施**：`admin` 包被并行会话的进行中 cherry-pick 冲突堆死 |

### ⚠ 工作区状态：有人在跑进行中的 cherry-pick，我方未参与

`.git/CHERRY_PICK_HEAD = 2c611a6d7`（「WIP(保全): 合并前工作区快照」）。
未合并路径 6 个，其中**我方 2 个**：`installer/cmd/llm-gw-installer/main.go`、
`sql/schema/installed_startup_migrations.tsv`。冲突期间冲突数从 8 降到 6，
说明**有人在推进解决**。

**我方已核实的幸存情况**（逐项 grep 确认，不是推测）：
`docs/audit/...`（§9.73 ×18、§9.73.9 ×2、口径句 69 ×1）、
`docs/handoff/...`（3986 行、0 冲突标记、7 轮追加都在）、
`main.go` 的 819 embed 与 TSV 的 819 行、embeddata 源文件、runner.go 均在。

⇒ **下一轮接手前先跑 `git status` 与 `git diff --name-only --diff-filter=U`**。
若冲突已解决，**第一件事是跑 `go test ./admin/ ./cmd/gateway/`**——
`cmd/gateway/dual_read_class_parity_test.go` 是本次 `db` 侧 SQL 分类器重构唯一的
语义核对门，本轮用三道可运行的断言替代了它，但**替代不等于等价**。

### 本轮最值得带走的三条

1. **扩档是「改判」不是「新增」。** 我在选项里写「70 保持不变」是错的：登记表自己的
   约定要求把那一条**挪进**新档，于是 `silently_degraded_content` 23 → 22、三档合计
   70 → 69。是门先抓到的。⇒ 写「某个数字不变」的承诺前，先确认新机制是加法还是改判。
2. **门可以自己有洞，而洞是变异抓出来的，不是读代码看出来的**（本会话第三次：
   §9.71 漏 `else`、§9.72 解析不了就静默跳过、这次「测了函数的性质，没测调用点的语义」）。
   ⇒ 任何「函数 A 满足性质 P」的测试都不能替代「A 在调用链上实际是 Q」的测试。
3. **过期的「例外登记表」比没有登记表更坏。** SSOT 门的 `knownRemaining` 里那条
   `cmd/gateway/dual_read_validator.go` 早已没有字面量，被 stale 检查照出来才删。

### 下一轮提示词

```
在 llm-gateway-go-4 上继续。先做两件事再谈别的：

1) git status + git diff --name-only --diff-filter=U。
   上一轮结束时工作区处于他人进行中的 cherry-pick（CHERRY_PICK_HEAD=2c611a6d7），
   6 路径未合并。若已解决，跑 go test ./admin/ ./cmd/gateway/ —— 尤其
   cmd/gateway/dual_read_class_parity_test.go：那是 db 侧 MirrorDriftClassSQL
   改用 internal/internaltraffic 之后唯一的语义核对门，上一轮因编译失败没跑过。

2) 补做 §9.74 唯一未实施项：21 条 silently_frozen 读点的「接受冻结 + UI 标注数据已停更」。
   口径已由用户裁决（接受冻结、UI 标注），不需再问。§9.48.5 已定顺序：先口径后原语，
   现在口径有了，但本次裁决选的是「标注」而不是造 v1DataHorizon 原语 ——
   开工前先确认是否仍按此口径（用户当时对 ③ 四项的答复有 3 项是超时自动采纳）。

不要重复本轮已完成的事：cohort 分族修正、内部流量 SSOT、扩档三项都已实施且已做变异。
不要 rebase，不要替别人解决 cherry-pick 冲突，不要提交/推送/部署。
```

## 2026-10-03 追加（第九轮）：补上 §9.74.8 的 parity 缺口 + ③-4 的「接受冻结 + UI 标注」

### 上一轮的两件堆塞都已解除

- 冲突从 8 → 6 → 4 → **0**，`admin` / `cmd/gateway` 重新可编译。
  我方 2 个文件（`main.go` 819 embed、TSV 819 行）已被**别人**解决；
  逐项 grep 确认 819 五点同步完整且 embeddata 副本逐字一致。
- ⇒ **原版 integration parity 门已补跑并全绿**（`TestMirrorDriftClassSQL_MatchesIsInternalAutoEntry`
  7 格 + `_NonTerminalArm` 6 格，`TEST_PG_URL` 指本机 PG）。
  §9.74.8 那句「**替代不等于等价**」现在有答案了：替代确实不等于等价，
  但**真正的门也已经跑过**，两处都绿。

⚠ `.git/CHERRY_PICK_HEAD` **仍然存在**（`2c611a6d7`）⇒ 那次 cherry-pick
**尚未 `--continue` / `--abort`**。那是他人的在途操作，本会话**没有代为处理**。

### ③ 四项裁决现已全部实施

| 裁决 | 状态 |
|---|---|
| cohort 分族修正 | ✅（第五轮） |
| 单一「内部」判定函数 | ✅（第五轮） |
| §9.49.8 扩档（静默档 70 → 69） | ✅（用户显式确认） |
| 21 条 frozen 的「接受冻结 + UI 标注」 | ✅ 本轮 |

### 本轮最值得带走的两条

1. **新实现的第一版有洞，而洞是被自己写的用例抓出来的——不是读代码看出来的。**
   连续三轮如此（§9.71 漏 `else`、§9.72 解析不了就静默跳过、本轮两处）：
   - 前端 composable 第一稿用 `res?.v1_data_horizon ?? null`，
     后端省掉该键时状态变成 `live` ⇒ **停写期间页面照常展示过期数字**。
     而这正是它要防的失效形态。测试第 1 次跑就红了。
   - `db` 那道 parity 门第一稿**逐字比较**撞上跨行缩进（测不到语义，只测到排版），
     且「多余字面量」那道检查**方向写反了**（自相矛盾，且真正多余的名字反而查不到）。
   ⇒ **可推广：写完判据先问「这条判据自己第一版会在哪种真实输入上失效」，
   而答案只能从跑出来的失败里拿。**

2. **composable 的测试**不能**替代**「有没有消费者」的测试。
   7 条用例全绿的情况下把 `V1DataFrozenBanner.vue` 整个删掉，测试照样绿
   ⇒ 得到一个「有状态、有测试、无消费者」的 composable。
   ⇒ 必须有一道断言 **App.vue 真的渲染它**（变异 F1 专测这条）。

### ③-4 的诚实边界（别把它读大了）

- **全局横幅，不逐读点接线**：21 个 frozen 读点里只有 **11 个**是 UI 可见的
  （`admin/` 8 + `cmd/gateway/` 2 + `domains/streaming/` 1），
  **10 个是后台 worker / bench**（`bg/` 7 + `internal/quality` 1 +
  `domains/routeincident` 1 + bench 1）——它们不面向用户，UI 标注不是正确概念。
  **本轮没给它们做任何指标/告警侧处理，不假装做了。**
- **写门当前是开着的** ⇒ **今天这条横幅不会显示**。这是正确的，
  但也意味着「横幅永远显示」这一类错误在灰度前完全看不出来
  ⇒ 门 G1 必须存在。
- `failed` 态（取不到告示）**也显示横幅**，且不说「已停更」——
  **取不到 ≠ 未停更**。这是整套设计里最要紧的一格。
- 横幅**只在应用启动时拉一次**，没有轮询/推送；也没有「停更了多久」的时间戳
  （写门可被重开，存时间戳会留下过期告示）。

### 下一轮提示词

```
在 llm-gateway-go-4 上继续。先跑两条基线，确认没有回退：

1) go build ./... && go test ./admin/ ./db/ ./internal/internaltraffic/ ./bg/ -count=1
2) cd web && npx vitest run src/components/shell/V1DataFrozenBanner.test.ts \
     src/composables/useV1DataHorizon.test.ts && node scripts/i18n-audit.mjs
   （基线：typecheck 有 19 处**既有**错误，与本轮改动无关；
     i18n 审计必须仍是 0 missing —— 8 个 locale 的 app.ts 都被加了 6 个 key。）

然后考虑这三件，都还没做：
① .git/CHERRY_PICK_HEAD 仍在（2c611a6d7）—— **问用户**那次 cherry-pick 该
   --continue 还是 --abort，不要自己动。
② §9.65.8 的三条判据取舍（失败登记保留期 / 计数器跨重启基线 /
   backlog 淘汰落盘还是取消 backlog）—— 需要用户裁决。
③ 819 是否部署到 252 —— 外部动作，需显式授权。

不要重复已完成的事：③ 四项裁决（cohort 分族修正 / 内部流量 SSOT / 扩档 /
UI 标注）都已实施且已做变异，记录在 §9.73–§9.75。
不要 rebase，不要提交/推送。
```

## 2026-10-03 追加（第十轮）：★订正 §9.73 —— 我把 cohort 量在了错误的行源上

### 最重要的一件事：§9.73 的绝对数字要按 §9.76 重读

**我的 §9.73.2 / §9.73.4 测量查的是 `request_logs`（父表），
而生产 worker `settleBaselinesSQL` 读的是 `request_logs_hot`。
实测这两个行源不是包含关系**（`hot_only = 4,613 = hot_total`）：

| 行源 | auto 行 | `is_internal` |
|---|---:|---:|
| `request_logs`（父表） | 24,466 | 3,200 |
| `request_logs_hot` | 4,613 | — |
| 两者并集 | 29,026 | 4,047 |

**结论没变，绝对数字与成分都变了：**

| | 父表口径（我先前报的） | 生产口径（hot，24h） |
|---|---:|---:|
| 修正前 cohort | 20,355 | **3,871** |
| 其中 `probe_triggered` | 20,340（99.93%） | **3,849（99.45%）** |
| 修正后 cohort | 15（`code 10/chat 3/long_context 2`） | **22（`chat 19/code 3`）** |

⇒ §9.73.4 的判断（**探针占 cohort 约 99%，服务 0 条结算**）**仍然成立**；
被订正的是数字。**§9.74 那次修正（加探针谓词）在两个口径下都成立。**

### 顺带闭合了一个卡了几轮的「未查明」

§9.73.3 记的「3,884 vs 3,200 差异来源未查明」——
**答案不是判定逻辑，是行源 + 时钟**。而且那张表**一小时 +911 行**
（23,555 @01:22 → 24,466 @02:21）。

⚠ **有界结论，不是证明**：旧那个 3,884 的**原始查询没有留下来**，
所以不能说「它就是某个口径的重算结果」。

⇒ **教训**：数字对不上时，先怀疑**量具**（口径/时刻/行源），再怀疑被测对象。
后者更「有意思」，所以更容易被先想到。

### 两条量不出来的路径（记下来，别重试）

| 目标 | 结果 |
|---|---|
| 视图 `request_logs_with_current_month` 的 auto 总体 | **statement timeout**（30s 也跑不动，视图 150+ 列带 join） |
| `session_turns` 侧的 auto 行 / actor 命中 | **statement timeout** |

⇒ 答案**存在**，只是本轮查询形状跑不动。要闭合需先看执行计划 / 补索引，
**不是**把查询放宽一点重试。

### 本轮没改代码

订正落在**文档**上。原因是 cohort 那两道门按**迁移文件**验列存在性、
**不查库** ⇒ 口径变了不影响它们（已复验 `go test ./admin/` 绿）。
**这正是「把结论钉在可执行判据上」的回报**：没做那些门的话，
这次订正会直接推翻已写下的数字。

### 下一轮提示词

```
在 llm-gateway-go-4 上继续。三件都还没做，都需要人：

① .git/CHERRY_PICK_HEAD 仍在（2c611a6d7），未合并路径已清 0。
   → 问用户：--continue 还是 --abort。**不要自己动。**
② §9.65.8 三条判据取舍（失败登记保留期 / 计数器跨不住重启 /
   backlog 淘汰落盘还是取消 backlog）→ 需用户裁决。
③ 819 是否部署到 252 → 外部动作，需显式授权。

可做但不需裁决的一件（如果上一轮没做）：
§9.76.7 说 §9.54/§9.55/§9.58 的数字**也是查父表量的**，需要按生产行源复核。
注意视图与 session_turns 两条路径在 252 上 statement timeout
（见上一轮「量不出来的路径」），别原样重试。

基线：go build ./... && go test ./admin/ ./db/ ./internal/internaltraffic/ ./bg/ -count=1 全绿；
web 前端 14 用例绿、i18n 审计 0 missing、typecheck 19 处**既有**错误。
③ 四项裁决（cohort 分族 / 内部流量 SSOT / 扩档 / UI 标注）已全部实施，记录在 §9.73–§9.76。
不要 rebase，不要提交/推送。
```

## 2026-10-03 追加（第十一轮）：★★「24h 基线」其实只有约 8h、缺 65.3% 的数据

### 这条比 §9.76 更要紧

`request_logs_hot` 是**暂存表**（**全部**写入方的 INSERT 目标），
后台 promoter 每 **8 小时**把超龄行**移动**（DELETE 源行 + INSERT 分区父表）
到 `request_logs` ⇒ **两表按设计不相交**，并集才是完整面。

而 `settleBaselinesSQL` 窗口是 `baselineWindow = 24h`、数据源只有 hot：

| 24h 窗口读法 | cohort 行数 | 占完整总体 |
|---|---:|---:|
| 完整总体（`hot ∪ parent`） | **13,452** | 100% |
| 只读 `request_logs_hot`（现状） | **4,665** | **34.7%** |

⇒ **缺 65.3%。** 它问 24 小时的 p95/p75，表里最多只有 8 小时；
且那 8 小时是**非随机切片**，基线会随 promoter 排空节奏漂移，
**漂移原因与数据无关**。§9.44 埋的 `cohort_rows` gauge **不会报红**（它只报为 0）。

### 我先想到的答案又是错的（记下来）

hot 里有 4,457 条「10-02 的行」，看着像 promoter 停摆。实测：
超过 8 小时的行只有 **41** 条、集中在 10-02 18:19–18:25 批次边界；
小时分布显示 hot 恰好持有 10-02 18:00 → 10-03 02:00 = **最近 ~8 小时**。
⇒ **promoter 正常。** 那个时点那些行只有 2.5–8.5 小时大。
**「日期看起来很旧」不等于「超过保留期」，必须按 `NOW() - interval` 算。**

### §9.74 那次修法**不因此失效，但不充分**

- 探针排除谓词：仍然正确且必要（探针污染是真实的 99%）。
- 但它只解决「**总体选错**」，没解决「**总体被欠采样**」。两条是独立缺陷。
- 登记表 `bg/auto_route_settle_sql.go` 那一档 `silently_degraded_aggregate`
  正好装得下这条（**不需要改档位**，已把证据补进 Note）。

### 修法候选 —— **未实施，需要裁决**（§9.77.5）

| 候选 | 代价 |
|---|---|
| v1 侧改读 `hot ∪ parent` 或 710 视图 | 视图 150+ 列本机实测 **statement timeout**；且改基线口径 = reward 行为变更 |
| `baselineWindow` 24h → ≤8h | 名副其实但样本更小；等于改掉已声明的 24h 语义 |
| **保留现状 + 加「cohort 覆盖率」判据**（`hot ∪ parent` vs `hot`） | 不改行为，先让它**变可见** |
| 接受欠采样 + 文档标注 | 零风险，基线仍偏 |

我倾向第三项，但它需要新增周期性查询，且「分母用哪张表」本身也是口径决策
⇒ **不代为裁决**。

### 一个连带缺口

登记表 106 条里 `Evidence` 写 `FROM request_logs_hot` 的那些，
**只要窗口 > 8h 就同样欠采样**。但 Evidence 只记表名、**不记窗口**，
所以「哪些读点窗口超过 8h」**目前无人能答**。
⇒ 登记表的 Evidence 维度**缺「时间窗」一维**。**如实记账，本轮未逐条统计。**

### 下一轮提示词

```
在 llm-gateway-go-4 上继续。四件都需人，不要自己决定：

① .git/CHERRY_PICK_HEAD 仍在（2c611a6d7），未合并路径已清 0 → 问用户 continue 还是 abort。
② §9.77.5 的四条修法候选 → 需用户裁决（我倾向第三条：先让它变可见）。
③ §9.65.8 三条判据取舍 → 需用户裁决。
④ 819 是否部署 252 → 外部动作，需显式授权。

不需裁决、可直接做的一件（如果要做）：
登记表的 Evidence 维度缺「时间窗」——可以加一列「窗口」并把已知读点补上，
再用一条判据钉住「窗口 > 8h 且数据源是 request_logs_hot 的读点必须显式声明处理方式」。
⚠ 注意：视图与 session_turns 两条路径在 252 上 statement timeout，别原样重试。

基线：go build ./... 通过；go test ./admin/ ./db/ ./internal/internaltraffic/ ./bg/ -count=1 全绿；
web 前端 14 用例绿、i18n 审计 0 missing、typecheck 19 处**既有**错误。
③ 四项裁决已全部实施（§9.73–§9.75）；行源/欠采样两条发现见 §9.76 / §9.77。
不要 rebase，不要提交/推送。
```

## 2026-10-03 追加（第十二轮）：★★§9.77 不是新问题 —— R44/R45 的「读面必须 hot∪母表」纪律仍在，settle 读点违反它

### 定位纠正：这是既有纪律的漏网实例，不是我发现的新问题

我上一轮把「24h 基线只有 8h」写成新发现，**定位错了**：

| 项 | 出处 |
|---|---|
| 纪律 | R44 / R45：「读面必须 hot∪母表」 |
| 执行 ① | **R46 F3**（`docs/audit/2026-09-19-r46-48h-audit-round.md:16`）：3 处 admin 读面全部切 `request_logs_with_current_month` |
| 执行 ② | **R46 F4**（同文 `:17`）：affinity worker 盲窗 → 裁决 **NOT EXISTS 双探测（hot∪母表）**，实测 110ms @14d；字面 JOIN 视图 6.98s **否决** |
| 已钉进代码 | `bg/auto_route_affinity_worker.go:263-276` 注释原文：「hot and parent are mutually exclusive (**promote is DELETE+INSERT**)」「retention-independent」 |

⇒ **「hot 与 parent 互斥」这句话在代码注释里躺了 4 天**，
今天只是把它套到了一个**没被修**的读点上。

### 纪律是多数派实践

非测试文件里读并集视图 vs 裸读 hot：`admin` **50 : 19**、`bg` 10 : 9、
`domains` 6 : 7、`cmd/gateway` 1 : 3、`internal` 2 : 3。
⇒ `settleBaselinesSQL` 是 41 个 hot-only 文件之一，且是**窗口最长的一批**（24h）。

### §9.77.5 的选项因此有了先例背书

「改读 hot ∪ parent」有先例（R46 F3）；「NOT EXISTS 双探测」也有先例（R46 F4）。
**不需要重新发明**。最自然是抄 F3，但 settle 窗口 24h > F3 那三处，
**视图在该窗口下的成本要重测**（F4 的 6.98s 是它自己的查询形状，不能套）。
⇒ **仍不代为裁决**（改基线口径 = 改 reward）。

### ★★我为这件事写的扫描器不可靠，已弃用

我写了个静态扫描输出「24 个登记项里 7 个超 8h」。**这个数不能用**：

- `bg/model_tier.go:165` 的 `windowHours` 默认 **72**（3 天），
  传给 `now() - make_interval(hours => $1)` ⇒ 我的扫描只认 `INTERVAL '…'` 字面量，
  **把它判成 ≤8h**（假阴性）；
- 扫描窗口（回退 60 / 前瞻 25 行）是我拍的，同文件另一条查询的 interval 会被算进来；
- 第一版整文件扫，把 `14 * 24 * time.Hour` 挂到了错误的聚合上。

⇒ **与 §9.72.4 的 M4 同族：解析不出某种写法 ⇒ 静默跳过 ⇒ 输出「没问题」。**
我说「7 个」时输出的其实是「我看得见的 7 个」，**当总数就是伪造测量**。

⇒ **因此本轮不建「hot-only 读点登记表」**：它要由**逐文件读**产出，
而我只有一台不可靠的扫描器。**用不可靠的量具建的登记表比没有更坏。**

### 下一轮提示词

```
在 llm-gateway-go-4 上继续。五件都需人，不要自己决定：

① .git/CHERRY_PICK_HEAD 仍在（2c611a6d7），未合并路径已清 0 → 问用户 continue 还是 abort。
② §9.77.5 / §9.78.3 的数据源修法 → 需用户裁决。**先读 §9.78 再决定**：
   R46 已经用实测回答了「怎么改」（F3 切视图 / F4 NOT EXISTS 双探测 110ms），
   我们不需要重新发明；真正缺的只是「24h 窗口下视图的成本」这一个数。
③ §9.65.8 三条判据取舍 → 需用户裁决。
④ 819 是否部署 252 → 外部动作，需显式授权。
⑤ 要建「hot-only 读点登记表」的话，必须**逐文件读**那 41 个读点的窗口。
   ⚠ 不要用正则扫描器 —— 上一轮的扫描器在参数化窗口上假阴性（model_tier.go 72h 判成 ≤8h），
   且它「解析不出就静默说没问题」。

基线：go build ./... 通过；go test ./admin/ ./db/ ./internal/internaltraffic/ ./bg/ -count=1 全绿；
web 前端 14 用例绿、i18n 审计 0 missing、typecheck 19 处**既有**错误。
③ 四项裁决已全部实施（§9.73–§9.75）；行源/暂存面两条发现见 §9.76 / §9.77 / §9.78。
不要 rebase，不要提交/推送。
```

---

## 第十三轮（2026-10-03 02:35–03:10 CST）—— §9.79：settle 的欠采样严重性被我高估了 1300 倍

### 做了什么

1. **§9.78 遗留的 44 个 hot-only 读点逐文件提取完成**（第一批 22 个在前一轮，
   第二批 22 个本轮）⇒ §9.79.7 有完整表格，**窗口 > 8h 的读点不再无人能答**。
2. **查同族是否已修** ⇒ 找到**三处已修、一处没修**（§9.79.2）。
3. **实测 252 生产配置**：`lifecycle.hot_retention_hours = 8`，`prev_value IS NULL`
   （从未改过），`updated_at = 2026-08-18 02:52:32+08`。
4. **实测 4 种读法的 cohort 规模与成本**（24h 窗，代码同源谓词）⇒ **§9.78 的 blocker 关闭**。
5. ★★ **发现并纠正自己的错误结论**：「cohort 缺 65.3%」是**总体**的欠采样率，
   cohort 实际只缺 1 行（4.5%）。

### ★★★ 本轮最重要的三个数字（252，采样时刻 2026-10-03 02:35:42+08）

| 族 | 形态 | chat | code | 合计 | 耗时 |
|---|---|---:|---:|---:|---:|
| v1 | **现状**（裸读 `request_logs_hot`） | 19 | 3 | **22** | 0.515s |
| v1 | 双探测 `UNION ALL` | 20 | 3 | **23** | **0.434s** |
| 会话 | **现状**（裸读 `session_turns_hot`） | 19 | 3 | **22** | 1.103s |
| 会话 | 双探测 `UNION ALL` | 20 | 3 | **23** | **4.601s** |

⚠ **库在被写**（同口径父表 auto 一小时 +911 行），引用任何数字都要带时刻。

### ★★ 自我纠正（这一条最重要）

**我说错了「危害的形状」。**

- ❌ 我在 §9.77 说：「settle 的 24h cohort 缺 65.3%」——
  那是**含探针的全量 auto 总体**的欠采样率。
  cohort 先过探针排除谓词，被削到 22 行；**真实业务 auto 极低频且集中在最近**，
  几乎全在 hot 里 ⇒ **cohort 实际只缺 1 行**。
- ✅ **真实形状**：23 行里 **20 行（87%）挤在 01:00 一个小时**。
  ⇒ 8h 窗捕到哪几个小时**完全取决于 settle worker 的运行时刻**：
  02:35 跑捕获 91%；10:35 跑 01:00 的峰在窗外 ⇒ **近乎为空**；15:00 跑只剩 1 行。
- ⇒ 危害不是「基线稳定偏小 65%」，而是「**基线在一天内可能从 23 行掉到 0 行，且无信号**」。
- ⇒ 空 cohort 的后果 = §9.73.9 那条 panic 注释描述的后果
  （延迟项与成本项同时塌成中性 0.5，空 map 不是 error）
  —— 那是**第二个触发路径**（第一个是「谓词回落到空」，这条是「时间窗截断到空」）。
- ⇒ **量级对比**：§9.74 修的「总体选错」是 **1,360 → 23**（1300 倍效果），
  §9.77 要修的「数据面扩容」是 **22 → 23**（1 行）。**不要把它们当同量级的事。**

### ★★ 同族四处：三处已修，一处没修

| # | 位置 | 修法 | 轮次 |
|---|---|---|---|
| 1 | `bg/ledger_reconciliation.go:217` `effectiveWindow()` | clamp 窗口到 hot 保留期 | **R56**（`dccf93e51`，2026-09-23 05:49:43） |
| 2 | `bg/partition_manager.go:1749` `autoRouteMinRetention = 5h` | clamp 保留期下限 + `slog.Warn` | 未标轮次 |
| 3 | `bg/auto_route_affinity_worker.go:295-304` | NOT EXISTS 双探测（hot ∪ parent） | **R46 F4** |
| 4 | **`bg/auto_route_settle_sql.go:78` `settleBaselinesSQL`** | **无任何保护** | **本轮发现** |

**R56 的 commit body 逐字写着**「原 24h 默认只扫 _hot 表（**8h 即排干**）」——
2026-09-23 别人已经写下这五个字。我用 252 实测独立得出同一结论。

**为什么 R56/R46 的修法不能直接套到 settle**：
- R46 F4 面对的是 14d 窗 ⇒ clamp 会让功能作废，所以走「双探测」；
- R56 面对的是对账差异 ⇒ 窗口缩短不改变语义，走「clamp」；
- **settle 的窗口就是 baseline 定义本身**（`baselineWindow = 24h` 是 reward 的一部分）
  ⇒ **clamp = 改 reward，扩容才是不改语义的那个。**

**第三个自认实例**：`domains/streaming/model_alternatives.go:240-245`
7 天窗读 hot，注释明写「reflects the hot retention window (~8h of traffic) rather than
a full 7 days」，且 2026-09-25 实测过替代方案（视图 7d 窗 **151s**，导致功能从未返回）
⇒ **四个同族实例里唯一把代价写在脸上的**。

### ★★ 我本轮连着两次写了「自己的一份判据」，两次都没报错

| 轮次 | 我写的 | 正确 | 结果 |
|---|---|---|---|
| 1 | 漏生成器 actor 排除臂 | `settleBaselinesSQL:82` 还要 `AND autoroute.SQLExcludeSyntheticActors("rl")` | 报 **1,360 行**（正确 23）；1,337 是三个生成器 actor |
| 2 | 连接键 `p.id = rl.id` | **`request_id`**（hot 的 pkey） | **0.801s vs 0.434s**（慢 1.8 倍），`id` 上无可用索引 |

⚠⚠ **两个错数字都「看起来合理」，没有任何报错**。第 1 个甚至自洽
（1,360 = 1,337 空 task_type + 20 chat + 3 code，像一张完整的分族表）。
⇒ **审计里手写 SQL 谓词 = 新建一个无漂移护栏的真相源**，
只能靠「与生产读点逐字比对」发现，**不能靠「数字看着合理」排除**。
本轮 4 组数的谓词与连接键全部取自
`bg/probe_policy.go` / `internal/internaltraffic` / `pg_indexes`，不是手写近似。

### 连带：§9.75 横幅的两个问题

1. **§9.75 的记录写错了行源**：我说「`settings_kv` 的
   `storage.request_logs_write_enabled` 当前 = true」——**假**。
   表只有 41 行，**没有这个键**。真实机制是
   `GetPlatformBool(key, true)` 键缺失 ⇒ **回落 fallback `true`**。
   ⇒ 「横幅今天不显示」结论仍成立，但依据是回落值。
2. **★ 设计缺口**：`getPlatformBool`（`settings/helpers.go:83-101`）有
   **三个回落点**（`Global==nil` / `Spec==nil` / `len(raw)==0`），
   **三个全部返回 fallback（=true）**；
   而 `EffectiveValue`（`settings/spec.go:284`）**本来就返回 `source ∈ {db,env,default}`**，
   `getPlatformBool:91` 用 `raw, _, err :=` **把 source 丢掉了**。
   ⇒ 横幅**无法区分**「DB 里 true」与「DB 没这个键 / 读点出错 ⇒ 回落 true」。
   ⚠ 对写入方 fail-open 方向是对的；对**告示**方向是反的（把「不知道」显示成「知道」）。
   **修法**：告示端点额外读一次 `source`，`default` 时产出 `unavailable`
   （前端已有 `failed` 展示路径）。**不碰写入方读点**。
   ⚠ **改 API 响应形状 ⇒ 列进待裁决，未实施。**

### 下一轮提示词

```
在 llm-gateway-go-4 上继续。六件都需人，不要自己决定：

① .git/CHERRY_PICK_HEAD 仍在（2c611a6d7），未合并路径已清 0 → 问用户 continue 还是 abort。
② settle 数据源修法 → 需用户裁决。**先读 §9.79.2–§9.79.5 再决定**：
   ⚠⚠ **不要**再用「cohort 缺 65.3%」论证必须改 —— 那是总体口径，cohort 实际只缺 1 行（22→23）。
   真正的论证是 §9.79.5 的**时刻依赖**（23 行里 20 行挤在 01:00，8h 窗随 worker 运行时刻
   在 0–23 行之间波动，且无信号）。成本已实测：v1 0.434s / 会话 4.601s，后台 job 可接受。
   ⚠ clamp 路线（#1/#2 的先例）**对 settle = 改 reward**（baselineWindow 是 reward 定义的一部分）。
   ⚠ **两个族必须一起改**（用户纪律；实测两族数字完全一致 = 23）。
   ⚠ 视图形态（710）**别试**：24h 窗 statement timeout，7d 窗已知 151s。
③ §9.65.8 三条判据取舍 → 需用户裁决。
④ 819 是否部署 252 → 外部动作，需显式授权。
⑤ §9.79.8(2) 横幅 `unavailable` 态 → 需用户裁决（改 API 响应形状）。
⑥ §9.79.7 那 9 个「无时间谓词」的 admin 文件窗口未判定 → 需连调用方一起读才能定。

**两条方法论纪律（这一轮各犯了一次，都没报错）**：
  - 审计里**不要手写 SQL 谓词**，取自 bg/probe_policy.go / internal/internaltraffic。
    漏一条臂 ⇒ cohort 从 23 变 1,360；错连接键 ⇒ 慢 1.8 倍。**两个错值都看起来合理。**
  - 引用计数时写明**总体 vs 子总体**：§9.76 的「量的是 A 表、读的是 B 表」有第三种形态 ——
    「量对了总体、推错了子总体」。总体欠采样率不能推给被强过滤过的子总体。

基线（未变，本轮只改文档）：go build ./... 通过；
go test ./admin/ ./db/ ./internal/internaltraffic/ ./bg/ -count=1 全绿；
web 前端 14 用例绿、i18n 0 missing、typecheck 19 处**既有**错误。
审计文档 10,300 行（U+FFFD 仍只有既有的 4471/6674 两行，无新增）；
本 handoff 无 U+FFFD、无冲突标记。
⚠ HEAD 在 2026-10-03 被他人推进到 cb16ee00a（cherry-pick 未终结）。
不要 rebase，不要提交/推送/部署，不要代为处理他人的 cherry-pick。
```

---

## 第十四轮（2026-10-03 02:49–03:15 CST）—— §9.80：Task A 撤案、Task B 实施、以及一个三次打不死的变异

### ⚠ 本轮的问卷是**超时自动采纳**，不是用户显式回复

四项推荐全部被自动采纳（`responseSource: automatic_timeout`）：

| 项 | 采纳结果 | 本轮实际处置 |
|---|---|---|
| CHERRY_PICK_HEAD | 「我自己处理」 | **没动** |
| settle 扩数据面 | 采纳 | **撤案**（§9.80.0，收益被自己的数据归零） |
| 横幅加 source | 采纳 | **已实施**（§9.80.1） |
| 819 部署 | 暂不部署 | 遵守 |

⚠ 下一轮若要引用这些决策，**必须标注「未获显式确认」**。

### ★ Task A 撤案：批准的动作被自己的数据推翻

`settleBaselinesSQL` 扩数据面（UNION ALL 双探测）的**收益是 1 行**：

| 窗口 | cohort 行数 |
|---|---:|
| 8h | 22 |
| 12h | 22 |
| **24h** | **23** |
| 48h | 34 |

⚠ **`code` 档完全不受影响**（`code` 那 3 行全在冷面，8h 窗里 01:00 那 20 行全是 `chat`）。
⇒ 收益 1 行 vs 成本 0.434s（v1）/ 4.601s（会话），**不做**。
⇒ 对比 §9.74「加探针排除谓词」= **1,360 → 23**：**量级差 1300 倍**。

**建议改为**：加「cohort 样本量过小」告警（`code` 档 p95 = **110,019 ms** 由 **3 行**支撑；
现有 gauge 只报 `= 0`，看不见「= 3」）。

### ★★ 本轮最大的新发现：`settlePendingSQL` 的 LATERAL 欠采样 47%

> ⚠⚠ **§9.80.8 已推翻这条：47% 是我的口径错误，LATERAL 实际缺 0 行。**
> 保留原文是为了让「错在哪」可查。

`bg/auto_route_settle_sql.go:121-123` 聚合**同会话全部行且无窗口**：

| | hot | 完整面 | 欠采样 |
|---|---:|---:|---:|
| 行数 | 1,532 | **2,889** | **46.96%（缺 1,357）** |
| 会话数 | 1,414 | **2,754** | 48.6% |

⇒ ~~**`retry_count` 系统性偏小 47%，而它进 reward。**~~
⇒ **已量化未修**（超出「基线数据面」的授权范围）⇒ **本轮留下的最大未处理项。**

**★ §9.80.8 订正（03:15 后）**：LATERAL 的锚点 `s` 来自
`auto_route_selections_hot` 的未结算行 ⇒ **必然在 hot 里**，
而我量的是「24h 窗内所有有 auto 行的会话」，**含整个会话都在冷面的那批**
——那批行物理上不可能被 hot 里的 selection 锚定。

**按正确口径重测**：

| 指标 | 值 |
|---|---:|
| 会话数（hot 里有行的） | 2,567 |
| hot 行数 | 3,021 |
| **完整面行数** | **3,021** |
| **缺失** | **0 行 / 0.00%** |

⇒ **LATERAL 不欠采样，撤回。**

**附带一条更有用的实测**（完整面会话跨度）：**14,234 / 14,253 = 99.85% 跨度 ≤1h**，
跨 8h 的只有 6 个 ⇒ `settleAbandonAfter = 4h` 那层保护有约 4 倍余量。

⚠ **教训**（本轮第三次同族错误）：**量「某个子查询会漏多少行」之前，
先确认那个子查询的锚点在哪个面**。我量的是「冷面有多少行」，
被问的却是「从 hot 出发能漏掉它们吗」——**差集不是同一批行**，
而我直接拿差集当了答案。

### ★ 另一条：1,258 行未结算 selection 是**历史遗留**，但隐患仍在

`auto_route_selections` 父表 1,258 行 `settled_at IS NULL`（5.56%），
驱动表只读 `auto_route_selections_hot`。

⚠ **已证伪「正在发生」**：按天分布 = **09-07: 678、09-08: 580**，09-09 之后**每天 0**
⇒ 与 §9.68 记的 154 崩溃循环同期。

⚠ **但隐患是真的**：`auto_route_settle_worker.go:53-59` 承诺
「每条 selection 必须在 promote 前到达终态」，而
`partition_manager.go:1749` 把 `auto_route_selections_hot` 保留期 clamp 到 **5h**，
`settleAbandonAfter = 4h` + `DefaultPromoteInterval = 1h`
⇒ **余量恰好等于一个 promote 周期**（余下 1h 什么都不剩）。

### ★ Task B 已实施：横幅第三态

`GetPlatformBool` 三个回落点**全部**返回 fallback(=true)
⇒「读到 true」既可能是 DB 真值，也可能是**根本没读到**。
`EffectiveValue` 的 `source` 被 `getPlatformBool:91` 丢掉 ⇒ 现在用上了。

- 后端：`V1GateState` 三态 + `v1GateState` 纯函数 + `readV1GateSource` + `Unknown` 字段；
  `v1FreezeNoticeFor` 收**变参** `source ...string`（不破坏既有 `(bool)` 调用点）。
- **写入方读点 `settings.RequestLogsWriteEnabled()` 一个字未改。**
- 前端：`V1HorizonState` 加 `'unconfirmed'`；banner 的 `unconfirmed` 与 `failed` 分开，
  两者都**不**展示 frozen 态那两句后端文案。
- i18n：8 locale 各加 `titleUnconfirmed`。

⚠ **上线后生产会常显「停写开关未显式配置」横幅**（键不在 `settings_kv` 的 41 行里）。
要回到 `live` 需 seed `storage.request_logs_write_enabled = true`（**生产写，需授权**）。

### ★★★ 本轮最可复用的教训：**变异打不死 ≠ 判据不够强**

`if (n.unknown)` 分支**连续三次删掉都全绿**：

| 轮次 | 动作 | 结果 |
|---|---|---|
| 1 | 删判据 | 12 用例全绿 |
| 2 | 补 2 条专打它的用例 | **仍全绿**（补的断言走同一条路径 = 把巧合正确钉成契约） |
| 3 | 给 unknown 独立状态 `'unconfirmed'` | **F4 红 3 条 / F5 红 1 条** |

**真因**：后端契约下 `unknown` 态的 `frozen` 恒为 `false`
⇒ `n.frozen ? 'frozen' : 'failed'` 与「先判 unknown 再判 frozen」**行为等价**
⇒ 那个分支是**冗余的**，删掉零后果。
⚠ **解法不是补断言，是造一个兜底无法产生的对外值。**

⚠ 且**每次都 diff 确认了变异落盘**（三次都 diff）——否则第 1 次就会把结论
误判成「变异没生效」而放过它。

### ★ 两次「同族只改一半」当天被抓

1. banner 测试 fixture 漏 `unknown` 字段 ⇒ `typecheck` 20 vs 基线 19，当场红；
2. **端点契约门在全量跑才红**（单跑那批 `TestV1*` 时它正好绿）——
   它的前提「`GetPlatformBool` 默认 true ⇒ live」在本环境下是**假的**，
   因为键不在 `settings_kv` ⇒ source=default。
   ⚠ **讽刺但真实：新门抓到了旧门**，而旧门正是 §9.75 那条纪律（区分行源）的受害者。
   ⇒ 修法不是放宽，改为断言**四段三态契约**。

### ★ 收口：5h clamp 真实生效，promote 正常（§9.80.9）

`auto_route_selections_hot`「5h vs abandon 4h ⇒ 余量一个 promote 周期」是**读代码**的结论，
252 实测收口：**clamp 生效，promote 正常，隐患未发生**。

⚠ 中途一个像缺陷的现象：`pg_proc` 里
`promote_auto_route_selections_hot_to_partition` 的默认参数是 **`'08:00:00'`**，
与 Go 侧 5h 不一致。⇒ 查 `partition_manager.go:1508` 是
`SELECT fn($1::interval, $2::int), retention, batchSize` ⇒ **显式传值**，
函数默认值永不使用。
⚠ **「读数据库元数据推运行时行为」的陷阱**：`pg_get_function_arguments`
给的是**声明时的默认值**，不代表调用方传了什么 ⇒ 判行为要读**调用点**。

| 实测项 | 值 |
|---|---|
| hot 最老行 | **2.83 小时**（远小于 5h ⇒ 未到阈值） |
| hot 里 >5h 的行 | **0** |
| 2h–8h 区间 hot / 父表 | **4 / 0**（全在 hot ⇒ 正常） |
| 父表最新 ts | 10-01 15:00（36h 前，与「5h 阈值 + 流量极低」自洽） |

⚠ 父表 12h 内 0 行**一度**像「那段数据在两张表里都消失了」，
实际是**根本没有那个时段的流量** ⇒ 「查不到」与「不存在」两种成因必须先区分
（与 §9.80.8 同族，本轮第四个实例）。

⇒ **`settleAbandonAfter = 4h` 保护链完整**：2min 可结算 → 最迟 4h 盖终态 → 5h 被搬走，
余量 1 小时；而 99.85% 的会话 1 小时内结束（§9.80.8）⇒ 充足。
⚠ 09-07/08 的 1,258 行孤儿是**崩溃循环**导致（进程没跑），**不是**余量不足。

### 下一轮提示词

```
在 llm-gateway-go-4 上继续。**先读 §9.80 再决定做什么**——本轮撤案了两件已批准的事。

① 【需人工】.git/CHERRY_PICK_HEAD 仍在（2c611a6d7）——用户已选「我自己处理」，别动。
② 【建议】加「cohort 样本量过小」告警：code 档 p95=110,019ms 由 **3 行**支撑，
   24h 也只有 23 行 ⇒ 换数据面救不了。现有 gauge 只报 =0。
③ 【需授权】是否 seed settings_kv 的 storage.request_logs_write_enabled = true
   —— 不 seed 的话，Task B 上线后生产会常显「无法确认」横幅（正确行为，但是可见变化）。
④ 【需裁决】§9.65.8 三条判据取舍；819 部署（本轮「暂不部署」）。
⑤ 【已收口，不需再查】settle 的两条「疑似缺陷」都已证明不是缺陷：
   扩数据面只值 1 行（§9.80.0）、LATERAL 缺 0 行（§9.80.8）、
   5h clamp 生效且 promote 正常（§9.80.9）。


⚠⚠ **§9.80 撤案教训：不要用「cohort 缺 65.3%」论证任何修法**——
   那是总体口径；cohort 实际只缺 1 行，且 **8h 与 24h 只差 1 行**。
   **先扫窗口自变量（8/12/24/48h），确认结论不随窗口翻转，再谈修法。**

⚠⚠⚠ **本轮三条新纪律（都是我当天犯的）**：
   - 变异打不死，先问「**这个分支删掉后有没有可观察后果**」——
     没有就是状态机压掉了区别，补断言只会把巧合正确钉得更牢。
   - 加响应字段时，**它的契约门要同批更新**，否则只在全量跑里红。
   - ★ **量「某个子查询会漏多少行」之前，先确认那个子查询的锚点在哪个面**。
     我量了「冷面有多少行」却回答了「从 hot 出发能漏掉吗」，差集不是同一批行。
     ⚠ 三条错误**都产出了看起来合理的具体数字**（47%、91%、1,360）。

基线（2026-10-03 03:15 复验）：go build ./... rc=0；
go test ./admin/ ./db/ ./internal/internaltraffic/ ./bg/ ./settings/ -count=1 全绿；
web 前端 158 文件 / 1141 用例全绿；i18n 审计 0 missing；
typecheck 19 处**既有**错误（本轮一度 20，已修）。
审计文档 10,551 行（U+FFFD 仍只有既有的 4471/6674，冲突 0）。
⚠ 本会话全部改动**未提交、未推送、未部署**。
不要 rebase，不要代为处理他人的 cherry-pick。
```

---

## 第十五轮（2026-10-03 18:40–19:00 CST）—— 交付前批判式审计：抓到 2 个真缺陷，819 已被别人提交

### ⚠ 工作区状态已被别人改过（先核实，否则后面全是空话）

| 项 | 值 |
|---|---|
| `.git/CHERRY_PICK_HEAD` | **已消失**（那个悬了 15 小时的操作被别人 `--continue/--abort` 了） |
| `HEAD` | `b9365c215`（15:10，被人从我工作的 `cb16ee00a` 推进） |
| 与 `origin/main` | **落后 99 个提交** |
| 并发进程 | 无 `deploy-seamless` / `go build` |

⇒ **我 3 小时前的全部测试结果作废，本轮全部重跑。**

★ **819 已经不在我的脏文件里**：`git log` 显示它由
`54a63d0e5 feat(819): request_abandoned 落点 —— 「开始了却没终态」的独立记账(审计 §9.66/§9.67)`
进入 `main`（就是那个 cherry-pick 的内容）。
⇒ **我这次提交不含 819**；剩余 29 个文件是 §9.74/§9.75 那一批。

### ★★ 审计抓到 2 个真缺陷（都在我自己的改动里）

**缺陷一（会静默说谎）**：`v1FreezeNoticeFor(bool, source ...string)` 的缺省
我第一稿写成 `"db"`（= live），理由是「不破坏既有单参调用点」。
**那个理由恰好重新引入了 §9.79.8 要治的病**：
将来任何人加一个单参调用点，会**静默落到 live**——「以为知道」而实际没读。
⇒ **一个为消除歧义而加的变参，自己成了新的歧义源。**
修法：缺省改成 `V1GateSourceDefault`（= unknown，**读不到就说读不到**）。
⚠ 改完之后两条既有门**立刻变红**——**这是门有效的证据，不是回归**
（它们测「明确知道」的两态，却按单参调用）⇒ 改为显式传 `"db"`。

**缺陷二（静默腐烂）**：`firstLinesAround` 在我改掉唯一调用点后成为孤儿函数，
而 **Go 不报未使用函数** ⇒ 没有任何门会响。已删，并在原位留记录。

### ★ 补一道**端到端**门（此前只有手写 fixture 的单测）

前端判据是 `if (n.unknown)`。若后端**省掉**该字段，JS 读到 `undefined`
⇒ falsy ⇒ 落到 `frozen` ⇒ **「读不到」被显示成「已停更」**。
⚠ **手写 fixture 测不到**——fixture 是我自己写的。

新门 `TestV1DataHorizonEmitsUnknownFieldEndToEnd` 真调 `handleV1DataHorizon`，
断言 ①200 ②`unknown` **字段存在** ③`unknown:true ⇒ frozen≠true`
④**响应头 = `"unknown"`**（与 frozen 的 `"1"` 必须不同）。

★ **它第一次跑就给出意外事实**：**没有走 Skip**，实测 `unknown=true`
⇒ **单元测试环境里该键同样未配置**，与 **252 生产一致**
（`settings_kv` 41 行里没有 `storage.request_logs_write_enabled`）。
⚠ 我原以为测试环境会读到显式 live——**我错了**。

### 三个新变异（每个先 diff 确认落盘）

| # | 变异 | 结果 |
|---|---|---|
| M1 | 变参缺省改回 `"db"` | **红** |
| M2 | 删掉 M1 的新门 | **0 条红** ⇒ 该门是**唯一**守门人 |
| M3 | `json:"unknown"` → `json:"-"` | **红 2 条**（新端到端门 + 既有契约门） |

⚠ M2 的 0 条红**不是「新门没用」**，而是「**它是唯一的守门人**」。

### 两件**不是**缺陷的核实

1. **819 五点同步完整**（源/embeddata/`//go:embed` main.go:725/`embeddedSQLFiles` :944/
   `StartupFiles` runner.go:700/写路径在 `if logsWrite` 之外 client.go:1294）。
   ⚠ **我审计时两次 grep 落空**（猜错路径），**差点误判成「同步缺失」**
   ⇒ 纪律：「grep 没命中」先确认搜索面再下结论。
2. **815–819 五个迁移在 runner 全部登记**。
   ⚠ 反查「登记了但文件缺失」命中 `600_outbound_body_to_bodies_hot.sql`
   ——**既有的、与本轮无关的登记腐烂**，不属本次范围，**未碰**。

### ★ typecheck 归零的归属

`npm run typecheck` 现在 **0 错误**（上午是 19 处既有，我一度 20）。
⇒ **那 19 处是被并进来的 99 个提交修掉的，不是我的门通过。**
⚠ 记下来，别把「0 错误」当成自己的成果。

### 下一轮提示词

```
在 llm-gateway-go-4 上继续。

★ **工作区状态每次开工必须重查**（本轮它已被别人改了两次）：
  CHERRY_PICK_HEAD、HEAD、与 origin/main 的差异、并发进程。
  ⚠ 本地一度落后 99 个提交，而其中 819 迁移已被别人替我提交进 main。

① 【最重要】**storage.request_logs_write_enabled 未 seed**
   —— 端到端实测（TestV1DataHorizonEmitsUnknownFieldEndToEnd）确认
   单元测试环境与 252 生产**都**是 source=default ⇒ **横幅一上线就显示
   「停写开关未显式配置」**。这是正确行为，但是**可见的 UI 变化**。
   需用户决定：seed 该键 = true（生产写，需授权），还是接受常显横幅。
② 【建议】加「cohort 样本量过小」告警：code 档 p95=110,019ms 由 **3 行**支撑，
   24h 也只有 23 行 ⇒ 换数据面救不了；现有 gauge 只报 =0。
③ 【既有腐烂，非本次范围】installer runner 登记了
   `600_outbound_body_to_bodies_hot.sql` 但文件缺失 —— 需确认是「文件被删」
   还是「登记笔误」。
④ 【需裁决】§9.65.8 三条判据取舍。
⑤ 【不做】settle 那三条已全部证伪（§9.80.0/§9.80.8/§9.80.9），**别再查**：
   扩数据面只值 1 行、LATERAL 缺 0 行、5h clamp 生效且 promote 正常。

⚠⚠⚠ **本轮新增的两条纪律（都是我当天犯的）**：
   - **「让旧门变红」有两种成因**：「我改坏了」和「契约变了门必须跟着变」。
     判据是：门检查的那一格在新契约下**仍应成立**，只是需要**显式声明测的是哪一态**。
   - **「grep 没命中」先确认搜索面，再下结论。**
     本轮两次 grep 落空（猜错路径）差点把「同步完整」误判成「同步缺失」。

⚠⚠⚠⚠ **本轮审计还发现一条通用的坏味道**（已修，值得记住）：
   **为消除歧义而加的可选参数，自己可能成为新的歧义源。**
   `f(source ...string)` 的缺省值选错（选「知道」而非「不知道」），
   任何忘记传参的新调用点都会静默落到「知道」。
   ⇒ 变参/默认值的设计原则：**缺省必须落在「更保守、更响」的一侧**。

基线（2026-10-03 19:00 复验，变更后重跑）：
  go build ./... rc=0
  go test ./admin/ ./db/ ./internal/internaltraffic/ ./bg/ ./settings/ ./autoroute/ \
            ./domains/hooks/observability/telemetry/ -count=1  —— **7 包全绿**
  web 前端 158 文件 / 1141 用例全绿；i18n 审计 0 missing；typecheck **0**（他人修复）
审计文档 10,797 行（U+FFFD 仍只有既有的 4471/6674，冲突 0）。
不要 rebase，不要代为处理他人的 cherry-pick。
```

---

## 第四十二轮（§9.90）：★查完 §9.59.9 那 6 条 —— 根因是**产品缺陷**：持久化重试面已死 10 天，兜底落进内存缓冲，重启即丢

> 编号说明：本文件存在两套轮次编号（我的「第四十X轮」与并行会话的「第十X轮」）。
> 本轮用**第四十二轮**以免与并行会话的编号撞车；审计文档侧因并行会话已排到 §9.89，
> 本节取号 **§9.90**（原写 §9.60，提交前发现已被占用，已改）。

### 结论

§9.59.9 那批「已终态却无孪生」的请求，**不是口径问题，是镜像写链的真实丢失**，
且**此刻仍在发生**。

### 根因链（每一步都有证据）

1. **规模比 §9.59.9 记的大**：按「会话在 `sessions` 里存不存在」切，
   未配对的非 auto 行分两群——
   **A 群（64 条）：会话从未被创建**（`in_progress` 58 按设计 + `failure` 5 + `rate_limited` 1）；
   **B 群（10 条）：会话存在（2~600 轮）但个别轮缺失**。
   §9.59.9 只按 `request_status` 切，漏掉了这个更重要的切法。
2. **四道镜像闸门全放行**：首门（终态）、`IsProbeSyntheticSession`（`gw_session_id` 真实非空）、
   `IsInternalAutoEntry`（非 auto）、`entryToProcessedRequest`（sessionID 非空）。
3. **它们没走到写库**：`w.Write` 失败会打 `WARN … request_id=`。
   `/var/log/messages` 里这类 WARN 有 492 条（Oct 1/2/3 = 161/65/222），
   **这 6 个 id 命中 0 次**。
4. **持久化面已死**：`session_mirror_outbox` **147 条全是 `dead`，全部创建于 2026-09-23，
   10 天没接过任何东西**；失败原因全是 `timeout: context deadline exceeded`。
   日志实证降级：`Oct 1 05:05:08 outbox registration failed, degrading to in-process backlog`、
   `Oct 3 07:00:00 outbox registration commit failed, degrading to in-process backlog`。
5. **兜底是内存**：`EnqueueMirrorFailure` 返回 false ⇒ `appendBacklog`（纯内存）⇒ 重启即丢。
6. **此刻正在丢**：252 `/metrics` 实测
   `session_v2_mirror_backlog_pending = 1`、`llm_gateway_shadow_write_failed_total{kind="session_v2"} = 246`。

### ★§9.37 的第三次复现，但这次是升级形态

| 检查 | 结果 |
|---|---|
| `shadow_write_failed_total` 有告警 | ✅ `alerts/shadow-write-failures.yaml:51` |
| 该告警阈值 | `rate(…[5m]) > 10/60` = **>10 次/分钟** |
| 实际速率 | ≈ **0.06 次/分钟**（246 次 ÷ 2.9 天）⇒ **差约 170 倍** |
| `session_v2_mirror_backlog_pending` 有告警 | ❌ **无**（全库 grep 无命中） |

⇒ 计数器**被读**，但阈值高三个数量级，等于事实上不读；
唯一直接回答「有多少行正躺着等死」的 gauge **连告警都没有**。
**「有没有告警」不是充分检查，「告警会不会响」才是。**

### ⚠️ §9.56 的一条记录已失效（订正）

§9.56 写「journal 最早只到 2026-10-02 16:22」。**本轮实测 journal 最早只到
2026-10-03 17:05:41（已滚动）**——该条覆盖窗口**已作废**。
拿 journal 查 10-01/10-02 的请求只会得到 0 条，**那不构成证据**。
本轮改用 `/var/log/messages`（1.5 GB，归档到 09-27，当前文件覆盖 09-27 至今）才拿到覆盖。
**交叉数据源 > 缺失证据。**

### 建议修法（**本轮未落**，需拍板）

1. 告警 `session_v2_mirror_backlog_pending > 0 for: 5m`。
   ⚠️ **在 252 上会立刻响并持续响**（outbox 已死 10 天）——
   「让它一直响」vs「先修 outbox 再上告警」是**运维决策**。
2. 降 `shadow_write_failed_total{kind="session_v2"}` 的阈值（现高 170 倍）。
3. **补 `hook.go:152` 信号量满分支的日志**——它现在**一条都不打**，只记指标。
   这是本节最直接的可观测性缺口：一行 `slog.Warn` 就能让这类丢失有迹可循，
   也能让「6 条各走了哪条路」当场可判。

未落原因：① `deploy/prometheus/` 正被并行会话改动，扩大冲突面无益；
② 告警一上线就在 252 持续 firing，是需要负责人拍板的运维姿态。

### 测试

本节为**生产只读取证 + 文档**，无代码改动、无新增门。
引用门仍是 §9.59 交付的（`internal/sessionv2mirror/terminal_failure_gate_test.go`、
`dual_write_value_parity_integration_test.go`），两者已在 `origin/main`。

### 遗留

- 6 条各走了三条**无日志**返回路径中的哪一条，**无法逐条判定**（日志无痕迹）。
- B 群 10 条（会话存在、个别轮缺失）**未查**。
- outbox 为何从 2026-09-23 起停止接收（DB 超时？迁移？部署？）**未查明**。

### 下一轮提示词

> ① **需拍板**：`session_v2_mirror_backlog_pending` 告警是否落。
>    落则 252 立刻持续 firing（这是正确读法——outbox 已死 10 天）。
> ② **建议直接做**：`hook.go:152` 信号量满分支补一行 `slog.Warn`。
>    它现在不打任何日志，导致一类镜像丢失完全无迹可寻——这是本轮最硬的缺口。
> ③ 查 outbox 为何 2026-09-23 起停止接收（147 条 dead 之后），
>    以及 B 群 10 条「会话存在但个别轮缺失」。
> ④ §9.37 的判据要升级：检查「指标有没有告警」**不够**，
>    必须检查**告警阈值与实测发生率的量级差**（本例 170 倍）。

---

## 第四十三轮（§9.91）：★★**撤回 §9.90.4 的核心结论** —— 我把「outbox 表是空的」当成了「没人再写」

### 结论先行

§9.90.4 写「`session_mirror_outbox` 已死 10 天，不再是持久化面」，
并据此把 §9.59.9 那 6 条的根因归给它。**这个结论错了。**

| §9.90 的说法 | 订正后 |
|---|---|
| outbox 已死 10 天 | **正常工作**：入队 → 重放 → 成功后 `DELETE`（`replay.go:453`） |
| 292 次写失败、只有 2 条注册日志 ⇒ 290 次静默失败 | **290 次静默成功入队**（成功路径不打日志） |
| 147 条 dead ⇒ 近期仍在丢 | **09-23 的历史残留**，10-01 耗尽 attempts 判死，**全是探针** |
| §9.59.9 那 6 条根因 = outbox 死 | **仍未查明** |

### 我错在哪

`EnqueueMirrorFailure` 的成功路径 `return true` 且**不打任何日志**，
而重放成功会**删掉**那一行。所以：
**「表空」+「日志只有 2 条」= 工作正常的证据，我读成了停摆的证据。**

### 147 条的真实定性

```
request_id LIKE 'probe-%'  → 147/147
session_id LIKE 'sys:%'    → 147/147
attempts = 9，created 2026-09-23，updated 2026-10-01
```

`syntheticKindOf` 见到 `probe` 标记就返回 `"probe"`
⇒ `IsProbeSyntheticSession` **当前会拦下它们**。
**关键佐证：09-23 之后再无任何 `sys:probe:*` 行进入过。**
⇒ R51 那道门**现在有效**，这 147 条是它落地前的残渣。
死因正是 R51 注释点名的「合成探针会话集中打 advisory lock，失败噪声 ~115/min」。

### 真正的洞（我上轮误读的地方）

`replay.go:529-530` 的 `mirrorReplayTotal` / `mirrorReplayDeadTotal`
**已计数但根本没导出到 `/metrics`**
⇒ **「重放成功多少、失败多少」在监控上完全不可见。**
我正是因此把「不可见」读成了「没在工作」。

（附带交叉验证：当前进程日志 246 条 WARN 与指标
`shadow_write_failed_total=246` **完全相等** ⇒ 日志源完整，§9.90.3 的「0 命中」可信。）

### 建议的优先级要换

1. **先补量具**：把 `mirrorReplayTotal` / `mirrorReplayDeadTotal` 导出。
   **导出它们比加告警更根本。**
2. `session_v2_mirror_backlog_pending > 0` 告警**保留**，
   但理由换成「它是唯一能暴露内存兜底正在吃数据的信号」，
   不再是「outbox 已死」。

### 教训

> **「表是空的」有两种读法：没人写，或写了又被删。**
> 判定「写入停了」之前，**必须先确认这张表有没有「成功即删除」的语义**。
> 同一处证据（147 条 dead）我在两节里给了两个**互相矛盾**的解释，
> 第二个还是被交叉验证推翻后才发现的。

### 仍然未查明

- §9.59.9 / §9.90 的 A 群那 6 条终态请求（会话从未被创建）——**本节不提供新归因**。
- B 群 10 条（会话存在、个别轮缺失）。
- `session_v2_mirror_backlog_pending = 1` 的那一条是谁。

---

## 第四十四轮（§9.92）：★★**再撤一次** —— §9.91 的「重放计数已计数但未导出」也是错的；并**补上真正该补的告警**

### 连着两轮的自我订正

| 轮次 | 我的结论 | 真相 |
|---|---|---|
| §9.90.4 | 「outbox 已死 10 天」 | ❌ 正常工作（重放成功后 `DELETE`） |
| §9.91.2 | 「重放计数**根本没导出**到 `/metrics`」 | ❌ **一直在导出** |

§9.91.2 错在哪：我用 `grep -E "^mirror_replay|^session_v2_mirror"` 去搜 `/metrics`，
而这两个 `promauto` 计数器的**导出名与 Go 变量名完全不同**：

| Go 变量 | 实际导出名 | 命中？ |
|---|---|---|
| `mirrorReplayTotal` | `session_mirror_outbox_replays_total` | ❌ |
| `mirrorReplayDeadTotal` | `session_mirror_outbox_dead_total` | ❌ |

**我拿源码标识符去搜运行时产物。** 真值：`{ok}=2301 {retry}=1392 {dead}=112`，
`dead_total=112` 与按日志数的 112 条 ERROR **完全相等**（独立交叉验证）。
**重放面工作得很好。**

### 真正的洞（比原来说的更硬）

指标在、有值、还被**两处文档点名是告警主信号**：
- `replay.go:105` Help：*"the alerting-friendly top-level signal"*
- `db-changelog.md:751`：*"适合做告警主信号"*

**而仓内零告警读它。** 意图被写下了，执行没发生。

更隐蔽的是：这两个指标**改过名**（去 `llmgw_` 前缀）。`db-changelog.md:744-746`
警告「改名即断流」，且明说「仓外 Grafana/告警/采集配置不在该论证范围内」
⇒ **仓内 grep 无引用推不出没有断流风险。**

### 本轮落地

- `deploy/prometheus/rules/session-mirror-outbox.yml`
  `MirrorOutboxDeadLettered`（`increase(dead_total[1h]) > 0 for: 5m`）
  + `MirrorOutboxBacklogStuck`（`pending > 0 for: 30m`）
- `deploy/prometheus/rules/session_mirror_outbox_rules_test.go`
  两道门：**从 `replay.go` 的 `Name:` 推导指标名并与规则对账** + 结构完整性。

**门必须从源码推导、不能把名字写死**：写死 = 改名时顺手把门里的名字也改掉
⇒ 又回到人肉同步，**而那正是 db-changelog 警告的失效模式本身**。

变异 3/3 红因即断言：M1 改源码 `Name:` / M2 规则残留 `llmgw_` 前缀 / M3 删 `for:`。
`promtool check rules`：新文件 SUCCESS(2 rules)，`rules/*.yml` 全目录 SUCCESS。

### 未做（需拍板）

`session_v2_mirror_backlog_pending > 0` 告警**故意未加**：252 恒为 1，
加上即永久 firing，属运维姿态决策。

### 纪律（连踩两次）

> **grep 运行时产物（`/metrics`、JSON、HTTP 响应）时，关键词必须取自
> 「产物里的那个名字」，不能取自源码里的变量名。**
> 变量名→指标名要经过重命名+加前缀的转换，这个转换**源码里看得见，grep 里看不见**。
> 「grep 不到」的精确形态不是「不存在」，而是**「你的关键词写对了没有」**。

### 仍然未查明

A 群 6 条终态请求（会话从未被创建）、B 群 10 条、`backlog_pending=1` 的那一条。
**两轮未查明，不写归因。**

---

## 第四十五轮（§9.93）：四轮查不出来之后，改为把**丢弃面列成清单**

### 决定

§9.59.9 / §9.90 / §9.91 / §9.92 连追四轮「v1 有、会话族无孪生」那批请求，
中间推翻自己两次，**仍无定论**。**不再试第五种归因**，交付能做完的那半边。

### 根子：没有清单 ⇒「查不出来」与「没发生」在证据上无法区分

`PersistHook` 是长链闸门。逐条数下来，**能走到「一个字节都不写、一行日志都不打」
的分支有 6 处，其中 4 处按设计（有注释说明），2 处说不出理由**：

| 判据 | 日志 | 理由 |
|---|---|---|
| `!Success && !isTerminalFailure` | ❌ | 按设计（幂等约束，§9.59.6） |
| `IsProbeSyntheticSession` | ❌ | 按设计（R51，§9.91.3） |
| `!synthetic && IsInternalAutoEntry` | ❌ | 按设计（内部回环非用户 turn） |
| `!shadowWriteEnabled()` | ❌ | 运维主动关开关 |
| **`req == nil`** | ❌ | **说不出** |
| **`writer == nil`（整个 hook 变 no-op）** | ❌ | **说不出** |

那两条让「镜像被关掉」和「镜像因初始化失败而没接上」**在可观测性上完全等价**。

### 落地

- `hook.go`：`PersistHook(nil)` 构造期 `slog.Error`（*V2 mirror writes are SILENTLY
  disabled for the whole process lifetime* + hint 点明≠`shadow_write` 开关）；
  `req == nil` 分支 `slog.Warn` 带 `request_id`。
- `silent_drop_paths_test.go`：3 道门（行为门 / 结构门 / **总纲门：未登记的纯静默
  return 即红**，并防清单漂移）。

### ★变异抓到门自己第一版漏报

**同一个变异，块后留不留空行，红与不绿。** 根因：分支体扫描没在同缩进的 `}`
停下，走到下一条同缩进 `if`，把它的判据当成本分支语句，`pure` 被误判 false。
⇒ **门不是「写了就信」，是跑变异才发现自己没钉住。**
本会话第三次靠变异拦下装饰门。

M4 第一版 `sed` 也**没跑成**（`|` 分隔符冲突）——没生效的变异不是证据。

### 仍需拍板 / 仍需查明

- `session_v2_mirror_backlog_pending` 告警是否落（252 恒为 1）。
- A 群 6 条 / B 群 10 条 / `backlog_pending=1` 那一条：**四轮未收敛，不硬编归因**。

---

## 第四十六轮（§9.94）：★我 §9.59 那条「唯一会判红的自检」**是装饰** —— 实测坐实

### 我说过的原话（错）

> 这是本门唯一一条会判红的自检：若有人再把 `request_logs_hot` 从 v 里删掉……
> **没有它，§9.59 那个取样洞会被静默重新打开，而所有门仍然全绿。**

### 实测：在 252 上，删掉 `_hot` 腿，我那条自检**是绿的**

| 被测版本 | 变异 | 结果 |
|---|---|---|
| **我 §9.59 原版**（`total == parentFace+hotFace`） | 删 `_hot` 腿 | **绿** |
| 修版（`hotFace>0`/`parentFace>0`，并行会话 `d2adc3a37`） | 删 `_hot` 腿 | 🔴 `_hot 面贡献 0 行（父表 21425）` |
| 修版 | 删父表腿、只留 `_hot` | 🔴 `父表贡献 0 行（_hot 2773）` |
| 修版还原 | 无 | 绿 |

### 根因：它是**集合分划恒等式**

`total` / `parentFace` / `hotFace` **三个数全来自同一个 CTE**。
删掉 `_hot` 腿后：`total == parentFace + 0` 恒成立。
**用「全集 = 各部分之和」校验「各部分是否齐全」在逻辑上不可能发现缺了一个部分**——
缺一个时等式照样成立（另一部分独自就是全集）。

### 不是我写的修复

并行会话 `d2adc3a37` 读了我的注释、指出恒等式防不住并直接改掉，注释原文：
「`total == parentFace+hotFace` 是**集合分划恒等式**……**『唯一会判红的自检』实际防不住
任何改窄**。真判据是 `hotFace > 0`」。**我本轮把两边说法都变成实测数据。**

### 可复用规矩

> **不要用「全集 = 各部分之和」验「各部分是否齐全」——它是恒等式。**
> 要验齐全，必须让**每一部分各自被独立地看见**（各自 `> 0`，或从它自己的来源独立取数）。
> 更一般：**当判据的所有观测量都来自同一个被检验对象时，它多半验不了那个对象。**

### 为什么我没当场发现

写那条自检时我只做了「语法正确 + 跑通」，没做变异。而它偏偏是那种
「加了只会显得更严谨」的判据。**不写变异，就分不出恒等式和真检查。**

本会话同类已是第三次：§9.59.7 自指护栏、§9.93.3 分支体扫描、本节恒等式——
**三处都是「判据的正当性」问题，不是「判据写没写」问题。**

---

## 第四十七轮（§9.95）：给「退役 v1」配一个**可测量的进度数字** —— 第一步就发现现有的门测不了这件事

### 做了什么

前几轮都在查「镜像有没有丢数据」。本节换方向：查**读取面迁移了多少**——
这是目标「确认对原api尽可能的更新」的直接度量，此前**从未被量化过**。

### 现有登记表是当前的

`TestRequestLogsReadInventoryIsComplete` 本轮 **PASS**：登记 **106 文件 / 239 调用点**，
陈旧条目 **0**。

### ★第一次量化出的数，我立刻自己否掉了

| 分类 | 文件数 |
|---|---|
| 本文件内直接出现 `from session_*` | 6 |
| 只读 `request_logs`、本文件内无 `session_*` 直接 SQL | 100 |

**「6/106 已迁」不能用。** 它量的是**直接 SQL 不是能力**：
`db/db.go` 通用查询层 + `admin/unified_detail.go` / `session_summary_v2.go` /
`domains/session/v2/*` / `domains/sessionsummary/*` 都是会话侧共享抽象，
很多读点不自己写 SQL。
⇒ 这正是本会话反复订正的代理指标错误（§9.58 拿 `origin_stage` 当「真实业务」、
§9.59 拿「内连接配对数」当「覆盖率」）。**量具先问取样方向，再报数。**

### 一个读源码坐实的具体切面

`admin/unified_detail.go`（`GET /api/admin/request-detail/{request_id}`）实际顺序：

| 行 | 查询 | 位次 |
|---|---|---|
| 181/192 | `request_logs_hot` | 1 |
| 205/217 | `request_logs_with_current_month` | 2 |
| 275/284 | `request_logs_bodies_*` | 3 |
| **347** | `session_turns_with_current_month` | **4（最后）** |

注释自陈 *"Fill only missing fields … never replace fields already present in request_logs_bodies"*
⇒ **会话族在这个 API 里是「补空」兜底，v1 仍排在前面。**
这是「尽可能更新原 API」在一个具体端点上的真实完成度：**未完成，且方向是反的。**

### 元结论（比任何单个读点都更该先解决）

**退役 v1 目前没有可测量的进度。** 两条独立原因：
1. 守门**只数不判**（§8.4 已记）——它证明清单与扫描一致，不回答「还剩多少」；
2. **任何直接计数都会被共享抽象污染**（本轮实测）。

⇒ 一个不能被度量的迁移，就不能被管理。

### 需一次口径裁决（未落任何门）

| 口径 | 含义 | 能否做成常驻机械门 |
|---|---|---|
| (i) 直接 SQL | 文件里出现 `from session_*` | 就是那个被污染的 6 |
| (ii) 能力等价 | 该读点数据在 `session_*` **已齐备** | ❌ 只能人工核（§9.32 已证「有值」≠「值相同」） |
| (iii) 兜底顺序 | 门面是否**优先**读会话侧 | ✅ 顺序可解析 |

建议 **(iii) 先落门**（挡住「新增 API 又把 v1 排在前面」），
**(ii) 用值层对账门在生产定期抽测**。
**口径未裁决前不落任何门**——否则又是一道测错东西的判据。

---

## 第四十八轮（§9.96）：读取面普查 —— **93/106 个 v1 读点够不到会话请求数据**

口径裁决仍挂着，但裁决**之前**要先把数据取出来。本节做了普查，**不落任何门**。

### 实测（可靠）

| 分类 | 文件数 |
|---|---|
| **仅读 v1，够不到会话请求数据表** | **93 / 106（87.7%）** |
| 同时读会话请求数据（**可及**） | 13 / 106 |

会话侧表集合限定为 `session_turns*` / `session_bodies*` / `sessions*`。
⇒ 对「还剩多少没迁」，能站得住的答案是：**87.7% 的 v1 读点连表都没接触过**。

### 但「可及」≠「已迁」

13 个里 `dual_read_validator.go`（对账器）、`lite_retention_worker.go`（保留期清理）、
`credential_selfcheck.go`（自检）**按设计就该读 v1**。
真正「优先用会话侧」的是**个位数**（已坐实的只有 `unified_detail.go` 里的兜底链）。

### 我没量的东西：运行时兜底顺序

曾按「同文件内 `request_logs` 与会话侧首次出现位置」给 8 个文件排了「v1 在前」，
**该数不可用**——两个抽样直接证伪：
- `admin/session_title.go` 的会话侧读的是 `session_titles`（**标题存储表**）
- `domains/analysis/request_summary.go` 是「读 v1 → 写 `session_request_summaries`」（**派生表**）

⇒ 文本位置 ≠ 运行时顺序；真顺序要函数内控制流分析，人工也不可靠。
**兜底顺序目前无法用 grep 得出** ⇒ §9.95.5 的 (iii) 只能落成**人工判读清单**，
不能落成自动门。

### ★这把尺子上我犯的三个错

| # | 错 | 怎么发现 |
|---|---|---|
| 1 | 关键词**太宽**（`session_titles`/`session_request_summaries` 当成会话请求数据） | 抽 2 个文件逐读源码，都证伪 |
| 2 | 关键词**漏 `public.` schema 前缀** ⇒ `unified_detail.go` 被漏 | 自检项「unified_detail 必须命中」 |
| 3 | 拿**弱代理**当结论（文本先后 = 运行时顺序） | 同一批抽样 |

**三个错里两个是同一个病：关键词不是我以为的那个名字**（与 §9.92 同源）。

⇒ 纪律：**判「某能力是否存在于某处」时，先找一个已知必然命中的样本做自检，再报总数。
没有自检的计数，分母都可能错。**

---

## 第四十九轮（§9.97）：★退役 v1 的**硬阻塞清单** —— 两列在会话侧 0% 填充

§9.96 说「87.7% 的读点够不到会话请求数据」。本节问更前置的问题：
**那些数据在会话侧到底有没有？**

### 88 列的差距不是数据缺口，是**视图缺口**

`request_logs_with_current_month` **118 列** vs `session_turns_with_current_month` **55 列**，
同名仅 **30**，v1 独有 **88**。

但全族模糊匹配后，`system_fingerprint` / `is_final_success` / `raw_model_name` /
`origin_stage` / `is_auto_request` / `search_text` / `upstream_status_code` /
`origin_actor` / `task_type` **在会话族里全部存在**——在 `session_turns` 及其分区与
`_hot` 上，只是**没被 `session_turns_with_current_month` 视图投影出来**。
⇒ **视图可以改，这层不阻塞退役。**

### ★真正的阻塞是填充率（252 实测，7 天）

| 面 | 总数 | `search_text` | `system_fingerprint` | `is_final_success` |
|---|---|---|---|---|
| **v1**（父+hot） | 59,721 | **59,721（100%）** | 0 | **59,721（100%）** |
| **会话**（父+hot） | 50,295 | **0（0%）** | 0 | **0（0%）** |

1. **`search_text` 0%** → 退役 v1 后**全文检索彻底失效**，而且是**静默的**：
   列还在，查询不报错，只是永远空。
2. **`is_final_success` 0%** → GLOBAL_G2 对账与 `shouldClaimFinalSuccess`
   所依赖的列恒空，退役后这套对账**恒定认为「没有 final-success 行」**。

**「列存在但永远 NULL」比「列不存在」更危险**——它不报错，只给看起来正常的空答案。

### ★订正我自己的假设

我原以为 `system_fingerprint` 也是阻塞（§9.50–§9.52 刚为它做完一轮）。
**实测两侧都是 0** ⇒ 它是 §9.51 已查明的**上游缺口**（网关从不下发
`X-System-Fingerprint`），**不是**迁移缺口。我差点把「上游没发」当成「会话侧没存」。

### 阻塞处置顺序（本轮不实施）

| 优先级 | 项 | 性质 |
|---|---|---|
| 1 | `search_text` 写入会话侧 | 功能：不修则退役后搜索静默失效 |
| 2 | `is_final_success` 写入会话侧 | 正确性：不修则 final-success 对账恒空 |
| 3 | 88 列视图投影补齐 | 迁移成本：机械活 |
| 4 | 87.7% 读点换源（§9.96） | 迁移主体：依赖 1–3 |

**1、2 完成之前，任何 S4 停写都会造成不可逆的功能与正确性损失。**
这一条**不需要口径裁决**，它需要的是补两列的写入。

### 为什么填充率必须单独测

只比「列是否存在」会得出「全部齐备、可以退役」——**完全相反**。
填充率只能在生产库上测：本地库量不到（§9.53 有先例），视图列数也量不到。
⇒ 与 §9.59 同源：**「存在」与「有值」差着一个数量级。**

---

## 第五十轮（§9.99）：阻塞 #2（`is_final_success`）——★先撤回我 §9.98 的说法，再给真正的理由

### 撤回

§9.98 我写「`is_final_success` 是语义变更、需拍板，**不是搬运**」。**不准确。**
读 `client.go:2688` 本体后发现：

```go
func shouldClaimFinalSuccess(entry) bool {
    return entry != nil && entry.Success &&
        entry.GwSessionID != nil && *entry.GwSessionID != "" &&
        !IsInternalAutoEntry(entry)
}
```

**纯函数**，与 `searchText(entry)` 同一形状 ⇒ **布尔值这一半是机械的**。
我上一轮「不是搬运」是**凭想象下的难度判断，没读函数**。

### 真正的阻塞：每会话唯一性这个不变量，会话侧根本不存在

| 面 | 机制 |
|---|---|
| **v1** | 迁移 532 建 `UNIQUE INDEX … (gw_session_id) WHERE is_final_success AND gw_session_id IS NOT NULL AND gw_session_id <> ''`；claim 走 `SET is_final_success = TRUE`，撞 23505 降级 superseded，历史不回改 |
| **会话** | `session_turns` 上 `is_final_success` **连唯一索引都没有** |

⇒ **只搬布尔值是危险的**：会话里**每个**成功轮次都会是 TRUE，
而 v1 是**每会话恰好一个**。
**这比 0% 更坏**——0% 一眼看出坏了；「每行都 TRUE」是**看起来完全正常**的答案，
会让 GLOBAL_G2 把每个成功轮都当 final success，正是 `internal_loopback.go:19-22`
警告过的那类永久性虚高。

### 真正的修法（三步，缺一不可）

1. `session_turns` 上建等价唯一索引 `(session_id) WHERE is_final_success`
   ——**没有它，第 2 步的竞争语义无处依附**；
2. 把 claim-and-supersede 搬进会话写链（撞 23505 降级 superseded）；
3. 门：每个 `session_id` 至多一条 `is_final_success = TRUE`。

⚠ **1 是 schema 变更，且必须与 2 同批上线**——
否则唯一索引会让并发 claim 直接失败、反而造成**写入被拒**。

**本轮不实施**：schema + 写链的成对变更，属需负责人拍板，不是机械修复。

### 教训

> **「这个修复是机械的还是语义的」——不读函数本体就答不了。**
> 我凭「它由某个函数推导」判成语义变更，**理由是猜的**；
> 读完之后结论反了一半：布尔值机械，但**不变量**不在函数里、也不在列上，
> 而在**v1 独有的唯一索引**里。
> ⇒ 与 §9.92 / §9.96 同族：**先读，再断言**。
> 尤其是「难不难」这种判断——它最容易被**想象**替代**阅读**。

---

## 第五十一轮（§9.100）：★把 §9.98 的「只证明链路」升级为「真库证明落库」—— 顺手查出两个此前无人发现的缺陷

上一轮我把 §9.98 的三道门当成「阻塞 #1 已修」，只差一个库验效果。
本轮把库的问题解决了（**不需要任何新授权**），然后真库一跑就炸出两件事。

### 一、缺的是「可丢弃库」，不是「生产写权限」

我上一轮把这件事当生产授权问题处理，方向就错了。
仓内对真库测试的约定本来就写在注释里：`TEST_PG_URL` / `TEST_DB_URL`
**必须指向一次性可丢弃库**（`migration_711_test.go`、`hook_integration_test.go`）。

本机现成有：`llm-gateway-pg-amd64`（`registry.internal.example.com/kx-citus-pg17:13.3.0-vector-amd64`，
127.0.0.1:55432，superuser `llm_gateway`，初始 0 张表）。
⚠ 必须用 `kx-citus-pg17` 系镜像，裸 `postgres:17-alpine` 会让 `~/kaixuan/postgres`
的 citus 残留把任何 DROP POLICY/INDEX/CONSTRAINT 打崩。

### 二、缺陷 A：全新安装在 01-schema.sql 就断（真门 FAIL → 修后 PASS）

用仓内自己的 opt-in 真门裁决（**不自己复刻安装流程**）：
`cd installer && TEST_INSTALLER_FRESH_DB_URL=… go test -tags=integration -run TestFreshInstallerSessionTurnsHotBootstrap ./cmd/llm-gw-installer/`

- 修前 `FAIL`：01-schema.sql 报 `relation "public.candidate_failure_logs_hot" does not exist`
- 修后 `ok … 61.382s`

**根因**：`01-schema.sql` 是从**已跑过迁移的生产库**重新 dump 的，把
`v_adaptive_probe_targets` 子查询改指 `_hot`；而该表由 `392` 创建，
`392` 在 `StartupFiles[1]`——**排在基线之后**。基线只建父表（`:5913`）。
`814` 只有 `CREATE OR REPLACE`，视图缺席时也建不出来 ⇒ **依赖环，全新安装无解**。
引入提交 `199c65747`。

**修法**：基线视图子查询改回读自建父表，由 `814` 在 `392` 之后改指 `_hot`。
三份 `01-schema.sql` 副本（`deploy/sql/schemas/baseline/`、embeddata、`sql/schema/`）
md5 相同，**同改后仍相同**。

### 三、缺陷 B：★origin/main 的 session turn 写入 SQL 连解析都过不去（潜伏部署阻断）

```
write turn: insert turn: ERROR: inconsistent types deduced for parameter $3 (42P08)
DETAIL: text versus character varying
```

**差分对照**（不猜归因）：在 §9.98 之前的 `383ef8d03` 上跑**既有的**集成测试，
**同库同错** ⇒ 非 §9.98 引入；`turn_writer.go` 两树 diff 为空 ⇒ 对照干净。

**机制**：`$3`(tenant_id) 用两次——INSERT 目标列 `varchar(255)` 推成 varchar；
反连接 `tenant_id = $3` 经算子决议落到 `texteq(text,text)`（string 类 preferred type
是 `text`）推成 text。**pgx 不发参数 OID**，全靠服务端推导 ⇒ 同一 $3 两种类型，Parse 即拒。

**不是全新安装的假象**：252 三处 `tenant_id` 同为 `varchar(255)`，
在 252 上 `PREPARE` 同一段 SQL **报完全相同的错**。

**为什么今天还写得动**：在跑的 2026-10-01 构建**不含这段反连接**
（`grep -a 'NOT EXISTS (SELECT 1 FROM public.session_turns_with_current_month'` 命中 0），
且 `session_turns_hot` 最新 ts 就在查询当时，生产日志该错计数 0。
⇒ **潜伏部署阻断**：下次部署 origin/main 会丢掉**全部** session turn 写入。

**修法**：`WHERE tenant_id = $3::varchar`。修后 `PREPARE` 在本地库与 252 **双双通过**。

### 四、§9.98 的结论再收一次：写入修好了，**读取面根本没开始迁**

新真库门第一次跑撞 `column "search_text" does not exist (42703)`：
`session_turns_with_current_month` 只投影 **55 列，不含 `search_text`**。
而检索**今天仍在 v1**（`admin/logs.go:185/:537`，`rl` = `request_logs_hot`）。

⇒ **阻塞 #1 只修了一半**。v1 一退役，检索即断。读取侧迁移**尚未开始**。

### 五、新增真库门（opt-in `TEST_DB_URL`）

`internal/sessionv2mirror/search_text_realdb_integration_test.go`
写入 → 从 `session_turns_hot` 读回 → 断言 9 个特征 token 全在列 + 与 v1 纯函数逐字节相同。

**刻意避开恒等式**：`stored == *telemetry.SearchText(entry)` 在**两侧都空时恒真**，
而那正是修复前的世界。故先钉住纯函数产出真实内容，再逐 token 断言。

**变异 2/2**：

| 变异 | 红的断言 |
|---|---|
| 断 `s1a_fields.go` 映射 | `mirror-side mapping must reproduce the v1 pure function byte for byte` |
| 映射正确、writer 写回 `""` | `session_turns_hot.search_text is empty` |

第二个证明**落库断言独立于映射断言**（反空转成立）。

**断言面不含 `session_turns` 父表**：镜像只写 hot，父表由异步 promotion 搬运
（252 父表比 hot 落后 8 小时）——断言父表等于把周期任务编码成 §9.98 的期望。
视图**只 `t.Logf` 不断言**：给缺口加断言＝把缺陷钉成预期行为。

### 教训

> **「缺一个库」和「缺一份授权」是两件事。** 仓内注释早就写明真库测试要指向
> 可丢弃库——**先读约定，再提需求**。我为这个等了一整轮。
>
> **门只证明它覆盖的那一层。** §9.98 三道门全绿，真库一跑同时爆出
> 「全新安装断链」和「写入 SQL 不可解析」两个它们**结构上碰不到**的缺陷。
> §9.59 恒等式恒真那条教训第三次复现：**观测量全来自被检验对象内部时，
> 门验不了那个对象**。
>
> **潜伏缺陷比现网故障更危险。** 42P08 今天不发作，恰恰因为在跑的构建不含那段 SQL；
> 「生产写入正常」曾让我差点把它读成「我的复现有问题」。
>
> **副本纪律**：三份 `01-schema.sql` md5 相同，只改一份＝静默分叉。
> 同族的还有：`turn_writer.go` 在 origin/main 上**本来就不是 gofmt 干净的**，
> `gofmt -w` 会顺手重排 100 行——P0 提交不该混入别人的格式债，我还原了。

### 下一轮从哪开始（不要重做）

1. **可丢弃库已就绪**：`postgres://llm_gateway:***@127.0.0.1:55432/gw_fresh_test`
   （schema 由修好的 installer e2e 灌好，442 张表，会话族齐全）。
2. **两条真库门的复现命令**见上面各节。
3. **仍未收口**（与上轮相同，未因本轮而消解）：
   - 阻塞 #1 **读取侧**：`session_turns_with_current_month` 要不要加 `search_text`
     + `admin/logs.go` 的 `rl` 何时从 v1 切到会话族（**需拍板**）。
   - 阻塞 #2 `is_final_success`：schema + 写链成对变更（**需拍板**）。
   - 等价口径 (i)直接 SQL /(ii)能力等价 /(iii)兜底顺序（**需裁决**）。
   - `session_v2_mirror_backlog_pending` 告警（252 恒为 1，加即永久 firing）。
   - A 群 6 条 / B 群 10 条 / `backlog_pending=1`——五轮未收敛。
   - auto-route 自 2026-09-15 不产出 selection 的原因。
   - 内部 actor 名单 (a)(b)(c)、cohort 修正、§9.49.8 扩档、§9.48 口径、
     「开始了却没结束」要不要留落点。
4. **本轮新发现的待办**：`01-schema.sql` 副本无同步门（三份靠人工保持一致）；
   Makefile 无 gofmt 门（故 `turn_writer.go` 的格式债能长期存在）。
5. **部署提醒**：`a87258952` 未部署到任何环境。部署前确认 §9.100.3 的 cast 已在
   该构建里，否则**全部 session turn 写入丢失**。

---

## 第五十二轮（§9.101）：把 42P08 从「撞见的」变成「扫出来的」——80 条 SQL 全量 PREPARE，又抓两个同族缺陷

§9.100 那个 42P08 是跑测试时**撞见**的。撞见 ⇒ 可能还有别的。
本轮把会话写/读路径每条 SQL 抽出来逐条 `PREPARE`（只解析、不执行、不写行）到真 schema。

### 一、扫描：80 条 → 修前 10 条失败，修后 7 条（4 条是提取假阳性）

范围：`domains/session/v2` + `internal/sessionv2mirror` +
`domains/hooks/observability/telemetry`（`db.Selectf` 的 `%s` 拼接件跳过，不臆造替换）。

### 二、缺陷 C：★全新安装的会话元数据读写**完全不可用**（已修）

`session_aggregator.go` 两条语句恒 42703：
- `UpdateSessionMetadata`：`UPDATE public.sessions SET … title=…, user_tags=…`
- `GetSessionMetadata`：`SELECT … COALESCE(title,''), COALESCE(user_tags,…) FROM public.sessions`

真安装库的 `public.sessions` **既无 `title` 也无 `user_tags`**。

**根因**：`467_sessions_title_user_tags.sql` 存在、幂等、正好加这两列，
但**既没进 `Runner.StartupFiles` 也没进 embeddata**；基线 `CREATE TABLE public.sessions`
不含它们，且**没有任何已登记迁移**会加（655 加的是 `session_summaries`）。
⇒ 正是 runner 注释已描述的 **baseline-gap class**（388/392/471），**只是漏了 467**。

**为什么一直没人发现**：**生产 252 有这两列**，聚合器在生产完全正常；只有全新安装会坏。

**修法**：按既有五点同步接入（embeddata 副本 + `go:embed` 变量 +
`embeddedSQLFiles` map + `StartupFiles` 条目 + 用门自己的 `-update` 重生成 manifest），
放在 388/392/471 那个 pre-478 块里。

### 三、缺陷 D：`CorrectEstimatedUsage` 的 UPDATE 同样不可解析（已修，与 §9.100.3 同族）

`client.go:2034` 的 `AND ($2 IS NOT NULL OR $3 IS NOT NULL)`：
`IS NOT NULL` 对未定型参数**不提供类型信息**，`COALESCE` 给的类型**救不了它**。
受控实验：只有 COALESCE → 成功；只有 IS NOT NULL → 失败；完整语句 → 失败且报错行正是 `IS NOT NULL`。
**用本仓真实 pgx 驱动复现**（不只靠 psql）。修法 `$2::int` / `$3::int`（语义恒等，
`COALESCE` 已定为 integer）。

### 四、★一个我没解决的矛盾（如实记录）

252 上有 **10,023 行** `request_logs.usage_source='corrected'`（hot 2,010），
跨度与整表相同（09-30→10-03）。若该语句自 2026-08-15 就不可解析，这些行不可能由它写入。

试过五个独立解释，**全部不成立**：
1. 语句自 `e5d8cdeb9` 起从未改动，`$2::int` 从未存在；
2. 生产二进制含**同样**的无 cast 文本（`grep -a` 命中 1，无 `::int` 变体）；
3. 252 上 `PREPARE` 报同样的错，列类型与本地一致（全 `integer`）；
4. 全仓无第二个把 `usage_source` 写成 `'corrected'` 的地方
   （`format_anomaly_recorder.go:310` 写的是另一张表 + `UsageSourceLLM`）；
5. `request_logs` 整表就只覆盖 09-30→10-03（所以「corrected 只在近 4 天」是**保留期**，
   不是「路径何时生效」——我一开始也误读成后者）。

⇒ **来源未查明**。可能是仓外/不在本树历史中的旧版本，或运维一次性回填。
**在查明前不得宣称「生产这条路径是坏的」**。我只主张三方独立证实的那一件：
**当前这棵树里，这条语句无法被解析。**

### 五、门 + 变异

`fresh_installer_integration_test.go` 加三条：`sessions.title` 存在、
`sessions.user_tags` 存在、**把 `GetSessionMetadata` 那条 SELECT 当语句跑一遍**。

第三条刻意：**列存在 ≠ 语句可解析**——那正是 §9.100.3 的形态。只钉列会漏掉一半。

变异：从 `StartupFiles` 摘掉 467 ⇒ `sessions.title check failed: got "f"` + `user_tags` 同红。

### 六、教训

> **撞见的缺陷只是样本，不是全集。** 一个测试撞见的 42P08，系统扫 80 条又找出
> 两个同族问题，其中一个让全新安装的会话元数据读写**完全不可用**。
> ⇒ **能枚举的，就不要靠撞。**
>
> **「看起来矛盾」时不要急着选一个能自圆其说的解释。** 缺陷 D 的生产数据与代码
> 结论互相打架，我试了五个独立解释后把它记成**未解决**，而不是硬挑一个。
> **把矛盾当矛盾记下来，比编一个解释有用。**
>
> **可推广的判据**（不是两个孤立 bug）：`pgx` 不发参数 OID ⇒
> **凡参数出现在不提供类型上下文的位置**（`IS NOT NULL` / `IS NULL`）都可能 42P08；
> **同一参数在别处有 COALESCE 也救不了**。

### 下一轮

1. **可丢弃库就绪**：`postgres://llm_gateway:***@127.0.0.1:55432/gw_fresh_test`
2. **本轮仍未收口**（与上轮相同）：
   - ★缺陷 D 的 10,023 行 `corrected` 来源未查明（下一轮可查：252 上这批行的
     `request_id` 是否对应某类可识别的写入模式；或查仓外/旧构建）
   - 扫描剩余 3 条待查项：`outbox_events`、`gateway.session_tags`
     （两侧都没有 ⇒ 非新安装特有，但 `gateway` schema 缺失是否有意需确认）
   - 阻塞 #1 读取侧、阻塞 #2 `is_final_success`、等价口径 (i)/(ii)/(iii)、
     `backlog_pending` 告警、A/B 群 16 条、auto-route 断流、actor 名单等 —— 均待拍板
   - `01-schema.sql` 三副本无同步门；Makefile 无 gofmt 门
3. **部署提醒**：`$3::varchar`（§9.100.3）与 `$2::int`（§9.101.3）两处都未部署，
   部署前确认在构建里。

---

## 第五十三轮（§9.102）：★★撤回我 §9.100.3 与 §9.101.3——那两个「P0」都不是真的

**这是本会话最重要的一次自我更正。两个已被推送的结论作废。**

### 一、错在哪

`db/db.go:72`：

```go
cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
```

这是 **2026-07-15 的 P0 修复**（禁用预处理缓存，防长连接持有重命名关系的旧计划），
**生产必需配置**。该模式下 pgx **把参数客户端内联成字面量**，
服务端**从不做参数类型推导** ⇒ **42P08 在生产根本不可能发生**。

我两轮的真库门都用 `pgxpool.New(dbURL)`（pgx **默认扩展协议**）建池，
与产品真实配置不一致。**量具错了，于是读出两个不存在的缺陷。**

### 二、双协议对照（决定性）

同一条语句、同一张库，只改 exec mode：

| 语句 | 扩展协议（我的门） | SimpleProtocol（生产） |
|---|---|---|
| `CorrectEstimatedUsage` UPDATE | **FAIL 42P08** | **OK** |
| `turn_writer` INSERT（无 cast） | **FAIL 42P08** | **OK** |

⇒ **撤回 §9.100.3「下次部署会丢全部 session turn 写入」——不是真的。**
⇒ **撤回 §9.101.3「`CorrectEstimatedUsage` 从未成功过」——不是真的。**

生产独立佐证：252 上 `usage_source='corrected'` 的 max_ts 就是查询前几分钟
（01:15:02 vs 当时 01:18:59）；10,023 行里 10,023 行满足
`total_tokens = prompt_tokens + completion_tokens`、5,778 行 `cache_read_tokens`
非空，而 `estimated` 行这两项**全为 0** —— 正是那条 UPDATE 的指纹。**一直在跑。**

（上一轮记成「未解决」的矛盾，根因就是这个量具错误。）

### 三、两个 cast 保留但改定性

`$3::varchar` 与 `$2::int`/`$3::int` **保留**：语义恒等、两种协议下都合法、
不再依赖连接池配置、代价为零。但它们是**健壮性收口**，
**不是 P0、不是部署阻断**。

### 四、本轮唯一的行为改动：真库门保真度

`setupTestDB` 由 `pgxpool.New` 改为照抄 `db/db.go:72`（ParseConfig + SimpleProtocol），
并注明「`db/db.go` 那行变了这里也要变」。

**这才是根因修复**：用产品不用的配置建池的真库门，会持续产出「生产会炸」的假阳性。
⇒ 与「判据第一次运行前先验桩件接线」同源：**桩件接线方式必须与被验对象一致**。

### 五、顺带查出的既有测试缺陷（未修）

`TestPersistHook_Integration_DBWrite` 在**两种协议下都同样失败**
（`expected 1 bodies row`）——与本轮改动无关（前后同错）。
`storage.session_turns_bodies_enabled` 默认 false + 测试池 `settings.Global` 为 nil
⇒ bodies 分支不执行，断言却要求 1 行。**长期红的既有 opt-in 测试，需单独修。**

### 六、教训

> **「修复前先问：这个失败在我的运行配置下、在被测对象的运行配置下，
> 分别会发生吗」。** 我跳过了这一个问题，把量具的缺陷写成了产品的缺陷，
> 并推上了主分支。
>
> **已推送的错误结论必须公开撤回，不能悄悄改小。**

### 下一轮

1. **仍需拍板**（与前几轮相同，未变）：
   - 阻塞 #1 读取侧：`session_turns_with_current_month` 加不加 `search_text`
     + `admin/logs.go` 的 `rl` 何时切会话族
   - 阻塞 #2 `is_final_success`：唯一索引 + claim-and-supersede 同批上线
   - 等价口径 (i)/(ii)/(iii)
   - `session_v2_mirror_backlog_pending` 告警（252 恒为 1）
   - 内部 actor 名单 (a)(b)(c) / cohort 修正 / §9.49.8 扩档 / §9.48 口径
2. **不需要拍板**：
   - 修 `TestPersistHook_Integration_DBWrite` 的 bodies 断言（§9.102.5）
   - 查 `outbox_events` / `gateway.session_tags` 缺失是否有意
   - 补 `01-schema.sql` 三副本同步门 + Makefile gofmt 门
   - auto-route 自 2026-09-15 断流原因（仍缺 09-08/09-09 日志）
   - A 群 6 条 / B 群 10 条 / `backlog_pending=1`（五轮未收敛）
3. **纪律**：
   - 建真库门/压测前先确认 exec mode 与 `db/db.go` 一致
   - 能枚举的 SQL 用 `PREPARE` 扫（不需写权限），但**要按被测对象的协议解读结果**
   - 潜伏缺陷比现网故障危险（这条仍然成立）
   - 观测量全来自被检验对象内部的门，多半验不了那个对象

---

## 第五十四轮（§9.103）：让一道**长期红**的真库门变绿——它红的两个原因都不是写链坏

§9.102.5 记下 `TestPersistHook_Integration_DBWrite` 长期红。修它不是为了好看——
**它红着，就意味着「会话写链在真库上可用」从来没被验证过**，
而这正是本次任务要求确认的存储可用性。

### 一、失败一：断言查错了表（必然 0）

```
expected 1 bodies row → actual: 0
```

`SessionBodiesWriter.WriteBodiesInTx` 写的是 **`session_bodies_hot`**
（注释原话：「Write to session_bodies_hot (8-hour window), not directly to
partitioned table；PartitionManager 在 8h 后提升到 session_bodies 月度分区」），
而断言查的是 `public.session_bodies`——刚写的行在 `_hot` 里，分区表必然没有。
**这条断言从写下那天起就不可能通过。**

（与我 §9.100 主动从自己测试里删掉的 `session_turns` 父表断言同类：
把**周期搬运**编码成**写链的期望**。我犯了，仓里早有一处一样的。）

### 二、失败二：全新安装没有**当月**分区（真缺陷，但是启动窗口）

```
ERROR: no partition of relation "sessions" found for row (SQLSTATE 23514)
```

真安装库 `sessions` 只有 `sessions_2026_07` / `sessions_2026_08`，
**没有当月的 `sessions_2026_10`**。schema 是建库当时的月份形状，没人往后铺。
仓里有现成函数（迁移 430 的 `ensure_sessions_v2_partitions`），
但**只有 `bg/partition_manager.go` 后台工调它**，写链自己不调。

**实测**：`SELECT public.ensure_sessions_v2_partitions(current_date)` 立刻创建出
`sessions_2026_10` ⇒ 函数是好的，只是没被调到。

**定性**：**启动时序窗口**，不是永久缺陷。让写链自己 ensure 是行为变更
（每请求一次函数调用），属需拍板，**本轮只做测试侧对齐**。

### 三、结果

```
--- PASS: TestPersistHook_Integration_DBWrite
    Integration test passed: session=intg_test_… turn=1 bodies=1 sessions=1
```

三个 1 是**第一次同时为真**：turn 进 hot、bodies 进 hot、sessions 进当月分区。
⇒ **「会话写链在全新安装上可用」现在有了真库证据**，而不是「没人跑过所以不知道」。

再次核过：`sessions_2026_10` 是 `sessions` 的**分区**（pg_inherits），
而 `session_bodies_hot`/`session_turns_hot` 是**独立存储面**。
**分区父表（异步搬运）与独立热表（直接写）不能混为一谈。**

### 四、测试

```
TEST_DB_URL=… go test -tags=integration ./internal/sessionv2mirror/   → ok（两道 integration 门都过）
go test ./internal/sessionv2mirror/ ./domains/session/v2/
  ./domains/hooks/observability/telemetry/                            → 全 ok
```

### 五、教训

> **一道长期红的门，等于一段没被验证过的代码。** 我一直把它当噪音记着；
> 修它才暴露出「存储可用」这件事我此前从未真正验过。
>
> **红着的门要先问「它红是因为被测对象坏，还是因为门自己写错了」。**
> 这次两个原因都是后者。**先修门，再谈被测对象。**
>
> **「查错表」和「等异步」是两个独立故障，会互相掩护**：
> bodies 断言遮住了 sessions 断言。逐条修才逐条暴露。

### 六、下一轮

**不需要拍板**：
- 查 `outbox_events` / `gateway.session_tags` 缺失是否有意（两侧均无）
- 补 `01-schema.sql` 三副本同步门 + Makefile gofmt 门
- auto-route 自 2026-09-15 断流原因（仍缺 09-08/09-09 日志或 Prometheus 历史）
- A 群 6 条 / B 群 10 条 / `backlog_pending=1`（五轮未收敛）
- **新增候选**：是否让写链在缺当月分区时自 ensure（属行为变更，**需拍板**）

**待拍板**（与前几轮相同）：
- 阻塞 #1 读取侧：`session_turns_with_current_month` 加不加 `search_text`
  + `admin/logs.go` 的 `rl` 何时从 v1 切会话族
- 阻塞 #2 `is_final_success`：唯一索引 + claim-and-supersede 同批上线
- 等价口径 (i)/(ii)/(iii)
- `session_v2_mirror_backlog_pending` 告警（252 恒为 1，加即永久 firing）
- 内部 actor 名单 (a)(b)(c) / cohort 修正 / §9.49.8 扩档 / §9.48 口径

---

## 第五十五轮（§9.104）：★撤回「`01-schema.sql` 三副本无同步门」——门有，而且我差点加的那道是**已被否决的假不变式**

上一轮我列了「补三副本同步门」当待办。这轮去加，**先查了仓里有没有**——
两次都指向：我错了，而且那道门不该加。

### 一、门是有的，两道，当前都 PASS

- `sql/schema/baseline_drift_test.go` → `TestDerivedBaselineLagIsSuppliedByMigrations`
  （派生副本相对 canonical 的**世代差**必须由增量迁移补齐）
- `sql/migrations/startup/baseline_ensure_functions_contract_test.go`
  （三副本的 **`ensure_*` 函数**必须一致）

我 §9.100.2 的三副本同改**没违反任何一条**。

### 二、★那道门不该加：仓里把它记成「假不变式，不得重新发明」

`baseline_drift_test.go` 开头原话（节选）：

> ── A FALSE INVARIANT, RECORDED SO IT IS NOT RE-INVENTED ──
> The first version of this file asserted that all three copies create the
> same object set. It went red, and **the red was correct while the rule was
> wrong**… That delta is not drift to be closed. It is a **generation offset**.

而且三份副本是**手工维护的孤儿**：`dump-schema.sh` 依赖的
`scripts/_lib/db-init-lib.sh` **从来不在这个仓里**（路径逃出仓外），
自 `28d4d8612`（2026-07-05）起就不可运行。

⇒ **我正要写的「三份必须一致」就是那条被试过、被判错、并写明「不要重新发明」的规则。**

### 三、变异界定了既有门的**边界**（不是证明它坏）

只把 canonical 改成引用一张**不存在的表** `candidate_failure_logs_TAMPERED`：

```
TestBaselineEnsure… → ok   ← 没红
```

⇒ ensure 门**确实不覆盖非 `ensure_*` 的语句**（我 §9.100.2 改的视图在覆盖之外）。
**但这不构成加门的理由**：派生副本本来就应该靠迁移补齐世代差，
「任意语句不一致即红」不是成立的不变式。真要补，得先裁决**哪些差异是合法世代差**。

**本节不改任何门**，只撤回上一轮的待办条目。

### 四、顺带：`sql/schema` 整包在本机红，但与产品无关

`TestFirstLivePythonControls` 失败：PATH 里没有 `python` 这个可执行名（只有 `python3`）。
**环境前置条件**，不是缺陷，也与本会话改动无关。记下来免得下一轮误判。

### 五、教训

> **写新门前先问「这条方向已有门覆盖吗」，而且要去读那条门留下的注释。**
> 仓里不仅有门，还把**加这个门**的失败尝试写进了注释，专门防止重蹈。
>
> **变异的作用不只是证明「门有效」，也可以界定「门的边界」。**
> 但**「不覆盖」≠「该加」**——先问这条不变式成不成立，再问有没有门。
>
> **重复的门 + 错的范围 = 专门生产假红的机器，比不写更坏。**

### 六、下一轮

**不需要拍板**（清单已缩短一项）：
- 查 `outbox_events` / `gateway.session_tags` 缺失是否有意（两侧均无）
- auto-route 自 2026-09-15 断流原因（仍缺 09-08/09-09 日志或 Prometheus 历史）
- A 群 6 条 / B 群 10 条 / `backlog_pending=1`（五轮未收敛）
- Makefile 无 gofmt 门（`turn_writer.go` 的格式债因此长期存在）

**待拍板**（与前几轮相同）：
- 阻塞 #1 读取侧：`session_turns_with_current_month` 加不加 `search_text`
  + `admin/logs.go` 的 `rl` 何时从 v1 切会话族
- 阻塞 #2 `is_final_success`：唯一索引 + claim-and-supersede 同批上线
- 等价口径 (i)/(ii)/(iii)
- `session_v2_mirror_backlog_pending` 告警（252 恒为 1）
- 内部 actor 名单 (a)(b)(c) / cohort 修正 / §9.49.8 扩档 / §9.48 口径
- **新增候选**：写链是否自 ensure 当月分区（消除全新安装启动窗口，行为变更）

---

## 第五十六轮（§9.105）：扫描剩项结案——`outbox_events` 良性，`gateway.session_tags` 真错（两层）

### 一、`outbox_events` 缺失 = **良性，设计如此**

表由 **`deploy/sql/migrations/V357__…`**（Flyway 目录，不在 `sql/migrations/startup/`）创建，
无任何已登记 startup 迁移建它，252 也没有。看着像缺陷，但守卫链完整
（`cmd/gateway/main.go:2457-2470`）：只有 `ASM_INTERNAL_ENDPOINT` **且**
`AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET` 都配置时才 `SetOutboxWriter`，
否则打 "outbox writer disabled: incomplete ASM configuration"。
两处 INSERT 都在 `c.outboxWriter != nil` 内 ⇒ **缺表是设计内的良性状态**。

### 二、★`gateway.session_tags` = **两层都错，且从未被解析过**

```sql
SELECT DISTINCT tag_value
FROM gateway.session_tags          -- schema 早不存在（表已被统一到 public）
WHERE tenant_id = $1 AND session_id = $2 AND tag_source = 'auto'
```

1. **schema**：252 有 `public.session_tags`，`information_schema.schemata` 无 `gateway`
   （schema 统一时移除；迁移 430 删掉了冗余的 `CREATE SCHEMA gateway`）。
2. **列名**：真表主键列是 **`gw_session_id`**，不是 `session_id`。

**为何无人发现**：`GetSessionMetadata` **无任何生产调用方**（只有
`session_metadata_test.go`），而那个单测**驱动 mock**，SQL 从未被解析。

**修法**：`gateway.` → `public.`，`session_id` → `gw_session_id`；
修后 **252 上 PREPARE 通过**（只解析）。

### 三、有意留下的缺口：全新安装没有 `session_tags`

真安装库无此表；建它的是未登记的 `351_session_analytics_tables.sql`
（与 §9.101-C 的 467 同类 baseline-gap）。
**本轮不登记 351**：`GetSessionMetadata` 无调用方，为一条**死读路径**登记迁移
比留缺口更糟。要不要补取决于这个函数将来是否接线。
**这是有意留的缺口，不是遗漏。**

### 四、测试

```
go test ./domains/session/v2/ ./internal/sessionv2mirror/            → 全 ok
TEST_DB_URL=… go test -tags=integration ./internal/sessionv2mirror/  → ok
252: PREPARE 新写法 → PREPARE OK（只解析，不执行不写行）
```

### 五、教训

> **「缺一张表」≠「缺一个缺陷」**：追下去发现整条链都有守卫。
> **没有守卫的缺失才是缺陷。**
>
> **一条从未被解析的 SQL 可以同时错两处而无人知晓**——唯一调用方是 mock 单测。
> mock 验的是「调用发生了」，不是「SQL 能跑」。
>
> **「修一半」也要查第二半**：我先改 schema，立刻又撞列名错误。
> 停在第一步就会留下一个仍 42P01 的语句，且注释写着「已修复」。

### 六、下一轮

**不需要拍板**（清单已再缩短一项）：
- auto-route 自 2026-09-15 断流原因（仍缺 09-08/09-09 日志或 Prometheus 历史）
- A 群 6 条 / B 群 10 条 / `backlog_pending=1`（五轮未收敛）
- Makefile 无 gofmt 门

**待拍板**（与前几轮相同）：
- 阻塞 #1 读取侧：`session_turns_with_current_month` 加不加 `search_text`
  + `admin/logs.go` 的 `rl` 何时从 v1 切会话族
- 阻塞 #2 `is_final_success`：唯一索引 + claim-and-supersede 同批上线
- 等价口径 (i)/(ii)/(iii)
- `session_v2_mirror_backlog_pending` 告警（252 恒为 1）
- 内部 actor 名单 (a)(b)(c) / cohort 修正 / §9.49.8 扩档 / §9.48 口径
- 写链是否自 ensure 当月分区（消除全新安装启动窗口，行为变更）

---

## 第五十七轮（§9.106）：★再撤回「Makefile 无 gofmt 门」——门有，且是 ratchet 模式

**连续第二次栽在「grep 不到 ≠ 没有」上。**

### 一、门在哪

`.golangci.yml` 的 `formatters: enable: [gofmt, goimports]`，注释写「CI 中以 check
模式运行：发现未格式化文件即 fail」。`make lint` 调 `golangci-lint run`，
跑在 `.github/workflows/sessionforensics-ci.yml`（工作流真名 **`llm-gateway-go-ci`**），
触发条件是 **push/PR 到 main** —— 就是主 CI。
`grep Makefile 找不到 gofmt`，只因**门在 golangci-lint 里，不在 Makefile 文本里**。

### 二、实测：门在响，但 main 一直在违反它

```
golangci-lint fmt --diff  → rc=1，约 341 个文件
gofmt -l .（排除 vendor） → 296 个文件
```

### 三、但这是**被明确容忍的存量债**

CI 的 lint step 用 `--new-from-rev=$LINT_RATCHET_BASE`，注释原话（AUDIT_24H, 2026-08-17）：
「The repo carries ~224+ legacy lint findings, so a plain `golangci-lint run` is
permanently red and **gates nothing**… the legacy stock ratchets down as it gets
cleaned.」

⇒ **本节不格式化那 296 个文件**：几百个文件、跨别人的在途工作，属项目级决策。

### 四、我该做的那部分（做了并核验）

ratchet 的实际约束是「别引入新的」。我这轮改过的 8 个 Go 文件里 **7 个 clean**，
唯一 DIRTY 的 `turn_writer.go` 是**我改之前就有**的存量债（§9.100.3 已记录，
当时我有意没替别人还）。

**精确核验是否踩 ratchet**：
`gofmt -d turn_writer.go` 的两个 hunk 是 `@@ -147,21 @@` 与 `@@ -175,36 @@`，
而我改的两处在 **:347 与 :434**，**完全落在 hunk 之外** ⇒ 不会因我的提交而红。

### 五、纪律

> **「grep 不到」不等于「没有」——这一轮连续第二次。**
> 下断言前要问「**这个门可能以什么形式存在**」，而不是「我 grep 的是什么」。
>
> **两个量具口径不对齐时，数据矛盾不是结论。**
> 296 个文件脏 **且** 门存在，两件事同时为真并不矛盾；
> 矛盾的是我拿 `Makefile` 一个文件推断「整个仓没有格式门」。
>
> **ratchet 模式改变了「什么算缺陷」**：存量红是已知且被容忍的，
> 新增红才是问题 ⇒ 正确动作是**确认自己不新增**，不是清债。

### 六、下一轮

**不需要拍板**（清单已再缩短一项）：
- auto-route 自 2026-09-15 断流原因（仍缺 09-08/09-09 日志或 Prometheus 历史）
- A 群 6 条 / B 群 10 条 / `backlog_pending=1`（五轮未收敛）

**待拍板**（与前几轮相同）：
- 阻塞 #1 读取侧、阻塞 #2 `is_final_success`、等价口径 (i)/(ii)/(iii)、
  `backlog_pending` 告警、内部 actor 名单/cohort/§9.49.8/§9.48 口径、
  写链是否自 ensure 当月分区

---

## 第五十八轮（§9.107）：★§9.98「待实测」收口——生产 0% 的原因是**修复没部署**

### 一、252 侧实测（只读 SELECT）

| 表 | 行数 | `search_text` 非空 |
|---|---|---|
| `request_logs`（v1） | 54,969 | **54,969（100%）** |
| `session_turns` | 810,226 | **0** |
| `session_turns_hot` | 1,920 | **0** |

### 二、根因：部署构建里没有这个修复

`go version -m /opt/llm-gateway-go/bin/gateway`：

```
vcs.revision=2b6d337b21cd4acf7ab9bc51e3f0b3011dfb2aa8
vcs.time=2026-09-30T21:09:37Z     （= 10-01 05:09 CST，与二进制 mtime 吻合）
```

服务 `llmgo-252-dev` 自 2026-10-01 05:19 起跑。

**关键校验**（不靠推断）：
`git merge-base --is-ancestor 366b1b2ac 2b6d337b2` → **不是祖先**
⇒ §9.98 的修复不在部署血缘里 ⇒ **0% 完全符合预期，不是修复无效**。

### 三、★部署差距

```
git rev-list --count 2b6d337b2..origin/main  → 790 个提交
时间跨度 2026-10-01 → 2026-10-04（3 天），其中 sql/migrations/ 变动 50 个文件
```

⇒ **生产落后 origin/main 790 个提交 / 3 天 / 50 个迁移文件。**
本会话 §9.98～§9.106 的全部修复都在这个**未部署 delta** 里。

⇒ §9.100.3 那条「部署前必须确认 cast，否则丢全部写入」的**部署提醒彻底作废**：
当前构建根本不含它们，且 §9.102 已证明它们是健壮性收口、不是 P0。

### 四、我自己差点写错的一条（如实记）

`git diff --stat 2b6d337b2 origin/main` 显示三份 `01-schema.sql` 改动量差异极大
（46 / 534 / 353 行），我差点断言「三份又分叉了」。
**实测推翻**：origin/main 上三份**字节完全相同**（md5 均 `40742fd6…`，各 271 表）。
⇒ 那组数字是**从部署树到 main 的累计改动量不同**，不是 main 的当前状态。
⇒ 差集要看**同一时刻的状态**，不是**两端的差**。

### 五、部署前应该知道

1. 790 提交 + 50 迁移文件，**不是一次普通发布**。
2. 本会话 6 个修复中，**只有 3 个影响新装环境**（01-schema 断链、467 缺失、
   当月分区）；其余对现网是「行为改善」而非「修现网故障」。
3. §9.102 撤回的两个 P0 **不存在**，现网**没有**因本会话被推迟的修复。
4. 现网 `session_turns.search_text` 0% **不是故障**，是未部署；
   但它是**退役 v1 硬阻塞的当前状态**——要退役 v1，必须先部署并验证填充率。

### 六、下一轮

**不需要拍板**：
- auto-route 自 2026-09-15 断流原因（仍缺 09-08/09-09 日志或 Prometheus 历史）
- A 群 6 条 / B 群 10 条 / `backlog_pending=1`（五轮未收敛）

**待拍板**：
- **★新**：790 提交的发布窗口与回滚方案（是否分批、是否先发全新安装类的 3 个修复）
- 阻塞 #1 读取侧、阻塞 #2 `is_final_success`、等价口径 (i)/(ii)/(iii)、
  `backlog_pending` 告警、actor 名单/cohort/§9.49.8/§9.48 口径、
  写链是否自 ensure 当月分区

---

## 第五十九轮（§9.108）：把「阻塞 #1 读取侧」从抽象问题变成**可定量的三段决定**

我把「视图加不加 `search_text` / `admin/logs.go` 何时切会话族」挂成抽象问题挂了好几轮。
这轮把它量化了——**这个决定要买的东西有多贵，数字是多少。**

### 一、读取面规模

`admin/logs.go` 用到的 `rl.*` 去重列共 **71 个**（主查询读
`request_logs_with_current_month`，统计查询读 `request_logs_hot`）。

与会话侧对账（列名逐个比对）：

| 分类 | 列数 |
|---|---|
| 会话**视图已投影** | **16** |
| **父表有、视图未投影** | **30** |
| 会话侧**完全没有** | **25** |

⇒ **46 / 71（65%）只差「视图没投影」这一层。**

### 二、机械的那段：加宽视图覆盖 30 列

父表 `session_turns`（104 列）已有但 55 列视图没带出来、且 admin 用到的 30 列里
**就有 `search_text`**。⇒ 「加不加 search_text」的真实规模不是 1 列，
而是**一次视图口径重定**（55 → 85 列的宽视图）。

代价提示：改视图定义要**同改三份 baseline + 相应迁移**（§9.104 已知三份是手工
维护的孤儿），**不是一行 SQL**。

### 三、非机械的那段：25 列会话侧没有

可派生/改名对齐（初判非结论）：`total_tokens`、`gw_session_id`、`request_status`、
`attachments`（部分）、`provider_id`。
真正无来源（v1 独有采集面）：`outbound_msg_hashes`、`outbound_token_est`、
`outbound_msg_count`、`virtual_ip`、`virtual_mac`、`request_class`、`due_at`、
`affinity_hit`、`trace_seq` 等。

### 四、三条必须说清的限定

1. 这是**按列名**比对的，**不是按语义**——`success` 两侧都有但口径未验证。
   **同名 ≠ 同义**。
2. 视图加宽要同改三份 baseline，**不是一行 SQL**。
3. 25 列的归属是**产品决策**：退役时「接受能力下降」还是「补采集」。

### 五、所以读取侧可以拆成三个独立决定

| # | 决定 | 性质 | 规模 |
|---|---|---|---|
| A | 会话视图是否加宽覆盖那 30 列 | **机械** | 视图定义 ×3 + 迁移 |
| B | 那 16 个已有列的**语义**是否与 v1 等价 | **需核对** | 逐列对账 |
| C | 剩下 25 列：接受能力下降 vs 补采集 | **产品决策** | 未知 |

**A 不必等 B/C，可以先做；B 和 C 才决定「v1 能不能退役」。**

### 六、教训

> **把「要不要做」翻译成「要做的话买什么」。** 数字一变，这件事的性质就从
> 「开放决策」变成「一段机械工作 + 两个有界决定」。
>
> **同名不等于同义**——不能因为「列都在」就说 A 和 B 一起完成了。

### 七、下一轮

**不需要拍板**：
- 决定 B 的逐列语义对账（可先做 A 的准备工作：列出加宽视图的候选列集）
- auto-route 自 2026-09-15 断流原因（仍缺 09-08/09-09 日志）
- A 群 6 条 / B 群 10 条 / `backlog_pending=1`（五轮未收敛）

**待拍板**：
- 决定 A / B / C（见上表）
- 阻塞 #2 `is_final_success`、等价口径 (i)/(ii)/(iii)、`backlog_pending` 告警、
  actor 名单/cohort/§9.49.8/§9.48 口径、写链是否自 ensure 当月分区
- **发布**：790 提交 / 50 迁移文件的窗口与回滚方案

---

## 第六十轮（§9.109）：决定 B 的第 1 列就抓到硬阻断——`credential_id` 类型不兼容，**只在运行时炸**

### 一、16 列类型对账：14 一致，2 不同

| 列 | 会话视图 | v1 |
|---|---|---|
| `credential_id` | **text** | **bigint** |
| `tenant_id` | `character varying` | `text` |
| `client_protocol` | `text` | `character varying` |

后两个只是 text vs varchar，字符串可比较，**无碍**。

### 二、★`credential_id` 是硬阻断，且 PREPARE 查不出来

`admin/logs.go:531` 用 `queryIntPtr` 传**整数**：
`addFilter("rl.credential_id = $%d", *v)`

**PREPARE 通过**（`$1` 未定型 → PG 按 texteq 定为 text）：
```
PREPARE … WHERE credential_id = $1   → OK
```

**运行时形态炸**（SimpleProtocol 把整数内联成字面量 `42`）：
```
SELECT … FROM session_turns_with_current_month WHERE credential_id = 42;
  ERROR: operator does not exist: text = integer      ← 42883
对照 request_logs_hot（bigint）→ 正常
```
父表 `session_turns`（视图未投影那层）**同样炸** ⇒ 不是「加宽视图」能解决的，
是**列类型本身**。

⇒ **只要把 `rl` 切到会话族，credential_id 过滤每个请求都会 42883。**

### 三、修法两种，都要拍板（本节不擅自改——它改变查询形状）

| 方案 | 代价 |
|---|---|
| `rl.credential_id::bigint = $1` | 可能丢索引；**会话侧是否有 credential_id 前导索引要先量** |
| `= $1::text` | 不改索引，但入参语义从数值变字符串 |

### 四、教训

> **PREPARE 通过 ≠ 运行时会过**（客户端内联字面量时）。
> §9.102 我用「两协议对照」推翻了自己的两个假 P0，根因正是这个内联；
> 这一轮同一机制又制造了**反向陷阱：PREPARE 给了假的绿灯**。
> ⇒ 类型不兼容的怀疑，**两种形态都要测**：占位符形态 + 内联字面量形态。
>
> **别把上一轮的教训用反**：§9.102 说「PREPARE 的失败在生产不成立」，
> 这轮说「PREPARE 的通过在生产也不成立」。**两个方向都不可单独采信**。
>
> 逐列对账在**第 1 列**就出了硬阻断 ⇒ 这个动作的产出密度比预期高。

### 五、下一轮

**不需要拍板**：
- 量 `session_turns(_hot)` 上有没有 `credential_id` 前导索引（决定上面选哪个修法）
- 继续决定 B 的第 2–16 列类型对账
- auto-route 断流原因、A/B 群 16 条

**待拍板**：
- 决定 A（视图加宽 30 列）/ B（语义等价）/ C（25 列归属）
- `credential_id` 的修法（cast vs 改入参类型）
- 阻塞 #2 `is_final_success`、等价口径 (i)/(ii)/(iii)、`backlog_pending` 告警、
  actor 名单/cohort/§9.49.8/§9.48 口径、写链是否自 ensure 当月分区、
  790 提交的发布窗口与回滚方案

---

## 第六十一轮（§9.111）：把读取侧的**过滤面**整体过一遍——14 条谓词里 4 条整数过滤 4/4 全断

§9.109 只查了 `credential_id` 一列就抓到硬阻断。**同类问题必须一次问完**。

### 一、14 条谓词里 4 条传整数（`queryIntPtr`）

`api_key_id`、`canonical_id`、`credential_id`、`provider_id`。
其余 10 条传字符串，与列类型天然匹配，不构成同类风险。

### 二、实测（运行时形态，整数字面量 `= 1`）

| 列 | 会话**视图** | 会话**父表** | v1 |
|---|---|---|---|
| `api_key_id` | ✗ 列不存在 | ✗ `text = integer` | ✓ |
| `canonical_id` | ✗ 列不存在 | **✓ OK** | ✓ |
| `credential_id` | ✗ `text = integer` | ✗ `text = integer` | ✓ |
| `provider_id` | ✗ 列不存在 | ✗ 列不存在 | ✓ |

⇒ **当前视图上 4/4 全断；即使下沉到父表也只有 `canonical_id` 一条能用。**
v1 侧四条全部正常。

### 三、三种不同失败形态，修法不同

- **42703 列不存在**（视图未投影）：`api_key_id`、`canonical_id`（父表有，
  加宽视图可解 2 条）、`provider_id`（父表也没有 ⇒ 归入 §9.108 的 25 列无来源）
- **42883 类型不兼容**：`credential_id`、`api_key_id`（父表层）
- **`credential_id` 是唯一「两条路都断」的列**

### 四、与 §9.108 的数字合起来

§9.108 那个「65% 只差视图投影」**只对 SELECT 成立**。把过滤面算进来：
- SELECT 面：71 列 → 16 已有 / 30 加宽可覆盖 / 25 无来源
- 过滤面：14 条 → **10 条天然安全 / 4 条全断**

⇒ **过滤面比投影面更窄也更脆**：投影缺列是「少几个字段」，
过滤断掉是「**请求直接报错**」。
⇒ 决定 A 的必要性从「补全数据」升级为「**不补就不能切**」。

### 五、教训

> **同类问题必须一次问完**——§9.109 逐列做撞上第 1 列；
> §9.111 改成先按「参数类型 × 列类型」分组，一次查完 4 条，成本几乎相同。
> **先按「失败模式」分组，再逐组查**，而不是按对象逐个查。
>
> **「投影面 65% 可覆盖」这类乐观数字有陷阱**：它只统计 SELECT，漏了过滤。
> **能力面和错误面不是同一面**——少投影几列是「少几个字段」，
> 过滤断掉是「请求直接报错」。

### 六、下一轮

**不需要拍板**：决定 B 剩余 15 列（但 4 条整数过滤已覆盖了最高风险面）

**待拍板**（核心阻塞已全部量化完毕）：
- 决定 A（视图加宽 30 列）/ B（语义等价）/ C（25 列归属）
- `credential_id` 修法 + 会话族 `credential_id` 索引补齐
- 阻塞 #2 `is_final_success`、等价口径 (i)/(ii)/(iii)、`backlog_pending` 告警、
  actor 名单/cohort/§9.49.8/§9.48 口径、写链是否自 ensure 当月分区、
  790 提交的发布窗口与回滚方案

---

## 第六十二轮（§9.114）：去修 §9.113 的重影，撞出两个全新安装 500 + 一个地雷迁移

### 本轮做了什么

§9.113 把跨月重影定位到 `admin/turns_sessions.go:164-180`，但把「要不要推广
`MAX(partition_date)` 口径」留成产品决策。本轮去修，连带挖出三个缺陷。

**① 跨月重影已修（原本就知道的那个）**
- 门打**真实 handler 路径**（`Handler{db,secret}` → `handleTurnsSessions`），不自己拼 SQL。
- 变异实证：修复前同一 `session_id` 回 **2 行**（`active`/1 轮 + `closed`/2 轮），
  §9.113 的「重影各显示一半状态」真库复现。
- 修法：`turnsSessionsFinalizeWhere()` 无条件前置
  `s.partition_date = (SELECT MAX(x.partition_date) …)`，调用方过滤整体加括号。
- 三个选择都有理由：① 口径对齐**写侧已有的** `titlestore` `MAX(partition_date)`
  （不是新发明语义）；② 用普通 WHERE 而非 `DISTINCT ON` 子查询——后者是优化屏障，
  会阻断谓词下推且改变 LIMIT 页大小语义；③ 括号，不依赖 `buildTurnsSessionWhere` 的内部约定。
- 验证：变异红 → 修复绿，**且绿在真 installer 路径建出的全新安装库上**
  （`applied=213 failed=0`，`relations=445`，该库 2 个 `sessions` 分区）。`./admin/` 全包 79s 绿。

**② `session_title_states` 缺失 → 全新安装上会话列表恒 500（42P01）**
- 与 §9.101 的 467 **完全同类**，只是漏了 `550`/`551` 两个文件（自包含幂等，
  却在 embeddata 与 `StartupFiles` 里都没有）。
- 引用面比 467 大：**5 个生产文件**（`admin/turns_sessions.go`、`session_meta_view.go`、
  `session_turns_v2.go`、`internal/titlestore/store.go`、`db/db.go`）。
- 生产 252 实测**有**这张表 ⇒ 只影响全新安装，也正因如此一直隐形。
- 修法 = §9.101 五点同步（副本 `cmp` 字节一致 + `//go:embed` + `embeddedSQLFiles`
  + `StartupFiles` 登记 + `tsv` 用 `-update` 旗标重新生成，不手改）。

**③ `sessions.summary` 三列缺失（42703）+ 它底下的地雷迁移 456**
- 补完 ② 后门**又**红：`column s.summary does not exist`。缺的正好三列
  `summary` / `summary_model` / `summary_generated_at`，来自未登记的 `456`。
- **456 当前必然失败**：它的后置断言查 `table_schema='gateway'`，而同文件上面的
  `ALTER TABLE` 建在 `public`——文件自相矛盾。`gateway` schema 在**生产、权威全新安装库、
  本地测试库三处都不存在**，`NOT EXISTS` 恒真。实测
  `ERROR: public.sessions.last_full_payload_at not created`。
- ⇒ 这是**地雷迁移**：它不自己爆炸，它等着「有人终于去登记它」的那一刻爆炸，而那正是修复动作本身。
- 修法：迁移自身断言 `gateway`→`public`（`ALTER` 全是 `IF NOT EXISTS`，对已应用的库零 schema 变化）
  + 登记在 471 与 467 之间。456 头部说「430 必须先跑」而 430 仍未登记，但 baseline 已带三张表，
  实测能干净应用——**这句是门禁库重建实测的，不是推断**。
- ⚠️ 顺序纪律：**先复现 456 会失败，再登记它**。反过来的话结论会变成
  「启动链 failed=1」，根因被误判成我的登记顺序错。

### 根因量化（本轮新增量）

与生产 252 做 `public` schema 关系集合差（量具用**真 installer 路径**建的权威库，
不采信我自己早先搭的 `gw_fresh_test`；两者差集**零分歧**，故 `gw_fresh_test` 亦被验证为忠实）：

| 项 | 数 |
|---|---|
| 生产 252 关系数 | 856 |
| 真 installer 全新安装 | 635 |
| **生产有、全新安装缺** | **238** |
| 剔除序列/`bak_*`/日期后缀分区后的候选基表与视图 | 116 |
| 被生产 Go 代码（非测试）引用 | **106** |
| 无 `db.go` 自愈覆盖 | **102** |

**证据等级必须分开说**：
- `session_title_states`（42P01）与 `sessions.summary`（42703）= **实测**，查询真报错。
- 其余 100 项 = **结构性候选**，只做了静态交叉，**没逐条跑过代码路径，不宣称都坏**。

⚠️ **差点犯的错**：我第一反应把「275 个迁移未登记」当新系统性缺陷写进结论。
查证发现 `installer/internal/dbinit/startup_manifest_test.go:32-35` **早已写明**
「458 个迁移编号但只登记 200 个」（本轮实测 485 / 292 / 210）。
⇒ 值得报的是**代码级后果**，不是根因本身。**根因已知、后果未量化，这才是新增量。**

### 我自己的量具缺陷（记下来，因为它两次伪装成产品缺陷）

`sessionsPartitionLowerBounds` 漏了 `ORDER BY`。`pg_inherits` 返回顺序与月份无关，
`bounds[0]/[1]` 的「最新/次新」标签随机反转 ⇒ 我写进夹具的期望描述的是一条**根本没写的行**。
A 报「取到的不是最新分区行」、B 报「旧行从 OR 支路漏进来」，**两个都长得像产品缺陷**，
实际上去重是对的（两场景都只回 1 行）。修法：加 `ORDER BY lo DESC` + 夹具注释记踩坑。
**判据第一次运行前要自检桩件——这次是「桩件数据自检」。**

### 变更文件

- `admin/turns_sessions.go`（新增 `turnsSessionsLatestPartitionSQL` / `turnsSessionsFinalizeWhere` + handler 接线）
- `admin/turns_sessions_crossmonth_realdb_test.go`（新增，真库门 A/B 两子场景）
- `sql/migrations/startup/456_session_v2_display_columns.sql`（断言 `gateway`→`public`）
- `installer/internal/dbinit/runner.go`（登记 456/550/551，各带理由注释）
- `installer/cmd/llm-gw-installer/main.go`（3 组 `//go:embed` + 3 条 map）
- `installer/cmd/llm-gw-installer/embeddata/startup/{456,550,551}*.sql`（副本，`cmp` 字节一致）
- `sql/schema/installed_startup_migrations.tsv`（`-update` 重新生成，210→213）
- `docs/audit/2026-09-30-session-request-data-re-audit.md`（§9.114）
- 本 handoff

### 下一轮提示词

> 1. **本轮已推 `origin/main`**，起点 = 本轮最后一个提交 hash。
> 2. **待拍板（全部仍未决）**：决定 A（视图加宽 30 列）/ B（16 列语义等价）/ C（25 列归属）、
>    `credential_id` 修法（`::bigint` cast vs 改入参类型）+ 会话族 `credential_id` 索引、
>    阻塞 #2 `is_final_success`、等价口径 (i)/(ii)/(iii)、`backlog_pending` 告警、
>    actor 名单 (a)(b)(c)/cohort/§9.49.8/§9.48、写链是否自 ensure 当月分区、
>    790 提交 / 50 迁移文件的发布窗口与回滚方案。
> 3. **本轮新增待拍板**：那 **102 项**「生产有 / 全新安装缺 / 无自愈 / 被生产代码引用」的关系怎么处理。
>    三条路：(i) 逐个登记对应迁移（要先复现每个是否像 456 一样是地雷）；
>    (ii) 只修会话族（`session_*` / `request_*`）让核心 API 在全新安装上可跑；
>    (iii) 承认现状、补文档与告警。**注意 (i) 的成本：275 个未登记迁移里有多少能直接登记是未知的，
>    456 就是直接登记会炸的例子。**
> 4. **待查**：auto-route 自 2026-09-15 断流；A 群 6 条 / B 群 10 条 / `backlog_pending=1`。
> 5. **有意留的缺口**：全新安装无 `session_tags`（未登记 351，因 `GetSessionMetadata` 无生产调用方）——
>    ⚠️ 但本轮已证明 252 上**有** `session_tags`，而 §9.112 修的 `session_aggregator.go`
>    现在真的读它了。**这条「有意留的缺口」的前提可能已经不成立，下一轮必须重判。**
> 6. **工作区纪律不变**：本地 `main` 与 `origin/main` 分叉（合并需人工裁决，不用 rebase）；
>    共享工作区不跑 `git restore`、不 stash 他人工作。

---

## 第六十三轮（§9.115）：推翻我自己写进注释的一条结论——`session_tags` 不是「有意留的缺口」

### 本轮做了什么

两件不依赖拍板的收尾。其中一件**推翻了我自己此前写进代码注释和审计文档的结论**。

**① 先重判我上一轮自己提的怀疑——它站不住**
上一轮我在结论里写「`session_tags` 那条『有意留的缺口』的前提可能已不成立」。
这轮查了 `GetSessionMetadata`（§9.105 修的那条**读**路径）的全部引用：
只有 `session_metadata_test.go` ×4 + 我自己加的 PREPARE 门。**确实无生产调用方**，
§9.105 结论成立。

⚠️ 而我上一轮确实犯了个错：看到 `grep session_tags` 命中一堆生产文件
（`state_projector.go`/`tagger.go`/`cache_update_hook.go`/`approval_integration.go`）
就提了怀疑——**那些是写入方，不是这个读函数的调用方**。
**验证「某函数无生产调用方」必须把函数的引用和表的引用分开数。**

**② 但顺着写入方查，撞出一个真的问题（已修）**

```
cmd/gateway/approval_integration.go:137  proj := NewSessionStateProjector(...); SetStateProjector(proj)
domains/hooks/sessionaudit/cache_update_hook.go:126  if h.stateProjector != nil { … Project(ctx, proj) }
domains/analysis/state_projector.go:59   INSERT INTO session_tags …
```

全新安装上 `session_tags` 不存在 ⇒ INSERT 是 42P01 ⇒ `Project()` 返回 error
⇒ **hook 按设计吞掉**（best-effort）。⇒ 缺陷形态**不是 500，是静默能力丢失**：
v6 审计状态投影不进统一打标层，只有日志里一条 `WARN`，无告警。

**实测**（拿掉表后直接调 `Project()`）：`state_projector: failed 2/2 tags`。
**门必须打 `Project()` 而不是打 hook**——hook 吞掉错误，观测不到。

**③ 一个未登记文件 = 六张表**
351 建 `session_tags` / `session_request_summaries` / `session_embeddings` /
`session_clusters` / `session_cluster_members` / **`session_optimization_suggestions`**
（**6 张，数出来的**；我第一版写 5，还把这个错数字写进了注释和测试，已同步更正）。
⇒ §9.114.4 那 102 项里 **6 项来自这一个文件**。
**给「逐个登记 vs 只修会话族」这个待拍板项的量级信息：缺失不是 102 个独立问题，
而是若干文件各带一批。**

**④ 351 不可重跑——我刚把它变成活的，就顺手修了**
PG **没有 `CREATE POLICY IF NOT EXISTS`**，351 的 **12 条 policy 全无守卫**，
且整个文件在一个事务里 ⇒ 重跑时第二条起全部作废（我是**恢复测试库**时撞到的，
「恢复步骤也是测试」）。全新安装无害、installer 只应用一次也不受影响，
但既然我把它登记进链，这个性质就从潜伏变活了。修法：每条 `CREATE POLICY` 前置
`DROP POLICY IF EXISTS`（首次运行行为完全不变）。**验证=把文件连跑两次，两次都无错。**

### 门的设计教训

第一版把「6 张表都在」和「Project() 能写」写进**同一个函数**、用 `t.Fatalf`，
变异时红在表不存在那条——**`Project()` 的断言一次都没跑到**。
拆成两个独立测试后，变异时**两条一起红**：
`TestMigration351TablesExistOnRealDB` + `TestSessionStateProjectorWritesOnRealDB`。
⇒ `Fatalf` 短路掉了更要紧的那条断言。**「报了一个红」≠「只有一个问题」。**

### 变更文件

- `sql/migrations/startup/351_session_analytics_tables.sql`（12 条 policy 幂等守卫）
- `installer/cmd/llm-gw-installer/embeddata/startup/351_session_analytics_tables.sql`（副本，cmp 一致）
- `installer/internal/dbinit/runner.go`（登记 351 + 理由注释，含推翻 §9.105 的说明）
- `installer/cmd/llm-gw-installer/main.go`（`//go:embed` + map）
- `sql/schema/installed_startup_migrations.tsv`（`-update`，213→214）
- `domains/analysis/session_tags_realdb_test.go`（新增，两个测试）
- 审计文档 §9.115、本 handoff

### 验证

| 项 | 结果 |
|---|---|
| 启动链（真 installer 路径） | `applied=214 failed=0 missing=0`、`relations=451`（445→**451**，正好 +6） |
| 两个真库门 @ 权威全新安装库 | 全绿 |
| 变异（拿掉 `session_tags`） | **两条一起红** |
| 351 连跑两次 | 两次均无错误 |
| `installer` 全包 | 绿 |

### 下一轮提示词

> 1. **本轮已推 `origin/main`**，起点 = 本轮最后一个提交 hash。
> 2. **待拍板（新增，量级已变清晰）**：§9.114.4 那 102 项怎么处理。
>    现在知道它们**不是 102 个独立问题**，而是若干未登记文件各带一批
>    （351 一个就带 6 张，且已修）。建议下一步**按文件聚合**这 102 项
>    （找出还有哪些迁移文件能一次补一批），而不是逐表处理。
>    ⚠️ **登记前必须先复现每个文件能否干净应用**——456 就是直接登记会炸的。
> 3. **待拍板**（全部仍未决）：决定 A/B/C、`credential_id` 修法 + 索引、
>    `is_final_success`、等价口径 (i)/(ii)(iii)、`backlog_pending` 告警、
>    actor 名单/cohort/§9.49.8/§9.48、写链是否自 ensure 当月分区、
>    790 提交的发布窗口与回滚方案。
> 4. **待查**：按文件聚合扫描其余未登记迁移（485 文件 / 214 登记）。
> 5. **待查**：`public.sessions` 其余读点（`turn_logs_aggregator.go`、
>    `lite_retention_worker.go`、`session_detail_v2.go`）是否同一跨月未去重缺陷。
> 6. **待查**：auto-route 断流；A 群 6 条 / B 群 10 条 / `backlog_pending=1`。
> 7. **工作区纪律不变**：共享工作区不跑 `git restore`、不 stash 他人工作；
>    本地 `main` 与 `origin/main` 分叉，合并需人工裁决（不用 rebase）。

---

## 第六十四轮（§9.116）：收回 §9.114 的两个结论——一个说过头，一个方向错

### 本轮做了什么

§9.115 顺手修掉 351 后，我按自己写下的纪律（"按未登记迁移文件聚合"）去复核
§9.114.4 那份量化表。**复核发现两个结论都不能成立**，并已公开收回。

**① 收回「全新安装上会话列表恒 500」（550 缺失那条）**
漏查了运行时自愈：`db/db.go:4054 ensureSessionTitleStates` 由
`db/db.go:388 applyMigrationsOnce` 调用，**运行中的网关启动时会自己建这张表**。
准确表述：installer 交付的库缺这张表，跑起来的网关会补上，500 取决于启动时序。

⚠️ 但**只有 550 我查漏了，456 没有**：`public.sessions.summary` 三列在 Go 里
**无人自愈**（`session_summaries_schema.go` 的 `ADD COLUMN summary` 目标是
`session_summaries` 这张**另一张表**；全仓 `ALTER TABLE public.sessions` 只有一处命中，
在测试文件里）。⇒ **§9.114.3 的 42703 成立且更硬**。
登记 550/551 的动作仍正确，错的只是严重性描述。

**② 收回「102 项本仓安装缺口」——方向错**
§9.114.4 把"生产有 / 全新安装缺"直接当本仓安装缺口，**但没验证这个前提**。
生产是多子系统部署，一个网关的全新安装**合法地**比它少很多表。

按"DDL 在不在本仓"重新分类（116 候选）：

| 类别 | 数量 |
|---|---|
| **(B) 本仓完全没有其 DDL**（memora/openclaw/kxmemory/wiki/docs 等外部子系统） | **75** |
| **(C) Go 代码运行时自建**（启动自愈，不是缺口） | **16** |
| (A) DDL 在 `sql/migrations/` 里 | 26 |
| (A) 中无人自愈 | 13（其中 **6 项就是 351，本轮已修**） |
| (A) 中属 `local/` 迁移（641 = RedClaw/ACC 本地栈专用，本仓零引用） | 4 |
| **⇒ 本仓真正仍缺** | **3** |

验证方式：对 116 个名字在全仓 `*.sql`/`*.go`/`*.tmpl` 用宽松跨行模式搜
`CREATE TABLE|VIEW|MATERIALIZED VIEW`，并单独扫 Go 建表名（190 个）判自愈。
`projects`/`memories`/`documents` **全仓零命中**（`sql/schema/` 基线与
`deploy/sql/schemas/baseline/` 副本都零命中）——生产副本来自别的仓。

⚠️ **我上一轮的自愈检查本身有缺陷**：只 grep 了 4 个文件，
而本仓 Go 里共 190 个建表名 ⇒ 「102 项无自愈覆盖」**系统性高估**。
**自愈类判断必须全量枚举后再统计，挑几个文件 grep 不构成结论。**

**③ 把最后 3 项补了：426 / 470 / 472**（先实测再登记，沿用 456 的教训）
- `task_type_centroids` ← 426（`autoroute/embedding_classifier.go` 读）
- `cache_metrics` ← 470（`cachemetrics/recorder.go` 等三处读）
- `cache_metrics_default` ← 472（470 是分区表，需要 DEFAULT 分区接住月区间外的行）

三个文件均无 `information_schema` 后置断言、不引用自己没建的对象、干净应用。
472 只引用 `cache_metrics`（470 建）+ `pg_inherits`/`pg_tables` ⇒ 470 必须先于 472。

### 修正后的净结论

| 轮次 | 说法 | 现状 |
|---|---|---|
| §9.114.2 | 全新安装会话列表**恒** 500（550） | **收回「恒」**；登记动作仍正确 |
| §9.114.3 | 全新安装会话列表 500（`sessions.summary`） | **成立**，且无人自愈 |
| §9.114.4 | 102 项本仓安装缺口 | **方向错**：本仓责任实际 3 项（已修） |
| §9.115 | 351 = 六张表 | 成立，是"按文件聚合"这条线索的起点 |

**净结果**：本轮共登记 5 个迁移（456/550/551/351/426/470/472 中的 7 个文件，
启动链 210 → **217**），本仓责任范围内全新安装真正缺失的关系从 102 收敛到 0。

### 验证

| 项 | 结果 |
|---|---|
| 启动链（真 installer 路径） | `applied=217 failed=0 missing=0`（见提交说明） |
| `installer` 全包 | 绿 |
| 3 个新登记文件逐个试应用 | 均干净 |

### 下一轮提示词

> 1. **本轮已推 `origin/main`**，起点 = 本轮最后一个提交 hash。
> 2. **§9.114.4 的「102 项待拍板」这一项可以关闭**（已收敛为 0，见 §9.116）。
> 3. **待拍板（全部仍未决）**：决定 A（视图加宽 30 列）/ B（16 列语义等价）/
>    C（25 列归属）、`credential_id` 修法 + 会话族 `credential_id` 索引、
>    阻塞 #2 `is_final_success`、等价口径 (i)/(ii)/(iii)、`backlog_pending` 告警、
>    actor 名单 (a)(b)(c)/cohort/§9.49.8/§9.48、写链是否自 ensure 当月分区、
>    **790 提交 / 50 迁移文件的发布窗口与回滚方案**。
> 4. **待查**：`public.sessions` 其余读点（`turn_logs_aggregator.go`、
>    `lite_retention_worker.go`、`session_detail_v2.go`）是否同一跨月未去重缺陷。
> 5. **待查**：auto-route 自 2026-09-15 断流；A 群 6 条 / B 群 10 条 /
>    `backlog_pending=1`。
> 6. **方法论留档**（给后续任何量化任务）：①「量化」不等于「对」——
>    口径、证据等级、量具接线都要核，**最上面那层前提最容易漏**；
>    ② 自愈类判断必须全量枚举（我挑 4 个文件就得出了偏大的数）；
>    ③ 一个样本支持的归纳要标成假设（351 的 6 张表让我以为 102 项也是）；
>    ④ 「生产有的，全新安装就该有」这个前提本身要先验证。
> 7. **工作区纪律不变**：共享工作区不跑 `git restore`、不 stash 他人工作；
>    本地 `main` 与 `origin/main` 分叉，合并需人工裁决（不用 rebase）。

---

## 第六十五轮（§9.117）：把 §9.113.4 欠的账还掉——`public.sessions` 读点逐个判完

### 本轮做了什么

§9.113.4 点名了三个"不提 `partition_date`"的读点并声明"未逐个验证、不宣称它们也坏"。
本节把那笔账还掉，方法是**枚举式**（24 处 `FROM public.sessions` 全判）而不是抽查。

**① 先划清问题域：不是"分区表"，是"同一逻辑实体被按月复制"**
`public` 下有 30 张分区表，但判据不在"分不分区"，在**唯一键除 `partition_date` 之外
还剩什么**。252 实测跨月实体数 + 唯一键：

| 表 | 跨月 | 唯一键（去 partition_date） | 判定 |
|---|---|---|---|
| `sessions` | 413 | `(session_id)` | ⚠️ **同形**，§9.114 已修 |
| `session_turns` | 413 | `(tenant_id,session_id,turn_no)`/`(tenant_id,request_id)` | ✅ 跨月只是不同轮次 |
| `session_turn_details` | 413 | `(session_id,turn_no)` | ✅ |
| `session_bodies` | 1 | `(…,turn_no)` | ✅ |
| `session_censors` | 47 | 未查 | **未查，不宣称** |
| `session_tools` | 0 | — | 无风险 |

另两个同形的**部分唯一索引**：`uq_session_turns_final_success (tenant_id,session_id) WHERE is_final_success`
—— 252 **0 行**（与阻塞 #2 一致）；`uq_session_bodies_final_full (…,session_id) WHERE kind='final_full'`
—— 29 行 / **0 会话重复**。⇒ **结构允许、实测 0 例，不报缺陷。**

**② 主动收回一：`bg/lite_retention_worker.go` 不适用（我推错了）**
我读到它三处 `DELETE … WHERE EXISTS (SELECT 1 FROM sessions s WHERE s.id = session_turn_details.session_id …)`，
而 `sessions.id` 每月一个新值 ⇒ 判它有"过度删除跨月会话近期数据"的风险。
**错在**：`session_turn_details.session_id` 是 **text** 不是 `sessions.id`；而且那三处用
`?` 占位符 + `tx.ExecContext` ⇒ 那是 **SQLite**。**我在 PG 的分区性质上推了一整套故事。**
⇒ 记一条：**判读点风险前先确认它连的是哪个库。**

**③ 真正的潜伏缺陷（已修）：`AggregateAndFlush` 读改写前提失效**
`cmd/gateway/turn_logs_aggregator.go` 两条语句都**没有 `partition_date` 谓词**：
- `flushLockQuery … FOR UPDATE` 匹配 2+ 行，`QueryRow` 静默取其中一行当 merge 种子；
- `flushUpdateQuery` 把同一份 payload 写进**所有**行。
⇒ 用一行的累积值合并、覆盖其他行 = 静默内容丢失。设计注释里那条
"re-reads the column value the first one committed" 的不变量**假设了一个 (tenant,session) 只有一行**。

修法：两条**都**限定 `partition_date = (SELECT MAX(partition_date) …)`，与写侧 `titlestore`
和 `turnsSessionsFinalizeWhere` 同口径。**必须一起改**——只改其一会让读种子行与写目标行不一致。

**④ 诚实定性：潜伏，不是现网事故**
`session_turn_logs` **0 行**（聚合器输入）、`turn_logs_summary` 在 **177500/177500** 个会话里
**全 NULL**、413 个跨月会话里非空的行 **0**、两行摘要冲突的 **0**。
⇒ 门是**构造场景**，不是追认现网损失（已写进门头注释）。
⚠️ 这与阻塞 #2 的 `is_final_success`（0 行）是同一形态：**特性一直休眠**。

### 门（`cmd/gateway/turn_logs_aggregator_crossmonth_realdb_test.go`）

夹具：同一 `(tenant,session)` 两行且**两行摘要刻意不同**（若相同，写错行看不出来）+ 一条 turn 2 stage 行。
**变异实证**（去掉两处谓词）两条断言都报：
- 「最新分区行合并时丢了它自己的累积值：`turn_1.stages=[routing]`（期望 compression）」= 读种子取错行
- 「旧分区行被写入了 `turn_2`」= 写落到所有行

**我在这道门上错了四次夹具，每次都长得像产品缺陷**：
1. `stage='request'` 被 CHECK 拒（合法值只有 `routing|compression|injection_check|llm_call|output_check|response|cache_update`）——**枚举值要查，别猜**。
2. 漏给 `latency_ms` ⇒ `cannot scan NULL into *int`。查写侧 `turn_logs_writer.go:136` 确认它
   **恒计算 latencyMs、从不写 NULL** ⇒ **不是缺陷**，是夹具错。（schema 可空 + 扫描非指针确实脆，但生产写侧不可达。）
3. `oldSummary` 用了非法 `LogSummary` 形状 ⇒ `mergeSummaries` 解析失败 ⇒ **整份 existing 被丢弃**
   （已文档化的容错路径），门红在"丢了累积值"上。
4. 断言二原写"旧行**逐字节**不变" ⇒ 第一次就红：`turn_logs_summary` 是 `jsonb`，
   PG 存取规范化空白（`"a":1`→`"a": 1`），**没被改动的行也会红**。改成 flush 前后**语义**比较
   + 另加锐判据（旧行不得出现 `turn_2`）。
并把断言从 `Fatalf` 改成 `Errorf`：`Fatalf` 会短路掉第二条——这正是 §9.115.5 刚写进
351 那道门的教训，**隔一天撞上第二次**。

### 变更文件

- `cmd/gateway/turn_logs_aggregator.go`（两条 flush 语句加 `MAX(partition_date)` 限定 + 理由注释）
- `cmd/gateway/turn_logs_aggregator_crossmonth_realdb_test.go`（新增真库门）
- 审计文档 §9.117、本 handoff

### 验证

| 项 | 结果 |
|---|---|
| 跨月门（真库 + 真实 `AggregateAndFlush`） | 绿 |
| 变异（去掉两处谓词） | 红，**两条断言都报** |
| `go test ./cmd/gateway/` 全包 | 绿 3.8s（含既有 pgxmock SQL 形态门） |

### 下一轮提示词

> 1. **本轮已推 `origin/main`**，起点 = 本轮最后一个提交 hash。
> 2. **`public.sessions` 读点这条线到此闭合**：24 处全判完，已修 2 处
>    （主列表 §9.114、聚合器 §9.117）、已自守 2 处、不适用 1 处、其余安全。
>    `session_censors`（47 跨月）的唯一键**仍未查**，是这条线唯一的尾巴。
> 3. **待拍板（全部仍未决）**：决定 A/B/C、`credential_id` 修法 + 索引、
>    阻塞 #2 `is_final_success`、等价口径 (i)/(ii)(iii)、`backlog_pending` 告警、
>    actor 名单/cohort/§9.49.8/§9.48、写链是否自 ensure 当月分区、
>    **790 提交 / 50 迁移文件的发布窗口与回滚方案**。
> 4. **待查**：auto-route 断流；A 群 6 条 / B 群 10 条 / `backlog_pending=1`。
> 5. **方法论留档**：① 判跨月重复要看**唯一键**，不能看跨月计数
>    （`session_turns` 413 跨月但每行是不同轮次）；② 判读点风险前先确认
>    **连的是哪个库**（我在 SQLite 上推了整套 PG 故事）；③ 潜伏缺陷与现网事故
>    分开定性（输入 0 行就别写成"数据丢失"）；④ 门红了先怀疑夹具。
> 6. **工作区纪律不变**：共享工作区不跑 `git restore`、不 stash 他人工作；
>    本地 `main` 与 `origin/main` 分叉，合并需人工裁决（不用 rebase）。

---

## 第六十六轮（§9.118）：闭合 `session_censors`——跨月读点这条线结束

无代码改动，纯闭合记录。

### 结论

`session_censors` 表面**同形**（唯一键只有 PK `(id, partition_date)`，代理键），
252 实测跨分区重复元组：`(tenant,request_id)` **2** 个、
`(tenant,session,turn_no,placeholder,sensitive_type)` **9,371** 个（比 `sessions` 的 413 还多）。

**但不是缺陷**，两条判据：

1. **零消费者**。Go 侧 `FROM/JOIN session_censors`（排除 `_hot`、排除测试）零命中，
   `sql/` 侧零命中。但**不靠 grep 就下结论**——真库补查三处间接依赖：
   视图定义含它的 **0** 个；函数体含它的 2 个（`ensure_session_family_partitions`、
   `promote_session_censors_hot_to_partition`）；`pg_depend` 依赖方只有它自身的
   attrdef/class/constraint/policy/type。⇒ **本表是只写的脱敏审计存储**
   （`db_sink.go` 头注释自述：线上未配 `LLM_GATEWAY_SESSION_CENSOR_KEY`、
   `count(original_encrypted)=0`、全仓无解密读取方）。
2. **唯一会碰这些行的机制按代理键走**。`promote_session_censors_hot_to_partition` 的
   `ON CONFLICT (id, partition_date) DO NOTHING` 与 `DELETE … WHERE h.id = i.id`
   都用代理键 ⇒ 跨月重复既不产生假冲突也不产生漏删，幂等且正确。

⇒ 与 `sessions` 恰好相反：同样同形，`sessions` **有消费者**所以是缺陷，
`session_censors` **零消费者**所以不是。

### `public.sessions` 跨月重复全表判定（此线结束）

| 表 | 跨月 | 同形 | 有消费者 | 结论 |
|---|---|---|---|---|
| `sessions` | 413 | ✅ | ✅ | **已修 2 处**（§9.114 列表、§9.117 聚合） |
| `session_turns` | 413 | ❌ 键含 `turn_no` | ✅ | 不同轮次，非重复 |
| `session_turn_details` | 413 | ❌ 键含 `turn_no` | ✅ | 同上 |
| `session_bodies` | 1 | ❌ 键含 `turn_no` | ✅ | 同上 |
| `session_censors` | 47 | ✅ 键全是代理 | **❌ 零消费者** | **不是缺陷** |
| `session_tools` | 0 | — | ✅ | 无风险 |

两个同形部分唯一索引维持 §9.117.1 定性：结构允许、实测 0 例，只记形状不报缺陷。

### 下一轮提示词

> 1. **本轮已推 `origin/main`**（文档闭合，无代码改动），起点 = 本轮最后一个提交 hash。
> 2. **「跨月重复 / 一会话一行」这条线到此全量结束**（§9.112→§9.118，六轮）。
>    下轮不必再从 `public.sessions` 读点重新起头。
> 3. **待拍板（全部仍未决，且已无本会话可自行推进的项）**：
>    决定 A（视图加宽 30 列）/ B（16 列语义等价）/ C（25 列归属）、
>    `credential_id` 修法（`::bigint` cast vs 改入参类型）+ 会话族 `credential_id` 索引、
>    阻塞 #2 `is_final_success`（252 实测 0 行、特性休眠）、
>    等价口径 (i)/(ii)/(iii)、`backlog_pending` 告警、
>    actor 名单 (a)(b)(c)/cohort/§9.49.8/§9.48、
>    写链是否自 ensure 当月分区、
>    **790 提交 / 50 迁移文件的发布窗口与回滚方案**（所有修复均未部署）。
> 4. **待查**：auto-route 自 2026-09-15 断流；A 群 6 条 / B 群 10 条 / `backlog_pending=1`。
> 5. **方法论留档**：① 判跨月重复要看**唯一键**且必须同时问**消费者**——
>    `sessions` 与 `session_censors` 同形，一个有消费者一个没有，结论相反；
>    ② 「零消费者」不能靠 grep 断言，视图定义 / 函数体 / `pg_depend` 三层各验一次；
>    ③ 审计型表天生多行，实体表的「一行」判据不能套用。
> 6. **工作区纪律不变**：共享工作区不跑 `git restore`、不 stash 他人工作；
>    本地 `main` 与 `origin/main` 分叉，合并需人工裁决（不用 rebase）。

---

## 第六十七轮：把待办里三条「待查」结清，并产出一张决策清单

### 本轮做了什么

1. **结清三条挂了五轮以上的「待查」** —— 它们不是没查，是**标签已失效**：
   - 「A 群 6 条 / B 群 10 条」这两个标签**在仓内根本不存在**（`docs/` 与 handoff 全零命中）。
     实际所指：6 条 = §9.65 的 WARN 行，§9.65.7 已写明**证据被重启+轮转销毁、按构造不可归因**，
     真正遗留是它暴露的「这类缺失不可检测」；10 条 = §9.28 的 10 个 bodies 腿读点，
     依赖「正文来源」裁决 ⇒ 归入决定 C，不是调查项。
   - `backlog_pending=1`：同进程实测 `backlog_pending=0`、`mirrorBacklogCap=10000`、
     同期失败仅 90 ⇒ **FIFO 淘汰不可能发生**，那几行不在 backlog 里。
     真正的问题是「该 gauge 无告警规则」⇒ 转为决策 D6。
   - auto-route 断流：§9.70.5 **已完整归因**（断点 09-09、host 154、蓝绿降级后长驻进程
     traffic-only ⇒ decider 未装配、09-15 00:12 补丁 00:17 自愈）。**无需再查。**

   ⇒ **教训**：一条待办挂到第五轮时，该问的不是「怎么查」而是「**它指的是什么**」。
   指代物在文档里找不到，说明标签本身已腐烂；继续挂着只会假装还有事可做。

2. **产出决策清单** `docs/audit/2026-10-04-session-migration-decision-sheet.md`：
   D1–D10，每项含选项 / 证据 / 影响 / **推荐**，可一次性回「全按推荐」。

### 当前状态

`origin/main` = 本轮最后一个提交，双向 0 差。本仓责任内、全新安装上真正缺失的 schema 已
从 102 项收敛到 0（§9.116）；跨月重复这条线全量判完、已修 2 处（§9.114/§9.117）。
**所有修复均未部署**（生产 252 = `2b6d337b2`，落后 790+ 提交 / 50 个迁移文件）。

### 下一轮提示词

> **可自行推进的工作已清空。** 起点 = `origin/main` 当前提交。
> 1. 先读 `docs/audit/2026-10-04-session-migration-decision-sheet.md`——**所有待决项都在那**。
> 2. 用户回复后按 D1–D10 逐项落地。**D9（790 提交 / 50 迁移文件的发布窗口与回滚）建议优先**，
>    它决定 D1–D4 怎么上线；且 456 那次「直接登记就炸」的地雷先例说明
>    **新增迁移登记前必须先复现能否干净应用**。
> 3. 若用户未回复，**不要再自行找活干**——已连续三轮确认可自行推进项为空，
>    继续产出只会制造「看起来还有进展」的假象。

---

## 第六十八轮（§9.119）：新测出一类缺陷——启动链「不可重跑」，且现有门按定义看不见它

上一轮把工作标为受阻后，本轮没有停在报告上，而是回头验了一个**从没验过、
但决定 217 条迁移安全性**的问题：**installer 到底需不需要幂等？**

### 结论：需要，且是硬要求；而实测有 3 条不满足

**① `InitSchema` 无条件全量重放，无"已装过"探测**
`installer/internal/dbinit/runner.go:716`：先 00-prereqs/01-schema/02-seed，
再 for 循环跑完 217 条 `StartupFiles`，首错即返回。**没有"这条应用过吗"的判断**，
**版本表 `schema_migrations` 不参与跳过逻辑**（个别迁移只是 `INSERT … ON CONFLICT DO NOTHING`
记一笔），唯一调用点 `main.go:1338 runInstall` 里也**没有"这个库已装过吗"的探测**
（按 `already`/`已安装`/`existing`/`to_regclass`/`pg_database` 搜过，全零命中）。
⇒ 对已有库重跑 installer = 全量重放 + 首错中止。

**② 实测两遍**（真 installer 路径建库，照抄 `applySQL` 的
`--single-transaction` / `dbinit:no-transaction` 规则）：

| 遍 | 结果 |
|---|---|
| PASS 1 | **ok=217 fail=0** ← 现有门 `run-integration-gate.sh` 只覆盖这一遍 |
| PASS 2 | **ok=214 fail=3** |

3 条真实报错均为 `ERROR: cannot drop columns from view`：
`625_session_bodies_unified_explicit`（文件第 50 行）、
`637_session_bodies_unified_today_visible`（第 67 行）、
`656_auto_route_selections_hot`（第 174 行）。

**③ 机制**（查清了，不猜）
两个文件里**都没有 `DROP COLUMN` 字面量**（grep 零命中）。真因是 PostgreSQL 对
`CREATE OR REPLACE VIEW` 的硬限制——**替换后的定义不得比现存视图少列**。
625 建的是显式列清单，637 又用 `REPLACE` 换成另一个（更宽的）清单，
实测 PASS 1 结束后该视图 **13 列**：第一遍列数不减故通过，第二遍 625 想换回
自己那个更少的形态 ⇒ 失败（文件自带 `BEGIN` + `--single-transaction` ⇒ 整条回滚）。
⇒ **625 只在「视图处于 625 之前形态」的库上成立**，它的可重跑性依赖链上后续迁移
没有把它加宽 —— **是链的偶然顺序，不是文件属性**。
⚠️ 顺带 smell：`applySQL` 加 `--single-transaction` 而这些文件自带 `BEGIN;`，
每次应用都打 `WARNING: there is already a transaction in progress`。

**④ 与 351 的关系**（别当同一处）
351 的非幂等是 `CREATE POLICY` 无守卫（PG 无 `CREATE POLICY IF NOT EXISTS`），
**已于 §9.115.4 修掉**；这 3 条是 `REPLACE VIEW` 丢列，**成因不同、修法不同**。
同类要按**机制**分组，不按修法相似度归堆。

**⑤ 现有门为什么永远发现不了这一类**
`run-integration-gate.sh` 与 `startup_known_gaps.tsv` 的 ratchet 都是**单遍语义**，
而「不可重跑」按定义只有第二遍才显形。⇒ 量具的覆盖范围与被检验对象的真实用法不对齐。

### 新增决策 D11（已写进决策清单）

三条可选修法，都不是纯机械，**我没有擅自落地**：
- **E1** 625/637/656 改 `DROP VIEW IF EXISTS` + `CREATE VIEW`（需先枚举下游依赖；改已应用的生产迁移）
- **E2** 保留 `REPLACE`，把定义改成**并集**（放弃 625「显式列清单」的原始意图）
- **E3** 给 `InitSchema` 加"已初始化"守卫（**最治本**，去掉"重放"前提；但改 installer 行为契约）

推荐 **E3**。⚠️ 无论选哪个，**D9 都必须写明：发布流程不得包含「对已有库重跑 installer」**。

另附一条**不需决策的量具补强**（用户点头即可做）：把门扩成两遍语义，
专门守「注册链可重跑」。纯加法，不改产品行为。

### 下一轮提示词

> **可自行推进的工作已清空**（连续第二轮确认）。起点 = `origin/main` 当前提交。
> 1. 先读 `docs/audit/2026-10-04-session-migration-decision-sheet.md`——**D1–D11 全在那**。
> 2. **D9 与 D11 建议一起定**：D9 定发布流程，D11 定「能否对已有库重跑 installer」，
>    而 D9 的流程里恰好要写这一条。
> 3. 若用户要加「两遍语义门」，那是纯量具补强，直接做即可，无需再问。
> 4. 若用户不回复，**不要再自行找活干**。

---

## 第六十九轮（§9.124–§9.131）：把「可自行推进」跑到底，8 个提交全部落在**量具**上

> 起点 `4232b53b8^` = `213699cee`，终点 `cf90bdbe3` = `origin/main`（双向 0 差）。
> ⚠️ **本 handoff 上一版停在第六十八轮（§9.119），落后 11 节** ——
> 也就是说 §9.120–§9.131 期间我**只更新了审计文档、没更新本文件**。
> 这次补上，并把「补 handoff」列进本轮的固定收尾项（见 §69.6）。

### §69.1 这一轮实际做了什么（8 个提交，全是量具/文档，**零产品行为改动**）

| 提交 | 节 | 实质 |
|---|---|---|
| `4232b53b8` | §9.124 | 公开收回 §9.119 两个错误：3 条 → **11 个文件**；重跑 installer 第一现场不是 625 而是 `01-schema.sql`（**1665 条 ERROR**）。门扩成**两遍语义**，两份 ratchet（`startup_rerun_known_gaps.tsv` 11 条按 6 机制分组 / `baseline_rerun_budget.tsv` 错误行数上界 1665） |
| `c72cb732d` | §9.125 | 11 文件 = **18 条真实独立错误**（40 原始行中 22 条是显式 `BEGIN;` 造成的 25P02 级联噪声）。全链 88 条 `CREATE POLICY` **只有 520 一条非幂等**（证伪我「还有别的实例」的推测）；520 是**死守卫** |
| `30d29aecc` | §9.126 | 证明搜索读侧**不是** schema 缺口：`session_turns_with_current_month` 确无 `search_text`，但灰度开关用的 `SessionFamilyTurnsSourceSQL()` 内联投影含 `t.search_text`。新增**真库往返门**（121 字节逐字节相等） |
| `c52365593` | §9.127 | 镜像排除**枚举为 6 条**（策略 A/B/C + 运行期 D/E/F），不是我手读得出的 3 条。新增枚举门 + 双向变异取证 |
| `b1c3d4b2f` | §9.128 | **生产 252 实测**：近窗 v1 7,236 行，会话族无孪生 **4,369（60.38%）**；A 非终态 6 / B 探针 2,637 / C 内部回环 1,727 / **无法解释 0**。真实用户流量覆盖 **99.826%** |
| `1c4908538` | §9.129 | 把 NULL 当成零；否掉「`cost_display` 全 NULL 是缺陷」 |
| `0020cfe67` | §9.130 | **更正 §9.129**：ledger 是 `request_logs` 的 **1:1 全量镜像**，「id 空间不通」**是错的**。修正后 B/C 命中均 100%，但 `cost_usd` 填充率 0/0/**1.7%** |
| `cf90bdbe3` | §9.131 | 成本链完整机制：**不是后置结算，是「没有价格就没有成本」**。另收一处编号撞车（见 §69.5） |

⚠️ 一句话总结：**这一轮没有任何一个数字是「修好的功能」**，
全部是「把还不知道的东西变成已知的」+「把量具的盲区补上」。
**审计面已连续两轮确认「可自行推进的工作」耗尽**，剩下的都要你拍板。

### §69.2 退役 `request_logs` 的现状（这一轮新测出来的，原先文档里没有）

生产 252（仍是 `2b6d337b2`，**所有修复均未部署**）近窗实测：

- v1 侧 7,236 行中 **60.38% 在会话族里没有孪生**，
  其中 **100% 被三条镜像排除策略解释**（A 非终态 0.14% / B 探针 2,637 / C 内部回环 1,727），
  **无法解释的丢失为 0**。
- ⇒ **「停写 v1」不是退役的收尾动作**：它只消 0.14%，
  而 **B（探针）与 C（内部回环）按设计永远不进会话族**。
  **退役的前提是先决定 B/C 的归属，而不是先停写。**
- 真实用户流量侧，会话族覆盖率 **99.826%** —— 缺的不是质量，是那三类**按设计排除**的行。

### §69.3 成本这一维：这一轮把问题**换了**，不是答了

- §9.130/§9.131 查清了机制：占位 INSERT + 终态 UPDATE（`cost_usd = COALESCE(..)`，两条分支都有），
  补全逻辑**正确**；成本来自 `handler.go:6619` 传候选的 per-1M 价格；
  `AssignRequestCost` 三条 nil 返回 ⇒ **没有价格就没有成本**。
- 实测：有凭证行 `cost_usd` 填充率 **1.70%**，无凭证 0.00%，在用 **201 个模型**。
- ⚠️ 同一份「无价格」在路由里是**最大惩罚分**（`strategy_cost.go:12`「未知价格不是免费」，
  订阅制凭据 `BillingRound==1` 豁免为 0）⇒
  「罕见地未知」与「普遍地未知」在 cost-optimized 策略下后果不同：
  **只能指出，不能断言它当前是否造成错误选路**（需路由决策日志，本轮没有）。
- ⇒ **D7 的前置从「定成本口径」换成「把在用模型的价格补齐」**，
  否则「改动前后成本一致」**没有可比基线**。
- ⚠️ **仍然验不了的一格**：C 类那 0 条成本记录，是「没配价」还是「订阅制按设计为 0」？
  `usage_ledger` 只有 20 列、**没有 `billing_mode`**，从 ledger 侧判不出来，
  需从 provider/credential 配置侧查。

### §69.4 这一轮欠下的账（我自己的，列出来而不是藏起来）

1. **本 handoff 落后 11 节**（§9.120–§9.131 期间未更新）——本节已补，但**说明我此前把「更新 handoff」当成了可选项**。已改：此后每次推审计章节，同批更新本文件。
2. **编号撞车**：决策表 D3「决定 C」（25 个**列**）与 §9.128 起的「C 类」（**行**分类）
   在同一份文档里共用字母 `C`，已致 §9.130.4 需要重读。已在 §9.131.7 定命名约定。
3. **`cost_optimized` 是否已在错误选路** —— 指出未证伪，缺路由决策日志。
4. **生产仍未部署**（252 落后 `origin/main`；⚠️ 此处原写「19+ 提交」是**错数**，第七十轮已更正为 §9.107.3 实测的 **790 个提交**），本轮所有结论都只在**文档与量具**里生效。

### §69.5 命名约定（§9.131.7 起生效，读旧章节时按此对照）

- **行分类**一律写「A 类 / B 类 / C 类」并附类别名（非终态 / 探针 / 内部回环）；
- **决策项**一律写「D1–D11」；
- **不再单写「决定 A / 决定 B / 决定 C」** —— 那是 D1/D2/D3 的旧标签，
  仅在引用决策表标题原文时保留，并同时带 D 编号。
- ⚠️ 文档**章节号**另有撞车：`§9.120–§9.123` 已被 `feat/820-abandoned-turn`
  的 4 节占用（见审计文档文首横幅）。

### §69.6 本轮方法教训（比结论更耐用）

- **能自己查的口径问题，不要变成用户的问题。** §9.130 结尾我把
  「成本是实时算还是后置结算」当成决策抛出去，而它读三行代码就有答案。
- **同一个符号在一个文档里出现两次，要当缺陷处理**，不能让读者「自会分辨」。
- **「这一列空着」要先问「它由什么算出来」，再问「为什么没算出来」** ——
  本轮把 98% 空从三个候选收敛到「定价缺失」一个，靠的就是读 `AssignRequestCost` 的三条 nil 返回。
- **同一个「缺失」可能同时喂给两个决策**：价格缺失既让成本列空白，又在路由里当最大惩罚。
  只看前者会漏掉后者。
- **跨源比对前先量两源各自的 `(min_ts, max_ts, rows)` 再选重叠时段** —— 本会话三次栽在这类错误上。
- **同一个 0，对照组无信号时是废的，对照组有信号时才是结论**（§9.129 vs §9.130）。
- **更正要推到文档里，不能只在对话里说**（§9.129 已上 main，读者会读到那个错结论）。
- **变异必须让包仍能编译**；init panic / 编译失败**不算**变异证据。
- **`-run` 过滤后的「全绿」必须同时报出实际执行条数**。

### §69.7 下一轮提示词

> **可自行推进的工作已连续两轮确认耗尽**（本轮 8 个提交全是量具与文档）。
> 起点 = `origin/main` = `cf90bdbe3`。
>
> 1. **先读 `docs/audit/2026-10-04-session-migration-decision-sheet.md` —— D1–D11 全在那**；
>    再读审计文档 **§9.128 / §9.130 / §9.131**（本轮三节新证据直接改写了 D3 与 D7 的权重）。
> 2. **最关键仍是 D3**：C 类每天 **1,345 行 / 386 万 token**，
>    会话族与 ledger **两处都无成本记录** ⇒
>    排除它的代价是**丢 token 计量**，不是「丢计费事实」（那本来就没有）。
>    同时要定 **B（探针）2,641 行/2 天**的归属 —— 它按设计永不进会话族。
>    ⇒ **退役的前提是先定 B/C 归属，停写 v1 只消 0.14%。**
> 3. **D9 建议优先定**：发布流程**不得包含「对已有库重跑 installer」**，
>    且**回滚不得依赖重跑 installer**（§9.124/§9.125 已证 baseline 有 1665 条 ERROR）。
> 4. **D11 与 D9 一起定**：推荐 **E3**（给 `InitSchema` 加「已初始化」守卫）或 **E3+E1′**；
>    ⚠️ **E1/E2 都不解决那 1665 条**。
> 5. **D7 的前置已换**（§9.131）：不是定口径，是**补齐在用 201 个模型的 per-1M 定价**；
>    「C 类 0 成本是缺价还是订阅制豁免」仍需从 provider/credential 配置侧查。
> 6. **若用户不回复，不要再自行找活干** —— 但可以先做「量具补强」类纯加法
>    （例如 `usage_ledger` 是否值得加 `billing_mode` 列以让 §69.3 那一格可判；
>    以及路由决策日志能否导出，用于验证 `cost_optimized` 是否已错误选路）。

## 第七十轮（§9.132–§9.149）：29 个提交仍是**零产品改动**，但挖出本项目最重的一条事实

> 起点 `21f09d4ce^` = `cf90bdbe3`，终点 `361905ec5` = `origin/main`（双向 0 差）。
> ⚠️ **本 handoff 上一版停在第六十九轮（§9.124–§9.131），落后 18 节** ——
> §9.132–§9.149 期间我**只更新了审计文档与决策表、没更新本文件**，这是连续第二次。
> 本节补上；「补 handoff」自第六十九轮起已列进固定收尾项，本轮是它第一次被真正执行。

### §70.1 这一轮的形状：29 个提交，**全部是文档，零产品行为改动**

`4c4f5bdfe`(§9.132) → `423d5272d`(§9.133) → `070168ba7`(§9.134) → `835c845c1`(§9.135) →
`2578e516b`(§9.136) → `a531aabbe`(§9.137) → `8a8554545`(§9.138) → `d9e0780f9`(§9.139) →
`316ac33a9`(§9.140) → `6be438bdd`(§9.141) → `d43fe9526`(§9.142) → `646e1d42f`(§9.143) →
`342abaeaf`(§9.144) → `e62490213`(§9.145) → `fba9e46b4`(§9.146) → `76506309f`(§9.147) →
`3b6da2e81`(§9.148) → `cb00f8fb0`(§9.149)，
其余 11 个是决策表/handoff 的同步更新。

⚠️ 与第六十九轮同一句结论，但这次要说得更准：
**不是「审计面已耗尽」，而是「剩下的都是需要拍板或需要生产访问的」**。
本轮 18 节里，有 **11 节**的净产出是「把一条我此前写进文档的结论判掉或降级」。

### §70.2 本轮最重的一条：**停写门已经被真实打开过，4 天 `request_logs` 精确为 0**（§9.149）

本地库逐日对照，两张表同窗口：

| 日期 | `request_logs` | `session_turns` | 其中无 v1 孪生 |
|---|---:|---:|---:|
| 09-06 | 33,394 | 15,991 | 2,370 |
| **09-07 ~ 09-10** | **0 / 0 / 0 / 0** | 70,757 / 25,795 / 28,945 / 29,585 | **同左（全部无孪生）** |
| 09-11 | 2,353 | 10,478 | 9,655 |
| 09-12 起 | 恢复 | 恢复 | **0** |

- 同四天 `session_turns` **照常写入 155,082 行**；机制由代码结构决定：
  `insertRequestLog`（`client.go`）里 **ledger 无条件写 / v1 受 `requestLogsWriteEnabled()` 门控 / 镜像无条件触发**。
- 那 52,205 行**全部 `source_kind='live'`**（回填工具写的是 `'backfill'`）⇒ **是在线路径产的**。
- ⇒ **退役的「停写」这一步已被证明可行**（v1 归零而会话族持续增长）；
  同时**它一打开就丢数据且不可逆**（那 4 天 v1 永久缺失，session 侧多出 155,082 行无孪生）。
- ⚠️ **边界**：这是**本地库**。生产是否也发生过，**我没有验证过**，不得写进任何结论。
  但 D9 第三条**无论生产有没有发生过都成立** —— 它由代码结构决定，不由数据决定。

### §70.3 退役的真实成本被换掉了两次

| 轮次 | 当时的说法 | 现在（§9.142/§9.141/§9.146/§9.148/§9.149） |
|---|---|---|
| 第六十七轮 | 「要搬 217 万行数据」 | 不搬；**停写 + 改写 4 条策略 + 删表** |
| 第六十九轮 | 「停写只消 0.14%，B/C 才是真问题」 | 成立，但 B/C 之外**新增了第三类**：**停写窗口本身会造出 15 万行无孪生 turn** |
| 本轮新增 | —— | 风险方向**不是「丢数据」，是「继承噪声」**：`rate_limited` 的 **394,614 行（占 v1 全部行 18.1%）早已被镜像进 `session_turns`** |

### §70.4 凭证归属：机制判掉了，权威源定了（D7-e）

- **现象**：生产 7.97%（本地 3.80%）两表 `credential_id` 不一致。
- **机制（§9.142 判掉）**：t0 `insertRequestLog` 同事务写两表 = A₀（开始时凭证）；
  终态 `updateRequestLog` 的 v1 UPDATE（`client.go:2168`，`credential_id = COALESCE($4, …)`，
  **SET 清单不含 `ts`**）把 v1 改成 A₁（终态凭证），ledger 的 UPDATE 不含该列，留在 A₀。
- **判别实测**：能判别的 **641 行上，`session_turns` 与 v1 100% 一致、与 ledger 0% 一致**；
  两侧归属**都内部自洽**（644/644）。
- ⇒ **`session_turns.credential_id` = 终态归属**（与成本实算用的 `result.Candidate` 同侧），
  **推荐作权威源**；`usage_ledger.credential_id` 站非终态侧，不推荐。
- ⇒ **§9.140.4「必须先把终态归属搬进 `session_*`」已收回** —— 它**早就存在**，不需要搬。

### §70.5 owner 隔离：从「退役阻断项」降级为「潜伏依赖」，但配套的权限缺陷更值得立项

- **§9.137**：`session_*` 的 `*_owner_filter`（RESTRICTIVE）策略表达式内部 UNION ALL 读 `request_logs`
  取最早行 `owner_user`（迁移 457/526；526 还留着门 `p.qual::TEXT ~ 'request_logs'`）。
- **§9.138 降级**：唯一有访问权的 `llm_gateway` 是 **`rolsuper=t` + `bypassrls=t`** ⇒ RLS 对它永不生效；
  其余 **17 个可登录角色对四张关键表零授权** ⇒ **今天对任何角色都不生效**。
- **§9.139 实测（scratch 库 + 探针角色）**：
  - `DROP TABLE request_logs` 被 PG 以 `other objects depend on it` **拒绝** ⇒ **响亮失败，不静默损坏**；
  - 非特权角色查 `session_turns` 得 `permission denied` ⇒ **一旦给授权，每查必报错**。
- **整改三步（已有现成源，不需要新增字段）**：`sessions.owner_user` **已存在** →
  改写 4 条 `*_owner_filter` 策略 → 最后删表。
- ⚠️ 顺带立项：**应用角色是超级用户**（与 821 审计里「绿色 e2e 对 RLS 零信息量」是同一条事实）。

### §70.6 成本：这一轮把问题从「补定价」改成了「先定失败流量算不算钱」

- **§9.132**：C 类那 1,779 行 **100% 单价 NULL 且 `pricing_updated_at` 从未写过** ⇒ **从未定价**，
  **不是**订阅制豁免（对照：订阅制行 `pricing_updated_at` 非空）。
  且**补齐定价会凭空造出成本行** —— `AssignRequestCost` **没有 `billing_mode` 入参**（`usage.go:252`）。
- **§9.133**：单价有**两个来源** `COALESCE(mo.unit_price, pricing_plans.plan_json)`，
  我原先只量了第一个；**第二个在生产 0 命中** ⇒ 结论侥幸成立，但**当时没有证据支撑**。
- **§9.145（降级 §9.143/§9.144）**：全量 217 万行里 **成本几乎只记在 success 行** ——
  `failure` 1,453,695 行有成本 **31 行（0.002%）**、`rate_limited` 403,031 行 **0**、
  `success` 316,673 行 **18,830（5.9%）**。⇒ 此前「UPSERT 守卫挡住 enrichment」的归因**判掉**
  （全量 87% 的 failure 行有凭证，守卫不是判别式）。
- **§9.146**：`rate_limited` 是**模型目录枚举扫描**（key 727 枚举 421 个模型名 / 252,902 行，
  前四个 key 占 85.9%），**从未联系上游**（`outbound_model`/`provider_id`/`completion_tokens` 全 0，
  均值延迟 51.6ms）⇒ **成本为 0 是正确的**。
- ⇒ **D7 的前置第二次换**：不是「补齐定价」，而是**「失败 + 限流约 185 万行/30 天（占 86%）算不算钱」**。
  不定这条，「改动前后成本一致」在 **86% 的流量上根本无从比较**。

### §70.7 噪声：退役后无法只用 `session_*` 现有列把它认出来（§9.147–§9.148）

- 真正该看的口径是 `provider` **与** `credential_id` **双空 = 630,143 行**，而它**不是同一种东西**：

| 段 | 行数 | 占双空 | 性质 |
|---|---:|---:|---|
| `rate_limited` 扫描噪声 | 394,614 | 62.6% | 一行一个合成会话；**已在会话族里** |
| `failure` 真实失败请求 | 183,324 | 29.1% | **183,299 有 token，是真实消耗** |
| 无 v1 孪生 | 52,205 | 8.3% | **= §70.2 的停写窗口**（已定位成因） |

- 扫描噪声在 `model` 上 **100% 有值**，但**唯一有差异的列不能干净筛**：
  最好的代理（`model` 为空）**误报率 41.2%**。
- ⇒ 三个选项：**① 接受** / **② 补 `turn_kind` 标记列**（可 100% 标三种，我倾向此项但**未拍板**）/
  **③ 改镜像排除**（**只覆盖 62.6%**）。

### §70.8 这一轮我自己收回/降级的结论（不藏）

| 原结论 | 处置 | 出处 |
|---|---|---|
| §9.134 的机制（v1 被第二次写覆盖） | **落点说错，机制本身经 §9.142 判掉后基本正确** | §9.135 / §9.142 |
| 「`ts` 相等 ⇒ 同一事务」 | **收回**（SET 清单不含 `ts`，相等说明不了任何事） | §9.142.1 |
| §9.140.4「必须先把终态归属搬进 `session_*`」 | **收回**（`session_turns` 已有终态值） | §9.141 |
| §9.137「RLS 是退役硬前置」 | **降级为潜伏依赖**（今天对任何角色都不生效） | §9.138 |
| §9.143/§9.144「UPSERT 守卫挡住终态数据」 | **降级**（守卫不是判别式） | §9.145 |
| §9.148.3「52,205 行成因待查」 | **已解** = 停写窗口 | §9.149.5 |
| §9.132.0「量具先说清楚，因为这一节我先犯了一次错」 | 保留 | §9.132.0 |

### §70.9 本轮方法教训（比结论更耐用）

- **只量了优先级更高的那个来源就敢下结论 = 一次靠运气正确的断言**（§9.133）。
  本次「`pricing_plans` 兜底 0 命中」救了我，但**下结论时并没有证据**。
  ⇒ 结论里出现的每个列名，都要能指着它的**计算表达式**。
- **测的是生产、读的是 main，混着推会得出「矛盾」**（§9.135.3）——
  生产跑的是 `2b6d337b2`，落后 `origin/main`（实测 **898 个提交**，非「19+」，见第七十轮更正）。
- **「不生效的依赖」要先问「谁被它约束」**（§9.138）——
  我漏问这一句，就把「潜伏依赖」写成了「硬阻断」。
- **一个「待查」桶的成因，有时就藏在它的时间分布里**（§9.149）——
  按 `session_id` 前缀分组看到的是「上千个极小会话」，**按天分组立刻看到「4 天、之后归零」**。
  ⇒ **分组维度选错，会把一个事件看成一片噪声**；试第二、第三个维度常比深挖第一维度更快。
- **代码结构能回答的问题不必等数据**：「门控位置 + 镜像无条件」两行代码直接预测了停写期的形态，
  数据（4 天 0 行 + session 照常）与之**一致** ⇒ 这条因果不需要补实验。
- **判据的严重性要能被自己的数据否掉**：§9.143/§9.144 靠一个干净指纹立起来，
  §9.145 扩窗后发现「全量 87% 的 failure 行有凭证」⇒ 指纹不是判别式，**降级**。
- **扩窗会改变绝对值但不改变比例** ⇒ 结论要分开写「绝对值偏低」与「比例方向不变」（§9.149.5）。
- **一个错数会沿「摘要 → 下一轮 handoff → 再下一轮」传染**（本轮自查抓到）：
  审计文档 §9.107.3 实测生产落后 **790 个提交**，而我在 §9.135.3 与连续两轮 handoff 里
  写成「**19+ 提交**」—— 差 40 倍。根因是**我复述的是摘要里的数字，没有回去指它的出处**。
  ⇒ **凡是要复述一个数字，先 grep 它的原始出处**；发现错数要**在错的地方就地更正**，
  不能只在最新一轮加一句说明（否则旧章节继续误导读者）。

### §70.10 这一轮欠下的账

1. **本 handoff 落后 18 节**（§9.132–§9.149 未更新）——本节已补，**连续第二次**，已复发两次。
2. **§9.149 的停写窗口只在本地库验证** ⇒ 需一条只读逐日 `count(*)` 上生产 252（**待你授权**）。
3. **生产仍是 `2b6d337b2`（落后 `origin/main` **898 个提交**；⚠️ 我此前两处写「19+ 提交」是**错数**，已在本节更正）**，本轮全部结论只存在于文档，一行修复都没部署。**
   ⚠️ 「生产仍是 `2b6d337b2`」的**证据时点是 §9.107.2**，此后本会话**未再复验生产** —— 应读作「本会话最后一次实测」，不是「刚刚确认」。
4. D1–D11 + D7-e + 「审计新发现」段**全部待拍板**。

### §70.11 下一轮提示词

> **起点 = `origin/main` = `361905ec5`。** 本 handoff 已补到第七十轮，与审计文档同步到 §9.149。
> 生产仍是 `2b6d337b2`，**本轮全部结论都只在文档里**。
>
> 1. **先读决策表 `docs/audit/2026-10-04-session-migration-decision-sheet.md`** ——
>    D1–D11 + **D7-e**（凭证权威源）+「审计新发现」段 + D9 三条强制条款都在那。
>    再读审计文档 **§9.141 / §9.145 / §9.146 / §9.147 / §9.149**（本轮五节改写了 D3/D7/D9 的权重）。
> 2. **优先回「双空 turn 三类怎么处理」**（§70.7 表）——它同时决定 D3/D5/D7 的权重。
>    我倾向 **② 补 `turn_kind` 标记列**（可 100% 标三种），**③ 只覆盖 62.6%**、**① 把问题留给下游**。
> 3. **D7 必须先定「失败流量算不算钱」**：失败 + 限流约 **185 万行/30 天（占 86%）**目前免费；
>    不定这条，「改动前后成本一致」在 86% 流量上**无从比较**。
> 4. **D9 三条强制条款**：① 发布不得含「对已有库重跑 installer」；② owner 过滤策略改写是退役硬前置
>    （今天**不阻断**，是**潜伏依赖**）；③ **停写门已验证可用、打开即丢数据且不可逆**，
>    必须有开启/回滚责任人 + 「v1 当日行数」低阈值告警。
> 5. **D7-e 建议直接采纳**：权威源 = **`session_turns.credential_id`**（终态 A₁，
>    与成本实算 `result.Candidate` 同侧）；`usage_ledger.credential_id` 站非终态侧 A₀。
> 6. **可选纯加法（不需拍板）**：`RowsAffected == 0` 指标 ——
>    v1 的两条写入语句命中 0 行时**目前没有任何信号**（§9.143 那 3,699 行就是这种静默）。
> 7. **若仍不回复**：不要再自行找活干；只剩「生产只读逐日 `count(*)` 确认停写窗口」需要你点头。

### §70.12 第七十一轮补记：两条「暂定采纳」+ 一个被自己推翻的推荐

> 起点 `20a3daccc` = 第七十轮推送后的 `origin/main`。本段是**同日追加**，
> 因为 §70.11 之后的问卷回执改变了状态。

**问卷是超时自动采纳的，不是你的显式确认**（`responseSource=automatic_timeout`）：

| 项 | 收到的 | 我的处置 |
|---|---|---|
| 双空 turn 三类 | ② 补 `turn_kind`，可 100% 标三种 | **采纳方案，但推翻「三种」这个前提** → 决策表新增 **D7-g** |
| 失败流量算不算钱 | 不计费，但把 `rate_limited` 显式标注 | 采纳为口径 → 决策表新增 **D7-f**（标**暂定**） |
| 生产 252 只读 `count(*)` | 授权 | ❌ **不执行** —— 超时采纳不等于授权碰生产 |

**§9.150 的净产出（一句话）**：
**「双空 turn 有三类」是错的 —— 第三类不是一种流量，是 v1 停写窗口的残影。**

| 检验 | 结果 |
|---|---|
| 三类零残余 | ✅ 成立（394,614 / 183,324 / 52,205 = 630,143） |
| 第三类时间窗 | **09-06→09-11**，单独占住 §9.149 的停写窗口 |
| 每会话行数 | **100.17**，与 `rate_limited` 段 100.00 **同形** |
| 模型集重合 | **52,186 / 52,205 = 99.96%** 落在扫描集内；19 行例外全在窗口内 |

⇒ **`turn_kind` 应是两值**（`rate_limited` / `failure`），
**第三个标签依赖 `request_logs` 存在，删表后指向的东西就没了** ——
「能标三类」不等于「该有三类」。
⇒ **`rate_limited` 真实规模 394,614 → ≈446,819**（占 `session_turns` 全体 **26.5%**），
**比我此前报的大 13%**，D3/D5 权重需重算。

**两个顺带抓到的缺陷**：

1. **`entry.RequestStatus` 一直带着 `rate_limited`，却在落库时被丢弃** ——
   `hook.go:1082` 在读它，`entryToProcessedRequest` 没带进 `ProcessedRequest`，
   `session_turns` **没有 `request_status` 列**。
   ⇒ 标注 = **写入时原样落库**，**不需推断、不需回填**。D7-f/D7-g 的落点都在这里。
2. **§9.148.1 表「无 v1 孪生段有 token = 0」是错值** ——
   那是 join v1 后取 `rl.total_tokens` 的**连接产物**；实测会话侧 **52,203 行有 token**。已就地更正。
   ⚠️ 顺带更正 §9.149 一处措辞：「镜像无条件触发」只对 `firePersistedHooks` 成立，
   **正文镜像 `mirrorRequestBodies` 是受停写门控的**，且 `degraded` 分支在 hooks 之前早退。

**另登记一项不可恢复缺陷**：审计文档有 **4 个 U+FFFD**（行 4471 / 6674），
两个引入提交**自身就已损坏** ⇒ **原文从未以完好形态进过 git**，`git log -L` 无法恢复。
**我没有猜字**（§9.150.6）—— 猜一个读得通的词塞进审计结论，比留两个方块危险。
⚠️ 登记时**刻意不复刻坏字节**，否则「全文 U+FFFD 计数」这个体检指标自己翻倍（实测 4 → 8）。

### §70.13 下一轮提示词（覆盖 §70.11）

> **起点 = `origin/main` = `20a3daccc` + 本轮提交。**
> 1. **D7-f / D7-g 是「暂定采纳」**（问卷超时自动采纳，非显式确认）。
>    需你一句显式确认才转正。**D7-g 的方案已被 §9.150 更正为两值**，不是三值。
> 2. **仍未决**：非双空行要不要 `turn_kind`；D1/D2/D4/D5/D6/D7-a~d/D7-e/D8/D9/D10/D11；
>    `RowsAffected==0` 纯加法指标是否做。
> 3. **生产 252 只读验证未做**（我拒绝了超时采纳作为授权）。
>    要做请显式说一声；只需一条逐日 `count(*)`。
> 4. **可做的纯加法**（不需拍板）：把 `entry.RequestStatus` 落进 `session_turns` ——
>    它是 D7-f/D7-g 的共同前置，且**写入时即正确**。
> 5. ⚠️ **不要**在生产上验证「噪声规模 ≈446,819」—— 那是本地库口径。

### §70.14 第七十二轮补记：把 §9.150 推到全体 + 一个**排期阻断**（不是拍板阻断）

> 起点 `5ba14834e`。同日追加，状态没有变化（仍在等你显式确认 D7-f / D7-g）。

**§9.151 的产出**：

| 项 | 结果 |
|---|---|
| 四象限 | 1,016,959 / 630,143 / 40,528 / **0** ⇒ **`provider` 有值 ⇒ `credential_id` 必有值**（第四象限 0 行，此前从未记录） |
| **D7-g 开放问题** | ✅ **不需要扩到非双空行** —— `rate_limited` **394,614 行 100% 在双空象限内，界外 0 行** |
| 但失败相反 | `failure` 全体 **905,222**，双空内仅 183,324 ⇒ **双空判据对失败的召回只有 20.3%** |
| 40,528 那块 | **99.2% 是 `failure`**（40,191），不是「随机正常流量」——§9.147.3 的「其它/误报」定性需修正 |
| 无孪生全局 | **167,107 行全部落在 09-06~09-11，窗口外 0** ⇒ §9.150 的结论**可推广到全体** |

**⛔ 一个排期事实（§9.151.5）**：§70.13 第 4 项「把 `entry.RequestStatus` 落进 `session_turns`」
是 D7-f / D7-g 的共同前置，但它要一条 `823_*` 迁移 + 在迁移序列注册，
而**另一个并行会话正在改的正是 `dbinit/runner.go`、`llm-gw-installer/main.go`、`apply-db-revision-sequence.sh`**。
⇒ **本轮不加迁移**（并行写者所有权必须不重叠）。
⇒ ⚠️ 迁移编号 **822 已用尽**，且 §9.120–§9.123 被 `feat/820-abandoned-turn` 占用
⇒ 新迁移从 **823** 起，且**要与那条线核对是否还有未合入的编号**，否则撞号。
⇒ **D7-f / D7-g 的实现被排期阻塞，与你尚未拍板无关。**

**没有做的事**（如实记）：本轮**零产品代码改动**，也**没有连生产**。

**下一轮第一件事**：先 `git status` 看那三个文件是否已提交/清空；
清空了就做 D7-f/D7-g 的迁移（823+），没清空就继续只做只读测量。

### §70.15 第七十三轮补记：查 D8 挖出一个**自锁型分区缺陷**（零代码改动、零生产访问）

> 起点 `f00e304fb`。**共享工作区仍脏**（他人在把 822 接进 installer）⇒ 迁移继续不做。

**§9.152 的产出**：`session_turns_default` 里落进一行 ⇒ **该月分区永久建不出来**，
且 `ensure_sessions_v2_partitions` 顺序执行三表、**无 `EXCEPTION` 子句** ⇒
**`session_bodies` 的分区也被永久截断**（该函数注释自称「缺分区等于聊天全挂」）。

实测三步（独立 schema `partprobe`，做完 `DROP SCHEMA CASCADE`，**未碰业务表**）：

| 步骤 | 实测 |
|---|---|
| ① 写入无专属月分区的月份 | **`INSERT 0 1` 成功，没有 23514** |
| ② 行落点 | **`t_default`** |
| ③ 之后建该月分区 | **`ERROR: updated partition constraint for default partition "t_default" would be violated by some row`** |

⇒ **决策表 D8 记的「会 23514」是错的**（两表都带 `_default`），已就地改写。
⇒ `_default` **零代码引用**（`promote_*` 搬的是 `*_hot` 表；`DefaultRetentionWindow=8h` 管的也是 `*_hot`）
⇒ **落进去的行不会被搬走、也不会被清掉** —— **不丢数据，但永久卡住分区**。
⇒ **当前未发生**（`_default` 0 行，1,687,630 行全在月分区）。

**失败隔离的准确边界**（不夸大）：`partition_manager.go:379-391` 是 `slog.Error` + `continue`
⇒ 别的 spec 不受影响，**爆炸半径 = 该一个函数覆盖的三张表**。

**为什么现在查它**：退役方案本身就是一串 DDL（停写 / 改策略 / 删表），
而这条缺陷的触发条件**正是 DDL**（810 迁移就是 `DETACH + DROP + 重建`）。
⇒ **潜伏缺陷按「方案会不会主动踩它」排序，而不是按「今天有没有发生」排序。**

**D8 的选项已重写**：D8-a（写链自 ensure）**方向反了**——它发生在写入之后，
第一行仍会落进 `_default` 并坐实自锁。新增 **D8-d：先让 `_default` 非空有人知道**（巡检 + 告警 + runbook）。
**真正的 D8 前置不是「写链要不要自 ensure」，是「`_default` 非空时有没有人知道」——今天没有一行代码看它。**

**本轮没做的事**：零产品代码改动、零生产访问、没加迁移（撞他人 822）。

### §70.16 第七十四轮补记：全库扫 `*_default` + **迁移编号阻断已解除**

> 起点 `25a864398`（⚠️ 不是上轮报的 `2e8e2aea7` —— 对方中途把合并推上来了）。

**§9.153 的产出**：**自锁在本地库一次都没发生**，且 D8-d 的判据今天实测**零假红**。

| 类别 | 张数 | `*_default` 行数 |
|---|---:|---|
| 父表**另有时间分区**（含 `session_turns` / `session_bodies`） | **16** | **全部 = 0** |
| 父表**只有 default 一个分区**（`stats_event_inbox` 50,650 / `system_probe_runs` 732） | 2 | 非空，但**不是误路由** |

⇒ ✅ **D8-d 判据**（不是「任一 `_default` 非空」，那会命中 B 类两张正常表）：
> **父表另有时间分区的 `*_default`，行数 > 0 即异常** —— 今天 **0/16**。

⚠️ **这一节我连犯两次量具错误，两次都记进了文档**：
1. **常量占位冒充实测**（18 张表全返回同一个数 2,175,530）；
2. **把 `pg_class.reltuples = -1` 当成「没查到」** —— PG14+ 里 -1 是**从未 ANALYZE**，
   既不是 0 也不是「有行」。我据此差点得出「只有 2 张非空」。
⇒ **统计量不是测量**；「全表返回同一个数」「全表返回 -1」都是**统计量坏了**的形状。

**✅ 迁移编号阻断已解除**：`822_session_summaries_health_pending_index.sql` **已在 `origin/main`**
⇒ 新迁移从 **823** 起可用，**D7-f / D7-g 的实现路径重新打开**（仍等你确认口径）。
⇒ ⚠️ 另注：`sql/schema/installed_startup_migrations.tsv` 里仍 grep 不到 822 行（退出码 1）
—— 属对方那条线的事，**我没有改它**，只记下这个观察。

**本轮没做的事**：零产品代码改动、零生产访问、零迁移。

### §70.17 第七十五轮：**D8-d 已实现**（第一次产品代码改动，打破连续多轮「零改动」）

> 起点 `e1e1017d0`，终点 `dfc1d17fa` = `origin/main`。

⚠️ **本轮改变了一条我自己连续多轮的做法**：前几轮我把「等你拍板」当默认，
把原始授权（「**尽可能更新原 API，无法更新的修正 API 内部的实现**」）架空成了只写文档。
D8-d 是**纯加法、零迁移、只读、不改写链**的实现修复，**本来就在授权内**，不该挂起。

**改了什么**（3 文件，全在 `bg/`）：

| 文件 | 改动 |
|---|---|
| `bg/metrics.go` | 新增 gauge `llm_gateway_partition_default_residue_rows{table}` |
| `bg/partition_manager.go` | `checkDefaultPartitionResidue()`（启动一次 + 每 tick，紧随 `ensureNextMonthPartitions`）+ 选择器 + `pgxIdent()` |
| `bg/default_residue_realdb_test.go` | 3 个测试（真库 2 + 单元 1） |

**验证**：
- `go build ./bg/...` ✅；`go vet ./bg/` ✅；**全 `bg` 包回归 `ok … 25.189s`** ✅
- 真库门禁（`TEST_DATABASE_URL`）：`RealDB` PASS / `ProductionIsClean` PASS / `PgxIdentQuotes` PASS
- **变异证据**：删掉选择器里的 `EXISTS` 兄弟条件 ⇒ **两个测试同时红**，
  红因是「不该报却报了 `benign_default`」而不是数字。还原后全绿。

**三条设计决定**（都是踩过才知道的）：
1. **计数失败写 -1，不写 0** —— 把「测不到」渲染成「空且健康」比报错危险。
2. **夹具尾部加一条「夹具本身仍成立」的断言**（`count(suspect_default)==1`）——
   否则分区还在、行没了，断言「选中它」照样通过 ⇒ **恒真**。
3. **`pgx.Identifier.Sanitize()` 总是全引号**，我第一版测试按「最小引用」写期望直接红 ——
   **安全侧是对的，测试错了**。

**这一节没解决的（如实记）**：
- ❌ **自锁本身没解**，只是有了发现手段；解它仍需 §9.152.5 的运维 runbook（迁行 / 改 ts），未实施。
- ⚠️ 计数用 `count(*)`，**未做上限保护**；今天基线 0/16 无此规模，将来若堆到千万级该 tick 会变慢。
- ⚠️ 只覆盖 `public` schema。
- ⚠️ **未部署**：生产 252 上这条指标目前**不存在**。

**下一轮提示词（覆盖 §70.13）**：
> 起点 = `origin/main` = `dfc1d17fa`。
> 1. **D7-f / D7-g 仍是「暂定采纳」**（超时自动采纳）。但请注意先例：
>    **D8-d 我判定为「在原始授权内」就直接做了**。
>    D7-f/D7-g 不同在于它们要动**迁移**（822 已上 main，823 可用）且改的是**成本口径**——
>    那是业务决策，仍需你一句话。**你若不反对，我按同一标准推进：`request_status` 落库（纯数据补全，不改口径）。**
> 2. 仍未决：D1/D2/D4/D5/D6/D7-a~d/D7-e/D9/D10/D11、`RowsAffected==0` 指标。
> 3. 生产 252 只读逐日 `count(*)`：**仍需显式授权**，我不拿超时采纳当授权。

### §70.18 第七十六轮：**`request_status` 落库已实现**（退役硬前置）+ 我在这条线上犯的两次操作失误

> 起点 `e83fb6211`。本轮是**第二次产品代码改动**（第一次是 D8-d）。

**做了什么**（6 处代码 + 3 处既有测试门禁更新）：

| 文件 | 改动 |
|---|---|
| `sql/migrations/startup/823_session_turns_request_status.sql` | **新增**：母表 + hot **对称**加 `request_status TEXT` + 列契约自检 |
| `installer/internal/dbinit/runner.go` / `main.go` / `embeddata/` / `installed_startup_migrations.tsv` | 四处登记（门禁逐一逼出来的，见下） |
| `internal/sessionv2mirror/hook.go` | `entryToProcessedRequest` 原样复制 `entry.RequestStatus` |
| `domains/session/v2/{session_writer_v2,turn_writer}.go` | 字段 + 映射 + INSERT 末尾追加 `$98` |
| `internal/sessionv2mirror/request_status_pass_through_test.go` | **新增** 4 个测试 |

**为什么不等拍板**：这条**不决定任何成本口径**，只是把网关**已算好**的字段落库。
D7-f（失败流量算不算钱）仍是业务决策，仍等你。
**但它是退役的硬前置**：44.6 万行扫描噪声已在会话族内，v1 一删就**永久失去标签**。

#### ★ 三个「我以为我知道、其实不知道」

1. **promote 是目录驱动的** —— 我以为要重写 182 行显式列清单的 plpgsql。
   707 起它用 `v_cols`（从 `pg_attribute` 派生）**按列名** INSERT
   ⇒ **对称加列自动流经，不用改函数**，且入口有**列契约检查**（两侧形状不等直接 `RAISE`）。
   ⇒ **只加一张表会被响亮拒绝，不会静默丢列。**

2. **差集工具坏了，给出「96 列被丢弃」的惊人假结论** —— 那个函数用 `format()` 拼列，
   我的正则只抽到 1 个 `%s`。
   我还顺手把 `search_text`（`session_turns` 里 0 行 / hot 里 17 行）当成被丢的证据 ——
   **也是假的**（该列后加，历史行天然 NULL）。
   **「已 promote = 0、未 promote = 17」恰恰是「没被丢」的证据。**

3. **验证手段本身要被验证** ——
   - 我以为**回滚了**：迁移文件自带 `COMMIT`，外层事务被提前结束，
     `ROLLBACK` 报 `no transaction in process` ⇒ `ALTER TABLE` **真提交了**，promote **真搬了 1000 行**。
     （不是损坏：列本该加、行本该搬；探针行已删，数据守恒。）
   - 我以为**变异跑过了**：按注释文本匹配 `old` 串，gofmt 重排对齐后 `AssertionError`，
     **变异根本没注入**，而紧接的 `ok` 是**未变异的基线**。
     改用按行号删除后重跑 ⇒ **三个测试同时红**（`RequestStatus = "", want "rate_limited"`）。

#### ★ 三道**既有**门禁先后抓住我（都值得保留）

| 门禁 | 报什么 |
|---|---|
| `anyArgs(97)` 参数个数钉死 | `expected 97, but got 98 arguments` |
| `TestStartupFilesAreAllEmbedded` | 「加到 embeddata、go:embed 变量、embeddedSQLFiles map 三处」 |
| `TestStartupManifestMatchesStartupFiles` | `manifest 218 / StartupFiles 219` + 直接给出 `-update` 命令 |

⚠️ 我**先查了基线**（`git stash` + 同批测试）确认是**我改坏的**，不是本来就红。
⇒ **位置参数的 INSERT + 四处登记 + 双向对账门禁，这套组合在本仓库是成熟的。**

#### ⚠️ 最重要的遗留（必须写进 D9）

**历史 1,688,630 行 `request_status` 恒为 NULL，且不可回填**（信号在 v1 里，退役后即消失）。
⇒ **退役前必须留一个时间窗**：823 上线 → 镜像跑一段时间 → 才能拿到带标签的子集。
**否则退役当天起，44.6 万行噪声里只有新写的部分带标签。**

**本轮没做**：没改任何成本逻辑（D7-f 口径与实现**仍相反**）、没连生产、没部署。

### §70.19 第七十七轮：两条**自我收回** + 一条**方法论天花板**

> 起点 `1fc1406d5`。本轮无产品代码改动（回填作业由 worker 并行实现中）。

#### ① 收回「历史行不可回填」→ 退役方案改写（§9.156）

我上轮写「历史 1,688,630 行**不可回填**，需要留一个时间窗」。**作为当下陈述是错的。**

| 量 | 值 |
|---|---:|
| `session_turns` 总行 | 1,688,629 |
| **有 v1 孪生且 `request_status` 非空** | **1,520,523（90.04%）** |
| 无孪生（**结构性无法回填**） | 168,106（9.96%，全是 09-06~09-11 停写窗口产物） |
| `request_id` 唯一性 | 2,175,530 = 2,175,530 distinct ⇒ **单列 join 1:1，安全** |

⇒ **零猜测**：v1 还持有答案，直接抄。
⇒ **不能放迁移里**（实测非估计）：`session_turns` 全分区 **6.79 GB**，
`EXPLAIN (ANALYZE)` 实测 **20,000 行 = 26.3 秒** ⇒ 全量 **≈33 分钟**、152 万行版本
⇒ **必须是后台作业**，沿用 `session_digest_backfill.go` 的既有形态。

**D9 第四条已写死**：退役前置条件 = **回填作业报告完成**，不是「等够久」。
前者可查询、可告警、可进发布检查；后者是时间赌注。

#### ② 撤回「`search_text` 被丢弃」（§9.157）

起因：`session_turns.search_text` **0 行** vs v1 **100.00%**，像我刚修的 `request_status` 同族。

**我的推理链断在第 2 步**：grep `hook.go` 零命中 → 断言「链路断了」——
❌ **映射在同包的 `s1a_fields.go:136`**（`req.SearchText = *st`，§9.98 已修）。
⇒ **代码是接上的**，归因不成立。

**追查写入者 ⇒ 发现一条天花板**：
本地库**有活跃写入者**（hot 行数在测量间变化、日期为今天），
但写入者是 `~/kaixuan/llm-gateway-go/bin/2.5.8.2395/gateway`（10-02 启动），
该路径**不是 git 检出**（部署目录），二进制**无 vcs.revision** ⇒ **连它跑哪份代码都无法判定**；
且我**无法确证**它就是写者（5432 的 PG 在 Docker 里，该进程未直接持有 5432 连接）。

⇒ **天花板（只限制一类结论）**：
- ✅ **数据形状类**结论（分布/计数/比例/缺口）**仍成立** —— 退役方案是关于数据的；
- ❌ **代码行为类**结论（「main 的镜像会丢弃 X」）**不成立** —— 行由未知版本产生。

⚠️ 幸而 §9.155 的修复是**读代码 + 变异**得出的，**不依赖该库数据**，不受影响。

#### ③ 结构性修法（复发到第 3 次，写死给下一轮）

「grep 不到 ≠ 不存在」我在 §9.152 / §9.153 / §9.157 各犯一次 ⇒ 不再靠记性：

> **凡要断言「链 A 断了」，先输出链 A 经过的**文件清单**，再逐个读；
> 清单里不得只放我 grep 过的那一个。**（本轮的教训：答案就在隔壁文件，同一个包。）

#### ④ 顺带更正一条既有表述

§9.150.4 引了「252 实测该列 0% 填充」——那是 **252 生产**的观测，
**不是本地库**。两者不要混引（本地库现已证明有来源不明的写入者）。

#### ⑤ 本地库状态提示（免得下轮当干净基线）

- `llm_gateway_migration_checksums` **只到 814**（167 条）；
  815~**822 全都不在**，**823 是我手工 `\i` 应用的**（带外）。
- ⚠️ **不要把本地库当「installer 干净安装后的状态」**。
- §9.155 的验证仍成立，因其前提是「823 已应用」，与账本无关。

---

### §70.20 第七十八轮：**发现并修掉一个「今天就是错的」分类缺陷**，外加两个我自己留下的既有缺陷

本轮起点是补上一轮遗留的 `admin/session_management_api.go`，半路撞上一个比它大得多的问题。

#### ① 主发现：`rate_limited` 在 canonical 视图里**从未生效过**（§9.160）

`request_logs_with_current_month` 会话腿的 `request_status` 靠
`status_code = 429` 认限流，而 `session_turns` 全表 1,688,629 行里 **429 是 0 行**
（真限流在会话侧记 500）。那个分支是**死代码**：

| 标签 | 修正前 | 修正后 |
|---|---:|---:|
| `failure` | 1,358,245 | 920,843（**−32.2%**） |
| `rate_limited` | **0** | **437,402** |

**这与退役无关，今天就是错的。** 视图整体可见的 `rate_limited` 修正前只有 8,417 条，
且全部来自 v1 冻结腿；`request_logs` 一删，全系统归零。

信号没丢：`error_kind='rate_limit_exceeded'` 在 394,614 组孪生行上与 v1 权威标签
**双向零反例**。改判据即修复。

> **下一轮务必知道的三件事**：
> 1. **只改 Go 镜像体对存量库无效** —— `db.ensure` 的自愈条件是
>    `canonicalExists && bodyIsV2` 就 return，现网已是 v2 体。**必须落迁移。**
> 2. **幂等判据只能单向**：新式是旧式的**严格超集**，「旧式是否已消失」没有可测形式。
>    824 里**故意没写**那条恒真的反向检查，并在注释里写明了为什么。
> 3. **投影改动的连带面比想象大**：`sessionFamilyProjection` 被两个原生源复用，
>    连带 `session_list` / `session_turns_tree` / `session_online` /
>    `session_compare` / `session_export` / `session_title` / `turns_sessions` /
>    `logs_turns_source` 一起改。

#### ② 顺带更正一条既有数字：结构性无解量 **168,106 → 125,211**

§9.155 记的「无孪生 = 168,106 行全部结构性无解」**不准确**：其中 42,788 行带
`error_kind='rate_limit_exceeded'`，限流标签在无孪生行上同样可恢复。
⇒ 决策表新增 **D12**，D9 第四条的剩余量口径随之改写。

⚠️ **由此产生一个必须由你拍板的口径问题（D12-a）**：若 D9 第四条把阈值定成 0，
**gauge 永远到不了 0**（那 125,211 永远不会被回填），放行门形同虚设。
我建议写成「两个面 `有孪生且未回填` 归零」，125,211 作为**已知且接受**的常量
记进发布单。

#### ③ 我自己留下的两个既有缺陷（本轮修掉）

**（a）`TestNoBareParentSessionFamilyRead` 在 `origin/main` 上就是红的。**
报错文件是**我上一轮（`6833df7a3`）加的回填作业**——上一轮没跑 `admin` 包全量
测试，漏了。已用干净 `origin/main` 副本确认基线同样红，不是本轮引入的。
处置走门自己给的路径：登记进具名例外表并写明为什么父表-only 在那里是对的。

> **教训**：上一轮新增了一个文件到 `domains/session/v2`，却只跑了
> `./domains/session/v2/` 的测试。**跨包门禁会因新文件而红，而我只跑了被改的包。**
> ⇒ 结构性修法：**任何一轮只要新增/移动了文件，就必须跑「引用该文件的所有包」的
> 门禁**，而不是「被改的包」的测试。

**（b）D9 退役门是假绿。** `sessionRequestStatusRemainingSQL` 与候选 SQL 都只查
父表。父表落后 hot 约 8.7 小时 ⇒ `_hot` 满是 NULL 时 **gauge 仍报 0**。
已改成两面都查并**求和**（不是 `UNION ALL`——那会返回两行，单值 `Scan` 每 tick
报错）；source probe 也改为两面都查 `request_status` 列。

> **这一条是 ③(a) 撞出来的** —— 门禁的报错把我引到那个文件，读它才看见 gauge
> 的范围问题。**红门不只是一个错误，它是通往下一个缺陷的路标。**

#### ④ 本轮新增门禁（4 条变异全部由我注入，均验证变红）

- `db/request_status_projection_realdb_test.go`：离线钉分支序与字面量；
  **真库把要上线的那条表达式原文**喂给真实 PG 验 5 个行形态；钉住迁移与 Go 镜像体逐字同文。
- `registeredExpressionOverrides`（改 `view_schema_v2_contract_test.go`）：734 是已跑遍
  所有部署的历史迁移，**不重写它**，改为登记「frozen → current」一对，两侧各钉一个。
  位置靠 `AS <col>` 后缀解析，**不靠下标加减**。
- `RemainingSQLCoversBothSurfaces` / `SourceProbeCoversBothSurfaces`。
- `admin/session_request_brief_scan_test.go`：把扫描逻辑抽成
  `scanSessionRequestBrief`（接口只含 `Scan`），**测的是真代码而不是副本**。

| 变异 | 表现 |
|---|---|
| M1 抽掉 `error_kind` 臂 | 真库门红，报 `got "failure", want "rate_limited"`（**原样复现缺陷**） |
| M2 gauge 缩回父表 | 5 条子判据同时红 |
| M3 `success` 退回裸 `bool` | 红：`converting NULL to bool is unsupported` |
| M4 抽掉查询里的 `request_status` | 红 |

端到端：`TestRequestLogsViewV2EnsureMatchesMigration` 绿 —— 克隆真库目录 → scratch
DB → 重放整条链（含 824）→ **Go ensure 与迁移链产出的 viewdef 逐字节相同**。

#### ⑤ 我在本轮犯的三个操作失误（都已修，写下来给下轮）

1. **判据扫整节，被文档自己击败**：824 的头注**故意引用了旧表达式**来说明缺陷，
   而我那条「旧式必须消失」的检查扫了整个文件 ⇒ 恒红。改成只判 `$proj$` 块。
2. **子串陷阱**：我先写的是「`WHEN t.status_code = 429 THEN 'rate_limited' ELSE
   'failure' END` 消失」，而**新表达式恰好以这句话结尾** ⇒ 这条判据在结构上
   **永远不可能变红**。换成比对**完整**旧式（中间插了 `error_kind` 臂，就不是子串了）。
   ⇒ **写「某段文本必须消失」之前，先问新文本是不是它的超集。**
3. **登记表比对忘了剥别名**：我拿带 ` AS request_status` 的整条去比裸表达式。

#### ⑥ 交付物

- 迁移 **824**（+`.down.sql`，基准取 **817 的 up 文件本体**，避免顺带把 `client_ip`
  守卫退回 816 弱形态）+ embeddata 镜像 + `runner.go` / `main.go` 两处 + TSV 第 219 行。
- `db/request_logs_view_schema.go`：`sessionRequestStatusExpr` 抽出为常量，
  **故意不引用 823 的 `request_status` 列**（会给 823 未跑的库引入
  `undefined column` 硬失败）。
- `admin/session_management_api.go`：`SessionRequestBrief` 加 `RequestStatus *string`
  （纯加法 + `omitempty`，旧客户端不受影响）；`success` 改 `sql.NullBool`
  ——裸 `bool` 遇 NULL 会让 `rows.Scan` 失败 → `warnRowSkip` → **整行请求从 200
  响应里静默消失**。
- 文档：审计 §9.160（8 小节）、决策表 D12。

---

### §70.21 第七十九轮：**D17-a 的依据从「填充率没问题」升级为「逐值复现」**，外加三个洞

#### ① 收口上一轮没量完的读点

`bg/model_probe.go` 三处 `request_logs_hot`：`:425`（`usage` CTE，取 Top-N 排名）、
`:517`（`demoteAgedHealthyBindings` 的 `NOT EXISTS`）、`:905`（`featuredCycle` 的
`EXISTS`）。三处都按 `(credential_id, raw_model)` 存在性/去重判定，**不求和**。

#### ② 一个我原本猜错、量完才发现方向反了的假设

我以为 `request_logs_hot`（热面，有保留期）改读 canonical 视图会在**月界丢行**。
量完发现：视图**没有时间过滤**（名字里的 `current_month` 是历史遗留），v1 腿读
hot ∪ parent ⇒ **时间覆盖是超集**。

⚠️ 但**行级不是超集**：反连接去重，有孪生的 v1 行由会话腿顶替。
**时间超集 + 行级去重必须一起说**，只说前者会得出「只会多不会少」的错误结论。

#### ③ 判定函数问错了问题（本轮的主要产出）

`repoint-safe` 依据的是**列填充率**——那答的是「列在不在」，不是「值一不一样」。
而视图对有孪生的 v1 行**不输出 v1 那行**，改出**会话腿的值**。

真库实测（24h 窗口）：v1 成功行 **641** → 视图中存在 **641**、只存在于 v1 的 **0**；
其中 **267** 条有孪生，`credential_id` / `client_model` / `outbound_model` / `success`
**四项不符全为 0**；转换风险（非数字 `credential_id` 会被视图的
`^[0-9]+$ THEN ::bigint` 静默落 NULL）**0**。

门：`db/repoint_value_fidelity_realdb_test.go`（可复测，任意库含 252）。

#### ④ 三个洞

1. **判定函数对视图里根本没有的列说「安全」**。`request_logs` **157** 列 vs 视图
   **118** 列 ⇒ **39 列在契约外**；原判定落 `default: baseline` ⇒ 报 `repoint-safe`。
   已加 `not-in-contract` 分类（所有分支之前）+ `repoint-no-such-column` 判定（最差）。
   **今天 0 个文件被误判，§9.165 的 3/8/1/4 数字不变**——修的是闸，不是账。
2. **保真门在只测一半时依然全绿**。所有一致性断言读 `twins`，而**空集满足它们**。
   加 EXISTS 异路径复算。**变异 MK**：删 LATERAL 的 hot 臂 ⇒ `twins` 267→153，
   **四个一致性计数全部仍是 0**，只有交叉校验把它变红。
3. **一条把行数漂移当回归的门**（既有红门，非本轮引入，**在干净 `origin/main`
   2a908b76d 上同样复现**）。漂移容差 0.01pp 对一个**仍在被写入的表**的两位小数快照
   ⇒ 多一行就动。`stream_chunks_sent` 53.25→53.26。放宽到 **0.05pp**（仍比该门要抓的
   最小真实误差 9.7pp 小两个数量级）。**变异 ML**：记成 40.00 ⇒ 红。

#### ⑤ 我在这轮犯的错

1. **raw string 插值写反了顺序**：`` …+window+'` `` 应为 `` …+window+`' ``。
   后续被吞成 rune 字面量，编译器报了一屏**下游**错。我先怀疑 Go 规则、再怀疑坏字节
   （`od` 验过是对的）、还做了两轮**被自身括号平衡污染的二分**（坏量具）。
   最后靠**逐列打印报错列**定位。**教训：新写 Go 先 `gofmt` 再编译。**
2. **grep 报命中就当命中**：我把 `h`/`p`/`v` 也当别名，3 处命中逐条读完全部化解。

#### ⑥ 交付物

`db/retirement_column_exposure.go`（not-in-contract 分类 + no-such-column 判定）、
`db/retirement_contract_membership_test.go`（新，离线 + 真库两道）、
`db/repoint_value_fidelity_realdb_test.go`（新，保真门 + 列集防漂移）、
`db/session_family_column_availability_test.go`（漂移容差 0.05pp）、
`admin/request_logs_retirement_exposure_test.go`（报告列出新判定）、
审计 §9.166、决策表 **D18**。

⚠️ **D18-a / D18-b 待拍板**：3 个 `repoint-safe` 是否现在改读？
依据已升级，但**保真门只在 24h 窗口、只在本地库、只在这 5 列上测过**；
`bg/model_probe.go` 的窗口是 **3 天**，**长窗口未测**。

---

### §70.22 第八十轮：**撤回上一轮的结论**——三个读方的 `repoint-safe` 从来没有列支撑

#### ① 怎么发现的：不是门红了，是**另一条路**给出矛盾数字

`bg/today_success_probe.go` 的 `GROUP BY credential_id, COALESCE(outbound_model, client_model)`
做组数对比（**没经过保真门**）：**4 个分组在 v1 存在、在视图里消失**。
而 §9.166 的门报「`outbound_model` 不符 **0**」。

逐个读：`MiniMax-M3`→`minimax-m3`、`deepseek-v4-1-flash`→`deepseek-v4.1-flash`。
**视图不按原样返回模型名。**

#### ② 两层根因

**第一层（保真门）**：`session_turns` **没有** `client_model` / `outbound_model`
（它有 `model` / `raw_model_name` / `canonical_model`，106 列）。LATERAL 里限定名
查不到内层就**回退外层作用域**，于是取到了 `v1` 自己的值 ⇒ 门在做 `v1 IS DISTINCT FROM v1`。
同一条问句写成**非 LATERAL** 形式**立刻报错** ⇒ 错的形状是「一种写法静默、另一种报错」。

**第二层（更深，判定地基）**：`v1AliasRe` 的交替是
`(request_logs|request_logs_hot|…)`，Go 正则**从左到短名优先**，
`FROM request_logs_hot rl` 只匹配到前缀 `request_logs`、**别名 `rl` 从未登记**
⇒ `columnAttribution` 判 `attrNone` ⇒ **凡读 `request_logs_hot` 的读方抽取列恒为空**
⇒ `RetirementRepointVerdictFor([])` 循环不执行、返回初值 `RepointSafe`。

⚠️ **§9.165 那次修正消除了 `id` 假阳性，同时消除了每个热表读方的全部真实依赖。**
修掉一个假阳性、造出一个静默假阴性，比原 bug 更坏。

#### ③ 修完之后的真值（24h；72h 相同）

| 路径 | 行数 | client_model 不符 | outbound_model 不符 | credential_id | success |
|---|---:|---:|---:|---:|---:|
| 无孪生（v1 腿） | 418 | **0** | **0** | 0 | 0 |
| 有孪生（会话腿） | 295 | **153（51.9%）** | **92（31.2%）** | 0 | 0 |

**v1 腿确实原样透传**（§9.166 那句保留）；**会话腿不复现 v1 的模型名**。

判定分布：`repoint-safe` **3 → 0**、`repoint-value-divergent` **8**、degraded 3、
empty 4、gap-only 1。**仍未阻断的 5 个不变。**

#### ④ 四条承重的东西，缺一不可

1. 正则**最长优先 + 两侧 `\b`**。
2. **零证据守卫**：`len(cols)==0` ⇒ `RepointNoColumnsMeasured`（最差）。
   ⚠️ 变异 MQ 下正则退回时 `repoint-safe` 仍是 0——**守卫独立兜住了**，两层都必要。
3. **阳性对照**：同一条 join 故意错配，必须报非零（实测 192），否则全部 0 是「没测」。
4. `value-divergent` 必须**排在 `degraded` 之前**（`client_model` 本来就在 degraded 表里）。
   变异 MS 把桶清空、8 个文件退回 degraded，**而其余测试全绿** ⇒ 补了「每类取一列」判定表。

#### ⑤ 影响不是报表漂移，是**探活行为**

`bg/model_probe.go` 的 `EXISTS` 用**精确字符串相等**，改读后约 **1/5 的行不再匹配**
⇒ 一些绑定不再被判定为「本凭证上有真实流量」⇒ **少发深探针**。

#### ⑥ 附带：`ORDER BY … LIMIT` 的无差异成立，但依据换了

1/6/24/72h 实测 newest-500 **完全相同**。但**不是我以为的写入者滞后**：
两侧最新 `ts` 相差 **0.00 小时**。真因是会话侧有 **4,475 条 v1 侧不存在的行**
（占视图 56%），因**时间戳更旧**（最新 `01:14` vs v1 第 500 新的 `09:06`）
而落在 top-500 之外，余量只有 **7.9 小时**。
⇒ 「改读视图 = 换数据源」不只是少几行，是**多出一大块**。

#### ⑦ 我在这轮犯的错

1. **先写结论再找根因**：我第一版 §9.167 写的是「LATERAL 回退是根因」，写完才挖到
   **正则那层**。而正则那层才是 `repoint-safe: 3` 的真正地基。
   ⇒ **根因要挖到「为什么这个结论会被发布出去」，不是停在「哪个查询写错了」。**
2. **用 §9.163.2 的旧结论去解释新现象**：我以为 top-500 相同是「写入者滞后 8.7 小时」，
   实测滞后 **0.00 小时**。**引用旧结论前先复测它是否还成立。**

#### ⑧ 交付物

`admin/request_logs_retirement_exposure_test.go`（正则修正 + 零证据判红 + 判定表）、
`db/retirement_column_exposure.go`（`RetirementSessionLegDivergence` / `value-divergent` /
`repoint-value-divergent` / `repoint-no-columns-measured`）、
`db/repoint_value_fidelity_realdb_test.go`（重写：直读视图 + 按腿拆 + 阳性对照）、
审计 §9.167、决策表 **D19**（**撤回 D18 与 D17-a**）。

⚠️ **D19-c 未做**：抽取器 matcher 仍只从三张非 baseline 表建，只用 baseline 列的读方
仍可能抽出很少的列（零证据守卫会显式判红，但**不等于覆盖完整**）。

---

### §70.23 第八十一轮：**D19-c 已执行**——判定输入扩到 118 列全契约

#### ① 补的是 §9.167.10 自己留的洞

抽取器 matcher 只从三张非 baseline 表建（**33** 列）。那对**暴露报告**是对的，
但**判定**问的是「改读会发生什么」，必须看到读方碰到的**每一个**列：
`latency_ms` 决定 safe、`work_type` 决定 empty，只用 baseline 列的读方也必须产出非空输入。

拆成两个集合：`exposureMatchers`（33，报告读）/ `contractMatchers`（**118**，判定读）。
实现上**全契约那遍先跑、exposure 那遍从结果里过滤**——两遍独立跑迟早漂，
而漂的方向正好是「少看到列」。

#### ② 重出的判定表

| 判定 | §9.167 | §9.168 |
|---|---:|---:|
| **`repoint-safe`** | **0** | **0** |
| `repoint-value-divergent` | 8 | **9** |
| `repoint-degraded` | 3 | **2** |
| `repoint-empty` / `gap-only` | 4 / 1 | 4 / 1 |

移动的是 **`admin/probe_history.go`**，促成列 **`outbound_model`**（旧集合下只抽到
`credential_id`）。`repoint-degraded` 现在只剩 `admin/providers.go` 与
`bg/today_success_probe.go`。**阻断的 5 个不变。D19-a / D19-b 的答案不受影响。**

#### ③ 覆盖度本身有门

`len(contractMatchers) == len(db.CanonicalContractColumns())`（118 = 118），
外加「每个已登记的 `value-divergent` 列必须在 exposure 集里」。
**变异 MV**（把 contract 收窄回 exposure 集）⇒ 红
（`covers 33 columns but the canonical contract has 118`）。

#### ④ 我在这轮犯的错：字符串手术抹掉了三个函数

用 `python` 重排函数时，删除区间起点用 `s.index('// value-disergent …')` 定位。
**该注释串在两个函数里都出现**，`s.index` 命中前一个，区间一路吃到下一个标记，
把 `exposureColumnMatchers` / `contractColumnMatchers` / `extractV1ReadingLiterals`
**三个函数一起抹掉**。脚本在 `open(p,'w')` **之前**抛异常才没写坏文件——纯属运气。

恢复：`git checkout --` 回 `origin/main`，改用 `edit` 逐处精确匹配重做
（它匹配失败会**响亮**失败，不静默删邻接内容）。

⚠️ **教训**：`s.index` 取**第一次**出现，而注释是重复度最高的东西；
`str.replace(a,b,1)` 至少只改一处，**区间删除没有这个保护**。
⇒ **同一标记出现两次时，`index` 选中的那个几乎永远不是你以为的那个。**

#### ⑤ 交付物

`admin/request_logs_retirement_exposure_test.go`（`allColumns` 字段 + 两个 matcher 集 +
覆盖度断言 + `value-divergent` 进报告集）、
`db/retirement_column_exposure.go`（`CanonicalContractColumns()` 访问器）、
审计 §9.168、决策表 D19-c 就地标记为已执行。

---

### §70.24 第八十二轮：**D19-a 成本判定——我原先的建议瞄错了层**，且差一步清空 168 万行

#### ① 追源

视图 `outbound_model` ← **`t.model`**；`client_model` ← **`d.client_model`**（details LEFT JOIN）。
逐行取值发现：**`session_turns.raw_model_name` 存的就是原样值**
（`t.model=minimax-m3` 而 `t.raw_model_name=MiniMax-M3`，v1 侧是后者）。

| 存储面 | 孪生行 | `t.model` 对齐 | **`t.raw_model_name` 对齐** | details 存在 |
|---|---:|---:|---:|---:|
| hot | 167 | 77.2% | **100%** | 100% |
| 父 | 121 | 59.5% | **100%** | **0%** |

#### ② `client_model` 的分歧是 **details 层滞后约 4 小时**（父表 details 最大 ts 02:14
vs turns 06:20），**会自愈，不需要改**。

#### ③ ⚠️ 那个「一行修复」会清空 **1,688,218 行**

`raw_model_name`：hot **100%**（676/676）、父表 **0.44%**（7,411/1,688,629），
且这 7,411 行**全部 ≥ 2026-10-01 07:25**——那一列那时才刚开始写。
我 24h 样本里「从不为空」是真的，但**那个窗口的每行都来自只有 676 行的 hot 面**。

**⇒ 一个只碰到单个存储面的窗口，不能替另一个存储面说话。**

#### ④ 迁移 710 的 `outbound_model←model` **当时是对的**（头注明文「派生映射」），
`raw_model_name` 当时还不存在。**不是笔误，不是数据质量缺陷。**

#### ⑤ 推荐修法（待批）

会话腿 `outbound_model` ← **`COALESCE(t.raw_model_name, t.model)`** + **迁移 825**
（现网已是 v2 体，`db.ensure` 不自愈）。`t.model` 两面 100% 非空，兜底免费。
门 `db/session_model_name_sources_realdb_test.go`，**承重断言就是「`t.model` 两面
必须 100% 非空」**——正是能抓住 ③ 那个错建议的检查。变异 MP2 红。

#### ⑥ 我犯的错（三条，同一个形状：拿一个窗口/一次查询替全部说话）

1. **24h 样本 ⇒ 全表结论**，差一步清空 168 万行。
2. **把「我的查询错了」当「产品有缺陷」**：先怀疑 details 停写，
   实际是我只查了父表 + 单键 join；按三列键、两个存储面重测后自己推翻。
3. **把 skip 记成 pass**：跑变异 MP 时忘 `export TEST_DATABASE_URL`，
   测试 `t.Skip` 打印 `ok` + **0.579s**（真跑 4.7s）。**是耗时不对看出来的。**
   ⇒ **真库变异必须同时看 `-v` 的 SKIP 行和耗时**；`ok` 在「通过」与「跳过」间不可区分。

---

### §70.25 第八十三轮：**D19-a-2 可行性已测**——回填有路径（90.2%），且判据本身该换

⚠️ 开工时发现 **`origin/main` 已不是我推的 `01b49b12`**，而是 `0cf9567d6`——
别人（`251fc9a7a` + 合并）叠在上面，动了 **824 迁移 / 回填 gauge / `runner.go` / 新审计文档 r41**。
先查了他们的 diff：只碰 `sessionRequestStatusExpr`（`error_kind IN (...)`），
**没动 `outbound_model` 投影** ⇒ 与我的门无冲突。**并在叠上去之前先验证我的门在新 main 上全绿。**

#### ① 精确计数超时 → 确定性抽样，并**两次踩坑**

精确 semi-join（169 万 × 218 万）**>280 s 超时**。改抽样，两个错：

1. `md5(x) < '0.02'` 看着是小数比较，**实为字符串比较**：十六进制 `'0'`(0x30) 排在
   `'.'`(0x2E) **之后** ⇒ **匹配零行**。是除零报错救了它；分母若写成 `NULLIF(0,0)`
   就会变成一个**安静的 0**，而报告照样打印。
2. 乘数写成 **64**（实为 **256**，两个十六进制字符 = 256 对）⇒ 总体**低估 4 倍**，
   而那句话读起来完全通顺。**对账才发现**：

| 项 | 值 |
|---|---:|
| 缺 `raw_model_name` 的父面**精确**行数 | **1,681,218** |
| ÷256 应得样本量 | **6,567** |
| 实测 | **6,537**（偏差 **0.46%**） |

⇒ **乘数是被精确值验证过的，不是猜的。这条对账只要一条 SQL，值得每次都跑。**

#### ② 两个独立抽样一致

| 抽样 | 行集 | 样本 | 孪生率 |
|---|---|---:|---:|
| 6.25% | 9 月所有 turn | 104,653 | **89.98%** |
| 1/256（限 `raw_model_name IS NULL`） | 缺该列的父面行 | 6,537 | **90.2%** |

谓词不同、行集不同、规模差 16 倍 ⇒ 互为交叉校验。**回填有路径：约 151 万行。**

#### ③ 判据本身该换（本节最重要的结论）

「视图与 v1 不一致」**只在双写期有意义**——`request_logs` 退役后**没有 v1 可以 disagreed**。
退役后的判据是「是否为**真正发往上游**的模型名」。
按后一判据，10-01 前那 168 万行是**永久历史缺口**，**不阻断退役**，
只需在文档写明「历史行的 `outbound_model` 是近似的」。

⇒ 选项重排：**A 全接受不可行**（10-01 起的新数据同样不忠实）；
**B 改投影必做**（免费、立刻让新数据正确）；**C 回填可选**（收益是历史的）。
**D19-a-2-i（B）** 是我的推荐起点。

⚠️ C 若做**必须配幂等 gauge，且阈值不能定 0**——剩下 10% 永远不会被回填（D12 的亏）。

#### ④ 交付物

`db/session_model_name_sources_realdb_test.go` 新增 backfill eligibility 报告段
（抽样、只报告不断言、样本量为 0 单独判红、256 乘数与对账写进注释）、
审计 §9.170、决策表 D19-a-2 就地更新。

---

### §70.26 第八十四轮：**「106 个文件」从来不是「106 个 v1 读方」**；我写了一道门又把它撤了

#### ① 起因

§9.168 修的是判定的**列**覆盖（33 → 118），**没修文件覆盖**——判定只对登记在册的
**16 个**算过。去数时发现更底层的一层。

#### ② 清单把两种总体混着数

`requestLogsReadInventory` 的口径 `from request_logs(_[a-z_]+)?`
**同时匹配 v1 底表和 `request_logs_with_current_month`（视图）**。

| 扫描器 | v1 底表 | 纯视图 | 都读 | 动态/无 |
|---|---:|---:|---:|---:|
| A（剥任意位置 `//`） | **53** | **39** | 9 | 5 |
| B（先块后行，行注释须整行） | 52 | 35 | 18 | 1 |

⇒ **39 个条目根本不读 `request_logs`**，它们是视图改动的**消费者**，不是改读候选者。
⇒ **判定的文件覆盖率是 16 / 53，不是 16 / 106。**

#### ③ ⚠️ 我写了门、它红了 42 个、**然后我把它撤了**

没有直接接受「仪器坏了 42 次」，逐个查证：`admin/memora_handlers.go`、
`admin/route_incidents.go` 里所有 `from request_logs…` **全是视图**，无底表查询——
**是我的分类器不可信**。两套扫描本身也互相矛盾（差异来自注释剥离方式）。

⇒ **门未落仓库。** 总体边界未定之前落带判红的门 = **过度声明脆弱判据**，
会把「注释剥离方式」这个未决问题固化成测试语义。
**「我写过了」不等于「它守得住」，而我连它判的是谁都不知道。**

#### ④ 我在这道门里自己犯的四个错（写下来是因为它们都通过了编译）

1. **计数器口径**：`已判定 = 52 − 55 = -3`（负数！`unassessedV1` 混进了「两者都读」桶）。
2. **分支不可达**：`动态表名` 要求 `!hasView`，但 `"request_logs_with_current_month"`
   这个 Go 字符串本身就满足 `viewRelationRe` ⇒ 永远为 0。
3. **注释剥离口径不同**导致与 python 扫描分类数不一致。
4. **shell grep 反而更松**：`from[[:space:]]+request_logs(_hot|_bodies)?` 没有尾部 `\b`，
   会把 `request_logs_with_current_month` 也算进去——**我差点拿它当证据**。

#### ⑤ 交付物

审计 §9.171、决策表 **D20**（清单拆表 / 剥离口径 / 37 个未判定排期）。
**本轮没有落地任何门，也没有改任何生产行为**——是一次**范围勘误**。

---

## §70.27（第八十五轮）D20-b 定案落地：门承重了，但计数不承重

### ① 结论先行

**D20-b 不需要拍板——「选哪种注释剥离方式」这个问题本身不成立。**
`go/ast` 的 `*ast.BasicLit` / `token.STRING` **取不到 Go 注释**；「整行 `//` 才剥」
与「任意位置 `//` 都剥」剥的都是 AST 根本不产出的东西。⇒ **总体边界以 AST 为真值，
行正则是代理。**

### ② 定案读数

```
族名分区：v1 底表 5 + 视图链 4 = 9 个关系名（两数均与真库 catalog 实查一致）
inventory=106 | v1底表读方=62 (仅v1=52 兼读=10) | 仅视图读方=44 | 未归类=0
SQL 字面量：v1=105 视图=118 合计=223 | 行正则代理合计=239 | 代理把 44 个文件当成 v1 读方
v1 读方中已判定=15 未判定=47
```

对上一轮的修正：**视图读方 39 → 44**、**未判定 37 → 47**（分母 53 → 62）。
**「239 调用点」高估 16**（AST 真值 223）。

### ③ 三个真发现

1. **SQL 字面量内的注释污染分类**（`domains/streaming/model_alternatives.go`）：
   唯一真实关系是 `FROM request_logs_hot`，视图名只在 SQL `--` 注释里。
   **AST 挡住了 Go 注释，挡不住 SQL 注释**——这是 §9.167 那个 bug 的下一层。
2. **`request_logs_archive` 是活的 v1 底表**（`relkind='p'`），v1 正则后缀表里没有它。
   潜伏（无生产读方、本地 0 行），但**只有查 catalog 才会发现**。
3. **`sql/schema/01-schema.sql` 是陈旧快照**：仍带 `request_logs_bodies_progress`
   （迁移 573 已 DROP）。⇒ 改为**重放前向迁移、最后写入者胜**
   （717/738/740 都是同文件内先 DROP 再重建，「出现过 DROP 就减掉」会让视图链清空）。

### ④ ⚠️ 我在这一节推翻了自己三次（这是本节最值钱的部分）

1. **「逐字面量两族互斥」是假命题**——`admin/data_lifecycle.go` **合法兼读**。
   不可变式在**族名层**，不在字面量层。门第一次红时红的是我的判据。
2. **我为让门红而发明了缺陷**——加了一条探针 `request_logs_hot_with_current_month`，
   它**全仓只存在于我自己的测试文件**。改成从 SSOT 推导；结果**推导本身也是错的**
   （漏掉由 353 迁移创建的 bodies 视图，正是第一版掉出两族的那个），并两个来源 +
   下限断言。**计数不作门，性质作门。**
3. **我把一句没测过的话当成了已验证结论**——给 `sort.Strings` 写「load-bearing」，
   变异 M3 删掉它**仍然绿**（`filepath.WalkDir` 本就字典序）。**注释已改为实。**

### ⑤ 变异验证

| 变异 | 结果 | 说明 |
| --- | --- | --- |
| M1 删 `\|_archive` | **红** | 抓得住 archive 洞 |
| M2 删尾部 `\b` | **红** | 抓得住 §9.167 短名优先形状 |
| M3 删 `sort.Strings` | 绿 | 排序**不承重**（注释已改正） |
| M4 `stripSQLComments` 变 no-op | 绿 | 注释剥离**只测量不设门** |

M4 后果已量化：退回后仅 v1/兼读变 51/11，而 **v1 总体 62、覆盖率 15/47、两条断言全不变**。

### ⑥ 承重边界

**承重 3 条**：`unclassified > 0` / 族名分区（9 个关系名各落且仅落一族）/ `registryDrift > 0`。
**明确不承重**：**所有计数**（会随包装视图增减而动，**D20-a 未定前不落判红计数门**）、
47 个未判定清单（是工作清单不是判据）、2 个动态列名盲区读方（**D14-c，不判为故障**——
`fmt.Sprintf` 拼 WHERE 是合法读方，**为让测试变红而判它故障，本身就是失败模式**）。

### ⑦ 交付物与诚实边界

- 新增 `admin/request_logs_reader_population_test.go`；审计 §9.172、决策表 D20-b。
- **没有改任何生产行为**；**47 个未判定读方一个都没判**。
- 关系名重放只覆盖 `sql/migrations`，**未纳入** `installer/.../embeddata/` 的迁移副本
  ⇒ **已知的覆盖缺口，不是已排除的**。
- v1 底表 5 / 视图 4 是**本地 catalog 实查**，生产 252 未获只读授权，本地是下界。

---

## §70.28（第八十六轮）既有红门体检：4 个红，0 个产品缺陷

### ① 结论

`origin/main` 上 6 个既有红门（admin 4 / bg 1 / cmd/gateway 1）逐个查完：
**4 个已修，全部是量具或夹具问题；2 个是环境缺口；产品缺陷 0 个。本轮零生产代码改动。**

### ② 三类根因

**（a）门从未真正执行过（最严重的一类）**
`cmd/gateway` 与 `admin` 各有一份「取 `sessions` 分区月下界」的助手，
正则抽不出 `sessions_default`（`relpartbound='DEFAULT'`）的月下界 ⇒ NULL 扫描崩溃。
**这两道跨月门一直红在这一行上，断言一行都没跑到。**
⚠️ 更隐蔽的：`ORDER BY lo DESC` 默认 **NULLS FIRST**，谁为了让扫描不崩而把 `lo` 改成可空，
默认分区就会被当成「最新分区」，夹具往错分区写、断言读错行，**且不报任何错**。
⇒ 两处都加「WHERE 排掉无月下界」+「ORDER BY … NULLS LAST 写死」。

**（b）夹具从未种下它声称要种的东西**
`bg` 账本夹具注释写「corrupt 200 instead of 190」，但余额是 running balance：
`90 + 110 = 200`，**200 本来就是对的**，四行 drift 全为 0，**一个断点都没种下**。
且账本 `consume` 行走默认 `created_at=now()`，被 10 分钟结算延迟上界排除 ⇒
`debited=0`，门拿到 `30 vs 0`——**一条夹具自己制造的伪差异**，不是注释声称的 `30 vs 25`。
⇒ 改余额不改编金额（第三行 201，第四行 151 自洽）；`created_at` 显式设为 1 小时前。

**（c）夹具助手不可重入**
`ensureFixtureTenant` 用 `ON CONFLICT DO NOTHING RETURNING true`，
冲突时 RETURNING 零行 ⇒ `ErrNoRows`。它的 doc 依赖「gate 库是空的（2026-10-02 实测 tenants=0）」，
**该前提早已不成立**（现有 11 个租户）⇒ 只在纯净库上能跑一次，此后永远红。

### ③ 两个故意保持红的门（环境缺口，没改）

- `TestProjectTasksSkipsNullTaskID`：**迁移 762 从未在本地库应用**。
  触发器函数 `sync_session_project_attr` 不存在；`schema_migrations` 288 行、含 `762%` 的 0 行；
  审计表 76x 无记录 ⇒ 不是回滚，是没跑。本地库是**部分迁移实例**（缺 762、缺 818+）。
- `TestReportRollup_HTTPContract`：`report_snapshots` **0 行**，是数据前提缺失。

⚠️ **我没有替属主应用 762，也没有把任何一个改成 skip。**
把如实报红的门改成永不执行的门，与 §9.171 撤掉「清单全覆盖」门是同一类错误：
**为了让门好看而让它不再说话。**

### ④ 变异（含一个必须单独说的）

M5 红 / M6b 红 / M7 红 / M9 红 / M10 红。**M6 绿**——但那是因为
**我写的变异不描述一个真缺陷**（drift 换成 `amount` 后被种下那行 `amount=110` 依然命中）。
**「变异绿」与「门无牙」是两个结论，中间隔着「这个变异是否真的描述了一个坏法」。**
M8 绿则证实旧的「只数数量」断言**对种类完全无牙** ⇒ 已补 `check_kind` 身份断言（M9 红证明它非恒真）。

### ⑤ 顺手修掉的一处自身失误

写文档时 heredoc 引入了 4 个坏字节，U+FFFD 从 4 变 8，当场发现并修回 4。
（依据是我自己写过的纪律：文档基线 U+FFFD 必须 = 4，**写完必须核**。）

### ⑥ 待你拍板

- **是否在本地库应用迁移 762**（幂等、有 `.down.sql`；不应用则该门永远红）。
- D19-a-1 / D19-a-2 / D19-b / D19-d / D20-a / D20-c / D17 / D15 / D13 / D12（同前）。

---

## §70.29（第八十七轮）47 个未判定读方的分布 —— 顺手推翻了 D19-a-1 的价值判断

### ① 结论先行

**D19-a-1（`outbound_model ← COALESCE(raw_model_name, model)`）单独做，18 个
`value-divergent` 读方里最多只能清掉 2 个。** 另 8 个只引用 `client_model`（不覆盖），
8 个两者都有（只修一半）。而 `client_model` **目前没有任何在案修法**。

### ② 分布（机器打印并对账，非手数）

47 个未判定 v1 读方：`value-divergent 18 | safe 21 | degraded 5 | no-columns 2 | empty 1`，
求和自检 47 = 47。反推已判定的 15 个：value-divergent 8、degraded 2、empty 4、gap-only 1、**safe 0**
——已判定的那批里一个 safe 都没有，与它们当初为何被登记一致。

### ③ 口径又踩了一次（已修）

我第一版手写 dump 得 **48**，主测试是 **47**。差在：主测试**先由 AST 判 v1 读方再抽列**，
我那份**对全部清单项抽列**。这正是 §9.171.4 记过的「两套扫描互相矛盾」。
⇒ 改成完全复用主测试的判定链 + 求和自检。

### ④ 新增棘轮门（唯一一个「计数承重」的地方）

此前本文件所有计数都明确不承重。`未判定` 不同：**判定只会让它变小**，所以是棘轮。
基线 47、成员逐个登记，触发时报出**是哪个文件**。
变异 M11（摘掉 `admin/work_types.go` 的登记）⇒ **红并指名该文件**。

### ⑤ ⚠️⚠️ 我这道新门第一版犯了和上一轮 `bg` 门一模一样的错

棘轮门最初只在**数量增长**时触发。但清单一**换货**（移除一个未判定 + 新增一个未判定）
时数量仍是 47 ⇒ 门静默放过。**数量能被交换满足，身份不能。**
上一轮我刚在 `bg` 门写下「数量不够身份」，隔一轮就忘了用在自己门上。
⇒ 改成基线是**集合**，断言 `当前未判定 ⊆ 基线`（单向：判定只会让文件离开）。
变异 M12（基线表把一个真名换成不存在的名，**数量仍 = 47**）⇒ **红**，
此时数量门全程静默，只有身份门报出 `admin/analytics.go`。
M11（数量涨）红、M13（只抬基线数）红。

### ⑥ ⚠️ 这不是「判定完成」

上表是**列级分类器对静态列**的结果，**不是对文件查询语义的审阅**。
分类器看不到「引用了但没暴露给任何人」，也看不到动态拼装 WHERE（§9.49 盲区）。
**我没有把这 47 个中的任何一个登记进 `retirementBreakers` / `retirementReattributed`**
——登记的含义是「已审阅并确认阻断」，而审阅没有发生。
**把它们按机器分布批量登记，就是把「算出来的」冒充「看过的」。**

⇒ **D20-c 的工作量没有减少**，只是从「未知」变成「可排期」。

### ⑦ 待拍板（新增）

- **D22-a**：把「追 `client_model` 会话侧真值」与 D19-a-1 **作为同一个决策包**吗？
  我的建议：是。只批 D19-a-1 会让退役判据 `value-divergent` 继续标红 16 个。

---

## §70.30（第八十八轮）三个自我更正 + 一个我前三轮都没跑过的包

### ① D19-a-1 的价值被高估（已进 D22 / D22-b）

47 个未判定读方跑一遍列级分类器：`value-divergent 18 | safe 21 | degraded 5 | no-columns 2 | empty 1`
（求和自检 47 = 47）。18 个按引用列拆开：
**仅 `outbound_model` 2 个（可修）/ 仅 `client_model` 8 个（不覆盖）/ 两者都有 8 个。**
⇒ **D19-a-1 单独做最多清掉 2 个。**
进一步查 schema：`client_model` **只在 `session_turn_details`**，`session_turns` 无对应列
⇒ 它**不可能是「投影错列」**，与 `outbound_model` 性质不同（后者是投影错列）。
⇒ **我原来「把两者捆成一个决策包」的建议是错的，已在 D22-b 更正为「D19-a-1 可独立推进，但不解退役判据」**。

### ② ⚠️ 我作废了自己刚写下的一个结论

我一度断言「本地 v1↔会话孪生行根本不存在，测不了分歧率」，
理由是我自建的连接（`request_logs_hot` 直连 `session_turn_details`）返回 0 行。
同一轮 `db` 包的门实测 **377 孪生行**。
**错因：我拿「按我选的键查不到」当成了「不存在」。**
与 §9.172「grep 命中/未命中都可能是量具错」同族。**已作废并改写。**

### ③ ⚠️ 我前三轮报的「全量回归」根本不含 `db` 包

`TestRepointValueFidelity`（§9.167 的值保真门 = `repoint-value-divergent` 判据的**实现**）
就在 `db/` 里。我前两轮只跑 admin/bg/cmd/gateway ⇒ **判据门从未被测量过**。
本轮首次纳入，它就是红的。
⇒ **「全量回归」这个词只有在我真的列出跑了哪些包时才能用。**

### ④ ⚠️ 我对那道门的归因也被自己的第二次测量推翻了

第一次 `101/377 = 26.8%`，我算二项标准误 ≈ 2.3pp，判定「只差 1.4 SE，统计上不显著」，
并建议**改成统计带**。
**再测两次，两次都是 `101/378 = 26.7%`。三次合计：
v1 腿行数 509 / 513 / 514（**总体在变，数据确实在动**），
而 `client_model_mm` **三次都是 101，一个不差**。**
⇒ 比「比率稳定」更强：**总体变化下分歧行数是不变量** ⇒ 不是逐行随机抖动，
而是一组稳定的行（为何稳定**未追查**：候选是 details 缺失行 / 10-01 前父表那批 /
某固定租户或日期cohort，**均未验证**）。
⇒ **不是噪声，是稳定的约 3.3pp 缺口** ⇒ `client_model` 的 `MinRate=30%` 很可能**一开始就定高了**。
⇒ **「统计带」这个修法基于错误诊断，已撤回。**
⇒ 正确顺序：**先把阈值改对（D23-a），再谈容差（D23-a-2）**；
只做后者只是把「门槛偏高」变成「门槛更宽地偏高」。
⚠️ 但我**无法证明** 30% 是「一直定高」还是「regime 变过」——§9.167 只留了结论没留原始数据。

### ⑤ 本轮交付

新增未判定清单**棘轮门**（数量 + **身份**两条断言，基线 47）。
⚠️ 第一版只判数量，被我上一轮刚写下的「数量不够身份」教训当场抓住并改正：
M12（基线表换名、数量不变）⇒ **红并指名 `admin/analytics.go`**，M11/M13 亦红。

**本轮零生产代码改动**（Go 改动只有 `admin/request_logs_reader_population_test.go`）。

### ⑥ 待拍板

**D23-a**（保真门 `client_model` 下限 30% 是否下调到 26.7%）/ **D23-a-2**（是否再加统计带）/
**D22-b**（D19-a-1 可独立推进但不退役判据，接受吗）/
**D21-a**（是否应用迁移 762）/ **D21-b** / **D19-b** / **D19-d**（252 只读授权）/
**D20-a** / **D20-c**。

---

## §70.31（第八十九轮）把那 101 行拆开：两列是两种缺陷，且我上一轮的降级建议是错的

### ① 方法（这一步本身是上一轮教训的复现）

门用的是**只按 `request_id`** 的口径（`has_twin` 是 EXISTS，JOIN 也不带 tenant/partition）。
我上一轮自建连接时多加了两个键，384 行筛成 0 行，据此断言「孪生行不存在」。
⇒ **「按我选的键查不到」与「不存在」之间没有推理关系。** 本节全部查询复制门的原句。

### ② 两列并排（实测，非推断）

| | `client_model` | `outbound_model` |
| --- | --- | --- |
| 孪生 / 分歧 | 384 / **101** | 390 / **103** |
| 视图侧 NULL | **101（全部）** | **0** |
| 两侧都有值但不等 | **0** | **103（全部）** |
| 时间性 | **有界**：03:34–06:18 缺 details 行，**06:18 已恢复**（07:00 后 264 行 / 0 分歧） | **持续到 11:00**（69 行 / 20 分歧） |

- `client_model`：**没有值失真**。101 条全是 details 行不存在 ⇒ LEFT JOIN 扑空 ⇒ NULL
  （已逐个确认：101 个 request_id 在 `session_turn_details` 里**一行都没有**）。
- `outbound_model`：**当下仍在写错值**。视图从不返回 NULL，每条分歧都是「两侧都有值但不同」。

### ③ 「101 为何三次测量一模一样」的答案

它是一批**固定的历史行**：03:34:25–06:17:57 之间约 **2h45m** `session_turn_details`
对成功请求没写行，**06:18 之后写入恢复**。**2026-10-05 06:18 后它会自行滑出 24h 窗口。**
⚠️ 「07:00 之后 0 分歧」**排除了空洞的 0**——那里有 **264 个孪生行**。

### ④ ⚠️ 我上一轮降级 D19-a-1 是错的

上一轮我写「D19-a-1 最多清掉 2 个读方」，据此把它降级。
「2」是**读方计数**，没错；但**据此推断它价值有限是错的推断方向**——
`outbound_model` 是**当下持续产生错误模型名**的投影缺陷。
⇒ **D19-a-1 应当优先，不是降级。** 只是它不会独自让 `value-divergent` 转绿。

### ⑤ D23-a 的答案：**否决下调下限**

26.7% 是**瞬时伪影**（有界事件尚未滑出窗口）。把它固化成门的常量是错的。
且 `client_model` 的分歧**不是值失真**（`both_set_diff=0`），
用「分歧率下限」描述一个**覆盖率**，语义本身就错。
正确做法：`client_model` 按**覆盖率**登记，`outbound_model` 按**值失真率**登记
（其下限 20% vs 实测 27.2%，**有效，保持不动**）。
⚠️ **本轮仍未改那道门**（它是退役判据本身）。

### ⑥ 待拍板

**D23-c-1 / D23-c-2（101 行要不要回填，不阻断退役）/ D23-c-3（D19-a-1 按优先推进？）**
+ D21-a / D21-b / D19-b / D19-d / D20-a / D20-c。

---

## §70.32（第九十轮）D19-a-1 定案：有效，但有两条腿的硬要求

### ① 缺陷比 §9.169 的样本严重

24h 窗口 106 条 `outbound_model` 分歧：**6 条（5.7%）仅大小写不同，
100 条（94.3%）是真正不同的模型名**。含 `glm-5-2-260617 → glm-5.1`（版本号都变）
与 `glm-5-3-flash-260828 → glm-5.3-flashx`（畸形名）——**不是任何归一化规则**。
⚠️ §9.169 那个样本是大小写差异，**低估了严重性**。

### ② ⚠️ 我给的「修法 100% 有效」是错的 —— 只覆盖 30%

我在**去重后的 26 个 request_id** 上验出「26/26 完全相等 = 100%」。
对账发现视图口径是 **87 行**。那 26 行**恰好就是修法唯一有效的子集**——
我测的正是能被我那条连接取到的行。
**「我能测到的子集」与「我的假设成立的子集」重合，30% 就被读成了 100%。**
本会话第二次栽在**样本被自己的连接筛出来**上（第一次是 §9.174）。

### ③ 61 行为什么测不到

它们在 **`session_turns_hot`，而它不是 `session_turns` 的分区**
（`pg_inherits` 查不到；分区是 `session_turns_2026_07`…`_default`），是独立的表。
我那条只查父表的连接**结构性**漏掉了它们。
⇒ 它们**不是「没有原样值」**，hot 里也有 `raw_model_name`。

### ④ 定案

视图的会话腿**读两张表各一条**（`pg_get_viewdef` 实查）。
在这 87 行上实测 `COALESCE(t.raw_model_name, t.model)`：
**hot 腿 61 + 父表腿 26 = 87/87 = 100%**，且这 87 行**无一行** `raw_model_name` 为空。

⚠️ **⇒ 迁移 825 必须同时改两条腿。** 只改一条会静默留下 70% 或 30% 的错值，
而保真门**仍然会红**（它测的是视图输出，不是投影改了几处）——
典型的「改了一处、测试还红、以为没改对」。

### ⑤ 待拍板

**D19-a-3-1**（批准同时改两条腿）/ **D19-a-3-2**（是否加「两条腿都用了 COALESCE」的
结构性门；**本轮未加**，因为它会立刻把当前 main 判红）+
D23-c-1 / D23-c-2 / D23-c-3 / D21-a / D21-b / D19-b / D19-d / D20-a / D20-c。

---

## §70.33（第九十一轮）推翻 §9.176：那不是写入缺口，是 join 不跨面

### ① 顺着 §9.177 的结构发现查下去

视图读 6 张表 = 3 对父子（`session_turns`/`_hot`、`session_turn_details`/`_hot`、
`request_logs`/`_hot`），**三对都成对覆盖 ⇒ 结构本身完整**。
§9.177 措辞里「两条腿」的暗示被修正：那是既定构造方式，不是缺陷。

但 details join 是**腿内配对**（viewdef 第 147/295 行），
三键 `tenant_id+request_id+partition_date` 两腿完全相同。

### ② ⚠️ §9.176 只查了父表，结论反了

那批分歧行：`session_turn_details` **0 行**、`session_turn_details_hot` **63 行（全部）**。
⇒ **数据是存在的。** §9.176 的「写入缺口、已恢复」**作废**。

### ③ 正面确认（非推断）

03:34–06:18 窗口 `session_turn_details_hot` 的 268 行：
**turn 在父表 268（100%）、在 hot 0；父表 turn 三键可配 268（100%）。**
⇒ **数据完全可配，只是跨面，而视图的 join 从不跨面。**

### ④ 被连带推翻的三条

1. §9.176「06:18 已恢复、不阻断退役」—— 06:18 是**落面分界点**，不是修复点。
2. **D23-c-2（回填 101 行）方向错误** —— 数据已存在，回填只会写重复行。
3. 「101 条滑出窗口后门自然转绿」**不成立** —— 只要两面切分有时间差，错配会再次出现。

### ⑤ 待拍板

**D24-a**（details join 跨面化？影响 30 个特征列，改动面比 D19-a-1 大）/
**D24-b**（还是从写侧保证 turn 与 details 同面落库？）/
**D24-c**（是否加「跨面行」结构门？**本轮未加**，它会立刻把当前 main 判红）
+ D19-a-3-1 / D19-a-3-2 / D23-c-1 / D23-c-3 / D21-a / D21-b / D19-b / D19-d / D20-a / D20-c。

---

## §70.34（第九十二轮）D24 定案：两个 hot 缓冲区切点差 2 小时，稳态失配带

### ① 规模

当前 **269 行**跨面（details 在 hot、turn 在父表），占 hot 特征层 **19.1%**；
**反向 0 行**。

### ② 根因：两个 hot 缓冲区的保留窗口不一致

| 表 | 冷热切点 |
| --- | --- |
| `session_turns`（父）/ `_hot` | **06:20:26** |
| `session_turn_details`（父）/ `_hot` | **04:15:45 / 04:17:23** |

**details 的 hot 缓冲比 turns 长约 2 小时** ⇒ 产生 [04:17:23, 06:20:26] 的失配带，
269 行全部落在带内（跨度 123.0 分钟 = 2h03m），
且**这些行的 details `ts` 与 turn `ts` 逐一相等**
⇒ **同一时间戳在两张表里被判到不同的面**。不是没写，是**晋升判定不一致**。

### ③ 解释了 §9.176 当时读不懂的现象

「06:18 之后归零」——**06:18 ≈ turns 切点 06:20:26**，是失配带的**上界**，不是修复点。

### ④ 是稳态，不是历史事件

带宽约 2 小时且**随新数据持续移动**。§9.178 的预测得到证实，
并给出了那条时间差 ≈ 2 小时。

### ⑤ ⚠️ 我没有追查「两窗口为何差 2 小时」

这是写侧/运维侧的设定，**需要单独一轮**。
若两窗口本就应一致（很可能）⇒ **D24-b' 治本且代价更小**；
若是两条独立且有意的策略 ⇒ D24-a 才对。
⇒ **建议先查清再定取舍**（D24-d）。**在本会话里我又一次选择了「先把事实查清」而不是「先给方案」。**

### ⑥ 诚实边界

本地 hot 表仅 1137/1406 行，**生产上带宽按流量放大**，绝对数不可直接外推。
生产 252 未授权。**本节无代码/视图/迁移改动。**

### ⑦ 待拍板

**D24-d**（先追查两窗口差异再定 D24-a/D24-b'）/ D24-a / D24-b' / D24-c +
D19-a-3-1 / D19-a-3-2 / D23-c-1 / D23-c-3 / D21-a / D21-b / D19-b / D19-d / D20-a / D20-c。

---

## §70.35（第九十三轮）D24-d：配置侧已排除，差异在执行侧；turns 侧方向相反

### ① 配置逐项相同 ⇒ 差异不在配置

两表同 `default` 分支、同一个键 `lifecycle.hot_retention_hours`（运行时值 **8**）、
同 `DefaultRetentionWindow = 8h`、promote 时间谓词与函数默认参数全同。

### ② 但两表都偏离配置，**方向相反**

| 表 | 配置 | 实测 | 偏离 |
| --- | ---: | ---: | --- |
| `session_turns_hot` | 8h | **6.5h** | 少 1.5h，**提前被搬走** |
| `session_turn_details_hot` | 8h | **8.6h** | 多 0.6h，**搬不动** |

promote 只搬「更老」的行 ⇒ **搬不动只会让窗口更长**，details 与此一致
（**积压**，与 R51 饥饿同族）。⚠️ **turns 侧方向相反，饥饿解释不了**
⇒ **有 promote 之外的东西在提前搬走它，本轮未定位。**

### ③ 因此 D24-b' 现在还不能定稿

若 turns 侧是「提前搬走」，**把 details 窗口调长去对齐 turns，只是把 6.5h 固化成 8h**，
失配带的方向与宽度随之改变，**不一定变小，甚至可能变大**。

### ④ 诚实边界

本地 hot 表仅千行量级，生产上积压行为会不同；生产 252 未授权。
**本节无代码/配置/视图/迁移改动。**

### ⑤ 待拍板

**D24-d-1**（先定位「谁在提前搬走 `session_turns_hot`」——我的建议：是）/
**D24-d-2**（若不查而直接选 D24-a 是否可接受）+
D24-a / D24-b' / D24-c + D19-a-3-1 / D19-a-3-2 / D23-c-1 / D23-c-3 / D21-a / D21-b / D19-b / D19-d / D20-a / D20-c。

---

## §70.36（第九十四轮）D24-d 结论：代码与配置都排除；D24-b' 前提不成立

### ① 排除清单

- `session_turns_hot` 上**无触发器、无规则**。
- 两表走**同一循环、同一句调用、同一 `resolvePromoteConfig`、同一 retention（8h）**。
- 两个 promote 函数**时间谓词逐字相同**，DEFAULT 都是 `'08:00:00'`。
- 迁移 688 修过**同一病灶**（SQL DEFAULT 7d vs Go 8h）并已把 turns 那个改成 8h；
  details 那个本来就是 8h ⇒ **688 修的是已修好的问题，不能解释今天的观测**。

⇒ 剩下的候选全在**操作侧**：手工 promote 脚本、`validate_sessions_v2/repair.go`
直写父表、以及**旧二进制**。

### ② ⚠️ 最重要的限定

**本地写入者是 823 前的旧二进制**（§9.163.2），本节所有读数都来自本地。
⇒ **本地这组观测可能整体不可外推**，「turns 侧被提前搬走」很可能不是生产现象。
⇒ **在拿到 252 只读授权前，把 269 行 / 19.1% / 2 小时当作生产缺陷规模来决策，不成立。**

### ③ D24-b' 降级

| 方案 | 依赖「谁在搬」吗 | 地位 |
| --- | :---: | --- |
| D24-a join 跨面化 | **否** | **任何成因下都成立** |
| D24-b' 对齐保留窗口 | 是 | ⚠️ **前提不成立**（配置与代码本就一致） |

⇒ 我的建议**改为**：**先拿 252 只读数据确认真机上是否存在这个失配带（D19-d）**——
**真机上没有这个问题之前，为它改视图是净新增风险。**

### ④ 诚实边界

「谁在提前搬走 `session_turns_hot`」**仍未定位**（但已证明不在配置/代码/触发器）。
`repair.go:260` 直写父表这条路径**本轮未审**（新增 D24-d-2）。
**无代码/配置/视图/迁移改动。**

### ⑤ 待拍板

**D24-d-1 / D24-d-2** + D24-a / D24-c + D19-a-3-1 / D19-a-3-2 / D23-c-1 / D23-c-3 /
D21-a / D21-b / **D19-d（252 只读授权，优先级已显著上升）** / D20-a / D20-c。

---

## §70.37（第九十五轮）`repair.go` 否证；本地数据与当前代码不自洽

### ① D24-d-2：`validate_sessions_v2/repair.go` 排除

它确实是从 v1 重建、**直写父表绕过 hot** 的回填工具（`source_kind='backfill'`）。
**但父表 1,688,629 行的 `source_kind` 实测：`live` = 1,688,629、`backfill` = **0****
⇒ **从未对本库执行过。排除。**

### ② ⇒ 本地数据与当前代码不自洽（把「不可外推」升级为结论）

- promote 只搬 `ts < now - 8h`；13:00 时 `now - 8h = 05:00`；
- 父表 `max(ts) = 06:20`（6.5h 前的行）⇒ **不可能是当前 promote 搬的**。

排除清单已完整：DB 无触发器/规则、配置同键同值、代码同一句调用、
两函数时间谓词逐字相同、函数体内**只有**那一个时间条件、回填工具 0 行。

⇒ **§9.179/§9.180/§9.181 的全部数字（269 行 / 19.1% / 2.15h / 6.5h vs 8.6h）
是「一份当前代码产生不了的数据」上的读数，不能作为生产缺陷规模，
也不能作为 D24-a / D24-b' 的取舍依据。**

### ③ 给 D19-d 一份**可执行验证清单**

在 252 上跑三条即可判定：①两表冷热切点都应 = `now - 8h`；②切点应相同
⇒ 跨面失配带应为 **0**；③`source_kind` 应无 `backfill`。
**三条都成立 ⇒ 撤销 D24，不改任何东西。**

### ④ 建议冻结 D24

**在 252 验证结果出来前，不为本地现象改视图。**
理由不是「稳妥」，而是**本地数据已被证明与当前代码不自洽**——
为一个当前代码产生不了的观测改生产结构，是在为一个伪像付真实成本。

### ⑤ 诚实边界

「本地数据是谁写的、何时写的」**仍未定位**，但**已不需要定位**：
无论是谁写的，**与当前代码不自洽**就足以否决「拿它当生产规模」。
**本节未连接生产**，无代码/配置/视图/迁移改动。

### ⑥ 待拍板

**D24-d-3**（按清单申请 252 只读验证——我的建议：是）/
**D24**（建议冻结 D24-a 与 D24-b'）+ D24-c / D19-a-3-1 / D19-a-3-2 / D23-c-1 / D23-c-3 /
D21-a / D21-b / D19-b / D20-a / D20-c。

---

## §70.38 §9.183：两面门上有一个**以错误理由排除**的目录，里面藏着真的漏读

### ① 结论

初筛「只读 `session_turns` 父表、不带 `_hot`」的 14 个非测试 Go 文件，
**13 个是假阳性，1 个是真缺陷**。但本轮的价值不在那个缺陷，
**在于初筛撞出了 `admin/session_family_two_surface_test.go` 的一个洞**：

```go
var sessionFamilySQLiteDirs = []string{"storage/sqlite", "tests", "installer", "cmd/tools"}
```

**`cmd/tools` 不是 SQLite 目录。** 该目录引用 `session_turns` 的 6 个非测试文件，
`sqlite` 出现次数**全为 0**，`pgx` 2/5/5/2/2/0，`public.session*` 1/10/0/5/1/1。
⇒ **一个完整的 PostgreSQL 目录不在两面门的覆盖内。**

收窄排除（只去掉 `"cmd/tools"`，其余不动）后，门立刻报 4 处，全在
`cmd/tools/validate_sessions_v2/repair.go`。

### ② 缺陷本体

`ExecuteRepair` 删 bodies 时：

```go
// Delete bodies
tag, err = tx.Exec(ctx, `DELETE FROM public.session_bodies WHERE tenant_id=$1 AND session_id=$2`)
result.DeletedRows["session_bodies"] = int(tag.RowsAffected())   // 单面
```

而 `session_bodies` **有 `_hot` 孪生面**（`bodies_writer.go:310/368` 写的就是它），
本机实测该表 **1990 行**。三条证据说明是**疏漏不是设计**：

1. **同一函数内不对称** —— turns 的删除注释「Delete turns from both stores」并把两面
   `RowsAffected` 相加；bodies 单面。相邻 20 行、同一批兄弟表，两种写法。
2. **计数与动作用了不同的面** —— `PlanRepair` 的计数走 `LoadV2Bodies`，
   读的是合并视图 `session_bodies_unified`（两面）⇒ **计划数两面、删一面**，
   差额静默留下。修完才第一次口径一致。
3. **紧接的重建只写父表** ⇒ 残留 hot 行 + 新插入父表行，经合并视图读出来是**重复的**。

**命中区间正是修复最常发生的区间**：会话最近一次写入 8h 内，其 bodies 必然还在 hot 面上。

### ③ 一个结构问题：登记不是那道修复的守卫

`sessionFamilyBareParentReaders` 是**按文件**索引的。`repair.go` 因剩余 3 个形状
（turns 的 DELETE 父表腿，hot 对偶在**紧邻上一条语句**，跨语句配对逐串判看不见；
两处 INSERT 是**写**方选择直落父表、非漏读）而整文件登记。
⇒ **§9.183.4 那条 bodies 修复修完之后无人看守**：删掉 hot 腿、计数改回单项，
`TestNoBareParentSessionFamilyRead` 依然全绿。

⇒ 补 `cmd/tools/validate_sessions_v2/repair_two_surface_test.go`，逐条断两件事：
两条 DELETE 各自的面覆盖 + 计数是**相加**表达式。**登记项的注释里已写明
「此处登记不得被当成那道修复的守卫」，并指明守卫在哪个文件。**

### ④ 判据自己踩了三个坑（都是红得没有意义的红，值得单列）

- **坑 1**：`pgx.Exec(ctx, sql, args...)` 的 SQL 在 **args[1]**，我写成 args[0] ⇒
  一条 SQL 都取不到（取到的全是 `fmt.Errorf` 格式串）。门红了，但红在**判据失效**。
  **是那条「一条都没取到 ⇒ Fatal」喊出来的**——「观察不到任何东西 ⇒ 无命中」
  是最危险的一种绿。
- **坑 2**：SQL 大写后 `HasPrefix` 小写关系名 ⇒ 两个面都报「缺」，
  **一条完全正确的删除被报成两条缺失**。误报方向是「全红」不是「全绿」，方向安全。
- **坑 3**：`containsBinaryExpr` 无脑穿透所有 `CallExpr` ⇒ 在
  `int(tag.RowsAffected())` 上下降进零参 `RowsAffected()` 并 **panic**。
  也就是说**「删除退回单面」这个恰恰要抓的变异，是崩溃呈现的**——
  崩溃也是红，但红不出「哪条性质被破坏」，会把人引去修判据而不是修代码。
  修法：只穿透**类型转换**（`Fun` 是 Ident 且恰一实参）。

### ⑤ 变异与回归

| 变异 | 结果 |
| --- | --- |
| M1 删掉 `session_bodies_hot` 那条 DELETE | **红，两条断言各自报出正确红因**，无 panic |
| M2 删除仍两面、仅计数改回单项（补 `_ =` 保证可编译） | **红，且只有计数那条红** ⇒ 两条断言可区分 |

M2 **第一次尝试是编译失败**（`declared and not used`）——编译失败也是红，
但门根本没运行，那个红不作数，故重做到可编译才采信。

回归（带真库，`llm_gateway` 库）：

| 状态 | `admin` FAIL | `cmd/tools/...` |
| --- | --- | --- |
| 带本轮改动 | **2**：`TestReportRollup_HTTPContract`、`TestProjectTasksSkipsNullTaskID` | 全绿 |
| `git stash` 后**同环境**基线 | **2**：**逐名相同** | — |

⇒ **零回归**；两条红都是已登记的 D21 环境缺口（迁移 762 未应用 /
`report_snapshots` 0 行），与本轮无关。

### ⑥ 环境事实（第二次被凭证骗，记下来）

本机测试库角色是 **`llm_gateway`**，密码取
`envs/common/database.yaml` 的 `COMMON_PG_SUPERUSER_PASS`。
**不是 `postgres`**，**不是 `kxuser`**（后两者均认证失败）。
用错角色会得到 **46 条 `password authentication failed`**，
看着像大面积回归，**其实一条代码都没跑**。
上一次是连接串漏密码。⇒ **验回归前先确认连接串能连通，再看 FAIL 数。**

### ⑦ 诚实边界

- **本轮没有跑过一次真实的 `ExecuteRepair`。** 守卫是**静态源码判据**，
  守的是「这段 SQL 被写出来了」，**不是**「运行时删干净了」。
  端到端断言（同会话 bodies 在两个面上都不重）**没有做**。
- **爆炸半径未量**：§9.182 测得父表 `source_kind` 的 `backfill` = **0**，
  提示本工具在本机可能从未运行过 ⇒ **缺陷是真的，本机无受害数据**；
  生产是否跑过、跑过多少次，**未验证**。
- `cmd/tools` 其余文件本轮只做了「是否 PG」的分类，**未逐条审 SQL 语义**。
  收窄后门绿 ⇒ 门看得见的形状都合法；**看不见的形状（拼装、裸名、跨语句配对）
  仍在盲区**，其中跨语句配对已知 1 处（turns 的 hot 对偶），已具名登记。
- **本轮未连接生产。** 全部读数来自本地。

### ⑧ 待拍板（沿用，未新增）

**D24-d-3**（按 §9.182.4 清单申请 252 只读验证——建议：是）/
**D24**（建议冻结 D24-a 与 D24-b'）+ D24-c / D19-a-3-1 / D19-a-3-2 /
D23-c-1 / D23-c-3 / D21-a / D21-b / D19-b / D20-a / D20-c。

---

## §70.39 §9.184：这套库执行不了产品自己在用的那个查询形状

### ① 一句话

`public.request_logs_bodies`（V1 body 存储，分区父表）一旦出现在
**子查询内的 `UNION ALL`** 里就报 `invalid perminfoindex 0 in RTE with relid 0`，
**12/12 确定性失败**，`PREPARE`/简单协议/`EXPLAIN` 三条路径全失败。
**只有这一张表坏** ⇒ **是本机 catalog 异常，不是产品缺陷。**

### ② 怎么撞上的

去跨 §9.183 留的那条边界（把静态守卫换成端到端实测）。造夹具时：
先撞 `provider_id` 是 bigint（**夹具写错，不是产品缺陷**），改对之后
撞上这个 —— 纯 psql 逐字复现，**Go 完全不参与**。

### ③ 影响边界（逐对实测）

| 父表 + `_hot`（子查询内 UNION ALL） | 结果 |
| --- | --- |
| **`request_logs_bodies`** | **FAIL-perminfo** |
| `request_logs` | OK（2,181,914） |
| `session_turns` | OK（1,689,885） |
| `session_turn_details` | OK（1,689,879） |
| `session_bodies` | OK（1,778,292） |

`session_turns` 是每请求都在跑的热路径、`request_logs` 是核心表，
**同样形状在它们上面完全正常** ⇒ **形状没问题。**

逐条排除：非 Citus 分片（`pg_dist_partition` 0 行）、非分区裁剪
（`enable_partition_pruning=off` 仍失败）、非分区树损坏（`pg_partition_tree` 完整）、
非 TOAST 缺失（`request_logs` 同样无 TOAST 却正常）、非协议、非列类型。
**机理未定位，不猜。**

### ④ 两次量具失实（这一节最该被记住）

1. **分类器没有默认分支**：`case … *perminfoindex*) FAIL ;; *) OK`，
   连接失败不匹配 ⇒ 判 OK。而那次普查脚本里 `PGPASSWORD="$PW"` 的 `PW`
   **从未被赋值**（我在脚本跑完之后才导出），12 次查询**全是连接失败**。
   ⇒ 整张矩阵「全部 OK」，我据此写下「只有 UNION ALL 子查询+分区表才触发」
   「Citus 元凶」「session_turns 正常」——**三句全部作废**。
   **与本会话早先那条「分类器不允许有默认分支」是同一条规则、同一个坑，
   而我在知道这条规则的情况下又踩了一次。**
2. **探针形状与失败形状不一致**：`count(request_id)` vs `count(*)`。
   同一张表先 FAIL 后 OK。**矛盾出现时先怀疑量具**——
   我当时还据此归因成「间歇性故障」，那是**第三个**错误结论。

代价：**我一度准备把 `v1BodyQuery` 改成两次独立查询**（那在本机确实能跑通）。
若照做，就是拿一个本机 catalog 异常去改产品代码
——**为伪像付真实成本**，正是 D24 冻结的理由。**没有改。**

### ⑤ 后果

1. **`validate_sessions_v2` 在本机从来跑不起来**（`LoadV1Turns` 必经
   `loader.go:115` 的 `v1BodyQuery`）⇒ `ExecuteRepair` **不可能成功执行过**。
   **加强** D19-a-2：不是「跑过没写 backfill」，是**压根跑不通**。
2. **§9.172–§9.183 的读数不受影响**（用到的表逐个实测正常，行数量级一致）。
3. V1 侧 body 存储读不出来 ⇒ **无法用它做迁移源交叉校验**，而这正是
   「退役 `request_logs`」主线的一环。

### ⑥ 交付：一条会响的门

新增 `admin/session_family_surface_readable_realdb_test.go`：每对存储面
**真的执行**生产同款形状，任一执行不了即报红，**按实际报错分类、无默认分支**，
开头 `Ping` 失败直接 `t.Fatalf`（不让它落进「形状可执行」）。
变异：移除异常对 ⇒ **转绿**（非恒红）；换不存在的表 ⇒ **`MISSING RELATION`**
（分类器可区分）。

**这是有意的第三条常驻红门**（前两条是 D21）。不提交它的替代方案是让一个
经实测的真缺陷只剩文档里的一句话 ⇒ **我选择让它响**。见 D25-b。

### ⑦ 诚实边界

- **§9.183 的端到端断言仍未做成**，本轮**再次确认**它被同一个 catalog 异常挡住。
  **两次尝试都未跨过**；§9.183 门头声明的「端到端未验」边界**依然有效**。
- **未改任何产品代码/配置/视图/迁移。**
- **未连接生产**；「生产是否也有此异常」**未验证**，不能由本节推断。

### ⑧ 待拍板

新增 **D25-a**（本机库重建/修复还是换库——建议先只读 catalog 排查）/
**D25-b**（第三条常驻红门保留还是撤——建议保留，修好后自动转绿）/
**D25-c**（生产是否也跑一遍，可并入 D19-d / D24-d-3 的 252 只读申请）。
沿用未决：**D24-d-3 / D24 / D24-c / D19-a-3-1 / D19-a-3-2 / D23-c-1 / D23-c-3 /
D21-a / D21-b / D19-b / D20-a / D20-c**。

---

## §70.40 §9.185：把「机理未定位」缩成「没有廉价修复路径」

### ① 目的

D25-a 要回答的是「**能便宜修好，还是只能重建/换库**」。
「机理未定位」答不了它，「哪些假设已被排除」可以。⇒ 只读排查，不写库。

### ② 10 类 catalog 假设，全部实测排除

RLS 策略 0 行 / 触发器 0 行 / 无效索引 0 行 / 父子列无差异（3 分区各 5 列、类型不一致 0、
已丢列 0）/ 分区边界 09→10→11 连续无缝、无 DEFAULT 分区 / 约束仅 4 个主键且全部 validated /
扩展统计·生成列·表达式索引 0 行 / reloptions 仅 autovacuum 常规 /
**时区**（库级本来就是 `Asia/Shanghai`，切 UTC 与切回读数完全相同，仍失败；
同轮 `request_logs` 对照正常）/ **分区裁剪开关**（`enable_partition_pruning=off` 仍失败）。

⇒ **没找到任何一条廉价的 catalog 修复路径。**

### ③ 故障形状（最尖锐的部分）

| 形状 | 结果 |
| --- | --- |
| 父表**无分区键谓词** + `UNION ALL` | **FAIL** |
| 父表**带分区键谓词**（全范围）+ `UNION ALL` | **OK（2,246,897）** |
| 无谓词 + `UNION`（去重） | OK（2,246,903） |
| 无谓词 + `EXCEPT` / `INTERSECT` / `IN` / `EXISTS` / `CROSS JOIN` | **全部 OK** |
| 对照 `request_logs` 每一个形状 | **全部 OK** |

⇒ **只有 `UNION ALL` 触发。** 两条判定式：
**① 分区键谓词的存在与否**决定能否跑通（行数完全一致）；
**② `UNION ALL` 是唯一触发的集合运算**。

失败点：**计划已生成，炸在执行器初始化**——`EXPLAIN` 在打印计划前就报错，
同轮 `request_logs` 对照能打出完整 `Parallel Append` 计划。

### ④ 有一条 workaround，**没有采用**

加一个冗余分区键谓词就能跑通，改 `v1BodyQuery` 约一行。
**没有改**——治的是一套库的 catalog 状态，不是代码缺陷
（其余四张表同形状全部正常）。与 D24 冻结同一理由：
**不为伪像付真实成本。** 记下来是为了属主要用时有完整依据。

### ⑤ 同样没做的（写明以免被当成遗漏）

`DETACH/ATTACH PARTITION`、`REINDEX`、`VACUUM FULL`、`CLUSTER`、重建分区
——共享开发库上的真实写操作，且本节目标是**判断有没有便宜的路**、不是动手。

### ⑥ D25-a 建议**变更**（本节的实际产出）

| | 建议 |
| --- | --- |
| §9.184 原建议 | 先只读 catalog 排查（成本低），定位不了再重建 |
| **§9.185 之后** | 只读排查**已做完且无所获** ⇒ **建议直接重建该库或换用另一套测试库** |

若要继续深挖，下一步应是**服务端侧取证**
（`debug_print_plan` / 服务端日志 / 该表历史 DDL 追溯），**不再是 catalog SELECT**。

### ⑦ 诚实边界

**机理仍未定位**——本节产出是**排除**与**形状刻画**，不是根因。
未连接生产；未改任何产品代码/配置/视图/迁移；未对本机库做任何写操作。
§9.183 的「端到端未验」边界**依然有效**，本节没有跨过它。

---

## §70.41 §9.186：边界终于跨过，并坐实 D25-a

### ① 换思路——验证不需要那套坏库

§9.183 修了 `repair.go` 漏 `_hot` 腿并配了静态判据；
§9.184/§9.185 两次想跨「端到端未验」这条边界都被本机库挡住。
**前两轮隐含假设「必须在 `llm_gateway` 上验」——这个假设本身没人验证过。**

`pg_dump --schema-only -n public`（79,014 行，含全部分区/视图/advisory lock 函数）
→ `CREATE DATABASE llmgw_probe_9186` → 灌入（3,402 对象建成，
14 个错误全是 `columnar` 访问方法与 `schema already exists`，与目标对象无关）。

### ② 决定性对照：同一套 DDL、同一服务端、同一条查询

| 数据库 | `UNION ALL` 子查询（`v1BodyQuery` 形状） |
| --- | --- |
| `llm_gateway`（用了两年） | **FAIL-perminfo** |
| `llmgw_probe_9186`（刚建，**同一套 DDL**） | **OK** |

⇒ **失败是数据/状态相关，不是 schema 相关。**
「表建错了」「分区定义有问题」被**直接证伪**。
⇒ **D25-a 从「排除清单支撑」升级为「正面证据支撑」：重建即可解决。**

### ③ 端到端门跨过去了

新增 `cmd/tools/validate_sessions_v2/repair_e2e_realdb_test.go`：
夹具把 bodies **劈在两个面各一行**，跑前先断**阳性对照**（两面各 1 行确实存在）。
三条断言：`DeletedRows==2` / hot 面归零 / 合并视图 `count(*)==2 且 count(DISTINCT turn_no)==2`。

**基线 PASS**（3 次连跑全绿、零残留）。
**变异 M1**（删掉 hot 那条 DELETE）**三条断言各自报红**：

```
DeletedRows["session_bodies"] = 1，应为 2
修复后 session_bodies_hot 仍有 1 行
合并视图实测 3 行 / 2 个不同 turn_no，应为 2 / 2
```

★ **3 行 / 2 个不同 turn_no** 这个读数说明 `count(DISTINCT)` 不是冗余：
若是「两份 turn_no=1 + 一份 turn_no=2」，数量恰好等于 2 会被放过。

### ④ 前置探针：让「跳过」不等于「通过」

跑之前先单独执行一次 `v1BodyQuery` 形状；跑不通就 `t.Skip` 并**指名**：

```
这套库读不了 V1 body 存储（ERROR: invalid perminfoindex 0 in RTE with relid 0）
⇒ ExecuteRepair 在其上不可能执行。本门在此跳过**不是通过**。
  同一条件下 admin.TestSessionFamilyTwoSurfaceUnionShapeIsExecutable 会**报红**。
```

⇒ **两道门分工而非重复**：§9.184 那条让环境缺陷**持续可见**（报红），
本条在库可用时**把修复验实**。
**这个联动是脆弱的**（删掉 §9.184 那条 ⇒ 本门 Skip 变成静默通过），
已写进代码注释就是为了让这种联动被看见。

### ⑤ 我自己的第三个量具错误：绿着的测试在**污染库**

第一次跑完探针库留下 `request_logs=6 bodies=6 bodies_hot=1 sessions=3 turns=6`
——**正好是三次运行的量**，清理从未生效。

**根因**：`defer pool.Close()` + `t.Cleanup(...)`。
Go 里 `defer` 在函数体返回时执行、**早于** `t.Cleanup` 回调
⇒ **清理时池已关闭**，每条 DELETE 全失败；而我又用 `_, _ =` **吞了错误**。

⇒ **测试断言全绿，同时把夹具留在库里。**
这是 §9.184.4「分类器没有默认分支」的**同族第三例**：
**失败被静默吞掉 ⇒ 门看起来是好的。**
三例共同形状：**量具的失败形态与被检验对象的失败形态不同形，于是失败被当成通过。**

顺带查出第二处：清理表清单含 `request_logs_bodies{,_hot}`，
而这两张表**没有 `tenant_id` 列**，清理报 `column tenant_id does not exist`
——**同样被吞掉**。

**修法**：①池在 `t.Cleanup` **内部**关闭；②清理**不吞错误**，清不掉就 `t.Errorf`；
③表清单去掉那两张并注明原因。
**验证**：连跑 3 次，每次 `ok` 且残留 `0/0/0/0/0`。残留已全部清除并复测为 0。
**一个污染测试库的测试，哪怕断言全绿也是负资产**——残留必须被测量，不能假定。

### ⑥ §9.183 那条边界：现在可以撤销

撤销**不是因为断言它成立，而是因为它现在有一个会红的运行时判据**。
**仍不覆盖**：`session_turns` 腿的两端读法（`claimAggregateTurn` 的父表优先 + hot 回退）
不在 `ExecuteRepair` 断言范围内。

### ⑦ 留下的新库（故意没删）

| 项 | 值 |
| --- | --- |
| 库名 | `llmgw_probe_9186` |
| 内容 | `llm_gateway` 的**完整 public schema**（**无数据**） |
| 重建 | `pg_dump --schema-only -n public` → `CREATE DATABASE` → 灌入 |
| 删除 | `DROP DATABASE llmgw_probe_9186;` |

⚠️ **没有数据** ⇒ 绝大多数真库门在它上面会因「查无此行」而红或跳过；
**只适合跑需要干净 schema 的结构性门**。
保留的理由：它是当前唯一一套「schema 相同且 `v1BodyQuery` 可用」的库。

### ⑧ 诚实边界

**未连接生产**；§9.186 证明的是**本机这套库的状态问题**，**不能外推**。
**未改任何产品代码/配置/视图/迁移**，本节新增的**只有一条测试**。
**未对本机库 `llm_gateway` 做任何写操作**。

### ⑨ 待拍板

**D25-a** 建议细化为：先**重建 `llm_gateway`**（数据要留），
`llmgw_probe_9186` 保留作结构性门专用库（**扶正为常驻库需先灌夹具数据**，独立一轮）。
**D25-b** 维持建议保留。**D25-c** 未变，可并入 D19-d / D24-d-3 的 252 只读申请。
沿用未决：**D24-d-3 / D24 / D24-c / D19-a-3-1 / D19-a-3-2 / D23-c-1 / D23-c-3 /
D21-a / D21-b / D19-b / D20-a / D20-c**。

---

## §70.42 §9.187：补上 §9.186 自己标的缺口（`session_turns` 腿的两端读法）

### ① 缺口

§9.186 补上了 `ExecuteRepair` 的端到端断言，并在诚实边界里写明：
**该门不覆盖 `claimAggregateTurn`（`session_aggregator.go:160`）的
「父表优先 + hot 回退」**。这条路径决定「这一轮 turn 算没算被快照消费过」，
**是去重语义的承重处**。

### ② 先核实那个「不对称」是不是缺陷

`parentExists` 判据是 `(tenant_id, request_id, partition_date)`，**漏了 `session_id`**，
而认领 UPDATE 带了 `session_id`。

⇒ **不是缺陷**：父表上有 `UNIQUE (tenant_id, request_id, partition_date)`
（`session_turns_tenant_request_partition_key`），`parentExists` 用的恰好就是这条唯一键。
本机实测 **1,688,629 行 / 1,688,629 个不同 `request_id`，完全唯一**。
**本门把这个前提一并锁住**——删掉那条唯一约束，用例 4 会先红。

### ③ 新门：5 种形状，基线 5/5 PASS

| 用例 | 形状 | 期望 |
| --- | --- | --- |
| 1 | 只在父表 | 认领**父表** |
| 2 | 只在 hot | 回退认领 **hot** |
| 3 | **两个面都有** | 认领父表，**hot 保持未认领** |
| 4 | 两个面都有、**父表已认领** | `false`，**仍不碰 hot** |
| 5 | hot 侧已认领 | `false`（幂等） |

**变异 M1**（删掉 `parentExists` 守卫）⇒ **只有用例 4 红**
（「hot 行被认领了 ⇒ 父表已认领时不得回退到 hot（会重复计入）」），
用例 1/2/3/5 仍 PASS。
★ **用例 3 在该变异下仍 PASS**（父表认领成功时走不到 hot）
⇒ **两个用例抓不同的坏法，不是同一件事的两个写法。**

### ④ 夹具：本包**已有**一次性数据库做法，我差点又重造一遍

`session_request_status_backfill_test.go` 的 `statusBackfillFixtureDB` 早就在，
且它的注释**已经写着我 §9.186 踩的那个坑**
（`defer pool.Close()` 早于 `t.Cleanup` ⇒ 清理静默失败、留下夹具
——「a trap this project has already hit once」）。

⇒ **我又踩了一次**，因为手工搭夹具而没先找现成 helper。
**本门直接沿用既有做法**，把「不留残留」从根上消掉。
已给 §9.186 那条门**补上引证注释**并写明它为何仍需在共享库上跑
（`ExecuteRepair` 要生产那套完整 schema，塞不进最小内联 DDL 的形状）。

### ⑤ 我这一轮的两个错

**错 1**：把 drop 的 `defer` 放进了 **helper 里**。
`defer` 属于它所在的函数 ⇒ **helper 一返回就把库删了**，调用方还握着池：
`FATAL: database "claimtwo_..." does not exist`。
既有 helper 只做 `defer admin.Close()`，**drop 留在测试里**——我把两半拼错了位置。
⇒ **抄既有做法要抄「为什么在这个位置」，不只是抄代码。**

**错 2**：`runClaim` 在事务里认领后 `defer tx.Rollback`（想每个用例从干净状态起步），
**同时又事后读库断言效果已置位**——回滚把效果撤销了，
三个子测试红在「`aggregate_applied_at` 未被置位」。
⇒ **那是我判据自相矛盾，不是被测对象的问题。** 改为提交；
隔离靠**每个子测试各自的 `request_id`**，整座库结束时被 drop。
⇒ **判据红了先怀疑判据**——这次它确实该被怀疑。

### ⑥ 回归

`domains/session/v2`：**FAIL=0 SKIP=0，ok**；`go build ./...` OK；
残留一次性库 **0**（`claimtwo_%` / `rsbfix_%` 均 0）。

### ⑦ 诚实边界

**未改任何产品代码/配置/视图/迁移**，本节新增**只有一条测试**（外加一处注释）。
本门**不覆盖**：并发认领（需并发夹具）、`upsertSessionSnapshot` 的聚合算术
（只覆盖「认领落在哪一面」）。**未连接生产。**
§9.186 那条门**仍在共享库跑、仍靠清理**——本节记录了这个权衡，**没有**改它。

### ⑧ 待拍板（沿用，无新增）

**D25-a / D25-b / D25-c / D24-d-3 / D24 / D24-c / D19-a-3-1 / D19-a-3-2 /
D23-c-1 / D23-c-3 / D21-a / D21-b / D19-b / D20-a / D20-c**。

---

## §70.43 §9.188：并发——两道机制不是冗余的，承重的是 `IS NULL`

### ① 起点

§9.187 诚实边界里点名的「并发认领未覆盖」补上了。
而 `claimAggregateTurn` 注释里那句「the durable, **concurrent-safe** claim」
**从未被验证过**。

先查窗口：`UpdateSession`（:113）在同一事务里先取 `sessionAdvisoryLockSQL`（:130），
再调 `claimAggregateTurn`（:134）。锁键 `public.session_turns_advisory_lock_key($1,$2)`
**全仓共用**（写方 :245、聚合 :130、repair.go、迁移 688 promote），已逐处核实。

### ② 我先写了一个**错的**前提，并被实测推翻

我一开始把两道机制当「冗余防御」，在注释里写：
*「只写一条并发测试没有意义——只有 A、B 同时失效才会红」*，
并据此宣称 **T1 区分不了 A/B**。

**变异矩阵直接推翻：**

| 变异 | T1（端到端） | T2（并行无锁） |
| --- | --- | --- |
| 基线（两道都在） | 绿 | 绿 |
| **A 完好 / B 破** | **红** | **红** |
| **A 破 / B 完好** | 绿 | 绿 |
| A 破 / B 破 | 红 | 红 |
| 还原 | 绿 | 绿 |

### ③ 定论：**B 承重，A 对正确性冗余**

我预测「删 B 时 T1 仍绿（有锁兜着）」——**实际也红了**。原因很具体：

> **advisory lock 只「串行化」，不阻止「顺序重复认领」。**
> 第 2..6 个调用者照样依次拿锁、依次各认领一次、依次各 +1。
> **删掉 `IS NULL`，有锁也照样重复计数。**

⇒ **`WHERE aggregate_applied_at IS NULL` 才是承重的那道**。
⇒ **advisory lock 对「计数恰好一次」冗余**（删掉它两条判据仍全绿）；
它的作用是**延迟/隔离**，**不是正确性**。
★ 顺带修正一处归因错位：注释把「concurrent-safe」记给那条 UPDATE 是对的，
但容易被误读成「锁也在保证正确性」——**它不是**。

### ④ T2 推翻后重新界定的价值

不是「隔离 B」（T1 已做到）。真正价值是**到达 T1 结构性到不了的场景**：
T1 里锁把事务**串行化** ⇒ 那 6 条 UPDATE **从不重叠**
⇒ **T1 根本没测到行级竞争**。T2 不取锁、6 事务真并行，测的才是
**「PG 的 UPDATE 拿到行锁后会重新检查谓词」** 这个原子性保证。
⇒ **T1 测性质**（`total_turns == 1`），**T2 测机制**（无串行化的行级竞争）。

### ⑤ 不 flake 的理由

两条都**只断最终库状态**，**不断时序**、不断「谁先谁后」。
正确实现下**每种交错**都同一结果 ⇒ 非概率断言。**实测 3 次连跑全绿。**

### ⑥ 诚实的记录方式

被推翻的结论**没有被悄悄改掉**：测试文件里**同时保留**
「错误前提」「实测矩阵」「推翻后的定论」三段，并注明**被实测推翻**。
理由：只留正确结论的注释，会让下一个人重新发明一遍那个错误前提。

### ⑦ 诚实边界

**未改任何产品代码/配置/视图/迁移**；新增**只有两条测试**（外加 DDL 扩展与注释改写）。
变异全部临时、已还原。T1／T2 **都不覆盖跨进程竞争**（只覆盖同进程多连接）。
**未连接生产**；全部实验在一次性数据库上。

### ⑧ 待拍板（沿用，无新增）

**D25-a / D25-b / D25-c / D24-d-3 / D24 / D24-c / D19-a-3-1 / D19-a-3-2 /
D23-c-1 / D23-c-3 / D21-a / D21-b / D19-b / D20-a / D20-c**。

---

## §70.44 §9.189：7 个归因列的 SQL 与它自己的注释相反（D26）

### ① 现象（逐列比对，非推断）

Go 字段注释（`session_aggregator.go:80-87`）对 **7 列**
（`ProjectID`/`APIKeyID`/`ApplicationID`/`EndUserID`/`OwnerUser`/`ClientIP`/`AgentName`）
写的是「**首值优先…后续轮不覆盖**」，而冲突臂是

```sql
project_id = COALESCE(NULLIF(EXCLUDED.project_id, ''), public.sessions.project_id)
```

⇒ **EXCLUDED 优先 = 最后一个非空值胜，与注释正好相反。**

### ② 为什么是「改到一半」而不是「注释过时」

**同一种形态作者已认定错误并修好两处**：
`AgentRole` 用 `CASE WHEN 存量='main' THEN EXCLUDED ELSE 存量 END`，
注释写「不能用 `COALESCE(NULLIF(EXCLUDED...))` 形态学一致」；
`PrimaryRequestID` 用 `COALESCE(存量, NULLIF(EXCLUDED,''))`，注释写
「R69 初版…EXCLUDED 优先即 last-write-wins…**翻转为存量优先**」。
**那 7 列没一起改。**

### ③ 本机暴露度（实测）

`public.sessions` **839,661** 行：`project_id` 非空 **1**、`agent_role<>'main'` **0**、
`primary_request_id` 非空 30,789 ⇒ **本地几乎零暴露**。
⚠️ **不能外推到生产。**

### ④ 成本漂移：实测**没有**（排除项）

`total_cost_usd` 是 NUMERIC、`CostIncrement` 是 float64，但 PG 的
`float8→numeric` 走**最短往返文本**（`0.1`→`0.1`、`0.1+0.2`→`0.3`）
⇒ **聚合层无漂移面**；真实漂移在上游调用方。

### ⑤ ⚠️ 改的时候的坑：只翻冲突臂会把这 7 列**冻成永远为空**

变异 M1 实测：改成真首值优先后值是 **`""` 而非首个值**。
原因：`VALUES` 首写臂存 `$13` ⇒ **空串不是 NULL**；
冲突臂 `COALESCE(存量, ...)` 判 **NULL** ⇒ 之后任何轮都过不了「存量为空」这关。
⇒ **冲突臂 + 首写臂 `''`→`NULL` 归一化必须同一次做。**
症状会从「归因错」变成「归因查不到」——**更隐蔽**。

### ⑥ 门：**如实刻画**，刻意不判红

`TestUpsertSessionSnapshot_ArithmeticAndPrecedence_Characterization` 6 子测试：
计数与成本累加 / `project_id` 被后轮覆盖（⚠️ 与注释相反）/ 空串保留旧值 /
`agent_role` 精化且不被降级 / `primary_request_id` 存量优先 / 租户隔离。

**刻意不把分歧写成会红的断言**——那等于替属主改行为。
变异：M1 FAIL=2、M2 FAIL=2、M3 FAIL=1；基线与还原 PASS=6。

### ⑦ 租户隔离：第二道防线在正常路径上**不可达**

原用例前提不成立：`sessions` 的唯一约束是
**`UNIQUE (session_id, partition_date)`，不含 `tenant_id`**
⇒ 跨租户复用 `session_id` 被数据库**直接拒绝**（SQLSTATE 23505）。
⇒ 冲突臂末尾 `WHERE tenant_id = EXCLUDED.tenant_id` 是**纵深防御**，
正常路径走不到。**我原以为它在防「他租户混写」，实测第一层就拦住了。**

### ⑧ 我这一轮两个错（都是我的前提/计数错）

① 租户用例的前提被唯一约束拒绝；② `total_turns` 断言写 9、实际 8 轮。
两处都是**先怀疑判据**才对的。

### ⑨ 回归与边界

包级 `ok`；`go build ./...` OK；残留一次性库 **0**；3 次连跑全绿。
**未改任何产品代码/配置/视图/迁移**；分歧**只记录未修复**（属主决定）。
**未连接生产**；暴露度是这一套库的数字。

### ⑩ 待拍板

**D26-a**（建议：先取生产只读看真实填充率再定；改则冲突臂+首写臂同一次做）/
**D26-b**（建议：接受「先刻画、后改契约」的两步走）。
沿用未决：**D25-a / D25-b / D25-c / D24-d-3 / D24 / D24-c / D19-a-3-1 / D19-a-3-2 /
D23-c-1 / D23-c-3 / D21-a / D21-b / D19-b / D20-a / D20-c**。

---

## §70.45 退役进度实测 + 互锁门 + 补完最后一个读点（107/107）

### ① 退役进行到哪一步：**仍在双写**

两个活的生产写方（`domains/hooks/observability/telemetry/client.go`、
`admin/telemetry.go` 写 `request_logs_hot`），`request_logs_hot` 的 `max_ts`
距测量时刻 **86 秒**。停写开关 `storage.request_logs_write_enabled`
`Default: true`、HotReload、覆盖很全（15 个测试文件引用），
但**真库 `settings_kv` 该键行数 = 0 ⇒ 取默认 true**。

### ② 逐点评估：106/107 → **107/107**（未评估 0）

`admin/usage_enhanced.go` 是最后一个。判 `effectSilentlyDegradedAggregate`。

### ③ 它的四个读点，三个不受影响、一个真退化

- **① `work_type` 维度：退化。** 视图里 `work_type` 非空的 4,357 行**全在 v1 臂**，
  **session 臂 31,222 行 100% NULL**；复核到源头表 `session_turns` 29,620 +
  `session_turns_hot` 1,603 行同样全 NULL ⇒ 710 的「直映」是忠实实现，
  **缺的是写方从不填 `session_turns.work_type`**。停写后该维度塌成只剩 `unknown`。
- **② `intent`：不受影响。** `session_summaries` 仍增长（7 天更新 3,852 行），
  session 臂 `gw_session_id` 实测 0 NULL。
- **③ 压缩请求数：不受影响，方向与直觉相反。** `compression_strategy`
  v1 臂 **0 / 46,398** vs session 臂 **4,796 / 31,223** ⇒ 今天就只由 session 臂供数。
- 幅度：停写少掉的 4,359 行 = **5.6% 行 / 4.20% token**。
  ⚠ 本地 `cost_usd ≈ 0`，**美元占比本机测不了**。

### ④ 独立佐证

把本条改判 `effectUnaffected` 立刻被族门判红，族 =
`reads_view_with_null_padded_predicate` ⇒ **机械分类器不靠我的读码
也已把 `work_type` 算进 session 臂 NULL 补位列集合**。

### ⑤ ⚠️ 我这一轮自己犯的三个错（都是我的前提/读数错，不是代码问题）

1. **把「NULL 计数 = 总行数」读成「填充率 100%」**——那一列恰恰是一列都没填。
2. **算出「47% 的计费成功请求没有 session 镜像」**——口径含探针流量，
   而**探针带 token**；789 条里 **784 条是 `probe_triggered`**。
   2026-10-02 已写在 `measurementCaveat` 里的结论是对的，我差点用错数字推翻它。
3. **把 `ClassifyInternalLoopback` 读成「四臂与」，实际是「三臂或」**
   （首个命中臂即 return）⇒ 差点把**按设计排除的内部回环**报成「镜像漏写」并开 P0。

### ⑥ 互锁门，以及它自己一个真实缺陷

新增 `admin/request_logs_stop_write_interlock_test.go`：把
「先补完评估才允许关停」从约定变成不变量（纯函数判定 + 接线阳性对照 + 真实库不变量）。
基线 3/3 PASS，变异 M3（恒真化）红因正确。

⚠ **第一版的逻辑自检有个真缺陷**：前提用
`todo := unclassifiedStopWriteReaders(); if len(todo)==0 { Skip }`。
**§9.191 把最后一个读点评估完之后，这条自检会永久 Skip**——
而它恰是唯一能证明互锁没退化成恒真门的那条。
⇒ 互锁会在**它刚开始变得重要的那一刻静默作废，且毫无信号**。
修法：自检**自带合成前提**（编造假读点名），恒定可构造。
**判据的有效性不能挂在「被检验对象当前恰好处于某个状态」上。**
M3 就是在未评估清单归零**之后**跑的，直接证明修复有效。

### ⑦ 回归与边界

`admin` 包回归 FAIL 名单与基线逐名相同（D21 两条 + §9.184 存储面可读性一条），
**未新增**；`admin/dashboardapi|dashboarddegrade|distlock` ok。
**未改任何产品代码/配置/视图/迁移**；**未连接生产**。
本地 `cost_usd ≈ 0`、无客户端发 `X-Gw-Work-Type` ⇒
**「session 侧 `work_type` 无供给」本地为真、生产未知**。

### ⑧ 待拍板

**D27-a**（`silently_degraded_aggregate` 的灰度清单排除是否覆盖「维度取值集合塌缩成单值」这一形状；三选一，建议 ② 逐形状登记）/
**D27-b**（把生产 `session_turns.work_type` 填充率列入 252 只读清单，建议与 D24-d-3 合并）/
**D27-c**（若生产确认由客户端头驱动，是否把「session 族补齐 `work_type` 写入」列为 S4 硬前置）。
沿用未决：**D26-a / D26-b / D25-a / D25-b / D25-c / D24-d-3 / D24 / D24-c /
D19-a-3-1 / D19-a-3-2 / D23-c-1 / D23-c-3 / D21-a / D21-b / D19-b / D20-a / D20-c**。

---

## §70.46 S4 前置条件一达成、前置条件二转红，并挖出一个**今天就已存在**的读迁移不一致

### ① 前置条件一（逐点评估）**已达成且机器可验**

`go test -tags s4audit ./admin/ -run 'TestRequestLogsStopWriteNothingLeftUnclassified|TestRequestLogsControlPlaneNothingLeftUnreviewed'`
⇒ **两道硬门全 PASS**（读端 104/104 + 控制面轴）。
对外数字「静默档 **70** 条」仍成立（本轮新增的 1 条登记落在被显式排除的档上）。

### ② 前置条件二**是红的**：`db.RetirementUnservableColumns` 在 session 臂 0 供给

真库 24h：视图 session 臂 3,449 行 / v1 臂 6,329 行；
`client_protocol` s=0/v=38、**`is_final_success` s=0/v=6,329**、`work_type` s=0/v=3。

7 天全量逐列普查（118 投影列）：session 臂 0 供给 24 列，其中 **20 列 v1 侧也 0
（无信号）**、3 列 v1 侧有值（+ `id`/`test_col` 结构缺口）。

⚠ **框架别读反**：视图按 `request_id` 去重（v1 臂带 `NOT EXISTS(session_turns*)`），
**业务行的值今天就来自 session 臂** ⇒「session 臂不供某列」是**既有事实，不是停写回归**；
停写回归是**行**的消失。

### ③ 独立复核：与 §9.161 既有结论**逐列吻合**

`db/retirement_column_exposure.go`（今天写的）已登记同样三列
⇒ **我差点把 §9.161 重做一遍**。这次重复反而证明那张表**可被独立测量复现**
（它不是注释，是可被推翻的事实）。

### ④ 真正的发现：`is_final_success` 的**读迁移前后不一致**

| 源 | 行数 | 非空 |
|---|---|---|
| `session_turns` ∪ `session_turns_hot` | 31,274 | **0** |
| `request_logs` | 73,264 | **9,614** |

`admin/session_online.go:446` 直读 session 族原生表取
`COALESCE(rl.is_final_success, FALSE)` ⇒ **恒 FALSE** ⇒
`deriveTurnOutcome` 里 `final_success` 与 `superseded_success` **永不出现**。
⇒ 自读迁移之后，`GET /api/admin/sessions/{id}/timeline` **再没标出过最终成功轮次**。

**根因是写侧**：`claimSessionFinalSuccess`
(`domains/hooks/observability/telemetry/client.go:2711` 起) 只 `UPDATE request_logs_hot`，
**session 族无等价写方**。读路已由新门证明是好的（排除投影缺陷）。
⚠ 这是**代码事实**，不依赖任何库，**可直接外推到生产**。

**停写不修它，只让它变永久**：v1 停写后连 v1 侧那 9,614 个标记也没了，
而 session 族补不上 ⇒ `final_success` 在系统里**彻底不可表示**。

### ⑤ 顺带核实：`client_protocol` **全仓无人读取**

grep 全仓非测试代码：全部出现都是**写侧**（`telemetry/context_attrs.go:162/:192`、
若干 executors 的 log fields）+ 710 投影 `t.client_protocol::character varying(50)`，
**没有任何 SELECT 读它**。⇒ 它在 unservable 清单里但**不会让任何读方断掉**。
⚠ 二阶事实：无人消费的列留在「会断的列」清单里会让风险清单虚高。

### ⑥ 我这一轮自己犯的错

1. **把逐列测量的框架起错名字**（「停写即消失」）⇒ 差一步发出方向相反的结论。
2. **变异「没打红」的第一反应必须是「变异没做」**。读路门连输两次缩进不匹配；
   第二次 Python `assert` 把「锚点没找到」报出来才去看缩进
   ⇒ **替换之前先断言锚点存在**。
3. **`validIdent` 分支顺序让首位数字通过**（`"1abc"` 过关）
   ⇒ **判据的分支顺序本身就是判据的一部分**。
4. **差点重做 §9.161** ⇒ 先 grep 既有机制再动手。

### ⑦ 门与变异

| 文件 | tag | 基线 |
|---|---|---|
| `admin/session_final_success_readpath_realdb_test.go` | 无（常跑） | **PASS** |
| `admin/s4_session_family_unservable_realdb_test.go` | `s4audit` | **FAIL**（红因精确） |

读路门：一次性事务种 `TRUE`/`FALSE` 双向、用**生产 SQL** 读回、全程 ROLLBACK
（结构上不可能污染库；已核探针库 `session_turns` 仍 0 行）。
变异：反断言 ⇒ **双向各报一条红**（证明投影没写死任一值）。
S4 门：**M-A**「两侧都空」⇒ **转绿**（非结构必红）；**M-B** 最小样本量抬到 1000 万
⇒ **指名 SKIP**（零样本护栏承重）。护栏已在无数据的探针库**实地生效**。

### ⑧ 回归与边界

`admin` 回归 FAIL 名单**与基线逐名相同**（3 条），**本轮零新增**；`go build ./...` OK；
gofmt 干净；U+FFFD 三基线守住（4/0/0）。
**未改任何产品代码/配置/视图/迁移**；**未连接生产**。
补 `session_turns.is_final_success` 写方 = 改生产行为（决策表 **D28**），本轮未做。

### ⑨ 待拍板

**D28-a**（双写 / 只在 session 侧认领 / 接受现状并下线两个 outcome——建议 ①，
但**必须连带 session 侧唯一性约束**，否则会同时标出两个 final_success）/
**D28-b**（历史回填：建议**不单独做**，先做对写方再定）/
**D28-c**（`work_type` 见 D27-c；`client_protocol` 已核实无人读，建议移出清单并写明理由）。
沿用未决：**D27-a / D27-b / D27-c / D26-a / D26-b / D25-a / D25-b / D25-c / D24-d-3 /
D24 / D24-c / D19-a-3-1 / D19-a-3-2 / D23-c-1 / D23-c-3 / D21-a / D21-b / D19-b /
D20-a / D20-c**。

### ⑩ 下一轮提示词

1. `git fetch && git rev-parse origin/main`（当前 `8a7e90c94`）；
   `git worktree add --detach /tmp/<新> origin/main`。
2. 跑 `go test -tags s4audit ./admin/ -run 'TestRequestLogsStopWriteNothingLeftUnclassified|TestRequestLogsControlPlaneNothingLeftUnreviewed|TestS4SessionFamilyCanServeUnservableColumns'`
   ⇒ 前两条应绿、第三条应红（真库）。**若第三条变绿，先怀疑 `llm_gateway` 是否被重建过**。
3. 回归基线：`admin` 带真库 FAIL = 3（`TestReportRollup_HTTPContract` /
   `TestDimensionNamesQueriesRunAgainstRealSchema` / `TestProjectTasksSkipsNullTaskID`），
   **在同一环境 before/after 比对**，只看差集。
4. D28 的三个选项都需要属主拍板；**在拍板前不要动写侧**。
5. 若属主要 D28-c 推广：把「grep SELECT 侧引用」跑遍
   `db.RetirementUnservableColumns` + `RetirementDegradedColumns`，
   找出**没有读方消费**的列，单独立一张「写侧但无人读」清单。

---

## §70.47 退役清单的读方普查：新增 1 门，**推翻**我上一轮的一个结论 + 推翻共享提取器的自述

### ① 本轮做了什么

给 S4 退役三张清单（unservable 3 / degraded 24 / structural 5，合计 32 列）
补上后半个问题：**「这一列有人在读吗」**。原本只回答「退役后会怎样」。
理由：无人读取的列归哪一档都不伤害任何人，留在「会断的列」清单里只会让清单虚高，
而**虚高的清单会被整体折扣**。

### ② ⚠️ 推翻我上一轮的结论：`client_protocol` **有读方**

上一轮（§9.192.10 / D28-c）我写「全仓无任何 SELECT 读它，建议移出清单」——**错**。
错因：用**逐行** `grep "SELECT" | grep client_protocol`，
而 `admin/logs.go:204` 的 `rl.client_protocol` 在**跨行**的 SQL 字面量里
（查询由 `requestLogsListCols` + `requestLogsJoins` + `requestLogStatusExpr`
三段常量拼接）。已核实：该文件主日志列表走 710 视图 SELECT 它并下发到 JSON 字段。
⇒ **移出清单的建议作废**；D28-c 改为「两列都留在清单里」，
并把「主日志列表今天对业务请求就返回空 `client_protocol`」记为已知既有降级。

### ③ ⚠️ 顺带推翻共享提取器的自述：「只会多报，不会漏报」**不成立**

`extractV1ReadingLiterals` 按**单个字符串字面量**建别名表。
拼接查询里含列名的那段（投影清单）**自己不带 FROM** ⇒ 别名表为空 ⇒
`columnAttribution` 返回 `attrNone` ⇒ **该列从未被归因**。
实测：`admin/logs.go` 在暴露报告里 `definite` 只有 2 列，缺的正是这整段投影。
⇒ **少报的方向恰好是「让读点看起来安全」。** 修它会改已公布数字 ⇒ **D29-a**（属主决定），
本轮**不动**共享提取器。

### ④ 我为写这道门迭代了 **4 版判据**，每一版都被实测打掉

| 版 | 判据 | 实测 |
|---|---|---|
| v1 | 逐行 `grep SELECT` | **漏** `client_protocol`（跨行字面量） |
| v2 | 字面量含 `SELECT` | **漏**（投影段没有 SELECT） |
| v3 | 含 `SELECT`、不排除 INSERT | **多报** `client_forwarded_for`（巨型字面量同含 INSERT 与 SELECT） |
| v4 | 整文件字面量**合并**后判读 | 造出「假语句」——真实 `;` 不在字面量里，`WHERE … $` 读区吞到合并文本末尾 |

最终：**逐字面量 + 读区（SELECT…FROM / WHERE / GROUP BY / ORDER BY）
+ 写语句整条跳过 + 「投影段形状」兜底**（≥3 逗号项、含 `AS` 或点号、
不含结构关键词）。后者能认出 `requestLogsListCols`，
又因 `canonicalColumnOrderV2` 每元素是**单个**裸名（逗号不在字面量里）而被排除。
★ 失败形态不对称 ⇒ 偏向「认得出读方」（门的失败形态是误报，不是漏报）。

### ⑤ 新门与结果

`admin/request_logs_retirement_column_reader_gate_test.go`（常跑）
默认拒绝 + 具名登记；**登记过期也会红**（逼人销账）。
32 列 ⇒ **5 列确无生产 SQL 读方**：
`test_col` / `test_tab_indent`（测试占位）、`stream_chunks_sent`（只有写方；
`handler.go:6255` 读的是内存 map）、`client_forwarded_for`（只有写方）、
`quality_fix_actions`（只出现在 `db/db.go` 的 **DDL**：`SET storage` 清单 :2056、
`ADD COLUMN` :2158）。
变异 **M1** 删登记 ⇒ 红并点名；**M2** 清空理由 ⇒ 红并指名「登记必须写机制」。

### ⑥ 我这一轮自己犯的错（4 个）

1. **逐行 grep 下结论**（②）。SQL 字面量常跨行 ⇒ 逐行 grep 对「某列是否被读」不可用。
2. **先动手后 grep**：自建了 `goStringLiterals`，编译报错才发现本包已有同名 AST 版。
   ⇒ 复用既有 helper 这条规矩本轮又被我违反一次。
3. **合并文本制造假语句**（v4）。拼接的正确解法不是合并文本，是识别「投影段」形状。
4. **判据分支/终止条件的副作用**：`WHERE … $` 的终止于文末在合并文本里跨界 ⇒
   静默多报。**终止条件本身是判据的一部分。**

### ⑦ 回归与边界

待补（见本轮提交信息）。**未改任何产品代码与共享提取器**；**未连接生产**。
但「`admin/logs.go` 投影段未被归因」与「`client_protocol` 在该查询里被 SELECT」
都是**代码事实**，可直接外推。

### ⑧ 待拍板

**D29-a**（是否把 `extractV1ReadingLiterals` 的别名表改成文件级并集——
会改 §9.161/§9.162 已公布数字，只会变多；建议修，并在同一 commit 重跑三道门、
新旧数字并列写进审计）/
**D29-b**（是否专项普查「SQL 常量被 `+` 拼接」的查询有多少处，建议与 D29-a 同 commit）/
**D29-c**（5 条具名登记是否需人工复核：建议不需，门已把机制写死且空理由会红）。
沿用未决：**D28-a / D28-b / D28-c（已改写） / D27-a / D27-b / D27-c /
D26-a / D26-b / D25-a / D25-b / D25-c / D24-d-3 / D24 / D24-c /
D19-a-3-1 / D19-a-3-2 / D23-c-1 / D23-c-3 / D21-a / D21-b / D19-b /
D20-a / D20-c**。

### ⑨ 下一轮提示词

1. `git fetch && git rev-parse origin/main`；`git worktree add --detach /tmp/<新> origin/main`。
2. 跑 `go test ./admin/ -run TestRetirementListedColumnsHaveAProductionReader`（应绿）。
3. `s4audit` 三条门：前二绿、第三红（真库）。
4. 回归基线：`admin` 带真库 FAIL = 3，**只看差集**。
5. **若决定做 D29-a**：先量「拼接式查询」有几处（grep SQL 常量的 `+` 拼接），
   再改别名作用域，然后在**同一个 commit** 里重跑
   `TestRequestLogsRetirementExposure` / `…BreakersRegistryIsConsistent` /
   `…RepointVerdict`，把新旧数字并列写进审计。
6. **D28 拍板前不要动写侧。**

---

## §70.48 D29-b 专项普查：把「可能少报」变成确切数字

### ① 目标

§9.193 证明了提取器在拼接式查询上漏归因，但只给了一个样本。
D29-b 要的是**范围**——好让属主用数字拍板 D29-a。

### ② 我先试了做成门，**失败了**（这个失败比结论更值得记）

「默认拒绝 + 具名登记」的门，判据三轮：

| 版 | 判据 | 命中 |
|---|---|---|
| v1 | 片段须含 SQL 关键词 | 12（**却漏掉 `admin/logs.go` 的 `client_protocol`**） |
| v2 | 放宽到「含列名即可」 | 276（绝大多数是结构体 tag） |
| v3 | 收紧为「像查询的一部分」 | 171 |

⚠ **v1 第一次运行就复现了它自己要记录的失效形状**：
`requestLogsListCols` 是纯投影清单、**一个 SQL 关键词都没有**，
被「须含 SQL 关键词」滤掉 ⇒ 判据与缺陷是同一种病。

**171 条登记不能要**：没人会维护；没人维护的清单等于没有清单，
还会给人「已经管过了」的错觉。**正确做法是修掉整类，不是枚举它。**

### ③ 改为探针（不判红，但敢在自己坏时红）

`admin/retirement_exposure_attribution_gap_probe_test.go`
唯一红条件：**探针自己坏了**（扫到 0 个读方文件 / 分母 < 50）。
理由：漏归因的正确值是 0，把已知坏值写成期望值**等于把缺陷冻进断言**。
变异 **M1**（扫不到文件）⇒ Fatal；**M2**（分母地板抬到 1000）⇒ Fatal。

### ④ 普查结果（分母 = 读方清单 107）

| 口径 | 文件 | 「列×文件」对 | 不同列 |
|---|---|---|---|
| A（含 `id`） | **29** | **66** | 20 |
| B（剔除 `id`） | **21** | **49** | 19 |

`admin/logs.go` **一个人漏 13 列**（暴露报告里它 `definite` 只有 2 列）。
`id` 必须单列：提取器自陈它是「假阳性磁铁」，常以 `AS id` 派生名出现。

⇒ §9.161/§9.162 的暴露报告**在这 21 个文件上少报 49 对**，
且方向是「让读点显得安全」。

### ⑤ 本轮我自己的错

1. **判据第一次运行就复现了它要记录的那个缺陷**（见 ② v1）。
2. **把 171 条登记当成方案**，直到想清楚「没人维护的清单等于没有清单」才放弃。
3. 探针初版有 `hitA` 未使用（编译期才发现）——小，但说明我跳过了 `go vet` 的第一遍。

### ⑥ 回归与边界

见提交信息。**未改共享提取器、未改已公布数字**；**未连接生产**。
但「哪些文件的查询是拼接的」是**代码事实**，可直接外推。

### ⑦ 待拍板

**D29-a**（是否把别名表改成文件级并集；影响面 = 21 文件 / 49 对，**只会变多**；
建议**修**，且修完后 §9.194 的探针分子应降到 0，可当验证器用；
**修法不要用「合并全文文本」**——本轮实测 v4 那样会造出假语句）/
**D29-b**（**本轮已回答**：107 个读方里 21 个有漏归因面，处置随 D29-a，无需逐个登记）/
**D29-c**（5 条具名登记不需人工复核，但新增读方必须回来销账）。
沿用未决：**D28-a / D28-b / D28-c / D27-a / D27-b / D27-c / D26-a / D26-b /
D25-a / D25-b / D25-c / D24-d-3 / D24 / D24-c / D19-a-3-1 / D19-a-3-2 /
D23-c-1 / D23-c-3 / D21-a / D21-b / D19-b / D20-a / D20-c**。

### ⑧ 下一轮提示词

1. `git fetch && git rev-parse origin/main`（当前 `29b2aebd0`）；
   `git worktree add --detach /tmp/<新> origin/main`。
2. 若决定做 D29-a：
   - **先**跑 `go test ./admin/ -run TestProbeRetirementExposureAttributionGap -v`
     记下基线数字（21 文件 / 49 对）；
   - 改法 = `extractV1ReadingLiterals` 里先收集**全文件**别名并集，
     再逐字面量 `columnAttribution`。**不要**把文件内字面量合并成一段文本；
   - 改完**同一个 commit** 里跑：探针（分子应降）、`TestRequestLogsRetirementExposure`、
     `…BreakersRegistryIsConsistent`、`…RepointVerdict`、
     `TestAuditDocSilentClaimMatchesRegistry`，**新旧数字并列**写进审计。
3. 回归基线：`admin` 带真库 FAIL = 3，**只看差集**。
4. **D28 拍板前不要动写侧。**

---

## §70.49 把 D29-a 变成一步操作，并**推翻我上一轮对它的建议**

### ① 做了什么

`extractV1ReadingLiterals` 参数化为
`extractV1ReadingLiteralsScoped(t, path, fileScope)`，**默认口径逐字不变**
（暴露报告五桶 `4/1/27/4/70` 与三道门全部原样通过），
让「翻默认值」成为一步可做、结果可核对的操作。

### ② ⚠️ 我上一轮的修法建议**基本没用**

把探针分子改成「两个口径都没归因」（修完该降到 0）：

| 口径 | 文件 | 「列×文件」对 |
|---|---|---|
| 默认（改动前） | 21 | **49** |
| 文件级并集（我上轮的建议） | 21 | **47** |

⇒ **只解决 2 对（4%）**。若照上轮建议拍板，会为 4% 付出一次决策
+ 一次对已发布数字的改动。**给修法建议之前，先把修法实现出来量一下。**

### ③ 真正的原因是**关系宇宙**

`v1TableRe` / `v1AliasRe` **只认 4 张 v1 裸表**；
`request_logs_with_current_month`（710 视图）不在名单里
⇒ 入口过滤就不通过 ⇒ `admin/logs.go` 的主查询**一个字面量都产不出来**。
别名并集无从谈起——它没走到归因那一步。

### ④ 权威数字是 §9.172 早就数过的，不是我数出来的

```
inventory=106 | v1底表读方=62 (仅v1=52 兼读=10) | 仅视图读方=44 | 未归类=0
SQL 字面量：v1=105  视图=118  合计=223
```

⚠ 我上一轮说「没人看见视图」**不准确**——§9.172 地面真值扫描器从 D20-b 起
就刻意区分基表/视图，注释里甚至写明「`\b` 之后把 `request_logs_with_current_month`
排除在本族之外——那正是 §9.167 别名 bug 藏身之处」。
**准确表述：两个扫描器、两个宇宙，§9.162 的暴露报告从未采纳后者。**

⇒ 后果：报告的 `clean (70 files)` 桶里，「查过了没问题」与「**从来没被看过**」
长得一模一样。探针在全仓 2,273 个生产文件上量到 **63** 个后者
（读方清单内是 §9.172 的 **44** 个；分母不同，都记）。
⚠ 其中含**停写分类表已判为退化**的文件：`admin/usage_enhanced.go`、
`admin/compression_sessions.go`、`admin/logs_summary.go`、
`domains/attachments/handler.go`、`admin/model_status.go`、
`domains/sessionsummary/system_prompt_prefix.go`。
⇒ **两套机制互相矛盾，而更可靠的是逐点评估那套**（要求 Evidence 逐字 + 真库测量）。

### ⑤ 修法已被变异证实

把探针的裸表匹配改成**也认视图** ⇒ 盲区 **63 → 0**。
⇒ 正确修法是**扩关系宇宙**（接入 §9.172 的 9 个关系名：5 底表 + 4 视图链，
从同一份 SSOT 取，避免第三份名单），别名并集只是附带小修。

### ⑥ 本轮我自己的错（三条）

1. **上轮修法建议是错的**，且我写建议时手里**已有**能推翻它的证据形态却没量。
2. **又一次先动手后 grep**（自建 `v1BaseTableRe`，撞上
   `request_logs_reader_population_test.go` 的同名常量）——**本轮第三次**。
3. 「量具与被检验对象必须同源」我记成了无条件规则。**前提是两者都对**；
   要量的正是「它哪里错了」时，量具必须**独立抄写**，否则断言恒真。

### ⑦ 待拍板

**D29-a（已重写）**：扩关系宇宙 = 接入 §9.172 的 9 个关系名；
建议**修**；验收用 §9.195 的盲区探针（应降到 0）+ 三道门 + 静默档计数门，
新旧数字并列写进审计。**改共享分析仍属属主决定，本轮未改。**
**D29-b**（已回答）/ **D29-c**（5 条登记不需人工复核，但新增读方须销账）。
沿用未决：**D28-a / D28-b / D28-c / D27-a / D27-b / D27-c / D26-a / D26-b /
D25-a / D25-b / D25-c / D24-d-3 / D24 / D24-c / D19-a-3-1 / D19-a-3-2 /
D23-c-1 / D23-c-3 / D21-a / D21-b / D19-b / D20-a / D20-c**。

### ⑧ 下一轮提示词

1. `git fetch && git rev-parse origin/main`（当前 `d852f5292`）；
   `git worktree add --detach /tmp/<新> origin/main`。
2. 跑两个探针记基线：
   - `TestProbeRetirementExposureBlindSpotFiles`（当前 **63**）
   - `TestProbeRetirementExposureAttributionGap`（当前 **47** 对）
3. 若决定做 D29-a：
   - 关系名**从 §9.172 的 SSOT 取**，不要新写第三份名单；
   - 改完在**同一个 commit** 跑：盲区探针（应 0）+ 归因探针（应降）
     + `TestRequestLogsRetirementExposure` / `…BreakersRegistryIsConsistent` /
     `…RepointVerdict` / `TestAuditDocSilentClaimMatchesRegistry`，
     **新旧数字并列**写进审计。
4. 回归基线：`admin` 带真库 FAIL = 3，**只看差集**。
5. **D28 拍板前不要动写侧。**

---

## §70.50 找到并**修掉**本机库异常根因：columnar + 未命名子查询（D30）

### ① 目标

§9.184–§9.186 证明本机 `llm_gateway` 的 `request_logs_bodies` 一进「子查询内
`UNION ALL`」就炸，并据此把 D25-a 定为**重建该库**。本轮问一个更前置的问题：
**重建是修复，还是掩盖？**

### ② 逐项排除（全部实测）

行数（干净库灌到 **2,501,007** 行仍正常）· 数据内容 · 并行度 ·
统计信息（`ANALYZE` 后**仍失败**）· `attcompression`（两库完全相同）· DDL/分区树/约束/索引。

⚠ `attcompression` 我一度当成根因（父表 `'l'`、分区 `''` 的「混合状态」），
**被探针库的同形对照当场否掉**——它一模一样却完全正常。
**「像根因」和「是根因」之间隔着一次对照。**

### ③ 根因：访问方法

| 库 | bodies 的 RANGE 分区 | hot |
|---|---|---|
| `llm_gateway`（失败） | **columnar** | heap |
| 探针库（正常） | heap | heap | heap |

探针库是 `pg_dump --schema-only` 建的，**不还原 Citus `columnar` 转换**
⇒ §9.186「同 DDL 换库就好」同时换掉了访问方法。来源是**生产迁移 `765_bodies_columnar_storage`**。

### ④ 因果确证

干净库上把**一个 0 行的分区** `SET ACCESS METHOD columnar` ⇒ **同一句报错**。
之后已复原探针库（4 分区全 heap、删 250 万合成行、查询恢复正常）。

### ⑤ 修法：去掉子查询包裹（已实施，语义不变）

`v1BodyQuery` 拆成 hot 腿 + 母表腿，顺序执行。
不用「补分区键谓词」那条绕过，因为**它会改分区裁剪**；
逐字保留「hot 优先、母表兜底」。**代价**：1 次往返变最多 2 次（写明，不藏）。

⇒ **`TestExecuteRepair_RealDB_BodiesLeaveNoRowOnEitherSurface` 在真库首次真正执行并
PASS（103s）**，此前必然 Skip。**目标里「确认数据的存储可用」这一条在本机达成。**
⇒ 顺带**加强 D19-a-2 的否证**：`ExecuteRepair` 在本机从来没能成功执行过。

### ⑥ 两个「自己的测试遮住修复」

1. e2e 测试的**前置探针硬编码了旧形状**——修好 loader 却留着它，
   等于让修复被自己的测试遮住。已改为**直接引用 loader 的两个常量**（同源），
   判定口径从「只跑 hot 腿」改成**两条腿都跑**（母表才是 columnar 那一张）。
2. `TestSessionFamilyTwoSurfaceUnionShapeIsExecutable` 的失败文案把处置指向
   「重建」。已改正为写明根因与两条真出路。**门保持红**（表确实还是列存）——
   它提醒的正是「这个库的存储形态与生产不一致」，而重建恰恰会掩盖这一点。

### ⑦ ⚠️ 推翻 D25-a

**重建是掩盖不是修复**：重建得到全 heap 的库，故障消失，
但**下次部署 migration 765 就复发**，且复发前的库已不是生产形态。

### ⑧ 残留自证

跑完 e2e 后 `zz-repair-e2e-%` 残留 2 行，但时间戳 **13:36:36**（§9.186 那轮），
本轮在 **16:57** ⇒ **本轮零残留**。共享库另有活网关进程持续写入，与本测试无关。

### ⑨ 待拍板

**D30-a**（回滚 765 vs 保留列存+修读法；**建议先做 D30-c 普查再选**）/
**D30-b**（**生产是否已受影响需 252 只读确认**，可并入 D24-d-3——最该先确认的一条，
它决定 D19-a-3-1 是否也踩这个坑）/
**D30-c**（全仓普查还有谁在未命名子查询里 UNION ALL 读 bodies，并落成常驻门）。
沿用未决：**D29-a / D29-c / D28-a / D28-b / D28-c / D27-a / D27-b / D27-c /
D26-a / D26-b / D25-b / D25-c / D24-d-3 / D24 / D24-c / D19-a-3-1 / D19-a-3-2 /
D23-c-1 / D23-c-3 / D21-a / D21-b / D19-b / D20-a / D20-c**。
**D25-a 已由 D30 取代。**

### ⑩ 下一轮提示词

1. `git fetch && git rev-parse origin/main`；`git worktree add --detach /tmp/<新> origin/main`。
2. 跑 D30-c 普查：bodies 两表配对 × 未命名子查询形状；
   生产形态（columnar）下测，heap 形态下做**阳性对照**。
3. `go test ./cmd/tools/validate_sessions_v2/ -run TestExecuteRepair_RealDB_BodiesLeaveNoRowOnEitherSurface`
   应 PASS；跑完核 `zz-repair-e2e-%` 残留为 **0**。
4. `TestSessionFamilyTwoSurfaceUnionShapeIsExecutable` **应仍然红**——
   若它变绿，先查 bodies 分区的 `relam`（`columnar` 变 `heap` 了）。
5. 回归基线：`admin` 带真库 FAIL = 4（D21 两条 + 本条 + 既有第三条），**只看差集**。

---

## §70.51 D30-c 普查完成：故障面比 bodies 宽得多，还多出一类

### ⑪ 结论先行

1. **§9.196 的根因成立，但范围被低估了。** 不是 `request_logs_bodies` 一张表有问题——
   库里 **7 个带 `_hot` 孪生的母表**都有列存分区，两腿 `UNION ALL` 形状 **7/7 全挂**；
   堆对照臂 `request_logs` 同形状正常返回 **2,184,300** 行。
2. **触发条件是「列存 + 子查询内的 `UNION ALL`」，不是「子查询不能包列存表」。**
   顶层 `UNION ALL`、CTE、普通/嵌套子查询、子查询内 JOIN/LEFT JOIN、
   子查询内 `UNION`（去重）与 `EXCEPT` **全部实测通过**。失败发生在**计划期**。
3. **新发现第二类独立故障**：视图 targetlist 里的**合成输出列**
   （`SELECT 'hot'::text AS source`）在列存关系上**不可投影**。
   8 视图 147 列里只有 `supplier_errors_unified.source` 一列挂。**潜伏**，现网读法碰不到。
4. **生产代码当前没有踩坑**：89 条提到列存关系的 SQL 字面量里，
   命中危险形状的候选只有 1 条（`bg/supplier_error_stats_aggregator.go:64`），
   **真库裁决：可计划**——它两条腿都带分区键谓词。
   ⚠ **但它只是恰好躲过**：三臂对照证明「只给 hot 腿加谓词」照样炸。
   谁把列存腿那个 `WHERE` 删了，聚合器当场炸。
5. **本轮未改任何产品代码**。改的都是文档 + 一道新门。

### ⑫ 改了哪些文件

- **新增** `admin/columnar_surface_servable_realdb_test.go`（5 个子测试）
  - `TestColumnarUniverseFromCatalog` —— **量具自证**（全集非空 + 必须认得 bodies）。PASS
  - `TestColumnarParentTwoSurfaceSetopShape_RealDB` —— 7 个列存母表跑两腿形状。**FAIL 7/7（真实故障）**
  - `TestDeployedViewOverColumnarIsServable_RealDB` —— 8 视图 147 列逐列 EXPLAIN。**FAIL 1/147（真实故障）**
  - `TestColumnarSetopSubqueryIsTheNarrowTrigger_RealDB` —— 13 个**安全**形状必须通过。PASS
  - `TestProductionGoSQLOverColumnarCandidatesAreVerified_RealDB` —— 静态找候选 + **真库裁决**。PASS
- 文档：审计 `§9.197.1–§9.197.10`；决策表 D30-a 改写 + D30-c 标记完成 + **新增 D30-d**；本节。

⚠ **两道红是故意保留的**。不要用重建库/回滚 765 弄绿（理由同 D25-a）。

### ⑬ 本轮判据自身的四次修正（都是被自证抓到的，不是事后补的）

1. `LIKE '%except%'` 匹配到 plpgsql 的 **`EXCEPTION WHEN`** ⇒ 2 条假命中。改词边界正则。
2. 列存集合只收**分区名**、没收**母表名** ⇒ 报「0 命中」的**假零**。
   与 D29-a「关系宇宙漏掉视图」同类，本轮第二次踩。
3. `oid::regclass::text` 在 search_path 下**不返回 schema 前缀** ⇒ 我的正对照全部落空，
   一个子测试误报、另一个误 Skip。
4. 形状矩阵里 `JOIN ... ON true` = **笛卡尔积**，单条跑 7 分半未完，
   差点被读成「形状很慢」而不是「判据写错了」。

⇒ 这四条印证了既有纪律：**量具自证不是形式**。第 2、3 条如果不自证，
本轮会交出「0 命中」「形状安全」两份**格式正确、全错**的报告。

### ⑭ 待拍板（顺序即优先级）

1. **D30-d**（新）：`supplier_errors_unified.source` 怎么修。改视图定义代价最低。
2. **D30-a**（已改写）：回滚列存转换，还是立「列存腿必须自带分区键谓词」的约定。
   ⚠ 倾向回滚或立约定，因为该约定**静态门判不了**。
3. **D30-b**（升级）：252 只读确认生产是否同形态。范围从「bodies」扩到
   **7 个族 + 视图合成列**。并入 D24-d-3 的同一次申请。
4. 其余：D29-a / D29-c、D28-a/b/c、D27-a/b/c、D26-a/b、D25-b/c、
   D24-d-3 / D24 / D24-c、D19-a-3-1/2、D23-c-1/3、D21-a/b、D19-b、D20-a、D20-c。

### ⑮ 下一轮提示词

1. `git fetch && git rev-parse origin/main`；`git worktree add --detach /tmp/<新> origin/main`。
2. **回归基线（已用同 base 的 before/after 实测，勿凭记忆）**：
   - before（把本轮新门文件移走、在 `e6193ea92` 上跑）= **3**：
     `TestProjectTasksSkipsNullTaskID`、`TestReportRollup_HTTPContract`、
     `TestSessionFamilyTwoSurfaceUnionShapeIsExecutable`（§9.196 起就是红的）。
   - after = **5**，＝上面 3 条 **+ 本轮新增的 2 条**
     （`TestColumnarParentTwoSurfaceSetopShape_RealDB`、
     `TestDeployedViewOverColumnarIsServable_RealDB`）。
   ⇒ **精确新增 2 条，都是本轮故意保留的真故障红。**
   ⚠ 此前交接里写的「基线 3 = ReportRollup / DimensionNames / ProjectTasks」**组成是错的**：
   实测第三条是 `TestSessionFamilyTwoSurfaceUnionShapeIsExecutable`，
   而 `TestDimensionNamesQueriesRunAgainstRealSchema` 本轮两次跑都**没有**失败。
   这类「凭记忆的基线组成」不可信，只能 before/after 实测。
3. `TestProductionGoSQLOverColumnarCandidatesAreVerified_RealDB` 应 PASS，
   候选仍是 1 条（`bg/supplier_error_stats_aggregator.go:64`）且裁决为「可计划」。
   候选数变化**不是**故障，但要在报告里写清变了还是没变。
4. ⚠ **D28/D30 拍板前不要**：改写侧、不要改 migration 765、不要改视图定义。
5. 若要继续 D30-c 的延伸（可选）：把同样的「静态找候选 + 真库裁决」
   套到 `deploy/sql/migrations/*.sql` 的视图/函数定义上——本轮只扫了
   已部署对象与生产 Go，**没扫仓库里的迁移 SQL 文本**。

### ⑯ §70.52 补完 D30-c 的第三面：仓库 `.sql` 文本

§9.197 只扫了已部署对象与生产 Go。仓库里 3,475 个 `.sql` 此前没查过。
本轮查了（关系宇宙**从真库 catalog 推导**，不手写）：

- 提到列存关系的视图/函数定义：**65** 条
- 其中含集合算子：**43** 条
- **其中集合算子在括号内的：0**

⇒ 三面合起来，代码库里**不存在**「列存 + 子查询内 `UNION ALL`」的组合。
这个零非空洞：同语料里扫描器数出了 43 个 setop。

⚠ **这一面又踩了两次同族假零**（详见审计 §9.198.2）：
① 手写族名单把 heap 的 `request_logs` 算进去 ⇒ 36 条**假阳性**；
② 语句抽取器要求含 `WITH` 子句且以 `$tag$;`/EOF 收尾，
而多数视图是裸的 `CREATE VIEW … ;` ⇒ 第一次「0 命中」是**抽取器坏了**。
两次都是阳性对照 / 真库对照臂抓到的。

⇒ 累计四次假零/假阳，全是「先造量具再让结论跑在量具上」的不同变体。

### ⑰ §70.53 D29-a 已获授权并完成（§9.199）

**做了什么**：`v1TableRe` / `v1AliasRe` 从「4 张裸表写死」改为从 **§9.172 的同一份 SSOT**
推导（5 底表 + 4 视图链 = 9 个关系名）。**没有第三份名单。**

**盲区 63 → 0**，并新增门 `TestEveryV1ReaderIsAnalyzedByExtractor` 守住它
（探针挡不住：它永不判红，回归的表现是 clean 桶悄悄变大）。

**新旧数字并列**（详见审计 §9.199.2）：
clean **70 → 42** / breaks-possibly **1 → 9** / undercounts-possibly **4 → 24** /
判定分布 empty **5 → 6**、value-divergent **26 → 27** / 契约列合计 **540 → 568**。
`TestReaderPopulationGroundTruth` **一行未动**——它用自己那对正则统计两族，
测的不是同一件事，一个动一个不动是正确结果。

### ⑱ 改完之后立刻暴露的真问题：两个总体被混成了一个

关系宇宙一放宽，breaker 门就报「8 个文件不在册」。逐个查证后发现
**这 8 个全部是经 710 视图读 v1 的、已经 repoint 完的读方**。
它们依赖的是**视图的 v1 臂**（DROP 时要拆的东西），属于**切换时的迁移问题**，
不是「这个文件必须在 DROP 之前改掉」。
⇒ 塞进 `retirementBreakers` 会写出 `reads request_logs.work_type directly`
这种**不属实**的条目，比缺一条登记更糟。

已加 `viaBaseTable` / `viaCanonicalView` 两个标记，门分成两个总体：
- `measured`（直读底表）= 恰好原来那 5 个已登记 breaker，**无新增、无 stale**；
- `viewArm`（视图臂）= **10 个**，门内显式日志，**不进登记表**。处置 = 决策表 **D29-d**。

顺带修掉 `sessionFamilyRe` 认不出包装视图的缺陷
（`admin/auto_route.go` 曾因此被误判成 definite「只可能来自 v1」，
而它读的是 `..._without_customer_id` 视图，是**已 repoint** 的读方）。

### ⑲ 本次改动自身的四次错（都是门自己抓到的）

1. **两总体写成二选一** ⇒ 混合读方从视图桶消失、视图侧暴露被丢。
   抓到它的现象：`domains/sessionforensics/export.go` 在报告里 `breaks-possibly`，
   两个桶里都找不到——因为它 `:416 FROM request_logs` **又**读视图。
2. **`measured` 分支多加了 `inView` 条件** ⇒ 只读底表的字面量被整段跳过
   ⇒ 5 个已登记 breaker 变成 stale。方向是**少报**，
   而少报在这个门上表现为「有人修好了、该销账了」——看起来无害、实则错误的红。
3. `t.Fatalf` 里 `0%` 未转义，vet 失败。
4. 以为「8 个都要登记」，核到第 7 个才发现是视图读方
   ——**差点写下一批不属实的登记**。

⇒ 1、2 的共同点：**分总体时把「并集」写成「二选一」或叠加多余条件**。
都不编译失败、不让测试变绿，只是**安静地少报**。

### ⑳ §70.54 D30-d 授权修法实测**无效** + 一个更基础的发现

**四种变体，真库事务内实测（ROLLBACK，不留痕）**：

| 变体 | 结果 |
|---|---|
| `CASE WHEN <rel>.id IS NOT NULL THEN 'hot' … END::text AS source` | ❌ 仍 `cache lookup failed for attribute source` |
| `tenant_id AS source`（纯改名，源列真实存在） | ❌ 同样报错 |
| 两条腿各包一层 CTE | ❌ 换成 `invalid perminfoindex`（集合算子+列存） |
| **删掉 `source` 列** | ✅ 正常返回行 |
| `SELECT * FROM supplier_errors_unified` | ✅（planner 裁掉了用不到的视图列） |
| `SELECT id, source` / `SELECT source, id` | ❌ 两种顺序都失败 |

⇒ **触发条件不是「合成常量」，而是「视图输出列不是基表列的直接 Var」。**
⇒ 唯一可行的修法是**删列**，属契约变更，**超出原授权，本轮未执行任何 DDL**。

**🆕 更基础的发现**：这个视图**在仓库里根本不存在**。
三份 schema 快照 + startup 链 + embeddata 链全部 0 处；
唯一定义处 `deploy/sql/migrations/V371`，而 `schema_migrations` 里
**V371 未被记录**（最高 V359）。视图却真实存在于本机库，且**无依赖视图**。
⇒ **全新安装/重建的库不会有它**，而 admin 三个读端都查它。
⇒ 即便修好 `source`，视图本身仍不可复现。**这是两件独立的事。**
⇒ 也解释了 §9.198 的仓库 SQL 普查为什么一条都没命中它。

### ㉑ §70.55 D30-a「回滚」的真实范围（本地实测）

1. **回滚 765 不足以止血**——765 的 A 段让 `ensure_request_logs_bodies_partition`
   在有 `citus_columnar` 时继续**新建列存分区**。
2. **现有 `.down.sql` 根本不转分区**，它自己写着：
   「columnar 分区一旦承接数据即**不可无损回转**（需重写全表，
   且 columnar 无 UPDATE/DELETE 路径）……**不要在生产执行**」。
3. **代价**：7 族列存分区合计 **3,359 MB**，`request_logs_bodies_2026_09`
   单个 **3,026 MB**。转回 heap = 全表重写、需维护窗口。
4. **另外 6 个族不由 765 管**（`routing_decision_log` 268 MB、
   `credential_model_index` 30 MB 等各有各的迁移历史）。

⇒ 「回滚」若按字面执行，范围远大于 765。**决策表 D30-a 需要把范围写清楚。**

### ㉒ 下一轮提示词

1. `git fetch && git rev-parse origin/main`；`git worktree add --detach /tmp/<新> origin/main`。
2. 回归基线：带真库 `admin` FAIL = **4**（`TestProjectTasksSkipsNullTaskID` /
   `TestReportRollup_HTTPContract` / `TestSessionFamilyTwoSurfaceUnionShapeIsExecutable`
   / `TestColumnarParentTwoSurfaceSetopShape_RealDB`），**只看差集**。
   ⚠ 曾是 5：第 5 条 `TestDeployedViewOverColumnarIsServable_RealDB` 已于 828 **真绿**
   （视图 21→20 列，真库逐列验证 0/146），不是被跳过、也不是被放宽。
   ⚠ **829 若在生产跑了，这两条会一起转绿**——那是生产形态真的变了，
   不是本机形态漂移。届时必须重跑 D30-b 的只读清单确认生产已不是列存。
3. ⚠ **本机 Go 构建缓存近期被并发会话搞坏过**（`cannot open file .../go-build/...`）。
   建议 `export GOCACHE=/tmp/gocache-<worktree名>` 隔离，否则会误判成代码坏了。
4. D29-a 已完成，**不要**再改 `v1TableRe` 的族名来源——
   它现在从 `v1BaseTableNames` + `viewChainNames(t)` 推导，是唯一真相源。

### ㉓ §70.56 D30-d 已落地（828）+ D30-a 正确形态（829，本机不应用）

**828**（新增 startup 迁移 + down + embeddata 镜像）：
视图 21 → **20** 列、`security_invoker` 保留、行数 **2031 前后不变**。
`TestDeployedViewOverColumnarIsServable_RealDB` **FAIL 1/147 → PASS 0/146**（真绿）。
`schema_migrations` 已登记 828。

**829**（新增 startup 迁移 + down + embeddata 镜像）：**3 GB 重写不需要做。**
- `drop_old_request_logs_bodies_partitions()` 对列存分区是**裸 `DROP TABLE`**
  （实测函数体，无数据搬迁）；
- TTL 默认 7 天、`bg/partition_manager.go:1232` 周期调用；
- `2026_09` 月末 2026-10-01 ⇒ **自 2026-10-08 起自动 DROP**；
- 765 作者当初的「仅转空分区（数据安全阀）……非空分区按 TTL 退役」姿态被沿用。

三段：① 重定义 ensure 函数为**恒 heap**（止血）；② **只转空分区**；
③ NOTICE 列出仍列存且有数据的分区与总 MB。实测 NOTICE：
`2026_09/2026_10 has rows, keep as-is` + `converted empty 2026_11` + `3048 MB ... left`。

⚠ **829 本机故意不应用**：应用后本机不再是生产形态，
两道红门会**假绿**——它们红的意义正是「这个库的形态与生产不一致」（D25-a 同款）。

### ㉔ ⚠ 我在验证 829 时真的把它应用了，已完整回退

为「验证但不改状态」，我外层包了 `BEGIN; … ROLLBACK;`。
**错在迁移文件自带 `COMMIT;`**——文件里的 COMMIT 真提交了，外层 ROLLBACK 无对象可回
（psql 打出两行 WARNING，是证据）。

回退时又踩两个自己的坑：
1. `docker exec -i psql -c <<EOF` ⇒ **静默无效果**
   （`psql -c` 不读 stdin，缺 `-f -`）——「没报错」= 「没执行」；
2. 改用 `-f -` ⇒ `ERROR: LOCK TABLE can only be used in transaction blocks` ⇒ 补 `BEGIN/COMMIT` 成功。

回退后逐项核对与实验前一致：3 个 bodies 分区**全 columnar**、`bodies_total` = **2,244,772**、
ensure 函数含 `USING columnar`、`schema_migrations` **只有 828 没有 829**。

**教训**：「想验证但不想改状态」**不能靠外层包事务**——
被验证对象自带 COMMIT 时外层事务就是摆设。要么用**不含 COMMIT 的副本**，要么在一次性库上跑。

⇒ 带真库 `admin` FAIL 集合 **5 → 4**，减少的那条是**真绿**。

### ㉕ §70.57「V371 未被记录」是个例：全库可复现性普查

实测口径：可复现来源 = startup 链 + 旧扁平链 + domain 链 + `sql/schema/*.sql`（3 个文件全部）
+ deploy baseline + deploy migrations + embeddata 快照 + **全部生产 `.go`**（本项目有 Go 侧 schema 自举）。

| 口径 | 分母 | 查无此名 |
|---|---|---|
| 视图（排除 `citus_*`/`pg_stat*`） | 76 | **0**（828 之后干净） |
| 应用自有函数（排除扩展成员） | 187 | **5** |
| 应用自有基表 | 420 | **15** |

15 张表里 11 张属于**别的服务**（agent/mcp/task_assigner/orchestration/kxmemory/memora，
本库多项目共用，`kxmemory_migration_ownership` 21 行是它们的台账），
2 张是已 detach 的旧分区，1 张 `pg_test_t` 是测试残留，1 张 legacy。
⇒ **不是系统性缺陷**，`supplier_errors_unified` 是唯一一个真缺口，已由 828 补掉。

**真正值得跟进的是 5 个函数**（→ 决策表 **D31-a**）：
4 个 `updated_at`/normalize trigger + `ensure_handoff_logs_partitions`，
全仓 `.sql` 与生产 `.go` 都搜不到。⚠ **后果静默**：全新安装会有那些表，
但**没有** trigger ⇒ `updated_at` 停止维护，而**没有任何门会报**。
其中 3 张挂着的表当前是空表，暂无实际损失。

### ㉖ ⚠ 这一节我把量具写坏了**四次**（可能比结论更值得记）

| # | 错法 | 假结论 | 怎么暴露 |
|---|---|---|---|
| 1 | 只并 `sql/migrations/startup/*.sql`，漏旧扁平链 `sql/migrations/*.sql`（22 个文件） | 「1 个视图查无此名」 | 手工查 `v_free_resource_summary` → 在 `sql/migrations/075-omnifree-schema.sql` |
| 2 | 只并 `sql/schema/01-schema.sql` **一个**文件，漏同目录另 2 个 | 「5 个函数查无此名」 | 逐个定位定义处 |
| 3 | 函数表**未排除扩展成员** | 「383 个函数不可复现」 | 那批是 `gbt_*`/`vector_*`/`pgp_*`/`pgstat*` |
| 4 | `AND c.relispartition IS NOT TRUE` —— **留的是分区、丢的是基表**，口径反了 | 分子分母全错 | 分母 420 与「基表」标签对不上（库里有 988 个分区） |

⇒ 本次任务里**第 6 次**「量具读错了对象」。
前 5 次：`EXCEPTION` 误配 `EXCEPT`、只收分区名致假零、`WITH` 子句要求致假零、
仓库 SQL 普查手写族名单、「验证不改状态」靠外层包事务。
⇒ 共同形状永远是同一句：**量具能跑完、能出数、看不出异常，而它量的不是那件事。**

⇒ 正确口径已写进审计 §9.202.3，下一轮**照抄，别重写**。

---

### ㉗ `session_turns.is_final_success` **从来没有被任何代码写过**（§9.203）

**这是本轮找到的最严重的一个，性质是「API 改对了，数据没跟上」。**

真库实测（2026-10-05）：

| 面 | 口径 | 结果 |
|---|---|---|
| v1 `request_logs_hot` | 近 7 天 `is_final_success = TRUE` | **463** / 4,532 行 |
| v2 `session_turns` | 全表 `is_final_success IS NOT NULL` | **0** / **1,689,308** 行 |

⚠ 是 **NOT NULL** 为 0，不是 TRUE 为 0 ⇒ **这一列从未被写入过**。

而 schema 侧**全部齐备**：列存在、`turn_writer.go:396` 的 INSERT 列清单**已经包含**它、
`uq_session_turns_hot_final_success` / `uq_session_turns_final_success` / 各月分区
各一张 `(tenant_id, session_id, partition_date) WHERE is_final_success` **全部存在**。
⇒ **唯一性约束一直在对一个永远为空的集合生效。**

**用户可见后果**：`admin/session_online.go` 的会话时间线 2026-09-30 迁到 session 族
原生源后，`deriveTurnOutcome` 的前两个分支（`final_success` / `superseded_success`）
**双双不可达** ⇒ 每一个成功轮次都被标成普通 `success`。
**独立旁证**：`db/session_family_column_availability_test.go` 早就报
`GO EMPTY ON THE SESSION SIDE (2): client_protocol, is_final_success`，一直亮着没人去读它指向哪。

**已修（写侧）**：新增 `internal/sessionv2mirror/final_success_turn.go`，
落点与 `is_abandoned`（migration 821）**完全同款**——因为机械原因一样：
v1 认领在 telemetry 事务里，session turn 在镜像 hook 之后**异步**写，
从 telemetry 侧发标记必然与插入竞态。
- telemetry：认领成功才置 `entry.FinalSuccessClaimed`；
  `updateRequestLog` 的 `fallback := *entry` 分支**把标志回拷**（不拷就丢，T0Missing 踩过）。
- 落点：自带事务 + `setBypassGUCs` + tenant GUC + **两张脸** + 幂等 + 指标。
- live hook 与 outbox 回放**两条路径**都在 `w.Write` 之后打标。
- ⚠ 标志**必须序列化**（`json:"final_success_claimed,omitempty"`），与 `T0Missing` 的 `json:"-"` 不同：
  outbox 是在认领成功的**同一事务**里登记的，正是「认领了但镜像行没落地」那个洞的修补路径。
- **session 侧不自己认领，只镜像 v1 的裁决**：v1 已 race-proof，每会话至多一个授予 ⇒
  不可能违反 session 侧唯一索引。万一真有两个，`mark_superseded` 单独记（说明两族对「谁赢了」有分歧）。

**配 4 个门全绿**（`internal/sessionv2mirror/`）：3 个静态接线 +
1 个真库（种行 → 标记 → 回读 + 幂等 + 2 条阴性对照 + 残留复核）。

**⚠ 遗留**：**历史数据未回填**（→ 决策表 **D32**）。可回填量已实测：
`request_logs_2026_09` 的 107,794 个 v1 winner 中 **107,756** 能命中（99.96%），
`2026_10` 另有 2,425 个；`matched == distinct` ⇒ 无重复行，唯一索引可满足。
连接耗时 5.1s。**UPDATE 耗时未实测**，所以 D32 的选项 1/2/3 还没定。
⚠ 不回填的后果：写侧修复只对**新请求**生效；已存在的会话**永远**显示不出最终成功轮，
而 v1 退役后这份信息**再也无法恢复**。

### ㉘ ⚠ 这一节我把量具写坏了**三次**，外加揪出一个**生产代码**的假警报

| # | 错法 | 报出来的现象 | 真因 |
|---|---|---|---|
| 1 | 回读只查 `session_turns` 母表 | hot 行明明标上了却报「仍非 TRUE」 | **`session_turns_hot` 不是 `session_turns` 的分区**（hot 是独立表，月度分区才是母表子表） |
| 2 | 给两张脸种**不同 request_id** 却只调一次标记，然后要求两次回读都真 | 父表腿报「没生效」 | 标记按 request_id 定位；一张行要么在 hot 要么已 promote，**不会同时在两处**。量具建模错了 |
| 3 | 阴性对照 B：wrong-tenant 行种在 hot，回读只查母表 | 「通过」 | **这条对照是恒真的**——母表里永远查不到那行 |
| 4 | 清理只删母表 | 门自己的残留复核抓到 1 行 | 同 #1，两张脸要一起删 |
| 5 | **阴性对照 B 复用了阳性行的 `session_id`** | 变异把 tenant 谓词换成恒真表达式，**门仍然 PASS** | 标记 wrong-tenant 行时撞上 `uq_session_turns_hot_final_success`（阳性行已持标记）⇒ 23505 ⇒ 走 `mark_superseded` **提前返回，根本没执行到 tenant 谓词** |

⇒ 本次任务里**第 7 次**「量具读错了对象」。
⇒ **#3 与 #5 是新变种：对照本身恒真。** 前 6 次是量具读错对象，这两次是
量具/对照**根本没在被检验的对象上**却仍然报绿。恒真的判据不会变红，
所以它连「判据红了先怀疑判据」这条自救路径都用不上——
**这比读错对象更危险，因为它连报警的机会都不给。**

★ **#5 是本轮最实用的一条方法论，也是唯一一条靠变异测试才发现的。**
前三项在写门时被自己的断言抓住，#4 被门自带的残留复核抓住；
#5 是在「验证这道门到底有没有牙」时才暴露的——
**把 tenant 谓词换成恒真表达式，门照样绿。**
⇒ **阴性对照必须与阳性行解耦到不可能互相短路。**
凡是要靠「唯一索引 / 唯一键 / 排斥约束」判定成败的对照，
它的种子行必须避开阳性行触发该约束所需的**同一组键**；
否则对照测的是「约束有没有挡住」，不是「谓词有没有生效」。
⇒ 另一条同源教训：**变异没生效时，「测试通过」什么都不能证明。**
本轮第一次跑变异 2 时 `perl` 正则没匹配上、文件其实没变，
而门照样「通过」——**先验证变异落地（diff 备份），再读测试结果。**

★ **#4 还牵出一个生产代码的真缺陷**（不是门的问题）：第一版把「0 行」
一律记成 `mark_no_row` + WARN，于是**幂等重跑会打出一条
「v1 认领了但 turn 不在两张脸上」的假警**——而那正是这条告警要抓的真故障。
**告警一旦会说假话就等于没有告警。**
已改为：0 行时先探测行是否存在，行在且已标记 ⇒ `mark_noop`（Debug 不告警），
行不在 ⇒ `mark_no_row`（Warn）。

### ㉙ 回归基线（同 base 实测，不凭记忆）

| 包 | 本轮 | origin/main 同 base | 判定 |
|---|---|---|---|
| `internal/sessionv2mirror` | ok | — | 新增 4 门全绿 |
| `domains/hooks/observability/telemetry` | ok | — | 无回归 |
| `domains/session/...` | ok | — | 无回归 |
| `admin` | **FAIL 4** | 见下 | **与基线同 4 条** |
| `db` | FAIL | **FAIL（逐位相同）** | 既有失败，非本轮引入 |
| `installer`（独立 module） | ok 全绿 | — | 无回归 |
| `internal/sqlguard` | ok | — | 无回归 |

⚠ **`db` 的 FAIL 是既有的**：`work_type` unservable + 7 个 `RetirementColumnFill` 漂移
+ 5 个 structural gaps。在 **origin_main（`909b4b047`，无我任何改动）** 上以
**完全相同**的断言 FAIL，7 个漂移数值**逐位相同**
（12.28/18.80、90.12/100.00、46.91/64.80、54.73/100.00、53.35/100.00、39.78/100.00）
⇒ **本轮零新增 FAIL**。
★ 顺带：这一轮**先怀疑了判据**——我的真库门要往正在被别的真库门测量的库写探针行，
所以我怀疑是污染，跑去 origin/main 单跑了一遍基线才发现数字完全一致。判据没红，疑对了方向。

⚠ **`go test ./installer/...` 会报 `[setup failed]`**，那**不是回归**：
`installer` 是独立 Go module，根模块的 `./...` 模式覆盖不到它。
要在 `installer/` 目录里跑 `go test ./...`。

---

### ㉚ D32 实测完成：89s，形态定为 operator 脚本，工具已就绪（§9.204）

上一轮我挂着「先实测 UPDATE 耗时」。**测完了，数字推翻了原建议。**

| 面 | 行数 | 耗时 | 单行 |
|---|---|---|---|
| `session_turns_2026_09` | 107,756 | **76.4s** | 0.71 ms |
| `session_turns_2026_10` | 2,328 | 0.50s | 0.21 ms |
| `session_turns_hot` | 395 | 0.08s | 0.20 ms |
| **脚本整体** | **110,479** | **89.0s** | — |

★ **上一轮记的 5.1s 是 SELECT 的耗时，与 UPDATE 差 17 倍。**
那是「同一个操作的两个阶段」被当成了同一件事 ——
量具读错对象的又一种形态（错的是阶段，不是对象）。

★ 成本集中在索引维护：`is_final_success` 在部分唯一索引的**谓词**里
⇒ 每行非 HOT 更新，要重写该表所有索引。大分区 0.71 ms/行 vs 小面 0.20 ms/行
的 3.5 倍差，不是「大表更慢」，是「大表索引更多、要重写的页更多」。
★ 成本**随 v1 winner 数增长，不随 `session_turns` 体量增长** ⇒ 全新安装零工作量。

**形态从 startup 迁移改成 operator 脚本**，理由：
启动迁移链是 installer 逐文件串行同步 + `--single-transaction` 全量原子 + **无超时**
（`installer/internal/dbinit/runner.go:883-890`）。
把 89s、且**生产体量我从未测过**的数据 UPDATE 放进升级路径 = 拿没量过的环境赌启动预算。
而这件事**必须发生**（v1 退役后永久不可恢复）⇒ **换载体，不是换时机**。
形态与仓库既有 `sql/scripts/backfill_*.sql` 一致，不是我发明的。

### ㉛ 两个实测陷阱（不查 catalog 看不见）

1. **本库有第二个 schema**：`gateway.session_turns_2026_07/08/09` 是**空的**同名克隆。
   `search_path = public, llm_gateway` ⇒ 未限定名恰好踩不到，
   但上生产的脚本**不能依赖 search_path**。脚本每个标识符都显式限定。
   ⚠ 只查 `relname` 会看到「同名两份」而不知道它们在不同 schema ——
   必须 `pg_class` + `pg_namespace` 联查。
2. **`request_logs_hot` / `session_turns_hot` 不是母表的分区**，是独立表。
   忘了单独处理 ⇒ **静默漏掉最后 8 小时**，且不报错。

### ㉜ 交付物

- `sql/scripts/backfill_final_success_marks.sql` — **已端到端实跑验证**
  （同一份内容外包 `BEGIN`/`ROLLBACK` 喂 `psql -f`，跑完逐面核对回到 0）。
  自带 3 条核对，实测：逐会话唯一性 **0** ✅ / 残余缺口 **221**（镜像按设计排除，
  **故意不计入判据**，否则造一个永远红的门）/ 回滚后全 **0** ✅。
- `admin/session_final_success_backlog_realdb_test.go` — **把欠账变成会红的线**。
  ⚠ **它现在就是红的，这是设计如此**（与 D30-a 故意红的门同款）；
  回填执行后转绿，**那是预期，不是「被改绿」**。
  三种零样本指名 Skip：未设 DSN / **v1 已退役**（窗口关闭，恰恰最该报警）/ v1 无 winner。

★ 这道门补的是本审计反复遇到的那类洞：
**「事实存在但没人消费，就不是守卫」**。
`session_family_column_availability_test.go` 早就在**信息行**里报了
`is_final_success` 是空的，但那是 `t.Log` 不是断言 ⇒ 没人读它指向哪里，
这一空就是 **6 天**。

### ⚠ 因此 admin 的 FAIL 由 4 条变 5 条

第 5 条是 `TestSessionFinalSuccessBacklogIsClosed`，**故意红**（欠账未清）。
判别方法：它形如「v1 有 110,084 个 winner…v2 只有 0 行带标记」并给出修复命令，
与 `TestColumnarParentTwoSurfaceSetopShape_RealDB` /
`TestSessionFamilyTwoSurfaceUnionShapeIsExecutable`（等 829 上生产才转绿）同类。

---

### ㉞ D33 关闭：一条**从来没跑通过**的测试，里面藏着**两层**缺陷（§9.205）

上轮记下它恒红、被「Skip 即绿」掩盖，判为「按 bodies 拆分前的 schema 断言」。
修掉之后的结论比那一句严重：**它从来没有跑到过终点。**

**第一层**：验证段对主表 `SELECT request_body, response_body`，
而拆分后这两列**已不在主表**（实测主表与 710 视图都只剩 preview）⇒ 42703。
⚠ 这条测试是**部分迁移**的：第 3–5 段**已经**改用 `request_logs_bodies_hot`，
只有**第一段**留在旧 schema。**「改了一半」最难发现**——读代码时每段单独看都合理。

★ **关键判断**：不能只是把 SELECT 改掉。
原断言 `if gotRequestBody != nil { fail }` 在拆分后是**结构保证**（列没了）；
只改 SELECT 会让它变绿，但那条不变式从此**只是碰巧成立** ——
哪天有人把列加回来就静默失效。
⇒ 改成**显式断言两列不存在于主表**，并断言 preview 哨兵值真落库
（`RequestPreview="hello"` 与完整 body 不同，能分辨「写进去的是 preview」）。

**第二层**（被第一层挡住的）：修好后立刻 panic 在 `require.JSONEq(t, *entry.RequestBody, ...)`
—— `persistRequestLog` 成功后调 `releaseBodies()` 把 entry 上正文**置 nil**（`client.go:1221`）。
⇒ **这条测试从来没执行到过这一行。** 上层先炸，它就没机会。
★ 这是「修好一层、下一层缺陷才现形」的教科书案例，
也说明**「测试存在」与「测试跑通过终点」是两件事**。

★ 顺带：失败信息里 `%v` 一个 `*string` 印的是**指针地址**
（`request_preview = 0x62fddf94b980`）。**信息量为零的失败信息 = 让人从头再查一遍。**

**同类清扫**：全仓 `*_test.go` 扫 `request_body`/`response_body`，
其余引用**全部**已走 `request_logs_bodies_*` 或自建隔离 schema
⇒ `TestRequestLogInsertParamCount` 是**最后一个**还在主表脸上选 body 列的测试。

**门状态**：`telemetry` 包 **FAIL 1 → ok**；变异验证两条新断言都有牙
（结构断言改指向真实列 ⇒ 红；preview 期望改错 ⇒ 红）；
测试自清理经核对（三张表 0 残留）；无 DSN 时正常 Skip 而非红。

---

### ㉟ D29-d 关闭：10 个读方从「一句日志」变成登记表 + 双向门（§9.206）

**之前只有一句 `t.Logf` —— 那不是守卫。** 新增一个经视图读 v1 臂的读方
不会让任何东西变红。与 §9.204 修掉的 `is_final_success` 空列是**同一族洞**。

**语义差别决定了措辞**：直读底表 = `DROP` **之前**必须改、失败是**报错**；
经视图读 = `DROP` **那一刻**才出问题、失败是**静默返回空结果**（接口 200、日志无痕）。
⇒ 登记条目**不能写 `reads request_logs.X directly`** —— **不实陈述比缺一条登记更糟**。

★ **实测：10 个依赖里 7 个点名 `work_type`，而它在 session 臂上是 0.00%**（2% 采样）
⇒ 切换后按它过滤的页面直接空集。后果最隐蔽的是 `summarizer.go`：
摘要输入变空**不失败**，会生成「看起来正常」的错摘要。

★⚠ **我第一版写了一条假不变式**（「两表必须不相交」），被真数据当场否掉：
`db/db.go`（投影定义者）与 `telemetry/client.go`（v1 writer）本来就该留在
`retirementBreakers`，它们出现在 view-arm 扫描里只因**提到了那些关系名**。
**这次是我错了，不是数据错了** —— 把「角色不同」当成了「登记重复」。
⇒ 换成两条**方向相反**的真不变式：① 两登记表不相交；
② 每个实测依赖必须被其中之一解释，被 breaker 解释的**打出来但不失败**（可见 ≠ 洞）。

★ 顺带把测量抽成 `measureV1ReadingExposure`：**两个门共用一份事实**。
「同一个事实两个来源」迟早不一致，而不一致那天没人知道该信谁。重构行为等价。

★ **stale 分支故意不自动销账**：实测不到可能是「修好了」，也可能是
「依赖被重构成另一种形状、提取器看不见了」——后者意味着这道门正在对真实依赖
**失明**，而「门还绿着」会被读成「没问题」。

**3 个方向变异验证有牙**：删条目 ⇒ 红 / 登记不存在的文件 ⇒ 红 /
breaker 也放进切换清单 ⇒ 红。

---

### ㊱ D31-a 关闭：**结论翻转 —— 5 个函数里一个缺口都没有**（§9.207）

§9.202 报「5 个函数本库存在、全仓搜不到」，建议补进迁移链，理由是「后果静默」。
**逐个查证后那个推断有两处是错的。**

| 对象 | 真实归属 |
|---|---|
| 2 个 `memora_*` updated_at trigger | **另一个服务（memora）的表**，本仓不负责 |
| `update_conversation_updated_at` | 表**与** trigger **都不在链内**、Go 零引用 ⇒ 一致缺席 |
| `llm_hourly_stats_normalize_hour_trigger` | 表在链内，但 **667/668 已在链内解决同一问题** ⇒ 是另一种带外解法 |
| `ensure_handoff_logs_partitions`（复数） | **刻意排除**（契约测试第 88 行原文），零调用方 |

★ **决定性的一查**：那 4 张表在**全部非测试 Go 代码里零引用**
⇒ trigger 缺不缺**没有可观测后果**。
⇒ **不补**。抄进迁移链 = 把**死代码**引进全新安装，而死代码不会被任何门抓到。

★ 顺带查清一件更要紧的：单数版 `ensure_handoff_logs_partition` 在 baseline 里是
`RAISE NOTICE 'noop'` 的**退化体**，活库是真实实现 ——
**但那个真实实现在仓库里**（714 / 534）⇒ 也不是缺口。

⇒ 改为一道**绊线**（`live_only_object_ownership_gate_test.go`）：
钉住「这 4 张表本仓无调用方」这个**前提**；将来有人加调用方，门变红并要求
**同时**补 trigger —— 那时才第一次成为真缺口。
判据要先**剥掉迁移文件名**再判（667/668 的 `go:embed` 与 StartupFiles
实测 6 处会被误判成表访问）。

### ⚠⚠ 又一次「恒真的门」，但形态是**新的**

`stripMigrationFilename` 处理**非文件名**出现时用 `"\x00"` 就地替换，
下次 `Index` 就找不到那个名字，函数返回一个**不再含该名字**的字符串
⇒ 它抹掉的是**全部**出现，不止文件名那一种。
**结果：加一条真实的 `SELECT … FROM conversation_history`，门仍然 PASS。**

⇒ 形态与前几次都不同：不是读错对象，是**判据把自己的信号删了**。
它能跑完、能出数（报「0 个调用方」）、看不出异常 ——
而它量的根本不是那件事。

⇒ 修法：逐 token 扫描，**只**删文件名那一种，裸出现原样保留。
两个方向都经变异验证：真实 SQL 引用 ⇒ 红（精确到 `file:line`）/ 迁移文件名 ⇒ 绿。

★ **方法论补丁（跨项目可复用）**：「应用自有对象」这个判定
**必须交叉核对归属**，不能只看「名字在别的服务清单里没有」。
本次**同一个库、同一份扫描**（§9.202.1）已经列出了 memora 是外部服务，
却没把那 5 个函数与那份清单对照 ——
**两条结论挨在一起却没交叉**，这是比「扫错了对象」更隐蔽的一层。

---

## §70.49 `session_turns.client_protocol` 从未被写过，而 admin 主列表**正在读它**（D28-c 结案）

### ① 本轮做了什么

照 `agent_name` 的样板，把 `ClientProtocol` 接进 v2 写入链四处
（bridge / `ProcessedRequest` / `TurnRecord` / INSERT），
配真库门 `TestTurnWriterWritesClientProtocol_RealDB`，并经 2 方向变异验证有牙。
D28-c 相应改写：从「排期」升级为「已修」。

### ② 实测：与 `is_final_success` 同款，但这次**有人读**

| 面 | 口径 | 结果 |
|---|---|---|
| `session_turns` | 近 30 天 `client_protocol` 非空 | **0** / **1,659,271** |
| `request_logs_hot` | 近 30 天非空 | 1,455 / 4,081 |
| `session_turns_hot` | `agent_name`（同族对照） | 1,455 / 1,473（98.8%） |

区别于 `is_final_success`（列在 INSERT 清单里、只是没人置位）：
`client_protocol` **连管道都没接**。

### ③ 后果是用户可见的，且本机已经命中

`storage.admin_logs_native_turns_read = true`（本机实测）⇒
`logsSourceFromSQL()`（`admin/logs_turns_source.go:62`）直读
`db.SessionFamilyTurnsSourceSQL()`，而 `admin/logs.go:204` 选 `rl.client_protocol`
（投影列 `db/request_logs_view_schema.go:457`）。
⇒ **admin 请求日志列表的「客户端协议」列恒空**；接口 200、页面正常、无错误日志。

⚠ **710 视图该列填充 35,185 / 2,278,971** ⇒ 从视图读是好的、从原生投影读才空。
这正是它此前没被发现的原因：**任何只测视图的核对都看不出问题。**
这条要记住：切臂之后，同一列在两条路径上的质量可以差到「好 vs 恒空」。

### ④ ⚠ D28-c 的前提是错的 —— 「没人读」是量具错了

D28-c 写「`client_protocol` 全仓无人 SELECT 读」，
`admin/request_logs_retirement_column_reader_gate_test.go:25` 记着该断言用**逐行 grep** 做。
但 `admin/logs.go:204` 那行**确实含列名**（`rl.client_protocol,`），
逐行 grep 本该命中 ⇒ **当时的断言口径比「逐行 grep」更窄**
（多半只扫 v1 底表字面量，而该 SQL 来自拼接的 `logsFrom`）。

★ 与 §9.202 / §70.47 同族：**被测对象经过拼接时，静态字面量扫描会漏**。
⇒ 通则：「用某个口径量出 0」不等于「不存在」。先问清那个口径的边界，再问结论。

### ⑤ `$99` 刻意追加在末尾

参数列表是**位置编号**，插在 `agent_name` 旁边会重排其后每一个 `$N`，
漏一个就是 `mismatched param and argument count` ——
正是 `TestRequestLogInsertParamCount` 守着的那类事故。

★ **我第一版真的漏了 SELECT 段的 `$99`**，被本轮临时写的参数计数检查抓到，
**不是任何既有门**。⇒ 于是把「真跑一次」变成常规动作（真库门）。
pgxmock 只要参数个数对就放行，**只有真库会炸**。

### ⑥ 顺带修一句会误导人的过期注释

`TurnRecord.DigestJSON` 原注释写「It is nil for rows that cannot produce a useful digest」——
**不对**：nil ⇒ `''` ⇒ `''::jsonb` ⇒ **22P02**。本轮照这句注释构造零值记录，被打回来。
生产侧不踩（唯一赋值点出错即 return），但注释会**主动**把人送进坑。

### ⑦ 门与变异

| 变异 | 结果 |
|---|---|
| SELECT 段删掉 `$99`（本轮亲手犯的错） | 红：`syntax error at or near "WHERE"` |
| 映射写成常量 `nilIfEmpty("MUTATION-CONSTANT")` | 红（**阴性对照**抓的） |

阴性对照是这道门的意义所在：没有「空值必须落 NULL」这一段，
一个把该列写死的实现也能对有值用例报绿。

⚠ **投影腿刻意不在本门考核**（`db.SessionFamilyTurnsSourceSQL()` 在 db 包，
从 v2 引入会穿过 db→v2 的依赖方向）。这是**已知未覆盖**，不是「已验证没问题」。

### ⑧ 回归与边界

`domains/session/...`、`internal/sessionv2mirror`、`telemetry` 全绿；
`go build ./...` 通过；gofmt 干净；`admin` 全量基线仍为 FAIL 5（同 base 实测）。

### ⑨ 待拍板

**D28-c 已结案**（`client_protocol` 已闭合）。
⚠ 但它与 `work_type` **关闭路径不同**，不要一起「排期」：
前者是纯实现缺口、已修；后者仍卡 **D27-c** 的客户端头驱动，需属主拍板。

沿用未决：**D30-b**（252 只读凭据，唯一硬阻塞）、
D32 + D29-d 的生产切换时点（建议同一次部署窗口，且必须赶在 `request_logs` 被 DROP 之前）、
**D28-a / D28-b / D27-a / D27-b / D27-c / D26-a / D26-b / D25-a / D25-b / D25-c /
D24 系列 / D23 系列 / D21-a / D21-b / D19-b / D20-a / D20-c**。

### ⑩ 下一轮提示词

1. `git fetch && git rev-parse origin/main`；`git worktree add --detach /tmp/<新> origin/main`。
2. 每轮开头 `export GOCACHE=/tmp/gocache-<worktree名>`（本机共享缓存会被并发会话搞坏）。
3. 剩余候选：`client_protocol` 之外，`db/session_family_column_availability_test.go`
   的 `GO EMPTY ON THE SESSION SIDE` 若还有成员，逐个查**是否有活读方**——
   本轮的教训是**「空」不等于「没人用」**。
4. 回归只看差集；`installer` 是独立 module，须进目录跑 `go test ./...`。
5. 文档 U+FFFD 基线：审计 2（历史遗留）、决策表 0、handoff 0，每轮写完必核零新增。

---

## §70.50 退役可行性仪表把三种状态读成同一个「空」；而它长期报红的真因是**判据自己不自洽**（§9.209）

### ① 标题那个问题：GO EMPTY 名单**已穷尽**，零新增成员

`client_protocol`（§9.208 已补写方）与 `is_final_success`（§9.203 已补写方）。
两者仍测得 0%，但原因不同：前者是**修复尚未部署**（近期窗口实测 0.0%），
后者是 **D32 回填尚未执行**。

### ② ⚠ 但这个「答案」是拿读写侧代码得来的，不是拿这道仪表得来的

仪表只有一个窗口：**全生命周期**。它把下面三种情况压成同一个读数，而**只有第一种是缺陷**：

| 状态 | 例子 | 终身 | 近期 2h | 是缺陷吗 |
|---|---|---|---|---|
| A 写方从未存在 | `client_protocol`（修前） | 0.00% | 0.0% | **是** |
| B 写方已修好，历史欠账 | `search_text`（今天 16:00 起） | 0.04% | **100.0%** | 否 |
| B' 同上 | `raw_model_name` | 0.59% | **100.0%** | 否 |
| C 流量构成变了 | `auto_decision` | 44.63% | 5.0% | 否 |

A 与 B 从终身读数上不可区分 —— §9.203 与 §9.208 两次都只能回头读写侧代码才能分辨。
**这就是这道仪表一直在替人说一件它没有能力说的事。**

### ③ 我第一版把 C 类读成了回归，被自己的数据打掉

我把 `auto_decision`(44.7%→1.2%)、`client_request_id`(15.7%→0.7%) 判成「写方疑似刚坏」。
按小时拉序列后**两者在最近 26 个整点都是 0%**，12:31 的 hot/月度分界处**没有阶跃**。
⇒ **两窗口差值这个启发式本身就会制造假阳性**，所以落桶标签写的是
「候选，需交叉核对流量构成」，不是「回归」。与 §9.157「本机有未识别的活跃写方」一致。

### ④ 第二窗口取 2 小时不是调参

**横跨变更点的窗口会把修复稀释成不可见**：24h 窗口下 `search_text` 得 **18.2%**
（半个窗口在 16:00 部署之前），于是落进「平稳」桶 —— **一个主动否认写方刚启动的标签**。
同一列在 2h 窗口读 100%。空窗口 ⇒ **指名 SKIP**，绝不静默算 0%
（0 行窗口会把每列都判成「近期下跌」，正好制造这节要消除的假警报）。
三个桶**必须划分**测得了源的列 —— 这条有牙且不随流量抖动；近期速率**只报告不注册**。

### ⑤ ★★★ 本轮最重要的一条：`db` 包长期报红，根因是**判据自己不自洽**

我前几轮一直把 `db` FAIL 记作「既有」。这次查了为什么：

**红① `registered but not measured: [work_type]` —— 判据错，注册表对。**

同一轮打印 `work_type` 是 session **0.00%** / v1 1.93%，看着就该进 goEmpty。
探针真值：`sessNonNull=19 / 1691251 = sp=0.0011%`。
⇒ 分类判据是 `sp == 0`（**精确浮点零**），报表是 `%.2f%%`（**小于 0.005% 显示 0.00%**）。
**同一行：显示 0.00%、note 为空、分类「没空」—— 自相矛盾。**

注册表三条注释都写「0.00% session」，即注册表语义是**按显示精度算空**，
判据语义是**精确等于零**。两者只在「极小但非零」分歧，而 `work_type` 今天正好落在这里
（写方刚被本会话之外的改动接上，19 行）。

★★ **这条红在教我做错事。** 错误信息原文是「Re-derive the list from this run and
update it deliberately」——**照做会把 `work_type` 从注册表删掉**，等于宣告它的读方已可服务，
而那些读方在 99.999% 的行上仍读到 NULL。**注册表是对的，仪表是错的。**

修法：`effectivelyEmptyPP = 0.005`（= 显示精度半个末位），分类与说明共用；
非零但低于阈值的列**显式写出** `non-zero: 19 row(s) = 0.0011%, below the 0.005% threshold`。
改后 `unservable` 断言转绿（3 = 3）。

**红② `RetirementColumnFill` 漂移 —— 门被行数绊倒，容差两轮都不够。**

容差 0.01 → 上一轮提到 0.05，注释写的正是「报红与任何人的工作无关」。
但它**今天仍红**：10 列漂移 0.06–0.11pp。**任何固定容差都跑不过一个被多进程写入的活库。**
改成 1.0pp，**从要抓的缺陷倒推**：真实缺陷（6 列写成猜的 0）最小错 9.7pp、最大约 100pp；
实测噪声 ≤0.11pp ⇒ 1.0pp 在噪声上方约 9 倍、真实缺陷下方约 10 倍。
⚠ 决定曝光类别的**集合断言完全不受影响**，仍是精确的。

### ⑥ 遗留

- `work_type` 近期约 4.9%、终身 0.0011%：**写方已被接上**，但它留在 unservable 清单里是**对的**
  （历史仍空），只是**清单注释已过期**，需在 D27-c 拍板时一并处理。**我没有改注册表** —— 那是政策决定。
- `RetirementColumnFill` 是**活库快照**。容差放宽能撑一段时间，但迟早还会漂。
  ⚠ 根本问题（提交进仓库的活库快照该不该存在）需属主定，未单方面动。
- 投影腿（`SessionFamilyTurnsSourceSQL`）仍不在本门考核，同 §9.208。

### ⑦ 待拍板

**D30-b（252 只读凭据，唯一硬阻塞）**、D32 + D29-d 生产切换时点、
`work_type` 清单注释随 D27-c 一并更新、`RetirementColumnFill` 是否应继续作为仓库内快照。
沿用未决：D28-a/b、D27-a/b/c、D26-a/b、D25-a/b/c、D24 系列、D23 系列、D21-a/b、D20-a/c、D19-b。

### ⑧ 下一轮提示词

1. `git fetch && git rev-parse origin/main`；`git worktree add --detach /tmp/<新> origin/main`。
2. **所有编辑只在 worktree 里做**；共享主工作区此前处于未解决合并冲突，别碰。
3. 每轮开头 `export GOCACHE=/tmp/gocache-<worktree名>`。
4. ⚠ **本机教训**：`go test ./db/` 这类真库门跑在**共享的、持续被写入的库**上，
   同一门两次跑出的数字可以不同（lifetime 1691231 / 1691238 / 1691245 / 1691251）。
   任何 before/after 比较必须**同 base 同一次运行**内取两个数，不能跨运行比。
5. 回归只看差集；`installer` 是独立 module，须进目录跑 `go test ./...`。

---

## §70.51 `client_model` 值分歧下限已过期 —— 补上判据缺口，但**不**单方面撤销登记（§9.210）

### ① 本轮处理的是 `db` 包剩下的那条红

`TestRepointValueFidelity` 报 `client_model diverges 0/457 = 0.0% (floor 30%)`。

### ② 我一度判错：以为这个测量是恒真的

看到视图定义尾部有 `WHERE NOT EXISTS(session_turns_hot …) AND NOT EXISTS(session_turns …)`，
我以为视图把所有双生行都排除了 ⇒ session 腿计数在结构上恒为 0 ⇒ 一条恒真的测量。

**错了。** 该视图是**三分支 UNION ALL**：① `session_turns_hot` ② `session_turns`
③ `request_logs_hot`（**仅**无双生行的行）。排除条件只作用在第 ③ 个 v1 回退分支。
session 腿比较的确实是 **session 臂 vs v1 臂**。

★ 教训：**看定义只看尾部，会把三分支 UNION 读成单表。**
一个差点被写进结论的「发现」，依据只是片段。

### ③ 0.0% 是真一致，不是「两侧都是 NULL」

`IS DISTINCT FROM` 认为 NULL = NULL，所以 0 分歧也是「两侧全空」的样子。
实测 457 个 session 腿成功行：v1 侧 NULL **0**、视图侧 NULL **0**、
两侧都非 NULL **457/457**、在其上分歧 **0**。

### ④ ★ 门无法区分「已修好」与「测量失明」，我补上了

原错误信息：*either the normalisation was fixed (drop the registration) or it regressed*
——**点了两个相反结论却没给二选一的依据**，而真正的第三种可能
（**比较器失明**）根本没被提及。两处缺口：

1. **正向控制是整窗口算的**，不限腿 ⇒ 一个在 v1 腿活着、session 腿死掉的比较器
   **能通过它**，然后把 session 腿全部分歧报成 0 —— 与「一致」不可区分。
   而整个下限机制就活在这个区分上。
2. **没有 NULL 剖面** ⇒ 0 分歧无法与「两侧全空」区分。

两处都补（分腿控制为 0 直接 `Fatal`；NULL 剖面随行打印），
并把错误信息改成**说清它证明了什么、没证明什么**。

### ⑤ 我**没有**撤销那条登记

实测只证明**本机 24h 窗口**上分歧归零。但那条登记是**关于生产的缺陷声明**
（原注：52.8% 双生行不同，打断 `bg/model_probe.go` 的
`pm.raw_model_name = rl.client_model`）。⚠ **本机窗口无法为生产结论背书**
—— §9.157 的边界，也是 §9.209 刚吃过的亏。我**没有生产只读凭据**（D30-b 阻塞）。

⇒ 不改注册表；让门**继续红**，但红得**可判读**：它现在问的是
「这条声明在它被提出的那个地方还成立吗」，不是「有个数字动了」。

★ **两种撤销方式的差别（变异实测过）**：`MinRate` 改 `0.0` 也能转绿，
但那是**空条件**（"至少 0% 分歧"），不保证以后仍为 0；
**从登记删除**会让 Direction 2 接管（未登记列必须为 0），把恒等式守死。
⇒ **真要撤销，删除比置零强。**

### ⑥ 变异验证（先 diff 确认落地再读结果）

| 变异 | 结果 |
|---|---|
| 分腿正向控制改成恒等比较（模拟 session 腿失明） | **Fatal**：`POSITIVE CONTROL FAILED ON THE SESSION LEG … (0/102/0/0) are the absence of a measurement, not agreement` |
| `client_model` 下限临时置 0（模拟「移除登记」） | **ok** 全绿 ⇒ 证明那条红**仅**由过期下限造成，撤销路径是通的 |

### ⑦ 遗留

- ⚠ **`client_model` 值分歧登记待撤销，需生产核对**（卡 D30-b）。撤销应**删除条目**。
- ⚠ `outbound_model` 下限 20% 仍成立（实测 22.3%）但**已贴近下限**，
  同一套归一化逻辑的另一面。
- ⚠ 错误信息引用的 NULL 剖面**只对 `client_model` 测量**，代码已加保护：
  仅当失败列就是它时才引用，**不虚假引用**。否则需另行补测。

### ⑧ 待拍板

**D30-b（252 只读凭据，唯一硬阻塞）** —— 现在它同时卡着
`client_model` 登记的撤销判定、D30-a 列存形态、D31-a 生产核对、829 部署时机。
D32 + D29-d 生产切换时点；`outbound_model` 20% 下限是否也该复核。
沿用未决：D28-a/b、D27-a/b/c、D26-a/b、D25-a/b/c、D24 系列、D23 系列、
D21-a/b、D20-a/c、D19-b、`RetirementColumnFill` 是否应作为仓库内活库快照。

### ⑨ 下一轮提示词

1. `git fetch && git rev-parse origin/main`；`git worktree add --detach /tmp/<新> origin/main`。
2. **所有编辑只在 worktree 里做**；共享主工作区此前处于未解决合并冲突。
3. `export GOCACHE=/tmp/gocache-<worktree名>`。
4. ⚠ SQL 在 Go raw string 里，**注释里不能出现反引号** —— 会截断字符串
   （本轮踩到一次，报 `missing ',' in argument list`）。
5. ⚠ 真库门跑在**共享的、持续被写入的库**上，同一门两次跑出的行数就不同；
   before/after 必须**同一次运行**内取两个数。
6. ⚠ **看视图定义要看全貌**：三分支 UNION 的排除条件常只作用在最后一个分支。

### ⑩ 补记：终身总体有 44.6% 是合成压测流量（§9.210.8）

清探针残留时发现 `request_id LIKE 'probe-direct-%'` 有 **753,425** 行 = 全表 **44.6%**，
**全部在 2026-09 分区，2026-10 为 0**。命名形如
`probe-direct-c11-mdoub…` / `c126-mgem…` / `c31-mclau…` / `c71-mgpt-…`，
像是模型基线压测，名字里编码了被探测的模型。

⚠ 这给 §9.209 的「近期窗口」补了一条**当时没想到的独立理由**：
我当时的理由是「窗口要短到不横跨变更」；还有一条更硬的 ——
**终身总体里 44.6% 是一批已经不再产生的合成流量**，
它的列特征会把终身速率拉成一个**描述一个已不存在总体的数字**。
近期窗口天然排除这批行。

⇒ 也给「`RetirementColumnFill` 这张活库快照该不该留」添了一条论据：
它测的是一个 **45% 由压测流量构成**的总体。

⚠ **未验证**：这批流量是什么工具、何时跑的、还会不会再跑，本轮没查
（`storage` 侧无变更史，同 §9.202 的 `admin_logs_native_turns_read`）。
**不据此改任何结论。**

---

## §70.52 顶层总闸「S4 能不能开」= **不能**；而它的措辞原本在替人说「正在恢复」（§9.211）

### ① 回到目标本身：`request_logs` 现在能不能 DROP？

前面几节都在查部件。这一节问总闸。答案：**不能**，卡在两处，顺序不能颠倒。

**① S4（停写）尚未开启 —— 这是先决条件。**

实测最近 2 小时写入量：

| 面 | 行数 | 最新一行 |
|---|---|---|
| `request_logs_hot` | **1,136** | 21:39:59 |
| `session_turns_hot` | 418 | 21:39:59 |

⇒ **v1 仍在写，且比 session 臂多。** 登记表里那个
`telemetry/client.go`「v1 writer」**不是历史遗留，是当前活跃的**。
DROP `request_logs` 前它必须被移除或改指向，否则 INSERT 直接报错。

⚠ 顺带一个事实（**未查原因，不据此下结论**）：
`request_logs`（**月度面**）最近 2h 新增 **0** 行、最新一行停在 **13:30:41**，
而热面 21:39 仍在写 ⇒ 热→月搬迁看起来滞后/停了。

**② 5 个已登记 breaker**（`TestRequestLogsRetirementBreakersRegistryIsConsistent` **PASS**，
登记与实测一致、无漂移）：`admin/work_types.go`、`db/db.go`、
`telemetry/client.go`、`domains/streaming/model_alternatives.go`、
`cmd/gateway/dual_read_validator.go`。

### ② S4 度量门的数，和它**说错的一句话**

`TestS4GateMeasurement`（仓库已有的总闸）实测：

| 窗口 | internal_loopback | non_terminal | genuine_loss | s4_ready |
|---|---|---|---|---|
| 1h | 0 | 0 | 0 | **true** |
| 24h | 1 | 20 | **4** | false |
| 7d | 3,004 | 205 | **10** | false |
| 30d | 32,880 | 1,628 | 10 | false |

**门的总结行写的是**「historical … none in the last hour — **decaying**」。

⚠ **这句话是错的**：10 次里有 **4 次在最近 24 小时内**。今天还在丢，
而这句话读起来像「已恢复、可以开了」。

### ③ 根因：判据**从不看 24h**，只覆盖了三种状态里的两种

原代码是 `if 1h>0 && 30d>0 {ONGOING} else if 30d > 1h {decaying}`。
「1h=0、24h=4、30d=10」落进第二分支。
★ 与 §9.209/§9.210 同一类：**措辞像一个结论，判据只覆盖部分状态。**

改成**四态全划分**（仍是报告、不是断言——「S4 能不能开」是发布决定，属决策表）：
`ONGOING` / **`RECENT, NOT YET HISTORICAL`**（1h 干净但 24h 仍有 ⇒
「每天发作几次的丢行机制，在两次事件之间长得一模一样」）/
`historical … decaying` / `clean`。

外加一条**有牙**的断言：四个窗口是同一行源 + 同一谓词 + 逐渐放宽的 `ts` 边界
⇒ 计数**必须单调不减**；任何窗口被改 scope 都会立刻报红，
因为那意味着这些数字来自**两个不同的总体**。

### ④ 变异验证（先 diff 确认落地再读结果）

| 变异 | 结果 |
|---|---|
| 打乱窗口顺序（模拟某窗口被错误缩小） | 红：`window 1h … but the wider 30d window reports genuine_loss=10 … one of them is scoped differently` |
| 拿掉 24h 分支（退回旧的二分支判据） | 绿，但输出变成 **`historical: all 10 genuine losses are older than 24h — decaying`** —— 而 4 次就在 24h 内。**这就是缺陷本身** |

### ⑤ ⚠ 我这一轮差点报出一个**来自另一份测量**的数

为给「v1 还在写」配分母，我手写 SQL 数「v1 成功行里没有 session 双生行的」，
得 **519/932（56%）**。⚠ **这个数不能用**：S4 门把漂移分成
`internal_loopback` / `non_terminal` / `genuine_loss` 三类，
大量「无双生行」是**合法不镜像**的（内部回环、非终态轮次）。

⇒ 与 §9.211.3 同一根因的另一面：**同一件事只能有一份测量**。
`genuine_loss` 的口径由生产代码 `mirrorDriftClassSQL` 定义，
手写一条「看起来等价」的 SQL 就会得到一个**大 100 倍**的数。
**已弃用该数，改用门自己的输出。**

### ⑥ 给属主的一句话

`request_logs` **现在不能退役**：① S4 未开（7d `genuine_loss=10`，
**24h 窗口仍有 4，今天还在丢**）；② 5 个已登记 breaker。
⇒ **在 ① 变绿之前讨论 ② 的切换时点（D29-d）没有意义。**

### ⑦ 待拍板

- ⚠ **S4 开启时点**（本轮新增，且是所有退役事项的前置）；
- ⚠ **D30-b（252 只读凭据）** —— 本机测得的 `genuine_loss` 只能说明本机，
  生产是否同样在丢**无法回答**；
- D32 回填 + D29-d 切换时点（须在 `request_logs` DROP 之前）；
- `client_model` 值分歧登记是否删除；`outbound_model` 20% 下限复核；
- `RetirementColumnFill` 是否继续作为仓库内活库快照。
沿用未决：D28-a/b、D27-a/b/c、D26-a/b、D25-a/b/c、D24 系列、D23 系列、
D21-a/b、D20-a/c、D19-b。

### ⑧ 下一轮提示词

1. `git fetch && git rev-parse origin/main`；`git worktree add --detach /tmp/<新> origin/main`。
2. **所有编辑只在 worktree 里做**；共享主工作区处于未解决合并冲突。
3. `export GOCACHE=/tmp/gocache-<worktree名>`；
   **真库门必须同时导出 `TEST_DATABASE_URL`**，否则真库测试是 **skip 而不是 fail**，
   全绿具有误导性（本轮已踩：`go test ./cmd/gateway/` 1.67s 返回 ok，实际全 skip）。
4. ⚠ 报任何「缺失/漂移」比例前，先确认口径与生产代码里的分类器一致
   （`mirrorDriftClassSQL`），**不要手写一条看起来等价的 SQL**。
5. ⚠ SQL 在 Go raw string 里，注释中不能出现反引号。
6. ⚠ 真库跑在**共享的、持续被写入的库**上；跨运行的数字会变，
   before/after 必须**同一次运行**内取两个数。

---

## §70.53 卡住 S4 的 10 次「真实丢行」：9 次是**带会话头的探针**，2026-10-02 起的新现象（§9.212）

### ① 构成（用**生产分类器原样**分类）

30d `genuine_loss` = **10 行**：

| origin_actor | request_status | error_kind | 行数 | 首见 → 末见 |
|---|---|---|---|---|
| `probe-service` | failure | `no_candidate` | 7 | **10-02** → 10-04 |
| `probe-service` | failure | `routing_schema_error` | 1 | 10-04 |
| `probe-service` | failure | `no_candidates` | 1 | 10-03 |
| (null) | failure | `session_unavailable` | 1 | 10-03 |

⚠ **9/10 是 `probe-service`**，全部 `is_auto_request` NULL、`work_type` NULL、
**全部始于 2026-10-02**（此前 30 天一天都没有）。

⚠ **我第一次用手写预筛跑，得到「11,693 行」**，与门报的 10 差 1000 倍——
因为预筛没排 `internal_loopback`。**这正是 §9.211.5 自己写下的教训，
隔一轮又踩了一次。** 改用 `db.MirrorDriftClassSQL` 三条分支逐字复刻才对。

### ② ★★ 我提的假设被**真实谓词**推翻了

`hook.go:91` 有条早就在的探针门，注释写「**无会话头**的探针不进 mirror」，
而 `MirrorDriftClassSQL` 的注释又警告「hook 跳过但 SQL 判成 genuine_loss 的行
会让 s4_ready 永远为假」。⇒ 我形成假设：**SQL 与 Go 门不同步，缺第三条臂**。

**错了。** `IsProbeSyntheticSession` 第一行：

```go
if entry.GwSessionID != nil && *entry.GwSessionID != "" { return false }  // 有会话头 ⇒ 不跳过
```

这 9 行**全部带 `gw_session_id`**（漂移口径本身就要求它非空）
⇒ **hook 本该镜像它们 ⇒ 它们是真丢行，不是分类器漏了一条臂。**

★★★ **注释与代码方向相反**：注释说「**无**会话头被排除」，
谓词是「**有**会话头就 return false（不排除）」。
**只读注释就动手，会做出一个把真实丢行藏起来的「修复」。**

### ③ 异常的形状：探针轮次**获得了真实会话 id**

| | 会话 id 形态 | 落点 |
|---|---|---|
| 正常探针（`probe-direct-*`，753,425 行） | **无** `gw_session_id` | 合成 `sys:probe*` 会话，**有 turn** |
| 这 9 行 | **有** `gw_<uuid>` | 该 session_id 下 **0 turn** |

且**连 details 层都是空的** ⇒ **不是写了一半，是什么都没写。**

⚠ 30 天里 `auto-title/summary-generator` 的 11,000+ 行**全部**落进
`internal_loopback` ⇒ **回环臂工作正常**；不正常的只有这一类。

### ④ 我**没有**改分类器

加一条探针排除臂能让 `s4_ready` 转绿，⚠ 但那是「放宽到刚好不红」，
而且按 ② 这些行**本该被镜像**，排除它们等于**把真实丢行藏起来**。

### ⑤ 我**查不到**的部分（不猜）

无法判断是 **hook 没被调用**还是**调用了但写失败**：
本机**没有网关进程**在跑（写入来自别处），我没有这些请求的日志。
⇒ 需要属主提供 gateway 日志或确认写入来源。

### ⑥ 修正 §9.211.6 的一半措辞

§9.211.6 写「24h 仍有 4，今天还在丢」。**测量准确**，但「丢」易被读成
「丢的是业务轮次」。**现在可以精确说**：卡住 S4 的 10 行里
**9 行是 10-02 起新出现的「带会话头的探针」**，全在选型阶段失败
（`no_candidate` / `routing_schema_error`），**没有任何 turn 或 details**；
另 1 行 `(null)`/`session_unavailable`。
**不是**回环误分类，**也不能**说它们「不算丢失」——按 hook 谓词它们**本该**被镜像。
⇒ 要让 S4 转绿，答案是「这 9 行为什么没被镜像」，**不是**调整分类器。

### ⑦ 待拍板

- ⚠ **这 9 行为什么没被镜像** —— 需 gateway 日志 / 写入来源确认（本轮唯一新增的实质问题）；
- ⚠ **D30-b（252 只读凭据）**：本机 9/10 是本机探针，**生产是否同样**无法回答；
- S4 开启时点（其余事项的前置）；D32 + D29-d 切换时点；
- `client_model` 登记是否删除 / `outbound_model` 20% 下限复核 /
  `RetirementColumnFill` 是否继续作为仓库内活库快照。
沿用未决：D28-a/b、D27-a/b/c、D26-a/b、D25-a/b/c、D24 系列、D23 系列、
D21-a/b、D20-a/c、D19-b。

### ⑧ 下一轮提示词

1. `git fetch && git rev-parse origin/main`（当前 `0bbece77c`）；worktree 开工。
2. **所有编辑只在 worktree 里做**；共享主工作区处于未解决合并冲突。
3. `export GOCACHE=/tmp/gocache-<worktree名>`；**真库门必须同时导出
   `TEST_DATABASE_URL`**，否则真库测试是 **skip 而非 fail**。
4. ⚠ **注释可能与代码方向相反**：动手前读**谓词本体**。
   本轮 `hook.go` 注释说「无会话头的探针被排除」，真实代码是
   「**有**会话头就 return false（不排除）」。
5. ⚠ **不要手写预筛替代生产分类器**（本轮两次踩到，量级差 100–1000 倍）。
6. ⚠ SQL 在 Go raw string 里，注释中不能出现反引号。
7. ⚠ 真库跑在**共享的、持续被写入的库**上；跨运行数字会变，
   before/after 必须**同一次运行**内取两个数。

### ⑪ 把「所有有记录的失败路径」都排除了，并给总闸加了「阻塞行有没有被失败机制记录过」（§9.212.8–9）

§9.212.6 说「查不到」。本轮用**数据 + 代码**把它收窄了一大截。逐条排除：

| 路径 | 判定 | 依据 |
|---|---|---|
| `!Success && !isTerminalFailure` | 排除 | `request_status='failure'`，Go 常量 `RequestStatusFailure = "failure"`（`client.go:249`）与库中字面**逐字相同** ⇒ 返回 true |
| `IsProbeSyntheticSession` | 排除 | 有 `gw_session_id` ⇒ 第一行 `return false` |
| `IsInternalAutoEntry` | 排除 | 需 `IsAutoRequest != nil && *IsAutoRequest`，这 9 行是 **NULL** |
| `!shadowWriteEnabled()` | 排除 | `settings_kv.sessions_v2.shadow_write = true`（2026-07-21 起未变） |
| `EnqueueMirrorFailure("semaphore_full")` | 排除 | `session_mirror_outbox` **总行数 = 0** |
| `EnqueueMirrorFailure("write_failed")` | 排除 | 同上，9 个 request_id 一个都不在 |
| replay 三道门 | 排除 | `replay.go:418/426/432` 同三道；跳过会**删除**行、重放成功会有 turn，两者都不是观察到的状态 |
| 部分写 | 排除 | turn / details 按 `request_id` 与 `gw_session_id` 全部为 0 |

⇒ **hook 里每一条有记录的失败路径都不成立。** 只剩一类解释：
**entry 压根没走到 hook 的写入尝试**（上游某条产出 v1 行的路径没调用 hook），
或存在一条我没找到的、**无日志无指标**的早退。

⚠ **hook 的 8 个入口里只有 2 个留痕**（`entryToProcessedRequest` 返回 nil、
写失败的 `slog.Warn`），其余早退全是**静默 return** ⇒
「上游没调用 hook」这一类**天生不可观测**，这正是它活到今天的原因。

**新增判据**（`TestS4GateMeasurement`）：阻塞行**有没有被任何失败机制记录过**。
全仓只有两处持久化失败的镜像尝试（`hook.go:180` / `hook.go:279`），都落进 outbox。
⇒ **有** outbox 行 = 已知可重试的丢；**没有** = 丢在失败记录机制**之前**，
**调 reaper、查 lease 都找不到**。混为一谈会让两边看起来像同一个问题。

实测：outbox **总行数 0**，10 个阻塞行**全部**属第二类。
窗口从 `s4Windows` 表读，不重拼字符串。
**变异**：反转 outbox 的 `NOT EXISTS` ⇒ `0 of 10` 且 `⇒` 行消失 ⇒ 条件承重。

⚠ 这**没有**定位到机制，只是把「丢在哪里」从四种收窄成一类，
并让这一类**每次运行都被测量**，不再靠一次人工排查。

### ⑫ 待拍板（更新）

- ⚠ **这 9 行为什么没进 hook** —— 需 gateway 日志 / 写入来源确认。
  本轮已把「有记录的路径」全部排除，剩下的**不可从数据库观测**（早退全静默）。
- ⚠ **D30-b（252 只读凭据）**：本机这 9/10 是本机探针，生产无法回答。
- S4 开启时点（其余事项的前置）；D32 + D29-d 切换时点；
  `client_model` 登记是否删除；`outbound_model` 20% 下限复核；
  `RetirementColumnFill` 是否继续作为仓库内活库快照。
沿用未决：D28-a/b、D27-a/b/c、D26-a/b、D25-a/b/c、D24 系列、D23 系列、
D21-a/b、D20-a/c、D19-b。

### ⑬ ⚠⚠ 撤回 §70.53 ⑪ 的一个断言：把「会被排空的队列」当成了「从未被填过」

⑪ 里我写了「**每一个**阻塞行都绕过了两个 `EnqueueMirrorFailure` 调用点」。
**这个断言不成立，撤回。**

**错在量纲**：我读的是 outbox 的**活行数**（0），当成「是否曾被写入」的证据。

| 计数器 | 值 | 含义 |
|---|---|---|
| `n_live_tup` | 0 / 1 | **水平**——两次运行之间就从 0 变成过 1 |
| `n_tup_ins` | **2,502** | 被**持续写入** |
| `n_tup_del` | **2,498** | 被**持续清空** |

且被清空的行是**删除**不是死信：replay 三条 skip 分支调 `deleteRow`
（`replay.go:419/427/433`），而 `markDead` 是 `UPDATE … SET status='dead'`
（`replay.go:522`）**保留行** ⇒ 死信仍计入活行数，那 2,498 次删除**全是 skip**。

⇒ **活行数 0 无法区分「从未入队」与「入队后被跳过并删除」。**
分开二者的是 reaper 的**日志行**，不是这张表。

⚠ **自我演示**：同一段代码两次运行分别打印 `live=0` 与 `live=1` ——
**活行数就是会变的**。这是「水平读数不是发生过的证据」的直接演示。

**修正后的判据只声称能支撑的那件事**：阻塞行若**当前仍在 outbox**
（pending/dead），失败机制**还持有**它 ⇒ 不查日志也能行动。
其它只是「**当前没被持有**」，**那不是诊断**。三分支 + 常驻打印三个计数器。
**变异**：去掉 `EXISTS` 守卫 ⇒ `10 of 10 HELD`，消息切到「已被记录」那一支 ⇒ 承重。

⚠ **净结论**：§9.212.8 那张「八条路径逐条排除」的表**仍然有效**
（那是代码 + 数据库上的静态排除），但它排除的是「**有记录的**失败路径」；
⑪ 误以为还能进一步排除「入队后被跳过删除」——**不能**。
⇒ 「这 9 行为什么没被镜像」**重新回到需要日志**，但现在知道**该找什么**：
reaper 的 `skipped:` 删除行，以及 `semaphore_full` / `write_failed` 两条入队路径。

★ 同族教训：**队列的「活行数」是水平量，队列的性质决定该用速率量**；
看到一个 0 之前，先问「这张表会被排空吗，死信会被保留吗」。

### ⑭ 待拍板（更新）

- ⚠ **gateway 日志**：找 reaper 的 `skipped:` 删除行与两条入队路径（本轮把范围收窄到这些）。
- ⚠ **D30-b（252 只读凭据）**：本机这 9/10 是本机探针，生产无法回答。
- S4 开启时点（其余事项的前置）；D32 + D29-d 切换时点；
  `client_model` 登记是否删除；`outbound_model` 20% 下限复核；
  `RetirementColumnFill` 是否继续作为仓库内活库快照。
沿用未决：D28-a/b、D27-a/b/c、D26-a/b、D25-a/b/c、D24 系列、D23 系列、
D21-a/b、D20-a/c、D19-b。

### ⑮ ★★★ 破案（§9.213）：失败**记录**的那条路，没有活过它要记录的那次失败

⑬ 我说「需要日志」。⚠ **那个说法是错的：日志一直在我手上**
——`docker logs llm-gateway-local-8782`。我先去找「哪台机器在写」，
查到写入方是容器之后**没有回头看容器日志**。

写入方确认：`pg_stat_activity` 客户端全是 `172.18.0.x`；容器跑
`kx-llm-gateway-local:2.5.8.2449`，`StartedAt = 2026-10-04T09:29:10Z`。
九行里**只有 `59bf8998…`（10:35:28Z）在启动之后** ⇒ 只有它有逐条日志。

**那一行的决定性两行**：
```
10:35:38 WARN final-success claim: rollback to savepoint failed  error="conn closed"
10:35:38 WARN telemetry request db persist failed; fallback written  op="update"  error="conn closed"
```
`op="update"` = **终态**那一笔失败了 ⇒ 库里那行至今是 `in_progress`（**与实测一致**）。

**镜像侧（当前容器 4.6 小时日志）**：`V2 shadow write failed` **53** 次
（~11.5/h）、`context deadline exceeded` 132、`conn closed` 13、
**`outbox row dead` 0** ⇒ 这些都被 replay 捞回来了，与
`n_tup_ins=2502 / n_tup_del=2498`（入出 1:1）吻合。

**★ 断链的那一环**：`hook.go:279` 兜底是
`if !EnqueueMirrorFailure(...) { appendBacklog(...) }`，
而 `EnqueueMirrorFailure` 要 **INSERT** 到 outbox ——
**在 `conn closed` 的这一刻这个 INSERT 必然也失败**。
`appendBacklog` 是**进程内**有界 slice，满了**丢最老的**；
注释自认「drain via `DrainBacklog` or a future background reaper（spec §12 GAP 2）」。
⚠ **全仓核实：`DrainBacklog(` 只有 `backlog_test.go` 4 处调用，生产零调用。**

⇒ **完整因果链**：连接死 → 镜像写失败 → 记录失败也要写库（同样失败）
→ 退到进程内队列 → **无人排空** → 进程重启（17:29 重建；连接史始于 **10-02**）
→ **无 turn、无 details、无 outbox 行**。
⇒ 这也解释了 **10-02 这个起点**：那天有一次重启，把内存里积的清零了。

⚠ **不是逻辑缺陷，是耐久性缺陷**：失败恢复依赖一条**必须先成功**的写路径，
而它要记录的正是「这条写路径刚刚失败」。

**诚实边界**：第 1–3 节是 `59bf8998` 的**逐条日志直接证据**；其余 8 行日志已随
上个容器销毁 ⇒ 本节给的是「由同一机制推出、且与库中观测一致」的解释，
**不是**逐行证明。生产完全未验证（D30-b 阻塞）。

### ⑯ 本轮我犯的错（值得单记）

§9.212.6 我写「本机没有网关进程在跑……我没有这些请求的日志」。
前半句对，**后半句错**——写入方是**容器**，日志就在 `docker logs` 里，一行命令。
⚠ 形状很典型：我在**数据库**这条路上找证据找得极深（逐条排除 8 个分支、
查 outbox 计数），却**没先问「这个进程在哪儿、它的日志在哪儿」**。
⇒ 通则：**说「我拿不到 X」之前，先确认 X 的载体是什么**；
在数据库里找不到不等于不存在，它可能在**进程边界之外**。

⚠ 更贵的一层：我上一轮把这个错误结论**推了 main**（`591eaca20`），
而修正（`b451bd298`）只撤回了其中一条断言，**没有**指出「日志一直在手上」——
**回应的对象错了**。

### ⑰ 待拍板（更新）

- ⚠ **失败记录的耐久性**：兜底需要一条**不依赖那条已死连接**的路
  （独立连接 / 本地 WAL / 换队列）。这是设计决定，需属主定。
  现状是 GAP 2 未建 + `DrainBacklog` 生产零调用。
- ⚠ **D30-b（252 只读凭据）**：本机 9/10 是本机探针，生产无法回答。
- S4 开启时点；D32 + D29-d 切换时点；`client_model` 登记是否删除；
  `outbound_model` 20% 下限复核；`RetirementColumnFill` 是否继续作为仓库内活库快照。
沿用未决：D28-a/b、D27-a/b/c、D26-a/b、D25-a/b/c、D24 系列、D23 系列、
D21-a/b、D20-a/c、D19-b。

### ⑱ ⚠ 第二次修正 §9.213：丢行在**镜像上游**，镜像的失败恢复从未介入（§9.214）

§9.213.4–5 说「镜像写失败 → 记录失败也要写库（同样失败）→ 退到进程内 backlog
→ 无人排空 → 重启归零」。**第 3–4 步是错的。**

**两条硬证据：**
1. 容器全量日志里 `degrading to in-process backlog` = **0** ⇒ 那 53 次
   `V2 shadow write failed` **每次入队都成功**（与 `outbox row dead=0`、
   `n_tup_ins≈n_tup_del` 吻合）。
2. `59bf8998…` **自己**没有 `V2 shadow write failed` 行；10:35:31.77 那条属于
   **另一个** request `384a9caa…`（只差 7 秒，**极易误并**）。

**读代码本体**（`telemetry/client.go`）：
```go
err = c.updateRequestLog(entry)   // … 或 insert / sink
if err == nil { c.firePersistedHooks(entry) }   // ← 只在成功时
```
session turn 镜像**就是** `onPersisted`；`firePersistedHooks` 全仓两处调用
（783 / 1202）**都在成功路径**；fallback 与 degraded 路径直接 return。
⚠ 开头那句「H3……PG 不可用时镜像仍写入」指的是 `mirrorRequestBodies`
——**正文**镜像，**不是** session turn 镜像。**同名，极易看错。**

⇒ **真正的链（更短、也更上游）**：连接死 → v1 终态 UPDATE 失败 → 写 fallback
→ `err != nil` ⇒ **`onPersisted` 不触发** ⇒ **镜像从未被调用**
⇒ 无 turn、无 details、v1 停在 `in_progress`。
⇒ 镜像的失败恢复只覆盖「**v1 写成功、镜像写失败**」；
**「v1 自己写失败」根本不在它的覆盖范围内**，所以它的仪表**结构上看不见**。

**仍成立**：v1 UPDATE 因 `conn closed` 失败（逐条日志）、那行停在 `in_progress`、
三项全 0、10-02 与重启重合、53 次 shadow write 失败全被捞回。
**撤回**：「记录失败也要写库」「进程内 backlog 无人排空」作为这 9 行的成因。
⚠ `DrainBacklog` 生产零调用**仍是真实缺口**（且 `backlog.go:108` 那句
「counter 已记录每次丢弃」不准确——记的是**失败**不是**丢弃**），
但**不是这 9 行的原因**。我在 `40dfa7546` 把两件事混为一谈了。

★ **我这一轮第三次在同一条链上改结论**，三次都是「把相邻环节当成一个」：
① 日志一直在我手上；② 入队从未失败；③ `59bf8998` 与 `384a9caa…`
**只差 7 秒但是两个 request**。
⇒ **因果链每一步都要有独立观测**，不能因为相邻且时间接近就并成一个事件。

### ⑲ 待拍板（更新）

- ⚠ **「v1 持久化失败」这一类丢行要不要恢复**：它**不在镜像失败恢复的覆盖范围内**，
  属设计问题（要么 v1 失败也走 onPersisted，要么显式承认这类丢行并给出口径）。
- ⚠ **D30-b（252 只读凭据）**：生产完全未验证。
- S4 开启时点；D32 + D29-d 切换时点；`client_model` 登记；`outbound_model` 下限；
  `RetirementColumnFill` 快照去留。沿用未决：D28-a/b、D27-a/b/c、D26、D25-b/c、
  D24 系列、D23 系列、D21、D20、D19-b。

---

## §70.54 我把 §9.213/§9.214 挂到了**错误的总体**上，而且方向是反的（§9.215）

### ⑳ 撤回

§9.213/§9.214 的整条链（「v1 持久化失败 ⇒ `onPersisted` 不触发 ⇒ 镜像从未被调用」）
**不适用于 `genuine_loss`**，而 `genuine_loss` 才是 S4 门在管的总体。

被挂错的那一环：**`59bf8998` 按生产分类器属于 `non_terminal`，不是 blocker。**
`non_terminal`（30d 1,631 行）**按设计就不计入丢行** ——
我拿一条门本来就不管的行，去解释门正在管的丢行。

而且不是「缺证据」，是**方向相反**：`genuine_loss` 的 11 行**全部**是
终态失败（`request_status='failure'` + `error_kind` 非空），
即 **v1 写成功了** ⇒ `firePersistedHooks` 已触发。

### ㉑ 为什么这是结构性的：门只报数，不报形状

`hook.go:78` 第一道门 `if !entry.Success && !isTerminalFailure(entry) { return }`，
而 `isTerminalFailure`（hook.go:1093）**在 `Success` 为真时直接返回 false**
⇒ 成功行直接通过。
`MirrorDriftClassSQL` 的 `ELSE` 臂按构造就是「success **或** 终态失败」。

⇒ 两条 SSOT 合起来：**每一行 `genuine_loss` 都必然到过 `PersistHook`。**
不需要任何日志。丢行只可能在 hook **之后**。

而门只输出 `genuine_loss=11`，不输出这 11 行长什么样 ——
所以「上游还是下游」在库里无法回答，我只能拿手边唯一有日志的行倒推，
而那一行不在这个总体里。**这是判据缺形状，不是判据红。**

### ㉒ 活体机制（方向相反的那条）

查询时总体正在增长（30 分钟 +1），抓到带完整日志的 blocker `7204907f9c5f…`：

```
upstream_status=200 → audit: request completed success=true
→ WARN sessionv2mirror: V2 shadow write failed
    error="write turn: insert turn: timeout: context deadline exceeded"
```

- v1 写成功（三个 fallback 后端**全只写文件/环形缓冲，没有一个写 PG**
  ⇒ PG 里有这行 ⇒ 主路径 `err==nil`）；
- hook 被调用；
- **turn 写超时**；
- **恢复路径确实入队了**（`7b475e9e…` `status=pending` `attempts=2`，
  错误是同一个超时）—— 与 §9.213 的猜测相反，reaper 在重试但**仍然超时**。

当前容器 5h 内 `V2 shadow write failed` **82** 次，含
`enqueue session aggregate outbox: context deadline exceeded` ×5
⇒ **兜底与主路径抢同一个正在超时的资源**（该通则第四次印证）。

⇒ **S4 卡住的是一条正在超时的会话写路径，不是镜像逻辑。**

### ㉓ 改动

`cmd/gateway/s4_gate_measurement_test.go` 新增 **blocker 形状剖面**：
按 `origin_actor × request_status × error_kind × success` 分组，
用**同一个** scope + **同一个** `mirrorDriftClassSQL`（不重抄），
两条断言：① 剖面求和 == 报的计数；② **不允许出现
`success=false 且无终态标记` 的行**（那种行按 hook 第一道门根本没到过镜像，
是另一种缺陷）。真库实测通过，11 行 / 5 组，最新一行 7h8m 前。

### ㉔ 遗留（本轮新增）

- ✅ 断言 ② 的**阴性对照已补**：断言 ② 在数据上**不可证伪**（`ELSE` 臂本身
  就是「success 或终态」，能触发它的行会被分成 `non_terminal`，进不了剖面），
  所以它是**漂移绊线**而非数据探测器。正解同 §9.209 —— 抽出可测谓词
  `blockerSkippedByFirstGate`，用 `TestBlockerSkippedByFirstGate_PositiveAndNegative`
  打双向对照（4 阴 + 1 阳，阳性对照就是 `59bf8998` 的形状），5/5 通过。
- ⚠ `get next turn_no`（`turn_writer.go:340`）用
  `MAX(turn_no)+1` 从**分区视图** `session_turns_with_current_month` 算，
  在**按 request** 的 advisory lock 事务内（该锁不串行化同 session 并发）。
  实测最热 session **231ms**，`Merge Append` + top-N heapsort。
  **这是水平读数，不是超时根因** —— 下一轮的活。
- ⚠ 我自己交接里记的「30d = 14，含 node-probe-worker 4」是**错的**，
  实测 **11**。那份 14 出自我手写预筛 SQL 漏了 `internal_loopback` 那条臂
  —— **本轮第二次犯「手写预筛代替生产分类器」**。
- 待拍板沿用 §⑲；并新增一条：**会话写路径的 deadline 与并发分布要不要治**
  （它才是 S4 的实际阻塞项）。

### ㉕ 补记：恢复路径**有效**，永久丢行 = 「写超时 ∩ 入队也超时」

rebase 后复验，`genuine_loss` 从 11 变 10。查差掉那行 `7b475e9e`：
`session_turns_hot` 有 **1** 行、outbox 已清空 ⇒ **replay 重试成功救回**。

outbox 全局 `dead=0 / pending=0 / live=0`，`n_tup_ins=2616 / n_tup_del=2612`
⇒ **一条死信都没有**；`markDead` 是保留行的 UPDATE，真死过表不该是 0。

⇒ **凡入队的都救回了** ⇒ 剩下 10 行**从未入队**，
对应日志里的 `enqueue session aggregate outbox: context deadline exceeded`（5 次）。

⇒ 完整链条（每环独立观测）：
**v1 写成功 → hook 触发 → turn 写超时 → 入队成功则 replay 救回（实证） /
入队也超时则永久丢行且零痕迹。**

⇒ 决定性的是**交集**：「写超时」不够，「入队也超时」才丢。
**兜底与主路径共享同一个正在超时的资源** —— 该通则第四次印证，
这次带可测量后果（10 行永久丢行）。

⚠ 未回答：turn 写为何超时。`MAX(turn_no)` 231ms 已测，
**并发分布与 deadline 值未测** —— 下一轮的活。

---

## §70.55 第二次改判 S4 的 blocker 主体，并撤回我自己的两处结论（§9.216）

### ㉖ 撤回 ①：`MAX(turn_no)` 231ms **不是**根因

§9.215.4 拿最热 session 的 EXPLAIN（231ms）当候选根因。补测分布：
**840,977 个会话，p50 = p90 = p99 = 每会话 1 turn，max 53,851。**
我测的是 **0.0001% 的离群点**。

⚠ 与 §70.54 那个「14 vs 10」是**同一族错误**：**拿一个测量点当总体**。
上一轮我写了「已测**未**测并发分布」——**留口子不等于没误导**，
读者仍会当根因候选。它是真实性能缺陷，但解释不了 99% 的会话。

### ㉗ 撤回 ②：「写超时」不是常驻阻塞

§9.215.3 逐行追踪的 `7204907f9c5f`（success=true，turn 写超时）
**不在当前 10 行里** —— 它就是 11→10 时消失的那行，**被 replay 救回了**。

⇒ 写超时确实发生，但**可自愈**。把偶发当常驻阻塞机制，是又一次「把偶发当常态」。

### ㉘ 真实形状 + 分母

- **11 天精确为 0**（09-21…10-01，逐日 2.4k–151.8k 行，合计 >70 万），10-02 起 3/3/4。
- 分母：10-02…10-04 是 **0.094% / 0.129% / 0.105%**。
- ⚠ 此前只报计数，**没有分母的计数无法判断严重性**。

### ㉙ blocker 内部是两个总体

| 切分 | 行数 | 该怎么修 |
|---|---:|---|
| 所在会话 `session_turns = 0` | **9** | 排除/分类问题，**提速无用** |
| 所在会话有 turn（314） | 1 | 写路径时序 |

那 9 行：**每个会话整个 `request_logs` 只有 1 行**，且那行失败
（`no_candidate`×7 / `no_candidates` / `routing_schema_error`）
⇒ 「单请求会话，唯一请求在路由层被拒、从未派发上游」
（`origin_mw.go:418` 正是记这类早期退出「never reached the initial INSERT」）。

按 hook 跑没跑细分（dim 先于 V2 写）：`session_dim=1` **5 行**（V2 写没成）、
`session_dim=0` **4 行**（**不可判读**，无日志能分开「hook 未跑」与「dim 失败」）。

### ㉚ 根因的形状：两个既有排除键在不同属性上

| 排除 | 判据 | 这 10 行 |
|---|---|---|
| `IsProbeSyntheticSession` | **无** `gw_session_id` | **有** ⇒ 不算探针 |
| `internal_loopback` 臂 | `is_auto_request = TRUE` | **NULL** ⇒ 不命中 |

⇒ `origin_actor='probe-service'` 说明它**语义上是探针**，
但**没有任何排除项按 actor 判探针**。
⇒ **S4 的常驻 blocker 主体是「排除覆盖缺口」，不是容量问题**。
也解释了为何 09-21…10-01 精确为 0：这批探针是 10-02 前后才带会话头落库的。

### ㉛ 新增方法论护栏

容器 `2.5.8.2449` 的 `version.json` = **`git_sha=57d69f9c`**（HEAD 的祖先）。
我前几轮**一直拿 HEAD 源码解释那个二进制**。已核对关键常量在 `57d69f9c` 一致，
已发表结论未被推翻。⇒ **今后任何「机制」断言要么在运行的那个 commit 上核对，
要么标注「读的是 HEAD」**。

⚠ 顺带排掉假线索：`write_timeout_ms` 我先查 `settings_kv`（unset）差点下结论；
`enable_sessions_v2.sql` 写的是 **`platform_settings`，而本库没这张表**
⇒ unset 碰巧对，但**是查对了表才对的**。「用某个口径量出 0」≠「不存在」。

### ㉜ 新增待属主（**这是 S4 真正要拍的板**）

**一个在路由层被拒、从未派发上游的探针请求，要不要成为一条 session turn？**
现有代码在「无会话头探针」上已选择**不**镜像（`hook.go:71` + R51 教训），
这批行只是**恰好有会话头**就绕过了那个决定。
- 若**不该**镜像 ⇒ 要扩排除（按 actor 或按「未派发」判），S4 可望转绿；
- 若**该**镜像 ⇒ 要修的是那 5 行「hook 跑过但 V2 写没成」，与另外 4 行无关。

⚠ 未闭环：9 行的成因 4 行不可判读；生产（252）完全未验证（D30-b）。

### ㉝ 那 4 行：本机无法判定，且我验掉了两个**看起来能用**的假设（§9.217.1）

- **假设 A（部署时间边界）证伪**：`10-03 16:21` 那行 `dim=0`
  **夹在两行 `dim=1` 中间** ⇒ 是每请求行为，不是边界。
- **假设 B（`error_kind` 能分开）证伪**：两边都有 `no_candidate`。
  （复数 `no_candidates` 走 `executor.go:2364` 的 router 路，与单数
  `provider/client.go:1177` 的 503 不同源，但**只解释 1 行**。）
- **我提出的静默 0 行 race 不成立**：`session_dim.go` 有
  `RowsAffected() > 0` 判据，0 行会走 VALUES 兜底。

⇒ **本机判不了。** 唯一能分开的是那个 request_id 的 gateway 日志行，
而容器已重建、日志已销毁。

⇒ 门新增 `mirror reach` 三分组并**断言求和**：`6` session_dim 有行（hook 跑过、V2 写没成）
/ `4` 无 dim 但 ctx_attrs 有（**两种成因，库分不开**）/ `0` 两者皆无。
⚠ 并在输出里明写「**不要把『没有 session_dim 行』读成『镜像没被调用』**」。

★ **自罚**：假设 A 长得极像结论（10-02 三连 0、之后连 1）。
**逐行时间序证伪了它**——差一点就把「10-02 前后有部署边界」写进文档。

### ㉞ 我的门自己有个跨快照求和断言（真缺陷，与那次偶发 FAIL 是不是它无关）

跑门时出现**一次** `cmd/gateway` FAIL（46.6s vs 其它次 14–28s），
**失败测试名没抓到**，随后连跑 4 次全过（14.9/15.2/24.4/28.5s），**未复现**。

我先怀疑判据——这次怀疑对了。`TestS4GateMeasurement` 有**三段求和断言**
（窗口计数 ↔ 形状剖面 ↔ 会话切分），断言它们划分同一集合；
但这三条是**独立查询**、跑在 `pgx.Connect` 上默认 **READ COMMITTED** ⇒
**每条各拿一个快照**，而总体**是活的**（新漂移行持续落、replay 又移走一行）。

⇒ 查询 1 与查询 3 之间落进一行，两个划分**合法地**不等，
断言报「scope 漂了」而**其实都对，是总体动了**。
★ **这个失败模式与真正的 scope 漂移不可区分 ⇒ 它训练人怀疑仪器。**

修法：整段量测进**一个 `REPEATABLE READ, READ ONLY` 事务**，五条查询共享快照。

⚠ **因果如实标注（两件事分开）**：
① 竞态是真的，但**与那次 FAIL 是不是它无关**（未抓到测试名、4 次未复现）。
② **代价我第一次也归因错了**：看到修复后单跑 150.6s（此前 9.5–28.5s）就写成
「修复的代价」。补测后 —— 隔离单跑 **150.6s(冷缓存) / 82.5s / 96.7s**，
全包 **47.9s**（修复前 14.0–28.5s）⇒ **真实代价约 2–3 倍，不是 5–10 倍**。
⚠ **本会话第三次「把一次观测直接归因」**：测到变慢要先分离「改动」与「缓存/负载」。

★ 通则：**多条查询 + 「应当互相吻合」的断言 + 会变的总体 = 迟早的假红。**

### ㉟ 节号撞车：我的 §9.217 改为 §9.218（对方在先，两段都保留）

rebase 到 `a926410f9` 时冲突：该链已用 **§9.217** 写「**★821 已经在 252
生产上跑着**，而且它选中的总体与 §9.66 的目标零重叠」——
**与我的 S4 内容完全无关，但节号相同**。
⇒ 我的改为 **§9.218**（对方在先），两段都**完整保留**，未合并、未删减。

⚠ **顺带一条对本轮全局的影响**：那条 §9.217 说交接文档写的
「821 未部署到 252」**不成立**，且已在真库上验过 ——
**D30-b（252 只读凭据）可能已不再是硬阻塞**。下一轮开工必须先核实
是否已有 252 通道，以及该节给出的 252 读数口径，
**不要**继续按「生产完全未验证」行动。

---

## §70.56 ★**D30-b 已解：252 生产实测**——生产的 blocker 与本地**不是同一个**（§9.219）

`ssh 252` 直连可用，`psql` 在该主机上可用，PG 在内网 `172.16.2.210:5432`。
**只做只读 SELECT，未改生产一字。**

### ㉞ 生产形态与本地根本不同

| 表 | 252 生产 | 本地 |
|---|---|---|
| `request_logs` 父表 | **0 行 / 0 字节** | 1.5M+ |
| `session_turns` 父表 | **0 行** | 1.69M |
| `session_turns_hot` | **1,733** | 活跃 |
| `session_dim` | **646,550** / 294 MB | 少 |
| `session_mirror_outbox` | **147** | 0 |

### ㉟ 生产 `genuine_loss = 10`，形状完全不同

`internal_loopback=8,001 / non_terminal=67 / genuine_loss=10`（共 8,078 行）。

生产那 10 行：**3 行是 `success`**、会话**全是 `gw_*` 真实会话**、
`error_kind` = `provider_error`×3 / `routing_database_error` / `transient` /
`rate_limit_exceeded`。

★ **本地那套叙事一条都不迁移**：本地 blocker 是
「`probe-service` + `no_candidate` 单请求探针会话」，**生产一行都不是那样**。
⇒ §9.216.5 的「排除覆盖缺口」**对本机成立、对生产不成立**。

生产同一刀切分：**6 会话整体未镜像 / 4 健康会话丢单 turn**（本地是 9/1），
且那 4 条**全部是成功请求**、会话**有 `session_dim` 行**
⇒ hook 跑过、turn 写没成。**那才是生产最该先查的一类。**

### ㉠ 生产 147 条 `dead` 死信（本地 0），但**不在增长**

- 全部 `source=hook / fail_reason=write_failed / attempts=9`；
- `session_id` **147/147 是 `sys:probe:*` 合成会话**，且只集中在 **2 个**
  （`cred35` ×135、`cred63` ×12）⇒ **锁护航**；
- 时间窗 created 09-23 09:11→16:46，updated 最晚 **09-24 01:37**，之后无新增
  ⇒ **是 09-23 的一次事件，不是稳态，也不是当前 `genuine_loss=10` 的成因**。

⚠ **`get next turn_no` 占死信 67%（98/147），与我 §9.215.4 的撤回冲突，但我不翻回去**：
① `acquire advisory lock` 是事务**第一条**语句、自己就超时 19 次 ⇒
预算被锁等待耗尽后，**下一条语句报的是「已过期」**；
**错误名只说明「预算耗尽时正在跑哪条」，不说明「哪条慢」**。
② 生产 `session_turns_hot` 每会话 **p50=p90=p99=1、max=8**，
**没有任何 `sys:probe:*` 会话在 turns 表里** ⇒ 那两个会话在 turns 表是空的，
`MAX(turn_no)` 对空集是瞬时的。

⇒ 被证据支持的是**锁护航**，不是 `MAX()`。
而这与 §9.216.5 在本地发现的排除缺口是**同一个洞的两端**：
hook 的探针门只挡「无会话头」的探针，**挡不住已合成 `sys:probe:*` 的那一支**。

### ㉡ 目标问题的分环境答案（**不合并**）

| 问题 | 本地 | 252 生产 |
|---|---|---|
| `request_logs` 能否 DROP | **不能** | **不能** |
| 丢行主体 | 探针单请求会话 | **成功请求 + `provider_error` 等** |
| 会话族数据可用性 | 可用 | **可用**：`session_turns` 817,736 行 / 2.7 GB ← ⚠ 原写「仅 1,733、比例失衡」是**分区父表**读数，已由 §9.220 更正 |
| 镜像失败恢复 | 生效（`dead=0`） | 生效（147 条走完 9 次重试后死信，符合设计） |

⚠ **最重要的一条**：生产 `request_logs` 与 `session_turns` **父表都是 0 行**，
只有 hot 面有量 ⇒ 动 DROP 前必须先回答
「生产的 v1 / turns 历史是否已被清空或轮转」——
**这正是「确保数据在更改前后一致」的核心，而本地查不到。**

### ㉢ 边界

- 只读观测，未改生产一字；生产的 10 行**成因未查**（无生产日志）。
- 「`session_turns` 仅 1,733 行」是**计数不是结论**：可能刚清空/轮转、
  可能 hot 面按设计就小、也可能镜像整体没起来。
  **没有分母也没有时间序列，不下判断。**
- §9.215–§9.218 的机制结论**只对本地成立**，本节给出了不迁移的证据。

---

## §70.57 撤回我自己的「生产历史可能已丢失」——那是**分区父表的天然 0**（§9.220）

### ㉣ 撤回

§9.219 把生产的 `request_logs` / `session_turns` 父表 `0 行 / 0 字节`
列为「最重要的一条新事实」，并据此提出「生产历史可能已被清空或轮转，
动 DROP 前必须先查」。

**那是假警报。** 两张表都是 **`relkind='p'` 的分区父表**
（`request_logs` 5 个子分区、`session_turns` 6 个），
**分区父表按设计就没有自己的存储**。

### ㉤ 真实读数：历史完好，且会话族已是 v1 的 10.8 倍

| 分区 | live | 体积 |
|---|---:|---:|
| `session_turns_2026_09` | **798,893** | **2,634 MB** |
| `session_turns_2026_10` | 18,843 | 73 MB |
| `request_logs_2026_10` | **71,360** | 189 MB |
| `request_logs_2026_09` | 4,307 | 14 MB |

⇒ **`session_turns` 817,736 行 / 2.7 GB vs `request_logs` 75,667 行 / 203 MB。**
累计 del 几乎为 0 ⇒ 历史没被动过。

★ 两条对退役决策直接有用：
① **会话族已是 v1 的 10.8 倍** ⇒「只用 `session_*`」在生产**已经成立**，
   不是待迁移的愿望；
② **两族历史起点差约 2 个月**（v1 无 2026_07 分区，turns 从 2026_07 就有）
   ⇒ **照本地写的「按时间对齐两族做差分」脚本，拿到生产会缺一整个月。**

### ㉥ §9.219 剩下的结论仍然成立

- 生产 `genuine_loss = 10` 及那 10 行形状 —— **仍成立**，而且**更强**：
  scope 查分区父表会扇出全部分区 ⇒ 那是**全历史**累计，不是 30 天。
- 生产 147 条 `dead` 死信、集中在 2 个 `sys:probe:*` 合成会话 —— 仍成立。
- 「生产的会话族几乎只有 `session_dim` 有量」—— **撤回**（turns 有 81.8 万行）。

⚠ **新发现的覆盖面问题**：生产 v1 总行数 81,799，**只有 8,078 带会话头**
⇒ **S4 门只覆盖约 10% 的 v1 流量**。
⇒ **「门绿了」与「v1 全量都进了会话族」不是同一件事。**

### ㉦ 本轮教训（与已记的那条同源、反向）

`pg_total_relation_size` 对分区父表返回 0，与「表被清空了」**结果完全同形**。
⚠ 阴险之处：**同一个对象在行查询里是全量的、在存储/统计 API 里是 0**
⇒ 光看那个 0 **永远分不出来**，只能看 `relkind` 与 `pg_inherits`。

⇒ **报「某表 0 行/0 字节」之前，先问「是量具错了，还是被测对象按设计就该是 0」。**
（与已记的「查错地方得到 0」是**同一规则的两个方向**，
症状不可区分，必须一起防。）

---

## §70.58 生产侧两个决定性事实：v1 是 2 个月滚动、列存缺陷已确证且修法未部署（§9.221）

### ㉧ ★ 留存策略：**v1 每月被 DROP，会话族从不被 DROP**

`/opt/scripts/pg17-drop-old-columnar-partitions.sh`（每月 1 号 02:30）：
`RETAIN_MONTHS=2`，对 12 个 parent DROP 掉 **≥2 个月**的月分区 ——
**`request_logs` 与 `request_logs_bodies` 都在列表里**，
而 **`session_turns` / `session_dim` 不在任何轮转列表里**。
另两个也会 DROP/TRUNCATE 的作业（每日 02:00 的空表清理、**每 15 分钟**的
emergency-cleanup），其父表白名单**同样不含 `session_turns`**。

⇒ **对目标问题的直接回答**：

1. **v1 的历史是设计上的 2 个月滚动** ⇒「确保数据在更改前后一致」
   **只能在 ≤2 个月的窗口上验证**；再往前 v1 是按设计删掉的，不是丢失。
2. **目标族完全不被轮转** ⇒ 这是「可以退役 v1」最硬的一条理由：
   **丢掉的是本来就会自己消失的东西。**
3. ⚠ 这**同时解释**了 §9.220.1 里「v1 没有 `2026_07` 分区」——
   **不是怪现象，是那个月度作业把它 DROP 了**。
   ⇒ 任何「按时间对齐两族做差分」的脚本都要**按 2 个月窗口设计**。

⚠ **一条潜在隐患**：`pg17-proactive-empty-table-cleanup.sh` 的**通用分支没有父表白名单**
（空表 + ≥100MB 就 DROP）。今天会话族空分区仅 ~152 kB，
**是靠体量侥幸躲过，不是靠设计挡住**。建议白名单补 `session_turns` / `session_dim`。

### ㉨ ★ 列存缺陷在生产**已确证**，修法**未部署**

| 分区 | 访问方法 | live | 体积 |
|---|---|---:|---:|
| `request_logs_bodies_2026_09` | **columnar** | 4,408 | 7,888 kB |
| `request_logs_bodies_2026_10` | **columnar** | 71,538 | 52 MB |
| `request_logs_bodies_2026_11` | heap | 0 | 8,192 B |
| `request_logs_bodies_hot` | heap | 5,988 | 398 MB |

⇒ **D30-b 答案：「生产已受影响」**。两种形状**各跑一个进程**实测：

| 形状 | 生产结果 |
|---|---|
| A 子查询包裹（原生产形状） | ❌ `invalid perminfoindex 0 in RTE with relid 0` |
| C 顶层 `UNION ALL`（§9.196.5 修法） | ✅ 返回 `1` |

⚠ **生产二进制不含修法**：`version.json` = `383ef8d0`，
而修法是 `e6193ea92` ⇒ **不是** `383ef8d0` 的祖先；
**是**本地容器 `57d69f9c` 的祖先；**已在 `origin/main`**。

⇒ **D19-a-3-1 的前置条件已在生产侧核实完毕**：修法在生产**可用**（C 形状实测通过），
只是**还没部署** ⇒ 部署它**低风险且已验证**。

⚠ **暴露是「潜在」不是「进行中」**：252 的 `cron.d` / `systemd` / `spool/cron`
**均未找到** `validate_sessions_v2` 的调度项。
⚠ 这是**「未找到」不是「没有」**——搜索仅限那几处与限深度 `find`。
**手动跑一次就会炸**，必须写进部署提醒。

### ㉩ 本轮的测量坑（差点报错结论）

第一次对比 A / C 时我**把结论读反了**，差点写成「A 通过、C 炸」。
根因：`psql` 的 `\echo` 走 stdout、报错走 stderr，`2>&1` 合并后**顺序不可靠**。

⇒ **判别动作**：要靠「读输出顺序」判定哪条语句成败时，
**必须一条语句一个进程**，不要靠同一次调用里的输出次序。
⇒ **合并 stdout/stderr 的批处理输出，不能用来判定「哪一条失败了」。**

### ㉪ 更新后的待办优先级

1. **部署 `e6193ea92` 到 252**（低风险、生产已验证）——建议尽快，
   赶在任何人手动跑 `validate_sessions_v2` 之前。
2. 给 `pg17-proactive-empty-table-cleanup.sh` 补会话族白名单（防未来误删）。
3. 生产的 4 条 success 丢单 turn 成因（无生产日志采集）。
4. S4 门口径：只覆盖 ~10% v1 流量（8,078 / 81,799 带会话头）。
5. §9.219 / D30 列存与函数是否生效 —— **本轮已答（是）**，
   剩下的只是「什么时候部署修法」。

---

## §70.59 校验器会对着它看不见的窗口报平——生产的 v1 实际窗口只有 4 天（§9.222）

### ㉫ 生产 v1 的**实际**窗口只有 4 天（策略说 2 个月）

只读实测 252（带会话头的 v1 行）：

| 面 | 最早 | 最晚 | 跨度 |
|---|---|---|---|
| `request_logs`（v1） | **2026-09-30 18:54:28** | 2026-10-04 15:53:32 | **4 天** |
| `session_turns` | **2026-09-06 11:03:50** | 2026-10-04 15:53:33 | **28 天** |

⇒ **`session_turns` 比 v1 早 24 天** ⇒ 存在 **24 天**区间
**只有会话数据、没有 v1 可对照**。
⇒ §70.58 的「2 个月」是**策略上限，不是可用数据量**；
**实际只有 4 天** ⇒ **把它当可用窗口写进任何脚本都是错的**。

★ **这就是门必须实测、不能比常量的理由**：2 个月会放过一个 24 天窗口，
而 v1 只装了其中 4 天。

### ㉬ 改了两个真缺陷（产品代码）

① **只给 `-start-date` ⇒ 静默查出 0 行**：`end` 保持零值时间（公元 1 年），
`ts < $3` 匹配不到任何行；工具打印「没找到已结算会话」，
把操作者指向**数据**，真因是**参数**。退出码靠 `minimumSessions=100` 兜住了，
**但归因是错的**。⇒ 修：`LoadSessionsInRange` 对零值 `endDate` 返回**指名错误**。
（只给 `-end-date` 仍合法 ⇒ 不拦。）

② **窗口被静默截断而报告读起来完全正常**：宽于实际数据的窗口
**既不报错也不返回空**，返回被截断的一段；**被截断的窗口照样可能加载 ≥100 个会话**，
于是 parity 报告与对真实窗口跑出来的**一模一样**。
⚠ 它**正是**那份会被用来论证「可以退役源表」的报告。
⇒ 修：新增 `LoadV1TimeRange` + `WindowExceedsV1Data`，
`validateBatch` 先打印两侧跨度、然后 **fail-closed** 并指名该缩窗口。

### ㉭ 判据配双向对照（6 例，用生产实测数字作夹具）

拦：start 早于最老行 / end 晚于最新行（start 在数据内，隔离 end 分支）。
不拦：窗口在数据内 / 只给 `-end-date` / 租户无 v1 行 /
**窗口恰好等于数据跨度**（off-by-one 边界，最常见的输入正落在这）。
**6/6 通过。**

⚠ **其中一例第一版是错的**：end 分支那个用例的 start 也落在数据之前，
于是 start 分支先命中、**end 分支从未被执行**。
**红的是夹具不是判据** —— 若没追那句 `does not mention "is after the newest v1 row"`，
就会把「end 分支已覆盖」当事实记下来。
**负向断言的信息量不亚于正向断言。**

### ㉮ 边界

- 改的是**产品代码**（`validate_sessions_v2/loader.go` + `main.go`），
  依据是「修正发现的问题」+「确保数据在更改前后一致」。
- **未对生产做任何写入**，**未在生产上跑这个工具**（会产真实负载，属主决定）。
- 门的「会响」是**用生产实测数字作夹具证明**的，
  **不是在生产上实跑触发的** —— 两者不是一回事。
- `session_turns` 早 24 天这件事**只说明那段区间无法用 v1 对账**，
  **不说明那段数据有问题** —— 会话族在 v1 缺失期的正确性**未验证**。

---

## §70.60 两个回填在生产从未跑过；退役 v1 会把可回填比例锁死在 1.2% / 2.9%（§9.223）

### 㭋 实测（252，只读）

**`is_final_success`（D32）**：

| 读数 | 值 |
|---|---:|
| v1 `is_final_success IS NOT NULL` | 75,851（**100%**） |
| v1 `is_final_success IS TRUE` | **10,006** |
| `session_turns` 总行数 | **817,140** |
| `session_turns.is_final_success IS NOT NULL` | **0** |
| v1 TRUE 声明能匹配到 turn | **10,006（100% 匹配）** |
| 其中已被标记 | **0** |

**`client_protocol`（R43）**：v1 31,495 非空 → 能匹配 **23,617** →
`session_turns` 侧 **0** 行非空 ⇒ 可填 **2.890%**。

⇒ **两个脚本在生产都从未执行过**（目标侧 100% NULL，源侧填好且 100% 可匹配）。
⇒ 跑它们能覆盖 **1.225% / 2.890%**；
⇒ 其余 98%+ **永远补不上**——因为那 793,452 行的 **v1 源已不存在**
（v1 实际只跨 4 天，turns 跨 28 天）。

### 㭌 对退役决策的含义（**本节唯一要属主拍板的事**）

两个脚本头注释都写着 **「run it before `request_logs` is dropped, not after」**。

- **先 DROP 再跑** ⇒ 10,006 条 final-success 声明 + 23,617 条 client_protocol
  **永久丢失**（源没了；脚本是幂等一次性修复，不是能从别处重建的推导）。
- **先跑** ⇒ 仍只覆盖 1.2% / 2.9%。
- ⇒ **「确保数据在更改前后一致」在生产上的真实上限就是这两个比例。**
  剩下 98%+ 的 turn 在 v1 缺失期里**既不能回填，也不能校验**。

⚠ **现在就存在的用户可见后果**（脚本头注释自述）：
`admin/session_online.go` 已于 2026-09-30 把会话时间线切到会话族，
并从 `is_final_success` 派生 `final_success` / `superseded_success`
⇒ **生产上 817,140 行 turn 全部渲染成普通 `success`**。
⚠ **但这句话的限度**：那是从「该列 100% NULL」推出的**代码路径必然**，
**我没有在生产 admin 页面实际看过渲染结果**。
**它不是已观测到的用户投诉。**

★ **「早跑」真正的理由**：v1 若继续接收新的 TRUE 声明，这个比例会**随时间上升**
⇒ **先跑回填、先 DROP v1** 的顺序价值**随时间递减**。

### 㭍 边界

- **未在生产执行任何回填 / 未跑校验器** —— 那是对生产的写入，**属主决定**。
- 「从未执行过」依据是**目标侧 0 行非 NULL**，属**间接（结果侧）证据**；
  实测为 0 所以依据成立，但**不是**「我确认过没人跑过脚本」。
- 本轮**无代码变更**（上一轮的校验器改动已推 `fe5003034`）。

---

## §70.61 「先跑回填再 DROP」只存在于一句 SQL 注释——补上一道**故意红**的门（§9.224）

### 㭎 已有判据**拦不住**（它是注册表一致性检查）

`db/session_family_column_availability_test.go` 早就把两条列报进
`GO EMPTY ON THE SESSION SIDE`（D32 备注：「一直亮着没人去读它指向哪里」）。
但它断言的是 `assertSameSet(t, "unservable", goEmpty, RetirementUnservableColumns)`
⇒ **列变空它照样通过**。
「现在能不能 DROP 源表」是另一个问题，此前**在仓库里没有家**。

⚠ 与已记的那条同源：**只报不拦的信号，和没有这个信号，在决策链上等价。**

### 㭏 新门（`db/retirement_backfill_gate_test.go`）

- 纯谓词 `BackfillBlocksRetirement(...)`（不依赖库，可测两向）
- 真库应用 `TestRetirementBlockedByUnrunBackfills`
- 报错**指名可执行命令**（`Run sql/scripts/backfill_final_success_marks.sql before retiring v1`）

本机真库实跑**确实响了**（不是装饰）：

```
request_logs still present: true   session_turns rows: 1690372
is_final_success         0.0000%  (0/1690372 rows)
client_protocol          0.0000%  (0/1690372 rows)
```

### 㭐 双向对照 7 例（7/7）

拦：未跑 + 源表还在（两条列各一例）、阈值下侧（0.00049%）。
不拦：**已跑但只覆盖 1.2%**、**源表已退役**、会话族 0 行、阈值上侧（0.00502%）。

★ **两条关键阴性对照**：「已跑但只覆盖 1.2%」**必须放过**，
否则它永远清不掉、训练所有人忽略；
「源表已退役」**必须放过**，否则它在最该拦的时刻之前就无解。
⇒ 与「一条永不可能绿的门，红着红着就被无视，那比没有更糟」一致。

⚠ 阈值 `0.005` 与 §9.209 同源：**分类精度 == 显示精度**，
否则「4 行 = 0.00049%」会同时读成「0.00%」与「没空」。

### 㭑 `db` 包基线从 FAIL 1 变 FAIL 2（**不是回归**）

| 红 | 性质 |
|---|---|
| `TestRepointValueFidelity` | 既有，§9.210 刻意留红 |
| **`TestRetirementBlockedByUnrunBackfills`** | **新增，刻意红** |

⇒ 它陈述一个**尚未满足的前置条件**，不是靠改代码能消掉的失败。
⇒ 要转绿只有一条路：**在 DROP 源表之前把两个回填跑掉**（属主决定）。

### 㭒 边界

- 门**只在 `TEST_DATABASE_URL` 存在时运行**，否则 `t.Skip`
  ⇒ **全绿具有误导性**；本轮实跑已确认它响了。
- `work_type` **刻意不在**列表里：v1 侧自身只有 1.93%，
  拷贝只动 ~2% 行，且该列已在注册表里标为 unservable
  ⇒ 列入会产生**没人能行动的红**。
- 门**只管本地/测试库**，**不会**在 252 上自动运行；
  生产的同一判断目前只存在于本文档的只读实测里。

---

## §70.62 `s4_ready` 变绿**不等于**可以退役——第二个阻塞项已贴到 S4 门的结论行（§9.225）

### 㭓 陷阱的形状

> 修好镜像 → 看着 S4 门变绿 → DROP v1 → **什么红都没有，列值永久丢失。**

§70.61 那道硬门只在测试库跑，而操作者真正会看的是 **S4 那份报告** ——
它原本**只字未提**第二个阻塞项。

### 㭔 补法：只报告，不决策

`cmd/gateway/s4_gate_measurement_test.go` 总结块现在紧接在
`ONGOING / RECENT / historical / clean` 之后打印第二项前置条件：

```
RETIREMENT PREREQUISITE **NOT MET**: 2 of 2 column(s) are still empty on the session side
(is_final_success, client_protocol, of 1690576 session_turns) and are fillable **only** from v1,
by a one-shot idempotent script. s4_ready=false above says nothing about them: it measures drift,
not copies. Dropping request_logs now makes these permanently unfillable.
  ⇒ s4_ready is necessary but NOT sufficient. Read it together with the line above.
```

⚠ **为什么只报告**：§70.52 已把这条门定为**报告**
（"S4 能不能开"是发布决定）。⇒ **报告负责「别把 s4_ready 单独读」，
硬门负责「拦住顺序错误」**，分工不混。

★ **措辞里嵌了真实的 `s4_ready` 值**，所以等镜像修好、它变成 `true` 时，
同一句话自动变成「**`s4_ready=true` above says nothing about them**」——
**那正是最危险的那一读法，而它不需要改代码就会自己出现。**

⚠ 第一版我用 `month.genuine`（30d），而这条门自述**定义窗口是 7d**
⇒ 两行引用**不同的 readiness**，**恰恰制造了它本该消除的割裂**。
已改为 `def.genuine == 0` 并在注释里写明理由。

### 㭕 两个门现在的分工

| 门 | 形态 | 回答 | 跑在哪 |
|---|---|---|---|
| `TestS4GateMeasurement` | **报告** | 镜像有没有在丢行（drift） | 本地/测试库 |
| `TestRetirementBlockedByUnrunBackfills` | **硬门（故意红）** | 能不能 DROP 源表（copies） | 本地/测试库 |
| §70.60 的只读实测 | 文档 | 生产的同一判断 | 252 |

⚠ **三者都不含 252 的自动执行** —— 生产的 S4 与回填状态
**目前只能靠人工只读复测**。已知缺口。

### 㭖 边界

- 本轮改的是**测试的报告内容**，**不改变任何产品行为**。
- 该块**不做决策、不影响退出码**，只让 `s4_ready` 不能被单独读。
- 本机真库实跑确认打印了 `NOT MET`（`session_turns` 1,690,576 行、两条列均低于阈值）
  —— **不是只在纸面上存在**。

---

## §70.63 全面性有三层，每层各有一个洞——本轮把三层都验了（§9.226）

### 结论先行

- **我上一轮把 exposure 门记成「只数不断言」是错的。** 它有具名登记表 +
  双向门，既数也判。那句英文是**上游** `requestLogsReadInventory` 的自述。
  改正记录在审计 §9.226 开头。
- **而且 `origin/main` 上 `TestRequestLogsReadInventoryIsComplete` 此刻就是红的**，
  是 **§9.222（`fe5003034`）我自己弄红的**（`loader.go` 4→5）。
  §9.222 那轮我核了 `cmd/gateway`、`validate_sessions_v2`、`db`，
  **没把 `./admin/` 当包跑**。⇒ **交接里写的「门状态」只列我跑过的那几个，
  读起来却像全仓。本轮起 `admin` 进固定核验清单。**
- 本轮找到并修掉 3 个「全面性」漏洞，**都是量具的洞，不是产品的洞**。

### 三个洞

| # | 洞 | 后果 | 状态 |
|---|---|---|---|
| 1 | inventory 模式 `from` 独占 | **3 个活着的 API 读方对门完全不可见**；其中 2 个是会话导出/对比仍挂在 v1 上的**唯一原因** | 已修（扩 `from\|join`，106/240 → 109/271） |
| 2 | `sql_source_indirection_audit` 把子查询取第一个 token | `admin/tenants.go` 读**两张 v1 底表**却被输出成「**退役安全**」 | 已修（4 处移出，18 → 5） |
| 3 | bodies 列不在 canonical 合同里 | 26 文件 / 44 调用点的 bodies 依赖，exposure 门读成 **`clean`** | 已加**故意红**的门 |

洞 2 与 §9.45 记的「把『不知道』报成安全」**不是同一件事**：
那一类不可判定被当成安全、输出里还留着「不可静态解析」的标签；
这一类是**解析成功了却解析错了**，错得**自信**，输出里没有任何视觉信号。
**只写「不许把未知报成安全」的门挡不住它。**

洞 3 的失效形态值得单独记：`admin/session_export.go:225` 写的是
`COALESCE(rb.request_body, '{}'::jsonb)`，所以 DROP 之后
**不报错、不返回空、照常导出一个完整会话包**，只是每条正文都是 `{}`。

### 判据自证：三道变异，两道是我自己写的**恒真门**（已写进审计，此处只留一句）

先写门、再做变异验证，是这一轮唯一没有被跳过的步骤。结果：
变异 A（删子查询扫描）✅ 红、变异 C（去 `_test.go` 过滤）✅ 红、
变异 D（不剥引号）首轮 ❌ 绿、变异 B（不剥 SQL 行注释）❌ 绿。

- **B 绿暴露的是我的错**：我为「剥 SQL 行注释」配的门，夹具写成
  `-- 历史实现读 request_logs，已改`。逐字 diff 全仓输出：
  **剥与不剥完全相同** —— 仓库里唯一挂在解析结果上的注释里没有
  `from`/`join` 加标识符。⇒ **那道门在测一个不存在的场景，已删**，
  只留那一行归一化并注明「全仓实测零影响」。
- **D 首轮绿**是因为我把 `"request_logs_hot"` 放进了「期望 false」那张表
  （它期望 true），且夹具走的路径不经过被测代码。补对照组后转红。

**两条新方法论**：
1. **「判据红了」不等于「判据在拦」。** bodies 门若被收窄回 `from`，
   26 个文件变 23 个，**门照样红**。⇒ 另配一条不依赖红绿的判据
   （`TestV1BodiesScanSeesJoinOnlyReaders`：总体里必须存在只靠 JOIN
   才被看见的读方）。**同一个坑在两个门上各踩了一次。**
2. **夹具必须能失败**——先问「它在变异下真的会红吗」，而不是「它看起来在测什么」。

### 未闭合项（本轮明确没做，不要当成已完成）

- ★ **`AuditRepo` 至今没有被任何门跑过全仓。** `resolve_test.go` 只对临时夹具调它，
  全仓无第二个调用点。**本轮修了它算错，没建那道门**——建门需要一份
  「哪些间接读点已评估」的登记，工作量与本轮不成比例。**这是本轮最大缺口。**
- `autoroute/metrics.go:382` 仍有一条英文散文落进「退役安全」桶（方向安全，不影响退役判断）。
- `admin/usage_enhanced.go:120` 的裸表名字符串 `BaseTable: "request_logs_with_current_month rl"`
  仍是 inventory 的已知盲区（扩到「裸表名」会把列注册表/配置键/分区管理一并扫进来，
  §8.5 明令要先评估）。原注释已登记，本轮未动。
- **bodies 侧没有 SQL helper**：turns 侧有
  `SessionFamilyTurnsSourceSQL` / `SessionFamilyTurnsForSessionSQL`，
  bodies 侧没有。⇒ 这是 bodies 退役的第一块砖，已写进新门的错误信息。

### 下一轮第一件事

**全量跑 `admin` 包**（本轮只跑了受影响的 6 道 + 新的 3 道）。
在 §9.226.1 已确认「漏了一整个没跑的包」这个失效模式之后，
再写一句「门状态」而不注明覆盖范围，就是同型复发。

---

## §70.64 间接读点审计成了门，它叫出了 4 个**计费路径**读方（§9.227）

### 结论先行

- ★ **`maas/usage.go` / `maas/consumption_detail.go` / `maas/credit_buckets.go` /
  `admin/usage_credits.go` 读 v1，而它们在四张登记表里一处都没有。**
  默认支就是 v1 底表（`ClampUsageDays` 对 `days<1` 返回 1）。
- ★ **`maas/credit_buckets.go` 与其它几条不同类**：它 `ON CONFLICT DO UPDATE`
  **写**小时级 credit 桶 ⇒ DROP 后已有小时桶被**覆盖成 0**，**不可逆**。
- ★ `admin/tenants.go` 与 `maas/usage.go` 叠加时，两条路径都把「读不到」
  变成 0 ⇒ **「这个租户从没调用过」会被当成事实**。这是本会话至今
  最接近「静默财务错误」的一条。

### 为什么四道门全都看不见它（两道各自的原因都实测过）

1. `requestLogsReadInventory` 按行扫 `from request_logs`。`maas/usage.go` 里唯一的
   这串文本在**第 27 行的注释里**（`// … aggregates … from request_logs.`），
   注释被剔除 ⇒ 计数 0 ⇒ 不进总体。
2. exposure 门按 AST 抽 **SQL 字面量里的 canonical 合同列**。这里关系名是
   **Go 变量**（`FROM ` + logsTable），字面量里一个 v1 关系名都没有 ⇒ 零证据。

机制是切换层（`maas/usage.go:66` / `admin/usage_credits.go:120`）：

```go
func requestLogsSource(days int) (string, string) {
	if days <= 7 { return "request_logs_hot AS r", "r" }          // ← 默认支，v1 底表
	return "request_logs_with_current_month AS r", "r"
}
```

### 门的设计要点

不是「把工具输出冻结成表」——那正是 §9.37 记的静默腐烂路径。
清单由人读源码写，与工具分类**双向交叉核对**（8 条变异全部转红）。
其中最关键的一条：

> ★ **把 §9.226.3 的分类器修法撤掉 ⇒ 门转红。**
> 单向抄表的话，撤掉修法会让清单跟着改成 canonical-only ⇒ **两边一起绿**，
> 而 `admin/tenants.go` 会重新变成「读两张 v1 底表却报退役安全」。

另加一道 ratchet：`still-unknown` 必须为空 ⇒「先填上以后再说」从结构上堵死。
判定结果：**26 条 / 实测 26 文件，still-unknown = 0**
（工具判 unresolved 的 16 文件 / 34 处全部已由人读源码定级，跨包调用为主）。

### 顺带修掉工具自己的一个假阳性

`"balance_usd IS DISTINCT FROM " + balArg` 被 `fragmentTailRE` 当成 FROM 子句
（它分不清 `LEFT JOIN ` + 表 与 `IS DISTINCT FROM ` + 值）。全仓 35→34 处、27→26 文件。

⚠ **这一版我第一版写反了，而且是静默失效**：先按长度切尾部关键词的话，
`"… FROM "` 的末 4 个字符是 `"ROM "` 而不是 `"FROM"` ⇒ 等值比较永不成立
⇒ 过滤从未生效。**变异验证时才发现。**
⇒ 一个恒假的过滤器看起来和一个正确的过滤器一模一样。

### 边界

- 无产品行为改动（审计工具，不在请求路径上）；零生产写入，未连 252。
- 15 条 `nonv1-by-inspection` 的依据是**读源码**，依据本身写进 `Via` 字段可复核。
- ⚠ `autoroute/metrics.go` 那条**仍不是读点**（Prometheus Help 文本里恰好有 `from `）。
  本轮修了同族的 `IS DISTINCT FROM`，**没修这一条**——需要「拼接里有没有关系名形状」
  的判据，是另一个更大的改动。

### 下一轮第一件事（已更新）

1. ★ **`maas/credit_buckets.go` 的桶覆盖不可逆**——它是本清单里唯一
   「DROP 会**改写已有数据**」的一条，应与回填顺序放在一起排期。
2. `SessionFamilyBodiesSourceSQL()`（bodies 侧至今没有这个 helper，§9.226.4）。
3. 给 `AuditRepo` 的 `canonical-only` 里那 2 个「视图 v1 臂」条目
   （`admin/logs.go` / `admin/usage_enhanced.go`）并入 D29-d 切换清单的复核。
4. `autoroute/metrics.go` 的 Help 文本假阳性（见上）。

---

## §70.65 bodies 切换的**数据级**判据：两表部分不相交，收益上界 22 / 损失上界 1,365（§9.228）

### 结论先行

- ★ **`session_bodies` 现在**不能**替代 `request_logs_bodies`。缺口 **29,691** 条。
- ⚠ **「session 覆盖 95.26%」是错误读法**：两表**不是包含关系，是部分不相交**。
  `session_bodies` 命中**更多**（770,209 > 742,472），但切换是**换了一批消息**：
  丢 29,691 条、另 27,737 条从空变有。
- ⚠ **会话级尾部才是决定性的**：11,783 个会话（1.59%）会至少丢一条，
  而**单会话最多丢 1,365 条、最多只「得救」22 条**。
  ⇒ **收益上界 22，损失上界 1,365。** 无灰度直切风险与收益完全不成比例。
- ★ **不是机械替换**：v1 是 `request_body`/`response_body`，
  会话侧是 **`request_delta`/`response_delta`**；且 db 包**没有** bodies 源 helper。

### 新增门（`db/bodies_cutover_gap_test.go`，**故意红**）

判据只有一条：**`v1 有而 session_bodies 没有` 必须为 0**。
命中率与百分比**都不作为判据**——§9.228.1 已证明它们会导出错误结论。

与 admin 侧那道 bodies 门（§9.226.4）**不是重复**：

| | 问 | 何时可能变绿 |
|---|---|---|
| `admin/request_logs_bodies_retirement_gate_test.go` | 谁读过、评估登记了吗（**流程**） | 永远不会先变绿（26 文件未登记） |
| `db/bodies_cutover_gap_test.go` | 切过去会不会丢正文（**数据**） | 回填补齐后**可能**变绿 |

### 判据自证：这条门第一版有个洞，是变异验证逼出来的

M1/M2/M3/M4 四种削弱**全部让门变绿** ⇒ 判据、阈值、SQL 口径、纯函数都在承重。
M5（不设 `TEST_DATABASE_URL`）**SKIP 而非绿** ✓。

★ **M6′ 最有价值**：我把总体改成 0 行去找漏洞，发现第一版只有
`WouldBeLost > 0` 一条判据 ⇒ **总体被改窄会让门变绿**，
而它一次都没量过东西。真库门最容易被改坏的就是总体口径。
已加 `V1Turns == 0 ⇒ 阻塞`（排在缺口判据**之前**），M7 验证转红。

⚠ **M6′ 第一版是无效演示**：我写出了双 `WHERE`，0.01s 就失败。
**运行时 0.01s 是「SQL 语法错」而不是「量到 0」的信号。**

### 边界

- 数字**只对本地库成立**；生产 bodies 覆盖与回填状态**未知**且无自动门。
  据此排生产切换前需在 252 只读复测。
- 无产品行为改动；只读 SELECT；未连 252。

### 下一轮第一件事（已更新）

1. ★ `maas/credit_buckets.go` 的桶覆盖**不可逆**（§70.64），与回填顺序一起排期。
2. ★ bodies 三件事按 D33 顺序：回填 → 门转绿 → 补 `SessionFamilyBodiesSourceSQL()`（带列映射）。
3. 在 **252 上只读复测** bodies 缺口（本地数字不能外推）。
4. `admin/logs.go` / `admin/usage_enhanced.go` 两个「视图 v1 臂」并入 D29-d 复核。
5. `autoroute/metrics.go` 的 Help 文本假阳性。

---

## §70.66 ★撤回 §70.65 / §9.228 的全部 bodies 数字——**总体选错了**（§9.229）

### 结论先行（这一轮把上一轮的结论推翻了）

- ★ **本地：bodies 切换一条都不丢（`would_be_lost = 0`）**，不是 29,691。
  `session_bodies` 覆盖 `session_turns` 的 **100.00%**。
- ★ **生产 252：缺口 1,113 条，且全部落在 2026-09-30 一天**；
  10-01→10-04 逐日为 **0**。695 个会话（0.38%），单会话最多 152。
  ⇒ 是**一个回填边界日**，不是写入路径缺陷。处置从「查路径」缩到「补一天」。
- ⚠ **那 29,691 到底是什么**：96% 是 `is_auto_request = t` 的探针/自动流量，
  **按设计**不进会话族（§9.215）。它们**切不切 bodies 都从未被导出/对比 API 看到**，
  所以**不是切换损失**。
- ⚠ 退役 v1 的**真实代价**不是数据不一致，而是**失去探针/自动流量的可审计性**。
  那是**属主决定**，且**至今没有任何门覆盖它**。

### 错在哪一维（可复用的那一条）

`admin/session_export.go:227` 的 FROM 是 `dbpkg.SessionFamilyTurnsForSessionSQL()`。
⇒ 「切过去会不会丢正文」的总体**必须是 `session_turns`**。

而 §9.228 按「**哪张表要退役**」选了总体（`request_logs`），
不是按「**被影响的那段 SQL 的 FROM 读哪张表**」。

⇒ 于是把「**镜像没捕获的 turn**」量成了「**切换会丢的正文**」。
两个量都真实，但问的不是同一件事，混成一个数就等于
**把一个设计决定报成了数据缺陷**，并据此给出了一条**过度的处置建议**。

### 门的状态变化：从「永远红」变成「真守卫」

`db/bodies_cutover_gap_test.go` 已换成正确总体（`session_turns`），
并新增：
- `v1OnlyTurnsSQL` —— 明确标注**不是切换损失**的另一个量（本地 38,348，
  其中 36,717 = 95.7% 是 auto）
- `lossByDaySQL` —— 把「系统性缺口」与「一个回填边界日」分开
  （两者的处置完全不同：查路径 vs 补一天）

**本地现在绿了。** 它不再只是提醒人的判据，而是真的回归守卫：
会话侧 bodies 写入路径一旦被改坏，它会红。生产上它会红（1,113），那正是它该说的话。

### 生产只读复测是这一轮的关键动作

所有修正都来自 252 上三条 SELECT（**零写入**）：
逐日分布（证明是边界日）、`session_turns` 侧 100% 覆盖（证明写入路径没问题）、
`is_auto` 拆分（证明缺口是探针流量）。
⇒ **本地数字本来会把我引向错误的结论。**

### 仍然未闭合

- ⚠ **生产无自动门**（§9.223 同一缺口），只能人工只读复测。
- ⚠ `is_auto_request = t` 的可审计性**没有门**。
- 列名不同不变（`request_body`/`response_body` vs `request_delta`/`response_delta`），
  db 包**仍没有** bodies 源 helper ⇒ 那是**改读方**时的事，与数据够不够无关。

> ⚠⚠ **就地更正（2026-10-05，§9.231）**：本条在写下时为真，但它**误导了后续动作** ——
> 补上 `db.SessionFamilyBodiesSourceSQL()` 之后，很容易以为 25 个 bodies 读方
> 可以一次性替换。**实测不是**：它们分 6 档，那个 helper **只 fit 其中 13 个**；
> 另有 4 个用到 bodies 的 `ts`（helper 故意不投影）、2 个是刻意的 hot→view 延迟分层、
> 3 个只读体量、**2 个工具读 v1 就是其职责且 v1 退役后无处可去**。
> ⇒ 「补 helper」只是 6 件事里的第 1 件。见 §9.231 / §70.68。

### 下一轮第一件事（已更新）

1. ★ **生产只需补 2026-09-30 一天**（1,113 条）—— **需属主批准**（属生产数据变更）。
2. ★ 补 `SessionFamilyBodiesSourceSQL()`（带显式列映射），然后才谈改 bodies 腿。
3. ★ **`is_auto_request = t` 的可审计性**要不要保留 ⇒ **属主决定**，需先想清楚
   「自动流量是否需要可审计记录」，再决定是加门还是记档。
4. `maas/credit_buckets.go` 桶覆盖不可逆（§70.64）。
5. `admin/logs.go` / `admin/usage_enhanced.go` 两个「视图 v1 臂」并入 D29-d 复核。
6. `autoroute/metrics.go` 的 Help 文本假阳性。

---

## §70.67 bodies 读端灰度开关已就位（默认关），以及它造出并已修掉的假绿

### 做了什么

- `db.SessionFamilyBodiesSourceSQL()`：会话族 bodies 源（hot ∪ parent），
  在**这一层**把 `request_delta`/`response_delta` 映射成 v1 的
  `request_body`/`response_body`，调用点投影一个字不用改。
- `admin/sessionBodiesFromSQL()` + `storage.admin_session_bodies_native_read`
  （spec 登记，`Default: false`），消费点 `admin/session_export.go:227`、
  `admin/session_compare.go:903`。
- 4 道新门：db 侧列名/类型对等（真库）、db 侧真实 JOIN 不放大（真库）、
  admin 侧默认关闭、admin 侧两个消费点都走开关。
- 1 道自证门：`TestV1BodiesScanIncludesRegisteredIndirectReaders`。

### ★ 本轮真正值得记的一条

**把一个 v1 读方改成间接读法，会让它从「按字面量扫」的几道门里静默消失。**
本轮实测四道门的后果，其中一道最险：

| 门 | 后果 | 有没有信号 |
|---|---|---|
| `TestRequestLogsReadInventoryIsComplete` | 109/271 → **107/269** | 红（抓到了） |
| `TestV1BodiesReadersAreAssessed` | 26/44 → **24/42** | **仍红，本来就故意红** |
| exposure（`measureV1ReadingLiterals`） | 两个文件不再被评估 | **无** |
| indirection audit | unresolved 34 → **36** 处 | 绿（文件早就在清单里） |

第 2 行是重点：总体少了两个**最要紧**的读方，而退出码**一模一样**。
M8 变异（删掉修复）实测确认：故意红的那道门**照样红**。
⇒ 「总体静缩」在现有门矩阵里完全没有信号，必须自己写一条判据守它，
且判据必须是**集合关系**（登记的间接 bodies 读方必须逐个出现在实测总体里），
**不能**钉死「总体数 ≥ 25」这种会漂的数字。

处置没有新造机制，走的是本仓既有两张表的分工
（与 `bg/auto_route_settle_sql.go` 同形）：字面量读方归
`requestLogsReadInventory`，间接读方归 `indirectRequestLogsReaders`。

### 变异验证

**10 条全部转红**（M1–M9 + M11，见审计 §9.230.5 表），M10 阴性对照确认并入计数确为 1。

★ 第一轮有 3 条「没被抓住」，**逐条查下来没有一条是门的问题**：
两条是 perl 锚点没匹配上（变异根本没写进去），
一条是我把 `ResolvesTo` 从 `request_logs_bodies` 改成 `request_logs`
——那仍是 `v1DirectTables` 里的合法表，门判绿是对的。
**门绿时先确认变异改到了东西、路径真的经过。**

### ★ 同一个坑在下一层又踩了一次（exposure 门）

我第一次改 exposure 总体时**只改了报告循环、忘了测量循环**，
然后跑反向变异（把总体退回字面量表）——**没有任何一道门变红**。
viewArm 桶没有登记表、报告内容也没有门在核。
⇒ 那个「修复」**什么都没改变**，只是看起来像修了。

实测确认量具本身没问题：`extractV1ReadingLiterals` 对
`admin/session_bodies_source.go` 能产出 1 个字面量、判为 `viewArm`；
而 `admin/session_export.go` 现在产出 0 个。**是总体选错了，不是量具不行。**

处置：总体提成单一 SSOT `retirementExposurePopulation()` + 集合相等判据，
反向变异 M11 转红并指名两个文件。
★ 顺带补上一个**此前就存在**的缺口：`bg/auto_route_settle_sql.go`
从 §9.43 起就是间接读法，却**从来不在 exposure 总体里**。

### 顺带修掉的一处自欺

`db/session_bodies_source_test.go` 的 v1 侧对等检查只查了 `request_body` 一列，
注释却写「必须同形」；调用点实际用三列（`request_body`/`response_body`/`request_id`）。
已改为三列逐列比类型。

### 门结果

见本节末尾「本轮全量门结果」。

### 状态：**开关是关的，行为零变化**

- 默认臂字符串与改造前**逐字相同**，两个读方的 SQL 一字未改。
- `request_logs` **仍然不能 DROP**（本地与生产都不行）：
  `maas/*` + `admin/usage_credits.go` 默认读 `request_logs_hot`（§70.64），
  `is_final_success` / `client_protocol` 两个回填未跑。
- 生产 2026-09-30 那 1,113 条**仍未补**——属主未批准，本会话**零写入**。

### 下一轮第一件事

1. ★ **先跑 `is_final_success` / `client_protocol` 两个回填再谈 DROP v1**
   （后跑则 33,623 条永久丢失）——需属主批准。
2. ★ 生产补 2026-09-30 一天的 bodies（1,113 条）——需属主批准；
   补完之后 `storage.admin_session_bodies_native_read` 才可以灰度开。
3. ★ `is_auto_request = t` 的探针/自动流量要不要保留可审计记录（属主决定，**至今无门**）。
4. 部署 `e6193ea92` 到 252；给 `pg17-proactive-empty-table-cleanup.sh` 补会话族白名单。
5. S4 门口径与开启时点；D32 + D29-d 切换时点。
6. `session_bodies` 父表 57 行重复 `request_id`（37 个 id）今天不发作，
   开关**打开前**应先处置（源里 `DISTINCT ON` 或加唯一约束）。

### ★ 跑门时的一条操作隐患（实测，本轮踩到）

本地 `llm-gateway-pg` 里会留下**孤儿 backend**：某些 admin 真库测试发的
`WITH agg AS (… FROM request_logs WHERE ts >= now() - interval '30 days' …)`
聚合查询，在**测试进程已经退出之后**仍然继续跑（本轮实测跑了 **26 分钟**和
**20 分钟**），把之后每一轮门验证拖慢一个数量级（admin 全量 599s → 706s →
部分子集直接顶到超时）。

症状：Go 侧进程 CPU 几乎是 0（`0:00.11`）却迟迟不出结果，
`pg_stat_activity` 里能看到几条 `state=active` 的长查询，其中后面的几条
`wait_event_type=Lock`（`LWLock`）—— 是在**排队等前一条**，不是各自慢。

处置（**只动本地库，未碰生产**）：
`SELECT pg_terminate_backend(<pid>);` 清掉，活动查询归零后耗时立刻回到正常。

⚠ 我**没能**定位到是哪个测试发的（按 `AS pt FROM request_logs` 反查无命中，
那两个 SQL 形态像是运行时拼出来的）。**下次再遇到先查 `pg_stat_activity`
而不是先怀疑代码变慢了** —— 那两次 26 分钟里我一度以为是自己改动引入了 hang。

---

## §70.68 bodies 读方形状分类：25 个读方不是一个动作（订正 §70.67 的隐含前提）

### 结论先行

§70.67 写「补了 helper 之后才谈改 bodies 腿」——**那句话隐含了「25 个读方是同一次替换」，
实测不是**。逐个读真实 SQL 之后分 6 档，helper 只 fit 其中一档（13 个）：

| 档位 | 数量 | 能不能换 |
|---|---|---|
| A 单键 JOIN + 列在合同内 | **13** | 能 |
| B 用到 bodies 的 `ts` | **4** | **不能**（helper 故意不投影 ts） |
| C 两次顺序查询（延迟分层） | **2** | **不该换**（会毁掉性能设计） |
| D 只读体量 | **3** | 不是同一件事 |
| E 读 v1 就是其职责 | **2** | **无处可去** |
| F 已建开关 | **1** | §9.230 已处理 |

### ★ 最要紧的一条：两个工具在 v1 退役后无处可去

- `cmd/tools/backfill_session_bodies` 从 v1 读、往 session_bodies 填 ⇒ 换源=自毁；
- `cmd/tools/validate_sessions_v2` 的职责是 v1↔session 对拍 ⇒ 换源=对着自己校验自己。

⇒ **回填「完成」的定义必须包含「这两个工具已被处置」**（随 v1 退役，或重新设计）。
此前没有人写下来过，而它决定回填什么时候算做完。

### 我的两个错，都是门抓到的

1. 正则分类器假设 bodies 别名是 `rb` ⇒ `cmd/compression-bench/main.go` 用的是 `b`，
   一个 ts 等值 JOIN 被判成「可换」。**结论是读源码读出来的，不是分类器算出来的。**
2. 登记表里把 `admin/session_bodies_batch.go` 归成可迁移（按名字最 trivial），
   门报它**用到了 `rb.ts`**（`WHERE (rb.request_id, rb.ts) IN (unnest(...))`）⇒ 整个文件不能换。
   第一版的 ts 判据只测 `ON rb.ts = rl.ts`，**漏了 WHERE 元组里的 ts**。

### 变异脚本自己也出过一次错

第一版 7 条变异**全部报「仍绿」**：`run` 只 `grep '^--- FAIL'`，
而 `go test` **编译失败只打 `FAIL <pkg>`、不打 `--- FAIL`**
⇒ 6 条「仍绿」其实是编译失败被读成了通过。已修。
（详见审计 §9.231.5。）

### 下一轮第一件事（更新）

1. ★ **先跑 `is_final_success` / `client_protocol` 两个回填**（需批准）。
2. ★ 生产补 2026-09-30 bodies（1,113 条）——需批准。
3. ★ **定义「回填完成」**：含 `backfill_session_bodies` 与 `validate_sessions_v2` 的处置。
4. A 类 13 个换源（开关默认关，逐个带门）。
5. B 类 4 个先定口径（ts 语义 / 指标定义），不是 repoint。
6. C 类 2 个重做延迟分层，**必须带前后延迟读数**。
7. `is_auto_request = t` 的可审计性要不要保留（属主决定，至今无门）。
8. 部署 `e6193ea92`；`pg17-proactive-empty-table-cleanup.sh` 补会话族白名单。

---

## §70.69 A 类 7 个读方已迁（开关默认关）；并修掉 §70.67 那个**不完整**的修复

### 做了什么

- **消费者识别机制**：`indirectReader.SwitchFunc` + `indirectSourceConsumers`，
  并入三处总体（退役清单证据 / bodies 门总体 / 族分类器 `sourceFamilyOf`）。
- **迁移 7 个文件 / 11 处** bodies 腿走 `sessionBodiesFromSQL()`。
- 2 道新门 + Evidence 门两跳校验。
- 一处 `const` → `var`（函数调用不能出现在 const 声明里）。

### ★ §70.67 的修复是不完整的（本轮实测发现）

把机制建好、还没迁任何文件时，bodies 总体从 **25 变成 27** ——
多的正是 `admin/session_compare.go` 与 `admin/session_export.go`。

⇒ §70.67 只把**切换层自己**加回了总体，**没加它的两个消费点** ⇒
**全表最要紧的两个 v1 bodies 读方从退役证据里消失了**，
而当时那道自证门**只检查登记条目本身**，所以它是绿的。

★ 新变体，单独记：「修复本身也需要门」—— 而**第一版那道门比缺陷窄**，
于是漏掉了缺陷的一半。门必须覆盖消费点，且消费点必须**机器可算**。

### 迁了 7 个，没迁 6 个

- **读基表的 2 个刻意不迁**：`request_logs_bodies`（基表）⊂ 视图（hot ∪ 父表），
  换过去会**扩大覆盖** ⇒ 行为变更不是等价替换。
  `quality_correlations.go` 还带 `WHERE is_auto_request = TRUE` ⇒ 换源同时改口径。
- **跨包 4 个**：`sessionBodiesFromSQL` 在 `admin` 包，domains/bg 看不见
  ⇒ 处置是把开关下沉到 `db` 包，**下一步**。
- `system_prompt_prefix.go` 是 INNER JOIN，同样待下沉后处理。

### 门

`TestV1BodiesScanIncludesSwitchConsumers`（带地板断言：消费点为 0 即 Fatal）、
`TestV1BodiesSwitchConsumersHaveNoLeftoverLiteral`（抓半迁移）、
Evidence 门两跳校验。

★ **本轮返工两处，都是变异抓出来的**：
1. 半迁移判据第一版只查「同一行」⇒ 实测抓不住 ⇒ 放宽为「消费点文件里
   不得有任何匹配 v1 bodies 模式的非注释行」，三种形态现在全红。
2. ★ **两跳校验第一版极性写反、几乎恒真**：它遍历的是**已登记的**切换层，
   能报的唯一情形已被另一道门禁掉 ⇒ 那段代码等价于注释。
   是 M34 的**阴性对照**抓到的（把 Evidence 指向 `sessionBodiesFromSQLUnregistered()`
   门仍绿）。判据已改为「Evidence 里的 `from/join` + `fn()` 必须是已登记切换层」。

变异：8 条（M30–M35，含 3 种半迁移形态）全部转红。
其中 M33″ 的第一次尝试**锚点没匹配上、变异根本没写进去**，
而那个「仍绿」一度看着像真实的门失效。

### 下一轮第一件事（更新）

1. ★ **先跑 `is_final_success` / `client_protocol` 两个回填**（需批准）。
2. ★ 生产补 2026-09-30 bodies（1,113 条）——需批准。
3. ★ 把开关**下沉到 `db` 包**（`SessionBodiesSourceSQL()`），
   让 domains/bg 的 4 个 A 类读方也能迁。
4. ★ 决定读基表的 2 个（`quality_correlations.go` / `goal/history_store.go`）：
   迁（扩大覆盖，需接受）还是保持（基表退役时才有问题）。
5. 定义「回填完成」：含 `backfill_session_bodies` 与 `validate_sessions_v2` 的处置。
6. B 类 4 个先定口径；C 类 2 个重做延迟分层并带前后读数。
7. `is_auto_request = t` 的可审计性要不要保留（属主决定，至今无门）。
8. `admin/zz_tmp_crosstab_test.go` 是已提交的临时调试文件（改 `sourceFamilyOf`
   签名时必须连带修）—— 是否清理，属主决定。

---

## §70.70 切换层下沉到 `db`；13 个 A 类读方里 12 个已迁

### 做了什么

- 切换层从 `admin` 下沉到 **`db.SessionBodiesSourceSQL()`**（与
  `SessionFamilyTurnsSourceSQL` 等同族 helper 同处），并**删掉 admin 侧薄包装**。
- 开关 key 改名 `storage.admin_session_bodies_native_read` →
  **`storage.session_bodies_native_read`**（不再只服务 admin）。
- 迁移 4 个跨包读方：`domains/sessionforensics/export.go`(2)、
  `domains/sessionsummary/summarizer.go`(2)、`system_prompt_prefix.go`(1，INNER)、
  `bg/passive_probe_listener.go`(1)。
- 消费点识别支持**导出符号全仓解析**（按大小写分流）。
- 3 道新门 + 1 道改名残留门。

### ★ 切换层「住哪个包」是会静默吃掉读方的决定

迁完 4 个跨包读方后，bodies 总体从 **27 掉到 23** —— 4 个文件从退役证据里
消失，而那道门**本来就故意红** ⇒ **零信号**。这与 §70.67 是同一失效模式的
**跨包形态**：消费点识别当时只在切换层所在包内找 `fn(`。

### ★ 薄包装是**有害**的，已删

第一版让 `admin.sessionBodiesFromSQL()` 委托给 db 那一层。实测
**只找到 5 个消费点，应有 14 个** —— 9 个 admin 消费点调的是那个只做
`+ " rb"` 的包装，门按「谁调用了切换层」识别，看不见它。
⇒ **门与 v1 字面量之间又隔了一层，而那层对门不透明。** 13 个调用点全部
直接调 `dbpkg.SessionBodiesSourceSQL()`。

### ★ 本轮我三次把自己的门写错

1. **薄包装**让 9 个消费点隐形 ⇒ 层数即不透明度。
2. **消费点识别把注释当调用** —— 我自己写的注释里含
   `dbpkg.SessionBodiesSourceSQL()`，于是定义文件成了自己的消费点。
   **注释不是调用。**
3. **两跳校验第二版过宽** —— 把 `SessionFamilyTurnsSourceSQL()`
   （会话族 raw helper，不需要登记）也当成切换层。
   **一个必然误报的判据比没有判据更糟。**

★ 2 和 3 同根：**在源码文本里找东西，却没先想清楚「什么不算」。**

### 门

`TestBodiesSwitchDefaultsToV1`、`TestBodiesSwitchKeyIsSingleSourced`（禁旧 key 残留）、
`TestV1BodiesScanIncludesSwitchConsumers`。Evidence 锚点改用**函数名 token**
（gofmt 会在两种拼接拼写间切换，锚拼接形态必然脆 —— 实测 3 个文件失配）。

### 仍未迁的 A 类

`admin/quality_correlations.go`、`domains/hooks/goal/history_store.go` ——
读的是**基表** `request_logs_bodies`，视图 ⊋ 基表 ⇒ 换过去扩大覆盖 = 行为变更；
前者还带 `is_auto_request = TRUE` ⇒ 换源同时改口径。
**需属主拍板，不是技术阻塞。**

### 下一轮第一件事（更新）

1. ★ **先跑 `is_final_success` / `client_protocol` 两个回填**（需批准）。
2. ★ 生产补 2026-09-30 bodies（1,113 条）——需批准。
3. ★ **属主拍板**：读基表的那 2 个 A 类读方，迁（接受覆盖扩大）还是保持。
4. ★ 定义「回填完成」：含 `backfill_session_bodies` 与 `validate_sessions_v2` 的处置。
5. B 类 4 个先定口径（ts 语义 / 指标定义）；C 类 2 个重做延迟分层并带前后读数。
6. `is_auto_request = t` 的可审计性要不要保留（属主决定，至今无门）。
7. 清理 `admin` 包里两份「剥 Go 注释」的实现（本轮撞上，非本轮引入）。
8. `admin/zz_tmp_crosstab_test.go` 是已提交的临时调试文件 —— 是否清理。

---

## §70.70b 收口阶段：连续三次自纠，机制化而非「我小心一点」

上一节写的是功能，本节写的是**收口时才发现的东西** —— 三次全是我自己造成的。

### ① gofmt 连带损伤**第二次**复发（§9.232 已犯过一次）

提交前显式白名单差集：**预期 22 / 实际 87 ⇒ 多出 65 个**，
其中 **64 个是我从未打算碰的文件**。形态：结构体列宽重排、doc 注释凭空插入
`//` 空行，以及最阴的一种 ——
`admin/models_alias_sql_live_test.go` 里 `COALESCE(quantization,'')`
被 gofmt 智能引号化成 **`COALESCE(quantization,”)`**
（Go 1.19+ 对 doc 注释里的 `''` 会做智能引号替换）。

**第二次复发证明「我小心一点」不是解法。** 三个机制：

- 只对**自己改过的文件**跑 `gofmt -w <file>`，绝不 `gofmt -w <包>`；
- 提交前跑**显式白名单差集**，差集非空就逐个判，不靠「我记得改了哪些」；
- 差集计算两侧**排序口径必须一致** —— 本轮 `comm` 两侧同时出现同一文件，
  原因是清单用默认 locale 排序而 `git diff --name-only` 用另一套。
  ⇒ `export LC_ALL=C`。**同一数据出矛盾，先怀疑排序口径再怀疑数据。**

★ 反向发现：连带损伤**只落在我没碰的文件上** ——
我改的 23 个文件在 HEAD 上**本来就 gofmt-clean**（逐个核实过）。
⇒ 风险可以**预先判定**，不必事后逐个看 diff。

### ② 我自己写坏了 3 处中文（U+FFFD），HEAD 侧全是 0

`函<U+FFFD><U+FFFD>名数` / `db <U+FFFD><U+FFFD>是同族` / `切<U+FFFD><U+FFFD>层`。
第 3 处是在**修完前两处之后、编辑别的文件时**又新坏的
⇒ **「刚刚学到的检查」不会自动防止下一次**，必须有机械门。

⇒ 提交前逐文件把 U+FFFD 计数与 HEAD 对照。文档另有基线
（审计 2 行 / 决策表 0 / handoff 0）。
⚠ **`grep -c` 数的是行、python `count()` 数的是出现次数**，别互相换算 ——
本轮一度以为多出 3 处，其实是同一行里的 2 个字符。
⚠ **文档里引用损坏字符时用码位记法**（`<U+FFFD>`），
否则会**抬高基线**、把证据变成新的噪声。

### ③ 改了函数名、删了文件，却没扫**引用面**

`admin/session_bodies_source.go` 已删、`sessionBodiesFromSQL()` 已改名。
我扫了 `SessionBodiesSourceSQL()` 的**调用点**（13 个，都对），
**没扫提到旧名字符串的地方** ⇒ 漏掉
`cmd/tools/sql_source_indirection_audit/manifest_test.go` 里 13 处旧函数名 +
2 处指向已删文件的路径。

★ 抓它的不是我自己的 grep，是
`TestIndirectSiteManifestCoversEveryReportedFile` 报了 3 个文件
「被审计报出但清单里没有判定」。这 3 条是**迁移的直接产物**：
迁移前 bodies 腿是**字面量**（不进该工具），迁移后进 unresolved 桶。
⇒ **清单 32 → 35 条是正确信号，不是误报。**

★ 由此一条可复用的判读规则：
**把字面量改成受管制的拼接，会让 unresolved 桶变大。**
那不是退化，是「看不见」变成「看得见且已定级」。评审看到桶在涨，
先查是**新读方**还是**老读方换了写法**。

补的 3 条里有一条**降级形态与同批不同类**：
`system_prompt_prefix.go` 是**唯一的 INNER JOIN** ⇒ 停写/DROP 后
不是「字段变空」，而是**整个系统提示词前缀查不到任何一行**。

### ④ 该工具的**结构性盲点**（读码确认，非推测）

`domains/sessionforensics/export.go` 同批迁了 2 处却**没进清单** ——
不是漏登记，是**工具看不见**：`resolve.go` 的点位枚举只遍历 `*ast.FuncDecl`，
包级 `var` 只进 `env.globals` 供解析，**不进点位枚举**
（`admin/compression_stats.go` 第 3 处同形态，清单早已注明）。
⇒ **包级 `var` 的拼接点在该工具覆盖范围之外**，
由 admin 侧三道门覆盖 —— **是另一组门在管，不是所有门都在管。**
本轮不修（修工具不在范围），已写进代码注释。

### 下一轮第一件事（§70.70b 更新）

1. ★ **先跑 `is_final_success` / `client_protocol` 两个回填**（需批准），
   后跑则 33,623 条永久丢失。
2. ★ 生产补 2026-09-30 bodies（1,113 条）——需批准。
3. ★ **属主拍板**：读基表的 2 个 A 类读方
   （`quality_correlations.go` / `goal/history_store.go`）迁还是保持。
4. ★ 定义「回填完成」：含 `backfill_session_bodies` 与 `validate_sessions_v2` 的处置
   （v1 退役后这两个工具**无处可去**）。
5. 部署 `e6193ea92` 到 252；给 `pg17-proactive-empty-table-cleanup.sh` 补会话族白名单。
6. B 类 4 个先定口径；C 类 2 个重做延迟分层并带前后读数。
7. `is_auto_request = t` 的可审计性要不要保留（属主决定，至今无门）。
8. 清理 `admin` 包里两份「剥 Go 注释」的实现。
9. `admin/zz_tmp_crosstab_test.go` 是已提交的临时调试文件 —— 是否清理。
10. （新）是否修 `sql_source_indirection_audit` 的**包级 var 盲点** ——
    漏扫的是包级 `var` 里的拼接点，不是漏扫读方。

---

## §70.71 `session_dim` 覆盖率必须按 `sys:` 拆开；探针占了 44.6% 的行

### 本轮查的事

去查 Blocker 3（`session_turns` 早段无 v1 可对照、正确性未验证），
路上撞见两件更要紧的事，都做成了门。

### ★ 1. 我自己的测量踩了 §9.160.7 的坑（第二次复发）

会话族是**两张不相交的表**：
`session_turns`（分区父表 p，1,691,590）与
`session_turns_hot`（独立普通表 r，1,890），
交集 **0**，union **1,693,480**。

本节开头的所有测量我都只用了父表。而 hot 面装的是
**10-04→10-05 的最新行、100% 用户流量、99.9% 有 dim** ——
省掉的正是问「当前数据还好吗」时最想看的那部分。
⇒ 已在 `TestSessionDimCoverageIsAUserSessionMetric` 里加**面完整性断言**：
逐面计数之和必须等于独立的 `UNION ALL` 计数。

### ★ 2. `session_dim` 覆盖率不是完整性指标

| 口径 | 值 |
|---|---|
| 总体 | **54.28%**（算术正确、**完全无用**） |
| 用户会话（非 `sys:%`） | **97.78%**（919,185 / 940,055） |
| 内部流量（`sys:%`） | **0.00%**（0 / 753,425） |

54.28% 把两个总体平均掉了，而后者的 0 是**设计如此**
（读层投影里 `sys:%` 被 NULL 掉，探针不是用户会话）。

⚠ 中间我差点写下一个错结论。行级覆盖 49.5% 看着像「一半数据缺维表行」，
真相是**分布**：21,295 个内部会话（2.5% 的会话）扛着 774,293 行（**45.7% 的行**），
单会话最大 53,851 行，最大的 5 个全是 `sys:probe:credNNN:*`。
会话级覆盖其实是 **97.5%**。**行级比例 ≠ 会话级比例**，中间隔着极度偏斜的分布。

⚠ 判据有两套，看起来等价其实不等价：
读层按 `session_id LIKE 'sys:%'` 判；列标志是 `is_auto_request`。
实测 **`sys:%` ⇒ auto 精确成立（0 例外），但 auto ⇏ `sys:%`**（1,439 例外）。
生产按 id 判，门就必须按 id 判。

### 对「探针要不要留可审计记录」那个待拍板项的影响

| 口径 | 探针占比 |
|---|---|
| `session_turns` **行数** | **44.6%** |
| `prompt_tokens` | **0.41%** |

⇒ **用行数做验收标准的比较，有近一半是被探针决定的。**
「`session_turns` 有 169 万行」**不是用户数据量的度量**。
这不是拍板依据（保留与否是政策问题），但评估退役影响时
**只报行数会被探针主导**。

### 门

`db/session_dim_population_test.go`：
`TestSessionDimCoverageIsAUserSessionMetric`（真库，含面完整性断言）、
`TestSessionDimCoverageVerdict_ControlPair`（10 例对照）、
`TestSessionDimCoverageVerdictReportsBothArms`、`TestInternalSessionIDExpr`。
阈值是**设计不变量**（内部 bar 1%、用户 floor 90%；实测 0.00% / 97.78%），
不是钉死的测量值。

变异 **7 条全部转红 + 2 条阴性对照仍绿**。
★ 第 7 条第一轮**没咬住**，抓到我自己写的近乎恒真的断言：
投影「值」层面查常量的字符串，而**常量的值就等于那个字面量** ⇒
「用了常量」与「内联了逐字相同的副本」无法区分。
已补**块内源码级**判定（限定 `projectionExprsV2` 块内，
因为同文件注释里逐字引用过该字面量，而 db 包里没有 `stripGoComments`）。

### Blocker 3 的现状（本地口径）

无 v1 可对照区间 = **09-07 → 09-11 共 5 天 / 163,207 条 turn**（占 9.6%）
—— 注意「24 天」是**生产**口径，本地不是。
该段完备性：bodies 100%、ts/success/turn_no 100%、dim 98.1%。
**完备性没问题**；**「正确性」仍未验证**（无 v1 可对照，只能靠内部不变量），
本轮未解决。

### 下一轮第一件事（§70.71 更新）

1. ★ **先跑 `is_final_success` / `client_protocol` 两个回填再 DROP v1**（需批准），
   后跑则 33,623 条永久丢失。本地实测两列同样**未回填**
   （`is_final_success` 100% 非 TRUE、`client_protocol` 0%）。
2. ★ 生产补 2026-09-30 bodies（1,113 条）——需批准。
3. ★ **属主拍板**：读基表的 2 个 A 类读方迁还是保持。
4. ★ 定义「回填完成」：含 `backfill_session_bodies` 与 `validate_sessions_v2` 的处置。
5. ★ **属主拍板**：`is_auto_request = t` 的探针流量要不要保留可审计记录 ——
   现在有量化依据了（行数 44.6% / token 0.41%），见 §70.71。
6. 部署 `e6193ea92` 到 252；给 cleanup 脚本补会话族白名单。
7. Blocker 3：盲区 5 天的**正确性**仍无验证手段。
   可考虑的方向是「不依赖 v1 的内部不变量」（turn_no 连续性、
   与 `session_bodies` 的一致性、API 层读回），但这需要先确认值不值得。
8. B 类 4 个先定口径；C 类 2 个重做延迟分层并带前后读数。
9. 清理 `admin` 两份 `stripGoComments`；`admin/zz_tmp_crosstab_test.go` 去留。
10. 是否修 `sql_source_indirection_audit` 的**包级 var 盲点**。
