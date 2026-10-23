#!/bin/bash
# =============================================================================
# scripts/mut-nginx-realip-patch.sh — 变异台：证明这两道门承重，不是恒绿。
#
#   门① tests/nginx_llm_realip_patch_test.sh   （补丁器的**行为**门，夹具驱动）
#   门② tests/deploy_211_contract_test.sh      （引擎的**接线**门）
#
# ★ 与 scripts/mut-r53-role-fallback.sh 的关键差别：**本台不改工作区**。
#   那个台直接改源码文件，跑动期间会与并发会话抢地盘（还原动作可能把别人的
#   在途编辑一起抹掉）。本台把被测文件**复制到临时目录**再变异：
#     · 工作区永远逐字节不变（每条变异后校验原路径 md5）；
#     · 与并发会话零冲突；
#     · 没有「变异残留」的可能。
#   ⇒ 代价：门必须支持「用环境变量指定被测文件路径」——
#     门①读 NGINX_REALIP_PATCH，门②读 DEPLOY_211_ENGINE（两者都已在门里备好）。
#
# 纪律（来自既往踩坑）：
#   · **先跑 CONTROL**（未变异必须全绿），否则无法区分「门红」与「变异残留」；
#   · 判红 = 门退出码非 0；「门没跑起来」（找不到结论行）= **作废**，不是绿；
#   · 锚点 0 命中 = 变异可能已过期 = **作废**，不许当成「没被抓住」。
#
# 用法：bash scripts/mut-nginx-realip-patch.sh
# =============================================================================
set -uo pipefail
cd "$(dirname "$0")/.."

ROOT="$PWD"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

PATCHER_REL="scripts/ops/nginx-llm-realip-patch.py"
ENGINE_REL="scripts/deploy-seamless-docker.sh"
GATE_PATCH="tests/nginx_llm_realip_patch_test.sh"
GATE_CONTRACT="tests/deploy_211_contract_test.sh"

PASS=0; MISS=0; INVALID=0

# ── 状态判定：red=被抓住 / green=没抓住 / invalid=门没跑起来 ──────────
state() {
  # state <输出> <退出码> <结论行正则>
  local out="$1" rc="$2" pat="$3"
  if ! grep -qE "$pat" <<<"$out"; then echo invalid; return 2; fi
  if [ "$rc" -ne 0 ]; then echo red; return 0; fi
  echo green; return 1
}

run_gate_patch() {   # run_gate_patch  （用 $MUT_PATCHER / $MUT_ENGINE）
  local out rc st
  out="$(NGINX_REALIP_PATCH="$MUT_PATCHER" DEPLOY_211_ENGINE="$MUT_ENGINE" \
         bash "$GATE_PATCH" 2>&1)"; rc=$?
  st="$(state "$out" "$rc" 'nginx llm realip patch: [0-9]+ 通过')"
  printf '%s\n' "$out" | grep -E '^\s+FAIL' | head -3
  echo "STATE=$st"
}

run_gate_contract() {  # run_gate_contract  （用 $MUT_ENGINE）
  local out rc st
  out="$(DEPLOY_211_ENGINE="$MUT_ENGINE" bash "$GATE_CONTRACT" 2>&1)"; rc=$?
  st="$(state "$out" "$rc" '结果: [0-9]+ 通过')"
  printf '%s\n' "$out" | grep -E 'FAIL' | head -3
  echo "STATE=$st"
}

# ── 变异：复制 → 改副本 → 跑门 → 判红 → 校验工作区逐字节未变 ─────────
# ★ 每次变异只把**被测的那一个文件**换成变异副本，另一个保持 pristine
#   （门①同时读补丁器与引擎 ⇒ 两个副本要分别指定）。
mutate() {
  # mutate <标签> <被测相对路径> <门: patch|contract> <旧文本> <新文本>
  local label="$1" rel="$2" which="$3" old="$4" new="$5"
  local src="$ROOT/$rel" copy="$WORK/$(basename "$rel").mut"
  local before after st out

  MUT_PATCHER="$ROOT/$PATCHER_REL"
  MUT_ENGINE="$ROOT/$ENGINE_REL"
  if [[ "$rel" == "$PATCHER_REL" ]]; then MUT_PATCHER="$copy"; else MUT_ENGINE="$copy"; fi

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

  if [[ "$which" == "patch" ]]; then
    out="$(run_gate_patch "$copy")"
  else
    out="$(run_gate_contract "$copy")"
  fi
  st="$(grep -m1 '^STATE=' <<<"$out" | cut -d= -f2-)"

  # ★ 工作区必须逐字节未变（本台不改工作区 ⇒ 这条是恒真的自检）
  after="$(md5 -q "$src")"
  if [[ "$before" != "$after" ]]; then
    echo "  ★ 工作区被改动了（before=${before} after=${after}）⇒ 异常，计作废"
    INVALID=$((INVALID+1))
    return
  fi

  case "$st" in
    red)     echo "  ✓ 被抓住（$which 门转红）"; PASS=$((PASS+1)) ;;
    green)   echo "  ✗ 没被抓住（$which 门仍绿）"; MISS=$((MISS+1)) ;;
    *)       echo "  ★ 作废：门没跑起来"; INVALID=$((INVALID+1)) ;;
  esac
}

