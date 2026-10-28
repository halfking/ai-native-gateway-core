#!/usr/bin/env python3
"""Mutation arms for the switch-lock liveness / parity gates.

2026-10-28（r1039 后续轮 · 第二轮批判性审计）。
审计档：docs/audit/2026-10-28-r1039-后续轮-批判性审计-第二轮.md

★ 为什么要有这个脚本：仓内纪律「判据必须能红」。本轮新增的判据
  （I11~I17 + 跨仓对拍门）全部是**新写**的 ⇒ 「它们能抓住对应的缺陷吗」
  只能靠**真仓变异臂**回答，不能靠「读起来对」。

用法：
    python3 scripts/audit/mutate_r1039_switch_lock_liveness.py [--keep-going]

退出码：0 = 全部臂都「红了且还原后逐字一致」；1 = 有臂没红 / 还原不一致。
"""

from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LIB = ROOT / "scripts" / "deploy-local-lib.sh"
CONTRACT = ROOT / "tests" / "deploy_switch_lock_contract_test.sh"
PARITY = ROOT / "tests" / "deploy_switch_lock_parity_test.sh"
BASELINE = ROOT / "tests" / "deploy_lock_parity_baseline.json"
HOLLOWOUT_GO = ROOT / "scripts" / "ursmcheck" / "structured_registry_hollowout_test.go"

