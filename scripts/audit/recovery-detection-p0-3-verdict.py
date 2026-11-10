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
    txt = (f"★ 基线口径：★ M5 的 base = **pre24 单日快照**（9242）⇒ ★ 而探测量**按天波动**"
           f"（★ pre7d 逐日（★ 按**日历日**切 ⇒ 7 天窗口跨 8 个日历日）min={dmin} / max={dmax}"
           f" ⇒ **{ratio} 差距**；★ n_days={nd}）"
           f"⇒ ★★ **pre 期已有 {over}/{total_days} 个日历日超预算 {lim}** ⇒ ★★ 该阈值**在 pre 期就不成立**"
           f"⇒ ★★ 故本行 ❌ **不可归因于修复**（★ post 逐日：{post_days}）")
    return txt


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
        rows.append((label_x, base_txt, post_x, verdict(ok, note)))

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
                rows.append((label, base_txt, obs,
                             verdict(ok, f"★ post p90={p90} / p99={p99}（pre {pre[1]}/{pre[2]}）"
                                       f"⇒ 旧 featured 人群{'尾部未变差' if ok else '★ 尾部变差（无回归失败）'}")))
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
            rows.append((label, base_txt, obs_txt,
                         verdict(ok, f"★ 【指纹·fp 视图（★ 无成熟度过滤 ⇒ 尾部不被截断）】{fp_txt}"
                                   f" / ★ 【slow·成熟视图（★ 需 6h 结算）】{slow_txt}{p90_txt}"
                                   f" / ★ 【摇摆（★ featured 状态稳定性）】{swing_txt}"
                                   f"（★ ★ 两分量**跨度不同**：★ 指纹覆盖到「读数时刻」，"
                                   f"★ slow 覆盖到「读数时刻 − 6h」⇒ ★ 行内数字不可直接相加）"
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
            # ★★ 附「fp 口径」的对照读数（★ 豁免类**设计上钉 6h** ⇒ 未减半顶格应当**仍在**）
            fctl = ""
            if fobs is not None:
                _ftop = field(fobs, "top21600_n")
                if _ftop is not None:
                    fctl = (f" ★（★ 对照：豁免类在 fp 口径的未减半 6h 顶格 {_ftop:.0f} 条"
                            f"（★ 设计上钉 6h ⇒ **期望 >0** ⇒ 若为 0 ⇒ 说明分层或机制变了））")
            rows.append((label, base_txt, obs,
                         verdict(ok, f"★ 豁免类（★ 协议形态：设计上钉 6h、乘数豁免）post p90_gap={p90:.0f}"
                                   f"（期望 ≤{pre_p90}）/ 慢事件 {slow}%（期望 ≤{lim_slow}%；★ {pre_slow_src}：{pre_slow_eff}%）"
                                   f" ⇒ {'未变慢（不回归成立）' if ok else '★ 豁免类被拖慢'}"
                                   f"（★ n={n}）{fctl}{contam_txt}")))

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
            rows.append((label, base_txt, f"n={n} med={med} p90={p90} max={mx}",
                         verdict(ok, "★ 逐项不劣于 pre 即达成")))

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
    if "M5" not in hb or "基线口径" not in hb:
        failures.append("[㉠ 诚实边界] 缺少「M5 的 ❌ 不可归因于修复（阈值取自 pre24 单日快照）」边界")

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
    print("   ✔ ㉟ ★★★ M5 判 ❌ 时必须给出「基线口径」披露（★ pre 期已超预算 ⇒ ❌ 不可归因于修复；★ 符号不得软化）")
    print("   ✔ ㉠ ★★ 诚实边界必须含「M5 的 ❌ 不可归因于修复（阈值取自 pre24 单日快照）」一条")
    print("   ✔ ㉡ ★★★ M5 判 ❌ 时必须给出「增量归因（by_kind）」分解（★ 回答「增量从哪来」）"
          "（★ 与 ㉟ 的「基线口径」互补：★ 那边答「阈值是否成立」、★ 这边答「增量从哪来」）")
    print("   ✔ ㉣ ★★★ 诚实边界必须含「负控窗口几何必须与被验收窗口一致」+「原负控的旧 featured ❌ 是假红」")
    print("   ✔ ㉤ ★★★ **跨脚本守卫**：★ 负控脚本必须真的把 `now()` 随 era 切点平移（★ 否则门没牙）")
    print("   ✔ ㉥ ★★★ 诚实边界必须含「M11 的基线不同量纲（★ Stage 1 的 M11 符号两个方向都可能错）」")
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
          "⇒ ★★ **纪律：★ 凡判据有多个分量，先逐分量问一句「这个量需要等结算吗」；"
          "★ 需要与不需要的，必须分视图读，否则尾部会静默失明。**")
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
    lines.append("   ㉘ ★★★★ **M5 的 ❌ 不可归因于修复（★ 阈值取自 pre24 的「单日快照」）**："
          "★ M5 的 base = **9242** = **pre24 单日快照**（★ 实测 pre_24h_count = 9243）"
          "⇒ ★ 而探测量**按天剧烈波动**（★ 实测 pre7d 逐日 min=**1292** / max=**9905** ⇒ **7.7×**）"
          "⇒ ★★ **pre 期实测 11-09 = 9685 已超 limit 9334** ⇒ ★★ ⇒ **该阈值在 pre 期就不成立**"
          "⇒ ★★ ⇒ M5 判 ❌ 时**不得**被读成「修复引起爆炸半径」⇒ ★★ 本轮只做**行内披露**"
          "（★ **裁决符号保持 ❌** ⇒ ★ 门不钝化；★ 且逐日分布键缺失时必须显式写「基线口径不可见」）"
          "⇒ ★★★ **真正的修法**（★ 用 ≥7 天的均值/中位/分位做基线，而不是单日快照）"
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
