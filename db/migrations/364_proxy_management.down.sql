-- Migration 364 Down: Rollback Proxy Management System

-- 1. 删除供应商表新增字段
ALTER TABLE providers DROP COLUMN IF EXISTS proxy_subscription_id;

-- 2. 删除表（按依赖关系逆序）
DROP TABLE IF EXISTS provider_domains;
DROP TABLE IF EXISTS proxy_nodes;
DROP TABLE IF EXISTS proxy_subscriptions;

-- 3. 删除索引（如果表已删除则自动删除，此处为安全起见）
DROP INDEX IF EXISTS idx_providers_egress;
DROP INDEX IF EXISTS idx_providers_proxy_sub;
