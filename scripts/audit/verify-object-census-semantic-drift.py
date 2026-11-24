#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""verify-object-census-semantic-drift.py —— ★ census 底账的【语义漂移】判据（★ 姊妹工具 ★）

★ 它解决的是哪一个缺口（★ 2026-11-25 第 23 轮实测撞到 ★）：
    上游 `verify-object-census-drift.sh` 把【按日期滚动的对象】也算成【结构漂移】。

★★ 具体形态（★ 全实测 ★）：
    · `usage_facts_20261123` / `usage_facts_20261124` / `usage_facts_20261126`
      （★ 按日分区的物化表 ★★）
    · `ursm_node_snapshot_min_20261117` / `ursm_node_snapshot_min_20261126`
      （★ 按日滚动的快照表 ★★）
    · `mock_probe_history_20261123` / `mock_probe_history_20261124`
  ⇒ ★ 这族对象【每过一天就换一批名字】⇒ ★ 任何【固定快照】与现场比对
  ⇒ ★ **必然**报出结构漂移 ⇒ ★ ⇒ ★ 而这【不是底账过期、★ 是底账的年龄】。

★★ 所以上游那道判据的结论形态是：
    「211 上缺 2 / 新 10」⇒ ★★ 读起来像【生产被改过】★★
    ⇒ ★ 但逐条看过之后：★ 12 条**全部**带日期戳 ⇒ ★ **零非滚动漂移**。

★★ 还有第二种噪声（★ 本轮实测撞到 ★★）：**服务器配置向量**
    census 的 `srv` 行记的是 PG 的 `shared_buffers,work_mem,max_connections,max_wal_size`
    ⇒ ★ 那是【环境级事实】、★ 随容器参数变 ⇒ ★★ ⇒ ★ **不是「底账过期」**
    ⇒ ★★ ⇒ ★ 本工具把它单列为 `server_config_changed`、★ 不计入结构漂移
    ⇒ ★★ ⇒ ★ （★ 实测：★ fresh 快照那一行是 `128MB,4MB,100,4GB`、★ 现场是 `2GB,8MB,200,6GB` ★★）
    ⇒ ★★ ⇒ ★ ⇒ **⇒ census 这张表把【环境级事实】与【对象级事实】混在了一起 ⇒ ⇒ ⇒ 「结构漂移」天然会吃到环境级噪声 ★★★**

★★ 本工具做什么（★ 只加一层分类、★ diff 口径不重写 ★★）：
    ① **口径同源**：★ 直接 import 上游 `db-object-census.py`、★ 复用它的
       `parse_census_header` / `parse_census_env_rows` / `_collect_raw_rows` /
       `diff_env` ⇒ ⇒ ★★ **不重写一遍 diff**（★ 重写＝第二份真源 ⇒ 口径必然分叉 ★★）。
    ② **只加一层**：★ 把 diff 的结果按【滚动 / 非滚动】分成两堆。
    ③ **滚动的判据不是「名字像日期」而是「这一族真的换过日期」**：
       对象名里含日期戳 **且** 另一侧存在【同族但日期不同】的成员
       ⇒ ⇒ ★ **家族真的滚动过** ⇒ ⇒ ★ 这一条缺失/新增是【轮换】、不是【漂移】。
       ⇒ ★★ ⇒ ★ 而【名字带日期、但同族没有别的日期成员】的对象
       ⇒ ★★ ⇒ **不豁免** ⇒ ★★ ⇒ ★ ★ ⇒ 【豁免口径不会放过真正的删除/新增】。

★★ 退出码（★ 与上游同语义、★ 不许静默降级 ★★）：
    0  ★ 非滚动漂移为零 ⇒ ★★ **底账仍是「此刻的真事实」**
    1  ★ 有非滚动漂移   ⇒ ★★ **底账已过期**（★ 缺货棘轮／reachability 判据会失真 ★★）
    2  ★ 无法判定       ⇒ ★★ （★ 缺 DSN / 采集失败 / 快照里没有该环境 ★★）

★★ 三条纪律（★ 与上游同族 ★★）：
    ① **只读**：★ 全程只 SELECT；★ 采集 SQL 由上游工具生成（★ 不手搓 ★★）。
    ② **凭据不回显**：★ DSN 只走环境变量（★ 不进 argv、★ 不打印 ★★）。
    ③ **不许静默降级**：★ 「无法判定」必须是退出码 2（★ 绝不与「没漂移」的 0 混同 ★★）。

