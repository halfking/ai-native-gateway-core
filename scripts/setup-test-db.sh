#!/bin/bash
# ★★ 2026-11-23（211 配置优化执行报告 §9.24 轮 · N4 基线红收口）：★★ 本脚本在【宿主】上
# ★★ 编译 Go ⇒ ⇒ 子进程会往 Go 的构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】
# ★★ ⇒ ⇒ 必须指向【专用缓存】（★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；
# ★★ 理由与实测读数见 `internal/gocache` 的包注释 ⇒ ★ 实测：★ **单包** `go test ./admin/`
# ★★ 就要 **846 MB** ⇒ ⇒ 体积由【传递依赖闭包】决定 ⇒ ⇒ 按「编译范围」分层不成立）。
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
# Setup script for PostgreSQL test database

set -e

# Configuration
DB_NAME="${TEST_DB_NAME:-llm_gateway_test}"
DB_USER="${TEST_DB_USER:-postgres}"
DB_HOST="${TEST_DB_HOST:-localhost}"
DB_PORT="${TEST_DB_PORT:-5432}"

echo "Setting up test database: $DB_NAME"

# Check if PostgreSQL is running
if ! pg_isready -h "$DB_HOST" -p "$DB_PORT" > /dev/null 2>&1; then
    echo "ERROR: PostgreSQL is not running on $DB_HOST:$DB_PORT"
    echo ""
    echo "To start PostgreSQL:"
    echo "  macOS (Homebrew): brew services start postgresql@14"
    echo "  Linux: sudo systemctl start postgresql"
    echo "  Docker: docker run --name postgres-test -e POSTGRES_PASSWORD=postgres -p 5432:5432 -d postgres:14"
    exit 1
fi

# Create test database if it doesn't exist
echo "Creating database $DB_NAME (if not exists)..."
psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -tc "SELECT 1 FROM pg_database WHERE datname = '$DB_NAME'" | grep -q 1 || \
    psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -c "CREATE DATABASE $DB_NAME"

# Export connection string for tests
export TEST_DB_URL="postgres://$DB_USER@$DB_HOST:$DB_PORT/$DB_NAME?sslmode=disable"

echo ""
echo "✓ Test database ready"
echo ""
echo "Run tests with:"
echo "  export TEST_DB_URL=\"$TEST_DB_URL\""
echo "  go test -v ./plugin-runtime -run TestDBRegistry"
echo ""
echo "Or use Docker:"
echo "  docker run --name postgres-test -e POSTGRES_PASSWORD=postgres -p 5432:5432 -d postgres:14"
echo "  export TEST_DB_URL=\"postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable\""
echo "  go test -v ./plugin-runtime -run TestDBRegistry"
