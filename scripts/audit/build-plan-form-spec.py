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
            fp = statement_fingerprint(sql)
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
                "sql_order_cols": "",
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
