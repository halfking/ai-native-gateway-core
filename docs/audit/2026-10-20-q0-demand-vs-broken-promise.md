# 2026-10-20 审计轮 · 那个恒为 0 的读数，和三条静默失效

> 本轮范围：`llm-gateway-go`（主）+ 同远端的其它克隆、父仓库 `ai-native-parent`、
> `ai-session-manager`、`llm-gateway-client`。
> 起因是上一轮留下的一个刺：877 决策文档里「`?q=` 的实际 QPS = **0**」，
> 台账上连着两批、两个窗口的读数全是 0。
> 本轮要回答的问题只有一句：**那个 0 是真的业务为 0，还是度量本身坏了？**

## 0. 一句话结论

**读数 0 是真的，量具也是好的 —— 但从那个 0 推出的结论是坏的。**

`?q=` 那个搜索框**承诺了一件后端做不到的事**：它的 placeholder 写「搜索请求消息内容…」，
而 `search_text` 里**只有元数据、一条正文都没有**。用户照提示去搜正文片段必然 0 命中，
于是「带 `q=` 的请求数 = 0」**分不清**「没人要」和「承诺落空后没人要」。
⇒ 台账上那四行 0 **不是**「需求为零」的证据。

同轮还查出**三条独立的静默失效**，其中两条已经修掉并落地验证：

| # | 缺陷 | 性质 | 状态 |
|---|---|---|---|
| 1 | `?q=` 搜索框承诺正文检索、后端只搜元数据 | 承诺与能力不一致 | **已修**（前端文案 + 两道契约门） |
| 2 | `injection_attack_vectors` 写侧**每次都失败**（枚举数组列绑了 JSON 字节） | 静默失效，表恒为空 | **已修**（真库验证） |
| 3 | 同表那条 ivfflat 索引**只写不读**，且建在一列**永远为 NULL** 的列上 | 纯写放大 + 误导性对象 | **已修**（迁移 881，EXPLAIN 前后对比） |

外加两条**账目/门禁**问题：`sessions.duration_ms` 的第二个夹具漏修导致两条既有测试变红；
仓内留了 678MB 审计 scratch，把仓库自己的可达性门顶红了。

---

## 1. `?q=` 的 0：读数可信，结论不可信

### 1.1 量具是好的（三条独立证据）

| 检查 | 结果 |
|---|---|
| `scripts/audit/q-usage.sh --self-test` | **4/4 全绿**：用含真 `?q=` 的离线夹具驱动**同一套**计数逻辑，断言计数器能数出非 0；另断言 `?seq=123&req=abc` 不会被裸 `q=` 误配；还断言"轮转文件一个都没命中"时必须告警 |
| nginx 日志格式 | 仓内 `deploy/` 下**没有任何 `log_format` 覆写** ⇒ 走默认 `combined`（`$request` 含查询串）⇒ `?q=` 在访问日志里**看得见** |
| 功能是否早于窗口存在 | 搜索框 `v-model="keyword"` 引入于 **2026-05-28**（dee57388a）、placeholder 于 **2026-06-12**（9156d1965）、后端 `q` 过滤于 **2026-06-08**（e9c7c9398）⇒ 测量窗口（≈53 天）**远晚于**功能存在 ⇒ 「0」不是"功能还没上线"造成的必然值 |

### 1.2 但「服务端 → 结果」这一半没人核过

上一轮 877 文档 §2.3 写：

> 「搜索框确实存在且接线正确 ⇒ 所以这是**需求为零**，不是**功能缺失**。」

这句话核对了**客户端 → 服务端**（`RequestLogsView.vue:886` 的 `q: keyword.value.trim() || undefined`），
**没有核对服务端到底搜不搜得到东西**。补上这一半：

- 后端 `?q=` 的谓词：`admin/logs.go:539-541`
  `if v := strings.TrimSpace(queryString(r, "q")); v != "" { addFilter("rl.search_text ILIKE $%d", "%"+v+"%") }`
- 而 `search_text` 的成分由 `domains/hooks/observability/telemetry/client.go:3320-3336` 的 `searchText()` 决定：
  `ClientModel / OutboundModel / ClientProfile / RequestMode / GwSessionID / GwTaskID /
  APIKeyPrefix / APIKeyOwnerUser / ApplicationCode`
  —— **一条请求/响应正文都没有**。
