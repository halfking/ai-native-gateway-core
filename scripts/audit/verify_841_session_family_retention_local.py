#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
verify_841_session_family_retention_local.py —— §143.69 实验：
841 `monthly_partition_retention` 对**会话族**（`session_bodies` / `session_turns`）
启用的行为实测（一次性库，零生产写入）。

用途
----
把属主决策 ⑪（「v2 正文表 / turn 表在 DB 层无保留期 ⇒ ≈230 MB/天 ⇒ 一年 ≈84 GB」）
从「一条建议」变成「**已实测可执行的方案**」，并把它的**两个此前没被识别的副作用**钉住：

  ★ 副作用 A（已被 §143.68 量化）：**8 小时日历口径错位 ⇒ 边界那 8 小时
    （每月 1 日 00:00~08:00 (+08)）跟着【上一个月分区】一起被淘汰** ⇒
    它的实际保留期 = 配置月数 − 8 小时 ⇒ 相对 30 天窗口，误差 ≈ 0.9% ⇒ **不是阻塞项**。
  ★★ 副作用 B（★ 本轮新发现，**未在 §143.68 预见**）：**被 841 DROP 掉的月份
    若又有写入（补录 / 重放 / 时钟回拨 / 跨月补录）⇒ 不是 23514 硬失败，
    而是【静默落进 `default` 分区】** ⇒ ⇒
      ① 按月分区裁剪的「查某月」**查不到它**（它不在任何月分区里）；
      ② ★★ **被删月份的分区重建会被 default 分区的内容挡住**：
         `ERROR: updated partition constraint for default partition "…" would be
          violated by some row` (23514) ⇒ ⇒ **运维恢复路径被堵死**。

用法
----
    python3 scripts/audit/verify_841_session_family_retention_local.py
    python3 scripts/audit/verify_841_session_family_retention_local.py --keep

环境要求
--------
本机 docker 里有 `llm-gateway-pg`（PG 17.x）。脚本在该实例里建一次性库
`itgate_14369`，跑完即 DROP ⇒ **对生产零写入**。

★ 本脚本**不读 .env / 不连生产**：DSN 由 docker 内置的本地实例提供。
★ 841 迁移本体从仓内 `sql/migrations/startup/841_monthly_partition_retention.sql`
  读取 ⇒ **测的就是生产要用的那一份字节**。
