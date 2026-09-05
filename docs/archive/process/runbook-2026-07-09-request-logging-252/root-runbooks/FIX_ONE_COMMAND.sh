#!/bin/bash
# 一键修复命令 - 直接复制到终端执行

echo "==================================="
echo "开始修复252数据库..."
echo "==================================="
echo ""

# 在252服务器上执行修复
ssh root@192.168.0.252 "sudo -u postgres psql -d llm_gateway" << 'EOSQL'
BEGIN;

CREATE TABLE IF NOT EXISTS request_wal_hot (
    request_id character varying(64) NOT NULL,
    tenant_id character varying(64) NOT NULL,
    gw_session_id character varying(128),
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    stage smallint DEFAULT 0 NOT NULL,
    client_model character varying(100),
    upstream_provider_id bigint,
    upstream_credential_id bigint,
    completion_tokens integer,
    prompt_tokens integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    upstream_request_at timestamp with time zone,
    upstream_response_at timestamp with time zone,
    error text,
    compression_strategy character varying(50),
    compression_meta jsonb,
    CONSTRAINT request_wal_hot_pkey PRIMARY KEY (request_id, created_at)
) WITH (fillfactor=90);

CREATE TABLE IF NOT EXISTS request_wal_bodies (
    request_id character varying(64) NOT NULL,
    outbound_body text,
    compression_meta jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT request_wal_bodies_pkey PRIMARY KEY (request_id)
);

DROP VIEW IF EXISTS request_wal_with_current_month;
CREATE VIEW request_wal_with_current_month AS
SELECT * FROM request_wal_hot UNION ALL SELECT * FROM request_wal;

COMMIT;

\echo '✓ 表和视图创建完成'
EOSQL

echo ""
echo "==================================="
echo "重启154服务器上的网关..."
echo "==================================="
echo ""

ssh root@192.168.0.154 'systemctl restart llm-gateway && systemctl status llm-gateway | head -10'

echo ""
echo "==================================="
echo "修复完成！等待2分钟后验证..."
echo "==================================="
echo ""
echo "验证命令："
echo "ssh root@192.168.0.252 'sudo -u postgres psql -d llm_gateway -c \"SELECT COUNT(*), MAX(created_at) FROM request_wal_hot WHERE created_at > NOW() - INTERVAL '\"'\"'5 minutes'\"'\"';\"'"
