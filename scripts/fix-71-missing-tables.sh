#!/bin/bash
# LLM Gateway 71服务器缺失表快速修复脚本
# 日期: 2026-06-30
# 问题: request_wal 和 request_logs 表完全缺失
#
# ⚠️  WARNING: This script modifies PRODUCTION database (71)
# ⚠️  Requires DBA approval and change request ID
# ⚠️  See docs/DATABASE-ENVIRONMENT-SEPARATION.md for details

set -e

# 环境确认
echo "=========================================="
echo "⚠️  PRODUCTION ENVIRONMENT WARNING"
echo "=========================================="
echo "This script will modify database on 71 PRODUCTION server"
echo ""
echo "Before proceeding, you must have:"
echo "  1. DBA approval"
echo "  2. Change request ID"
echo "  3. Backup verification"
echo ""
read -p "Enter your change request ID (or 'cancel' to abort): " APPROVAL_ID
if [ -z "$APPROVAL_ID" ] || [ "$APPROVAL_ID" == "cancel" ]; then
    echo "❌ Operation cancelled - no approval ID provided"
    exit 1
fi
echo "Change request ID: $APPROVAL_ID"
echo ""
read -p "Type 'CONFIRM' to proceed with production modification: " CONFIRM
if [ "$CONFIRM" != "CONFIRM" ]; then
    echo "❌ Operation cancelled - confirmation not received"
    exit 1
fi

# 记录审计日志
AUDIT_LOG="fix-71-tables-$(date +%Y%m%d-%H%M%S).log"
exec > >(tee -a "$AUDIT_LOG")
exec 2>&1
echo "Audit log: $AUDIT_LOG"
echo "Timestamp: $(date -Iseconds)"
echo "Operator: $(whoami)"
echo "Approval ID: $APPROVAL_ID"
echo "=========================================="
echo ""

SERVER="${SERVER:-__HOST_71_IP__}"
PORT="${PORT:-25022}"
USER="${USER:-root}"
# SSHPASS must be set in the environment before running this script.
# Example: SSHPASS='xxx' bash scripts/fix-71-missing-tables.sh
if [ -z "${SSHPASS:-}" ]; then
  echo "ERROR: SSHPASS environment variable is not set. Export it before running." >&2
  exit 1
fi

DB_CONTAINER="${DB_CONTAINER:-llm-gateway-pg-71-replica}"
DB_USER="${DB_USER:-llm_gateway}"
DB_NAME="${DB_NAME:-llm_gateway}"

echo "================================================"
echo "LLM Gateway 71服务器缺失表修复脚本"
echo "================================================"
echo "服务器: $SERVER:$PORT"
echo "数据库容器: $DB_CONTAINER"
echo "数据库: $DB_NAME"
echo ""

# 步骤1: 备份现有数据
echo "[1/6] 备份现有数据..."
sshpass -e ssh -p $PORT -o StrictHostKeyChecking=no $USER@$SERVER \
  "docker exec $DB_CONTAINER pg_dump -U $DB_USER -d $DB_NAME -Fc -f /tmp/crm_backup_$(date +%Y%m%d_%H%M%S).dump"
echo "✓ 备份完成"
echo ""

# 步骤2: 验证表是否缺失
echo "[2/6] 验证表状态..."
EXISTING_TABLES=$(sshpass -e ssh -p $PORT -o StrictHostKeyChecking=no $USER@$SERVER \
  "docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME -t -c \"SELECT count(*) FROM pg_tables WHERE tablename LIKE 'request%'\"" | tr -d ' ')

echo "现有 request* 表数量: $EXISTING_TABLES"

if [ "$EXISTING_TABLES" -gt 0 ]; then
    echo "⚠️  检测到已存在 request 相关表，跳过创建"
    echo "如需重新创建，请先手动删除："
    echo "  DROP TABLE IF EXISTS request_wal CASCADE;"
    echo "  DROP TABLE IF EXISTS request_logs CASCADE;"
    exit 0
fi
echo "✓ 确认需要创建表"
echo ""

# 步骤3: 创建 request_wal 表
echo "[3/6] 创建 request_wal 表..."
sshpass -e ssh -p $PORT -o StrictHostKeyChecking=no $USER@$SERVER bash << 'EOSSH'
docker exec -i llm-gateway-pg-71-replica psql -U llm_gateway -d crm << 'EOSQL'

-- 创建 request_wal 主表（分区表）
CREATE TABLE IF NOT EXISTS request_wal (
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
    compression_meta jsonb
) PARTITION BY RANGE (created_at);

-- 创建6月分区
CREATE TABLE IF NOT EXISTS request_wal_2026_06 PARTITION OF request_wal
    FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00');

