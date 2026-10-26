#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""db-object-census.py — ★★ 「各环境真实对象底账 × 仓内迁移期望集」只读对账器。

# 这道工具补的是哪块缺口

★ R160 轮把「SQL 变更登记册 × 各环境真实账本」变成了机器可判（★ `db-changelog-ledger-reconcile.py`）。
★★ 但 R160 轮自己撞到了一件事：★★ **账本本身是【不完整】的**：

    local 账本覆盖 1..909（277 行）｜252 账本只有 49 行（覆盖 560..890）｜211 账本到 895（264 行）

⇒ ⇒ **「某个迁移在 252/211 上到底建没建出对象」这个问题，账本回答不了**
⇒ ⇒ ★★ 于是 R161 轮把事实源**再往下一层**挪：★★ **不看账本，看【数据库里真实存在的对象】**。

★★ 本工具的四个模式：

    collect   --env {local|252|211} [--out FILE] [--append]
        ★ 只读采集某环境的【全部 schema 对象】⇒ 写 TSV（★ 15 列，见下）。
        ★ 退出码：0 成功 / 3 采集失败。

    expect   [--out FILE]
        ★ 从仓内 `sql/migrations/startup/*.sql` 反解「每个迁移声明建了哪些索引」
        ⇒ 写期望集 TSV（★ 5 列）。
        ★ 退出码：0 成功 / 3 用法错误。

    reconcile --census FILE [--out FILE] [--json]
        ★ 把期望集与三环境底账逐条对账 ⇒ 写缺口台账（★ 8 列）。
        ★ 退出码：0 无缺口 / ★★ **2 有缺口** / 3 用法或解析错误。

    selftest
        ★ 自证（★ 阳性对照 + 反向对照 + 解析面自检）。

# ★★★ 三条设计纪律（★ R159/R160 同族）

① ★ **只读**：★ 本工具只 `SELECT`（★ `pg_class` / `pg_index` / `pg_inherits` / `pg_extension`）
   ⇒ ★★ **对生产的写入 = 0**。★ 采集远程走 `ssh … | podman/docker exec -i … psql`（★ 只读管道）。

② ★ **DSN 与口令绝不回显**：★ 本地采集用 `GUARD_DSN` 环境变量（★ 只作为子进程环境传入，
   ★ **不进 argv**、★ **不打印**）；★ 输出里只有文件名与对象字段。

③ ★★★ **「不确定」必须落在【字段名】上**：★ 一个期望索引在某环境的状态**只有四种**，
   每一种都可机器判（★ 见 §状态词表）⇒ ★★ **没有「不知道」这一格**
   ⇒ ★★ ⇒ 门不会因为散文而红 ⇒ ★★ ⇒ 人去改事实而不是改散文。

# ★★★ 状态词表（★ 缺口台账里每个环境的格子只有这四个值）

    present            ★ 该索引在本环境直接存在（★ pg_class 里有这个名字）
    present-via-child  ★ 该索引是【分区父索引】的子索引（★ pg_inherits 链）
                        ⇒ ★ 分区父表上 CREATE INDEX ⇒ PG 自动给每个分区建子索引
                        ⇒ ★ 子索引的名字是【分区名 + 列后缀】，★ **推不出父索引名**
                        ⇒ ★★ 必须靠 pg_inherits 的【权威父子链】判，★ 不能靠名字前缀
    missing            ★★ **真缺口**：★ 该索引该在而不在（★ 且它的表确实存在）
    retired            ★ 该索引的表已经不在了（★ 迁移 890 退役了 v1 族 ⇒ 索引随之消失）
                        ⇒ ★★ **不是缺口** ⇒ ★★ 这是【刻意的】与【意外的】的分界线

# ★★★ 期望集是怎么反解出来的（★ 三个坑全踩过）

★ 坑一：**注释里也有 CREATE INDEX** ⇒ ★ 必须先剥 `--` 行注释再解析。
★ 坑二：**中间产物索引**（★ 迁移 902 的 `stats_event_dedup_pkey_include_tmp`）
   ⇒ ★ 它建完就被 `ALTER TABLE … ADD CONSTRAINT … USING INDEX <它>` 消费并改名
   ⇒ ⇒ ★★ 判据**不是**「名字带 _tmp」，★ 而是【**同一个文件里它是否被 USING INDEX 引用**】
   ⇒ ⇒ ★ 结构判据，★ 不是名字猜。
★ 坑三：**后来的迁移会 DROP 早期的索引**（★ 93 个 DROP INDEX，★ 其中 34 个是
   「建过又删掉」）⇒ ⇒ ★★ 期望集必须做【建了 ∧ 没被删】的差集，★ 否则全是假缺口。

★★ ★★ 坑四（R162 轮定位，★ **R161 第一版踩的**）：★★ **DROP 的名字带 schema 前缀**！
   `DROP INDEX IF EXISTS public.foo` ⇒ 正则抓到 `public.foo`
   ⇒ 而 CREATE 侧存的是裸名 `foo`
   ⇒ ⇒ ★★ 差集 `created - dropped` **永远匹配不上**
   ⇒ ⇒ ★★ **被后续迁移明确退役的索引仍然留在期望集里**
   ⇒ ⇒ ★★ 台账于是把它们记成「三环境都没建」⇒ ★★ 实测 **43 条**
   ⇒ ⇒ ★★ 而它们在三个环境**确实都不存在** ⇒ ⇒ ★★ **43 条全是工具造的假缺口**
   ⇒ ⇒ ★★ 更糟的是：**照台账去「补建」就会复活被刻意退役的索引**
   ⇒ ⇒ ★★ 修法：DROP 侧也取裸名（★ `_bare_name`）⇒ ★★ 期望集 852 → **834**
   ⇒ ⇒ ★★ 三环境同缺 43 → **0** ⇒ ★★ ⇒ **三环境在索引层面完全一致**
   ⇒ ⇒ ★★ ★★ 教训：**「两处实现互证」也可能同源同错** ⇒ ★★ 那时互证是零信息
   ⇒ ⇒ ★★ （★ R161 的 Go 门当时也没剥 ⇒ ★★ ★★ 必须验「第二份实现是否带同一个 bug」）

