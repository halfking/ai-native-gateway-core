# omni-ref3：会话管理 / 压缩 / 拼接 / 缓存转发 / 数据转换 融合优化方案

> 范围：把本仓库（`llm-gateway-go`）的**会话相关管理功能**（总结、标题、tag、项目 / 任务归纳）、**压缩**、**多轮拼接**、**多级缓存与转发**、**数据结构转换（IR）**与参考实现 `/Users/xutaohuang/workspace/ai/omniroute`（package 3.8.49，TS/Next.js）逐项对照，吸收对方优点，形成**本仓库可落地**的优化方案。
>
> 这是 `omni-ref`（翻译方法论 + 文档审计）和 `omni-ref2`（融合基线 / 跨仓事件契约）之后的**第 3 轮**，聚焦**会话与上下文运行时**本身。

## 0. 一句话结论

> 本仓库在**会话存储 / 压缩 / IR / 多级 sticky**上已比 omniroute 更完整、更接近生产级（增量 delta、原子生命周期、三级 sticky、LLM 摘要、向量聚类），但**三套并存的会话存储 + 未接线的归纳能力 + 进程内多级缓存**是主要债务；omniroute 的优点集中在**声明式压缩引擎编排、引擎熔断 + 保真度门禁、cache-safe 注入、可预览的压缩 REST、声明式系统变换 DSL**。优化方案的核心不是“抄功能”，而是**接线、去重、补门禁**。

## 1. 文档结构

| 文件 | 主题 | 阅读对象 |
|---|---|---|
| `README.md`（本文件） | 总览、阅读顺序、结论、证据标签 | 所有人 |
| `00-EVIDENCE-BASELINE.md` | 双仓代码核对清单（file:line）、证据等级、不变量 | 评审 / 实现者 |
| `01-SESSION-METADATA.md` | 总结 / 标题 / tag / 项目 / 任务归纳的对比与优化 | 会话域 owner |
| `02-COMPRESSION.md` | 压缩管线、引擎编排、门禁、token 计数的对比与优化 | 压缩域 owner |
| `03-MULTITURN-ASSEMBLY.md` | 多轮消息拼接、submit-mode、历史重建的对比与优化 | 执行器 owner |
| `04-CACHE-AND-FORWARDING.md` | 多级缓存（session-state / semantic / sticky）、转发的对比与优化 | 缓存 / 路由 owner |
| `05-DATA-STRUCTURE-IR.md` | IR、消息净化、provider 变换 DSL 的对比与优化 | IR owner |
| `06-OPTIMIZATION-ROADMAP.md` | 分阶段优化路线图（P0–P3）、门禁、回滚、验收 | PM / Tech Lead |
| `07-AUDIT-REPORT.md` | 方案自审计：缺口、矛盾、风险、遗留问题 | 评审 |
| `08-A1-V2-READ-CANARY-RFC.md` | A1：V2 会话读路径放开（**已执行，2026-08-07**：压缩+摘要一起切、默认开、平台级 kill-switch、不灰度；文中原灰度方案保留作参考） | 会话域 owner / 运维 |

## 2. 阅读顺序

1. 本 README 的“结论”和“证据标签”。
2. `00-EVIDENCE-BASELINE.md`：确认每条结论的 file:line 证据，区分 `SOURCE-VERIFIED` / `TARGET-BOUNDARY` / `NEW-DESIGN` / `BLOCKED`。
3. 按主题读 `01`–`05`：每篇都遵循固定结构——**现状（双仓对比表）→ omniroute 优点 → 本仓库优点 → 缺口 / 债务 → 优化建议（带优先级）**。
4. `06-OPTIMIZATION-ROADMAP.md`：把 01–05 的建议汇总成有依赖关系的阶段计划。
5. `07-AUDIT-REPORT.md`：在评审前先读，了解方案的自审遗留。

## 3. 证据标签

| 标签 | 含义 |
|---|---|
| `SOURCE-VERIFIED` | 已从本仓库或 omniroute 当前 checkout 的具体源码 file:line 核对 |
| `TARGET-BOUNDARY` | 由本仓库现有架构决定的责任边界，不是 omniroute 功能承诺 |
| `NEW-DESIGN` | 本仓库当前不存在，需评审后新增 |
| `BLOCKED` | 依赖跨仓契约 / 数据源 / 安全审查，未满足前不得上线 |