★ 用法：
    GUARD_DSN='<local dsn>' GUARD_PSQL=<psql 可执行文件> \\
      python3 scripts/audit/verify-object-census-semantic-drift.py --env local

    ★ 远程车道（★ 采集 SQL 由上游工具生成 ⇒ 口径不分叉 ★★）：
    python3 scripts/audit/db-object-census.py census-sql > /tmp/census.sql
    ssh <host> "docker exec -i acc-pg17 psql -U llm_gateway -d llm_gateway -tA -q" \\
      < /tmp/census.sql \\
      | python3 scripts/audit/verify-object-census-semantic-drift.py --env 211 --from-stdin

★ ★ ★ 边界（★ 如实登记 ★★）：
    ★ 本工具【不判定】「该补哪些对象」⇒ ★ 那是 reachability / 缺口台账的职责；
    ★ 本工具只判定「底账是不是还等于现场」⇒ ★★ ⇒ ★ 两个判据【互补、★ 不重叠】★★。
"""

import argparse
import importlib.util
import json
import os
import re
import sys

# ── 口径同源：★ 直接复用上游模块、★ 不重写 diff ★★ ────────────────────────
_HERE = os.path.dirname(os.path.abspath(__file__))
_CENSUS_PATH = os.path.join(_HERE, "db-object-census.py")
_spec = importlib.util.spec_from_file_location("_kx_census", _CENSUS_PATH)
if _spec is None or _spec.loader is None:  # pragma: no cover
    sys.stderr.write("ERROR: 无法加载上游工具 %s\n" % _CENSUS_PATH)
    sys.exit(2)
census = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(census)

# ★ 日期戳：★ 两种形态都收（★ `20261126` 与 `2026_08` ★★）
DATE_STAMP = re.compile(r"20\d{6}|20\d{2}[_-]\d{2}")

# ★ 家族键：★ 把日期戳抹掉 ⇒ ★ 同族不同日期的对象共享一个键 ★★）
def family_key(key):
    """★ (kind, schema, name) ⇒ 家族键（★ 日期戳替换成 `<DATE>` ★★）"""
    kind, schema, name = key
    return (kind, schema, DATE_STAMP.sub("<DATE>", name))


def classify(keys, other_keys):
    """★ 把 diff 的键分成【滚动】与【非滚动】两堆。

    ★★ 滚动判据（本工具的核心、★ 也是它区别于「按名字猜」的地方 ★★）：
        名字里含日期戳 **且** 【另一侧存在同族但日期不同】的成员
        ⇒ ⇒ ★ 这一族真的滚动过 ⇒ ★ 这一条是【轮换】而不是【漂移】。
        ⇒ ★★ ⇒ ★ 名字带日期、★ 但同族没有别的日期成员 ⇒ **不豁免**。
    """
    other_families = set(family_key(k) for k in other_keys)
    rolling = []
    structural = []
    for k in keys:
        if DATE_STAMP.search(k[2]) and family_key(k) in other_families:
            rolling.append(k)
        else:
            structural.append(k)
    return rolling, structural


def cmd(args):
    meta, hdr_envs = census.parse_census_header(args.census)
    if args.env not in hdr_envs:
        sys.stderr.write(
            "ERROR: census %s has no env=%s (★ envs=%s)\n"
            % (args.census, args.env, ",".join(sorted(hdr_envs)))
        )
        return 2
    snap_rows = census.parse_census_env_rows(args.census, args.env)
    if not snap_rows:
        sys.stderr.write("ERROR: census %s has no rows for env=%s\n" % (args.census, args.env))
        return 2

    try:
        raw_rows = census._collect_raw_rows(args.from_stdin, args.psql)
    except SystemExit as exc:
        # ★ 「采集失败」在判定语境下是【无法判定】⇒ ★ 必须是 2 ★（★ 不与 0 混同 ★★）
        return 2 if exc.code not in (0, None) else exc.code
    live_rows = ["%s\t%s" % (args.env, r) for r in raw_rows]

    # ★★ 口径同源：★ diff 本身【完全复用上游】★★
    missing, added, changed, metric_only = census.diff_env(snap_rows, live_rows)

    snap_keys = set(census._row_identity(r.split("\t")) for r in snap_rows)
    live_keys = set(census._row_identity(r.split("\t")) for r in live_rows)

    miss_roll, miss_struct = classify(missing, live_keys)
    add_roll, add_struct = classify(added, snap_keys)
    # ★★ `changed`（★ 形态变了 ★）⇒ ★ 分两堆：
    #   · kind == "srv" ⇒ ★【服务器配置向量】（★ shared_buffers / work_mem / max_connections
    #     / max_wal_size ★★）⇒ ★ 它是【环境级事实】、★ 随容器参数变 ⇒ ★★ ⇒ ★
    #     **不是「底账过期」** ⇒ ★ 归到 `server_config_changed` 单独报。
    #   · 其余 ⇒ ★ 对象的形态变了 ⇒ ★ 一律算【非滚动漂移】⇒ ★ 保守方向（★ 不豁免 ★★）
    srv_changed = [k for k in changed if k[0] == "srv"]
    obj_changed = [k for k in changed if k[0] != "srv"]

    summary = {
        "env": args.env,
        "snapshot_rows": len(snap_rows),
        "live_rows": len(live_rows),
        "rolling_missing_in_live": len(miss_roll),
        "rolling_added_in_live": len(add_roll),
        "structural_missing_in_live": len(miss_struct),
        "structural_added_in_live": len(add_struct),
        "changed": len(obj_changed),
        "server_config_changed": len(srv_changed),
        "metric_only": len(metric_only),
    }
    summary["ledger_is_current"] = not (miss_struct or add_struct or obj_changed)

    if args.json:
        sys.stderr.write(json.dumps(summary, ensure_ascii=False, sort_keys=True) + "\n")
    else:
        sys.stderr.write(
            "semantic-drift: env=%s snapshot_rows=%d live_rows=%d\n"
            % (summary["env"], summary["snapshot_rows"], summary["live_rows"])
        )
        sys.stderr.write(
            "  rolling (★ 按日期滚动、★ 不算漂移 ★): missing=%d added=%d\n"
            % (len(miss_roll), len(add_roll))
        )
        sys.stderr.write(
            "  structural (★ 真漂移 ★): missing=%d added=%d changed=%d\n"
            % (len(miss_struct), len(add_struct), len(obj_changed))
        )
        sys.stderr.write(
            "  server-config (★ 环境级事实、★ 不算底账过期 ★): changed=%d\n" % len(srv_changed)
        )
        sys.stderr.write("  metric-only movement (★ NOT drift): %d rows\n" % len(metric_only))
        for k in miss_roll[:20]:
            sys.stderr.write("  ROLLING-MISSING  %s/%s %s\n" % (k[0], k[1], k[2]))
        for k in add_roll[:20]:
            sys.stderr.write("  ROLLING-ADDED    %s/%s %s\n" % (k[0], k[1], k[2]))
        for k in miss_struct[:20]:
            sys.stderr.write("  MISSING-IN-LIVE  %s/%s %s\n" % (k[0], k[1], k[2]))
        for k in add_struct[:20]:
            sys.stderr.write("  ADDED-IN-LIVE    %s/%s %s\n" % (k[0], k[1], k[2]))
        for k in obj_changed[:20]:
            sys.stderr.write("  CHANGED          %s/%s %s\n" % (k[0], k[1], k[2]))
        for k in srv_changed[:20]:
            sys.stderr.write("  SERVER-CONFIG-CH %s/%s %s\n" % (k[0], k[1], k[2]))

    if summary["ledger_is_current"]:
        sys.stderr.write("RESULT: LEDGER IS CURRENT (★ 非滚动漂移为零 ★)\n")
        return 0
    sys.stderr.write("RESULT: LEDGER IS STALE (★ 有非滚动漂移 ★)\n")
    return 1


def main(argv=None):
    ap = argparse.ArgumentParser(description="★ census 底账的语义漂移判据（★ 只读 ★）")
    ap.add_argument("--env", required=True, help="★ 环境标签（★ 与 census 快照里的 env 对齐 ★）")
    ap.add_argument(
        "--census",
        default=os.path.join(_HERE, "asset", "db-object-census.tsv"),
        help="★ 快照路径（★ 默认 scripts/audit/asset/db-object-census.tsv ★）",
    )
    ap.add_argument("--psql", help="★ psql 可执行文件（★ 默认 $GUARD_PSQL 或 psql ★）")
    ap.add_argument("--from-stdin", action="store_true", help="★ 从 stdin 读已采集的行（★ 远程车道 ★）")
    ap.add_argument("--json", action="store_true", help="★ 输出机器可读摘要 ★")
    return cmd(ap.parse_args(argv))


if __name__ == "__main__":
    sys.exit(main())
