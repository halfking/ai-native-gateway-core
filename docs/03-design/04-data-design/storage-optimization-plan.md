# 存储结构优化方案 v2 —— 会话中心化：弃用 request_logs，六表会话族为最终态

> 状态：v2 方案整理（2026-09-14）。P0（迁移 705，request_logs promote 断链修复）已落地并验收，是过渡期 request_logs 保持健康的前提。
> v1 → v2 方向变更：v1 是"优化 request_logs 体系 + session_turns 瘦身"；v2 按用户需求（2026-09-14）反转为**弃用 request_logs 表族，以 sessions / session_turns / session_bodies / session_memora / session_censors / session_tools 六表族为唯一事实源**，session_turns 拼装还原 request_logs。v1 的 P2（session_turns 瘦身）**撤销**，P1a 落点修正（尾部快照从 sessions 列改到 session_bodies），P1b（promote 幂等 + 轮转入链）保留并升级为会话表族自身的收尾步骤。
> 实测基线：本机 llm-gateway-pg（PG17/citus 13.3-1），2026-09 分区，2026-09-14。

## 0. 结论摘要

1. 六表族中 sessions / session_turns / session_bodies 已存在且骨架吻合目标；差距在：sessions 缺项目/访问维度/耗时、client_type 断供；session_turns 缺 per-turn 正文与五类 request_logs 独有数据（计费归因、路由决策、运维诊断、轻量检索、完整性指纹）；session_bodies 需从"逐轮 delta"pivot 为"最后完整快照"。
2. 三张新表都有现成承接物：**session_tools ≈ tool_executions**（已有会话级工具调用明细含参数/结果，migration 134）；**session_censors = SmartSaniGuard 可逆脱敏映射落库**（现仅存 Redis、TTL 30min 即永久丢失）；**session_memora 全新**（外部 kxmemory 不动，本表存初始环境/上下文快照）。
3. 弃用 request_logs 的硬约束：**60% 的 request_logs 行没有 gw_session_id**（探针/系统/无会话头流量）——必须有落点；**credits_charged 是计费事实源**——必须先补采再停写；约 40 个 Go 文件读 request_logs/视图——用"同名视图体替换"实现瞬时兼容切换，再分波原生改造。
4. 净收益测算（月增）：request_logs(537MB) + request_logs_bodies(841MB) + session_bodies outbound_body(1.8GB) + 冗余消失，session_turns 增重后整体约 **6.5GB/月 → 1.5~2GB/月**，且会话分析（六表族）成为一等公民。

---

## 1. 目标架构：六表会话族