## 4. 关键结论（按主题）

- **会话元数据（01）**：本仓库**已有**比 omniroute 更强的归纳基建——intent 已写入 `session_intent_evolution`（`intentconfig/store.go:60`）、`SessionClusterer` 已在 `main_pipeline.go:1280` 装配并由 admin/cron 触发、归纳层还含 `TopicSummarizer`/`MultiSessionCluster`、另有 LLM `Summarizer`。**缺口精准定位**为：这些产出**未汇聚写回 live `sessions.task_type/topic/intent`**（`SetSessionMetadata` 零生产调用，`SOURCE-VERIFIED`），也**未回填 `Session.Tags`**。omniroute 反而**没有**项目 / 任务归纳，只有规则 fact 抽取。→ 优化核心是**接线 + 节流**，不是新建。审计另发现 `domain/analysis` 与 `domains/analysis` 命名空间并存（D8），接线前需先确认活跃包。
- **压缩（02）**：本仓库压缩是**单一有损 / 无损管线**，胜在 LLM 摘要 + 完整性守卫；omniroute 是**多引擎可堆叠管线 + 引擎熔断 + 保真度门禁 + 风险门禁 + 结果 memo + 可预览 REST**，工程化程度更高。→ 吸收其**门禁 / 熔断 / 预览 / 引擎注册表**，不引入其庞大的 `~25 嵌套配置`。
- **多轮拼接（03）**：本仓库的**增量 delta（V2）+ LCS（V1）+ submit-mode 5 级探测**已优于 omniroute 的“客户端永远全量”假设；但 V2 读路径被 `shouldUseV2` 硬编码 `return false` 关闭（`session_compressor.go:691`，`SOURCE-VERIFIED`）。→ 优化核心是**按租户灰度放开 V2 读 + 统一消息指纹**。
- **缓存转发（04）**：本仓库**三级 sticky（session/client+model/client）**是真实多实例可用的（Redis + PG 落盘），远胜 omniroute 的进程内 Map；但**会话状态缓存 V1/V2 双轨 + 四类 cache 概念无统一指标**。→ 优化核心是**统一指标 + 灰度切 V2**。
- **IR / 数据转换（05）**：本仓库的 `internal/ir` **超集 hub-and-spoke + Raw 透传 + 净化顺序修复**已优于 omniroute 的 `Record<string,unknown>` 变换；omniroute 的**声明式系统变换 DSL（`TransformOp`）**值得吸收为“provider 适配配置化”。→ 优化核心是**把零散的 provider 适配收敛成声明式 DSL**。

## 5. 不变量（任何优化不得破坏）

1. 数据面唯一 owner 是 `llm-gateway-go`；新增包不得让 `executor` 依赖 `mcp`/`a2a`/`fusion`（沿用 `omni-ref2` 硬约束）。
2. 跨租户读写一律走现有认证上下文与 RLS；禁止从请求 arguments 覆盖 tenant。
3. 压缩 / 摘要 / 净化必须**fail-open 且 NeverWorse**：出错时回退到原始 body，绝不上送更差的内容（现有 `handler.go:2561` 守卫）。
4. 工具链完整性：任何裁剪后必须保证 `tool_use ↔ tool_result` 配对完整（`toolChainIntact` / `SanitizeToolMessages` 语义）。
5. 最大数据库迁移号以 checkout 实际文件为准；新增迁移必须先重检，不得沿用旧文档占位号。

## 6. 源参考与快照

- 参考实现：`/Users/xutaohuang/workspace/ai/omniroute`（package 3.8.49，TS/Next.js）。
- 关键参考路径：`open-sse/services/compression/`、`open-sse/services/{contextManager,contextHandoff,sessionManager}.ts`、`src/lib/memory/`、`src/lib/{cacheLayer,semanticCache,contextWindowResolver}.ts`、`open-sse/services/{roleNormalizer,responsesInputSanitizer,systemTransforms,ccBridgeTransforms}.ts`、`open-sse/translator/`。
- 本仓库规划基线：`main` @ `2d1d57bd`（feat(session): integrate V2 components into main gateway initialization）。
- 本方案所有 `SOURCE-VERIFIED` 均可追到具体 file:line，见 `00-EVIDENCE-BASELINE.md`。
