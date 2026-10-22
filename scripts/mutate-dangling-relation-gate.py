#!/usr/bin/env python3
"""R106 轮 · 「悬空引用」门的变异验证。

用法：python3 scripts/mutate-dangling-relation-gate.py

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
  ⇒ ⇒ ★★ **本脚本在下面声明了它会写这三个文件** ⇒ ⇒
  ⇒ ⇒ **⇒ 任何人把变异留在树里（没还原），残留门就会报红**
  ⇒ ⇒ ★★ 这是「门自检恒绿」那个通病（把自检删掉 ⇒ 没人守）的【外部机制保障】。

★★ 本脚本的两条「重头变异」是 **M5 / M6** —— 它们改的是【量具本身】：

  M5 = 破坏腿 A 的一个 DDL 形态（CREATE TEMP TABLE）⇒ ⇒ 腿 A 少建一批关系
  M6 = 放宽腿 B 的 SQL 上下文判据（从「成对结构」退回「单个关键字」）
  ⇒ ⇒ 两者都会让【悬空引用集合】变化 ⇒ ⇒ 门必须红
  ⇒ ⇒ ★★ **这才是「门绿、量具瞎了」的反向证明**：
  ⇒ ⇒ ★★ 门不只对【清单变化】敏感，★ 它对【量具变化】同样敏感。

★ 为什么必须「按原路径 md5 还原」而不是「git checkout」：
  R65 轮出过两次假读数 —— 还原函数把文件拷到仓库根目录而不是原路径，
  于是源文件从未被还原，而「还原校验」读的是同一个错路径 ⇒ 报出假读数。
  ⇒ 这里备份与校验**都**用原路径，且校验读的是**被改的那个文件**。

★★★ 【R107 轮新增】★ 两道【预检】（★ 都在动任何文件之前）：
  ① **锚点预检** ⇒ 先用每个变异器**自己的**查找逻辑试跑一遍 ⇒ ⇒ 锚点漂移在【跑之前】报出来
     ⇒ ⇒ ★ 起因：R107 轮 gofmt 重新对齐了门文件里的 map ⇒ ⇒ 一个把【对齐空白】写进锚点的
     变异（M3）失效了 ⇒ ⇒ 旧版脚本跑到 M3 才报「变异未生效」⇒ ⇒ 失败点离原因点很远。
  ② **对齐脆弱性静态自检** ⇒ 扫本脚本自己的源码 ⇒ ⇒ 任何「锚点/替换串」字面量里
     出现 2 个以上连续空格 ⇒ ⇒ 立刻报红 ⇒ ⇒ **从机制上堵死「再写一个对齐依赖的锚点」**。
  ⇒ ★ 两条合起来 = ★★ **机制优于约定：★ 不靠「写锚点的人记得别写对齐」⇒ ⇒ 靠【跑之前先验一遍】。**
"""

import argparse
import ast
import hashlib
import os
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# ★★ 下面这三个路径字面量【会被 scripts/audit/mutation_residue_guard.py 扫到】
# ⇒ ⇒ 残留门据此认定「本脚本会写它们」⇒ ⇒ 变异未还原时会被报红。
GATE_REL = "scripts/ursmcheck/dangling_relation_gate_test.go"
DDL_REL = "internal/relrefs/ddl.go"
PRODUCT_REL = "internal/relrefs/product.go"

GATE_TEST = "TestDanglingRelation"


