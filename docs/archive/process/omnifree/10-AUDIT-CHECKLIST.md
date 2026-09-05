# 审计清单

> OmniFree 方案完整审计 — 确保架构合理、实施可行、风险可控

---

## ✅ 架构审计

### 1. 数据模型设计

- [x] **表结构完整性**
  - [x] `free_resource_catalog`: 15 字段，覆盖 free_type/ToS/配额/验证
  - [x] `free_quota_tracker`: 17 字段，支持双窗口 + 429 校准
  - [x] `auto_combo_templates`: 14 字段，支持变体/过滤/评分权重
  - [x] `keyless_providers`: 11 字段，覆盖无认证提供商元数据
  
- [x] **RLS 多租户隔离**
  - [x] 所有表启用 RLS
  - [x] 统一策略: `tenant_id = current_setting('app.current_tenant_id', true)::bigint`
  - [x] 索引覆盖 tenant_id
  
- [x] **索引优化**
  - [x] `free_resource_catalog`: 5 个索引 (provider/free_type/tos/pool_key/tenant)
  - [x] `free_quota_tracker`: 6 个索引 (credential/provider_model/exhausted/window/cleanup/tenant)
  - [x] `auto_combo_templates`: 3 个索引 (variant/name/tenant)
  - [x] 所有外键有对应索引
  
- [x] **数据完整性**
  - [x] CHECK 约束: free_type/tos_verdict/window_type/variant
  - [x] 外键约束: provider_code → provider_catalog.code
  - [x] UNIQUE 约束: 防止重复条目
  - [x] NOT NULL 约束: 关键字段非空
  
- [ ] **待补充**
  - [ ] 触发器: `update_updated_at_column()` 需确认已存在
  - [ ] 分区: `free_quota_tracker` 未来可按时间分区优化查询

### 2. 配额追踪逻辑

- [x] **窗口类型覆盖**
  - [x] hour-5: 滚动 5 小时 (FreeModel.dev 模式)
  - [x] day-1: UTC 日历日 (OpenRouter/Groq)
  - [x] day-7: 7 日滚动 (Codex)
  - [x] month-1: UTC 日历月 (Mistral/Gemini)
  
- [x] **429 校准机制**
  - [x] 解析 `Retry-After` (秒数 / HTTP-date)
  - [x] 解析 `X-RateLimit-Reset` (Unix timestamp)
  - [x] 解析 `X-RateLimit-Limit` (上限覆盖)
  - [x] 自动标记耗尽状态 + 设置 `auto_reset_at`
  
- [x] **预检过滤**
  - [x] 查询当前窗口使用情况
  - [x] 计算剩余百分比
  - [x] 阈值过滤 (默认最少剩余 10%)
  - [x] 自动解除过期耗尽状态
  
- [x] **并发安全**
  - [x] UPSERT 使用 `ON CONFLICT DO UPDATE`
  - [x] 原子递增 `request_count` / `token_count`
  
- [ ] **待完善**
  - [ ] 滚动窗口 (hour-5/day-7) 的清理逻辑需优化
  - [ ] 高并发场景下的热点行竞争优化 (考虑 Redis 缓存)

### 3. 虚拟路由设计

- [x] **解析器完整性**
  - [x] 支持 `auto/*` 模式识别
  - [x] 数据库模板优先
  - [x] 内置模板回退 (6 个常用模板)
  
- [x] **候选池构建**
  - [x] 加载已连接免费凭据
  - [x] 加载 keyless 提供商
  - [x] 配额预检过滤
  - [x] ToS 合规过滤
  - [x] 白名单/黑名单过滤
  - [x] 候选数量限制
  
- [x] **评分与选择**
  - [x] 6 维评分: health/latency/quota/cost/task_fit/tier_affinity
  - [x] 归一化处理
  - [x] 分层策略: top (≥0.8) / mid (0.5-0.8) / rest (<0.5)
  - [x] 明显优胜者选择 (diff ≥ 0.1)
  - [x] Tier 内轮换
  
