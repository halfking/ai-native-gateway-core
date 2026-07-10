-- Migration 334 (down): 撤销 cmb.billing_mode 的对齐 — 把 plan_type_origin='sync_with_cred:334' 的行回退
-- 注意: 不一定能精准还原为原来的 'per_token'/'free'（不知道原值），所以 down-migration 采用
--        保守策略：仅清掉 plan_type_origin 标记，billing_mode 保留修改后的值。下游可以
--        重新跑其他修复（比如上游凭证解除 token_plan）恢复原状。

BEGIN;

UPDATE credential_model_bindings
SET    plan_type_origin = NULL,
       plan_type_updated_at = NULL
WHERE  plan_type_origin = 'sync_with_cred:334';

SELECT pg_notify('auto_route_refresh', 'manual:334:revert');

COMMIT;