- v1 写侧 `admin/telemetry.go:878` 的 `buildSearchText()` 同样只取 4 个模型/配置字段。
- 真库抽样佐证（`llm-gateway-pg`，`session_turns_2026_10`）：
  `claude-opus-4-8 chat gw_d1359b20-… sk-selfcheck***` —— 就是模型 + 模式 + 会话 ID + 打码密钥。

⇒ **这个框当时承诺「搜索请求消息内容…」，实际只能搜元数据。**
而模型 / 会话 ID / 任务 ID 在**同一行工具条上各有专用输入框**（ModelPicker、gwSessionFilter、gwTaskFilter）
⇒ 这个 `?q=` 框对用户**没有任何独有用途**：它能匹配的每个字段都已被更精确的筛选器覆盖。

### 1.3 修法：让界面说实话 + 两道门不许它再漂

- `web/src/views/RequestLogsView.vue`：placeholder 改成「搜索模型/会话ID/密钥前缀…」，
  title 写清能搜哪些字段，并明说「**不检索请求/响应正文**」。
- `web/src/views/RequestLogsView.searchContract.test.ts`（新）：钉住
  ① placeholder 不许承诺正文检索 ② title 必须把限制说清楚 ③ `?q=` 接线不许被摘。
- `admin/logs_q_search_contract_test.go`（新）：钉住
  ① `?q=` 必须仍落在 `rl.search_text` 上 ② **写侧 `searchText()` / `buildSearchText()`
  的成分里不许出现正文类字段**。
  ⇒ 一旦有人把正文塞进 `search_text`，这道门会变红并**要求同步改前端文案** ——
  而那会牵动 877 的写放大结论（正文是 TOAST 宽列），必须重做定量决策，不能顺手改文案。

> ⚠ **刻意的取舍**：本轮**没有**把 `?q=` 改成真正的正文检索。
> 那需要让谓词去匹配 `request_preview/response_preview`（v1 族）或
> `request_delta/response_delta`（session 族）—— 前导通配符 + 无 trigram 索引
> ⇒ 877 那条 GIN 索引将**不再能服务这条谓词**，写放大结论要整体重做。
> 那是**架构决策**，不在本轮边界内。本轮只做**诚实化**并把它记成待办。

> ★ **同轮另一会话的订正与本轮的关系（必读，否则会读成两份互相矛盾的结论）**：
> 同一仓库里另一会话在 `docs/audit/2026-10-20-877-读侧语义订正.md` 里
> 也核对了同一件事（`search_text` = 9 个元数据字段、不含正文），
> 并写下「**该能力没有坏：它精确地做它广告出来的事**」。
> ⇒ **这句只对修复后的稳态成立**：它引用的那行 placeholder
> （`搜索模型/会话ID/密钥前缀…`）是**本轮改完之后**才有的；
> 改动前的原文（git 可查，`git show 103a95465:web/src/views/RequestLogsView.vue`）是
>
> ```
>             placeholder="搜索请求消息内容…"
>             title="消息片段"
> ```
>
> ⇒ **改动前**界面承诺的正是"请求消息内容"，而后端只能搜元数据
> ⇒ 那是**承诺落空**，不是"能力精确"。
> 我已在那份文档顶部加了一条补注（不改它的正文），把两个结论各自成立的前提写清。
>
> ★ 这条插曲本身是一条**量具教训**：**把"改动后"的量具读数当成"改动前"的性质**，
> 结论会"看起来自洽、实际前提不成立"。
> ⇒ 凡是引用"当前工作树的某一行"来论证**历史状态**的结论，
> 都必须把那一行**固定到某个 SHA** 再引。

### 1.4 对 877 决策的订正

`docs/audit/2026-10-19-877-写放大定量决策.md` §2.3 已改：
删掉「所以这是需求为零」那半句，改为「读数 0 是真的，但它**不足以**支撑那个动作的决定性理由」，
并在台账表格下加了一条**读数边界**提示（那四行 0 仍然是"没人发出过带 q= 的请求"的有效证据，
但不是"没人需要这个能力"的证据）。

**877 本身不回滚** —— 写放大是实测的、索引的成本是实打实的，降级 `?q=` 为显式启用的**结论不变**，
只是**理由**从"没人用"换成了"能力与承诺不一致，先修承诺再谈启用策略"。

