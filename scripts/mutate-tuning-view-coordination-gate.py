#!/usr/bin/env python3
"""tuning refresher 跨实例协调判据门的变异验证脚本（R51 96h 轮）。

每条变异改坏一处 ⇒ 对应判据必须变红。
另有一条**对照变异**（只改日志文案）⇒ 判据必须**仍然绿**
（证明门不是「任何改动都红」的恒红门）。

用法：python3 scripts/mutate-tuning-view-coordination-gate.py
"""
import pathlib
import shutil
import subprocess
import sys
import tempfile

REPO = pathlib.Path(__file__).resolve().parents[1]
TARGET = REPO / "bg" / "tuning_view_refresher.go"
TEST = "./bg/"

MUTATIONS = [
    ("M1-改回普通 ticker(墙钟对齐腿)",
     "return nextAlignedWait(now, r.tick)",
     "return r.tick",
     "red"),
    ("M2-去掉 follower 早退(不碰数据库腿)",
     """			for _, v := range TuningViews {
				recordMVRefreshSkipped(v, "skipped_follower")
			}
			return nil
		}""",
     """			for _, v := range TuningViews {
				recordMVRefreshSkipped(v, "skipped_follower")
			}
		}""",
     "red"),
    ("M3-advisory 锁键复用 MV 的(锁键独立性腿)",
     "tuningViewAdvisoryLockKey int64 = 915_2026_10_20",
     "tuningViewAdvisoryLockKey int64 = mvRefreshLockKey",
     "red"),
    ("M4-令牌 logical 复用 MV 的(锁键独立性腿)",
     'tuningViewDistLockLogical   = "tuning_view_refresh"',
     "tuningViewDistLockLogical   = mvRefreshDistLockLogical",
     "red"),
    ("M5-令牌 TTL 跨窗口(TTL 腿)",
     "tuningViewDistLockTTL = 90 * time.Second",
     "tuningViewDistLockTTL = 10 * time.Minute",
     "red"),
    ("M6-失败不记指标(失败可见腿)",
     "			recordMVRefreshFailure(view)\n",
     "",
     "red"),
    ("M7-成功不记指标(指标接线腿)",
     "			recordMVRefreshSuccess(view, elapsed.Seconds())\n",
     "",
     "red"),
    ("M8-follower 跳过不记指标(尺子腿)",
     "				recordMVRefreshSkipped(v, \"skipped_follower\")\n",
     "",
     "red"),
    ("M9-去掉白名单校验(白名单腿)",
     """	if !isTuningView(view) {
		return tuningSkippedMissingView, 0,
			fmt.Errorf("refusing to refresh non-whitelisted materialized view %q", view)
	}
""",
     "",
     "red"),
    ("M10-周期层不释放令牌(释放腿)",
     "		defer handle.Release(context.WithoutCancel(ctx))\n",
     "",
     "red"),
    ("C0-只改日志文案(对照, 必须仍绿)",
     'slog.Info("tuning view refresh: redis leader token acquired")',
     'slog.Info("tuning view refresh: redis leader token acquired (v2)")',
     "green"),
]


def run_gate():
    r = subprocess.run(
        ["go", "test", TEST, "-run", "TuningViewRefresher", "-count=1"],
        cwd=REPO, capture_output=True, text=True,
    )
    return r.returncode == 0, r.stdout + r.stderr


def failing_lines(out):
    """把失败的那几条腿点名 —— 没有它，一次「对照变异变红」只能靠猜。"""
    return [l.strip() for l in out.splitlines()
            if l.strip().startswith("--- FAIL") or l.strip().startswith("FAIL\t")
            or "build failed" in l or "cannot use" in l]


def main():
    backup = pathlib.Path(tempfile.mkdtemp()) / "tvr.go.orig"
    shutil.copy2(TARGET, backup)
    orig = backup.read_text()

    failures = []
    try:
        for name, old, new, expect in MUTATIONS:
            if old not in orig:
                print(f"!! {name}: 锚点文本在源文件里找不到，跳过")
                failures.append(name)
                continue
            TARGET.write_text(orig.replace(old, new, 1))
            green, out = run_gate()
            if expect == "red":
                if green:
                    print(f"FAIL {name}: 变异后判据仍然全绿 ⇒ 这条腿抓不住该缺陷")
                    failures.append(name)
                else:
                    fails = failing_lines(out)
                    print(f"OK   {name}: 判据变红 ({'; '.join(fails) if fails else 'FAIL'})")
            else:
                if green:
                    print(f"OK   {name}: 判据仍然全绿（无假阳性）")
                else:
                    fails = failing_lines(out)
                    print(f"??  {name}: 第一轮变红，失败腿 = {fails or '(未解析出腿名)'}；重跑一次判定是真假阳性还是不稳定")
                    green2, out2 = run_gate()
                    if green2:
                        print(f"OK   {name}: 重跑后全绿 ⇒ 上一轮是**不稳定**（不是门的假阳性）")
                    else:
                        print(f"FAIL {name}: 重跑仍然变红 ⇒ 门有假阳性，失败腿 = {failing_lines(out2)}")
                        failures.append(name)
    finally:
        TARGET.write_text(orig)
        shutil.rmtree(backup.parent, ignore_errors=True)

    if TARGET.read_text() != orig:
        print("!! 源文件未还原干净")
        failures.append("restore")

    if failures:
        print(f"\n=== 变异验证失败: {failures}")
        return 1
    print("\n=== 变异验证全部通过（每条腿都能红，对照变异不红）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
