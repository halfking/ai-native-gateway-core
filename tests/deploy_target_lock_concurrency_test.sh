#!/usr/bin/env bash
# =====================================================================
# tests/deploy_target_lock_concurrency_test.sh
#   —— 守「目标机侧部署锁」（2026-11-05 蓝绿并发事故轮）
# =====================================================================
# ★ 为什么要有这个门（**真实事故**，不是假想）：
#   2026-11-05 00:47~00:53，211 上**同时**跑着两次部署：
#     · 00:49:18  另一次部署构建镜像 llm-gateway-211:latest
#     · 00:49:49  它起了槽 b 并把 nginx upstream 切到 8783
#     · 00:53:00  本次部署起了槽 a
#     · 00:53:03  本次部署把 upstream 切回 8781
#     · 00:53:14  本次部署**停掉**了另一个部署刚接流量的槽 b
#   ⇒ 两次部署**互相切流、互相停对方的槽** ⇒ 槽位撞车时
#     「起候选槽」那步的 `docker rm -f` 会**直接删掉正在服务的那一个** ⇒ 502。
#   ★ 根因：**部署锁只在部署机侧拿** ⇒ 挡不住「两台不同部署机并发同一个目标」。
#   ⇒ 修法：目标机侧也有一把锁（与 154/245 同族 SSOT：lock_acquire_remote）。
#
# ★ 本门回答的问题（**每层一个不同的问题**）：
#   ① 接线层：--via 路径**真的**去目标机拿锁了吗？（远端建锁命令发生过吗）
#   ② 结论层：锁被占用时，部署**拒绝**了吗？（fail-closed，不是照跑）
#   ③ 顺序层：拒绝发生在**部署 ssh 之前**吗？（不能「先部署再拒绝」）
#   ④ 不自踩层：父进程把 `DD_TARGET_LOCK_HELD=1` 随行给子进程了吗？
#   ⑤ 变异臂：把锁获取去掉 ⇒ ①②③ 变红 ⇒ **证明门有牙**（不是恒绿）
#
# 用法: bash tests/deploy_target_lock_concurrency_test.sh
# 退出码: 0 全绿 / 1 有失败
# =====================================================================
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENGINE_SRC="$REPO_ROOT/scripts/deploy-seamless-docker.sh"

PASS=0; FAIL=0; FAILED_NAMES=()
ok()  { PASS=$((PASS + 1)); printf '  ✓ %s\n' "$1"; }
bad() { FAIL=$((FAIL + 1)); FAILED_NAMES+=("$1"); printf '  ✗ %s\n' "$1"; }

ISO="$(mktemp -d "${TMPDIR:-/tmp}/dd-lock.XXXXXX")"
# KEEP_ISO=1 ⇒ 保留隔离目录并打印路径（调试用；失败时看现场用）
if [[ "${KEEP_ISO:-0}" == "1" ]]; then
  echo "（KEEP_ISO=1）隔离目录：$ISO"
else
  cleanup() { rm -rf "$ISO"; }
  trap cleanup EXIT
fi
mkdir -p "$ISO/bin" "$ISO/remote-bin" "$ISO/logs" "$ISO/tmp" "$ISO/locks"

# ── PATH 垫片：ssh 记录并**在本机执行**远端命令 ──────────────────────────
#   ⇒ 「目标机」被模拟成本机的一个子目录 ⇒ 目标机锁的建/抢/放都能真跑。
cat > "$ISO/bin/ssh" <<'SHIM'
#!/usr/bin/env bash
set -u
LOG="${ISO_SSH_LOG:?}"
host="$1"; shift
printf '[ssh] host=%s cmd<<<%s>>>\n' "$host" "$*" >> "$LOG"
exec env -i PATH="${ISO_REMOTE_BIN:?}:/usr/bin:/bin:/usr/sbin:/sbin" HOME="${HOME:-/var/root}" bash -c "$*"
SHIM
cat > "$ISO/bin/scp" <<'SHIM'
#!/usr/bin/env bash
echo "scp: isolated harness — not expected in deploy-mode --via run" >&2
exit 64
SHIM
chmod +x "$ISO/bin/ssh" "$ISO/bin/scp"
printf '#!/bin/sh\necho Linux\n' > "$ISO/remote-bin/uname"
cat > "$ISO/remote-bin/docker" <<'SHIM'
#!/usr/bin/env bash
echo "docker: isolated harness denies daemon access" >&2
exit 1
SHIM
chmod +x "$ISO/remote-bin/uname" "$ISO/remote-bin/docker"