- [ ] **待完善**
  - [ ] 轮换状态持久化 (当前为随机，应记录上次选择)
  - [ ] 探索率 (exploration_rate) 的 bandit 逻辑未实现
  - [ ] Task fit 评分需对接任务分类器

### 4. Keyless 提供商

- [x] **注册表设计**
  - [x] 支持 3 种 bootstrap 方法: none / device-fingerprint / embedded-browser
  - [x] 限制元数据: rpm/rpd/concurrent
  - [x] 可靠性评分
  - [x] Auto combo allowlist 控制
  
- [ ] **实现缺失**
  - [ ] `provider/keyless/` 包尚未实现
  - [ ] 设备指纹生成逻辑
  - [ ] 嵌入浏览器 (Playwright/Puppeteer) 集成
  - [ ] 合成凭据 `SYNTHETIC_KEYLESS_CREDENTIAL_ID = -1` 的特殊处理

---

## 🔒 安全审计

### 1. 多租户隔离

- [x] **RLS 策略**
  - [x] 所有表启用 RLS
  - [x] 统一 `tenant_id` 过滤
  - [x] 触发器自动填充 `tenant_id`
  
- [x] **SQL 注入防护**
  - [x] 所有查询使用参数化 `$1, $2...`
  - [x] 不拼接用户输入到 SQL
  
- [ ] **待加强**
  - [ ] 审计日志: 免费资源配置变更应记录审计事件
  - [ ] 权限控制: Admin API 需集成现有 RBAC

### 2. ToS 合规

- [x] **风险分级**
  - [x] ok: 明确允许 (6 个提供商)
  - [x] caution: 灰色地带 (2 个)
  - [x] ambiguous: 未找到条款 (2 个)
  - [x] avoid: 明确禁止 (需排除)
  - [x] unknown: 未审查
  
- [x] **过滤机制**
  - [x] Auto combo 支持 `tos_filter` 配置
  - [x] 默认排除 `avoid`
  - [x] `tos_notes` 说明理由
  
- [ ] **待完善**
  - [ ] 定期重新审查 ToS (每季度)
  - [ ] 用户可自定义 ToS 风险容忍度

### 3. 配额滥用防护

- [x] **本地计量**
  - [x] 双窗口限制 (短期 + 长期)
  - [x] 预检拦截
  
- [ ] **待加强**
  - [ ] 异常检测: 单凭据短时间内耗尽多次 → 告警
  - [ ] 租户级限流: 防止单租户耗尽所有免费配额
  - [ ] IP/设备指纹关联: 防止账号轮换滥用

---

## 📊 性能审计

### 1. 数据库性能

- [x] **索引覆盖**
  - [x] 所有高频查询路径有索引
  - [x] 复合索引优先级合理
  
- [x] **查询优化**
  - [x] Preflight 查询限制到单行 (`window_start <= now() AND window_end >= now()`)
  - [x] 候选池查询使用 JOIN 而非 N+1
  
- [ ] **待优化**
  - [ ] `free_quota_tracker` 在高并发下可能成为热点 (考虑 Redis 缓存)
  - [ ] 候选池构建查询 (2 JOIN) 需 EXPLAIN 分析
  - [ ] 定期清理历史窗口 (避免表膨胀)

### 2. 内存与并发

- [ ] **待评估**
  - [ ] 虚拟 combo 候选池构建的内存开销 (最多 50 个候选)
  - [ ] 配额追踪 UPSERT 的行锁竞争
  - [ ] 轮换状态持久化的并发控制

### 3. 缓存策略

- [ ] **建议引入**
  - [ ] 免费资源目录缓存 (5 分钟 TTL)
  - [ ] Auto combo 模板缓存 (10 分钟 TTL)
  - [ ] Keyless 提供商列表缓存 (30 分钟 TTL)
  - [ ] 配额剩余状态 Redis 缓存 (1 分钟 TTL)

---

## 🧪 测试审计

### 1. 单元测试覆盖

