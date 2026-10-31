#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.97 会话族 hot 排水门的变异台：★ 验证「门绿 ≠ 门有牙」。

★ 用法（★ 在仓库根目录执行）：

    python3 scripts/audit/mutate_14397_gate.py

★ 退出码：0 = 全部符合预期；非 0 = 有用例的预期没达成。

★ ★★ 八条纪律（★ 与前几轮同构）：
★   ① 还原必须按【原路径】做 md5 逐字校验；
★   ② 变异必须改【全部】出现处（★ 且要声明改了几处）；
★   ③ 变异必须【语义变了但仍能编译】（★ 编译红 ≠ 断言红）；
★   ④ 环境指纹对拍（★ 共享工作树里 ⇒ ⇒ 并发改动会被误读成「门没牙」）；
★   ⑤ 变异必须落在【判据实际读到的那一处】；
★   ⑥ ★★ 变异必须落在【函数体作用域内】时才用「函数体作用域」计数；
★   ⑦ ★★★ **每个变异都必须自带【效果探针】** —— ★ 「目标 needle 在【判据实际读的那一面】
★      里的出现次数变成 0」+「文件内容确实变了（changed == True）」；
★      ★★ ★★ **needle 必须与【实际写入的形态】逐字对齐** —— ★ 本轮首跑 M03 就栽在这里：
★      ★★ needle 写成 `WHERE ts > now()`（比实际写入的
★      ★★ `WHERE ts > now() - interval '8 hours'` **短**）⇒ ⇒ **探针恒成立**
★      ★★ ⇒ ⇒ 变异台自己崩（★ 与 §143.96 记的「探针作用域」同族的**反向**形态：
★      ★★ ★★ 那次是探针查得太宽撞上字面量，这次是探针查得太窄 ⇒ ⇒
★      ★★ ★★ **⇒ 探针的 needle 长度与内容都必须与变异写入的形态一致**）。
★   ⑧ ★★★ **探针的【方向】必须与变异的方向一致** —— ★ 这是本轮 M03 连栽两次的
★      根因（★ §143.96 记的「探针作用域」同族的**第三种形态**）：
★      ★★ 「删掉 X」的变异 ⇒ 探针查 **X 归零**；
★      ★★ 「引入 X」的变异 ⇒ 探针查 **X 出现**；
★      ★★ ⇒ ★★ 用反方向 ⇒ ⇒ 探针**恒失败** ⇒ ⇒ 变异台自己崩 ⇒ ⇒
★      ★★ ⇒ **「效果探针失败」与「门没牙」必须分开读**（★ §143.96 的纪律）。
★   ⑨ ★★★ **跑变异台前必须先 commit 或 stash** —— ★ 否则工作树里已有的未提交改动
★      会与「还原校验」混在一起 ⇒ ⇒ 读数不可信。

