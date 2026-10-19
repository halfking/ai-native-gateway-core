#!/usr/bin/env python3
"""autoroute SQL 形态门的变异验证脚本（R51 96h 轮）。

每条变异改坏一处 ⇒ 对应判据必须变红。
另有一条**对照变异**（只加注释）⇒ 判据必须**仍然绿**
（证明门不是「任何改动都红」，也不是「注释里的字样也算违规」）。

用法：python3 scripts/mutate-autoroute-index-sql-gate.py
"""
import pathlib
import shutil
import subprocess
import sys
import tempfile

REPO = pathlib.Path(__file__).resolve().parents[1]
TARGET = REPO / "autoroute" / "index.go"
TEST = "./autoroute/"

MUTATIONS = [
    # (名称, 旧文本, 新文本, 期望: red=该腿必须红, green=必须仍绿)
    ("M1-DISTINCT-ON(性能腿)",
     "SELECT credential_id, raw_model, MAX(bucket) AS bucket",
     "SELECT DISTINCT ON (credential_id, raw_model) credential_id, raw_model, MAX(bucket) AS bucket",
     "red"),
    ("M2-去掉分组(语义腿)",
     "    GROUP BY credential_id, raw_model\n)",
     ")",
     "red"),
    ("M3-换第三种形态(准入闸)",
     "JOIN latest_bucket",
     "JOIN lb_alias",
     "red"),
    ("M4-把过滤推进去重段(位置腿)",
     "    FROM credential_model_index_with_current_month\n    GROUP BY credential_id, raw_model\n)",
     "    FROM credential_model_index_with_current_month\n    JOIN credentials cr2 ON cr2.id = credential_id\n    GROUP BY credential_id, raw_model\n)",
     "red"),
    ("M5-事实源换成冷父表(事实源腿)",
     "FROM credential_model_index_with_current_month",
     "FROM credential_model_index ",
     "red"),
    ("M6-去掉 ORDER BY(收尾腿)",
     "ORDER BY cmi.canonical_id, cmi.score_smart DESC",
     "",
     "red"),
    ("M7-丢掉可用性过滤(收尾腿)",
     "  AND cmb.available = TRUE\n",
     "",
     "red"),
    ("C0-只加注释(对照, 必须仍绿)",
     "const refreshIndexSQL = `\n",
     "const refreshIndexSQL = `\n-- 说明：这里刻意没用 DISTINCT ON，原因是 252 真库实测它慢 2~3 倍\n",
     "green"),
]


def run_gate():
    r = subprocess.run(
        ["go", "test", TEST, "-run", "AutoRouteRefreshIndex", "-count=1"],
        cwd=REPO, capture_output=True, text=True,
    )
    return r.returncode == 0, r.stdout + r.stderr


def failing_lines(out):
    """把失败的那几条腿点名 —— 没有它，一次「对照变异变红」只能靠猜。"""
    return [l.strip() for l in out.splitlines()
            if l.strip().startswith("--- FAIL") or l.strip().startswith("FAIL\t")
            or "build failed" in l or "cannot use" in l]


def main():
    backup = pathlib.Path(tempfile.mkdtemp()) / "index.go.orig"
    shutil.copy2(TARGET, backup)
    orig = backup.read_text()

    failures = []
    try:
        for name, old, new, expect in MUTATIONS:
            if old not in orig:
                print(f"!! {name}: 锚点文本在源文件里找不到，跳过")
                failures.append(name)
                continue
            TARGET.write_text(orig.replace(old, new))
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
                    print(f"FAIL {name}: 对照变异竟然让判据变红 ⇒ 门把注释当成了违规")
                    failures.append(name)
    finally:
        TARGET.write_text(orig)
        shutil.rmtree(backup.parent, ignore_errors=True)

    # 还原校验
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
