# 2026-10-17 24h 审计轮 · 运行日志与慢 SQL 审计

> 取证环境：245（`/opt/llm-gateway-go/current` = release `2696-a4e2480e`，
> 与审计 HEAD `a4e2480ea` 同一提交），PostgreSQL `172.16.2.210:5432/llm_gateway`。
> 量具：`pg_stat_statements`（已 preload）+ `EXPLAIN` + 进程 stdout/stderr 实测 A/B。
>
> 本篇记录两条**已实施并验证**的修复，以及一条**已定位但需发布授权**的建议，
> 外加两个**在过程中被实测推翻**的假设（推翻过程比结论更值得留档）。

---

## 一、已修 ①：`quality-service.error.log` 681MB / 358 万行 INFO 淹没 74 条真实错误

### 根因（三段，缺一不成立）

1. **从未配置 slog**：Go 的 `slog` 默认处理器把所有级别写 **stderr**。
   `cmd/quality-service/main.go` 既没调 `slog.SetDefault`，也没调
   `pkg/logger.SetLevel`（后者写 stdout，但本服务没调）。
2. **systemd 把 stderr 当错误流**：
   `StandardError=append:/opt/llm-gateway-go/logs/quality-service.error.log`，
   `StandardOutput=append:.../quality-service.log`。
3. ⇒ `quality-service.log` **自 7-19 起恒为 0 字节**，全部日志（含 INFO）
   灌进 `.error.log`。

### 实测证据

| 量 | 实测值 |
|---|---|
| `.error.log` 大小 / 行数 | **681 MB / 3,593,726 行** |
| 其中 `availability score calculated`（INFO） | 抽样 20 万行占 **76%** |
| 同期**真实**错误（`connection refused`） | 全文件仅 **74 条** |
| 日增速 | ≈ 6.6 MB/天（199 B/行 × 33,120 行/天） |

**关键判断：磁盘不是问题**（18G 可用 ÷ 6.6MB/天 ≈ 7.5 年），
**信号淹没才是问题**——任何按 `*.error.log` 抓错误的规则或人工排查，
都要先翻过 358 万行 INFO 才看得到那 74 条错。

### 修复

1. **`cmd/quality-service/main.go`**：新增 `configureLogging` + `parseLogLevel`
   + `levelSplitHandler`，按级别分流：Debug/Info → stdout，Warn/Error → stderr，
   并统一为 **JSON**（此前是 Go log 的纯文本 `2026/10/17 10:58:39 INFO …`，
   与本仓其余二进制 `cmd/gateway` 的 `JSONHandler` 不一致，无法直接 jq）。
   新增 `-log-level` 参数（默认 info）。
2. **`internal/quality/scorer_availability.go:98`**：
   `availability score calculated` 由 **Info 降为 Debug**。
   依据：它是**逐项明细**（112 个 provider×model/轮），
   同轮汇总已在 `scheduler.go:130` 的 `quality calculation run completed`
   （duration/success/failed/total）；且本文件入口 `:39` 本来就是 Debug，
   只有结果行是 Info，与自身约定不一致。

### 验证（生产真库 A/B，同一 100s 窗口、同样 2 个调度轮）

| | 旧二进制（现网） | 新二进制 |
|---|---|---|
| stdout（`quality-service.log`） | **0 行** | **12 行** |
| stderr（`quality-service.error.log`） | **236 行** | **0 行** |
| 错误流里有 INFO？ | 是（100%） | **否** |
| 格式 | 纯文本 | JSON |

新二进制 stdout 的 12 行里，运维信号**完整保留**：
`starting quality calculation run` / `found active providers count=112` /
`quality calculation run completed duration=1.74s success=112 failed=0 total=112`。
⇒ 正常运行时**错误流写入 0 字节**，那 74 条真实错误重新可见。

**变异验证 3/3 变红**：Handle 全送 stdout ⇒ 分流判据红；
`With` 只返回单侧 ⇒ 红（主路径每次都过 With，非边角）；
未知 level 默认 error ⇒ `parseLogLevel` 红。
新增判据 `cmd/quality-service/logging_test.go` 3 条（含 With/WithGroup 分流）。

---

## 二、已修 ②：integration gate 漏注入 `LLM_GATEWAY_DATABASE_URL`

`sql/schema` 的 `TestGateInjectsEveryDBCredentialName` 红：
harness 未注入 `LLM_GATEWAY_DATABASE_URL=$GATE_URL`。

该门的设计意图正是「只注入一部分名字会让读该变量的 integration 文件
全部 skip 而门禁仍显示绿」。而 `LLM_GATEWAY_DATABASE_URL` 恰是**本仓真库
变量名**（AGENTS.md 记载 154/245 共用 `172.16.2.210:5432` 即此名），
`admin/` 下 `admin_500_channel_gap_realdb_test.go`、
`data_lifecycle_storage_perf_test.go`、`data_lifecycle_storage_tables_perf_test.go`、
`session_audit_tenant_scope_realdb_test.go`、`routing_resolve_handler_invariant_test.go`
等 realdb 判据都读它。

