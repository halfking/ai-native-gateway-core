# 会话存储V2架构实施方案 - 完整总结

> **版本**: v1.0
> **日期**: 2026-07-17
> **状态**: Phase 0 & Phase 1.1 完成
> **作者**: llm-gateway-ops

---

## 执行摘要

本方案采用**并行新表架构**，在不改动现有request_logs体系的前提下，创建完全独立的会话存储V2系统。通过Feature Flag控制流量路由，实现零风险渐进式迁移。

### 核心优势

✅ **零风险**：旧系统完全不动，V2独立开发验证
✅ **可回滚**：任何时候可一键切回旧系统
✅ **可验证**：双写期间持续对账，充分观察
✅ **高性能**：增量存储 + columnar压缩，预计节省60-80%磁盘

---

## Phase 0: 准备阶段 ✅ 已完成

### 已交付成果

#### 1. 数据库Schema (Migration 430)

**文件**: `sql/migrations/startup/430_sessions_v2_schema.sql`

创建了4张核心表：

```sql
gateway.sessions              -- 会话快照（一条/会话）
gateway.session_turns         -- 轮次元数据（不含正文）
gateway.session_bodies        -- 正文增量（columnar存储）
gateway.session_turn_logs     -- 环节日志（24h TTL）
```

**关键特性**：
- ✅ 按月分区（RANGE partitioning）
- ✅ 完整的RLS租户隔离
- ✅ session_bodies使用columnar格式
- ✅ 自动分区管理函数
- ✅ 完整的down migration回滚脚本

**验证命令**：
```bash
# 测试环境执行
psql -f sql/migrations/startup/430_sessions_v2_schema.sql

# 验证表创建
\dt gateway.sessions*
\d gateway.session_turns

# 测试回滚
psql -f sql/migrations/startup/430_sessions_v2_schema.down.sql
```

#### 2. 数据映射文档

**文件**: `docs/SESSION_V2_DATA_MAPPING.md`

**内容**：
- ✅ V1→V2完整字段映射表
- ✅ SQL转换示例
- ✅ 增量提取算法
- ✅ 数据校验规则
- ✅ 性能优化建议
- ✅ 监控指标定义

#### 3. Git合并处理

**状态**: ✅ 已完成
- 拉取最新远程代码（4个commits）
- 解决本地改动冲突
- 代码库已同步到最新状态

---

## Phase 1.1: SessionWriterV2 ✅ 已完成

### 核心组件实现

#### 1. TurnWriter - 轮次元数据写入器

**文件**: `domains/session/v2/turn_writer.go`

**核心功能**：
```go
func (w *TurnWriter) AppendTurn(ctx context.Context, rec TurnRecord) (turnNo int, err error)
```

**并发安全机制**：
- ✅ 使用PostgreSQL advisory lock
- ✅ 基于(tenant_id, session_id)哈希生成锁键
- ✅ 事务内串行分配turn_no
- ✅ request_id幂等性保证（ON CONFLICT DO NOTHING）

**特性**：
- 支持按request_id查询单个turn
- 支持按session_id列出所有turns
- 自动计算turn_no（MAX + 1）
- 完整的质量标记（live/backfill, verified/inferred）

#### 2. SessionBodiesWriter - 正文增量写入器

**文件**: `domains/session/v2/bodies_writer.go`

**核心创新**：增量存储避免request_logs的全量JSONB膨胀

```go
type BodiesRecord struct {
    RequestDelta  []Message  // 只存本轮新增消息
    ResponseDelta []Message  // 本轮回复
    OutboundBody  []Message  // 压缩后正文（审计用）
}
```

**特性**：
- ✅ 增量存储（核心优化）
- ✅ 附件引用（不存base64）
- ✅ 全景重建功能（ReconstructFullHistory）
- ✅ 按turn查询和列出所有bodies

#### 3. SessionAggregator - 会话快照聚合器

**文件**: `domains/session/v2/session_aggregator.go`

**功能**：
```go
type SessionUpdate struct {
    LastTurnNo          int       // 替换
    LastRequestSummary  string    // 替换
    TurnIncrement       int       // 累加
    TokensIncrement     int       // 累加
    CostIncrement       float64   // 累加
}
```

**特性**：
- ✅ INSERT ... ON CONFLICT DO UPDATE（原子性）
- ✅ 增量更新计数器
- ✅ 替换最后轮次摘要
- ✅ 支持关闭会话和设置元数据

