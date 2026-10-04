#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""降级契约「元素集覆盖」量具（第八轮）。

它回答的问题
------------
`admin/degrade_marker_test.go` 是降级诚实性的主门。**这个门到底扫了哪些站点？**

主门的元素集由两件事决定，两者都会把「未知的」伪装成「已覆盖的」：

  1. 文件集：`collectGoFiles(".")` —— 测试的工作目录是 `admin/`，所以
     **只有 admin/ 及其子包在门内**。`domains/`、`db/` 整棵树不在。
  2. helper 集：`degradeHelpers = {"IsSchemaBehindError(", "IsMissingRelationError(err)"}`
     —— 硬编码字面量，而且**把实参写进了字面量**。于是
     `IsMissingRelationError(logsErr)` / `(scanErr)` / `(c.err)` 不匹配，
     既不计数也不进范围表。裸 `pgErr.Code == "42P01"` 更是谁都不匹配。

「门是绿的」与「门扫过那些文件」是两件事。本工具把元素集扩到**全仓**，
再与**活的门**（现场解析主门文件，不是我手抄的快照）求集合差。

为什么现场解析主门
------------------
主门此刻有 3 个并发会话在改（第八轮实测）。手抄它的范围表 = 抄一份马上
过期的快照，然后基于过期快照下结论。因此本工具**读它自己声明的**
`degradeHelpers` 与 `degradeMarkerOutOfScope`，据此判定覆盖。
解析不到任何一项时**退出码 2 并报缺哪一项**，绝不静默降级成
「什么都没覆盖」——那会让门变成永远红的噪声源，训练下一个人忽略它。

与主门的关系
------------
本工具**不改**主门，也**不复述**它的范围表；它只把缺口量化并钉住。
修主门的元素集是持有该文件的会话的活。

解析器纪律
----------
`strip_go_comments_keep_lines()` 是 admin/usage_ledger_sourceless_columns_test.go:227
那个 Go 状态机（code / lineComment / blockComment / rawString 四态）的**逐字节移植**。
必须同一套解析：注释里提到键名 ≠ 写了那个键。`--selfcheck` 里有专项断言。

已知的**继承缺陷**（不是本工具引入，如实登记）
------------------------------------------------
Go 版不识别**双引号解释型字符串**：`"https://x"` 里的 `//` 会被当成行注释起点，
吞掉**同一行**后半段（含可能的降级调用）——漏报方向。
本工具刻意保留同一缺陷（否则就成了「另一套解析」），并在 `--selfcheck` 里
把同行/换行两个方向都钉成期望值，使它可见而不是静默。

用法
----
    python3 docs/audit/verify/degrade-site-census.py             # 普查（退出码即门）
    python3 docs/audit/verify/degrade-site-census.py --selfcheck # 量具自证
    python3 docs/audit/verify/degrade-site-census.py --where raw-sqlstate-42p01
    python3 docs/audit/verify/degrade-site-census.py --json

退出码：0 绿 / 1 有未登记或已失效的登记项 / 2 解析主门失败或 selfcheck 失败
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys

# ── 仓库根：本文件在 docs/audit/verify/ 下，往上三层 ────────────────────────
ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", ".."))
MAIN_GATE = os.path.join(ROOT, "admin", "degrade_marker_test.go")

SKIP_DIRS = {
    ".git", ".venv", "node_modules", "vendor", "testdata", "dist", "build",
    ".idea", ".vscode", "coverage", "__pycache__",
}

# ── 降级判定族 ────────────────────────────────────────────────────────────
# helper 族**不带实参**：主门写死 `IsMissingRelationError(err)` 导致
# `IsMissingRelationError(logsErr)` 漏配，这正是本工具要暴露的缺口之一。
FAMILIES = [
    ("missing-relation-helper", "42P01 共享判定 helper（任意实参）",
     ["IsMissingRelationError("]),
    ("schema-behind-helper", "缺表/缺列合并判定 helper（任意实参）",
     ["IsSchemaBehindError("]),
    ("raw-sqlstate-42p01", "裸 SQLSTATE 字面量（不经 helper）",
     ['"42P01"']),
]

