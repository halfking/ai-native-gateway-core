#!/usr/bin/env python3
"""825 分区迁移跨文件契约门的变异验证。

与 retention 那份同一套纪律：
  1. 变异必须仍能编译；BUILD-BROKEN 不算证据
  2. 变异必须让**目标门**转红
  3. 逐字节还原（md5）
  4. 打出实际跑的用例数；0 条直接判为脚本错误
"""
import hashlib
import os
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SQL = os.path.join(REPO, "sql/migrations/startup/830_ursm_node_snapshot_min_partitioned.sql")
SQL_DOWN = os.path.join(REPO, "sql/migrations/startup/830_ursm_node_snapshot_min_partitioned.down.sql")
PM = os.path.join(REPO, "bg/partition_manager.go")
DB = os.path.join(REPO, "db/db.go")
RUNNER = os.path.join(REPO, "installer/internal/dbinit/runner.go")
GATE_PKG = "./bg/"

MUTATIONS = [
    ("M26", PM,
     '\t\t{fnName: "ensure_ursm_node_snapshot_min_daily_partition", label: "ursm_node_snapshot_min (daily)", argExpr: "$1::date", partitionUnit: "day"}, // Migration 825\n',
     '',
     "Test825EnsureFunctionIsWiredIntoEnsureSpecs",
     "★ 反 473 本体：迁移建了分区却忘了接 24h tick ⇒ 跨日 0 点全量写入失败"),

    ("M27", PM,
     'label: "ursm_node_snapshot_min (daily)", argExpr: "$1::date", partitionUnit: "day"',
     'label: "ursm_node_snapshot_min (daily)", argExpr: "$1::date", partitionUnit: "month"',
     "Test825EnsureFunctionIsWiredIntoEnsureSpecs",
     "粒度写成月 ⇒ 预建的是月边界，而表按日分区"),

    ("M28", PM,
     'label: "ursm_node_snapshot_min (daily)", argExpr: "$1::date", partitionUnit: "day"',
     'label: "ursm_node_snapshot_min (daily)", argExpr: "$1", partitionUnit: "day"',
     "Test825EnsureFunctionIsWiredIntoEnsureSpecs",
     "去掉 ::date ⇒ pgx 传 text，PG 报 42883，分区永不预建"),

    ("M29", DB,
     '\tif err := db.ensureURSMNodeSnapshotMinDailyPartition(migCtx); err != nil {\n\t\treturn err\n\t}\n',
     '',
     "Test825BootEnsureIsWired",
     "去掉 boot ensure ⇒ 两次 tick 之间启动的实例没有当日分区"),

    ("M30", DB,
     "to_regprocedure('public.ensure_ursm_node_snapshot_min_daily_partition(date)') IS NOT NULL",
     "true",
     "Test825BootEnsureIsWired",
     "去掉 to_regprocedure 探针 ⇒ 825 未跑时每次 boot 都从 db.Open 报错进 no-DB"),

    ("M31", RUNNER,
     '\t\t\t"750_usage_facts_daily_partition.sql",',
     '\t\t\t"750_usage_facts_daily_partition.sql",\n\t\t\t"830_ursm_node_snapshot_min_partitioned.sql",',
     "Test825IsDeliberatelyNotInTheAutoStartupSequence",
     "把 825 注册进 installer 启动序列 ⇒ 无人值守升级会 RENAME 10 GB 活表"),

    ("M32", SQL,
     "format('ursm_node_snapshot_min_%s', to_char(p_date, 'YYYYMMDD'))",
     "format('ursm_node_snapshot_min_%s', to_char(p_date, 'YYYY_MM_DD'))",
     "TestPartitionNameContractMatchesRetentionParser",
     "分区名改成 YYYY_MM_DD ⇒ retention 的 _([0-9]{8})$ 永远匹配不上，空间永不回收且无报错"),

    ("M33", SQL,
     ") PARTITION BY RANGE (snapshot_ts);",
     ") PARTITION BY RANGE (snapshot_ts);\nCREATE TABLE public.ursm_node_snapshot_min_default PARTITION OF public.ursm_node_snapshot_min DEFAULT;",
     "Test825StillDeclaresNoDefaultPartition",
     "加真实的 DEFAULT 分区 ⇒ 变成永远被扫的垃圾堆，且 ensure/留存语义都要跟着改"),

    ("M34", SQL,
     "CREATE OR REPLACE FUNCTION public.ensure_ursm_node_snapshot_min_daily_partition(p_date DATE)",
     "CREATE OR REPLACE FUNCTION public.ensure_ursm_snapshot_daily_partition(p_date DATE)",
     "Test825MigrationDefinesTheEnsureFunction",
     "SQL 里的函数名漂移 ⇒ Go 侧 tick 调的函数不存在，永不预建分区"),

    ("M36", SQL,
     """    -- 同名对象存在却不挂在本父表下 ⇒ 明确报错，绝不静默跳过。
    --   「宁可吵，也不要安静地留一个零分区的分区父表」。
    IF to_regclass('public.' || pname) IS NOT NULL THEN
        RAISE EXCEPTION
            '分区名 % 已被占用，但它不是 public.ursm_node_snapshot_min 的子分区。'
            '若直接跳过，新建的父表将没有任何分区，之后每次写入都会报 '
            'no partition of relation found，而迁移仍会报成功。'
            '请先 DROP 或改名该对象（常见来源：825.down 保留的 _post825）。',
            pname;
    END IF;""",
     """    -- (mutation M36: 恢复成最初的按名字短路)
    IF to_regclass('public.' || pname) IS NOT NULL THEN
        RETURN;
    END IF;""",
     "Test825EnsureRejectsForeignPartitionSquatting",
     "★ 把 ensure 的占用检查改成静默 ⇒ 新父表零分区，所有写入报 no partition of relation found，而迁移报成功"),
    ("M37", SQL_DOWN,
     "    IF cur_name = 'ursm_node_snapshot_min_legacy_pkey' THEN\n        EXECUTE 'ALTER TABLE public.ursm_node_snapshot_min\n                 RENAME CONSTRAINT ursm_node_snapshot_min_legacy_pkey\n                 TO ursm_node_snapshot_min_pkey';",
     "    IF false THEN\n        EXECUTE 'ALTER TABLE public.ursm_node_snapshot_min\n                 RENAME CONSTRAINT ursm_node_snapshot_min_legacy_pkey\n                 TO ursm_node_snapshot_min_pkey';",
     "Test825UpDownRoundTripRealDB",
     "down 不把旧 PK 约束名换回 canonical ⇒ 回滚后再也上不了迁移（假回滚）"),
    ("M38", SQL_DOWN,
     "    IF cur_name = 'ursm_node_snapshot_min_pkey' THEN\n        EXECUTE 'ALTER TABLE public.ursm_node_snapshot_min_post825\n                 RENAME CONSTRAINT ursm_node_snapshot_min_pkey\n                 TO ursm_node_snapshot_min_post825_pkey';",
     "    IF false THEN\n        EXECUTE 'ALTER TABLE public.ursm_node_snapshot_min_post825\n                 RENAME CONSTRAINT ursm_node_snapshot_min_pkey\n                 TO ursm_node_snapshot_min_post825_pkey';",
     "Test825UpDownRoundTripRealDB",
     "down 不让 _post825 腾出 PK 名 ⇒ canonical 名被占，重新 up 建 PK 撞名"),

    ("M35", DB,
     "AND c.relkind = 'p'),",
     "AND c.relkind <> 'p'),",
     "Test825BootEnsureIsWired",
     "★ 反向守卫失效：825.down 回滚后父表已非分区，ensure 却照调 ⇒ 报错冒到 db.Open ⇒ no-DB"),
]


