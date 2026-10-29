#!/usr/bin/env python3
"""R105 轮 · 「节号唯一性」门的变异验证。

用法：python3 scripts/mutate-section-numbering-gate.py

它做什么：对每一处「改了就会红」的判据做一次**真实**变异，然后：

  ① 变异后跑**目标门**，必须**红**；
  ② 红的原因必须是**目标断言**（不是编译错、不是别的门）；
  ③ 还原必须回到**原路径**且**逐字一致**（md5 比对）；
  ④ 备份**不得删除**（基线必须来自独立快照，不能来自「就地还原」）；
  ⑤ 全部跑完后工作区必须零残留。

★ 为什么这个脚本必须是 ``mutate-*`` 形态：
  仓里的 `scripts/audit/mutation_residue_guard.py` 会
  ① 扫 ``scripts/*mutate*.py`` 与 ``scripts/audit/*mutate*.py``
  ② 从这些脚本里**解析**出「它们声明会写的仓库文件」
  ③ 报红条件 = 「该文件当前内容 ≠ ``git show HEAD:<path>`` 的内容」
  ⇒ ⇒ ★★ **本脚本在下面声明了它会写这两个文件** ⇒ ⇒
  ⇒ ⇒ **⇒ 任何人把变异留在树里（没还原），残留门就会报红**
  ⇒ ⇒ ★★ **这正是「门自检恒绿」那个通病（把自检删掉 ⇒ 没人守）的【外部机制保障】**：
  门内无法阻止「自检被删」，但**树外可以检出「它被删了还没还原」**。

★ 「门自检恒绿」的三层（本脚本逐条对着验）：
  第一层 = 自指（判据引用了它要测的那个字面量）
  第二层 = 标记不唯一（同一个字面量在别处也有）
  第三层 = ★★ 搜索面不完整（判据只覆盖了目标的一部分）⇒ ⇒ **本轮的真发现就在这一层**

★ 为什么必须「按原路径 md5 还原」而不是「git checkout」：
  R65 轮出过两次假读数 —— 还原函数把文件拷到仓库根目录而不是原路径，
  于是源文件从未被还原，而「还原校验」读的是同一个错路径 ⇒ 报出假读数。
  ⇒ 这里备份与校验**都**用原路径，且校验读的是**被改的那个文件**。
"""

import argparse
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

# ★★ r1047 轮（★ 2026-10-28）：★ 子进程走【专用 GOCACHE】⇒ ⇒ 不再往用户的全局缓存里
# ★★ 写「随树变化而失效」的一次性产物（★ 理由与实测读数见 `scripts/lib/gocache.py`
# ★★ 与 `internal/gocache` 的包注释）。
# ★★ **两条路径都试**（★ ★★ 深度无关：★ 本文件在 scripts/ 或 scripts/audit/ 下）★★
for _gc_lib in (os.path.join(os.path.dirname(os.path.abspath(__file__)), "lib"),
                os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "lib")):
    if os.path.isfile(os.path.join(_gc_lib, "gocache.py")):
        sys.path.insert(0, _gc_lib)
        break
import gocache  # noqa: E402

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# ★★ 下面这两个路径字面量【会被 scripts/audit/mutation_residue_guard.py 扫到】
# ⇒ ⇒ 残留门据此认定「本脚本会写它们」⇒ ⇒ 变异未还原时会被报红。
GATE_REL = "scripts/ursmcheck/section_numbering_uniqueness_test.go"
LESSONS_REL = "LESSONS.md"
RUNBOOK_REL = "docs/96小时内修订审计-20261007-1350.md"

GATE_TEST = "TestSectionNumber"


