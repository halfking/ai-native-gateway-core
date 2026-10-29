#!/usr/bin/env bash
# =============================================================================
# deploy_via_identity_gates_iso_test.sh — 部署引擎三道版本身份闸的隔离 --via 实测
# =============================================================================
# 承接 2026-10-28 批判式审计第二轮 §6 遗留项②：「211 部署引擎改动的非生产
# 验证」——把 deploy-seamless-docker.sh 的三道身份闸（部署机侧取号 /
# --via 转发前 / 目标机侧）在一个**隔离环境**里跑一遍真实 --via 流程。
#
# 与 tests/deploy_seamless_version_identity_test.sh 的分工：
#   · 那个门是**抽取式**行为门：把引擎里身份闸的真代码抠出来驱动（9 断言 /
#     3 变异臂），锁的是「闸的代码本身真的拒绝」；
#   · 本门是**集成**门：真实引擎整个跑完 --via 流程（取号 → 前端门 → tar
#     同步 → 转发前门 → 目标机引擎），锁的是「号在链路上每一跳的实值」。
#
# 隔离手段（本机零副作用）：
#   · 部署树 = 临时目录里的**真引擎副本** + 假前端产物（T1 无 .git / T2 带 git）；
#   · `ssh`/`scp` 用 PATH 垫片拦截：记录完整远端命令后**在本机执行**
#     （远端 = 本机里的另一个临时目录，模拟目标机）；
#   · 目标机语境 = PATH 垫片里再放 `uname`（报 Linux，越过 macOS 本机守卫）
#     与 `docker`（拒绝守护进程连接 ⇒ 引擎在身份闸之后、任何容器动作之前
#     fail-closed 终止）；
#   · 锁目录（部署锁 + 共享构建锁）钉进隔离 LOCK_TMP_ROOT，不与真实部署的
#     /tmp 锁相碰；前端构建用 DD_SKIP_WEB_BUILD=1 跳过（引擎自带的离线/CI
#     逃生舱），新鲜度门由假 dist（无 src 目录 ⇒ 判新鲜）真实通过。
#
# 实测断言（每条都对着「号在哪一跳、值是多少」）：
#   A 空号 → 拦截      部署树无 .git 且未声明 ⇒ 转发前门 die（错误落在部署机），
#                      且引擎 ssh 一次都不发生（不把空号发到远端）；
#   B 显式号 → 透传    export DD_BUILD_SHA=<声明号>（≠ git HEAD）⇒ 转发命令里
#                      是声明号、目标机引擎日志点名声明号（2026-10-28 修复：
#                      显式号不被 git 覆盖）；
#   C git 号 → 透传    未声明 ⇒ 从部署树 git 取号并原样转发；
#   D 目标机侧兜底闸   同步树（无 .git）上直接跑引擎且号为空 ⇒ 目标机侧
#                      fail-closed die（最后一道防线的实测）。
#
# 变异臂（门自己的牙，§527：变异臂的靶点必须真实落在被锁判据上）：
#   M1 转发前门降级为 log ⇒ A 的「拦截」形态必须消失：空号被发到目标机、
#      错误落在远端（= 批判式审计第二轮 §1.2 缺陷的现场复现）；
#   M2 取号段回退成无条件 git 覆盖（§1.1 缺陷）⇒ B 的「显式号透传」必须变成
#      「git 号上链路」（声明号被吃掉）。
#   变异只作用于隔离树里的引擎副本，仓内文件一个字节不动。
#
# 用法：bash tests/deploy_via_identity_gates_iso_test.sh
# =============================================================================
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENGINE_SRC="$REPO_ROOT/scripts/deploy-seamless-docker.sh"
RESOLVE_SRC="$REPO_ROOT/scripts/deploy-lib-resolve.sh"
# 启动时的仓内引擎指纹（用于末尾的隔离对照断言：拿它跟**结束时的同一文件**
# 比，而不是拿文件跟自己比——后者是恒真的空洞断言）。
ENGINE_MD5_AT_START="$(md5 -q "$ENGINE_SRC")"

PASS=0
FAIL=0
FAILED_NAMES=()

ok()  { PASS=$((PASS + 1)); printf '  ✓ %s\n' "$1"; }
bad() { FAIL=$((FAIL + 1)); FAILED_NAMES+=("$1"); printf '  ✗ %s\n' "$1"; }

