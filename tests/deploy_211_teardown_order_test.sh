#!/usr/bin/env bash
# =============================================================================
# deploy_211_teardown_order_test.sh — 211 部署的「拆服务 vs 判定 vs 切流」顺序门
# =============================================================================
# 为什么要有这个门（2026-10-20 事故）：
#   旧单容器形态的顺序是
#       docker rm -f 旧容器 → 才做前端新鲜度判定 → 判定失败 die
#   ⇒ 一次「部署失败」被放大成「服务下线」：旧容器已删 + 新容器未起，
#     211 上 nginx 的 upstream（127.0.0.1:8781）无监听 ⇒ llm.itestu.cn 502。
#   根因是**顺序**不是规则：守卫本身是对的（陈旧前端确实不该发），
#   但它跑在了拆服务之后。
#
# 2026-10-20 第二轮：211 改成**蓝绿**（与 245/252 同款），于是「退役」不再是
#   拆旧容器，而是：
#     守卫 → 构建 → 起**候选槽**（旧槽全程在服务）→ 验收候选
#     → 切 nginx upstream → 复验公网版本身份 → 停旧槽（停而不删）
#   任一步失败 ⇒ abort：丢候选槽 + 把 upstream 切回旧槽 + 旧槽若已停则拉回。
#
# 本门三层：
#   ① 行为层：把被测脚本里抽出来的**真函数**用 docker/nginx 替身驱动，证明
#      - 守卫拒绝时旧槽毫发无损（没被改名/停掉/删掉）；
#      - 切流**前**失败：候选被丢、upstream 仍指旧槽、旧槽仍在服务；
#      - 切流**后**失败：候选被丢、upstream 切回旧槽、旧槽被拉回；
#      - 种子按槽隔离 ⇒ 回退**不需要**还原种子（旧槽产物不被本轮覆盖）。
#   ② 语义层：前端新鲜度判定的四种结局（新鲜/陈旧/缺失/缺失但上一轮种子还在）。
#   ③ 结构层：顺序不变量（守卫在破坏性动作之前；切流在验收之后、停旧槽之前；
#      失败点走 dd_fail 而不是裸 die；切流带 nginx -t；abort 里有切回）。
#
# 判据纪律：①③ 两层必须同时存在。只留 ①（函数本身对）⇒ 有人把切流挪到验收
# 之前，门照样全绿；只留 ③（文本形状对）⇒ 有人把 abort 的切回改坏，门也
# 未必抓得住。2026-10-17 审计轮已记过这个教训（结构门不覆盖行为门）。
# =============================================================================
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# 被测引擎路径可被环境变量覆盖 —— **只给变异验证用**（把改坏的副本喂给门，
# 证明门确实在测东西而不是恒绿）。日常运行不设该变量，指向仓库里的真引擎。
SCRIPT="${DEPLOY_211_ENGINE:-$ROOT/scripts/deploy-seamless-docker.sh}"
TMP="$(mktemp -d)"
# ⚠ 临时目录的清理不用 `rm -rf`：agent 环境里 `rm` 是「可恢复删除」shim，
#   每个目录约 3.8s 墙钟（实测 2026-10-20）⇒ 一个门里十几处清理就跑不完。
#   `find -delete` 走真实删除，秒级；mktemp 出来的临时目录本就是一次性产物。
nuke() { [[ -e "${1:-}" ]] && { find "$1" -depth -mindepth 1 -delete 2>/dev/null; rmdir "$1" 2>/dev/null || true; }; return 0; }
trap 'nuke "$TMP"' EXIT

