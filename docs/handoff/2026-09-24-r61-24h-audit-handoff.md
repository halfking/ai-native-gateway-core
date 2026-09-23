# R61 24h 审计轮 Handoff（2026-09-24）

> 承接 R60（8ce9da13c → 本轮收口提交）。轮文档：
> docs/audit/2026-09-24-r61-24h-audit-round.md（发现清单与实证全记录）。

## 1. 任务概要（Mission Summary）

R61 定位 = R60 自身改动（a3e2232e3）的批判式复审 + 用户标准清单在 HEAD 的
横切复验 + 24h 文档一致性清扫。6 组并行子代理分域审计；当轮修复代码 8 项
（P2×4/P3×4）+ 文档勘误 7 处；DDL 守卫真库契约与迁移 743 矩阵以一次性
scratch PG17 容器独立复验（含敏感性回退红验证）。

## 2. 任务进度（Progress）

- ✅ main ff-only 合并远端 2 提交（ef87317c5→8ce9da13c，零冲突）。
- ✅ F1 users ENABLE RLS 移入守卫 miss 分支（31 点位最后一个每 boot
  ACCESS EXCLUSIVE 残口闭合）。
- ✅ F2 契约测试 2b：第二遍 ensure 零 POLICY/TRIGGER DDL 硬断言
  （pgx TraceQueryStart）；敏感性验证：破坏 triggersCurrent → 红
  （列出重放语句）→ 恢复绿。
- ✅ F3 bg 探针族+free-pool 读面归一（credential_probe_v2×2、
  active_probe_executor×2、balance_floor_guard、admin/routing 三路分发）
  ——availability_state 裁决面全部接 NormalizeProviderProtocol。
- ✅ F4 deliverOne GetEvent 瞬时错误可重试（ErrNotFound 才降级）；
  deliverOne 参数提取窄接口 + 双断言单测。
- ✅ F5-F8 credential_keys 函数体移出 else / triggerCurrent nil 检查 /
  bpchar 移出 noise 集 / 守卫 miss Warn（表名+原因分类）。
- ✅ requestID stamp 扩展 openai 三路径（stampIRRequestID 助手+钉桩测试）；
  anomaly dedup 硬上界 65536（风暴测试）；proxy env 30s 下界（钳制用例）。
- ✅ 文档清扫：T1/T2 报告不实 ✅ 改 ❌+勘误、252 审计 9→11 提交+三修补登、
  全面测试 README 路径纠偏、revision-sequence 括注 725…743。
- ✅ S3-F3 理由补档（轮文档 §五）+ S8-F6 重新登记（§六.4）。

## 3. 当前状态（Current State）

- 分支 main；构建/vet/全测绿（db/ir/hostedtask/proxy/admin 67s/bg 25s/
  streaming/executors）；零新增迁移（**下一可用 744**：740/742/743 已占，
  741=B11 预留）。
- scratch 容器 pg17-scratch-r61 已用后即焚；子代理本轮全程禁连库
  （R60 B 组事故教训制度化：库级验证收敛协调者 scratch 单点）。
- 生产位面未动：154/245 仍为 R58+17 类修复版；R59+R60+R61 待例行部署。

## 4. 下一步（Next Steps）

1. **例行部署**（运行 deploy-local/deploy-seamless 全流程，核对
   active-version 字面量）：154 与 245 携带 R59+R60+R61 三轮修复 + 迁移
   743（存量 protocol 归一，纯 DML 幂等）。部署后运行
   docs/06-deployment/04-runbooks/154-stream-fix-recheck-v2-automation.md
   harness 复验双终态面。
2. **验证 automation 产物**：2026-09-24 20:52 automation-1c111234 定时复核
   产物核对（cron 52 20 24,25 9 *）——committed 自然断流首帧样本 +
   R58 handoff §4 首项哨兵确认。
3. **读面归一收尾**（可选，文件在 docs/audit/2026-09-24-r61-24h-audit-round.md
   §六.1）：admin 诊断面 ~7 处裸比较（provider_diagnose.go 恒 chat 探针为
   主项，依赖 doMessagesProbe 已就绪）。
4. **R62 审计轮入口**：§六 登记项按序——parse 面 requestID 签名下传（P3）、
   hostedtask store 并发形状（多 worker 化前必修）、S8-F6 UA SSOT 专项、
   清理候选 C1-C5 owner 确认。

## 5. 下一轮提示词（R62，可直接拷贝）

```text
承接 R61，轮文档 docs/audit/2026-09-24-r61-24h-audit-round.md §六 + handoff 按序执行：
首项 = 例行部署：154/245 携带 R59+R60+R61 三轮修复与迁移 743 走 deploy 全流程，
部署后核对 active-version 字面量，并运行 154-stream-fix-recheck-v2 runbook harness 复验；
次项 = 9/24 20:52 automation-1c111234 产物核对（committed 自然断流 wire 样本 + §11.6 哨兵）；
再次 = R61 §六 登记项按序收口：parse 面 requestID plumb（parse_openai/anthropic/gemini/
responses 4 处 + extensions_restore，handler 侧签名下传）→ hostedtask store 并发形状
（RecordProgress 等追加事件行锁 + ClaimDueCallbacks Commit 语义，多 worker 化前必修）→
admin 诊断面读面归一（provider_diagnose 协议分支，复用 doMessagesProbe）；
迁移纪律：新增迁移前 fetch 核对远端编号，下一可用 744（740/742/743 已占，741=B11 预留）。
硬约束：子代理线索非事实；『已根修』断言必须实跑测试；回归测试必须敏感性验证（回退必红）；
子代理禁连库，库级验证用一次性 scratch 库（R60 B 组事故教义）；deploy 脚本改动必须有
契约测试背书；知识库入口 tests/48h-audit/README.md + docs/audit/playbook/conventions.md。
```
