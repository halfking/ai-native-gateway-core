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
}
EFFECT_LABELS = ("新覆盖人群（★ 效应）",)


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
        f'DSN=$(docker exec llm-gateway-211-b env | grep LLM_GATEWAY_DATABASE_URL | cut -d= -f2-) && '
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
        pat = re.compile(r'("%s": \{"n": )\d+' % re.escape(bkey))
        src, c = pat.subn(lambda mm: f"{mm.group(1)}{n}", src)
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
    print("\n== 负控守卫 ==")
    for name, val in guards:
        mark = "★★" if ("★★" in val or not val.strip()) else "✔"
        print(f"   {mark} {name}: {val}")
    if bad:
        print(f"\n★★ 负控自身有守卫失败：{bad} ⇒ 先查口径，勿读裁决")

    print("\n== 负控裁决（★ 期望：效应判据**未达成**）==")
    failures = []
    seen_effect = 0
    for r in rows:
        label, base, obs, verdict_txt = r
        is_effect = any(e in label for e in EFFECT_LABELS)
        if is_effect:
            seen_effect += 1
            print(f"   | {label}\n       基线: {base}\n       观测: {obs}\n       裁决: {verdict_txt}")
            if verdict_txt.startswith("✅"):
                failures.append(f"[负控] 效应判据被判达成 ⇒ ★★ 判据**恒真** ⇒ 门没牙 ⇒ {label}")
            elif not (verdict_txt.startswith("❌") or verdict_txt.startswith("⚠️")):
                failures.append(f"[负控] 效应判据的裁决形态异常 ⇒ {label}: {verdict_txt}")
        elif "对照组" in label or "无回归" in label:
            print(f"   | {label}\n       观测: {obs}\n       裁决: {verdict_txt}")
    if seen_effect == 0:
        failures.append("[负控] 没找到效应判据行 ⇒ 判据集结构变了")

    print()
    if failures:
        for f in failures:
            print("   ❌ " + f)
        print("\n★★ 负控未通过")
        return 1
    print(f"   ✔ 负控通过：★ {seen_effect} 条效应判据在**修复前**的窗口全部判「未达成」")
    print("     ⇒ ★★ 判据**不是恒真** ⇒ 明天关窗读出的「达成/未达成」可信")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
