#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.96 会话族 final_success 自愈门的变异台：★ 验证「门绿 ≠ 门有牙」。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_14396_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 七条纪律（★ 与前几轮同构）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须改【全部】出现处（★ 且要声明改了几处）；
★   ③ 变异必须【语义变了但仍能编译】（★ 编译红 ≠ 断言红）；
★   ④ 环境指纹对拍（★ 共享工作树里 ⇒ ⇒ 并发改动会被误读成「门没牙」）；
★   ⑤ 变异必须落在【判据实际读到的那一处】；
★   ⑥ ★★ 变异必须落在【函数体作用域内】时才用「函数体作用域」计数
★      （★ 否则「可执行面里出现 0 次」这条自检会被同名片段骗过）；
★   ⑦ ★★★ **每个变异都必须自带【效果探针】** —— ★ 「目标 needle 在【判据实际读的那一面】
★      里的出现次数变成 0」+「文件内容确实变了（changed == True）」⇒ ⇒
★      ★★ 这是 §143.96 的新增纪律：★ §143.95 轮的变异台出现过「变异其实没生效、
★      ★★ 而门照样绿」⇒ ⇒ ⇒ 于是那一轮的读数作废 ⇒ ⇒ ⇒ 治法固定为【每臂带探针】。

★ ★★ 本轮特有的一条：
★   ⑧ ★★ 本轮的门有一条【逐行差异判据】（897 减去 demote 段 == 869 的函数体）
★      ⇒ ⇒ 它能抓住「有人顺手改了 869 的两道牙」⇒ ⇒ ⇒ 变异台里 M5 / M6 就是冲它去的。
"""
import hashlib
import os
import subprocess
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))

MIGRATION = os.path.join(ROOT, "sql/migrations/startup/897_session_turns_promote_final_success_self_heal.sql")
DOWN = os.path.join(ROOT, "sql/migrations/startup/897_session_turns_promote_final_success_self_heal.down.sql")
EMBED = os.path.join(ROOT, "installer/cmd/llm-gw-installer/embeddata/startup/897_session_turns_promote_final_success_self_heal.sql")
EMBED_DOWN = os.path.join(ROOT, "installer/cmd/llm-gw-installer/embeddata/startup/897_session_turns_promote_final_success_self_heal.down.sql")
MAIN = os.path.join(ROOT, "installer/cmd/llm-gw-installer/main.go")
RUNNER = os.path.join(ROOT, "installer/internal/dbinit/runner.go")
TSV = os.path.join(ROOT, "sql/schema/installed_startup_migrations.tsv")
CHANNEL = os.path.join(ROOT, "scripts/apply-db-revision-sequence.sh")
RUNBOOK = os.path.join(
    ROOT,
    "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md",
)
LESSONS = os.path.join(ROOT, "LESSONS.md")
AGENTS = os.path.join(ROOT, "AGENTS.md")

WATCHED = (MIGRATION, DOWN, EMBED, EMBED_DOWN, MAIN, RUNNER, TSV, CHANNEL,
           RUNBOOK, LESSONS, AGENTS)

FUNC_HEAD = "CREATE OR REPLACE FUNCTION public.promote_session_turns_hot_to_partition("

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
    print("✅ 还原校验：%d 个文件 md5 逐字一致" % len(backups))


def git_status():
    p = subprocess.run(["git", "status", "--porcelain"], cwd=ROOT,
                       capture_output=True, text=True)
    return p.stdout


def file_md5(path):
    with open(path, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def env_fingerprint():
    return {"status": git_status(), "md5": {p: file_md5(p) for p in WATCHED}}


ENV_BEFORE = env_fingerprint()


def run_gate(pattern):
    env = dict(os.environ)
    env.pop("TEST_DATABASE_URL", None)
    p = subprocess.run(
        ["go", "test", "./cmd/gateway/", "-run", pattern, "-count=1"],
        cwd=ROOT, capture_output=True, text=True, env=env, timeout=900,
    )
    return p.returncode != 0, (p.stdout + p.stderr)


results = []


def check(name, expect_red, pattern, mutate, restore, probe):
    """★ 每个臂都带【效果探针】⇒ ⇒ 变异没生效的那一臂会被当场判失败。"""
    for p in WATCHED:
        snap(p)
    try:
        mutate()
        # ★★ 纪律⑦之一：文件内容确实变了。
        changed = False
        for p in WATCHED:
            with open(p, "rb") as f:
                if f.read() != backups[p]:
                    changed = True
                    break
        if not changed:
            print("🔴 " + name + " ⇒ ★★ 变异没有改变任何文件 ⇒ ★★ 本臂作废")
            results.append((name, False))
            return
        # ★★ 纪律⑦之二：目标 needle 在【判据实际读的那一面】里出现次数变成 0。
        if not probe():
            print("🔴 " + name + " ⇒ ★★ 效果探针不通过 ⇒ ★★ 变异没有落到判据实际读的那一处")
            results.append((name, False))
            return
        red, out = run_gate(pattern)
        ok = (red == expect_red)
        if expect_red:
            print(("🔴 " if red else "🟢 ") + name + (" ⇒ 变红 ✔" if red else " ⇒ 仍然绿 ❌"))
        else:
            print(("🟢 " if not red else "🔴 ") + name + (" ⇒ 仍然绿 ✔" if not red else " ⇒ 变红 ❌"))
        if not ok:
            print(out[-1800:])
        results.append((name, ok))
    finally:
        restore()
        restore_all()


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def write(path, s):
    with open(path, "w", encoding="utf-8") as f:
        f.write(s)


def executable_text(path):
    """★ 判据实际读的那一面：★ 剥掉整行 `--` 注释。"""
    out = []
    for line in read(path).split("\n"):
        t = line.strip()
        if not t or t.startswith("--"):
            continue
        out.append(t)
    return "\n".join(out)


def func_body_text(path):
    """★ 判据实际读的那一面（函数体作用域）：★ 剥注释后再截函数体。"""
    s = executable_text(path)
    i = s.index(FUNC_HEAD)
    j = s.index("\n$$;", i)
    return s[i:j]


def replace_all(path, old, new, expect_n=None):
    """★ 变异必须改【全部】出现处（★ 并声明改了几处）。"""
    s = read(path)
    n = s.count(old)
    assert n > 0, "needle 在 %s 里出现 0 次：%r" % (path, old)
    if expect_n is not None:
        assert n == expect_n, "needle 在 %s 里出现 %d 次（期望 %d）" % (path, n, expect_n)
    write(path, s.replace(old, new))
    return n


def replace_in_func_body(path, old, new, expect_n=1):
    """★ 只在函数体作用域内替换（★ 纪律⑥）。

    ★ 为什么必须限定作用域：★ 897 的【可执行面】里，同一段文字会出现两次
    ★（★ 一次在函数体、★ 一次在后置条件的字符串字面量里）⇒ ⇒ 若不限定作用域，
    ★ 「出现次数变成 0」这条自检就永远不成立 ⇒ ⇒ ⇒ 探针会失效。
    """
    s = executable_text(path)
    i = s.index(FUNC_HEAD)
    j = s.index("\n$$;", i)
    body = s[i:j]
    n = body.count(old)
    assert n > 0, "needle 在 %s 的函数体里出现 0 次：%r" % (path, old)
    assert n == expect_n, "needle 在 %s 的函数体里出现 %d 次（期望 %d）" % (path, n, expect_n)
    newbody = body.replace(old, new)
    write(path, s[:i] + newbody + s[j:])
    return n


def restore_path(path):
    with open(path, "wb") as f:
        f.write(backups[path])


# ---------- M1：★ demote 步被摘掉（★ 判据①：★ 「只插入一处」被违反） ----------
def m1_mut():
    replace_in_func_body(MIGRATION, "SET is_final_success = FALSE",
                         "SET is_final_success_x = FALSE")
def m1_restore():
    restore_path(MIGRATION)
def m1_probe():
    return "SET is_final_success = FALSE" not in func_body_text(MIGRATION)


# ---------- M2：★★ demote 的键被换回 promote 自己的键（★ 判据③） ----------
def m2_mut():
    replace_in_func_body(MIGRATION, "AND p.session_id = h.session_id",
                         "AND p.request_id = h.request_id")
def m2_restore():
    restore_path(MIGRATION)
def m2_probe():
    return "AND p.session_id = h.session_id" not in func_body_text(MIGRATION)


# ---------- M3：★ demote 的谓词被反向（★ 判据③的反向禁） ----------
def m3_mut():
    replace_in_func_body(MIGRATION, "AND p.is_final_success IS TRUE",
                         "AND p.is_final_success IS FALSE")
def m3_restore():
    restore_path(MIGRATION)
def m3_probe():
    return "AND p.is_final_success IS TRUE" not in func_body_text(MIGRATION)


# ---------- M4：★★ demote 步被移到【批次选取之后】（★ 判据②：★ 位置即语义） ----------
def m4_mut():
    s = read(MIGRATION)
    start = s.index("    UPDATE public.session_turns_hot h\n")
    endmark = "            v_demoted;\n    END IF;\n"
    end = s.index(endmark, start) + len(endmark)
    block = s[start:end]
    s2 = s[:start] + s[end:]
    anchor = "    LIMIT p_batch_size;\n"
    assert s2.count(anchor) == 1
    s2 = s2.replace(anchor, anchor + "\n" + block)
    write(MIGRATION, s2)
def m4_restore():
    restore_path(MIGRATION)
def m4_probe():
    body = func_body_text(MIGRATION)
    return body.index("UPDATE public.session_turns_hot h") > body.index("INSERT INTO session_turns_promotion_batch (")


# ---------- M5：★★ 869 的【形状守卫】被改（★ 判据②：★ 逐行差异判据） ----------
def m5_mut():
    replace_in_func_body(MIGRATION,
                         "IF v_parent_shape IS DISTINCT FROM v_hot_shape THEN",
                         "IF v_parent_shape IS NOT DISTINCT FROM v_hot_shape THEN")
def m5_restore():
    restore_path(MIGRATION)
def m5_probe():
    return "IS NOT DISTINCT FROM" in func_body_text(MIGRATION)


# ---------- M6：★★ 869 的【目录派生列清单】被改成硬编码（★ 判据②） ----------
def m6_mut():
    replace_in_func_body(MIGRATION, "SELECT %s FROM moved", "SELECT id FROM moved")
def m6_restore():
    restore_path(MIGRATION)
def m6_probe():
    return "SELECT %s FROM moved" not in func_body_text(MIGRATION)


# ---------- M7：★ 后置条件的「demote 在位」断言被摘掉（★ 判据⑤） ----------
def m7_mut():
    replace_all(MIGRATION,
                "IF v_prosrc NOT LIKE '%SET is_final_success = FALSE%' THEN",
                "IF v_prosrc NOT LIKE '%SET is_final_success_x = FALSE%' THEN",
                expect_n=1)
def m7_restore():
    restore_path(MIGRATION)
def m7_probe():
    return "IF v_prosrc NOT LIKE '%SET is_final_success = FALSE%' THEN" not in executable_text(MIGRATION)


# ---------- M8：★ down 体不再等于 869（★ 判据⑥） ----------
def m8_mut():
    replace_in_func_body(DOWN, "v_conflicts BIGINT := 0;", "v_conflicts_x BIGINT := 0;")
def m8_restore():
    restore_path(DOWN)
def m8_probe():
    return "v_conflicts_x BIGINT" in func_body_text(DOWN)


# ---------- M9：★ embeddata 副本与 canonical 漂移（★ 判据④） ----------
def m9_mut():
    s = read(EMBED)
    write(EMBED, s.rstrip("\n") + "\n-- ★ 变异体：embeddata 副本漂移\n")
def m9_restore():
    restore_path(EMBED)
def m9_probe():
    return read(EMBED) != read(MIGRATION)


# ---------- M10：★ installer 的 embeddedSQLFiles 映射被摘掉（★ 判据⑦） ----------
def m10_mut():
    replace_all(MAIN,
                "\"startup/897_session_turns_promote_final_success_self_heal.sql\":",
                "\"startup/897_session_turns_promote_final_success_self_heal_x.sql\":",
                expect_n=1)
def m10_restore():
    restore_path(MAIN)
def m10_probe():
    return "\"startup/897_session_turns_promote_final_success_self_heal.sql\":" not in read(MAIN)


# ---------- M11：★ StartupFiles 的登记被摘掉（★ 判据⑦） ----------
def m11_mut():
    replace_all(RUNNER,
                "\"897_session_turns_promote_final_success_self_heal.sql\",",
                "\"897_session_turns_promote_final_success_self_heal_x.sql\",",
                expect_n=1)
def m11_restore():
    restore_path(RUNNER)
def m11_probe():
    return "\"897_session_turns_promote_final_success_self_heal.sql\"," not in read(RUNNER)


# ---------- M12：★ manifest tsv 的登记行被摘掉（★ 判据⑦） ----------
def m12_mut():
    s = read(TSV)
    lines = [l for l in s.split("\n")
             if "897_session_turns_promote_final_success_self_heal.sql" not in l]
    write(TSV, "\n".join(lines))
def m12_restore():
    restore_path(TSV)
def m12_probe():
    return "897_session_turns_promote_final_success_self_heal.sql" not in read(TSV)


# ---------- M13：★ 通道腿 files=() 的登记被摘掉（★ 判据⑦） ----------
def m13_mut():
    replace_all(CHANNEL,
                "\"$ROOT_DIR/sql/migrations/startup/897_session_turns_promote_final_success_self_heal.sql\"",
                "\"$ROOT_DIR/sql/migrations/startup/897_session_turns_promote_final_success_self_heal_x.sql\"",
                expect_n=1)
def m13_restore():
    restore_path(CHANNEL)
def m13_probe():
    return "$ROOT_DIR/sql/migrations/startup/897_session_turns_promote_final_success_self_heal.sql" not in read(CHANNEL)


# ---------- M14：★ intentional_function_chains 里 897 被摘掉（★ 判据⑦） ----------
def m14_mut():
    replace_all(CHANNEL,
                "|897_session_turns_promote_final_success_self_heal.sql|",
                "|",
                expect_n=1)
def m14_restore():
    restore_path(CHANNEL)
def m14_probe():
    return "|897_session_turns_promote_final_success_self_heal.sql|" not in read(CHANNEL)


# ---------- M15：★★ 897 在链里但【不是最后一项】（★ 判据⑦：★ 顺序约束） ----------
def m15_mut():
    replace_all(CHANNEL,
                "869_promote_session_turns_trace_events.sql|897_session_turns_promote_final_success_self_heal.sql|",
                "897_session_turns_promote_final_success_self_heal.sql|869_promote_session_turns_trace_events.sql|",
                expect_n=1)
def m15_restore():
    restore_path(CHANNEL)
def m15_probe():
    for line in read(CHANNEL).split("\n"):
        t = line.strip()
        if t.startswith("'promote_session_turns_hot_to_partition|"):
            return not t.endswith("897_session_turns_promote_final_success_self_heal.sql|'")
    return False


# ---------- M16：★ 文档登记被摘掉（★ 判据⑧） ----------
def m16_mut():
    # ★★ needle 必须带【尾部空格】—— ★ "### §143.96" 是 "#### §143.96.1" 的前缀
    # ★ ★⇒⇒ 不带边界 ⇒ 出现 9 次 ⇒ ⇒ 断言直接炸 ⇒ ⇒ 而炸掉的是【变异台自己】。
    replace_all(RUNBOOK, "\n### §143.96 ", "\n### §143.99 ", expect_n=1)
    replace_all(LESSONS, "## §290", "## §291", expect_n=1)
    replace_all(AGENTS, "R140 轮", "R141 轮", expect_n=1)
def m16_restore():
    restore_path(RUNBOOK)
    restore_path(LESSONS)
    restore_path(AGENTS)
def m16_probe():
    return ("\n### §143.96 " not in read(RUNBOOK)
            and "## §290" not in read(LESSONS)
            and "R140 轮" not in read(AGENTS))


# ---------- M17（★ 期望【仍然绿】）：★ demote 的关键文字只出现在【注释】里 ----------
def m17_mut():
    # ★★ 这一臂验的是【反向禁】只查【可执行面】—— ★ 所以把【被禁的错键形态】
    # ★ ★ 直接塞进 demote 段里、但只放在【注释】中 ⇒ ⇒ 门必须【仍然绿】。
    # ★ ★⇒⇒ 若门红 ⇒ ⇒ 说明反向禁查的是【原文】而不是【可执行面】⇒ ⇒ 门过严。
    # ★★ canonical 与 embeddata 副本必须一起改（★ 否则字节门会正确地变红，
    # ★★ 而这一臂要验的是【门不许过严】⇒ ⇒ ⇒ 变红的原因是【变异本身不合法】）。
    for path in (MIGRATION, EMBED):
        s = read(path)
        anchor = "    UPDATE public.session_turns_hot h\n"
        assert s.count(anchor) == 1
        ins = "    -- ★ 变异体：AND p.request_id = h.request_id（★ 错键形态只出现在注释里）\n"
        write(path, s.replace(anchor, ins + anchor))
def m17_restore():
    restore_path(MIGRATION)
    restore_path(EMBED)
def m17_probe():
    raw = read(MIGRATION)
    # ★ 探针必须限定在【函数体作用域】—— ★ 897 的【可执行面】里，
    # ★ 后置条件的字符串字面量也会命中同类文本 ⇒ ⇒ 不限定 ⇒ ⇒ 探针永远不成立。
    return ("-- ★ 变异体：AND p.request_id = h.request_id" in raw
            and "p.request_id = h.request_id" not in func_body_text(MIGRATION))


# ---------- M18（★ 期望【仍然绿】）：★ 函数体内加一段与判据无关的注释 ----------
def m18_mut():
    # ★★ canonical 与 embeddata 副本**必须一起改** —— ★ 只改一份 ⇒ ⇒
    # ★ ⇒ 字节门会（正确地）变红 ⇒ ⇒ 而这一臂要验的是「门不许过严」⇒ ⇒ ⇒
    # ★ ⇒ 变红的原因是【变异本身不合法】，不是【门没牙】。
    for path in (MIGRATION, EMBED):
        s = read(path)
        anchor = "    RETURN v_moved;\n"
        assert s.count(anchor) == 1
        ins = "    -- ★ 变异体：一段与判据完全无关的注释\n"
        write(path, s.replace(anchor, ins + anchor))
def m18_restore():
    restore_path(MIGRATION)
    restore_path(EMBED)
def m18_probe():
    raw = read(MIGRATION)
    return ("-- ★ 变异体：一段与判据完全无关的注释" in raw
            and "★ 变异体：一段与判据完全无关的注释" not in func_body_text(MIGRATION))


check("M1 ★ demote 步被摘掉（★ 判据①：★「只插入一处」被违反）", True,
      "TestSection14396", m1_mut, m1_restore, m1_probe)
check("M2 ★★ demote 的键被换回 promote 自己的键（★ 判据③）", True,
      "TestSection14396", m2_mut, m2_restore, m2_probe)
check("M3 ★ demote 的谓词被反向（★ 判据③的反向禁）", True,
      "TestSection14396", m3_mut, m3_restore, m3_probe)
check("M4 ★★ demote 步被移到批次选取之后（★ 判据②：★ 位置即语义）", True,
      "TestSection14396", m4_mut, m4_restore, m4_probe)
check("M5 ★★ 869 的形状守卫被改（★ 判据②：★ 逐行差异判据）", True,
      "TestSection14396", m5_mut, m5_restore, m5_probe)
check("M6 ★★ 869 的目录派生列清单被改成硬编码（★ 判据②）", True,
      "TestSection14396", m6_mut, m6_restore, m6_probe)
check("M7 ★ 后置条件的「demote 在位」断言被摘掉（★ 判据⑤）", True,
      "TestSection14396", m7_mut, m7_restore, m7_probe)
check("M8 ★ down 体不再等于 869（★ 判据⑥）", True,
      "TestSection14396", m8_mut, m8_restore, m8_probe)
check("M9 ★ embeddata 副本与 canonical 漂移（★ 判据④）", True,
      "TestSection14396", m9_mut, m9_restore, m9_probe)
check("M10 ★ installer 的 embeddedSQLFiles 映射被摘掉（★ 判据⑦）", True,
      "TestSection14396", m10_mut, m10_restore, m10_probe)
check("M11 ★ StartupFiles 的登记被摘掉（★ 判据⑦）", True,
      "TestSection14396", m11_mut, m11_restore, m11_probe)
check("M12 ★ manifest tsv 的登记行被摘掉（★ 判据⑦）", True,
      "TestSection14396", m12_mut, m12_restore, m12_probe)
check("M13 ★ 通道腿 files=() 的登记被摘掉（★ 判据⑦）", True,
      "TestSection14396", m13_mut, m13_restore, m13_probe)
check("M14 ★ intentional_function_chains 里 897 被摘掉（★ 判据⑦）", True,
      "TestSection14396", m14_mut, m14_restore, m14_probe)
check("M15 ★★ 897 在链里但不是最后一项（★ 判据⑦：★ 顺序约束）", True,
      "TestSection14396", m15_mut, m15_restore, m15_probe)
check("M16 ★ 文档登记被摘掉（★ 判据⑧）", True,
      "TestSection14396", m16_mut, m16_restore, m16_probe)
check("M17 demote 的关键文字只出现在注释里（★ 期望【仍然绿】：★ 判据查可执行面）", False,
      "TestSection14396", m17_mut, m17_restore, m17_probe)
check("M18 函数体内加一段与判据无关的注释（★ 期望【仍然绿】：★ 门不许过严）", False,
      "TestSection14396", m18_mut, m18_restore, m18_probe)

print()
n_ok = sum(1 for _, ok in results if ok)
print("★ 变异台结果：%d / %d 全部符合预期" % (n_ok, len(results)))

env_after = env_fingerprint()
env_ok = True
for p in WATCHED:
    if env_after["md5"][p] != ENV_BEFORE["md5"][p]:
        print("❌ 环境指纹不一致：%s 的 md5 变了（★ 可能是并行会话的并发改动）" % p)
        env_ok = False
if env_after["status"] != ENV_BEFORE["status"]:
    print("⚠ 全仓 git status 在变异期间发生变化（★ 并行会话的并发改动）")
    env_ok = False
if env_ok:
    print("✅ 环境指纹一致：★ %d 个文件 md5 逐字未变 + 全仓 git status 未变" % len(WATCHED))

sys.exit(0 if (n_ok == len(results) and env_ok) else 1)
