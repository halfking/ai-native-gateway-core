# dream cycle 首跑：接真实 LLM 网关（2026-10-19）

> 任务：把 memora 的 dream cycle 在**真实网关**上跑通一次（禁用 llm-mock），
> 并验证产出是**真落库**的图数据，而不是一个"成功形状"的 stub。
> 关联仓：`/Users/niuer/workspace/ai-native-tools/memora`（本文涉及的 memora 改动）。

## 结论

**首跑成功**，且首跑本身暴露了**七个真缺陷**（不是环境噪声，都是代码与真实环境不匹配）。

首轮四个（用 `sensenova-6.8-flash-lite` 跑出来的）：

| # | 现象 | 真因 | 修法 |
|---|---|---|---|
| 1 | 抽取超时 `context deadline exceeded` | `llm.Client` 默认超时 **20s**，而真实网关对抽取 prompt 实测要 **26.2s / 26.9s** | 引擎显式给 `Timeout: 120s` |
| 2 | `LLM gateway returned malformed JSON` | `max_tokens=2000` 把输出**截断在 JSON 中间**（同批数据实际 `completion_tokens=2982`） | 引擎显式给 `MaxTokens: 8000` |
| 3 | `graph_episodes.updated_at does not exist` (42703) | 000024 只给 `graph_entities` 声明了 `updated_at`，另两张表没有；而 pgstore 三处 `ON CONFLICT DO UPDATE SET updated_at = NOW()` | 新增迁移 **000060** 补两列 |
| 4 | 实体写入 25P02、但事实写进去了 | pgstore 用 `CopyFrom` 走快路径，而 **PG 不支持在启用 RLS 的表上 COPY FROM**；回退又跑在**已被中止的事务**里 ⇒ 真实根因被掩盖 | 去掉 CopyFrom，直接走 `INSERT … ON CONFLICT` |

续轮三个（**换成旗舰模型 `minimax-m3` 才显形**的，详见下文"续轮"节）：

| # | 现象 | 真因 | 修法 |
|---|---|---|---|
| 5 | 同一模型下 `malformed JSON`（连解析都过不去） | 推理模型在 `response_format=json_object` 下仍先输出 `<think>…</think>`，而解析只剥 ```json 围栏 | `stripThinkBlocks`，只认成对的块 |
| 6 | 抽取全成功，但 `entity_type` 全是 `other` | 模型用 `type` 而不是 `entity_type`；而 `entity_type` 是**有真读方**的过滤键 ⇒ 按类型查询**静默返 0 行** | `entityTypeOf` 别名取值 |
| 7 | 耗时超 60s 时**响应写不出去**（`http_code=000`） | 网关 `http.Server` 写着 `WriteTimeout: 60s`，而抽取实测要 26.2s ~ **82.9s** ⇒ 客户端收到连接错误，**但 cycle 跑完了、数据也落库了** | 按路由延长写超时（`http.ResponseController`），不抬全局值 |

首轮结果（真实网关 `https://llm.kxpms.cn`，模型 `sensenova-6.8-flash-lite`）：

```json
{"status":"completed","observations":12,
 "episode_uuid":"7115dfed-3757-4e21-8f59-c752e2e085fe",
 "entities_written":22,"facts_written":17,"duration_ms":12452}
```

PG 侧真值（`acc-pg17` / 库 `memora`）：**1 episode + 22 entities + 17 facts**，内容语义正确
（`GIN trigram 索引` / `ScopeRedact` / `ScopeEgress` / `planner` / `duration_ms` / `search_text` …）。

续轮结果（模型 `minimax-m3`，修复前**完全跑不起来**）：
`http_code=200`、37.0s、`status=completed`、**1 episode + 18 entities + 14 facts**，
`entity_type` 分布 `concept 7 / tool 5 / person 2 / service 2 / other 2`。

## 环境与拓扑

