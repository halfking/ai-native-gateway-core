#!/usr/bin/env python3
"""pi-agent-mutation-test.py —— PI 智能体审计轮的**变异测试**驱动（2026-11-17）。

# 这道门在钉什么

「守门测试」只有在**它对该死的缺陷会红**时才是门。
一个全绿的守门测试套件，可能只是因为**断言写错了**、或者
「守门测试与被守对象同步改坏」而全绿（本仓 §708① 的教训：
门只做「投影含 COALESCE」这种**同源自证**，投影引用了库里没有的列它也看不见）。

⇒ 所以每一条守门测试都必须跑一遍**变异**：把实现改坏成缺陷形态，
断言「对应的守门测试**变红**」⇒ 证明这道门**有牙**。

# 三条纪律（本脚本强制）

1. **编译红不算数**：变异后若包编译不过，只能说明代码坏了，
   不能说明判据有牙 ⇒ 这样的变异记为 INVALID，不计入通过。
2. **必须能还原**：每个变异都从**原文件**重新生成（不是「反向替换」），
   ⇒ 还原一定等于原状（`bytes` 级）。
3. **锚点必须唯一**：锚点在文件里出现次数 ≠ 1 ⇒ 拒绝执行（退出码 2），
   避免「改到了别的地方」这种假阴性（★ 本仓已踩过：锚点未命中 ≠ 守卫失效，
   它意味着变异压根没发生）。

# 用法

    python3 scripts/audit/pi-agent-mutation-test.py            # 跑全部
    python3 scripts/audit/pi-agent-mutation-test.py M01 M04    # 只跑指定变异

退出码：0 = 全部变异都「改坏 ⇒ 对应测试变红」；1 = 有变异没红（门没牙）；
2 = 脚本自身出错（锚点不唯一 / 文件缺失）。
"""

import os
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

REPO = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))


class Mutation:
    def __init__(self, mid, file, old, new, pkg, run, note):
        self.id = mid
        self.file = file
        self.old = old
        self.new = new
        self.pkg = pkg
        self.run = run
        self.note = note

    def apply(self, text):
        n = text.count(self.old)
        if n != 1:
            raise AnchorError(f"锚点在 {self.file} 里出现 {n} 次（期望 1）")
        return text.replace(self.old, self.new, 1)


class AnchorError(Exception):
    pass


PI = "./domains/promptinjection/"
AR = "./autoroute/"
GW = "./cmd/gateway/"

