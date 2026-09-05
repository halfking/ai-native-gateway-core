# 24小时代码审计执行计划 (2026-09-04)

## 审计范围识别

根据git log分析,24小时内有以下**7大核心变更领域**:

### 1. **可用性增强 - Redis/DB故障容忍** (b31c227de)
- **文件范围**: 15+ 文件涉及 keystore, verifier, router, manager, statesource
- **审计重点**: 
  - Recovery gate 机制的闭环逻辑
  - Snapshot 缓存的stale状态处理
  - 故障模式下的数据一致性保障

### 2. **Dispatch调度系统修正** (e5476c2cb, f8af3a0c)
- **文件范围**: dimension_index.go, pipeline.go, queued_request.go, forwarder.go, governor_backend.go
- **审计重点**:
  - Composition-root 初始化顺序验证
  - DimensionIndex trimRingLocked 正确性
  - Backend lifecycle 边界条件处理
  - 并发锁粒度 (modelMu, selectedCredMu, stageMu)

### 3. **Analytics MV一致性修正** (2e1ccfb5f, be4bd0f48, a046f7739)
- **文件范围**: analytics.go, analytics_materialized.go, bg/mv_consistency.go
- **审计重点**:
  - routing_analytics_source probe过滤逻辑
  - MV rebuild 幂等性保障
  - Tenant-scope funnel cache 正确性
  - Migration 649 部署状态验证

### 4. **Turn Digest Phase 2** (d77cb92db, fa21a467b, 0f1a6f63)
- **文件范围**: sessiondigest/, sessionturnsdb/, admin/
- **审计重点**:
  - Digest生成的确定性算法验证
  - Backfill job 限流与幂等性
  - Hot+Columnar 转移任务正确性
  - 摘要数据的隐私保护验证

### 5. **IR协议与凭据管理** (b76f9f2d4, 8a27d7daa, 127d8c2f5)
- **文件范围**: ir/, secret/, admin/handler.go, web/credential-console
- **审计重点**:
  - Responses request shape 校验
  - Fernet envelope unwrap 逻辑
  - 凭据权限与readonly控制一致性

### 6. **节点探测与错误聚合** (476c3814, 44bc6723, 1bc4e5b9)
- **文件范围**: bg/nodeprobe/, provider/candidate_diagnostic_metrics.go
- **审计重点**:
  - Queue submission 重试与持久化
  - 错误聚合管道完整性
  - Probe流量排除逻辑一致性

### 7. **Plugin Runtime V2** (6290c7e4, 63a91afb, 2c072656)
- **文件范围**: plugin-runtime/, web/dynamic nav injection
- **审计重点**:
  - Manifest v2 schema 向后兼容性
  - Capability 鉴权三入口一致性
  - Lifecycle admin (upgrade/rollback/uninstall) 闭环

---

## 并行代理任务分配

### **Agent 1: 可用性与故障容忍审计**
**提示词**:
```
审计 commit b31c227de "feat(availability): keep serving through Redis/DB outages"

**任务目标**:
1. 绘制 Recovery Gate 流程闭环图:
   - 启动时的 snapshot 初始化
   - Redis/DB 故障检测触发机制
   - Stale state 降级服务路径
   - 恢复后的 snapshot 刷新逻辑

2. 验证数据一致性保障:
   - keystore_snapshot 与 DB 的同步机制
   - verifier 的 stale token 处理
   - router 的 outage 模式下的路由决策
   - NodeMirror cache 的失效策略

3. 检查并发安全:
   - recovery_gate_adapter 的锁粒度
   - snapshot 读写的竞态条件
   - outage_test 覆盖的边界场景

4. 输出结论:
   - 流程闭环是否完整
   - 数据闭环是否一致
   - 反馈闭环(监控/告警)是否存在
   - 发现的风险点与建议

**关键文件**:
- cmd/gateway/recovery_gate_adapter.go
- domains/authentication/keystore_sync.go
- domains/authentication/verifier_stale_test.go
- domains/ursm/v2/manager_outage_test.go
- domains/streaming/executors/router_outage_test.go
```

