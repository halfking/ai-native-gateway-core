#!/usr/bin/env python3
"""recovery-detection-p0-3-verdict.py —— Phase 2b 观测层判定器（可复采）

★ 用途：era2（2026-11-10 13:42:30 +08）24h / 7 天窗口关闭后，**一条命令**跑完
  复采 + 对拍 + 判定，输出逐条判据的裁决表。

★ 为什么要有它：判据与基线**在窗口关闭前就写死**（见
  docs/audit/2026-11-10-P0-3验收判据审计.md §3）⇒ ★ 关窗时不该再临时解释口径。
  ⇒ ★ 裁决表把「可判 / 不可判 / 样本不足」也一并输出 ⇒ 避免「关窗才发现判据用不了」。

★ 执行（本地）：
    python3 scripts/audit/recovery-detection-p0-3-verdict.py            # 默认 Stage 1
    python3 scripts/audit/recovery-detection-p0-3-verdict.py --stage 2
    python3 scripts/audit/recovery-detection-p0-3-verdict.py --ssh-host 211
    python3 scripts/audit/recovery-detection-p0-3-verdict.py --selftest          # ★ 合成数据自测
    python3 scripts/audit/recovery-detection-p0-3-verdict.py --inject-guard-fail # ★ 守卫变异验证
    python3 scripts/audit/recovery-detection-p0-3-verdict.py --from-file <f>     # ★ 离线复采

★ 依赖：ssh/scp 可用；211 上的容器 acc-pg17 与在役网关容器可达。
  ★ 唯一权威库连接来自在役容器 env（不凭记忆写连接串）。

★★ 本轮加固（★ 都是「关窗前必须先排除的失败模式」）：
  ① ★★ 「量具算不出」（分位是 `?`）**不能**报成「未达成」⇒ 单独一档「不可判（量具缺数）」。
     （否则关窗时会用一条坏读数把结论判成红的 —— 量具缺陷伪装成修复失败。）
  ② ★★ 守卫**缺失**（指标没产出 / SQL 出错）**不能**当成守卫通过
     ⇒ 守卫值为空 ⇒ 直接判「量具不可信」⇒ 整表作废。
  ③ ★★ 基线展示文字**一律从 BASELINES 取**（★ 硬编码字面量会和基线静默脱钩 —— 本轮踩过）。
  ④ ★★ `--selftest` 用**合成数据**证明「数据到位时判定器真能给出裁决」
     ⇒ 关窗前就知道判定链路是通的，而不是只验证过「数据没到时报样本不足」。
"""
from __future__ import annotations

import argparse
import re
import subprocess
import sys
import tempfile
from pathlib import Path

SCRIPT = Path(__file__).with_name("recovery-detection-p0-3-metrics.sql")

# ★★ 基线（全部来自 T1 锚点，2026-11-10 13:45~14:05 +08 实测；★ 写「方法+观测值」）
#    —— ★ 样本量规则同样预先写死：post episode < 5 ⇒ 「样本不足以判定」，不许外推。
#    —— ★★ 本轮改为 **dict**：基线值 / 期望 / 阈值 / 单位 **同处一源**
#        （★ 本轮踩过「判定器的基线显示文字是硬编码字面量」⇒ 表格与基线静默脱钩）。
BASELINES: dict[str, dict] = {
    # ── 资格面 / 成本面 ─────────────────────────────────────────────
    "M2_featured_static": {"base": 210, "expect": "==210",
                           "note": "featured 集合规模（era2 T0）"},
    "M6_free_featured_pairs": {"base": 80, "expect": ">=80",
                               "note": "免费在服对 featured 命中（★ 资格面，非行为面）"},
    "M9_missing_raw_variant": {"base": 0, "expect": "==0",
                               "note": "展开覆盖完整性"},
    "M5_probe_runs_24h": {"base": 9242, "expect": "<=9334", "limit": 9334,
                          "note": "24h 探测量（爆炸半径；9233×1.01）"},
    # ── 以下是**修正 join 后**的实测值（pre 段 7 天，2026-11-10 14:2x +08）──
    "M11_ns_pre": {"base": (3, 360, 1791), "expect": "<=", "units": "med/p90/max",
                   "note": "namespaced 恢复感知时延 pre（分钟）"},
    "M11_bare_pre": {"base": (3, 180, 360), "expect": "<=", "units": "med/p90/max",
                     "note": "bare 恢复感知时延 pre（分钟）"},
    "M12s_ns_feat_pre": {"base": (3, 60, 1791), "expect": "-", "units": "med/p90/p99",
                         "tag": "★ **参考：post 人群会变**（含 106 个新纳入冷对）",
                         "note": "ns featured 复检间隔 pre（★ 参考）"},
    "M12s_ns_not_pre": {"base": (360, 361, 5524), "expect": "-", "units": "med/p90/p99",
                        "tag": "★ **参考：post 人群会变**（少了 106 个冷对）",
                        "note": "ns 非 featured 复检间隔 pre（★ 参考）"},
    # ★★★ M12p 的基线必须与 post **同量纲**：★ pending 的定义是「失败之后没有任何后续探测」
    #   ⇒ 观察窗口越长，越容易把「之后一直没被探」的对算成 pending ⇒ ★★ 7 天 pre 与
    #   24h post 直接比是不同量纲（实测 5.8% vs 0.0%）⇒ 改用**切点前 24h**（pre24h）对照。
    "M12p_pending_ns_not_pre24": {"base": 0.0, "expect": "<=1.0", "limit": 1.0,
                                   "min_n": 30, "suffix": "%",
                                   "note": "ns 非 featured 待复检率 pre24h（★ 与 post 同量纲）"},
    # ★★ M14 三个组（★ population-stable：st_old ⊆ st_new ⇒ pre/post 是同一批对）
    #    ★★ **每个窗口各有一套基线**（★ 同量纲）：★ post 段的长度随 stage 变
    #      （Stage 1 = 24h、Stage 2 = 7 天）⇒ ★ 基线必须用**同样长度**的 pre 段
    #      ⇒ 实测两窗口的分布**截然不同**（newly 组：pre24 slow=100%（14 条）
    #      vs pre7d slow=13.0%（461 条））⇒ ★★ 借用另一窗口的基线 = 判错
    # ★★★ **窗口四桶**（★ 本轮第 10 处修正）：★ 'pre7d' 原来实际只覆盖
    #      [切点-7d, 切点-24h) ⇒ **只有 6 天** ⇒ Stage 2 拿「6 天 pre」比「7 天 post」
    #      ⇒ **不同量纲**（★ 这正是本轮纪律的核心）⇒ 现在 **pre7d = 整 7 天**
    #      （★ 不变量：pre7d = pre6d + pre24 ⇒ 可对拍）
    "M14_old_feat_pre24": {"n": 94, "base": (3, 31, 360), "expect": "<=", "units": "med/p90/p99",
                            "slow": 6.4, "note": "旧 featured 人群（★ Stage 1 无回归基线，24h）"},
    "M14_old_feat_pre7d": {"n": 394, "base": (3, 60, 1791), "expect": "<=", "units": "med/p90/p99",
                            "slow": 8.1, "note": "旧 featured 人群（★ Stage 2 无回归基线，★ 整 7 天）"},
    "M14_newly_covered_pre24": {"n": 14, "base": (360, 360, 360), "expect": "<=", "units": "med/p90/p99",
                         "slow": 100.0, "post_p90_max": 180,
                         "note": "新覆盖人群（★ Stage 1 效应基线，24h；★ **free 切片仅 14 条 ⇒ 不足** ⇒ 幅度留 Stage 2）"},
    "M14_newly_covered_pre7d": {"n": 461, "base": (2, 360, 1390), "expect": "<=", "units": "med/p90/p99",
                         "slow": 13.0, "post_p90_max": 180,
                         "note": "新覆盖人群（★ **效应判据**：pre p90=360 → post 期望 ≤180；★ 整 7 天）"},
    "M14_other_pre24": {"n": 311, "base": (360, 360, 360), "expect": "-", "units": "med/p90/p99",
                         "slow": 77.2, "tag": "★ 对照项：节奏不该变",
                         "note": "对照组（★ Stage 1 基线，24h）"},
    "M14_other_pre7d": {"n": 1241, "base": (360, 368, 5524), "expect": "-", "units": "med/p90/p99",
                         "slow": 85.6, "tag": "★ 对照项：节奏不该变",
                         "note": "对照组（★ Stage 2 基线，★ 整 7 天）"},
    # ── ★★★ M14x：**全计费口径**（★ 本轮第 9 处口径修正）
    #   ★★ 为什么要有：★ free 口径只取「有 free 绑定」的对 ⇒ ★★ 同一个模型名同时挂在
    #   free / per_token / token_plan 三种凭据上 ⇒ ★★ **另外两类凭据上的探测被静默丢掉**
    #   ⇒ ★★ 样本量被系统性压缩（★ 与「一对多取代表行」同类的**不报错、只少报**）
    #   ⇒ ★★ 实测 pre24 新覆盖人群：free **14** 条 vs 全计费 **78** 条（5.6×）
    #   ⇒ ★★ **Stage 1 的效应判据在 free 口径下必然「样本不足」，在 all 口径下够样**。
    #   ★★ 两档各有**自己**的基线与阈值 ⇒ ★★ 阈值**不可跨口径混用**（slow 基线差 2×）。
    "M14x_old_feat_pre24": {"n": 226, "base": (4, 31, 360), "expect": "<=", "units": "med/p90/p99",
                             "slow": 8.0, "note": "全计费口径·旧 featured（★ Stage 1 无回归基线）"},
    "M14x_old_feat_pre7d": {"n": 1878, "base": (3, 31, 361), "expect": "<=", "units": "med/p90/p99",
                             "slow": 4.8, "note": "全计费口径·旧 featured（★ Stage 2 无回归基线）"},
    "M14x_newly_covered_pre24": {"n": 78, "base": (360, 360, 361), "expect": "<=", "units": "med/p90/p99",
                                  "slow": 67.9, "post_p90_max": 180,
                                  "note": "全计费口径·新覆盖人群（★ **Stage 1 效应判据**：78 条 ⇒ 够样）"},
    "M14x_newly_covered_pre7d": {"n": 691, "base": (3, 360, 5517), "expect": "<=", "units": "med/p90/p99",
                                  "slow": 27.8, "post_p90_max": 180,
                                  "note": "全计费口径·新覆盖人群（★ Stage 2 效应判据）"},
    "M14x_other_pre24": {"n": 972, "base": (360, 360, 360), "expect": "-", "units": "med/p90/p99",
                         "slow": 74.0, "tag": "★ 对照项：节奏不该变",
                         "note": "全计费口径·对照组（★ Stage 1 基线）"},
    "M14x_other_pre7d": {"n": 3792, "base": (360, 368, 5524), "expect": "-", "units": "med/p90/p99",
                         "slow": 84.4, "tag": "★ 对照项：节奏不该变",
                         "note": "全计费口径·对照组（★ Stage 2 基线）"},
    # ── ★★★★ M20：**分层效应指标**（★ 本轮第 11 处口径修正，★★ 差点造成结构性假阴性）
    #   ★★ 为什么必须分层（★ 实测，211 真库 pre7d 全计费口径新覆盖组 691 条失败事件）：
    #     · **exempt（豁免类）537 条 = 78%** ⇒ 协议形态（404/405/410/415、400/422 非配额体
    #       ⇒ classifyProbeRootCause ⇒ ProbeRootCauseProtocol）+ 配额钉住
    #     · ★★ 豁免类**设计上就钉在 6h**（modelNotServedRecheckInterval）且**豁免 featured 乘数**
    #       （bg/probe_service.go:582 的两个例外）⇒ ★★ post 的混合 p90 必然仍 ≈360
    #       ⇒ ★★ M14x 的「post p90 ≤180 / slow ≤pre/2」**结构性不可达 ⇒ 假阴性**
    #   ★★ ⇒ 效应判据改到 **normal 子集**（★ 乘数应当生效的那一类）；exempt 子集作**不回归守卫**
    #   ★★ **机制级正控（已实测）**：pre 期同刻同形态，old_feat 阶梯全是减半值（150/1800/10800），
    #      newly 全是未减半值（300/3600/7200/21600）⇒ 乘数是唯一解释；post 期 newly 出现 10800/1800
    #   ★★ **调度余量实测**：观察间隔 − 阶梯值 ⇒ 中位 0 分钟、p90 1 分钟 ⇒ 零余量不来自调度
    "M20_normal_pre6d": {"n": 133, "base": (3, 360, 12.0, 17), "expect": "-",
                          "units": "med_gap/p90_gap/slow_pct/top21600_n",
                          "tag": "★ 分层：乘数应当生效的那一类",
                          "note": "新覆盖组·非豁免（★ 桶间加法用）"},
    "M20_normal_pre24": {"n": 21, "base": (60, 360, 14.3, 3), "expect": "post:未减半顶格=0 且 慢事件≤pre/2",
                          "units": "med_gap/p90_gap/slow_pct/top21600_n",
                          "tag": "★ **效应判据**（Stage 1 同量纲，24h）",
                          "note": "新覆盖组·非豁免（★ Stage 1 效应基线）"},
    "M20_normal_pre7d": {"n": 154, "base": (3, 360, 12.3, 20), "expect": "post:未减半顶格=0 且 慢事件≤pre/2",
                          "units": "med_gap/p90_gap/slow_pct/top21600_n",
                          "tag": "★ **效应判据**（Stage 2 同量纲，★ 整 7 天）",
                          "note": "新覆盖组·非豁免（★ Stage 2 效应基线）"},
    "M20_exempt_pre6d": {"n": 480, "base": (2, 360, 25.6, 124), "expect": "-",
                          "units": "med_gap/p90_gap/slow_pct/top21600_n",
                          "tag": "★ 分层：设计上不该被减半（6h 停放）",
                          "note": "新覆盖组·豁免（★ 桶间加法用）"},
    "M20_exempt_pre24": {"n": 57, "base": (360, 360, 87.7, 54), "expect": "post:慢事件≤pre×1.25 且 p90≤pre",
                          "units": "med_gap/p90_gap/slow_pct/top21600_n",
                          "tag": "★ 不回归守卫（Stage 1 同量纲，24h）",
                          "note": "新覆盖组·豁免（★ Stage 1 不回归基线）"},
    "M20_exempt_pre7d": {"n": 537, "base": (3, 360, 32.2, 178), "expect": "post:慢事件≤pre×1.25 且 p90≤pre",
                          "units": "med_gap/p90_gap/slow_pct/top21600_n",
                          "tag": "★ 不回归守卫（Stage 2 同量纲，★ 整 7 天）",
                          "note": "新覆盖组·豁免（★ Stage 2 不回归基线）"},
    # ── ★★★★ M15x：**逐对金丝雀·分层口径**（★ 本轮第 13 处口径修正）──
    #   ★★ 为什么新增：★ M15 旧键是 **free 切片** ⇒ 实测**四只同形**（pre7d 各 12 豁免 + 1 normal）
    #     ⇒ 剔豁免后**只剩 1 条** ⇒ ★★ 逐对判据在 free 口径**既不可达又样本不足**
    #     ⇒ ★ 而**全计费口径**下 deepseek 的 normal 有 **41 条（pre7d）/ 18 条（pre24）**
    #     ⇒ ★★ 它是**唯一**一只可判的金丝雀 ⇒ 逐对判据只能落在它身上
    #   ★★ base 元组 = (normal 复检中位分钟, normal 阶梯中位秒, 未减半档 3600 计数, 未减半档 21600 计数)
    #     ★★ 阈值**全部由 pre 实测推导**（post ≤ pre/2）⇒ 不拍脑袋
    #   ★★ **有牙性**：★ 只有「pre 期该档位计数 > 0」的档位才参与「未减半档位清零」判定
    #     ⇒ ★ pre 计数 = 0 的档位在 post 侧**恒真** ⇒ 判定器必须标注「★ 恒真 ⇒ 不作判据」
    #     （★ 否则就是**恒真判据**＝门没牙 ⇒ ★ 本轮纪律的第三次应用）
    "M15x_pre24__deepseek-ai/deepseek-v4-pro-0813": {
        "n": 27, "base": (60, 3600, 18, 0, 0),
        "expect": "post:normal med≤pre/2 且 阶梯p50≤pre/2 且 未减半档位清零",
        "units": "med_min/ladder_p50_s/top3600_n/top7200_n/top21600_n",
        "tag": "★ **逐对判据**（唯一 normal 样本足够的金丝雀）",
        "note": "★ 全计费口径·非豁免层（★ Stage 1 同量纲，24h）"},
    "M15x_pre24__minimaxai/minimax-m2.7": {
        "n": 11, "base": (0, 0, 0, 0, 0),
        "expect": "-", "units": "med_min/ladder_p50_s/top3600_n/top7200_n/top21600_n",
        "tag": "★ 观察面（★ normal 层为空 ⇒ 样本不足）",
        "note": "★ 全计费口径·非豁免层（★ Stage 1）"},
    "M15x_pre24__minimaxai/minimax-m3": {
        "n": 9, "base": (0, 0, 0, 0, 0),
        "expect": "-", "units": "med_min/ladder_p50_s/top3600_n/top7200_n/top21600_n",
        "tag": "★ 观察面（★ normal 层为空 ⇒ 样本不足）",
        "note": "★ 全计费口径·非豁免层（★ Stage 1）"},
    "M15x_pre24__z-ai/glm-5.3": {
        "n": 9, "base": (0, 0, 0, 0, 0),
        "expect": "-", "units": "med_min/ladder_p50_s/top3600_n/top7200_n/top21600_n",
        "tag": "★ 观察面（★ normal 层为空 ⇒ 样本不足）",
        "note": "★ 全计费口径·非豁免层（★ Stage 1）"},
    "M15x_pre7d__deepseek-ai/deepseek-v4-pro-0813": {
        "n": 77, "base": (60, 3600, 31, 0, 10),
        "expect": "post:normal med≤pre/2 且 阶梯p50≤pre/2 且 未减半档位清零",
        "units": "med_min/ladder_p50_s/top3600_n/top7200_n/top21600_n",
        "tag": "★ **逐对判据**（唯一 normal 样本足够的金丝雀）",
        "note": "★ 全计费口径·非豁免层（★ Stage 2 同量纲，★ 整 7 天）"},
    "M15x_pre7d__minimaxai/minimax-m2.7": {
        "n": 48, "base": (360, 21600, 0, 0, 1),
        "expect": "-", "units": "med_min/ladder_p50_s/top3600_n/top7200_n/top21600_n",
        "tag": "★ 观察面（★ normal 仅 1 条 ⇒ 样本不足）",
        "note": "★ 全计费口径·非豁免层（★ Stage 2）"},
    "M15x_pre7d__minimaxai/minimax-m3": {
        "n": 43, "base": (360, 21600, 0, 0, 2),
        "expect": "-", "units": "med_min/ladder_p50_s/top3600_n/top7200_n/top21600_n",
        "tag": "★ 观察面（★ normal 仅 2 条 ⇒ 样本不足）",
        "note": "★ 全计费口径·非豁免层（★ Stage 2）"},
    "M15x_pre7d__z-ai/glm-5.3": {
        "n": 71, "base": (361, 21600, 0, 0, 1),
        "expect": "-", "units": "med_min/ladder_p50_s/top3600_n/top7200_n/top21600_n",
        "tag": "★ 观察面（★ normal 仅 1 条 ⇒ 样本不足）",
        "note": "★ 全计费口径·非豁免层（★ Stage 2）"},
}

# ★ 三个「冷金丝雀」：目标人群（106 个新覆盖变体）里仍被探测到的冷对
CANARIES = [
    "minimaxai/minimax-m2.7",
    "minimaxai/minimax-m3",
    "deepseek-ai/deepseek-v4-pro-0813",
    "z-ai/glm-5.3",          # ★ 修正 join 后才发现它（pre 13 events / med 360 min）
]
CANARY_PRE_MED = 360          # ★ pre 段实测中位（各 13 次/7 天）
CANARY_POST_MAX_MED = 180     # ★ 期望：featured backoff 减半 ⇒ ≤180 分钟
# ★★ M15x 逐对判据的**数据驱动分派**参数（★ 本轮第 13 处口径修正）
#   ★ CANARY_MIN_NORMAL：★ normal（非豁免）层的最小样本量 ⇒ 低于此 ⇒ 该只**降为观察面**
#     （★ 实测：★ free 切片下四只剔豁免后各只剩 0~1 条 ⇒ 不可判；★ 全计费口径下
#       ★ 只有 deepseek 达标（pre7d 41 条 / pre24 18 条））
#   ★ CANARY_LADDER_STEPS：★ 阶梯链里 ≥60s 的**未减半档位**
#     （★ 链 = 5s,30s,60s,5m,1h,2h,6h ⇒ ★ 秒 = 300/3600/7200/21600 ⇒ 减半值 = 150/1800/3600/10800）
CANARY_MIN_NORMAL = 10
CANARY_LADDER_STEPS = (3600, 7200, 21600)
# ★★ 合成数据里**唯一** normal 样本足够的金丝雀（★ 真库实测：★ pre7d normal 41 条 / pre24 18 条）
#   ★★ 其余三只的 normal 只有 0~2 条 ⇒ 判定器**数据驱动**地判它们为观察面
#   ★★ 本常量只用于**自测**（★ 合成数据是固定的 ⇒ 可硬编码；★ 真实判定不依赖它）
CANARY_JUDGEABLE = "deepseek-ai/deepseek-v4-pro-0813"

EPOCH = "2026-11-10 13:42:30+08"


# ─────────────────────────────────────────────────────────────────────
# 采集 / 解析
# ─────────────────────────────────────────────────────────────────────
def run_metrics(host: str) -> str:
    """把验收脚本送到 211 → 容器 → psql 执行，返回原始输出。"""
    with tempfile.TemporaryDirectory() as td:
        local = Path(td) / "/tmp" / "m.sql" if False else Path(td) / "m.sql"
        local.write_text(SCRIPT.read_text(encoding="utf-8"))
        subprocess.run(["scp", "-q", str(local), f"{host}:/tmp/m.sql"], check=True)
        cmd = (
            "docker cp /tmp/m.sql acc-pg17:/tmp/m.sql && "
            # ★★ psql 必须 **-v ON_ERROR_STOP=1**（★ 本轮自踩的**真缺陷**：★ 主判定路径**原先没有**这条
            #   ★★ ⇒ 运行期 SQL 错误（如时间戳非法 / 语法错）会被 psql **吞掉**、returncode 仍 0
            #   ★★ ⇒ 输出少了若干行 ⇒ ★★ 看起来像「该键没产出」⇒ ★★ **静默失效**
            #   ★★ 且这类失效会被读成「量具缺数 / 样本不足」⇒ ★★ **假阴性**
            #   ★★ 同族脚本 clockcheck.py / negcontrol.py 里本来就有这一条 ⇒ ★ 要抄，不要重新发明）
            "docker exec acc-pg17 psql -U acc_super -d llm_gateway -tA -F'|' -v ON_ERROR_STOP=1 -f /tmp/m.sql"
        )
        out = subprocess.run(["ssh", host, cmd], capture_output=True, text=True)
        if out.returncode != 0:
            sys.stderr.write(out.stderr)
            raise SystemExit("验收脚本执行失败（★ psql ON_ERROR_STOP=1 ⇒ 运行期错误 ⇒ "
                             "**采集失败**，★ 不是「缺键」⇒ ★ 先修采集口径）")
        return out.stdout


def parse(output: str) -> dict[str, str]:
    data: dict[str, str] = {}
    for line in output.splitlines():
        if "|" not in line:
            continue
        k, _, v = line.partition("|")
        data.setdefault(k.strip(), v.strip())
    return data


def num(text: str) -> float | None:
    m = re.search(r"-?\d+(?:\.\d+)?", text or "")
    return float(m.group()) if m else None


def last_num(text: str) -> float | None:
    """取文本里**最后一个**数字（用于 '34 rp + 15 canon + 200 raw = 210' 这类复合串）。"""
    nums = re.findall(r"-?\d+(?:\.\d+)?", text or "")
    return float(nums[-1]) if nums else None


def median_retry(text: str) -> float | None:
    """★ M3b 的行是 '2 rows | median_retry_s=300' ⇒ 必须取 median_retry_s，
    ★ **不能**取第一个数字（那是行数）—— ★ 本轮实测踩过这个坑。"""
    m = re.search(r"median_retry_s=(\d+)", text or "")
    return float(m.group(1)) if m else None


def pending_pct(text: str) -> float | None:
    """★ M12p 的值形如 '581 failures_aged6h | pending=1 | pending_pct=0.2'
    ⇒ ★ **不能**按位置取数字：'0.2' 会被切成 0 和 2（实测踩坑形态）⇒ 必须定点取。"""
    m = re.search(r"pending_pct=([\d.]+)", text or "")
    return float(m.group(1)) if m else None


def head_count(text: str) -> int | None:
    """★ 取**行首计数**（'394 events | …' ⇒ 394）。
    ★ **不能**用 `re.findall(r'\\d+')` 取第一个数字 —— 见 field() 的说明。"""
    m = re.match(r"\s*(\d+)", text or "")
    return int(m.group(1)) if m else None


def field(text: str, *names: str) -> float | None:
    """★★ 从 `k=v` 字段**定点**取值。

    ★★ 为什么不能用 `re.findall(r'\\d+')` 按位置取（本轮自测抓到的真缺陷）：
      '394 events | med_min=30 | p90=60 | p99=1200'
      ⇒ findall 得 [394, 30, **90**, **60**, **99**, 1200]
      ⇒ ★★ 'p90=' / 'p99=' 的**标签里自带数字** ⇒ 按位置取会
         · 把 p90 读成 **90**（标签里的 90）、p99 读成 **60**（真 p90）
         ⇒ ★★ M12s 判据「p90 ≤ 60 且 p99 ≤ 1791」会**恒假**
         ⇒ ★★ 关窗时无论真实数据如何，M12s 都会被判成「尾部变差」（假红）。
      同一形态还会把 M11 的 `max` 读成真 p90 ⇒ **max 校验被静默削弱**。
    ⇒ ★ 一律按字段名定点取；`p90_min` / `p90` 两种写法都要认。
    """
    for n in names:
        m = re.search(rf"\b{n}(?:_min)?=(-?\d+(?:\.\d+)?)", text or "")
        if m:
            return float(m.group(1))
    return None


def field_str(text: str, name: str) -> str | None:
    """★ 取**非数值**字段（★ 如 `unexplained_values=-` / `30630,30658`）"""
    m = re.search(rf"\b{re.escape(name)}=([^|]*)", text or "")
    return m.group(1).strip() if m else None


def m5_baseline_disclosure(data: dict[str, str], n, lim) -> str:
    """★★★ 第 22 处口径修正：★ M5 的「**基线口径**」披露（★ 只披露，★ **不改 base/limit**）

    ★★ **问题（★ 实测）**：★ M5 的 base = **pre24 的单日快照**（9242）⇒ ★ 而探测量**按天剧烈波动**
    ★   （★ 实测 pre7d 逐日：min=**1292** / max=**9905** ⇒ **8× 差**；★ 且 **pre 期实测 11-09 = 9683 > limit 9334**）
    ★   ⇒ ★★ ⇒ **该阈值在 pre 期就不成立** ⇒ ★★ ⇒ M5 判 ❌ 时**不能**读成「修复引起爆炸半径」
    ★ ★★ **处置（★ 本轮纪律）**：★ **不改 base/limit**（★ 关窗前**不重冻**已验证基线）
    ★   ⇒ ★★ 只在行内把「**基线口径问题**」摆出来 ⇒ ★★ **裁决符号保持不变**（★ ❌ 仍是 ❌ ⇒ 门不钝化）
    ★   ⇒ ★★ 真正的改进项（用 ≥7 天的均值/中位/分位做基线）登记为 **Stage 2 之后的基线口径改进项**
    """
    pre_raw = data.get("M5_pre7d_runs_by_day") or ""
    post_raw = data.get("M5_post_runs_by_day") or ""
    # ★★ **键缺失 ⇒ 显式说「不可见」**（★ 不得默认「阈值成立」⇒ ★ 那就是静默判绿）
    if not pre_raw.strip():
        return ("★ 基线口径：★★ **逐日分布键未产出** ⇒ ★★ **基线口径不可见**"
                "（★ ⇒ 本行 ❌ 只能读成「超预算」这一事实，★ **不得**据此断言「修复引起爆炸半径」，"
                "★ 也**不得**默认「阈值在 pre 期成立」）")
    days_txt = field_str(pre_raw, "days") or "-"
    dmin = field_str(pre_raw, "min") or "?"
    dmax = field_str(pre_raw, "max") or "?"
    nd = field_str(pre_raw, "n_days") or "?"
    # ★★ 逐日超预算天数（★ 由判定器自己的 limit 计算 ⇒ 阈值只有一个来源）
    over = 0
    total_days = 0
    for part in days_txt.split(","):
        if ":" not in part:
            continue
        total_days += 1
        try:
            if int(part.rsplit(":", 1)[1]) > int(lim):
                over += 1
        except (TypeError, ValueError):
            pass
    # ★★ 波动倍数（★ 读数异常时不得炸掉披露 ⇒ 兜底写「?」）
    try:
        ratio = f"{float(dmax) / float(dmin):.1f}×"
    except (TypeError, ValueError, ZeroDivisionError):
        ratio = "?"
    post_days = field_str(post_raw, "days") or "-"
    # ★★★ 第 31 处：★ 「**等长 24h 口径**」⇒ ★ 与 M5 的裁决窗口**同量纲**
    #   ★★ 「跨段比必须等长」（★ LESSONS §667 / §679）—— ★ 日历日桶里首尾是 partial day ⇒ ★★ 不能当 24h 基线
    _dist_raw = data.get("M5_roll24_pre7d_dist") or ""
    _p50 = field_str(_dist_raw, "p50")
    _p75 = field_str(_dist_raw, "p75")
    _p90 = field_str(_dist_raw, "p90")
    _dn = field_str(_dist_raw, "n")
    try:
        _p50v, _p75v, _p90v = float(_p50), float(_p75), float(_p90)
    except (TypeError, ValueError):
        _p50v = _p75v = _p90v = None
    if _p50v is None:
        eq_txt = ("★ ★★ **等长 24h 基线不可见**（★ 键 `M5_roll24_pre7d_dist` 未产出 ⇒ ★★ "
                  "**不得**默认本行读数在 pre 分布里的位置）")
    else:
        try:
            _nv = float(n)
        except (TypeError, ValueError):
            _nv = None
        if _nv is None:
            _bucket = "**本行读数不可见 ⇒ 位置不可判**"
        elif _nv > _p90v:
            _bucket = "**高于 p90**"
        elif _nv > _p75v:
            _bucket = "**p75–p90 之间**"
        elif _nv > _p50v:
            _bucket = "**p50–p75 之间**"
        else:
            _bucket = "**低于 p50**"
        eq_txt = (f"★ ★★ **等长 24h 口径**（★ pre7d 的 **{_dn}** 个**等长**滚动 24h 窗 ⇒ ★ 与本行**同量纲**）："
                  f"★ p50 = **{_p50v:g}** / p75 = **{_p75v:g}** / p90 = **{_p90v:g}**"
                  f" ⇒ ★★ **本行读数 = {n}** ⇒ ★★ 落在 {_bucket}"
                  f"（★ 窗口：post = 滚动 24h（now-24h→now）/ pre = [切点-7d, 切点] 内的等长 24h 窗）")
    # ★★★ 第 31 处：★ pre24 侧 by_kind（★ 来自**独立夹具**的期望值 ⇒ ★★ 判据与被守卫内容不同源）
    #   ★★★ ★★ **方向必须从 live 数据算出来**（★ 硬编码「periodic 反降」⇒ ★★ 关窗时若方向变了 ⇒ ★★ 与真实 by_kind 冲突）
    _m5fx = _load_m5_fixture()
    _post_kind_raw = (data.get("M5_probe_runs_24h_by_kind") or "").strip()
    if _m5fx and _post_kind_raw:
        _kk = []
        for _k, _fxk in (("credential_recovery", "pre24_kind_credential_recovery"),
                         ("periodic", "pre24_kind_periodic"),
                         ("sync_request", "pre24_kind_sync_request")):
            _pv = _m5fx.get(_fxk, "")
            _qv = field_str(_post_kind_raw, _k)
            try:
                _pn, _qn = float(_pv), float(_qv)
                _d = _qn - _pn
                _pc = f"{(_d / _pn * 100):+.1f}%" if _pn else "?"
                _kk.append(f"★ {_k} **{_pv} → {_qv}**（**{_pc}**）")
            except (TypeError, ValueError, ZeroDivisionError):
                _kk.append(f"★ {_k} **{_pv} → {_qv or '?'}**")
        # ★★ **方向结论由 live 值推出**（★ 不硬编码）
        try:
            _dp = float(field_str(_post_kind_raw, "periodic")) - float(_m5fx.get("pre24_kind_periodic"))
        except (TypeError, ValueError):
            _dp = None
        _dir = ("★ ★★ **periodic 反降**（★ 爆炸半径的直接代理是 periodic 调度量 ⇒ ★ 它降 ⇒ ★ 不是调度失控）"
                if (_dp is not None and _dp < 0) else
                "★ ★★ **periodic 未下降**（★ ⇒ ★★ 「爆炸半径失控」这一读法**不能**被排除 ⇒ ★★ 须按真实增量对待）")
        kind_txt = (f"⇒ ★★ **pre24 → post24 的 by_kind 对拍**（★ pre 侧锚定切点 ⇒ 时钟无关；★ post 侧 = 本行窗口）："
                    f"{' ｜ '.join(_kk)} ⇒ ★★ {_dir}")
    else:
        kind_txt = ("⇒ ★★ **by_kind 对拍不可见**（★ 期望值登记表或 post 侧 by_kind 键读不到 ⇒ ★★ "
                    "增量来源不可分辨 ⇒ ★★ 不得断言归因）")
    txt = (f"★ 基线口径：★ M5 的 base = **pre24 单日快照**（9242）⇒ ★ 而探测量**按天波动**"
           f"（★ pre7d 逐日（★ 按**日历日**切 ⇒ 7 天窗口跨 8 个日历日）min={dmin} / max={dmax}"
           f" ⇒ **{ratio} 差距**；★ n_days={nd}）"
           f"⇒ ★★ **pre 期已有 {over}/{total_days} 个日历日超预算 {lim}** ⇒ ★★ 该阈值**在 pre 期就不成立**"
           f"⇒ ★★ 故 **「❌ ⇒ 修复引起爆炸半径」这个读法不成立**"
           f"⇒ ★★★ **★ 但「不可归因于修复」这半也不成立（★ 第 31 处修正）**：★ 增量按 **下面的 by_kind 对拍**"
           f"**看来源**（★ 方向由 live 值推出，★ 不硬编码）⇒ ★★ **正确读法 = 预算口径过时（单日快照）+ 增量按 by_kind 归因**"
           f"⇒ ★★ **既不能读成「爆炸半径失控」，★ 也不能读成「与修复无关」**"
           f"⇒ {eq_txt}"
           f"{kind_txt}"
           f"⇒ ★★ （post 逐日：{post_days}）")
    return txt


def rounding_disclosure(raw_text, threshold, *, slack: float = 0.5, what: str = "") -> str:
    """★★★ 第 26 处口径修正（★ 2026-11-11 02:0x）：★ 「**::int 取整边界**」伪影的行内披露

    ★★ **形态（★ 211 真库实测，★ 不是推演）**：
    ★ · 分钟型量（`recheck_min` / `lat`）是**浮点分钟**（★ 实测精度到**秒**）
    ★   ⇒ 采集侧 `percentile_disc(...)::int` 把它**截到分钟**
    ★ · 判据再拿这个**取整值**与阈值做**精确比较**（★ 如 `p90 ≤ pre_p90`）
    ★ ⇒ ★★ 真实差只要落在 **[−0.5, +0.5) 分钟** 的取整跨界带里 ⇒ 裁决符号就会翻转
    ★ ⇒ ★★ **实测（M20 豁免组）**：post raw p90 = **360.5069** min ⇒ `::int` = **361**
    ★   而基线取整值 = **360** ⇒ 基线真值 ∈ [359.5, 360.5)
    ★   ⇒ ⇒ **真实差 ∈ (0.01, 1.01] 分钟 ⇒ 上界 ≈ 60 秒（= 判据的整个分辨率）** ⇒ ⇒ **判 ❌** ⇒ ★★ **假红**
    ★   ⇒ ⇒ ★★ **更锋利的实测（★ 同一份诊断 SQL 同时读到 pre24 的 raw）**：★ pre24 **raw** p90 = **360.2095**
    ★     ⇒ ⇒ **实测真实差 = 0.2974 分钟 = 17.8 秒** ⇒ ⇒ **比判据的 1 分钟分辨率细 ~3.4 倍** ⇒ ⇒ **那个 ❌ 几乎就是噪声**
    ★ ⇒ ★★ **同款形态在第 23 处已于 M14/M14x 的 `p99 361 vs 360` 上出现** ⇒ ★★ **同一类缺陷**

    ★★★★ **纪律（★ 本轮处置）**：
    ★ · ★ **只披露，不软化裁决符号**（★ ❌ 仍是 ❌ ⇒ ★ 门不钝化）⇒ ★★ 与第 22 处 `m5_baseline_disclosure` 同款体例
    ★ · ★ **关窗前不改判据阈值、不改 pre 基线、不重冻**（★ 真修法见下）
    ★ · ★★ 真修法 = **亚分钟精度比较 + 容差**；★ 且对「**设计上钉住**」的组（如豁免组钉 6h）
    ★   ⇒ 正确形态是**带宽判据**（★ 如 355–365 分钟）⇒ ★★ **登记为 Stage 2 之后**
    """
    if raw_text is None:
        # ★★ raw 读数缺失 ⇒ **显式说「不可见」**（★ 不得静默默认「这就是真差」）
        return (" ★ ⚠️ **取整边界不可见**（★ ★ **raw 读数未产出** ⇒ ★ 无法判定本 ❌ 是否由取整造成"
                " ⇒ ★ 本 ❌ **按面值读**）")
    try:
        raw = float(raw_text)
        thr = float(threshold)
    except (TypeError, ValueError):
        return ""
    diff = raw - thr
    # ★★ **只认「恰好差 1 个取整单位」**的形态（★ obs_int = thr_int + 1 ⇒ raw ∈ [thr+0.5, thr+1.5)）
    if not (0.0 < diff < 1.0):
        return ""
    lo, hi = diff - slack, diff + slack
    what_txt = (f"★（★ 比较量：{what}）" if what else "")
    return (f" ★ ⚠️ **取整边界伪影（第 26 处）**{what_txt}：★ 本行的分钟量是**浮点值**（★ 实测精度到秒）"
            f"，★ 采集侧 `::int` 把它**截到分钟**后再与阈值做**精确比较**"
            f" ⇒ ★★ 观测 raw = **{raw:.4f} 分钟**，取整值 = {round(raw):d}"
            f"，阈值 = {thr:.0f} ⇒ ★★ **表观差 = {diff:.4f} 分钟**"
            f" ⇒ ★★ **真实差 ∈ ({lo:.2f}, {hi:.2f}] 分钟**（★ 上界 ≈ {hi * 60:.0f} 秒）"
            f" ⇒ ★★★ ⇒ **判据的分辨率（1 分钟）比被测抖动（秒级）粗 ~60 倍**"
            f" ⇒ ★★★ ⇒ ★ **本 ❌ 不可读成「行为回归」**（★ 裁决符号**保持 ❌** ⇒ 门不钝化）"
            f" ⇒ ★★ 真修法（亚分钟精度比较 + 容差；★ 对「设计上钉住」的组改用**带宽**判据）"
            f"**登记为 Stage 2 之后**")