MUTATIONS = [
    # ── Area 3：PI 检测第二/第三层 ───────────────────────────────────────
    Mutation(
        "M01", "domains/promptinjection/detector.go",
        "tail := tailUTF8Bytes(input, tailBudget)",
        "tail := truncateUTF8Bytes(input[len(head):], tailBudget)",
        PI, "TestTruncateForDetection",
        "F-PI-05：尾部取自「头部之后」⇒ 前置填充时尾部载荷根本没被保住",
    ),
    Mutation(
        "M02", "domains/promptinjection/detector.go",
        "return head + marker + tail",
        "\t_ = marker\n\t_ = tail\n\treturn head",
        PI, "TestTruncateForDetection",
        "F-PI-05：退回「只留头部」⇒ 攻击者前置填充即可藏载荷",
    ),
    Mutation(
        "M03", "domains/promptinjection/detector.go",
        "override := regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(ignore|disregard|forget|override|bypass|discard)(?:[^a-z0-9]|$)`)",
        "override := regexp.MustCompile(`(?i)\\b(ignore|disregard|forget|override|bypass|discard)\\b`)",
        PI, "TestHeuristicEngine_DetectsStructuralInjection",
        "L3：\\b 把下划线算作单词字符 ⇒ 'ignore_all_previous_instructions' 绕过",
    ),
    Mutation(
        "M04", "domains/promptinjection/detector.go",
        "const detectorLLMEngineSelectCols = `model_canonical_id, credential_id, temperature, max_tokens, timeout_ms,\n\t\t\tsystem_prompt, detection_prompt`",
        "const detectorLLMEngineSelectCols = `model_name, api_key, base_url, confidence_threshold`",
        PI, "TestLLMDetectionEngine_QueryUsesCanonicalColumns|TestProjectedColumnsExistInSchema",
        "F-PI-01：判官配置读四个**不存在**的列 ⇒ 每次调用 42703",
    ),
    Mutation(
        "M05", "domains/promptinjection/detector.go",
        "inputJSON, _ := json.Marshal(truncateUTF8Bytes(neutraliseUntrustedText(input), 4000))",
        "inputJSON, _ := json.Marshal(truncateUTF8Bytes(input, 4000))",
        PI, "TestBuildLLMDetectionPrompt_PayloadCannotCloseFrame",
        "F-PI-06：去掉显式标签中和 ⇒ 载荷的标签不再以惰性记号出现",
    ),
    Mutation(
        "M06", "domains/promptinjection/detector.go",
        '\tflags := "(?s)"\n\tif !caseSensitive {\n\t\tflags = "(?si)"\n\t}\n\treturn regexp.Compile(flags + pattern)',
        "\treturn regexp.Compile(pattern)",
        PI, "TestCompileRulePattern",
        "F-PI-08a：去掉 (?s) ⇒ 多词规则换行即绕过",
    ),
    Mutation(
        "M07", "domains/promptinjection/detector.go",
        "\tif maxSeverity > 10 {\n\t\treturn 10\n\t}\n\treturn maxSeverity",
        "\treturn maxSeverity",
        PI, "TestCalculateScore_ClampedToDBCheckBounds",
        "F-PI-13：评分不再夹到 1..10 ⇒ 写库撞 CHECK 丢审计行",
    ),
    Mutation(
        "M08", "domains/promptinjection/detector.go",
        "\tif v > 10 {\n\t\treturn 10\n\t}\n\treturn v",
        "\tif v > 10 {\n\t\treturn v\n\t}\n\treturn v",
        PI, "TestHeuristicSeverity_AlwaysWithinDBCheckBounds",
        "F-PI-13：启发式 severity 无上界 ⇒ score 可 >10",
    ),
    Mutation(
        "M09", "domains/promptinjection/detector.go",
        "\tsum := sha256.Sum256([]byte(input))\n\treturn hex.EncodeToString(sum[:])",
        '\treturn fmt.Sprintf("%x", 10)',
        PI, "TestAttackVectorHash_UsedForInputHash",
        "F-PI-04：input_hash 存分数的十六进制",
    ),
    Mutation(
        "M10", "domains/promptinjection/detector.go",
        '\t\tpolicyFingerprint = fmt.Sprintf("mode=%s;sample=%.4f;failclosed=%t;timeout=%d",\n\t\t\tpolicy.DetectionMode, policy.LLMDetectionSampleRate,\n\t\t\tpolicy.LLMDetectionFailClosed, policy.LLMDetectionTimeoutSec)',
        '\t\tpolicyFingerprint = "const"',
        PI, "TestLLMJudgeCacheKey_IsolatesTenantAndEngine",
        "F-PI-07：缓存键不含策略指纹 ⇒ 策略收紧后旧结论继续命中",
    ),
    Mutation(
        "M11", "domains/promptinjection/llm_client_gateway.go",
        '\tif strings.TrimSpace(c.baseURL) == "" {\n\t\treturn "", 0, fmt.Errorf("gateway llm client: analysis endpoint not configured")\n\t}',
        '\tif strings.TrimSpace(c.baseURL) == "" {\n\t\treturn "", 0, nil\n\t}',
        PI, "TestGatewayLLMClient_NeverReturnsEmptySuccess",
        "F-PIERR-12：分析客户端退回**空成功桩**",
    ),
    Mutation(
        "M12", "domains/promptinjection/error_analyzer.go",
        "\tparsed.SeverityLevel = normalizeSeverityLevel(parsed.SeverityLevel)\n\tparsed.ImpactScope = normalizeImpactScope(parsed.ImpactScope)",
        "\tparsed.SeverityLevel = parsed.SeverityLevel\n\tparsed.ImpactScope = parsed.ImpactScope",
        PI, "TestParseResponse_NormalisesEnums",
        "F-PIERR-13：枚举不归一 ⇒ LLM 自由文本撞 23514 丢整条结果",
    ),
    Mutation(
        "M13", "domains/promptinjection/pi_agent_helpers.go",
        '\t// ① 分隔符切词（high-risk / high_risk / high risk / HIGH-RISK）。\n'
        '\tfor _, tok := range splitEnumTokens(s) {\n'
        '\t\tswitch tok {\n'
        '\t\tcase "low", "minor", "info", "informational":\n'
        '\t\t\treturn "low"\n'
        '\t\tcase "medium", "med", "moderate", "normal":\n'
        '\t\t\treturn "medium"\n'
        '\t\tcase "high":\n'
        '\t\t\treturn "high"\n'
        '\t\tcase "critical", "crit", "severe", "blocker":\n'
        '\t\t\treturn "critical"\n'
        '\t\t}\n'
        '\t}\n'
        '\t// ② 子串兜底（p0 / sev1 / "严重性高"）。顺序即优先级。\n'
        '\tif strings.Contains(s, "critical") || strings.Contains(s, "crit") ||\n'
        '\t\tstrings.Contains(s, "severe") || strings.Contains(s, "blocker") ||\n'
        '\t\tstrings.Contains(s, "致命") || strings.Contains(s, "紧急") {\n'
        '\t\treturn "critical"\n'
        '\t}\n'
        '\tif strings.Contains(s, "high") || strings.Contains(s, "严重") {\n'
        '\t\treturn "high"\n'
        '\t}\n'
        '\tif strings.Contains(s, "medium") || strings.Contains(s, "moderate") {\n'
        '\t\treturn "medium"\n'
        '\t}\n'
        '\tif strings.Contains(s, "low") || strings.Contains(s, "轻微") {\n'
        '\t\treturn "low"\n'
        '\t}\n'
        '\treturn "medium"\n',
        '\t// ★ 变异：退回**修复前**的实现（只有全等分支，没有任何兜底）。\n'
        '\treturn "medium"\n',
        PI, "TestParseResponse_NormalisesEnums",
        "F-PIERR-13b：枚举归一完全失效 ⇒ 'high-risk' / 'global-ish' 漏到默认值",
    ),
    # ── Area 1：错误收集与分析管线 ───────────────────────────────────────
    Mutation(
        "M14", "domains/promptinjection/error_collector.go",
        "if limit := ec.maxDepth.Load(); limit > 0 {",
        "if limit := int64(0); limit > 0 {",
        PI, "TestErrorCollector_Collect_RespectsQueueDepthLimit",
        "队列深度闸失效 ⇒ 错误风暴把持久队列撑成只涨不消的黑洞",
    ),
    Mutation(
        "M15", "domains/promptinjection/error_collector.go",
        "NULLIF($10, '')::text::jsonb",
        "$10::text::jsonb",
        PI, "TestErrorCollector_Collect_JsonbColumnsUseNullIfTextCast",
        "F-PIERR-01：空串直接过 ::text::jsonb ⇒ 22P02",
    ),
    Mutation(
        "M16", "domains/promptinjection/error_analyzer.go",
        "\tea.batchSize = n",
        "\t_ = n",
        PI, "TestErrorAnalyzer_SetBatchSize_ChangesClaimSize",
        "单批条数配置项形同虚设（自述与实现不符）",
    ),
    # ── Area 2：auto 第二层 ─────────────────────────────────────────────
    Mutation(
        "M17", "autoroute/classifier_llm.go",
        "\tcase matchFuzzy:\n\t\treturn 0.55",
        "\tcase matchFuzzy:\n\t\treturn 0.85",
        AR, "TestNormaliseLLMTaskType_Quality",
        "F-AUTO-03：fuzzy 也给 0.85 ⇒ R44 低置信护栏恒不触发",
    ),
    Mutation(
        "M18", "autoroute/classifier_llm.go",
        "\tsort.SliceStable(candidates, func(i, j int) bool {\n\t\treturn len(string(candidates[i])) > len(string(candidates[j]))\n\t})",
        "\t_ = candidates",
        AR, "TestNormaliseLLMTaskType_LongestMatchWins",
        "F-AUTO-01：模糊匹配不再「最长优先」⇒ code_audit 被 code 吃掉",
    ),
    Mutation(
        "M19", "autoroute/classifier_llm.go",
        "\tif t, ok := matchTaskTypePhraseAlias(s); ok {\n\t\treturn t, matchAlias, true\n\t}",
        "\tif t, ok := matchTaskTypePhraseAlias(s); ok && false {\n\t\treturn t, matchAlias, true\n\t}",
        AR, "TestNormaliseLLMTaskType_LongestMatchWins",
        "短语别名层失效 ⇒ 'security code review' 退化成 code",
    ),
    # ── 接线：管理 API 路由可达性 ───────────────────────────────────────
    Mutation(
        "M20", "admin/pi_analysis_handler.go",
        "\t\treturn AdminMiddleware(fn, h.pool, h.secret)",
        "\t\treturn fn",
        GW, "TestPIAnalysisRoutesReachableOverHTTP",
        "P0：端点**去掉鉴权**（AdminMiddleware 被摘掉）⇒ 未授权即可读内部错误队列 / POST trigger",
    ),
    Mutation(
        "M21", "cmd/gateway/main.go",
        "\t\t\tpiAnalysisHandler.RegisterRoutes(mux, \"/api/admin/pi-analysis\")",
        "\t\t\t_ = piAnalysisHandler",
        GW, "TestPIAnalysisRoutesRegisteredOnMainMux",
        "handler 写好但**没被 wire 到 main mux**（§708① 同型事故）",
    ),
]