# ── 部署树（有 .git，能取到提交号）─────────────────────────────────────
TREE="$ISO/tree"
mkdir -p "$TREE/scripts" "$TREE/web/dist" "$TREE/web-mobile/dist"
cp "$ENGINE_SRC" "$TREE/scripts/deploy-seamless-docker.sh"
cp "$REPO_ROOT/scripts/deploy-lib-resolve.sh" "$TREE/scripts/deploy-lib-resolve.sh"
# ★ 2026-11-09（F51）：★ deploy-lib-resolve.sh 的候选链搬到了共享的唯一真相文件
#   scripts/deploy-lib-candidates.sh ⇒ ★ 夹具必须**连它一起**复制 ⇒ ★ 否则
#   ★ 解析器在 source 那一行就断掉（★ 而断点与本门要测的并发互斥无关 ★）
cp "$REPO_ROOT/scripts/deploy-lib-candidates.sh" "$TREE/scripts/deploy-lib-candidates.sh"
printf '<html>iso</html>\n' > "$TREE/web/dist/index.html"
printf '<html>iso</html>\n' > "$TREE/web-mobile/dist/index.html"
git -C "$TREE" init -q -b main
git -C "$TREE" -c user.email=t@t -c user.name=t add -A
git -C "$TREE" -c user.email=t@t -c user.name=t commit -qm "iso tree"
GIT_SHA="$(git -C "$TREE" rev-parse --short=8 HEAD)"

LOCK_PATH="$ISO/locks/deploy.lock"

run_engine() { # run_engine <标签> [--hold-lock]
  local tag="$1" hold="${2:-}"
  : > "$ISO/logs/$tag.ssh.log"
  rm -rf "$ISO/locks"; mkdir -p "$ISO/locks"
  if [[ "$hold" == "--hold-lock" ]]; then
    # 模拟「另一个部署正持有目标机锁」：目录已存在 ⇒ 远端 mkdir 失败 ⇒ rc=75
    mkdir -p "$LOCK_PATH"
    printf 'target=211\npid=999999\nsource_host=other\n' > "$LOCK_PATH/metadata"
  fi
  (
    export PATH="$ISO/bin:$PATH"
    export TMPDIR="$ISO/tmp" LOCK_TMP_ROOT="$ISO/tmp"
    export LLM_GATEWAY_ROOT="$TREE"
    export DD_TARGET=211 DD_HOST=isolated-target
    export DD_STATE_DIR="$ISO/state" DD_REMOTE_ROOT="$ISO/remote-root"
    export DD_GW_DATA_ROOT="$ISO/gwdata"
    export DD_SKIP_WEB_BUILD=1
    export DD_REMOTE_LOCK_PATH="$LOCK_PATH"     # ★ 目标机锁落到隔离目录（不碰 /var/lib）
    export ISO_SSH_LOG="$ISO/logs/$tag.ssh.log" ISO_REMOTE_BIN="$ISO/remote-bin"
    bash "$TREE/scripts/deploy-seamless-docker.sh" --via fake-target
  ) > "$ISO/logs/$tag.out" 2>&1
  echo $?
}

echo "== 目标机侧部署锁（并发互斥）=="

# ── 场景 1：锁空闲 ⇒ 正常路径 ───────────────────────────────────────────
rc1=$(run_engine free)
if grep -q "cmd<<<LOCK_PATH=" "$ISO/logs/free.ssh.log"; then
  ok "① 接线层：--via 路径真的去目标机建锁了（远端建锁命令已发生）"
else
  bad "① 接线层：--via 路径**没有**去目标机建锁（并发互斥不存在）"
fi
if grep -q "DD_TARGET_LOCK_HELD=1" "$ISO/logs/free.ssh.log"; then
  ok "④ 不自踩层：父进程把 DD_TARGET_LOCK_HELD=1 随行给子进程"
else
  bad "④ 不自踩层：父进程**没有**随行 DD_TARGET_LOCK_HELD=1（子进程会与父进程互踩）"
fi

# ── 场景 2：锁被占用 ⇒ 必须 fail-closed ────────────────────────────────
rc2=$(run_engine held --hold-lock)
if [[ "$rc2" != "0" ]]; then
  ok "② 结论层：锁被占用时退出码非 0（fail-closed，rc=${rc2}）"
else
  bad "② 结论层：锁被占用时**照样部署成功**（rc=0）—— 并发保护形同虚设"
fi
if grep -q "拒绝并发部署" "$ISO/logs/held.out"; then
  ok "② 结论层：拒绝文案落地（运维看得懂为什么被挡）"
else
  bad "② 结论层：没有「拒绝并发部署」的明确文案"
fi
if grep -q "cmd<<<cd .*bash scripts/deploy-seamless-docker.sh" "$ISO/logs/held.ssh.log"; then
  bad "③ 顺序层：锁被占用时**仍然**发生了部署 ssh（先部署再拒绝 = 已经动过生产）"
else
  ok "③ 顺序层：拒绝发生在部署 ssh 之前（生产未被触碰）"
fi

