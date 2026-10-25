#!/usr/bin/env python3
"""post-surface-classify.py —— ★ 给「注册点」做【静态副作用分级】（v2：★ 按接收者类型解析）。

★ # 它解决什么问题

本仓的绝大多数 admin 路由是用 **不带方法前缀** 的形式注册的：

    mux.HandleFunc("/api/admin/xxx", adminWrap(h.handleXxx))

⇒ ★★ Go 的 `http.ServeMux` 对这种注册**接受任意 HTTP 方法** ⇒ ★★★ **POST 也会被受理**。
⇒ 而仓内的 `scripts/audit/api-sweep.sh`（★ 日常巡检）**只发 GET**
⇒ ★★★ **⇒ 存在一片「注册了但从未用 POST 验证过」的端点面。**

★ # 为什么需要「静态分级」而不是直接全 POST

★ 空 body 的 POST 落到 handler 体内时，★ **有的 handler 会先校验参数并 4xx 退出（零副作用）**，
★ **有的会直接执行（副作用已经发生）**。
⇒ ★★ **直接全 POST = 在不知道后果的情况下执行一遍所有端点** ⇒ ★★ 不可接受。
⇒ ⇒ ★★★ **本脚本给出【哪些端点值得先探测】的候选集**，★ 判据只做**保守分类**：
★★     · `external`  —— 源码里出现【出网调用】⇒ ★★ **绝不可批量 POST**（★ 不可回滚）
★★     · `write`     —— 源码里出现【写库调用】⇒ ★★ **不批量 POST**（★ 业务副作用）
★★     · `read`      —— 两类都没命中 ⇒ ★★ **可批量探测**（★ 空 body 大概率 4xx）
★★     · `unknown`   —— ★★ **解析不出来、或【同名撞车】** ⇒ ★★ **按 `write` 对待**（★ 保守）

★ # ★★ v2 的核心修正：★【按接收者类型】解析，★ 不再只按函数名

★ v1 的教训（★ 实测）：★ 按函数名解析 ⇒ **`ServeHTTP` / `Health` 这类名字全仓几十个类型都有**
★★ ⇒ ★★ **v1 把 `/api/admin/sessions/list`（★ 明显只读）判成了 `external`**（★ 因为撞上了别的类型的
★★ `webhook` 字样）⇒ ★★ **⇒ 撞名必须归 `unknown`，★ 不能归 `read`/`external`。**

★ v2 的解析规则：
★★   ① 剥掉任意调用包装（★ `adminWrap(h.x)` / `wrapAdmin(x.ServeHTTP)` / `m.New(c).Wrap(y)`）
★★   ② 若剥完是 `recv.Method` 形态 ⇒ ★★ **按注册点所在函数的【接收者类型】过滤候选**
★★   ③ 若剥完是裸函数名 ⇒ ★★ **只取【无接收者】的候选**
★★   ④ 过滤后仍有多个候选 ⇒ ★★ **撞名 ⇒ `unknown`**

★ # ★★ 诚实边界（★ 必读）

★ 本分类是**词法级**的 —— ★★ 它**看不穿调用链**
（★ handler 调 `h.doThing()`、★ `doThing` 内部写库 ⇒ ★★ 本脚本会把它判成 `read`）。
⇒ ⇒ ★★★ **⇒ 本脚本的输出只能当【候选集】，★ 真正的安全网是【探测时的行为判定】**：
★★     · POST 返回 4xx ⇒ ★★★ **可证明**「副作用被拦在前面」⇒ ★★ 这条 POST 面**已验证**
★★     · POST 返回 2xx/5xx ⇒ ★★★ **它真的执行了** ⇒ ★★ 该端点进【需人工裁决】清单
⇒ ⇒ ★★ **「静态分级」负责【缩小爆炸半径】，★「行为判定」负责【给出结论】。**

★ # 输出（TSV，★ 一行一路由）

    path <TAB> method <TAB> source <TAB> handler <TAB> class <TAB> reason

★ `class` ∈ {external, write, read, unknown}；★ `reason` 给出命中依据（★ 可为空）。
"""

from __future__ import annotations

import os
import re
import sys
from collections import defaultdict

