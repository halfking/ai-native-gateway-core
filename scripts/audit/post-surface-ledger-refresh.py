#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""post-surface-ledger-refresh.py —— **补齐** POST 面台账里缺失的路由（★ 不重写既有行）。

── 为什么需要它（★ 2026-10-29 审计轮） ──────────────────────────────────────────

本仓的「POST 面台账」是 `scripts/audit/asset/post-surface-ledger.tsv`，
它的行数必须等于 `scripts/audit/asset/admin-routes.txt` 的行数
（门 `TestPostSurfaceLedgerCoversEveryRoute`）。

⇒ ★★ **这个漂移已经复发过多次**：
  · 2026-10-07（96h 审计档）  记为「589 ≠ 591 ⇒ 登记，不代劳」；
  · 2026-10-29（12h 审计档）  记为「589 ≠ 592 ⇒ 上游新增端点后只刷了 admin-routes.txt、
    没刷 post-surface 台账」。
⇒ ⇒ **同一个漂移，两次被记下来，两次都没有被修** ⇒ ⇒
⇒ ⇒ ★★ **真正缺的不是「发现」的工具（门当场就红了），而是「一条不破坏既有裁决就能补齐」的路径**。

── 为什么**不能**直接重跑生成器（★ 这是本脚本存在的核心理由） ───────────────────

`post-surface-probe.sh ledger` 是生成器，理论上能重出台账。
★ **但重跑会毁掉 9 行【人工改判】**：
  台账里有 9 行是审计轮**按 handler 源码手工改判**的（class 由 `read` 改写成 `write`，
  note 里写明「★ 源码确证 write：POST-only（:64 method guard）出账 ⇒ 静态分级器判 read 是调用链盲区」）。
⇒ 而静态分级器**至今仍判 read**（那是它自己的已知盲区，R159 §10.2 同族反例）
⇒ ⇒ **重跑生成器 = 把人工裁决静默改回机器盲区** ⇒ ⇒ ★★ **那是回退，不是修复。**

⇒ ⇒ **所以本脚本只做一件事**：把「路由清单里有、但台账里没有」的那几条**补进去**，
  ★ **既有行逐字保留**（人工改判、观测值、note 全部原样）。

── 新行怎么裁决（★ 不伪造观测） ───────────────────────────────────────────────

新路由**没有被探测过** ⇒ ⇒ 探测桶（bucket）为空 ⇒ ⇒
裁决**完全由静态分级推导**（`post-surface-probe.sh ledger` 自己的 `derive()` 规则）：
  write → not-probed-writes · external → not-probed-external
  unknown → not-probed-unresolved · read → not-probed-not-reached（★ 「只读却没探测到 ⇒ 需查」）
⇒ ★★ **本脚本不编造任何 code/bucket**（那两列留空）⇒ ⇒
⇒ ⇒ ★★ **「没探测过」这件事在台账里是【如实可见】的**，不是「看起来探测过了」。

── 裁决规则**不重写**（★ 只调用生成器） ─────────────────────────────────────────

新行的 9 列由 `post-surface-probe.sh ledger` 生成（喂一个只含新行的迷你 class 文件 +
一个只含 `#meta` 的迷你 probe 文件）⇒ ⇒ **裁决规则只有一份**（生成器里的 awk）
⇒ ⇒ **本脚本不会与生成器漂移**。

── 用法 ──────────────────────────────────────────────────────────────────────

    bash scripts/audit/post-surface-ledger-refresh.py            # dry-run：只报告
    bash scripts/audit/post-surface-ledger-refresh.py --apply    # 真正补进台账

