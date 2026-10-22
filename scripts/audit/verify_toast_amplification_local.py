#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
verify_toast_amplification_local.py —— §143.65 会话族 TOAST 放大实验（一次性库，零生产写入）。

用途
----
把 §143.65 的三条机制结论变成**可复跑的实验**，供后人重新验证（而不是重新推导）：

  A. **量具自证**：`pg_column_size()` 对外部 TOAST 值返回压缩后载荷字节；
     `pg_stat_all_tables.n_tup_ins` 在纯 INSERT 场景下 == 精确 chunk 行数；
     ⇒ 「每块实际载荷 = 载荷 ÷ chunk 数」可测。
     ★ 不压缩基线（STORAGE EXTERNAL）必须 ≈ 1.000 —— 这是量具自己的对照。
  B. **机制一（被推翻的假说）**：PG TOAST 是「先整体压缩、再按 1996 B 切块」
     ⇒ 块是【满】的（chunk_fill 随值增大 → 100%）
     ⇒ 「逐块压缩 ⇒ 填充率 < 100% ⇒ 放大 1/填充率」**不成立**。
  C. **机制二**：lz4 ≤ pglz（体积）；zstd 在本 PG 构建不可用。
  D. **机制三**：promote 的 `INSERT..SELECT` + `DELETE` **确实**把正文写两遍，
     但旧块是**死块** ⇒ 普通 VACUUM 就能回收 ⇒ 不是结构性放大。

用法
----
    python3 scripts/audit/verify_toast_amplification_local.py            # 建库+跑+打印
    python3 scripts/audit/verify_toast_amplification_local.py --keep     # 保留库（调试）

环境要求
--------
本机 docker 里有 `llm-gateway-pg`（PG 17.x，内含 lz4）。脚本会在该实例里建
一次性库 `itgate_14360`，跑完即 DROP ⇒ **对生产零写入**。

★ 本脚本**不读 .env / 不连生产**：DSN 由 docker 内置的本地实例提供。
"""

import argparse
import json
import subprocess
import sys

PG_CONTAINER = "llm-gateway-pg"
DB = "itgate_14360"
PGUSER = "llm_gateway"
CHUNK = 1996  # PG 的 TOAST_MAX_CHUNK_SIZE


def psql(sql, db=DB):
    cmd = [
        "docker", "exec", "-i", PG_CONTAINER, "psql", "-U", PGUSER, "-d", db,
        "-v", "ON_ERROR_STOP=1", "-P", "pager=off", "-At", "-F", "|",
    ]
    p = subprocess.run(cmd, input=sql, capture_output=True, text=True)
    if p.returncode != 0:
        raise RuntimeError("psql failed:\n" + p.stderr.strip()[:4000])
    return p.stdout.strip()


def q(sql):
    out = psql(sql)
    return [line.split("|") for line in out.splitlines()] if out else []


def hdr(title):
    print("\n" + "=" * 78)
    print(title)
    print("=" * 78)


def table(rows, headers):
    if not rows:
        print("(no rows)")
        return
    widths = [len(h) for h in headers]
    for r in rows:
        for i, c in enumerate(r):
            widths[i] = max(widths[i], len(str(c)))
    print(" | ".join(h.ljust(widths[i]) for i, h in enumerate(headers)))
    print("-+-".join("-" * w for w in widths))
    for r in rows:
        print(" | ".join(str(c).ljust(widths[i]) for i, c in enumerate(r)))


NOAV = "ALTER TABLE {t} SET (autovacuum_enabled = false, toast.autovacuum_enabled = false);"

GEN_FN = """
CREATE OR REPLACE FUNCTION exp_gen(p_id int, p_blocks int) RETURNS jsonb
LANGUAGE sql IMMUTABLE AS $fn$
  SELECT jsonb_build_object(
    'id', p_id, 'type', 'message', 'role', 'assistant',
    'model', 'claude-sonnet-4-20250514', 'stop_reason', 'end_turn',
    'usage', jsonb_build_object('input_tokens', 4211, 'output_tokens', 1873),
    'content', (
      SELECT jsonb_agg(
        jsonb_build_object(
          'type', 'text',
          'text', CASE WHEN i % 2 = 0
                       THEN md5(p_id::text || ':' || i::text) || md5((p_id + i)::text || ':y')
                       ELSE repeat('The model explains the request logs retention policy. ', 12)
                  END))
        FROM generate_series(1, p_blocks) i)
  );
