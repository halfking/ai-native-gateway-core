# compression-bench — 会话压缩性能基准测试工具

## 概述

`compression-bench` 是 llm-gateway-go 的会话压缩算法基准测试工具，用于：
1. 从生产 DB 拉取历史 request_logs
2. 对每条记录执行压缩算法
3. 记录压缩前后的指标（字节数、token 数、消息数）
4. 输出详细的分析报告和优化建议

## 核心算法架构

### 三层压缩策略

llm-gateway-go 的会话压缩采用**智能滑动窗口 + 分段增量**架构：

#### 1. **Delta-append（增量追加）**
- 使用 LCS 指纹算法识别客户端新增的消息
- 只追加新消息到已压缩的历史，避免重复发送
- **充分利用 LLM 的 KV 缓存**：相同前缀复用缓存，降低首 token 延迟

#### 2. **Sliding Window（智能滑动窗口）**
三个独立触发条件（OR 逻辑）：
- **TOKEN 触发**：outbound body 超过 contextWindow × 0.85
- **COUNT 触发**：消息数 ≥50（防止工具调用链无限增长）
- **IDLE 触发**：会话空闲 ≥5 分钟且消息数 ≥10

触发后优先使用 **LOSSLESS LLM 总结**（保留所有关键值、错误信息、ID），失败后才降级为机械 trim。

#### 3. **Mechanical Trim（机械裁剪）**
- 最后兜底策略
- 从中间删除旧消息，保留最新的 N 条
- 损失可恢复性：被删除的消息仍在 SessionCache/request_logs 中

### 分段利用 LLM 缓存的设计

**为什么分段？**
- LLM 按前缀匹配 KV 缓存（如 Claude 的 prompt caching）
- 如果每次发送完整历史 → 缓存命中率可能降低
- 如果只发送增量 → 更容易保持稳定前缀。具体缓存命中和延迟收益必须按上游模型的账单/遥测验证，不能由本工具推断。

**实现方式：**
```
请求 1: [system, user1, asst1]               → LLM 缓存整个前缀
请求 2: [system, user1, asst1, user2]        → 复用前 3 条缓存，只推理 user2
请求 3: [system, user1, asst1, user2, asst2, user3] → 复用前 5 条缓存，只推理 user3
```

**Gateway 的职责：**
1. 通过 delta-append 保持前缀不变（不插入、不重排）
2. 只在触发滑动窗口时才重构历史（生成总结）
3. 总结后插入 `[smm_v1:...]` 标记，下次 delta 时跳过总结块

## 使用方法

### 基础用法

```bash
# 连接 252 DB，测试最近 7 天的 100 条记录
go run ./cmd/compression-bench/ \
  -dsn="postgres://llm_gateway:xxx@127.0.0.1:25432/llm_gateway?sslmode=disable" \
  -days=7 \
  -max-samples=100 \
  -protocol=openai \
  -context-window=128000
```

### 测试模式

#### 1. `prepare` 模式（默认）— 完整压缩流程
测试完整的 SessionCompressor 逻辑：
- Phase 1: Delta-append（需要 session cache）
- Phase 2: 滑动窗口触发检测
- Phase 3: LLM 总结 / 机械 trim

```bash
go run ./cmd/compression-bench/ \
  -test-mode=prepare \
  -context-window=128000
```

**注意**：`prepare` 模式使用进程内 L1 SessionCache，并且按 `ts ASC` 串行回放以保持 turn 顺序。该模式不覆盖 Redis/PG cache 故障恢复或 LLM summary provider 的真实效果。

#### 2. `mechanical` 模式 — 机械 trim 快速验证
直接调用 `CompressMessagesIfNeededBody`，跳过 cache 和 delta 逻辑：

```bash
go run ./cmd/compression-bench/ \
  -test-mode=mechanical \
  -context-window=32000
```

**适用场景**：
- 快速验证 trim 逻辑正确性
- 无 Redis/cache 环境的本地测试
- 评估机械压缩的最坏效果（baseline）

### 共享会话模式

模拟多轮对话，所有 row 共享同一个 session_id：

```bash
go run ./cmd/compression-bench/ \
  -share-session \
  -serial \
  -context-window=8000
```

**参数说明**：
- `-share-session`：所有 row 使用同一 session_id
- `-serial`：串行执行（delta-append 需要前序状态）
- **建议搭配小 context-window 触发压缩**

