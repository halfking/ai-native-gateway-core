#!/usr/bin/env bash
# 部署后验收冒烟（211 / 245 / 154 通用）
#
# 用法:
#   ADMIN_PASSWORD='<口令>' bash scripts/audit/deploy-smoke-admin.sh <base_url>
#
# 检查面（全部只读，不改任何生产数据）：
#   ① /healthz 与 /readyz 探活（cmd/gateway/main.go:6873,6879 —— 两个路径共用
#      同一个 healthHandler ⇒ 两处返回码必须一致）
#   ② POST /api/auth/token 登录（admin/handler.go:995 —— 公开路由）
#      实测响应结构 = {access_token, expires_at, token_type, user}
#   ③ 带 Bearer token 调三个只读 admin API，验证**鉴权面真的通**
#      （401 说明路由命中但鉴权没过；200 才算通）
#   ④ 版本面 /api/system/bootstrap/status（公开）
#
# ★ 口令**只从环境变量读**（ADMIN_PASSWORD）⇒ 本文件不含任何凭据字面量，
#   可安全入库；调用方需自行保证口令不进 shell 历史（建议用 env -i 或 read -s）。
set -uo pipefail

BASE="${1:?用法: deploy-smoke-admin.sh <base_url>}"
U="${ADMIN_USER:-admin}"
P="${ADMIN_PASSWORD:-}"
if [[ -z "$P" ]]; then
  echo "★ 缺少 ADMIN_PASSWORD 环境变量（口令只从环境读，脚本内不含凭据）" >&2
  exit 2
fi

TMP="$(mktemp -t deploy-smoke.XXXXXX)"
trap 'rm -f "$TMP" "$TMP".*' EXIT
fail=0

echo "=== 目标: $BASE ==="

# ① 探活
for path in /healthz /readyz; do
  code=$(curl -sS -m 20 -o "$TMP.body" -w '%{http_code}' "$BASE$path" 2>/dev/null || echo 000)
  echo "  $path => $code"
  [[ "$code" == "200" ]] || fail=1
done

# ② 登录取 token（口令经 stdin 传给 python，避免出现在 ps 里）
code=$(printf '{"username":"%s","password":"%s"}' "$U" "$P" | \
  curl -sS -m 25 -o "$TMP.login" -w '%{http_code}' \
    -X POST "$BASE/api/auth/token" \
    -H 'Content-Type: application/json' \
    --data-binary @- 2>/dev/null || echo 000)
echo "  POST /api/auth/token => $code"
if [[ "$code" != "200" ]]; then
  echo "  ★ 登录失败，响应前 200 字："; head -c 200 "$TMP.login" 2>/dev/null; echo
  exit 1
fi

TOKEN=$(python3 - "$TMP.login" <<'PY'
import json, sys
try:
    d = json.load(open(sys.argv[1]))
except Exception:
    print(''); sys.exit()
for k in ('access_token', 'token', 'jwt'):
    v = d.get(k)
    if isinstance(v, str) and v:
        print(v); break
else:
    v = d.get('data')
    if isinstance(v, dict):
        for kk in ('access_token', 'token', 'jwt'):
            if isinstance(v.get(kk), str) and v[kk]:
                print(v[kk]); break
PY
)
if [[ -z "${TOKEN:-}" ]]; then
  echo "  ★ 登录 200 但未取到 access_token，响应前 200 字："; head -c 200 "$TMP.login" 2>/dev/null; echo
  exit 1
fi
echo "  access_token 长度: ${#TOKEN}"

# ③ 带 token 调只读 admin API
#   ★ 端点选取经**现役真机实测**（非推断）：
#     /api/credentials/monitor-summary  → 200（凭据侧读面）
#       （注意：/api/credentials/ 与 /api/credentials/heatmap 是**带必填查询参数**的
#        入口，缺参时返回 400 ⇒ 不能拿它们当冒烟目标，否则 400 会被误读成服务坏了）
#     /api/admin/topology/providers-today → 200
#     /api/admin/request-anomalies        → 200
for ep in /api/credentials/monitor-summary /api/admin/topology/providers-today /api/admin/request-anomalies; do
  code=$(curl -sS -m 20 -o "$TMP.api" -w '%{http_code}' \
    -H "Authorization: Bearer $TOKEN" "$BASE$ep" 2>/dev/null || echo 000)
  bytes=$(wc -c < "$TMP.api" 2>/dev/null | tr -d ' ')
  echo "  GET $ep => $code (bytes=${bytes:-0})"
  [[ "$code" == "200" ]] || fail=1
done

# ④ 版本面（公开端点）
code=$(curl -sS -m 20 -o "$TMP.ver" -w '%{http_code}' "$BASE/api/system/bootstrap/status" 2>/dev/null || echo 000)
ver=$(python3 - "$TMP.ver" <<'PY'
import json, sys
try:
    d = json.load(open(sys.argv[1]))
    for k in ('version', 'gateway_version', 'build'):
        if isinstance(d.get(k), str):
            print(d[k]); break
    else:
        print(json.dumps(d, ensure_ascii=False)[:160])
except Exception:
    print('')
PY
)
echo "  GET /api/system/bootstrap/status => $code version=${ver:-?}"

if [[ $fail -eq 0 ]]; then
  echo "=== 冒烟全部通过 ✓"
else
  echo "=== 冒烟有失败项 ✗"
fi
exit $fail