# ─────────────────────────── 路径与参数 ───────────────────────────

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
ROUTES_FILE = os.path.join(REPO_ROOT, "scripts", "audit", "asset", "admin-routes.txt")

# ★ 出网调用（★ 一旦命中 ⇒ ★★ 绝不批量 POST：★ 外呼的副作用【不可回滚】）。
# ★ ★★ v1 的 `webhook` / `notify[A-Z]` 被删掉了（★ 撞名假阳性；★ 而且这两个词在只读 handler 里也常出现）。
# ★★★ ★★ **阳性对照抓到的一个真缺陷（★ 保留在注释里，★ 防止再犯）**：
# ★★ 第一版写的是 `http\.(Post|PostForm|Get|Head|NewRequest)\s*\(` ⇒ ★★ **漏了 `http.NewRequestWithContext`**
# ★★ ⇒ ★★★ **全仓有 109 处真实出网调用，★ 判据却报 `external = 0`** ⇒ ★★★ **判据没跑过阳性对照就等于没测过。**
EXTERNAL_PATTERNS = [
    # ★ `NewRequest` 后面必须允许 `WithContext` 等后缀 ⇒ ★★ 用 `\b` 收口，★ 不写死 `\s*\(`。
    (re.compile(r"\bhttp\.(Post|PostForm|Get|Head|NewRequest|NewRequestWithContext)\b"), "http-call"),
    (re.compile(r"\bhttps?\.Get\s*\("), "http-get"),
    (re.compile(r"\b(httpClient|apiClient|restyClient|upstreamClient|mailClient)\.(Do|Get|Post|Put)\s*\("), "client-call"),
    (re.compile(r"\bsmtp\.SendMail\s*\("), "smtp"),
    (re.compile(r"\bexec\.Command\s*\("), "exec"),
    (re.compile(r"\bnet\.Dial\s*\("), "net-dial"),
]

# ★ 写库调用（★ 命中 ⇒ ★★ 不批量 POST：★ 业务副作用）。
WRITE_PATTERNS = [
    (re.compile(r"\.(Exec|ExecContext)\s*\("), "exec"),
    (re.compile(r"\b(INSERT\s+INTO|UPDATE\s+\w+\s+SET|DELETE\s+FROM|TRUNCATE\b|DROP\s+TABLE|CREATE\s+TABLE|ALTER\s+TABLE)\b", re.IGNORECASE), "sql-write"),
    (re.compile(r"\.(Save|Create|Update|Delete|Purge|Remove)\s*\("), "orm-write"),
    (re.compile(r"\b(os\.Remove|os\.RemoveAll|os\.Rename|os\.WriteFile|os\.MkdirAll)\s*\("), "fs-write"),
    (re.compile(r"\b(invalidateRoutingCaches|InvalidateCredentialKeyCache)\s*\("), "cache-invalidate"),
]

# ★★★ **「会不会校验请求体」判据**（★ v3 引入，★ ★★ 这是【最关键】的一条）。
#
# ★ 为什么它比「会不会写库」精确：★★ 空 body 的 POST 落到 handler 体内时，
# ★★ **只要它先校验 body ⇒ 必然 4xx ⇒ ★★ 可证明「副作用被拦在前面」**。
# ⇒ ⇒ ★★★ **⇒ 凡是【校验了 body】的 handler ⇒ ★★ 空体 POST 是【零副作用】的 ⇒ ★★ 可以放心批量探测。**
# ⇒ ⇒ ★★★ **⇒ 凡是【没有校验 body】的 handler ⇒ ★★ 空体 POST 会【真的执行】⇒ ★★ 只能进人工清单。**
#
# ★ ★★ 方向性很重要：★★ 判据【漏判】（把有校验的当成没校验）⇒ ★★ 只是【少探测几条】⇒ ★★ 安全；
# ★★ 判据【误判】（把没校验的当成有校验）⇒ ★★ **会真的执行副作用** ⇒ ★★ 危险。
# ⇒ ⇒ ★★★ **⇒ 所以这里的判据必须【偏保守】：★ 命中才算「校验了」。**
BODY_CHECK_PATTERNS = [
    (re.compile(r"r\.Body"), "r.Body"),
    (re.compile(r"json\.(NewDecoder|Unmarshal)\b"), "json-decode"),
    (re.compile(r"\bDecode\s*\("), "decode"),
    (re.compile(r"r\.ParseForm\s*\("), "parse-form"),
    (re.compile(r"r\.ParseMultipartForm\s*\("), "parse-multipart"),
    (re.compile(r"ShouldBindJSON|BindJSON|ShouldBind\b|Bind\(&"), "bind"),
    (re.compile(r"io\.ReadAll\s*\(\s*r\.Body"), "readall-body"),
    (re.compile(r"missing required|Missing required|required fields|缺少必填|参数缺失"), "required-msg"),
    (re.compile(r"writeError\s*\([^,]+,\s*http\.StatusBadRequest"), "400-guard"),
    (re.compile(r"http\.StatusBadRequest"), "400-guard"),
]