- **旁挂容器，不动 acc 部署**：`memora-dream`（镜像 `memora:amd64-dream-20261019`），
  端口 8092，env 从运行中的 `memora-server` **反推**（不手抄凭据），只覆盖 dream 相关开关：
  `FEATURE_DREAM_CYCLE=true`、`FEATURE_PG_GRAPH=true`、`LLM_MODEL_DEFAULT=sensenova-6.8-flash-lite`。
  数据目录独立挂 `/opt/kaixuan/data/memora/dream-{data,logs}`，不与 acc 共用。
- **为什么旁挂**：`/opt/kaixuan/stacks/platform/docker-compose.platform.yml` 文件头写明
  「本文件由 gen-platform-compose.py 从运行中的容器反推生成，请勿手工编辑」，
  且 acc 是线上验收服务 ⇒ 用 `-f` 追加 override 不改生成文件，比替换线上镜像更可回滚。
- **迁移**：211 的 `memora` 库原停在 **56**；新镜像带 57/58/59/060，
  用镜像内 `kxmemory-migrate up` 依次应用到 **60**（`dirty=f`）。
- **种子数据**：12 条 `project_observations`（项目 `dream-first-run`，tenant `default`）。
  ⚠ RLS 要求 `app.tenant_id` **与** `app.project_id` 两个 GUC 都设上，否则插入被拒。

## 三次失败的排查链

### 失败 1：超时（`context deadline exceeded`）

首跑 `POST /api/dream/run` 直接 500：

```
"detail": "dream: llm extract: LLM gateway request failed:
 Post \"https://llm.kxpms.cn/v1/chat/completions\": context deadline exceeded
 (Client.Timeout exceeded while awaiting headers)"
```

实测（211 上直打真实网关，同一条 prompt，max_tokens=2000）：
`minimax-m3` 连续两次 **26.2s / 26.9s**。而 `internal/llm/chat.go` 的
`postChat` 在 `opts.Timeout == 0` 时取 **20s**；`internal/dream/engine.go` 的抽取调用
**没有显式给 Timeout** ⇒ 抽取必然被客户端掐断。

### 失败 2：输出被截断（`malformed JSON`）

把超时放宽后，错误换成 `LLM gateway returned malformed JSON`。
直连复现（`max_tokens=2000`）：输出在第 21 行断掉 ——
`Unterminated string starting at: line 21 column 14`。
把 `max_tokens` 提到 8000：`completion_tokens=2982`、`17.6s`、26 条 facts 全部完整。

⇒ **2000 这个上限从一开始就不可能够**：抽取的输出规模随 observation 条数增长，
而 engine 的 observation 上限是 500。

全量 20 个模型在同一 prompt、同一 `max_tokens=2000` 下的交叉验证（按 **memora 自己的
解析语义**评判，不是"是不是合法 JSON"）：

| 判定 | 模型 |
|---|---|
| schema 合规（entities+facts 都取到） | `glm-4`（18/13）、`glm-4-air`（25/17）、`glm-4-flash`（14/10） |
| 截断导致非法 JSON | `sensenova-6.8-flash-lite`、`glm-5.2`、`glm-4.5-flash` |
| `<think>` 前缀导致非法 JSON | `minimax-m3`、`glm-z1-flash` |
| 合法 JSON 但键名不合消费方 | `deepseek-v4-flash`（`nodes`/`edges`）、`glm-4-flash`（`knowledge_graph`） |
| 调用即 503 | `glm-4.5`/`glm-4.6`/`glm-4.7`/`glm-5*`/`nemotron-3-*` 等 13 个 |

★ 注意「能过」的那 4 个模型，**只是输出恰好没超 2000**；
`sensenova-6.8-flash-lite` 在 2000 上是**时好时坏**（边界抖动）⇒
这类缺陷单跑一次是抓不到的，必须按输出规模定参数。

★ 模型名会漂移：开工时 `abab6.5s-chat` / `abab5.5-chat` 实测可用，几小时后变成
`No available provider for model 'abab6.5s-chat'. All 6 candidates failed.`；
`GET /v1/models` 当时列 27 个，其中多数**调用即 503** ⇒
**清单里有 ≠ 可用 ≠ 守 schema**，必须当场拉清单 + 当场实测 + 按消费方语义评判。