| 表 | 职责（用户需求） | 现状 | 差距 → 动作 |
|---|---|---|---|
| **sessions** | 主会话：时间、耗时、tokens、成本、标题、总结、轮次数、客户端信息、项目、任务、主题、tags；**不含请求内容** | 已有 created_at/updated_at/closed_at/status/total_turns/total_tokens/total_cost_usd/title/summary(+model/quality)/task_type/client_type/topic/intent/user_tags/last_turn_no/last_model/last_provider/primary_request_id | ①缺 project → 补 `project_id`（turn_writer 已写 turns.project_id，聚合时带上）；②缺访问维度 → 补 api_key_id/application_id/end_user_id/owner_user/client_ip/agent_name（首值优先，数据源 request_context_attrs + session_dim 已验证链路，internal/sessionv2mirror/session_dim.go:49-62）；③缺耗时 → 补 `duration_ms`（close 时 closed_at−首 turn t0）或查询派生；④client_type 断供修复（mirror bridge 不填，internal/sessionv2mirror/hook.go:211-330）；⑤456 死列 last_full_request/last_full_response/last_full_payload_at **DROP**（与"不含请求内容"冲突，快照职责移交 session_bodies） |
| **session_turns** | 每轮请求：时间、耗时、tokens、成本、**请求信息、回复信息**、模型、供应商凭据 | 已有 ts/t0-t9/latency_ms/status_code/success/error_kind/prompt+completion+cache tokens/cost_usd/model/provider/credential_id/request_id/turn_no/parent_request_id/attempt_no/task_type/project_id/compression_*/verdicts/title/summary/digest/protocols | ①**补 per-turn 正文**：`request_delta`/`response_delta` JSONB（现由 session_bodies 逐轮承载，writer 同事务已有该数据，session_writer_v2.go:282-291）；②**补采五类独有列**（见 §3 D1 清单）；③tools 列激活或移交 session_tools（现无写者） |
| **session_bodies** | 会话**最后一次完整**内容信息 | 现为逐轮 delta 行（request_delta/response_delta/outbound_body，253K 行/4.5GB，outbound_body 占 41%） | **pivot**：每会话一行"final_full"（kind 列或 turn_no=0 语义），会话关闭时由聚合器拼装全部 turn delta 写入一次；outbound_body 停写（差集提取改读本表 final_full，比 v1 的 sessions 列方案更符合用户语义） |
| **session_memora**（新） | 会话初始环境及上下文 | 无表（session_memora_extraction_log 只是外部 kxmemory 提炼的统计台账；memora_session_summaries 是同实例 kxmemory 产品的表，**非本网关所有，勿动**） | 新建：session_id/tenant_id/client_env jsonb（客户端、项目、入口）/initial_context jsonb（system prompt 摘要+digest、初始消息指纹、可用工具清单）/created_at；写点=会话首 turn 持久化时一次写入（insert-only） |
| **session_censors**（新） | 敏感信息及占位符映射 | SmartSaniGuard 可逆脱敏（`{SENSITIVE:type:index}` 占位符）映射**只存 Redis、TTL 30min 即永久丢失**（security/sanitize/smart_sani_guard.go:45-95；admin 只读端点 source 枚举就是 redis\|empty） | 新建：session_id/tenant_id/request_id/placeholder/sensitive_type/original_encrypted（应用层 AES-GCM，密钥走现有 env 体系）/created_at；写点=sanitize 中间件在会话持久化时双写；TTL 策略独立可配（默认 30d）；admin sanitize-matches 端点 source 增 `db`。**保留权与合规**：原文加密落库需按租户开关，默认开启可逆、可配置降级为只存 type+占位符 |
| **session_tools**（新） | 会话用到的工具及参数 | **tool_executions 已 90% 达标**（domains/toolexecution，含 SessionID/RequestID/ToolCallID/Arguments/Result/Status/DurationMs，migration 134，支持按 session 查询）；tool_call_events（8 行、无 session、零读者）是残表 | 新建 session_tools 为规范名（列承接 tool_executions + 补 turn_no/tool_name），tool_executions 写链改名/双写迁移，tool_call_events 废弃；tool_usage_stats 日聚合链路不动 |

### 会话分析卫星表（已存在，不动，作为六表族的下游）

session_summaries / session_titles / session_title_states / session_tags / session_clusters / session_embeddings / session_dim / session_project_attribution / session_intent_evolution / session_analysis_metadata / session_request_summaries / session_memory_summaries / session_compressions / session_turn_logs / session_turn_snapshots / session_aggregate_outbox / session_memora_extraction_log——这些是"分析产物"层，六表族补全后它们的数据源更干净。session_dim 的访问维度提升进 sessions 主表后，session_dim 降级为兼容视图或保留为缓存表。

---

## 2. 现状盘点（依据 docs 会话优化方案 + 代码调查 2026-09-14）

### 2.1 已有方案的定位（学习结论）

- `docs/会话优化v4/客户端会话保持.md` FR-6：**sessions / session_turns / session_bodies（分区三件套）+ session_summaries = 轮次账本**；`session_turns.request_id ↔ request_logs.request_id` 交叉校验已具备；timeline 端点当前只读 request_logs_hot（>7d 截断）。
- `docs/会话优化v4/CONTRACT_FREEZE_2026-08-22.md`：五类身份标识（request_id / attempt_id / gw_session_id / session_id / SessionPK）互不替代；gw_session_id 是客户端文本、**非 DB PK、不可唯一约束**；session_id/SessionPK 由服务端生成。
- turn_writer.go:115-130 注释自证：session_turns 冗余 t0-t9 正是为将来脱离 request_logs 回答 timeline——**本方案是把该意图推到终点**。
- admin/session_turns_tree.go:10-19 注释"当前以 request_logs_with_current_month 为单一事实源"——切换后此注释作废，改造点即此处。

### 2.2 request_logs 表族读写面（弃用影响面）

