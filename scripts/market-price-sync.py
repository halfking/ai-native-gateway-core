#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""market-price-sync.py —— salesdecorate 行情 → 网关 940 市场参考价【单向】同步。

用法（在 211 宿主上执行；两侧 DB 都只经 docker exec psql 访问，零端口依赖）：
  python3 scripts/market-price-sync.py [--dry-run] [--as-of YYYY-MM-DD]
      [--scope 标准按量] [--dimension standard] [--unit per_1M_tokens]
      [--exclude-platform ID ...] [--source-container wa-pg17]
      [--target-container acc-pg17]

红线（脚本自身强制，见 2026-11-03 方案档 §4.4 与 940 迁移头注）：
  · 只写 market_price_reference / market_price_reference_sync（940 行情面）；
    绝不触碰 model_credit_rates / model_price_versions /
    credential_model_bindings（路由与计费面）——行情价禁止回流路由。
    脚本在目标事务前后对 model_credit_rates 做行数锚点自检，不等则中止。
  · 幂等：同键 upsert（ON CONFLICT DO UPDATE），台账追加可对账
    （源条数 = 落地 + 拒绝）。
  · NULL≠0：源侧无价保持 NULL（未知 ≠ 免费红线）。

同步口径（镜像 salesdecorate CompareByModel，backend/internal/store/market.go:506）：
  · 每 (platform, raw_model, scope, tier, region, currency, unit) 取最新
    run_date 行（DISTINCT ON）；
  · P2 市场最低 = is_vendor=false 平台的 min(CNY)；P1 原厂锚 = is_vendor=true
    平台的 min(CNY)；fx 取源库 fx_rate 最新 USD→CNY；
  · dimension = price_scope 归一（默认 standard=「标准按量」；条件价不压平）。

台账口径（2026-11-10 审计 P2-C 修）：
  · **源侧候选**（含「源侧有行但无市场价」的缺价行）全部进 src_rows
    ⇒ source_rows = len(src_rows)；
  · 缺价 / 无名 / 无出价日期 ⇒ **拒绝**（不进 940 目标表）⇒ rejected_rows；
  · 其余 ⇒ **落地**（写 market_price_reference）⇒ landed_rows；
  · ⇒ **恒等式：source_rows = landed_rows + rejected_rows**（构造上闭合）。
    ★ 旧实现在**源 SQL 的 WHERE** 里就把缺价行滤掉了 ⇒ rejected_rows 恒 0
      ⇒ 「源=落地+拒绝」是空恒等式 ⇒ 台账没有任何信号。
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
      canonical_name / source_url 都是 `NOT NULL DEFAULT ''` ⇒ 源侧 NULL
      必须落成 ''（**不是** SQL NULL）⇒ 否则整笔事务 NOT NULL 违例 abort。
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