### 失败 3：图表列不存在（42703）

抽取通过后，卡在写图：

```
"detail": "dream: write episode: pgstore: write episode:
 ERROR: column \"updated_at\" of relation \"graph_episodes\" does not exist (SQLSTATE 42703)"
```

比对代码与迁移：

- `internal/graph/pgstore/writes.go` 的三处 `ON CONFLICT … DO UPDATE SET … updated_at = NOW()`
  覆盖 `graph_episodes` / `graph_entities` / `graph_facts`；
- 000024 建表时**只给 `graph_entities` 声明了** `updated_at`；
  `graph_episodes` / `graph_facts` 从来没有这一列。

⇒ **PG 图后端的写入路径从未对真库跑通过**。既有防线 `CheckTables` 只查"表在不在"，
不查"列在不在"；既有单测全用 fake ⇒ 两道门都盖不住。

### 失败 4：实体写入 25P02（真实根因被掩盖）

补列后 cycle 能跑完，但报告里 `entities_written: 0`，并带一条警告：

```
"write entities: pgstore: write entities: error preprocessing batch (prepare):
 ERROR: current transaction is aborted, commands ignored until end of transaction block
 (SQLSTATE 25P02)"
```

直连复现（`SET LOCAL ROLE memora_app_rls` + `set app.tenant_id`）：

```
ERROR:  COPY FROM not supported with row-level security
HINT:  Use INSERT statements instead.
```

⇒ `WriteEntities` 先走 pgx `CopyFrom` 快路径，而 **PostgreSQL 不支持在启用 RLS 的表上
COPY FROM**（`graph_entities` 由 000027 装 `tenant_isolation` 策略且 FORCE RLS）
⇒ 这条快路径**永远失败**；原实现在失败后回退到 `INSERT … ON CONFLICT`，
但回退**在同一个已被 COPY 失败置为 aborted 的事务里**跑 ⇒
报出来的是 25P02「事务中止」，真实根因（COPY 与 RLS 不兼容）被彻底掩盖。

★ 实体最终仍落了 19 行，但**不是** `WriteEntities` 写的 ——
是 `WriteFacts` 的端点 MERGE（`upsertFactEndpointsTx`，用普通 INSERT，RLS 下可用）顺带写的。
所以「事实写进去了」与「实体写入路径是坏的」可以同时成立。

## 修法与判据

### memora 侧改动

| 文件 | 改动 |
|---|---|
| `internal/dream/engine.go` | 抽取调用显式给 `Timeout: 120s`、`MaxTokens: 8000`（两个常量带实测注释） |
| `internal/graph/pgstore/writes.go` | 去掉 `CopyFrom` 快路径，直接走 `INSERT … ON CONFLICT`（附 PG 报错原文与"回退在中毒事务里跑"的说明） |
| `internal/kbmigrations/000060_graph_updated_at.{up,down}.sql` | 新迁移：`ALTER TABLE … ADD COLUMN IF NOT EXISTS updated_at`（幂等，走 fast-default 不重写表） |
| `internal/dream/extraction_params_test.go` | **新门**：断言**调用时真正传出去的** `opts.Timeout ≥ 60s`、`opts.MaxTokens ≥ 4000` |
| `internal/graph/pgstore/write_columns_contract_test.go` | **新门**：代码写路径引用的每一列，迁移都必须建出来（代码↔迁移契约） |
| `internal/graph/pgstore/write_entities_rls_test.go` | **新门（真库，`-tags=integration`）**：entity 写入在**带 RLS**的真库上可用且真的落库；重复写不翻倍 |
| `internal/llm/chat.go`（续轮） | 新增 `stripThinkBlocks`：`ChatJSON` 的 JSON 路径上先剥成对思考块再剥围栏 |
| `internal/dream/extract.go`（续轮） | 新增 `entityTypeOf`：`entity_type` → `type` → `entityType` 别名取值 |
| `internal/dream/handler.go`（续轮） | dream 路由组挂 `extendWriteDeadline(150s)`，按请求延长写超时 |
| `internal/llm/think_prefix_test.go`（续轮） | **新门**：think 前缀能被解析 + 未闭合标签不剥 + 截断 JSON 仍被拒 |
| `internal/dream/entity_type_alias_test.go`（续轮） | **新门**：实体类型别名（用实测原样 payload） |
| `internal/dream/write_deadline_test.go`（续轮） | **新门**：真路由 + 慢假 LLM + 短 `WriteTimeout` ⇒ 响应写得出去 |

