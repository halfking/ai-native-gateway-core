# 会话管理模块全景架构分析

**分析日期：** 2026-07-10  
**分析范围：** 会话缓存、会话压缩、安全检测、合规检查、会话全景分析、健康检查、数据聚合

---

## 一、核心发现

### 1.1 数据生产机制已找到 ✅

**session_summaries 的初始化：数据库触发器（实时聚合）**

- **位置：** `sql/migrations/startup/350_session_analytics_fix.sql`
- **机制：** PostgreSQL AFTER INSERT trigger on `request_logs` 表
- **触发时机：** 每次请求日志写入 `request_logs` 时自动触发
- **执行逻辑：**
  ```sql
  CREATE TRIGGER trg_update_session_summary
      AFTER INSERT ON request_logs
      FOR EACH ROW
      WHEN (NEW.gw_session_id IS NOT NULL AND NEW.gw_session_id != '')
      EXECUTE FUNCTION update_session_summary();
  ```

**update_session_summary() 函数做了什么：**
1. **UPSERT session_dim** - 维护会话生命周期（active/closed/idle）
2. **UPSERT session_summaries** - 实时聚合统计数据：
   - 请求计数、成功/错误数
   - 成本累加（input/output/total）
   - Token 累加（prompt/completion）
   - 延迟统计（min/max/avg）
   - 模型/供应商/工作类型数组去重追加

**关键点：**
- ✅ 不需要额外的应用层代码触发
- ✅ 数据库级别保证原子性和一致性
- ✅ 分区表支持：父表触发器自动作用于所有分区
- ⚠️ 依赖 request_logs 表的字段完整性

---

## 二、会话管理模块职责分层

### 2.1 数据层（Database Layer）

#### 核心表结构

**request_logs（原始数据）**
- 每次 LLM 请求的完整记录
- 包含：request_id, gw_session_id, gw_task_id, tokens, cost, latency
- 触发器来源：`domains/hooks/observability/telemetry/client.go`

**session_dim（维度表）**
- 会话生命周期管理：active/closed/idle
- 关联字段：gw_session_id ↔ task_id ↔ owner_user ↔ client_id
- 用途：
  - 输出合规检查的数据拥有者判定
  - Top客户端/Top任务排行统计
  - 会话状态追踪

**session_summaries（聚合表）**
- 实时聚合统计数据（由触发器自动维护）
- 包含：
  - 基础统计：request_count, cost, tokens, latency
  - 数组字段：models_used, providers, client_models, work_types
  - AI生成字段：title, summary, key_topics, user_intent（由后台worker补充）
  - 健康字段：health_score, health_grade, outcome（由后台worker补充）

**session_state_snapshots（状态快照）**
- 会话状态完整快照（由 domains/session/session_db_writer.go 写入）
- 用途：会话恢复、审计、调试

---

### 2.2 请求处理层（Request Pipeline）

#### 2.2.1 请求前置处理（PreRouting Hooks）

**1. domains/hooks/sessionaudit/hook.go - 会话审计**
- **职责：** 内容安全检测（prompt injection, jailbreak, 有害内容）
- **时机：** 请求到达，路由前
- **输出：** 
  - Pass/Warn/Block/NeedApproval 决策
  - 发布 SessionAuditEvent 到 EventBus
  - NeedApproval 时创建审批记录
- **数据流：** 
  - 检测结果写入 `env.Metadata["audit_result"]`
  - 异步深度检测（多模型共识）

**2. domains/hooks/session-inspector/hook.go - 会话巡检**
- **职责：** 会话级异常检测（频率、并发、错误率、突发、模型切换）
- **时机：** 请求到达，路由前
- **检测项：**
  - IdleSessionInspector：会话空闲超时
  - BurstInspector：短时间内请求突发
  - ErrorRateInspector：错误率过高
  - ModelSwitchInspector：频繁切换模型
  - ConcurrentRequestInspector：并发请求过多
  - TenantActiveSessionInspector：租户活跃会话数
