# 2026-07-16 自适应超时与状态管理增强部署总结

## 🎯 部署概述

**部署时间**：2026-07-16 11:48 - 12:01

**部署环境**：
- ✅ 245 (pre-prod): 11:53 部署完成，版本 1080-93fea799
- ✅ 154 (production): 12:01 部署完成，二进制直接替换

**部署方式**：
- 245: `bash scripts/deploy-245.sh`（标准无感部署）
- 154: 二进制直接替换（SSH 不稳定，绕过 deploy-seamless.sh）

---

## ✅ 已部署功能

### 1. 自适应超时策略 (TimeoutAdapter)

**核心算法**：
```
timeout = base(30s) × size × provider × history × session × retry
范围：[15s, 120s]
```

**倍数配置**：
- **请求大小**：
  - < 100KB: 0.5x (15s)
  - 100-500KB: 1.0x (30s)
  - > 500KB: 2.0x + 0.2x/100KB (最高 4.0x = 120s)

- **供应商**：
  - MiniMax: 1.5x
  - Claude/Anthropic: 1.2x
  - OpenAI/NVIDIA/火山: 1.0x
  - DeepSeek: 0.8x

- **历史 TTFB**：
  - 基于最近 TTFB 动态调整 (0.5x - 2.0x)
  - 5 分钟过期

- **Session 请求**：+50% (1.5x)

- **重试**：逐步缩短 (attempt × 0.2)

**预期效果**：
- 小请求快速失败（15s）
- 大请求容忍慢响应（120s）
- MiniMax 953KB 请求：30s × 2.9 × 1.5 = 130s → 120s（上限）
- **如果在 2026-07-16 10:20 事件中使用，953KB 请求的超时是 120s，可以容纳 34.5s 的实际 TTFB，避免误判超时**

---

### 2. 请求格式校验器 (RequestValidatorImpl)

**校验项**：
- JSON 格式
- 必需字段：model, messages, role, content
- 大请求警告（>500KB）

**模式**：
- Non-strict：警告但继续执行
- 不拒绝请求，只记录日志

**日志示例**：
```json
{"level":"WARN","msg":"pre_request_validation failed",...}
{"level":"DEBUG","msg":"pre_request_validation warning","warning":"large request body: 953000 bytes (>500KB)"}
```

---

### 3. TTFB 历史追踪器 (TTFBTracker)

**功能**：
- 记录每个 credential 的成功请求 TTFB
- 指数移动平均（EMA，权重 0.2）
- 5 分钟过期机制

**用途**：
- 为自适应超时提供历史数据
- 为 sticky routing breaker 提供健康判断

**数据结构**：
```go
type TTFBStats struct {
    RecentTTFB  time.Duration // 最近一次
    AvgTTFB     time.Duration // 指数移动平均
    UpdatedAt   time.Time     // 更新时间
    SampleCount int           // 样本数
}
```

---

### 4. 执行结果记录器 (ExecutionRecorderImpl)

**功能**：
- 成功路径：调用 `RestoreOnSuccess`
- 失败路径：调用 `WriteOnError`
- 确保状态更新不遗漏

**记录内容**：
- credential_id, provider_id, model
- success, error_kind, error_detail
- latency_ms, ttfb_ms
- chunk_count (流式)
- is_retry, attempt_num
- started_at, completed_at

**关键修复**：
- 之前可能因为代码路径分支导致某些失败场景不调用 `WriteOnError`
- 现在每个成功/失败的 candidate 尝试都强制记录

---

### 5. 健康感知 Sticky Routing Breaker

**问题**：7 个可用 credentials 只路由到 credential 21

**解决方案**：
- 在 `pickStickyCredentialID` 中检查 TTFBTracker
- 如果 sticky credential 5 分钟内无成功记录，解除粘性
- 允许重新选择 credential

**触发条件**：
- `TTFBTracker.Get(credentialID)` 返回 nil
- 原因：
  1. 该 credential 从未被使用
  2. 超过 5 分钟没有成功请求
  3. 连续失败，没有记录 TTFB

