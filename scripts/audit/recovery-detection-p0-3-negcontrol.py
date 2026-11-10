#!/usr/bin/env python3
"""★★ 验收判据的**负控**（★ 修复前 era 干跑）：★ 证伪「恒真判据」

★ 为什么要它：★★ 判据有两种坏法 ——
  ① **恒真**（★ 恒过 ⇒ 什么都能"验证通过"⇒ ★★ 门没牙）
  ② **恒假**（★ 恒不过 ⇒ ★★ 什么结论都出不来）
⇒ ★★ **两者都不会在「关窗读数」里暴露** ⇒ ★★ 必须在关窗前用**已知答案**的窗口验一次。

★ 怎么造负控：★★ **把 era 切点整体前移 N 天**（★ 只改 `/tmp` 里的临时副本，
★ **不动仓库脚本、不重划真实 era 切点**）⇒ ★★ 「post」段就落在**修复之前**的窗口
⇒ ★★ 于是同一把尺子必须判 **❌ 未达成**（★ 期望的答案已知）。

★★★ **第 23 处口径修正（2026-11-11 00:4x）：★★ `now()` 必须随 era 切点「一起」平移**
★★ ⇒ ★★ 否则负控的 post 窗口长度 ≠ 被验收窗口的长度 ⇒ ★★ **负控与验收几何不一致**
★★   ⇒ ★★ 实测（本轮真抓到）：★ 原负控只平移了**切点**、没平移 `now()`
★★     ⇒ ★★ 负控的 post 窗口 = **[切点-N天, 真实now]** = **~35h**（★ Stage 1 验收窗口是 **24h**）
★★     ⇒ ★★ 实测**两行裁决符号在两种几何下翻转**（★ M14 / M14x 旧 featured 人群（无回归）：
★★        35h ⇒ **❌ 未达成**、24h ⇒ **✅ 达成**）⇒ ★★ **负控的绿不能代表验收窗口**
★★     ⇒ ★★ 修法：★ `now()` 一并平移到「**新切点 + stage 窗口**」（★ Stage 1 = 24h、Stage 2 = 7d）
★★        ⇒ ★★ 负控成为验收窗口的**忠实复刻**（★ 窗口长度、成熟度尾部（now-6h）都对齐）
★★     ⇒ ★★ **门不钝**（★ 实测两种几何下效应行 M20 都判 ❌ ⇒ 负控仍有牙）
★   ⇒ ★★ 另：★ 原负控里「M14/M14x 旧 featured 判 ❌」是**几何错配造成的假红**
★     （★ 35h post 比 24h pre ⇒ 不同量纲）⇒ ★★ **不得**读成「负控里旧 featured 也退化了」。

★★★ **第 24 处口径修正（2026-11-11 01:2x）：★★ 「post 段」必须被 `now()` 界定，否则负控/预演对它无效**
★★ ★★ 形态：★ M11 的两个 fr CTE（M11_latency @fr#1、M11b @fr#2）**完全没有 `now()`**
★★   ⇒ ★★ post 段 = **[切点, ∞)** ⇒ ★★ **无界** ⇒ ★★ **as-of 替换对它无效**
★★   ⇒ ★★ 第 23 处的几何对齐（★ `now()` 随 era 切点平移）**对 M11 不生效**
★★   ⇒ ★★ ⇒ 负控/预演里的 M11 窗口**仍不是 stage 窗口** ⇒ ★★ ⇒ **负控证伪不了 M11**
★★ ★★★ 实测（负控 era + as-of = 新切点+24h）：★ 无上界 ⇒ 13 ep / med **3** ⇒ **判 ✅**（★ **恒真 ⇒ 没有牙**）
★★   ⇒ ★★ 有上界（★ 采集侧加 `AND n.created_at <= now()`）⇒ 9 ep / med **30** ⇒ **判 ❌**（★ **长出牙了**）
★★ ⇒ ★★ 负控必须**点名要求** M11 namespaced 判负（★ **Stage 1 专属**：★ Stage 2 的 168h 窗口
★★   **不含**那段退化 ⇒ 实测 med **3** ⇒ ✅ 达成 ⇒ ★★ 对 Stage 2 也要求就是**假红**）

★ 用法（本地）：
    python3 scripts/audit/recovery-detection-p0-3-negcontrol.py --shift-days 7 --stage 2
    python3 scripts/audit/recovery-detection-p0-3-negcontrol.py --shift-days 1 --stage 1
    python3 scripts/audit/recovery-detection-p0-3-negcontrol.py --selftest     # ★ 离线自检（不连库）

★ 判定：★ 效应判据行必须是 **❌ 未达成**（或 free 切片的「⚠️ 样本不足」）
⇒ ★★ 若它判 ✅ ⇒ ★★ **判据恒真 ⇒ 门没牙 ⇒ 禁止用这份判据下结论**。
⇒ ★★ 此外 ★ **Stage 1 专属**的 M11 namespaced 行**也必须判 ❌**（★ 否则 M11 在负控里恒真）。
"""
from __future__ import annotations

