-- Rollback Migration 134: Tool Execution Tracking

-- 删除触发器和函数
DROP TRIGGER IF EXISTS trg_tool_usage_stats_updated_at ON tool_usage_stats;
DROP TRIGGER IF EXISTS trg_tool_executions_duration ON tool_executions;
DROP FUNCTION IF EXISTS update_tool_usage_stats_updated_at();
DROP FUNCTION IF EXISTS calculate_tool_execution_duration();

-- 删除表
DROP TABLE IF EXISTS tool_usage_stats;
DROP TABLE IF EXISTS tool_executions;
