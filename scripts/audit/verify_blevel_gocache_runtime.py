#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""r1049 审计轮 · 【运行期验证器】（★ 入仓版 · ★ 可复现 · ★ 两把尺对拍）。

★ ★★ **它验什么**：★★
★★ ① **前导块真的把 GOCACHE 指向了【专用目录】**（★ 不是「文件里写着」，★ 而是
★★    **真的执行**那段前导块 ⇒ ⇒ 然后看进程里的 GOCACHE 是什么）；
★★ ② **Python 两种新形态真的生效**（★ `gocache.apply_to_environ("mutate")` 与
★★    `gocache.env("mutate")`）★★ ⇒ ⇒ ★★ **这一档是 r1049 轮新加的**（★ ★★ r1048
★★    那版验证器**只认前导块** ⇒ ⇒ ★★ ⇒ 那 51 个 `dir_for` 脚本**全落在跳过桶里**
★★    ⇒ ⇒ ★★ ⇒ **它们从未被运行期验证过** ⇒ ⇒ ★★ ⇒ 而它们恰是带洞的那批）；
★★ ③ **失败安全**：★ 把专用缓存目录弄成**建不出来** ⇒ ⇒ 两种形态都必须
★★    **不覆盖** GOCACHE（★ 而**不是**设成一个用不了的路径 ⇒ ⇒ 那会让 go 直接报错）。

★ ★★ **★ 这一版与上一版最关键的区别（★ 上一版是「量具不同源」）：★★**
★★ 上一版验证器**自己重新枚举脚本族** ⇒ ⇒ ⇒ 而 B 级门也是**自己枚举** ⇒ ⇒ ⇒
★★ ⇒ ⇒ 两边的【枚举集】可能不同 ⇒ ⇒ ⇒ 「覆盖完整」这句话只是【两把尺各自量自己】
★★ ⇒ ⇒ ⇒ **量具不同源 ⇒ ⇒ 结论不可比** ⇒ ⇒ ⇒ ★★ 这一版改成：
★★ ⇒ ⇒ ★★ 【**静态作用域集由门导出**】（★ `GOCACHE_GATE_SCOPE_DUMP` 环境变量
★★ ⇒ ⇒ ★★ ⇒ 门把【它认定的编译型脚本清单】写到文件 ⇒ ⇒ ★★ ⇒ 本验证器**对着
★★ ⇒ ⇒ ★★ 这同一份清单**逐个跑运行期探针 ⇒ ⇒ ⇒ 「静态认定的每一个，都被运行期
★★ ⇒ ⇒ ★★ 验过」⇒ ⇒ ⇒ **这句话才站得住**）。
★★ ⇒ ⇒ ★★ 同时本验证器**自己**也算一遍编译型集（★ 独立的 Python 实现 ★）⇒ ⇒ ⇒
★★ ⇒ ⇒ 与门导出的那一份**逐个对拍** ⇒ ⇒ ⇒ **两把尺的差集必须列出来**（★ 差集
★★ ⇒ ⇒ ⇒ 非空 ⇒ ⇒ ★★ ⇒ 说明两把尺有一把歪了 ⇒ ⇒ ⇒ **不许只报自己那把**）。

★ ★★ **为什么必须「按形态分档报读数」**：★★ r1048 轮的读数是「95/95 + 跳过 755」
★★ ⇒ ⇒ ★★ ⇒ **跳过桶没有构成** ⇒ ⇒ ★★ ⇒ 无法判断「被跳过的那些」是不是就是
★★ ⇒ ⇒ ★★ 带洞的那批 ⇒ ⇒ ★★ ⇒ 本文件对**每个形态**都给读数 ⇒ ⇒ ★★ ⇒ **桶不再是桶**。

