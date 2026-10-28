#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""r1041 会话正文 1 MiB 上限的变异台：★ 验证「守卫绿 ≠ 守卫有牙」。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_r1041_session_body_bound.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★★ 纪律（★ 与 scripts/audit/mutate_r1040_address_family_gates.py 同构）：

★   ① 还原必须按【原路径】做 **md5 逐字校验**；
★   ② 变异必须改【全部】出现处（★ 且要声明改了几处）；
★   ③ 变异必须【语义变了但仍能编译】（★ 编译红 ≠ 断言红 ⇒ 不算数）；
★   ④ 环境指纹对拍（★ 共享工作树里 ⇒ ⇒ 并发改动会被误读成「门没牙」）；
★   ⑤ 变异必须落在【判据实际读到的那一处】；
★   ⑥ 每个变异都必须自带【效果探针】——★ 「目标 needle 在【判据实际读的
★      那一面】里的出现次数变了」+「文件内容确实变了」；
★★ ⑦ ★★★ **探针的【方向】必须与变异的方向一致**；
★★ ⑧ ★★★ **「没牙」要先过【惰性变异】筛查**——★ 用判据自己的谓词复算一次
★      （★ 本台 = 先跑一遍【未变异】的基线，确认它本来就绿 ⇒ 否则
★      「变异后红」可能只是【基线本来就红】）。

★★ 本台覆盖的 5 个臂（★ 每个臂对应一条真实修过的缺陷）：

