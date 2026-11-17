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
  A8 源 SQL 不得在 DB 层丢弃候选行
  A9 端到端 main()（假 psql）验台账三联真闭合
  A10 A-17 双地板折算（derive_dual_floors）：原生币事实源 + 快照 fx 折算 +
      综合地板逐轴 CNY 最低 + fx 缺失 fail-closed（fx_missing 旗标，不猜）

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


def row(key, ov_in=None, ov_out=None, ov_cnt="0", ov_platform=None,
        dm_in=None, dm_out=None, dm_cnt="0", dm_platform=None,
        vd_cur=None, vd_in=None, vd_out=None, vd_cny_in=None, vd_cny_out=None,
        vd_name=None, vd_platform=None, max_run_date="2026-11-09"):
    """构造一行 src_rows（列序与 psql_tsv 输出一致：17 列，A-17 双地板）。"""
    return [key, ov_in, ov_out, ov_cnt, ov_platform,
            dm_in, dm_out, dm_cnt, dm_platform,
            vd_cur, vd_in, vd_out, vd_cny_in, vd_cny_out,
            vd_name, vd_platform, max_run_date]


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
    start = src.find('src_sql = latest_cte + """')
    # ★ 2026-11-12：format 移挂 latest_cte 定义处 ⇒ src 拼接段的终结符是裸 """
    #   （旧锚 '""".format(' 已不存在；取 start 之后第一处三引号）
    end = src.find('"""', start + len('src_sql = latest_cte + """'))
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
    if "ov_in IS NOT NULL" in sql or "dm_in IS NOT NULL" in sql or "vd_in IS NOT NULL" in sql:
        bad("A8 源 SQL 在 DB 层丢弃无价候选（A-17 双地板版同款预过滤）"
            "⇒ rejected_rows 会恒为 0 ⇒ 空恒等式")
        return
    ok("A8 源 SQL 不在 DB 层丢弃无价候选（拒绝判定交给 Python 侧）")


