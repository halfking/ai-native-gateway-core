# 附录 B — 索引差异逐条配对报告（子代理产出，2026-10-12）

## RENAMED_PAIR — 14 条（本地名 → 生产名；同表、列与顺序相同、唯一性相同、谓词相同）

| 表 | 本地索引名 | 生产索引名 | 列(顺序一致) | 唯一 | WHERE |
|---|---|---|---|---|---|
| api_key_auto_profile | api_key_auto_profile_pkey | api_key_auto_profile_api_key_id_key | (api_key_id) | UNIQ | - |
| candidate_failure_logs_hot | idx_cfl_hot_cred_ts | candidate_failure_logs_hot_credential_id_ts_idx | (credential_id, ts DESC) | - | - |
| candidate_failure_logs_hot | idx_cfl_hot_model_ts | candidate_failure_logs_hot_raw_model_name_ts_idx | (raw_model_name, ts DESC) | - | - |
| candidate_failure_logs_hot | idx_cfl_hot_provider_ts | candidate_failure_logs_hot_provider_id_ts_idx | (provider_id, ts DESC) | - | - |
| candidate_failure_logs_hot | idx_cfl_hot_request_id | candidate_failure_logs_hot_request_id_idx | (request_id) | - | - |
| durable_llm_tasks | durable_llm_tasks_tenant_request_unique | uq_durable_llm_tasks_tenant_request | (tenant_id, request_id) | UNIQ | - |
| durable_llm_tasks | idx_durable_tasks_session_request | idx_durable_llm_tasks_session_request | (session_id, request_id, updated_at DESC) | - | - |
| durable_llm_tasks | idx_durable_tasks_tenant_status | idx_durable_llm_tasks_tenant_status | (tenant_id, status) | - | - |
| session_turns_hot | session_turns_hot_request_id_idx | idx_session_turns_hot_request | (request_id) | - | - |
| session_turns_hot | session_turns_hot_tenant_id_namespace_ts_idx | idx_session_turns_hot_tenant_namespace | (tenant_id, namespace, ts DESC) | - | namespace IS NOT NULL |
| session_turns_hot | session_turns_hot_tenant_id_parent_request_id_ts_idx | idx_session_turns_hot_tenant_parent_request | (tenant_id, parent_request_id, ts DESC) | - | parent_request_id IS NOT NULL |
| session_turns_hot | session_turns_hot_tenant_id_project_id_ts_idx | idx_session_turns_hot_tenant_project | (tenant_id, project_id, ts DESC) | - | project_id IS NOT NULL |
| session_turns_hot | session_turns_hot_tenant_id_task_type_ts_idx | idx_session_turns_hot_tenant_task_type | (tenant_id, task_type, ts DESC) | - | task_type IS NOT NULL |
| session_turns_hot | session_turns_hot_tenant_id_ts_idx | idx_session_turns_hot_tenant_ts | (tenant_id, ts DESC) | - | - |

旁证：生产侧新名在仓库 `sql/migrations/` 与 `deploy/sql/migrations/` 均搜不到，说明本地库才是按当前仓库迁移建的。

## LOCAL_REAL_EXTRA — 16 条（本地多出）

| 表 | 本地索引名 | 列 | 唯一 | WHERE | 备注 |
|---|---|---|---|---|---|
| credential_probe_queue | idx_credential_probe_queue_automatic_claim | (automatic, priority DESC, next_run_at, id) | - | status='ready' | 生产完全没有 → 已列生产侧补齐项 |
| durable_llm_tasks | idx_durable_tasks_deadline | (deadline_at) | - | status<>ALL(终态4) | 与生产同名对但谓词不同（durable 休眠代差，不修） |
| durable_llm_tasks | idx_durable_tasks_lease_expiry | (lease_until) | - | lease_until IS NOT NULL AND status<>ALL(终态4) | 同上 |
| durable_llm_tasks | idx_durable_tasks_runnable | (status, next_retry_at) | - | status=ANY(waiting_recovery,retry_scheduled,running) | 同上 |
| durable_llm_tasks | idx_durable_tasks_unsafe_checkpoint | (updated_at) | - | commit_state=ANY(content,tool_call,terminal) AND status<>ALL(终态4) | 同上 |
| free_resource_catalog | free_resource_catalog_provider_code_model_id_tenant_id_key | (provider_code, model_id, tenant_id) | UNIQ | - | 生产同列集但顺序 (tenant_id,provider_code,model_id)，btree 顺序不同不配对 |
| keyless_providers | keyless_providers_provider_tenant_key | (provider_code, tenant_id) | UNIQ | - | 生产同列集但顺序 (tenant_id,provider_code) |
| request_logs | idx_request_logs_token_band_ts | (token_band, ts DESC) | - | token_band IS NOT NULL | 生产缺（分区父表，延后项） |
| session_bodies | session_bodies_request_id_partition_date_key | (request_id, partition_date) | UNIQ | - | 生产版加 tenant_id；本地旧键保留 |
| session_bodies | session_bodies_session_id_turn_no_partition_date_key | (session_id, turn_no, partition_date) | UNIQ | - | 同上 |
| session_bodies_hot | session_bodies_hot_request_id_idx | (request_id) | - | - | 生产为 (request_id, tenant_id) |
| session_bodies_hot | session_bodies_hot_request_id_partition_date_key | (request_id, partition_date) | UNIQ | - | 生产无 |
| session_bodies_hot | session_bodies_hot_session_id_turn_no_idx | (session_id, turn_no DESC) | - | - | 生产无 |
| session_bodies_hot | session_bodies_hot_session_id_turn_no_partition_date_key | (session_id, turn_no, partition_date) | UNIQ | - | 生产无 |
| session_turns_hot | session_turns_hot_session_id_turn_no_idx | (session_id, turn_no DESC) | - | - | 生产只有带 tenant_id 版 |
| ursm_node_snapshot_min | ursm_node_snapshot_min_ts_idx | (snapshot_ts) | - | - | 生产无（830 分区血统） |

## PROD_REAL_MISSING_LOCAL — 39 条（本地缺）

26 条有 creator（→ 已纳入 02-local-fix-extra.sql C 节）：518/527（handoff idx×2）、
621（ped）、478p2（hot affinity×2）、511（state_transitions）、476（tenant unique×2 + 2 约束）、
614（session_bodies_hot×2）、526（session_turns_hot×5）、718（cfl_hot session）、
544/548/539（stats×3）。
13 条 prod-manual/运维手建（auto_combo×2、durable×2、free_quota×1、free_resource×3、
keyless×3、cfl 父表 deploy-V359 系×5 中 2 条无 startup 出处）：与复刻列配套的唯一索引随
02 脚本 B7/B8 补齐，其余纯性能索引不复制。

## DEF_DIFF — 3 条逐条判定

| 索引 | 判定 | 说明 | 出处 |
|---|---|---|---|
| idx_request_logs_provider_tool_calls | 谓词语义真差 | 生产 WHERE 多 `jsonb_typeof(tool_calls)='array'` 守卫，防非数组 jsonb 使 jsonb_array_length 报错；本地缺 → 本轮修（02 脚本 C2） | startup/605 |
| idx_request_logs_rate_limit_status | 纯 deparse 格式差 | 数组 cast 写法不同，值集合与语义完全一致 | startup/443 |
| ursm_node_snapshot_min_pkey | 结构差 | 生产 ON ONLY=已分区（830.skip 手工），本地 heap | startup/463+830 |