import argparse
import importlib.util
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
SQL_PATH = ROOT / "scripts/audit/recovery-detection-p0-3-metrics.sql"
VERDICT_PATH = ROOT / "scripts/audit/recovery-detection-p0-3-verdict.py"

# ★ 基线键（判定器里的命名：M14_<组>_<窗>）⇒ 采集侧产出键（M14_<窗>_<组>）
BASELINE_KEYS = {
    "M14_old_feat_pre24": "M14_pre24_old_feat", "M14_old_feat_pre7d": "M14_pre7d_old_feat",
    "M14_newly_covered_pre24": "M14_pre24_newly_covered",
    "M14_newly_covered_pre7d": "M14_pre7d_newly_covered",
    "M14_other_pre24": "M14_pre24_other", "M14_other_pre7d": "M14_pre7d_other",
    "M14x_old_feat_pre24": "M14x_pre24_old_feat", "M14x_old_feat_pre7d": "M14x_pre7d_old_feat",
    "M14x_newly_covered_pre24": "M14x_pre24_newly_covered",
    "M14x_newly_covered_pre7d": "M14x_pre7d_newly_covered",
    "M14x_other_pre24": "M14x_pre24_other", "M14x_other_pre7d": "M14x_pre7d_other",
    # ★★ M20 的 pre 键（★ 本轮第 11 处口径修正后新增的**分层**基线）
    #   ★ 采集侧键名与基线键名**同序**（M20_<层>_<窗>）⇒ 无需映射
    "M20_normal_pre6d": "M20_normal_pre6d", "M20_normal_pre24": "M20_normal_pre24",
    "M20_normal_pre7d": "M20_normal_pre7d", "M20_exempt_pre6d": "M20_exempt_pre6d",
    "M20_exempt_pre24": "M20_exempt_pre24", "M20_exempt_pre7d": "M20_exempt_pre7d",
    # ★★ M15x 的 pre 键（★ 本轮第 13 处口径修正新增，★ 逐对金丝雀的分层基线）
    #   ★ 采集侧键名与基线键名**同序**（M15x_<窗>__<金丝雀>）⇒ 无需映射
    #   ★★ 必须进负控的冻结对齐表 ⇒ ★★ 否则 M18 会拿**真实 era** 的事件数去对拍
    #   ★★ **负控 era** 的实测值 ⇒ ★★ 守卫会报「pre 基线漂移」⇒ 整表作废 ⇒ ★★ 负控读不出裁决
    "M15x_pre24__deepseek-ai/deepseek-v4-pro-0813": "M15x_pre24__deepseek-ai/deepseek-v4-pro-0813",
    "M15x_pre24__minimaxai/minimax-m2.7": "M15x_pre24__minimaxai/minimax-m2.7",
    "M15x_pre24__minimaxai/minimax-m3": "M15x_pre24__minimaxai/minimax-m3",
    "M15x_pre24__z-ai/glm-5.3": "M15x_pre24__z-ai/glm-5.3",
    "M15x_pre7d__deepseek-ai/deepseek-v4-pro-0813": "M15x_pre7d__deepseek-ai/deepseek-v4-pro-0813",
    "M15x_pre7d__minimaxai/minimax-m2.7": "M15x_pre7d__minimaxai/minimax-m2.7",
    "M15x_pre7d__minimaxai/minimax-m3": "M15x_pre7d__minimaxai/minimax-m3",
    "M15x_pre7d__z-ai/glm-5.3": "M15x_pre7d__z-ai/glm-5.3",
}
# ★★ 效应判据行（★ 本轮迁移）：★ M14x 的新覆盖行已降为**观察面**（★ 混合口径含 78% 豁免类
#   ⇒ post 的 p90/slow 结构性不可达 ⇒ 若拿它做负控，★ 负控**必然**通过 ⇒ 什么也证伪不了）
#   ⇒ ★ 负控必须指向 **M20 normal 分层**（★ 机制原生：未减半 6h 顶格必须出现）
EFFECT_LABELS = ("M20 新覆盖组·非豁免",)
# ★ 观察面行（★ 负控里它必须是 ⚠️ ⇒ 若变 ✅ ⇒ 说明「混合口径」被误当成判据）
OBSERVE_LABELS = ("M14x 新覆盖人群（★ 混合口径·观察面）",)