"""

import argparse
import atexit
import os
import re
import signal
import subprocess
import sys

PG_CONTAINER = os.environ.get("ITGATE_PG_CONTAINER", "llm-gateway-pg")
DB = "itgate_14369"
PGUSER = "llm_gateway"
REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
MIG_841 = os.path.join(REPO, "sql", "migrations", "startup",
                        "841_monthly_partition_retention.sql")

RESULTS = []

# ★ psql 的 NOTICE / ERROR 都走 **stderr** ⇒ 判定「是否报错」必须看 stderr，
#   否则会把「成功」误判成「失败」（本轮 B1/B3 首跑就踩了这个量具坑）。
LAST_STDERR = ""


def psql(sql, db=DB, quiet=False):
    global LAST_STDERR
    cmd = ["docker", "exec", "-i", PG_CONTAINER, "psql", "-U", PGUSER, "-d", db,
           "-v", "ON_ERROR_STOP=0", "-P", "pager=off", "-tA", "-f", "-"]
    p = subprocess.run(cmd, input=sql, capture_output=True, text=True)
    LAST_STDERR = p.stderr
    if not quiet and p.stderr.strip():
        for line in p.stderr.splitlines():
            if "NOTICE" in line:
                print("   " + line.strip())
    return p.stdout.strip()


def check(name, ok, detail=""):
    RESULTS.append((name, ok, detail))
    print(("  [PASS] " if ok else "  [FAIL] ") + name + ("  " + detail if detail else ""))
    return ok


# ── 回收兜底（R99 形态 · 与 verify_archive_orphan_local.py 同形）────────────
# ★★ 原形态的缺口（三条，都是实测出来的，不是「看着像」）：
#   ① DROP 只在【正常跑完】时执行（原第 205 行 `if not args.keep:` 内）⇒
#      而第 84 / 97 行两个 `sys.exit(2)` **都发生在 CREATE DATABASE 之后**
#      ⇒ ⇒ 第 97 行那条是【确定的泄漏路径】：itgate_14369 已经建好才退出。
#      ⇒ 修法：atexit 钩子 ⇒ 覆盖 sys.exit / 未捕获异常 / 正常返回 三条路径。
#   ② 【信号路径】实测 python 的 atexit 在进程被 SIGTERM 终止时【不执行】
#      ⇒ ⇒ 被 CI / Ctrl-C / 编排器超时杀掉 ⇒ 库留下
#      ⇒ ⇒ 修法：显式接管 SIGTERM/SIGINT ⇒ 处理器【抛异常】
#      ⇒ ⇒ 异常沿栈展开 ⇒ finally 与 atexit 钩子【真的】跑起来。
#   ③ 【防卡】原 DROP 是【裸】的 ⇒ 遇到「还有连接没断干净」就无限期挂着
#      ⇒ ⇒ 修法：先 terminate 踢掉占用会话，再【带 statement_timeout】地 DROP。
#      ★★ 超时取 **120s** 而不是 15s：实测「terminate 之后 DROP 仍要等被
#        SIGTERM 的后端收尾」可达数十秒（PG 17.10 高负载下实测到 89.37s）
#        ⇒ ⇒ 15s 会把【合法】清理砍成「被超时取消」⇒ 而「清理失败」＝「库泄漏」
#        ⇒ ⇒ 与守卫的目的正好相反。
#      ★★ SET 与 DROP 必须落在【同一条逻辑命令】里、且 terminate 那条紧邻其前
#        ⇒ 这是本仓 cleanup 质量门的判据形状（照抄模板，别拆行）。
#   ⚠ 诚实边界：atexit 不覆盖 os._exit 与 SIGKILL。
_KEEP = False


def _cleanup_sql(sql, stop_on_error=True):
    """清理路径专用的 psql。

    ★ 不抛异常、★ **不碰 LAST_STDERR**（那是本脚本的量具变量，清理路径不许污染）。
    """
    cmd = ["docker", "exec", "-i", PG_CONTAINER, "psql", "-U", PGUSER, "-d", "postgres",
           "-v", "ON_ERROR_STOP=" + ("1" if stop_on_error else "0"), "-f", "-"]
    p = subprocess.run(cmd, input=sql, capture_output=True, text=True)
    return p.stdout + p.stderr


def _residual():
    """库里还有几个同名库（★ 清理是否成功只能【查落点】，不能看退出码）。

    实测 psql 在 stdin 形态（`-f -`）下【失败也 exit 0】⇒ ⇒ 退出码不是判据。
    """
    out = _cleanup_sql(f"SELECT 'LEFT=' || count(*) FROM pg_database WHERE datname = '{DB}';")
    m = re.search(r"LEFT=(\d+)", out)
    return m.group(1) if m else "?"


def drop_db():
    """回收一次性库：atexit 兜底与正常路径共用【同一个】实现 ⇒ 只留一份 DROP。"""
    if _KEEP:
        return ""
    try:
        _cleanup_sql(f"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '{DB}' AND pid <> pg_backend_pid();")
        out = _cleanup_sql(f"SET statement_timeout = '120s'; DROP DATABASE IF EXISTS {DB} WITH (FORCE);",
                           stop_on_error=True)
        left = _residual()
        if left != "0":
            out += f"\n⚠ 回收未完成：pg_database 里仍有 {left} 行匹配 {DB}"
        return out
    except Exception:  # noqa: BLE001 —— 回收失败不得盖掉真正的错误
        return ""


atexit.register(drop_db)


# ★ 信号接管 ⇒ 让上面的兜底在【信号路径】上也真的执行。
#   ★ 抛自定义 BaseException 而不是 KeyboardInterrupt：后者会被 subprocess.run
#     特殊对待，而这里要的是「异常展开（让 finally / atexit 真的跑）」。
class _Terminated(BaseException):
    """收到 SIGTERM / SIGINT ⇒ 抛出来让兜底回收真的执行。"""


def _on_signal(signum, _frame):
    raise _Terminated(f"收到信号 {signum}")


def _install_signal_handlers():
    """接管 SIGTERM / SIGINT ⇒ 走异常展开路径 ⇒ finally / atexit 真的跑。"""
    signal.signal(signal.SIGTERM, _on_signal)
    signal.signal(signal.SIGINT, _on_signal)


def main():
    global _KEEP
    ap = argparse.ArgumentParser()
    ap.add_argument("--keep", action="store_true", help="保留一次性库（调试用）")
    args = ap.parse_args()
    _KEEP = bool(args.keep)
    _install_signal_handlers()

    if not os.path.exists(MIG_841):
        print(f"找不到 841 迁移：{MIG_841}")
        sys.exit(2)

    print("== 0. 建一次性库 ==")
    psql(f"DROP DATABASE IF EXISTS {DB};", db="postgres")
    psql(f"CREATE DATABASE {DB};", db="postgres")

    print("== 1. 应用仓内 841 迁移（★ 测的就是生产那一份字节） ==")
    with open(MIG_841, encoding="utf-8") as fh:
        mig = fh.read()
    psql(mig)
    if psql("SELECT count(*) FROM pg_class WHERE relname='llm_gateway_partition_retention';",
            quiet=True) != "1":
        print("841 迁移未生效，放弃")
        sys.exit(2)

    print("== 2. 造【生产形态】的模拟会话族（父表 + 月分区 + default + 父表级 ONLY 索引 + 唯一约束） ==")
    setup = """
