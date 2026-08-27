# 🔄 会话压缩算法对比与优化任务交接

**交接时间：** 2026-08-28 凌晨  
**接手工程师：** 待分配  
**交接工程师：** ZCode Agent  
**任务状态：** 当前压缩实现已完成6轮审计并稳定；下一步需与OmniRoute对比并实现算法选择机制

---

## 📋 任务概述

### 核心目标

1. **对比分析**：将当前 llm-gateway-go 的会话压缩实现与 `/Users/xutaohuang/workspace/ai/OmniRoute` 中的压缩方案对比
2. **学习改进**：识别 OmniRoute 方案的优势，评估可移植性
3. **算法选择**：实现手动/自动选择压缩算法与模式的机制
4. **生产验证**：使用 245 log 中的大量真实请求数据进行压缩测试，对比信息密度与遗失率

### 背景说明

当前 llm-gateway-go 的会话压缩系统（v4 intelligent compression）已经过 6 轮审计（2026-08-27至08-28），修复了所有已知的协议语义缺陷：

- **Marker 嵌套** bug（P0）：旧摘要marker被误当FirstUser导致嵌套累积
- **工具回合原子性**：tail截断保证tool_calls与tool_call_id配对
- **Anthropic fallback过滤**：防御路径统一过滤marker/system
- **字段语义判断**：hasMeaningfulMessagePayload替代字段数量判断
- **文档一致性**：strip.go注释与实现对齐

现在需要评估是否还有更优的压缩策略可以从 OmniRoute 学习并集成。

---

## 🎯 当前状态（截至 e8c4654b5）

### 项目信息

- **仓库**：https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git
- **分支**：main
- **最新提交**：e8c4654b5 (Merge remote-tracking branch 'origin/main' into fix/redis-audit-followup-20260828)
- **工作区**：干净，无未提交更改

### 当前压缩实现架构

#### 核心文件位置

```
domains/hooks/compression/
├── session_compressor.go       # 压缩主入口，触发逻辑
├── compaction.go                # LLM摘要生成（7段格式）
├── strip.go                     # Phase 1: 工具回合剥离
├── diff.go                      # 增量diff与指纹计算
├── retain.go                    # A/B/C-track提取（system+firstUser+summary）
├── rebuilder_openai.go          # OpenAI重建器（带原子tail）
├── rebuilder_anthropic.go       # Anthropic重建器（带TrimAnthropicTail）
├── marker_idempotency_test.go   # marker幂等性回归测试
└── docs/audit/2026-08-27-session-compression-algorithm-audit.md  # 完整审计记录
```

#### 压缩策略概览

**触发条件**（可配置）：
- 消息数量 > `LLM_GATEWAY_COMPRESSION_MAX_MESSAGE_COUNT`（默认50）
- Token数 > `LLM_GATEWAY_COMPRESSION_MAX_TOKEN_COUNT`（默认128000）
- 空闲时长 > `LLM_GATEWAY_COMPRESSION_IDLE_MINUTES`（默认30）

**压缩流程**：
1. **Phase 1 - Strip**：删除已完成的工具回合，保留最近2轮
2. **Phase 2 - Thinking**：删除Anthropic thinking块
3. **LLM Summarization**：调用配置的压缩模型生成7段摘要
4. **Rebuild**：
   - **A-track**：保留原始system消息
   - **B-track**：保留第一条真实用户消息（跳过marker/reminder）
   - **C-track**：注入新摘要marker `[smm_v1:<hash>]`
   - **Tail**：保留最近N条消息（原子工具回合）

**关键特性**：
- **Marker幂等**：二次压缩时旧marker被替换，不嵌套
- **工具回合原子**：tail截断向前扩展到assistant tool_calls锚点
- **协议分离**：OpenAI/Anthropic各有独立rebuilder
- **增量diff**：只发送变化的消息，减少LLM summarization成本

#### 当前验证状态

✅ **测试覆盖**：
- `go test ./domains/hooks/compression/... -count=1` → 全绿
- `go test ./domains/hooks/... -count=1` → 全绿（20个包）
- 包含marker幂等、工具原子性、fallback过滤、扩展字段等回归