### 输出 JSON 结果

```bash
go run ./cmd/compression-bench/ \
  -output=bench_results.json \
  -max-samples=500
```

## 输出报告解读

### 示例报告（格式示例，不是生产基线）

```
============================================================
          SESSION COMPRESSION BENCHMARK REPORT
============================================================
Total rows processed:     69
Protocol:                 openai
Context window:           8000 tokens

--- Strategy Distribution ---
  mechanical_trim                    5 (  7.2%)
  noop                              64 ( 92.8%)

--- Lossiness Distribution ---
  none                              64 ( 92.8%)
  tail                               5 (  7.2%)

--- Compression Ratios (after/before) ---
  Overall bytes ratio:     0.8351  (saved 398KB)
  Overall tokens ratio:    0.0000  (saved 0 tokens)
  Overall msgs ratio:      0.0000  (saved -215 msgs)

  Median bytes ratio (P50): 1.0000
  P90 bytes ratio:          1.0000
  P95 bytes ratio:          1.0000
  P99 bytes ratio:          1.0000
  Min/Max bytes ratio:      0.2166 / 1.0000

--- Per-Strategy Breakdown ---
Strategy                   Count  AvgBytesR AvgTokensR   AvgMsgsR   BytesSaved
---------------------------------------------------------------------------
mechanical_trim                5     0.3864     0.0000     0.0000       398613
noop                          64     1.0000     0.0000     0.0000            0

--- Optimisation Suggestions ---

=== Summary ===
✅ Compression is effective: 16.5% average byte reduction

Top strategies by bytes saved:
  1. mechanical_trim: 398KB saved (count=5, avg ratio=0.3864)
  2. noop: 0 bytes saved (count=64, avg ratio=1.0000)
```

### 关键指标

| 指标 | 含义 | 理想值 |
|------|------|--------|
| Overall bytes ratio | 压缩后/压缩前字节比 | 按协议、模型窗口与数据集建立基线 |
| Strategy Distribution | 各策略触发占比 | 与真实会话长度分布对比 |
| Lossiness: tail | 机械 trim（可恢复） | 趋势应受控，不能替代质量审计 |
| Lossiness: whole | LLM 总结（不可恢复） | 必须抽样核验关键事实保留 |
| Lossiness: none | 无损（delta-only） | 结合 session cache hit 指标观察 |

### 优化建议解读

1. **⚠️ High no-op rate: 100%**  
   → 所有请求都没触发压缩，可能原因：
   - context-window 设置过大（建议测试时设为 8000-32000）
   - 数据集的请求都太短
   - SessionCache 未生效（prepare 模式需要 cache）

2. **⚠️ Degraded mode usage: 15%**  
   → 15% 的压缩降级为机械 trim，LLM 总结失败率高
   → 检查 CompactionDeps 是否正确配置

3. **⚠️ Sliding window: 30% no effect (ratio > 0.95)**  
   → 触发了滑动窗口但没压缩效果
   → 可能窗口设置过大，建议调小

## 数据库准备

### 连接 252 DB（通过 SSH 隧道）

```bash
# 1. 建立隧道
ssh -p 25022 root@115.29.212.252 -L 25432:172.16.2.210:5432 -N -f

# 2. 测试连接
psql -h 127.0.0.1 -p 25432 -U llm_gateway -d llm_gateway -c "SELECT COUNT(*) FROM request_logs;"

# 3. 运行 benchmark
export DATABASE_URL='postgres://llm_gateway:<password>@127.0.0.1:25432/llm_gateway?sslmode=disable'
go run ./cmd/compression-bench/ \
  -dsn="$DATABASE_URL"
```

### 连接 184 DB（Kubernetes）

```bash
# 1. 建立隧道
ssh -p 25022 root@14.103.112.184 -L 18432:10.43.118.61:5432 -N -f

# 2. 运行 benchmark
go run ./cmd/compression-bench/ \
  -dsn="postgres://llm_gateway:xxx@127.0.0.1:18432/llm_gateway?sslmode=disable"
```

## 测试用例建议

### 1. Baseline — 机械压缩能力
```bash
go run ./cmd/compression-bench/ \
  -test-mode=mechanical \
  -context-window=32000 \
  -max-samples=500
```
**验收**：只对实际触发的样本比较 trim 前后大小；样本覆盖率由历史数据决定。