ISO="$(mktemp -d "${TMPDIR:-/tmp}/deploy-iso.XXXXXX")"
trap 'rm -rf "$ISO"' EXIT
mkdir -p "$ISO/bin" "$ISO/remote-bin" "$ISO/logs" "$ISO/tmp"

# ── PATH 垫片：部署机侧（ssh/scp 拦截 + 记录）────────────────────────────
cat > "$ISO/bin/ssh" <<'SHIM'
#!/usr/bin/env bash
set -u
LOG="${ISO_SSH_LOG:?ISO_SSH_LOG not set}"
host="$1"; shift
printf '[ssh] host=%s cmd<<<%s>>>\n' "$host" "$*" >> "$LOG"
# ★ 真实 ssh 不转发本地环境变量——远端只拿到命令行上显式随行的 DD_*。
#   垫片必须用 env -i 模拟这一点：否则部署机侧导出的 LLM_GATEWAY_ROOT 等
#   会泄进「远端」，目标机的 DD_REPO_ROOT/.git 判定就不再反映真实同步树
#   形态，身份闸的可观测行为整体失真（本 harness 第一版实测踩中）。
exec env -i \
  PATH="${ISO_REMOTE_BIN:?}:/usr/bin:/bin:/usr/sbin:/sbin" \
  HOME="${HOME:-/var/root}" \
  bash -c "$*"
SHIM
cat > "$ISO/bin/scp" <<'SHIM'
#!/usr/bin/env bash
echo "scp: isolated harness — not expected in deploy-mode --via run" >&2
exit 64
SHIM
chmod +x "$ISO/bin/ssh" "$ISO/bin/scp"

# ── PATH 垫片：目标机语境（Linux + 无 docker 守护进程）───────────────────
printf '#!/bin/sh\necho Linux\n' > "$ISO/remote-bin/uname"
cat > "$ISO/remote-bin/docker" <<'SHIM'
#!/usr/bin/env bash
echo "docker: isolated harness denies daemon access" >&2
exit 1
SHIM
chmod +x "$ISO/remote-bin/uname" "$ISO/remote-bin/docker"

# ── 部署树：T1（无 .git）与 T2（git 检出，提交号 S）──────────────────────
make_tree() { # make_tree <目录>
  local t="$1"
  mkdir -p "$t/scripts" "$t/web/dist" "$t/web-mobile/dist"
  cp "$ENGINE_SRC" "$t/scripts/deploy-seamless-docker.sh"
  cp "$RESOLVE_SRC" "$t/scripts/deploy-lib-resolve.sh"
  printf '<html><body>iso web dist</body></html>\n' > "$t/web/dist/index.html"
  printf '<html><body>iso mobile dist</body></html>\n' > "$t/web-mobile/dist/index.html"
}
T1="$ISO/tree-nogit"; make_tree "$T1"
T2="$ISO/tree-git";   make_tree "$T2"
git -C "$T2" init -q -b main
git -C "$T2" -c user.email=iso@test -c user.name=iso add -A
git -C "$T2" -c user.email=iso@test -c user.name=iso commit -qm "iso tree"
GIT_SHA="$(git -C "$T2" rev-parse --short=8 HEAD)"
EXPLICIT_SHA="deadbeef"
if [[ "$GIT_SHA" == "$EXPLICIT_SHA" ]]; then
  echo "夹具冲突：git sha 恰好等于声明号 $EXPLICIT_SHA"; exit 1
fi

# ── 一次真实 --via 流程：真引擎在隔离部署树上整个跑一遍 ──────────────────
run_via() { # run_via <标签> <部署树> [额外环境: VAR=值]
  local tag="$1" tree="$2" env_extra="${3:-}"
  : > "$ISO/logs/$tag.ssh.log"
  (
    export PATH="$ISO/bin:$PATH"
    export TMPDIR="$ISO/tmp" LOCK_TMP_ROOT="$ISO/tmp"
    export LLM_GATEWAY_ROOT="$tree"
    export DD_TARGET=isol211 DD_HOST=isolated-target
    export DD_STATE_DIR="$ISO/state" DD_REMOTE_ROOT="$ISO/remote-root"
    export DD_GW_DATA_ROOT="$ISO/gwdata"
    export DD_SKIP_WEB_BUILD=1
    export ISO_SSH_LOG="$ISO/logs/$tag.ssh.log" ISO_REMOTE_BIN="$ISO/remote-bin"
    if [[ -n "$env_extra" ]]; then export "$env_extra"; fi
    bash "$tree/scripts/deploy-seamless-docker.sh" --via fake-target
  ) > "$ISO/logs/$tag.out" 2>&1
  echo $?
}