# ── 登记表：**只登记「不在主门元素集内」的缺口** ─────────────────────────────
# 纪律：每条都要能回答「谁在守它」。答不上来的不许进表 ——
# 豁免表一旦只写「内部」「不影响页面」就会退化成没人核的散文。
# GAP_REGISTERED：登记「**主门看不见**的降级缺口」—— 键 = 仓库相对路径，
# 值 = (理由, 谁在守)。
#
# 2026-10-03（第八轮）本表**已清空**，而且这是本轮最重要的一个数：
# 主门把三种元素集形状（文件集 / needle 集 / 本地谓词间接）都收进去之后，
# 全仓 21 个命中文件**一个不漏**，缺口 0。
#
# 为什么登记要「只在缺口真实存在时有效」：本轮真踩过一个恒绿洞 ——
# 把第三种形状的检测器关掉，cache.go 从「门已覆盖」退回「缺口」，
# 而它早已登记在表里 ⇒ 旧判据（文件是否还存在）看不出变化 ⇒ 工具报绿。
# 「表里有」会把检测器的退化藏起来。现在 stale 判据是
# 「**登记项是否仍是缺口**」，门一旦收进去，表项即失效并报红。
#
# 纪律不变：每条都要能回答「谁在守它」，答不上来的不许进表。
GAP_REGISTERED: dict[str, tuple[str, str]] = {
    # 当前为空。缺口真的回来时（门被改窄、或某棵新树进入仓库），
    # 本工具会把它指出来，那时在这里登记理由 + 把守者。
}


# ── 解析器：对 Go 状态机的逐字节移植 ───────────────────────────────────────
def strip_go_comments_keep_lines(src: str) -> str:
    """去掉 Go 注释但保留换行（行号不变），不碰反引号原始字符串内容。

    与 admin/usage_ledger_sourceless_columns_test.go:227 同源。
    刻意**不**识别双引号解释型字符串 —— 见模块 docstring「已知的继承缺陷」。
    """
    out: list[str] = []
    CODE, LINE, BLOCK, RAW = 0, 1, 2, 3
    state = CODE
    i, n = 0, len(src)
    while i < n:
        c = src[i]
        if state == CODE:
            if c == "/" and i + 1 < n and src[i + 1] == "/":
                state = LINE
                i += 2
                continue
            if c == "/" and i + 1 < n and src[i + 1] == "*":
                state = BLOCK
                i += 2
                continue
            if c == "`":
                state = RAW
            out.append(c)
        elif state == LINE:
            if c == "\n":
                state = CODE
                out.append(c)
        elif state == BLOCK:
            if c == "\n":
                out.append(c)          # 保留行号
            if c == "*" and i + 1 < n and src[i + 1] == "/":
                state = CODE
                i += 2
                continue
        else:  # RAW
            out.append(c)
            if c == "`":
                state = CODE
        i += 1
    return "".join(out)


_FUNC_RE = re.compile(r"^\s*func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*\(")


def _enclosing_predicate(lines: list[str], idx: int) -> str:
    """向上找最近的 `func Name(`：判断该命中是「判定函数定义」还是「控制流分支」。"""
    for j in range(idx, -1, -1):
        m = _FUNC_RE.match(lines[j])
        if m:
            return m.group(1)
    return ""


def classify_line(trimmed: str) -> str:
    """主门同款排除规则：函数声明与 return 判断表达式不是「绕过标记的写入点」。

    但它们是「这一族在这个文件里存在」的证据，所以照样计数，只标 kind。
    """
    if trimmed.startswith("func ") or trimmed.startswith("return "):
        return "definition"
    return "site"


def _looks_like_control_flow(ln: str) -> bool:
    t = ln.strip()
    if t.startswith(("//", "*", "func ")):
        return False
    for kw in ("if ", "for ", "switch ", "case ", "&&", "||", "return ", "else if "):
        if kw in ln:
            return True
    return False


def indirect_sites(code: str, needles: list[str]) -> list[tuple[int, str]]:
    """第三种形状：本地谓词间接。移植自 admin/degrade_marker_test.go 的
    indirectDegradeSites（同一份实现，同一处取舍）。

    小写谓词的**区域**内含 needle，且该谓词在**控制流里**被调用 ——
    于是调用那一行本身不含任何 needle，前两种形状都看不见它。
    真实对象：domains/credentialstate/cache.go 的 isUndefinedTable。

    ★ 必须与 Go 门同口径，否则本工具会把「门已经收进去了」的文件
      继续报成缺口（假警报），或反过来把门没管的当已覆盖。
    """
    lines = code.split("\n")
    decl_re = re.compile(r"^func ([a-z]\w*)\(")
    preds: list[tuple[str, int, int]] = []
    for i, ln in enumerate(lines):
        m = decl_re.match(ln)
        if m is None:
            continue
        end = len(lines)
        for j in range(i + 1, len(lines)):
            if lines[j].startswith("func "):
                end = j
                break
        # 区域边界取下一个顶层 func，而不是第一个 `}` ——
        # 用 `}` 会在函数内含闭包时提前截断，把 needle 判成不在体内 ⇒ 漏报。
        if any(nd in lines[j] for j in range(i + 1, end) for nd in needles):
            preds.append((m.group(1), i, end))
    out: list[tuple[int, str]] = []
    for name, start, end in preds:
        call_re = re.compile(r"\b" + re.escape(name) + r"\(")
        for i, ln in enumerate(lines):
            if start < i <= end:
                continue
            if call_re.search(ln) and _looks_like_control_flow(ln):
                out.append((i + 1, name))
    return out


