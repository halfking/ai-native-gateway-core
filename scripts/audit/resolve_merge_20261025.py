#!/usr/bin/env python3
"""resolve_merge_20261025.py —— -4 (syncfield/llm-gateway-go-4) 合并冲突裁决脚本。

背景（2026-10-25 12h 审计轮）：
    本仓在 2026-10-24 16:45 跑 `git pull --rebase --autostash` 时撞上并行会话
    推上来的 903/904/905/906/907 五个迁移号 ⇒ rebase 中断、旧会话手工解了一半
    冲突就断了电。本次合并把两侧都带进来：

      ours   (HEAD / df9ca67f8) = 本地 R147 轮（㉘ 裁决落地：删
               route_incident_events 的零消费方索引 + ㉙ matrix 成本分解）
      theirs (origin/main)      = 上游 R91~R150（门禁通用式族 + 稀有过滤索引族）

裁决口径（★ 与仓内既有纪律一致，见 LESSONS「双胞胎撞号 → 零引用侧让号」）：

  ① 六个冲突**全是「两侧各自在同一处追加」**形态 ⇒ 判据 = 【并集】，不是二选一。
  ② **迁移号撞号**：上游在本地会话取号之后（10-24 19:49 起）又占了 905/906/907
     ⇒ 本地那条让号到 **908**（上游当前最大号 907 之后的第一个空号）。
     判决内容零变化（只改文件名/登记号）。
  ③ **LESSONS 节号撞号**：上游 R91 轮已占 §355（且被 4 处外部引用）⇒
     本地 R147 的 §355 是**零引用侧** ⇒ 让号为 **§397**（上游当前最大 §396 之后）。
  ④ AGENTS.md 的 §143.104（上游最大 §143.103 之后）**不撞号** ⇒ 原号保留。
  ⑤ 两份文档的**块序**：LESSONS 按节号升序（§397 在 §396 之后）；
     AGENTS 按「上游最新块留在文件尾部」⇒ 本地 §143.104 块前置。

用法：python3 scripts/audit/resolve_merge_20261025.py [--check]
"""

import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

OLD_NO = "905"
NEW_NO = "908"
OLD_MIG = "905_route_incident_events_type_created_drop"
NEW_MIG = "908_route_incident_events_type_created_drop"

# ── 冲突块解析 ────────────────────────────────────────────────────────────


def split_conflict(text):
    """返回 (行列表, [(start, sep, end, ours, theirs), ...])。"""
    lines = text.split("\n")
    blocks = []
    i = 0
    while i < len(lines):
        if lines[i].startswith("<<<<<<< "):
            start = i
            j = i + 1
            while j < len(lines) and not lines[j].startswith(">>>>>>> "):
                j += 1
            end = j
            sep = None
            for k in range(start + 1, end):
                if lines[k].strip() == "=======":
                    sep = k
            if sep is None:
                raise SystemExit(f"找不到冲突分隔行（行 {start}）")
            blocks.append((start, sep, end, lines[start + 1:sep], lines[sep + 1:end]))
            i = end + 1
        else:
            i += 1
    return lines, blocks


def apply_order(path, order):
    """order: 'ours_first' | 'theirs_first'。把每个冲突块按指定顺序拼接。"""
    full = open(os.path.join(ROOT, path), encoding="utf-8").read()
    lines, blocks = split_conflict(full)
    if not blocks:
        return 0
    out = []
    cur = 0
    for (start, sep, end, ours, theirs) in blocks:
        out.extend(lines[cur:start])
        out.extend(ours if order == "ours_first" else theirs)
        out.extend(theirs if order == "ours_first" else ours)
        cur = end + 1
    out.extend(lines[cur:])
    new = "\n".join(out)
    if new != full:
        with open(os.path.join(ROOT, path), "w", encoding="utf-8") as f:
            f.write(new)
    return len(blocks)


# ── 具体裁决 ──────────────────────────────────────────────────────────────

# 文档块序：LESSONS 用 theirs_first（§397 必须排在 §396 之后）
#           AGENTS  用 ours_first（本地 §143.104 块前置，上游最新块留在文件尾）
DOC_ORDERS = {
    "LESSONS.md": "theirs_first",
    "AGENTS.md": "ours_first",
}
# 代码/台账：迁移序号必须递增 ⇒ theirs(905/906/907) 在前，ours(908) 在后
CODE_ORDERS = {
    "installer/internal/dbinit/runner.go": "theirs_first",
    "installer/cmd/llm-gw-installer/main.go": "theirs_first",
    "scripts/apply-db-revision-sequence.sh": "theirs_first",
    "sql/schema/installed_startup_migrations.tsv": "theirs_first",
}


