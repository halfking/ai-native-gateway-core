# D11 auto 模型域 子代理报告（窗口：`24c5c545a..3a750b3a7`）

> R75 留档注：本报告为只读子代理（Explore）原文，主代理逐条亲读复核后处置。
> 复核结论：发现 #1 由本轮收口（taskVocabularyAbsentTotal counter）；#4 由本轮收口
> （改名 + 真坍缩用例）；#5 由本轮收口（recorded 对称）；#2/#3/#6/#7/#8 登记。
> 交叉验证表全部核实。见 docs/audit/2026-09-28-r75-24h-audit-round.md。

窗口终点 `3a750b3a7` == 当前 origin/main == 本地 HEAD，交叉验证全部可直接对当前代码核实。窗口内 D11 相关改动面共 8 文件：`autoroute/patterns.go`、`recommend_v2.go`、`scoring.go`、`task_types_ext.go`、`classifier_zh_live_repro_test.go`、`task_vocabulary_regression_test.go`、`cmd/auto-testbench/main.go`、`cmd/auto-testbench/e2e_selection_test.go`。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2 | **fail-open 分支零可观测性：词表被误清空会被静默掩盖**。判定条件是「当前候选池内无任一候选携带该任务任一所需标签」（pool 级 ANY 语义），命中后把该维度静默置 0.5，无任何 log/metric/counter。若迁移事故清空 `models_canonical.tags`，每个请求都会走这条分支降级为纯价格/通道质量排序，生产端唯一发现手段是手工跑 E2E harness（离线门禁不覆盖）。触发路径：tags 全空 → `TaskVocabularyRepresented=false` → 循环置 `TaskMatchScoreUnknown` → 无信号 | `autoroute/recommend_v2.go:127-132`（分支本体，无 log/metric）；`autoroute/scoring.go:427-447`（判定函数） | 在该分支加 per-task counter（如 `autoroute_task_vocabulary_absent_total{task}`）或低频 Warn 日志，使「词表缺失」与「正常路由」在指标面可区分 |
| 2 | P3 | **P1/P3 语言段词表与 P2 不一致**，commit message 宣称「词表与 P2 共用」但实际 P2 多 11 个框架名（django/flask/spring/gin/echo/flutter/nextjs/nuxt/tailwind/html/css/powershell）。同型漏归仍在：`写一个 Django 中间件` → P1 语言段无 django、P2 无「用」、P3 无此前缀 → chat 0.1 | `autoroute/patterns.go:178`（P1 插入段）vs `autoroute/patterns.go:189`（P2 全表） | 把 P2 的框架名并入 P1/P3 语言段（或抽公共词表常量），补一条 repro 测试 |
| 3 | P3 | **量词与语言名之间无空白的变体仍漏归**：`写一个python快速排序`（中文输入常不带空格）——P1 语言段强制 `\s+`，无空格则整组失效；kw.Code 无 "python"；P2 需「用」。静态推演落 chat 0.1。现场修复只覆盖了带空格形式 | `autoroute/patterns.go:178`（`(?:\s+(?:python\|...))?` 的 `\s+`） | 语言段改 `\s*` 或量词后补 `\s*`；先实测确认再加钉桩 |
| 4 | P3 | **`TestRecommendV2_DiscriminatingLowMatchStillFallsBack` 名称与断言相反**：名称说「仍坍缩」，body 实际断言**不**坍缩（1/3=33.3 ≥ 30）。并行审计文档 §4.3 表格按 body 描述（"不坍缩"），与名称矛盾。后果：「词表在场 + winner<30 → 坍缩仍应触发」这一真正场景**无测试钉住**——且在 ANY 匹配语义下，2/3 元 required 列表单命中即 50/33.3 ≥ 30，该分支对除 long_context（5 元，单命中 20<30）外的所有任务实际不可达，坍缩分支近乎死代码 | `autoroute/task_vocabulary_regression_test.go:130-164`（名称 vs :160-163 断言）；`autoroute/scoring.go:386-401`（hits/len 量化）；`autoroute/recommend_v2.go:299-312`（分支） | 改名（如 `PartialVocabularyAtBoundaryDoesNotCollapse`）；如要钉住「仍坍缩」，需用 long_context 5 元列表单命中构造 <30 场景 |
| 5 | P3 | **loadE2EReport `pass=true` 分支未置 `recorded`**：若未来写端契约漂移（pass=true 但 decision.task_type≠expected），同一行会同时计入 `Pass` 和 `Failures`。当前写端契约 `pass := row.GotTask == tc.ExpectedTask` 使该态不可达，属防御性小账。对称地 pass=false 分支置了 recorded 防双记 | `cmd/auto-testbench/main.go:334-335`（pass 分支无 recorded=true）vs :336-338；:350-356（else if !recorded）；写端契约 `cmd/autoroute-e2e-audit/main.go:349-350` | pass=true 分支同样置 `recorded = true`，一行改动 |
| 6 | P3 | 样式两处：patterns.go 新增注释块顶格列 0（函数字面量内）；`classifier_zh_live_repro_test.go` 文件末尾缺换行符 | `autoroute/patterns.go:195-198`；`autoroute/classifier_zh_live_repro_test.go:42`（无 EOF newline） | 顺手清账 |
| 7 | P3 | **.md 报告未含选型层指标**：9b00f0f18 把 collapse 指标写进了 console 与 JSON，但 markdown 报告的 E2E 节只有 pass/failures——人类读者看 .md 仍看不到坍缩率 | `cmd/auto-testbench/main.go:533-545`（md 节）vs :394-414（console 节） | md 节补 collapse 一行 |
| 8 | P3 | repro 测试覆盖面注记：`帮我写一个 Go 锁的实现` 这例实际命中的是**关键词层**「实现」（kw.Code 既有词，conf 0.40），不是新 P3 前缀——P1/P3 对象表都没有「锁」。新 P3 前缀 `帮我写一个` 只被 `...解析 CSV 的脚本` 一例真正覆盖。测试仍有效钉住 TaskCode 结果，但对该 pattern 自身是弱钉桩 | `autoroute/classifier_zh_live_repro_test.go:57-73`；`autoroute/classifier.go:321`；`autoroute/patterns.go:200` | 可接受；若想强钉 P3，补一例 kw.Code 完全不沾边的对象 |