pass=0; fail=0
ok()   { pass=$((pass+1)); printf '  ok   %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf '  FAIL %s\n' "$1"; }
check(){ if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1 (期望=$2 实得=$3)"; fi; }

[[ -f "$SCRIPT" ]] || { echo "找不到 $SCRIPT"; exit 1; }

# ── 从被测脚本里抽出真函数（测的是真代码，不是复制品）────────────────
extract_fn() {
  local fn="$1" start first
  start="$(grep -nE "^${fn}\(\)[[:space:]]*\{" "$SCRIPT" | head -1 | cut -d: -f1)"
  [[ -n "$start" ]] || return 1
  first="$(sed -n "${start}p" "$SCRIPT")"
  if [[ "$first" == *"}"* && "$first" != *"then"* ]]; then
    printf '%s\n' "$first"; return 0
  fi
  sed -n "${start},/^}/p" "$SCRIPT"
}

FNS="dd_web_dist_newer_source dd_check_web_dist dd_preflight_web_dist
     dd_slot_port dd_container_name dd_active_slot dd_write_active_slot
     dd_upstream_port dd_switch_upstream dd_abort_deploy dd_fail
     dd_public_version dd_switch_and_verify"
# 引擎按**自身所在目录**解析 upstream 端口判定脚本（scripts/ops/…）；
# 本门把函数抽到 $TMP/lib.sh 里 source ⇒ 解析到 $TMP/ops/ ⇒ 把**真脚本**放那。
# ★ 放真脚本（不是复制品/替身）⇒ 门测的仍是仓里那段判定逻辑。
mkdir -p "$TMP/ops"
cp "$ROOT/scripts/ops/nginx-upstream-port.py" "$TMP/ops/nginx-upstream-port.py"

: > "$TMP/lib.sh"
for fn in $FNS; do
  if ! extract_fn "$fn" > "$TMP/fn.sh"; then
    echo "抽不出 ${fn}（脚本结构变了？）"; exit 1
  fi
  cat "$TMP/fn.sh" >> "$TMP/lib.sh"
  printf '\n' >> "$TMP/lib.sh"
done

# ── docker 替身：容器状态 = $TMP/docker/<名字> 文件，内容 running/stopped ──
# ⚠ 必须复刻真实 docker 的**名字解析**语义，否则替身会替被测代码「擦屁股」：
#   `docker inspect <名字>` 不带 `--type` 时，**同名镜像也会命中**（本机镜像就叫
#   llm-gateway-211，容器同名）⇒ 「容器不在场」会被镜像骗过。
#   2026-10-20 真机实测：容器不存在时 `docker inspect llm-gateway-211` 仍返 rc=0，
#   随后 `docker rename` 报 "No such container"。替身若只认容器，这个缺陷就测不出来。
docker_stub() {
  local cmd="$1"; shift
  printf '%s\n' "$cmd $*" >> "$DD_STUB_DIR/.calls"
  local type=""
  while [[ "${1:-}" == --type ]]; do type="$2"; shift 2; done
  case "$cmd" in
    inspect)
      if [[ "$type" == "container" ]]; then
        [[ -f "$DD_STUB_DIR/$1" ]]
      else
        # 不带 --type：容器或镜像任一命中即 rc=0（真实 docker 的行为）
        [[ -f "$DD_STUB_DIR/$1" || -f "$DD_STUB_DIR/images/$1" ]]
      fi
      ;;
    rename)
      [[ -f "$DD_STUB_DIR/$1" ]] || return 1
      mv "$DD_STUB_DIR/$1" "$DD_STUB_DIR/$2"
      ;;
    stop)
      [[ -f "$DD_STUB_DIR/$1" ]] || return 1
      printf 'stopped\n' > "$DD_STUB_DIR/$1"
      ;;
    start)
      [[ -f "$DD_STUB_DIR/$1" ]] || return 1
      printf 'running\n' > "$DD_STUB_DIR/$1"
      ;;
    rm)
      local a
      for a in "$@"; do
        case "$a" in -*) continue ;; esac
        rm -f "$DD_STUB_DIR/$a"
      done
      ;;
    run)
      local a name="" want=0
      for a in "$@"; do
        if (( want )); then name="$a"; want=0; continue; fi
        if [[ "$a" == "--name" ]]; then want=1; fi
      done
      [[ "${DD_STUB_RUN_FAIL:-0}" == "1" ]] && return 1
      printf 'running\n' > "$DD_STUB_DIR/$name"
      ;;
    *) return 0 ;;
  esac
}

# ── nginx 替身：只记录调用与结果，不真的起 nginx ───────────────────────
# ⚠ 替身必须复刻真实 nginx 的**关键语义**：`nginx -t` 失败 ⇒ **不得** reload
#   （真实 nginx 里配置没通过就 reload 会被拒）。这里用 DD_STUB_NGINX_T_FAIL
#   让 -t 失败，并断言没有发生 reload —— 否则「切流失败不 reload」这条契约
#   就是假绿（替身永远成功，缺陷测不出来）。
nginx_stub() {
  printf '%s\n' "nginx $*" >> "$DD_STUB_DIR/.calls"
  case "${1:-}" in
    -t) [[ "${DD_STUB_NGINX_T_FAIL:-0}" == "1" ]] && return 1; return 0 ;;
    -s) [[ "${DD_STUB_NGINX_RELOAD_FAIL:-0}" == "1" ]] && return 1; return 0 ;;
  esac
  return 0
}

# ── 夹具：nginx upstream conf（形态照抄 211 线上）───────────────────────
# 关键：backup 行是**另一台机器**（100.106.169.63 = mac mini），切流只改
# 127.0.0.1:<port> 那一行；backup 行必须保持原样（切错了会把流量送到别的机）。
make_nginx_conf() {
  local port="${1:-8781}"
  mkdir -p "$TMP/nginx"
  cat > "$TMP/nginx/ns-gateway-test.conf" <<EOF
upstream ns_llm_gateway_upstream {
    server 127.0.0.1:${port} max_fails=2 fail_timeout=10s;
    server 100.106.169.63:8782 max_fails=2 fail_timeout=10s backup;
}
server {
    listen 192.168.31.211:443 ssl;
    server_name llm.itestu.cn;
}
EOF
}

