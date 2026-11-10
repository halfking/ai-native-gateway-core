#!/usr/bin/env python3
"""★★ 「关窗预演」：★ 关窗前把判定链在**关窗时钟**下跑一遍 ⇒ 提前知道当天会不会整表作废

★★ 为什么需要它（★ 本轮第 21 处口径修正的触发点）：
★ Stage 1 的效应主判据 M20 里，**slow 分量**测的是「失败 → 下一次探测」的**间隔**
★ ⇒ **必须结算**（★ 成熟度 6h）⇒ ★★ 关窗前（now = 切点 + ~5.6h）post 段**一条都还没结算**
★ ⇒ ★★ 判定器只能显示「★ slow 分量**未结算** ⇒ 不可判」⇒ ★★ **关窗那一刻会读成什么，在关窗前完全不可见**
★ ⇒ ★★ 而这正是「关窗读数」特有的盲区（★ 指纹分量已由第 18 处修正解决，★ **slow 分量至今仍盲**）。

★★★ 本脚本做的事：
① ★★ 用**关窗时钟**（as-of = era 切点 + 24h）复采一遍（★ 同一份 metrics.sql，★ 只替换 `now()`）
② ★★ 跑**完整判定链**（★ Stage 1 路径）⇒ 拿到**预演裁决表**
③ ★★ 验「**pre 侧逐字节不变**」⇒ ★★ 这**就是**当天 M18 冻结守卫会不会过的**预演**
④ ★★ 并列 M20 的**三分量**读数（★ 指纹 / slow / 摇摆）+ **逐模型样本量**

★★★ 三条硬纪律（★ 写死）：
① ★★ **同一份口径**：★ 投影 = 把 `metrics.sql` 里的 `now()` **确定性地**替换成 as-of 时间戳
   ⇒ ★ **不是**另写一份 SQL ⇒ ★★ 口径不可能分叉（★ 与负控同款思路：★ 只改 /tmp 临时副本）
② ★★ **pre 侧必须逐字节不变**：★ 投影下的 pre 键 == 真实时钟下的 pre 键 == 冻结基线
   ⇒ ★★ 破了 ⇒ ★★ **Stage 1 当天 M18 守卫会作废整表** ⇒ ★ 本脚本 rc=1
③ ★★★ **投影只是「样本下界」，永远不替代关窗真读**：
   ★ as-of 是**未来时刻** ⇒ ★ 数据库里**还没发生**的事件不会出现在投影里
   ⇒ ★★ 投影的样本量 **≤** 关窗真读 ⇒ ★★ **投影只能判「方向」，不能判「终局」**
   ⇒ ★★ 判负时**必须**区分：「投影已负（大概率真负）」vs「投影未负（关窗后仍可能翻）」
   ⇒ ★★ 且**任何**结论都必须以**关窗当天真读**为准

★ 用法：
    python3 scripts/audit/recovery-detection-p0-3-stage1-projection.py             # Stage 1 关窗预演
    python3 scripts/audit/recovery-detection-p0-3-stage1-projection.py --stage 2   # Stage 2 关窗预演
    python3 scripts/audit/recovery-detection-p0-3-stage1-projection.py --asof "2026-11-11 13:42:30"
    python3 scripts/audit/recovery-detection-p0-3-stage1-projection.py --save-out /tmp/asof.txt

★ 退出码：0 = 预演完成且 pre 侧零漂移；1 = **pre 侧漂移 / 判定链走不通 / 采集无效** ⇒ ★ 当天会作废 ⇒ 必须先修
"""
from __future__ import annotations

import argparse
import importlib.util
import pathlib
import re
import sys
from datetime import datetime, timedelta

ROOT = pathlib.Path(__file__).resolve().parents[2]
SQL_PATH = ROOT / "scripts/audit/recovery-detection-p0-3-metrics.sql"
CC_PATH = ROOT / "scripts/audit/recovery-detection-p0-3-clockcheck.py"

# ★★ M20 的「三分量」读数键（★ 第 18/20 处口径修正）
FP_KEY = "M20_normal_post_fp"
SLOW_KEY = "M20_normal_post"
SWING_KEY = "M20_normal_post_fp_swing"
PERMODEL_PREFIX = "M20_normal_post_fp_model__"


