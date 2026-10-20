# 会话族死表精确清单（2026-10-20 审计 G-6 复核轮）

> 本清单回答一个问题：**会话族里哪些表还在被人用，哪些只剩「写得进去、读不出来」，
> 哪些连写方都没有。**
>
> 结论先行（判定口径见 §2）：
>
> | 表 | 写方 | 读方 | 判定 | 处置 |
> |---|---|---|---|---|
> | `public.session_turn_snapshots` | **无** | **无** | **完全死表**（只剩 DDL + 5 条索引 + 2 条 RLS 策略） | 保留不动，见 §4 |
> | `public.session_last_requests` | **无**（SQL 函数有写方但**零调用方**） | **无** | **完全死表**（表 + 3 个函数 + 2 个视图全是孤岛） | 保留不动，见 §4 |
> | `public.session_memora` / `_hot` | **有**（706 写链，live） | **无** | **只写不读**（审计台账，无消费者） | 保留不动，见 §4 |
> | `public.session_censors` / `_hot` | **有**（脱敏映射双写，live） | **无**（Redis 是热缓存，DB 无回读） | **只写不读** | 保留不动，见 §4 |
>
> ★ 本轮**不删任何表**（用户明确要求）。本文只把「谁在用 / 谁没用」写成
>   **可复核的清单**，让下一位维护者不用重新做一遍全仓 grep。

---

## 1. 普查方法（可复核）

```bash
# 每个候选表名在 Go / SQL / shell 里的**全部**命中
grep -rn "\b<table>\b" --include=*.go --include=*.sql --include=*.sh . \
  | grep -v "_test.go" | grep -v "^./docs/"
```

命中项逐条归类为四类之一：**Go 读方** / **Go 写方** / **SQL 调用方** /
**仅为 DDL 自身或注释**。索引清单取自**真库** `pg_index`（不是从迁移文件推的，
因为父表上的 `ON ONLY` 壳与分区上的实际索引是两个东西）。

行数取自**本机开发库**（docker 容器 `llm-gateway-pg`，PG 17.10，库 `llm_gateway`）。
⚠ **生产 245 / 252 本轮无访问权限**，生产行数与索引可能与本机不同 ⇒ 本文里的
行数只用于「本机是否有流量」的旁证，**不作为生产结论**。

---

## 2. 判定口径

| 判定 | 含义 |
|---|---|
| **完全死表** | 无 Go 写方、无 Go 读方、无 SQL 调用方（DDL/索引/RLS 之外无引用） |
| **只写不读** | 有活跃写方，但**全仓没有任何读方**（数据只进不出） |
| **活表** | 有写方且有读方 |

⚠ 「只写不读」不等于「可以删」：两者都是**审计台账**（保留证据链），
   处置口径见 §4。

---

## 3. 逐表清单

### 3.1 `public.session_turn_snapshots` —— 完全死表

| 项 | 内容 |
|---|---|
| **DDL 出处** | `sql/migrations/startup/385_session_turn_snapshots.sql`（表 + 2 索引 + 2 条 RLS 策略 + 表注释） |
| **Go 读方** | **无**。全仓 `*.go` 对 `session_turn_snapshots` / `TurnSnapshot` **零命中** |
| **Go 写方** | **无**（同上，零命中） |
| **SQL 调用方** | **无**。385 之外没有任何迁移引用它；**也没有清理函数** —— 表有 `expires_at` 列和 `idx_session_turn_snapshots_expiry` 索引，但**没有任何作业按 expires_at 清它** ⇒ 即便有数据也只会一直堆着 |
| **索引清单（真库）** | `session_turn_snapshots_pkey` UNIQUE (id)<br>`idx_session_turn_snapshots_session` (tenant_id, gw_session_id, turn_no DESC)<br>`idx_session_turn_snapshots_expiry` (expires_at)<br>`uq_session_turn_snapshot` UNIQUE (tenant_id, gw_session_id, turn_no)<br>`uq_session_turn_snapshot_request` UNIQUE (tenant_id, request_id) |
| **本机行数** | 0 |
| **建它的意图（注释原文）** | `385:2-4`「Per-turn audit snapshots for original, compressed and security-processed conversation stages. One row always represents one request round so all six directions remain aligned and can be evicted as a unit.」 |
| **判定** | **完全死表**。六向快照（original/compressed/secured × send/receive）的写入方在后续重构中消失，写入点与清理点**一起**没了 —— 表连同它的 `expires_at` 语义一起成了孤儿 |

### 3.2 `public.session_last_requests` —— 完全死表