# run_stub <代码> —— 在带 docker/nginx 替身的环境里跑一段代码；输出 = stdout+stderr
run_stub() {
  local code="$1"
  (
    set -uo pipefail
    DD_STUB_DIR="$TMP/docker"
    DD_TARGET="211"
    DD_SLOT_A_PORT="8781"
    DD_SLOT_B_PORT="8782"
    DD_CONTAINER_PREFIX="llm-gateway-211"
    DD_GW_DATA_ROOT="$TMP/data"
    DD_REPO_ROOT="$TMP/repo"
    DD_WEB_DIST_SRC="$TMP/repo/web/dist"
    DD_MOBILE_DIST_SRC="$TMP/repo/web-mobile/dist"
    DD_WEB_SRC_DIR="$TMP/repo/web/src"
    DD_MOBILE_SRC_DIR="$TMP/repo/web-mobile/src"
    DD_ALLOW_STALE_WEB="0"
    DD_WEB_CHECK_ERR=""
    DD_NGINX_CONF="$TMP/nginx/ns-gateway-test.conf"
    DD_NGINX_UPSTREAM="ns_llm_gateway_upstream"
    # 引擎按自身目录解析 upstream 端口判定脚本；本门把函数抽到 $TMP/lib.sh
    # ⇒ 解析到 $TMP/ops/（真脚本已拷到那里，见上面的 cp）
    DD_UPSTREAM_PORT_HELPER="$TMP/ops/nginx-upstream-port.py"
    DD_DOMAIN="llm.itestu.cn"
    DD_HOST="192.168.31.211"
    DD_ACTIVE_SLOT="a"
    DD_CANDIDATE_SLOT="b"
    DD_CANDIDATE_PORT="8782"
    DD_CANDIDATE_CONTAINER="llm-gateway-211-b"
    DD_ACTIVE_CONTAINER="llm-gateway-211-a"
    DD_SWITCHED=0
    DD_OLD_STOPPED=0
    log()  { printf '[log] %s\n' "$*" >&2; }
    warn() { printf '[warn] %s\n' "$*" >&2; }
    die()  { printf '[die] %s\n' "$*" >&2; exit 9; }
    docker() { docker_stub "$@"; }
    nginx() { nginx_stub "$@"; }
    # 每次调用都从空的调用流水开始（否则上一场景的 rename 会污染本场景的断言）
    : > "$DD_STUB_DIR/.calls"
    source "$TMP/lib.sh"
    eval "$code"
  ) 2>&1
}

container_state() {
  local name="$1"
  if [[ -f "$TMP/docker/$name" ]]; then cat "$TMP/docker/$name"; else printf 'absent'; fi
}

upstream_port_now() {
  # 只取 127.0.0.1:<port> 的 port 段；backup 行是别的 IP，天然不匹配。
  # ⚠ 早期版本用 `awk '{print $2}' | tr -d ':'` ⇒ 把 IP 一起截掉
  #   （127.0.0.1:8781 → 127.0.0.18781）⇒ 断言恒红。
  sed -n 's/^[[:space:]]*server 127\.0\.0\.1:\([0-9][0-9]*\).*/\1/p' \
    "$TMP/nginx/ns-gateway-test.conf" 2>/dev/null | head -1
}

# 真实机器上**镜像与容器同名**（llm-gateway-211），且镜像总是存在。
# ⚠ 必须清空上一场景的容器残留：否则本场景会「继承」一个在跑的容器，
#   断言读到的就不是本场景的状态了（2026-10-20 自己踩过：⑥ 场景误报）。
setup_docker_state() {
  nuke "$TMP/docker"
  mkdir -p "$TMP/docker/images"
  : > "$TMP/docker/images/llm-gateway-211"
}

# ── 夹具：前端产物与源码树 ────────────────────────────────────────────
# $1 = fresh|stale ; $2 = 是否留上一轮种子
make_web_fixture() {
  local mode="$1" with_prev="${2:-no}"
  nuke "$TMP/repo"; nuke "$TMP/data"
  mkdir -p "$TMP/repo/web/dist" "$TMP/repo/web/src" \
           "$TMP/repo/web-mobile/dist" "$TMP/repo/web-mobile/src"
  printf '<html><body>app</body></html>\n' > "$TMP/repo/web/dist/index.html"
  printf '<html><body>m</body></html>\n' > "$TMP/repo/web-mobile/dist/index.html"
  printf 'export const a = 1\n' > "$TMP/repo/web/src/app.ts"
  printf 'export const b = 1\n' > "$TMP/repo/web-mobile/src/app.ts"
  # 固定时间戳：产物 2026-01-01，源码 2026-01-02 ⇒ 源码更新 ⇒ 陈旧
  touch -t 202601010000 "$TMP/repo/web/dist/index.html" "$TMP/repo/web-mobile/dist/index.html"
  touch -t 202601020000 "$TMP/repo/web/src/app.ts" "$TMP/repo/web-mobile/src/app.ts"
  if [[ "$mode" == "fresh" ]]; then
    touch -t 202601030000 "$TMP/repo/web/dist/index.html" "$TMP/repo/web-mobile/dist/index.html"
  fi
  mkdir -p "$TMP/data"
  if [[ "$with_prev" == "yes" ]]; then
    mkdir -p "$TMP/data/web/dist" "$TMP/data/web-mobile/dist"
    printf 'OLD\n' > "$TMP/data/web/dist/index.html"
    printf 'OLD\n' > "$TMP/data/web-mobile/dist/index.html"
  fi
}

