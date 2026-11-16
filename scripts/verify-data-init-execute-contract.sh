#!/usr/bin/env bash
# scripts/verify-data-init-execute-contract.sh —— 线上验收：数据初始化三端点的契约 + 「没删任何生产数据」
#
# 验五件事（都用**线上真值**，不看源码）：
#   ① 公网版本身份含本轮提交号（部署确实生效）
#   ② readyz 绿
#   ③ 三端点契约：catalog 200（9 类零缺表）/ preview 200 且签发令牌 /
#      execute 缺令牌 400 / execute 篡改令牌 400 / execute 缺类别 400
#   ④ ★ **生产数据零删除**：验收前后对拍「配置类」表行数（users / tenants /
#      api_keys / providers / credentials / models_canonical）——
#      验收只打**拒绝路径**，本就不该删任何一行；对拍是把这个事实**量出来**。
#   ⑤ 一次性库零残留（隔离库测试在同一个 PG 集群上建过临时库）
#
# ★ 为什么 ④ 只对拍「配置类」表：运行数据类（请求/探测/路由记录）在验收窗口里
#   本来就**一直在被正常流量写入** ⇒ 它们的行数变化不能证明「谁删的」。
#   配置类表不会因流量变化 ⇒ 它们的行数不变才是有效证据。
#
# 用法: bash scripts/verify-data-init-execute-contract.sh <本轮提交号>
set -uo pipefail
BASE="https://llm.itestu.cn"
EXPECT_SHA="${1:-}"
FAIL=0
ok()  { echo "  v $1"; }
bad() { echo "  x $1"; FAIL=$((FAIL+1)); }

echo "① 公网版本身份"
VER="$(curl -s --max-time 20 "$BASE/api/system/version")"
echo "   $VER"
if [ -n "$EXPECT_SHA" ]; then
  printf '%s' "$VER" | grep -q "$EXPECT_SHA" && ok "版本身份含 $EXPECT_SHA" || bad "版本身份不含 ${EXPECT_SHA}（部署未生效？）"
else
  echo "   (未给提交号 ⇒ 跳过本项)"
fi

echo "② readyz"
RD="$(curl -s --max-time 20 "$BASE/readyz")"
echo "   $RD"
printf '%s' "$RD" | grep -q '"status":"ready"' && ok "readyz ready" || bad "readyz 非 ready"

echo "④ 前置：生产配置类表行数（验收前）"
COUNT_SQL="SELECT 'users='||(SELECT count(*) FROM public.users)
  ||' tenants='||(SELECT count(*) FROM public.tenants)
  ||' api_keys='||(SELECT count(*) FROM public.api_keys)
  ||' providers='||(SELECT count(*) FROM public.providers)
  ||' credentials='||(SELECT count(*) FROM public.credentials)
  ||' models_canonical='||(SELECT count(*) FROM public.models_canonical);"
BEFORE="$(ssh 211 "docker exec -i acc-pg17 psql -U acc_super -d llm_gateway -A -t -c \"$COUNT_SQL\"" 2>/dev/null | tr -d '[:space:]')"
echo "   before: $BEFORE"
if [ -z "$BEFORE" ]; then bad "取不到验收前的行数（对拍无法成立）"; fi

echo "③ 三端点契约（在 211 本机现签 JWT；密钥与 token 不出机器）"
PROBE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/audit/api-211-data-init-contract-probe.py"
ssh 211 'cat > /tmp/api-211-data-init-contract-probe.py' < "$PROBE" 2>/dev/null
PROBE_OUT="$(ssh 211 'python3 /tmp/api-211-data-init-contract-probe.py' 2>&1)"
ssh 211 'rm -f /tmp/api-211-data-init-contract-probe.py' 2>/dev/null
echo "$PROBE_OUT" | tail -1 > /tmp/lgw-di-probe.json
if ! grep -q '"checks"' /tmp/lgw-di-probe.json; then
  bad "探针没产出结果：$(echo "$PROBE_OUT" | tail -3)"
else
  python3 - <<'PY'
import json
doc = json.load(open('/tmp/lgw-di-probe.json'))
for c in doc['checks']:
    print(f"  {'v' if c['ok'] else 'x'} {c['name']}: {c.get('detail','')}")
s = doc['summary']
print(f"  → 失败 {s['failed']}/{s['total']}（活跃端口 {s['port']}）")
PY
  FAILED="$(python3 -c "import json;print(json.load(open('/tmp/lgw-di-probe.json'))['summary']['failed'])")"
  [ "$FAILED" = "0" ] && ok "三端点契约全部符合" || bad "三端点契约有 ${FAILED} 项不符合"
fi

echo "④ 后置：生产配置类表行数（验收后）"
AFTER="$(ssh 211 "docker exec -i acc-pg17 psql -U acc_super -d llm_gateway -A -t -c \"$COUNT_SQL\"" 2>/dev/null | tr -d '[:space:]')"
echo "   after:  $AFTER"
if [ -n "$BEFORE" ] && [ "$BEFORE" = "$AFTER" ]; then
  ok "配置类表行数前后逐字一致 ⇒ 本轮验收**没有删掉任何一行生产数据**"
else
  bad "配置类表行数变了：before=[$BEFORE] after=[$AFTER]"
fi

echo "⑤ 一次性库零残留"
LEFTOVER="$(ssh 211 "docker exec -i acc-pg17 psql -U acc_super -d llm_gateway -A -t -c \"SELECT count(*) FROM pg_database WHERE datname LIKE 'llm_gateway_t%' OR datname LIKE '%_t1%';\"" 2>/dev/null | tr -d '[:space:]')"
echo "   残留数: ${LEFTOVER:-查询失败}"
if [ "$LEFTOVER" = "0" ]; then ok "无一次性库残留"; else bad "有一次性库残留（${LEFTOVER}）"; fi

echo
echo "=== 线上验收：失败 ${FAIL} 项 ==="
[[ ${FAIL} -eq 0 ]] || exit 1
exit 0