#### 4. TurnLogsWriter - 环节日志写入器

**文件**: `domains/session/v2/turn_logs_writer.go`

**功能**：
- ✅ 记录每个处理环节（routing/compression/llm_call等）
- ✅ 24小时TTL自动过期
- ✅ 会话关闭时聚合到sessions.turn_logs_summary
- ✅ 支持按turn或session查询日志

#### 5. SessionWriterV2 - 主协调器

**文件**: `domains/session/v2/session_writer_v2.go`

**核心方法**：
```go
func (w *SessionWriterV2) Write(ctx context.Context, req *ProcessedRequest) error
```

**工作流程**：
1. 检测提交模式（DetectSubmitMode）
2. 提取请求增量（ExtractRequestDelta）
3. 写入session_turns（元数据）
4. 写入session_bodies（增量正文）
5. 写入session_turn_logs（环节日志）
6. 异步更新sessions（快照）

**特性**：
- ✅ 协调所有V2表的写入
- ✅ 异步更新session快照（不阻塞主流程）
- ✅ 完整的错误处理和日志记录
- ✅ 智能增量提取算法

---

## 架构对比

### V1体系（现有）
```
request_logs 主表
├─ request_body (JSONB全量)     ← 每轮重复存储完整历史
├─ outbound_body (JSONB全量)    ← 膨胀问题
├─ response_body (JSONB全量)
└─ 所有元数据字段混在一起
```

**问题**：
- ❌ 每轮存3份完整JSONB
- ❌ 长会话磁盘线性膨胀
- ❌ 元数据和正文耦合
- ❌ 无法独立优化存储格式

### V2体系（新架构）
```
sessions (会话快照)
├─ 一个会话一条记录
└─ 汇总统计 + 最后轮次摘要

session_turns (轮次元数据)
├─ 模型、供应商、Token、成本
├─ 压缩元数据、治理verdict
└─ 不含正文，轻量级

session_bodies (正文增量，columnar)
├─ request_delta (只存新增消息)  ← 核心优化
├─ response_delta (本轮回复)
└─ columnar格式 + zstd压缩

session_turn_logs (环节日志，24h TTL)
└─ 临时诊断数据，自动清理
```

**优势**：
- ✅ 增量存储避免重复
- ✅ 元数据和正文分离
- ✅ columnar优化大字段存储
- ✅ 临时数据自动清理
- ✅ 预计节省60-80%磁盘

---

## 数据流对比

### V1写入流程
```
请求到达
  ↓
处理Pipeline
  ↓
PersistRequestLog (一次写入)
  ↓
request_logs (完整JSONB × 3)
```

### V2写入流程
```
请求到达
  ↓
处理Pipeline
  ↓
SessionWriterV2.Write
  ├─ TurnWriter.AppendTurn
  │   └─ session_turns (元数据)
  ├─ BodiesWriter.WriteBodies
  │   └─ session_bodies (增量)
  ├─ TurnLogsWriter.WriteStage
  │   └─ session_turn_logs (环节)
  └─ SessionAggregator.UpdateSession (异步)
      └─ sessions (快照)
```

### 双写模式（Phase 2）
```
请求到达
  ↓
处理Pipeline
  ↓
DualWriter.Write
  ├─ V1Writer (主写，失败则整体失败)
  │   └─ request_logs
  └─ V2Writer (副写，失败不影响主流程)
      └─ sessions体系
```

---

## 下一步计划

### Phase 1.2: SubmitMode检测器 (1-2天)

**目标**：实现客户端提交模式的多信号检测

**文件**: `domains/session/v2/submit_mode_detector.go`

**检测优先级**：
```
P0: X-Gw-Submit-Mode header  ← 最高优先级
P1: 消息数回退检测
P2: Summary marker检测
P3: Orphaned tool_result检测
P4: LCS稳定前缀检测
P5: 首请求或冷启动
```

**实现要点**：
- [ ] Header解析
- [ ] LCS算法实现
- [ ] Summary marker正则匹配
- [ ] 单元测试覆盖所有场景

### Phase 1.3: SessionCacheV2 (2-3天)

**目标**：重构缓存体系，读取session_turns

**文件**: `domains/session/v2/cache_v2.go`

**架构**：
```
L0: TurnDeltaStorage (增量)
L1: CompressionMetaCache (元数据)
L2: GovernanceCache (verdict)
L3: SessionTurnsReader (读V2表)
```

