# -*- coding: utf-8 -*-
#!/usr/bin/env python3
"""extract-canonical-read-point-shapes.py —— R179 轮：从【活代码】里机械抽取
canonical 视图（request_logs_with_current_month）的读点 SQL，并把它们**按形态归组**。

# 这个工具解决什么

R174 登记了 35 个「受 canonical 视图无法分区裁剪影响」的生产文件；
`admin/canonical_read_points_registry_test.go` 的登记簿把它们拆成 **68 个读点**。
R179 要回答的是：**这 68 个读点，每一个「能不能用索引 seek」**。

★ 手抄 68 条 SQL 是不现实的（会抄错、且抄完无法复核）⇒
⇒ 所以本工具做**机械抽取**：
   ① 从 35 个文件里扫出**所有**提到 canonical 视图的 SQL 语句（Go raw string / 解释型字符串）；
   ② 把每个语句里的 `$1..$n` 占位符**归一**（⇒ 参数值不影响「形态」的判定）；
   ③ 折叠空白 + 小写 ⇒ 归一化成**形态键**；
   ④ 相同形态键 ⇒ 同一组。

★ 输出两份：
   - `--out-shapes`：形态册（每个形态一行：形态键 / 代表 SQL / 出现处 / 文件数 / 读点数）
   - `--out-points`：逐读点清单（每行一个读点：文件 / 行号 / 形态键 / SQL 摘要）

★ **不做**的事（本工具只抽，不判）：
   - 不判定「能不能 seek」⇒ ⇒ 那是 `cmd/tools/query_plan_form` 的事（要连库 EXPLAIN）。
   - 不判定读点的语义分类 ⇒ ⇒ 那是 `admin/canonical_read_points_registry_test.go` 的登记簿的事。
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
from collections import OrderedDict

CANONICAL_VIEW = "request_logs_with_current_month"

# ★ 退役的 v1 包装视图（迁移 890 已 DROP）⇒ **不是** canonical 视图的读点。
# ★ 判据用**标识符边界**（与仓内 sqlMentionsRelation 同款）。
RETIRED_WRAPPERS = (
    "request_logs_with_current_month_without_customer_id",
    "request_logs_with_current_month_without_request_class_due_at",
)

# ★ Go 源码里可能出现的视图名拼法：带 schema 前缀 / 带别名。
VIEW_RE = re.compile(r"\b(?:public\.)?" + re.escape(CANONICAL_VIEW) + r"\b")


def load_affected_files(repo_root: str) -> list[str]:
    """从 canonical 读点登记簿里取受影响文件清单（★ 单一事实源）。

    ★★ R179 轮修正（本轮自伤自愈）：★★ 原来这里硬编码「期望 35」⇒ ⇒ 上游新增一个
    ★★ 时间窗读点（`admin/topology_clients_today.go`）⇒ ⇒ 提取器**直接死掉**⇒ ⇒
    ★★ 产物再生成路径断了 ⇒ ⇒ 而**登记簿才是事实源**⇒ ⇒ 清单条数变了是【正常事件】，
    ★★ 不是缺陷 ⇒ ⇒ 现在改成【读登记簿，不设死数】⇒ ⇒ 死数只留在**产物头**里当【来源快照】。
    ★★ ⇒ ⇒ 上游再加读点，提取器不再死 ⇒ ⇒ 产物能不能跟上，由**漂移校验**去报。
    """
    path = os.path.join(repo_root, "admin", "canonical_read_points_registry_test.go")
    with open(path, encoding="utf-8") as fh:
        src = fh.read()
    marker = "var canonicalTimeWindowedReadPoints = map[string]string{"
    if marker not in src:
        die(f"登记簿里找不到 {marker} ⇒ 取不到受影响文件清单")
    blk = src.split(marker, 1)[1]
    blk = blk.split("\n}\n", 1)[0]
    files = re.findall(r'^\t"([^"]+)"', blk, re.M)
    if not files:
        die("登记簿里的受影响文件清单是空的 ⇒ 工具没有扫描面")
    return files


def load_file_list(path: str) -> list[str]:
    """读【钉住的文件清单】（★ 产物自证来源 ⇒ 漂移校验按它重放 ⇒ 不受上游新增影响）。"""
    with open(path, encoding="utf-8") as fh:
        files = [ln.strip() for ln in fh if ln.strip() and not ln.lstrip().startswith("#")]
    if not files:
        die(f"钉住的文件清单是空的：{path}")
    return files


def files_digest(files: list[str]) -> str:
    """文件清单的指纹（★ 排序后 sha256 前 12 位 ⇒ 写进产物头当【来源快照】）。"""
    h = hashlib.sha256()
    for f in sorted(files):
        h.update((f + "\n").encode("utf-8"))
    return h.hexdigest()[:12]


def strip_sql_comments(s: str) -> str:
    """去掉 -- 行注释与 /* */ 块注释（★ 注释里的视图名不是读点）。"""
    s = re.sub(r"/\*.*?\*/", " ", s, flags=re.S)
    s = re.sub(r"--[^\n]*", " ", s)
    return s


def extract_string_literals(src: str) -> list[tuple[int, str]]:
    """返回 [(行号, 字面量内容)]。

    ★ 只取两种 Go 字面量：
       - 反引号 raw string（`` ` `` … `` ` ``）：★ 生产 SQL 绝大多数是这种
       - 双引号解释型字符串：★ 拼接型 SQL 的片段
    ★ **必须**跳过 raw string 内部的反引号转义（Go raw string 里 `` `` `` 是转义）。
    """
    out: list[tuple[int, str]] = []
    i = 0
    n = len(src)
    line = 1
    while i < n:
        ch = src[i]
        if ch == "\n":
            line += 1
            i += 1
            continue
        # ── raw string ──
        if ch == "`":
            i += 1
            buf = []
            start_line = line
            while i < n:
                if src[i] == "`":
                    # Go raw string 里 `` `` 表示一个反引号
                    if i + 1 < n and src[i + 1] == "`":
                        buf.append("`")
                        i += 2
                        continue
                    i += 1
                    break
                if src[i] == "\n":
                    line += 1
                buf.append(src[i])
                i += 1
            out.append((start_line, "".join(buf)))
            continue
        # ── 解释型字符串 ──
        if ch == '"':
            i += 1
            buf = []
            start_line = line
            while i < n:
                if src[i] == "\\" and i + 1 < n:
                    buf.append(src[i + 1])
                    i += 2
                    continue
                if src[i] == '"':
                    i += 1
                    break
                if src[i] == "\n":
                    line += 1
                buf.append(src[i])
                i += 1
            out.append((start_line, "".join(buf)))
            continue
        i += 1
    return out


def _strip_strings(s: str) -> str:
    """把字符串字面量替换成同长度的占位（★ 保留偏移，★ 便于切分）。"""
    out = []
    i = 0
    n = len(s)
    while i < n:
        if s[i] == "'":
            j = i + 1
            while j < n:
                if s[j] == "'":
                    if j + 1 < n and s[j + 1] == "'":
                        j += 2
                        continue
                    j += 1
                    break
                j += 1
            out.append(" " * (j - i))
            i = j
            continue
        out.append(s[i])
        i += 1
    return "".join(out)


def _top_level_keyword_positions(masked: str, keyword: str) -> list[int]:
    """返回 masked 里【括号深度为 0】的 keyword 起始位置。"""
    positions = []
    depth = 0
    low = masked.lower()
    kw = keyword.lower()
    i = 0
    n = len(masked)
    while i < n:
        ch = masked[i]
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
        elif depth == 0 and low.startswith(kw, i):
            before = masked[i - 1] if i > 0 else " "
            after = masked[i + len(kw)] if i + len(kw) < n else " "
            if not (before.isalnum() or before == "_") and not (after.isalnum() or after == "_"):
                positions.append(i)
                i += len(kw)
                continue
        i += 1
    return positions


def plan_shape_sql(sql: str) -> str:
    """把 SQL 削成**计划形态**：删掉顶层 SELECT 列表，保留其余全部。

    ★ 为什么删 SELECT 列表：★ 计划形态由【关系 + 谓词 + 分组 + 排序 + 限行】决定，
      ★ **与投影哪些列无关** ⇒ ⇒ 删掉它才能把「同一个查询形状、不同投影」的读点并成一类。
    ★ 为什么**只**删顶层：★ CTE 体（WITH … AS (…)）里的谓词**也要保留**
      ⇒ ⇒ 顶层 SELECT 到顶层 FROM 之间那一段才是可以删的。
    """
    masked = _strip_strings(sql)
    selects = _top_level_keyword_positions(masked, "select")
    if not selects:
        return sql
    start = selects[0]
    froms = [p for p in _top_level_keyword_positions(masked, "from") if p > start]
    if not froms:
        return sql
    end = froms[0]
    return sql[:start] + " <select-list> " + sql[end:]


def normalize(sql: str) -> str:
    """把 SQL 归一化成**形态键**。

    ★ 归一化只做四件事（★ 每件都必须是**语义无关**的变换）：
       ① `$1..$n` → `?`（★ 参数值不影响「这条查询的形状」）
       ② 字符串字面量 → `''`（★ 字面量值不影响形状）
       ③ 顶层 SELECT 列表 → `<select-list>`（★ 投影列不影响计划形态）
       ④ 空白折叠 + 小写（★ 排版不影响形状）
    ★ **不**做的事：★ 不删列、不改别名、不重排 ⇒ ⇒ 否则【不同查询会被误并成一类】。
    """
    s = plan_shape_sql(sql)
    s = re.sub(r"\$([0-9]+)", "?", s)
    s = re.sub(r"'(?:[^']|'')*'", "''", s)
    s = re.sub(r"\s+", " ", s)
    return s.strip().lower()


# ── 谓词 / 排序列抽取（★ 台账要的两列）────────────────────────────────

_PRED_RE = re.compile(
    r"^\s*(?:[A-Za-z_][\w$]*\s*\.\s*)?([A-Za-z_][\w$]*)\s*(=|!=|<>|<|>|<=|>=|\bIN\b|\bLIKE\b|\bILIKE\b|\bBETWEEN\b|\bIS\b|\bIS NOT\b|@>|@<|\b=~)"
)


def _is_ident_char(ch: str) -> bool:
    return ch.isalnum() or ch == "_" or ch == "$"


def _split_top_level_kw(s: str, kw: str) -> list[str]:
    """按【顶层关键字】切分（★ 词边界判定，★ 不受分隔符两侧空格影响）。

    ★ 为什么不用「带空格的分隔符」：★ 那会把 `= $1 AND ts` 里的 `AND` 误判成
      「标识符内部」（★ `1` 也是 alnum）⇒ ⇒ 切不开 ⇒ ⇒ 谓词列只抽到第一个。
    ★ 这里改成：**关键字两侧都不能是标识符字符**（★ 与 _top_level_keyword_positions 同款）。
    """
    masked = _strip_strings(s)
    low = masked.lower()
    kwl = kw.lower()
    parts = []
    cur = []
    depth = 0
    i = 0
    n = len(masked)
    while i < n:
        ch = masked[i]
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
        if depth == 0 and low.startswith(kwl, i):
            before = masked[i - 1] if i > 0 else " "
            after = masked[i + len(kwl)] if i + len(kwl) < n else " "
            if not _is_ident_char(before) and not _is_ident_char(after):
                parts.append("".join(cur))
                cur = []
                i += len(kwl)
                continue
        cur.append(ch)
        i += 1
    parts.append("".join(cur))
    return [p.strip() for p in parts if p.strip()]


def _split_top_level(s: str, sep: str = " AND ") -> list[str]:
    """★ 兼容入口：把 " AND " 归一成关键字切分。"""
    return _split_top_level_kw(s, sep.strip())


def _find_where(sql: str) -> str:
    masked = _strip_strings(sql)
    positions = _top_level_keyword_positions(masked, "where")
    if not positions:
        return ""
    start = positions[0] + len("where")
    tail = masked[start:]
    # ★ WHERE 到下一个顶层子句为止
    ends = []
    for kw in ("group by", "order by", "limit", "offset", "having", "window", "union"):
        for p in _top_level_keyword_positions(masked, kw):
            if p >= start:
                ends.append(p)
    end = min(ends) if ends else len(masked)
    return sql[start:end]


def _find_order_by(sql: str) -> str:
    masked = _strip_strings(sql)
    positions = _top_level_keyword_positions(masked, "order by")
    if not positions:
        return ""
    start = positions[0] + len("order by")
    tail_masked = masked[start:]
    ends = []
    for kw in ("limit", "offset", "window", "union"):
        for p in _top_level_keyword_positions(masked, kw):
            if p >= start:
                ends.append(p)
    end = min(ends) if ends else len(masked)
    return sql[start:end]


def _all_keyword_positions(masked: str, keyword: str) -> list[int]:
    """返回 masked 里**任意深度**的 keyword 起始位置（★ 词边界判定）。"""
    positions = []
    low = masked.lower()
    kw = keyword.lower()
    i = 0
    n = len(masked)
    while i < n:
        if low.startswith(kw, i):
            before = masked[i - 1] if i > 0 else " "
            after = masked[i + len(kw)] if i + len(kw) < n else " "
            if not _is_ident_char(before) and not _is_ident_char(after):
                positions.append(i)
                i += len(kw)
                continue
        i += 1
    return positions


def _clause_text(masked: str, sql: str, keyword: str, stoppers: tuple[str, ...]) -> list[str]:
    """抽出【所有深度】上从 keyword 到下一个子句之间的文本。"""
    out = []
    for start in _all_keyword_positions(masked, keyword):
        body_start = start + len(keyword)
        ends = []
        for kw in stoppers:
            for p in _all_keyword_positions(masked, kw):
                if p >= body_start:
                    ends.append(p)
        end = min(ends) if ends else len(masked)
        out.append(sql[body_start:end])
    return out


def extract_filter_cols(sql: str) -> list[str]:
    """抽出 WHERE 里的过滤列（★ **全深度** ⇒ CTE 内的谓词也算；★ 去重、★ 小写）。"""
    masked = _strip_strings(sql)
    out: list[str] = []
    stoppers = ("group by", "order by", "limit", "offset", "having", "window", "union", "from")
    for where_text in _clause_text(masked, sql, "where", stoppers):
        for pred in _split_top_level_kw(where_text, "AND"):
            m = _PRED_RE.match(pred)
            if not m:
                continue
            col = m.group(1).lower()
            if col not in out:
                out.append(col)
    return out


def extract_order_cols(sql: str) -> str:
    """抽出 ORDER BY 的排序列序列（★ **全深度**；★ 带方向、★ 逗号分隔）。"""
    masked = _strip_strings(sql)
    out = []
    seen = set()
    for raw in _clause_text(masked, sql, "order by", ("limit", "offset", "window", "union")):
        for seg in raw.split(","):
            seg = seg.strip()
            m = re.match(r"^(?:[A-Za-z_][\w$]*\s*\.\s*)?([A-Za-z_][\w$]*)\s*(ASC|DESC)?$", seg, re.I)
            if not m:
                continue
            col = m.group(1).lower()
            direction = (m.group(2) or "").upper()
            token = f"{col} {direction}".strip()
            if token not in seen:
                seen.add(token)
                out.append(token)
    return ",".join(out)


def extract_param_roles(sql: str) -> list[dict]:
    """抽出每个 `$N` 占位符的**角色**（★ 实测时按角色取真实值）。

    ★ 角色 = （占位符序号, 它参与的谓词的列, 运算符）。
    ★ 例如 `api_key_id = $1 AND ts >= $2 AND ts < $3`
       ⇒ [{index:1,column:"api_key_id",op:"="},
          {index:2,column:"ts",op:">="},
          {index:3,column:"ts",op:"<"}]
    ★ 取**全深度**的谓词（★ 与 filter_cols 同口径）。
    """
    masked = _strip_strings(sql)
    stoppers = ("group by", "order by", "limit", "offset", "having", "window", "union", "from")
    roles: dict[int, dict] = {}
    for where_text in _clause_text(masked, sql, "where", stoppers):
        for pred in _split_top_level_kw(where_text, "AND"):
            m = _PRED_RE.match(pred)
            if not m:
                continue
            col = m.group(1).lower()
            op = m.group(2).upper()
            for pm in re.finditer(r"\$(\d+)", pred):
                idx = int(pm.group(1))
                if idx not in roles:
                    roles[idx] = {"index": idx, "column": col, "op": op}
    return [roles[k] for k in sorted(roles)]


def sql_digest(sql: str) -> str:
    """给审计档看的一行摘要（★ 去掉换行，截断）。"""
    flat = re.sub(r"\s+", " ", sql).strip()
    return flat[:400]


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--repo-root", default=os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..")))
    ap.add_argument("--out-shapes", required=True)
    ap.add_argument("--out-points", required=True)
    ap.add_argument("--json", default="")
    ap.add_argument(
        "--file-list",
        default="",
        help="★ 钉住的文件清单（★ 产物自证来源 ⇒ 漂移校验按它重放 ⇒ 上游新增读点不影响重放）",
    )
    args = ap.parse_args()

    root = args.repo_root
    files = load_file_list(args.file_list) if args.file_list else load_affected_files(root)
    digest = files_digest(files)

    shapes: "OrderedDict[str, dict]" = OrderedDict()
    points: list[dict] = []

    for rel in files:
        path = os.path.join(root, rel)
        if not os.path.exists(path):
            die(f"受影响文件不存在：{rel}")
        with open(path, encoding="utf-8") as fh:
            src = fh.read()
        for lineno, lit in extract_string_literals(src):
            clean = strip_sql_comments(lit)
            if not VIEW_RE.search(clean):
                continue
            # ★ 退役包装视图不是 canonical 视图的读点（标识符边界）
            if any(w in clean for w in RETIRED_WRAPPERS):
                continue
            norm = normalize(lit)
            if not norm:
                continue
            shape_id = shape_key(rel, lineno, norm)
            sh = shapes.get(shape_id)
            if sh is None:
                sh = {
                    "shape_id": shape_id,
                    "representative_file": rel,
                    "representative_line": lineno,
                    "representative_sql": lit.strip(),
                    "filter_cols": extract_filter_cols(lit),
                    "order_cols": extract_order_cols(lit),
                    "param_roles": extract_param_roles(lit),
                    # ★ dynamic = SQL 里有 fmt 占位（%s / %d …）⇒ 静态形态不可知 ⇒
                    #   ⇒ 这类读点**不能**直接拿去 EXPLAIN（★ 必须先解析 helper）。
                    "dynamic": bool(re.search(r"%[sdvq]", lit)),
                    "occurrences": [],
                    "files": [],
                }
                shapes[shape_id] = sh
            sh["occurrences"].append({"file": rel, "line": lineno})
            if rel not in sh["files"]:
                sh["files"].append(rel)

    for sh in shapes.values():
        sh["occurrence_count"] = len(sh["occurrences"])
        sh["file_count"] = len(sh["files"])

    for shape_id, sh in shapes.items():
        for occ in sh["occurrences"]:
            points.append(
                {
                    "file": occ["file"],
                    "line": occ["line"],
                    "shape_id": shape_id,
                    "sql_digest": sql_digest(sh["representative_sql"]),
                }
            )

    with open(args.out_shapes, "w", encoding="utf-8") as fh:
        fh.write("# canonical-read-point-shapes.tsv —— R179 轮：canonical 视图读点的**形态册**\n")
        fh.write("# ★ 生成器：scripts/audit/extract-canonical-read-point-shapes.py（★ 机械抽取，★ 非手抄）\n")
        fh.write("# ★ 口径：从 canonical 读点登记簿列出的受影响文件里，扫出所有提到 canonical 视图的 SQL 语句\n")
        fh.write(f"# ★ 来源快照：受影响文件 {len(files)} 个 · 指纹 {digest}（★ 漂移校验按此快照重放 ⇒ 上游新增读点不影响重放）\n")
        fh.write("# ★ 形态键 = 删掉顶层 SELECT 列表后的归一化 SQL（★ 计划形态只由 关系/谓词/分组/排序 决定）\n")
        fh.write("# ★ 列：shape_id\toccurrence_count\tfile_count\tdynamic\tfilter_cols\torder_cols\trepresentative_file:line\tsql_digest\n")
        for sh in shapes.values():
            fh.write(
                "\t".join(
                    [
                        sh["shape_id"],
                        str(sh["occurrence_count"]),
                        str(sh["file_count"]),
                        "dynamic" if sh["dynamic"] else "static",
                        ",".join(sh["filter_cols"]),
                        sh["order_cols"].replace(",", " "),
                        f"{sh['representative_file']}:{sh['representative_line']}",
                        sql_digest(sh["representative_sql"]).replace("\t", " "),
                    ]
                )
                + "\n"
            )

    with open(args.out_points, "w", encoding="utf-8") as fh:
        fh.write("# canonical-read-points.tsv —— R179 轮：逐读点清单\n")
        fh.write(f"# ★ 来源快照：受影响文件 {len(files)} 个 · 指纹 {digest}\n")
        fh.write("# ★ 列：file\tline\tshape_id\tsql_digest\n")
        for p in sorted(points, key=lambda x: (x["file"], x["line"])):
            fh.write(
                "\t".join(
                    [p["file"], str(p["line"]), p["shape_id"], p["sql_digest"].replace("\t", " ")]
                )
                + "\n"
            )

    if args.json:
        with open(args.json, "w", encoding="utf-8") as fh:
            json.dump(
                {
                    "_comment": [
                        "★ R179 轮：canonical 视图读点的**形态册**（机器抽取，★ 非手抄）。",
                        "★ 每个形态带【真实 SQL】+【参数角色】⇒ 实测工具按角色取真实值再 EXPLAIN。",
                        "★ dynamic=true 的形态含 fmt 占位（%s/%d）⇒ ★ 静态不可知 ⇒ 实测须先解析 helper。",
                        "★ source_files = 本册的【来源快照】（★ 受影响文件清单）⇒ 漂移校验按它重放。",
                    ],
                    "source_files": list(files),
                    "source_files_digest": digest,
                    "shapes": [
                        {
                            "shape_id": sh["shape_id"],
                            "file": sh["representative_file"],
                            "line": sh["representative_line"],
                            "occurrence_count": sh["occurrence_count"],
                            "file_count": sh["file_count"],
                            "dynamic": sh["dynamic"],
                            "sql": sh["representative_sql"],
                            "sql_filter_cols": sh["filter_cols"],
                            "sql_order_cols": sh["order_cols"],
                            "param_roles": sh["param_roles"],
                        }
                        for sh in shapes.values()
                    ],
                    "points": points,
                },
                fh,
                ensure_ascii=False,
                indent=2,
            )

    print(f"受影响文件 = {len(files)}")
    print(f"读点（SQL 语句出现处）= {len(points)}")
    print(f"形态 = {len(shapes)}")
    return 0


def shape_key(rel: str, lineno: int, norm: str) -> str:
    import hashlib

    h = hashlib.sha256(norm.encode("utf-8")).hexdigest()[:10]
    return f"{os.path.splitext(os.path.basename(rel))[0]}-{h}"


def die(msg: str) -> None:
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(2)


if __name__ == "__main__":
    sys.exit(main())
