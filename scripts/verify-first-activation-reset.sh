#!/usr/bin/env bash
# scripts/verify-first-activation-reset.sh —— 线上验收：首次激活的「重置数据」一步 + 面板默认重置
#
# 验四件事（都用**线上真值**，不看源码）：
#   ① 公网版本身份 = 本轮提交号（部署确实生效）
#   ② 前端产物含本轮新键（面板默认重置提示 / 全选全不选 / 向导重置步骤）
#   ③ 数据初始化三端点仍通（catalog 200，预览签发令牌，执行缺令牌被拒）
#   ④ readyz 绿
set -uo pipefail
BASE="https://llm.itestu.cn"
EXPECT_SHA="${1:-ef71c74a6}"
FAIL=0
ok()  { echo "  v $1"; }
bad() { echo "  x $1"; FAIL=$((FAIL+1)); }

echo "① 公网版本身份"
VER="$(curl -s --max-time 20 "$BASE/api/system/version")"
echo "   $VER"
if printf '%s' "$VER" | grep -q "$EXPECT_SHA"; then ok "版本身份含 $EXPECT_SHA"; else bad "版本身份不含 ${EXPECT_SHA}（部署未生效？）"; fi

echo "② readyz"
RD="$(curl -s --max-time 20 "$BASE/readyz")"
echo "   $RD"
printf '%s' "$RD" | grep -q '"status":"ready"' && ok "readyz ready" || bad "readyz 非 ready"

echo "③ 前端产物含本轮新键"
echo "   （在 211 上部署引擎播种的 admin SPA 产物里找默认重置提示 / 全选全不选 / 向导重置步骤）"
DIST="/opt/kaixuan/data/llm-gateway/web/a/dist"
for KEY in 'defaultHint' 'selectAll' 'selectNone' 'wizard.stepLabel'; do
  HIT="$(ssh 211 "grep -rl '$KEY' $DIST 2>/dev/null | head -2" | head -1)"
  if [ -n "$HIT" ]; then ok "产物含键 ${KEY}（${HIT}）"; else bad "产物里找不到键 $KEY"; fi
done

echo "④ 数据初始化端点"
JWT="$(cat /tmp/lgw-jwt.txt 2>/dev/null)"
if [ -z "$JWT" ]; then
  bad "没有可用 JWT（/tmp/lgw-jwt.txt 缺失或过期）—— ③ 之后的端点验收未执行"
else
  CODE="$(curl -s -o /tmp/cat.json -w '%{http_code}' --max-time 60 -H "Authorization: Bearer $JWT" \
    "$BASE/api/admin/data-lifecycle/data-init/catalog")"
  echo "   catalog HTTP=$CODE"
  if [ "$CODE" = "200" ]; then
    ok "catalog 200"
    python3 - <<'PY'
import json
d = json.load(open('/tmp/cat.json'))
cats = d.get('categories') or []
print(f"   类别数={len(cats)} 缺表={[c.get('missing_tables') for c in cats if c.get('missing_tables')]}")
for c in cats:
    print(f"     {c['key']:<18} rows={c.get('rows'):<9} protected={c.get('protected_rows')}")
PY
  else
    bad "catalog 不是 200"
  fi

  CODE="$(curl -s -o /tmp/prev.json -w '%{http_code}' --max-time 60 -X POST -H "Authorization: Bearer $JWT" \
    -H "Content-Type: application/json" -d '{"categories":["request_records"]}' \
    "$BASE/api/admin/data-lifecycle/data-init/preview")"
  echo "   preview HTTP=$CODE"
  if [ "$CODE" = "200" ] && grep -q 'confirm_token' /tmp/prev.json; then
    ok "preview 200 且签发令牌"
  else
    bad "preview 未签发令牌"
  fi

  CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 60 -X POST -H "Authorization: Bearer $JWT" \
    -H "Content-Type: application/json" -d '{"categories":["request_records"]}' \
    "$BASE/api/admin/data-lifecycle/data-init/execute")"
  echo "   execute(缺令牌) HTTP=$CODE"
  [ "$CODE" = "400" ] && ok "缺令牌被拒（400）" || bad "缺令牌未按预期被拒（HTTP=${CODE}）"
fi

echo
echo "=== 线上验收：失败 ${FAIL} 项 ==="
[[ ${FAIL} -eq 0 ]] || exit 1
exit 0
