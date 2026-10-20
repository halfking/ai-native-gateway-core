#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""§143.23 本地真库实测：复现「DETACH 对孤儿表必然报错」并验证修法 A/C。

关键约束（证据力所在）：
  ★ 本地镜像 `llm-gateway-pg` 的 citus 13.3-1 **不含 `columnar` access method**
    （实测 `access method "columnar" does not exist`）⇒ 本地**只能测 heap 路径**。
  ★ 因此本脚本把**仓内 654 迁移原文**的函数体**只替换一处**：
    `USING columnar` → `USING heap`。
    ⇒ **核心逻辑逐字不变**（DETACH PARTITION 的收尾、col_list 构造、EXCEPT ALL 比对、
      skipped 分支、advisory lock）⇒ 实测结论对生产 columnar 路径同样成立。
  ★ 被测的 654 函数体由**脚本从仓内迁移文件提取**（不是手抄）⇒ 杜绝「抄错版本」。

场景：
  S1 正常路径：目标分区**在父表下** ⇒ 654 应 success（建 dst → 搬 → DETACH → DROP）
  S2 缺陷复现：目标分区**被 DETACH 过**（孤儿表）⇒ 654 应**报错**（缺陷 ②）
  S3 修法验证：同一孤儿表场景 ⇒ 885 应 success（直接搬 + DROP，不报错）
  S4 回归：修法版对**在父表下**的分区 ⇒ 应 success 且仍走 DETACH + DROP
  S5 幂等：再跑一次 ⇒ 应 skipped（源已不存在）
"""
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
MIG654 = os.path.join(REPO, "sql/migrations/startup/654_archive_credential_model_index_detach_drop.sql")
CONTAINER = "llm-gateway-pg"
PGUSER = "llm_gateway"
DB = "itgate_14323"

# ── 885 修法版函数体（签名与返回形状**逐字不变** ⇒ Go 侧零改动）。
#    与 sql/migrations/startup/885_*.sql 将要落地的内容一致。
F885 = r"""
CREATE OR REPLACE FUNCTION public.archive_credential_model_index(archive_month date)
    RETURNS TABLE(status text, rows_migrated bigint, partition_dropped boolean)
    LANGUAGE plpgsql
    AS $$
        DECLARE
            month_start date := date_trunc('month', archive_month)::date;
            month_end   date := (date_trunc('month', archive_month) + interval '1 month')::date;
            src_part    text := 'credential_model_index_' || to_char(month_start, 'YYYY_MM');
            dst_part    text := 'credential_model_index_archive_' || to_char(month_start, 'YYYY_MM');
            row_count   bigint;
            source_count bigint;
            target_count bigint;
            col_list    text;
            rows_match  boolean;
            target_exists boolean;
            src_attached boolean;
        BEGIN
            PERFORM pg_advisory_xact_lock(hashtext('archive_credential_model_index:' || to_char(month_start, 'YYYY-MM')));

            IF NOT EXISTS (SELECT 1 FROM pg_class
                           WHERE relname = src_part AND relnamespace = 'public'::regnamespace) THEN
                RETURN QUERY SELECT 'skipped'::text, 0::bigint, false;
                RETURN;
            END IF;

            -- ★ 修法 C：源表是否**仍挂在父表下**？
            --   迁移 337 把 CMI 的 _2026_07 / _2026_08 DETACH 成了孤儿表；
            --   654 的收尾 `ALTER TABLE ... DETACH PARTITION` 对孤儿表**必然报错**。
            SELECT EXISTS (
                SELECT 1 FROM pg_inherits i
                JOIN pg_class p ON p.oid = i.inhparent
                WHERE i.inhrelid = to_regclass(src_part)
                  AND p.relname = 'credential_model_index'
            ) INTO src_attached;

            IF NOT src_attached THEN
                RAISE WARNING 'archive_credential_model_index: target % is detached from credential_model_index (orphan table); archiving via direct copy + DROP', src_part;
            END IF;

            IF NOT EXISTS (SELECT 1 FROM pg_class
                           WHERE relname = dst_part AND relnamespace = 'public'::regnamespace) THEN
                EXECUTE format(
                    'CREATE TABLE %I PARTITION OF credential_model_index_archive FOR VALUES FROM (%L) TO (%L) USING heap',
                    dst_part, month_start, month_end
                );
                target_exists := false;
            ELSE
                target_exists := true;
            END IF;

            SELECT string_agg(a.column_name, ', ' ORDER BY a.ordinal_position)
            INTO col_list
            FROM information_schema.columns a
            JOIN information_schema.columns r
              ON a.table_schema = r.table_schema
             AND a.column_name  = r.column_name
            WHERE a.table_name = 'credential_model_index_archive'
              AND r.table_name = src_part
              AND a.table_schema = 'public'
              AND a.ordinal_position > 0;

            IF col_list IS NULL OR length(col_list) = 0 THEN
                RAISE EXCEPTION 'No common columns between % and credential_model_index_archive', src_part;
            END IF;

            IF target_exists THEN
                EXECUTE format('SELECT count(*) FROM %I', src_part) INTO source_count;
                EXECUTE format('SELECT count(*) FROM %I', dst_part) INTO target_count;
                IF target_count = 0 THEN
                    EXECUTE format(
                        'INSERT INTO %I (%s) SELECT %s FROM %I',
                        dst_part, col_list, col_list, src_part
                    );
                    GET DIAGNOSTICS row_count = ROW_COUNT;
                ELSE
                    EXECUTE format(
                        'SELECT NOT EXISTS ((SELECT %s FROM %I EXCEPT ALL SELECT %s FROM %I) UNION ALL (SELECT %s FROM %I EXCEPT ALL SELECT %s FROM %I))',
                        col_list, src_part, col_list, dst_part,
                        col_list, dst_part, col_list, src_part
                    ) INTO rows_match;
                    IF NOT rows_match THEN
                        RAISE EXCEPTION 'archive target % contains partial or mismatched rows (source %, target %)',
                            dst_part, source_count, target_count;
                    END IF;
                    row_count := 0;
                END IF;
            ELSE
                EXECUTE format(
                    'INSERT INTO %I (%s) SELECT %s FROM %I',
                    dst_part, col_list, col_list, src_part
                );
                GET DIAGNOSTICS row_count = ROW_COUNT;
            END IF;

            -- ★ 修法 A：源还在父表下 ⇒ DETACH + DROP；源是孤儿表 ⇒ 只 DROP
            --   （对孤儿表执行 DETACH PARTITION 必然报错，见 654 的收尾动作）
            IF src_attached THEN
                EXECUTE format('ALTER TABLE credential_model_index DETACH PARTITION %I', src_part);
            END IF;
            EXECUTE format('DROP TABLE %I', src_part);

            RETURN QUERY SELECT 'success'::text, row_count, true;
        END;
    $$;
