#!/bin/bash
# =============================================================================
# scripts/mut-upstream-port-binding.sh — 变异台：证明「upstream 端口」这条
# 事实链的诸门承重，不是恒绿。
#
#   门① tests/nginx_upstream_port_test.sh   （共享脚本 + 只读体检的行为门）
#   门② tests/deploy_211_contract_test.sh  （引擎接线门）
#
# ★ 与 scripts/mut-nginx-realip-patch.sh 同款：**本台不改工作区**。
#   把被测文件**复制到临时目录**再变异 ⇒ 与并发会话零冲突、结构上不可能留残留；
#   代价是门必须支持「用环境变量指定被测文件路径」（两者都备好了：
#   门①读 NGINX_UPSTREAM_PORT_HELPER / DEPLOY_DOCTOR，门②读 DEPLOY_211_ENGINE）。
#
# 纪律：先跑 CONTROL（未变异必须全绿）；「门没跑起来」= **作废**不是绿；
#       锚点 0 命中 = 变异过期 = **作废**。
#
# 用法：bash scripts/mut-upstream-port-binding.sh
# =============================================================================
set -uo pipefail
cd "$(dirname "$0")/.."

ROOT="$PWD"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

HELPER_REL="scripts/ops/nginx-upstream-port.py"
ENGINE_REL="scripts/deploy-seamless-docker.sh"
GATE_PORT="tests/nginx_upstream_port_test.sh"
GATE_CONTRACT="tests/deploy_211_contract_test.sh"

PASS=0; MISS=0; INVALID=0
MUT_HELPER="$ROOT/$HELPER_REL"
MUT_ENGINE="$ROOT/$ENGINE_REL"
MUT_DOCTOR="$ROOT/scripts/ops/deploy-doctor.sh"

state() {
  # state <输出> <退出码> <结论行正则>
  local out="$1" rc="$2" pat="$3"
  if ! grep -qE "$pat" <<<"$out"; then echo invalid; return 2; fi
  if [ "$rc" -ne 0 ]; then echo red; return 0; fi
  echo green; return 1
}

run_gate_port() {
  local out rc st
  out="$(NGINX_UPSTREAM_PORT_HELPER="$MUT_HELPER" DEPLOY_DOCTOR="$MUT_DOCTOR" \
         bash "$GATE_PORT" 2>&1)"; rc=$?
  st="$(state "$out" "$rc" 'nginx upstream port: [0-9]+ 通过')"
  printf '%s\n' "$out" | grep -E '^\s+FAIL' | head -3
  echo "STATE=$st"
}

run_gate_contract() {
  local out rc st
  out="$(DEPLOY_211_ENGINE="$MUT_ENGINE" bash "$GATE_CONTRACT" 2>&1)"; rc=$?
  st="$(state "$out" "$rc" '结果: [0-9]+ 通过')"
  printf '%s\n' "$out" | grep -E 'FAIL' | head -3
  echo "STATE=$st"
}

# ── 变异：复制 → 改副本 → 跑门 → 判红 → 校验工作区逐字节未变 ─────────
mutate() {
  # mutate <标签> <被测相对路径> <门: port|contract> <旧文本> <新文本>
  local label="$1" rel="$2" which="$3" old="$4" new="$5"
  local src="$ROOT/$rel" copy="$WORK/$(basename "$rel").mut"
  local before after st out

  MUT_HELPER="$ROOT/$HELPER_REL"
  MUT_ENGINE="$ROOT/$ENGINE_REL"
  MUT_DOCTOR="$ROOT/scripts/ops/deploy-doctor.sh"
  case "$rel" in
    "$HELPER_REL") MUT_HELPER="$copy" ;;
    scripts/ops/deploy-doctor.sh) MUT_DOCTOR="$copy" ;;
    *) MUT_ENGINE="$copy" ;;
  esac

  before="$(md5 -q "$src")"
  cp "$src" "$copy"
  if ! python3 - "$copy" "$old" "$new" <<'PY'
import sys
path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(path, encoding="utf-8").read()
if old not in s:
    print("MUTATION-ANCHOR-NOT-FOUND")
    raise SystemExit(3)
