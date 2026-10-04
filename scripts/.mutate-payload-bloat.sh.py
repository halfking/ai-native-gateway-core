#!/usr/bin/env python3
"""payload 膨胀巡检脚本的行为门变异验证。

门是 Go 测试，被测对象是 shell 脚本。纪律同前：
  1. 变异后逐字节还原（md5）
  2. 必须让**目标门**转红
  3. 打出实际跑的用例数；0 条直接判为脚本错误
"""
import hashlib
import os
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(REPO, "scripts/252-monitor/ursm-snapshot-payload-bloat.sh")

MUTATIONS = [
    ("M39",
     '[ "$n" -gt 0 ]    || { echo "ABORT: 样本为 0 行（表空？）—— 没有参照系，本次不出结论。" >&2; exit 3; }',
     '[ "$n" -ge 0 ]   || { echo "ABORT: 样本为 0 行（表空？）—— 没有参照系，本次不出结论。" >&2; exit 3; }',
     "TestBloatScriptZeroRowsIsNoConclusionNotHealthy",
     "★ 去掉空样本守卫 ⇒ 表被清空时报「健康」。这正是 go-cache-guard 事故的同款形态："
     "「查不到」被记成「没问题」，天天报绿"),

    ("M40",
     '[ "$nfield" = "4" ] || { echo "ABORT: 期望 4 个字段，实得 $nfield" >&2; exit 3; }',
     '[ "$nfield" -ge 1 ] || { echo "ABORT: 期望 4 个字段，实得 $nfield" >&2; exit 3; }',
     "TestBloatScriptWrongFieldCountAborts",
     "放松字段数校验 ⇒ psql 输出意外时下游把缺失字段当 0 ⇒ 报 OK"),

    ("M41",
     'if [ "$newest_age" -gt "$STALE_SECONDS" ] 2>/dev/null; then',
     'if false; then',
     "TestBloatScriptStaleSampleIsNoConclusion",
     "去掉样本新鲜度守卫 ⇒ 写入停了之后仍拿历史数据当现状报"),

    ("M42",
     'BLOAT_BYTES=${BLOAT_BYTES:-64}',
     'BLOAT_BYTES=${BLOAT_BYTES:-64000}',
     "TestBloatScriptBloatExitsOne",
     "阈值放大到永不触发 ⇒ 膨胀永远检不出，脚本退化成永远 OK"),

    ("M43",
     'if [ "$p95_b" -le "$BLOAT_BYTES" ]; then',
     'if [ "$p95_b" -le "$BLOAT_BYTES" ] || grep -q "updated_at_ms" /dev/null; then',
     "TestBloatScriptVerdictDoesNotDependOnKeyNames",
     "让判据开始看键名 ⇒ 键名一漂就静默恒绿（该门钉住的就是这一点）"),
]


def md5(p):
    with open(p, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def run(cmd):
    return subprocess.run(cmd, shell=True, cwd=REPO, capture_output=True, text=True)


def main():
    orig = md5(SCRIPT)
    failures = []
    only = set(sys.argv[1:])
    selected = [m for m in MUTATIONS if not only or m[0] in only]

    for mid, old, new, target, desc in selected:
        with open(SCRIPT, encoding="utf-8") as f:
            body = f.read()
        if old not in body:
            print(f"[{mid}] SKIP 锚点未命中，脚本自身失效：{desc}")
            failures.append(f"{mid}: anchor not found")
            continue
        with open(SCRIPT, "w", encoding="utf-8") as f:
            f.write(body.replace(old, new, 1))
        try:
            b = run("bash -n scripts/252-monitor/ursm-snapshot-payload-bloat.sh")
            if b.returncode != 0:
                print(f"[{mid}] SYNTAX-BROKEN 不算证据：{desc}")
                failures.append(f"{mid}: syntax broken")
                continue
            r = run(f"go test ./scripts/ursmcheck/ -run {target} -v -count=1")
            out = r.stdout + r.stderr
            ran = out.count("=== RUN")
            if ran == 0:
                print(f"[{mid}] 跑了 0 条 ⇒ 不可判定")
                failures.append(f"{mid}: 0 tests ran")
                continue
            red = r.returncode != 0
            print(f"[{mid}] {'RED ✓' if red else 'GREEN ✗ 无牙'}  ran={ran}  {desc}")
            if not red:
                failures.append(f"{mid}: gate stayed green")
        finally:
            with open(SCRIPT, "w", encoding="utf-8") as f:
                f.write(body)

    ok = md5(SCRIPT) == orig
    print(f"\n--- 逐字节还原 ---\n{'OK  ' if ok else 'FAIL'} {os.path.basename(SCRIPT)} {orig[:12]}")
    if not ok:
        failures.append("script not restored")

    print("\n=== 结论 ===")
    if failures:
        for x in failures:
            print("✗ " + x)
        return 1
    print(f"全部 {len(selected)} 条变异均有牙，且逐字节还原")
    return 0


if __name__ == "__main__":
    sys.exit(main())