- **写入**：telemetry/client.go:1146（INSERT ~99 列）+ :1900（终态 UPDATE ~60 列）+ bodies upsert :2518-2556；admin/telemetry.go:341,420（多机 ingest）；trace/attachments/blob 清理 UPDATE。
- **读取**：约 **40 个 Go 文件**。四大类：admin 日志 API（/api/logs 列表/详情/聚合，admin/logs.go:620-1170）、仪表盘与监控（dashboard_board、analytics、credential_monitor、stats_minute_rollup、cost_reconciliation 等）、会话面（timeline、turns_tree、unified_detail、导出取证）、网关运行时旁路（压缩冷启动 main_v3_wiring.go:107、输出合规、双读校验器 dual_read_validator.go）。
- **五类独有数据**（会话表族没采集，兼容视图救不了，必须补采）：
  1. **计费归因**：api_key_id、application_id、end_user_id、customer_id、credits_charged（**计费事实源**，cmd/gateway/main.go:2686）、cost_display/cost_currency、work_type、token_band、usage_source；
  2. **路由决策**：is_auto_request、auto_decision、auto_confidence、task_type_chosen、routing_attempts、routing_summary、canonical_id/canonical_model、raw_model_name；
  3. **运维诊断**：trace_events、failure_stage/failure_detail_code、upstream_status_code/upstream_finish_reason、stream_first_chunk_ms/chunk_count/interrupted/done_sent、client_request_id、client_endpoint/client_timeout、egress_protocol；
  4. **轻量检索**：search_text、request_preview/response_preview、transform_summary；
  5. **完整性/来源**：identity_hash、request_checksum/response_checksum、system_fingerprint、origin_stage/origin_actor、client_ip/client_forwarded_for、agent_name/agent_type、virtual_client_id。
- **关键缺口流量**：gw_session_id 仅 40% 填充——探针（probe/probe_triggered）、系统请求、无会话头流量占多数，弃用前必须有落点（§3 D4）。
- is_final_success：会话"唯一成功"claim 机制建在 request_logs_hot（部分唯一索引 + NOT EXISTS 守卫），需移植到 session_turns（§3 D8）。
- request_logs_archive 已死（migration 331 移除，零读写）；request_logs_bodies 读取面约 20 处（body 查看器/标题/摘要/导出/探针），全部有 session 表族替代物。

### 2.3 censor / tools / memora 现状

见 §1 表格"差距 → 动作"列。要点：脱敏映射 Redis-only（30min TTL 丢失，无法审计回溯）；tool_executions 已支持按 session 查询工具调用明细；memora 记忆本体在外部 kxmemory（MemoraAutoHook 异步沉淀），本库只有提炼统计台账——**session_memora 是"会话初始环境快照"表，不是记忆库**，与 kxmemory 是上下游关系。

---

## 3. 核心设计决策

**D1｜session_turns = turn 级唯一事实源（宽表路线，撤销 v1 P2 瘦身）。**
补采列分四组落在 session_turns：
- 计费组（turn 级语义，不提升到 sessions）：`api_key_id, application_id, end_user_id, customer_id, credits_charged, cost_display, cost_currency, work_type, token_band, usage_source`；
- 路由组：`is_auto_request, auto_decision, auto_confidence, task_type_chosen, routing_attempts, routing_summary, canonical_id, raw_model_name`；
- 诊断组：`trace_events, failure_stage, failure_detail_code, upstream_status_code, upstream_finish_reason, stream_first_chunk_ms, stream_chunk_count, stream_interrupted, stream_done_sent, client_request_id, client_endpoint, client_timeout, egress_protocol`；
- 检索/完整性组：`search_text, request_preview, response_preview, transform_summary, identity_hash, request_checksum, response_checksum, system_fingerprint, origin_stage, origin_actor, client_ip, agent_name, agent_type`。
正文组：`request_delta, response_delta` JSONB（从 session_bodies 逐轮语义平移）。
数据源：telemetry RequestLogEntry 全量可得；mirror bridge（internal/sessionv2mirror/hook.go entryToProcessedRequest）扩字段是主代码工作项。**访问维度（api_key 等）双落**：sessions 存首值（会话归属），turns 存每轮值（计费精确到轮）。