**实现要点**：
- [ ] L3改读session_turns
- [ ] L1只存元数据不存body
- [ ] L0增量存储逻辑
- [ ] 与旧缓存并行运行

### Phase 2: 双写机制 (3-5天)

**文件**: `domains/session/dual_writer.go`

**核心逻辑**：
```go
type DualWriter struct {
    v1Writer *RequestLogWriter
    v2Writer *SessionWriterV2
    flags    *FeatureFlags
    metrics  *DualWriteMetrics
}

func (w *DualWriter) Write(ctx context.Context, req *ProcessedRequest) error {
    // 1. 主写V1
    if err := w.v1Writer.Write(ctx, req); err != nil {
        return err  // 主写失败则整体失败
    }

    // 2. 副写V2
    if w.flags.IsEnabled("sessions_v2_shadow_write") {
        if err := w.v2Writer.Write(ctx, req); err != nil {
            log.Error("v2 shadow write failed", err)
            w.metrics.V2WriteFailed.Inc()
        } else {
            w.metrics.V2WriteSuccess.Inc()
        }
    }

    return nil
}
```

**监控指标**：
- [ ] sessions_v2_write_success_total
- [ ] sessions_v2_write_failure_total
- [ ] sessions_v2_write_latency_seconds
- [ ] sessions_v2_validation_diff_total

### Phase 3: 数据校验工具 (2-3天)

**文件**: `cmd/tools/validate_sessions_v2.go`

**校验项**：
1. 请求ID一致性（V1和V2的request_id应一一对应）
2. 轮次数一致性（COUNT(*) 应相等）
3. Token总数一致性（SUM(tokens) 应相等）
4. 成本一致性（SUM(cost) 应相等）
5. 增量重建验证（重建的全景应等于V1的request_body）

**输出**：
```json
{
  "session_id": "gw_abc123",
  "validation_status": "ok",
  "differences": [],
  "v1_turns": 10,
  "v2_turns": 10,
  "v1_tokens": 15000,
  "v2_tokens": 15000
}
```

---

## Feature Flags配置

### Phase 1-2: Shadow Write（双写，旧读）
```yaml
sessions_v2_enabled: false
sessions_v2_shadow_write: true
sessions_v2_rollout_percent: 10  # 10%灰度
sessions_v2_l3_read: false
sessions_v2_primary_read: false
```

### Phase 3: Dual Read（双写，双读对账）
```yaml
sessions_v2_shadow_write: true
sessions_v2_rollout_percent: 50  # 50%灰度
sessions_v2_l3_read: true
sessions_v2_dual_read: true
sessions_v2_primary_read: false
```

### Phase 4: Primary Read（双写，新读为主）
```yaml
sessions_v2_shadow_write: true
sessions_v2_rollout_percent: 100  # 100%
sessions_v2_l3_read: true
sessions_v2_dual_read: true
sessions_v2_primary_read: true
```

### Phase 5: Full Migration（新写新读）
```yaml
sessions_v2_enabled: true
sessions_v2_shadow_write: false  # 停止副写
sessions_v2_rollout_percent: 100
sessions_v2_l3_read: true
sessions_v2_dual_read: false
sessions_v2_primary_read: true
```

---

## 回滚策略

### 紧急回滚（任何阶段）

**步骤1: 立即禁用V2读取**
```bash
# 更新配置
sessions_v2_l3_read: false
sessions_v2_primary_read: false

# 热重载配置（无需重启）
curl -X POST http://localhost:8080/api/admin/reload-config
```

**步骤2: 停止副写**
```bash
sessions_v2_shadow_write: false
```

**步骤3: 验证回退**
```bash
# 验证所有请求走V1
SELECT COUNT(*) FROM request_logs WHERE ts > NOW() - INTERVAL '5 minutes';

# 验证V2不再写入
SELECT COUNT(*) FROM gateway.session_turns WHERE ts > NOW() - INTERVAL '5 minutes';
```

**步骤4: 评估数据**
```bash
# 运行数据一致性校验
go run cmd/tools/validate_sessions_v2.go --session-id=<id>
```

**步骤5: 决策**
- 如果数据可修复：运行补偿脚本
- 如果数据无法修复：执行down migration

### 完全回滚（删除V2表）

```bash
# 仅在确认不再需要V2数据后执行
psql -f sql/migrations/startup/430_sessions_v2_schema.down.sql
```

