# -*- coding: utf-8 -*-
#!/usr/bin/env python3
"""build-plan-form-spec.py —— R179 轮：把「生产真实语句」变成【实测计划形态】的 spec。

# 输入
① 生产语句（scripts/audit/asset/canonical-view-statements.tsv）
   —— 由 scripts/audit/collect-canonical-statements.sh 从 252 的 pg_stat_statements 采集；
② 源码侧形态册（scripts/audit/asset/canonical-read-point-shapes.tsv + 形态 JSON）
   —— 由 scripts/audit/extract-canonical-read-point-shapes.py 从 35 个受影响文件抽取。

# 输出
① cmd/tools/query_plan_form/specs/r179-canonical-statements.json
   —— 实测工具的输入（★ 每条语句 + 它的参数角色）；
② scripts/audit/asset/canonical-statement-map.tsv
   —— 【生产语句】↔【源码读点】的映射（★ 带匹配依据）。

# ★★ 参数角色是什么

pg_stat_statements 把**字面量**也归一成了 $N ⇒ ⇒ 所以「$1 是 api_key_id」
「$2 是时间窗下界」这类信息**不在语句文本里** ⇒ ⇒ 必须**从语法上下文反推**：

  · `col <op> $N` / `$N <op> col`  ⇒  $N 是 **col 的取值**（★ 列角色）
  · `COALESCE(col, $N)`             ⇒  $N 是 **col 的兜底常量**（★ 常量角色）
  · `IN ($N, ...)` / `NOT IN (...)` ⇒  $N 是 **常量**
  · `func($N, col)`                 ⇒  $N 是 **常量**（★ 例如 date_trunc('day', ts) 的 'day'）
  · 其余                             ⇒  **常量**

★ 实测时：列角色 ⇒ 取该环境里**真实存在**的代表值；常量角色 ⇒ 取**类型合适的代表值**。

# ★★ 诚实边界（★ 写在这里，也写进审计档）

  · 常量位用代表值代填 ⇒ ⇒ 读数是【该形态的**典型**计划】，
    **不是**【某一次调用的精确计划】；
  · ★ 但「计划形态」这个判据本身对常量值**不敏感**：★ 它由
    【哪些列进了索引条件】决定 ⇒ ⇒ 常量只影响**估计行数**，不影响形态归类；
  · ★ 例外：**ts 窗口**对形态敏感（★ 窗口决定分区裁剪 ⇒ 决定扫几个分区）
    ⇒ ⇒ 窗口固定为「近 7 天」（★ 默认）并写进读数。
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import importlib.util

_spec = importlib.util.spec_from_file_location(
    "shapes_mod", os.path.join(os.path.dirname(os.path.abspath(__file__)), "extract-canonical-read-point-shapes.py")
)
shapes_mod = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(shapes_mod)


# ── 参数角色反推 ────────────────────────────────────────────────────────

# ★ 谓词比较符（★ 与抽取器同款）
_OP = r"(?:=|<>|!=|<|>|<=|>=|@>|<@|\bIN\b|\bNOT IN\b|\bLIKE\b|\bILIKE\b|\bBETWEEN\b|\bIS\b)"

_COL_THEN_PARAM = re.compile(
    r"(?:[A-Za-z_][\w$]*\s*\.\s*)?([A-Za-z_][\w$]*)\s*" + _OP + r"\s*\$(\d+)", re.I)
_PARAM_THEN_COL = re.compile(
    r"\$(\d+)\s*" + _OP + r"\s*(?:[A-Za-z_][\w$]*\s*\.\s*)?([A-Za-z_][\w$]*)", re.I)
_COALESCE_CONST = re.compile(r"COALESCE\s*\(\s*(?:[A-Za-z_][\w$]*\s*\.\s*)?([A-Za-z_][\w$]*)\s*,\s*\$(\d+)", re.I)


def infer_param_roles(sql: str) -> list[dict]:
    """反推每个 $N 的角色。"""
    roles: dict[int, dict] = {}

    def put(idx: int, role: dict) -> None:
        if idx not in roles:
            # ★ index 必须带上（★ R179 实测踩过：★ 漏了它 ⇒ ⇒ Go 侧全落到 0
            #   ⇒ ⇒ $N 一个都没被替换 ⇒ ⇒ EXPLAIN 报「there is no parameter $N」）
            role = dict(role)
            role["index"] = idx
            roles[idx] = role

    for m in _COL_THEN_PARAM.finditer(sql):
        put(int(m.group(2)), {"column": m.group(1).lower(), "kind": "column"})
    for m in _PARAM_THEN_COL.finditer(sql):
        put(int(m.group(1)), {"column": m.group(2).lower(), "kind": "column"})
    for m in _COALESCE_CONST.finditer(sql):
        put(int(m.group(2)), {"column": m.group(1).lower(), "kind": "constant"})

    # ★★ `col = ANY($N)`（★ 数组参数）⇒ ★ 角色是「列 + 数组」
    #   ⇒ ★ 不识别它 ⇒ $N 落到「纯常量」⇒ ⇒ 填 NULL ⇒ ⇒ `= ANY(NULL)` ⇒ ⇒ 谓词恒空
    for m in re.finditer(r"([A-Za-z_][\w$]*)\s*(?:=|<>|!=|<|>|<=|>=)\s*ANY\s*\(\s*\$(\d+)", sql, re.I):
        put(int(m.group(2)), {"column": m.group(1).lower(), "kind": "any"})

    # ★★ date_trunc 的第一个参数（★ 形如 date_trunc('day', ts) 的 'day'）
    #   ⇒ ★ 必须给**合法的日期部分**，★ 否则 NULL 会报语法错
    for m in re.finditer(r"date_trunc\s*\(\s*\$(\d+)", sql, re.I):
        put(int(m.group(1)), {"column": "", "kind": "datepart"})

    # ★ 其余一律常量
    # ★★ 「INTERVAL 上下文」单独标记（★ 实测踩过：★ INTERVAL NULL 会报
    #   「invalid input syntax for type interval」⇒ ⇒ 常量位要取**类型合适**的值）。
    for m in re.finditer(r"\$(\d+)", sql):
        idx = int(m.group(1))
        if idx in roles:
            continue
        before = sql[max(0, m.start() - 40):m.start()].lower()
        kind = "interval" if "interval" in before else "constant"
        put(idx, {"column": "", "kind": kind})
    return [roles[k] for k in sorted(roles)]


# ── 源码侧形态（★ 用于把生产语句映射回 35 个文件）──────────────────────

def load_source_shapes(path: str) -> list[dict]:
    with open(path, encoding="utf-8") as fh:
        data = json.load(fh)
    return data.get("shapes", [])


def statement_fingerprint(sql: str) -> frozenset:
    """语句的「过滤列指纹」（★ 用于与源码侧形态对齐）。"""
    return frozenset(shapes_mod.extract_filter_cols(sql))


# ── 排序列抽取（★ 只认【查询级】排序）──────────────────────────────────

# ★★ 口径（★ 写在这里，也写进审计档）：
#   · 只取【查询级】ORDER BY ⇒ ★ 聚合函数里的 ORDER BY（★ `WITHIN GROUP (ORDER BY x)` /
#     `OVER (ORDER BY x)` / 函数实参里的 ORDER BY）**不是**查询排序 ⇒ ⇒ 一律不算；
#   · 只保留**裸列名** ⇒ ★ 剥掉表名/别名限定（★ `rl.ts` ⇒ `ts`）
#     ⇒ ★ 与同文件 `sql_filter_cols` 同口径（★ 那边也只留列名、也小写）
#     ⇒ ★ **同一份输出里口径一致**；
#   · ASC / DESC / NULLS FIRST / NULLS LAST 一律去掉（★ 只留列名，方向不入判据）；
#   · 序号（★ `ORDER BY 1, 2`）跳过；表达式（★ `ORDER BY count(*)` / `lower(x)` /
#     `CASE … END`）跳过 ⇒ ★ **不瞎猜**；
#   · 抽不到 ⇒ 返回**空列表** ⇒ ★ 空 = 「没抽到」，**不是**「没有排序」。
#
# ★★ 关于【注释】的口径（★ R180 轮实测踩出来的 ⇒ 别改成「一律按注释切分」）：
#   生产语句台账由 `regexp_replace(query, '\s+', ' ', 'g')` 生成（★ 见
#   scripts/audit/collect-canonical-statements.sh）⇒ ⇒ **换行被压成空格**
#   ⇒ ⇒ `--` 注释的边界在事实源里**已经不可复原**
#   ⇒ ⇒ 若一律按注释切分（★ 把 `--` 之后整段抹掉）⇒ ⇒ **注释之后的真 SQL 会被一起吞掉**
#   ⇒ ⇒ 实测 6 条语句的真实排序列（★ `ts` / `day` / `latest_at` …）会变成「没有排序」
#   ⇒ ⇒ 而「空排序列」在下游（★ 索引覆盖门）会被读成「不需要排序索引」⇒ ⇒ **假绿**
#   ⇒ ⇒ 所以按**有没有换行**分两种口径（★ 见 _lex）：
#     · 换行还在 ⇒ `--` 的边界可复原 ⇒ ⇒ **抹掉注释**（★ 注释里的 ORDER BY 不算数）；
#     · 换行被压掉（★ 本仓生产语句台账就是这种）⇒ ⇒ **不抹注释**
#       ⇒ ⇒ 代价：注释里若提到 `ORDER BY <列>` 会造成假阳性
#       ⇒ ⇒ 实测：当前事实源 104 条里**零**处注释提及 ORDER BY（★ 见审计档读数）
#       ⇒ ⇒ 假阳性的后果是【多要一条索引】（★ 门判 gap ⇒ 偏保守 ⇒ 不会假绿）。

# ★ ORDER BY 子句体的终止关键字（★ 只在**同层**认 ⇒ 嵌套括号里的不算）
_ORDER_STOP_KWS = ("limit", "offset", "fetch", "for", "window", "union", "except", "intersect")

# ★ ORDER BY 关键字（★ 用它判断「子句体到下一个 ORDER BY 为止」）
_ORDER_BY_AT = re.compile(r"order\s+by\b", re.I)

# ★ 裸列名（★ 可带一层表/别名限定；★ 序号与表达式都匹配不上 ⇒ 自动被跳过）
_ORDER_BARE_COL_RE = re.compile(
    r'^(?:(?:"[^"]+"|[A-Za-z_][\w$]*)\s*\.\s*)?("[^"]+"|[A-Za-z_][\w$]*)$')

# ★ 「左括号前是这些词 ⇒ 它开的是【子查询/派生表】⇒ 其中的 ORDER BY 算查询排序」
#   ⇒ ★ 不在这张表里的 ⇒ 一律当【函数实参表】处理 ⇒ **跳过**（★ 保守：不瞎猜）
_ORDER_SUBQUERY_CTX = frozenset({
    "from", "in", "as", "exists", "lateral", "values", "select", "all", "any",
    "some", "on", "where", "and", "or", "not", "join", "using", "union",
    "except", "intersect", "with", "returning", "case", "when", "then",
    "else", "between", "by", "table", "only",
})


def _closing_quote(sql: str, start: int) -> int:
    """找 start 处引号的**配对结束位置**（★ 找不到 ⇒ 返回 -1 ⇒ ★ 视为普通字符）。

    ★ 「配对才当字面量」是必需的（★ R180 轮实测踩过）：
      生产语句台账把换行压成空格（★ 见上方口径）⇒ ⇒ 注释里的撇号会变成**未闭合**的引号
      ⇒ ⇒ 若照样当字面量 ⇒ ⇒ 抹字面量会一路吞到句尾
      ⇒ ⇒ 真 SQL（包括 ORDER BY）会整段消失（★ 实测 2 条语句受害）。
    """
    quote = sql[start]
    j = start + 1
    n = len(sql)
    while j < n:
        if sql[j] == quote:
            if j + 1 < n and sql[j + 1] == quote:
                j += 2
                continue
            return j + 1
        j += 1
    return -1


def _lex(sql: str) -> tuple[str, list[int], list[int]]:
    """单遍词法扫描 ⇒ (抹掉字面量的文本, 每个位置的括号深度, 每个位置的最内层左括号位置)。

    ★ 单引号字面量（★ `'…'`）抹成同长度空格（★ 保留偏移）；**没配上的引号不当字面量**。
    ★ 双引号标识符（★ `"…"`)**保留原文**（★ 它是列名本体 ⇒ 抹掉就认不出列了）
      ⇒ ★ 但扫描时整体跳过（★ 里面的 `--` 不是注释、里面的括号不计入深度）。
    ★ 括号深度只统计【代码区】⇒ ⇒ 字面量里的括号**不算**。
    ★ 注释：★ **换行还在** ⇒ 按注释切分并抹掉（★ 注释里的 ORDER BY 不算数）；
      ★ **换行被压掉**（★ 本仓生产语句台账就是这种）⇒ ⇒ 注释边界不可复原
      ⇒ ⇒ 注释当普通文本（★ 但括号照算 ⇒ ⇒ 注释之后的真 SQL 仍能算准深度）。
    """
    n = len(sql)
    out = list(sql)
    depth_of = [0] * n
    inner_open = [-1] * n
    stack: list[int] = []
    mask_comments = "\n" in sql
    i = 0
    while i < n:
        ch = sql[i]
        if ch == "'" or ch == '"':
            j = _closing_quote(sql, i)
            if j < 0:
                # ★ 没配上 ⇒ ★ 当普通字符（★ 不吞后面）
                depth_of[i] = len(stack)
                inner_open[i] = stack[-1] if stack else -1
                i += 1
                continue
            for k in range(i, j):
                if ch == "'":
                    out[k] = " "
                depth_of[k] = len(stack)
                inner_open[k] = stack[-1] if stack else -1
            i = j
            continue
        if mask_comments and ch == "-" and sql[i + 1:i + 2] == "-":
            j = i + 2
            while j < n and sql[j] != "\n":
                out[j] = " "
                depth_of[j] = len(stack)
                inner_open[j] = stack[-1] if stack else -1
                j += 1
            i = j
            continue
        if mask_comments and ch == "/" and sql[i + 1:i + 2] == "*":
            j = sql.find("*/", i + 2)
            j = n if j < 0 else j + 2
            for k in range(i, j):
                out[k] = " "
                depth_of[k] = len(stack)
                inner_open[k] = stack[-1] if stack else -1
            i = j
            continue
        # ★ 代码区
        depth_of[i] = len(stack)
        inner_open[i] = stack[-1] if stack else -1
        if ch == "(":
            stack.append(i)
        elif ch == ")" and stack:
            stack.pop()
        i += 1
    return "".join(out), depth_of, inner_open


def _word_before(text: str, end: int) -> str:
    """取 text[0:end] 末尾那个「词」（★ 小写；★ 没有就是空串）。"""
    k = end - 1
    while k >= 0 and text[k].isspace():
        k -= 1
    j = k
    while k >= 0 and (text[k].isalnum() or text[k] in "_$"):
        k -= 1
    return text[k + 1:j + 1].lower()


def _is_word_boundary(text: str, i: int, length: int) -> bool:
    before = text[i - 1] if i > 0 else " "
    after = text[i + length] if i + length < len(text) else " "
    return not (before.isalnum() or before in "_$") and not (after.isalnum() or after in "_$")


def _bare_order_col(seg: str) -> str:
    """把一个 ORDER BY 段解析成【裸列名】（★ 抽不出就返回空串 ⇒ 不瞎猜）。"""
    s = seg.strip()
    if not s:
        return ""
    s = re.sub(r"\s+nulls\s+(?:first|last)\s*$", "", s, flags=re.I)
    s = re.sub(r"\s+(?:asc|desc)\s*$", "", s, flags=re.I)
    s = s.strip()
    m = _ORDER_BARE_COL_RE.match(s)
    if not m:
        return ""
    name = m.group(1)
    if len(name) >= 2 and name.startswith('"') and name.endswith('"'):
        name = name[1:-1]
    return name.lower()


def extract_order_cols(sql: str) -> list[str]:
    """抽出【查询级】ORDER BY 的排序列（★ 裸列名；★ 去重；★ 保持出现顺序）。

    ★ 与 `shapes_mod.extract_order_cols` **同名但不同物**：
       那边是【源码侧形态册】用的（★ 返回带方向的串 "ts DESC"）；
       这边是【生产语句 spec】用的（★ 返回裸列名列表 ["ts"]）。
    """
    masked, depth_of, inner_open = _lex(sql)
    n = len(masked)
    if not n:
        return []

    out: list[str] = []
    for m in re.finditer(r"\border\s+by\b", masked, re.I):
        pos = m.start()
        d0 = depth_of[pos]
        inner = inner_open[pos]

        # ★★ 不是【查询级】排序 ⇒ 跳过（★ 宁可空着，也不把聚合排序当查询排序）
        if inner >= 0:
            ctx = masked[max(0, inner - 40):inner]
            if re.search(r"within\s+group\s*$", ctx, re.I):
                continue          # ★ 聚合函数的有序集（★ percentile_cont 等）
            if re.search(r"\bover\s*$", ctx, re.I):
                continue          # ★ 窗口函数的帧
            if _word_before(masked, inner) not in _ORDER_SUBQUERY_CTX:
                continue          # ★ 函数实参表 ⇒ 不是查询排序

        # ── 取 ORDER BY 子句体（★ 到同层终止关键字 / 作用域结束为止）──
        body_start = m.end()
        i = body_start
        seg_start = body_start
        segs: list[str] = []
        while i < n:
            ch = masked[i]
            dep = depth_of[i]
            if dep < d0:
                break
            if dep == d0 and ch == ")":
                break            # ★ 作用域在这一层闭合了
            if dep == d0:
                stop = False
                for kw in _ORDER_STOP_KWS:
                    if masked[i:i + len(kw)].lower() == kw and _is_word_boundary(masked, i, len(kw)):
                        stop = True
                        break
                if stop:
                    break
                # ★ 同层的下一个 ORDER BY ⇒ ★ 本子句到此为止
                #   （★ 否则一个子句会把后面几段排序一起吞掉 ⇒ ⇒ 抽出来的列是错的）
                if _ORDER_BY_AT.match(masked, i):
                    break
                if ch == ",":
                    segs.append(masked[seg_start:i])
                    seg_start = i + 1
            if ch == "-" and masked[i + 1:i + 2] == "-":
                # ★ 注释起点 ⇒ ★ 本段到此为止（★ 注释里的字不是列名）
                segs.append(masked[seg_start:i])
                seg_start = i
                j = i + 2
                while j < n and masked[j] != "\n":
                    j += 1
                i = j
                seg_start = i
                continue
            if ch == "/" and masked[i + 1:i + 2] == "*":
                segs.append(masked[seg_start:i])
                j = masked.find("*/", i + 2)
                i = n if j < 0 else j + 2
                seg_start = i
                continue
            i += 1
        segs.append(masked[seg_start:i])

        for seg in segs:
            col = _bare_order_col(seg)
            if col and col not in out:
                out.append(col)
    return out


# ── 抽出「读视图的那段 SELECT」 ─────────────────────────────────────────

_VIEW_RE = re.compile(r"\brequest_logs_with_current_month\b", re.I)
_SELECT_RE = re.compile(r"\bSELECT\b", re.I)


def _balanced_end(sql: str, start: int) -> int:
    """从 start 起扫到【括号配平】的语句末尾（★ 返回不含末尾 ')' 的位置）。"""
    depth = 0
    i = start
    n = len(sql)
    while i < n:
        ch = sql[i]
        if ch == "'":
            i += 1
            while i < n:
                if sql[i] == "'":
                    if i + 1 < n and sql[i + 1] == "'":
                        i += 2
                        continue
                    break
                i += 1
        elif ch == "(":
            depth += 1
        elif ch == ")":
            if depth == 0:
                return i
            depth -= 1
        elif ch == ";" and depth == 0:
            return i
        i += 1
    return n


def extract_view_select(sql: str) -> tuple[str, str]:
    """抽出「读 canonical 视图的那段 SELECT」。

    ★★ 为什么必须抽（★ R179 实测逼出来的）：
       生产语句常常是 `INSERT INTO rollup (…) SELECT … FROM view …`
       或 `DELETE FROM rollup WHERE … NOT EXISTS (SELECT … FROM view …)`
       ⇒ ⇒ 它们的参数**大量指向别的表**（★ bucket / dim_type / catalog 列）
       ⇒ ⇒ 拿整条语句去 EXPLAIN ⇒ ⇒ 那些参数取不到代表值 ⇒ ⇒ **整条作废**。
    ⇒ ⇒ 而**我们真正要判的**是「★ 读视图那一段走了哪种计划形态」
    ⇒ ⇒ ⇒ 只 EXPLAIN 那一段 ⇒ ⇒ 参数就只剩视图的列 ⇒ ⇒ 拿得到代表值。

    ★ 返回 (抽出的 SQL, 抽取方式)；★ 抽不出 ⇒ 返回 (原 SQL, "whole")。
    """
    m = _VIEW_RE.search(sql)
    if not m:
        return sql, "whole"
    pos = m.start()
    # ★ 从视图名**向前**找所有 SELECT 关键字（★ 从近到远）
    starts = [mm.start() for mm in _SELECT_RE.finditer(sql, 0, pos)]
    best = None
    for st in reversed(starts):
        end = _balanced_end(sql, st)
        seg = sql[st:end]
        if _VIEW_RE.search(seg):
            # ★ 取**最短**的那一段（★ 即最内层的 SELECT）
            if best is None or len(seg) < len(best):
                best = seg
                break
    if not best:
        return sql, "whole"
    return best, "view-select"


def _norm_frag(sql: str) -> str:
    """把 SQL 归一化成可做**子串包含**比较的形态。

    ★ 归一化：字符串字面量 → ''，$N → ?，**裸常量** → ?，空白折叠，小写。
    ★★ 「裸常量」这一步是必需的（★ R179 实测踩过）：
      源码里写的是 `success = TRUE` / `COALESCE(is_auto_request, FALSE) = FALSE`，
      而 pg_stat_statements 把这些**常量换成了 $N** ⇒ ⇒ 两侧不抹平就永远匹配不上。
    ★ 目的：两侧抹平后，源码片段应当是生产语句的**子串**。
    """
    s = re.sub(r"'(?:[^']|'')*'", "''", sql)
    s = re.sub(r"\$(\d+)", "?", s)
    # ★ 裸常量（★ 数字 / true / false / null）
    s = re.sub(r"(?<![\w$])\d+(?:\.\d+)?(?![\w$])", "?", s)
    s = re.sub(r"(?<![\w$])(true|false|null)(?![\w$])", "?", s, flags=re.I)
    s = re.sub(r"\s+", " ", s)
    return s.strip().lower()


def map_to_source(stmt_sql: str, source_shapes: list[dict]) -> tuple[str, str, str, list[str]]:
    """把一条生产语句映射到源码侧形态。

    ★ 主判据 = **子串包含**（★ 最强）：
      源码里那一段字面量（★ 拼接前的原文）应当**逐字出现在**生产语句里
      ⇒ ⇒ 抹平字面量与参数后，源码片段应当是生产语句的**子串**。
    ★ 次判据 = **比较列集合**（★ 最弱，★ 只在包含判据打平时用来窄化）。
    ★ **绝不硬凑**：★ 多个候选 ⇒ 判 ambiguous，★ 0 个 ⇒ 判 unmatched。
    ★ 返回的第四项 = **候选 shape 列表**（★ 歧义时也留痕，★ 便于人工复核）。
    """
    stmt_norm = _norm_frag(stmt_sql)
    # ★ 片段太短 ⇒ 会命中一堆语句 ⇒ ⇒ 设长度门槛（★ 门槛下面的片段**没有区分度**）
    MIN_FRAG = 25
    hits = []
    for sh in source_shapes:
        src = sh.get("sql") or ""
        # ★★ 匹配片段 = **WHERE 子句**（★ 而不是整条 SQL）：
        #   源码片段的 SELECT 列表与生产语句不同（★ 源码是局部，生产是整条）
        #   ⇒ ⇒ 整条比对必然匹配不上 ⇒ ⇒ 谓词才是【可跨两侧对齐】的部分。
        frag = _norm_frag(_where_clause(src))
        if len(frag) < MIN_FRAG:
            frag = _norm_frag(src)
        if len(frag) < MIN_FRAG:
            continue
        if "request_logs_with_current_month" not in frag and " where " not in frag:
            continue
        if frag in stmt_norm:
            hits.append((len(frag), sh))
    cand = [sh["shape_id"] for _, sh in hits]
    if len(hits) == 1:
        sh = hits[0][1]
        return sh["shape_id"], f"{sh['file']}:{sh['line']}", "substring-unique", cand
    if len(hits) > 1:
        # ★ 窄化：★ 取**最长**命中片段（★ 片段越长越具体）
        best_len = max(n for n, _ in hits)
        best = [sh for n, sh in hits if n == best_len]
        if len(best) == 1:
            sh = best[0]
            return sh["shape_id"], f"{sh['file']}:{sh['line']}", f"substring-longest:{best_len}", cand
        # ★ 同长但不同 shape ⇒ 再用「比较列集合」窄化
        stmt_cols = set(compared_cols(stmt_sql))
        scored = []
        for sh in best:
            cols = set(compared_cols(sh.get("sql") or ""))
            inter = len(cols & stmt_cols)
            union = len(cols | stmt_cols) or 1
            scored.append((inter / union, sh))
        scored.sort(key=lambda x: -x[0])
        if len(scored) > 1 and scored[0][0] - scored[1][0] > 0.2:
            sh = scored[0][1]
            return sh["shape_id"], f"{sh['file']}:{sh['line']}", f"substring-scored:{scored[0][0]:.2f}", cand
        return "", "", f"ambiguous:{len(hits)}", cand
    return "", "", "unmatched", cand


def _where_clause(sql: str) -> str:
    """取 SQL 的 WHERE 子句文本（★ 抽不出就返回空串）。"""
    masked = shapes_mod._strip_strings(sql)
    positions = shapes_mod._all_keyword_positions(masked, "where")
    if not positions:
        return ""
    start = positions[0] + len("where")
    tail_masked = masked
    ends = []
    for kw in ("group by", "order by", "limit", "offset", "having", "window", "union"):
        for p in shapes_mod._all_keyword_positions(tail_masked, kw):
            if p >= start:
                ends.append(p)
    end = min(ends) if ends else len(masked)
    return sql[start:end]


def compared_cols(sql: str) -> list[str]:
    """「与参数做比较」的列（★ 真正限制行的列）。"""
    out = []
    for r in infer_param_roles(sql):
        if r.get("kind") == "column" and r.get("column"):
            if r["column"] not in out:
                out.append(r["column"])
    return out


# ── 主流程 ──────────────────────────────────────────────────────────────

def die(msg: str) -> None:
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(1)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--statements", required=True)
    ap.add_argument("--source-shapes-json", required=True)
    ap.add_argument("--out-spec", required=True)
    ap.add_argument("--out-map", required=True)
    args = ap.parse_args()

    # ★★ 空输入 ⇒ 判据失效 ⇒ ★ 直接失败（★ 掏空法门会真的跑这个读方 ⇒ ★ 必须红）
    if os.path.getsize(args.statements) == 0:
        die("★ 生产语句台账是空的 ⇒ ⇒ 事实源没了 ⇒ ⇒ 判据失效 ⇒ ⇒ 退出码非 0")
    source_shapes = load_source_shapes(args.source_shapes_json)

    shapes = []
    map_rows = []
    seen: dict[str, dict] = {}
    with open(args.statements, encoding="utf-8") as fh:
        for lineno, line in enumerate(fh, 1):
            line = line.rstrip("\n")
            if not line.strip():
                continue
            # ★★ 必须跳过注释行（★ R179 实测踩过：★ 头部的「列说明」那一行
            #   ⇒ 恰好有 5 个制表符分隔的字段 ⇒ ⇒ 被当成了一条生产语句
            #   ⇒ ⇒ 读数里混进一条【表头】⇒ ⇒ 条数对不上（104 读成 105））。
            if line.startswith("#"):
                continue
            parts = line.split("\t")
            if len(parts) < 5:
                print(f"★ 跳过坏行 {lineno}", file=sys.stderr)
                continue
            calls, total_ms, mean_ms, rows, sql = parts[0], parts[1], parts[2], parts[3], parts[4]
            sql = sql.strip()
            if not sql:
                continue
            shape_id = "stmt-" + hashlib.sha256(sql.encode("utf-8")).hexdigest()[:10]
            if shape_id in seen:
                continue
            # ★★ 只取「读视图的那段 SELECT」去 EXPLAIN（★ 理由见 extract_view_select）
            probe_sql, how = extract_view_select(sql)
            roles = infer_param_roles(probe_sql)
            # ★★ 指纹必须取【真正送进 EXPLAIN 的那段】（★ probe_sql）
            #   ⇒ ★ 送 EXPLAIN 的是 probe_sql（★ extract_view_select 抽出的那段 SELECT）
            #   ⇒ ★ 若拿整条 sql（含 JOIN）算指纹 ⇒ ⇒ JOIN 条件会被当成【过滤谓词】
            #   ⇒ ⇒ 判据错（★ R180 轮审计取证出的真实缺陷①）。
            fp = statement_fingerprint(probe_sql)
            sid, source, reason, cands = map_to_source(sql, source_shapes)
            shapes.append({
                "shape_id": shape_id,
                "file": "",
                "line": 0,
                "occurrence_count": 0,
                "file_count": 0,
                "dynamic": False,
                "sql": probe_sql,
                "full_sql": sql,
                "extract": how,
                "sql_filter_cols": sorted(fp),
                # ★ 排序列（★ 从 probe_sql 抽「查询级」ORDER BY ⇒ ★ 裸列名、逗号分隔）
                #   ⇒ ★ 类型保持**字符串**（★ Go 侧 `SQLOrderCols string` ⇒ 改成数组会让
                #     EXPLAIN 工具 json.Unmarshal 直接失败 ⇒ 那是另一条线的事，★ 不在本轮）。
                "sql_order_cols": ",".join(extract_order_cols(probe_sql)),
                "param_roles": roles,
            })
            seen[shape_id] = shapes[-1]
            map_rows.append({
                "shape_id": shape_id,
                "calls": calls,
                "total_exec_time_ms": total_ms,
                "mean_exec_time_ms": mean_ms,
                "rows": rows,
                "source_shape_id": sid,
                "source": source,
                "map_reason": reason,
                "candidates": ",".join(cands),
                "filter_cols": ",".join(sorted(fp)),
            })

    with open(args.out_spec, "w", encoding="utf-8") as fh:
        json.dump({
            "_comment": [
                "★ R179 轮：生产真实语句（252 的 pg_stat_statements）⇒ 实测计划形态。",
                "★ 生成器：scripts/audit/build-plan-form-spec.py",
            ],
            "shapes": shapes,
        }, fh, ensure_ascii=False, indent=1)

    with open(args.out_map, "w", encoding="utf-8") as fh:
        fh.write("# canonical-statement-map.tsv —— R179 轮：生产语句 ↔ 源码读点\n")
        fh.write("# ★ 列：shape_id\tcalls\ttotal_exec_time_ms\tmean_exec_time_ms\trows\tsource_shape_id\tsource\tmap_reason\tcandidates\tfilter_cols\n")
        for r in map_rows:
            fh.write("\t".join([
                r["shape_id"], r["calls"], r["total_exec_time_ms"], r["mean_exec_time_ms"],
                r["rows"], r["source_shape_id"], r["source"], r["map_reason"], r["candidates"], r["filter_cols"],
            ]) + "\n")

    if not map_rows:
        die("★ 没解析出任何生产语句 ⇒ ⇒ 事实源是空的 ⇒ ⇒ 退出码非 0")
    matched = sum(1 for r in map_rows if r["source_shape_id"])
    print(f"★ 生产语句 = {len(map_rows)} 条；映射到源码形态 = {matched} 条")
    return 0


if __name__ == "__main__":
    sys.exit(main())