def scan_file(path: str, gate_needles: list[str] | None = None) -> dict:
    with open(path, "r", encoding="utf-8", errors="replace") as fh:
        code = strip_go_comments_keep_lines(fh.read())
    lines = code.split("\n")
    hits: dict[str, list[dict]] = {}
    for fam_id, _desc, needles in FAMILIES:
        for i, ln in enumerate(lines):
            if not any(nd in ln for nd in needles):
                continue
            hits.setdefault(fam_id, []).append({
                "line": i + 1,
                "kind": classify_line(ln.strip()),
                "predicate": _enclosing_predicate(lines, i),
                # 去注释后的原始行：主门的 contains 判定用的就是它
                "_line": ln,
            })
    # 第三种形状。families 相同 ⇒ 与主门一样，间接站点的行仍会落进某个族，
    # 但它们不匹配 needle（这正是问题所在），所以必须单独记账。
    if gate_needles is not None:
        for line, name in indirect_sites(code, gate_needles):
            hits.setdefault("indirect-predicate", []).append({
                "line": line,
                "kind": "indirect",
                "predicate": name,
                "_line": lines[line - 1] if 0 < line <= len(lines) else "",
            })
    return hits


def walk_go_files(root: str):
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS and not d.startswith(".")]
        for fn in sorted(filenames):
            if fn.endswith(".go") and not fn.endswith("_test.go"):
                full = os.path.join(dirpath, fn)
                yield full, os.path.relpath(full, root).replace(os.sep, "/")


def census(root: str = ROOT, gate_needles: list[str] | None = None) -> dict:
    files: dict[str, dict] = {}
    for full, rel in walk_go_files(root):
        hits = scan_file(full, gate_needles)
        if hits:
            files[rel] = hits
    return files


# ── 现场解析主门，得出它**自己声明**的元素集 ───────────────────────────────
class GateShapeError(RuntimeError):
    pass


def _go_string_literals(block: str) -> list[str]:
    """取一段 Go 源码里的全部字符串字面量（双引号与反引号两种）。

    为什么不能用 `"([^"]*)"`：degradeHelpers 里第三条是**反引号原始字符串**
    `` `"42P01"` ``，双引号正则会从它内部啃出 `42P01`（丢了外层引号），
    于是本工具的匹配比主门**更宽** —— 工具报的「已覆盖」就不再等于主门
    真的覆盖。量具与被判据必须**同一口径**，宽一点窄一点都不行。
    """
    out: list[str] = []
    i, n = 0, len(block)
    while i < n:
        c = block[i]
        if c == "`":
            j = block.find("`", i + 1)
            if j < 0:
                break
            out.append(block[i + 1:j])
            i = j + 1
        elif c == '"':
            j = i + 1
            buf: list[str] = []
            while j < n and block[j] != '"':
                if block[j] == "\\" and j + 1 < n:
                    buf.append(block[j + 1])
                    j += 2
                else:
                    buf.append(block[j])
                    j += 1
            out.append("".join(buf))
            i = j + 1
        else:
            i += 1
    return out


