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
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LIB = ROOT / "scripts" / "deploy-local-lib.sh"
CONTRACT = ROOT / "tests" / "deploy_switch_lock_contract_test.sh"
PARITY = ROOT / "tests" / "deploy_switch_lock_parity_test.sh"

# ── 各臂的「靶点」与「必须抓住它的判据」────────────────────────────────────
ARMS = [
    {
        "name": "N1 · 存活判据退回 kill -0 单判（EPERM 被当成进程已死）",
        "target": LIB,
        "old": """dl_pid_alive() {
  local pid="$1"
  dl_pid_valid "$pid" || return 1
  dl_pid_probe_kill "$pid" && return 0
  dl_pid_probe_ps "$pid" && return 0
  return 1
}""",
        "new": """dl_pid_alive() {
  local pid="$1"
  dl_pid_valid "$pid" || return 1
  dl_pid_probe_kill "$pid" && return 0
  return 1
}""",
        "gates": [
            (CONTRACT, "a live process we may not signal (EPERM) must be judged ALIVE"),
            (PARITY, "repo oracle says DEAD, OS says ALIVE"),
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
]


def run_gate(gate: Path) -> tuple[int, str]:
    proc = subprocess.run(
        ["bash", str(gate)], cwd=ROOT, capture_output=True, text=True
    )
    return proc.returncode, proc.stdout + proc.stderr


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

        try:
            mutated = text
            for old, new in anchors:
                mutated = mutated.replace(old, new)
            target.write_text(mutated, encoding="utf-8")
            for gate, expect in arm["gates"]:
                markers = [expect] if isinstance(expect, str) else list(expect)
                rc, out = run_gate(gate)
                rel = gate.relative_to(ROOT)
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

    print()
    if failures:
        print(f"MUTATION ARMS FAILED: {len(failures)} arm(s): {failures}")
        return 1
    print(f"ALL ARMS RED ({len(ARMS)}/{len(ARMS)}) and every file restored byte-identically")
    return 0


if __name__ == "__main__":
    sys.exit(main())