★ ★★ 本轮特有的三条：
★   ⑨ ★★ 本轮的门有一条【逐字判据】（899 的函数体 == db/db.go 的 ensure 体）
★      ⇒ ⇒ 它能抓住「有人顺手改了 Go ensure 或迁移里的任一份」⇒ ⇒ ⇒
★      ⇒ 变异台里 M5 / M6 / M7 就是冲它去的。
★      ★★ 且注意：★ **只改一份 ⇒ 字节门会正确地红** ⇒ ⇒ 那不是「门过严」，而是【字节门有牙】。
★   ⑩ ★★★★ **「变异必须改【全部】出现处」的「全部」不是「同一字符串的全部字节位置」**
★      ★★ 而是★ 【判据实际读到的那一面的全部等价形态】——
★      ★★ 首版 M04 只替换了 898 标记的**一种拼写**（首行 `-- dbinit:no-transaction`），
★      ★★ 而 898 第 139 行还有**第二种拼写**（`-- ★ ★ dbinit:no-transaction 标记`）
★      ★★ ⇒ ⇒ installer's requiresNoTransaction 谓词**仍为 true**
★      ★★ ⇒ ⇒ 变异是【惰性的】⇒ ⇒ **门绿是对的**⇒ ⇒ 首版 M04 的期望本身是错的
★      ★★ ⇒ ⇒ 修法：M04 改成「删掉【所有】满足谓词的注释行」。
★   ⑪ ★★★★ **「门没牙」这个结论必须先问「变异是不是惰性的」** ——
★      ★★ 读数「期望红但门仍绿」有**两种**成因：★ (a) 门真的没牙；
★      ★★ (b) 变异没造成功能回归 ⇒ ⇒ ⇒ **先验证变异的【效果】**（用
★      ★★ installer's 真谓词复算一次），★ 再判是 (a) 还是 (b)。
★      ★★ ⇒ ⇒ 本轮 M04 落在 (b) ⇒ ⇒ **误报了门的缺陷**（★ 已订正）。
★   ⑫ ★★★ **门要守【真函数】，不是它的一份拷贝** ⇒ ⇒ 本轮加第二道门
★      ★★ （installer/internal/dbinit/untransactable_requires_marker_test.go）
★      ★★ 它**调用** requiresNoTransaction ⇒ ⇒ installer 改口径时门自动跟随
★      ★★ ⇒ ⇒ M24（改 installer's token）就是冲它去的。
★   ⑬ ★★★★ **探针的【取值来源】也必须对齐判据** —— ★ 这是 §143.96 记的
★      ★★ 「探针作用域必须与判据实际读的那一面逐字对齐」的**又一种形态**：
★      ★★ 首版 M24 的探针**硬编码**了 token `dbinit:no-transaction`，
★      ★★ 而 M24 恰恰是**改 token** 的变异 ⇒ ⇒ 探针永远拿**旧 token** 去判
★      ★★ ⇒ ⇒ 判出来的结论与 installer's 真实行为无关 ⇒ ⇒ 探针恒失败 ⇒ 崩。
★      ★★ ⇒ ⇒ **探针必须从判据的源码里解析出当前取值**（本台用
★      ★★ `installer_marker_token()` 解析 runner.go 的常量）。
"""
import hashlib
import os
import re
import subprocess
import sys

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

MIG898 = os.path.join(ROOT, "sql/migrations/startup/898_session_bodies_hot_ts_index.sql")
DOWN898 = os.path.join(ROOT, "sql/migrations/startup/898_session_bodies_hot_ts_index.down.sql")
MIG899 = os.path.join(ROOT, "sql/migrations/startup/899_session_bodies_promote_body_sink.sql")
DOWN899 = os.path.join(ROOT, "sql/migrations/startup/899_session_bodies_promote_body_sink.down.sql")
EMBED898 = os.path.join(ROOT, "installer/cmd/llm-gw-installer/embeddata/startup/898_session_bodies_hot_ts_index.sql")
EMBED898D = os.path.join(ROOT, "installer/cmd/llm-gw-installer/embeddata/startup/898_session_bodies_hot_ts_index.down.sql")
EMBED899 = os.path.join(ROOT, "installer/cmd/llm-gw-installer/embeddata/startup/899_session_bodies_promote_body_sink.sql")
EMBED899D = os.path.join(ROOT, "installer/cmd/llm-gw-installer/embeddata/startup/899_session_bodies_promote_body_sink.down.sql")
MAIN = os.path.join(ROOT, "installer/cmd/llm-gw-installer/main.go")
RUNNER = os.path.join(ROOT, "installer/internal/dbinit/runner.go")
TSV = os.path.join(ROOT, "sql/schema/installed_startup_migrations.tsv")
CHANNEL = os.path.join(ROOT, "scripts/apply-db-revision-sequence.sh")
DBGO = os.path.join(ROOT, "db/db.go")
RUNBOOK = os.path.join(
    ROOT,
    "docs/06-deployment/04-runbooks/runbooks/2026-10-04-ursm-write-amplification-measurement.md",
)
LESSONS = os.path.join(ROOT, "LESSONS.md")
AGENTS = os.path.join(ROOT, "AGENTS.md")

WATCHED = (MIG898, DOWN898, MIG899, DOWN899, EMBED898, EMBED898D, EMBED899, EMBED899D,
           MAIN, RUNNER, TSV, CHANNEL, DBGO, RUNBOOK, LESSONS, AGENTS)

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


def read(path):
    with open(path, "r", encoding="utf-8") as f:
        return f.read()


def write(path, text):
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


def executable_surface(path):
    """剥掉整行注释后的可执行面（★ 与门同口径）。"""
    out = []
    for line in read(path).split("\n"):
        t = line.strip()
        if not t or t.startswith("--"):
            continue
        out.append(t)
    return "\n".join(out)


def func_body(path):
    """截 $function$ ... $function$ 之间的函数体原文（★ 保留缩进）。"""
    raw = read(path)
    start = raw.index("AS $function$\n") + len("AS $function$\n")
    end = raw.index("\n$function$;", start)
    return raw[start:end]


def go_ensure_body():
    """从 db/db.go 取 ensureSessionBodiesPromoteDrain 的函数体原文。"""
    raw = read(DBGO)
    marker = "func (d *DB) ensureSessionBodiesPromoteDrain(ctx context.Context) error {"
    s = raw.index(marker)
    rest = raw[s:]
    b = rest.index("d.pool.Exec(ctx, `") + len("d.pool.Exec(ctx, `")
    e = rest.index("`)", b)
    inner = rest[b:e]
    fs = inner.index("AS $function$\n") + len("AS $function$\n")
    fe = inner.index("$function$;", fs)
    return inner[fs:fe]


def run_gate():
    """★ 跑**两道**门（★ 2026-10-23 增补第二道）：

    ① cmd/gateway 的 §143.97 门 —— 结构面（登记点 / 形态 / 逐字函数体）；
    ② installer/internal/dbinit 的**逆向不变式**门 —— ★ 它**调用真函数**
       requiresNoTransaction（★ 不是拷贝一份判据）⇒ ⇒ installer 改了
       标记口径时这道门自动跟随 ⇒ ⇒ 不会出现「门守的是一份拷贝」。

    ★ ★ 为什么要加第二道：★ §143.97 首跑的 M04 臂读数是
    ★ 「期望红但门仍绿 ⇒ 门没牙」，★ 而逐字复核后的真实结论是
    ★ **那一臂是惰性变异**（★ 只删了标记的一种拼写 ⇒ installer's
    ★ 谓词仍为 true ⇒ 非事务通道仍在）⇒ ⇒ **门绿是对的**。
    ★ ★ 但它暴露了一个真缺口：★ 仓库里没有那道门会抓
    ★ 「**被 installer 真正应用的文件**在需要非事务通道时丢了标记」
    ★ ⇒ ⇒ ② 就是补这个缺口的 ⇒ ⇒ 且 ② 用的是真函数 ⇒ ⇒ M04 这类
    ★ 变异在 ② 上一定会红。
    """
    env = dict(os.environ)
    env.pop("TEST_DATABASE_URL", None)
    targets = (
        ("gateway §143.97",
         ["go", "test", "./cmd/gateway/", "-run", "TestSection14397", "-count=1"],
         ROOT),
        ("installer dbinit 逆向不变式",
         ["go", "test", "./internal/dbinit/", "-run",
          "TestStartupFilesWithUntransactableStatementsAreMarked", "-count=1"],
         os.path.join(ROOT, "installer")),
    )
    ok = True
    chunks = []
    for label, cmd, cwd in targets:
        p = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, env=env)
        if p.returncode != 0:
            ok = False
        chunks.append("---- %s ----\n%s" % (label, p.stdout + p.stderr))
    return ok, "\n".join(chunks)


RESULTS = []


def arm(name, expect_green, mutate, probe=None):
    """跑一条变异臂。

    expect_green=True  ⇒ 变异合法 ⇒ 门应当**仍绿**。
    expect_green=False ⇒ 变异应当让门**变红**（证明门有牙）。
    """
    for path in WATCHED:
        snap(path)
    before_status = git_status()
    before_md5 = {p: file_md5(p) for p in WATCHED}

    try:
        detail = mutate()
        changed = [p for p in WATCHED if file_md5(p) != before_md5[p]]
        if not changed:
            RESULTS.append((name, False, "★ 变异没生效（没有任何受监控文件的字节变化）"))
            return

        if probe is not None:
            ok, msg = probe()
            if not ok:
                RESULTS.append((name, False, "★ 效果探针失败：%s" % msg))
                return

        green, out = run_gate()
        if expect_green and not green:
            RESULTS.append((name, False, "★ 期望绿但门红了（变异不合法 / 门过严）\n%s" % out[-1500:]))
        elif (not expect_green) and green:
            RESULTS.append((name, False, "★ 期望红但门仍绿 ⇒ ★ 门没牙"))
        else:
            RESULTS.append((name, True, "OK（改动 %d 个文件）%s" % (len(changed), detail)))
    finally:
        restore_all()

    after_status = git_status()
    if after_status != before_status:
        RESULTS.append((name, False, "★ 环境指纹不一致（工作树状态变了）"))
        sys.exit(1)


def replace_in_files(paths, old, new, expect_count=None):
    """在若干文件里替换全部出现处；★ 返回 (改了几处, 实际出现次数)。"""
    total = 0
    for p in paths:
        s = read(p)
        n = s.count(old)
        if n:
            write(p, s.replace(old, new))
        total += n
    if expect_count is not None and total != expect_count:
        raise AssertionError("★ 期望改 %d 处，实际 %d 处" % (expect_count, total))
    return total


# ═══════════════════════════════════════════════════════════════════════
# 变异臂
# ═══════════════════════════════════════════════════════════════════════

def m01_ts_index_to_single_column():
    """把 (ts, id, partition_date) 改成单列 (ts) ⇒ 形态判据应红。"""
    n = replace_in_files([MIG898, EMBED898],
                         "(ts, id, partition_date)", "(ts)")
    return "（改了 %d 处）" % n


def m02_drop_concurrently():
    """把 CONCURRENTLY 去掉 ⇒ 活写表上会阻断写入 ⇒ 判据应红。"""
    n = replace_in_files([MIG898, EMBED898],
                         "CREATE INDEX CONCURRENTLY IF NOT EXISTS",
                         "CREATE INDEX IF NOT EXISTS")
    return "（改了 %d 处）" % n


def m03_reintroduce_now_predicate():
    """★ 把 614 当年失败的那个 now() 谓词放回去 ⇒ 判据应红。"""
    n = replace_in_files([MIG898, EMBED898],
                         "USING btree (ts, id, partition_date)",
                         "USING btree (ts) WHERE ts > now() - interval '8 hours'")
    return "（改了 %d 处）" % n


# ★ ★ M03 的探针 needle 必须与【实际写入的形态】逐字对齐。
# ★ ★ 首跑栽在这里：needle 写成 `WHERE ts > now()`（比实际内容短）
# ★ ★ ⇒ 探针恒成立 ⇒ 变异台自己崩（★ 与 §143.96 记的「探针作用域」同族）。
M03_NEEDLE = "WHERE ts > now() - interval '8 hours'"


def installer_marker_token():
    """★ 从 installer 源码解析【当前】的标记 token。

    ★ ★★ 纪律（★ 本轮第 5 处变异台自身问题）：
    ★ ★★ **探针不能硬编码 token** —— ★ M24 这条变异恰恰是**改 token**，
    ★ ★★ 硬编码的探针会永远拿**旧 token** 去判 ⇒ ⇒ 判出来的结论与
    ★ ★★ installer's 真实行为**无关** ⇒ ⇒ 探针恒成立/恒失败 ⇒ ⇒ 崩。
    ★ ★★ ⇒ ⇒ 探针必须读【判据实际用的那个 token】⇒ ⇒ 从源码解析。
    ★ ★★ （★ 这是 §143.96 记的「探针作用域必须与判据实际读的那一面逐字
    ★ ★★ 对齐」的**又一种形态**：★ 那次是作用域错，这次是**取值来源**错。）
    """
    s = read(RUNNER)
    m = re.search(r'const\s+noTransactionMarker\s*=\s*"([^"]+)"', s)
    if not m:
        raise AssertionError("★ 解析 installer's noTransactionMarker 失败 —— "
                             "★ installer 改了常量声明形态？★ 变异台需同步")
    return m.group(1)


def installer_marker_predicate(content):
    """★ 逐字镜像 installer/internal/dbinit/runner.go 的 requiresNoTransaction。

    ★ 判据（★ 与真函数同语义）：
    ★   逐行 TrimSpace ⇒ 以 `--` 开头 ⇒ 该行含【installer 当前的】token。
    ★ ★ 这不是「拷贝一份判据来用」，★ 而是**变异台自己的效果探针**
    ★ ★ —— 用来回答「这次变异有没有真的让 installer's 谓词变 false」。
    """
    tok = installer_marker_token()
    for line in content.split("\n"):
        t = line.strip()
        if t.startswith("--") and tok in t:
            return True
    return False


def strip_marker_lines(path):
    """★ 删掉【所有】满足 installer's 标记谓词的注释行，返回删了几行。

    ★ ★★ 纪律（★ 本轮第 4 处变异台自身问题）：
    ★ ★★ 「变异必须改【全部】出现处」—— ★★ 而这里的「全部出现处」
    ★ ★★ **不是**「同一字符串的全部字节位置」，而是★ 【判据实际读到的
    ★ ★★ 那一面的全部等价形态】。
    ★ ★★ 898 里有**两种**拼写带 token：
    ★ ★★   首行  `-- dbinit:no-transaction —— 本文件含 …`
    ★ ★★   第139行 `-- ★ ★ dbinit:no-transaction 标记 ⇒ …`
    ★ ★★ ⇒ ★ 首版 M04 只替换首行那种拼写 ⇒ ⇒ installer's 谓词仍为 true
    ★ ★★ ⇒ ⇒ **变异是惰性的** ⇒ ⇒ 门绿是【对的】⇒ ⇒ 首版 M04 的期望
    ★ ★★ 「期望红」本身就是错的。
    """
    tok = installer_marker_token()
    s = read(path)
    kept, removed = [], 0
    for line in s.split("\n"):
        t = line.strip()
        if t.startswith("--") and tok in t:
            removed += 1
            continue
        kept.append(line)
    if removed:
        write(path, "\n".join(kept))
    return removed


def m04_drop_no_transaction_marker():
    """★ 删掉【全部】带标记的注释行（canonical + embeddata，898 + down）
    ⇒ ⇒ installer's 谓词变 false ⇒ ⇒ 判据应红。"""
    total = 0
    for p in (MIG898, EMBED898, DOWN898, EMBED898D):
        total += strip_marker_lines(p)
    return "（删掉了 %d 行带标记的注释）" % total


