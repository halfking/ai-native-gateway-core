# 2026-08-31 24小时修正最终审计执行计划

**审计时间**: 2026-08-31
**审计范围**: 过去24小时内的所有代码修正（自 2026-08-30 00:00 至 2026-08-31 07:00）
**审计模式**: 主代理 + 5个并行子代理

---

## 一、审计目标

基于过去24小时的修正工作，进行**闭环验证审计**，确保：

### 1.1 数据闭环（Data Closure）
- IR数据结构的完整性：创建→解析→转换→存储→序列化/反序列化
- 数据来源与去向的可追溯性
- 附件与媒体的存储与引用闭环
- 多轮对话上下文的版本管理与压缩脱敏

### 1.2 流程闭环（Process Closure）
- 请求处理流程的完整性：接收→路由→调度→响应→记录
- 多层队列的解耦与协调
- 错误处理与重试逻辑的闭环
- 供应商请求失败的备用方案与降级

### 1.3 反馈闭环（Feedback Closure）
- 可观测性数据的完整采集与展示
- 供应商服务质量的度量与反馈
- 用户体验的一致性与交互规范
- 数据完整性验证与审计追踪

---

## 二、审计架构

### 2.1 主代理职责
- 协调5个子代理的并行执行
- 汇总各子代理的审计结果
- 进行系统层面的根因分析
- 生成统一修复方案
- 执行代码修正、测试验证、提交推送

### 2.2 子代理分工

#### Agent 1: IR核心数据结构与传输闭环审计
**关注点**:
- IR数据结构定义的完整性（`internal/ir/`）
- IR在各协议间的转换（adapter层）
- IR序列化/反序列化的数据保真度
- Extensions/Modalities/Metadata的持久化
- 路由决策瀑布数据的链路追踪

**审计维度**:
1. IR结构定义审计（`internal/ir/*.go`）
2. 协议适配器审计（`adapter/unified/`, `adapter/openai/`, `adapter/anthropic/`等）
3. IR持久化审计（`domains/session/v2/`, `ir_message_adapter.go`）
4. 序列化测试覆盖率审计

#### Agent 2: 多层队列/并发/容量控制审计
**关注点**:
- 前端并发与后端限流的解耦
- 队列容量控制与降级策略
- 并发竞态与死锁风险
- 异步任务的生命周期管理

**审计维度**:
1. 并发控制器审计（`domains/streaming/executors/`, `bg/rate_limiter.go`）
2. 队列管理审计（`bg/credential_forwarder.go`, `queue_manager.go`）
3. Redis降级场景审计
4. 竞态条件与锁控制审计

#### Agent 3: 存储层/迁移/数据完整性审计
**关注点**:
- Hot+Columnar分区架构的一致性
- 迁移脚本的幂等性与回滚安全性
- 大数据表的存储优化
- 数据完整性约束与级联关系

**审计维度**:
1. 迁移文件审计（`sql/migrations/startup/620-631`）
2. Hot表promote逻辑审计（`*_promote_*.sql`, `bg/vacuum_worker.go`）
3. embeddata同步审计（`installer/cmd/llm-gw-installer/embeddata/`）
4. db-changelog.md一致性审计

#### Agent 4: 错误处理/供应商质量/可观测性审计
**关注点**:
- 供应商请求错误的分类与记录
- 错误备用方案的触发逻辑
- 可观测性数据的完整性
- 日志/指标/追踪的关联性

**审计维度**:
1. 错误分类审计（`errorsx/classify.go`, `bg/provider_error_aggregator.go`）
2. 供应商错误记录审计（`provider_error_details`, `candidate_failure_logs`）
3. 可观测性sink审计（`cmd/gateway/main_dispatch_observation.go`）
4. 追踪链路审计（`domains/requestjourney/`）

#### Agent 5: UI/UX一致性与国际化审计
**关注点**:
- UI组件的复用与一致性
- 国际化覆盖率
- 菜单组织与交互规范
- 加载/空状态/错误提示的统一

**审计维度**:
1. 国际化覆盖率审计（`web/src/locales/*/`）
2. UI组件一致性审计（`web/src/components/`, `web/src/views/`）
3. 菜单结构审计（`web/public/menu-config.json`）
4. 交互规范审计（确认对话框、数据表格、分页等）

