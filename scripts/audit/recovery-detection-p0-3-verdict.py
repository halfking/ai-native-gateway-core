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
    #      vs pre7d slow=10.3%（447 条））⇒ ★★ 借用另一窗口的基线 = 判错
    "M14_old_feat_pre24": {"base": (3, 31, 360), "expect": "<=", "units": "med/p90/p99",
                            "slow": 6.4, "note": "旧 featured 人群（★ Stage 1 无回归基线，24h）"},
    "M14_old_feat_pre7d": {"base": (3, 120, 1791), "expect": "<=", "units": "med/p90/p99",
                            "slow": 8.7, "note": "旧 featured 人群（★ Stage 2 无回归基线，7 天）"},
    "M14_newly_covered_pre24": {"base": (360, 360, 360), "expect": "<=", "units": "med/p90/p99",
                         "slow": 100.0, "post_p90_max": 180,
                         "note": "新覆盖人群（★ Stage 1 效应基线，24h；★ **样本仅 14 条 ⇒ 不足** ⇒ 幅度留 Stage 2）"},
    "M14_newly_covered_pre7d": {"base": (2, 360, 1390), "expect": "<=", "units": "med/p90/p99",
                         "slow": 10.3, "post_p90_max": 180,
                         "note": "新覆盖人群（★ **效应判据**：pre p90=360 → post 期望 ≤180）"},
    "M14_other_pre24": {"base": (360, 360, 360), "expect": "-", "units": "med/p90/p99",
                         "slow": 77.2, "tag": "★ 对照项：节奏不该变",
                         "note": "对照组（★ Stage 1 基线，24h）"},
    "M14_other_pre7d": {"base": (360, 369, 5524), "expect": "-", "units": "med/p90/p99",
                         "slow": 88.4, "tag": "★ 对照项：节奏不该变",
                         "note": "对照组（★ Stage 2 基线，7 天）"},
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
            "docker exec acc-pg17 psql -U acc_super -d llm_gateway -tA -F'|' -f /tmp/m.sql"
        )
        out = subprocess.run(["ssh", host, cmd], capture_output=True, text=True)
        if out.returncode != 0:
            sys.stderr.write(out.stderr)
            raise SystemExit("验收脚本执行失败")
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
# 判定主体（★ 纯函数：输入 = 复采结果字典，输出 = 裁决行 + 守卫状态）
# ─────────────────────────────────────────────────────────────────────
def judge(data: dict[str, str], stage: int = 1,
          inject_guard_fail: bool = False) -> tuple[list[tuple[str, str, str, str]],
                                                    list[str], list[tuple[str, str]]]:
    # ── ★★ 先看量具守卫：守卫不过 ⇒ **整张裁决表作废** ──────────────
    guards = []
    g16 = data.get("M16_join_caliber_check", "")
    g17 = data.get("M17_event_definition_check", "")
    guards.append(("M16 绑定 join 口径", g16))
    guards.append(("M17 事件定义", g17))
    if inject_guard_fail:
        # ★ 自测：模拟守卫失败（★ 只改判定，不改采集）
        g16 = "used=13563 ref=99999 | ★★ **量具漂移（注入）**"
        guards[0] = ("M16 绑定 join 口径", g16)
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
            rows.append((label, base_cell(key), obs,
                         verdict(ok, f"★ 爆炸半径未爆（{n} ≤ {lim}）")))

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
                         verdict(None, "★ post 段无事件（尚未结算）")))
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

    # ── M15：三个冷金丝雀逐对 ────────────────────────────────────────
    for pair in CANARIES:
        pre = data.get(f"M15_pre__{pair}")
        post = data.get(f"M15_post__{pair}")
        pre_med = field(pre, "med_min", "med") if pre else None
        post_n = head_count(post) if post else None
        post_med = field(post, "med_min", "med") if post else None
        if post_med is None:
            verdict_txt = verdict(None, "★ post 尚无已结算事件")
        elif post_n is not None and post_n < 5:
            verdict_txt = verdict(None, f"★ post n={post_n} < 5 ⇒ 幅度不足（方向可读）")
        else:
            ok = post_med <= CANARY_POST_MAX_MED
            verdict_txt = verdict(ok, f"post med={post_med}（期望 ≤{CANARY_POST_MAX_MED}）")
        rows.append((f"M15 金丝雀 {pair}",
                     f"pre med={CANARY_PRE_MED} / 期望 post ≤{CANARY_POST_MAX_MED}",
                     f"pre_med={pre_med} post_med={post_med} post_n={post_n}",
                     verdict_txt))

    # ── M14：★★ 三个 population-stable 组（★ 本轮判据体系的主体）─────────────
    #   ★ 为什么是它：★ st_old ⊆ st_new ⇒ 三个组在 pre/post **是同一批对**
    #     · old_feat      = st_old 里的 ns 对      ⇒ 无回归判据
    #     · newly_covered = st_new \ st_old 的 106 个 ⇒ **效应判据**（本轮的主判据）
    #     · other         = 两个集合都没有的 ns 对  ⇒ 对照判据（节奏不该变）
    #   ★★ **样本量**：old_feat ≈56/日、newly ≈66/日 ⇒ Stage 1（24h）可判群体层
    #   ★★ **分位数脆**：newly 组是双峰（热对 med≈3 / 冷对 med≈360）⇒ p90 处于边界
    #     ⇒ ★ 另用**速率型**指标 slow_gt300_pct（稳）⇒ 判据用 p90 + 慢事件占比
    # ★★ 基线按 **stage** 选同量纲的那一套（★ Stage 1 的 post 是 24h ⇒ 用 pre24；
    #    ★ Stage 2 的 post 是 7 天 ⇒ 用 pre7d）★ 借用另一窗口 = 判错
    win = "pre24" if stage == 1 else "pre7d"
    for grp, label, mode in (
        ("old_feat", "M14 旧 featured 人群（无回归）", "noregress"),
        ("newly_covered", "M14 新覆盖人群（★ 效应）", "effect"),
        ("other", "M14 对照组（两个集合都没有）", "control"),
    ):
        bkey = f"M14_{grp}_{win}"
        b = BASELINES[bkey]
        pre = b["base"]
        base_txt = base_cell(bkey)
        key = f"M14_post_{grp}"
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
        if n is None or n < 30:
            rows.append((label, base_txt, obs,
                         verdict(None, f"★ post n={n} < 30 ⇒ 读数不稳（方向可读）")))
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
            lim = BASELINES[bkey].get("post_p90_max", 180)
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
            rows.append((label, base_txt, obs,
                         verdict(ok, f"★ 对照：post med={med}（pre {pre[0]}，容差 ±20%）"
                                   f"⇒ {'未受影响的 ns 对节奏未变' if ok else '★ 对照也变了 ⇒ 有其它因素'}")))

    # ── M11：episode 层症状 ──────────────────────────────────────────
    for tag, label, bkey in (("namespaced", "M11 namespaced 恢复感知时延", "M11_ns_pre"),
                             ("bare", "M11 bare 恢复感知时延", "M11_bare_pre")):
        base = BASELINES[bkey]["base"]
        base_txt = base_cell(bkey)
        key = f"M11b_post_fix_{tag}"
        obs = data.get(key)
        if obs is None:
            rows.append((label, base_txt, "—", verdict(None, "★ post 段无 episode")))
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
      · M12p = post pending_pct ≤1.0% 且 failures ≥30
    """
    if pass_case:
        s12s = "394 events | med_min=30 | p90=60 | p99=1200"      # ★ 参考项（人群会变）
        s12n = "1703 events | med_min=360 | p90=361 | p99=5524"    # ★ 参考项
        s11n = "7 episodes | med=3 | p90=60 | max=1200"            # 3≤3 ✔ 60≤360 ✔ 1200≤1791 ✔
        s11b = "5 episodes | med=3 | p90=120 | max=300"            # 3≤3 ✔ 120≤180 ✔ 300≤360 ✔
        m15_post_med = 120                                          # ≤180 ✔
        m12p = "240 failures_aged6h | pending=1 | pending_pct=0.4"  # 0.4≤1.0 ✔ n≥30 ✔
        # ★★ 按 **Stage 1 的 pre24 基线**（old_feat p90≤31/p99≤360）取值
        m14_old = "56 events | med_min=3 | p90_min=20 | p99_min=300 | max_min=300 | slow_gt300_pct=3.0"
        m14_new = "66 events | med_min=30 | p90_min=120 | p99_min=600 | max_min=600 | slow_gt300_pct=20.0"
        m14_oth = "300 events | med_min=360 | p90_min=368 | p99_min=5524 | max_min=5659 | slow_gt300_pct=85.0"
        m3b_f429, m3b_n429, m3b_f503, m3b_n503 = 300, 3600, 1800, 3600
        m2, m6, m9, m5 = "34 rp + 15 canon + 200 raw_variant = 210", \
            "80 featured / 1060 total_free_eligible", \
            "0 个 featured canonical 的 raw 变体未进 static（★ 期望 0）", \
            "9228 total | free=1234"
    else:
        s12s = "394 events | med_min=600 | p90=900 | p99=2400"      # ★ 仍是参考项
        s12n = "1703 events | med_min=600 | p90=700 | p99=6000"    # ★ 仍是参考项
        # ★★ 失败用例：**每行只坏一个东西** ⇒ ★ 任何单点阈值变异都会被抓到
        s11n = "7 episodes | med=10 | p90=60 | max=1200"            # 只坏 med（10>3）⇒ ❌
        s11b = "5 episodes | med=10 | p90=120 | max=300"            # 只坏 med（10>3）⇒ ❌
        m15_post_med = 300                                          # >180 ⇒ ❌
        m12p = "240 failures_aged6h | pending=40 | pending_pct=16.7"  # >1.0 ⇒ ❌
        m14_old = "56 events | med_min=3 | p90_min=200 | p99_min=300 | max_min=300 | slow_gt300_pct=3.0"
        m14_new = "66 events | med_min=30 | p90_min=600 | p99_min=1400 | max_min=1400 | slow_gt300_pct=20.0"
        m14_oth = "300 events | med_min=120 | p90_min=368 | p99_min=5524 | max_min=5659 | slow_gt300_pct=85.0"
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
        "M12p_pending_post_ns_not": m12p,
        "M12p_pending_pre24_ns_not": "1377 failures_aged6h | pending=0 | pending_pct=0.0",
    }
    for pair in CANARIES:
        d[f"M15_pre__{pair}"] = "13 events | med_min=360 | p90_min=360 | p99_min=1791 | max_min=1791"
        d[f"M15_post__{pair}"] = (f"9 events | med_min={m15_post_med} | p90_min=240"
                                  f" | p99_min=600 | max_min=600")
    return d


def selftest() -> int:
    """★ 四种场景各断言一次：① 全达成 ② 全未达成 ③ 量具缺数 ④ 守卫失败。"""
    failures: list[str] = []

    def rows_by_label(rows):
        return {r[0]: r[3] for r in rows}

    # ① 全达成（★ M12s 两行是**参考项** ⇒ 期望 ⚠️，其余可判行期望 ✅）
    rows, bad, _ = judge(_synthetic(True))
    by = rows_by_label(rows)
    REFERENCE = ("M12s ns featured 复检间隔（med/p90/p99）",
                 "M12s ns 非featured 复检间隔（med/p90/p99）")
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
              "M14 新覆盖人群（★ 效应）",
              "M14 对照组（两个集合都没有）",
              "M11 namespaced 恢复感知时延",
              "M12p ns 非featured 待复检率", f"M15 金丝雀 {CANARIES[0]}"):
        if not by.get(k, "").startswith("❌"):
            failures.append(f"[② 全未达成] {k} ⇒ {by.get(k, '<缺行>')}")

    # ③ 量具缺数（分位是 '?'）⇒ 必须「不可判」，**不能**是「未达成」
    d = _synthetic(True)
    d["M12s_settled_post_ns_feat"] = "394 events | med_min=? | p90=? | p99=?"
    d["M11b_post_fix_namespaced"] = "7 episodes | med=? | p90=? | max=?"
    d["M3b_backoff_ns_featured_err_http_429"] = "5 rows | median_retry_s=?"
    d["M14_post_newly_covered"] = "66 events | med_min=30 | p90_min=? | p99_min=600 | max_min=600 | slow_gt300_pct=2.0"
    rows, bad, _ = judge(d)
    by = rows_by_label(rows)
    for k in ("M12s ns featured 复检间隔（med/p90/p99）",
              "M11 namespaced 恢复感知时延",
              "M3b namespaced backoff 档位",
              "M14 新覆盖人群（★ 效应）"):
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
    d = _synthetic(True)
    d["M14_post_newly_covered"] = ("66 events | med_min=30 | p90_min=120 | p99_min=600"
                                   " | max_min=600 | slow_gt300_pct=80.0")
    rows, _, _ = judge(d, stage=1)
    v = rows_by_label(rows).get("M14 新覆盖人群（★ 效应）", "<缺行>")
    if not v.startswith("❌"):
        failures.append(f"[⑦ slow 单项] 只坏 slow 时应判未达成 ⇒ {v}")

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
    print("\n== 合成数据自测通过 ==")
    return 0


# ─────────────────────────────────────────────────────────────────────
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
    print("\n== M15 逐对明细 ==")
    for pair in CANARIES:
        pre = data.get(f"M15_pre__{pair}")
        post = data.get(f"M15_post__{pair}")
        pre_med = field(pre, "med_min", "med") if pre else None
        post_n = head_count(post) if post else None
        post_med = field(post, "med_min", "med") if post else None
        print(f"   {pair}: pre med={pre_med} (n={head_count(pre) if pre else '?'})"
              f" → post med={post_med} (n={post_n})")

    if args.stage == 2:
        print("★ Stage 2（7 天窗口）：M11 episode 层与 M15 金丝雀为**主判据**"
              "（★ Stage 1 的「样本不足」项在此应全部可判）。")
    else:
        print("★ Stage 1（24h 窗口）判据集：★ 机制面（M3b）+ 资格/覆盖面（M2/M6/M9）"
              "+ 成本面（M5）+ ★★ **M14 三组**（population-stable：无回归 / 效应 / 对照）"
              "+ M12p 护栏（待复检率）；"
              "★ M11 若 post episode ≥5 即可判（≈7/日）；"
              "★★ M15 逐对幅度**留 Stage 2**（≈2/对/日 ⇒ 24h 不足）；"
              "★ M12s 两行是**参考**（post 人群会变 ⇒ 不可归因）。")
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

    print("\n★ 诚实边界（必须一起写进结论）：")
    print("   ① M1 / M1b 可用率**一律不作判据**（归因不可分 + 相位敏感："
          "credential_recovery 扫描 6h 周期 ⇒ 采样须标注「距上次 sweep 多久」）。")
    print("   ② era2 的增量效果是「消除资格障碍」的**潜在收益**；"
          "目标人群（106 个新覆盖变体）修正 join 后 7 天有 **461 事件 / 9 个对"
          "（≈66 事件/日）**，但其中四个「冷金丝雀」各仅 13 次（≈1.9/日）"
          "⇒ ★★ 群体层可判（M14），★ 逐对幅度仍需 Stage 2。")
    print("   ③ M12 post 段存在**右删失**（漏掉复检慢的对 ⇒ 偏差方向对我们有利）"
          "⇒ 已用 M12s（结算版）+ M12p（护栏）对冲，但**必须在报告里披露**。")
    print("   ④ M6 是**资格面**，不是「行为已改善」的证据。")
    print("   ⑤ ★★ 基线口径已在 2026-11-10 14:2x 修正：原先的绑定过滤用 "
          "`provider_model_id = (… LIMIT 1)`，而 raw_model_name 是**一对多**"
          "⇒ 只统计到 30% 事件（4,073 vs 13,555）⇒ ★ 现全部基线均为**修正后**口径。")
    print("   ⑥ ★★ 「量具算不出」（分位缺失）单列为「不可判」，**不并入未达成**；"
          "守卫指标**缺失**同样判量具不可信 ⇒ 整表作废。")
    print("   ⑦ ★★★ 判据的第四道关 = **人群稳定性**：★ 高样本 ≠ 可判 —— "
          "M12s 的 post ns|feat 混入了 era2 新纳入的 106 个冷对 ⇒ 尾部变化**不可归因** "
          "⇒ ★ 降级为参考；★ 判据改用 **M14 三组**（st_old ⊆ st_new ⇒ pre/post 同一批对）。")
    print("   ⑧ ★★★ pre 基线必须**冻结**：★ 口径已把窗口/成熟度/era 纯度全部**锚定到切点** "
          "⇒ ★ 真跑 / Stage 1 / Stage 2 三个时钟下 pre 段**逐字节一致**（38/38 键）；"
          "★ 否则 Stage 2 的 pre 侧会整段消失、Stage 1 的 pre 会随查询时刻漂移。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