★ dry-run 是默认 ⇒ 误用不会写盘。
"""

from __future__ import annotations

import os
import subprocess
import sys
import tempfile
from collections import defaultdict, deque

REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

ROUTES_REL = "scripts/audit/asset/admin-routes.txt"
LEDGER_REL = "scripts/audit/asset/post-surface-ledger.tsv"
CLASSIFIER_REL = "scripts/audit/post-surface-classify.py"
GENERATOR_REL = "scripts/audit/post-surface-probe.sh"

APPLY_FLAG = "--apply"


def die(msg: str) -> "None":
    sys.stderr.write("FATAL: %s\n" % msg)
    sys.exit(1)


def read_rows(path: str):
    """读一个 TSV ⇒ (header_lines, data_rows)。★ 注释行（#）与数据行分开。"""
    header, rows = [], []
    with open(path, encoding="utf-8") as fh:
        for raw in fh:
            line = raw.rstrip("\n")
            if not line:
                continue
            if line.startswith("#"):
                header.append(line)
            else:
                rows.append(line.split("\t"))
    return header, rows


def run_classifier(routes_abs: str) -> str:
    """跑静态分级器 ⇒ class TSV 文本。★ 分级器是仓内唯一的静态事实源。"""
    proc = subprocess.run(
        [sys.executable, os.path.join(REPO_ROOT, CLASSIFIER_REL), routes_abs],
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        die("分级器失败（exit=%d）：\n%s" % (proc.returncode, proc.stderr))
    return proc.stdout


def derive_new_rows(new_class_rows, header_hint_target: str):
    """★ 用【生成器本身】推导新行的 9 列 ⇒ 裁决规则不重写、不漂移。"""
    with tempfile.TemporaryDirectory() as tmp:
        class_path = os.path.join(tmp, "class-mini.tsv")
        probe_path = os.path.join(tmp, "probe-mini.tsv")

        with open(class_path, "w", encoding="utf-8") as fh:
            fh.write("#post-surface-classify\tversion\t2\n")
            for row in new_class_rows:
                fh.write("\t".join(row) + "\n")

        # ★ 迷你 probe：**只有 #meta，没有任何数据行** ⇒ ⇒ 新路由的 bucket/code 必为空
        # ⇒ ⇒ 裁决只可能来自静态分级 ⇒ ⇒ **不伪造任何探测观测**。
        with open(probe_path, "w", encoding="utf-8") as fh:
            fh.write("#meta\tprobe_version\t1\n")
            fh.write("#meta\ttarget\t%s\n" % (header_hint_target or ""))
            fh.write("#meta\treconstructed_by\tpost-surface-ledger-refresh.py (★ 无探测观测)")

        proc = subprocess.run(
            ["bash", os.path.join(REPO_ROOT, GENERATOR_REL), "ledger", probe_path, class_path],
            capture_output=True,
            text=True,
        )
        if proc.returncode != 0:
            die("生成器失败（exit=%d）：\n%s" % (proc.returncode, proc.stderr))
        # ★ ledger 模式把台账打到 stdout（★ 头部 # 行 + 数据行）。
        ledger_text = proc.stdout

    _, rows = read_rows_from_text(ledger_text)
    if len(rows) != len(new_class_rows):
        die("生成器产出 %d 行 ≠ 新路由 %d 行 ⇒ 中止" % (len(rows), len(new_class_rows)))
    return rows


def main() -> int:
    routes_abs = os.path.join(REPO_ROOT, ROUTES_REL)
    ledger_abs = os.path.join(REPO_ROOT, LEDGER_REL)
    for abs_path in (routes_abs, ledger_abs):
        if not os.path.exists(abs_path):
            die("文件不存在：%s" % abs_path)

    header, ledger_rows = read_rows(ledger_abs)
    class_text = run_classifier(routes_abs)
    _, class_rows = read_rows_from_text(class_text)

    if len(class_rows) != len(ledger_rows) + 0 and len(class_rows) == len(ledger_rows):
        # 路由数与台账行数一致 ⇒ 无漂移
        pass

    # ── ① 把台账行按 path 建队列（★ 重复 path 要按出现次序配对，不能按集合比） ──
    ledger_by_path = defaultdict(deque)
    for idx, row in enumerate(ledger_rows):
        ledger_by_path[row[0]].append(idx)

    # ── ② 按 class 文件顺序走一遍：能配上的是既有行，配不上的是【新路由】 ──
    matched = {}          # class 行下标 → ledger 行下标
    used_ledger = set()
    new_class_idx = []
    for ci, crow in enumerate(class_rows):
        q = ledger_by_path.get(crow[0])
        if q:
            li = q.popleft()
            matched[ci] = li
            used_ledger.add(li)
        else:
            new_class_idx.append(ci)

    orphan = [i for i in range(len(ledger_rows)) if i not in used_ledger]
    if orphan:
        # ⇒ 台账里有、而当前路由清单里没有的路由 ⇒ ★ 路由被删了 ⇒ 裁决要人来定 ⇒ 中止。
        sample = [ledger_rows[i][0] for i in orphan[:5]]
        die(
            "台账里有 %d 条已不在当前路由清单里（例：%s）⇒ ★ 路由被删/改名 ⇒ "
            "裁决需要人工判断 ⇒ 本脚本不代劳（只补不删）" % (len(orphan), "、".join(sample))
        )

    if not new_class_idx:
        print("POST 面台账与当前路由清单一致（%d 条）⇒ 无需补齐。" % len(class_rows))
        return 0

    target = ""
    for line in header:
        if line.startswith("#gen_target\t"):
            target = line.split("\t", 1)[1]
    new_rows = derive_new_rows([class_rows[i] for i in new_class_idx], target)

    print("⇒ 路由清单 %d 条 / 台账 %d 条 ⇒ ★ 缺 %d 条：" % (len(class_rows), len(ledger_rows), len(new_rows)))
    for i in new_class_idx:
        crow = class_rows[i]
        print("   + %s\t%s\t%s\t(class=%s)" % (crow[0], crow[1], crow[2], crow[4]))
    for row in new_rows:
        print("     ⇒ 裁决：%s（%s）" % (row[7], row[8]))

    if "--apply" not in sys.argv:
        print("\n（dry-run；加 --apply 才写盘）")
        return 0

    # ── ③ 合并：既有行逐字保留，新行插到它在 class 顺序里的位置 ──
    merged = []
    new_iter = iter(new_rows)
    for ci, crow in enumerate(class_rows):
        if ci in matched:
            merged.append(ledger_rows[matched[ci]])
        else:
            merged.append(next(new_iter))

    if len(merged) != len(class_rows):
        die("合并后 %d 行 ≠ 路由清单 %d 行 ⇒ 中止" % (len(merged), len(class_rows)))

    stamp = "2026-10-29"
    prov = (
        "#gen_append\t%s 审计轮 —— 补 %d 条新增路由（★ 无探测观测 ⇒ 裁决按静态分级推导；"
        "既有 %d 行逐字保留，含人工改判）" % (stamp, len(new_rows), len(ledger_rows))
    )

    out_lines = list(header) + [prov]
    for row in merged:
        out_lines.append("\t".join(row))

    tmp_path = ledger_abs + ".refresh.tmp"
    with open(tmp_path, "w", encoding="utf-8") as fh:
        fh.write("\n".join(out_lines) + "\n")
    # ★ 原子替换 ⇒ 中途失败不会留下半个台账；★ 并保留原权限位（★ 仓内产物统一 0644）。
    mode = os.stat(ledger_abs).st_mode & 0o777
    os.chmod(tmp_path, mode)
    os.replace(tmp_path, ledger_abs)
    print("\n✔ 已补进 %d 条 ⇒ %s（%d 行）" % (len(new_rows), LEDGER_REL, len(merged)))
    return 0


def read_rows_from_text(text: str):
    header, rows = [], []
    for raw in text.split("\n"):
        line = raw.rstrip("\r")
        if not line:
            continue
        if line.startswith("#"):
            header.append(line)
        else:
            rows.append(line.split("\t"))
    return header, rows


if __name__ == "__main__":
    sys.exit(main())