def m05_targeted_arbiter_in_899():
    """★ 把 899 的裸 ON CONFLICT 换成带仲裁目标 ⇒ 判据应红。"""
    n = replace_in_files([MIG899, EMBED899],
                         "ON CONFLICT DO NOTHING",
                         "ON CONFLICT (id, partition_date) DO NOTHING")
    return "（改了 %d 处）" % n


def m06_drop_reconciled_from_899():
    """★ 从 899 的函数体里删掉 reconciled CTE ⇒ 逐字判据 + 形态判据应红。"""
    body = func_body(MIG899)
    # 删掉 reconciled AS (...) 整块
    pat = re.compile(r"\n\s*reconciled AS \(\n.*?\n\s*\),\n", re.S)
    new_body, n = pat.subn("\n", body)
    if n != 1:
        raise AssertionError("★ reconciled 块匹配数 %d（期望 1）" % n)
    raw = read(MIG899)
    write(MIG899, raw.replace(body, new_body))
    s2 = read(MIG899)
    write(EMBED899, s2)
    return "（改了 1 处）"


def m07_go_ensure_body_drift():
    """★ 只改 Go ensure 那一份 ⇒ ★ 逐字判据应红（★ 字节门也一起红）。"""
    body = go_ensure_body()
    new_body = body.replace("cutoff_ts := now() - retention_window;",
                             "cutoff_ts := now() - retention_window; -- drift")
    if new_body == body:
        raise AssertionError("★ Go ensure 体里没找到目标行")
    raw = read(DBGO)
    write(DBGO, raw.replace(body, new_body))
    return "（改了 1 处）"


