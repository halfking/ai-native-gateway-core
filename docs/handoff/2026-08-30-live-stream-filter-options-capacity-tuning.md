# 实时请求流筛选选项数据容量调整 - 交接文档

**日期**: 2026-08-30  
**会话**: ZCode  
**分支**: `fix/gateway-survival-observability-20260830`  
**任务类型**: 性能优化 + 用户体验改进

---

## 📋 当前状态

### 已完成的工作（上一个会话）

1. **深入诊断实时流筛选选项过少问题**
   - 连接到 252 生产环境检查实际数据
   - 发现根本原因：实时流窗口限制（每泳道仅保留 20 条请求）
   - 验证代码逻辑完全正确，问题在于容量设计

2. **创建完整的诊断工具集**
   - 自动诊断脚本：`scripts/debug-live-stream-filters.sh`
   - 浏览器诊断脚本：`web/debug-filters.js`
   - 5 个完整文档（见下方文件清单）

3. **代码已提交并推送**
   - Commit: `67442e4bb` - docs(diagnostic): 实时请求流筛选选项过少问题诊断工具和文档
   - 分支: `fix/gateway-survival-observability-20260830`
   - 状态: ✅ 已推送到远程

### 未完成的工作（需要继续）

用户提出新需求：
1. **增加每个泳道的容量**：从 20 条增加到 50 条
2. **增加整个请求队列长度**：需要重新计算，目标约 1000 条

---

## 🎯 新任务需求

### 需求 1：调整泳道容量

**当前配置**：
```go
// admin/live_stream_redis_store.go
const LiveStreamLaneVisibleLimit = 20  // 每个泳道最多 20 条请求
```

**目标配置**：
```go
const LiveStreamLaneVisibleLimit = 50  // 增加到 50 条
```

**影响评估**：
- Redis 内存使用增加约 150%（20 → 50）
- SSE 初始传输量增加约 150%
- 前端渲染压力增加（但可接受，泳道采用虚拟滚动）
- **正面效果**：筛选选项数据覆盖面增加 2.5 倍

### 需求 2：调整全局队列长度

**当前配置**：
```go
// admin/live_stream_redis_store.go
const liveStreamReplayLimit = 200  // 初始回放限制
```

**目标配置**：需要计算并调整到约 1000 条

**计算逻辑**：
```
假设：
- 4 个维度：credential, vendor, provider, model
- 每个维度平均泳道数：
  - credential: 10-15 个
  - vendor: 5-8 个
  - provider: 7-10 个
  - model: 8-12 个
- 每个泳道 50 条

理论最大值 = (15 + 8 + 10 + 12) × 50 = 2,250 条

但实际请求在不同维度间**共享**（同一条请求出现在多个维度），
去重后的唯一请求数约为泳道总数的 50-60%。

建议配置：
- liveStreamReplayLimit = 1000  （初始回放）
- 主队列保留 1200 条（留 20% 余量）
```

**相关常量**：
```go
// admin/live_stream_redis_store.go
const (
    LiveStreamLaneVisibleLimit  = 50    // 每泳道可见限制（从 20 → 50）
    liveStreamReplayLimit       = 1000  // 初始回放限制（从 200 → 1000）
    liveStreamMainQueueLimit    = 1200  // 主队列保留数（新增，从隐式 200 → 1200）
)
```

---

## 📂 相关文件

### 需要修改的文件

1. **`admin/live_stream_redis_store.go`**
   - 调整 `LiveStreamLaneVisibleLimit = 50`
   - 调整 `liveStreamReplayLimit = 1000`
   - 新增或调整主队列限制常量

2. **`admin/live_stream_sse.go`** (可能需要)
   - 检查是否有相关的配置或缓冲区大小
   - 检查 `InitialReplayLimit` 配置

### 已创建的诊断文件（上一会话）

