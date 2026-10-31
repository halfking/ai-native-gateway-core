#!/usr/bin/env bash
# Parity gate: the repo's switch-lock implementation vs the shared deploy
# library (SSOT).
#
# 2026-10-28 · r1039 后续轮 · 第二轮批判性审计（初版）
# 2026-10-28 · r1039 后续轮 · 第三轮（合并就绪轮）：★ 门从「比两份实现」
#   改成「**比一份实现 + 盯住剩下的漂移**」。
#
# ## 为什么要有这道门（★ 第二轮最值钱的一处发现的来历）
#
# ★ 共享部署库 lock.sh 的 `lock_pid_alive` 早在 **2026-09-18** 就把口径从
#   `kill -0` 换成了 `ps -p`，它的注释原文写着：
#
#     "The previous checks used `kill -0 "$pid"`, which conflates ESRCH
#      (process gone) with EPERM (process ALIVE but owned by another user)
#      — both exit nonzero. A deploy launched by a different user (root cron
#      vs operator shell) therefore looked DEAD to force recovery, which then
#      deleted a live holder's lock and let two deploys interleave.
#      `ps -p` reports existence regardless of ownership on macOS and Linux,
#      so EPERM can no longer masquerade as death."
#
# ⇒ ★★ **本仓 2026-10-27/28 新写的 `dl_pid_alive` 把那个已经修掉的 bug 又抄了回来**
#   ⇒ 这不是「巧合写错」，而是**同一套机制存在两份实现、其中一份停在了旧版本**
#     ⇒ **两侧语义会漂移**，而仓内没有任何一道门比对过两侧。
#
# ## 第三轮：判据的两处改造
#
# ★ **改造一（委托）**：本仓的存活判据已**委托**共享部署库的 `lock_pid_alive`
#   ⇒ 「repo vs SSOT」这条比较从此**变成平凡真**（同一个函数）
#   ⇒ ⇒ 牙齿必须从别处来 ⇒ **P3 结构性判据**：
#     ★ **本仓的生产脚本里不得再自带第二份存活探针**（`ps -p` / `kill -0` 命中数 = 0）
#     ⇒ 这条判据能红（有人把探针抄回来 ⇒ 红），且**不依赖**共享库在不在。
#   ⇒ ⚠ **「委托」不等于「判据消失」**：委托后「共享库没被 source」变成
#     **可用性前提** ⇒ 那条由**契约门**的 I19 钉住（fail-fast，不许猜）。
#
# ★ **改造二（漂移账）**：剩余的跨仓差异从「注释里的一句声明」变成
#   **可执行的账**（P4）⇒ 每条判据都**实测**（能测的测，测不了的明说测不了）
#   ⇒ 待合并集合走**只降不升**的棘轮（见 tests/deploy_lock_parity_baseline.json）。
#
# ## 刻意**不**对拍的部分（★ 政策差异，不是语义漂移）
#
#   ★ 本仓的 acquire 会对残锁**自动接管**（否则机器被永久卡死）；
#     共享部署库的 acquire 一律快速失败（由运维显式 force-unlock）。
#   ★ 共享部署库 unlock-local.sh 的 --force 档会**杀掉**活着的持有者；
#     本仓的 --force 档只清锁、不杀进程（杀掉别的用户的部署是更重的动作，
#     属需要 owner 裁决的语义变更 ⇒ 只登记）。
#   ⇒ 这两条是**有意**不同，判据只钉「持有者是否还活着」这一个共同前提。
#
# ## 缺 SSOT 时怎么办
#
#   共享部署库在仓外（~/workspace/ai-native-tools/deploy-lib）。CI runner 上
#   可能没有 ⇒ 此时**明确留痕 SKIP**（并说明「本次没比对」），**不**输出
#   ALL PASS ⇒ 避免「假绿」（★ 仓内纪律：门绿了不代表事实成立）。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASELINE="$ROOT/tests/deploy_lock_parity_baseline.json"
UPDATE_BASELINE=0
[[ "${1:-}" == "--update-baseline" ]] && UPDATE_BASELINE=1

