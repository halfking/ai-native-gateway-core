#!/usr/bin/env python3
"""252 cron 正典文件三道门的变异验证。

被测：scripts/252-monitor/etc.cron.d.pg17
判据：scripts/ursmcheck/cron_registration_test.go

★ 特别关注 M44：它复现的正是 **2026-10-04 真实发生的漂移**
  （模板缺 ursm-snapshot-health.sh，而部署方式是整文件覆盖 ⇒
    下一次部署会把生产上每小时跑的巡检删掉）。
"""
import hashlib
import os
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CRON = os.path.join(REPO, "scripts/252-monitor/etc.cron.d.pg17")

HEALTH_LINE = ("17 * * * * root llmgw-source /opt/scripts/ursm-snapshot-health.sh "
               "2>&1 | tee -a /var/log/ursm-snapshot-health.log; "
               "find /var/log -maxdepth 1 -name \"ursm-snapshot-health.log.*\" -mtime +7 -delete")

MUTATIONS = [
    ("M44", HEALTH_LINE + "\n", "",
     "TestEveryMonitorScriptIsRegisteredInCron",
     "★ 复现真实事故：模板删掉线上正在跑的 ursm-snapshot-health.sh。"
     "部署是**整文件覆盖** ⇒ 这一行会让每小时巡检从生产消失"),

    ("M45", "23 * * * * root llmgw-source /opt/scripts/ursm-snapshot-payload-bloat.sh "
            "2>&1 | tee -a /var/log/ursm-snapshot-payload-bloat.log; "
            "find /var/log -maxdepth 1 -name \"ursm-snapshot-payload-bloat.log.*\" -mtime +7 -delete",
     "23 * * * * root llmgw-source /opt/scripts/ursm-snapshot-payload-bloat.sh >/dev/null 2>&1",
     "TestCronFileActuallyHasTaskLines",
     "把分级退出的巡检 >/dev/null 2>&1 掉 ⇒ 0/1/3 全被丢弃，等于没巡检"),

    ("M46", "# ★ 同步契约：本文件必须与服务器 /etc/cron.d/pg17 逐行一致。",
     "# cron 配置模板。",
     "TestCronFileKeepsTheSyncContractWarning",
     "把同步契约警示从头部删掉 ⇒ 没人再知道漏一条就等于删一条生产任务"),

    ("M47", "#   部署方式是**整文件覆盖**，不是合并 —— 少一条就会在覆盖时把生产任务删掉。",
     "#   部署时合并即可。",
     "TestCronFileKeepsTheSyncContractWarning",
     "把「整文件覆盖」改成「合并」⇒ 门失去它要守的那件事，且警示变成误导"),
]


def md5(p):
    with open(p, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def run(cmd):
    return subprocess.run(cmd, shell=True, cwd=REPO, capture_output=True, text=True)


def main():
    orig = md5(CRON)
    failures = []
    only = set(sys.argv[1:])
    selected = [m for m in MUTATIONS if not only or m[0] in only]

    for mid, old, new, target, desc in selected:
        with open(CRON, encoding="utf-8") as f:
            body = f.read()
        if old not in body:
            print(f"[{mid}] SKIP 锚点未命中，脚本自身失效：{desc}")
            failures.append(f"{mid}: anchor not found")
            continue
        with open(CRON, "w", encoding="utf-8") as f:
            f.write(body.replace(old, new, 1))
        try:
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
            with open(CRON, "w", encoding="utf-8") as f:
                f.write(body)

    ok = md5(CRON) == orig
    print(f"\n--- 逐字节还原 ---\n{'OK  ' if ok else 'FAIL'} etc.cron.d.pg17 {orig[:12]}")
    if not ok:
        failures.append("cron file not restored")

    print("\n=== 结论 ===")
    if failures:
        for x in failures:
            print("✗ " + x)
        return 1
    print(f"全部 {len(selected)} 条变异均有牙，且逐字节还原")
    return 0


if __name__ == "__main__":
    sys.exit(main())
