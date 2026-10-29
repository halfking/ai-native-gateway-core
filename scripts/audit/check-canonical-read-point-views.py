#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""check-canonical-read-point-views.py —— R179 轮：【人读视图】的漂移校验。

# 这个工具解决什么

R179 轮把 canonical 读点的形态册落成三份产物：

  - scripts/audit/asset/canonical-read-point-shapes.tsv   （人读：形态册）
  - scripts/audit/asset/canonical-read-points.tsv         （人读：逐读点清单）
  - scripts/audit/asset/canonical-read-point-shapes.json  （机器读：实测工具的输入）

★★ 本轮发现的缺陷：★★ 那两份 .tsv 是【人读产物】⇒ ⇒ **没有任何代码读它们**
★★ ⇒ ⇒ 它们【漂了也没人知道】⇒ ⇒ 而它们又是审计档引用的证据
★★ ⇒ ⇒ 登记成「例外（无代码读方）」是**掩盖**，不是**保护**。
★★ ⇒ ⇒ 而本轮慢门实测：★★ 「例外行」的机械段必须写**已知的机械断言**
★★ ⇒ ⇒ 写中文理由 ⇒ ⇒ 门红 ⇒ ⇒ 门把「我没把话说成可核的形式」抓了出来。

★★ 修法：★★ 给它们一个**真读方** —— 就是本工具：
  ① 从**已入库的 .json** 里读出【来源快照】（source_files）⇒ ⇒ 这是**产物自证来源**；
  ② 按该快照**重放**提取器 ⇒ ⇒ 得到「按定义应当长什么样」的产物；
  ③ 与已入库的三份产物**逐字比对** ⇒ ⇒ 任何手改/漂移 ⇒ ⇒ 红。

★ 为什么重放要按【快照】而不是【当前登记簿】：
  ★ 登记簿会随上游持续增长 ⇒ 若按当前登记簿重放 ⇒ 上游每加一个读点，
  ★ 本门就会红 ⇒ ⇒ 主干长期带红 ⇒ ⇒ 那就是**假红**（★ 假红比没有门更坏）。
  ★ ⇒ 快照让「产物内部一致」成为**恒成立**的判据；
  ★ 而「登记簿比快照新」这件事，本工具**另外报出来**（★ 缺口，不静默）。

# 判据

  □ **漂移**（硬闸）：重放产物 ≠ 已入库产物 ⇒ ⇒ 红（★ 逐字比对，不是「看着像」）。
  □ **事实源缺失**（硬闸）：已入库的 .json 读不出来/没有 source_files ⇒ ⇒ 红
    （★ 这份 .json 一旦丢了，产物就失去自证来源 ⇒ ⇒ 必须红，不能降级）。
  □ **登记簿缺口**（★ 默认只报不红）：当前登记簿列出的文件 ≠ 快照里的文件
    ⇒ ⇒ 报出差集 ⇒ ⇒ 「形态册还没覆盖上游新增的读点」。
    ⇒ ★ 用 `URSM_CANONICAL_VIEWS_STRICT_REGISTRY=1` 可把它升级成硬闸
    ⇒ ⇒ （★ 下一轮把它设成默认 ⇒ 届时缺口必须先补齐再推）。

# 用法

    python3 scripts/audit/check-canonical-read-point-views.py
    echo $?   # 0 = 绿；1 = 红