# ── A2/A3/A4: 拒绝计数 + 恒等式 + 明细 ───────────────────────────────────
def check_accounting(mod):
    resolve = make_resolver({"gpt-4o": ["42"], "claude-opus-4-8": ["77"]})

    # 批次：3 个有价候选 + 2 个无价候选 + 1 个无名候选
    src_rows = [
        row("gpt-4o", dm_in="1.0", dm_out="2.0", dm_cnt="3", dm_platform="openai",
            vd_cur="CNY", vd_in="1.5", vd_out="3.0", vd_name="OpenAI",
            vd_platform="openai"),
        row("claude-opus-4-8", ov_in="33.38", ov_out="166.92", ov_cnt="4",
            ov_platform="anthropic", vd_cur="USD", vd_in="30.0", vd_out="150.0",
            vd_name="Anthropic", vd_platform="anthropic"),
        row("bge-m3"),                                               # 无价候选
        row("embed-3"),                                              # 无价候选
        row(None, dm_in="1.0", dm_out="2.0", dm_cnt="1", dm_platform="x"),  # 无名候选
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
    unresolved_landed = [r for r in landable if r[17] is None and r[0] is not None]
    if len(unresolved_landed) != 0:
        bad("A2 解析不出的候选被丢掉了（应为落地+canonical_id=NULL）：%r"
            % [r[0] for r in unresolved_landed])
    else:
        ok("A2 解析不出的候选仍落地（本次批次无此类行，逻辑在 A3 覆盖）")

    # 解析不出的候选也要能被解析为 unresolved ⇒ 仍落地（构造一批）
    src_rows2 = [row("mystery-model", dm_in="1.0", dm_out="2.0", dm_cnt="1", dm_platform="x")]
    landable2, rejects2 = mod.plan_rows(src_rows2, resolve, {})
    if len(landable2) != 1 or landable2[0][17] is not None or landable2[0][18] != "unresolved":
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
        [row("gpt-4o", dm_in="1.0", dm_out="2.0", dm_cnt="3", dm_platform="openai",
             vd_cur="USD", vd_in="1.5", vd_out="3.0", vd_name="OpenAI",
             vd_platform="openai")],
        resolve, {})
    if not landable:
        bad("A5 构造不出可落地行，测试本身失效")
        return
    sql = mod.build_upsert_sql(landable[0], mod.derive_dual_floors(landable[0], "7.2"),
                               "standard", "7.2")

    values_part = sql.split("VALUES", 1)[-1]
    values_part = values_part[:values_part.index("ON CONFLICT")].strip()
    cells = [c.strip() for c in values_part.rstrip(";").split(",")]
    # 940+959 列序（36 列；959 新 16 列插在 market_min_cny_* 与 fx_rate 之间）：
    #  0 canonical_name  1 dimension  2 currency(字面量'CNY')  3 canonical_id
    #  4 vendor_list_in  5 vendor_list_out  6 vendor_platform_id  7 vendor_vendor
    #  8 market_min_in   9 market_min_out   10 market_min_platform_id
    # 11 market_min_platform_count 12 market_min_cny_in 13 market_min_cny_out
    # 14 vendor_list_currency 15 vendor_list_cny_in 16 vendor_list_cny_out
    # 17 vendor_list_usd_in 18 vendor_list_usd_out
    # 19 overseas_min_in 20 overseas_min_out 21 overseas_min_platform_id
    # 22 overseas_min_platform_count 23 overseas_min_cny_in 24 overseas_min_cny_out
    # 25 domestic_min_in 26 domestic_min_out 27 domestic_min_platform_id
    # 28 domestic_min_platform_count 29 domestic_min_usd_in 30 domestic_min_usd_out
    # 31 fx_rate 32 source_run_date 33 source_confidence 34 source_url 35 updated_at
    if len(cells) != 36:
        bad("A5 生成的 VALUES 元组有 %d 个值（应为 36）⇒ 测试与实现不同步：\n%s"
            % (len(cells), sql[:400]))
        return

    # NOT NULL 文本列：canonical_name / vendor_platform_id / vendor_vendor /
    #                 market_min_platform_id / vendor_list_currency /
    #                 overseas_min_platform_id / domestic_min_platform_id / source_url
    notnull_text = {0: "canonical_name", 6: "vendor_platform_id",
                    7: "vendor_vendor", 10: "market_min_platform_id",
                    14: "vendor_list_currency", 21: "overseas_min_platform_id",
                    27: "domestic_min_platform_id", 34: "source_url"}
    for idx, name in notnull_text.items():
        if cells[idx].upper() == "NULL":
            bad("A5 NOT NULL 列 %s 被写成了 SQL NULL ⇒ 整笔事务会 abort：\n%s"
                % (name, sql[:400]))
            return
    ok("A5 NOT NULL 文本列（%s）都不是 SQL NULL"
       % "/".join(sorted(notnull_text.values())))

    # vendor 原生币事实源（A-17 原则 1）：USD 原生价进 vendor_list_*，
    # 快照 fx 折算进 vendor_list_cny_*（1.5×7.2=10.8）、USD 列=原币值。
    if cells[4] != "1.5" or cells[5] != "3.0":
        bad("A5 vendor 原生价应为 1.5/3.0（事实源），实得 %r/%r" % (cells[4], cells[5]))
        return
    if cells[14] != "'USD'":
        bad("A5 vendor_list_currency 应为 'USD'（registry 裁定），实得 %r" % (cells[14],))
        return
    if cells[15] != "10.800000" or cells[17] != "1.500000":
        bad("A5 vendor 折算列错：cny=1.5×7.2 应为 10.800000、usd=原币 1.500000，"
            "实得 %r/%r" % (cells[15], cells[17]))
        return
    ok("A5 vendor 原生币事实源 + 快照 fx 折算列（10.800000/1.5）")

    # 综合地板（CNY 逐轴最低）：dm 1.0/2.0 原生 CNY，ov 无 ⇒ 1.0/2.0
    # （fmt6 归一 numeric(14,6) 精度）；归属 = domestic（dm_platform）。
    if cells[8] != "1.000000" or cells[9] != "2.000000" or cells[10] != "'openai'":
        bad("A5 综合地板错：应为 1.000000/2.000000@openai，实得 %r/%r/%r"
            % (cells[8], cells[9], cells[10]))
        return
    ok("A5 综合地板 = 双地板折 CNY 逐轴最低（1.000000/2.000000@openai）")

    # A-17 双地板列：海外地板全缺 ⇒ NULL；国内容生 1.0/2.0 + 折 USD 列
    # （2.0/7.2=0.277778、1.0/7.2=0.138889）。
    if cells[19] != "NULL" or cells[20] != "NULL" or cells[23] != "NULL" or cells[24] != "NULL":
        bad("A5 海外地板应为全 NULL（无海外观测），实得 %r/%r/%r/%r"
            % (cells[19], cells[20], cells[23], cells[24]))
        return
    if cells[25] != "1.0" or cells[26] != "2.0" or cells[29] != "0.138889" or cells[30] != "0.277778":
        bad("A5 国内地板原生/折算错：应为 1.0/2.0 + 折 USD 0.138889/0.277778，"
            "实得 %r/%r/%r/%r" % (cells[25], cells[26], cells[29], cells[30]))
        return
    ok("A5 双地板列：overseas=NULL（无观测）、domestic 原生+折 USD（快照 fx=7.2）")

    # ★ NULL≠0：**半价行**（市场侧只有输入价、输出价缺）才是「未知 ≠ 免费」的真实形态。
    #   ⚠ 判据不能用「市场侧完全无价」的行来验：那种候选按设计就是**被拒**
    #     （保持旧行为：不写入目标表），不是落地行 ⇒ 拿它断言会误判（门自己红）。
    #   ⇒ 这里造一行「市场侧半价、原厂侧有价」：它**落地**，
    #     但缺失的那一维必须是 NULL 而不是 0。
    resolve2 = make_resolver({"gpt-4o": ["42"]})
    landable2, _ = mod.plan_rows(
        [row("gpt-4o", dm_in="1.0", dm_cnt="3", dm_platform="openai",
             vd_cur="USD", vd_in="1.5", vd_out="3.0", vd_name="OpenAI",
             vd_platform="openai")],
        resolve2, {})
    if not landable2:
        bad("A5 半价候选未被落地（测试前提失效）")
        return
    sql2 = mod.build_upsert_sql(landable2[0], mod.derive_dual_floors(landable2[0], "7.2"),
                                "standard", "7.2")
    vp = sql2.split("VALUES", 1)[-1]
    vp = vp[:vp.index("ON CONFLICT")].strip()
    cells2 = [c.strip() for c in vp.rstrip(";").split(",")]
    # 8 market_min_in（=1.0） 9 market_min_out（缺失 ⇒ 必须 NULL）
    if cells2[8] != "1.000000":
        bad("A5 market_min_in 应为 1.000000，实得 %r" % (cells2[8],))
        return
    if cells2[9] != "NULL":
        bad("A5 NULL≠0 被破坏：缺失的市场输出价应写 NULL（未知），实得 %r" % (cells2[9],))
        return
    # 12/13 market_min_cny_in/out：缺的那一维也必须是 NULL
    if cells2[12] != "1.000000" or cells2[13] != "NULL":
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
        row("gpt-4o", dm_in="1.0", dm_out="2.0", dm_cnt="3", dm_platform="openai",
            vd_cur="CNY", vd_in="1.5", vd_out="3.0", vd_name="OpenAI",
            vd_platform="openai"),
        row("claude-opus-4-8", ov_in="33.38", ov_out="166.92", ov_cnt="4",
            ov_platform="anthropic"),
        row("bge-m3"),          # 无价候选
        row(None, dm_in="1.0", dm_out="2.0", dm_cnt="1", dm_platform="x"),  # 无名候选
    ]
    captured = {}

    def fake_psql_tsv(container, user, db, sql):
        s = sql
        if "to_regclass('public.market_platform_registry')" in s:
            return [["t"]]      # 958/959 前置门
        if "FROM public.market_platform_registry" in s:
            # boolean::text 渲染 = 'true'/'false'（裸列才是 't'/'f'）
            return [["openai", "domestic", "CNY", "false", "true"],
                    ["anthropic", "overseas", "USD", "false", "true"],
                    ["bge", "domestic", "CNY", "false", "true"],
                    ["x", "domestic", "CNY", "false", "true"]]
        # ★ 2026-11-17 模型身份对齐：源侧 key 清单（canonical_key + model_kind）。
        #   必须**排在**下面的聚合查询之前 —— 两者都含 "canonical_key"，
        #   只按关键词分流会把 17 列的聚合行当 2 列的 key 清单解包 ⇒ 门自己红。
        if "SELECT m.canonical_key, m.model_kind" in s:
            return [["gpt-4o", "文本/对话"],
                    ["claude-opus-4-8", "文本/对话"],
                    ["bge-m3", "向量/Embedding"],
                    ["", "文本/对话"]]
        # ★ 970 平台明细（19 列）：走的是**全部 scope** 的 latest CTE。
        if "cache_hit::text" in s:
            return []
        # ★ A-17 平台×币种诊断查询必须先于源查询判定：diag 与 src 共享
        #   latest_cte（都含 price_observation + canonical_key），唯一区分锚是
        #   "FROM mapped r"（diag 独有）。
        if "FROM mapped r" in s:
            return []           # A-17 平台×币种诊断（本批无行）
        # 源侧聚合（2026-11-17 起按 **out_name** 分组）。
        if "GROUP BY out_name" in s:
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
    marker = ('if (ov_in is None and ov_out is None and dm_in is None and dm_out is None\n'
              '                and vd_in is None and vd_out is None):\n'
              '            rejects["no_price"] += 1\n'
              '            continue')
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
        src_rows = [row("gpt-4o", dm_in="1.0", dm_out="2.0", dm_cnt="3",
                        dm_platform="openai"),
                    row("bge-m3")]
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