---

## 2. 缺陷 2：攻击向量库的写侧**从来没有成功过**

### 2.1 根因（三个独立证据）

`domains/promptinjection/detector.go:798-816`（修前）：

```go
categoriesJSON, _ := json.Marshal(result.Categories)
query := `INSERT INTO injection_attack_vectors (
    tenant_id, attack_text, attack_hash, categories, severity, source, detected_at
) VALUES ($1, $2, $3, $4, $5, 'detection', NOW()) …`
_, err := d.db.ExecContext(ctx, query, tenantID, input, hash, categoriesJSON, result.Score)
```

列 `categories` 的类型是**枚举数组** `injection_category[]`（迁移 364），
而 `$4` 绑的是 `json.Marshal` 的 `[]byte` ⇒ lib/pq 按文本发送 ⇒ PG 报：

```
ERROR:  malformed array literal: "["prompt_injection","jailbreak"]"
DETAIL:  "[" must introduce explicitly-specified array dimensions.
```

真库复刻（用同样的字面量形状）⇒ **同样的错误**。
而 `d.db` 是 `*sql.DB` + `lib/pq` ⇒ 这条语句**每次都失败**。

失败被一行 `slog.Warn("prompt_injection: failed to learn attack pattern", …)` 吞掉
⇒ **静默失效**：请求路径不受影响（写在 goroutine 里），表**恒为空**，
读侧 `admin/prompt_injection_handler.go:1219` 永远返回空集。

**第二道防线也会单独致命**：`severity INT CHECK (severity >= 1 AND severity <= 10)`，
而 `result.Score` 取的是命中规则的**最大 severity**（规则取值域 3..10）——
正常路径在区间内，但**一旦规则集变了**（出现 0 分规则）就会再撞一次 CHECK。

### 2.2 修法

`insertAttackVectorSQL` 提为具名常量（测试与生产共用**同一条**语句），
`$4` 改传 `pq.Array(result.Categories)`（text[]），
由 SQL 侧把**合法**的枚举标签转成 `injection_category[]`：

```sql
SELECT $1, $2, $3,
       ARRAY(SELECT c::injection_category
             FROM unnest($4::text[]) AS c
             WHERE c = ANY (enum_range(NULL::injection_category)::text[])),
       $5, 'detection', NOW()
```

- **不在枚举里的标签被丢弃**而不是让整条 INSERT 失败 ——
  DB 动态规则可以带自定义类别，一个不认识的类别不该让整条学习写不进去；
- `attackVectorSeverity(score)` 把分数夹进 CHECK 区间（越界降级成边界值，
  而不是"整条写不进去"）。

### 2.3 判据（真库，跑不起来就跳过，但跳过**不构成证据**）

`domains/promptinjection/detector_attackvector_test.go`（新，**该包此前没有测试文件**）：

| 测试 | 断言 |
|---|---|
| `TestAttackVectorSeverity_ClampsIntoCheckRange` | 越界分数被夹到 1 / 10 |
| `TestInsertAttackVectorSQL_LandsAndDropsUnknownCategories` | 合法类别**真的落库**；非法类别被丢弃；severity 0 夹到 1 后能落库 |
| `TestInsertAttackVectorSQL_OldShapeStillFails` | **变异见证**：旧形状（JSON 字节）必须仍然失败 |

★ 用 `TEST_PG_URL` 门控：pgxmock 对参数类型不敏感 ⇒ 旧 bug 在 mock 下**照样全绿**，
能抓住它的只有**让 PG 自己解析 $4 的实际线格式**。
所有写入在事务里、结尾 ROLLBACK ⇒ 不污染真库。

**变异实测**（把修正后的 SQL 换回旧形状）：门**变红**
（`ERROR: invalid input value for enum injection_category: …`），
还原后转绿、`md5` 逐字一致。

---

## 3. 缺陷 3：一条只写不读、且建在恒 NULL 列上的 ivfflat 索引

### 3.1 读方普查（全仓源码，非 pg_stat_statements）

| 表 | 索引 | `<=>` 读方 | 结论 |
|---|---|---|---|
| `task_type_centroids` | `idx_task_type_centroids_embedding` (ivfflat, lists=10) | **有**：`autoroute/embedding_classifier.go:97-100` `ORDER BY centroid <=> $1::vector` | **活的，保留** |
| `injection_attack_vectors` | `idx_attack_vectors_embedding` (ivfflat, lists=100) | **无** | **死的，删除** |

