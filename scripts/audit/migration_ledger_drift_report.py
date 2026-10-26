#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""migration_ledger_drift_report.py — 迁移台账漂移只读报告器

为什么需要这个工具
------------------
部署通道的 `_deploy_migration_checksum_reconcile`
(`scripts/deploy-lib.legacy/db-changelog.sh`) 会对每个**已应用**的迁移做
「本地文件名 + SHA-256」对账，缺行 / 文件名不符 / 校验和不符一律 fail closed。
但 R75 轮实测发现：**那道闸门在生产上从未真正跑过**（详见
`docs/audit/2026-10-24-R75-台账对账闸门失效与D4后置基线.md`）——
它的 `while read` 从进程替换（管道）读版本列表，而循环体内的 `ssh_run`
用命令替换捕获远端输出 ⇒ ssh 继承了循环的 stdin 并把它读空 ⇒ 循环在
**第一次迭代后**就 EOF ⇒ 443 个已应用迁移里**只校验了第 412 号**。

⇒ ⇒ 因此「台账有没有漂移」这件事必须有一个**独立于部署通道**的读数手段。
本工具就是这个手段：**只读、离线、不碰凭据**（输入是两个 TSV 转储）。

判定口径（逐字复刻对账函数的判定顺序）
--------------------------------------
1. canonical 候选集 = `sql/migrations/startup/<数字>*.sql`，排除
   `*.down.sql` / `*.skip` / `*.bak.skip`，再排除**前 15 行**里出现
   SUPERSEDED / superceded / DEPRECATED / deprecated 的文件；
2. canonical 解析 = 先按「文件名后缀 == description」或「文件名 == description」
   匹配；匹配不到但**候选恰好只有 1 个**时取该候选；否则判
   `CANONICAL_UNRESOLVED`（部署侧同样的单候选兜底见 db-changelog.sh）；
3. 账本行 = `llm_gateway_migration_checksums` 里 `version` 相等的那一行，
   取 `migration_name` 与 `checksum`；
4. 逐版本判定：
   - 无账本行                     ⇒ `MISSING_LEDGER`
   - `migration_name` != 本地文件名 ⇒ `NAME_MISMATCH`
   - `checksum` != 本地文件 SHA-256 ⇒ `CHECKSUM_MISMATCH`
   - 否则                         ⇒ `OK`

用法
----
    # 1) 在库所在机器上导出两个 TSV（只读）
    psql "$DSN" -At -c "SELECT version || '|' || COALESCE(description,'') || '|' || applied_at \
                        FROM schema_migrations WHERE version ~ '^[0-9]+$' ORDER BY version::int" \
      > /tmp/applied.tsv
    psql "$DSN" -At -c "SELECT version || '|' || migration_name || '|' || checksum \
                        FROM llm_gateway_migration_checksums ORDER BY version::int" \
      > /tmp/ledger.tsv

    # 2) 在仓库里跑报告
    python3 scripts/audit/migration_ledger_drift_report.py \
        --applied /tmp/applied.tsv --ledger /tmp/ledger.tsv --boundary 412

    # 3) 自检（离线 fixture，CI/门禁用）
    python3 scripts/audit/migration_ledger_drift_report.py --self-test

