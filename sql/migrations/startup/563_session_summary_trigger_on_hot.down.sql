-- 563_session_summary_trigger_on_hot.down.sql
-- 回滚：移除 hot 触发器。不恢复 310 旧函数体（错误列名会再次弄坏写入）。

DROP TRIGGER IF EXISTS trg_update_session_summary ON request_logs_hot;