## 二、核实为健康的面

**核心任务 1 —— fail-open 语义深审结论：**
- 判定条件是 **pool 级 ANY**：`TaskVocabularyRepresented(task, available)` 作用在可用性/IQ 过滤后的内存候选池上，不区分「整库 tags 表空 vs 该池恰好无标签 vs DB 查询错误」。DB 查询错误有独立处理（`recommend_v2.go:75-87`：`recordLiveFilterFailure` + 快照降载，不会伪装成词表缺失）；空池走 `len(available)==0` 分支（:134-148）；**部分缺失时 fail-open 不触发**（任一候选带任一所需标签即 represented）。
- fail-open 降级行为：该维度置 0.5（=50 分，与 chat 既有中性值等值，`scoring.go:389` 由字面量改常量无行为变化），排序退化为价格/通道质量/可靠性/affinity 主导，候选多样性保留（topN=3），48h 坍缩不再触发。中性化只写 `available` 副本的标量字段（:89-114 复制构造），无 `idx.entries` 别名污染。
- 回归钉桩扎实，变异分析：删 neutralization → per-candidate MatchScore==0 断言红；guard+neutralization 同删 → `isFallbackWinner` 红；`TaskMatchScoreUnknown` 改 0 → 红；ANY 语义改 ALL → 前置 Fatal 红。guard 单删不红——但这是注释明示的 belt-and-suspenders。

