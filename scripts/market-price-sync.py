#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""market-price-sync.py —— salesdecorate 行情 → 网关 940 市场参考价【单向】同步。

用法（在 211 宿主上执行；两侧 DB 都只经 docker exec psql 访问，零端口依赖）：
  python3 scripts/market-price-sync.py [--dry-run] [--as-of YYYY-MM-DD]
      [--scope 标准按量] [--dimension standard] [--unit per_1M_tokens]
      [--exclude-platform ID ...] [--source-container acc-pg17]
      [--target-container acc-pg17]

★ 2026-11-12 A-17 收口轮两处运维修正：
  · **默认源容器 wa-pg17 → acc-pg17**：salesdecorate 库已并入 acc-pg17 实例
    （collector SD_DB_HOST=192.168.31.211:15432 = acc-pg17 的宿主映射端口；
    wa-pg17 容器已不存在 ⇒ 2026-11-11 02:30 起 cron 夜跑 FATAL
    "No such container: wa-pg17"，行情管道断供一夜）。
  · 源/目标默认同一容器、不同 DB 名（salesdecorate / llm_gateway）；
    源侧用户默认 postgres（docker exec 本地通道，与 11-10 首跑一致）。

红线（脚本自身强制，见 2026-11-03 方案档 §4.4 与 940 迁移头注）：
  · 只写 market_price_reference / market_price_reference_sync（940 行情面）；
    绝不触碰 model_credit_rates / model_price_versions /
    credential_model_bindings（路由与计费面）——行情价禁止回流路由。
    脚本在目标事务前后对 model_credit_rates 做行数锚点自检，不等则中止。
  · 幂等：同键 upsert（ON CONFLICT DO UPDATE），台账追加可对账
    （源条数 = 落地 + 拒绝）。
  · NULL≠0：源侧无价保持 NULL（未知 ≠ 免费红线）。
  · FX 单源：折算只用一份快照汇率（源库 fx_rate 最新 USD→CNY），
    禁第二把汇率；快照缺失 ⇒ 折算列 NULL（fx_missing 显式旗标），绝不猜。

同步口径（A-17 双币制，总体设计定稿 2026-11-11 §3.6 + 2026-11-12 收口轮）：
  · 每 (platform, raw_model, scope, tier, region, currency, unit) 取最新
    run_date 行（DISTINCT ON）；
  · **区域/币种裁定唯一权威 = 网关侧 market_platform_registry（958/959）**：
    overseas ⇒ USD 原生、domestic ⇒ CNY 原生（region×currency 联动 CHECK）；
    未登记平台 ⇒ 拒绝族 **platform_unmapped_region**（台账 detail 逐平台计数
    + stderr 告警，绝不按名字猜区域）；观测行币种 ≠ 区域原生币 ⇒ 币种冲突
    行不进任何地板（a17_currency_mismatch_* 显式计数，不做静默改判——
    deepseek 等边缘案例以 registry 为准，与定稿 notes 纪律一致）；
  · **P1 原厂锚** = is_vendor 平台的最低价，按采集时点 CNY 折算跨币选赢家，
    赢家行的**原生币 + 原生价**落 vendor_list_currency + vendor_list_in/out
    （A-17 原则 1：原币是事实源）；P1 的 CNY 折算列用快照 fx 重算
    （vendor_list_cny_*），采集时点折算只用于选赢家；
  · **P2a 海外地板** = overseas 区非 vendor 非 我方 平台的 USD 原生最低价
    （overseas_min_*）+ 折 CNY 快照列；**P2b 国内地板** = domestic 区同口径
    的 CNY 原生最低价（domestic_min_*）+ 折 USD 快照列；
  · **综合地板**（market_min_*，940 原列位）= 双地板按快照 fx 折到 CNY 后的
    逐轴全局最低（与 940 旧值同口径 ⇒ overview/941 视图/报价 P2 轴零漂移）；
  · 我方平台排除（A-5）= registry is_ours=true 或 enabled=false 的平台，
    不进任何地板（台账 a17_excluded_* 显式计数）；
  · --exclude-platform 保持 940 旧语义（源侧预聚合排除，运维 override 旋钮）；
  · dimension = price_scope 归一（默认 standard=「标准按量」；条件价不压平）。

台账口径（2026-11-10 审计 P2-C 修 + A-17 收口轮扩）：
  · **源侧候选**（含「源侧有行但无市场价」的缺价行）全部进 src_rows
    ⇒ source_rows = len(src_rows)；
  · 缺价 / 无名 / 无出价日期 ⇒ **拒绝**（不进 940 目标表）⇒ rejected_rows
    （rejected_by_reason 逐族计数）；
  · 其余 ⇒ **落地**（写 market_price_reference）⇒ landed_rows；
  · ⇒ **恒等式：source_rows = landed_rows + rejected_rows**（构造上闭合）。
  · A-17 平台级排除（unmapped / ours_excluded / currency_mismatch）发生在
    聚合层的 FILTER 上（候选仍进 src_rows 恒等式），逐平台行数进台账
    detail.a17_* 键 ⇒ 不静默。