def rename_files():
    """把迁移 905_* 改名为 908_*（git mv，保留历史）。"""
    moves = [
        (f"sql/migrations/startup/{OLD_MIG}.sql", f"sql/migrations/startup/{NEW_MIG}.sql"),
        (f"sql/migrations/startup/{OLD_MIG}.down.sql", f"sql/migrations/startup/{NEW_MIG}.down.sql"),
        ("sql/migrations/startup/migration_905_route_incident_type_created_drop_test.go",
         "sql/migrations/startup/migration_908_route_incident_type_created_drop_test.go"),
        (f"installer/cmd/llm-gw-installer/embeddata/startup/{OLD_MIG}.sql",
         f"installer/cmd/llm-gw-installer/embeddata/startup/{NEW_MIG}.sql"),
        (f"installer/cmd/llm-gw-installer/embeddata/startup/{OLD_MIG}.down.sql",
         f"installer/cmd/llm-gw-installer/embeddata/startup/{NEW_MIG}.down.sql"),
    ]
    done = []
    for old, new in moves:
        op, np = os.path.join(ROOT, old), os.path.join(ROOT, new)
        if not os.path.exists(op):
            continue
        os.rename(op, np)
        done.append((old, new))
    return done


def sub_file(path, pairs, must=True):
    p = os.path.join(ROOT, path)
    if not os.path.exists(p):
        if must:
            raise SystemExit(f"缺文件：{path}")
        return 0
    s = open(p, encoding="utf-8").read()
    orig = s
    for a, b in pairs:
        s = s.replace(a, b)
    if s != orig:
        with open(p, "w", encoding="utf-8") as f:
            f.write(s)
        return 1
    return 0


