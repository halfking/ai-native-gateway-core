#!/usr/bin/env bash
# scripts/audit/verify-211-captcha-ttl.sh —— 「登录失败计数存活窗口」的**验收探针**
#
# ★ 它验的是什么（★ 2026-11-25 的机制结论，★ 逐行可查 ★）：
#   ① 失败计数是**纯内存、无 TTL**（admin/auth_captcha.go:217 `loginFailuresGlobal`）；
#   ② captcha 闸门挂在**任何 DB 查询与 bcrypt 之前**（admin/auth.go:229，★ 注释明写 ★）；
#      ⇒ `captcha_required` 时**走不到密码校验** ⇒ **成功不可达** ⇒ 计数**永不清**
#      （清零只在成功路径：auth_captcha.go:509 `clearLoginFailures`）；
#   ③ ⇒ 某 IP 连错到阈值（`CaptchaFailureThreshold=3`）后，**连正确口令都过不去**，
#      **直到网关进程重启**（★ 仓内无任何管理端点能重置它 ★★）。
#   ④ 候选实现（★ 分支 `wip/captcha-ttl-20261125`，★ **未合 main、未部署** ★）
#      给计数加**存活窗口**（`CaptchaFailureTTL = 15 * time.Minute`）⇒ **闸门会自愈**。
#
# ★ 两个相位（★ 判据不同、★ 别混用 ★）：
#   --phase baseline    ⇒ **当前构建（尚未加窗口）**：连错 3 次 ⇒ 用**正确口令**
#                          仍应回 `400 {"code":"captcha_required"}`
#                          ⇒ ★ 这是「**死锁确实在**」的判据（★ 期望成立 ⇒ 退出码 0 ★）
#   --phase after-ttl   ⇒ **已加窗口的构建**：连错 3 次 ⇒ 锁住 ⇒ 等过窗口 ⇒
#                          再用**正确口令**应当**不再**是 `captcha_required`（⇒ 自愈）
#                          ⇒ ★ 这是「**修好了**」的判据（★ 期望成立 ⇒ 退出码 0 ★）
#
# ★ 隔离（★ 硬要求 ★）：★ **所有**请求都带 `X-Forwarded-For: <bucket>`
#   （★ 默认 10.99.99.99 ★）⇒ 失败计数只落**隔离桶**
#   ⇒ ★ **真实用户桶不受影响**（★ 分桶依据 admin/auth.go:639 取 XFF **首段** ★★）
#   ⇒ ★ 本脚本**绝不**发不带 XFF 的登录请求 ⇒ 不会污染任何真实来源。
# ★ 口令只从环境变量读（`ADMIN_PASSWORD`，或仓根 `.env.local` 的
#   `LLM_GATEWAY_ADMIN_PASSWORD`），且**经 stdin 交给 curl** ⇒ 不出现在 `ps` 里、
#   脚本内不含任何凭据字面量。
# ★ 退出码：0 = 期望成立；1 = **期望不成立**（★ 缺陷仍在，或修复没生效 ★）；2 = 量具/用法问题。
#
# 用法:
#   bash scripts/audit/verify-211-captcha-ttl.sh --phase baseline
#   bash scripts/audit/verify-211-captcha-ttl.sh --phase after-ttl --ttl-seconds 900
set -uo pipefail

PHASE="baseline"
BASE="${LLM_GATEWAY_BASE_URL:-https://llm.itestu.cn}"
BUCKET="10.99.99.99"
USER_NAME="${ADMIN_USER:-admin}"
TTL_SECONDS=900          # after-ttl 相位要等的窗口长度（候选实现是 15 分钟）
MARGIN_SECONDS=120       # 再多等一会儿，避开边界抖动
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

while [ $# -gt 0 ]; do
  case "$1" in
    --phase) PHASE="${2:-}"; shift 2 ;;
    --url) BASE="${2:-}"; shift 2 ;;
    --bucket) BUCKET="${2:-}"; shift 2 ;;
    --user) USER_NAME="${2:-}"; shift 2 ;;
    --ttl-seconds) TTL_SECONDS="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) echo "★ 未知参数：$1" >&2; exit 2 ;;
  esac
done

case "$PHASE" in
  baseline|after-ttl) ;;
  *) echo "★ --phase 只能是 baseline 或 after-ttl（收到：${PHASE}）" >&2; exit 2 ;;
esac

# ── 口令（只从环境读；缺省从仓根 .env.local 取 LLM_GATEWAY_ADMIN_PASSWORD）────
if [ -z "${ADMIN_PASSWORD:-}" ]; then
  for f in "${REPO_ROOT}/.env.local" "${REPO_ROOT}/../llm-gateway-go/.env.local"; do
    [ -f "$f" ] || continue
    set -a
    # shellcheck disable=SC1090
    . "$f"
    set +a
    break
  done
fi
ADMIN_PASSWORD="${ADMIN_PASSWORD:-${LLM_GATEWAY_ADMIN_PASSWORD:-}}"
if [ -z "$ADMIN_PASSWORD" ]; then
  echo "★ 缺少口令：设 ADMIN_PASSWORD，或让仓根 .env.local 里有 LLM_GATEWAY_ADMIN_PASSWORD" >&2
  exit 2
fi

TMP="$(mktemp -t captcha-ttl.XXXXXX)"
trap 'rm -f "$TMP" "$TMP".*' EXIT