# ★★★★ 第 24 处口径修正（2026-11-11 01:2x）：★ Stage 1 专属的「必须判负」清单
#   ★★ **形态**：★ M11 的两个 fr CTE（M11_latency @fr#1、M11b @fr#2）**完全没有 `now()`**
#     ⇒ ★★ post 段 = **[切点, ∞)** ⇒ ★★ **无界**
#     ⇒ ★★ ⇒ **as-of 替换对它无效** ⇒ ★★ 第 23 处的几何对齐（★ `now()` 随 era 切点平移）**对 M11 不生效**
#     ⇒ ★★ ⇒ 负控/预演里的 M11 窗口**仍不是 stage 窗口** ⇒ ★★ ⇒ **负控证伪不了 M11**
#   ★★★ **实测（负控 era + as-of = 新切点+24h）**：
#     · **无上界** ⇒ `M11b_post_fix_namespaced` = 13 ep / med **3** ⇒ ★★ **判 ✅**
#       ⇒ ★★ ⇒ **M11 在负控里是「恒真」的 ⇒ 它没有牙**
#       ⇒ ★★ ⇒ Stage 1 的 M11 读数**无法区分「修复生效」与「修复没生效」**
#     · **有上界**（★ 采集侧加 `AND n.created_at <= now()`）⇒ 9 ep / med **30** ⇒ ★★ **判 ❌** ⇒ ★★ **长出牙了**
#   ★★ ⇒ 负控必须**点名要求** M11 namespaced 判负（★ 否则这一轮负控对它**什么也没证伪**）
#   ★★★ **为什么只限 Stage 1**：★ Stage 2 的窗口是 **168h**（★ as-of = 新切点 + 7d）
#     ⇒ ★★ 实测 M11 ns = 50 ep / med **3** ⇒ ✅ **达成**（★ 7 天的 pre-fix 窗口**不含**那段退化）
#     ⇒ ★★ ⇒ 「必须判负」**只能是 Stage 1 专属** ⇒ ★★ 否则 Stage 2 的负控会被**错误地**判失败 ⇒ ★★ **假红**
#   ★★ **bare 行也不作要求**：★ 实测 Stage 1 负控里 bare = 8 ep / med **2** ⇒ ✅
#     （★ 该 24h pre-fix 窗口里 bare 人群未退化 ⇒ 要求它判负才是假红）
STAGE1_MUST_FAIL_LABELS = ("M11 namespaced 恢复感知时延",)


def must_fail_labels_for_stage(stage: int) -> tuple[str, ...]:
    """★ Stage 专属的「必须判负」清单（★ 第 24 处口径修正）

    ★★ **只有 Stage 1 有要求**：★ Stage 2 的 168h 窗口**不含**那段退化
    ⇒ ★★ 实测 M11 ns = 50 ep / med **3** ⇒ ✅ **达成**
    ⇒ ★★ 若对 Stage 2 也要求判负 ⇒ ★★ 负控会被**错误地**判失败 ⇒ ★★ **那就是假红**
    ⇒ ★★★ ⇒ **stage 门禁必须是行为级的**（★ 不写成「两个 stage 共用一张表」⇒ 迟早被误用）
    """
    return STAGE1_MUST_FAIL_LABELS if stage == 1 else ()


def head_count(text: str) -> int | None:
    m = re.match(r"^\s*(\d+)", text or "")
    return int(m.group(1)) if m else None


def parse(raw: str) -> dict[str, str]:
    d = {}
    for line in raw.splitlines():
        if "|" not in line:
            continue
        k, v = line.split("|", 1)
        d[k.strip()] = v.strip()
    return d


