-- 878 down：删掉 878 在 supplier_errors 分区父表上声明的 5 条索引。
--
-- ⚠ 回滚代价：删父表索引会**级联删掉各分区的子索引**（PG 语义）。
--   重建需要重新扫全部分区数据（本实例月分区 54MB，分钟级；
--   若生产列存形态则见 878 文件头的多副本警告，成本另计）。
--   不要把这条当轻量操作。
--
-- ⚠ 回滚会把 211 实测的读侧代价原样带回来：
--   supplier_errors_2026_10 累计 seq_tup_read=69,873,160 / idx_scan 为空。
BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

DROP INDEX IF EXISTS public.idx_supplier_errors_part_error_type;
DROP INDEX IF EXISTS public.idx_supplier_errors_part_tenant;
DROP INDEX IF EXISTS public.idx_supplier_errors_part_credential_occurred;
DROP INDEX IF EXISTS public.idx_supplier_errors_part_supplier_credential;
DROP INDEX IF EXISTS public.idx_supplier_errors_part_occurred_at;

COMMIT;

-- 878 删掉的 5 条索引，回滚时按 878 原样重建（形状与 hot 侧对称）。
CREATE INDEX IF NOT EXISTS idx_supplier_errors_part_occurred_at
    ON public.supplier_errors USING btree (occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_supplier_errors_part_supplier_credential
    ON public.supplier_errors USING btree (supplier, credential_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_supplier_errors_part_credential_occurred
    ON public.supplier_errors USING btree (credential_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_supplier_errors_part_tenant
    ON public.supplier_errors USING btree (tenant_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_supplier_errors_part_error_type
    ON public.supplier_errors USING btree (error_type, occurred_at DESC);
