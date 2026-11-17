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
import hashlib
import json
import os
import re
import subprocess
import sys
from datetime import date

SCOPE_STANDARD = "标准按量"

# price_scope（源侧中文原值）→ 网关 dimension（归一值）。
# ★ 标准按量是唯一进 940 地板的口径；其余口径（限时优惠/批量/思考…）
#   **只进 970 平台现价明细** —— 促销价进「市场地板」会把地板系统性拉低，
#   等于把「最便宜的常规价」错报成「市场价」（A-17 双地板红线的延伸）。
SCOPE_DIMENSION = {
    "标准按量": "standard",
    "限时优惠": "promo",
    "限时免费": "promo_free",
    "批量(Batch)": "batch",
    "思考": "thinking",
    "非思考": "non_thinking",
    "包月/套餐": "subscription",
    "专属/预留": "reserved",
}


def dimension_for_scope(scope):
    """price_scope → dimension；未登记的 scope 归到 other_<hash>（不丢数据）。"""
    if scope in SCOPE_DIMENSION:
        return SCOPE_DIMENSION[scope]
    return "other_" + hashlib.sha256(scope.encode("utf-8")).hexdigest()[:8]


def script_sha256():
    """本脚本自身的 sha256（写进台账 ⇒ 运维一眼看出「跑的是哪个版本」）。

    ★ 动机（2026-11-17 211 实测）：宿主 cron 跑的是 /root/market-price-sync.py，
      它与仓库副本**长期漂移**（仓库已有 970 写入段、宿主副本没有）⇒
      平台现价表恒空、而台账看起来一切正常。
      ⇒ 台账自带脚本指纹 ⇒ 漂移**当场可读**，不必靠翻文件。
    """
    try:
        with open(os.path.abspath(__file__), "rb") as fh:
            return hashlib.sha256(fh.read()).hexdigest()
    except OSError:
        return ""


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


# ── 模型身份对齐（2026-11-17 定价对比完善轮）───────────────────────────────
#
# ★ 为什么必须做（211 真库实测，2026-11-17，详见审计主档 F2/F3）：
#   salesdecorate 的 canonical_key 由**平台展示名**归一而来，而展示名带厂商
#   前缀（`openai:-gpt-4o` / `z.ai:-glm-5.3` / `baai:bge-m3`）⇒ 采集侧
#   1246 个 key 里 **365 个带命名空间前缀**（占观测 42.8%）。
#   网关侧 models_canonical 用的是**裸模型名**（`gpt-4o` / `glm-5.3`）。
#   ⇒ 两边名字空间**不同源** ⇒ 旧的两趟解析（小写精确 → 去 `_- . ` 归一）
#     **永不命中带前缀的 key**（normalize 不剥 ':'）⇒ 实测解析率仅 **24.3%**。
#   ⇒ 而评分面 supplier_market_position 存的是**网关 canonical 名** ⇒
#     参考价与评分行的名字交集只有 218/1746 = **12.5%** ⇒
#     绝大多数「我方价格 vs 市场地板」**没有地板可比**（对账页空得离谱的根因）。
#   ⇒ 本轮把「剥命名空间」加进解析链：实测解析率 24.3% → **62.3%**（观测口径）。
#
# ★ 顺序铁律：**原样精确优先于剥前缀精确**。
#   网关自己有 10 个 Ollama 风格 canonical 名（`qwen2.5:7b` /
#   `claude-opus-5-5:high` / `qwen3-embedding:0.6b` …）⇒ 若一律先剥前缀，
#   这些名会被剥成 `7b` / `high` / `0.6b` 而**误命中别的模型**。
#   ⇒ 原样精确命中时直接采信，不剥。

_NAMESPACE_TAIL = re.compile(r"\{\}$")


def strip_namespace(key):
    """剥掉采集侧的厂商命名空间前缀 ⇒ 裸模型名。

    形如 `openai:-gpt-4o` ⇒ `gpt-4o`；`z.ai:-glm-5.3` ⇒ `glm-5.3`；
    `baai:bge-m3` ⇒ `bge-m3`。
    ★ 同时去掉源侧偶发的 `{}` 噪声尾巴（211 实测 `doubao-seed-2.1-pro{}`）。
    ★ 只在「冒号后有内容」时才剥 ⇒ `foo:` 不会被剥成空串。
    """
    k = (key or "").strip()
    k = _NAMESPACE_TAIL.sub("", k).strip()
    if ":" in k:
        tail = k.split(":", 1)[1].lstrip("- ")
        if tail:
            return tail
    return k


def resolve_canonical(key, canon_exact, canon_norm, canon_names):
    """解析源侧 canonical_key ⇒ 网关 canonical（**纯函数**：零 IO ⇒ 可单测）。

    返回 (cid | None, gw_name | None, family)：
      cid     —— 网关 models_canonical.id（未命中 None）
      gw_name —— 网关 canonical_name（未命中 None）
      family  —— resolved | unresolved | ambiguous

    ★ 四趟顺序（铁律见上）：原样精确 → 剥前缀精确 → 原样归一 → 剥前缀归一。
    ★ ambiguous ⇒ 保守不解析（fail-closed）：归一后命中多个网关 canonical 时
      不猜，改由台账计数暴露（canonical_ambiguous）。
    """
    bare = strip_namespace(key)
    pairs = [(key, False)]
    if bare != key:
        pairs.append((bare, True))
    for cand, _ns in pairs:
        ids = canon_exact.get(cand.lower(), [])
        if len(ids) == 1:
            return ids[0], canon_names.get(ids[0], cand), "resolved"
        if len(ids) > 1:
            return None, None, "ambiguous"
    for cand, _ns in pairs:
        ids = canon_norm.get(normalize(cand), [])
        if len(ids) == 1:
            return ids[0], canon_names.get(ids[0], cand), "resolved"
        if len(ids) > 1:
            return None, None, "ambiguous"
    return None, None, "unresolved"


