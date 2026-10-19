#!/usr/bin/env bash
# =============================================================================
# deploy_211_teardown_order_test.sh — 211 部署的「拆服务 vs 判定」顺序门
# =============================================================================
# 为什么要有这个门（2026-10-20 事故）：
#   deploy-211-docker.sh 的旧顺序是
#       docker rm -f 旧容器 → 才做前端新鲜度判定 → 判定失败 die
#   ⇒ 一次「部署失败」被放大成「服务下线」：旧容器已删 + 新容器未起，
#     211 上 nginx 的 upstream（127.0.0.1:8781）无监听 ⇒ llm.itestu.cn 502。
#   根因是**顺序**不是规则：守卫本身是对的（陈旧前端确实不该发），
#   但它跑在了拆服务之后。
#
# 154/245 靠蓝绿（候选实例先起、验证过才切流）天然免疫；211 是单容器 docker
# 形态，没有候选端口，所以必须自己实现「可回滚的退役」。
#
# 本门三层：
#   ① 行为层：把被测脚本里抽出来的**真函数**用 docker 替身驱动，证明
#      - 守卫拒绝时旧容器仍在服务（名字没变、没被改名/停掉）；
#      - 拆后失败时旧容器被原样拉回并重新拉起；
#      - 回滚时旧种子被还原（不出现「旧二进制 + 新 SPA」错配）。
#   ② 语义层：前端新鲜度判定的四种结局（新鲜/陈旧/缺失/缺失但上一轮种子还在）。
#   ③ 结构层：顺序不变量（守卫在退役之前；退役之后不再硬删在跑的容器；
#      失败点走 d211_fail 而不是裸 die）。
#
# 判据纪律：①③ 两层必须同时存在。只留 ①（函数本身对）⇒ 有人把守卫挪到
# 退役之后，门照样全绿；只留 ③（文本形状对）⇒ 有人把回滚实现改坏，门也
# 未必抓得住。2026-10-17 审计轮已记过这个教训（结构门不覆盖行为门）。
# =============================================================================
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$ROOT/scripts/deploy-211-docker.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

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

FNS="d211_web_dist_newer_source d211_check_web_dist d211_preflight_web_dist
     d211_backup_seed d211_restore_seed d211_retire_gateway d211_rollback_gateway d211_fail"
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
  printf '%s\n' "$cmd $*" >> "$D211_STUB_DIR/.calls"
  local type=""
  while [[ "${1:-}" == --type ]]; do type="$2"; shift 2; done
  case "$cmd" in
    inspect)
      if [[ "$type" == "container" ]]; then
        [[ -f "$D211_STUB_DIR/$1" ]]
      else
        # 不带 --type：容器或镜像任一命中即 rc=0（真实 docker 的行为）
        [[ -f "$D211_STUB_DIR/$1" || -f "$D211_STUB_DIR/images/$1" ]]
      fi
      ;;
    rename)
      [[ -f "$D211_STUB_DIR/$1" ]] || return 1
      mv "$D211_STUB_DIR/$1" "$D211_STUB_DIR/$2"
      ;;
    stop)
      [[ -f "$D211_STUB_DIR/$1" ]] || return 1
      printf 'stopped\n' > "$D211_STUB_DIR/$1"
      ;;
    start)
      [[ -f "$D211_STUB_DIR/$1" ]] || return 1
      printf 'running\n' > "$D211_STUB_DIR/$1"
      ;;
    rm)
      local a
      for a in "$@"; do
        case "$a" in -*) continue ;; esac
        rm -f "$D211_STUB_DIR/$a"
      done
      ;;
    run)
      local a name="" want=0
      for a in "$@"; do
        if (( want )); then name="$a"; want=0; continue; fi
        if [[ "$a" == "--name" ]]; then want=1; fi
      done
      [[ "${D211_STUB_RUN_FAIL:-0}" == "1" ]] && return 1
      printf 'running\n' > "$D211_STUB_DIR/$name"
      ;;
    *) return 0 ;;
  esac
}

# run_stub <代码> —— 在带 docker 替身的环境里跑一段代码；输出 = stdout+stderr
run_stub() {
  local code="$1"
  (
    set -uo pipefail
    D211_STUB_DIR="$TMP/docker"
    D211_GW_CONTAINER="llm-gateway-211"
    D211_PREV_CONTAINER="llm-gateway-211-prev"
    D211_GW_DATA_ROOT="$TMP/data"
    D211_REPO_ROOT="$TMP/repo"
    D211_WEB_DIST_SRC="$TMP/repo/web/dist"
    D211_MOBILE_DIST_SRC="$TMP/repo/web-mobile/dist"
    D211_WEB_SRC_DIR="$TMP/repo/web/src"
    D211_MOBILE_SRC_DIR="$TMP/repo/web-mobile/src"
    D211_ALLOW_STALE_WEB="0"
    D211_HAVE_PREV=0
    D211_SEED_BACKUP=""
    log()  { printf '[log] %s\n' "$*" >&2; }
    warn() { printf '[warn] %s\n' "$*" >&2; }
    die()  { printf '[die] %s\n' "$*" >&2; exit 9; }
    docker() { docker_stub "$@"; }
    # 每次调用都从空的调用流水开始（否则上一场景的 rename 会污染本场景的断言）
    : > "$D211_STUB_DIR/.calls"
    source "$TMP/lib.sh"
    eval "$code"
  ) 2>&1
}

