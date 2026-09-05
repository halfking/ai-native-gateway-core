# 模型发现全量失败 — 4 表缺少 UNIQUE 约束 + 1 列缺少 DEFAULT

## 现象

本地 PG 初始化 seed 后启动 gateway，模型发现始终输出 `credentials=65, models=0`，日志持续 WARN `failed to upsert model`，错误：

```
ERROR: there is no unique or exclusion constraint matching the ON CONFLICT specification (SQLSTATE 42P10)
ERROR: null value in column "id" of relation "models_canonical" violates not-null constraint (SQLSTATE 23502)
```

## 根因

Go 代码的 `discovery.go:upsertModel` 使用 4 个 `ON CONFLICT` upsert SQL（依次写入 `models_canonical`、`model_aliases`、`provider_models`、`credential_model_bindings`），但相关表的 UNIQUE 约束缺失：

| 表 | 目标列 | 存在 UNIQUE 约束 | 修复 |
|---|---|---|---|
| `models_canonical` | `(canonical_name)` | ❌ 缺少 | `ADD CONSTRAINT uq_models_canonical_name` |
| `model_aliases` | `(raw_name)` | ❌ 缺少 | `ADD CONSTRAINT uq_model_aliases_raw_name` |
| `provider_models` | `(provider_id, raw_model_name)` | ❌ 缺少 | `ADD CONSTRAINT uq_provider_models_provider_raw` |
| `credential_model_bindings` | `(credential_id, provider_model_id)` | ❌ 缺少 | `ADD CONSTRAINT uq_cmb_credential_provider_model` |

此外，`models_canonical.id` 的类型是 `bigint NOT NULL` 且无 DEFAULT 值，INSERT 不指定 id 时返回 `null value in column "id"`。虽然存在同名的 SEQUENCE `models_canonical_id_seq`，但从未通过 `ALTER COLUMN SET DEFAULT` 绑定为列的默认值。

## 修复

共 5 条 `ALTER TABLE`：

```sql
ALTER TABLE credential_model_bindings
  ADD CONSTRAINT uq_cmb_credential_provider_model UNIQUE (credential_id, provider_model_id);

ALTER TABLE provider_models
  ADD CONSTRAINT uq_provider_models_provider_raw UNIQUE (provider_id, raw_model_name);

ALTER TABLE models_canonical
  ADD CONSTRAINT uq_models_canonical_name UNIQUE (canonical_name);

ALTER TABLE models_canonical
  ALTER COLUMN id SET DEFAULT nextval('models_canonical_id_seq');

ALTER TABLE model_aliases
  ADD CONSTRAINT uq_model_aliases_raw_name UNIQUE (raw_name);
```

## 验证

重启后模型发现输出 `credentials=65, models=1500`，`/healthz` 返回 `{"status":"ok"}`，chat-completion 请求正常路由。

## 文件

- `deploy/sql/docs/features/2026-07-25-model-discovery-fix-constraints.sql` — SQL 迁移脚本
