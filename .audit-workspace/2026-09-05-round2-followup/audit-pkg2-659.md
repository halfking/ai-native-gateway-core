# 审计报告：迁移 659（七张遗留 hot 表 promote 原子化）— pkg2 只读审计

- 审计对象：commit `203021f33`，`sql/migrations/startup/659_legacy_promote_atomic_cte.sql`（up, 497 行）+ `.down.sql`（314 行）+ `migration_659_test.go`（160 行）+ `docs/db-changelog.md` 659 行
- 审计方式：逐函数体人工核对 + 本地真实 PG（容器 llm-gateway-pg，llm_gateway@llm_gateway）information_schema / pg_indexes / pg_constraint / pg_get_functiondef 只读交叉验证；psql 全程只做 SELECT 类查询，未执行 659、未调函数、未写库
- 总评：**迁移核心质量高**。列清单 7/7 逐位对齐，批次键 5/7 有唯一性强制、2 个无强制（与旧体语义相同、fail-safe），ensure 粒度 7/7 正确，down 7/7 与历史原文逐字一致（去空白/限定符规范化后），契约测试通过。发现 2 个 P2（changelog SHA 记账错、usage_ledger_hot 批次键无唯一强制）、4 个 P3。

---

## 一、逐表核对表（列清单 / 批次键 / ensure 粒度 三项判定）

证据来源：`information_schema.columns`（hot+父表实际列序）、`pg_indexes`/`pg_constraint`（hot 表唯一性与父表约束）、`pg_get_partkeydef`（父表分区键）、`pg_get_functiondef`（ensure 实现）。

### 1. usage_ledger / usage_ledger_hot
| 项 | 判定 | 证据 |
|---|---|---|
| 列清单 | **PASS** | 父表 19 列，迁移 RETURNING（659:88-91）与 INSERT（659:93-101）逐位对齐且与 `information_schema.columns` 序一致。hot 表 24 列 = 19 共有 + 5 个多模态列（reasoning/image/audio/video/provider_tokens，第 20-24 位）**有意排除**，迁移头 659:45-49 已声明（父表无对应列，`*_with_current_month` 视图也不暴露）|
| 批次键 | **P2-2**（见发现清单）| hot 表**无任何唯一索引**（仅 4 个非唯一索引；`pg_constraint` 返回空）。DELETE join `(request_id, ts)`（659:87）在 hot 上无强制唯一。父表有 `usage_ledger_partitioned_request_id_ts_key UNIQUE (request_id, ts)` |
| ensure 粒度 | **PASS** | 父表 `RANGE (ts)`；预 ensure（659:74-77）与 batch CTE（659:83）同谓词 `ts < now() - p_retention`，同源列 ts；`ensure_usage_ledger_partition(timestamptz)` 内部自做 `date_trunc('month', target)`，传 month_start 幂等正确 |

### 2. request_wal / request_wal_hot
| 项 | 判定 | 证据 |
|---|---|---|
| 列清单 | **PASS** | 两表均 17 列且完全同序；迁移 RETURNING（659:138-142）/INSERT（659:144-154）逐位一致 |
| 批次键 | **PASS** | hot `request_wal_hot_pkey PRIMARY KEY (request_id)`（另有冗余 `udx_request_wal_hot_request_id UNIQUE (request_id)`）；join `(request_id, created_at)`（659:137）是唯一键的超集 → 唯一安全 |
| ensure 粒度 | **PASS** | 父表 `RANGE (created_at)`；预 ensure（659:124-127）与 batch（659:133）同谓词 created_at；ensure 内部自做 date_trunc（"找所在月"语义），传 month_start 正确 |

### 3. routing_decision_log / routing_decision_log_hot
| 项 | 判定 | 证据 |
|---|---|---|
| 列清单 | **PASS** | 两表均 36 列同序；迁移 RETURNING（659:191-199）/INSERT（659:201-219）36 列逐位一致 |
| 批次键 | **PASS** | hot 有**两个**完全相同的 `UNIQUE (request_id, ts)`（`idx_routing_decision_log_hot_request_ts` 与 `routing_decision_log_hot_request_id_ts_key`）；join `(request_id, ts)`（659:190）精确等于唯一键。父表无约束（与迁移头 659:164 声明一致）|
| ensure 粒度 | **PASS** | 父表 `RANGE (ts)`；预 ensure/batch 同谓词 ts |