def plan_rows(src_rows, resolve, existing_owner):
    """把源侧候选行分成【可落地】与【拒绝】两族（**纯函数**：零 IO ⇒ 可被单测直调）。

    返回 (landable, rejects)：
      landable = [(key, min_in, min_out, plat_cnt, min_platform, vendor_in,
                   vendor_out, vendor_name, max_run_date, cid, family, final_cid)]
      rejects  = {"no_price": n, "no_canonical_name": n, "no_source_run_date": n}

    ★ 拒绝族只收「**写不出合法行**」的候选：
        · no_price          —— 源侧有该模型，但市场最低价 in/out **都是** NULL
                               （未知 ≠ 免费 ⇒ 绝不能折 0，也绝不能写空行）；
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
        (key, min_in, min_out, plat_cnt, min_platform,
         vendor_in, vendor_out, vendor_name, max_run_date) = row[:9]
        # ── 拒绝族判定（先于解析）────────────────────────────────────────
        if key is None or str(key).strip() == "":
            rejects["no_canonical_name"] += 1
            continue
        if min_in is None and min_out is None:
            rejects["no_price"] += 1
            continue
        if max_run_date is None:
            rejects["no_source_run_date"] += 1
            continue
        cid, family = resolve(key)
        if cid is not None:
            claims.setdefault(cid, set()).add(key.lower())
        staged.append((key, min_in, min_out, plat_cnt, min_platform,
                       vendor_in, vendor_out, vendor_name, max_run_date, cid, family))

    landable = []
    for (key, min_in, min_out, plat_cnt, min_platform, vendor_in,
         vendor_out, vendor_name, max_run_date, cid, family) in staged:
        final_cid = cid
        if cid is not None:
            if len(claims.get(cid, ())) > 1:
                # 批内两个源 key 解析到同一网关 canonical（别名）⇒ 全部回退 NULL，
                # 否则撞 uq_mpr_canonical_id（首跑实证：canonical 352 双声明）。
                final_cid = None
                family = "ambiguous"
            elif existing_owner.get(cid) not in (None, key.lower()):
                # 目标已有别的 canonical_name 占有该 id（跨日属主漂移）⇒ 回退。
                final_cid = None
                family = "ambiguous"
        landable.append((key, min_in, min_out, plat_cnt, min_platform, vendor_in,
                         vendor_out, vendor_name, max_run_date, cid, family, final_cid))
    return landable, rejects


def build_upsert_sql(row, dimension, fx):
    """生成一条 market_price_reference 的 upsert（940 列序，见 940 迁移 DDL）。

    ★ NOT NULL 文本列一律走 qstr_notnull（源侧 NULL ⇒ ''）⇒ 绝不触发
      NOT NULL 违例把整笔事务 abort 掉。
    """
    (key, min_in, min_out, plat_cnt, min_platform, vendor_in,
     vendor_out, vendor_name, max_run_date, cid, family, final_cid) = row
    return (
        "INSERT INTO public.market_price_reference (canonical_name, dimension, currency, canonical_id,"
        " vendor_list_in_per_1m, vendor_list_out_per_1m, vendor_platform_id, vendor_vendor,"
        " market_min_in_per_1m, market_min_out_per_1m, market_min_platform_id, market_min_platform_count,"
        " market_min_cny_in_per_1m, market_min_cny_out_per_1m, fx_rate, source_run_date, source_confidence,"
        " source_url, updated_at)"
        " VALUES (%s, %s, 'CNY', %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s::date, 'A', '', now())"
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
        " fx_rate = EXCLUDED.fx_rate, source_run_date = EXCLUDED.source_run_date,"
        " updated_at = now();" % (
            qstr(key), qstr(dimension),
            (final_cid if final_cid else "NULL"),
            num(vendor_in), num(vendor_out), qstr_notnull(""), qstr_notnull(vendor_name),
            num(min_in), num(min_out), qstr_notnull(min_platform),
            (plat_cnt if plat_cnt else "0"),
            num(min_in), num(min_out), num(fx), qstr(max_run_date)))


def main():
    ap = argparse.ArgumentParser(description="salesdecorate → gateway market price sync (one-way)")
    ap.add_argument("--source-container", default="wa-pg17")
    ap.add_argument("--source-user", default="salesdecorate")
    ap.add_argument("--source-db", default="salesdecorate")
    ap.add_argument("--target-container", default="acc-pg17")
    ap.add_argument("--target-user", default="acc_super")
    ap.add_argument("--target-db", default="llm_gateway")
    ap.add_argument("--scope", default=SCOPE_STANDARD, help="price_scope（中文原值）")
    ap.add_argument("--dimension", default="standard", help="940 dimension 归一值")
    ap.add_argument("--unit", default="per_1M_tokens")
    ap.add_argument("--as-of", default="", help="只取 run_date <= as-of（默认今天）")
    ap.add_argument("--exclude-platform", nargs="*", default=[],
                    help="从市场最低价中排除的平台（A-5 排除我方平台；当前源无我方平台）")
    ap.add_argument("--dry-run", action="store_true", help="只打印将要落地的行数与样例，不写目标")
    ap.add_argument("--dump-sql", default="", help="把目标事务 SQL 原样写到该路径（调试用）")
    args = ap.parse_args()

    # ── 1) 源侧聚合（镜像 CompareByModel 的 latest-row 语义） ────────────────
    exclude_cond = ""
    if args.exclude_platform:
        ids = ", ".join(qstr(p) for p in args.exclude_platform)
        exclude_cond = " AND p.platform_id <> ALL(ARRAY[%s])" % ids
    # ★ 空 as-of = 不过滤（run_date <= NULL::date 会把全部行判 NULL 杀光——
    #   首跑实测踩到，勿回退成三元拼 NULL 的写法）。
    asof_cond = (" AND o.run_date <= %s::date" % qstr(args.as_of)) if args.as_of else ""
    src_sql = """
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
           o.cny_input::float8 AS cny_in, o.cny_output::float8 AS cny_out, o.run_date
    FROM latest l
    JOIN price_observation o ON o.id = l.id
    JOIN platform p ON p.platform_id = o.platform_id
    JOIN model m ON m.id = o.model_id{exclude}
),
agg AS (
    SELECT canonical_key,
           min(cny_in)  FILTER (WHERE NOT is_vendor) AS min_in,
           min(cny_out) FILTER (WHERE NOT is_vendor) AS min_out,
           count(DISTINCT platform_id) FILTER (WHERE NOT is_vendor) AS plat_cnt,
           (ARRAY_AGG(platform_id ORDER BY cny_out NULLS LAST)
              FILTER (WHERE NOT is_vendor))[1] AS min_platform,
           min(cny_in)  FILTER (WHERE is_vendor) AS vendor_in,
           min(cny_out) FILTER (WHERE is_vendor) AS vendor_out,
           (ARRAY_AGG(vendor ORDER BY cny_out NULLS LAST)
              FILTER (WHERE is_vendor))[1] AS vendor_name,
           max(run_date)::text AS max_run_date
    FROM rows_
    GROUP BY canonical_key
)
SELECT canonical_key, min_in, min_out, plat_cnt, min_platform,
       vendor_in, vendor_out, vendor_name, max_run_date
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
ORDER BY canonical_key
""".format(scope_q=qstr(args.scope), unit_q=qstr(args.unit),
           asof=asof_cond, exclude=exclude_cond)
    src_rows = psql_tsv(args.source_container, args.source_user, args.source_db, src_sql)
    # source_platforms = 源侧本 scope 出现过的独立平台数（全量，未排除前）
    plat_rows = psql_tsv(args.source_container, args.source_user, args.source_db,
                         "SELECT count(DISTINCT platform_id) FROM price_observation WHERE price_scope = %s"
                         % qstr(args.scope))
    source_platforms = int(plat_rows[0][0]) if plat_rows else 0
    print("source: %d canonical rows (scope=%s, unit=%s), %d platforms in scope"
          % (len(src_rows), args.scope, args.unit, source_platforms))

    # ── 2) fx 快照（源库最新 USD→CNY） ──────────────────────────────────────
    fx = None
    fx_rows = psql_tsv(args.source_container, args.source_user, args.source_db,
                       "SELECT rate::text FROM fx_rate WHERE base='USD' AND quote='CNY' "
                       "ORDER BY retrieved_at DESC LIMIT 1")
    if fx_rows:
        fx = fx_rows[0][0]
    print("fx USD→CNY = %s" % (fx or "NULL"))

    # ── 3) 目标侧 canonical 解析（两趟：精确小写 → 归一化；歧义进 conflicts） ──
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
    for row in landable:
        (key, min_in, min_out, plat_cnt, min_platform, vendor_in,
         vendor_out, vendor_name, max_run_date, cid, family, final_cid) = row
        fam_count[family] += 1
        landed += 1
        upserts.append(build_upsert_sql(row, args.dimension, fx))

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

    # ── 4) 目标事务（含红线锚点自检） ────────────────────────────────────────
    today = date.today().isoformat()
    conflicts = {"canonical_unresolved": fam_count["unresolved"],
                 "canonical_ambiguous": fam_count["ambiguous"]}
    ledger = ("INSERT INTO public.market_price_reference_sync"
              " (run_date, source_platforms, source_rows, landed_rows, rejected_rows, conflicts, detail)"
              " VALUES (current_date, %d, %d, %d, %d, %s::jsonb, %s::jsonb);" % (
                  source_platforms, len(src_rows), landed, rejected,
                  qstr(json.dumps(conflicts)),
                  qstr(json.dumps({
                      "scope": args.scope, "dimension": args.dimension, "unit": args.unit,
                      "as_of": args.as_of or today, "fx_usd_cny": fx,
                      "exclude_platforms": args.exclude_platform,
                      "source_container": args.source_container,
                      # ★ 拒绝族明细（2026-11-10 审计 P2-C）：拒绝**为什么**拒绝，
                      #   要能被台账读到，不能只有一个总数。
                      "rejected_by_reason": rejects}))))

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

    print("landed=%d rejected=%d conflicts=%s rejects=%s"
          % (landed, rejected, conflicts, rejects))
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
