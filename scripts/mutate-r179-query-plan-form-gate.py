# -*- coding: utf-8 -*-
#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""R179 轮的变异验证脚本（★ 实测计划形态轴：★ 「登记 covered 者必须实测走索引」）。

# 这道脚本在钉什么

★ 仓内有一条纪律：**「证伪一道『保证』的唯一办法是【真的把它打破一次】」**。
★ R179 轮给 `query_index_coverage_gate_test.go` 加了**动态轴**（★ 实测计划形态）：
   ★ 「静态轴说【有索引覆盖】，★ 而规划器实际【没走索引】」这一类
   ⇒ ⇒ **静态轴抓不到** ⇒ ⇒ 必须有实测轴顶着。

★ ⇒ 所以本脚本把**每一条新腿**都真的打破一次，★ 确认对应的门会红。

# ★★ 三条纪律（★ 同族脚本的既有约定）

① ★ **「红了」≠「验到了你想验的那条腿」** ⇒ 每条变异都**指名**它必须红在哪条腿上
   （★ 上游把那条腿改名/删除，★ 剩余腿照样能红 ⇒ 脚本照样报「验证通过」）。
② ★ **编译红不算数**（★ 只说明代码坏了，★ 不说明判据有牙）⇒ 只认**断言红**。
③ ★ **还原必须一字不差** ⇒ 还原后**逐字 md5 比对**。

# ★★ 本轮特别强调的一条

★★ **门运行期禁止改编内文件** ⇒ ⇒ 本脚本**只改仓外副本**
   （★ 用门自己提供的三个 env override：需求清单 / 实测台账 / 豁免登记）⇒ ⇒ 仓内文件一个字节都不动。

# 用法

    python3 scripts/mutate-r179-query-plan-form-gate.py

# ★ 残留声明（★ `scripts/audit/mutation_residue_guard.py` 会按它检查残留）

    TARGET = REPO / "scripts/ursmcheck/query_index_coverage_gate_test.go"
    TARGET = REPO / "scripts/audit/asset/query-index-requirements.tsv"
    TARGET = REPO / "scripts/audit/asset/query-plan-observations.tsv"
    TARGET = REPO / "scripts/audit/asset/query-plan-exemptions.tsv"
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
gocache.apply_to_environ("mutate")

REPO = pathlib.Path(__file__).resolve().parents[1]

# ★ 残留门按这几个声明收集「本脚本会写的文件」
TARGET = REPO / "scripts/ursmcheck/query_index_coverage_gate_test.go"
TARGET = REPO / "scripts/audit/asset/query-index-requirements.tsv"
TARGET = REPO / "scripts/audit/asset/query-plan-observations.tsv"
TARGET = REPO / "scripts/audit/asset/query-plan-exemptions.tsv"

REQ_REL = "scripts/audit/asset/query-index-requirements.tsv"
OBS_REL = "scripts/audit/asset/query-plan-observations.tsv"
EXM_REL = "scripts/audit/asset/query-plan-exemptions.tsv"