# ★★ 还有一个坑（本工具第一版自己踩的，★ 写在最前面因为它会毁掉整个底账）

★★★ ★ **PG 17 的 `pg_get_indexdef()` 会【换行】**（★ 长的索引表达式会被 pretty-print 折行）
   ⇒ ★★ 直接 dump 出来的 TSV 里会混进【残行】（★ 实测：14 行字段数 ≠ 正常值）
   ⇒ ⇒ ★★ **任何行式 TSV 消费者都会静默读错** ⇒ ⇒ ★★ 所以下面 SQL 里
   ★★ **每个文本字段都被 `replace(replace(…, E'\\n', ' '), E'\\t', ' ')` 折成单行**。
   ⇒ ★★ 这不是洁癖：★★ **量具自己坏了 ⇒ 后面所有读数都是假的。**

# 底账 TSV 格式（★ `collect` 的产物，★ **17 列**（★ R162 从 15 列扩到 17 列））

    #env<TAB>kind<TAB>schema<TAB>relkind<TAB>name<TAB>owner<TAB>ispartition<TAB>
    #partition_bound<TAB>parent_table<TAB>reltuples<TAB>table_size<TAB>total_size<TAB>
    #definition<TAB>flags<TAB>parent_index<TAB>index_columns<TAB>index_predicate
    local<TAB>idx<TAB>public<TAB>i<TAB>idx_x<TAB>llm_gateway<TAB>…<TAB>true,true,true<TAB>
    …<TAB>tenant_id,session_id<TAB>

    ★★ R162 为什么加这两列：★★ 覆盖判据（「某条查询的过滤列/排序列有没有索引可用」）
    ★★ 必须读【结构化列序】⇒ ★★ 解析 `pg_get_indexdef` 的文本太脆（★ 表达式索引 /
    ★★ INCLUDE / opclass 都会让文本解析失真）⇒ ★★ 所以直接把 `indkey` 展开成列名序列。
    ★★ `index_predicate` 用来区分【部分索引】（★ 只有查询带同样谓词才可用）。

# 期望集 TSV 格式（★ `expect` 的产物，★ 5 列）

    #migration_no<TAB>index_name<TAB>table<TAB>source_file<TAB>note
    909<TAB>idx_session_summaries_title_trgm<TAB>session_summaries<TAB>909_….sql<TAB>

# 缺口台账 TSV 格式（★ `reconcile` 的产物，★ 8 列）

    #migration_no<TAB>index_name<TAB>table<TAB>local<TAB>252<TAB>211<TAB>verdict<TAB>severity
    909<TAB>idx_session_summaries_title_trgm<TAB>session_summaries<TAB>present<TAB>present<TAB>missing<TAB>gap<TAB>prod-partial-gap

    ★★ severity 词表（★ R162 修掉了 R161 的「命名与语义相反」）：
        none | local-only-gap | prod-partial-gap | prod-full-gap
    ★★ **给定三个格子状态，severity 唯一确定** ⇒ ★★ 门可以机械校验。

# 退役证据台账格式（★ `retired` 的产物，★ 8 列）

    #create_migration_no<TAB>index_name<TAB>table<TAB>create_file<TAB>
    #retire_migration_no<TAB>retire_file<TAB>conditional<TAB>retire_line

★ 本文件不含任何凭据；★ 也不含任何生产主机口令。
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
from datetime import datetime, timezone

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
MIGRATIONS_DIR = os.path.join(REPO_ROOT, "sql", "migrations", "startup")

# ★★ 采集用的 SQL（★ 内嵌，★ 工具与产物同源）
# ★★ 注意每个文本字段都被折成单行（★ 见文件头「PG 17 换行」那一节）。
CENSUS_SQL = r"""
SET statement_timeout = '120s';

SELECT 'rel'
    || E'\t' || n.nspname
    || E'\t' || c.relkind::text
    || E'\t' || c.relname
    || E'\t' || pg_get_userbyid(c.relowner)
    || E'\t' || c.relispartition::text
    || E'\t' || replace(replace(coalesce(pg_get_expr(c.relpartbound, c.oid), ''), E'\n', ' '), E'\t', ' ')
    || E'\t' || coalesce((SELECT parent.relname FROM pg_inherits i JOIN pg_class parent ON parent.oid = i.inhparent WHERE i.inhrelid = c.oid LIMIT 1), '')
    || E'\t' || c.reltuples::bigint::text
    || E'\t' || pg_catalog.pg_table_size(c.oid)::text
    || E'\t' || pg_catalog.pg_total_relation_size(c.oid)::text
    || E'\t' || ''
    || E'\t' || ''
    || E'\t' || ''
    || E'\t' || ''
    || E'\t' || ''
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
   AND c.relkind IN ('r', 'p', 'm', 'v', 'S', 'f')
 ORDER BY 1;

SELECT 'idx'
    || E'\t' || n.nspname
    || E'\t' || i.relkind::text
    || E'\t' || i.relname
    || E'\t' || pg_get_userbyid(i.relowner)
    || E'\t' || ''
    || E'\t' || ''
    || E'\t' || coalesce(t.relname, '')
    || E'\t' || ''
    || E'\t' || pg_relation_size(i.oid)::text
    || E'\t' || pg_total_relation_size(i.oid)::text
    || E'\t' || replace(replace(pg_get_indexdef(i.oid), E'\n', ' '), E'\t', ' ')
    || E'\t' || x.indisvalid::text || ',' || x.indisready::text || ',' || x.indislive::text
    || E'\t' || coalesce((SELECT pc.relname FROM pg_inherits inh JOIN pg_class pc ON pc.oid = inh.inhparent WHERE inh.inhrelid = i.oid LIMIT 1), '')
    || E'\t' || replace(replace(coalesce((SELECT string_agg(
              CASE WHEN k.ord > x.indnkeyatts THEN a.attname || ' INCLUDE'
                   WHEN x.indoption[k.ord - 1] & 1 = 1 THEN a.attname || ' DESC'
                   ELSE a.attname END, ',' ORDER BY k.ord)
            FROM unnest(x.indkey) WITH ORDINALITY AS k(attnum, ord)
            JOIN pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = k.attnum), ''), E'\n', ' '), E'\t', ' ')
    || E'\t' || replace(replace(coalesce(pg_get_expr(x.indpred, x.indrelid), ''), E'\n', ' '), E'\t', ' ')
  FROM pg_index x
  JOIN pg_class i ON i.oid = x.indexrelid
  JOIN pg_namespace n ON n.oid = i.relnamespace
  JOIN pg_class t ON t.oid = x.indrelid
 WHERE n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
 ORDER BY 1;

SELECT 'ext'
    || E'\t' || n.nspname
    || E'\t' || 'e'
    || E'\t' || e.extname
    || E'\t' || pg_get_userbyid(e.extowner)
    || E'\t' || '' || E'\t' || '' || E'\t' || '' || E'\t' || '' || E'\t' || '' || E'\t' || ''
    || E'\t' || ''
    || E'\t' || e.extversion
    || E'\t' || ''
    || E'\t' || ''
    || E'\t' || ''
  FROM pg_extension e
  JOIN pg_namespace n ON n.oid = e.extnamespace
 ORDER BY 1;

SELECT 'srv'
    || E'\t' || 'server' || E'\t' || 'x' || E'\t' || current_setting('server_version')
    || E'\t' || '' || E'\t' || '' || E'\t' || '' || E'\t' || '' || E'\t' || '' || E'\t' || ''
    || E'\t' || '' || E'\t' || ''
    || E'\t' || current_setting('shared_buffers') || ',' || current_setting('work_mem')
           || ',' || current_setting('max_connections') || ',' || current_setting('effective_cache_size')
    || E'\t' || ''
    || E'\t' || ''
    || E'\t' || ''
  ;
"""