- [ ] **domains/freeresource/**
  - [ ] `catalog.go`: CRUD 操作
  - [ ] `quota_tracker.go`: Record / CorrectFromHeaders / Preflight
  - [ ] `pool_dedup.go`: 去重计算
  
- [ ] **domains/autocombo/**
  - [ ] `resolver.go`: 内置模板回退
  - [ ] `virtual_factory.go`: 候选池构建 + 过滤
  - [ ] `engine.go`: 评分 / 分层 / 选择
  
- [ ] **bg/freequota***
  - [ ] `reset_worker.go`: 过期窗口重置
  - [ ] `cleanup_worker.go`: 历史数据清理

### 2. 集成测试

- [ ] **端到端场景**
  - [ ] 用户请求 `auto/free` → 路由到免费提供商
  - [ ] 配额耗尽 → 自动切换下一凭据
  - [ ] 429 响应 → 校准限制 + 跳过凭据
  - [ ] Keyless 提供商调用
  
- [ ] **边界条件**
  - [ ] 无可用免费凭据 → 返回友好错误
  - [ ] 所有候选配额耗尽 → 降级到付费 tier
  - [ ] 窗口边界 (UTC 日/月切换)

### 3. 压力测试

- [ ] **高并发配额追踪**
  - [ ] 100 并发请求同一凭据 → UPSERT 竞争
  - [ ] 10 并发 429 响应 → 校准幂等性
  
- [ ] **候选池规模**
  - [ ] 50 个候选 × 1000 QPS → 选择延迟
  - [ ] 数据库连接池饱和

---

## 📝 文档审计

- [x] **设计文档完整性**
  - [x] `00-OVERVIEW.md`: 目标 / 对比 / 架构 / 路线图
  - [x] `01-DATA-MODEL.md`: 表结构 / RLS / 索引 / 视图
  - [x] `02-QUOTA-TRACKING.md`: 窗口模型 / 429 校准 / Go 实现
  - [x] `03-AUTO-COMBO.md`: 虚拟路由 / 评分引擎 / API
  - [x] `10-AUDIT-CHECKLIST.md`: 本文档
  
- [ ] **待补充**
  - [ ] `04-KEYLESS-PROVIDERS.md`: Keyless 实现细节
  - [ ] `05-TOS-COMPLIANCE.md`: ToS 审查流程
  - [ ] `06-DISCOVERY.md`: 自动发现机制
  - [ ] `07-MIGRATION-GUIDE.md`: 从现有系统迁移
  - [ ] `08-TESTING-PLAN.md`: 详细测试用例
  - [ ] `09-DEPLOYMENT.md`: 部署步骤 / 灰度策略
  
- [x] **种子数据**
  - [x] `seed/free_resource_catalog.json`: 15 个初始免费资源
  - [x] `seed/auto_combo_templates.json`: 6 个内置模板
  - [x] `seed/keyless_providers.json`: 3 个 keyless 提供商

---

## 🚀 实施审计

### Phase 1: 数据模型 (P0)

- [ ] **SQL 迁移**
  - [ ] 编写 `sql/migrations/XXX-omnifree-schema.sql`
  - [ ] 包含 4 张新表 + 扩展现有表
  - [ ] RLS 策略 + 索引 + 触发器
  - [ ] 测试迁移回滚
  
- [ ] **种子数据导入**
  - [ ] 编写 Go 工具 `cmd/seed-free-resources/main.go`
  - [ ] 从 JSON 读取 → INSERT ON CONFLICT DO UPDATE
  - [ ] 验证数据完整性

### Phase 2: 配额追踪 (P0)

- [ ] **核心实现**
  - [ ] `domains/freeresource/quota_tracker.go`: 300 行
  - [ ] `domains/freeresource/types.go`: 100 行
  - [ ] 单元测试: 150 行
  
- [ ] **集成**
  - [ ] Hook 到 `domains/streaming/executors/`
  - [ ] 请求前 Preflight
  - [ ] 响应后 Record + 429 Correct
  
- [ ] **后台 Worker**
  - [ ] `bg/freequotareset/worker.go`: 每 5 分钟重置过期窗口
  - [ ] `bg/freequotacleanup/worker.go`: 每日清理历史数据

### Phase 3: 虚拟路由 (P1)

- [ ] **核心实现**
  - [ ] `domains/autocombo/resolver.go`: 150 行
  - [ ] `domains/autocombo/virtual_factory.go`: 300 行
  - [ ] `domains/autocombo/engine.go`: 250 行
  - [ ] 单元测试: 200 行
  
- [ ] **集成**
  - [ ] Hook 到 `domains/streaming/handler.go`
  - [ ] 解析 `auto/*` → 构建候选池 → 选择 → 重写请求
  
- [ ] **Admin API**
  - [ ] `GET /admin/auto-combos`: 列出模板
  - [ ] `GET /admin/auto-combos/:name/preview`: 预览候选池
  - [ ] `POST /admin/auto-combos`: 创建自定义模板

### Phase 4: Keyless 提供商 (P2)

- [ ] **框架实现**
  - [ ] `provider/keyless/registry.go`: 注册表
  - [ ] `provider/keyless/types.go`: 接口定义
  
- [ ] **示例实现**
  - [ ] `provider/keyless/opencode.go`: OpenCode AI
  - [ ] 其他 keyless 提供商延后
  
- [ ] **集成**
  - [ ] VirtualFactory 加载 keyless 候选
  - [ ] 合成凭据特殊处理

### Phase 5: 监控与可观测 (P1)

- [ ] **指标**
  - [ ] `free_quota.exhausted_ratio`: 耗尽率
  - [ ] `free_quota.429_corrections`: 校准次数
  - [ ] `auto_combo.requests`: 使用量
  - [ ] `auto_combo.candidate_pool_size`: 候选池大小
  
- [ ] **日志**
  - [ ] Auto combo 路由决策日志
  - [ ] 配额耗尽事件日志
  - [ ] ToS 过滤日志
  
- [ ] **仪表盘**
  - [ ] Grafana: 免费资源总配额 / 使用率 / Top 提供商
  - [ ] Admin UI: 免费资源目录管理

### Phase 6: 文档与培训 (P2)

- [ ] **用户文档**
  - [ ] 如何使用 `auto/free` API
  - [ ] 免费配额限制说明
  - [ ] ToS 合规提示
  
- [ ] **运维文档**
  - [ ] 如何添加新免费提供商
  - [ ] 如何审查 ToS
  - [ ] 配额异常排查手册

---

## 🎯 审计结论

### 设计合理性: ✅ 通过

- 数据模型完整，覆盖免费资源全生命周期
- 配额追踪机制借鉴 OmniRoute 成熟方案
- 虚拟路由设计灵活，支持零配置使用
- 多租户隔离、ToS 合规考虑周全

### 实施可行性: ⚠️ 需补充

- **P0 缺失**: Keyless 提供商实现尚未启动
- **P1 缺失**: 轮换状态持久化、探索率 bandit
- **P2 缺失**: 自动发现、定期 ToS 审查

### 风险评估: 🔶 中等风险

| 风险 | 等级 | 缓解措施 |
|------|------|----------|
| 配额追踪热点竞争 | 🔶 中 | 引入 Redis 缓存 |
| Keyless 提供商不稳定 | 🔶 中 | 可靠性评分 + 自动降级 |
| ToS 变更导致合规问题 | 🔴 高 | 定期审查 + 用户告知 |
| 免费资源滥用 | 🔶 中 | 租户级限流 + 异常检测 |

### 下一步建议

1. **立即执行**: Phase 1 (数据模型) + Phase 2 (配额追踪)
2. **2 周内**: Phase 3 (虚拟路由) + Phase 5 (监控)
3. **1 个月内**: Phase 4 (Keyless) + Phase 6 (文档)
4. **持续优化**: ToS 审查流程、性能优化、自动发现

---

**审计完成时间**: 2026-08-07  
**审计人**: ZCode AI Agent  
**审计版本**: v1.0