echo "=========== CONTROL（未变异，两道门必须全绿）==========="
MUT_PATCHER="$ROOT/$PATCHER_REL"
MUT_ENGINE="$ROOT/$ENGINE_REL"
c1="$(run_gate_patch)"; echo "$c1"
[[ "$(grep -m1 '^STATE=' <<<"$c1" | cut -d= -f2-)" == "green" ]] || {
  echo "!! CONTROL 不绿 ⇒ 门本身有问题，不能用来判变异"; exit 1; }
c2="$(run_gate_contract)"; echo "$c2"
[[ "$(grep -m1 '^STATE=' <<<"$c2" | cut -d= -f2-)" == "green" ]] || {
  echo "!! CONTROL 不绿 ⇒ 门本身有问题，不能用来判变异"; exit 1; }

# ── M1：443 判定恒真 ⇒ mesh 腿（:8000）也会被当成 TLS 腿改掉 ─────────
echo; echo "=========== M1: is_tls_443 恒真（mesh 腿会被误改）==========="
mutate "M1" "$PATCHER_REL" patch \
'def is_tls_443(listen_args):
    """listen 参数里是否有 443 且带 ssl。"""
    args = listen_args.split()
    if '"'"'ssl'"'"' not in args:
        return False
    for a in args:
        if a == '"'"'443'"'"' or a.endswith('"'"':443'"'"'):
            return True
    return False' \
'def is_tls_443(listen_args):
    """MUTATED: 恒真"""
    return True'

# ── M2：未知取值也照改 ⇒ 「形态不认识」这条结论消失 ───────────────────
echo; echo "=========== M2: 未知取值也替换（第三种形态被瞎改）==========="
mutate "M2" "$PATCHER_REL" patch \
'            if value == OLD_VALUE:' \
'            if value != NEW_VALUE:'

# ── M3：共享终结器不再被识别 ⇒ 会去改别人的域 ─────────────────────────
echo; echo "=========== M3: 不识别共享终结器（会波及别的域）==========="
mutate "M3" "$PATCHER_REL" patch \
'            others = [n for n in names if n != '"'"'_'"'"' and not name_matches(n, domain)]
            if others:' \
'            others = []
            if others:'

# ── M4：丢掉「该域的块在非 443 腿上」的诊断 ⇒ 又变成「什么都不知道」 ──
echo; echo "=========== M4: 丢掉非 443 腿诊断（结论失去自证）==========="
mutate "M4" "$PATCHER_REL" patch \
'                non_tls_legs.append((path, listen_args or '"'"'(无 listen)'"'"'))' \
'                pass'

# ── M5：引擎 nginx -t 失败时不还原 ⇒ 留潜伏半切换态 ───────────────────
echo; echo "=========== M5: nginx -t 失败不还原（潜伏半切换态）==========="
mutate "M5" "$ENGINE_REL" contract \
'cp "${backs[$i]}" "${files[$i]}" 2>/dev/null || true' \
'true'

# ── M6：引擎把域名写死 ⇒ 换域名会静默改错对象 ─────────────────────────
echo; echo "=========== M6: 引擎写死域名 ============"
mutate "M6" "$ENGINE_REL" contract \
'  local domain="${DD_DOMAIN:-}"' \
'  local domain="llm.itestu.cn"'

# ── M7：引擎把「本机没有该域的 443」重新报成告警 ⇒ F2 的假告警复发 ────
echo; echo "=========== M7: 「本机没有该域 443」又变回告警 ============"
mutate "M7" "$ENGINE_REL" patch \
'      log "nginx ${domain}：本机没有该域的 443 TLS 块 ⇒ 无需 X-Real-IP 修补"' \
'      warn "nginx ${domain}：本机没有该域的 443 TLS 块 ⇒ 无需 X-Real-IP 修补"'

echo
echo "=========== 变异结果：$PASS 被抓住 / $MISS 没被抓住 / $INVALID 作废 ==========="
[[ "$MISS" -eq 0 ]] && [[ "$INVALID" -eq 0 ]] && [[ "$PASS" -gt 0 ]]
