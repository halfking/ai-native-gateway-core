#!/usr/bin/env python3
"""变异验证 harness（★ 2026-11-11 12h 审计轮 · 第 36 处收尾）。

★ 目的：★ 证明本轮**新写的门**真的有牙（★ 变异打不中 ⇒ 「变异打不中」
★★会被误读成「守卫没牙」⇒ ★★ 那是 LESSONS §680 的同款坑）★★。

★ 三条纪律（★ 从本仓教训里学的）★★：
 ① **备份取在「补丁之后」** ★ ⇒ ★★ 否则还原会把补丁本身冲掉 ★★。
  ② **还原后按 SHA 复核** ★ ⇒ ★★ 不靠「跑通了」当还原成功的证据 ★★。
  ③ **还原后复跑正向用例** ★ ⇒ ★★ 否则验的可能是另一个状态 ★★。

用法：python3 scripts/audit/mutation-verify-2026-11-11.py [--only M3,M7]
"""
from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
import sys

sys.path.insert(0, str(((__import__("pathlib").Path(__file__).resolve().parent) / ".." / "lib").resolve()))
import gocache  # 仓内专用 Go 构建缓存（r1047 约定；gocache_script_family_guard 门判据①）
import tempfile
from dataclasses import dataclass, field

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))


@dataclass
class Arm:
    name: str
    desc: str
    files: list[str]
    pkg: str
    test: str
    # (old, new) 精确替换；old 必须**恰好出现一次**（否则判失败 ⇒ 防改错地方）
    edits: list[tuple[str, str]]
    expect_in_output: str