def build_shifted_sql(sql: str, shift_days: int, stage: int) -> tuple[str, dict]:
    """★★★ **第 23 处口径修正**：★ era 切点 **与 `now()` 一起**平移 ⇒ ★★ 负控几何 == 验收几何

    ★★ **为什么必须连 `now()` 一起平移**（★ 本轮实测抓到的真缺陷）：
    ★ · 只平移切点 ⇒ ★「post」段 = **[新切点, 真实now]** ⇒ ★ 窗口长度 = **~35h**（★ Stage 1）
    ★ · 而被验收的 Stage 1 窗口是 **24h** ⇒ ★★ **两者几何不一致**
    ★ · ⇒ ★★ 实测**两行裁决符号在两种几何下翻转**（★ M14 / M14x 旧 featured：35h ⇒ ❌、24h ⇒ ✅）
    ★ · ⇒ ★★ **负控的绿不能代表验收窗口** ⇒ ★★ 负控必须成为验收窗口的**忠实复刻**

    ★★ **做法**：★ `now()` → **新切点 + stage 窗口**（★ Stage 1 = 24h、Stage 2 = 168h）
    ★   ⇒ ★★ 窗口长度、成熟度尾部（now-6h）、事件总量**全部对齐**
    ★ ★★ **防静默失效**：★ 若 SQL 里没有 `now()` ⇒ ★ 平移不会发生 ⇒ ★ 负控仍是「35h 窗口」
    ★   ⇒ ★★ 直接 `assert` 失败（★ 绝不让它「静默地跑错几何」）
    """
    from datetime import datetime, timedelta
    m = re.search(r"TIMESTAMPTZ '(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\+08'", sql)
    assert m, "★ 采集 SQL 里找不到 era 切点字面量"
    cut = datetime.strptime(m.group(1), "%Y-%m-%d %H:%M:%S")
    new_cut = cut - timedelta(days=shift_days)
    new_lit = f"TIMESTAMPTZ '{new_cut:%Y-%m-%d %H:%M:%S}+08'"
    n_cut = sql.count(m.group(0))
    variant = sql.replace(m.group(0), new_lit)

    win_hours = 24 if stage == 1 else 7 * 24
    asof = new_cut + timedelta(hours=win_hours)
    asof_lit = f"TIMESTAMPTZ '{asof:%Y-%m-%d %H:%M:%S}+08'"
    n_now = sql.count("now()") + sql.count("NOW()")
    # ★★★ **assert 而不是警告**：★ 没有 now() ⇒ 平移不发生 ⇒ ★ 负控仍是错几何 ⇒ ★ 静默失效
    assert n_now > 0, ("★ 采集 SQL 里没有 now() ⇒ ★ now() 平移不会发生 ⇒ "
                        "★ 负控的 post 窗口会退回「~35h」⇒ ★ 与验收几何不一致 ⇒ ★ 静默失效")
    variant = variant.replace("now()", asof_lit).replace("NOW()", asof_lit)
    geo = {"cut": cut, "new_cut": new_cut, "asof": asof, "win_hours": win_hours,
           "n_cut": n_cut, "n_now": n_now, "asof_lit": asof_lit}
    return variant, geo


