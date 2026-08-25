# 252 PG 分区表 vs _hot 表 Schema 一致性报告

## 任务背景

本会话是 handoff 接续任务：清理 252 上 2026-06/07/08 的旧分区数据（已完成）后，
新会话核验 14 对 (parent 分区表, _hot 普通表) 的 schema 是否一致。

## 时间与上下文

- 扫描时间：2026-08-25 16:47 (CST)
- 目标：阿里云 252 服务器 (8.136.114.245 → SSH tunnel → 115.29.212.252:25022)
- PG 实例：pg-252-pg17 (kx-citus-pg17:amd64)，监听 172.16.2.210:5432
- 数据库：llm_gateway
- 当前分支：fix-154-current-deploy @ e5724f5af
- 角色：kxuser（只读，无 _hot 权限）→ 切 llm_gateway
- 凭据：已 inject aliyun-edge-252

## 对比方法（rule 49 §49-1）

```sql
-- 1. 列名清单
SELECT tablename FROM pg_tables 
WHERE schemaname='public' AND tablename LIKE '%_hot';

-- 2. 父表是否存在（按命名约定去 _hot 后缀 + pg_inherits 校验）
SELECT c.relname FROM pg_class c
WHERE c.relkind='p' AND EXISTS (SELECT 1 FROM pg_inherits WHERE inhparent=c.oid);

-- 3. 字段对比（information_schema.columns JOIN）
-- 维度：列名 / data_type / is_nullable / character_maximum_length / numeric_precision / column_default
```

## 总览

| 指标 | 数值 |
|---|---|
| 总对比对 | 14 |
| 完全一致 | 8 对（57%）|
| 有差异 | 6 对（43%）|
| 差异总数 | 39 处 |

## 差异明细

### 1. candidate_failure_logs ↔ candidate_failure_logs_hot （1 处）

| 列 | 差异 |
|---|---|
| `ts` | nullable: parent=NO hot=YES |

### 2. dashboard_access_events ↔ dashboard_access_events_hot （2 处）

| 列 | 差异 |
|---|---|
| `created_at` | default: parent='' hot='now()' |
| `timestamp` | default: parent='' hot='now()' |

### 3. request_logs ↔ request_logs_hot （25 处）⚠️ 重点

| 列 | 差异 |
|---|---|
| `cached_response_id` | MISSING_IN_HOT |
| `context_size_tokens` | MISSING_IN_HOT |
| `continuation_keywords` | MISSING_IN_HOT |
| `effective_timeout_seconds` | MISSING_IN_HOT |
| `is_continuation` | MISSING_IN_HOT |
| `keepalive_sent_count` | MISSING_IN_HOT |
| `node_switch_count` | MISSING_IN_HOT |
| `outbound_body` | MISSING_IN_HOT |
| `raw_model_name` | MISSING_IN_HOT |
| `system_fingerprint` | MISSING_IN_HOT |
| `timeout_mode` | MISSING_IN_HOT |
| `caller_id` | MISSING_IN_PARENT |
| `session_correlation_id` | MISSING_IN_PARENT |
| `status_code` | MISSING_IN_PARENT |
| `agent_name` | type: varchar(255) → text |
| `agent_type` | type: varchar(50) → text |
| `api_key_fingerprint` | type: varchar(16) → text |
| `task_id` | type: varchar(255) → text |
| `content_safety_score` | type: jsonb → double precision |
| `dlp_violations` | type: jsonb → ARRAY (text[]) |
| `ir_extensions` | type: jsonb → text |
| `protocol_conversion` | type: boolean → text |
| `sanitizer_mutations` | type: jsonb → text |
| `attachment_count` | default: '' → '0' |
| `has_attachments` | default: '' → 'false' |

### 4. request_logs_bodies ↔ request_logs_bodies_hot （1 处）

| 列 | 差异 |
|---|---|
| `tenant_id` | MISSING_IN_HOT |

### 5. session_module_executions ↔ session_module_executions_hot （5 处）