- **输出：**
  - findings 写入 `env.Metadata["session_findings"]`
  - 发布 EventBus 事件
  - Critical finding → 429/403 阻断

**3. domains/hooks/compression/session_compressor.go - 会话压缩**
- **职责：** 智能上下文压缩（LLM总结 + 机械裁剪）
- **时机：** 请求路由前，构建 outbound body 时
- **策略：**
  - Delta-append：增量追加新消息
  - LLM summary：有损压缩（保留关键信息）
  - Sliding-window trim：机械裁剪（降级方案）
- **数据依赖：**
  - SessionCache (L1/L2/L3 三层缓存)
  - Memora 服务（LLM 总结）
- **输出：**
  - PrepareResult（outbound_body, compression_strategy）
  - 写入 request_logs.compression_* 字段

**4. domains/security/hook.go - 安全检测**
- **职责：** Prompt injection 增强检测
- **时机：** 请求到达时
- **输出：** 阻断或记录安全事件

---

#### 2.2.2 请求后置处理（PostRouting / ResponseInterceptors）

**1. domains/hooks/goal/audit_hook.go - 目标审计**
- **职责：** 用户目标检测与偏离告警
- **时机：** LLM 响应返回后
- **输出：** 目标偏离事件

**2. domains/hooks/outputcompliance/hook.go - 输出合规**
- **职责：** 输出内容脱敏与所有权控制
- **时机：** LLM 响应返回前
- **检查逻辑：**
  - 从 session_dim 查询 owner_user（数据拥有者）
  - 从 request_logs.api_key_owner_user 获取调用者
  - 拥有者不匹配 → 脱敏处理
- **输出：** 原文或脱敏后的响应

**3. domains/hooks/handoff/trigger_hook.go - 切换触发**
- **职责：** 检测会话是否需要切换模型/供应商
- **时机：** 响应返回后
- **触发条件：**
  - Token 接近上限
  - 错误率过高
  - 成本超预算
- **输出：**
  - 更新 session_summaries.handoff_count
  - 发布切换建议事件

---

### 2.3 后台任务层（Background Workers）

#### 1. bg/session_health_worker.go - 健康度计算
- **职责：** 批量计算会话健康分数
- **周期：** 每 60 分钟
- **扫描条件：**
  ```sql
  WHERE last_request_at < NOW() - INTERVAL '1 hour'
    AND health_score IS NULL
  ```
- **计算因子：**
  - 成功率、平均延迟、模型切换次数
  - 合规问题数、是否检测到 PII/toxic/injection
- **输出：**
  - UPDATE session_summaries SET health_score, health_grade, outcome

#### 2. domains/analysis/workers/session_summary_worker.go - 会话摘要生成
- **职责：** 调用 LLM 生成会话标题和摘要
- **触发：** 订阅 EventSessionClosed 事件
- **流程：**
  1. 从 request_logs 提取会话消息
  2. 调用 LLM（gpt-4o-mini）生成标题、摘要、关键主题、用户意图
  3. UPDATE session_summaries SET title, summary, key_topics, user_intent
- **策略：**
  - 全量摘要：首次生成
  - 滚动摘要：增量更新（省 token）
- **依赖：**
  - domains/sessionsummary/summarizer.go

---

## 三、数据流向图