# ── 各臂的「靶点」与「必须抓住它的判据」────────────────────────────────────
ARMS = [
    {
        # ★ 第三轮重写：判据已**委托**共享部署库 ⇒ 这条臂改成
        #   「把委托换回自带探针」⇒ 复现「两份实现」这个缺陷类别本身。
        #   ⇒ 它必须同时被两道门抓住：
        #     ① 契约门：判据说「活」时本仓必须说「活」⇒ 本仓自带 kill -0
        #        单判会说「死」⇒ 红；
        #     ② 跨仓门：**结构性**判据 ⇒ 生产脚本里出现了 `kill -0` ⇒ 红。
        "name": "N1 · 把委托换回自带 kill -0 探针（EPERM 被当成进程已死 ⇒ 又变成两份实现）",
        "target": LIB,
        "old": """dl_pid_alive() {
  local pid="$1"
  dl_pid_valid "$pid" || return 1
  if ! command -v lock_pid_alive >/dev/null 2>&1; then
    printf 'ERROR: shared deploy library liveness oracle (lock_pid_alive) is not loaded\\n' >&2
    return 2
  fi
  lock_pid_alive "$pid"
}""",
        "new": """dl_pid_alive() {
  local pid="$1"
  dl_pid_valid "$pid" || return 1
  kill -0 "$pid" 2>/dev/null && return 0
  return 1
}""",
        "gates": [
            (CONTRACT, "a live foreign-user pid must be ALIVE"),
            (PARITY, "the repo must not carry its own liveness probe"),
        ],
    },
    {
        # ★ 第三轮新增：委托之后，「判据不知道」必须**说不知道**（rc=2）。
        #   猜「死」= 把一把可能还活着的锁当残锁接管 ⇒ F1 换入口复活。
        "name": "N7 · 判据缺席时「猜死」（把 UNKNOWN 降级成 DEAD）",
        "target": LIB,
        "old": """  if ! command -v lock_pid_alive >/dev/null 2>&1; then
    printf 'ERROR: shared deploy library liveness oracle (lock_pid_alive) is not loaded\\n' >&2
    return 2
  fi""",
        "new": """  if ! command -v lock_pid_alive >/dev/null 2>&1; then
    return 1
  fi""",
        "gates": [
            (CONTRACT, "must be UNKNOWN (rc=2), not DEAD (rc=1)"),
        ],
    },
    {
        # ★ 第三轮新增：入口的前置检查去掉 ⇒ 判据缺席也继续跑
        #   ⇒ 要么把「不知道」当「没人持有」而接管活锁，要么造出一把没人认领的锁。
        "name": "N8 · 去掉入口的 fail-fast 前置检查（判据缺席也继续跑）",
        "target": LIB,
        "old": """  dl_switch_lock_require_ssot || return 75""",
        "new": """  :""",
        "gates": [
            (CONTRACT, "acquiring the switch lock must fail fast with 75"),
        ],
    },
    {
        # ★ 第三轮新增：恢复命令在「判据不知道」时照样清锁 ⇒ fail-open。
        "name": "N9 · 恢复命令在「判据不知道」时照样清锁（fail-open）",
        "target": LIB,
        "old": """  if [[ "$pid_rc" -ne 1 ]]; then
    printf 'ERROR: refusing to remove the deployment switch lock: liveness of holder pid %s could not be determined (the shared deploy library oracle is not loaded)\\n' "$holder_pid" >&2
    return 1
  fi
""",
        "new": "",
        "gates": [
            (CONTRACT, "recovery must refuse when the liveness of the holder cannot be determined"),
        ],
    },
    {
        "name": "N2 · 去掉形态判定（目录形态的锁也去走 flock 分支）",
        "target": LIB,
        "old": """  if [[ -d "$path" ]]; then
    dl_acquire_switch_lock_dir
    return $?
  fi
  if [[ -n "$flock_bin" ]]; then""",
        "new": """  if [[ -n "$flock_bin" ]]; then""",
        "gates": [
            (
                CONTRACT,
                [
                    "a stale directory-shaped lock must be taken over even when flock is available",
                    "a live directory-shaped lock must fail fast with 75 even when flock is available",
                ],
            ),
        ],
    },
    {
        # ★ N2b：把**两层防御**一起去掉 ⇒ 复现原始缺陷：
        #   `exec <> 目录` ⇒ EISDIR ⇒ POSIX 规定 exec 的重定向失败是**致命**的
        #   ⇒ 整个部署脚本带着 "Is a directory" 死掉（而不是 rc=75 快速失败）。
        "name": "N2b · 形态判定 + exec 前守卫一起去掉（复现原始的致命 exec）",
        "target": LIB,
        "old": """  if [[ -d "$path" ]]; then
    dl_acquire_switch_lock_dir
    return $?
  fi
  if [[ -n "$flock_bin" ]]; then
    # ★ 既不是文件也不是目录（FIFO / 设备 …）⇒ 绝不对它 exec（exec 重定向失败是致命的）
    if [[ -e "$path" && ! -f "$path" ]]; then
      printf 'ERROR: deployment switch lock path %s exists but is not a regular file (refusing)\\n' "$path" >&2
      return 75
    fi
    dl_acquire_switch_lock_flock "$flock_bin"
    return $?
  fi""",
        "new": """  if [[ -n "$flock_bin" ]]; then
    dl_acquire_switch_lock_flock "$flock_bin"
    return $?
  fi""",
        "old2": """  if [[ -e "$path" ]]; then
    if [[ ! -f "$path" || ! -w "$path" ]]; then
      printf 'ERROR: deployment switch lock file %s is not a writable regular file (refusing)\\n' "$path" >&2
      return 75
    fi
  elif [[ ! -d "$dir" || ! -w "$dir" ]]; then
    printf 'ERROR: deployment switch lock directory %s is not writable (refusing)\\n' "$dir" >&2
    return 75
  fi
""",
        "new2": "",
        "gates": [
            (
                CONTRACT,
                [
                    "a live directory-shaped lock must fail fast with 75 even when flock is available",
                    "a stale directory-shaped lock must be taken over even when flock is available",
                ],
            ),
        ],
    },
    {
        "name": "N3 · 恢复命令在「持有者读不出来」时直接清掉（与残锁判定口径相反）",
        "target": LIB,
        "old": """  if [[ -z "$holder_pid" ]]; then
    printf 'ERROR: refusing to remove the deployment switch lock: holder pid cannot be determined (metadata missing or incomplete)\\n' >&2
    if [[ -n "$meta" ]]; then
      printf 'holder metadata:\\n' >&2
      cat "$meta" >&2
    fi
    printf 'HINT: if you are sure no deploy is running, run: bash scripts/deploy-local.sh recover-switch-lock --force\\n' >&2
    return 1
  fi
""",
        "new": "",
        "gates": [
            (CONTRACT, "recovery must REFUSE when the holder cannot be identified"),
        ],
    },
    {
        "name": "N4 · flock 释放后又删锁文件（unlink-after-unlock ⇒ 两个持有者）",
        "target": LIB,
        "old": """    eval "exec ${DL_SWITCH_LOCK_FD}<&-"
    DL_SWITCH_LOCK_FD=
    return 0""",
        "new": """    eval "exec ${DL_SWITCH_LOCK_FD}<&-"
    DL_SWITCH_LOCK_FD=
    rm -f "$path"
    return 0""",
        "gates": [
            (CONTRACT, "flock release must NOT unlink the lock file"),
        ],
    },
    {
        "name": "N5 · 元数据完整性检查去掉（半截元数据被当成存活证据 ⇒ 接管活锁）",
        "target": LIB,
        "old": """  grep -q '^pid=' "$meta" 2>/dev/null || return 1
  grep -q '^started_at=' "$meta" 2>/dev/null || return 1
""",
        "new": """  grep -q '^pid=' "$meta" 2>/dev/null || return 1
""",
        "gates": [
            (CONTRACT, "a half-written metadata file must NOT be treated as a stale lock"),
        ],
    },
    {
        "name": "N6 · --force 与别的动作同用时不再拒绝（静默忽略一个「强制」标记）",
        "target": ROOT / "scripts" / "deploy-local.sh",
        "old": """  if (( RECOVER_FORCE )) && [[ "${ACTION:-}" != "recover-switch-lock" ]]; then
    printf 'error: --force is only valid with recover-switch-lock (got action: %s)\\n' "${ACTION:-<none>}" >&2
    exit 64
  fi""",
        "new": """  if (( RECOVER_FORCE )) && [[ "${ACTION:-}" == "__never__" ]]; then
    printf 'error: --force is only valid with recover-switch-lock (got action: %s)\\n' "${ACTION:-<none>}" >&2
    exit 64
  fi""",
        "gates": [
            (CONTRACT, "--force with a non-recover action must be refused with 64"),
        ],
    },
    {
        # ★ 第三轮新增：争用退回普通 die ⇒ rc 75 的 CLI 契约失效
        #   ⇒ 「稍后重试」与「硬失败」在监督方眼里不可区分。
        "name": "N10 · 争用退回普通 die（rc 75 的 CLI 契约失效）",
        "target": ROOT / "scripts" / "deploy-local.sh",
        "old": """  dl_acquire_switch_lock || die75 'another deployment is switching the active port; retry after it finishes'""",
        "new": """  dl_acquire_switch_lock || die 'another deployment is switching the active port; retry after it finishes'""",
        "gates": [
            (CONTRACT, "must use the 75-exit helper"),
        ],
    },
    {
        # ★ 第三轮新增：mkdir 形态不再自己建父目录（形态不对称 ⇒ 报错理由不对）
        "name": "N11 · mkdir 形态不再建父目录（与 flock 形态不对称）",
        "target": LIB,
        "old": """  # ★ 父目录要**先建**（flock 形态那一支一直这么做 ⇒ ★ 两种形态不该有差别）。
  #   缺了它 ⇒ 父目录不存在时 `mkdir "$path"` 直接失败 ⇒ 报出来的是
  #   「锁被占用」，而真相是「路径不存在」⇒ ★ 报错理由不对，运维会照着
  #   错误结论去 unlock 一把并不存在的锁。
  mkdir -p "$dir" 2>/dev/null || true
""",
        "new": "",
        "gates": [
            (CONTRACT, "the mkdir shape must create its own parent directory"),
        ],
    },
    {
        # ★ 第三轮（收尾）新增：★ 把「基线形状契约」从对拍门里删掉
        #   ⇒ ⇒ 漂移账被掏空时，**读方会改用另一条理由红**（★ P3 的
        #   「豁免表空了」⇒ 探针命中无人认领）⇒ ⇒ 而普查册登记的
        #   red_signal 是 "baseline shape broken" ⇒ ⇒ **红因对不上**
        #   ⇒ ⇒ 普查侧的掏空门必须报「红了，但红因不对」⇒ ⇒ 证明
        #   ★★ **red_signal 是承重的**（★ 不是「退出码非 0 就算有牙」）。
        "name": "N12 · 对拍门删掉基线形状契约（掏空后改用别的理由红 ⇒ 红因对不上）",
        "target": PARITY,
        "old": r"""for key in '"note"' '"pendingMergeCriteria"' '"probeExemptions"'; do
  if ! grep -qF "$key" "$BASELINE"; then
    printf 'FAIL baseline shape broken: %s is missing from %s\n' "$key" "$BASELINE" >&2
    printf '  the drift ledger is a registry this gate reads: its keys are part of the contract\n' >&2
    printf '  (★ a missing key would silently read as an empty list ⇒ the ratchet would compare against nothing)\n' >&2
    exit 1
  fi
done
""",
        "new": "",
        # ★ ★ 副作用：★ 掏空门会**真的把漂移账掏空再还原** ⇒ ⇒ 本臂
        #   额外盯一次基线文件（★ 门自己会还原，★ 这里再兜一层）。
        "side_files": [BASELINE],
        "gates": [
            (
                {
                    "cmd": [
                        "go",
                        "test",
                        "./scripts/ursmcheck/",
                        "-run",
                        "TestStructuredRegistryHollowoutReadersRejectEmptyRegistry/tests/deploy_lock_parity_baseline.json",
                        "-timeout",
                        "10m",
                    ]
                },
                "红了，但红因不对",
            ),
        ],
    },
    {
        # ★ 第三轮（收尾）新增：★ 把普查侧的读方形态词表里新加的 `shell` 删掉
        #   ⇒ ⇒ 漂移账这条条目（★ 读方是 shell 脚本）就**不再有归属**
        #   ⇒ ⇒ 普查门必须红 ⇒ ⇒ 证明词表那一行是承重的（★ 词表比
        #   共享 harness 窄这件事，必须有人补上，否则新登记册只能被当
        #   「没归属」⇒ ⇒ 那是一条**假红**，不是一条有牙的门）。
        "name": "N13 · 普查侧读方词表去掉 shell（这条登记册就变成「没归属」）",
        "target": HOLLOWOUT_GO,
        "old": '\t"shell":  true,\n',
        "new": "",
        "gates": [
            (
                {
                    "cmd": [
                        "go",
                        "test",
                        "./scripts/ursmcheck/",
                        "-run",
                        "TestStructuredRegistryCensus",
                        "-timeout",
                        "10m",
                    ]
                },
                "必须给出 reader_kind",
            ),
        ],
    },
    {
        # ★ 第三轮（收尾）新增：★ 让 `--update-baseline` 的写入器**丢掉豁免表**
        #   （★ 这是本轮当场抓到的潜伏缺陷）⇒ ⇒ 基线被改写后 P3 的豁免表没了
        #   ⇒ ⇒ 下一个跑门的人会看到「登记册里没有豁免」的假红
        #   ⇒ ⇒ 写回器必须让这条**当场红**。
        "name": "N14 · --update-baseline 丢掉豁免表（改写后基线让 P3 假红）",
        "target": PARITY,
        "old": r"""  exemptions_block="$(sed -n '/"probeExemptions"/,/^  ]/p' "$BASELINE" || true)"
""",
        "new": """  exemptions_block='  "probeExemptions": []'
""",
        # ★ ★ 副作用：★ 这条臂会真的改写基线文件 ⇒ ⇒ 跑完必须逐字还原。
        "side_files": [BASELINE],
        "gates": [
            (
                {
                    "cmd": [
                        "bash",
                        "-c",
                        "bash tests/deploy_switch_lock_parity_test.sh --update-baseline >/dev/null 2>&1 || true; "
                        "bash tests/deploy_switch_lock_parity_test.sh",
                    ]
                },
                "the repo must not carry its own liveness probe",
            ),
        ],
    },
]