def load_module(path: pathlib.Path, name: str):
    spec = importlib.util.spec_from_file_location(name, path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def era_cut(sql: str) -> datetime:
    """★ era 切点**从 SQL 里取**（★ 不写死 ⇒ ★ 切点变了投影自动跟上）"""
    m = re.search(r"TIMESTAMPTZ '(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\+08'", sql)
    assert m, "★ 采集 SQL 里找不到 era 切点字面量"
    return datetime.strptime(m.group(1), "%Y-%m-%d %H:%M:%S")


def field(text: str | None, name: str) -> str | None:
    if not text:
        return None
    m = re.search(rf"\b{re.escape(name)}=([^|\s]+)", text)
    return m.group(1) if m else None


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--asof", default="",
                    help="★ 关窗时刻（★ 不带时区，★ 脚本统一拼 +08；★ 默认 = era 切点 + stage 天数）")
    ap.add_argument("--stage", type=int, default=1, choices=(1, 2))
    ap.add_argument("--ssh-host", default="211")
    ap.add_argument("--save-out", default="", help="★ 把 as-of 复采输出存盘（★ 供离线复算）")
    args = ap.parse_args()

    cc = load_module(CC_PATH, "cc_proj")
    src = SQL_PATH.read_text(encoding="utf-8")
    cut = era_cut(src)

    # ★★ 纪律①：**确定性替换**（★ 同一份 SQL ⇒ 口径不可能分叉）
    # ★★★ ★★ **stage → 关窗时刻的映射必须按「窗口小时数」算，★ 不能按「天数 = stage」**
    #   （★ 本轮真踩到：★ 原写法 `cut + timedelta(days=args.stage)` ⇒ ★ Stage 2 得 **cut+2 天**
    #    ⇒ ★★ 而 Stage 2 的窗口是 **168h = cut+7 天**（★ negcontrol 断言 `win_hours == 168`
    #    ★★ / ★ clockcheck 的 Stage 2 默认 future = 2026-11-17 13:45）⇒ ★★ **Stage 2 的预演会验错几何**
    #    ⇒ ★★ Stage 1 恰好相等（cut+1天 == cut+24h）⇒ ★★ **所以这个错在 Stage 1 上完全看不出来**）
    STAGE_HOURS = {1: 24, 2: 168}
    if args.asof:
        asof_txt = args.asof.strip()
    else:
        asof_txt = f"{(cut + timedelta(hours=STAGE_HOURS[args.stage])).strftime('%Y-%m-%d %H:%M:%S')}"
    fut = f"TIMESTAMPTZ '{asof_txt}+08'"
    n_now = src.count("now()") + src.count("NOW()")
    variant = src.replace("now()", fut).replace("NOW()", fut)
    left = variant.lower().count("now()")
    if left:
        print(f"★★ 替换后仍残留 now() {left} 处 ⇒ 投影无效", file=sys.stderr)
        return 1

    print("== 关窗预演 ==")
    print(f"   era 切点   = {cut:%Y-%m-%d %H:%M:%S} +08")
    print(f"   as-of      = {asof_txt} +08（★ 替换 now()/NOW() 共 {n_now} 处）")
    print(f"   判定阶段   = Stage {args.stage}")
    # ★★★ **自检**：★ as-of 必须 = 切点 + **本 stage 的窗口长度**（★ 防止 stage↔窗口映射再错）
    #   ⇒ ★★ 这条与 negcontrol 的 `win_hours == 168` 断言、★ clockcheck 的 Stage 2 默认 future **同一语义**
    if not args.asof:
        _expect = cut + timedelta(hours=STAGE_HOURS[args.stage])
        if _expect.strftime("%Y-%m-%d %H:%M:%S") != asof_txt:
            print(f"★★ as-of 算错：★ 期望 {_expect:%Y-%m-%d %H:%M:%S}（★ cut + "
                  f"{STAGE_HOURS[args.stage]}h）但得到 {asof_txt} ⇒ 投影无效", file=sys.stderr)
            return 1
        print(f"   ✔ as-of 自检：★ cut + {STAGE_HOURS[args.stage]}h = {asof_txt}（★ 与 Stage 窗口同长度）")

    real_raw = cc.run_sql(src, "real_proj", args.ssh_host)
    asof_raw = cc.run_sql(variant, "asof_proj", args.ssh_host)
    real, asof = cc.parse(real_raw), cc.parse(asof_raw)

    # ★★ 先验「as-of 这次采集**有效**」（★ 否则会把「采集失败」误读成「pre 键漂移」）
    if len(asof) < max(10, len(real) * 0.5):
        print(f"   ★★ as-of 采集**无效**（★ 真实 {len(real)} 键 vs as-of {len(asof)} 键）"
              f" ⇒ ★ 这是采集失败（★ 不是口径漂移）⇒ ★ 先看 psql 报错")
        return 1
    print(f"   键数       = 真实 {len(real)} / as-of {len(asof)}")

    if args.save_out:
        pathlib.Path(args.save_out).write_text(asof_raw, encoding="utf-8")
        print(f"   as-of 输出 → {args.save_out}")

    # ── 纪律②：pre 侧逐字节不变（★ 这就是当天 M18 守卫会不会过的预演）──
    pre_keys = sorted(k for k in real if cc.PRE_KEY_RE.match(k))
    missing = [k for k in pre_keys if k not in asof]
    diff = [(k, real[k], asof[k]) for k in pre_keys if k in asof and real[k] != asof[k]]
    print(f"\n== pre 键逐字节比对（★ {len(pre_keys)} 个）==")
    if missing:
        print(f"   ★★ as-of **缺键** {len(missing)} 个：{missing[:6]}")
    if diff:
        print(f"   ★★ **漂移 {len(diff)} 个**（★ ⇒ 当天 M18 会报「pre 基线漂移」⇒ ★ **整表作废**）：")
        for k, a, b in diff:
            print(f"      - {k}\n          真实: {a}\n          as-of: {b}")
    else:
        print("   ✔ 全部逐字节一致（★ pre 基线不受关窗时钟影响 ⇒ ★ 当天 M18 守卫会过）")

    # ★★★ 第 22/23 处口径修正：★ 「披露用」的 pre 侧键**也要**逐字节比对
    #   ★★ 它不进 M18 冻结面（★ 不扩冻结面 ⇒ 不触发重冻）⇒ ★★ 但它**是 pre 侧读数**
    #   ⇒ ★★ 若它随时钟变 ⇒ ★★ 同一窗口的「基线口径披露」会前后不一致 ⇒ ★★ 报告不可复核
    ci_keys = sorted(k for k in cc.CLOCK_INDEP_KEYS if k in real or k in asof)
    ci_missing = [k for k in ci_keys if k not in asof]
    ci_diff = [(k, real[k], asof[k]) for k in ci_keys if k in asof and real[k] != asof[k]]
    print(f"\n== 披露用 pre 侧键的时钟无关性（★ {len(ci_keys)} 个）==")
    if ci_missing:
        print(f"   ★★ as-of **缺键** {len(ci_missing)} 个：{ci_missing}")
    if ci_diff:
        print(f"   ★★ **漂移 {len(ci_diff)} 个**（★ ⇒ 该键随时钟变 ⇒ ★ 同一窗口的披露会前后不一致）：")
        for k, a, b in ci_diff:
            print(f"      - {k}\n          真实: {a}\n          as-of: {b}")
    else:
        print("   ✔ 全部逐字节一致（★ 披露用 pre 侧键时钟无关 ⇒ ★ 披露可复核）")

    # ── 完整判定链（★ Stage 路径）──
    mod = cc.load_verdict()
    rows, bad, guards = mod.judge(asof, stage=args.stage)
    print(f"\n== 预演裁决（★ as-of 时钟 · Stage {args.stage}）==")
    for name, val in guards:
        mark = "★★" if ("★★" in val or not val.strip()) else "✔"
        print(f"   {mark} {name}: {val}")
    if bad:
        print("   ★★ **量具守卫未通过 ⇒ 当天整表作废**")
    print()
    for label, base_cell, obs_cell, verdict in rows:
        print(f"   {verdict:<6} | {label}")
        print(f"          base: {base_cell}")
        print(f"          obs : {obs_cell}")

    # ── M20 三分量 + 逐模型样本量（★ Stage 1 报告必须并列）──
    print("\n== M20 normal 三分量（★ as-of 读数）==")
    fp, slow, swing = asof.get(FP_KEY), asof.get(SLOW_KEY), asof.get(SWING_KEY)
    print(f"   ① 指纹（fp 视图 · 无需结算）: {fp or '★ 键未产出'}")
    print(f"   ② slow（成熟视图 · 需结算）  : {slow or '★ 键未产出'}")
    print(f"   ③ 摇摆（featured 稳定性）    : {swing or '★ 键未产出'}")
    print("\n== 逐模型样本量（★ 不得只报总数）==")
    per = sorted((k, v) for k, v in asof.items() if k.startswith(PERMODEL_PREFIX))
    if not per:
        print("   ★ 无逐模型键")
    for k, v in per:
        print(f"   {k[len(PERMODEL_PREFIX):]:<42} {v}")

    # ── 纪律③：投影只是样本下界 ──
    print("\n== ★★★ 投影的边界（★ 必须随结论一起发布）==")
    print(f"   ★ as-of = {asof_txt} +08 是**未来时刻** ⇒ ★ 数据库里**还没发生**的事件不在投影里")
    print(f"   ★ as-of 下的 post 样本量 = {field(fp, 'n_all') or '?'}（★ 指纹视图）")
    print(f"     ⇒ ★★ 这是**下界** ⇒ ★★ 关窗真读的样本量会**更大** ⇒ ★★ 投影只判「方向」，不判「终局」")
    print("   ★★ 任何结论都以**关窗当天真读**为准；★ 投影判负 ⇒ 大概率真负；★ 投影未负 ⇒ 关窗后仍可能翻")
    print(f"   ★★ M20 slow 分量的阈值来自**干净 pre**（★ Stage {args.stage} 用 pre{24 if args.stage == 1 else 7}d）"
          f" ⇒ ★ 关窗当天会随 composition 键重算")
    # ★★ 第 21 处口径修正：★ 把判定器的两条新边界**引到投影输出里**（★ 避免两处各说一半）
    #   ★ 「相位效应」：★ 投影的 post 窗口在**数据分布上**仍是当前已积累的那一段
    #   ★   ⇒ ★★ 任何按「窗口内事件构成」计算的判据（★ 尤其是对照组的中位数）在投影里可能相位失真
    #   ★ 「只投影时钟」：★ 投影能可靠暴露时钟/成熟度类缺陷，但对构成类判据只能提示、不能定论
    # ★★ 第 24 处口径修正（★ 2026-11-11）：★ 把「post 段必须被当前时刻界定」这条边界也引进来
    #   ★ 理由：★ 投影**就是**靠替换 `now()` 生效的 ⇒ ★ 若某个判据的 post 段**无界**
    #   ★   ⇒ ★★ 投影对它**无效** ⇒ ★★ 它在负控/预演里恒真 ⇒ ★★ 没有牙
    #   ★   ⇒ ★★ 投影输出必须显式带上这条 ⇒ ★★ 读者才知道哪些判据的投影值是「有牙的」
    # ★★ §671（★ 2026-11-11 观察层事件）：★ 投影输出还要带上「第二次部署 + 三核查 + 代码解耦 + 等长窗口实测」
    #   ★ 理由：★ 投影是**关窗当天第 0 步** ⇒ ★★ 若窗口内又换了构建 ⇒ ★★ 预演必须自带这条边界
    vmod = cc.load_verdict()
    for ln in vmod.honest_boundary_lines():
        if "㉖" in ln or "㉗" in ln or "㉚" in ln or "㉦" in ln or "㉩" in ln or "㉭" in ln or "㉰" in ln:
            print("   " + ln.strip())

    if missing or diff or ci_missing or ci_diff or bad:
        print("\n★★ **关窗预演未通过 ⇒ ★ 关窗当天会作废 ⇒ 必须先修口径**")
        return 1
    print("\n   ✔ 关窗预演通过：★ pre 侧零漂移 + 守卫全绿 + 判定链走通")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