```
llm-gateway-go/
├── README_FILTER_FIX.md
├── scripts/
│   └── debug-live-stream-filters.sh
├── web/
│   └── debug-filters.js
└── docs/troubleshooting/
    ├── FILTER_OPTIONS_FINAL_REPORT.md      # 最终报告 ⭐
    ├── LIVE_STREAM_FILTER_ANALYSIS.md      # 详细分析
    ├── live-stream-filter-options-diagnostic.md
    └── live-stream-filter-options-usage.md
```

---

## 🔍 技术背景

### 数据结构

```
Redis 实时流数据结构：
├── llmgw:live:main (ZSET)                    ← 主队列，所有请求的 request_id
│   └── score = unix_ms, member = request_id
│
├── llmgw:live:dim:{dimension}:{key} (ZSET)   ← 维度队列（泳道）
│   ├── llmgw:live:dim:vendor:openai
│   ├── llmgw:live:dim:provider:pulian
│   ├── llmgw:live:dim:model:gpt-4
│   └── llmgw:live:dim:credential:17
│   └── score = unix_ms, member = tile JSON (slim)
│
└── llmgw:live:req:{request_id} (STRING)      ← 请求详情，完整 LiveRequest
```

### 容量计算公式

```
总内存占用 ≈ 主队列大小 + Σ(维度队列大小)

主队列：
- 每条 request_id: ~50 bytes (含 ZSET overhead)
- 1200 条 ≈ 60 KB

维度队列（泳道）：
- 每个 tile (slim): ~200 bytes
- 假设 40 个泳道，每个 50 条
- 40 × 50 × 200 bytes ≈ 400 KB

请求详情：
- 每个完整 LiveRequest: ~1-2 KB
- 1200 条 ≈ 1.2-2.4 MB

总计：~3 MB (可接受)
```

### 252 生产环境数据

```sql
-- 最近 24 小时统计（来自上一会话诊断）
总请求数: 5,620
唯一 client_model: 11 个
唯一 canonical_model: 7 个
唯一 provider_id: 8 个
唯一 credential_id: 13 个

-- Providers 分布
主力供应商（占 80%+ 流量）：
- pulian (id=5917)
- glm-5.2-month (id=12763)
- 速云U站 (id=13092)

其他供应商：
- nvidia, zhipu, volcano-tokenplan, apiclaude
```

---

## 💡 实现建议

### 步骤 1：调整常量

```go
// admin/live_stream_redis_store.go

// 原值：
const LiveStreamLaneVisibleLimit = 20
const liveStreamReplayLimit = 200

// 新值：
const LiveStreamLaneVisibleLimit = 50   // 2.5x 增加
const liveStreamReplayLimit = 1000      // 5x 增加

// 新增（如果没有明确的主队列限制常量）：
const liveStreamMainQueueLimit = 1200   // 主队列最大保留数
```

### 步骤 2：更新主队列修剪逻辑

查找主队列的 ZREMRANGEBYSCORE 或 ZREMRANGEBYRANK 调用，确保：
- 主队列最多保留 1200 条（而不是隐式的 200 条）
- TTL 保持 24 小时（`LiveStreamLaneQueueRetention`）

### 步骤 3：验证配置一致性

检查以下配置是否需要同步调整：
```go
// admin/live_stream_sse.go
type LiveStreamConfig struct {
    InitialReplayLimit int  // 客户端连接时回放多少条
    // 应该 = liveStreamReplayLimit = 1000
}
```

### 步骤 4：更新文档

更新 `README_FILTER_FIX.md` 和 `FILTER_OPTIONS_FINAL_REPORT.md`，说明：
- 容量已调整到 50 条/泳道
- 主队列已扩大到 1000-1200 条
- 内存影响评估

---

## 🧪 测试验证

### 单元测试

检查是否有使用硬编码 20 的测试：
```bash
grep -rn "LiveStreamLaneVisibleLimit\|20.*tile\|20.*request" admin/*_test.go
```

需要更新的测试断言：
```go
// 从
assert.Equal(t, 20, len(lane.Requests))
// 改为
assert.Equal(t, LiveStreamLaneVisibleLimit, len(lane.Requests))
```

