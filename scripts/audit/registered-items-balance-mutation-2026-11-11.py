#!/usr/bin/env python3
"""登记项**计数守恒门**的变异验证（★ 验证它真的**有牙**）。

★ 被验对象：★ `scripts/audit/registered-items-balance-2026-11-11.py`
★ ★ （★ 守恒门：★ 第一轮登记17 项 ⇒ ★ 本轮实施 8 项 + 仍开口 9 项 = 17 ★★）。

★★★ **本 harness 自身的两条纪律**（★ 都是本轮真踩到的）：
★★★ ① ★★ **备份必须取在「补丁之后」** ⇒ ★★ 否则还原会把**补丁本身**一起冲掉
★★★    ⇒ ★★ 现场变成「补丁没上」⇒ ★★ 后续步骤**静默验错对象** ⇒ ★★ 这里在**执行时点**
★★★    才取快照（★ 并在每臂后**按 SHA 复核**还原结果 ★★）。
★★★ ② ★★ **「变异生效」必须有独立证据** ⇒ ★★ 否则「门没牙」与「变异根本没生效」
★★★    **分不清**（★ 本轮就有三个臂因为 grep 标记串写错而**误判为无效臂** ★★）
★★★    ⇒ ★★ 这里用**文件 SHA 发生变化**作为生效判据 ★★。

★ 用法：★ `python3 scripts/audit/registered-items-balance-mutation-2026-11-11.py`
★ 退出码：★ 0 = 全部臂被抓住；★ 1 = 有臂漏过或脚本自身出错。
"""

from __future__ import annotations

import hashlib
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]

ROUND1 = REPO / "docs/12小时内修订审计-2026-11-11-0707.md"
ROUND2 = REPO / "docs/12小时内修订审计-2026-11-11-第二轮-登记项实施.md"
GATE = REPO / "scripts/audit/registered-items-balance-2026-11-11.py"


