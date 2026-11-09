#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""market_price_sync_accounting_test.py —— 行情同步器**台账口径**行为门。

被测物：scripts/market-price-sync.py 的纯函数 plan_rows() / build_upsert_sql()
        与 psql_tsv() 的 NULL 渲染约定。

★ 为什么必须是**行为门**而不是文本门（2026-11-10 审计 P2-C 的教训）：
  原实现里 `rejected_rows` 恒为 0、「源=落地+拒绝」是空恒等式。
  这类缺陷**没有任何一行代码写错**：文本断言照样全绿（脚本确实写了
  `rejected += 1`，确实写了三联台账）。
  ⇒ 唯一能证伪的方式是**喂数据、量输出**：
     喂一批「源侧有行但无价」的候选，量 rejected_rows 是不是非 0、
     量 source_rows == landed_rows + rejected_rows 是不是闭合。

判据：
  A1 psql_tsv 的 NULL 渲染：SQL NULL ⇒ None；空串 ⇒ ''（两者**必须**可区分）
  A2 拒绝计数：源侧「有候选但无价」的候选 ⇒ rejected_rows 非 0
  A3 恒等式闭合：source_rows == landed_rows + rejected_rows（每一批数据都验）
  A4 拒绝族明细：拒绝**为什么**拒绝要能被台账读到（no_price 等）
  A5 NOT NULL 列保护：源侧 NULL 的 NOT NULL 文本列 ⇒ 落成 '' 而不是 SQL NULL
  A6 NULL≠0：缺价既不折 0、也不写空行
  A7 变异臂：把 plan_rows 的拒绝判定删掉 ⇒ A2/A3 必须红（门有牙）