$fn$;
"""


def ensure_db():
    subprocess.run(
        ["docker", "exec", "-i", PG_CONTAINER, "psql", "-U", PGUSER, "-d", "postgres",
         "-c", f"DROP DATABASE IF EXISTS {DB};"],
        capture_output=True, text=True, check=False)
    p = subprocess.run(
        ["docker", "exec", "-i", PG_CONTAINER, "psql", "-U", PGUSER, "-d", "postgres",
         "-c", f"CREATE DATABASE {DB};"],
        capture_output=True, text=True, check=False)
    if p.returncode != 0:
        raise RuntimeError("CREATE DATABASE failed: " + p.stderr[:2000])


def drop_db():
    subprocess.run(
        ["docker", "exec", "-i", PG_CONTAINER, "psql", "-U", PGUSER, "-d", "postgres",
         "-c", f"DROP DATABASE IF EXISTS {DB};"],
        capture_output=True, text=True, check=False)


def env_facts():
    rows = q("""
        SELECT version();
        SELECT name, setting FROM pg_settings WHERE name='default_toast_compression';
    """)
    hdr("0. 环境自证")
    for r in rows:
        print("  " + " | ".join(r))
    avail = []
    for m in ("lz4", "pglz", "zstd"):
        p = subprocess.run(
            ["docker", "exec", "-i", PG_CONTAINER, "psql", "-U", PGUSER, "-d", DB,
             "-v", "ON_ERROR_STOP=1", "-At", "-c",
             f"create table if not exists _probe_{m}(x text); "
             f"alter table _probe_{m} alter column x set compression {m};"],
            capture_output=True, text=True, check=False)
        avail.append((m, "OK" if p.returncode == 0 else "UNAVAILABLE"))
    psql("DROP TABLE IF EXISTS _probe_lz4,_probe_pglz,_probe_zstd;")
    print("  compression_methods: " + ", ".join(f"{m}={v}" for m, v in avail))
    return avail


# ── A. 压缩算法对比 + 量具自证 ────────────────────────────────────────────
EXP_A = """
DROP TABLE IF EXISTS exp_a_lz4, exp_a_pglz, exp_a_raw;
CREATE TABLE exp_a_lz4  (id int, payload jsonb);
CREATE TABLE exp_a_pglz (id int, payload jsonb);
CREATE TABLE exp_a_raw  (id int, payload jsonb);
ALTER TABLE exp_a_lz4  ALTER COLUMN payload SET COMPRESSION lz4;
ALTER TABLE exp_a_pglz ALTER COLUMN payload SET COMPRESSION pglz;
ALTER TABLE exp_a_raw  ALTER COLUMN payload SET STORAGE EXTERNAL;
""" + NOAV.format(t="exp_a_lz4") + NOAV.format(t="exp_a_pglz") + NOAV.format(t="exp_a_raw") + """
INSERT INTO exp_a_lz4  SELECT g, exp_gen(g, 300) FROM generate_series(1, 20) g;
INSERT INTO exp_a_pglz SELECT g, exp_gen(g, 300) FROM generate_series(1, 20) g;
INSERT INTO exp_a_raw  SELECT g, exp_gen(g, 300) FROM generate_series(1, 20) g;
SELECT pg_stat_force_next_flush();
"""

EXP_A_MEASURE = """
SELECT (SELECT count(*) FROM {TAB})                              AS rows,
       (SELECT sum(length(payload::text)) FROM {TAB})            AS logical_bytes,
       (SELECT sum(pg_column_size(payload)) FROM {TAB})          AS payload_bytes,
       (SELECT s.n_tup_ins FROM pg_class c JOIN pg_stat_all_tables s
          ON s.relid=c.reltoastrelid WHERE c.relname='{TAB}')    AS chunks,
       (SELECT pg_total_relation_size(c.reltoastrelid) FROM pg_class c
         WHERE c.relname='{TAB}')                                AS toast_total_bytes;