### 判据的变异验证（各自变红）

| 门 | 变异 | 结果 |
|---|---|---|
| `extraction_params_test.go` | 删掉 `Timeout:` 字段 | 红：`extraction timeout = 0s, want >= 60s` |
| `extraction_params_test.go` | `MaxTokens` 回落到 2000 | 红：`extraction max_tokens = 2000, want >= 4000` |
| `write_columns_contract_test.go` | 移走 000060 迁移 | 红：`graph_episodes: … [updated_at]`、`graph_facts: … [updated_at]` |
| `write_columns_contract_test.go` | 把 `entity_type` 改成 `entity_typo` | 红：`graph_entities: … [entity_typo]` |
| `write_entities_rls_test.go`（真库） | 把 CopyFrom 放回去 | 红：**逐字复现**生产首跑的 25P02 文本 |

★ 最后一条特别说明问题：变异复现出的错误文本与生产首跑**完全一致**，说明这道真库门
覆盖的正是真实缺陷，而不是另一个近似问题。

### 门的边界（写明，避免误读）

- `extraction_params_test.go` 断言的是**下限**（60s / 4000），不是"精确等于 120s / 8000"；
  实测值随批量线性涨，按**最大批量**留余量，而不是按"这次刚好够"。
- `write_columns_contract_test.go` 只覆盖**写入路径**引用的列
  （`INSERT` 列清单 + `ON CONFLICT` 赋值目标），**不覆盖读路径**（SELECT / JOIN）——
  那需要 SQL 解析器，不是一道正则门该背的债。门内有 3 条非空自检
  （迁移侧读到 ≥8 列、代码侧读到 ≥5 列、`graph_entities` 的 `updated_at` 必须被解析到），
  防止"解析器读不出东西 ⇒ 没有不存在的列 ⇒ 恒绿"。
- `write_entities_rls_test.go` 需要 Docker（dockertest 自起 `postgres:17-alpine`），
  Docker 不可用时 `t.Skip` ⇒ 它的强度依赖 CI 有 Docker。

## 证据链

1. **memora 侧真实调用**：`docker logs memora-dream` —
   `ai_call endpoint=dream_cycle_extract user_id=dream-runner latency_ms=12424 tokens=3409`，
   紧随 `dream cycle completed observations=12 entities=22 facts=17 duration_ms=12452`。
2. **网关侧真实流量**：154 的 `/var/log/nginx/llm-kxpms-cn-access.log` 在同一时间窗
   （00:25:10 / 00:25:12）出现 `POST /v1/chat/completions 200`。
   ⚠ 该 nginx 的 client IP 是**共享 NAT**（`111.0.232.97`），单看 IP 不能归属到 211，
   归属靠上面的 `ai_call` 日志做锚点。
3. **PG 落库真值**（`acc-pg17` / `memora`，带 `app.tenant_id` + `app.project_id`）：
   `graph_episodes=1`、`graph_entities=22`、`graph_facts=17`。
   ⚠ 计数**必须**带两个 GUC —— 否则 FORCE RLS 会把它们全滤成 0（我第一次就踩了这个坑：
   "落库 0 行"其实是"看不见"）。

## 续轮：换模型才暴露的另外三处（2026-10-20 凌晨）

首跑用的 `sensenova-6.8-flash-lite` 恰好是"守 schema + 慢"的那一类。
把它换成网关在架的旗舰模型 **`minimax-m3`** 后，又暴露三处**只在换模型时
才会显形**的问题 —— 它们都藏在"模型恰好听话"的后面。

