# 会话优化 v4 — 权威整合设计与实施计划

> 本目录是 6 套历史文档（会话优化v1~v3 / 路由优化v3 / 修订0811 / 自检优化）的**版本裁决 + 权威整合**产物。
> **历史 v4 基线快照（2026-08-17）**：当时代码基线为 HEAD `ea4f49452`，工作树另含未提交的客户端/供应商稳定性修复；它不是当前代码或发布事实。当前 T0/key-schema 裁决以 [13-T0契约冻结与所有权](./13-T0契约冻结与所有权.md)、[14-URSM Redis delimiter-safe key兼容迁移冻结决策](./14-URSM Redis delimiter-safe key兼容迁移冻结决策.md) 和相应 session audit 为准。
> 落地工作区：`docs/会话优化v4/`。

---

## 1. 目录导航

| 文件 | 内容 |
|---|---|
| `00-README.md` | 本文件：导航 + 版本裁决结论 + 权威基线三件套 |
| `01-现状基线与版本裁决.md` | 6 套来源的提取摘要 + 冲突速查表（以谁为准） |
| `02-会话管理与压缩设计.md` | 主题①会话管理 + ②会话压缩 |
| `03-缓存映射与请求队列设计.md` | 主题③轮次多级缓存映射 + ④请求队列 |
| `04-节点状态与路由处理设计.md` | 主题⑤供应商节点状态 + ⑥路由处理（URSM 裁决） |
| `05-会话分析与模型选择设计.md` | 主题⑦会话分析 + ⑧任务模型选择 |
| `10-实施计划.md` | 对齐代码基线的可执行实施计划（P0/P1/P2 分级 + 里程碑） |
| `11-完成情况核实与并发执行方案.md` | **历史核验快照**：记录 2026-08-16 当时的文档/代码差异，不再作为 2026-08-17 当前实现状态源 |
| `12-GoalHandoff契约.md` | Goal↔Handoff 组件边界、握手与恢复契约 |
| `13-T0契约冻结与所有权.md` | T0 身份、重启、URSM authoritative 契约与 workstream 所有权 |
| `14-URSM Redis delimiter-safe key兼容迁移冻结决策.md` | delimiter-safe Redis key 版本化、双读/双写、TTL、清理、回滚与 G1/G4 门禁 |
| `15-URSM delimiter-safe key迁移实施计划.md` | 协调方交给 Migration owner 的实施计划：可复用 ledger/checkpoint 模式、preflight 判定、拓扑裁决清单、状态机与 G1/G4 边界（不解除 NO-GO） |
| `16-URSM delimiter-safe key迁移测试矩阵与TDD顺序.md` | k2 迁移的 L1/L2/L3/G1/G4 测试矩阵、首个 RED→GREEN 测试与 slice 顺序、依赖层级边界、SKIP 报告规则（含 2026-08-18 审计更正 A1-A4；不解除 NO-GO） |
---

## 2. 版本裁决结论（一句话版）

历史文档横跨 08-06 ~ 08-15 共 5 个版本层次，**非严格线性**，互相覆盖/纠错。v4 裁决三原则：

1. **以代码为真相**：历史实现状态论断以其声明的历史 HEAD 与工作树快照为准；当前 T0/key-schema 状态以 13/14 号冻结文档及当前 Git 状态为准，未提交修复不得冒充已发布版本。
2. **以最新文档集为权威**：修订0811（v1.6，08-15）> 会话优化v3（08-14~15）> 会话优化v2（08-13）> 路由优化v3（08-12）> 自检优化（08-13）。
3. **纸面 vs 接线**：URSM v2 现为默认 `authoritative`；`off` 是紧急回退，`shadow`/`canary` 是兼容与隔离观测模式，不是上线前置流程。详见 04 号文档及[切换 runbook](../../../06-deployment/04-runbooks/runbooks/ursm-v2-cutover.md)。

> **2026-08-17 当前裁决**：
> - schema 仍以 migration 513 证明的 `public.*` 为准（分区表除外）。
> - 缓存指会话三层数据：原始多轮 → 压缩后多轮 → 安全脱敏后发送，不是 Redis/内存缓存层。
> - 会话管理采用 `ursm/v2`；缺省模式为 `authoritative`。`off`/`shadow`/`canary` 仅承担兼容、观测和回退。
> - migration 525/526 已落地六维字段与 `session_turns_hot`；生产回填和运行验证仍属 deployment pending。
> - 客户端物理 TCP 断开不可恢复；首语义帧提交后不可透明切换供应商并从头重放。详见[稳定性审计](../../../archive/process/audit-collection/2026-08-17-ursm-v2-client-provider-stability-audit.md)。