### 4. credential_model_index / credential_model_index_hot
| 项 | 判定 | 证据 |
|---|---|---|
| 列清单 | **PASS** | 两表均 17 列同序；迁移 RETURNING（659:258-261）/INSERT（659:263-271）逐位一致 |
| 批次键 | **PASS** | hot `credential_model_index_hot_bucket_credential_id_raw_model_idx UNIQUE (bucket, credential_id, raw_model)`；join 三列（659:257）精确等于唯一键。父表无约束（与 659:230 一致）|
| ensure 粒度 | **PASS（重点核对项）** | 父表 `RANGE (bucket)`，保留谓词是 `updated_at`：预 ensure（659:243-247）正确地用 `date_trunc('month', bucket)` 建分区（而非 updated_at），batch 谓词 `updated_at < now() - p_retention`（659:253）与 399 旧体一致。谓词列与分区键分离但 ensure 取列正确 |

### 5. tool_usage_stats / tool_usage_stats_hot
| 项 | 判定 | 证据 |
|---|---|---|
| 列清单 | **PASS** | 两表均 11 列同序；迁移 RETURNING（659:311-313）/INSERT（659:315-321）逐位一致（id/created_at 显式携带，满足父表 PK）|
| 批次键 | **PASS**（brief 中怀疑的"PK 含 created_at"与实库不符）| hot `tool_usage_stats_hot_tool_tenant_date_key UNIQUE (tool_id, tenant_id, usage_date)`，**hot 无 PK**；join 三列（659:310）精确等于 hot 唯一键。父表 PK `(id, created_at)` + `UNIQUE (tool_id, tenant_id, usage_date, created_at)` |
| ensure 粒度 | **PASS（重点核对项）** | 父表 `RANGE (created_at)`，保留谓词是 `usage_date`：预 ensure（659:296-300）正确用 `date_trunc('month', created_at)`，batch 谓词 `usage_date < CURRENT_DATE - p_retention`（659:306）与 348 旧体同源（CURRENT_DATE 粒度一致，预 ensure 与 batch 同表达式）|

### 6. credit_ledger / credit_ledger_hot
| 项 | 判定 | 证据 |
|---|---|---|
| 列清单 | **PASS** | 两表均 10 列同序；迁移 RETURNING（659:358-359）/INSERT（659:361-365）逐位一致 |
| 批次键 | **P3-3**（见发现清单）| hot **无任何唯一索引**（6 个索引全非唯一，`pg_constraint` 空）。join `(id)`（659:357）无强制唯一；但 hot/父表共享同一序列 `credit_ledger_partitioned_id_seq`（column_default 证据），写侧 `maas/service.go:665` 不提供 id → 实际唯一。父表 PK `(id, created_at)` |
| ensure 粒度 | **PASS** | 父表 `RANGE (created_at)`；预 ensure/batch 同谓词 created_at |

### 7. request_logs_bodies / request_logs_bodies_hot
| 项 | 判定 | 证据 |
|---|---|---|
| 列清单 | **PASS** | 父表 5 列，hot 6 列（多 `tenant_id` 第 6 位）**有意排除**，迁移头 659:45-49 已声明；RETURNING（659:446）/INSERT（659:448-450）5 列逐位一致 |
| 批次键 | **PASS** | hot `idx_request_logs_bodies_hot_request_id UNIQUE (request_id)`；join `request_id`（659:445）精确等于唯一键。父表无约束（与 659:375 一致）|
| ensure 粒度 | **PASS** | 父表 `RANGE (ts)`；预 ensure/batch 同谓词 ts；Phase-1 TTL 删除谓词与 528 逐字相同 |

---

## 二、发现清单

### P2-1 changelog 659 行 SHA-256 与提交文件不匹配（记账错误，同提交内自相矛盾）
- 证据：`docs/db-changelog.md:292` 记录 `ea5476af92d859768d60af34e28764528ae10fe1616fa441c790f3a19927e1ea`；实际（工作区与 `git show 203021f33` 一致）= `ac7794c4b7982cb88b5fb8912516c27eec3ef76c0b39bd672a0e0135a119e472`。changelog 条目与文件在**同一提交** 203021f33 内引入，却互相矛盾。
- 时间线：DB `schema_migrations` 659 行 applied_at `2026-09-05 17:24:01+08`，changelog 时间戳 `09:24:51Z`（=17:24:51+08），提交时刻 17:42:58+08——应用在先、提交在后，期间文件又被编辑但 changelog 未同步。
- **无内容漂移**：已将 DB 中 7 个函数的 `pg_get_functiondef` 与提交文件做规范化 diff，7/7 逐字一致（唯一差异是 pg_get_functiondef 不保留 `$$;` 的末尾分号），即 DB 应用版与提交版功能等同，差异至多为注释/头部级。
- 同类既有问题：656 行（changelog `5261ec20` vs HEAD 文件 `d286e42f`，系 wip 提交 b2442aa63 改文件未更账）。657/658 行均精确匹配。
- 影响：SHA 记录失去了"审计产物业已应用"的凭证作用；未来按 changelog SHA 校验文件会误报。
- 修法：将 659 行 SHA 更新为 `ac7794c4…`；顺手核对/修正 656 行。

