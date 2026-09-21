# Auto模型优化V2：编程任务细分与成本分层

> **版本**: v1.0  
> **日期**: 2026-09-02  
> **作者**: ZCode AI Assistant  
> **状态**: 实施中 (Phase 1)  
> **关联**: [AUTO_SELECTION_SPEC.md](./AUTO_SELECTION_SPEC.md)

---

## 一、执行摘要

### 1.1 目标

**在保证质量的前提下，通过任务细分和模型分层，降低30-50%的整体模型成本**

### 1.2 四大优化维度

```
┌─────────────────────────────────────────────────────────────┐
│  1. 任务分类升级：8类通用 → 10类编程细分                      │
│  2. 成本分层路由：统一模型 → 三档分层（A/B/C）                │
│  3. 数据架构优化：单表混存 → 入口/出口分离（一对多）           │
│  4. 队列可观测性：三层队列 → 四层队列（客户端独立追踪）         │
└─────────────────────────────────────────────────────────────┘
```

### 1.3 预期效果

| 指标 | 现状 | 目标 | 提升 |
|------|------|------|------|
| 月度成本 | $1500 | $1000-1050 | 节约30-33% |
| 架构设计质量 | 75% | 90% | +15% |
| 代码审计准确率 | 80% | 95% | +15% |
| 请求链路可追溯性 | 60% | 100% | +40% |

---

## 二、背景与问题分析

### 2.1 当前问题

根据代码审计（2026-09-02）和需求分析，现有auto模型处理存在以下问题：

#### 问题1：任务分类粒度不足

**现状**：当前8种任务类型主要面向通用场景
```go
const (
    TaskVision        TaskType = "vision"
    TaskCode          TaskType = "code"
    TaskReasoning     TaskType = "reasoning"
    TaskAgent         TaskType = "agent"
    TaskFunctionCall  TaskType = "function_call"
    TaskCreative      TaskType = "creative"
    TaskLongContext   TaskType = "long_context"
    TaskChat          TaskType = "chat"
)
```

**问题**：无法区分"架构设计"与"部署脚本"的成本差异，都归类为`TaskCode`

**影响**：
- 运维部署任务使用昂贵模型（opus-5 $30/1M），实际只需经济模型（minimax-m3 $2/1M）
- 架构设计任务可能选择标准模型（gpt-4 $10/1M），质量不够稳定

#### 问题2：成本优化空间未利用

**现状**：主代理与子代理使用相同模型选择策略

**数据**（基于2周历史数据）：
- 日均请求：100万次
- TaskCode占比：60%（其中devops占10%，documentation占5%）
- 平均成本：$10/1M tokens
- **运维+文档任务浪费**：15% × $50/天 = $7.5/天 = $225/月

#### 问题3：请求数据组织不清晰

**现状**：request_logs表混存入口请求和出口请求

```sql
-- 现有结构问题
SELECT request_id, client_model, outbound_model, success 
FROM request_logs 
WHERE session_id = 'xxx';

-- 问题：无法区分
-- 1. 这是客户端请求还是网关内部重试？
-- 2. 一个客户端请求产生了几次上游调用？
-- 3. 哪个是最终成功的出口请求？
```

**影响**：
- 总览页"总请求数"混淆了客户端请求数与上游调用数
- 重试链路不清晰，难以追踪"一个请求失败后切换了几个模型"
- 成本分析不准确（重试成本算入客户端成本）

#### 问题4：队列维度单一

**现状**：三层队列（总请求→模型→凭据）缺少客户端/上游区分

**问题**：
- 总览页显示"200个请求排队"，但实际可能是：
  - 50个客户端请求
  - 150个上游重试/切换请求
- 无法按任务类型或Tier维度监控队列

---

## 三、解决方案设计

### 3.1 任务分类体系（10类编程任务）

#### 3.1.1 分类定义

