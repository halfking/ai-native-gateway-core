#!/usr/bin/env python3
"""★ 判据「post 段窗口上界」的**穷举**审计（★ 第 25 处口径修正的量具）

★★ **为什么需要它**：★ 第 24 处发现「M11 的 post 段**无界** ⇒ as-of 替换对它无效 ⇒ 负控/预演里的
★★ M11 窗口不是 stage 窗口」。★ 那次是**抽查**发现的（★ 只看了 M11 vs M14 族）
⇒ ★★ ⇒ **同一类缺陷必须穷举**，★ 否则「修完这一条、下一条还在」。

★★★★ **★ 穷举的结果（★ 第 25 处实测）**：★ 除 M11 两块外，还有 **6 处**同类无界窗口：
★★   ① `M5_probe_runs_24h` ★★ **判据**（★ 爆炸半径）
★★   ② `M5_probe_runs_24h_by_kind` ★★ 该判据的「增量归因」分解（★ 必须与①同窗口 ⇒ 否则披露不可复核）
★★   ③ `M5_post_runs_by_day` ★★ 该判据 ❌ 时的「post 逐日」披露
★★   ④ `M12_recheck_`（★ era 型）★ 参考项
★★   ⑤ `M12b_episodes_`（★ era 型）★ 参考项
★★   ⑥ `M12c_episodes_`（★ era 型）★ 参考项
★★   ⑦ `H4_probe_coverage_24h` ★ 参考项（★ handoff 原始判据）

★★★ **判定口径（★ 精确）**：★ 一个「post 段窗口」要能被 as-of 替换**收口成恰好 stage 窗口长度**，
★★★ 就必须**在窗口上界处锚一个 `now()`**（★ as-of 替换会把它换成「新切点 + stage 窗口」）。
★★★ ⇒ ★★ 两种坏法：
★★★   ① **无上界**（★ 窗口 = [下界, ∞)）⇒ ★★ as-of 替换无效 ⇒ ★★ 窗口长度 = 真实漂移量（★ 负控里 ~35h）
★★★   ② **上界锚在别的字面量上**（★ 如 era 切点）⇒ ★★ as-of 替换只改得动下界 ⇒ ★★ 同样不是 stage 窗口

★★★ **★ 判据的「可达性」也要一起问**（★ 第 11 处纪律）：★ 一个窗口型读数若**根本没有 now() 锚定的下界**
★★★ （★ 例如按 era 切点取「切点之后的一切」）⇒ ★★ 它在 as-of 替换下**窗口长度随真实 now 漂移**
★★★ ⇒ ★★ ⇒ 这类读数**只能当描述性读数**，★ 不能进「必须判负」清单。

★ 用法：
    python3 scripts/audit/recovery-detection-p0-3-post-bound-audit.py            # ★ 审计（★ 有违例 ⇒ rc=1）
    python3 scripts/audit/recovery-detection-p0-3-post-bound-audit.py --json     # ★ 机器可读
★ ★★ 本脚本**不连库**（★ 纯静态分析）⇒ ★★ 关窗前可随时跑 ⇒ ★★ 且**变异可验证**（★ 删掉任一上界 ⇒ rc=1）。
"""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
SQL_PATH = ROOT / "scripts/audit/recovery-detection-p0-3-metrics.sql"

# ★ 键字面量：★ SELECT 后面紧跟的字符串常量（★ 可能是前缀，★ 后面可能拼 ||era|| ...）
KEY_RE = re.compile(r"SELECT\s+'([^']*)'", re.IGNORECASE)
# ★ 时间谓词：★ `<列> <比较符> <表达式>`（★ 表达式含 now() / TIMESTAMPTZ 字面量 / CURRENT_*）
TIME_PRED_RE = re.compile(
    r"(?P<col>\b\w+\.(?:created_at|fail_at|next_at|last_attempt_at)\b"
    r"|\b(?:created_at|fail_at|next_at|last_attempt_at)\b)"
    r"\s*(?P<op><=|>=|<|>|=)\s*"
    r"(?P<rhs>now\(\)|TIMESTAMPTZ\s+'[^']+'|CURRENT_[A-Z_]+)",
    re.IGNORECASE,
)
# ★★★ **事件时间列白名单**（★ 关键）：★ 只有这些列的上下界才构成「事件窗口」
#   ★★ ⇒ ★★ 时长型/派生型读数（如 `gap > interval '24h'`、★ `recheck_min`）**不在其列** ⇒ 自动排除
#   ★★ ⇒ ★★ 否则会把 `M4b_free_gap_gt24h`（★ 快照表 + gap 时长，★ 根本不是事件窗口）误报成违例
EVENT_TIME_COLS = ("created_at", "fail_at", "next_at", "last_attempt_at")
# ★ post 段判定：★ 键前缀里的窗口型标记
#   ★★ 注意：★ **不能**把「24h」当 post 标记（★ `M4b_free_gap_gt24h` 的 24h 是**时延阈值**不是窗口长度
#   ★★   ⇒ ★★ 它读的是快照表 `node_probe_state` 的 gap ⇒ ★★ 根本没有事件窗口 ⇒ ★★ 误报）
POST_MARKERS = ("post", "_fix_")
# ★ era 型块：★ 键表达式里拼了 `era`（★ 同一块同时产出 pre 键与 post 键）
ERA_KEY_RE = re.compile(r"\|\|\s*era\b|\bera\s*\|\|", re.IGNORECASE)