echo "== ① 守卫在前：判定失败时旧槽必须毫发无损 =="
# 2026-10-20 事故形态：陈旧前端 → 守卫拒绝 → 旧容器被拆 → 502。
# 蓝绿形态下守卫拒绝更早：候选槽都没起，旧槽一直在服务。
make_web_fixture stale no
setup_docker_state
printf 'running\n' > "$TMP/docker/llm-gateway-211-a"
out="$(run_stub 'dd_preflight_web_dist "$DD_REPO_ROOT"')"
rc=$?
if [[ "$rc" == "9" ]]; then ok "陈旧前端被守卫拒绝（退出码 9）"
else bad "陈旧前端未被拒绝（rc=$rc 输出=${out}）"; fi
if grep -q '陈旧' <<<"$out"; then ok "拒绝原因点名「陈旧」"
else bad "拒绝原因未提陈旧：$out"; fi
check "拒绝后旧槽仍在服务" "running" "$(container_state llm-gateway-211-a)"
check "拒绝后候选槽压根没起" "absent" "$(container_state llm-gateway-211-b)"

echo "== ② 切流前失败：abort 丢候选槽，upstream 仍指旧槽 =="
make_web_fixture fresh no
setup_docker_state
printf 'running\n' > "$TMP/docker/llm-gateway-211-a"
printf 'running\n' > "$TMP/docker/llm-gateway-211-b"
make_nginx_conf 8781
out="$(run_stub 'dd_fail "验收失败"')"
rc=$?
if [[ "$rc" == "9" ]]; then ok "切流前失败以非零退出（rc=9）"
else bad "切流前失败未以非零退出（rc=${rc}）"; fi
check "abort 后候选槽被丢弃" "absent" "$(container_state llm-gateway-211-b)"
check "abort 后旧槽仍在服务" "running" "$(container_state llm-gateway-211-a)"
check "abort 后 upstream 仍指旧槽" "8781" "$(upstream_port_now)"
if grep -q '回退本次部署' <<<"$out"; then ok "abort 动作有明确日志"
else bad "看不到 abort 日志：$out"; fi

echo "== ③ 切流后失败：abort 必须把 upstream 切回并把旧槽拉回 =="
make_web_fixture fresh no
setup_docker_state
printf 'stopped\n' > "$TMP/docker/llm-gateway-211-a"
printf 'running\n' > "$TMP/docker/llm-gateway-211-b"
make_nginx_conf 8782
out="$(run_stub 'DD_SWITCHED=1; DD_OLD_STOPPED=1; dd_fail "公网验收失败"')"
rc=$?
if [[ "$rc" == "9" ]]; then ok "切流后失败以非零退出（rc=9）"
else bad "切流后失败未以非零退出（rc=${rc}）"; fi
check "abort 后候选槽被丢弃" "absent" "$(container_state llm-gateway-211-b)"
check "abort 后旧槽被拉回服务" "running" "$(container_state llm-gateway-211-a)"
check "abort 后 upstream 切回旧槽" "8781" "$(upstream_port_now)"
if grep -q 'upstream 已切回旧槽' <<<"$out"; then ok "切回动作有明确日志"
else bad "看不到切回日志：$out"; fi

echo "== ④ 切流本身：改主 server 行，backup 行不动，且先 nginx -t =="
make_web_fixture fresh no
setup_docker_state
make_nginx_conf 8781
out="$(run_stub 'dd_switch_upstream 8782')"
rc=$?
check "切流返回成功" "0" "$rc"
check "upstream 主 server 行已改到候选端口" "8782" "$(upstream_port_now)"
if grep -q '100.106.169.63:8782.*backup' "$TMP/nginx/ns-gateway-test.conf"; then
  ok "backup 行未被改动（流量不会误送到别的机器）"
else
  bad "切流把 backup 行也改了 —— 会把流量送到另一台主机"
fi
calls="$(cat "$TMP/docker/.calls" 2>/dev/null || true)"
if grep -q 'nginx -t' <<<"$calls"; then ok "切流先跑 nginx -t"
else bad "切流没跑 nginx -t —— 配置坏了也照样 reload"; fi
if grep -q 'nginx -s reload' <<<"$calls"; then ok "nginx -t 通过后 reload"
else bad "切流没有 reload —— upstream 文件改了但流量没切"; fi