def selftest() -> int:
    """★ 离线自检（★ 不连库）：★ 证明「切点与 now() 一起平移」这件事本身是对的"""
    from datetime import datetime
    fails = []
    fake = ("SELECT 'x' WHERE created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08' "
            "- interval '7 days' AND created_at > now() - interval '24 hours' "
            "AND x > NOW()")
    # ① Stage 1：切点前移 1 天、now() 随动 ⇒ post 窗口 = 24h
    v, g = build_shifted_sql(fake, 1, 1)
    if g["new_cut"] != datetime(2026, 11, 9, 13, 42, 30):
        fails.append(f"[① 切点平移] 期望 2026-11-09 13:42:30 实得 {g['new_cut']}")
    if g["asof"] != datetime(2026, 11, 10, 13, 42, 30):
        fails.append(f"[① as-of] 期望 2026-11-10 13:42:30 实得 {g['asof']}")
    if g["win_hours"] != 24:
        fails.append(f"[① 窗口] Stage 1 期望 24h 实得 {g['win_hours']}h")
    if (g["asof"] - g["new_cut"]).total_seconds() != 24 * 3600:
        fails.append("[① 窗口] as-of 与新切点之差必须 = 24h")
    if "now()" in v.lower():
        fails.append("[① 平移] 变体里仍残留 now() ⇒ 平移未生效")
    if v.count(g["asof_lit"]) != 2:
        fails.append(f"[① 平移] as-of 替换次数应为 2（now()/NOW()）实得 {v.count(g['asof_lit'])}")
    # ② Stage 2：切点前移 7 天、now() 随动 ⇒ post 窗口 = 168h
    v2, g2 = build_shifted_sql(fake, 7, 2)
    if g2["win_hours"] != 168:
        fails.append(f"[② 窗口] Stage 2 期望 168h 实得 {g2['win_hours']}h")
    if (g2["asof"] - g2["new_cut"]).total_seconds() != 7 * 24 * 3600:
        fails.append("[② 窗口] as-of 与新切点之差必须 = 168h")
    if g2["new_cut"] != datetime(2026, 11, 3, 13, 42, 30):
        fails.append(f"[② 切点平移] 期望 2026-11-03 13:42:30 实得 {g2['new_cut']}")
    # ③ ★★ **没有 now() ⇒ 必须报错**（★ 防「静默地跑错几何」）
    try:
        build_shifted_sql("SELECT 'x' WHERE created_at > TIMESTAMPTZ '2026-11-10 13:42:30+08'", 1, 1)
        fails.append("[③ 防静默失效] SQL 里没有 now() 时应当报错（★ 否则负控会退回错几何）")
    except AssertionError:
        pass
    # ④ ★★ 变体里出现的 era 切点字面量**必须只有两个**：「新切点」与「as-of」
    #   （★ 注意：★ Stage 1 + shift 1 天时 as-of **恰好等于原切点文本** ⇒ ★ 不能用
    #   ★   「是否残留原切点文本」来判定 ⇒ ★★ 必须按**字面量集合**判定）
    lits = set(re.findall(r"TIMESTAMPTZ '\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\+08'", v))
    allowed = {f"TIMESTAMPTZ '{g['new_cut']:%Y-%m-%d %H:%M:%S}+08'", g["asof_lit"]}
    extra = lits - allowed
    if extra:
        fails.append(f"[④ 切点替换] 变体里出现了预期外的时间戳字面量 ⇒ {sorted(extra)}")
    if f"TIMESTAMPTZ '{g['new_cut']:%Y-%m-%d %H:%M:%S}+08'" not in lits:
        fails.append("[④ 切点替换] 变体里缺少「新切点」字面量")
    # ⑤ ★★★ **Stage 专属「必须判负」清单**（★ 第 24 处口径修正）
    #   ★★ 形态：★ M11 的 post 段曾**无界**（★ 采集侧两个 fr CTE 都没有 now()）
    #   ⇒ ★★ 负控里它**恒判 ✅** ⇒ ★★ 它在负控里**没有牙**
    #   ⇒ ★★ ⇒ Stage 1 必须**点名要求**它判负 ⇒ ★★ 否则 M11 读数区分不了「修复生效/没生效」
    l1 = must_fail_labels_for_stage(1)
    l2 = must_fail_labels_for_stage(2)
    if not any("M11 namespaced" in x for x in l1):
        fails.append("[⑤ Stage 1 必须判负] 清单里必须有「M11 namespaced 恢复感知时延」"
                     "（★ 否则 M11 在负控里恒真 ⇒ 没有牙）")
    if l2:
        fails.append("[⑤ Stage 2 必须判负] Stage 2 **不得**要求判负（★ 168h 窗口**不含**那段退化"
                     " ⇒ 实测 M11 ns = 50 ep / med 3 ⇒ ✅ 达成 ⇒ 否则是**假红**）")

    print("== 负控几何自检（★ 不连库）==")
    if fails:
        for f in fails:
            print("   ❌ " + f)
        print(f"\n★★ 自检未通过：{len(fails)} 项")
        return 1
    print("   ✔ ① Stage 1：切点前移 1 天 + now() 随动 ⇒ post 窗口 = 24h")
    print("   ✔ ② Stage 2：切点前移 7 天 + now() 随动 ⇒ post 窗口 = 168h")
    print("   ✔ ③ ★★ SQL 里没有 now() ⇒ 必须报错（★ 防「静默地跑错几何」）")
    print("   ✔ ④ 变体里不残留原始切点字面量")
    print("   ✔ ⑤ ★★ Stage 1 必须判负清单含 M11 namespaced（★ M11 post 段无界时会恒判 ✅）"
          "；★ Stage 2 不要求（★ 否则假红）")
    print("\n== 负控几何自检通过 ==")
    return 0


