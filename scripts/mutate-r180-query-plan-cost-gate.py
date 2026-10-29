# -*- coding: utf-8 -*-
#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""R180 轮的变异验证脚本（★ 生产代价轴：★ 「慢的读点必须写清为什么慢」）。

# 这道脚本在钉什么

★ 仓内有一条纪律：**「证伪一道『保证』的唯一办法是【真的把它打破一次】」**。
★ R180 轮给读点台账加了**第二条轴**：★ **生产代价轴**
   （★ 「形态」答「走没走索引」，★ 「代价」答「贵不贵」—— ★ 实测两轴几乎正交：
   ★ 形态=seek 的 6 条只占累计代价的 0.37%，★ 而形态=error（测不出来）的 72 条占了 79%）。

★ ⇒ 所以本脚本把**每一条新腿**都真的打破一次，★ 确认对应的门会红。

# ★★ 三条纪律（★ 同族脚本的既有约定）

① ★ **「红了」≠「验到了你想验的那条腿」** ⇒ 每条变异都**指名**它必须红在哪条腿上
   （★ 上游把那条腿改名/删除，★ 剩余腿照样能红 ⇒ 脚本照样报「验证通过」）。
② ★ **编译红不算数**（★ 只说明代码坏了，★ 不说明判据有牙）⇒ 只认**断言红**。
③ ★ **还原必须一字不差** ⇒ 还原后**逐字 md5 比对**。

# ★★ 本轮特别强调的一条

★★ **门运行期禁止改编内文件** ⇒ ⇒ 本脚本**只改仓外副本**
   （★ 用门自己提供的两个 env override：代价台账 / 代价豁免登记）⇒ ⇒ 仓内文件一个字节都不动。

# 用法

    python3 scripts/mutate-r180-query-plan-cost-gate.py

# ★ 残留声明（★ `scripts/audit/mutation_residue_guard.py` 会按它检查残留）

    TARGET = REPO / "scripts/ursmcheck/query_plan_cost_gate_test.go"
    TARGET = REPO / "scripts/audit/asset/query-plan-cost-ledger.tsv"
    TARGET = REPO / "scripts/audit/asset/query-plan-cost-exemptions.tsv"
