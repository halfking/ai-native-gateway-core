# 接力 handoff — 会话请求数据存储与查询批判式复审收口

**日期**：2026-10-01
**检出**：`/Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4`
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
cd /Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4

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
1. 等 §8 六项拍板。其中第 7 项影响面最大：
   session_summary_v2 的正文配对键（现状 (request_id, ts) 99.85% 配不上，
   长期用空正文做总结）。改动已就位（两个函数并存），
   只差把 queryRequestLogsFallback 从 …AndTS 切到 …ByRequestID。
2. ~~补 timeline 迭代错误传播的钉测~~ → **已完成**（`3834d12f5`）。
3. 若获批 S4 真机灰度：开 storage.request_logs_write_enabled=false，
   用 GET /api/admin/sessions/dual-read-drift 盯 s4_ready；
   灰度期必须保留 734 视图的 v1 冻结分支。

【纪律】
- 改动关联键前先量命中率（一条 join + count FILTER 足够），不要推断。
- 合并后必须重跑全量门禁再提交；编译全绿不等于合并正确。
- 守卫改动必须做变异验证；判据打在产物上，不打在被测对象之外的文本上。
- 性能数字与数据前提分别验证，不接受「实测如此」的口头结论。
```
