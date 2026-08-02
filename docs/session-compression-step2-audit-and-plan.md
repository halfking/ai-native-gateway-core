# 会话压缩优化 - 源码审计报告与下一步执行计划

**报告日期**: 2026-08-02  
**审计人员**: AI Assistant  
**版本**: v1.0

---

## 📋 执行摘要

本报告基于会话压缩与三级缓存优化方案（v1.0），对现有源码进行深入审计，识别已完成功能、需要增强模块、需要新增模块，并制定安全的增量实施计划。

### 核心发现

✅ **已完成功能**（保护性开发）:
- SessionCache 三层架构（L1/L2/L3 存储层）
- SessionCompressor 协调器
- LCS 增量差分压缩
- LLM 无损摘要压缩 (v7)
- 滑动窗口触发器
- 机械裁剪策略
- 协议适配器 (OpenAI/Anthropic)
- RecoveryCoordinator 恢复协调

🆕 **需要新增**:
- 三级缓存语义重构（原始/压缩/安全处理）
- OmniRoute Lite 压缩流水线
- 可配置多维度总结模型系统
- 多维度总结（项目/关键词/任务/决策/问题/技术）

🔧 **需要增强**:
- 总结模块支持即时/事后模式
- 总结模型热更新
- 总结质量评估

---

## 🔍 详细源码审计

### 1. SessionCompressor (session_compressor.go)

**位置**: `domains/hooks/compression/session_compressor.go` (21068 行)

**已完成功能**:
```go
// ✅ Phase 0: Session ID 验证
if err := ValidateSessionID(gwSessionID); err != nil {
    return sc.fallbackResult(clientBody, res)
}

// ✅ Phase 1: 三层缓存查询 (L1/L2/L3)
// ✅ Phase 2: LCS 增量差分
// ✅ Phase 3: 滑动窗口触发
// ✅ Phase 4: LLM 无损摘要 (LossinessWhole)
// ✅ Phase 5: 机械裁剪兜底 (LossinessTail)
```

**保留价值**:
- `PrepareResult` 结构定义清晰
- `Lossiness` 分类完整 (None/Tail/Whole)
- `Dependencies` 依赖注入模式良好
- 失败开放 (fail-open) 机制健全

**需增强**:
- 当前使用单模型做 LLM 摘要 → 需要多模型可配置
- 缺少 Lite 预处理 → 需要集成

---

### 2. SessionCache (session_cache.go)

**位置**: `domains/hooks/compression/session_cache.go` (27828 行)

**当前架构**（存储层次）:
```
L1: 进程内存 LRU (最热数据)
L2: Redis (持久化)
L3: PostgreSQL (长期存储)
```

**已完成功能**:
- ✅ `SessionCacheBackend` 接口（Redis/PG 实现）
- ✅ LRU 内存缓存层
- ✅ Redis 持久化层
- ✅ PG 长期存储层
- ✅ 三层一致性保证

**新方案架构**（处理阶段，需增量实现）:
```
原始会话缓存 (RawSessionCache)     → 客户端原始消息
   ↓
压缩会话缓存 (CompressedSessionCache) → 压缩后消息
   ↓  
安全处理缓存 (SafeSessionCache)      → 发送给 LLM 的最终消息
```

**关键设计**: 三级缓存共享同一存储层（复用现有 L1/L2/L3），但在逻辑上分为三个独立模块。

---

### 3. 压缩策略模块

#### 3.1 Compaction (compaction.go)

**位置**: `domains/hooks/compression/compaction.go` (29909 行)

**已完成**:
- ✅ `tryLLMContextCompaction` - LLM 摘要压缩（v7）
- ✅ `compactionSystemPrompt` - 增强系统提示词
- ✅ Lossiness 分类管理
- ✅ Memora + Provider 客户端集成

**可扩展点**:
- 需要支持多模型选择（项目/关键词/任务 → 不同模型）
- 需要支持异步批量总结

#### 3.2 Diff (diff.go)

**位置**: `domains/hooks/compression/diff.go` (11357 行)

**已完成**:
- ✅ `BuildOutboundMessages` - LCS 增量差分
- ✅ 消息级别 delta-append
- ✅ 哈希指纹计算

#### 3.3 Window (window.go)

**位置**: `domains/hooks/compression/window.go` (7362 行)

**已完成**:
- ✅ `ShouldTriggerWindow` - 滑动窗口触发器
- ✅ Token 预算管理

#### 3.4 其他策略模块

| 模块 | 行数 | 功能 |
|------|------|------|
| `smart_window.go` | 15894 | 智能窗口计算 |
| `strip.go` | 9715 | 机械裁剪 |
| `task_analyzer.go` | 7376 | 任务类型分析 |
| `recovery_coordinator.go` | 11736 | 恢复协调 |

**所有现有策略可复用**，新方案在此基础上增强。

---

### 4. 会话总结模块现状

**当前实现** (`compaction.go`):
- ✅ LLM 摘要生成（单维度：压缩上下文）
- ✅ 系统提示词增强
- ✅ 保留精确值（IDs, paths, error messages）

**缺失功能**:
- ❌ 即时总结 vs 事后总结模式分离
- ❌ 多维度总结（项目/关键词/任务/决策/问题/技术）
- ❌ 可配置总结模型
- ❌ 总结质量评估

**集成路径**:
- 在 `compression` 包下新建 `summary` 子包
- 复用 `compaction.go` 的 LLM 客户端基础设施
- 通过配置驱动多维度总结器

---

## 📐 增量实施方案

### 阶段 1: 可配置总结模型系统