def m08_drop_shape_guard():
    """★ 从 899 的函数体里删掉形状守卫 ⇒ 判据应红。"""
    body = func_body(MIG899)
    pat = re.compile(r"\n\s*IF v_parent_shape IS DISTINCT FROM v_hot_shape THEN\n.*?\n\s*END IF;\n", re.S)
    new_body, n = pat.subn("\n", body)
    if n != 1:
        raise AssertionError("★ 形状守卫块匹配数 %d（期望 1）" % n)
    raw = read(MIG899)
    write(MIG899, raw.replace(body, new_body))
    write(EMBED899, read(MIG899))
    return "（改了 1 处）"


def m09_swap_try_lock_to_blocking_lock():
    """★ 把 try 锁换成阻塞锁 ⇒ 判据应红。"""
    n = replace_in_files([MIG899, EMBED899],
                         "pg_try_advisory_xact_lock", "pg_advisory_xact_lock")
    return "（改了 %d 处）" % n


def m10_break_signature():
    """★ 改签名（batch_size → limit_size）⇒ 判据应红。"""
    n = replace_in_files([MIG899, EMBED899],
                         "batch_size integer DEFAULT 5000",
                         "limit_size integer DEFAULT 5000")
    return "（改了 %d 处）" % n


def m11_drop_postcondition_reverse_assert():
    """★ 去掉后置条件的【反向】断言 ⇒ 判据应红。"""
    n = replace_in_files([MIG899, EMBED899],
                         "installed body still carries the targeted arbiter",
                         "installed body shape note")
    return "（改了 %d 处）" % n