def split_statements(sql: str) -> list[str]:
    """★ 按**语句**切分（★ 每条语句 = 一个 WITH + SELECT）

    ★★★ **★ 本轮真踩到的坑（★ 假阴性来源）**：★ 第一版用「**行尾是 `;`**」判断语句结束
    ★★★ ⇒ ★★ 只要有一行写成 `;  -- 注释`（★ 或 `; -- xxx`）⇒ ★★ **该行不被判为语句结束**
    ★★★ ⇒ ★★ 于是**后面若干个读数块被并进同一块** ⇒ ★★ 而并进来的块里**有** `now()` 上界
    ★★★ ⇒ ★★ ⇒ **被删掉上界的那一块被别块「背书」⇒ ★★ 审计器报「全部合规」⇒ ★★ 假阴性**
    ★★★ ⇒ ★★ ⇒ 切分必须**逐字符扫描**：★ 找**字符串外、注释外**的第一个 `;` ⇒ ★ `;` 之后的行尾内容是注释 ⇒ 丢弃
    """
    out: list[str] = []
    buf: list[str] = []
    in_str = False
    for line in sql.splitlines():
        buf.append(line)
        in_comment = False
        for i, ch in enumerate(line):
            if in_comment:
                break
            if ch == "'":
                in_str = not in_str
            elif in_str:
                continue
            elif ch == "-" and line[i:i + 2] == "--":
                in_comment = True
            elif ch == ";":
                out.append("\n".join(buf))
                buf = []
                break
    if buf and "".join(buf).strip():
        out.append("\n".join(buf))
    return out


def strip_comments(stmt: str) -> str:
    return "\n".join(l for l in stmt.splitlines() if not l.strip().startswith("--"))


def analyze(stmt: str) -> dict:
    code = strip_comments(stmt)
    keys = KEY_RE.findall(code)
    if not keys:
        return {}
    preds = []
    for m in TIME_PRED_RE.finditer(code):
        rhs = m.group("rhs")
        preds.append({
            "col": m.group("col"), "op": m.group("op"), "rhs": rhs.strip(),
            "now": "now()" in rhs.lower(),
            "literal": rhs.upper().startswith("TIMESTAMPTZ"),
        })
    uppers = [p for p in preds if p["op"] in ("<", "<=")]
    lowers = [p for p in preds if p["op"] in (">", ">=")]
    key_expr = " ".join(keys)
    era_block = bool(ERA_KEY_RE.search(code))
    post_by_marker = any(m in key_expr.lower() for m in POST_MARKERS)
    post_by_era = era_block and ("'post'" in code or "'post_fix'" in code)
    return {
        "keys": keys,
        "preds": preds,
        "has_now_upper": any(p["now"] for p in uppers),
        "has_now_lower": any(p["now"] for p in lowers),
        "post": post_by_marker or post_by_era,
        "era_block": era_block,
    }


def audit(sql: str) -> tuple[list[dict], list[dict]]:
    rows, violations = [], []
    for stmt in split_statements(sql):
        info = analyze(stmt)
        if not info:
            continue
        row = {
            "prefix": info["keys"][0], "post": info["post"], "era_block": info["era_block"],
            "now_upper": info["has_now_upper"], "now_lower": info["has_now_lower"],
            "preds": [(p["col"], p["op"], p["rhs"]) for p in info["preds"]],
        }
        rows.append(row)
        # ★★ **窗口型读数的判定（★ 两个条件满足其一）**：
        #   ① post/fix 键（★ era 型块也算）⇒ ★★ 它读的是「切点之后」那一段
        #   ② 有 **now() 锚定的下界**（★ 「最近 N 小时」型）⇒ ★★ 它的窗口长度由 now() 决定
        #   ★★ 两者都要求 **上界也锚在 now()**（★ 否则 as-of 替换只改得动下界 ⇒ 窗口长度漂移）
        windowed = info["post"] or info["has_now_lower"]
        row["windowed"] = windowed
        if windowed and not info["has_now_upper"]:
            violations.append(row)
    return rows, violations


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--sql", default=str(SQL_PATH))
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()

    sql = pathlib.Path(args.sql).read_text(encoding="utf-8")
    rows, violations = audit(sql)

    if args.json:
        print(json.dumps({"blocks": rows, "violations": violations}, ensure_ascii=False, indent=1))
        return 1 if violations else 0

    print("== 判据「post 段窗口上界」穷举审计 ==")
    print(f"   读数块总数：{len(rows)}　★ 其中 post/窗口型：{sum(1 for r in rows if r.get('windowed'))}")
    print()
    for r in rows:
        if not r.get("windowed"):
            continue
        mark = "✔" if r["now_upper"] else "★★"
        print(f"   {mark} {r['prefix']}{'  (era 型)' if r['era_block'] else ''}")
        for col, op, rhs in r["preds"]:
            print(f"          · {col} {op} {rhs}")
    print()
    if violations:
        print(f"★★ **发现 {len(violations)} 个 post/窗口型读数没有 now() 锚定的上界** ⇒ ★★ as-of 替换对它们无效")
        for v in violations:
            print(f"   ❌ {v['prefix']} ⇒ 窗口上界不是 now() ⇒ ★★ 负控/预演里窗口长度 = 真实漂移量")
        return 1
    print("   ✔ 全部 post/窗口型读数都有 now() 锚定的上界")
    print("   ⇒ ★★ as-of 替换能把它们**收口成恰好 stage 窗口**（★ 24h / 168h）⇒ ★★ 负控与预演几何一致")
    return 0


if __name__ == "__main__":
    sys.exit(main())
