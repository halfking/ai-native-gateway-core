# 会话存储解耦方案 v3 —— session_turn_details 特征层落地

> 日期：2026-09-20（2026-09-21 批判式审计修正）
> 状态：已实施（migration **733/734** + 写入器 + Lite 链路对等）。
> 迁移编号两度撞号：原编 727/728（撞 R46/R48 的 727-730）→ 731/732（推前 pull 实测再撞
> origin 731_auto_route）→ **终编 733/734**。
> 前置：docs/04-implementation/plan/2026-08-25-request-session-persistence-final-plan.md、
> migration 706/707/710/712/713（S1a 宽表 + 710 拼装视图 + mirror outbox + cost 精度）

## 1. 两条存储链路（严格区分，不混为一谈）

| | Full（主力） | Lite（降级） |
|---|---|---|
| 触发 | `storage_mode=full`（生产/多实例/本地 Docker 部署） | `storage_mode=lite`（单机/个人） |
| 组成 | **PG17**（结构化事实）+ **files**（附件等大对象）+ **Redis**（队列/缓存/URSM 状态） | **SQLite**（单文件库）+ **files**（body 落盘）+ **memory**（进程内 KV：`storage/lite` MemoryStateStore） |
| 请求日志现状 | `request_logs` 145 列宽表 + hot + 月分区 + `request_logs_bodies` | SQLite `request_logs` 9 列瘦身表（body 不入库，has_body 标记 + 文件） |
| 会话族现状 | `sessions` + `session_turns`（707 后 ~123 列）+ `session_bodies` | SQLite `sessions` + `session_turns`（sink 已 journal）+ FileBodies |
| 本次改动 | **+`session_turn_details` 特征层**，视图 LEFT JOIN 替换 NULL 占位 | **+SQLite `session_turn_details`** 对等 journal + `session_logs_view` 拼装视图 |
| request_logs 退役路径 | S4 门控 `storage.request_logs_write_enabled` → 停写 → TTL 燃尽 → DROP | 同门控；读端迁 `session_logs_view` |

**本地方布署（deploy-local.sh）验证的是 Full 链路**（Docker PG+Redis）；Lite 链路由单测/构建门禁覆盖。

## 2. 目标（提效 / 降本 / 会话分解准备）

1. **提效**：`session_turns` 保持瘦核心（索引/WHERE 命中列），大特征（routing/quality/stream/安全 jsonb）
   移 1:1 `session_turn_details` 懒加载；canonical 视图 session 分支 30 处 `NULL::` 占位替换为真实列，
   管理端/回放工具不再需要回源 request_logs。
2. **降本**：会话族成为唯一事实源后，`request_logs` 走 S4 停写 → 7 天 TTL 燃尽 → DROP，
   消除双写存储与写入放大；details 特征列可后续转 columnar/compression。
3. **会话分解准备**：四表分层（sessions 聚合 / turns 元数据 / details 特征 / bodies 原文）
   让"按会话切分、按层取数"成为自然操作：分解器只读 turns+bodies 重建上下文，
   计费/路由分析只读 details，互不拖拽大字段。

## 3. Full 链路实施（migration 733/734）

### 3.1 `session_turn_details`（733）

- 分区：`PARTITION BY RANGE (partition_date)`，hot 表 `session_turn_details_hot`（heap，autovacuum 调优）
- 键：`UNIQUE (tenant_id, request_id, partition_date)`（对齐 session_turns_hot 冲突键），
  `UNIQUE (session_id, turn_no, partition_date)`，主键 `(partition_date, id)`（id 用独立序列）
- 列三组：
  - **视图契约组（30 列，710 NULL 占位 → d.col）**：client_model, provider_id, client_profile,
    virtual_ip, virtual_mac, affinity_hit, transform_rule_id, gw_task_id, api_key_prefix, owner_user,
    application_code, key_alias, api_key_owner_user, auto_profile, confidence_num, model_chosen,
    strategy_used, compression_reason, outbound_msg_count, outbound_token_est, outbound_msg_hashes,
    quality_flags, quality_fix_actions, quality_score, stream_chunk_errors, stream_chunks_sent,
    attachments, request_type, request_class, due_at
  - **分析视图储备组（v_node_switch/v_timeout/v_continuation 三视图待迁）**：node_switch_count,
    timeout_mode, effective_timeout_seconds, is_continuation, cached_response_id, context_size_tokens,
    keepalive_sent_count, cache_hit, cache_tokens_saved
  - **多维 token / 安全 / 上游组**：reasoning_tokens, image_tokens, audio_tokens, video_tokens,
    provider_tokens, protocol_conversion, rate_limit_status, content_safety_score, dlp_violations,
    sensitive_keywords, upstream_endpoint, task_id, task_title, api_key_fingerprint, provider_model
