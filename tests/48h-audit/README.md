# 48 小时审计 · 测试整合中心

> **目的**：把"48 小时滚动审计"从一份单段提示词 + 17 个分散域文档，升级为一套**可并行、可复用、可累积**的测试中心。
> **用法**：每个域一个独立子目录 `DXX-name/`，内含审计计划 + 业务/数据/压力/安全四类测试 + 脚本 + 累积报告。
> **跨链接**：审计域知识库仍在 `docs/audit/playbook/domains/`；本目录是它们的**测试具象化层**，互相引用。
> **开始审计**：拷贝 [`00-PLAN.md`](00-PLAN.md) 全文作为新会话主代理提示词；或按域派子代理，参考对应域 `plan.md`。
> **入口报告**：[`MASTER_REPORT.md`](MASTER_REPORT.md) — R56 总览、当前状态、下一步

---

## 目录结构

```
tests/48h-audit/
├── README.md                          # 本文件：索引与用法
├── 00-PLAN.md                         # 主代理总控提示词（48h 审计 + 分类测试入口）
├── TEMPLATE-domain.md                 # 域目录结构模板（拷给子代理）
├── D01-ir-lifecycle/                  # 审计域 1：IR 生命周期
│   ├── plan.md                        # 审计要点 + 测试分类 + 验收门
│   ├── business/                      # 业务测试：API 行为、契约
│   ├── data/                          # 数据测试：IR 字段、序列化、缓存一致
│   ├── stress/                        # 压力测试：吞吐、并发、长会话
│   ├── safety/                        # 安全测试：锁、泄漏、异常路径
│   ├── scripts/                       # 驱动脚本：go test / runner.sh / scenario.go
│   └── reports/                       # 累积报告：latest.md + history/
├── D02-protocol-adaptation/           # 审计域 2：协议适配
│   └── ... (同结构)
├── D03-three-tier-cache/              # 审计域 3：三层缓存 provenance
├── D04-queue-concurrency/             # 审计域 4：多队列并发
├── D05-auto-compression/              # 审计域 5：自动压缩
├── D06-dual-storage-mode/             # 审计域 6：双模式存储
├── D07-hot-columnar/                  # 审计域 7：热 + 分区存储
├── D08-provider-errors/               # 审计域 8：供应商错误处理
├── D09-node-state-selfcheck/          # 审计域 9：节点状态自检
├── D10-stats-aggregation/             # 审计域 10：统计聚合
├── D11-auto-model/                    # 审计域 11：auto 模型
├── D12-egress-proxy/                  # 审计域 12：海外模型代理
├── D13-free-token-pool/               # 审计域 13：免费 token 池
├── D14-security/                      # 审计域 14：场景安全
├── D15-observability-ux/              # 审计域 15：可观测性 + UX
├── D16-flow-closure/                  # 审计域 16：流程闭环
└── D17-code-hygiene/                  # 审计域 17：代码卫生
```

每个域目录都长一样，便于子代理按统一模板填充。

---

## 分类测试（业务/数据/压力/安全）的定义

| 类别 | 目的 | 典型形态 |
|---|---|---|
| **business/** | 验证 API、流程、契约在正常路径下符合 RFC/方案 | 单元测试 + 集成测试，go test `-run TestBusiness*` |
| **data/** | 验证 IR 字段、序列化、缓存一致性、分区迁移 | 表驱动 + golden 对拍，go test `-run TestData*` |
| **stress/** | 验证大压力下吞吐、并发、长会话、P95/P99 不退化 | 突发/持续 soak/race，go test `-bench` + 自定义并发跑 |
| **safety/** | 验证锁/资源/异常路径无泄漏、不 panic、不丢消息 | `-race` + leak detector + chaos（强制断连、断电） |

一个域可以只有其中 1-2 类（看 RFC 与 48h 改动面决定），但目录结构统一。

---

## 报告累积协议

每个域 `reports/` 下放两份：

- `latest.md` — 本轮 48h 审计结论（按域文档 §6 留档结构）
- `history/<RNN>-<YYYY-MM-DD>.md` — 历史轮次留档，按 `git log` 时间倒序

聚合索引见 [`reports/INDEX.md`](reports/INDEX.md)（由 [`scripts/aggregate-reports.sh`](scripts/aggregate-reports.sh) 自动生成）。

---

## 与既有 docs/audit/playbook/ 的关系

| 资源 | 角色 |
|---|---|
| `docs/audit/playbook/conventions.md` | 审计纪律（分级/证据/复核/git/上下文预算） |
| `docs/audit/playbook/domains/DXX-*.md` | **审计域知识库**（意图基线、关键面、入口索引） |
| `docs/audit/playbook/orchestrator-prompt.md` | 主代理总控提示词 v1 |
| `docs/audit/playbook/runs/RNN-*/` | 各轮子代理原文（已存在，留档） |
| `tests/48h-audit/00-PLAN.md` | 主代理 v2（审计 + 测试整合入口） |
| `tests/48h-audit/COMPLETE_TEST_LAUNCH_PROMPT.md` | **完整测试启动提示词（可复用 · 4 变体：A 快速 / B stress / C 门禁 / D 单域）** |
| `tests/48h-audit/TEMPLATE-domain.md` | 域目录 + 测试 + 报告三套模板 |
| `tests/48h-audit/MASTER_REPORT.md` | R56 入口总览 |
| `tests/48h-audit/DXX-*/plan.md` | **本轮测试具象化**：从域文档抽取可执行项 |
| `tests/48h-audit/DXX-*/{business,data,stress,safety}/` | **测试代码** |
| `tests/48h-audit/DXX-*/reports/` | **累积报告** |

主代理提示词见 [`00-PLAN.md`](00-PLAN.md)，测试启动见 [`COMPLETE_TEST_LAUNCH_PROMPT.md`](COMPLETE_TEST_LAUNCH_PROMPT.md)。

---

## 复跑（持续验证）

每轮审计结束后做：

```bash
# 1. 全量重跑本轮激活域的测试
bash tests/48h-audit/scripts/run-all.sh --domains=D01,D02,D04

# 2. 报告回写
bash tests/48h-audit/scripts/aggregate-reports.sh > tests/48h-audit/reports/INDEX.md
git add tests/48h-audit/
git commit -m "docs(audit): R<N> 留档 + 回归测试通过"

# 3. 下一轮入口（handoff）
#    拷贝 tests/48h-audit/00-PLAN.md 末尾的"下一轮提示词"到新会话
```

---

**版本**: v1.0 (2026-09-22)  
**对应审计轮次**: R55+ 入口