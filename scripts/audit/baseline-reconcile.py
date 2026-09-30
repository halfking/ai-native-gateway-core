#!/usr/bin/env python3
"""baseline-reconcile.py — per-object reconciliation between two pg_dump baselines.

Round 43. The three committed 00-prereqs/01-schema copies were hand-maintained
because sql/scripts/dump-schema.sh sourced a library that was never in this
repository. That library is now in-repo and produces a clean baseline, so the
committed copies and a regenerated one must be reconciled object-by-object
before anyone replaces anything.

Categories reported, per pg_dump "-- Name: X; Type: Y; Schema:" banner:
  ADDED        present only in the generated baseline
  MISSING      present only in the committed baseline
  REORDERED    present in both, but at a different ordinal
  SHARED       present in both at the same ordinal

Usage:
  baseline-reconcile.py --committed sql/schema/01-schema.sql \
                        --generated /tmp/baseline/01-schema.sql \
                        [--objects-dir sql/objects] \
                        [--report docs/....md]

--objects-dir adds a third representation to the comparison. Round 43
established that sql/objects/ is a fourth copy of the schema with a life of
its own: it is the column-map source for the internal/dbx static jsonb lint
(load-bearing) and the only provenance for some baseline objects, yet it is
never applied anywhere and deploy/sql/objects/ was never materialised.
Measured drift against the canonical baseline is BIDIRECTIONAL — 31 objects
only in sql/objects, 618 only in the baseline — so the two are not merely
offset generations, they have drifted apart in both directions. A guard
demanding they be equal would be a false invariant; this tool reports the
drift instead.
"""
import argparse
import os
import re
import sys
from collections import OrderedDict

BANNER = re.compile(r"^-- Name: (.+?); Type: (.+?); Schema:")


def load(path):
    """Return OrderedDict[key] = (name, type, 1-based line)."""
    if not os.path.exists(path):
        sys.exit("ERROR: baseline not found: %s" % path)
    out = OrderedDict()
    with open(path, encoding="utf-8", errors="replace") as fh:
        for lineno, line in enumerate(fh, 1):
            m = BANNER.match(line.rstrip("\n"))
            if not m:
                continue
            name, typ = m.group(1), m.group(2)
            # Key on type+name so a function and table sharing a name do not
            # collide (this schema has several).
            key = typ + ":" + name
            # First definition wins: pg_dump can repeat a banner for a
            # partitioned child, and the first is the one that must apply.
            if key not in out:
                out[key] = (name, typ, lineno)
    return out