def sha(p: Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


def run_gate() -> tuple[int, str]:
    proc = subprocess.run(
        [sys.executable, str(GATE)], capture_output=True, text=True, cwd=str(REPO)
    )
    return proc.returncode, proc.stdout + proc.stderr


def main() -> int:
    for p in (ROUND1, ROUND2, GATE):
        if not p.is_file():
            print(f"FAIL 读不到必需文件：{p}")
            return 1

    workdir = Path(tempfile.mkdtemp(prefix="balance-mut-"))
    backup = {p: workdir / p.name for p in (ROUND1, ROUND2)}
    for p, b in backup.items():
        shutil.copy2(p, b)
    base_sha = {p: sha(p) for p in (ROUND1, ROUND2)}

    # ★★ 每条臂：★ (名称, 期望命中正则, 变异函数, 变异生效的判据函数) ★★
    def rm_r2_row(t: str) -> str:
        return re.sub(r"^\| 9 \| \*\*R-2\*\*.*\n", "", t, flags=re.M)

    def rm_r3_row(t: str) -> str:
        return re.sub(r"^\| \*\*R-3\*\* \|.*\n", "", t, flags=re.M)

    def swap_r3_for_a11(t: str) -> str:
        return t.replace(
            "| **R-3** | P2-K 测试注释的闭集枚举过时",
            "| **A-11** | P2-K 测试注释的闭集枚举过时",
            1,
        )

    def add_a6_to_r1(t: str) -> str:
        return t.replace(
            "C-1/C-2/C-3/C-5/R-1/R-2/R-3）本轮未实施",
            "C-1/C-2/C-3/C-5/R-1/R-2/R-3/A-6）本轮未实施",
            1,
        )

    def break_section_title(t: str) -> str:
        return t.replace(
            "## 八、★ 仍需属主裁决 / 仍开口的登记项", "## 八改、★ 仍需属主裁决", 1
        )

    def stray_in_prose(t: str) -> str:
        return t.replace(
            "## 九、★ 关联", "★ 顺带一提 R-9 也值得看。\n\n## 九、★ 关联", 1
        )

    def stray_in_table(t: str) -> str:
        return t.replace(
            "| 10 | **A-6 / A-7 / A-8** |", "| 10 | **A-6 / A-7 / A-8 / R-9** |", 1
        )

    arms = [
        (
            "M1：§八 表里删掉 R-2 一行",
            r"未被分完|守恒失败|开口应是",
            ROUND2,
            rm_r2_row,
        ),
        (
            "M2：§一 表里删掉 R-3 一行",
            r"未被分完",
            ROUND2,
            rm_r3_row,
        ),
        (
            "M3：§一 表里把 R-3 换成不存在的 A-11（★ 数量守恒仍成立 ⇒ 只能靠集合比）",
            r"混入了不在第一轮登记全集里的编号",
            ROUND2,
            swap_r3_for_a11,
        ),
        (
            "M4：第一轮清单括号里多加一项 A-6（★ 全集口径漂移）",
            r"口径已漂|守恒失败|应是 ?17",
            ROUND1,
            add_a6_to_r1,
        ),
        (
            "M5：第二轮 §八 章节标题被改（★ fail-closed）",
            r"找不到章节",
            ROUND2,
            break_section_title,
        ),
        (
            "M6：§八 散文里偷偷塞一个未列的编号 R-9",
            r"散文里出现了",
            ROUND2,
            stray_in_prose,
        ),
        (
            "M7：§八 表里塞一个不在全集里的编号 R-9",
            r"不在全集里的编号",
            ROUND2,
            stray_in_table,
        ),
    ]

    passed = 0
    failed = 0

    def restore() -> None:
        for p, b in backup.items():
            shutil.copy2(b, p)

    try:
        # ── 正向基线：★ 门必须是绿的，否则后面的「红」没有意义 ★──────────────
        rc, out = run_gate()
        if rc != 0:
            print("FAIL 守恒门在**未变异**状态下就不是绿的 ⇒ 本次变异验证无意义：")
            print(out)
            return 1
        print("OK  基线：守恒门 rc=0 ✔")

        # ── M8：★ 文档整体缺失 ⇒ fail-closed（★ 单独跑，★ 因为它破坏文件本身）──
        print("\n── M8：第一轮文档整体缺失（★ fail-closed）──")
        hidden = workdir / "hidden-round1.md"
        shutil.move(str(ROUND1), str(hidden))
        try:
            rc, out = run_gate()
            if rc != 0 and "读不到文档" in out:
                print("  被抓 ✔（rc=1，定位命中 '读不到文档'）")
                passed += 1
            else:
                print(f"  ✘ 漏过（rc={rc}）★ 门没牙")
                failed += 1
        finally:
            shutil.move(str(hidden), str(ROUND1))
            restore()

        for name, expect, target, mutate in arms:
            print(f"\n── {name} ──")
            restore()
            original = target.read_text(encoding="utf-8")
            mutated = mutate(original)
            if mutated == original:
                print("  ⚠ 变异函数没有改变文件 ⇒ ★ 本臂**无效**（★ 不是门没牙）")
                failed += 1
                restore()
                continue
            target.write_text(mutated, encoding="utf-8")

            # ★★ 变异生效的独立证据：★ SHA 变化 ★★
            if sha(target) == base_sha[target]:
                print("  ⚠ 变异未生效（★ SHA 未变）⇒ ★ 本臂**无效**")
                failed += 1
                restore()
                continue

            rc, out = run_gate()
            if rc != 0:
                if re.search(expect, out):
                    print(f"  被抓 ✔（rc={rc}，定位命中 '{expect}'）")
                    passed += 1
                else:
                    print(f"  被抓 ✔ 但定位不准（rc={rc}）：")
                    print("    " + out.replace("\n", "\n    "))
                    passed += 1
            else:
                print(f"  ✘ 漏过（rc=0）★ 门没牙")
                print("    " + out.replace("\n", "\n    "))
                failed += 1

            # ★ 还原 + SHA 复核 + 正向复跑（三件套齐全才算还原成功）★ ─────────
            restore()
            for p in (ROUND1, ROUND2):
                if sha(p) != base_sha[p]:
                    print(f"  ✘ 还原后 SHA 不一致：{p.name}")
                    failed += 1
            rc, _ = run_gate()
            if rc == 0:
                print("  还原后 SHA 复核：一致 ✔  还原后正向复跑：rc=0 ✔")
            else:
                print(f"  ✘ 还原后正向复跑 rc={rc}")
                failed += 1
    finally:
        restore()
        shutil.rmtree(workdir, ignore_errors=True)

    print("\n── 汇总 ──")
    print(f"PASS={passed} FAIL={failed}")
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