**⚠️ 警告**：此操作会删除所有V2表数据，不可恢复！

---

## 门禁指标

### Phase 2 → Phase 3 门禁
- ✅ V2写入成功率 >= 99.99%
- ✅ 数据一致性差异率 < 0.01%
- ✅ V2写入延迟 p99 < 100ms

### Phase 3 → Phase 4 门禁
- ✅ 双读对账差异率 < 0.001%
- ✅ V2读取延迟 p99 < 50ms
- ✅ 缓存命中率 >= 90%

### Phase 4 → Phase 5 门禁
- ✅ V2稳定运行 >= 7天
- ✅ 零生产事故
- ✅ 回滚演练通过
- ✅ 人工最终审批

---

## 预期收益

### 磁盘空间节省

**估算**（基于典型会话分布）：
- 当前：每轮 ~3× 完整JSONB（request + outbound + response）
- 优化后：每轮 ~1× 增量delta
- **预计节省：60-80%**

**示例**：
```
10轮会话，每轮平均10条消息，每条消息1KB：

V1: 10 turns × (10 msg × 1KB × 3) = 300 KB
V2: 10 turns × (1 new msg × 1KB × 2) = 20 KB

节省：(300 - 20) / 300 = 93%
```

**注意**：实际节省取决于：
- 会话长度分布
- 客户端压缩行为
- 附件大小占比
- 需要真实数据压测验证

### 查询性能提升

- **元数据查询**：session_turns不含正文，查询更快
- **正文查询**：columnar格式优化大字段读取
- **缓存命中**：L1不存body，内存利用率提升

### 运维改善

- **分区管理**：自动按月分区，便于归档清理
- **数据质量**：source_kind/quality标记便于追溯
- **故障诊断**：session_turn_logs提供完整处理链路

---

## 风险与缓解

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|----------|
| 双写性能影响 | 中 | 低 | 副写失败不阻塞，异步更新session快照 |
| 增量提取错误 | 高 | 中 | 充分的单元测试，双读对账持续验证 |
| turn_no并发冲突 | 高 | 低 | advisory lock保证串行，重试机制 |
| 历史回填失败 | 中 | 中 | 可重跑，分批执行，完整的错误日志 |
| 数据不一致 | 高 | 低 | 持续对账，自动补偿，人工审计 |

---

## 项目时间线

```
Week 1 (7/17-7/21): Phase 1完成
├─ Phase 1.2: SubmitMode检测
├─ Phase 1.3: SessionCacheV2基础
└─ 单元测试

Week 2 (7/22-7/26): Phase 2完成
├─ DualWriter实现
├─ 监控指标接入
├─ 数据校验工具
└─ 集成测试

Week 3 (7/29-8/2): Phase 3灰度
├─ 10% shadow write
├─ 持续对账验证
└─ 性能监控

Week 4 (8/5-8/9): Phase 4扩大灰度
├─ 50% → 100% shadow write
├─ 开始dual read
└─ 数据一致性验证

Week 5 (8/12-8/16): Phase 5切换准备
├─ 100% dual read验证
├─ 回滚演练
└─ 门禁审批

Week 6 (8/19-8/23): 完全切换
├─ V2成为主读写
├─ V1降级为备份
└─ 文档交付
```

**并行任务**：
- 历史数据回填（Week 2-5，不阻塞主线）
- 前端V2适配（Week 3-5，独立开发）

---

## 交付清单

### 已完成 ✅
- [x] Migration 430 (up/down)
- [x] 数据映射文档
- [x] TurnWriter实现
- [x] SessionBodiesWriter实现
- [x] SessionAggregator实现
- [x] TurnLogsWriter实现
- [x] SessionWriterV2协调器

### 进行中 🔄
- [ ] SubmitMode检测器
- [ ] SessionCacheV2
- [ ] 单元测试编写

### 待开始 📋
- [ ] DualWriter
- [ ] 数据校验工具
- [ ] 历史回填脚本
- [ ] 前端V2 API
- [ ] 监控Dashboard
- [ ] 运维文档

---

## 联系方式

**技术负责人**: llm-gateway-ops
**项目文档**: `docs/SESSION_V2_*.md`
**代码目录**: `domains/session/v2/`
**Migration**: `sql/migrations/startup/430_*.sql`

**紧急联系**: 如遇生产问题，立即执行回滚策略并通知团队

---

**最后更新**: 2026-07-17
**下次审查**: 完成Phase 1.2后
