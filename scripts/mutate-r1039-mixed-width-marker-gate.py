#!/usr/bin/env python3
"""r1039 混拼标记门的变异脚本 —— 证明门有牙。

★★ **隔离副本模式**（承 §143.36 / §143.37 的 LESSONS「共享仓的变异脚本必须
用隔离副本」）：本仓是**共享仓 + 并行会话** ⇒ 就地改共享工作树的源文件时，
若并行会话恰好 `git commit -a` ⇒ **被故意改坏的那一行会被推上 main**
⇒ 那是「一个活着的缺陷」，不是「一个脏文件」。
⇒ **全部变异只在 `git archive HEAD` 导出的副本里发生**，共享工作树**一个字节都不动**；
收尾再核一次「共享工作树的已跟踪文件未被改动」。

本轮要证明的：**混拼形态（半角全角混用）必须被看见**，且这个判定
  ① 在【折宽前置门】上承重（门不放行 ⇒ 折宽副本不构造 ⇒ 扫描器看不见）；
  ② 在【逐字符宽度判定】上承重（宽度判定坏了 ⇒ 全角/混拼看不见）；
  ③ 不是「门永远为真」（否则普通中文全被拖进昂贵路径 ⇒ 误报 + 成本双杀）；
  ④ 必须与 internal/markerbound 这个**刻意副本**同步（不同步 ⇒ 截断切点把
     混拼标记切成两半 ⇒ 半截标记随上游跑掉）；
  ⑤ 不能过宽（把 9 个任意 ASCII 字母都当标记 ⇒ 散文被误判）。

其余纪律（承 §143.35 / §143.36）：
  · 文件级备份 + md5 逐字校验（禁用 git checkout --）
  · 替换串不得包含 needle 作为子串
  · needle 必须唯一（否则 Contains 型门只保护「至少有一处」⇒ 假绿）
  · ★★ 变异必须【编译干净再验】（编译红 ≠ 断言红）
  · 变异后必须逐字还原

用法：
    python3 scripts/mutate-r1039-mixed-width-marker-gate.py [REPO_ROOT]
"""
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else ".")

SANITIZE_REL = "security/sanitize/placeholder_loose.go"
MARKERBOUND_REL = "internal/markerbound/markerbound.go"

TEST_RE = "TestR1039MixedWidth"

# 每条变异：(编号, 说明, 目标文件, needle, repl, 期望红的判据正则)
MUTATIONS = [
    ("M1",
     "折宽前置门退回「只认全 ASCII 词」（= 修复前的真实形态）⇒ 折宽副本不构造 ⇒ 混拼形态不可见",
     SANITIZE_REL,
     "\tif !containsMarkerWordAnyWidth(text) {\n",
     "\tif !containsFoldCaseWord(text, placeholderMarkerWord) {\n",
     "TestR1039MixedWidth"),

    ("M2",
     "逐字符判定里的全角分支失效（非 ASCII 一律按 ASCII 处理）⇒ 全角/混拼形态不可见",
     SANITIZE_REL,
     "\t\tletter, ok := fullWidthLetterAt(text, i)\n",
     "\t\tletter, ok := byte(text[i]), true\n",
     "TestR1039MixedWidth"),

    ("M3",
     "门永远为真（=「修漏检」的最省事写法）⇒ 普通中文全被拖进昂贵路径 ⇒ 误报 + 成本双杀",
     SANITIZE_REL,
     "func containsMarkerWordAnyWidth(text string) bool {\n\t// 候选 1：",
     "func containsMarkerWordAnyWidth(text string) bool {\n\tif true {\n\t\treturn true\n\t}\n\t// 候选 1：",
     "TestR1039MixedWidth"),

    ("M4",
     "刻意副本 internal/markerbound 没同步（只改 sanitize）⇒ 截断切点会把混拼标记切成两半",
     MARKERBOUND_REL,
     "\t\tletter, ok := fullWidthLetterAt(text, i)\n",
     "\t\tletter, ok := byte(text[i]), true\n",
     "TestR1039MixedWidthSpanContractMatchesMarkerbound"),

    ("M5",
     "逐字符判定过宽（ASCII 位置不再比对字母）⇒ 9 个任意 ASCII 字母都被当标记 ⇒ 散文被误判",
     SANITIZE_REL,
     "\t\t\tif asciiLower(text[i]) != word[k] {\n\t\t\t\treturn false\n\t\t\t}\n",
     "\t\t\tif false {\n\t\t\t\treturn false\n\t\t\t}\n",
     "TestR1039MixedWidth"),
]



# stdout/stderr 必须显式配 UTF-8：这些脚本会打印含「⇒」等字符的中文，
# 而 cp936（Windows 中文默认）编码不了 ⇒ print() 抛 UnicodeEncodeError
# ⇒ 门在出读数时就崩了。在 UTF-8 locale 下本段是 no-op，POSIX 行为不变。
for _stream in (sys.stdout, sys.stderr):
    try:
        _stream.reconfigure(encoding='utf-8')
    except (AttributeError, ValueError):
        pass