def run_go(args, cwd=REPO):
    return subprocess.run(args, cwd=cwd, capture_output=True, text=True)


def main() -> int:
    wanted = sys.argv[1:]
    muts = [m for m in MUTATIONS if not wanted or m.id in wanted]
    if not muts:
        print("没有匹配的变异 ID", file=sys.stderr)
        return 2

    failures = []
    invalids = []
    passed = []

    tmpdir = tempfile.mkdtemp(prefix="pi-mutate-")
    try:
        for m in muts:
            path = os.path.join(REPO, m.file)
            if not os.path.exists(path):
                print(f"[{m.id}] INVALID  文件不存在：{m.file}")
                invalids.append(m.id)
                continue

            with open(path, "rb") as fh:
                original = fh.read()
            original_text = original.decode("utf-8")

            try:
                mutated_text = m.apply(original_text)
            except AnchorError as e:
                print(f"[{m.id}] INVALID  {e}")
                invalids.append(m.id)
                continue

            # 写入变异
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(mutated_text)

            # 纪律①：编译红不算数。
            build = run_go(["go", "build", m.pkg])
            if build.returncode != 0:
                print(f"[{m.id}] INVALID  变异后编译失败（编译红不算数）：")
                print("        " + build.stderr.strip().splitlines()[0] if build.stderr.strip() else "")
                invalids.append(m.id)
                with open(path, "wb") as fh:
                    fh.write(original)
                continue

            # 跑目标守门测试：必须**红**。
            test = run_go(["go", "test", m.pkg, "-run", m.run, "-count=1"])
            if test.returncode == 0:
                print(f"[{m.id}] ★ 没红 —— 守门测试对该缺陷**没有牙**")
                print(f"        缺陷：{m.note}")
                print(f"        期望变红的测试：{m.pkg} -run {m.run}")
                failures.append(m.id)
            else:
                # 断言红的原因是「测试失败」而不是「panic / 编译错误」。
                out = test.stdout + test.stderr
                if "FAIL" not in out:
                    print(f"[{m.id}] INVALID  测试进程异常退出（不是断言失败）")
                    invalids.append(m.id)
                else:
                    print(f"[{m.id}] OK    变异后守门测试变红：{m.note}")
                    passed.append(m.id)

            # 还原（从原文件字节重新写回）。
            with open(path, "wb") as fh:
                fh.write(original)
            # 纪律②：还原必须逐字节一致。
            with open(path, "rb") as fh:
                if fh.read() != original:
                    print(f"[{m.id}] ★ 还原失败（文件与变异前不一致）", file=sys.stderr)
                    failures.append(m.id)

        # 还原后复跑：全部必须绿。
        print("\n── 还原后复跑（必须全绿）──")
        for m in muts:
            test = run_go(["go", "test", m.pkg, "-run", m.run, "-count=1"])
            state = "ok" if test.returncode == 0 else "RED"
            if test.returncode != 0:
                failures.append(m.id)
            print(f"[{m.id}] 还原后 {state}  ({m.pkg} -run {m.run})")
    finally:
        shutil.rmtree(tmpdir, ignore_errors=True)

    print("\n════════ 变异测试小结 ════════")
    print(f"通过（有牙）：{len(passed)}  {passed}")
    if invalids:
        print(f"无效：      {len(invalids)}  {invalids}")
    if failures:
        print(f"★ 失败：    {len(failures)}  {failures}")
        return 1
    print("结论：全部变异的对应守门测试都变红 ⇒ 守门测试有牙。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
