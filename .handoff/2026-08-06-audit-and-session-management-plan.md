# 2026-08-06 代码审计与会话管理功能规划

## 一、今日修改审计总结

### 1.1 提交概览
今天共有 **47+ 个提交**，主要集中在以下几个方面：

#### 核心修改领域
1. **会话压缩与工具剥离** (compression/strip.go)
2. **自动总结生成器** (auto_summary_generator.go)
3. **自动标题生成器** (auto_title_generator.go)
4. **总结存储层** (summarystore/store.go)
5. **流式执行器** (streaming/executors)
6. **安全修复** (prompt injection防护)

### 1.2 关键提交分析

#### ✅ 325d6fe2 - Prompt Injection 防护
**修改内容**：
- 在 `auto_summary_generator.go` 和 `auto_title_generator.go` 中，用户内容被包裹在 `<session_transcript>` XML 标签中
- 防止用户输入劫持系统提示词

**审计评价**：✅ 正确
- 这是标准的 prompt injection 防护措施
- XML 标签明确分隔系统指令与用户内容

#### ✅ 6ddef3d0 - 文档与设计说明
**修改内容**：
- 补充了压缩模块的 fail-open 设计理念文档
- 说明了 keepLastRounds 的设计
- 添加了 UTF-8 清理的可观测性

**审计评价**：✅ 正确
- 文档完整，设计合理

#### ⚠️ 693714f8 - 自动标题 max_tokens 限制移除
**修改内容**：
- 移除了 `max_tokens=48` 的硬编码限制
- 改由系统提示词控制 18 字符标题长度

**审计评价**：⚠️ 需要验证
- **潜在问题**：依赖 LLM 自律遵守字符限制，可能导致：
  - 成本失控（生成超长响应）
  - 标题过长影响 UI 显示
- **建议**：保留一个合理的 max_tokens 上限（如 128），作为防护栏

#### ✅ 010f7cf5 - UTF-8 清理防止数据库错误
**修改内容**：
- 在 INSERT 前清理无效 UTF-8 序列
- 防止 SQLSTATE 22021 错误

**审计评价**：✅ 正确
- 使用 `strings.ToValidUTF8` 替换无效字节为 U+FFFD
- 生产环境已验证有效（解决了 157 个失败案例）

#### ✅ 4e3341a4 - 压缩模块原子性修复 (P0)
**修改内容**：
- tool-strip 原子 round 处理
- 执行顺序优化
- summarystore 时间列修复
- trim/retry 逻辑拆分

**审计评价**：✅ 正确
- 关键 P0 修复，解决了工具调用完整性问题

### 1.3 架构层面审计

#### 会话生命周期管理
```
用户请求 → 流式处理 → emitTelemetry → MaybeGenerate{Title,Summary}
                                            ↓
                                    后台异步处理
                                            ↓
                                    LLM 调用（带重试）
                                            ↓
                                    summarystore.Upsert
                                            ↓
                                    session_summaries 表
```

**审计发现的问题**：

#### ❌ 问题1：会话总结触发时机不合理
**当前实现**：
- `MaybeGenerateSummary` 在**每次请求成功后**都触发
- 仅通过 rolling gate（3轮门槛）来过滤

**问题**：
- ❌ **违反需求**：用户要求"会话至少5轮以上或者会话完成后才进行"
- ❌ **成本浪费**：即使有 rolling gate，仍然会在第 3、6、9... 轮频繁触发
- ❌ **缺少会话完成信号**：没有检测会话是否真正结束

**建议修复**：
```go
// 触发条件应该是：
// 1. 会话轮次 >= 5 AND 距上次总结 >= 3 轮
// 2. OR 接收到会话结束信号（需要新增）
func (g *AutoSummaryGenerator) shouldTriggerSummary(ctx context.Context, sessionID string) (bool, string, time.Time, error) {
    // 获取会话总轮次
    totalTurns, err := g.store.CountTotalTurns(ctx, sessionID)
    if err != nil {
        return true, "db_error", time.Time{}, err
    }
    
    // 第一条规则：会话必须至少5轮
    if totalTurns < 5 {
        return false, fmt.Sprintf("session_too_short_%d_turns", totalTurns), time.Time{}, nil
    }
    
    // 第二条规则：rolling gate（距上次总结至少3轮）
    last, err := g.store.LastSummarized(ctx, sessionID)
    if err != nil && !isPgxNoRows(err) {
        return true, "db_error", time.Time{}, err
    }
    if !last.IsZero() {
        n, err := g.store.CountNewTurns(ctx, sessionID, last)
        if err != nil {
            return true, "db_error", last, err
        }
        if n < rollingTurnGate() {
            return false, fmt.Sprintf("only_%d_new_turns_since_last_summary", n), last, nil
        }
    }
    
    return true, "rolling_gate_open", last, nil
}
```

