#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""生成【多语句启动迁移的存量豁免台账】（★ 官方生成器 ★）。

★ 为什么需要它（★ 2026-11-24 会话统计修正轮 / 211 配置优化轮的发现）★★：
#
#　★ 仓里的启动迁移共 675 个（★ `sql/migrations/startup/*.sql`、★ 不含 `.down.sql` ★）★★
#　★ 其中 **429 个已经事务包裹**（★ `BEGIN;` … `COMMIT;` ★）、★ **244 个没有** ★★ ★★
#　★ ⇒ ★★ **「多语句文件必须事务包裹」这条判据在【存量水位】上不成立**（★ 改那 244 个
#　★ 　 ⇒ 触及已 applied 字节 ⇒ ★★ **绝对不可行** ★★）★★ ⇒ ★★ **⇒ 判据必须做成【增量口径】★★**
#
# ★★ **本生成器与那道门共享一个【基线 pin】★★（★ `scripts/audit/asset/multi_statement_migration_baseline.ref` ★）：
#
#　★ **基线之前**存在的「多语句且未包裹」文件 ⇒ ★★ **存量豁免**（★ 登记进台账 ★★）
#　★ **基线之后**新增的迁移文件 ⇒ ★★ **必须事务包裹**（★ 不在台账里 ⇒ 门判红 ★★）
#　★ ⇒ ★★ **⇒ 豁免是【显式的、可审计的】：★ 想让一个新文件豁免 ⇒ 必须【前移基线 pin】★★
#　★ ⇒ ★★ **⇒ 豁免集随 pin 前移而自然收敛（★ 而不会变成一个只增不减的垃圾桶 ★★）★★
#
# ★★★ **通用式：★ 「这条规则要普遍适用」这句话、★ 在存量水位上常常【不成立】★★ ⇒ ★★★
#　★★ **★ ⇒ 正确做法是【把判据做成增量口径 ＋ 把存量做成一次性快照豁免】★★ ★★ ★★★
#
# 用法：
#　python3 scripts/audit/measure-multi-statement-migrations.py --out <台账路径>
#
# ★ 台账是**生成物** ⇒ ★ **不要手改**（★ 手改会被门抓到：★ 门要求台账与现实双向闭合 ★★）。
"""

import argparse
import os
import re
import subprocess
import sys

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
STARTUP_DIR = "sql/migrations/startup"
BASELINE_REF_FILE = "scripts/audit/asset/multi_statement_migration_baseline.ref"

BEGIN_RE = re.compile(r"(?im)^\s*BEGIN\s*;")
COMMIT_RE = re.compile(r"(?im)^\s*COMMIT\s*;")


def read_baseline_pin(repo_root: str) -> str:
    """读基线 pin（★ 纯文本一行 = 一个 commit sha ★）。"""
    path = os.path.join(repo_root, BASELINE_REF_FILE)
    if not os.path.exists(path):
        raise SystemExit(f"★ 找不到基线 pin 文件：{BASELINE_REF_FILE}")
    with open(path, encoding="utf-8") as fh:
        lines = [l.strip() for l in fh.read().split("\n") if l.strip() and not l.strip().startswith("#")]
    if not lines:
        raise SystemExit(f"★ 基线 pin 文件为空：{BASELINE_REF_FILE}")
    return lines[0]


def list_startup_files(repo_root: str) -> list:
    """列启动迁移目录里的文件（★ 不含 `.down.sql` ★）。"""
    d = os.path.join(repo_root, STARTUP_DIR)
    if not os.path.isdir(d):
        raise SystemExit(f"★ 找不到启动迁移目录：{STARTUP_DIR}")
    return sorted(f for f in os.listdir(d) if f.endswith(".sql") and not f.endswith(".down.sql"))


def count_statements(text: str) -> int:
    """数「有效语句行」（★ 去掉空行与注释行 ★）。"""
    n = 0
    for raw in text.split("\n"):
        s = raw.strip()
        if not s or s.startswith("--"):
            continue
        n += 1
    return n


def is_transaction_wrapped(text: str) -> bool:
    """是否被 `BEGIN;` … `COMMIT;` 包裹。"""
    return bool(BEGIN_RE.search(text)) and bool(COMMIT_RE.search(text))


def baseline_files(repo_root: str, pin: str) -> set:
    """列基线 pin 时已存在的启动迁移文件（★ 用 git 读历史树 ★）。"""
    out = subprocess.run(
        ["git", "-C", repo_root, "ls-tree", "-r", "--name-only", pin, "--", STARTUP_DIR],
        capture_output=True, text=True,
    )
    if out.returncode != 0:
        raise SystemExit(f"★ 读基线 pin 失败（★ {pin} ★）：{out.stderr.strip()}")
    names = set()
    for p in out.stdout.split("\n"):
        if not p.strip():
            continue
        base = os.path.basename(p)
        if base.endswith(".sql") and not base.endswith(".down.sql"):
            names.add(base)
    return names


def main() -> int:
    ap = argparse.ArgumentParser(description="生成多语句启动迁移的存量豁免台账")
    ap.add_argument("--out", required=True, help="输出 TSV 路径")
    ap.add_argument("--repo-root", default=REPO_ROOT)
    args = ap.parse_args()

    repo_root = os.path.abspath(args.repo_root)
    pin = read_baseline_pin(repo_root)
    base_set = baseline_files(repo_root, pin)

    rows = []
    for f in list_startup_files(repo_root):
        with open(os.path.join(repo_root, STARTUP_DIR, f), encoding="utf-8") as fh:
            text = fh.read()
        n = count_statements(text)
        if n <= 1:
            continue
        if is_transaction_wrapped(text):
            continue
        rows.append((f, n, "是" if f in base_set else "否"))

    # ★ 只登记【基线之前就存在的】存量 ⇒ ★ 新文件不得进台账（★ 豁免只能靠前移 pin ★）
    legacy = [r for r in rows if r[2] == "是"]
    after_base = [r for r in rows if r[2] == "否"]

    out_path = os.path.join(repo_root, args.out) if not os.path.isabs(args.out) else args.out
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w", encoding="utf-8") as fh:
        fh.write("# multi_statement_migration_legacy.tsv — 存量豁免台账（★ 生成物、★ 不要手改 ★）\n")
        fh.write(f"# 生成器：scripts/audit/measure-multi-statement-migrations.py\n")
        fh.write(f"# 基线 pin：{pin}\n")
        fh.write("# 语义：★ 基线之前存在、★ 且【多语句且未事务包裹】的启动迁移 ⇒ ★ 登记在此即豁免 ★\n")
        fh.write("# ★ 新增迁移不在此表 ⇒ 门判红（★ 豁免须【前移基线 pin】、★ 而非【往表里加一行】★）\n")
        fh.write("# 列：file\tstatements\tin_baseline\n")
        for f, n, _ in legacy:
            fh.write(f"{f}\t{n}\t是\n")

    print(f"✔ 台账已生成：{args.out}")
    print(f"  基线 pin：{pin}")
    print(f"  启动迁移：{len(list_startup_files(repo_root))} 个")
    print(f"  存量豁免（基线前已存在、★ 多语句且未包裹）：{len(legacy)} 个")
    if after_base:
        print(f"  ★★ 【基线之后新增的、未包裹】：{len(after_base)} 个 ⇒ ★★ 它们【不在台账里】⇒ ★★ 门会判红 ★★")
        for f, n, _ in after_base:
            print(f"     {f}（★ 语句 {n} ★）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
