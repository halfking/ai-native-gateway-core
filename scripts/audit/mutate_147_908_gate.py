#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""mutate_147_908_gate.py —— 908 门变异验证（R147 存储效率续轮）。

纪律（LESSONS §302 / §301 族）：
  * 每个变异：备份 → 改 → 只跑 908 门 → 断言红 → 按原路径回拷 → md5 逐字校验；
  * 替换串不得包含 needle 作为子串（否则变异是假变异）；
  * 备份不删（审计留痕），但还原后源文件必须与备份逐字节一致。
"""
import hashlib
import subprocess
import sys
import shutil
import os

# ★★ r1047 轮（★ 2026-10-28）：★ 子进程走【专用 GOCACHE】⇒ ⇒ 不再往用户的全局缓存里
# ★★ 写「随树变化而失效」的一次性产物（★ 理由与实测读数见 `scripts/lib/gocache.py`
# ★★ 与 `internal/gocache` 的包注释）。
# ★★ **两条路径都试**（★ ★★ 深度无关：★ 本文件在 scripts/ 或 scripts/audit/ 下）★★
for _gc_lib in (os.path.join(os.path.dirname(os.path.abspath(__file__)), "lib"),
                os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "lib")):
    if os.path.isfile(os.path.join(_gc_lib, "gocache.py")):
        sys.path.insert(0, _gc_lib)
        break
import gocache  # noqa: E402
gocache.apply_to_environ("mutate")

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
GATE_TEST = "TestMigration908"

MUTATIONS = [
    # (标签, kind, 目标文件(相对), needle, 替换串)
    # kind='drop'：替换后 needle 必须消失（防假变异：repl 含 needle ⇒ BLOCKED）
    # kind='insert'：repl = needle + 追加内容（纯插入，needle 留在原位是预期的）
    ("M1_TSV行移除", "drop", "sql/schema/installed_startup_migrations.tsv",
     "310\t908_route_incident_events_type_created_drop.sql\n", ""),
    ("M2_runner登记移除", "drop", "installer/internal/dbinit/runner.go",
     '"908_route_incident_events_type_created_drop.sql",',
     '"908_x_never_shipped.sql",'),
    ("M3_embeddata副本漂移", "drop", "installer/cmd/llm-gw-installer/embeddata/startup/908_route_incident_events_type_created_drop.sql",
     "DROP INDEX CONCURRENTLY IF EXISTS public.idx_route_incident_events_type_created;",
     "DROP INDEX CONCURRENTLY IF EXISTS public.idx_x_never;"),
    ("M4_迁移体抽lock_timeout", "drop", "sql/migrations/startup/908_route_incident_events_type_created_drop.sql",
     "SET lock_timeout = '60s';\n", ""),
    ("M5_台账行移除", "drop", "docs/db-changelog.md",
     "| 908 | `908_route_incident_events_type_created_drop.sql`",
     "| 90X | `908_x_never.sql`"),
    ("M6_db_go复活CREATE", "insert", "db/db.go",
     "\t\tCREATE INDEX IF NOT EXISTS idx_route_incident_events_incident_created\n\t\t\tON route_incident_events (incident_id, created_at DESC);",
     "\t\tCREATE INDEX IF NOT EXISTS idx_route_incident_events_incident_created\n\t\t\tON route_incident_events (incident_id, created_at DESC);\n\t\tCREATE INDEX IF NOT EXISTS idx_route_incident_events_type_created\n\t\t\tON route_incident_events (event_type, created_at DESC);"),
]


def md5(p):
    with open(p, "rb") as f:
        return hashlib.md5(f.read()).hexdigest()


def run_gate():
    r = subprocess.run(
        ["go", "test", "./sql/migrations/startup/", "-run", GATE_TEST, "-count=1"],
        cwd=ROOT, capture_output=True, text=True)
    return r.returncode, r.stdout + r.stderr


def main():
    failures = []
    for label, kind, rel, needle, repl in MUTATIONS:
        if repl == needle:
            print(f"[BLOCKED] {label}: 替换串与 needle 相同 —— 无变化，拒跑")
            failures.append(label)
            continue
        if kind == "drop" and needle in repl:
            print(f"[BLOCKED] {label}: drop 型替换串包含 needle 作为子串 —— 假变异，拒跑")
            failures.append(label)
            continue
        if kind == "insert" and not repl.startswith(needle):
            print(f"[BLOCKED] {label}: insert 型替换串必须以 needle 为前缀（纯插入）")
            failures.append(label)
            continue
        path = os.path.join(ROOT, rel)
        bak = path + ".mut147bak"
        shutil.copy2(path, bak)
        orig_md5 = md5(bak)
        with open(path, "r", encoding="utf-8") as f:
            src = f.read()
        if needle not in src:
            print(f"[SKIP-RED] {label}: needle 不在文件里（门已应红）—— 直接跑门验证")
        else:
            with open(path, "w", encoding="utf-8") as f:
                f.write(src.replace(needle, repl, 1))
        rc, out = run_gate()
        if rc == 0:
            print(f"[FAIL] {label}: 变异后门仍绿 —— 门没有牙")
            failures.append(label)
        else:
            red_lines = [ln for ln in out.splitlines() if "--- FAIL" in ln or ".go:" in ln and "908" in ln]
            print(f"[RED] {label}: 门按预期红（exit={rc}，失败测试 {len(red_lines)} 行）")
        shutil.copy2(bak, path)
        if md5(path) != orig_md5:
            print(f"[FATAL] {label}: 还原后 md5 不一致 —— {path} 处于变异态！")
            failures.append(label + "(restore)")
        os.remove(bak)

    if failures:
        print("FAILED:", failures)
        sys.exit(1)
    print("ALL MUTATIONS RED + RESTORED VERBATIM")


if __name__ == "__main__":
    main()
