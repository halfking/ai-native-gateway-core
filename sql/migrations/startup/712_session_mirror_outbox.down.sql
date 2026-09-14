-- Migration 712 down: drop the session mirror outbox replay queue.
-- 未消化的 pending/claimed 行将永久丢失（对应 turns 缺行），
-- 生产环境回滚前应确认 status 仅剩 'dead' 或行数为 0。

DROP TABLE IF EXISTS public.session_mirror_outbox;