# ★ 函数声明（★ 用于建立「函数名 → 候选」索引）。
FUNC_RE = re.compile(
    r"^func\s+(?:\((?P<recv>[^)]*)\)\s*)?(?P<name>[A-Za-z_][A-Za-z0-9_]*)\s*\(",
    re.MULTILINE,
)

# ★ 包级函数变量（★ `var x http.HandlerFunc = func(...)` ⇒ 也要能解析）。
VAR_FUNC_RE = re.compile(
    r"^var\s+(?P<name>[A-Za-z_][A-Za-z0-9_]*)\s+(?:[A-Za-z_][A-Za-z0-9_.]*\s*)?=\s*func\s*\(",
    re.MULTILINE,
)


def go_files() -> list[str]:
    """收集仓内非测试的 .go 文件（★ 排除 vendor / node_modules / .git）。"""
    out = []
    skip_dirs = {".git", "node_modules", "vendor", "dist", "build", "testdata"}
    for root, dirs, files in os.walk(REPO_ROOT):
        dirs[:] = [d for d in dirs if d not in skip_dirs]
        for f in files:
            if f.endswith(".go") and not f.endswith("_test.go"):
                out.append(os.path.join(root, f))
    return out


def extract_body(src: str, open_brace: int) -> str | None:
    """从 `{` 起做括号配平，取出函数体。★ 配平失败返回 None（★ ⇒ unknown）。"""
    depth = 0
    i = open_brace
    n = len(src)
    in_str = None
    in_line_comment = False
    in_block_comment = False
    while i < n:
        c = src[i]
        nxt = src[i + 1] if i + 1 < n else ""
        if in_line_comment:
            if c == "\n":
                in_line_comment = False
            i += 1
            continue
        if in_block_comment:
            if c == "*" and nxt == "/":
                in_block_comment = False
                i += 2
                continue
            i += 1
            continue
        if in_str:
            if c == "\\":
                i += 2
                continue
            if c == in_str:
                in_str = None
            i += 1
            continue
        if c == "/" and nxt == "/":
            in_line_comment = True
            i += 2
            continue
        if c == "/" and nxt == "*":
            in_block_comment = True
            i += 2
            continue
        if c in ('"', "`", "'"):
            in_str = c
            i += 1
            continue
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                return src[open_brace + 1 : i]
        i += 1
    return None


def recv_type_name(recv: str | None) -> str | None:
    """★ 从接收者声明里取类型名（★ `h *Handler` ⇒ `Handler`；★ `h Handler` ⇒ `Handler`）。"""
    if not recv:
        return None
    parts = recv.split()
    if not parts:
        return None
    t = parts[-1].lstrip("*")
    t = t.split("[")[0]
    return t or None


def recv_var_name(recv: str | None) -> str | None:
    """★ 从接收者声明里取变量名（★ `h *Handler` ⇒ `h`）。"""
    if not recv:
        return None
    parts = recv.split()
    if not parts:
        return None
    v = parts[0].lstrip("*")
    return v or None


