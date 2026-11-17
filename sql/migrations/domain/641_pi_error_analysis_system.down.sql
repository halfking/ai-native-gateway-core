-- Migration Rollback: 641_pi_error_analysis_system
-- Purpose: 回滚 PI 智能体错误自动分析系统
-- Author: System
-- Date: 2026-11-17

-- 按依赖顺序逆序删除

-- 1. 删除清理函数
DROP FUNCTION IF EXISTS cleanup_old_error_analysis(INTEGER);

-- 2. 删除触发器和触发函数
DROP TRIGGER IF EXISTS trigger_error_analysis_results_updated_at ON error_analysis_results;
DROP FUNCTION IF EXISTS update_error_analysis_updated_at();

-- 3. 删除视图
DROP VIEW IF EXISTS v_error_analysis_stats;

-- 4. 删除结果表（会级联删除外键约束）
DROP TABLE IF EXISTS error_analysis_results CASCADE;

-- 5. 删除队列表
DROP TABLE IF EXISTS error_analysis_queue CASCADE;

-- Rollback complete