#### ❌ 问题2：项目(project)和任务(task)概念混淆
**当前实现**：
- `request_logs` 表有 `gw_task_id` 字段
- 但没有 `gw_project` 或 `gw_project_id` 字段
- `session_summaries` 表也缺少项目关联

**问题**：
- ❌ 用户提到"根据会话的项目、任务及tag等"，但系统缺少项目维度
- ❌ 无法按项目分组统计成本和进度

**建议修复**：
- 添加 `gw_project_id` 字段到 `request_logs` 和 `session_summaries`
- 或者明确 `gw_task_id` 的语义（如果它实际是项目ID）

#### ❌ 问题3：缺少会话标签(tags)支持
**当前实现**：
- `request_logs` 表没有 tags 字段
- `session_summaries` 表有 `key_topics`（从总结中提取），但不是用户标签

**问题**：
- ❌ 无法让用户手动标记会话（如 "bugfix", "feature", "探索"）
- ❌ 无法按标签过滤和组织会话

**建议修复**：
```sql
ALTER TABLE session_summaries ADD COLUMN user_tags text[] DEFAULT '{}';
CREATE INDEX idx_session_summaries_user_tags ON session_summaries USING gin(user_tags);
```

#### ❌ 问题4：缺少会话列表页
**当前实现**：
- 只有请求列表页（request_logs）
- 没有独立的会话列表页

**问题**：
- ❌ 用户无法按会话维度浏览和管理
- ❌ 无法看到会话的脉络和流程

## 二、会话管理功能需求分析

### 2.1 核心需求
根据用户描述，需要实现以下功能：

#### 需求1：会话列表页（非请求列表页）
**功能**：
- 展示所有会话的摘要信息
- 每个会话显示：标题、项目、任务、标签、轮次、成本、时长、最后活动时间
- 支持按项目、任务、标签、时间范围过滤
- 支持搜索（标题、总结内容）

#### 需求2：会话脉络展示
**功能**：
- 按时间顺序展示会话流程
- 显示会话之间的关联（同一任务/项目下的会话）
- 可视化展示会话的意图演进

#### 需求3：任务脉络展示
**功能**：
- 按任务组织会话
- 显示任务完成的过程（多个会话协同）
- 汇总任务级别的成本和时间

#### 需求4：成本统计与分析
**功能**：
- 单个会话的总 token 用量和成本
- 任务级别的成本汇总
- 项目级别的成本汇总
- 按时间维度的成本趋势

### 2.2 数据模型设计

#### 当前表结构问题
```
request_logs:
  ✅ gw_session_id (会话ID)
  ✅ gw_task_id (任务ID)
  ❌ gw_project_id (项目ID) - 缺失
  ❌ user_tags (用户标签) - 缺失
  ✅ session_title (会话标题)
  ✅ session_summary (会话总结) - 但在请求表中冗余

session_summaries:
  ✅ session_key (会话ID，PK)
  ✅ tenant_id
  ✅ title (标题)
  ✅ summary (总结)
  ✅ key_topics (关键主题)
  ✅ user_intent (用户意图)
  ✅ total_cost_usd (成本)
  ✅ total_tokens (token总量)
  ✅ request_count (请求数)
  ❌ gw_task_id (任务ID) - 缺失
  ❌ gw_project_id (项目ID) - 缺失
  ❌ user_tags (用户标签) - 缺失
```