class FileIndex:
    """★ 单个 .go 文件的索引：★ 函数候选 + 接收者类型 + 注册点所属的外层函数。"""

    def __init__(self, path: str, src: str):
        self.path = path
        self.src = src
        self.lines = src.splitlines(keepends=True)
        # 偏移 → 行号
        self.line_start = []
        off = 0
        for ln in self.lines:
            self.line_start.append(off)
            off += len(ln)
        # func 声明：(name, recv_type, recv_var, body, start_line, end_line)
        self.funcs = []
        for m in FUNC_RE.finditer(src):
            name = m.group("name")
            recv = m.group("recv")
            brace = src.find("{", m.end())
            if brace < 0:
                continue
            body = extract_body(src, brace)
            if body is None:
                continue
            self.funcs.append(
                {
                    "name": name,
                    "file": path,
                    "recv_type": recv_type_name(recv),
                    "recv_var": recv_var_name(recv),
                    "body": body,
                    "start_line": self.line_of(brace),
                    "end_line": self.line_of(src.find("}", brace)) if src.find("}", brace) >= 0 else 10**9,
                }
            )
        # ★★ 文件级粗筛：★ 本文件里是否出现过出网调用。
        self.file_has_external = False
        for pat, _tag in EXTERNAL_PATTERNS:
            if pat.search(strip_go_comments(src)):
                self.file_has_external = True
                break
        # 包级函数变量：name → body
        self.varfuncs = {}
        for m in VAR_FUNC_RE.finditer(src):
            name = m.group("name")
            brace = src.find("{", m.end())
            if brace < 0:
                continue
            body = extract_body(src, brace)
            if body is None:
                continue
            self.varfuncs[name] = body

    def line_of(self, offset: int) -> int:
        lo, hi = 0, len(self.line_start)
        while lo < hi:
            mid = (lo + hi) // 2
            if self.line_start[mid] <= offset:
                lo = mid + 1
            else:
                hi = mid
        return lo  # ★ 0-based 行下标

    def enclosing_func(self, line_idx: int):
        """★ 找包含该行（0-based）的最内层函数。"""
        best = None
        for f in self.funcs:
            if f["start_line"] <= line_idx <= f["end_line"]:
                if best is None or (f["end_line"] - f["start_line"]) < (best["end_line"] - best["start_line"]):
                    best = f
        return best


_FILE_CACHE: dict[str, FileIndex] = {}

# ★★★ **全局索引**（★ v2 第一版只索引了【注册文件自身】的函数
# ★★★ ⇒ ★★ handler 定义在别的文件时全部落成 `func-not-indexed` ⇒ ★★ 444 unknown）。
_GLOBAL_FUNCS: dict[str, list[dict]] | None = None
_GLOBAL_VARFUNCS: dict[str, str] | None = None


def build_global_index():
    """★ 建立「函数名 → 候选」与「包级函数变量名 → 函数体」的【全仓】索引。"""
    global _GLOBAL_FUNCS, _GLOBAL_VARFUNCS
    if _GLOBAL_FUNCS is not None:
        return
    funcs: dict[str, list[dict]] = defaultdict(list)
    varfuncs: dict[str, str] = {}
    for path in go_files():
        fi = load_file(path)
        if fi is None:
            continue
        for f in fi.funcs:
            funcs[f["name"]].append(f)
        for name, body in fi.varfuncs.items():
            varfuncs[name] = body
    _GLOBAL_FUNCS = funcs
    _GLOBAL_VARFUNCS = varfuncs


def load_file(full: str) -> FileIndex | None:
    if full in _FILE_CACHE:
        return _FILE_CACHE[full]
    try:
        with open(full, encoding="utf-8", errors="replace") as fh:
            src = fh.read()
    except OSError:
        return None
    fi = FileIndex(full, src)
    _FILE_CACHE[full] = fi
    return fi


def strip_go_comments(src: str) -> str:
    """去掉 Go 注释（★ 保留字符串字面量内容，★ 那是代码不是注释）。"""
    out = []
    i = 0
    n = len(src)
    in_str = None
    while i < n:
        c = src[i]
        nxt = src[i + 1] if i + 1 < n else ""
        if in_str:
            out.append(c)
            if c == "\\" and in_str != "`":
                if i + 1 < n:
                    out.append(src[i + 1])
                i += 2
                continue
            if c == in_str:
                in_str = None
            i += 1
            continue
        if c in ('"', "`", "'"):
            in_str = c
            out.append(c)
            i += 1
            continue
        if c == "/" and nxt == "/":
            while i < n and src[i] != "\n":
                i += 1
            continue
        if c == "/" and nxt == "*":
            i += 2
            while i + 1 < n and not (src[i] == "*" and src[i + 1] == "/"):
                i += 1
            i += 2
            continue
        out.append(c)
        i += 1
    return "".join(out)