**修法**：`scripts/audit/run-integration-gate.sh` 的 `env` 块按字母序补一行
`LLM_GATEWAY_DATABASE_URL="$GATE_URL" \`（该门对非 tenant、非 dedicated
的名字一律要求 `$GATE_URL`，这是它自己的规则）。
**变异验证**：删掉该行 ⇒ 门红并打出准确诊断；还原 ⇒ 绿。

---

## 三、已定位但**未实施**（需发布/DB 授权）：`usage_ledger` 月度对账查询 129s/次

### 实测

`pg_stat_statements`（窗口内）：

| mean | max | calls | 累计 | 查询 |
|---|---|---|---|---|
| 129.2s | 533.9s | 87 | **187.3 min** | `WITH deduped AS (SELECT DISTINCT ON (request_id) …)` |
| 98.0s | 582.3s | 131 | 213.9 min | `SELECT analyze_llm_gateway_table_stats($1)` |
| 444.5s | 1313.8s | 7 | 51.9 min | `… CROSS JOIN LATERAL pgstattuple_approx(…)` |

### 根因（有 EXPLAIN 证据，非推测）

`domains/providerprofile/pg_reconciliation_store.go:285` 的月度对账聚合。
月窗（2026-09）的 EXPLAIN：

```
->  Unique
      ->  Incremental Sort  Sort Key: request_id, ts DESC
            Presorted Key: request_id
            ->  Merge Append
                  ->  Index Scan using usage_ledger_2026_09_request_id_idx   (rows=1340927)
                        Filter: (provider_id IS NOT NULL AND ts >= … AND ts < …)
```

分区上有 `usage_ledger_2026_09_request_id_ts_key (request_id, ts)`，
也有 covering 索引 `usage_ledger_2026_09_ts_tenant_id_prompt_tokens_completion__idx
(ts, tenant_id) INCLUDE (…)`。但：
- leading 列是 `request_id` 的索引**不携带** `provider_id/prompt_tokens/
  completion_tokens/total_tokens/cost_usd` ⇒ 134 万行**逐行回表**；
- ts 范围过滤发生在索引扫描**之后**，推不下去。

⇒ 建议索引（可让整条 DISTINCT ON + 聚合走 **Index Only Scan**，零回表）：
`(request_id, ts DESC) INCLUDE (provider_id, prompt_tokens, completion_tokens, total_tokens, cost_usd)`

**为什么不本轮实施**：这是对生产库 1,416,772 行 / 799 MB 分区建索引，
需进迁移通道并占用磁盘与 I/O，属**不可轻易回退**的生产 DDL，
须 owner 授权后随发布执行。本轮只交付定位与 EXPLAIN 证据。

### 附带澄清（避免下轮重走弯路）

- 父表上的 `usage_ledger_ts_tenant_cover_agg_idx` 是 `ON ONLY`、0 字节，
  看似「索引没落到分区」。**实测为假**：分区创建路径确实给每个分区建了
  等价索引（自动改名 `usage_ledger_2026_09_ts_tenant_id_prompt_tokens_completion__idx`）。
  父表那个只是冗余声明，**不是**本查询慢的原因。
- 87 次 ≈ 每日 1 次 × 两台机（默认 interval 86400s），**不是**调度失控。

---

## 四、两个被实测**推翻**的假设（留档以免下轮重犯）

### 假设 A：「`session_turns_2026_10` 从未被 ANALYZE，是覆盖缺口」——推翻

`analyze_llm_gateway_table_stats` 的表名单（正则）里确实**没有** `session_turns`，
初看像是缺口。但实测：

```
session_turns_2026_10 | am=heap | autoanalyze=7 | last_autoanalyze=2026-10-16 13:02:36
request_logs_2026_10 | am=heap | autoanalyze=60 | last_autoanalyze=2026-10-14 03:45:26
request_logs_bodies_2026_10 | am=columnar | autoanalyze=0 | last_analyze=2026-10-14 13:09:48
```

**错在把 `last_analyze`（手工 ANALYZE）当成了 `last_autoanalyze`。**
前两张是 heap，autovacuum 正常采集（autoanalyze 7 / 60 次）；
只有列存分区 `request_logs_bodies_2026_10` 需要手工 ANALYZE，
而 856 的 6 小时陈旧门**正在生效**（last_analyze 10-14、n_mod_since_analyze=0）。
⇒ 不是缺口，无须修。

### 假设 B：「681MB 是磁盘风险」——推翻

日增速约 6.6 MB/天，18 GB 可用 ⇒ 约 7.5 年。**风险不在磁盘，
在告警信号被淹没**。这也是本条修复的真实动因——若按「磁盘要满」定级，
会得出错误的优化方向（去加轮转），而轮转只会让错误**更难**被翻到。

### 顺带：`analyze_llm_gateway_table_stats` 判定为「已缓解，非新缺陷」

累计 213.9 min 看着最大，但它已有三重治理：50 分钟跨实例节流槽
（`bg/partition_manager.go:1695`，锁防同时 + 槽防重复）、
838/839 的统计职责移交 autovacuum、856 的列存分区 6 小时陈旧门。
实测门在生效。**本轮不动它**——把它当"新慢 SQL"去优化会与既有治理打架。

---

## 五、本轮新增的可复用判据纪律

**第 6 条：`last_analyze` ≠ `last_autoanalyze`。**
判断「某表有没有被统计过」时，前者只记手工 `ANALYZE`、后者才记 autovacuum。
用错列会把「autovacuum 正常工作的 heap 表」误判成「从未统计的缺口」。
（假设 A 即栽在这里，且是靠再查一列 `autoanalyze_count` 才推翻的。）

**第 7 条：统计信息的「无索引」初判要先验证分区继承。**
父表 `ON ONLY` 的 0 字节索引看起来像「索引没建到分区」，
但分区创建路径可能已各自建了等价索引（自动改名）。
下结论前必须 `pg_index` 逐分区列一次，否则会把一个**不存在的缺陷**
当成优化目标。（第三节的「附带澄清」即此。）

**第 8 条：给日志/指标定级时，先分清「资源风险」与「信号风险」。**
681MB 日增 6.6MB/天不是磁盘问题，却是严重的可观测性问题。
按资源口径定级会导向「加轮转」——而轮转让错误更难被翻到，方向恰好相反。