def percentile_fragility_disclosure(n_post, n_pre, pct_label: str) -> str:
    """★★★ 第 26 处**附带发现**：★ 「**小样本 + 秩统计量**」⇒ 分位数在窗口未满时极不稳定

    ★★ **形态（★ 211 真库实测，★ 2026-11-11 02:2x）**：★ M14x 旧 featured 行在 **02:00 与 02:20 两次真读里**
    ★★   都判 **❌**（★ post n=34 / p90=360 vs pre p90=31），而**同一时刻的预演（as-of = 关窗时钟）判 ✅**
    ★★   ⇒ ★★ 差别**不在数据**（同一批数据）⇒ ★★ **在成熟度窗口的大小** ⇒ ★★ post 的成熟段只有 ~6.6h
    ★★   ⇒ ★★ 而 6h 批次（★ 协议形态模型 ⇒ 设计上钉 6h）在 post 段占 **17.6%**、在 pre24 只占 **~2.6%**
    ★★   ⇒ ★★ ⇒ **同一批 6h 事件，在小样本里把 p90 顶到 360，在大样本里进不了前 10%**
    ★★ ⇒ ★★ **⇒ 根因是「秩统计量 + 样本量」**：★ p90 = 第 ⌈0.9n⌉ 小的那个值 ⇒ ★ n 小 ⇒ 秩边界一动就跳档
    ★★   （★ 实测：★ n=34 ⇒ p90 秩 = 第 32 位；★ 只差 1 条新事件就可能从 31 跳到 360）
    ★★ ⇒ ★★ ⇒ **窗口未满时的读数 ≠ 关窗读数** ⇒ ★★ 一切以关窗当天真读为准（★ 诚实边界 ㉗）

    ★ ★★ **处置**：★ **只披露，不改裁决符号**（★ ❌ 仍是 ❌ ⇒ 门不钝化）
    ★   ⇒ ★★ 披露里给「post n vs pre n 的样本量倍数」⇒ ★★ 读者可自行判断这条 ❌ 有多脆
    """
    if n_post is None or n_pre is None:
        return ""
    try:
        n_post = float(n_post)
        n_pre = float(n_pre)
    except (TypeError, ValueError):
        return ""
    if n_pre <= 0:
        return ""
    ratio = n_post / n_pre
    if ratio >= 0.5:
        return ""      # ★ 样本量相当 ⇒ 不披露（★ 不得制造噪声）
    return (f" ★ ⚠️ **小样本分位数不稳（第 26 处附带发现）**：★ post n={n_post:.0f} vs pre n={n_pre:.0f}"
            f"（★ **样本量差 {1.0 / ratio:.1f}×**）⇒ ★★ 分位数是**秩统计量**（★ p90 = 第 ⌈0.9n⌉ 小的那个值）"
            f"⇒ ★★ **n 小 ⇒ 秩边界一动就跳档**（★ 实测：★ post n=34 ⇒ p90 秩 = 第 32 位；"
            f"★ 只差 1 条新事件就可能从 31 跳到 360）"
            f"⇒ ★★ ⇒ ★ **本 ❌ 必须与「关窗当天真读」比**（★ 窗口越满 ⇒ n 越大 ⇒ 越稳）"
            f"⇒ ★★ 且 {pct_label} 的**真实差**须配合 ㉭ 的 raw 读数一起读")


def cross_window_disclosure(mature_n, fp_n_all, window_n=None, tail_n=None) -> str:
    """★★★ 第 13 处纪律的**最后一个候选点**：★★ **同一行里并列出现的两个 n 可能来自不同窗口**

    ★★ **形态（★ 211 真库实测，★ 2026-11-11 05:2x）**：★ M20 的 post 段有**两个视图**：
    ★ · **成熟视图**（★ `M20_<层>_post`）：★ 带两道只对「已结算段」成立的过滤
    ★   （★ ① `fail_at < now() - 6h`（成熟度）★ ② `next_at IS NOT NULL`（LEAD 必须找到下一次探测））
    ★   ⇒ ★★ 它覆盖到「读数时刻 − 6h」
    ★ · **指纹视图**（★ `M20_<层>_post_fp`）：★ 无成熟度过滤、无 next_at 要求 ⇒ ★★ 覆盖到「读数时刻」
    ★ ⇒ ★★ 两个视图的 **n 本来就不同**（★ 成熟视图 = fp 视图 − 尾部 6h − 无 next_at 的那部分）
    ★ ⇒ ★★ ⇒ ★★ **把两个 n 并列写在一行、却不标明各自窗口** ⇒ ★★ 读者会自然地拿它们相比
    ★   ⇒ ★★ **读成「fp 比成熟视图多出 X 条 ⇒ 尾部有回归」** ⇒ ★★ **这是误读**
    ★   （★ 那 X 条**本来就该在 fp 里** ⇒ ★★ 指纹分量的设计就是要看见它们）
    ★ ⇒ ★★ ⇒ **行内必须显式给出两个 n 各自的窗口**（★ 这也是第 26 处「跨窗比分位数必须同时报两边的 n」的同款）

    ★ ★★ **处置**：★ **只披露，不改裁决符号**（★ ❌ 仍是 ❌ ⇒ 门不钝化）
    ★ ★★ **不制造噪声**：★ 两个 n 相等 ⇒ 无混淆风险 ⇒ 不披露。
    """
    if mature_n is None or fp_n_all is None:
        return ""
    try:
        mature_n = float(mature_n)
        fp_n_all = float(fp_n_all)
    except (TypeError, ValueError):
        return ""
    if fp_n_all == mature_n:
        return ""      # ★ 两视图同数 ⇒ 不披露（★ 不得制造噪声）
    tail_txt = ""
    if window_n is not None and tail_n is not None:
        try:
            tail_txt = (f"（★ 切分：成熟段 {float(window_n):.0f} 条 + 尾部 6h {float(tail_n):.0f} 条"
                        f" = fp {fp_n_all:.0f} 条）")
        except (TypeError, ValueError):
            tail_txt = ""
    diff_txt = ""
    if fp_n_all > mature_n:
        diff_txt = (f"（★ fp 多出的 {fp_n_all - mature_n:.0f} 条**本来就该在 fp 里**"
                    f" ⇒ ★ **不是「尾部有回归」）")
    return (f" ★ ⚠️ **两个 n 不同窗口（第 13 处纪律）**：★ 成熟视图 n={mature_n:.0f}"
            f"（★ 覆盖到「读数时刻 − 6h」⇒ ★ **慢事件需结算**）"
            f" vs fp 视图 n_all={fp_n_all:.0f}"
            f"（★ 覆盖到「读数时刻」⇒ ★ **指纹无需结算**）{tail_txt}"
            f" ⇒ ★★ **两个 n 不可直接相减/相比**{diff_txt}")


def control_phase_disclosure(data: dict[str, str], scope: str, pre_med: float) -> str:
    """★★★ 第 21 处口径修正：★ 「对照组」的**相位/构成**披露（★ 只披露，★ **不改裁决语义**）

    ★★ **机理（★ 关窗预演抓到的真缺陷，★ 已用真数据证实）**：
    ★ · 对照组里 **~80 只**协议形态模型（★ http 404/410 ⇒ 协议形态 ⇒ 设计上钉 **6h = 21600s** 阶梯）
    ★   以 **6h 节奏**被探（★ 实测 11-09/11-10：13:30 → 19:30 → 01:30 → 07:30 → 13:30）
    ★ · era 切点 = 11-10 **13:42:30** ⇒ ★★ **切点落在两次 6h 批次之间**
    ★   ⇒ ★★ post 窗口的**前 5.6h 里一个 6h 批次都没有**（★ 下一次是 19:30）
    ★   ⇒ ★★ 对照组 post 段只剩 **11 只**（★ timeout/429/500 形态 ⇒ 阶梯 3600）⇒ med=60
    ★   ⇒ ★★ 而 pre24 含 **4 个** 6h 批次（★ ≈960 条 360min）⇒ med=360
    ★ ⇒ ★★ **两窗口的「失败形态构成」不同构** ⇒ ★★ 中位数不可直接比 ⇒ ★★ 是**相位假红**
    ★ ⇒ ★★ 已实测证实：★ 11-10 **19:28–19:31** 那批 6h 周期**如期落地**（★ 阶梯仍是 21600）
    ★   ⇒ ★★ 相位解释成立 ⇒ ★★ **Stage 1 的 24h post 窗口含 ≥1 个完整 6h 周期 ⇒ 构成可比**

    ★★ **为什么只披露、不改裁决**：★ 门**不许**因为「看起来像假红」就软化 ⇒ ★★ 裁决符号保持原样
    ★   （★ ❌ 仍是 ❌）⇒ ★★ 门不钝化；★ 本函数只负责把**构成**摆到行内 ⇒ ★★ 让读者能自行判读。
    ★★ 判定条件（★ **客观可测**，★ 不靠措辞）：★ post 窗口内 **6h 顶格事件数 == 0** 且 **pre med ≥ 300**
    """
    key = ("M14x_" if scope == "x" else "M14_") + "post_other_composition"
    raw = data.get(key)
    if not raw:
        # ★★ 键缺失 ⇒ **必须显式说「看不见」**（★ 否则「相位」问题会被静默吞掉）
        return ("★ ★★ **相位/构成不可见**（★ 键 `%s` 未产出 ⇒ ★ 无法判断 post 与 pre 的"
                "失败形态是否同构 ⇒ ★★ 该行结论**不可归因**）" % key)
    top6h = field(raw, "top21600_n")
    n = field(raw, "n")
    models = field(raw, "models_n")
    slow = field(raw, "slow_gt300_pct")
    spec = field_str(raw, "ladder_spectrum")

    def _i(v):
        try:
            return str(int(float(v)))
        except (TypeError, ValueError):
            return str(v)

    txt = (f"★ 相位/构成：post n={_i(n)} / models={_i(models)} / 6h 顶格事件={_i(top6h)}"
           f" / 慢事件占比={_i(slow)}% / 阶梯谱={spec}")
    try:
        n_int = int(n) if n is not None else None
        t6 = int(top6h) if top6h is not None else None
        # ★★ 触发条件（★ **客观可测**，★ 不靠措辞）：★ pre 是 6h 单一形态（med ≥ 300）
        #   且 **post 的 6h 顶格事件占比 < 20%** ⇒ ★★ post 的**主导失败形态已换人**
        #   ⇒ ★★ 两窗口**不同构** ⇒ ★★ 中位数不可直接比 ⇒ ★★ 该 ❌ **不可归因**
        #   ⇒ ★★ Stage 1 的 24h post 含 ≥1 个完整 6h 周期 ⇒ 占比 ≈85% ⇒ **不触发** ✔
        if (n_int is not None and n_int > 0 and t6 is not None
                and float(pre_med) >= 300 and (t6 / n_int) < 0.2):
            txt += ("★★ ★★ **post 窗口内 6h 顶格事件仅 %s/%s（%.1f%%）**，★ 而 pre med=%s "
                    "由 6h 顶格主导（★ 对照组里 ~80 只协议形态模型以 6h 节奏被探）"
                    "⇒ ★★ 两窗口**形态不同构**（★ post 的主导失败形态已换人）"
                    "⇒ ★★ 该 ❌ **不可归因于修复**（★ 极可能是**相位/窗口覆盖**：★ post 窗口"
                    "不足一个完整 6h 阶梯周期 ⇒ ★ 须补齐 ≥1 个完整周期后复判）"
                    % (top6h, n, 100.0 * t6 / n_int, pre_med))
    except (TypeError, ValueError):
        pass
    return txt


# ─────────────────────────────────────────────────────────────────────
# 裁决措辞
# ─────────────────────────────────────────────────────────────────────
def verdict(ok: bool | None, note: str) -> str:
    if ok is None:
        return f"⚠️ 样本不足/不可判 — {note}"
    return ("✅ 达成 — " if ok else "❌ 未达成 — ") + note


def verdict_missing(note: str) -> str:
    """★★ 「量具算不出」（分位是 `?`）**不是**「未达成」
    ⇒ 单独一档，否则会用一条坏读数把结论判成红的。"""
    return f"⚠️ 不可判（量具缺数） — {note}"


def base_cell(key: str) -> str:
    """★★ 基线展示文字**一律从 BASELINES 取**（★ 硬编码字面量会和基线静默脱钩）。"""
    b = BASELINES.get(key)
    if not b:
        return "—"
    base, expect = b["base"], b["expect"]
    units = b.get("units")
    suffix = b.get("suffix", "")
    if isinstance(base, tuple):
        body = " / ".join(f"{u}={v}" for u, v in zip(units.split("/"), base))
        head = "pre "
    else:
        body = f"{base}{suffix}"
        head = ""
    if expect == "-":
        return f"{head}{body}（{b.get('tag', '★ 对照项 / 参考项')}）"
    if expect == "<=":
        if isinstance(base, tuple):
            return f"{head}{body}（期望 post 各项 ≤ pre）"
        return f"{head}{body}（期望 ≤{base}{suffix}）"
    return f"{head}{body}（期望 {expect}）"


# ─────────────────────────────────────────────────────────────────────
def _sql_key(bkey: str) -> str:
    """★★ 基线键 ⇒ 采集侧产出键（★ **两者键序不同**）

    ★ 基线键是 `M14_<组>_<窗>`（BASELINES 里手写），采集侧产出是 `M14_<窗>_<组>`
    ⇒ ★★ 映射写错 ⇒ ★★ **守卫永远匹配不到 ⇒ 守卫形同虚设 ⇒ 假绿**
    ⇒ ★ 本轮真踩到：M18 守卫首跑报「未产出 pre 基线键」⇒ ★ 差点把一个
      ★ **永远不会报警的守卫**当成「守卫已就位」⇒ ★⇒ 纪律：★ **守卫接上真数据后，
      必须要看到它「真的比对过一次」（m18_seen > 0），否则它等于没接**。
    """
    m = re.match(r"^(M14x?)_(newly_covered|old_feat|other)_(pre24|pre7d)$", bkey)
    if m:
        return f"{m.group(1)}_{m.group(3)}_{m.group(2)}"
    return bkey


# ─────────────────────────────────────────────────────────────────────
# ★★ M20 的**机制指纹**分量（★ 本轮第 18 处口径修正，★★ 见主档 §5.9.21.13.14）
# ─────────────────────────────────────────────────────────────────────
def fingerprint_component(fobs: str | None, pobs: str | None,
                          min_n: int = 10) -> tuple[bool | None, str]:
    """★★ 机制指纹：★「未减半 6h 顶格是否清零」+ ★「有没有观察到减半独占值」

    ★★ **为什么读 fp 视图**（★ 第 18 处口径修正）：
    ★★ 阶梯值 `next_retry_seconds` 随**失败 run 即落库**（`bg/node_probe.go:3043`）
    ★★ ⇒ ★★ 指纹**不需要等下一次探测** ⇒ 可从**未成熟段**读
    ★★ ⇒ ★★ post 窗口的**尾部 6h 不再失明**（★ 否则尾部若发生回归 ⇒ 指纹仍读 0 ⇒ ★★ **假绿**）
    ★★ ⇒ ★★ 与第 17 处修正同族：★ **过滤掉尾部 ⇒ 偏差偏向判绿**

    ★★ **三态判定**（★ 关键：★「0」既可能是「生效」也可能是「没样本」）：
    ★★ ① `top21600_n > 0`                  ⇒ ❌ 仍有**未减半**的 6h 顶格 ⇒ 乘数未完全作用
    ★★ ② `top21600_n == 0` 且有**正向证据** ⇒ ✅ 观察到**减半独占值**（★ ∉ 未减半空间 ⇒ 只能是乘数产生的）
    ★★ ③ `top21600_n == 0` 且**无正向证据** ⇒ ⚠️ **不可判**（★ **不得**读成「已生效」）

    ★★ **量具正控**（★ pre 侧同口径）：★ pre 侧若连「未减半顶格」都读不到 ⇒ ★ 量具**无鉴别力**
    ★★   ⇒ ★★ post 侧的 0 **不可解释** ⇒ ★★ fail-closed（判不可判，★ **不判绿**）
    """
    if fobs is None:
        return None, ("★ 指纹键**未产出**（★ 先查采集侧是否还带着 6h 成熟度过滤 / next_at 要求"
                      " ⇒ ★ **不是「量具缺数」**）")
    n_all = field(fobs, "n_all")
    f_top = field(fobs, "top21600_n")
    f_10800 = field(fobs, "top10800_n")
    f_ev = field(fobs, "mul_evidence_n")
    if f_top is None:
        return None, "★ 指纹键算不出 top21600_n ⇒ 不可判"
    # ★★ 量具正控：★ pre 侧同口径必须能读到「未减半顶格」⇒ ★ 否则量具无鉴别力
    ctl_ok = True
    if pobs is None:
        ctl_ok = False
        ctl_txt = " ★⚠️ **pre 侧指纹键未产出** ⇒ ★ 无法做量具正控"
    else:
        p_top = field(pobs, "top21600_n")
        if p_top is None:
            ctl_ok = False
            ctl_txt = " ★⚠️ **pre 侧指纹键算不出 top21600_n** ⇒ ★ 无法做量具正控"
        elif p_top <= 0:
            ctl_ok = False
            ctl_txt = (" ★⚠️ **量具无鉴别力**（★ pre 侧同口径的未减半顶格也是 0"
                       " ⇒ ★ post 的 0 **不可解释**）")
        else:
            ctl_txt = (f" ★（★ 量具正控：pre 侧同口径读到未减半顶格 {p_top:.0f} 条"
                       f" ⇒ 量具有鉴别力）")
    n_txt = f"（★ 指纹样本 n_all={n_all:.0f}）" if n_all is not None else ""
    if f_top > 0:
        return False, (f"★ 未减半 6h 顶格 {f_top:.0f} 条（期望 0）⇒ ★ 仍有未减半顶格"
                       f"（乘数未完全作用）{n_txt}{ctl_txt}")
    if (f_ev or 0) > 0:
        txt = (f"★ 未减半 6h 顶格 0 条（期望 0）⇒ ★ 阶梯已减半（乘数生效）"
               f"（★ **正向证据**：观察到减半独占值 {f_ev:.0f} 条，★ 其中减半后的 3h 顶格 "
               f"{f_10800 or 0:.0f} 条 ⇒ ★ 该值 ∉ 未减半空间 ⇒ ★ 只能是乘数作用产生的）"
               f"{n_txt}{ctl_txt}")
        if not ctl_ok:
            return None, txt + " ★★ ⇒ ★ 但量具正控未过 ⇒ ★ 不可判（★ 不判绿）"
        if n_all is not None and n_all < min_n:
            return None, txt + f" ★★ ⇒ ★ 样本 n_all<{min_n} ⇒ ★ 只给方向（★ 不许外推）"
        return True, txt
    return None, ("★ 未减半 6h 顶格 0 条，★ 但**未观察到任何减半独占值**"
                  "（★ 既不能证实也不能证伪 ⇒ ★ 不可判，★ **不得**读成「已生效」）"
                  f"{n_txt}{ctl_txt}")


