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
import io
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
RECONCILE_COLUMNS = 9  # ★ R163：★ 加了 fresh 环境 ⇒ 8 列 → 9 列
COLUMNS_ARTIFACT_COLUMNS = 4  # ★ R163：★ env + schema + table + column

# ★★ R164 新增：★【结构漂移】与【指标漂移】必须分开算 ——
# ★★ 理由（★ 本轮实测出来的坑）：★ 底账里带着 `reltuples` / `table_size` / `total_size`
# ★★ 这三列【每次 autovacuum / analyze 都会变】⇒ ⇒ 若拿整行做差集 ⇒ ⇒ **任何环境任何时刻**
# ★★ 都会报「漂移」⇒ ⇒ ⇒ **噪声淹没的判据等于没有判据**。
# ★★ ⇒ ⇒ 【对象没了 / 多了 / 形态变了】与【大小在动】必须分开报。
# ★★ 排除面必须【显式且被门钉住】⇒ ★ 少排除 = 噪声，★ 多排除 = 漏报。
VOLATILE_CENSUS_COLUMNS = ("reltuples", "table_size", "total_size")

# ★ 底账 17 列的列名（★ 与 `collect` 的 SELECT 顺序逐字对应 ⇒ 判据按名字写，不按序号）。
CENSUS_COLUMN_NAMES = (
    "env", "kind", "schema", "relkind", "name", "owner", "ispartition",
    "partition_bound", "parent_table", "reltuples", "table_size", "total_size",
    "definition", "flags", "parent_index", "key_columns", "predicate",
)
assert len(CENSUS_COLUMN_NAMES) == CENSUS_COLUMNS
assert set(VOLATILE_CENSUS_COLUMNS) <= set(CENSUS_COLUMN_NAMES)


# ★★ R163 新增：★【列底账】的采集 SQL（★ 为什么单列一张表而不是塞进底账）。
# ★★ 理由：★★ 「列级联退役」的判据必须回答【这个列在本环境还在不在】
# ★★ ⇒ ⇒ 必须有【每环境每表每列】的事实 ⇒ ⇒ 底账（17 列）装不下 ⇒ ⇒ 单列一张。
# ★★ ★★ 而这一层事实此前**完全没有** ⇒ ⇒ 「列没了索引也没了」这件事在仓内是【不可见的】
# ★★ ⇒ ⇒ 假缺口的第三种来源一直躺在那儿（★ idx_sessions_last_full_at 实测命中）。
COLUMNS_SQL_TEMPLATE = r"""
SET statement_timeout = '120s';

SELECT n.nspname
    || E'\t' || c.relname
    || E'\t' || a.attname
  FROM pg_attribute a
  JOIN pg_class c ON c.oid = a.attrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE a.attnum > 0
   AND NOT a.attisdropped
   AND c.relkind IN ('r', 'p', 'm', 'f')
   AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
   %s
 ORDER BY 1;
"""


def columns_sql(pairs=None):
    """★ 生成列底账的采集 SQL。

    ★★ `pairs` 非空时只采【(表, 列)】白名单里的列。
    ★★ 为什么收窄：★★ 判据只需要回答「这个【键列】在本环境还在不在」
    ★★ ⇒ ⇒ 全量列（★ 实测 ~8800/环境 ⇒ 4 环境 39k 行 ⇒ ★★ 1.5MB 的仓内产物）
    ★★ ⇒ ⇒ 只采期望集索引的声明键列 ⇒ ★★ 规模降到 1/8 ⇒ ⇒ 而【判据覆盖度不变】。
    """
    if not pairs:
        return COLUMNS_SQL_TEMPLATE % ""
    quoted = ", ".join("('%s', '%s')" % (t.replace("'", "''"), c.replace("'", "''"))
                       for (t, c) in sorted(pairs))
    return COLUMNS_SQL_TEMPLATE % ("AND (c.relname, a.attname) IN (%s)" % quoted)


COLUMNS_SQL = columns_sql()

# ★★ R163 修掉的老 bug：★ `CREATE INDEX x ON ONLY <table>` ⇒ 老正则把表名吃成 "ONLY"
# ★★ ⇒ ⇒ 那 17 条索引在缺口台账里被判成 `retired`（★ 表不存在）⇒ ⇒ ★★ **真缺口被掩盖**
# ★★ ⇒ ⇒ 实测：★ 7 条四个环境全判 retired（★ 生产缺口被少算 7 条）
# ★★ ⇒ ⇒ + 4 条在 fresh 判 retired（★ fresh 缺口被少算 4 条）
# ★★ ⇒ ⇒ ★★ 这是「假退役」⇒ ★★ 危害是【掩盖真缺口】⇒ ★★ 与 R162 的「假缺口」正好相反。
RE_CREATE_INDEX = re.compile(
    r"CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?"
    r'([a-z0-9_"]+)\s+ON\s+(?:ONLY\s+)?([a-z0-9_."]+)',
    re.I,
)
RE_DROP_INDEX = re.compile(
    r"DROP\s+INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+EXISTS\s+)?([a-z0-9_.\"]+)", re.I
)
RE_USING_INDEX = re.compile(r"ADD\s+CONSTRAINT[^;]*?USING\s+INDEX\s+([a-z0-9_.\"]+)", re.I | re.S)
RE_LINE_COMMENT = re.compile(r"--[^\n]*")

# ★★ R163 新增：★【括号配对】的 CREATE INDEX 扫描（★ 不用正则的 `\(([^)]*)\)`）。
# ★★ 理由：★ 表达式索引（`lower(x)` / `(a->>'b')`）会被 `[^)]*` 截断在第一个 `)`，
# ★★ ⇒ 列清单不完整 ⇒ ⇒ 「列级联退役」判据会失真。
# ★★ 实测：★ 索引名集合逐字相同（842 = 842）、表名零差异 ⇒ ★★ 零回归；
# ★★ 而列文本有 16 个表达式索引从「被截断」变成「完整」。
RE_CREATE_INDEX_HEAD = re.compile(
    r"CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?"
    r'([a-z0-9_"]+)\s+ON\s+(?:ONLY\s+)?([a-z0-9_."]+)\s*(?:USING\s+[a-z0-9_]+\s*)?\(',
    re.I,
)

# ★★ R163 新增：★ ALTER TABLE … DROP COLUMN 的解析（★ 列级联退役的证据源）。
# ★★ ★★ 为什么按【语句】解析而不是一条正则吞到分号：★
# ★★ `ALTER TABLE t DROP COLUMN a, DROP COLUMN b, DROP COLUMN c;`（★ 708 就是这个形态）
# ★★ ⇒ 一条正则只能抓到第一条（★ 其余两条没有 DROP COLUMN 关键字 ⇒ 藏在逗号列表里）
# ★★ ⇒ ⇒ 实测漏掉 last_full_payload_at ⇒ ⇒ 判据失灵。
RE_ALTER_TABLE = re.compile(
    r"ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([a-z0-9_.\"]+)",
    re.I,
)


def _parse_drop_columns(stmt):
    """★ 从一条 ALTER TABLE 语句里取出被删的列名（★ 支持逗号列表与多条 DROP COLUMN）。"""
    m = re.search(r"DROP\s+COLUMN\s+(?:IF\s+EXISTS\s+)?([^;]+)", stmt, re.I)
    if not m:
        return []
    out = []
    for raw in m.group(1).split(","):
        p = raw.strip()
        p = re.sub(r"^IF\s+EXISTS\s+", "", p, flags=re.I)
        p = re.sub(r"^DROP\s+COLUMN\s+(IF\s+EXISTS\s+)?", "", p, flags=re.I)
        p = p.strip().strip('"')
        if not p or not re.match(r"^[A-Za-z_][A-Za-z0-9_$]*$", p):
            continue
        out.append(p.lower())
    return out


def _scan_create_indexes(body):
    """★ 括号配对扫描 CREATE INDEX ⇒ [(name, table, cols_text)]。

    ★★ 为什么不用正则的 `\(([^)]*)\)`：★ 表达式索引（lower(x) / (a->>'b')）
    ★★ 会被截断在第一个 `)` ⇒ 列清单不完整 ⇒ 「列级联退役」判据会失真。

    ★★★ ★★ 而 `USING <method>` 必须在表名与列清单之间被容忍：
    ★★★ `CREATE INDEX x ON t USING gin (tags)` ⇒ 若要求表名后紧跟 `(` ⇒ **漏掉这条索引**
    ★★★ ⇒ 实测漏 **45 条**（★ GIN / 表达式索引）⇒ ⇒ **期望集会缩 45 条** ⇒ ⇒ 回归。
    ★★★ ⇒ ⇒ 所以正则里显式允许 `(?:USING\s+[a-z0-9_]+\s*)?`。
    """
    out = []
    for m in RE_CREATE_INDEX_HEAD.finditer(body):
        depth = 0
        cols = ""
        for j in range(m.end() - 1, len(body)):
            ch = body[j]
            if ch == "(":
                depth += 1
            elif ch == ")":
                depth -= 1
                if depth == 0:
                    cols = body[m.end():j]
                    break
        name = _strip_name(m.group(1))
        table = _strip_name(m.group(2)).split(".")[-1]
        out.append((name, table, cols))
    return out