#### 建议的 schema 修改
```sql
-- 1. 添加项目和标签支持
ALTER TABLE session_summaries 
  ADD COLUMN gw_project_id text,
  ADD COLUMN gw_task_id text,
  ADD COLUMN user_tags text[] DEFAULT '{}',
  ADD COLUMN session_status varchar(20) DEFAULT 'active'; -- active, completed, abandoned

-- 2. 创建索引
CREATE INDEX idx_session_summaries_project ON session_summaries(gw_project_id) WHERE gw_project_id IS NOT NULL;
CREATE INDEX idx_session_summaries_task ON session_summaries(gw_task_id) WHERE gw_task_id IS NOT NULL;
CREATE INDEX idx_session_summaries_user_tags ON session_summaries USING gin(user_tags);
CREATE INDEX idx_session_summaries_status ON session_summaries(session_status, last_request_at DESC);

-- 3. 添加全文搜索支持
ALTER TABLE session_summaries 
  ADD COLUMN search_vector tsvector GENERATED ALWAYS AS (
    to_tsvector('simple', COALESCE(title, '') || ' ' || COALESCE(summary, ''))
  ) STORED;
CREATE INDEX idx_session_summaries_search ON session_summaries USING gin(search_vector);

-- 4. 创建会话关联视图（用于显示会话脉络）
CREATE OR REPLACE VIEW v_session_flow AS
SELECT 
  s.session_key,
  s.gw_project_id,
  s.gw_task_id,
  s.title,
  s.summary,
  s.user_intent,
  s.first_request_at,
  s.last_request_at,
  s.duration_seconds,
  s.request_count,
  s.total_cost_usd,
  s.total_tokens,
  s.user_tags,
  s.session_status,
  -- 同任务下的前后会话
  LAG(s.session_key) OVER (PARTITION BY s.gw_task_id ORDER BY s.first_request_at) as prev_session,
  LEAD(s.session_key) OVER (PARTITION BY s.gw_task_id ORDER BY s.first_request_at) as next_session,
  -- 任务内排序
  ROW_NUMBER() OVER (PARTITION BY s.gw_task_id ORDER BY s.first_request_at) as session_order_in_task
FROM session_summaries s
WHERE s.gw_task_id IS NOT NULL;

-- 5. 创建任务汇总视图
CREATE OR REPLACE VIEW v_task_summary AS
SELECT 
  gw_task_id,
  tenant_id,
  COUNT(DISTINCT session_key) as session_count,
  MIN(first_request_at) as task_started_at,
  MAX(last_request_at) as task_last_activity,
  SUM(request_count) as total_requests,
  SUM(total_cost_usd) as total_cost_usd,
  SUM(total_tokens) as total_tokens,
  SUM(duration_seconds) as total_duration_seconds,
  -- 任务状态：所有会话都完成则完成，否则进行中
  CASE 
    WHEN BOOL_AND(session_status = 'completed') THEN 'completed'
    WHEN BOOL_OR(session_status = 'active') THEN 'in_progress'
    ELSE 'abandoned'
  END as task_status,
  -- 聚合所有会话的标题（用于展示任务脉络）
  ARRAY_AGG(title ORDER BY first_request_at) as session_titles
FROM session_summaries
WHERE gw_task_id IS NOT NULL
GROUP BY gw_task_id, tenant_id;

-- 6. 创建项目汇总视图
CREATE OR REPLACE VIEW v_project_summary AS
SELECT 
  gw_project_id,
  tenant_id,
  COUNT(DISTINCT gw_task_id) as task_count,
  COUNT(DISTINCT session_key) as session_count,
  MIN(first_request_at) as project_started_at,
  MAX(last_request_at) as project_last_activity,
  SUM(request_count) as total_requests,
  SUM(total_cost_usd) as total_cost_usd,
  SUM(total_tokens) as total_tokens
FROM session_summaries
WHERE gw_project_id IS NOT NULL
GROUP BY gw_project_id, tenant_id;
```

### 2.3 API 设计

#### API 1: 会话列表
```
GET /api/sessions/list
Query Parameters:
  - tenant_id: string (租户过滤)
  - project_id: string (项目过滤)
  - task_id: string (任务过滤)
  - tags: string[] (标签过滤，多个用逗号分隔)
  - status: string (active|completed|abandoned)
  - search: string (全文搜索)
  - from_date: string (起始日期)
  - to_date: string (结束日期)
  - sort_by: string (first_request_at|last_request_at|total_cost_usd|total_tokens)
  - sort_order: string (asc|desc)
  - page: int
  - page_size: int

Response:
{
  "sessions": [
    {
      "session_key": "gw_abc123",
      "title": "实现用户认证功能",
      "summary": "...",
      "project_id": "proj_001",
      "task_id": "task_042",
      "user_tags": ["feature", "auth"],
      "user_intent": "implement_feature",
      "status": "completed",
      "first_request_at": "2026-08-06T10:00:00Z",
      "last_request_at": "2026-08-06T12:30:00Z",
      "duration_seconds": 9000,
      "request_count": 23,
      "total_cost_usd": 0.45,
      "total_tokens": 125000,
      "models_used": ["claude-opus-5", "gpt-4o"]
    }
  ],
  "total": 156,
  "page": 1,
  "page_size": 20
}
```