退出码：恒 0（**只读报告器不是闸门**）；`--fail-on-drift` 时有漂移则退 1。
"""

import argparse
import datetime
import hashlib
import os
import re
import subprocess
import sys

MIGRATION_DIR = "sql/migrations/startup"
SUPERSEDED_RE = re.compile(r"superseded|superceded|deprecated", re.I)
# 部署侧对账函数里的同款特例（db-changelog.sh: case "$ver:$description"）
DESCRIPTION_OVERRIDES = {
    ("461", "request_wal_hot_request_id_unique"): "request_wal_hot_unique_request_id",
}

OK = "OK"
MISSING_LEDGER = "MISSING_LEDGER"
NAME_MISMATCH = "NAME_MISMATCH"
CHECKSUM_MISMATCH = "CHECKSUM_MISMATCH"
CANONICAL_UNRESOLVED = "CANONICAL_UNRESOLVED"


def sha256_file(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def load_candidates(repo_root):
    """复刻对账函数的 canonical 候选集构建。"""
    mig = os.path.join(repo_root, MIGRATION_DIR)
    candidates = {}
    if not os.path.isdir(mig):
        return candidates
    for name in sorted(os.listdir(mig)):
        if not re.match(r"^[0-9].*\.sql$", name):
            continue
        if name.endswith(".down.sql") or name.endswith(".skip") or name.endswith(".bak.skip"):
            continue
        try:
            with open(os.path.join(mig, name), encoding="utf-8", errors="replace") as fh:
                head = fh.read().splitlines()[:15]
        except OSError:
            continue
        if any(SUPERSEDED_RE.search(line) for line in head):
            continue
        ver = name.split("_")[0]
        candidates.setdefault(ver, []).append(name)
    return candidates


def resolve_canonical(candidates, ver, description):
    """复刻对账函数的 canonical 解析；返回文件名或 None。"""
    description = DESCRIPTION_OVERRIDES.get((ver, description), description)
    pool = candidates.get(ver, [])
    for name in pool:
        suffix = name[len(ver) + 1:-4]
        if suffix == description or name[:-4] == description:
            return name
    if len(pool) == 1:
        return pool[0]
    return None


def _split_cols(line):
    """分列：**优先 TAB**（description 里可能含 '|'，TAB 更安全），否则退回 '|'。"""
    if "\t" in line:
        return line.split("\t")
    return line.split("|")


def parse_applied(path):
    """解析 schema_migrations 转储：version|description|applied_at"""
    rows = {}
    with open(path, encoding="utf-8") as fh:
        for raw in fh:
            line = raw.rstrip("\n")
            if not line or "|" not in line and "\t" not in line:
                continue
            parts = _split_cols(line)
            ver = parts[0].strip()
            if not re.match(r"^[0-9]+$", ver):
                continue
            description = parts[1] if len(parts) > 1 else ""
            applied_at = parts[2] if len(parts) > 2 else ""
            rows[ver] = (description, applied_at)
    return rows


def parse_ledger(path):
    """解析 llm_gateway_migration_checksums 转储：version|migration_name|checksum"""
    rows = {}
    if not path or not os.path.exists(path):
        return rows
    with open(path, encoding="utf-8") as fh:
        for raw in fh:
            line = raw.rstrip("\n")
            if not line or "|" not in line and "\t" not in line:
                continue
            parts = _split_cols(line)
            ver = parts[0].strip()
            if not ver:
                continue
            name = parts[1] if len(parts) > 1 else ""
            checksum = parts[2] if len(parts) > 2 else ""
            rows[ver] = (name, checksum)
    return rows


def parse_ts(s):
    s = (s or "").strip().replace(" ", "T")
    s = re.sub(r"([+-]\d{2})$", r"\1:00", s)
    m = re.match(r"^(.*?)\.(\d+)(.*)$", s)
    if m:
        s = m.group(1) + "." + (m.group(2) + "000000")[:6] + m.group(3)
    try:
        return datetime.datetime.fromisoformat(s)
    except ValueError:
        return None


def git_blob_sha(repo_root, commit, rel_path):
    try:
        out = subprocess.run(
            ["git", "-C", repo_root, "show", "%s:%s" % (commit, rel_path)],
            capture_output=True,
        )
    except OSError:
        return None
    if not out.stdout:
        return None
    return hashlib.sha256(out.stdout).hexdigest()


def applied_blob_sha(repo_root, rel_path, applied_at):
    """取「应用时刻之前最后一次提交」的文件字节指纹（= 生产实际应用的字节）。"""
    if not applied_at:
        return None, None
    cutoff = parse_ts(applied_at)
    if cutoff is None:
        return None, None
    try:
        log = subprocess.run(
            ["git", "-C", repo_root, "log", "--format=%H|%cI", "--", rel_path],
            capture_output=True, text=True,
        ).stdout.strip().splitlines()
    except OSError:
        return None, None
    for line in log:
        if "|" not in line:
            continue
        commit, date = line.split("|", 1)
        when = parse_ts(date)
        if when is None:
            continue
        if when <= cutoff:
            return commit, git_blob_sha(repo_root, commit, rel_path)
    return None, None


def classify(repo_root, applied, ledger, boundary):
    candidates = load_candidates(repo_root)
    verdict = {}
    for ver in sorted(applied, key=lambda v: int(v)):
        if int(ver) < boundary:
            continue
        description, applied_at = applied[ver]
        name = resolve_canonical(candidates, ver, description)
        if name is None:
            verdict[ver] = (CANONICAL_UNRESOLVED, description, "", applied_at, [])
            continue
        rel = os.path.join(MIGRATION_DIR, name)
        local = sha256_file(os.path.join(repo_root, rel))
        if ver not in ledger:
            verdict[ver] = (MISSING_LEDGER, name, "", applied_at, [rel])
            continue
        rec_name, rec_sum = ledger[ver]
        if rec_name != name:
            verdict[ver] = (NAME_MISMATCH, name, rec_name, applied_at, [rel])
            continue
        if rec_sum != local:
            commit, blob = applied_blob_sha(repo_root, rel, applied_at)
            notes = []
            if blob is None:
                notes.append("APPLIED_BYTES_UNKNOWN")
            elif blob == rec_sum:
                notes.append("LEDGER_EQ_APPLIED_BYTES")
            elif blob == local:
                notes.append("LOCAL_EQ_APPLIED_BYTES")
            else:
                notes.append("NEITHER_EQ_APPLIED_BYTES")
            if commit:
                notes.append("applied_blob_commit=%s" % commit[:12])
            verdict[ver] = (CHECKSUM_MISMATCH, name, rec_sum, applied_at, notes + [rel])
            continue
        verdict[ver] = (OK, name, rec_sum, applied_at, [])
    return verdict


def render(verdict, repo_root, boundary=None):
    buckets = {OK: [], MISSING_LEDGER: [], NAME_MISMATCH: [], CHECKSUM_MISMATCH: [], CANONICAL_UNRESOLVED: []}
    for ver, item in verdict.items():
        if not isinstance(item, tuple):
            continue
        buckets[item[0]].append((ver, item))
    total = sum(len(v) for v in buckets.values())
    print("== 迁移台账漂移报告（只读）==")
    print("边界 version >= %s；判定条目 = %d" % (boundary, total))
    for key in (OK, MISSING_LEDGER, NAME_MISMATCH, CHECKSUM_MISMATCH, CANONICAL_UNRESOLVED):
        print("  %-20s = %d" % (key, len(buckets[key])))
    for key in (MISSING_LEDGER, NAME_MISMATCH, CHECKSUM_MISMATCH, CANONICAL_UNRESOLVED):
        if not buckets[key]:
            continue
        print("\n-- %s --" % key)
        for ver, item in sorted(buckets[key], key=lambda t: int(t[0])):
            print("  %-5s %s" % (ver, " | ".join(str(x) for x in item[1:] if x != "")))
    return buckets


def self_test():
    """离线 fixture 自检：分类器的判定顺序必须与部署侧一致。"""
    import tempfile

    tmp = tempfile.mkdtemp(prefix="ledger-drift-selftest-")
    mig = os.path.join(tmp, MIGRATION_DIR)
    os.makedirs(mig)
    with open(os.path.join(mig, "500_ok_file.sql"), "w") as fh:
        fh.write("SELECT 1;\n")
    with open(os.path.join(mig, "501_missing.sql"), "w") as fh:
        fh.write("SELECT 1;\n")
    with open(os.path.join(mig, "502_mismatch.sql"), "w") as fh:
        fh.write("SELECT 1;\n")
    # 503：库里有「已应用」但仓库里**没有对应文件** ⇒ canonical 解析不出来
    with open(os.path.join(mig, "504_superseded.sql"), "w") as fh:
        fh.write("-- DEPRECATED: replaced by 505\nSELECT 1;\n")
    # 505：仓库里只有 .down.sql（up 体已被删）⇒ 同样解析不出 canonical
    with open(os.path.join(mig, "505_down_only.sql.down.sql"), "w") as fh:
        fh.write("SELECT 1;\n")

    applied = {
        "500": ("ok_file", ""),
        "501": ("missing", ""),
        "502": ("mismatch", ""),
        "503": ("orphan", ""),
        "504": ("superseded", ""),
        "505": ("down_only", ""),
    }
    ledger = {
        "500": ("500_ok_file.sql", sha256_file(os.path.join(mig, "500_ok_file.sql"))),
        "502": ("502_mismatch.sql", "0" * 64),
        "504": ("504_superseded.sql", "0" * 64),
    }
    verdict = classify(tmp, applied, ledger, 500)
    expect = {
        "500": OK,
        "501": MISSING_LEDGER,
        "502": CHECKSUM_MISMATCH,
        "503": CANONICAL_UNRESOLVED,
        "504": CANONICAL_UNRESOLVED,   # 被 SUPERSEDED 头注排除 ⇒ 无候选
        "505": CANONICAL_UNRESOLVED,   # 只有 .down.sql ⇒ 无候选
    }
    bad = []
    for ver, want in expect.items():
        got = verdict.get(ver, (None,))[0]
        if got != want:
            bad.append("%s: want=%s got=%s" % (ver, want, got))
    # 边界过滤：低于 boundary 的版本不参与判定
    verdict2 = classify(tmp, applied, ledger, 501)
    if "500" in verdict2:
        bad.append("boundary 过滤失效：500 被判了")
    if bad:
        print("SELF-TEST FAILED:")
        for b in bad:
            print("  -", b)
        return 1
    print("SELF-TEST OK：%d 条 fixture 判定全部符合预期" % len(expect))
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(description="迁移台账漂移只读报告器")
    ap.add_argument("--repo-root", default=os.getcwd())
    ap.add_argument("--applied", help="schema_migrations 转储 TSV")
    ap.add_argument("--ledger", help="llm_gateway_migration_checksums 转储 TSV")
    ap.add_argument("--boundary", type=int, default=412, help="对账边界（与 DB_LEDGER_RECONCILE_FROM 同义）")
    ap.add_argument("--fail-on-drift", action="store_true", help="有漂移时退 1")
    ap.add_argument("--self-test", action="store_true", help="跑离线 fixture 自检")
    args = ap.parse_args(argv)

    if args.self_test:
        return self_test()

    if not args.applied:
        ap.error("需要 --applied（或用 --self-test）")

    repo_root = os.path.abspath(args.repo_root)
    applied = parse_applied(args.applied)
    ledger = parse_ledger(args.ledger)
    verdict = classify(repo_root, applied, ledger, args.boundary)
    buckets = render(verdict, repo_root, args.boundary)
    drift = len(buckets[MISSING_LEDGER]) + len(buckets[NAME_MISMATCH]) \
        + len(buckets[CHECKSUM_MISMATCH]) + len(buckets[CANONICAL_UNRESOLVED])
    print("\n漂移合计 = %d（OK = %d）" % (drift, len(buckets[OK])))
    if args.fail_on_drift and drift:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