def load_main_gate(path: str = MAIN_GATE) -> dict:
    """读主门文件，取出它的 walk 根、helper 字面量、范围表文件列表。

    任何一项取不到 ⇒ GateShapeError（退出码 2）。**绝不**静默退化成
    「什么都没覆盖」：那会让门恒红，而恒红的门比没有门更坏。
    """
    if not os.path.exists(path):
        raise GateShapeError(f"主门文件不存在：{path}")
    with open(path, "r", encoding="utf-8", errors="replace") as fh:
        raw = fh.read()
    # ⚠ 一律在**去注释后**的源码上提取。本轮真踩过：主门的 doc comment 里
    # 写着 `collectGoFiles(".")`（解释历史），而真实调用已改成 `".."`；
    # 工具从注释里取到旧值 ⇒ 覆盖前缀算成 admin/ ⇒ 一批文件被误判成缺口。
    # 「注释里提到 ≠ 代码里写了」这条，本工具自己也得守。
    src = strip_go_comments_keep_lines(raw)

    m = re.search(r"collectGoFiles\(\s*\"([^\"]*)\"\s*\)", src)
    if not m:
        raise GateShapeError("取不到 collectGoFiles(\"...\")：主门改了遍历入口？")
    walk_arg = m.group(1)

    m = re.search(r"degradeHelpers\s*=\s*\[\]string\{(.*?)\}", src, re.S)
    if not m:
        raise GateShapeError("取不到 degradeHelpers：主门改了 helper 名单的写法？")
    needles = _go_string_literals(m.group(1))
    if not needles:
        raise GateShapeError("degradeHelpers 解析出 0 个字面量：写法已变？")

    m = re.search(r"degradeMarkerOutOfScope\s*=\s*map\[string\]string\{(.*?)\n\}", src, re.S)
    if not m:
        raise GateShapeError("取不到 degradeMarkerOutOfScope：主门改了范围表写法？")
    scope = sorted(set(re.findall(r'^\s*"([^"]+\.go)"\s*:', m.group(1), re.M)))
    if not scope:
        raise GateShapeError("degradeMarkerOutOfScope 解析出 0 个文件：写法已变？")

    # collectGoFiles 的实参决定**文件集**：
    #   "."  → 测试工作目录（= 包目录 admin/）⇒ 覆盖前缀 "admin/"
    #   ".." → 仓库根 ⇒ 覆盖前缀 ""（本工具的 rel 本身就是仓库相对路径）
    # ⚠ 两版都踩过这里：先把 "." 算成 ""（恒真，缺口全靠 helper 条件碰巧抓到），
    # 后来若把 ".." 当字面前缀 "../"，则 rel.startswith("../") 恒**假** ⇒
    # 全仓每个文件都被算成缺口 ⇒ 门恒红。恒真的条件连门都算不上，恒假的更糟。
    if walk_arg in (".", ""):
        walk_prefix = "admin/"
        walk_note = 'collectGoFiles(".") 的工作目录是包目录 admin/'
    elif walk_arg == "..":
        walk_prefix = ""
        walk_note = 'collectGoFiles("..") = 仓库根，覆盖全仓'
    else:
        walk_prefix = walk_arg.rstrip("/") + "/"
        walk_note = f"collectGoFiles({walk_arg!r})：未识别的遍历根，按字面处理（请核对）"
    return {"walk_arg": walk_arg, "walk_prefix": walk_prefix, "walk_note": walk_note,
            "needles": needles, "scope": scope}


def main_gate_covers(rel: str, hits: dict, gate: dict) -> bool:
    """主门会真正检查这个文件吗？

    两个条件都要满足，缺一即为缺口：
      · 文件在 walk 根之内（admin/ 及子包）
      · 至少有一处 **site** 形态的命中，匹配主门自己声明的 helper 字面量
    （site 形态 = 非 func 声明、非 return 表达式 —— 与主门排除规则一致）
    """
    if not rel.startswith(gate["walk_prefix"]):
        return False
    # 第三种形状：间接站点同样让文件进入元素集（Go 门已如此）。
    if hits.get("indirect-predicate"):
        return True
    for _fam, sites in hits.items():
        if _fam == "indirect-predicate":
            continue
        for s in sites:
            # 与主门 strings.Contains(ln, helper) 同形：字面量里带着实参
            # （"IsMissingRelationError(err)"），所以非 err 实参天然不匹配 ——
            # 这正是本工具要量化的那个缺口，不能帮主门把它补上。
            if s["kind"] == "site" and any(nd in s["_line"] for nd in gate["needles"]):
                return True
    return False


