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
import atexit
import os
import re
import signal
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



# stdout/stderr 必须显式配 UTF-8：这些脚本会打印含「⇒」等字符的中文，
# 而 cp936（Windows 中文默认）编码不了 ⇒ print() 抛 UnicodeEncodeError
# ⇒ 门在出读数时就崩了。在 UTF-8 locale 下本段是 no-op，POSIX 行为不变。
for _stream in (sys.stdout, sys.stderr):
    try:
        _stream.reconfigure(encoding='utf-8')
    except (AttributeError, ValueError):
        pass

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


def run_sql(sql: str, db: str = "postgres", stop_on_error: bool = False) -> str:
    """跑一段 SQL。

    ★★ 【失败可见性】实测（PG 17.10；★ R102 轮在 PG 17.11 + psql 18.6 上复验并**扩一维**）：
    psql 的失败可见性**由「读入方式」决定**，**不由「语句是否出错」决定**：
      · `-c` 形态（**一条 `-c` 内多条语句**）⇒ 失败 ⇒ **exit 1**，
        且**整串回滚**（前面已成功的语句也一并回滚 ⇒ 落点**干净**）
      · stdin 形态 ⇒ 失败 ⇒ **exit 0**（**仅当 ON_ERROR_STOP=0**）
        ⇒ 且**后续语句照常执行** ⇒ ★★ 这是「**半执行**」⇒ 落点**不干净**
        ⇒ ⇒ 「退出码」与「落点」是**两个轴**，不是一个
      · stdin 形态 + ON_ERROR_STOP=1 ⇒ **exit 3**，且**后续语句不再执行**
        ⇒ ★★ 「失败可检出」**≠**「落点干净」⇒ ⇒ 【查落点】**不可省**
      ★ R102 轮实测：三种 stdin 读入方式（**裸 stdin / `-f -` / `-f /dev/stdin`**）
        的失败语义**完全一致** ⇒ ⇒ 叫法差异（交互模式 vs 脚本模式）不影响结论。
    ⇒ 本脚本走 stdin 形态（`-f -`）⇒ ⇒ 【清理路径必须显式 ON_ERROR_STOP=1】，
      否则「DROP 被 statement_timeout 取消」会**静默**⇒ 而「清理失败」＝「库泄漏」。
    ⇒ 其它路径保持 ON_ERROR_STOP=0：S2 场景**要**复现 654 的报错并继续往下跑。
    """
    p = subprocess.run(
        ["docker", "exec", "-i", CONTAINER, "psql", "-U", PGUSER, "-d", db,
         "-v", "ON_ERROR_STOP=" + ("1" if stop_on_error else "0"), "-f", "-"],
        input=sql, capture_output=True, encoding='utf-8', errors='replace')
    return p.stdout + p.stderr


def _residual() -> str:
    """库里还有几个同名库（★ 清理是否真的成功，只能【查落点】，不能看退出码）。"""
    out = run_sql(f"SELECT 'LEFT=' || count(*) FROM pg_database WHERE datname = '{DB}';")
    m = re.search(r"LEFT=(\d+)", out)
    return m.group(1) if m else "?"