**目标**: 支持在系统配置中设定总结模型，支持多维度、多模型、热更新。

**实现位置**:
1. `config/summary_config.go` - 配置结构定义
2. `domains/hooks/compression/summary/model_config.go` - 模型加载器
3. `domains/hooks/compression/summary/summarizer.go` - 多维度总结器

**配置示例** (settings_kv 表):
```
key:                                  value:
summary_models.project_context        gpt-4o-mini
summary_models.keywords               gpt-3.5-turbo
summary_models.tasks                  gpt-4o-mini
summary_models.decisions              gpt-4o
summary_models.problems               gpt-4o-mini
summary_models.technical_details      gpt-4o
summary_models.fallback               gpt-3.5-turbo
summary_mode                          hybrid  (realtime/deferred/hybrid)
summary_realtime_trigger_turns        10
summary_realtime_trigger_tokens       8000
```

**热更新机制**:
- 利用现有 `system_settings` 配置管理
- 通过 `hotconfig` 模块监听配置变更
- 模型变更时自动重新加载

---

### 阶段 2: 三级缓存语义重构

**目标**: 在现有 L1/L2/L3 存储层之上，引入处理阶段的逻辑分离。

**实现策略**: 不破坏现有存储，添加逻辑包装层。

```go
// 新增结构 (不破坏现有)
type RawSessionCache struct {
    storage *SessionCache  // 复用现有存储
}

type CompressedSessionCache struct {
    storage *SessionCache
}

type SafeSessionCache struct {
    storage *SessionCache
}
```

**轮次一致性保证**:
- 所有缓存层共享 `turn_id` 序列
- 压缩块记录 `[start_turn, end_turn]` 区间
- 安全缓存记录 `original_hash` 指向压缩块

---

### 阶段 3: 多维度总结器

**6 个维度总结器**:

| 维度 | 推荐模型 | 用途 |
|------|----------|------|
| 项目上下文 | gpt-4o-mini | 项目名称、技术栈、目标 |
| 关键词提取 | gpt-3.5-turbo | 降低成本 |
| 任务追踪 | gpt-4o-mini | 已完成/进行中/计划 |
| 决策记录 | gpt-4o | 重要决策高保真 |
| 问题与解决方案 | gpt-4o-mini | 知识库 |
| 技术细节 | gpt-4o | 代码/API/命令 |

**双模式支持**:
- **即时模式**: 每 N 轮触发，< 2s 延迟
- **事后模式**: 会话关闭后异步处理，< 10s
- **混合模式**: 即时压缩 + 事后归档

---

## 🛡️ 兼容性保护策略

### 1. 不破坏现有 API

- `SessionCompressor.Prepare()` 接口保持不变
- 新功能通过可选参数添加
- 现有调用方零改动

### 2. 向后兼容的数据格式

- 新增字段使用 `omitempty` JSON tag
- 旧数据可平滑读取
- 新数据自动填充新字段

### 3. 渐进式部署

- 通过 feature flag 控制新功能
- 默认关闭，逐步灰度
- 异常时自动降级到现有实现

---

## 📊 实施进度

| 阶段 | 任务 | 状态 |
|------|------|------|
| 1 | 源码审计 | ✅ 完成 |
| 2 | 设计文档 | ✅ 完成 |
| 3 | 可配置模型系统 | 📋 待实现 |
| 4 | 三级缓存语义 | 📋 待实现 |
| 5 | 多维度总结器 | 📋 待实现 |
| 6 | Lite 压缩集成 | 📋 待实现 |
| 7 | 单元测试 | 📋 待实现 |
| 8 | 集成测试 | 📋 待实现 |
| 9 | 灰度发布 | 📋 待实现 |

---

## 📝 下一步执行计划

### Week 1: 可配置总结模型
- Day 1-2: 配置结构定义 + 加载器实现
- Day 3-4: 模型选择器实现
- Day 5-7: 单元测试

### Week 2: 三级缓存语义重构
- Day 8-10: RawSessionCache/CompressedSessionCache/SafeSessionCache 实现
- Day 11-12: 轮次一致性保证
- Day 13-14: 单元测试

### Week 3: 多维度总结器
- Day 15-17: 6 个维度总结器实现
- Day 18-19: 即时/事后双模式
- Day 20-21: 单元测试

### Week 4: Lite 压缩 + 集成
- Day 22-24: OmniRoute Lite 流水线
- Day 25-26: 集成测试
- Day 27-28: 性能基准测试

---

## ⚠️ 风险评估

| 风险 | 影响 | 概率 | 缓解措施 |
|------|------|------|----------|
| 破坏现有 API | 高 | 低 | 接口保持不变 + 全量回归测试 |
| 数据格式不兼容 | 中 | 低 | omitempty + 渐进式迁移 |
| 性能下降 | 高 | 低 | 性能基准测试 + 灰度发布 |
| 模型成本增加 | 中 | 中 | 维度级模型选择 + 降级策略 |
| 配置热更新失败 | 低 | 低 | 原子加载 + 回滚机制 |

---

## ✅ 验收标准

- [ ] 现有 `SessionCompressor.Prepare()` 接口零改动
- [ ] 现有单元测试 100% 通过
- [ ] 新增功能测试覆盖率 > 85%
- [ ] 性能基准测试无回归
- [ ] 可配置模型系统支持热更新
- [ ] 三级缓存轮次一致性 100% 保证
- [ ] 多维度总结延迟满足 SLA

---

**报告生成时间**: 2026-08-02  
**下一步行动**: 进入 Week 1 - 可配置总结模型系统实现