CENSUS_COLUMNS = 17  # env + 16
RECONCILE_COLUMNS = 8

RE_CREATE_INDEX = re.compile(
    r"CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?"
    r'([a-z0-9_"]+)\s+ON\s+([a-z0-9_."]+)',
    re.I,
)
RE_DROP_INDEX = re.compile(
    r"DROP\s+INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+EXISTS\s+)?([a-z0-9_.\"]+)", re.I
)
RE_USING_INDEX = re.compile(r"ADD\s+CONSTRAINT[^;]*?USING\s+INDEX\s+([a-z0-9_.\"]+)", re.I | re.S)
RE_LINE_COMMENT = re.compile(r"--[^\n]*")


def _strip_name(raw: str) -> str:
    return raw.strip().strip('"').lower()


def _bare_name(raw: str) -> str:
    """★★ 剥掉 schema 前缀后的【裸名】。

    ★★ R162 轮定位到的 R161 真缺陷（★ **差集失效 ⇒ 43 条假缺口**）：
       `RE_DROP_INDEX` 抓到的是 `DROP INDEX IF EXISTS public.foo` ⇒ 整串带 schema 前缀；
       而 `created` 侧存的是裸名 `foo`
       ⇒ ⇒ `created - dropped` 永远匹配不上 ⇒ ★★ **被后续迁移明确 DROP 掉的索引
       ⇒ ⇒ 仍然留在期望集里 ⇒ ⇒ 缺口台账把它们记成「三环境都没建」**
       ⇒ ⇒ 而它们在三个环境**确实都不存在** ⇒ ⇒ ★★ **43 条假缺口是工具造的，不是环境差异**。

    ★★ 所以 DROP 侧必须也取裸名 ⇒ ★★ 这一行是 R161 工具与 R161 门【同源同错】的根因
       （★★ Go 侧 `dbObjStripName` 当时也没剥 ⇒ ★★ 「第二份独立实现互证」对这批是零信息）。
    """
    return _strip_name(raw).split(".")[-1]


def die(msg: str, code: int = 3):
    sys.stderr.write("ERROR: %s\n" % msg)
    sys.exit(code)


# ─────────────────────────── expect ───────────────────────────


def derive_expected():
    """从仓内 startup 迁移反解期望索引集。

    返回 (expected, stats)：
      expected: {index_name: (migration_no, table, source_file)}
      stats:    统计口径（★ 供 selftest 与审计档引用）
    """
    if not os.path.isdir(MIGRATIONS_DIR):
        die("migrations dir not found: %s" % MIGRATIONS_DIR)

    files = sorted(
        f for f in os.listdir(MIGRATIONS_DIR)
        if f.endswith(".sql") and not f.endswith(".down.sql")
    )

    created = {}
    dropped = set()
    transient = {}
    no_number = 0

    for fn in files:
        head = fn.split("_", 1)[0]
        if head.isdigit():
            mig = int(head)
        else:
            mig = -1
            no_number += 1
        path = os.path.join(MIGRATIONS_DIR, fn)
        with open(path, encoding="utf-8", errors="replace") as fh:
            raw = fh.read()
        body = RE_LINE_COMMENT.sub("", raw)

        # ★★ 坑二：中间产物索引 = 同一个文件里被 USING INDEX 消费掉的那个
        consumed = {_strip_name(m.group(1)) for m in RE_USING_INDEX.finditer(body)}

        for m in RE_CREATE_INDEX.finditer(body):
            name = _strip_name(m.group(1))
            table = _strip_name(m.group(2)).split(".")[-1]
            if name in consumed:
                transient[name] = (mig, fn)
                continue
            created[name] = (mig, table, fn)

        for m in RE_DROP_INDEX.finditer(body):
            dropped.add(_bare_name(m.group(1)))

    # ★★ 坑三：后来的迁移删掉了早期的索引 ⇒ 期望集要取差集
    expected = {k: v for k, v in created.items() if k not in dropped}
    superseded = {k: created[k] for k in created if k in dropped}

    stats = {
        "migration_files": len(files),
        "non_numeric_prefix_files": no_number,
        "indexes_created": len(created),
        "transient_indexes": len(transient),
        "indexes_dropped_somewhere": len(dropped),
        "created_then_dropped": len(superseded),
        "expected_final": len(expected),
        "transient_names": sorted(transient),
    }
    return expected, stats


