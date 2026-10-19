# dream cycle 首跑：接真实 LLM 网关（2026-10-19）

> 任务：把 memora 的 dream cycle 在**真实网关**上跑通一次（禁用 llm-mock），
> 并验证产出是**真落库**的图数据，而不是一个"成功形状"的 stub。
> 关联仓：`/Users/niuer/workspace/ai-native-tools/memora`（本文涉及的 memora 改动）。

## 结论

**首跑成功**，且首跑本身暴露了**三个真缺陷**（不是环境噪声，都是代码与真实环境不匹配）：

| # | 现象 | 真因 | 修法 |
|---|---|---|---|
| 1 | 抽取超时 `context deadline exceeded` | `llm.Client` 默认超时 **20s**，而真实网关对抽取 prompt 实测要 **26.2s / 26.9s** | 引擎显式给 `Timeout: 120s` |
| 2 | `LLM gateway returned malformed JSON` | `max_tokens=2000` 把输出**截断在 JSON 中间**（同批数据实际 `completion_tokens=2982`） | 引擎显式给 `MaxTokens: 8000` |
| 3 | `graph_episodes.updated_at does not exist` (42703) | 000024 只给 `graph_entities` 声明了 `updated_at`，另两张表没有；而 pgstore 三处 `ON CONFLICT DO UPDATE SET updated_at = NOW()` | 新增迁移 **000060** 补两列 |
| 4 | 实体写入 25P02、但事实写进去了 | pgstore 用 `CopyFrom` 走快路径，而 **PG 不支持在启用 RLS 的表上 COPY FROM**；回退又跑在**已被中止的事务**里 ⇒ 真实根因被掩盖 | 去掉 CopyFrom，直接走 `INSERT … ON CONFLICT` |

最终首跑（真实网关 `https://llm.kxpms.cn`，模型 `sensenova-6.8-flash-lite`）：

```json
{"status":"completed","observations":12,
 "episode_uuid":"7115dfed-3757-4e21-8f59-c752e2e085fe",
 "entities_written":22,"facts_written":17,"duration_ms":12452}
```

PG 侧真值（`acc-pg17` / 库 `memora`）：**1 episode + 22 entities + 17 facts**，内容语义正确
（`GIN trigram 索引` / `ScopeRedact` / `ScopeEgress` / `planner` / `duration_ms` / `search_text` …）。

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

## 遗留与边界

- **旁挂容器未纳入 compose**：`memora-dream` 目前是 211 上的独立容器
  （`docker rm -f memora-dream` 即可撤）。若要长期启用 dream cycle，
  应把它并进 `docker-compose.platform.yml` 的生成流程（该文件由 `gen-platform-compose.py`
  从运行容器反推生成 ⇒ 改完必须跑 `verify-compose-equivalence.py`）。
- **模型选型仍是脆弱点**：`sensenova-6.8-flash-lite` 本轮可用且守 schema，
  但实测其可用性随网关侧 provider 状态漂移；`glm-4` / `glm-4-air` / `glm-4-flash`
  是本轮唯一三个"schema 合规且在 2000 tokens 内"的备选。
  建议给 dream 的抽取任务固定一个**经过 schema 验证**的模型，而不是跟随默认模型。
- **`minimax-m3` 的 `<think>` 前缀**：`internal/llm/chat.go` 的 `stripCodeFences` 只剥
  ```json 围栏，不处理 `<think>…</think>` ⇒ 该模型在本仓的所有 `ChatJSON` 调用上都会
  解析失败。本轮未改（属另一条独立改动），但它会让"换个模型就好"的直觉失效。
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
bash /tmp/prepare_dream_sidecar.sh sensenova-6.8-flash-lite

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
```