echo "== ⑤ nginx -t 失败时不得 reload（配置坏了不能切流）=="
make_web_fixture fresh no
setup_docker_state
make_nginx_conf 8781
out="$(run_stub 'DD_STUB_NGINX_T_FAIL=1; dd_switch_upstream 8782')"
rc=$?
if [[ "$rc" != "0" ]]; then ok "nginx -t 失败 ⇒ 切流失败（rc=${rc}）"
else bad "nginx -t 失败但切流报成功"; fi
calls="$(cat "$TMP/docker/.calls" 2>/dev/null || true)"
if grep -q 'nginx -s reload' <<<"$calls"; then
  bad "nginx -t 失败仍然 reload 了"
else
  ok "nginx -t 失败时没有 reload"
fi
check "nginx -t 失败后 upstream 未被改" "8781" "$(upstream_port_now)"

echo "== ⑥ 种子按槽隔离 ⇒ 回退不需要还原种子 =="
# 蓝绿把种子分到 web/<slot>/dist、web-mobile/<slot>/dist、version/<slot>.json：
# 候选槽的种子化**只写自己那格**，旧槽那格原样不动 ⇒ abort 不必还原种子，
# 也就不会出现「旧二进制 + 新 SPA」的错配组合。
make_web_fixture fresh no
setup_docker_state
mkdir -p "$TMP/data/web/a/dist" "$TMP/data/web-mobile/a/dist" "$TMP/data/version"
printf 'OLD\n' > "$TMP/data/web/a/dist/index.html"
printf 'OLD\n' > "$TMP/data/web-mobile/a/dist/index.html"
printf '{"version":"old"}\n' > "$TMP/data/version/a.json"
out="$(run_stub '
  mkdir -p "$DD_GW_DATA_ROOT/web/$DD_CANDIDATE_SLOT" "$DD_GW_DATA_ROOT/web-mobile/$DD_CANDIDATE_SLOT" "$DD_GW_DATA_ROOT/version"
  cp -R "$DD_WEB_DIST_SRC" "$DD_GW_DATA_ROOT/web/$DD_CANDIDATE_SLOT/dist"
  cp -R "$DD_MOBILE_DIST_SRC" "$DD_GW_DATA_ROOT/web-mobile/$DD_CANDIDATE_SLOT/dist"
  cp -f "$DD_REPO_ROOT/version.json" "$DD_GW_DATA_ROOT/version/$DD_CANDIDATE_SLOT.json" 2>/dev/null || true
')"
check "旧槽的 web 种子未被本轮覆盖" "OLD" "$(cat "$TMP/data/web/a/dist/index.html" 2>/dev/null)"
check "旧槽的移动端种子未被本轮覆盖" "OLD" "$(cat "$TMP/data/web-mobile/a/dist/index.html" 2>/dev/null)"
check "旧槽的版本戳未被本轮覆盖" '{"version":"old"}' "$(cat "$TMP/data/version/a.json" 2>/dev/null)"

echo "== ⑦ 同名镜像在场、容器不在场：不得误判（2026-10-20 真机踩到）=="
# 真机形态：镜像叫 llm-gateway-211，容器也叫 llm-gateway-211。`docker inspect`
# 不带 --type 时同名镜像也命中 ⇒ 「容器不在场」被判成「在场」。
make_web_fixture fresh no
setup_docker_state          # 只有同名镜像，没有容器
out="$(run_stub 'if docker inspect --type container "llm-gateway-211" >/dev/null 2>&1; then echo INSPECTED; else echo ABSENT; fi')"
if grep -q 'ABSENT' <<<"$out"; then ok "容器不在场时 --type container 判定为不在场"
else bad "容器不在场却被判成在场（inspect 命中了同名镜像）"; fi
calls="$(cat "$TMP/docker/.calls" 2>/dev/null || true)"
if grep -q 'rename' <<<"$calls"; then
  bad "容器不在场却发起了 rename"
else
  ok "容器不在场时不发起 rename"
fi