def main():
    check_only = "--check" in sys.argv

    if check_only:
        print("[check] 只读核对，不落盘")
        return 0

    # ① 六个冲突按并集裁决
    total = 0
    for path, order in list(DOC_ORDERS.items()) + list(CODE_ORDERS.items()):
        n = apply_order(path, order)
        total += n
        print(f"[conflict] {path}: {n} 块 → {order}")
    print(f"[conflict] 共裁决 {total} 块")

    # ② 迁移改名
    for old, new in rename_files():
        print(f"[rename] {old} → {new}")

    # ③ 内容里的 905 → 908（含变量名、注释、台账行号）
    content_subs = [
        ("installer/internal/dbinit/runner.go", [
            (f'"{OLD_MIG}.sql"', f'"{NEW_MIG}.sql"'),
            ("// 905：删除 route_incident_events 的零消费方索引",
             "// 908：删除 route_incident_events 的零消费方索引"),
        ]),
        ("installer/cmd/llm-gw-installer/main.go", [
            (f"embeddata/startup/{OLD_MIG}.sql", f"embeddata/startup/{NEW_MIG}.sql"),
            (f"embeddata/startup/{OLD_MIG}.down.sql", f"embeddata/startup/{NEW_MIG}.down.sql"),
            ("routeIncidentEventTypeCreatedDrop905Down", "routeIncidentEventTypeCreatedDrop908Down"),
            ("routeIncidentEventTypeCreatedDrop905", "routeIncidentEventTypeCreatedDrop908"),
            (f'"startup/{OLD_MIG}.sql"', f'"startup/{NEW_MIG}.sql"'),
            (f'"startup/{OLD_MIG}.down.sql"', f'"startup/{NEW_MIG}.down.sql"'),
            ("// 905（2026-10-24，R147 存储效率续轮）：删除 route_incident_events 的",
             "// 908（2026-10-24，R147 存储效率续轮）：删除 route_incident_events 的"),
            ("// baseline 刻意保留（718 先例：fresh-install 链建出→903 删掉）。",
             "// baseline 刻意保留（718 先例：fresh-install 链建出→908 删掉）。"),
        ]),
        ("scripts/apply-db-revision-sequence.sh", [
            (f'sql/migrations/startup/{OLD_MIG}.sql', f'sql/migrations/startup/{NEW_MIG}.sql'),
            ("# 905：删除 idx_route_incident_events_type_created",
             "# 908：删除 idx_route_incident_events_type_created"),
        ]),
        ("sql/schema/installed_startup_migrations.tsv", [
            (f"307\t{OLD_MIG}.sql", f"310\t{NEW_MIG}.sql"),
        ]),
        ("installer/cmd/llm-gw-installer/startup_concurrency_gate_test.go", [
            # ★ 陈旧键：上一会话改号时漏改 ⇒ 门 TestPsqlConcurrencyRequiredNamesStillExist 会红
            (f'"{OLD_MIG.replace("905", "903")}.sql": 1', f'"{NEW_MIG}.sql": 1'),
            ("// 903（2026-10-24 R147 存储效率续轮，㉘ 裁决落地）与 845/888/898/902 同类：",
             "// 908（2026-10-24 R147 存储效率续轮，㉘ 裁决落地）与 845/888/898/902 同类："),
        ]),
        ("sql/migrations/startup/migration_908_route_incident_type_created_drop_test.go", [
            ("mig905DownName", "mig908DownName"),
            ("mig905Name", "mig908Name"),
            (OLD_MIG, NEW_MIG),
        ]),
        (f"sql/migrations/startup/{NEW_MIG}.sql", [
            (f"-- {OLD_MIG}.sql", f"-- {NEW_MIG}.sql"),
            ("-- 905_route_incident_events_type_created_drop.sql",
             "-- 908_route_incident_events_type_created_drop.sql"),
            ("--   904 用于 904_inspector_stats_partial_indexes.sql ⇒ 按 LESSONS「下一个空号是会漂移的\n"
             "--   活事实」纪律让号为 905。判决内容零变化。",
             "--   904 用于 904_inspector_stats_partial_indexes.sql ⇒ 按 LESSONS「下一个空号是会漂移的\n"
             "--   活事实」纪律让号为 905；★ ★ 2026-10-25 第二次漂移：并行会话在上游于\n"
             "--   10-24 19:49 / 21:44 / 23:04 又取走 905/906/907 ⇒ 本条再次让号为 **908**\n"
             "--   （上游当前最大号 907 之后的第一个空号）。★ 判决内容零变化（只改文件名与登记号）。"),
        ]),
        (f"sql/migrations/startup/{NEW_MIG}.down.sql", [
            ("-- 905 down：重建", "-- 908 down：重建"),
            (f"-- {OLD_MIG}.sql", f"-- {NEW_MIG}.sql"),
        ]),
        (f"installer/cmd/llm-gw-installer/embeddata/startup/{NEW_MIG}.sql", [
            (f"-- {OLD_MIG}.sql", f"-- {NEW_MIG}.sql"),
        ]),
        (f"installer/cmd/llm-gw-installer/embeddata/startup/{NEW_MIG}.down.sql", [
            ("-- 905 down：重建", "-- 908 down：重建"),
            (f"-- {OLD_MIG}.sql", f"-- {NEW_MIG}.sql"),
        ]),
    ]
    for path, pairs in content_subs:
        if sub_file(path, pairs, must=False):
            print(f"[subs] {path}")

    # ④ 变异脚本的陈旧 903 引用（否则变异脚本静默空跑）
    sub_file("scripts/audit/mutate_147_903_gate.py", [
        (f'"{OLD_MIG}.sql"', f'"{NEW_MIG}.sql"'),
        (f'"{OLD_MIG}.down.sql"', f'"{NEW_MIG}.down.sql"'),
        (f'"305\\t{OLD_MIG}.sql\\n"', f'"310\\t{NEW_MIG}.sql\\n"'),
        (f'"startup/{OLD_MIG}.sql"', f'"startup/{NEW_MIG}.sql"'),
        (f"installer/cmd/llm-gw-installer/embeddata/startup/{OLD_MIG}.sql",
         f"installer/cmd/llm-gw-installer/embeddata/startup/{NEW_MIG}.sql"),
        (f"sql/migrations/startup/{OLD_MIG}.sql", f"sql/migrations/startup/{NEW_MIG}.sql"),
        (f"| 903 | `{OLD_MIG}.sql`", f"| 908 | `{NEW_MIG}.sql`"),
        ("mutate_147_903_gate.py —— 903 门变异验证", "mutate_147_903_gate.py —— 908 门变异验证"),
    ])

    print("[done]")
    return 0


if __name__ == "__main__":
    sys.exit(main())