container_state() {
  local name="$1"
  if [[ -f "$TMP/docker/$name" ]]; then cat "$TMP/docker/$name"; else printf 'absent'; fi
}

# 真实机器上**镜像与容器同名**（llm-gateway-211），且镜像总是存在。
# ⚠ 必须清空上一场景的容器残留：否则本场景会「继承」一个在跑的容器，
#   断言读到的就不是本场景的状态了（2026-10-20 自己踩过：⑥ 场景误报）。
setup_docker_state() {
  rm -rf "$TMP/docker"
  mkdir -p "$TMP/docker/images"
  : > "$TMP/docker/images/llm-gateway-211"
}

# ── 夹具：前端产物与源码树 ────────────────────────────────────────────
# $1 = fresh|stale ; $2 = 是否留上一轮种子
make_web_fixture() {
  local mode="$1" with_prev="${2:-no}"
  rm -rf "$TMP/repo" "$TMP/data"
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

echo "== ① 守卫在前：判定失败时旧容器必须毫发无损 =="
# 2026-10-20 事故形态：陈旧前端 → 守卫拒绝 → 旧容器被拆 → 502。
# 这里跑脚本的**真实顺序**（守卫 → 退役），断言守卫一拒绝就到不了退役。
make_web_fixture stale no
setup_docker_state
printf 'running\n' > "$TMP/docker/llm-gateway-211"
out="$(run_stub 'd211_preflight_web_dist "$D211_REPO_ROOT"')"
rc=$?
if [[ "$rc" == "9" ]]; then ok "陈旧前端被守卫拒绝（退出码 9）"
else bad "陈旧前端未被拒绝（rc=$rc 输出=${out}）"; fi
if printf '%s' "$out" | grep -q '陈旧'; then ok "拒绝原因点名「陈旧」"
else bad "拒绝原因未提陈旧：$out"; fi
check "拒绝后旧容器仍在服务" "running" "$(container_state llm-gateway-211)"
check "拒绝后旧容器没被改名退役" "absent" "$(container_state llm-gateway-211-prev)"

echo "== ② 拆后失败：旧容器必须被原样拉回 =="
make_web_fixture fresh no
setup_docker_state
printf 'running\n' > "$TMP/docker/llm-gateway-211"
out="$(run_stub 'd211_backup_seed; d211_retire_gateway; d211_fail "网关容器启动失败"')"
rc=$?
if [[ "$rc" == "9" ]]; then ok "拆后失败以非零退出（rc=9）"
else bad "拆后失败未以非零退出（rc=${rc}）"; fi
check "回滚后旧容器名字回来了" "running" "$(container_state llm-gateway-211)"
check "回滚后没有残留退役容器" "absent" "$(container_state llm-gateway-211-prev)"
if printf '%s' "$out" | grep -q '已回滚到上一版容器'; then ok "回滚动作有明确日志"
else bad "看不到回滚日志：$out"; fi

echo "== ③ 回滚必须还原种子（旧二进制不能配新 SPA）=="
make_web_fixture fresh no
mkdir -p "$TMP/data/web/dist"
setup_docker_state
printf 'running\n' > "$TMP/docker/llm-gateway-211"
printf 'OLD\n' > "$TMP/data/web/dist/index.html"
run_stub 'd211_backup_seed
           d211_retire_gateway
           rm -rf "$D211_GW_DATA_ROOT/web/dist"
           mkdir -p "$D211_GW_DATA_ROOT/web/dist"
           printf "NEW\n" > "$D211_GW_DATA_ROOT/web/dist/index.html"
           d211_fail "网关容器启动失败"' >/dev/null
check "回滚后 web/dist 回到旧种子" "OLD" "$(cat "$TMP/data/web/dist/index.html" 2>/dev/null)"

echo "== ⑥ 同名镜像在场、容器不在场：退役不得误判（2026-10-20 真机踩到）=="
# 真机形态：镜像叫 llm-gateway-211，容器也叫 llm-gateway-211。`docker inspect`
# 不带 --type 时同名镜像也命中 ⇒ 「容器不在场」被判成「在场」⇒ 随后 rename 报
# "No such container"，退役变成噪声警告。
make_web_fixture fresh no
setup_docker_state          # 只有同名镜像，没有容器
out="$(run_stub 'd211_retire_gateway')"
calls="$(cat "$TMP/docker/.calls" 2>/dev/null || true)"
if printf '%s' "$calls" | grep -q 'rename'; then
  bad "容器不在场却发起了 rename（inspect 命中了同名镜像）"
  printf '%s\n' "$calls" | sed 's/^/      /'
else
  ok "容器不在场时不发起 rename（inspect 用 --type container 限定了类型）"
fi
if printf '%s' "$out" | grep -q '改名失败'; then
  bad "退役输出了「改名失败」噪声：$out"
else
  ok "退役在容器不在场时安静返回"
fi

echo "== ④ 前端新鲜度判定的四种结局 =="
# 直接驱动 d211_check_web_dist（返回码是契约：0/1/2/3）
check_code() {
  local desc="$1" want="$2"; shift 2
  local got=0
  ( set -uo pipefail
    D211_GW_DATA_ROOT="$TMP/data"; D211_WEB_CHECK_ERR=""
    source "$TMP/lib.sh"
    d211_check_web_dist "$@" || got=$?
    printf '%s' "$got" ) > "$TMP/code.out"
  got="$(cat "$TMP/code.out")"
  check "$desc" "$want" "$got"
}
make_web_fixture fresh no
check_code "产物比源码新 ⇒ 通过" 0 "$TMP/repo/web/dist" "$TMP/repo/web/src" web
make_web_fixture stale no
check_code "产物比源码旧 ⇒ 陈旧" 1 "$TMP/repo/web/dist" "$TMP/repo/web/src" web
make_web_fixture stale yes
check_code "缺失但上一轮种子还在 ⇒ 无逃生舱" 3 "$TMP/repo/nonexistent" "$TMP/repo/web/src" web
rm -rf "$TMP/data"
check_code "缺失且无上一轮种子 ⇒ 可走纯 API" 2 "$TMP/repo/nonexistent" "$TMP/repo/web/src" web

echo "== ⑤ 结构不变量（顺序 / 不硬删 / 失败点走回滚）=="
pre_line="$(grep -n 'ANCHOR: d211-preflight-guard' "$SCRIPT" | cut -d: -f1 | head -1)"
retire_line="$(grep -n 'ANCHOR: d211-retire-old-container' "$SCRIPT" | cut -d: -f1 | head -1)"
if [[ -n "$pre_line" && -n "$retire_line" && "$pre_line" -lt "$retire_line" ]]; then
  ok "守卫在退役之前（${pre_line} < ${retire_line}）"
else
  bad "守卫不在退役之前（守卫=${pre_line:-无} 退役=${retire_line:-无}）—— 守卫失败会变成停机"
fi
# 退役之后不得再出现「硬删在跑的容器」——回滚删的是**新**容器，
# 退役容器用的是 ${D211_PREV_CONTAINER}。
after_retire="$(sed -n "$((retire_line + 1)),\$p" "$SCRIPT")"
if printf '%s' "$after_retire" | grep -qF 'docker rm -f "$D211_GW_CONTAINER"'; then
  bad "退役之后仍硬删在跑的容器（docker rm -f \${D211_GW_CONTAINER}）—— 失败不可回滚"
else
  ok "退役之后不再硬删在跑的容器"
fi
retire_body="$(sed -n "/^d211_retire_gateway()/,/^}/p" "$SCRIPT")"
if printf '%s' "$retire_body" | grep -qF 'docker rename "$D211_GW_CONTAINER" "$D211_PREV_CONTAINER"' \
   && printf '%s' "$retire_body" | grep -qF 'docker stop "$D211_PREV_CONTAINER"' \
   && ! printf '%s' "$retire_body" | grep -qF 'docker rm -f "$D211_GW_CONTAINER"'; then
  ok "退役是「改名 + 停机」，不是硬删（回滚的原材料还在）"