### 续 1：推理前缀让整类模型完全不可用

`minimax-m3` 在 `response_format=json_object` 下仍会**先输出一段推理**：

```
<think>The user wants me to extract a knowledge graph from the project event records
...
</think>

{"entities":[{"name":"alice","type":"person"}],"facts":[...]}
```

而 `internal/llm/chat.go` 的 `stripCodeFences` 只剥 ```json 围栏
⇒ `json.Unmarshal` 直接报 `Expecting value: line 1 column 1 (char 0)`
⇒ `ChatJSON` 恒返回 `LLM gateway returned malformed JSON`。

★ 这意味着**换一个会思考的模型，dream cycle 立刻全线失效**，
而错误信息指向"网关返回了坏 JSON"这个与根因无关的方向。
（`glm-z1-flash` 实测同样是这个形态。）

**修法**：`internal/llm/chat.go` 增加 `stripThinkBlocks`，在 `ChatJSON` 的
JSON 路径上先剥成对的 `<think>…</think>` / `<thinking>…</thinking>` /
`<reasoning>…</reasoning>`，再剥围栏。**只认成对的块** —— 推理被截断时
留下的是残缺标签，那种内容本来就不是合法 JSON，不该在这里被"猜"出来。
`ChatText` 故意不剥：它的调用方（rerank 等）要的是模型原文。

⚠ Go 的 regexp 是 RE2，**不支持反向引用**，所以三种标签各写一条分支，
不能用 `\1` 配对。

### 续 2：`entity_type` 的别名让"按类型查询"静默返空集

`minimax-m3` 的实体用 **`type`** 而不是 prompt 要求的 `entity_type`：

```json
{"name":"alice","type":"person","summary":"…"}
```

⇒ `entity_type` 取不到值 ⇒ 全部落成 `"other"`。

★ **这不是观感问题**：`entity_type` 是**有真读方**的过滤键 ——
`internal/graph/pgstore/read_queries.go` 的 `n.entity_type = $5`，
并由 `/api/graph/entities?entity_type=…` 暴露。
类型在解析阶段全丢成 `other` 之后，按类型查询**静默返回 0 行** ——
看起来像"没有这类实体"，实际是"类型信息压根没进来"。

**修法**：`internal/dream/extract.go` 增加 `entityTypeOf`，
按 `entity_type` → `type` → `entityType` 取值。与 `parseFacts` 里
`relationships` 别名同一条理由：不同模型对同一 schema 的键名选择不稳定，
接受别名比静默丢掉整类信息更稳妥。

复验（同一批 observations、同一模型，修复前后）：

| | entity_type 分布 |
|---|---|
| 修复前 | 全部 `other` |
| 修复后 | `concept 7 / tool 5 / person 2 / service 2 / other 2` |

### 续 3：耗时超 60s 时，**响应写不出去**（"成功长得和失败一模一样"）

抽取耗时实测 26.2s ~ **82.9s**（minimax-m3 在 8000 max_tokens 下更慢），
而网关的 `http.Server` 写着

```go
WriteTimeout: 60 * time.Second      // internal/server/server.go
```

⇒ 超过 60s 的 cycle，**响应根本写不出去**：客户端收到连接错误
（`curl: http_code=000` + 响应体为空；Go 客户端收到 `EOF`），
**而 cycle 其实跑完了、数据也全部落库了**。

★ 现场表现是"成功长得和失败一模一样"：调用方看到报错，合理地重试，
于是每重试一次就多出一个 episode（每次 run 生成新的 uuid）。

**修法**：`internal/dream/handler.go` 给 dream 路由组挂
`extendWriteDeadline(150s)`，用 `http.NewResponseController`（Go 1.20+）
**按请求**延长写超时（gin v1.10 的 `ResponseWriter` 实现了 `Unwrap()`）。
**不抬全局 `WriteTimeout`** —— 全局抬高会让所有慢请求都失去上限；
而 dream run 的耗时上界是已知的（由 LLM 侧预算 `extractionTimeout=120s` 决定）。

⚠ 服务端延长**不等于**这条 HTTP 入口适合长任务：前置代理的读超时也会掐断。
**已把整条入口链实测一遍**（2026-10-20）：

```
252 nginx  memora-api.itestu.cn / memora.itestu.cn
              proxy_read_timeout 300s
              └─ proxy_pass 100.106.67.198:8000
