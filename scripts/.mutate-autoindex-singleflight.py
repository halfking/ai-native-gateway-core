#!/usr/bin/env python3
"""AutoIndexRefresher singleflight 互斥的行为门变异验证。

门：bg/auto_index_refresher_mutex_test.go（3 条）
纪律同前：逐字节还原 / 目标门必须转红 / 打出实际跑的条数，0 条即不可判定。
"""
import hashlib
import os
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(REPO, "bg/auto_index_refresher.go")

TARGETS = "TestBeginRefresh|TestRefreshOnceWires"

MUTATIONS = [
    ("M65",
     "\tr.refreshMu.Lock()\n\tdefer r.refreshMu.Unlock()\n\tif r.inFlight {\n\t\treturn false\n\t}\n\tr.inFlight = true\n\treturn true",
     "\treturn true",
     "TestBeginRefreshIsExclusive|TestBeginRefreshConcurrentExactlyOneWins",
     "★ 去掉互斥本体（beginRefresh 恒真）⇒ 64 个并发调用全部通过 ⇒ "
     "两个 rollup 并发跑、DELETE+INSERT 交错 ⇒ 正是 2026-09-10 那次 "
     "duplicate key 故障的形态。并发那条（winners 必须 =1）才是关键见证"),

    ("M66",
     "\tdefer r.endRefresh()\n",
     "",
     "TestRefreshOnceWiresTheSingleflightGuard",
     "★ 去掉 defer endRefresh：一旦 rollup 返回错误或 panic，inFlight 永不复位 ⇒ "
     "refresher **永久跳过所有刷新**。这比原缺陷更糟 —— 原缺陷会报错，"
     "这个是静默失效（索引再也不更新，路由永远用陈旧数据）"),

    ("M67",
     "\t\tslog.Debug(\"auto index refresh: 已有刷新在跑，跳过本次并发触发\",\n\t\t\t\"interval\", r.RefreshInterval.String())\n\t\treturn nil",
     "\t\tslog.Debug(\"auto index refresh: 已有刷新在跑，跳过本次并发触发\",\n\t\t\t\"interval\", r.RefreshInterval.String())\n\t\treturn fmt.Errorf(\"refresh already in progress\")",
     "TestRefreshOnceWiresTheSingleflightGuard",
     "跳过时返回 error 而非 nil ⇒ 调用方（admin 手动接口、LISTEN 监听）把"
     "「本来就在跑」当成「刷新失败」上报 ⇒ 高频假告警，手动接口会被误判为故障。"
     "★ 用 fmt.Errorf 而不是 errors.New：errors 包在该文件未导入，"
     "写成 errors.New 会 BUILD-BROKEN —— 而 BUILD-BROKEN 不算证据"),

    ("M68",
     "\tif !r.beginRefresh() {",
     "\tif false {",
     "TestRefreshOnceWiresTheSingleflightGuard",
     "★ 装配断链：原语（beginRefresh/endRefresh）测得好好的，但 RefreshOnce "
     "不再调用它 ⇒ 门 1/2 全绿而缺陷依旧。这是「门只测了零件、没测装配」"
     "的老形态，专门由第 3 条契约门守住"),
]


def md5(p):
    with open(p, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def run(cmd):
    return subprocess.run(cmd, shell=True, cwd=REPO, capture_output=True,
                          text=True, errors="replace")


def main():
    orig = md5(SRC)
    failures = []
    only = set(sys.argv[1:])
    selected = [m for m in MUTATIONS if not only or m[0] in only]

    for mid, old, new, target, desc in selected:
        body = open(SRC, encoding="utf-8").read()
        if old not in body:
            print(f"[{mid}] SKIP 锚点未命中，变异脚本自身失效")
            failures.append(f"{mid}: anchor not found")
            continue
        open(SRC, "w", encoding="utf-8").write(body.replace(old, new, 1))
        try:
            b = run("go build ./bg/")
            if b.returncode != 0:
                print(f"[{mid}] BUILD-BROKEN 不算证据")
                failures.append(f"{mid}: build broken")
                continue
            v = run(f"go test ./bg/ -run '{target}' -v -count=1")
            out = v.stdout + v.stderr
            ran = out.count("=== RUN")
            if ran == 0:
                print(f"[{mid}] 跑了 0 条 ⇒ 不可判定（通常是 target 名写错）")
                failures.append(f"{mid}: 0 tests ran")
                continue
            red = v.returncode != 0
            print(f"[{mid}] {'RED ✓' if red else 'GREEN ✗ 无牙'}  ran={ran}  {desc}")
            if not red:
                failures.append(f"{mid}: gate stayed green")
        finally:
            open(SRC, "w", encoding="utf-8").write(body)

    ok = md5(SRC) == orig
    print(f"\n--- 逐字节还原 ---\n{'OK  ' if ok else 'FAIL'} auto_index_refresher.go {orig[:12]}")
    if not ok:
        failures.append("source not restored")

    print("\n=== 结论 ===")
    if failures:
        for x in failures:
            print("✗ " + x)
        return 1
    print(f"全部 {len(selected)} 条变异均有牙，且逐字节还原")
    return 0


if __name__ == "__main__":
    sys.exit(main())