"""

from __future__ import annotations

import difflib
import json
import os
import subprocess
import sys
import tempfile

REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))
ASSET_DIR = os.path.join(REPO_ROOT, "scripts", "audit", "asset")

SHAPES_TSV = "canonical-read-point-shapes.tsv"
POINTS_TSV = "canonical-read-points.tsv"
SHAPES_JSON = "canonical-read-point-shapes.json"

EXTRACTOR = "scripts/audit/extract-canonical-read-point-shapes.py"


def fail(msg: str) -> None:
    print("★ 红：%s" % msg)
    sys.exit(1)


def read_bytes(path: str) -> bytes:
    if not os.path.exists(path):
        fail("产物不存在：%s（★ 事实源缺失 ⇒ 必须红）" % path)
    with open(path, "rb") as fh:
        return fh.read()


def diff_text(name: str, want: bytes, got: bytes) -> str:
    """★ 逐字比对 ⇒ 给出可读的差异（前 40 行）。"""
    a = want.decode("utf-8", "replace").splitlines()
    b = got.decode("utf-8", "replace").splitlines()
    out = list(difflib.unified_diff(a, b, fromfile="已入库/" + name, tofile="重放/" + name, lineterm=""))
    if not out:
        return ""
    return "\n".join(out[:40])


def main() -> int:
    # ① 事实源：已入库的 .json ⇒ 它带 source_files（★ 产物自证来源）
    json_path = os.path.join(ASSET_DIR, SHAPES_JSON)
    if not os.path.exists(json_path):
        fail("机器读方的输入不存在：%s" % SHAPES_JSON)
    try:
        with open(json_path, encoding="utf-8") as fh:
            meta = json.load(fh)
    except Exception as exc:  # noqa: BLE001
        fail("读 %s 失败：%s（★ 事实源读不出来 ⇒ 必须红）" % (SHAPES_JSON, exc))
    source_files = meta.get("source_files")
    if not isinstance(source_files, list) or not source_files:
        fail("%s 里没有 source_files（★ 产物没有自证来源 ⇒ 重放无从谈起 ⇒ 必须红）" % SHAPES_JSON)

    # ② 按快照重放提取器
    with tempfile.TemporaryDirectory() as tmp:
        list_path = os.path.join(tmp, "files.txt")
        with open(list_path, "w", encoding="utf-8") as fh:
            for f in source_files:
                fh.write(f + "\n")
        out_shapes = os.path.join(tmp, SHAPES_TSV)
        out_points = os.path.join(tmp, POINTS_TSV)
        out_json = os.path.join(tmp, SHAPES_JSON)
        proc = subprocess.run(
            [
                sys.executable,
                os.path.join(REPO_ROOT, EXTRACTOR),
                "--repo-root", REPO_ROOT,
                "--file-list", list_path,
                "--out-shapes", out_shapes,
                "--out-points", out_points,
                "--json", out_json,
            ],
            cwd=REPO_ROOT,
            capture_output=True,
            text=True,
        )
        if proc.returncode != 0:
            fail("重放提取器失败（exit=%d）：\n%s\n%s" % (proc.returncode, proc.stdout[-2000:], proc.stderr[-2000:]))

        # ③ 逐字比对三份产物
        drifted = []
        for name, replay in (
            (SHAPES_TSV, out_shapes),
            (POINTS_TSV, out_points),
            (SHAPES_JSON, out_json),
        ):
            want = read_bytes(os.path.join(ASSET_DIR, name))
            got = read_bytes(replay)
            if want != got:
                drifted.append((name, diff_text(name, want, got)))
        if drifted:
            for name, d in drifted:
                print("=== 漂移：%s ===" % name)
                print(d)
            fail("%d 份产物与【按定义应当长什么样】不一致 ⇒ ⇒ 人读视图已漂移" % len(drifted))

    # ④ 登记簿缺口（★ 默认只报不红）
    strict = os.environ.get("URSM_CANONICAL_VIEWS_STRICT_REGISTRY", "") == "1"
    live = None
    try:
        # ★ 模块名带连字符 ⇒ 不能用 import ⇒ 走 importlib（★ 与 build-plan-form-spec.py 同款）。
        import importlib.util

        spec = importlib.util.spec_from_file_location(
            "shapes_mod",
            os.path.join(REPO_ROOT, "scripts", "audit", "extract-canonical-read-point-shapes.py"),
        )
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
        live = mod.load_affected_files(REPO_ROOT)
    except Exception as exc:  # noqa: BLE001
        print("★ 提示：读不到当前登记簿（%s）⇒ ⇒ 缺口检查本轮跳过" % exc)
    if live is not None:
        snapshot = set(source_files)
        current = set(live)
        missing = sorted(current - snapshot)   # ★ 登记簿有、快照没有 ⇒ 形态册还没覆盖
        stale = sorted(snapshot - current)     # ★ 快照有、登记簿没有 ⇒ 登记簿删了文件
        if missing or stale:
            print("★ 登记簿缺口（★ 默认只报不红）：")
            if missing:
                print("  · 登记簿新增、形态册未覆盖（%d 个）：%s" % (len(missing), ", ".join(missing)))
            if stale:
                print("  · 形态册有、登记簿已无（%d 个）：%s" % (len(stale), ", ".join(stale)))
            print("  ⇒ 下一轮要重生成形态册 + 重测 + 重算台账（★ 或用 "
                  "URSM_CANONICAL_VIEWS_STRICT_REGISTRY=1 把本项升级成硬闸）")
            if strict:
                fail("登记簿与形态册快照不一致（strict 模式 ⇒ 硬闸）")
        else:
            print("★ 登记簿与形态册快照一致（%d 个文件）" % len(current))

    print("✅ 人读视图与机器真值同源、无漂移（%d 个来源文件）" % len(source_files))
    return 0


if __name__ == "__main__":
    sys.exit(main())
