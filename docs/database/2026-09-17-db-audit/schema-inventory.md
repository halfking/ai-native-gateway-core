# 表结构全量清单（271 表）

> 由 `.db-audit/parse_schema.py` 自动生成，来源 `sql/schema/01-schema.sql`。
NN = NOT NULL；PK 列已标注。月分区叶子（`*_YYYY_MM`）与根表列结构一致。


## agent_relationships

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| src_agent_id | bigint | Y |  |  |
| dst_agent_id | bigint | Y |  |  |
| rel | text | Y |  |  |
| weight | double precision | Y | 1.0 |  |
| created_at | timestamp with time zone | Y | now() |  |

外键：
- (dst_agent_id) → agents(id)
- (src_agent_id) → agents(id)

索引（2）：
- idx_agent_rel_dst
- idx_agent_rel_src

## agents

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | text | Y |  |  |
| name | text | Y |  |  |
| kind | text | Y |  |  |
| endpoint | text | Y |  |  |
| status | text | Y | 'unknown'::text |  |
| capabilities | jsonb | Y | '{}'::jsonb |  |
| version | text | Y | '0.0.0'::text |  |
| auth_scheme | text |  |  |  |
| last_heartbeat | timestamp |  |  |  |
| registered_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| metadata | jsonb | Y | '{}'::jsonb |  |

索引（4）：
- idx_agents_capabilities
- idx_agents_heartbeat
- idx_agents_kind
- idx_agents_tenant

## analysis_events

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| event_id | text | Y |  |  |
| type | text | Y |  |  |
| tenant_id | text | Y |  |  |
| session_id | text |  |  |  |
| request_id | text |  |  |  |
| payload | jsonb | Y | '{}'::jsonb |  |
| occurred_at | timestamp with time zone | Y | now() |  |
| processed_at | timestamp |  |  |  |
| worker | text |  |  |  |
| attempts | integer | Y | 0 |  |
| last_error | text |  |  |  |
| claimed_at | timestamp |  |  |  |
| claimed_by | text |  |  |  |

索引（3）：
- idx_analysis_events_session
- idx_analysis_events_tenant_type
- idx_analysis_events_unprocessed

## api_key_auto_profile

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| api_key_id | integer | Y |  | PK |
| profile | text | Y | 'smart'::text |  |
| first_chosen_at | timestamp with time zone |  | now() |  |
| last_used_at | timestamp with time zone |  | now() |  |
| updated_at | timestamp with time zone |  | now() |  |

## api_key_model_cost

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| bucket | timestamp with time zone | Y |  |  |
| api_key_id | integer | Y |  |  |
| canonical_id | integer |  |  |  |
| raw_model | text | Y |  |  |
| billing_mode | text |  |  |  |
| requests_total | integer | Y | 0 |  |
| requests_success | integer | Y | 0 |  |
| tokens_input | bigint | Y | 0 |  |
| tokens_output | bigint | Y | 0 |  |
| cost_usd | numeric(12,6) | Y | 0 |  |
| active_concurrent | integer | Y | 0 |  |
| concurrency_limit | integer |  |  |  |
| pressure_ratio | numeric(5,4) |  |  |  |
| score_smart | numeric(8,4) |  |  |  |
| score_speed_first | numeric(8,4) |  |  |  |
| score_cost_first | numeric(8,4) |  |  |  |
| last_request_at | timestamp |  |  |  |
| last_decision_at | timestamp |  |  |  |
| updated_at | timestamp with time zone |  | now() |  |

## api_keys

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| application_id | bigint | Y |  |  |
| tenant_id | text | Y | 'default'::text |  |
| key_hash | text | Y |  |  |
| key_prefix | text | Y |  |  |
| owner_user | text |  |  |  |
| data_sensitivity | text | Y | 'internal'::text |  |
| default_end_user_id | text |  |  |  |
| budget_usd | numeric(14,6) |  |  |  |
| rate_limit_rpm | integer |  |  |  |
| enabled | boolean | Y | true |  |
| expires_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| last_used_at | timestamp |  |  |  |
| status | character varying(16) | Y | 'active'::character |  |
| key_ciphertext | text |  |  |  |
| is_system | boolean | Y | false |  |
| rate_limit_concurrent | integer |  |  |  |
| rate_limit_tpm | integer |  |  |  |
| key_tier | character varying(16) | Y | 'default'::character |  |
| key_ciphertext_kid | text |  |  |  |
| throttled_at | timestamp |  |  |  |
| throttled_reason | text |  |  |  |
| ewma_rpm_baseline | numeric(10,3) |  |  |  |
| ewma_updated_at | timestamp |  |  |  |
| reveal_count | integer | Y | 0 |  |
| last_revealed_at | timestamp |  |  |  |
| last_revealed_by | text |  |  |  |
| remark | text |  |  |  |
| key_alias | text |  |  |  |
| total_requests | bigint | Y | 0 |  |
| total_prompt_tokens | bigint | Y | 0 |  |
| total_completion_tokens | bigint | Y | 0 |  |
| total_tokens | bigint | Y | 0 |  |
| total_cost_usd | numeric(14,8) | Y | 0 |  |
| last_request_at | timestamp |  |  |  |
| default_client_profile | text |  |  |  |

## applications

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | text | Y | 'default'::text |  |
| code | text | Y |  |  |
| display_name | text | Y |  |  |
| owner_user | text |  |  |  |
| data_sensitivity | text | Y | 'internal'::text |  |
| enabled | boolean | Y | true |  |
| notes | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| default_client_profile | text |  |  |  |
| allowed_models_json | jsonb |  |  |  |
| customer_id | bigint |  |  |  |

索引（1）：
- idx_applications_tenant_code

## approval_approvers

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| user_id | character varying(64) | Y |  |  |
| name | character varying(128) | Y |  |  |
| email | character |  |  |  |
| phone | character |  |  |  |
| role | character varying(32) | Y |  |  |
| priority | integer |  | 0 |  |
| enabled | boolean |  | true |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（3）：
- idx_approval_approvers_enabled
- idx_approval_approvers_priority
- idx_approval_approvers_tenant

## approval_configs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| enabled | boolean |  | false |  |
| mode | character varying(32) | Y | 'disabled'::character |  |
| timeout_seconds | integer |  | 3600 |  |
| auto_reject_on_timeout | boolean |  | true |  |
| config | jsonb | Y | '{}'::jsonb |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（2）：
- idx_approval_configs_enabled
- idx_approval_configs_tenant

## approval_queue

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | uuid | Y |  |  |
| session_id | text | Y |  |  |
| tenant_id | text | Y |  |  |
| request_id | text | Y |  |  |
| detect_result | jsonb | Y |  |  |
| snapshot | jsonb | Y |  |  |
| status | text | Y | 'pending'::text |  |
| approved_by | text |  |  |  |
| approved_at | timestamp |  |  |  |
| reason | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| expires_at | timestamp with time zone | Y |  |  |
| resume_state | text | Y | 'idle'::text |  |
| resume_owner | text |  |  |  |
| resume_lease_until | timestamp |  |  |  |
| resume_fencing_token | bigint | Y | 0 |  |
| resume_started_at | timestamp |  |  |  |
| resume_completed_at | timestamp |  |  |  |
| resume_error | text |  |  |  |

索引（4）：
- idx_approval_queue_expires
- idx_approval_queue_resume_claimable
- idx_approval_queue_session
- idx_approval_queue_tenant_pending

## approval_requests

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| request_id | character varying(64) | Y |  |  |
| session_id | character varying(64) | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| trigger_type | character varying(32) | Y |  |  |
| trigger_reason | text |  |  |  |
| risk_level | character varying(16) | Y |  |  |
| session_summary | jsonb |  |  |  |
| sensitive_info | jsonb |  |  |  |
| user_message | text |  |  |  |
| full_context | jsonb |  |  |  |
| estimated_cost | numeric(10,4) |  |  |  |
| estimated_tokens | integer |  |  |  |
| status | character varying(32) | Y | 'pending'::character |  |
| created_at | timestamp with time zone | Y | now() |  |
| expires_at | timestamp with time zone | Y |  |  |
| approved_by | character |  |  |  |
| approved_at | timestamp |  |  |  |
| approval_note | text |  |  |  |
| rejected | boolean |  | false |  |
| rejection_reason | text |  |  |  |
| metadata | jsonb |  | '{}'::jsonb |  |

索引（7）：
- idx_approval_requests_created_at
- idx_approval_requests_expires_at
- idx_approval_requests_request_id UNIQUE
- idx_approval_requests_session_id
- idx_approval_requests_status
- idx_approval_requests_tenant_id
- idx_approval_requests_tenant_status

## approval_routing_rules

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | text | Y | 'default'::text |  |
| rule_name | text | Y |  |  |
| rule_type | text | Y |  |  |
| conditions | jsonb | Y | '{}'::jsonb |  |
| approvers | jsonb | Y | '[]'::jsonb |  |
| enabled | boolean | Y | true |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| risk_level | character |  |  |  |
| channel_type | character |  |  |  |
| approver_ids | jsonb |  | '[]'::jsonb |  |
| priority | integer | Y | 0 |  |

索引（2）：
- idx_approval_routing_risk
- idx_approval_routing_rules_tenant

## approval_rules

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| name | character varying(128) | Y |  |  |
| enabled | boolean |  | true |  |
| priority | integer |  | 0 |  |
| conditions | jsonb | Y |  |  |
| action | jsonb | Y |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（3）：
- idx_approval_rules_enabled
- idx_approval_rules_priority
- idx_approval_rules_tenant

## armor_judgments

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| request_id | text | Y |  |  |
| tenant_id | text | Y |  |  |
| check_type | text | Y |  |  |
| decision | text | Y |  |  |
| source | text | Y |  |  |
| pattern_ids | text[] |  |  |  |
| judge_model | text |  |  |  |
| score | real |  |  |  |
| threshold | real |  |  |  |
| mode | text | Y | 'observe'::text |  |
| latency_ms | integer | Y | 0 |  |
| prompt_sha256 | text |  |  |  |
| snippet | text |  |  |  |
| reason | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

索引（3）：
- idx_armor_judgments_request
- idx_armor_judgments_stats
- idx_armor_judgments_tenant_time

## asset_relationships

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| src_kind | text | Y |  |  |
| src_ref_id | bigint | Y |  |  |
| dst_kind | text | Y |  |  |
| dst_ref_id | bigint | Y |  |  |
| rel | text | Y |  |  |
| weight | double precision | Y | 1.0 |  |
| created_at | timestamp with time zone | Y | now() |  |

外键：
- (dst_kind, dst_ref_id) → assets(kind, ref_id)
- (src_kind, src_ref_id) → assets(kind, ref_id)

索引（2）：
- idx_asset_rel_dst
- idx_asset_rel_src

## assets

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| kind | text | Y |  |  |
| ref_id | bigint | Y |  |  |
| tenant_id | text | Y |  |  |
| name | text | Y |  |  |
| owner | text |  |  |  |
| team | text |  |  |  |
| cost_center | text |  |  |  |
| tags | jsonb | Y | '{}'::jsonb |  |
| health_state | text | Y | 'unknown'::text |  |
| version | text | Y | '0.0.0'::text |  |
| registered_at | timestamp with time zone | Y | now() |  |
| last_seen_at | timestamp |  |  |  |
| metadata | jsonb | Y | '{}'::jsonb |  |

索引（2）：
- idx_assets_tags
- idx_assets_tenant_kind

## attachments

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | text | Y |  |  |
| tenant_id | text | Y |  |  |
| request_id | text | Y |  |  |
| attachment_type | text | Y |  |  |
| media_type | text | Y |  |  |
| file_size | bigint | Y |  |  |
| file_path | text | Y |  |  |
| original_data_type | text | Y |  |  |
| original_url | text |  |  |  |
| content_hash | text | Y |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| metadata | jsonb |  |  |  |

索引（3）：
- idx_attachments_hash
- idx_attachments_request
- idx_attachments_tenant_created

## auto_tune_audit

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| credential_id | bigint | Y |  |  |
| raw_model | text | Y | ''::text |  |
| action | text | Y |  |  |
| old_limit | integer |  |  |  |
| new_limit | integer |  |  |  |
| reason | text |  |  |  |
| peak_concurrent | integer |  |  |  |
| p95_concurrent | numeric(8,2) |  |  |  |
| week_start | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| applied_by | text |  |  |  |

## background_tasks

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | text | Y | 'default'::text |  |
| task_type | text | Y |  |  |
| provider_id | bigint |  |  |  |
| credential_id | bigint |  |  |  |
| status | text | Y | 'running'::text |  |
| request_json | jsonb | Y | '{}'::jsonb |  |
| result_json | jsonb |  |  |  |
| error | text |  |  |  |
| started_at | timestamp with time zone | Y | now() |  |
| finished_at | timestamp |  |  |  |

## background_tasks_duplicates

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | text | Y |  |  |
| task_type | text | Y |  |  |
| provider_id | bigint |  |  |  |
| credential_id | bigint |  |  |  |
| status | text | Y |  |  |
| request_json | jsonb | Y |  |  |
| result_json | jsonb |  |  |  |
| error | text |  |  |  |
| started_at | timestamp with time zone | Y |  |  |
| finished_at | timestamp |  |  |  |
| removed_at | timestamp with time zone |  | now() |  |

## billing_orders

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| order_no | character varying(64) | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| order_type | character varying(16) | Y |  |  |
| status | character varying(16) | Y | 'pending'::character |  |
| amount_cents | integer | Y |  |  |
| credits | bigint | Y |  |  |
| plan_id | integer |  |  |  |
| package_id | integer |  |  |  |
| payment_channel | character varying(16) | Y | 'alipay'::character |  |
| qr_payload | text | Y | ''::text |  |
| qr_url | text | Y | ''::text |  |
| paid_at | timestamp |  |  |  |
| expires_at | timestamp with time zone | Y |  |  |
| note | text | Y | ''::text |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（2）：
- idx_billing_orders_status
- idx_billing_orders_tenant

## canary_tokens

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| tenant_id | character varying(255) | Y | 'default'::character |  |
| token_value | character varying(255) | Y |  |  |
| token_type | character varying(50) |  | 'uuid'::character |  |
| token_name | character |  |  |  |
| prompt_template_id | character |  |  |  |
| description | text |  |  |  |
| leak_action | public.injection_action |  | 'block'::public.injection_action |  |
| notify_on_leak | boolean |  | true |  |
| active | boolean |  | true |  |
| expires_at | timestamp |  |  |  |
| times_injected | integer |  | 0 |  |
| times_leaked | integer |  | 0 |  |
| last_leaked_at | timestamp |  |  |  |
| created_at | timestamp with time zone |  | now() |  |
| updated_at | timestamp with time zone |  | now() |  |
| created_by | character |  |  |  |

索引（1）：
- idx_canary_tokens_tenant

## candidate_failure_logs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint |  |  |  |
| request_id | text |  |  |  |
| ts | timestamp |  |  |  |
| tenant_id | text |  |  |  |
| credential_id | integer |  |  |  |
| provider_id | integer |  |  |  |
| raw_model_name | text |  |  |  |
| attempt_index | integer |  |  |  |
| error_kind | text |  |  |  |
| error_message | text |  |  |  |
| upstream_status_code | integer |  |  |  |
| upstream_response_body | text |  |  |  |
| upstream_response_preview | text |  |  |  |
| latency_ms | integer |  |  |  |
| retryable | boolean |  |  |  |
| context | jsonb |  |  |  |
| per_attempt_latency_ms | integer |  |  |  |
| extracted_upstream_status_code | integer |  |  |  |
| diagnosed_error_kind | text |  |  |  |

索引（4）：
- idx_candidate_failure_logs_cred_ts
- idx_candidate_failure_logs_model_ts
- idx_candidate_failure_logs_provider_ts
- idx_candidate_failure_logs_req

## center_commands

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| command_id | text | Y |  |  |
| instance_id | text | Y |  |  |
| command | text | Y |  |  |
| args | jsonb |  |  |  |
| status | text | Y | 'pending'::text |  |
| issued_at | timestamp with time zone | Y |  |  |
| issued_by | text | Y |  |  |
| expires_at | timestamp |  |  |  |
| executed_at | timestamp |  |  |  |
| result | jsonb |  |  |  |
| envelope_signature | text |  |  |  |

索引（4）：
- idx_cc_command_id
- idx_cc_instance
- idx_cc_instance_command UNIQUE
- idx_cc_status

## compression_bench_results

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| row_id | bigint |  |  |  |
| request_id | text |  |  |  |
| tenant_id | text |  |  |  |
| gw_session_id | text |  |  |  |
| ts | timestamp |  |  |  |
| bytes_before | integer |  |  |  |
| tokens_before | integer |  |  |  |
| msgs_before | integer |  |  |  |
| bytes_after | integer |  |  |  |
| tokens_after | integer |  |  |  |
| msgs_after | integer |  |  |  |
| bytes_ratio | double |  |  |  |
| tokens_ratio | double |  |  |  |
| msgs_ratio | double |  |  |  |
| bytes_saved | integer |  |  |  |
| tokens_saved | integer |  |  |  |
| msgs_saved | integer |  |  |  |
| strategy | text |  |  |  |
| window_triggered | text |  |  |  |
| summary_marker | text |  |  |  |
| degraded | boolean |  |  |  |
| lossiness | text |  |  |  |
| protocol | text |  |  |  |
| created_at | timestamp with time zone |  | now() |  |

## credential_capabilities

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| credential_id | bigint | Y |  |  |
| capability | text | Y |  |  |
| supported | boolean | Y | false |  |
| last_tested_at | timestamp |  |  |  |
| evidence_json | jsonb |  |  |  |

## credential_health_checks

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| run_id | bigint |  |  |  |
| tenant_id | text | Y | 'default'::text |  |
| provider_id | bigint | Y |  |  |
| credential_id | bigint | Y |  |  |
| models_ok | boolean | Y | false |  |
| probe_ok | boolean | Y | false |  |
| health_status | text | Y |  |  |
| warning_code | text |  |  |  |
| classification_reason | text |  |  |  |
| models_failure_reason | text |  |  |  |
| models_http_status | integer |  |  |  |
| probe_http_status | integer |  |  |  |
| models_latency_ms | integer |  |  |  |
| probe_latency_ms | integer |  |  |  |
| probe_model | text |  |  |  |
| models_error | text |  |  |  |
| probe_error | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

## credential_model_bindings

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| credential_id | bigint | Y |  |  |
| provider_model_id | bigint | Y |  |  |
| routing_tier | smallint |  | 2 |  |
| weight | smallint |  | 100 |  |
| manual_priority | smallint |  | 99 |  |
| success_rate | numeric |  |  |  |
| p95_latency_ms | integer |  |  |  |
| active_sessions | integer |  | 0 |  |
| consecutive_failures | integer |  | 0 |  |
| unit_price_in_per_1m | numeric |  |  |  |
| unit_price_out_per_1m | numeric |  |  |  |
| cache_read_price_per_1m | numeric |  |  |  |
| cache_write_price_per_1m | numeric |  |  |  |
| currency | text |  | 'USD'::text |  |
| billing_mode | text |  | 'per_token'::text |  |
| pricing_source | text |  |  |  |
| pricing_updated_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| available | boolean | Y | true |  |
| unavailable_reason | text |  |  |  |
| unavailable_at | timestamp |  |  |  |
| plan_meta | jsonb | Y | '{}'::jsonb |  |
| admin_protected | boolean | Y | false |  |
| unavailable_recover_at | timestamp |  |  |  |
| transient_failure_count | integer |  | 0 |  |
| pending_verification | boolean |  | false |  |
| plan_type_origin | text |  |  |  |
| plan_type_updated_at | timestamp |  |  |  |
| context_window_override | integer |  |  |  |
| priority | boolean | Y | false |  |

索引（4）：
- idx_cmb_credential_provider_model
- idx_cmb_pending_verification
- idx_cmb_plan_type_origin
- idx_cmb_unavailable_recover_at

## credential_model_call_history

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| credential_id | bigint | Y |  |  |
| raw_model | text | Y |  |  |
| window_start | timestamp with time zone | Y |  |  |
| total_calls | integer | Y | 0 |  |
| success_calls | integer | Y | 0 |  |
| failed_calls | integer | Y | 0 |  |
| avg_latency_ms | numeric(8,2) |  |  |  |
| p95_latency_ms | integer |  |  |  |
| p99_latency_ms | integer |  |  |  |
| error_rate_limit_count | integer | Y | 0 |  |
| error_quota_count | integer | Y | 0 |  |
| error_concurrent_count | integer | Y | 0 |  |
| error_network_count | integer | Y | 0 |  |
| error_auth_count | integer | Y | 0 |  |
| error_other_count | integer | Y | 0 |  |
| avg_concurrent | numeric(5,2) |  |  |  |
| peak_concurrent | integer |  |  |  |
| created_at | timestamp with time zone |  | now() |  |

索引（3）：
- idx_call_history_cred_time
- idx_call_history_errors
- idx_call_history_model_time

## credential_model_index

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| bucket | timestamp with time zone | Y |  |  |
| credential_id | bigint | Y |  |  |
| raw_model | text | Y |  |  |
| canonical_id | integer |  |  |  |
| billing_mode | text |  |  |  |
| unit_price_in_per_1m | numeric(10,4) |  |  |  |
| unit_price_out_per_1m | numeric(10,4) |  |  |  |
| context_window | integer |  |  |  |
| success_rate | numeric(5,4) |  |  |  |
| p95_latency_ms | integer |  |  |  |
| active_sessions | integer |  | 0 |  |
| concurrency_limit | integer |  |  |  |
| pressure_ratio | numeric(5,4) |  |  |  |
| score_smart | numeric(8,4) |  |  |  |
| score_speed_first | numeric(8,4) |  |  |  |
| score_cost_first | numeric(8,4) |  |  |  |
| updated_at | timestamp with time zone |  | now() |  |

## credential_model_index_2026_07

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| bucket | timestamp with time zone | Y |  |  |
| credential_id | bigint | Y |  |  |
| raw_model | text | Y |  |  |
| canonical_id | integer |  |  |  |
| billing_mode | text |  |  |  |
| unit_price_in_per_1m | numeric(10,4) |  |  |  |
| unit_price_out_per_1m | numeric(10,4) |  |  |  |
| context_window | integer |  |  |  |
| success_rate | numeric(5,4) |  |  |  |
| p95_latency_ms | integer |  |  |  |
| active_sessions | integer |  | 0 |  |
| concurrency_limit | integer |  |  |  |
| pressure_ratio | numeric(5,4) |  |  |  |
| score_smart | numeric(8,4) |  |  |  |
| score_speed_first | numeric(8,4) |  |  |  |
| score_cost_first | numeric(8,4) |  |  |  |
| updated_at | timestamp with time zone |  | now() |  |

索引（1）：
- credential_model_index_2026_0_bucket_credential_id_raw_mod_idx1 UNIQUE

## credential_model_index_2026_08

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| bucket | timestamp with time zone | Y |  |  |
| credential_id | bigint | Y |  |  |
| raw_model | text | Y |  |  |
| canonical_id | integer |  |  |  |
| billing_mode | text |  |  |  |
| unit_price_in_per_1m | numeric(10,4) |  |  |  |
| unit_price_out_per_1m | numeric(10,4) |  |  |  |
| context_window | integer |  |  |  |
| success_rate | numeric(5,4) |  |  |  |
| p95_latency_ms | integer |  |  |  |
| active_sessions | integer |  | 0 |  |
| concurrency_limit | integer |  |  |  |
| pressure_ratio | numeric(5,4) |  |  |  |
| score_smart | numeric(8,4) |  |  |  |
| score_speed_first | numeric(8,4) |  |  |  |
| score_cost_first | numeric(8,4) |  |  |  |
| updated_at | timestamp with time zone |  | now() |  |

索引（1）：
- credential_model_index_2026_0_bucket_credential_id_raw_mod_idx2 UNIQUE

## credential_model_index_archive

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| bucket | timestamp with time zone | Y |  |  |
| credential_id | bigint | Y |  |  |
| raw_model | text | Y |  |  |
| canonical_id | integer |  |  |  |
| billing_mode | text |  |  |  |
| unit_price_in_per_1m | numeric(10,4) |  |  |  |
| unit_price_out_per_1m | numeric(10,4) |  |  |  |
| context_window | integer |  |  |  |
| success_rate | numeric(5,4) |  |  |  |
| p95_latency_ms | integer |  |  |  |
| active_sessions | integer |  | 0 |  |
| concurrency_limit | integer |  |  |  |
| pressure_ratio | numeric(5,4) |  |  |  |
| score_smart | numeric(8,4) |  |  |  |
| score_speed_first | numeric(8,4) |  |  |  |
| score_cost_first | numeric(8,4) |  |  |  |
| updated_at | timestamp with time zone |  | now() |  |

## credential_model_index_hot

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| bucket | timestamp with time zone | Y |  |  |
| credential_id | bigint | Y |  |  |
| raw_model | text | Y |  |  |
| canonical_id | integer |  |  |  |
| billing_mode | text |  |  |  |
| unit_price_in_per_1m | numeric(10,4) |  |  |  |
| unit_price_out_per_1m | numeric(10,4) |  |  |  |
| context_window | integer |  |  |  |
| success_rate | numeric(5,4) |  |  |  |
| p95_latency_ms | integer |  |  |  |
| active_sessions | integer |  | 0 |  |
| concurrency_limit | integer |  |  |  |
| pressure_ratio | numeric(5,4) |  |  |  |
| score_smart | numeric(8,4) |  |  |  |
| score_speed_first | numeric(8,4) |  |  |  |
| score_cost_first | numeric(8,4) |  |  |  |
| updated_at | timestamp with time zone |  | now() |  |

索引（5）：
- credential_model_index_hot_bucket_credential_id_raw_model_idx UNIQUE
- credential_model_index_hot_canonical_id_idx
- credential_model_index_hot_credential_id_idx
- credential_model_index_hot_unique_key UNIQUE
- credential_model_index_hot_updated_at_idx

## credential_model_peak_1m

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| bucket | timestamp with time zone | Y |  |  |
| credential_id | bigint | Y |  |  |
| raw_model | text | Y | ''::text |  |
| peak_concurrent | integer | Y | 0 |  |
| avg_concurrent | numeric(8,2) | Y | 0 |  |
| sample_count | integer | Y | 0 |  |

## credential_model_stats_1m

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| bucket | timestamp with time zone | Y |  |  |
| credential_id | bigint | Y |  |  |
| canonical_id | bigint |  |  |  |
| raw_model | text | Y | ''::text |  |
| requests | integer | Y | 0 |  |
| successes | integer | Y | 0 |  |
| failures | integer | Y | 0 |  |
| latency_p50_ms | integer |  |  |  |
| latency_p95_ms | integer |  |  |  |
| latency_p99_ms | integer |  |  |  |
| prompt_tokens | bigint | Y | 0 |  |
| completion_tokens | bigint | Y | 0 |  |
| cost_usd | numeric(14,8) | Y | 0 |  |
| error_counts | jsonb | Y | '{}'::jsonb |  |

## credential_model_weekly_peak

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| week_start | timestamp with time zone | Y |  |  |
| credential_id | bigint | Y |  |  |
| raw_model | text | Y | ''::text |  |
| peak_concurrent | integer | Y | 0 |  |
| peak_concurrent_5min | integer | Y | 0 |  |
| p95_concurrent | numeric(8,2) | Y | 0 |  |
| avg_concurrent | numeric(8,2) | Y | 0 |  |
| total_requests | bigint | Y | 0 |  |
| sample_days | integer | Y | 0 |  |
| current_limit | integer | Y | 0 |  |
| suggested_limit | integer |  |  |  |
| suggestion_reason | text |  |  |  |
| updated_at | timestamp with time zone | Y | now() |  |

## credential_probe_configs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| credential_id | bigint | Y |  |  |
| probe_model | text | Y |  |  |
| priority | integer |  | 1 |  |
| enabled | boolean |  | true |  |
| created_at | timestamp with time zone |  | now() |  |

外键：
- (credential_id) → credentials(id)

## credential_probe_model_log

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | text | Y | 'default'::text |  |
| credential_id | bigint | Y |  |  |
| source | text | Y |  |  |
| old_model | text |  |  |  |
| new_model | text |  |  |  |
| actor | text |  |  |  |
| reason | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

索引（1）：
- idx_credential_probe_model_log_created