# ── A10: A-17 双地板折算（derive_dual_floors 行为门） ───────────────────
def check_dual_floors(mod):
    """A-17 双币制的折算行为门（纯函数直调）：
      ① 原生币事实源：USD vendor 原生价不动，CNY 折算 = ×快照 fx；
      ② 双地板：overseas（USD 原生）折 CNY、domestic（CNY 原生）折 USD；
      ③ 综合地板 = 逐轴 CNY 全局最低（海外折算值 vs 国内容生值）；
      ④ fx 缺失 ⇒ 需要 fx 的折算列全 NULL + fx_missing 旗标（fail-closed，
        绝不猜第二把汇率；NULL≠0）。
    """
    if not hasattr(mod, "derive_dual_floors"):
        bad("A10 脚本缺少 derive_dual_floors（A-17 双币折算缺失）")
        return

    # 场景 1：海外 USD 地板 33.38/166.92 vs 国内 CNY 地板 220/1100，fx=7.2
    # ⇒ 海外折 CNY = 240.336/1201.824；国内折 USD = 30.556/152.778；
    #   综合地板逐轴 CNY = min(240.336, 220)/min(1201.824, 1100) = 220/1100（国内赢）。
    r = row("m", ov_in="33.38", ov_out="166.92", ov_cnt="3", ov_platform="openrouter",
            dm_in="220", dm_out="1100", dm_cnt="4", dm_platform="siliconflow",
            vd_cur="USD", vd_in="30.0", vd_out="150.0", vd_name="Anthropic",
            vd_platform="anthropic")
    d = mod.derive_dual_floors(r[:17] + [None, None, None], "7.2")
    if d["ov_cny_in"] != "240.336000" or d["ov_cny_out"] != "1201.824000":
        bad("A10 海外地板折 CNY 错：应为 240.336000/1201.824000，实得 %r/%r"
            % (d["ov_cny_in"], d["ov_cny_out"]))
        return
    if d["dm_usd_in"] != "30.555556" or d["dm_usd_out"] != "152.777778":
        bad("A10 国内地板折 USD 错：应为 30.555556/152.777778，实得 %r/%r"
            % (d["dm_usd_in"], d["dm_usd_out"]))
        return
    if d["mm_in"] != "220.000000" or d["mm_out"] != "1100.000000":
        bad("A10 综合地板应为国内赢（220/1100），实得 %r/%r" % (d["mm_in"], d["mm_out"]))
        return
    if d["winner"] != "domestic" or d["mm_platform"] != "siliconflow" or d["mm_cnt"] != "4":
        bad("A10 综合地板归属错：应为 domestic@siliconflow(4)，实得 %r/%r/%r"
            % (d["winner"], d["mm_platform"], d["mm_cnt"]))
        return
    if d["vd_cny_in"] != "216.000000" or d["vd_usd_in"] != "30.000000":
        bad("A10 vendor 折算错：cny=30×7.2=216、usd=原币 30，实得 %r/%r"
            % (d["vd_cny_in"], d["vd_usd_in"]))
        return
    if d["fx_missing"]:
        bad("A10 fx=7.2 ⇒ fx_missing 必须为 False")
        return
    ok("A10 双地板折算：overseas×fx / domestic÷fx / 综合地板国内赢（逐轴 CNY）")

    # 场景 2：fx 缺失 ⇒ 折算列全 NULL + fx_missing=True；综合地板仍可用
    # 国内原生（零折算）值；海外原生值保留在 regional 列（由 upsert 写）。
    d2 = mod.derive_dual_floors(r[:17] + [None, None, None], None)
    if not d2["fx_missing"]:
        bad("A10 fx=None ⇒ fx_missing 必须为 True（fail-closed）")
        return
    if d2["ov_cny_in"] is not None or d2["dm_usd_in"] is not None or d2["vd_cny_in"] is not None:
        bad("A10 fx 缺失 ⇒ 需要 fx 的折算列必须 NULL（不猜第二把汇率）：%r" % (d2,))
        return
    if d2["mm_in"] != "220.000000" or d2["winner"] != "domestic":
        bad("A10 fx 缺失时综合地板应回落国内原生（220）：%r" % (d2,))
        return
    ok("A10 fx 缺失 fail-closed：折算列 NULL + 旗标 + 综合地板回落国内原生")

    # 场景 3：纯国内（无海外观测、无 vendor）——currency_region 之外的候选形态。
    r3 = row("m2", dm_in="5.0", dm_out="10.0", dm_cnt="2", dm_platform="ppio")
    d3 = mod.derive_dual_floors(r3[:17] + [None, None, None], "7.2")
    if d3["mm_in"] != "5.000000" or d3["winner"] != "domestic" or d3["fx_missing"]:
        bad("A10 纯国内候选错：%r" % (d3,))
        return
    ok("A10 纯国内候选：综合地板=国内原生、无 fx 依赖")