ARMS: list[Arm] = [
    Arm(
        name="M1",
        desc="DDL 副本漂移：删掉 baseline 闭集里的一个字面量",
        files=["sql/schema/01-schema.sql"],
        pkg="./sql/migrations/startup/",
        test="TestSelfCheckErrorTypeClosureCoversEveryDeclaredFace",
        edits=[("'account_restricted'", "'account_restricted_MUTANT'")],
        expect_in_output="account_restricted_MUTANT",
    ),
    Arm(
        name="M2",
        desc="阈值下限漂移：settings 常量 2 → 1",
        files=["settings/spec_thresholds.go"],
        pkg="./settings/",
        test="TestEmptyResponseStreakThresholdBoundsAreSingleSourced",
        edits=[("MinEmptyResponseStreakThreshold = 2", "MinEmptyResponseStreakThreshold = 1")],
        expect_in_output="契约值是 2",
    ),
    Arm(
        name="M3",
        desc="reducer 下限守卫退回 n > 0",
        files=["domains/nodehealth/reducer.go"],
        pkg="./domains/nodehealth/",
        test="TestEmptyResponseStreakBelowDeclaredBoundFallsBackToDefault",
        edits=[
            (
                "if n := fn(); n >= settings.MinEmptyResponseStreakThreshold {",
                "if n := fn(); n > 0 {",
            )
        ],
        expect_in_output="越界阈值",
    ),
    Arm(
        name="M4",
        desc="featured 查询出错 ⇒ 退化成空集合（fail-open）",
        files=["bg/featured_sources.go"],
        pkg="./bg/",
        test="TestQueryStaticFeaturedModels_QueryErrorIsNotEmpty",
        edits=[
            (
                "\trows, err := db.Query(ctx, `WITH `+StaticFeaturedCTESQL()+` SELECT model FROM static`, tenant)\n\tif err != nil {\n\t\treturn nil, err\n\t}",
                "\trows, err := db.Query(ctx, `WITH `+StaticFeaturedCTESQL()+` SELECT model FROM static`, tenant)\n\tif err != nil {\n\t\treturn nil, nil\n\t}",
            )
        ],
        expect_in_output="fail-closed",
    ),
    Arm(
        name="M5",
        desc="给一侧分类器加一条未登记的正文规则",
        files=["errorsx/classify.go"],
        pkg="./errorsx/",
        test="TestDualClassifierRuleSetDivergenceIsRegistered",
        edits=[
            (
                "var eofWithoutDoneRe = regexp.MustCompile(",
                "var mutantOnlyRuleRe = regexp.MustCompile(`mutant-rule-marker`)\n\nvar eofWithoutDoneRe = regexp.MustCompile(",
            ),
            (
                "func ClassifyErrorWithBody(status int, body []byte) ErrorKind {",
                "func ClassifyErrorWithBody(status int, body []byte) ErrorKind {\n\tif mutantOnlyRuleRe.Match(body) {\n\t\treturn KindTransient\n\t}",
            ),
        ],
        expect_in_output="mutantOnlyRuleRe",
    ),
    Arm(
        name="M6",
        desc="登记的单边规则从一侧消失（登记漂移）",
        files=["errorsx/classify.go"],
        pkg="./errorsx/",
        test="TestDualClassifierRuleSetDivergenceIsRegistered",
        edits=[
            (
                "if eofWithoutDoneRe.Match(body) && SeverityOf(classifyStatusFallback(status)) == SeverityCredential {",
                "if SeverityOf(classifyStatusFallback(status)) == SeverityCredential {",
            )
        ],
        expect_in_output="不单边",
    ),
    Arm(
        name="M7",
        desc="protocolformat 闭集多一项",
        files=["internal/protocolformat/validate.go"],
        pkg="./internal/ir/",
        test="TestProtocolBlockClosureThreeWayReconciliation",
        edits=[
            (
                '"search_result": true, "container_upload": true, "citations": true,',
                '"search_result": true, "container_upload": true, "citations": true,\n\t"mutant_block": true,',
            )
        ],
        expect_in_output="mutant_block",
    ),
    Arm(
        name="M8",
        desc="测试注释里塞回 guard_content",
        files=["internal/ir/responses_reasoning_item_anthropic_test.go"],
        pkg="./internal/ir/",
        test="TestProtocolBlockClosureThreeWayReconciliation",
        edits=[
            (
                "// server_tool_use, web_search_tool_result, search_result,",
                "// server_tool_use, web_search_tool_result, guard_content, search_result,",
            )
        ],
        expect_in_output="guard_content",
    ),
    Arm(
        name="M9",
        desc="去掉记录文件的数量上界淘汰",
        files=["domains/contextagent/record_retention.go"],
        pkg="./domains/contextagent/",
        test="TestFileRecorder_RetentionBoundsFileCount",
        edits=[
            (
                "if f.policy.MaxFiles > 0 && len(files) > f.policy.MaxFiles {",
                "if f.policy.MaxFiles > 1000000 && len(files) > f.policy.MaxFiles {",
            )
        ],
        expect_in_output="超过上界",
    ),
    Arm(
        name="M10",
        desc="去掉记录文件的 TTL 淘汰",
        files=["domains/contextagent/record_retention.go"],
        pkg="./domains/contextagent/",
        test="TestFileRecorder_RetentionRemovesExpiredByTTL",
        edits=[
            (
                "if f.policy.TTL > 0 {\n\t\tcutoff := now.Add(-f.policy.TTL)",
                "if f.policy.TTL > 1000000*time.Hour {\n\t\tcutoff := now.Add(-f.policy.TTL)",
            )
        ],
        expect_in_output="TTL 淘汰后目录",
    ),
    Arm(
        name="M11",
        desc="去掉「刚写入的文件绝不被删」的保护",
        files=["domains/contextagent/record_retention.go"],
        pkg="./domains/contextagent/",
        test="TestFileRecorder_SweepNeverRemovesJustWrittenRecord",
        edits=[
            (
                "\t\t\tif it.path == keepPath {\n\t\t\t\tcontinue\n\t\t\t}",
                "\t\t\tif it.path == \"__never_matches__\" {\n\t\t\t\tcontinue\n\t\t\t}",
            )
        ],
        expect_in_output="刚写入的文件被删了",
    ),
    Arm(
        name="M12",
        desc="去掉清扫限频（退化成每次 Save 都 O(n)）",
        files=["domains/contextagent/record_retention.go"],
        pkg="./domains/contextagent/",
        test="TestFileRecorder_SweepIsRateLimited",
        edits=[
            (
                "\tif !due && !batch {\n\t\treturn\n\t}",
                "\tif !due && !batch && false {\n\t\treturn\n\t}",
            )
        ],
        expect_in_output="清扫没有限频",
    ),
    Arm(
        name="M13",
        desc="去掉请求 ctx 的 deadline（Timeout 退回死字段）",
        files=["domains/contextagent/service.go"],
        pkg="./domains/contextagent/",
        test="TestLoopbackSubmitter_TimeoutBoundsInjectedClient",
        edits=[
            (
                "\tctx, cancel := context.WithTimeout(ctx, timeout)\n\tdefer cancel()\n",
                "",
            )
        ],
        expect_in_output="没有被 Timeout 约束住",
    ),
    Arm(
        name="M14",
        desc="默认超时契约值被改小（120s → 2s）",
        files=["domains/contextagent/service.go"],
        pkg="./domains/contextagent/",
        test="TestLoopbackSubmitter_ZeroTimeoutFallsBackToDefault",
        edits=[
            (
                "const defaultLoopbackTimeout = 120 * time.Second",
                "const defaultLoopbackTimeout = 2 * time.Second",
            )
        ],
        expect_in_output="契约值是 120s",
    ),
]