### 集成测试

1. **本地验证**：
   ```bash
   # 启动本地环境
   docker-compose up -d
   
   # 发起 100 条测试请求
   for i in {1..100}; do
     curl -X POST http://localhost:8080/v1/chat/completions \
       -H "Authorization: Bearer test-key" \
       -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}'
   done
   
   # 检查 Redis
   redis-cli ZCARD llmgw:live:main
   redis-cli ZCARD llmgw:live:dim:model:gpt-4
   ```

2. **252 测试环境验证**：
   ```bash
   # 运行诊断脚本
   ssh -p 25022 root@115.29.212.252 \
     "cd /path/to/llm-gateway-go && ./scripts/debug-live-stream-filters.sh"
   ```

3. **前端验证**：
   - 打开实时流页面
   - 点击筛选按钮，检查选项数量
   - 预期：模型 7-11 个，供应商 7-8 个，原厂 5-8 个

---

## ⚠️ 风险评估

### 内存风险

**影响**：
- Redis 内存增加约 3 MB（从 ~1 MB → ~3 MB）
- 252 服务器 Redis 内存充足，可接受

**监控指标**：
```bash
# 检查 Redis 内存使用
redis-cli INFO memory | grep used_memory_human
```

### 性能风险

**SSE 传输**：
- 初始回放从 200 条 → 1000 条
- 单次传输约 1-2 MB（取决于压缩）
- 对于现代网络，可接受

**前端渲染**：
- 泳道使用虚拟滚动，最多渲染 50 条可见项
- 不会造成性能问题

### 降级方案

如果发现性能问题：
1. 回退到 30 条/泳道（折中方案）
2. 主队列保持 1000，但 InitialReplayLimit 限制为 500

---

## 🎯 成功标准

### 功能验证
- ✅ 每个泳道最多显示 50 条请求
- ✅ 主队列保留约 1000-1200 条请求
- ✅ 筛选选项数量增加到 7-8 个供应商

### 性能验证
- ✅ Redis 内存增加在 5 MB 以内
- ✅ SSE 初始连接时间 < 2 秒
- ✅ 前端页面流畅，无卡顿

### 数据验证
- ✅ 运行诊断脚本，唯一供应商数 = 7-8
- ✅ 运行诊断脚本，Redis 主队列 > 500 条

---

## 📚 参考资料

### 代码位置
- `admin/live_stream_redis_store.go:70-85` - 常量定义
- `admin/live_stream_redis_store.go:320-400` - Record 逻辑
- `admin/live_stream_redis_store.go:910-1110` - Snapshot 构建
- `admin/live_stream_sse.go:370-410` - Config 结构

### 相关文档
- `docs/troubleshooting/FILTER_OPTIONS_FINAL_REPORT.md` - 问题分析
- `README_FILTER_FIX.md` - 快速指南
- `docs/06-deployment/02-database/redis-live-stream.md` (如果存在)

### Git 历史
- `67442e4bb` - docs(diagnostic): 实时请求流筛选选项过少问题诊断工具和文档
- `f375ace24` - fix(stream): close legacy empty-response recovery gap

---

## 🚀 下一步行动

1. **修改常量配置**（5 分钟）
   - 调整 `LiveStreamLaneVisibleLimit = 50`
   - 调整 `liveStreamReplayLimit = 1000`
   - 新增主队列限制

2. **更新相关测试**（10 分钟）
   - 查找硬编码 20 的测试
   - 改用常量引用

3. **本地验证**（15 分钟）
   - 运行单元测试
   - 启动本地环境测试

4. **提交并推送**（5 分钟）
   - Commit message: `feat(live-stream): increase lane capacity to 50 and queue limit to 1000`

5. **部署到 252 测试**（20 分钟）
   - 运行诊断脚本验证
   - 前端功能测试

---

**交接完成时间**: 2026-08-30  
**下一会话建议时长**: 45-60 分钟  
**复杂度**: 🟢 低（主要是常量调整和测试）
