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

- [R36] survival 层 KindContextLength 一律 FailTerminal → 5h 预算对超长完全不工作；压缩重试改判必须与 body 重写同侧（SurvivalCoordinator.Run），不可放共享决策层——durable worker 复用聚合但无 body 钩子，会空转烧 retry ceiling（钉桩 TestDurableRecoveryWorkerTerminalDecisionFails 实证）
- [R37] duration 类 env 必须带单位（time.ParseDuration 裸数字静默回退——Layer-2 兜底 90 实际无效，钉桩 deploy-local-lib-bootretry_test.sh）；sed -nE 无匹配退出码 0，`! var=$(sed…)` 解析护栏是死代码，必须校验变量非空；docker exec 探测=容器内网络视角≠网关实际路径，日志须如实描述； DL_DB_MODE=external 时 docker exec 选容器会误选 stopped 容器

## 5. 子代理派发提示词

```text
你是 D16（流程/数据/反馈闭环与附件管理）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D16-flow-closure.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：窗口内新增数据流的三问核对与悬挂态排查。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```

### R39 回注（2026-09-17，部署/脚本闭环批）
- **修复落点必须核对 source 链**：deploy-local 运行时 source 仓外 SSOT（$AIAN_DEPLOY_LIB），本仓 deploy-lib.legacy 是留档拷贝——修了拷贝≠修了运行时（eb5a922c7 即此，R39 回灌 SSOT b569613）。仓外 SSOT 修复也要在本仓轮文档留痕。
- **改被测函数必重跑其契约测试**：ef408a026 把 dl_pg_container_name 提为无条件调用，test-dl-pg-preflight-required.sh 未同步 stub → FAIL×3 入库三天无人察觉（R39 修复，5/5 绿）。
- **shell 测试禁跨子 shell 断言 export**：`out=$(fn)` 内的 export 随子 shell 消亡；要断言 export 语义须父 shell 直调（test-smart-discovery-pg.sh "HAS_DB=1" 用例曾确定性必红，R39 修复 4/4 绿）。
- preflight docker-exec 分支守卫：DL_DB_MODE=external 排除 + docker ps status=running（docker ps -a 含 stopped，90s 空烧/误 die）；无密码 DSN user 段正则 `([^@:/]+)(:[^@]*)?@`。
- 启用计划注释必须指向真实可验证宿主（DL_PG_PREFLIGHT_REQUIRED 曾写"245 验证后启用"，但 245 走 deploy-seamless 根本不 source 此库）。

### R42 回注（2026-09-18，installer 全新装断链收口 + 事务嵌套陷阱）
- **五点同步纪律扩展到"前置创建者"**：StartupFiles 含 657/722（ALTER/FK 引用 durable_llm_tasks）而 516/520（唯一创建者）缺登记 → 全新装必崩 657。`≥704 注册测试`只保护新迁移；本轮加 `TestDurableFamilyPrerequisitesRegistered` 钉"基表必须先于依赖者注册且顺序正确"。任何新迁移触碰某张表的 ALTER/FK 前，先确认该表在 installer 链有创建者。
- **迁移文件自带 BEGIN/COMMIT 的，绝不能包进外层事务验证**：516/520 的显式 BEGIN/COMMIT 会把外层 psql 事务切成 autocommit 段，外层 ROLLBACK 失效（R42 §三 事故：改名被永久提交，靠改名回滚修复）。验证手法二选一：裸跑幂等文件（确认无 BEGIN 头）或 scratch database。
- **723 教训（to_regclass 守卫）**：迁移引用"不在 canonical 链创建面上的表"（deploy 链 rename 产物、fixes 瞬态表）必须逐表 DO+to_regclass 守卫，720/723 同款事故两犯。