"""


def extract_654_body() -> str:
    """从仓内 654 迁移里提取 CREATE OR REPLACE FUNCTION 段（不手抄）。"""
    with open(MIG654, encoding="utf-8") as f:
        text = f.read()
    m = re.search(r"CREATE OR REPLACE FUNCTION public\.archive_credential_model_index\(archive_month date\).*?\$\$;",
                  text, re.S)
    if not m:
        print("从 654 迁移里提取函数体失败", file=sys.stderr)
        sys.exit(2)
    body = m.group(0)
    # 唯一一处替换：本地无 columnar AM
    assert "USING columnar" in body, "654 原文里没有 USING columnar —— 提取到的不是预期的版本"
    body = body.replace("USING columnar", "USING heap")
    return body


def run_sql(sql: str, db: str = "postgres") -> str:
    p = subprocess.run(
        ["docker", "exec", "-i", CONTAINER, "psql", "-U", PGUSER, "-d", db, "-v", "ON_ERROR_STOP=0", "-f", "-"],
        input=sql, capture_output=True, text=True)
    return p.stdout + p.stderr


def main() -> int:
    body654 = extract_654_body()
    print(f"★ 已从 {os.path.relpath(MIG654, REPO)} 提取 654 函数体（仅替换 USING columnar→USING heap）")

    setup = f"""
\\set ON_ERROR_STOP on
DROP DATABASE IF EXISTS {DB};
CREATE DATABASE {DB};
\\connect {DB}

\\echo '=== 建同构 schema（RANGE 分区父表 + 月分区 + archive 父表 + ON ONLY 索引）==='
CREATE TABLE public.credential_model_index (
    bucket        timestamptz NOT NULL,
    credential_id bigint       NOT NULL,
    raw_model     text         NOT NULL,
    canonical_id  integer,
    success_rate  numeric(5,4),
    updated_at    timestamptz DEFAULT now()
) PARTITION BY RANGE (bucket);

CREATE INDEX idx_cmi_archive_bucket   ON ONLY public.credential_model_index USING btree (bucket DESC);
CREATE INDEX idx_cmi_archive_canonical ON ONLY public.credential_model_index USING btree (canonical_id, bucket DESC) WHERE (canonical_id IS NOT NULL);
CREATE INDEX idx_cmi_archive_cred_model ON ONLY public.credential_model_index USING btree (credential_id, raw_model, bucket DESC);