SET TIME ZONE 'Asia/Shanghai';
CREATE TABLE public.session_bodies (
    id bigint, session_id text, turn_no int, tenant_id text,
    request_id text, ts timestamptz, partition_date date, kind text
) PARTITION BY RANGE (partition_date);
CREATE INDEX idx_sb_request ON ONLY public.session_bodies (request_id);
CREATE UNIQUE INDEX session_bodies_tenant_request_partition_key
    ON ONLY public.session_bodies (tenant_id, request_id, partition_date);
CREATE UNIQUE INDEX session_bodies_tenant_session_turn_partition_key
    ON ONLY public.session_bodies (tenant_id, session_id, turn_no, partition_date);
"""
    for m in ("06", "07", "08", "09", "10", "11"):
        setup += (f"CREATE TABLE public.session_bodies_2026_{m} PARTITION OF public.session_bodies "
                  f"FOR VALUES FROM ('2026-{m}-01') TO ('2026-{int(m)+1:02d}-01');\n")
    setup += "CREATE TABLE public.session_bodies_default PARTITION OF public.session_bodies DEFAULT;\n"
    setup += """
INSERT INTO public.session_bodies (id, session_id, turn_no, tenant_id, request_id, ts, partition_date, kind) VALUES
 (1,'s01',1,'t1','r01','2026-06-15 10:00+08','2026-06-15','turn_delta'),
 (2,'s02',1,'t1','r02','2026-07-15 10:00+08','2026-07-15','turn_delta'),
 (3,'s03',1,'t1','r03','2026-08-15 10:00+08','2026-08-15','turn_delta'),
 (4,'s04',1,'t1','r04','2026-09-15 10:00+08','2026-09-15','turn_delta'),
 (5,'s04',2,'t1','r05','2026-09-20 10:00+08','2026-09-20','turn_delta'),
 (6,'s05',1,'t1','r06','2026-10-15 10:00+08','2026-10-15','turn_delta'),
 (7,'s06',1,'t1','r07','2026-11-15 10:00+08','2026-11-15','turn_delta'),
 (8,'s07',1,'t1','r08','2026-10-20 10:00+08','2026-10-20','turn_delta');
INSERT INTO public.session_bodies (id, session_id, turn_no, tenant_id, request_id, ts, partition_date, kind) VALUES
 (10,'s_boundary',1,'t1','r_boundary','2026-10-01 03:00+08','2026-09-30','turn_delta');