# ─────────────────────────────────────────────────────────────────────
# 判定主体（★ 纯函数：输入 = 复采结果字典，输出 = 裁决行 + 守卫状态）
# ─────────────────────────────────────────────────────────────────────
def judge(data: dict[str, str], stage: int = 1,
          inject_guard_fail: bool = False) -> tuple[list[tuple[str, str, str, str]],
                                                    list[str], list[tuple[str, str]]]:
    # ── ★★ 先看量具守卫：守卫不过 ⇒ **整张裁决表作废** ──────────────
    # ★★ 判据基线的**同量纲窗口**（★ 本轮第 5 处口径修正的纪律）
    #   ★ Stage 1 的 post 是 24h ⇒ 用 **pre24**；★ Stage 2 的 post 是 7 天 ⇒ 用 **pre7d**
    #   ★ 借用另一窗口的基线 = 判错（★ 实测 pre24 与 pre7d 的分布差 4×）
    #   ★★ 定义放在**最前面**（★ M15x 逐对段也要用它 ⇒ 晚定义会 NameError）
    win = "pre24" if stage == 1 else "pre7d"
    guards = []
    g16 = data.get("M16_join_caliber_check", "")
    g17 = data.get("M17_event_definition_check", "")
    guards.append(("M16 绑定 join 口径", g16))
    guards.append(("M17 事件定义", g17))
    if inject_guard_fail:
        # ★ 自测：模拟守卫失败（★ 只改判定，不改采集）
        g16 = "used=13563 ref=99999 | ★★ **量具漂移（注入）**"
        guards[0] = ("M16 绑定 join 口径", g16)
    # ★★★ M18 守卫（★ 本轮新增）：★ **pre 基线冻结一致性**
    #   ★★ pre 基线是**历史事实**：★ 本轮改了 10 处采集口径 ⇒ ★★ 基线必须同步改
    #   ★★ 但「基线与实测读数不一致」本身就是**口径漂移**的信号 ⇒ ★★ 把**冻结的事件数**
    #   ★★ 写进 BASELINES ⇒ ★★ 每次跑判定都自动对拍（★ 不靠人记得核对）
    #   ★★ 本轮真踩过：'pre7d' 实际只有 6 天（447 vs 真 461）⇒ ★ 若无此守卫，
    #   ★★ **基线显示（6d 的 10.3%）与判据阈值（7d 的 13.0% 折半）会静默脱钩** ⇒ 永远发现不了
    m18_bad: list[str] = []
    m18_seen = 0
    for bkey, b in BASELINES.items():
        exp_n = b.get("n")
        if exp_n is None:
            continue
        skey = _sql_key(bkey)
        raw = data.get(skey)
        if raw is None:
            continue          # ★ 该键没产出（离线复采 / 部分数据）⇒ 不判失败
        m18_seen += 1
        got = head_count(raw)
        if got is None or got != exp_n:
            m18_bad.append(f"{skey}: 期望 {exp_n} 实得 {got}")
    if m18_bad:
        g18 = "★★ **pre 基线漂移**：" + "; ".join(m18_bad)
    elif m18_seen == 0:
        g18 = "（未产出 pre 基线键 ⇒ 本次未对拍）"
    else:
        g18 = f"✔ {m18_seen} 个 pre 基线键与冻结值一致"
    guards.append(("M18 pre 基线冻结一致性", g18))
    # ★★★ M19 守卫（★ 本轮新增）：★★ **口径的结构性不变量**（★ 不需要知道冻结值就能查）
    #   · ★★ **all 口径的样本空间是 free 的超集** ⇒ all 事件数必须 ≥ free 事件数
    #     （★ 一旦 all < free ⇒ 两套口径自相矛盾 ⇒ 判据读数不可信）
    #   · ★★ **桶间加法**：pre7d = pre6d + pre24（★ 标签与边界不符 ⇒ 必破）
    #   ★★ 这两条是**结构性质**⇒ ★★ 换 era 切点、换数据量都应成立 ⇒ ★★ 比 M18 的冻结值更通用
    #   ★★ 教训（本轮真踩到）：★ 终端回显把长行吞了字符 ⇒ 一度看成「all 比 free 还少」的幻影
    #   ★★ ⇒ ★ **断言必须基于落盘原字节**（od/grep），★ 不基于终端回显
    m19_bad: list[str] = []
    m19_seen = 0
    # ★★★ ★★ **守卫里的循环变量不许叫 `win`**（★ 本轮真踩到，★★ 差点造成**判据基线错窗口**）
    #   ★★ `win` 是判据的**同量纲窗口**（★ Stage 1=pre24 / Stage 2=pre7d）⇒ ★★ 一旦被
    #   ★★ 守卫里的 `for win in (...)` 覆盖 ⇒ ★★ 循环结束后 `win` 恒为最后一个值（"pre7d"）
    #   ★★ ⇒ ★★ **Stage 1 也会用 pre7d 基线** ⇒ ★★ stage-aware 机制**静默失效**
    #   ★★ ⇒ ★★ 自测场景 ⑤/⑥（stage 基线）当场抓红 ⇒ ★ 门有牙 ★（本次即靠它抓到）
    #   ★★ ⇒ 纪律：★ 守卫/循环的临时变量**不要与判据语义变量同名**
    for w in ("pre6d", "pre24", "pre7d", "post"):
        for grp in ("newly_covered", "old_feat", "other"):
            nf = head_count(data.get(f"M14_{w}_{grp}", "") or "")
            na = head_count(data.get(f"M14x_{w}_{grp}", "") or "")
            if nf is None or na is None:
                continue
            m19_seen += 1
            if na < nf:
                m19_bad.append(f"{w}/{grp}: all={na} < free={nf}")
    for scope in ("M14", "M14x"):
        for grp in ("newly_covered", "old_feat", "other"):
            n6 = head_count(data.get(f"{scope}_pre6d_{grp}", "") or "")
            n24 = head_count(data.get(f"{scope}_pre24_{grp}", "") or "")
            n7 = head_count(data.get(f"{scope}_pre7d_{grp}", "") or "")
            if n6 is None or n24 is None or n7 is None:
                continue
            m19_seen += 1
            if n7 != n6 + n24:
                m19_bad.append(f"{scope}/{grp}: pre7d={n7} ≠ pre6d+pre24={n6 + n24}")
    # ★★★★ M20 的**分层**不变量（★ 本轮新增，★★ 分层判据必须自带结构性守卫）
    #   ① 桶间加法：M20_<层>_pre7d = pre6d + pre24（★ 同 M14 ⇒ 标签与边界不符必破）
    #   ② ★★★ **分层穷尽性**：M20_normal_pre<窗> + M20_exempt_pre<窗> == M14x_pre<窗>_newly_covered
    #      （★ ★ 这条最锐：★ 分层必须**穷尽**原集合 ⇒ 若豁免分类写错（漏了第三类 / 重复计入）
    #        ⇒ 两层之和 ≠ 原集合 ⇒ 立刻破 ⇒ ★ 守卫有牙）
    for lay in ("normal", "exempt"):
        n6 = head_count(data.get(f"M20_{lay}_pre6d", "") or "")
        n24 = head_count(data.get(f"M20_{lay}_pre24", "") or "")
        n7 = head_count(data.get(f"M20_{lay}_pre7d", "") or "")
        if n6 is not None and n24 is not None and n7 is not None:
            m19_seen += 1
            if n7 != n6 + n24:
                m19_bad.append(f"M20/{lay}: pre7d={n7} ≠ pre6d+pre24={n6 + n24}")
    for w in ("pre6d", "pre24", "pre7d"):
        nt = head_count(data.get(f"M20_normal_{w}", "") or "")
        et = head_count(data.get(f"M20_exempt_{w}", "") or "")
        ref = head_count(data.get(f"M14x_{w}_newly_covered", "") or "")
        if nt is not None and et is not None and ref is not None:
            m19_seen += 1
            if nt + et != ref:
                m19_bad.append(f"M20/{w}: normal+exempt={nt + et} ≠ M14x newly={ref}（★ 分层不穷尽）")
    # ★★★★★ M15x 的**逐对分层穷尽性**（★ 本轮第 13 处口径修正新增）
    #   ★★ 每只金丝雀的每个 pre 窗口：exempt + normal 必须 == total
    #     ⇒ ★ 分类写错（漏一类 / 重复计入）**立刻破** ⇒ ★ 守卫有牙
    #   ★★ 与 M20 的穷尽性同款，但粒度是**逐对** ⇒ ★ 两处分类若漂移也会被这条抓住
    for bkey in BASELINES:
        if not bkey.startswith("M15x_pre"):
            continue
        raw = data.get(bkey)
        if raw is None:
            continue
        tot = head_count(raw)
        ex = field(raw, "exempt")
        no = field(raw, "normal")
        if tot is None or ex is None or no is None:
            continue
        m19_seen += 1
        if ex + no != tot:
            m19_bad.append(f"{bkey}: exempt+normal={ex + no:.0f} ≠ total={tot}（★ 逐对分层不穷尽）")
    if m19_bad:
        g19 = "★★ **口径自相矛盾**：" + "; ".join(m19_bad)
    elif m19_seen == 0:
        g19 = "（未产出可对拍的窗口键 ⇒ 本次未对拍）"
    else:
        g19 = f"✔ {m19_seen} 组结构性不变量成立（all≥free、pre7d=pre6d+pre24）"
    guards.append(("M19 口径结构不变量", g19))
    # ★★★★ M20 的**「fp 视图尾部可见性」守卫**（★ 第 18 处口径修正的核心守卫）
    #   ★★ 不变量：★ `fp_n_all ≥ window_n + tail_n`
    #   ★★   （★ window_n = post 段里 now()-6h **之前**的失败数（成熟视图能看见的）
    #   ★★     tail_n   = post 段里 **最后 6h** 的失败数（★ 成熟视图**完全不可见**的）
    #   ★★    ⇒ ★ 两集合**互不相交**，且都 ⊆ fp 视图（★ fp 无成熟度过滤、无 next_at 要求））
    #   ★★ ⇒ ★ 若破 ⇒ ★ **fp 视图被改回带成熟度过滤** ⇒ ★★ post 窗口的尾部 6h 再次失明
    #   ★★   ⇒ ★★ 尾部若发生回归 ⇒ 指纹仍读 0 ⇒ ★★ **假绿** ⇒ ⇒ **整表作废**
    #   ★★ ⇒ ★ 这条守卫是「分视图读取」这条修正的**牙齿**（★ 否则改回旧口径没人会发现）
    m20fp_bad: list[str] = []
    m20fp_seen = 0
    for lay in ("normal", "exempt"):
        fp_n = field(data.get(f"M20_{lay}_post_fp", "") or "", "n_all")
        split = data.get(f"M20_{lay}_post_split")
        if fp_n is None or split is None:
            continue
        w_n = field(split, "window_n")
        t_n = field(split, "tail_n")
        if w_n is None or t_n is None:
            continue
        m20fp_seen += 1
        if fp_n < w_n + t_n:
            m20fp_bad.append(f"M20/{lay}: fp n_all={fp_n:.0f} < 窗口 {w_n:.0f} + 尾部 {t_n:.0f}"
                             f"（★ fp 视图疑似仍带成熟度过滤 ⇒ ★ **尾部仍失明** ⇒ 假绿）")
    if m20fp_bad:
        g20fp = "★★ **fp 视图尾部失明**：" + "; ".join(m20fp_bad)
    elif m20fp_seen == 0:
        g20fp = "（未产出 fp/切分键 ⇒ 本次未对拍）"
    else:
        g20fp = f"✔ {m20fp_seen} 组尾部可见性不变量成立（fp ≥ 窗口 + 尾部）"
    guards.append(("M20 fp 视图尾部可见性", g20fp))
    # ★★ 守卫**缺失**（指标没产出 / SQL 出错）⇒ 同样判失败
    #    （★ 旧写法只看有没有 '★★' ⇒ 守卫空值会被当成「通过」⇒ 整表被信任）
    bad = [name for name, val in guards if "★★" in val or not val.strip()]
    rows: list[tuple[str, str, str, str]] = []   # 判据 | 基线 | 观测 | 裁决

    # ── 资格面 / 成本面（Stage 1 可判）──────────────────────────────
    def scalar(key: str, label: str):
        obs = data.get(key)
        if obs is None:
            rows.append((label, base_cell(key), "—",
                         verdict(None, "指标缺失（脚本未产出该键）")))
            return
        n = head_count(obs)
        base = BASELINES[key]["base"]
        if key == "M2_featured_static":
            # ★ '34 rp + 15 canon + 200 raw_variant = 210' ⇒ 取**最后一个**数字
            v_last = last_num(obs)
            ok = None if v_last is None else int(v_last) == base
            rows.append((label, base_cell(key), obs,
                         verdict(ok, f"static={int(v_last)}" if v_last is not None else "?")))
        elif key == "M6_free_featured_pairs":
            ok = None if n is None else n >= base
            rows.append((label, base_cell(key), obs,
                         verdict(ok, "★ 资格面，不是行为面")))
        elif key == "M9_missing_raw_variant":
            ok = None if n is None else n == base
            rows.append((label, base_cell(key), obs,
                         verdict(ok, "覆盖无缺口" if ok else "★ 有缺口 ⇒ 展开已退化")))
        elif key == "M5_probe_runs_24h":
            lim = BASELINES[key]["limit"]
            ok = None if n is None else n <= lim
            # ★ 2026-11-11 修正：文案必须与裁决符号一致（原版恒写「未爆（n ≤ lim）」，
            #   ★ Stage 1 干跑实测 9816 > 9334 判 ❌ 时文案仍自称「未爆（9816 ≤ 9334）」
            #   ★ ——数字对比是假的 ⇒ 读者无法分辨「爆了还是没爆」）。
            if ok is True:
                note = f"★ 爆炸半径未爆（{n} ≤ {lim}）"
            elif ok is False:
                note = (f"★ 探测量超预算（{n} > {lim}）⇒ 复核探针调度增量来源"
                        f"（era2 纳入 106 新覆盖变体 + 恢复链 re-arm/回退探测均会推高读数 ⇒ "
                        f"先分辨「设计内恢复探测」与「调度失控」再定责）")
                # ★★★ 第 22 处口径修正：★ 把「基线口径问题」摆进行内（★ 只披露，不改符号）
                note += m5_baseline_disclosure(data, n, lim)
                # ★★★ 2026-11-11 关窗预演补（与第 22 处互补）：★ **增量归因**分解
                #   ★（第 22 处回答「阈值是否成立」；本键回答「增量从哪来」）
                #   ★ 预演实测：credential_recovery 持平 / periodic +3.7%（era2 新覆盖变体
                #   入调度，设计内）/ sync_request +131%（恢复链再探测，设计内＝本轮
                #   修复的目的行为）⇒ ❌ 的增量可完全归因 ⇒ 只披露，不改符号。
                bk = (data.get("M5_probe_runs_24h_by_kind") or "").strip()
                if bk:
                    note += f" ‖ ★ 增量归因（by_kind）: {bk}"
                else:
                    note += (" ‖ ★ 增量归因不可见（键 M5_probe_runs_24h_by_kind 未产出"
                             " ⇒ 无法分辨「设计内恢复探测」与「调度失控」）")
            else:
                note = "指标缺失（脚本未产出该键）"
            rows.append((label, base_cell(key), obs, verdict(ok, note)))

    scalar("M2_featured_static", "M2 featured static 规模")
    scalar("M6_free_featured_pairs", "M6 免费面 featured 命中")
    scalar("M9_missing_raw_variant", "M9 展开覆盖缺口")
    scalar("M5_probe_runs_24h", "M5 24h 探测量")

    # ── M3b 机制证据：backoff 档位 2:1 ────────────────────────────────
    # ★ 取 median_retry_s（★ 不是行数）
    f429 = median_retry(data.get("M3b_backoff_ns_featured_err_http_429", ""))
    n429 = median_retry(data.get("M3b_backoff_ns_not_err_http_429", ""))
    f503 = median_retry(data.get("M3b_backoff_ns_featured_err_http_503", ""))
    n503 = median_retry(data.get("M3b_backoff_ns_not_err_http_503", ""))
    if None in (f429, n429, f503, n503):
        rows.append(("M3b namespaced backoff 档位", "featured≤1800 / 非featured≥3600", "—",
                     verdict_missing("★ 档位读数算不出（无样本或分位缺失）")))
    else:
        ok = (f429 <= 1800 and n429 >= 3600) or (f503 <= 1800 and n503 >= 3600)
        rows.append(("M3b namespaced backoff 档位", "featured≤1800 / 非featured≥3600",
                     f"429: {f429:.0f} vs {n429:.0f}; 503: {f503:.0f} vs {n503:.0f}",
                     verdict(ok, "★ featured 乘数已作用到 namespaced（2:1）" if ok
                     else "★ 档位未呈 2:1 ⇒ 乘数可能未作用到 namespaced")))

    # ── M12s：复检节奏（★★ 降级为**参考**：★ post 的 ns|feat 人群会变）────────
    # ★★ 为什么不能当判据（★ 本轮第 4 道关「人群稳定性」）：
    #   pre 的 ns|feat = st_old 的 namespaced（38 条）；post 的 ns|feat = st_new 的（144 条）
    #   ⇒ ★★ post 混入了 era2 新纳入的 106 个**冷对**（med 360）⇒ 尾部必然被抬高
    #   ⇒ ★★ 即使「post p90/p99 ≤ pre」通过，也**不可归因**给本轮修复（人群变了）
    #   ⇒ ⇒ ★ **判据改用 M14 的三个 population-stable 组**（st_old ⊆ st_new ⇒ 组恒定）
    for grp, label, bkey in (
        ("ns_feat", "M12s ns featured 复检间隔（med/p90/p99）", "M12s_ns_feat_pre"),
        ("ns_not", "M12s ns 非featured 复检间隔（med/p90/p99）", "M12s_ns_not_pre"),
    ):
        base_txt = base_cell(bkey)
        pre = BASELINES[bkey]["base"]
        key = f"M12s_settled_post_{grp}"
        obs = data.get(key)
        if obs is None:
            rows.append((label, base_txt, "—",
                         verdict(None, "★ post 段无事件（★ ★ **键未产出**：★ 尚未结算 / 或采集侧"
                                       "把 post 段的**成功行**也过滤了 ⇒ ★ 不是「量具缺数」）")))
            continue
        n = head_count(obs)
        med = field(obs, "med_min", "med")
        p90 = field(obs, "p90")
        p99 = field(obs, "p99")
        if n is None or n < 20:
            rows.append((label, base_txt, obs,
                         verdict(None, f"★ post n={n} < 20 ⇒ 尾部读数不稳（方向可读）")))
        else:
            rows.append((label, base_txt, obs,
                         verdict(None, f"★ 参考读数：post med={med} / p90={p90} / p99={p99}"
                                       f"（pre {pre[0]}/{pre[1]}/{pre[2]}）"
                                       "⇒ ★★ **不可作判据**：post 人群含 era2 新纳入的 106 个冷对"
                                       "⇒ 尾部变化**不可归因**（★ 判据见 M14 三组）")))

    # ── M15 / M15x：逐对金丝雀（★ 本轮第 13 处口径修正）────────────────
    #   ★★ **两个口径的结论相反**（★ 本轮真踩到，★★ 差点写错进结论）：
    #     · M15  = **free 切片**（旧键）⇒ 实测**四只完全同形**（pre7d 各 12 豁免 + 1 normal）
    #              ⇒ ★ 「post med ≤180」**结构性不可达**（协议形态设计上钉 6h、且**豁免 featured 乘数**）
    #              ⇒ ★ 且剔豁免后只剩 0~1 条 ⇒ ★★ **只能作观察面**（★ 否则必然假阴性）
    #     · M15x = **全计费口径**（新键）⇒ deepseek normal **41 条**（pre7d）/ **18 条**（pre24）
    #              ⇒ ★★ **唯一**可判的逐对判据 ⇒ 阈值**全部由 pre 冻结基线推导**（★ 不拍脑袋）
    #   ★★ **分派是数据驱动的**（★ 不硬编码模型名）：★ 每只按**它自己**的 normal 样本量决定
    #     判据 / 观察面 ⇒ ★ 换一批金丝雀也成立。
    for pair in CANARIES:
        # ① 旧口径（free 切片）⇒ ★ 观察面（★ 结构性不可达 + 样本不足）
        pre_f = data.get(f"M15_pre__{pair}")
        post_f = data.get(f"M15_post__{pair}")
        pre_med_f = field(pre_f, "med_min", "med") if pre_f else None
        post_n_f = head_count(post_f) if post_f else None
        post_med_f = field(post_f, "med_min", "med") if post_f else None
        rows.append((f"M15 金丝雀 {pair}（★ 免费面·观察面）",
                     f"pre med={CANARY_PRE_MED}（★ 实测口径）",
                     f"pre_med={pre_med_f} post_med={post_med_f} post_n={post_n_f}",
                     verdict(None,
                             "★ free 切片下本只的失败**绝大多数是豁免类**（协议形态 ⇒ 设计上钉 6h "
                             "⇒ **豁免 featured 乘数**）⇒ ★ 混合口径的「post med ≤180」"
                             "**结构性不可达** ⇒ ★★ **不作判据**（★ 否则必然假阴性）"
                             "⇒ ★ 逐对判据看 **M15x（全计费口径·分层）**")))

        # ② 新口径（全计费·分层）⇒ ★ 数据驱动分派
        bkey = f"M15x_{win}__{pair}"
        pre_x = data.get(bkey)
        post_x = data.get(f"M15x_post__{pair}")
        label_x = f"M15x 金丝雀 {pair}（★ 全计费·分层）"
        if pre_x is None:
            rows.append((label_x, "—", "—",
                         verdict(None, "★ pre 分层读数缺失（★ 该只在本窗口无新覆盖失败事件）")))
            continue
        b = BASELINES.get(bkey)
        if b is None:
            rows.append((label_x, "—", "—", verdict(None, "★ 该窗口没有冻结基线 ⇒ 不可判")))
            continue
        base_txt = (f"pre normal_med={b['base'][0]}min ladder_p50={b['base'][1]}s"
                    f" | top3600={b['base'][2]} top7200={b['base'][3]} top21600={b['base'][4]}")
        n_tot = head_count(pre_x)
        n_ex = field(pre_x, "exempt")
        n_no = field(pre_x, "normal")
        ex_pct = (100.0 * n_ex / n_tot) if (n_ex is not None and n_tot) else None
        if n_no is None:
            rows.append((label_x, base_txt, "—",
                         verdict(None, "★ pre 分层读数缺 normal 计数 ⇒ 不可判")))
            continue
        if n_no < CANARY_MIN_NORMAL:
            pct_txt = f"{ex_pct:.0f}%" if ex_pct is not None else "?"
            rows.append((label_x, base_txt,
                         f"total={n_tot} exempt={n_ex:.0f} normal={n_no:.0f}（豁免 {pct_txt}）",
                         verdict(None,
                                 f"★ 豁免占 {pct_txt}（★ 协议形态 ⇒ 设计上钉 6h ⇒ **豁免 featured 乘数**）"
                                 f"⇒ ★ 混合口径「post med ≤{CANARY_POST_MAX_MED}」**结构性不可达**"
                                 f"；★ 且剔豁免后 normal 仅 {n_no:.0f} 条 < {CANARY_MIN_NORMAL}"
                                 f" ⇒ ★★ **样本不足** ⇒ 本只为**观察面**（★ 不作判据）")))
            continue
        # ---- 可判：三个分量（★ 阈值与「有牙档位」全部取自**冻结基线**）----
        if post_x is None:
            rows.append((label_x, base_txt, "—",
                         verdict(None, "★ post 段无事件（★ post 失败需 6h 才结算 ⇒ 窗口未满）")))
            continue
        p_nno = field(post_x, "normal")
        if p_nno is None:
            rows.append((label_x, base_txt, post_x, verdict_missing("★ post 分层读数缺 normal 计数")))
            continue
        if p_nno < CANARY_MIN_NORMAL:
            rows.append((label_x, base_txt, post_x,
                         verdict(None, f"★ post normal 仅 {p_nno:.0f} 条 < {CANARY_MIN_NORMAL}"
                                       f" ⇒ ★ 样本不足 ⇒ 不可判（★ 不许外推）")))
            continue
        p_med = field(post_x, "normal_med_min")
        p_lad = field(post_x, "normal_ladder_p50_s")
        pre_med_b, pre_lad_b = b["base"][0], b["base"][1]
        if p_med is None or p_lad is None or not pre_med_b or not pre_lad_b:
            rows.append((label_x, base_txt, post_x,
                         verdict_missing("★ post 的 normal med / 阶梯中位算不出（★ 或 pre 基线为空）")))
            continue
        lim_med = pre_med_b / 2.0
        lim_lad = pre_lad_b / 2.0
        ok1 = p_med <= lim_med
        ok2 = p_lad <= lim_lad
        # ★③ 未减半档位清零：★ **只对 pre 期出现过的档位**（★ pre 计数=0 ⇒ post 侧**恒真**
        #   ⇒ 恒真判据 = 门没牙 ⇒ 必须标注「★ 恒真 ⇒ 不作判据」）
        ok3 = True
        teeth_txt: list[str] = []
        vac_txt: list[str] = []
        for idx, d in ((2, 3600), (3, 7200), (4, 21600)):
            pn = b["base"][idx]
            qn = field(post_x, f"normal_top{d}_n")
            if qn is None:
                continue
            if pn > 0:
                ok3 = ok3 and (qn == 0)
                teeth_txt.append(f"{d}s 档 pre {pn:.0f} 条 ⇒ post {qn:.0f} 条"
                                 f"（{'✔ 已清零' if qn == 0 else '★ 仍有未减半档'}）")
            else:
                vac_txt.append(f"{d}s 档")
        ok = ok1 and ok2 and ok3
        # ★★★★ 第 26 处口径修正：★ 「::int 取整边界」伪影 ⇒ ★ normal med **恰好压在阈值上**时披露
        #   ★★ 阈值 lim_med = pre_med_b / 2，★ 而 pre_med_b 本身是**取整样本** ⇒ 阈值真值 ∈ [lim−0.25, lim+0.25)
        #   ★★ ⇒ slack = 0.25（★ 不是 0.5：★ 除以 2 把不确定带也减半）
        rnd_txt = ""
        if not ok1:
            rnd_txt = rounding_disclosure(field(post_x, "normal_med_min_raw"), lim_med,
                                          slack=0.25, what="post normal med")
            rnd_txt += percentile_fragility_disclosure(p_nno, b.get("n"), "normal med")
        note = (f"★ 观察间隔：post normal med={p_med:.0f}min（期望 ≤{lim_med:.0f}；pre {pre_med_b:.0f}）"
                f" ⇒ {'✔ 已折半' if ok1 else '★ 未折半'}"
                f" / ★ 阶梯（★ 机制原生·零余量）：post ladder_p50={p_lad:.0f}s"
                f"（期望 ≤{lim_lad:.0f}；pre {pre_lad_b:.0f}）⇒ {'✔ 已折半' if ok2 else '★ 未折半'}")
        note += (" / ★ 未减半档位清零：" + "；".join(teeth_txt) if teeth_txt
                 else " / ★ 未减半档位清零：★ pre 期**无任何未减半档位** ⇒ 该分量**恒真** ⇒ 不作判据")
        if vac_txt:
            note += (f"（★ 恒真档位（pre 计数=0）：{'、'.join(vac_txt)}"
                     f" ⇒ 本窗口**恒真** ⇒ 不作判据）")
        note += f"（★ post normal n={p_nno:.0f}）"
        rows.append((label_x, base_txt, post_x, verdict(ok, note + rnd_txt)))

    # ── M14：★★ 三个 population-stable 组 × ★★ 两个计费口径（★ 本轮判据体系的主体）──
    #   ★ 为什么是它：★ st_old ⊆ st_new ⇒ 三个组在 pre/post **是同一批对**
    #     · old_feat      = st_old 里的 ns 对      ⇒ 无回归判据
    #     · newly_covered = st_new \ st_old 的 106 个 ⇒ **效应判据**（本轮的主判据）
    #     · other         = 两个集合都没有的 ns 对  ⇒ 对照判据（节奏不该变）
    #   ★★ **分位数脆**：newly 组是双峰（热对 med≈3 / 冷对 med≈360）⇒ p90 处于边界
    #     ⇒ ★ 另用**速率型**指标 slow_gt300_pct（稳）⇒ 判据用 p90 + 慢事件占比
    # ★★ 基线按 **stage** 选同量纲的那一套（★ Stage 1 的 post 是 24h ⇒ 用 pre24；
    #    ★ Stage 2 的 post 是 7 天 ⇒ 用 pre7d）★ 借用另一窗口 = 判错
    # ★★★ **计费口径两档**（★ 本轮第 9 处口径修正）：
    #   · ''  = **free 切片**（该对有 free 绑定）⇒ ★ **症状切片**（免费供应商）
    #   · 'x' = **全计费口径**（该对已绑定）⇒ ★★ **效应判据用它** ⇒ 样本量 5.6×
    #     （★ free 口径把 per_token / token_plan 凭据上的探测**静默丢掉**了）
    #   ★★ 两档各有**自己**的基线与阈值 ⇒ ★★ 阈值**不可跨口径混用**（slow 基线差 2×）
    #   ★★ 某档 n 不足时，**另一档**可能够 ⇒ ★★ 行内互相点名 ⇒ 读者不会误读成「修复无效」
    for scope, prefix in (("", "M14 "), ("x", "M14x ")):
        other_tag = "M14x " if scope == "" else "M14 "
        for grp, label, mode in (
            ("old_feat", "旧 featured 人群（无回归）", "noregress"),
            ("newly_covered", "新覆盖人群（★ 混合口径·观察面）", "observe"),
            ("other", "对照组（两个集合都没有）", "control"),
        ):
            label = prefix + label
            bkey = f"M14{scope}_{grp}_{win}"
            b = BASELINES.get(bkey)
            if b is None:
                # ★★ **不许直接索引**（★ 本轮加固）：★ 若同量纲窗口变量被别处污染
                #   ⇒ ★ 直接索引会 KeyError 崩掉（★ fail-loud 是对的，但读者看不到「为什么」）
                #   ⇒ ★★ 这里改成**可读的一行**：「窗口变量被污染」本身要**看得见**
                rows.append((label, "—", "—",
                             verdict(None, f"★ 冻结基线缺失（{bkey}）⇒ ★★ **窗口变量异常**"
                                           f"（★ 当前 win={win!r}）⇒ 不可判")))
                continue
            pre = b["base"]
            base_txt = base_cell(bkey)
            key = f"M14{scope}_post_{grp}"
            obs = data.get(key)
            if obs is None:
                rows.append((label, base_txt, "—",
                             verdict(None, "★ post 段无事件（尚未结算）/ 指标缺失")))
                continue
            n = head_count(obs)
            med = field(obs, "med_min", "med")
            p90 = field(obs, "p90_min", "p90")
            p99 = field(obs, "p99_min", "p99")
            slow = field(obs, "slow_gt300_pct")
            if mode == "observe":
                # ★★★ 观察面分支**先于**样本量判定（★ 本轮自测抓到：
                #   ★ 样本不足时若先落 n<30 的措辞，★ 这行就**说不出「观察面」**
                #   ★ ⇒ 读者会把「不参与判据」误读成「样本不足所以没结论」）
                rows.append((label, base_txt, obs,
                             verdict(None, "★ **观察面（不作判据）**：★ 混合口径里 78% 是豁免类"
                                           "（协议形态 6h 停放 + 乘数豁免）⇒ ★ post 的 p90/slow"
                                           "**结构性不可达** ⇒ ★ 效应判据改用 **M20 分层**"
                                           "（★ M20 新覆盖组·非豁免）"
                                           + (f"（★ 本行 n={n}）" if n is not None else ""))))
                continue
            if n is None or n < 30:
                rows.append((label, base_txt, obs,
                             verdict(None, f"★ post n={n} < 30 ⇒ 读数不稳（方向可读）"
                                           f"（★ 本口径样本不足；★ 另一口径见「{other_tag}」同组行）")))
                continue
            if mode == "noregress":
                if p90 is None or p99 is None:
                    rows.append((label, base_txt, obs,
                                 verdict_missing("★ post 的 p90/p99 算不出")))
                    continue
                ok = p90 <= pre[1] and p99 <= pre[2]
                # ★★★★ 第 26 处口径修正：★ 「::int 取整边界」伪影 ⇒ 分量级披露（★ 逐个分量定位）
                #   ★★ pre[1]/pre[2] 是**取整样本** ⇒ 阈值真值 ∈ [thr−0.5, thr+0.5) ⇒ slack = 0.5
                rnd_txt = ""
                if not ok:
                    if p90 > pre[1]:
                        rnd_txt = rounding_disclosure(field(obs, "p90_min_raw"), pre[1],
                                                      slack=0.5, what="post p90")
                    elif p99 > pre[2]:
                        rnd_txt = rounding_disclosure(field(obs, "p99_min_raw"), pre[2],
                                                      slack=0.5, what="post p99")
                    rnd_txt += percentile_fragility_disclosure(n, b.get("n"), "p90/p99")
                rows.append((label, base_txt, obs,
                             verdict(ok, f"★ post p90={p90} / p99={p99}（pre {pre[1]}/{pre[2]}）"
                                       f"⇒ 旧 featured 人群{'尾部未变差' if ok else '★ 尾部变差（无回归失败）'}"
                                       + rnd_txt)))
            elif mode == "effect":
                if p90 is None:
                    rows.append((label, base_txt, obs,
                                 verdict_missing("★ post 的 p90 算不出")))
                    continue
                lim = BASELINES[bkey].get("post_p90_max", pre[1] // 2)
                ok = p90 <= lim
                slow_txt = ""
                if slow is not None:
                    slow_ok = slow <= b.get("slow", 0) / 2
                    slow_txt = (f" / 慢事件(>300min)占比 {slow}%（pre {b.get('slow')}%）"
                                f"⇒ {'冷簇已收缩' if slow_ok else '★ 冷簇未收缩'}")
                    ok = ok and slow_ok
                rows.append((label, base_txt, obs,
                             verdict(ok, f"★ post p90={p90}（期望 ≤{lim}；pre {pre[1]}）"
                                       f"⇒ {'复检间隔明显下移' if ok else '★ 未见明显下移'}"
                                       f"{slow_txt}（★ n={n}）")))
            else:
                if med is None:
                    rows.append((label, base_txt, obs,
                                 verdict_missing("★ post 的中位算不出")))
                    continue
                ok = abs(med - pre[0]) <= max(30, int(pre[0] * 0.2))
                # ★★★ 第 21 处口径修正：★ 把「相位/构成」摆进对照组行内
                #   ★★ **只披露，不改裁决符号**（★ ❌ 仍是 ❌ ⇒ ★ 门不钝化）
                phase_txt = control_phase_disclosure(data, scope, pre[0])
                rows.append((label, base_txt, obs,
                             verdict(ok, f"★ 对照：post med={med}（pre {pre[0]}，容差 ±20%）"
                                       f"⇒ {'未受影响的 ns 对节奏未变' if ok else '★ 对照也变了 ⇒ 有其它因素'}"
                                       + phase_txt)))
    # ── M20：★★★★ **分层效应指标**（★ 本轮第 11 处口径修正）──
    #   ★★ 为什么要它：★ M14x 的混合口径里 **78% 是豁免类**（协议形态 ⇒ 钉 6h ⇒ 乘数豁免）
    #     ⇒ ★★ post 的混合 p90 必然仍 ≈360 ⇒ ★★ M14x 的「post p90 ≤180」**结构性不可达**
    #     ⇒ ★★ 若不换判据，★ 关窗必得一个**假阴性**，且会被读成「修复没生效」
    #   ★★ M20 = 同一批新覆盖变体，按「该被减半(normal) / 不该被减半(exempt)」分层
    #   ★★ 判据阈值**全部从 pre 实测推导**（★ 不拍脑袋）：
    #     · normal（★ 效应主判据）：① **未减半 6h 顶格清零**（top21600_n == 0）
    #         —— ★ 机制指纹：21600 = 6h 未减半；乘数（50%）生效则变 10800 = 3h
    #         —— ★★ 计数型、**零余量**、无调度噪声
    #       ② **慢事件折半**（slow_gt300_pct ≤ pre/2）★ 沿用「机制预期 = pre 的一半」语义
    #     · exempt（★ 不回归守卫）：p90_gap ≤ pre 且 slow ≤ pre×1.25
    for lay, label, mode in (
        ("normal", "M20 新覆盖组·非豁免（★ 效应主判据）", "effect"),
        ("exempt", "M20 新覆盖组·豁免（★ 不回归守卫）", "noregress"),
    ):
        bkey = f"M20_{lay}_{win}"
        b = BASELINES.get(bkey)
        if b is None:
            # ★★ 同上（★ 本轮加固）：★ 窗口变量被污染时不许崩 ⇒ ★ 改成可读的一行
            rows.append((label, "—", "—",
                         verdict(None, f"★ 冻结基线缺失（{bkey}）⇒ ★★ **窗口变量异常**"
                                       f"（★ 当前 win={win!r}）⇒ 不可判")))
            continue
        base_txt = base_cell(bkey)
        obs = data.get(f"M20_{lay}_post")             # ★ 成熟视图（★ 测「间隔」⇒ 必须结算）
        fobs = data.get(f"M20_{lay}_post_fp")        # ★ 指纹视图（★ 测「阶梯值」⇒ **无需结算**）
        pobs = data.get(f"M20_{lay}_{win}_fp")       # ★ pre 侧同口径（★ 量具正控 + 同量纲对照）
        # ★★★★ 第 18 处口径修正（★★ 详见主档 §5.9.21.13.14）：★★ **两个分量的成熟度需求不同**
        #   ★ 指纹（阶梯值）⇒ **不需要结算** ⇒ 读 **fp 视图**（★ 无成熟度过滤 ⇒ ★ 尾部 6h 不再失明）
        #   ★ slow（失败 → 下一次探测的间隔）⇒ **需要结算** ⇒ 读**成熟视图**（★ 固有 6h 延迟 ⇒ 行内披露）
        #   ★★ ⇒ ★★ 若两个分量都读成熟视图 ⇒ ★★ post 窗口的**最后 6h 完全不可见**
        #   ★★ ⇒ ★★ 尾部若发生回归（修复失效）⇒ ★★ 指纹仍读 0 ⇒ ★★ **假绿**（★ 同第 17 处修正同族）
        if mode == "noregress" and obs is None:
            rows.append((label, base_txt, "—",
                         verdict(None, "★ post 段无事件（★ post 事件需 6h 才结算 ⇒ 窗口未满）")))
            continue
        pre_med, pre_p90, pre_slow, pre_top = b["base"]
        p90 = field(obs, "p90_gap")
        slow = field(obs, "slow_gt300_pct")
        n = head_count(obs)
        # ★★★★ **pre 基线的「阶梯构成」**（★ 本轮第 14 处口径修正，★★ 阈值同量纲）
        #   ★★ 「newly」的定义 = `st_new \ st_old`，而 `st_old` 的 `rawv_old` **只覆盖
        #   ★★ 「models_canonical.featured=TRUE 且 active」的 raw 变体** ⇒ ★★ **rp 成员的 raw 变体**
        #   ★★ 不在 st_old 里 ⇒ ★★ 而 featured 乘数**恰恰对它们生效** ⇒ ★★ 它们 pre 期就已 featured
        #   ★★   ⇒ ★★ 「newly」≠「pre 期未 featured」⇒ ★★ pre 基线里混着**已 featured 的样本**
        #   ★★   ⇒ ★★ 它们阶梯短 ⇒ **拉低 pre 的 slow** ⇒ ★★ 「post ≤ pre/2」**偏严** ⇒ ★★ 假阴性
        #   ★★ 实测：★ M20 normal pre7d **41/154 = 26.6%**（★ pre24 = 0、exempt 两窗口 = 0）
        #   ★★ ⇒ ★★ 阈值必须按「**干净 pre**」推导（★ 干净 pre 与 post 段**同量纲**：post 全是 featured 后）
        #   ★★ ★ 「乘数已生效」的**无歧义证据集** = {150,450,1800,10800}
        #   ★★   （★ 三条链的未减半并集 = {0,5,10,15,30,60,120,300,900,3600,7200,21600}）
        comp = data.get(f"M20_{lay}_{win}_composition")
        c_n = field(comp, "clean_n") if comp else None
        c_slow = field(comp, "clean_slow") if comp else None
        contam = field(comp, "contam_pct") if comp else None
        mul_ev = field(comp, "mul_evidence") if comp else None
        unexpl = field(comp, "unexplained_n") if comp else None
        unexpl_v = field_str(comp, "unexplained_values") if comp else None
        if c_n and c_slow is not None and c_n > 0:
            pre_slow_eff = round(100.0 * c_slow / c_n, 1)
            pre_slow_src = "★ **干净 pre**（★ 已剔除乘数证据样本 ⇒ 与 post 段**同量纲**）"
        else:
            pre_slow_eff = pre_slow
            pre_slow_src = "★ 原始 pre（★ ★ composition 缺失 ⇒ 阈值**可能偏严** ⇒ 判负前先复核）"
        contam_txt = ""
        if contam is not None and mul_ev:
            contam_txt = (f" ★ ⚠️ **pre 基线含 {mul_ev:.0f} 条「乘数已生效」样本**"
                          f"（★ pre 期已 featured、却被算进 newly ⇒ 污染 **{contam:.1f}%**）")
        if unexpl is not None and unexpl > 0:
            # ★★ **证据集不完备 ⇒ 干净 pre 仍可能被污染 ⇒ 阈值仍偏严** ⇒ ★ fail-closed 提示
            contam_txt += (f" ★ ⚠️ **阶梯证据集不完备**（★ 有 **{unexpl:.0f}** 条样本的阶梯值**无法解释**"
                           f"（值 `{unexpl_v or '?'}`）⇒ ★ 干净 pre 可能**仍偏严** ⇒ ★ 判负前先复核")
        if mode == "effect":
            # ★★★★ 分量①：**机制指纹**（★ 第 18 处口径修正 ⇒ 读 **fp 视图**）
            ok_fp, fp_txt = fingerprint_component(fobs, pobs)
            # ★★★★ 分量②：**slow**（★ 仍读成熟视图 ⇒ 需 6h 结算 ⇒ ★ 固有延迟）
            lim_slow = round(pre_slow_eff / 2, 1)
            if obs is None:
                ok_slow = None
                slow_txt = ("★ slow 分量**未结算**（★ 它测「失败 → 下一次探测」的间隔 ⇒ "
                            "必须等下一次探测发生 ⇒ ★ 需 6h；★ 与「量具缺数」不是一回事）")
            elif slow is None:
                ok_slow = None
                slow_txt = "★ slow 分量算不出 slow_gt300_pct ⇒ 不可判"
            elif n is None or n < 10:
                ok_slow = None
                slow_txt = (f"★ slow 分量样本不足（n={n} < 10 ⇒ **方向可读，★ 不许外推**）")
            else:
                ok_slow = slow <= lim_slow
                slow_txt = (f"慢事件(>300min) {slow}%（期望 ≤{lim_slow}%；★ {pre_slow_src}：{pre_slow_eff}%）"
                            f" ⇒ {'冷簇已收缩' if ok_slow else '★ 冷簇未收缩'}")
            # ★★★★ 分量③：**「摇摆」**（★ 第 20 处口径修正 ⇒ ★ 修复前后的判别信号）
            #   ★★ 同一模型**同时**出现「减半独占值」（⇒ 当时 featured ⇒ 阶梯被减半）
            #   ★★ 与「未减半 6h 顶格」（⇒ 当时不 featured ⇒ 阶梯未减半）
            #   ★★ ⇒ ★★ 该模型的 **featured 状态在窗口内不稳定**
            #   ★★ ★★ 机制推演（★ 真库双向实测确认）：
            #   ★★   · **修复前**：★ newly 的 namespaced raw 不在 static 集里（★ static 只有 45）
            #   ★★     ⇒ ★★ 只能靠 usage top-N tier featured ⇒ ★★ tier 变动 ⇒ ★★ **摇摆普遍**
            #   ★★     ⇒ ★★ 实测（★ 负控 era）：★ `anthropic/claude-opus-4.8` **同时**有 10800 + 21600 ✔
            #   ★★   · **修复后**：★ static 展开到 **210**（★ 含 200 raw 变体 ⇒ newly **永久 featured**）
            #   ★★     ⇒ ★★ **tier churn 不再影响它们** ⇒ ★★ 不应摇摆 ⇒ ★★ 实测：★ 当前 **swing_n=0** ✔
            #   ★★ ⇒ ★★ **⇒ 摇摆 0 只 = 修复已真正落地（★ 而不只是「碰巧 featured」）**
            #   ★★ ⇒ ★★ **⇒ 且它天然免疫「usage tier churn ⇒ 假红」**（★ 摇摆的成因已被 static 展开消除）
            sw = data.get("M20_normal_post_fp_swing")
            if sw is None:
                ok_swing = None
                swing_txt = ("★ 摇摆键**未产出**（★ 先查采集侧是否漏了逐模型聚合 ⇒ ★ 不是「量具缺数」）")
            else:
                s_n = field(sw, "swing_n")
                s_models = field_str(sw, "swing_models")
                if s_n is None:
                    ok_swing = None
                    swing_txt = "★ 摇摆键算不出 swing_n ⇒ 不可判"
                elif s_n > 0:
                    ok_swing = False
                    swing_txt = (f"★ 摇摆模型 **{s_n:.0f}** 只（★ 同一模型同时出现减半独占值与未减半 6h 顶格"
                                 f" ⇒ ★ featured 状态不稳 ⇒ ★ **修复前特征**）★ 名单: {s_models or '?'}")
                else:
                    ok_swing = True
                    swing_txt = "★ 摇摆模型 0 只（★ 新覆盖模型 featured 状态稳定 ⇒ ★ 修复已真正落地）"
            # ★★ 总体裁决：★ 三个分量**都**成立才算达成；★ 任一 ❌ ⇒ ❌；★ 有不可判 ⇒ 不可判
            if ok_fp is False or ok_slow is False or ok_swing is False:
                ok = False
            elif ok_fp is None or ok_slow is None or ok_swing is None:
                ok = None
            else:
                ok = True
            p90_txt = f" / p90_gap {p90:.0f}（★ 参考：期望 ≤{pre_p90 // 2}）" if p90 is not None else ""
            # ★★★ **era 纯度的存活者偏差自检**（★ 本轮新增，★ 主档 §5.9.21.13.8）
            #   ★★ pre 侧成熟度过滤要求 `next_at < 切点` ⇒ ★★ 被丢掉的事件间隔必然跨越切点
            #   ★★ （≥6h）⇒ ★★ 必然是「慢事件」⇒ ⇒ ★★ 若丢弃量 > 0 ⇒ ★★ pre 的慢事件占比
            #   ★★ 被**系统性低估** ⇒ ★★ 阈值「post ≤ pre/2」**偏严** ⇒ ★★ 可能假阴性
            _drop = num(data.get(f"M20_era_dropped_{win}", "") or "")
            if _drop is None:
                bias_txt = ""
            elif _drop > 0:
                bias_txt = (f" ★⚠️ **pre 基线有 {_drop:.0f} 条被 era 纯度丢弃**"
                            f"（★ 存活者偏差 ⇒ 慢事件占比被低估 ⇒ ★ 阈值偏严 ⇒ ★ 判负前先复核）")
            else:
                bias_txt = " ★（★ pre 侧 era 纯度未丢事件 ⇒ 无存活者偏差）"
            obs_txt = (f"【指纹·fp】{fobs or '—（键未产出）'}"
                       f" || 【slow·成熟】{obs or '—（未结算）'}"
                       f" || 【摇摆】{sw or '—（键未产出）'}")
            # ★★★★ 第 13 处纪律的最后一处候选点：★ **同一行里两个 n 来自不同窗口** ⇒ 必须显式标明
            _split_e = data.get(f"M20_{lay}_post_split")
            xwin_txt = cross_window_disclosure(n, field(fobs, "n_all"),
                                                field(_split_e, "window_n"),
                                                field(_split_e, "tail_n"))
            rows.append((label, base_txt, obs_txt,
                         verdict(ok, f"★ 【指纹·fp 视图（★ 无成熟度过滤 ⇒ 尾部不被截断）】{fp_txt}"
                                   f" / ★ 【slow·成熟视图（★ 需 6h 结算）】{slow_txt}{p90_txt}"
                                   f" / ★ 【摇摆（★ featured 状态稳定性）】{swing_txt}"
                                   f"（★ ★ 两分量**跨度不同**：★ 指纹覆盖到「读数时刻」，"
                                   f"★ slow 覆盖到「读数时刻 − 6h」⇒ ★ 行内数字不可直接相加）"
                                   f"{xwin_txt}"
                                   f"{bias_txt}{contam_txt}")))
        else:
            if n is None or n < 10:
                rows.append((label, base_txt, obs,
                             verdict(None, f"★ post n={n} < 10 ⇒ **样本不足以判定**（★ 方向可读，★ 不许外推）")))
                continue
            if p90 is None or slow is None:
                rows.append((label, base_txt, obs, verdict_missing("★ post 的 p90_gap / slow 算不出")))
                continue
            lim_slow = round(pre_slow_eff * 1.25, 1)
            ok = p90 <= pre_p90 and slow <= lim_slow
            # ★★★★ 第 26 处口径修正：★ 「::int 取整边界」伪影 ⇒ **只对 p90 分量**披露
            #   ★★ pre_p90 是**取整样本** ⇒ 阈值真值 ∈ [pre_p90−0.5, pre_p90+0.5) ⇒ slack = 0.5
            #   ★★ ★ slow 分量是**百分比**（不是分钟分位数）⇒ **不受本类缺陷影响** ⇒ 不混披露
            rnd_txt = ""
            if not ok and p90 > pre_p90:
                rnd_txt = rounding_disclosure(field(obs, "p90_gap_raw"), pre_p90,
                                              slack=0.5, what="post p90_gap")
            if not ok:
                rnd_txt += percentile_fragility_disclosure(n, b.get("n"), "p90_gap")
            # ★★ 附「fp 口径」的对照读数（★ 豁免类**设计上钉 6h** ⇒ 未减半顶格应当**仍在**）
            fctl = ""
            if fobs is not None:
                _ftop = field(fobs, "top21600_n")
                if _ftop is not None:
                    fctl = (f" ★（★ 对照：豁免类在 fp 口径的未减半 6h 顶格 {_ftop:.0f} 条"
                            f"（★ 设计上钉 6h ⇒ **期望 >0** ⇒ 若为 0 ⇒ 说明分层或机制变了））")
            # ★★★★ 第 13 处纪律的最后一处候选点：★ **本行同时引用了两个视图** ⇒ 两个 n 必须标明窗口
            _split_x = data.get(f"M20_{lay}_post_split")
            xwin_txt = cross_window_disclosure(n, field(fobs, "n_all"),
                                                field(_split_x, "window_n"),
                                                field(_split_x, "tail_n"))
            rows.append((label, base_txt, obs,
                         verdict(ok, f"★ 豁免类（★ 协议形态：设计上钉 6h、乘数豁免）post p90_gap={p90:.0f}"
                                   f"（期望 ≤{pre_p90}）/ 慢事件 {slow}%（期望 ≤{lim_slow}%；★ {pre_slow_src}：{pre_slow_eff}%）"
                                   f" ⇒ {'未变慢（不回归成立）' if ok else '★ 豁免类被拖慢'}"
                                   f"（★ n={n}）{fctl}{xwin_txt}{contam_txt}{rnd_txt}")))

    # ── M11：episode 层症状 ──────────────────────────────────────────
    for tag, label, bkey in (("namespaced", "M11 namespaced 恢复感知时延", "M11_ns_pre"),
                             ("bare", "M11 bare 恢复感知时延", "M11_bare_pre")):
        base = BASELINES[bkey]["base"]
        base_txt = base_cell(bkey)
        key = f"M11b_post_fix_{tag}"
        obs = data.get(key)
        if obs is None:
            # ★★ **「键不存在」≠「没有 episode」**（★ 本轮真踩到）：★ 采集侧若把 post 段的
            #   「成功行」也过滤掉 ⇒ ⇒ **该键根本不产出** ⇒ ⇒ 这里会显示「post 段无 episode」
            #   ⇒ ⇒ ★★ **读起来像「样本不足」，实际是「采集口径把判据行静默掉了」** ⇒ ⇒ 假阴性
            rows.append((label, base_txt, "—",
                         verdict(None, "★ post 段无 episode（★ ★ **键未产出**：★ 若窗口已满 ⇒ "
                                       "先查采集侧是否把 post 段的**成功行**也过滤了 ⇒ ★ 不是「量具缺数」")))
            continue
        n = head_count(obs)
        med = field(obs, "med_min", "med")
        p90 = field(obs, "p90")
        mx = field(obs, "max")
        if n is None or n < 5:
            rows.append((label, base_txt, f"n={n} med={med} p90={p90} max={mx}",
                         verdict(None, f"★ post episodes={n} < 5 ⇒ **样本不足以判定**，★ 不许外推")))
        elif med is None or p90 is None or mx is None:
            rows.append((label, base_txt, f"n={n} med={med} p90={p90} max={mx}",
                         verdict_missing("★ post 的 med/p90/max 算不出（样本不足）")))
        else:
            ok = med <= base[0] and p90 <= base[1] and mx <= base[2]
            # ★★★★ 第 24 处口径修正：★ 行内点明「post 段已被 now() 界定」⇒ ★ 与 stage 窗口同量纲
            #   ★★ 同时把「pre 基线是 7 天」的量纲提醒带上（★ 诚实边界 ㉚）⇒ ★ 报告读者不会误读
            rows.append((label, base_txt, f"n={n} med={med} p90={p90} max={mx}",
                         verdict(ok, "★ 逐项不劣于 pre 即达成"
                                   "（★ post 段**已被 now() 界定** ⇒ ★ 与 stage 窗口同量纲；"
                                   "★ ★★ 但 pre 基线取自 **7 天** ⇒ ★ Stage 1 量纲不同 ⇒ ★ 只能当描述性读数）")))

    # ── M12p：待复检护栏（★ 「复检不能反而变慢」）──────────────────────────
    # ★★ 口径（★ 本轮第 5 处修正）：★ pending = 「失败之后没有任何后续探测」
    #   ⇒ 观察窗口越长越容易被计入 ⇒ ★★ 7 天 pre 与 24h post **不同量纲**
    #   ⇒ 判据改为「post 绝对水平 ≤1%」+ 与**切点前 24h**（同量纲）对照
    obs = data.get("M12p_pending_post_ns_not")
    pre_obs = data.get("M12p_pending_pre24_ns_not")
    b = BASELINES["M12p_pending_ns_not_pre24"]
    if obs is None:
        rows.append(("M12p ns 非featured 待复检率",
                     base_cell("M12p_pending_ns_not_pre24"), "—",
                     verdict(None, "★ post 段无事件（尚未结算）/ 指标缺失")))
    else:
        n_fail = head_count(obs)
        pct = pending_pct(obs)
        pre_pct = pending_pct(pre_obs) if pre_obs else None
        if pct is None:
            rows.append(("M12p ns 非featured 待复检率",
                         base_cell("M12p_pending_ns_not_pre24"), obs,
                         verdict_missing("★ pending_pct 算不出")))
        elif n_fail is None or n_fail < b.get("min_n", 30):
            rows.append(("M12p ns 非featured 待复检率",
                         base_cell("M12p_pending_ns_not_pre24"), obs,
                         verdict(None, f"★ post failures={n_fail} < {b.get('min_n', 30)} ⇒ 比率不稳（方向可读）")))
        else:
            lim = b["limit"]
            ok = pct <= lim
            cmp_txt = (f"（同量纲对照 pre24h = {pre_pct}%）" if pre_pct is not None else "（★ 无 pre24h 对照）")
            rows.append(("M12p ns 非featured 待复检率",
                         base_cell("M12p_pending_ns_not_pre24"), obs,
                         verdict(ok, f"★ post pending_pct={pct}%（上限 {lim}%）{cmp_txt}"
                                   f"⇒ {'复检没有变慢' if ok else '★ 待复检率升高 ⇒ 复检反而变慢'}")))

    # ★★ 守卫不过 ⇒ **整表作废**（★ 在 judge() 内统一做，★ 别在打印层做
    #    ⇒ 否则「自测断言的裁决」与「人看到的裁决」会不一致）
    if bad:
        rows = [(r[0], r[1], r[2], "★★ **量具守卫未通过 ⇒ 本行裁决作废**") for r in rows]

    return rows, bad, guards


# ─────────────────────────────────────────────────────────────────────
# 合成数据自测（★★ 关窗前必须证明「数据到位时判定链路是通的」）
# ─────────────────────────────────────────────────────────────────────
def _synthetic(pass_case: bool) -> dict[str, str]:
    """构造一份「窗口已满」的合成复采输出。pass_case=True ⇒ 全达成；False ⇒ 全未达成。

    ★★ 合成值**必须严格对齐真实判据阈值**（否则自测会断言错的东西）：
    ★★ **失败用例里每个判据行只坏一个东西** ⇒ ★ 任何**单点**阈值/条件变异都会被抓到
       （★ 否则「两个条件都坏」会互相掩盖 ⇒ 变异漏网 —— 本轮实测踩过）
      · M11 ns pre = med/p90/max = 3/360/1791 ⇒ pass 段 med ≤3、p90 ≤360、max ≤1791
      · M12s ns_feat / ns_not = **参考项**（post 人群会变 ⇒ 不可作判据 ⇒ 期望是 ⚠️ 不是 ✅/❌）
      · M14 old_feat（无回归）= p90 ≤60 且 p99 ≤1791
      · M14 newly（效应）= p90 ≤180 且 slow_gt300_pct ≤ 6.5（pre 13.0 的一半）
      · M14 other（对照）= med ≈360（±20%）
      · ★★ **M14x = 全计费口径**（★ 本轮第 9 处修正）：★ 同一套判据，**各自的基线**
        （★ M14x newly pre24：p90≤180 且 slow ≤ 33.95（pre 67.9 折半）；pre7d：slow ≤ 13.9）
      · M12p = post pending_pct ≤1.0% 且 failures ≥30
    """
    if pass_case:
        # ★★ M20 post（★ 本轮新增）：★ 阈值对齐 **Stage 1 的 pre24 基线**
        #   normal 判据：top21600_n == 0（★ 机制指纹）且 slow ≤ 14.3/2 = 7.15
        m20_normal_post = ("21 events | med_gap=60 | p90_gap=180 | slow_gt300_pct=5.0"
                           " | ladder_p50=30 | top21600_n=0 | ladder_max_s=10800")
        #   exempt 判据：p90_gap ≤ 360 且 slow ≤ 87.7×1.25 = 109.6
        m20_exempt_post = ("57 events | med_gap=360 | p90_gap=360 | slow_gt300_pct=87.7"
                           " | ladder_p50=360 | top21600_n=54 | ladder_max_s=21600")
        # ★★ M20 的**指纹视图**（★ 第 18 处口径修正）★ post 两个键 = ★ **真捕获**（★ 211 真库）
        #   normal：★ 未减半 6h 顶格 **0** 条（★ 乘数生效）★ 且有**正向证据**（★ 减半独占值 10 条）
        #   exempt：★ 未减半 6h 顶格 **4** 条（★ 设计上钉 6h ⇒ 未减半顶格**仍在** ⇒ 对照组成立）
        m20_normal_post_fp = ("n_all=10 | top21600_n=0 | top10800_n=1 | mul_evidence_n=10"
                              " | ladder_p50=30 | ladder_max_s=10800")
        m20_exempt_post_fp = ("n_all=4 | top21600_n=4 | top10800_n=0 | mul_evidence_n=0"
                              " | ladder_p50=360 | ladder_max_s=21600")
        s12s = "394 events | med_min=30 | p90=60 | p99=1200"      # ★ 参考项（人群会变）
        s12n = "1703 events | med_min=360 | p90=361 | p99=5524"    # ★ 参考项
        s11n = "7 episodes | med=3 | p90=60 | max=1200"            # 3≤3 ✔ 60≤360 ✔ 1200≤1791 ✔
        s11b = "5 episodes | med=3 | p90=120 | max=300"            # 3≤3 ✔ 120≤180 ✔ 300≤360 ✔
        m15_post_med = 120                                          # ≤180 ✔
        # ★★ M15x post（★ 本轮第 13 处口径修正）：★ deepseek 的「全达成」读数
        #   ★ normal med 30 ≤ pre 60/2=30 ✔；★ 阶梯 p50 1800 ≤ pre 3600/2=1800 ✔
        #   ★ 未减半档 3600（★ pre 侧 18 条）⇒ post 必须清零 ✔；★ 21600 档 pre24=0 ⇒ 恒真
        m15x_post = ("27 events | exempt=9 | normal=18 | normal_med_min=30"
                     " | normal_ladder_p50_s=1800 | normal_top3600_n=0"
                     " | normal_top7200_n=0 | normal_top21600_n=0"
                     " | normal_ladder_max_s=1800")
        m12p = "240 failures_aged6h | pending=1 | pending_pct=0.4"  # 0.4≤1.0 ✔ n≥30 ✔
        # ★★ 按 **Stage 1 的 pre24 基线**（old_feat p90≤31/p99≤360）取值
        m14_old = "56 events | med_min=3 | p90_min=20 | p99_min=300 | max_min=300 | slow_gt300_pct=3.0"
        m14_new = "66 events | med_min=30 | p90_min=120 | p99_min=600 | max_min=600 | slow_gt300_pct=20.0"
        m14_oth = "300 events | med_min=360 | p90_min=368 | p99_min=5524 | max_min=5659 | slow_gt300_pct=85.0"
        # ★★ 全计费口径（★ 本轮第 9 处口径修正）★ 阈值**按各自口径**取：
        #   M14x_old_feat_pre24  p90≤31 / p99≤360 / slow≤4.0
        #   M14x_newly_pre24     p90≤180（pre p90 360 折半）/ slow≤33.95（pre 67.9 折半）
        #   M14x_other_pre24     med≈360（±20%）
        m14x_old = "226 events | med_min=4 | p90_min=20 | p99_min=300 | max_min=300 | slow_gt300_pct=4.0"
        m14x_new = "78 events | med_min=30 | p90_min=120 | p99_min=600 | max_min=600 | slow_gt300_pct=20.0"
        m14x_oth = "972 events | med_min=360 | p90_min=360 | p99_min=360 | max_min=360 | slow_gt300_pct=74.0"
        # ★★ 第 21 处口径修正：★ 「对照组」的**相位/构成**键（★ post 侧）
        #   ★ 「达成」场景：★ post 侧**含 6h 顶格事件**（★ 与 pre 同构 ⇒ 可比）
        m14_oth_comp = ("n=300 | models_n=85 | top21600_n=240 | slow_gt300_pct=85.0"
                        " | ladder_spectrum=21600,3600")
        m14x_oth_comp = ("n=972 | models_n=85 | top21600_n=840 | slow_gt300_pct=74.0"
                         " | ladder_spectrum=21600,3600")
        m3b_f429, m3b_n429, m3b_f503, m3b_n503 = 300, 3600, 1800, 3600
        m2, m6, m9, m5 = "34 rp + 15 canon + 200 raw_variant = 210", \
            "80 featured / 1060 total_free_eligible", \
            "0 个 featured canonical 的 raw 变体未进 static（★ 期望 0）", \
            "9228 total | free=1234"
    else:
        # ★★ M20 post 的失败用例（★ 每行**只坏一个分量**）
        #   normal：只坏 top21600_n（0 → 3）⇒ ❌（★ slow / p90 都好）
        m20_normal_post = ("21 events | med_gap=60 | p90_gap=180 | slow_gt300_pct=5.0"
                           " | ladder_p50=180 | top21600_n=3 | ladder_max_s=21600")
        #   exempt：只坏 p90_gap（360 → 720）⇒ ❌（★ slow 仍好）
        m20_exempt_post = ("57 events | med_gap=360 | p90_gap=720 | slow_gt300_pct=87.7"
                           " | ladder_p50=360 | top21600_n=54 | ladder_max_s=21600")
        # ★★ 指纹视图的失败用例（★ 第 18 处口径修正）：★ 指纹分量读的是 **fp 键**
        #   ⇒ ★ 失败用例必须把 **fp 键**的未减半顶格弄坏（★ top21600_n 0 → 3）⇒ ❌
        m20_normal_post_fp = ("n_all=21 | top21600_n=3 | top10800_n=1 | mul_evidence_n=10"
                              " | ladder_p50=180 | ladder_max_s=21600")
        m20_exempt_post_fp = ("n_all=4 | top21600_n=4 | top10800_n=0 | mul_evidence_n=0"
                              " | ladder_p50=360 | ladder_max_s=21600")
        s12s = "394 events | med_min=600 | p90=900 | p99=2400"      # ★ 仍是参考项
        s12n = "1703 events | med_min=600 | p90=700 | p99=6000"    # ★ 仍是参考项
        # ★★ 失败用例：**每行只坏一个东西** ⇒ ★ 任何单点阈值变异都会被抓到
        s11n = "7 episodes | med=10 | p90=60 | max=1200"            # 只坏 med（10>3）⇒ ❌
        s11b = "5 episodes | med=10 | p90=120 | max=300"            # 只坏 med（10>3）⇒ ❌
        m15_post_med = 300                                          # >180 ⇒ ❌
        # ★★ M15x post 的失败用例（★ **只坏一个分量**：★ 阶梯中位 1800 → 3600 ⇒ ❌
        #   ★ 观察间隔（30 ≤ 30）与未减半档清零（3600 档 post=0）都仍然好）
        m15x_post = ("27 events | exempt=9 | normal=18 | normal_med_min=30"
                     " | normal_ladder_p50_s=3600 | normal_top3600_n=0"
                     " | normal_top7200_n=0 | normal_top21600_n=0"
                     " | normal_ladder_max_s=3600")
        m12p = "240 failures_aged6h | pending=40 | pending_pct=16.7"  # >1.0 ⇒ ❌
        m14_old = "56 events | med_min=3 | p90_min=200 | p99_min=300 | max_min=300 | slow_gt300_pct=3.0"
        m14_new = "66 events | med_min=30 | p90_min=600 | p99_min=1400 | max_min=1400 | slow_gt300_pct=20.0"
        m14_oth = "300 events | med_min=120 | p90_min=368 | p99_min=5524 | max_min=5659 | slow_gt300_pct=85.0"
        # ★★ 全计费口径的失败用例（★ 每行仍只坏一个东西）
        m14x_old = "226 events | med_min=4 | p90_min=200 | p99_min=300 | max_min=300 | slow_gt300_pct=4.0"
        m14x_new = "78 events | med_min=30 | p90_min=600 | p99_min=1400 | max_min=1400 | slow_gt300_pct=20.0"
        m14x_oth = "972 events | med_min=120 | p90_min=360 | p99_min=360 | max_min=360 | slow_gt300_pct=74.0"
        # ★★ 第 21 处口径修正：★ 「未达成」场景下的构成 = ★ **post 侧零 6h 顶格事件**
        #   ⇒ ★★ 两窗口**形态不同构** ⇒ ★★ 必须触发「相位/构成」披露（★ 但裁决符号仍是 ❌）
        m14_oth_comp = ("n=300 | models_n=11 | top21600_n=0 | slow_gt300_pct=0.0"
                        " | ladder_spectrum=3600")
        m14x_oth_comp = ("n=972 | models_n=11 | top21600_n=0 | slow_gt300_pct=0.0"
                         " | ladder_spectrum=3600")
        m3b_f429, m3b_n429, m3b_f503, m3b_n503 = 3600, 1800, 3600, 1800  # 两类错误码都失配 ⇒ ❌
        m2, m6, m9, m5 = "34 rp + 15 canon + 160 raw_variant = 190", \
            "70 featured / 1060 total_free_eligible", \
            "2 个 featured canonical 的 raw 变体未进 static（★ 期望 0）", \
            "9800 total | free=1234"

    d: dict[str, str] = {
        "M16_join_caliber_check": "used=100 ref=100 | legacy_背景=30 | ✔ 口径健康（used == ref ⇒ 两种独立写法一致）",
        "M17_event_definition_check": "50 events | zero_gap=0 | under_1min=0% | ✔ 无重复行污染",
        "M2_featured_static": m2,
        "M6_free_featured_pairs": m6,
        "M9_missing_raw_variant": m9,
        "M5_probe_runs_24h": m5,
        # ★★★ 第 22 处口径修正：★ M5 的**逐日探测量分布**（★ 真捕获 2026-11-11 00:3x / 211）
        #   ★★ pre 侧那个键是 **pre 侧读数** ⇒ ★★ 它若混进 now() 依赖 ⇒ ★ 随时钟漂移
        #   ★★ ⇒ ★ 由 **clockcheck 的 CLOCK_INDEP_KEYS** 单独查（★ **不进 M18 冻结对拍** ⇒ 不重冻）
        "M5_pre7d_runs_by_day": ("days=11-03:1292,11-04:2764,11-05:7794,11-06:9905,11-07:8973"
                                 ",11-08:2500,11-09:9685,11-10:5589 | n_days=8 | min=1292"
                                 " | max=9905 | total=48502"),
        "M5_post_runs_by_day": ("days=11-10:4218,11-11:115 | n_days=2 | min=115"
                                " | max=4218 | total=4333"),
        # ★★★ 第 31 处：★ M5 的「**等长滚动 24h**」基线分布（★ 真捕获 2026-11-11 03:4x / 211）
        #   ★★ **端点锚定 era 切点 ⇒ 时钟无关** ⇒ ★★ 由 clockcheck 的 CLOCK_INDEP_KEYS 单独查
        #   ★★ ⇒ ★★ **不进 M18 冻结对拍** ⇒ ★★ **不触发重冻**（★ 与 `M5_pre7d_runs_by_day` 同款定位）
        "M5_roll24_pre7d_dist": ("n=145 | min=2467 | mean=7037.5 | p50=7871 | p75=9755"
                                 " | p90=10417 | p95=10693 | max=11197 | sum=1020438"
                                 " | each_window_hours=24"),
        # ★ 2026-11-11 关窗预演补：★ M5 的 **by_kind 分解**（★ 真捕获 2026-11-11 00:5x / 211；
        #   ★★ now()-锚定滚动 24h 窗（与 M5 主键同窗）⇒ **不进** CLOCK_INDEP_KEYS
        #   ★（★ 关窗时刻 now=切点+24h ⇒ 读数恰好 = 纯 post 段））
        "M5_probe_runs_24h_by_kind": ("credential_recovery=6082 | periodic=3282 | sync_request=450"
                                      " | admin=0 | external_async=0 | integrity_probe_planner=0"
                                      " | manual=0 | other=0 | request_failure=0 | selfcheck=0"),
        "M3b_backoff_ns_featured_err_http_429": f"5 rows | median_retry_s={m3b_f429}",
        "M3b_backoff_ns_not_err_http_429": f"9 rows | median_retry_s={m3b_n429}",
        "M3b_backoff_ns_featured_err_http_503": f"4 rows | median_retry_s={m3b_f503}",
        "M3b_backoff_ns_not_err_http_503": f"7 rows | median_retry_s={m3b_n503}",
        "M12s_settled_post_ns_feat": s12s,
        "M12s_settled_post_ns_not": s12n,
        "M11b_post_fix_namespaced": s11n,
        "M11b_post_fix_bare": s11b,
        "M14_post_old_feat": m14_old,
        "M14_post_newly_covered": m14_new,
        "M14_post_other": m14_oth,
        # ★★ 第 21 处口径修正：★ 「对照组」的相位/构成键（★ post 侧 ⇒ 不进 M18 冻结对拍）
        "M14_post_other_composition": m14_oth_comp,
        "M14x_post_old_feat": m14x_old,
        "M14x_post_newly_covered": m14x_new,
        "M14x_post_other": m14x_oth,
        "M14x_post_other_composition": m14x_oth_comp,
        "M12p_pending_post_ns_not": m12p,
        "M12p_pending_pre24_ns_not": "325 failures_aged6h | pending=0 | pending_pct=0.0",
        # ★★ M14/M14x 的 **pre 键**（★ 真库实测值，2026-11-10 211）
        #   ★ 加进来的目的：★ 让 **M18 冻结对拍**与 **M19 结构性不变量**在**自测里也被走到**
        #   ★★ 否则自测只覆盖 post 侧 ⇒ 守卫的另一半从未被验证过
        "M14_pre6d_newly_covered": "447 events | med_min=2 | p90_min=360 | p99_min=1390 | max_min=5523 | slow_gt300_pct=10.3",
        "M14_pre24_newly_covered": "14 events | med_min=360 | p90_min=360 | p99_min=360 | max_min=360 | slow_gt300_pct=100.0",
        "M14_pre7d_newly_covered": "461 events | med_min=2 | p90_min=360 | p99_min=1390 | max_min=5523 | slow_gt300_pct=13.0",
        "M14_pre6d_old_feat": "300 events | med_min=3 | p90_min=120 | p99_min=1791 | max_min=5522 | slow_gt300_pct=8.7",
        "M14_pre24_old_feat": "94 events | med_min=3 | p90_min=31 | p99_min=360 | max_min=360 | slow_gt300_pct=6.4",
        "M14_pre7d_old_feat": "394 events | med_min=3 | p90_min=60 | p99_min=1791 | max_min=5522 | slow_gt300_pct=8.1",
        "M14_pre6d_other": "930 events | med_min=360 | p90_min=369 | p99_min=5524 | max_min=5659 | slow_gt300_pct=88.4",
        "M14_pre24_other": "311 events | med_min=360 | p90_min=360 | p99_min=360 | max_min=360 | slow_gt300_pct=77.2",
        "M14_pre7d_other": "1241 events | med_min=360 | p90_min=368 | p99_min=5524 | max_min=5659 | slow_gt300_pct=85.6",
        "M14x_pre6d_newly_covered": "613 events | med_min=3 | p90_min=360 | p99_min=5517 | max_min=5551 | slow_gt300_pct=22.7",
        "M14x_pre24_newly_covered": "78 events | med_min=360 | p90_min=360 | p99_min=361 | max_min=361 | slow_gt300_pct=67.9",
        "M14x_pre7d_newly_covered": "691 events | med_min=3 | p90_min=360 | p99_min=5517 | max_min=5551 | slow_gt300_pct=27.8",
        "M14x_pre6d_old_feat": "1652 events | med_min=3 | p90_min=30 | p99_min=361 | max_min=5522 | slow_gt300_pct=4.4",
        "M14x_pre24_old_feat": "226 events | med_min=4 | p90_min=31 | p99_min=360 | max_min=360 | slow_gt300_pct=8.0",
        "M14x_pre7d_old_feat": "1878 events | med_min=3 | p90_min=31 | p99_min=361 | max_min=5522 | slow_gt300_pct=4.8",
        "M14x_pre6d_other": "2820 events | med_min=360 | p90_min=369 | p99_min=5524 | max_min=5669 | slow_gt300_pct=87.9",
        "M14x_pre24_other": "972 events | med_min=360 | p90_min=360 | p99_min=360 | max_min=360 | slow_gt300_pct=74.0",
        "M14x_pre7d_other": "3792 events | med_min=360 | p90_min=368 | p99_min=5524 | max_min=5669 | slow_gt300_pct=84.4",
        # ★★ M20 的 **8 个键**（★ pre 6 个 = 真库实测冻结值；post 2 个按场景给）
        "M20_normal_pre6d": "133 events | med_gap=3 | p90_gap=360 | slow_gt300_pct=12.0 | ladder_p50=3 | top21600_n=17 | ladder_max_s=21600",
        "M20_normal_pre24": "21 events | med_gap=60 | p90_gap=360 | slow_gt300_pct=14.3 | ladder_p50=60 | top21600_n=3 | ladder_max_s=21600",
        "M20_normal_pre7d": "154 events | med_gap=3 | p90_gap=360 | slow_gt300_pct=12.3 | ladder_p50=3 | top21600_n=20 | ladder_max_s=21600",
        "M20_exempt_pre6d": "480 events | med_gap=2 | p90_gap=360 | slow_gt300_pct=25.6 | ladder_p50=0 | top21600_n=124 | ladder_max_s=30658",
        "M20_exempt_pre24": "57 events | med_gap=360 | p90_gap=360 | slow_gt300_pct=87.7 | ladder_p50=360 | top21600_n=54 | ladder_max_s=21600",
        "M20_exempt_pre7d": "537 events | med_gap=3 | p90_gap=360 | slow_gt300_pct=32.2 | ladder_p50=0 | top21600_n=178 | ladder_max_s=30658",
        "M20_normal_post": m20_normal_post,
        "M20_exempt_post": m20_exempt_post,
        # ★★★★ M20 的**指纹视图**（★ 第 18 处口径修正，★★ 详见主档 §5.9.21.13.14）
        #   ★★ **pre 的 4 个键 = 真库实测值**（★ 与 fixtures/p0-3-m20-fp-pre.txt 逐字同形）
        #   ★★ ★ pre 侧这 4 个键是**量具正控**：★ 若 pre 侧 top21600_n=0 ⇒ 量具无鉴别力
        #   ★★   ⇒ ★ post 的 0 不可解释 ⇒ ★★ 判定器 fail-closed（判不可判，★ 不判绿）
        "M20_normal_pre24_fp": ("n_all=29 | top21600_n=4 | top10800_n=0 | mul_evidence_n=0"
                                " | ladder_p50=60 | ladder_max_s=21600"),
        "M20_normal_pre7d_fp": ("n_all=165 | top21600_n=21 | top10800_n=0 | mul_evidence_n=47"
                                " | ladder_p50=3 | ladder_max_s=21600"),
        "M20_exempt_pre24_fp": ("n_all=80 | top21600_n=71 | top10800_n=0 | mul_evidence_n=0"
                                " | ladder_p50=360 | ladder_max_s=21600"),
        "M20_exempt_pre7d_fp": ("n_all=564 | top21600_n=196 | top10800_n=0 | mul_evidence_n=2"
                                " | ladder_p50=0 | ladder_max_s=423256"),
        # ★★ post 的 2 个键按场景给（★ 真捕获为基准）
        "M20_normal_post_fp": m20_normal_post_fp,
        "M20_exempt_post_fp": m20_exempt_post_fp,
        # ★★ M20 的 **post 段切分**（★ 第 18 处口径修正 ⇒ ★ 尾部可见性守卫的输入）
        #   ★ window_n = post 段里 now()-6h **之前**的失败数（★ 成熟视图能看见的）
        #   ★ tail_n   = post 段里 **最后 6h** 的失败数（★ ★ 成熟视图**完全不可见**的那一段）
        #   ★ ★ 不变量：★ `fp_n_all ≥ window_n + tail_n` ⇒ ★ fp 视图被改回带成熟度过滤 ⇒ 必破
        "M20_normal_post_split": "window_n=0 | tail_n=10",
        "M20_exempt_post_split": "window_n=0 | tail_n=4",
        # ★★ M20 的**「摇摆」**键（★ 第 20 处口径修正 ⇒ ★ 修复前后的判别信号）
        #   ★★ swing_n = post 段 normal 层里「同一模型同时出现减半独占值与未减半 6h 顶格」的模型数
        #   ★★ 实测（★ 真捕获 2026-11-10 19:2x）：★ swing_n=**0**（★ 2 只 normal 模型 ⇒ 都稳定 featured）
        "M20_normal_post_fp_swing": "swing_n=0 | swing_models=- | models_n=2",
        # ★★ **逐模型 fp 阶梯谱**（★ 真捕获 ⇒ ★ Stage 1 逐模型复核用）
        "M20_normal_post_fp_model__anthropic/claude-opus-4.8": "n=1 | mul_evid=1 | top21600=0 | ladder=10800",
        "M20_normal_post_fp_model__deepseek-ai/deepseek-v4-pro-0813": "n=10 | mul_evid=10 | top21600=0 | ladder=1800",
        # ★★ era 纯度存活者偏差自检（★ 真库实测 pre24/pre7d 均为 0 ⇒ 无存活者偏差）
        "M20_era_dropped_pre24": "0 events dropped_by_era_purity",
        "M20_era_dropped_pre7d": "0 events dropped_by_era_purity",
        # ★★★★ M20 的**阶梯构成**（★ 本轮第 14 处口径修正 + ★ 第 16 处修正）：★ pre 基线里混着
        #   「pre 期已 featured（乘数已生效）」的样本 ⇒ ★ 「newly」≠「pre 期未 featured」
        #   ⇒ ★ 「post ≤ pre/2」阈值**偏严** ⇒ ★ 阈值必须按「**干净 pre**」推导
        #   ★★ 证据集 = {7,90,150,450,1800,10800}（★ 「未减半阶梯空间」取半后的独占值）
        #   ★★ 真库实测：★ normal pre7d 污染 **47/154 = 30.5%**；★ clean 19/107 = **17.8%**
        #   ★★ ★★ 本行的**格式必须与真实采集输出逐字同形**（★ 否则自测验的是虚构 ⇒ §662 的教训）
        "M20_normal_pre24_composition": ("n=21 | mul_evidence=0 | clean_n=21 | clean_slow=3"
                                          " | slow=3 | contam_pct=0.0 | null_ladder_n=0"
                                          " | unexplained_n=0 | unexplained_values=-"),
        "M20_normal_pre7d_composition": ("n=154 | mul_evidence=47 | clean_n=107 | clean_slow=19"
                                          " | slow=19 | contam_pct=30.5 | null_ladder_n=0"
                                          " | unexplained_n=0 | unexplained_values=-"),
        "M20_exempt_pre24_composition": ("n=57 | mul_evidence=0 | clean_n=57 | clean_slow=50"
                                         " | slow=50 | contam_pct=0.0 | null_ladder_n=0"
                                         " | unexplained_n=0 | unexplained_values=-"),
        "M20_exempt_pre7d_composition": ("n=537 | mul_evidence=2 | clean_n=535 | clean_slow=173"
                                         " | slow=173 | contam_pct=0.4 | null_ladder_n=0"
                                         " | unexplained_n=0 | unexplained_values=-"),
    }
    # ★★ M15x 的 **12 个键**（★ 本轮第 13 处口径修正）：
    #   ★ pre 键的**事件数必须与冻结基线逐字一致**（★ 否则 M18 守卫会报漂移 ⇒ 整表作废
    #   ★ ⇒ 自测自己就过不了 ⇒ ★ 这正是「冻结对拍」该有的约束力）
    #   ★★ 三只「观察面」金丝雀**各用自己**的冻结值（★ 不能共用一套 ⇒ 否则 M18 必报漂移）
    #   ★ post 键：★ 「全达成」场景给**好值**（★ deepseek：med 30 ≤ 30、ladder 1800 ≤ 1800、
    #   ★   未减半档 3600/21600 都清零）；★「全未达成」场景**只坏一个分量**（★ 阶梯中位）
    m15x_obs_pre = {
        "minimaxai/minimax-m2.7": (
            "11 events | exempt=11 | normal=0 | normal_med_min=? | normal_ladder_p50_s=?"
            " | normal_top3600_n=0 | normal_top7200_n=0 | normal_top21600_n=0"
            " | normal_ladder_max_s=?",
            "48 events | exempt=47 | normal=1 | normal_med_min=360 | normal_ladder_p50_s=21600"
            " | normal_top3600_n=0 | normal_top7200_n=0 | normal_top21600_n=1"
            " | normal_ladder_max_s=21600"),
        "minimaxai/minimax-m3": (
            "9 events | exempt=9 | normal=0 | normal_med_min=? | normal_ladder_p50_s=?"
            " | normal_top3600_n=0 | normal_top7200_n=0 | normal_top21600_n=0"
            " | normal_ladder_max_s=?",
            "43 events | exempt=41 | normal=2 | normal_med_min=360 | normal_ladder_p50_s=21600"
            " | normal_top3600_n=0 | normal_top7200_n=0 | normal_top21600_n=2"
            " | normal_ladder_max_s=21600"),
        "z-ai/glm-5.3": (
            "9 events | exempt=9 | normal=0 | normal_med_min=? | normal_ladder_p50_s=?"
            " | normal_top3600_n=0 | normal_top7200_n=0 | normal_top21600_n=0"
            " | normal_ladder_max_s=?",
            "71 events | exempt=70 | normal=1 | normal_med_min=361 | normal_ladder_p50_s=21600"
            " | normal_top3600_n=0 | normal_top7200_n=0 | normal_top21600_n=1"
            " | normal_ladder_max_s=21600"),
    }
    for pair in CANARIES:
        d[f"M15_pre__{pair}"] = "13 events | med_min=360 | p90_min=360 | p99_min=1791 | max_min=1791"
        d[f"M15_post__{pair}"] = (f"9 events | med_min={m15_post_med} | p90_min=240"
                                  f" | p99_min=600 | max_min=600")
        if pair == CANARY_JUDGEABLE:
            d[f"M15x_pre24__{pair}"] = (
                "27 events | exempt=9 | normal=18 | normal_med_min=60 | normal_ladder_p50_s=3600"
                " | normal_top3600_n=18 | normal_top7200_n=0 | normal_top21600_n=0"
                " | normal_ladder_max_s=3600")
            d[f"M15x_pre7d__{pair}"] = (
                "77 events | exempt=36 | normal=41 | normal_med_min=60 | normal_ladder_p50_s=3600"
                " | normal_top3600_n=31 | normal_top7200_n=0 | normal_top21600_n=10"
                " | normal_ladder_max_s=21600")
            d[f"M15x_post__{pair}"] = m15x_post
        else:
            d[f"M15x_pre24__{pair}"], d[f"M15x_pre7d__{pair}"] = m15x_obs_pre[pair]
            d[f"M15x_post__{pair}"] = (
                "6 events | exempt=5 | normal=1 | normal_med_min=360 | normal_ladder_p50_s=21600"
                " | normal_top3600_n=0 | normal_top7200_n=0 | normal_top21600_n=1"
                " | normal_ladder_max_s=21600")
    return d


def _load_expected_facts(filename: str) -> dict[str, str]:
    """★ 读「期望值登记表」（★ **独立文件** ⇒ ★★ 判据字面量与被守卫内容**不同源** ⇒ ★ 守卫不会被同源变异致盲）

    ★★ 形态（本轮真踩到，★ LESSONS §679）：★ 守卫的判据字面量若与被守卫的事实住在**同一个文件**里
    ★★   ⇒ ★★ 变异「事实」时会**把判据一起改掉** ⇒ ★★ 守卫被致盲 ⇒ ★★ 「变异打不中」会被误读成「守卫没牙」
    ★★ ⇒ ★★ 隔离层 = ★ 把期望值放到**另一个文件** ⇒ ★ 只改内容不改判据 ⇒ ★ 守卫照样红

    ★ 格式：``key|value`` 逐行（★ 与 fixtures/ 下既有夹具同格式）；★ ``#`` 开头为注释；★ 值为空 = 该事实此刻不成立
    """
    p = Path(__file__).resolve().parent / "fixtures" / filename
    out: dict[str, str] = {}
    for raw in p.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "|" not in line:
            continue
        k, v = line.split("|", 1)
        out[k.strip()] = v.strip()
    return out


_M5_FX_CACHE: dict[str, str] | None = None


def _load_m5_fixture() -> dict[str, str]:
    """★ 读 M5 的**期望值登记表**（★ 独立夹具 ⇒ ★★ 判据字面量与被守卫内容**不同源** ⇒ ★ 同源变异打不中它）

    ★★ 读不到 ⇒ 返回空 dict（★ 行内披露写「不可见」）★ ★★ 但自测会 **fail-closed**（★ 不可判 ≠ 没发现问题）
    """
    global _M5_FX_CACHE
    if _M5_FX_CACHE is None:
        try:
            _M5_FX_CACHE = _load_expected_facts("p0-3-m5-baseline-pre7d.txt")
        except Exception:
            _M5_FX_CACHE = {}
    return _M5_FX_CACHE


def selftest() -> int:
    """★ 四种场景各断言一次：① 全达成 ② 全未达成 ③ 量具缺数 ④ 守卫失败。"""
    failures: list[str] = []

    def rows_by_label(rows):
        return {r[0]: r[3] for r in rows}

    # ★★ M15/M15x 的行标签（★ 本轮第 13 处口径修正后）：
    #   ★ free 切片的 M15 行**全部**是观察面（★ 实测四只豁免主导 ⇒ 结构性不可达）
    #   ★ M15x 行按 normal 样本量**数据驱动**分派 ⇒ ★ 合成数据里只有 deepseek 是判据行
    CANARY_OBS_LABELS = ([f"M15 金丝雀 {p}（★ 免费面·观察面）" for p in CANARIES]
                          + [f"M15x 金丝雀 {p}（★ 全计费·分层）" for p in CANARIES
                             if p != CANARY_JUDGEABLE])
    CANARY_JUDGE_LABEL = f"M15x 金丝雀 {CANARY_JUDGEABLE}（★ 全计费·分层）"

    # ① 全达成（★ M12s 两行是**参考项** ⇒ 期望 ⚠️，其余可判行期望 ✅）
    rows, bad, _ = judge(_synthetic(True))
    by = rows_by_label(rows)
    REFERENCE = ("M12s ns featured 复检间隔（med/p90/p99）",
                 "M12s ns 非featured 复检间隔（med/p90/p99）",
                 # ★★ M14/M14x 的新覆盖人群行现在是**观察面**（★ 混合口径含 78% 豁免类
                 #   ⇒ post 的 p90/slow 结构性不可达 ⇒ 效应判据改用 M20 分层）
                 "M14 新覆盖人群（★ 混合口径·观察面）",
                 "M14x 新覆盖人群（★ 混合口径·观察面）",
                 # ★★ M15/M15x 的观察面行（★ 本轮第 13 处口径修正）
                 ) + tuple(CANARY_OBS_LABELS)
    for k in by:
        if k in REFERENCE:
            if not by[k].startswith("⚠️"):
                failures.append(f"[① 全达成] {k} 应为参考项（⚠️）⇒ {by[k]}")
        elif not by[k].startswith("✅"):
            failures.append(f"[① 全达成] {k} ⇒ {by[k]}")
    if bad:
        failures.append(f"[① 全达成] 守卫误报：{bad}")

    # ② 全未达成
    rows, bad, _ = judge(_synthetic(False))
    by = rows_by_label(rows)
    for k in ("M2 featured static 规模", "M6 免费面 featured 命中", "M9 展开覆盖缺口",
              "M5 24h 探测量", "M3b namespaced backoff 档位",
              "M14 旧 featured 人群（无回归）",
              "M14 对照组（两个集合都没有）",
              "M14x 旧 featured 人群（无回归）",
              "M14x 对照组（两个集合都没有）",
              "M20 新覆盖组·非豁免（★ 效应主判据）",
              "M20 新覆盖组·豁免（★ 不回归守卫）",
              "M11 namespaced 恢复感知时延",
              "M12p ns 非featured 待复检率", CANARY_JUDGE_LABEL):
        if not by.get(k, "").startswith("❌"):
            failures.append(f"[② 全未达成] {k} ⇒ {by.get(k, '<缺行>')}")
    # ★★ 观察面行**不得**被误判成 ❌（★ 否则「混合口径不可达」会被读成「修复没生效」）
    for k in ("M14 新覆盖人群（★ 混合口径·观察面）",
              "M14x 新覆盖人群（★ 混合口径·观察面）") + tuple(CANARY_OBS_LABELS):
        if by.get(k, "").startswith("❌"):
            failures.append(f"[② 全未达成] {k} 是观察面，不该判 ❌ ⇒ {by[k]}")

    # ★ ②-b（2026-11-11 补）：★ **文案必须与裁决符号一致**（★ 真 bug 钉门）：
    #   ★ Stage 1 干跑实测：M5 超 9334 预算判 ❌ 时，文案恒写「爆炸半径未爆（9816 ≤ 9334）」
    #   ★ ⇒ 数字对比是假的 ⇒ 读者无法分辨「爆了还是没爆」。
    m5_bad = by.get("M5 24h 探测量", "")
    if m5_bad.startswith("❌"):
        if "未爆" in m5_bad:
            failures.append(f"[② 文案一致] M5 判 ❌ 但文案自称「未爆」⇒ {m5_bad}")
        if "超预算" not in m5_bad:
            failures.append(f"[② 文案一致] M5 判 ❌ 但文案未说明「超预算」⇒ {m5_bad}")

    # ③ 量具缺数（分位是 '?'）⇒ 必须「不可判」，**不能**是「未达成」
    d = _synthetic(True)
    d["M12s_settled_post_ns_feat"] = "394 events | med_min=? | p90=? | p99=?"
    d["M11b_post_fix_namespaced"] = "7 episodes | med=? | p90=? | max=?"
    d["M3b_backoff_ns_featured_err_http_429"] = "5 rows | median_retry_s=?"
    d["M14_post_newly_covered"] = "66 events | med_min=30 | p90_min=? | p99_min=600 | max_min=600 | slow_gt300_pct=2.0"
    d["M20_normal_post"] = ("21 events | med_gap=60 | p90_gap=? | slow_gt300_pct=?"
                            " | ladder_p50=? | top21600_n=? | ladder_max_s=?")
    rows, bad, _ = judge(d)
    by = rows_by_label(rows)
    for k in ("M12s ns featured 复检间隔（med/p90/p99）",
              "M11 namespaced 恢复感知时延",
              "M3b namespaced backoff 档位",
              "M20 新覆盖组·非豁免（★ 效应主判据）"):
        v = by.get(k, "<缺行>")
        if v.startswith("❌"):
            failures.append(f"[③ 量具缺数] {k} 被误判为未达成 ⇒ {v}")
        elif "不可判" not in v and "不可判" not in v:
            failures.append(f"[③ 量具缺数] {k} 未落到「不可判」⇒ {v}")

    # ④ 守卫失败 ⇒ 整表作废；且**守卫缺失**也要判失败
    rows, bad, _ = judge(_synthetic(True), inject_guard_fail=True)
    if not bad:
        failures.append("[④ 守卫失败] 注入失败后守卫未报警")
    for r in rows:
        if "作废" not in r[3]:
            failures.append(f"[④ 守卫失败] {r[0]} 未作废 ⇒ {r[3]}")
            break
    d = _synthetic(True)
    d.pop("M16_join_caliber_check")
    rows, bad, _ = judge(d)
    if not bad:
        failures.append("[④ 守卫缺失] 守卫指标缺失时未判失败（★ 会把没跑出来的量具当成通过）")

    # ⑤ ★★ stage-aware 基线：★ 同一份 post 读数，在 Stage 1（pre24 基线）与 Stage 2（pre7d 基线）
    #    下裁决**必须不同**（★ old_feat 的 pre24 p90=31 vs pre7d p90=120 ⇒ 阈值差 4×）
    d = _synthetic(True)
    d["M14_post_old_feat"] = "56 events | med_min=3 | p90_min=60 | p99_min=300 | max_min=300 | slow_gt300_pct=3.0"
    r1, _, _ = judge(d, stage=1)
    r2, _, _ = judge(d, stage=2)
    v1 = rows_by_label(r1).get("M14 旧 featured 人群（无回归）", "<缺行>")
    v2 = rows_by_label(r2).get("M14 旧 featured 人群（无回归）", "<缺行>")
    if not v1.startswith("❌"):
        failures.append(f"[⑤ stage 基线] Stage 1 应判未达成（pre24 p90=31，观测 60）⇒ {v1}")
    if not v2.startswith("✅"):
        failures.append(f"[⑤ stage 基线] Stage 2 应判达成（pre7d p90=120，观测 60）⇒ {v2}")

    # ⑥ ★★ **每个判据分量都要能被单独变异抓住**（★ 本轮实测：p99 的基线被改（360→999）
    #    而自测仍全绿 ⇒ ★ 因为失败用例只坏了 p90 ⇒ ★⇒ 补「只坏 p99」的场景）
    d = _synthetic(True)
    d["M14_post_old_feat"] = "56 events | med_min=3 | p90_min=20 | p99_min=900 | max_min=900 | slow_gt300_pct=3.0"
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get("M14 旧 featured 人群（无回归）", "<缺行>")
    if not v.startswith("❌"):
        failures.append(f"[⑥ p99 单项] 只坏 p99 时应判未达成 ⇒ {v}")

    # ⑦ ★★ 同理：慢事件占比（速率型读数）也要能单独被抓住
    #   ★★ 本轮改指 **M20 normal**（★ 效应判据已从 M14x 混合口径迁到 M20 分层）
    d = _synthetic(True)
    d["M20_normal_post"] = ("21 events | med_gap=60 | p90_gap=180 | slow_gt300_pct=80.0"
                            " | ladder_p50=30 | top21600_n=0 | ladder_max_s=10800")
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get("M20 新覆盖组·非豁免（★ 效应主判据）", "<缺行>")
    if not v.startswith("❌"):
        failures.append(f"[⑦ slow 单项] 只坏 slow 时应判未达成 ⇒ {v}")

    # ⑧ ★★★ **M18 守卫**：★ pre 基线冻结一致性（★ 本轮新增）
    #   ★★ 本轮真踩过：'pre7d' 实际只有 6 天（447 vs 真 461）⇒ ★ 若无此守卫，
    #   ★★ **基线显示（6d 的 10.3%）与判据阈值（7d 的 13.0% 折半）会静默脱钩** ⇒ 永远发现不了
    d = _synthetic(True)
    # ★ 键名用**采集侧**的键序（M14_<窗>_<组>）⇒ ★ 与真库产出同形
    d["M14_pre7d_newly_covered"] = ("447 events | med_min=2 | p90_min=360 | p99_min=1390"
                                     " | max_min=5523 | slow_gt300_pct=10.3")
    rows, bad, _ = judge(d)
    if not bad:
        failures.append("[⑧ M18 守卫] pre 基线漂移（447 ≠ 冻结值 461）时守卫未报警")
    for r in rows:
        if "作废" not in r[3]:
            failures.append(f"[⑧ M18 守卫] pre 基线漂移时未把裁决表作废 ⇒ {r[0]} {r[3]}")
            break

    # ⑧b ★★★ **守卫必须真的比对过一次**（★ 本轮真踩到：键序映射写错 ⇒ 守卫形同虚设 ⇒ 假绿）
    d = _synthetic(True)
    d["M14_pre7d_newly_covered"] = ("461 events | med_min=2 | p90_min=360 | p99_min=1390"
                                     " | max_min=5523 | slow_gt300_pct=13.0")
    rows, bad, guards = judge(d)
    g18 = dict(guards).get("M18 pre 基线冻结一致性", "")
    # ★★ 本轮把 M14/M14x/M20/**M15x** 的 pre 键都放进合成数据 ⇒ ★ 比对过的键数应 ≥ 26
    #   ★ 断言口径从「恰好 1 个」改成「**≥26 且无未比对标记**」⇒ ★ 键序映射写错会立刻掉到 0
    if "未产出 pre 基线键" in g18 or "个 pre 基线键" not in g18:
        failures.append(f"[⑧b M18 守卫] 守卫未真正比对过（键序映射可能错了）⇒ {g18}")
    else:
        import re as _re
        _m = _re.search(r"(\d+) 个 pre 基线键", g18)
        if not _m or int(_m.group(1)) < 26:
            failures.append(f"[⑧b M18 守卫] 比对过的 pre 基线键过少（应 ≥26）⇒ {g18}")

    # ⑨ ★★★ **两个计费口径都要出行**，且某口径样本不足时**必须点名另一口径**
    #   （★ 否则「样本不足」会被读成「修复无效」——★ 本轮反复强调的纪律）
    d = _synthetic(True)
    d["M14_post_newly_covered"] = ("14 events | med_min=360 | p90_min=360 | p99_min=360"
                                    " | max_min=360 | slow_gt300_pct=100.0")
    rows, _, _ = judge(d, stage=1)
    by = rows_by_label(rows)
    v = by.get("M14 新覆盖人群（★ 混合口径·观察面）", "<缺行>")
    if "M20" not in v:
        failures.append(f"[⑨ 双口径] free 口径样本不足时未点名效应判据（M20）⇒ {v}")
    if "M14x 新覆盖人群（★ 混合口径·观察面）" not in by:
        failures.append("[⑨ 双口径] 全计费口径的行缺失")
    if "M20 新覆盖组·非豁免（★ 效应主判据）" not in by:
        failures.append("[⑨ 双口径] ★ M20 效应判据行缺失（★ 混合口径不可达 ⇒ 效应判据只能靠它）")

    # ⑩ ★★★ **M19 守卫**：★ 口径的结构性不变量（★ **不需要知道冻结值**就能查）
    #   ★★ all ≥ free（样本空间包含关系）+ pre7d = pre6d + pre24（桶间加法）
    #   ★★ 换 era 切点、换数据量都应成立 ⇒ ★★ 比 M18 的冻结值更通用
    WINDOW_KEYS = {
        "M14_pre6d_newly_covered": 447, "M14_pre24_newly_covered": 14,
        "M14_pre7d_newly_covered": 461, "M14x_pre6d_newly_covered": 613,
        "M14x_pre24_newly_covered": 78, "M14x_pre7d_newly_covered": 691,
    }

    def _with_window_keys(dd, overrides=None):
        dd = dict(dd)
        for k, n in WINDOW_KEYS.items():
            dd[k] = f"{n} events | med_min=2 | p90_min=360 | p99_min=1390 | max_min=5523 | slow_gt300_pct=10.3"
        if overrides:
            dd.update(overrides)
        return dd

    rows, bad, guards = judge(_with_window_keys(_synthetic(True)))
    g19 = dict(guards).get("M19 口径结构不变量", "")
    if not g19.startswith("✔"):
        failures.append(f"[⑩ M19 守卫] 自洽的窗口键应通过守卫 ⇒ {g19}")
    if bad:
        failures.append(f"[⑩ M19 守卫] 自洽的窗口键不应触发守卫失败 ⇒ {bad}")

    rows, bad, guards = judge(_with_window_keys(
        _synthetic(True), {"M14x_pre7d_newly_covered": "10 events | med_min=2 | p90_min=360"}))
    g19 = dict(guards).get("M19 口径结构不变量", "")
    if "★★" not in g19:
        failures.append(f"[⑩ M19 守卫] all<free 时应报警 ⇒ {g19}")
    if not bad:
        failures.append("[⑩ M19 守卫] all<free 时守卫未判失败")

    # ⑪ ★★★ **M20 的机制指纹分量要能单独被抓住**（★ 纪律：每个判据分量都有独立场景）
    #   ★★ 第 18 处口径修正后：★ 指纹分量读的是 **fp 键**（★ 成熟键的 top21600 **不再**参与指纹判定）
    d = _synthetic(True)
    d["M20_normal_post_fp"] = ("n_all=21 | top21600_n=3 | top10800_n=1 | mul_evidence_n=10"
                               " | ladder_p50=180 | ladder_max_s=21600")
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get("M20 新覆盖组·非豁免（★ 效应主判据）", "<缺行>")
    if not v.startswith("❌"):
        failures.append(f"[⑪ top21600 单项] 只坏「未减半顶格」时应判未达成 ⇒ {v}")

    # ⑫ ★★★★ **混合口径未变 ≠ 修复没生效**（★★ 本轮第 11 处口径修正的**核心断言**）
    #   ★★ 场景：M14x 混合口径仍 p90=360（★ 豁免类占 78% ⇒ 结构性如此），
    #      而 M20 分层显示阶梯已减半 ⇒ ★★ 判定器**不得**出现任何 ❌
    #      （★ 否则「假阴性」会被读成「修复没生效」⇒ ★ 这正是本轮差点发生的事）
    d = _synthetic(True)
    d["M14x_post_newly_covered"] = ("78 events | med_min=360 | p90_min=360 | p99_min=360"
                                    " | max_min=360 | slow_gt300_pct=27.8")
    rows, _, _ = judge(d, stage=1)
    by = rows_by_label(rows)
    for lbl, val in by.items():
        if val.startswith("❌"):
            failures.append(f"[⑫ 混合口径假阴性] {lbl} 被判 ❌（★ 混合口径结构性不可达 ⇒ 不该判红）⇒ {val}")
    if not by.get("M20 新覆盖组·非豁免（★ 效应主判据）", "").startswith("✅"):
        failures.append("[⑫ 混合口径假阴性] M20 效应判据行未判达成 ⇒ "
                        + by.get("M20 新覆盖组·非豁免（★ 效应主判据）", "<缺行>"))
    if not by.get("M14x 新覆盖人群（★ 混合口径·观察面）", "").startswith("⚠️"):
        failures.append("[⑫ 混合口径假阴性] M14x 新覆盖行应落「观察面（⚠️）」⇒ "
                        + by.get("M14x 新覆盖人群（★ 混合口径·观察面）", "<缺行>"))

    # ⑬b ★★★ **era 纯度的存活者偏差必须被显式提示**（★ 主档 §5.9.21.13.8）
    #   ★★ pre 侧 `next_at < 切点` 会丢掉「间隔跨越切点」的事件 ⇒ ★★ 它们**必然**是慢事件
    #   ★★ ⇒ 若丢弃量 > 0 ⇒ ★★ pre 的慢事件占比被低估 ⇒ ★★ 阈值「post ≤ pre/2」偏严
    #   ★★ ⇒ ★★ 判负之前必须看到这条提示（★ 否则会误判成「修复没生效」）
    d = _synthetic(True)
    d["M20_era_dropped_pre24"] = "5 events dropped_by_era_purity"
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get("M20 新覆盖组·非豁免（★ 效应主判据）", "<缺行>")
    if "存活者偏差" not in v:
        failures.append(f"[⑬b 存活者偏差] pre 基线被 era 纯度丢弃时应显式提示 ⇒ {v}")

    # ⑬ ★★★ **M19 的「分层穷尽性」不变量**（★ normal + exempt == M14x newly）
    d = _synthetic(True)
    d["M20_exempt_pre7d"] = ("538 events | med_gap=3 | p90_gap=360 | slow_gt300_pct=32.2"
                              " | ladder_p50=0 | top21600_n=178 | ladder_max_s=30658")
    rows, bad, guards = judge(d)
    g19 = dict(guards).get("M19 口径结构不变量", "")
    if "分层不穷尽" not in g19:
        failures.append(f"[⑬ M19 分层穷尽性] 分层之和 ≠ 原集合时应报警 ⇒ {g19}")
    if not bad:
        failures.append("[⑬ M19 分层穷尽性] 分层不穷尽时守卫未判失败")

    # ⑭ ★★★★ **逐对金丝雀的「豁免主导 ⇒ 观察面」分派**（★ 本轮第 13 处口径修正）
    #   ★★ 场景：某只金丝雀的 pre 期 normal 样本不足 ⇒ ★★ 必须落「观察面（⚠️）」
    #      ⇒ ★ 绝不能判 ❌（★ 否则「结构性不可达」会被读成「修复没生效」）
    d = _synthetic(True)
    d["M15x_pre24__minimaxai/minimax-m3"] = (
        "9 events | exempt=9 | normal=0 | normal_med_min=? | normal_ladder_p50_s=?"
        " | normal_top3600_n=0 | normal_top7200_n=0 | normal_top21600_n=0"
        " | normal_ladder_max_s=?")
    d["M15x_post__minimaxai/minimax-m3"] = (
        "6 events | exempt=5 | normal=1 | normal_med_min=360 | normal_ladder_p50_s=21600"
        " | normal_top3600_n=0 | normal_top7200_n=0 | normal_top21600_n=1"
        " | normal_ladder_max_s=21600")
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get("M15x 金丝雀 minimaxai/minimax-m3（★ 全计费·分层）", "<缺行>")
    if v.startswith("❌"):
        failures.append(f"[⑭ 金丝雀分派] normal 样本不足时不该判 ❌ ⇒ {v}")
    elif not v.startswith("⚠️"):
        failures.append(f"[⑭ 金丝雀分派] normal 样本不足时应落「观察面（⚠️）」⇒ {v}")
    elif "结构性不可达" not in v or "样本不足" not in v:
        failures.append(f"[⑭ 金丝雀分派] 观察面行必须写明「结构性不可达 + 样本不足」⇒ {v}")

    # ⑮ ★★★★ **逐对判据的三个分量要能各自被单独变异抓住**（★ 纪律：每个分量都有独立场景）
    #   ★ 分量①：观察间隔中位（post med ≤ pre/2）
    d = _synthetic(True)
    d["M15x_post__" + CANARY_JUDGEABLE] = (
        "27 events | exempt=9 | normal=18 | normal_med_min=60"
        " | normal_ladder_p50_s=1800 | normal_top3600_n=0"
        " | normal_top7200_n=0 | normal_top21600_n=0 | normal_ladder_max_s=1800")
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get(CANARY_JUDGE_LABEL, "<缺行>")
    if not v.startswith("❌"):
        failures.append(f"[⑮a 金丝雀 med 分量] 只坏 med 时应判未达成 ⇒ {v}")
    #   ★ 分量②：阶梯中位（★ 机制原生）
    d = _synthetic(True)
    d["M15x_post__" + CANARY_JUDGEABLE] = (
        "27 events | exempt=9 | normal=18 | normal_med_min=30"
        " | normal_ladder_p50_s=3600 | normal_top3600_n=0"
        " | normal_top7200_n=0 | normal_top21600_n=0 | normal_ladder_max_s=3600")
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get(CANARY_JUDGE_LABEL, "<缺行>")
    if not v.startswith("❌"):
        failures.append(f"[⑮b 金丝雀阶梯分量] 只坏阶梯中位时应判未达成 ⇒ {v}")
    #   ★ 分量③：未减半档位清零（★ pre 期出现过的档位）
    d = _synthetic(True)
    d["M15x_post__" + CANARY_JUDGEABLE] = (
        "27 events | exempt=9 | normal=18 | normal_med_min=30"
        " | normal_ladder_p50_s=1800 | normal_top3600_n=2"
        " | normal_top7200_n=0 | normal_top21600_n=0 | normal_ladder_max_s=3600")
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get(CANARY_JUDGE_LABEL, "<缺行>")
    if not v.startswith("❌"):
        failures.append(f"[⑮c 金丝雀未减半档位] 只坏 3600 档清零时应判未达成 ⇒ {v}")

    # ⑯ ★★★★ **「恒真分量」必须被显式标注**（★ 恒真判据 = 门没牙）
    #   ★ Stage 1 的 deepseek pre24 基线里 **21600 档计数 = 0** ⇒ 该分量在 post 侧**恒真**
    #   ★ ⇒ 行内必须写明「★ 恒真 ⇒ 不作判据」（★ 否则读者会以为它在把关）
    d = _synthetic(True)
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get(CANARY_JUDGE_LABEL, "<缺行>")
    if "恒真" not in v:
        failures.append(f"[⑯ 恒真分量标注] pre 计数=0 的档位必须标注「恒真 ⇒ 不作判据」⇒ {v}")
    # ★ Stage 2（pre7d 基线）里 21600 档计数 = 10 ⇒ 该分量**有牙** ⇒ 不得再标恒真
    rows2, _, _ = judge(_synthetic(True), stage=2)
    v2 = rows_by_label(rows2).get(CANARY_JUDGE_LABEL, "<缺行>")
    if "21600s 档 pre 10 条" not in v2:
        failures.append(f"[⑯ 恒真分量标注] Stage 2 的 21600 档有牙 ⇒ 必须出现在「未减半档位清零」里 ⇒ {v2}")

    # ⑰ ★★★★ **M15x 的逐对分层穷尽性**（★ exempt + normal == total）
    d = _synthetic(True)
    d["M15x_pre7d__" + CANARY_JUDGEABLE] = (
        "77 events | exempt=36 | normal=40 | normal_med_min=60 | normal_ladder_p50_s=3600"
        " | normal_top3600_n=31 | normal_top7200_n=0 | normal_top21600_n=10"
        " | normal_ladder_max_s=21600")
    rows, bad, guards = judge(d)
    g19 = dict(guards).get("M19 口径结构不变量", "")
    if "逐对分层不穷尽" not in g19:
        failures.append(f"[⑰ M15x 逐对穷尽性] exempt+normal ≠ total 时应报警 ⇒ {g19}")
    if not bad:
        failures.append("[⑰ M15x 逐对穷尽性] 逐对分层不穷尽时守卫未判失败")

    # ⑱ ★★★★ **阈值必须由「干净 pre」推导**（★ 本轮第 14/16 处口径修正）
    #   ★★ 场景：pre 基线含 30.5% 的「乘数已生效」样本 ⇒ ★ 「pre/2」偏严
    #   ★★ ⇒ ★ 阈值 = clean_slow/clean_n/2（★ 与 post 段**同量纲**）⇒ ★ 行内必须披露污染率
    rows, _, _ = judge(_synthetic(True), stage=2)
    v = rows_by_label(rows).get("M20 新覆盖组·非豁免（★ 效应主判据）", "<缺行>")
    # ★ 合成数据里 clean_slow=19 / clean_n=113 ⇒ 16.8% ⇒ 阈值 8.4%（★ 而非原始 12.3%/2=6.15%）
    if "≤8.9%" not in v:
        failures.append(f"[⑱ 干净 pre 阈值] Stage 2 的 slow 阈值应按干净 pre 推导（8.9%）⇒ {v}")
    if "30.5" not in v or "乘数已生效" not in v:
        failures.append(f"[⑱ 污染披露] 行内必须披露 pre 基线的污染率 ⇒ {v}")
    # ★ 把 composition 改掉（clean_n 改成 154 ⇒ clean slow% = 12.3 ⇒ 阈值 6.15）⇒ ★ 阈值必须跟着变
    d = _synthetic(True)
    d["M20_normal_pre7d_composition"] = ("n=154 | mul_evidence=47 | clean_n=154 | clean_slow=19"
                                          " | slow=19 | contam_pct=30.5 | null_ladder_n=0"
                                          " | unexplained_n=0 | unexplained_values=-")
    rows, _, _ = judge(d, stage=2)
    v = rows_by_label(rows).get("M20 新覆盖组·非豁免（★ 效应主判据）", "<缺行>")
    if "≤6.2%" not in v:
        failures.append(f"[⑱ 阈值来源] 阈值必须**来自 composition 读数**（⇒ 随 clean_n 变化）⇒ {v}")

    # ⑲ ★★★★ **composition 缺失 ⇒ 回退 + 显式提示**（★ 不得静默用偏严阈值）
    d = _synthetic(True)
    d.pop("M20_normal_pre7d_composition")
    rows, _, _ = judge(d, stage=2)
    v = rows_by_label(rows).get("M20 新覆盖组·非豁免（★ 效应主判据）", "<缺行>")
    if "composition 缺失" not in v:
        failures.append(f"[⑲ composition 缺失] 必须显式提示「阈值可能偏严」⇒ {v}")

    # ⑳ ★★★★ **诚实边界的披露不许被静默删掉**（★ 本轮第 14/15 处口径修正 ⇒ 抽成函数才可测）
    #   ★★ 形态：★ ⑳（污染率 26.6% / newly ≠ pre 期未 featured）、⑳（M15x 逐对判据**未被负控证伪**）、
    #   ★★ ㉒（deepseek pre 基线是**短窗口快照**）——★ 这三条都是**结论的限定条件**，
    #   ★★ 若被静默删掉 ⇒ ★ 报告会读成「机制生效 + 逐对已判 + 样本充足」⇒ ★★ **假绿**。
    #   ★★ ⇒ 断言基于**实际打印出来的文本**（★ 不是源码字符串断言 ⇒ 不是 §654 说的那种假门）。
    import contextlib
    import io as _io
    _buf = _io.StringIO()
    with contextlib.redirect_stdout(_buf):
        print_honest_boundaries()
    _printed = _buf.getvalue()
    for _need, _why in (
        ("30.5", "⑳ 必须披露 pre 基线的污染率"),
        ("干净 pre", "⑳ 必须说明 slow 阈值改由「干净 pre」推导"),
        ("未被证伪", "㉑ 必须披露 M15x 逐对判据**未被负控证伪**"),
        ("短窗口快照", "㉒ 必须披露 deepseek 的 pre 基线是短窗口快照"),
        ("偏向判绿", "㉓ 必须披露「过滤诱导的尾部截断」的假阴性风险"),
        ("分视图", "㉔ 必须披露「不同分量的成熟度需求不同 ⇒ 分视图读取」"),
        ("机制连续性", "㉕ 必须披露「窗口中途的部署 ⇒ 已核查机制连续性」"),
    ):
        if _need not in _printed:
            failures.append(f"[⑳ 诚实边界披露] {_why}（打印文本里缺 {_need!r}）")

    # ★★ **「键未产出」必须与「没数据」区分开**（★ 本轮第 17 处口径修正带出的连带缺陷）
    #   ★★ 形态：★ 采集侧若把 post 段的**成功行**也滤掉 ⇒ ⇒ **该键根本不产出**
    #   ⇒ ⇒ 判定器只能显示「post 段无 episode」⇒ ⇒ ★★ **读起来像「样本不足」，实际是「采集口径把判据行静默掉了」**
    d = _synthetic(True)
    d.pop("M11b_post_fix_namespaced", None)
    rows, _, _ = judge(d, stage=2)
    v = rows_by_label(rows).get("M11 namespaced 恢复感知时延", "<缺行>")
    if "键未产出" not in v:
        failures.append(f"[⑳ 键未产出] M11 的「post 段无 episode」必须区分「键未产出」与「没数据」⇒ {v}")

    # ㉓ ★★★ **合成自测数据必须与「真捕获夹具」逐字同形**（★ 本轮真踩到）
    #   ★★ 形态：★ 合成数据里的 composition 曾写成 `"77 events | n=154 | ..."`
    #   ★★ ⇒ ★★ 那个前缀**真实 SQL 永远不产出**（且 77 本身是错的数）⇒ ★★ 自测验的是**虚构格式**
    #   ★★ ⇒ ★★ 「自测全绿」≠「解析器认得真数据」⇒ ★★ ⇒ 必须拿**真捕获**逐字对拍
    def _load_fixture(name: str) -> dict[str, str]:
        """★ 读真捕获夹具（★ 「k|v」逐行）"""
        out: dict[str, str] = {}
        p = Path(__file__).resolve().parent / "fixtures" / name
        for line in p.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "|" not in line:
                continue
            k, v = line.split("|", 1)
            out[k.strip()] = v.strip()
        return out

    try:
        fx = _load_fixture("p0-3-m20-composition-pre.txt")
    except Exception as _e:  # ★ 夹具缺失 ⇒ 门失去意义 ⇒ 直接判失败
        fx = {}
        failures.append(f"[㉓ 夹具] 真捕获夹具读取失败（★ 自测失去意义）⇒ {_e}")
    try:
        fx_fp = _load_fixture("p0-3-m20-fp-pre.txt")
    except Exception as _e:  # ★ 同上（★ 第 18 处口径修正新增的指纹夹具）
        fx_fp = {}
        failures.append(f"[㉓ 夹具] **指纹**真捕获夹具读取失败（★ 自测失去意义）⇒ {_e}")
    syn = _synthetic(True)
    for _k in ("M20_normal_pre24_composition", "M20_normal_pre7d_composition",
               "M20_exempt_pre24_composition", "M20_exempt_pre7d_composition"):
        if _k not in fx:
            failures.append(f"[㉓ 夹具] 夹具缺少 {_k} ⇒ 无法对拍")
        elif syn.get(_k) != fx[_k]:
            failures.append(f"[㉓ 夹具] 合成数据与**真捕获**不同形 ⇒ {_k}\n"
                            f"      合成: {syn.get(_k)}\n      真库: {fx[_k]}")
    # ★★ 第 18 处口径修正：★ 指纹键的合成值也必须与**真捕获**逐字同形（★ 不得手写）
    for _k in ("M20_normal_pre24_fp", "M20_normal_pre7d_fp",
               "M20_exempt_pre24_fp", "M20_exempt_pre7d_fp"):
        if _k not in fx_fp:
            failures.append(f"[㉓ 夹具] 指纹夹具缺少 {_k} ⇒ 无法对拍")
        elif syn.get(_k) != fx_fp[_k]:
            failures.append(f"[㉓ 夹具] 合成数据与**真捕获**不同形 ⇒ {_k}\n"
                            f"      合成: {syn.get(_k)}\n      真库: {fx_fp[_k]}")

    # ㉔ ★★★★ **阶梯证据集不完备 ⇒ 必须 fail-closed 提示**（★ 本轮第 16 处口径修正）
    #   ★★ 场景：composition 报 `unexplained_n > 0` ⇒ ★ 说明有样本的阶梯值**无法解释**
    #   ★★ ⇒ ★ 干净 pre 可能**仍偏严** ⇒ ★ 判负前必须复核 ⇒ ★ 行内不得静默
    d = _synthetic(True)
    d["M20_normal_pre7d_composition"] = ("n=154 | mul_evidence=47 | clean_n=107 | clean_slow=19"
                                          " | slow=19 | contam_pct=30.5 | null_ladder_n=0"
                                          " | unexplained_n=4 | unexplained_values=8888,9999")
    rows, _, _ = judge(d, stage=2)
    v = rows_by_label(rows).get("M20 新覆盖组·非豁免（★ 效应主判据）", "<缺行>")
    if "证据集不完备" not in v:
        failures.append(f"[㉔ 证据集不完备] unexplained_n>0 ⇒ 必须提示「阈值可能仍偏严」⇒ {v}")
    if "8888" not in v:
        failures.append(f"[㉔ 证据集不完备] 必须列出**无法解释的阶梯值** ⇒ {v}")

    # ㉕ ★★★★ **指纹分量必须读 fp 键**（★ 第 18 处口径修正的**核心断言**）
    #   ★★ 形态：★ 若判定器仍从**成熟键**读 top21600 ⇒ ★★ post 窗口的**尾部 6h 依旧失明**
    #   ★★ ⇒ ★★ 尾部若发生回归 ⇒ 指纹仍读 0 ⇒ ★★ **假绿**（★ 同第 17 处修正同族）
    #   ★★ ⇒ ★ 断言：★ 只坏**成熟键**的 top21600（fp 键仍好）⇒ ★ **不得**判 ❌
    d = _synthetic(True)
    d["M20_normal_post"] = ("21 events | med_gap=60 | p90_gap=180 | slow_gt300_pct=5.0"
                            " | ladder_p50=180 | top21600_n=3 | ladder_max_s=21600")
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get("M20 新覆盖组·非豁免（★ 效应主判据）", "<缺行>")
    if v.startswith("❌"):
        failures.append(f"[㉕ 指纹读 fp 键] 成熟键的 top21600 变坏**不该**影响指纹分量 ⇒ {v}")
    if "fp" not in v:
        failures.append(f"[㉕ 指纹读 fp 键] 行内必须写明读的是 **fp 视图** ⇒ {v}")

    # ㉖ ★★★★ **fp 键未产出 ⇒ fail-closed**（★ 不得静默判绿）
    d = _synthetic(True)
    d.pop("M20_normal_post_fp", None)
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get("M20 新覆盖组·非豁免（★ 效应主判据）", "<缺行>")
    if v.startswith("✅"):
        failures.append(f"[㉖ fp 键未产出] fp 键缺失时**不得**判绿 ⇒ {v}")
    if "未产出" not in v:
        failures.append(f"[㉖ fp 键未产出] 必须写明「键未产出」（★ 与「没数据」区分）⇒ {v}")

    # ㉗ ★★★★ **「0 条未减半顶格」在无正向证据时 ⇒ 不可判**（★ 不得读成「已生效」）
    #   ★★ 形态：★ n_all=12、top21600_n=0、mul_evidence_n=0 ⇒ ★ 既没看到未减半、也没看到减半独占值
    #   ★★ ⇒ ★ 样本里可能根本没有 6h 顶格类事件 ⇒ ★ **不可判**（★ 不判绿、不判红）
    d = _synthetic(True)
    d["M20_normal_post_fp"] = ("n_all=12 | top21600_n=0 | top10800_n=0 | mul_evidence_n=0"
                               " | ladder_p50=60 | ladder_max_s=3600")
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get("M20 新覆盖组·非豁免（★ 效应主判据）", "<缺行>")
    if v.startswith("✅"):
        failures.append(f"[㉗ 无正向证据] mul_evidence_n=0 ⇒ 不得判绿 ⇒ {v}")
    if "不得" not in v:
        failures.append(f"[㉗ 无正向证据] 必须写明「不得读成已生效」⇒ {v}")

    # ㉘ ★★★★ **量具正控**：★ pre 侧同口径读不到未减半顶格 ⇒ ★ 量具无鉴别力 ⇒ ★ 不得判绿
    #   ★★ 形态：★ post 的 top21600_n=0 在「量具坏掉」时也是 0 ⇒ ★ ⇒ 必须靠 pre 侧正控区分
    d = _synthetic(True)
    d["M20_normal_pre24_fp"] = ("n_all=29 | top21600_n=0 | top10800_n=0 | mul_evidence_n=0"
                                " | ladder_p50=60 | ladder_max_s=3600")
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get("M20 新覆盖组·非豁免（★ 效应主判据）", "<缺行>")
    if v.startswith("✅"):
        failures.append(f"[㉘ 量具正控] pre 侧无鉴别力时不得判绿 ⇒ {v}")
    if "量具无鉴别力" not in v:
        failures.append(f"[㉘ 量具正控] 必须提示「量具无鉴别力」⇒ {v}")

    # ㉙ ★★★★ **「fp 视图尾部可见性」守卫必须有牙**（★ 第 18 处口径修正的**核心守卫**）
    #   ★★ 场景：★ 把 fp 键的 n_all 改小到 < window_n + tail_n
    #   ★★ ⇒ ★ 等价于「fp 视图被改回带 6h 成熟度过滤」⇒ ★★ post 窗口的尾部 6h 再次失明
    #   ★★ ⇒ ★ 守卫必须报警 + **整表作废**（★ 否则尾部失明 ⇒ 假绿 ⇒ 没人会发现）
    d = _synthetic(True)
    d["M20_normal_post_fp"] = ("n_all=3 | top21600_n=0 | top10800_n=0 | mul_evidence_n=3"
                               " | ladder_p50=30 | ladder_max_s=10800")
    rows, bad, guards = judge(d, stage=1)
    g20 = dict(guards).get("M20 fp 视图尾部可见性", "")
    if "★★" not in g20:
        failures.append(f"[㉙ 尾部可见性守卫] fp 视图变窄时守卫未报警 ⇒ {g20}")
    if not bad:
        failures.append("[㉙ 尾部可见性守卫] fp 视图变窄时守卫未判失败")
    for r in rows:
        if "作废" not in r[3]:
            failures.append(f"[㉙ 尾部可见性守卫] fp 视图变窄时未把裁决表作废 ⇒ {r[0]} {r[3]}")
            break
    # ★★ **守卫必须真的比对过一次**（★ 纪律：★ 接上真数据后必须看到它真的比对了）
    _rows, _bad, _guards = judge(_synthetic(True))
    _g20 = dict(_guards).get("M20 fp 视图尾部可见性", "")
    if "组尾部可见性不变量" not in _g20:
        failures.append(f"[㉙ 尾部可见性守卫] 守卫未真正比对过（★ 接上真数据后必须看到它比对）⇒ {_g20}")

    # ㉚ ★★★★ **「摇摆」分量要能单独被抓住**（★ 第 20 处口径修正 ⇒ 修复前后的判别信号）
    #   ★★ 场景：同一模型同时出现减半独占值与未减半 6h 顶格 ⇒ ★ featured 状态不稳
    #   ★★ ⇒ ★ 指纹读 0 也**必须**判未达成（★ 否则「修复只是碰巧 featured」会被读成达成）
    d = _synthetic(True)
    d["M20_normal_post_fp_swing"] = ("swing_n=1 | swing_models=anthropic/claude-opus-4.8"
                                     " | models_n=2")
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get("M20 新覆盖组·非豁免（★ 效应主判据）", "<缺行>")
    if not v.startswith("❌"):
        failures.append(f"[㉚ 摇摆分量] 摇摆模型 >0 时应判未达成 ⇒ {v}")
    if "摇摆" not in v:
        failures.append(f"[㉚ 摇摆分量] 行内必须写明「摇摆」分量 ⇒ {v}")

    # ㉛ ★★★★ **摇摆键未产出 ⇒ fail-closed**（★ 不得静默判绿）
    d = _synthetic(True)
    d.pop("M20_normal_post_fp_swing", None)
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get("M20 新覆盖组·非豁免（★ 效应主判据）", "<缺行>")
    if v.startswith("✅"):
        failures.append(f"[㉛ 摇摆键未产出] 键缺失时**不得**判绿 ⇒ {v}")
    if "未产出" not in v:
        failures.append(f"[㉛ 摇摆键未产出] 必须写明「键未产出」⇒ {v}")

    # ㉜ ★★★★ **摇摆 0 ⇒ 行内必须给出正向结论**（★ 「featured 状态稳定」是本轮最想说的话）
    rows, _, _ = judge(_synthetic(True), stage=1)
    v = rows_by_label(rows).get("M20 新覆盖组·非豁免（★ 效应主判据）", "<缺行>")
    if "featured 状态稳定" not in v:
        failures.append(f"[㉜ 摇摆正向披露] 摇摆 0 时必须写明「featured 状态稳定 ⇒ 修复已真正落地」⇒ {v}")

    # ㉝ ★★★★ **「对照组」的相位/构成必须披露**（★ 第 21 处口径修正）
    #   ★ 「未达成」场景里 post 对照组的构成是 **零 6h 顶格事件**（★ 与 pre 不同构）
    #   ⇒ ★★ 行内必须写明「形态不同构 ⇒ 不可归因」
    #   ⇒ ★★ **但裁决符号仍是 ❌**（★ 只披露、不软化 ⇒ 门不钝化）
    rows, _, _ = judge(_synthetic(False), stage=1)
    by = rows_by_label(rows)
    for k in ("M14 对照组（两个集合都没有）", "M14x 对照组（两个集合都没有）"):
        v = by.get(k, "<缺行>")
        if "相位/构成" not in v:
            failures.append(f"[㉝ 相位披露] {k} 行内必须给出「相位/构成」⇒ {v}")
        if "不可归因" not in v:
            failures.append(f"[㉝ 相位披露] {k} 零 6h 顶格时必须写明「不可归因」⇒ {v}")
        if not v.startswith("❌"):
            failures.append(f"[㉝ 相位披露] {k} 的裁决符号**不得**被软化（★ ❌ 仍是 ❌）⇒ {v}")
    # ★★ 构成键**缺失** ⇒ 必须显式说「不可见」（★ 不得静默吞掉相位问题）
    d = _synthetic(False)
    d.pop("M14x_post_other_composition", None)
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get("M14x 对照组（两个集合都没有）", "<缺行>")
    if "相位/构成不可见" not in v:
        failures.append(f"[㉝ 相位披露] 构成键缺失时必须写明「相位/构成不可见」⇒ {v}")

    # ㉞ ★★★★ **诚实边界必须包含「相位」与「投影只投影时钟」两条**（★ 第 21 处口径修正）
    hb = "\n".join(honest_boundary_lines())
    if "相位" not in hb:
        failures.append("[㉞ 诚实边界] 缺少「相位效应」边界（★ 跨窗比中位数前先看形态是否同构）")
    if "只投影" not in hb:
        failures.append("[㉞ 诚实边界] 缺少「关窗预演只投影时钟、不投影数据积累」边界")

    # ㉟ ★★★★ **M5 判 ❌ 时必须给出「基线口径」披露**（★ 第 22 处口径修正）
    #   ★★ M5 的 base = **pre24 单日快照**（9242）⇒ ★ 而探测量**按天剧烈波动**
    #   ★★ ⇒ ★★ **pre 期实测 11-09 = 9685 已超 limit 9334** ⇒ ★★ 该阈值**在 pre 期就不成立**
    #   ★★ ⇒ ★★ M5 判 ❌ 时**不得**被读成「修复引起爆炸半径」⇒ ★★ 行内必须自曝基线口径
    #   ★★ **但裁决符号不得被软化**（★ ❌ 仍是 ❌ ⇒ ★ 门不钝化）
    rows, _, _ = judge(_synthetic(False), stage=1)
    v = rows_by_label(rows).get("M5 24h 探测量", "<缺行>")
    if "基线口径" not in v:
        failures.append(f"[㉟ M5 基线口径披露] M5 判 ❌ 时行内必须给出「基线口径」⇒ {v}")
    if "不可归因" not in v:
        failures.append(f"[㉟ M5 基线口径披露] 必须写明「不可归因」⇒ {v}")
    if "超预算" not in v:
        failures.append(f"[㉟ M5 基线口径披露] 必须写明「超预算」⇒ {v}")
    if not v.startswith("❌"):
        failures.append(f"[㉟ M5 基线口径披露] 裁决符号**不得**被软化（★ ❌ 仍是 ❌）⇒ {v}")
    # ★★ 逐日分布键**缺失** ⇒ 必须显式说「基线口径不可见」（★ 不得静默默认阈值成立）
    d = _synthetic(False)
    d.pop("M5_pre7d_runs_by_day", None)
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get("M5 24h 探测量", "<缺行>")
    if "基线口径不可见" not in v:
        failures.append(f"[㉟ M5 基线口径披露] 逐日分布键缺失时必须写明「基线口径不可见」⇒ {v}")
    if not v.startswith("❌"):
        failures.append(f"[㉟ M5 基线口径披露] 键缺失时裁决符号**不得**被软化 ⇒ {v}")

    # ㉠ ★★★★ **诚实边界必须包含「M5 的 ❌ 不可归因于修复」这条**（★ 第 22 处口径修正）
    hb = "\n".join(honest_boundary_lines())
    # ★★★ 第 31 处：★ **期望串从夹具取**（★ ★ 第 30 处的隔离层 ⇒ ★ 同源变异打不中本守卫）
    #   ★★ 形态（★ 本轮当场复现）：★ 若这里写死字面量 ⇒ ★★ 同源变异会把**判据与事实一起改掉** ⇒ ★★ 守卫致盲
    _m5fx_t = _load_m5_fixture()
    _mk_base = _m5fx_t.get("guard_marker_m5_baseline", "")
    _mk_half = _m5fx_t.get("guard_marker_half_truth", "")
    if not _mk_base or _mk_base not in hb:
        failures.append(f"[㉠ 诚实边界] 缺少「M5 的 ❌：★ 阈值口径无效 ⇒ 「爆炸半径失控」不成立」边界"
                        f"（★ 期望标记 = {_mk_base or '?（★ 夹具读不到 ⇒ 不可判）'}）")
    if not _mk_half or _mk_half not in hb:
        failures.append(f"[㉠ 诚实边界] 缺少「M5 的 ❌ 的**另一半**（★ 「阈值无效」不推出「增量与修复无关」）」"
                        f"（★ 期望标记 = {_mk_half or '?（★ 夹具读不到 ⇒ 不可判）'}）")

    # ㉣ ★★★★ **诚实边界必须包含「负控几何」这条**（★ 第 23 处口径修正）
    hb = "\n".join(honest_boundary_lines())
    if "负控" not in hb or "几何" not in hb:
        failures.append("[㉣ 诚实边界] 缺少「负控的窗口几何必须与被验收窗口一致」边界")
    if "假红" not in hb:
        failures.append("[㉣ 诚实边界] 缺少「原负控里旧 featured 判 ❌ 是几何错配造成的假红」提醒")

    # ㉤ ★★★★ **跨脚本守卫：★ 负控脚本必须真的做几何对齐**（★ 第 23 处口径修正）
    #   ★★ 负控脚本若被回退（★ 只平移切点、不平移 now()）⇒ ★★ 负控仍是「~35h 窗口」
    #   ⇒ ★★ **与验收几何不一致** ⇒ ★★ 负控的绿不能代表验收窗口 ⇒ ★★ 门没牙
    #   ⇒ ★★ ⇒ 断言它**真的**含「now() 随切点平移」的实现（★ 跨文件断言 ⇒ 守得住）
    try:
        negsrc = Path(__file__).with_name("recovery-detection-p0-3-negcontrol.py").read_text(encoding="utf-8")
    except Exception as e:  # ★ 读不到 ⇒ 必须判失败（★ 不得静默跳过）
        negsrc = ""
        failures.append(f"[㉤ 负控几何守卫] 读不到负控脚本 ⇒ {e}")
    if negsrc:
        if "build_shifted_sql" not in negsrc:
            failures.append("[㉤ 负控几何守卫] 负控脚本里没有 `build_shifted_sql`（★ 几何对齐的实现）")
        if 'replace("now()"' not in negsrc:
            failures.append("[㉤ 负控几何守卫] 负控脚本没有把 `now()` 平移（★ 负控仍是错几何 ⇒ 门没牙）")
        if "asof" not in negsrc:
            failures.append("[㉤ 负控几何守卫] 负控脚本没有 as-of 概念（★ post 窗口长度未对齐验收窗口）")

    # ㉥ ★★★★ **诚实边界必须包含「M11 的基线不同量纲」这条**（★ 第 23 处口径修正的伴生边界）
    #   ★★ 为什么必须进边界：★ M11 是「最直接回答及时性」的头号判据（★ 锚点档 T1 列在 Stage 1 判据集里）
    #   ★★ 而它的基线是 7 天的 ⇒ ★★ 若不披露 ⇒ ★★ Stage 1 报告会把它读成独立判据 ⇒ ★★ 符号两个方向都可能错
    hb = "\n".join(honest_boundary_lines())
    if "M11" not in hb or "量纲" not in hb:
        failures.append("[㉥ 诚实边界] 缺少「M11 的基线取自 7 天 pre、Stage 1 的 post 是 24h ⇒ 不同量纲」边界")
    if "两个方向都可能错" not in hb:
        failures.append("[㉥ 诚实边界] 缺少「M11 符号两个方向都可能错」的提醒")

    # ㉧ ★★★★ **诚实边界必须包含「post 段必须被当前时刻界定」这条**（★ 第 24 处口径修正）
    #   ★★ 为什么必须进边界：★ M11 是**头号判据**（★ 锚点档 T1 列进 Stage 1 判据集）
    #   ★★ 而它的 post 段曾**无界** ⇒ ★★ 负控里它**恒判 ✅** ⇒ ★★ 它在负控里**没有牙**
    #   ★★ ⇒ 不披露 ⇒ ★★ 报告会把它读成「已证伪的判据」⇒ ★★ 符号的含义被高估
    #   ★★ ⇒ 断言基于**打印文本**（★ 唯一来源 = honest_boundary_lines()）
    if "无界" not in hb:
        failures.append("[㉧ 诚实边界] 缺少「M11 的 post 段曾**无界** ⇒ as-of 替换对它无效」这条边界")
    if "恒真" not in hb:
        failures.append("[㉧ 诚实边界] 缺少「M11 在负控里**恒真** ⇒ 没有牙」这条边界")
    if "post 段必须被" not in hb:
        failures.append("[㉧ 诚实边界] 缺少「post 段必须被当前时刻界定」的通用纪律")

    # ㉪ ★★★★ **诚实边界必须包含「第二次部署：★ diff 碰了 featured 路径 ⇒ ★ 但代码解耦 + 等长窗口实测 ⇒ era 切点仍有效」**（★ 观察层事件）
    #   ★★ 为什么必须进边界：★ 观测窗口**再次**跨构建（11-11 01:07:37），且这次 diff **真的碰了** featured 路径
    #   ★★ ⇒ ★★ 「机制连续」的结论**不是**靠「diff 不碰被测路径」⇒ ★★ 必须把**替代证据**写出来（★ 代码解耦 + 等长窗口实测）
    #   ★★ ⇒ ★★ 不披露 ⇒ ★★ 报告会以为「窗口内只换过 19:01 一次构建」⇒ ★★ 且读不到「碰了但可证解耦」这个处置方式
    if "01:07:37" not in hb:
        failures.append("[㉪ 诚实边界] 缺少「观测窗口的第二次部署（2026-11-11 01:07:37）」这条观察层边界")
    if "解耦" not in hb:
        failures.append("[㉪ 诚实边界] 缺少「diff 碰了 ≠ 机制变了 ⇒ ★ 可证代码解耦」的处置说明")
    if "等长窗口" not in hb:
        failures.append("[㉪ 诚实边界] 缺少「等长窗口实测」这条可复核证据的说明")

    # ㉬ ★★★★ **诚实边界 ㉫ 必须存在 ＋ ★★ 穷举审计必须真的通过（★ 跨脚本**行为**守卫）**（★ 第 25 处）
    #   ★★ 形态：★ 第 24 处只修了 M11（★ **抽查**）⇒ ★★ 穷举后**又找出 8 处同类无界窗口**
    #   ★★ ⇒ ★★ 「同类缺陷会不会复发」不能靠人记得 ⇒ ★★ 必须**有工具 + 有守卫**
    #   ★★ ⇒ ★★ 守卫必须是**行为级**（★ 真的 import 审计器并跑它）⇒ ★★ 而不只是「文件存在」
    if "穷举" not in hb or "14221" not in hb:
        failures.append("[㉬ 诚实边界] 缺少「同类缺陷必须穷举」边界（含 ★ M5 无上界 14221 vs 有上界 9245 的实测）")
    if "9245" not in hb or "假红" not in hb:
        failures.append("[㉬ 诚实边界] 缺少「M5 修好窗口后在负控里判 ✅（9245 ≤ 9334）⇒ ★ 它不是必须判负的判据；"
                        "★ 窗口没修好时的 14221 ⇒ ❌ 是纯假红」这条结论")
    try:
        import importlib.util as _ilu2
        _auditp = Path(__file__).with_name("recovery-detection-p0-3-post-bound-audit.py")
        _spec2 = _ilu2.spec_from_file_location("p0_3_post_bound_audit", _auditp)
        _am = _ilu2.module_from_spec(_spec2)
        _spec2.loader.exec_module(_am)
        _rows, _viol = _am.audit(_am.SQL_PATH.read_text(encoding="utf-8"))
        if _viol:
            failures.append(f"[㉬ 穷举审计守卫] 采集 SQL 里存在「post/窗口型读数没有 now() 上界」⇒ "
                            f"{[v['prefix'] for v in _viol]}")
        if not _rows:
            failures.append("[㉬ 穷举审计守卫] 审计器一个读数块都没解析出来（★ 口径失效 ⇒ 守卫是假门）")
    except Exception as e:  # ★ 读不到/跑不动 ⇒ 必须判失败（★ 不得静默跳过）
        failures.append(f"[㉬ 穷举审计守卫] 读不到/跑不动穷举审计器 ⇒ {e}")

    # ═══════════════════════════════════════════════════════════════════════
    # ㉳ ★★★★ **第 27 处：★ 「审计器自己也要被审计」⇒ ★★ 覆盖率守卫 + 边界 ㉲ 必须存在**
    #   ★★ 形态（★ 本轮真踩到）：★ 第 25 处的审计器 v1 **看不见 M14/M14x 主判据块**
    #   ★★   （★ 键是 CASE 表达式 ⇒ v1 的键提取抓不到 ⇒ ★★ 覆盖率 0%）
    #   ★★   ⇒ ★★ v1 报「全部合规」时，★ **没人知道它没看过主判据** ⇒ ★★ 静默失效
    #   ★★★ ⇒ ★★★ 守卫必须做两件事：★ ① **断言覆盖到的块数与关键块可见**
    #   ★★★   ② ★ **断言新边界 ㉲ 存在**（★ 归档档 ≠ 报告里能看见 ⇒ 等于没发现）
    # ═══════════════════════════════════════════════════════════════════════
    if "㉲" not in hb:
        failures.append("[㉳ 诚实边界] 缺少「审计器自己也要被审计 ⇒ 守卫必须报告覆盖率 + 窗口必须逐个判定」边界（★ 第 27 处）")
    if "覆盖率" not in hb or "post_fix" not in hb:
        failures.append("[㉳ 诚实边界] 缺少第 27 处的两个关键事实：★ 覆盖率（M14/M14x 曾为 0%）与 ★ era 词表漏 `post_fix`")
    try:
        import importlib.util as _ilu3
        _auditp3 = Path(__file__).with_name("recovery-detection-p0-3-post-bound-audit.py")
        _spec3 = _ilu3.spec_from_file_location("p0_3_audit_cov", _auditp3)
        _am3 = _ilu3.module_from_spec(_spec3)
        _spec3.loader.exec_module(_am3)
        _rows3, _viol3 = _am3.audit(_am3.SQL_PATH.read_text(encoding="utf-8"))
        _cov3 = _am3.coverage(_rows3)
        # ★★ ① 覆盖率守卫：★ 关键块必须可见（★ 否则「审计器自己瞎了」）
        if _cov3["missing_required"]:
            failures.append(f"[㉳ 覆盖率守卫] 审计器看不见这些关键块 ⇒ ★★ 它对它们的「合规」结论是**空的** ⇒ {_cov3['missing_required']}")
        # ★★ ② 覆盖到的块数下界（★ v1 只解析 37 块且主判据不可见 ⇒ 现在 38 块全可见）
        if _cov3["n_blocks"] < 38:
            failures.append(f"[㉳ 覆盖率守卫] 审计器解析到的读数块数 { _cov3['n_blocks'] } < 38 ⇒ ★★ 覆盖面回退 ⇒ ★★ 可能是键提取又退化了")
        # ★★ ③ 行为守卫：★ 主判据块必须**在**被检查的列表里（★ v1 时它不在）
        _pfx3 = [r["prefix"] for r in _rows3]
        if not any(p.startswith("M14x_") for p in _pfx3):
            failures.append("[㉳ 覆盖率守卫] **M14/M14x 主判据块不在审计器的可见列表里** ⇒ ★★ v1 的老问题又回来了")
    except Exception as e:  # ★ 读不到/跑不动 ⇒ 必须判失败（★ 不得静默跳过）
        failures.append(f"[㉳ 覆盖率守卫] 读不到/跑不动穷举审计器 ⇒ {e}")

    # ㉨ ★★★★ **跨脚本**行为**守卫：★ 负控必须真的要求「M11 namespaced 判负」（★ 第 24 处口径修正）
    #   ★★ 形态：★ 若负控脚本里这条要求被回退（★ 或被改成两个 stage 共用）
    #   ⇒ ★★ Stage 1 的 M11 又变成「恒真 ⇒ 没有牙」的判据 ⇒ ★★ 门没牙 ⇒ ★★ 且**静默**
    #   ★★ ⇒ ★★ 断言必须是**行为级**（★ 真的 import 负控脚本并调用它的 stage 门禁）
    #   ★★   ⇒ 而不是「源码里有没有某个字符串」（★ 字符串断言守不住「字符串在但没接上」）
    try:
        import importlib.util as _ilu
        _negp = Path(__file__).with_name("recovery-detection-p0-3-negcontrol.py")
        _spec = _ilu.spec_from_file_location("negctl_guard_m24", _negp)
        _nc = _ilu.module_from_spec(_spec)
        _spec.loader.exec_module(_nc)
        _l1 = tuple(_nc.must_fail_labels_for_stage(1))
        _l2 = tuple(_nc.must_fail_labels_for_stage(2))
    except Exception as e:  # ★ 读不到 ⇒ 必须判失败（★ 不得静默跳过）
        _l1, _l2 = (), ()
        failures.append(f"[㉨ 负控 Stage 门禁守卫] 读不到/跑不动负控脚本 ⇒ {e}")
    # ★★ 注意：★ 这里**不能**写 `if _l1 and ...`（★ 本轮变异真踩到）
    #   ★★ 若清单被清空（★ 要求整个被删掉）⇒ ★★ `_l1` 为空 ⇒ ★★ `if _l1` 为假 ⇒ ★★ **守卫不报警**
    #   ★★ ⇒ ★★ **空清单本身就是「要求被删掉」的证据** ⇒ ★★ 必须判失败
    if not _l1 or not any("M11 namespaced" in x for x in _l1):
        failures.append(f"[㉨ 负控 Stage 门禁守卫] Stage 1 的必须判负清单**必须非空且含「M11 namespaced 恢复感知时延」**"
                        f"（★ 空清单 = 要求被删掉了 ⇒ M11 又变成恒真判据）⇒ {_l1}")
    if _l2:
        failures.append(f"[㉨ 负控 Stage 门禁守卫] Stage 2 **不得**要求判负（★ 168h 窗口不含那段退化"
                        f" ⇒ 实测 med=3 ⇒ ✅ 达成 ⇒ 否则是**假红**）⇒ {_l2}")
    # ㉡ ★★★★ **M5 判 ❌ 时必须给出「增量归因（by_kind）」分解**（★ 2026-11-11 关窗预演补；
    #   ★★ 与第 22 处「基线口径」互补：★ 那边回答「阈值是否成立」；这边回答「增量从哪来」）
    rows, _, _ = judge(_synthetic(False), stage=1)
    v = rows_by_label(rows).get("M5 24h 探测量", "<缺行>")
    if "增量归因（by_kind）" not in v:
        failures.append(f"[㉡ M5 增量归因] M5 判 ❌ 时行内必须给出「增量归因（by_kind）」分解 ⇒ {v}")
    if "sync_request=" not in v:
        failures.append(f"[㉡ M5 增量归因] 分解里必须能看到逐 kind 计数（如 sync_request=）⇒ {v}")
    if not v.startswith("❌"):
        failures.append(f"[㉡ M5 增量归因] 裁决符号**不得**被软化（★ ❌ 仍是 ❌）⇒ {v}")
    # ★★ by_kind 键**缺失** ⇒ 必须显式说「增量归因不可见」（★ 不得静默只留基线口径披露）
    d = _synthetic(False)
    d.pop("M5_probe_runs_24h_by_kind", None)
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get("M5 24h 探测量", "<缺行>")
    if "增量归因不可见" not in v:
        failures.append(f"[㉡ M5 增量归因] by_kind 键缺失时必须写明「增量归因不可见」⇒ {v}")
    if not v.startswith("❌"):
        failures.append(f"[㉡ M5 增量归因] 键缺失时裁决符号**不得**被软化 ⇒ {v}")

    # ㉮ ★★★★ **诚实边界 ㉭ 必须存在**（★ 第 26 处口径修正：★ 「::int 取整边界」伪影）
    #   ★★ 为什么必须进边界：★ 这是**判据分辨率**层面的缺陷（★ 分辨率 1 分钟 vs 抖动秒级）
    #   ★★ ⇒ ★★ 关窗报告若不写它 ⇒ ★★ 一个由「≤60 秒」真实差决定的 ❌ 会被读成「修复失败」
    if "取整" not in hb or "360.5069" not in hb:
        failures.append("[㉮ 诚实边界] 缺少「分钟型量被 ::int 截到分钟后与阈值做精确比较 ⇒ 亚分钟差被放大成 1 单位假红」"
                        "边界（含 ★ 实测 raw p90 = 360.5069）")
    if "分辨率" not in hb:
        failures.append("[㉮ 诚实边界] 缺少「判据的分辨率必须 ≥ 被测机制的抖动幅度」这条纪律")

    # ㉯ ★★★★ **行为守卫**：★ 「取整边界伪影」的行内披露必须真的出现，且**不得软化裁决符号**（★ 第 26 处）
    #   ★★ 形态：★ 若披露被回退 ⇒ ★★ 判 ❌ 的行里只剩「豁免类被拖慢」⇒ ★★ 读者必然误读成行为回归
    #   ★★ ⇒ ★★ 断言必须是**行为级**（★ 真的构造一个「取整跨界」的数据再跑 judge）
    _d26 = _synthetic(True)
    _d26["M20_exempt_post"] = ("57 events | med_gap=360 | p90_gap=361 | slow_gt300_pct=87.7"
                                " | ladder_p50=360 | top21600_n=54 | ladder_max_s=21600"
                                " | med_gap_raw=360.0753 | p90_gap_raw=360.5069 | max_gap_raw=360.5274")
    _rows26, _, _ = judge(_d26, stage=1)
    _v26 = rows_by_label(_rows26).get("M20 新覆盖组·豁免（★ 不回归守卫）", "<缺行>")
    if "取整边界伪影" not in _v26:
        failures.append(f"[㉯ 取整边界披露] M20 豁免行判 ❌（p90_gap 361 > 360）时必须行内披露"
                        f"「取整边界伪影」⇒ {_v26}")
    if "360.5069" not in _v26:
        failures.append(f"[㉯ 取整边界披露] 披露里必须给出**实测 raw 值**（★ 360.5069）⇒ {_v26}")
    if not _v26.startswith("❌"):
        failures.append(f"[㉯ 取整边界披露] 裁决符号**不得**被软化（★ ❌ 仍是 ❌ ⇒ 门不钝化）⇒ {_v26}")
    # ★★ raw 键缺失 ⇒ 必须显式说「不可见」（★ 不得静默按面值读）
    _d26b = _synthetic(True)
    _d26b["M20_exempt_post"] = ("57 events | med_gap=360 | p90_gap=361 | slow_gt300_pct=87.7"
                                " | ladder_p50=360 | top21600_n=54 | ladder_max_s=21600")
    _rows26b, _, _ = judge(_d26b, stage=1)
    _v26b = rows_by_label(_rows26b).get("M20 新覆盖组·豁免（★ 不回归守卫）", "<缺行>")
    if "取整边界不可见" not in _v26b:
        failures.append(f"[㉯ 取整边界披露] raw 读数缺失时必须显式写「取整边界不可见」⇒ {_v26b}")
    if not _v26b.startswith("❌"):
        failures.append(f"[㉯ 取整边界披露] raw 缺失时裁决符号**不得**被软化 ⇒ {_v26b}")
    # ★★ ★★★ **反向守卫**：★ 真的差很多（p90_gap=720）⇒ **不得**出现「取整边界伪影」
    #   ★★   ⇒ ★★ 这是第 25 处纪律的「假阳性也要治」同款（★ 守卫的判据必须是结构，不是关键词）
    _d26c = _synthetic(True)
    _d26c["M20_exempt_post"] = ("57 events | med_gap=360 | p90_gap=720 | slow_gt300_pct=87.7"
                                " | ladder_p50=360 | top21600_n=54 | ladder_max_s=21600"
                                " | p90_gap_raw=720.0")
    _rows26c, _, _ = judge(_d26c, stage=1)
    _v26c = rows_by_label(_rows26c).get("M20 新覆盖组·豁免（★ 不回归守卫）", "<缺行>")
    if "取整边界伪影" in _v26c:
        failures.append(f"[㉯ 取整边界披露] 真实差很大（p90_gap 720）时**不得**出现「取整边界伪影」"
                        f"（★ 守卫必须是结构判据，不是关键词匹配）⇒ {_v26c}")

    # ㉱ ★★★★ **诚实边界 ㉰ 必须存在 ＋ ★★ 「小样本分位数不稳」的披露必须真的出现**（★ 第 26 处附带发现）
    #   ★★ 形态：★ M14x 旧 featured 行在窗口未满时判 ❌（★ post n=34 vs pre n=226）⇒ ★★ 极脆
    #   ★★ ⇒ ★★ 若披露缺失 ⇒ ★★ 读者会把「秩统计量跳档」读成「旧 featured 人群真的退化了」
    if "㉰" not in hb or "秩统计量" not in hb:
        failures.append("[㉱ 诚实边界] 缺少「分位数是秩统计量 ⇒ 样本量小的时候极不稳定」这条边界")
    if "关窗读数" not in hb:
        failures.append("[㉱ 诚实边界] 缺少「窗口未满时的读数 ≠ 关窗读数」这条纪律")
    # ★★ **行为守卫**：★ 真的构造一个「post n 远小于 pre n」的 M14x 旧 featured ⇒ 行内必须披露
    _d26d = _synthetic(True)
    # ★★ n 必须 **≥30**（★ 低于 30 走的是「样本不足」分支 ⇒ 根本到不了不回归判定 ★ 本轮自测真踩到）
    # ★★ 且必须**同时**改 free 口径那侧（★ M19 不变量「all ≥ free」⇒ ★ 只改 all 会触发守卫 ⇒ 整表作废）
    _d26d["M14_post_old_feat"] = ("20 events | med_min=30 | p90_min=360 | p99_min=360 | max_min=360"
                                  " | slow_gt300_pct=20.0 | p90_min_raw=360.0352 | p99_min_raw=360.1988")
    _d26d["M14x_post_old_feat"] = ("34 events | med_min=30 | p90_min=360 | p99_min=360 | max_min=360"
                                    " | slow_gt300_pct=17.6 | p90_min_raw=360.0352 | p99_min_raw=360.1988")
    _rows26d, _, _ = judge(_d26d, stage=1)
    _v26d = rows_by_label(_rows26d).get("M14x 旧 featured 人群（无回归）", "<缺行>")
    if "小样本分位数不稳" not in _v26d:
        failures.append(f"[㉱ 小样本披露] post n 远小于 pre n 时，M14x 旧 featured 判 ❌ 必须行内披露"
                        f"「小样本分位数不稳」⇒ {_v26d}")
    if not _v26d.startswith("❌"):
        failures.append(f"[㉱ 小样本披露] 裁决符号**不得**被软化（★ ❌ 仍是 ❌）⇒ {_v26d}")
    # ★★ **反向守卫**：★ post n 接近 pre n ⇒ **不得**出现该披露（★ 假阳性也要治）
    _d26e = _synthetic(True)
    _d26e["M14_post_old_feat"] = ("180 events | med_min=30 | p90_min=360 | p99_min=360 | max_min=360"
                                  " | slow_gt300_pct=22.2 | p90_min_raw=360.0352 | p99_min_raw=360.1988")
    _d26e["M14x_post_old_feat"] = ("200 events | med_min=30 | p90_min=360 | p99_min=360 | max_min=360"
                                    " | slow_gt300_pct=22.2 | p90_min_raw=360.0352 | p99_min_raw=360.1988")
    _rows26e, _, _ = judge(_d26e, stage=1)
    _v26e = rows_by_label(_rows26e).get("M14x 旧 featured 人群（无回归）", "<缺行>")
    if "小样本分位数不稳" in _v26e:
        failures.append(f"[㉱ 小样本披露] post n 与 pre n 相当（200 vs 226）时**不得**出现「小样本分位数不稳」"
                        f"（★ 守卫必须是结构判据，不是关键词匹配）⇒ {_v26e}")

    # ㉴ ★★★★ **第 13 处纪律的最后一个候选点**：★ 「同一行里两个 n 来自不同窗口」的披露必须出现
    #   ★★ 形态：★ M20 的**豁免行**也引用了 fp 视图（★ 对照读数：未减半 6h 顶格）⇒ ★★ 一行里出现两个 n
    #   ★★   ⇒ ★★ 读者会自然地拿它们相比 ⇒ ★★ 读成「fp 多出的 X 条 ⇒ 尾部有回归」⇒ ★★ **误读**
    #   ★★ ⇒ ★★ 行内必须显式标明两个 n 各自的窗口
    #   ★★ ⇒ ★★ 断言必须是**行为级**（★ 真的构造两个视图 n 不同的数据再跑 judge）
    if "㉔" not in hb or "两个 n" not in hb:
        failures.append("[㉴ 诚实边界] 缺少「同一行里出现两个 n 时必须标明各自窗口」这条边界（★ 第 13 处纪律）")
    if "豁免" not in hb or "fp 视图" not in hb:
        failures.append("[㉴ 诚实边界] 缺少「豁免行同样引用 fp 视图 ⇒ 两个 n 同源不同窗」这个关键事实")
    # ★★ **行为守卫**：★ 合成数据里成熟视图 n=57、fp 视图 n_all=4 ⇒ ★ 必须行内披露
    _d27 = _synthetic(True)
    _rows27, _, _ = judge(_d27, stage=1)
    _v27 = rows_by_label(_rows27).get("M20 新覆盖组·豁免（★ 不回归守卫）", "<缺行>")
    if "两个 n 不同窗口" not in _v27:
        failures.append(f"[㉴ 跨窗口披露] M20 豁免行同时引用两个视图时必须行内披露"
                        f"「两个 n 不同窗口」（★ 第 13 处纪律）⇒ {_v27}")
    if "n=57" not in _v27 or "n_all=4" not in _v27:
        failures.append(f"[㉴ 跨窗口披露] 披露里必须**同时给出两个 n 的数字**（★ 成熟 57 / fp 4）⇒ {_v27}")
    # ★★ **反向守卫**：★ 两个 n 相等 ⇒ **不得**出现该披露（★ 不得制造噪声）
    _d27b = _synthetic(True)
    _d27b["M20_exempt_post_fp"] = ("n_all=57 | top21600_n=4 | top10800_n=0 | mul_evidence_n=0"
                                   " | ladder_p50=360 | ladder_max_s=21600")
    _rows27b, _, _ = judge(_d27b, stage=1)
    _v27b = rows_by_label(_rows27b).get("M20 新覆盖组·豁免（★ 不回归守卫）", "<缺行>")
    if "两个 n 不同窗口" in _v27b:
        failures.append(f"[㉴ 跨窗口披露] 两个视图的 n 相等（57 vs 57）时**不得**出现「两个 n 不同窗口」"
                        f"（★ 守卫必须是结构判据，不是关键词匹配）⇒ {_v27b}")

    # ㉵ ★★★★ **第三次跨构建（★ 11-11 02:06:59）**：★ 四条关键事实 + 1 分钟缺口披露 + 两条新纪律
    #   ★★ 形态：★ post 窗口**第三次**跨构建 ⇒ ★★ 「机制连续」的结论必须落在**可复核证据**上
    #   ★★   ⇒ ★★ 这四条事实（★ 事件链 / ★ 代码腿 / ★ 数据腿 / ★ 形态腿）必须**留在打印文本里**
    #   ★★   ⇒ ★★ 否则「era 切点为何不重划」这句话**后人无从复核**（★ 同 §671 的教训）
    #   ★★ ⇒ ★★ 且 ★ **「探针未中断」不能写成「零中断」**（★ 部署瞬间有 1 分钟停探）⇒ ★★ 必须带量化
    #   ★★ ⇒ ★★ 本块的守卫是**文本级**的（★ 这四条是**观测事件**的事实、不是可由 judge() 计算的行为）
    #   ★★   ⇒ ★★ 但仍必须有：★ **退回「两次」版 ⇒ 必须报警**（★ 这正是本轮要防的回归）
    #   ★★ ⇒ ★★ ★★ **★★ 期望值必须「分文件」（★ 本轮真踩到 ⇒ LESSONS §679）**：
    #   ★★   形态：★ 守卫的**判据字面量**与**被守卫的事实**住在**同一个文件**里
    #   ★★     ⇒ ★★ 变异「事实」时会**把判据一起改掉** ⇒ ★★ 守卫被**同源变异**致盲
    #   ★★     ⇒ ★★ 「变异打不中」会被误读成「守卫没牙」⇒ ★★ 真因是「守卫自己被改瞎了」
    #   ★★   ⇒ ★★ 修法：★ 期望值登记到**独立夹具**（★ fixtures/p0-3-deploy-events.txt）
    #   ★★     ⇒ ★★ 只改 verdict.py 的边界文本 ⇒ ★★ 判据不动 ⇒ ★★ 守卫照样红
    #   ★★ ⇒ ★★ ★★ **fail-closed**：★ 登记表读不到 ⇒ ★★ **不可判**（★ 不得静默跳过）
    try:
        _facts = _load_expected_facts("p0-3-deploy-events.txt")
    except Exception as _e:
        _facts = {}
        failures.append(f"[㉵ 期望值登记表] 读不到 `fixtures/p0-3-deploy-events.txt` ⇒ ★★ 守卫的期望值必须"
                        f"**独立于**被守卫内容 ⇒ ★★ 缺失 ⇒ **不可判**（★ 不得静默跳过）⇒ {_e}")
    for _k, _v in sorted(_facts.items()):
        if _v and _v not in hb:
            failures.append(f"[㉵ 期望值登记表] 诚实边界缺少登记事实 {_k}={_v} ⇒ ★★ 「次数」与「列举」必须一致"
                            f" ⇒ ★ 否则「era 切点为何不重划」这句话后人无从复核")
    # ★★ **反向守卫**：★ 旧的「两次」措辞不得残留（★ 与「三次」自相矛盾 ⇒ ★★ 文字层面的内部不一致）
    #   ★★ ★★ 这条**不受同源变异影响**：★ 变异是「往边界里**注入**」旧措辞 ⇒ ★ 判据侧不变 ⇒ ★ 照样红
    if "两次部署" in hb or "第二次部署" in hb:
        failures.append("[㉵ 事件链] 诚实边界里仍残留「两次部署 / 第二次部署」的旧措辞 ⇒ ★★ 与「已跨三次构建」自相矛盾")

    # ㉶ ★★★★ **第 31 处：★ 「阈值口径无效」≠「增量与修复无关」⇒ ★★ 两半必须同时披露**
    #   ★★ 形态（★ 真踩到）：★ M5 的 ❌ 曾只写「不可归因于修复」⇒ ★★ 与同一行的 by_kind 增量归因**自相矛盾**
    #   ★★   ⇒ ★★ 关窗报告若照抄 ⇒ ★★ 会漏掉一个**真实且可解释**的增量 ⇒ ★★ ⇒ ★★ 必须先修
    #   ★★ ⇒ ★★ 期望值放**独立夹具**（★ 承第 30 处 ⇒ ★★ 同源变异打不中它）
    try:
        _m5fx = _load_expected_facts("p0-3-m5-baseline-pre7d.txt")
    except Exception as _e:
        _m5fx = {}
        failures.append(f"[㉶ 期望值登记表] 读不到 `fixtures/p0-3-m5-baseline-pre7d.txt` ⇒ ★★ **不可判**"
                        f"（★ 不得静默跳过 ⇒ ★★ 「读不到判据」≠「没发现问题」）⇒ {_e}")
    # ★★ ① 夹具里的每个期望值必须出现在**边界文本**里（★ 隔离层 ⇒ 同源变异打不中）
    #   ★★ ★ 注意：`guard_` 前缀的是**守卫专用期望串**（★ 有的必须出现、有的必须**不**出现）⇒ ★ 不进本循环
    for _k, _v in sorted(_m5fx.items()):
        if _k.startswith("guard_"):
            continue
        if _v and _v not in hb:
            failures.append(f"[㉶ 期望值登记表] 诚实边界缺少登记事实 {_k}={_v} ⇒ ★★ 「次数」与「列举」必须一致"
                            f" ⇒ ★ 否则「阈值无效 ≠ 增量不可归因」这句话读者无从复核")
    # ★★ ② **反向守卫**：★ 旧的**错误断言句**不得残留（★ 期望串同样从夹具取 ⇒ ★★ 同源变异打不中）
    _forbid = _m5fx.get("guard_forbidden_old_claim", "")
    if _forbid and _forbid in hb:
        failures.append(f"[㉶ 事件链] 诚实边界里仍残留「{_forbid}」这句**错误断言**"
                        f" ⇒ ★★ 与 by_kind 增量归因**自相矛盾**（★ 第 31 处）")
    # ★★ ③ **行为守卫**：★ M5 判 ❌ 时行内必须给出「等长 24h 基线」的**两个数字**（★ 第 28 处纪律）
    _d31 = _synthetic(True)
    # ★★ 守卫必须打在 **M5 判 ❌** 的场景上（★ 基线合成场景是 9228 ≤ 9334 ⇒ ✅ ⇒ 披露不渲染）
    _d31["M5_probe_runs_24h"] = "9800 total | free=1900"
    _rows31, _, _ = judge(_d31, stage=1)
    _v31 = rows_by_label(_rows31).get("M5 24h 探测量", "<缺行>")
    if "等长 24h" not in _v31:
        failures.append(f"[㉶ 行内披露] M5 判 ❌ 时必须给出「等长 24h 基线」的口径（★ 与裁决窗口同量纲）⇒ {_v31}")
    if "p50" not in _v31 or "p75" not in _v31:
        failures.append(f"[㉶ 行内披露] 必须给出 pre 侧等长分布的 **p50 / p75**（★ 两边数字 ⇒ 读者能自行判读）⇒ {_v31}")
    if "爆炸半径失控" not in _v31 or "与修复无关" not in _v31:
        failures.append(f"[㉶ 行内披露] 必须同时出现两半：「爆炸半径失控」不成立 **且**「与修复无关」不成立 ⇒ {_v31}")
    # ★★ ④ **反向守卫（行为级）**：★ 旧措辞不得出现在 M5 行内（★ 期望串从夹具取）
    _forbid_row = _m5fx.get("guard_forbidden_old_claim", "")
    if _forbid_row and _forbid_row in _v31:
        failures.append(f"[㉶ 行内披露] M5 行内仍写「{_forbid_row}」⇒ ★★ 与同行的 by_kind 增量归因矛盾 ⇒ {_v31}")
    # ★★★ ★★ **判据必须是结构而不是关键词**（★ 本轮 mutation 抓到的第二个真缺陷）：
    #   ★★ 弱判据 `if "等长 24h" not in _v31` ⇒ ★★ 变异整段后，**兜底分支**里的「等长 24h 基线不可见」
    #   ★★ **仍然命中该关键词** ⇒ ★★ 守卫打不中 ⇒ ★★ ⇒ ★★ 改为断言**具体数值** + **禁止兜底分支**
    _m5_p50 = _m5fx.get("roll24_p50", "")
    _m5_p75 = _m5fx.get("roll24_p75", "")
    if _m5_p50 and _m5_p50 not in _v31:
        failures.append(f"[㉶ 行内披露] 必须给出 pre 侧等长分布的 **p50 = {_m5_p50}**（★ 具体数值 ⇒ 读者能自行判读）⇒ {_v31}")
    if _m5_p75 and _m5_p75 not in _v31:
        failures.append(f"[㉶ 行内披露] 必须给出 pre 侧等长分布的 **p75 = {_m5_p75}**（★ 具体数值 ⇒ 读者能自行判读）⇒ {_v31}")
    if "等长 24h 基线不可见" in _v31:
        failures.append(f"[㉶ 行内披露] 分布键已产出却写「等长 24h 基线不可见」⇒ ★★ 兜底分支误触发 ⇒ {_v31}")
    if "by_kind 对拍不可见" in _v31:
        failures.append(f"[㉶ 行内披露] by_kind 两侧都在却写「by_kind 对拍不可见」⇒ ★★ 兜底分支误触发 ⇒ {_v31}")

    print("== 判定器合成数据自测 ==")
    if failures:
        for f in failures:
            print("   ❌ " + f)
        print(f"\n★★ 自测未通过：{len(failures)} 项")
        return 1
    print("   ✔ ① 全达成 ⇒ 全 ✅")
    print("   ✔ ② 全未达成 ⇒ 全 ❌")
    print("   ✔ ③ 量具缺数 ⇒ 「不可判」（不是 ❌）")
    print("   ✔ ④ 守卫失败/缺失 ⇒ 整表作废")
    print("   ✔ ⑤ Stage 1 / Stage 2 各用**同量纲**基线（pre24 vs pre7d）")
    print("   ✔ ⑥⑦ 每个判据分量（p90 / p99 / slow）都能被单独变异抓住")
    print("   ✔ ⑧ M18 守卫：★ pre 基线与冻结值不一致 ⇒ 报警并整表作废（★ 且 ⑧b 断言它**真的比对过**）")
    print("   ✔ ⑨ 两个计费口径都出行；★ 某口径样本不足时**点名另一口径**")
    print("   ✔ ⑩ M19 守卫：★ 口径结构不变量（all≥free、pre7d=pre6d+pre24）⇒ 自洽通过 / 矛盾报警")
    print("   ✔ ⑪ M20 机制指纹分量（未减半顶格）能单独被变异抓住")
    print("   ✔ ⑫ ★★ 混合口径仍 360 ⇒ **不得**出现 ❌（★ 避免结构性假阴性被读成「修复没生效」）")
    print("   ✔ ⑬ M19 分层穷尽性（normal+exempt == M14x newly）⇒ 不穷尽即报警")
    print("   ✔ ⑬b era 纯度存活者偏差：★ pre 基线被丢弃事件 > 0 ⇒ 行内必须显式提示")
    print("   ✔ ⑭ ★★ 逐对金丝雀**豁免主导 ⇒ 观察面**（★ 不判 ❌，★ 且写明结构性不可达 + 样本不足）")
    print("   ✔ ⑮ ★★ 逐对判据三个分量（med / 阶梯中位 / 未减半档位清零）各自可被单独变异抓住")
    print("   ✔ ⑯ ★★ **恒真分量必须被标注**（★ pre 计数=0 的档位 ⇒ 恒真 ⇒ 不作判据）")
    print("   ✔ ⑰ M15x 逐对分层穷尽性（exempt+normal == total）⇒ 不穷尽即报警")
    print("   ✔ ⑱ ★★ 阈值必须由「干净 pre」推导（★ 剔除「乘数已生效」样本 ⇒ 与 post 同量纲）+ 污染率披露")
    print("   ✔ ⑲ composition 缺失 ⇒ 回退原阈值 + **显式提示「可能偏严」**（★ 不得静默偏严）")
    print("   ✔ ⑳ ★★ 诚实边界的**限定条件不许被静默删掉**（★ 污染率 30.5% / M15x 未被负控证伪 / pre 基线是短窗口快照）")
    print("   ✔ ㉓ ★★★ 合成自测数据与**真捕获夹具**逐字同形（★ 不得手写采集输出）")
    print("   ✔ ㉔ ★★ 阶梯证据集不完备（unexplained_n>0）⇒ **fail-closed 提示「阈值可能仍偏严」**")
    print("   ✔ ㉕ ★★★ 指纹分量**读 fp 键**（★ 成熟键的 top21600 变坏**不该**影响它 ⇒ 尾部不失明）")
    print("   ✔ ㉖ ★★ fp 键未产出 ⇒ **fail-closed**（★ 不得静默判绿）")
    print("   ✔ ㉗ ★★ 「0 条未减半顶格」但**无正向证据** ⇒ 不可判（★ 不得读成「已生效」）")
    print("   ✔ ㉘ ★★ 量具正控：★ pre 侧同口径读不到未减半顶格 ⇒ 「量具无鉴别力」⇒ 不得判绿")
    print("   ✔ ㉙ ★★★ 「fp 视图尾部可见性」守卫：★ fp 视图被改窄 ⇒ **报警 + 整表作废**（★ 且断言它真的比对过）")
    print("   ✔ ㉚ ★★★ 「摇摆」分量（★ featured 状态稳定性）能单独被变异抓住（★ 修复前后的判别信号）")
    print("   ✔ ㉛ ★★ 摇摆键未产出 ⇒ **fail-closed**（★ 不得静默判绿）")
    print("   ✔ ㉜ ★★ 摇摆 0 ⇒ 行内必须给出「featured 状态稳定 ⇒ 修复已真正落地」的正向结论")
    print("   ✔ ㉝ ★★★ 「对照组」的**相位/构成**必须披露（★ 零 6h 顶格 ⇒ 不可归因；★ 裁决符号不得软化）")
    print("   ✔ ㉞ ★★ 诚实边界必须含「相位效应」与「投影只投影时钟、不投影数据积累」两条")
    print("   ✔ ㉟ ★★★ M5 判 ❌ 时必须给出「基线口径」披露（★ 阈值口径无效 ⇒ 「爆炸半径失控」不成立；"
          "★★ 但「增量与修复无关」同样不成立；★ 符号不得软化）")
    print("   ✔ ㉠ ★★ 诚实边界必须含「M5 的 ❌：★ 阈值口径无效 ⇒ 「爆炸半径失控」不成立」"
          "＋「★ 但『增量与修复无关』同样不成立」两条")
    print("   ✔ ㉡ ★★★ M5 判 ❌ 时必须给出「增量归因（by_kind）」分解（★ 回答「增量从哪来」）"
          "（★ 与 ㉟ 的「基线口径」互补：★ 那边答「阈值是否成立」、★ 这边答「增量从哪来」）")
    print("   ✔ ㉣ ★★★ 诚实边界必须含「负控窗口几何必须与被验收窗口一致」+「原负控的旧 featured ❌ 是假红」")
    print("   ✔ ㉤ ★★★ **跨脚本守卫**：★ 负控脚本必须真的把 `now()` 随 era 切点平移（★ 否则门没牙）")
    print("   ✔ ㉥ ★★★ 诚实边界必须含「M11 的基线不同量纲（★ Stage 1 的 M11 符号两个方向都可能错）」")
    print("   ✔ ㉧ ★★★ 诚实边界必须含「post 段必须被当前时刻界定（★ M11 的 post 段曾**无界** ⇒ 负控里恒真 ⇒ 没有牙）」")
    print("   ✔ ㉨ ★★★ **跨脚本行为守卫**：★ 负控必须真的要求 Stage 1 的 M11 namespaced 判负"
          "（★ 且 Stage 2 不得要求 ⇒ 否则是假红）")
    print("   ✔ ㉪ ★★★ 诚实边界必须含「第二次部署（11-11 01:07:37）★ diff 碰了 featured 路径 ⇒ ★ 代码解耦 + 等长窗口实测 ⇒ era 切点仍有效」")
    print("   ✔ ㉬ ★★★ 诚实边界必须含「同类缺陷必须穷举」＋ ★★ **跨脚本行为守卫**：★ 穷举审计器必须真的跑通且零违例")
    print("   ✔ ㉳ ★★★★ **第 27 处：★ 「审计器自己也要被审计」⇒ ★★ 覆盖率守卫（★ M14/M14x 主判据块必须可见、★ 块数 ≥ 38、★ 关键块不得缺失）＋ ★★ 边界 ㉲ 必须存在**（★ v1 时主判据块覆盖率 = 0% ⇒ 「全部合规」是空的）")
    print("   ✔ ㉮ ★★★ 诚实边界必须含「分钟型量被 ::int 截到分钟 ⇒ 与阈值做精确比较 ⇒ 亚分钟差被放大成 1 单位假红」"
          "（★ 含实测 raw p90 = 360.5069）+「判据分辨率必须 ≥ 被测抖动幅度」")
    print("   ✔ ㉯ ★★★ **行为守卫**：★ 「取整边界伪影」披露必须在 M20 豁免行真的出现（含 raw 值）"
          "＋ ★ 裁决符号**不得**软化 ＋ ★ raw 缺失须写「不可见」＋ ★ 真实差很大时**不得**出现该披露（★ 假阳性也要治）")
    print("   ✔ ㉱ ★★★ 诚实边界必须含「分位数是秩统计量 ⇒ 样本量小的时候极不稳定」"
          "＋ ★ **行为守卫**：★ post n ≪ pre n 时 M14x 旧 featured 判 ❌ 必须行内披露「小样本分位数不稳」"
          "＋ ★ 样本量相当时**不得**出现该披露（★ 假阳性也要治）")
    print("   ✔ ㉴ ★★★★ **第 13 处纪律的最后一个候选点**：★ 「同一行里两个 n 来自不同窗口」必须行内披露"
          "（★ M20 **豁免行也引用 fp 视图** ⇒ 成熟 n vs fp n_all 不同窗 ⇒ 不可相减 ⇒ 不得读成「尾部有回归」）"
          "＋ ★ 披露须**同时给出两个 n 的数字** ＋ ★ 两个 n 相等时**不得**出现该披露（★ 假阳性也要治）")
    print("   ✔ ㉵ ★★★★ **第三次跨构建（★ 11-11 02:06:59 / ★ 构建 `7b926bfb`）**：★ 事件链（★ 19:01:04 / ★ 01:07:37 / ★ 02:06:59）"
          "＋ ★ 代码腿（★ `bg/` diff 为空 ⇒ 探针源码逐字节未变；★ `ir.ParseAnthropicStreamEvent` 只被 `responses_bridge.go` 调用 ⇒ 探针层与被改层解耦）"
          "＋ ★ 数据腿（★ 相位对齐等长窗 W1 = 89 / W3 = 66 ⇒ 形态同构 ⇒ 部署未改变机制）"
          "＋ ★ 形态腿（★ `claude/sonnet-5` / ★ `http_404` ⇒ exempt ⇒ 设计上钉 6h ⇒ 6h 周期常态）"
          "＋ ★ **1 分钟停探必须披露**（★ 02:07 = 0 ⇒ 0.07% ≪ ㉰ 的 5.8% ⇒ 不影响分位数、★ 但不许写成「零中断」）"
          "＋ ★ 两条新纪律（★「部署后出现 X」≠「部署引起 X」/ ★ 自己写的等长窗也要打印长度核对）"
          "＋ ★★ **期望值登记表**（★ `fixtures/p0-3-deploy-events.txt`）⇒ ★★ 判据与被守卫内容**不同源** ⇒ ★★ 同源变异也打不中它")
    print("   ✔ ㉶ ★★★★★ **第 31 处：★ 「阈值口径无效」≠「增量与修复无关」⇒ ★★ 两半必须同时披露**"
          "（★ 证据①等长 24h 口径：★ n=145、★ p50=7871 / p75=9755 / p90=10417 ⇒ ★ 阈值 9334 落在 62.1% 分位"
          "＋ ★ 证据②日历日桶是 partial day ⇒ 不得当 24h 基线 ＋ ★ 证据③pre24 by_kind 5766/3257/222 ⇒ ★ periodic 反降）"
          "＋ ★ 期望值走**独立夹具** ⇒ ★★ 同源变异打不中它 ＋ ★ 反向守卫（★ 旧错误断言句不得残留）")
    print("\n== 合成数据自测通过 ==")
    return 0


# ─────────────────────────────────────────────────────────────────────
def honest_boundary_lines() -> list[str]:
    """★ 诚实边界的**唯一来源**（★ 本轮抽成函数 ⇒ 自测能守住「披露不许被静默删掉」）"""
    lines: list[str] = []
    lines.append("   ⑮ ★★★★ **判据的「可达性」必须先审计**（★★ 本轮第 11 处口径修正，★★ 差点造成结构性假阴性）："
          "★ 实测新覆盖人群 **78%** 的失败是**豁免类**（协议形态 ⇒ 设计上钉 6h ⇒ **豁免 featured 乘数**）"
          "⇒ ★★ 混合口径的「post p90 ≤180」**结构性不可达** ⇒ ★★ 若不先审可达性就关窗 ⇒ "
          "**必然假阴性**且会被读成「修复没生效」⇒ ★★ 纪律：**阈值先问「机制能不能做到」，"
          "再问「读数长什么样」**；★ 效应判据必须**分层**（该减半的 / 设计上不变的），"
          "且 ★ **分层穷尽性要有守卫**（normal + exempt == 原集合）。")
    lines.append("   ⑱ ★★★★ **同一个模型在不同口径下结论可以相反**（★ 本轮第 13 处口径修正，★★ 差点写错进结论）："
          "★ M15 旧键是 **free 切片** ⇒ 实测四只金丝雀**完全同形**（pre7d 各 13 条 = **12 豁免 + 1 normal**，"
          "★ 豁免占比 **92%**）⇒ ★ 「post med ≤180」**结构性不可达** + 剔豁免后**只剩 1 条** ⇒ ★★ **四只全是观察面**；"
          "★ 而**全计费口径**下 **deepseek-v4-pro-0813 的 normal 有 41 条（pre7d）/ 18 条（pre24）**"
          "⇒ ★★ **它是唯一**可做逐对判据的金丝雀 ⇒ ★★ 逐对判据**只落在它一只**。"
          "⇒ ★★ 纪律：**同一段论证里出现两个口径的数字，必须标口径**（★ 「deepseek normal 47 条」是全计费口径，"
          "★ 「normal 1 条」是 free 切片 ⇒ ★ 混着说就会得出相反结论）。")
    lines.append("   ⑲ ★★★★ **逐对判据必须「逐只」自证可达性**（★ 本轮第 13 处口径修正）：★ 每只金丝雀按**它自己**的"
          "normal 样本量（阈值 10）决定判据 / 观察面（★ **数据驱动**，★ 不硬编码模型名）；★ 阈值**全部由该只 pre 实测推导**"
          "（★ deepseek 的 normal 阶梯 pre = **3600s（60min）** ⇒ 期望 post ≤ **1800s（30min）**，"
          "★ 而**不是**笼统的「≤180min」⇒ ★★ 180min 对 60min 的事件**毫无鉴别力**）；"
          "★ 且 ★ **pre 期计数为 0 的档位 ⇒ post 侧恒真 ⇒ 必须标注「不作判据」**（★ 否则就是**恒真判据**＝门没牙）。")
    lines.append("   ⑳ ★★★★ **「newly」≠「pre 期未 featured」**（★ 本轮第 14/16 处口径修正，★★ 同量纲的第 2 维）："
          "★ 「newly」的定义 = `st_new \\ st_old`，而 `st_old` 的 raw 变体**只覆盖 `models_canonical.featured=TRUE` 且 active**"
          "⇒ ★★ **`routing_policy.featured_models` 成员的 raw 变体不在 st_old 里**"
          "⇒ ★★ ★★ **但「rp 成员 pre 期就已 featured」是错的** ✘（★ 实测 rp 的 **34 条全是裸 canonical 名、0 条 namespaced**，"
          "★ 且修复前只拿 raw 名比 ⇒ **一律命不中**）⇒ ★★ 真正让 namespaced raw 在 pre 期拿到 featured 的是"
          "**用量 top-N tier**（★ `GROUP BY rl.raw_model_name ORDER BY calls DESC LIMIT $2` ⇒ tier 里存的就是 namespaced raw 名）"
          "⇒ ★★★ ★★ **该 tier 每 10 分钟刷新 + 24h 滚动窗 ⇒ 「pre 期是否 featured」是逐时刻变动的属性**"
          "⇒ ★★ ⇒ 同一只模型在同一 pre 窗口里**既有减半样本也有未减半样本**（实测 `zcode/GLM-5.3-Flash-Trial`）"
          "⇒ ★★★ ⇒ **「人群状态」这一维不可能用成员资格重建**（DB 里没有 tier 的历史）⇒ ★★★ **只能用「阶梯值痕迹」反推**。"
          "⇒ ★★ 实测污染：★ M20 normal pre7d **47/154 = 30.5%**（★ 来源几乎全是 `zcode/GLM-5.3-Flash-Trial` 一只："
          "ladder 150×33 + 1800×8 + **90×6**，出现在 11-05/07/09）；★ pre24 normal = **0/21**；★ exempt pre7d = **2/537**。"
          "⇒ ★★ 修法（★ **不改 st_old 口径、不重冻基线**，★ 关窗前重冻风险太高）：① 把污染率固化成 "
          "`M20_<层>_<窗>_composition` 读数键（n/mul_evidence/clean_n/clean_slow/slow/contam_pct/null_ladder_n/"
          "unexplained_n/unexplained_values）⇒ ★★ 行内**披露污染率**；"
          "② slow 阈值改由「**干净 pre**」推导（★ 干净 pre 与 post 段**同量纲**：post 全是 featured 后）"
          "⇒ ★★ normal pre7d 干净 slow = 19/107 = **17.8%** ⇒ 阈值 **8.9%**（★ 未修正的 12.3%/2 = 6.15% ⇒ ★★ **偏严 45%**）；"
          "③ composition 缺失 ⇒ **回退原阈值 + 显式提示「可能偏严」**（★ 不得静默偏严）；"
          "④ ★★ **证据集完备性自检**：★ 每个观测阶梯值必须**可解释**（∈ 未减半空间 ∪ 证据集）"
          "⇒ ★★ 否则 `unexplained_n > 0` ⇒ ★★ 判定器**显式提示「阈值可能仍偏严」**（★ fail-closed，★ 不整表作废）。"
          "⇒ ★★ 纪律：**「同量纲」不只指窗口长度，也指**人群状态**（★ pre 的这批样本在 post 段会「变样」⇒ 阈值必须同状态）。")
    lines.append("   ㉑ ★★ **M15x 逐对判据未被负控证伪**（★ 只能作为「未被证伪的判据」披露）："
          "★ 负控把 era 切点前移 N 天 ⇒ ★ 负控 era 里 **deepseek 的 normal = 0 条** ⇒ ★★ 它被**自动降级为观察面**"
          "⇒ ★★ 也就是说 ★ **M15x 的逐对判据没有经历过一次「必须判负」的检验**（★ 与群体层的 M20 不同：★ M20 负控**判负** ✔）。"
          "⇒ ★★ 诚实表述：★ M15x 是「**未被证伪**」，不是「**已被证伪为恒真/恒假**」⇒ ★ Stage 1/2 读出 ✅ 时必须并列这句。")
    lines.append("   ㉒ ★★ **pre 基线的「时间构成」必须披露**（★ 本轮第 14/15 处口径修正）："
          "★ 逐日拆开看，★ M20 normal pre7d 的阶梯**混了三个阶段**（11-03/04 = 6h 顶格；11-05~07 = 极短阶梯 0/150；"
          "11-09 = 1800 且同日还有 top3600=24）⇒ ★★ pre 基线**严重不均匀** ⇒ ★★ 「pre/2」阈值的含义**受限**；"
          "★ 且 ★ deepseek 的 41 条 normal **几乎全来自 11-09、11-10 两天**（11-09 单日 23 条 http=503 ladder=3600；"
          "11-06~11-08 三天一条都没有）⇒ ★★ 它的 pre 基线是「**短窗口快照**」而非 7 天稳态。"
          "⇒ ★★ 推论：★ **Stage 1 的 pre24/post24 是紧邻两个 24h 窗口 ⇒ 构成漂移最小** ⇒ ★★ 反而比 Stage 2 的 "
          "pre7d/post7d 更稳（★ 成本是样本量更小 ⇒ ★ Stage 1 只给方向）。")
    lines.append("   ㉓ ★★★★ **「排除进行中的段」的过滤若作用在窗口函数（LEAD）之前 ⇒ 会把「成功行」也滤掉**"
          "（★ 本轮第 17 处口径修正，★★ 又一个**「偏向判绿」的假阴性来源**）：★ 判据层的「排除最近 2h」"
          "写在 **fr**（LEAD 的输入）上 ⇒ ★★ **post 段的成功行也被滤掉** ⇒ ★★ 恢复发生在最近 2h 内的 episode"
          "⇒ 它的成功行没了 ⇒ `next_success` 不再是 TRUE ⇒ ★★ **该 episode 被整体漏掉**"
          "⇒ ★★ ⇒ **post 的 max / p90 / p99 被系统性拉低** ⇒ ★★ ⇒ 「post ≤ pre」的判据**天然偏向判绿**。"
          "⇒ ★★ 而它宣称的用途（「排除进行中的段」）**本来就已经由 `next_success = TRUE` 满足** ⇒ ★★ 纯属副作用。"
          "⇒ ★★ 修法＝**删掉该过滤**（★ pre 侧逐字节不变 ⇒ ★ 基线不漂；★ post 样本随观测时长增长 ⇒ ★ 尾部不再被截断）。"
          "⇒ ★★★ **纪律：★ 「过滤掉尾部」的谓词要问一句：★ 它作用在窗口函数之前还是之后？★ 之前 ⇒ 会连「完成信号」一起滤掉。**")
    lines.append("   ㉔ ★★★★ **同一判据的不同分量，成熟度需求可能不同 ⇒ 必须分视图读取**"
          "（★ 本轮第 18 处口径修正，★★ 又一个**「偏向判绿」的假阴性来源**）：★ M20 的 post 段带着"
          "**两道**只对「已结算段」成立的过滤（★ ① `fail_at < now() - 6h`（成熟度）"
          "★ ② `next_at IS NOT NULL`（LEAD 必须找到下一次探测））⇒ ★★ 关窗读数时 post 窗口的"
          "**最后 6h 完全不可见** ⇒ ★★ **尾部若发生回归（修复失效），指纹仍会读出 0** ⇒ ★★ **假绿**"
          "（★ 与第 17 处修正同族：★ **过滤掉尾部 ⇒ 偏差偏向判绿**）。"
          "⇒ ★★ **关键洞察（★ 本轮最可复用的）**：★ 阶梯值 `next_retry_seconds` 随**失败 run 即落库**"
          "（`bg/node_probe.go:3043`）⇒ ★★ **指纹不需要结算** ⇒ 可从**未成熟段**读；"
          "★ 而 slow 分量测的是「失败 → 下一次探测」的**间隔** ⇒ ★★ **必须结算** ⇒ 只能读成熟段。"
          "⇒ ★★ 修法 = **分视图读取**（★ 新增 `M20_<层>_<窗>_fp` 键：★ 无成熟度过滤、无 next_at 要求）"
          "⇒ ★★ 判定器的 M20 normal 变成**两个分量**：① **指纹**读 fp（★ 需**正向证据**：★ 减半独占值 "
          "{7,90,150,450,1800,10800} ∉ 未减半空间 ⇒ ★ 只能是乘数产生的；★ 且 **量具正控**：★ pre 侧同口径"
          "必须能读到未减半顶格 ⇒ 否则「**量具无鉴别力 ⇒ post 的 0 不可解释**」⇒ fail-closed）；"
          "② **slow** 仍读成熟段（★ 固有 6h 结算延迟 ⇒ 行内披露「两分量跨度不同」）。"
          "⇒ ★★★★ **★ 补（第 13 处纪律的最后一个候选点，★ 2026-11-11 05:2x 实测）**："
          "★ **不只有 normal 行有两个视图** ⇒ ★ **豁免行同样引用了 fp 视图**（★ 对照读数：未减半 6h 顶格）"
          "⇒ ★★ ⇒ ★★ **一行里并列出现的两个 n 可能来自不同窗口** ⇒ ★★ 读者会自然地拿它们相比"
          "⇒ ★★ **读成「fp 比成熟视图多出 X 条 ⇒ 尾部有回归」** ⇒ ★★ **这是误读**"
          "（★ ★ 那 X 条**本来就该在 fp 里** ⇒ ★ 指纹分量的设计就是要看见它们）"
          "⇒ ★★ ⇒ **行内必须显式给出两个 n 各自的窗口**（★ `cross_window_disclosure()`）"
          "⇒ ★★ 且 **两个 n 相等时不披露**（★ 不得制造噪声）。"
          "⇒ ★★ **纪律：★ 凡判据有多个分量，先逐分量问一句「这个量需要等结算吗」；"
          "★ 需要与不需要的，必须分视图读，否则尾部会静默失明；"
          "★ ★★ 且**同一行里出现两个 n 时，必须标明各自窗口**（★ 否则读者会误读成「尾部有回归」）。**")
    lines.append("   ㉕ ★★★★ **观测窗口中途的部署 ⇒ 必须显式核查「机制连续性」，不能假设**（★ 2026-11-10 19:01 实测）："
          "★ era2（★ build_seq 2995 / sha 8028459d）在 **2026-11-10 19:01:04 +08** 被 SIGKILL(137) 停掉，"
          "★ 由 **build_seq 2996 / sha b92313da**（★ P0-B streaming 修复）接管 ⇒ ★★ **post 窗口跨越两个构建**。"
          "⇒ ★★ 已核查三件事（★ 见主档 §5.9.21.13.15 与 `scripts/audit/recovery-detection-p0-3-deploy-split.sql`）："
          "① **featured 集未变**（★ 15 canon_featured / 34 rp / 55 old_raw / 200 new_raw ⇒ ★ era2 展开完好）"
          "⇒ ★ **机制连续**；② **探针未中断**（★ 新容器起来后每分钟仍有 probe run ⇒ ★ 无「停探窗口」）；"
          "③ **diff 不碰探针/退避路径**（★ 只改 `domains/streaming/handler.go`）⇒ ★★ **乘数/退避机制未被改动**。"
          "⇒ ★★ 故 **era 切点（13:42:30）仍然有效**（★ post = 「featured 展开之后」，★ 与切点定义一致）⇒ ★ **不重划**。"
          "⇒ ★★ **但必须披露**：★ 窗口中途换过构建 ⇒ ★ 若读数时 post 段出现未减半 6h 顶格，"
          "★ ★ **优先怀疑新容器启动后的 tier 冷启动窗口**（★ usage top-N tier 每 10 分钟刷新、启动时从 DB 重载"
          "★ ★ ⇒ 该窗口内 newly 失败可能读到**未减半**阶梯）⇒ ★ **先跑分段脚本看 segment B 的 top21600_n**。"
          "⇒ ★★ **纪律：★ 观测窗口不是「一口气跑完」的 ⇒ ★ 中途任何部署都要显式核查三件事"
          "（★ ① 机制集有没有变 ② 探针有没有停 ③ diff 有没有碰被测路径）⇒ ★★ 否则「窗口连续」只是假设。**")
    lines.append("   ① M1 / M1b 可用率**一律不作判据**（归因不可分 + 相位敏感："
          "credential_recovery 扫描 6h 周期 ⇒ 采样须标注「距上次 sweep 多久」）。")
    lines.append("   ② era2 的增量效果是「消除资格障碍」的**潜在收益**；"
          "目标人群（106 个新覆盖变体）修正 join 后 7 天有 **461 事件（free 口径）"
          "/ 691 事件（全计费口径）**，但其中四个「冷金丝雀」各仅 13 次（≈1.9/日）"
          "⇒ ★★ 群体层可判（M14），★ 逐对幅度仍需 Stage 2。")
    lines.append("   ③ ★★ M11/M12/M12s/M12c 的 post 段**右删失**（★ **偏差方向对我们有利** ⇒ 必须在报告里披露）："
          "★ **固有的那部分**（★ 恢复尚未被观测到的「进行中」段 ⇒ 本就不该计入）★ 仍然存在；"
          "⇒ ★★ **而「过滤诱导」的那部分已在 §5.9.21.13.13 修掉**（★ 原先 post 侧额外滤掉了"
          "**最近 2h 的成功行** ⇒ ⇒ **post 的 max/p90/p99 被系统性截断** ⇒ ⇒ **偏差方向恰好是「偏向判绿」**"
          "⇒ ★★ 实测：★ 改后 post 侧**多出 3 个键**（★ `M11b_post_fix_namespaced` 从「不存在」变为存在）"
          "⇒ ★★ **而 pre 侧逐字节不变**（★ 37/50 episodes 全部一致 ⇒ ★ 基线不漂 ⇒ ★ M18/三时钟门仍全绿）。")
    lines.append("   ④ M6 是**资格面**，不是「行为已改善」的证据。")
    lines.append("   ⑤ ★★ 基线口径已在 2026-11-10 14:2x 修正：原先的绑定过滤用 "
          "`provider_model_id = (… LIMIT 1)`，而 raw_model_name 是**一对多**"
          "⇒ 只统计到 30% 事件（4,073 vs 13,555）⇒ ★ 现全部基线均为**修正后**口径。")
    lines.append("   ⑥ ★★ 「量具算不出」（分位缺失）单列为「不可判」，**不并入未达成**；"
          "守卫指标**缺失**同样判量具不可信 ⇒ 整表作废。")
    lines.append("   ⑦ ★★★ 判据的第四道关 = **人群稳定性**：★ 高样本 ≠ 可判 —— "
          "M12s 的 post ns|feat 混入了 era2 新纳入的 106 个冷对 ⇒ 尾部变化**不可归因** "
          "⇒ ★ 降级为参考；★ 判据改用 **M14 三组**（st_old ⊆ st_new ⇒ pre/post 同一批对）。")
    lines.append("   ⑧ ★★★ pre 基线必须**冻结**：★ 口径已把窗口/成熟度/era 纯度全部**锚定到切点** "
          "⇒ ★ 真跑 / Stage 1 / Stage 2 三个时钟下 pre 段**逐字节一致**（41/41 键）；"
          "★ 否则 Stage 2 的 pre 侧会整段消失、Stage 1 的 pre 会随查询时刻漂移。")
    lines.append("   ⑨ ★★★ **样本量不能用「事件率 × 窗口长度」外推**：★ 事件**成簇**"
          "（48h 内只有 11 个活跃小时桶、前 5 簇占 75%）+ ★ **右删失**"
          "（慢阶梯 6h 的失败在 6h 内不计入 ⇒ post 段样本随时间「回填」）"
          "⇒ ★★ 只能用「同长度、同成熟度」的实测窗口算（★ 本轮新增 pre24 维度）。")
    lines.append("   ⑩ ★★ **同量纲不只是「同人群」，还是「同窗口长度」**：★ 实测 newly 组的 "
          "pre24（14 条，slow 100%）与 pre7d（461 条，slow 13.0%）**分布截然不同** "
          "⇒ ★★ 借用另一窗口的基线 = 判错 ⇒ ★★ **判据基线随 stage 切换**"
          "（Stage 1 → pre24、Stage 2 → pre7d）。")
    lines.append("   ⑪ ★★ **指标的「窗口维度」标签必须与边界一致**：★ 本轮曾把 CASE 顺序写反 ⇒ "
          "「pre24」标签实际命中「更早 5 天」（样本差 32×、阈值差 4×）"
          "⇒ ★★ **用「两个窗口长度对拍同一查询」验一次**（7d 461 条 vs 48h 55 条 ⇒ 物理上不可能）。")
    lines.append("   ⑫ ★★★ **标签名必须代表它真实的窗口长度**（★ 本轮第 10 处修正）：★ 改完标签后 "
          "「pre7d」实际只覆盖 [切点-7d, 切点-24h) ⇒ **只有 6 天**却叫 pre7d "
          "⇒ ★ Stage 2 拿「6 天 pre」比「7 天 post」⇒ **不同量纲** "
          "⇒ ★★ 而且判据阈值是按 7 天的 slow 13.0% 折半来的、显示的却是 6 天的 10.3% "
          "⇒ ★★ **基线与阈值静默脱钩**。⇒ 现在四桶：pre6d / pre24 / **pre7d（整 7 天）** / post，"
          "且恒等式 **pre7d = pre6d + pre24** 可对拍（★ 461 = 447 + 14 ✔）；"
          "⇒ ★★ 并加了 **M18 守卫**：pre 基线事件数与冻结值不符 ⇒ 整表作废。")
    lines.append("   ⑬ ★★★ **口径过滤器也会静默丢样本**（★ 本轮第 9 处修正）：★ M14 原先限定 "
          "`billing_mode='free'` ⇒ ★★ 同一模型名同时挂在 free / per_token / token_plan "
          "三种凭据上 ⇒ ★★ 另外两类凭据上的探测被**静默丢掉**（★ 与「一对多取代表行」同类）"
          "⇒ ★★ post era 实测 5 次新覆盖探测**全在 per_token** ⇒ free 口径下 post 恒为 0 "
          "⇒ ★★ **效应判据会永远空**，被误读成「修复没生效」。⇒ 现在同时产出 free 切片与"
          "全计费口径（★ pre24 新覆盖：free **14** 条 vs all **78** 条 ⇒ ★ Stage 1 才够样）。")
    lines.append("   ⑭ ★★ **判据的目标人群要先算「可观测子集」**：★ 106 个新覆盖变体里 "
          "**61 个挂在 `providers.enabled=false` 的供应商上**（结构性不可探）+ 48 个无 free 绑定 "
          "⇒ ★★ 真正可探的只有 **60 个**（且这 60 个恰好都在 `node_probe_state` 调度表里）"
          "⇒ ★★ 7 天内真正发出过探测的只有 **≈21 个模型名** ⇒ ★★ 头条口径「106 个」"
          "**高估了效应面约 5×** ⇒ ★★ 结论必须写成「**可观测子集**上的效应」，不能写成「106 个全部」。")
    lines.append("   ㉖ ★★★★ **「对照组」这类跨窗比较，先问「两窗口的失败形态是否同构」"
          "⇒ ★★ 中位数对「突发批次的相位」极其敏感（★ 本轮第 21 处口径修正）："
          "★ 对照组里 **~80 只**协议形态模型（★ http 404/410 ⇒ 设计上钉 **6h = 21600s** 阶梯）"
          "以 **6h 节奏**被探（★ 实测 13:30 → 19:30 → 01:30 → 07:30 → 13:30）"
          "⇒ ★★ era 切点（11-10 **13:42:30**）**落在两次批次之间** ⇒ ★★ post 窗口的**前 5.6h "
          "里一个批次都没有** ⇒ ★★ 对照组 post 只剩 11 只（★ 阶梯 3600）⇒ med **60** "
          "⇒ ★★ 而 pre24 含 **4 个**批次（★ ≈960 条 360min）⇒ med **360** "
          "⇒ ★★ ⇒ **「对照组也变了」判 ❌** ⇒ ★★ **纯相位假红**（★ 机制没变、读数变了）"
          "⇒ ★★ 已实测证实：★ 11-10 **19:28–19:31** 那批 6h 周期**如期落地**（★ 阶梯仍 21600）。"
          "⇒ ★★ 纪律：★ **跨窗比中位数前，先看两窗口的阶梯谱是否同构**；★ ★★ **窗口长度必须 ≥ "
          "被观测的最长阶梯周期**（★ 24h ≥ 6h ✔ / 7d ✔），★ **中途读数（预演/诊断）不具备可比性**。")
    lines.append("   ㉗ ★★★★ **「关窗预演（as-of）」只投影「时钟语义」，不投影「数据积累」**"
          "（★ 本轮第 21 处口径修正的伴生边界）：★ 投影 = 把 `now()` 换成未来时刻后复采"
          "⇒ ★★ 它能可靠暴露**与时钟/成熟度相关**的缺陷（★ slow 未结算、★ M18 基线漂移、★ 键缺失）"
          "⇒ ★★ 但 post 窗口在投影里**仍是「当前已积累的那一段」** ⇒ ★★ 任何按「窗口内事件构成」"
          "计算的判据（★ 尤其是**对照组的中位数**）在投影里可能**相位失真** ⇒ ★★ 投影对这类判据"
          "只能**提示**，**不能定论** ⇒ ★★ 一切结论以**关窗当天真读**为准。")
    lines.append("   ㉘ ★★★★ **M5 的 ❌：★ 「阈值口径无效」⇒ 「❌ ⇒ 爆炸半径失控」不成立；★★ 但「增量与修复无关」**同样不成立** ⇒ ★★ 两半必须同时披露（★ 第 31 处口径修正，★ 2026-11-11 04:4x +08）**："
          "★ M5 的 base = **9242** = **pre24 单日快照**（★ 实测 pre_24h_count = 9243）"
          "⇒ ★ 而探测量**按天剧烈波动**（★ 实测 pre7d 逐日 min=**1292** / max=**9905** ⇒ **7.7×**）"
          "⇒ ★★ **pre 期实测 11-09 = 9685 已超 limit 9334** ⇒ ★★ ⇒ **该阈值在 pre 期就不成立**"
          "⇒ ★★ ⇒ M5 判 ❌ 时**不得**被读成「修复引起爆炸半径」⇒ ★★ 本轮只做**行内披露**"
          "（★ **裁决符号保持 ❌** ⇒ ★ 门不钝化；★ 且逐日分布键缺失时必须显式写「基线口径不可见」）"
          "⇒ ★★★★ **★★★ 第 31 处补上「另一半」（★ 关窗前实测 ⇒ ★★ 否则关窗报告会自相矛盾）**："
          "★ **「阈值无效」只推出「爆炸半径失控」不成立 ⇒ ★★ 它并不推出「增量与修复无关」**"
          "⇒ ★★ ⇒ ★★ **两半必须同时披露**，★ 否则同一行里「❌ 不可归因」与 by_kind 的「增量可完全归因」**自相矛盾**"
          "⇒ ★★★★ **★ 证据 ①（★ 等长 24h 口径 ⇒ ★ 与 M5 裁决窗口**同量纲**）**："
          "★ pre7d 的 **145** 个**等长**滚动 24h 窗口（★ 端点锚定 era 切点 ⇒ 时钟无关）："
          "★ **p50 = 7871 / p75 = 9755 / p90 = 10417 / max = 11197 / min = 2467 / mean = 7037.5**"
          "⇒ ★★ 冻结阈值 **9334** 落在该分布的 **62.1%** 分位（★ **90/145** 个窗口 ≤ 它、★ **37.9%** 的窗口超过它）"
          "⇒ ★★ ⇒ 「阈值在 pre 期就不成立」**第一次有了等长同量纲的量化**"
          "⇒ ★★★★ **★ 证据 ②（★ 日历日桶不能当 24h 基线 ⇒ ★ 另一个口径缺陷）**："
          "★ `M5_pre7d_runs_by_day` 的 8 个桶里首尾是 **partial day**（★ 11-03 只覆盖 **10.29h**、★ 11-10 只到 **13.71h**）"
          "⇒ ★★ 计数**必然低估** ⇒ ★★ 用它的均值当 24h 基线会**系统性压低**基线 ⇒ ★★ 真修法必须用**等长 24h 口径**"
          "⇒ ★★★★ **★ 证据 ③（★ 增量的来源 ⇒ ★ 归因钉死）**："
          "★ pre24 by_kind = **credential_recovery 5766 / periodic 3257 / sync_request 222**"
          "⇒ ★★ post24 by_kind（★ **时点观测**：★ 2026-11-11 03:4x +08）= **credential_recovery 6172 / periodic 3141 / sync_request 428**"
          "⇒ ★★ 总量 **+496（+5.4%）** ⇒ ★★ **全部**来自 **credential_recovery +406（+7.0%）** 与 **sync_request +206（+92.8%）**"
          "⇒ ★★ 而 **periodic 反而 -116（-3.6%）** ⇒ ★★ ⇒ ★★ **「爆炸半径失控」的最强反证就在 periodic 下降**"
          "（★ 爆炸半径的直接代理是 periodic 调度量 ⇒ ★ 它降 ⇒ ★ 不是调度失控）"
          "⇒ ★★ ⇒ ★★ **正确读法：★ ❌ = 「预算口径过时（单日快照）+ 增量来自设计内的恢复探测」；★ 不是「爆炸半径失控」，★ 也不是「与修复无关」**"
          "⇒ ★★★ **真正的修法**（★ 用 ≥7 天的均值/中位/分位做基线，★ 且必须**等长 24h 口径**、★ 不得用日历日桶）"
          "**登记为 Stage 2 之后的基线口径改进项**（★ 关窗前**不重冻**已验证基线）。")
    lines.append("   ㉙ ★★★★ **负控的窗口几何必须与被验收窗口一致（★ `now()` 必须随 era 切点一起平移）**"
          "（★ 本轮第 23 处口径修正）：★ 原负控只平移了**切点**、没平移 `now()`"
          "⇒ ★★ 负控的 post 窗口 = **[切点-N天, 真实now]** = **~35h**（★ 而被验收的 Stage 1 窗口是 **24h**）"
          "⇒ ★★ **两者几何不一致** ⇒ ★★ 实测**两行裁决符号在两种几何下翻转**"
          "（★ M14 / M14x 旧 featured 人群（无回归）：35h ⇒ **❌ 未达成**、24h ⇒ **✅ 达成**）"
          "⇒ ★★ ⇒ **负控的绿不能代表验收窗口** ⇒ ★★ 修法：★ `now()` 一并平移到「**新切点 + stage 窗口**」"
          "⇒ ★★ 负控成为验收窗口的**忠实复刻**（★ 窗口长度、成熟度尾部（now-6h）、事件总量全对齐）"
          "⇒ ★★ **门不钝**（★ 实测两种几何下效应行 M20 **都判 ❌** ⇒ 负控仍有牙）。"
          "⇒ ★★★ **★ 另**：★ 原负控里「M14 / M14x 旧 featured 判 ❌」是**几何错配造成的假红**"
          "（★ 35h post 比 24h pre ⇒ 不同量纲）⇒ ★★ **不得**读成「负控里旧 featured 人群也退化了」。")
    lines.append("   ㉚ ★★★★ **M11 的基线取自「7 天」pre，而 Stage 1 的 post 是 24h ⇒ ★ 不同量纲 ⇒ ★ 两个分量的偏差方向相反**"
          "（★ 本轮第 23 处口径修正的伴生边界）：★ 实测（M11 namespaced）："
          "★ pre **7d**（★ 当前判据用的基线）= 41 episodes / med **3** / p90 360 / max **1791**；"
          "★ pre **24h**（★ Stage 1 的同量纲窗口）= 9 episodes / med **30** / p90 360 / max **360**"
          "⇒ ★★ **两个分量的偏差方向相反**：★ `med` 项 ⇒ 7d 基线**偏严 10×**（⇒ 假红风险）；"
          "★ `max` 项 ⇒ 7d 基线**偏松 5×**（⇒ 假绿风险）⇒ ★★ ⇒ **Stage 1 的 M11 符号两个方向都可能错**"
          "⇒ ★★ **但不能**改成 pre24 基线（★ 换基线后的「牙」来自**窗口错配** ⇒ 另一重错；★ 且它要动 M18 冻结面 ⇒ **关窗前不动**）"
          "⇒ ★★★ 处置 = **披露 + 改进项**：★ 真修法（★ 按 stage 切换 pre24/pre7d 基线 ＋ 把 M11 基线纳入 M18 冻结）"
          "**登记为 Stage 2 之后** ⇒ ★★ ⇒ ★★ **Stage 1 报告读到 M11 行时，必须把它当「描述性读数」而不是独立判据**。")
    lines.append("   ㉦ ★★★★ **post 段必须被「当前时刻」界定（★ 否则 as-of 替换对它无效 ⇒ 负控/预演里的窗口不是 stage 窗口）**"
          "（★ 本轮第 24 处口径修正）：★ M11 的两个 fr CTE（M11_latency @fr#1、M11b @fr#2）**完全没有 `now()`**"
          "⇒ ★★ post 段 = **[切点, ∞)** ⇒ ★★ **无界** ⇒ ★★ **as-of 替换对它无效**"
          "⇒ ★★ ⇒ 第 23 处的几何对齐（★ `now()` 随 era 切点平移）**对 M11 不生效**"
          "⇒ ★★ ⇒ 负控/预演里的 M11 窗口**仍不是 stage 窗口** ⇒ ★★ ⇒ **负控证伪不了 M11**。"
          "⇒ ★★★ 实测（负控 era + as-of = 新切点+24h）：★ **无上界** ⇒ 13 ep / med **3** ⇒ **判 ✅**"
          "⇒ ★★ ⇒ **M11 在负控里是「恒真」的 ⇒ 它没有牙** ⇒ ★★ ⇒ Stage 1 的 M11 读数"
          "**区分不了「修复生效」与「修复没生效」**；★ ★ **有上界** ⇒ 9 ep / med **30** ⇒ **判 ❌**（★ 长出牙了）。"
          "⇒ ★★★ 修法：★ 采集侧两个 fr CTE 各加 `AND n.created_at <= now()`"
          "⇒ ★★ **真实读数逐字节不变**（★ now 时刻 = 「观测到的全部」）⇒ ★★ **pre 侧逐字节不变**（★ pre 全在切点前）"
          "⇒ ★★ ⇒ **M18 冻结面不受影响**（★ 不重冻）⇒ ★★ 并**点名要求**：★ 负控的 **Stage 1 专属**"
          "「必须判负」清单含 M11 namespaced ⇒ ★★ ⇒ ★★★ **纪律：★★ 凡是要做「时间轴平移重跑」的判据，"
          "其 post 段必须被「当前时刻」界定；★ 无界 ⇒ ★★ 平移对它无效 ⇒ ★★ 负控/预演对它**没有牙齿**。"
          "★★ 另注：★ 「必须判负」**只能是 Stage 1 专属**（★ Stage 2 的 168h 窗口**不含**那段退化"
          "⇒ 实测 M11 ns = 50 ep / med **3** ⇒ ✅ 达成 ⇒ ★★ 对 Stage 2 也要求就是**假红**；"
          "★ 且 **bare 行不作要求**（★ 该 24h pre-fix 窗口 bare 人群未退化）。")
    lines.append("   ㉩ ★★★★ **观测窗口已**第三次**跨构建（★ 11-10 19:01:04 / ★ 11-11 01:07:37 / ★ **11-11 02:06:59**）⇒ ★★ 三次都经「代码解耦 + **相位对齐的等长窗口实测**」⇒ ★★ era 切点（★ 11-10 13:42:30 +08）**仍有效 ⇒ 不重划**（★ 观察层事件）**"
          "★ **三次事件一览**：★ ① **19:01:04**（★ `8028459d`）★ ② **01:07:37**（★ `199c20a00`）★ ③ **02:06:59**（★ `7b926bfb9-dirty`）"
          "⇒ ★★ post 窗口共跨 **3 个构建** ⇒ ★★ ⇒ ★★ **「机制连续」的结论必须落到可复核的证据上**（★ 代码解耦的位置 + ★ 等长窗口实测）⇒ ★★ **不能只写「已核查」**。"
          "⇒ ★★★★ **★ 第二次（01:07:37）的证据（★ 已有）**：★ diff **碰了** featured 路径（★ `bg/featured_sources.go` **+90 行**、★ `bg/model_tier.go` **-13**、★ `bg/model_probe.go` **-14**）"
          "⇒ ★★ ⇒ 诚实边界 ㉕ 的「③ diff 不碰被测路径」这条**这次不成立** ⇒ ★★ 换证据：① ★★ **featured 集未变**（★ M2 static = **210** / M6 = **80·1067** / M9 缺口 = **0**）✔"
          "② ★★ **探针未中断**（★ 部署前后每分钟 **1–12** 条 node_probe_runs；★ 等长窗口全量 A = **61** vs B = **83**）✔；"
          "③ ★★ **diff 碰了 ⇒ 但可证「解耦」**：★ · `bg/model_tier.go` 的 SQL 体**逐字未变**（★ 纯「单一事实源化」重构）⇒ ★★ 探针分层口径（★ 210 集合）不变；"
          "★ · watchdog 改的是 `model_probe_state`（★ `bg/model_probe.go`）⇒ ★★ 而 **node_probe 模式不读该表**（★ `bg/node_probe.go:26-29` 注释 + `node_probe.go`/`probe_service.go` 里**没有** `FROM/JOIN model_probe_state` 的真实读取）⇒ ⇒ **被判定的读数（node_probe_runs）不受影响**；"
          "★ · ★★ **等长窗口实测**（★ 部署前后各 **15 分钟** ⇒ 同量纲）：全量探测量 A = 61 / B = 83（★ 没停）；★ newly 失败 A = **0** / B = **1**，且该 1 条是**减半独占值**（★ mul_evidence = 1、★ **top21600_n = 0**）"
          "⇒ ⇒ **没有出现「未减半 6h 顶格」** ⇒ ⇒ 诚实边界 ㉕ 的「tier 冷启动窗口 ⇒ 未减半顶格」这条指纹**当前读数下不成立**。"
          "⇒ ★★★★ **★★ 第三次（02:06:59）的事件与证据（★ 本轮新增，★ 2026-11-11 03:2x–03:3x +08）**："
          "★ 事件：★ 并行会话把构建 `211-7b926bfb-dirty`（★ commit `7b926bfb9` = ★ **tooldialect 工具参数流方言抽象工厂**）部署到 211"
          "（★ `StartedAt` = 2026-11-10T18:06:59Z = **11-11 02:06:59 +08**；★ 旧容器 `llm-gateway-211-a` **Exited (137)** = SIGKILL，★ 与 19:01 事件同款）⇒ ★★ post 窗口**第三次**跨构建。"
          "⇒ ★★ ★ **代码腿（★ 本轮最强的一腿）**：★ `git diff 199c20a00..7b926bfb -- bg/` = **空** ⇒ ★★ **探针源码逐字节未变**；"
          "★ 全部生产改动只在 `domains/streaming/`（★ anthropic_bridge + toolargs_dialect_wiring）、★ `internal/ir/stream.go`、★ 新包 `internal/tooldialect/`、★ `installer/`（★ CLI + migration 957）"
          "⇒ ★★ ⇒ ★★ **`bg/` 对 `internal/ir` 与 `internal/tooldialect` 零 import**（★ bg/ 里唯一的 “domains/streaming” 是**测试断言里的字符串字面量**，★ 不是 import）"
          "⇒ ★★ ⇒ ★★ 而 `ir.ParseAnthropicStreamEvent` **只被 `domains/streaming/responses_bridge.go` 调用**（★ 客户端流量路径）⇒ ★★ **探针层与被改层完全解耦**"
          "⇒ ★★ ⇒ ★★ ⇒ **被测口径（★ 探针分层用 210 集合 + `node_probe_runs` 的阶梯值）前后逐字未变 ⇒ era 切点语义不变**。"
          "⇒ ★★★★ **★★ 数据腿（★ 相位对齐的等长窗口，★ 三个各 15 分钟、距边界 6h / 1h / 0h）**："
          "★ W1（★ 11-10 20:06:59）= 全量 **89** / ns_fail **2** / newly_fail **1** / newly_top21600 **1**（★ 模型 = `claude/sonnet-5`）；"
          "★ W2（★ 11-11 01:06:59）= 全量 **83** / ns_fail **1** / newly_fail **1** / newly_top21600 **0**；"
          "★ W3（★ 11-11 02:06:59）= 全量 **66** / ns_fail **2** / newly_fail **2** / newly_top21600 **1**（★ 模型 = `claude/sonnet-5`）"
          "⇒ ★★ ⇒ ★★ **W1（部署前 6h）与 W3（部署后）形态同构**（★ 同量纲 + 同相位 + 同一只模型 + 同一失败形态）⇒ ★★ **部署未改变机制**。"
          "⇒ ★★★★ **★★ 形态腿（★ 定位到具体对象）**：★ 那条「未减半 6h 顶格」= **`claude/sonnet-5`**，★ `direct_http_status = 404` / `direct_err_code = http_404`"
          "⇒ ★★ ⇒ ★★ 按 M20 的豁免分类（★ `404/405/410/415` ⇒ **exempt**）⇒ ★★ **协议形态 ⇒ 设计上钉 6h ⇒ 乘数豁免 ⇒ 「未减半」是设计使然**，★ **不是** featured 状态翻转"
          "⇒ ★★ ⇒ ★★ 且它在 post 窗内出现在 **14:17 / 20:17 / 02:17**（★ **严格 6 小时一次**）⇒ ★★ 与部署时刻无关；★ pre 窗背景频率 = 每天 **1–75** 条 ⇒ ★★ **常态**。"
          "⇒ ★★★★ **★★ 必须披露的一处瑕疵（★ 诚实）**：★ 逐分钟探测量显示 **02:07 那一分钟为 0**（★ 部署瞬间的 **1 分钟停探**；★ 对照：01:07 那次**没有**缺口）"
          "⇒ ★★ ⇒ ★★ **已量化**：★ 1 分钟 / 1440 分钟 = **0.07%** ⇒ ★★ 远小于 ㉰ 的 6h 批次占比（★ **5.8%**）⇒ ★★ **不影响 24h 口径的分位数**，★ ★★ 但**必须写进报告**（★ 「探针未中断」这条不能写成「零中断」）。"
          "⇒ ★★★ **★★★ 两条新增纪律**："
          "★★ ① ★★ **「部署后出现 X」不能读成「部署引起 X」** ⇒ ★★ 必须找**部署前同相位**的对照（★ 本轮实测：★ 部署后 newly 出现 1 条「未减半 6h 顶格」⇒ ★★ 但部署前 **6h** 的同一 15 分钟窗里**同一只模型、同一形态**也出现了 ⇒ ★★ **与部署无关**）"
          "★★ ② ★★ **自己写的「相位对齐等长窗」也可能不等长** ⇒ ★★ 必须把**窗口长度打印出来**核对（★ 本轮自踩：★ 第一版「A 段」的 WHERE 跨了 75 分钟而 CASE 只在边界分段 ⇒ ★★ **「A 段」实际是 60 分钟** ⇒ ★★ 607 vs 66 是**窗口长度 + 批次覆盖**的产物 ⇒ ★★ **假象**）"
          "⇒ ★★★ **结论：★ era 切点（★ 11-10 13:42:30 +08）仍有效 ⇒ 不重划**（★ post 的定义 = 「探针分层用 210 集合」⇒ ★ 该口径前后逐字未变）。"
          "⇒ ★★★ **纪律**：★★ 「窗口中途换构建 ⇒ 核查机制连续性」的三条里，★ 第 ③ 条**不是**「diff 不碰被测路径」的同义词"
          "⇒ ★★ **diff 碰了 ≠ 机制变了**（★ 若被测链与被改路径**代码解耦** ⇒ 仍连续）；★ ★★ 且 ★★ **「跨构建次数多」也不是不连续的理由**（★ 连续性取决于**口径**，★ 不取决于构建数）。")
    lines.append("   ㉫ ★★★★ **同一类缺陷必须「穷举」而不是「抽查」⇒ ★★ 第 25 处：★ 除 M11 外还有 8 处同类无界窗口；★ 其中 M5 就是爆炸半径判据本身**"
          "（★ 本轮第 25 处口径修正）：★ 第 24 处是**抽查**发现的（★ 只看了 M11 vs M14 族）"
          "⇒ ★★ ⇒ 本轮做了**穷举审计**（★ 静态分析采集 SQL 的每个读数块）⇒ ★★ **又找出 8 处**同款「post 段无 `now()` 上界」："
          "★ ① `M5_probe_runs_24h` ★★ **判据**（★ 爆炸半径）② `M5_probe_runs_24h_by_kind` ★★ 该判据的「增量归因」分解"
          "③ `M5_post_runs_by_day` ★★ 该判据 ❌ 时的「post 逐日」披露 ④ `M12_recheck_` ⑤ `M12b_episodes_` ⑥ `M12c_episodes_`"
          "（★ ④⑤⑥ 是 era 型的参考项）⑦ `H4_probe_coverage_24h` ⑧ `H5_featured_probe_freq`（★ handoff 原始判据）"
          "⇒ ★★ ⇒ **全部补 `created_at <= now()` / `last_attempt_at <= now()`**（★ 真实读数逐字节不变 ⇒ ★★ M18 不受影响 ⇒ 不重冻）。"
          "⇒ ★★★★ **★ 实测这条缺陷有多贵（★ 真库负控、★ 同量纲对照）**：★ 负控 era（★ 切点前移 1 天、as-of = 新切点+24h）里"
          "★ **M5 无上界 ⇒ `M5_probe_runs_24h` = 14221**（★ 窗口 = [asof-24h, 真实now] = **~35h**）"
          "★ ★ **有上界 ⇒ 9245**（★ 窗口 = [asof-24h, asof] = **恰好 24h** ⇒ ★ 逐日桶 3656+5589 = 9245 完全吻合 ✔）"
          "⇒ ★★ **差 54%** ⇒ ★★ ⇒ 若 M5 进负控的「必须判负」清单 ⇒ ★★ **拿 14221 去比 9242 的基线 ⇒ 必然假红**"
          "⇒ ★★ ★ **且更隐蔽**：★ 变异版里同一个 M5 行「`total=14221`」与「逐日桶 `total=9245`」**互相矛盾**"
          "⇒ ★★ ⇒ **同一个判据行里的两个数字来自不同长度的窗口 ⇒ 披露自相矛盾 ⇒ 读者无法判读**。"
          "⇒ ★★★★ **★★ 最要命的一条（★ 真库负控实测）**：★ **修好窗口后，M5 在负控 era 里判 ✅ 达成**"
          "（★ 实测 `M5_probe_runs_24h` = **9245** ≤ limit **9334** ⇒ ★★ 「爆炸半径未爆」）"
          "⇒ ★★ ⇒ **M5 根本不是「必须判负」的判据**（★ 修复前的 24h 窗口探测量**本来就在预算内**）"
          "⇒ ★★ ⇒ ★★ **而窗口没修好时它读成 14221 ⇒ ❌ ⇒ 纯假红** ⇒ ★★ ⇒ **一个本来通过的判据会被伪装成「效应判据」**"
          "（★ 负控的职责恰恰是「证伪效应判据」⇒ ★★ 这个假红会让人以为「M5 是效应判据、负控已证伪它」"
          "⇒ ★★ **它会把「量具缺陷」误读成「机制效应」**）⇒ ★★ ⇒ ★★★ **M5 不得进负控的「必须判负」清单**"
          "（★ 它的 base 是 pre24 单日快照、★ 本身就脆 ⇒ ★★ 真修法仍是「≥7 天的均值/中位/分位做基线」⇒ 登记为 Stage 2 之后）。"
          "⇒ ★★★ **纪律**：★★ ① **同类缺陷必须穷举**（★ 修完一处要问「还有几处同款」）"
          "★ ② **穷举要落成工具**（★ `scripts/audit/recovery-detection-p0-3-post-bound-audit.py` ⇒ ★★ 静态、不连库、★ **变异可验证**）"
          "★ ③ **判据的裁决数与它的归因/披露数必须同窗口**（★ 否则同一行自相矛盾）"
          "★ ④ ★ **假阳性也要治**：★ 本轮首版审计器把 `M4b_free_gap_gt24h` 误报成违例"
          "（★ 它的「24h」是**时延阈值**不是窗口长度、★ 读的是快照表）⇒ ★★ ⇒ 「窗口型读数」的判定必须基于"
          "**事件时间列白名单 ＋ now() 锚定的下界**，★ 而不能靠键名里的「24h」。")
    lines.append("   ㉭ ★★★★ **分钟型量被 `::int` 截到分钟后又与阈值做「精确比较」⇒ ★★ 亚分钟差被放大成「1 单位假红」**（★ 第 26 处口径修正，★ 真库实测）"
          "：★ **形态**：★ `recheck_min` / `lat` 是**浮点分钟**（★ 实测精度到**秒**）"
          "⇒ ★★ 采集侧 `percentile_disc(...)::int` 把它**截到分钟** ⇒ ★★ 而判据拿这个**取整值**与阈值做**精确比较**"
          "⇒ ★★ ⇒ 真实差只要落在 **[−0.5, +0.5) 分钟** 的取整跨界带里 ⇒ ★★ **裁决符号就会翻转**。"
          "⇒ ★★★★ **实测（★ 211 真库，★ M20 豁免组）**：★ post raw p90 = **360.5069** min ⇒ `::int` = **361**"
          "⇒ ★★ 而基线取整值 = **360** ⇒ 基线真值 ∈ [359.5, 360.5) ⇒ ★★ ⇒ **真实差 ∈ (0.01, 1.01] 分钟 ⇒ 上界 ≈ 60 秒（★ = 判据的整个分辨率）**"
          "⇒ ★★ ★★ **更锋利的实测（★ 同一份诊断 SQL 同时读到 pre24 的 raw）**：★ pre24 **raw** p90 = **360.2095**"
          "⇒ ★★ ⇒ **实测真实差 = 0.2974 分钟 = 17.8 秒** ⇒ ★★ ⇒ **比判据的 1 分钟分辨率细 ~3.4 倍**"
          "⇒ ★★ ⇒ ★★★ **那个 ❌ 几乎就是噪声**（★ 诊断 SQL：`scripts/audit/recovery-detection-p0-3-m26-rounding-raw.sql` ⇒ 可复核）"
          "⇒ ★★ ⇒ 该行判 **❌** ⇒ ★★ **纯假红**（★ 慢事件分量 100% ≤ 109.6% **是通过的** ⇒ ❌ 只由 p90 一项造成）。"
          "⇒ ★★ **同款形态第 23 处已在 M14/M14x 的 `p99 361 vs 360` 上出现** ⇒ ★★ **同一类缺陷、同一机制**。"
          "⇒ ★★★★ **穷举结论（★ 按第 25 处纪律，不抽查）**：★ 判据族里**只有「分钟型分位数」这一族**受影响 ⇒ "
          "★ ① M14 / M14x 旧 featured（★ p90 / p99 vs pre）② M15x normal（★ med ≤ 30）③ M20 normal / exempt（★ p90_gap）；"
          "⇒ ★★ ★ **阶梯类不适用**（★ `next_retry_seconds` 是**整秒** ⇒ `::int` 无损）"
          "⇒ ★★ ★ **计数/百分比类不适用**（★ M5 total、H4 coverage、M12p pending_pct ⇒ 本来就是整数或一位小数）"
          "⇒ ★★ ★ **M11 是描述性读数**（★ 诚实边界 ㉚ 已声明其量纲问题）⇒ ★★ 本轮**不**给 M11 追加 raw。"
          "⇒ ★★★★ **处置（★ 只披露、不软化）**：★ ① **采集侧只给 post 行**追加 `*_raw` 字段"
          "（★ `CASE WHEN win='post'` ⇒ ★★ **pre 键逐字节不变** ⇒ ★★ **M18 不受影响 ⇒ 不重冻** ⇒ ★★ 时钟门的 pre 键逐字节对拍也不变 ✔）"
          "⇒ ★★ ② **判定器行内披露**（★ `rounding_disclosure()` ⇒ ★★ 给出**真实差的区间上界**）"
          "⇒ ★★ ③ ★ **裁决符号保持 ❌**（★ 门不钝化）⇒ ★★ ④ 真修法（★ **亚分钟精度比较 + 容差**；★ 且对"
          "「**设计上钉住**」的组（★ 豁免组钉 6h）⇒ 正确形态是**带宽判据**）**登记为 Stage 2 之后**。"
          "⇒ ★★★★ **★★ 关窗风险实测**：★ M15x 金丝雀 `deepseek-ai/deepseek-v4-pro-0813` 的 normal med raw = **30.0636**"
          "（★ 阈值 = 30 ⇒ 现在判 ✅）⇒ ★★ **只差 3.8 秒** ⇒ ★★ 关窗时 post 样本增加 ⇒ med 可能在 30.0–30.5 之间摆"
          "⇒ ★★ ⇒ **可能从 ✅ 翻成 ❌** ⇒ ★★ ⇒ **行内披露是关窗报告的必需品，不是装饰**。"
          "⇒ ★★★ **纪律**：★★ ① **判据的分辨率必须 ≥ 被测机制的抖动幅度**（★ 本例：分辨率 1 分钟 vs 抖动秒级 ⇒ 粗 ~60 倍）"
          "★ ② ★ **凡是比较「连续量」，必须先问「取整后还剩多少分辨率」**（★ 取整前先量分布，取整后再看判据）")
    lines.append("   ㉰ ★★★★ **分位数是「秩统计量」⇒ ★★ 样本量小的时候极不稳定 ⇒ ★★ 窗口未满时的读数 ≠ 关窗读数**（★ 第 26 处附带发现，★ 真库实测）"
          "：★ **实测形态**：★ M14x 旧 featured 行在 **2026-11-11 02:00 与 02:20 两次真读里都判 ❌**"
          "（★ post n=**34** / p90=**360** vs pre p90=**31**）⇒ ★★ 而**同一时刻的预演（as-of = 关窗时钟）判 ✅**"
          "⇒ ★★ ⇒ ★★ 差别**不在数据**（★ 同一批数据）⇒ ★★ **在成熟度窗口的大小**"
          "⇒ ★★ post 的成熟段此时只有 ~6.6h（★ 真 now 时 post 只结算到 now−6h）"
          "⇒ ★★ ⇒ ★★ 根因 = **秩统计量 + 样本量**：★ p90 = 第 ⌈0.9n⌉ 小的那个值 ⇒ ★ n 小 ⇒ 秩边界一动就跳档"
          "（★ 实测：★ n=34 ⇒ p90 秩 = 第 **32** 位；★ 只差 **1** 条新事件就可能从 31 跳到 360）"
          "⇒ ★★★★ **★★ 机理（★ 本轮逐层实测，★ 数字可复核）**：★ M14x 旧 featured 组的间隔分布实测为"
          "★ ① **pre24**（★ 成熟段 18h）= n **226** ＝ 短事件 **208** ＋ **6h 事件 18**（★ 占 **8.0%**）⇒ p90 = **31**"
          "★ ② **post @真实 now**（★ 成熟段仅 ~6.8h）= n **35** ＝ 短事件 **29** ＋ **6h 事件 6**（★ 占 **17.1%**）⇒ p90 = **360** ❌"
          "★ ③ **post @as-of 13:42:30**（★ 成熟段 ~12.8h）= n **103** ＝ 短事件 **97** ＋ **6h 事件 6**（★ 占 **5.8%**）⇒ p90 = **31** ✅"
          "⇒ ★★★ ⇒ ★★★ **★ 关键机理**：★ **6h 事件数是「每批次固定」的**（★ 两个窗口都是 6 条 ⇒ 它**不随窗口长度增长**）"
          "⇒ ★★ 而**短事件随窗口线性增长**（★ 35 → 103 ⇒ 短事件 29 → 97）"
          "⇒ ★★ ⇒ ★★ ⇒ **6h 占比 = 固定数 /（固定数 + 线性增长）⇒ ★★ 窗口越短 ⇒ 占比越高 ⇒ ★★ 必然把 p90 顶到 360**"
          "⇒ ★★ ⇒ ★★ ⇒ **窗口未满时的读数不能代表关窗读数**（★ 与 ㉗ 同源：★ 预演只投影时钟、不投影数据积累）"
          "⇒ ★★★★ **★★ 关窗预期（★ 可证伪的预测，★ 依据上面 ①②③）**：★ Stage 1 关窗 = 13:42:30"
          "⇒ ★★ ⇒ 成熟段 **~18h** ⇒ ★★ ⇒ post n 将 **> 103**、6h 占比将 **< 5.8%** ⇒ ★★ ⇒ **p90 应回到 31 ⇒ ✅ 达成**"
          "⇒ ★★ ★ **★ 若关窗当天它仍判 ❌ ⇒ 必须按「真实回归」严肃对待**（★ 不得再用本边界解释掉）"
          "⇒ ★★ ★★ **★ 推论（★ 关窗纪律）**：★ **预演（as-of = 关窗时钟）比「真实 now」更接近关窗读数**"
          "⇒ ★★ ⇒ ★★ ⇒ 「T1 关窗判读清单」里的**预演那一步不是形式**（★ 它是关窗读数的**几何+成熟度**预演）"
          "⇒ ★★★★ **处置**：★ **只披露，不改裁决符号**（★ ❌ 仍是 ❌ ⇒ 门不钝化）"
          "⇒ ★★ 判定器行内给出「post n vs pre n 的样本量倍数」⇒ ★★ 读者自行判断这条 ❌ 有多脆"
          "⇒ ★★★ **纪律**：★★ ① **跨窗比分位数时，必须同时报两边的 n**（★ 只报分位数不报 n ⇒ 读者无法判断脆不脆）"
          "★ ② ★ **窗口未满时的裁决符号会摇摆**（★ 本轮实测：★ 同一批数据，真实 now ❌ / 预演 ✅）"
          "★ ⇒ ★★ **一切以关窗当天真读为准**（★ 窗口越满 ⇒ n 越大 ⇒ 越稳）")
    lines.append("   ㉲ ★★★★ **审计器自己也要被审计 ⇒ ★★ 判据的守卫必须报告自己的覆盖率 ⇒ ★★ 且每个「窗口」必须**逐个**判定 ⇒ ★★ 否则同语句的另一个窗口会替它背书 ⇒ ★★ 结构性假阴性**（★ 第 27 处口径修正，★ 真库实测）"
          "：★ **形态**：★ 第 25 处造的穷举审计器 v1 的键提取只认「`SELECT` 后**紧跟**字面量」⇒ ★★ 而 **M14/M14x 块的键是 CASE 表达式**"
          "（★ `SELECT CASE WHEN scope='all' THEN 'M14x_' ELSE 'M14_' END || win || '_' || grp_new k`）⇒ ★★ **整个主判据块（★ 11413 字符）对审计器完全不可见**"
          "⇒ ★★ ⇒ ★★ v1 报「全部合规」时，★ **它对主判据的覆盖率 = 0%** ⇒ ★★ ⇒ ★★ **「全部合规」这句话必须附带覆盖率**（★ 否则读者无法判断它覆盖了什么）"
          "⇒ ★★★★ **★★ 四个假阴性来源（★ 本轮逐个实测 ⇒ 每个都用变异验证钉死）**："
          "① ★ **键提取只认字面量** ⇒ ★ CASE 键（M14/M14x）不可见 ⇒ ★★ 修法：★ 读顶层 SELECT 的**首个输出表达式**（★ 不要求它是字面量）"
          "② ★ **post 判定靠键名前缀** ⇒ ★ 键由 `era`/`win` 拼出的块（M15x / M12p）被判非 post ⇒ ★★ 修法：★ 结构化三路（★ 键含 post 标记 / 键引用窗口列且块内有 post 取值 / 时间轴在切点处被分桶）"
          "③ ★ **era 取值词表漏了 `post_fix`** ⇒ ★ `'post_fix'` 里**不含** `'post'`（★ 后面是 `_` 不是引号）⇒ ★★ **M11 的 post 段从未被检查** ⇒ ★★ 修法：★ 词表按真实取值穷举"
          "④ ★★ **窗口粒度太粗** ⇒ ★★ 同一条语句里的**另一个**窗口会替它背书：★ ① **输出分支级**（★ M14/M20 的 `UNION ALL` 分支）② **CTE 体级**（★ M16 的 fr_used / fr_ref / fr_legacy = 三个独立窗口）③ **标量子查询级**（★ M20 的 `tail_n` ⇒ `[now-6h, ∞)`）⇒ ★★ ⇒ ★★ 修法：★ **逐分支 / 逐 CTE / 逐子查询**判定"
          "★ ★★ ★ **CASE 内的分桶条件不是行过滤**：★ era 切分写成 `CASE WHEN created_at < 切点 THEN 'pre' ELSE 'post' END` ⇒ ★★ 里面的 `created_at < 切点` **不是上界** ⇒ ★★ 若把它当上界 ⇒ ★★ `M8_ns_` 会被误判成「窗口闭合」⇒ ★★ **假阴性**"
          "⇒ ★★★★ **★★ 采集侧真缺口（★ v1 从未看见 ⇒ 因为它看不见那些块）**：★ `M3_feat_*`（★ 窗口 = **[CUT-24h, ∞)**）★ `M8_ns_*`（★ post 段 = **[CUT, ∞)**）★ `M16` / `M17`（★ 只有下界）"
          "★ ★★ **`M20` 的 fp 指纹视图 post 段 = [CUT, ∞)**（★ 「**无成熟度过滤**」≠「**无上界**」⇒ ★★ as-of 替换对它**完全无效** ⇒ ★★ 负控里的 fp 窗口比 Stage 1 更长 ⇒ ★★ **几何不一致**）"
          "★ ★★ **`M20` 的 `tail_n` 子查询 = [now-6h, ∞)** ⇒ ★★ ⇒ ★★ **「fp ≥ 窗口+尾部」这个不变量此前是「恒真」的**（★ 两个视图都无界 ⇒ 两边都 = [CUT, 真实now] ⇒ 相等）⇒ ★★ ⇒ ★★ **守卫在缺陷所在处没有牙**（★ 盲区正在缺陷处）⇒ ★★ 补界后恢复为「真不变量」（★ 且**牙齿恢复**：★ 若 fp 被改回带成熟度过滤 ⇒ fp = window < window+tail ⇒ ★★ 报警）"
          "⇒ ★★★ **★ 处置**：★ 采集侧 6 处补界**只补上界、不动下界与阈值** ⇒ ★★ **同事务双跑对拍：259 键逐字节 0 差异** ⇒ ★★ **M18 不受影响 ⇒ 不重冻**（★ M20 的 fp 视图**只给 post 分支**补界 ⇒ ★★ pre 侧仍时钟无关 ⇒ ★★ 三时钟门逐字节一致不受影响）"
          "⇒ ★★★ **纪律**：★★ ① **任何「全部合规 / 无违例」的结论必须附带它检查了哪些东西（★ 覆盖率）**"
          "★★ ② ★ **守卫必须能看见自己瞎了**（★ 覆盖率缺口 ⇒ **rc≠0**）"
          "★★ ③ ★ **窗口 = 最小独立读数单元** ⇒ ★★ 判上界必须**逐单元**判定 ⇒ ★★ 「同语句别的窗口替它背书」是**结构性假阴性**"
          "★★ ④ ★ **不变量必须验证它不是「恒真」**（★ 两个视图都无界 ⇒ 等式恒成立 ⇒ 守卫无牙 ⇒ ★ 问「**它要防的缺陷真的存在时，它会红吗？**」）")
    return lines


def print_honest_boundaries() -> None:
    """★ 打印诚实边界（★ 唯一来源 ⇒ 自测可**直接断言打印出来的文本** ⇒ 披露不会被静默删掉）"""
    print("\n★ 诚实边界（必须一起写进结论）：")
    for _line in honest_boundary_lines():
        print(_line)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--ssh-host", default="211")
    ap.add_argument("--stage", type=int, default=1, choices=(1, 2))
    ap.add_argument("--inject-guard-fail", action="store_true",
                    help="★ 自测用：把 M16 守卫改成失败 ⇒ 应看到整表裁决作废")
    ap.add_argument("--from-file", help="★ 自测/离线用：读本地文件作为复采输出（不连 211）")
    ap.add_argument("--selftest", action="store_true",
                    help="★ 自测用：合成数据跑判定链路（不连 211）")
    args = ap.parse_args()

    if args.selftest:
        return selftest()

    if args.from_file:
        print(f"== 复采（离线：{args.from_file}）==", flush=True)
        raw = Path(args.from_file).read_text(encoding="utf-8")
    else:
        print(f"== 复采（ssh {args.ssh_host} → acc-pg17）==", flush=True)
        raw = run_metrics(args.ssh_host)
    data = parse(raw)
    print(f"   指标条数：{len(data)}")

    rows, bad, guards = judge(data, stage=args.stage,
                              inject_guard_fail=args.inject_guard_fail)

    print("\n== 量具守卫（★ 守卫不过 ⇒ 下列所有裁决作废）==")
    for name, val in guards:
        failed = ("★★" in val) or (not val.strip())
        mark = "★★" if failed else "✔"
        note = "" if val.strip() else "（★ 守卫指标缺失 ⇒ 量具不可信）"
        print(f"   {mark} {name}: {val}{note}")
    if bad:
        print("\n★★ **量具守卫未通过 ⇒ 全部读数不可信 ⇒ 立即修口径，勿采信下面的裁决表**")

    # M15 逐对明细（★ 便于人工逐只看幅度）
    # ★★ 同量纲窗口（★ 与 judge() 里的 `win` 同义，★ 这里是 main() 的作用域）
    win = "pre24" if args.stage == 1 else "pre7d"
    print("\n== M15 逐对明细 ==")
    print("   ★ M15 = **免费面切片**（★ 旧口径）｜★ M15x = **全计费口径·豁免/非豁免分层**（★ 本轮第 13 处口径修正）")
    print("   ★★ **两个口径结论相反**（★ 实测）：★ free 切片下四只**同形**（pre7d 各 12 豁免 + 1 normal）"
          "⇒ ★ 剔豁免后样本不足 ⇒ **观察面**；★ 全计费口径下只有 deepseek 的 normal 够样（41/18 条）⇒ **判据**")
    for pair in CANARIES:
        pre = data.get(f"M15_pre__{pair}")
        post = data.get(f"M15_post__{pair}")
        pre_med = field(pre, "med_min", "med") if pre else None
        post_n = head_count(post) if post else None
        post_med = field(post, "med_min", "med") if post else None
        print(f"   [免费面·观察面] {pair}: pre med={pre_med} (n={head_count(pre) if pre else '?'})"
              f" → post med={post_med} (n={post_n})")
        px = data.get(f"M15x_{win}__{pair}")
        qx = data.get(f"M15x_post__{pair}")
        if not px:
            continue
        ex_n = field(px, "exempt") or 0
        no_n = field(px, "normal") or 0
        p_med = field(px, "normal_med_min")
        p_lad = field(px, "normal_ladder_p50_s")
        p_med_s = f"{p_med:.0f}" if p_med is not None else "?"
        p_lad_s = f"{p_lad:.0f}" if p_lad is not None else "?"
        if qx:
            q_no = field(qx, "normal") or 0
            print(f"   [全计费·分层 ] {pair}: pre total={head_count(px)} exempt={ex_n:.0f} normal={no_n:.0f}"
                  f" normal_med={p_med_s}min ladder_p50={p_lad_s}s"
                  f" → post total={head_count(qx)} normal={q_no:.0f}"
                  f" normal_med={field(qx, 'normal_med_min')}min"
                  f" ladder_p50={field(qx, 'normal_ladder_p50_s')}s")
        else:
            print(f"   [全计费·分层 ] {pair}: pre total={head_count(px)} exempt={ex_n:.0f} normal={no_n:.0f}"
                  f" normal_med={p_med_s}min ladder_p50={p_lad_s}s → post 段无事件")

    if args.stage == 2:
        print("★ Stage 2（7 天窗口）：M11 episode 层与 M15 金丝雀为**主判据**"
              "（★ Stage 1 的「样本不足」项在此应全部可判）；"
              "★★ **M20 normal（★ 效应主判据）**在此窗口样本充足（pre7d n=154 ⇒ post7d ≈154）"
              "⇒ ★★ 这才是**正式判定**（★ Stage 1 的 24h 窗口只有 ≈21 条 ⇒ 只能给方向）。")
    else:
        print("★ Stage 1（24h 窗口）判据集：★ 机制面（M3b）+ 资格/覆盖面（M2/M6/M9）"
              "+ 成本面（M5）+ ★★ **M14 三组**（population-stable：无回归 / 对照）"
              "+ ★★ **M20 分层**（★ 效应主判据 + 不回归守卫）+ M12p 护栏（待复检率）；"
              "★ M11 若 post episode ≥5 即可判（≈7/日）；"
              "★★ **M15x 逐对**（★ 全计费口径·分层）：★ free 切片下四只**同形**（豁免主导 ⇒ 剔豁免后仅 0~1 条）"
              "⇒ ★★ **四只全是观察面**；★ 全计费口径下**只有 deepseek 的 normal 够样**"
              "（pre24 18 条 / pre7d 41 条）⇒ ★★ 逐对判据**只落在它一只**；"
              "★ M12s 两行是**参考**（post 人群会变 ⇒ 不可归因）。")
        print("★★★ ★★ **Stage 1 的预期边界（★ 别把「样本不足」写成「修复无效」）**："
              "★★ **效应判据已从「M14x 混合口径」迁到「M20 分层」**（★ 本轮第 11 处口径修正）："
              "· ★★ **混合口径（M14/M14x 的新覆盖行）已降为「观察面」**：★ 实测该人群 **78%** 的失败是"
              "**豁免类**（协议形态 ⇒ 设计上钉 6h ⇒ **豁免 featured 乘数**）⇒ ★ post 的 p90/slow"
              "**结构性不可达** ⇒ ★★ 拿它判效应 ⇒ **必然假阴性**（且会被读成「修复没生效」）；"
              "· ★ **M20 normal（★ 效应主判据）**：★ 「未减半 6h 顶格」清零（top21600_n == 0）"
              "★ 且 慢事件占比 ≤ pre 的一半 ⇒ ★ 24h 实测样本 ≈21 条（**薄**）⇒ ★ Stage 1 只给**方向**，"
              "★ 正式判定留 Stage 2（pre7d n=154）；"
              "· ★ **M20 exempt（★ 不回归守卫）**：★ 豁免类**设计上钉 6h** ⇒ 只防「变慢」，不防「变快」；"
              "⇒ ★★ Stage 1 能给的结论是：**机制已生效 + 资格/成本达标 + 旧 featured 人群无回归"
              "+ 对照组未变 + 新覆盖人群「该减半的那部分」阶梯已减半（M20 normal）**；"
              "★ **仍不得写成「修复已生效」**（★ 免费面可用率不可作判据 ⇒ 全局症状未闭环），"
              "★ **逐对幅度留 Stage 2**（★ pre7d 461 vs post7d ≈450，★ 同量纲且样本充足）。")
    print(f"\n== 裁决表（Stage {args.stage}；era 切点 {EPOCH}）==")
    w = [34, 34, 46, 62]
    print(" | ".join(h.ljust(w[i]) for i, h in enumerate(["判据", "基线", "观测", "裁决"])))
    print("-" * (sum(w) + 12))
    for r in rows:
        cells = [str(c) for c in r]
        print(" | ".join(cells[i].ljust(w[i]) for i in range(len(cells))))

    # ── ★★ handoff 原样/修正版读数（★ 用 handoff 自己的语言回答它的问题）──
    print("\n== handoff 口径读数（★ 原样 SQL 不可执行 ⇒ 这里用修正版；★ 参考读数，不是判据）==")
    handoff_rows = [
        ("H1 unavailable_count（handoff 指标 1）", data.get("H1_unavailable_count", "—"),
         "★ 瞬时量 + 归因不可分 ⇒ 不可作判据（handoff 目标「下降 ≥20%」不可用）"),
        ("H2 免费可用率（handoff 指标 2）", data.get("H2_free_available_pct", "—"),
         "★ 恒真（修复前即 72.4% ≫ 40%）⇒ 不可作判据"),
        ("H3 探测间隔 p50/p95/p99（handoff 指标 3）", data.get("H3_probe_interval", "—"),
         "★ p95 被 30 天 healthy-parked 行主导 ⇒ 不可作判据（handoff 目标「<60min」差 300×）"),
        ("H4 探测覆盖率 24h（handoff 指标 4）", data.get("H4_probe_coverage_24h", "—"),
         "★ 恒真（修复前即 >70%）⇒ 不可作判据"),
    ]
    for label, obs, note in handoff_rows:
        print(f"   {label}: {obs}")
        print(f"      ⇒ {note}")
    h5 = sorted((k, v) for k, v in data.items() if k.startswith("H5_featured_probe_freq"))
    if h5:
        print("   H5 featured 真实高频模型（handoff 任务 2.2-3 的修正版；★ 原样用幻觉模型名 ⇒ 必然 0 行）:")
        for k, v in h5[:5]:
            print(f"      - {v}")

    print_honest_boundaries()
    # ★★★ **守卫不过 ⇒ 退出码必须非 0**（★ 本轮第 19 处修正，★★ 又一个「静默失效」）
    #   ★★ 形态：★ 守卫失败时脚本只**打印**「★★ 量具守卫未通过」⇒ ★ 但 `return 0`
    #   ★★ ⇒ ★★ 自动化/流水线只看 rc ⇒ ★★ 守卫红着也当「通过」⇒ ★★ **假绿**
    #   ★★ ⇒ ★★ 纪律：★ **守卫是判据的牙齿** ⇒ ★ 牙掉了，rc 必须跟着红。
    if bad:
        print(f"\n★★ **退出码 1**：★ 量具守卫未通过（{len(bad)} 项）⇒ ★ 本次裁决表**作废**")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
