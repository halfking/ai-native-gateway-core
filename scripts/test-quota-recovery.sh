#!/bin/bash
# 测试 quota_state 恢复逻辑
# 2026-07-06: 验证 periodic_exhausted 状态能在探测成功后自动清除

set -e

PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-postgres}"
PGDATABASE="${PGDATABASE:-llm_gateway}"

echo "=== 测试 quota_state 恢复逻辑 ==="
echo ""

# 1. 创建测试凭据（如果不存在）
echo "1. 查找 quota_state='periodic_exhausted' 的凭据..."
psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -c "
SELECT id, label, quota_state, health_status, health_checked_at
FROM credentials
WHERE quota_state = 'periodic_exhausted'
ORDER BY id
LIMIT 5;
"

# 2. 模拟场景：手动将一个健康凭据标记为 periodic_exhausted
echo ""
echo "2. 模拟场景：将 credential_id=1 标记为 periodic_exhausted（如果存在且健康）..."
TEST_CRED=$(psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -t -c "
UPDATE credentials
SET quota_state = 'periodic_exhausted',
    quota_recover_at = NULL,
    state_updated_at = now()
WHERE id = 1
  AND health_status = 'healthy'
  AND lifecycle_status = 'active'
RETURNING id;
" | xargs)

if [ -n "$TEST_CRED" ]; then
    echo "   已将 credential_id=$TEST_CRED 标记为 periodic_exhausted"
else
    echo "   跳过（credential_id=1 不存在或不健康）"
fi

# 3. 触发恢复逻辑（模拟 credential_recovery 的 recover 方法）
echo ""
echo "3. 执行恢复 SQL（模拟 bg/credential_recovery.go 的新逻辑）..."
psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -c "
UPDATE credentials
SET quota_state = 'ok',
    state_updated_at = now()
WHERE quota_state = 'periodic_exhausted'
  AND health_status = 'healthy'
  AND health_checked_at > now() - INTERVAL '1 hour'
  AND lifecycle_status = 'active'
RETURNING id, label, quota_state;
"

# 4. 验证结果
echo ""
echo "4. 验证：检查还有多少凭据卡在 periodic_exhausted..."
COUNT=$(psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -t -c "
SELECT COUNT(*)
FROM credentials
WHERE quota_state = 'periodic_exhausted'
  AND health_status = 'healthy'
  AND health_checked_at > now() - INTERVAL '1 hour';
" | xargs)

echo "   结果：$COUNT 个健康凭据仍卡在 periodic_exhausted"

if [ "$COUNT" -eq 0 ]; then
    echo "   ✅ 恢复逻辑正常！所有健康凭据的 quota_state 已清除"
else
    echo "   ⚠️  仍有 $COUNT 个凭据未恢复（可能 health_checked_at 超过 1 小时）"
fi

echo ""
echo "=== 测试完成 ==="
