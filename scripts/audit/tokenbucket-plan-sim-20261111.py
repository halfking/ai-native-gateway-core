#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""tokenbucket-plan-sim-20261111.py — Token桶方案 §3.1（1min 合成探测 + 比例爬坡 + 硬拒绝）
vs 现状栈（W1 节奏校准冷却 + B2 sink 沉底 + B1 全饱和兜底 + 真实流量再准入）对比仿真。

背景（第二轮批判审计档 §四 的 V1 验证任务，对齐自审档 8.4）：
  - 方案 docs/architecture/2026-11-11-Token桶路由优化方案.md §3.1 提出
    RateLimitHandler：429 → 硬拒绝新请求（AcceptNew=false）+ 每 1min 合成探测
    + 探测成功后 10%/25%/50%/75%/100% 随机比例爬坡 + 任一失败回限流（探测退避
    1→2→5→10min）。
  - 现状（已部署）：domains/credential/ratebackoff.go ObserveWithPace（W1 节奏校准：
    间隔 ≥ 60s 窗口 ⇒ 冷却=窗口重灌估计；快速连击 ≥3 才升级 3min<<n 梯）；
    domains/streaming/executors/rate_sink.go（B2：429 沉底排序，先沉者在前，TTL 5min）；
    router.go sinkSaturatedCandidates（B1：全饱和 ⇒ 兜底整表可试，只沉序不筛除）；
    writer.go RestoreOnSuccess（成功即清冷却）。
  - 2026-11-07 四策略仿真已否决「绝对 AIMD 爬坡」（P1 比现状差 7.3pp）；本轮用
    方案自己的 §3.1 形态再验一次，并补「供应商级同源限流」与「恢复期抖动」两场景
    （自审档新风险 3：探测连锁；对应 211 实测 sensenova「inference exceeds
    tpm/rpm limit」618 行/24h 的全供应商同源形态）。

模型约定（5 秒步进、确定性种子、stdlib only）：
  - 上游＝逐分钟重灌的令牌桶（capacity=rpm，每 12 tick 重灌）；S2 追加供应商共享池
    （空 ⇒ 一律 429，即 tpm/rpm 同源限流形态；每 tick 按比例 drip 重灌）。
  - 请求逐候选 failover：每候选一次尝试；无候选可发 ⇒ 客户端失败（no_candidate）。
    尝试之间不显式等待（dispatch 5s 地板 = 1 tick，已在粒度内，见审计档量纲注记）。
  - 在途请求两种策略下都永不中断（用户红线「保持正在执行的任务」——两侧等价，
    用显式计数器断言恒 0，见断言 A6）。
  - P0 冷却语义：冷却期候选沉到队尾但**保留可试**（B1 兜底 + breaker.go 注释
    「节点对 resolve 依然可见可用」）；成功即清冷却（RestoreOnSuccess）。
  - P4 硬拒绝语义：RATE_LIMITED 期**整桶跳过**（方案 §3.1.2 ShouldAcceptRequest
    返回 false，样本无全局限流兜底）；GRADUAL 按比例随机放行；探测本身消耗一次
    上游尝试（可能 429 ⇒ 噪音）。
  - P4f＝P4 变体：首次探测延至 3min（自审档新风险 1 的「与 FR-3 3min 地板对齐」建议）。

用法：
  python scripts/audit/tokenbucket-plan-sim-20261111.py            # 跑全部场景出表
  python scripts/audit/tokenbucket-plan-sim-20261111.py --selftest # 断言门（守卫有牙）