## credential_probes

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| credential_id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| probe_model | text | Y |  |  |
| success | boolean | Y |  |  |
| http_status | integer |  |  |  |
| latency_ms | integer |  |  |  |
| error_kind | text |  |  |  |
| error_message | text |  |  |  |
| response_preview | text |  |  |  |
| triggered_by | text |  | 'scheduled'::text |  |
| created_at | timestamp with time zone |  | now() |  |

外键：
- (credential_id) → credentials(id)

索引（2）：
- idx_credential_probes_cred_time
- idx_credential_probes_success

## credential_quota_usage

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| quota_id | bigint | Y |  |  |
| window_started_at | timestamp with time zone | Y |  |  |
| window_ends_at | timestamp with time zone | Y |  |  |
| used_total_tokens | bigint | Y | 0 |  |
| used_input_tokens | bigint | Y | 0 |  |
| used_output_tokens | bigint | Y | 0 |  |
| used_requests | bigint | Y | 0 |  |
| used_cost_usd | numeric(18,8) | Y | 0 |  |
| last_event_at | timestamp |  |  |  |
| exhausted | boolean | Y | false |  |

## credential_quotas

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| credential_id | bigint | Y |  |  |
| quota_name | text | Y |  |  |
| window_type | text | Y |  |  |
| starts_at | timestamp |  |  |  |
| ends_at | timestamp |  |  |  |
| period | text |  |  |  |
| cron_expr | text |  |  |  |
| timezone | text | Y | 'UTC'::text |  |
| reset_anchor_local | time |  |  |  |
| rolling_seconds | integer |  |  |  |
| cap_total_tokens | bigint |  |  |  |
| cap_input_tokens | bigint |  |  |  |
| cap_output_tokens | bigint |  |  |  |
| cap_requests | bigint |  |  |  |
| cap_cost_usd | numeric(14,6) |  |  |  |
| unlimited_in_window | boolean | Y | false |  |
| enabled | boolean | Y | true |  |
| priority | integer | Y | 100 |  |
| notes | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

## credential_state_log

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| credential_id | integer | Y |  |  |
| raw_model_name | text | Y |  |  |
| available | boolean |  |  |  |
| health_status | text |  |  |  |
| latency_ms | integer |  |  |  |
| last_success_at | timestamp |  |  |  |
| last_failure_at | timestamp |  |  |  |
| last_error | text |  |  |  |
| recover_at | timestamp |  |  |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（2）：
- idx_credential_state_log_credential_id
- idx_credential_state_log_updated_at

## credentials

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| tenant_id | text | Y | 'default'::text |  |
| label | text | Y |  |  |
| secret_ciphertext | bytea |  |  |  |
| secret_kid | text |  |  |  |
| trust_level | text | Y | 'trusted'::text |  |
| status | text | Y | 'active'::text |  |
| concurrency_limit | integer |  |  |  |
| effective_concurrency | integer |  |  |  |
| balance_usd | numeric(14,6) |  |  |  |
| pricing_distrust | boolean | Y | false |  |
| relay_overhead_ms | integer |  |  |  |
| active_plan_id | bigint |  |  |  |
| plan_consumed_json | jsonb | Y | '{}'::jsonb |  |
| api_models_ok | boolean |  |  |  |
| api_models_last_checked_at | timestamp |  |  |  |
| api_models_error | text |  |  |  |
| last_used_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| circuit_state | text |  | 'closed'::text |  |
| circuit_opened_at | timestamp |  |  |  |
| consecutive_failures | integer |  | 0 |  |
| cooling_until | timestamp |  |  |  |
| circuit_open_count_window | integer |  | 0 |  |
| circuit_window_started_at | timestamp |  |  |  |
| effective_at | timestamp |  |  |  |
| expires_at | timestamp |  |  |  |
| tags | jsonb |  | '[]'::jsonb |  |
| notes | text |  |  |  |
| health_status | text | Y | 'unknown'::text |  |
| health_checked_at | timestamp |  |  |  |
| health_source | text |  |  |  |
| health_warning_code | text |  |  |  |
| health_error | text |  |  |  |
| health_latency_ms | integer |  |  |  |
| health_probe_model | text |  |  |  |
| lifecycle_status | text | Y | 'active'::text |  |
| availability_state | text | Y | 'ready'::text |  |
| quota_state | text | Y | 'ok'::text |  |
| state_reason_code | text |  |  |  |
| state_reason_detail | text |  |  |  |
| state_updated_at | timestamp |  |  |  |
| availability_recover_at | timestamp |  |  |  |
| quota_recover_at | timestamp |  |  |  |
| balance_currency | text |  | 'USD'::text |  |
| balance_last_checked_at | timestamp |  |  |  |
| balance_check_endpoint | text |  |  |  |
| balance_floor_usd | numeric(14,6) |  |  |  |
| quota_floor_tokens | bigint |  |  |  |
| quota_floor_percent | numeric(5,2) |  |  |  |
| plan_quota_kind | text |  |  |  |
| plan_quota_windows | jsonb |  |  |  |
| plan_quota_remaining_tokens | bigint |  |  |  |
| plan_quota_used_percent | numeric(5,2) |  |  |  |
| plan_quota_checked_at | timestamp |  |  |  |
| pool_group | text |  |  |  |
| acquisition_source | text |  |  |  |
| acquisition_detail | text |  |  |  |
| manual_disabled | boolean | Y | false |  |
| default_probe_model | text |  |  |  |
| default_probe_model_source | text |  |  |  |
| default_probe_model_picked_at | timestamp |  |  |  |
| concurrency_limit_auto | integer |  |  |  |
| concurrency_mode | text |  |  |  |
| max_queue_depth | integer |  |  |  |
| max_queue_wait_ms | integer |  |  |  |
| fp_slot_limit | integer | Y |  |  |
| probe_enabled | boolean |  | true |  |
| probe_interval_sec | integer |  | 300 |  |
| last_probe_at | timestamp |  |  |  |
| last_probe_success | boolean |  |  |  |
| probe_consecutive_failures | integer |  | 0 |  |
| probe_failure_threshold | integer |  | 3 |  |
| plan_type | text |  |  |  |
| plan_type_updated_at | timestamp |  |  |  |
| rpm_limit | integer |  |  |  |
| tpm_limit | integer |  |  |  |
| revision | bigint | Y | 0 |  |
| auto_disabled_at | timestamp |  |  |  |
| auto_disabled_reason | text |  |  |  |
| auto_enabled_at | timestamp |  |  |  |
| auto_enabled_reason | text |  |  |  |

索引（3）：
- idx_credentials_auto_limit
- idx_credentials_plan_type
- credentials_revision_idx

## credit_ledger

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | character varying | Y |  |  |
| entry_type | character varying | Y |  |  |
| amount | bigint | Y |  |  |
| balance_after | bigint | Y |  |  |
| ref_type | character |  |  |  |
| ref_id | character |  |  |  |
| note | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| pool | character |  |  |  |

## credit_ledger_2026_07

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | nextval('public.credit_ledger_partitioned_id_seq'::regclass) |  |
| tenant_id | character varying | Y |  |  |
| entry_type | character varying | Y |  |  |
| amount | bigint | Y |  |  |
| balance_after | bigint | Y |  |  |
| ref_type | character |  |  |  |
| ref_id | character |  |  |  |
| note | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| pool | character |  |  |  |

索引（3）：
- credit_ledger_2026_07_created_at_idx
- credit_ledger_2026_07_ref_type_ref_id_idx
- credit_ledger_2026_07_tenant_id_created_at_idx

## credit_ledger_2026_08

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | nextval('public.credit_ledger_partitioned_id_seq'::regclass) |  |
| tenant_id | character varying | Y |  |  |
| entry_type | character varying | Y |  |  |
| amount | bigint | Y |  |  |
| balance_after | bigint | Y |  |  |
| ref_type | character |  |  |  |
| ref_id | character |  |  |  |
| note | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| pool | character |  |  |  |

索引（3）：
- credit_ledger_2026_08_created_at_idx
- credit_ledger_2026_08_ref_type_ref_id_idx
- credit_ledger_2026_08_tenant_id_created_at_idx

## credit_ledger_hot

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | nextval('public.credit_ledger_partitioned_id_seq'::regclass) |  |
| tenant_id | character varying | Y |  |  |
| entry_type | character varying | Y |  |  |
| amount | bigint | Y |  |  |
| balance_after | bigint | Y |  |  |
| ref_type | character |  |  |  |
| ref_id | character |  |  |  |
| note | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| pool | character |  |  |  |

索引（7）：
- credit_ledger_hot_created_at_idx
- credit_ledger_hot_created_idx
- credit_ledger_hot_pool_idx
- credit_ledger_hot_ref_idx
- credit_ledger_hot_ref_type_ref_id_idx
- credit_ledger_hot_tenant_created_idx
- credit_ledger_hot_tenant_id_created_at_idx

## credit_ledger_old

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| entry_type | character varying(32) | Y |  |  |
| amount | bigint | Y |  |  |
| balance_after | bigint | Y |  |  |
| ref_type | character |  |  |  |
| ref_id | character |  |  |  |
| note | text | Y | ''::text |  |
| created_at | timestamp with time zone | Y | now() |  |
| pool | character |  |  |  |

索引（1）：
- idx_credit_ledger_tenant_ts

## dashboard_access_events

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| event_id | character varying(64) | Y |  |  |
| event_type | character varying(20) | Y |  |  |
| timestamp | timestamp with time zone | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| user_id | character |  |  |  |
| user_role | character |  |  |  |
| session_id | character |  |  |  |
| api_path | character varying(255) | Y |  |  |
| api_method | character varying(10) | Y |  |  |
| api_version | character |  |  |  |
| query_params | jsonb |  |  |  |
| status_code | integer | Y |  |  |
| response_time_ms | integer | Y |  |  |
| cache_hit | boolean |  | false |  |
| data_size | integer |  |  |  |
| error_code | character |  |  |  |
| error_message | text |  |  |  |
| client_ip | inet |  |  |  |
| user_agent | text |  |  |  |
| referer | text |  |  |  |
| db_query_time_ms | integer |  |  |  |
| cache_query_time_ms | integer |  |  |  |
| created_at | timestamp with time zone | Y |  |  |

## dashboard_access_events_2026_07

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| event_id | character varying(64) | Y |  |  |
| event_type | character varying(20) | Y |  |  |
| timestamp | timestamp with time zone | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| user_id | character |  |  |  |
| user_role | character |  |  |  |
| session_id | character |  |  |  |
| api_path | character varying(255) | Y |  |  |
| api_method | character varying(10) | Y |  |  |
| api_version | character |  |  |  |
| query_params | jsonb |  |  |  |
| status_code | integer | Y |  |  |
| response_time_ms | integer | Y |  |  |
| cache_hit | boolean |  | false |  |
| data_size | integer |  |  |  |
| error_code | character |  |  |  |
| error_message | text |  |  |  |
| client_ip | inet |  |  |  |
| user_agent | text |  |  |  |
| referer | text |  |  |  |
| db_query_time_ms | integer |  |  |  |
| cache_query_time_ms | integer |  |  |  |
| created_at | timestamp with time zone | Y |  |  |

索引（1）：
- idx_dashboard_access_events_2026_07_tenant

## dashboard_access_events_2026_08

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| event_id | character varying(64) | Y |  |  |
| event_type | character varying(20) | Y |  |  |
| timestamp | timestamp with time zone | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| user_id | character |  |  |  |
| user_role | character |  |  |  |
| session_id | character |  |  |  |
| api_path | character varying(255) | Y |  |  |
| api_method | character varying(10) | Y |  |  |
| api_version | character |  |  |  |
| query_params | jsonb |  |  |  |
| status_code | integer | Y |  |  |
| response_time_ms | integer | Y |  |  |
| cache_hit | boolean |  | false |  |
| data_size | integer |  |  |  |
| error_code | character |  |  |  |
| error_message | text |  |  |  |
| client_ip | inet |  |  |  |
| user_agent | text |  |  |  |
| referer | text |  |  |  |
| db_query_time_ms | integer |  |  |  |
| cache_query_time_ms | integer |  |  |  |
| created_at | timestamp with time zone | Y |  |  |

索引（1）：
- idx_dashboard_access_events_2026_08_tenant

## dashboard_access_events_hot

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| event_id | character varying(64) | Y |  |  |
| event_type | character varying(20) | Y |  |  |
| timestamp | timestamp with time zone | Y | now() |  |
| tenant_id | character varying(255) | Y |  |  |
| user_id | character |  |  |  |
| user_role | character |  |  |  |
| session_id | character |  |  |  |
| api_path | character varying(255) | Y |  |  |
| api_method | character varying(10) | Y |  |  |
| api_version | character |  |  |  |
| query_params | jsonb |  |  |  |
| status_code | integer | Y |  |  |
| response_time_ms | integer | Y |  |  |
| cache_hit | boolean |  | false |  |
| data_size | integer |  |  |  |
| error_code | character |  |  |  |
| error_message | text |  |  |  |
| client_ip | inet |  |  |  |
| user_agent | text |  |  |  |
| referer | text |  |  |  |
| db_query_time_ms | integer |  |  |  |
| cache_query_time_ms | integer |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

索引（8）：
- idx_dae_hot_api_time
- idx_dae_hot_cleanup
- idx_dae_hot_errors
- idx_dae_hot_event_type
- idx_dae_hot_slow
- idx_dae_hot_tenant_time
- idx_dae_hot_timestamp
- idx_dae_hot_user_time

## diagnostic_runs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | uuid | Y | gen_random_uuid() |  |
| incident_id | uuid |  |  |  |
| tenant_id | text | Y | 'default'::text |  |
| kind | text | Y |  |  |
| state | text | Y | 'pending'::text |  |
| started_at | timestamp with time zone | Y | now() |  |
| finished_at | timestamp |  |  |  |
| heartbeat_at | timestamp |  |  |  |
| trigger_source | text |  |  |  |
| error | text |  |  |  |
| summary_json | jsonb | Y | '{}'::jsonb |  |
| updated_at | timestamp with time zone | Y | now() |  |
| created_at | timestamp with time zone | Y | now() |  |

索引（6）：
- idx_diagnostic_runs_created
- idx_diagnostic_runs_incident
- idx_diagnostic_runs_kind_state
- idx_diagnostic_runs_state
- idx_diagnostic_runs_tenant
- idx_diagnostic_runs_tenant_started

## donations

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| order_no | text | Y |  |  |
| holder_id | bigint |  |  |  |
| email | text | Y | ''::text |  |
| amount_cents | integer | Y |  |  |
| currency | text | Y | 'CNY'::text |  |
| channel | text | Y | 'alipay'::text |  |
| status | text | Y | 'pending'::text |  |
| tier_label | text | Y | 'supporter'::text |  |
| paid_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

外键：
- (holder_id) → license_holders(id)

索引（2）：
- idx_donations_email
- idx_donations_status

## download_events

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| request_id | text | Y |  |  |
| release_version | text | Y |  |  |
| platform | text | Y |  |  |
| arch | text | Y | ''::text |  |
| edition | text | Y | 'customer'::text |  |
| channel | text | Y | 'stable'::text |  |
| holder_id | bigint |  |  |  |
| donation_id | bigint |  |  |  |
| result | text | Y | 'started'::text |  |
| duration_ms | integer |  |  |  |
| source | text | Y | 'web'::text |  |
| created_at | timestamp with time zone | Y | now() |  |

外键：
- (holder_id) → license_holders(id)

索引（2）：
- idx_download_events_created
- idx_download_events_version

## download_publish_runs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| release_version | text | Y |  |  |
| build_seq | integer | Y | 0 |  |
| status | text | Y | 'pending'::text |  |
| artifact_count | integer | Y | 0 |  |
| test_passed | boolean | Y | false |  |
| log_summary | text |  |  |  |
| created_by | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| finished_at | timestamp |  |  |  |

索引（1）：
- idx_download_publish_runs_created

## fault_action_logs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| event_id | bigint | Y |  |  |
| action | text | Y |  |  |
| status | text | Y |  |  |
| result | text |  |  |  |
| duration_ms | bigint | Y | 0 |  |
| triggered_at | timestamp with time zone | Y |  |  |
| completed_at | timestamp |  |  |  |

外键：
- (event_id) → fault_events(id)

索引（2）：
- idx_fal_event
- idx_fal_triggered

## fault_events

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| rule_id | bigint | Y |  |  |
| rule_name | text | Y |  |  |
| severity | text | Y |  |  |
| title | text | Y |  |  |
| description | text | Y |  |  |
| source | text | Y |  |  |
| status | text | Y | 'new'::text |  |
| metadata | jsonb |  |  |  |
| detected_at | timestamp with time zone | Y |  |  |
| acked_at | timestamp |  |  |  |
| acked_by | text |  |  |  |
| resolved_at | timestamp |  |  |  |
| resolved_by | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（4）：
- idx_fe_detected
- idx_fe_rule
- idx_fe_severity
- idx_fe_status

## fault_rules

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| name | text | Y |  |  |
| description | text | Y |  |  |
| metric | text | Y |  |  |
| operator | text | Y |  |  |
| threshold | double precision | Y |  |  |
| duration | text | Y |  |  |
| severity | text | Y |  |  |
| action | text | Y |  |  |
| action_config | jsonb |  |  |  |
| enabled | boolean | Y | true |  |
| cooldown | text | Y | '5m'::text |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（2）：
- idx_fr_enabled
- idx_fr_metric

## gateway_instances

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| instance_id | text | Y |  |  |
| hostname | text | Y |  |  |
| ip_address | text | Y |  |  |
| region | text |  |  |  |
| version | text | Y |  |  |
| build_seq | integer | Y |  |  |
| status | text | Y | 'online'::text |  |
| started_at | timestamp with time zone | Y |  |  |
| last_heartbeat | timestamp with time zone | Y | now() |  |
| registered_at | timestamp with time zone | Y | now() |  |
| metadata | jsonb | Y | '{}'::jsonb |  |
| instance_token | text |  |  |  |
| refresh_token | text |  |  |  |
| refresh_token_issued_at | timestamp |  |  |  |
| refresh_token_expires_at | timestamp |  |  |  |
| public_key | text |  |  |  |
| current_version | text |  |  |  |
| license_key_hash | text |  |  |  |
| hardware_hash | text |  |  |  |
| instance_type | text |  | 'standalone'::text |  |
| deployment_id | text |  |  |  |
| replica_count | integer |  | 1 |  |

索引（7）：
- idx_gi_deployment
- idx_gi_heartbeat
- idx_gi_license
- idx_gi_refresh_token
- idx_gi_region
- idx_gi_status
- idx_gi_version

## goal_sessions

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| session_id | character varying(64) | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| state | character varying(32) | Y | 'active'::character |  |
| original_goal | text | Y |  |  |
| retry_count | integer |  | 0 |  |
| decision_count | integer |  | 0 |  |
| auto_continue_count | integer |  | 0 |  |
| last_activity_at | timestamp without time zone |  | now() |  |
| completed_at | timestamp |  |  |  |
| audit_result | jsonb |  |  |  |
| created_at | timestamp without time zone |  | now() |  |
| model_switch_count | integer | Y | 0 |  |
| repeat_count | integer | Y | 0 |  |
| last_response_hash | character varying(64) |  | ''::character |  |
| current_model | character varying(128) |  | ''::character |  |
| continue_attempt | integer | Y | 0 |  |
| last_completion_judgement | character varying(32) |  | ''::character |  |
| sub_agents_total | integer | Y | 0 |  |
| sub_agents_completed | integer | Y | 0 |  |
| sub_agents_pending | integer | Y | 0 |  |
| last_sub_agents_report_at | timestamp |  |  |  |

索引（3）：
- idx_goal_sessions_session
- idx_goal_sessions_state
- idx_goal_sessions_tenant

## gray_release_rules

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| release_id | bigint | Y |  |  |
| phase | text | Y |  |  |
| percent | integer | Y |  |  |
| selectors | jsonb |  |  |  |
| status | text | Y | 'active'::text |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

外键：
- (release_id) → releases(id)

索引（2）：
- idx_gray_rules_release
- idx_gray_rules_status

## handoff_logs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| session_id | character varying(64) | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| trigger_reason | character varying(64) | Y |  |  |
| tokens_at_handoff | integer | Y |  |  |
| context_window | integer |  |  |  |
| handoff_prompt | text |  |  |  |
| new_session_id | character |  |  |  |
| created_at | timestamp without time zone |  | now() |  |
| summary_text | text |  |  |  |
| summary_engine | character |  |  |  |
| trigger_mode | character |  |  |  |
| tokens_in_session | integer |  |  |  |
| messages_in_session | integer |  |  |  |
| skill_name | character |  |  |  |
| duration_ms | integer |  |  |  |

索引（3）：
- idx_handoff_logs_session
- idx_handoff_logs_tenant
- idx_handoff_logs_trigger_mode

## injection_attack_vectors

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | character varying(255) | Y | 'default'::character |  |
| attack_text | text | Y |  |  |
| attack_hash | character varying(64) | Y |  |  |
| categories | public.injection_category[] |  |  |  |
| severity | integer |  |  |  |
| embedding | text |  |  |  |
| source | character varying(50) | Y | 'detection'::character |  |
| request_id | character |  |  |  |
| detected_at | timestamp |  |  |  |
| created_at | timestamp with time zone |  | now() |  |

索引（2）：
- idx_attack_vectors_categories
- idx_attack_vectors_tenant

## instance_heartbeats

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| instance_id | text | Y |  |  |
| timestamp | timestamp with time zone | Y | now() |  |
| uptime_secs | bigint | Y |  |  |
| num_goroutine | integer | Y |  |  |
| alloc_mb | double precision | Y |  |  |
| status | text | Y |  |  |
| metrics | jsonb |  |  |  |

索引（2）：
- idx_ih_instance
- idx_ih_timestamp

## instance_release_status

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| release_id | bigint | Y |  |  |
| instance_id | text | Y |  |  |
| status | text | Y |  |  |
| version | text | Y |  |  |
| started_at | timestamp with time zone | Y | now() |  |
| completed_at | timestamp |  |  |  |
| error | text |  |  |  |
| retry_count | integer | Y | 0 |  |
| updated_at | timestamp with time zone | Y | now() |  |

外键：
- (release_id) → releases(id)

索引（2）：
- idx_instance_status_release
- idx_instance_status_status

## instance_status_reports

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| instance_id | text | Y |  |  |
| timestamp | timestamp with time zone | Y | now() |  |
| state | text | Y |  |  |
| active_licenses | integer | Y | 0 |  |
| active_devices | integer | Y | 0 |  |
| requests_total | bigint | Y | 0 |  |
| requests_ok | bigint | Y | 0 |  |
| requests_err | bigint | Y | 0 |  |
| avg_latency_ms | double precision | Y | 0 |  |
| p99_latency_ms | double precision | Y | 0 |  |

索引（2）：
- idx_isr_instance
- idx_isr_timestamp

## integrity_fingerprint_baseline

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| tenant_id | text | Y | 'default'::text |  |
| provider_id | integer |  |  |  |
| credential_id | integer | Y |  |  |
| raw_model_name | text | Y |  |  |
| baseline_fingerprint | text |  |  |  |
| baseline_share_pct | integer |  |  |  |
| baseline_sample_count | bigint | Y | 0 |  |
| baseline_window_start | timestamp |  |  |  |
| baseline_window_end | timestamp |  |  |  |
| current_fingerprint | text |  |  |  |
| current_share_pct | integer |  |  |  |
| last_observed_at | timestamp with time zone | Y | now() |  |
| last_alerted_fingerprint | text |  |  |  |
| last_alerted_at | timestamp |  |  |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（1）：
- idx_integrity_fingerprint_baseline_cred_model

## intent_aggregates

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| tenant_id | text | Y |  |  |
| intent_kind | text | Y |  |  |
| count | bigint | Y | 0 |  |
| last_updated | timestamp with time zone | Y | now() |  |

索引（1）：
- idx_intent_aggregates_tenant_updated

## intent_analysis_adjustments

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | text | Y |  |  |
| adjustment_type | text | Y |  |  |
| target_intent | text |  |  |  |
| adjustment_detail | jsonb | Y |  |  |
| reason | text |  |  |  |
| triggered_by | text | Y | 'manual'::text |  |
| operator_id | text |  |  |  |
| effectiveness_score | double |  |  |  |
| evaluation_sample_size | integer |  |  |  |
| before_accuracy | double |  |  |  |
| after_accuracy | double |  |  |  |
| status | text | Y | 'active'::text |  |
| rollback_reason | text |  |  |  |
| superseded_by | bigint |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| evaluated_at | timestamp |  |  |  |
| rolled_back_at | timestamp |  |  |  |

外键：
- (superseded_by) → intent_analysis_adjustments(id)

索引（5）：
- idx_adjustments_active
- idx_adjustments_effectiveness
- idx_adjustments_rolled_back
- idx_adjustments_tenant
- idx_adjustments_type

## intent_classification_feedback

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| session_id | text | Y |  |  |
| request_id | text | Y |  |  |
| tenant_id | text | Y |  |  |
| predicted_intent | text | Y |  |  |
| predicted_confidence | double precision | Y |  |  |
| actual_intent | text |  |  |  |
| is_correct | boolean |  |  |  |
| annotator_id | text |  |  |  |
| annotated_at | timestamp |  |  |  |
| annotation_notes | text |  |  |  |
| user_accepted_model | boolean |  |  |  |
| user_switched_to_model | text |  |  |  |
| user_retry_count | integer |  | 0 |  |
| session_duration_sec | integer |  |  |  |
| user_satisfaction_score | integer |  |  |  |
| user_content_hash | text |  |  |  |
| classification_context | jsonb |  |  |  |
| evolution_id | bigint |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

索引（7）：
- idx_feedback_annotated
- idx_feedback_content_hash
- idx_feedback_correct
- idx_feedback_session
- idx_feedback_tenant
- idx_feedback_unannotated
- idx_feedback_user_behavior

## intent_classifier_config

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| tenant_id | text |  |  |  |
| strategy | text | Y | 'pattern_layered'::text |  |
| enabled_layers | jsonb |  | '{"hard_rules": |  |
| keywords_config | jsonb | Y | '{}'::jsonb |  |
| patterns_config | jsonb | Y | '{}'::jsonb |  |
| confidence_thresholds | jsonb |  | '{"low": |  |
| drift_threshold | double precision | Y | 0.3 |  |
| multi_turn_memory | integer | Y | 5 |  |
| llm_fallback_enabled | boolean | Y | false |  |
| llm_model | text |  | 'gpt-4o-mini'::text |  |
| llm_confidence_threshold | double precision |  | 0.50 |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（2）：
- idx_intent_config_strategy
- idx_intent_config_tenant

## internal_service_keys

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| service_id | text | Y |  |  |
| secret_hash | text | Y |  |  |
| description | text |  |  |  |
| enabled | boolean | Y | true |  |
| last_used_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| rotated_at | timestamp |  |  |  |
| rotation_notes | text |  |  |  |

## ip_blocklist

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| ip_or_cidr | text | Y |  |  |
| reason | text | Y | ''::text |  |
| scope | text | Y | 'global'::text |  |
| source | text | Y | 'manual'::text |  |
| enabled | boolean | Y | true |  |
| expires_at | timestamp |  |  |  |
| hit_count | bigint | Y | 0 |  |
| created_by | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（3）：
- idx_ip_blocklist_active_entry UNIQUE
- idx_ip_blocklist_enabled
- idx_ip_blocklist_expires