```
[请求进入]
   │
   ├──> [SessionAuditHook] ────> 安全检测 ────> 阻断/通过
   │
   ├──> [SessionInspectorHook] ─> 会话巡检 ───> 阻断/告警/通过
   │
   ├──> [SessionCompressor] ────> 压缩上下文 ─> outbound_body
   │
   ├──> [路由 + 转发上游]
   │
   ├──> [LLM 响应返回]
   │
   ├──> [OutputComplianceHook] ─> 输出合规检查 ─> 原文/脱敏
   │
   ├──> [HandoffTriggerHook] ───> 切换检测
   │
   ├──> [telemetry/client.go] ──> INSERT request_logs ──┐
   │                                                      │
   └────────────────────────────────────────────────────┘
                                                           ↓
                                    [PostgreSQL Trigger: update_session_summary()]
                                                           │
                        ┌──────────────────────────────────┴─────────────────────┐
                        ↓                                                         ↓
              UPSERT session_dim                                     UPSERT session_summaries
        (gw_session_id, task_id, status)                    (实时聚合: count, cost, tokens, latency)
                        │                                                         │
                        └─────────────────┬───────────────────────────────────────┘
                                          ↓
                            [后台 Worker 补充字段]
                                          │
                    ┌─────────────────────┼─────────────────────┐
                    ↓                     ↓                     ↓
        [SessionHealthWorker]  [SessionSummaryWorker]  [其他分析Worker]
         每60分钟补充           事件触发生成             异步分析任务
         health_score          title/summary            优化建议等
                    │                     │                     │
                    └─────────────────────┼─────────────────────┘
                                          ↓
                              [session_summaries 完整数据]
                                          │
                                          ↓
                          [Dashboard API 查询展示]
                               (/api/admin/dashboard/session-overview)
                                          │
                                          ↓
                              [前端 SessionStatsPanel.vue]
```

---

## 四、职责边界清晰化

### 4.1 不重复的职责分工

| 模块 | 职责 | 处理时机 | 数据输出 |
|------|------|----------|----------|
| **SessionAuditHook** | 内容安全（prompt injection, jailbreak） | PreRouting | 阻断决策 + 审批记录 |
| **SessionInspectorHook** | 会话行为异常（频率、并发、突发） | PreRouting | 阻断/告警 + findings |
| **SessionCompressor** | 上下文压缩（LLM总结+裁剪） | PreRouting | outbound_body |
| **SecurityHook** | 通用安全检测（增强版 prompt injection） | PreRouting | 阻断/记录 |
| **OutputComplianceHook** | 输出脱敏与所有权控制 | PostRouting | 脱敏后响应 |
| **HandoffTriggerHook** | 会话切换检测 | PostRouting | 切换建议 |
| **TelemetryClient** | 请求日志记录 | PostRouting | INSERT request_logs |
| **DB Trigger** | 实时统计聚合 | request_logs INSERT 后 | UPSERT session_summaries |
| **SessionHealthWorker** | 健康分数计算 | 定时（60分钟） | health_score/grade |
| **SessionSummaryWorker** | AI 摘要生成 | 会话关闭事件 | title/summary |

---

### 4.2 重复职责识别

#### ⚠️ 潜在重复：安全检测

**SessionAuditHook vs SecurityHook**
- **SessionAuditHook：** 专注会话级审计（支持审批流程）
- **SecurityHook：** 通用安全检测（独立插件体系）
- **建议：** 
  - SecurityHook 作为 SessionAuditHook 的检测器之一
  - 避免在两处重复调用相同的 prompt injection 检测模型

#### ⚠️ 潜在重复：会话状态追踪

**session_dim vs session_state_snapshots**
- **session_dim：** 轻量维度表（status, task_id, owner_user）
- **session_state_snapshots：** 完整状态快照（raw_snapshot JSON）
- **建议：**
  - session_dim：用于实时查询和关联统计
  - session_state_snapshots：用于会话恢复和深度审计
  - 保持分离，职责明确

---

## 五、数据完整性验证

### 5.1 关键检查点

**✅ 确认正常工作的部分：**

1. **request_logs 写入** - telemetry/client.go 每次请求都写入
2. **触发器聚合** - 350 迁移修复后正常工作
3. **后端API降级保护** - session_dim 不存在时仍能返回数据
4. **健康度计算** - 后台worker定期执行

**⚠️ 需要验证的部分：**