# ── A11: 模型身份对齐（2026-11-17 新增）──────────────────────────────────
def check_identity_alignment(mod):
    """★ 本轮**根因**门：源侧 canonical_key 带厂商命名空间前缀，而网关 canonical
    名是裸模型名 ⇒ 旧两趟解析解析率仅 24.3%、参考价与评分面名字交集仅 12.5%
    ⇒ 对账页「我方价格 vs 市场地板」几乎全空。

    A11a 剥前缀：`openai:-gpt-4o` ⇒ `gpt-4o`；`z.ai:-glm-5.3` ⇒ `glm-5.3`；
         `baai:bge-m3` ⇒ `bge-m3`；`{}` 噪声尾巴要去掉。
    A11b **原样精确优先**：网关自己有 Ollama 风格名（`qwen2.5:7b`）⇒ 原样
         命中时**不得**剥前缀（否则会误命中别的模型）。
    A11c 归一兜底：`GPT-4O` ⇒ `gpt-4o`。
    A11d 歧义 fail-closed：归一后命中多个 ⇒ 不猜（None）。
    """
    for fn in ("strip_namespace", "resolve_canonical", "is_not_a_model"):
        if not hasattr(mod, fn):
            bad("A11 脚本缺少 %s（模型身份对齐缺失 ⇒ 对账页无地板可比）" % fn)
            return

    cases = [
        ("openai:-gpt-4o", "gpt-4o"),
        ("z.ai:-glm-5.3", "glm-5.3"),
        ("baai:bge-m3", "bge-m3"),
        ("doubao-seed-2.1-pro{}", "doubao-seed-2.1-pro"),
        ("plain-name", "plain-name"),
    ]
    for src, want in cases:
        got = mod.strip_namespace(src)
        if got != want:
            bad("A11a strip_namespace(%r) = %r（应为 %r）" % (src, got, want))
            return
    ok("A11a 剥命名空间前缀：%d 个形态全对（openai:-/z.ai:-/baai:/{} 尾巴）" % len(cases))

    # 网关字典：42=gpt-4o、77=claude-opus-4-8、900=qwen2.5:7b（Ollama 风格）
    names = {"42": "gpt-4o", "77": "claude-opus-4-8", "900": "qwen2.5:7b"}
    exact, nrm = {}, {}
    for cid, n in names.items():
        exact.setdefault(n.lower(), []).append(cid)
        nrm.setdefault(mod.normalize(n), []).append(cid)

    cid, gwn, fam = mod.resolve_canonical("openai:-gpt-4o", exact, nrm, names)
    if cid != "42" or gwn != "gpt-4o" or fam != "resolved":
        bad("A11b 剥前缀后应解析到 gpt-4o，实得 %r/%r/%r" % (cid, gwn, fam))
        return
    ok("A11b 带命名空间的源 key ⇒ 解析到网关 canonical（openai:-gpt-4o → gpt-4o）")

    cid, gwn, fam = mod.resolve_canonical("qwen2.5:7b", exact, nrm, names)
    if cid != "900" or gwn != "qwen2.5:7b":
        bad("A11c 原样精确命中必须优先于剥前缀（qwen2.5:7b 被剥成 7b ⇒ 误命中）：%r/%r"
            % (cid, gwn))
        return
    ok("A11c 原样精确优先：Ollama 风格名 qwen2.5:7b 不被剥前缀（不误命中 7b）")

    cid, gwn, fam = mod.resolve_canonical("GPT-4O", exact, nrm, names)
    if cid != "42" or fam != "resolved":
        bad("A11d 大小写归一兜底失效：GPT-4O ⇒ %r/%r" % (cid, fam))
        return
    ok("A11d 归一兜底：GPT-4O ⇒ gpt-4o")

    dup_exact = {"1": ["a"], "2": ["b"]}
    dup_nrm = {mod.normalize("gpt-4o"): ["1", "2"]}
    dup_names = {"1": "gpt-4o", "2": "gpt4o"}
    cid, gwn, fam = mod.resolve_canonical("gpt-4o", dup_exact, dup_nrm, dup_names)
    if cid is not None or fam != "ambiguous":
        bad("A11e 歧义必须 fail-closed（不猜），实得 %r/%r" % (cid, fam))
        return
    ok("A11e 歧义 fail-closed：归一命中多个 ⇒ 不解析（台账计数暴露）")