# ── 门：缺口 = 全仓命中文件 − 主门覆盖文件 ────────────────────────────────
def verdict(files: dict, gate: dict, registered: dict | None = None) -> dict:
    reg = GAP_REGISTERED if registered is None else registered

    covered, gaps = [], []
    for rel, hits in sorted(files.items()):
        (covered if main_gate_covers(rel, hits, gate) else gaps).append(rel)

    unregistered = []
    for rel in gaps:
        if rel in reg:
            continue
        detail = ", ".join(f"{f}×{len(v)}" for f, v in sorted(hits_of(files, rel).items()))
        unregistered.append(f"{rel}  [{detail}]")

    # ⚠ 判据必须是「**是否仍是缺口**」，不是「文件是否还存在」。
    # 本轮真踩过：把第三种形状的检测器关掉，cache.go 从「门已覆盖」退回
    # 「缺口」，而它**早已登记在表里** ⇒ 旧判据（r not in files）看不到变化 ⇒
    # 工具 exit 0 报绿，而真值是「检测器坏了」。
    # 登记的意义是「门看不见的缺口」；门一旦收进去，表项就该失效并报红，
    # 否则这张表会变成掩盖检测器退化的免死金牌。
    stale = [f"{r}  已登记为缺口，但主门现在**覆盖**它了 —— "
             f"该表项已失效（请删表项；缺口真的回来时会重新要求登记）"
             for r in sorted(reg) if r not in gaps]

    per_family_sites = {f[0]: 0 for f in FAMILIES}
    per_family_files = {f[0]: 0 for f in FAMILIES}
    per_family_sites["indirect-predicate"] = 0
    per_family_files["indirect-predicate"] = 0
    for hits in files.values():
        for fam, sites in hits.items():
            per_family_sites[fam] = per_family_sites.get(fam, 0) + len(sites)
            per_family_files[fam] = per_family_files.get(fam, 0) + 1

    # 站内未覆盖站点：文件被主门覆盖（有别的命中匹配了字面量），但**这一处**
    # 匹配不上 —— 典型是实参不是 `err`。主门是文件级判定，所以它看不见这一层。
    # 这不是「未登记的缺口」，而是范围表计数的口径问题（见第八轮 §1.3）。
    site_gaps: list[str] = []
    for rel in covered:
        for fam, sites in sorted(files[rel].items()):
            for s in sites:
                if s["kind"] != "site":
                    continue
                if any(nd in s["_line"] for nd in gate["needles"]):
                    continue
                site_gaps.append(
                    f"{rel}:{s['line']}  [{fam}]  {s['_line'].strip()[:90]}")

    return {
        "code": 1 if (unregistered or stale) else 0,
        "unregistered": unregistered,
        "stale": stale,
        "covered": covered,
        "gaps": gaps,
        "site_gaps": site_gaps,
        "per_family_sites": per_family_sites,
        "per_family_files": per_family_files,
        "files": files,
    }


def hits_of(files: dict, rel: str) -> dict:
    return files.get(rel, {})