#### API 2: 会话详情
```
GET /api/sessions/{session_key}

Response:
{
  "session_key": "gw_abc123",
  "title": "实现用户认证功能",
  "summary": "完整总结...",
  "key_topics": ["JWT", "OAuth", "Session管理"],
  "user_intent": "implement_feature",
  "project_id": "proj_001",
  "task_id": "task_042",
  "user_tags": ["feature", "auth"],
  "status": "completed",
  // 统计信息
  "stats": {
    "first_request_at": "2026-08-06T10:00:00Z",
    "last_request_at": "2026-08-06T12:30:00Z",
    "duration_seconds": 9000,
    "request_count": 23,
    "success_count": 21,
    "error_count": 2,
    "total_cost_usd": 0.45,
    "total_tokens": 125000,
    "total_prompt_tokens": 95000,
    "total_completion_tokens": 30000
  },
  // 会话流程（所有请求的简要信息）
  "requests": [
    {
      "request_id": "req_001",
      "ts": "2026-08-06T10:00:00Z",
      "client_model": "claude-opus-5",
      "request_preview": "如何实现JWT认证？",
      "success": true,
      "tokens": 5000,
      "cost_usd": 0.02
    }
  ],
  // 同任务下的相关会话
  "related_sessions": {
    "prev": {
      "session_key": "gw_abc122",
      "title": "设计认证架构"
    },
    "next": {
      "session_key": "gw_abc124",
      "title": "实现权限控制"
    }
  }
}
```

#### API 3: 任务脉络
```
GET /api/sessions/task-flow/{task_id}

Response:
{
  "task_id": "task_042",
  "task_summary": {
    "session_count": 5,
    "total_cost_usd": 2.15,
    "total_tokens": 580000,
    "started_at": "2026-08-05T09:00:00Z",
    "last_activity": "2026-08-06T16:00:00Z",
    "status": "in_progress"
  },
  "sessions": [
    {
      "session_key": "gw_abc120",
      "title": "需求分析",
      "summary": "...",
      "order": 1,
      "started_at": "2026-08-05T09:00:00Z",
      "cost_usd": 0.35
    },
    {
      "session_key": "gw_abc121",
      "title": "技术选型",
      "summary": "...",
      "order": 2,
      "started_at": "2026-08-05T14:00:00Z",
      "cost_usd": 0.28
    }
  ]
}
```

#### API 4: 项目成本汇总
```
GET /api/sessions/project-costs/{project_id}

Response:
{
  "project_id": "proj_001",
  "summary": {
    "task_count": 12,
    "session_count": 47,
    "total_cost_usd": 18.65,
    "total_tokens": 5200000,
    "started_at": "2026-07-01T00:00:00Z",
    "last_activity": "2026-08-06T16:00:00Z"
  },
  "tasks": [
    {
      "task_id": "task_042",
      "session_count": 5,
      "cost_usd": 2.15,
      "status": "in_progress"
    }
  ],
  "daily_costs": [
    {
      "date": "2026-08-06",
      "cost_usd": 1.25,
      "tokens": 350000,
      "session_count": 3
    }
  ]
}
```

#### API 5: 更新会话元数据
```
PATCH /api/sessions/{session_key}
Body:
{
  "project_id": "proj_001",
  "task_id": "task_042",
  "user_tags": ["feature", "auth", "security"],
  "status": "completed"
}

Response:
{
  "success": true,
  "session_key": "gw_abc123"
}
```

### 2.4 前端页面设计

#### 页面1: 会话列表页
```
/sessions

布局：
┌──────────────────────────────────────────────────┐
│ 会话管理                                          │
├──────────────────────────────────────────────────┤
│ [搜索框] [项目筛选] [任务筛选] [标签筛选] [新建] │
│                                                   │
│ ┌────────────────────────────────────────────┐  │
│ │ 📋 实现用户认证功能                         │  │
│ │ 项目: Web应用 | 任务: 认证模块 | #feature  │  │
│ │ 23轮 | ¥0.45 | 2.5小时 | 完成               │  │
│ │ 2026-08-06 10:00 - 12:30                   │  │
│ └────────────────────────────────────────────┘  │
│                                                   │
│ ┌────────────────────────────────────────────┐  │
│ │ 📋 调试登录失败问题                         │  │
│ │ 项目: Web应用 | 任务: bugfix | #bugfix     │  │
│ │ 12轮 | ¥0.18 | 45分钟 | 活跃                │  │
│ │ 2026-08-06 14:00 - 14:45                   │  │
│ └────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────┘
```