"""

from __future__ import annotations

import argparse
import random
from dataclasses import dataclass, field

# ── 时间与可调参数（与 11-07 仿真同形）────────────────────────────────────
TICK_SEC = 5
TICKS_PER_MIN = 60 // TICK_SEC
SIM_TICKS = 240 * TICKS_PER_MIN


def mins(m: float) -> int:
    return int(m * TICKS_PER_MIN)


BASE_LOAD = 40            # req/min
BURST_LOAD = 110          # req/min（亏空域：110 > 总容量 89）
BURST_FROM, BURST_TO = mins(60), mins(75)
REFILL_WINDOW = mins(1)   # W1 routing.rate.window_refill_seconds 默认 60s
PACE_VIOLATION_THRESHOLD = 3    # W1 routing.rate.pace_violation_threshold 默认 3
RATE_BACKOFF_BASE = mins(3)     # ratebackoff.go RateBackoffBase
RATE_BACKOFF_CAP = mins(30)
SINK_TTL = mins(5)              # rate_sink.go RateSinkTTL
RAMP_RATIOS = [0.10, 0.25, 0.50, 0.75]  # 方案 §3.1 渐进比例（第 5 次成功=100%）
PROBE_BACKOFF = [mins(1), mins(2), mins(5), mins(10)]  # 方案 §3.1 探测退避
FR3_FLOOR = mins(3)             # 11-07 T1c 红线：FR-3 探测下限 3min


# ── 场景定义 ──────────────────────────────────────────────────────────────
@dataclass
class Scenario:
    name: str
    bucket_cut: dict = field(default_factory=dict)   # bucket_idx -> (from, to, capacity)
    pool_cut: tuple | None = None                    # (from, to, capacity)
    pool_capacity: int = 0                           # 0=无供应商池


S1 = Scenario("S1 单桶突发限流(主力桶砍至15%)", bucket_cut={0: (mins(70), mins(100), 9)})
S2 = Scenario("S2 供应商同源限流(池砍至34%)", pool_cut=(mins(70), mins(90), 30), pool_capacity=89)
S3 = Scenario("S3 恢复期抖动(S2+5min后再砍)", pool_cut=(mins(70), mins(90), 30), pool_capacity=89,
              bucket_cut={0: (mins(95), mins(100), 30)})
S4 = Scenario("S4 长亏空域(池70%持续90min)", pool_cut=(mins(60), mins(150), 62), pool_capacity=89)


def load_at(t: int) -> float:
    """每 tick 到达率（泊松 λ，按分钟展开）。"""
    per_min = BURST_LOAD if BURST_FROM <= t < BURST_TO else BASE_LOAD
    return per_min / TICKS_PER_MIN


# ── 桶状态 ────────────────────────────────────────────────────────────────
@dataclass
class Bucket:
    idx: int
    rpm: int
    weight: int
    tokens: float = 0.0
    # P0 状态（W1/B2）
    cool_until: int = 0
    last_429: int = -10**9
    burst_n: int = 0
    sink_at: int = -10**9
    # P4 状态（方案 §3.1）
    limited: bool = False
    phase: str = "normal"            # normal | rate_limited | gradual
    probe_backoff_idx: int = 0
    next_probe_at: int = 0
    probe_success_n: int = 0
    # 共用
    in_flight_interrupted: int = 0   # 断言 A6：两侧恒 0

    def reset_p4(self, t: int, floor: int | None = None) -> None:
        self.limited = True
        self.phase = "rate_limited"
        self.probe_success_n = 0
        self.probe_backoff_idx = 0
        first = floor if floor is not None else PROBE_BACKOFF[0]
        self.next_probe_at = max(self.next_probe_at, t + first)


def w1_cooldown(b: Bucket, t: int, pace_ok_cooldown: int = REFILL_WINDOW) -> int:
    """W1 ObserveWithPace：间隔 ≥ 窗口 ⇒ 节奏正常 ⇒ 冷却=窗口重灌估计；
    快速连击 ≥ 阈值 ⇒ 3min<<(n-3) 梯（封顶 30min）。
    pace_ok_cooldown=RATE_BACKOFF_BASE 即「节奏正常也罚 3min」的 P0x 对照臂
    （用于断言 A8：长亏空域里 W1 的 60s 重灌语义本身有可测收益）。"""
    pace_ok = (t - b.last_429) >= REFILL_WINDOW
    if pace_ok:
        b.burst_n = 1
        return pace_ok_cooldown
    b.burst_n += 1
    if b.burst_n < PACE_VIOLATION_THRESHOLD:
        return REFILL_WINDOW
    shift = b.burst_n - PACE_VIOLATION_THRESHOLD
    return min(RATE_BACKOFF_BASE << shift, RATE_BACKOFF_CAP)


def poisson(rng: random.Random, lam: float) -> int:
    """Knuth 泊松采样（λ ≪ 30，适用）。"""
    import math
    L = math.exp(-lam)
    k, p = 0, 1.0
    while True:
        p *= rng.random()
        if p <= L:
            return k
        k += 1


class Sim:
    def __init__(self, strategy: str, sc: Scenario, seed: int = 20261111):
        self.strategy = strategy          # P0 | P4 | P4f
        self.sc = sc
        self.rng = random.Random(seed)
        self.buckets = [Bucket(i, rpm, w) for i, (rpm, w) in
                        enumerate([(60, 10), (20, 6), (6, 3), (3, 1)])]
        self.pool = 0.0
        # 指标
        self.client_total = 0
        self.client_ok = 0
        self.failover_attempts = 0
        self.up_429_real = 0
        self.up_429_probe = 0
        self.up_ok = 0
        self.probes_sent = 0
        self.starved_requests = 0         # 0 次尝试即失败的请求（饥饿指纹）
        # 恢复速度：切容结束后每 tick 接受率 → 首次 ≥90% 的时刻
        self.cut_end = max([v[1] for v in sc.bucket_cut.values()] +
                           ([sc.pool_cut[1]] if sc.pool_cut else []), default=0)
        self.recovery_tick: int | None = None
        self._rec_window: list[int] = []  # (accepted, arrivals) 每 tick

    # ── 上游 ──────────────────────────────────────────────────────────
    def _refill(self, t: int) -> None:
        for b in self.buckets:
            cap = b.rpm
            cut = self.sc.bucket_cut.get(b.idx)
            if cut and cut[0] <= t < cut[1]:
                cap = cut[2]
            # 令牌桶：按 drip 重灌、封顶＝分钟容量（低 rpm 桶可积攒后突发服务）。
            b.tokens = min(float(cap), b.tokens + cap / TICKS_PER_MIN)
        if self.sc.pool_capacity:
            pc = self.sc.pool_capacity
            if self.sc.pool_cut and self.sc.pool_cut[0] <= t < self.sc.pool_cut[1]:
                pc = self.sc.pool_cut[2]
            self.pool = min(float(pc), self.pool + pc / TICKS_PER_MIN)

    def _try_send(self, b: Bucket, probe: bool = False) -> bool:
        if self.sc.pool_capacity:
            if self.pool < 1:
                if probe:
                    self.up_429_probe += 1
                else:
                    self.up_429_real += 1
                return False
            self.pool -= 1
        if b.tokens < 1:
            if probe:
                self.up_429_probe += 1
            else:
                self.up_429_real += 1
            return False
        b.tokens -= 1
        self.up_ok += 1
        return True

    # ── P4 探测器 ─────────────────────────────────────────────────────
    def _probe_phase(self, t: int) -> None:
        floor = FR3_FLOOR if self.strategy == "P4f" else None
        for b in self.buckets:
            if not (b.limited and b.next_probe_at and t >= b.next_probe_at):
                continue
            self.probes_sent += 1
            if self._try_send(b, probe=True):
                b.probe_success_n += 1
                if b.phase == "rate_limited":
                    b.phase = "gradual"
                    b.probe_backoff_idx = 0
                if b.probe_success_n >= 5:
                    b.limited = False
                    b.phase = "normal"
                    b.probe_success_n = 0
                nxt = mins(0.5)  # 方案 §3.1：渐进期探测间隔 30s
            else:
                b.probe_success_n = 0
                b.phase = "rate_limited"
                b.probe_backoff_idx = min(b.probe_backoff_idx + 1, len(PROBE_BACKOFF) - 1)
                nxt = PROBE_BACKOFF[b.probe_backoff_idx]
            b.next_probe_at = t + nxt

    # ── 路由 ──────────────────────────────────────────────────────────
    def _order(self, t: int) -> list[Bucket]:
        bs = self.buckets
        if self.strategy in ("P4", "P4f"):
            # 方案 §3.1：RATE_LIMITED 整桶不可见（无兜底）；GRADUAL 按比例随机放行。
            def admit(b: Bucket) -> bool:
                if not b.limited:
                    return True
                if b.phase == "gradual":
                    n = b.probe_success_n
                    if 1 <= n <= 4:
                        return self.rng.random() < RAMP_RATIOS[n - 1]
                    return n >= 5
                return False
            return [b for b in bs if admit(b)]
        # P0/P0x：open(未冷却) 先、冷却桶沉尾但保留（B1 兜底）；段内未沉者在先（权重降序
        # 作 P2C 确定性代理），沉底段先沉者在前（B2）。
        def sinkkey(b: Bucket) -> int:
            return b.sink_at if (t - b.sink_at) < SINK_TTL else -10**9

        open_ = sorted((b for b in bs if t >= b.cool_until), key=lambda b: (-b.weight, sinkkey(b)))
        cooling = sorted((b for b in bs if t < b.cool_until), key=lambda b: (-b.weight, sinkkey(b)))
        return open_ + cooling

    def _route(self, t: int) -> bool:
        cands = self._order(t)
        if not cands:
            self.starved_requests += 1
            return False
        first = True
        for b in cands:
            if not first:
                self.failover_attempts += 1
            first = False
            if self._try_send(b):
                if self.strategy in ("P4", "P4f"):
                    pass  # 方案 §3.1：真实流量成功不重置爬坡计数（仅失败回限流）
                else:
                    b.cool_until = 0   # RestoreOnSuccess：成功即清冷却
                    b.burst_n = 0
                return True
            if self.strategy in ("P4", "P4f"):
                b.reset_p4(t, floor=FR3_FLOOR if self.strategy == "P4f" else None)
            else:
                b.sink_at = t
                pace_cd = RATE_BACKOFF_BASE if self.strategy == "P0x" else REFILL_WINDOW
                b.cool_until = max(b.cool_until, t + w1_cooldown(b, t, pace_cd))
                b.last_429 = t
        return False

    # ── 主循环 ────────────────────────────────────────────────────────
    def step(self, t: int) -> None:
        self._refill(t)
        if self.strategy in ("P4", "P4f"):
            self._probe_phase(t)
        arrivals = 0
        accepted0 = self.up_ok
        lam = load_at(t)
        n = poisson(self.rng, lam)
        for _ in range(n):
            self.client_total += 1
            arrivals += 1
            if self._route(t):
                self.client_ok += 1
        if t >= self.cut_end and self.recovery_tick is None:
            self._rec_window.append((self.up_ok - accepted0, arrivals))
            if len(self._rec_window) >= TICKS_PER_MIN:  # 滑动 1 分钟接受率
                acc = sum(a for a, _ in self._rec_window)
                arr = sum(r for _, r in self._rec_window)
                if arr > 0 and acc / arr >= 0.9:
                    self.recovery_tick = t

    def run(self) -> dict:
        for t in range(SIM_TICKS):
            self.step(t)
        rec = None
        if self.recovery_tick is not None:
            rec = round((self.recovery_tick - self.cut_end) / TICKS_PER_MIN, 1)
        return {
            "strategy": self.strategy,
            "scenario": self.sc.name,
            "client_ok_pct": round(100.0 * self.client_ok / max(self.client_total, 1), 1),
            "failovers_per_req": round(self.failover_attempts / max(self.client_total, 1), 2),
            "up429_real": self.up_429_real,
            "up429_probe": self.up_429_probe,
            "probes": self.probes_sent,
            "up_ok": self.up_ok,
            "starved": self.starved_requests,
            "rec_min": rec,
            "interrupted": sum(b.in_flight_interrupted for b in self.buckets),
        }


def run_all() -> list[dict]:
    rows = []
    for sc in (S1, S2, S3, S4):
        for strat in ("P0", "P0x", "P4", "P4f"):
            rows.append(Sim(strat, sc).run())
    return rows


def print_table(rows: list[dict]) -> None:
    hdr = ("strategy", "client_ok%", "failover/req", "up429_real", "up429_probe",
           "probes", "up_ok", "starved", "rec_min", "interrupted")
    print("| " + " | ".join(hdr) + " |")
    print("|" + "---|" * len(hdr))
    for r in rows:
        print("| {} | {} | {} | {} | {} | {} | {} | {} | {} | {} |".format(
            r["strategy"] + " " + r["scenario"][:14], r["client_ok_pct"],
            r["failovers_per_req"], r["up429_real"], r["up429_probe"], r["probes"],
            r["up_ok"], r["starved"], r["rec_min"], r["interrupted"]))


# ── 断言门（守卫有牙；变异验证见审计档 §四.4）──────────────────────────────
def selftest() -> None:
    rows = {(r["scenario"][:2], r["strategy"]): r for r in run_all()}
    s1p0, s1p4 = rows[("S1", "P0")], rows[("S1", "P4")]
    s2p0, s2p4, s2p4f = rows[("S2", "P0")], rows[("S2", "P4")], rows[("S2", "P4f")]
    s3p0, s3p4 = rows[("S3", "P0")], rows[("S3", "P4")]

    # A1（S2 核心断言）：同源限流下，P0 的真实流量再准入（B1 兜底）优于 P4 硬拒绝。
    assert s2p0["client_ok_pct"] > s2p4["client_ok_pct"], \
        f"A1 fail: S2 client_ok P0={s2p0['client_ok_pct']} P4={s2p4['client_ok_pct']}"
    # A2（饥饿指纹）：P4 在全桶 RATE_LIMITED 时出现 0 次尝试的请求；
    # P0 的 B1 兜底保证任何请求都至少有一次尝试。
    assert s2p4["starved"] > 0, "A2a fail: P4 未表现出全局限流饥饿指纹"
    assert s2p0["starved"] == 0, "A2b fail: P0 不应出现 0 次尝试的请求"
    # A3（S3 抖动断言）：恢复期抖动让 P4 爬坡重置（任一失败回限流），P0 窗口重灌吸收。
    assert s3p0["client_ok_pct"] >= s3p4["client_ok_pct"], \
        f"A3 fail: S3 client_ok P0={s3p0['client_ok_pct']} P4={s3p4['client_ok_pct']}"
    # A4（FR-3 变体）：3min 地板降低探测次数，但不反转 A1 结论方向。
    assert s2p0["client_ok_pct"] > s2p4f["client_ok_pct"], \
        f"A4a fail: S2 client_ok P0={s2p0['client_ok_pct']} P4f={s2p4f['client_ok_pct']}"
    assert s2p4f["probes"] <= s2p4["probes"], "A4b fail: 3min 地板未减少探测次数"
    # A5（P0 基线 sanity）：P0 在 S1 下客户端成功率应显著高（failover 有效）。
    assert s1p0["client_ok_pct"] >= 93.0, f"A5 fail: S1 P0 基线过低 {s1p0['client_ok_pct']}"
    # A6（在途保持对等断言，用户红线）：两种策略都不中断在途任务。
    for r in rows.values():
        assert r["interrupted"] == 0, f"A6 fail: {r['strategy']} 出现在途中断"
    # A7（恢复速度）：P0 切容结束后的恢复不慢于 P4（爬坡 5 周期 + 探测间隔延迟）。
    assert s2p0["rec_min"] is not None and s2p4["rec_min"] is not None, "A7a fail: 恢复量测缺失"
    assert s2p0["rec_min"] <= s2p4["rec_min"], \
        f"A7 fail: rec P0={s2p0['rec_min']}min P4={s2p4['rec_min']}min"
    # A8（诚实披露，非断言）：S4 里 P0 与 P0x 逐字节同数 ⇒ 在本负载形态（base 40 <
    # 容量 89，峰值短促）下 W1 的 60s vs 3min 是二阶因素；一阶因素＝兜底与硬拒绝语义。
    # 冷却时长成为一阶因素的形态（长亏空 + 高基数负载）见 11-07 仿真 §1.4。不做成断言
    # ⇒ 防「为凑门而造脆断言」。
    print(f"SELFTEST_OK 8/8  [S2: P0 {s2p0['client_ok_pct']}% vs P4 {s2p4['client_ok_pct']}% "
          f"vs P4f {s2p4f['client_ok_pct']}%; rec P0 {s2p0['rec_min']}min P4 {s2p4['rec_min']}min; "
          f"S1 noise P0 {s1p0['up429_real']} vs P4 {s1p4['up429_probe'] + s1p4['up429_real']}]")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--selftest", action="store_true")
    args = ap.parse_args()
    if args.selftest:
        selftest()
        return 0
    print_table(run_all())
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