def resolve_go() -> str:
    """★ 解析 go 二进制（★ 与仓内 Go 侧 harness 同一套兜底）。

    ★ 为什么要有：★ 变异臂要在**本机**跑 Go 门（★ 普查 / 掏空那两道在
    `scripts/ursmcheck/`）⇒ ⇒ 而本机的 go 不一定在 PATH 上
    （★ `/Users/niuer/sdk/go/bin`）⇒ ⇒ 找不到就报清楚，别静默跳过。
    """
    p = shutil.which("go")
    if p:
        return p
    goroot = os.environ.get("GOROOT", "")
    if goroot:
        cand = Path(goroot) / "bin" / "go"
        if cand.exists():
            return str(cand)
    raise RuntimeError("找不到 go 二进制（★ 本臂需要跑 Go 门）")


def run_gate(gate) -> tuple[int, str]:
    """★ 跑一道门 ⇒ 返回 (退出码, 输出)。

    ★ 门有两种形态：
      · `Path`         ⇒ `bash <脚本>`（★ 本轮前 11 条臂都是这一种）
      · `{"cmd": [...]}` ⇒ 任意命令（★ 第三轮收尾新增：★ 有些判据在
        Go 侧（★ 普查门 / 掏空门）⇒ ⇒ 只跑 shell 门够不着它们）
    """
    if isinstance(gate, dict):
        argv = list(gate["cmd"])
        if argv and argv[0] == "go":
            argv[0] = resolve_go()
        proc = subprocess.run(argv, cwd=ROOT, capture_output=True, text=True)
        return proc.returncode, proc.stdout + proc.stderr
    proc = subprocess.run(
        ["bash", str(gate)], cwd=ROOT, capture_output=True, text=True
    )
    return proc.returncode, proc.stdout + proc.stderr