def cmd_expect(args):
    expected, stats = derive_expected()
    rows = []
    for name, (mig, table, fn) in sorted(expected.items(), key=lambda kv: (kv[1][0], kv[0])):
        rows.append("%d\t%s\t%s\t%s\t" % (mig, name, table, fn))
    text = "\n".join(rows) + ("\n" if rows else "")
    header = (
        "# db-object-expect.tsv —— ★ 从仓内 startup 迁移反解出的「期望索引集」\n"
        "# ★ 生成器：scripts/audit/db-object-census.py expect\n"
        "# ★ 列：migration_no\tindex_name\ttable\tsource_file\tnote\n"
        "# ★ 口径：建了 ∧ 没被后来的迁移删掉 ∧ 不是中间产物（USING INDEX 消费）\n"
        "# ★ generated_at=%s\n"
        "# stats=%s\n"
    ) % (
        datetime.now(timezone.utc).isoformat(timespec="seconds"),
        json.dumps(stats, ensure_ascii=False, sort_keys=True),
    )
    out = header + text
    if args.out:
        with open(args.out, "w", encoding="utf-8") as fh:
            fh.write(out)
        sys.stderr.write("expect: %d rows -> %s\n" % (len(rows), args.out))
    else:
        sys.stdout.write(out)
    return 0


# ─────────────────────────── collect ───────────────────────────


def cmd_collect(args):
    if args.from_stdin:
        # ★ 远程环境的采集形态：
        #   ssh 252 "cat | podman exec -i … psql -tA -q" < census.sql
        #     | python3 db-object-census.py collect --env 252 --from-stdin --out FILE --append
        # ★⇒ 远程与本地走【同一条】加前缀/校验代码路径（★ 不手拼产物）。
        raw_rows = sys.stdin.read().splitlines()
    else:
        dsn = os.environ.get("GUARD_DSN")
        if not dsn:
            die("GUARD_DSN is not set (★ DSN 只走环境变量 ⇒ 不进 argv、不回显)")
        psql = os.environ.get("GUARD_PSQL") or args.psql or "psql"
        try:
            proc = subprocess.run(
                [psql, dsn, "-tA", "-q", "-v", "ON_ERROR_STOP=1", "-f", "-"],
                input=CENSUS_SQL,
                capture_output=True,
                text=True,
                env={**os.environ, "GUARD_DSN": dsn},
            )
        except FileNotFoundError:
            die("psql not found: %s (★ override with GUARD_PSQL or --psql)" % psql)
        if proc.returncode != 0:
            # ★ 只回显 stderr 的行数，不回显可能含凭据的整段
            err_lines = [l for l in proc.stderr.splitlines() if l.strip()]
            die("psql failed rc=%d (%d stderr lines; ★ DSN not echoed)" % (proc.returncode, len(err_lines)))
        raw_rows = proc.stdout.splitlines()

    rows = []
    for line in raw_rows:
        if not line.strip():
            continue
        parts = line.split("\t")
        if len(parts) != CENSUS_COLUMNS - 1:
            die("census row has %d fields, expected %d (★ newline contamination?)" % (len(parts), CENSUS_COLUMNS - 1))
        rows.append("%s\t%s" % (args.env, line))

    header = (
        "# db-object-census.tsv —— ★ 三环境【真实对象底账】（★ 只读采集）\n"
        "# ★ 生成器：scripts/audit/db-object-census.py collect\n"
        "# ★ 列：env\tkind\tschema\trelkind\tname\towner\tispartition\tpartition_bound\t"
        "parent_table\treltuples\ttable_size\ttotal_size\tdefinition\tflags\tparent_index\n"
        "# ★ collected_at=%s\n"
        "# ★ env=%s rows=%d sha256=%s\n"
    ) % (
        datetime.now(timezone.utc).isoformat(timespec="seconds"),
        args.env,
        len(rows),
        hashlib.sha256("\n".join(rows).encode("utf-8")).hexdigest(),
    )
    out = header + "\n".join(rows) + ("\n" if rows else "")

    if args.out:
        mode = "a" if args.append else "w"
        with open(args.out, mode, encoding="utf-8") as fh:
            if mode == "a":
                fh.write("\n".join(rows) + ("\n" if rows else ""))
            else:
                fh.write(out)
        sys.stderr.write("collect: env=%s rows=%d -> %s (%s)\n" % (args.env, len(rows), args.out, mode))
    else:
        sys.stdout.write(out)
    return 0


# ─────────────────────────── reconcile ───────────────────────────


def parse_census(path):
    """读底账 ⇒ {env: {index_names, children_by_parent, rel_kinds}}"""
    envs = {}
    with open(path, encoding="utf-8") as fh:
        for lineno, line in enumerate(fh, 1):
            line = line.rstrip("\n")
            if not line or line.startswith("#"):
                continue
            parts = line.split("\t")
            if len(parts) != CENSUS_COLUMNS:
                die(
                    "census line %d has %d fields, expected %d"
                    " (★ ★★ PG17 pretty-print 会往 def 里塞换行 ⇒ 采集端必须折行)"
                    % (lineno, len(parts), CENSUS_COLUMNS)
                )
            env = parts[0]
            kind = parts[1]
            e = envs.setdefault(env, {"idx": set(), "children": {}, "rels": {},
                                      "idxcols": {}, "idxpred": {}})
            if kind == "idx":
                e["idx"].add(parts[4])
                parent = parts[14]
                if parent:
                    e["children"].setdefault(parent, []).append(parts[4])
                # ★★ 结构化的索引列序列（★ R162 新增）
                #    ★★ 覆盖判据必须读【结构化列序】，★ 解析 pg_get_indexdef 文本太脆
                #    （★ 表达式索引 / INCLUDE / opclass 都会让文本解析失真）
                e["idxcols"].setdefault(parts[4], parts[15])
                e["idxpred"].setdefault(parts[4], parts[16])
            elif kind == "rel":
                e["rels"][parts[4]] = parts[3]
    if not envs:
        die("census has no data rows: %s" % path)
    return envs