def split_args(expr: str) -> list[str]:
    """★ 按【顶层逗号】切分参数（★ 括号/字符串内的逗号不算）。"""
    parts = []
    depth = 0
    cur = []
    in_str = None
    for c in expr:
        if in_str:
            cur.append(c)
            if c == in_str:
                in_str = None
            continue
        if c in ('"', "`", "'"):
            in_str = c
            cur.append(c)
            continue
        if c in "([{":
            depth += 1
        elif c in ")]}":
            depth -= 1
        if c == "," and depth == 0:
            parts.append("".join(cur).strip())
            cur = []
            continue
        cur.append(c)
    if cur:
        parts.append("".join(cur).strip())
    return parts


def top_level_call(expr: str):
    """★ 若 expr 是「一次调用」⇒ 返回 (头, 实参列表)；★ 否则返回 None。"""
    s = expr.strip()
    if not s.endswith(")"):
        return None
    stack = []
    spans = []
    in_str = None
    i = 0
    while i < len(s):
        c = s[i]
        if in_str:
            if c == "\\":
                i += 2
                continue
            if c == in_str:
                in_str = None
            i += 1
            continue
        if c in ('"', "`", "'"):
            in_str = c
            i += 1
            continue
        if c == "(":
            stack.append(i)
        elif c == ")":
            if stack:
                st = stack.pop()
                if not stack:
                    spans.append((st, i))
        i += 1
    if not spans:
        return None
    st, en = spans[-1]
    if en != len(s) - 1:
        return None
    return s[:st], split_args(s[st + 1 : en])


HANDLER_SHAPE_RE = re.compile(r"[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*\Z")


def peel_to_handler(expr: str):
    """★ 剥调用链 ⇒ 返回 (handler_expr, factory_callee)。

    ★ ★★ **必须区分「包装器」与「工厂」**（★ R159 实测踩到）：
    ★★   · 包装器：★ `adminWrap(h.handleList)` ⇒ ★★ 实参**就是** handler ⇒ 继续剥
    ★★   · 工厂：★★ `admin.HandleCredentialSuccessRates(dbConn.Pool())` ⇒ ★★ 实参是**依赖**（★ 一个 pool），
    ★★     ★★ 真正的 handler 是这个工厂**返回的闭包** ⇒ ★★ **要去看工厂自己的函数体**
    ⇒ ⇒ ★★ 判据：★ 实参长得像 handler（标识符 / 选择器 / 闭包）⇒ 继续剥；★ 实参是【调用】⇒ 它是依赖 ⇒ 停。
    """
    cur = expr.strip()
    for _ in range(12):
        if cur.startswith("func"):
            return cur, None
        call = top_level_call(cur)
        if not call:
            return cur, None
        head, args = call
        cand = None
        for a in reversed(args):
            a = a.strip()
            if not a or a in ("nil", "http.HandlerFunc", "{}"):
                continue
            cand = a
            break
        if cand is None:
            return cur, None
        if HANDLER_SHAPE_RE.match(cand) or cand.startswith("func"):
            cur = cand
            continue
        # ★★ 实参是【调用】⇒ 它是依赖 ⇒ handler 工厂是 head 里的那个函数
        m = re.search(r"([A-Za-z_][A-Za-z0-9_]*)\s*$", head.strip())
        return None, (m.group(1) if m else head.strip())
    return cur, None


def unwrap_handler(expr: str) -> str:
    """★ 兼容入口（★ 只返回 handler 表达式；★ 工厂形态返回空串）。"""
    h, _factory = peel_to_handler(expr)
    return h or ""


def extract_handler_expr(reg_text: str) -> str | None:
    """从注册调用文本里取出 handler 表达式（★ 最后一个参数）。"""
    m = re.search(r"\.(?:HandleFunc|Handle)\s*\(", reg_text)
    if not m:
        return None
    i = m.end()
    depth = 1
    in_str = None
    while i < len(reg_text):
        c = reg_text[i]
        if in_str:
            if c == "\\":
                i += 2
                continue
            if c == in_str:
                in_str = None
            i += 1
            continue
        if c in ('"', "`"):
            in_str = c
            i += 1
            continue
        if c == "(":
            depth += 1
        elif c == ")":
            depth -= 1
            if depth == 0:
                break
        i += 1
    inner = reg_text[m.end() : i]
    args = split_args(inner)
    if len(args) < 2:
        return None
    return args[-1].strip()