`injection_attack_vectors` 全仓只有两处引用：
`detector.go` 的 INSERT 与 `admin/prompt_injection_handler.go:1219` 的
`SELECT … FROM injection_attack_vectors WHERE tenant_id = $1`（**不碰 embedding**）。

而且 `embedding` 列**恒为 NULL**：迁移 364 建表时它是 `embedding TEXT`，
随后 `ALTER COLUMN embedding TYPE VECTOR(1536) USING NULL`；
而唯一的写入方（detector.go）的 INSERT 列清单里**没有 embedding**。

### 3.2 写放大实测（`EXPLAIN (ANALYZE, BUFFERS)`，同机同数据，唯一差别是这条索引）

插入 200 行 1536 维向量：

| | Execution Time | Buffers: shared hit / **read** / written |
|---|---|---|
| **有** ivfflat (lists=100) | **3229.421 ms** | 2463 / **21279** / 1737 |
| **无** ivfflat | **1149.310 ms**（**-64.4%**） | 3116 / **12** / 313 |

⇒ 读缓冲 21279 → 12（-99.9%），墙钟 -64%。

### 3.3 为什么这条索引此前**完全隐形**

因为表恒为空（§2 的写侧缺陷）⇒ 索引里一个条目都没有 ⇒ 零成本。
**修好写侧之后表开始有行，它就从"零成本的死物"变成"有成本的死物"**
⇒ 必须与写侧修复**同轮**处理，否则修完写侧反而更慢。

### 3.4 实施：迁移 881

`sql/migrations/startup/881_drop_attack_vectors_embedding_index.sql`

守卫**在文件里现算**（不写死结论），两条，缺一不可：

1. `embedding` 列**没有非 NULL 值** ⇒ 将来真有人写向量了 ⇒ **拒绝删**；
2. 目标索引形态确实是 **ivfflat on embedding** ⇒ 形态被改过 ⇒ **拒绝删**。

任一不成立 ⇒ `RAISE EXCEPTION` ⇒ 整条回滚，什么都没发生。

**真库实测**（`llm-gateway-pg`，PG 17.10）：

```
-- 守卫的反向分支（塞一行 embedding 非 NULL 的数据）
ERROR:  881: public.injection_attack_vectors 里已有 1 行 embedding 非 NULL
        ⇒ 该索引可能已在服务向量检索，拒绝删除。请先确认读方与用途，再决定是删索引还是保留。

-- 正常路径
NOTICE:  881: 守卫通过 —— public.idx_attack_vectors_embedding 无非 NULL embedding、形态为 ivfflat ⇒ 将删除
DROP INDEX
COMMIT
-- 迁移后同表剩 4 条索引：categories(GIN) / tenant / pkey / unique_attack_hash
-- 再跑一遍：NOTICE: 881: … 不存在（已删或未建）⇒ 无事可做，跳过（幂等）
```

契约门：`sql/migrations/startup/migration_881_drop_attack_vectors_embedding_index_test.go`（4 条，全绿）
—— 钉住"只删那一条"、"守卫含反向分支"、"两份 spec 同一份字节"、"RAISE 只用裸 %"。

> ★ **本轮踩到的一个坑，值得单列**：PL/pgSQL 的 `RAISE` **只认裸 `%`**（`%` 取下一个参数），
> `%s` 是 SQL 函数 `format()` 的 specifier，**在 RAISE 里不是 spec** ——
> 解析器把 `%` 当作"取下一个参数"、紧跟的 `s` 照原样输出。
> ⇒ `'已有 %s 行'` 实际渲染成 **"已有 1s 行"**。
> 这条错误消息是本轮**靠肉眼发现**的（消息里多了个 "s"）——
> **一个"看起来像排版问题"的异常，根因是一条会静默吃掉参数的 spec 误用**。
> 仓内另有 1 处同形（`sql/fixes/fix-request-wal-hot-primary-key.sql:52`），
> 渲染成 "  .212：request_wal_hots 上找不到…"（编号被吃掉、表名后面挂了 s），**已修**。

---

## 4. 数据库侧：慢 SQL 的**量具本身是坏的**