## key_applications

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | uuid | Y | gen_random_uuid() |  |
| client_ip | inet | Y |  |  |
| fingerprint | text | Y |  |  |
| contact | text | Y |  |  |
| purpose | text |  |  |  |
| status | text | Y | 'pending'::text |  |
| issued_key_id | bigint |  |  |  |
| admin_notes | text |  |  |  |
| reviewed_by | text |  |  |  |
| reviewed_at | timestamp |  |  |  |
| expires_at | timestamp with time zone | Y | (now() |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

## key_rpm_daily

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| api_key_id | bigint | Y |  |  |
| day_bucket | date | Y |  |  |
| peak_rpm | numeric(10,3) | Y | 0 |  |
| avg_rpm | numeric(10,3) | Y | 0 |  |
| request_count | bigint | Y | 0 |  |

## license_devices

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| license_id | bigint | Y |  |  |
| instance_id | text | Y |  |  |
| hardware_hash | text | Y |  |  |
| device_name | text | Y |  |  |
| activated_at | timestamp with time zone | Y | now() |  |
| last_heartbeat | timestamp |  |  |  |
| status | text | Y | 'active'::text |  |
| deactivated_at | timestamp |  |  |  |
| deactivate_reason | text |  |  |  |

外键：
- (license_id) → licenses(id)

索引（3）：
- idx_ld_hardware
- idx_ld_license
- idx_ld_status

## license_holders

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| email | text | Y |  |  |
| display_name | text | Y | ''::text |  |
| holder_type | text | Y | 'individual'::text |  |
| consent_version | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| last_seen_at | timestamp |  |  |  |

索引（1）：
- idx_license_holders_email_lower UNIQUE

## license_module_audit

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| license_key | text | Y |  |  |
| module_key | text | Y |  |  |
| action | text | Y |  |  |
| old_value | jsonb |  |  |  |
| new_value | jsonb |  |  |  |
| actor | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

索引（2）：
- idx_lma_key
- idx_lma_module

## license_modules

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| license_id | bigint | Y |  |  |
| module_key | text | Y |  |  |
| enabled | boolean | Y | true |  |
| config | jsonb |  |  |  |
| expires_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

外键：
- (license_id) → licenses(id)
- (module_key) → product_modules(key)

索引（2）：
- idx_lm_license
- idx_lm_module

## license_trial_consents

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| license_id | bigint | Y |  |  |
| agreement_version | text | Y |  |  |
| accepted_at | timestamp with time zone | Y |  |  |
| source | text | Y |  |  |
| created_at | timestamp with time zone | Y | now() |  |

外键：
- (license_id) → licenses(id)

索引（1）：
- idx_license_trial_consents_accepted_at

## licenses

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| license_key | text | Y |  |  |
| customer_name | text | Y | ''::text |  |
| customer_email | text | Y | ''::text |  |
| max_devices | integer | Y | 2 |  |
| subscription_tier | text | Y | 'starter'::text |  |
| features | jsonb | Y | '[]'::jsonb |  |
| expires_at | timestamp |  |  |  |
| revoked_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| holder_id | bigint |  |  |  |

外键：
- (holder_id) → license_holders(id)

索引（3）：
- idx_licenses_expires
- idx_licenses_holder
- idx_licenses_key

## llm_gateway_migration_checksums

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| version | text | Y |  |  |
| migration_name | text | Y |  |  |
| checksum | text | Y |  |  |
| applied_at | timestamp with time zone | Y | now() |  |

## local_models

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| runtime_id | bigint | Y |  |  |
| canonical_id | bigint |  |  |  |
| raw_name | text | Y |  |  |
| quantization | text |  |  |  |
| size_bytes | bigint |  |  |  |
| family | text |  |  |  |
| parameters_b | numeric(8,2) |  |  |  |
| loaded | boolean | Y | false |  |
| keep_alive_seconds | integer | Y | 0 |  |
| last_used_at | timestamp |  |  |  |

## local_runtimes

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| host_code | text | Y |  |  |
| runtime_type | text | Y |  |  |
| base_url | text | Y |  |  |
| mode | text | Y | 'direct'::text |  |
| status | text | Y | 'unknown'::text |  |
| gpu_info_json | jsonb |  |  |  |
| vram_total_mb | integer |  |  |  |
| vram_used_mb | integer |  |  |  |
| ram_total_mb | integer |  |  |  |
| last_heartbeat_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

## maas_credit_consumption_buckets

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| tenant_id | text | Y |  |  |
| bucket_start | timestamp with time zone | Y |  |  |
| credits | bigint | Y | 0 |  |
| request_count | integer | Y | 0 |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（1）：
- idx_mccb_recent

## maas_settings

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y | 1 |  |
| cents_per_credit | numeric(10,4) | Y | 0.1 |  |
| base_credits_per_1m | bigint | Y | 10000 |  |
| currency_display | character varying(8) | Y | 'CNY'::character |  |
| updated_at | timestamp with time zone | Y | now() |  |
| alipay_account | character varying(128) | Y | ''::character |  |
| wechat_mch_id | character varying(128) | Y | ''::character |  |
| stub_alipay_qr_url | text | Y | ''::text |  |
| stub_wechat_qr_url | text | Y | ''::text |  |
| base_credits_per_1m_out | bigint |  |  |  |
| base_credits_per_1m_cache_in | bigint |  |  |  |
| base_credits_per_1m_cache_out | bigint |  |  |  |
| global_discount | numeric(6,4) | Y | 1.0 |  |

## model_aliases

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| canonical_id | bigint | Y |  |  |
| raw_name | text | Y |  |  |
| quantization | text |  |  |  |
| surface | text |  |  |  |
| status | text | Y | 'active'::text |  |
| notes | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| client_profiles | text[] |  |  |  |

索引（1）：
- idx_model_aliases_lower_raw_name_status

## model_credit_rates

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| canonical_id | integer | Y |  |  |
| credits_per_1m_in | bigint |  |  |  |
| credits_per_1m_out | bigint |  |  |  |
| updated_at | timestamp with time zone | Y | now() |  |
| credits_per_1m_cache_in | bigint |  |  |  |
| credits_per_1m_cache_out | bigint |  |  |  |
| manual_in | boolean | Y | false |  |
| manual_out | boolean | Y | false |  |
| manual_cache_in | boolean | Y | false |  |
| manual_cache_out | boolean | Y | false |  |
| credits_per_1m_image_tokens | bigint |  |  |  |
| credits_per_1m_audio_tokens | bigint |  |  |  |
| credits_per_1m_video_tokens | bigint |  |  |  |
| manual_image | boolean | Y | false |  |
| manual_audio | boolean | Y | false |  |
| manual_video | boolean | Y | false |  |

## model_discovery_runs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | text | Y | 'default'::text |  |
| trigger | text | Y | 'manual'::text |  |
| status | text | Y | 'running'::text |  |
| started_at | timestamp with time zone | Y | now() |  |
| finished_at | timestamp |  |  |  |
| heartbeat_at | timestamp with time zone | Y | now() |  |
| lease_expires_at | timestamp with time zone | Y |  |  |
| requested_by | text |  |  |  |
| request_json | jsonb | Y | '{}'::jsonb |  |
| summary_json | jsonb |  |  |  |
| error | text |  |  |  |

## model_families

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | text | Y |  |  |
| display_name | text | Y |  |  |
| vendor | text |  |  |  |
| status | text | Y | 'active'::text |  |
| source | text | Y | 'db'::text |  |
| notes | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

## model_fingerprints

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| credential_id | bigint | Y |  |  |
| canonical_id | bigint | Y |  |  |
| fingerprint_hash | text | Y |  |  |
| sampled_features_json | jsonb |  |  |  |
| last_verified_at | timestamp |  |  |  |
| drift_detected | boolean | Y | false |  |

## model_integrity_events

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| ts | timestamp with time zone | Y | now() |  |
| request_id | text |  |  |  |
| tenant_id | text |  |  |  |
| application_id | integer |  |  |  |
| api_key_id | integer |  |  |  |
| provider_id | integer |  |  |  |
| provider_code | text |  |  |  |
| credential_id | integer |  |  |  |
| client_model | text |  |  |  |
| outbound_model | text |  |  |  |
| raw_model_name | text |  |  |  |
| anomaly_type | text | Y |  |  |
| severity | text | Y | 'low'::text |  |
| expected_value | text |  |  |  |
| actual_value | text |  |  |  |
| sample | text |  |  |  |
| context | jsonb |  |  |  |
| resolved | boolean | Y | false |  |
| resolved_at | timestamp |  |  |  |
| resolution_notes | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

索引（5）：
- idx_model_integrity_events_bridge
- idx_model_integrity_events_cred_model_type
- idx_model_integrity_events_provider_type
- idx_model_integrity_events_request_id
- idx_model_integrity_events_ts

## model_lifecycle_jobs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| runtime_id | bigint | Y |  |  |
| action | text | Y |  |  |
| target | text | Y |  |  |
| status | text | Y | 'queued'::text |  |
| progress_pct | numeric(5,2) |  | 0 |  |
| log | text |  |  |  |
| started_at | timestamp |  |  |  |
| finished_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

## model_name_mapping

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| raw_model_name | text | Y |  |  |
| standardized_name | text | Y |  |  |
| description | text |  |  |  |
| auto_generated | boolean |  | false |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| created_by | text |  |  |  |

索引（1）：
- idx_model_name_mapping_standardized

## model_offer_events

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint |  |  |  |
| ts | timestamp |  |  |  |
| source | text |  |  |  |
| action | text |  |  |  |
| credential_id | bigint |  |  |  |
| provider_id | bigint |  |  |  |
| canonical_id | bigint |  |  |  |
| raw_model_name | text |  |  |  |
| reason_code | text |  |  |  |
| reason_detail | text |  |  |  |
| request_id | text |  |  |  |
| run_id | bigint |  |  |  |
| metadata_json | jsonb |  |  |  |

## model_pricing

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| model_canonical | character varying(64) | Y |  |  |
| display_name | character varying(128) | Y |  |  |
| input_credits_per_1m | bigint | Y |  |  |
| output_credits_per_1m | bigint | Y |  |  |
| cache_write_credits_per_1m | bigint |  |  |  |
| cache_read_credits_per_1m | bigint |  |  |  |
| provider | character varying(32) | Y |  |  |
| provider_model | character |  |  |  |
| context_window | integer | Y | 128000 |  |
| max_output_tokens | integer | Y | 4096 |  |
| supports_streaming | boolean | Y | true |  |
| supports_tools | boolean | Y | true |  |
| supports_vision | boolean | Y | false |  |
| supports_caching | boolean | Y | false |  |
| tier | character varying(16) | Y |  |  |
| daily_free_quota_credits | bigint |  | 0 |  |
| requires_plan | character |  |  |  |
| min_credits_per_request | bigint |  | 0 |  |
| active | boolean | Y | true |  |
| deprecated | boolean | Y | false |  |
| replacement_model | character |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| notes | text |  |  |  |

索引（3）：
- idx_model_pricing_active
- idx_model_pricing_provider
- idx_model_pricing_tier

## model_pricing_history

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| model_canonical | character varying(64) | Y |  |  |
| old_input_credits_per_1m | bigint |  |  |  |
| old_output_credits_per_1m | bigint |  |  |  |
| new_input_credits_per_1m | bigint |  |  |  |
| new_output_credits_per_1m | bigint |  |  |  |
| changed_by | character |  |  |  |
| change_reason | text |  |  |  |
| effective_date | timestamp with time zone | Y | now() |  |
| created_at | timestamp with time zone | Y | now() |  |

索引（2）：
- idx_pricing_history_date
- idx_pricing_history_model

## model_probe_runs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint |  |  |  |
| tenant_id | text |  |  |  |
| credential_id | bigint |  |  |  |
| raw_model_name | text |  |  |  |
| status | text |  |  |  |
| http_status | integer |  |  |  |
| error_code | text |  |  |  |
| error_message | text |  |  |  |
| latency_ms | integer |  |  |  |
| state_change | text |  |  |  |
| state_applied | boolean |  |  |  |
| triggered_by | text |  |  |  |
| created_at | timestamp with time zone |  | now() |  |

## model_probe_runs_2026_07

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint |  |  |  |
| tenant_id | text |  |  |  |
| credential_id | bigint |  |  |  |
| raw_model_name | text |  |  |  |
| status | text |  |  |  |
| http_status | integer |  |  |  |
| error_code | text |  |  |  |
| error_message | text |  |  |  |
| latency_ms | integer |  |  |  |
| state_change | text |  |  |  |
| state_applied | boolean |  |  |  |
| triggered_by | text |  |  |  |
| created_at | timestamp with time zone |  | now() |  |

## model_probe_runs_hot

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint |  |  |  |
| tenant_id | text |  |  |  |
| credential_id | bigint |  |  |  |
| raw_model_name | text |  |  |  |
| status | text |  |  |  |
| http_status | integer |  |  |  |
| error_code | text |  |  |  |
| error_message | text |  |  |  |
| latency_ms | integer |  |  |  |
| state_change | text |  |  |  |
| state_applied | boolean |  |  |  |
| triggered_by | text |  |  |  |
| created_at | timestamp with time zone |  | now() |  |

索引（5）：
- idx_mpr_hot_created_at
- idx_mpr_hot_cred_created
- idx_mpr_hot_model_created
- idx_mpr_hot_status_created
- idx_mpr_hot_tenant_created

## model_probe_state

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| credential_id | bigint | Y |  |  |
| raw_model_name | text | Y |  |  |
| state | text | Y | 'unknown'::text |  |
| consecutive_successes | integer | Y | 0 |  |
| consecutive_failures | integer | Y | 0 |  |
| total_attempts | integer | Y | 0 |  |
| last_attempt_at | timestamp |  |  |  |
| next_retry_at | timestamp with time zone | Y | now() |  |
| last_status | text |  |  |  |
| last_state_change_at | timestamp |  |  |  |
| last_state_change_run | bigint |  |  |  |
| last_unavailable_reason | text |  |  |  |
| last_err_code | text |  |  |  |
| next_retry_at_override | timestamp |  |  |  |
| state_expires_at | timestamp |  |  |  |
| marked_suspicious_at | timestamp |  |  |  |
| probing_started_at | timestamp |  |  |  |
| probing_credential_concurrency | integer |  | 0 |  |
| probe_priority | text |  | 'watchdog'::text |  |
| last_verified_at | timestamp |  |  |  |
| verification_interval | interval |  | '04:00:00'::interval |  |
| success_rate_7d | numeric(5,2) |  | 0.00 |  |
| consecutive_watchdog_successes | integer |  | 0 |  |
| last_real_request_at | timestamp |  |  |  |
| real_request_success_count | integer |  | 0 |  |
| real_request_failure_count | integer |  | 0 |  |
| verification_attempt_1_at | timestamp |  |  |  |
| verification_attempt_2_at | timestamp |  |  |  |
| verification_result_1 | boolean |  |  |  |
| verification_result_2 | boolean |  |  |  |
| verification_latency_1_ms | integer |  |  |  |
| verification_latency_2_ms | integer |  |  |  |

索引（7）：
- idx_model_probe_state_retry
- idx_mps_due
- idx_mps_priority_next_retry
- idx_mps_probing
- idx_mps_success_rate
- idx_mps_suspicious_expired
- idx_mps_suspicious_pending

## model_reconcile_log

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| credential_id | bigint |  |  |  |
| ts | timestamp with time zone | Y | now() |  |
| added | integer | Y | 0 |  |
| removed | integer | Y | 0 |  |
| changed | integer | Y | 0 |  |
| diff_json | jsonb |  |  |  |

## model_task_index

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| bucket | timestamp with time zone | Y |  |  |
| canonical_id | integer | Y |  |  |
| task_type | text | Y |  |  |
| sample_count | integer | Y | 0 |  |
| success_rate | numeric(5,4) |  |  |  |
| avg_latency_ms | integer |  |  |  |
| p95_latency_ms | integer |  |  |  |
| avg_cost_per_1k_usd | numeric(10,6) |  |  |  |
| primary_credential_id | bigint |  |  |  |
| updated_at | timestamp with time zone |  | now() |  |

## models_canonical

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| canonical_name | text | Y |  |  |
| family | text |  |  |  |
| parameters_b | numeric(8,2) |  |  |  |
| modality | text | Y | 'text'::text |  |
| context_window | integer |  |  |  |
| notes | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| tags | text[] | Y | '{}'::text[] |  |
| tags_locked | boolean | Y | false |  |
| tags_updated_at | timestamp |  |  |  |
| display_name | text |  |  |  |
| status | text | Y | 'active'::text |  |
| source | text | Y | 'db'::text |  |
| disabled_reason | text |  |  |  |
| updated_at | timestamp with time zone | Y | now() |  |
| input_price_cny | numeric(10,4) |  | 0 |  |
| output_price_cny | numeric(10,4) |  | 0 |  |
| released_at | date |  |  |  |
| strengths | text[] | Y | '{}'::text[] |  |
| cost_tier | text | Y | 'unknown'::text |  |
| multimodal_caps | text[] | Y | '{}'::text[] |  |
| version_rank | integer |  |  |  |
| complexity_ceiling | text |  |  |  |
| min_complexity | text |  |  |  |

索引（4）：
- idx_models_canonical_complexity_ceiling
- idx_models_canonical_released
- idx_models_canonical_strengths
- idx_models_canonical_version_rank

## node_probe_runs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| credential_id | bigint | Y |  |  |
| raw_model_name | text | Y |  |  |
| trigger_kind | text | Y |  |  |
| trigger_request_id | text |  |  |  |
| attempt | integer | Y |  |  |
| next_retry_seconds | integer | Y |  |  |
| direct_ok | boolean | Y |  |  |
| direct_http_status | integer |  |  |  |
| direct_err_code | text |  |  |  |
| direct_latency_ms | integer |  |  |  |
| direct_err_detail | text |  |  |  |
| gateway_ok | boolean | Y |  |  |
| gateway_http_status | integer |  |  |  |
| gateway_err_code | text |  |  |  |
| gateway_latency_ms | integer |  |  |  |
| gateway_err_detail | text |  |  |  |
| success | boolean | Y |  |  |
| started_at | timestamp with time zone | Y | now() |  |
| completed_at | timestamp |  |  |  |
| duration_ms | integer | Y | 0 |  |
| created_at | timestamp with time zone | Y | now() |  |
| api_model | text |  |  |  |
| outbound_model | text |  |  |  |
| provider_id | bigint |  |  |  |
| request_url | text |  |  |  |
| request_headers | jsonb |  |  |  |
| request_body | text |  |  |  |
| response_body | text |  |  |  |
| timeout_at_ms | integer |  |  |  |
| via_proxy | boolean |  |  |  |

索引（4）：
- idx_node_probe_runs_api_model
- idx_node_probe_runs_cred_model
- idx_node_probe_runs_provider
- idx_node_probe_runs_started

## node_probe_state

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| credential_id | bigint | Y |  |  |
| raw_model_name | text | Y |  |  |
| consecutive_failures | integer | Y | 0 |  |
| consecutive_successes | integer | Y | 0 |  |
| last_attempt_at | timestamp |  |  |  |
| next_retry_at | timestamp with time zone | Y | now() |  |
| next_retry_seconds | integer | Y | 5 |  |
| paused | boolean | Y | false |  |
| last_run_id | bigint |  |  |  |
| last_direct_ok | boolean |  |  |  |
| last_gateway_ok | boolean |  |  |  |
| last_err_code | text |  |  |  |
| last_err_detail | text |  |  |  |
| in_flight_until | timestamp |  |  |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（1）：
- idx_node_probe_state_due

## node_stats

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| credential_id | bigint |  |  |  |
| raw_model_name | text |  |  |  |
| success_rate | double precision |  | 0.95 |  |
| p95_latency_ms | integer |  | 1000 |  |
| created_at | timestamp with time zone |  | now() |  |
| updated_at | timestamp with time zone |  | now() |  |

## offline_activation_requests

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| license_key | text | Y |  |  |
| hardware_hash | text | Y |  |  |
| instance_id | text | Y |  |  |
| device_name | text | Y |  |  |
| request_id | text | Y |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| approved_at | timestamp |  |  |  |
| signed_license | jsonb |  |  |  |

索引（3）：
- idx_oar_created
- idx_oar_license
- idx_oar_request

## ops_node_registrations

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| instance_id | text | Y |  |  |
| region | text | Y |  |  |
| license_key | text | Y |  |  |
| license_id | bigint |  |  |  |
| admin_user | text | Y |  |  |
| admin_email | text |  |  |  |
| hostname | text |  |  |  |
| ip_address | text |  |  |  |
| version | text |  |  |  |
| build_seq | integer | Y | 0 |  |
| status | text | Y | 'active'::text |  |
| registered_at | timestamp with time zone | Y | now() |  |
| last_heartbeat | timestamp |  |  |  |
| metadata | jsonb | Y | '{}'::jsonb |  |

外键：
- (license_id) → licenses(id)

索引（3）：
- idx_onr_admin
- idx_onr_license
- idx_onr_region

## output_compliance_audit

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| request_id | character varying(255) | Y |  |  |
| session_key | character |  |  |  |
| detected_at | timestamp with time zone |  | now() |  |
| issue_type | character varying(50) | Y |  |  |
| issue_subtype | character |  |  |  |
| severity | integer | Y |  |  |
| evidence | text |  |  |  |
| location | character |  |  |  |
| score | numeric(5,4) |  |  |  |
| action_taken | character varying(20) | Y |  |  |
| redacted | boolean |  | false |  |
| blocked | boolean |  | false |  |
| original_output | text |  |  |  |
| redacted_output | text |  |  |  |
| model | character |  |  |  |
| client_ip | character |  |  |  |
| policy_id | integer |  |  |  |
| rule_triggered | character |  |  |  |
| exception_matched | boolean |  | false |  |
| exception_scope | character |  |  |  |
| alert_sent | boolean |  | false |  |
| skill_suggestion | text |  |  |  |
| review_queue_id | integer |  |  |  |

索引（4）：
- idx_output_audit_issue
- idx_output_audit_request
- idx_output_audit_session
- idx_output_audit_tenant_time

## output_compliance_custom_keywords

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| tenant_id | character varying(255) | Y | 'default'::character |  |
| keyword | character varying(200) | Y |  |  |
| category | character varying(50) | Y | 'custom'::character |  |
| severity | integer | Y | 7 |  |
| action | character varying(20) |  | 'warn'::character |  |
| enabled | boolean |  | true |  |
| description | text |  |  |  |
| created_at | timestamp with time zone |  | now() |  |
| updated_at | timestamp with time zone |  | now() |  |
| created_by | character |  |  |  |
| updated_by | character |  |  |  |

## output_compliance_feedback

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| audit_id | bigint | Y |  |  |
| feedback_type | character varying(20) | Y |  |  |
| reporter | character |  |  |  |
| comment | text |  |  |  |
| created_at | timestamp with time zone |  | now() |  |

索引（1）：
- idx_output_compliance_feedback_tenant

## output_compliance_policies

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| enabled | boolean |  | true |  |
| enforcement_mode | character varying(20) |  | 'observe'::character |  |
| check_pii | boolean |  | true |  |
| check_toxicity | boolean |  | true |  |
| check_bias | boolean |  | false |  |
| check_hallucination | boolean |  | false |  |
| pii_threshold | numeric(3,2) |  | 0.7 |  |
| toxicity_threshold | numeric(3,2) |  | 0.7 |  |
| bias_threshold | numeric(3,2) |  | 0.6 |  |
| hallucination_threshold | numeric(3,2) |  | 0.7 |  |
| action_on_pii | character varying(20) |  | 'redact'::character |  |
| action_on_toxicity | character varying(20) |  | 'warn'::character |  |
| action_on_bias | character varying(20) |  | 'log'::character |  |
| action_on_hallucination | character varying(20) |  | 'log'::character |  |
| auto_redact | boolean |  | true |  |
| redact_email | boolean |  | true |  |
| redact_phone | boolean |  | true |  |
| redact_id_card | boolean |  | true |  |
| redact_credit_card | boolean |  | true |  |
| strict_mode | boolean |  | false |  |
| log_all_outputs | boolean |  | false |  |
| whitelist_patterns | text[] |  |  |  |
| total_checks | integer |  | 0 |  |
| total_issues | integer |  | 0 |  |
| total_redactions | integer |  | 0 |  |
| last_check_at | timestamp |  |  |  |
| created_at | timestamp with time zone |  | now() |  |
| updated_at | timestamp with time zone |  | now() |  |
| created_by | character |  |  |  |
| updated_by | character |  |  |  |
| llm_engine_id | integer |  |  |  |
| pii_engine | character varying(20) |  | 'regex'::character |  |
| toxicity_engine | character varying(20) |  | 'keyword'::character |  |
| check_secrets | boolean |  | true |  |
| check_internal_ip | boolean |  | true |  |
| check_jailbreak_response | boolean |  | false |  |
| check_instruction_injection_response | boolean |  | false |  |
| secrets_threshold | numeric(3,2) |  | 0.7 |  |
| internal_ip_threshold | numeric(3,2) |  | 0.7 |  |
| alert_threshold_severity | integer |  | 7 |  |
| action_on_secrets | character varying(20) |  | 'redact'::character |  |
| action_on_internal_ip | character varying(20) |  | 'redact'::character |  |
| action_on_jailbreak_response | character varying(20) |  | 'block'::character |  |
| action_on_instruction_injection_response | character varying(20) |  | 'block'::character |  |
| block_message | text |  | '响应因合规策略被阻断'::text |  |
| redact_bank_card | boolean |  | false |  |
| redact_jwt | boolean |  | true |  |
| redact_password | boolean |  | true |  |
| toxic_replacement | character varying(100) |  | '[内容已过滤]'::character |  |
| redact_format_overrides | jsonb |  | '{}'::jsonb |  |
| whitelist_keywords | text[] |  | '{}'::text[] |  |
| exception_rules | jsonb |  | '[]'::jsonb |  |
| notification_channels | jsonb |  | '[]'::jsonb |  |
| realtime_alert_enabled | boolean |  | false |  |
| alert_aggregation_window_minutes | integer |  | 5 |  |
| sampling_rate | numeric(3,2) |  | 1.0 |  |
| auto_review_queue_enabled | boolean |  | false |  |
| feedback_loop_enabled | boolean |  | false |  |
| skill_generation_enabled | boolean |  | false |  |
| auto_threshold_tuning_enabled | boolean |  | false |  |
| retention_days | integer |  | 90 |  |
| policy_name | character varying(100) |  | 'default'::character |  |

外键：
- (tenant_id) → tenants(code)

## output_compliance_review_queue

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| audit_id | bigint | Y |  |  |
| request_id | character varying(255) | Y |  |  |
| session_key | character |  |  |  |
| issue_type | character varying(50) | Y |  |  |
| issue_subtype | character |  |  |  |
| severity | integer | Y |  |  |
| status | character varying(20) |  | 'pending'::character |  |
| reviewer | character |  |  |  |
| review_comment | text |  |  |  |
| created_at | timestamp with time zone |  | now() |  |
| reviewed_at | timestamp |  |  |  |

索引（1）：
- idx_output_compliance_review_status

## passive_probe_state

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| credential_id | integer | Y |  |  |
| raw_model_name | text | Y |  |  |
| error_kind | text | Y |  |  |
| consecutive_count | integer | Y | 0 |  |
| total_recent_count | integer | Y | 0 |  |
| window_total_count | integer | Y | 0 |  |
| first_seen_at | timestamp with time zone | Y | now() |  |
| last_seen_at | timestamp with time zone | Y | now() |  |
| in_reviewing | boolean | Y | false |  |
| reviewing_until | timestamp |  |  |  |
| final_marked_at | timestamp |  |  |  |
| unavailable_reason | text |  |  |  |
| last_response_body_preview | text |  |  |  |

索引（1）：
- idx_passive_probe_reviewing

## pii_patterns

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| pattern_name | character varying(100) | Y |  |  |
| pattern_type | character varying(50) | Y |  |  |
| regex_pattern | text | Y |  |  |
| description | text |  |  |  |
| enabled | boolean |  | true |  |
| severity | integer |  | 7 |  |
| redact_format | character |  |  |  |
| created_at | timestamp with time zone |  | now() |  |

## price_change_events

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint |  |  |  |
| old_plan_id | bigint |  |  |  |
| new_plan_id | bigint |  |  |  |
| delta_json | jsonb |  |  |  |
| detected_at | timestamp |  |  |  |
| notify_channel | text |  |  |  |
| applied | boolean |  |  |  |

## pricing_plans

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| scope | text | Y |  |  |
| provider_id | bigint |  |  |  |
| credential_id | bigint |  |  |  |
| tenant_id | text |  |  |  |
| model_canonical_id | bigint |  |  |  |
| plan_type | text | Y |  |  |
| currency | text | Y | 'USD'::text |  |
| plan_json | jsonb | Y | '{}'::jsonb |  |
| effective_from | timestamp with time zone | Y | now() |  |
| effective_to | timestamp |  |  |  |
| source | text | Y | 'manual'::text |  |
| confidence | numeric(4,3) |  | 1.000 |  |
| scraped_url | text |  |  |  |
| offer_scope_key | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

## pricing_refresh_log

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| run_id | text | Y |  |  |
| run_ts | timestamp with time zone | Y | now() |  |
| trigger | text | Y | 'cron'::text |  |
| status | text | Y |  |  |
| before_summary | jsonb | Y |  |  |
| after_summary | jsonb | Y |  |  |
| diff_count | integer | Y | 0 |  |
| new_offers | integer | Y | 0 |  |
| removed_offers | integer | Y | 0 |  |
| changed_offers | integer | Y | 0 |  |
| artifacts_path | text |  |  |  |
| feishu_sent | boolean | Y | false |  |
| error_message | text |  |  |  |
| duration_seconds | integer |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

## product_module_features

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| module_key | text | Y |  |  |
| feature_key | text | Y |  |  |
| feature_name | text | Y |  |  |
| description | text | Y | ''::text |  |
| setting_key | text |  |  |  |
| enabled | boolean | Y | true |  |
| created_at | timestamp with time zone | Y | now() |  |

外键：
- (module_key) → product_modules(key)

索引（1）：
- idx_pmf_module

## product_modules

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| key | text | Y |  |  |
| name | text | Y |  |  |
| description | text | Y | ''::text |  |
| category | text | Y |  |  |
| icon | text |  |  |  |
| setting_key | text |  |  |  |
| is_base | boolean | Y | false |  |
| sort_order | integer | Y | 0 |  |
| enabled | boolean | Y | true |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（2）：
- idx_pm_category
- idx_pm_setting

## prompt_injection_detections

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| request_id | character varying(255) | Y |  |  |
| session_key | character |  |  |  |
| detected_at | timestamp with time zone |  | now() |  |
| risk_level | integer | Y |  |  |
| rule_id | integer |  |  |  |
| rule_name | character |  |  |  |
| category | character |  |  |  |
| matched_pattern | text |  |  |  |
| input_sample | text |  |  |  |
| blocked | boolean |  | false |  |
| action_taken | character varying(20) | Y |  |  |
| evidence_text | text |  |  |  |
| input_hash | character |  |  |  |
| client_ip | character |  |  |  |
| user_agent | text |  |  |  |
| llm_engine_id | integer |  |  |  |
| llm_confidence | double |  |  |  |
| llm_reason | text |  |  |  |
| categories | public.injection_category[] |  |  |  |
| canary_token_leaked | character |  |  |  |
| similar_attack_id | bigint |  |  |  |
| approval_id | character |  |  |  |
| replaced_content | text |  |  |  |
| original_content_hash | character |  |  |  |

外键：
- (rule_id) → prompt_injection_rules(id)

索引（6）：
- idx_detections_approval
- idx_detections_categories
- idx_detections_request
- idx_detections_risk
- idx_detections_session
- idx_detections_tenant_time

## prompt_injection_llm_engines

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| tenant_id | character varying(255) | Y | 'default'::character |  |
| engine_name | character varying(100) | Y |  |  |
| description | text |  |  |  |
| model_canonical_id | integer |  |  |  |
| credential_id | integer |  |  |  |
| temperature | double precision |  | 0.1 |  |
| max_tokens | integer |  | 512 |  |
| timeout_ms | integer |  | 3000 |  |
| max_retries | integer |  | 1 |  |
| system_prompt | text | Y | '你是一个专业的 |  |
| detection_prompt | text |  | '分析以下用户输入，判断是否存在提示词注入攻击。返回 |  |
| priority | integer |  | 0 |  |
| enabled | boolean |  | true |  |
| total_calls | integer |  | 0 |  |
| total_detections | integer |  | 0 |  |
| avg_latency_ms | double precision |  | 0 |  |
| error_count | integer |  | 0 |  |
| last_called_at | timestamp |  |  |  |
| created_at | timestamp with time zone |  | now() |  |
| updated_at | timestamp with time zone |  | now() |  |
| created_by | character |  |  |  |

索引（1）：
- idx_llm_engines_tenant

## prompt_injection_policies

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| enabled | boolean |  | true |  |
| detection_mode | character varying(20) |  | 'observe'::character |  |
| enable_basic_rules | boolean |  | true |  |
| enable_advanced_rules | boolean |  | true |  |
| enable_heuristics | boolean |  | true |  |
| enable_ml_model | boolean |  | false |  |
| score_threshold_log | integer |  | 3 |  |
| score_threshold_warn | integer |  | 6 |  |
| score_threshold_sanitize | integer |  | 8 |  |
| score_threshold_block | integer |  | 10 |  |
| action_on_low_risk | character varying(20) |  | 'log'::character |  |
| action_on_medium_risk | character varying(20) |  | 'warn'::character |  |
| action_on_high_risk | character varying(20) |  | 'block'::character |  |
| whitelist_patterns | text[] |  |  |  |
| whitelist_users | text[] |  |  |  |
| notify_on_detection | boolean |  | false |  |
| notification_webhook | character |  |  |  |
| notification_email | character |  |  |  |
| total_detections | integer |  | 0 |  |
| total_blocks | integer |  | 0 |  |
| last_detection_at | timestamp |  |  |  |
| created_at | timestamp with time zone |  | now() |  |
| updated_at | timestamp with time zone |  | now() |  |
| created_by | character |  |  |  |
| updated_by | character |  |  |  |
| llm_engine_id | integer |  |  |  |
| enable_llm_detection | boolean |  | true |  |
| enable_canary_detection | boolean |  | true |  |
| enable_vector_similarity | boolean |  | false |  |
| vector_similarity_threshold | double precision |  | 0.85 |  |
| content_replacement_strategy | character varying(50) |  | 'llm_rewrite'::character |  |
| max_input_length | integer |  | 50000 |  |
| auto_learn_enabled | boolean |  | false |  |
| detection_timeout_ms | integer |  | 5000 |  |

外键：
- (tenant_id) → tenants(code)

## prompt_injection_rules

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| rule_name | character varying(100) | Y |  |  |
| rule_type | character varying(50) | Y |  |  |
| category | character varying(50) | Y |  |  |
| pattern | text | Y |  |  |
| description | text |  |  |  |
| severity | integer | Y |  |  |
| enabled | boolean |  | true |  |
| case_sensitive | boolean |  | false |  |
| created_at | timestamp with time zone |  | now() |  |
| updated_at | timestamp with time zone |  | now() |  |
| category_new | public.injection_category |  |  |  |
| action_override | public.injection_action |  |  |  |
| is_system | boolean |  | true |  |
| tags | text[] |  |  |  |
| examples | text[] |  |  |  |
| false_positive_rate | double precision |  | 0 |  |

## provider_catalog

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| code | text | Y |  |  |
| tier | text | Y |  |  |
| display_name | text | Y |  |  |
| display_name_en | text |  |  |  |
| category | text | Y | 'official'::text |  |
| kind | text | Y | 'cloud'::text |  |
| protocol | text | Y |  |  |
| base_url_template | text | Y |  |  |
| docs_url | text |  |  |  |
| default_egress_profile | text | Y | 'direct'::text |  |
| domestic | boolean | Y | true |  |
| discount_rate_default | numeric(5,4) |  | 1.0 |  |
| models_manifest_json | jsonb |  | '[]'::jsonb |  |
| discovery_strategy | text | Y | 'auto'::text |  |
| models_endpoint_template | text |  |  |  |
| seed_pricing_plans_json | jsonb |  | '[]'::jsonb |  |
| price_sources_json | jsonb |  | '{}'::jsonb |  |
| hidden | boolean | Y | false |  |
| notes | text |  |  |  |
| catalog_version | integer | Y | 1 |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| header_profile_code | text |  |  |  |
| capabilities | jsonb |  | '{}'::jsonb |  |
| vendor_name | text |  |  |  |

## provider_cost_reconciliation

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| reconciliation_month | date | Y |  |  |
| gateway_total_tokens | bigint |  |  |  |
| gateway_input_tokens | bigint |  |  |  |
| gateway_output_tokens | bigint |  |  |  |
| gateway_total_cost | numeric(12,4) |  |  |  |
| provider_total_tokens | bigint |  |  |  |
| provider_input_tokens | bigint |  |  |  |
| provider_output_tokens | bigint |  |  |  |
| provider_total_cost | numeric(12,4) |  |  |  |
| token_diff_rate | numeric(5,4) |  |  |  |
| cost_diff_rate | numeric(5,4) |  |  |  |
| data_source | text |  |  |  |
| notes | text |  |  |  |
| created_at | timestamp with time zone |  | now() |  |
| updated_at | timestamp with time zone |  | now() |  |

索引（1）：
- idx_pcr_provider_month

## provider_credibility_tests

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| credential_id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| model_name | text | Y |  |  |
| test_time | timestamp with time zone | Y |  |  |
| test_type | text | Y |  |  |
| authenticity_score | numeric(5,2) |  |  |  |
| compliance_score | numeric(5,2) |  |  |  |
| consistency_score | numeric(5,2) |  |  |  |
| version_score | numeric(5,2) |  |  |  |
| test_details | jsonb |  |  |  |
| anomalies | jsonb |  |  |  |
| created_at | timestamp with time zone |  | now() |  |

索引（3）：
- idx_pct_credential_time
- idx_pct_model
- idx_pct_provider

## provider_error_details

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| model_name | character |  |  |  |
| endpoint | character |  |  |  |
| error_type | character varying(50) | Y |  |  |
| error_code | character |  |  |  |
| error_message | text |  |  |  |
| request_id | character |  |  |  |
| user_id | character |  |  |  |
| tenant_id | character |  |  |  |
| input_tokens | integer |  |  |  |
| context | jsonb |  |  |  |
| occurrences | integer |  | 1 |  |
| first_seen_at | timestamp with time zone | Y |  |  |
| last_seen_at | timestamp with time zone | Y |  |  |
| acknowledged | boolean |  | false |  |
| resolved | boolean |  | false |  |
| resolution_note | text |  |  |  |
| created_at | timestamp with time zone | Y | CURRENT_TIMESTAMP |  |
| updated_at | timestamp with time zone | Y | CURRENT_TIMESTAMP |  |

索引（3）：
- idx_ped_last_seen
- idx_ped_provider_type
- idx_ped_unresolved

## provider_events

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint |  |  |  |
| credential_id | bigint |  |  |  |
| event_kind | text |  |  |  |
| payload_json | jsonb |  |  |  |
| ts | timestamp |  |  |  |

## provider_header_profiles

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| profile_code | text | Y |  |  |
| display_name | text | Y |  |  |
| protocol | text |  |  |  |
| headers_json | jsonb | Y | '{}'::jsonb |  |
| strip_headers_json | jsonb | Y | '[]'::jsonb |  |
| notes | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

## provider_health_events

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| model_name | character |  |  |  |
| event_type | character varying(50) | Y |  |  |
| severity | character varying(10) | Y |  |  |
| title | character varying(200) | Y |  |  |
| description | text |  |  |  |
| trigger_metric | character |  |  |  |
| trigger_value | numeric(10,2) |  |  |  |
| threshold_value | numeric(10,2) |  |  |  |
| affected_requests_count | bigint |  |  |  |
| estimated_downtime_seconds | integer |  |  |  |
| auto_action | character |  |  |  |
| manual_action | text |  |  |  |
| acknowledged_by | character |  |  |  |
| acknowledged_at | timestamp |  |  |  |
| resolved_at | timestamp |  |  |  |
| notified | boolean |  | false |  |
| notification_channels | text[] |  |  |  |
| created_at | timestamp with time zone | Y | CURRENT_TIMESTAMP |  |

索引（3）：
- idx_phe_provider
- idx_phe_severity
- idx_phe_unresolved

## provider_metrics_hour

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| model_name | character |  |  |  |
| endpoint | character |  |  |  |
| bucket | timestamp with time zone | Y |  |  |
| total_requests | bigint | Y | 0 |  |
| successful_requests | bigint | Y | 0 |  |
| error_5xx | integer | Y | 0 |  |
| error_4xx | integer | Y | 0 |  |
| error_timeout | integer | Y | 0 |  |
| success_rate | numeric(5,2) |  |  |  |
| error_rate_5xx | numeric(5,2) |  |  |  |
| latency_p50 | integer |  |  |  |
| latency_p95 | integer |  |  |  |
| latency_p99 | integer |  |  |  |
| ttft_p95 | integer |  |  |  |
| total_input_tokens | bigint |  | 0 |  |
| total_output_tokens | bigint |  | 0 |  |
| total_cost | numeric(12,6) |  | 0 |  |
| created_at | timestamp with time zone | Y | CURRENT_TIMESTAMP |  |

索引（2）：
- idx_pmh_bucket
- idx_pmh_provider_bucket

## provider_metrics_minute

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| model_name | character |  |  |  |
| endpoint | character |  |  |  |
| bucket | timestamp with time zone | Y |  |  |
| total_requests | integer | Y | 0 |  |
| successful_requests | integer | Y | 0 |  |
| error_5xx | integer | Y | 0 |  |
| error_4xx | integer | Y | 0 |  |
| error_timeout | integer | Y | 0 |  |
| error_other | integer | Y | 0 |  |
| latency_sum | bigint | Y | 0 |  |
| latency_min | integer |  |  |  |
| latency_max | integer |  |  |  |
| latency_p50 | integer |  |  |  |
| latency_p95 | integer |  |  |  |
| latency_p99 | integer |  |  |  |
| ttft_sum | bigint |  | 0 |  |
| ttft_p95 | integer |  |  |  |
| total_input_tokens | bigint |  | 0 |  |
| total_output_tokens | bigint |  | 0 |  |
| total_cost | numeric(12,6) |  | 0 |  |
| created_at | timestamp with time zone | Y | CURRENT_TIMESTAMP |  |

索引（3）：
- idx_pmm_bucket
- idx_pmm_model_bucket
- idx_pmm_provider_bucket

## provider_models

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| tenant_id | text | Y | 'default'::text |  |
| raw_model_name | text | Y |  |  |
| canonical_id | bigint |  |  |  |
| standardized_name | text |  |  |  |
| outbound_model_name | text |  |  |  |
| available | boolean | Y | true |  |
| unavailable_reason | text |  |  |  |
| unavailable_at | timestamp |  |  |  |
| last_seen_at | timestamp with time zone | Y | now() |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| canonical_raw_name | text | Y |  |  |
| modality | text | Y | 'text'::text |  |

索引（5）：
- idx_provider_models_canonical_id
- idx_provider_models_canonical_raw_name
- idx_provider_models_lower_raw_model_name
- idx_provider_models_lower_standardized_name
- uq_provider_models_canonical_raw_name UNIQUE

## provider_models_v1000_backup

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| provider_model_id | bigint | Y |  |  |
| canonical_id | bigint |  |  |  |
| standardized_name | text |  |  |  |
| outbound_model_name | text |  |  |  |

## provider_profile_alerts

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| credential_id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| alert_type | text | Y |  |  |
| alert_level | text | Y |  |  |
| trigger_date | date | Y |  |  |
| current_score | numeric(5,2) |  |  |  |
| previous_score | numeric(5,2) |  |  |  |
| score_change | numeric(5,2) |  |  |  |
| dimension | text |  |  |  |
| message | text | Y |  |  |
| details | jsonb |  |  |  |
| action_taken | text |  |  |  |
| resolved_at | timestamp |  |  |  |
| created_at | timestamp with time zone |  | now() |  |

索引（4）：
- idx_ppa_credential
- idx_ppa_provider
- idx_ppa_type_level
- idx_ppa_unresolved

## provider_profile_daily

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| credential_id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| profile_date | date | Y |  |  |
| network_score | numeric(5,2) |  |  |  |
| credibility_score | numeric(5,2) |  |  |  |
| availability_score | numeric(5,2) |  |  |  |
| stability_score | numeric(5,2) |  |  |  |
| scale_score | numeric(5,2) |  |  |  |
| cost_accuracy_score | numeric(5,2) |  |  |  |
| price_score | numeric(5,2) |  |  |  |
| total_score | numeric(5,2) |  |  |  |
| timeslot_scores | jsonb |  |  |  |
| score_stddev | numeric(5,2) |  |  |  |
| best_timeslot | text |  |  |  |
| worst_timeslot | text |  |  |  |
| raw_stats | jsonb |  |  |  |
| created_at | timestamp with time zone |  | now() |  |

索引（4）：
- idx_ppd_credential_date
- idx_ppd_date
- idx_ppd_provider_date
- idx_ppd_total_score

## provider_profile_metrics

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| credential_id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| metric_time | timestamp with time zone | Y |  |  |
| time_slot | text | Y |  |  |
| network_latency_p50 | integer |  |  |  |
| network_latency_p95 | integer |  |  |  |
| network_latency_p99 | integer |  |  |  |
| availability_total_requests | integer |  | 0 |  |
| availability_success_requests | integer |  | 0 |  |
| availability_ttft_avg_ms | integer |  |  |  |
| availability_duration_avg_ms | integer |  |  |  |
| stability_error_count | integer |  | 0 |  |
| stability_error_types | jsonb |  |  |  |
| scale_total_models | integer |  |  |  |
| scale_available_models | integer |  |  |  |
| rate_limit_hits | integer |  |  |  |
| rate_limit_total_requests | integer |  |  |  |
| concurrency_limit | integer |  |  |  |
| concurrency_limit_auto | integer |  |  |  |
| concurrency_eff_limit | integer |  |  |  |
| concurrency_is_capped | boolean |  |  |  |
| downtime_buckets | integer |  |  |  |
| downtime_total_buckets | integer |  |  |  |
| longest_downtime_run | integer |  |  |  |
| quality_stability_mean | double |  |  |  |
| quality_stability_stddev | double |  |  |  |
| quality_stability_cv | double |  |  |  |
| quality_stability_is_volatile | boolean |  |  |  |
| quality_stability_sample_n | integer |  |  |  |
| created_at | timestamp with time zone |  | now() |  |

索引（3）：
- idx_ppm_cleanup
- idx_ppm_credential_time
- idx_ppm_provider_time

## provider_profile_whitelist

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| reason | text |  |  |  |
| added_by | text |  |  |  |
| added_at | timestamp with time zone |  | now() |  |

索引（1）：
- idx_ppw_provider

## provider_quality_configs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| alert_error_rate_5xx_p0 | numeric(5,2) |  | 5.0 |  |
| alert_error_rate_5xx_p1 | numeric(5,2) |  | 1.0 |  |
| alert_availability_p0 | numeric(5,2) |  | 95.0 |  |
| alert_latency_p99_p0 | integer |  | 30000 |  |
| alert_latency_p95_p1 | integer |  | 10000 |  |
| target_success_rate | numeric(5,2) |  | 99.5 |  |
| target_latency_p95 | integer |  | 5000 |  |
| target_latency_p99 | integer |  | 10000 |  |
| weight_availability | numeric(3,2) |  | 0.4 |  |
| weight_performance | numeric(3,2) |  | 0.3 |  |
| weight_stability | numeric(3,2) |  | 0.2 |  |
| weight_cost_efficiency | numeric(3,2) |  | 0.1 |  |
| circuit_breaker_enabled | boolean |  | true |  |
| circuit_breaker_threshold | integer |  | 5 |  |
| circuit_breaker_timeout_seconds | integer |  | 300 |  |
| downgrade_on_score_below | numeric(5,2) |  | 70.0 |  |
| downgrade_weight_multiplier | numeric(3,2) |  | 0.5 |  |
| updated_at | timestamp with time zone | Y | CURRENT_TIMESTAMP |  |
| created_at | timestamp with time zone | Y | CURRENT_TIMESTAMP |  |

外键：
- (provider_id) → providers(id)

## provider_quality_profiles

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| model_name | character |  |  |  |
| success_rate_5m | numeric(5,2) |  |  |  |
| success_rate_1h | numeric(5,2) |  |  |  |
| success_rate_24h | numeric(5,2) |  |  |  |
| error_rate_5xx_5m | numeric(5,2) |  |  |  |
| error_rate_5xx_1h | numeric(5,2) |  |  |  |
| error_rate_5xx_24h | numeric(5,2) |  |  |  |
| error_rate_4xx_5m | numeric(5,2) |  |  |  |
| error_rate_4xx_1h | numeric(5,2) |  |  |  |
| error_rate_4xx_24h | numeric(5,2) |  |  |  |
| error_rate_timeout_5m | numeric(5,2) |  |  |  |
| error_rate_timeout_1h | numeric(5,2) |  |  |  |
| error_rate_timeout_24h | numeric(5,2) |  |  |  |
| availability_24h | numeric(5,2) |  |  |  |
| latency_p50_5m | integer |  |  |  |
| latency_p50_1h | integer |  |  |  |
| latency_p50_24h | integer |  |  |  |
| latency_p95_5m | integer |  |  |  |
| latency_p95_1h | integer |  |  |  |
| latency_p95_24h | integer |  |  |  |
| latency_p99_5m | integer |  |  |  |
| latency_p99_1h | integer |  |  |  |
| latency_p99_24h | integer |  |  |  |
| ttft_p95_5m | integer |  |  |  |
| ttft_p95_1h | integer |  |  |  |
| ttft_p95_24h | integer |  |  |  |
| throughput_tokens_per_sec_1h | numeric(10,2) |  |  |  |
| volatility_24h | numeric(5,3) |  |  |  |
| mttr_seconds_24h | integer |  |  |  |
| error_diversity_score_24h | numeric(5,2) |  |  |  |
| consecutive_failures | integer |  | 0 |  |
| last_failure_at | timestamp |  |  |  |
| last_recovery_at | timestamp |  |  |  |
| cost_per_1k_tokens | numeric(10,6) |  |  |  |
| quota_usage_percentage | numeric(5,2) |  |  |  |
| availability_score | numeric(5,2) |  |  |  |
| performance_score | numeric(5,2) |  |  |  |
| stability_score | numeric(5,2) |  |  |  |
| cost_efficiency_score | numeric(5,2) |  |  |  |
| quality_score | numeric(5,2) |  |  |  |
| quality_grade | character |  |  |  |
| total_requests_5m | bigint |  | 0 |  |
| total_requests_1h | bigint |  | 0 |  |
| total_requests_24h | bigint |  | 0 |  |
| successful_requests_5m | bigint |  | 0 |  |
| successful_requests_1h | bigint |  | 0 |  |
| successful_requests_24h | bigint |  | 0 |  |
| updated_at | timestamp with time zone | Y | CURRENT_TIMESTAMP |  |
| created_at | timestamp with time zone | Y | CURRENT_TIMESTAMP |  |
| reliability_score | numeric(5,2) |  | 0 |  |
| calculated_at | timestamp without time zone |  | CURRENT_TIMESTAMP |  |

外键：
- (provider_id) → providers(id)

索引（8）：
- idx_pqp_provider_id
- idx_pqp_provider_model
- idx_pqp_quality_score
- idx_pqp_updated_at
- idx_quality_profiles_calculated_at
- idx_quality_profiles_model
- idx_quality_profiles_provider
- idx_quality_profiles_quality_score

## provider_quality_rollup

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| provider_id | integer | Y |  |  |
| bucket_start | timestamp with time zone | Y |  |  |
| total_requests | integer | Y | 0 |  |
| bad_requests | integer | Y | 0 |  |
| fixed_requests | integer | Y | 0 |  |
| avg_quality_score | numeric(3,2) |  |  |  |
| top_flag | text |  |  |  |

索引（1）：
- idx_provider_quality_rollup_bucket

## provider_scores

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| credential_id | bigint | Y |  |  |
| canonical_id | bigint |  |  |  |
| score | numeric(6,4) | Y |  |  |
| factors_json | jsonb |  |  |  |
| computed_at | timestamp with time zone | Y | now() |  |

## provider_settings

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| provider_id | bigint | Y |  |  |
| setting_key | text | Y |  |  |
| setting_value | jsonb | Y |  |  |
| enabled | boolean | Y | true |  |
| created_by | text |  | 'system'::text |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（2）：
- idx_provider_settings_key
- idx_provider_settings_provider

## providers

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | text | Y | 'default'::text |  |
| code | text | Y |  |  |
| display_name | text | Y |  |  |
| catalog_code | text |  |  |  |
| is_custom | boolean | Y | false |  |
| catalog_version_at_create | integer |  |  |  |
| user_overrides_json | jsonb | Y | '[]'::jsonb |  |
| kind | text | Y | 'cloud'::text |  |
| category | text | Y | 'official'::text |  |
| protocol | text | Y |  |  |
| base_url | text | Y |  |  |
| egress_profile | text | Y | 'direct'::text |  |
| domestic | boolean | Y | true |  |
| discount_rate | numeric(5,4) |  | 1.0 |  |
| enabled | boolean | Y | true |  |
| network_quality_score | numeric(4,3) |  | 1.000 |  |
| owner_user | text |  |  |  |
| notes | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| manual_disabled | boolean | Y | false |  |
| quality_fix_mode | text | Y | 'off'::text |  |

## release_artifacts

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| release_version | text | Y |  |  |
| platform | text | Y |  |  |
| arch | text | Y | ''::text |  |
| edition | text | Y | 'customer'::text |  |
| artifact_name | text | Y |  |  |
| sha256 | text | Y | ''::text |  |
| size_bytes | bigint | Y | 0 |  |
| download_path | text | Y |  |  |
| created_at | timestamp with time zone | Y | now() |  |

## releases

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| version | text | Y |  |  |
| build_seq | integer | Y |  |  |
| channel | text | Y | 'stable'::text |  |
| title | text | Y |  |  |
| description | text |  |  |  |
| changelog | text |  |  |  |
| image_tag | text | Y |  |  |
| image_digest | text |  |  |  |
| min_version | text |  |  |  |
| mandatory | boolean | Y | false |  |
| created_by | text | Y |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| published_at | timestamp |  |  |  |

索引（3）：
- idx_releases_channel
- idx_releases_published
- idx_releases_version

## request_attachments

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| request_id | text | Y |  |  |
| attachment_type | text | Y | 'image'::text |  |
| content_type | text |  |  |  |
| size_bytes | bigint | Y | 0 |  |
| storage_path | text |  |  |  |
| hash | text |  |  |  |
| original_url | text |  |  |  |
| message_index | integer | Y | 0 |  |
| block_index | integer | Y | 0 |  |
| status | text | Y | 'detected'::text |  |
| error_code | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

索引（4）：
- idx_request_attachments_created_at
- idx_request_attachments_hash
- idx_request_attachments_request_id
- idx_request_attachments_status_time

## request_context_attrs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y | now() |  |
| tenant_id | text | Y | 'default'::text |  |
| gw_session_id | text |  |  |  |
| gw_task_id | text |  |  |  |
| identity_hash | character |  |  |  |
| virtual_client_id | character |  |  |  |
| virtual_ip | inet |  |  |  |
| virtual_mac | character |  |  |  |
| agent_name | character |  |  |  |
| agent_type | character |  |  |  |
| client_ip | inet |  |  |  |
| client_forwarded_for | text |  |  |  |
| api_key_fingerprint | character |  |  |  |
| api_key_id | bigint |  |  |  |
| application_id | bigint |  |  |  |
| application_code | text |  |  |  |
| owner_user | text |  |  |  |
| end_user_id | text |  |  |  |
| customer_id | bigint |  |  |  |
| client_protocol | character |  |  |  |
| is_retry | boolean | Y | false |  |
| attempt_no | integer |  |  |  |
| is_probe | boolean | Y | false |  |
| origin_stage | character |  |  |  |
| turn_no | integer |  |  |  |
| source_channel | character |  |  |  |
| client_request_id | text |  |  |  |
| project_id | text |  |  |  |
| session_title | text |  |  |  |
| session_summary | text |  |  |  |
| task_id | text |  |  |  |
| fingerprint_raw | jsonb |  |  |  |

索引（7）：
- idx_rca_agent
- idx_rca_customer
- idx_rca_identity_hash
- idx_rca_probe
- idx_rca_session_turn
- idx_rca_tenant_ts
- idx_rca_ts

## request_envelope

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | uuid | Y |  |  |
| client_model | text | Y |  |  |
| client_metadata | jsonb |  |  |  |
| client_headers_redacted | jsonb |  |  |  |
| outbound_model | text |  |  |  |
| outbound_protocol | text |  |  |  |
| credential_id | bigint |  |  |  |
| fingerprint_seed | text |  |  |  |
| stream_chunks_sent | integer | Y | 0 |  |
| stream_completed | boolean | Y | false |  |
| created_at | timestamp with time zone | Y | now() |  |
| expires_at | timestamp with time zone | Y |  |  |

## request_logs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | nextval('public.request_logs_id_seq'::regclass) |  |
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y |  |  |
| tenant_id | text | Y |  |  |
| application_id | bigint |  |  |  |
| api_key_id | bigint |  |  |  |
| end_user_id | text |  |  |  |
| client_model | text |  |  |  |
| outbound_model | text |  |  |  |
| credential_id | bigint |  |  |  |
| provider_id | bigint |  |  |  |
| canonical_id | bigint |  |  |  |
| client_profile | text |  |  |  |
| request_mode | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| total_tokens | integer |  |  |  |
| cost_usd | numeric(14,8) |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean | Y |  |  |
| error_kind | text |  |  |  |
| search_text | text |  |  |  |
| cache_read_tokens | integer |  |  |  |
| cache_write_tokens | integer |  |  |  |
| identity_hash | text |  |  |  |
| virtual_client_id | text |  |  |  |
| virtual_ip | text |  |  |  |
| virtual_mac | text |  |  |  |
| affinity_hit | boolean |  |  |  |
| stream_first_chunk_ms | integer |  |  |  |
| stream_chunk_count | integer |  |  |  |
| stream_interrupted | boolean |  |  |  |
| stream_done_sent | boolean |  |  |  |
| request_checksum | text |  |  |  |
| response_checksum | text |  |  |  |
| transform_rule_id | text |  |  |  |
| egress_protocol | text |  |  |  |
| failure_stage | text |  |  |  |
| failure_detail_code | text |  |  |  |
| request_preview | text |  |  |  |
| transform_summary | text |  |  |  |
| response_preview | text |  |  |  |
| stream_done_received | boolean |  |  |  |
| request_body | jsonb |  |  |  |
| response_body | jsonb |  |  |  |
| cost_display | numeric(14,8) |  |  |  |
| cost_currency | text |  |  |  |
| usage_source | text | Y | 'llm'::text |  |
| gw_session_id | text |  |  |  |
| gw_task_id | text |  |  |  |
| request_status | text |  |  |  |
| api_key_prefix | text |  |  |  |
| owner_user | text |  |  |  |
| application_code | text |  |  |  |
| key_alias | text |  |  |  |
| api_key_owner_user | text |  |  |  |
| is_auto_request | boolean |  | false |  |
| task_type | text |  |  |  |
| auto_profile | text |  |  |  |
| auto_decision | jsonb |  |  |  |
| auto_confidence | numeric(4,3) |  |  |  |
| work_type | text |  |  |  |
| task_type_chosen | text |  |  |  |
| confidence_num | numeric(4,3) |  |  |  |
| model_chosen | text |  |  |  |
| strategy_used | text |  |  |  |
| credits_charged | bigint |  |  |  |
| parent_request_id | text |  |  |  |
| compression_reason | text |  |  |  |
| compression_strategy | text |  |  |  |
| compression_meta | jsonb |  |  |  |
| outbound_body | jsonb |  |  |  |
| outbound_msg_count | integer |  |  |  |
| outbound_token_est | integer |  |  |  |
| outbound_msg_hashes | jsonb |  |  |  |
| quality_flags | text[] | Y | '{}'::text[] |  |
| quality_fix_actions | jsonb | Y | '{}'::jsonb |  |
| quality_score | numeric(3,2) |  |  |  |
| upstream_finish_reason | text |  |  |  |
| tool_calls | jsonb |  |  |  |
| client_endpoint | text |  |  |  |
| client_timeout | boolean |  |  |  |
| stream_chunk_errors | integer |  |  |  |
| stream_chunks_sent | integer | Y | 0 |  |
| client_request_id | text |  |  |  |
| upstream_status_code | integer |  |  |  |
| test_col | text[] | Y | '{}'::text[] |  |
| test_tab_indent | text |  |  |  |
| provider_model | text |  |  |  |
| attachments | jsonb |  |  |  |
| has_attachments | boolean |  |  |  |
| attachment_count | integer |  |  |  |
| client_ip | inet |  |  |  |
| client_forwarded_for | text |  |  |  |
| agent_name | character |  |  |  |
| agent_type | character |  |  |  |
| api_key_fingerprint | character |  |  |  |
| customer_id | bigint |  |  |  |
| upstream_endpoint | text |  |  |  |
| session_title | text |  |  |  |
| session_summary | text |  |  |  |
| task_id | character |  |  |  |
| task_title | text |  |  |  |
| compression_start_index | integer |  |  |  |
| compression_end_index | integer |  |  |  |
| compression_ratio | double |  |  |  |
| cache_hit | boolean |  |  |  |
| cache_tokens_saved | integer |  |  |  |
| content_safety_score | jsonb |  |  |  |
| dlp_violations | jsonb |  |  |  |
| sensitive_keywords | text[] |  |  |  |
| rate_limit_status | character |  |  |  |
| client_protocol | character |  |  |  |
| upstream_protocol | character |  |  |  |
| protocol_conversion | boolean |  |  |  |
| ir_extensions | jsonb |  |  |  |
| sanitizer_mutations | jsonb |  |  |  |
| vendor_metadata | jsonb |  |  |  |
| reasoning_tokens | integer |  |  |  |
| image_tokens | integer |  |  |  |
| audio_tokens | integer |  |  |  |
| video_tokens | integer |  |  |  |
| provider_tokens | integer |  |  |  |
| origin_stage | character varying(32) |  | NULL::character |  |
| origin_actor | character varying(255) |  | NULL::character |  |
| trace_events | jsonb |  |  |  |
| routing_attempts | jsonb |  |  |  |
| routing_summary | text |  |  |  |
| effective_timeout_seconds | integer |  |  |  |
| context_size_tokens | integer |  |  |  |
| timeout_mode | character |  |  |  |
| is_continuation | boolean |  | false |  |
| continuation_keywords | text[] |  |  |  |
| node_switch_count | integer |  | 0 |  |
| keepalive_sent_count | integer |  | 0 |  |
| cached_response_id | bigint |  |  |  |
| canonical_model | text |  |  |  |

## request_logs_2026_07

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | NULL |  |
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y |  |  |
| tenant_id | text | Y |  |  |
| application_id | bigint |  |  |  |
| api_key_id | bigint |  |  |  |
| end_user_id | text |  |  |  |
| client_model | text |  |  |  |
| outbound_model | text |  |  |  |
| credential_id | bigint |  |  |  |
| provider_id | bigint |  |  |  |
| canonical_id | bigint |  |  |  |
| client_profile | text |  |  |  |
| request_mode | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| total_tokens | integer |  |  |  |
| cost_usd | numeric(14,8) |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean | Y |  |  |
| error_kind | text |  |  |  |
| search_text | text |  |  |  |
| cache_read_tokens | integer |  |  |  |
| cache_write_tokens | integer |  |  |  |
| identity_hash | text |  |  |  |
| virtual_client_id | text |  |  |  |
| virtual_ip | text |  |  |  |
| virtual_mac | text |  |  |  |
| affinity_hit | boolean |  |  |  |
| stream_first_chunk_ms | integer |  |  |  |
| stream_chunk_count | integer |  |  |  |
| stream_interrupted | boolean |  |  |  |
| stream_done_sent | boolean |  |  |  |
| request_checksum | text |  |  |  |
| response_checksum | text |  |  |  |
| transform_rule_id | text |  |  |  |
| egress_protocol | text |  |  |  |
| failure_stage | text |  |  |  |
| failure_detail_code | text |  |  |  |
| request_preview | text |  |  |  |
| transform_summary | text |  |  |  |
| response_preview | text |  |  |  |
| stream_done_received | boolean |  |  |  |
| request_body | jsonb |  |  |  |
| response_body | jsonb |  |  |  |
| cost_display | numeric(14,8) |  |  |  |
| cost_currency | text |  |  |  |
| usage_source | text | Y | NULL |  |
| gw_session_id | text |  |  |  |
| gw_task_id | text |  |  |  |
| request_status | text |  |  |  |
| api_key_prefix | text |  |  |  |
| owner_user | text |  |  |  |
| application_code | text |  |  |  |
| key_alias | text |  |  |  |
| api_key_owner_user | text |  |  |  |
| is_auto_request | boolean |  | NULL |  |
| task_type | text |  |  |  |
| auto_profile | text |  |  |  |
| auto_decision | jsonb |  |  |  |
| auto_confidence | numeric(4,3) |  |  |  |
| work_type | text |  |  |  |
| task_type_chosen | text |  |  |  |
| confidence_num | numeric(4,3) |  |  |  |
| model_chosen | text |  |  |  |
| strategy_used | text |  |  |  |
| credits_charged | bigint |  |  |  |
| parent_request_id | text |  |  |  |
| compression_reason | text |  |  |  |
| compression_strategy | text |  |  |  |
| compression_meta | jsonb |  |  |  |
| outbound_body | jsonb |  |  |  |
| outbound_msg_count | integer |  |  |  |
| outbound_token_est | integer |  |  |  |
| outbound_msg_hashes | jsonb |  |  |  |
| quality_flags | text[] | Y | NULL |  |
| quality_fix_actions | jsonb | Y | NULL |  |
| quality_score | numeric(3,2) |  |  |  |
| upstream_finish_reason | text |  |  |  |
| tool_calls | jsonb |  |  |  |
| client_endpoint | text |  |  |  |
| client_timeout | boolean |  |  |  |
| stream_chunk_errors | integer |  |  |  |
| stream_chunks_sent | integer | Y | NULL |  |
| client_request_id | text |  |  |  |
| upstream_status_code | integer |  |  |  |
| test_col | text[] | Y | NULL |  |
| test_tab_indent | text |  |  |  |
| provider_model | text |  |  |  |
| attachments | jsonb |  |  |  |
| has_attachments | boolean |  |  |  |
| attachment_count | integer |  |  |  |
| client_ip | inet |  |  |  |
| client_forwarded_for | text |  |  |  |
| agent_name | character |  |  |  |
| agent_type | character |  |  |  |
| api_key_fingerprint | character |  |  |  |
| customer_id | bigint |  |  |  |
| upstream_endpoint | text |  |  |  |
| session_title | text |  |  |  |
| session_summary | text |  |  |  |
| task_id | character |  |  |  |
| task_title | text |  |  |  |
| compression_start_index | integer |  |  |  |
| compression_end_index | integer |  |  |  |
| compression_ratio | double |  |  |  |
| cache_hit | boolean |  |  |  |
| cache_tokens_saved | integer |  |  |  |
| content_safety_score | jsonb |  |  |  |
| dlp_violations | jsonb |  |  |  |
| sensitive_keywords | text[] |  |  |  |
| rate_limit_status | character |  |  |  |
| client_protocol | character |  |  |  |
| upstream_protocol | character |  |  |  |
| protocol_conversion | boolean |  |  |  |
| ir_extensions | jsonb |  |  |  |
| sanitizer_mutations | jsonb |  |  |  |
| vendor_metadata | jsonb |  |  |  |
| reasoning_tokens | integer |  |  |  |
| image_tokens | integer |  |  |  |
| audio_tokens | integer |  |  |  |
| video_tokens | integer |  |  |  |
| provider_tokens | integer |  |  |  |
| origin_stage | character varying(32) |  | NULL::character |  |
| origin_actor | character varying(255) |  | NULL::character |  |
| trace_events | jsonb |  |  |  |
| routing_attempts | jsonb |  |  |  |
| routing_summary | text |  |  |  |
| effective_timeout_seconds | integer |  |  |  |
| context_size_tokens | integer |  |  |  |
| timeout_mode | character |  |  |  |
| is_continuation | boolean |  | false |  |
| continuation_keywords | text[] |  |  |  |
| node_switch_count | integer |  | 0 |  |
| keepalive_sent_count | integer |  | 0 |  |
| cached_response_id | bigint |  |  |  |
| canonical_model | text |  |  |  |

索引（34）：
- request_logs_2026_07_agent_type_idx
- request_logs_2026_07_cached_response_id_idx
- request_logs_2026_07_canonical_model_ts_idx
- request_logs_2026_07_client_ip_idx
- request_logs_2026_07_client_model_idx1
- request_logs_2026_07_client_model_idx2
- request_logs_2026_07_client_model_idx3
- request_logs_2026_07_client_request_id_ts_idx
- request_logs_2026_07_customer_id_idx
- request_logs_2026_07_effective_timeout_seconds_latency_ms_idx
- request_logs_2026_07_gw_session_id_ts_idx
- request_logs_2026_07_gw_session_id_ts_idx1
- request_logs_2026_07_gw_task_id_ts_idx
- request_logs_2026_07_lower_idx
- request_logs_2026_07_node_switch_count_ts_idx
- request_logs_2026_07_parent_request_id_ts_idx
- request_logs_2026_07_protocol_conversion_idx
- request_logs_2026_07_provider_id_quality_score_ts_idx
- request_logs_2026_07_provider_id_ts_idx
- request_logs_2026_07_provider_model_ts_idx
- …共 34 个

## request_logs_2026_08

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | nextval('public.request_logs_id_seq'::regclass) |  |
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y |  |  |
| tenant_id | text | Y |  |  |
| application_id | bigint |  |  |  |
| api_key_id | bigint |  |  |  |
| end_user_id | text |  |  |  |
| client_model | text |  |  |  |
| outbound_model | text |  |  |  |
| credential_id | bigint |  |  |  |
| provider_id | bigint |  |  |  |
| canonical_id | bigint |  |  |  |
| client_profile | text |  |  |  |
| request_mode | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| total_tokens | integer |  |  |  |
| cost_usd | numeric(14,8) |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean | Y |  |  |
| error_kind | text |  |  |  |
| search_text | text |  |  |  |
| cache_read_tokens | integer |  |  |  |
| cache_write_tokens | integer |  |  |  |
| identity_hash | text |  |  |  |
| virtual_client_id | text |  |  |  |
| virtual_ip | text |  |  |  |
| virtual_mac | text |  |  |  |
| affinity_hit | boolean |  |  |  |
| stream_first_chunk_ms | integer |  |  |  |
| stream_chunk_count | integer |  |  |  |
| stream_interrupted | boolean |  |  |  |
| stream_done_sent | boolean |  |  |  |
| request_checksum | text |  |  |  |
| response_checksum | text |  |  |  |
| transform_rule_id | text |  |  |  |
| egress_protocol | text |  |  |  |
| failure_stage | text |  |  |  |
| failure_detail_code | text |  |  |  |
| request_preview | text |  |  |  |
| transform_summary | text |  |  |  |
| response_preview | text |  |  |  |
| stream_done_received | boolean |  |  |  |
| request_body | jsonb |  |  |  |
| response_body | jsonb |  |  |  |
| cost_display | numeric(14,8) |  |  |  |
| cost_currency | text |  |  |  |
| usage_source | text | Y | 'llm'::text |  |
| gw_session_id | text |  |  |  |
| gw_task_id | text |  |  |  |
| request_status | text |  |  |  |
| api_key_prefix | text |  |  |  |
| owner_user | text |  |  |  |
| application_code | text |  |  |  |
| key_alias | text |  |  |  |
| api_key_owner_user | text |  |  |  |
| is_auto_request | boolean |  | false |  |
| task_type | text |  |  |  |
| auto_profile | text |  |  |  |
| auto_decision | jsonb |  |  |  |
| auto_confidence | numeric(4,3) |  |  |  |
| work_type | text |  |  |  |
| task_type_chosen | text |  |  |  |
| confidence_num | numeric(4,3) |  |  |  |
| model_chosen | text |  |  |  |
| strategy_used | text |  |  |  |
| credits_charged | bigint |  |  |  |
| parent_request_id | text |  |  |  |
| compression_reason | text |  |  |  |
| compression_strategy | text |  |  |  |
| compression_meta | jsonb |  |  |  |
| outbound_body | jsonb |  |  |  |
| outbound_msg_count | integer |  |  |  |
| outbound_token_est | integer |  |  |  |
| outbound_msg_hashes | jsonb |  |  |  |
| quality_flags | text[] | Y | '{}'::text[] |  |
| quality_fix_actions | jsonb | Y | '{}'::jsonb |  |
| quality_score | numeric(3,2) |  |  |  |
| upstream_finish_reason | text |  |  |  |
| tool_calls | jsonb |  |  |  |
| client_endpoint | text |  |  |  |
| client_timeout | boolean |  |  |  |
| stream_chunk_errors | integer |  |  |  |
| stream_chunks_sent | integer | Y | 0 |  |
| client_request_id | text |  |  |  |
| upstream_status_code | integer |  |  |  |
| test_col | text[] | Y | '{}'::text[] |  |
| test_tab_indent | text |  |  |  |
| provider_model | text |  |  |  |
| attachments | jsonb |  |  |  |
| has_attachments | boolean |  |  |  |
| attachment_count | integer |  |  |  |
| client_ip | inet |  |  |  |
| client_forwarded_for | text |  |  |  |
| agent_name | character |  |  |  |
| agent_type | character |  |  |  |
| api_key_fingerprint | character |  |  |  |
| customer_id | bigint |  |  |  |
| upstream_endpoint | text |  |  |  |
| session_title | text |  |  |  |
| session_summary | text |  |  |  |
| task_id | character |  |  |  |
| task_title | text |  |  |  |
| compression_start_index | integer |  |  |  |
| compression_end_index | integer |  |  |  |
| compression_ratio | double |  |  |  |
| cache_hit | boolean |  |  |  |
| cache_tokens_saved | integer |  |  |  |
| content_safety_score | jsonb |  |  |  |
| dlp_violations | jsonb |  |  |  |
| sensitive_keywords | text[] |  |  |  |
| rate_limit_status | character |  |  |  |
| client_protocol | character |  |  |  |
| upstream_protocol | character |  |  |  |
| protocol_conversion | boolean |  |  |  |
| ir_extensions | jsonb |  |  |  |
| sanitizer_mutations | jsonb |  |  |  |
| vendor_metadata | jsonb |  |  |  |
| reasoning_tokens | integer |  |  |  |
| image_tokens | integer |  |  |  |
| audio_tokens | integer |  |  |  |
| video_tokens | integer |  |  |  |
| provider_tokens | integer |  |  |  |
| origin_stage | character varying(32) |  | NULL::character |  |
| origin_actor | character varying(255) |  | NULL::character |  |
| trace_events | jsonb |  |  |  |
| routing_attempts | jsonb |  |  |  |
| routing_summary | text |  |  |  |
| effective_timeout_seconds | integer |  |  |  |
| context_size_tokens | integer |  |  |  |
| timeout_mode | character |  |  |  |
| is_continuation | boolean |  | false |  |
| continuation_keywords | text[] |  |  |  |
| node_switch_count | integer |  | 0 |  |
| keepalive_sent_count | integer |  | 0 |  |
| cached_response_id | bigint |  |  |  |
| canonical_model | text |  |  |  |

索引（37）：
- request_logs_2026_08_agent_type_idx
- request_logs_2026_08_cached_response_id_idx
- request_logs_2026_08_canonical_model_ts_idx
- request_logs_2026_08_client_ip_idx
- request_logs_2026_08_client_model_idx
- request_logs_2026_08_client_model_idx1
- request_logs_2026_08_client_model_idx2
- request_logs_2026_08_client_request_id_ts_idx
- request_logs_2026_08_client_timeout_ts_idx
- request_logs_2026_08_customer_id_idx
- request_logs_2026_08_effective_timeout_seconds_latency_ms_idx
- request_logs_2026_08_gw_session_id_ts_idx
- request_logs_2026_08_gw_session_id_ts_idx1
- request_logs_2026_08_gw_task_id_ts_idx
- request_logs_2026_08_lower_idx
- request_logs_2026_08_node_switch_count_ts_idx
- request_logs_2026_08_parent_request_id_ts_idx
- request_logs_2026_08_protocol_conversion_idx
- request_logs_2026_08_provider_id_quality_score_ts_idx
- request_logs_2026_08_provider_id_ts_idx
- …共 37 个

## request_logs_archive

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y |  |  |
| tenant_id | text | Y |  |  |
| application_id | bigint |  |  |  |
| api_key_id | bigint |  |  |  |
| end_user_id | text |  |  |  |
| client_model | text |  |  |  |
| outbound_model | text |  |  |  |
| credential_id | bigint |  |  |  |
| provider_id | bigint |  |  |  |
| canonical_id | bigint |  |  |  |
| client_profile | text |  |  |  |
| request_mode | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| total_tokens | integer |  |  |  |
| cost_usd | numeric(14,8) |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean | Y |  |  |
| error_kind | text |  |  |  |
| search_text | text |  |  |  |
| cache_read_tokens | integer |  |  |  |
| cache_write_tokens | integer |  |  |  |
| identity_hash | text |  |  |  |
| virtual_client_id | text |  |  |  |
| virtual_ip | text |  |  |  |
| virtual_mac | text |  |  |  |
| affinity_hit | boolean |  |  |  |
| stream_first_chunk_ms | integer |  |  |  |
| stream_chunk_count | integer |  |  |  |
| stream_chunks_sent | integer | Y | 0 |  |
| stream_chunk_errors | integer |  |  |  |
| stream_done_sent | boolean |  |  |  |
| client_timeout | boolean |  |  |  |
| client_endpoint | text |  |  |  |
| failure_stage | text |  |  |  |
| failure_detail_code | text |  |  |  |
| request_preview | text |  |  |  |
| transform_summary | text |  |  |  |
| response_preview | text |  |  |  |
| stream_done_received | boolean |  |  |  |
| request_body | jsonb |  |  |  |
| response_body | jsonb |  |  |  |
| cost_display | numeric(14,8) |  |  |  |
| cost_currency | text |  |  |  |
| usage_source | text | Y | 'llm'::text |  |
| gw_session_id | text |  |  |  |
| gw_task_id | text |  |  |  |
| request_status | text |  |  |  |
| api_key_prefix | text |  |  |  |
| owner_user | text |  |  |  |
| application_code | text |  |  |  |
| key_alias | text |  |  |  |
| api_key_owner_user | text |  |  |  |
| is_auto_request | boolean |  | false |  |
| task_type | text |  |  |  |
| auto_profile | text |  |  |  |
| auto_decision | jsonb |  |  |  |
| auto_confidence | numeric(4,3) |  |  |  |
| work_type | text |  |  |  |
| task_type_chosen | text |  |  |  |
| confidence_num | numeric(4,3) |  |  |  |
| model_chosen | text |  |  |  |
| strategy_used | text |  |  |  |
| credits_charged | bigint |  |  |  |
| parent_request_id | text |  |  |  |
| compression_reason | text |  |  |  |
| compression_strategy | text |  |  |  |
| compression_meta | jsonb |  |  |  |
| outbound_body | jsonb |  |  |  |
| outbound_msg_count | integer |  |  |  |
| outbound_token_est | integer |  |  |  |
| outbound_msg_hashes | jsonb |  |  |  |
| quality_flags | text[] | Y | '{}'::text[] |  |
| quality_fix_actions | jsonb | Y | '{}'::jsonb |  |
| quality_score | numeric(3,2) |  |  |  |
| upstream_finish_reason | text |  |  |  |
| tool_calls | jsonb |  |  |  |
| stream_interrupted | boolean |  |  |  |
| request_checksum | text |  |  |  |
| response_checksum | text |  |  |  |
| transform_rule_id | text |  |  |  |
| egress_protocol | text |  |  |  |
| client_request_id | text |  |  |  |

## request_logs_bodies

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y | now() |  |
| request_body | jsonb |  |  |  |
| outbound_body | jsonb |  |  |  |
| response_body | jsonb |  |  |  |

## request_logs_bodies_2026_07

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y | now() |  |
| request_body | jsonb |  |  |  |
| outbound_body | jsonb |  |  |  |
| response_body | jsonb |  |  |  |

## request_logs_bodies_2026_08

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y | now() |  |
| request_body | jsonb |  |  |  |
| outbound_body | jsonb |  |  |  |
| response_body | jsonb |  |  |  |

## request_logs_bodies_2026_09

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y | now() |  |
| request_body | jsonb |  |  |  |
| outbound_body | jsonb |  |  |  |
| response_body | jsonb |  |  |  |

## request_logs_bodies_hot

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y | now() |  |
| request_body | jsonb |  |  |  |
| outbound_body | jsonb |  |  |  |
| response_body | jsonb |  |  |  |

索引（3）：
- idx_request_logs_bodies_hot_request_id UNIQUE
- request_logs_bodies_hot_request_id_idx
- request_logs_bodies_hot_ts_idx

## request_logs_hot

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | nextval('public.request_logs_id_seq'::regclass) |  |
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y |  |  |
| tenant_id | text | Y |  |  |
| application_id | bigint |  |  |  |
| api_key_id | bigint |  |  |  |
| end_user_id | text |  |  |  |
| client_model | text |  |  |  |
| outbound_model | text |  |  |  |
| credential_id | bigint |  |  |  |
| provider_id | bigint |  |  |  |
| canonical_id | bigint |  |  |  |
| client_profile | text |  |  |  |
| request_mode | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| total_tokens | integer |  |  |  |
| cost_usd | numeric(14,8) |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean | Y |  |  |
| error_kind | text |  |  |  |
| search_text | text |  |  |  |
| cache_read_tokens | integer |  |  |  |
| cache_write_tokens | integer |  |  |  |
| identity_hash | text |  |  |  |
| virtual_client_id | text |  |  |  |
| virtual_ip | text |  |  |  |
| virtual_mac | text |  |  |  |
| affinity_hit | boolean |  |  |  |
| stream_first_chunk_ms | integer |  |  |  |
| stream_chunk_count | integer |  |  |  |
| stream_interrupted | boolean |  |  |  |
| stream_done_sent | boolean |  |  |  |
| request_checksum | text |  |  |  |
| response_checksum | text |  |  |  |
| transform_rule_id | text |  |  |  |
| egress_protocol | text |  |  |  |
| failure_stage | text |  |  |  |
| failure_detail_code | text |  |  |  |
| request_preview | text |  |  |  |
| transform_summary | text |  |  |  |
| response_preview | text |  |  |  |
| stream_done_received | boolean |  |  |  |
| request_body | jsonb |  |  |  |
| response_body | jsonb |  |  |  |
| cost_display | numeric(14,8) |  |  |  |
| cost_currency | text |  |  |  |
| usage_source | text | Y | 'llm'::text |  |
| gw_session_id | text |  |  |  |
| gw_task_id | text |  |  |  |
| request_status | text |  |  |  |
| api_key_prefix | text |  |  |  |
| owner_user | text |  |  |  |
| application_code | text |  |  |  |
| key_alias | text |  |  |  |
| api_key_owner_user | text |  |  |  |
| is_auto_request | boolean |  | false |  |
| task_type | text |  |  |  |
| auto_profile | text |  |  |  |
| auto_decision | jsonb |  |  |  |
| auto_confidence | numeric(4,3) |  |  |  |
| work_type | text |  |  |  |
| task_type_chosen | text |  |  |  |
| confidence_num | numeric(4,3) |  |  |  |
| model_chosen | text |  |  |  |
| strategy_used | text |  |  |  |
| credits_charged | bigint |  |  |  |
| parent_request_id | text |  |  |  |
| compression_reason | text |  |  |  |
| compression_strategy | text |  |  |  |
| compression_meta | jsonb |  |  |  |
| outbound_body | jsonb |  |  |  |
| outbound_msg_count | integer |  |  |  |
| outbound_token_est | integer |  |  |  |
| outbound_msg_hashes | jsonb |  |  |  |
| quality_flags | text[] | Y | '{}'::text[] |  |
| quality_fix_actions | jsonb | Y | '{}'::jsonb |  |
| quality_score | numeric(3,2) |  |  |  |
| upstream_finish_reason | text |  |  |  |
| tool_calls | jsonb |  |  |  |
| client_endpoint | text |  |  |  |
| client_timeout | boolean |  |  |  |
| stream_chunk_errors | integer |  |  |  |
| stream_chunks_sent | integer | Y | 0 |  |
| client_request_id | text |  |  |  |
| upstream_status_code | integer |  |  |  |
| test_col | text[] | Y | '{}'::text[] |  |
| test_tab_indent | text |  |  |  |
| provider_model | text |  |  |  |
| attachments | jsonb |  |  |  |
| has_attachments | boolean |  | false |  |
| attachment_count | integer |  | 0 |  |
| client_ip | inet |  |  |  |
| caller_id | text |  |  |  |
| session_correlation_id | text |  |  |  |
| compression_start_index | integer |  |  |  |
| compression_end_index | integer |  |  |  |
| client_forwarded_for | text |  |  |  |
| agent_name | character |  |  |  |
| agent_type | character |  |  |  |
| api_key_fingerprint | character |  |  |  |
| customer_id | bigint |  |  |  |
| upstream_endpoint | text |  |  |  |
| session_title | text |  |  |  |
| session_summary | text |  |  |  |
| task_id | character |  |  |  |
| task_title | text |  |  |  |
| protocol_conversion | boolean |  |  |  |
| ir_extensions | jsonb |  |  |  |
| sanitizer_mutations | jsonb |  |  |  |
| compression_ratio | double |  |  |  |
| cache_hit | boolean |  |  |  |
| cache_tokens_saved | integer |  |  |  |
| content_safety_score | jsonb |  |  |  |
| dlp_violations | jsonb |  |  |  |
| sensitive_keywords | text[] |  |  |  |
| vendor_metadata | jsonb |  |  |  |
| client_protocol | character |  |  |  |
| rate_limit_status | character |  |  |  |
| upstream_protocol | character |  |  |  |
| reasoning_tokens | integer |  |  |  |
| image_tokens | integer |  |  |  |
| audio_tokens | integer |  |  |  |
| video_tokens | integer |  |  |  |
| provider_tokens | integer |  |  |  |
| origin_stage | character varying(32) |  | NULL::character |  |
| origin_actor | character varying(255) |  | NULL::character |  |
| routing_attempts | jsonb |  |  |  |
| routing_summary | text |  |  |  |
| trace_events | jsonb |  |  |  |
| canonical_model | text |  |  |  |

索引（11）：
- idx_request_logs_hot_api_key_ts
- idx_request_logs_hot_canonical_model_ts
- idx_request_logs_hot_credential_model_ts
- idx_request_logs_hot_has_trace_events
- idx_request_logs_hot_multimodal_usage
- idx_request_logs_hot_request_id
- idx_request_logs_hot_routing_attempts
- idx_request_logs_hot_success_false_ts
- idx_request_logs_hot_success_true_ts
- idx_request_logs_hot_tenant_ts
- idx_request_logs_hot_ts

## request_stage_events

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| request_id | text | Y |  |  |
| tenant_id | text | Y |  |  |
| seq | integer | Y |  |  |
| stage | text | Y |  |  |
| stage_name | text |  |  |  |
| module | text |  |  |  |
| event_timestamp | timestamp with time zone | Y |  |  |
| duration_ms | integer |  |  |  |
| status | text | Y |  |  |
| error_message | text |  |  |  |
| http_status | integer |  |  |  |
| response_body | text |  |  |  |
| failure_hint | text |  |  |  |
| details | jsonb |  |  |  |
| snapshot | jsonb |  |  |  |
| redis_hit | boolean |  |  |  |
| redis_key | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

索引（5）：
- idx_stage_events_redis_miss
- idx_stage_events_request_id
- idx_stage_events_stage_status
- idx_stage_events_tenant_ts
- idx_stage_events_upstream_failure

## request_stats_dim_minute

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| bucket | timestamp with time zone | Y |  |  |
| tenant_id | text | Y | 'default'::text |  |
| dim_type | text | Y |  |  |
| dim_key | text | Y |  |  |
| requests | bigint | Y | 0 |  |
| success_count | bigint | Y | 0 |  |
| failure_count | bigint | Y | 0 |  |
| total_tokens | bigint | Y | 0 |  |
| credits_charged | bigint | Y | 0 |  |
| cost_usd | numeric(18,8) | Y | 0 |  |

索引（2）：
- idx_rsdm_bucket
- idx_rsdm_type_bucket

## request_stats_error_drill_minute

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| bucket | timestamp with time zone | Y |  |  |
| tenant_id | text | Y | 'default'::text |  |
| error_kind | text | Y |  |  |
| model_name | text | Y | ''::text |  |
| provider_id | bigint | Y | 0 |  |
| client_profile | text | Y | ''::text |  |
| requests | bigint | Y | 0 |  |

索引（1）：
- idx_rsedm_error_bucket

## request_stats_minute

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| bucket | timestamp with time zone | Y |  |  |
| tenant_id | text | Y | 'default'::text |  |
| provider_id | bigint | Y | 0 |  |
| canonical_id | bigint | Y | 0 |  |
| requests | bigint | Y | 0 |  |
| success_count | bigint | Y | 0 |  |
| failure_count | bigint | Y | 0 |  |
| prompt_tokens | bigint | Y | 0 |  |
| completion_tokens | bigint | Y | 0 |  |
| total_tokens | bigint | Y | 0 |  |
| credits_charged | bigint | Y | 0 |  |
| cost_usd | numeric(18,8) | Y | 0 |  |
| latency_ms_sum | bigint | Y | 0 |  |

索引（2）：
- idx_rsm_bucket
- idx_rsm_tenant_bucket

## request_stats_rollup_cursor

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | smallint | Y | 1 |  |
| last_ts | timestamp |  |  |  |
| last_request_id | text |  |  |  |
| updated_at | timestamp with time zone | Y | now() |  |

## request_wal

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | character varying(64) | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| gw_session_id | character |  |  |  |
| status | character varying(20) | Y | 'pending'::character |  |
| stage | smallint | Y | 0 |  |
| client_model | character |  |  |  |
| upstream_provider_id | bigint |  |  |  |
| upstream_credential_id | bigint |  |  |  |
| completion_tokens | integer |  |  |  |
| prompt_tokens | integer |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| completed_at | timestamp |  |  |  |
| upstream_request_at | timestamp |  |  |  |
| upstream_response_at | timestamp |  |  |  |
| error | text |  |  |  |
| compression_strategy | character |  |  |  |
| compression_meta | jsonb |  |  |  |

## request_wal_2026_07

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | character varying(64) | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| gw_session_id | character |  |  |  |
| status | character varying(20) | Y | 'pending'::character |  |
| stage | smallint | Y | 0 |  |
| client_model | character |  |  |  |
| upstream_provider_id | bigint |  |  |  |
| upstream_credential_id | bigint |  |  |  |
| completion_tokens | integer |  |  |  |
| prompt_tokens | integer |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| completed_at | timestamp |  |  |  |
| upstream_request_at | timestamp |  |  |  |
| upstream_response_at | timestamp |  |  |  |
| error | text |  |  |  |
| compression_strategy | character |  |  |  |
| compression_meta | jsonb |  |  |  |

索引（5）：
- request_wal_2026_07_gw_session_id_created_at_idx
- request_wal_2026_07_status_stage_idx
- request_wal_2026_07_status_stage_idx2
- request_wal_2026_07_tenant_id_created_at_idx
- request_wal_2026_07_tenant_id_created_at_idx2

## request_wal_2026_08

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | character varying(64) | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| gw_session_id | character |  |  |  |
| status | character varying(20) | Y | 'pending'::character |  |
| stage | smallint | Y | 0 |  |
| client_model | character |  |  |  |
| upstream_provider_id | bigint |  |  |  |
| upstream_credential_id | bigint |  |  |  |
| completion_tokens | integer |  |  |  |
| prompt_tokens | integer |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| completed_at | timestamp |  |  |  |
| upstream_request_at | timestamp |  |  |  |
| upstream_response_at | timestamp |  |  |  |
| error | text |  |  |  |
| compression_strategy | character |  |  |  |
| compression_meta | jsonb |  |  |  |

索引（3）：
- request_wal_2026_08_gw_session_id_created_at_idx
- request_wal_2026_08_status_stage_idx
- request_wal_2026_08_tenant_id_created_at_idx

## request_wal_archive

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | character varying(64) | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| gw_session_id | character |  |  |  |
| status | character varying(20) | Y | 'pending'::character |  |
| stage | smallint | Y | 0 |  |
| client_model | character |  |  |  |
| upstream_provider_id | bigint |  |  |  |
| upstream_credential_id | bigint |  |  |  |
| completion_tokens | integer |  |  |  |
| prompt_tokens | integer |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| completed_at | timestamp |  |  |  |
| upstream_request_at | timestamp |  |  |  |
| upstream_response_at | timestamp |  |  |  |
| error | text |  |  |  |
| compression_strategy | character |  |  |  |
| compression_meta | jsonb |  |  |  |

## request_wal_bodies

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | character varying(64) | Y |  |  |
| outbound_body | text |  |  |  |
| compression_meta | jsonb |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

## request_wal_hot

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | character varying(64) | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| gw_session_id | character |  |  |  |
| status | character varying(20) | Y | 'pending'::character |  |
| stage | smallint | Y | 0 |  |
| client_model | character |  |  |  |
| upstream_provider_id | bigint |  |  |  |
| upstream_credential_id | bigint |  |  |  |
| completion_tokens | integer |  |  |  |
| prompt_tokens | integer |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| completed_at | timestamp |  |  |  |
| upstream_request_at | timestamp |  |  |  |
| upstream_response_at | timestamp |  |  |  |
| error | text |  |  |  |
| compression_strategy | character |  |  |  |
| compression_meta | jsonb |  |  |  |

索引（5）：
- idx_request_wal_hot_created_at
- idx_request_wal_hot_tenant_created
- request_wal_hot_status_stage_idx
- request_wal_hot_tenant_id_created_at_idx
- udx_request_wal_hot_request_id UNIQUE

## response_format_anomalies

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| detected_at | timestamp with time zone | Y | now() |  |
| request_id | text | Y |  |  |
| provider_id | integer |  |  |  |
| provider_code | text |  |  |  |
| client_model | text |  |  |  |
| outbound_model | text |  |  |  |
| anomaly_type | text | Y |  |  |
| severity | text | Y | 'medium'::text |  |
| usage_source | text |  |  |  |
| expected_tokens | integer |  |  |  |
| actual_tokens | integer |  |  |  |
| content_size_bytes | integer |  |  |  |
| response_structure | jsonb |  |  |  |
| response_sample | text |  |  |  |
| resolved | boolean | Y | false |  |
| resolved_at | timestamp |  |  |  |
| resolution_notes | text |  |  |  |
| tenant_id | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

索引（6）：
- idx_response_format_anomalies_bridge
- idx_response_format_anomalies_detected_at
- idx_response_format_anomalies_provider
- idx_response_format_anomalies_request_id
- idx_response_format_anomalies_type
- idx_response_format_anomalies_unresolved

## route_decisions

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| request_id | text |  |  |  |
| ts | timestamp with time zone | Y | now() |  |
| tenant_id | text |  |  |  |
| api_key_id | bigint |  |  |  |
| canonical_id | bigint |  |  |  |
| selected_credential_id | bigint |  |  |  |
| candidates_json | jsonb |  |  |  |
| reason | text |  |  |  |
| sticky_hit | boolean |  |  |  |

## route_incident_events

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| incident_id | uuid | Y |  |  |
| event_type | text | Y |  |  |
| request_id | text |  |  |  |
| terminal_status | text |  |  |  |
| failure_kind | text |  |  |  |
| failure_stage | text |  |  |  |
| failure_streak | integer |  |  |  |
| recovery_streak | integer |  |  |  |
| evidence | jsonb | Y | '{}'::jsonb |  |
| actor | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

外键：
- (incident_id) → route_incidents(id)

索引（3）：
- idx_route_incident_events_incident_created
- idx_route_incident_events_type_created
- uq_route_incident_events_idem UNIQUE

## route_incidents

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | uuid | Y | gen_random_uuid() |  |
| tenant_id | text | Y |  |  |
| endpoint_protocol | text | Y |  |  |
| model | text | Y |  |  |
| provider_id | bigint |  |  |  |
| credential_id | bigint |  |  |  |
| state | text | Y |  |  |
| failure_streak | integer | Y | 0 |  |
| recovery_streak | integer | Y | 0 |  |
| first_failure_at | timestamp with time zone | Y |  |  |
| last_failure_at | timestamp |  |  |  |
| last_success_at | timestamp |  |  |  |
| recovered_at | timestamp |  |  |  |
| total_failures | bigint | Y | 0 |  |
| total_successes | bigint | Y | 0 |  |
| last_error_kind | text |  |  |  |
| last_failure_stage | text |  |  |  |
| resolution_source | text |  |  |  |
| resolved_by_user | text |  |  |  |
| resolved_reason | text |  |  |  |
| version | bigint | Y | 1 |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（3）：
- idx_route_incidents_state_updated
- idx_route_incidents_tenant_state
- uq_route_incidents_active_route UNIQUE

## routing_audit_log

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| ts | timestamp with time zone |  | now() |  |
| actor | text | Y |  |  |
| action | text | Y |  |  |
| target_type | text |  |  |  |
| target_id | bigint |  |  |  |
| before_json | jsonb |  |  |  |
| after_json | jsonb |  |  |  |
| incident_id | uuid |  |  |  |
| tenant_id | text |  |  |  |
| confirmation_token_hash | text |  |  |  |
| idempotency_key | text |  |  |  |
| request_payload | jsonb | Y | '{}'::jsonb |  |
| pre_snapshot | jsonb | Y | '{}'::jsonb |  |
| post_snapshot | jsonb | Y | '{}'::jsonb |  |
| response_payload | jsonb | Y | '{}'::jsonb |  |
| outcome | text |  |  |  |
| failure_reason | text |  |  |  |
| diagnostic_run_id | uuid |  |  |  |
| actor_ip_hash | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

索引（8）：
- idx_routing_audit_log_action
- idx_routing_audit_log_actor_created
- idx_routing_audit_log_idempotency UNIQUE
- idx_routing_audit_log_incident
- idx_routing_audit_log_incident_created
- idx_routing_audit_log_tenant_created
- idx_routing_audit_log_tenant_ts
- uq_routing_audit_log_idem UNIQUE

## routing_decision_log

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| ts | timestamp with time zone | Y | now() |  |
| request_id | uuid | Y |  |  |
| idempotency_key | text |  |  |  |
| tenant_id | text |  |  |  |
| api_key_id | bigint |  |  |  |
| model | text | Y |  |  |
| chosen_credential_id | bigint |  |  |  |
| chosen_provider_id | bigint |  |  |  |
| tier | smallint |  |  |  |
| candidates_tried | smallint |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean | Y |  |  |
| error_class | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| cost_usd | numeric(12,6) |  |  |  |
| request_bytes | integer |  |  |  |
| response_bytes | integer |  |  |  |
| client_model | text |  |  |  |
| resolved_raw_model | text |  |  |  |
| sticky_hit | boolean |  |  |  |
| client_profile | text |  |  |  |
| outbound_model | text |  |  |  |
| request_mode | text |  |  |  |
| identity_hash | text |  |  |  |
| transform_rule_id | text |  |  |  |
| egress_protocol | text |  |  |  |
| failure_stage | text |  |  |  |
| failure_detail_code | text |  |  |  |
| virtual_client_id | text |  |  |  |
| virtual_ip | text |  |  |  |
| virtual_mac | text |  |  |  |
| resolution_path | text |  |  |  |
| canonical_model | text |  |  |  |
| resolution_raw_models | jsonb |  |  |  |
| decision_trace | jsonb |  |  |  |

## routing_decision_log_2026_07

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| ts | timestamp with time zone | Y | now() |  |
| request_id | uuid | Y |  |  |
| idempotency_key | text |  |  |  |
| tenant_id | text |  |  |  |
| api_key_id | bigint |  |  |  |
| model | text | Y |  |  |
| chosen_credential_id | bigint |  |  |  |
| chosen_provider_id | bigint |  |  |  |
| tier | smallint |  |  |  |
| candidates_tried | smallint |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean | Y |  |  |
| error_class | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| cost_usd | numeric(12,6) |  |  |  |
| request_bytes | integer |  |  |  |
| response_bytes | integer |  |  |  |
| client_model | text |  |  |  |
| resolved_raw_model | text |  |  |  |
| sticky_hit | boolean |  |  |  |
| client_profile | text |  |  |  |
| outbound_model | text |  |  |  |
| request_mode | text |  |  |  |
| identity_hash | text |  |  |  |
| transform_rule_id | text |  |  |  |
| egress_protocol | text |  |  |  |
| failure_stage | text |  |  |  |
| failure_detail_code | text |  |  |  |
| virtual_client_id | text |  |  |  |
| virtual_ip | text |  |  |  |
| virtual_mac | text |  |  |  |
| resolution_path | text |  |  |  |
| canonical_model | text |  |  |  |
| resolution_raw_models | jsonb |  |  |  |
| decision_trace | jsonb |  |  |  |

索引（6）：
- routing_decision_log_2026_07_chosen_credential_id_ts_idx
- routing_decision_log_2026_07_model_ts_idx
- routing_decision_log_2026_07_request_id_idx
- routing_decision_log_2026_07_success_ts_idx
- routing_decision_log_2026_07_tenant_id_ts_idx
- routing_decision_log_2026_07_ts_idx

## routing_decision_log_2026_08

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| ts | timestamp with time zone | Y | now() |  |
| request_id | uuid | Y |  |  |
| idempotency_key | text |  |  |  |
| tenant_id | text |  |  |  |
| api_key_id | bigint |  |  |  |
| model | text | Y |  |  |
| chosen_credential_id | bigint |  |  |  |
| chosen_provider_id | bigint |  |  |  |
| tier | smallint |  |  |  |
| candidates_tried | smallint |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean | Y |  |  |
| error_class | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| cost_usd | numeric(12,6) |  |  |  |
| request_bytes | integer |  |  |  |
| response_bytes | integer |  |  |  |
| client_model | text |  |  |  |
| resolved_raw_model | text |  |  |  |
| sticky_hit | boolean |  |  |  |
| client_profile | text |  |  |  |
| outbound_model | text |  |  |  |
| request_mode | text |  |  |  |
| identity_hash | text |  |  |  |
| transform_rule_id | text |  |  |  |
| egress_protocol | text |  |  |  |
| failure_stage | text |  |  |  |
| failure_detail_code | text |  |  |  |
| virtual_client_id | text |  |  |  |
| virtual_ip | text |  |  |  |
| virtual_mac | text |  |  |  |
| resolution_path | text |  |  |  |
| canonical_model | text |  |  |  |
| resolution_raw_models | jsonb |  |  |  |
| decision_trace | jsonb |  |  |  |

索引（6）：
- routing_decision_log_2026_08_chosen_credential_id_ts_idx
- routing_decision_log_2026_08_model_ts_idx
- routing_decision_log_2026_08_request_id_idx
- routing_decision_log_2026_08_success_ts_idx
- routing_decision_log_2026_08_tenant_id_ts_idx
- routing_decision_log_2026_08_ts_idx

## routing_decision_log_archive

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| ts | timestamp with time zone | Y | now() |  |
| request_id | uuid | Y |  |  |
| idempotency_key | text |  |  |  |
| tenant_id | text |  |  |  |
| api_key_id | bigint |  |  |  |
| model | text | Y |  |  |
| chosen_credential_id | bigint |  |  |  |
| chosen_provider_id | bigint |  |  |  |
| tier | smallint |  |  |  |
| candidates_tried | smallint |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean | Y |  |  |
| error_class | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| cost_usd | numeric(12,6) |  |  |  |
| request_bytes | integer |  |  |  |
| response_bytes | integer |  |  |  |
| client_model | text |  |  |  |
| resolved_raw_model | text |  |  |  |
| sticky_hit | boolean |  |  |  |
| client_profile | text |  |  |  |
| outbound_model | text |  |  |  |
| request_mode | text |  |  |  |
| identity_hash | text |  |  |  |
| transform_rule_id | text |  |  |  |
| egress_protocol | text |  |  |  |
| failure_stage | text |  |  |  |
| failure_detail_code | text |  |  |  |
| virtual_client_id | text |  |  |  |
| virtual_ip | text |  |  |  |
| virtual_mac | text |  |  |  |
| resolution_path | text |  |  |  |
| canonical_model | text |  |  |  |
| resolution_raw_models | jsonb |  |  |  |
| decision_trace | jsonb |  |  |  |

## routing_decision_log_archive_2026_08

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| ts | timestamp with time zone | Y | now() |  |
| request_id | uuid | Y |  |  |
| idempotency_key | text |  |  |  |
| tenant_id | text |  |  |  |
| api_key_id | bigint |  |  |  |
| model | text | Y |  |  |
| chosen_credential_id | bigint |  |  |  |
| chosen_provider_id | bigint |  |  |  |
| tier | smallint |  |  |  |
| candidates_tried | smallint |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean | Y |  |  |
| error_class | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| cost_usd | numeric(12,6) |  |  |  |
| request_bytes | integer |  |  |  |
| response_bytes | integer |  |  |  |
| client_model | text |  |  |  |
| resolved_raw_model | text |  |  |  |
| sticky_hit | boolean |  |  |  |
| client_profile | text |  |  |  |
| outbound_model | text |  |  |  |
| request_mode | text |  |  |  |
| identity_hash | text |  |  |  |
| transform_rule_id | text |  |  |  |
| egress_protocol | text |  |  |  |
| failure_stage | text |  |  |  |
| failure_detail_code | text |  |  |  |
| virtual_client_id | text |  |  |  |
| virtual_ip | text |  |  |  |
| virtual_mac | text |  |  |  |
| resolution_path | text |  |  |  |
| canonical_model | text |  |  |  |
| resolution_raw_models | jsonb |  |  |  |
| decision_trace | jsonb |  |  |  |

## routing_decision_log_hot

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| ts | timestamp with time zone | Y | now() |  |
| request_id | uuid | Y |  |  |
| idempotency_key | text |  |  |  |
| tenant_id | text |  |  |  |
| api_key_id | bigint |  |  |  |
| model | text | Y |  |  |
| chosen_credential_id | bigint |  |  |  |
| chosen_provider_id | bigint |  |  |  |
| tier | smallint |  |  |  |
| candidates_tried | smallint |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean | Y |  |  |
| error_class | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| cost_usd | numeric(12,6) |  |  |  |
| request_bytes | integer |  |  |  |
| response_bytes | integer |  |  |  |
| client_model | text |  |  |  |
| resolved_raw_model | text |  |  |  |
| sticky_hit | boolean |  |  |  |
| client_profile | text |  |  |  |
| outbound_model | text |  |  |  |
| request_mode | text |  |  |  |
| identity_hash | text |  |  |  |
| transform_rule_id | text |  |  |  |
| egress_protocol | text |  |  |  |
| failure_stage | text |  |  |  |
| failure_detail_code | text |  |  |  |
| virtual_client_id | text |  |  |  |
| virtual_ip | text |  |  |  |
| virtual_mac | text |  |  |  |
| resolution_path | text |  |  |  |
| canonical_model | text |  |  |  |
| resolution_raw_models | jsonb |  |  |  |
| decision_trace | jsonb |  |  |  |

索引（10）：
- idx_routing_decision_log_hot_request_ts UNIQUE
- idx_routing_decision_log_hot_tenant_ts
- idx_routing_decision_log_hot_ts
- routing_decision_log_hot_chosen_credential_id_ts_idx
- routing_decision_log_hot_model_ts_idx
- routing_decision_log_hot_request_id_idx
- routing_decision_log_hot_request_id_ts_key UNIQUE
- routing_decision_log_hot_success_ts_idx
- routing_decision_log_hot_tenant_id_ts_idx
- routing_decision_log_hot_ts_idx

## routing_health_checks

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| check_id | text | Y |  |  |
| severity | text | Y | 'warning'::text |  |
| entity_type | text | Y |  |  |
| entity_id | bigint |  |  |  |
| entity_name | text | Y | ''::text |  |
| detail | text | Y | ''::text |  |
| fix_sql | text | Y | ''::text |  |
| status | text | Y | 'open'::text |  |
| auto_fixed_at | timestamp |  |  |  |
| auto_fix_result | text |  |  |  |
| dismissed_at | timestamp |  |  |  |
| dismissed_by | text |  |  |  |
| dismissed_reason | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（3）：
- idx_rhc_check_id
- idx_rhc_severity
- idx_rhc_status

## routing_overrides

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| task_type | text | Y |  |  |
| profile | text | Y | ''::text |  |
| mode | text | Y |  |  |
| model_chosen | text |  |  |  |
| reason | text | Y | ''::text |  |
| created_by | text |  |  |  |
| expires_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（3）：
- idx_routing_overrides_expires
- idx_routing_overrides_task_profile
- idx_routing_overrides_unique UNIQUE

## routing_overrides_audit

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| ts | timestamp with time zone | Y | now() |  |
| action | text | Y |  |  |
| override_id | bigint |  |  |  |
| task_type | text |  |  |  |
| profile | text |  |  |  |
| mode | text |  |  |  |
| model_chosen | text |  |  |  |
| reason | text |  |  |  |
| expires_at | timestamp |  |  |  |
| old_expires_at | timestamp |  |  |  |
| actor | text |  |  |  |

索引（3）：
- idx_routing_overrides_audit_actor_ts
- idx_routing_overrides_audit_override_ts
- idx_routing_overrides_audit_ts

## routing_policy

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | smallint | Y | 1 |  |
| tenant_id | text | Y | 'default'::text |  |
| weights_json | jsonb | Y | '{}'::jsonb |  |
| sticky_ttl_seconds | integer | Y | 1800 |  |
| local_bonus | numeric(4,3) | Y | 0.000 |  |
| notes | text |  |  |  |
| updated_at | timestamp with time zone | Y | now() |  |
| algorithm_version | smallint |  | 2 |  |
| retry_per_credential | smallint |  | 1 |  |
| tier_fallback_max | smallint |  | 4 |  |
| slot_soft_limit_ratio | numeric(3,2) |  | 1.00 |  |
| slot_hard_limit_ratio | numeric(3,2) |  | 1.50 |  |
| slot_wait_max_ms | smallint |  | 200 |  |
| circuit_open_seconds | integer |  | 300 |  |
| circuit_failure_threshold | smallint |  | 5 |  |
| circuit_max_open_seconds | integer |  | 1800 |  |
| featured_models | text[] |  | ARRAY['gpt-4o'::text |  |
| transient_fail_threshold | integer | Y | 2 |  |
| stats_window_minutes | integer |  | 10 |  |
| stats_update_interval_seconds | integer |  | 60 |  |
| scoring_weights_json | jsonb |  | '{"price": |  |

## runtime_alert_events

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| rule_key | text | Y |  |  |
| instance_id | text | Y |  |  |
| severity | text | Y |  |  |
| title | text | Y |  |  |
| message | text | Y |  |  |
| status | text | Y | 'triggered'::text |  |
| metric_value | double |  |  |  |
| detected_at | timestamp with time zone | Y | now() |  |
| acked_at | timestamp |  |  |  |
| acked_by | text |  |  |  |
| resolved_at | timestamp |  |  |  |
| resolved_by | text |  |  |  |
| suppressed_until | timestamp |  |  |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（3）：
- idx_rae_detected_at
- idx_rae_instance_status
- idx_rae_rule_open

## runtime_metrics

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| instance_id | text | Y |  |  |
| license_id | bigint |  |  |  |
| timestamp | timestamp with time zone | Y | now() |  |
| cpu_usage_pct | real |  |  |  |
| mem_used_mb | bigint |  |  |  |
| mem_total_mb | bigint |  |  |  |
| disk_used_gb | bigint |  |  |  |
| disk_total_gb | bigint |  |  |  |
| db_size_mb | bigint |  |  |  |
| uptime_secs | bigint |  |  |  |
| current_concurrency | integer |  |  |  |
| last_5min_tps | real |  |  |  |
| last_5min_p50_ms | real |  |  |  |
| last_5min_p99_ms | real |  |  |  |
| last_5min_success_pct | real |  |  |  |
| model_usage | jsonb |  |  |  |
| tenant_count | integer |  |  |  |

外键：
- (license_id) → licenses(id)

索引（7）：
- idx_rt_instance_time
- idx_rt_time
- idx_runtime_metrics_cpu_high
- idx_runtime_metrics_cpu_usage
- idx_runtime_metrics_instance
- idx_runtime_metrics_mem_usage
- idx_runtime_metrics_timestamp

## runtime_telemetry_consent_events

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| hardware_hash | text | Y |  |  |
| license_id | bigint | Y |  |  |
| enabled | boolean | Y |  |  |
| agreement_version | text | Y |  |  |
| operator_user_id | bigint | Y |  |  |
| source | text | Y |  |  |
| occurred_at | timestamp with time zone | Y |  |  |

外键：
- (license_id) → licenses(id)

索引（1）：
- idx_runtime_telemetry_consent_events_hardware_time

## runtime_telemetry_preferences

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| hardware_hash | text | Y |  |  |
| license_id | bigint | Y |  |  |
| enabled | boolean | Y | false |  |
| agreement_version | text | Y |  |  |
| updated_at | timestamp with time zone | Y | now() |  |
| disabled_at | timestamp |  |  |  |

外键：
- (license_id) → licenses(id)

索引（1）：
- idx_runtime_telemetry_preferences_license

## schema_migration_audit

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| migration_id | text | Y |  |  |
| applied_at | timestamp with time zone | Y | now() |  |
| row_count | bigint | Y | 0 |  |
| note | text | Y | ''::text |  |

## schema_migrations

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| version | text | Y |  |  |
| description | text |  |  |  |
| applied_at | timestamp with time zone |  | now() |  |

## schema_migrations_backup_20260722

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| version | text |  |  |  |
| description | text |  |  |  |
| applied_at | timestamp |  |  |  |

## security_audit_log

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| ts | timestamp with time zone | Y | now() |  |
| event_kind | text | Y |  |  |
| api_key_id | bigint |  |  |  |
| internal_service_id | text |  |  |  |
| actor | text |  |  |  |
| tenant_id | text |  |  |  |
| remote_ip | inet |  |  |  |
| detail_json | jsonb |  |  |  |

## security_detector_config

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| tenant_id | text |  |  |  |
| config_name | text | Y | 'default'::text |  |
| description | text |  |  |  |
| sensitive_words | jsonb | Y | '[]'::jsonb |  |
| injection_patterns | jsonb | Y | '[]'::jsonb |  |
| pii_patterns | jsonb | Y | '[]'::jsonb |  |
| jailbreak_patterns | jsonb | Y | '[]'::jsonb |  |
| max_content_len | integer | Y | 50000 |  |
| score_threshold_log | integer | Y | 3 |  |
| score_threshold_warn | integer | Y | 5 |  |
| score_threshold_approval | integer | Y | 8 |  |
| score_threshold_block | integer | Y | 10 |  |
| severity_threshold_approval | integer | Y | 8 |  |
| audit_enabled | boolean | Y | true |  |
| audit_sampling_rate | double precision | Y | 1.0 |  |
| auto_approval_whitelist | jsonb |  | '[]'::jsonb |  |
| enabled | boolean | Y | true |  |
| version | integer | Y | 1 |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（2）：
- idx_security_config_tenant
- idx_security_config_version

## self_check_round_results

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| run_id | bigint | Y |  |  |
| round_index | integer | Y |  |  |
| is_ping | boolean | Y | false |  |
| is_tool_call | boolean | Y | false |  |
| latency_ms | integer | Y | 0 |  |
| prompt_tokens | integer | Y | 0 |  |
| completion_tokens | integer | Y | 0 |  |
| total_tokens | integer | Y | 0 |  |
| success | boolean | Y | false |  |
| http_code | integer |  |  |  |
| error_message | text |  |  |  |
| request_body | text |  |  |  |
| response_preview | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

外键：
- (run_id) → self_check_runs(id)

索引（1）：
- idx_self_check_rounds_run

## self_check_runs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| model_name | text | Y |  |  |
| started_at | timestamp with time zone | Y | now() |  |
| completed_at | timestamp |  |  |  |
| duration_ms | integer | Y | 0 |  |
| status | text | Y | 'running'::text |  |
| rounds_total | integer | Y | 3 |  |
| rounds_success | integer | Y | 0 |  |
| had_tool_call | boolean | Y | false |  |
| total_tokens | integer | Y | 0 |  |
| avg_latency_ms | integer | Y | 0 |  |
| error_type | text |  |  |  |
| error_detail | text |  |  |  |
| upstream_tested | boolean | Y | false |  |
| upstream_result | text |  |  |  |
| upstream_latency_ms | integer |  |  |  |
| upstream_error | text |  |  |  |
| tenant_id | text | Y | 'default'::text |  |
| created_at | timestamp with time zone | Y | now() |  |
| selection_strategy | text |  | 'most_used'::text |  |
| attempted_models | jsonb |  | '[]'::jsonb |  |

索引（4）：
- idx_self_check_runs_model
- idx_self_check_runs_model_started
- idx_self_check_runs_started
- idx_self_check_runs_status

## self_check_settings

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y | 1 |  |
| enabled | boolean | Y | true |  |
| normal_interval_seconds | integer | Y | 60 |  |
| fault_interval_seconds | integer | Y | 30 |  |
| model_source | text | Y | 'both'::text |  |
| max_models | integer | Y | 10 |  |
| max_tokens_per_run | integer | Y | 100000 |  |
| featured_model_ids | jsonb | Y | '[]'::jsonb |  |
| updated_at | timestamp with time zone | Y | now() |  |
| updated_by | text |  |  |  |
| monitor_concurrency | integer | Y | 5 |  |

## session_audit_records

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| session_id | text | Y |  |  |
| tenant_id | text | Y |  |  |
| request_id | text | Y |  |  |
| client_ip | text |  |  |  |
| client_user_agent | text |  |  |  |
| client_model | text |  |  |  |
| content_summary | text |  |  |  |
| content_title | text |  |  |  |
| content_hash | text |  |  |  |
| intent_type | text |  |  |  |
| intent_score | double |  |  |  |
| intent_reason | text |  |  |  |
| security_score | integer |  |  |  |
| danger_score | integer |  |  |  |
| trust_score | integer |  |  |  |
| sensitive_score | integer |  |  |  |
| detect_score | integer | Y | 0 |  |
| detect_decision | text | Y | 'pass'::text |  |
| threats | jsonb | Y | '[]'::jsonb |  |
| sensitive_words | jsonb | Y | '[]'::jsonb |  |
| status | text | Y | 'pass'::text |  |
| approval_status | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（3）：
- idx_session_audit_records_session
- idx_session_audit_records_status
- idx_session_audit_records_tenant_created

## session_bodies

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| session_id | text | Y |  |  |
| turn_no | integer | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| request_id | text | Y |  |  |
| request_delta | jsonb |  |  |  |
| response_delta | jsonb |  |  |  |
| outbound_body | jsonb |  |  |  |
| request_attachments | jsonb |  | '[]'::jsonb |  |
| response_attachments | jsonb |  | '[]'::jsonb |  |
| ts | timestamp with time zone | Y | now() |  |
| partition_date | date | Y | CURRENT_DATE |  |

## session_bodies_2026_07

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | nextval('public.session_bodies_id_seq'::regclass) |  |
| session_id | text | Y |  |  |
| turn_no | integer | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| request_id | text | Y |  |  |
| request_delta | jsonb |  |  |  |
| response_delta | jsonb |  |  |  |
| outbound_body | jsonb |  |  |  |
| request_attachments | jsonb |  | '[]'::jsonb |  |
| response_attachments | jsonb |  | '[]'::jsonb |  |
| ts | timestamp with time zone | Y | now() |  |
| partition_date | date | Y | CURRENT_DATE |  |

索引（2）：
- session_bodies_2026_07_request_id_idx
- session_bodies_2026_07_session_id_turn_no_idx

## session_bodies_2026_08

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | nextval('public.session_bodies_id_seq'::regclass) |  |
| session_id | text | Y |  |  |
| turn_no | integer | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| request_id | text | Y |  |  |
| request_delta | jsonb |  |  |  |
| response_delta | jsonb |  |  |  |
| outbound_body | jsonb |  |  |  |
| request_attachments | jsonb |  | '[]'::jsonb |  |
| response_attachments | jsonb |  | '[]'::jsonb |  |
| ts | timestamp with time zone | Y | now() |  |
| partition_date | date | Y | CURRENT_DATE |  |

索引（2）：
- session_bodies_2026_08_request_id_idx
- session_bodies_2026_08_session_id_turn_no_idx

## session_intent_evolution

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| session_id | text | Y |  |  |
| tenant_id | text | Y |  |  |
| request_id | text | Y |  |  |
| turn_number | integer | Y |  |  |
| intent_candidates | jsonb | Y | '[]'::jsonb |  |
| primary_intent | text | Y |  |  |
| primary_confidence | double precision | Y |  |  |
| previous_primary_intent | text |  |  |  |
| intent_drift_score | double |  |  |  |
| is_intent_changed | boolean |  | false |  |
| classifier_version | text | Y | 'v2_pattern'::text |  |
| classification_latency_ms | integer |  |  |  |
| user_content | text |  |  |  |
| user_content_hash | text |  |  |  |
| context_length | integer |  | 0 |  |
| has_images | boolean |  | false |  |
| tool_count | integer |  | 0 |  |
| classified_at | timestamp with time zone | Y | now() |  |

索引（5）：
- idx_session_intent_changed
- idx_session_intent_content_hash
- idx_session_intent_primary
- idx_session_intent_session
- idx_session_intent_tenant

## session_last_requests

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| session_id | character varying(255) | Y |  |  |
| last_request_id | bigint | Y |  |  |
| last_request_status | character varying(50) | Y |  |  |
| last_request_user_message | text |  |  |  |
| last_response_cached | text |  |  |  |
| last_response_chunks | integer |  | 0 |  |
| last_model | character |  |  |  |
| last_provider_id | integer |  |  |  |
| last_latency_ms | integer |  |  |  |
| created_at | timestamp with time zone |  | now() |  |
| updated_at | timestamp with time zone |  | now() |  |
| expires_at | timestamp with time zone |  | (now() |  |

索引（2）：
- idx_session_last_requests_expires
- idx_session_last_requests_status

## session_memora_extraction_log

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| task_id | text | Y |  |  |
| extracted_at | timestamp with time zone | Y | now() |  |
| written | integer | Y | 0 |  |
| skipped_noise | integer | Y | 0 |  |
| skipped_duplicate | integer | Y | 0 |  |
| status | text | Y | 'ok'::text |  |
| detail | jsonb |  |  |  |

索引（1）：
- idx_session_memora_extraction_at

## session_module_executions

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| execution_id | bigint | Y |  |  |
| gw_session_id | character varying(128) | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| module_name | character varying(100) | Y |  |  |
| module_version | character |  |  |  |
| request_id | character |  |  |  |
| batch_key | character varying(255) |  | ''::character |  |
| status | character varying(20) | Y |  |  |
| started_at | timestamp with time zone | Y |  |  |
| completed_at | timestamp |  |  |  |
| duration_ms | integer |  |  |  |
| result_summary | jsonb |  |  |  |
| result_detail | jsonb |  |  |  |
| error_message | text |  |  |  |
| cache_key | character varying(255) | Y |  |  |
| ttl_seconds | integer | Y | 3600 |  |
| expires_at | timestamp with time zone | Y |  |  |
| created_at | timestamp with time zone | Y |  |  |
| updated_at | timestamp with time zone | Y |  |  |

## session_module_executions_2026_07

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| execution_id | bigint | Y |  |  |
| gw_session_id | character varying(128) | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| module_name | character varying(100) | Y |  |  |
| module_version | character |  |  |  |
| request_id | character |  |  |  |
| batch_key | character varying(255) |  | ''::character |  |
| status | character varying(20) | Y |  |  |
| started_at | timestamp with time zone | Y |  |  |
| completed_at | timestamp |  |  |  |
| duration_ms | integer |  |  |  |
| result_summary | jsonb |  |  |  |
| result_detail | jsonb |  |  |  |
| error_message | text |  |  |  |
| cache_key | character varying(255) | Y |  |  |
| ttl_seconds | integer | Y | 3600 |  |
| expires_at | timestamp with time zone | Y |  |  |
| created_at | timestamp with time zone | Y |  |  |
| updated_at | timestamp with time zone | Y |  |  |

索引（2）：
- idx_session_module_executions_2026_07_session
- idx_session_module_executions_2026_07_tenant

## session_module_executions_2026_08

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| execution_id | bigint | Y |  |  |
| gw_session_id | character varying(128) | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| module_name | character varying(100) | Y |  |  |
| module_version | character |  |  |  |
| request_id | character |  |  |  |
| batch_key | character varying(255) |  | ''::character |  |
| status | character varying(20) | Y |  |  |
| started_at | timestamp with time zone | Y |  |  |
| completed_at | timestamp |  |  |  |
| duration_ms | integer |  |  |  |
| result_summary | jsonb |  |  |  |
| result_detail | jsonb |  |  |  |
| error_message | text |  |  |  |
| cache_key | character varying(255) | Y |  |  |
| ttl_seconds | integer | Y | 3600 |  |
| expires_at | timestamp with time zone | Y |  |  |
| created_at | timestamp with time zone | Y |  |  |
| updated_at | timestamp with time zone | Y |  |  |

索引（2）：
- idx_session_module_executions_2026_08_session
- idx_session_module_executions_2026_08_tenant

## session_module_executions_hot

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| execution_id | bigint | Y |  |  |
| gw_session_id | character varying(128) | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| module_name | character varying(100) | Y |  |  |
| module_version | character |  |  |  |
| request_id | character |  |  |  |
| batch_key | character varying(255) |  | ''::character |  |
| status | character varying(20) | Y | 'running'::character |  |
| started_at | timestamp with time zone | Y | now() |  |
| completed_at | timestamp |  |  |  |
| duration_ms | integer |  |  |  |
| result_summary | jsonb |  |  |  |
| result_detail | jsonb |  |  |  |
| error_message | text |  |  |  |
| cache_key | character varying(255) | Y |  |  |
| ttl_seconds | integer | Y | 3600 |  |
| expires_at | timestamp with time zone | Y |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（5）：
- idx_sme_hot_cleanup
- idx_sme_hot_lookup
- idx_sme_hot_module_stats
- idx_sme_hot_status
- idx_sme_hot_tenant_time

## session_summaries

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| session_key | character varying(255) | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| first_request_at | timestamp with time zone | Y |  |  |
| last_request_at | timestamp with time zone | Y |  |  |
| duration_seconds | integer |  |  |  |
| request_count | integer | Y | 0 |  |
| success_count | integer | Y | 0 |  |
| error_count | integer | Y | 0 |  |
| total_cost_usd | numeric(12,6) | Y | 0 |  |
| input_cost_usd | numeric(12,6) | Y | 0 |  |
| output_cost_usd | numeric(12,6) | Y | 0 |  |
| total_prompt_tokens | bigint | Y | 0 |  |
| total_completion_tokens | bigint | Y | 0 |  |
| total_tokens | bigint |  |  |  |
| avg_latency_ms | integer | Y | 0 |  |
| min_latency_ms | integer |  |  |  |
| max_latency_ms | integer |  |  |  |
| models_used | text[] | Y | '{}'::text[] |  |
| primary_model | character |  |  |  |
| model_switch_count | integer | Y | 0 |  |
| title | character |  |  |  |
| summary | text |  |  |  |
| key_topics | text[] |  |  |  |
| user_intent | character |  |  |  |
| quality_score | integer |  |  |  |
| compliance_status | character varying(20) |  | 'compliant'::character |  |
| compliance_issues_count | integer | Y | 0 |  |
| prompt_injection_detected | boolean |  | false |  |
| pii_detected | boolean |  | false |  |
| toxic_output_detected | boolean |  | false |  |
| work_types | text[] |  |  |  |
| providers | text[] |  |  |  |
| client_models | text[] |  |  |  |
| last_summarized_at | timestamp |  |  |  |
| summary_version | integer |  | 1 |  |
| created_at | timestamp with time zone |  | now() |  |
| updated_at | timestamp with time zone |  | now() |  |
| handoff_count | integer |  |  |  |
| last_handoff_at | timestamp |  |  |  |
| health_score | integer |  |  |  |
| health_grade | character |  |  |  |
| range | character |  |  |  |
| last_health_at | timestamp |  |  |  |
| tokens_at_trigger | bigint | Y | 0 |  |
| messages_at_trigger | integer | Y | 0 |  |
| last_trigger_reason | character |  |  |  |
| last_trigger_at | timestamp |  |  |  |
| parent_session_key | character varying(255) |  | ''::character |  |
| handoff_reason | character varying(64) |  | ''::character |  |

外键：
- (tenant_id) → tenants(code)

索引（9）：
- idx_session_summaries_compliance
- idx_session_summaries_cost
- idx_session_summaries_handoff
- idx_session_summaries_intent
- idx_session_summaries_models
- idx_session_summaries_quality
- idx_session_summaries_parent
- idx_session_summaries_tenant_time
- idx_session_summaries_topics

## session_titles

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| task_id | text | Y |  |  |
| scoped_session_id | text | Y | ''::text |  |
| title | text | Y |  |  |
| generated_at | timestamp with time zone | Y | now() |  |
| model | text |  |  |  |
| api_key_id | integer |  |  |  |

索引（1）：
- idx_session_titles_generated_at

## session_turn_logs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| session_id | text | Y |  |  |
| turn_no | integer | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| request_id | text | Y |  |  |
| stage | text | Y |  |  |
| stage_status | text | Y |  |  |
| event_data | jsonb | Y | '{}'::jsonb |  |
| error_message | text |  |  |  |
| started_at | timestamp with time zone | Y | now() |  |
| completed_at | timestamp |  |  |  |
| latency_ms | integer |  |  |  |
| expires_at | timestamp with time zone | Y | (now() |  |

索引（3）：
- idx_session_turn_logs_expires
- idx_session_turn_logs_request
- idx_session_turn_logs_session

## session_turn_snapshots

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| gw_session_id | character varying(128) | Y |  |  |
| turn_no | integer | Y |  |  |
| request_id | text | Y |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| expires_at | timestamp with time zone | Y |  |  |
| original_send | jsonb |  |  |  |
| original_receive | jsonb |  |  |  |
| compressed_send | jsonb |  |  |  |
| compressed_receive | jsonb |  |  |  |
| secured_send | jsonb |  |  |  |
| secured_receive | jsonb |  |  |  |
| original_send_ref | character |  |  |  |
| original_receive_ref | character |  |  |  |
| compressed_send_ref | character |  |  |  |
| compressed_receive_ref | character |  |  |  |
| secured_send_ref | character |  |  |  |
| secured_receive_ref | character |  |  |  |
| compression_strategy | text |  |  |  |
| compression_meta | jsonb | Y | '{}'::jsonb |  |
| security_tags | text[] | Y | '{}'::text[] |  |
| compressed_range_start | integer |  |  |  |
| compressed_range_end | integer |  |  |  |
| summary_marker | text |  |  |  |
| token_original | integer | Y | 0 |  |
| token_compressed | integer | Y | 0 |  |
| token_secured | integer | Y | 0 |  |
| stream_completed | boolean | Y | true |  |

索引（2）：
- idx_session_turn_snapshots_expiry
- idx_session_turn_snapshots_session

## session_turns

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| session_id | text | Y |  |  |
| turn_no | integer | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| request_id | text | Y |  |  |
| project_id | text |  |  |  |
| namespace | text |  |  |  |
| parent_request_id | text |  |  |  |
| task_type | text |  |  |  |
| ts | timestamp with time zone | Y | now() |  |
| submit_mode | text | Y | 'full'::text |  |
| compression_applied | boolean |  | false |  |
| compression_strategy | text |  |  |  |
| compression_meta | jsonb |  | '{}'::jsonb |  |
| compression_tokens_saved | integer |  |  |  |
| injection_verdict | text |  | 'skip'::text |  |
| output_verdict | text |  | 'skip'::text |  |
| model | text |  |  |  |
| provider | text |  |  |  |
| credential_id | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| cache_read_tokens | integer |  |  |  |
| cache_write_tokens | integer |  |  |  |
| cost_usd | numeric(12,6) |  |  |  |
| latency_ms | integer |  |  |  |
| status_code | integer |  |  |  |
| success | boolean |  |  |  |
| error_kind | text |  |  |  |
| source_kind | text | Y | 'live'::text |  |
| quality | text | Y | 'verified'::text |  |
| partition_date | date | Y | CURRENT_DATE |  |
| attachment_count | integer |  | 0 |  |
| attachment_total_bytes | bigint |  | 0 |  |
| multimodal_types | text[] |  | '{}'::text[] |  |
| attempt_no | integer | Y | 0 |  |
| tools | jsonb | Y | '[]'::jsonb |  |
| title | text |  |  |  |
| summary | text |  |  |  |
| digest | jsonb |  |  |  |
| aggregate_applied_at | timestamp |  |  |  |
| t0_arrived_at | timestamp |  |  |  |
| t1_total_enqueued_at | timestamp |  |  |  |
| t2_total_dequeued_at | timestamp |  |  |  |
| t3_model_enqueued_at | timestamp |  |  |  |
| t4_model_dequeued_at | timestamp |  |  |  |
| t5_cred_enqueued_at | timestamp |  |  |  |
| t6_cred_dequeued_at | timestamp |  |  |  |
| t7_forward_start_at | timestamp |  |  |  |
| t8_response_start_at | timestamp |  |  |  |
| t9_response_end_at | timestamp |  |  |  |

索引（4）：
- idx_session_turns_tenant_project
- idx_session_turns_tenant_namespace
- idx_session_turns_tenant_parent_request
- idx_session_turns_tenant_task_type

## session_turns_2026_07

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | nextval('public.session_turns_id_seq'::regclass) |  |
| session_id | text | Y |  |  |
| turn_no | integer | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| request_id | text | Y |  |  |
| project_id | text |  |  |  |
| namespace | text |  |  |  |
| parent_request_id | text |  |  |  |
| task_type | text |  |  |  |
| ts | timestamp with time zone | Y | now() |  |
| submit_mode | text | Y | 'full'::text |  |
| compression_applied | boolean |  | false |  |
| compression_strategy | text |  |  |  |
| compression_meta | jsonb |  | '{}'::jsonb |  |
| compression_tokens_saved | integer |  |  |  |
| injection_verdict | text |  | 'skip'::text |  |
| output_verdict | text |  | 'skip'::text |  |
| model | text |  |  |  |
| provider | text |  |  |  |
| credential_id | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| cache_read_tokens | integer |  |  |  |
| cache_write_tokens | integer |  |  |  |
| cost_usd | numeric(12,6) |  |  |  |
| latency_ms | integer |  |  |  |
| status_code | integer |  |  |  |
| success | boolean |  |  |  |
| error_kind | text |  |  |  |
| source_kind | text | Y | 'live'::text |  |
| quality | text | Y | 'verified'::text |  |
| partition_date | date | Y | CURRENT_DATE |  |
| attachment_count | integer |  | 0 |  |
| attachment_total_bytes | bigint |  | 0 |  |
| multimodal_types | text[] |  | '{}'::text[] |  |
| attempt_no | integer | Y | 0 |  |
| tools | jsonb | Y | '[]'::jsonb |  |
| title | text |  |  |  |
| summary | text |  |  |  |
| digest | jsonb |  |  |  |
| aggregate_applied_at | timestamp |  |  |  |
| t0_arrived_at | timestamp |  |  |  |
| t1_total_enqueued_at | timestamp |  |  |  |
| t2_total_dequeued_at | timestamp |  |  |  |
| t3_model_enqueued_at | timestamp |  |  |  |
| t4_model_dequeued_at | timestamp |  |  |  |
| t5_cred_enqueued_at | timestamp |  |  |  |
| t6_cred_dequeued_at | timestamp |  |  |  |
| t7_forward_start_at | timestamp |  |  |  |
| t8_response_start_at | timestamp |  |  |  |
| t9_response_end_at | timestamp |  |  |  |

索引（3）：
- session_turns_2026_07_request_id_idx
- session_turns_2026_07_session_id_turn_no_idx
- session_turns_2026_07_tenant_id_ts_idx

## session_turns_2026_08

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | nextval('public.session_turns_id_seq'::regclass) |  |
| session_id | text | Y |  |  |
| turn_no | integer | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| request_id | text | Y |  |  |
| project_id | text |  |  |  |
| namespace | text |  |  |  |
| parent_request_id | text |  |  |  |
| task_type | text |  |  |  |
| ts | timestamp with time zone | Y | now() |  |
| submit_mode | text | Y | 'full'::text |  |
| compression_applied | boolean |  | false |  |
| compression_strategy | text |  |  |  |
| compression_meta | jsonb |  | '{}'::jsonb |  |
| compression_tokens_saved | integer |  |  |  |
| injection_verdict | text |  | 'skip'::text |  |
| output_verdict | text |  | 'skip'::text |  |
| model | text |  |  |  |
| provider | text |  |  |  |
| credential_id | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| cache_read_tokens | integer |  |  |  |
| cache_write_tokens | integer |  |  |  |
| cost_usd | numeric(12,6) |  |  |  |
| latency_ms | integer |  |  |  |
| status_code | integer |  |  |  |
| success | boolean |  |  |  |
| error_kind | text |  |  |  |
| source_kind | text | Y | 'live'::text |  |
| quality | text | Y | 'verified'::text |  |
| partition_date | date | Y | CURRENT_DATE |  |
| attachment_count | integer |  | 0 |  |
| attachment_total_bytes | bigint |  | 0 |  |
| multimodal_types | text[] |  | '{}'::text[] |  |
| attempt_no | integer | Y | 0 |  |
| tools | jsonb | Y | '[]'::jsonb |  |
| title | text |  |  |  |
| summary | text |  |  |  |
| digest | jsonb |  |  |  |
| aggregate_applied_at | timestamp |  |  |  |
| t0_arrived_at | timestamp |  |  |  |
| t1_total_enqueued_at | timestamp |  |  |  |
| t2_total_dequeued_at | timestamp |  |  |  |
| t3_model_enqueued_at | timestamp |  |  |  |
| t4_model_dequeued_at | timestamp |  |  |  |
| t5_cred_enqueued_at | timestamp |  |  |  |
| t6_cred_dequeued_at | timestamp |  |  |  |
| t7_forward_start_at | timestamp |  |  |  |
| t8_response_start_at | timestamp |  |  |  |
| t9_response_end_at | timestamp |  |  |  |

索引（3）：
- session_turns_2026_08_request_id_idx
- session_turns_2026_08_session_id_turn_no_idx
- session_turns_2026_08_tenant_id_ts_idx

## sessions

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| session_id | text | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| closed_at | timestamp |  |  |  |
| status | text | Y | 'active'::text |  |
| total_turns | integer | Y | 0 |  |
| total_tokens | integer | Y | 0 |  |
| total_cost_usd | numeric(12,6) | Y | 0 |  |
| last_turn_no | integer |  |  |  |
| last_request_summary | text |  |  |  |
| last_response_summary | text |  |  |  |
| last_model | text |  |  |  |
| last_provider | text |  |  |  |
| task_type | text |  |  |  |
| client_type | text |  |  |  |
| topic | text |  |  |  |
| intent | text |  |  |  |
| primary_request_id | text |  |  |  |
| turn_logs_summary | jsonb |  |  |  |
| partition_date | date | Y | CURRENT_DATE |  |

## sessions_2026_07

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | nextval('public.sessions_id_seq'::regclass) |  |
| session_id | text | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| closed_at | timestamp |  |  |  |
| status | text | Y | 'active'::text |  |
| total_turns | integer | Y | 0 |  |
| total_tokens | integer | Y | 0 |  |
| total_cost_usd | numeric(12,6) | Y | 0 |  |
| last_turn_no | integer |  |  |  |
| last_request_summary | text |  |  |  |
| last_response_summary | text |  |  |  |
| last_model | text |  |  |  |
| last_provider | text |  |  |  |
| task_type | text |  |  |  |
| client_type | text |  |  |  |
| topic | text |  |  |  |
| intent | text |  |  |  |
| primary_request_id | text |  |  |  |
| turn_logs_summary | jsonb |  |  |  |
| partition_date | date | Y | CURRENT_DATE |  |

索引（4）：
- sessions_2026_07_primary_request_id_idx
- sessions_2026_07_session_id_idx
- sessions_2026_07_status_updated_at_idx
- sessions_2026_07_tenant_id_created_at_idx

## sessions_2026_08

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | nextval('public.sessions_id_seq'::regclass) |  |
| session_id | text | Y |  |  |
| tenant_id | character varying(255) | Y |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| closed_at | timestamp |  |  |  |
| status | text | Y | 'active'::text |  |
| total_turns | integer | Y | 0 |  |
| total_tokens | integer | Y | 0 |  |
| total_cost_usd | numeric(12,6) | Y | 0 |  |
| last_turn_no | integer |  |  |  |
| last_request_summary | text |  |  |  |
| last_response_summary | text |  |  |  |
| last_model | text |  |  |  |
| last_provider | text |  |  |  |
| task_type | text |  |  |  |
| client_type | text |  |  |  |
| topic | text |  |  |  |
| intent | text |  |  |  |
| primary_request_id | text |  |  |  |
| turn_logs_summary | jsonb |  |  |  |
| partition_date | date | Y | CURRENT_DATE |  |

索引（4）：
- sessions_2026_08_primary_request_id_idx
- sessions_2026_08_session_id_idx
- sessions_2026_08_status_updated_at_idx
- sessions_2026_08_tenant_id_created_at_idx

## settings_audit

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| setting_key | character varying(128) | Y |  |  |
| tenant_id | character |  |  |  |
| action | character varying(16) | Y |  |  |
| old_value | jsonb |  |  |  |
| new_value | jsonb |  |  |  |
| operator_user | character varying(64) | Y |  |  |
| operator_role | character varying(32) | Y |  |  |
| confirm_token | character |  |  |  |
| client_ip | character |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

索引（4）：
- idx_settings_audit_created
- idx_settings_audit_key_time
- idx_settings_audit_operator
- idx_settings_audit_tenant_time

## settings_kv

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| key | character varying(128) | Y |  |  |
| value | jsonb | Y |  |  |
| value_type | character varying(32) | Y |  |  |
| scope | character varying(16) | Y | 'platform'::character |  |
| category | character varying(32) | Y | 'general'::character |  |
| updated_at | timestamp with time zone | Y | now() |  |
| updated_by | character |  |  |  |
| prev_value | jsonb |  |  |  |
| prev_updated_at | timestamp |  |  |  |

索引（3）：
- idx_settings_kv_category
- idx_settings_kv_scope
- idx_settings_kv_updated

## severity_action_matrix

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| tenant_id | character varying(255) | Y | 'default'::character |  |
| severity_level | character varying(20) | Y |  |  |
| observe_action | public.injection_action |  | 'log'::public.injection_action |  |
| enforce_action | public.injection_action |  | 'block'::public.injection_action |  |
| require_approval | boolean |  | false |  |
| approval_timeout_minutes | integer |  | 0 |  |
| notify_on_detect | boolean |  | false |  |
| notify_channels | jsonb |  | '[]'::jsonb |  |
| affect_session_health | boolean |  | true |  |
| session_health_penalty | integer |  | 10 |  |
| terminate_session_on_repeat | boolean |  | false |  |
| repeat_threshold | integer |  | 3 |  |
| created_at | timestamp with time zone |  | now() |  |
| updated_at | timestamp with time zone |  | now() |  |

## sticky_sessions

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| sticky_key | text | Y |  |  |
| credential_id | bigint | Y |  |  |
| set_at | timestamp with time zone | Y | now() |  |
| expires_at | timestamp with time zone | Y |  |  |
| canonical_id | bigint |  |  |  |
| last_request_id | text |  |  |  |

## subscription_plans

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| code | character varying(32) | Y |  |  |
| tier | character varying(16) | Y |  |  |
| name | character varying(128) | Y |  |  |
| price_cents | integer | Y |  |  |
| monthly_credits | bigint | Y |  |  |
| enabled | boolean | Y | true |  |
| sort_order | integer | Y | 0 |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

## subscription_tiers

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| code | text | Y |  |  |
| name | text | Y |  |  |
| description | text | Y | ''::text |  |
| price_cents | integer | Y | 0 |  |
| sort_order | integer | Y | 0 |  |
| enabled | boolean | Y | true |  |
| created_at | timestamp with time zone | Y | now() |  |

索引（1）：
- idx_st_code

## system_identity_pool

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y | 1 |  |
| max_identities | integer | Y | 10000 |  |
| updated_at | timestamp with time zone | Y | now() |  |
| updated_by | text |  |  |  |

## system_probe_runs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| task_id | bigint | Y |  |  |
| task_type | text | Y |  |  |
| automaticity | text | Y | 'mandatory'::text |  |
| credential_id | bigint | Y |  |  |
| provider_id | bigint |  |  |  |
| raw_model | text | Y |  |  |
| source | text | Y |  |  |
| worker_id | text |  |  |  |
| status | text | Y |  |  |
| attempt | integer | Y | 1 |  |
| max_attempts | integer | Y | 3 |  |
| http_status | integer |  |  |  |
| latency_ms | integer |  |  |  |
| dns_ms | integer |  |  |  |
| tls_ms | integer |  |  |  |
| request_url | text |  |  |  |
| request_body_preview | text |  |  |  |
| response_body_preview | text |  |  |  |
| err_code | text |  |  |  |
| err_detail | text |  |  |  |
| skip_reason | text |  |  |  |
| recent_request_id | text |  |  |  |
| recent_request_at | timestamp |  |  |  |
| started_at | timestamp with time zone | Y |  |  |
| finished_at | timestamp with time zone | Y |  |  |
| created_at | timestamp with time zone | Y | now() |  |

## system_probe_runs_default

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| task_id | bigint | Y |  |  |
| task_type | text | Y |  |  |
| automaticity | text | Y | 'mandatory'::text |  |
| credential_id | bigint | Y |  |  |
| provider_id | bigint |  |  |  |
| raw_model | text | Y |  |  |
| source | text | Y |  |  |
| worker_id | text |  |  |  |
| status | text | Y |  |  |
| attempt | integer | Y | 1 |  |
| max_attempts | integer | Y | 3 |  |
| http_status | integer |  |  |  |
| latency_ms | integer |  |  |  |
| dns_ms | integer |  |  |  |
| tls_ms | integer |  |  |  |
| request_url | text |  |  |  |
| request_body_preview | text |  |  |  |
| response_body_preview | text |  |  |  |
| err_code | text |  |  |  |
| err_detail | text |  |  |  |
| skip_reason | text |  |  |  |
| recent_request_id | text |  |  |  |
| recent_request_at | timestamp |  |  |  |
| started_at | timestamp with time zone | Y |  |  |
| finished_at | timestamp with time zone | Y |  |  |
| created_at | timestamp with time zone | Y | now() |  |

索引（7）：
- system_probe_runs_default_automaticity_created_at_idx
- system_probe_runs_default_credential_id_created_at_idx
- system_probe_runs_default_provider_id_created_at_idx
- system_probe_runs_default_raw_model_created_at_idx
- system_probe_runs_default_skip_reason_idx
- system_probe_runs_default_status_created_at_idx
- system_probe_runs_default_task_id_idx

## system_settings

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| key | character varying(255) | Y |  |  |
| value | jsonb | Y |  |  |
| description | text |  |  |  |
| category | character varying(100) |  | 'general'::character |  |
| is_public | boolean |  | false |  |
| created_at | timestamp with time zone |  | now() |  |
| updated_at | timestamp with time zone |  | now() |  |
| updated_by | character |  |  |  |

索引（3）：
- idx_system_settings_category
- idx_system_settings_key
- idx_system_settings_updated

## task_default_routing

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| task_type | text | Y |  |  |
| profile | text | Y | ''::text |  |
| tier | text | Y | 'primary'::text |  |
| canonical_model | text | Y |  |  |
| tenant_id | character |  |  |  |
| priority | integer | Y | 100 |  |
| reason | text | Y | ''::text |  |
| created_by | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| expires_at | timestamp |  |  |  |

索引（2）：
- idx_task_default_routing_lookup
- uq_task_default_routing UNIQUE

## task_default_routing_audit

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| ts | timestamp with time zone | Y | now() |  |
| action | text | Y |  |  |
| routing_id | bigint |  |  |  |
| task_type | text |  |  |  |
| profile | text |  |  |  |
| tier | text |  |  |  |
| canonical_model | text |  |  |  |
| tenant_id | character |  |  |  |
| priority | integer |  |  |  |
| reason | text |  |  |  |
| expires_at | timestamp |  |  |  |
| old_expires_at | timestamp |  |  |  |
| actor | text |  |  |  |

## tenant_credit_wallets

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| tenant_id | character varying(64) | Y |  |  |
| balance_credits | bigint | Y | 0 |  |
| locked_credits | bigint | Y | 0 |  |
| updated_at | timestamp with time zone | Y | now() |  |
| granted_balance | bigint | Y | 0 |  |
| purchased_balance | bigint | Y | 0 |  |

## tenant_model_policies

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| canonical_name | text | Y |  |  |
| reason | text | Y | ''::text |  |
| created_by | character varying(128) | Y | ''::character |  |
| deleted_at | timestamp |  |  |  |
| deleted_by | character |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（2）：
- idx_tmp_canonical
- idx_tmp_tenant_active

## tenant_model_policies_audit

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| ts | timestamp with time zone | Y | now() |  |
| action | text | Y |  |  |
| policy_id | bigint |  |  |  |
| tenant_id | text |  |  |  |
| canonical_name | text |  |  |  |
| reason | text |  |  |  |
| actor | text |  |  |  |

索引（2）：
- idx_tmp_audit_tenant_ts
- idx_tmp_audit_ts

## tenant_settings_kv

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| tenant_id | character varying(64) | Y |  |  |
| key | character varying(128) | Y |  |  |
| value | jsonb | Y |  |  |
| value_type | character varying(32) | Y |  |  |
| category | character varying(32) | Y | 'general'::character |  |
| updated_at | timestamp with time zone | Y | now() |  |
| updated_by | character |  |  |  |
| prev_value | jsonb |  |  |  |
| prev_updated_at | timestamp |  |  |  |

索引（2）：
- idx_tenant_settings_kv_category
- idx_tenant_settings_kv_tenant

## tenant_subscriptions

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| plan_id | integer | Y |  |  |
| status | character varying(32) | Y | 'active'::character |  |
| period_start | timestamp with time zone | Y |  |  |
| period_end | timestamp with time zone | Y |  |  |
| quota_remaining | bigint | Y | 0 |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（1）：
- idx_tenant_subscriptions_tenant

## tenant_tool_policies

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | character varying(64) | Y |  |  |
| tool_pattern | character varying(128) | Y |  |  |
| policy_type | character varying(16) | Y |  |  |
| reason | character |  |  |  |
| enabled | boolean | Y | true |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| created_by | character |  |  |  |

索引（2）：
- idx_tenant_tool_policies_enabled
- idx_tenant_tool_policies_tenant

## tenants

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| code | character varying(64) | Y |  |  |
| name | character varying(128) | Y |  |  |
| status | character varying(32) | Y | 'active'::character |  |
| description | text | Y | ''::text |  |
| contact_email | character varying(256) | Y | ''::character |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（2）：
- idx_tenants_name
- idx_tenants_status

## test_columnar_new

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| tenant_id | text |  |  |  |
| model | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| created_at | timestamp with time zone |  | now() |  |

## tier_module_map

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| tier_code | text | Y |  |  |
| module_key | text | Y |  |  |
| max_features | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

外键：
- (module_key) → product_modules(key)
- (tier_code) → subscription_tiers(code)

## token_audit_events

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| request_id | text | Y |  |  |
| credential_id | bigint | Y |  |  |
| claimed_tokens | integer |  |  |  |
| estimated_tokens | integer |  |  |  |
| delta_pct | numeric(6,3) |  |  |  |
| ts | timestamp with time zone | Y | now() |  |

## tool_call_events

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint |  |  |  |
| tool_id | character |  |  |  |
| tenant_id | character |  |  |  |
| request_id | character |  |  |  |
| api_key | character |  |  |  |
| status | character |  |  |  |
| latency_ms | integer |  |  |  |
| error_code | character |  |  |  |
| called_at | timestamp |  |  |  |

索引（3）：
- idx_tool_call_events_called_at
- idx_tool_call_events_tenant_id
- idx_tool_call_events_tool_id

## tool_categories

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | character varying(64) | Y |  |  |
| name | character varying(128) | Y |  |  |
| description | text |  |  |  |
| enabled | boolean |  | true |  |
| display_order | integer |  | 0 |  |
| created_at | timestamp with time zone |  | now() |  |
| updated_at | timestamp with time zone |  | now() |  |

索引（1）：
- idx_tool_categories_order

## tool_registry

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| category | character varying(64) | Y |  |  |
| tool_name | character varying(128) | Y |  |  |
| tool_definition | jsonb | Y |  |  |
| enabled | boolean |  | true |  |
| priority | integer |  | 0 |  |
| created_at | timestamp with time zone |  | now() |  |
| updated_at | timestamp with time zone |  | now() |  |
| tool_id | character varying(128) | Y |  |  |
| tenant_id | character varying(64) |  | 'default'::character |  |
| version | integer |  | 1 |  |
| deprecation_date | timestamp |  |  |  |
| min_client_version | character |  |  |  |
| breaking_changes | jsonb |  | '[]'::jsonb |  |
| superseded_by | character |  |  |  |

索引（5）：
- idx_tool_registry_category
- idx_tool_registry_deprecation
- idx_tool_registry_name
- idx_tool_registry_tenant_tool
- idx_tool_registry_unique_version UNIQUE

## tool_usage_stats

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tool_id | character varying | Y |  |  |
| tenant_id | character varying | Y |  |  |
| usage_date | date | Y |  |  |
| call_count | bigint |  | 0 |  |
| success_count | bigint |  | 0 |  |
| error_count | bigint |  | 0 |  |
| avg_latency_ms | integer |  |  |  |
| last_called_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp |  |  |  |

## tool_usage_stats_2026_07

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | nextval('public.tool_usage_stats_partitioned_id_seq'::regclass) |  |
| tool_id | character varying | Y |  |  |
| tenant_id | character varying | Y |  |  |
| usage_date | date | Y |  |  |
| call_count | bigint |  | 0 |  |
| success_count | bigint |  | 0 |  |
| error_count | bigint |  | 0 |  |
| avg_latency_ms | integer |  |  |  |
| last_called_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp |  |  |  |

索引（4）：
- tool_usage_stats_2026_07_created_at_idx
- tool_usage_stats_2026_07_tenant_id_usage_date_idx
- tool_usage_stats_2026_07_tool_id_usage_date_idx
- tool_usage_stats_2026_07_usage_date_idx

## tool_usage_stats_2026_08

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | nextval('public.tool_usage_stats_partitioned_id_seq'::regclass) |  |
| tool_id | character varying | Y |  |  |
| tenant_id | character varying | Y |  |  |
| usage_date | date | Y |  |  |
| call_count | bigint |  | 0 |  |
| success_count | bigint |  | 0 |  |
| error_count | bigint |  | 0 |  |
| avg_latency_ms | integer |  |  |  |
| last_called_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp |  |  |  |

索引（4）：
- tool_usage_stats_2026_08_created_at_idx
- tool_usage_stats_2026_08_tenant_id_usage_date_idx
- tool_usage_stats_2026_08_tool_id_usage_date_idx
- tool_usage_stats_2026_08_usage_date_idx

## tool_usage_stats_hot

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y | nextval('public.tool_usage_stats_partitioned_id_seq'::regclass) |  |
| tool_id | character varying | Y |  |  |
| tenant_id | character varying | Y |  |  |
| usage_date | date | Y |  |  |
| call_count | bigint |  | 0 |  |
| success_count | bigint |  | 0 |  |
| error_count | bigint |  | 0 |  |
| avg_latency_ms | integer |  |  |  |
| last_called_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp |  |  |  |

索引（8）：
- tool_usage_stats_hot_created_at_idx
- tool_usage_stats_hot_date_idx
- tool_usage_stats_hot_tenant_date_idx
- tool_usage_stats_hot_tenant_id_usage_date_idx
- tool_usage_stats_hot_tool_date_idx
- tool_usage_stats_hot_tool_id_usage_date_idx
- tool_usage_stats_hot_tool_tenant_date_key UNIQUE
- tool_usage_stats_hot_usage_date_idx

## tool_usage_stats_old

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tool_id | character varying(128) | Y |  |  |
| tenant_id | character varying(64) | Y | 'default'::character |  |
| usage_date | date | Y | CURRENT_DATE |  |
| call_count | bigint | Y | 0 |  |
| success_count | bigint | Y | 0 |  |
| error_count | bigint | Y | 0 |  |
| avg_latency_ms | integer |  | 0 |  |
| last_called_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（4）：
- idx_tool_usage_stats_date
- idx_tool_usage_stats_tenant_id
- idx_tool_usage_stats_tool_id
- idx_tool_usage_stats_tool_tenant

## topup_packages

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| code | character varying(32) | Y |  |  |
| tier | character varying(16) | Y |  |  |
| name | character varying(128) | Y |  |  |
| price_cents | integer | Y |  |  |
| credits_amount | bigint | Y |  |  |
| enabled | boolean | Y | true |  |
| sort_order | integer | Y | 0 |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

## toxic_keywords

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| keyword | character varying(100) | Y |  |  |
| category | character varying(50) | Y |  |  |
| severity | integer | Y |  |  |
| language | character varying(10) |  | 'zh'::character |  |
| enabled | boolean |  | true |  |
| created_at | timestamp with time zone |  | now() |  |

## tuning_params

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| key | text | Y |  |  |
| value | jsonb | Y |  |  |
| category | text | Y |  |  |
| source | text | Y | 'default'::text |  |
| confidence | numeric(4,3) | Y | 1.0 |  |
| enabled | boolean | Y | true |  |
| description | text |  |  |  |
| applied_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

## tuning_proposals

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| ts | timestamp with time zone | Y | now() |  |
| category | text | Y |  |  |
| task_type | text |  |  |  |
| proposal | jsonb | Y |  |  |
| evidence | jsonb | Y |  |  |
| status | text | Y | 'pending'::text |  |
| reviewed_by | text |  |  |  |
| reviewed_at | timestamp |  |  |  |
| applied_at | timestamp |  |  |  |
| review_note | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（3）：
- idx_tuning_proposals_cat
- idx_tuning_proposals_created
- idx_tuning_proposals_status

## tuning_signals

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| request_id | text | Y |  |  |
| session_id | text |  |  |  |
| ts | timestamp with time zone | Y | now() |  |
| task_type | text | Y |  |  |
| classifier | text | Y |  |  |
| confidence | numeric(4,3) |  |  |  |
| chosen_model | text |  |  |  |
| canonical_id | integer |  |  |  |
| success_score | numeric(3,2) | Y | 0.5 |  |
| latency_score | numeric(3,2) | Y | 0.5 |  |
| cost_score | numeric(3,2) | Y | 0.5 |  |
| drift_flag | boolean | Y | false |  |
| quality_score | numeric(3,2) | Y | 0.5 |  |
| latency_ms | integer |  |  |  |
| cost_usd | numeric(10,6) |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| signal_payload | jsonb |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| strategy | text | Y | 'pattern_layered'::text |  |

索引（5）：
- idx_tuning_signals_lowq
- idx_tuning_signals_session
- idx_tuning_signals_strategy_task
- idx_tuning_signals_strategy_ts
- idx_tuning_signals_task_ts

## upgrade_logs

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| instance_id | text | Y |  |  |
| old_version | text | Y |  |  |
| new_version | text | Y |  |  |
| status | text | Y | 'pending'::text |  |
| started_at | timestamp with time zone | Y | now() |  |
| completed_at | timestamp |  |  |  |
| error_message | text |  |  |  |
| retry_count | integer | Y | 0 |  |
| duration_ms | integer |  |  |  |

索引（3）：
- idx_upgrade_logs_failed
- idx_upgrade_logs_instance
- idx_upgrade_logs_status

## ursm_node_snapshot_min

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| snapshot_ts | timestamp with time zone | Y |  |  |
| recovery_epoch | bigint | Y |  |  |
| provider_id | integer | Y |  |  |
| credential_id | integer | Y |  |  |
| raw_model_name | text | Y |  |  |
| canonical_name | text |  |  |  |
| tenant_id | text | Y | ''::text |  |
| available | boolean | Y |  |  |
| health_status | text |  |  |  |
| fail_streak | integer |  |  |  |
| cool_until | timestamp |  |  |  |
| sr_1m | real |  |  |  |
| sr_5m | real |  |  |  |
| sr_30m | real |  |  |  |
| samples_1m | integer |  |  |  |
| samples_5m | integer |  |  |  |
| samples_30m | integer |  |  |  |
| lat_p50_ms | integer |  |  |  |
| lat_p95_ms | integer |  |  |  |
| score | real |  |  |  |
| price_in_per_1m | numeric |  |  |  |
| price_out_per_1m | numeric |  |  |  |
| billing_mode | text |  |  |  |
| trust_level | real |  |  |  |
| baseurl_latency_ms | integer |  |  |  |
| conc_used | integer |  |  |  |
| conc_limit | integer |  |  |  |
| fp_used | integer |  |  |  |
| fp_limit | integer |  |  |  |
| source_priority | integer |  |  |  |
| generation | bigint |  |  |  |
| payload | jsonb |  |  |  |

索引（1）：
- ursm_node_snapshot_min_ts_idx

## usage_ledger

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y |  |  |
| tenant_id | text | Y |  |  |
| application_id | integer |  |  |  |
| api_key_id | integer |  |  |  |
| end_user_id | text |  |  |  |
| credential_id | integer |  |  |  |
| provider_id | integer |  |  |  |
| canonical_id | integer |  |  |  |
| raw_model_name | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| cache_read_tokens | integer |  |  |  |
| cache_write_tokens | integer |  |  |  |
| total_tokens | integer |  |  |  |
| cost_usd | numeric(12,6) |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean |  |  |  |
| error_kind | text |  |  |  |

## usage_ledger_2026_07

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y |  |  |
| tenant_id | text | Y |  |  |
| application_id | integer |  |  |  |
| api_key_id | integer |  |  |  |
| end_user_id | text |  |  |  |
| credential_id | integer |  |  |  |
| provider_id | integer |  |  |  |
| canonical_id | integer |  |  |  |
| raw_model_name | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| cache_read_tokens | integer |  |  |  |
| cache_write_tokens | integer |  |  |  |
| total_tokens | integer |  |  |  |
| cost_usd | numeric(12,6) |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean |  |  |  |
| error_kind | text |  |  |  |

索引（5）：
- usage_ledger_2026_07_request_id_idx
- usage_ledger_2026_07_tenant_id_ts_idx
- usage_ledger_2026_07_tenant_id_ts_idx2
- usage_ledger_2026_07_ts_idx
- usage_ledger_2026_07_ts_idx2

## usage_ledger_2026_08

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y |  |  |
| tenant_id | text | Y |  |  |
| application_id | integer |  |  |  |
| api_key_id | integer |  |  |  |
| end_user_id | text |  |  |  |
| credential_id | integer |  |  |  |
| provider_id | integer |  |  |  |
| canonical_id | integer |  |  |  |
| raw_model_name | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| cache_read_tokens | integer |  |  |  |
| cache_write_tokens | integer |  |  |  |
| total_tokens | integer |  |  |  |
| cost_usd | numeric(12,6) |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean |  |  |  |
| error_kind | text |  |  |  |

索引（3）：
- usage_ledger_2026_08_request_id_idx
- usage_ledger_2026_08_tenant_id_ts_idx
- usage_ledger_2026_08_ts_idx

## usage_ledger_hot

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y |  |  |
| tenant_id | text | Y |  |  |
| application_id | integer |  |  |  |
| api_key_id | integer |  |  |  |
| end_user_id | text |  |  |  |
| credential_id | integer |  |  |  |
| provider_id | integer |  |  |  |
| canonical_id | integer |  |  |  |
| raw_model_name | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| cache_read_tokens | integer |  |  |  |
| cache_write_tokens | integer |  |  |  |
| total_tokens | integer |  |  |  |
| cost_usd | numeric(12,6) |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean |  |  |  |
| error_kind | text |  |  |  |
| reasoning_tokens | integer |  |  |  |
| image_tokens | integer |  |  |  |
| audio_tokens | integer |  |  |  |
| video_tokens | integer |  |  |  |
| provider_tokens | integer |  |  |  |

索引（4）：
- usage_ledger_hot_api_key_id_ts_idx
- usage_ledger_hot_request_id_idx
- usage_ledger_hot_tenant_id_ts_idx
- usage_ledger_hot_ts_idx

## usage_ledger_old

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| request_id | text | Y |  |  |
| ts | timestamp with time zone | Y |  |  |
| tenant_id | text | Y |  |  |
| application_id | integer |  |  |  |
| api_key_id | integer |  |  |  |
| end_user_id | text |  |  |  |
| credential_id | integer |  |  |  |
| provider_id | integer |  |  |  |
| canonical_id | integer |  |  |  |
| raw_model_name | text |  |  |  |
| prompt_tokens | integer |  |  |  |
| completion_tokens | integer |  |  |  |
| cache_read_tokens | integer |  |  |  |
| cache_write_tokens | integer |  |  |  |
| total_tokens | integer |  |  |  |
| cost_usd | numeric(12,6) |  |  |  |
| latency_ms | integer |  |  |  |
| success | boolean |  |  |  |
| error_kind | text |  |  |  |

## usage_minute

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| bucket | timestamp with time zone | Y |  |  |
| tenant_id | text | Y | 'default'::text |  |
| application_id | bigint |  |  |  |
| api_key_id | bigint |  |  |  |
| end_user_id | text |  |  |  |
| department | text |  |  |  |
| employee | text |  |  |  |
| position | text |  |  |  |
| credential_id | bigint |  |  |  |
| provider_id | bigint |  |  |  |
| canonical_id | bigint |  |  |  |
| requests | bigint | Y | 0 |  |
| prompt_tokens | bigint | Y | 0 |  |
| completion_tokens | bigint | Y | 0 |  |
| total_tokens | bigint | Y | 0 |  |
| cost_usd | numeric(18,8) | Y | 0 |  |
| errors | bigint | Y | 0 |  |

## users

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| tenant_id | character varying(64) | Y | 'default'::character |  |
| username | character varying(128) | Y |  |  |
| password_hash | character varying(256) | Y |  |  |
| display_name | character varying(128) | Y | ''::character |  |
| email | character varying(256) | Y | ''::character |  |
| role | character varying(32) | Y | 'tenant_admin'::character |  |
| enabled | boolean | Y | true |  |
| last_login_at | timestamp |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |
| must_change_password | boolean | Y | false |  |

索引（2）：
- idx_users_tenant
- idx_users_username

## vibe_code_reviews

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| session_id | bigint |  |  |  |
| tenant_id | text | Y | 'default'::text |  |
| file_path | text |  |  |  |
| language | text |  |  |  |
| original_code | text |  |  |  |
| review_result | jsonb |  |  |  |
| score | numeric(3,2) |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |

外键：
- (session_id) → vibe_coding_sessions(id)

索引（2）：
- vcr_session
- vcr_tenant

## vibe_coding_projects

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| tenant_id | text | Y | 'default'::text |  |
| name | text | Y |  |  |
| description | text |  |  |  |
| language | text |  |  |  |
| framework | text |  |  |  |
| status | text | Y | 'active'::text |  |
| settings | jsonb | Y | '{}'::jsonb |  |
| created_by | text |  |  |  |
| created_at | timestamp with time zone | Y | now() |  |
| updated_at | timestamp with time zone | Y | now() |  |

索引（2）：
- vcp_status
- vcp_tenant

## vibe_coding_sessions

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | bigint | Y |  |  |
| project_id | bigint |  |  |  |
| tenant_id | text | Y | 'default'::text |  |
| session_id | text | Y |  |  |
| task_type | text | Y |  |  |
| status | text | Y | 'active'::text |  |
| messages | jsonb | Y | '[]'::jsonb |  |
| metadata | jsonb | Y | '{}'::jsonb |  |
| created_at | timestamp with time zone | Y | now() |  |
| completed_at | timestamp |  |  |  |

外键：
- (project_id) → vibe_coding_projects(id)

索引（3）：
- vcs_project
- vcs_session
- vcs_tenant

## work_type_config

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| key | text | Y |  |  |
| label | text | Y |  |  |
| category | text | Y |  |  |
| l1_task_type | text | Y |  |  |
| default_profile | text | Y | 'smart'::text |  |
| tags | text[] | Y | '{}'::text[] |  |
| prompt_keywords | text[] | Y | '{}'::text[] |  |
| acc_task_type | text |  |  |  |
| enabled | boolean | Y | true |  |
| sort_order | integer | Y | 0 |  |
| synced_from_acc_at | timestamp |  |  |  |
| updated_at | timestamp with time zone | Y | now() |  |
| system_prompt | text |  |  |  |

索引（2）：
- idx_work_type_config_category
- idx_work_type_config_l1

## work_type_model_route

| 列 | 类型 | NN | 默认 | 键 |
|---|---|---|---|---|
| id | integer | Y |  |  |
| work_type_key | text | Y |  |  |
| canonical_name | text | Y |  |  |
| weight | numeric(5,2) | Y | 1.0 |  |
| min_score | numeric(8,4) | Y | 0 |  |
| enabled | boolean | Y | true |  |
| tier | text | Y | 'secondary'::text |  |
| task_quality_score | numeric(5,2) | Y | 0 |  |

索引（2）：
- idx_wtmr_tier
- idx_wtmr_work_type