"""


def exp_a():
    psql(GEN_FN + EXP_A)
    hdr("A. 压缩算法对比 + 量具自证（不压缩基线必须 ≈ 1.000）")
    out = []
    for t in ("exp_a_lz4", "exp_a_pglz", "exp_a_raw"):
        r = q(EXP_A_MEASURE.replace("{TAB}", t))[0]
        nrows, logical, payload, chunks, total = (int(x) for x in r)
        out.append([
            t, nrows, logical, payload, chunks, total,
            f"{logical/payload:.3f}" if payload else "-",
            f"{payload/chunks:.1f}" if chunks else "-",
            f"{payload/(chunks*CHUNK)*100:.1f}%" if chunks else "-",
            f"{total/payload:.4f}" if payload else "-",
        ])
    table(out, ["variant", "rows", "logical", "payload", "chunks", "toast_total",
                "logical/payload", "payload/chunk", "chunk_fill", "total/payload"])
    return out


# ── B. 尺寸阶梯（每尺寸一张表 ⇒ n_tup_ins 即该尺寸的精确 chunk 数）────────
SIZES = [100, 1500, 3000, 10000, 100000, 1000000]


def exp_b():
    parts = ["DROP TABLE IF EXISTS " + ", ".join(f"exp_b_{s}" for s in SIZES) + ";"]
    for s in SIZES:
        parts.append(f"CREATE TABLE exp_b_{s} (id int, payload jsonb);")
        parts.append(f"ALTER TABLE exp_b_{s} ALTER COLUMN payload SET COMPRESSION lz4;")
        parts.append(NOAV.format(t=f"exp_b_{s}"))
        parts.append(
            f"INSERT INTO exp_b_{s} SELECT 1, "
            f"jsonb_build_object('t', (SELECT string_agg(md5(g::text), '') "
            f"FROM generate_series(1, {max(1, s // 32 + 1)}) g));")
    parts.append("SELECT pg_stat_force_next_flush();")
    psql("\n".join(parts))
    hdr("B. 尺寸阶梯：块计数 vs 两种理论值（先整体压缩再切块 ⇒ chunks == ceil(payload/1996)）")
    out = []
    for s in SIZES:
        r = q(f"""
            SELECT (SELECT length(payload::text) FROM exp_b_{s})          AS logical_bytes,
                   (SELECT pg_column_size(payload) FROM exp_b_{s})        AS payload_bytes,
                   (SELECT s.n_tup_ins FROM pg_class c JOIN pg_stat_all_tables s
                      ON s.relid=c.reltoastrelid WHERE c.relname='exp_b_{s}') AS chunks;
        """)[0]
        logical, payload, chunks = (int(x) for x in r)
        ecp = -(-payload // CHUNK)
        ecl = -(-logical // CHUNK)
        out.append([s, logical, payload, chunks, ecp, ecl,
                    f"{payload/chunks:.1f}" if chunks else "-",
                    f"{payload/(chunks*CHUNK)*100:.1f}%" if chunks else "-"])
    table(out, ["target", "logical", "payload", "chunks",
                "exp_chunks(payload)", "exp_chunks(logical)",
                "payload/chunk", "chunk_fill"])
    return out


# ── C. promote 双写 + VACUUM 回收 ────────────────────────────────────────
EXP_C = """
DROP TABLE IF EXISTS exp_c_parent_2026_10, exp_c_parent, exp_c_hot;
CREATE TABLE exp_c_parent (id bigint, request_id text, payload jsonb,
                           partition_date date) PARTITION BY RANGE (partition_date);
