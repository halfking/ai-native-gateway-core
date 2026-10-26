#!/usr/bin/env python3
"""同窗口 WAL 覆盖率差分器（§143.50，2026-10-21）。

用法：python3 scripts/audit/pss_wal_same_window_coverage.py <t0.log> <t1.log>

★ 为什么必须有这个工具（而不是一条 SQL 自比）：
  ① `pg_stat_wal` 的统计快照**按事务缓存** ⇒ 同一事务内读两次差值恒为 0
     ⇒ 两次采样必须落在**两个不同事务**（本仓配套的 `pss_wal_snapshot.sql`
     用两条独立语句实现，psql 自动提交 ⇒ 独立事务）。
  ② `pg_stat_statements` 只有累计值 ⇒ 同样只能靠两次快照做差。
  ③ ★ 覆盖率必须**同窗口**：`pg_stat_statements_info.stats_reset` 与
     `pg_stat_wal.stats_reset` 实测**不同**（2.42 天 vs 10.66 天）
     ⇒ ⇒ 拿两个累计值相除是错的比率
     （§143.49 据此得出的「覆盖率 16.23%」已在 §143.50 订正横幅二十七就地作废）。

★ 本工具只读本地文件，不连接数据库。
"""
import re
import sys

PSS_RE = re.compile(
    r"^\s*(-?\d+)\s*\|\s*(\d+)\s*\|\s*(\d+)\s*\|\s*([tf])\s*\|\s*(\d+)\s*\|\s*(\d+)\s*\|\s*(\d+)\s*\|\s*([\d.]+)\s*\|?\s*$"
)


def _sections(path):
    """按快照文件里的段标记切分 ⇒ ★ 避免「读到别的段的数据行」。

    ★ 为什么要切分：pss 快照的每一行都含多个大数字（queryid / calls / wal_bytes），
    若不限定段，WAL 解析器会把 **pss 的某一行**当成 WAL 快照
    ⇒ ⇒ 增量恒为 0 ⇒ ⇒ 【实测踩过：T0/T1 各 4,900+ 行，WAL 增量算出 0】。
    ⇒ ⇒ 所以本工具与 `pss_wal_snapshot.sql` 的段名必须成对。
    """
    cur = None
    out = {}
    for line in open(path, encoding="utf-8", errors="replace"):
        s = line.strip()
        if s.startswith("### "):
            cur = s[4:].strip()
            out.setdefault(cur, [])
            continue
        if cur:
            out[cur].append(line)
    return out


def parse_pss(path):
    """⇒ {(queryid, dbid, userid, toplevel): (calls, rows, wal_bytes)}"""
    out = {}
    for line in _sections(path).get("PSS_SNAPSHOT", []):
        m = PSS_RE.match(line.replace(",", ""))
        if not m:
            continue
        qid, dbid, uid, top, calls, rows, wal = m.groups()[:7]
        out[(int(qid), int(dbid), int(uid), top)] = (int(calls), int(rows), int(wal))
    return out


def parse_wal(path):
    """⇒ (wal_bytes_total, wal_records, wal_fpi) —— ★ 只取 WAL_SNAPSHOT 段"""
    for line in _sections(path).get("WAL_SNAPSHOT", []):
        if "|" not in line:
            continue
        nums = re.findall(r"\b\d{4,}\b", line.replace(",", ""))
        if len(nums) >= 3:
            return tuple(int(x) for x in nums[-3:])
    raise SystemExit("WAL 快照解析失败（未找到 WAL_SNAPSHOT 段的数据行）: " + path)


def main():
    if len(sys.argv) != 3:
        print(__doc__)
        sys.exit(2)
    p0, p1 = sys.argv[1], sys.argv[2]
    t0, t1 = parse_pss(p0), parse_pss(p1)
    w0, w1 = parse_wal(p0), parse_wal(p1)

    actual = w1[0] - w0[0]
    rec_d = w1[1] - w0[1]
    fpi_d = w1[2] - w0[2]

    pss_all = 0
    pss_cur = 0
    new_entries = 0
    regressed = 0
    for k, v in t1.items():
        old = t0.get(k)
        if old is None:
            new_entries += 1
            d = v[2]
        else:
            d = v[2] - old[2]
        if d < 0:
            regressed += -d
            d = 0
        pss_all += d
        if k[1] == 16384:  # llm_gateway 的 dbid（实测固定）
            pss_cur += d

    print("pss 条目：T0=%d  T1=%d  （新增 %d，回退 %d B）" % (len(t0), len(t1), new_entries, regressed))
    print("WAL 增量：bytes=%d (%.3f MB)  records=%d  fpi=%d" % (actual, actual / 1048576.0, rec_d, fpi_d))
    print()
    if actual <= 0:
        print("★ 窗口内实际 WAL 增量 <= 0 ⇒ 换个更长的窗口再测（本次读数不成立）")
        sys.exit(3)
    print("  实际 WAL 增量 (pg_stat_wal)     = %14d B" % actual)
    print("  pss 记账增量（全库）             = %14d B" % pss_all)
    print("  pss 记账增量（本库 dbid=16384）  = %14d B" % pss_cur)
    print()
    print("  ⇒ 同窗口覆盖率（全库 pss）= %.2f%%" % (100.0 * pss_all / actual))
    print("  ⇒ 同窗口覆盖率（本库 pss）= %.2f%%" % (100.0 * pss_cur / actual))
    print("  ⇒ 未被 pss 记账的部分          = %d B (%.3f MB) = %.2f%%" % (
        actual - pss_all, (actual - pss_all) / 1048576.0, 100.0 * (actual - pss_all) / actual))
    print()
    print("  ★ 判读纪律：")
    print("    · 同窗口覆盖率越高 ⇒ pss 的语句级 WAL 排名【越可用】")
    print("    · 未记账部分【尚未定性】（候选：checkpoint 触发的 FPI、非语句级写入）；")
    print("      ★ 若「未记账绝对量」在不同窗口相近、而 FPI 数不同 ⇒ 不能简单归因给 FPI")
    print("    · ★★ 严禁用【跨窗口累计】相除来算覆盖率（pss 窗口 ≠ wal 窗口）")


if __name__ == "__main__":
    main()