**D2｜session_bodies pivot 为"最后完整快照"，outbound_body 停写。**
- 新增 `kind TEXT NOT NULL DEFAULT 'final_full'`（保留旧行 kind='turn_delta' 历史）；每会话至多一行 final_full，会话关闭聚合时拼装全 turn delta 写入（幂等 upsert by (tenant_id, session_id, kind)）。
- 差集提取链（TurnReader.LoadLatestOutbound ← outbound_builder ← cache_v2 L3 回源 ← writer 下一轮差集）改为读 final_full；未命中回退旧 outbound_body（历史行）。
- **v1 P1a 修正**：尾部快照落点从 `sessions.last_full_*`（与"sessions 不含请求内容"冲突）改为本表；456 三死列在 708 DROP。
- 收效：−1.8GB/月（outbound_body 停写）+ 逐轮 delta 行与 final_full 不再双份。

**D3｜sessions 补列不含内容。** 补 project_id、api_key_id、application_id、end_user_id、owner_user、client_ip、agent_name（首值优先）、duration_ms（关闭时计算）、client_type 修复；DROP last_full_* 三死列（708）。会话行诞生时机维持"首 turn 聚合 upsert"（≈会话开始；若需严格开始时间，后续可在请求注册时预插 status='pending' 行，列为增强项非阻塞）。

**D4｜无会话流量落点 = 合成系统会话。** 探针/系统/无 gw_session_id 流量统一映射 `session_id='sys:{kind}:{credential_id|provider_id}'`（按日聚合，如 `sys:probe:cred123:20260914`），client_type='system'、task_type 沿用现值。优点：单事实管道、探针统计照走 session_turns；代价：sessions 表混入系统行（分析侧用 client_type 过滤）。**不新建平行请求表**（否则等于换名重建 request_logs，违背弃用初衷）。

**D5｜三新表承接策略。** session_tools 承接 tool_executions（改名迁移，写链 domains/toolexecution/postgres_store.go 切表名，tool_call_events 废弃）；session_censors 承接 SmartSaniGuard Redis 映射（双写期后 DB 为权威，Redis 仅作热缓存）；session_memora 全新（首 turn 快照 insert-only；kxmemory 外部链路不动，extraction_log 台账保留）。

**D6｜拼装还原两步走。**
- 第一步（瞬时切换）：**重建同名视图** `request_logs_with_current_month`——视图体改为 session 家族拼装（session_turns JOIN sessions），列集维持既有 113 列冻结交集（缺源列补 NULL），约 40 个读方零改动切换；`request_logs_with_current_month_without_customer_id` 包装视图同步。视图自愈链（db/request_logs_view_schema.go + 577/610/680/696/700 迁移）同步改写为 v2 体。
- 第二步（按域原生改造）：admin/logs、dashboard、jobs 分波改读 session_turns 原生列（消除视图 NULL 补位与 JOIN 开销），视图最终退化为过渡 shim 后移除。
- 存量历史窗口：过渡期视图体为 `session_family UNION ALL request_logs（冻结只读）`，历史分区按 TTL 逐步退出后切纯 session 体。

**D7｜计费事实迁移先行。** credits_charged/cost_display/currency/work_type/token_band 补采进 session_turns 并经 dual_read_validator 校验等值后，才允许 request_logs 停写；usage_ledger 族（计费台账）**不在弃用范围**，保持现状。

**D8｜is_final_success 语义移植。** session_turns 增 `is_final_success BOOLEAN` + 部分唯一索引（tenant_id, session_id）WHERE is_final_success（沿 request_logs_hot 的 claim 模式：守卫 + 23505 降级）；claimSessionFinalSuccess 改写 turns 版；timeline 的 superseded 标注与 promote 唯一索引依赖随之切换。

---

## 4. 分阶段路线图