- 回填（**733 首版两处缺陷已审计修正**）：
  - 候选行 = **session_turns(hot ∪ parent)**：首版只扫父表，漏掉尚未 promote 的热表 turns
    （writer 只覆盖部署后的新 turn，错过回填即永久缺行）；本地实测部署窗口 + 热表遗漏
    累积 30,041 行存量缺口，修正版复放 8.5s 全量闭合。
  - 源列两级守卫：视图契约组 30 列硬引用（冻结 113 列契约成员，request_logs 必有）；
    **储备/多维组 24 列按 information_schema 交集动态回退 `NULL::<type>`**——首版硬引用
    冻结契约外的物理附加列，陈旧库/交集克隆库直接 42703 炸停部署（live 契约测试实证）。
  - `request_logs(hot UNION parent)` LEFT JOIN，50000/批循环防长事务；幂等可重跑。
- 不建 default 分区（与月分区创建互斥）；promote/回填先 ensure 批次月份再 INSERT
- RLS：`ENABLE ROW LEVEL SECURITY` + 租户策略（GUC `app.current_tenant`，与 session_turns 同词表，720 规范）
- promote：`promote_session_turn_details_hot_to_partition`（镜像 526/707 契约：显式列清单 + 原子 CTE），
  挂入 `bg/partition_manager.go` 调度（与 session_turns_hot 同批）

### 3.2 视图升级（734）

- `projectionExprsV2` 改为 overlay 结构：基础表达式不变，30 个 NULL 占位按位置替换为 `d.<col>`
- `canonicalV2DDL(baseHasFP, baseHasRaw, hasDetails)`：
  - hasDetails=true：session 分支 `LEFT JOIN session_turn_details_hot/parent d ON d.tenant_id=t.tenant_id
    AND d.request_id=t.request_id AND d.partition_date=t.partition_date`（LEFT 保语义：无 details 行时
    仍输出 NULL，行为向后兼容）
  - hasDetails=false（陈旧库/733 未跑）：回退 710 形态，不引用 d.*
- 探测：ensure 启动时 probe `session_turn_details(_hot)` 存在性；v2 检测 `LIKE '%session_turns%'`
  **且（按库况）要求 `LIKE '%session_turn_details%'`**
- **down 契约（本轮审计补全）**：down 按号逆序 734.down → 733.down；734.down 的
  「已是 710 体」守卫必须 `NOT LIKE '%session_turn_details%'`（否则 734 体被误判为 710 体
  跳过重建，733 down DROP 表族时 canonical 视图残留依赖 → 2BP01 炸停回滚链）；
  710.down 改先 DROP 再 CREATE（`CREATE OR REPLACE VIEW` 不能变更列类型，会话体
  text → v1 体 varchar 的 42P16）
- `SessionFamilyTurnsSourceSQL()`（S3 wave-1 原生读端）同步带 JOIN
- 契约测试改指 734 文件的 `$proj$/$names$` 块；live 重放顺序 733 → 710 → 734；
  down 链 734.down → 733.down → 710.down 分步断言

### 3.3 写入路径

- `domains/session/v2/details_writer.go`：`DetailsRecord` + `UpsertDetailsInTx`（与 turn+bodies **同事务**，
  `ON CONFLICT (tenant_id, request_id, partition_date) DO UPDATE`——telemetry 晚到回填/重放取最新值）
- `ProcessedRequest.Details` 字段；`SessionWriterV2.Write` 在 `AppendTurnInTx` 返回 turnNo 后补键并 upsert
- `internal/sessionv2mirror/s1b_fields.go`：`applyStorageS1BFields(entry → req.Details)`，
  **30 列映射 = 26 有源 + 4 缺源**（virtual_ip/virtual_mac/key_alias/owner_user，RequestLogEntry
  无字段，保持 NULL 待管道接线；沿用 s1a §9 登记惯例）

## 4. Lite 链路实施

- `storage/sqlite/schema.go`：+`session_turn_details`（SQLite 类型对等）+ `session_logs_view`
  （turns LEFT JOIN details，本地管理端读端逐步切换的目标；LEFT 语义与 PG17 canonical 734 对齐）
- `cmd/gateway/lite_telemetry_sink.go`：journal 追加 details 行（entry 上可得的特征列）；
  `request_logs` 写入受 `storage.request_logs_write_enabled` 门控（默认开，S4 同门）

## 5. 退役门（本方案之后）

1. S3：11 个 request_logs 视图 → 改读 canonical（已带 details 列）或 session 族原生源
2. S4：`storage.request_logs_write_enabled=false`（前提：dual_read 7 日零漂移 + mirror outbox 清零）
3. S5：TTL 燃尽（bodies 7 天 / logs 30 天）
4. S6：DROP `request_logs` 族 + `request_wal` 族 + lite SQLite 旧表；视图改名定稿