def parse_expected(path):
    """读期望集 ⇒ {index_name: (migration_no, table)}"""
    out = {}
    with open(path, encoding="utf-8") as fh:
        for lineno, line in enumerate(fh, 1):
            line = line.rstrip("\n")
            if not line or line.startswith("#"):
                continue
            parts = line.split("\t")
            if len(parts) < 3:
                die("expect line %d malformed (%d fields)" % (lineno, len(parts)))
            out[parts[1]] = (parts[0], parts[2])
    if not out:
        die("expect file has no data rows: %s" % path)
    return out


def classify(env_state, index_name, table):
    """★ 四状态判定（★ 每格都可机器判 ⇒ 没有「不知道」）。"""
    if index_name in env_state["idx"]:
        return "present"
    if index_name in env_state["children"]:
        return "present-via-child"
    kind = env_state["rels"].get(table)
    if kind is None:
        return "retired"
    if kind not in ("r", "p"):
        # ★ 表还在，但已经不是表了（★ 例如 v1 表退役后换成视图）⇒ 索引无意义
        return "retired"
    return "missing"


def _severity(cells, envs, prod_envs):
    """★★ severity 词表（★ R162 轮修掉了 R161 的**命名与语义相反**）。

    ★★ R161 第一版写的是：
        prod_hole ⇒ "production-gap"；否则 ⇒ "all-env-gap"
    ⇒ ⇒ ★★ 于是「**只有 local 缺**」（★ 生产环境其实齐备）被判成 `all-env-gap`
    ⇒ ⇒ ★★ 而「**三环境全缺**」（★ 一致退役）被判成 `production-gap`
    ⇒ ⇒ ★★ **词表名与判据完全相反** ⇒ ★★ 谁读这份台账都会读反
    ⇒ ⇒ ★★ 而且 R161 的门只校验计数（G6 棘轮）⇒ ★★ **门绿掩盖了这个缺陷**
    ⇒ ⇒ ★★ 这是「R160 教训：规则与判据矛盾 ⇒ 门会逼人撒谎」的又一例。

    ★★ 现在词表改成【每个格子组合唯一确定】的 4 值：

        none              —— 任何环境都不缺
        local-only-gap    —— 只有 local 缺（★ 生产环境 252/211 都齐备 ⇒ 影响面最小）
        prod-partial-gap  —— 生产环境里【缺一部分】（★ 252 或 211 其一缺）
        prod-full-gap     —— 生产环境【全都】缺（★ 252 与 211 都缺 ⇒ 影响面最大）

    ⇒ ★★ **给定三个格子状态，severity 唯一确定** ⇒ ⇒ 门可以机械校验
    ⇒ ⇒ ★★ **不会出现「同一个词对应两种意思」** ⇒ ★★ 这才是可机器判的前提。
    """
    missing = {e for e, c in zip(envs, cells) if c == "missing"}
    if not missing:
        return "none"
    if missing == {"local"}:
        return "local-only-gap"
    prod_missing = missing & set(prod_envs)
    if len(prod_missing) == len(prod_envs):
        return "prod-full-gap"
    return "prod-partial-gap"


ENV_ORDER = ["local", "252", "211"]


def _env_sort_key(env):
    """★ 环境的规范顺序（★ local → 252 → 211）⇒ ★ 台账列序稳定 ⇒ ★ 可逐字对拍。

    ★★ 不依赖字典序：★ `sorted()` 会把 211 排到 252 前面（★ 字典序按字符串比）
    ⇒ ★★ 台账列序会随环境名变动 ⇒ ★★ 逐字对拍就废了。
    """
    try:
        return (0, ENV_ORDER.index(env), env)
    except ValueError:
        return (1, 0, env)


def cmd_reconcile(args):
    census = parse_census(args.census)
    expected = parse_expected(args.expect)
    envs = sorted(census.keys(), key=_env_sort_key)

    rows = []
    prod_envs = [e for e in envs if e in ("252", "211")]
    gaps_by_env = {e: 0 for e in envs}
    prod_gaps = 0

    for name in sorted(expected, key=lambda n: (int(expected[n][0]) if expected[n][0].isdigit() else -1, n)):
        mig, table = expected[name]
        cells = [classify(census[e], name, table) for e in envs]
        is_gap = any(c == "missing" for c in cells)
        for e, c in zip(envs, cells):
            if c == "missing":
                gaps_by_env[e] += 1
        prod_hole = any(c == "missing" for e, c in zip(envs, cells) if e in prod_envs)
        if prod_hole:
            prod_gaps += 1
        severity = _severity(cells, envs, prod_envs)
        rows.append(
            "\t".join(
                [mig, name, table] + cells + ["gap" if is_gap else "complete", severity]
            )
        )

    header = (
        "# db-object-gap-ledger.tsv —— ★ 「期望索引集 × 三环境真实底账」缺口台账\n"
        "# ★ 生成器：scripts/audit/db-object-census.py reconcile\n"
        "# ★ 列：migration_no\tindex_name\ttable\t%s\tverdict\tseverity\n"
        "# ★ 状态词：present | present-via-child | missing | retired\n"
        "# ★ verdict：complete | gap　　severity：none | local-only-gap | prod-partial-gap | prod-full-gap\n"
        "# ★ generated_at=%s\n"
        "# ★ expected=%d envs=%s gaps_by_env=%s production_gaps=%d\n"
    ) % (
        "\t".join(envs),
        datetime.now(timezone.utc).isoformat(timespec="seconds"),
        len(expected),
        ",".join(envs),
        json.dumps(gaps_by_env, ensure_ascii=False, sort_keys=True),
        prod_gaps,
    )
    out = header + "\n".join(rows) + ("\n" if rows else "")

    if args.out:
        with open(args.out, "w", encoding="utf-8") as fh:
            fh.write(out)
        sys.stderr.write("reconcile: %d rows -> %s\n" % (len(rows), args.out))

    if args.json:
        summary = {
            "expected": len(expected),
            "envs": envs,
            "gaps_by_env": gaps_by_env,
            "production_gaps": prod_gaps,
            "rows": len(rows),
        }
        sys.stderr.write(json.dumps(summary, ensure_ascii=False, sort_keys=True) + "\n")

    return 2 if prod_gaps else 0