### **Agent 2: Dispatch调度系统审计**
**提示词**:
```
审计 commit e5476c2cb "fix(dispatch): close backend and lifecycle edge cases"

**任务目标**:
1. 验证 Composition-root 初始化顺序:
   - main_dispatch_backend.go 的组装顺序
   - forwarder 观察 backend 的时机
   - dispatcher.Close() 的清理顺序

2. 审计 DimensionIndex 正确性:
   - trimRingLocked 的非连续过期清理逻辑
   - MarkNode 的时机 (pipeline.go)
   - MaxKeys 边界条件处理

3. 检查 QueuedRequest 并发安全:
   - modelMu, selectedCredMu, stageMu 的保护范围
   - getter/setter 的锁粒度
   - Waterfall 流程中的竞态条件

4. 验证 Backend lifecycle:
   - governor_backend 的 Redis enforce fail-closed
   - backend.Close() 的资源释放
   - 边界条件测试覆盖度

5. 输出结论:
   - 三层队列架构的完整性
   - 并发安全保障
   - 发现的问题与修复建议

**关键文件**:
- domains/dispatch/dimension_index.go
- domains/dispatch/pipeline.go
- domains/dispatch/queued_request.go
- domains/dispatch/forwarder.go
- domains/dispatch/governor_backend.go
- cmd/gateway/main_dispatch_backend.go
```

### **Agent 3: Analytics MV一致性审计**
**提示词**:
```
审计 commit 2e1ccfb5f "fix(analytics): align routing stats source and harden MV rebuild"

**任务目标**:
1. 验证 Migration 649 正确性:
   - routing_analytics_source 的 probe 过滤条件
   - 8种探测类型的完整性 (probe_type IN (...))
   - MV 重建的幂等性保障

2. 检查数据源一致性:
   - admin/analytics.go 使用的数据源
   - bg/mv_consistency.go drift checker 的查询路径
   - 实时查询 vs MV 查询的过滤条件一致性

3. 验证历史数据处理:
   - 历史 NULL origin_stage 的业务语义
   - Migration 649 的向后兼容性
   - MV 刷新频率 (10分钟) 的合理性

4. 审计 Tenant-scope funnel cache:
   - commit a046f7739 的缓存键构造
   - probe 流量排除逻辑
   - 缓存失效策略

5. 输出结论:
   - 数据闭环完整性
   - probe 流量过滤的一致性
   - 部署验证 SQL 脚本
   - 发现的风险点

**关键文件**:
- sql/migrations/startup/649_routing_analytics_probe_filter.sql
- admin/analytics.go
- admin/analytics_materialized.go
- admin/funnel_cache.go
- bg/mv_consistency.go
```

### **Agent 4: Turn Digest & Session存储审计**
**提示词**:
```
审计 commit 0f1a6f63, d77cb92db, fa21a467b 的 Turn Digest Phase 2

**任务目标**:
1. 验证 Digest 生成闭环:
   - sessiondigest.Build() 的确定性算法
   - Rune 级截断 (260 runes) 的正确性
   - 多模态标记 ([附图×N]) 的生成逻辑
   - 隐私保护: tool arguments, base64, provider metadata 的移除

2. 审计 Backfill 机制:
   - Backfill job 的幂等性保障
   - 限流 (100行/秒) 的实现
   - 指数退避 (30s → 30min) 的触发条件
   - Backfill 进度的可观测性

3. 检查 Hot+Columnar 转移:
   - session_turns_hot (8小时) 的清理逻辑
   - 批量转移到 session_turns 的事务性
   - Columnar 表的分区策略
   - 转移任务的失败恢复

4. 验证数据完整性:
   - Request/Response → digest 的信息损失
   - 跨协议 (chat/message/response) 的统一性
   - 流式 vs 非流式会话的差异处理

5. 输出结论:
   - Turn Digest 的人类可读性
   - 存储闭环的完整性
   - 隐私保护的充分性
   - 性能基准与优化建议

**关键文件**:
- domains/sessiondigest/
- admin/sessionturnsdb/
- sql/migrations/startup/636_session_turns_digest.sql
- bg/session_turns_hot_mover.go
```

### **Agent 5: IR协议与错误处理审计**
**提示词**:
```
审计 IR 协议与错误处理的完整性

**任务目标**:
1. 验证 IR 结构完整性 (commit b76f9f2d4):
   - Responses request shape 校验逻辑
   - Extensions 透传机制
   - 参数注册表驱动的字段映射
   - 协议转换的 O(N) 复杂度保障

2. 审计供应商错误记录闭环:
   - candidate_failure_logs_hot 的写入路径
   - ProviderErrorAggregator 的聚合逻辑
   - provider_error_details 的粒度 (credential_id)
   - Admin API /error-stats 的查询路径

3. 检查错误反馈机制:
   - 错误信息是否返回客户端 (think-mode)
   - 错误触发的 NodeProbeWorker 探测
   - 凭据状态变更 → 候选缓存失效
   - Sticky 路由的清理逻辑

4. 验证流式错误处理:
   - StreamChunk 的 error 类型处理
   - 客户端断开的清理逻辑
   - 供应商流中断的错误记录
   - 工具参数跨块拼接的完整性

5. 输出结论:
   - 错误处理的反馈闭环
   - IR 协议转换的可靠性
   - 流式边界条件的覆盖度
   - 发现的遗漏与建议

**关键文件**:
- domains/ir/
- provider/candidate_diagnostic_metrics.go
- domains/streaming/executors/
- admin/analytics.go (error-stats API)
```

