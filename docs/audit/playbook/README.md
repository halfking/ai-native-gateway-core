# 审计 Playbook —— 滚动窗口自检体系

> 目的：把"48 小时内修改的自检"从一段长提示词，变成一套**可并行执行、可逐步生长、上下文经济**的文档化审计体系。
> 本目录是审计域的**常驻知识库**；每轮审计的产出落在 `docs/audit/YYYY-MM-DD-rNN-*.md`（轮文档，沿用既有惯例）与 `runs/`（子代理原文）。

## 快速开始（三种用法）

1. **跑一轮完整审计**：拷贝 [orchestrator-prompt.md](orchestrator-prompt.md) 全文作为主代理提示词，粘贴到新会话（goal 模式或普通模式均可）。
2. **只审计某几个域**：拷贝 [domains/](domains/) 下对应域文档末尾的"子代理派发提示词"，单独派发只读子代理。
3. **项目推进中随手补检查项**：按 [extension-guide.md](extension-guide.md) 往域文档里增补条目/回归点，让检查清单与项目演进并行生长。

## 目录结构

```
docs/audit/playbook/
├── README.md                 # 本文件：索引与用法
├── conventions.md            # 通用纪律：分级/证据/复核/git/上下文预算（所有子代理必读）
├── orchestrator-prompt.md    # 主代理总控提示词（每轮审计的入口，直接拷贝）
├── extension-guide.md        # 如何新增域、如何回注新检查项（体系生长规则）
├── CHANGELOG.md              # playbook 自身演进记录
├── domains/                  # 17 个审计域，一域一文档，含各自的子代理派发提示词
│   ├── D01-ir-lifecycle.md
│   ├── D02-protocol-adaptation.md
│   ├── D03-three-tier-cache.md
│   ├── D04-queue-concurrency.md
│   ├── D05-auto-compression.md
│   ├── D06-dual-storage-mode.md
│   ├── D07-hot-columnar.md
│   ├── D08-provider-errors.md
│   ├── D09-node-state-selfcheck.md
│   ├── D10-stats-aggregation.md
│   ├── D11-auto-model.md
│   ├── D12-egress-proxy.md
│   ├── D13-free-token-pool.md
│   ├── D14-security.md
│   ├── D15-observability-ux.md
│   ├── D16-flow-closure.md
│   └── D17-code-hygiene.md
└── runs/                     # 每轮子代理报告原文（RNN-YYYY-MM-DD/ 子目录）
```

## 域 → 原始提示词条目映射

原始单段提示词的每一条 bullet 都已落到确定域，无遗漏：

| 原始条目（摘要） | 域 |
|---|---|
| 流程/数据/反馈闭环、上传文件解析存储版本管理 | D16 |
| IR 定义、创建/赋值/序列化/各类缓存 | D01 |
| IR 上下游协议适配、流式/非流式、双向解析、轮次摘要 | D02 |
| 三层缓存 provenance / AlignmentMap / V2 metadata | D03 |
| 多层队列、限流并发、权重负载均衡 | D04 |
| 超长会话自动压缩、context overflow 保连接重试 | D05 |
| 双重存储架构（pg+redis+… vs sqlite+…） | D06 |
| hot+columnar 分区、8h hot、批量转移 | D07 |
| 供应商错误处理、错误表、凭据服务质量、think 回传 | D08 |
| 全局节点状态统一、自检流程 | D09 |
| 数据统计模块 | D10 |
| auto 模型全量实现 | D11 |
| 审计代理、海外模型代理、避开香港、地域优先级 | D12 |
| 免费 token 扫描/注册/聚合 | D13 |
| 安全：并发锁/资源竞争/泄漏/溢出/网络可靠性/密钥 | D14 |
| 可观测性、UI 一致性、菜单组织 | D15 |
| 冗余代码标注清理 | D17 |

## 与既有审计惯例的衔接

- 轮文档命名、P0–P3 分级、"下一轮提示词（建议）"收尾等惯例**不变**，见 [conventions.md](conventions.md)。
- 历史轮次：R27–R30 见 `docs/audit/2026-09-1*-rNN-*.md`；R31/R32 以回注形式存在于 R30 文档内；R33 见 `docs/audit/2026-09-17-r33-24h-audit-round.md`。**下一轮从 R34 起编号**。
- 本 playbook 首版（2026-09-17）是 R34 轮起的常驻输入。

## 上下文经济（goal 模式必读）

- 子代理派发提示词刻意保持**极短**：只含"先 Read 域文档 + conventions，再执行"，实质检查内容全在文档里——这正是为了让提示词可拷贝、可增量演进、不占会话上下文。
- 主会话接近 500K 时：先 handoff 技能生成续跑提示词 → `/new` 清空上下文 → 用生成的新会话提示词继续。见 [conventions.md](conventions.md) §6。