★ ★★ **怎么用**：★★
★★     python3 scripts/audit/verify_blevel_gocache_runtime.py
★★ ⇒ ⇒ 退出码 0 = 全绿；1 = 有洞（★ 并逐条列出来 ★）。
"""
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import tokenize

ROOT = subprocess.run(["git", "rev-parse", "--show-toplevel"],
                     cwd=os.path.dirname(os.path.abspath(__file__)),
                     capture_output=True, text=True).stdout.strip()
LIB = os.path.join(ROOT, "scripts", "lib")
PRELUDE_MARK = "# ★★ r1048 轮"
PY_START = "import os as _gocache_os"
PY_END = 'gocache.apply_to_environ("script")'
DEDICATED = os.path.join(ROOT, ".build-local", ".gocache-script")
DEDICATED_MUTATE = os.path.join(ROOT, ".build-local", ".gocache-mutate")
# ★ 失败安全探针用的【建不出来的仓根】：★ `/dev/null` 是**文件** ⇒ ⇒
# ★ 在它下面建目录 ⇒ ⇒ 必然失败（★ NotADirectoryError ⊂ OSError ⇒ ⇒ 被 ensure 捕获）。
BAD_ROOT = "/dev/null/nope"

# ★ ★★ **与 B 级门同款的「基础设施跳过目录」**（★ 这一层不是政策 ⇒ ⇒ 不进登记簿）★★
INFRA_SKIP_DIRS = {".git", "node_modules", "vendor", ".build-local", "maestro"}

# ★ ★★ **编译型 go 子命令**（★ 与 B 级门的两条正则同款）★★
COMPILE_RE = re.compile(r"\bgo\s+(build|test|vet|run)\b")
COMPILE_LIST_RE = re.compile(r"[\"']go[\"']\s*,\s*[\"'](build|test|vet|run)[\"']")

REGISTRY_REL = "scripts/ursmcheck/gocache_script_family_exclusions.json"
SCOPE_DUMP_ENV = "GOCACHE_GATE_SCOPE_DUMP"

# ★ ★★ **第 5 种形态（★ r1047 轮的「直接执行」形态）** ★★
# ★ ★★ `export GOCACHE="$("$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh" mutate)"`
# ★ ★★ ⇒ ⇒ ★★ ⇒ 16 个脚本用这个形态 ⇒ ⇒ ★★ ⇒ ★★ **r1049 轮第一版验证器不认它**
# ★ ★★ ⇒ ⇒ ★★ ⇒ 那 16 个脚本全落进「洞」桶 ⇒ ⇒ ★★ ⇒ ★★ **量具窄于事实源**
# ★ ★★ ⇒ ⇒ ★★ ⇒ 是本轮【两把尺交叉对拍】抓到的 ⇒ ⇒ ★★ ⇒ 不是门抓到的。
SHELL_EXEC_RE = re.compile(r"export\s+GOCACHE=.*gocache\.sh")


# ── 枚举 ──

def read_registry():
    with open(os.path.join(ROOT, REGISTRY_REL), encoding="utf-8") as f:
        return json.load(f)


def excluded_prefixes(reg):
    return [e["path"] for e in reg.get("excludedPaths", [])]


def is_excluded(rel, prefixes):
    for p in prefixes:
        if rel == p or rel.startswith(p + "/"):
            return True
    return False


def walk_scripts():
    """枚举工作树里全部 `.sh` / `.py`（★ 与门的 `gocacheScriptScan` 同口径）。"""
    out = []
    for dirpath, dirnames, filenames in os.walk(ROOT):
        dirnames[:] = [d for d in dirnames if d not in INFRA_SKIP_DIRS]
        for name in filenames:
            if not name.endswith(".sh") and not name.endswith(".py"):
                continue
            full = os.path.join(dirpath, name)
            rel = os.path.relpath(full, ROOT).replace(os.sep, "/")
            out.append(rel)
    out.sort()
    return out


def read(rel):
    with open(os.path.join(ROOT, rel), encoding="utf-8", errors="replace") as f:
        return f.read()


# ── ★★ 独立的编译型判定（★ 本验证器自己算一遍 ⇒ ⇒ 与门对拍）★★

def _blank(buf, start, end):
    for i in range(start, end):
        if buf[i] != "\n":
            buf[i] = " "


def _line_offsets(text):
    offs = [0]
    for line in text.splitlines(True):
        offs.append(offs[-1] + len(line))
    return offs


def mask_python(text):
    """把【注释】与【三引号块】抹成空格（★ 普通字符串**留着** ⇒ ⇒ 因为
    `["go", "test"]` 这种列表形态的 go 调用本身就在字符串里 ⇒ ⇒ 抹掉就漏判）。"""
    buf = list(text)
    offs = _line_offsets(text)
    try:
        toks = list(tokenize.generate_tokens(io.StringIO(text).readline))
    except Exception as exc:  # ★ 语法错误 ⇒ ⇒ 记下来 ⇒ ⇒ 不用掩码口径下结论
        return None, str(exc)
    for tok in toks:
        if tok.type == tokenize.COMMENT:
            _blank(buf, offs[tok.start[0] - 1] + tok.start[1],
                   offs[tok.end[0] - 1] + tok.end[1])
        elif tok.type == tokenize.STRING:
            if re.match(r"^[A-Za-z]*(\"\"\"|''')", tok.string):
                _blank(buf, offs[tok.start[0] - 1] + tok.start[1],
                       offs[tok.end[0] - 1] + tok.end[1])
    return "".join(buf), None


def mask_shell(text):
    """把【词首的 `#` 注释】抹成空格（★ 引号内的 `#` 不是注释 ⇒ ⇒ 要跟踪引号）。"""
    out = []
    in_single = in_double = False
    for line in text.split("\n"):
        b = list(line)
        i = 0
        while i < len(line):
            c = line[i]
            if in_single or in_double:
                if in_double and c == "\\" and i + 1 < len(line):
                    i += 2
                    continue
                if in_single and c == "'":
                    in_single = False
                elif in_double and c == '"':
                    in_double = False
                i += 1
                continue
            if c == "#":
                prev_ok = i == 0 or line[i - 1] in " \t;&|"
                if prev_ok:
                    for j in range(i, len(line)):
                        b[j] = " "
                    break
                i += 1
                continue
            if c == "'":
                in_single = True
                i += 1
                continue
            if c == '"':
                in_double = True
                i += 1
                continue
            i += 1
        out.append("".join(b))
    return "\n".join(out)


def masked_text(rel, text):
    """返回【只看代码】的文本（★ 注释与三引号块已被抹掉）。"""
    if rel.endswith(".py"):
        m, err = mask_python(text)
        return (m, err)
    return (mask_shell(text), None)


def has_compile(masked):
    for line in masked.split("\n"):
        if COMPILE_RE.search(line) or COMPILE_LIST_RE.search(line):
            return True
    return False


# ── 形态分档 ──

def shell_prelude(text):
    lines = text.split("\n")
    start = None
    for i, l in enumerate(lines):
        if l.startswith(PRELUDE_MARK):
            start = i
            break
    if start is None:
        return None
    for j in range(start, len(lines)):
        if lines[j].rstrip() == "fi":
            return "\n".join(lines[start:j + 1])
    return None


def py_prelude(text):
    lines = text.split("\n")
    start = end = None
    for i, l in enumerate(lines):
        if l.startswith(PY_START) and start is None:
            start = i
        if l.strip() == PY_END and start is not None:
            end = i
            break
    if start is None or end is None:
        return None
    return "\n".join(lines[start:end + 1])


def classify(rel, text):
    if rel.endswith(".sh"):
        if shell_prelude(text) is not None:
            return "prelude-shell"
        if SHELL_EXEC_RE.search(text):
            return "export-gocache.sh"
        return "other"
    if py_prelude(text) is not None:
        return "prelude-python"
    if 'gocache.apply_to_environ("mutate")' in text:
        return "apply_to_environ"
    if 'gocache.env("mutate")' in text:
        return "env()"
    return "other"


# ── 运行期探针 ──

def probe_shell(block, real_path):
    script = (
        "#!/usr/bin/env bash\n"
        + block.replace('"${BASH_SOURCE[0]:-$0}"', '"%s"' % os.path.abspath(real_path))
        + "\nprintf '%s' \"${GOCACHE:-<EMPTY>}\"\n"
    )
    with tempfile.TemporaryDirectory() as td:
        tf = os.path.join(td, "probe.sh")
        with open(tf, "w", encoding="utf-8") as f:
            f.write(script)
        r = subprocess.run(["bash", tf], capture_output=True, text=True, cwd=ROOT)
    return r.stdout.strip(), r.stderr.strip()[:160]


def probe_python(code):
    with tempfile.TemporaryDirectory() as td:
        tf = os.path.join(td, "probe.py")
        with open(tf, "w", encoding="utf-8") as f:
            f.write(code)
        r = subprocess.run([sys.executable, tf], capture_output=True, text=True,
                           cwd=ROOT)
    out = r.stdout.strip().splitlines()
    return (out[-1] if out else "<NO-OUTPUT>"), r.stderr.strip()[:160]


def probe_apply_to_environ():
    code = (
        "import os, sys\n"
        "sys.path.insert(0, %r)\n" % LIB +
        "import gocache\n"
        "gocache.apply_to_environ(\"mutate\")\n"
        "print(os.environ.get('GOCACHE','<EMPTY>'))\n"
    )
    return probe_python(code)


def probe_env():
    code = (
        "import os, sys\n"
        "sys.path.insert(0, %r)\n" % LIB +
        "import gocache\n"
        "e = gocache.env(\"mutate\")\n"
        "print(e.get('GOCACHE','<EMPTY>'))\n"
    )
    return probe_python(code)


def probe_shell_exec():
    """第 5 种形态：★ `export GOCACHE="$(…/gocache.sh mutate)"` ★ 真跑一遍。"""
    script = (
        "#!/usr/bin/env bash\n"
        "export GOCACHE=\"$(\"$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh\" mutate)\"\n"
        "printf '%s' \"${GOCACHE:-<EMPTY>}\"\n"
    )
    with tempfile.TemporaryDirectory() as td:
        tf = os.path.join(td, "probe.sh")
        with open(tf, "w", encoding="utf-8") as f:
            f.write(script)
        r = subprocess.run(["bash", tf], capture_output=True, text=True, cwd=ROOT)
    return r.stdout.strip(), r.stderr.strip()[:160]


# ── ★★ 静态作用域集（★ 由门导出 ⇒ ⇒ 两把尺同源）★★

def find_go():
    g = shutil.which("go")
    if g:
        return g
    cand = os.path.expanduser("~/sdk/go/bin/go")
    return cand if os.path.isfile(cand) else None


def gate_scope_dump():
    """跑一次 B 级门（★ 只跑判据①那个测试 ★）⇒ ⇒ 拿它认定的作用域清单。"""
    go = find_go()
    if not go:
        return None, "go 不在 PATH（★ 试过 `which go` 与 ~/sdk/go/bin/go）"
    with tempfile.TemporaryDirectory() as td:
        dst = os.path.join(td, "scope.txt")
        env = dict(os.environ)
        env[SCOPE_DUMP_ENV] = dst
        env["PATH"] = os.path.dirname(go) + os.pathsep + env.get("PATH", "")
        r = subprocess.run(
            [go, "test", "./scripts/ursmcheck/", "-count=1",
             "-run", "TestScriptFamilyCompileSitesUseDedicatedGoCache"],
            cwd=ROOT, capture_output=True, text=True, env=env)
        if not os.path.isfile(dst):
            return None, "门没写出作用域清单（★ rc=%d ★ %s）" % (
                r.returncode, (r.stdout or r.stderr)[-300:])
        with open(dst, encoding="utf-8") as f:
            return set(x.strip() for x in f if x.strip()), None


# ── 主流程 ──

def main():
    reg = read_registry()
    prefixes = excluded_prefixes(reg)
    paths = walk_scripts()
    print("★ 工作树里的 .sh/.py（★ 排基础设施目录 %s）：%d 个"
          % (",".join(sorted(INFRA_SKIP_DIRS)), len(paths)))

    kinds = {}
    for p in paths:
        kinds[p] = classify(p, read(p))

    # ── 本验证器自己算的编译型集 ──
    mine = set()
    mask_errors = []
    for p in paths:
        m, err = masked_text(p, read(p))
        if err:
            mask_errors.append("%s（★ %s）" % (p, err))
            continue
        if has_compile(m):
            mine.add(p)
    mine_scope = set(p for p in mine if not is_excluded(p, prefixes))

    print()
    print("=== ★ 形态分档（★ 这一张表就是「跳过桶的构成」）===")
    for k in ("prelude-shell", "prelude-python", "apply_to_environ", "env()", "export-gocache.sh", "other"):
        print("  %-20s %4d" % (k, sum(1 for p in paths if kinds[p] == k)))

    # ── 门导出的作用域集（★ 两把尺的第一把）──
    scope, scope_err = gate_scope_dump()
    print()
    print("=== ★ 静态作用域集（★ 由 B 级门导出 ⇒ ⇒ 两把尺同源）===")
    if scope is None:
        print("  ★ ★★ 没拿到：%s" % scope_err)
        print("  ★ ⇒ ⇒ 【交叉对拍未做】⇒ ⇒ 下面的结论只覆盖「本验证器自己算的那把尺」")
    else:
        print("  门认定（★ 编译型 ∧ 在作用域内）：%d 个" % len(scope))

    print()
    print("=== ★ 交叉对拍（★ 本验证器的独立实现 vs 门的认定）===")
    cross_failed = False
    if scope is not None:
        only_mine = sorted(mine_scope - scope)
        only_gate = sorted(scope - mine_scope)
        if not only_mine and not only_gate:
            print("  ✔ 两把尺**逐个一致**（★ %d 个）" % len(scope))
        else:
            cross_failed = True
            print("  ★ ★★ 两把尺**不一致**（★ ⇒ ⇒ 至少有一把歪了 ⇒ ⇒ 不能只报自己那把）")
            for p in only_mine:
                print("    ★ 本验证器算它编译型、门不算：%s" % p)
            for p in only_gate:
                print("    ★ 门算它编译型、本验证器不算：%s" % p)
    if mask_errors:
        cross_failed = True
        print("  ★ ★★ 掩码口径失败的脚本（★ ⇒ ⇒ 它的编译型判定**没有结论**）：")
        for e in mask_errors:
            print("    ★ " + e)

    # ── 运行期：逐个验 ──
    print()
    print("=== ★ 运行期读数（★ 「真跑一遍」而不是「文件里写着」）===")
    fails = []
    results = {}

    def check(kind, path, got, want):
        if got == want:
            results[kind] = results.get(kind, 0) + 1
        else:
            fails.append("%s（%s）：期望 %s，实测 %s" % (kind, path, want, got))

    # ★ 目标集 = 门认定的作用域集（★ 若拿不到 ⇒ 退回本验证器自己算的那把）★
    targets = sorted(scope) if scope is not None else sorted(mine_scope)
    print("  目标集（★ 要逐个验的编译型脚本）：%d 个" % len(targets))

    holes = []
    for p in targets:
        k = kinds[p]
        text = read(p)
        if k == "prelude-shell":
            got, err = probe_shell(shell_prelude(text), p)
            check(k, p, got, DEDICATED)
        elif k == "prelude-python":
            block = py_prelude(text)
            code = ("import os, sys\n"
                    + block.replace("__file__", '"%s"' % os.path.abspath(p))
                    + "\nprint(os.environ.get('GOCACHE','<EMPTY>'))\n")
            got, err = probe_python(code)
            check(k, p, got, DEDICATED)
        elif k == "apply_to_environ":
            got, err = probe_apply_to_environ()
            check(k, p, got, DEDICATED_MUTATE)
        elif k == "env()":
            got, err = probe_env()
            check(k, p, got, DEDICATED_MUTATE)
        elif k == "export-gocache.sh":
            got, err = probe_shell_exec()
            check(k, p, got, DEDICATED_MUTATE)
        else:
            # ★ ★★ **这才是 F4 真正要回答的** ★★：★ 一个【编译型】脚本 ⇒ ⇒
            # ★ ★★ 却**没有任何**被认的隔离形态 ⇒ ⇒ ★★ ⇒ 它静态就没人管、运行期也没人验
            holes.append(p)

    for k in ("prelude-shell", "prelude-python", "apply_to_environ", "env()", "export-gocache.sh"):
        print("  %-20s %4d / %4d" % (k, results.get(k, 0),
                                      sum(1 for p in targets if kinds[p] == k)))
    print("  %-20s %4d" % ("合计", sum(results.values())))

    print()
    print("=== ★ 「没有隔离形态」的编译型脚本（★ ★★ 这就是洞 ★★）===")
    if holes:
        for p in holes:
            print("  ★ " + p)
    else:
        print("  （无）")

    # ── 失败安全 ──
    fs = []
    code = (
        "import os, sys\n"
        "sys.path.insert(0, %r)\n" % LIB +
        "import gocache\n"
        "os.environ['GOCACHE'] = '/sentinel/global-cache'\n"
        "gocache.apply_to_environ('mutate', root=%r)\n" % BAD_ROOT +
        "print(os.environ.get('GOCACHE','<EMPTY>'))\n"
    )
    got, err = probe_python(code)
    if got != "/sentinel/global-cache":
        fs.append("apply_to_environ：建不出来时把 GOCACHE 变成了 %s（★ 期望【原样保留】）" % got)
    code2 = (
        "import os, sys\n"
        "sys.path.insert(0, %r)\n" % LIB +
        "import gocache\n"
        "os.environ['GOCACHE'] = '/sentinel/global-cache'\n"
        "e = gocache.env('mutate', root=%r)\n" % BAD_ROOT +
        "print(e.get('GOCACHE','<EMPTY>'))\n"
    )
    got2, err2 = probe_python(code2)
    if got2 != "/sentinel/global-cache":
        fs.append("env()：建不出来时把 GOCACHE 变成了 %s（★ 期望【原样保留】）" % got2)
    code3 = (
        "import os, sys\n"
        "sys.path.insert(0, %r)\n" % LIB +
        "import gocache\n"
        "os.environ['GOCACHE'] = ''\n"
        "e = gocache.env('mutate', root=%r)\n" % BAD_ROOT +
        "print('ABSENT' if 'GOCACHE' not in e else repr(e['GOCACHE']))\n"
    )
    got3, err3 = probe_python(code3)
    if got3 != "ABSENT":
        fs.append("env()：建不出来且原值为空串时，键仍在（★ 实测 %s，★ 期望【删掉】）" % got3)

    print()
    print("=== ★ 失败安全（★ 建不出来 ⇒ ⇒ 不许把 GOCACHE 指到用不了的路径）===")
    if fs:
        for x in fs:
            print("  ★ " + x)
    else:
        print("  ✔ 两种形态都【不覆盖】★ 且空串那个键被删掉（★ 共 3 项）")

    # ── 排除路径的实测计数（★ 与登记簿逐字对拍 ★）──
    print()
    print("=== ★ 排除路径的实测编译型计数（★ 与登记簿逐字对拍）===")
    reg_bad = []
    for e in reg.get("excludedPaths", []):
        actual = sum(1 for p in mine if p == e["path"] or p.startswith(e["path"] + "/"))
        mark = "✔" if actual == e["compileFiles"] else "★ ★★"
        print("  %s %-14s 登记 %d / 实测 %d" % (mark, e["path"], e["compileFiles"], actual))
        if actual != e["compileFiles"]:
            reg_bad.append("%s：登记 %d，实测 %d" % (e["path"], e["compileFiles"], actual))

    total_fail = len(fails) + len(fs) + len(holes) + len(reg_bad)
    if cross_failed:
        total_fail += 1
    print()
    if total_fail:
        print("★ ★★ 失败 %d 项（★ 运行期 %d + 失败安全 %d + 洞 %d + 登记簿 %d%s）⇒ ⇒ rc=1"
              % (total_fail, len(fails), len(fs), len(holes), len(reg_bad),
                 " + 交叉对拍 1" if cross_failed else ""))
        return 1
    print("★ 全部通过：★ 运行期 %d 项 + 失败安全 3 项 + 洞 0 + 登记簿逐字一致%s"
          % (sum(results.values()), " + 交叉对拍一致" if scope is not None else "（★ 未做交叉对拍）"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