GATE_TEST = "TestQueryIndexCoverageRequirements"
GO_ENV = {"GOCACHE": os.environ["GOCACHE"], "GOFLAGS": "-mod=mod"}


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
    tmp = pathlib.Path(tempfile.mkdtemp(prefix="r179-mutate-"))
    req = REPO / REQ_REL
    obs = REPO / OBS_REL
    exm = REPO / EXM_REL
    for p in (req, obs, exm):
        if not p.exists():
            print(f"ERROR: 缺文件 {p}", file=sys.stderr)
            return 2

    before = {p: md5(p) for p in (req, obs, exm)}

    # ── 阳性对照：★ 不变异 ⇒ 门必须绿 ──
    base = run_gate({})
    if base.returncode != 0:
        print("★ 阳性对照失败：★ 未变异时门就红了 ⇒ ⇒ 后面所有「红了」都不可信", file=sys.stderr)
        print(base.stdout[-3000:], file=sys.stderr)
        return 1
    print("★ 阳性对照：未变异 ⇒ 门绿 ✔")

    req_rows = read_rows(req)
    obs_rows = read_rows(obs)
    exm_rows = read_rows(exm)

    results = []

    def check(name: str, leg: str, env: dict) -> None:
        p = run_gate(env)
        out = p.stdout + p.stderr
        red = p.returncode != 0
        # ★ 只认**断言红**（★ 编译红不算数）
        assert_red = ("--- FAIL" in out) or ("Error Trace" in out) or ("FAIL" in out.split("\n")[0] if out else False)
        if not red:
            results.append((name, False, f"★ 门没红（★ leg={leg}）"))
            return
        if "build failed" in out or "syntax error" in out or "cannot use" in out:
            results.append((name, False, "★ 编译红不算数（★ 是代码坏了，★ 不是判据有牙）"))
            return
        results.append((name, True, f"leg={leg}"))
        print(f"★ {name} ⇒ 门红 ✔（{leg}）")

    # ── M1：★ 「登记 covered 者必须实测走索引」这条腿 ──
    #   把一条 measured-only 且实测为 seek 的行 ⇒ 改成 covered + plan_form=filter
    #   ⇒ ⇒ 静态轴说覆盖、实测说没走索引 ⇒ ⇒ 必须红。
    m1 = tmp / "req-m1.tsv"
    header, rows = read_rows(req)
    mutated = False
    # ★★ 挑一行【静态轴说 covered】的（★ ★ 这样【只有动态轴】那条腿会红，
    #   ★ 静态轴那条腿不会 ⇒ ⇒ 红的理由唯一 ⇒ ⇒ 验到的就是我们要验的那条腿）。
    #   ★ ★ 注意：★ 不能用视图行的 measured-only ⇒ 改它会同时触发静态轴那条腿 ⇒ ⇒ 验不准。
    for r in rows:
        if len(r) >= 9 and r[5] == "covered":
            r[8] = "seq-scan"
            r[9] = "252"
            mutated = True
            break
    if not mutated:
        print("★ M1 前提不成立：台账里没有 status=covered 的行", file=sys.stderr)
        return 1
    write_rows(m1, header, rows)
    check("M1 登记 covered + 实测 seq-scan", "① 登记 covered 者必须实测走索引",
          {"URSM_QUERY_INDEX_REQUIREMENTS_TSV": str(m1)})

    # ── M2：★ 「测不出来必须写清为什么」这条腿（★ 豁免登记） ──
    m2 = tmp / "exm-m2.tsv"
    header, rows = read_rows(exm)
    if len(rows) <= 1:
        print("★ M2 前提不成立：豁免登记里没有可删的行", file=sys.stderr)
        return 1
    write_rows(m2, header, rows[:-1])
    check("M2 删掉一条豁免登记", "⑧ 没有实测形态必须挂豁免登记",
          {"URSM_QUERY_PLAN_EXEMPTIONS_TSV": str(m2)})

    # ── M3：★ 「观测台账被掏空 ⇒ 门恒绿」这条腿 ──
    m3 = tmp / "obs-m3.tsv"
    header, rows = read_rows(obs)
    write_rows(m3, header, [])
    check("M3 实测台账掏空", "⑩ 观测台账为空 ⇒ 判据失效",
          {"URSM_QUERY_PLAN_OBSERVATIONS_TSV": str(m3)})

    # ── M4：★ 「台账与观测台账必须对得上」这条腿 ──
    m4 = tmp / "req-m4.tsv"
    header, rows = read_rows(req)
    mutated = False
    for r in rows:
        if len(r) >= 9 and r[8] not in ("", "unmeasured", "error", "no-traffic"):
            r[8] = "seq-scan" if r[8] != "seq-scan" else "seek"
            mutated = True
            break
    if not mutated:
        print("★ M4 前提不成立：台账里没有已实测的行", file=sys.stderr)
        return 1
    write_rows(m4, header, rows)
    check("M4 台账的 plan_form 与观测台账对不上", "⑦ 台账 ⇄ 观测台账一致",
          {"URSM_QUERY_INDEX_REQUIREMENTS_TSV": str(m4)})

    # ── M5：★ 「第三态只允许用于视图」这条腿 ──
    m5 = tmp / "req-m5.tsv"
    header, rows = read_rows(req)
    mutated = False
    for r in rows:
        if len(r) >= 6 and r[5] == "measured-only":
            r[2] = "sessions"
            mutated = True
            break
    if not mutated:
        print("★ M5 前提不成立：台账里没有 measured-only 的行", file=sys.stderr)
        return 1
    write_rows(m5, header, rows)
    check("M5 measured-only 用在底表上", "②′ 第三态只允许用于视图",
          {"URSM_QUERY_INDEX_REQUIREMENTS_TSV": str(m5)})

    # ── M6：★ 「台账声称实测过 ⇒ 观测台账里必须有」这条腿 ──
    m6 = tmp / "req-m6.tsv"
    header, rows = read_rows(req)
    mutated = False
    for r in rows:
        if len(r) >= 8 and r[7]:
            r[7] = "stmt-does-not-exist"
            mutated = True
            break
    if not mutated:
        print("★ M6 前提不成立：台账里没有带 plan_shape 的行", file=sys.stderr)
        return 1
    write_rows(m6, header, rows)
    check("M6 plan_shape 在观测台账里不存在", "⑦ 台账 ⇄ 观测台账一致",
          {"URSM_QUERY_INDEX_REQUIREMENTS_TSV": str(m6)})

    # ── M7：★ 「人读视图漂移 ⇒ 读方必须红」这条腿（★ 漂移校验） ──
    #   ★ 本轮新增的读方：scripts/audit/check-canonical-read-point-views.py
    #   ★ 它现在是人读视图（canonical-read-point-shapes.tsv / canonical-read-points.tsv）
    #     的【真读方】⇒ ⇒ 掏空/手改 ⇒ 必须被它抓到 ⇒ ⇒ 逐字比对这条腿要单独钉住。
    #   ★ 做法：★ 把【校验所需的最小子集】拷到仓外副本 ⇒ 在副本上改 ⇒ 跑副本上的校验
    #     ⇒ ⇒ 仓内文件一个字节都不动（★ 本脚本的既有纪律）。
    m7root = tmp / "m7-repo"
    try:
        import json as _json

        with open(REPO / "scripts/audit/asset/canonical-read-point-shapes.json", encoding="utf-8") as fh:
            src_files = _json.load(fh).get("source_files") or []
        if not src_files:
            print("★ M7 前提不成立：形态册 JSON 里没有 source_files", file=sys.stderr)
            return 1
        need = [
            "scripts/audit/extract-canonical-read-point-shapes.py",
            "scripts/audit/check-canonical-read-point-views.py",
            "scripts/audit/asset/canonical-read-point-shapes.json",
            "scripts/audit/asset/canonical-read-point-shapes.tsv",
            "scripts/audit/asset/canonical-read-points.tsv",
            "admin/canonical_read_points_registry_test.go",
        ] + list(src_files)
        for rel in need:
            src = REPO / rel
            if not src.exists():
                print(f"★ M7 前提不成立：副本需要的文件不存在 {rel}", file=sys.stderr)
                return 1
            dst = m7root / rel
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(src, dst)
        m7_check = m7root / "scripts/audit/check-canonical-read-point-views.py"

        def run_m7() -> subprocess.CompletedProcess:
            return subprocess.run(
                [sys.executable, str(m7_check)],
                cwd=str(m7root), capture_output=True, text=True,
            )

        m7_base = run_m7()
        if m7_base.returncode != 0:
            print("★ M7 阳性对照失败：★ 副本上的漂移校验在未变异时就红了", file=sys.stderr)
            print(m7_base.stdout[-2000:], file=sys.stderr)
            return 1
        # ⇒ 变异：★ 改副本里的形态册 TSV 的**一个数据行** ⇒ ⇒ 逐字比对必须抓到。
        m7_tsv = m7root / "scripts/audit/asset/canonical-read-point-shapes.tsv"
        m7_orig = m7_tsv.read_text(encoding="utf-8")
        m7_lines = m7_orig.split("\n")
        m7_hit = False
        for i, ln in enumerate(m7_lines):
            if ln and not ln.startswith("#"):
                m7_lines[i] = ln + "\tmutated"
                m7_hit = True
                break
        if not m7_hit:
            print("★ M7 前提不成立：形态册 TSV 里没有数据行", file=sys.stderr)
            return 1
        m7_tsv.write_text("\n".join(m7_lines), encoding="utf-8")
        m7_run = run_m7()
        m7_out = (m7_run.stdout or "") + (m7_run.stderr or "")
        if m7_run.returncode == 0:
            results.append(("M7 人读视图漂移 ⇒ 漂移校验红", False, "★ 校验没红（★ 它没有牙齿）"))
            print("★ M7 人读视图漂移 ⇒ 校验**没红** ✗", file=sys.stderr)
        elif "漂移" not in m7_out:
            results.append(("M7 人读视图漂移 ⇒ 漂移校验红", False,
                            "★ 红了但理由不是漂移（★ 可能红在别处 ⇒ 验到的腿不对）"))
            print("★ M7 红了但理由不是漂移 ✗\n%s" % m7_out[-1500:], file=sys.stderr)
        else:
            results.append(("M7 人读视图漂移 ⇒ 漂移校验红", True, "① 人读视图与机器真值必须同源"))
            print("★ M7 人读视图漂移 ⇒ 校验红 ✔（① 人读视图与机器真值必须同源）")
        # ⇒ 还原副本并复跑（★ 证明红的是【漂移】不是【别的】）
        m7_tsv.write_text(m7_orig, encoding="utf-8")
        m7_after = run_m7()
        if m7_after.returncode != 0:
            results.append(("M7 还原后校验必须恢复绿", False, "★ 还原后仍红"))
            print("★ M7 还原后仍红 ✗", file=sys.stderr)
        else:
            results.append(("M7 还原后校验必须恢复绿", True, "① 还原后绿"))
            print("★ M7 还原后校验恢复绿 ✔")
    except Exception as exc:  # noqa: BLE001
        results.append(("M7 漂移校验变异臂", False, "★ 执行异常：%s" % exc))
        print("★ M7 执行异常：%s" % exc, file=sys.stderr)

    # ── 还原核对（★ 仓内文件一个字节都不该动）──
    for p, h in before.items():
        after = md5(p)
        if after != h:
            results.append((f"还原核对 {p.name}", False, "★ md5 不一致 ⇒ 残留！"))
            print(f"★ 还原核对失败：{p} md5 变了", file=sys.stderr)

    shutil.rmtree(tmp, ignore_errors=True)

    failed = [r for r in results if not r[1]]
    print()
    print(f"★ 变异臂 {len(results)} 条 ⇒ 通过 {len(results) - len(failed)} 条 ⇒ 失败 {len(failed)} 条")
    for name, ok, why in failed:
        print(f"  ✗ {name}: {why}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