"""

import argparse
import json
import subprocess
import sys
from datetime import date

SCOPE_STANDARD = "标准按量"


def die(msg, code=1):
    print("FATAL: " + msg, file=sys.stderr)
    sys.exit(code)


# NULL 哨兵（2026-11-10 审计 P2-C）。
#
# ★ 缺陷形态：psql 的 `\pset null` 默认是**空串**，`-tA`（unaligned）输出下
# SQL NULL 列就印成空串 ⇒ 原实现里 `c.strip().upper() != "NULL"` 这个分支
# **永不触发**（psql 不会打印字面 "NULL"）⇒ DB NULL 到达 Python 时是 ""，
# 不是 None。
# ⇒ 后果链：① `if min_in is None and min_out is None: rejected += 1` 是死代码
#   ⇒ `rejected_rows` **恒为 0**，「源 = 落地 + 拒绝」是空恒等式，台账没有信号；
#   ② 无法区分「上游没给价（NULL）」与「上游给了空字符串」——
#   而 `num()` 把两者都折成 "NULL"、`qstr()` 把 "" 写成 `''` ⇒
#   「vendor_platform_id 恒为空串」这条 P2-2 的根因也在这里。
#
# ⇒ 修法：显式给 psql 一个**不可能出现在数据里**的 NULL 渲染串，再把该串
# 还原成 None。哨兵用可打印 ASCII（不用控制字符：控制字符在 argv / 编码
# 边界上更脆），且带本仓专属前缀 ⇒ 与平台名 / vendor 名 / canonical_key
# 撞车的概率可忽略。
NULL_SENTINEL = "__LLMGW_NULL__"


def psql_tsv(container, user, db, sql):
    """Run psql inside a container, return list of rows (lists of stripped strings).

    ★ NULL 与空串**必须**可区分：psql 默认把 NULL 渲染成空串 ⇒ 这里用
    `-P null=<哨兵>` 显式改写渲染，再在解析时还原成 None（见 NULL_SENTINEL）。
    """
    cmd = ["docker", "exec", container, "psql", "-U", user, "-d", db, "-tA",
           "-P", "null=" + NULL_SENTINEL,
           "-v", "ON_ERROR_STOP=1", "-c", sql]
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if proc.returncode != 0:
        die("psql failed on %s: %s" % (container, proc.stderr.strip()[:500]))
    rows = []
    for line in proc.stdout.splitlines():
        if line == "":
            continue
        cells = []
        for c in line.split("|"):
            c = c.strip()
            # 哨兵 ⇒ None（真 NULL）；空串 ⇒ ""（真空字符串，保留原义）。
            cells.append(None if c == NULL_SENTINEL else c)
        rows.append(cells)
    return rows


def psql_exec(container, user, db, sql):
    """Run a multi-statement psql script inside a container (stdin, ON_ERROR_STOP)."""
    cmd = ["docker", "exec", "-i", container, "psql", "-U", user, "-d", db,
           "-v", "ON_ERROR_STOP=1", "-q", "-f", "-"]
    proc = subprocess.run(cmd, input=sql, capture_output=True, text=True)
    return proc


def num(v):
    """TSV numeric → SQL literal (NULL preserved, never coerced to 0)."""
    if v is None or v == "":
        return "NULL"
    try:
        float(v)
    except ValueError:
        return "NULL"
    return v


def qstr(v):
    """Escape a text value as SQL string literal."""
    if v is None:
        return "NULL"
    return "'" + str(v).replace("'", "''") + "'"


def qstr_notnull(v):
    """Escape a text value into a **NOT NULL** column.

    ★ 940 的 vendor_vendor / market_min_platform_id / vendor_platform_id /
      canonical_name / source_url，与 959 的 vendor_list_currency /
      overseas_min_platform_id / domestic_min_platform_id 都是
      `NOT NULL DEFAULT ''` ⇒ 源侧 NULL 必须落成 ''（**不是** SQL NULL）
      ⇒ 否则整笔事务 NOT NULL 违例 abort。
      ⇒ psql 侧一旦开始区分 NULL 与空串（见 NULL_SENTINEL），这一层就是必需的：
        哨兵把 NULL 还原成 None ⇒ qstr(None) 会写 SQL NULL ⇒ 必须走本函数。
    """
    if v is None:
        return "''"
    return "'" + str(v).replace("'", "''") + "'"


def normalize(name):
    """模型名归一（与 scripts/audit/market-price-compare.py 同源）：小写、去 _-. 空白。"""
    out = []
    for ch in (name or "").lower():
        if ch not in "_-. ":
            out.append(ch)
    return "".join(out)


def numf(v):
    """TSV numeric → float（None/空/非数 ⇒ None）。折算算术的入口。"""
    if v is None or v == "":
        return None
    try:
        return float(v)
    except ValueError:
        return None


def fmt6(v):
    """float → SQL numeric 字面量（numeric(14,6) 精度；None ⇒ None）。"""
    if v is None:
        return None
    return "%.6f" % v


def derive_dual_floors(row, fx):
    """A-17 双地板快照折算（**纯函数**：零 IO ⇒ 可被单测直调）。

    row = plan_rows 产出的落地候选（20 元组，见 plan_rows 返回注释）；
    fx  = 快照汇率（USD→CNY，字符串或 None）。

    返回 dict：
      vd_cny_in/out   vendor 原生价的 CNY 折算（快照 fx）
      vd_usd_in/out   vendor 原生价的 USD 折算（快照 fx）
      ov_cny_in/out   海外地板（USD 原生）折 CNY
      dm_usd_in/out   国内地板（CNY 原生）折 USD
      mm_in/out       综合地板（逐轴 CNY 全局最低；940 原列位口径，恒 CNY）
      mm_cny_in/out   综合地板 CNY 表达（= mm_in/out，双列同值）
      mm_platform     综合地板归属平台（out 轴赢家所在平台，940 旧惯例）
      mm_cnt          综合地板归属区域的平台数
      fx_missing      本次折算是否因快照缺失而被迫置 NULL（True ⇒ 台账旗标）
      winner          综合地板归属区域（诊断）：'overseas'|'domestic'|None

    ★ FX 单源红线：需要 fx 而快照缺失 ⇒ 折算列 NULL + fx_missing=True，
    绝不引入第二把汇率；NULL≠0 ⇒ 缺失维度绝不折 0。
    ★ 归属区域（定稿「折算后最低」的区域级比较）只决定 mm_platform/mm_cnt；
      值本身逐轴独立取 CNY 最低（两轴可来自不同区域 —— 区域原生事实在
      overseas_min_* / domestic_min_* 列组，不在此列混写）。
    """
    fx_missing = False
    fx_f = numf(fx)

    def to_cny(v, cur):
        nonlocal fx_missing
        if v is None:
            return None
        if cur == "CNY":
            return v
        if cur == "USD" and fx_f is not None:
            return v * fx_f
        fx_missing = True
        return None

    def to_usd(v, cur):
        nonlocal fx_missing
        if v is None:
            return None
        if cur == "USD":
            return v
        if cur == "CNY" and fx_f is not None:
            return v / fx_f
        fx_missing = True
        return None

    cur = row[9] or ""
    ov_f_in, ov_f_out = numf(row[1]), numf(row[2])
    dm_f_in, dm_f_out = numf(row[5]), numf(row[6])
    vd_f_in, vd_f_out = numf(row[10]), numf(row[11])

    vd_cny_f_in = to_cny(vd_f_in, cur)
    vd_cny_f_out = to_cny(vd_f_out, cur)
    vd_usd_f_in = to_usd(vd_f_in, cur)
    vd_usd_f_out = to_usd(vd_f_out, cur)
    ov_cny_f_in = to_cny(ov_f_in, "USD")
    ov_cny_f_out = to_cny(ov_f_out, "USD")
    dm_usd_f_in = to_usd(dm_f_in, "CNY")
    dm_usd_f_out = to_usd(dm_f_out, "CNY")

    # 综合地板：逐轴 CNY 最低（海外折算值 vs 国内容生值）；两轴独立。
    ov_blend = None if (ov_cny_f_in is None or ov_cny_f_out is None) \
        else (ov_cny_f_in + ov_cny_f_out)
    dm_blend = None if (dm_f_in is None or dm_f_out is None) \
        else (dm_f_in + dm_f_out)
    if ov_blend is not None and dm_blend is not None:
        winner = "overseas" if ov_blend < dm_blend else "domestic"
    elif ov_blend is not None:
        winner = "overseas"
    elif dm_blend is not None:
        winner = "domestic"
    else:
        winner = None

    def axis_min_cny(ov_cny, dm_native):
        vals = [v for v in (ov_cny, dm_native) if v is not None]
        return min(vals) if vals else None

    mm_cny_in = axis_min_cny(ov_cny_f_in, dm_f_in)
    mm_cny_out = axis_min_cny(ov_cny_f_out, dm_f_out)
    if winner == "overseas":
        mm_platform, mm_cnt = row[4], row[3]
    elif winner == "domestic":
        mm_platform, mm_cnt = row[8], row[7]
    else:
        mm_platform, mm_cnt = "", 0

    return {
        "vd_cny_in": fmt6(vd_cny_f_in), "vd_cny_out": fmt6(vd_cny_f_out),
        "vd_usd_in": fmt6(vd_usd_f_in), "vd_usd_out": fmt6(vd_usd_f_out),
        "ov_cny_in": fmt6(ov_cny_f_in), "ov_cny_out": fmt6(ov_cny_f_out),
        "dm_usd_in": fmt6(dm_usd_f_in), "dm_usd_out": fmt6(dm_usd_f_out),
        "mm_in": fmt6(mm_cny_in), "mm_out": fmt6(mm_cny_out),
        "mm_cny_in": fmt6(mm_cny_in), "mm_cny_out": fmt6(mm_cny_out),
        "mm_platform": mm_platform, "mm_cnt": mm_cnt,
        "fx_missing": fx_missing, "winner": winner,
    }


def plan_rows(src_rows, resolve, existing_owner):
    """把源侧候选行分成【可落地】与【拒绝】两族（**纯函数**：零 IO ⇒ 可被单测直调）。

    src_rows 列序（17 列，与 psql_tsv 输出一致）：
      0 canonical_key, 1 ov_in, 2 ov_out, 3 ov_cnt, 4 ov_platform,
      5 dm_in, 6 dm_out, 7 dm_cnt, 8 dm_platform,
      9 vd_cur, 10 vd_in, 11 vd_out, 12 vd_cny_in, 13 vd_cny_out,
      14 vd_name, 15 vd_platform, 16 max_run_date

    返回 (landable, rejects)：
      landable = 20 元组（上述 17 列 + cid + family + final_cid）；
      rejects  = {"no_price": n, "no_canonical_name": n, "no_source_run_date": n}

    ★ 拒绝族只收「**写不出合法行**」的候选：
        · no_price          —— 厂商价与双地板**全部**缺价（A-17：市场面与
                               原厂面都未知 ⇒ 绝不能折 0，也绝不能写空行）；
        · no_canonical_name —— 没有 canonical_key（写不出主键）；
        · no_source_run_date—— 没有出价日期（source_run_date 是 NOT NULL）。
      ⇒ 拒绝判定**先于** canonical 解析：被拒的行不该占用 canonical 声明。

    ★ canonical 解析不出来（unresolved/ambiguous）的行**仍然落地**
      （canonical_id=NULL，如实保留 NULL），只在 conflicts 里计数
      ⇒ landed_rows == 实际写入条数 ⇒ 「源=落地+拒绝」是**可对账**的等式。
    """
    rejects = {"no_price": 0, "no_canonical_name": 0, "no_source_run_date": 0}
    claims = {}   # canonical_id -> set(lower canonical_key)（批内声明）
    staged = []
    for row in src_rows:
        key = row[0]
        ov_in, ov_out = row[1], row[2]
        dm_in, dm_out = row[5], row[6]
        vd_in, vd_out = row[10], row[11]
        max_run_date = row[16]
        # ── 拒绝族判定（先于解析）────────────────────────────────────────
        if key is None or str(key).strip() == "":
            rejects["no_canonical_name"] += 1
            continue
        if (ov_in is None and ov_out is None and dm_in is None and dm_out is None
                and vd_in is None and vd_out is None):
            rejects["no_price"] += 1
            continue
        if max_run_date is None:
            rejects["no_source_run_date"] += 1
            continue
        cid, family = resolve(key)
        if cid is not None:
            claims.setdefault(cid, set()).add(key.lower())
        staged.append(list(row[:17]) + [cid, family])

    landable = []
    for row in staged:
        cid, family = row[17], row[18]
        final_cid = cid
        if cid is not None:
            if len(claims.get(cid, ())) > 1:
                # 批内两个源 key 解析到同一网关 canonical（别名）⇒ 全部回退 NULL，
                # 否则撞 uq_mpr_canonical_id（首跑实证：canonical 352 双声明）。
                final_cid = None
                family = "ambiguous"
            elif existing_owner.get(cid) not in (None, row[0].lower()):
                # 目标已有别的 canonical_name 占有该 id（跨日属主漂移）⇒ 回退。
                final_cid = None
                family = "ambiguous"
        landable.append(row[:17] + [cid, family, final_cid])
    return landable, rejects


def build_upsert_sql(row, d, dimension, fx):
    """生成一条 market_price_reference 的 upsert（940+959 列序，36 列）。

    row = plan_rows 产出的 20 元组（17 源列 + cid + family + final_cid）；
    d   = derive_dual_floors() 的 A-17 折算结果；fx = 快照汇率字面量。

    ★ NOT NULL 文本列一律走 qstr_notnull（源侧 NULL ⇒ ''）⇒ 绝不触发
      NOT NULL 违例把整笔事务 abort 掉。
    ★ currency（940 原列）= 综合地板币种 = 'CNY'（959 COMMENT 钉死的落地口径；
      定稿「vendor_list_currency 镜像」的有意偏离 ⇒ 厂商原生币在列 14）。
    """
    (key, ov_in, ov_out, ov_cnt, ov_platform,
     dm_in, dm_out, dm_cnt, dm_platform,
     vd_cur, vd_in, vd_out, vd_cny_in, vd_cny_out, vd_name, vd_platform,
     max_run_date, cid, family, final_cid) = row[:20]
    return (
        "INSERT INTO public.market_price_reference (canonical_name, dimension, currency, canonical_id,"
        " vendor_list_in_per_1m, vendor_list_out_per_1m, vendor_platform_id, vendor_vendor,"
        " market_min_in_per_1m, market_min_out_per_1m, market_min_platform_id, market_min_platform_count,"
        " market_min_cny_in_per_1m, market_min_cny_out_per_1m,"
        " vendor_list_currency, vendor_list_cny_in_per_1m, vendor_list_cny_out_per_1m,"
        " vendor_list_usd_in_per_1m, vendor_list_usd_out_per_1m,"
        " overseas_min_in_per_1m, overseas_min_out_per_1m, overseas_min_platform_id, overseas_min_platform_count,"
        " overseas_min_cny_in_per_1m, overseas_min_cny_out_per_1m,"
        " domestic_min_in_per_1m, domestic_min_out_per_1m, domestic_min_platform_id, domestic_min_platform_count,"
        " domestic_min_usd_in_per_1m, domestic_min_usd_out_per_1m,"
        " fx_rate, source_run_date, source_confidence, source_url, updated_at)"
        " VALUES (%s, %s, 'CNY', %s,"
        " %s, %s, %s, %s,"
        " %s, %s, %s, %s,"
        " %s, %s,"
        " %s, %s, %s, %s, %s,"
        " %s, %s, %s, %s, %s, %s,"
        " %s, %s, %s, %s, %s, %s,"
        " %s, %s::date, 'A', '', now())"
        " ON CONFLICT (canonical_name, dimension) DO UPDATE SET"
        " currency = EXCLUDED.currency, canonical_id = EXCLUDED.canonical_id,"
        " vendor_list_in_per_1m = EXCLUDED.vendor_list_in_per_1m,"
        " vendor_list_out_per_1m = EXCLUDED.vendor_list_out_per_1m,"
        " vendor_platform_id = EXCLUDED.vendor_platform_id, vendor_vendor = EXCLUDED.vendor_vendor,"
        " market_min_in_per_1m = EXCLUDED.market_min_in_per_1m,"
        " market_min_out_per_1m = EXCLUDED.market_min_out_per_1m,"
        " market_min_platform_id = EXCLUDED.market_min_platform_id,"
        " market_min_platform_count = EXCLUDED.market_min_platform_count,"
        " market_min_cny_in_per_1m = EXCLUDED.market_min_cny_in_per_1m,"
        " market_min_cny_out_per_1m = EXCLUDED.market_min_cny_out_per_1m,"
        " vendor_list_currency = EXCLUDED.vendor_list_currency,"
        " vendor_list_cny_in_per_1m = EXCLUDED.vendor_list_cny_in_per_1m,"
        " vendor_list_cny_out_per_1m = EXCLUDED.vendor_list_cny_out_per_1m,"
        " vendor_list_usd_in_per_1m = EXCLUDED.vendor_list_usd_in_per_1m,"
        " vendor_list_usd_out_per_1m = EXCLUDED.vendor_list_usd_out_per_1m,"
        " overseas_min_in_per_1m = EXCLUDED.overseas_min_in_per_1m,"
        " overseas_min_out_per_1m = EXCLUDED.overseas_min_out_per_1m,"
        " overseas_min_platform_id = EXCLUDED.overseas_min_platform_id,"
        " overseas_min_platform_count = EXCLUDED.overseas_min_platform_count,"
        " overseas_min_cny_in_per_1m = EXCLUDED.overseas_min_cny_in_per_1m,"
        " overseas_min_cny_out_per_1m = EXCLUDED.overseas_min_cny_out_per_1m,"
        " domestic_min_in_per_1m = EXCLUDED.domestic_min_in_per_1m,"
        " domestic_min_out_per_1m = EXCLUDED.domestic_min_out_per_1m,"
        " domestic_min_platform_id = EXCLUDED.domestic_min_platform_id,"
        " domestic_min_platform_count = EXCLUDED.domestic_min_platform_count,"
        " domestic_min_usd_in_per_1m = EXCLUDED.domestic_min_usd_in_per_1m,"
        " domestic_min_usd_out_per_1m = EXCLUDED.domestic_min_usd_out_per_1m,"
        " fx_rate = EXCLUDED.fx_rate, source_run_date = EXCLUDED.source_run_date,"
        " updated_at = now();" % (
            qstr(key), qstr(dimension),
            (final_cid if final_cid else "NULL"),
            num(vd_in), num(vd_out), qstr_notnull(vd_platform), qstr_notnull(vd_name),
            num(d["mm_in"]), num(d["mm_out"]), qstr_notnull(d["mm_platform"]),
            (d["mm_cnt"] if d["mm_cnt"] else "0"),
            num(d["mm_cny_in"]), num(d["mm_cny_out"]),
            qstr_notnull(vd_cur), num(d["vd_cny_in"]), num(d["vd_cny_out"]),
            num(d["vd_usd_in"]), num(d["vd_usd_out"]),
            num(ov_in), num(ov_out), qstr_notnull(ov_platform),
            (ov_cnt if ov_cnt else "0"),
            num(d["ov_cny_in"]), num(d["ov_cny_out"]),
            num(dm_in), num(dm_out), qstr_notnull(dm_platform),
            (dm_cnt if dm_cnt else "0"),
            num(d["dm_usd_in"]), num(d["dm_usd_out"]),
            num(fx), qstr(max_run_date)))


def main():
    ap = argparse.ArgumentParser(description="salesdecorate → gateway market price sync (one-way)")
    ap.add_argument("--source-container", default="acc-pg17",
                    help="默认 acc-pg17：salesdecorate 库已并入该实例（wa-pg17 已退役）")
    ap.add_argument("--source-user", default="postgres")
    ap.add_argument("--source-db", default="salesdecorate")
    ap.add_argument("--target-container", default="acc-pg17")
    ap.add_argument("--target-user", default="acc_super")
    ap.add_argument("--target-db", default="llm_gateway")
    ap.add_argument("--scope", default=SCOPE_STANDARD, help="price_scope（中文原值）")
    ap.add_argument("--dimension", default="standard", help="940 dimension 归一值")
    ap.add_argument("--unit", default="per_1M_tokens")
    ap.add_argument("--as-of", default="", help="只取 run_date <= as-of（默认今天）")
    ap.add_argument("--exclude-platform", nargs="*", default=[],
                    help="从市场最低价中排除的平台（A-5 排除我方平台；940 旧语义=源侧预聚合排除）")
    ap.add_argument("--dry-run", action="store_true", help="只打印将要落地的行数与样例，不写目标")
    ap.add_argument("--dump-sql", default="", help="把目标事务 SQL 原样写到该路径（调试用）")
    args = ap.parse_args()

    # ── 0) 前置：目标库必须有 958/959（registry + 双地板列）——fail-closed ──
    prereq = psql_tsv(args.target_container, args.target_user, args.target_db,
                      "SELECT to_regclass('public.market_platform_registry') IS NOT NULL"
                      " AND EXISTS (SELECT 1 FROM information_schema.columns"
                      "   WHERE table_schema='public' AND table_name='market_price_reference'"
                      "   AND column_name='overseas_min_in_per_1m')")
    if not prereq or prereq[0][0] != "t":
        die("目标库缺 958/959 前置（market_platform_registry 或 959 双地板列不在场）"
            "⇒ 先部署网关（migration ≥ 959）再跑同步")

    # ── 1) A-17 registry（区域/币种裁定唯一权威，定稿 §3.2 原则 2） ──────────
    reg_rows = psql_tsv(args.target_container, args.target_user, args.target_db,
                        "SELECT platform_id, region, native_currency, is_ours::text, enabled::text"
                        " FROM public.market_platform_registry")
    reg = {}   # pid -> (region, native_currency, excluded_bool)
    for pid, region, native, is_ours, enabled in reg_rows:
        reg[pid] = (region, native, is_ours == "t" or enabled != "t")
    reg_values = ", ".join(
        "(%s, %s, %s, %s)" % (qstr(pid), qstr(region), qstr(native),
                              "true" if excluded else "false")
        for pid, (region, native, excluded) in sorted(reg.items()))
    if not reg_values:
        die("market_platform_registry 为空 ⇒ 区域裁定无权威（958 种子缺失？）")

    # ── 2) 源侧聚合（镜像 CompareByModel 的 latest-row 语义 + A-17 双地板） ──
    exclude_cond = ""
    if args.exclude_platform:
        ids = ", ".join(qstr(p) for p in args.exclude_platform)
        exclude_cond = " AND p.platform_id <> ALL(ARRAY[%s])" % ids
    # ★ 空 as-of = 不过滤（run_date <= NULL::date 会把全部行判 NULL 杀光——
    #   首跑实测踩到，勿回退成三元拼 NULL 的写法）。
    asof_cond = (" AND o.run_date <= %s::date" % qstr(args.as_of)) if args.as_of else ""
    latest_cte = """
