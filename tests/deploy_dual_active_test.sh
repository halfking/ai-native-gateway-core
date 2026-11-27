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
grep -q 'dd_register_goacos "$_port" 1' "$ENGINE" \
  && ok "⑥ 副节点 goacos 注册带 keep=1（不注销主槽条目）" \
  || bad "⑥ 副节点 goacos 注册缺 keep 参数"
grep -q 'runtime_role' "$ENGINE" \
  && ok "⑥ 副节点就绪后有角色自证（/version.runtime_role）" \
  || bad "⑥ 副节点缺角色自证判据"

# ── 结果 ──────────────────────────────────────────────────────────────
echo
if (( fail == 0 )); then
  echo "ALL PASS ($pass checks)"
  exit 0
else
  echo "FAILED: $fail / $((pass+fail))"
  exit 1
fi