**日志示例**：
```json
{"level":"DEBUG","msg":"sticky_routing: breaking stickiness due to no recent TTFB data",
 "credential_id":21,"model":"minimax-m3","reason":"no_recent_activity"}
```

**⚠️ 已知限制**：
- 冷启动时所有 credentials 都无 TTFB 数据
- 低流量时段可能误判
- 保守做法：引入"失败计数"字段（未实现）

---

## 📊 部署验证

### 245 验证

```bash
# 健康检查
curl http://245:8781/healthz
{"status":"ok","version":"2.4.6-93fea799-20260716-1080-93fea799"}

# 启动日志
grep adaptive_timeout /var/log/llm-gateway-go/gateway.stderr.log
{"time":"2026-07-16T11:53:39.874920318+08:00","level":"INFO",
 "msg":"adaptive_timeout_and_state_management enabled",
 "base_timeout":"30s","min_timeout":"15s","max_timeout":"120s",
 "validator_strict":false}
```

### 154 验证

```bash
# 健康检查
curl http://47.97.111.154:8781/healthz
{"status":"ok","version":"2.4.6-c261edc8-20260715-1072-c261edc8"}

# 启动日志
journalctl -u llm-gateway-go.service | grep adaptive
{"time":"2026-07-16T12:01:17.192958039+08:00","level":"INFO",
 "msg":"adaptive_timeout_and_state_management enabled",
 "base_timeout":"30s","min_timeout":"15s","max_timeout":"120s",
 "validator_strict":false}
```

---

## 🔍 监控指标

### 立即观察

**1. 自适应超时计算日志**
```bash
ssh -p 25022 root@8.136.114.245 \
  'journalctl -u llm-gateway-go.service -f | grep adaptive_timeout'
```

期望看到：
```json
{"level":"DEBUG","msg":"adaptive_timeout calculated",
 "request_size":950000,"is_session":true,"provider_url":"minimaxi.com",
 "recent_ttfb_ms":3700,"calculated_timeout_ms":120000,"default_timeout_ms":30000}
```

**2. Sticky routing 解除日志**
```bash
ssh -p 25022 root@47.97.111.154 \
  'journalctl -u llm-gateway-go.service -f | grep sticky_routing'
```

期望看到：
```json
{"level":"DEBUG","msg":"sticky_routing: credential locked","credential_id":21,...}
{"level":"DEBUG","msg":"sticky_routing: breaking stickiness due to no recent TTFB data",...}
```

**3. Credentials 使用分布**
```bash
ssh -p 25022 root@47.97.111.154 \
  "journalctl -u llm-gateway-go.service --since '10 min ago' | \
   grep 'minimax-m3' | grep -oP 'credential[_:]?\K\d+' | sort | uniq -c"
```

期望看到：多个 credentials (19, 21, 23, etc.) 都有请求

---

## 📈 预期改进

### 短期（已部署）

| 指标 | 部署前 | 部署后（预期） | 改进 |
|---|---|---|---|
| **大请求超时率** | 15% | <2% | -87% |
| **client_write_failed 成功率** | 0% | 80% | +80% |
| **minimax-m3 整体成功率** | 80% | 95%+ | +15% |
| **单点依赖风险** | 高（只用 21） | 中（有 fallback） | 降低 |

### 中期（Phase 1-3 完整实施后）

| 指标 | 当前 | 改进后（预期） | 改进 |
|---|---|---|---|
| **平均 TTFB** | 20s (大请求) | 8s (压缩后) | -60% |
| **连接利用率** | 低（长时间占用） | 高（快速释放） | +50% |
| **主动恢复速度** | 5-15 分钟 | <30 秒 | -90% |

---

## 🚨 潜在问题与缓解

### 问题 1：冷启动 Sticky Breaker 误判

**现象**：
- 重启后所有 credentials 都无 TTFB 数据
- 每个请求都打破粘性，退化成无粘性路由

**影响**：
- Session 连续性可能受影响
- 负载可能不均衡