-- 创建7月分区
CREATE TABLE IF NOT EXISTS request_wal_2026_07 PARTITION OF request_wal
    FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');

-- 创建索引
CREATE INDEX IF NOT EXISTS idx_request_wal_request_id 
    ON request_wal (request_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_request_wal_tenant_ts 
    ON request_wal (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_request_wal_status 
    ON request_wal (status, created_at DESC) 
    WHERE status = 'pending';

EOSQL
EOSSH

echo "✓ request_wal 表创建完成"
echo ""

# 步骤4: 创建 request_wal_bodies 表
echo "[4/6] 创建 request_wal_bodies 表..."
sshpass -e ssh -p $PORT -o StrictHostKeyChecking=no $USER@$SERVER bash << 'EOSSH'
docker exec -i llm-gateway-pg-71-replica psql -U llm_gateway -d crm << 'EOSQL'

CREATE TABLE IF NOT EXISTS request_wal_bodies (
    request_id character varying(64) PRIMARY KEY,
    outbound_body jsonb,
    compression_meta jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_request_wal_bodies_created 
    ON request_wal_bodies (created_at DESC);

EOSQL
EOSSH

echo "✓ request_wal_bodies 表创建完成"
echo ""

# 步骤5: 创建 request_logs 表（简化版，完整版需要从 01-schema.sql 导入）
echo "[5/6] 创建 request_logs 表（简化版）..."
sshpass -e ssh -p $PORT -o StrictHostKeyChecking=no $USER@$SERVER bash << 'EOSSH'
docker exec -i llm-gateway-pg-71-replica psql -U llm_gateway -d crm << 'EOSQL'

-- 创建序列
CREATE SEQUENCE IF NOT EXISTS request_logs_id_seq;

-- 创建主表（分区表）
CREATE TABLE IF NOT EXISTS request_logs (
    id bigint DEFAULT nextval('request_logs_id_seq'::regclass) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_interrupted boolean,
    stream_done_sent boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb
) PARTITION BY RANGE (ts);

-- 创建6月分区
CREATE TABLE IF NOT EXISTS request_logs_2026_06 PARTITION OF request_logs
    FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00');

-- 创建7月分区
CREATE TABLE IF NOT EXISTS request_logs_2026_07 PARTITION OF request_logs
    FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');

-- 创建关键索引
CREATE INDEX IF NOT EXISTS idx_request_logs_ts 
    ON request_logs (ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_request_id 
    ON request_logs (request_id);
CREATE INDEX IF NOT EXISTS idx_request_logs_credential_ts 
    ON request_logs (credential_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_tenant_ts 
    ON request_logs (tenant_id, ts DESC);

EOSQL
EOSSH

echo "✓ request_logs 表创建完成"
echo ""

# 步骤6: 验证表创建
echo "[6/6] 验证表创建..."
echo ""
echo "表列表:"
sshpass -e ssh -p $PORT -o StrictHostKeyChecking=no $USER@$SERVER \
  "docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME -c '\dt request*'"

echo ""
echo "分区统计:"
TABLES=$(sshpass -e ssh -p $PORT -o StrictHostKeyChecking=no $USER@$SERVER \
  "docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME -t -c \"SELECT tablename FROM pg_tables WHERE tablename LIKE 'request%' ORDER BY tablename\"")

echo "$TABLES"
TABLE_COUNT=$(echo "$TABLES" | wc -l | tr -d ' ')
echo ""
echo "总计: $TABLE_COUNT 个表"
echo ""

# 验证关键函数
echo "验证 recent_success_rate() 函数..."
FUNC_EXISTS=$(sshpass -e ssh -p $PORT -o StrictHostKeyChecking=no $USER@$SERVER \
  "docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME -t -c \"SELECT count(*) FROM pg_proc WHERE proname = 'recent_success_rate'\"" | tr -d ' ')

if [ "$FUNC_EXISTS" -gt 0 ]; then
    echo "✓ recent_success_rate() 函数已存在"
else
    echo "⚠️  recent_success_rate() 函数不存在，路由可能仍有问题"
    echo "   请执行 sql/migrations/startup/035_routing_recent_success_rate.sql"
fi

echo ""
echo "================================================"
echo "✓ 修复完成！"
echo "================================================"
echo ""
echo "下一步："
echo "1. 重启 llm-gateway-go 服务："
echo "   docker restart llm-gateway-go"
echo ""
echo "2. 观察日志确认写入成功："
echo "   docker logs -f llm-gateway-go 2>&1 | grep request_logger"
echo ""
echo "3. 查询新写入的数据："
echo "   docker exec $DB_CONTAINER psql -U $DB_USER -d $DB_NAME -c 'SELECT count(*) FROM request_wal'"
echo ""