# 非模型拒绝族（2026-11-17）：源侧把**上下文阶梯标签 / 图片分辨率档**当成了
# 模型名（211 实测：`128k<token≤256k` / `256k<token≤1m` / `32k<token≤128k` /
# `720p` / `480p` / `768p` / `1080p`）⇒ 这些行会污染参考表，并在对账页上
# 显示成「市场上有这个模型」的**假信号**。
#
# ★ 判据只在**未解析到网关 canonical**时生效 —— 已解析的行是真模型，一律保留
#   （网关自己也服务 embedding / rerank 模型 ⇒ 不按类别一刀切丢弃）。
# ★ 不按「可比类别白名单」丢弃（salesdecorate 的
#   `model_kind ∈ {文本/对话, 多模态, 推理(深度思考)}` 口径）—— 那会把
#   `bge-m3` / `text-embedding-3` 这类**网关确实在用**的模型价格一起丢掉
#   （211 实测网关有 19 个 embedding/rerank canonical 名）。
NOT_A_MODEL_KINDS = {"图像生成", "语音(ASR/TTS)", "视频生成", "OCR", "其它"}
_TIER_MARKERS = ("≤", "≥", "<", ">")

# model_kind 的「可比性」序（合并同 canonical 时取最高者作为该 canonical 的类别）。
# ★ 排序依据＝「网关自己是否可能路由这类模型」：文本/对话 > 多模态 > 推理 >
#   向量/Embedding > 重排/Rerank > 其它。
_KIND_RANK = {
    "文本/对话": 6,
    "多模态": 5,
    "推理(深度思考)": 4,
    "向量/Embedding": 3,
    "重排/Rerank": 2,
    "图像生成": 1,
}


def _kind_rank(kind):
    """model_kind 的可比性序（未知类别 ⇒ 0）。"""
    return _KIND_RANK.get(kind or "", 0)


def is_not_a_model(name, model_kind, cid):
    """True ⇒ 该行不是模型（阶梯/分辨率标签），应当**拒绝**而不是落地。

    cid 非 None ⇒ 已解析到网关 canonical ⇒ 是真模型 ⇒ False。
    """
    if cid is not None:
        return False
    n = name or ""
    if any(m in n for m in _TIER_MARKERS):
        return True                      # 上下文阶梯标签（`128k<token≤256k`）
    if not any(ch.isalpha() for ch in n):
        return True                      # 纯数字/符号（分辨率档 `720p` 的极端形态）
    return model_kind in NOT_A_MODEL_KINDS



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


def _plat_order_key(pr):
    """970 明细**同表键多行**时的确定性择优键（越小越优先）。

    ★ 为什么需要：别名收敛（多个源 key → 同一 out_name）与源侧同键多行
      观测会让 (dimension, canonical_name, platform_id, tier, source_region,
      currency) 出现多行 ⇒ 表是带主键的，必须**只留一行**。
      而「留哪一行」若不定死 ⇒ 每次运行结果可能不同 ⇒ 不可复现、不可审计。

    ★ 择优口径＝**地板口径**（与 940 地板同源）：CNY 输入价低者优先 ⇒
      同一平台同一模型若有多个别名报价不一致，取**最低**那档（市场地板语义）；
      CNY 价相同时取输出价低者；再相同取 run_date **最新**者。
    ★ NULL≠0：CNY 价缺失（None）排在已知价**之后**（未知不等于便宜）。
    """
    cny_in = numf(pr[8])
    cny_out = numf(pr[9])
    run = (pr[12] or "").strip()
    try:
        run_key = -int(run.replace("-", ""))     # ISO 日期：越新越靠前
    except (ValueError, AttributeError):
        run_key = 0
    return (0 if cny_in is not None else 1,
            cny_in if cny_in is not None else 0.0,
            0 if cny_out is not None else 1,
            cny_out if cny_out is not None else 0.0,
            run_key)


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