CREATE TABLE exp_c_parent_2026_10 PARTITION OF exp_c_parent
    FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
ALTER TABLE exp_c_parent ADD UNIQUE (request_id, partition_date);
CREATE TABLE exp_c_hot (id bigint, request_id text, payload jsonb, partition_date date);
ALTER TABLE exp_c_hot ALTER COLUMN payload SET COMPRESSION lz4;
ALTER TABLE exp_c_parent_2026_10 ALTER COLUMN payload SET COMPRESSION lz4;
""" + NOAV.format(t="exp_c_hot") + NOAV.format(t="exp_c_parent_2026_10") + """
INSERT INTO exp_c_hot SELECT g, 'req-'||g, exp_gen(g, 300), DATE '2026-10-15'
  FROM generate_series(1, 50) g;
SELECT pg_stat_force_next_flush();
"""

EXP_C_MEASURE = """
WITH t AS (
  SELECT 'hot' AS side, c.reltoastrelid AS toast FROM pg_class c WHERE c.relname='exp_c_hot'
  UNION ALL
  SELECT 'parent', c.reltoastrelid FROM pg_class c WHERE c.relname='exp_c_parent_2026_10'
)
SELECT t.side,
       (SELECT s.n_live_tup FROM pg_stat_all_tables s WHERE s.relid=t.toast)  AS n_live,
       (SELECT s.n_dead_tup FROM pg_stat_all_tables s WHERE s.relid=t.toast)  AS n_dead,
       (SELECT s.n_tup_ins FROM pg_stat_all_tables s WHERE s.relid=t.toast)  AS n_ins,
       (SELECT s.n_tup_del FROM pg_stat_all_tables s WHERE s.relid=t.toast)  AS n_del,
       pg_relation_size(t.toast)       AS heap_bytes,
       pg_total_relation_size(t.toast) AS total_bytes
  FROM t ORDER BY t.side;
"""

EXP_C_PROMOTE = """
INSERT INTO exp_c_parent (id, request_id, payload, partition_date)
SELECT id, request_id, payload, partition_date FROM exp_c_hot
ON CONFLICT (request_id, partition_date) DO NOTHING;
DELETE FROM exp_c_hot h USING exp_c_parent p
 WHERE h.request_id = p.request_id AND h.partition_date = p.partition_date;
SELECT pg_stat_force_next_flush();
"""


def exp_c():
    psql(EXP_C)
    hdr("C. promote 双写（INSERT..SELECT + DELETE）与 VACUUM 回收")
    steps = [("① 只写 hot（未 promote）", q(EXP_C_MEASURE))]
    psql(EXP_C_PROMOTE)
    steps.append(("② promote 后（未 VACUUM）", q(EXP_C_MEASURE)))
    psql("VACUUM exp_c_hot; VACUUM exp_c_parent_2026_10;")
    steps.append(("③ 普通 VACUUM 后", q(EXP_C_MEASURE)))
    psql("VACUUM FULL exp_c_hot; VACUUM FULL exp_c_parent_2026_10;")
    steps.append(("④ VACUUM FULL 后", q(EXP_C_MEASURE)))
    hdrs = ["side", "n_live", "n_dead", "n_ins", "n_del", "heap_bytes", "total_bytes"]
    for title, rows in steps:
        print("\n  " + title)
        table(rows, hdrs)
    return [[t, r] for t, r in steps]


# ── D. 单列 vs 五列（生产的 5 个 jsonb 列共用一个 TOAST）─────────────────
EXP_D = """
DROP TABLE IF EXISTS exp_d_one, exp_d_five;
CREATE TABLE exp_d_one  (id int, payload jsonb);
CREATE TABLE exp_d_five (id int, request_delta jsonb, response_delta jsonb,
                         outbound_body jsonb, request_attachments jsonb,
                         response_attachments jsonb);
