#!/usr/bin/env bash
# =============================================================================
# nginx_llm_realip_patch_test.sh — scripts/ops/nginx-llm-realip-patch.py 的行为门
#
# 纯 bash + python3，零外部依赖（不碰 nginx、不碰任何目标机）：
# 用**夹具**（真 nginx conf 形态）驱动补丁器，钉住它的**结论**与**副作用**。
#
# 为什么是「行为门」而不是「源码里有几个字符串」（判据纪律）：
#   F2 的原症状是「引擎每次部署 WARN『块形态变了?』」——那是一条**假告警**。
#   如果门只查「引擎源码里有没有 X-Real-IP / $remote_addr 字样」，
#   那么把整段逻辑删成空壳、门照样绿 ⇒ **恒绿门**。
#   ⇒ 这道门必须**跑真代码、喂真 conf、看真文件变化**。
#
# 夹具覆盖的形态（每条都对应一种真实拓扑）：
#   ① 按域 443 块 + mesh 腿同文件 ⇒ 只改 443 腿，**mesh 腿必须原样不动**
#   ② 已是 $remote_addr ⇒ 结论 already，且**不产生任何文件改动**
#   ③ ★ 211 现状：域名的块只在 mesh 腿，443 由**通用终结器**承担
#      （终结器的 server_name 不含该域）⇒ 结论「本机不承担该域 443」，
#      **不是**「形态变了」，且**不改任何文件**
#   ④ 共享终结器（server_name 还含别的域）⇒ 不自动改（波及别的域）
#   ⑤ X-Real-IP 是第三种取值 ⇒ 形态不认识（这才是该 WARN 的场合）
#   ⑥ 不带 --apply ⇒ 只报 would-patch，**不改文件**
#   ⑦ 解析鲁棒性：块里有 ${var} 大括号、注释里有 '{' ⇒ 仍能正确切块
# =============================================================================
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 被测脚本可被环境变量覆盖 —— **只给变异验证用**（把改坏的副本喂给门，
# 证明门确实在测东西而不是恒绿）。日常运行不设，指向仓里的真脚本。
PATCHER="${NGINX_REALIP_PATCH:-$ROOT/scripts/ops/nginx-llm-realip-patch.py}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0; fail=0
ok()   { pass=$((pass+1)); printf '  ok   %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf '  FAIL %s\n' "$1"; }
check(){ if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1 (期望=$2 实得=$3)"; fi; }

[[ -f "$PATCHER" ]] || { echo "找不到 $PATCHER"; exit 1; }

DOMAIN='llm.itestu.cn'

# ── 夹具工厂 ──────────────────────────────────────────────────────────
# ① 按域 443 块 + 同一文件里的 mesh 腿（211 真实拓扑的缩影）
fx_dedicated_443() {
  local f="$1"
  cat > "$f" <<'CONF'
map $http_x_real_ip $ns_real_ip {
    default $http_x_real_ip;
}

server {
    listen 100.106.67.198:8000;
    server_name llm.itestu.cn;
    location / {
        proxy_set_header X-Real-IP $ns_real_ip;
    }
}

server {
    listen 443 ssl;
    server_name llm.itestu.cn;
    location / {
        proxy_set_header X-Real-IP $ns_real_ip;
        proxy_pass http://127.0.0.1:8781;
    }
}
CONF
}

# ③ 通用 TLS 终结器（**不含**该域的 server_name）+ 引擎自己的 conf 里只有 mesh 腿
fx_generic_terminator() {
  local f="$1"
  cat > "$f" <<'CONF'
server {
    listen 443 ssl default_server;
    server_name _;
    location / {
        proxy_set_header X-Real-IP $ns_real_ip;
        proxy_pass http://127.0.0.1:9000;
    }
}
CONF
}

# ④ 共享终结器：server_name 含该域 + 另一个域
fx_shared_leg() {
  local f="$1"
  cat > "$f" <<'CONF'
server {
    listen 443 ssl;
    server_name llm.itestu.cn other.example.com;
    location / {
        proxy_set_header X-Real-IP $ns_real_ip;
    }
}
CONF
}

# ⑦ 解析鲁棒性：${var} 大括号 + 注释里的 '{' + 注释里的 server {
fx_robust() {
  local f="$1"
  cat > "$f" <<'CONF'
# server { listen 443 ssl; }  ← 注释里假装的 server 块，不该被认出来
server {
    listen 443 ssl;
    server_name llm.itestu.cn;
    set $x "${scheme}://${host}";
    location / {
        # { 注释里的孤立大括号
        proxy_set_header X-Real-IP $ns_real_ip;
        proxy_pass http://${upstream};
    }
}
CONF
}

run_patcher() {
  # run_patcher <conf文件...> [--apply]  → stdout
  # ★ 参数是**纯文件名列表**（可选 --apply）；不要在这里塞 --conf 之类选项，
  #   否则会被当成文件名传给补丁器（2026-10-23 实测踩过：输出空 ⇒ 门红在
  #   「量具调用错了」而不是「被测行为变了」）。
  local apply_flag=""
  local confs=()
  for a in "$@"; do
    if [[ "$a" == "--apply" ]]; then apply_flag="--apply"; else confs+=("$a"); fi
  done
  local cargs=()
  for f in "${confs[@]}"; do cargs+=(--conf "$f"); done
  python3 "$PATCHER" --domain "$DOMAIN" "${cargs[@]}" $apply_flag 2>&1
}

result_of() { grep -m1 '^RESULT=' <<<"$1" | cut -d= -f2-; }

# ═══════════════════════════════════════════════════════════════════════
# 场景①：按域 443 块（+ 同文件 mesh 腿）⇒ patched；mesh 腿必须不动
# ═══════════════════════════════════════════════════════════════════════
d="$TMP/case1"; mkdir -p "$d"; fx_dedicated_443 "$d/ns.conf"
out="$(run_patcher "$d/ns.conf" --apply)"
check "① 结论=patched" "patched" "$(result_of "$out")"
n_443="$(grep -c 'X-Real-IP \$remote_addr;' "$d/ns.conf" || true)"
n_ns="$(grep -c 'X-Real-IP \$ns_real_ip;' "$d/ns.conf" || true)"
check "① 443 腿被改成 \$remote_addr（1 处）" "1" "$n_443"
check "① mesh 腿保留 \$ns_real_ip（1 处，未被误改）" "1" "$n_ns"
[[ -f "$d/ns.conf.bak-dd-realip" ]] && ok "① 产生改前备份" || bad "① 没有产生改前备份"
if grep -q 'X-Real-IP \$ns_real_ip;' "$d/ns.conf.bak-dd-realip" 2>/dev/null; then
  ok "① 备份里是**改前**内容（含 \$ns_real_ip）"
else
  bad "① 备份内容不是改前形态"
fi
# 幂等：再跑一次 ⇒ already，且不再改文件
before="$(md5 -q "$d/ns.conf")"
out2="$(run_patcher "$d/ns.conf" --apply)"
check "① 幂等：第二次结论=already" "already" "$(result_of "$out2")"
check "① 幂等：文件未再变" "$before" "$(md5 -q "$d/ns.conf")"

# ═══════════════════════════════════════════════════════════════════════
# 场景②：已是 $remote_addr ⇒ already，不改文件
# ═══════════════════════════════════════════════════════════════════════
d="$TMP/case2"; mkdir -p "$d"
cat > "$d/already.conf" <<'CONF'
server {
    listen 443 ssl;
    server_name llm.itestu.cn;
    location / {
        proxy_set_header X-Real-IP $remote_addr;
    }
}
CONF
before="$(md5 -q "$d/already.conf")"
out="$(run_patcher "$d/already.conf" --apply)"
check "② 结论=already" "already" "$(result_of "$out")"
check "② 文件未变" "$before" "$(md5 -q "$d/already.conf")"
[[ ! -f "$d/already.conf.bak-dd-realip" ]] && ok "② already 不产生备份（没改就不留痕）" \
  || bad "② already 却留下了备份"

# ═══════════════════════════════════════════════════════════════════════
# 场景③ ★ 211 现状：域名的块只在 mesh 腿，443 是通用终结器
#   ⇒ 结论必须是「本机不承担该域 443」而不是「形态变了」；且不改文件
# ═══════════════════════════════════════════════════════════════════════
d="$TMP/case3"; mkdir -p "$d"
cat > "$d/ns-gateway-211.conf" <<'CONF'
server {
    listen 100.106.67.198:8000;
    server_name llm.itestu.cn;
    location / {
        proxy_set_header X-Real-IP $ns_real_ip;
        proxy_pass http://127.0.0.1:8782;
    }
}
CONF
fx_generic_terminator "$d/terminator.conf"
before_ns="$(md5 -q "$d/ns-gateway-211.conf")"
before_tm="$(md5 -q "$d/terminator.conf")"
out="$(run_patcher "$d/ns-gateway-211.conf" "$d/terminator.conf" --apply)"
check "③ 结论=skip-domain-not-on-443-block" "skip-domain-not-on-443-block" "$(result_of "$out")"
check "③ mesh 腿文件未被改" "$before_ns" "$(md5 -q "$d/ns-gateway-211.conf")"
check "③ 通用终结器未被改" "$before_tm" "$(md5 -q "$d/terminator.conf")"
if grep -q 'mesh' <<<"$out" || grep -q '8000' <<<"$out"; then
  ok "③ 明细里点出了该域的块在哪、listen 什么（可自证不是『形态变了』）"
else
  bad "③ 明细没说明该域的 server 块实际在哪条腿上"
fi

# ═══════════════════════════════════════════════════════════════════════
# 场景④ 共享终结器 ⇒ skip-shared-leg，不自动改
# ═══════════════════════════════════════════════════════════════════════
d="$TMP/case4"; mkdir -p "$d"; fx_shared_leg "$d/shared.conf"
before="$(md5 -q "$d/shared.conf")"
out="$(run_patcher "$d/shared.conf" --apply)"
check "④ 结论=skip-shared-leg" "skip-shared-leg" "$(result_of "$out")"
check "④ 共享终结器未被改（不波及别的域）" "$before" "$(md5 -q "$d/shared.conf")"

# ═══════════════════════════════════════════════════════════════════════
# 场景⑤ 第三种取值 ⇒ noop-unknown-shape（这才是该 WARN 的场合）
# ═══════════════════════════════════════════════════════════════════════
d="$TMP/case5"; mkdir -p "$d"
cat > "$d/weird.conf" <<'CONF'
server {
    listen 443 ssl;
    server_name llm.itestu.cn;
    location / {
        proxy_set_header X-Real-IP $http_x_forwarded_for;
    }
}
CONF
before="$(md5 -q "$d/weird.conf")"
out="$(run_patcher "$d/weird.conf" --apply)"
check "⑤ 结论=noop-unknown-shape" "noop-unknown-shape" "$(result_of "$out")"
check "⑤ 未知形态不被瞎改" "$before" "$(md5 -q "$d/weird.conf")"
if grep -q 'http_x_forwarded_for' <<<"$out"; then
  ok "⑤ 明细里报出了实际取值（人能据此判断）"
else
  bad "⑤ 明细没报出 X-Real-IP 的实际取值"
fi

# ═══════════════════════════════════════════════════════════════════════
# 场景⑥ 默认（不带 --apply）⇒ only would-patch，不改文件
# ═══════════════════════════════════════════════════════════════════════
d="$TMP/case6"; mkdir -p "$d"; fx_dedicated_443 "$d/ns.conf"
before="$(md5 -q "$d/ns.conf")"
out="$(run_patcher "$d/ns.conf")"
check "⑥ 结论=would-patch" "would-patch" "$(result_of "$out")"
check "⑥ 默认不改文件" "$before" "$(md5 -q "$d/ns.conf")"
[[ ! -f "$d/ns.conf.bak-dd-realip" ]] && ok "⑥ 默认不产生备份" \
  || bad "⑥ 默认（未 --apply）却产生了备份"

# ═══════════════════════════════════════════════════════════════════════
# 场景⑦ 解析鲁棒性：${var} / 注释里的假 server 块
# ═══════════════════════════════════════════════════════════════════════
d="$TMP/case7"; mkdir -p "$d"; fx_robust "$d/robust.conf"
out="$(run_patcher "$d/robust.conf" --apply)"
check "⑦ 结论=patched（没被注释里的假块骗过）" "patched" "$(result_of "$out")"
n_443="$(grep -c 'X-Real-IP \$remote_addr;' "$d/robust.conf" || true)"
check "⑦ 只改了 1 处" "1" "$n_443"
if grep -q 'proxy_pass http://${upstream};' "$d/robust.conf"; then
  ok '⑦ ${var} 原样保留（没把 ${upstream} 当变量吃掉）'
else
  bad '⑦ ${var} 被改坏了'
fi

# ═══════════════════════════════════════════════════════════════════════
# 场景⑧ --search-root：跨文件搜（引擎的真实用法）
# ═══════════════════════════════════════════════════════════════════════
d="$TMP/case8"; mkdir -p "$d/conf.d"
cat > "$d/conf.d/ns-gateway-211.conf" <<'CONF'
server {
    listen 100.106.67.198:8000;
    server_name llm.itestu.cn;
    location / {
        proxy_set_header X-Real-IP $ns_real_ip;
    }
}
CONF
fx_generic_terminator "$d/conf.d/00-terminator.conf"
out="$(python3 "$PATCHER" --domain "$DOMAIN" --search-root "$d/conf.d" 2>&1)"
check "⑧ search-root 下结论=skip-domain-not-on-443-block" \
  "skip-domain-not-on-443-block" "$(result_of "$out")"

# ═══════════════════════════════════════════════════════════════════════
# 引擎级端到端演练（真引擎文件 + 替身 nginx）
# ───────────────────────────────────────────────────────────────────────
# 上面的场景验的是**补丁器**；这里验的是**引擎怎么用它**：
#   · 分类 → 处置的映射（信息 vs 告警）；
#   · patched ⇒ nginx -t ⇒ reload；
#   · nginx -t 失败 ⇒ **还原**（不留半切换态配置）。
# ★ 函数是从**真引擎文件**里抽出来的（不是复制品）⇒ 测的是真接线。
ENGINE="${DEPLOY_211_ENGINE:-$ROOT/scripts/deploy-seamless-docker.sh}"
extract_fn() {
  local fn="$1" start
  start="$(grep -nE "^${fn}\(\)[[:space:]]*\{" "$ENGINE" | head -1 | cut -d: -f1)"
  [[ -n "$start" ]] || return 1
  sed -n "${start},/^}/p" "$ENGINE"
}
if ! extract_fn dd_fix_nginx_llm_real_ip > "$TMP/engine_fn.sh"; then
  bad "抽不出引擎函数 dd_fix_nginx_llm_real_ip（脚本结构变了？）"
else
  ok "抽得出引擎函数 dd_fix_nginx_llm_real_ip"

  # 替身 nginx：-t 的退出码由 FAKE_NGINX_T_EXIT 控制；-s reload 记到日志
  mkdir -p "$TMP/bin"
  cat > "$TMP/bin/nginx" <<'SH'
#!/bin/sh
case "$1" in
  -t) exit "${FAKE_NGINX_T_EXIT:-0}" ;;
  -s) echo "reload" >> "$FAKE_NGINX_LOG"; exit 0 ;;
esac
exit 0
SH
  chmod +x "$TMP/bin/nginx"

  run_engine() {
    # run_engine <conf> <搜索根> [nginx -t 退出码] → stdout（LOG/WARN 行）
    local conf="$1" roots="$2" t_exit="${3:-0}" logf="$TMP/nginx.log"
    : > "$logf"
    (
      export PATH="$TMP/bin:$PATH"
      export FAKE_NGINX_T_EXIT="$t_exit"
      export FAKE_NGINX_LOG="$logf"
      export DD_REPO_ROOT="$ROOT"
      export DD_DOMAIN="$DOMAIN"
      export DD_NGINX_CONF="$conf"
      export DD_NGINX_SEARCH_ROOTS="$roots"
      log()  { printf 'LOG %s\n' "$*"; }
      warn() { printf 'WARN %s\n' "$*"; }
      # shellcheck disable=SC1090
      source "$TMP/engine_fn.sh"
      dd_fix_nginx_llm_real_ip
    ) 2>&1
  }

  # ── E1：211 现状（本机没有该域的 443）⇒ **信息**，不告警、不 reload ──
  d="$TMP/e1"; mkdir -p "$d/conf.d"
  cat > "$d/conf.d/ns-gateway-211.conf" <<'CONF'
server {
    listen 100.106.67.198:8000;
    server_name llm.itestu.cn;
    location / {
        proxy_set_header X-Real-IP $ns_real_ip;
    }
}
CONF
  fx_generic_terminator "$d/conf.d/00-terminator.conf"
  before="$(md5 -q "$d/conf.d/ns-gateway-211.conf")"
  out="$(run_engine "$d/conf.d/ns-gateway-211.conf" "$d/conf.d")"
  if grep -q '^WARN' <<<"$out"; then
    bad "E1 引擎把「本机没有该域的 443」报成了告警（F2 的假告警没消掉）"
  else
    ok "E1 「本机没有该域的 443」不再产生告警"
  fi
  if grep -q '本机没有该域的 443' <<<"$out"; then
    ok "E1 引擎明确说了「本机没有该域的 443」（结论自报家门）"
  else
    bad "E1 引擎没说清为什么没改"
  fi
  check "E1 文件未被改" "$before" "$(md5 -q "$d/conf.d/ns-gateway-211.conf")"

  # ── E2：按域 443 块存在 ⇒ 改了 + nginx -t + reload ──────────────────
  d="$TMP/e2"; mkdir -p "$d/conf.d"; fx_dedicated_443 "$d/conf.d/llm.conf"
  out="$(run_engine "$d/conf.d/llm.conf" "$d/conf.d")"
  n_443="$(grep -c 'X-Real-IP \$remote_addr;' "$d/conf.d/llm.conf" || true)"
  check "E2 引擎把 443 腿改成了 \$remote_addr" "1" "$n_443"
  if grep -q '^LOG .*reloaded' <<<"$out"; then
    ok "E2 改完 reload 了 nginx"
  else
    bad "E2 改完没 reload"
  fi

  # ── E3：nginx -t 失败 ⇒ **还原**（不留半切换态配置）─────────────────
  d="$TMP/e3"; mkdir -p "$d/conf.d"; fx_dedicated_443 "$d/conf.d/llm.conf"
  before="$(md5 -q "$d/conf.d/llm.conf")"
  out="$(run_engine "$d/conf.d/llm.conf" "$d/conf.d" 1)"
  check "E3 nginx -t 失败后文件被还原" "$before" "$(md5 -q "$d/conf.d/llm.conf")"
  if grep -q '^WARN' <<<"$out"; then
    ok "E3 nginx -t 失败产生了告警（不能静默）"
  else
    bad "E3 nginx -t 失败静默了"
  fi
fi

echo
echo "nginx llm realip patch: $pass 通过 / $fail 失败"
[[ "$fail" -eq 0 ]]