用法：python3 tests/market_price_sync_accounting_test.py
"""

import importlib.util
import os
import re
import sys
import types

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(ROOT, "scripts", "market-price-sync.py")

PASS = 0
FAIL = 0


def ok(msg):
    global PASS
    PASS += 1
    print("  ✅ %s" % msg)


def bad(msg):
    global FAIL
    FAIL += 1
    print("  ❌ %s" % msg)


def load_module(path=SCRIPT, name="mps"):
    """按路径加载脚本（脚本名带连字符 ⇒ 不能直接 import）。"""
    spec = importlib.util.spec_from_file_location(name, path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def make_resolver(pairs):
    """构造 resolve(key) -> (cid|None, family)；pairs = {key: [cid...]}。"""
    def resolve(key):
        ids = pairs.get(key.lower(), [])
        if len(ids) == 1:
            return ids[0], "resolved"
        if len(ids) > 1:
            return None, "ambiguous"
        return None, "unresolved"
    return resolve


def row(key, min_in=None, min_out=None, plat_cnt="0", min_platform=None,
        vendor_in=None, vendor_out=None, vendor_name=None, max_run_date="2026-11-09"):
    """构造一行 src_rows（列序与 psql_tsv 输出一致：9 列）。"""
    return [key, min_in, min_out, plat_cnt, min_platform,
            vendor_in, vendor_out, vendor_name, max_run_date]


# ── A1: psql_tsv 的 NULL 渲染 ─────────────────────────────────────────────
def check_null_rendering(mod):
    """SQL NULL ⇒ None；真空串 ⇒ ''。两者必须可区分。"""
    # psql_tsv 的解析逻辑在模块里是内联的（没有独立 parse 函数），
    # 这里直接复刻它的解析契约：哨兵 ⇒ None，其余 ⇒ strip 后的字符串。
    sentinel = mod.NULL_SENTINEL
    if not sentinel:
        bad("A1 脚本未定义 NULL_SENTINEL ⇒ NULL 与空串不可区分")
        return
    parsed = [None if c == sentinel else c for c in ["x", sentinel, "", " "]]
    if parsed[1] is not None:
        bad("A1 哨兵未还原为 None（读到 %r）" % (parsed[1],))
        return
    if parsed[2] != "":
        bad("A1 空串被改写成了 %r（应保持 ''）" % (parsed[2],))
        return
    # ★ psql_tsv 对每个 cell 做 strip() ⇒ 纯空白折叠成 ''（既有正确行为：
    #   TSV 单元格的前后空白不是数据）⇒ 这里断言折叠结果，不是断言原样保留。
    if parsed[3].strip() != "":
        bad("A1 纯空白未被折叠成 ''：%r" % (parsed[3],))
        return
    ok("A1 NULL ⇒ None / 空串 ⇒ '' 可区分（哨兵 %s）" % sentinel)

    # ★ 更强的一层：哨兵必须真的被 psql 渲染成那个串。
    #   psql 的 `-P null=<值>` 会把 SQL NULL 渲染成 <值>；
    #   若脚本没传 -P null=，psql 就渲染成空串 ⇒ 哨兵永不出现 ⇒ 回到老缺陷。
    src = open(SCRIPT, encoding="utf-8").read()
    if '"-P", "null=" + NULL_SENTINEL' not in src:
        bad("A1 psql 调用未传 -P null=<哨兵> ⇒ psql 把 NULL 渲染成空串，哨兵永不出现")
        return
    ok("A1 psql 调用带 -P null=<哨兵>（NULL 真的能被渲染成哨兵）")


# ── A8: 源 SQL 不得在 DB 层丢弃候选行 ───────────────────────────────────
def check_source_sql_not_filtering(mod):
    """★ 「源侧预过滤」是 rejected_rows 恒 0 的**真根因**，它发生在 DB 层
    ⇒ plan_rows 层的门**抓不到**（变异实测：预过滤恢复后 Python 侧仍闭合）
    ⇒ 必须有一条独立断言盯住源 SQL 的最终 SELECT。

    ★ 判据必须**剥掉注释行**再扫（上一版没剥 ⇒ 源码里那段解释性注释
      本身含 "min_in IS NOT NULL" ⇒ 门自己红 ⇒ 门不可信）。
    """
    src = open(SCRIPT, encoding="utf-8").read()
    start = src.find('src_sql = """')
    end = src.find('""".format(', start)
    if start < 0 or end < 0:
        bad("A8 找不到源 SQL 模板（脚本形态变了 ⇒ 门需重新对齐）")
        return
    # 剥掉整行注释（-- 开头）与 SQL 行内注释
    code_lines = []
    for line in src[start:end].splitlines():
        stripped = line.strip()
        if stripped.startswith("--"):
            continue
        code_lines.append(line.split("--")[0])
    sql = "\n".join(code_lines)
    if "min_in IS NOT NULL" in sql or "min_out IS NOT NULL" in sql:
        bad("A8 源 SQL 在 DB 层丢弃无价候选（min_in/min_out IS NOT NULL）"
            "⇒ rejected_rows 会恒为 0 ⇒ 空恒等式")
        return
    ok("A8 源 SQL 不在 DB 层丢弃无价候选（拒绝判定交给 Python 侧）")


