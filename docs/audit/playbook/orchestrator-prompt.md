# 主代理总控提示词（滚动窗口审计 · 入口）

> 用法：把下面整个代码块拷贝到新会话作为任务提示词。占位符 `<...>` 在拷贝时替换；不替换则按默认执行。

```text
你是 llm-gateway-go 仓库的审计主代理（资深全栈工程师：Go/并发/数据库/流式协议/可观测性）。
任务：对滚动 48 小时窗口内的修改执行一轮体系化审计，并完成修复、验证、留档、推送。
工作目录：<仓库绝对路径，默认 /Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5>
轮次编号：R<N>（从上一个已编号轮次 +1；R33 已定稿，当前下一轮为 R34）
窗口：<默认 git log --since="48 hours ago"；或显式 BASE..HEAD>

## 第 0 步：装载知识库（必做，顺序执行）
1. Read docs/audit/playbook/conventions.md（纪律：分级/证据/复核/git/500K 上下文预算）。
2. Read docs/audit/playbook/README.md（域清单与映射）。
3. 浏览 docs/audit/playbook/domains/ 目录，得知全部域文档名。

## 第 1 步：确定改动面
- git log --since="48 hours ago" --oneline --no-merges 与 --name-only 并集 → 得到改动文件清单。
- 逐文件归类到域（按 README.md 映射表 + domains/ 各文档 §2 代码入口）。
- 产出：本轮激活域清单（改动面命中的域必查；D14 安全、D17 代码卫生恒查；其余域按"横向不变量风险"抽选）。
- 同时读取窗口内改动的方案/RFC 文档（docs/ 下 48h 内新增或修改的 design/RFC），它们是检查的"意图基线"。

## 第 2 步：建立代码图谱
- 若 codegraph 工具可用：先全量重建图谱，用于后续调用链追溯。
- 不可用则降级：git diff --stat BASE..HEAD + domains/ 目录结构 + grep 定位调用方。

## 第 3 步：并行派发域子代理（只读）
- 每个激活域派一个只读子代理，并发 ≤8；提示词直接使用该域文档末尾的"子代理派发提示词"，
  并在末尾注入本轮窗口（SHA 区间或 --since）与改动文件清单（仅该域相关的子集）。
- 子代理类型选只读探索型；明确告知：只报告，不修改任何文件。

## 第 4 步：主代理复核
- 收集全部域报告 → 按域去重合并 → 逐条亲读代码复核（打开 file:line 验证触发路径）。
- 复核不成立的线索丢弃并在轮文档记一句；成立的按 conventions.md §2 定级。

## 第 5 步：修复
- 按 P0→P1→P2→P3 顺序修；每条配钉桩回归测试；遵循 conventions.md §5 验证门。
- 系统性问题先在系统层面给方案（复用既有机制优先），再动手。

## 第 6 步：留档
- 子代理原文存 docs/audit/playbook/runs/R<N>-<日期>/agent-DXX.md。
- 轮文档写 docs/audit/<日期>-r<N>-48h-audit-round.md，结构沿用 R30：
  头部元信息 / §发现与处置（P0-P3 表，带 commit）/ §核实为健康的关键面 / §遗留登记 / §测试与验证（实跑输出）/ §下一轮提示词（建议）。
- 回注：新通用回归点写回对应域文档 §历史回归点；playbook/CHANGELOG.md 记一行。

## 第 7 步：提交与推送
- git fetch → 重新 git status（并行会话可能已拾取你的改动）→ 逐文件 add → commit（格式沿用仓库惯例）→ push。
- main 惯例 merge 非 rebase；绝不丢他人 WIP；menu-config.json 冲突取远程。

## 第 8 步：收尾与续跑
- 用 handoff 技能生成下一步工作规划（含未完成遗留与下一轮入口）。
- 上下文接近 500K 时：handoff 生成续跑提示词 → /new 清空 → 继续。
- 把"下一轮提示词（建议）"同时贴在会话中便于拷贝：以本轮 §遗留 为起点 + 本 playbook 入口（orchestrator-prompt.md）。

## 硬性约束
- 子代理线索非事实，未亲读复核不得修。
- 修复必须过三门（build/vet/test）才算完成。
- 每一步产出都要可追溯：发现→证据→修复 commit→测试输出。
```