---

## 三、审计输入

### 3.1 代码修改清单（24小时内）
```
核心提交（按时间倒序）:
1448f1ecb - fix(db-sync): close audit blind spot — add functions dimension
45840e2ca - fix(audit-data-closure): post-C-completion audit fixes
72d1c72b3 - fix(audit-data-closure-C): wire writer outbox enqueue + reaper
ef6273f5b - fix(audit): close soft-delete migration and terminal-state gaps
a57df720e - fix(db-sync): surface silent schema-import failure
94a0221eb - fix(audit-data-closure): address 4 deferred audit findings
55114b147 - feat(providers): soft-delete for providers and terminal 'deleted' status
f1ae3c71e - fix(audit): second-pass review corrections to the 24h audit fixes
6b1aa4ad5 - fix(probe): correct periodic-quota self-check gaps
d558ec334 - fix(audit): close 24h-correction follow-up audit findings
f13c47ce7 - fix(journalsnapshot): harden adapter lifecycle, tenant scope
```

### 3.2 文档参考（24小时内修改）
```
docs/fixes/2026-08-31-data-closure-audit-implementation.md
docs/fixes/2026-08-30-complete-audit-implementation.md
docs/audit/2026-08-31-24h-correction-followup-audit.md
docs/audit/2026-08-31-24h-comprehensive-audit-final.md
docs/audit/2026-08-31-db-structure-consistency-audit.md
docs/audit/agent-1-ir-structure-audit.md
docs/audit/agent-2-concurrency-queue-audit.md
docs/audit/agent-3-storage-migration-audit.md
docs/audit/agent-4-error-handling-observability-audit.md
docs/audit/agent-5-ui-ux-consistency-audit.md
docs/audit/agent-6-code-debt-audit.md
AUDIT_PERIODIC_QUOTA_SELFCHECK_20260831.md
```

### 3.3 迁移文件（新增）
```
sql/migrations/startup/627_candidate_failure_logs_aggregation_id_unified.sql
sql/migrations/startup/628_candidate_failure_logs_promote_atomic_v3.sql
sql/migrations/startup/629_audit_attachments_cleanup.sql
sql/migrations/startup/630_session_aggregate_outbox.sql
sql/migrations/startup/631_provider_credential_soft_delete.sql (重命名自627)
```

---

## 四、审计方法

### 4.1 并行执行策略
```
T0: 主代理创建5个子代理，传递审计提示词
T1-T30: 5个子代理并行审计（每个预计20-30分钟）
T31: 主代理汇总结果，进行根因分析
T32: 主代理生成统一修复方案
T33: 主代理执行修复、测试、提交
```

### 4.2 审计检查清单

#### 每个子代理需要回答：
1. **闭环完整性**: 该领域的数据/流程/反馈是否形成闭环？
2. **遗漏点识别**: 是否存在数据丢失、流程中断、反馈缺失？
3. **一致性验证**: 代码实现与文档/测试/数据库是否一致？
4. **安全性检查**: 是否存在并发问题、内存泄漏、异常未处理？
5. **优化建议**: 是否存在冗余代码、性能瓶颈、可维护性问题？

#### 输出格式（每个子代理）：
```markdown
# Agent X: [领域名称]审计报告

## 审计范围
- 文件清单: [...]
- 审计维度: [...]

## 审计发现

### P0级问题（立即修复）
- [问题描述]
  - 位置: [file:line]
  - 影响: [...]
  - 建议: [...]

### P1级问题（下一迭代）
- [问题描述]
  - 位置: [file:line]
  - 影响: [...]
  - 建议: [...]

### P2级问题（技术债）
- [问题描述]
  - 位置: [file:line]
  - 影响: [...]
  - 建议: [...]

## 闭环验证
- [ ] 数据闭环: [通过/不通过 - 原因]
- [ ] 流程闭环: [通过/不通过 - 原因]
- [ ] 反馈闭环: [通过/不通过 - 原因]

## 建议的修复方案
[...]
```

---

## 五、审计输出