# ── A12: 同 canonical 合并（别名收敛）────────────────────────────────────
def check_merge_keeps_identity(mod):
    """★ 旧实现把「多个源 key 解析到同一网关 canonical」误判成**歧义**并把
    canonical_id 全部回退 NULL ⇒ 211 实测 165 个 canonical / 43.3% 观测拿不到
    可比地板。新实现：**同名合并**（SQL 侧 GROUP BY out_name）⇒ cid 必须保住。

    本门验「合并后的形态」：resolved_map 里同一个 out_name 只对应**一个** cid；
    plan_rows 走 resolved_map 路径时**不得**把它回退成 NULL。
    """
    resolved_map = {"gpt-4o": ("42", "resolved", "文本/对话")}
    src_rows = [row("gpt-4o", dm_in="1.0", dm_out="2.0", dm_cnt="3", dm_platform="openai")]
    landable, rejects = mod.plan_rows(src_rows, None, {}, resolved_map)
    if len(landable) != 1:
        bad("A12 合并路径产出 %d 行（应为 1）" % len(landable))
        return
    if landable[0][17] != "42":
        bad("A12 合并后 canonical_id 被回退成 %r（应保住 42）"
            "⇒ 别名收敛把可比地板又弄丢了" % (landable[0][17],))
        return
    if landable[0][18] != "resolved":
        bad("A12 family 应为 resolved，实得 %r" % (landable[0][18],))
        return
    ok("A12 合并路径保住 canonical_id（别名收敛不再回退 NULL）")

    # 反向对照：resolved_map 为 None（旧路径）时，resolve 回调仍被使用。
    resolve = make_resolver({"gpt-4o": ["42"]})
    landable2, _ = mod.plan_rows(src_rows, resolve, {})
    if landable2[0][17] != "42":
        bad("A12 旧路径（resolved_map=None）解析失效：%r" % (landable2[0][17],))
        return
    ok("A12 旧路径（resolved_map=None）兼容：resolve 回调仍生效")