✅ **静态检查**：
- `go vet ./domains/hooks/compression/...` → 通过
- `go build ./domains/hooks/compression/...` → 通过
- `scan-secrets.sh --mode=strict` → 0 findings

✅ **审计文档**：
- 完整记录6轮审计发现与修复（§2至§10）
- 规格轴/规范轴双轴复核无矛盾
- 文档声称与实现一致

---

## 🔍 后续任务详细说明

### 任务1：OmniRoute 压缩方案分析

#### 1.1 定位与理解 OmniRoute 压缩实现

**目标路径**：`/Users/xutaohuang/workspace/ai/OmniRoute`

**需要回答的问题**：
1. OmniRoute 使用什么压缩策略？
   - 基于规则的截断？
   - LLM摘要？
   - 向量检索选择性保留？
   - 混合方案？

2. 触发条件与当前实现有何不同？
   - 阈值设置
   - 触发时机
   - 是否支持多种模式

3. 压缩后的信息密度如何衡量？
   - 是否有量化指标
   - 如何验证信息不丢失

4. 协议适配如何处理？
   - 是否支持OpenAI/Anthropic/其他协议
   - 工具调用如何保留

#### 1.2 对比分析矩阵

需要填写以下对比表格：

| 维度 | llm-gateway-go (当前) | OmniRoute | 优劣对比 | 可移植性 |
|------|----------------------|-----------|----------|---------|
| **压缩策略** | Phase1剥离+LLM摘要 | ? | | |
| **触发条件** | 消息数/token/空闲 | ? | | |
| **信息保留** | A/B/C三轨+tail | ? | | |
| **协议支持** | OpenAI/Anthropic | ? | | |
| **工具调用** | 原子回合保留 | ? | | |
| **性能开销** | LLM调用成本 | ? | | |
| **信息密度** | 未量化 | ? | | |
| **遗失率** | 未量化 | ? | | |

**输出文件**：`docs/research/omniroute-compression-comparison.md`

### 任务2：算法选择机制设计

#### 2.1 需求分析

**手动选择场景**：
- 管理员/租户配置偏好算法
- 不同会话类型使用不同策略（例如：工具密集型会话 vs 纯文本对话）

**自动选择场景**：
- 根据会话特征动态选择（消息类型分布、token密度、工具调用频率）
- 根据性能/成本权衡自动降级

#### 2.2 设计要点

1. **配置层**：
   ```go
   type CompressionStrategy string
   const (
       StrategyIntelligent  CompressionStrategy = "intelligent"  // 当前v4
       StrategyOmniRoute    CompressionStrategy = "omniroute"    // 待实现
       StrategyHybrid       CompressionStrategy = "hybrid"       // 混合
       StrategyDisabled     CompressionStrategy = "disabled"
   )
   ```

2. **选择器接口**：
   ```go
   type StrategySelector interface {
       SelectStrategy(ctx context.Context, session *Session) (CompressionStrategy, error)
   }
   ```

3. **配置优先级**：
   - 租户级配置 > 会话级hint > 自动选择 > 全局默认

**输出文件**：`docs/design/compression-strategy-selector.md`

### 任务3：245 log 数据验证

#### 3.1 数据准备

**数据源**：245服务器上的生产请求日志

**需要提取的字段**：
- 会话ID
- 原始消息数组
- 消息token统计
- 工具调用次数
- 会话时长

**采样策略**：
- 随机采样1000个真实会话
- 覆盖不同长度区间（10-50条、50-100条、100+条）
- 覆盖不同类型（纯文本、工具密集、混合）

#### 3.2 评估指标

**信息密度**（Information Density）：
```
density = 压缩后有效信息量 / 压缩后token数
```

**信息遗失率**（Information Loss Rate）：
```
loss_rate = (原始关键信息 - 压缩后可恢复信息) / 原始关键信息
```

**关键信息定义**：
- 用户明确的问题/指令
- 工具调用的输入输出
- 模型的关键结论
- 上下文依赖的引用

**压缩效率**：
```
compression_ratio = 原始token数 / 压缩后token数
```

#### 3.3 测试脚本

需要实现：
```bash
# 在当前仓库
scripts/compression-benchmark.sh \
  --data-source /path/to/245-logs \
  --sample-size 1000 \
  --strategies intelligent,omniroute,hybrid \
  --output docs/benchmark/compression-results.json
```