echo "== ⑧ 前端新鲜度判定的四种结局 =="
# 直接驱动 dd_check_web_dist（返回码是契约：0/1/2/3）
check_code() {
  local desc="$1" want="$2"; shift 2
  local got=0
  ( set -uo pipefail
    DD_GW_DATA_ROOT="$TMP/data"; DD_WEB_CHECK_ERR=""
    source "$TMP/lib.sh"
    dd_check_web_dist "$@" || got=$?
    printf '%s' "$got" ) > "$TMP/code.out"
  got="$(cat "$TMP/code.out")"
  check "$desc" "$want" "$got"
}
make_web_fixture fresh no
check_code "产物比源码新 ⇒ 通过" 0 "$TMP/repo/web/dist" "$TMP/repo/web/src" web
make_web_fixture stale no
check_code "产物比源码旧 ⇒ 陈旧" 1 "$TMP/repo/web/dist" "$TMP/repo/web/src" web
make_web_fixture stale yes
check_code "缺失但上一轮种子还在（平铺布局）⇒ 无逃生舱" 3 "$TMP/repo/nonexistent" "$TMP/repo/web/src" web
# ★ 蓝绿按槽布局的「上一轮种子」也必须被认出来（2026-10-20 修的真缺陷）：
#   槽位隔离后种子落在 <root>/<label>/<slot>/dist；若只认平铺路径，
#   这条探测恒为「不在」⇒ 结论从「硬拒绝」掉成「逃生舱可放行」⇒
#   逃生舱会把**没有前端**的版本发上线（用户看到白页）。
#   ⇒ 单独造「只有按槽那份」的场景来钉。
nuke "$TMP/data/web/dist"
mkdir -p "$TMP/data/web/a/dist"
printf 'OLD\n' > "$TMP/data/web/a/dist/index.html"
check_code "缺失但上一轮种子还在（按槽布局）⇒ 无逃生舱" 3 "$TMP/repo/nonexistent" "$TMP/repo/web/src" web
nuke "$TMP/data"
check_code "缺失且无上一轮种子 ⇒ 可走纯 API" 2 "$TMP/repo/nonexistent" "$TMP/repo/web/src" web

echo "== ⑧b 逃生舱在 preflight 层必须真的放行 =="
# 2026-10-20 实测踩到：dd_preflight_web_dist 在「逃生舱放行」时返回 **1**，
# 而调用方写的是 `dd_preflight_web_dist ... || exit 1` ⇒ 返回 1 = 失败
# ⇒ **带了 DD_ALLOW_STALE_WEB=1 也照样退出** ⇒ 逃生舱形同虚设。
# ⇒ 判据直接驱动 preflight 函数，看它的**返回码**。
preflight_rc() {
  local allow="$1" root="$2"
  # 用**子 shell 的退出码**当读数：`die` 会直接 exit 子 shell，
  # 写文件那行根本执行不到（文件会是空的 ⇒ 读数丢失）。
  ( set -uo pipefail
    DD_GW_DATA_ROOT="$TMP/data"
    DD_WEB_CHECK_ERR=""
    DD_ALLOW_STALE_WEB="$allow"
    # ⚠ 必须自带 log/warn/die：被测函数里会调它们。没定义时 `die` 变成
    #   「command not found」(127) ⇒ 函数**继续往下跑**并返回 0 ⇒ 断言
    #   读到的不是「被拒绝」而是「没报错」—— 又是量具自己的坑。
    log()  { printf '[log] %s\n' "$*" >&2; }
    warn() { printf '[warn] %s\n' "$*" >&2; }
    die()  { printf '[die] %s\n' "$*" >&2; exit 9; }
    source "$TMP/lib.sh"
    dd_preflight_web_dist "$root" >/dev/null 2>&1
  )
  local rc=$?
  printf '%s' "$rc"
}
make_web_fixture stale no
check "preflight：陈旧且无逃生舱 ⇒ 拒绝（die → 9）" "9" "$(preflight_rc 0 "$TMP/repo")"
check "preflight：陈旧且有逃生舱 ⇒ 放行（返回 0）" "0" "$(preflight_rc 1 "$TMP/repo")"
# 「缺失但上一轮种子还在」必须**硬拒绝**（逃生舱也不放行）：
# 造一个**没有 dist** 的 repo 根 + 宿主上留着上一轮种子。
make_web_fixture stale yes
mkdir -p "$TMP/emptyroot"
check "preflight：缺失但上一轮种子还在 ⇒ 逃生舱也不放行（die）" "9" "$(preflight_rc 1 "$TMP/emptyroot")"

echo "== ⑧c 切流后的公网身份复验必须**轮询**（nginx reload 是异步的）=="
# 2026-10-20 真机实测：切流后**立刻**探一次公网 /version，拿到的是**旧槽**的
# 版本戳 ⇒ 误判「半切换态」⇒ 触发 abort（abort 本身对，但判据太急，把一次
# 成功的部署判成失败）。真因：`nginx -s reload` 只给 master 发信号，**新 worker
# 起来之前旧 worker 仍用旧 upstream 应答新连接**。
# ⇒ 判据：把公网版本探针替身成「前 N 次返回旧版本、第 N+1 次返回新版本」，
#   切流复验必须**最终通过**（轮询覆盖了 reload 窗口）。
make_web_fixture fresh no
setup_docker_state
make_nginx_conf 8781
out="$(run_stub '
  DD_BUILD_SHA="abc12345"
  DD_SWITCH_VERIFY_TRIES=6
  dd_public_version() {
    # ⚠ 计数必须落**文件**：被测函数用 `_after="$(dd_public_version)"` 读结果
    #   ⇒ 命令替换在**子 shell**里跑 ⇒ shell 变量计数器每���都从 0 开始
    #   （实测：_n 永远是 1）⇒ 替身根本演不出「先旧后新」。
    _c=$(cat "$DD_STUB_DIR/.pubn" 2>/dev/null || echo 0)
    _c=$((_c+1)); printf "%s" "$_c" > "$DD_STUB_DIR/.pubn"
    if [[ "$_c" -le 3 ]]; then
      printf "{\"git_sha\":\"old-old-old\",\"version\":\"2.5.8-old-20261020-211\"}"
    else
      printf "{\"git_sha\":\"abc12345\",\"version\":\"2.5.8-abc12345-20261020-211\"}"
    fi
  }
  dd_switch_and_verify