reset_remote() { rm -rf "$ISO/remote-root" "$ISO/state" "$ISO/gwdata"; }

# 变异：只改隔离树里的引擎副本（仓内文件不动）
mutate() { # mutate <引擎文件> <old> <new>
  python3 - "$1" "$2" "$3" <<'PY'
import pathlib, sys
p = pathlib.Path(sys.argv[1]); s = p.read_text()
old, new = sys.argv[2], sys.argv[3]
assert s.count(old) == 1, "mutation anchor count=%d" % s.count(old)
p.write_text(s.replace(old, new))
PY
}
restore_engine() { # restore_engine <树目录>
  cp "$ENGINE_SRC" "$1/scripts/deploy-seamless-docker.sh"
}

echo "== 隔离 --via 实测：部署引擎三道身份闸（仓 HEAD $(git -C "$REPO_ROOT" rev-parse --short=8 HEAD)，T2 git 号 ${GIT_SHA}）=="

# ── 场景 A：空号 → 拦截 ──────────────────────────────────────────────────
echo "-- 场景 A：部署树无 .git 且未声明号 ⇒ 部署机侧转发前门拦截"
reset_remote
rc_a=$(run_via a "$T1")
if grep -q "部署机侧取不到构建提交号" "$ISO/logs/a.out"; then
  ok "A1 拒绝信息落在部署机侧（转发前门文案）"
else
  bad "A1 拒绝信息落在部署机侧（转发前门文案）"
fi
if grep -q "cmd<<<cd .*bash scripts/deploy-seamless-docker.sh" "$ISO/logs/a.ssh.log"; then
  bad "A2 空号场景没有发生引擎 ssh（不把空号发到远端）"
else
  ok "A2 空号场景没有发生引擎 ssh（不把空号发到远端）"
fi
if grep -q "cmd<<<" "$ISO/logs/a.ssh.log"; then
  ok "A3 同步类 ssh 正常发生（流程真实走到了门前）"
else
  bad "A3 同步类 ssh 正常发生（流程真实走到了门前）"
fi
if [[ "$rc_a" != "0" ]]; then ok "A4 退出码非零（fail-closed：${rc_a}）"; else bad "A4 退出码非零（fail-closed）"; fi

# ── 场景 B：显式号 → 透传（不被 git 覆盖）────────────────────────────────
echo "-- 场景 B：export DD_BUILD_SHA=${EXPLICIT_SHA}（git HEAD=${GIT_SHA}）⇒ 声明号透传"
reset_remote
rc_b=$(run_via b "$T2" "DD_BUILD_SHA=$EXPLICIT_SHA")
if grep -q "DD_BUILD_SHA='$EXPLICIT_SHA'" "$ISO/logs/b.ssh.log"; then
  ok "B1 转发的引擎 ssh 命令携带声明号"
else
  bad "B1 转发的引擎 ssh 命令携带声明号"
fi
if grep -q "DD_BUILD_SHA='$GIT_SHA'" "$ISO/logs/b.ssh.log"; then
  bad "B2 git 号没有顶掉声明号（2026-10-28 修复保持）"
else
  ok "B2 git 号没有顶掉声明号（2026-10-28 修复保持）"
fi
if grep -q "为显式声明（不取 git、不追加 -dirty" "$ISO/logs/b.out"; then
  ok "B3 部署机侧日志注明「显式声明」来源"
else
  bad "B3 部署机侧日志注明「显式声明」来源"
fi
if grep -q "DD_BUILD_SHA=$EXPLICIT_SHA 为显式提供（目标树无 .git" "$ISO/logs/b.out"; then
  ok "B4 目标机引擎收到声明号并注明来源（透传到链路末端）"
else
  bad "B4 目标机引擎收到声明号并注明来源（透传到链路末端）"
fi
if [[ "$rc_b" != "0" ]]; then
  ok "B5 隔离目标机在 docker 前置处 fail-closed 终止（${rc_b}）—— 身份闸之后第一个必然失败点"
