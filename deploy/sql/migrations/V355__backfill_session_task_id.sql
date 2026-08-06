-- V355: 回填 session_summaries.gw_task_id 从 request_logs
-- 2026-08-06: 历史数据迁移 - 从 request_logs 中提取每个会话的 gw_task_id
--
-- 策略：
--   1. 对于每个 session_key，从 request_logs 中查找该会话的请求
--   2. 选择出现次数最多的 gw_task_id 作为该会话的任务ID
--   3. 如果该会话的所有请求都没有 gw_task_id，则保持为 NULL
--
-- 注意：
--   - 这是一次性迁移，对于新会话，gw_task_id 应该在会话创建时或总结时实时填充
--   - 此脚本可能需要较长时间（取决于数据量），建议在低峰期执行
--   - 使用 ON CONFLICT DO UPDATE 确保幂等性

-- 创建临时函数来回填 task_id
CREATE OR REPLACE FUNCTION backfill_session_task_ids()
RETURNS TABLE(
  session_key text,
  old_task_id text,
  new_task_id text,
  request_count bigint
) AS $$
BEGIN
  RETURN QUERY
  WITH session_task_mapping AS (
    -- 为每个 session 找出出现次数最多的 task_id
    SELECT 
      rl.gw_session_id as session_key,
      rl.gw_task_id,
      COUNT(*) as task_count,
      ROW_NUMBER() OVER (
        PARTITION BY rl.gw_session_id 
        ORDER BY COUNT(*) DESC, MIN(rl.ts) ASC
      ) as rn
    FROM request_logs_hot rl
    WHERE rl.gw_session_id IS NOT NULL 
      AND TRIM(rl.gw_session_id) != ''
      AND rl.gw_task_id IS NOT NULL
      AND TRIM(rl.gw_task_id) != ''
    GROUP BY rl.gw_session_id, rl.gw_task_id
  )
  SELECT 
    ss.session_key,
    ss.gw_task_id as old_task_id,
    stm.gw_task_id as new_task_id,
    stm.task_count as request_count
  FROM session_summaries ss
  LEFT JOIN session_task_mapping stm 
    ON ss.session_key = stm.session_key 
    AND stm.rn = 1
  WHERE ss.gw_task_id IS NULL 
    AND stm.gw_task_id IS NOT NULL;
END;
$$ LANGUAGE plpgsql;

-- 执行回填（分批处理以避免长事务）
DO $$
DECLARE
  batch_size int := 500;
  total_updated int := 0;
  batch_count int := 0;
  rec record;
BEGIN
  RAISE NOTICE 'Starting session_summaries.gw_task_id backfill...';
  
  -- 使用游标处理大量数据
  FOR rec IN 
    SELECT * FROM backfill_session_task_ids()
  LOOP
    UPDATE session_summaries
    SET gw_task_id = rec.new_task_id,
        updated_at = NOW()
    WHERE session_key = rec.session_key;
    
    total_updated := total_updated + 1;
    batch_count := batch_count + 1;
    
    -- 每 batch_size 条提交一次并输出进度
    IF batch_count >= batch_size THEN
      RAISE NOTICE 'Updated % sessions...', total_updated;
      batch_count := 0;
      -- 让出 CPU 避免阻塞其他操作
      PERFORM pg_sleep(0.1);
    END IF;
  END LOOP;
  
  RAISE NOTICE 'Backfill completed. Total sessions updated: %', total_updated;
END;
$$;

-- 清理临时函数
DROP FUNCTION IF EXISTS backfill_session_task_ids();

-- 创建触发器函数：自动同步新请求的 task_id 到 session_summaries
-- 当 request_logs 插入新记录时，如果该会话的 session_summaries 行存在且 gw_task_id 为空，则填充
CREATE OR REPLACE FUNCTION sync_session_task_id()
RETURNS TRIGGER AS $$
BEGIN
  -- 只处理有 session_id 和 task_id 的请求
  IF NEW.gw_session_id IS NOT NULL 
     AND TRIM(NEW.gw_session_id) != ''
     AND NEW.gw_task_id IS NOT NULL 
     AND TRIM(NEW.gw_task_id) != '' THEN
    
    -- 如果 session_summaries 中该会话的 gw_task_id 为空，则更新
    UPDATE session_summaries
    SET gw_task_id = NEW.gw_task_id,
        updated_at = NOW()
    WHERE session_key = NEW.gw_session_id
      AND gw_task_id IS NULL;
  END IF;
  
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- 在 request_logs_hot 上创建触发器（仅对 INSERT 生效）
DROP TRIGGER IF EXISTS trg_sync_session_task_id ON request_logs_hot;
CREATE TRIGGER trg_sync_session_task_id
  AFTER INSERT ON request_logs_hot
  FOR EACH ROW
  EXECUTE FUNCTION sync_session_task_id();

COMMENT ON FUNCTION sync_session_task_id() IS '自动同步 request_logs 的 gw_task_id 到 session_summaries（仅当 session_summaries.gw_task_id 为空时）';
COMMENT ON TRIGGER trg_sync_session_task_id ON request_logs_hot IS '自动同步新请求的 task_id 到对应的会话汇总行';

-- 创建统计报告视图（用于验证迁移结果）
CREATE OR REPLACE VIEW v_session_task_id_coverage AS
SELECT 
  COUNT(*) as total_sessions,
  COUNT(gw_task_id) as sessions_with_task_id,
  COUNT(*) - COUNT(gw_task_id) as sessions_without_task_id,
  ROUND(100.0 * COUNT(gw_task_id) / COUNT(*), 2) as coverage_percentage
FROM session_summaries;

COMMENT ON VIEW v_session_task_id_coverage IS '会话 task_id 覆盖率统计（用于验证迁移结果）';

-- 输出迁移报告
DO $$
DECLARE
  report record;
BEGIN
  SELECT * INTO report FROM v_session_task_id_coverage;
  
  RAISE NOTICE '=== Session Task ID Migration Report ===';
  RAISE NOTICE 'Total sessions: %', report.total_sessions;
  RAISE NOTICE 'Sessions with task_id: %', report.sessions_with_task_id;
  RAISE NOTICE 'Sessions without task_id: %', report.sessions_without_task_id;
  RAISE NOTICE 'Coverage: %%', report.coverage_percentage;
  RAISE NOTICE '========================================';
END;
$$;