**核心任务 2 —— 分类器模式修复：**
- 通道顺序未破坏：Channel 1.5 intent 硬覆盖（conf 0.82）在 pattern 层之前，`classify sentiment` 不再可能被 import/include pattern（0.55）劫持；`matchPatterns` 每任务保留首个命中，P1(0.65) 先于 P3(0.60)，权重是 floor 非 ceiling。
- 过宽评估：语言段插入的边际误伤面极小——对象表全是具体技术名词，纯创意/规划动词后接语言名不落对象表。
- repro 测试断言质量：ZhQuickSort 4 例含 reason 通道双断言；ClassifySentiment 3 例断言 TaskIntentClassification（经 `intentClassificationKeywords` 新增 3 短语 → Channel 1.5）。

**核心任务 3 —— testbench 选型层度量：**
- pass=null 修复后区分彻底：pass=null+decision 对 → `Errors++`+`ClassCorrect++`；pass=null+decision 错 → `Errors++`+`Failures`（真 miss 不被掩盖，有专测）；decision 缺失（401 等）→ Errors 且不入分类分母（有专测）。写读契约互补。
- e2e_selection_test.go 7 例与文档 §6.1 清单一致；变异分析支持其可达性。

**生产路径覆盖：**
- gateway `NewDecider` → `DecideWithFeatureFlags`：任一 V2 子特性 flag 开启 → `DecideV2` → `RecommendV2WithHints`（修复所在）；全关 → legacy `Decide` → v1 无 48h 坍缩分支——两分支均不再发生本缺陷。
- 合并零漂移：相关文件 diff 为空，merge 未损伤修复内容。
- 窗口 8 文件未触碰终态 outcome 回填、settle/affinity、decision 置信度门、迁移——无回退风险。

**核心任务 4 —— 并行审计线交叉验证**（`docs/audit/2026-09-28-auto-routing-selection-layer-audit.md` 逐条对照）：

| 并行审计条目 | 状态 | 核实结果 |
|---|---|---|
| §2.2/2.3 选型坍缩（5 类任务 100% fallback、单候选放大 429） | **已修复 `69007a3c2`** | 与代码现状一致 |
| §三 根因 requiredTagsForTask 词表不同构 | 根因描述成立 | 逐项比对 scoring.go:474-501，文档表格与代码完全吻合 |
| §4.1/4.2 修复方案 | **已修复 `69007a3c2`** | 代码片段与现状一致；补充：实现是池级 ANY 判定 |
| §4.3 五个回归测试 | 存在 | 但 `DiscriminatingLowMatchStillFallsBack` 名称与断言相反（本报告 #4） |
| §6.1 testbench 两缺陷 + double-Failures | **已修复 `9b00f0f18`** | 与代码现状一致；残余不对称见 #5 |
| §6.2#1 词表补齐是数据/运营活 | **仍开放**，描述与代码一致 | 全仓无 cap:code/cap:creative 补齐动作 |
| §6.2#2 门禁只覆盖分类层 | **仍开放**，描述与代码一致 | baseline.json 阈值零区分力；evaluateGate 不收 e2e |
| §6.2#3 P2 TierSelector 未接线（G1 堵点） | **仍开放**，描述与代码一致 | `NewTierSelector` 全仓仅定义处 |
| §一 进度表 | 成立 | 两 SHA 存在；AUTO_V3_SHADOW 零引用 |

对「文档写于窗口中段、可能已被后续提交部分修复」的担忧：不成立——其后至窗口终点对相关代码零改动（diff 为空已证）。

## 三、未覆盖项与原因

- **240 例 E2E 实测数字复核** —— 需真机网关 + `AUTO_AUDIT_API_KEY`，只读子代理环境无凭据。
- **DB 取证复核**（models_canonical.tags 词表构成、strengths 0/950）—— 需真库连接。
- **无空格变体归类实测**（`写一个python快速排序` 是否 chat 0.1）—— 只读纪律禁止新建测试文件；结论来自正则静态推演，建议主代理以临时探针或补充测试复核后登记。
- **三门验证未执行**（只读纪律下未运行构建/测试命令）；建议主代理跑 `go test ./autoroute/ ./cmd/auto-testbench/ -count=1` 收口。