def md5(path):
    with open(path, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def run_gate():
    env = gocache.env("mutate")
    r = subprocess.run(
        ["go", "test", "./scripts/ursmcheck/", "-run", GATE_TEST, "-count=1"],
        cwd=ROOT, capture_output=True, text=True, env=env,
    )
    out = r.stdout + r.stderr
    failed = [l.split()[2] for l in out.splitlines() if l.startswith("--- FAIL:")]
    return ("FAIL" in out), failed, out


# ── 变异清单 ────────────────────────────────────────────────────────────
# (编号, 名称, 目标文件相对路径, 变异函数, 期望红的判据, 期望红吗)
def _mutate_data_repeat(rel, text):
    """数据侧：在文件末尾追加一个【当前唯一节号】的副本 ⇒ 制造重复。

    ★★ 签名统一为 ``(rel, text)``：★ 数据侧与源码侧的变异函数【必须同一个签名】
    ⇒ ⇒ 否则调用方会把【文本】当【路径】传给数据侧 ⇒ ⇒
    ⇒ ⇒ **⇒ 而且那个错会把【整份文件内容】塞进异常消息 ⇒ traceback 刷屏 ⇒ 真因被淹没**
    ⇒ ⇒ ★ 这是本脚本第一版的真实 bug（★ 记在这里，免得下次重犯）。
    """
    import re
    head = "## " if rel == LESSONS_REL else "### "
    pat = re.compile(r"^%s§(\d+(?:\.\d+)*[A-Za-z]*)(\s|$)" % re.escape(head), re.M)
    counts = {}
    order = []
    for m in pat.finditer(text):
        counts[m.group(1)] = counts.get(m.group(1), 0) + 1
        order.append(m.group(1))
    exempt = {"211", "212", "213", "218", "116", "148"}
    target = None
    for n in order:
        if counts[n] == 1 and n not in exempt:
            target = n
            break
    if target is None:
        return None
    return text + "\n%s§%s ★ 故意制造的重复（变异夹具）\n" % (head, target)


def _mutate_data_delete_exempt(rel, text):
    """数据侧：**删掉**存量重复中的一条 ⇒ 次数 2→1 ⇒ 期望红。

    ★ 这一条对应并行会话指出的「配套手法」：**专门造『把要守的东西删掉』
    这一类变异** —— 别的变异只能证明「形态判据有牙」，证明不了「自检有牙」。
    """
    lines = text.split("\n")
    out = []
    removed = False
    for l in lines:
        if not removed and l.startswith("## §211 ★★★★★ **★ 「造好落点」"):
            removed = True
            continue
        out.append(l)
    if not removed:
        return None
    return "\n".join(out)


def _mutate_gate_regex_narrow(rel, text):
    """源码侧：正则漏掉一层标题（`#{2,4}` → `#{2,3}`）⇒ 解析器坏了 ⇒ 期望红。"""
    return text.replace(
        "`^\\s*#{2,4}\\s.*?§([0-9][0-9.]*[A-Za-z]*)(\\s|$)`",
        "`^\\s*#{2,3}\\s.*?§([0-9][0-9.]*[A-Za-z]*)(\\s|$)`",
    )


def _mutate_gate_truncate_suffix(rel, text):
    """源码侧：把字母后缀截掉（`10bis` ⇒ `10`）⇒ 对应「标记不唯一」⇒ 期望红。"""
    return text.replace(
        "`^\\s*#{2,4}\\s.*?§([0-9][0-9.]*[A-Za-z]*)(\\s|$)`",
        "`^\\s*#{2,4}\\s.*?§([0-9][0-9.]*)(\\s|$)`",
    )


def _mutate_gate_shrink_exempt(rel, text):
    """源码侧：偷偷把豁免表的次数 2→1 ⇒ 期望红。"""
    return text.replace('"211": 2,', '"211": 1,')


def _mutate_gate_narrow_to_prefix_only(rel, text):
    """源码侧：★★ **把搜索面退化回「只认节号在最前」** ⇒ 期望红。

    ★★★ 这是本轮最关键的一条：它模拟「搜索面不完整」这个第三层。
    ⇒ 修好互证之前，它【抓不到】（门会绿）⇒ 修好之后，它被「双口径互证」抓住。
    """
    return text.replace(
        "`^\\s*#{2,4}\\s.*?§([0-9][0-9.]*[A-Za-z]*)(\\s|$)`",
        "`^\\s*#{2,4}\\s §([0-9][0-9.]*[A-Za-z]*)(\\s|$)`",
    )


def _mutate_gate_drop_crosscheck(rel, text):
    """源码侧：★ 删掉「双口径互证」自检本身 ⇒ **期望【不】红**。

    ★★★ 这是「自检型门」的通病：**把自检删掉 ⇒ 没人守 ⇒ 门仍绿**。
    ⇒ ⇒ 门内无解 ⇒ ⇒ **⇒ 真正的保障在【树外】：本脚本在下面声明了它会写门文件
    ⇒ ⇒ ⇒ 变异未还原 ⇒ `scripts/audit/mutation_residue_guard.py` 报红**。
    ⇒ ⇒ 所以这条变异**故意期望不红** ⇒ 它的作用是**把这条盲区显式登记**。
    """
    old = '''	if independentLines != total {
		t.Fatalf("%s：独立口径数出的标题行数 %d != 解析口径数出的节号次数之和 %d ⇒ "+
			"【搜索面不完整】⇒ ★ 别据此下「无重复」的结论\\n"+
			"  ★ 两口径【已刻意做成互不相同】：口径 B 是纯字符串判据（行以 # 开头且含 §）、"+
			"不解析节号、不用解析正则 ⇒ 解析器坏了它不会跟着坏",
			path, independentLines, total)
	}'''
    if old not in text:
        return None
    return text.replace(old, "")


MUTATIONS = [
    ("M1", "数据侧：追加一个重复节号", LESSONS_REL, _mutate_data_repeat,
     "TestSectionNumbersAreUniqueBeyondKnownBaseline", True),
    ("M2", "数据侧：handoff 追加一个重复节号", RUNBOOK_REL, _mutate_data_repeat,
     "TestSectionNumbersAreUniqueBeyondKnownBaseline", True),
    ("M3", "数据侧：★删掉存量重复中的一条（配套手法：删要守的东西）", LESSONS_REL,
     _mutate_data_delete_exempt, "TestSectionNumbersAreUniqueBeyondKnownBaseline", True),
    ("M4", "源码侧：正则漏掉一层标题", GATE_REL, _mutate_gate_regex_narrow,
     "TestSectionNumberParserHasTeeth", True),
    ("M5", "源码侧：字母后缀被截断（对应「标记不唯一」）", GATE_REL, _mutate_gate_truncate_suffix,
     "TestSectionNumberParserHasTeeth", True),
    ("M6", "源码侧：偷偷把豁免表次数 2→1", GATE_REL, _mutate_gate_shrink_exempt,
     "TestSectionNumbersAreUniqueBeyondKnownBaseline", True),
    ("M7", "源码侧：★★ 搜索面退化回「只认节号在最前」", GATE_REL, _mutate_gate_narrow_to_prefix_only,
     "TestSectionNumbersAreUniqueBeyondKnownBaseline", True),
    ("M8", "源码侧：★ 删掉双口径互证自检本身（★ 故意期望【不】红）", GATE_REL,
     _mutate_gate_drop_crosscheck, "", False),
]


# ── ★★★ R107 轮新增：锚点【预检】（★ 与 mutate-dangling-relation-gate.py 同机制）──
# ★ 为什么要有：★ 「锚点漂移」最典型的成因是【目标文件被格式化 / 被改写】⇒ ⇒ 锚点失效
#   ⇒ ⇒ 旧版脚本要跑到那一条才报「变异未生效」⇒ ⇒ 失败点离原因点很远 ⇒ ⇒ 排查成本高。
#   ★ ⇒ 【锚点漂移必须在【动任何文件之前】就被看见】⇒ ⇒ 预检就是干这个的。
#   ★ ★ 复用变异器**自己的**查找逻辑（不另写一份锚点表）⇒ ⇒ 不会有第二份口径。
def preflight_anchors():
    """在动任何文件之前，先确认每个变异器的锚点还找得到。"""
    bad = []
    for mid, name, rel, fn, _expect_test, _expect_red in MUTATIONS:
        src = os.path.join(ROOT, rel)
        try:
            text = open(src, encoding="utf-8").read()
        except OSError as e:
            bad.append((mid, name, rel, "读不到目标文件：%s" % e))
            continue
        if fn(rel, text) is None:
            bad.append((mid, name, rel, "锚点未命中（目标文件里找不到它要改的那段）"))
    return bad


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--keep-going", action="store_true", help="某条失败后继续")
    args = ap.parse_args()

    # ── ★ 预检：先验锚点，再动任何文件 ──
    print("[预检] 锚点预检（★ 在动任何文件之前）")
    bad = preflight_anchors()
    if bad:
        for mid, name, rel, why in bad:
            print("  ✘ [%s] %s ⇒ %s（%s）" % (mid, name, why, rel))
        print("\n★ 预检失败：★ 锚点漂移 ⇒ ⇒ **先修锚点，别先跑变异**")
        return 1
    print("  ✓ 全部 %d 个锚点在当前目标文件里都能命中" % len(MUTATIONS))

    backup_dir = tempfile.mkdtemp(prefix="mutate-section-numbering-")
    print("★ 备份目录（**不删除**，基线必须来自独立快照）：%s" % backup_dir)

    base_red, _, _ = run_gate()
    print("[基线] %s ⇒ %s" % (GATE_TEST, "RED（★ 不该如此！）" if base_red else "GREEN"))

    failures = []
    for mid, name, rel, fn, expect_test, expect_red in MUTATIONS:
        src = os.path.join(ROOT, rel)
        backup = os.path.join(backup_dir, mid + "-" + rel.replace("/", "_"))
        shutil.copy2(src, backup)
        original = open(backup, encoding="utf-8").read()
        original_md5 = md5(src)
        try:
            mutated = fn(rel, original)
            if mutated is None:
                # ★ R107 轮（12h 审计 C1）修正：这里**原来只 print + continue**，
                #   不进 failures ⇒ 锚点漂移时该条变异被**静默跳过**，
                #   脚本最后仍然打印「=== 变异验证全部通过 ===」并 exit 0
                #   ⇒ **一条未经证明的判据被报告为已证明**。
                #   ★ 同族脚本 scripts/mutate-dangling-relation-gate.py:228-231
                #     与 scripts/mutate-r75-patrol-slot-gate.py:117-120 都是
                #     「记为失败」⇒ 本处是**漏写**，不是有意设计。
                #   ⇒ 理由：节号唯一性门的锚点绑死在 LESSONS.md 的具体标题行上，
                #     而节号在仓里反复重排 ⇒ 锚点漂移是**常见**事件，不是边缘情况。
                print("[跳过] %s %s：目标文本不存在（★ 锚点漂移 ⇒ 记为失败）" % (mid, name))
                failures.append("%s %s：变异未生效（锚点没找到）" % (mid, name))
                continue
            if mutated == original:
                print("[不变] %s %s：变异未生效（★ 这一条自己就失效了）" % (mid, name))
                failures.append("%s %s：变异未生效" % (mid, name))
                continue
            with open(src, "w", encoding="utf-8") as f:
                f.write(mutated)
            red, failed, _ = run_gate()
            verdict = "RED" if red else "GREEN"
            print("[%s] %s %s ⇒ %s ｜失败的判据: %s"
                  % (mid, verdict, name, "", failed or "（无）"))
            if expect_red:
                if not red:
                    failures.append("%s %s：期望红，实际绿 ⇒ 门没牙" % (mid, name))
                elif expect_test and expect_test not in failed:
                    failures.append("%s %s：红了，但红因不是 %s（实际 %s）"
                                    % (mid, name, expect_test, failed))
                else:
                    print("      ✓ 变红且红因正确（%s）" % (expect_test,))
            else:
                if red:
                    print("      ★ 说明这条盲区已经被修掉了 ⇒ 请更新本脚本的期望")
                else:
                    print("      ✓ 符合预期：不红 ⇒ 盲区仍在 ⇒ 已显式登记")
        finally:
            with open(src, "w", encoding="utf-8") as f:
                f.write(original)
            now = md5(src)
            if now != original_md5:
                failures.append("%s %s：还原后 md5 不一致（★ 假读数风险）" % (mid, name))
            else:
                print("      ✓ 还原逐字一致（md5 %s）" % now[:12])

    print("★ 备份目录保留在：%s" % backup_dir)

    dirty = subprocess.run(["git", "status", "--porcelain"], cwd=ROOT,
                           capture_output=True, text=True).stdout.strip()
    if dirty:
        print("★ 工作区状态（%d 项；本轮预期的改动应当只有那几处）：" % len(dirty.splitlines()))
        for l in dirty.splitlines():
            print("   " + l)
    else:
        print("★ 工作区干净（零残留）✔")

    if failures:
        print("\n=== 变异验证失败 ===")
        for f in failures:
            print("  · " + f)
        return 1
    print("\n=== 变异验证全部通过 ===")
    print("★ 提醒：M8 故意期望【不】红 ⇒ 那是「自检型门」的通病（删掉自检 ⇒ 门仍绿）")
    print("★   ⇒ 真正的保障在树外：本脚本声明了它会写门文件 ⇒ 变异未还原 ⇒ 残留门会报红")
    return 0


if __name__ == "__main__":
    sys.exit(main())