### 5.1 主代理汇总报告
```markdown
# 2026-08-31 24小时修正最终审计报告

## 执行摘要
- 审计时间范围: 2026-08-30 00:00 ~ 2026-08-31 07:00
- 审计代码行数: [...]
- 审计文件数: [...]
- 发现问题数: P0:[...] P1:[...] P2:[...]

## 问题汇总（按优先级）

### P0级问题（共X个）
1. [问题] - 涉及领域: [...] - 修复状态: [...]
2. ...

### P1级问题（共X个）
[...]

### P2级问题（共X个）
[...]

## 系统层面根因分析
[跨领域的深层次原因分析]

## 统一修复方案
[系统性的解决方案]

## 已执行修复
- 提交: [commit hash]
- 文件修改: [...]
- 测试验证: [...]

## 遗留工作
[需要下一步处理的事项]
```

### 5.2 后续行动（handoff）
使用handoff技能规划下一步工作，包括：
- 遗留P1/P2问题的修复计划
- 技术债清理计划
- 文档补充计划
- 测试覆盖率提升计划

---

## 六、审计执行

### 6.1 子代理提示词模板

#### Agent 1提示词（IR核心数据结构）
```
你是Agent 1，负责审计LLM Gateway的IR（Intermediate Representation）核心数据结构与传输闭环。

审计范围:
- internal/ir/*.go（IR定义）
- adapter/unified/*.go（协议适配器）
- adapter/openai/*.go, adapter/anthropic/*.go等（厂商适配器）
- domains/session/v2/ir_message_adapter.go（IR持久化）
- 相关测试文件

审计重点:
1. IR数据结构完整性
   - 检查RequestIR/ResponseIR的所有字段是否完整传输
   - 关注Extensions/Modalities/Metadata的持久化
   - 验证压缩脱敏前后的数据保留

2. 协议转换闭环
   - 检查OpenAI/Anthropic/Gemini等协议到IR的转换
   - 检查IR到各协议的序列化
   - 验证流式/非流式会话的处理差异

3. 路由决策数据
   - 检查routing_tracker的数据是否完整记录
   - 验证瀑布调度的每一步尝试是否可追溯
   - 确认路由失败的原因是否清晰记录

4. 持久化闭环
   - 检查IR到数据库的映射（session_turns, request_logs, session_bodies等）
   - 验证附件引用的存储策略
   - 确认多模态内容的元数据保存

审计方法:
1. 读取24小时内修改的相关文件
2. 分析数据流向：Client → Adapter → IR → Processing → IR → Adapter → Client
3. 检查每个环节的数据完整性
4. 查找数据丢失、字段缺失、映射错误
5. 验证测试覆盖率

输出格式: 按照第四节4.2的格式输出审计报告

开始审计。
```

#### Agent 2提示词（多层队列/并发）
```
你是Agent 2，负责审计LLM Gateway的多层队列、并发控制与容量管理。

审计范围:
- domains/streaming/executors/*.go（请求调度）
- bg/credential_forwarder.go（凭据队列转发）
- bg/rate_limiter.go（限流器）
- domains/streaming/concurrency/*.go（并发控制）
- domains/streaming/queue_manager.go（队列管理）

审计重点:
1. 队列分层架构
   - 检查总待处理队列与分维队列的解耦
   - 验证按权重负载均衡的实现
   - 确认队列容量控制逻辑

2. 并发控制安全性
   - 检查channel切换的竞态条件（如credForwarder.replaceDepth）
   - 验证锁的使用是否正确（sync.Mutex, sync.RWMutex）
   - 查找可能的死锁场景
   - 检查goroutine泄漏风险

3. Redis降级场景
   - 验证Redis不可用时的本地降级逻辑
   - 检查容量计数器的一致性
   - 确认降级恢复的平滑性

4. 错误传播与重试
   - 检查队列满时的拒绝策略
   - 验证预检拒绝的分类（并发限制 vs 密钥耗尽）
   - 确认panic恢复机制

审计方法:
1. 读取24小时内修改的相关文件
2. 绘制队列与并发控制的架构图
3. 使用静态分析查找竞态条件
4. 检查每个channel操作的安全性
5. 验证降级场景的测试覆盖

输出格式: 按照第四节4.2的格式输出审计报告

开始审计。
```