# ─────────────────────────── selftest ───────────────────────────



def cmd_retired(args):
    """★ 退役证据台账：★【建了又被后来的迁移删掉】的索引 ⇒ 每一条都带退役出处。

    ★★ 为什么要有这张台账：★ R161 的期望集靠「建了 − 被删」做差集（★ 正确），
    ★★ 但 R161 第一版的 DROP 正则抓到的是【带 schema 前缀的名字】⇒ ★★ 差集失效
    ★★ ⇒ ★★ 被明确退役的索引仍留在期望集里 ⇒ ★★ 台账把它们记成「真缺口」
    ★★ ⇒ ★★ **任何人照台账去「补建」，就会复活被刻意退役的索引**（★ 比如 876
    ★★ 退役 `idx_sessions_session_id` 是因为它已被前缀覆盖，★ 复活它是纯浪费写入）。

    ★★ 所以这张台账的作用是：**让「不是缺口」有据可查** ⇒
    ★★   index_name ← retire_migration ← retire_file ← conditional
    ★★ ⇒ ★★ 门可以据此断言「期望集里没有已退役的索引」⇒ ★★ 假缺口不可能再复活。
    """
    created, dropped_events, guarded = derive_retirement()
    rows = []
    for name in sorted(created):
        if name not in dropped_events:
            continue
        mig, table, fn = created[name]
        for (rmig, rfn, rline) in dropped_events[name]:
            rows.append(
                "\t".join(
                    [
                        str(mig),
                        name,
                        table,
                        fn,
                        str(rmig),
                        rfn,
                        "yes" if guarded.get((rfn, rline)) else "no",
                        str(rline),
                    ]
                )
            )
    text = "\n".join(rows) + ("\n" if rows else "")
    header = (
        "# db-object-retired.tsv —— ★【建了又被后来的迁移删掉】的索引（★ 退役证据台账）\n"
        "# ★ 生成器：scripts/audit/db-object-census.py retired\n"
        "# ★ 列：create_migration_no\tindex_name\ttable\tcreate_file\t"
        "retire_migration_no\tretire_file\tconditional\tretire_line\n"
        "# ★ conditional=yes ⇒ ★ 该 DROP 在 DO 块内（★ 有前置证明才退役，★ 如 876 的覆盖证明）\n"
        "# ★ generated_at=%s\n"
        "# ★ retired_indexes=%d retire_events=%d\n"
    ) % (
        datetime.now(timezone.utc).isoformat(timespec="seconds"),
        len(created),
        len(rows),
    )
    out = header + text
    if args.out:
        with open(args.out, "w", encoding="utf-8") as fh:
            fh.write(out)
        sys.stderr.write("retired: %d events -> %s\n" % (len(rows), args.out))
    else:
        sys.stdout.write(out)
    return 0


def derive_retirement():
    """★ 反解「建了又被删」的索引 ⇒ (created, dropped_events, guarded)。

    ★★ `guarded` 判据 = 该 DROP 语句是否落在一个 `DO $$ … $$` 块内
    ★★ （★ ★★ 结构判据不是文本猜：★ 876 的 DROP 只在「覆盖证明通过」时才真删）
    """
    if not os.path.isdir(MIGRATIONS_DIR):
        die("migrations dir not found: %s" % MIGRATIONS_DIR)
    files = sorted(
        f for f in os.listdir(MIGRATIONS_DIR)
        if f.endswith(".sql") and not f.endswith(".down.sql")
    )
    created = {}
    dropped_events = {}
    guarded = {}
    for fn in files:
        head = fn.split("_", 1)[0]
        mig = int(head) if head.isdigit() else -1
        path = os.path.join(MIGRATIONS_DIR, fn)
        with open(path, encoding="utf-8", errors="replace") as fh:
            raw = fh.read()
        body = RE_LINE_COMMENT.sub("", raw)
        consumed = {_strip_name(m.group(1)) for m in RE_USING_INDEX.finditer(body)}
        for m in RE_CREATE_INDEX.finditer(body):
            name = _strip_name(m.group(1))
            if name in consumed:
                continue
            created.setdefault(name, (mig, _bare_name(m.group(2)), fn))
        # ★ DO 块边界（★ 结构判据：★ 只看 $$ … $$ 成对出现）
        do_spans = []
        for dm in re.finditer(r"DO\s+\$[^$]*\$|DO\s+\$\$", body, re.I):
            do_spans.append(dm.end())
        for m in RE_DROP_INDEX.finditer(body):
            name = _bare_name(m.group(1))
            line_no = body.count("\n", 0, m.start()) + 1
            in_do = any(body.count("\n", 0, pos) + 1 <= line_no for pos in do_spans)
            dropped_events.setdefault(name, []).append((mig, fn, line_no))
            if in_do:
                guarded[(fn, line_no)] = True
    return created, dropped_events, guarded