### **Agent 6: 节点探测与凭据管理审计**
**提示词**:
```
审计节点探测系统与凭据生命周期

**任务目标**:
1. 验证 NodeProbeWorker 流程闭环 (commit 476c3814):
   - Queue submission 的重试逻辑
   - 持久化机制 (是否有 DB 记录)
   - 两轮验证: Direct vs Gateway
   - 探测触发条件: 创建/更新/故障检测

2. 审计错误聚合管道:
   - candidate_failure_logs_hot 的数据流向
   - ProviderErrorAggregator 的聚合频率 (10分钟)
   - provider_error_details 的指纹 (credential_id)
   - Admin API 的可视化完整性

3. 检查凭据生命周期 (commit 127d8c2f5, 8a27d7daa):
   - Secret unwrap (v1:legacy fernet) 的兼容性
   - 凭据权限与 readonly 控制的一致性
   - 凭据更新触发的探测
   - 凭据禁用触发的候选缓存失效

4. 验证自动恢复机制:
   - 指数退避 (5s → 24h, 最多7次)
   - 恢复后的状态变更
   - Circuit breaker 的状态转换

5. 输出结论:
   - 探测系统的完整性
   - 凭据管理的安全性
   - 自动恢复的可靠性
   - 可观测性指标覆盖度

**关键文件**:
- bg/nodeprobe/
- secret/aes_gcm.go
- admin/handler.go (credential APIs)
- admin/handler_cred_encrypt_test.go
- provider/candidate_diagnostic_metrics.go
```

### **Agent 7: 大数据分区表审计**
**提示词**:
```
审计所有大数据表的 Hot+Columnar 架构完整性

**任务目标**:
1. 枚举所有大数据表:
   - session_turns_hot + session_turns ✅
   - request_logs_hot + request_logs ✅
   - candidate_failure_logs_hot + ? (需确认)
   - provider_error_details (是否有分区表?)
   - streaming_usage, token_usage, routing_analytics_7d (是否需要分区?)
   - 其他大数据表

2. 验证 Hot 表特性:
   - 8小时保留窗口的实现
   - 更新/删除操作的限制
   - 索引策略的合理性

3. 检查批量转移任务:
   - 转移任务的触发频率
   - 事务性保障
   - 失败恢复机制
   - 转移后的数据验证

4. 审计 Columnar 表特性:
   - 分区键的选择
   - 分区大小的合理性
   - 历史分区的清理策略
   - 查询性能优化 (MV, 索引)

5. 输出结论:
   - 大数据表清单
   - Hot+Columnar 覆盖度
   - 缺失的分区表建议
   - 性能优化建议

**关键文件**:
- sql/migrations/startup/ (所有 migration)
- bg/session_turns_hot_mover.go
- bg/request_logs_hot_mover.go
- admin/analytics_materialized.go
```

---

## 汇总分析任务

在所有并行代理完成后,主代理执行:

1. **系统层面关联分析**:
   - 流程闭环: 识别跨模块的断点
   - 数据闭环: 识别数据流向的遗漏
   - 反馈闭环: 识别监控/告警的盲区

2. **安全性综合评估**:
   - 并发锁的粒度与死锁风险
   - 资源泄漏的潜在点
   - 异常处理的覆盖度
   - 边界条件的测试覆盖

3. **优先级问题清单**:
   - P0: 必须立即修复
   - P1: 本周内完成
   - P2: 月度持续改进

4. **文档与运维建议**:
   - 架构文档更新
   - 运维手册补充
   - 监控指标增强
   - 测试覆盖提升

---

## 执行方式

```bash
# 启动7个并行代理
/handoff "Agent 1 提示词" --background
/handoff "Agent 2 提示词" --background
/handoff "Agent 3 提示词" --background
/handoff "Agent 4 提示词" --background
/handoff "Agent 5 提示词" --background
/handoff "Agent 6 提示词" --background
/handoff "Agent 7 提示词" --background

# 等待所有代理完成后,主代理汇总分析
```