#### Agent 3提示词（存储层/迁移）
```
你是Agent 3，负责审计LLM Gateway的存储层、SQL迁移与数据完整性。

审计范围:
- sql/migrations/startup/620-631.sql（新迁移文件）
- installer/cmd/llm-gw-installer/embeddata/startup/（embeddata同步）
- bg/vacuum_worker.go（Hot表promote逻辑）
- docs/db-changelog.md（迁移变更日志）
- scripts/pg-table-copy.sh（252同步脚本）

审计重点:
1. 迁移文件完整性
   - 检查627-631迁移文件的幂等性
   - 验证.down.sql回滚脚本的安全性
   - 确认迁移文件已同步到embeddata
   - 核对db-changelog.md的checksum记录

2. Hot+Columnar架构一致性
   - 检查所有大数据表是否采用hot+columnar分区
   - 验证promote函数的原子性（v3版本）
   - 确认hot表的8小时保留策略
   - 检查分区表的列存储设置

3. 数据完整性约束
   - 验证所有外键的ON DELETE行为
   - 检查CHECK约束的合理性
   - 确认唯一索引的覆盖范围
   - 查找可能的数据孤儿

4. 存储优化
   - 验证573迁移（body列删除）的执行状态
   - 检查大字段外置的完整性（session_bodies表）
   - 确认索引的合理性（避免冗余索引）

审计方法:
1. 读取24小时内新增/修改的迁移文件
2. 对比sql/migrations与installer/embeddata的一致性
3. 验证db-changelog.md的完整性
4. 检查promote函数的逻辑正确性
5. 使用verify-db-consistency.sh验证结构一致性

输出格式: 按照第四节4.2的格式输出审计报告

开始审计。
```

#### Agent 4提示词（错误处理/可观测性）
```
你是Agent 4，负责审计LLM Gateway的错误处理、供应商质量度量与可观测性。

审计范围:
- errorsx/classify.go（错误分类）
- bg/provider_error_aggregator.go（供应商错误聚合）
- domains/requestjourney/*.go（请求链路追踪）
- cmd/gateway/main_dispatch_observation.go（观测sink）
- admin/provider_diagnose.go（供应商诊断）

审计重点:
1. 供应商错误处理闭环
   - 检查供应商请求错误的分类是否完整
   - 验证错误信息是否记录到provider_error_details
   - 确认候选失败是否记录到candidate_failure_logs
   - 检查错误是否影响会话流程（think模式返回）

2. 错误备用方案
   - 验证路由重试逻辑的触发条件
   - 检查备用凭据的选择策略
   - 确认降级模型的切换逻辑
   - 查找备用失败后的兜底方案

3. 可观测性数据完整性
   - 检查trace事件的完整记录（request_logs.trace_events）
   - 验证快照数据的采集（Snapshot结构）
   - 确认日志与指标的关联性
   - 检查ObservationSink的panic恢复

4. 供应商质量度量
   - 验证错误聚合的统计逻辑
   - 检查凭据健康度评分的计算
   - 确认供应商SLA的监控指标
   - 查找错误趋势的可视化

审计方法:
1. 读取24小时内修改的相关文件
2. 绘制错误处理的完整流程图
3. 验证每种错误类型的处理路径
4. 检查可观测性数据的采集点
5. 确认错误信息的展示位置（UI）

输出格式: 按照第四节4.2的格式输出审计报告

开始审计。
```

#### Agent 5提示词（UI/UX一致性）
```
你是Agent 5，负责审计LLM Gateway前端的UI/UX一致性与国际化覆盖率。

审计范围:
- web/src/views/*.vue（页面组件）
- web/src/components/*.vue（UI组件）
- web/src/locales/*/*.ts（国际化文件）
- web/public/menu-config.json（菜单配置）
- web/src/composables/*.ts（可组合函数）

审计重点:
1. 国际化覆盖率
   - 统计硬编码中文的位置与数量
   - 验证所有语言文件的key一致性
   - 检查缺失翻译的key
   - 确认日期/时间/数字的本地化格式

2. UI组件一致性
   - 检查是否存在多套确认对话框实现
   - 验证加载状态的统一展示（Spinner/Skeleton）
   - 确认空状态的一致表达
   - 检查数据表格的统一样式

3. 菜单组织易用性
   - 验证菜单分组的合理性（避免单一分组过多项）
   - 检查菜单项的命名一致性
   - 确认权限控制的正确性
   - 查找冗余或重复的菜单项

4. 交互规范一致性
   - 检查表单验证的统一处理
   - 验证错误提示的展示方式
   - 确认成功/失败反馈的一致性
   - 检查分页组件的统一使用

审计方法:
1. 读取24小时内修改的前端文件
2. 统计国际化覆盖率（硬编码 vs i18n key）
3. 查找重复实现的UI逻辑
4. 验证菜单配置的合理性
5. 检查UI组件库的使用一致性

输出格式: 按照第四节4.2的格式输出审计报告

开始审计。
```