| 阶段 | 内容 | 迁移编号 | 代码工作项 | 退出条件 |
|---|---|---|---|---|
| **P0（✅已完成）** | request_logs promote 断链修复，过渡期 request_logs 保持健康 | 705（已落地） | — | 本机已验收；生产 252 应用前复核 default 分布（v1 §2.5） |
| **S1a 六表族补全（schema+新表）** | 三新表 + sessions 补列 + turns 五类列 + bodies kind 列 | **706**：session_memora/session_censors/session_tools 建表 + sessions 补列 + client_type 修复；**707**：session_turns 补采五类列+正文列+is_final_success+部分唯一索引；**708**：session_bodies kind 列 + DROP sessions.last_full_* | mirror bridge 扩字段；aggregator 写 project/访问维度/duration；工具链切 session_tools；sanitize 双写；memora 首轮快照写点 | 双账本登记；行为测试覆盖新写点 |
| **S1b 写链切换（会话侧）** | turn writer 写正文+新列；聚合器关闭时写 final_full、停写 outbound_body、差集读端切 final_full | （随 707/708 的 Go 侧） | bodies_writer/turn_writer/session_writer_v2/outbound_builder/cache_v2 | settings 开关灰度：`storage.session_turns_bodies_enabled`、`storage.session_final_full_enabled`；回切开关保留 |
| **S2 拼装还原 + 双读校验** | 同名视图体替换（session UNION ALL 冻结 request_logs） | **709**：视图 v2 体 + 视图自愈链改写 + request_logs 停写 gate（settings `storage.request_logs_write_enabled` 默认 true） | dual_read_validator 对账扩展（turns vs request_logs 等值） | 对账 7 天零漂移 |
| **S3 读端分波切换** | 波1 admin 日志/详情；波2 仪表盘/监控 jobs；波3 网关旁路（压缩冷启动/摘要/导出） | 无（纯代码） | 约 40 文件按 §2.2 分组迁移；session_turns_tree 等注释更新 | 各波功能回归通过 |
| **S4 停写 request_logs** | gate 关闭；telemetry/admin ingest 停写 request_logs_hot 与 bodies_hot（usage_ledger、turns 写入不变） | 710：无 DDL 的 gate 收口 + 双账本 | telemetry client 分支化 | request_logs 行数归零增长；compat 视图历史窗口正常 |
| **S5 存储收尾** | 历史分区 TTL/DROP 决策（合规窗口）；**711**：promote 幂等去 ON CONFLICT（session_bodies 反连接版）+ `enforce_columnar_partition_aged`（月关闭后 heap→columnar，当月恒 heap）；DROP 456 死列残留与 sensitive_keywords 死列；request_logs_bodies 停写与分区回收 | 710/711 | partition_manager 挂点（archiveSpecs 的 day 调度模式） | 月增存储达 §6 目标 |

依赖关系：S1 → S2 → S3 → S4 → S5 严格顺序；711（轮转）依赖 707/708（turns 变宽表后列存收益才最大）。

---

## 5. schema 设计细节（草案）