def load_objects_dir(root):
    """Return {type:name -> name} for every pg_dump banner under root/*/*.sql."""
    out = OrderedDict()
    if not os.path.isdir(root):
        return None
    for sub in sorted(os.listdir(root)):
        subdir = os.path.join(root, sub)
        if not os.path.isdir(subdir):
            continue
        for name in sorted(os.listdir(subdir)):
            if not name.endswith(".sql"):
                continue
            got = load(os.path.join(subdir, name))
            if not got:
                continue
            for k, v in got.items():
                out.setdefault(k, v)
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--objects-dir")
    ap.add_argument("--committed", required=True)
    ap.add_argument("--generated", required=True)
    ap.add_argument("--report")
    ap.add_argument("--sample", type=int, default=25,
                    help="how many names to list per category")
    args = ap.parse_args()

    com = load(args.committed)
    gen = load(args.generated)
    if not com or not gen:
        sys.exit("ERROR: a baseline parsed to zero objects — refusing to report")

    com_keys, gen_keys = set(com), set(gen)
    added = [k for k in gen if k not in com_keys]
    missing = [k for k in com if k not in gen_keys]
    shared = [k for k in com if k in gen_keys]
    reordered = [k for k in shared if com[k][2] != gen[k][2]]

    L = []
    L.append("# 基线逐对象对账报告（round 43）\n")
    L.append("生成方式：`scripts/audit/baseline-reconcile.py`，按 pg_dump 的\n"
             "`-- Name: X; Type: Y; Schema:` 横幅逐对象比对。\n")
    L.append("| 项 | 值 |")
    L.append("|---|---|")
    L.append("| 已提交基线 | `%s` |" % args.committed)
    L.append("| 新生成基线 | `%s` |" % args.generated)
    L.append("| 已提交对象数 | %d |" % len(com))
    L.append("| 新生成对象数 | %d |" % len(gen))
    L.append("| **ADDED**（仅新生成有） | **%d** |" % len(added))
    L.append("| **MISSING**（仅已提交有） | **%d** |" % len(missing))
    L.append("| REORDERED（两边都有但位置不同） | %d |" % len(reordered))
    L.append("| SHARED（两边都有且行号相同） | %d |" % (len(shared) - len(reordered)))

    def block(title, keys, src, note):
        L.append("\n## %s（%d）\n" % (title, len(keys)))
        L.append(note + "\n")
        if not keys:
            L.append("（无）\n")
            return
        L.append("| Type | Name | 行号 |")
        L.append("|---|---|---|")
        for k in keys[: args.sample]:
            name, typ, line = src[k]
            L.append("| %s | `%s` | %d |" % (typ, name, line))
        if len(keys) > args.sample:
            L.append("\n… 另有 %d 个未列出" % (len(keys) - args.sample))

    block("MISSING — 已提交基线有、新生成没有", missing, com,
          "**这是唯一有语义风险的一类**：对象在新基线里消失，说明源库上它已被"
          "重命名或删除。若确认是源库真实状态，替换基线前需确认无代码/迁移仍依赖它。")
    block("ADDED — 新生成基线有、已提交没有", added, gen,
          "对应本轮实测的缺口：新基线含 `candidate_failure_logs_hot`、"
          "`session_turns_hot`、`session_dim`、`model_offers`、`proxy_subscriptions` "
          "等已提交基线缺失的对象。")
    block("REORDERED — 位置不同", reordered, gen,
          "位置变化本身不必然是缺陷；关键是重排后 `LANGUAGE sql` 函数体与 "
          "`COMMENT ON` 仍满足校验顺序（新生成器已把自包含单行 `COMMENT ON` 后置）。")

    if args.objects_dir:
        objs = load_objects_dir(args.objects_dir)
        if not objs:
            print("ERROR: %s yielded no objects — refusing to report" % args.objects_dir,
                  file=sys.stderr)
            return 1
        L.append("\n## 第三份表示：%s（%d 对象）\n" % (args.objects_dir, len(objs)))
        L.append("`sql/objects/` 既是 `internal/dbx` 静态 jsonb lint 的列映射来源（承重），"
                 "又是部分基线对象的唯一出处；但它**从不参与任何 apply**，"
                 "`deploy/sql/objects/` 镜像也从未产出。\n")
        L.append("与已提交基线的差异是**双向的**，因此不能用「二者应当一致」当不变式——"
                 "那是一条不成立的门。下表只作测量。\n")
        L.append("| 相对基线 | 数量 | 样例 |")
        L.append("|---|---|---|")
        def label(v):
            # load() yields (name, type, lineno); load_objects_dir yields a bare name.
            return v[0] if isinstance(v, (tuple, list)) else v

        for title, keys, src in (
            ("仅 sql/objects 有", [k for k in objs if k not in com], objs),
            ("仅已提交基线有", [k for k in com if k not in objs], com),
        ):
            sample = "、".join("`%s`" % label(src[k]) for k in keys[:3]) or "（无）"
            L.append("| %s | %d | %s |" % (title, len(keys), sample))
        L.append("\n> 处置建议见 `docs/audit/2026-10-01-802-missing-breaks-installer-build.md`"
                 " 同批留档；替换基线前需先决定 `sql/objects/` 是继续做静态分析 SSOT，"
                 "还是降级为历史归档。")

    text = "\n".join(L) + "\n"

    # Always print the summary; write the full report only when asked.
    print("committed objects : %d" % len(com))
    print("generated objects : %d" % len(gen))
    print("ADDED             : %d" % len(added))
    print("MISSING           : %d" % len(missing))
    print("REORDERED         : %d" % len(reordered))
    print("SHARED same-line  : %d" % (len(shared) - len(reordered)))
    if args.report:
        os.makedirs(os.path.dirname(args.report) or ".", exist_ok=True)
        with open(args.report, "w", encoding="utf-8") as fh:
            fh.write(text)
        print("report written    : %s" % args.report)
    if not args.report:
        print()
        print(text)


if __name__ == "__main__":
    main()
