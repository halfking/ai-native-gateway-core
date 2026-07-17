# Sessions V2 - 会话存储优化架构

> **状态**: Phase 0 & 1.1 完成，Phase 1.2 进行中  
> **最后更新**: 2026-07-17

## 快速开始

### 当前进度

```
✅ Phase 0: 准备阶段 (已完成)
   ├─ ✅ Migration 430 创建
   ├─ ✅ 数据映射文档
   └─ ✅ Git合并处理

✅ Phase 1.1: 核心Writer实现 (已完成)
   ├─ ✅ TurnWriter (并发安全)
   ├─ ✅ SessionBodiesWriter (增量存储)
   ├─ ✅ SessionAggregator (快照聚合)
   ├─ ✅ TurnLogsWriter (环节日志)
   └─ ✅ SessionWriterV2 (协调器)

🔄 Phase 1.2: SubmitMode检测 (进行中)
📋 Phase 1.3: SessionCacheV2 (待开始)
```

## 核心文件

### 数据库Schema
```
sql/migrations/startup/
├─ 430_sessions_v2_schema.sql      # V2表结构
└─ 430_sessions_v2_schema.down.sql # 回滚脚本
```

### 文档
```
docs/
├─ SESSION_V2_DATA_MAPPING.md              # 数据映射详解
└─ SESSION_V2_IMPLEMENTATION_SUMMARY.md    # 完整实施方案
```

### 代码
```
domains/session/v2/
├─ turn_writer.go           # 轮次元数据写入
├─ bodies_writer.go         # 正文增量写入
├─ session_aggregator.go    # 会话快照聚合
├─ turn_logs_writer.go      # 环节日志写入
└─ session_writer_v2.go     # 主协调器
```

## 测试Schema

```bash
# 创建V2表
psql -f sql/migrations/startup/430_sessions_v2_schema.sql

# 验证
psql -c "\dt gateway.sessions*"
psql -c "\d gateway.session_turns"

# 回滚
psql -f sql/migrations/startup/430_sessions_v2_schema.down.sql
```

## 核心优势

### 🎯 并行架构 - 零风险
- ✅ 旧系统（request_logs）完全不动
- ✅ 新系统（sessions）独立开发验证
- ✅ 通过Feature Flag控制流量
- ✅ 任何时候可一键回滚

### 💾 增量存储 - 高效率
```
V1: 每轮存储完整消息历史 (指数增长)
V2: 每轮只存储新增消息 (线性增长)

预计节省: 60-80% 磁盘空间
```

### 🔧 数据分离 - 易维护
```
sessions         → 会话快照（汇总统计）
session_turns    → 轮次元数据（不含正文）
session_bodies   → 正文增量（columnar压缩）
session_turn_logs → 环节日志（24h TTL）
```

## 架构对比

### V1体系（现有）
```
request_logs
├─ request_body (JSONB × 每轮全量)
├─ outbound_body (JSONB × 每轮全量)  ← 膨胀问题
└─ response_body (JSONB × 每轮全量)
```

### V2体系（新架构）
```
sessions (一条/会话)
├─ 汇总统计
└─ 最后轮次摘要

session_turns (元数据)
├─ 模型、Token、成本
└─ 压缩meta、verdict

session_bodies (columnar)
├─ request_delta  ← 只存新增！
└─ response_delta

session_turn_logs (24h TTL)
└─ 临时诊断数据
```

## 使用示例

### 写入一个Turn

```go
import "llm-gateway-go/domains/session/v2"

// 1. 创建Writers
turnWriter := v2.NewTurnWriter(db)
bodiesWriter := v2.NewSessionBodiesWriter(db)
aggregator := v2.NewSessionAggregator(db)
logsWriter := v2.NewTurnLogsWriter(db)

// 2. 创建协调器
writerV2 := v2.NewSessionWriterV2(
    turnWriter, 
    bodiesWriter, 
    aggregator, 
    logsWriter,
)

// 3. 写入请求
err := writerV2.Write(ctx, &v2.ProcessedRequest{
    SessionID: "gw_abc123",
    TenantID:  "default",
    RequestID: "req_xyz789",
    
    RequestBody:  messages,
    ResponseBody: response,
    
    CompressionApplied: true,
    TokensSaved: 5000,
    
    // ... 其他字段
})
```

### 查询Turn数据

```go
// 查询单个turn元数据
turn, err := turnWriter.GetTurn(ctx, requestID)

// 查询会话所有turns
turns, err := turnWriter.ListTurns(ctx, tenantID, sessionID, 100)

// 查询turn正文
bodies, err := bodiesWriter.GetBodies(ctx, tenantID, sessionID, turnNo)

// 重建完整历史（用于验证）
history, err := bodiesWriter.ReconstructFullHistory(ctx, tenantID, sessionID)
```

## 并发安全

### TurnWriter使用Advisory Lock

```go
// 自动处理并发turn_no分配
turnNo, err := turnWriter.AppendTurn(ctx, rec)

// 内部机制：
// 1. pg_advisory_xact_lock(hash(tenant:session))
// 2. SELECT MAX(turn_no) + 1
// 3. INSERT with unique constraint
// 4. Commit (自动释放锁)
```

**保证**：
- ✅ 同一会话内turn_no单调递增
- ✅ 无并发冲突
- ✅ request_id幂等性（ON CONFLICT DO NOTHING）

## 下一步

### 本周计划
- [ ] 实现SubmitMode检测器（识别客户端压缩行为）
- [ ] 实现SessionCacheV2（读取session_turns）
- [ ] 编写单元测试

### 下周计划
- [ ] 实现DualWriter（双写机制）
- [ ] 实现数据校验工具
- [ ] 集成到主Pipeline

## 监控指标

启用后可通过Prometheus监控：

```
sessions_v2_write_success_total      # V2写入成功次数
sessions_v2_write_failure_total      # V2写入失败次数
sessions_v2_write_latency_seconds    # V2写入延迟
sessions_v2_validation_diff_total    # 数据差异数量
```

## 回滚策略

### 紧急回滚（随时可执行）

```bash
# 1. 禁用V2读取
sessions_v2_l3_read: false
sessions_v2_primary_read: false

# 2. 停止副写
sessions_v2_shadow_write: false

# 3. 热重载配置
curl -X POST http://localhost:8080/api/admin/reload-config

# 系统立即回退到V1，零停机
```

### 完全删除V2表

```bash
# 仅在确认不再需要V2数据后执行
psql -f sql/migrations/startup/430_sessions_v2_schema.down.sql
```

## 详细文档

- **实施方案**: [docs/SESSION_V2_IMPLEMENTATION_SUMMARY.md](./SESSION_V2_IMPLEMENTATION_SUMMARY.md)
- **数据映射**: [docs/SESSION_V2_DATA_MAPPING.md](./SESSION_V2_DATA_MAPPING.md)
- **参考设计**: [~/workspace/ai-native-tools/llm-gateway/docs/拆分/08-会话存储与缓存优化.md](../../../llm-gateway/docs/拆分/08-会话存储与缓存优化.md)

## 贡献者

- 架构设计: llm-gateway-ops
- 参考文档: ~/workspace/ai-native-tools/llm-gateway/docs/拆分/
- 实施日期: 2026-07-17

---

**问题反馈**: 如遇问题请查阅文档或联系团队