1. **EventSessionClosed 事件发布**
   ```bash
   # 验证方法：搜索事件发布代码
   grep -r "EventSessionClosed.*Publish\|Publish.*EventSessionClosed" --include="*.go"
   ```
   
2. **LLM摘要功能是否启用**
   - 检查 SessionSummaryWorker 是否在 main.go 中注册
   - 检查 LLMClient 是否正确配置

3. **session_dim 的数据同步**
   ```sql
   -- 生产环境检查
   SELECT COUNT(*) as session_dim_count,
          (SELECT COUNT(DISTINCT gw_session_id) FROM request_logs WHERE gw_session_id IS NOT NULL) as request_logs_sessions,
          (SELECT COUNT(*) FROM session_summaries) as session_summaries_count;
   ```

---

## 六、优化建议

### 6.1 架构优化

**1. 统一安全检测入口**
```
建议：将 SecurityHook 作为 SessionAuditHook 的检测器插件
   SessionAuditHook
      ├─> FastDetector（当前）
      ├─> SecurityHook.PromptInjectionDetector（新增）
      └─> 其他检测器
```

**2. 事件驱动标准化**
```
当前状态：部分功能依赖事件，部分直接调用
建议：统一使用 EventBus 解耦
   - 会话关闭 → EventSessionClosed
   - 会话异常 → EventSessionAnomaly
   - 切换触发 → EventHandoffTriggered
```

**3. 数据预聚合优化**
```
当前：触发器实时聚合（每个 INSERT 都触发）
优化：批量聚合（每5分钟聚合一次）
好处：减轻数据库压力，适用于高并发场景
```

---

### 6.2 监控与告警

**关键指标监控：**

1. **触发器执行监控**
   ```sql
   -- 检查触发器是否正常执行
   SELECT COUNT(*) as with_session,
          COUNT(*) FILTER (WHERE gw_session_id IS NOT NULL) as should_trigger
   FROM request_logs
   WHERE ts > NOW() - INTERVAL '1 hour';
   
   SELECT COUNT(*) as actually_aggregated
   FROM session_summaries
   WHERE updated_at > NOW() - INTERVAL '1 hour';
   ```

2. **健康度覆盖率**
   ```sql
   SELECT 
     COUNT(*) as total,
     COUNT(health_score) as with_health,
     COUNT(health_score) * 100.0 / COUNT(*) as coverage_pct
   FROM session_summaries
   WHERE last_request_at < NOW() - INTERVAL '2 hours';
   ```

3. **摘要生成覆盖率**
   ```sql
   SELECT 
     COUNT(*) as total,
     COUNT(title) as with_title,
     COUNT(summary) as with_summary,
     COUNT(title) * 100.0 / COUNT(*) as title_coverage_pct
   FROM session_summaries;
   ```

---

## 七、总结

### 7.1 核心成果

✅ **完全理清了 session_summaries 的数据来源**
- 数据库触发器实时聚合（350迁移修复后正常工作）
- 不需要应用层额外代码触发

✅ **清晰划分了各模块职责**
- 请求前：安全检测、会话巡检、上下文压缩
- 请求后：输出合规、切换检测、日志记录
- 后台：健康计算、AI摘要生成

✅ **识别了潜在的重复职责**
- 安全检测：SessionAuditHook vs SecurityHook
- 状态追踪：session_dim vs session_state_snapshots

### 7.2 下一步行动

**立即执行：**
1. 生产环境数据完整性检查（SQL验证脚本）
2. 确认 EventSessionClosed 事件发布机制
3. 验证 SessionSummaryWorker 是否已注册

**短期优化：**
1. 统一安全检测入口（避免重复调用）
2. 补充监控指标和告警规则
3. 优化高并发场景的触发器性能

**长期规划：**
1. 标准化事件驱动架构
2. 考虑批量聚合替代实时触发器
3. 完善会话生命周期管理

---

**文档版本：** v1.0  
**维护者：** Backend Team  
**最后更新：** 2026-07-10
