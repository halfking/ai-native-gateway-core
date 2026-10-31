#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""r1044 移动端行点击契约门「登记表自身重复」的变异台。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_r1044_mobile_list_ux_duplicate_registry.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★★ 背景（★ 为什么这道门本来不存在）：

★ 2026-10-28 实测：并行会话与本会话**各自**为
★ `web/src/components/ui/mobileListUx.spec.ts` 里的
★ `src/views/tenant/MaaSBillsView.vue:125` 登记了一条 NON_NAVIGABLE
★ （同一行号、两条不同理由），两边都推上 origin/main。
★ `git merge` **自动合并成功、零冲突** ⇒ 合并后的登记表里同一 (文件, 行号)
★ 出现了**两条登记** ⇒ 而本门**全绿** ⇒ ★ 「git 合干净了」≠「语义合干净了」。

★★ 为什么本门此前看不见：★ 登记集被收进 `Set` ⇒ `Set` 会**去重** ⇒
★   · `registered.size` 不变 ⇒ `measured.size === registered.size` 照常绿；
★   · `unregistered` / `stale` 都是集合差 ⇒ 照常绿；
★   · 两条「逐条遍历」断言把同一条跑两遍 ⇒ 照常绿。
★★ ⇒ **重复登记对既有断言完全不可见** —— 这是门自己的盲区，不是新增的规约。

★★ 本台覆盖的 3 个臂：

★   M01 ★★★ 在 NON_NAVIGABLE 里注入一条**重复登记**（真实实例的复刻）
★        ⇒ 期望：**只有**新断言「登记表自身不得有重复的 (文件, 行号)」转红，
★          ★ 且**其余 7 条既有断言全部照常绿** ——
★          ★★ 「其余全绿」才是本臂的价值：它证明**盲区是真的**
★          ★★ （既有断言确实看不见重复），而不是新断言顺手把别的也拦了。
★   M02 ★★ 在 ASYMMETRY 里注入一条**重复登记** ⇒ 期望同上。
★★   M03 ★★★ **掏空**新断言的判据（让 `seen` 永远为空 ⇒ 断言恒真）
★        ★★ 同时保留 M01 的重复登记 ⇒ 期望：**全部 8 条断言转绿**
★★        ⇒ ⇒ 这一臂同时证明两件事：
★★          ① **归因**：M01 的红确实是这条断言的牙，不是别的；
★★          ② **非空洞**：这条断言不是恒真（恒真的断言会「因为没有红」而被误判成没牙）。

★★ 纪律（★ 与 scripts/audit/mutate_r1041_session_body_bound.py 同构）：