## 6. 验收（本地 Full 链路）

- [x] `go build ./... && go vet ./...`
- [x] 契约测试（offline）`TestViewV2ProjectionContractSync`（含 734 overlay/回退双形态断言）
- [x] 迁移唯一性 `TestNumericUpMigrationVersionsAreUnique`
- [x] **live 等价** `TestRequestLogsViewV2EnsureMatchesMigration`（真实目录克隆 scratch：
      ensure ≡ 734 重放 viewdef 全等 + details_join=true；**down 链 734→733→710 首次全通**——
      该测试在本轮审计前从未跑绿，接连暴露 42703 / 2BP01 / 42P16 三个缺陷）
- [x] 五点同步门禁：`TestPromoteSpecsCoversAllDefaultPartitions` + `TestHotPromoteTableMap`
- [x] 六点同步登记：`scripts/apply-db-revision-sequence.sh` 显式清单追加 733/734
- [x] deploy-local.sh 部署（2.5.6.2156→2158 三次蓝绿，`--root` 指向本 worktree）
- [x] 迁移落库 + 回填缺口闭合：首版回填 438,156 行后实测仍差 **30,041 行**（热表遗漏 +
      部署窗口）；修正版复放 8.5s 插入 30,062 行，缺口 → 9（零特征 turns 设计语义），
      动态版再复放幂等（按需补增量 ~237 行后残余 ~24 行在途/零特征）
- [x] writer 端到端：新流量 details_hot 持续产出（60min 窗口 836 行全带 client_model；
      promote 后台调度把近期行扫入月分区 249 行/10min），业务行
      `client_model=deepseek-v4-flash`、探针行 `quality_flags={probe,gateway}` 经
      canonical 视图浮出（此前恒 NULL）；canonical 视图非空 client_model 计 502,412 行
- [x] promote 实测：`promote_session_turn_details_hot_to_partition('1 millisecond')`
      搬运 21 行到月分区；后台调度 + admin 手动入口可用
- [x] RLS：4 条策略（tenant_isolation + super_admin_bypass × hot/parent）；分区 2026_09/2026_10
      预建、无 default
- [x] 健康检查：/health=200，凭据解密冒烟 providers=587 creds=7 failed=0

## 7. 实施中的坑（供后续参考）

1. `ensure_session_turn_details_partition(CURRENT_DATE + INTERVAL '1 month')` —— date+interval
   是 timestamp，需 `::date` 强转否则函数签名不匹配。
2. promote 的 `EXECUTE format('%L', interval)` 内嵌为字符串字面量，PG 把
   `'08:00:00'` 强转 timestamptz 报 22007——须 `%L::interval` 显式 cast。
3. 不建 default 分区：default 会与后续月分区创建互斥（PG 拒绝容纳既有行的
   default 上再建重叠分区）；promote/回填必须先 ensure 批次月份再插入。
4. pg_get_viewdef 输出省略 search_path 内对象的 schema 前缀，视图探测 LIKE
   模式不要带 `public.`。
5. 部署链：`cmd/gateway migrate` → db.Open Go ensure 链（不扫 SQL 文件）；
   存量库应用走 `scripts/apply-db-revision-sequence.sh` 显式清单。
6. **（2026-09-21 审计新增）回填候选必须含热表**：`session_turns(_hot)` 是
   「heap hot + 月分区父表」双层，只扫父表会漏掉尚未 promote 的近期 turns——
   它们由旧二进制写入、永不会再被 writer 覆盖，缺口随 promote 单调累积。
7. **（审计新增）迁移引用物理附加列须过 information_schema 交集**：canonical
   冻结 113 列契约之外的 request_logs 附加列（储备/多维组 24 列）在陈旧库/
   交集克隆库可能不存在，硬引用即 42703 炸停部署通道；契约外列一律动态探测
   回退 NULL，契约内列才可硬引用。
8. **（审计新增）down 链三类盲区**：①「已是目标体」探针要排除更新的体形
  （734 体同样含 `session_turns`，只 LIKE 不 NOT LIKE 会误判跳过）；②
  `CREATE OR REPLACE VIEW` 不能变更列类型（42P16），跨体形回滚先 DROP 再
  CREATE 同事务替换；③ down 测试必须按号逆序（734→733→710）并分步断言，
  才能把这些盲区钉在门禁里。
9. **（审计新增）live 契约测试不可「声明可跑」**：本轮前 live 重放测试从未
   真正跑绿；「测试存在」≠「测试通过」，交付证据以实测输出为准。