# ── selfcheck ──────────────────────────────────────────────────────────────
def selfcheck() -> int:
    fails: list[str] = []

    def ok(cond: bool, label: str, detail: str = "") -> None:
        if not cond:
            fails.append(f"{label}{(' :: ' + detail) if detail else ''}")

    # 1) 解析器夹具
    fx = 'a := `raw // not a comment` // real\n/* block\n   still */ b := 1\nc := 2\n'
    st = strip_go_comments_keep_lines(fx)
    ok("raw // not a comment" in st, "夹具1 反引号内的 // 必须保留")
    ok("real" not in st, "夹具2 行注释必须去掉")
    ok("block" not in st and "still" not in st, "夹具3 块注释必须去掉")
    ok(st.count("\n") == fx.count("\n"), "夹具4 行数不变（行号可读）")
    ok("b := 1" in st and "c := 2" in st, "夹具5 注释后的代码必须保留")

    # 2) 继承缺陷：同行才触发。双向钉，避免过度声称。
    same = strip_go_comments_keep_lines('u := "https://x/y" ; IsMissingRelationError(err)\n')
    ok("IsMissingRelationError" not in same, "夹具6a 同行：字符串里的 // 吞掉行尾（已知缺陷）", repr(same))
    nxt = strip_go_comments_keep_lines('u := "https://x/y"\nIsMissingRelationError(err)\n')
    ok("IsMissingRelationError" in nxt, "夹具6b 换行：下一行的调用必须存活（防过度声称）", repr(nxt))

    # 3) 人工地锚（正例）
    got = census(ROOT)   # 锚点只关心直接族；间接族需要 gate needles，见下一步
    for rel, fam in {
        "domains/authentication/verifier.go": "raw-sqlstate-42p01",
        "domains/credentialstate/cache.go": "raw-sqlstate-42p01",
        "admin/auto_route_outcome_freshness.go": "raw-sqlstate-42p01",
        "db/db.go": "raw-sqlstate-42p01",
        "admin/usage_credits.go": "missing-relation-helper",
    }.items():
        ok(rel in got and fam in got[rel], f"地锚(正) {rel} 命中 {fam}", f"实到 {sorted(got.get(rel, {}))}")

    # 4) 人工地锚（反向样本）：只在注释里提到 ⇒ 必须 0
    tmp = os.path.join(ROOT, "docs", "audit", "verify", ".selfcheck-fixture.go")
    with open(tmp, "w", encoding="utf-8") as fh:
        fh.write('package x\n\n// 解释 42P01 与 IsMissingRelationError(err) 的关系。\nfunc F() {}\n')
    try:
        h = scan_file(tmp)
        ok(h == {}, "地锚(反) 注释里提到族名不得算命中", f"实到 {h}")
    finally:
        os.unlink(tmp)

    # 5) 主门现场解析（取不到就是退出码 2 的事）
    try:
        gate = load_main_gate()
        ok(len(gate["needles"]) >= 2, "主门 helper 名单解析出 ≥2 项", str(gate["needles"]))
        ok(len(gate["scope"]) >= 5, "主门范围表解析出 ≥5 个文件", str(len(gate["scope"])))
    except GateShapeError as e:
        fails.append(f"主门解析失败：{e}")

    # 5b) ★ 本轮真踩过的两个解析缺陷，各自钉一条。
    #     它们此前**没有任何门守着** —— 工具照样 exit 0，只是默默给错答案。
    #     (a) 注释里提到 ≠ 代码里写了：主门 doc comment 里留着历史的
    #         `collectGoFiles(".")`，真实调用已是 `".."`。不去注释就取到旧值。
    ok(gate["walk_arg"] != ".",
       "夹具8a 遍历根必须取自**代码**而非注释（主门注释里留着 collectGoFiles(\".\")）",
       f"取到 walk_arg={gate['walk_arg']!r}")
    # (b) 裸 SQLSTATE needle 在 Go 源码里是**反引号原始字符串**：
    #     `"42P01"`。用双引号正则解析会丢掉外层引号，工具的匹配就比主门宽。
    quoted = [n for n in gate["needles"] if "42P01" in n]
    ok(bool(quoted) and all(n.startswith('"') and n.endswith('"') for n in quoted),
       "夹具8b 42P01 needle 必须连外层引号一起取到（反引号原始字符串）",
       f"needles={gate['needles']}")

    # 5c) 解析器单元：反引号 vs 双引号两种字面量
    lits = _go_string_literals('`"42P01"`, "IsMissingRelationError("')
    ok(lits == ['"42P01"', "IsMissingRelationError("],
       "夹具8c _go_string_literals 必须同时认反引号与双引号字面量", f"实到 {lits}")

    # 5d) 第三种形状（本地谓词间接）的检测器自证 + 两个阴性样本。
    #     真实对象是 domains/credentialstate/cache.go 的 isUndefinedTable ——
    #     它按前两种形状算下来「没有降级站点」，全靠这一条进元素集。
    real = os.path.join(ROOT, "domains", "credentialstate", "cache.go")
    if os.path.exists(real):
        rcode = strip_go_comments_keep_lines(open(real, encoding="utf-8").read())
        rsites = indirect_sites(rcode, gate["needles"])
        ok(len(rsites) >= 2, "夹具9a cache.go 的 isUndefinedTable 间接站点必须被检出",
           f"实到 {rsites}")
    neg_not_control = (
        'package p\n\nfunc isGone(err error) bool {\n'
        '\treturn err != nil && string(err) == "42P01"\n}\n\n'
        'var _ = isGone\n')
    ok(indirect_sites(strip_go_comments_keep_lines(neg_not_control), gate["needles"]) == [],
       "夹具9b 谓词未在控制流里被调用 ⇒ 不得报站点",
       f"实到 {indirect_sites(strip_go_comments_keep_lines(neg_not_control), gate['needles'])}")
    neg_comment_only = (
        'package p\n\nfunc isGone(err error) bool {\n'
        '\t// 曾经这里判 42P01\n\treturn err != nil\n}\n\n'
        'func f(err error) bool {\n\tif isGone(err) {\n\t\treturn true\n\t}\n\treturn false\n}\n')
    ok(indirect_sites(strip_go_comments_keep_lines(neg_comment_only), gate["needles"]) == [],
       "夹具9c 注释里的 42P01 不得让谓词成为降级谓词",
       f"实到 {indirect_sites(strip_go_comments_keep_lines(neg_comment_only), gate['needles'])}")

    # 6) 反向对照（防恒绿 + 防恒红）：未登记 ⇒ 红；登记后 ⇒ 绿
    #
    # ⚠ 两个夹具陷阱，本轮都踩了：
    #  (a) 整个调用写在 `func f(...) { ... }` 那一行会被 classify_line 判成
    #      definition（主门同款规则），于是「红」是因为没有 site 命中，
    #      不是因为未登记 —— 门因为错误的原因通过。
    #  (b) 用 helper 形状的夹具 + 只认 helper 的假门 ⇒ 文件被判**已覆盖**，
    #      登记表根本没被测到。测登记表必须先让文件成为**缺口**。
    #  这里用 raw 42P01 形状（取自 auto_route_outcome_freshness.go 的真实写法），
    #  假门只认 helper ⇒ 该文件必为缺口，红绿才由登记表驱动。
    RAW_BODY = (
        'package pkgx\n\n'
        'func f(err error) bool {\n'
        '\tvar pgErr *pgconn.PgError\n'
        '\tif errors.As(err, &pgErr) && pgErr.Code == "42P01" {\n'
        '\t\treturn true\n'
        '\t}\n'
        '\treturn false\n'
        '}\n'
    )
    SITE_BODY = (
        'package pkgx\n\n'
        'func f(err error) bool {\n'
        '\tif IsMissingRelationError(err) {\n'
        '\t\treturn true\n'
        '\t}\n'
        '\treturn false\n'
        '}\n'
    )
    reg2: dict[str, tuple[str, str]] = {}
    fake = os.path.join(ROOT, "docs", "audit", "verify", ".selfcheck-root")
    os.makedirs(os.path.join(fake, "admin", "pkgx"), exist_ok=True)
    with open(os.path.join(fake, "admin", "pkgx", "a.go"), "w", encoding="utf-8") as fh:
        fh.write(RAW_BODY)
    try:
        f_files = census(fake)
        f_gate = {"walk_prefix": "admin/", "needles": ["IsMissingRelationError(err)"], "scope": []}
        rel2 = "admin/pkgx/a.go"
        # 先自证夹具与「它必为缺口」这两个前提，否则下面的红绿都没有意义
        sites = [s for ss in f_files.get(rel2, {}).values() for s in ss if s["kind"] == "site"]
        ok(len(sites) == 1, "夹具自证：RAW_BODY 必须产生恰好 1 处 site 命中",
           f"实到 {[(s['kind'], s['line']) for ss in f_files.get(rel2, {}).values() for s in ss]}")
        v0 = verdict(f_files, f_gate, reg2)
        ok(rel2 in v0["gaps"], "夹具自证：raw 形状 + 只认 helper 的门 ⇒ 必须先是缺口",
           f"covered={v0['covered']} gaps={v0['gaps']}")
        v1 = verdict(f_files, f_gate, reg2)
        ok(v1["code"] == 1, "反向对照：未登记的降级站点必须报红（防恒绿）", str(v1["unregistered"]))
        ok(any(rel2 in u for u in v1["unregistered"]),
           "反向对照：报红时必须点名那个文件", str(v1["unregistered"]))
        reg2[rel2] = ("夹具", "夹具")
        v2 = verdict(f_files, f_gate, reg2)
        ok(v2["code"] == 0, "反向对照：登记后必须转绿（防恒红）",
           f"unreg={v2['unregistered']} stale={v2['stale']}")
    finally:
        for dirpath, dirnames, filenames in os.walk(fake, topdown=False):
            for fn in filenames:
                os.unlink(os.path.join(dirpath, fn))
            if dirnames or filenames:
                os.rmdir(dirpath)

    # 7) 双向：登记了但代码里没有 ⇒ 红（防表项退化成散文）
    v3 = verdict({}, {"walk_prefix": "admin/", "needles": [], "scope": []},
                 {"pkgx/ghost.go": ("凭印象", "none")})
    ok(v3["code"] == 1 and any("ghost.go" in s for s in v3["stale"]),
       "双向：登记了但代码里没有 ⇒ 报红", str(v3["stale"]))

    # 7b) ★ 这条让「stale 判据 = 是否仍是缺口」真正承重。
    #     登记表**当前是空的**，所以新旧两种判据在真实运行里完全等价 ——
    #     也就是说只靠真实运行，把判据退回旧版（文件是否存在）根本不会变红。
    #     一个「改了也不会有人发现」的判据等于没有判据，所以这里造一个非空
    #     登记表专测它：verifier.go 是**真实存在**的文件（旧判据看不出问题），
    #     但它已被主门覆盖（新判据必须判它表项失效）。
    try:
        live_gate = load_main_gate()
        live = census(ROOT, live_gate["needles"])
        v10 = verdict(live, live_gate,
                      {"domains/authentication/verifier.go": ("已被门覆盖的假缺口", "x")})
        ok(v10["code"] == 1 and any("verifier.go" in s for s in v10["stale"]),
           "夹具10 stale 判据必须是「是否仍是缺口」而非「文件是否存在」",
           f"stale={v10['stale']}")
    except GateShapeError as e:
        fails.append(f"夹具10 前置失败：{e}")

    # 8) **文件集条件不得恒真**（本轮真踩的 bug：前缀算成 ""，条件形同虚设）。
    #    同一份内容分别放在 admin/ 内与外：门内必须覆盖、门外必须缺口。
    #    两侧判定相同 ⇒ 这个条件没起作用，工具的「缺口」只是 helper 条件碰巧抓到的。
    body = SITE_BODY
    base = os.path.join(ROOT, "docs", "audit", "verify", ".selfcheck-root2")
    for sub in ("admin/pkgx", "outside/pkgx"):
        os.makedirs(os.path.join(base, sub), exist_ok=True)
        with open(os.path.join(base, sub, "a.go"), "w", encoding="utf-8") as fh:
            fh.write(body)
    try:
        f2 = census(base)
        g2 = {"walk_prefix": "admin/", "needles": ["IsMissingRelationError(err)"], "scope": []}
        v4 = verdict(f2, g2, {})
        ok("admin/pkgx/a.go" in v4["covered"],
           "文件集(正) admin/ 内且命中字面量 ⇒ 必须判为已覆盖", str(v4["covered"]))
        ok("outside/pkgx/a.go" in v4["gaps"],
           "文件集(反) admin/ 外即便命中字面量也必须判为缺口", str(v4["gaps"]))
        # 再证一次：把前缀退回 ""（曾经的 bug），两者应当**同时**变成 covered ——
        # 证明确实是前缀在起作用，而不是别的什么。
        v5 = verdict(f2, dict(g2, walk_prefix=""), {})
        ok("outside/pkgx/a.go" in v5["covered"],
           "文件集(反证) 前缀为 \"\" 时门外文件被误判为已覆盖 —— "
           "这正是本轮修掉的那个恒真条件", str(v5["covered"]))
    finally:
        for dirpath, dirnames, filenames in os.walk(base, topdown=False):
            for fn in filenames:
                os.unlink(os.path.join(dirpath, fn))
            if dirnames or filenames:
                os.rmdir(dirpath)

    for f in fails:
        print(f"  ✗ {f}", file=sys.stderr)
    return 1 if fails else 0