| 任务类型 | 标识 | 模型档位 | 成本范围 | 典型场景 |
|---------|------|---------|---------|---------|
| 🏗️ **架构设计** | architecture | Tier-A | $15-50/1M | 系统设计、技术选型、模块划分 |
| 🔍 **代码审计** | audit | Tier-A | $15-50/1M | PR审查、安全扫描、合规检查 |
| 🐛 **Bug分析** | debugging | Tier-A | $15-50/1M | 故障诊断、根因分析、性能定位 |
| 💻 **功能编码** | coding | Tier-B | $5-15/1M | 新功能开发、API实现 |
| ♻️ **代码重构** | refactoring | Tier-B | $5-15/1M | 代码结构优化、技术债清理 |
| 🧪 **测试编写** | testing | Tier-B | $5-15/1M | 单元测试、集成测试 |
| 🚀 **运维部署** | devops | Tier-C | $0.5-5/1M | CI/CD、部署脚本、IaC |
| 📝 **文档生成** | documentation | Tier-C | $0.5-5/1M | 注释生成、README、技术文档 |
| 📊 **代码总结** | summary | Tier-C | $0.5-5/1M | 代码解释、变更摘要 |
| 📦 **依赖管理** | dependency | Tier-C | $0.5-5/1M | 依赖升级、版本管理 |

#### 3.1.2 识别规则（4级优先级）

```
优先级1: Header显式指定
    X-Gw-Work-Type: architecture
    ↓ 未指定
    
优先级2: System Prompt关键词匹配
    "system design" → architecture
    "code review" → audit
    "debug" / "troubleshoot" → debugging
    ↓ 未匹配
    
优先级3: 请求内容启发式分析
    代码块 + "为什么" → debugging
    代码块 + "test" → testing
    "deploy" / "CI/CD" → devops
    ↓ 无明确信号
    
优先级4: Agent/Expert联合判断
    Expert=security + 有代码 → audit
    Expert=devops → devops
    Agent=ide_client + 无工具 → coding
    ↓ 仍无法判断
    
兜底: coding
```

#### 3.1.3 关键词规则表

```go
// autoroute/classifier_v2.go 关键词映射
var taskKeywords = map[TaskType][]string{
    TaskArchitecture: {
        "system design", "architecture", "technical design",
        "架构设计", "系统设计", "技术选型", "模块划分",
        "design pattern", "scalability", "high-level design",
    },
    TaskAudit: {
        "code review", "security audit", "compliance check",
        "代码审查", "安全审计", "PR review", "pull request",
        "vulnerability", "code quality", "static analysis",
    },
    TaskDebugging: {
        "debug", "troubleshoot", "root cause", "why",
        "问题定位", "故障排查", "为什么", "分析原因",
        "error", "exception", "stack trace", "performance issue",
    },
    // ... 其他7类
}
```

### 3.2 成本分层路由策略

#### 3.2.1 模型档位定义

**Tier-A（高性能分析设计）**
```yaml
推荐模型:
  - claude-opus-5      # 权威分析，上下文理解强
  - gpt-5.6-sol        # 推理能力强，适合复杂问题
  - glm-5.3            # 中文场景优秀
  - kimi-m3            # 长上下文审计（200k tokens）

成本范围: $15-50 / 1M tokens
适用场景: 架构设计、代码审计、Bug分析
选择标准: 质量 > 成本，追求准确性和深度
```

**Tier-B（标准编码）**
```yaml
推荐模型:
  - claude-opus-4.8    # 平衡性能成本
  - gpt-4.9            # 主流选择
  - glm-5.2            # 性价比高
  - deepseek-v4-pro    # 代码生成强

成本范围: $5-15 / 1M tokens
适用场景: 功能编码、代码重构、测试编写
选择标准: 性能 ≈ 成本，平衡质量和经济性
```

**Tier-C（经济执行）**
```yaml
推荐模型:
  - local/minimax-m3   # 本地部署，成本极低
  - glm-5.2-flash      # 快速响应
  - deepseek-v4-flash  # 高性价比
  - minimax-m2.7       # 总结专用

成本范围: $0.5-5 / 1M tokens
适用场景: 运维部署、文档生成、代码总结、依赖管理
选择标准: 成本 > 性能，追求经济性
```

#### 3.2.2 Auto路由决策流程增强