| 项 | 内容 |
|---|---|
| **DDL 出处** | ⚠ **不在启动迁移链里**：`sql/migrations/timeout-optimization/003_create_session_last_requests.sql`（表 + 3 个函数 + 2 个视图）；基线快照 `sql/schema/01-schema.sql` 里也带着它们 |
| **Go 读方** | **无**。唯一带该表名的 Go 命中是 `bg/metrics.go:394`（`hotTableTSColumn` 里的一条 ts 列映射），而该映射只被 `bg/partition_manager.go:1853` 的 promote 循环使用 —— **`session_last_requests` 不在 `promoteSpecs()` 名单里**（`bg/partition_manager.go:1600-1618`）⇒ 这条映射**永远走不到**，是一条**死映射**（不是读方） |
| **Go 写方** | **无** |
| **SQL 调用方** | **有函数、零调用方**。三个函数全部只被自己那一族引用：<br>· `public.upsert_session_last_request(...)`（INSERT）—— 全仓除 `sql/migrations/timeout-optimization/003_*.sql` 与 `sql/objects/functions/upsert_session_last_request_*.sql` 外**无调用方**<br>· `public.get_session_last_request(...)`（SELECT）—— 同上，**零调用方**<br>· `public.cleanup_expired_session_requests()`（DELETE）—— 同上，**零调用方**<br>两个视图 `v_session_cache_stats` / `v_session_cache_by_model` 只读本表，也**无调用方** |
| **Go 侧的旁证** | `admin/session_online.go:12` 的注释明确写着「旧链 `session_last_requests ⋈ request_logs_with_current_month` 在 S4 **停写**后…」⇒ 写链确实在 S4 被摘掉了，但**表和函数没跟着摘** |
| **索引清单（真库）** | `session_last_requests_pkey` UNIQUE (session_id)<br>`idx_session_last_requests_expires` (expires_at)<br>`idx_session_last_requests_status` (last_request_status, updated_at DESC)<br>另有触发器 `trigger_session_last_requests_updated_at` |
| **本机行数** | 0 |
| **判定** | **完全死表**（表 + 3 函数 + 2 视图构成一个自洽但**无人进出**的孤岛） |

### 3.3 `public.session_memora` / `public.session_memora_hot` —— 只写不读

| 项 | 内容 |
|---|---|
| **DDL 出处** | `sql/migrations/startup/706_session_family_s1a.sql:31`（父表 + hot 表 + 索引 + RLS + 注释 + `promote_session_tools…` 同型的 promote 函数 + `ensure_session_family_partitions`） |
| **Go 写方** | **有，且是活链路**：`domains/session/v2/family_writers.go:120` 的 `WriteMemoraSnapshotInTx` → `INSERT INTO public.session_memora_hot`；接线点 `cmd/gateway/session_v2_init.go:52`（`writer.SetMemoraWriter(v2.NewSessionMemoraWriter(pool))`）；触发条件 `domains/session/v2/session_writer_v2.go:766`（`turnNo == 1` ⇒ **每会话只写首轮**） |
| **Go 读方** | **无**。全仓 `FROM public.session_memora` / `JOIN public.session_memora` **零命中**（⚠ 注意别把 `session_memora_extraction_log` 误算进来 —— 那是**另一张表**，在 `admin/session_extract.go` / `admin/memora_handlers.go` / `admin/no_topic_session.go` 里被读） |
| **SQL 调用方** | `promote_session_memora_hot_to_partition`（706）+ `bg/partition_manager.go:1614` 的 promote 规格；`internal/partguard/parents.go:39` 把 `session_memora` 登记为**分区父表**（这是**结构登记**，不是读方） |
| **索引清单（真库）** | 父表：`session_memora_pkey` UNIQUE (id, partition_date) / `idx_session_memora_session` (session_id, partition_date DESC) / `session_memora_tenant_id_session_id_partition_date_key` UNIQUE<br>hot 表：`session_memora_hot_pkey` UNIQUE (id) / `idx_session_memora_hot_session` (session_id, partition_date DESC) / `idx_session_memora_hot_ts` (created_at) / `session_memora_hot_uq` UNIQUE (tenant_id, session_id, partition_date) |
| **本机行数** | 父表 964 / hot 表 439（⇒ 写链在本机**确实在跑**） |
| **判定** | **只写不读**：数据只进不出，是纯审计台账 |

### 3.4 `public.session_censors` / `public.session_censors_hot` —— 只写不读

| 项 | 内容 |
|---|---|
| **DDL 出处** | `sql/migrations/startup/706_session_family_s1a.sql:65`（父表 + hot 表 + 索引 + RLS + 注释 + promote 函数） |
| **Go 写方** | **有，且是活链路**：`security/sanitize/db_sink.go:133` 的 `INSERT INTO public.session_censors_hot`（脱敏占位符→原值的可逆映射双写）；接线点 `cmd/gateway/goal_control.go:516`（`mw.SetCensorSink(sanitize.NewPostgresCensorSink(db))`） |
| **Go 读方** | **无（DB 侧）**。全仓 `FROM public.session_censors` / `JOIN public.session_censors` **零命中**。`security/sanitize/smart_sani_guard.go:102` 的注释明说「（public.session_censors），**Redis 降级为热缓存**」⇒ 还原走 Redis，DB 是**兜底副本**而不是热路径 |
| **SQL 调用方** | `promote_session_censors_hot_to_partition`（706）+ `bg/partition_manager.go:1615` 的 promote 规格；`internal/partguard/parents.go:38` 的分区父表登记 |
| **索引清单（真库）** | 父表：`session_censors_pkey` UNIQUE (id, partition_date) / `idx_session_censors_session` (tenant_id, session_id, partition_date) / `idx_session_censors_request` (request_id, partition_date) WHERE request_id IS NOT NULL<br>hot 表：`session_censors_hot_pkey` UNIQUE (id) / `idx_session_censors_hot_session` (tenant_id, session_id, partition_date) / `idx_session_censors_hot_ts` (created_at) |
| **本机行数** | 父表 0 / hot 表 0 |
| **判定** | **只写不读**（本机无脱敏流量所以为 0；写链路本身是活的） |