### P2-2 usage_ledger_hot 批次 join 键 `(request_id, ts)` 无唯一性强制（批次上界语义无保障 + 潜在卡批）
- 证据：`pg_indexes` 中 usage_ledger_hot 仅 4 个**非唯一**索引；`pg_constraint` 无任何约束。而 659:60 注释声称 "hot keys: (request_id, ts)"，659:86-87 DELETE `USING batch b WHERE h.request_id = b.request_id AND h.ts = b.ts`。
- 危害链：若 hot 出现重复 `(request_id, ts)`（写侧重试等），① batch LIMIT 选 N 行但 DELETE 会连带删除未入批的同键行（moved > LIMIT）；② 两行同键同时 INSERT 触发父表 `usage_ledger_partitioned_request_id_ts_key` 唯一冲突 → **整语句回滚**。相比旧体（DELETE 已提交 + EXCEPTION 吞错 = 整批静默丢失），659 是 fail-safe（行保留、错误上抛，明确更优）；但同 `ORDER BY ts, request_id` 每次选中同一批 → promote 对该表**永久报错卡死**，需要人工清重。
- 实际概率：request_id 为服务端中间件生成、全局唯一（455:13 的论证），且重试写入 ts 大概率不同 → 概率低。
- 非回归：399 旧体 `DELETE ... WHERE (request_id, ts) IN (batch)` 语义完全相同。
- 修法：迁移中 `CREATE UNIQUE INDEX` on usage_ledger_hot (request_id, ts)（先查重清理）；或在无唯一键的 hot 表上统一改用 `ctid` 批（batch SELECT ctid ... FOR UPDATE，DELETE ... WHERE h.ctid = b.ctid RETURNING 显式列）——ctid 方案同时覆盖 P3-3。

### P3-3 credit_ledger_hot 批次 join 键 `(id)` 无唯一性强制（同 P2-2 同类，实际风险更低）
- 证据：credit_ledger_hot 无 PK/UNIQUE；`credit_ledger_hot.id` 与 `credit_ledger.id` 共享 `credit_ledger_partitioned_id_seq`（`information_schema.columns.column_default`），写侧 `maas/service.go:665` 不显式供 id → 序列保证实际唯一；旧体 349 亦按 `id IN (batch)` 删除，语义相同。
- 修法：与 P2-2 合并处理（唯一索引或 ctid 批），可低优先。

### P3-4 down #5（tool_usage_stats）在现 schema 上 `ON CONFLICT` 必然 42P10，且被还原的吞错 handler 转为丢行
- 证据：down:193 `ON CONFLICT (tool_id, tenant_id, usage_date) DO NOTHING`（忠实还原 348:164）；现父表唯一约束是 4 列 `tool_usage_stats_partitioned_tool_id_tenant_id_usage_date_c_key UNIQUE (tool_id, tenant_id, usage_date, created_at)`。3 列推断列表无法匹配 4 列唯一索引 → `there is no unique or exclusion constraint matching the ON CONFLICT specification`（42P10），发生在还原体的 `EXCEPTION WHEN OTHERS` 内 → DELETE 已生效、n:=0（正是 down 头部警告的丢失机制）。348 时代父表唯一键大概率为 3 列（分表改造后必须含分区键 created_at）。
- 影响：down 头部已有总 Warning 并禁止用于生产，但未指出 #5 属"必败体"；应急回滚者会踩坑。
- 修法：不建议改体（破坏"逐字还原"）；在 down #5 注释追加一行"现 schema 唯一键为 4 列，本 ON CONFLICT 会 42P10 进入丢失路径"。

### P3-5 预 ensure 循环 `LIMIT 12` 边界可造成跨月积压卡批（7 函数共有，承袭 656 模板）
- 证据：659:77/127/180/247/300/347/434 `ORDER BY 1 LIMIT 12`（只 ensure 最旧 12 个不同月份）；656:127 同款。若冷积压 >12 个月且最旧 12 个月合计行数 < p_batch_size，batch 会触及未 ensure 的第 13 个月 → 23514（no partition found）→ 整语句回滚，且每次重选同一批 → 永久卡死。
- 现实约束：7 天保留 + promote 自 2026-07-13 才开始积压（usage_ledger 旧体必败），当前积压约 2 个月，12 个月边界远不可达。
- 修法：低优先。改为先 `SELECT count(DISTINCT month)` 决定循环上限，或去掉 LIMIT 改游标循环。

