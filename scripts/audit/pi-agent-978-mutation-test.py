#!/usr/bin/env python3
"""scripts/audit/pi-agent-978-mutation-test.py —— PI 智能体交付轮（2）的变异验证。

对**本轮新增/修改**的门禁逐条做「改坏 ⇒ 必须红；还原 ⇒ 必须绿」：

  M1  977 的产物探针退回「不剥注释」的判据       ⇒ 缺席型探针门 + 点名门 红
  M2  从安装器 tsv 移除 978 行                   ⇒ 交付可达性门 红
  M3  从 dbinit StartupFiles 移除 978            ⇒ 交付可达性门 红
  M4  从 deploy 通道 files 数组移除 978          ⇒ 交付可达性门 红
  M5  embeddata 副本改动一个字节                 ⇒ 六点门（字节一致）红
  M6  978 里某条 364 规则行的字段被改             ⇒ 规则集保真门 红
  M7  978 去掉 ON CONFLICT (rule_name) DO NOTHING ⇒ 幂等门 红
  M8  978 的 down 删掉 364 时代的规则            ⇒ 非破坏门 红
  M9  978 的 privilege_escalation pattern 退回旧形态 ⇒ 规则集保真门 红

用法：python3 -u scripts/audit/pi-agent-978-mutation-test.py
退出码：0 = 全部变异都被抓到（红→还原绿）；1 = 有变异**未被抓到**。
"""

import pathlib
import shutil
import subprocess
import sys
import tempfile
# ★★ 2026-11-23（211 配置优化执行报告 §9.24 轮 · N4 基线红收口）：★★ 子进程走【专用
# ★★ GOCACHE】⇒ ⇒ 不再往用户的全局缓存里写「随树变化而失效」的一次性产物
# ★★ （★ 理由与实测读数见 `scripts/lib/gocache.py` 与 `internal/gocache` 的包注释）。
# ★★ **两条路径都试**（★ ★★ 深度无关：★ 本文件在 scripts/ 或 scripts/audit/ 下）★★
for _gc_lib in (os.path.join(os.path.dirname(os.path.abspath(__file__)), "lib"),
                os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "lib")):
    if os.path.isfile(os.path.join(_gc_lib, "gocache.py")):
        sys.path.insert(0, _gc_lib)
        break
import gocache  # noqa: E402
gocache.apply_to_environ("mutate")

REPO = pathlib.Path(__file__).resolve().parents[2]

SEQ = "scripts/apply-db-revision-sequence.sh"
TSV = "sql/schema/installed_startup_migrations.tsv"
RUNNER = "installer/internal/dbinit/runner.go"
EMBED = "installer/cmd/llm-gw-installer/embeddata/startup/978_pi_canonical_rule_set.sql"
CANON = "sql/migrations/startup/978_pi_canonical_rule_set.sql"
DOWN = "sql/migrations/startup/978_pi_canonical_rule_set.down.sql"

GATE_TEST = "./internal/sqlguard/"
GATE_RUNS = [
    "TestAbsenceArtifactProbesMustStripComments",
    "TestAbsenceProbeRegression977",
    "TestMigration978DeliverySixPointsAreInPlace",
    "TestMigration978RuleSetIsByteFaithfulToItsSources",
    "TestMigration978IsIdempotentAndNonDestructive",
    "TestMigration978DeliveryIsReachable",
]


def apply_mutation(path: pathlib.Path, find: str, repl: str) -> bool:
    """**行级定点变异**：只改**含锚点的那一行**，并断言该行确实变了。

    ★ 为什么必须行级（而不是整文件 replace）：
      仓里的注释常常**引用**被判据的字符串（例如「修法：`position('RETURNING' in
      regexp_replace(...) = 0`」这句话就写在探针上方的注释里）⇒ 整文件 replace
      会**先命中注释**，代码里的生效面**原封不动** ⇒ 变异「发生在散文里」
      ⇒ 门当然不红 ⇒ **假阴性**（本轮真踩过一次：M1 报「改坏了却仍然绿」，
      真因是变异改的是注释）。
      ⇒ 规则：**变异必须落在生效面**，且必须断言「那行确实变了」。
    """
    # 尾部换行不属于「行内容」⇒ 先剥掉（否则逐行匹配永远命中不了）。
    needle = find[:-1] if find.endswith("\n") else find
    lines = path.read_text(encoding="utf-8").split("\n")
    hits = [i for i, l in enumerate(lines) if needle in l]
    if not hits:
        return False
    # 取**最后一处**命中：探针 SQL 行总在注释块之后 ⇒ 注释里的引用在前。
    i = hits[-1]
    if repl == "":
        # ★ 空替换 = **删掉整行**（删文件里的某条登记 ⇒ 门必须红）。
        del lines[i]
    else:
        lines[i] = lines[i].replace(needle, repl)
    path.write_text("\n".join(lines), encoding="utf-8")
    return True