def md5(path):
    with open(path, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def run_go(cwd, args):
    env = dict(os.environ)
    env.setdefault("GOCACHE", "/tmp/gocache-r1039-mut")
    env.setdefault("CGO_ENABLED", "0")
    return subprocess.run(args, cwd=cwd, env=env, capture_output=True, encoding='utf-8', errors='replace')


def tracked_dirty(repo):
    """返回工作树里【已跟踪文件】的改动列表（未跟踪文件不算）。"""
    p = subprocess.run(["git", "status", "--porcelain", "--untracked-files=no"],
                       cwd=repo, capture_output=True, encoding='utf-8', errors='replace')
    return [l for l in p.stdout.splitlines() if l.strip()]


def main():
    # ── 前置条件：工作树的已跟踪文件必须干净（否则副本不是 HEAD 那棵树）──
    dirty = tracked_dirty(REPO)
    if dirty:
        print("REFUSED: 工作树里【已跟踪文件】有未提交改动 ⇒ 导出的副本不是 HEAD ⇒ 拒绝运行：")
        for d in dirty:
            print("   ", d)
        return 2

    # ── 导出 HEAD 副本 ───────────────────────────────────────────────
    work = tempfile.mkdtemp(prefix="r1039-mut-")
    p = subprocess.run(["git", "archive", "HEAD"], cwd=REPO, capture_output=True)
    if p.returncode != 0:
        print("REFUSED: git archive HEAD 失败:", p.stderr.decode()[:400])
        return 2
    tar = os.path.join(work, "head.tar")
    with open(tar, "wb") as f:
        f.write(p.stdout)
    tree = os.path.join(work, "tree")
    os.makedirs(tree, exist_ok=True)
    p = subprocess.run(["tar", "-x", "-C", tree, "-f", tar], capture_output=True, encoding='utf-8', errors='replace')
    if p.returncode != 0:
        print("REFUSED: 解包失败:", p.stderr[:400])
        return 2

    targets = {}
    for rel in (SANITIZE_REL, MARKERBOUND_REL):
        path = os.path.join(tree, rel)
        if not os.path.exists(path):
            print(f"REFUSED: 副本里没有 {rel}")
            return 2
        targets[rel] = (path, open(path, "rb").read(), md5(path),
                        os.path.join(work, rel.replace("/", "_") + ".bak"))

    results = []
    try:
        for mid, desc, rel, needle, repl, want_red in MUTATIONS:
            path, orig, orig_md5, backup = targets[rel]
            shutil.copy2(path, backup)

            nb = needle.encode()
            n = orig.count(nb)
            if n != 1:
                print(f"❌ {mid}: needle 在 {rel} 出现 {n} 次（必须恰好 1 次）⇒ 作废")
                results.append((mid, "SKIP-needle", False))
                continue
            if needle in repl:
                print(f"❌ {mid}: 替换串包含 needle 作为子串 ⇒ 作废")
                results.append((mid, "SKIP-substr", False))
                continue

            open(path, "wb").write(orig.replace(nb, repl.encode(), 1))

            # ★ 编译必须干净（编译红 ≠ 断言红）
            cp = run_go(tree, ["go", "vet", "./security/sanitize/", "./internal/markerbound/"])
            if cp.returncode != 0:
                print(f"❌ {mid}: 变异后【编译不干净】⇒ 读数作废：\n{cp.stdout[-800:]}{cp.stderr[-800:]}")
                results.append((mid, "COMPILE-RED", False))
                open(path, "wb").write(orig)
                continue

            tp = run_go(tree, ["go", "test", "./security/sanitize/", "-run", TEST_RE, "-count=1", "-v"])
            out = tp.stdout + tp.stderr
            red = tp.returncode != 0
            hit = want_red in out
            if red and hit:
                print(f"✅ {mid}: 判据变红（{desc}）")
                results.append((mid, "RED-OK", True))
            elif not red:
                print(f"❌ {mid}: 变异后判据仍绿 ⇒ 门无牙（{desc}）")
                results.append((mid, "STILL-GREEN", False))
            else:
                print(f"❌ {mid}: 红了但红在别的判据上（期望 {want_red}）")
                results.append((mid, "RED-OTHER", False))

            # 逐字还原 + md5 校验
            open(path, "wb").write(orig)
            if md5(path) != orig_md5:
                print(f"❌ {mid}: 还原后 md5 不一致 ⇒ 副本被改脏")
                results.append((mid, "RESTORE-MD5", False))
            else:
                shutil.copy2(backup, path)
    finally:
        print("\n收尾：核一次共享工作树未被改动 …")
        after = tracked_dirty(REPO)
        if after:
            print("❌ 共享工作树有已跟踪文件被改动：")
            for d in after:
                print("   ", d)
        else:
            print("✅ 共享工作树的已跟踪文件未被改动")
        shutil.rmtree(work, ignore_errors=True)

    ok = sum(1 for _, _, good in results if good)
    print(f"\n结果：{ok}/{len(results)} 条变异按预期把判据打红")
    for mid, why, good in results:
        print(f"  {mid}: {why}")
    return 0 if ok == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())