211 nginx  ns-gateway-211.conf  (listen 100.106.67.198:8000)
              proxy_read_timeout 300s
              └─ upstream ns_memora_api_upstream → 127.0.0.1:8091
211 docker  memora-server (acc) :8091
              └─ Go http.Server WriteTimeout 60s  ← 真正的瓶颈
```

⇒ **每一层能读到的 nginx 都是 300s**，远高于 dream 的 150s
⇒ **瓶颈只有 Go 服务那 60s**（已按路由提到 150s），nginx 不是问题。
⚠ 但"nginx 默认 60s"这条**一般性提醒仍然成立**：本链之所以是 300s，
是因为这些 vhost 显式写了 `proxy_read_timeout 300s`；**新加的 vhost 不会自动继承**。
⇒ 若将来把 dream 暴露到**别的**入口，必须逐层确认读超时 > 150s。
⇒ 一层**没读到**的：252 的 `memora.kxpms.cn` → `127.0.0.1:18082` →
`kxmemory-go-prod`（podman，host 网络）。那个 `gateway` 二进制**不是 nginx**，
没有可读的超时配置；而且它连的是 `llm_gateway` 库、**不带任何 `FEATURE_*` 开关**
⇒ 与 dream cycle 无关（它是网关产品面，不是 memora 面）。

代码里本就写明「生产路径应走 asynq 任务（worker.TypeDreamCycle），
这里的同步入口保留给手动触发与调试」（该任务类型与 handler **已存在**），
本修复只是让**手动触发**这条路真能拿到结果。

### 续轮的三道新门（各自做过变异验证）

| 门 | 位置 | 变异 | 结果 |
|---|---|---|---|
| think 前缀能被解析 | `internal/llm/think_prefix_test.go`（httptest 走真 HTTP 往返，返回**逐字取自实测**的 think 前缀响应） | ChatJSON 不再调用 `stripThinkBlocks` | 红：`LLM gateway returned malformed JSON` |
| 未闭合标签不被猜 | 同上 | 放宽到连未闭合标签也剥 | 红：`unclosed_tag_is_kept` |
| 截断 JSON 仍然被拒 | 同上 | —— | 绿（确认"剥思考块"没有把 JSON 合法性门开得过大） |
| entity_type 别名 | `internal/dream/entity_type_alias_test.go`（用**实测到的原样 payload**） | 去掉 `type` 别名 | 红：`EntityType = "other", want "person"` |
| 响应真的写得出去 | `internal/dream/write_deadline_test.go`（真路由 + 慢假 LLM + 短 `WriteTimeout` 的测试服务器） | 摘掉路由上的注册行 | 红：`EOF`（**与生产症状一致**） |

★ 最后一道门的构造值得记：它跑的是
`RegisterRoutes → Handler.Run → Service.Run → Engine.RunCycle` 的**完整链路**，
只把 LLM 换成慢假货，并把服务器的 `WriteTimeout` 设成比 handler 耗时短。
少了"注册"这一步，中间件写对了但没挂上路由，门照样会绿。

### 续轮端到端复验（minimax-m3，真实网关）

```
http_code=200  time=37.0s
status=completed  observations=12
episodes=1  entities=18  facts=14
entity_type 分布：concept 7 / tool 5 / person 2 / service 2 / other 2
```

对照：修复前同一个模型是 `http_code=000`（响应写不出去）+ 全 `other` + 完全解析不了。

## 推送就绪度（干跑，未改动共享 checkout）

两仓的 `origin/main` 都被其他会话推进了（llm-gateway-go 落后 **56** 条、memora 落后 **1** 条）。
用**临时 worktree** 做 cherry-pick 干跑（不碰共享 checkout 的 `main`）：

| 仓 | cherry-pick 到最新 origin/main | 冲突 | rebase 后跑门 |
|---|---|---|---|
| llm-gateway-go | 4 个提交 | **0** | `go test ./admin/` **exit=0**（那 7 条既有失败已被上游那 56 条修掉）、`sql/migrations/startup` ok、`domains/session/v2` ok |
| memora | 3 个提交 | **0** | `go test ./...` **exit=0**、`-tags=integration ./internal/graph/pgstore/` ok |

`LESSONS.md` 与 `docs/db-changelog.md` 两处有上游追加，**自动合并成功**。
逐文件比对确认：我改过的 17 个文件在两棵树里**逐字节一致**（只有
`docs/db-changelog.md` 因上游追加而不同）⇒ 前端 `SessionSummaryBar` 的 **8/8**
结果可直接继承，无需在 rebase 树里重装 node_modules。

⇒ **结论：rebase 后可直接推送，且门全绿。** 但 rebase 会改动共享 checkout 的
`main` 基线（其他会话也从这个 checkout 出发），所以**没有自动执行** ——
留给仓库属主决定。


## 遗留与边界

- **旁挂容器未纳入 compose**：`memora-dream` 目前是 211 上的独立容器
  （`docker rm -f memora-dream` 即可撤）。若要长期启用 dream cycle，
  应把它并进 `docker-compose.platform.yml` 的生成流程（该文件由 `gen-platform-compose.py`
  从运行容器反推生成 ⇒ 改完必须跑 `verify-compose-equivalence.py`）。
- **模型选型仍是脆弱点**：`sensenova-6.8-flash-lite` 本轮可用且守 schema，
  但实测其可用性随网关侧 provider 状态漂移；`glm-4` / `glm-4-air` / `glm-4-flash`
  是本轮唯一三个"schema 合规且在 2000 tokens 内"的备选。
  建议给 dream 的抽取任务固定一个**经过 schema 验证**的模型，而不是跟随默认模型。
  ★ 续轮已把"模型不听话"这一类整体降级了：think 前缀、entity_type 别名、
  超时与输出上限都已修，`minimax-m3` 这类旗舰模型现在也能跑通。
  ★ 但**"模型守 schema"这件事本身仍靠运气** —— 键名别名是按需补的，
  不是有 schema 约束的保证。
  ⚠️ **不要指望网关的 `json_schema` 能兜底**：实测它是**语法透传、不是语义执行**。
  2026-10-20 续测（真实网关，minimax-m3，`strict:true` + 完整 schema +
  `additionalProperties:false`）：网关**照收不误**，但模型**完全无视** ——
  返回内容依然以 `<think>…</think>` 开头（`completion_tokens=2337`，
  `reasoning_tokens=381`）。另有一次 `glm-4` 的 json_schema 返回**不是对象**
  （`completion_tokens=292`），而 `glm-4` 随后 4 次重试全 503。
  ⇒ `adapter/unified/interface.go` 的 `JSONSchema` 类型只保证**能传过去**，
  不保证**对端会执行**。⇒ 结论：**解析层的别名兜底是当前唯一有效手段**，
  别把它换成"我们有 schema 支持所以可以放松解析"。
- ~~**`minimax-m3` 的 `<think>` 前缀**~~ —— **续轮已修**（见上文"续 1"）：
  `internal/llm/chat.go` 新增 `stripThinkBlocks`，并在 `internal/llm/think_prefix_test.go`
  钉住（含"未闭合标签不剥""截断 JSON 仍被拒"两条反向断言）。
- **实体名的表层变体会分裂成多个节点**：`minimax-m3` 抽出 `211 机器` 与 `211机器`、
  `GIN trigram 索引` 与 `GIN索引` 两组，MERGE 键是 `(name, user_id, project_id)`
  精确匹配 ⇒ 变成两个节点。这是 `parseEntities` 里**刻意**的选择
  （代码注释写明"去重用精确 name，因为 WriteEntities 的 MERGE key 是精确匹配"），
  本轮**未改**：改它要动 MERGE 键语义（= 图的节点身份），属产品决策。
  ⇒ 但它意味着"图里的实体数"会随模型/措辞波动，做指标时要按 name 归一再看。
- **读路径的列契约未覆盖**：见上文"门的边界"。
- **acc 库**：`memora` 库的迁移被本轮从 56 推到 60，`graph_*` 三表各多了 `updated_at` 列
  （仅 `graph_entities` 原本就有）；种子数据（project + 12 observations）留在库里未清理，
  便于复核。

## 复现

```bash
# 1) 迁移（镜像内 migrate，DSN 走 acc-pg17）
docker run --rm --network shared-infra --entrypoint /usr/local/bin/kxmemory-migrate \
  -e POSTGRES_HOST=acc-pg17 -e POSTGRES_PORT=5432 -e POSTGRES_DB=memora \
  -e POSTGRES_USER=kxuser -e POSTGRES_PASSWORD=… \
  -e POSTGRES_MIGRATE_USER=postgres -e POSTGRES_MIGRATE_PASSWORD=… \
  memora:amd64-dream-20261019 up