def sha(path: str) -> str:
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def run_go(pkg: str, test: str) -> tuple[int, str]:
    # ★ -timeout 90s：★ 变异可能让某个用例**挂死**（★ 例：去掉 ctx 的
    # ★ deadline ⇒ ★ 请求永不取消）⇒ ★★ 若不设超时，★ 一次变异验证要拖
    # ★★ go test 的默认 10 分钟 ⇒ ★★ 且报错定位很差 ★★。
    proc = subprocess.run(
        ["go", "test", pkg, "-run", test, "-count=1", "-timeout", "90s"],
        cwd=REPO,
        env=gocache.env("mutate"),
        capture_output=True,
        text=True,
    )
    return proc.returncode, proc.stdout + proc.stderr


def main() -> int:
    only = None
    if "--only" in sys.argv:
        only = set(sys.argv[sys.argv.index("--only") + 1].split(","))

    backup = tempfile.mkdtemp(prefix="mut-verify-backup-")
    try:
        # ★ 纪律①：备份取在**所有补丁之后**（★ 现在）。
        targets = sorted({f for arm in ARMS for f in arm.files})
        for rel in targets:
            src = os.path.join(REPO, rel)
            dst = os.path.join(backup, rel)
            os.makedirs(os.path.dirname(dst), exist_ok=True)
            shutil.copy2(src, dst)
        baseline = {rel: sha(os.path.join(backup, rel)) for rel in targets}
        print(f"备份目录（补丁后状态）：{backup}")
        print(f"已备份 {len(targets)} 个文件\n")

        # ★ 正向基线：★ 变异前必须全绿（★ 否则测的是别的状态）。
        print("── 正向基线 ──")
        base_ok = True
        for arm in ARMS:
            if only and arm.name not in only:
                continue
            rc, out = run_go(arm.pkg, arm.test)
            status = "rc=0 ✔" if rc == 0 else f"rc={rc} ✘"
            print(f"  {arm.name} {arm.test[:52]:<52} {status}")
            if rc != 0:
                base_ok = False
                print(out[-3000:])
        if not base_ok:
            print("\n✘ 正向基线不绿 ⇒ 停（★ 变异验证的前提不成立）")
            return 2
        print()

        results = []
        for arm in ARMS:
            if only and arm.name not in only:
                continue
            print(f"── {arm.name}：{arm.desc} ──")
            applied = True
            for rel in arm.files:
                path = os.path.join(REPO, rel)
                with open(path, "r", encoding="utf-8") as fh:
                    body = fh.read()
                for old, new in arm.edits:
                    cnt = body.count(old)
                    if cnt != 1:
                        print(f"  ✘ 变异锚点在 {rel} 出现 {cnt} 次（要求恰好 1 次）⇒ 判失败")
                        applied = False
                        break
                    body = body.replace(old, new)
                if not applied:
                    break
                with open(path, "w", encoding="utf-8") as fh:
                    fh.write(body)
            if not applied:
                results.append((arm.name, "锚点不唯一", False))
                print()
                continue

            rc, out = run_go(arm.pkg, arm.test)
            red = rc != 0
            precise = arm.expect_in_output in out
            verdict = "被抓 ✔" if (red and precise) else ("红了但定位不准 ✘" if red else "没被抓 ✘")
            print(f"  {verdict}（rc={rc}，期望输出含 {arm.expect_in_output!r}：{precise}）")
            if not precise:
                print("  ---- 输出尾部 ----")
                print("\n".join(out.splitlines()[-25:]))
            results.append((arm.name, verdict, red and precise))

            # ★ 纪律②：还原（★ 用备份副本 ★不用 git checkout）★ 并按 SHA 复核。
            for rel in arm.files:
                shutil.copy2(os.path.join(backup, rel), os.path.join(REPO, rel))
            sha_ok = all(sha(os.path.join(REPO, rel)) == baseline[rel] for rel in arm.files)
            print(f"  还原后 SHA 复核：{'一致 ✔' if sha_ok else '不一致 ✘'}")
            if not sha_ok:
                results.append((arm.name, "还原后 SHA 不一致", False))
                print()
                continue
            # ★ 纪律③：还原后复跑正向用例（★ 确认还原的是「补丁已在」的状态）。
            rc2, _ = run_go(arm.pkg, arm.test)
            print(f"  还原后正向复跑：{'rc=0 ✔' if rc2 == 0 else f'rc={rc2} ✘'}")
            if rc2 != 0:
                results.append((arm.name, "还原后正向不绿", False))
            print()

        print("── 汇总 ──")
        ok = True
        for name, verdict, good in results:
            print(f"  {name:<4} {verdict}")
            ok = ok and good
        print("\n总判定：", "全部通过 ✔" if ok else "有未通过项 ✘")
        return 0 if ok else 1
    finally:
        shutil.rmtree(backup, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