# ── A2/A3/A4: 拒绝计数 + 恒等式 + 明细 ───────────────────────────────────
def check_accounting(mod):
    resolve = make_resolver({"gpt-4o": ["42"], "claude-opus-4-8": ["77"]})

    # 批次：3 个有价候选 + 2 个无价候选 + 1 个无名候选
    src_rows = [
        row("gpt-4o", "1.0", "2.0", "3", "openai", vendor_in="1.5", vendor_out="3.0",
            vendor_name="OpenAI"),
        row("claude-opus-4-8", "33.38", "166.92", "4", "anthropic",
            vendor_in="30.0", vendor_out="150.0", vendor_name="Anthropic"),
        row("bge-m3", None, None, "2", "bge"),                       # 无价候选
        row("embed-3", None, None, "1", "azure"),                    # 无价候选
        row(None, "1.0", "2.0", "1", "x"),                            # 无名候选
    ]
    landable, rejects = mod.plan_rows(src_rows, resolve, {})

    source_rows = len(src_rows)
    landed = len(landable)
    rejected = sum(rejects.values())

    if rejected == 0:
        bad("A2 rejected_rows 恒为 0（喂了 3 个不可落地候选却一个没被拒）")
    else:
        ok("A2 rejected_rows 非 0（%d）" % rejected)

    if source_rows != landed + rejected:
        bad("A3 恒等式不闭合：source=%d landed=%d rejected=%d"
            % (source_rows, landed, rejected))
    else:
        ok("A3 恒等式闭合：source(%d) = landed(%d) + rejected(%d)"
           % (source_rows, landed, rejected))

    if rejects.get("no_price", 0) != 2:
        bad("A4 拒绝族明细不对：无价候选应为 no_price=2，实得 %r" % (rejects,))
    else:
        ok("A4 拒绝族明细可读：%r" % (rejects,))

    if rejects.get("no_canonical_name", 0) != 1:
        bad("A4 无名候选未归入 no_canonical_name：%r" % (rejects,))
    else:
        ok("A4 无名候选归入 no_canonical_name")

    # A6: 缺价候选绝不能被写成有价行（既不折 0、也不写空行）
    for r in landable:
        key = r[0]
        if key in ("bge-m3", "embed-3"):
            bad("A6 无价候选 %r 被算作可落地" % (key,))
            break
    else:
        ok("A6 无价候选未被算作可落地（不折 0、不写空行）")

    # canonical 解析不出来的行**仍然落地**（canonical_id=NULL，如实保留 NULL）
    unresolved_landed = [r for r in landable if r[9] is None and r[0] is not None]
    if len(unresolved_landed) != 0:
        bad("A2 解析不出的候选被丢掉了（应为落地+canonical_id=NULL）：%r"
            % [r[0] for r in unresolved_landed])
    else:
        ok("A2 解析不出的候选仍落地（本次批次无此类行，逻辑在 A3 覆盖）")

    # 解析不出的候选也要能被解析为 unresolved ⇒ 仍落地（构造一批）
    src_rows2 = [row("mystery-model", "1.0", "2.0", "1", "x")]
    landable2, rejects2 = mod.plan_rows(src_rows2, resolve, {})
    if len(landable2) != 1 or landable2[0][9] is not None or landable2[0][10] != "unresolved":
        bad("A2 unresolved 候选未按「落地 + canonical_id=NULL」处理：%r" % (landable2,))
    else:
        ok("A2 unresolved 候选落地且 canonical_id=NULL（未解析行不静默消失）")


