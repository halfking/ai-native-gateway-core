#!/usr/bin/env python3
"""变异验证 harness（★ 2026-11-11 12h 审计轮 · 第 36 处收尾）。

★ 目的：★ 证明本轮**新写的门**真的有牙（★ 变异打不中 ⇒ 「变异打不中」
★★会被误读成「守卫没牙」⇒ ★★ 那是 LESSONS §680 的同款坑）★★。

★ 三条纪律（★ 从本仓教训里学的）★★：
 ① **备份取在「补丁之后」** ★ ⇒ ★★ 否则还原会把补丁本身冲掉 ★★。
  ② **还原后按 SHA 复核** ★ ⇒ ★★ 不靠「跑通了」当还原成功的证据 ★★。
  ③ **还原后复跑正向用例** ★ ⇒ ★★ 否则验的可能是另一个状态 ★★。

★★ **2026-11-11（协议转换审计轮）扩充**：★★
  · 新增 **group** 字段 ⇒ CI 可按组调用（★ `--group toolcall-canonical`）
    ⇒ ★★ **不把臂名单手抄进 Makefile**（★★ 名单手抄 = 必然漂移 = LESSONS 同款坑）★★。
  · 新增 **① 变异生效的独立证据 = 目标文件 SHA 变化** ⇒ ★★ 原来只有
    「锚点命中恰好 1 次」⇒ ★★ 那是**同源**证据（★ 锚点与替换在同一份
    脚本里 ⇒ 同源变异可能把锚点一起改掉）⇒ ★★ 现在补一条**独立**证据 ★★。
  · 新增 `--group` **fail-closed**：★ 组内 0 臂 ⇒ rc≠0（★ 「读不到判据」
    ≠ 「没发现问题」★）★★
  · ★★ **未知参数必须报错**：★★ `--group` / `--only` 写错时**不许**静默忽略
    ⇒ ★★ 静默忽略 = 「调了一个不存在的组，harness 跑别的臂，退出 0」
    ⇒ ★★ ⇒ **门绿着，而它要保护的东西根本没被跑到** ⇒ ★★ 正是
    ★★ LESSONS「守卫自己也要被审计」的同款 ⇒ ★★ 现在 rc≠0 ★★。

用法：python3 scripts/audit/mutation-verify-2026-11-11.py [--only M3,M7]
      python3 scripts/audit/mutation-verify-2026-11-11.py --group toolcall-canonical
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
    # ★ 2026-11-11（协议转换审计轮）：分组 ⇒ CI 按组调用 ⇒ **不手抄臂名单**。
    #   ★ 空串 = 未分组（⇒ `--group` 选不中 ⇒ 保持旧行为）。
    group: str = ""


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


# ══════════════════════════════════════════════════════════════════════════
# ★★★ 2026-11-11（协议转换审计轮 · D1–D5）★★★
#
# ★ 这组臂守的是「工具调用的**规范视图**」这一族修复 ★
#   （internal/ir/tool_calls_canonical.go + 三个序列化器 + 清洗链三处）。
#
# ★ **为什么单独成组**：★ 这一族修复的共同根因是「IR 里同一个工具调用有
#   **两种表示**（内容块 tool_use / 消息级 ToolCalls）」⇒ ⇒ 修复面横跨
#   **6 个生产文件** ⇒ ⇒ 任何一处回退都可能让缺陷复活 ⇒ ⇒ 必须**逐处**有臂。
#
# ★ **每条臂对应一个可独立回退的决策点**：
#   TC01 D1 Gemini 侧重复发 functionCall（内容块分支被放行）
#   TC02 规范视图的去重规则被拆掉（D1 的根因层）
#   TC03 D5 OpenAI 侧丢工具调用（退回只读消息级 ToolCalls）
#   TC04 D5 Responses 侧丢工具调用（退回只读消息级 ToolCalls）
#   TC05 D2 内容块 tool_result 的 functionResponse.name 退回「从 ID 猜名字」
#   TC06 D2 消息级工具结果的 functionResponse.name 退回「从 ID 猜名字」
#   TC07 D4 工具结果载荷发两次（consumedAsResponse 被置回 false）
#   TC08 D3① removeEmptyMessages 把 Gemini 工具结果当空消息删除
#   TC09 D3② expandToolResultMessages 不展开 tool 角色消息
#   TC10 D3③ SanitizeToolMessages 的活跃 ID 集不含内容块 tool_use 的 ID
# ══════════════════════════════════════════════════════════════════════════
TOOLCALL_GROUP = "toolcall-canonical"

ARMS += [
    Arm(
        name="TC01",
        desc="D1：Gemini 序列化器的内容块 tool_use 分支被放行 ⇒ 同一调用发两次",
        files=["internal/ir/serialize_gemini.go"],
        pkg="./internal/ir/",
        test="TestToolCallCanonical_GeminiNoDuplicateFunctionCall",
        edits=[
            (
                "\t\t\tcase \"tool_use\":\n"
                "\t\t\t\t// ★ 2026-11-11（D1）：内容块 tool_use 已由上面的**规范视图**\n"
                "\t\t\t\t//   （canonicalToolCalls）统一发出 ⇒ 此处**不再**重复发，\n"
                "\t\t\t\t//   否则同一个 functionCall 会被发两次。\n"
                "\t\t\t\tcontinue\n",
                "\t\t\tcase \"tool_use\":\n"
                "\t\t\t\tif block.ToolUse != nil {\n"
                "\t\t\t\t\tparts = append(parts, map[string]any{\n"
                "\t\t\t\t\t\t\"functionCall\": map[string]any{\n"
                "\t\t\t\t\t\t\t\"name\": block.ToolUse.Name,\n"
                "\t\t\t\t\t\t\t\"args\": block.ToolUse.Input,\n"
                "\t\t\t\t\t\t},\n"
                "\t\t\t\t\t})\n"
                "\t\t\t\t}\n",
            )
        ],
        expect_in_output="只发一个 functionCall",
        group=TOOLCALL_GROUP,
    ),
    Arm(
        name="TC02",
        desc="规范视图的去重规则被拆掉（D1 的根因层：两种表示各发一次）",
        files=["internal/ir/tool_calls_canonical.go"],
        pkg="./internal/ir/",
        test="TestToolCallCanonical_OpenAINoDuplicateToolCalls",
        edits=[
            (
                "\tfor i, tc := range msg.ToolCalls {\n"
                "\t\tif representedToolCallIndexes(msg)[i] {\n"
                "\t\t\tcontinue\n"
                "\t\t}\n"
                "\t\tout = append(out, tc)\n"
                "\t}\n",
                "\tfor _, tc := range msg.ToolCalls {\n"
                "\t\tout = append(out, tc)\n"
                "\t}\n",
            )
        ],
        expect_in_output="只发一个 tool_calls",
        group=TOOLCALL_GROUP,
    ),
    Arm(
        name="TC03",
        desc="D5：OpenAI 序列化器退回只读消息级 ToolCalls ⇒ 内容块形态的调用消失",
        files=["internal/ir/serialize_openai.go"],
        pkg="./internal/ir/",
        test="TestToolCallCanonical_GeminiToolCallSurvivesToOpenAI",
        edits=[
            (
                "\ttoolCalls := canonicalToolCalls(msg)",
                "\ttoolCalls := msg.ToolCalls",
            )
        ],
        expect_in_output="不得被当成孤儿工具结果而拒绝整条请求",
        group=TOOLCALL_GROUP,
    ),
    Arm(
        name="TC04",
        desc="D5：Responses 序列化器退回只读消息级 ToolCalls ⇒ 内容块形态的调用消失",
        files=["internal/ir/serialize_responses.go"],
        pkg="./internal/ir/",
        test="TestToolCallCanonical_GeminiToolCallSurvivesToResponses",
        edits=[
            (
                "\t\tif msg.Role == \"assistant\" && len(canonicalToolCalls(msg)) > 0 {",
                "\t\tif msg.Role == \"assistant\" && len(msg.ToolCalls) > 0 {",
            ),
            (
                "\t\t\tfor _, call := range canonicalToolCalls(msg) {",
                "\t\t\tfor _, call := range msg.ToolCalls {",
            ),
        ],
        expect_in_output="不得消失",
        group=TOOLCALL_GROUP,
    ),
    Arm(
        name="TC05",
        desc="D2：内容块 tool_result 的 functionResponse.name 退回「从 ID 猜名字」",
        files=["internal/ir/serialize_gemini.go"],
        pkg="./internal/ir/",
        test="TestToolCallCanonical_GeminiFunctionResponseNameMatchesCall",
        edits=[
            (
                "\t\t\t\t\tname := toolNames[block.ToolResult.ToolUseID]\n"
                "\t\t\t\t\tif name == \"\" {\n"
                "\t\t\t\t\t\tname = toolUseNameFromID(block.ToolResult.ToolUseID)\n"
                "\t\t\t\t\t}\n",
                "\t\t\t\t\tname := toolUseNameFromID(block.ToolResult.ToolUseID)\n",
            )
        ],
        expect_in_output="functionResponse.name 必须等于 functionCall.name",
        group=TOOLCALL_GROUP,
    ),
    Arm(
        name="TC06",
        desc="D2：消息级工具结果的 functionResponse.name 退回「从 ID 猜名字」",
        files=["internal/ir/serialize_gemini.go"],
        pkg="./internal/ir/",
        test="TestToolCallCanonical_GeminiFunctionResponseNameMatchesCall",
        edits=[
            (
                "\t\t\tname := msg.Name\n"
                "\t\t\tif name == \"\" {\n"
                "\t\t\t\tname = toolNames[msg.ToolCallID]\n"
                "\t\t\t}\n",
                "\t\t\tname := msg.Name\n",
            )
        ],
        expect_in_output="functionResponse.name 必须等于 functionCall.name",
        group=TOOLCALL_GROUP,
    ),
    Arm(
        name="TC07",
        desc="D4：工具结果载荷发两次（consumedAsResponse 被置回 false）",
        files=["internal/ir/serialize_gemini.go"],
        pkg="./internal/ir/",
        test="TestToolCallCanonical_GeminiToolResultNotDuplicated",
        edits=[
            (
                "\t\t\t\tconsumedAsResponse = true",
                "\t\t\t\tconsumedAsResponse = false",
            )
        ],
        expect_in_output="不得再作为独立 text part 发一遍",
        group=TOOLCALL_GROUP,
    ),
    Arm(
        name="TC08",
        desc="D3①：removeEmptyMessages 退回「无 tool_call_id 即空消息」⇒ Gemini 工具结果被删",
        files=["internal/ir/validate_and_fix.go"],
        pkg="./internal/ir/",
        test="TestToolCallCanonical_GeminiClientToolResultSurvivesFix",
        edits=[
            (
                "\t\t\tif msg.ToolCallID == \"\" && !hasToolResultBlock(msg.Content) {",
                "\t\t\tif msg.ToolCallID == \"\" {",
            )
        ],
        expect_in_output="不得被 ValidateAndFixRequest 删除",
        group=TOOLCALL_GROUP,
    ),
    Arm(
        name="TC09",
        desc="D3②：expandToolResultMessages 不展开 tool 角色消息 ⇒ 关联键恒空 ⇒ 被删",
        files=["internal/ir/tool_result_messages.go"],
        pkg="./internal/ir/",
        test="TestToolCallCanonical_GeminiClientToolResultSurvivesFix",
        edits=[
            (
                "\t\tisToolShape := (msg.Role == \"tool\" || msg.Role == \"function\") && msg.ToolCallID == \"\"",
                "\t\tisToolShape := false",
            )
        ],
        expect_in_output="不得被 ValidateAndFixRequest 删除",
        group=TOOLCALL_GROUP,
    ),
    Arm(
        name="TC10",
        desc="D3③：SanitizeToolMessages 的活跃调用 ID 集不含内容块 tool_use 的 ID ⇒ 孤儿被删",
        files=["internal/ir/sanitize_tool_messages.go"],
        pkg="./internal/ir/",
        test="TestToolCallCanonical_GeminiClientToolResultSurvivesFix",
        edits=[
            (
                "\t\t\tfor _, block := range msg.Content {\n"
                "\t\t\t\tif block.Type == \"tool_use\" && block.ToolUse != nil && block.ToolUse.ID != \"\" {\n"
                "\t\t\t\t\tactiveToolCallIds[block.ToolUse.ID] = true\n"
                "\t\t\t\t}\n"
                "\t\t\t}\n",
                "",
            )
        ],
        expect_in_output="不得被 ValidateAndFixRequest 删除",
        group=TOOLCALL_GROUP,
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
    # ★★ 未知参数必须报错（★ 2026-11-11 协议转换审计轮实测踩到）：★★
    #   ★ 本轮把 `--group toolcall-canonical` 写进 Makefile ⇒ 但 harness 的
    #   ★ 那一版**不认识** `--group` ⇒ ★★ 旧写法会**静默忽略**它 ⇒ ★★
    #   ★★ 于是 harness 跑的是**别的** 14 条臂、退出 0 ⇒ ★★ 门绿着，★★
    #   ★★ 而它要保护的那 10 条臂**根本没被跑到** ⇒ ★★ 这正是 LESSONS
    #   ★★「守卫自己也要被审计」的同款 ⇒ ★★ 现在：未知参数 ⇒ rc≠0 ★★。
    argv = sys.argv[1:]
    known_flags = {"--only", "--group"}
    for i, tok in enumerate(argv):
        if tok in known_flags:
            continue
        if tok.startswith("--"):
            print(f"✘ 未知参数 {tok!r}（已知：{sorted(known_flags)}）⇒ ⇒ "
                  f"【调用方与本 harness 的接口不一致】⇒ ⇒ fail-closed（rc≠0）")
            return 2
        # 位置参数（--only/--group 的取值）由下面按 flag 消费。

    only = None
    if "--only" in argv:
        idx = argv.index("--only")
        if idx + 1 >= len(argv):
            print("✘ --only 缺取值 ⇒ ⇒ fail-closed（rc≠0）")
            return 2
        only = set(argv[idx + 1].split(","))
    # ★ 2026-11-11（协议转换审计轮）：--group ⇒ CI 按组调用 ⇒ **不手抄臂名单**。
    group = None
    if "--group" in argv:
        idx = argv.index("--group")
        if idx + 1 >= len(argv):
            print("✘ --group 缺取值 ⇒ ⇒ fail-closed（rc≠0）")
            return 2
        group = argv[idx + 1]

    def selected(arm: Arm) -> bool:
        if group is not None:
            return arm.group == group
        if only is not None:
            return arm.name in only
        return True

    # ★ fail-closed：★ 选中的组里 0 条臂 ⇒ ⇒ 「判据不存在」≠ 「没发现问题」
    #   ⇒ ⇒ 直接 rc≠0 ⇒ ⇒ 不许静默跳过（★ 与「读不到锚点档 ⇒ fail-closed」同款）。
    picked = [a for a in ARMS if selected(a)]
    if group is not None and not picked:
        print(f"✘ 组 {group!r} 里没有任何臂 ⇒ ⇒ 【判据不存在】⇒ ⇒ fail-closed（rc≠0）")
        return 2
    if only is not None and not picked:
        print(f"✘ --only {sorted(only)} 没有命中任何臂 ⇒ ⇒ 【判据不存在】⇒ ⇒ fail-closed（rc≠0）")
        return 2

    backup = tempfile.mkdtemp(prefix="mut-verify-backup-")
    try:
        # ★ 纪律①：备份取在**所有补丁之后**（★ 现在）。
        targets = sorted({f for arm in picked for f in arm.files})
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
        for arm in picked:
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
        for arm in picked:
            print(f"── {arm.name}：{arm.desc} ──")
            applied = True
            mut_sha: dict[str, str] = {}
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
                mut_sha[rel] = sha(path)
            if not applied:
                results.append((arm.name, "锚点不唯一", False))
                print()
                continue

            # ★ 纪律①的**独立证据**：★ 变异必须真的改了文件（★ SHA 变化）★★
            #   ⇒ ★★ 「锚点命中 1 次」是**同源**证据（★ 锚点与替换物同在这份
            #   ⇒ ★★ 脚本里 ⇒ 同源变异可能把锚点一起改掉 ⇒ ★★ 门被改瞎）
            #   ⇒ ⇒ ★★ 这里用**目标文件的 SHA**当独立证据 ★★。
            unchanged = [rel for rel in arm.files if mut_sha.get(rel) == baseline[rel]]
            if unchanged:
                print(f"  ✘ 变异后目标文件 SHA 未变（{', '.join(unchanged)}）⇒ ⇒ 变异**没生效**"
                      f"⇒ ⇒ 不能判「门没牙」")
                results.append((arm.name, "变异未生效（SHA 未变）", False))
            else:
                print(f"  变异生效证据：目标文件 SHA 已变化（{', '.join(arm.files)}）✔")

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