def _declared_key_columns(cols_text):
    """★ 取索引的【直接键列】（★ 保守口径：★ 表达式索引不取列名）。

    ★★ 为什么保守：★ 取错列名 ⇒ 把活索引误判成「被列级联退役」
    ★★ ⇒ 假缺口从「缺」翻成「不该有」⇒ ⇒ **两个方向都会骗人**
    ★★ ⇒ ⇒ 只在【证据明确】时退役（★ 直接键列 + 同表 + 更晚的 DROP COLUMN）。
    """
    parts = []
    depth = 0
    cur = ""
    for ch in cols_text:
        if ch == "(":
            depth += 1
            cur += ch
            continue
        if ch == ")":
            depth -= 1
            cur += ch
            continue
        if ch == "," and depth == 0:
            parts.append(cur)
            cur = ""
            continue
        cur += ch
    if cur.strip():
        parts.append(cur)
    cols = []
    for entry in parts:
        e = entry.strip()
        if not e:
            continue
        e = re.sub(r"\s+(ASC|DESC)\s+NULLS\s+(FIRST|LAST)$", "", e, flags=re.I)
        e = re.sub(r"\s+(ASC|DESC)$", "", e, flags=re.I)
        if "(" in e:
            continue
        e = e.strip().strip('"')
        if not e or not re.match(r"^[A-Za-z_][A-Za-z0-9_$]*$", e):
            continue
        cols.append(e.lower())
    return cols


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


# ★ 自证钩子（★ R164）：★ `verify` 的现场采集行的覆盖入口。
# ★ 只在 `selftest` 里被设置；★ 正常路径永远为 None ⇒ ★ 永远走真实采集。
_LIVE_ROWS_OVERRIDE = None


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
    # ★★ R163：★ 记下「创建顺序」⇒ 列级联退役要靠【先后】判定（★ 删列在建索引之后才算退役）
    order_of = {fn: i for i, fn in enumerate(files)}
    # ★★ R163：★ DROP COLUMN 事件（★ 列级联退役的证据源）
    drop_col_events = []

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

        # ★★ R163：★ 用【括号配对】扫描取声明列（★ 名字/表与老正则逐字一致 ⇒ 零回归）
        scanned = {}
        for (sname, stable, scols) in _scan_create_indexes(body):
            scanned[sname] = (stable, _declared_key_columns(scols))
        for m in RE_CREATE_INDEX.finditer(body):
            name = _strip_name(m.group(1))
            table = _strip_name(m.group(2)).split(".")[-1]
            if name in consumed:
                transient[name] = (mig, fn)
                continue
            stable, keycols = scanned.get(name, (table, []))
            created[name] = (mig, table, fn, keycols, order_of[fn])

        for m in RE_DROP_INDEX.finditer(body):
            dropped.add(_bare_name(m.group(1)))

        # ★★ R163：★ 解析 ALTER TABLE … DROP COLUMN（★ 语句级：★ 多列同语句要拆开）
        for am in RE_ALTER_TABLE.finditer(body):
            tbl = _strip_name(am.group(1)).split(".")[-1]
            nxt_semi = body.find(";", am.end())
            nxt_alt = RE_ALTER_TABLE.search(body, am.end())
            ends = [x for x in (nxt_semi, nxt_alt.start() if nxt_alt else -1) if x > 0]
            end = min(ends) if ends else len(body)
            stmt = body[am.end():end]
            for c in _parse_drop_columns(stmt):
                drop_col_events.append(
                    (order_of[fn], fn, tbl, c, body.count("\n", 0, am.start()) + 1)
                )

    # ★★ 坑三：后来的迁移删掉了早期的索引 ⇒ 期望集要取差集
    expected = {k: v for k, v in created.items() if k not in dropped}
    superseded = {k: created[k] for k in created if k in dropped}

    # ★★ R163：★ 列级联退役（★ 建索引之后、又有同表同列被 DROP COLUMN）
    # ★★ ⇒ PG 会【自动删掉】那个索引 ⇒ ⇒ 它不是「该建没建」，★ 是「被列的死亡带走」
    # ★★ ⇒ ⇒ 假缺口的第三种来源（★ 前两种：★ DROP 名字带 schema 前缀 / ★ 解析口径不一致）
    col_retired = {}
    for name, (mig, table, fn, keycols, order) in created.items():
        if not keycols:
            continue
        for (dorder, dfn, dtable, dcol, _dline) in drop_col_events:
            if dorder <= order or dtable != table:
                continue
            if dcol in keycols:
                col_retired[name] = (mig, table, fn, dfn, dcol, keycols, _dline)
                break

    stats = {
        "migration_files": len(files),
        "non_numeric_prefix_files": no_number,
        "indexes_created": len(created),
        "transient_indexes": len(transient),
        "indexes_dropped_somewhere": len(dropped),
        "created_then_dropped": len(superseded),
        "expected_final": len(expected),
        "transient_names": sorted(transient),
        "drop_column_events": len(drop_col_events),
        "column_retired_indexes": len(col_retired),
    }
    return expected, stats, col_retired, drop_col_events