### P3-6 down "逐字还原"的残余差异（零行为差异，仅记录）
- 证据（机械化 squash-diff：去注释、去 `public.` 限定符、去空白后 7/7 IDENTICAL）：down 相对历史原文的差异仅为 ① 函数名加 `public.` 限定符；② 省略 348/349 体内 3 处中文行内注释；③ CREATE 行排版。down 头部 WARNING（down:1-12）存在且明确。down 与 `DELETE FROM public.schema_migrations WHERE version='659'`（down:312）符合 656 down:36 惯例。
- 结论：**down 保真 PASS**。7 个体来源链核实无误：usage_ledger/routing/credential←399（>354，最后重装）、request_wal←345、tool_usage_stats←348、credit_ledger←349、bodies←528（>455>399>353，最后重装；481/562/602 均未重装该 7 函数，481:11/562:14 仅注释提及）。

---

## 三、其余审计项结论（无定级 PASS / 观察）

1. **原子性与错误路径 PASS**：7 个体均为单条 `WITH batch…moved_rows(DELETE RETURNING 显式列)…inserted(INSERT…RETURNING)SELECT count(*)` 数据修改 CTE；两个 RAISE EXCEPTION 守卫在 CTE 之前（retention>0、batch 1..50000）；全文无 `EXCEPTION WHEN`/`RAISE WARNING`/temp table/`SELECT *`；now() 在预 ensure 与 batch 同谓词复用（tool_usage_stats 用 CURRENT_DATE，与其保留谓词列 usage_date 的日期粒度同源）。DELETE+INSERT 同语句原子成立；bodies Phase-1 TTL 删除为有意保留的 528 两阶段契约（528 谓词逐字一致）。
2. **ensure 签名匹配 PASS**：7 个 ensure 现库签名均为 `(timestamptz)`，全部内部自做 `date_trunc('month', target)`（"找时间戳所在月"语义），传 month_start 幂等正确；7 个分区键（ts/created_at/ts/bucket/created_at/created_at/ts）与 ensure 传参列一一对应（含 credential_model_index 的 updated_at 谓词/bucket 分区键、tool_usage_stats 的 usage_date 谓词/created_at 分区键两组易错分离，均正确）。无 305/562 vs 330/319/475 的粒度错配风险——所有 ensure 实现现已统一为自截断式。
3. **schema_migrations 登记 PASS**：up:493-495 与 656:176 同款 `INSERT … ON CONFLICT (version) DO NOTHING`；down:312 与 656 down:36 同款 DELETE；`migration_version_unique_test` 兼容（659 文件名唯一）；实库 659 行存在（applied_at 17:24:01+08）。实库 7 函数体含 `FOR UPDATE SKIP LOCKED`、不含 `WHEN OTHERS`/temp table（抽查 4 项全过），up 的 `DO $verify_659$` 运行时校验有效。
4. **契约测试通过但存在盲区**（`migration_659_test.go`，go test -run TestMigration659 → ok）：能拦住 模式回退（temp table/SELECT */EXCEPTION/ON CONFLICT/守卫缺失/函数数不符）。**拦不住**：① 列清单错位/漏列（只断言 `INSERT INTO public.x (` 存在，不比对 RETURNING/INSERT/父表列三者的序列一致——P0 级"数据错装"类错误不可见）；② DELETE join 键弱化（删掉 `AND h.ts = b.ts` 仍通过）；③ 预 ensure 谓词与 CTE 谓词/分区键错列（如把 date_trunc(bucket) 换成 date_trunc(updated_at) 仍通过）；④ down 与历史原文是否逐字（仅 marker 检查）。建议：在测试内嵌 7 表黄金对照表（父表列清单 + RETURNING 清单 + INSERT 清单 + join 键元组），逐 token 断言。
5. **changelog 内容**：除 P2-1 的 SHA 外，条目格式与 657/658 一致，status=applied+verified 与实库相符。
6. **观察（既有问题，非 659 引入）**：routing_decision_log_hot 存在两个完全相同的 `UNIQUE (request_id, ts)` 索引（写放大，可清理一个）；request_logs_bodies_hot 的 `request_logs_bodies_hot_request_id_idx` 与同列 UNIQUE 索引冗余；commit message 自述 659 未注册 installer embed（并行部署会话处理中，迁移文件不会被 startup installer 加载——需在后续工作包确认补注册）。
7. **行为变更确认**：request_wal promote 由 345 占位体（恒返 0）激活为真实迁移，hot 表从此有界——迁移头 659:39-43 已声明，签名/DEFAULT 不变（7 处 `'7 days'::interval` + `integer DEFAULT 5000` 与实库签名一致）。