★   M01 ★★ 「额度用完 ⇒ 原样放行」⇒ 共享预算下总量失控（★ 旧实现 3.67 MiB）
★   M02 ★★ 三方向【各】拿一半 ⇒ 总量 1.5 MiB（★ 契约说 1 MiB）
★   M03 ★★ 按【原始字节】裁 ⇒ 转义膨胀撑破上限（★ 旧实现超 50%）
★   M04 ★★ ContentRaw【任意字节截断】⇒ 产出非法 JSON ⇒ 那一轮写不进去
★   M05 ★★ 数组框架漏算 ⇒ 记账偏差传递到后面的方向 ⇒ 总量溢出
"""

import hashlib
import os
import subprocess
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

GO_BIN = os.path.join(os.path.expanduser("~"), "sdk", "go", "bin")

TARGET = os.path.join(ROOT, "domains/session/v2/bodies_bound.go")
PKG = "./domains/session/v2/"

CAP_TEST = "TestBoundBodiesRecord_TotalIsCappedAtOneMiB"
ESC_TEST = "TestBoundBodiesRecord_EscapingDoesNotBreakCap"
JSON_TEST = "TestBoundBodiesRecord_TruncatedContentRawStaysValidJSON"

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


def go_env():
    env = dict(os.environ)
    env["PATH"] = GO_BIN + os.pathsep + env.get("PATH", "")
    return env


def run_go(args, timeout=600):
    return subprocess.run(
        ["go"] + args,
        cwd=ROOT,
        env=go_env(),
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        timeout=timeout,
    )


def run_test(test_name, timeout=600):
    return run_go(["test", PKG, "-run", test_name, "-count=1", "-v"], timeout=timeout)


# ───────────────────────── 变异臂定义 ─────────────────────────
# needle / replacement 必须【逐字】匹配当前实现（★ 改实现时本台会自己红，
# ★ 那说明臂的定义该跟着更新 —— ★ 而不是把臂删掉）。

ARMS = [
    {
        "id": "M01",
        "desc": "「额度用完 ⇒ 原样放行」被放回来（★ 共享预算下总量失控）",
        "needle": "\tif budget <= 0 {\n\t\treturn nil, true\n\t}",
        "replacement": "\tif budget <= 0 {\n\t\treturn msgs, false\n\t}",
        "test": CAP_TEST,
    },
    {
        "id": "M02",
        "desc": "三方向【各】拿一半预算（★ 总量 1.5 MiB）",
        "needle": (
            "\trec.RequestDelta, cut = boundMessages(rec.RequestDelta, budget)\n"
            "\ttruncated = truncated || cut\n"
            "\tbudget = remainingBudget(budget, rec.RequestDelta)\n"
            "\n"
            "\trec.ResponseDelta, cut = boundMessages(rec.ResponseDelta, budget)\n"
            "\ttruncated = truncated || cut\n"
            "\tbudget = remainingBudget(budget, rec.ResponseDelta)\n"
        ),
        "replacement": (
            "\trec.RequestDelta, cut = boundMessages(rec.RequestDelta, MaxTurnBodyBytes/2)\n"
            "\ttruncated = truncated || cut\n"
            "\tbudget = MaxTurnBodyBytes / 2\n"
            "\n"
            "\trec.ResponseDelta, cut = boundMessages(rec.ResponseDelta, MaxTurnBodyBytes/2)\n"
            "\ttruncated = truncated || cut\n"
            "\tbudget = MaxTurnBodyBytes / 2\n"
        ),
        "test": CAP_TEST,
    },
    {
        "id": "M03",
        "desc": "按【原始字节】裁（★ 转义膨胀撑破上限）",
        "needle": "\t\tm.Content = fitTextToLimit(m.Content, m, limit)",
        "replacement": (
            "\t\tn := limit - 24\n"
            "\t\tif n < 0 {\n\t\t\tn = 0\n\t\t}\n"
            "\t\tif n > len(m.Content) {\n\t\t\tn = len(m.Content)\n\t\t}\n"
            "\t\tm.Content = m.Content[:n]"
        ),
        "test": ESC_TEST,
    },
    {
        "id": "M04",
        "desc": "ContentRaw【任意字节截断】（★ 产出非法 JSON ⇒ 写不进去）",
        "needle": (
            "\t\tif shrunk, ok := shrinkRawToFit(m.ContentRaw, limit, m); ok {\n"
            "\t\t\tm.ContentRaw = shrunk\n"
            "\t\t} else {\n"
            "\t\t\t// ★ 收缩不动（形状不可收缩 / 原始值本就不是合法 JSON）\n"
            "\t\t\t// ★ ⇒ ★ 丢弃正文，保住 JSON 合法性与 role / tool_calls 等结构。\n"
            "\t\t\tm.ContentRaw = nil\n"
            "\t\t}\n"
        ),
        "replacement": (
            "\t\t{\n"
            "\t\t\tn := limit - 24\n"
            "\t\t\tif n < 0 {\n\t\t\t\tn = 0\n\t\t\t}\n"
            "\t\t\tif n > len(m.ContentRaw) {\n\t\t\t\tn = len(m.ContentRaw)\n\t\t\t}\n"
            "\t\t\tm.ContentRaw = m.ContentRaw[:n]\n"
            "\t\t}\n"
        ),
        "test": JSON_TEST,
    },
    {
        "id": "M05",
        "desc": "数组框架漏算（★ 记账偏差传递 ⇒ 总量溢出）",
        "needle": (
            "\tif b, err := json.Marshal(msgs); err == nil {\n"
            "\t\treturn len(b)\n"
            "\t}"
        ),
        "replacement": (
            "\tif b, err := json.Marshal(msgs); err == nil {\n"
            "\t\treturn len(b) - 2 - (len(msgs) - 1)\n"
            "\t}"
        ),
        "test": CAP_TEST,
    },
]


# ───────────────────────── 主流程 ─────────────────────────

def main():
    baseline = snap(TARGET)
    if baseline is None:
        print("✗ 目标文件不存在：%s" % TARGET)
        return 2
    baseline_md5 = md5(baseline)
    print("★ 目标文件：%s" % os.path.relpath(TARGET, ROOT))
    print("★ 基线 md5：%s" % baseline_md5)
    print("")

    # ★★ 惰性变异筛查：★ 基线本来就绿 ⇒ 后面「变异后红」才是变异造成的。
    for arm in ARMS:
        r = run_test(arm["test"])
        if r.returncode != 0:
            print("✗ 基线就不绿（%s / %s）⇒ ★ 后面的红绿读数不可归因 ⇒ 先修基线"
                  % (arm["id"], arm["test"]))
            sys.stdout.write(r.stdout.decode("utf-8", "replace")[-3000:])
            return 2
    print("★ 惰性变异筛查：5 个臂的基线测试【全部本来就绿】✓")
    print("")

    failures = []
    for arm in ARMS:
        arm_id = arm["id"]
        needle = arm["needle"]
        replacement = arm["replacement"]

        # ── 效应探针 ①：needle 在文件里【恰好出现一次】──
        count = baseline.count(needle.encode("utf-8"))
        if count != 1:
            print("✗ %s 探针失败：needle 在文件里出现 %d 次（★ 期望恰好 1 次）"
                  % (arm_id, count))
            failures.append(arm_id)
            continue

        # ── 施加变异 ──
        mutated = baseline.replace(needle.encode("utf-8"), replacement.encode("utf-8"))
        with open(TARGET, "wb") as f:
            f.write(mutated)

        # ── 效应探针 ②：文件内容确实变了 ──
        after = snap(TARGET)
        if md5(after) == baseline_md5:
            print("✗ %s 探针失败：施加变异后 md5 未变 ⇒ ★ 变异没生效" % arm_id)
            failures.append(arm_id)
            continue
        if after.count(needle.encode("utf-8")) != 0:
            print("✗ %s 探针失败：变异后 needle 仍在 ⇒ ★ 变异没生效" % arm_id)
            failures.append(arm_id)
            continue

        # ── 编译门（★ 编译红 ≠ 断言红 ⇒ 不算数）──
        c = run_go(["build", "./..."])
        if c.returncode != 0:
            print("✗ %s 编译失败 ⇒ ★ 本臂不算数（★ 编译红 ≠ 断言红）" % arm_id)
            sys.stdout.write(c.stdout.decode("utf-8", "replace")[-2000:])
            failures.append(arm_id)
            with open(TARGET, "wb") as f:
                f.write(baseline)
            continue

        # ── 跑判据（★ 期望【红】）──
        r = run_test(arm["test"])
        red = r.returncode != 0
        print("── %s %s" % (arm_id, arm["desc"]))
        print("   判据：%s" % arm["test"])
        print("   期望：红（检出）　实测：%s" % ("红 ✓" if red else "绿 ✗（没牙！）"))
        if not red:
            failures.append(arm_id)
            sys.stdout.write(r.stdout.decode("utf-8", "replace")[-2500:])
        else:
            out = r.stdout.decode("utf-8", "replace")
            for line in out.splitlines():
                if "FAIL" in line and "---" in line:
                    print("   命中：%s" % line.strip())
                    break

        # ── 还原（★ 按原路径 + md5 逐字校验）──
        with open(TARGET, "wb") as f:
            f.write(baseline)
        restored = snap(TARGET)
        if md5(restored) != baseline_md5:
            print("   ✗ 还原后 md5 不一致（★ %s vs %s）" % (md5(restored), baseline_md5))
            failures.append(arm_id)
        print("")

    print("=" * 68)
    if failures:
        print("✗ 变异台结果：%d/%d 臂未达预期 ⇒ %s" %
              (len(failures), len(ARMS), ", ".join(failures)))
        return 1
    print("✓ 变异台结果：%d/%d 臂全部【检出】（★ 绿 ⇒ 有牙）" % (len(ARMS), len(ARMS)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
