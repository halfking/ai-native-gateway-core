#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""r1040 地址族三道门的变异台：★ 验证「门绿 ≠ 门有牙」。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_r1040_address_family_gates.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★★ 纪律（★ 与 scripts/audit/mutate_14399_gate.py 同构，逐条落地）：

★   ① 还原必须按【原路径】做 **md5 逐字校验**；
★   ② 变异必须改【全部】出现处（★ 且要声明改了几处）；
★   ③ 变异必须【语义变了但仍能编译】（★ 编译红 ≠ 断言红 ⇒ 不算数）；
★   ④ 环境指纹对拍（★ 共享工作树里 ⇒ ⇒ 并发改动会被误读成「门没牙」）；
★   ⑤ 变异必须落在【判据实际读到的那一处】；
★   ⑥ ★★ 每个变异都必须自带【效果探针】——★ 「目标 needle 在【判据实际读的
★      那一面】里的出现次数变了」+「文件内容确实变了」；
★   ⑦ ★★★ 探针的【方向】必须与变异的方向一致；
★   ⑧ ★★★ 跑变异台前必须先确认工作树状态（★ 本台用 md5 快照代替 git stash：
★      本台要变异的文件里有**本轮新建**的文件，stash 不会带上未跟踪文件 ⇒
★      ⇒ 用 md5 快照做还原凭据更可靠）；
★   ⑨ ★★★ **「门没牙」要先过【惰性变异】筛查**——★ 用判据自己的谓词复算一次。

★★ 本台特有的一条：