# ── main ───────────────────────────────────────────────────────────────────
def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--selfcheck", action="store_true")
    ap.add_argument("--json", action="store_true")
    ap.add_argument("--where", help="打印某族在每个文件里的 file:line")
    args = ap.parse_args()

    if args.selfcheck:
        rc = selfcheck()
        print("selfcheck: " + ("PASS" if rc == 0 else "FAIL"))
        return rc

    try:
        gate = load_main_gate()
    except GateShapeError as e:
        print(f"✗ {e}\n  拒绝在读不到主门元素集的情况下给结论 —— "
              f"那会把「读不到」报成「全都缺」。", file=sys.stderr)
        return 2

    files = census(ROOT, gate["needles"])

    if args.where:
        fam = args.where
        if fam not in dict((f[0], f) for f in FAMILIES):
            print(f"未知族 {fam}；可选：{[f[0] for f in FAMILIES]}", file=sys.stderr)
            return 2
        for rel in sorted(files):
            for s in files[rel].get(fam, []):
                print(f"{rel}:{s['line']}  [{s['kind']}]  {s['predicate']}")
        return 0

    v = verdict(files, gate)

    if args.json:
        slim = {r: {f: [{kk: vv for kk, vv in s.items() if kk != "_line"} for s in ss]
                    for f, ss in h.items()} for r, h in files.items()}
        print(json.dumps({k: val for k, val in v.items() if k != "files"} | {"files": slim},
                         ensure_ascii=False, indent=2))
        return v["code"]

    print("降级判定族普查 —— 元素集 = 全仓，与活的门求集合差")
    print(f"  仓库根            {ROOT}")
    print(f"  主门 walk 根      {gate['walk_arg']!r} → 覆盖前缀 {gate['walk_prefix']!r}  ({gate['walk_note']})")
    print(f"  主门 helper 字面量 {gate['needles']}")
    print(f"  主门范围表        {len(gate['scope'])} 个文件")
    print()
    for fam, desc, _ in FAMILIES:
        print(f"  {fam:<28} 站点 {v['per_family_sites'][fam]:>3}  "
              f"文件 {v['per_family_files'][fam]:>3}   {desc}")
    print(f"  {'indirect-predicate':<28} 站点 {v['per_family_sites']['indirect-predicate']:>3}  "
          f"文件 {v['per_family_files']['indirect-predicate']:>3}   "
          f"第三种形状：本地谓词在控制流里被调用（行内无 needle）")
    print()
    print(f"  全仓命中文件 {len(files)}；主门已覆盖 {len(v['covered'])}；"
          f"**元素集缺口 {len(v['gaps'])}**")

    if v["stale"]:
        print("\n已登记为缺口但代码里已无命中（表项失真）：")
        for s in v["stale"]:
            print(f"  ✗ {s}")
    if v["unregistered"]:
        print("\n不在缺口表内、需逐条定性的降级站点：")
        for u in v["unregistered"]:
            print(f"  ✗ {u}")
    else:
        print("\n✓ 全部元素集缺口均已登记（登记 = 有理由 + 有把守者）")

    if v["site_gaps"]:
        print(f"\n站内未覆盖站点 {len(v['site_gaps'])} 处（文件级已覆盖，"
              f"但这一处匹配不上主门字面量 —— 范围表计数的口径漏项）：")
        for s in v["site_gaps"]:
            print(f"  · {s}")
    return v["code"]


if __name__ == "__main__":
    sys.exit(main())