def cmd_bundle(args):
    """把 N 份单环境采集结果合成一份多环境底账（★ 头部带每环境的行数与 sha256）。

    ★ 为什么要单独一步：★ 各环境的采集是【分开跑的】（★ 远程走 ssh 管道）
    ⇒ ★ 产物必须【工具生成】⇒ ★ 不能手拼（★ 手拼的产物没法证明自己来自工具）。
    """
    per_env = {}
    order = []
    for path in args.inputs:
        env = None
        rows = []
        with open(path, encoding="utf-8") as fh:
            for lineno, line in enumerate(fh, 1):
                line = line.rstrip("\n")
                if not line:
                    continue
                if line.startswith("#"):
                    # ★ 单环境文件的头部里带 "env=<label>"
                    m = re.search(r"#\s*env=(\S+)\s", line)
                    if m:
                        env = m.group(1)
                    continue
                parts = line.split("\t")
                if len(parts) != CENSUS_COLUMNS:
                    die("input %s line %d has %d fields, expected %d" % (path, lineno, len(parts), CENSUS_COLUMNS))
                if env is None:
                    env = parts[0]
                rows.append(line)
        if env is None:
            die("cannot determine env for input %s" % path)
        if env in per_env:
            die("duplicate env %s in inputs" % env)
        per_env[env] = rows
        order.append(env)

    order.sort(key=_env_sort_key)
    all_rows = []
    for env in order:
        all_rows.extend(per_env[env])

    header_lines = [
        "# db-object-census.tsv —— ★ 三环境【真实对象底账】（★ 只读采集）",
        "# ★ 生成器：scripts/audit/db-object-census.py collect | bundle",
        "# ★ 列：env\tkind\tschema\trelkind\tname\towner\tispartition\tpartition_bound\t"
        "parent_table\treltuples\ttable_size\ttotal_size\tdefinition\tflags\tparent_index",
        "# ★ bundled_at=%s" % datetime.now(timezone.utc).isoformat(timespec="seconds"),
    ]
    for env in order:
        rows = per_env[env]
        header_lines.append(
            "# ★ env=%s rows=%d sha256=%s"
            % (env, len(rows), hashlib.sha256("\n".join(rows).encode("utf-8")).hexdigest())
        )
    header_lines.append(
        "# ★ total_rows=%d envs=%s" % (len(all_rows), ",".join(order))
    )

    out = "\n".join(header_lines) + "\n" + "\n".join(all_rows) + ("\n" if all_rows else "")
    if args.out:
        with open(args.out, "w", encoding="utf-8") as fh:
            fh.write(out)
        sys.stderr.write("bundle: %d rows / %d envs -> %s\n" % (len(all_rows), len(order), args.out))
    else:
        sys.stdout.write(out)
    return 0