"""
    psql(setup)

    print("== 3. A1/A2 配置表为空 ⇒ 零删除安全态 ==")
    check("A1 expired_month_partitions() 返回 0 行",
          psql("SELECT count(*) FROM public.llm_gateway_expired_month_partitions();") == "0")
    dropped = psql("SELECT public.llm_gateway_drop_expired_month_partitions();")
    check("A2 drop 返回空数组", dropped in ("{}", ""), f"got={dropped!r}")
    check("A2 审计表 0 行",
          psql("SELECT count(*) FROM public.llm_gateway_partition_drop_log;") == "0")

    print("== 4. A3 启用 retain_months=2 ⇒ 过期集合 ==")
    psql("INSERT INTO public.llm_gateway_partition_retention (family, retain_months, enabled, note) "
         "VALUES ('session_bodies', 2, true, 'itgate_14369 实验');")
    exp = sorted(psql("SELECT partition_name FROM public.llm_gateway_expired_month_partitions() "
                      "ORDER BY partition_name;").splitlines())
    check("A3 过期集合 = {06,07,08}",
          exp == ["session_bodies_2026_06", "session_bodies_2026_07", "session_bodies_2026_08"],
          f"got={exp}")
    cur = psql("SELECT count(*) FROM public.llm_gateway_expired_month_partitions() "
               "WHERE partition_name IN ('session_bodies_2026_10','session_bodies_2026_11',"
               "'session_bodies_default');")
    check("A3 当月/未来月/default 都不在过期列表（★ 不可逆安全属性）", cur == "0", f"count={cur}")

    print("== 5. A4/A5 执行 DROP ⇒ 返回值 / 审计表 / 剩余分区 ==")
    dropped = psql("SELECT public.llm_gateway_drop_expired_month_partitions();")
    check("A4 drop 返回 3 个分区名",
          all(f"session_bodies_2026_{m}" in dropped for m in ("06", "07", "08")), f"got={dropped!r}")
    audit = psql("SELECT count(*) FROM public.llm_gateway_partition_drop_log;")
    check("A4 审计表 3 行", audit == "3", f"rows={audit}")
    left = sorted(psql("SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid=i.inhrelid "
                       "WHERE i.inhparent='public.session_bodies'::regclass;").splitlines())
    check("A5 剩余分区 = {09,10,11,default}",
          left == ["session_bodies_2026_09", "session_bodies_2026_10",
                   "session_bodies_2026_11", "session_bodies_default"], f"got={left}")
    surv = psql("SELECT count(*) FROM public.session_bodies WHERE id=10;")
    check("A6 8 小时边界行在 retain_months=2 时存活（它所在 9 月分区不过期）", surv == "1", f"rows={surv}")

    print("== 6. A7 幂等 ==")
    dropped2 = psql("SELECT public.llm_gateway_drop_expired_month_partitions();")
    audit2 = psql("SELECT count(*) FROM public.llm_gateway_partition_drop_log;")
    check("A7 第二次 drop 返回空", dropped2 in ("{}", ""), f"got={dropped2!r}")
    check("A7 审计表不增", audit2 == "3", f"rows={audit2}")

    print("== 7. ★★ 副作用 B：被 DROP 的月份『再写入』会怎样 ==")
    psql("INSERT INTO public.session_bodies (id, session_id, turn_no, tenant_id, request_id, ts, partition_date, kind) "
         "VALUES (99,'s_late',1,'t1','r_late','2026-07-20 10:00+08','2026-07-20','turn_delta');")
    b1_err = LAST_STDERR
    check("B1 写入被 DROP 的月份【不是】23514 硬失败（★ stderr 无 ERROR）",
          "ERROR" not in b1_err, b1_err.replace("\n", " ")[:200])
    in_default = psql("SELECT count(*) FROM public.session_bodies_default WHERE partition_date='2026-07-20';")
    check("B2 它静默落进了 default 分区（★ 查某月时按分区裁剪查不到）", in_default == "1", f"rows={in_default}")

    print("== 8. ★★ 副作用 B 的第二段：被删月份的分区重建能否恢复 ==")
    psql("CREATE TABLE public.session_bodies_2026_07 PARTITION OF public.session_bodies "
         "FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');")
    b3_err = LAST_STDERR
    # ★ psql 默认【不打印 SQLSTATE】⇒ 只能按特征文本判定
    #   （`updated partition constraint for default partition ... would be violated by some row`
    #    是 PG 的 23514 partition constraint 冲突）。
    check("B3 重建被 default 内容挡住的月份 ⇒ 23514 ⇒ 运维恢复路径被堵",
          "would be violated by some row" in b3_err, b3_err.replace("\n", " ")[:220])

    print("== 9. ★★ 副作用 A：边界那 8 小时的真实淘汰时刻（retain_months=1） ==")
    psql("UPDATE public.llm_gateway_partition_retention SET retain_months=1 WHERE family='session_bodies';")
    exp1 = psql("SELECT partition_name FROM public.llm_gateway_expired_month_partitions();")
    check("A8 retain_months=1 ⇒ 9 月分区被列为过期", "session_bodies_2026_09" in exp1, exp1)
    psql("SELECT public.llm_gateway_drop_expired_month_partitions();")
    b = psql("SELECT count(*) FROM public.session_bodies WHERE id=10;")
    check("A9 ★ 边界行随 9 月分区一起消失 ⇒ 比同月其它数据早 8 小时淘汰", b == "0", f"rows={b}")
    left2 = sorted(psql("SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid=i.inhrelid "
                        "WHERE i.inhparent='public.session_bodies'::regclass;").splitlines())
    check("A10 10 月/11 月/default 仍在", left2 == ["session_bodies_2026_10",
                                                    "session_bodies_2026_11",
                                                    "session_bodies_default"], f"got={left2}")

    if not args.keep:
        # ★ 与 atexit 兜底【同一个】实现（drop_db）⇒ 只留一份 DROP 实现，不漂移；
        #   且它自带 terminate + statement_timeout + 查落点。
        warn = drop_db()
        if warn:
            print(warn)
        print(f"\n== 一次性库 {DB} 已 DROP ==")

    failed = [n for n, ok, _ in RESULTS if not ok]
    print(f"\n== 结果：{len(RESULTS)-len(failed)}/{len(RESULTS)} 通过 ==")
    if failed:
        for n in failed:
            print("  FAILED:", n)
        sys.exit(1)
    print("== 全部通过 ==")


if __name__ == "__main__":
    try:
        main()
    except _Terminated as e:
        # ★ 信号路径：异常已沿栈展开 ⇒ atexit 兜底已真的跑过（库已回收）
        print(f"⚠ {e} ⇒ 兜底回收已执行（atexit）", file=sys.stderr)
        sys.exit(143)
