-- 414_drop_model_probe_runs_old.down.sql
-- 回滚不可行：model_probe_runs_old 已 DROP，数据无法恢复。
-- 如需保留 columnar 历史，应在应用 414 前先 pg_dump model_probe_runs_old。
-- 此 down 文件仅为保持 migration 文件成对的结构约定，不做任何操作。
SELECT 1;