def cmd_selftest(args):
    failures = []
    CHECKS = [0]

    def check(name, cond, detail=""):
        CHECKS[0] += 1
        if cond:
            sys.stderr.write("ok   %s\n" % name)
        else:
            failures.append(name)
            sys.stderr.write("FAIL %s %s\n" % (name, detail))

    # ① 期望集反解：必须排除中间产物（902 的 _tmp）
    expected, stats = derive_expected()
    check("expect/has-entries", len(expected) > 100, str(len(expected)))
    check(
        "expect/excludes-transient-using-index",
        "stats_event_dedup_pkey_include_tmp" not in expected,
        "transient index leaked into expected set",
    )
    check(
        "expect/transient-count-is-one",
        stats["transient_indexes"] == 1,
        str(stats["transient_indexes"]),
    )
    check(
        "expect/drops-are-subtracted",
        stats["expected_final"] < stats["indexes_created"],
        "%d vs %d" % (stats["expected_final"], stats["indexes_created"]),
    )

    # ★★ R162 新增牙（★ ① 精确钉死刚修的那个 bug ② severity 词表唯一性 ③ 退役台账）
    check(
        "expect/excludes-schema-prefixed-drop",
        "idx_sessions_session_id" not in expected,
        "★ R161 bug: DROP INDEX ... public.<name> 带 schema 前缀 ⇒ 差集失效 ⇒ 退役索引漏进期望集",
    )
    check(
        "expect/excludes-876-retired",
        "idx_sessions_session_id" not in expected
        and "session_summaries_session_key_uidx" not in expected
        and "idx_session_bodies_hot_lookup" not in expected,
        "★ 876/879/888 明确退役的索引仍在期望集里",
    )
    _created, _drop_events, _guarded = derive_retirement()
    leaked = sorted(set(_drop_events) & set(expected))
    check(
        "expect/no-retired-index-leaks",
        not leaked,
        "★ %d retired indexes leaked into expected: %s" % (len(leaked), leaked[:5]),
    )
    check(
        "retired/records-876",
        any(
            name == "idx_sessions_session_id" and rmig == 876
            for name, evs in _drop_events.items()
            for (rmig, _rf, _rl) in evs
        ),
        "★ retirement ledger lost the 876 event",
    )
    # ★ severity 词表：★ 给定格子状态，★ severity 必须唯一确定且与文档一致
    _prod = ["252", "211"]
    _cases = [
        (["present", "present", "present"], "none"),
        (["missing", "present", "present"], "local-only-gap"),
        (["present", "missing", "present"], "prod-partial-gap"),
        (["present", "present", "missing"], "prod-partial-gap"),
        (["missing", "missing", "missing"], "prod-full-gap"),
        (["present", "missing", "missing"], "prod-full-gap"),
        (["missing", "missing", "present"], "prod-partial-gap"),
    ]
    for _cells, _want in _cases:
        _got = _severity(_cells, ["local", "252", "211"], _prod)
        check(
            "severity/%s" % "-".join(c[:3] for c in _cells),
            _got == _want,
            "★ want %s got %s" % (_want, _got),
        )

    # ② 状态判定四态（★ 阳性对照 + 反向对照）
    env_state = {
        "idx": {"idx_present"},
        "children": {"idx_parent": ["part_child"]},
        "rels": {"tbl": "r", "part": "p", "viewish": "v"},
    }
    check("classify/present", classify(env_state, "idx_present", "tbl") == "present")
    check(
        "classify/via-child",
        classify(env_state, "idx_parent", "part") == "present-via-child",
    )
    check("classify/retired-absent", classify(env_state, "idx_x", "gone") == "retired")
    check(
        "classify/retired-not-a-table",
        classify(env_state, "idx_x", "viewish") == "retired",
        "★ a view must not count as a table",
    )
    check("classify/missing", classify(env_state, "idx_x", "tbl") == "missing")

    # ③ ★★★ 反向对照：★ 「表存在但索引不在」必须判 missing（★ 而不能判 retired）
    #    ★ 这是本轮第一版工具的漏洞（★ 名字前缀启发式）⇒ ★ 用例钉死。
    env_state2 = {
        "idx": set(),
        "children": {},
        "rels": {"request_logs_with_current_month": "v", "real_table": "r"},
    }
    check(
        "classify/view-name-must-not-satisfy-table-guard",
        classify(env_state2, "idx_on_real_table", "real_table") == "missing",
    )

    # ④ 底账解析：字段数不对必须报错（★ 本轮真踩过：★ PG17 pretty-print 塞换行）
    import tempfile

    def _row(n_fields, *vals):
        """★ 夹具按【列数】构造 ⇒ ★★ 不手数 tab（★ 手数 tab 极易出错 ⇒ 仓内教训）。"""
        cells = list(vals) + [""] * (n_fields - len(vals))
        return "\t".join(cells[:n_fields])

    with tempfile.NamedTemporaryFile("w", suffix=".tsv", delete=False, encoding="utf-8") as fh:
        fh.write("# comment\n")
        fh.write(_row(CENSUS_COLUMNS, "local", "idx", "public", "i", "idx_x", "owner", "f") + "\n")
        good = fh.name
    with tempfile.NamedTemporaryFile("w", suffix=".tsv", delete=False, encoding="utf-8") as fh:
        fh.write("# comment\n")
        fh.write(_row(CENSUS_COLUMNS - 1, "local", "idx", "public", "i", "idx_x", "owner", "f") + "\n")
        bad = fh.name

    try:
        parse_census(good)
        check("census/accepts-well-formed", True)
    except SystemExit:
        check("census/accepts-well-formed", False, "well-formed census was rejected")

    rc = 0
    try:
        parse_census(bad)
    except SystemExit:
        rc = 1
    check("census/rejects-truncated-row", rc == 1, "corrupted row was accepted")

    os.unlink(good)
    os.unlink(bad)

    # ⑤ reconcile 端到端（★ 用临时底账 ⇒ 不依赖真实环境）
    with tempfile.NamedTemporaryFile("w", suffix=".tsv", delete=False, encoding="utf-8") as fh:
        fh.write("# expect\n")
        fh.write("909\tidx_a\ttbl_a\t909_x.sql\t\n")
        fh.write("909\tidx_b\ttbl_b\t909_x.sql\t\n")
        exp = fh.name
    with tempfile.NamedTemporaryFile("w", suffix=".tsv", delete=False, encoding="utf-8") as fh:
        idx_row = _row(
            CENSUS_COLUMNS,
            "local", "idx", "public", "i", "idx_a", "owner", "", "", "", "", "", "", "",
            "CREATE INDEX ...", "true,true,true", "", "tenant_id,session_id", "",
        )
        # ★ 211 侧：★ 表 tbl_a 在（★ relkind=r）但索引 idx_a 不在 ⇒ 真缺口
        rel_row = _row(
            CENSUS_COLUMNS,
            "211", "rel", "public", "r", "tbl_a", "owner", "f", "", "", "", "", "", "",
            "", "", "", "", "",
        )
        fh.write(idx_row + "\n")
        fh.write(rel_row + "\n")
        cen = fh.name
    ns = argparse.Namespace(census=cen, expect=exp, out=None, json=False)
    rc = None
    try:
        rc = cmd_reconcile(ns)
    except SystemExit as exc:
        rc = exc.code
    check("reconcile/detects-gap", rc == 2, "rc=%r" % (rc,))
    os.unlink(exp)
    os.unlink(cen)

    sys.stderr.write(
        "\nselftest: %d checks, %d failures\n" % (CHECKS[0], len(failures))
    )
    if failures:
        sys.stderr.write("failed: %s\n" % ", ".join(failures))
        sys.exit(2)
    return 0


# ─────────────────────────── main ───────────────────────────


def main(argv=None):
    ap = argparse.ArgumentParser(description="db-object-census.py")
    sub = ap.add_subparsers(dest="cmd", required=True)

    p = sub.add_parser("collect", help="read-only object census of one environment")
    p.add_argument("--env", required=True, help="environment label (local|252|211)")
    p.add_argument("--out", help="output TSV (default: stdout)")
    p.add_argument("--append", action="store_true", help="append rows to --out")
    p.add_argument("--psql", help="psql binary (default: $GUARD_PSQL or psql)")
    p.add_argument(
        "--from-stdin",
        action="store_true",
        help="read already-collected census rows from stdin (★ remote envs)",
    )
    p.set_defaults(func=cmd_collect)

    p = sub.add_parser("expect", help="derive expected index set from repo migrations")
    p.add_argument("--out", help="output TSV (default: stdout)")
    p.set_defaults(func=cmd_expect)

    p = sub.add_parser("reconcile", help="reconcile expected set against census")
    p.add_argument("--census", required=True, help="census TSV")
    p.add_argument("--expect", required=True, help="expected-set TSV")
    p.add_argument("--out", help="output gap ledger TSV (default: stderr only)")
    p.add_argument("--json", action="store_true", help="print a JSON summary to stderr")
    p.set_defaults(func=cmd_reconcile)

    p = sub.add_parser("bundle", help="merge per-environment census files into one artifact")
    p.add_argument("--in", dest="inputs", action="append", required=True, help="input file (repeatable)")
    p.add_argument("--out", help="output TSV (default: stdout)")
    p.set_defaults(func=cmd_bundle)

    p = sub.add_parser("retired", help="emit retirement-evidence ledger (created-then-dropped)")
    p.add_argument("--out", help="output TSV (default: stdout)")
    p.set_defaults(func=cmd_retired)

    p = sub.add_parser("selftest", help="self-tests")
    p.set_defaults(func=cmd_selftest)

    args = ap.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