```sql
-- 706a 三新表（均含 partition_date 月分区 + hot 表，沿用 430 惯例）
CREATE TABLE public.session_memora (
  id BIGINT GENERATED ALWAYS AS IDENTITY,
  session_id TEXT NOT NULL, tenant_id TEXT NOT NULL,
  client_env JSONB,            -- 客户端类型/版本/入口/项目/agent
  initial_context JSONB,       -- system prompt digest+指纹、初始消息指纹、可用工具清单
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  partition_date DATE NOT NULL,
  PRIMARY KEY (id, partition_date)
) PARTITION BY RANGE (partition_date);
CREATE UNIQUE INDEX uq_session_memora_session ON session_memora (tenant_id, session_id, partition_date);

CREATE TABLE public.session_censors (
  id BIGINT GENERATED ALWAYS AS IDENTITY,
  session_id TEXT NOT NULL, tenant_id TEXT NOT NULL,
  request_id TEXT, turn_no INT,
  placeholder TEXT NOT NULL,       -- {SENSITIVE:phone:3}
  sensitive_type TEXT NOT NULL,    -- phone/id_card/email/credit_card/...
  original_encrypted BYTEA,        -- AES-GCM(会话级数据密钥)；降级模式为 NULL
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  partition_date DATE NOT NULL,
  PRIMARY KEY (id, partition_date)
) PARTITION BY RANGE (partition_date);
CREATE INDEX idx_session_censors_session ON session_censors (tenant_id, session_id, partition_date);

CREATE TABLE public.session_tools (
  id BIGINT GENERATED ALWAYS AS IDENTITY,
  session_id TEXT NOT NULL, tenant_id TEXT NOT NULL,
  request_id TEXT, turn_no INT, tool_call_id TEXT,
  tool_name TEXT NOT NULL,
  arguments JSONB, result_digest TEXT, result JSONB,
  status TEXT, latency_ms INT, error_code TEXT,
  called_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  partition_date DATE NOT NULL,
  PRIMARY KEY (id, partition_date)
) PARTITION BY RANGE (partition_date);
CREATE INDEX idx_session_tools_session ON session_tools (tenant_id, session_id, partition_date);

-- 706b sessions 补列（全部可空，无回填锁风险）
ALTER TABLE public.sessions
  ADD COLUMN IF NOT EXISTS project_id TEXT,
  ADD COLUMN IF NOT EXISTS api_key_id TEXT,
  ADD COLUMN IF NOT EXISTS application_id TEXT,
  ADD COLUMN IF NOT EXISTS end_user_id TEXT,
  ADD COLUMN IF NOT EXISTS owner_user TEXT,
  ADD COLUMN IF NOT EXISTS client_ip TEXT,
  ADD COLUMN IF NOT EXISTS agent_name TEXT,
  ADD COLUMN IF NOT EXISTS duration_ms BIGINT;

-- 707 session_turns 补采（列清单见 §3 D1，全部可空）＋ is_final_success claim
ALTER TABLE public.session_turns
  ADD COLUMN IF NOT EXISTS request_delta JSONB,
  ADD COLUMN IF NOT EXISTS response_delta JSONB,
  ADD COLUMN IF NOT EXISTS is_final_success BOOLEAN,
  ADD COLUMN IF NOT EXISTS api_key_id TEXT,
  -- …（§3 D1 四组列，形态同上）；
CREATE UNIQUE INDEX IF NOT EXISTS uq_session_turns_final_success_session
  ON session_turns (tenant_id, session_id, partition_date) WHERE is_final_success;

-- 708 session_bodies pivot + sessions 死列清理
ALTER TABLE public.session_bodies ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'final_full';
ALTER TABLE public.sessions
  DROP COLUMN IF EXISTS last_full_request,
  DROP COLUMN IF EXISTS last_full_response,
  DROP COLUMN IF EXISTS last_full_payload_at;
-- 历史逐轮 delta 行回填 kind='turn_delta'（一次性 UPDATE，分批）
```

## 6. 存储收效测算（月增，本机 2026-09 实测外推）

| 项 | 现状 | 目标态 |
|---|---|---|
| request_logs（月分区+hot） | 537MB+ | 0（停写后冻结，历史按 TTL 退出） |
| request_logs_bodies | 841MB（columnar） | 0（正文进 turns/final_full） |
| session_bodies | 4,478MB（outbound_body 1,837MB + 双份 delta） | final_full ≈ 1,200~1,600MB（一次拼装、无 outbound 副本、可轮转 columnar 后 ≈400~600MB） |
| session_turns | 519MB | +正文与五类列 ≈ 900~1,300MB；月关闭后 columnar ≈ 400~600MB |
| 三新表 | — | censors/tools 按会话渗透率，预估 <100MB/月；memora ≈20~40MB/月 |
| **合计** | **≈6.4GB/月** | **≈1.5~2.2GB/月（轮转后 ≈1.0~1.5GB）** |

## 7. 回滚策略

- **S1 全部列/表为增量**：回滚 = 关 settings 开关（写入分支化）+ 列保留不删（无破坏性）。
- **S2 视图**：709.down = 恢复旧视图体（hot UNION ALL request_logs，v1 700/696 链即为来源）；视图自愈链保留双版本定义。
- **S4 停写 gate**：settings 一键回 true；hot 表结构不动，无数据丢失窗口。
- **不可逆点**：仅 S5 的 DROP（历史分区/死列/last_full_*）——执行前强制快照分区清单与行数守恒（§8-A），并确认合规保留期已过。session_censors 原文加密列的销毁单独走租户级请求。

## 8. 验收 SQL