def has_body_check(code: str) -> str:
    """★ 判 handler 是否【校验请求体】⇒ 返回命中标签（★ 空串 = 没校验）。"""
    for pat, tag in BODY_CHECK_PATTERNS:
        if pat.search(code):
            return tag
    return ""


def classify_body(body: str) -> tuple[str, str]:
    """★ 对函数体做副作用分级。★ 顺序：external > write > read。"""
    code = strip_go_comments(body)
    for pat, tag in EXTERNAL_PATTERNS:
        if pat.search(code):
            return "external", tag
    for pat, tag in WRITE_PATTERNS:
        if pat.search(code):
            return "write", tag
    return "read", ""


def resolve_handler(fi: FileIndex, reg_text: str, line_idx: int):
    """★ 解析 handler ⇒ 返回 (显示名, [候选函数体], 失败原因)。"""
    expr = extract_handler_expr(reg_text)
    if not expr:
        return "", [], "no-registration-expr"
    inner, factory = peel_to_handler(expr)
    if factory:
        # ★★ 工厂形态：★ handler 是工厂返回的闭包 ⇒ ★★ 看【工厂自己的函数体】
        inner = factory
    if inner.startswith("func"):
        brace = reg_text.find("{")
        body = extract_body(reg_text, brace) if brace >= 0 else None
        if body is None:
            return "inline", [], "inline-body-unbalanced"
        return "inline", [body], ""

    build_global_index()
    assert _GLOBAL_VARFUNCS is not None and _GLOBAL_FUNCS is not None

    # ★ 包级函数变量
    if inner in _GLOBAL_VARFUNCS:
        return inner, [_GLOBAL_VARFUNCS[inner]], ""

    # ★ 函数名候选（★ 全仓）
    m = re.match(r"^(?P<recv>[A-Za-z_][A-Za-z0-9_]*)\.(?P<meth>[A-Za-z_][A-Za-z0-9_]*)$", inner)
    meth = m.group("meth") if m else (inner if re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", inner) else None)
    if meth is None:
        return inner[:60], [], "unresolved-handler"

    cands = list(_GLOBAL_FUNCS.get(meth, []))
    if not cands:
        return meth, [], "func-not-indexed"

    # ★★★ **消歧规则**（★ v2 核心）：
    #   ① **名字全仓唯一** ⇒ ★★ **没有歧义** ⇒ ★★ 直接用（★ 不管接收者怎么拼）
    #      （★ 覆盖 `h.field.Method` / `pkgVar.Method` 这类 ★★ 接收者不是【外层接收者变量】的形态）
    #   ② 名字**不唯一** ⇒ ★★ 试按【外层接收者类型】过滤 ⇒ ★★ 过滤后仍不唯一 ⇒ ★★ unknown
    if len(cands) == 1:
        return meth, [cands[0]["body"]], ""

    if m:
        recv_var = m.group("recv")
        enclosing = fi.enclosing_func(line_idx)
        if enclosing and enclosing["recv_var"] == recv_var and enclosing["recv_type"]:
            filtered = [c for c in cands if c["recv_type"] == enclosing["recv_type"]]
            if len(filtered) == 1:
                return meth, [filtered[0]["body"]], ""
            if not filtered:
                return meth, [], "recv-type-mismatch"
    else:
        pkg_level = [c for c in cands if c["recv_type"] is None]
        if len(pkg_level) == 1:
            return meth, [pkg_level[0]["body"]], ""

    # ★★ 撞名 ⇒ unknown（★ 保守）
    return meth, [], "ambiguous-name(%d)" % len(cands)


def read_registration(fi: FileIndex, lineno_i: int) -> str:
    """★ 从【指定行（1-based）】起，取出整条注册调用的文本（★ 按括号配平，★ 可跨多行）。"""
    if lineno_i < 1 or lineno_i > len(fi.lines):
        return ""
    text = "".join(fi.lines[lineno_i - 1 :])
    m = re.search(r"\.(?:HandleFunc|Handle)\s*\(", text)
    if not m:
        return text
    i = m.end()
    depth = 1
    in_str = None
    while i < len(text):
        c = text[i]
        if in_str:
            if c == "\\":
                i += 2
                continue
            if c == in_str:
                in_str = None
            i += 1
            continue
        if c in ('"', "`"):
            in_str = c
            i += 1
            continue
        if c == "(":
            depth += 1
        elif c == ")":
            depth -= 1
            if depth == 0:
                return text[: i + 1]
        i += 1
    return text



# ★★★ **有界传递闭包**（★ v4 引入）：★ handler 调 `h.doThing()`、★ `doThing` 内部写库
# ★★★ ⇒ ★★ **纯词法判据会把它判成 `read`** ⇒ ★★ **⇒ 那就是「批量探测会真执行写库」的那一类**。
# ⇒ ⇒ ★★★ **⇒ 所以分级必须【顺着调用走一层】。**
#
# ★★ 名字解析是**按名匹配**的（★ 不做类型解析）⇒ ★★ 会误命中别的包的同名函数
# ⇒ ★★ **但方向是安全的**：★ 误命中只会让分级【更保守】（★ 把可探测的排除掉）⇒ ★★ 不会漏判副作用。

CALL_RE = re.compile(r"\b([a-z][A-Za-z0-9_]*)\s*\(")
METHOD_CALL_RE = re.compile(r"\.([A-Z][A-Za-z0-9_]*)\s*\(")

TRANSITIVE_DEPTH = 3


def called_names(body: str) -> set[str]:
    """★ 取出函数体里【调用到的名字】（★ 含方法名）。"""
    code = strip_go_comments(body)
    names = set(CALL_RE.findall(code))
    names.update(METHOD_CALL_RE.findall(code))
    return names


# ★★★ **「含出网调用的文件」集合**（★ v5）。
# ★★ 为什么要收窄到这里：★ v4 的传递闭包**按名解析**⇒ ★★ 全仓同名函数互相污染
# ★★ ⇒ ★★★ **449/576 被误判成 external** ⇒ ★★★ **那是【更宽的假阳性】，★ 不是【更准的判据】**。
# ⇒ ⇒ ★★★ **⇒ 正解：★ 只顺着【真正含出网调用的那些文件】往下追。**
# ★★ 那些文件很少（★ 实测十几个）⇒ ★★ 按名撞车的概率大幅下降 ⇒ ★★ 假阳性可控。

_OUTBOUND_FILES: set[str] | None = None
_OUTBOUND_FUNC_NAMES: set[str] | None = None


def outbound_files() -> set[str]:
    """★ 全仓扫描：★ 哪些文件里出现了出网调用。"""
    global _OUTBOUND_FILES, _OUTBOUND_FUNC_NAMES
    if _OUTBOUND_FILES is not None:
        return _OUTBOUND_FILES
    files: set[str] = set()
    names: set[str] = set()
    for path in go_files():
        fi = load_file(path)
        if fi is None:
            continue
        hit = False
        for f in fi.funcs:
            code = strip_go_comments(f["body"])
            for pat, _tag in EXTERNAL_PATTERNS:
                if pat.search(code):
                    hit = True
                    names.add(f["name"])
                    break
            if hit:
                break
        if hit:
            files.add(path)
    _OUTBOUND_FILES = files
    _OUTBOUND_FUNC_NAMES = names
    return files


def transitive_external(bodies: list[str]):
    """★ **收窄版**传递闭包：★ 只顺着【含出网调用的文件】里的函数往下追。

    ★ 返回 (是否 external, 命中标签)。★ ★★ 撞名只会让结果【更保守】⇒ ★★ 安全。
    """
    build_global_index()
    assert _GLOBAL_FUNCS is not None
    ofiles = outbound_files()
    seen: set[int] = set()
    frontier = list(bodies)
    for _depth in range(TRANSITIVE_DEPTH + 1):
        nxt = []
        for body in frontier:
            for nm in called_names(body):
                cands = _GLOBAL_FUNCS.get(nm)
                if not cands:
                    continue
                for c in cands:
                    key = id(c)
                    if key in seen:
                        continue
                    seen.add(key)
                    if c["file"] in ofiles:
                        cls, tag = classify_body(c["body"])
                        if cls == "external":
                            return True, tag
                        nxt.append(c["body"])
        frontier = nxt
        if not frontier:
            break
    return False, ""


def main() -> int:
    routes_file = sys.argv[1] if len(sys.argv) > 1 else ROUTES_FILE

    rows = []
    with open(routes_file, encoding="utf-8") as fh:
        for line in fh:
            line = line.rstrip("\n")
            if not line or line.startswith("#"):
                continue
            parts = line.split("\t")
            if len(parts) < 3:
                continue
            path, method, source = parts[0], parts[1], parts[2]
            fpath, _, lineno = source.rpartition(":")
            try:
                lineno_i = int(lineno)
            except ValueError:
                lineno_i = 0
            fi = load_file(os.path.join(REPO_ROOT, fpath))
            if fi is None:
                rows.append((path, method, source, "", "unknown", "file-unreadable", ""))
                continue
            reg_text = read_registration(fi, lineno_i)
            name, bodies, reason = resolve_handler(fi, reg_text, lineno_i - 1)
            if not bodies:
                rows.append((path, method, source, name, "unknown", reason or "no-body", ""))
                continue
            worst = "read"
            why = ""
            bcheck = ""
            for body in bodies:
                bc = has_body_check(strip_go_comments(body))
                if bc and not bcheck:
                    bcheck = bc
            # ★★ **只用【词法层】分级**（★ handler 本体）。
            # ★★★ ★★ **不要用传递闭包**（★ v4 实测：★ 按名解析深度 ≥1 ⇒ ★★ 全仓同名函数互相污染
            # ★★★ ⇒ ★★ **449/576 被误判成 external** ⇒ ★★★ **那是【更宽的假阳性】，★ 不是【更准的判据】**。
            # ★★★ ⇒ ★★ **调用链盲区改由【运行时护栏】承担**（★ 写库护栏 + 出网连接监控 + 快照回滚）。
            worst, why = "read", ""
            for body in bodies:
                cls, rs = classify_body(body)
                if cls == "external":
                    worst, why = "external", rs
                    break
                if cls == "write":
                    worst, why = "write", rs
            # ★★★ **文件级粗筛**（★ 替代不可靠的传递闭包）：★ handler 所在的【源文件】里若出现
            # ★★★ 出网调用 ⇒ ★★ 该文件的 handler 一律按 `external` 对待（★ 保守 ⇒ 不批量探测）。
            if worst != "external" and fi.file_has_external:
                worst, why = "external", "file-level-external"
            # ★★★ **不做传递闭包**（★ v4/v5 实测两次失败：★ 按名解析深度 ≥1 ⇒ ★★ 全仓同名函数互相污染
            # ★★★ ⇒ ★★ **449/576 ⇒ 195/576 被误判成 external** ⇒ ★★★ **那是【更宽的假阳性】，★ 不是【更准的判据】**）。
            # ★★★ ⇒ ★★ **调用链盲区改由【运行时护栏】承担**（★ 写库护栏 + 快照回滚 + 文件级粗筛）。
            rows.append((path, method, source, name, worst, why, bcheck))

    out = sys.stdout
    out.write("#post-surface-classify\tversion\t2\n")
    out.write("#post-surface-classify\troutes_file\t%s\n" % os.path.relpath(routes_file, REPO_ROOT))
    counts = defaultdict(int)
    bulk = 0
    for p, m, s, h, c, r, bc in rows:
        out.write("%s\t%s\t%s\t%s\t%s\t%s\t%s\n" % (p, m, s, h, c, r, bc))
        counts[c] += 1
        # ★★ **批量探测集** = ★★ 「非写」且「校验了 body」⇒ ★★ 空体 POST 可证明零副作用。
        if c == "read" and bc:
            bulk += 1
    out.write("#meta\ttotal\t%d\n" % len(rows))
    for k in ("read", "write", "external", "unknown"):
        out.write("#meta\t%s\t%d\n" % (k, counts[k]))
    out.write("#meta\tbody_checked\t%d\n" % sum(1 for r in rows if r[6]))
    out.write("#meta\tbulk_probe_safe\t%d\n" % bulk)
    return 0


if __name__ == "__main__":
    sys.exit(main())
