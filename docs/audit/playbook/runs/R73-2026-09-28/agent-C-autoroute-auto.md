# C 分类器auto域子代理报告（窗口：383c4b976..3909d56e0）

> R73 审计轮只读子代理原文。主代理处置：#1（M-1）已根修+回归钉、#2（M-4）已拆分
> P3a/P3b+负例钉、#3（M-8）已修 \\s*+注释订正+正例钉、#4 已补 E3 测试、#5/#6 登记
> （#6 D11 勘误已回注）、#7/#8 记录。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P2（M-1 属实） | vocabularyPresent 按全量 available 计算，但打分发生在 hot-top3 子池，词表只在子池外时 guard 仍 true、全 0 子池照样坍缩到 48h 单模型。**long_context 结构性坍缩**：required 5 标签（long_context/128k/200k/512k/1m），live 词表 cap:long-context 至多 1/5=20 分 <30 → guard 恒触发。与 recommend_v2.go:289-298 注释语义不符 | autoroute/recommend_v2.go:127,155-176,299；autoroute/decision_v2.go:249-256；autoroute/scoring.go:485 | guard 改按 candidatePool 判定 → **已根修（R73）**；long_context 5 标签表与 live 词表能力不匹配→登记 owner 校准项（需真库 tag 分布） |
| 2 | P2（M-4 属实且更宽） | P3 无界 `.*?` 使可选语言段完全冗余；「帮我写一个短视频脚本」creative 竞争为 **0**（kw.Creative 无 短视频/脚本/致辞）；「0.60 压 0.40」形态出现在文本同时含 creative 关键词时。无负例测试 | autoroute/patterns.go:200；autoroute/classifier.go:332-343,579-618 | 拆分 P3a 强对象（不需语言段，间距 {0,16}）/P3b 弱对象（须语言段）→ **已落地（R73）**；短视频脚本=词汇级歧义残留（RE2 无负向环视），交上层 LLM 兜底 |
| 3 | P3（M-8 属实） | 「写一个Python快速排序」漏：P1 语言段 `\s+` 强制空白 + P3 前缀表无裸「写一个」+ kw.Code 无 python。另：dfacf0f18 声称「写一段 Go 锁」「做一个 React 表单」一并承接——逐字符核对**不成立**（对象表无「锁」无裸「表单」），覆盖声明过宽且无测试钉 | autoroute/patterns.go:178,174-176,200；autoroute/classifier.go:308-331 | `\s*` 放宽 + 注释订正 + 正例钉 → **已落地（R73）** |
| 4 | P3 | E3（model_alternatives fail-open）零测试覆盖：既有 4 测候选池全带 code/creative 标签，新 fail-open 分支从未执行 | autoroute/model_alternatives.go:81-104；model_alternatives_test.go:10-99 | 补「全 untagged 池 + code 任务 → 不再 ErrNoCandidates」回归钉 → **已落地（R73）** |
| 5 | P3 | 选型层有度量、无门禁：evaluateGate 仍只校验 accuracy/macro_f1/grrq 三项，baseline 阈值饱和于 1/1/100；再发生 124/240 级坍缩 GATE 照常 PASS | cmd/auto-testbench/metrics.go:275-287；main.go:268-301；testdata/baseline.json:16-20 | gate 加 max_collapse_rate 类阈值（与 M-1 fail-open 边界同批 owner 拍板）→ **登记遗留** |
| 6 | P3（文档） | D11 域文档与代码终态脱节：plan.md 仍「✅ R70 已关闭」、latest.md「本轮无新增 P0-P3」，而同窗口 live E2E 实测 52% 坍缩 + 修复只落在审计文档 | tests/48h-audit/D11-auto-model/plan.md:5,17；reports/latest.md:8-16,31-44 | 补勘误回注 → **已落地（R73 erratum + INDEX 挂载）** |
| 7 | 观察 | E3 与 69007a3c2 两套独立机制、无语义冲突但口径不同池（alternatives 判在 recommended 池、recommend_v2 判在全量池）；M-1 修复后 recommend_v2 也改池级 → 口径已统一为「实际打分池」 | autoroute/model_alternatives.go:81-88,101-104 | M-1 修复时已统一 |
| 8 | 观察 | fail-open 中性化不留痕：中性 match_score=50 与真实 50% 命中在 wire 不可区分（无 vocabulary_present 字段） | domains/streaming/auto_route.go:96-156；autoroute/decision.go:23-103 | 可选 wire omitempty 布尔（登记） |

**auto 全链路 fail-open/closed 一致性附表**（原文保留）：启发式 fail-open（classifier.go:625-629）→ LLM 兜底 fail-open（decision.go:908-920）→ 可用性 live 过滤降级（recommend_v2.go:74-87）→ 可用后全空热度兜底（:134-148）→ IQ 硬门默认 off（:92-112）→ 词表维度中性化（:113-132）→ 打分零值全中性（scoring.go:235-358）→ hot-top3 收窄（:155-192）→ 排序后坍缩 guard（:299-312）→ 兜底缺失 ErrNoCandidates=链上唯一 fail-closed 硬终点（decision_v2.go:358-364）→ alternatives E3 fail-open（model_alternatives.go:60-111）。结论：整体自洽；不自洽点即 M-1（已修）。

## 二、核实为健康的面

1. 69007a3c2 主修复逻辑自洽（中性化→消费→guard 三点一致 + 正向边界测试）。
2. 9b00f0f18 选型层度量终态正确（*bool 区分 null、429 计入分母、error-masking 仍计 Failure）。
3. 收集器契约一致（Pass *bool 产出方/消费方匹配）。
4. classifier_zh_live_repro_test 真复现目标输入（逐字在内）。
5. E3 实现本身正确（边界与 69007a3c2 同型、无 nil 风险）。
6. intent 三短语有语境守卫低风险。
7. auto-testbench 工程卫生（R64 flag 校验、语义化 baseline 校验、unreadable→exit 2）。

## 三、未覆盖项与原因

- 未运行 go test / go run（只读纪律）；long_context 线上第二标签待亲验。
- live 词表分布依赖真库（采信 task_vocabulary_regression_test.go 的 09-28 快照）。
- classifier_llm.go / embedding_classifier / affinity / optimizer_bridge 未审（窗口无改动）。
- executor_dispatch.go:177 alternatives 消费侧（不在窗口 diff）。
- 240 例 live E2E 无法复跑（需 AUTO_AUDIT_API_KEY）。
- D11 域知识库正文（domains/D11-auto-model.md）未抽查（任务指定 plan/reports）。
