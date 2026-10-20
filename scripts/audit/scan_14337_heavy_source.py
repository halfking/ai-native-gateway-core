#!/usr/bin/env python3
"""§143.37：全仓普查「同一份重型源在同一个 SQL 模板里被引用 ≥2 次」。

★ 背景（§143.36 已证）：P12 的根因不是「投影太宽」，而是
  **顶层 2 个标量子查询 ⇒ 同一份派生表被独立扫描 N 遍** ⇒ 物理读 ×N。
  ⇒ 这个形状**不只可能在 admin 包** ⇒ 本脚本扫全仓所有 .go 文件。

★ 判据口径（三条都是踩过坑换来的）：
  ① **限定在同一个 SQL 模板内** —— 按【文件】计数会把同文件的多个独立查询
     算在一起 ⇒ 恒红（§143.36 的量具坑）。
  ② **必须剥注释** —— 注释里的形态不产生 SQL ⇒ 不该算（§143.36.10）。
  ③ ★★ **必须做子串修正** —— `FROM public.session_turns` 是
     `FROM public.session_turns_hot` 的【子串】⇒ 朴素 count 把 hot 腿
     也计进 parent 腿 ⇒ **假命中**。
     ⇒ 口径：**parent 计数 = count(parent) − count(parent_hot)**（`_request_logs` 同理）。
     ⇒ 与 §143.36.8 记的「`AS r` 子串计数 50」是**同一族坑**。

用法：
    python3 scripts/audit/scan_14337_heavy_source.py [REPO_ROOT]
    ⇒ 退出码 0 = 产品代码 0 命中；1 = 有产品代码命中（需要处理）。
"""
import os
import re
import sys

# ── 剥注释（状态机版：认得解释字符串 / 原始字符串）────────────────────────
# ★ 正则会吃掉字符串字面量里的 `//` 与 `/* */`（URL 字面量会被整行吃掉）
#   ⇒ 必须状态机（与仓内 admin/request_logs_retirement_tiering_test.go 的
#   stripGoCommentsKeepingStrings 同款）。
def strip_go_comments(src: str) -> str:
    b = []
    rs = list(src)
    n = len(rs)
    i = 0
    while i < n:
        c = rs[i]
        if c in ('"', '`'):
            q = c
            b.append(c)
            i += 1
            while i < n:
                if rs[i] == "\\" and q != "`" and i + 1 < n:
                    b.append(rs[i]); b.append(rs[i + 1]); i += 2
                    continue
                b.append(rs[i])
                if rs[i] == q:
                    i += 1
                    break
                i += 1
        elif c == "/" and i + 1 < n and rs[i + 1] == "/":
            while i < n and rs[i] != "\n":
                i += 1
        elif c == "/" and i + 1 < n and rs[i + 1] == "*":
            j = i + 2
            while j + 1 < n and not (rs[j] == "*" and rs[j + 1] == "/"):
                j += 1
            i = j + 2 if j + 1 < n else n
        else:
            b.append(c)
            i += 1
    return "".join(b)


def sql_templates(src: str):
    """抽出所有 fmt.Sprintf(`...`) 的反引号实参。"""
    out = []
    rest = src
    while True:
        i = rest.find("fmt.Sprintf(`")
        if i < 0:
            return out
        rest = rest[i + len("fmt.Sprintf(`"):]
        j = rest.find("`")
        if j < 0:
            return out
        out.append(rest[:j])
        rest = rest[j + 1:]


# ── 重型源占位符（token, 需扣除的更长 token）────────────────────────────
TOKENS = [
    ("FROM %s", None),
    ("FROM public.session_turns_hot", None),
    ("FROM public.session_turns", "FROM public.session_turns_hot"),
    ("FROM public._request_logs_hot", None),
    ("FROM public._request_logs", "FROM public._request_logs_hot"),
    ("FROM request_logs_with_current_month", None),
    ("SessionFamilyTurnsSourceSQL()", None),
    ("SessionFamilyTurnsHotSourceSQL()", None),
    ("SessionFamilyBodiesSourceSQL()", None),
    ("boardRequestLogsFromClause(", None),
    ("requestLogsFromClause(", None),
]

SKIP_DIRS = {".git", "vendor", "node_modules", "testdata", "embeddata"}


def count_token(tmpl: str, tok: str, longer) -> int:
    c = tmpl.count(tok)
    if longer:
        c -= tmpl.count(longer)
    return c


def is_product_code(path: str) -> bool:
    """产品代码 = 不是 _test.go、且不在 tests/ 目录里。"""
    p = path.replace(os.sep, "/")
    if p.endswith("_test.go"):
        return False
    if "/tests/" in p or p.startswith("tests/"):
        return False
    return True


def main():
    root = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else ".")
    hits = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames
                       if d not in SKIP_DIRS and not (d.startswith(".") and d != ".")]
        for fn in filenames:
            if not fn.endswith(".go"):
                continue
            p = os.path.join(dirpath, fn)
            try:
                src = strip_go_comments(open(p, encoding="utf-8").read())
            except Exception:
                continue
            rel = os.path.relpath(p, root)
            for idx, tmpl in enumerate(sql_templates(src)):
                for tok, longer in TOKENS:
                    n = count_token(tmpl, tok, longer)
                    if n >= 2:
                        hits.append((rel, idx, tok, n, is_product_code(rel),
                                     re.sub(r"\s+", " ", tmpl[:110])))

    print("=== §143.37 全仓普查：同一 SQL 模板里重复引用同一份重型源 ===")
    print(f"扫描根: {root}   命中: {len(hits)}\n")
    prod = [h for h in hits if h[4]]
    for rel, idx, tok, n, prod_flag, head in sorted(hits, key=lambda x: (not x[4], x[0], x[1])):
        kind = "★★产品代码" if prod_flag else "  测试/工具"
        print(f"[{kind}] {rel}  模板#{idx}  token={tok!r} x{n}")
        print(f"          {head}")
    print()
    print(f"命中总数: {len(hits)}    ★产品代码命中数: {len(prod)}")
    if prod:
        print("⇒ ★★ 产品代码里还有「同一份重型源被引用 ≥2 次」的形态"
              "（这正是 P12 的失败形态 ⇒ 物理读 ×N）⇒ 需要合并成单遍。")
        return 1
    print("⇒ ★ 产品代码 0 命中 ⇒ 全仓产品代码里没有 P12 那种双扫形态。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
