#!/usr/bin/env python3
"""mutation_residue_guard.py —— 检出「变异脚本被中断后残留的变异」。

# 为什么需要这道门（2026-10-22 实测）

本仓有 23 个 `mutate-*` 变异门。它们的工作方式是：
  ① 把生产源码**就地改坏**（写文件）
  ② 跑判据，确认它变红
  ③ 把源码**一字不差地还原**

第 ③ 步的还原写在 `finally:` 里 ⇒ **只在 Python 正常展开时执行**。
而以下三种中断都会让 `finally` 不跑：
  ① 进程被 SIGTERM / SIGKILL 杀掉（本仓的 `timeout` 包装、任务管理器、CI 取消）；
  ② Windows 上 `subprocess.run(timeout=...)` 超时 ⇒ 子进程被 `TerminateProcess`
     ⇒ 没有任何清理机会；
  ③ 机器断电 / 会话被杀。

⇒ ★ **后果不是「门红」，而是「生产源码被留在变异态」** ——
   本轮实测复现：`domains/ursm/v2/persist/retention_partition.go` 的
   `p.dayEnd.After(cutoff)` 被留成 `p.dayEnd.Before(cutoff)`，
   `scripts/252-monitor/etc.cron.d.pg17` 的一行 cron 被删掉。
   ⇒ 在**共享仓库**里，下一个会话会把这份变异代码提交并推送 ⇒ ★★ 生产缺陷入库。

⇒ ⇒ ★ 这道门的作用是：**在变异残留进入提交之前把它挡住**。

# 判定口径（刻意保守）

只报「**确定是变异残留**」的，不报「工作区有未提交改动」：
  · 目标文件集合 = 本仓所有 `mutate-*` 脚本里**声明会写**的仓库文件；
  · 判据 = 该文件当前的**内容**与 `git show HEAD:<path>` 的内容**不一致**；
  · ⇒ 只在「工作区脏 ∧ 脏的正好是变异门会碰的文件」时报红
    ⇒ 普通开发改动（改业务代码）**不会**误报。

# 用法

    python3 scripts/audit/mutation_residue_guard.py            # 检查，有残留则退出码 1
    python3 scripts/audit/mutation_residue_guard.py --list     # 只列出可疑文件，不判红
"""
from __future__ import annotations

import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def git(*args: str) -> str:
    return subprocess.run(
        ['git', *args], cwd=ROOT, capture_output=True, encoding='utf-8', errors='replace'
    ).stdout


def mutate_scripts() -> list[str]:
    out = git('ls-files', 'scripts/*mutate*.py', 'scripts/audit/*mutate*.py')
    return [p for p in out.splitlines() if p]


def candidate_paths() -> set[str]:
    """Files the mutate scripts may write.

    Parsed from the scripts themselves (not a hand-kept list) so the gate
    cannot drift away from the scripts it guards.
    """
    found: set[str] = set()
    patterns = [
        re.compile(r'''["']((?:[\w./*-]+/)?[\w.-]+\.(?:go|sh|sql|ts|yml|yaml|cron\.\w+|md))["']'''),
        re.compile(r'''REPO\s*/\s*["']([^"']+)["']'''),
        re.compile(r'''TARGET\s*=\s*(?:REPO|Path\([^)]*\))\s*/\s*["']([^"']+)["']'''),
    ]
    for rel in mutate_scripts():
        p = ROOT / rel
        if not p.is_file():
            continue
        try:
            text = p.read_text(encoding='utf-8')
        except Exception:
            continue
        for pat in patterns:
            for m in pat.finditer(text):
                cand = m.group(1)
                if not cand or cand.startswith('/') or cand.startswith('..'):
                    continue
                # only keep things that look like repo-relative source paths
                if '/' in cand or cand.endswith(('.go', '.sh', '.sql')):
                    found.add(cand)
    return found


def head_content(path: str) -> str | None:
    out = subprocess.run(
        ['git', 'show', f'HEAD:{path}'], cwd=ROOT, capture_output=True, encoding='utf-8',
        errors='replace',
    )
    if out.returncode != 0:
        return None
    return out.stdout


def normalize(text: str) -> str:
    """Normalize line endings before comparing.

    ★ Required, not cosmetic: this repo has `core.autocrlf=true`, so the
      worktree copy of every text file has CRLF while `git show HEAD:<path>`
      returns the LF blob. Comparing raw bytes ⇒ EVERY mutation-capable file
      looks "changed" on a Windows host ⇒ the gate would be pure noise.
    ⇒ Compare with line endings normalized; anything else it reports is a
      REAL content difference.
    """
    return text.replace('\r\n', '\n').replace('\r', '\n')


def main() -> int:
    list_only = '--list' in sys.argv

    scripts = mutate_scripts()
    candidates = candidate_paths()

    # Restrict to files that actually exist in the repo.
    tracked = set(git('ls-files').splitlines())
    candidates = {c for c in candidates if c in tracked}

    dirty = [l for l in git('status', '--porcelain').splitlines() if l.strip()]
    dirty_paths = set()
    for line in dirty:
        # porcelain: XY <path>  (rename shows "old -> new")
        p = line[3:].strip()
        if ' -> ' in p:
            p = p.split(' -> ', 1)[1]
        dirty_paths.add(p.strip('"'))

    suspects = sorted(dirty_paths & candidates)

    print(f'mutate scripts scanned: {len(scripts)}')
    print(f'candidate paths derived from those scripts: {len(candidates)}')
    print(f'dirty paths in worktree: {len(dirty_paths)}')
    print(f'overlap (dirty AND mutation-capable): {len(suspects)}')

    if not suspects:
        print('\nRESULT: PASS — no mutation residue in the worktree.')
        return 0

    print('\nSuspicious files (worktree differs from HEAD AND a mutate script can write them):')
    real = []
    for p in suspects:
        cur = (ROOT / p).read_text(encoding='utf-8', errors='replace') if (ROOT / p).is_file() else ''
        head = head_content(p)
        if head is None:
            print(f'  {p}  (not in HEAD — newly added)')
            real.append(p)
            continue
        if normalize(cur) == normalize(head):
            # Only line-ending differences (core.autocrlf) ⇒ not a residue.
            continue
        real.append(p)
        cur_lines = normalize(cur).splitlines()
        head_lines = normalize(head).splitlines()
        # show the first differing line pair, for attribution
        detail = ''
        for i, (a, b) in enumerate(zip(head_lines, cur_lines)):
            if a != b:
                detail = f'\n      HEAD: {a.strip()[:120]}\n      WORK: {b.strip()[:120]}'
                break
        else:
            if len(cur_lines) != len(head_lines):
                detail = f'\n      line count HEAD={len(head_lines)} WORK={len(cur_lines)}'
        print(f'  {p}{detail}')

    if not real:
        print('\n  (all overlaps were line-ending-only differences — core.autocrlf — not residues)')

    if list_only:
        return 0

    if not real:
        print('\nRESULT: PASS — no mutation residue in the worktree.')
        return 0

    print(
        '\nRESULT: FAIL — a mutation script was interrupted before restoring its target.\n'
        '        Restore with: git checkout -- <path>\n'
        '        Do NOT commit these files.'
    )
    return 1


if __name__ == '__main__':
    sys.exit(main())