def plan_rows(src_rows, resolve, existing_owner, resolved_map=None):
    """把源侧候选行分成【可落地】与【拒绝】两族（**纯函数**：零 IO ⇒ 可被单测直调）。

    src_rows 列序（17 列，与 psql_tsv 输出一致）：
      0 out_name, 1 ov_in, 2 ov_out, 3 ov_cnt, 4 ov_platform,
      5 dm_in, 6 dm_out, 7 dm_cnt, 8 dm_platform,
      9 vd_cur, 10 vd_in, 11 vd_out, 12 vd_cny_in, 13 vd_cny_out,
      14 vd_name, 15 vd_platform, 16 max_run_date
    ★ out_name = **落地用 canonical_name**：
      · 已解析 ⇒ 网关 canonical 名（与评分面 supplier_market_position 同名空间
        ⇒ 对账页的「双地板对拍」才真的对得上）；
      · 未解析 ⇒ 剥掉厂商命名空间前缀后的源名（`openai:-gpt-4o` ⇒ `gpt-4o`）。

    返回 (landable, rejects)：
      landable = 21 元组（上述 17 列 + cid + family + model_kind + final_cid）
      rejects  = {"no_price": n, "no_canonical_name": n,
                  "no_source_run_date": n, "not_a_model": n}

    ★ 拒绝族只收「**写不出合法行**」的候选：
        · no_price          —— 厂商价与双地板**全部**缺价（A-17：市场面与
                               原厂面都未知 ⇒ 绝不能折 0，也绝不能写空行）；
        · no_canonical_name —— 没有 out_name（写不出主键）；
        · no_source_run_date—— 没有出价日期（source_run_date 是 NOT NULL）；
        · not_a_model       —— 采集侧把**上下文阶梯标签 / 图片分辨率档**当成了
                               模型名（`128k<token≤256k` / `720p`）⇒ 落地
                               只会在对账页上造出「市场上有这个模型」的假信号。
    ⇒ 拒绝判定**先于** canonical 解析：被拒的行不该占用 canonical 声明。

    ★ canonical 解析不出来（unresolved/ambiguous）的行**仍然落地**
      （canonical_id=NULL，如实保留 NULL），只在 conflicts 里计数
      ⇒ landed_rows == 实际写入条数 ⇒ 「源=落地+拒绝」是**可对账**的等式。

    ★ resolved_map（2026-11-17 新增）：{out_name: (cid, family, model_kind)}。
      解析与**同名合并**已在采集 SQL 侧完成（源侧 key 灌进 res CTE ⇒
      `GROUP BY out_name` ⇒ 多个源 key 别名（如 `glm-5.3` + `z.ai:-glm-5.3`）
      **合并成一行**，而不是各落一行互相覆盖）。
      ⇒ 旧实现在 Python 侧把「多 key 撞同一 canonical」误判成**歧义**并
        全部回退 canonical_id=NULL（211 实测 165 个 canonical / 43.3% 观测）
        ⇒ 合并后地板与归属平台一次算对，平台数也精确（count DISTINCT）。
      ⇒ resolved_map 为 None 时走**旧路径**（resolve 回调）⇒ 兼容既有单测。
    """
    rejects = {"no_price": 0, "no_canonical_name": 0,
               "no_source_run_date": 0, "not_a_model": 0}
    claims = {}   # canonical_id -> set(lower out_name)（批内声明）
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
        if resolved_map is not None:
            cid, family, model_kind = resolved_map.get(
                key, (None, "unresolved", ""))
        else:
            cid, family = resolve(key)
            model_kind = ""
        if is_not_a_model(key, model_kind, cid):
            rejects["not_a_model"] += 1
            continue
        if cid is not None:
            claims.setdefault(cid, set()).add(key.lower())
        staged.append(list(row[:17]) + [cid, family, model_kind])

    landable = []
    for row in staged:
        cid, family, model_kind = row[17], row[18], row[19]
        final_cid = cid
        if cid is not None:
            if len(claims.get(cid, ())) > 1:
                # 批内两个 out_name 解析到同一网关 canonical ⇒ 全部回退 NULL。
                # ★ 2026-11-17：改名合并后**正常路径不再命中这里**（同名 key
                #   在 SQL 侧已合并成一行）⇒ 这条只兜底「同名不同形」的异常。
                final_cid = None
                family = "ambiguous"
            elif existing_owner.get(cid) not in (None, row[0].lower()):
                # 目标已有别的 canonical_name 占有该 id（跨日属主漂移）⇒ 回退。
                final_cid = None
                family = "ambiguous"
        landable.append(row[:17] + [cid, family, model_kind, final_cid])
    return landable, rejects