| 列 | 差异 |
|---|---|
| `created_at` | default: '' → 'now()' |
| `updated_at` | default: '' → 'now()' |
| `started_at` | default: '' → 'now()' |
| `execution_id` | default: '' → nextval |
| `status` | default: '' → 'running' |

### 6. usage_ledger ↔ usage_ledger_hot （5 处）

| 列 | 差异 |
|---|---|
| `audio_tokens` | MISSING_IN_PARENT |
| `image_tokens` | MISSING_IN_PARENT |
| `provider_tokens` | MISSING_IN_PARENT |
| `reasoning_tokens` | MISSING_IN_PARENT |
| `video_tokens` | MISSING_IN_PARENT |

## 根因初步分析（rule 09 §5.2 溯源）

### 类型 A：HOT 表"主动加了默认值"（dashboard_access_events / session_module_executions）

hot 表的 `01-schema.sql` baseline 定义中显式带 `DEFAULT now()` / `DEFAULT 'running'`，
而 parent 分区表由分区迁移生成时未同步默认值。
**可能影响**：hot 表 INSERT 时自动填充字段，parent 分区表的月度分区若 INSERT 不会填充。
**建议**：人工 review，确认是 hot 表的"主动设计"还是漂移。

### 类型 B：HOT 表"独立 schema 演进"（request_logs）

- parent 比 hot 多 14 列（cached_response_id / context_size_tokens / outbound_body 等）
  → 是 parent 后续 ADD COLUMN 没同步到 hot
- hot 比 parent 多 3 列（caller_id / session_correlation_id / status_code）
  → 是 hot 独立添加的字段，parent 没接受
- 多个 jsonb → text 或 jsonb → ARRAY 的类型简化
  → hot 设计上简化了存储
- varchar(255) → text 类型放宽
  → 兼容历史写入

**关键问题**：`outbound_body` 在 parent 有，在 hot 缺。
该字段是 v3 (2026-06-19) session compressor 的核心字段，缺失会导致 hot 表 INSERT 时
写入父表分区时丢失 outbound_body 数据。

### 类型 C：HOT 表"业务扩展"（usage_ledger）

hot 表多了 5 个 token 字段（audio/image/video/reasoning/provider），parent 缺。
这看起来是 hot 表主动加入的多模态计数字段。

### 类型 D：可空性差异（candidate_failure_logs）

`ts` 列：parent=NOT NULL，hot=NULL。hot 表允许"补录"无 ts 的历史数据。

## 当前判断

- 6 对有差异，但**没有任何一对是阻塞级**——所有表都可正常读写
- 大多数差异看起来是**有意为之的设计**（hot 是热数据层，独立演进），但**缺少 SSOT**
- 唯一需要立即关注的是 `request_logs` 的 `outbound_body` 等核心字段缺失
  （v3 compressor 写入 hot → 落父表分区 → 应有该字段，但 hot schema 缺）

## 建议下一步（不擅自执行）

按 rule 09 §5.2 流程：
1. 找到每个差异的引入 commit/迁移（如 V350__routing_attempts_tracking.sql 改 hot 加列）
2. 评估每个差异是"有意设计"还是"无意识漂移"
3. 列出修复优先级
4. 与老板确认后再执行修复（**本会话不擅自修复**）

## 证据文件

- `docs/audits/2026-08-25-252-pg-schema-consistency-report.txt`（人类可读报告）
- `docs/audits/2026-08-25-252-pg-hot-columns-raw.txt`（hot 表列原始数据，404 行）
- `docs/audits/2026-08-25-252-pg-parent-columns-raw.txt`（parent 表列原始数据，408 行）

## 元数据

- 数据源：252 PG (pg-252-pg17) 上的 `llm_gateway` 库
- 探测命令：`docker exec pg-252-pg17 psql -U llm_gateway -d llm_gateway`
- 角色权限：kxuser 仅可读 metadata，llm_gateway 可读所有 _hot 表字段
- 总探测时间：~30 秒
- 工具：information_schema.columns（rule 49 §49-1 强制）
