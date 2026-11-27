#!/usr/bin/env bash
# =============================================================================
# deploy_dual_active_test.sh — 双活副节点（2026-11-27 多节点轮）的离线门
#
# 被测件：
#   ① scripts/ops/nginx-upstream-port.py  --list-ports / --add-server / --remove-server
#   ② scripts/deploy-seamless-docker.sh   dd_upstream_servers / dd_dual_active_ports
#      （双活探测：注释端口不算、backup 行不算、a/b 槽位端口不算副节点）
#   ③ 引擎 dd_read_env_file_value（%q 解码读值）与静态判据
#      —— 按引擎函数的「沙箱抽取」范式：抽出真实函数体在沙箱里驱动，
#         不 mock 引擎，不让门读环境里恰好存在的变量。
#
# 零外部依赖：纯 bash + python3，不碰 docker、不碰 nginx、不碰目标机。
# =============================================================================
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HELPER="${NGINX_UPSTREAM_PORT_HELPER:-$ROOT/scripts/ops/nginx-upstream-port.py}"
ENGINE="$ROOT/scripts/deploy-seamless-docker.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

pass=0; fail=0
ok()   { pass=$((pass+1)); printf '  ok   %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf '  FAIL %s\n' "$1"; }
check(){ if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1 (期望=$2 实得=$3)"; fi; }

[[ -f "$HELPER" ]] || { echo "找不到 $HELPER"; exit 1; }
[[ -f "$ENGINE" ]] || { echo "找不到 $ENGINE"; exit 1; }

UP="ns_llm_gateway_upstream"

# ── ① helper --list-ports：单活 / 双活 / 注释与 backup 不算 ────────────
d="$TMP/list"; mkdir -p "$d"
cat > "$d/single.conf" <<CONF
upstream $UP {
    server 127.0.0.1:8781 max_fails=2 fail_timeout=10s;
    server 100.106.169.63:8781 backup;
}
CONF
check "① list 单活：只回 8781" "8781" \
  "$(python3 "$HELPER" --conf "$d/single.conf" --upstream "$UP" --list-ports)"

cat > "$d/dual.conf" <<CONF
upstream $UP {
    # 历史注释里的 8799 不算数
    server 127.0.0.1:8781 max_fails=2 fail_timeout=10s;
    server 127.0.0.1:8784;
    server 100.106.169.63:8784 backup;
}
CONF
check "① list 双活：回两行（backup 不算）" "8781 8784" \
  "$(python3 "$HELPER" --conf "$d/dual.conf" --upstream "$UP" --list-ports | tr '\n' ' ' | sed 's/ $//')"

# ── ② helper --add-server：追加 / 幂等 / 读侧主行不变 ─────────────────
check "② add：追加 8784 ⇒ patched" "patched" \
  "$(python3 "$HELPER" --conf "$d/single.conf" --upstream "$UP" --add-server 8784 2>/dev/null)"
check "② add 幂等：再追加 ⇒ already" "already" \
  "$(python3 "$HELPER" --conf "$d/single.conf" --upstream "$UP" --add-server 8784 2>/dev/null)"
check "② add 后 list：8781 8784" "8781 8784" \
  "$(python3 "$HELPER" --conf "$d/single.conf" --upstream "$UP" --list-ports | tr '\n' ' ' | sed 's/ $//')"
check "② add 后主行读侧不变（switch 语义不破坏）" "8781" \
  "$(python3 "$HELPER" --conf "$d/single.conf" --upstream "$UP")"

# ── ③ helper --remove-server：删行 / 幂等 / 拒删最后一行 ──────────────
check "③ remove：删 8784 ⇒ patched" "patched" \
  "$(python3 "$HELPER" --conf "$d/single.conf" --upstream "$UP" --remove-server 8784 2>/dev/null)"
check "③ remove 幂等：再删 ⇒ already" "already" \
  "$(python3 "$HELPER" --conf "$d/single.conf" --upstream "$UP" --remove-server 8784 2>/dev/null)"
check "③ remove 最后一行 ⇒ noop-empty-block（拒绝整空块）" "noop-empty-block" \
  "$(python3 "$HELPER" --conf "$d/single.conf" --upstream "$UP" --remove-server 8781 2>/dev/null)"
check "③ 拒删后文件未动（8781 仍在）" "8781" \
  "$(python3 "$HELPER" --conf "$d/single.conf" --upstream "$UP")"

# ── ④ 引擎双活探测：dd_dual_active_ports ──────────────────────────────
# 抽真实函数体驱动（awk 括号配平抽取，与引擎执行的是同一份代码）。
awk '/^dd_upstream_servers\(\) \{/,/^\}/' "$ENGINE" > "$TMP/f_servers.sh"
awk '/^dd_dual_active_ports\(\) \{/,/^\}/' "$ENGINE" > "$TMP/f_dual.sh"
if [[ -s "$TMP/f_servers.sh" && -s "$TMP/f_dual.sh" ]]; then
  bash -c '
  source '"$TMP"'/f_servers.sh; source '"$TMP"'/f_dual.sh
  export DD_SLOT_A_PORT=8781 DD_SLOT_B_PORT=8783
  DD_UPSTREAM_PORT_HELPER="'"$HELPER"'"
  printf "upstream %s {\n    server 127.0.0.1:8781;\n}\n" '"$UP"' > '"$TMP"'/e_single.conf
  printf "upstream %s {\n    server 127.0.0.1:8781;\n    server 127.0.0.1:8784;\n}\n" '"$UP"' > '"$TMP"'/e_dual.conf
  printf "upstream %s {\n    # 注释 8799\n    server 127.0.0.1:8784;\n    server 127.0.0.1:8781;\n}\n" '"$UP"' > '"$TMP"'/e_dual2.conf
  DD_NGINX_CONF="'"$TMP"'/e_single.conf"; DD_NGINX_UPSTREAM='"$UP"'
  echo "single=[$(dd_dual_active_ports)]"
  DD_NGINX_CONF="'"$TMP"'/e_dual.conf"
  echo "dual=[$(dd_dual_active_ports)]"
  DD_NGINX_CONF="'"$TMP"'/e_dual2.conf"
  # 契约：主行=掩码后第一条；这里第一条是副端口 8784（不在 a/b 槽位表里）
  # ⇒ 引擎把它判为 extra；8781 是槽 a ⇒ 不出现在 extra 里。
  echo "dual2=[$(dd_dual_active_ports)]"
  ' > "$TMP/dual_out.txt" 2>/dev/null
  check "④ 引擎探测：单活 ⇒ 空" "single=[]"        "$(grep '^single=' "$TMP/dual_out.txt")"
  check "④ 引擎探测：双活 ⇒ 8784" "dual=[8784]"    "$(grep '^dual=' "$TMP/dual_out.txt")"
  check "④ 引擎探测：主行=副端口时 extra 取首行、槽位端口不算" "dual2=[8784]" "$(grep '^dual2=' "$TMP/dual_out.txt")"
else
  bad "④ 引擎函数抽取失败（dd_upstream_servers / dd_dual_active_ports 不在引擎里？）"
fi

# ── ⑤ 引擎 env 读值：dd_read_env_file_value（%q 解码）─────────────────
# 夹具用引擎同款 printf %q 形态；值里带 & ? 空格三类元字符。
# ★ 沙箱必须连 _dd_unq 一起 source（dd_read_env_file_value 依赖它）。
d="$TMP/env"; mkdir -p "$d"
awk '/^_dd_unq\(\) \{/,/^\}/' "$ENGINE" > "$TMP/f_unq.sh"
awk '/^dd_read_env_file_value\(\) \{/,/^\}/' "$ENGINE" > "$TMP/f_readenv.sh"
if [[ -s "$TMP/f_unq.sh" && -s "$TMP/f_readenv.sh" ]]; then
  {
    printf 'export LLM_GATEWAY_DATABASE_URL=%q\n' 'postgres://gwuser:gw.pass&x@pg-host:5432/llm_gateway?sslmode=disable'
    printf 'export LLM_GATEWAY_SECRET_KEY=%q\n' 's3cr3t key&more'
    printf 'export LLM_GATEWAY_REDIS_DB=%q\n' '5'
    printf '# comment line\n'
    printf 'export LLM_GATEWAY_RUNTIME_ROLE=%q\n' 'active'
  } > "$d/gateway.env"
  bash -c '
  source '"$TMP"'/f_unq.sh; source '"$TMP"'/f_readenv.sh
  GW="'"$d"'/gateway.env"
  echo "dburl=[$(dd_read_env_file_value "$GW" LLM_GATEWAY_DATABASE_URL)]"
  echo "secret=[$(dd_read_env_file_value "$GW" LLM_GATEWAY_SECRET_KEY)]"
  echo "role=[$(dd_read_env_file_value "$GW" LLM_GATEWAY_RUNTIME_ROLE)]"
  echo "missing=[$(dd_read_env_file_value "$GW" LLM_GATEWAY_NOT_THERE)]"
  ' > "$d/readenv_out.txt" 2>/dev/null
  check "⑤ 读值：DSN 的 & ? @ 元字符解码正确" \
    "dburl=[postgres://gwuser:gw.pass&x@pg-host:5432/llm_gateway?sslmode=disable]" \
    "$(grep '^dburl=' "$d/readenv_out.txt")"
  check "⑤ 读值：空格与 & 不破" "secret=[s3cr3t key&more]" "$(grep '^secret=' "$d/readenv_out.txt")"
  check "⑤ 读值：覆盖前读到主槽 role" "role=[active]" "$(grep '^role=' "$d/readenv_out.txt")"
  check "⑤ 读值：缺失键返回空（不发明）" "missing=[]" "$(grep '^missing=' "$d/readenv_out.txt")"
else
  bad "⑤ _dd_unq / dd_read_env_file_value 抽取失败"
fi

# ── ⑥ 引擎静态判据：守卫与模式分派在场 ────────────────────────────────
grep -q 'DD_DUAL_ACTIVE_EXTRA="$(dd_dual_active_ports' "$ENGINE" \
  && ok "⑥ 部署主入口有双活守卫（dd_dual_active_ports 判定）" \
  || bad "⑥ 部署主入口缺双活守卫"
grep -q '== "start-secondary"' "$ENGINE" \
  && ok "⑥ start-secondary 模式分派在场" \
  || bad "⑥ start-secondary 模式分派缺失"
grep -q '== "stop-secondary"' "$ENGINE" \
  && ok "⑥ stop-secondary 模式分派在场" \
  || bad "⑥ stop-secondary 模式分派缺失"
grep -q -- '--add-server "$_port"' "$ENGINE" \
  && ok "⑥ start-secondary 走 helper --add-server（不裸改 nginx）" \
  || bad "⑥ start-secondary 未走 helper 追加"
grep -q -- '--remove-server "$_port"' "$ENGINE" \
  && ok "⑥ stop-secondary 走 helper --remove-server（不裸改 nginx）" \
  || bad "⑥ stop-secondary 未走 helper 收缩"
grep -q 'LLM_GATEWAY_RUNTIME_ROLE=traffic-only' "$ENGINE" \
  && ok "⑥ 副节点 env 覆盖段钉 traffic-only 角色" \
  || bad "⑥ 副节点 env 未钉 traffic-only"

# ★ 派生循环单层 export（2026-11-27 真机教训）：gateway.env 的行本身带
#   `export ` 前缀，派生时必须先剥再写 ⇒ 输出是单层 export。首版产出
#   `export export KEY=…` ⇒ 注入循环剥一层后键名=「export KEY」⇒ 容器
#   只剩覆盖段 4 键活着（no DB/no Redis ⇒ readyz FATAL）。判据钉两点：
#   ① 搬运前先剥前缀（`_line="${_line#export }"` 在循环内）；
#   ② 注入循环有键名合法性守卫（垃圾键名 die 不静默注入）。
dd_secondary_derive_block() {
  awk '/^dd_start_secondary\(\) \{/,/^\}$/' "$ENGINE"
}
_dd_derive="$(dd_secondary_derive_block)"
printf '%s' "$_dd_derive" | grep -q 'while IFS= read -r _line' \
  && printf '%s' "$_dd_derive" | grep -q '_line="\${_line#export }"' \
  && printf '%s' "$_dd_derive" | grep -q "printf 'export %s" \
  && ok "⑥ 派生循环先剥再写单层 export（防 export export 双层）" \
  || bad "⑥ 派生循环缺「先剥 export 前缀」步骤——会产出双层 export"
grep -q '非法键名' "$ENGINE" \
  && grep -q 'env 派生层或 %q 解码层损坏' "$ENGINE" \
  && ok "⑥ 注入循环有键名合法性守卫（垃圾键名 die）" \
  || bad "⑥ 注入循环缺键名合法性守卫"
grep -q 'dd_register_goacos "$_port" 1' "$ENGINE" \
  && ok "⑥ 副节点 goacos 注册带 keep=1（不注销主槽条目）" \
  || bad "⑥ 副节点 goacos 注册缺 keep 参数"
grep -q 'runtime_role' "$ENGINE" \
  && ok "⑥ 副节点就绪后有角色自证（/version.runtime_role）" \
  || bad "⑥ 副节点缺角色自证判据"

# ★ _dd_unq 双定义对拍（2026-11-27 真机教训）：正式定义在 deploy 主流程读点前，
#   start-secondary 分派块里另有一份前置定义（bash 自上而下执行 ⇒ 分派块先进
#   dd_start_secondary → env 派生 → _dd_unq，正式定义那时还没被读到 ⇒ 真机
#   FATAL command not found）。两份必须逐字一致——漂移即解码语义分叉。
# 提取器按**出现序号**取第 N 处定义体（对全局提取取两次是自证：按构造恒等）。
# ★ 前置定义在 if 块内有缩进 ⇒ 匹配 ^[[:space:]]*_dd_unq；BSD head 无 -n -1 ⇒ 去
#   首尾行用 sed '1d;$d'。
_dd_unq_nth() {
  awk -v n="$2" '
    /^[[:space:]]*_dd_unq\(\) \{/ { c++; if (c == n) p = 1 }
    p { print }
    p && /^[[:space:]]*\}$/ { exit }
  ' "$1" | sed -e '1d' -e '$d' -e 's/^[[:space:]]*//' \
      -e '/^#/d' -e 's/[[:space:]]*$//' | grep -v '^$'
}
# ★ 归一化口径：剥行首空白（前置定义在 if 块内缩进）＋丢弃纯注释行＋去行尾
#   空白。★ 不剥行内 #：函数体有 `${#decoded}`——行内 # 剥除会把可执行行剥坏
#   （首版实测 (( ${#decoded} < ${#v} )) 被剥成 (( ${ ⇒ 误红）。两处定义的
#   可执行行必须逐字一致（含行尾注释——正式定义的 printf -v _bt 行带尾注，
#   前置副本已同步同款尾注）。
_du_count="$(grep -c '^[[:space:]]*_dd_unq() {' "$ENGINE")"
check "⑥ _dd_unq 恰好两处定义（前置 + 正式）" "2" "$_du_count"
_du_1="$(_dd_unq_nth "$ENGINE" 1 | md5 -q)"
_du_2="$(_dd_unq_nth "$ENGINE" 2 | md5 -q)"
if [[ -n "$_du_1" && "$_du_1" == "$_du_2" ]]; then
  ok "⑥ _dd_unq 两处定义逐字一致（防解码语义漂移）"
else
  bad "⑥ _dd_unq 两处定义体不一致或缺失——同步 :2776 正式定义体"
fi
# 提取器正向自检（2026-11-27 教训：首版写成「变异后 1 号≠2 号应红」——方向
# 反了：把第 1 处改名后提取器取到的 1 号就是原第 2 处，相等恰是按序号提取的
# 正确行为）。正向判据 = 变异后 1 号 == _du_2（序号语义正确）**且** 变异后
# 只剩 1 处 _dd_unq（改名生效）。★ 变异用 awk 不用 sed：BSD sed 不支持
# `0,/re/` 首次匹配地址（GNU 扩展）——首版实测变异静默不生效、count 恒 2。
awk ' /^[[:space:]]*_dd_unq\(\) \{/ && !done { sub(/_dd_unq\(\)/, "_dd_unq_MUTATED()"); done=1 }
      { print }' "$ENGINE" > "$TMP/engine_mutated.sh" \
  && { _mut_count="$(grep -c '^[[:space:]]*_dd_unq() {' "$TMP/engine_mutated.sh")"
       _mut_1="$(_dd_unq_nth "$TMP/engine_mutated.sh" 1 | md5 -q)"
       if [[ "$_mut_count" == "1" && "$_mut_1" == "$_du_2" ]]; then
         ok "⑥ 提取器正向自检：第 1 处改名后 1 号=原第 2 处（序号语义真实）"
       else
         bad "⑥ 提取器正向自检失败——一致性判据可能取错对象（count=${_mut_count}）"
       fi; } \
  || bad "⑥ 提取器正向自检无法执行（awk 失败）"

# ── 结果 ──────────────────────────────────────────────────────────────
echo
if (( fail == 0 )); then
  echo "ALL PASS ($pass checks)"
  exit 0
else
  echo "FAILED: $fail / $((pass+fail))"
  exit 1
fi