TMP=$(mktemp -d -t kx-lock-parity.XXXXXX)
trap 'rm -rf "$TMP"' EXIT

fail() { printf 'FAIL %s\n' "$*" >&2; exit 1; }
pass() { printf 'PASS %s\n' "$*"; }

# ── 基线形状契约（★ 与「共享库在不在」无关）──────────────────────────────
#
# ★★ 为什么要有这一条（★ r1039 第三轮实测出来的）：
#   ★ 这道门必须能在「漂移账被掏空 / 丢失」时**红** ⇒ ⇒ 红的理由必须
#     **与「共享部署库在不在」无关** —— ★ 缺 SSOT 时本门整段是 SKIP（★ exit 0）
#     ⇒ ⇒ 若红因依赖 SSOT，★ 掏空态就会「仍绿」⇒ ⇒ **假的有牙**。
#   ★ 而本门是用 sed/grep 从漂移账里取两个清单 ⇒ ⇒ **键缺失时取到的是空串**
#     ⇒ ⇒ 棘轮会拿一个空基线去比 ⇒ ⇒ 结论是假的（★ 「空基线」与
#     「没有待合并项」在文本上**长得一样**）⇒ ⇒ **键的存在性本身就是契约**。
#   ★ ⇒ 这条判据跑在 P3 之前（★ P3 也要读漂移账的豁免表）⇒ ⇒ 掏空态下
#     **第一个**失败一定是本条 ⇒ ⇒ 红因（red_signal）在任何环境下都一致。
if [[ ! -f "$BASELINE" ]]; then
  printf 'FAIL baseline missing: %s (the cross-repo drift ledger is part of the repo)\n' "$BASELINE" >&2
  exit 1
fi
for key in '"note"' '"pendingMergeCriteria"' '"probeExemptions"'; do
  if ! grep -qF "$key" "$BASELINE"; then
    printf 'FAIL baseline shape broken: %s is missing from %s\n' "$key" "$BASELINE" >&2
    printf '  the drift ledger is a registry this gate reads: its keys are part of the contract\n' >&2
    printf '  (★ a missing key would silently read as an empty list ⇒ the ratchet would compare against nothing)\n' >&2
    exit 1
  fi
done

# ── 解析共享部署库 SSOT（与 deploy-local.sh 的候选顺序一致）──────────────
ssot=''
for cand in "${AIAN_DEPLOY_LIB:-}" "$ROOT/scripts/deploy-lib" "$HOME/workspace/ai-native-workspace/deploy-lib" "$HOME/workspace/ai-native-tools/deploy-lib"; do
  if [[ -n "$cand" && -f "$cand/lock.sh" ]]; then
    ssot="$(cd "$cand" && pwd -P)"
    break
  fi
done

