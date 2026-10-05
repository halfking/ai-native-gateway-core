#!/usr/bin/env python3
"""retention 分区留存的行为门变异验证。

纪律（本轮反复踩过的坑，写进脚本里强制执行）：
  1. 变异必须**仍能编译** —— BUILD-BROKEN 不算证据；
  2. 变异必须**只让目标门转红** —— 顺带弄红别的门说明变异太宽，无定位力；
  3. 每条变异必须**逐字节还原**（md5 比对），共享工作区里污染别人的代价很高；
  4. 必须打出**实际跑的用例数** —— `-run` 匹配 0 条与全绿不可区分。
     跑了 0 条直接判为脚本错误。
"""
import hashlib
import subprocess
import sys
import os

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(REPO, "domains/ursm/v2/persist/retention_partition.go")
RET = os.path.join(REPO, "domains/ursm/v2/persist/retention.go")
PKG = "./domains/ursm/v2/persist/"

# (id, 文件, 原文, 变异后, 目标测试, 说明)
MUTATIONS = [
    ("M18", SRC,
     "\t\tif p.dayEnd.After(cutoff) {",
     "\t\tif p.dayEnd.Before(cutoff) {",
     "TestSnapshotRetentionDropsExpiredPartitionsNotRows",
     "边界判据 !After 改成 Before ⇒ dayEnd 恰好等于 cutoff 的分区不再删（提前 1ns 丢数据）"),

    ("M19", SRC,
     "\t\tif p.dayEnd == nil {",
     "\t\tif false {",
     "TestSnapshotRetentionNeverDropsPartitionWithoutDateContract",
     "去掉分区名契约守卫 ⇒ 名字不合契约的分区会被删"),

    ("M20", SRC,
     "\t}\n\treturn partitioned\n}",
     "\t}\n\treturn false\n}",
     "TestSnapshotRetentionDropsExpiredPartitionsNotRows",
     "形态探测恒返回非分区 ⇒ 已分区的表上永远走 DELETE，根治失效"),

    ("M21", RET,
     "\t\tres, err := w.cleanupPartitioned(ctx)\n\t\tif err == nil {\n\t\t\treturn res, nil\n\t\t}",
     "\t\tres, err := w.cleanupPartitioned(ctx)\n\t\tif true {\n\t\t\treturn res, nil\n\t\t}",
     "TestSnapshotRetentionFallsBackToRowDeleteWhenDropFails",
     "DROP 失败不再退回 DELETE ⇒ 留存静默停摆，磁盘单调增长且无告警"),

    ("M22", SRC,
     "\tif _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5min'`); err != nil {\n\t\treturn fmt.Errorf(\"set lock_timeout for dropping %s: %w\", name, err)\n\t}\n",
     "",
     "TestSnapshotRetentionDropsExpiredPartitionsNotRows",
     "去掉 DROP 前的 lock_timeout ⇒ 长读事务可把 DROP 无限期挂住"),

    ("M23", SRC,
     "\t\tif w.nowTime().After(deadline) {",
     "\t\tif w.nowTime().After(deadline) && false {",
     "TestSnapshotRetentionPartitionDropRespectsCleanupWindow",
     "去掉墙钟上限 ⇒ 长期停机后一次性 DROP 堆积的全部分区"),

    ("M24", SRC,
     "\tstmt := `DROP TABLE IF EXISTS ` + pgx.Identifier{\"public\", name}.Sanitize()",
     "\tstmt := `DROP TABLE IF EXISTS ` + pgx.Identifier(nil).Sanitize() + \"public.\" + name",
     "TestSnapshotRetentionDropsExpiredPartitionsNotRows",
     "分区名裸拼不转义 ⇒ 名字含引号时分���注入/语法错"),

    ("M25", RET,
     "\t\t\tMode:              RetentionModeRowDelete,\n\t\t\tRowsDeleted:       deleted,",
     "\t\t\tMode:              RetentionModePartitionDrop,\n\t\t\tRowsDeleted:       deleted,",
     "TestSnapshotRetentionFallsBackToRowDeleteWhenDropFails",
     "已降级却仍上报 partition-drop ⇒ 日志谎报走的哪条路"),
]


def md5(path):
    with open(path, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def run(cmd, **kw):
    return subprocess.run(cmd, shell=True, cwd=REPO, capture_output=True, text=True, **kw)


def count_ran(out):
    return out.count("=== RUN")


def main():
    orig = {p: md5(p) for p in (SRC, RET)}
    failures = []

    for mid, path, old, new, target, desc in MUTATIONS:
        with open(path, encoding="utf-8") as f:
            body = f.read()
        if old not in body:
            print(f"[{mid}] SKIP 锚点未命中，变异脚本自身失效: {desc}")
            failures.append(f"{mid}: anchor not found")
            continue
        with open(path, "w", encoding="utf-8") as f:
            f.write(body.replace(old, new, 1))

        try:
            build = run("go build ./domains/ursm/... ./cmd/gateway/")
            if build.returncode != 0:
                print(f"[{mid}] BUILD-BROKEN 不算证据: {desc}")
                for line in (build.stderr or "").strip().splitlines()[:6]:
                    print("       " + line)
                failures.append(f"{mid}: build broken")
                continue

            r = run(f"go test {PKG} -run {target} -v -count=1")
            out = r.stdout + r.stderr
            ran = count_ran(out)
            if ran == 0:
                print(f"[{mid}] 跑了 0 条 ⇒ 变异不可判定（-run 没匹配到）")
                failures.append(f"{mid}: 0 tests ran")
                continue

            red = r.returncode != 0
            status = "RED ✓" if red else "GREEN ✗ 无牙"
            print(f"[{mid}] {status}  ran={ran}  {desc}")
            if not red:
                failures.append(f"{mid}: gate stayed green")
        finally:
            with open(path, "w", encoding="utf-8") as f:
                f.write(body)

    print("\n--- 逐字节还原校验 ---")
    for p, h in orig.items():
        now = md5(p)
        ok = now == h
        print(f"{'OK  ' if ok else 'FAIL'} {os.path.basename(p)} {h[:12]}")
        if not ok:
            failures.append(f"{p}: not restored")

    print("\n=== 结论 ===")
    if failures:
        for f in failures:
            print("✗ " + f)
        return 1
    print(f"全部 {len(MUTATIONS)} 条变异均有牙，且逐字节还原")
    return 0


if __name__ == "__main__":
    sys.exit(main())