def m12_down_no_longer_restores_targeted():
    """★ 让 down 不再回到带仲裁目标形态 ⇒ 判据应红。"""
    n = replace_in_files([DOWN899, EMBED899D],
                         "ON CONFLICT (id, partition_date) DO NOTHING",
                         "ON CONFLICT DO NOTHING")
    return "（改了 %d 处）" % n


def m13_drop_channel_899_entry():
    """★ 从通道 files=() 里删掉 899 ⇒ 判据应红。"""
    n = replace_in_files([CHANNEL],
                         '"$ROOT_DIR/sql/migrations/startup/899_session_bodies_promote_body_sink.sql"',
                         '"$ROOT_DIR/sql/migrations/startup/899_removed.sql"')
    return "（改了 %d 处）" % n


def m14_break_chain_tail():
    """★ 把链尾从 899 换成 708（链序错）⇒ 判据应红。"""
    n = replace_in_files([CHANNEL],
                         "'promote_session_bodies_hot_to_partition|708_session_bodies_s1a.sql|899_session_bodies_promote_body_sink.sql|'",
                         "'promote_session_bodies_hot_to_partition|899_session_bodies_promote_body_sink.sql|708_session_bodies_s1a.sql|'")
    return "（改了 %d 处）" % n


def m15_drop_runner_registration():
    """★ 从 StartupFiles 里删掉 899 ⇒ 判据应红。"""
    n = replace_in_files([RUNNER],
                         '"899_session_bodies_promote_body_sink.sql"',
                         '"899_session_bodies_promote_body_sink.sql.disabled"')
    return "（改了 %d 处）" % n