def run_shifted_sql(shift_days: int, ssh_host: str, stage: int) -> str:
    """★ 把 era 切点 **与 `now()` 一起** 前移（★ 第 23 处口径修正）并跑真库（★ 只用临时副本）"""
    sql = SQL_PATH.read_text(encoding="utf-8")
    variant, geo = build_shifted_sql(sql, shift_days, stage)
    print(f"== 负控：era 切点 {geo['cut']:%Y-%m-%d %H:%M:%S} → {geo['new_cut']:%Y-%m-%d %H:%M:%S}"
          f"（{geo['n_cut']} 处）==")
    print(f"== ★ 几何对齐：now() → {geo['asof']:%Y-%m-%d %H:%M:%S}（{geo['n_now']} 处）"
          f" ⇒ ★ post 窗口 = **{geo['win_hours']}h**（★ == 被验收窗口长度）==")
    local = pathlib.Path(f"/tmp/negctl_{shift_days}d.sql")
    local.write_text(variant, encoding="utf-8")
    subprocess.run(["scp", "-q", str(local), f"{ssh_host}:{local}"], check=True)
    remote = (
        f"docker cp {local} acc-pg17:{local} && "
        # ★★★ **网关容器名不许写死**（★ 2026-11-10 19:0x 实测踩到：★ 写死 `llm-gateway-211-b`
        #   ★★ 而 19:01 有别的会话把槽位从 b 换到 a ⇒ ★★ `-b` 变成 Exited ⇒ ★★ `docker exec` 报
        #   ★★ 「container is not running」⇒ ★★ 负控**采不到数据** ⇒ ★★ 而 rc 仍是 0 ⇒ ★★ 静默失效）
        #   ⇒ ★★ ⇒ 改为**自动发现运行中的容器**（★ 槽位会轮换 ⇒ 写死必然过期）
        "GW=$(docker ps --filter name=llm-gateway-211 --format '{{.Names}}' | grep -v aux | head -1); "
        'test -n "$GW" || { echo "★ 找不到运行中的 llm-gateway-211-* 容器 ⇒ 采集失败"; exit 3; }; '
        'DSN=$(docker exec "$GW" env | grep LLM_GATEWAY_DATABASE_URL | cut -d= -f2-) && '
        f'docker exec -e PGPASSWORD="$(echo "$DSN" | '
        """sed -E "s#^postgres(ql)?://([^:]+):([^@]*)@.*#\\3#")" """
        f"acc-pg17 psql -U acc_super -d llm_gateway -tA -F\"|\" -v ON_ERROR_STOP=1 -f {local}"
    )
    out = subprocess.run(["ssh", ssh_host, remote], capture_output=True, text=True)
    if out.returncode != 0:
        print(out.stdout[-2000:], out.stderr[-2000:], file=sys.stderr)
        raise SystemExit(f"★ 采集失败 rc={out.returncode}")
    return out.stdout