WITH latest AS (
    SELECT DISTINCT ON (o.platform_id, o.raw_model_id, o.price_scope, o.tier, o.region, o.currency, o.unit)
        o.id
    FROM price_observation o
    WHERE o.price_scope = {scope_q}
      AND o.unit = {unit_q}{asof}
    ORDER BY o.platform_id, o.raw_model_id, o.price_scope, o.tier, o.region, o.currency, o.unit, o.run_date DESC
),
rows_ AS (
    SELECT m.canonical_key, p.platform_id, p.is_vendor, p.vendor,
           o.currency,
           o.input_price::float8  AS native_in,
           o.output_price::float8 AS native_out,
           o.cny_input::float8  AS cny_in,
           o.cny_output::float8 AS cny_out,
           o.run_date
    FROM latest l
    JOIN price_observation o ON o.id = l.id
    JOIN platform p ON p.platform_id = o.platform_id
    JOIN model m ON m.id = o.model_id{exclude}
),
reg(platform_id, reg_region, reg_native, reg_excluded) AS (
    VALUES {reg_values}
),
mapped AS (
    SELECT r.*, reg.reg_region, reg.reg_native, reg.reg_excluded
    FROM rows_ r
    LEFT JOIN reg ON lower(reg.platform_id) = lower(r.platform_id)
)"""
    src_sql = latest_cte + """,
