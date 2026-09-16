# D16 — 流程/数据/反馈闭环（含附件与上传）

> 领域编号: D16 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：三条闭环的端到端核对——**流程闭环**（每条链路有起点有终点，无悬挂态）、**数据闭环**（数据来源与去处明确：从哪来、落哪里、谁消费、何时清理）、**反馈闭环**（反馈产生→落账→消费→影响决策）；上传文件解析与存储及版本管理；托管任务（hosted task）的回调闭环。
**不管**：各域内部正确性（归 D01–D13）；统计口径（D10）。

## 2. 参考基线

设计文档：
- `docs/design/hosted-task-delegation-design.md` — 任务托管设计 v2（含回调与 reconciler）
- `docs/audit/2026-09-15-hosted-task-p0-verification.md` — P0 验证
- `docs/03-design/01-architecture/architecture/attachment-auth-integration.md` — 附件存储鉴权（files.kxpms.cn / Cloudreve）
- `docs/adr/2026-08-23-step7-raw-bytes-fallback.md` — 原始字节回退
- 历史方案：`docs/archive/process/changelogs/2026-07/*multimodal-attachment*`（追溯）

代码入口：
- `domains/attachments/`、`internal/attachmentmirror/`、`internal/fsstore/`、`internal/requestarchive/`
- `domains/hostedtask/`、`internal/hostedcallback/`、`internal/outbox/`（mirror_outbox 失败登记+重放）
- `internal/orchestration/`

## 3. 检查清单

1. **三问核对**：窗口内每个新增数据流回答三问——来源（谁写）、去处（哪张表/哪个目录/谁读）、清理（TTL/归档/无界？）；回答不了的 = 发现。
2. **附件闭环**：上传→解析（多模态抽取）→存储（对象存储/文件）→版本管理（同 key 多版本不覆盖丢史）→请求侧引用（IR 引用可解析）→清理；每环有失败路径与重试；mirror_outbox 失败登记+重放+kill switch 基准不破。
3. **流程闭环**：请求/任务生命周期状态机无不可达终态、无悬挂中间态（超时兜底）；取消传播到全部子操作。
4. **反馈闭环**：每类反馈（路由反馈、质量反馈、错误反馈、任务回调）有落账点与消费点；只落账无消费 = 登记债。
5. **hosted task 回调**：任务下发→远端执行→回调/reconciler 对账→状态收敛；网络故障后重推可恢复（已验证基准）；715 迁移的启动链（无镜像时 pending 写入撞 23514 的教训）不复发。
6. **版本管理**：附件/文件的版本链可追溯（版本号/内容寻址），覆盖写有显式语义。

## 4. 历史回归点（轮末回注区）
- [R35 09-17] goal 影子轮三纪律：① followUpSourceActor 只对 goal 族 action 打 goal-% 标（audit 前缀族→goal-audit，未知→不打头），未知 action 打泛化标记会污染 §8 预算对账；② advisory 帧不得抢占 Path 2 模型轮换（switchModel 生成与 finish_reason 无关，抢占=轮换通道永久饿死）；③ 查询侧消费 goal-% 行的入口（session_title 已修、work_types/top_models/settle 待 R35-R2 决策）

- [09-15] 715 迁移启动链缺失：无镜像时 pending 写入必撞 23514 — 修复 6f3d03073
- [09-15] 凭证状态持久化缺失与路由事件过早可见双重 bug — 修复 bba08b922
- [R30] mirror_outbox 失败登记+重放（SKIP LOCKED/lease/指数退避/dead）+ kill switch —— 健康面基准
- [运维] hostedtask 默认关，开启需 ENABLED + §6.0 跨仓库门禁

## 5. 子代理派发提示词

```text
你是 D16（流程/数据/反馈闭环与附件管理）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D16-flow-closure.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：窗口内新增数据流的三问核对与悬挂态排查。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```