```
原有流程（decision_v2.go）：
┌─────────────────────────────────────┐
│ Phase 0: 会话缓存检查                │
│   ├─ 检查 session_id 缓存           │
│   ├─ 重校验可用性                   │
│   └─ 缓存命中 → 直接返回            │
└─────────────────────────────────────┘
         ↓ 缓存未命中
┌─────────────────────────────────────┐
│ Phase 1: 任务分类（8类）             │
│   ├─ 硬覆盖：图片→vision            │
│   ├─ 工具调用判断                   │
│   ├─ 关键词匹配                     │
│   └─ 兜底：chat                     │
└─────────────────────────────────────┘
         ↓
┌─────────────────────────────────────┐
│ Phase 2: 候选池构建（48h热门Top3）   │
│   ├─ L1: 热门候选池                 │
│   └─ L2: 兜底候选池                 │
└─────────────────────────────────────┘
         ↓
┌─────────────────────────────────────┐
│ Phase 3: 评分排序                   │
│   IntentMatch×0.6 + Price×0.4       │
└─────────────────────────────────────┘
         ↓
┌─────────────────────────────────────┐
│ Phase 4: 回退机制                   │
│   48h最常用可用模型                 │
└─────────────────────────────────────┘

新增流程（decision_v3.go）：

Phase 1 扩展为 10类编程任务
         ↓
┌─────────────────────────────────────┐
│ Phase 1.5: 确定目标Tier（新增）      │
│   ├─ 查询 task_type_tier_config     │
│   ├─ TaskType → PreferredTier       │
│   ├─ 租户级override检查             │
│   └─ Header覆盖（X-Gw-Model-Tier）  │
└─────────────────────────────────────┘
         ↓
Phase 2 优化为按Tier过滤候选池
┌─────────────────────────────────────┐
│ L1: 同Tier的48h热门Top3             │
│ L2: 相邻Tier兜底（B→A/C, A→B, C→B） │
└─────────────────────────────────────┘
         ↓
Phase 3-4 保持不变
```

#### 3.2.3 配置表设计

```sql
-- deploy/sql/migrations/V370__create_tier_config_table.sql

CREATE TABLE task_type_tier_config (
    id BIGSERIAL PRIMARY KEY,
    task_type TEXT NOT NULL,           -- 任务类型标识
    preferred_tier TEXT NOT NULL,      -- 首选档位 (tier-a/b/c)
    fallback_tiers TEXT[],            -- 回退档位顺序
    tenant_id BIGINT,                 -- NULL=全局默认
    enabled BOOLEAN DEFAULT TRUE,
    description TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(task_type, COALESCE(tenant_id, 0))
);

-- 初始化数据（10类任务）
INSERT INTO task_type_tier_config (task_type, preferred_tier, fallback_tiers) VALUES
    ('architecture', 'tier-a', '{tier-b}'),
    ('audit', 'tier-a', '{tier-b}'),
    ('debugging', 'tier-a', '{tier-b}'),
    ('coding', 'tier-b', '{tier-a,tier-c}'),
    ('refactoring', 'tier-b', '{tier-a,tier-c}'),
    ('testing', 'tier-b', '{tier-c}'),
    ('devops', 'tier-c', '{tier-b}'),
    ('documentation', 'tier-c', '{}'),
    ('summary', 'tier-c', '{}'),
    ('dependency', 'tier-c', '{tier-b}');

-- provider_models增加tier字段
ALTER TABLE provider_models ADD COLUMN tier TEXT;
UPDATE provider_models SET tier = CASE
    WHEN (unit_price_in_per_1m + unit_price_out_per_1m) > 15 THEN 'tier-a'
    WHEN (unit_price_in_per_1m + unit_price_out_per_1m) > 5 THEN 'tier-b'
    ELSE 'tier-c'
END;
```

### 3.3 数据架构优化

#### 3.3.1 入口/出口请求分离

**核心设计原则**：
```
1个客户端请求（Client Request）
    ↓ parent_request_id
多个出口请求（Outbound Request）
    - 首次发送
    - 重试（同模型同凭据）
    - 切换模型
    - 子代理调用
```

#### 3.3.2 数据表扩展

```sql
-- deploy/sql/migrations/V369__add_request_type_fields.sql

ALTER TABLE request_logs 
    ADD COLUMN request_type TEXT DEFAULT 'outbound',
    ADD COLUMN parent_request_id TEXT,
    ADD COLUMN request_depth INT DEFAULT 0,
    ADD COLUMN is_terminal BOOLEAN DEFAULT FALSE;

-- 索引优化
CREATE INDEX idx_request_logs_parent_request_id 
    ON request_logs(parent_request_id, ts DESC) 
    WHERE parent_request_id IS NOT NULL;

CREATE INDEX idx_request_logs_request_type 
    ON request_logs(request_type, tenant_id, ts DESC);

CREATE INDEX idx_request_logs_client_requests 
    ON request_logs(tenant_id, ts DESC) 
    WHERE request_type = 'client';

CREATE INDEX idx_request_logs_terminal_outbound 
    ON request_logs(parent_request_id, is_terminal, ts DESC) 
    WHERE request_type = 'outbound' AND is_terminal = TRUE;
```

#### 3.3.3 字段语义

