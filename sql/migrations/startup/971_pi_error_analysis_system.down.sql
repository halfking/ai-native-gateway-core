-- Migration Rollback: 971_pi_error_analysis_system
-- Purpose: 回滚 PI 智能体错误自动分析系统
-- Author: System
-- Date: 2026-11-17
--
-- ★ 收编记录：原 641_pi_error_analysis_system.down.sql（落在 sql/migrations/domain/
--   死文件区，无执行器接线）⇒ 改号 971 并搬进正典通道 sql/migrations/startup/。
--   本件本身已是幂等形态（全部 IF EXISTS），仅补 public. 限定与台账行清理。

-- 按依赖顺序逆序删除

-- 1. 删除清理函数
DROP FUNCTION IF EXISTS public.cleanup_old_error_analysis(INTEGER);

-- 2. 删除触发器和触发函数
DROP TRIGGER IF EXISTS trigger_error_analysis_results_updated_at
    ON public.error_analysis_results;
DROP FUNCTION IF EXISTS public.update_error_analysis_updated_at();

-- 3. 删除视图
DROP VIEW IF EXISTS public.v_error_analysis_stats;

-- 4. 删除结果表（会级联删除外键约束）
DROP TABLE IF EXISTS public.error_analysis_results CASCADE;

-- 5. 删除队列表
DROP TABLE IF EXISTS public.error_analysis_queue CASCADE;

-- 6. 台账行清理（与 up 的 schema_migrations 自登记对称）
DELETE FROM public.schema_migrations WHERE version = '971';

-- Rollback complete