def run_gate() -> tuple[bool, str]:
    proc = subprocess.run(
        ["go", "test", GATE_TEST, "-run", "|".join(GATE_RUNS), "-count=1"],
        cwd=REPO, capture_output=True, text=True,
    )
    return proc.returncode == 0, (proc.stdout + proc.stderr)[-2500:]


MUTATIONS = [
    ("M1 977 探针退回不剥注释", SEQ,
     "position('RETURNING' in regexp_replace(p.prosrc, '--[^\\n]*', '', 'g')) = 0",
     "position('RETURNING' in p.prosrc) = 0"),
    ("M2 tsv 移除 978 行", TSV,
     "377\t978_pi_canonical_rule_set.sql\n", ""),
    ("M3 StartupFiles 移除 978", RUNNER,
     '\t\t\t"978_pi_canonical_rule_set.sql",\n', ""),
    ("M4 deploy files 数组移除 978", SEQ,
     '  "$ROOT_DIR/sql/migrations/startup/978_pi_canonical_rule_set.sql"\n', ""),
    ("M5 embeddata 副本改一个字节", EMBED,
     "-- Migration: 978_pi_canonical_rule_set",
     "-- Migration: 978_pi_canonical_rule_set "),
    ("M6 某条 364 规则行字段被改", CANON,
     "('unicode_zero_width', 'advanced', 'bypass', 'unicode_obfuscation',",
     "('unicode_zero_width', 'advanced', 'bypass', 'unicode_obfuscation', 'x',"),
    ("M7 去掉 ON CONFLICT", CANON,
     "    ON CONFLICT (rule_name) DO NOTHING;\n", "\n"),
    ("M8 down 删掉 364 时代的规则", DOWN,
     "WHERE rule_name IN ('privilege_escalation', 'command_injection');",
     "WHERE rule_name IN ('privilege_escalation', 'command_injection', 'unicode_zero_width');"),
    ("M9 privilege_escalation pattern 退回旧形态", CANON,
     "'(?i)(grant|give).*\\badmin\\b|\\bsudo\\b\\s+(-\\S+\\s+)*\\S+|",
     "'(?i)(grant|give).*admin|sudo|root|"),
]


def main() -> int:
    print("== 基线：全绿 ==", flush=True)
    ok, out = run_gate()
    if not ok:
        print(out)
        print("FAIL 基线不绿 ⇒ 后续变异无意义", file=sys.stderr)
        return 1
    print("PASS 基线全绿\n", flush=True)

    failed = []
    for name, rel, find, repl in MUTATIONS:
        path = REPO / rel
        original = path.read_text(encoding="utf-8")
        if find not in original:
            print(f"FAIL {name} —— 锚点未命中（变异没发生 ⇒ 假阴性）", file=sys.stderr)
            failed.append(name)
            continue
        try:
            if not apply_mutation(path, find, repl):
                print(f"FAIL {name} —— 锚点未命中（变异没发生 ⇒ 假阴性）", file=sys.stderr)
                failed.append(name)
                continue
            ok, out = run_gate()
            if ok:
                print(f"FAIL {name} —— 改坏了却仍然绿 ⇒ 门失效", file=sys.stderr)
                failed.append(name)
            else:
                print(f"PASS {name} ⇒ 门变红", flush=True)
        finally:
            path.write_text(original, encoding="utf-8")
        ok2, _ = run_gate()
        if not ok2:
            print(f"FAIL {name} —— 还原后仍红 ⇒ 还原失败", file=sys.stderr)
            failed.append(name)
        else:
            print(f"     还原后复跑绿\n", flush=True)

    print()
    if failed:
        print(f"=== 变异验证失败 {len(failed)}/{len(MUTATIONS)}：{failed} ===", file=sys.stderr)
        return 1
    print(f"=== 变异验证 {len(MUTATIONS)}/{len(MUTATIONS)} 全绿（改坏即红、还原即绿） ===")
    return 0


if __name__ == "__main__":
    sys.exit(main())