def m16_drop_embed_registration():
    """★ 从 main.go 的 map 里删掉 898 ⇒ 判据应红。"""
    n = replace_in_files([MAIN],
                         '"startup/898_session_bodies_hot_ts_index.sql":',
                         '"startup/898_session_bodies_hot_ts_index.sql.disabled":')
    return "（改了 %d 处）" % n


def m17_desync_embed_copy():
    """★ 只改 embeddata 副本（canonical 不动）⇒ ★ 字节门应红。"""
    s = read(EMBED898)
    write(EMBED898, s + "\n-- drift\n")
    return "（改了 1 处）"


def m18_remove_ledger_selfregistration():
    """★ 去掉 899 的台账自登记 ⇒ 判据应红。"""
    n = replace_in_files([MIG899, EMBED899],
                         "INSERT INTO public.schema_migrations (version, description)",
                         "-- INSERT INTO public.schema_migrations (version, description)")
    return "（改了 %d 处）" % n


def m19_rename_index_to_suffix_variant():
    """★ 把索引名改成后缀版（_x）⇒ ★ 判据用带边界 token ⇒ 仍应红。"""
    n = replace_in_files([MIG898, EMBED898],
                         "idx_session_bodies_hot_ts",
                         "idx_session_bodies_hot_ts_x")
    return "（改了 %d 处）" % n


def m20_remove_runbook_section():
    """★ 删掉 runbook 里的 §143.97 标记 ⇒ 文档判据应红。"""
    s = read(RUNBOOK)
    write(RUNBOOK, s.replace("§143.97", "§143.99"))
    return "（改了 1 处）"


# ═══════════════════════════════════════════════════════════════════════
# 效果探针（★ 每臂自带）
# ═══════════════════════════════════════════════════════════════════════

def probe_needle_gone_in_exec(path, needle):
    """★ 探针（方向：needle 必须【归零】）—— 用于「删掉某物」的变异。"""
    def _p():
        if needle in executable_surface(path):
            return False, "needle `%s` 在可执行面里仍然存在" % needle
        return True, ""
    return _p


def probe_needle_present_in_exec(path, needle):
    """★ 探针（方向：needle 必须【出现】）—— 用于「引入某物」的变异。

    ★ ★ 本轮 M03 连栽两次的根因（★ 与 §143.96 记的「探针作用域」同族的第三种形态）：
    ★ ★   ① 首跑：needle 比实际写入的形态**短** ⇒ 恒成立 ⇒ 崩
    ★ ★   ② 次跑：needle 逐字对齐了 ⇒ ★ 但用了「归零」方向的探针
    ★ ★      ⇒ ★ 而 M03 是**引入**needle 的变异 ⇒ ⇒ needle 当然还在
    ★ ★      ⇒ ⇒ 探针恒失败 ⇒ 崩
    ★ ★ ⇒ ⇒ **探针的方向必须与变异的方向一致**：
    ★ ★      「删掉 X」的变异 ⇒ 探针查 X 归零；
    ★ ★      「引入 X」的变异 ⇒ 探针查 X 出现。
    """
    def _p():
        if needle not in executable_surface(path):
            return False, "needle `%s` 在可执行面里没有出现（★ 变异没生效？★ 探针方向反了？）" % needle
        return True, ""
    return _p


def probe_body_changed(path):
    before = backups[path]
    def _p():
        with open(path, "rb") as f:
            now = f.read()
        if now == before:
            return False, "函数体没变"
        return True, ""
    return _p


def probe_no_marker_left(paths):
    """★ 探针（方向：installer's 标记谓词必须【全部变 false】）。

    ★ ★ 这条探针是 M04 / M23 的**效果**判据：★ 只有当这些文件里
    ★ ★ 【再没有任何一行】满足 installer's 谓词时，变异才真的
    ★ ★ 「让 installer 走事务通道」⇒ ⇒ 否则就是【惰性变异】。
    """
    def _p():
        for p in paths:
            if installer_marker_predicate(read(p)):
                return False, ("%s 仍满足 installer's 标记谓词 ⇒ ★ "
                               "这次变异是【惰性的】（★ 只删了一种拼写？）" % p)
        return True, ""
    return _p