# ── HTTP 小工具（★ 口令经 stdin ⇒ 不进 ps ★）───────────────────────────────
# login <password> <label> ⇒ 打印 "<label> HTTP <code>"，并把响应体存到 $TMP.body
login() {
  local pw="$1" label="$2"
  printf '{"username":"%s","password":"%s"}' "$USER_NAME" "$pw" | \
    curl -sS -m 25 -o "$TMP.body" -w '%{http_code}' \
      -X POST "$BASE/api/auth/token" \
      -H 'Content-Type: application/json' \
      -H "X-Forwarded-For: ${BUCKET}" \
      --data-binary @- 2>/dev/null || echo 000
}

body_is() { grep -q "$1" "$TMP.body" 2>/dev/null; }

# captcha_state ⇒ 只读地问「这个桶现在被要求验证码吗」（★ 不产生失败计数 ★）
# ★ 输出只留 required 字段（★ 响应里还有验证码图片 data URI ⇒ 不打印 ★）
captcha_state() {
  local code
  code="$(curl -sS -m 20 -o "$TMP.cap" -w '%{http_code}' \
    "$BASE/api/auth/captcha" -H "X-Forwarded-For: ${BUCKET}" 2>/dev/null || echo 000)"
  printf '%s' "$code"
  printf ' %s' "$(python3 - "$TMP.cap" <<'PY' 2>/dev/null || true
import json, sys
try:
    d = json.load(open(sys.argv[1]))
except Exception:
    print("(响应不是 JSON)")
    raise SystemExit(0)
print("required=" + str(d.get("required", "(字段缺失)")))
PY
)"
}

echo "=== captcha 存活窗口验收探针 ==="
echo "目标      : $BASE"
echo "相位      : $PHASE"
echo "隔离桶    : $BUCKET   （★ 全部请求带 XFF ⇒ 真实用户桶不受影响 ★）"
echo "用户名    : $USER_NAME"
echo

st_line="$(captcha_state)"
echo "① 桶的初始状态（只读 GET /api/auth/captcha）⇒ HTTP ${st_line}"

# ── 相位：baseline（★ 当前构建：期望「连正确口令都被挡」 ★）────────────────
if [ "$PHASE" = "baseline" ]; then
  echo "② 连错 3 次（★ 隔离桶内 ★）"
  for i in 1 2 3; do
    code="$(login "wrong-${i}-probe" "wrong${i}")"
    echo "   第 ${i} 次错误口令 ⇒ HTTP ${code}"
  done
  echo "③ 用**正确口令**再试（★ 这一步是判据 ★）"
  code="$(login "$ADMIN_PASSWORD" "correct")"
  echo "   正确口令 ⇒ HTTP ${code}  $(head -c 120 "$TMP.body" 2>/dev/null)"
  if [ "$code" = "400" ] && body_is captcha_required; then
    echo
    echo "VERDICT: PASS（★ 期望成立：死锁确实在 —— 连正确口令都回 400 captcha_required ★）"
    echo "★ ⇒ 候选实现（分支 wip/captcha-ttl-20261125）**尚未生效**（★ 与「未部署」一致 ★）"
    exit 0
  fi
  echo
  echo "VERDICT: FAIL（★ 期望不成立：期望 400 captcha_required，实际 HTTP ${code} ★）"
  echo "★ 可能含义：① 该构建**已经**带存活窗口（⇒ 修复已上线 ⇒ 改跑 --phase after-ttl ★）"
  echo "★          ② 桶的计数已过期（⇒ 换个 --bucket 重跑 ★）"
  exit 1
fi

# ── 相位：after-ttl（★ 已加窗口的构建：期望「等过窗口后自愈」 ★）────────────
echo "② 连错 3 次（★ 隔离桶内 ★）"
for i in 1 2 3; do
  code="$(login "wrong-${i}-probe" "wrong${i}")"
  echo "   第 ${i} 次错误口令 ⇒ HTTP ${code}"
done
code="$(login "$ADMIN_PASSWORD" "correct")"
echo "③ 用正确口令（★ 此刻应当**被挡** ⇒ 证明窗口内是锁着的 ★）⇒ HTTP ${code}  $(head -c 120 "$TMP.body" 2>/dev/null)"
if [ "$code" != "400" ] || ! body_is captcha_required; then
  echo
  echo "VERDICT: FAIL（★ 期望不成立：窗口内应当仍被挡（400 captcha_required），实际 HTTP ${code} ★）"
  echo "★ 可能含义：① 桶的计数本来就没锁上（⇒ 换 --bucket 重跑 ★）② 阈值/判定口径变了"
  exit 1
fi

wait_secs=$((TTL_SECONDS + MARGIN_SECONDS))
echo "④ 等过存活窗口（${TTL_SECONDS}s ＋ 余量 ${MARGIN_SECONDS}s ＝ ${wait_secs}s）…"
sleep "$wait_secs"

st_line="$(captcha_state)"
echo "   窗口后桶的状态（只读）⇒ HTTP ${st_line}"
echo "⑤ 再用正确口令（★ 此刻应当**不再**是 captcha_required ★）"
code="$(login "$ADMIN_PASSWORD" "correct")"
echo "   正确口令 ⇒ HTTP ${code}  $(head -c 160 "$TMP.body" 2>/dev/null)"
if [ "$code" = "200" ]; then
  echo
  echo "VERDICT: PASS（★ 期望成立：等过窗口后正确口令**恢复可用** ⇒ 闸门会自愈 ★）"
  exit 0
fi
if [ "$code" = "400" ] && body_is captcha_required; then
  echo
  echo "VERDICT: FAIL（★ 期望不成立：等过窗口后仍回 400 captcha_required ⇒ **窗口没生效** ★）"
  exit 1
fi
echo
echo "VERDICT: FAIL（★ 期望不成立：期望 200，实际 HTTP ${code} —— ★ 响应前 160 字见上 ★）"
exit 1