agg AS (
    SELECT canonical_key,
           -- P2a 海外地板（USD 原生）：overseas 区 + 非 vendor + 非 我方/停用
           -- + 行币种 == 区域原生币（币种冲突行不进地板，见 a17_currency_mismatch）
           min(native_in)  FILTER (WHERE reg_region = 'overseas' AND NOT is_vendor
                                     AND NOT reg_excluded AND currency = reg_native) AS ov_in,
           min(native_out) FILTER (WHERE reg_region = 'overseas' AND NOT is_vendor
                                     AND NOT reg_excluded AND currency = reg_native) AS ov_out,
           count(DISTINCT platform_id) FILTER (WHERE reg_region = 'overseas' AND NOT is_vendor
                                     AND NOT reg_excluded AND currency = reg_native) AS ov_cnt,
           (ARRAY_AGG(platform_id ORDER BY native_out NULLS LAST)
              FILTER (WHERE reg_region = 'overseas' AND NOT is_vendor
                        AND NOT reg_excluded AND currency = reg_native))[1] AS ov_platform,
           -- P2b 国内地板（CNY 原生）
           min(native_in)  FILTER (WHERE reg_region = 'domestic' AND NOT is_vendor
                                     AND NOT reg_excluded AND currency = reg_native) AS dm_in,
           min(native_out) FILTER (WHERE reg_region = 'domestic' AND NOT is_vendor
                                     AND NOT reg_excluded AND currency = reg_native) AS dm_out,
           count(DISTINCT platform_id) FILTER (WHERE reg_region = 'domestic' AND NOT is_vendor
                                     AND NOT reg_excluded AND currency = reg_native) AS dm_cnt,
           (ARRAY_AGG(platform_id ORDER BY native_out NULLS LAST)
              FILTER (WHERE reg_region = 'domestic' AND NOT is_vendor
                        AND NOT reg_excluded AND currency = reg_native))[1] AS dm_platform,
           -- P1 原厂锚：vendor 平台（须已登记且币种==原生、非 我方/停用），
           -- 跨币按采集时点 CNY 折算选赢家（cny_* 是源库采集时点的权威折算），
           -- 赢家的原生币/原生价/平台随行带出；折算列由快照 fx 重算（derive）。
           (ARRAY_AGG(currency ORDER BY cny_out NULLS LAST)
              FILTER (WHERE is_vendor AND reg_region IS NOT NULL
                        AND NOT reg_excluded AND currency = reg_native))[1] AS vd_cur,
           (ARRAY_AGG(native_in ORDER BY cny_out NULLS LAST)
              FILTER (WHERE is_vendor AND reg_region IS NOT NULL
                        AND NOT reg_excluded AND currency = reg_native))[1] AS vd_in,
           (ARRAY_AGG(native_out ORDER BY cny_out NULLS LAST)
              FILTER (WHERE is_vendor AND reg_region IS NOT NULL
                        AND NOT reg_excluded AND currency = reg_native))[1] AS vd_out,
           (ARRAY_AGG(platform_id ORDER BY cny_out NULLS LAST)
              FILTER (WHERE is_vendor AND reg_region IS NOT NULL
                        AND NOT reg_excluded AND currency = reg_native))[1] AS vd_platform,
           (ARRAY_AGG(vendor ORDER BY cny_out NULLS LAST)
              FILTER (WHERE is_vendor AND reg_region IS NOT NULL
                        AND NOT reg_excluded AND currency = reg_native))[1] AS vd_name,
           min(cny_in)  FILTER (WHERE is_vendor AND reg_region IS NOT NULL
                                  AND NOT reg_excluded AND currency = reg_native) AS vd_cny_in,
           min(cny_out) FILTER (WHERE is_vendor AND reg_region IS NOT NULL
                                  AND NOT reg_excluded AND currency = reg_native) AS vd_cny_out,
           max(run_date)::text AS max_run_date
    FROM mapped
    GROUP BY canonical_key
)
SELECT canonical_key, ov_in, ov_out, ov_cnt, ov_platform,
       dm_in, dm_out, dm_cnt, dm_platform,
       vd_cur, vd_in, vd_out, vd_cny_in, vd_cny_out, vd_name, vd_platform,
       max_run_date