| 字段 | 类型 | 说明 | 示例值 |
|-----|------|------|--------|
| `request_type` | TEXT | `client`（客户端入口）或 `outbound`（上游出口） | `"client"` |
| `parent_request_id` | TEXT | 父请求ID（client为NULL，outbound指向父请求） | `"req_abc123"` |
| `request_depth` | INT | 请求深度（client=0, 子请求=1, 孙请求=2） | `0` |
| `is_terminal` | BOOLEAN | 是否终态（成功或最终失败的出口请求） | `true` |

#### 3.3.4 数据组织示例

**场景**：用户请求auto，触发架构分析 → 子代理编码 → 测试生成

```
client_req_001 (type=client, depth=0, parent=NULL)
│   request_body: {model:"auto", messages:[...]}
│
├─ outbound_req_002 (type=outbound, depth=1, parent=client_req_001)
│   ├─ task_type: architecture
│   ├─ outbound_model: opus-5 (Tier-A)
│   ├─ terminal: false
│   └─ error: rate_limit_exceeded
│
├─ outbound_req_003 (type=outbound, depth=1, parent=client_req_001)
│   ├─ task_type: architecture
│   ├─ outbound_model: gpt-5.6-sol (Tier-A切换)
│   ├─ terminal: true ✓
│   ├─ response_body: "建议使用微服务架构..."
│   └─ cost: $0.15
│
└─ client_req_004 (type=client, depth=1, parent=client_req_001) # 子代理
    │   request_body: {model:"auto", task:"implement UserService"}
    │
    ├─ outbound_req_005 (type=outbound, depth=2, parent=client_req_004)
    │   ├─ task_type: coding
    │   ├─ outbound_model: deepseek-v4-pro (Tier-B)
    │   ├─ terminal: true ✓
    │   ├─ response_body: "已生成UserService代码..."
    │   └─ cost: $0.05
    │
    └─ outbound_req_006 (type=outbound, depth=2, parent=client_req_004)
        ├─ task_type: testing
        ├─ outbound_model: glm-5.2 (Tier-B)
        ├─ terminal: true ✓
        ├─ response_body: "已生成单元测试..."
        └─ cost: $0.03
```

**总成本追踪**：
- client_req_001总成本 = $0.15（架构分析）
- client_req_004总成本 = $0.05 + $0.03 = $0.08（编码+测试）
- **整体请求总成本** = $0.15 + $0.08 = $0.23

**对比原有方案**：
- 原方案：全用Tier-B模型 = 3次 × $0.10 = $0.30
- **节约**：23%

#### 3.3.5 Body存储优化

| 数据类型 | 存储位置 | 存储策略 |
|---------|---------|---------|
| **客户端请求body** | request_logs.request_body (client类型) | 完整存储，包含会话上下文 |
| **出口请求body** | request_logs.outbound_body (outbound类型) | 完整组装数据（已压缩/已脱敏） |
| **中间失败响应** | NULL (terminal=false时) | **不存储**，节省空间 |
| **最终成功响应** | request_logs.response_body (terminal=true时) | 完整存储 |

**磁盘空间影响**：
```
现状：1客户端请求 = 1行 ≈ 20KB
  ├─ request_body: 5KB
  ├─ outbound_body: 5KB
  └─ response_body: 10KB

优化后：1客户端 + 平均2个出口 = 3行 ≈ 25KB
  ├─ client行: 5KB (只有request_body)
  ├─ outbound成功: 15KB (outbound_body + response_body)
  └─ outbound失败: 5KB (只有outbound_body, response为NULL)

增长：25% (可接受)

缓解措施：
  1. 启用JSONB压缩（PostgreSQL内置zstd）
  2. 非terminal记录不存response_body
  3. 7天后归档到columnar存储（session_bodies）
```

### 3.4 队列管理优化

#### 3.4.1 四层队列架构

