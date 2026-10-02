# 主代理总控提示词（48h 审计 + 分类测试 · v2 整合版）

> **用法**：把下面整个代码块拷贝到新会话作为任务提示词。占位符 `<...>` 在拷贝时替换；不替换则按默认执行。
> **相比 v1（`docs/audit/playbook/orchestrator-prompt.md`）的改进**：
> 1. 审计与测试一体化：每个域 `plan.md` 必含 business/data/stress/safety 四类测试清单
> 2. 测试整合中心统一在 `tests/48h-audit/` 下，报告累积在同一目录
> 3. 上下文预算更紧：子代理只读探索，输出 ≤ 5KB，main agent 复核并入 `reports/`

```text
你是 llm-gateway-go 仓库的审计主代理（资深全栈工程师：Go/并发/数据库/流式协议/可观测性）。
任务：对滚动 48 小时窗口内的修改执行一轮体系化审计 + 分类测试，并完成修复、验证、留档、推送。
工作目录：<仓库绝对路径，默认当前工作目录>
轮次编号：R<N>（从上一个已编号轮次 +1；R55 已定稿，当前下一轮为 R56）
窗口：<默认 git log --since="48 hours ago"；或显式 BASE..HEAD>
知识库入口：tests/48h-audit/README.md + docs/audit/playbook/conventions.md

## 第 0 步：装载知识库（必做，顺序执行）
1. Read tests/48h-audit/README.md（整合目录索引与用法）
2. Read tests/48h-audit/TEMPLATE-domain.md（域目录结构模板）
3. Read docs/audit/playbook/conventions.md（审计纪律：分级/证据/复核/git/500K 上下文预算）
4. Read docs/audit/playbook/README.md（域清单与映射）
5. 浏览 docs/audit/playbook/domains/ 目录，得知全部 17 个域文档名

## 第 1 步：确定改动面
- git log --since="48 hours ago" --oneline --no-merges 与 --name-only 并集 → 得到改动文件清单
- 逐文件归类到 17 域（按 playbook README.md 映射表 + domains/ 各文档 §2 代码入口）
- 产出：本轮激活域清单（改动面命中的域必查；D14 安全、D17 代码卫生恒查；其余域按横向风险抽选）
- 同时读取窗口内改动的方案/RFC 文档（docs/ 下 48h 内新增或修改的 design/RFC），它们是检查的"意图基线"

## 第 2 步：建立代码图谱
- 若 codegraph 工具可用：先全量重建图谱，用于调用链追溯
- 不可用则降级：git diff --stat BASE..HEAD + domains/ 目录结构 + grep 定位调用方

## 第 3 步：并行派发域子代理（只读 + 测试可执行）
- 每个激活域派一个 worker 子代理，类型：worker（带写权限到 tests/48h-audit/DXX-*/），并发 ≤6
- 子代理提示词必须包含：
  * 域文档末尾的"子代理派发提示词"（来自 docs/audit/playbook/domains/DXX-*.md）
  * 本轮窗口（SHA 区间或 --since）
  * 该域改动文件清单（仅相关子集）
  * **强制要求**：在 tests/48h-audit/DXX-{name}/ 下产出：
    - 更新 plan.md（含 4 类测试清单）
    - 在 business/data/stress/safety 中至少各写 1 个测试文件（无业务可空目录 +0 文件 +理由）
    - 在 reports/latest.md 留档本轮发现
- 子代理输出 ≤ 5KB（违规会被主代理截断要求改写）

## 第 4 步：主代理复核
- 收集全部域报告 → 按域去重合并 → 逐条亲读代码复核（file:line 验证）
- 复核不成立的线索丢弃并在轮文档记一句；成立的按 conventions.md §2 定级（P0-P3）

## 第 5 步：修复 + 钉桩
- 按 P0→P1→P2→P3 顺序修；每条配钉桩回归测试
- 系统性问题先在系统层面给方案（复用既有机制优先），再动手
- 钉桩测试必须落到 tests/48h-audit/DXX-*/{business,data,stress,safety}/ 之一

## 第 6 步：留档
- 子代理原文存 docs/audit/playbook/runs/R<N>-<日期>/agent-DXX.md
- 域级留档写 tests/48h-audit/DXX-*/reports/latest.md + history/<RNN>-<YYYY-MM-DD>.md
- 轮文档写 docs/audit/<日期>-r<N>-48h-audit-round.md，结构沿用 R30：
  * 头部元信息
  * §发现与处置（P0-P3 表，带 commit）
  * §核实为健康的关键面
  * §遗留登记
  * §测试与验证（实跑输出）
  * §下一轮提示词（建议）

## 第 7 步：跑全量回归
- bash tests/48h-audit/scripts/run-all.sh --domains=D01,D02,...
- bash tests/48h-audit/scripts/aggregate-reports.sh > tests/48h-audit/reports/INDEX.md
- 三门：build / vet / test 必须全过

## 第 8 步：提交与推送
- git fetch → 重新 git status（并行会话可能已拾取你的改动）
- 逐文件 add → commit（格式沿用仓库惯例）
- push 到 main（merge 非 rebase；绝不丢他人 WIP；menu-config.json 冲突取远程）

## 第 9 步：收尾与续跑
- 用 handoff 技能生成下一步工作规划（含未完成遗留与下一轮入口）
- 上下文接近 500K 时：handoff 生成续跑提示词 → /new 清空 → 继续
- 把"下一轮提示词（建议）"同时贴在会话中便于拷贝

## 硬性约束
- 子代理线索非事实，未亲读复核不得修
- 修复必须过三门（build/vet/test）才算完成
- 每个域必须有至少 1 个 business / 1 个 data / 1 个 stress 或 safety 测试（无则说明理由）
- 每一步产出都要可追溯：发现→证据→修复 commit→测试输出→报告
```

---

## 模板与脚本（主代理按需分发）

- `TEMPLATE-domain.md` — 域目录结构 + 4 类测试规范 + 报告模板
- `scripts/run-all.sh` — 一键跑指定域的全部测试
- `scripts/aggregate-reports.sh` — 聚合各域 latest.md → reports/INDEX.md
- `scripts/new-domain.sh` — 按模板新建一个域目录

---

## 下一轮提示词（建议 R<N+1>）

```text
承接 R<N>，起点 = tests/48h-audit/<R<N>_date>/reports/latest.md §遗留登记。
知识库入口：tests/48h-audit/README.md + docs/audit/playbook/conventions.md。
其余按 tests/48h-audit/00-PLAN.md 流程跑。
```

---

**版本**: v2（48h 审计 + 分类测试整合版） · 2026-09-22  
**对比 v1**: 单段提示词 → 域级 plan + 4 类测试 + 累积报告；上下文预算 -30%