#### 页面2: 任务脉络页
```
/sessions/task/{task_id}

布局：
┌──────────────────────────────────────────────────┐
│ 任务: 认证模块 (task_042)                        │
│ 5个会话 | ¥2.15 | 进行中                          │
├──────────────────────────────────────────────────┤
│                                                   │
│ Timeline:                                         │
│                                                   │
│ 08-05 09:00 ──[1]──> 需求分析 (¥0.35)           │
│                │                                  │
│                ├─ 分析了JWT vs Session优劣       │
│                └─ 确定使用JWT方案                │
│                                                   │
│ 08-05 14:00 ──[2]──> 技术选型 (¥0.28)           │
│                │                                  │
│                ├─ 选择jose库                     │
│                └─ 设计token刷新机制              │
│                                                   │
│ 08-06 10:00 ──[3]──> 实现基础功能 (¥0.45) ✓     │
│                                                   │
│ 08-06 14:00 ──[4]──> 调试问题 (¥0.18)           │
│                                                   │
│ 08-06 16:00 ──[5]──> 编写测试 (进行中...)       │
└──────────────────────────────────────────────────┘
```

## 三、实施计划

### 阶段1: 审计修复 (优先级 P0)
- [ ] 修复自动总结触发逻辑（添加5轮门槛）
- [ ] 恢复 max_tokens 防护栏（auto_title）
- [ ] 添加会话完成信号检测
- [ ] 验证 UTF-8 清理的可观测性

### 阶段2: Schema 扩展 (优先级 P1)
- [ ] 添加 gw_project_id 字段
- [ ] 添加 user_tags 字段
- [ ] 添加 session_status 字段
- [ ] 创建全文搜索索引
- [ ] 创建视图 (v_session_flow, v_task_summary, v_project_summary)

### 阶段3: 后端 API (优先级 P1)
- [ ] 实现会话列表 API
- [ ] 实现会话详情 API
- [ ] 实现任务脉络 API
- [ ] 实现项目成本汇总 API
- [ ] 实现会话元数据更新 API

### 阶段4: 前端页面 (优先级 P2)
- [ ] 实现会话列表页
- [ ] 实现任务脉络页
- [ ] 实现项目成本汇总页
- [ ] 实现会话详情页

### 阶段5: 数据同步 (优先级 P2)
- [ ] 从 request_logs 回填 session_summaries.gw_task_id
- [ ] 实现实时同步（在 emitTelemetry 时）

## 四、待确认问题

### 问题1: 项目 vs 任务的定义
**当前不明确**：
- `gw_task_id` 是什么？是 ZCode 的 task_id 还是用户自定义的任务？
- 是否需要独立的项目概念？还是任务已经足够？

**建议**：
- 明确定义项目 = 一组相关任务的集合
- 任务 = 一个具体的工作目标（可能跨多个会话）
- 会话 = 完成任务的一次对话过程

### 问题2: 会话完成信号
**当前缺失**：
- 没有办法标记会话已经完成
- 依赖用户不再发送请求（被动判断）

**建议**：
- 添加显式的"关闭会话"操作
- 或者：超过 N 小时无活动自动标记为完成

### 问题3: 历史数据迁移
**当前问题**：
- 已有大量 session_summaries 数据，但缺少 project/task/tags
- 如何回填？

**建议**：
- 从 request_logs 的 gw_task_id 回填
- 用户标签需要手动补充或通过 LLM 推断

## 五、总结

### 今日修改质量
- ✅ 大部分修改质量良好，特别是安全修复和数据完整性修复
- ⚠️ 存在少量设计问题（总结触发逻辑、max_tokens 移除）
- ❌ 缺少核心功能（项目维度、标签、会话列表）

### 建议优先级
1. **P0**: 修复总结触发逻辑（避免成本浪费）
2. **P1**: 扩展 schema 和实现后端 API
3. **P2**: 实现前端页面和数据迁移