# ── A13: 非模型拒绝族 ────────────────────────────────────────────────────
def check_not_a_model_family(mod):
    """源侧把**上下文阶梯标签 / 图片分辨率档**当成了模型名（211 实测：
    `128k<token≤256k` / `720p` / `480p`）⇒ 落地只会在对账页造出
    「市场上有这个模型」的假信号 ⇒ 必须拒绝且**可计数**。
    """
    if mod.is_not_a_model("128k<token≤256k", "文本/对话", None) is not True:
        bad("A13 阶梯标签 `128k<token≤256k` 未被识别为非模型")
        return
    if mod.is_not_a_model("720p", "图像生成", None) is not True:
        bad("A13 分辨率档 `720p`（图像生成）未被识别为非模型")
        return
    if mod.is_not_a_model("gpt-4o", "文本/对话", "42") is not False:
        bad("A13 已解析到网关 canonical 的行被判成非模型 ⇒ 会误删真模型")
        return
    if mod.is_not_a_model("qwen3.7-text-embedding-flash", "向量/Embedding", None) is not False:
        bad("A13 向量/Embedding 未解析行被判非模型 ⇒ 会误删网关在用的模型价格")
        return
    ok("A13 非模型判据：阶梯标签/分辨率档拒绝；已解析行与 embedding 行保留")

    # 拒绝计数必须能被台账读到（不静默）。
    src_rows = [row("720p", dm_in="1.0", dm_out="2.0", dm_cnt="1", dm_platform="x")]
    landable, rejects = mod.plan_rows(
        src_rows, make_resolver({}), {},
        {"720p": (None, "unresolved", "图像生成")})
    if rejects.get("not_a_model", 0) != 1:
        bad("A13 not_a_model 拒绝族计数不对：%r" % (rejects,))
        return
    if len(landable) != 0:
        bad("A13 非模型行仍被算作可落地：%r" % (landable,))
        return
    ok("A13 非模型行落进 not_a_model 拒绝族（台账可读、恒等式闭合）")


# ── A14: 台账指纹与身份读数 ──────────────────────────────────────────────
def check_ledger_fingerprint(mod):
    """★ 部署漂移自证：台账 detail 必须带 **脚本自身 sha256** 与身份对齐读数。
    动机（211 实测）：宿主 cron 跑 /root/market-price-sync.py，它与仓库副本
    长期漂移 ⇒ 平台现价表恒空而台账看起来一切正常 ⇒ 台账自带指纹即可自证。
    """
    if not hasattr(mod, "script_sha256"):
        bad("A14 脚本缺少 script_sha256（部署漂移无法自证）")
        return
    digest = mod.script_sha256()
    if not digest or len(digest) != 64:
        bad("A14 script_sha256 返回的不是 sha256 十六进制：%r" % (digest,))
        return
    src = open(SCRIPT, encoding="utf-8").read()
    if '"script_sha256": script_sha256()' not in src:
        bad("A14 台账 detail 未写入 script_sha256 ⇒ 漂移不可读")
        return
    for key in ("identity_resolved_out_names", "identity_namespace_stripped",
                "identity_merged_groups"):
        if key not in src:
            bad("A14 台账 detail 缺身份对齐读数 %s" % key)
            return
    ok("A14 台账自带脚本指纹（sha256 %s…）+ 身份对齐读数" % digest[:12])


# ── A15: 970 明细的 model_kind 必须与门判同源（canonical 分类优先）────────
def _split_sql_values(tup):
    """把 SQL VALUES (...) 里的字面量按 SQL 语义拆开（尊重单引号转义）。

    ★ 为什么不能按 ", " 裸切：tier / model_kind 值里**含逗号**
      （如 `输入长度[0,32K),输入长度[32K,128K)`）⇒ 裸切会错位 ⇒
      量出来的「脏值」其实是量具自己的错位（2026-11-17 已踩过一次，
      曾据此误判「970 model_kind 有脏值」，实为假象）。
    """
    out, buf, i, inq = [], "", 0, False
    while i < len(tup):
        ch = tup[i]
        if inq:
            if ch == "'":
                if i + 1 < len(tup) and tup[i + 1] == "'":
                    buf += "'"
                    i += 2
                    continue
                inq = False
                i += 1
                continue
            buf += ch
            i += 1
            continue
        if ch == "'":
            inq = True
            i += 1
            continue
        if ch == ",":
            out.append(buf.strip())
            buf = ""
            i += 1
            continue
        buf += ch
        i += 1
    out.append(buf.strip())
    return out


