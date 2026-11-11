#!/usr/bin/env python3
"""登记项**计数守恒**核对（★ 2026-11-11 第二轮审计自审时抓到 ⇒ 机械化）。

★ 触发：★★ 本轮写审计档时**自审**发现一处计数不守恒 ——
★★ 第一轮把「17 项」写在 §六 的**表格行数**上（★ 该表恰有 17 行，★ 但 **B-5 未单列**
★★ ⇒ ★★ 行数与**事项项数**恰好相同 ★★ ⇒ ★★ 属**巧合**、★ 不可互推）★★
★★ ⇒ ★★ 本轮初稿沿用了那个口径 ⇒ ★★ 写成「实施了 **7** 项」★★ ⇒ ★★ 17 − 8 = **9**
★★ ⇒ ★★ **算术不守恒**（★ 7 + 9 + 3 = 19 ≠ 17）★★ ⇒ ★★ **当场改正**。

★★★ **通用式**：★★★ 「表格行数」与「事项项数」在**含合并行**的表里**不必相等**
★★★ ⇒ ★★★ 引用计数必须写明**取自哪一个口径** ⇒ ★★★ 且**必须能被机器核**。

★ 门做什么：★★★ 解析三份文档里的编号集合 ⇒ ★★★ 核对**互斥 + 并集 + 守恒**
★★★   ⇒ ★★★ 任一不成立 ⇒ **rc≠0** ⇒ ★★★ **fail-closed**（★ 读不到文档 ⇒ 也 rc≠0）。

★ 用法：★ `python3 scripts/audit/registered-items-balance-2026-11-11.py`
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]

# ── 三份文档的路径（★ 相对仓库根）──────────────────────────────────────────
ROUND1 = REPO / "docs/12小时内修订审计-2026-11-11-0707.md"
ROUND2 = REPO / "docs/12小时内修订审计-2026-11-11-第二轮-登记项实施.md"

# ── 编号形态：★ A-2 / B-7 / C-1 / R-3（★ M-x 是另一簇，★ 不参与本核对）──
ITEM_RE = re.compile(r"\b([ABCR])-(\d+)\b")


def read(path: Path) -> str:
    if not path.is_file():
        print(f"FAIL 读不到文档：{path}")
        sys.exit(1)
    return path.read_text(encoding="utf-8")


def ids_in(text: str) -> set[str]:
    """抽出编号集合（★ 去重、★ 保持 'A-2' 形态）。"""
    return {f"{m.group(1)}-{m.group(2)}" for m in ITEM_RE.finditer(text)}


def table_row_ids(section: str) -> set[str]:
    """只从**表格行**里取编号（★ 结构化，★ 不含散文提及）。

    ★ 为什么必须这样：★ 散文里**提及**一个编号（★ 「C-5 的量化已被推翻」）
    ★ ≠ ★ 它是**清单里的一项** ⇒ ★★ 按全文 grep 会把两者混起来（★ 本脚本初版就踩了）
    ★★ ⇒ ★★ **通用式：★★★ 核对「清单」必须按**结构**取（★ 表格行 / 括号内枚举），
    ★★★ 不可按全文关键词（★ 同「守卫的判据必须是结构而不是关键词」★★）。
    """
    out: set[str] = set()
    for line in section.splitlines():
        stripped = line.strip()
        #表格行 = 以 '|' 开头 ⇒ 表头行与分隔行（★ '|---|'）都不含编号 ⇒ 自然排除
        if not stripped.startswith("|"):
            continue
        out |= ids_in(stripped)
    return out


def listed_ids(text: str) -> set[str]:
    """从「登记项（…）本轮未实施」那条清单的**括号内**取编号。

    ★ 必须带「本轮未实施」这个限定：★ 文档里**别处**也有「登记项（★ §三/§四 …）」
    ★ 形态 ⇒ ★★ 不加限定会**匹配到错的那一处**⇒ ★★ 取回空集（★ 本脚本初版就踩了）
    ★★ ⇒ ★★ **通用式：★★★定位锚点必须**足够具体**，★ 「有若干处同名结构」时
    ★★★ 限定条件本身就是**锚点的一部分**（★ 同「约束名锚定」那类教训）★★。
    """
    m = re.search(r"登记项（([^）]*)）本轮未实施", text)
    if not m:
        return set()
    return ids_in(m.group(1))


def section(text: str, start_marker: str, end_marker: str | None) -> str:
    """截取一节（★ 找不到起始标记 ⇒ fail-closed）。"""
    i = text.find(start_marker)
    if i < 0:
        print(f"FAIL 找不到章节起始标记：{start_marker!r}")
        sys.exit(1)
    j = text.find(end_marker, i + 1) if end_marker else -1
    return text[i:] if j < 0 else text[i:j]


def main() -> int:
    r1 = read(ROUND1)
    r2 = read(ROUND2)

    # ── ① 第一轮的登记项全集：★ 取自 §七 末尾那条清单（★ 不是 §六 的表格行数）──
    #   形态：★ 「登记项（A-2/A-3/.../R-3）本轮未实施」
    registered = listed_ids(r1)
    if not registered:
        print("FAIL 第一轮文档里找不到「登记项（…）本轮未实施」那条清单")
        return 1

    # ── ② 本轮已实施：★ 取自第二轮 §一 的那张**表**（★ 只取表格行）─────────
    sec1 = section(r2, "## 一、本轮范围与结论", "## 二、")
    implemented = table_row_ids(sec1)

    # ── ③ 本轮仍开口：★ 取自第二轮 §八 的**表**（★ 只取表格行）──────────────
    sec8 = section(r2, "## 八、", "## 九、")
    still_open = table_row_ids(sec8)

    # ★ (b) 类低危项：★ 第一轮 §五 三路复核里标 (b) 的 A-6/A-7/A-8 ⇒ ★★ 不在登记全集里
    extra = {i for i in ("A-6", "A-7", "A-8") if i in r1}

    # ── ③b §八 的**散文**部分（★ 表格之外）里提及的编号：★ 必须是「已实施」
    #       或「(b) 类」⇒ ★★ 否则等于「表格里没列、散文里偷偷留了一项」★★
    prose_ids = ids_in("\n".join(
        line for line in sec8.splitlines() if not line.strip().startswith("|")
    ))
    stray = prose_ids - implemented - extra
    if stray:
        problems.append(f"§八 的散文里出现了既未实施、也不在 (b) 类里的编号 ⇒ {sorted(stray)}")

    # ── ④ 守恒核对 ─────────────────────────────────────────────────────────
    problems: list[str] = []

    overlap = implemented & still_open
    if overlap:
        problems.append(f"互斥失败：既称已实施又列开口 ⇒ {sorted(overlap)}")

    missing = registered - (implemented | still_open)
    if missing:
        problems.append(f"登记全集未被分完（★ 既没实施也没列开口）⇒ {sorted(missing)}")

    extra_in_open = still_open - registered - extra
    if extra_in_open:
        problems.append(f"§八 列了不在全集里的编号 ⇒ {sorted(extra_in_open)}")

    # ★★★ **反向**：★ 实施集里**不得**混入登记全集之外的编号 ★★★
    # ★★★ 「只比数量 ⇒ 守恒成立」**推不出**「集合相同」⇒ ★★★ 8 + 9 = 17 在
    # ★★★ 「把实施集里的 R-3 换成 A-11」时**依然成立**⇒ ★★★ 必须比**集合** ★★★
    # ★★★ （★ 同「键数相同 ≠ 键集相同」那类教训）★★★
    not_registered = implemented - registered
    if not_registered:
        problems.append(
            f"§一 的实施集里混入了不在第一轮登记全集里的编号 ⇒ {sorted(not_registered)}"
        )

    total = len(registered)
    if total != 17:
        problems.append(f"第一轮登记全集应是17 项，实际 {total} 项 ⇒口径已漂")

    if len(implemented) != 8:
        problems.append(f"本轮实施应是 8 项，实际 {len(implemented)} 项 ⇒ {sorted(implemented)}")

    if len(still_open - extra) != 9:
        problems.append(
            f"§八 的登记项开口应是 9 项，实际 {len(still_open - extra)} 项 ⇒ "
            f"{sorted(still_open - extra)}"
        )

    # ── ⑤ 计数守恒等式：★ 实施 + 开口 = 全集 ───────────────────────────────
    if len(implemented) + len(still_open - extra) != total:
        problems.append(
            f"守恒失败：{len(implemented)} + {len(still_open - extra)} ≠ {total}"
        )

    if problems:
        print("FAIL 登记项计数守恒核对未通过：")
        for p in problems:
            print(f"  · {p}")
        return 1

    print(f"OK  第一轮登记全集 = {total} 项（★ 口径：§七 清单，★ 非 §六 表格行数）")
    print(f"OK  本轮实施 = {len(implemented)} 项 ⇒ {sorted(implemented)}")
    print(f"OK  仍开口（登记项）= {len(still_open - extra)} 项 ⇒ {sorted(still_open - extra)}")
    print(f"OK  另列 (b) 类低危 = {len(extra)} 项 ⇒ {sorted(extra)}")
    print(f"OK  守恒等式：{len(implemented)} + {len(still_open - extra)} = {total} ✔")
    return 0


if __name__ == "__main__":
    sys.exit(main())