★   ① 还原必须按【原路径】做 **md5 逐字校验**；
★   ② 变异必须改【全部】出现处（★ 且要声明改了几处）；
★   ③ 变异必须【语义变了但仍能跑】（★ 「跑不起来」按纪律**不算数**）；
★   ④ 环境指纹对拍（★ 共享工作树里 ⇒ ⇒ 并发改动会被误读成「门没牙」）；
★   ⑤ 变异必须落在【判据实际读到的那一处】；
★   ⑥ 每个变异都必须自带【效果探针】；
★★ ⑦ ★★★ **「没牙」要先过【惰性变异】筛查**——★ 先跑一遍【未变异】的
★★      基线，确认它本来就绿 ⇒ 否则「变异后红」可能只是【基线本来就红】；
★★ ⑧ ★★★ 逐条判定【是哪一条断言红了】⇒ 只看 rc 不算数（★ rc=1 可能来自
★★      任何一条 ⇒ 必须指名道姓到【断言名】）。
"""

import hashlib
import json
import os
import subprocess
import sys
import tempfile

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))
WEB = os.path.join(ROOT, "web")

TARGET_REL = "src/components/ui/mobileListUx.spec.ts"
TARGET = os.path.join(WEB, TARGET_REL)

NODE_BIN = os.path.join(os.path.expanduser("~"), ".nvm", "versions", "node", "v22.23.3", "bin")

# ★ 新断言的**完整名**（vitest JSON reporter 的 fullName = describe 名 + it 名）。
NEW_ASSERTION = "登记表自身不得有重复的 (文件, 行号)"

# ── 变异锚点 ────────────────────────────────────────────────────────────

# M01：真实实例的复刻 —— 同一 (文件, 行号) 再登记一条，理由不同。
M01_NEEDLE = (
    "  ['src/views/tenant/MaaSBillsView.vue', 125, "
    "'月账单内嵌表（账期聚合行，title-key=period_ym）：/tenant/bills 无账单详情页，本视图无二级目的地'],\n"
)
M01_REPL = M01_NEEDLE + (
    "  ['src/views/tenant/MaaSBillsView.vue', 125, "
    "'月账单内嵌表（变异臂 M01：重复登记），无账单详情页'],\n"
)

# M02：ASYMMETRY 侧的重复登记（另一个登记表，同样没有重复检查）。
M02_NEEDLE = "const ASYMMETRY: Array<[string, number, string]> = [\n"
M02_REPL = M02_NEEDLE + (
    "  [\n"
    "    'src/views/TenantDashboardView.vue',\n"
    "    718,\n"
    "    '变异臂 M02：同族 ASYMMETRY 的重复登记条目（理由长度刻意 >20 以绕过既有断言）',\n"
    "  ],\n"
)

# M03：掏空新断言的判据 ⇒ 断言恒真（惰性变异）。
M03_NEEDLE = (
    "    const seen = new Map<string, number>()\n"
    "    for (const [f, l] of [...NON_NAVIGABLE, ...KNOWN_GAP, ...ASYMMETRY]) {\n"
    "      const k = key(f, l)\n"
    "      seen.set(k, (seen.get(k) ?? 0) + 1)\n"
    "    }\n"
)
M03_REPL = (
    "    const seen = new Map<string, number>()\n"
    "    const _hollowed = [...NON_NAVIGABLE, ...KNOWN_GAP, ...ASYMMETRY]\n"
    "    void _hollowed\n"
)

# ───────────────────────── 基础工具 ─────────────────────────


def snap(path):
    if not os.path.exists(path):
        return None
    with open(path, "rb") as f:
        return f.read()


def md5(data):
    if data is None:
        return "MISSING"
    return hashlib.md5(data).hexdigest()


def node_env():
    env = dict(os.environ)
    env["PATH"] = NODE_BIN + os.pathsep + env.get("PATH", "")
    return env


def run_spec():
    """跑一次目标 spec，返回 (rc, [(status, fullName), ...])。"""
    out_file = os.path.join(tempfile.gettempdir(), "mutate_r1044_vitest.json")
    if os.path.exists(out_file):
        os.remove(out_file)
    proc = subprocess.run(
        ["npx", "vitest", "run", TARGET_REL, "--reporter=json", "--outputFile=" + out_file],
        cwd=WEB,
        env=node_env(),
        capture_output=True,
        text=True,
        timeout=600,
    )
    if not os.path.exists(out_file):
        raise RuntimeError("vitest 没有产出 JSON 报告（rc=%s）\n%s\n%s"
                           % (proc.returncode, proc.stdout[-2000:], proc.stderr[-2000:]))
    with open(out_file, encoding="utf-8") as f:
        data = json.load(f)
    results = []
    for suite in data.get("testResults", []):
        for a in suite.get("assertionResults", []):
            results.append((a.get("status"), a.get("fullName")))
    return proc.returncode, results


def failed_names(results):
    return [n for s, n in results if s != "passed"]


def replace_once(text, needle, repl):
    n = text.count(needle)
    if n != 1:
        raise AssertionError("锚点命中 %d 次（期望恰好 1 次）⇒ 变异没有落在唯一一处" % n)
    return text.replace(needle, repl, 1)


# ───────────────────────── 台账 ─────────────────────────

LEDGER = []


def record(arm, expect, got, ok):
    LEDGER.append({"arm": arm, "expect": expect, "got": got, "ok": ok})
    mark = "OK " if ok else "FAIL"
    print("[%s] %s" % (mark, arm))
    print("       期望：%s" % expect)
    print("       实测：%s" % got)


def main() -> int:
    if not os.path.exists(TARGET):
        print("目标文件不存在：%s" % TARGET, file=sys.stderr)
        return 2

    original = snap(TARGET)
    base_md5 = md5(original)
    print("目标：%s" % TARGET_REL)
    print("基线 md5：%s" % base_md5)
    print("")

    # ── 纪律 ⑦：惰性变异筛查 —— 先确认基线本来就绿 ──────────────
    rc, results = run_spec()
    baseline_failed = failed_names(results)
    if rc != 0 or baseline_failed:
        record(
            "B00 基线（惰性变异筛查）",
            "rc=0 且 0 条断言失败（★ 基线本来就绿 ⇒ 否则「变异后红」可能只是基线本来就红）",
            "rc=%s，失败：%s" % (rc, baseline_failed or "无"),
            False,
        )
        return 1
    record(
        "B00 基线（惰性变异筛查）",
        "rc=0 且 0 条断言失败",
        "rc=0，%d 条断言全绿" % len(results),
        True,
    )
    total = len(results)

    def restore():
        with open(TARGET, "wb") as f:
            f.write(original)
        return md5(snap(TARGET)) == base_md5

    try:
        # ── M01：NON_NAVIGABLE 侧重复登记 ────────────────────────
        with open(TARGET, encoding="utf-8") as f:
            text = f.read()
        with open(TARGET, "w", encoding="utf-8") as f:
            f.write(replace_once(text, M01_NEEDLE, M01_REPL))

        rc, results = run_spec()
        failed = failed_names(results)
        ok = (
            rc != 0
            and len(failed) == 1
            and NEW_ASSERTION in failed[0]
            and len(results) == total
        )
        record(
            "M01 NON_NAVIGABLE 注入重复登记",
            "rc≠0；★ **只有**「%s」红；★ 其余 %d 条既有断言**全部照常绿**"
            "（★ 这才是本臂的价值：证明既有断言确实看不见重复）"
            % (NEW_ASSERTION, total - 1),
            "rc=%s，红 %d 条：%s" % (rc, len(failed), failed or "无"),
            ok,
        )
        if not restore():
            print("M01 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

        # ── M02：ASYMMETRY 侧重复登记 ────────────────────────────
        with open(TARGET, encoding="utf-8") as f:
            text = f.read()
        with open(TARGET, "w", encoding="utf-8") as f:
            f.write(replace_once(text, M02_NEEDLE, M02_REPL))

        rc, results = run_spec()
        failed = failed_names(results)
        ok = (
            rc != 0
            and len(failed) == 1
            and NEW_ASSERTION in failed[0]
            and len(results) == total
        )
        record(
            "M02 ASYMMETRY 注入重复登记",
            "rc≠0；★ **只有**「%s」红；★ 其余 %d 条既有断言全部照常绿"
            % (NEW_ASSERTION, total - 1),
            "rc=%s，红 %d 条：%s" % (rc, len(failed), failed or "无"),
            ok,
        )
        if not restore():
            print("M02 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

        # ── M03：掏空新断言 + 保留 M01 的重复登记 ────────────────
        with open(TARGET, encoding="utf-8") as f:
            text = f.read()
        text = replace_once(text, M01_NEEDLE, M01_REPL)
        text = replace_once(text, M03_NEEDLE, M03_REPL)
        with open(TARGET, "w", encoding="utf-8") as f:
            f.write(text)

        rc, results = run_spec()
        failed = failed_names(results)
        ok = rc == 0 and not failed
        record(
            "M03 掏空新断言的判据（判据恒真）+ 保留 M01 的重复登记",
            "rc=0 且 **全部 %d 条断言转绿** ⇒ ⇒ 归因（M01 的红确实来自这条断言）"
            " + 非空洞（这条断言不是恒真）" % total,
            "rc=%s，失败：%s" % (rc, failed or "无"),
            ok,
        )
        if not restore():
            print("M03 还原后 md5 不一致 ⇒ 中止", file=sys.stderr)
            return 1

    finally:
        # ── 纪律 ①：还原必须按原路径做 md5 逐字校验 ────────────
        with open(TARGET, "wb") as f:
            f.write(original)
        now = md5(snap(TARGET))
        if now != base_md5:
            print("★ 最终还原校验失败：%s != %s" % (now, base_md5), file=sys.stderr)
            return 1

    print("")
    passed = sum(1 for r in LEDGER if r["ok"])
    print("变异台账：%d/%d 通过" % (passed, len(LEDGER)))
    for r in LEDGER:
        print("  %s %s" % ("OK " if r["ok"] else "FAIL", r["arm"]))
    print("还原校验：md5 %s（与基线一致）" % base_md5)
    return 0 if passed == len(LEDGER) else 1


if __name__ == "__main__":
    sys.exit(main())