def md5(p):
    with open(p, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def run(cmd, env=None):
    e = dict(os.environ)
    if env:
        e.update(env)
    return subprocess.run(cmd, shell=True, cwd=REPO, capture_output=True,
                          text=True, env=e)


def main():
    orig = {p: md5(p) for p in (SQL, SQL_DOWN, PM, DB, RUNNER)}
    failures = []

    only = set(sys.argv[1:])
    selected = [m for m in MUTATIONS if not only or m[0] in only]
    if only:
        missing = only - {m[0] for m in MUTATIONS}
        if missing:
            print(f"未知变异 id: {sorted(missing)}")
            return 1
        print(f"只跑 {len(selected)} 条: {sorted(only)}\n")

    for mid, path, old, new, target, desc in selected:
        with open(path, encoding="utf-8") as f:
            body = f.read()
        if old not in body:
            print(f"[{mid}] SKIP 锚点未命中，脚本自身失效：{desc}")
            failures.append(f"{mid}: anchor not found")
            continue
        with open(path, "w", encoding="utf-8") as f:
            f.write(body.replace(old, new, 1))
        try:
            b = run("go build ./bg/ ./db/ ./cmd/gateway/")
            if b.returncode != 0:
                print(f"[{mid}] BUILD-BROKEN 不算证据：{desc}")
                for line in (b.stderr or "").strip().splitlines()[:4]:
                    print("       " + line)
                failures.append(f"{mid}: build broken")
                continue
            # 标 RealDB 的变异必须真跑：它们针对的正是"grep 门抓不到、
            # 只有执行才暴露"的那类缺陷。MUTATE_TEST_DSN 未设置时跳过，
            # 而不是让它绿着蒙混过去。
            env = None
            if "RealDB" in target or "Rejects" in target:
                dsn = os.environ.get("MUTATE_TEST_DSN")
                if not dsn:
                    print(f"[{mid}] 需真库但 MUTATE_TEST_DSN 未设置 ⇒ 跳过（不算证据）")
                    continue
                env = {"TEST_DATABASE_URL": dsn}
            r = run(f"go test {GATE_PKG} -run {target} -v -count=1", env=env)
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
            with open(path, "w", encoding="utf-8") as f:
                f.write(body)

    print("\n--- 逐字节还原校验 ---")
    for p, h in orig.items():
        ok = md5(p) == h
        print(f"{'OK  ' if ok else 'FAIL'} {os.path.basename(p)} {h[:12]}")
        if not ok:
            failures.append(f"{p}: not restored")

    print("\n=== 结论 ===")
    if failures:
        for x in failures:
            print("✗ " + x)
        return 1
    print(f"全部 {len(selected)} 条变异均有牙，且逐字节还原")
    return 0


if __name__ == "__main__":
    sys.exit(main())