def gate_label(gate) -> str:
    if isinstance(gate, dict):
        return " ".join(gate["cmd"])[:120]
    return str(Path(gate).relative_to(ROOT))


def main() -> int:
    keep_going = "--keep-going" in sys.argv
    failures: list[str] = []

    for arm in ARMS:
        target: Path = arm["target"]
        original = target.read_bytes()
        md5_before = hashlib.md5(original).hexdigest()
        text = original.decode("utf-8")

        # ★ 一条臂可以有多个锚点（old/old2/…）：每个都必须**恰好命中一次**，
        #   否则这条臂的「靶点」就不是它以为的那个 ⇒ 拒绝跑（★ 靶点错了 ⇒ 读数无效）。
        anchors = []
        if "old" in arm:
            anchors.append((arm["old"], arm.get("new", "")))
        i = 2
        while f"old{i}" in arm:
            anchors.append((arm[f"old{i}"], arm.get(f"new{i}", "")))
            i += 1
        if not anchors:
            print(f"FAIL {arm['name']}: no mutation anchor declared")
            failures.append(arm["name"])
            continue
        bad = False
        for old, _new in anchors:
            hits = text.count(old)
            if hits != 1:
                print(f"FAIL {arm['name']}: anchor found {hits} times (need exactly 1)")
                bad = True
        if bad:
            failures.append(arm["name"])
            continue

        # ★ ★ 副作用文件：★ 有些臂会**真的改写别的文件**（★ 例：N14 让
        #   `--update-baseline` 重写漂移账）⇒ ⇒ 跑完必须逐字还原 ⇒ ⇒ 否则
        #   「臂跑过了」会留下一个**被改坏的仓**（★ 而仓里的门正在读它）。
        side_files = arm.get("side_files", [])
        side_originals = {p: Path(p).read_bytes() for p in side_files}

        try:
            mutated = text
            for old, new in anchors:
                mutated = mutated.replace(old, new)
            target.write_text(mutated, encoding="utf-8")
            for gate, expect in arm["gates"]:
                markers = [expect] if isinstance(expect, str) else list(expect)
                rc, out = run_gate(gate)
                rel = gate_label(gate)
                if rc == 0:
                    print(f"FAIL {arm['name']}: {rel} did NOT go red (rc=0)")
                    failures.append(arm["name"])
                    continue
                hit = [m for m in markers if m in out]
                if not hit:
                    print(f"FAIL {arm['name']}: {rel} went red but NOT for the expected reason")
                    print("  expected marker(s): " + " | ".join(markers))
                    print("  gate output tail:\n" + "\n".join(out.splitlines()[-12:]))
                    failures.append(arm["name"])
                    continue
                print(f"RED  {arm['name']} → {rel} (rc={rc}, marker hit: {hit[0][:48]!r})")
        finally:
            target.write_bytes(original)
            md5_after = hashlib.md5(target.read_bytes()).hexdigest()
            if md5_after != md5_before:
                print(f"FAIL {arm['name']}: restore is NOT byte-identical ({md5_before} != {md5_after})")
                failures.append(arm["name"])
            else:
                print(f"     restored {target.relative_to(ROOT)} md5={md5_after}")
            # ★ 副作用文件同样逐字还原（★ 顺序：★ 先还原靶点，★ 再还原副作用文件）。
            for p, blob in side_originals.items():
                Path(p).write_bytes(blob)
                if Path(p).read_bytes() != blob:
                    print(f"FAIL {arm['name']}: side file {p} was NOT restored byte-identically")
                    failures.append(arm["name"])
                else:
                    print(f"     restored {Path(p).relative_to(ROOT)} md5={hashlib.md5(blob).hexdigest()}")

    print()
    if failures:
        print(f"MUTATION ARMS FAILED: {len(failures)} arm(s): {failures}")
        return 1
    print(f"ALL ARMS RED ({len(ARMS)}/{len(ARMS)}) and every file restored byte-identically")
    return 0


if __name__ == "__main__":
    sys.exit(main())