def _platform_quote_model_kind_msgs(mod):
    """返回失败消息列表（空 = 通过）。

    ★ 抽成「返回 msgs」是为了让**变异臂**能对变异体跑同一套断言
      （行为门 + 变异臂必须成对，否则门无牙）。
    """
    """★ 同一个模型在同一份产物里只能有**一个**类别。

    动机（211 真库实测）：源侧 `price_observation.model_kind`（观测快照）与
    `model.model_kind`（canonical 分类）在 6341/19181 行**不一致** ⇒ 旧口径下
    970 明细写的是观测快照值，而「非模型拒绝门」「同名合并取最高可比类别」
    用的是 canonical 值 ⇒ **决策面与候选面判据不一致**（与 F6 同型：
    键一样、闸不一样）。
    ⇒ 970 明细的 model_kind 必须与门判**同源**（canonical 优先）。

    ★ 这条是**行为门**：跑真 main()、抓真生成的 970 INSERT、逐条读列值。
    """
    import io
    import contextlib

    msgs = []

    src_rows = [row("gpt-4o", dm_in="1.0", dm_out="2.0", dm_cnt="3", dm_platform="openai")]
    # 970 明细行（19 列，列序与脚本注释一致）：
    #   0 out_name 1 pid 2 pname 3 is_vendor 4 vendor 5 cur 6 nin 7 nout
    #   8 cin 9 cout 10 region 11 native 12 run 13 url 14 tier
    #   15 model_kind 16 cache_native 17 cache_cny 18 scope 19 src_region
    plat_rows = [
        # ① 源侧给的是**观测快照值**（与 canonical 不同）⇒ 落库必须是 canonical
        ["gpt-4o", "openai", "OpenAI", "false", "OpenAI", "CNY",
         "1.0", "2.0", "6.0", "12.0", "domestic", "CNY", "2026-11-17",
         "https://example.invalid/p", "0<Token≤32K", "观测快照值",
         "0.5", "3.0", "标准按量", "中国大陆"],
        # ② 只在非标准 scope 出现的模型（不在 key 清单 ⇒ 不在 resolved_map）
        #    ⇒ 必须**回落**到源侧 SQL 给的值，不得被写成空
        ["promo-only-model", "promo", "Promo", "false", "Promo", "CNY",
         "3.0", "6.0", "18.0", "36.0", "domestic", "CNY", "2026-11-17",
         "https://example.invalid/q", "", "文本/对话",
         "", "", "限时优惠", "中国大陆"],
        # ③ **同模型同平台同 tier 但不同源 region** ⇒ 必须是**两行**
        #    （211 真库实测：bailian deepseek-v4-flash-0731 忙时档有 5 个 region，
        #     3.000000 vs 3.208000 CNY）⇒ 静默并成一行＝丢 7% 的价差。
        ["gpt-4o", "openai", "OpenAI", "false", "OpenAI", "CNY",
         "1.2", "2.4", "7.2", "14.4", "domestic", "CNY", "2026-11-17",
         "https://example.invalid/p", "0<Token≤32K", "观测快照值",
         "0.6", "3.6", "标准按量", "美国"],
        # ④ **完全同表键**（同模型/平台/tier/region/currency）但价格不同
        #    （别名收敛的产物）⇒ 必须**折叠成一行**且**取地板口径的最低价**
        ["gpt-4o", "openai", "OpenAI", "false", "OpenAI", "CNY",
         "0.9", "1.8", "5.4", "10.8", "domestic", "CNY", "2026-11-16",
         "https://example.invalid/p2", "0<Token≤32K", "观测快照值",
         "0.4", "2.7", "标准按量", "中国大陆"],
    ]
    captured = {}

    def fake_psql_tsv(container, user, db, sql):
        s = sql
        if "to_regclass('public.market_platform_registry')" in s:
            return [["t"]]
        if "to_regclass('public.market_price_platform_quotes')" in s:
            return [["t"]]
        if "FROM public.market_platform_registry" in s:
            return [["openai", "domestic", "CNY", "false", "true"],
                    ["promo", "domestic", "CNY", "false", "true"]]
        if "SELECT m.canonical_key, m.model_kind" in s:
            # ★ canonical 分类：gpt-4o 是「文本/对话」；promo-only-model 不在
            #   标准按量清单里（只有非标准 scope 才有）⇒ 不出现在这里。
            return [["gpt-4o", "文本/对话"]]
        if "cache_hit::text" in s:
            return plat_rows
        if "FROM mapped r" in s:
            return []
        if "GROUP BY out_name" in s:
            return src_rows
        if "count(DISTINCT platform_id)" in s:
            return [["2"]]
        if "FROM fx_rate" in s:
            return [["7.2"]]
        if "FROM models_canonical" in s:
            return [["42", "gpt-4o"]]
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
                             "_mps_dump_a15.sql")
    argv = sys.argv
    sys.argv = ["market-price-sync.py", "--dump-sql", dump_path]
    buf = io.StringIO()
    try:
        with contextlib.redirect_stdout(buf):
            mod.main()
    except SystemExit as exc:
        if exc.code not in (0, None):
            msgs.append("A15 main() 异常退出：%r" % (exc.code,))
            return msgs
    except Exception as exc:  # noqa: BLE001
        msgs.append("A15 main() 执行异常：%r" % (exc,))
        return msgs
    finally:
        mod.psql_tsv, mod.psql_exec = orig_tsv, orig_exec
        sys.argv = argv

    try:
        with open(dump_path, encoding="utf-8") as fh:
            script = fh.read()
    except OSError as exc:
        msgs.append("A15 读不到 dump 出来的目标事务 SQL：%r" % (exc,))
        return msgs
    finally:
        if os.path.exists(dump_path):
            os.remove(dump_path)

    # 970 INSERT 的 21 列：
    #   0 dimension 1 canonical_name 2 platform_id 3 platform_name 4 region
    #   5 native_currency 6 is_vendor 7 vendor 8 currency 9 input_per_1m
    #   10 output_per_1m 11 cny_in_per_1m 12 cny_out_per_1m 13 run_date
    #   14 source_url 15 tier 16 model_kind 17 source_region
    #   18 cache_hit_per_1m 19 cny_cache_hit_per_1m 20 updated_at
    rows = []
    for stmt in script.split(";\n"):
        stmt = stmt.strip()
        if not stmt.startswith("INSERT INTO public.market_price_platform_quotes"):
            continue
        i = stmt.index("VALUES")
        tup = stmt[i + len("VALUES"):].strip()
        if not (tup.startswith("(") and tup.endswith(")")):
            msgs.append("A15 970 INSERT 的 VALUES 形态异常：%s" % tup[:120])
            return msgs
        vals = _split_sql_values(tup[1:-1])
        if len(vals) != 21:
            msgs.append("A15 970 INSERT 列数 %d ≠ 21 ⇒ 列序错位" % len(vals))
            return msgs
        rows.append(vals)

    kinds = {}
    for vals in rows:
        kinds.setdefault(vals[1], []).append(vals[16])    # canonical_name -> [model_kind]

    # ① 源侧 4 行 ⇒ 落地 3 行（④ 与 ① 同表键 ⇒ 折叠）
    if len(rows) != 3:
        msgs.append("A15 落地行数 %d ≠ 3（源 4 行，同表键折叠 1 行）" % len(rows))
        return msgs
    # ② 同模型不同 region ⇒ 两行（不得并成一行）
    gpt = [v for v in rows if v[1] == "gpt-4o"]
    if len(gpt) != 2:
        msgs.append("A15 gpt-4o 落地 %d 行（应为 2：不同 source_region 各一行）"
                    % len(gpt))
        return msgs
    regions = sorted(v[17] for v in gpt)
    if not any("美国" in v[17] for v in gpt) or not any("中国" in v[17] for v in gpt):
        msgs.append("A15 gpt-4o 的两个 source_region 丢了：%r" % (regions,))
        return msgs
    # ③ 同表键折叠 ⇒ 留**地板口径最低价**那一条（cny_in 5.4 而非 6.0）
    cn = [v for v in gpt if "中国" in v[17]]
    if not cn:
        msgs.append("A15 gpt-4o 的中国大陆那一行不见了")
        return msgs
    if cn[0][11] != "5.4":
        msgs.append("A15 同表键折叠后留的是 %r（应为地板口径最低 cny_in 5.4）"
                    % (cn[0][11],))
        return msgs
    # ④ model_kind 取 canonical 分类
    for vals in rows:
        if vals[1] == "gpt-4o" and vals[16] != "文本/对话":
            msgs.append("A15 gpt-4o 的 model_kind 落库为 %r（应为 canonical 的「文本/对话」）"
                        "⇒ 970 明细与拒绝门/合并门不是同一套分类" % (vals[16],))
            return msgs
    promo = [v for v in rows if v[1] == "promo-only-model"]
    if not promo:
        msgs.append("A15 非标准 scope 独有模型没落地")
        return msgs
    if promo[0][16] != "文本/对话":
        msgs.append("A15 非标准 scope 独有模型的 model_kind 落库为 %r"
                    "（应回落保留源侧值，不得为空）" % (promo[0][16],))
        return msgs