# ★★★ 回收兜底（R94 轮实测修）：本脚本原本只在**正常跑完**时 DROP 一次性库
#   ⇒ `main()` 里的 `return 2`（setup 失败）经 `sys.exit(main())` 变成**脚本退出**
#   ⇒ **跳过回收** ⇒ 每次失败都在容器里留一个 `itgate_14323`。
#   ⇒ 修法：atexit 钩子 ⇒ 覆盖 sys.exit / 未捕获异常 / 正常返回 三种路径。
#   ★ Python 的 atexit ≈ shell 的 trap ≈ Go 的 defer ⇒ 同一个机制的三种语言实现。
#   ⚠ 诚实边界：atexit **不覆盖** os._exit 与 SIGKILL。
#
# ★★★★ R99 轮（§19.20.65）补两处 —— 上一版的兜底在**两个方向**上是空的：
#   ① 【信号路径】实测：python 的 atexit 在进程被 SIGTERM 终止时【不执行】
#      ⇒ ⇒ 「被 CI / Ctrl-C / 编排器超时杀掉」⇒ 一次性库留下
#      ⇒ ⇒ 修法：显式接管 SIGTERM/SIGINT ⇒ 处理器【抛异常】
#      ⇒ ⇒ 异常沿栈展开 ⇒ finally 与 atexit 钩子【真的】跑起来
#      ⇒ ⇒ 复用既有的兜底，而不是再写第二份。
#   ② 【防卡】上一版的 DROP 是【裸】的 ⇒ 遇到「还有连接没断干净」就无限期挂着
#      ⇒ ⇒ 修法：先 terminate 踢掉占用会话，再【带 statement_timeout】地 DROP。
#      ★★ 超时取 **120s** 而不是 15s：实测「terminate 之后 DROP 仍要等被
#        SIGTERM 的后端收尾」可达数十秒（PG 17.10 / 高负载下实测到 89.37s）
#        ⇒ ⇒ 15s 会把【合法】的清理砍成「被超时取消」⇒ ⇒ 而「清理失败」＝
#        「库泄漏」⇒ ⇒ 与守卫的目的**正好相反**。
#      ★★ SET 与 DROP 必须【逐条发送】：run_sql 走 psql -f - 的 stdin 形态
#        ⇒ 每条语句各自独立事务 ⇒ SET 对后面的 DROP 生效；
#        ★★ 若把两条塞进同一条 -c ⇒ PG 报「DROP DATABASE cannot run inside a
#          transaction block」⇒ 清理【直接失败且静默】。
#        ★★ R102 轮订正：「逐条发送」的准确含义是【**不把两条并进同一条 `-c`**】，
#          而**不是**【必须换形态】⇒ ★ 「同一 psql 进程的**多条独立 `-c`**」是**合法**的
#          （各自独立事务、SET 对后续生效）⇒ ★ 段八 的 shell 腿正是这个形态。
def _drop_db() -> str:
    try:
        run_sql(f"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '{DB}' AND pid <> pg_backend_pid();")
        out = run_sql(f"SET statement_timeout = '120s'; DROP DATABASE IF EXISTS {DB} WITH (FORCE);",
                      stop_on_error=True)
        # ★ 查落点：psql 在 stdin 形态下【ON_ERROR_STOP=0 时失败也 exit 0】⇒ 退出码不可信
        #   ⇒ ⇒ 「命令成功」必须由「库里还有没有」来判。
        #   ★★ R102 轮补一维：OES=1 时虽 exit 3（失败**可检出**），但 psql 会**在错误处中止**
        #     ⇒ 后续语句**不执行** ⇒ ★★ 「失败可检出」**≠**「落点干净」
        #     ⇒ ⇒ 【查落点】是**必需项**，不是可选项。
        left = _residual()
        if left != "0":
            out += f"\n⚠ 回收未完成：pg_database 里仍有 {left} 行匹配 {DB}"
        return out
    except Exception:  # noqa: BLE001 —— 回收失败不得盖掉真正的错误
        return ""


atexit.register(_drop_db)


# ★ 信号接管（R99 轮 · §19.20.65）⇒ 让上面的兜底在【信号路径】上也真的执行。
#   ★ 为什么抛自定义的 BaseException 而不是用 KeyboardInterrupt：
#     KeyboardInterrupt 会被 subprocess.run 特殊对待，而这里要的是
#     「异常展开（让 finally / atexit 真的跑）」+「保留被信号终止的语义」。
class _Terminated(BaseException):
    """收到 SIGTERM / SIGINT ⇒ 抛出来让兜底回收真的执行。"""


def _on_signal(signum, _frame):
    raise _Terminated(f"收到信号 {signum}")


def _install_signal_handlers() -> None:
    """接管 SIGTERM / SIGINT ⇒ 走「异常展开」路径 ⇒ finally / atexit 真的跑。"""
    signal.signal(signal.SIGTERM, _on_signal)
    signal.signal(signal.SIGINT, _on_signal)


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

    # ── 清理（★ 与 atexit 兜底【同一个】实现 ⇒ 只留一份 DROP，不漂移）
    print(_drop_db())
    return 0


if __name__ == "__main__":
    _install_signal_handlers()
    try:
        sys.exit(main())
    except _Terminated as e:
        # ★ 信号路径：异常已沿栈展开 ⇒ finally / atexit 钩子已真的跑过
        #   ⇒ 这里只把退出码还原成「被信号终止」的形状（128+15）。
        print(f"⚠ {e} ⇒ 兜底回收已执行（atexit）", file=sys.stderr)
        sys.exit(143)
