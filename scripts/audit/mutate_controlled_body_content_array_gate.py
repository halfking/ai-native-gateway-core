#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""mutate_controlled_body_content_array_gate.py —— `:response` 槽位「收下 content
分片数组」这道改动的变异台。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_controlled_body_content_array_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 本台要回答的问题（★ 别把它读成别的）：
★   「`:response` 槽位收下 content 数组」这个改动，★ **有没有被测试咬住**？
★   判据是 `admin/body_resolver_content_array_test.go`。
★   ⇒ 变异把改动改坏 ⇒ ★ **判据必须变红**；★ 判据不红 ⇒ ★ 这道改动没有护栏。

★ ★★ 本台沿用本仓已有的纪律（★ 与 §143.96–§143.99 同构）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须改【全部】出现处（★ 且要声明改了几处）；
★   ③ 变异必须【语义变了但仍能编译】—— ★ **编译红不算数**（★ 编译红只说明
★      代码坏了，★ 不说明判据有牙）；★ ⇒ 每条臂跑完后先看退出码，★ 编译红
★      单独报为「无效变异」；
★   ④ 环境指纹对拍（★ 共享工作树里 ⇒ 并发改动会被误读成「门没牙」）；
★   ⑤ 变异必须落在【判据实际读到的那一处】；
★   ⑥ ★★ 每条臂都必须声明【它该让哪条断言变红】—— ★ **红在别处 ＝ 这条臂
★      没被测到**（★ 与 ai-session-manager 第十五轮同一课）。
★   ⑦ ★★ 跑变异台前必须先 commit 或 stash —— ★ 否则读数不可信。
"""

import hashlib
import os
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

IMPL = os.path.join(ROOT, "admin", "body_resolver.go")
GATE = os.path.join(ROOT, "admin", "body_resolver_content_array_test.go")

# ★ 判据实际读的那一处：`normalizeControlledResponse` 里调用受控文本提取的那行。
CALLSITE = "if content, ok := controlledText(message.Content); ok {"
CALLSITE_OLD = "if content, ok := controlledString(message.Content); ok {"

# ★ 每条臂：(名称, 改坏的说法, 期望变红的断言, [(锚点, 替换物), ...])
ARMS = (
    (
        "M01",
        "the response slot goes back to accepting plain strings only: the "
        "content array is refused again",
        "a content array with a text part must be served",
        [(CALLSITE, CALLSITE_OLD)],
    ),
    (
        "M02",
        "the text parts are joined with an empty string instead of a newline: "
        "two blocks get glued into one word",
        "\u7b2c\u4e00\u5757\\n\u7b2c\u4e8c\u5757",
        [('return strings.Join(texts, "\\n"), true',
          'return strings.Join(texts, ""), true')],
    ),
    (
        "M03",
        "a part without a `text` field is appended as an empty block: a "
        "non-text part (an image) now leaves a hole in the answer",
        "a part without text contributes nothing",
        # ★ 第一版这条臂只把 `|| piece.Text == nil` 去掉 ⇒ ★ 那会**解引用 nil**
        #   ⇒ 报出来的是 panic 而不是断言红 ⇒ ★ 那是「臂自己写坏了」，
        #   不是「判据没咬住」⇒ ★ 改成【语义上真的改坏、且仍能编译】的形态。
        [("\t\tif err := json.Unmarshal(part, &piece); err != nil || piece.Text == nil {\n"
          "\t\t\tcontinue\n"
          "\t\t}\n",
          "\t\tif err := json.Unmarshal(part, &piece); err != nil {\n"
          "\t\t\tcontinue\n"
          "\t\t}\n"
          "\t\tif piece.Text == nil {\n"
          "\t\t\ttexts = append(texts, \"\")\n"
          "\t\t\tcontinue\n"
          "\t\t}\n")],
    ),
    (
        "M04",
        "a blank text part is kept: whitespace-only content is served as if it "
        "were an answer",
        "a blank text part is not text",
        [("\t\tif strings.TrimSpace(*piece.Text) == \"\" {\n"
          "\t\t\tcontinue\n"
          "\t\t}\n",
          "")],
    ),
    (
        "M05",
        "the array branch is dropped: only a plain string is readable again",
        "a content array with a text part must be served",
        [("\tvar parts []json.RawMessage\n"
          "\tif err := json.Unmarshal(raw, &parts); err != nil {\n"
          "\t\treturn \"\", false\n"
          "\t}\n",
          "\tvar parts []json.RawMessage\n"
          "\tif err := json.Unmarshal(raw, &parts); err != nil {\n"
          "\t\treturn \"\", false\n"
          "\t}\n"
          "\treturn \"\", false\n")],
    ),
    (
        "M06",
        "the choices[].message.content path still uses the plain-string "
        "reader: the OpenAI non-streaming shape stays invisible",
        "the choices[].message.content array must be served",
        [("\t\t\tif content, ok := controlledText(choice.Message.Content); ok {",
          "\t\t\tif content, ok := controlledString(choice.Message.Content); ok {")],
    ),
    (
        "M07",
        "the role filter is dropped: a user message's content array is served "
        "as if it were the assistant's answer",
        "a user message is still not the assistant's answer",
        [("\t\tfor _, message := range array {\n"
          "\t\t\tif message.Role != \"\" && message.Role != \"assistant\" {\n"
          "\t\t\t\tcontinue\n"
          "\t\t\t}\n",
          "\t\tfor _, message := range array {\n")],
    ),
)

backups = {}


def snap(path):
    if path not in backups:
        with open(path, "rb") as f:
            backups[path] = f.read()


def restore_all():
    for path, data in backups.items():
        with open(path, "wb") as f:
            f.write(data)
    bad = []
    for path, data in backups.items():
        with open(path, "rb") as f:
            now = f.read()
        if hashlib.md5(now).hexdigest() != hashlib.md5(data).hexdigest():
            bad.append(path)
    if bad:
        print("❌ 还原校验失败：", bad)
        sys.exit(1)


def run_gate():
    """Run the judgement (the Go test) and report (code, output)."""
    proc = subprocess.Popen(
        ["go", "test", "./admin/", "-run", "Controlled", "-count=1", "-v"],
        cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )
    out, _ = proc.communicate()
    return proc.returncode, out.decode("utf-8", "replace")


def is_build_failure(output):
    """Whether the mutated build broke — which does NOT count as a caught arm."""
    return "build failed" in output or "[build failed]" in output


def main():
    print("mutation gate for the `:response` content-array change")
    if not os.path.isfile(IMPL) or not os.path.isfile(GATE):
        print("gate or implementation not found under", ROOT)
        return 2

    snap(IMPL)
    snap(GATE)

    # ⑦ 环境指纹：**被测的那两个文件**必须先落定（commit 或 stash）。
    #    ★ 共享工作树里别的路径有并发改动是常态（★ 那是别的会话的在途）⇒
    #    ★ 所以这里只钉【本台会改的那两个文件】，★ 而不是要求整棵树干净
    #    —— ★ 要求整棵树干净会把「别的会话在干活」误读成「读数不可信」。
    proc = subprocess.Popen(
        ["git", "status", "--porcelain", "--", IMPL, GATE], cwd=ROOT,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    out, _ = proc.communicate()
    dirty = [l for l in out.decode("utf-8", "replace").splitlines() if l.strip()]
    if dirty:
        print("❌ 被测文件本身有未落定的改动 ⇒ 读数不可信（★ 纪律：先 commit "
              "或 stash 这两个文件）:")
        for line in dirty:
            print("   ", line)
        restore_all()
        return 2

    proc = subprocess.Popen(["git", "status", "--porcelain"], cwd=ROOT,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    out, _ = proc.communicate()
    others = [l for l in out.decode("utf-8", "replace").splitlines() if l.strip()]
    if others:
        print("  note: %d other path(s) in the worktree are dirty (another "
              "session's in-flight work) ⇒ they are NOT touched by this gate"
              % len(others))

    code, output = run_gate()
    if code != 0:
        print("❌ 未变异的判据是红的 ⇒ 变异台无法测量任何东西")
        print(output[-4000:])
        restore_all()
        return 1
    print("  baseline: the unmutated judgement is GREEN")
    print("")

    missed = []
    wrong_red = []
    invalid = []

    try:
        for name, why, expect, edits in ARMS:
            with open(IMPL, "r", encoding="utf-8") as f:
                source = f.read()
            broken = source
            broken_count = 0
            for old, new in edits:
                hits = broken.count(old)
                if hits == 0:
                    print("  FAIL %s: the anchor was not found ⇒ the arm does "
                          "not apply" % name)
                    missed.append(name)
                    broken = None
                    break
                if hits != 1:
                    print("  FAIL %s: the anchor appears %d times ⇒ every "
                          "occurrence must be changed, and the count must be "
                          "declared" % (name, hits))
                    missed.append(name)
                    broken = None
                    break
                broken = broken.replace(old, new)
                broken_count += hits
            if broken is None:
                continue
            if broken == source:
                print("  FAIL %s: the arm is a NO-OP (the source did not "
                      "change)" % name)
                missed.append(name)
                continue

            with open(IMPL, "w", encoding="utf-8") as f:
                f.write(broken)

            code, output = run_gate()

            if "[build failed]" in output or "build failed" in output:
                # ③ 编译红不算数：它只说明代码坏了，不说明判据有牙。
                print("  FAIL %s: the mutated build FAILED ⇒ that is not a "
                      "caught arm (the arm itself is broken)" % name)
                invalid.append(name)
                restore_all()
                snap(IMPL)
                continue

            if code == 0:
                print("  FAIL %s: the mutated judgement is STILL GREEN ⇒ the "
                      "arm was not caught" % name)
                print("        %s" % why)
                missed.append(name)
            else:
                # ⑥ 红必须落在【它该红的那条断言】上。
                #    ★ Go 的 `--- FAIL:` 行只给**测试函数名** ⇒ 断言的**那句话**
                #    在测试日志里 ⇒ 所以判据 = 「那句话出现在输出里」
                #    ∧ 「某个测试函数红了」⇒ 两者都要。
                failed_tests = [l.strip() for l in output.splitlines()
                                if l.strip().startswith("--- FAIL")]
                # 期望断点在断言消息里；有些断言是 testify 的 Equal，
                # 消息里出现的是**期望值本身** ⇒ 两种写法都找。
                expect_forms = [expect, expect.replace("\\\\n", "\\n"),
                                expect.replace("\\\\n", "\n")]
                hit = [f for f in expect_forms if f and f in output]
                if not hit or not failed_tests:
                    print("  FAIL %s: the judgement went RED, but on the WRONG "
                          "assertion (expected: %s)" % (name, expect))
                    for l in failed_tests[:4]:
                        print("        %s" % l)
                    wrong_red.append(name)
                else:
                    print("  ok   %s: the judgement went RED on the right "
                          "assertion (%s)" % (name, failed_tests[0]))
            restore_all()
    finally:
        restore_all()

    print("")
    if missed or wrong_red or invalid:
        if missed:
            print("MUTATION GATE FAILED: %d arm(s) not caught: %s"
                  % (len(missed), ", ".join(missed)))
        if wrong_red:
            print("MUTATION GATE FAILED: %d arm(s) went red on the WRONG "
                  "assertion: %s" % (len(wrong_red), ", ".join(wrong_red)))
        if invalid:
            print("MUTATION GATE FAILED: %d arm(s) are invalid (the build "
                  "broke): %s" % (len(invalid), ", ".join(invalid)))
        return 1
    print("ALL MUTATIONS DETECTED: %d/%d" % (len(ARMS), len(ARMS)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
