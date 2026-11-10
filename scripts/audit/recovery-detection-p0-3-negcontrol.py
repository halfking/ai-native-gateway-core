#!/usr/bin/env python3
"""★★ 验收判据的**负控**（★ 修复前 era 干跑）：★ 证伪「恒真判据」

★ 为什么要它：★★ 判据有两种坏法 ——
  ① **恒真**（★ 恒过 ⇒ 什么都能"验证通过"⇒ ★★ 门没牙）
  ② **恒假**（★ 恒不过 ⇒ ★★ 什么结论都出不来）
⇒ ★★ **两者都不会在「关窗读数」里暴露** ⇒ ★★ 必须在关窗前用**已知答案**的窗口验一次。

★ 怎么造负控：★★ **把 era 切点整体前移 N 天**（★ 只改 `/tmp` 里的临时副本，
★ **不动仓库脚本、不重划真实 era 切点**）⇒ ★★ 「post」段就落在**修复之前**的窗口
⇒ ★★ 于是同一把尺子必须判 **❌ 未达成**（★ 期望的答案已知）。

★ 用法（本地）：
    python3 scripts/audit/recovery-detection-p0-3-negcontrol.py --shift-days 7 --stage 2
    python3 scripts/audit/recovery-detection-p0-3-negcontrol.py --shift-days 1 --stage 1

★ 判定：★ 效应判据行必须是 **❌ 未达成**（或 free 切片的「⚠️ 样本不足」）
⇒ ★★ 若它判 ✅ ⇒ ★★ **判据恒真 ⇒ 门没牙 ⇒ 禁止用这份判据下结论**。
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


def run_shifted_sql(shift_days: int, ssh_host: str) -> str:
    """★ 把 era 切点整体前移 shift_days 天并跑真库（★ 只用临时副本）"""
    sql = SQL_PATH.read_text(encoding="utf-8")
    m = re.search(r"TIMESTAMPTZ '(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\+08'", sql)
    assert m, "★ 采集 SQL 里找不到 era 切点字面量"
    from datetime import datetime, timedelta
    cut = datetime.strptime(m.group(1), "%Y-%m-%d %H:%M:%S")
    new_cut = cut - timedelta(days=shift_days)
    new_lit = f"TIMESTAMPTZ '{new_cut:%Y-%m-%d %H:%M:%S}+08'"
    n = sql.count(m.group(0))
    variant = sql.replace(m.group(0), new_lit)
    print(f"== 负控：era 切点 {m.group(1)} → {new_cut:%Y-%m-%d %H:%M:%S}（{n} 处）==")
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
    args = ap.parse_args()

    raw = run_shifted_sql(args.shift_days, args.ssh_host)
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
    for r in rows:
        label, base, obs, verdict_txt = r
        is_effect = any(e in label for e in EFFECT_LABELS)
        if is_effect:
            seen_effect += 1
            print(f"   | {label}\n       基线: {base}\n       观测: {obs}\n       裁决: {verdict_txt}")
            # ★★★ 负控必须**判负**（★ 本轮收紧：★ 原来 ⚠️ 也算「通过」⇒ ★★ 负控什么也证伪不了）
            #   ★★ 效应判据在「修复前」的窗口里**必须**读到「未达成」
            #   ★★ 若落 ⚠️（不可判/样本不足）⇒ ★★ 说明这一轮负控**没有证伪任何东西** ⇒ 门没牙
            if not verdict_txt.startswith("❌"):
                failures.append(f"[负控] 效应判据**未判负**（★ 必须 ❌）⇒ ★★ 这一轮负控什么也证伪不了"
                                f" ⇒ 门没牙 ⇒ {label}: {verdict_txt}")
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

    print()
    if failures:
        for f in failures:
            print("   ❌ " + f)
        print("\n★★ 负控未通过")
        return 1
    print(f"   ✔ 负控通过：★ {seen_effect} 条效应判据（M20 分层）在**修复前**的窗口全部判「未达成」")
    print(f"     ★ 另：{seen_observe} 条混合口径行落「观察面（⚠️）」⇒ ★ 不会被误读成判据")
    print("     ⇒ ★★ 判据**不是恒真** ⇒ 明天关窗读出的「达成/未达成」可信")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