def cmd_expect(args):
    expected, stats, _col_retired, _drop_col_events = derive_expected()
    rows = []
    for name, (mig, table, fn, keycols, _order) in sorted(expected.items(), key=lambda kv: (kv[1][0], kv[0])):
        rows.append("%d\t%s\t%s\t%s\t%s\t" % (mig, name, table, fn, ",".join(keycols)))
    text = "\n".join(rows) + ("\n" if rows else "")
    header = (
        "# db-object-expect.tsv —— ★ 从仓内 startup 迁移反解出的「期望索引集」\n"
        "# ★ 生成器：scripts/audit/db-object-census.py expect\n"
        "# ★ 列：migration_no\tindex_name\ttable\tsource_file\tdeclared_columns\tnote\n"
        "# ★ 口径：建了 ∧ 没被后来的迁移删掉 ∧ 不是中间产物（USING INDEX 消费）\n"
        "# ★ declared_columns = ★ 建索引时的【直接键列】（★ 表达式索引保守取空）\n"
        "# ★   ⇒ ★★ 「列被 DROP ⇒ PG 级联删索引」的判据要用它\n"
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


def _collect_raw_rows(from_stdin: bool, psql_bin=None):
    """★ 采集一个环境的底账【原始行】（★ 不含 env 前缀）⇒ list[str]。

    ★ R164：★ 抽出来是为了让 `collect` 与 `verify` 走【同一条采集代码路径】
    ⇒ ⇒ ★ 「verify 用的采集口径」与「快照用的采集口径」不可能分叉。
    """
    # ★ 自证钩子：★ 让 selftest 能【端到端】验退出码，★ 而不必真的连库。
    # ★ 只在自证里被设置；★ 正常路径永远走真实采集。
    if _LIVE_ROWS_OVERRIDE is not None:
        return list(_LIVE_ROWS_OVERRIDE)
    if from_stdin:
        # ★ 远程环境的采集形态：
        #   ssh 252 "cat | podman exec -i … psql -tA -q" < census.sql
        #     | python3 db-object-census.py collect --env 252 --from-stdin --out FILE --append
        # ★⇒ 远程与本地走【同一条】加前缀/校验代码路径（★ 不手拼产物）。
        raw_rows = sys.stdin.read().splitlines()
    else:
        dsn = os.environ.get("GUARD_DSN")
        if not dsn:
            die("GUARD_DSN is not set (★ DSN 只走环境变量 ⇒ 不进 argv、不回显)")
        psql = os.environ.get("GUARD_PSQL") or psql_bin or "psql"
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
        rows.append(line)
    return rows


def cmd_collect(args):
    # ★ 前缀化放在这里（★ 与历史产物的 sha256 口径逐字一致 ⇒ ★ 旧快照仍可被新代码校验）
    rows = ["%s\t%s" % (args.env, line) for line in _collect_raw_rows(args.from_stdin, args.psql)]

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


# ─────────────────────────── columns ───────────────────────────


def cmd_columns(args):
    """★ 采集一个环境的【列底账】（★ 只读）⇒ 写 4 列 TSV。

    ★★ 为什么单独一个子命令而不是并进 collect：★ 底账是 17 列的【对象】账，
    ★★ 而列底账是 4 列的【属性】账 ⇒ ⇒ 两者的行数量级不同（★ 对象 ~2500/环境，
    ★★ 列 ~4000/环境）⇒ ⇒ 合在一起会让底账被列淹没 ⇒ ⇒ 单列。
    ★★ ★★ 而且它们的【读取方】也不同：★ 底账给缺口台账，★ 列底账给「列级联退役」判据。
    """
    sql_text = COLUMNS_SQL
    if getattr(args, "keys_from", None):
        _exp = parse_expected(args.keys_from)
        _pairs = set()
        for _n, (_mig, _tbl, _cols, _f) in _exp.items():
            for _c in _cols:
                _pairs.add((_tbl, _c))
        sql_text = columns_sql(_pairs)
    # ★★ R164 新增：★ 让【工具自己】产出 scoped SQL ⇒ ⇒ 远程采集用的 SQL 与本地同一份。
    # ★★ 起因（★ 本轮实测踩到）：★ 远程采集的 scoped SQL 是【手搓】的 ⇒ ★ 手搓时把期望集的
    # ★★ `source_file` 列当成了 `declared_columns` 列 ⇒ ⇒ IN 列表全是垃圾 ⇒ ⇒ 252/211 各采到
    # ★★ **0 行**（★ 而本地走工具自己的 `--keys-from` ⇒ 879/891 行 ⇒ ⇒ 两边口径分叉）。
    # ★★ ⇒ ⇒ 纪律：**采集 SQL 必须由工具生成**（★ 与「产物不许手拼」同族）。
    if getattr(args, "emit_sql", None):
        with open(args.emit_sql, "w", encoding="utf-8") as fh:
            fh.write(sql_text)
        sys.stderr.write(
            "columns: emit-sql -> %s (%d bytes; ★ scope=%s)\n"
            % (args.emit_sql, len(sql_text), "expected-index-key-columns"
               if getattr(args, "keys_from", None) else "all-columns")
        )
        return 0
    if not getattr(args, "env", None):
        die("columns needs --env (★ 只有 --emit-sql 模式可以省)")
    if args.from_stdin:
        raw_rows = sys.stdin.read().splitlines()
    else:
        dsn = os.environ.get("GUARD_DSN")
        if not dsn:
            die("GUARD_DSN is not set (★ DSN 只走环境变量 ⇒ 不进 argv、不回显)")
        psql = os.environ.get("GUARD_PSQL") or args.psql or "psql"
        try:
            proc = subprocess.run(
                [psql, dsn, "-tA", "-q", "-v", "ON_ERROR_STOP=1", "-f", "-"],
                input=sql_text,
                capture_output=True,
                text=True,
                env={**os.environ, "GUARD_DSN": dsn},
            )
        except FileNotFoundError:
            die("psql not found: %s (★ override with GUARD_PSQL or --psql)" % psql)
        if proc.returncode != 0:
            err_lines = [l for l in proc.stderr.splitlines() if l.strip()]
            die("psql failed rc=%d (%d stderr lines; ★ DSN not echoed)" % (proc.returncode, len(err_lines)))
        raw_rows = proc.stdout.splitlines()

    rows = []
    for line in raw_rows:
        if not line.strip():
            continue
        parts = line.split("\t")
        if len(parts) != COLUMNS_ARTIFACT_COLUMNS - 1:
            die(
                "columns row has %d fields, expected %d"
                " (★ 采集端必须折行 ⇒ PG17 pretty-print 会塞换行)"
                % (len(parts), COLUMNS_ARTIFACT_COLUMNS - 1)
            )
        rows.append("%s\t%s" % (args.env, line))

    header = (
        "# db-object-columns.tsv —— ★★【每环境每表每列】的事实底账（★ 只读采集）\n"
        "# ★ 生成器：scripts/audit/db-object-census.py columns\n"
        "# ★ 列：env\tschema\ttable\tcolumn\n"
        "# ★ 这层事实此前**不存在** ⇒ ⇒ 「列没了 ⇒ 索引被 PG 级联删掉」在仓内不可见\n"
        "# ★   ⇒ ⇒ 假缺口的第三种来源一直躺着（★ idx_sessions_last_full_at 实测命中）\n"
        "# ★ collected_at=%s\n"
        "# ★ env=%s rows=%d sha256=%s\n"
    ) % (
        datetime.now(timezone.utc).isoformat(timespec="seconds"),
        args.env,
        len(rows),
        hashlib.sha256("\n".join(rows).encode("utf-8")).hexdigest(),
    )
    scope_line = "# ★ scope=%s\n" % (
        "expected-index-key-columns" if getattr(args, "keys_from", None) else "all-columns"
    )
    out = header + scope_line + "\n".join(rows) + ("\n" if rows else "")

    if args.out:
        mode = "a" if args.append else "w"
        with open(args.out, mode, encoding="utf-8") as fh:
            if mode == "a":
                fh.write("\n".join(rows) + ("\n" if rows else ""))
            else:
                fh.write(out)
        sys.stderr.write("columns: env=%s rows=%d -> %s (%s)\n" % (args.env, len(rows), args.out, mode))
    else:
        sys.stdout.write(out)
    return 0


def parse_columns(path):
    """读列底账 ⇒ {env: {table: set(columns)}}"""
    out = {}
    with open(path, encoding="utf-8") as fh:
        for lineno, line in enumerate(fh, 1):
            line = line.rstrip("\n")
            if not line or line.startswith("#"):
                continue
            parts = line.split("\t")
            if len(parts) != COLUMNS_ARTIFACT_COLUMNS:
                die(
                    "columns line %d has %d fields, expected %d"
                    % (lineno, len(parts), COLUMNS_ARTIFACT_COLUMNS)
                )
            out.setdefault(parts[0], {}).setdefault(parts[2], set()).add(parts[3])
    if not out:
        die("columns artifact has no data rows: %s" % path)
    return out


def cmd_columns_bundle(args):
    """★ 合并 N 份单环境列底账 ⇒ 一份多环境列底账（★ 头部带每环境行数与 sha256）。

    ★★ 为什么要单独一步：★ 各环境的采集是【分开跑的】（★ 远程走 ssh 管道）
    ★★ ⇒ ★ 产物必须【工具生成】⇒ ★ 不能手拼（★ 手拼的产物没法证明自己来自工具）。
    """
    rows = []
    per_env = {}
    per_env_collected = {}
    for path in args.inputs:
        env_hint = None
        collected_at = None
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                line = line.rstrip("\n")
                if not line:
                    continue
                if line.startswith("#"):
                    # ★★ R164：★ 透传【采集时刻】（★ 与 census bundle 同款）
                    # ★★ 修：★ 头部 token 前带 "★ " ⇒ ★ 锚在 "#\s*" 上会匹配不到。
                    m = re.search(r"collected_at=(\S+)", line)
                    if m:
                        collected_at = m.group(1)
                    m = re.search(r"env=(\S+)\s+rows=", line)
                    if m:
                        env_hint = m.group(1)
                    continue
                parts = line.split("\t")
                if len(parts) != COLUMNS_ARTIFACT_COLUMNS:
                    die("columns line has %d fields, expected %d" % (len(parts), COLUMNS_ARTIFACT_COLUMNS))
                rows.append(line)
                per_env[parts[0]] = per_env.get(parts[0], 0) + 1
                if collected_at and parts[0] not in per_env_collected:
                    per_env_collected[parts[0]] = collected_at
    if not rows:
        die("columns bundle has no data rows")
    env_desc = []
    for env in sorted(per_env):
        env_rows = [r for r in rows if r.split("\t")[0] == env]
        env_desc.append(
            "%s rows=%d sha256=%s collected_at=%s"
            % (
                env,
                len(env_rows),
                hashlib.sha256("\n".join(env_rows).encode("utf-8")).hexdigest(),
                per_env_collected.get(env) or "unknown",
            )
        )
    header = (
        "# db-object-columns.tsv —— ★★【每环境每表每列】的事实底账（★ 只读采集）\n"
        "# ★ 生成器：scripts/audit/db-object-census.py columns-bundle\n"
        "# ★ 列：env\tschema\ttable\tcolumn\n"
        "# ★ 这层事实此前**不存在** ⇒ ⇒ 「列没了 ⇒ 索引被 PG 级联删掉」在仓内不可见\n"
        "# ★   ⇒ ⇒ 假缺口的第三种来源一直躺着（★ idx_sessions_last_full_at 实测命中）\n"
        "# ★ scope=expected-index-key-columns\n"
        "# ★ bundled_at=%s\n"
        "# ★ %s\n"
    ) % (
        datetime.now(timezone.utc).isoformat(timespec="seconds"),
        " | ".join(env_desc),
    )
    out = header + "\n".join(rows) + "\n"
    if args.out:
        with open(args.out, "w", encoding="utf-8") as fh:
            fh.write(out)
        sys.stderr.write("columns-bundle: %d rows -> %s\n" % (len(rows), args.out))
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
    """读期望集 ⇒ {index_name: (migration_no, table, declared_columns)}"""
    out = {}
    with open(path, encoding="utf-8") as fh:
        for lineno, line in enumerate(fh, 1):
            line = line.rstrip("\n")
            if not line or line.startswith("#"):
                continue
            parts = line.split("\t")
            if len(parts) < 5:
                die("expect line %d malformed (%d fields; ★ 期望集是 6 列)" % (lineno, len(parts)))
            cols = [c for c in parts[4].split(",") if c]
            out[parts[1]] = (parts[0], parts[2], cols, parts[3])
    if not out:
        die("expect file has no data rows: %s" % path)
    return out


def classify(env_state, index_name, table, keycols=None, colstate=None):
    """★ 五状态判定（★ 每格都可机器判 ⇒ 没有「不知道」）。

    ★★ R163 加了第 5 态 `retired-by-column-drop`：★ 索引的【直接键列】在本环境
    ★★ 已经不存在 ⇒ ⇒ PG 在 DROP COLUMN 时会【自动删掉】依赖它的索引
    ★★ ⇒ ⇒ 它【不是】缺口 ⇒ ⇒ **这是假缺口的第三种来源**
    ★★ （★ 前两种：★ R161 的「DROP 名带 schema 前缀」/ ★ R162 的「解析口径不一致」）
    ★★ ⇒ ⇒ 实测命中：★ idx_sessions_last_full_at（★ 迁移 708 删了 last_full_payload_at）
    ★★ ⇒ ⇒ 三个环境都判「缺口」⇒ ⇒★ 而 211 上那列还在（★ 456 跑在 708 之后）⇒ ⇒ ★★ 顺序倒置
    """
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
    # ★★ R163：★【列级联退役】⇒ ★ 键列没了 ⇒ 索引被 PG 自动带走 ⇒ ⇒ 不是缺口
    if keycols and colstate is not None:
        have = colstate.get(table)
        if have is not None:
            if [c for c in keycols if c not in have]:
                return "retired-by-column-drop"
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
        prod-full-gap     —— 生产环境【全都】缺（★ 252 与 211 都缺 ⇒ 影响面最大）
        prod-partial-gap  —— 生产环境里【缺一部分】（★ 252 或 211 其一缺）
        local-only-gap    —— 生产齐备，★ 只缺 local（★ 开发/验证环境）
        fresh-only-gap    —— 生产齐备，★ 只缺 fresh（★ 新装环境）
        new-install-gap   —— ★★★ 生产齐备，★ 而 local 与 fresh【都缺】
                              ⇒ ★★★ **⇒ 任何按安装器路径装出来的新环境【必然】缺**
                              ⇒ ★★★ ⇒ 这正是「多环境同一标准」在新装侧的真实缺口

    ★★ R163 轮追加：★ 加入 `fresh`（★ 新装环境）这个环境后，★ **「非 local 即生产」的口径就坏了**
    ★★ （★ 例：local 缺 + fresh 缺 + 252/211 齐备 ⇒ ★★ 旧口径会判成 prod-partial-gap
    ★★ ⇒ ★★ 把「生产齐备」说成「生产有缺口」）⇒ ★★ ⇒ 改成【按环境角色】判定。

    ⇒ ★★ **给定三个格子状态，severity 唯一确定** ⇒ ⇒ 门可以机械校验
    ⇒ ⇒ ★★ **不会出现「同一个词对应两种意思」** ⇒ ★★ 这才是可机器判的前提。
    """
    missing = {e for e, c in zip(envs, cells) if c == "missing"}
    if not missing:
        return "none"
    prod_missing = missing & set(prod_envs)
    # ★★ 先判生产侧（★ 生产侧优先：★ 它决定「用户可见的劣化」）
    if len(prod_missing) == len(prod_envs):
        return "prod-full-gap"
    if prod_missing:
        return "prod-partial-gap"
    # ★★ 生产侧齐备 ⇒ 只剩 local / fresh 缺 ⇒ ★★ 按【缺的是哪一个】分三档
    if missing == {"local"}:
        return "local-only-gap"
    if missing == {"fresh"}:
        return "fresh-only-gap"
    if "fresh" in missing:
        # ★★ local 与 fresh 都缺、★ 而生产齐备 ⇒ ★★★ **新装环境【必然】缺**
        return "new-install-gap"
    return "local-only-gap"


# ★★ R163 轮加入 `fresh`：★ 「新装环境」= 用【安装器真实路径】（00/01/02 + 312 条 StartupFiles）
# ★★ 在空库装出来的库 ⇒ ★★ 它是「多环境同一标准」的【新装侧】实测基准。
# ★★ 列序固定（★ 不依赖字典序 ⇒ ★★ 逐字对拍可用）。
ENV_ORDER = ["local", "fresh", "252", "211"]


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
    # ★★ R163：★ 列底账（★ 没有它就判不出「列级联退役」⇒ ⇒ 假缺口继续躺着）
    columns = parse_columns(getattr(args, "columns", None)) if getattr(args, "columns", None) else {}
    envs = sorted(census.keys(), key=_env_sort_key)

    rows = []
    prod_envs = [e for e in envs if e in ("252", "211")]
    gaps_by_env = {e: 0 for e in envs}
    prod_gaps = 0

    for name in sorted(expected, key=lambda n: (int(expected[n][0]) if expected[n][0].isdigit() else -1, n)):
        mig, table, keycols, _cfile = expected[name]
        cells = [
            classify(census[e], name, table, keycols, columns.get(e))
            for e in envs
        ]
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
        "# ★ 状态词：present | present-via-child | missing | retired | retired-by-column-drop\n"
        "# ★   ★★ retired-by-column-drop = ★ 索引的键列在本环境已被 DROP COLUMN 删掉\n"
        "# ★   ★★ ⇒ PG 自动删掉了那个索引 ⇒ ⇒ 它【不是】缺口（★ 假缺口的第三种来源）\n"
        "# ★ verdict：complete | gap　　severity：none | prod-full-gap | prod-partial-gap | local-only-gap | fresh-only-gap | new-install-gap\n"
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


# ─────────────────────────── reachability ───────────────────────────


def read_manifest(path):
    """读安装器登记清单 ⇒ {文件名: 0-based apply-order}"""
    out = {}
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.rstrip("\n")
            if not line or line.startswith("#"):
                continue
            parts = line.split("\t")
            if len(parts) < 2:
                continue
            try:
                order = int(parts[0])
            except ValueError:
                continue
            out[parts[1].strip()] = order
    return out


def read_package_up(dirpath):
    """读安装器分发包里的 up 迁移文件名集合（★ .down.sql 不算）"""
    out = set()
    if not os.path.isdir(dirpath):
        die("installer package dir not found: %s" % dirpath)
    for fn in os.listdir(dirpath):
        if fn.endswith(".sql") and not fn.endswith(".down.sql"):
            out.add(fn)
    return out


def cmd_reachability(args):
    """★★【安装器可达性台账】：★ 每条期望索引 ⇒ 它的新装可达性 + 四环境在位情况。

    ★★ 这张台账回答的是原始目标第 7 条（「多个环境同样的标准」）：
    ★★ **一条索引，新装环境为什么会有它？**
    ★★   reachable        ⇒ 创建它的迁移在【安装器登记清单 ∩ 分发包】里 ⇒ 新装会跑
    ★★   baseline-dependent ⇒ 创建它的迁移【不在】安装器清单里
    ★★                        ⇒ 新装能拥有它**只因为 01-schema.sql 是一份 dump**
    ★★   unreachable-and-missing ⇒ 创建它的迁移不可达、基线也没有 ⇒ ★★ 任何新装环境【必然缺】
    ★★   reachable-but-missing-in-fresh ⇒ 创建它的迁移【可达】、新装却没有 ⇒ ★★ 安装器自己有 bug
    """
    expected = parse_expected(args.expect)
    census = parse_census(args.census)
    columns = parse_columns(getattr(args, "columns", None)) if getattr(args, "columns", None) else {}
    manifest = read_manifest(args.manifest)
    package = read_package_up(args.package)
    envs = sorted(census.keys(), key=_env_sort_key)
    fresh = args.fresh_env
    prod_envs = [e for e in envs if e in ("252", "211")]

    rows = []
    counts = {}
    for name in sorted(expected, key=lambda n: (int(expected[n][0]) if expected[n][0].isdigit() else -1, n)):
        mig, table, keycols, cfile = expected[name]
        creator = os.path.basename(cfile)
        reachable = (creator in manifest) and (creator in package)
        cells = {e: classify(census[e], name, table, keycols, columns.get(e)) for e in envs}
        present_envs = [e for e in envs if cells[e].startswith("present")]
        missing_envs = [e for e in envs if cells[e] == "missing"]
        fresh_present = cells.get(fresh, "missing").startswith("present")
        fresh_cell = cells.get(fresh, "missing")
        if fresh_present:
            verdict = "reachable-and-present" if reachable else "baseline-dependent"
            severity = "none" if reachable else "baseline-dependent"
        elif fresh_cell in ("retired", "retired-by-column-drop"):
            # ★★★ 表/列都没了 ⇒ 索引【合法】退役 ⇒ ⇒ 不是缺口 ⇒ ★★ 也不能算「安装器 bug」
            verdict = "reachable-and-legitimately-retired"
            severity = "none"
        elif reachable:
            verdict = "reachable-but-missing-in-fresh"
            severity = "installer-bug"
        else:
            verdict = "unreachable-and-missing"
            prod_missing = [e for e in prod_envs if cells[e] == "missing"]
            severity = "prod-gap" if prod_missing else "new-install-gap"
        counts[verdict] = counts.get(verdict, 0) + 1
        rows.append(
            "\t".join(
                [
                    name,
                    table,
                    mig,
                    creator,
                    "yes" if reachable else "no",
                    ",".join(present_envs),
                    ",".join(missing_envs),
                    verdict,
                    severity,
                    "",
                ]
            )
        )

    header = (
        "# installer-reachability.tsv —— ★★「新装环境可达性」台账（★ 每条期望索引一行）\n"
        "# ★ 生成器：scripts/audit/db-object-census.py reachability\n"
        "# ★ 列：index_name\ttable\tcreator_migration_no\tcreator_file\tcreator_reachable\t"
        "present_envs\tmissing_envs\tverdict\tseverity\tnote\n"
        "# ★ verdict：reachable-and-present | reachable-and-legitimately-retired | "
        "baseline-dependent | unreachable-and-missing | reachable-but-missing-in-fresh\n"
        "# ★ severity：none | baseline-dependent | new-install-gap | prod-gap | installer-bug\n"
        "# ★ ★ baseline-dependent = ★ 创建迁移【不在安装器清单】⇒ 新装能拥有它只因为基线是 dump\n"
        "# ★ ★ new-install-gap    = ★ 创建迁移不可达 ⇒ ★★ 任何新装环境【必然缺】\n"
        "# ★ ★ installer-bug      = ★ 创建迁移【可达】、新装却没有 ⇒ ★★ 安装器自己有 bug\n"
        "# ★ generated_at=%s\n"
        "# ★ envs=%s fresh_env=%s manifest=%d package_up=%d rows=%d\n"
        "# ★ counts=%s\n"
    ) % (
        datetime.now(timezone.utc).isoformat(timespec="seconds"),
        ",".join(envs),
        fresh,
        len(manifest),
        len(package),
        len(rows),
        json.dumps(counts, ensure_ascii=False, sort_keys=True),
    )
    out = header + "\n".join(rows) + ("\n" if rows else "")

    if args.out:
        with open(args.out, "w", encoding="utf-8") as fh:
            fh.write(out)
        sys.stderr.write("reachability: %d rows -> %s\n" % (len(rows), args.out))
    if args.json:
        sys.stderr.write(json.dumps({"rows": len(rows), "counts": counts}, ensure_ascii=False, sort_keys=True) + "\n")
    else:
        sys.stderr.write(json.dumps(counts, ensure_ascii=False, sort_keys=True) + "\n")
    return 0


# ─────────────────────────── verify（R164）───────────────────────────


def parse_census_header(path):
    """读底账头部 ⇒ {'meta': {...}, 'envs': {env: {'rows': n, 'sha256': h, 'collected_at': ts}}}"""
    meta = {}
    envs = {}
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.rstrip("\n")
            if not line.startswith("#"):
                break
            m = re.search(r"#\s*★\s*env=(\S+)\s+rows=(\d+)\s+sha256=([0-9a-f]+)(?:\s+collected_at=(\S+))?", line)
            if m:
                envs[m.group(1)] = {
                    "rows": int(m.group(2)),
                    "sha256": m.group(3),
                    "collected_at": m.group(4),
                }
                continue
            m = re.search(r"#\s*★\s*collected_at=(\S+)", line)
            if m:
                meta["collected_at"] = m.group(1)
                continue
            m = re.search(r"#\s*★\s*bundled_at=(\S+)", line)
            if m:
                meta["bundled_at"] = m.group(1)
                continue
            m = re.search(r"#\s*★\s*total_rows=(\d+)\s+envs=(\S+)", line)
            if m:
                meta["total_rows"] = int(m.group(1))
                meta["envs"] = m.group(2).split(",")
                continue
    return meta, envs


def parse_census_env_rows(path, env):
    """读底账里某个环境的行（★ ★ 保留 env 前缀 ⇒ ★ 与 header 的 sha256 口径逐字一致）。"""
    out = []
    with open(path, encoding="utf-8") as fh:
        for lineno, line in enumerate(fh, 1):
            line = line.rstrip("\n")
            if not line or line.startswith("#"):
                continue
            parts = line.split("\t")
            if len(parts) != CENSUS_COLUMNS:
                die("census line %d has %d fields, expected %d" % (lineno, len(parts), CENSUS_COLUMNS))
            if parts[0] == env:
                out.append(line)
    return out


def _row_identity(parts):
    """★ 对象的身份 ⇒ (kind, schema, name)（★ relkind 属【形态】不属【身份】）。

    ★★ `parts` 必须是【17 列（带 env 前缀）】的形态 ⇒ ★ 与 `CENSUS_COLUMN_NAMES` 对齐。
    """
    return (parts[1], parts[2], parts[4])


def _row_payload(parts):
    """★ 对象的【结构载荷】⇒ tuple（★ 剔除 volatile 列 ⇒ ★ 只剩形态）。"""
    volatile_idx = tuple(CENSUS_COLUMN_NAMES.index(c) for c in VOLATILE_CENSUS_COLUMNS)
    return tuple(v for i, v in enumerate(parts) if i not in volatile_idx)


def _row_metrics(parts):
    """★ 对象的【指标】（★ 每次 autovacuum/analyze 都会动 ⇒ 不算漂移）。"""
    return tuple(parts[CENSUS_COLUMN_NAMES.index(c)] for c in VOLATILE_CENSUS_COLUMNS)


def diff_env(snap_rows, live_rows):
    """★ 对拍两个行集 ⇒ (missing, added, changed, metric_only)。

    ★ missing ⇒ ★ 【快照说该有、此刻没有】⇒ ★ 危险方向（★ 它会把人引向错误的补建动作）
    ★ added   ⇒ ★ 【此刻有、快照没有】⇒ ★ 快照落后了（★ 良性方向）
    ★ changed ⇒ ★ 同一对象的【形态】变了（★ 重建过 / 改过列 / 改过谓词）
    ★ metric_only ⇒ ★ 只有大小在动（★ ★ 不是漂移 ⇒ ★ 但必须报数，不能静默）
    """
    snap = {}
    for r in snap_rows:
        p = r.split("\t")
        snap[_row_identity(p)] = p
    live = {}
    for r in live_rows:
        p = r.split("\t")
        live[_row_identity(p)] = p

    missing = sorted(set(snap) - set(live))
    added = sorted(set(live) - set(snap))
    changed = []
    metric_only = []
    for k in sorted(set(snap) & set(live)):
        sp, lp = snap[k], live[k]
        if _row_payload(sp) != _row_payload(lp):
            changed.append(k)
        elif _row_metrics(sp) != _row_metrics(lp):
            metric_only.append(k)
    return missing, added, changed, metric_only


def cmd_verify(args):
    """★ R164：★ 把「快照」与「此刻的真实库」对拍 ⇒ 报【结构漂移】。

    ★★ 退出码（★★ 不许静默降级：★ 「没法判定」绝不能长成「没漂移」）：
        0  无结构漂移（★ 指标漂移不算）
        1  ★★ 有结构漂移（★ missing / added / changed）
        2  ★★ 无法判定（★ 缺 DSN / 采集失败 / 快照里没有该环境 / 参数错误）
    """
    meta, hdr_envs = parse_census_header(args.census)
    if args.env not in hdr_envs:
        sys.stderr.write(
            "ERROR: census %s has no env=%s (★ envs=%s)\n"
            % (args.census, args.env, ",".join(sorted(hdr_envs)))
        )
        sys.exit(2)
    snap_rows = parse_census_env_rows(args.census, args.env)
    if not snap_rows:
        sys.stderr.write("ERROR: census %s has no rows for env=%s\n" % (args.census, args.env))
        sys.exit(2)

    try:
        raw_rows = _collect_raw_rows(args.from_stdin, args.psql)
    except SystemExit as exc:
        # ★ 「采集失败」在 verify 语境下是【无法判定】⇒ ★ 必须是 2，不是 0，也别混成 3。
        sys.exit(2 if exc.code not in (0, None) else exc.code)
    # ★ 前缀化 ⇒ ★ 与快照的行【同形】（★ 17 列 ⇒ 索引口径一致）
    live_rows = ["%s\t%s" % (args.env, r) for r in raw_rows]

    missing, added, changed, metric_only = diff_env(snap_rows, live_rows)

    snap_sha = hashlib.sha256("\n".join(snap_rows).encode("utf-8")).hexdigest()
    live_sha = hashlib.sha256("\n".join(live_rows).encode("utf-8")).hexdigest()
    hdr_sha = hdr_envs[args.env]["sha256"]

    summary = {
        "env": args.env,
        "snapshot_rows": len(snap_rows),
        "live_rows": len(live_rows),
        "snapshot_sha256": snap_sha,
        "header_sha256": hdr_sha,
        "live_sha256": live_sha,
        "header_sha_matches_rows": hdr_sha == snap_sha,
        "collected_at": hdr_envs[args.env].get("collected_at"),
        "missing_in_live": len(missing),
        "added_in_live": len(added),
        "changed": len(changed),
        "metric_only": len(metric_only),
    }
    if args.json:
        sys.stderr.write(json.dumps(summary, ensure_ascii=False, sort_keys=True) + "\n")
    else:
        sys.stderr.write(
            "verify: env=%s snapshot_rows=%d live_rows=%d\n"
            % (args.env, len(snap_rows), len(live_rows))
        )
        sys.stderr.write(
            "  structural drift: missing_in_live=%d added_in_live=%d changed=%d\n"
            % (len(missing), len(added), len(changed))
        )
        sys.stderr.write(
            "  metric-only movement (★ NOT drift): %d rows\n" % len(metric_only)
        )
        if hdr_sha != snap_sha:
            sys.stderr.write(
                "  ★ header sha256 != rows sha256 (★ 产物自身不一致 ⇒ 先修产物)\n"
            )
        for k in missing[:20]:
            sys.stderr.write("  MISSING-IN-LIVE  %s/%s %s\n" % (k[0], k[1], k[2]))
        for k in added[:20]:
            sys.stderr.write("  ADDED-IN-LIVE    %s/%s %s\n" % (k[0], k[1], k[2]))
        for k in changed[:20]:
            sys.stderr.write("  CHANGED          %s/%s %s\n" % (k[0], k[1], k[2]))

    # ★ 产物自身 sha256 对不上 ⇒ ★ 无从判定 ⇒ ★ 也是「无法判定」⇒ ★ 不是 0。
    if not summary["header_sha_matches_rows"]:
        sys.exit(2)
    if missing or added or changed:
        sys.stderr.write("RESULT: STRUCTURAL DRIFT\n")
        sys.exit(1)
    sys.stderr.write("RESULT: NO STRUCTURAL DRIFT\n")
    return 0


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
    _expected, _stats, col_retired, _drop_col_events = derive_expected()
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
                        "drop-index",
                        "",
                    ]
                )
            )
    # ★★ R163：★【列级联退役】——★ 迁移删了索引的键列 ⇒ PG 自动删掉那个索引
    # ★★ ⇒ ⇒ 它不是「缺口」⇒ ⇒ 但【退役证据】必须可查 ⇒ ⇒ 记进同一张台账
    for name in sorted(col_retired):
        mig, table, fn, retire_file, col, _keycols, retire_line = col_retired[name]
        rows.append(
            "\t".join(
                [
                    str(mig),
                    name,
                    table,
                    fn,
                    "-1",
                    retire_file,
                    "no",
                    str(retire_line),
                    "drop-column",
                    col,
                ]
            )
        )
    text = "\n".join(rows) + ("\n" if rows else "")
    header = (
        "# db-object-retired.tsv —— ★【建了又被后来的迁移删掉】的索引（★ 退役证据台账）\n"
        "# ★ 生成器：scripts/audit/db-object-census.py retired\n"
        "# ★ 列：create_migration_no\tindex_name\ttable\tcreate_file\t"
        "retire_migration_no\tretire_file\tconditional\tretire_line\t"
        "retire_kind\tretired_column\n"
        "# ★ retire_kind：drop-index（★ 显式 DROP INDEX）| drop-column（★ ★ 删列 ⇒ PG 级联删索引）\n"
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
    per_env_collected = {}
    order = []
    for path in args.inputs:
        env = None
        collected_at = None
        rows = []
        with open(path, encoding="utf-8") as fh:
            for lineno, line in enumerate(fh, 1):
                line = line.rstrip("\n")
                if not line:
                    continue
                if line.startswith("#"):
                    # ★ 单环境文件的头部里带 "env=<label>"
                    # ★★ R164 修：★ 头部里这些 token 前面还带着 "★ "（★ 比如
                    # ★★ "# ★ env=local rows=…"）⇒ ⇒ ★ 用 "#\s*env=" 这种锚法会【匹配不到】
                    # ★★ ⇒ ★★ 实测 caught：★ collected_at 一律透传成 "unknown" ⇒ ★ 恰好把
                    # ★★ 本轮要闭合的那条边界（★ 「采集时刻查不到」）又关回去了 ⇒ ⇒ 改用无锚 token 匹配。
                    m = re.search(r"env=(\S+)\s+rows=", line)
                    if m:
                        env = m.group(1)
                    # ★★ R164：★ 把【采集时刻】透传下去（★ 否则 bundle 之后
                    # ★★ 「这个环境的数据是几号采的」在产物里就查不到了 ⇒ ⇒
                    # ★★ 「快照过期」不可机器判 ⇒ ★★ 这正是本轮要闭合的那条边界）。
                    m = re.search(r"collected_at=(\S+)", line)
                    if m:
                        collected_at = m.group(1)
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
        per_env_collected[env] = collected_at
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
            "# ★ env=%s rows=%d sha256=%s collected_at=%s"
            % (
                env,
                len(rows),
                hashlib.sha256("\n".join(rows).encode("utf-8")).hexdigest(),
                per_env_collected.get(env) or "unknown",
            )
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
    expected, stats, col_retired, drop_col_events = derive_expected()
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

    # ★★ R163 新增牙：★★【列级联退役】的识别 + 零回归钉死
    check(
        "expect/column-drop-events-found",
        len(drop_col_events) > 0,
        "★ ALTER TABLE … DROP COLUMN 一个都没解析到 ⇒ 假缺口的第三种来源看不见",
    )
    check(
        "expect/column-retired-detected",
        "idx_sessions_last_full_at" in col_retired,
        "★ 迁移 708 删了 last_full_payload_at ⇒ PG 会级联删掉 idx_sessions_last_full_at",
    )
    _cr = col_retired.get("idx_sessions_last_full_at")
    check(
        "expect/column-retired-names-the-column",
        bool(_cr) and _cr[4] == "last_full_payload_at" and "708_session_bodies_s1a.sql" in _cr[3],
        "★ 列级联退役的证据必须点名【哪一列】+【哪条迁移】",
    )
    # ★★ 零回归钉死：★ 括号配对扫描与老正则的【索引名集合】必须逐字相同
    _old_names = set()
    _new_names = set()
    for _fn in sorted(
        f for f in os.listdir(MIGRATIONS_DIR)
        if f.endswith(".sql") and not f.endswith(".down.sql")
    ):
        _body = RE_LINE_COMMENT.sub(
            "", open(os.path.join(MIGRATIONS_DIR, _fn), encoding="utf-8", errors="replace").read()
        )
        for _m in RE_CREATE_INDEX.finditer(_body):
            _old_names.add(_strip_name(_m.group(1)))
        for (_sn, _st, _sc) in _scan_create_indexes(_body):
            _new_names.add(_sn)
    check(
        "expect/balanced-scan-zero-regression",
        _old_names == _new_names,
        "★ 括号配对扫描改变了索引名集合（★ %d vs %d）⇒ 期望集会漂"
        % (len(_old_names), len(_new_names)),
    )
    check(
        "expect/declared-columns-parsed",
        "last_full_payload_at" in expected.get("idx_sessions_last_full_at", ("", "", "", [], ""))[3],
        "★ 声明列没解析出来 ⇒ 列级联退役判据没有输入",
    )
    check(
        "expect/using-method-indexes-kept",
        "idx_assets_tags" in _old_names and "idx_assets_tags" in _new_names,
        "★ `ON t USING gin (…)` 形态的索引被配对扫描漏掉 ⇒ 期望集缩了 45 条",
    )
    # ★★ 钉死 `ON ONLY <table>`：★ 表名被吃成 ONLY ⇒ ⇒ 17 条索引被判 retired ⇒ ★★ 掩盖真缺口
    _only_form = [
        n for n, v in expected.items() if v[1] == "only"
    ]
    check(
        "expect/no-only-as-table-name",
        not _only_form,
        "★ %d 条索引的表名被解析成 ONLY ⇒ 它们在台账里会被判 retired ⇒ 掩盖真缺口：%s"
        % (len(_only_form), _only_form[:5]),
    )
    check(
        "expect/only-form-table-resolved",
        expected.get("idx_routing_decision_log_canonical_ts", ("", "", "", [], ""))[1]
        == "routing_decision_log",
        "★ `ON ONLY routing_decision_log` 的表名没解析对",
    )
    # ★★ 钉死逗号列表形态的 DROP COLUMN（★ 708 就是三条同语句）
    check(
        "expect/drop-column-comma-list-parsed",
        any(
            tbl == "sessions" and col == "last_full_payload_at"
            for (_o, _f, tbl, col, _l) in drop_col_events
        ),
        "★ 逗号列表里的第二三条 DROP COLUMN 没解析到 ⇒ 708 的删列漏了",
    )

    # ② 状态判定五态（★ 阳性对照 + 反向对照）
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
    # ★★ R163：★【列级联退役】——★ 键列没了 ⇒ 索引被 PG 自动带走 ⇒ ⇒ 不是缺口
    env_state3 = {
        "idx": set(),
        "children": {},
        "rels": {"sessions": "p"},
    }
    _colstate = {"sessions": {"tenant_id", "session_id"}}
    check(
        "classify/retired-by-column-drop",
        classify(env_state3, "idx_x", "sessions", ["tenant_id", "last_full_payload_at"], _colstate)
        == "retired-by-column-drop",
        "★ 键列不在了却判成 missing ⇒ 假缺口继续躺着",
    )
    check(
        "classify/column-present-still-missing",
        classify(env_state3, "idx_x", "sessions", ["tenant_id"], _colstate) == "missing",
        "★ 键列都在却判成非缺口 ⇒ 真缺口被吞了",
    )
    check(
        "classify/no-column-facts-falls-back-to-missing",
        classify(env_state3, "idx_x", "sessions", ["tenant_id"], None) == "missing",
        "★ 没有列事实时必须保守判 missing（★ 不能瞎猜退役）",
    )

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

    # ★★ R164 新增牙（★★ 两个要点：★ ① 「结构漂移」与「指标漂移」必须分得开
    # ★★ ② 退出码语义必须分得开：★ 「没法判定」绝不能长成「没漂移」）
    check(
        "verify/volatile-columns-are-exactly-three",
        VOLATILE_CENSUS_COLUMNS == ("reltuples", "table_size", "total_size"),
        repr(VOLATILE_CENSUS_COLUMNS),
    )

    def _r(*vals):
        """★ 17 列（带 env 前缀）的行（★ 与底账产物的行【同形】）。"""
        return _row(CENSUS_COLUMNS, "local", *vals)

    def _raw(*vals):
        """★ 16 列（不带 env）的行（★ 与 `_collect_raw_rows` 的返回【同形】）。"""
        return _row(CENSUS_COLUMNS - 1, *vals)

    base = _r("idx", "public", "i", "idx_a", "owner", "f", "", "", "100", "4096", "8192",
              "CREATE INDEX ...", "true,true,true", "", "tenant_id,session_id", "")
    moved = _r("idx", "public", "i", "idx_a", "owner", "f", "", "", "200", "8192", "16384",
               "CREATE INDEX ...", "true,true,true", "", "tenant_id,session_id", "")
    redefined = _r("idx", "public", "i", "idx_a", "owner", "f", "", "", "100", "4096", "8192",
                   "CREATE INDEX ON public.t (a)", "true,true,true", "", "tenant_id,session_id", "")

    mi, ad, ch, mo = diff_env([base], [base])
    check("verify/identical-is-no-drift", not mi and not ad and not ch and not mo, repr((mi, ad, ch, mo)))
    mi, ad, ch, mo = diff_env([base], [moved])
    check(
        "verify/metric-only-is-not-drift",
        not mi and not ad and not ch and len(mo) == 1,
        "★ reltuples/size 变了 ⇒ 不该算漂移: %r" % ((mi, ad, ch, mo),),
    )
    mi, ad, ch, mo = diff_env([base], [])
    check("verify/detects-missing-in-live", len(mi) == 1 and not ad, repr((mi, ad)))
    mi, ad, ch, mo = diff_env([], [base])
    check("verify/detects-added-in-live", len(ad) == 1 and not mi, repr((mi, ad)))
    mi, ad, ch, mo = diff_env([base], [redefined])
    check("verify/detects-changed-shape", len(ch) == 1 and not mo, repr((ch, mo)))

    # ★★ 端到端退出码（★ 用自证钩子喂现场行 ⇒ 不连库）
    def _write_census(path, env, rows):
        pref = list(rows)
        sha = hashlib.sha256("\n".join(pref).encode("utf-8")).hexdigest()
        with open(path, "w", encoding="utf-8") as fh:
            fh.write("# db-object-census.tsv —— selftest\n")
            fh.write("# ★ bundled_at=2026-10-27T00:00:00+00:00\n")
            fh.write("# ★ env=%s rows=%d sha256=%s collected_at=2026-10-27T00:00:00+00:00\n"
                     % (env, len(pref), sha))
            fh.write("# ★ total_rows=%d envs=%s\n" % (len(pref), env))
            for r in pref:
                fh.write(r + "\n")

    global _LIVE_ROWS_OVERRIDE
    with tempfile.NamedTemporaryFile("w", suffix=".tsv", delete=False, encoding="utf-8") as fh:
        cen2 = fh.name
    _write_census(cen2, "local", [base])
    vns = argparse.Namespace(env="local", census=cen2, psql=None, from_stdin=False, json=False)

    raw_base = _raw("idx", "public", "i", "idx_a", "owner", "f", "", "", "100", "4096", "8192",
                    "CREATE INDEX ...", "true,true,true", "", "tenant_id,session_id", "")
    raw_moved = _raw("idx", "public", "i", "idx_a", "owner", "f", "", "", "200", "8192", "16384",
                     "CREATE INDEX ...", "true,true,true", "", "tenant_id,session_id", "")
    raw_redefined = _raw("idx", "public", "i", "idx_a", "owner", "f", "", "", "100", "4096", "8192",
                         "CREATE INDEX ON public.t (a)", "true,true,true", "", "tenant_id,session_id", "")

    _LIVE_ROWS_OVERRIDE = [raw_base]
    try:
        cmd_verify(vns)
        rc = 0
    except SystemExit as exc:
        rc = exc.code
    check("verify/exit-0-on-no-drift", rc == 0, "rc=%r" % (rc,))

    _LIVE_ROWS_OVERRIDE = [raw_moved]
    try:
        cmd_verify(vns)
        rc = 0
    except SystemExit as exc:
        rc = exc.code
    check("verify/exit-0-on-metric-only", rc == 0, "★ 只有统计量在动 ⇒ 不该红: rc=%r" % (rc,))

    _LIVE_ROWS_OVERRIDE = [raw_redefined]
    try:
        cmd_verify(vns)
        rc = 0
    except SystemExit as exc:
        rc = exc.code
    check("verify/exit-1-on-shape-drift", rc == 1, "rc=%r" % (rc,))

    _LIVE_ROWS_OVERRIDE = []
    try:
        cmd_verify(vns)
        rc = 0
    except SystemExit as exc:
        rc = exc.code
    check("verify/exit-1-on-missing-object", rc == 1, "rc=%r" % (rc,))

    # ★★ 「快照里没有这个环境」⇒ ★ 无法判定 ⇒ ★ 必须是 2（★ 绝不是 0）
    vns_absent = argparse.Namespace(env="211", census=cen2, psql=None, from_stdin=False, json=False)
    _LIVE_ROWS_OVERRIDE = [raw_base]
    try:
        cmd_verify(vns_absent)
        rc = 0
    except SystemExit as exc:
        rc = exc.code
    check("verify/exit-2-when-env-absent", rc == 2, "★ 「没法判定」≠「没漂移」: rc=%r" % (rc,))

    # ★★ 「连不上库」⇒ ★ 同样是无法判定 ⇒ ★ 必须是 2
    _LIVE_ROWS_OVERRIDE = None
    saved_dsn = os.environ.pop("GUARD_DSN", None)
    try:
        cmd_verify(vns)
        rc = 0
    except SystemExit as exc:
        rc = exc.code
    finally:
        if saved_dsn is not None:
            os.environ["GUARD_DSN"] = saved_dsn
    check("verify/exit-2-when-db-unreachable", rc == 2, "★ 「连不上」≠「没漂移」: rc=%r" % (rc,))

    os.unlink(cen2)

    # ★★ R164 第二组牙：★ bundle 的 collected_at 透传（★ 工具侧行为 ⇒ ★ 由本工具自证）
    # ★★ 起因（★ 本轮实测）：★ 第一版透传【没生效】⇒ ★ 正则被头部里的 "★ " 挡住
    # ★★ ⇒ ⇒ 头部出来全是 `collected_at=unknown` ⇒ ⇒ ★ 而「unknown」长得像一个合法值
    # ★★ ⇒ ⇒ ⇒ 「透传失败」被伪装成「采集时刻未知」⇒ ⇒ ⇒ ⇒ 所以这里要验两件事：
    # ★★   ① 透传【真的发生】；★ ★★ ② 兜底值必须是【显式的 unknown】（★ 不许长得像合法值）。
    def _write_single_env(path, env, rows, collected_at):
        pref = ["%s\t%s" % (env, r) for r in rows]
        sha = hashlib.sha256("\n".join(pref).encode("utf-8")).hexdigest()
        with open(path, "w", encoding="utf-8") as fh:
            fh.write("# db-object-census.tsv —— selftest\n")
            if collected_at is not None:
                fh.write("# ★ collected_at=%s\n" % collected_at)
            fh.write("# ★ env=%s rows=%d sha256=%s\n" % (env, len(pref), sha))
            for r in pref:
                fh.write(r + "\n")

    with tempfile.NamedTemporaryFile("w", suffix=".tsv", delete=False, encoding="utf-8") as fh:
        env_a = fh.name
    with tempfile.NamedTemporaryFile("w", suffix=".tsv", delete=False, encoding="utf-8") as fh:
        env_b = fh.name
    with tempfile.NamedTemporaryFile("w", suffix=".tsv", delete=False, encoding="utf-8") as fh:
        env_c = fh.name
    _write_single_env(env_a, "local", [raw_base], "2026-10-27T01:02:03+00:00")
    _write_single_env(env_b, "252", [raw_base], "2026-10-27T04:05:06+00:00")
    # ★ 第三个文件【故意没有 collected_at】⇒ ⇒ 兜底必须是 unknown
    _write_single_env(env_c, "211", [raw_base], None)

    bns = argparse.Namespace(inputs=[env_a, env_b, env_c], out=None)
    buf = io.StringIO()
    real_stdout = sys.stdout
    sys.stdout = buf
    try:
        cmd_bundle(bns)
    finally:
        sys.stdout = real_stdout
    bundled = buf.getvalue()
    check(
        "bundle/passes-collected-at-through",
        "collected_at=2026-10-27T01:02:03+00:00" in bundled
        and "collected_at=2026-10-27T04:05:06+00:00" in bundled,
        "★ 采集时刻没透传到 bundle 头部",
    )
    check(
        "bundle/fallback-is-explicit-unknown",
        "collected_at=unknown" in bundled,
        "★ 缺采集时刻时兜底必须是显式的 unknown（★ 不许长得像合法值）",
    )
    os.unlink(env_a)
    os.unlink(env_b)
    os.unlink(env_c)

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
    p.add_argument(
        "--columns",
        help="column census TSV (★ 没有它就判不出「列级联退役」⇒ ⇒ 假缺口继续躺着)",
    )
    p.add_argument("--out", help="output gap ledger TSV (default: stderr only)")
    p.add_argument("--json", action="store_true", help="print a JSON summary to stderr")
    p.set_defaults(func=cmd_reconcile)

    p = sub.add_parser("bundle", help="merge per-environment census files into one artifact")
    p.add_argument("--in", dest="inputs", action="append", required=True, help="input file (repeatable)")
    p.add_argument("--out", help="output TSV (default: stdout)")
    p.set_defaults(func=cmd_bundle)

    p = sub.add_parser(
        "verify",
        help="★ R164：★ 把快照与此刻的真实库对拍 ⇒ 报【结构漂移】（★ 退出码 0/1/2）",
    )
    p.add_argument("--env", required=True, help="environment label")
    p.add_argument("--census", required=True, help="census bundle TSV")
    p.add_argument("--psql", help="psql binary (default: $GUARD_PSQL or psql)")
    p.add_argument(
        "--from-stdin",
        action="store_true",
        help="read already-collected census rows from stdin (★ remote envs)",
    )
    p.add_argument("--json", action="store_true", help="print a JSON summary to stderr")
    p.set_defaults(func=cmd_verify)

    p = sub.add_parser("retired", help="emit retirement-evidence ledger (created-then-dropped)")
    p.add_argument("--out", help="output TSV (default: stdout)")
    p.set_defaults(func=cmd_retired)

    p = sub.add_parser("columns", help="read-only column census of one environment")
    # ★ R164：★ --env 不再是必填（★ 因为 `--emit-sql` 模式下不需要环境标签 ⇒ ★ 只产 SQL）
    p.add_argument("--env", help="environment label (★ --emit-sql 时可省)")
    p.add_argument("--out", help="output TSV (default: stdout)")
    p.add_argument("--append", action="store_true", help="append rows to --out")
    p.add_argument("--psql", help="psql binary (default: $GUARD_PSQL or psql)")
    p.add_argument("--from-stdin", action="store_true", help="read already-collected rows from stdin")
    p.add_argument(
        "--keys-from",
        help="expected-set TSV (★ 只采期望集索引的【声明键列】⇒ 规模降到 1/8)",
    )
    p.add_argument(
        "--emit-sql",
        help="★ R164：★ 只写出采集用的 SQL（★ 远程环境用；★ ★ 采集 SQL 必须由工具生成 ⇒ 不许手搓）",
    )
    p.set_defaults(func=cmd_columns)

    p = sub.add_parser(
        "reachability",
        help="emit the installer-reachability ledger (★ 新装环境为什么会有/不会有它)",
    )
    p.add_argument("--expect", required=True, help="expected-set TSV")
    p.add_argument("--census", required=True, help="census TSV")
    p.add_argument("--columns", help="column census TSV")
    p.add_argument("--manifest", required=True, help="installer manifest TSV")
    p.add_argument("--package", required=True, help="installer embeddata startup dir")
    p.add_argument("--fresh-env", default="fresh", help="which env is the fresh-install baseline")
    p.add_argument("--out", help="output TSV (default: stdout)")
    p.add_argument("--json", action="store_true", help="print a JSON summary to stderr")
    p.set_defaults(func=cmd_reachability)

    p = sub.add_parser("columns-bundle", help="merge per-environment column census files")
    p.add_argument("--in", dest="inputs", action="append", required=True, help="input file (repeatable)")
    p.add_argument("--out", help="output TSV (default: stdout)")
    p.set_defaults(func=cmd_columns_bundle)

    p = sub.add_parser("selftest", help="self-tests")
    p.set_defaults(func=cmd_selftest)

    # ★★ R164：★ 把采集 SQL 本身也做成一个模式 ⇒ ★ 远程采集不用再手搓 SQL
    # ★★ （★ 手搓过一次 ⇒ ★ 口径分叉 ⇒ ★ 252/211 采到 0 行 ⇒ ★ 见审计档 §5）。
    p = sub.add_parser(
        "census-sql",
        help="★ R164：★ 打印对象底账的采集 SQL（★ 只读；★ 远程采集用这个，★ 不许手搓）",
    )
    p.set_defaults(func=lambda args: (sys.stdout.write(CENSUS_SQL), 0)[1])

    args = ap.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
