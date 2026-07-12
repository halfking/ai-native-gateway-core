#!/bin/bash

# 会话输出审计测试 - 一键执行脚本

set -e

echo "=== 会话输出审计测试 - 一键执行 ==="
echo ""

# 1. 检查环境
echo "[1/5] 检查环境..."
if ! command -v go &> /dev/null; then
    echo "❌ Go 未安装"
    exit 1
fi

if ! command -v psql &> /dev/null; then
    echo "⚠️  psql 未安装，跳过数据库初始化"
else
    echo "✅ psql 已安装"
fi

# 2. 检查配置文件
echo ""
echo "[2/5] 检查配置文件..."
if [ ! -f "02_sensitive_words_test.yaml" ]; then
    echo "❌ 配置文件 02_sensitive_words_test.yaml 不存在"
    exit 1
fi
echo "✅ 配置文件存在"

# 3. 初始化数据库表（如果需要）
echo ""
echo "[3/5] 初始化数据库表..."
DB_HOST="172.16.2.210"
DB_PORT="5432"
DB_NAME="llm_gateway"
DB_USER="llm_gateway"
export PGPASSWORD="4Q92cFTaYY8Z3AO07XTBBH-1g7kceaxg"

if command -v psql &> /dev/null; then
    echo "  正在创建测试表..."
    psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f schema.sql 2>&1 | grep -v "already exists" || true
    echo "✅ 数据库表已准备"
else
    echo "⚠️  跳过数据库初始化"
fi

# 4. 编译测试程序
echo ""
echo "[4/5] 编译测试程序..."
cd ../..
go build -o tests/session_audit/audit_test tests/session_audit/audit_test_all_in_one.go
cd tests/session_audit
echo "✅ 编译完成"

# 5. 运行测试
echo ""
echo "[5/5] 运行测试..."
echo "────────────────────────────────────────"
./audit_test
echo "────────────────────────────────────────"

echo ""
echo "✅ 测试完成！"
echo ""
echo "📊 查看结果："
echo "  psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME"
echo "  SELECT * FROM v_audit_performance_summary ORDER BY total_tests DESC LIMIT 5;"
echo ""
echo "📈 详细分析："
echo "  SELECT test_run_id, avg_latency_ms, p95_latency_ms, p99_latency_ms,"
echo "         decision_pass, decision_warn, decision_approval,"
echo "         threat_injection, threat_pii, threat_jailbreak"
echo "  FROM audit_test_runs ORDER BY started_at DESC LIMIT 1;"