open(path, "w", encoding="utf-8").write(s.replace(old, new, 1))
PY
  then
    echo "  ★ 锚点 0 命中（变异可能已过期）⇒ 作废"
    INVALID=$((INVALID+1))
    return
  fi

  if [[ "$which" == "port" ]]; then
    out="$(run_gate_port)"
  else
    out="$(run_gate_contract)"
  fi
  st="$(grep -m1 '^STATE=' <<<"$out" | cut -d= -f2-)"

  after="$(md5 -q "$src")"
  if [[ "$before" != "$after" ]]; then
    echo "  ★ 工作区被改动了（before=${before} after=${after}）⇒ 异常，计作废"
    INVALID=$((INVALID+1))
    return
  fi

  case "$st" in
    red)   echo "  ✓ 被抓住（$which 门转红）"; PASS=$((PASS+1)) ;;
    green) echo "  ✗ 没被抓住（$which 门仍绿）"; MISS=$((MISS+1)) ;;
    *)     echo "  ★ 作废：门没跑起来"; INVALID=$((INVALID+1)) ;;
  esac
}

echo "=========== CONTROL（未变异，两道门必须全绿）==========="
c1="$(run_gate_port)"; echo "$c1"
[[ "$(grep -m1 '^STATE=' <<<"$c1" | cut -d= -f2-)" == "green" ]] || {
  echo "!! CONTROL 不绿 ⇒ 门本身有问题，不能用来判变异"; exit 1; }
c2="$(run_gate_contract)"; echo "$c2"
[[ "$(grep -m1 '^STATE=' <<<"$c2" | cut -d= -f2-)" == "green" ]] || {
  echo "!! CONTROL 不绿 ⇒ 门本身有问题，不能用来判变异"; exit 1; }

# ── M1：掩码失效（不剥注释）⇒ 读侧会读到注释里的旧端口 ─────────────
echo; echo "=========== M1: 掩码失效（读侧读到注释里的旧端口）==========="
mutate "M1" "$HELPER_REL" port \
"        if ch == '#':" \
"        if ch == chr(0):"

# ── M2：写侧定位不剥注释 ⇒ 改到注释里那个端口 ───────────────────────
echo; echo "=========== M2: 写侧不剥注释（改的是注释不是活的上游行）==========="
mutate "M2" "$HELPER_REL" port \
"    for lm in re.finditer(r'[^\\n]*', masked[body_start:body_end]):" \
"    for lm in re.finditer(r'[^\\n]*', text[body_start:body_end]):"

# ── M3：backup 行不再跳过 ⇒ 可能把备份上游当成主上游 ───────────────
echo; echo "=========== M3: backup 行不跳过 ============"
mutate "M3" "$HELPER_REL" port \
"    for line in body.split('\\n'):
        if 'backup' in line:
            continue" \
"    for line in body.split('\\n'):
        if False:
            continue"

# ── M4：医生把「脱节」判成「OK」⇒ 502 形态被放过 ───────────────────
echo; echo "=========== M4: 医生把脱节判成 OK ============"
mutate "M4" "scripts/ops/deploy-doctor.sh" port \
'  return 2
}' \
'  return 0
}'

# ── M5：引擎去掉「绑定体检」的调用 ⇒ 脱节时照常切流/停槽 ──────────
echo; echo "=========== M5: 引擎去掉绑定体检调用 ============"
mutate "M5" "$ENGINE_REL" contract \
'dd_check_slot_binding
' \
''

# ── M6：引擎把 upstream 端口读写改回内嵌正则（不剥注释）────────────
echo; echo "=========== M6: 引擎改回内嵌（不剥注释的）端口读取 ============"
mutate "M6" "$ENGINE_REL" contract \
'  python3 "$_helper" --conf "$_conf" --upstream "$DD_NGINX_UPSTREAM" 2>/dev/null' \
'  python3 - "$_conf" <<'"'"'PY'"'"' 2>/dev/null
import re, sys
t = open(sys.argv[1], encoding="utf-8").read()
m = re.search(r"server\s+127\.0\.0\.1:(\d+)", t)
print(m.group(1) if m else "")
PY'

echo
echo "=========== 变异结果：$PASS 被抓住 / $MISS 没被抓住 / $INVALID 作废 ==========="
[[ "$MISS" -eq 0 ]] && [[ "$INVALID" -eq 0 ]] && [[ "$PASS" -gt 0 ]]