else
  bad "B5 隔离目标机在 docker 前置处 fail-closed 终止"
fi

# ── 场景 C：git 号 → 透传 ────────────────────────────────────────────────
echo "-- 场景 C：未声明号 ⇒ 从部署树 git 取号并原样转发"
reset_remote
rc_c=$(run_via c "$T2")
if grep -q "DD_BUILD_SHA='$GIT_SHA'" "$ISO/logs/c.ssh.log"; then
  ok "C1 转发的引擎 ssh 命令携带 git 号（${GIT_SHA}）"
else
  bad "C1 转发的引擎 ssh 命令携带 git 号"
fi
if grep -q "DD_BUILD_SHA='$EXPLICIT_SHA'" "$ISO/logs/c.ssh.log"; then
  bad "C2 未声明时不会转发声明号残留"
else
  ok "C2 未声明时不会转发声明号残留"
fi
if grep -q "DD_BUILD_SHA=$GIT_SHA 为显式提供（目标树无 .git" "$ISO/logs/c.out"; then
  ok "C3 目标机引擎收到 git 号（${GIT_SHA}）"
else
  bad "C3 目标机引擎收到 git 号"
fi
if grep -q "为显式声明" "$ISO/logs/c.out"; then
  bad "C4 未声明时部署机侧不报「显式声明」"
else
  ok "C4 未声明时部署机侧不报「显式声明」"
fi

# ── 场景 D：目标机侧兜底闸（同步树直跑 + 空号）───────────────────────────
echo "-- 场景 D：同步树（无 .git）上直接跑引擎且号为空 ⇒ 目标机侧 fail-closed"
reset_remote
mkdir -p "$ISO/remote-root/scripts"
cp "$ENGINE_SRC" "$ISO/remote-root/scripts/deploy-seamless-docker.sh"
(
  export PATH="$ISO/remote-bin:$PATH"
  export DD_TARGET=isol211 DD_HOST=isolated-target
  export DD_STATE_DIR="$ISO/state" DD_REMOTE_ROOT="$ISO/remote-root"
  export DD_GW_DATA_ROOT="$ISO/gwdata"
  cd "$ISO/remote-root"
  unset DD_BUILD_SHA
  bash scripts/deploy-seamless-docker.sh
) > "$ISO/logs/d.out" 2>&1
rc_d=$?
if grep -q "目标树没有 .git（同步树形态）且未显式给 DD_BUILD_SHA" "$ISO/logs/d.out"; then
  ok "D1 目标机侧兜底闸 die（最后一道防线的真实文案）"
else
  bad "D1 目标机侧兜底闸 die"
fi
if [[ "$rc_d" != "0" ]]; then ok "D2 退出码非零（${rc_d}）"; else bad "D2 退出码非零"; fi

# ── 变异臂 ───────────────────────────────────────────────────────────────
echo "-- 变异臂（只改隔离树里的引擎副本）"

# M1：部署机侧转发前门 die → log ⇒ 空号被发到目标机、错误落在远端（§1.2 缺陷复现）。
# 变异落在 T1（M1 的被测部署树；T1 无 .git 才构成「空号」场景）。
echo "-- M1：转发前门降级为 log"
mutate "$T1/scripts/deploy-seamless-docker.sh" \
  'die "部署机侧取不到构建提交号' 'log "部署机侧取不到构建提交号（M1 变异：门已降级为 log'
reset_remote
rc_m1=$(run_via m1 "$T1")
if grep -q "cmd<<<cd .*DD_BUILD_SHA='' .*bash scripts/deploy-seamless-docker.sh" "$ISO/logs/m1.ssh.log" \
   && grep -q "目标树没有 .git（同步树形态）" "$ISO/logs/m1.out"; then
  ok "M1 门降级后空号被发到目标机、错误落在远端（缺陷现场复现，本门抓到）"
else
  bad "M1 门降级后空号被发到目标机、错误落在远端（缺陷现场复现，本门抓到）"
fi
restore_engine "$T1"

