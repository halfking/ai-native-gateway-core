# LLM Gateway 24小时代码审计计划
**审计日期**: 2026-09-04
**审计范围**: 过去24小时内的所有代码变更

## 一、审计目标

基于以下核心关注点进行全面审计：

### 1. 流程闭环
- 请求处理流程的完整性
- 数据来源与去处的追溯
- 文件解析与存储及版本管理
- 流程闭环、数据闭环和反馈闭环

### 2. 数据闭环 (IR 核心数据结构)
- IR定义的完整性：多轮对话、路由数据、流程跟踪、调度瀑布
- 压缩脱敏数据、附件与媒体
- 元数据：日期时间、项目、用户、任务、轮次、tag、模型、供应商、凭据
- IR传输转换的完整性
- 上游多个LLM厂商标准格式适配
- 下游客户端智能体匹配
- 多通讯协议适配：chat/message/response
- 流式与非流式会话模式

### 3. 数据处理
- IR数据的创建、分步赋值、解析
- 存储层次：内存缓存、队列元数据缓存、文件缓存、附件存储、数据库存储
- 序列化与反序列化
- 存储优化

### 4. 会话管理
- 单一请求与多轮会话的双向解析
- 原始轮次请求与关键文本
- 会话摘要方式：信息抽取、人类可读（去除格式）

### 5. 队列处理
- 多层队列处理机制
- 前端并发与出口LLM限流
- 基于权重的负载均衡
- 总待处理队列、分维队列
- 处理操作解耦
- 路由逻辑与错误处理逻辑封装

### 6. 安全性
- 并发锁控制
- 多线程资源竞争
- 内存或句柄泄漏
- 异步处理异常
- 异常错误处理
- 请求数据转换
- 数据结构与参数溢出可能性
- 网络请求处理可靠性
- TCP会话维持可靠性
- 密钥可用性
- 数据API可用性

### 7. 可观测性
- 流程可观测性
- 数据展示统一性（复用同组控件）
- 菜单组织易用性
- 交互操作规范一致性

### 8. 大数据分区存储
- hot+分区（columnar）表结构
- 更新、删除只在hot表
- hot保留8小时数据
- 批量转移到分区表

### 9. 供应商错误处理
- 连接供应商端的请求错误处理逻辑
- 错误信息记录
- 备用情况下的think模式返回
- 不中断请求流程
- 错误记录到log错误表
- 错误呈现在凭据详情下
- 评估供应商服务质量

## 二、审计域划分

### 审计域 1: Dispatch调度系统与Backend生命周期
**负责代理**: audit-agent-1
**关键文件**:
- `domains/dispatch/` (dispatcher.go, dimension_index.go, governor_backend.go, pipeline.go, waterfall.go)
- `cmd/gateway/main_dispatch*.go`
- `domains/streaming/executors/executor_dispatch.go`

**审计点**:
- Backend生命周期管理（创建、复用、关闭、泄漏）
- DimensionIndex正确性
- 队列处理流程与并发控制
- Governor机制与限流
- Waterfall调度瀑布完整性
- 错误处理与降级路径

### 审计域 2: IR数据结构与协议适配
**负责代理**: audit-agent-2
**关键文件**:
- `internal/ir/` (parse_responses.go)
- `internal/modelresponse/models.go`
- `domains/streaming/messages.go`, `responses.go`, `request_meta.go`
- `domains/streaming/executors/executor_*.go`

**审计点**:
- IR结构定义完整性
- 上游厂商协议适配（Anthropic, OpenAI, Minimax等）
- 下游客户端协议适配
- 流式与非流式会话处理
- 数据转换过程的完整性
- 序列化/反序列化安全性

### 审计域 3: 会话存储、Turn Digest与数据闭环
**负责代理**: audit-agent-3
**关键文件**:
- `domains/session/v2/` (raw_cache_v2.go, session_digest_*.go)
- `admin/session_turns*.go`, `turn_digest_integration_test.go`
- `domains/hooks/audit/` (audit.go, batch_writer.go)
- `domains/streaming/sub_agents.go`

**审计点**:
- 会话数据存储层次（内存、队列、文件、数据库）
- Turn Digest生成与人类可读性
- 原始请求与响应保留
- 多轮会话解析
- 数据闭环完整性
- 缓存隔离与深拷贝