---

## 3. 权威基线三件套（v4 引用的唯一事实源）

| 角色 | 文件 | 覆盖 |
|---|---|---|
| 实现状态 + ADR | 会话优化v2/`31-当前实现基线与修正决策.md` | Session V2 存储/Goal/Token/队列 ADR（⚠️ schema 归属按老板修正：public.*，非 gateway.*） |
| 业务流程 + 数据结构 | 会话优化v2/`59-会话完整业务流程与数据结构基准.md` | BP1-BP5 主流程 + X-1~X-3 横切 + 65 项 checklist |
| 核实结论 + 勘误 | 会话优化v2/`60-业务核实报告-20260813.md` | PASS 49 / 5 note / 1 fail / 8 N/A / 2 defer |

三件套为 **31 > 59 > 60** 优先级；43/44/45 属「历史诊断+方案」其结论已被 59/60 推翻。**schema 归属一律以代码/migration 513 为准：`public.*`。**

> **状态证据分层（2026-08-17）**：M3/M4 必须分别标注 `implemented`（代码已实现）、`local verified`（本地/代码级验证通过）、`deployment pending`（真实 Redis/PG/provider 与部署 smoke 未完成）。11 号是 2026-08-16 的历史核验快照，不再单独承担当前裁决。

---

## 4. 代码基线锚定（合并视角）

| 锚点 | 值 | 用途 |
|---|---|---|
| 代码事实基线 | HEAD `ea4f49452` + 工作树中未提交的稳定性修复（2026-08-17） | 区分 committed baseline 与未提交 implemented/local verified 证据 |
| 文档历史基线 | 修订0811 main@`96bc1425f` | M0/M1/M2 完成的历史记录 |
| 当前运行裁决 | URSM v2 默认 `authoritative` | `off` 回退；`shadow`/`canary` 兼容与观测 |
| 下一实施节点 | **deployment pending 门禁 + OBS/P0/P1 收尾** | 真实 Redis/PG/provider 故障注入、部署 smoke 与既有产品阻塞项 |

> **2026-08-17 发布边界**：当前稳定性修复达到 implemented/local verified，但工作树未提交，且真实 Redis/PG/provider 故障注入与部署 smoke 仍 pending。发布操作按[URSM v2 cutover runbook](../../../06-deployment/04-runbooks/runbooks/ursm-v2-cutover.md)执行。

---

## 5. 六套来源目录状态速览

| 目录 | 定位 | 是否采纳 |
|---|---|---|
| 会话优化v1 | 目录**不存在**（v1 内容散于 v2 的 00-README/00-完整方案文档） | 并入 v2 处理 |
| 会话优化v2 | 存储 + 业务基线（权威三件套所在） | ✅ 核心采纳 |
| 会话优化v3 | 会话语义管线（TARGET）+ V3.3-OBS 可观测（CURRENT 部分） | ✅ 采纳 OBS 部分 |
| 路由优化v3 | URSM 统一路由状态管理（设计蓝本 v3.0） | ⚠️ 部分采纳（裁决后） |
| 修订0811 | 六轨实施（SR/SC/MM/ROUTE/COST+BND）+ M0-M4 里程碑 | ✅ 采纳为实施主线 |
| 自检优化 | 统一自检队列 + ProbeService + 常用模型分级 | ✅ 采纳（已实现） |

---

## 6. 交付物范围（A+B）

- **A 权威设计方案**：02~05 号文档，覆盖 8 主题，按 `implemented / local verified / deployment pending / TARGET` 分层。
- **B 实施计划**：10 号文档，对齐代码基线，P0/P1/P2 分级；11 号仅作为历史核验快照。

---

**CHANGELOG**
- 2026-08-17 v1.4：基线更新为 HEAD `ea4f49452`，注明工作树含未提交稳定性修复；URSM v2 缺省 authoritative，off/shadow/canary 归为兼容/观测/回退；补 11/12 导航、525/526 与 TCP/首语义重放边界；M3/M4 改用 implemented/local verified/deployment pending 分层并链接审计/runbook
- 2026-08-16 v1.3：按 11 号核实校正基线锚点；确认 M3 durable 已落地、M4 已完成验证
- 2026-08-15 v1.2：老板确认 public.* + session_turns 走 hot+分区表模式（对齐 request_logs_hot）
- 2026-08-15 v1.1：老板修正落地——schema 全在 public.*（分区表除外）、缓存=会话三层数据、会话管理用 ursm/v2、会话维度分区表+双写逐步移除
- 2026-08-15 v1.0 初版（版本裁决 + 6 套整合 + 实施计划）