')"
rc=$?
if [[ "$rc" == "0" ]]; then
  ok "reload 窗口内拿到旧版本时，轮询复验最终通过（不误判半切换态）"
else
  bad "reload 窗口导致误判：复验未轮询就下结论（rc=${rc}）—— 一次成功部署被判成失败"
  printf '%s\n' "$out" | tail -4 | sed 's/^/      /'
fi
# 反向：公网**始终**是旧版本 ⇒ 必须判失败（轮询不能把真半切换态放过）
out="$(run_stub '
  DD_BUILD_SHA="abc12345"
  DD_SWITCH_VERIFY_TRIES=3
  dd_public_version() { printf "{\"git_sha\":\"old-old-old\"}"; }
  dd_switch_and_verify
')"
rc=$?
if [[ "$rc" != "0" ]]; then ok "公网始终是旧版本 ⇒ 判失败（轮询不会放过真半切换态）"
else bad "公网始终是旧版本却判成功 —— 半切换态被放过"; fi

echo "== ⑨ 结构不变量（顺序 / 切流 / 失败点走 abort）=="# ⚠ 顺序锚点必须落在**代码**上，不能落在注释/日志文案上 ——
#   2026-10-20 变异验证实测：首版锚在 `ANCHOR: d211-preflight-guard` 注释与
#   「起候选槽容器」「旧槽已停」这两句**文案**上 ⇒ 变异把代码搬走、注释留在原地
#   ⇒ 断言照样绿 ⇒ **判据钉不住它要钉的行为**（10 条变异里 1 条漏网）。
#   ⇒ 现在全部锚在可执行语句上。
pre_line="$(grep -n 'dd_preflight_web_dist "\$DD_REPO_ROOT" || exit 1' "$SCRIPT" | cut -d: -f1 | head -1)"
cand_line="$(grep -n 'docker run -d --name "\$DD_CANDIDATE_CONTAINER"' "$SCRIPT" | cut -d: -f1 | head -1)"
switch_line="$(grep -n 'dd_switch_and_verify || dd_fail' "$SCRIPT" | cut -d: -f1 | head -1)"
stop_line="$(grep -n 'docker stop "\$DD_ACTIVE_CONTAINER"' "$SCRIPT" | cut -d: -f1 | head -1)"
if [[ -n "$pre_line" && -n "$cand_line" && "$pre_line" -lt "$cand_line" ]]; then
  ok "守卫在起候选槽之前（${pre_line} < ${cand_line}）"
else
  bad "守卫不在起候选槽之前（守卫=${pre_line:-无} 候选=${cand_line:-无}）"
fi
if [[ -n "$cand_line" && -n "$switch_line" && "$cand_line" -lt "$switch_line" ]]; then
  ok "切流在候选槽起来之后（${cand_line} < ${switch_line}）"
else
  bad "切流不在候选槽之后（候选=${cand_line:-无} 切流=${switch_line:-无}）—— 切了流才发现新版本起不来"
fi
if [[ -n "$switch_line" && -n "$stop_line" && "$switch_line" -lt "$stop_line" ]]; then
  ok "停旧槽在切流之后（${switch_line} < ${stop_line}）"
else
  bad "停旧槽不在切流之后（切流=${switch_line:-无} 停旧槽=${stop_line:-无}）—— 先停旧槽＝先停机"
fi
# 候选槽验收必须在切流之前（切流后验到的不是本次发布）
verify_line="$(grep -n 'dd_verify_web_surface || dd_fail' "$SCRIPT" | cut -d: -f1 | head -1)"
if [[ -n "$verify_line" && -n "$switch_line" && "$verify_line" -lt "$switch_line" ]]; then
  ok "候选槽网页面验收在切流之前（${verify_line} < ${switch_line}）"
else
  bad "网页面验收不在切流之前（验收=${verify_line:-无} 切流=${switch_line:-无}）"
fi
# 失败点必须走 dd_fail（= abort + 退出），不是裸 die
for anchor in \
  'dd_fail "候选槽容器启动失败' \
  'dd_fail "切流失败' \
  'dd_fail "公网入口验收失败' \
  'dd_fail "网页面验收失败'; do
  if grep -qF "$anchor" "$SCRIPT"; then
    ok "失败点走 abort：$anchor"
  else
    bad "失败点未走 abort（缺 ${anchor}）—— 一次部署失败会变成停机"
  fi