# ── P3：**结构性**判据 —— 本仓生产脚本里不得再自带存活探针 ────────────────
#
# ★ 这条判据**不依赖**共享库 ⇒ 缺 SSOT 时也要跑（它是本仓自己的纪律）。
# ★ 只数**代码**里的命中（注释里解释历史的那段不算 ⇒ 先剥掉 `#` 之后的部分）。
# ★ 豁免**必须显式登记**（见 tests/deploy_lock_parity_baseline.json 的
#   probeExemptions）⇒ ★ **新命中一律红** ⇒ 豁免表本身就是审阅面。
#   ⇒ 豁免登记的是「站点 = 文件:函数」⇒ 命中点落在哪个函数里也要能对上。
probe_hits="$(ROOT="$ROOT" awk '{
  if ($0 ~ /^[A-Za-z_][A-Za-z0-9_]*\(\)/) { fn=$1; sub(/\(.*/, "", fn) }
  line=$0; sub(/#.*/, "", line);
  if (line ~ /ps[[:space:]]+-p|kill[[:space:]]+-0/) {
    f=FILENAME; sub(ENVIRON["ROOT"]"/", "", f);
    printf "%s:%s:%d\n", f, fn, FNR
  }
}' "$ROOT/scripts/deploy-local.sh" "$ROOT/scripts/deploy-local-lib.sh")"
if [[ -n "$probe_hits" ]]; then
  exemptions=''
  if [[ -f "$ROOT/tests/deploy_lock_parity_baseline.json" ]]; then
    exemptions="$(sed -n '/"probeExemptions"/,/]/p' "$ROOT/tests/deploy_lock_parity_baseline.json" \
      | grep -o '"site"[[:space:]]*:[[:space:]]*"[^"]*"' \
      | sed 's/.*"\(.*\)"$/\1/' || true)"
  fi
  unmatched=''
  while IFS= read -r hit; do
    [[ -n "$hit" ]] || continue
    site="${hit%:*}"
    if ! grep -qxF "$site" <<<"$exemptions"; then
      unmatched+="$hit"$'\n'
    fi
  done <<<"$probe_hits"
  if [[ -n "$unmatched" ]]; then
    printf 'FAIL the repo must not carry its own liveness probe (the shared SSOT owns it now):\n%s' "$unmatched" >&2
    printf '  an exemption must be registered in tests/deploy_lock_parity_baseline.json (site + reason) before this passes\n' >&2
    exit 1
  fi
  printf 'NOTE liveness-probe sites are present but every one of them is a registered exemption:\n%s\n' "$probe_hits"
fi
pass 'structural: the repo carries no second liveness probe (the SSOT is the single owner)'

if [[ -z "$ssot" ]]; then
  printf 'SKIP: shared deploy lib (lock.sh) not found — the cross-repo parity was NOT compared this run\n'
  printf '  looked at: AIAN_DEPLOY_LIB, %s/scripts/deploy-lib, ~/workspace/ai-native-workspace/deploy-lib, ~/workspace/ai-native-tools/deploy-lib\n' "$ROOT"
  printf 'SKIP deploy lock parity (no SSOT)\n'
  exit 0
fi
# ★★ 缺件就明说没跑：★ C3 的实测需要**真语义的假 flock**（python3 的 fcntl）
#   ⇒ 没有 python3 时，★ 若照样去测 ⇒ 量到的是「替身不存在」而不是
#   「共享库撞上致命 exec 路径」⇒ ⇒ **那会把 C3 判成别的结论** ⇒
#   ⇒ ⇒ 显式 SKIP 整段跨仓比对（★ 不输出 ALL PASS）。
if ! command -v python3 >/dev/null 2>&1; then
  printf 'SKIP: python3 is not available (the true-semantics fake flock needs fcntl) — the cross-repo comparison was NOT run this time\n'
  printf 'SKIP deploy lock parity (no python3)\n'
  exit 0
fi
printf 'SSOT: %s\n' "$ssot"

# ★ LOCK_TMP_ROOT 必须**在** source 之前设好 ⇒ SSOT 的 `: "${LOCK_TMP_ROOT:=/tmp}"`
#   不会把锁路径指到真实的 /tmp。
export LOCK_TMP_ROOT="$TMP/locks"
mkdir -p "$LOCK_TMP_ROOT"

# shellcheck source=/dev/null
source "$ssot/lock.sh"
# shellcheck source=/dev/null
source "$ROOT/scripts/deploy-local-lib.sh"

# oracle：OS 的事实（跨用户安全 —— 与 SSOT 的 liveness 口径同源，但这里
# 独立写一遍，避免「拿被测对象当标准」）。
oracle_alive() {
  local pid="$1"
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  ps -p "$pid" -o pid= >/dev/null 2>&1
}

verdict_of() {
  local fn="$1" pid="$2"
  if "$fn" "$pid" 2>/dev/null; then printf ALIVE; else printf DEAD; fi
}

# ── P1：逐 pid 对拍 ───────────────────────────────────────────────────────
#
# ★ pid 矩阵覆盖四种「形态」：
#   · 同用户的活进程（$$）
#   · **别的用户**的活进程（pid 1 = launchd / systemd，root 的）
#     ⇒ 这一格才是 EPERM 那一支的真正考题
#   · 死 pid（999999）
#   · 畸形 pid（0 / abc / 空）—— ★ pid 0 不是进程（`kill -0 0` 探的是进程组、
#     恒成功）⇒ 两侧都必须判「死」
failed=0
for pid in "$$" 1 999999 0 abc ""; do
  if oracle_alive "$pid"; then want=ALIVE; else want=DEAD; fi
  got_repo="$(verdict_of dl_pid_alive "$pid")"
  got_ssot="$(verdict_of lock_pid_alive "$pid")"
  label="pid=${pid:-<empty>}"
  if [[ "$got_repo" != "$want" ]]; then
    printf 'FAIL %s: repo oracle says %s, OS says %s\n' "$label" "$got_repo" "$want" >&2
    failed=1
    continue
  fi
  if [[ "$got_ssot" != "$want" ]]; then
    printf 'FAIL %s: shared SSOT oracle says %s, OS says %s\n' "$label" "$got_ssot" "$want" >&2
    failed=1
    continue
  fi
  if [[ "$got_repo" != "$got_ssot" ]]; then
    printf 'FAIL %s: repo says %s but shared SSOT says %s (semantic drift)\n' "$label" "$got_repo" "$got_ssot" >&2
    failed=1
    continue
  fi
  printf 'PASS %s: repo=%s ssot=%s os=%s\n' "$label" "$got_repo" "$got_ssot" "$want"
done
[[ "$failed" -eq 0 ]] || fail 'liveness parity between the repo and the shared SSOT is broken'

# ── P2：元数据字段集对拍（本仓注释里的「同字段集」是一条声名）─────────────
repo_meta="$TMP/repo-metadata"
ssot_meta="$TMP/ssot-metadata"
dl_switch_lock_write_metadata > "$repo_meta"
lock_write_metadata 154 > "$ssot_meta"
repo_fields="$(awk -F= '/^[a-z_]+=/ {print $1}' "$repo_meta" | sort | tr '\n' ',')"
ssot_fields="$(awk -F= '/^[a-z_]+=/ {print $1}' "$ssot_meta" | sort | tr '\n' ',')"
if [[ "$repo_fields" != "$ssot_fields" ]]; then
  printf 'FAIL metadata field set drifted\n  repo: %s\n  ssot: %s\n' "$repo_fields" "$ssot_fields" >&2
  exit 1
fi
printf 'PASS metadata field set matches the shared SSOT: %s\n' "$repo_fields"

# ═════════════════════════════════════════════════════════════════════════
# P4：跨仓漂移账（★ 只降不升的棘轮）
# ═════════════════════════════════════════════════════════════════════════
#
# ★ 三条判据各自**实测**（★ 能测的就测；测不了的**明说**测不了 —— 门不许
#   用「我以为」代替读数）：
#
#   C3 **锁形态**：本仓按「路径是什么形态」定；共享库按「本机有没有 flock」定。
#      ⇒ 实测：给共享库一个【目录形态】的锁 + 可用的 flock ⇒ 看它是
#        「干净地 rc=75」还是「打不开目录」。
#      ⇒ ★★ 本机（bash 3.2）上共享库的 flock 分支**根本跑不起来**：
#        `exec {VAR}<>` 不受支持 ⇒ fd 从未分配 ⇒ flock 收到空 fd
#        ⇒ ⇒ 这条判据**实测得到**「共享库会崩」而不是「共享库会慢」。
#   C4 **释放语义**：共享库的 flock 释放会 `rm -f` 锁文件 ⇒ unlink-after-unlock
#      ⇒ 两个持有者的竞态。本仓已修（只关 fd，不删文件）。
#      ⇒ ★ 同上：那半边实现在本机**跑不起来** ⇒ 这条只能**源码级**判定
#        （输出里必须标注「源码级」，不得冒充实测）。
#   C5 **pid 形态校验**：共享库 `^[0-9]+$`（接受 `0` 与前导零）；
#      本仓 `^[1-9][0-9]*$`（`0` 不是进程；前导零的进制语义依赖实现）。
#      ⇒ 行为上在本机**不可区分**（`ps -p 0` 与 `ps -p 0123` 都查无此进程）
#        ⇒ 同样只能**源码级**判定。
#
# ★ 棘轮：待合并集合**只准变小** ⇒ 任何一条「本来一致」变成「不一致」⇒ 红；
#   「本来待合并」变成「一致」⇒ 提示更新基线（不红）。
pending_now=()

# ── C3：锁形态 ────────────────────────────────────────────────────────────
# shellcheck source=lib/fake-flock.sh
source "$ROOT/tests/lib/fake-flock.sh"
fake_flock="$(fake_flock_make "$TMP/fake-flock")"
c3_out="$(LOCK_FLOCK_BIN="$fake_flock" LOCK_LOCAL_TARGET=154 bash -c '
  source "$1/lock.sh"
  export LOCK_TMP_ROOT="$2"
  mkdir -p "$LOCK_TMP_ROOT/kx-llm-gateway-deploy-154.lock"
  lock_acquire_local
' _ "$ssot" "$TMP/c3" 2>&1)" || c3_rc=$?
c3_rc=${c3_rc:-0}
if [[ "$c3_out" == *"Is a directory"* || "$c3_out" == *"not found"* || "$c3_rc" -ne 75 ]]; then
  c3_state=differs
  pending_now+=("C3-lock-shape")
  printf 'DIFF C3-lock-shape: the shared SSOT hits the fatal exec path on a directory-shaped lock (rc=%s): %s\n' "$c3_rc" "$(printf '%s' "$c3_out" | head -2 | tr '\n' ' ')"
else
  c3_state=agrees
  printf 'AGREE C3-lock-shape: the shared SSOT fails fast with 75 on a directory-shaped lock\n'
fi

# ── C4：释放语义（源码级 —— 那半边实现在本机跑不起来）────────────────────
ssot_release_body="$(sed -n '/^lock_release_local()/,/^}/p' "$ssot/lock.sh")"
if grep -q 'rm -f "\$LOCK_LOCAL_DIR"' <<<"$ssot_release_body"; then
  c4_state=differs
  pending_now+=("C4-release-unlink")
  printf 'DIFF C4-release-unlink: the shared SSOT unlinks the lock file after closing the fd (source-level; that branch cannot run on bash 3.2)\n'
else
  c4_state=agrees
  printf 'AGREE C4-release-unlink: the shared SSOT does not unlink the lock file on release\n'
fi

# ── C5：pid 形态校验（源码级）──────────────────────────────────────────────
#
# ★★ 判据实现上的一个坑（★ 本轮当场踩到）：第一版用 BRE 写 `^\[0-9\]\+\$`，
#   而 **macOS 的 grep 是 BSD 版**（`\+` 是 GNU 扩展）⇒ **静默不匹配**
#   ⇒ 判据「一致」地判绿，而事实是「不一致」⇒ ★★ **正则写不对 = 判据恒绿**
#   ⇒ ⇒ 现在改用**定长字符串**比对（grep -F）⇒ 没有转义歧义。
# ★ 两侧**对称**地钉：本仓必须仍然是严格的（否则是**本仓**回归 ⇒ 红），
#   共享库是否严格 ⇒ 决定这条判据是「一致」还是「待合并」。
repo_pid_body="$(sed -n '/^dl_pid_valid()/,/^}/p' "$ROOT/scripts/deploy-local-lib.sh")"
ssot_pid_body="$(sed -n '/^lock_pid_alive()/,/^}/p' "$ssot/lock.sh")"
repo_strict=0
ssot_strict=0
grep -qF '^[1-9][0-9]*$' <<<"$repo_pid_body" && repo_strict=1
grep -qF '^[1-9][0-9]*$' <<<"$ssot_pid_body" && ssot_strict=1
if [[ "$repo_strict" -ne 1 ]]; then
  printf 'FAIL the repo must keep rejecting pid 0 / leading zeros (^[1-9][0-9]*$) — that is a repo-side regression\n' >&2
  exit 1
fi
if [[ "$ssot_strict" -eq 1 ]]; then
  c5_state=agrees
  printf 'AGREE C5-pid-regex: the shared SSOT rejects pid 0 / leading zeros like the repo does\n'
else
  c5_state=differs
  pending_now+=("C5-pid-regex")
  printf 'DIFF C5-pid-regex: the shared SSOT accepts pid 0 / leading zeros (source-level; behaviourally indistinguishable on this machine)\n'
fi

# ── 棘轮判定：待合并集合只准变小 ──────────────────────────────────────────
base_pending=''
# ★ 基线的**存在性与形状**已在【基线形状契约】那一段验过（★ 那一段跑在 P3 之前
#   ⇒ 掏空态下第一个失败一定是它 ⇒ 红因稳定）⇒ ⇒ 这里直接取清单。
base_pending="$(sed -n '/"pendingMergeCriteria"/,/]/p' "$BASELINE" | grep -o '"[^"]*"' | tr -d '"' | grep -v '^pendingMergeCriteria$' || true)"

regressed=0
improved=0
for crit in C3-lock-shape C4-release-unlink C5-pid-regex; do
  in_base=0
  grep -qx "$crit" <<<"$base_pending" && in_base=1
  is_now=0
  grep -qx "$crit" <<<"$(printf '%s\n' "${pending_now[@]:-}")" && is_now=1
  if [[ "$in_base" -eq 0 && "$is_now" -eq 1 ]]; then
    printf 'FAIL regression: %s is no longer in agreement with the repo (it was not a pending merge item)\n' "$crit" >&2
    regressed=1
  fi
  if [[ "$in_base" -eq 1 && "$is_now" -eq 0 ]]; then
    printf 'IMPROVED %s: the two sides now agree — update the baseline (--update-baseline)\n' "$crit"
    improved=1
  fi
done
[[ "$regressed" -eq 0 ]] || fail 'a previously-agreed cross-repo criterion regressed'

if [[ "$UPDATE_BASELINE" -eq 1 ]]; then
  # ★★ 豁免表必须**原样保留**（★ r1039 第三轮当场抓到的潜伏缺陷）：
  #   ★ 第一版写入器只写 `note` + `pendingMergeCriteria` ⇒ ★ **不写
  #     `probeExemptions`** ⇒ ⇒ 用它更新一次基线，P3 的豁免表就没了
  #     ⇒ ⇒ 下一个跑门的人会看到「登记册里没有豁免」的假红。
  #   ⇒ ⇒ 这里**逐字**取出原文件里的那一段并写回；★ 取不到就**拒绝写**
  #     （★ 静默写成空数组 = 悄悄丢掉 P3 的豁免 ⇒ 比报错更糟）。
  exemptions_block="$(sed -n '/"probeExemptions"/,/^  ]/p' "$BASELINE" || true)"
  if [[ -z "$exemptions_block" ]]; then
    printf 'FAIL cannot locate the "probeExemptions" block in %s — refusing to rewrite the ledger\n' "$BASELINE" >&2
    printf '  (★ writing it back empty would silently drop every P3 exemption ⇒ the next run would report a false red)\n' >&2
    exit 1
  fi
  {
    printf '{\n'
    printf '  "note": "跨仓漂移账（只降不升）：pendingMergeCriteria = 共享部署库与本仓仍然不同的判据。改动 = 先改共享库，再改本仓，最后更新本基线。",\n'
    printf '  "pendingMergeCriteria": [\n'
    first=1
    for crit in "${pending_now[@]:-}"; do
      [[ -z "$crit" ]] && continue
      [[ "$first" -eq 1 ]] || printf ',\n'
      printf '    "%s"' "$crit"
      first=0
    done
    printf '\n  ],\n'
    printf '%s\n' "$exemptions_block"
    printf '}\n'
  } > "$BASELINE"
  printf 'UPDATED baseline: %s (pending=%s)\n' "$BASELINE" "${#pending_now[@]}"
fi

printf 'cross-repo drift ledger: pending=%s (baseline pending=%s)\n' "${#pending_now[@]}" "$(grep -c . <<<"$base_pending" || true)"
printf 'ALL PASS deploy lock parity\n'