FROM agg
-- ★★ 2026-11-10 审计 P2-C：**这里原本有一句**
--      `WHERE min_in IS NOT NULL OR min_out IS NOT NULL`
--    ⇒ 「源侧有该模型、但市场价全缺」的候选在**源侧就被丢掉**
--    ⇒ Python 永远看不到它们 ⇒ rejected_rows 恒为 0
--    ⇒ 「源条数 = 落地 + 拒绝」是**空恒等式**（两边都少了同一批行）
--    ⇒ 台账没有任何信号：既看不出丢了多少，也看不出为什么丢。
--    ⇒ 现在**不在源侧过滤**，改由 plan_rows() 在 Python 侧做拒绝判定
--      （拒绝族：no_price / no_canonical_name / no_source_run_date）
--      ⇒ 源侧候选全集进 src_rows ⇒ 恒等式真正闭合。
-- ★★ 2026-11-12 A-17：平台级排除（unmapped / 我方 / 币种冲突）发生在
--    聚合 FILTER 层 ⇒ 候选仍进 src_rows 恒等式；逐平台行数由下面的
--    诊断查询补进台账 detail.a17_*，不静默。
ORDER BY canonical_key
""".format(scope_q=qstr(args.scope), unit_q=qstr(args.unit),
           asof=asof_cond, exclude=exclude_cond, reg_values=reg_values)

    # 诊断查询：本 scope 每平台×币种的行数（A-17 台账 detail 的数据源）。
    diag_sql = latest_cte + """
