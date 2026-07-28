# 数据模型与 DDL

> 本文所有表均为 `TARGET` 草案，未创建 migration、未接入 writer。正式 migration 编号、部署顺序和 down 必须在实施阶段评审后确定。

## 1. 设计约束

- 所有表有 `tenant_id`；派生结果不能脱离 tenant/session scope；
- 原始输入通过 `input_hash`、`source_ref`、`body_ref` 引用，不复制未脱敏全文；
- 结果不可静默覆盖：使用 `result_version`、`model_version`、`created_at` 和状态；
- 人工 split/merge 使用 append-only event + override，不修改历史 cluster run；
- projection 可从 canonical result/event 重建；
- retention、legal hold、soft delete 和 RLS 必须与既有 session 契约一致。

## 2. 核心实体

### 2.1 会话与 turn 规范化

```sql
CREATE TABLE semantic_sessions (
  tenant_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  canonical_source TEXT NOT NULL, -- request_logs | gateway_v2 | merged
  first_seen_at TIMESTAMPTZ,
  last_seen_at TIMESTAMPTZ,
  turn_count INT NOT NULL DEFAULT 0,
  input_revision BIGINT NOT NULL DEFAULT 0,
  current_result_version BIGINT,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, session_id)
);

CREATE TABLE semantic_turns (
  tenant_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  turn_no INT NOT NULL,
  request_id TEXT NOT NULL,
  source_ref JSONB NOT NULL, -- table/id/body refs
  redacted_text TEXT,
  text_sha256 TEXT NOT NULL,
  role_counts JSONB NOT NULL DEFAULT '{}'::jsonb,
  boundary_score NUMERIC(6,5),
  created_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (tenant_id, session_id, turn_no),
  UNIQUE (tenant_id, request_id)
);
```

`redacted_text` 是受 retention/权限控制的分析副本；没有必要保存正文时只保留 hash 和 ref。

### 2.2 语义结果和候选标签

```sql
CREATE TABLE semantic_results (
  result_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  turn_start INT,
  turn_end INT,
  module_id TEXT NOT NULL, -- summary/title/intent/entity/boundary
  result_version BIGINT NOT NULL,
  input_hash TEXT NOT NULL,
  source_kind TEXT NOT NULL, -- rule | embedding | llm | human
  model_version TEXT,
  status TEXT NOT NULL DEFAULT 'completed',
  payload JSONB NOT NULL,
  evidence JSONB NOT NULL DEFAULT '[]'::jsonb,
  confidence NUMERIC(6,5),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, session_id, module_id, result_version, input_hash)
);

CREATE TABLE semantic_labels (
  label_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id TEXT NOT NULL,
  namespace TEXT NOT NULL, -- task | project | role | domain
  label_key TEXT NOT NULL,
  display_name TEXT NOT NULL,
  parent_label_id UUID,
  active BOOLEAN NOT NULL DEFAULT true,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  UNIQUE (tenant_id, namespace, label_key)
);

CREATE TABLE semantic_label_candidates (
  candidate_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  result_id UUID NOT NULL REFERENCES semantic_results(result_id),
  label_id UUID REFERENCES semantic_labels(label_id),
  namespace TEXT NOT NULL,
  proposed_key TEXT,
  score NUMERIC(6,5),
  evidence JSONB NOT NULL DEFAULT '[]'::jsonb,
  state TEXT NOT NULL DEFAULT 'candidate', -- candidate/accepted/rejected/stale
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 2.3 实体、关系和聚类

```sql
CREATE TABLE semantic_entities (
  entity_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id TEXT NOT NULL,
  canonical_name TEXT NOT NULL,
  entity_type TEXT NOT NULL, -- project/person/service/repo/model/issue
  aliases TEXT[] NOT NULL DEFAULT '{}',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  valid_from TIMESTAMPTZ,
  valid_to TIMESTAMPTZ,
  UNIQUE (tenant_id, entity_type, canonical_name)
);

CREATE TABLE semantic_relations (
  relation_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id TEXT NOT NULL,
  subject_id UUID NOT NULL REFERENCES semantic_entities(entity_id),
  predicate TEXT NOT NULL,
  object_id UUID NOT NULL REFERENCES semantic_entities(entity_id),
  result_id UUID REFERENCES semantic_results(result_id),
  confidence NUMERIC(6,5),
  source_kind TEXT NOT NULL,
  valid_from TIMESTAMPTZ,
  valid_to TIMESTAMPTZ,
  state TEXT NOT NULL DEFAULT 'candidate',
  evidence JSONB NOT NULL DEFAULT '[]'::jsonb,
  UNIQUE (tenant_id, subject_id, predicate, object_id, result_id)
);

