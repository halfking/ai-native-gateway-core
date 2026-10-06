#!/usr/bin/env bash
# 变异验证：migration 837 —— 摘掉 routing analytics 物化视图里的 NOW() AS refreshed_at
# 规则：还原一律用 cp 文件副本，禁止 git checkout --（会静默丢弃未提交工作）
set -uo pipefail
cd "$(dirname "$0")/.."

GATE='TestMigration837'
BAK="$(mktemp -d)/mv837"
mkdir -p "$BAK"
FILES=(
  "sql/migrations/startup/837_routing_mv_refresh_state.sql"
  "db/db.go"
  "bg/materialized_view_refresher.go"
  "admin/analytics_materialized.go"
)
for f in "${FILES[@]}"; do
  mkdir -p "$BAK/$(dirname "$f")"
  cp "$f" "$BAK/$f"
done

restore() { for f in "${FILES[@]}"; do cp "$BAK/$f" "$f"; done; }
trap restore EXIT

fail=0

mutate() {
  local name="$1" target="$2" pyexpr="$3"
  echo "----------------- $name -----------------"
  restore
  MD5_BEFORE=$(md5 -q "$target")
  python3 - "$target" <<PY
import sys
p = sys.argv[1]
t = open(p).read()
$pyexpr
open(p, 'w').write(t)
PY
  if diff -q "$BAK/$target" "$target" >/dev/null; then
    echo "  FAIL 变异没施上（字节没变）—— 量具问题"; fail=1; return
  fi
  echo "  变异已施上（$(diff "$BAK/$target" "$target" | grep -c '^[<>]') 行差异）"
  if go test ./sql/migrations/startup/ -run "$GATE" -count=1 >/tmp/mut837.out 2>&1; then
    echo "  FAIL 门仍然全绿 —— 判据无牙"; fail=1
    grep -E -- '--- (FAIL|PASS): TestMigration837' /tmp/mut837.out | head -12
  else
    echo "  PASS 门转红："
    grep -E -- '--- FAIL: TestMigration837' /tmp/mut837.out | head -6
  fi
  restore
  if [ "$(md5 -q "$target")" != "$MD5_BEFORE" ]; then
    echo "  FAIL 本轮还原失败（md5 漂移）"; fail=1
  fi
}

SQLF=sql/migrations/startup/837_routing_mv_refresh_state.sql

mutate "M72 7d 视图把 NOW() AS refreshed_at 加回目标列表" "$SQLF" "
t = t.replace('''  COALESCE(SUM(cost_usd), 0) AS total_cost_usd
FROM public.routing_analytics_source''', '''  COALESCE(SUM(cost_usd), 0) AS total_cost_usd,
  NOW() AS refreshed_at
FROM public.routing_analytics_source''')
"

mutate "M73 auto_request_count 谓词退回 IS NOT TRUE（自动请求计数静默归零）" "$SQLF" "
t = t.replace('''  COUNT(*) FILTER (WHERE is_auto_request = TRUE) AS auto_request_count,''', '''  COUNT(*) FILTER (WHERE is_auto_request IS NOT TRUE) AS auto_request_count,''')
"

mutate "M74 去掉 ensure 快路径的反向判据（旧视图永不重建）" db/db.go "
t = t.replace('''\t\t   AND POSITION('refreshed_at' IN COALESCE(pg_get_viewdef(to_regclass('public.routing_analytics_7d'), true), '')) = 0
''', '', 1)
"

mutate "M75 execRefresh 先打戳再 REFRESH（刷新失败也报新鲜）" bg/materialized_view_refresher.go "
t = t.replace('''\tif _, err := conn.Exec(ctx, \"REFRESH MATERIALIZED VIEW CONCURRENTLY \"+viewName); err != nil {
\t\treturn err
\t}
\tif _, err := conn.Exec(ctx, dbpkg.StampRoutingMVRefreshSQL, viewName); err != nil {''', '''\tif _, err := conn.Exec(ctx, dbpkg.StampRoutingMVRefreshSQL, viewName); err != nil {
\t\treturn fmt.Errorf(\"stamp first: %w\", err)
\t}
\tif _, err := conn.Exec(ctx, \"REFRESH MATERIALIZED VIEW CONCURRENTLY \"+viewName); err != nil {''')
"

mutate "M76 新鲜度闸退回 MAX(refreshed_at) FROM 视图（列已不存在）" admin/analytics_materialized.go "
t = t.replace('''		SELECT
			(SELECT refreshed_at FROM routing_mv_refresh_state WHERE view_name = \$1),
			EXISTS (SELECT 1 FROM pg_matviews WHERE schemaname = 'public' AND matviewname = \$1)
	\`, view).Scan(&refreshedAt, &viewExists); err != nil {''', '''		_ = viewExists
		refreshedAt = nil
		err := error(nil)
		_ = fmt.Sprintf(\`SELECT MAX(refreshed_at) FROM %s\`, view)''')
"

mutate "M77 只删 staleDefinition 块里的反向判据（快路径仍留着）" db/db.go "
t = t.replace('''
					AND POSITION('refreshed_at' IN COALESCE(pg_get_viewdef(to_regclass('public.routing_analytics_7d'), true), '')) = 0
					AND POSITION('refreshed_at' IN COALESCE(pg_get_viewdef(to_regclass('public.routing_audit_summary_7d'), true), '')) = 0
''', '', 1)
"

echo
if [ "$fail" -eq 0 ]; then
  echo "===== 全部变异均被门抓到 ====="
else
  echo "===== 存在无牙判据或还原失败 ====="
fi
exit $fail