"""

import hashlib
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

# ★★ 子进程走【专用 GOCACHE】⇒ ⇒ 不往用户的全局缓存里写「随树变化而失效」的一次性产物。
for _gc_lib in (os.path.join(os.path.dirname(os.path.abspath(__file__)), "lib"),
                os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "lib")):
    if os.path.isfile(os.path.join(_gc_lib, "gocache.py")):
        sys.path.insert(0, _gc_lib)
        break
import gocache  # noqa: E402

# ★★★ r1048 轮（★ 2026-10-29）：★★ 【失败安全】★★
# ★★★ 原写法是 `os.environ["GOCACHE"] = gocache.dir_for(...)` ⇒ ⇒ ★ **dir_for 只算路径，
# ★★★ 不建目录、也不查可写** ⇒ ⇒ ★★ 专用缓存目录**建不出来**时 ⇒ ⇒ ★ go 拿到一个
# ★★★ 不可用的 GOCACHE ⇒ ⇒ ★★ 会直接报 `failed to initialize build cache` ⇒ ⇒ ★★
# ★★★ **脚本直接坏掉** ⇒ ⇒ ★★ 而「脚本坏掉」比「这次没隔离」**严重得多** ⇒ ⇒ ★★★
# ★★★ ⇒ ⇒ ★★ 改走 `apply_to_environ()`（★ 内部先 `ensure()` 建目录并查可写 ⇒ ⇒
# ★★★ 建不出来就**不设** GOCACHE ⇒ ⇒ ★ 子进程回落用环境里原本的那个 ⇒ ⇒ ★ 脚本照常跑）
# ★★★ ⇒ ⇒ ★★ 原则：**宁可漏隔离，也不许把脚本弄坏**（★ 与 `scripts/lib/gocache.py`
# ★★★ 的 `ensure()` / `apply_to_environ()` 注释同款）。
_GC_OK = gocache.apply_to_environ("mutate")

REPO = pathlib.Path(__file__).resolve().parents[1]

# ★ 残留门按这几个声明收集「本脚本会写的文件」
TARGET = REPO / "scripts/ursmcheck/query_plan_cost_gate_test.go"
TARGET = REPO / "scripts/audit/asset/query-plan-cost-ledger.tsv"
TARGET = REPO / "scripts/audit/asset/query-plan-cost-exemptions.tsv"

LEDGER_REL = "scripts/audit/asset/query-plan-cost-ledger.tsv"
EXEMPT_REL = "scripts/audit/asset/query-plan-cost-exemptions.tsv"

GATE_TEST = "TestQueryPlanCostLedgerMatchesFacts"
# ★ 专用缓存**没生效**时（★ `_GC_OK` 为假 ⇒ ★ 专用目录建不出来）⇒ ⇒ ★★ **不许**把
# ★ GOCACHE 设成空串 ⇒ ⇒ ★★ go 会报「不是绝对路径」⇒ ⇒ ★★ ⇒ ⇒ 所以**只在生效时**
# ★ 才显式带出（★ 不带这个键 ⇒ ⇒ 子进程用 `os.environ` 原本的那个 ⇒ ⇒ 脚本照常跑）。
GO_ENV = {"GOFLAGS": "-mod=mod"}
if _GC_OK:
    GO_ENV["GOCACHE"] = os.environ["GOCACHE"]


def md5(path: pathlib.Path) -> str:
    return hashlib.md5(path.read_bytes()).hexdigest()


def run_gate(env_extra: dict) -> subprocess.CompletedProcess:
    env = {**dict(os.environ), **GO_ENV, **env_extra}
    return subprocess.run(
        ["go", "test", "./scripts/ursmcheck/", "-run", GATE_TEST, "-count=1"],
        cwd=str(REPO), capture_output=True, text=True, env=env,
    )


def read_rows(path: pathlib.Path) -> tuple[list[str], list[list[str]]]:
    header, rows = [], []
    for line in path.read_text(encoding="utf-8").split("\n"):
        if not line.strip() or line.startswith("#"):
            header.append(line)
            continue
        rows.append(line.split("\t"))
    return header, rows


def write_rows(path: pathlib.Path, header: list[str], rows: list[list[str]]) -> None:
    out = list(header)
    for r in rows:
        out.append("\t".join(r))
    path.write_text("\n".join(out), encoding="utf-8")


def main() -> int:
    tmp = pathlib.Path(tempfile.mkdtemp(prefix="r180-mutate-"))
    ledger = REPO / LEDGER_REL
    exempt = REPO / EXEMPT_REL
    for p in (ledger, exempt):
        if not p.exists():
            print(f"ERROR: 缺文件 {p}", file=sys.stderr)
            return 2

    before = {p: md5(p) for p in (ledger, exempt)}

    # ── 阳性对照：★ 不变异 ⇒ 门必须绿 ──
    base = run_gate({})
    if base.returncode != 0:
        print("★ 阳性对照失败：★ 未变异时门就红了 ⇒ ⇒ 后面所有「红了」都不可信", file=sys.stderr)
        print(base.stdout[-3000:], file=sys.stderr)
        return 1
    print("★ 阳性对照：未变异 ⇒ 门绿 ✔")

    results = []

    def check(name: str, leg: str, env: dict) -> None:
        p = run_gate(env)
        out = p.stdout + p.stderr
        red = p.returncode != 0
        if "build failed" in out or "syntax error" in out or "cannot use" in out:
            results.append((name, False, "★ 编译红不算数（★ 是代码坏了，★ 不是判据有牙）"))
            print(f"★ {name} ⇒ ★ 编译红（★ 不算数）")
            return
        if not red or "--- FAIL" not in out:
            results.append((name, False, f"★ 门没红（★ leg={leg}）"))
            print(f"★ {name} ⇒ ★ 门没红（★ leg={leg}）")
            return
        results.append((name, True, f"leg={leg}"))
        print(f"★ {name} ⇒ 门红 ✔（{leg}）")

    # ── M8：★ 「代价台账被掏空 ⇒ 门恒绿」这条腿 ──
    m8 = tmp / "ledger-m8.tsv"
    header, rows = read_rows(ledger)
    write_rows(m8, header, [])
    check("M8 代价台账掏空", "① 代价台账为空 ⇒ 判据失效",
          {"URSM_QUERY_PLAN_COST_LEDGER_TSV": str(m8)})

    # ── M9：★ 「超阈必须挂 slow 登记」这条腿（★ 删【整行】⇒ 对准主判据） ──
    m9 = tmp / "exempt-m9.tsv"
    header, rows = read_rows(exempt)
    kept = [r for r in rows if not (len(r) >= 3 and r[1] == "slow")]
    if len(kept) == len(rows):
        print("★ M9 前提不成立：豁免登记里没有 category=slow 的行", file=sys.stderr)
        return 1
    write_rows(m9, header, kept)
    check("M9 删掉一条 slow 登记", "⑤ 超阈值必须挂 category=slow 登记",
          {"URSM_QUERY_PLAN_COST_EXEMPTIONS_TSV": str(m9)})

    # ── M13：★ 「理由不能是占位符」这条子判据 ──
    #   ★★ M9 曾一度只把理由【换成无意义的中文】⇒ ⇒ 门没红 ⇒ ★★ 暴露了一处真缺陷：
    #   ★★ 门对「理由写得有没有意义」几乎无判据（★ 黑名单只认固定几个词）
    #   ⇒ ⇒ 这条子判据只能靠【人工复核】⇒ ⇒ 审计档必须如实写明这个边界。
    m13 = tmp / "exempt-m13.tsv"
    header, rows = read_rows(exempt)
    mutated = False
    for r in rows:
        if len(r) >= 3 and r[1] == "slow":
            r[2] = "待查"
            mutated = True
            break
    if not mutated:
        print("★ M13 前提不成立：豁免登记里没有 category=slow 的行", file=sys.stderr)
        return 1
    write_rows(m13, header, rows)
    check("M13 豁免理由写成占位符", "⑤ 豁免理由不得是占位符",
          {"URSM_QUERY_PLAN_COST_EXEMPTIONS_TSV": str(m13)})

    # ── M10：★ 「反向覆盖」这条腿（★ 台账必须覆盖事实源的全部语句） ──
    m10 = tmp / "ledger-m10.tsv"
    header, rows = read_rows(ledger)
    if len(rows) <= 1:
        print("★ M10 前提不成立：代价台账里没有可删的行", file=sys.stderr)
        return 1
    write_rows(m10, header, rows[:-1])
    check("M10 代价台账少登记一条语句", "③ 台账必须覆盖事实源的全部语句",
          {"URSM_QUERY_PLAN_COST_LEDGER_TSV": str(m10)})

    # ── M11：★ 「门自己重算阈值」这条腿（★ 反向：★ 台账说超阈，★ 门重算说没超阈） ──
    m11 = tmp / "ledger-m11.tsv"
    header, rows = read_rows(ledger)
    mutated = False
    for r in rows:
        if len(r) >= 8 and r[7] == "":
            r[7] = "total>=5min"
            mutated = True
            break
    if not mutated:
        print("★ M11 前提不成立：代价台账里没有未超阈的行", file=sys.stderr)
        return 1
    write_rows(m11, header, rows)
    check("M11 台账说超阈而门重算说没超阈", "⑥ 门自己重算阈值 ⇒ 必须与台账一致",
          {"URSM_QUERY_PLAN_COST_LEDGER_TSV": str(m11)})

    # ── M12：★ 同一条腿的另一侧（★ 台账说没超阈，★ 门重算说超阈） ──
    m12 = tmp / "ledger-m12.tsv"
    header, rows = read_rows(ledger)
    mutated = False
    for r in rows:
        if len(r) >= 8 and r[7] != "":
            r[7] = ""
            mutated = True
            break
    if not mutated:
        print("★ M12 前提不成立：代价台账里没有超阈的行", file=sys.stderr)
        return 1
    write_rows(m12, header, rows)
    check("M12 台账说没超阈而门重算说超阈", "⑥ 门自己重算阈值 ⇒ 必须与台账一致",
          {"URSM_QUERY_PLAN_COST_LEDGER_TSV": str(m12)})

    # ── 还原后逐字 md5 比对（★ 仓内文件一个字节都不该动） ──
    ok = True
    for p, h in before.items():
        now = md5(p)
        if now != h:
            print(f"★ 还原校验失败：{p} 的 md5 变了（★ {h} → {now}）", file=sys.stderr)
            ok = False
    if ok:
        print("★ 还原校验：仓内两个台账逐字未变（★ md5 一致）✔")

    shutil.rmtree(tmp, ignore_errors=True)

    passed = sum(1 for _, ok2, _ in results if ok2)
    print(f"\n★ 变异臂 6 条 ⇒ 通过 {passed} 条 ⇒ 失败 {len(results) - passed} 条")
    for name, ok2, why in results:
        if not ok2:
            print(f"   ✗ {name} —— {why}")
    return 0 if passed == len(results) and ok else 1


if __name__ == "__main__":
    sys.exit(main())
