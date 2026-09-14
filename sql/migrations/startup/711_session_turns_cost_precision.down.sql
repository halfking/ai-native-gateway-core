-- Migration 711 down: restore session_turns.cost_usd to numeric(12,6).
-- 注意：回滚会重新引入 E5 舍入漂移；已回填的 8 位小数值将被截断到 6 位。

ALTER TABLE public.session_turns
    ALTER COLUMN cost_usd TYPE numeric(12, 6);