---

## 4. 处置口径（本轮**不删**）

用户明确要求：**只出清单，不删表**。理由也成立 —— 这四张表里有三张是
**审计/取证台账**（memora 首轮快照、censors 脱敏映射、turn_snapshots 六向快照），
删表等于删证据链；而 `session_last_requests` 虽无调用方，但它的函数是
**公开 API 形态**（`upsert_session_last_request(...)` 签名稳定），
第三方/运维脚本可能在库外调用。

⇒ 因此本轮的交付是**清单 + 风险记录**，不是删除动作。
若将来决定删除，建议的顺序是：

1. 先确认**库外**调用方（运维脚本、psql 手册、外部 BI）；
2. 先删**视图与函数**（`v_session_cache_*` / `upsert_session_last_request` 等），
   让调用方以「函数不存在」的方式**显式失败**，而不是静默写进一张没人读的表；
3. 再删表；
4. 顺带清掉 `bg/metrics.go:394` 的**死映射**（`session_last_requests` 不在
   `promoteSpecs()` 里，那条 ts 列映射永远走不到）。

---

## 5. ★ `session_tools.turn_no` 恒为 NULL —— turn→tool join 丢行风险

### 5.1 事实

| 项 | 内容 |
|---|---|
| **列** | `public.session_tools.turn_no INT` / `public.session_tools_hot.turn_no INT`（706 建列，**可空**） |
| **写方** | `domains/toolexecution/postgres_store.go:61` 的 `Save`：INSERT 列清单是 `(execution_id, session_id, request_id, tenant_id, tool_name, tool_call_id, arguments, result, status, error_message, error_type, started_at, completed_at, duration_ms, identity_hash, model, created_at, partition_date)` —— **没有 `turn_no`** |
| **结果** | **100% 的行为 NULL** |

### 5.2 风险形态

任何按 `(session_id, turn_no)` 关联轮次与工具的查询会**丢掉全部行**：
SQL 里 `NULL = 任何值` 结果是 **NULL**（不是 TRUE），WHERE 只保留 TRUE ⇒
**没有一行能匹配上**。

⚠ 这类丢行**不会报错**：查询正常返回，只是**少了一侧的全部数据**
（LEFT JOIN ⇒ 工具列全 NULL；INNER JOIN ⇒ 整行消失）。

### 5.3 可用的连接键

`(session_id, request_id)` —— 两列都 `NOT NULL`，且 `session_tools_hot` 上有
`idx_session_tools_hot_session (session_id, started_at DESC)`
⇒ `session_id` 是索引前缀。

★ 这正是 G-6 的 `tools` 填充用 `(session_id, request_id)` 而不是
`(session_id, turn_no)` 的原因（`domains/session/v2/turn_writer.go` 的
`turnToolsJSONBExpr`）。

### 5.4 为什么**补不齐**（结构性问题，不是漏写）

工具行是在**请求进行中**写的（`domains/hooks/toolexecution/hook.go` 的
`BeforeToolCall` → `Tracker.RecordStart` → `store.Save`），
而**轮次行是在请求结束时**才写的 —— 写工具行的那一刻，`turn_no` **还没定**
⇒ 结构上无法在写工具行的当时填上正确的 `turn_no`。

补齐的可行路径（本轮**未做**）：

1. 工具写方在**同一请求内**延迟到轮次号确定后再回填 `turn_no`（需引入
   请求级上下文 ⇒ 跨包改动）；
2. 或在轮次写入时按 `(session_id, request_id)` 反查工具行**并回写**
   `turn_no`（需一条 UPDATE，且要处理「工具行已被 promote 搬走」的情况）。

⇒ 因此本轮只在**列注释**里把这条风险写死（迁移 889，
`COMMENT ON COLUMN public.session_tools.turn_no`），并由静态门
`sql/migrations/startup/session_turns_tools_population_test.go`
的 `TestToolStoreNeverWritesTurnNo` 钉住「写方确实不写这一列」。

---

## 6. 相关文件

| 用途 | 路径 |
|---|---|
| G-6 裁决迁移（列注释订正 + 决策记录） | `sql/migrations/startup/889_session_turns_tools_population.sql` |
| 同上回滚 | `sql/migrations/startup/889_session_turns_tools_population.down.sql` |
| G-6 静态契约门 | `sql/migrations/startup/session_turns_tools_population_test.go` |
| G-6 写路径门 | `domains/session/v2/turn_tools_test.go` |
| G-6 真库门（opt-in） | `domains/session/v2/turn_tools_realdb_test.go` |
| 基线同步门 | `sql/schema/session_turns_tools_comment_test.go` |
| `tools` 填充实现 | `domains/session/v2/turn_writer.go`（`turnToolsJSONBExpr`） |