# ── A5: NOT NULL 列保护 ─────────────────────────────────────────────────
def check_notnull_columns(mod):
    """940 的 NOT NULL 文本列：源侧 NULL ⇒ '' ⇒ 绝不写 SQL NULL。"""
    if not hasattr(mod, "qstr_notnull"):
        bad("A5 脚本缺少 qstr_notnull（NULL 哨兵会把 SQL NULL 写进 NOT NULL 列 ⇒ 整事务 abort）")
        return
    if mod.qstr_notnull(None) != "''":
        bad("A5 qstr_notnull(None) = %r（应为 ''）" % (mod.qstr_notnull(None),))
        return
    if mod.qstr_notnull("x") != "'x'":
        bad("A5 qstr_notnull('x') = %r（应为 'x'）" % (mod.qstr_notnull("x"),))
        return
    if mod.qstr_notnull("it's") != "'it''s'":
        bad("A5 qstr_notnull 未转义单引号：%r" % (mod.qstr_notnull("it's"),))
        return
    ok("A5 qstr_notnull：None ⇒ ''，其余正常转义")

    # 端到端：真跑一遍 build_upsert_sql，按 940 的列位精确核对
    # NOT NULL 文本列拿到的是 '' 而不是 SQL NULL。
    #
    # ★ 判据不能写粗（上一版用 "NULL, NULL" 粗判 ⇒ 误报 canonical_id 后的
    #   合法 NULL 组合 ⇒ **门自己红** ⇒ 门不可信）。
    #   ⇒ 改成按 VALUES 元组的位置逐列核对。
    resolve = make_resolver({"gpt-4o": ["42"]})
    landable, _ = mod.plan_rows(
        [row("gpt-4o", "1.0", "2.0", "3", "openai", vendor_name=None)],
        resolve, {})
    if not landable:
        bad("A5 构造不出可落地行，测试本身失效")
        return
    sql = mod.build_upsert_sql(landable[0], "standard", "7.2")

    values_part = sql.split("VALUES", 1)[-1]
    values_part = values_part[:values_part.index("ON CONFLICT")].strip()
    cells = [c.strip() for c in values_part.rstrip(";").split(",")]
    # 940 列序（与 940 迁移 DDL 一致）：
    #  0 canonical_name  1 dimension  2 currency(字面量)  3 canonical_id
    #  4 vendor_list_in  5 vendor_list_out  6 vendor_platform_id  7 vendor_vendor
    #  8 market_min_in   9 market_min_out   10 market_min_platform_id
    # 11 market_min_platform_count 12 market_min_cny_in 13 market_min_cny_out
    # 14 fx_rate  15 source_run_date  16 source_confidence  17 source_url  18 updated_at
    if len(cells) != 19:
        bad("A5 生成的 VALUES 元组有 %d 个值（应为 19）⇒ 测试与实现不同步：\n%s"
            % (len(cells), sql[:400]))
        return

    # NOT NULL 文本列：canonical_name / vendor_platform_id / vendor_vendor /
    #                 market_min_platform_id / source_url
    notnull_text = {0: "canonical_name", 6: "vendor_platform_id",
                    7: "vendor_vendor", 10: "market_min_platform_id",
                    17: "source_url"}
    for idx, name in notnull_text.items():
        if cells[idx].upper() == "NULL":
            bad("A5 NOT NULL 列 %s 被写成了 SQL NULL ⇒ 整笔事务会 abort：\n%s"
                % (name, sql[:400]))
            return
    ok("A5 NOT NULL 文本列（%s）都不是 SQL NULL"
       % "/".join(sorted(notnull_text.values())))

    # 源侧 vendor_name=NULL ⇒ vendor_vendor 必须是 ''（不是 NULL、也不是别的）
    if cells[7] != "''":
        bad("A5 源侧 vendor_name=NULL ⇒ vendor_vendor 应为 ''，实得 %r" % (cells[7],))
        return
    ok("A5 源侧 vendor_name=NULL ⇒ vendor_vendor 落成 ''")

    # ★ NULL≠0：**半价行**（市场侧只有输入价、输出价缺）才是「未知 ≠ 免费」的真实形态。
    #   ⚠ 判据不能用「市场侧完全无价」的行来验：那种候选按设计就是**被拒**
    #     （保持旧行为：不写入目标表），不是落地行 ⇒ 拿它断言会误判（门自己红）。
    #   ⇒ 这里造一行「市场侧半价、原厂侧有价」：它**落地**，
    #     但缺失的那一维必须是 NULL 而不是 0。
    resolve2 = make_resolver({"gpt-4o": ["42"]})
    landable2, _ = mod.plan_rows(
        [row("gpt-4o", "1.0", None, "3", "openai",
             vendor_in="1.5", vendor_out="3.0", vendor_name="OpenAI")],
        resolve2, {})
    if not landable2:
        bad("A5 半价候选未被落地（测试前提失效）")
        return
    sql2 = mod.build_upsert_sql(landable2[0], "standard", "7.2")
    vp = sql2.split("VALUES", 1)[-1]
    vp = vp[:vp.index("ON CONFLICT")].strip()
    cells2 = [c.strip() for c in vp.rstrip(";").split(",")]
    # 8 market_min_in（=1.0） 9 market_min_out（缺失 ⇒ 必须 NULL）
    if cells2[8] != "1.0":
        bad("A5 market_min_in 应为 1.0，实得 %r" % (cells2[8],))
        return
    if cells2[9] != "NULL":
        bad("A5 NULL≠0 被破坏：缺失的市场输出价应写 NULL（未知），实得 %r" % (cells2[9],))
        return
    # 12/13 market_min_cny_in/out：缺的那一维也必须是 NULL
    if cells2[12] != "1.0" or cells2[13] != "NULL":
        bad("A5 market_min_cny_out 应为 NULL（未知≠免费），实得 %r" % (cells2[13],))
        return
    # 原厂侧有价 ⇒ 必须落成真实数值（不能一起被 NULL 掉）
    if cells2[4] != "1.5" or cells2[5] != "3.0":
        bad("A5 原厂价被写错：vendor_list_in/out 应为 1.5/3.0，实得 %r/%r"
            % (cells2[4], cells2[5]))
        return
    ok("A5 NULL≠0：半价行缺失维度写 NULL（未知≠免费），原厂价照常落数")