SELECT r.platform_id, r.currency, count(*)
FROM mapped r
GROUP BY 1, 2 ORDER BY 1, 2
""".format(scope_q=qstr(args.scope), unit_q=qstr(args.unit),
           asof=asof_cond, exclude=exclude_cond, reg_values=reg_values)

    src_rows = psql_tsv(args.source_container, args.source_user, args.source_db, src_sql)
    # source_platforms = 源侧本 scope 出现过的独立平台数（全量，未排除前）
    plat_rows = psql_tsv(args.source_container, args.source_user, args.source_db,
                         "SELECT count(DISTINCT platform_id) FROM price_observation WHERE price_scope = %s"
                         % qstr(args.scope))
    source_platforms = int(plat_rows[0][0]) if plat_rows else 0
    print("source: %d canonical rows (scope=%s, unit=%s), %d platforms in scope"
          % (len(src_rows), args.scope, args.unit, source_platforms))

    # ── 2b) A-17 平台级诊断（unmapped / 我方排除 / 币种冲突，逐平台行数） ────
    unmapped_by_platform, excluded_by_platform, mismatch_by_platform = {}, {}, {}
    unmapped_total = excluded_total = mismatch_total = 0
    for row in psql_tsv(args.source_container, args.source_user, args.source_db, diag_sql):
        pid, cur, n = row[0], row[1], int(row[2] or 0)
        info = reg.get(pid)
        if info is None:
            # A-17 原则 2：未登记平台 ⇒ 拒绝族 platform_unmapped_region
            # （不按名字猜区域）；行不进任何地板，逐平台计数进台账。
            unmapped_by_platform[pid] = unmapped_by_platform.get(pid, 0) + n
            unmapped_total += n
        else:
            region, native, excluded = info
            if excluded:
                excluded_by_platform[pid] = excluded_by_platform.get(pid, 0) + n
                excluded_total += n
            elif cur != native:
                mismatch_by_platform[pid] = mismatch_by_platform.get(pid, 0) + n
                mismatch_total += n
    if unmapped_by_platform:
        print("WARNING platform_unmapped_region: %d rows on %d unregistered platform(s): %s"
              % (unmapped_total, len(unmapped_by_platform),
                 ",".join(sorted(unmapped_by_platform))), file=sys.stderr)
    if mismatch_by_platform:
        print("WARNING currency_region_mismatch: %d rows on %d platform(s): %s"
              % (mismatch_total, len(mismatch_by_platform),
                 ",".join(sorted(mismatch_by_platform))), file=sys.stderr)

    # ── 3) fx 快照（源库最新 USD→CNY；折算唯一依据，FX 单源红线） ────────────
    fx = None
    fx_rows = psql_tsv(args.source_container, args.source_user, args.source_db,
                       "SELECT rate::text FROM fx_rate WHERE base='USD' AND quote='CNY' "
                       "ORDER BY retrieved_at DESC LIMIT 1")
    if fx_rows:
        fx = fx_rows[0][0]
    print("fx USD→CNY = %s" % (fx or "NULL"))

    # ── 4) 目标侧 canonical 解析（两趟：精确小写 → 归一化；歧义进 conflicts） ──
    canon_rows = psql_tsv(args.target_container, args.target_user, args.target_db,
                          "SELECT id::text, lower(canonical_name) FROM models_canonical")
    canon_exact, canon_norm = {}, {}
    for cid, name in canon_rows:
        canon_exact.setdefault(name, []).append(cid)
        canon_norm.setdefault(normalize(name), []).append(cid)

    def resolve(key):
        """返回 (canonical_id | None, family)；family ∈ resolved|unresolved|ambiguous。"""
        ids = canon_exact.get(key.lower(), [])
        if len(ids) == 1:
            return ids[0], "resolved"
        if len(ids) > 1:
            return None, "ambiguous"
        ids = canon_norm.get(normalize(key), [])
        if len(ids) == 1:
            return ids[0], "resolved"
        if len(ids) > 1:
            return None, "ambiguous"
        return None, "unresolved"

    # 目标既有属主：canonical_id 已被别的 canonical_name 占有的 ⇒ 批内回退 NULL。
    existing_owner = {}
    own_rows = psql_tsv(args.target_container, args.target_user, args.target_db,
                        "SELECT canonical_id::text, lower(canonical_name) FROM market_price_reference"
                        " WHERE canonical_id IS NOT NULL")
    for cid, name in own_rows:
        existing_owner.setdefault(cid, name)

    # ★ 分类在纯函数里（plan_rows）：拒绝判定 + canonical 解析 + 批内/跨日属主回退。
    landable, rejects = plan_rows(src_rows, resolve, existing_owner)
    upserts, landed = [], 0
    fam_count = {"resolved": 0, "unresolved": 0, "ambiguous": 0}
    fx_missing_rows = 0
    for row in landable:
        d = derive_dual_floors(row, fx)
        if d["fx_missing"]:
            fx_missing_rows += 1
        fam_count[row[18]] += 1
        landed += 1
        upserts.append(build_upsert_sql(row, d, args.dimension, fx))

    # ★ 台账口径（2026-11-10 审计 P2-C）：
    #   source_rows  = 源侧候选全集（len(src_rows)）
    #   landed_rows  = 实际写入 market_price_reference 的行数
    #   rejected_rows= 拒绝族计数之和（no_price / no_canonical_name / no_source_run_date）
    #   ⇒ 恒等式 source_rows = landed_rows + rejected_rows **构造上闭合**
    #     （每个源侧候选要么落地、要么被拒，且只归入其中一侧）。
    #   ⇒ 旧实现里源侧 SQL 预过滤掉了「无价」候选 ⇒ rejected 恒 0 ⇒ 空恒等式。
    rejected = sum(rejects.values())

    # 歧义守卫由目标事务内的 SQL NULL-out 完成：同一 (canonical_id, dimension)
    # 出现两行（两个源 key 解析到同一网关 canonical）时，全部回退 NULL——
    # 940 部分唯一索引 uq_mpr_canonical_id 的硬约束，绝不能违反。
    ambiguity_nullout = ""
    if args.dimension:
        ambiguity_nullout = (
            "UPDATE public.market_price_reference mpr SET canonical_id = NULL"
            " WHERE mpr.dimension = %s AND mpr.canonical_id IS NOT NULL"
            "   AND EXISTS (SELECT 1 FROM public.market_price_reference other"
            "               WHERE other.dimension = mpr.dimension"
            "                 AND other.canonical_id = mpr.canonical_id"
            "                 AND other.canonical_name <> mpr.canonical_name"
            "                 AND other.canonical_id IS NOT NULL);" % qstr(args.dimension))

    # ── 5) 目标事务（含红线锚点自检） ────────────────────────────────────────
    today = date.today().isoformat()
    conflicts = {"canonical_unresolved": fam_count["unresolved"],
                 "canonical_ambiguous": fam_count["ambiguous"]}
    detail = {
        "scope": args.scope, "dimension": args.dimension, "unit": args.unit,
        "as_of": args.as_of or today, "fx_usd_cny": fx,
        "exclude_platforms": args.exclude_platform,
        "source_container": args.source_container,
        # ★ 拒绝族明细（2026-11-10 审计 P2-C）：拒绝**为什么**拒绝，
        #   要能被台账读到，不能只有一个总数。
        "rejected_by_reason": rejects,
        # ★ A-17 平台级排除与币种冲突（2026-11-12）：不静默，逐平台可对账。
        "a17_unmapped_rows": unmapped_total,
        "a17_unmapped_by_platform": unmapped_by_platform,
        "a17_excluded_rows": excluded_total,
        "a17_excluded_by_platform": excluded_by_platform,
        "a17_currency_mismatch_rows": mismatch_total,
        "a17_currency_mismatch_by_platform": mismatch_by_platform,
        "a17_overseas_platforms": sum(1 for r, _n, e in reg.values() if r == "overseas" and not e),
        "a17_domestic_platforms": sum(1 for r, _n, e in reg.values() if r == "domestic" and not e),
        "a17_fx_missing_rows": fx_missing_rows,
    }
    ledger = ("INSERT INTO public.market_price_reference_sync"
              " (run_date, source_platforms, source_rows, landed_rows, rejected_rows, conflicts, detail)"
              " VALUES (current_date, %d, %d, %d, %d, %s::jsonb, %s::jsonb);" % (
                  source_platforms, len(src_rows), landed, rejected,
                  qstr(json.dumps(conflicts)),
                  qstr(json.dumps(detail))))

    anchor = "" if args.dry_run else "SELECT count(*) FROM public.model_credit_rates;"
    anchor_after = "" if args.dry_run else "SELECT count(*) FROM public.model_credit_rates;"

    sql_script = "\n".join([
        "BEGIN;",
        "SET LOCAL lock_timeout = '5s';",
        "SET LOCAL statement_timeout = '10min';",
        anchor,
    ] + upserts + [
        ambiguity_nullout,
        "UPDATE public.market_price_reference mpr SET canonical_id = mc.id"
        " FROM public.models_canonical mc"
        " WHERE mpr.canonical_id IS NULL AND mpr.dimension = %s"
        "   AND lower(mc.canonical_name) = lower(mpr.canonical_name)"
        "   AND NOT EXISTS (SELECT 1 FROM public.market_price_reference other"
        "                   WHERE other.dimension = mpr.dimension"
        "                     AND other.canonical_id = mc.id"
        "                     AND other.canonical_name <> mpr.canonical_name);" % qstr(args.dimension),
        ledger,
        anchor_after,
        "COMMIT;",
    ])

    print("landed=%d rejected=%d conflicts=%s rejects=%s fx_missing_rows=%d"
          % (landed, rejected, conflicts, rejects, fx_missing_rows))
    if args.dry_run:
        print("DRY-RUN: %d upserts generated (not written)" % len(upserts))
        for stmt in upserts[:3]:
            print("  sample:", stmt[:160], "...")
        return 0

    if args.dump_sql:
        with open(args.dump_sql, "w", encoding="utf-8") as fh:
            fh.write(sql_script)
        print("sql dumped: %s (%d lines)" % (args.dump_sql, sql_script.count("\n") + 1))

    mcr_before = None
    if anchor:
        mcr_before = psql_tsv(args.target_container, args.target_user, args.target_db, anchor)[0][0]
    proc = psql_exec(args.target_container, args.target_user, args.target_db, sql_script)
    if proc.returncode != 0:
        die("target psql failed: %s" % proc.stderr.strip()[:800])
    if anchor_after:
        mcr_after = psql_tsv(args.target_container, args.target_user, args.target_db, anchor_after)[0][0]
        if mcr_before != mcr_after:
            die("RED-LINE VIOLATION: model_credit_rates row count changed (%s -> %s)"
                % (mcr_before, mcr_after))
    print("target: upserted %d rows (dimension=%s), ledger row appended, red-line anchor OK"
          % (landed, args.dimension))
    return 0


if __name__ == "__main__":
    sys.exit(main())