CREATE TABLE public.credential_model_index_archive (
    bucket        timestamptz NOT NULL,
    credential_id bigint       NOT NULL,
    raw_model     text         NOT NULL,
    canonical_id  integer,
    success_rate  numeric(5,4),
    updated_at    timestamptz DEFAULT now()
) PARTITION BY RANGE (bucket);

CREATE TABLE public.credential_model_index_2026_09 PARTITION OF public.credential_model_index
    FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE public.credential_model_index_2026_10 PARTITION OF public.credential_model_index
    FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');

\\echo '=== 装 654（仓内原文，AM 替换为 heap）==='
{body654}

\\echo '=== 造数据：09 月分区 30000 行 / 10 月分区 5000 行 ==='
INSERT INTO public.credential_model_index_2026_09
SELECT ts, g, 'model-' || (g % 40), g % 7, (g % 100)::numeric / 100, now()
FROM generate_series('2026-09-01'::timestamptz, '2026-09-30 23:55'::timestamptz, '5 minutes') ts,
     generate_series(1, 40) g;
INSERT INTO public.credential_model_index_2026_10
SELECT ts, g, 'model-' || (g % 40), g % 7, (g % 100)::numeric / 100, now()
FROM generate_series('2026-10-01'::timestamptz, '2026-10-05 23:55'::timestamptz, '5 minutes') ts,
     generate_series(1, 10) g;
SELECT '2026_09' AS part, count(*) FROM public.credential_model_index_2026_09
UNION ALL SELECT '2026_10', count(*) FROM public.credential_model_index_2026_10;
"""
    out = run_sql(setup)
    print(out)
    if "ERROR" in out and "FATAL" in out:
        print("setup 失败")
        return 2

    # ── S2：把 09 月分区 DETACH 成孤儿表（复现迁移 337 的形态）
    detach = """
\\echo '=== S2 前置：把 09 月分区 DETACH 成孤儿表（复现迁移 337 的形态）==='
ALTER TABLE public.credential_model_index DETACH PARTITION public.credential_model_index_2026_09;
\\echo '--- 孤儿表仍可直读、但父表查不到 ---'
SELECT (SELECT count(*) FROM public.credential_model_index_2026_09) AS orphan_readable,
       (SELECT count(*) FROM public.credential_model_index)      AS visible_via_parent;
"""
    print(run_sql(detach, DB))

    # ── S2b：654 对孤儿表 ⇒ 预期报错
    s2 = """
\\echo '=== S2：654（仓内原文）对孤儿表 ⇒ 预期 ERROR（缺陷 ② 复现）==='
SELECT * FROM public.archive_credential_model_index('2026-09-03');
\\echo '--- 事务是否回滚：dst 应不存在、源应仍在 ---'
SELECT count(*) AS dst_2026_09_exists FROM pg_class WHERE relname='credential_model_index_archive_2026_09';
SELECT count(*) AS src_orphan_rows FROM public.credential_model_index_2026_09;
"""
    print(run_sql(s2, DB))

    # ── S3：装 885 修法版，对同一孤儿表 ⇒ 预期 success
    s3 = f"""
\\echo '=== S3：885 修法版对同一孤儿表 ⇒ 预期 success（直接搬 + DROP）==='
\\echo '--- 装 885 修法版 ---'
{F885}
SELECT * FROM public.archive_credential_model_index('2026-09-03');
\\echo '--- 校验：archive 拿到全部行、源表已消失 ---'
SELECT count(*) AS archived_rows FROM public.credential_model_index_archive_2026_09;
SELECT count(*) AS src_still_exists FROM pg_class WHERE relname='credential_model_index_2026_09';
"""
    print(run_sql(s3, DB))

    # ── S4：修法版对**在父表下**的 10 月分区 ⇒ 预期 success 且走 DETACH+DROP
    s4 = """
\\echo '=== S4：885 修法版对在父表下的 10 月分区 ⇒ 预期 success（回归：仍走 DETACH + DROP）==='
SELECT * FROM public.archive_credential_model_index('2026-10-03');
\\echo '--- 校验：10 月源分区应已从父表下摘掉且被 DROP；父表应查不到它 ---'
SELECT count(*) AS src_2026_10_exists FROM pg_class WHERE relname='credential_model_index_2026_10';
SELECT count(*) AS parent_visible_rows FROM public.credential_model_index;
SELECT count(*) AS archived_2026_10 FROM public.credential_model_index_archive_2026_10;
"""
    print(run_sql(s4, DB))

    # ── S5：幂等（源已不存在 ⇒ skipped）
    s5 = """
\\echo '=== S5：再跑一次 ⇒ 预期 skipped（源已不存在）==='
SELECT * FROM public.archive_credential_model_index('2026-09-03');
"""
    print(run_sql(s5, DB))

    # ── 清理
    print(run_sql(f"DROP DATABASE IF EXISTS {DB};"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
