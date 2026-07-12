-- Migration 338 (down): 撤销系统自检模块
BEGIN;
DROP TABLE IF EXISTS self_check_round_results;
DROP TABLE IF EXISTS self_check_runs;
DROP TABLE IF EXISTS self_check_settings;
COMMIT;