★   ⑩ ★★★★ **M06 是「期望绿」臂**：★ 它把 M05 制造的那份「新名单」**登记进清单**
★      ⇒ ⇒ **门必须【仍绿】**
★      ⇒ ⇒ 因为「缺陷已登记」与「缺陷已修好」两种状态都应当是绿的
★      ⇒ ⇒ ★ **⇒ 一道门若在缺陷被修好之后变红，那它就是在阻止修复 ⇒ 必须改**
"""

import hashlib
import os
import re
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
gocache.apply_to_environ("mutate")

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

GO_BIN = os.path.join(os.path.expanduser("~"), "sdk", "go", "bin")

MANAGED = os.path.join(ROOT, "domains/outputcompliance/managed_rules.go")
SSOT = os.path.join(ROOT, "domains/sanitizerules/address_family.go")
YAML = os.path.join(ROOT, "configs/sensitive_patterns.yaml")
MANIFEST = os.path.join(ROOT, "cmd/tools/address_family_decision_audit/manifest_test.go")
INJECT = os.path.join(ROOT, "security/sanitize/zz_r1040_mutation_list.go")

WATCHED = (MANAGED, SSOT, YAML, MANIFEST, INJECT)

PKG_OUTPUTCOMPLIANCE = "./domains/outputcompliance/"
PKG_SANITIZERULES = "./domains/sanitizerules/"
PKG_SANITIZE = "./security/sanitize/"
PKG_AUDIT = "./cmd/tools/address_family_decision_audit/"

RESULTS = []


# ───────────────────────── 基础工具 ─────────────────────────

def snap(path):
    if not os.path.exists(path):
        return None
    with open(path, "rb") as f:
        return f.read()


def md5(path):
    data = snap(path)
    if data is None:
        return "<absent>"
    return hashlib.md5(data).hexdigest()


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


def read(path):
    with open(path, "r", encoding="utf-8") as f:
        return f.read()


def restore_all(backups):
    for path, data in backups.items():
        if data is None:
            if os.path.exists(path):
                os.remove(path)
            continue
        write(path, data.decode("utf-8"))


def go_test(pkg, run_filter=None):
    env = dict(os.environ)
    env["PATH"] = GO_BIN + os.pathsep + env.get("PATH", "")
    cmd = ["go", "test", pkg, "-count=1"]
    if run_filter:
        cmd += ["-run", run_filter]
    proc = subprocess.run(cmd, cwd=ROOT, env=env, capture_output=True, text=True)
    return proc.returncode, (proc.stdout + proc.stderr)


def go_build(pkg):
    """编译检查：★ 编译红**不算**判据红（纪律 ③）⇒ 单独判。"""
    env = dict(os.environ)
    env["PATH"] = GO_BIN + os.pathsep + env.get("PATH", "")
    proc = subprocess.run(["go", "build", pkg], cwd=ROOT, env=env, capture_output=True, text=True)
    return proc.returncode, (proc.stdout + proc.stderr)


def git_status_lines():
    env = dict(os.environ)
    proc = subprocess.run(["git", "status", "--short"], cwd=ROOT, env=env, capture_output=True, text=True)
    return [l for l in proc.stdout.split("\n") if l.strip()]


# ───────────────────────── 效果探针 ─────────────────────────

def probe_count(path, needle, expect_delta):
    """needle 的字面量出现次数相对基线的变化量（expect_delta）。"""
    data = snap(path)
    if data is None:
        return 0
    return data.decode("utf-8").count(needle) - expect_delta


def probe_file_changed(path, before_md5):
    return md5(path) != before_md5


def arm(name, expect_red, mutate, probe, checks, build_check=True):
    """checks: [(pkg, run_filter_or_None, expected_fail_test_or_None)]。

    ★ 判定要求「**指定的那个测试**红」而不是「这个包红」——
    否则别的断言偶然红了也会被记成「这条臂有牙」（量具指错对象的老毛病）。
    """
    print("-" * 78)
    print("▶ " + name)
    backups = {p: snap(p) for p in WATCHED}
    before_md5 = {p: md5(p) for p in WATCHED}
    status_before = git_status_lines()
    ok = True
    msg = ""
    try:
        mutate()
        # ⑥ 探针：变异确实打上了
        probe_msg = probe(before_md5)
        if not probe_msg.startswith("OK"):
            ok = False
            msg = "探针未达成（变异可能没打上）：" + probe_msg
        else:
            # ③ 编译必须仍然通过
            if build_check:
                rc, out = go_build("./...")
                if rc != 0:
                    ok = False
                    msg = "编译红 ⇒ 按纪律不算判据红（变异写法有问题）：\n" + out[-800:]
            if ok:
                # 跑判据
                reds = []
                for pkg, filt, want_fail in checks:
                    rc, out = go_test(pkg, filt)
                    if expect_red:
                        if rc == 0:
                            ok = False
                            msg = "★ 预期门红，但 %s%s 是绿的" % (pkg, (" -run " + filt) if filt else "")
                        elif want_fail and ("--- FAIL: " + want_fail) not in out:
                            ok = False
                            msg = ("★ %s%s 红了，但红在**别的**断言上（期望 %s）：\n%s"
                                   % (pkg, (" -run " + filt) if filt else "", want_fail, out[-1200:]))
                    else:
                        if rc != 0:
                            ok = False
                            msg = "★ 预期门绿，但 %s%s 红了：\n%s" % (pkg, (" -run " + filt) if filt else "", out[-1500:])
                    reds.append((pkg, rc))
                if ok:
                    msg = "预期%s，实测%s：%s" % (
                        "红" if expect_red else "绿",
                        "红" if expect_red else "绿",
                        ", ".join("%s=%s" % (p, "红" if rc != 0 else "绿") for p, rc in reds),
                    )
    finally:
        restore_all(backups)
        # ① md5 逐字校验
        for p in WATCHED:
            if before_md5[p] != md5(p):
                ok = False
                msg += "\n    ★ 还原不是逐字等价：%s（%s → %s）" % (p, before_md5[p], md5(p))
        # 还原后再跑一次判据：必须是绿
        if ok:
            for pkg, filt, _ in checks:
                rc, out = go_test(pkg, filt)
                if rc != 0:
                    ok = False
                    msg += "\n    ★ 还原后 %s%s 仍红 ⇒ 门不可复现：\n%s" % (pkg, (" -run " + filt) if filt else "", out[-800:])
    # ④ 环境指纹
    if git_status_lines() != status_before:
        ok = False
        msg += "\n    ★ 环境指纹变了（工作区状态与臂前不一致）"
    RESULTS.append((name, ok, msg))
    print(("  ✅ " if ok else "  ❌ ") + msg)


# ───────────────────────── 变异体 ─────────────────────────

def m01_reintroduce_third_face():
    """把 SSOT 那份映射抄回 Checker.RuleConfig（第三面复发）。"""
    src = read(MANAGED)
    anchor = "\t\tcfg.Rules = append(cfg.Rules, sanitizerules.LegacyPIIRule(id, p.PatternName, p.PatternType, p.RegexPattern, p.Enabled))"
    if src.count(anchor) != 1:
        raise RuntimeError("锚点命中 %d 次（应为 1）" % src.count(anchor))
    mutant = """\t\ttyp := p.PatternType