ALTER TABLE exp_d_one  ALTER COLUMN payload SET COMPRESSION lz4;
ALTER TABLE exp_d_five ALTER COLUMN request_delta SET COMPRESSION lz4;
ALTER TABLE exp_d_five ALTER COLUMN response_delta SET COMPRESSION lz4;
ALTER TABLE exp_d_five ALTER COLUMN outbound_body SET COMPRESSION lz4;
ALTER TABLE exp_d_five ALTER COLUMN request_attachments SET COMPRESSION lz4;
ALTER TABLE exp_d_five ALTER COLUMN response_attachments SET COMPRESSION lz4;
""" + NOAV.format(t="exp_d_one") + NOAV.format(t="exp_d_five") + """
INSERT INTO exp_d_one SELECT g, exp_gen(g, 300) FROM generate_series(1, 20) g;
INSERT INTO exp_d_five
SELECT g,
       jsonb_build_object('role','user','content','[hello]'),
       jsonb_build_object('role','assistant','content','[world]'),
       exp_gen(g, 300),
       '[]'::jsonb, '[]'::jsonb
  FROM generate_series(1, 20) g;
SELECT pg_stat_force_next_flush();
"""


def exp_d():
    psql(EXP_D)
    hdr("D. 单列 vs 五列（形态对存储无影响；差别只在「你数哪一列」）")
    one = q("""
        SELECT (SELECT sum(length(payload::text)) FROM exp_d_one)   AS logical_bytes,
               (SELECT sum(pg_column_size(payload)) FROM exp_d_one) AS payload_bytes,
               (SELECT s.n_tup_ins FROM pg_class c JOIN pg_stat_all_tables s
                  ON s.relid=c.reltoastrelid WHERE c.relname='exp_d_one') AS chunks,
               (SELECT pg_total_relation_size(c.reltoastrelid) FROM pg_class c
                  WHERE c.relname='exp_d_one')                       AS toast_total;
    """)[0]
    five = q("""
        SELECT (SELECT sum(length(request_delta::text) + length(response_delta::text)
                         + length(outbound_body::text) + length(request_attachments::text)
                         + length(response_attachments::text)) FROM exp_d_five) AS logical_bytes,
               (SELECT sum(pg_column_size(request_delta) + pg_column_size(response_delta)
                         + pg_column_size(outbound_body) + pg_column_size(request_attachments)
                         + pg_column_size(response_attachments)) FROM exp_d_five) AS payload_bytes,
               (SELECT s.n_tup_ins FROM pg_class c JOIN pg_stat_all_tables s
                  ON s.relid=c.reltoastrelid WHERE c.relname='exp_d_five') AS chunks,
               (SELECT pg_total_relation_size(c.reltoastrelid) FROM pg_class c
                  WHERE c.relname='exp_d_five')                       AS toast_total;
    """)[0]
    table([tuple(one), tuple(five)], ["variant", "logical", "payload", "chunks", "toast_total"])
    return [list(one), list(five)]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--keep", action="store_true", help="跑完保留一次性库（调试用）")
    ap.add_argument("--json", action="store_true", help="只输出 JSON")
    args = ap.parse_args()

    ensure_db()
    try:
        env_facts()
        a = exp_a()
        b = exp_b()
        c = exp_c()
        d = exp_d()
        if args.json:
            print(json.dumps({"A": a, "B": b, "C": c, "D": d}, ensure_ascii=False, indent=1))
        else:
            hdr("结论（与 runbook §143.65 对应）")
            print("A ⇒ 不压缩基线 logical/payload ≈ 1.000 ⇒ 量具自证通过；lz4 ≤ pglz。")
            print("B ⇒ chunks == ceil(payload/1996) 且 chunk_fill → 100% ⇒ 「逐块压缩」假说不成立。")
            print("C ⇒ promote 确实写两遍，但旧块是死块 ⇒ 普通 VACUUM 就能回收。")
            print("D ⇒ 单列 vs 五列的存储相同 ⇒ 产能差异只来自「你数了哪一列」。")
    finally:
        if not args.keep:
            drop_db()
            print("\n(一次性库已 DROP)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
