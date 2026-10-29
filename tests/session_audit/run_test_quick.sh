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

# 会话输出审计测试 - 快速测试模式（小数据量）

set -e

echo "=== 会话输出审计测试 - 快速测试模式 ==="
echo ""
echo "⚠️  使用测试模式：只提取 100 条数据进行快速验证"
echo ""

# 设置测试限制
export TEST_LIMIT=100

# 检查环境
echo "[1/5] 检查环境..."
if ! command -v go &> /dev/null; then
    echo "❌ Go 未安装"
    exit 1
fi
echo "✅ Go 已安装"

# 检查配置文件
echo ""
echo "[2/5] 检查配置文件..."
if [ ! -f "02_sensitive_words_test.yaml" ]; then
    echo "❌ 配置文件不存在"
    exit 1
fi
echo "✅ 配置文件存在"

# 初始化数据库表
echo ""
echo "[3/5] 初始化数据库表..."
DB_HOST="172.16.2.210"
DB_PORT="5432"
DB_NAME="llm_gateway"
DB_USER="llm_gateway"
: "${PGPASSWORD:?PGPASSWORD must be set for session audit database initialization}"

if command -v psql &> /dev/null; then
    echo "  正在测试数据库连接..."
    if timeout 5 psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c "SELECT 1;" > /dev/null 2>&1; then
        echo "  ✅ 数据库连接成功"
        echo "  正在创建测试表..."
        psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -f schema.sql 2>&1 | grep -v "already exists" || true
        echo "✅ 数据库表已准备"
    else
        echo "  ⚠️  数据库连接超时，跳过表初始化"
        echo "  提示：如果是首次运行，请手动执行："
        echo "    psql -h $DB_HOST -p $DB_PORT -U $DB_USER -d $DB_NAME -f schema.sql"
    fi
else
    echo "⚠️  psql 未安装，跳过数据库初始化"
fi

# 编译测试程序
echo ""
echo "[4/5] 编译测试程序..."
cd ../..
go build -o tests/session_audit/audit_test tests/session_audit/audit_test_all_in_one.go
cd tests/session_audit
echo "✅ 编译完成"

# 运行测试
echo ""
echo "[5/5] 运行快速测试（100 条数据）..."
echo "────────────────────────────────────────"
timeout 300 ./audit_test || {
    exit_code=$?
    if [ $exit_code -eq 124 ]; then
        echo "❌ 测试超时（5分钟）"
        exit 1
    else
        echo "❌ 测试失败，退出码: $exit_code"
        exit $exit_code
    fi
}
echo "────────────────────────────────────────"

echo ""
echo "✅ 快速测试完成！"
echo ""
echo "💡 如果要运行完整测试（10,000 条数据），请使用："
echo "  unset TEST_LIMIT"
echo "  ./run_test.sh"