\t\tswitch typ {
\t\tcase "bank_card":
\t\t\ttyp = "credit_card"
\t\tcase "ip", "ip_address", "ipv6":
\t\t\ttyp = "server_ip"
\t\tcase "phone", "id_card", "email", "credit_card", "secret", "account", "name", "internal_ip", "server_ip", "custom":
\t\tdefault:
\t\t\ttyp = "custom"
\t\t}
\t\taction := "mask"
\t\tif isAddressPIIType(typ) {
\t\t\taction = "log"
\t\t}
\t\tcfg.Rules = append(cfg.Rules, sanitizerules.Rule{ID: "compliance.pii." + strconv.Itoa(id), Name: p.PatternName, Type: typ, Kind: "pattern", Pattern: p.RegexPattern, Scopes: []string{"output"}, Action: action, Enabled: p.Enabled})"""
    src = src.replace(anchor, mutant)
    src = src.replace('import (\n\t"context"\n', 'import (\n\t"context"\n\t"strconv"\n')
    write(MANAGED, src)


def m02_widen_ssot_with_url():
    """把 url 塞进 SSOT 名单（安全后门：URL 能携带凭据）。"""
    src = read(SSOT)
    anchor = '\t\t"address",\n'
    if src.count(anchor) != 1:
        raise RuntimeError("锚点命中 %d 次（应为 1）" % src.count(anchor))
    write(SSOT, src.replace(anchor, anchor + '\t\t"url",\n'))


def m03_add_ip_rule_to_builtin_yaml():
    """往内置 YAML 里加一条名为 ip 的规则（落进 TypeCustom ⇒ 开始遮蔽）。"""
    src = read(YAML)
    anchor = "    # 中国身份证：18位数字或17位数字+X\n    id_card:"
    if src.count(anchor) != 1:
        raise RuntimeError("锚点命中 %d 次（应为 1）" % src.count(anchor))
    mutant = """    # 变异臂：模拟有人往内置 YAML 里加了一条名为 ip 的规则
    ip:
      regex: '\\b(?:\\d{1,3}\\.){3}\\d{1,3}\\b'
      confidence: 0.7
      description: "变异臂用"

    # 中国身份证：18位数字或17位数字+X
    id_card:"""
    write(YAML, src.replace(anchor, mutant))


def m04_delete_manifest_entry():
    """删掉一条清单登记（登记过期 ⇒ 门必须红）。"""
    src = read(MANIFEST)
    anchor = '\t"security/sanitize/detector.go|scopedType|membership-call": {'
    if src.count(anchor) != 1:
        raise RuntimeError("锚点命中 %d 次（应为 1）" % src.count(anchor))
    start = src.index(anchor)
    end = src.index("\t},\n", start) + len("\t},\n")
    write(MANIFEST, src[:start] + src[end:])


def m05_inject_second_list_in_production():
    """在生产代码里长出第二份地址族名单（未被登记）。"""
    write(INJECT, "package sanitize\n\n// r1040 变异臂：模拟「有人在业务代码里又写了一份名单」。\nvar r1040MutationList = []string{\"ip\", \"domain\"}\n")


def m06_inject_and_register():
    """M05 + 把新面**登记进清单** ⇒ 期望门【仍绿】（登记即可，不是禁止一切）。"""
    m05_inject_second_list_in_production()
    src = read(MANIFEST)
    anchor = '\t// ── 误报：同名不同义 ────────────────────────────────────────────────\n'
    if src.count(anchor) != 1:
        raise RuntimeError("锚点命中 %d 次（应为 1）" % src.count(anchor))
    entry = """\t"security/sanitize/zz_r1040_mutation_list.go||type-list": {
\t\tClass: classReviewedException,
\t\tWhy: "★ M06 变异臂登记的测试面：证明门是「必须登记」而不是「一律禁止」。",
\t},
"""
    write(MANIFEST, src.replace(anchor, anchor + entry))


# ───────────────────────── 探针 ─────────────────────────

def probe_m01(before):
    if not probe_file_changed(MANAGED, before[MANAGED]):
        return "FAIL: managed_rules.go 内容没变"
    if read(MANAGED).count('case "ip", "ip_address", "ipv6":') != 1:
        return "FAIL: 本地映射分支没打上"
    return "OK: 第三面已复原（本地映射分支 1 处）"


def probe_m02(before):
    if not probe_file_changed(SSOT, before[SSOT]):
        return "FAIL: address_family.go 内容没变"
    if read(SSOT).count('\t\t"url",\n') != 1:
        return "FAIL: url 没进名单"
    return "OK: url 已进 SSOT 名单"


def probe_m03(before):
    if not probe_file_changed(YAML, before[YAML]):
        return "FAIL: sensitive_patterns.yaml 内容没变"
    if read(YAML).count("\n    ip:\n") != 1:
        return "FAIL: ip 规则没加进去"
    return "OK: YAML 里多了 ip 规则"


def probe_m04(before):
    if not probe_file_changed(MANIFEST, before[MANIFEST]):
        return "FAIL: manifest_test.go 内容没变"
    if read(MANIFEST).count("security/sanitize/detector.go|scopedType|membership-call") != 0:
        return "FAIL: 清单条目还在"
    return "OK: 清单条目已删"


def probe_m05(before):
    if not os.path.exists(INJECT):
        return "FAIL: 注入文件不存在"
    if read(INJECT).count('"ip", "domain"') != 1:
        return "FAIL: 注入内容不对"
    return "OK: 生产代码里多了一份名单"


def probe_m06(before):
    if not os.path.exists(INJECT):
        return "FAIL: 注入文件不存在"
    if read(MANIFEST).count("zz_r1040_mutation_list.go") != 1:
        return "FAIL: 登记没写进去"
    return "OK: 新面已登记进清单"


def main():
    print("=" * 78)
    print("r1040 变异台：验证「地址族三道门绿 ≠ 门有牙」")
    print("=" * 78)
    print("环境指纹：git status 行数 =", len(git_status_lines()))
    print()

    arm("M01 第三面复发（把类型映射抄回 Checker.RuleConfig）", True, m01_reintroduce_third_face, probe_m01, [
        (PKG_OUTPUTCOMPLIANCE, "TestR1040", "TestR1040ManagedExportAndLegacyImportAgreeOnEveryDeclaredType"),
        (PKG_AUDIT, "TestAddressFamilyDecisionSitesAreAllRegistered", "TestAddressFamilyDecisionSitesAreAllRegistered"),
    ])

    arm("M02 SSOT 名单被放宽到 url（安全后门）", True, m02_widen_ssot_with_url, probe_m02, [
        (PKG_SANITIZERULES, None, "TestR1038bAddressFamilyListIsSharedByBothFaces"),
        (PKG_OUTPUTCOMPLIANCE, None, "TestR1038bOutputComplianceDoesNotExemptCredentialBearingTypes"),
        (PKG_AUDIT, "TestAddressFamilyTypeNameCopyMatchesTheSSOT", "TestAddressFamilyTypeNameCopyMatchesTheSSOT"),
    ])

    arm("M03 内置 YAML 新增地址族规则（ip）", True, m03_add_ip_rule_to_builtin_yaml, probe_m03, [
        (PKG_SANITIZE, "TestR1040", "TestR1040NoUnregisteredAddressNamedBuiltinRule"),
    ])

    arm("M04 清单条目过期（删掉一条登记）", True, m04_delete_manifest_entry, probe_m04, [
        (PKG_AUDIT, "TestAddressFamilyDecisionSitesAreAllRegistered", "TestAddressFamilyDecisionSitesAreAllRegistered"),
    ])

    arm("M05 生产代码里长出第二份地址族名单（未登记）", True, m05_inject_second_list_in_production, probe_m05, [
        (PKG_AUDIT, "TestNoProductionFileMayCarryItsOwnAddressFamilyList", "TestNoProductionFileMayCarryItsOwnAddressFamilyList"),
    ])

    arm("M06 同 M05，但把新面登记进清单（期望仍绿）", False, m06_inject_and_register, probe_m06, [
        (PKG_AUDIT, None, None),
    ])

    print("-" * 78)
    ok = 0
    for name, passed, msg in RESULTS:
        mark = "✅" if passed else "❌"
        print("%s %-56s %s" % (mark, name, "" if passed else msg))
        if passed:
            ok += 1
    print("-" * 78)
    print("结果：%d/%d 通过" % (ok, len(RESULTS)))
    print("环境指纹（末）：git status 行数 =", len(git_status_lines()))
    return 0 if ok == len(RESULTS) else 1


if __name__ == "__main__":
    sys.exit(main())