# 2) 旁挂启动（env 从现容器反推，只覆盖 dream 开关）
#    ★ 脚本已固化进 memora 仓（原先只活在 211 的 /tmp，机器清理即失去复现通道）：
bash scripts/prepare-dream-sidecar.sh --model minimax-m3
#    不想真起容器时先看差异（凭据自动打码）：
bash scripts/prepare-dream-sidecar.sh --model minimax-m3 --dry-run
#    离线自检（不需要 Docker）：
bash scripts/prepare-dream-sidecar.sh --self-test

# 3) 闸门表
curl -s http://127.0.0.1:8092/api/dream/config-check | python3 -m json.tool

# 4) 首跑
curl -s -X POST http://127.0.0.1:8092/api/dream/run \
  -H 'Content-Type: application/json' \
  -d '{"user_id":"dream-runner","project_id":"dream-first-run","force":true}'

# 5) 落库真值（两个 GUC 都要设）
docker exec acc-pg17 psql -U kxuser -d memora -At -F'|' -c \
  "set app.tenant_id='default'; set app.project_id='dream-first-run';
   select 'episodes',count(*) from graph_episodes
   union all select 'entities',count(*) from graph_entities
   union all select 'facts',count(*) from graph_facts;"
```

判据复现（memora 仓）：

```bash
go test ./internal/dream/ -run TestExtractionCallCarriesExplicitTimeoutAndMaxTokens -v
go test ./internal/graph/pgstore/ -run TestWriteSQLOnlyReferencesColumnsTheMigrationsCreate -v
go test -tags=integration -count=1 -run TestWriteEntitiesUnderRLS ./internal/graph/pgstore/ -v   # 需要 Docker
go test ./internal/dream/ -run Sidecar -v        # 旁挂脚本的契约门（含两处变异验证）
```

★ **复现通道此前是断的**（2026-10-20 补）：旁挂启动脚本此前只存在于
211 的 `/tmp/prepare_dream_sidecar.sh` ⇒ 机器清理即失去复现通道。
现已固化进 memora 仓 `scripts/prepare-dream-sidecar.sh`（带 `--self-test` 与
`--dry-run`；`--dry-run` 打印 env 差异时**凭据打码**）。
已在 211 实测：新脚本与当时运行中的 `memora-dream`（由 /tmp 原版启动）
**env 键集合完全一致** ⇒ 等价且更严。
★ 固化时自检抓到一处**继承自 /tmp 原版**的真 bug：base env 里若已存在
`LLM_MODEL_DEFAULT`，合并结果会有**重复键**（docker 取最后一个，行为碰巧
正确，但重复 env 是静默埋雷）⇒ 合并时把模型键也按「覆盖」处理。
