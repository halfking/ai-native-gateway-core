-- W4.3 down: 渠道/客户层级表（只删本迁移建的表，不动 applications.customer_id）。
DROP TABLE IF EXISTS public.channel_users;
DROP TABLE IF EXISTS public.customers;
DROP TABLE IF EXISTS public.channels;