CREATE TABLE semantic_clusters (
  cluster_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id TEXT NOT NULL,
  cluster_run_id UUID NOT NULL,
  stable_project_id UUID,
  label TEXT,
  topic_path TEXT[] NOT NULL DEFAULT '{}',
  algorithm TEXT NOT NULL,
  algorithm_version TEXT NOT NULL,
  parameters JSONB NOT NULL DEFAULT '{}'::jsonb,
  state TEXT NOT NULL DEFAULT 'candidate', -- candidate/accepted/archived
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE semantic_cluster_members (
  cluster_id UUID NOT NULL REFERENCES semantic_clusters(cluster_id),
  tenant_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  score NUMERIC(8,6),
  membership_source TEXT NOT NULL, -- model/human/override
  result_id UUID REFERENCES semantic_results(result_id),
  PRIMARY KEY (cluster_id, session_id)
);
```

`cluster_run_id` 代表一次算法运行；`stable_project_id` 只有受控确认后才可使用。noise 会以 `cluster_id` 或未归类状态保留，不强迫归档。

### 2.4 人工覆盖、变更和统计

```sql
CREATE TABLE semantic_membership_overrides (
  override_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  operation TEXT NOT NULL, -- include/exclude/split/merge/label/undo
  from_cluster_id UUID,
  to_cluster_id UUID,
  stable_project_id UUID,
  reason TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  effective_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at TIMESTAMPTZ,
  UNIQUE (tenant_id, idempotency_key)
);

CREATE TABLE semantic_change_events (
  event_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  aggregate_type TEXT NOT NULL,
  aggregate_id TEXT NOT NULL,
  actor_id TEXT,
  correlation_id TEXT,
  idempotency_key TEXT NOT NULL,
  payload JSONB NOT NULL,
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, idempotency_key)
);

CREATE TABLE semantic_daily_stats (
  tenant_id TEXT NOT NULL,
  stat_date DATE NOT NULL,
  stable_project_id UUID,
  task_label TEXT,
  role_label TEXT,
  session_count INT NOT NULL,
  turn_count INT NOT NULL,
  total_tokens BIGINT NOT NULL,
  total_cost_usd NUMERIC(14,8) NOT NULL,
  success_rate NUMERIC(8,6),
  avg_quality_score NUMERIC(8,6),
  source_revision BIGINT NOT NULL,
  generated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, stat_date, stable_project_id, task_label, role_label)
);
```

### 2.5 评估数据

```sql
CREATE TABLE semantic_eval_sets (
  eval_set_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id TEXT,
  name TEXT NOT NULL,
  version TEXT NOT NULL,
  task_type TEXT NOT NULL, -- summary/classification/cluster/boundary/memory
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (name, version)
);

CREATE TABLE semantic_eval_items (
  item_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  eval_set_id UUID NOT NULL REFERENCES semantic_eval_sets(eval_set_id),
  source_ref JSONB NOT NULL,
  input_hash TEXT NOT NULL,
  gold JSONB NOT NULL,
  split TEXT NOT NULL DEFAULT 'test'
);

CREATE TABLE semantic_annotations (
  annotation_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  item_id UUID NOT NULL REFERENCES semantic_eval_items(item_id),
  annotator_id TEXT NOT NULL,
  annotation JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

## 3. RLS、索引和保留

正式 migration 必须对所有 `semantic_*` tenant 表启用 RLS，并至少创建：

```sql
CREATE INDEX ON semantic_turns (tenant_id, session_id, turn_no);
CREATE INDEX ON semantic_results (tenant_id, session_id, module_id, created_at DESC);
CREATE INDEX ON semantic_label_candidates (tenant_id, session_id, state);
CREATE INDEX ON semantic_cluster_members (tenant_id, session_id);
CREATE INDEX ON semantic_change_events (tenant_id, occurred_at DESC);
```

租户策略只能使用服务端 `current_setting('app.current_tenant', true)` 或已验证的认证上下文。真实验证要求低权限、`NOSUPERUSER`、`NOBYPASSRLS` 角色；不能用 superuser 测出假阳性。

建议保留：

- 原始事实：按既有 legal hold/retention；
- `semantic_turns` 脱敏副本：短于原始正文，可配置；
- candidate/result：保留模型版本和输入 hash，支持回归；
- `semantic_change_events`：按审计/合规要求长期保留；
- daily stats：长期保留但可从事实重算。

## 4. split/merge 语义

- **split**：创建新的 stable project 或 cluster override，把指定 session/turn scope 迁入新逻辑成员；原 cluster run 不变；
- **merge**：创建目标 stable project，写入 source projects 的 merge event，原 ID 保留为 alias/redirect；
- **undo**：写 inverse event 或 revoke 原 override，不物理删除历史；
- 查询 current projection 时按 event sequence、scope 和人工优先级重放。

## 5. up/down 交付要求

TARGET migration 必须同时提供 up/down：up 创建 schema、表、索引、RLS、审计触发器和 retention job；down 只能在没有 active projection/引用或已完成备份的情况下执行，并保留 `semantic_change_events` 导出。当前文档只冻结模型，不执行 migration。