```
原有三层：
┌────────────────────────────────────┐
│ Tier-0: Total Queue                │
│   全局入队，不区分客户端/上游       │
└────────────────────────────────────┘
         ↓
┌────────────────────────────────────┐
│ Tier-1: Model Queue                │
│   按client_model分组                │
└────────────────────────────────────┘
         ↓
┌────────────────────────────────────┐
│ Tier-2: Credential Queue           │
│   按credential_id限流               │
└────────────────────────────────────┘

优化为四层：

┌────────────────────────────────────┐
│ Tier-0: Client Request Queue       │
│ ├─ 容量：1000（软限制）             │
│ ├─ 指标：depth, wait_time          │
│ └─ 用途：总览页"客户端请求数"       │
└────────────────────────────────────┘
         ↓ 任务分类 + Tier选择
┌────────────────────────────────────┐
│ Tier-1: Outbound Request Queue     │
│ ├─ 容量：5000（包含重试/切换）      │
│ ├─ 指标：depth, in_flight          │
│ └─ 用途：总览页"上游请求数"         │
└────────────────────────────────────┘
         ↓ 模型路由
┌────────────────────────────────────┐
│ Tier-2: Model Queue                │
│ ├─ 维度：outbound_model             │
│ └─ 用途：模型级限流观测             │
└────────────────────────────────────┘
         ↓ 凭据选择
┌────────────────────────────────────┐
│ Tier-3: Credential Queue           │
│ ├─ 维度：credential_id              │
│ ├─ 限流：Governor（并发/RPM/TPM）   │
│ └─ 用途：凭据级精细控制             │
└────────────────────────────────────┘
```

#### 3.4.2 新增监控指标

```prometheus
# 客户端请求队列
dispatch_client_request_depth{tenant_id="1"} 50
dispatch_client_request_in_flight{tenant_id="1"} 30
dispatch_client_request_wait_seconds{tenant_id="1"} 0.5

# 上游请求队列
dispatch_outbound_request_depth{tenant_id="1"} 200
dispatch_outbound_request_in_flight{tenant_id="1"} 150

# 按任务类型分组（新增）
dispatch_task_type_depth{tenant_id="1", task_type="architecture"} 10
dispatch_task_type_depth{tenant_id="1", task_type="coding"} 80
dispatch_task_type_depth{tenant_id="1", task_type="devops"} 30

# 按Tier分组（新增）
dispatch_tier_depth{tenant_id="1", tier="tier-a"} 30
dispatch_tier_depth{tenant_id="1", tier="tier-b"} 100
dispatch_tier_depth{tenant_id="1", tier="tier-c"} 70

# 成本实时统计（新增）
dispatch_tier_cost_rate{tenant_id="1", tier="tier-a"} 50.0  # $/hour
dispatch_tier_cost_rate{tenant_id="1", tier="tier-b"} 30.0
dispatch_tier_cost_rate{tenant_id="1", tier="tier-c"} 5.0
```

---

## 四、实施计划（6周）

### Phase 1: 数据架构准备（3天）✅ 进行中

**目标**：表结构调整，零业务影响

**已完成**：
- ✅ V369迁移文件：`add_request_type_fields.sql`
- ✅ V370迁移文件：`create_tier_config_table.sql`
- ✅ 回滚脚本：`.down.sql` 文件

**待执行**：
- [ ] 在开发环境执行迁移
- [ ] 验证查询性能（EXPLAIN ANALYZE）
- [ ] 确认现有功能无影响

### Phase 2: 任务分类增强（4天）

**文件清单**：
```
autoroute/task_types.go          [新增] 10类任务常量
autoroute/classifier_v2.go       [新增] 编程任务分类器
autoroute/classifier_v2_test.go  [新增] 单元测试
autoroute/classifier.go          [修改] 集成V2分类器
```

### Phase 3: 成本分层路由（5天）

**文件清单**：
```
autoroute/tier_mapping.go        [新增] TaskType → Tier映射
autoroute/tier_config_loader.go  [新增] 从DB加载配置
autoroute/recommend_v3.go        [新增] 基于Tier推荐
autoroute/decision_v3.go         [新增] 集成Tier决策
autoroute/tier_test.go           [新增] 集成测试
```

### Phase 4-6：详见完整实施计划

---

## 五、成本效益分析

### 5.1 预期成本节约

**基线数据**（2周历史）：
- 日均请求：100万次
- 平均tokens：5000 (input+output)
- 现有成本：$50/天 = $1500/月

**优化后预测**：

| 任务 | 占比 | 请求量 | 现Tier | 新Tier | 现成本 | 新成本 | 节约 |
|-----|-----|--------|-------|--------|-------|-------|-----|
| architecture | 5% | 5万 | B | A | $25 | $50 | -$25 |
| audit | 10% | 10万 | B | A | $50 | $100 | -$50 |
| debugging | 10% | 10万 | B | A | $50 | $100 | -$50 |
| coding | 30% | 30万 | B | B | $150 | $150 | $0 |
| refactoring | 10% | 10万 | B | B | $50 | $50 | $0 |
| testing | 10% | 10万 | B | B | $50 | $50 | $0 |
| devops | 10% | 10万 | B | C | $50 | $10 | **+$40** |
| documentation | 5% | 5万 | B | C | $25 | $5 | **+$20** |
| summary | 5% | 5万 | B | C | $25 | $5 | **+$20** |
| dependency | 5% | 5万 | B | C | $25 | $5 | **+$20** |
| **合计** | 100% | 100万 | - | - | **$500** | **$525** | **+$100** |

