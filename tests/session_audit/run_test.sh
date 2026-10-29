#!/bin/bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi

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
: "${PGPASSWORD:?PGPASSWORD must be set for session audit database initialization}"

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
