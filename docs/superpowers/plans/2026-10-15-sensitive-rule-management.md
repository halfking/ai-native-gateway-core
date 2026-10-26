# Sensitive Rule Management Implementation Plan

> **For agentic workers:** Use subagent-driven-development with one implementation worker at a time and spec/quality review between slices. The human approved this design and execution; proceed through delivery without another approval prompt.

**Goal:** 打通系统参数、规则发布、人工复核、集群热刷新及回滚，完成真实浏览器和跨实例验证后推送main。
**Architecture:** 独立规则域维护不可变配置与版本；现有sanitize/output合规链消费请求固定快照；管理API与Vue页面复用既有权限与导航。Redis通知加数据库轮询补偿。
**Tech Stack:** Go、PostgreSQL、Redis、Vue3、TypeScript、Vitest、browser-use，无新业务运行时依赖。

## S1 — 规则域与存储
- [x] 在domains/sanitizerules编写Config/Rule/Sample/Version/Event/Instance及Store接口，先测试验证/版本/CAS/rollback/坏版本不清空，再实现。
- [x] PostgresStore使用database/sql事务与SELECT FOR UPDATE锁定单一当前指针；事件按租户和结构指纹聚合，Review必须带tenant条件；心跳按instance_id更新。
- [x] Manager提供NewManager(store,fallback,options)、Current、SnapshotFor(ctx)、Pin(ctx)、Refresh、Publish、Rollback、Run(ctx)、Record等能力；规则快照不可变，通知与轮询同时生效，错误保留旧版。
- [x] 用本地临时PG真实验证迁移幂等、并发CAS与回滚；Redis用miniredis并保留真实双Manager共享库测试。

## S2 — 管理API与系统参数
- [x] 新增admin/sanitize_rules_handler.go及对应测试，注册设计中完整路由，superadmin写权限与租户过滤在服务端生效。
- [x] settings新增sanitize.rules.refresh_interval_seconds（1..300，默认5）、sanitize.rules.audit_enabled（默认true）、sanitize.rules.event_limit（100..100000，默认10000），沿用模块参数UI。
- [x] 规则发布和回滚只操作不可变版本，不直接改写历史规则；复核只改处理状态，不隐式放行。

## S3 — 运行时接线
- [x] 将PatternDetector的规则身份改为显式kind/scopes，保留旧配置兼容默认派生与R1034判据；自定义表达式修改不能扩大输入作用域到工具阻断。
- [x] 解析YAML生成默认Config，Manager在DB可用时读当前版本；InputMiddleware钉请求快照，工具/正文检测沿用相同快照。
- [x] 输出合规Checker接入相同规则来源，保持语法/JSON结构/占位符可信还原守卫；记录命中/阻断/豁免的无真值结构事件。
- [x] cmd/gateway启动共享Store/Manager、Redis通知、心跳，admin使用同一实例；数据库不可用时保留默认保护，后台明确报告不可发布。

## S4 — 管理页面
- [x] web新增类型化API与规则/事件/版本/实例组件，接入OutputComplianceView现有入口；多语言沿用既有locale结构。
- [x] 规则编辑含作用域、类型/种类、捕获组、动作、工具/字段、启停、样例校验；保存自动发布并显示目标版本。
- [x] 特殊组合列表显示四类动作、类型摘要、次数、版本、状态；人工复核可进关联规则草稿；回滚确认显示目标和新版本。
- [x] 先写API/组件行为测试，验证失败/加载/空态、CAS冲突保留草稿、防重提交、秘密零展示。

## S5 — 集成验证及收口
- [x] 本地临时PG/Redis验证两个独立规则/API进程；真实Handler/Executor覆盖公共协议，browser-use验证完整管理闭环，不以纯mock页面替代。
- [x] 精确运行go test ./domains/sanitizerules ./security/sanitize ./domains/outputcompliance ./domains/hooks/outputcompliance ./domains/streaming ./settings；相关admin与cmd/gateway子集；race；go vet/build ./...；installer迁移测试；pnpm typecheck/build/test。
- [x] 撤去CAS/原子切换/通知补偿/作用域/租户/事件遮蔽/回滚保护的变异应在对应断言变红。
- [x] 全量检查staged/unstaged/deleted/untracked并验证Task Stop Summary；实现按小于200改行的结构checkpoint收口，独立Go/TypeScript/规格/质量审查通过。

最终交付门：在小提交后绑定精确SHA审查，再merge --no-ff main、push origin main，核对远端包含交付SHA并清理worktree；实际结果以最终Task Stop Summary及远端核验记录为准。

验证命令使用Go源码实际工具链；统一门失败须与干净基线对照，禁止为通过而修改既有负向判据或添加排除项。生产部署不包含在任务中。