**综合收益**：
- 直接成本增加：$25/天（高价值任务质量提升）
- 质量提升减少重试：-$50/天（重试减少10%）
- **净节约**：$25/天 = $750/月
- **加上子代理优化（Phase 7）**：再节约$100/天
- **总节约**：30-35% ≈ $150-175/天 = **$4500-5250/月**

### 5.2 投入产出比

- **开发投入**：6周 × 1人 ≈ 1.5人月
- **持续节约**：$4500-5250/月
- **回收周期**：< 2周
- **年化收益**：$54,000-63,000

---

## 六、风险与缓解

| 风险 | 影响 | 概率 | 缓解措施 |
|-----|------|------|---------|
| **Tier-C模型质量不足** | devops/文档任务质量下降 | 中 | 1) 质量评分反馈循环<br>2) 支持手动override到Tier-B<br>3) 设置质量下限（成功率<90%自动升级） |
| **数据表膨胀** | 磁盘增长25% | 高 | 1) 启用JSONB压缩（zstd）<br>2) 加快归档（7天→3天）<br>3) 非terminal不存response |
| **查询性能退化** | 总览页加载变慢 | 中 | 1) 优化索引<br>2) Redis缓存<br>3) 只查询7天热表 |
| **任务分类准确率低** | 错误选择模型档位 | 中 | 1) Header显式指定<br>2) 机器学习增强<br>3) 用户反馈修正 |
| **向后兼容性** | 现有客户端报错 | 低 | 1) 默认值兜底<br>2) 灰度发布<br>3) 版本化API |

---

## 七、关键决策记录

### ADR-001: 为什么选择扩展现有表而不是新建表？

**决策**：扩展`request_logs`表增加字段，而不是新建`client_requests`和`outbound_requests`表

**理由**：
1. **复用现有分区策略**：request_logs已按时间分区，性能优化成熟
2. **查询简化**：大部分查询需要同时访问client和outbound数据
3. **迁移成本低**：只需ALTER TABLE，不需要数据复制
4. **索引有效**：通过WHERE子句过滤，索引高效

**权衡**：
- 优点：实施快速，风险低
- 缺点：单表行数增加（但通过分区+索引缓解）

### ADR-002: 为什么Tier配置使用数据库表而不是代码常量？

**决策**：使用`task_type_tier_config`表存储配置

**理由**：
1. **支持租户定制**：不同租户可有不同Tier偏好
2. **动态调整**：无需重启服务即可调整配置
3. **审计追踪**：配置变更有记录
4. **运维友好**：通过Admin UI或SQL直接修改

**权衡**：
- 优点：灵活性高，可运维
- 缺点：启动时需加载配置（通过缓存缓解）

---

## 八、后续优化方向

### Phase 7: 子代理成本优化（未来）

**思路**：根据请求深度自动降级Tier
- depth=0（主代理）：按任务类型选择Tier
- depth=1（子代理）：降级1档（A→B, B→C）
- depth≥2（孙代理）：固定Tier-C

**预期收益**：再节约20% ≈ $100/天

### Phase 8: 机器学习增强分类（未来）

**思路**：训练任务分类模型替代启发式规则
- 收集标注数据：10,000+样本
- 训练BERT/RoBERTa多分类模型
- 在线推理：system prompt + 最近3轮 → 任务类型

**预期收益**：分类准确率从85%提升到95%

---

## 九、参考文档

- [AUTO_SELECTION_SPEC.md](./AUTO_SELECTION_SPEC.md) - Auto模型选择规范
- [AUTO_SELECTION_IMPLEMENTATION_PLAN.md](./AUTO_SELECTION_IMPLEMENTATION_PLAN.md) - 实施计划
- [expert-detection/00-design.md](../../design/expert-detection/00-design.md) - 专家类型识别
- [routing-domain.md](../01-architecture/architecture/routing-domain.md) - 路由域架构

---

**文档维护**：
- 初始版本：2026-09-02（ZCode AI Assistant）
- 最后更新：2026-09-02
- 审核状态：✅ Phase 1已批准，实施中