### 审计域 4: 依赖崩溃可用性与错误处理
**负责代理**: audit-agent-4
**关键文件**:
- `cmd/gateway/main*.go`, `recovery_gate_adapter.go`
- `domains/ursm/v2/manager*.go`
- `domains/authentication/` (verifier.go, keystore_*.go)
- `provider/client*.go`
- `bg/systemmonitor/monitor.go`

**审计点**:
- Redis/DB崩溃时的降级路径
- URSM v2 outage fallback机制
- Keystore全量同步与快照恢复
- 候选路由缓存stale策略
- 凭证reveal降级
- 启动重试机制
- 错误传播与恢复

### 审计域 5: 凭据管理、安全性与权限控制
**负责代理**: audit-agent-5
**关键文件**:
- `admin/credential_*.go`, `provider_*.go`
- `secret/aes_gcm.go`
- `security/sanitize/` (sanitize_info_bridge.go, smart_sani_guard.go)
- `plugin-runtime/capability*.go`
- `docs/changelogs/2026-09-04-credential-decrypt-smoke-gate.md`

**审计点**:
- 凭据加密/解密安全性
- 凭据生命周期管理
- 权限控制与RBAC
- 凭据轮换机制
- 敏感数据脱敏
- 部署时解密smoke测试
- 插件能力鉴权

### 审计域 6: 大数据分区存储与MV一致性
**负责代理**: audit-agent-6
**关键文件**:
- `bg/mv_consistency*.go`, `stats_minute_rollup.go`
- `admin/analytics*.go`, `funnel_cache.go`
- `sql/migrations/startup/6*.sql`
- `installer/cmd/llm-gw-installer/embeddata/startup/6*.sql`
- `scripts/check-routing-mv-drift.sh`

**审计点**:
- hot表 + columnar分区表结构
- 8小时hot数据保留策略
- 批量转移机制
- MV一致性检查
- 分区表更新/删除限制
- 漂移检测与修复
- Analytics路由来源标记

### 审计域 7: 插件运行时v2与前端可观测性
**负责代理**: audit-agent-7
**关键文件**:
- `plugin-runtime/` (manifest.go, supervisor.go, lifecycle_*.go)
- `web/src/` (前端组件、路由、i18n)
- `cmd/gateway/plugin_*_init.go`
- `docs/adr/2026-09-04-plugin-runtime-v2-capability-lifecycle.md`

**审计点**:
- 插件manifest v2 schema
- 插件生命周期管理
- 动态导航注入
- 前端控件复用
- i18n CJK一致性
- 可观测性展示统一性
- 交互操作一致性

## 三、并行审计策略

1. **启动7个并行子代理**，每个代理负责一个审计域
2. **每个代理输出**：
   - 发现的问题清单（按优先级P0/P1/P2/P3分类）
   - 数据流图（涉及的数据结构与转换）
   - 安全风险评估
   - 代码质量评分

3. **主代理汇总**：
   - 合并所有子代理的发现
   - 识别跨域问题
   - 生成系统级解决方案
   - 优先级排序

4. **实施修正**：
   - 按优先级实施修正
   - 运行测试验证
   - 提交代码并推送

## 四、审计交付物

1. **审计报告**: `audit-report-2026-09-04.md`
2. **问题清单**: `issues-found.md`
3. **修正方案**: `fix-plan.md`
4. **测试验证**: `test-results.md`
5. **代码提交**: Git commit + PR

## 五、关键变更文档参考

- `docs/changelogs/2026-09-04-dep-outage-availability.md`
- `docs/changelogs/2026-09-04-credential-decrypt-smoke-gate.md`
- `docs/04-implementation/changes/2026-09-04-v6-dispatch-composition-and-index-correctness.md`
- 各种audit文档：`docs/audit/2026-09-0*.md`

## 六、审计执行时间线

1. **并行审计阶段**: 启动7个子代理同时执行 (预计20-30分钟)
2. **汇总分析阶段**: 主代理分析汇总 (预计10分钟)
3. **修正实施阶段**: 根据优先级修正 (预计30-60分钟)
4. **测试验证阶段**: 运行测试套件 (预计15分钟)
5. **提交推送阶段**: 提交代码并推送 (预计5分钟)

**总预计时间**: 1.5-2小时