def check_platform_quote_model_kind(mod):
    """A15 门：跑真 main()、抓真生成的 970 INSERT、逐条读列值。"""
    msgs = _platform_quote_model_kind_msgs(mod)
    if msgs:
        for m in msgs:
            bad(m)
        return
    ok("A15 970 明细 model_kind 取 canonical 分类（不取观测快照值）")
    ok("A15 不在标准按量清单里的模型回落保留源侧 model_kind")
    ok("A15 970 表键含 source_region（同模型不同地区不合并）")
    ok("A15 同表键折叠取地板口径最低价（别名收敛不丢行也不重复）")


def check_a15_mutation_arm():
    """★ 变异臂：把 canonical 优先改回「直接用源侧值」⇒ A15 必须红。

    ★ 为什么必须有这条：文本门抓不到「同一函数被掏空」这类行为变异
      （本仓 §708① 同型事故）⇒ 行为门必须配一条**证明自己会红**的变异臂。
    ★ 锚点未命中 ≠ 守卫失效，它意味着变异压根没发生（假阴性）
      ⇒ 锚点不命中时必须报红，不能静默放过。
    """
    src = open(SCRIPT, encoding="utf-8").read()
    anchor = ('            kind = ""\n'
              '            rm = resolved_map.get(pr[0])\n'
              '            if rm is not None:\n'
              '                kind = rm[2] or ""\n'
              '            if not kind:\n'
              '                kind = pr[15] or ""\n')
    if anchor not in src:
        bad("A15 变异臂锚点不在脚本里（canonical 优先那段形态变了 ⇒ 门需重新对齐）")
        return
    mutated = src.replace(anchor, '            kind = pr[15] or ""  # MUTATED: 退回观测快照值\n', 1)
    if mutated == src:
        bad("A15 变异未生效（替换后文本没变）")
        return
    tmp_path = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                            "_mps_mutant_a15_tmp.py")
    try:
        with open(tmp_path, "w", encoding="utf-8") as fh:
            fh.write(mutated)
        mmod = load_module(tmp_path, name="mps_mutant_a15")
        msgs = _platform_quote_model_kind_msgs(mmod)
        if msgs:
            ok("A15 变异臂：退回观测快照值 ⇒ 门抓到 %r（有牙）" % (msgs[0][:60],))
        else:
            bad("A15 变异臂无效：退回观测快照值后门仍绿 ⇒ 门无牙")
    except Exception as exc:  # noqa: BLE001
        bad("A15 变异臂执行异常：%r" % (exc,))
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
    check_dual_floors(mod)
    check_identity_alignment(mod)
    check_merge_keeps_identity(mod)
    check_not_a_model_family(mod)
    check_ledger_fingerprint(mod)
    check_platform_quote_model_kind(mod)
    check_a15_mutation_arm()

    print("\n================ 汇总 ================")
    print("通过 %d / 失败 %d" % (PASS, FAIL))
    return 0 if FAIL == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