def build_upsert_sql(row, d, dimension, fx):
    """生成一条 market_price_reference 的 upsert（940+959 列序，36 列）。

    row = plan_rows 产出的 21 元组（17 源列 + cid + family + model_kind +
          final_cid）；
    d   = derive_dual_floors() 的 A-17 折算结果；fx = 快照汇率字面量。

    ★ NOT NULL 文本列一律走 qstr_notnull（源侧 NULL ⇒ ''）⇒ 绝不触发
      NOT NULL 违例把整笔事务 abort 掉。
    ★ currency（940 原列）= 综合地板币种 = 'CNY'（959 COMMENT 钉死的落地口径；
      定稿「vendor_list_currency 镜像」的有意偏离 ⇒ 厂商原生币在列 14）。
    ★ row[0] 现在是 **out_name**（已解析 ⇒ 网关 canonical 名；未解析 ⇒ 剥前缀
      源名）⇒ 落地后参考表与评分面 supplier_market_position **同一名字空间**
      ⇒ 对账页的 LATERAL 对拍（按 canonical_name）才真的对得上。
    """
    (key, ov_in, ov_out, ov_cnt, ov_platform,
     dm_in, dm_out, dm_cnt, dm_platform,
     vd_cur, vd_in, vd_out, vd_cny_in, vd_cny_out, vd_name, vd_platform,
     max_run_date, cid, family, model_kind, final_cid) = row[:21]
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
    # ★ boolean::text 渲染为 'true'/'false'（裸列 -tA 才是 't'/'f'）——
    #   按 't' 判会把全部平台误判成 excluded ⇒ 地板全空（211 dry-run 实测）。
    for pid, region, native, is_ours, enabled in reg_rows:
        reg[pid] = (region, native, is_ours == "true" or enabled != "true")
    reg_values = ", ".join(
        "(%s, %s, %s, %s)" % (qstr(pid), qstr(region), qstr(native),
                              "true" if excluded else "false")
        for pid, (region, native, excluded) in sorted(reg.items()))
    if not reg_values:
        die("market_platform_registry 为空 ⇒ 区域裁定无权威（958 种子缺失？）")

    # ── 2) 采集过滤条件（先于一切取数：解析面与聚合面必须同源）────────────
    exclude_cond = ""
    if args.exclude_platform:
        ids = ", ".join(qstr(p) for p in args.exclude_platform)
        exclude_cond = " AND p.platform_id <> ALL(ARRAY[%s])" % ids
    # ★ 空 as-of = 不过滤（run_date <= NULL::date 会把全部行判 NULL 杀光——
    #   首跑实测踩到，勿回退成三元拼 NULL 的写法）。
    asof_cond = (" AND o.run_date <= %s::date" % qstr(args.as_of)) if args.as_of else ""
    # 940 聚合只看标准按量；970 明细看全部 scope（见 2a）⇒ 两个 CTE 变体。
    scope_cond = "o.price_scope = %s" % qstr(args.scope)
    scope_cond_all = "TRUE"

    # ── 3) 模型身份对齐（2026-11-17 定价对比完善轮）────────────────────────
    # ★ 这是「对账页空得离谱」的**根因修复**（211 真库实测，详见主档 F2/F3）：
    #   源侧 canonical_key 带厂商命名空间前缀（`openai:-gpt-4o`），
    #   网关 canonical 名是裸模型名 ⇒ 两边名字空间不同源 ⇒
    #   旧两趟解析解析率仅 24.3%，参考价与评分面名字交集仅 12.5%。
    #   ⇒ 本步把源侧 key 解析成**落地名 out_name**（已解析 ⇒ 网关 canonical 名），
    #     并把映射灌进源侧 SQL 的 res CTE ⇒ 聚合按 out_name 分组 ⇒
    #     **同 canonical 的多个源 key 别名合并成一行**（地板/平台数/归属一次算对）。
    #
    # 源侧 key 清单（scope + unit 与聚合一致 ⇒ 解析面与聚合面同源）：
    key_rows = psql_tsv(args.source_container, args.source_user, args.source_db,
                        "SELECT m.canonical_key, m.model_kind FROM price_observation o"
                        " JOIN model m ON m.id = o.model_id"
                        " WHERE o.price_scope = %s AND o.unit = %s%s GROUP BY 1, 2"
                        % (qstr(args.scope), qstr(args.unit), asof_cond))
    # 网关 canonical 字典（id ↔ name）：
    canon_rows = psql_tsv(args.target_container, args.target_user, args.target_db,
                          "SELECT id::text, canonical_name FROM models_canonical")
    canon_exact, canon_norm, canon_names = {}, {}, {}
    for cid, name in canon_rows:
        canon_exact.setdefault(name.lower(), []).append(cid)
        canon_norm.setdefault(normalize(name), []).append(cid)
        canon_names[cid] = name

    # 解析每个源 key ⇒ (out_name, cid, family, model_kind)
    resolved_map = {}          # out_name -> (cid, family, model_kind)
    res_pairs = []             # (src_key, out_name) —— 灌进 res CTE
    src_key_meta = {}          # src_key -> model_kind
    fam_counter = {"resolved": 0, "unresolved": 0, "ambiguous": 0}
    ns_stripped = 0
    for key, kind in key_rows:
        if key is None or str(key).strip() == "":
            continue
        src_key_meta[key] = kind or ""
        cid, gw_name, family = resolve_canonical(key, canon_exact, canon_norm, canon_names)
        if family == "resolved" and strip_namespace(key) != key:
            ns_stripped += 1
        bare = strip_namespace(key)
        out_name = gw_name if gw_name else bare
        if not out_name:
            continue
        res_pairs.append((key, out_name))
        # 同 out_name 合并时，model_kind 取「可比性最高」的那一个
        # （文本/对话 > 多模态 > 推理 > 向量 > 重排 > 其它）。
        prev = resolved_map.get(out_name)
        if prev is None:
            resolved_map[out_name] = (cid, family, src_key_meta[key])
        else:
            prev_cid, prev_family, prev_kind = prev
            if _kind_rank(src_key_meta[key]) > _kind_rank(prev_kind):
                resolved_map[out_name] = (prev_cid, prev_family, src_key_meta[key])
            # ★ cid 冲突（同 out_name 解析出不同 cid）⇒ 以先到者为准并计数，
            #   不静默覆盖（fail-closed：宁可少一个 cid，也别落一条错身份）。
    if res_pairs:
        res_values = ", ".join("(%s, %s)" % (qstr(k), qstr(v)) for k, v in res_pairs)
    else:
        res_values = "('', '')"

    # ── 4) 源侧聚合（镜像 CompareByModel 的 latest-row 语义 + A-17 双地板） ──
    latest_cte_tpl = """
WITH latest AS (
    SELECT DISTINCT ON (o.platform_id, o.raw_model_id, o.price_scope, o.tier, o.region, o.currency, o.unit)
        o.id
    FROM price_observation o
    WHERE {scope_cond}
      AND o.unit = {unit_q}{asof}
    ORDER BY o.platform_id, o.raw_model_id, o.price_scope, o.tier, o.region, o.currency, o.unit, o.run_date DESC
),
rows_ AS (
    SELECT m.canonical_key, p.platform_id, p.name AS platform_name,
           p.is_vendor, p.vendor,
           o.currency,
           o.input_price::float8  AS native_in,
           o.output_price::float8 AS native_out,
           o.cny_input::float8  AS cny_in,
           o.cny_output::float8 AS cny_out,
           -- ★ 2026-11-17：缓存命中价（源侧 cache_hit_price / cny_cache_hit）
           --   —— 缓存命中价是真实成本的大头（211 实测 49.7% 的观测有值），
           --   此前网关侧**一个列都没有** ⇒ 对比面看不到缓存成本。
           o.cache_hit_price::float8 AS cache_hit,
           o.cny_cache_hit::float8   AS cache_hit_cny,
           -- ★ 阶梯 / 类别 / 口径：tier（上下文阶梯、Priority、Flex…）、
           --   model_kind、price_scope（限时优惠 / 批量 / 思考…）。
           --   此前 tier 只活在采集侧 ⇒ 阶梯价在 940 聚合层被压平成最小值。
           COALESCE(o.tier, '')         AS tier,
           -- ★ 2026-11-17：**源侧 region（报价所属地区）必须带出**。
           --   211 真库实测：同一平台同一模型同一 tier 有 5 个 region
           --   （bailian / deepseek-v4-flash-0731 / 忙时 08:00-22:00：
           --     中国大陆 3.0 CNY、全球 3.0、美国/日本/新加坡 3.208）
           --   ⇒ 源侧观测键含 region（salesdecorate 的 ingest 去重键、
           --     price_hash 都含 region）⇒ 970 的表键也必须含它，
           --     否则要么撞主键、要么静默只留一档（价格差 7%）。
           COALESCE(o.region, '')       AS src_region,
           -- ★ 2026-11-17：**model_kind 统一取 canonical 模型的分类**（m.model_kind
           --   优先），不再优先取观测表的快照值（o.model_kind）。
           --   理由（211 真库实测）：o.model_kind 与 m.model_kind 在 6341/19181 行
           --   **不一致**（源侧曾把模型重分类过）⇒ 同一次运行里 970 明细写的是
           --   观测快照值、而「非模型拒绝门」与「同名合并取最高可比类别」用的是
           --   canonical 值 ⇒ **同一个模型在同一份产物里有两个类别** ⇒ 决策面与
           --   候选面判据不一致（与本轮 F6 同型：键一样、闸不一样）。
           --   ⇒ canonical 分类才是「这个模型是什么」的真源（m.canonical_key 唯一，
           --     211 实测 1381 个模型零重复）⇒ 全部改成 m 优先，o 仅作兜底。
           COALESCE(m.model_kind, o.model_kind, '') AS model_kind,
           COALESCE(o.price_scope, '')  AS price_scope,
           o.run_date,
           COALESCE(o.source_url, '') AS source_url
    FROM latest l
    JOIN price_observation o ON o.id = l.id
    JOIN platform p ON p.platform_id = o.platform_id
    JOIN model m ON m.id = o.model_id{exclude}
),
reg(platform_id, reg_region, reg_native, reg_excluded) AS (
    VALUES {reg_values}
),
-- ★ 2026-11-17 模型身份对齐：源侧 key → 落地名 out_name。
--   灌进来的每一个源 key 都有解析结果 ⇒ COALESCE 的 fallback 分支**永不**触发
--   ⇒ 「剥前缀」这件事只有 Python 一个实现（单点一致），SQL 不重复实现一遍。
res(src_key, out_name) AS (
    VALUES {res_values}
),
mapped AS (
    SELECT r.*, COALESCE(res.out_name, r.canonical_key) AS out_name,
           reg.reg_region, reg.reg_native, reg.reg_excluded
    FROM rows_ r
    LEFT JOIN res ON res.src_key = r.canonical_key
    LEFT JOIN reg ON lower(reg.platform_id) = lower(r.platform_id)
)"""
    # ★ format 必须挂在 latest_cte 的定义处（占位符都在这一段里）——挂到拼接后的
    #   第二段字面量上 ⇒ latest_cte 里的 {scope_q} 永远不被替换（211 dry-run
    #   实测红：psql syntax error at or near "{"）。
    # ★ 2026-11-17：同一模板 format **两次** —— 一次带 scope 过滤（940 聚合，
    #   只看标准按量），一次不带（970 平台明细，看全部 scope：限时优惠/批量/
    #   思考/包月…）。★ 明细的多 scope 只进 970，**绝不进 940 地板** ——
    #   促销价会把「市场地板」拉低 ⇒ 等于把「最便宜的常规价」错报成「市场价」。
    _ctpl_kwargs = dict(scope_q=qstr(args.scope), unit_q=qstr(args.unit),
                         asof=asof_cond, exclude=exclude_cond,
                         reg_values=reg_values, res_values=res_values)
    latest_cte = latest_cte_tpl.format(scope_cond=scope_cond, **_ctpl_kwargs)
    latest_cte_all = latest_cte_tpl.format(scope_cond=scope_cond_all, **_ctpl_kwargs)
    src_sql = latest_cte + """,
agg AS (
    SELECT out_name,
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
    -- ★★ 2026-11-17：**GROUP BY out_name**（不再是 canonical_key）——
    --   out_name 已是「落地身份」（已解析 ⇒ 网关 canonical 名）⇒
    --   同 canonical 的多个源 key 别名（`glm-5.3` + `z.ai:-glm-5.3`）
    --   **合并成一行**：地板取 min、平台数 count(DISTINCT platform_id) 精确、
    --   归属平台按组内全局最小重选 ⇒ 一次算对，不必在 Python 侧二次聚合。
    --   （旧口径在 Python 侧把这种收敛当「歧义」全部回退 canonical_id=NULL：
    --    211 实测 165 个 canonical / 43.3% 观测 ⇒ 大量模型拿不到可比地板。）
    GROUP BY out_name
)
SELECT out_name, ov_in, ov_out, ov_cnt, ov_platform,
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
--      （拒绝族：no_price / no_canonical_name / no_source_run_date / not_a_model）
--      ⇒ 源侧候选全集进 src_rows ⇒ 恒等式真正闭合。
-- ★★ 2026-11-12 A-17：平台级排除（unmapped / 我方 / 币种冲突）发生在
--    聚合 FILTER 层 ⇒ 候选仍进 src_rows 恒等式；逐平台行数由下面的
--    诊断查询补进台账 detail.a17_*，不静默。
ORDER BY out_name
"""

    # 诊断查询：本 scope 每平台×币种的行数（A-17 台账 detail 的数据源）。
    diag_sql = latest_cte + """
SELECT r.platform_id, r.currency, count(*)
FROM mapped r
GROUP BY 1, 2 ORDER BY 1, 2"""

    src_rows = psql_tsv(args.source_container, args.source_user, args.source_db, src_sql)
    # ── 4a) 平台现价明细（970：对齐 salesdecorate Compare 现价模式）──────────
    # mapped 已含每平台最新观测；此处另取一行集写 market_price_platform_quotes，
    # 不进 940 聚合恒等式（源=落地+拒绝仍只计 agg 行）。
    #
    # ★ 2026-11-17 三处改造：
    #   ① **身份对齐**：canonical_name 写 out_name（网关 canonical 名优先）
    #      ⇒ 970 明细、940 参考表、评分面 supplier_market_position **同一名字空间**
    #      ⇒ 对账页「按模型跨平台现价」能按网关模型名查到。
    #   ② **多 scope**：用 latest_cte_all（不过滤 scope）⇒ 限时优惠 / 批量 /
    #      思考 / 非思考 / 包月 / 限时免费 也落地（dimension 按 SCOPE_DIMENSION
    #      归一）。★ 只进 970，**绝不进 940 地板**（促销价会把地板拉低）。
    #   ③ **阶梯 + 缓存命中价**：tier 与 cache_hit 一并落地（972 加的列）⇒
    #      上下文阶梯价不再被压平成最小值；缓存命中价（真实成本大头）可见。
    #
    # 列序（20 列）：
    #  0 out_name 1 platform_id 2 platform_name 3 is_vendor 4 vendor
    #  5 currency 6 native_in 7 native_out 8 cny_in 9 cny_out
    # 10 region 11 native_currency 12 run_date 13 source_url
    # 14 tier 15 model_kind 16 cache_hit_native 17 cache_hit_cny
    # 18 price_scope 19 source_region
    plat_sql = latest_cte_all + """
SELECT out_name, platform_id, COALESCE(platform_name, ''),
       is_vendor::text, COALESCE(vendor, ''),
       COALESCE(currency, ''),
       native_in::text, native_out::text, cny_in::text, cny_out::text,
       COALESCE(reg_region, ''), COALESCE(reg_native, ''),
       run_date::text, COALESCE(source_url, ''),
       COALESCE(tier, ''), COALESCE(model_kind, ''),
       cache_hit::text, cache_hit_cny::text,
       COALESCE(price_scope, ''),
       COALESCE(src_region, '')
FROM mapped
WHERE out_name IS NOT NULL AND btrim(out_name) <> ''
  AND (native_in IS NOT NULL OR native_out IS NOT NULL
       OR cny_in IS NOT NULL OR cny_out IS NOT NULL
       OR cache_hit IS NOT NULL OR cache_hit_cny IS NOT NULL)"""
    plat_quote_rows = psql_tsv(args.source_container, args.source_user, args.source_db, plat_sql)
    # source_platforms = 源侧本 scope 出现过的独立平台数（全量，未排除前）
    plat_rows = psql_tsv(args.source_container, args.source_user, args.source_db,
                         "SELECT count(DISTINCT platform_id) FROM price_observation WHERE price_scope = %s"
                         % qstr(args.scope))
    source_platforms = int(plat_rows[0][0]) if plat_rows else 0
    print("source: %d canonical rows (scope=%s, unit=%s), %d platforms in scope, %d platform-quote rows"
          % (len(src_rows), args.scope, args.unit, source_platforms, len(plat_quote_rows)))

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

    # ── 5) fx 快照（源库最新 USD→CNY；折算唯一依据，FX 单源红线） ────────────
    fx = None
    fx_rows = psql_tsv(args.source_container, args.source_user, args.source_db,
                       "SELECT rate::text FROM fx_rate WHERE base='USD' AND quote='CNY' "
                       "ORDER BY retrieved_at DESC LIMIT 1")
    if fx_rows:
        fx = fx_rows[0][0]
    print("fx USD→CNY = %s" % (fx or "NULL"))

    # 目标既有属主：canonical_id 已被别的 canonical_name 占有的 ⇒ 批内回退 NULL。
    # ★ 2026-11-17：改为**以 models_canonical 为权威**（id → 网关 canonical 名）。
    #   旧实现以 market_price_reference 里已有的 canonical_name 为准 ⇒ 在
    #   「旧口径（源 key 名）+ 新口径（网关名）」并存的那一次同步里，
    #   新行会被旧行挤掉 ⇒ 正好是本次要修的那个漂移。
    existing_owner = {}
    for cid, name in canon_rows:
        existing_owner[cid] = name.lower()

    def resolve(key):
        """旧两趟解析（精确小写 → 归一化）；保留给 plan_rows 的**兼容路径**。

        ★ 主路径**不再**用它（解析已在步骤 3 用 resolve_canonical 完成，
                  并把结果灌进 res CTE 做了同名合并）⇒ 这里只服务旧调用方。
        """
        cid, _gw, family = resolve_canonical(key, canon_exact, canon_norm, canon_names)
        return cid, family

    # ★ 分类在纯函数里（plan_rows）：拒绝判定 + canonical 解析 + 批内/跨日属主回退。
    #   resolved_map 传 ⇒ 走「新路径」（解析结果来自步骤 3，含同名合并后的 cid）。
    landable, rejects = plan_rows(src_rows, resolve, existing_owner, resolved_map)
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

    # 970 平台明细：同 dimension 先清空再整批 INSERT（快照语义；只读运营面）。
    # 目标缺 970 表时 fail-closed 跳过明细写入（聚合 940 仍可落地）。
    # ★ 2026-11-17：dimension 按**源侧 price_scope** 归一（不只 standard），
    #   且新增 tier / model_kind / cache_hit 列（972 迁移）。
    plat_tbl = psql_tsv(args.target_container, args.target_user, args.target_db,
                        "SELECT to_regclass('public.market_price_platform_quotes') IS NOT NULL")
    plat_upserts = []
    plat_dim_count = {}
    plat_collapsed = 0
    plat_landed = 0
    if plat_tbl and plat_tbl[0][0] == "t":
        # ★ 快照语义：全表重写（维度集合会变 —— 多 scope 进来后不再只是 standard）
        plat_upserts.append("DELETE FROM public.market_price_platform_quotes;")
        #
        # ★ 2026-11-17 **表键粒度必须与源侧观测键对齐**（真库首次写入即暴露）：
        #   源侧观测键 = (platform, raw_model, price_scope, tier, region,
        #                 currency, unit)
        #   （salesdecorate 的 ingest 去重键与 price_hash 同款 ⇒ 官方口径）
        #   ⇒ 目标表键 = (dimension, canonical_name, platform_id, tier,
        #                 source_region, currency)
        #   ★ 不含 region 的后果（211 真库实测）：bailian 的
        #     deepseek-v4-flash-0731「忙时 08:00-22:00」有 5 个 region
        #     （中国大陆/全球 3.0 CNY、美国/日本/新加坡 3.208）⇒ 撞主键 abort，
        #     若改成静默取一档则是**丢掉 7% 的价差**（静默信息丢失）。
        #   ★ 别名收敛（多个源 key → 同一 out_name）会让同一表键出现多行 ⇒
        #     按「地板口径」**确定性**择优（CNY 价低者优先 ⇒ 与 940 地板同口径），
        #     并把折叠数写进台账（**不静默**）。
        best = {}
        collapsed = 0
        for pr in plat_quote_rows:
            dim = dimension_for_scope(pr[18] or args.scope)
            key = (dim, pr[0], pr[1], pr[14] or "", pr[19] or "", pr[5] or "")
            cand = _plat_order_key(pr)
            prev = best.get(key)
            if prev is None:
                best[key] = (pr, dim, cand)
                continue
            collapsed += 1
            if cand < prev[2]:
                best[key] = (pr, dim, cand)
        for pr, dim, _cand in best.values():
            # pr: out_name,pid,pname,is_vendor,vendor,cur,nin,nout,cin,cout,
            #     region,native,run,url,tier,model_kind,cache_native,cache_cny,
            #     scope,src_region
            is_vendor = "true" if pr[3] in ("t", "true", "True") else "false"
            plat_dim_count[dim] = plat_dim_count.get(dim, 0) + 1
            # ★ 2026-11-17：model_kind **统一取 canonical 模型的分类**
            #   （resolved_map 里那份，与「非模型拒绝门」「同名合并取最高可比类别」
            #   同一个来源）⇒ 决策面与候选面同闸、同一个模型在同一份产物里
            #   只有一个类别。
            #   ★ 为什么不在 SQL 里就地取：源侧 key 清单只覆盖**标准按量** scope，
            #     而 970 明细是**全部 scope** ⇒ 只在标准按量里出现的模型不在
            #     resolved_map 里 ⇒ 回落用源侧 SQL 的值（那边已 canonical 优先）。
            #   ★ 211 真库实测：o.model_kind 与 m.model_kind 在 6341/19181 行
            #     不一致 ⇒ 旧口径下 970 明细写的是观测快照值，与门判用的
            #     canonical 值分属两套分类。
            kind = ""
            rm = resolved_map.get(pr[0])
            if rm is not None:
                kind = rm[2] or ""
            if not kind:
                kind = pr[15] or ""
            plat_upserts.append(
                "INSERT INTO public.market_price_platform_quotes ("
                " dimension, canonical_name, platform_id, platform_name, region,"
                " native_currency, is_vendor, vendor, currency,"
                " input_per_1m, output_per_1m, cny_in_per_1m, cny_out_per_1m,"
                " run_date, source_url, tier, model_kind, source_region,"
                " cache_hit_per_1m, cny_cache_hit_per_1m, updated_at) VALUES ("
                " %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, now());"
                % (qstr(dim), qstr(pr[0]), qstr(pr[1]), qstr(pr[2]),
                   qstr(pr[10] or ""), qstr(pr[11] or ""), is_vendor, qstr(pr[4] or ""),
                   qstr(pr[5] or ""), num(pr[6]), num(pr[7]), num(pr[8]), num(pr[9]),
                   qstr(pr[12]), qstr(pr[13] or ""), qstr(pr[14] or ""),
                   qstr(kind), qstr(pr[19] or ""),
                   num(pr[16]), num(pr[17])))
        plat_landed = len(best)
        plat_collapsed = collapsed
        print("platform-quotes: %d rows (source=%d collapsed=%d, by dimension: %s)"
              % (plat_landed, len(plat_quote_rows), plat_collapsed,
                 ", ".join("%s=%d" % (d, n) for d, n in sorted(plat_dim_count.items()))))
    else:
        print("WARNING: market_price_platform_quotes missing (970) ⇒ skip platform-quote landing",
              file=sys.stderr)

    # ★ 台账口径（2026-11-10 审计 P2-C）：
    #   source_rows  = 源侧候选全集（len(src_rows)）
    #   landed_rows  = 实际写入 market_price_reference 的行数
    #   rejected_rows= 拒绝族计数之和（no_price / no_canonical_name / no_source_run_date）
    #   ⇒ 恒等式 source_rows = landed_rows + rejected_rows **构造上闭合**
    #     （每个源侧候选要么落地、要么被拒，且只归入其中一侧）。
    #   ⇒ 旧实现里源侧 SQL 预过滤掉了「无价」候选 ⇒ rejected 恒 0 ⇒ 空恒等式。
    rejected = sum(rejects.values())

    # ── 身份漂移自愈（2026-11-17）──────────────────────────────────────────
    # ★ 替换掉旧的「歧义 NULL-out」：旧语句在「同一 cid 出现两行」时把**两行都**
    #   置空 —— 那正是本轮要修的漂移形态（新行 canonical_name=网关名、旧行=源 key 名
    #   并存）⇒ 旧语句会把**刚写好的新行也置空**，等于把修复抵消掉。
    # ⇒ 新口径：**先删旧口径的漂移行，再 upsert 新行**。
    #   删两族（与 972 迁移同判据）：
    #     ① canonical_id 非空但 canonical_name ≠ 该 id 的网关名（= 旧口径行）；
    #     ② canonical_id 为空且 canonical_name 含 ':-' 形态（= 旧口径的命名空间行）。
    #   ⇒ 必须在 upsert **之前**执行，否则新行会撞 uq_mpr_canonical_id。
    stale_cleanup = (
        "DELETE FROM public.market_price_reference mpr"
        " WHERE mpr.dimension = %s AND ("
        "   (mpr.canonical_id IS NOT NULL AND NOT EXISTS ("
        "       SELECT 1 FROM public.models_canonical mc"
        "        WHERE mc.id = mpr.canonical_id"
        "          AND lower(mc.canonical_name) = lower(mpr.canonical_name)))"
        "   OR (mpr.canonical_id IS NULL AND mpr.canonical_name ~ ':-')"
        " );" % qstr(args.dimension))

    # ── 6) 目标事务（含红线锚点自检） ────────────────────────────────────────
    today = date.today().isoformat()
    conflicts = {"canonical_unresolved": fam_count["unresolved"],
                 "canonical_ambiguous": fam_count["ambiguous"]}
    detail = {
        "scope": args.scope, "dimension": args.dimension, "unit": args.unit,
        "as_of": args.as_of or today, "fx_usd_cny": fx,
        "exclude_platforms": args.exclude_platform,
        "source_container": args.source_container,
        # ★ 脚本自身指纹（2026-11-17）：宿主 cron 跑的是 /root/market-price-sync.py，
        #   它与仓库副本**长期漂移**（211 实测：仓库已有 970 写入段、宿主副本没有
        #   ⇒ 平台现价表恒空、而台账看起来一切正常）⇒ 台账自带指纹 ⇒ 漂移当场可读。
        "script_sha256": script_sha256(),
        # ★ 身份对齐读数（2026-11-17）：解析率/合并数一眼可见，
        #   不用翻日志就知道「这次落了多少可比的行」。
        "identity_source_keys": len(res_pairs),
        "identity_resolved_out_names": sum(1 for v in resolved_map.values() if v[0] is not None),
        "identity_unresolved_out_names": sum(1 for v in resolved_map.values() if v[0] is None),
        "identity_namespace_stripped": ns_stripped,
        "identity_merged_groups": sum(1 for _k, v in resolved_map.items()),
        # ★ 970 平台现价明细的落地/折叠读数（2026-11-17）：表键粒度与源侧观测键
        #   对齐后，别名收敛与同键多行观测仍可能落到同一表键 ⇒ 按地板口径
        #   确定性择优留一行 ⇒ **折叠数必须可读**（不静默丢价）。
        "platform_quote_rows": plat_landed,
        "platform_quote_collapsed": plat_collapsed,
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
        # ★ 970 明细按 dimension 的分布（多 scope 落地后可见）。
        "platform_quote_by_dimension": plat_dim_count,
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
    ] + [stale_cleanup] + upserts + plat_upserts + [
        # ★ 保留「按名回填 canonical_id」：未解析行（canonical_id=NULL）若其
        #   canonical_name 恰好等于某个网关 canonical 名（例如上一轮还没落定、
        #   这一轮网关新增了该模型），这里给它补上 id —— 与 upsert 的
        #   NOT EXISTS 守卫配套，保证不撞 uq_mpr_canonical_id。
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