RULES 的任务 4 要求查慢 SQL。开工第一步就撞上：

```
ERROR:  pg_stat_statements must be loaded via "shared_preload_libraries"
```

`llm-gateway-pg` 的实测状态：

| 设置 | 值 | 判读 |
|---|---|---|
| `pg_stat_statements` 扩展 | **已安装**（1.11） | 但**装了不等于能用** |
| `shared_preload_libraries` | `citus`（**没有 pg_stat_statements**） | ⇒ 上面那条 ERROR 的直接原因 |
| `log_min_duration_statement` | **-1**（关闭） | ⇒ 没有慢查询日志兜底 |
| `track_io_timing` | off | ⇒ 没有 I/O 计时 |

⇒ **"没有慢 SQL"这个读数是量具坏了的产物，不是事实。**

★ 但**这不是仓库的缺陷**，是**本地环境的漂移**：
本仓自己的部署脚本早就把 pg_stat_statements 预加载了 ——
`scripts/deploy-211-docker.sh:558`（`-c shared_preload_libraries=pg_stat_statements,pgcolumnar`）、
`scripts/deploy-local.sh:796`、以及 `scripts/local-dev/recover-pg-citus-image.sh:268`
（它整个脚本就是为了修这个：`shared_preload_libraries = 'citus,citus_columnar,pg_stat_statements'`），
`tests/deploy_host_first_contract_test.sh:205` 甚至**断言**部署配置里必须有这条。
⇒ **本地这个容器是绕过仓库脚本手工起的**，所以少了这一步。

⇒ 这正是本轮主线问题的**同形**：
**一个"恒为 0 / 没有"的读数，要先问"量具装上了吗"，再谈现象。**
`?q=` 那个 0 的量具**装上了**（自检全绿）⇒ 0 可信；
慢 SQL 这个"没有"**量具没装** ⇒ 不可信。

（`pg_stat_statements` 需要重启 postmaster 才能加载；
本轮**没有**动这个容器的配置 —— 它是本轮指定的运行实例，重启会影响在跑的作业。
本轮据此改用 RULES 给的兜底口径 `log_min_duration_statement` 做**会话级**测量，
不改服务器全局配置。）

---

## 5. 顺带修掉的两条账目问题

### 5.1 `sessions.duration_ms` 的**第二个**夹具漏修（既有测试变红）

上一轮的提交 `11598d847`（"接上 `sessions.duration_ms` 的消费方"）给
`admin/session_detail_v2.go` 的 SELECT 加了第 28 列（`s.duration_ms`），
并修了 `admin/session_detail_v2_test.go` 的夹具（27 → 28 列）。
**但 `tests/session_identity_contract/session_identity_contract_test.go` 里的
同款夹具没跟着改** ⇒ 本轮 `go test ./...` 报：

```
ERROR querySessionDetail failed err="query session: incorrect argument number 28 for columns 27"
--- FAIL: TestSessionDetailV2_ResolveGwSessionIDToSessionID
--- FAIL: TestSessionDetailV2_ReverseMapFallbackToPrimaryRequestID
```

⇒ **同因同形的第二个页面**，已按同样形状补上 `duration_ms`（位置 22，值 `*int64`），
两条测试转绿。

### 5.2 仓内 678MB 审计 scratch 把仓库自己的可达性门顶红了

`go test ./...` 里另一条红：

```
登记「routeincident …」的可达性判定与实测不符：期望 1 处调用，实测 2 处
（.audit-validation/trace-retirement/source/cmd/gateway/main.go, cmd/gateway/main.go）
```

根因：`.audit-validation/`（**gitignore 掉的**审计 scratch 目录）里存着仓库源码的**快照副本**，
而 `tests/48h-audit/D07-hot-columnar/data/schema_mismatch_reachability_test.go` 的
可达性扫描器按目录名排除 `vendor/node_modules/.git/web/installer/docs/sql/deploy/scripts/skills`，
**没有排除它** ⇒ 快照副本被当成生产调用点 ⇒ 计数从 1 变 2。

处置（两步，**没有放宽断言**）：

1. **把 scratch 搬出仓库**（保留，不删）⇒
   `/Users/niuer/workspace/audit-scratch-quarantine/llm-gateway-go/scratch-20261020`（678MB）；
