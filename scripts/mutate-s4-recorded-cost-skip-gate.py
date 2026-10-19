#!/usr/bin/env python3
"""S4 停写门（recorded_cost_is_negative 跳过）的变异验证脚本（R51 续轮）。

每条变异改坏一处 ⇒ 对应判据必须变红。
另有一条**对照变异**（只改日志文案）⇒ 判据必须**仍然绿**。

用法：python3 scripts/mutate-s4-recorded-cost-skip-gate.py
"""
import pathlib
import shutil
import subprocess
import sys
import tempfile

REPO = pathlib.Path(__file__).resolve().parents[1]
TEST = "./bg/"

# (相对路径, 旧文本, 新文本, 期望)
MUTATIONS = [
    ("bg/routing_health_checks.go",
     "func recordedCostComparability(logsWriteEnabled bool) (comparable bool, reason string) {\n\tif !logsWriteEnabled {",
     "func recordedCostComparability(logsWriteEnabled bool) (comparable bool, reason string) {\n\tif false && !logsWriteEnabled {",
     "red", "M1-永远判定为可判定(判定函数腿)"),

    ("bg/routing_health_checks.go",
     "\t\t\tcomparable, reason := recordedCostComparability(requestLogsWriteEnabled())\n\t\t\tif comparable {\n\t\t\t\treturn false, \"\"\n\t\t\t}\n\t\t\treturn true, reason",
     "\t\t\treturn false, \"\"",
     "red", "M2-守卫换成恒不跳(检查定义腿)"),

    ("bg/routing_health_checks.go",
     "\t\t\tcomparable, reason := recordedCostComparability(requestLogsWriteEnabled())\n\t\t\tif comparable {\n\t\t\t\treturn false, \"\"\n\t\t\t}\n\t\t\treturn true, reason",
     "\t\t\tcomparable, reason := recordedCostComparability(requestLogsWriteEnabled())\n\t\t\tif comparable {\n\t\t\t\treturn true, \"\"\n\t\t\t}\n\t\t\treturn false, reason",
     "red", "M2b-守卫极性接反(本轮真踩到的缺陷)"),

    ("bg/routing_health_checks.go",
     "\t\tif chk.SkipWhen != nil {",
     "\t\tif false && chk.SkipWhen != nil {",
     "red", "M3-守卫接不进 runner(短路腿)"),

    ("bg/routing_health_checks.go",
     "\t\t\t\tif recorder != nil {\n\t\t\t\t\trecorder.markSkipped(reason)\n\t\t\t\t}",
     "\t\t\t\tif false && recorder != nil {\n\t\t\t\t\trecorder.markSkipped(reason)\n\t\t\t\t}",
     "red", "M4-跳过不记进 recorder(可观测腿)"),

    ("bg/s4_scan_skip_metrics.go",
     "\t\ts4ScanWorkerRoutingHealthChecker,\n",
     "",
     "red", "M5-worker 不在闭集(闭集腿)"),

    ("bg/routing_health_checker.go",
     "\tdefer recordS4ScanSkipState(s4ScanWorkerRoutingHealthChecker, w.SkippedChecks(), time.Now())",
     "\trecordS4ScanSkipState(s4ScanWorkerRoutingHealthChecker, w.SkippedChecks(), time.Now())",
     "red", "M6-发布不是 defer(接线腿)"),

    ("bg/routing_health_checker.go",
     'slog.Info("routing_health_checker: run completed"',
     'slog.Info("routing_health_checker: run completed (v2)"',
     "green", "C0-只改日志文案(对照, 必须仍绿)"),
]


def run_gate():
    r = subprocess.run(
        ["go", "test", TEST, "-run",
         "RecordedCost|HealthCheckSkip|OnlyTheFrozenSource|SkippedCheckIsRecorded|"
         "RoutingHealthCheckerIsRegistered|S4ScanSkip",
         "-count=1"],
        cwd=REPO, capture_output=True, text=True,
    )
    return r.returncode == 0, r.stdout + r.stderr


def failing_lines(out):
    """把失败的那几条腿点名 —— 没有它，一次「对照变异变红」只能靠猜。"""
    return [l.strip() for l in out.splitlines()
            if l.strip().startswith("--- FAIL") or l.strip().startswith("FAIL\t")
            or "build failed" in l or "cannot use" in l]


def main():
    tmp = pathlib.Path(tempfile.mkdtemp())
    originals = {}
    for rel, _, _, _, _ in MUTATIONS:
        p = REPO / rel
        if rel not in originals:
            originals[rel] = (p, p.read_text())

    failures = []
    try:
        for rel, old, new, expect, name in MUTATIONS:
            path, orig = originals[rel]
            if old not in orig:
                print(f"!! {name}: 锚点文本在 {rel} 里找不到，跳过")
                failures.append(name)
                continue
            # ★ 每条变异之前把**所有**文件还原回原始态。
            # 只还原「本条要改的那个文件」是不够的：上一条变异可能改的是**另一个**文件
            # （本脚本的 M5 改 s4_scan_skip_metrics.go、M6/C0 改 routing_health_checker.go），
            # 残留会污染下一条 ⇒ 表现为「对照变异变红」这种**看似门的假阳性**。
            # （本轮真踩到过：M5 删掉闭集里的 worker 后没还原 ⇒ C0 在缺 worker 的树上跑 ⇒ 红。）
            for p, o in originals.values():
                p.write_text(o)

            path.write_text(orig.replace(old, new, 1))
            green, out = run_gate()
            if expect == "red":
                if green:
                    print(f"FAIL {name}: 变异后判据仍然全绿 ⇒ 这条腿抓不住该缺陷")
                    failures.append(name)
                else:
                    fails = failing_lines(out)
                    print(f"OK   {name}: 判据变红 ({'; '.join(fails) if fails else 'FAIL'})")
            else:
                # 对照变异变红有两种可能：门有假阳性，或这一轮跑出了**不稳定**。
                # 二者的处理完全不同（前者要改门，后者要改判据的量具），
                # 而「是哪条腿红的」是区分它们的第一手证据。
                # ⇒ 失败时重跑一次并把失败腿打出来；重跑还红 ⇒ 门有假阳性。
                fails = failing_lines(out)
                print(f"??  {name}: 第一轮变红，失败腿 = {fails or '(未解析出腿名)'}；重跑一次判定是真假阳性还是不稳定")
                green2, out2 = run_gate()
                if green2:
                    print(f"OK   {name}: 重跑后全绿 ⇒ 上一轮是**不稳定**（不是门的假阳性）")
                else:
                    print(f"FAIL {name}: 重跑仍然变红 ⇒ 门有假阳性，失败腿 = {failing_lines(out2)}")
                    failures.append(name)
    finally:
        for path, orig in originals.values():
            path.write_text(orig)
        shutil.rmtree(tmp, ignore_errors=True)

    for path, orig in originals.values():
        if path.read_text() != orig:
            print(f"!! {path} 未还原干净")
            failures.append("restore")

    if failures:
        print(f"\n=== 变异验证失败: {failures}")
        return 1
    print("\n=== 变异验证全部通过（每条腿都能红，对照变异不红）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