done
if grep -qE '\|\| die "(候选槽容器启动失败|切流失败|公网入口验收失败|网页面验收失败)' "$SCRIPT"; then
  bad "仍有裸 die 的失败点（裸 die = 不 abort = 停机）"
else
  ok "没有裸 die 的失败点"
fi
# nginx -t 失败时必须把配置**还原**（行为层是场景⑤；这里钉住还原这一腿
# 不会被人挪走）：磁盘停在「已改未生效」= 潜伏半切换态 —— 此后任何一次
# nginx reload（人工/systemd/crontab）都会把没验证过的配置加载进去。
switch_body="$(sed -n "/^dd_switch_upstream()/,/^}/p" "$SCRIPT")"
if grep -qF 'cp -f "$_bak" "$_conf"' <<<"$switch_body"; then
  ok "nginx -t 失败时把 upstream 配置还原（不留半切换态）"
else
  bad "nginx -t 失败时不还原配置 —— 磁盘停在已改未生效状态，下次 reload 会误切"
fi
# 备份必须**每次**写「改前内容」（只在不存在时写 ⇒ 还原会回退到错误版本）
# ★ 2026-10-23：切流判定逻辑已挪到 scripts/ops/nginx-upstream-port.py（掩码定位），
#   「备份每次写改前内容」这条行为随之迁到该脚本 ⇒ 断言必须指向**行为真正所在
#   的位置**，否则它就成了一条钉在空壳上的恒绿门（函数体里已经没有那行字了）。
HELPER="$ROOT/scripts/ops/nginx-upstream-port.py"
if [[ -f "$HELPER" ]] \
   && grep -qF "open(backup_path, 'w'" <<<"$(cat "$HELPER")" \
   && grep -qF -- '--backup "$_bak"' <<<"$switch_body"; then
  ok "切流备份每次都写改前内容（还原不会回退到更早的版本）"
else
  bad "切流备份不是每次都写改前内容 —— nginx -t 失败时的还原会拿到旧版本"
fi
# abort 里必须有「把 upstream 切回旧槽」这一腿（没有它 = 半切换态留在线上）
abort_body="$(sed -n "/^dd_abort_deploy()/,/^}/p" "$SCRIPT")"
if grep -qF 'dd_switch_upstream "$(dd_slot_port "$DD_ACTIVE_SLOT")"' <<<"$abort_body"; then
  ok "abort 里有把 upstream 切回旧槽的一腿"
else
  bad "abort 里没有切回旧槽 —— 失败后线上停在半切换态"
fi
if grep -qF 'docker rm -f "$(dd_container_name "$DD_CANDIDATE_SLOT")"' <<<"$abort_body"; then
  ok "abort 会丢弃候选槽容器"
else
  bad "abort 不丢弃候选槽 —— 端口被占，下一轮候选槽起不来"
fi
# 停旧槽是「停而不删」
if grep -qF 'docker stop "$DD_ACTIVE_CONTAINER"' "$SCRIPT" \
   && ! grep -qF 'docker rm -f "$DD_ACTIVE_CONTAINER"' "$SCRIPT"; then
  ok "旧槽是「停而不删」（保留容器与镜像，可 docker start 回滚）"
else
  bad "旧槽不是停而不删 —— 回滚材料被删了"
fi
# 容器存在性判断必须带 --type container（不被同名镜像骗过）
if grep -qF 'docker inspect --type container' "$SCRIPT"; then
  ok "容器存在性判断用 docker inspect --type container（不被同名镜像骗过）"
else
  bad "容器存在性判断没带 --type container —— 同名镜像会让「容器不在场」判断失真"
fi
# 部署机侧也必须有一道门（最早可判点：sync 之前）
if grep -q 'dd_preflight_web_dist "$DD_REPO_ROOT" repo || exit 1' "$SCRIPT"; then
  ok "部署机侧（--via）也有同步前的前置门"
else
  bad "部署机侧没有同步前的门—— 陈旧产物会被同步/构建/推送才发现"
fi
# 活跃槽判定：upstream 实际端口优先于状态文件（事实优先于账本）
active_body="$(sed -n "/^dd_active_slot()/,/^}/p" "$SCRIPT")"
if grep -qF 'dd_upstream_port' <<<"$active_body"; then
  ok "活跃槽以 nginx upstream 实际端口为事实优先（状态文件只是账本）"
else
  bad "活跃槽不读 upstream 实际端口 —— 手工改过 nginx 时切流方向会反"
fi

printf '\n== 结果：%s 通过 / %s 失败 ==\n' "$pass" "$fail"
[[ "$fail" == "0" ]] || exit 1
exit 0