---

## 七、执行时间表

| 时间 | 任务 | 负责人 |
|------|------|--------|
| T0 (00:00) | 创建审计计划文档 | 主代理 |
| T1 (00:05) | 启动5个子代理 | 主代理 |
| T2-T30 (00:05-00:35) | 并行审计执行 | 5个子代理 |
| T31 (00:35) | 汇总审计结果 | 主代理 |
| T32 (00:40) | 根因分析与修复方案 | 主代理 |
| T33 (00:45) | 执行修复与测试 | 主代理 |
| T34 (01:00) | 提交代码并推送 | 主代理 |
| T35 (01:05) | 生成handoff文档 | 主代理 |

---

## 八、成功标准

### 8.1 审计完成标准
- [ ] 5个子代理全部完成审计报告
- [ ] 主代理完成汇总与根因分析
- [ ] 识别所有P0级问题并提供修复方案
- [ ] 生成可执行的修复计划

### 8.2 修复完成标准
- [ ] 所有P0级问题已修复并通过测试
- [ ] 代码已提交到主分支并推送
- [ ] db-changelog.md已更新
- [ ] handoff文档已生成

### 8.3 质量标准
- [ ] 代码修复通过单元测试
- [ ] 迁移文件通过幂等性测试
- [ ] UI修改通过国际化检查
- [ ] 无新增技术债或已记录

---

## 九、风险与缓解

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|---------|
| 子代理审计时间超时 | 延迟交付 | 中 | 设置30分钟超时，超时则使用部分结果 |
| 发现大量P0问题 | 无法在一次修复 | 中 | 按影响面优先级排序，分批修复 |
| 修复引入新问题 | 回归风险 | 低 | 所有修复必须有测试覆盖 |
| 迁移脚本执行失败 | 数据不一致 | 低 | 使用事务包裹，提供回滚脚本 |

---

## 十、附录

### 10.1 审计检查清单

#### 数据闭环检查
- [ ] IR数据结构是否完整定义（所有字段有文档说明）
- [ ] IR在协议转换中是否有数据丢失
- [ ] IR持久化到数据库是否有字段遗漏
- [ ] 附件与媒体是否有完整的引用链
- [ ] 多轮对话上下文是否可追溯
- [ ] 压缩脱敏前后数据是否可对比

#### 流程闭环检查
- [ ] 请求处理流程是否有中断点
- [ ] 路由决策过程是否完整记录
- [ ] 多层队列是否有死锁或阻塞
- [ ] 错误处理是否有兜底方案
- [ ] 重试逻辑是否有上限控制
- [ ] 超时处理是否有清理逻辑

#### 反馈闭环检查
- [ ] 供应商错误是否被记录
- [ ] 错误信息是否展示给用户
- [ ] 性能指标是否被采集
- [ ] 日志与追踪是否可关联
- [ ] UI反馈是否及时一致
- [ ] 审计数据是否可查询

### 10.2 常见问题模式

#### 数据丢失模式
- 字段在结构体中定义但未序列化
- 序列化时字段被忽略（`json:"-"`）
- 数据库列缺失或类型不匹配
- 中间层转换时字段未映射

#### 并发问题模式
- channel关闭后仍有发送操作
- 锁的粒度过大导致性能问题
- 读写锁使用不当
- goroutine泄漏

#### 存储问题模式
- 迁移文件未同步到embeddata
- Hot表未配置promote任务
- 外键约束缺失或过严
- 索引冗余或缺失

#### 可观测性盲区
- 关键路径缺少日志
- 错误信息未分类
- 追踪链路断裂
- 指标维度不足

---

**审计准备完成，等待执行。**