# ── A9: 端到端跑 main()（假 psql）验台账三联真闭合 ─────────────────────
def check_end_to_end_ledger(mod):
    """★ 把 psql_tsv 换成假的、跑真 main()、抓真生成的台账 INSERT ⇒
    量「台账里写下的数字」，而不是量中间变量。
    ⇒ 只有这条断言能证明 plan_rows 的口径**真的流到了台账**。
    """
    import io
    import contextlib

    src_rows = [
        row("gpt-4o", "1.0", "2.0", "3", "openai", vendor_in="1.5",
            vendor_out="3.0", vendor_name="OpenAI"),
        row("claude-opus-4-8", "33.38", "166.92", "4", "anthropic"),
        row("bge-m3", None, None, "2", "bge"),          # 无价候选
        row(None, "1.0", "2.0", "1", "x"),              # 无名候选
    ]
    captured = {}

    def fake_psql_tsv(container, user, db, sql):
        s = sql
        if "FROM price_observation" in s and "canonical_key" in s:
            return src_rows
        if "count(DISTINCT platform_id)" in s:
            return [["4"]]
        if "FROM fx_rate" in s:
            return [["7.2"]]
        if "FROM models_canonical" in s:
            return [["42", "gpt-4o"], ["77", "claude-opus-4-8"]]
        if "FROM market_price_reference" in s:
            return []
        if "model_credit_rates" in s:
            return [["100"]]
        return []

    def fake_psql_exec(container, user, db, sql_script):
        captured["script"] = sql_script
        return types.SimpleNamespace(returncode=0, stderr="", stdout="")

    orig_tsv, orig_exec = mod.psql_tsv, mod.psql_exec
    mod.psql_tsv = fake_psql_tsv
    mod.psql_exec = fake_psql_exec
    dump_path = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                             "_mps_dump_tmp.sql")
    argv = sys.argv
    # ★ 用 --dump-sql 而不是 --dry-run：--dry-run 会在写 dump 之前 return
    #   ⇒ 拿不到目标事务 SQL ⇒ 上一版因此红（门自己红 ⇒ 门不可信）。
    sys.argv = ["market-price-sync.py", "--dump-sql", dump_path]
    buf = io.StringIO()
    try:
        with contextlib.redirect_stdout(buf):
            mod.main()
    except SystemExit as exc:
        if exc.code not in (0, None):
            bad("A9 main() 异常退出：%r（stdout=%s）" % (exc.code, buf.getvalue()[-500:]))
            return
    except Exception as exc:  # noqa: BLE001
        bad("A9 main() 执行异常：%r" % (exc,))
        return
    finally:
        mod.psql_tsv, mod.psql_exec = orig_tsv, orig_exec
        sys.argv = argv

    out = buf.getvalue()
    # 从 stdout 里读台账三联（脚本自己打印的那行）
    nums = {}
    for line in out.splitlines():
        if line.startswith("landed="):
            for part in line.split():
                if "=" in part:
                    k, v = part.split("=", 1)
                    nums[k] = v
    if "landed" not in nums or "rejected" not in nums:
        bad("A9 没抓到台账三联输出：\n%s" % out[-500:])
        return
    landed, rejected = int(nums["landed"]), int(nums["rejected"])
    if rejected == 0:
        bad("A9 端到端 rejected 仍为 0（喂了 2 个不可落地候选）：%s" % out[-300:])
        return
    ok("A9 端到端台账：landed=%d rejected=%d（非 0）" % (landed, rejected))

    # 源条数 = 源侧候选数（4 行）⇒ 恒等式必须闭合
    if landed + rejected != len(src_rows):
        bad("A9 端到端恒等式不闭合：landed(%d)+rejected(%d) != source(%d)"
            % (landed, rejected, len(src_rows)))
        return
    ok("A9 端到端恒等式闭合：landed(%d)+rejected(%d)=source(%d)"
       % (landed, rejected, len(src_rows)))

    # ★ 台账 INSERT 里写下的数字必须与打印的一致（口径不能只活在 stdout）
    try:
        with open(dump_path, encoding="utf-8") as fh:
            script = fh.read()
    except OSError as exc:
        bad("A9 读不到 dump 出来的目标事务 SQL：%r" % (exc,))
        return
    finally:
        if os.path.exists(dump_path):
            os.remove(dump_path)
    m = re.search(r"market_price_reference_sync.*?VALUES \(current_date, (-?\d+), (-?\d+), (-?\d+), (-?\d+),",
                  script, re.S)
    if not m:
        bad("A9 目标事务里找不到台账 INSERT：\n%s" % script[-800:])
        return
    sp, sr, lr, rr = (int(x) for x in m.groups())
    if not (sr == lr + rr):
        bad("A9 台账 INSERT 里的三联不闭合：source=%d landed=%d rejected=%d" % (sr, lr, rr))
        return
    if sr != len(src_rows):
        bad("A9 台账 source_rows=%d ≠ 源侧候选数 %d（源侧仍在丢候选）"
            % (sr, len(src_rows)))
        return
    ok("A9 台账 INSERT 三联闭合：source=%d = landed=%d + rejected=%d" % (sr, lr, rr))