def probe_installer_predicate_false(path):
    """★ 探针：installer's 谓词必须对这一个文件变 false
    （★ 用于「改 installer's token」那类变异）。"""
    def _p():
        if installer_marker_predicate(read(path)):
            return False, ("%s 仍被 installer's 谓词判为 true ⇒ ★ "
                           "变异没生效？（★ token 没改到？）" % path)
        return True, ""
    return _p


def m23_drop_marker_from_embeddata_only():
    """★ 只删 embeddata 副本的标记（★ canonical 保留）⇒ ⇒
    ★ embeddata 才是 installer 真正执行的那一份 ⇒ ⇒ 判据应红。"""
    total = 0
    for p in (EMBED898, EMBED898D):
        total += strip_marker_lines(p)
    return "（删掉了 %d 行带标记的注释）" % total


def m24_rename_installer_marker_token():
    """★ 把 installer's 标记 token 改名 ⇒ ⇒ 898 的标记不再被识别
    ⇒ ⇒ requiresNoTransaction(898) 变 false ⇒ ⇒ 判据应红。"""
    n = replace_in_files([RUNNER],
                         'const noTransactionMarker = "dbinit:no-transaction"',
                         'const noTransactionMarker = "dbinit:no-tx"')
    return "（改了 %d 处）" % n


def m25_remove_lessons_292_marker():
    """★ 删掉 LESSONS 里的 §292 标记 ⇒ ⇒ 文档判据应红。"""
    n = replace_in_files([LESSONS], "§292", "§999")
    return "（改了 %d 处）" % n


def m26_remove_agents_r142_marker():
    """★ 删掉 AGENTS 里的 R142 标记 ⇒ ⇒ 文档判据应红。"""
    n = replace_in_files([AGENTS], "R142", "R999")
    return "（改了 %d 处）" % n