else
  bad "退役实现不是「改名 + 停机」或仍含硬删"
fi
for anchor in \
  'docker run -d --name "$D211_GW_CONTAINER"' \
  'd211_fail "网关部署失败"' \
  'd211_fail "部署验证失败：readyz mode=lite' \
  'd211_fail "部署验证失败：readyz 未证明 DB/Redis 就绪'; do
  if printf '%s' "$after_retire" | grep -qF "$anchor"; then
    ok "失败点走回滚：$anchor"
  else
    bad "失败点未走回滚（缺 ${anchor}）—— 一次部署失败会变成停机"
  fi
done
if printf '%s' "$after_retire" | grep -qE '\|\| die "(网关容器启动失败|网关部署失败|部署验证失败)'; then
  bad "退役之后仍有裸 die 的失败点（裸 die = 不回滚 = 停机）"
else
  ok "退役之后没有裸 die 的失败点"
fi
if grep -qF 'docker inspect --type container "$D211_GW_CONTAINER"' "$SCRIPT" \
   && grep -qF 'docker inspect --type container "$D211_PREV_CONTAINER"' "$SCRIPT"; then
  ok "容器存在性判断用 docker inspect --type container（不被同名镜像骗过）"
else
  bad "容器存在性判断没带 --type container —— 同名镜像（llm-gateway-211）会让「容器不在场」判断失真"
fi
# 部署机侧也必须有一道门（最早可判点：sync 之前）
if grep -q 'd211_preflight_web_dist "$D211_REPO_ROOT" || exit 1' "$SCRIPT"; then
  ok "部署机侧（--via）也有同步前的前置门"
else
  bad "部署机侧没有同步前的门—— 陈旧产物会被同步/构建/推送才发现"
fi

printf '\n== 结果：%s 通过 / %s 失败 ==\n' "$pass" "$fail"
[[ "$fail" == "0" ]] || exit 1
exit 0
