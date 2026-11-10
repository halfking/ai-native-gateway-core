#!/usr/bin/env python3
"""★★ **三时钟一致性检查**：★ pre 键在【真实时钟】与【Stage 2 未来时钟】下必须**逐字节一致**

★★ 为什么需要它（★ 本轮真踩到，★ 而且是**同一个坑第二次**）：
① §656：★ 取数窗口写成滚动的 `created_at > now() - interval '7 days'`
   ⇒ ★★ Stage 2 模拟（now = 2026-11-17）时 **pre 侧整段消失**（窗口起点晚于 era 切点）
   ⇒ ★★ 复采的 pre 与冻结基线**不同源**（★ 实测同一锚点下 pre 事件数 1833 → 332）。
② ★★ 本轮：新写的 **M20** 块的豁免分类里写了 `quota_recover_at > now()`
   ⇒ ★★ **用「当前时钟」去判「历史事件」** ⇒ ★★ 在 Stage 2 的未来时钟下分层归属整体改变
   （★ 实测 normal 154→459、exempt 537→232，★ **总数 691 不变** ⇒ 纯分层污染）
   ⇒ ⇒ ★★ **M18 守卫会报「pre 基线漂移」⇒ 整表作废 ⇒ ★★ Stage 2 直接跑不了**
   ⇒ ★★ 修法：★ 谓词锚定**事件时点**（`quota_recover_at > created_at`）⇒ 时钟无关 ✔

★★★ **因此本脚本是门，不是体检**：★ 凡「判历史事件的谓词」用了 `now()` ⇒ ★★ pre 基线随时钟漂移
⇒ ★★ 而漂移**只在 Stage 2 才暴露**（★ Stage 1 用的是当前时钟 ⇒ 看不出来）⇒ ⇒ ★★ 必须提前验。

★ 纪律（★ 由本脚本保障）：
① ★ **采集口径里凡是判历史事件的谓词，一律不得用 `now()`**（★ 该用切点锚定的用切点，
   ★ 该用事件时点的用事件时点）；
② ★★ **改采集脚本后必须跑一次本脚本** ⇒ pre 键逐字节一致才算过；
③ ★★ 顺带验「**Stage 2 判定路径能走通**」（★ 未来时钟下 M18/M19 守卫必须全绿、关键裁决行必须出行）。

★ 用法：
    python3 scripts/audit/recovery-detection-p0-3-clockcheck.py
    python3 scripts/audit/recovery-detection-p0-3-clockcheck.py --future "2026-11-17 13:45:00+08"

★ 退出码：0 = 通过；1 = 有差异（★ **pre 键漂移 ⇒ Stage 2 会跑挂**）。
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

# ★ pre 侧键（★ 判定器用来算阈值/冻结对拍的那些）⇒ 必须在各时钟下逐字节一致
#   ★★ M15x_pre<窗>__<金丝雀>（★ 本轮第 13 处口径修正新增）★ 也必须进来
#     ⇒ ★★ 否则「M15x 的 pre 键是否时钟无关」**没人查** ⇒ ★ 漂移只会在 Stage 2 暴露
PRE_KEY_RE = re.compile(
    r"^(M14_(pre24|pre7d|pre6d)_|M14x_(pre24|pre7d|pre6d)_|M20_(normal|exempt)_(pre24|pre7d|pre6d)$|"
    r"^M20_era_dropped_(pre24|pre7d)$|M15x_(pre24|pre7d)__|"
    r"^M20_(normal|exempt)_(pre24|pre7d|pre6d)_composition$|"
    # ★★ 第 18 处口径修正：★ 「指纹视图」的 pre 键（★ 无成熟度过滤 ⇒ **必须**时钟无关
    #   ★★ ⇒ ★ 若它随时钟变 ⇒ ★ 说明 fp 视图里混进了 now() 依赖 ⇒ ★ Stage 2 会跑挂）
    r"^M20_(normal|exempt)_(pre24|pre7d)_fp$)"
)

# ★★★ 第 22 处口径修正：★ 「**披露用**」的 pre 侧键（★ **不进 M18 冻结对拍** ⇒ ★ 不重冻基线）
#   ★★ 但它仍是 **pre 侧读数** ⇒ ★★ 若混进 `now()` 依赖 ⇒ ★ 随时钟漂移
#   ★★ ⇒ ★★ 必须在各时钟下逐字节一致 ⇒ ★★ **单独查**（★ 不扩冻结面 ⇒ ★ 不触发重冻）
CLOCK_INDEP_KEYS = ("M5_pre7d_runs_by_day", "M5_roll24_pre7d_dist")


def run_sql(sql_text: str, tag: str, ssh_host: str) -> str:
    local = pathlib.Path(f"/tmp/clockcheck_{tag}.sql")
    local.write_text(sql_text, encoding="utf-8")
    subprocess.run(["scp", "-q", str(local), f"{ssh_host}:{local}"], check=True)
    # ★★ psql 必须 **-v ON_ERROR_STOP=1**（★ 本轮自踩：★ 缺这条 ⇒ 运行期错误（如时间戳非法）
    #   ★★ 被 psql 吞掉、returncode 仍为 0、输出为空 ⇒ ★★ 看起来像「未来时钟缺键」
    #   ★★ ⇒ ★★ 静默失效。★ 同族脚本 negcontrol.py 里本来就有这一条 ⇒ ★ 要抄）
    remote = (f"docker cp {local} acc-pg17:{local} && "
              f"docker exec acc-pg17 psql -U acc_super -d llm_gateway -tA -F\"|\" "
              f"-v ON_ERROR_STOP=1 -f {local}")
    out = subprocess.run(["ssh", ssh_host, remote], capture_output=True, text=True)
    if out.returncode != 0:
        print(out.stdout[-2000:], out.stderr[-2000:], file=sys.stderr)
        raise SystemExit(f"★ 采集失败 rc={out.returncode}（tag={tag}）")
    return out.stdout


def parse(raw: str) -> dict[str, str]:
    d: dict[str, str] = {}
    for line in raw.splitlines():
        if "|" not in line:
            continue
        k, _, v = line.partition("|")
        d[k.strip()] = v.strip()
    return d


def load_verdict():
    spec = importlib.util.spec_from_file_location("vclock", VERDICT_PATH)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--future", default="2026-11-17 13:45:00",
                    help="★ Stage 2 的「现在」时刻（★ 不带时区，★ 脚本统一拼 +08；★ 默认 = Stage 2 关窗时刻）")
    ap.add_argument("--ssh-host", default="211")
    ap.add_argument("--sql", default=str(SQL_PATH),
                    help="★ 指定采集脚本（★ 变异验证时指向被改坏的副本）")
    args = ap.parse_args()

    src = pathlib.Path(args.sql).read_text(encoding="utf-8")
    n_now = src.count("now()") + src.count("NOW()")
    # ★ 时区只在这里拼一次（★ 本轮自踩：★ 默认值已带 +08、这里又拼 ⇒ `+08+08` ⇒ 运行期非法时间戳）
    fut = f"TIMESTAMPTZ '{args.future.strip()}+08'"
    variant = src.replace("now()", fut).replace("NOW()", fut)

    print(f"== 三时钟一致性检查 ==")
    print(f"   真实时钟 → 现在；未来时钟 → {fut}（★ 替换 now()/NOW() 共 {n_now} 处）")

    real_raw = run_sql(src, "real", args.ssh_host)
    fut_raw = run_sql(variant, "future", args.ssh_host)
    real, future = parse(real_raw), parse(fut_raw)

    # ★★ 先验「未来时钟这次采集**有效**」（★ 否则会把「采集失败」误读成「pre 键漂移」）
    if len(future) < max(10, len(real) * 0.5):
        print(f"   ★★ 未来时钟采集**无效**（★ 真实 {len(real)} 键 vs 未来 {len(future)} 键）"
              f" ⇒ ★ 这是采集失败（★ 不是口径漂移）⇒ ★ 先看 psql 报错")
        return 1
    pre_keys = sorted(k for k in real if PRE_KEY_RE.match(k))
    missing = [k for k in pre_keys if k not in future]
    diff = [(k, real[k], future[k]) for k in pre_keys if k in future and real[k] != future[k]]

    print(f"\n== pre 键逐字节比对（★ {len(pre_keys)} 个）==")
    if missing:
        print(f"   ★★ 未来时钟**缺键** {len(missing)} 个：{missing[:6]}")
    if diff:
        print(f"   ★★ **漂移 {len(diff)} 个**（★ ⇒ pre 基线随时钟变 ⇒ ★ Stage 2 会报「基线漂移」并作废整表）：")
        for k, a, b in diff:
            print(f"      - {k}\n          真实: {a}\n          未来: {b}")
    else:
        print("   ✔ 全部逐字节一致（★ pre 基线时钟无关）")

    # ★★★ 第 22 处口径修正：★ 「披露用 pre 侧键」的**时钟无关性**（★ 单独查 ⇒ 不扩 M18 冻结面）
    ci_missing = [k for k in CLOCK_INDEP_KEYS if k not in real or k not in future]
    ci_diff = [(k, real.get(k, ""), future.get(k, ""))
               for k in CLOCK_INDEP_KEYS if k in real and k in future and real[k] != future[k]]
    print(f"\n== 披露用 pre 侧键的时钟无关性（★ {len(CLOCK_INDEP_KEYS)} 个）==")
    if ci_missing:
        print(f"   ★★ 未来时钟**缺键** {len(ci_missing)} 个：{ci_missing}")
    if ci_diff:
        print(f"   ★★ **漂移 {len(ci_diff)} 个**（★ ⇒ 该键随时钟变 ⇒ ★ 同一窗口的披露会前后不一致）：")
        for k, a, b in ci_diff:
            print(f"      - {k}\n          真实: {a}\n          未来: {b}")
    else:
        print("   ✔ 全部逐字节一致（★ 披露用 pre 侧键时钟无关）")

    # ★★ 顺带：未来时钟下跑一遍 Stage 2 判定链 ⇒ 验「Stage 2 路径能走通」
    print("\n== Stage 2 判定路径（★ 用未来时钟的复采输出）==")
    mod = load_verdict()
    rows, bad, guards = mod.judge(future, stage=2)
    guard_fail = [(name, val) for name, val in guards if ("★★" in val) or (not val.strip())]
    for name, val in guards:
        print(f"   {'★★' if (name, val) in guard_fail else '✔'} {name}: {val}")
    labels = {r[0] for r in rows}
    need = ["M20 新覆盖组·非豁免（★ 效应主判据）", "M20 新覆盖组·豁免（★ 不回归守卫）",
            "M14x 新覆盖人群（★ 混合口径·观察面）"]
    missing_rows = [x for x in need if x not in labels]
    if missing_rows:
        print(f"   ★★ Stage 2 裁决行缺失：{missing_rows}")
    if guard_fail or missing_rows:
        print("\n★★ **三时钟检查未通过**")
        return 1
    print("   ✔ Stage 2 路径可走通（★ 守卫全绿 + 关键裁决行齐备）")

    print()
    if missing or diff:
        print("★★ **三时钟检查未通过 ⇒ ★ Stage 2 跑之前必须先修口径**")
        return 1
    # ★★★ 第 22 处口径修正：★ 披露用 pre 侧键漂移 ⇒ **同样**不许过（★ 否则披露会前后不一致）
    if ci_missing or ci_diff:
        print("★★ **三时钟检查未通过 ⇒ ★ 披露用 pre 侧键随时钟漂移 ⇒ ★ 必须先修口径**")
        return 1
    print("   ✔ 三时钟检查通过：★ pre 基线时钟无关 ⇒ ★ Stage 2 可直接跑")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