**输出格式**：
```json
{
  "timestamp": "2026-08-28T...",
  "sample_size": 1000,
  "results": {
    "intelligent": {
      "avg_density": 0.75,
      "avg_loss_rate": 0.05,
      "avg_compression_ratio": 3.2,
      "p50_latency_ms": 1200,
      "p99_latency_ms": 3000
    },
    "omniroute": { ... }
  }
}
```

---

## 🛠 实施路径建议

### 阶段1：调研与对比（1-2天）

1. 克隆并理解 OmniRoute 仓库
2. 定位其压缩相关代码
3. 填写对比分析矩阵
4. 输出 `docs/research/omniroute-compression-comparison.md`

### 阶段2：设计与原型（2-3天）

1. 设计算法选择机制（配置+接口）
2. 实现 `StrategySelector` 基础框架
3. 如果 OmniRoute 方案可移植，实现 `StrategyOmniRoute` 适配器
4. 输出 `docs/design/compression-strategy-selector.md`

### 阶段3：数据验证（3-5天）

1. 从245 log提取采样数据
2. 实现评估指标计算
3. 编写benchmark脚本
4. 运行对比测试
5. 输出 `docs/benchmark/compression-results.json` 和分析报告

### 阶段4：集成与部署（2-3天）

1. 集成最优方案到主分支
2. 添加配置文档
3. 编写迁移指南（如果需要改变现有配置）
4. 在154/245测试环境验证

---

## 📚 关键参考资料

### 当前仓库

1. **审计文档**（必读）：
   - `docs/audit/2026-08-27-session-compression-algorithm-audit.md`
   - 包含完整的架构说明、6轮审计发现与修复

2. **设计文档**：
   - `docs/omni-ref3` (A3 media pruning)
   - `docs/business/agent-platform-strategy-2026-Q2-detailed-specs.md`（§压缩策略）

3. **实现代码**：
   - `domains/hooks/compression/` 整个目录
   - 重点关注 `session_compressor.go` 的触发逻辑

### 外部资源

1. **OmniRoute仓库**：
   - `/Users/xutaohuang/workspace/ai/OmniRoute`
   - 需要自行定位压缩相关模块

2. **245生产日志**：
   - 联系运维获取访问权限
   - 注意脱敏处理

---

## ⚠️ 注意事项

### 1. 协议兼容性

- 当前实现对OpenAI/Anthropic有精细的协议适配
- 引入新算法时必须保证工具调用链不断裂
- 必须通过现有回归测试：`marker_idempotency_test.go`、`rebuilder_*_test.go`

### 2. 向后兼容

- 已有会话的压缩marker格式 `[smm_v1:<hash>]` 不能改变
- 新算法可以用新marker格式，但必须能识别旧格式

### 3. 性能考量

- 当前LLM摘要调用成本较高，OmniRoute方案可能有优势
- 但不能为了性能牺牲信息完整性
- 需要在benchmark中量化权衡

### 4. 测试数据脱敏

- 245 log 中可能包含敏感信息
- benchmark脚本必须内置脱敏逻辑
- 输出报告不能包含真实用户数据

---

## 🔗 相关链接

- **当前仓库**：https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git
- **审计文档**：[docs/audit/2026-08-27-session-compression-algorithm-audit.md](/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5/docs/audit/2026-08-27-session-compression-algorithm-audit.md)
- **OmniRoute路径**：`/Users/xutaohuang/workspace/ai/OmniRoute`
- **CONTRIBUTING规范**：[CONTRIBUTING.md](/Users/xutaohuang/workspace/ai-native-tools/llm-gateway/llm-gateway-go-5/CONTRIBUTING.md)

---

## 📞 联系与支持

如果遇到以下问题，可以参考或寻求帮助：

1. **压缩算法理解**：参考审计文档 §2、§5-§10
2. **测试环境访问**：联系运维团队
3. **OmniRoute代码理解**：可能需要联系OmniRoute项目维护者
4. **性能调优**：参考 `docs/performance/` 目录

---

**祝接手顺利！如有疑问，审计文档和代码注释已经相当详尽，应该能解答大部分问题。**
