#!/usr/bin/env python3
"""★ 判据「post 段窗口上界」的**穷举**审计（★ 第 25 处口径修正的量具 · ★ 第 27 处升级为 v2）

★★★ **v1 的问题（★ 第 27 处实测，★ 三个真缺口）**：
★★★ ① ★★ **主判据块对审计器完全不可见**：★ v1 的键提取 `KEY_RE = SELECT\\s+'([^']*)'`
★★★    只认「SELECT 后面**紧跟**字面量」的键 ⇒ ★ 而 **M14/M14x 块的键是 CASE 表达式**
★★★    （`SELECT CASE WHEN scope='all' THEN 'M14x_' ELSE 'M14_' END || win || '_' || grp_new k`）
★★★    ⇒ ★★ `KEY_RE` 抓到 **0 个键** ⇒ ★★ **整个 M14/M14x 块（★ 11413 字符 · 主判据）从未被检查过**
★★★    ⇒ ★★ v1 报「全部合规」时，★ **它对 M14/M14x 的覆盖率是 0%**。
★★★ ② ★★ **post 判定靠键名前缀**（`'post' in key`）⇒ ★ 键由 `era`/`win` 列拼出来的块
★★★    （★ M15x / M12p）被判 `post=False` ⇒ ★★ **在列表里却被排除在违例检查之外**
★★★    ⇒ ★★ 删掉它们的 `now()` 上界 ⇒ ★★ 审计器**不会报警**（★ 潜在假阴性）。
★★★ ③ ★★ **采集侧真缺口（★ v1 从未看见 ⇒ 因为它看不见那些块）**：
★★★    ★ `M3_feat_*` 的 `runs` CTE **只有下界**（★ `n.created_at > CUT - 24h`）
★★★      ⇒ ★★ 窗口 = **[CUT-24h, ∞)** ⇒ ★★ 开区间 ⇒ ★★ as-of 替换只改得动下界
★★★      ⇒ ★★ 且它的注释**宣称**「post 段为切点→now」⇒ ★★ **注释与实现不符**
★★★    ★ `M16_join_caliber_check` / `M17_event_definition_check` 同样只有下界
★★★      （★ 两者是**同一集合内的口径自检** ⇒ ★ 等式判定仍成立 ⇒ ★ 属「观察面」）

★★★ **v2 的三条设计原则（★ 全部来自上面的实测）**：
★★★ ① ★★ **键提取必须能读懂 CASE 表达式**（★ 不再依赖「SELECT 后紧跟字面量」）
★★★ ② ★★ **post 段判定必须结构化**（★ 键里带 post 标记 / 键引用 `era`·`win` 且块内有 `'post'` 取值
★★★    / 时间轴在切点处被显式分桶（`col >= CUT`）或 CASE 切分（`< CUT THEN 'pre' ELSE 'post'`））
★★★    ⇒ ★★ **不能靠「键名里有没有 post」**（★ 那是**关键词判据** ⇒ ★ 第 25 处已吃过假阳性的亏）
★★★ ③ ★★★★ **审计器必须报告自己的覆盖率**（★ 「检查了哪些块」必须可复核）
★★★    ⇒ ★★ 否则「审计器自己瞎了」也会静默通过（★ 同族：★ 静默失效）

★★★ **v2 的两条规则（★ 判据是结构，不是关键词）**：
★★★ ★ **规则 A（post 段规则）**：★ 块若产出 post 段读数 ⇒ ★ post 段必须有**作用于 post 侧**的
★★★   `now()` 上界（★ 判定方式：★ 该谓词落在 ① CTE 体内（全局）② 含 `'post'` 的 OR 分支里
★★★   ③ 既不含 `'pre'` 也不含 `'post'` 的 OR 分支里（全局过滤）⇒ ★ 三者之一）。
★★★ ★ **规则 B（事件窗口闭合规则）**：★ 块若读**事件表**（`node_probe_runs` / `model_probe_runs`）
★★★   ⇒ ★ 事件时间窗必须有上界（★ `op ∈ {<, <=}`），★ 且该上界锚在 `now()` 或**切点或更早**的字面量上
★★★   ⇒ ★★ 锚在 `now()` ⇒ as-of 可替换；★ 锚在切点或更早 ⇒ 闭合历史窗（★ 也合法）。

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

# ★★★ **事件时间列白名单**（★ 第 25 处已确立）：★ 只有这些列的上下界才构成「事件窗口」
#   ★★ ⇒ ★★ 时长型/派生型读数（如 `gap > interval '24h'`、★ `recheck_min`）**不在其列** ⇒ 自动排除
EVENT_TIME_COLS = ("created_at", "fail_at", "next_at", "last_attempt_at")
# ★ post 段判定：★ 键前缀里的窗口型标记（★ **只作为一路** ⇒ ★ 主判据靠结构化三路）
POST_MARKERS = ("post", "_fix_")
# ★ 窗口列名（★ 采集侧约定：★ `era` = 切点前后；★ `win` = 四桶窗口标签）
WINDOW_COLS = ("era", "win")
# ★ 事件表（★ 读它们的块才受「规则 B」约束）
EVENT_TABLES = ("node_probe_runs", "model_probe_runs")

# ★ 时间谓词：★ `<列> <比较符> <表达式>`（★ 表达式含 now() / TIMESTAMPTZ 字面量 / CURRENT_*）
#   ★★ v2 变化：★ rhs **允许带 interval 偏移**（★ v1 只吃到 `now()` ⇒ 丢了 `now() - interval '6 hours'` 的语义）
TIME_PRED_RE = re.compile(
    r"(?P<col>\b\w+\.(?:created_at|fail_at|next_at|last_attempt_at)\b"
    r"|\b(?:created_at|fail_at|next_at|last_attempt_at)\b)"
    r"\s*(?P<op><=|>=|<|>|=)\s*"
    r"(?P<rhs>now\(\)(?:\s*[-+]\s*interval\s*'[^']+')?"
    r"|TIMESTAMPTZ\s+'[^']+'(?:\s*[-+]\s*interval\s*'[^']+')?"
    r"|CURRENT_[A-Z_]+)",
    re.IGNORECASE,
)
# ★ CTE 定义：★ `<名字> AS (` （★ 必须是**顶层**（★ 深度 0）⇒ ★ WHERE 里的括号不算）
CTE_DEF_RE = re.compile(r"\b(\w+)\s+AS\s*\(", re.IGNORECASE)
# ★ 顶层 SELECT（★ 深度 0）
SELECT_RE = re.compile(r"\bSELECT\b", re.IGNORECASE)
# ★ 顶层 FROM / 逗号（★ 用于切首个输出表达式）
FROM_RE = re.compile(r"\bFROM\b", re.IGNORECASE)
# ★ OR 关键字（★ 单词边界 ⇒ ★ 不会命中 ORDER / ORDINARY）
OR_RE = re.compile(r"\bOR\b", re.IGNORECASE)
# ★ CASE / END 关键字（★ v2：★ 用来识别「CASE 内的谓词」）
CASE_KW_RE = re.compile(r"\bCASE\b", re.IGNORECASE)
END_KW_RE = re.compile(r"\bEND\b", re.IGNORECASE)
# ★ CASE 切分：★ `<col> < CUT THEN 'pre...' ELSE 'post...' END`（★ 时间轴在切点处被切开）
CASE_SPLIT_RE = re.compile(
    r"\b(?P<col>\w+\.)?(?P<col2>created_at|fail_at|next_at|last_attempt_at)\b"
    r"\s*<\s*TIMESTAMPTZ\s+'[^']+'\s+THEN\s+'pre[a-z0-9_]*'\s+ELSE\s+'post[a-z0-9_]*'\s+END",
    re.IGNORECASE | re.DOTALL,
)
# ★★★ **era / win 取值词表（★ 真实取值，★ 不许只认 'post'）**：
# ★★★   实测（★ 本轮）：★ 采集侧的 era 取值有 **'post_fix'**（★ M11 族）⇒ ★★★
# ★★★   只认精确 `'post'` ⇒ ★★★ **`'post_fix'` 里不含 `'post'`（★ 后面是 `_` 不是引号）**
# ★★★   ⇒ ★★★ M11 被判成「非 post 块」⇒ ★★★ 它的 post 段**从未被检查**（★ 假阴性）
# ★★★ ★★ 且键里的 post 标记**不总是以 post 开头**：★ `'_post_split'` / `'M20_normal_post_fp_swing'`
# ★★★   ⇒ ★★ 判据必须是「**引号内含 post**」⇒ ★★ 不是「引号后紧跟 post」
POST_LIT_RE = re.compile(r"'[^']*post[^']*'", re.IGNORECASE)
PRE_LIT_RE = re.compile(r"'pre[a-z0-9_]*'", re.IGNORECASE)
# ★★ **pre-only 分支**（★ 只产出 pre 读数 ⇒ ★ 不需要 post 段上界）
#   ★★ 形态：★ `LEFT JOIN exp e ON e.era = 'pre' AND e.win = w.win`（★ era 纯度自检键）
#   ★★ ⇒ ★★ 它读到的是**含 post 的 CTE**，但**只取 pre 行** ⇒ ★★ 若按「读到 post CTE」判相关
#   ★★   ⇒ ★★ 会要求它有 post 上界 ⇒ ★★ **假阳性**（★ 本轮真踩到 ⇒ M20_era_dropped_*）
PRE_ONLY_RE = re.compile(r"\b(?:era|win)\s*=\s*'pre", re.IGNORECASE)
# ★ 键里的 quoted 字面量（★ 用于取 prefix）
STR_LIT_RE = re.compile(r"'([^']*)'")


# ═══════════════════════════════════════════════════════════════════════════
# ★ v2：★ 文本三视图（★ 长度对齐 ⇒ 位置可互相映射）
# ═══════════════════════════════════════════════════════════════════════════
def sql_views(sql: str) -> tuple[str, str, list[bool]]:
    """返回 (text, mask, in_string)

    ★ text   = **注释被抹掉、字符串保留**（★ 用于内容匹配：★ `'post'` / TIMESTAMPTZ 字面量）
    ★ mask   = **注释与字符串都被抹成空格**（★ 用于括号深度、关键字定位、CTE 范围）
    ★ in_string[i] = 第 i 个字符是否在字符串字面量内（★ 用于排除「字符串里的假谓词」）

    ★★ v1 的 `strip_comments` 只抹掉**整行**注释（★ 行首是 `--` 才算）⇒ ★★ 行尾注释留在文本里
    ★★ ⇒ ★★ 而本文件的注释里**大量出现** `now()` / `'post'` / `TIMESTAMPTZ '...'`（★ 如第 25 处的说明）
    ★★ ⇒ ★★ v1 的谓词提取**会吃到注释里的假谓词** ⇒ ★★ 这是 v1 的**第四个假阴性来源**（★ 本轮才发现）
    """
    out_t: list[str] = []
    out_m: list[str] = []
    in_s: list[bool] = []
    i = 0
    n = len(sql)
    in_str = False
    while i < n:
        ch = sql[i]
        if in_str:
            if ch == "'":
                # ★ SQL 里的 '' 是转义的单引号
                if i + 1 < n and sql[i + 1] == "'":
                    out_t.append("''")
                    out_m.append("  ")
                    in_s.extend([True, True])
                    i += 2
                    continue
                in_str = False
                out_t.append(ch)
                out_m.append(" ")
                in_s.append(True)
                i += 1
                continue
            out_t.append(ch)
            out_m.append(" ")
            in_s.append(True)
            i += 1
            continue
        # ★ 不在字符串里
        if ch == "'":
            in_str = True
            out_t.append(ch)
            out_m.append(" ")
            in_s.append(True)
            i += 1
            continue
        if ch == "-" and i + 1 < n and sql[i + 1] == "-":
            # ★ 行注释：★ 抹到行尾
            j = sql.find("\n", i)
            j = n if j < 0 else j
            out_t.append(" " * (j - i))
            out_m.append(" " * (j - i))
            in_s.extend([False] * (j - i))
            i = j
            continue
        if ch == "/" and i + 1 < n and sql[i + 1] == "*":
            j = sql.find("*/", i + 2)
            j = n if j < 0 else j + 2
            out_t.append(" " * (j - i))
            out_m.append(" " * (j - i))
            in_s.extend([False] * (j - i))
            i = j
            continue
        out_t.append(ch)
        out_m.append(ch)
        in_s.append(False)
        i += 1
    return "".join(out_t), "".join(out_m), in_s


def depth_map(mask: str) -> list[int]:
    """★ 每个位置的括号深度（★ 位置 i 处、**在**该字符之前的深度）"""
    d = 0
    out: list[int] = []
    for ch in mask:
        out.append(d)
        if ch == "(":
            d += 1
        elif ch == ")":
            d = max(0, d - 1)
    return out


def cte_ranges(mask: str, depths: list[int]) -> list[tuple[int, int]]:
    """★ CTE 体（★ `<名字> AS ( ... )`）的范围（★ 只认**顶层**定义 ⇒ 深度 0 处）"""
    out: list[tuple[int, int]] = []
    for m in CTE_DEF_RE.finditer(mask):
        if depths[m.start()] != 0:
            continue
        op = mask.find("(", m.end() - 1)
        if op < 0:
            continue
        d = 0
        end = -1
        for k in range(op, len(mask)):
            if mask[k] == "(":
                d += 1
            elif mask[k] == ")":
                d -= 1
                if d == 0:
                    end = k
                    break
        if end > 0:
            out.append((op, end))
    return out


def cte_names(mask: str, depths: list[int]) -> list[str]:
    """★ CTE 名字（★ 与 `cte_ranges` 同序）"""
    out: list[str] = []
    for m in CTE_DEF_RE.finditer(mask):
        if depths[m.start()] != 0:
            continue
        out.append(m.group(1).lower())
    return out


def case_spans(mask: str) -> list[tuple[int, int]]:
    """★ `CASE ... END` 的范围（★ 嵌套按栈配对）

    ★★★ **★ 为什么必须识别 CASE 域（★ 本轮真踩到 ⇒ v2 的第二个假阴性来源）**：
    ★★★  era 切分写成 `CASE WHEN created_at < CUT THEN 'pre' ELSE 'post' END win`
    ★★★ ⇒ ★★★ 里面的 `created_at < CUT` **不是行过滤**（★ 它只是「分桶条件」）
    ★★★ ⇒ ★★★ 若把它当成上界 ⇒ ★★★ 块会被判成「窗口闭合」⇒ ★★★ **假阴性**
    ★★★ ⇒ ★★★ 本轮实测：★ `M8_ns_` 的**唯一**上界就是 CASE 内的那个 ⇒ ★★★
    ★★★   v2 初版据此判它「闭合」⇒ ★★★ **漏报**（★ 而它其实 post 段是**开区间**）
    """
    spans: list[tuple[int, int]] = []
    stack: list[int] = []
    events: list[tuple[int, str]] = []
    for m in CASE_KW_RE.finditer(mask):
        events.append((m.start(), "CASE"))
    for m in END_KW_RE.finditer(mask):
        events.append((m.start(), "END"))
    events.sort()
    for pos, kind in events:
        if kind == "CASE":
            stack.append(pos)
        else:
            if stack:
                spans.append((stack.pop(), pos))
    return spans


def top_level_selects(mask: str, depths: list[int]) -> list[int]:
    """★ 顶层 SELECT 的起始位置（★ 深度 0）"""
    out: list[int] = []
    for m in SELECT_RE.finditer(mask):
        if depths[m.start()] == 0:
            out.append(m.start())
    return out


def first_select_expr(text: str, mask: str, depths: list[int], start: int) -> str:
    """★ 顶层 SELECT 的**首个输出表达式**（★ 到同层逗号或 FROM 为止）

    ★★ v2 的关键：★ **不要求**它是字面量 ⇒ ★ CASE 表达式产出的键（M14/M14x）也能被读到
    """
    m = SELECT_RE.match(mask, start)
    if not m:
        return ""
    i = m.end()
    # ★ 跳过 DISTINCT / ALL
    mm = re.match(r"\s*(DISTINCT|ALL)\b", mask[i:], re.IGNORECASE)
    if mm:
        i += mm.end()
    # ★ 找同层的 ',' 或 FROM
    d = depths[i] if i < len(depths) else 0
    j = i
    end = len(mask)
    while j < len(mask):
        if depths[j] != d:
            j += 1
            continue
        if mask[j] == ",":
            end = j
            break
        fm = FROM_RE.match(mask, j)
        if fm:
            end = j
            break
        j += 1
    return text[i:end].strip()


def split_or_fragments(text: str, mask: str, ranges: list[tuple[int, int]]) -> list[str]:
    """★ 顶层区域的 OR 分支（★ CTE 体被排除 ⇒ 避免 CTE 里的 CASE 污染分支上下文）

    ★★ 为什么必须排除 CTE 体：★ `CASE WHEN created_at < CUT THEN 'pre' ELSE 'post' END era`
    ★★ 写在 `seg` CTE 里 ⇒ ★★ 若不排除 ⇒ ★★ 它所在分支会同时含 `'pre'` 与 `'post'`
    ★★ ⇒ ★★ 「分支上下文」失去区分度（★ post 侧与 pre 侧被混为一谈）
    """
    # ★ 把 CTE 体抹成空格（★ 长度不变 ⇒ 位置保持）
    buf = list(text)
    for a, b in ranges:
        for k in range(a, min(b + 1, len(buf))):
            buf[k] = " "
    outer = "".join(buf)
    frags: list[str] = []
    prev = 0
    for m in OR_RE.finditer(outer):
        frags.append(outer[prev:m.start()])
        prev = m.end()
    frags.append(outer[prev:])
    return frags


def literal_le_cut(rhs: str, cut_text: str) -> bool:
    """★ rhs 的字面量是否 **≤ 切点**（★ 闭合历史窗 ⇒ 合法）"""
    m = re.search(r"TIMESTAMPTZ\s+'([^']+)'", rhs, re.IGNORECASE)
    if not m:
        return False
    lit = m.group(1)
    base = lit.split("+")[0].split("-")[0].strip()
    off = re.search(r"([-+])\s*interval\s*'([^']+)'", rhs, re.IGNORECASE)
    if off:
        # ★ 只有 '-' 偏移才保证 ≤ 切点；★ '+' 偏移 ⇒ 可能 > 切点 ⇒ 交给调用方按 now() 判定
        if off.group(1) == "-":
            return True
        return False
    cut_base = cut_text.split("+")[0].split("-")[0].strip()
    return base <= cut_base


def subquery_ranges(mask: str, depths: list[int], cte_ranges_: list[tuple[int, int]]) -> list[tuple[int, int]]:
    """★ **子查询**（★ 标量子查询 / EXISTS）的范围（★ 深度 ≥1 且**不在 CTE 体内**）

    ★★★ **★ 为什么必须单独成窗（★ 本轮真踩到 ⇒ v2 的第四个假阴性来源）**：
    ★★★   `M20_<层>_post_split` 的 **tail_n** 是一个**标量子查询**：★ 它的窗口是
    ★★★   `[now-6h, ∞)`（★ **只有下界**）⇒ ★★ 而它**不在** CTE 体内 ⇒ ★★ 语句级/CTE 级判定都看不见它
    ★★★   ⇒ ★★★ ⇒ **必须把子查询也当成独立窗口**
    """
    out: list[tuple[int, int]] = []
    for m in SELECT_RE.finditer(mask):
        if depths[m.start()] == 0:
            continue
        if any(a <= m.start() <= b for a, b in cte_ranges_):
            continue
        op = None
        for k in range(m.start(), -1, -1):
            if mask[k] == "(":
                op = k
                break
        if op is None:
            continue
        d = 0
        end = -1
        for k in range(op, len(mask)):
            if mask[k] == "(":
                d += 1
            elif mask[k] == ")":
                d -= 1
                if d == 0:
                    end = k
                    break
        if end > 0:
            out.append((op, end))
    return out


def ctx_id_of(p: dict, ranges: list[tuple]) -> object:
    """★ 谓词所属的**窗口上下文**（★ 最内层的 CTE 体 / 子查询；★ 都不在 ⇒ 'outer'）

    ★★★ **★ 为什么必须逐窗口（★ 本轮真踩到）**：★ M16 一条语句里有三个独立窗口
    ★★★   （★ fr_used / fr_ref / fr_legacy 三个 CTE 体）⇒ ★★ 语句级判定时
    ★★★   删掉其中一个的 `now()` 上界 ⇒ ★★ **另两个替它背书** ⇒ ★★ 假阴性
    """
    best = "outer"
    best_size = None
    for entry in ranges:
        cid, a, b = entry
        if a <= p["pos"] <= b:
            size = b - a
            if best_size is None or size < best_size:
                best, best_size = cid, size
    return best


def base_col(col: str) -> str:
    """★ 列的**基名**（★ 去掉表/别名限定：★ `n.created_at` ⇒ `created_at`）

    ★★ 为什么：**窗口的上下界必须在同一列上** ⇒ ★★ 而 SQL 里同���列可能写成
    ★★ `n.created_at`（CTE 内）或 `created_at`（外层）⇒ ★★ 比较时取基名
    """
    return col.strip().split(".")[-1].lower()


def key_prefix(key_expr: str, text: str) -> str:
    """★ 键前缀（★ 人可读的块名）

    ★★ **必须跳过「比较值」字面量**：★ CASE 键 `CASE WHEN scope='all' THEN 'M14x_' ELSE 'M14_' END`
    ★★ 里第一个字面量是 `'all'`（★ 它是**比较值**、不是键名）⇒ ★★ 直接取第一个会得到 "all"
    ★★ ⇒ ★★ 判据：★ 字面量**前面**（★ 忽略空白）是 `=` ⇒ ★★ 它是比较值 ⇒ ★★ 跳过
    """
    for m in STR_LIT_RE.finditer(key_expr):
        pre = key_expr[:m.start()].rstrip()
        if pre.endswith("="):
            continue
        return m.group(1)
    lits = STR_LIT_RE.findall(text)
    return lits[0] if lits else "?"


def analyze(stmt: str, stmt_idx: int = -1) -> dict:
    text, mask, in_string = sql_views(stmt)
    depths = depth_map(mask)

    # ★ 顶层 SELECT ⇒ ★ 键表达式（★ 可多个：★ UNION ALL 会产生第二个顶层 SELECT）
    key_exprs: list[str] = []
    for sp in top_level_selects(mask, depths):
        e = first_select_expr(text, mask, depths, sp)
        if e:
            key_exprs.append(e)
    if not key_exprs:
        return {}

    cut_text = detect_cutpoint(text)
    ranges = cte_ranges(mask, depths)
    cases = case_spans(mask)

    # ★ 时间谓词（★ 必须排除「落在字符串里」的匹配）
    preds: list[dict] = []
    for m in TIME_PRED_RE.finditer(text):
        if in_string[m.start()]:
            continue
        rhs = m.group("rhs").strip()
        preds.append({
            "col": m.group("col"), "op": m.group("op"), "rhs": rhs,
            "now": "now()" in rhs.lower(),
            "literal": "TIMESTAMPTZ" in rhs.upper(),
            "in_cte": any(a <= m.start() <= b for a, b in ranges),
            "in_case": any(a <= m.start() <= b for a, b in cases),
            "pos": m.start(),
        })

    uppers = [p for p in preds if p["op"] in ("<", "<=")]
    lowers = [p for p in preds if p["op"] in (">", ">=")]

    # ★★ OR 分支上下文（★ CTE 体已排除）
    frags = split_or_fragments(text, mask, ranges)
    frag_of: list[int] = []
    for p in preds:
        frag_of.append(frag_index_for_pos(frags, p["pos"], ranges, text, mask))

    def scope_of(p: dict, fi: int) -> str:
        # ★★ CASE 内的谓词**不是行过滤**（★ 它只是分桶条件）⇒ ★★ 单独归一类
        if p["in_case"]:
            return "case"
        if p["in_cte"]:
            return "cte"
        f = frags[fi] if 0 <= fi < len(frags) else ""
        # ★★ **词表必须含 post_fix / pre_fix**（★ 见 POST_LIT_RE 的说明）
        if POST_LIT_RE.search(f):
            return "post"
        if PRE_LIT_RE.search(f):
            return "pre"
        return "neutral"

    for p, fi in zip(preds, frag_of):
        p["scope"] = scope_of(p, fi)

    # ★★ **post 段判定（★ 结构化三路）**
    key_all = " ".join(key_exprs)
    key_low = key_all.lower()
    post_by_key = any(mk in key_low for mk in POST_MARKERS)
    win_col_in_key = any(re.search(r"\b%s\b" % c, key_all, re.IGNORECASE) for c in WINDOW_COLS)
    post_literal = bool(POST_LIT_RE.search(text))
    pre_literal = bool(PRE_LIT_RE.search(text))
    # ★ 时间轴在切点处被显式分桶（★ `col >= CUT` ⇒ ★ **恰好等于切点**才算）
    cut_partition = any(
        p["op"] in (">", ">=") and p["literal"] and _is_exact_cut(p["rhs"], cut_text)
        for p in lowers
    )
    # ★ CASE 切分（★ `<col> < CUT THEN 'pre...' ELSE 'post...' END`）
    case_split = bool(CASE_SPLIT_RE.search(text))
    emits_post = post_by_key or (win_col_in_key and post_literal) or cut_partition or case_split

    # ★★ **是否按切点分段（★ 决定适用哪条规则）**：
    #   ★ ★ 分段块（★ era/win 型）⇒ ★ **规则 A**：★ post 段必须被 now() 界定
    #   ★ ★ 连续窗块（★ 无 era 维度）⇒ ★ **规则 B**：★ 整窗必须有闭合上界
    segmented = case_split or cut_partition or post_by_key or (win_col_in_key and post_literal and pre_literal)

    # ★★ **规则 A：★ post 段必须有作用于 post 侧的 now() 上界**
    #   ★★ scope='case' 的谓词**不算**（★ 它是分桶条件、不是行过滤）
    now_uppers = [p for p in uppers if p["now"]]
    post_bound = any(p["scope"] in ("cte", "post", "neutral") for p in now_uppers)

    # ★★★★ **★★ 输出分支级判定（★ 第 27 处的第二个结构性缺陷）**：
    # ★★★★   一条语句里可能有**多个独立输出分支**（★ `UNION ALL` ⇒ ★ M14/M14x 的主 SELECT +
    # ★★★★   `post_other_composition`；★ M20 的两个 SELECT）⇒ ★★ 每个分支是**独立的窗口**
    # ★★★★   ⇒ ★★ 删掉**其中一个**分支的 `now()` 上界 ⇒ ★★ **另一个分支的 now() 会替它背书**
    # ★★★★   ⇒ ★★ ⇒ 上界必须**逐分支**判定（★ 且 CTE 体内的上界对所有分支有效 ⇒ ★ 继承）
    branch_spans = []
    sel_pos = top_level_selects(mask, depths)
    for i, sp in enumerate(sel_pos):
        end = sel_pos[i + 1] if i + 1 < len(sel_pos) else len(text)
        branch_spans.append((sp, end))

    def branch_of(pos: int) -> int:
        for i, (a, b) in enumerate(branch_spans):
            if a <= pos < b:
                return i
        return -1

    # ★★ **CTE 依赖图（★ 分支的上界可以来自它读到的 CTE ⇒ ★ 继承）**
    names = cte_names(mask, depths)
    cte_refs: list[set[str]] = []
    for (a, b) in ranges:
        body = text[a:b]
        cte_refs.append({n for n in names if re.search(r"\b%s\b" % re.escape(n), body, re.IGNORECASE)})
    branch_refs: list[set[str]] = []
    for (a, b) in branch_spans:
        body = text[a:b]
        branch_refs.append({n for n in names if re.search(r"\b%s\b" % re.escape(n), body, re.IGNORECASE)})

    def reachable_ctes(b: int) -> set[str]:
        """★ 分支 b **传递**可达的 CTE 名集合（★ fpexp → fpm → 输出分支 ⇒ ★ 必须传递）"""
        seen: set[str] = set()
        stack = list(branch_refs[b])
        while stack:
            nm = stack.pop()
            if nm in seen:
                continue
            seen.add(nm)
            for i, n in enumerate(names):
                if n == nm and i < len(cte_refs):
                    stack.extend(cte_refs[i])
        return seen

    # ★ CTE 上下文的能力：★ 有没有 now() 上界 / 有没有 post 段
    cte_has_now: set[str] = set()
    cte_has_post: set[str] = set()
    for i, (a, b) in enumerate(ranges):
        body = text[a:b]
        if any(p["now"] and a <= p["pos"] <= b for p in preds):
            cte_has_now.add(names[i])
        if POST_LIT_RE.search(body):
            cte_has_post.add(names[i])

    branch_now_cap: list[bool] = [False] * len(branch_spans)
    branch_relevant: list[bool] = [False] * len(branch_spans)
    for p in preds:
        b = branch_of(p["pos"])
        if b < 0:
            continue
        # ★★ 注意：★ **有谓词 ≠ 产出 post 读数**（★ M20_era_dropped_* 只有 pre 键却带谓词）
        #   ⇒ ★★ 相关性由「post 输出」判定（★ 见下），★ 谓词只贡献「上界能力」
        if p["now"]:
            branch_now_cap[b] = True
    for i, (a, b) in enumerate(branch_spans):
        body = text[a:b]
        if any(t in mask[a:b] for t in EVENT_TABLES):
            branch_relevant[i] = True
        # ★★ 有效性：★ 本分支产出 post 读数（★ 自己的键/文本里有 post 取值
        #   ★★ **或** 它读到的某个 CTE 产出 post 读数**且它没有把结果限到 pre**）
        if POST_LIT_RE.search(body):
            branch_relevant[i] = True
        elif (cte_has_post & reachable_ctes(i)) and not PRE_ONLY_RE.search(body):
            branch_relevant[i] = True
        # ★★ 上界继承：★ 本分支自己有 now() 上界，★ **或** 它读到的某个 CTE 里有
        if reachable_ctes(i) & cte_has_now:
            branch_now_cap[i] = True

    # ★★ **规则 A 的分支级结论**：★ 产出 post 读数的分支都必须有 now() 上界
    uncovered_branches = [i for i, rel in enumerate(branch_relevant)
                          if rel and not branch_now_cap[i]]

    # ★★ **规则 B：★ 读事件表的连续窗 ⇒ 每个窗口都必须闭合**
    #   ★★★ **★ 窗口必须逐个（CTE 体级）判定（★ 本轮真踩到 ⇒ v2 的第三个假阴性来源）**：
    #   ★★★   M16 一条语句里有**三个独立窗口**（★ fr_used / fr_ref / fr_legacy ⇒ ★ 三个 CTE 体）
    #   ★★★   ⇒ ★★ 语句级判定时，★ 删掉 fr_used 的 `now()` 上界 ⇒ ★ **fr_ref / fr_legacy 替它背书**
    #   ★★★   ⇒ ★★★ 审计器报「全部合规」⇒ ★★★ **假阴性**
    #   ★★★ ⇒ ★★★ ⇒ 上下界必须**在同一窗口上下文内配对**（★ 同一 CTE 体，或同一外层区）
    reads_events = any(t in mask for t in EVENT_TABLES)
    # ★★★ **子查询 = 独立窗口**（★ tail_n 同族：★ 标量子查询的窗口没有 CTE 体兜着）
    subqs = subquery_ranges(mask, depths, ranges)
    all_ranges: list[tuple] = [(i, a, b) for i, (a, b) in enumerate(ranges)]
    all_ranges += [("subq%d" % k, a, b) for k, (a, b) in enumerate(subqs)]
    ctx_preds: dict[object, list[dict]] = {}
    for p in preds:
        ctx_preds.setdefault(ctx_id_of(p, all_ranges), []).append(p)
    open_ctx: list[object] = []
    for cid, ps in ctx_preds.items():
        lcols = {base_col(p["col"]) for p in ps if p["op"] in (">", ">=")}
        if not lcols:
            continue
        closed = any(
            p["scope"] != "case"
            and base_col(p["col"]) in lcols
            and (p["now"] or (p["literal"] and literal_le_cut(p["rhs"], cut_text)))
            for p in ps if p["op"] in ("<", "<=")
        )
        if not closed:
            open_ctx.append(cid)
    closed_upper = not open_ctx

    # ★★ **子查询窗口必须自闭合**（★ 不许继承外层的界：★ 标量子查询是**独立**的窗口）
    open_subq: list[object] = []
    for idx, (a, b) in enumerate(subqs):
        ps = [p for p in preds if a <= p["pos"] <= b]
        lcols = {base_col(p["col"]) for p in ps if p["op"] in (">", ">=")}
        if not lcols:
            continue
        closed = any(
            p["scope"] != "case"
            and base_col(p["col"]) in lcols
            and (p["now"] or (p["literal"] and literal_le_cut(p["rhs"], cut_text)))
            for p in ps if p["op"] in ("<", "<=")
        )
        if not closed:
            open_subq.append(idx)

    prefix = key_prefix(key_all, text)

    return {
        "stmt_idx": stmt_idx,
        "keys": key_exprs,
        "prefix": prefix,
        "preds": preds,
        "uppers": uppers,
        "lowers": lowers,
        "now_upper": bool(now_uppers),
        "now_lower": any(p["now"] for p in lowers),
        "post": emits_post,
        "post_by_key": post_by_key,
        "post_struct": (win_col_in_key and post_literal) or cut_partition or case_split,
        "post_bound": post_bound,
        "segmented": segmented,
        "n_branches": len(branch_spans),
        "uncovered_branches": uncovered_branches,
        "open_subq": open_subq,
        "reads_events": reads_events,
        "closed_upper": closed_upper,
        "era_block": bool(re.search(r"\b(?:era|win)\b", key_all, re.IGNORECASE)),
        "cutpoint": cut_text,
    }


def _is_exact_cut(rhs: str, cut_text: str) -> bool:
    """★ rhs 的字面量是否**恰好等于切点**（★ 不带 interval 偏移）"""
    m = re.search(r"TIMESTAMPTZ\s+'([^']+)'", rhs, re.IGNORECASE)
    if not m:
        return False
    if re.search(r"[-+]\s*interval", rhs, re.IGNORECASE):
        return False
    return m.group(1).strip() == cut_text.strip()


def detect_cutpoint(text: str) -> str:
    """★ era 切点字面量（★ **必须**是「既不带偏移、又带偏移」的那个 ⇒ 唯一确定）

    ★★ v1 没有切点概念（★ 全靠键名猜）⇒ ★★ v2 必须有：★ 「≤ 切点」的判定依赖它
    """
    lits = re.findall(r"TIMESTAMPTZ\s+'([^']+)'", text, re.IGNORECASE)
    if not lits:
        return ""
    with_off = set()
    without = set()
    for m in re.finditer(r"TIMESTAMPTZ\s+'([^']+)'(?:\s*[-+]\s*interval\s*'[^']+')?", text, re.IGNORECASE):
        lit = m.group(1)
        tail = text[m.end(1):m.end()]
        if re.match(r"\s*[-+]\s*interval", tail, re.IGNORECASE):
            with_off.add(lit)
        else:
            without.add(lit)
    both = without & with_off
    if both:
        return sorted(both, key=lambda x: -without.count(x))[0]
    # ★ 退化：★ 取出现次数最多的
    return sorted(set(lits), key=lambda x: -lits.count(x))[0]


def frag_index_for_pos(frags: list[str], pos: int, ranges: list[tuple[int, int]], text: str, mask: str) -> int:
    """★ 谓词位置 ⇒ 落在哪个 OR 分支（★ 用「抹掉 CTE 体后的文本」重新定位）"""
    buf = list(text)
    for a, b in ranges:
        for k in range(a, min(b + 1, len(buf))):
            buf[k] = " "
    outer = "".join(buf)
    prev = 0
    for i, f in enumerate(frags):
        nxt = outer.find(f, prev) if f else prev
        if nxt < 0:
            continue
        if nxt <= pos < nxt + len(f):
            return i
        prev = nxt + len(f)
    return len(frags) - 1


def audit(sql: str) -> tuple[list[dict], list[dict]]:
    rows, violations = [], []
    stmts = split_statements(sql)
    for idx, stmt in enumerate(stmts):
        info = analyze(stmt, idx)
        if not info:
            continue
        row = {
            "prefix": info["prefix"], "post": info["post"], "era_block": info["era_block"],
            "now_upper": info["now_upper"], "now_lower": info["now_lower"],
            "post_bound": info["post_bound"], "reads_events": info["reads_events"],
            "closed_upper": info["closed_upper"], "segmented": info["segmented"],
            "n_branches": info["n_branches"],
            "uncovered_branches": info["uncovered_branches"],
            "open_subq": info["open_subq"],
            "stmt_idx": info["stmt_idx"], "n_keys": len(info["keys"]),
            "preds": [(p["col"], p["op"], p["rhs"], p["scope"]) for p in info["preds"]],
        }
        rows.append(row)
        # ★★ **违例判定（★ 两条规则 · ★ 按「是否按切点分段」分流）**
        #   ★ ★ 分段块（★ era/win 型）⇒ ★ **规则 A**：★ post 段必须被 now() 界定
        #   ★ ★ 连续窗块（★ 无 era 维度）⇒ ★ **规则 B**：★ 整窗必须有闭合上界
        why = []
        if info["uncovered_branches"]:
            why.append("★ 输出分支 %s 没有 now() 上界（★ 同语句的其它分支替它背书 ⇒ 假阴性）"
                       % info["uncovered_branches"])
        if info.get("open_subq"):
            why.append("★ 子查询窗口 %s 没有闭合上界（★ 标量子查询 = 独立窗口）"
                       % info["open_subq"])
        if not info["segmented"] and info["reads_events"] and not info["closed_upper"]:
            why.append("★ 连续窗块：读事件表但事件时间窗没有闭合上界（★ 窗口 = [下界, ∞)）")
        if why:
            row["violations"] = why
            violations.append(row)
    return rows, violations


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


# ★★ **覆盖率下界（★ 第 27 处新增）**：★ 审计器**必须**看得见这些块
#   ★★ 否则「审计器自己瞎了」（★ 如 v1 看不见 M14/M14x）⇒ ★★ 会静默通过
MUST_SEE_PREFIXES = ("M14x_", "M15x_", "M12c_episodes_", "M12p_pending_", "M3_feat_",
                     "M11_latency_", "M5_probe_runs_24h", "M20_", "M15_")


def coverage(rows: list[dict]) -> dict:
    """★ 覆盖率报告（★ 「检查了哪些块」必须可复核 ⇒ ★ 第 27 处的纪律）"""
    prefixes = [r["prefix"] for r in rows]
    seen = {p: (p in prefixes) for p in MUST_SEE_PREFIXES}
    missing = [p for p, ok in seen.items() if not ok]
    windowed = [r for r in rows if r["post"] or r["reads_events"]]
    return {
        "n_blocks": len(rows),
        "n_windowed": len(windowed),
        "n_post": sum(1 for r in rows if r["post"]),
        "n_event": sum(1 for r in rows if r["reads_events"]),
        "missing_required": missing,
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--sql", default=str(SQL_PATH))
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()

    sql = pathlib.Path(args.sql).read_text(encoding="utf-8")
    rows, violations = audit(sql)
    cov = coverage(rows)

    if args.json:
        print(json.dumps({"coverage": cov, "blocks": rows, "violations": violations},
                         ensure_ascii=False, indent=1))
        return 1 if violations or cov["missing_required"] else 0

    print("== 判据「post 段窗口上界」穷举审计 v2 ==")
    print(f"   语句总数：{len(split_statements(sql))}　★ 产出键的块：{cov['n_blocks']}")
    print(f"   ★ 分段块（★ 按切点切 era）���{cov['n_post']}　★ 读事件表型：{cov['n_event']}　★ 受检块合计：{cov['n_windowed']}")
    print()
    for r in rows:
        if not (r["post"] or r["reads_events"]):
            continue
        ok = (r["post_bound"] if r["segmented"] else r["closed_upper"])
        mark = "✔" if ok else "★★"
        print(f"   {mark} {r['prefix']}  (分段={r['segmented']} 事件表={r['reads_events']} "
              f"post有界={r['post_bound']} 窗口闭合={r['closed_upper']})")
        for col, op, rhs, scope in r["preds"]:
            print(f"          · {col} {op} {rhs}   [scope={scope}]")
    print()
    # ★★ **覆盖率守卫（★ 第 27 处）**：★ 审计器必须看得见关键块
    if cov["missing_required"]:
        print(f"★★ **覆盖率缺口：以下关键块审计器看不见** ⇒ ★★ {cov['missing_required']}")
    else:
        print(f"   ✔ 覆盖率守卫：★ {len(MUST_SEE_PREFIXES)} 个关键块全部可见"
              f"（★ 含 M14/M14x 主判据块）")
    print()
    # ★★★ **任何报警都必须同时改退出码**（★ 项目纪律）
    if violations:
        print(f"★★ **发现 {len(violations)} 处窗口未闭合 / post 段无 now() 上界** ⇒ ★★ as-of 替换对它们无效")
        for v in violations:
            print(f"   ❌ {v['prefix']} ⇒ {'；'.join(v.get('violations', []))}")
        return 1
    if cov["missing_required"]:
        # ★★ 覆盖率缺口**也是违例**（★ 否则「审计器自己瞎了」会静默通过）
        print("   ✔ 无窗口违例")
        print("   ★★ 但覆盖率有缺口 ⇒ ★★ 审计器**无效**（★ 它没看见那些块）⇒ ★★ rc=1")
        return 1
    print("   ✔ 全部 post 段 / 事件窗口型读数都具备有效的上界")
    print("   ⇒ ★★ as-of 替换能把它们**收口成恰好 stage 窗口**（★ 24h / 168h）⇒ ★★ 负控与预演几何一致")
    return 0


if __name__ == "__main__":
    sys.exit(main())