# M2：取号段回退成无条件 git 覆盖（§1.1 缺陷）⇒ 声明号被 git 号吃掉。
echo "-- M2：取号段回退成无条件 git 覆盖"
mutate "$T2/scripts/deploy-seamless-docker.sh" \
  '  if [[ -z "${DD_BUILD_SHA:-}" ]]; then
    DD_BUILD_SHA="$(git -C "$DD_REPO_ROOT" rev-parse --short=8 HEAD 2>/dev/null || true)"' \
  '  if true; then
    DD_BUILD_SHA="$(git -C "$DD_REPO_ROOT" rev-parse --short=8 HEAD 2>/dev/null || true)"'
reset_remote
rc_m2=$(run_via m2 "$T2" "DD_BUILD_SHA=$EXPLICIT_SHA")
# 断言按前缀匹配：变异后的树带未提交修改 ⇒ git 号合法地带 -dirty 后缀，
# 判据钉的是「上链路的是 git 号而不是声明号」这个语义。
if grep -q "DD_BUILD_SHA='$GIT_SHA" "$ISO/logs/m2.ssh.log" \
   && ! grep -q "DD_BUILD_SHA='$EXPLICIT_SHA'" "$ISO/logs/m2.ssh.log"; then
  ok "M2 无条件覆盖回归被本门抓到（声明号被 git 号吃掉 = §1.1 缺陷复现）"
else
  bad "M2 无条件覆盖回归被本门抓到（声明号被 git 号吃掉 = §1.1 缺陷复现）"
fi
restore_engine "$T2"

# M3（阴性回归臂）：还原后 B 场景恢复绿。
reset_remote
rc_r=$(run_via r "$T2" "DD_BUILD_SHA=$EXPLICIT_SHA")
if grep -q "DD_BUILD_SHA='$EXPLICIT_SHA'" "$ISO/logs/r.ssh.log"; then
  ok "M3 还原后 B 场景恢复绿（透传恢复）"
else
  bad "M3 还原后 B 场景恢复绿（透传恢复）"
fi

# M4（还原臂的完整性）：M1 的变异落在 T1 ⇒ 必须验证 **T1 也被还原**
# （否则 M1 之后所有 T1 场景都带着变异在跑，A 场景的绿是假的）。
reset_remote
rc_a2=$(run_via a2 "$T1")
if grep -q "部署机侧取不到构建提交号" "$ISO/logs/a2.out" \
   && ! grep -q "cmd<<<cd .*bash scripts/deploy-seamless-docker.sh" "$ISO/logs/a2.ssh.log"; then
  ok "M4 T1 还原后 A 场景恢复绿（拦截恢复）"
else
  bad "M4 T1 还原后 A 场景恢复绿（拦截恢复）"
fi

# M5（还原臂的完整性）：T2 也必须被还原 ⇒ C 场景恢复绿。
reset_remote
rc_c2=$(run_via c2 "$T2")
if grep -q "DD_BUILD_SHA='$GIT_SHA" "$ISO/logs/c2.ssh.log"; then
  ok "M5 T2 还原后 C 场景恢复绿（git 号恢复）"
else
  bad "M5 T2 还原后 C 场景恢复绿（git 号恢复）"
fi

# M6（阴性对照）：仓内引擎文件在整个 harness 运行期间一个字节都没被改。
# ★ 必须比对**启动时记录的 md5**与**结束时的 md5**——拿同一个路径跟自己比
#   是恒真的空洞断言（§531 通用式三：断言恒真比恒假更隐蔽）。
if [[ "$(md5 -q "$ENGINE_SRC" 2>/dev/null || echo missing)" == "$ENGINE_MD5_AT_START" ]]; then
  ok "M6 仓内引擎文件未被本 harness 改动（隔离成立，md5 ${ENGINE_MD5_AT_START}）"
else
  bad "M6 仓内引擎文件未被本 harness 改动（隔离不成立：启动 ${ENGINE_MD5_AT_START} → 现在 $(md5 -q "$ENGINE_SRC" 2>/dev/null || echo missing)）"
fi

# ── 汇总 ─────────────────────────────────────────────────────────────────
echo "=========================================="
echo "通过 $PASS / 失败 $FAIL"
if [[ "$FAIL" -gt 0 ]]; then
  printf '失败项: %s\n' "${FAILED_NAMES[*]:-}"
  echo "（隔离目录已清理；各场景完整输出见上方运行日志 a/b/c/d/m1/m2/r）"
  exit 1
fi
echo "隔离环境实测：三道身份闸 A/B/C/D 全部按预期；变异臂 M1/M2 被本门抓到，还原后恢复绿"
exit 0