def md5(path):
    with open(path, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def run_gate():
    env = dict(os.environ)
    env.setdefault("GOCACHE", os.path.expanduser("~/sdk/gocache-llm-gateway-go"))
    r = subprocess.run(
        ["go", "test", "./scripts/ursmcheck/", "-run", GATE_TEST, "-count=1"],
        cwd=ROOT, capture_output=True, text=True, env=env,
    )
    out = r.stdout + r.stderr
    failed = [l.split()[2] for l in out.splitlines() if l.startswith("--- FAIL:")]
    # ★★★ 「编译红」与「断言红」必须分开 —— ★ R65 轮教训：
    #   ★ 编译红 ≠ 断言红 ⇒ ⇒ 一个「编译坏了」的变异会让门变红，
    #   ★ ★ 但那【不是】「门有牙」的证据 ⇒ ⇒ 必须单独识别并判失败。
    build_failed = ("[build failed]" in out) or ("build failed" in out) \
        or ("cannot use" in out) or ("undefined:" in out)
    return ("FAIL" in out), failed, out, build_failed


# ── 变异清单 ────────────────────────────────────────────────────────────
# (编号, 名称, 目标文件相对路径, 变异函数, 期望红的判据, 期望红吗)
#
# ★ 变异函数签名统一为 ``(rel, text)``（★ 数据侧与源码侧必须同签名 ⇒
#   否则调用方会把【文本】当【路径】⇒ ⇒ 异常消息里塞进整份文件 ⇒ 真因被淹没）。


def _mutate_exempt_add_stale(rel, text):
    """判据侧：豁免表里加一个【当前并不存在】的名字 ⇒ 期望红（判据 ②）。

    ★ 这一条对应「豁免表不许被做大」：若它可以随便写名字 ⇒
      「先豁免掉，省得以后再讨论」就会把未来的真缺口一起掩盖掉。
    """
    anchor = 'var exemptedDanglingRefs = map[string]string{'
    if anchor not in text:
        return None
    return text.replace(
        anchor,
        anchor + '\n\t"definitely_not_a_real_relation_name": "★ 故意添加的过期豁免（变异夹具）",',
        1,
    )


def _mutate_exempt_drop_real(rel, text):
    """判据侧：把【真悬空引用】那条豁免【改名】⇒ 期望红。

    ★ 这一条是「删掉要守的东西」的同族：★ 不动判据、只动登记 ⇒
      ⇒ 证明这道门守的确实是【清单本身】，不是别的。

    ★★★ ⚠ 为什么是【改名】而不是【删行】：★★
      那条豁免的**理由是跨三行的字符串拼接** ⇒ ⇒ 若只删第一行 ⇒
      ⇒ 剩下的续行变成孤立表达式 ⇒ ⇒ **编译红** ⇒ ⇒ 门会红，但那是【编译红】
      ⇒ ⇒ **编译红 ≠ 断言红**（★ R65 轮教训）⇒ ⇒ 所以改成「改名」：
      ⇒ ⇒ 编译正常 ⇒ ⇒ 红的就一定是【真断言】。
    """
    # ★★★ R107 轮修正：★ 锚点【不能写死对齐空白】（★ 见 alignment_selfcheck 的说明）
    m = re.search(r'"prompt_injection_approvals":[ \t]+"★★★ 真悬空引用', text)
    if not m:
        return None
    return (text[:m.start()]
            + '"prompt_injection_approvals_RENAMED_BY_MUTATION": "★★★ 真悬空引用'
            + text[m.end():])


def _mutate_exempt_blank_reason(rel, text):
    """判据侧：把某条豁免的理由清空 ⇒ 期望红（判据 ③）。

    ★ 「不写理由就豁免」这条路必须堵死 ⇒ ⇒ 理由是判据的一部分，不是注释。
    """
    # ★★★ R107 轮修正：★ 锚点【不能写死对齐空白】——
    #   ★ 原来这里写的是 '"pg_class":           "PG 系统目录…"'（11 个空格）
    #   ★ ⇒ ⇒ gofmt 重新对齐了那个 map（变成 12 个空格）⇒ ⇒ 锚点失效
    #   ★ ⇒ ⇒ 变异跑到一半才发现「M3 未生效」⇒ ⇒ 【空白弹性】的正解是用正则。
    m = re.search(r'("pg_class":[ \t]+)"PG 系统目录（pg_catalog）⇒ 不是缺口",', text)
    if not m:
        return None
    return text[:m.start()] + m.group(1) + '"",' + text[m.end():]


def _mutate_gate_always_green(rel, text):
    """判据侧：把判定函数改成【恒返回空】⇒ 期望红（反向夹具）。

    ★ ★ 这是「门内无解」那一族的正面证明：
      ⇒ 门永远绿 ⇒ ⇒ 反向夹具必须抓到它 ⇒ ⇒ 门才有牙。
    """
    old = """func notExempted(current map[string]int, exempt map[string]string) []string {
	var out []string"""
    if old not in text:
        return None
    return text.replace(
        old,
        """func notExempted(current map[string]int, exempt map[string]string) []string {
	if true {
		return nil // ★ 故意恒绿（变异夹具）
	}
	var out []string""",
        1,
    )


def _mutate_ddl_drop_temp(rel, text):
    """★ 量具侧（M5）：破坏腿 A 的一个 DDL 形态（CREATE TEMP TABLE）⇒ 期望红。

    ★★★ 这一条改的是【量具本身】⇒ ⇒ 它证明：
    ⇒ ⇒ ★★ **门对【量具变化】敏感** ⇒ ⇒ 「门绿、量具瞎了」在这道门上不成立
    ⇒ ⇒ ★★ 而【清单不变、量具变了】这种情形，正是最危险的那种。
    """
    old = "(?:(?:GLOBAL|LOCAL)\\s+)?(?:(?:TEMP|TEMPORARY|UNLOGGED)\\s+)?TABLE"
    if old not in text:
        return None
    return text.replace(old, "(?:(?:GLOBAL|LOCAL)\\s+)?TABLE", 1)


def _mutate_product_loosen_sql_context(rel, text):
    """★ 量具侧（M6）：把 SQL 上下文判据从「成对结构」退回「单个关键字」⇒ 期望红。

    ★★★ 这一条改的是【量具本身】⇒ ⇒ 它证明：
    ⇒ ⇒ ★★ 腿 B 的搜索面一旦退化，★ 假阳性就会暴增 ⇒ ⇒ 门必须红。
    ⇒ ⇒ ★★ 而这正是 R106 轮开发过程中真实发生过的轨迹
    ⇒ ⇒   （309 个名字 → 12 个名字 ⇒ 每一处收紧都是靠这个判据发现的）。
    """
    old = '`(?i)\\b(SELECT\\s+[\\s\\S]{0,400}?\\bFROM\\b` +'
    if old not in text:
        return None
    return text.replace(
        old,
        '`(?i)\\b(SELECT|INSERT|UPDATE|DELETE|FROM|JOIN|WITH|COPY|REFERENCES|TRUNCATE\\b` +',
        1,
    )


MUTATIONS = [
    ("M1", "判据侧：豁免表里加一个【不存在】的名字", GATE_REL, _mutate_exempt_add_stale,
     "TestDanglingRelationExemptionTableIsNotInflated", True),
    ("M2", "判据侧：★删掉真悬空引用那条豁免", GATE_REL, _mutate_exempt_drop_real,
     "TestDanglingRelationReferencesAreExempted", True),
    ("M3", "判据侧：把某条豁免的理由清空", GATE_REL, _mutate_exempt_blank_reason,
     "TestDanglingRelationExemptionsMustHaveReason", True),
    ("M4", "判据侧：★把判定函数改成恒绿", GATE_REL, _mutate_gate_always_green,
     "TestDanglingRelationGateHasTeeth", True),
    ("M5", "★量具侧：破坏腿 A 的 CREATE TEMP TABLE 形态", DDL_REL, _mutate_ddl_drop_temp,
     "TestDanglingRelationReferencesAreExempted", True),
    ("M6", "★量具侧：把腿 B 的 SQL 上下文判据放宽成单个关键字", PRODUCT_REL,
     _mutate_product_loosen_sql_context,
     "TestDanglingRelationReferencesAreExempted", True),
]


# ── ★★★ R107 轮新增：锚点【预检】+ 【对齐脆弱性】静态自检 ──────────────────
# ★ 为什么要有「预检」：
#   ★★ R107 轮实测：gofmt 重新对齐了门文件里的 map ⇒ ⇒ 一个把【对齐空白】写进锚点的
#     变异（M3）就失效了 ⇒ ⇒ 脚本跑到 M3 才报「变异未生效」⇒ ⇒ **前面几条已经跑过了**
#     ⇒ ⇒ 失败点离原因点很远 ⇒ ⇒ 排查成本高。
#   ★★ ⇒ 【锚点漂移必须在【动任何文件之前】就被看见】⇒ ⇒ 预检就是干这个的。
#   ★★ ⇒ 机制优于约定：★ 不靠「写锚点的人记得别写对齐」⇒ ⇒ 靠【跑之前先验一遍】。
def preflight_anchors():
    """在动任何文件之前，先确认每个变异器的锚点还找得到。

    ★ ★ 复用变异器**自己的**查找逻辑（而不是另写一份锚点表）⇒ ⇒ 不会有第二份口径。
    """
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


def alignment_selfcheck():
    """静态自检：★ 任何「锚点/替换串」字面量都【不许含 2 个以上连续空格】。

    ★★ 为什么：★ Go 的 map 字面量会被 gofmt **按列对齐**⇒ ⇒ 一旦锚点里写死了对齐空格
      ⇒ ⇒ 任何一次格式化都会让锚点失效 ⇒ ⇒ 而【格式化的发生时机不可预测】。
    ★★ ⇒ ⇒ 所以对齐空格【根本不该出现在锚点里】⇒ ⇒ 用【正则 + [ \\t]+】表达弹性空白。
    ★★ ⇒ ⇒ 这条自检扫**本脚本自己的源码**⇒ ⇒ 谁再写一个对齐依赖的锚点 ⇒ 脚本自己报红。
    """
    here = os.path.abspath(__file__)
    try:
        tree = ast.parse(open(here, encoding="utf-8").read())
    except (OSError, SyntaxError) as e:
        return [("自检", "解析本脚本源码失败：%s" % e, here, "")]
    hits = []
    for node in ast.walk(tree):
        if not isinstance(node, ast.Assign):
            continue
        for tgt in node.targets:
            if not isinstance(tgt, ast.Name):
                continue
            if tgt.id not in ("anchor", "old"):
                continue
            if not isinstance(node.value, ast.Constant) or not isinstance(node.value.value, str):
                continue
            s = node.value.value
            if re.search(r"[ \t]{2,}", s):
                hits.append((tgt.id, s, here, "★ 锚点/替换串里含 2 个以上连续空格 ⇒ 对齐依赖"))
    return hits


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--keep-going", action="store_true",
                    help="某条变异不符合预期时继续跑（默认立即停）")
    args = ap.parse_args()

    # ── ★ 预检：先验锚点与自检，再动任何文件 ──
    print("[预检] ① 锚点预检（★ 在动任何文件之前）")
    bad = preflight_anchors()
    if bad:
        for mid, name, rel, why in bad:
            print("  ✘ [%s] %s ⇒ %s（%s）" % (mid, name, why, rel))
        print("\n★ 预检失败：★ 锚点漂移 ⇒ ⇒ **先修锚点，别先跑变异**"
              "（★ ★ 常见成因：★ 目标文件被 gofmt 重新对齐 ⇒ ⇒ 锚点里写死的空白失效）")
        return 1
    print("  ✓ 全部 %d 个锚点在当前目标文件里都能命中" % len(MUTATIONS))

    print("[预检] ② 对齐脆弱性静态自检（★ 锚点不许写死对齐空白）")
    aln = alignment_selfcheck()
    if aln:
        for tgt, s, path, why in aln:
            print("  ✘ %s ⇒ %s：%r" % (tgt, why, s[:80]))
        print("\n★ 自检失败：★ 锚点里不许含 2 个以上连续空格 ⇒ ⇒ 改用【正则 + [ \\t]+】")
        return 1
    print("  ✓ 没有锚点依赖对齐空白")

    backup_dir = tempfile.mkdtemp(prefix="mutate-dangling-relation-")
    print("★ 备份目录（**不删除**，基线必须来自独立快照）：%s" % backup_dir)

    print("[基线] %s ⇒ GREEN" % GATE_TEST)
    ok, failed, out, build_failed = run_gate()
    if build_failed:
        print(out)
        print("★ 基线【编译红】⇒ 先修编译再谈变异")
        return 1
    if ok:
        print(out)
        print("★ 基线就红了 ⇒ 门当前已经失败 ⇒ 先修门再谈变异")
        return 1

    failures = []
    for mid, name, rel, fn, expect_test, expect_red in MUTATIONS:
        src = os.path.join(ROOT, rel)
        backup = os.path.join(backup_dir, mid + "-" + rel.replace("/", "_"))
        shutil.copy2(src, backup)
        original = open(backup, encoding="utf-8").read()
        original_md5 = md5(src)

        mutated = fn(rel, original)
        if mutated is None:
            print("[%s] ★ 变异未生效（锚点没找到）⇒ 记为失败" % mid)
            failures.append(mid)
            continue
        with open(src, "w", encoding="utf-8") as f:
            f.write(mutated)

        is_red, failed_tests, raw, build_failed = run_gate()
        detail = ""
        if is_red:
            detail = "｜失败的判据: %s" % failed_tests
        if build_failed:
            detail += "｜★★【编译红】"
        print("[%s] %s ⇒ %s%s" % (mid, name, "RED" if is_red else "GREEN", detail))

        good = True
        if build_failed:
            print("      ✘ 编译红 ⇒ ★★ 这不是「门有牙」的证据（编译红 ≠ 断言红）⇒ 变异本身不合格")
            good = False
        if expect_red and not is_red:
            print("      ✘ 期望红，实际绿 ⇒ 门没牙")
            good = False
        if not expect_red and is_red:
            print("      ✘ 期望绿，实际红")
            good = False
        if is_red and expect_test not in failed_tests:
            if not failed_tests:
                print("      ✘ 红了但【没有任何失败判据】⇒ ★ 多半是编译红 ⇒ 变异不合格")
            else:
                print("      ✘ 红因不对（期望 %s 失败，实得 %s）" % (expect_test, failed_tests))
            good = False

        # ★ 还原：按【原路径】写回，并用 md5 逐字校验
        shutil.copy2(backup, src)
        now_md5 = md5(src)
        if now_md5 == original_md5:
            print("      ✓ 还原逐字一致（md5 %s）" % now_md5[:12])
        else:
            print("      ✘ 还原不一致（期望 %s，实得 %s）" % (original_md5[:12], now_md5[:12]))
            good = False
        if not good:
            failures.append(mid)
        if failures and not args.keep_going:
            print("\n★ 提前中止（先还原已完成的部分）")
            break

    print("★ 备份目录保留在：%s" % backup_dir)
    dirty = []
    for mid, name, rel, fn, expect_test, expect_red in MUTATIONS:
        src = os.path.join(ROOT, rel)
        r = subprocess.run(["git", "status", "--porcelain", "--", rel], cwd=ROOT,
                           capture_output=True, text=True)
        if r.stdout.strip():
            # ★ 保留 git 的原始状态码（★ M=已跟踪被改 / ??=未跟踪新文件）
            dirty.append(r.stdout.strip())
    if dirty:
        print("★ 工作区状态（%d 项；本轮预期的改动应当只有那几处）：" % len(dirty))
        for d in sorted(set(dirty)):
            print("   %s" % d)
    else:
        print("★ 工作区干净（零残留）✔")

    if failures:
        print("\n★ 变异验证失败：%s" % ", ".join(failures))
        return 1
    print("\n★ 变异验证全部通过")
    return 0


if __name__ == "__main__":
    sys.exit(main())