# ── 变异臂：把**整套**目标机侧锁机制摘掉 ⇒ ①②③ 必须变红（证明门有牙）──
#   ★ 为什么摘**两处**（父进程的获取 + 子进程的自持补偿）：
#     只摘父进程那一处时，**子进程仍会自己拿锁**（防御纵深）⇒ 拒绝照样发生
#     ⇒ 门测不出「整套机制不存在」这个**历史缺陷形态**。
#     ⇒ 变异要还原的是**事故当晚的形态**：父不拿、子也不拿 ⇒ 两次部署互相切流。
MUT="$ISO/tree-mut"
cp -R "$TREE" "$MUT"
python3 - "$MUT/scripts/deploy-seamless-docker.sh" <<'PY'
import pathlib, sys
p = pathlib.Path(sys.argv[1]); s = p.read_text(encoding="utf-8")
a_old = 'if ! lock_acquire_remote _dd_target_ssh "$DD_TARGET" "$DD_TARGET_LOCK_PATH"; then'
a_new = 'if false; then  # MUTATION: 目标机侧锁被摘掉（父进程侧）'
b_old = 'if [[ "$MODE" == "deploy" && "${DD_TARGET_LOCK_HELD:-0}" != "1" && "${DD_TARGET_LOCK_SKIP:-0}" != "1" ]]; then'
b_new = 'if false; then  # MUTATION: 目标机侧锁被摘掉（目标机直跑侧）'
assert s.count(a_old) == 1, "anchor A count=%d" % s.count(a_old)
assert s.count(b_old) == 1, "anchor B count=%d" % s.count(b_old)
s = s.replace(a_old, a_new).replace(b_old, b_new)
p.write_text(s, encoding="utf-8")
print("mutated")
PY
run_engine_mut() {
  local tag="$1" hold="${2:-}"
  : > "$ISO/logs/$tag.ssh.log"
  rm -rf "$ISO/locks"; mkdir -p "$ISO/locks"
  if [[ "$hold" == "--hold-lock" ]]; then
    mkdir -p "$LOCK_PATH"
    printf 'target=211\npid=999999\nsource_host=other\n' > "$LOCK_PATH/metadata"
  fi
  (
    export PATH="$ISO/bin:$PATH"
    export TMPDIR="$ISO/tmp" LOCK_TMP_ROOT="$ISO/tmp"
    export LLM_GATEWAY_ROOT="$MUT"
    export DD_TARGET=211 DD_HOST=isolated-target
    export DD_STATE_DIR="$ISO/state" DD_REMOTE_ROOT="$ISO/remote-root"
    export DD_GW_DATA_ROOT="$ISO/gwdata"
    export DD_SKIP_WEB_BUILD=1
    export DD_REMOTE_LOCK_PATH="$LOCK_PATH"
    export ISO_SSH_LOG="$ISO/logs/$tag.ssh.log" ISO_REMOTE_BIN="$ISO/remote-bin"
    bash "$MUT/scripts/deploy-seamless-docker.sh" --via fake-target
  ) > "$ISO/logs/$tag.out" 2>&1
  echo $?
}
rcm=$(run_engine_mut m1 --hold-lock)
if ! grep -q "cmd<<<LOCK_PATH=" "$ISO/logs/m1.ssh.log"; then
  ok "⑤ 变异臂：整套锁机制被摘掉后，建锁命令不再发生 ⇒ ① 抓得住（门有牙）"
else
  bad "⑤ 变异臂：整套锁机制被摘掉后，建锁命令仍在发生 ⇒ 门抓不住这个缺陷"
fi
if ! grep -q "拒绝并发部署" "$ISO/logs/m1.out"; then
  ok "⑤ 变异臂：整套锁机制被摘掉后，不再被锁挡住 ⇒ ② 抓得住（fail-closed 那一层有牙）"
else
  bad "⑤ 变异臂：整套锁机制被摘掉后仍被挡住 ⇒ 门测不到「没有锁会怎样」"
fi
if grep -q "cmd<<<cd .*bash scripts/deploy-seamless-docker.sh" "$ISO/logs/m1.ssh.log"; then
  ok "⑤ 变异臂：整套锁机制被摘掉后，部署照样发生 ⇒ ③ 抓得住（顺序层有牙）"
else
  bad "⑤ 变异臂：整套锁机制被摘掉后部署没发生 ⇒ 门测不出「会动到生产」"
fi

# ── 仓内文件未被本 harness 改动（隔离成立）──────────────────────────────
if [[ "$(md5 -q "$ENGINE_SRC")" == "$(md5 -q "$TREE/scripts/deploy-seamless-docker.sh")" ]]; then
  ok "⑥ 隔离成立：仓内引擎文件未被本 harness 改动"
else
  bad "⑥ 隔离成立：仓内引擎文件被本 harness 改动了"
fi

echo "=========================================="
printf '通过 %d / 失败 %d\n' "$PASS" "$FAIL"
if [[ "$FAIL" -gt 0 ]]; then
  printf '失败项: %s\n' "${FAILED_NAMES[*]}"
  exit 1
fi
exit 0