def load_patched_judge(data: dict[str, str], tag: str):
    """★ 判定器副本：★ 只把 12 个 pre 基线的**冻结事件数**换成该负控 era 自己的实测值
    （★ 冻结基线本来就是 era 专属的 ⇒ 这样对齐不是绕过，而是按该 era 的事实对齐）
    ⇒ ★ 于是 M18 守卫会通过，★ 可以读出**真实裁决行**。"""
    src = VERDICT_PATH.read_text(encoding="utf-8")
    for bkey, sqlkey in BASELINE_KEYS.items():
        n = head_count(data.get(sqlkey, ""))
        if n is None:
            continue
        # ★★ 正则必须**容忍空白/换行**（★ 本轮自踩：★ M15x 的 BASELINES 条目是多行排版
        #   ★★ ⇒ 旧的 `": \{"n": ` 形态匹配 0 次 ⇒ ★★ assert 直接炸 ⇒ ★★ 负控跑不动）
        pat = re.compile(r'("%s":\s*\{\s*"n":\s*)\d+' % re.escape(bkey))
        src, c = pat.subn(lambda mm: f"{mm.group(1)}{n}", src)
        assert c == 1, (bkey, c)
    # ★★ M20 的**阈值元组**也要按该 era 的实测值对齐（★ med_gap/p90_gap/slow/top21600_n）
    #   ★ 理由：M20 的判据阈值是「**pre 的一半**」⇒ ★ pre 必须是**该 era 自己的** pre
    for bkey in ("M20_normal_pre24", "M20_normal_pre7d", "M20_exempt_pre24", "M20_exempt_pre7d"):
        raw = data.get(bkey, "")
        if not raw:
            continue
        vals = []
        for nm in ("med_gap", "p90_gap", "slow_gt300_pct", "top21600_n"):
            m = re.search(rf"\b{nm}=(-?\d+(?:\.\d+)?)", raw)
            if not m:
                vals = []
                break
            vals.append(m.group(1))
        if len(vals) != 4:
            continue
        # ★★ 捕获**不含**外层括号的元组内容（★ 本轮自踩：★ 捕获含括号时替换会多出一层括号 ⇒ 语法错）
        pat = re.compile(r'("%s":\s*\{\s*"n":\s*\d+,\s*"base":\s*)\(([^)]*)\)' % re.escape(bkey))
        src, c = pat.subn(lambda mm: f"{mm.group(1)}({', '.join(vals)})", src)
        assert c == 1, (bkey, c)
    # ★★ M15x 的**逐对阈值元组**同样要按该 era 对齐（★ 本轮第 13 处口径修正新增）
    #   ★ 理由同 M20：★ 逐对判据的阈值是「该只自己 pre 的一半」⇒ ★ 必须是**该 era 的 pre**
    #   ★★ 五项：(normal_med_min, normal_ladder_p50_s, top3600_n, top7200_n, top21600_n)
    #   ★★ 「?」的条目（三只 normal 为空的观察面）解析不出来 ⇒ 跳过（★ 不影响判定：它们是观察面）
    for bkey in sorted(k for k in BASELINE_KEYS if k.startswith("M15x_pre")):
        raw = data.get(bkey, "")
        if not raw:
            continue
        vals = []
        for nm in ("normal_med_min", "normal_ladder_p50_s",
                   "normal_top3600_n", "normal_top7200_n", "normal_top21600_n"):
            m = re.search(rf"\b{nm}=(-?\d+(?:\.\d+)?)", raw)
            if not m:
                vals = []
                break
            vals.append(m.group(1))
        if len(vals) != 5:
            continue
        # ★★ 捕获**不含**外层括号的元组内容（★ 本轮自踩：★ 捕获含括号时替换会多出一层括号 ⇒ 语法错）
        pat = re.compile(r'("%s":\s*\{\s*"n":\s*\d+,\s*"base":\s*)\(([^)]*)\)' % re.escape(bkey))
        src, c = pat.subn(lambda mm: f"{mm.group(1)}({', '.join(vals)})", src)
        assert c == 1, (bkey, c)
    mod_path = pathlib.Path(f"/tmp/negctl_verdict_{tag}.py")
    mod_path.write_text(src, encoding="utf-8")
    spec = importlib.util.spec_from_file_location(f"negctl_{tag}", mod_path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--shift-days", type=int, default=7,
                    help="★ era 切点整体前移几天（★ 负控窗口长度）")
    ap.add_argument("--stage", type=int, default=2, choices=(1, 2))
    ap.add_argument("--ssh-host", default="211")
    ap.add_argument("--selftest", action="store_true",
                    help="★ 离线自检（★ 不连库）：★ 验「切点与 now() 一起平移」这件事本身")
    args = ap.parse_args()

    # ★★ 离线自检（★ 第 23 处口径修正：★ 负控脚本自己也要有牙齿）
    if args.selftest:
        return selftest()

    raw = run_shifted_sql(args.shift_days, args.ssh_host, args.stage)
    data = parse(raw)
    mod = load_patched_judge(data, f"{args.shift_days}d")

    rows, bad, guards = mod.judge(data, stage=args.stage)
    failures = []
    print("\n== 负控守卫 ==")
    for name, val in guards:
        mark = "★★" if ("★★" in val or not val.strip()) else "✔"
        print(f"   {mark} {name}: {val}")
    if bad:
        print(f"\n★★ 负控自身有守卫失败：{bad} ⇒ 先查口径，勿读裁决")
        # ★★★ **守卫失败 ⇒ 负控必须判失败**（★ 本轮第 19 处修正的同族）
        #   ★★ 形态：★ 守卫不过 ⇒ ★ 裁决表作废 ⇒ ★★ 负控**什么也没证伪** ⇒ ★★ 却 rc=0 ⇒ 假绿
        #   ★★ ⇒ ★★ 负控的守卫失败必须进 failures ⇒ ★ rc=1
        failures.append(f"[负控] 负控自身有守卫失败（★ 裁决表作废 ⇒ 本轮什么也没证伪）⇒ {bad}")

    print("\n== 负控裁决（★ 期望：效应判据**未达成**）==")
    seen_effect = 0
    seen_observe = 0
    seen_must_fail = 0
    # ★★★★ 第 24 处口径修正：★ Stage 专属的「必须判负」清单（★ Stage 1 才含 M11 namespaced）
    must_fail_labels = must_fail_labels_for_stage(args.stage)
    if must_fail_labels:
        print(f"   ★ Stage {args.stage} 专属必须判负：{list(must_fail_labels)}")
    for r in rows:
        label, base, obs, verdict_txt = r
        is_effect = any(e in label for e in EFFECT_LABELS)
        is_must_fail = label in must_fail_labels
        if is_effect:
            seen_effect += 1
            print(f"   | {label}\n       基线: {base}\n       观测: {obs}\n       裁决: {verdict_txt}")
            # ★★★ 负控必须**判负**（★ 本轮收紧：★ 原来 ⚠️ 也算「通过」⇒ ★★ 负控什么也证伪不了）
            #   ★★ 效应判据在「修复前」的窗口里**必须**读到「未达成」
            #   ★★ 若落 ⚠️（不可判/样本不足）⇒ ★★ 说明这一轮负控**没有证伪任何东西** ⇒ 门没牙
            if not verdict_txt.startswith("❌"):
                failures.append(f"[负控] 效应判据**未判负**（★ 必须 ❌）⇒ ★★ 这一轮负控什么也证伪不了"
                                f" ⇒ 门没牙 ⇒ {label}: {verdict_txt}")
        elif is_must_fail:
            # ★★★★ 第 24 处口径修正：★ Stage 专属判据**必须判负**
            #   ★★ 形态：★ M11 的 post 段曾**无界**（★ 没有 now()）⇒ ★★ 负控里它恒判 ✅
            #   ⇒ ★★ 它在负控里**没有牙** ⇒ ★★ Stage 1 的 M11 读数区分不了「修复生效/没生效」
            #   ⇒ ★★ ⇒ 必须**点名要求**它判负 ⇒ ★★ 若它判 ✅ ⇒ **门没牙** ⇒ rc=1
            seen_must_fail += 1
            print(f"   | {label}\n       基线: {base}\n       观测: {obs}\n       裁决: {verdict_txt}")
            if not verdict_txt.startswith("❌"):
                failures.append(f"[负控] Stage {args.stage} 专属判据**未判负**（★ 必须 ❌）"
                                f" ⇒ ★★ 这一轮负控对它**什么也没证伪**（★ 该判据在负控里恒真 ⇒ 没有牙）"
                                f" ⇒ {label}: {verdict_txt}")
        elif any(o in label for o in OBSERVE_LABELS):
            seen_observe += 1
            print(f"   | {label}\n       观测: {obs}\n       裁决: {verdict_txt}")
            if verdict_txt.startswith("✅"):
                failures.append(f"[负控] 观察面行被判达成 ⇒ ★★ 混合口径被误当成判据 ⇒ {label}")
        elif "对照组" in label or "无回归" in label:
            print(f"   | {label}\n       观测: {obs}\n       裁决: {verdict_txt}")
    if seen_effect == 0:
        failures.append("[负控] 没找到效应判据行 ⇒ 判据集结构变了")
    if seen_observe == 0:
        failures.append("[负控] 没找到观察面行（M14x 混合口径）⇒ 判据集结构变了")
    if must_fail_labels and seen_must_fail == 0:
        failures.append(f"[负控] 没找到 Stage {args.stage} 专属必须判负行（{list(must_fail_labels)}）⇒ 判据集结构变了")

    print()
    if failures:
        for f in failures:
            print("   ❌ " + f)
        print("\n★★ 负控未通过")
        return 1
    print(f"   ✔ 负控通过：★ {seen_effect} 条效应判据在**修复前**的窗口全部判「未达成」")
    if seen_must_fail:
        print(f"     ★ 另：{seen_must_fail} 条 Stage {args.stage} 专属判据也判「未达成」"
              f"（★ M11 的 post 段**无界**时会恒判 ✅ ⇒ 必须由这一条兜住）")
    print(f"     ★ 另：{seen_observe} 条混合口径行落「观察面（⚠️）」⇒ ★ 不会被误读成判据")
    print("     ⇒ ★★ 判据**不是恒真** ⇒ 明天关窗读出的「达成/未达成」可信")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