def main():
    print("=" * 78)
    print("§143.97 变异台：验证「门绿 ≠ 门有牙」")
    print("=" * 78)

    # ★ 纪律 ⑧：跑之前工作树必须是干净的（或至少已提交）⇒ 否则还原校验不可信。
    st = git_status()
    if st.strip():
        print("⚠️  工作树当前有未提交改动：")
        for line in st.strip().split("\n")[:20]:
            print("   ", line)
        print("★ 建议先 commit 或 stash 再跑本变异台（否则还原校验会与既有改动混淆）。")
        print("★ 继续。")

    # ── 期望红的臂（证明门有牙）────────────────────────────────────────
    arm("M01 ts 索引改成单列", False, m01_ts_index_to_single_column,
        probe_needle_gone_in_exec(MIG898, "btree (ts, id, partition_date)"))
    arm("M02 去掉 CONCURRENTLY", False, m02_drop_concurrently,
        probe_needle_gone_in_exec(MIG898, "CREATE INDEX CONCURRENTLY"))
    arm("M03 放回 now() 谓词（614 当年的失败形态）", False, m03_reintroduce_now_predicate,
        probe_needle_present_in_exec(MIG898, M03_NEEDLE))
    arm("M04 去掉 no-transaction 标记（★ 删【全部】拼写）", False, m04_drop_no_transaction_marker,
        probe_no_marker_left([MIG898, EMBED898, DOWN898, EMBED898D]))
    arm("M05 899 换成带仲裁目标", False, m05_targeted_arbiter_in_899,
        probe_needle_gone_in_exec(MIG899, "ON CONFLICT DO NOTHING"))
    arm("M06 899 删掉 reconciled CTE", False, m06_drop_reconciled_from_899,
        probe_body_changed(MIG899))
    arm("M07 Go ensure 体漂移（只改一份）", False, m07_go_ensure_body_drift,
        probe_body_changed(DBGO))
    arm("M08 899 删掉形状守卫", False, m08_drop_shape_guard,
        probe_body_changed(MIG899))
    arm("M09 try 锁换成阻塞锁", False, m09_swap_try_lock_to_blocking_lock,
        probe_needle_gone_in_exec(MIG899, "pg_try_advisory_xact_lock"))
    arm("M10 改函数签名", False, m10_break_signature,
        probe_needle_gone_in_exec(MIG899, "batch_size integer DEFAULT 5000"))
    arm("M11 去掉后置条件的反向断言", False, m11_drop_postcondition_reverse_assert,
        probe_needle_gone_in_exec(MIG899, "installed body still carries the targeted arbiter"))
    arm("M12 down 不再回到带仲裁目标形态", False, m12_down_no_longer_restores_targeted,
        probe_needle_gone_in_exec(DOWN899, "ON CONFLICT (id, partition_date) DO NOTHING"))
    arm("M13 通道 files=() 删掉 899", False, m13_drop_channel_899_entry,
        probe_needle_gone_in_exec(CHANNEL, "startup/899_session_bodies_promote_body_sink.sql\""))
    arm("M14 链序倒置（899 不在链尾）", False, m14_break_chain_tail,
        probe_needle_gone_in_exec(CHANNEL,
                                  "promote_session_bodies_hot_to_partition|708_session_bodies_s1a.sql|899_session_bodies_promote_body_sink.sql|"))
    arm("M15 StartupFiles 删掉 899", False, m15_drop_runner_registration,
        probe_needle_gone_in_exec(RUNNER, "\"899_session_bodies_promote_body_sink.sql\""))
    arm("M16 main.go 的 map 删掉 898", False, m16_drop_embed_registration,
        probe_needle_gone_in_exec(MAIN, "\"startup/898_session_bodies_hot_ts_index.sql\":"))
    arm("M17 embeddata 副本与 canonical 漂移", False, m17_desync_embed_copy)
    arm("M18 去掉 899 的台账自登记", False, m18_remove_ledger_selfregistration,
        probe_needle_gone_in_exec(MIG899, "INSERT INTO public.schema_migrations"))
    arm("M19 索引名改成后缀版（_x）", False, m19_rename_index_to_suffix_variant,
        probe_needle_gone_in_exec(MIG898, "'idx_session_bodies_hot_ts'"))
    arm("M20 删掉 runbook 的 §143.97 标记", False, m20_remove_runbook_section)

    # ── 期望绿的臂（★ 变异必须是【合法的】）────────────────────────────
    def m21_legal_comment():
        """★ 合法变异：在 898 的注释里加一段无害说明 ⇒ 门应当仍绿。

        ★ 这一臂证明门不是「见字就红」⇒ ⇒ 它查的是【可执行面】。
        """
        s = read(MIG898)
        write(MIG898, s.replace(
            "-- ── 第 1 段：建索引",
            "-- 合法注释：本迁移只为 session_bodies_hot 补 ts 首列索引。\n"
            "-- ── 第 1 段：建索引"))
        write(EMBED898, read(MIG898))
        return "（改了 2 处）"

    def m22_legal_chain_comment():
        """★ 合法变异：在通道链登记旁加注释 ⇒ 门应当仍绿。"""
        s = read(CHANNEL)
        write(CHANNEL, s.replace(
            "'promote_session_bodies_hot_to_partition|708_session_bodies_s1a.sql|899_session_bodies_promote_body_sink.sql|'",
            "# 合法注释：899 是链尾。\n"
            "'promote_session_bodies_hot_to_partition|708_session_bodies_s1a.sql|899_session_bodies_promote_body_sink.sql|'"))
        return "（改了 1 处）"

    arm("M21 合法：在 898 注释里加说明（期望仍绿）", True, m21_legal_comment)
    arm("M22 合法：在通道链登记旁加注释（期望仍绿）", True, m22_legal_chain_comment)

    # ★ ★ 本轮新增的两臂（★ 它们证明「embeddata 侧」与「真函数」都被守着）：
    arm("M23 只删 embeddata 副本的标记（canonical 保留）", False,
        m23_drop_marker_from_embeddata_only,
        probe_no_marker_left([EMBED898, EMBED898D]))
    arm("M24 改 installer's 标记 token（真函数口径漂移）", False,
        m24_rename_installer_marker_token,
        probe_installer_predicate_false(EMBED898))
    arm("M25 删掉 LESSONS 的 §292 标记", False, m25_remove_lessons_292_marker)
    arm("M26 删掉 AGENTS 的 R142 标记", False, m26_remove_agents_r142_marker)

    print("-" * 78)
    ok = 0
    for name, passed, msg in RESULTS:
        mark = "✅" if passed else "❌"
        print("%s %-46s %s" % (mark, name, msg if not passed else ""))
        if passed:
            ok += 1
    print("-" * 78)
    print("结果：%d/%d 通过" % (ok, len(RESULTS)))
    print("环境指纹：")
    st = git_status()
    print("  git status 行数 =", len([l for l in st.split("\n") if l.strip()]))
    return 0 if ok == len(RESULTS) else 1


if __name__ == "__main__":
    sys.exit(main())