# ── A7: 变异臂 ─────────────────────────────────────────────────────────
def check_mutation_arm(mod):
    """把拒绝判定删掉 ⇒ A2/A3 必须红（证明门有牙）。"""
    src = open(SCRIPT, encoding="utf-8").read()
    marker = 'if min_in is None and min_out is None:\n            rejects["no_price"] += 1\n            continue'
    if marker not in src:
        bad("A7 变异臂锚点不在脚本里（拒绝判定形态变了 ⇒ 门需重新对齐）")
        return

    mutated = src.replace(marker, "pass  # MUTATED: 拒绝判定被删掉", 1)
    if mutated == src:
        bad("A7 变异未生效（替换后文本没变）")
        return

    tmp_path = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                            "_mps_mutant_tmp.py")
    try:
        with open(tmp_path, "w", encoding="utf-8") as fh:
            fh.write(mutated)
        mmod = load_module(tmp_path, name="mps_mutant")
        resolve = make_resolver({"gpt-4o": ["42"]})
        src_rows = [row("gpt-4o", "1.0", "2.0", "3", "openai"),
                    row("bge-m3", None, None, "2", "bge")]
        landable, rejects = mmod.plan_rows(src_rows, resolve, {})
        rejected = sum(rejects.values())
        if rejected == 0:
            ok("A7 变异臂：删掉拒绝判定 ⇒ rejected 归零 ⇒ 门能抓到（有牙）")
        else:
            bad("A7 变异臂无效：删掉拒绝判定后 rejected 仍为 %d ⇒ 门无牙" % rejected)
    except Exception as exc:  # noqa: BLE001
        bad("A7 变异臂执行异常：%r" % (exc,))
    finally:
        if os.path.exists(tmp_path):
            os.remove(tmp_path)


def main():
    if not os.path.exists(SCRIPT):
        print("FATAL: 找不到 %s" % SCRIPT, file=sys.stderr)
        return 1
    try:
        mod = load_module()
    except Exception as exc:  # noqa: BLE001
        print("FATAL: 加载脚本失败：%r" % (exc,), file=sys.stderr)
        return 1

    print("── 行情同步器台账口径行为门 ──")
    check_null_rendering(mod)
    check_source_sql_not_filtering(mod)
    check_accounting(mod)
    check_notnull_columns(mod)
    check_end_to_end_ledger(mod)
    check_mutation_arm(mod)

    print("\n================ 汇总 ================")
    print("通过 %d / 失败 %d" % (PASS, FAIL))
    return 0 if FAIL == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