2. **给扫描器补一个排除项** `.audit-validation` ——
   理由与既有那一串**同类**：它是 **gitignore 掉的**目录 ⇒ **永远不可能是已提交的生产源码**，
   把它算进"生产调用点"只会数重。
   ★ 变异实测：在**真实**源码目录（`cmd/gateway/`）下造一个重复调用点 ⇒ 门**照样变红**
   （"期望 1 处调用，实测 2 处"）⇒ 排除项没有把门变成恒真。

---

## 6. 本轮没做 / 留给下一轮

| 项 | 状态 | 理由 |
|---|---|---|
| 把 `?q=` 改成真正的**正文**检索 | **未做** | 需要让谓词落到 `request_preview/response_preview`（v1）或 `request_delta/response_delta`（session 族）。前导通配符 + 无 trigram 索引 ⇒ 877 那条 GIN 索引将不再服务这条谓词 ⇒ **写放大结论要整体重做**。这是架构决策，不是本轮能顺手做的。**本轮只把承诺改成真的。** |
| `llm-gateway-pg` 预加载 `pg_stat_statements` | **未做**（需重启 postmaster） | 该容器是本轮指定的运行实例，重启会影响在跑的作业。仓库的部署脚本本来就带这条，本地容器是手工起的 ⇒ 属环境漂移，不属仓库缺陷。**下一轮可择机重启并补一条"量具自检"。** |
| `injection_attack_vectors.embedding` 的**写入方** | **未做** | 当前没有任何代码计算攻击向量的 embedding（364 把它设计成 pgvector 不可用时的占位列）。要不要真做向量检索是产品决策；迁移 881 只保证"将来真写了向量时，门会拦住删索引"。 |
| 其它仓的分支 | 见各仓小节 | — |

---

## 7. 复现

```bash
# ?q= 计数器的离线自检（4/4）
bash scripts/audit/q-usage.sh --self-test

# 攻击向量写侧（真库；TEST_PG_URL 不设就跳过，而跳过**不构成证据**）
export TEST_PG_URL='postgres://llm_gateway:<pw>@127.0.0.1:54318/llm_gateway?sslmode=disable'
go test -count=1 ./domains/promptinjection/

# 迁移 881 的守卫（反向分支）
#   在 injection_attack_vectors 里插一行 embedding 非 NULL 的数据，
#   再应用 881 ⇒ 应当 RAISE EXCEPTION 并整条回滚。

# 迁移 881 的正常路径 + 幂等
psql -v ON_ERROR_STOP=1 -f sql/migrations/startup/881_drop_attack_vectors_embedding_index.sql
```

---

## 8. 可复用的经验

1. **"恒为 0"要先分清是量具坏了还是现象为 0。**
   本轮两个例子分列两端：`?q=` 的 0 **量具是好的**（自检全绿 + 日志格式未覆写 + 功能早于窗口存在），
   而"没有慢 SQL"的读数**量具根本没装**（`pg_stat_statements` 装了扩展没预加载）。
   ⇒ 结论不同：前者可以拿来讨论需求，后者连讨论的资格都没有。
2. **量具的"能自己报错"比"能出数"更重要。**
   `q-usage.sh` 在 awk 没产出结果时**拒绝打印**并退出非零 —— 这正是它比"报个 0"可信的原因。
3. **写侧失败被 `slog.Warn` 吞掉 = 功能不存在，且没人会发现。**
   攻击向量库从建成到今天**一次都没写进过**，而表结构、索引、读侧 API、注释一应俱全。
4. **"承诺"和"能力"要分别验。** 前端接线对了、后端谓词对了，中间的**语义**没人管。
   本轮就漏在这一段：`search_text` 搜的是元数据，界面说的是正文。
5. **一个"看起来像排版问题"的异常值得追到底。**
   多出来的那个 "s"（"已有 1s 行"）是 `%s` 在 PL/pgSQL `RAISE` 里不是 spec 造成的 ——
   根因是一条会**静默吃掉参数**的 spec 误用，不是个显示瑕疵。
6. **索引的成本要按"将来会有多少行"来算，不是按"今天有多少行"。**
   一条建在恒 NULL 列上的索引今天是零成本，写侧一修好它就变成纯浪费 ——
   ⇒ 修写侧与删死索引必须**同轮**。