```sql
-- A. 行数守恒（每次切换后）：turns+final_full 覆盖率
SELECT count(DISTINCT s.session_id) AS sessions_total,
       count(DISTINCT b.session_id) FILTER (WHERE b.kind='final_full') AS with_final_full
FROM public.sessions s
LEFT JOIN public.session_bodies b ON b.session_id=s.session_id AND b.tenant_id=s.tenant_id
WHERE s.created_at > now() - interval '1 day';

-- B. 五类补采填充率（S1 后 T+1h，应 >95%）
SELECT count(*) FILTER (WHERE api_key_id IS NOT NULL)::float8 / count(*) AS billing_fill,
       count(*) FILTER (WHERE request_preview IS NOT NULL)::float8 / count(*) AS search_fill
FROM public.session_turns WHERE ts > now() - interval '1 hour';

-- C. 拼装还原等值（S2 双读期）：同名视图 vs request_logs 抽样比对
SELECT count(*) FROM (
  SELECT request_id FROM public.request_logs_with_current_month WHERE ts > now() - interval '1 hour'
  EXCEPT
  SELECT request_id FROM public.session_turns WHERE ts > now() - interval '1 hour'
) d;  -- 期望 0（双写期）

-- D. 计费等值：turns.credits_charged 合计 vs request_logs 同窗口合计
SELECT (SELECT sum(credits_charged) FROM public.session_turns WHERE ts > now()-interval '1 day') AS turns_credits,
       (SELECT sum(credits_charged) FROM public.request_logs WHERE ts > now()-interval '1 day') AS rl_credits;

-- E. final_success 唯一性
SELECT tenant_id, session_id, count(*) FROM public.session_turns
WHERE is_final_success GROUP BY 1,2 HAVING count(*) > 1;  -- 期望 0 行

-- F. 停写后 request_logs 冻结（S4）
SELECT max(ts) FROM public.request_logs;  -- 随时间不再前进
```

## 9. 风险登记

| 风险 | 缓解 |
|---|---|
| 计费事实源切换期漂移（credits_charged） | D7：补采→dual_read_validator 7 天零漂移→才停写；usage_ledger 独立不受影响 |
| 60% 无会话流量落点缺失 | D4 合成系统会话，先于 S2 落地；分析侧按 client_type='system' 过滤 |
| 113 列冻结交集视图在 session 家族上缺源列 | 缺源列显式 NULL 补位并在视图注释登记；分波原生改造后消除 |
| session_turns 宽表化后 TOAST 膨胀 | 正文 delta 与 final_full 走 TOAST（jsonb 天然）；711 月关闭后轮转 columnar；当月恒 heap（promote/UPDATE 需求不变） |
| session_censors 落原文的合规风险 | 应用层 AES-GCM + 租户级开关（可降级只存占位符）+ 独立 TTL + 读取审计 |
| memora 命名与同实例 kxmemory 产品表混淆 | session_memora 仅存初始环境快照；kxmemory 的 memora_* 表零接触（跨 schema/产品边界写入禁令） |
| 视图切换的 40 文件回归面 | 两步走：同名视图体替换（零代码改动）→ 分波原生改造（每波独立回归） |
| 双写期存储翻倍 | bodies_hot/request_logs_bodies 的 TTL（7d/24h）天然限幅；双写期 ≤2 周 |
| mirror bridge 扩字段的回归 | entryToProcessedRequest 单点扩展 + identity fixture 契约测试（CONTRACT_FREEZE §6） |

## 10. v1 → v2 对照

| v1 条目 | v2 处置 |
|---|---|
| P0 迁移 705（promote 断链） | **保留已完成**——过渡期 request_logs 健康是双写期前提 |
| P1a 激活 sessions.last_full_* 停写 outbound_body（706） | **修正落点**：尾部快照改落 session_bodies final_full（用户语义：sessions 不含请求内容）；456 死列改 DROP |
| P1b promote 幂等去 ON CONFLICT + 轮转入链（707） | **保留升级为 711**：session_turns 宽表化后收益更大；enforce_columnar_partition_aged 设计不变 |
| P2 session_turns 瘦身 + view JOIN request_logs（708） | **撤销并反转**：session_turns 成为 turn 级唯一事实源（宽表），view 拼装方向变为 session 家族 → request_logs 兼容视图 |
| 不物理合并 request_logs+session_turns / 不建物化视图 | **继续成立**（弃用 ≠ 合并；普通视图/兼容视图，无 matview） |
| session_bodies 不按纯 insert-only 裸转列存 | **继续成立**（final_full 关闭时 upsert 仍有重试覆盖；轮转仍走月关闭后） |