### 2. Delta-append 测试
```bash
go run ./cmd/compression-bench/ \
  -share-session \
  -serial \
  -context-window=128000 \
  -max-samples=20
```
**验收**：
- 第 1 条：新会话，无压缩
- 第 2-N 条：delta_append 策略，lossiness=none
- 观察 MsgCount 累积变化

### 3. 滑动窗口触发测试
```bash
go run ./cmd/compression-bench/ \
  -share-session \
  -serial \
  -context-window=8000 \
  -max-samples=50
```
**验收**：
- 当累积消息超过阈值 → sliding_window_count 触发
- 或累积 token 超过 0.85 × 8000 → sliding_window_token 触发
- Strategy 应为 `sliding_window_*`

### 4. 生产环境完整测试
```bash
go run ./cmd/compression-bench/ \
  -test-mode=prepare \
  -days=7 \
  -max-samples=5000 \
  -context-window=128000 \
  -output=prod_bench.json
```
**验收**：
- 按 `request_logs.compression_strategy` 和 `compression_meta` 审计实际策略
- 对 whole lossiness 抽样验证摘要保留关键 ID、路径、数字和错误信息
- 以部署前的模型、窗口、协议和会话长度分布建立自己的基线

## 技术细节

### 为什么逐条并行回放无效？

```go
// 并行执行
for row := range workCh {
    go func(row requestLogRow) {
        res := sc.Prepare(ctx, row.OutboundBody, ...)
    }(row)
}
```

**问题**：
- `sc.Prepare` 内部读取 SessionCache
- 如果 row 1 和 row 2 共享 session_id，并行执行会导致：
  - row 2 读不到 row 1 写入的 state
  - row 2 被当作"新会话"
  - delta-append 失效

**解决方案**：
- benchmark 始终按时间顺序串行回放；仅可通过独立 benchmark 进程做吞吐测试
- HTTP 压测应采用“会话内串行、会话间并行”的 worker 模型

### SessionCache 的重要性

SessionCompressor 依赖 SessionCache 记录：
- `LastOutboundHash`：上次 outbound body 的 sha256
- `LastCompressedAt`：上次压缩时间（用于 IDLE 触发）
- `MsgCount`：消息数（用于 COUNT 触发）
- `SummaryMarker`：LLM 总结的边界标记

**无 cache 时的行为**：
- 每个请求都是"新会话"
- delta-append 退化为 full-send
- 滑动窗口不会触发（state=nil 时跳过）

**benchmark 的简化**：
- 默认启用进程内 L1 cache；不覆盖 L2 Redis 和 L3 PostgreSQL 故障恢复
- mechanical 模式完全绕过 cache，仅验证 trim 行为

## 已知限制

1. **252 DB 数据量太小**  
   - 只有 69 条记录，且大部分是短请求
   - 建议在 184 或生产环境测试

2. **无 Redis 环境**  
   - benchmark 工具内置 `Cache: nil`
   - 无法测试 L2 Redis 缓存效果

3. **无 LLM 调用**  
   - `CompactionDeps: nil`（跳过 LLM 总结）
   - 只能测试机械 trim 路径

4. **并发范围**
   - 单一会话必须顺序处理；会话间可以并行
   - `go test -race ./domains/hooks/compression` 用于验证真实 compressor 的并发会话回归测试

## 下一步改进

1. [ ] 支持 Redis SessionCache 注入
2. [ ] 支持 LLM 总结路径测试（需配置 provider）
3. [ ] 增加"多会话并行"模式（每会话内串行，会话间并行）
4. [ ] 增加 KV 缓存命中率模拟（统计前缀复用率）
5. [ ] 支持从 Parquet/CSV 导入测试数据

## 相关文档

- [domains/hooks/compression/session_compressor.go](../domains/hooks/compression/session_compressor.go) — SessionCompressor 主逻辑
- [domains/hooks/compression/diff.go](../domains/hooks/compression/diff.go) — Delta-append LCS 算法
- [domains/hooks/compression/window.go](../domains/hooks/compression/window.go) — 滑动窗口触发条件
- [domains/hooks/compression/session_cache.go](../domains/hooks/compression/session_cache.go) — 三层缓存实现