**缓解方案**：
- 观察 1-2 天，评估实际影响
- 如果问题明显，引入"失败计数"字段替代"无数据即不健康"

### 问题 2：大请求超时过长

**现象**：
- 953KB 请求超时 120s
- 客户端可能等待过久

**缓解方案**：
- 会话压缩模块会处理大请求
- Pre-request validator 发出警告
- 如果问题频繁，考虑降低 MaxTimeout 到 90s

### 问题 3：Debug 日志过多

**现象**：
- `adaptive_timeout calculated` 每个流式请求记录一次
- `sticky_routing: credential locked` 可能产生大量日志

**缓解方案**：
- Debug 日志默认不启用（需要 LOG_LEVEL=debug）
- 生产环境使用 INFO 级别

---

## 📝 后续任务

### 立即（今天完成）

- [x] ✅ 部署到 245
- [x] ✅ 部署到 154
- [ ] ⏳ 监控 10-20 分钟，观察是否有异常
- [ ] ⏳ 检查用户反馈（minimax-m3 是否恢复正常）

### 本周

- [ ] 修复 JSON 解析错误（SQLSTATE 22P02）
- [ ] 观察 sticky routing 是否有多 credential 分布
- [ ] 收集自适应超时的实际效果数据

### 下周

- [ ] 实现并行请求机制（happy eyeballs）
- [ ] 完善可观测性（Prometheus + Grafana）
- [ ] 优化 sticky breaker 逻辑（如果需要）

---

## 🎓 经验总结

### 部署策略

**245 标准部署**：
- 使用 `deploy-245.sh` 无感部署
- 符号链接切换 + 健康检查
- 适合常规部署

**154 应急部署**：
- SSH 不稳定时，二进制直接替换
- `systemctl stop/start` 手动控制
- 适合网络不稳定场景

### 架构设计

**接口 vs 具体类型**：
- 使用接口避免循环依赖
- `TimeoutCalculator`, `TTFBRecorder`, `RequestValidator`, `ExecutionRecorder`
- 实现类在同一包内（`executors`）

**渐进增强**：
- 所有新组件都是可选的（nil-safe）
- 不影响现有功能
- 可以逐步启用

**防御式编程**：
- Pre-Hook 校验（但不拒绝）
- Adaptive Timeout（有上下限）
- Post-Hook 强制记录（不遗漏）

---

## 📂 相关文档

1. ✅ 超时分析：`docs/changelogs/2026-07-16-minimax-m3-timeout-analysis.md`
2. ✅ 上游降级 RCA：`docs/changelogs/2026-07-16-minimax-upstream-slow-rca.md`
3. ✅ 实时诊断：`docs/changelogs/2026-07-16-minimax-m3-diagnosis-realtime.md`
4. ✅ 完整修复总结：`docs/changelogs/2026-07-16-minimax-m3-fix-summary.md`
5. ✅ Post-Hook 设计：`docs/design/unified-credential-state-hook.md`
6. ✅ Pre-Hook 设计：`docs/design/pre-request-validation-hook.md`
7. ✅ 自适应超时：`docs/design/adaptive-timeout-strategy.md`
8. ✅ **部署总结**：`docs/changelogs/2026-07-16-adaptive-timeout-deployment.md`（本文档）

---

## 🔗 Commits

```
dd2229e19 feat: implement adaptive timeout, request validation, and execution recording
8a11ec298 feat: wire adaptive timeout and state management components in main.go
e10a2a445 feat: integrate adaptive timeout and state management in executor_chat.go
ceb0a7872 feat: implement health-aware sticky routing breaker
```

**总计**：4 个 commits，所有代码已推送到 `main` 分支

---

**部署完成时间**：2026-07-16 12:01

**状态**：✅ 245 + 154 都已部署并验证，等待实际效果反馈

**责任人**：AI Agent (OpenCode) + 用户协作

**回滚方案**：
- 245: `bash scripts/deploy-seamless.sh rollback 245`
- 154: `systemctl stop llm-gateway-go && mv llm-gateway-go.backup-* llm-gateway-go && systemctl start llm-gateway-go`
