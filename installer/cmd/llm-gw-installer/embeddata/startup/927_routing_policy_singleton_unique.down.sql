-- 927 down：撤销 routing_policy 的 UNIQUE (tenant_id) 约束。
--
-- ★ 被去重删掉的行**无法恢复** —— 但它们与保留行**内容相同**
--   （★ 步骤 1 已证明内容一致才删）⇒ 恢复它们只是重建重复行，没有信息量。
-- ★ 撤销约束后，★ 02-seed.sql 的 `ON CONFLICT DO NOTHING` 会**重新变成空操作**
--   （★ 没有唯一约束就没有可冲突的目标）⇒ ★ 下次安装会**再次堆出重复行**
--   ⇒ ★★ 也就是说：★ **这个 down 是「把根因放回去」**，不执行才是正确的常态。
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '30s';

ALTER TABLE public.routing_policy
  DROP CONSTRAINT IF EXISTS routing_policy_tenant_id_key;
