# OmniFree 实施计划与自动化

> 分阶段实施，每阶段可独立验证，支持灰度发布

---

## 📋 总体时间线

| 阶段 | 任务 | 工作量 | 优先级 | 目标日期 |
|------|------|--------|--------|----------|
| **Phase 1** | 数据模型与种子数据 | 3 天 | P0 | Day 1-3 |
| **Phase 2** | 本地配额追踪 | 5 天 | P0 | Day 4-8 |
| **Phase 3** | 虚拟自动路由 | 7 天 | P1 | Day 9-15 |
| **Phase 4** | Keyless 提供商 | 4 天 | P2 | Day 16-19 |
| **Phase 5** | 监控与可观测 | 3 天 | P1 | Day 20-22 |
| **Phase 6** | 文档与培训 | 2 天 | P2 | Day 23-24 |
| **总计** | - | **24 工作日** | - | **约 1 个月** |

---

## 🚀 Phase 1: 数据模型与种子数据 (Day 1-3)

### 任务清单

- [ ] **Day 1: 创建数据库迁移**
  - [ ] 编写 `sql/migrations/075-omnifree-schema.sql`
  - [ ] 创建 4 张新表
  - [ ] 扩展现有 3 张表
  - [ ] 添加 RLS 策略、索引、触发器
  - [ ] 编写回滚脚本 `075-omnifree-schema.down.sql`

- [ ] **Day 2: 种子数据导入工具**
  - [ ] 实现 `cmd/seed-free-resources/main.go`
  - [ ] 读取 JSON → 生成 SQL → 执行
  - [ ] 幂等性保证 (ON CONFLICT DO UPDATE)
  - [ ] 验证数据完整性

- [ ] **Day 3: 本地测试与验证**
  - [ ] 本地执行迁移
  - [ ] 导入种子数据
  - [ ] 验证 RLS 隔离
  - [ ] 验证去重查询 `fn_compute_deduped_quota`

### 交付物

```
sql/migrations/
├── 075-omnifree-schema.sql          (新建 4 表 + 扩展 3 表)
└── 075-omnifree-schema.down.sql     (回滚脚本)

cmd/seed-free-resources/
├── main.go                           (种子数据导入工具)
└── README.md                         (使用说明)

docs/omnifree/seed/
├── free_resource_catalog.json        (15 个免费资源)
├── auto_combo_templates.json         (6 个模板)
└── keyless_providers.json            (3 个 keyless)
```

### 验证命令

```bash
# 1. 执行迁移
psql -U llm_gateway -d llm_gateway_dev -f sql/migrations/075-omnifree-schema.sql

# 2. 导入种子数据
go run cmd/seed-free-resources/main.go \
  --db-url "postgres://llm_gateway:password@localhost/llm_gateway_dev" \
  --catalog docs/omnifree/seed/free_resource_catalog.json \
  --templates docs/omnifree/seed/auto_combo_templates.json \
  --keyless docs/omnifree/seed/keyless_providers.json

# 3. 验证数据
psql -U llm_gateway -d llm_gateway_dev -c "
  SELECT 
    COUNT(*) AS total_resources,
    SUM(monthly_tokens) FILTER (WHERE free_type='recurring-monthly') AS monthly_total
  FROM free_resource_catalog WHERE enabled=TRUE;
"
# 预期: total_resources=15, monthly_total≈1.3B
```

---

## 🔄 Phase 2: 本地配额追踪 (Day 4-8)

### 任务清单

- [ ] **Day 4: 核心类型与接口**
  - [ ] `domains/freeresource/types.go`: WindowType/QuotaWindow/FreeType/ToSVerdict
  - [ ] `domains/freeresource/quota_tracker.go`: QuotaTracker 结构体
  - [ ] 单元测试: 窗口计算逻辑

- [ ] **Day 5: Record 与 Preflight**
  - [ ] 实现 `QuotaTracker.Record()`: UPSERT 逻辑
  - [ ] 实现 `QuotaTracker.Preflight()`: 配额检查
  - [ ] 单元测试: 并发 UPSERT、窗口边界

- [ ] **Day 6: 429 校准**
  - [ ] 实现 `QuotaTracker.CorrectFromHeaders()`: 解析响应头
  - [ ] 支持 `Retry-After` (秒数 / HTTP-date)
  - [ ] 支持 `X-RateLimit-*` 系列
  - [ ] 单元测试: 各种响应头组合

- [ ] **Day 7: 集成到 Streaming Pipeline**
  - [ ] Hook 到 `domains/streaming/executors/stream_executor.go`
  - [ ] 请求前 Preflight (过滤耗尽凭据)
  - [ ] 响应后 Record (记录消耗)
  - [ ] 429 处理 (校准 + 标记耗尽)

- [ ] **Day 8: 后台 Worker**
  - [ ] `bg/freequotareset/worker.go`: 重置过期窗口
  - [ ] `bg/freequotacleanup/worker.go`: 清理历史数据
  - [ ] 集成到 `cmd/gateway/main.go` 启动流程

### 交付物

```
domains/freeresource/
├── types.go                          (核心类型定义)
├── quota_tracker.go                  (配额追踪器)
├── quota_tracker_test.go             (单元测试)
└── README.md                         (模块文档)

bg/freequotareset/
└── worker.go                         (重置 worker)

bg/freequotacleanup/
└── worker.go                         (清理 worker)

domains/streaming/executors/
└── free_quota_hook.go                (集成 hook)
```

### 验证命令

```bash
# 1. 单元测试
go test ./domains/freeresource/... -v -race

# 2. 集成测试: 模拟配额耗尽
curl -X POST http://localhost:8781/v1/chat/completions \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "model": "openrouter/openai/gpt-3.5-turbo:free",
    "messages": [{"role": "user", "content": "test"}]
  }'
# 连续请求 51 次 → 第 51 次应跳过该凭据

# 3. 查询配额追踪
psql -c "
  SELECT provider_code, model_id, request_count, is_exhausted, auto_reset_at
  FROM free_quota_tracker
  WHERE window_type='day-1' AND window_start >= current_date
  ORDER BY request_count DESC LIMIT 10;
"
```

---

## 🔀 Phase 3: 虚拟自动路由 (Day 9-15)

### 任务清单

- [ ] **Day 9-10: Resolver + Factory**
  - [ ] `domains/autocombo/resolver.go`: 解析 `auto/*` 模型
  - [ ] `domains/autocombo/virtual_factory.go`: 构建候选池
  - [ ] 单元测试: 内置模板回退、过滤逻辑

- [ ] **Day 11-12: 评分引擎**
  - [ ] `domains/autocombo/engine.go`: ScoreTierRotator
  - [ ] `domains/autocombo/scoring.go`: 6 维评分
  - [ ] 单元测试: 评分计算、分层、轮换

- [ ] **Day 13: 集成到 Handler**
  - [ ] Hook 到 `domains/streaming/handler.go`
  - [ ] 解析 → 构建 → 选择 → 重写请求
  - [ ] Fallback 逻辑 (失败自动切换)

- [ ] **Day 14: Admin API**
  - [ ] `GET /admin/auto-combos`: 列出模板
  - [ ] `GET /admin/auto-combos/:name/preview`: 预览候选池
  - [ ] `POST /admin/auto-combos`: 创建自定义模板
  - [ ] `PUT /admin/auto-combos/:id`: 更新模板
  - [ ] `DELETE /admin/auto-combos/:id`: 删除模板

- [ ] **Day 15: 集成测试**
  - [ ] 端到端: `auto/free` → 路由到免费提供商
  - [ ] 配额耗尽自动切换
  - [ ] 自定义模板验证

### 交付物

```
domains/autocombo/
├── resolver.go                       (auto/* 解析器)
├── virtual_factory.go                (候选池工厂)
├── engine.go                         (评分与选择引擎)
├── scoring.go                        (评分权重)
├── types.go                          (核心类型)
├── *_test.go                         (单元测试)
└── README.md                         (模块文档)

admin/autocombos/
├── list.go                           (GET /admin/auto-combos)
├── preview.go                        (GET /admin/auto-combos/:name/preview)
├── create.go                         (POST /admin/auto-combos)
├── update.go                         (PUT /admin/auto-combos/:id)
└── delete.go                         (DELETE /admin/auto-combos/:id)
```

### 验证命令

```bash
# 1. 单元测试
go test ./domains/autocombo/... -v -race

# 2. 请求 auto/free
curl -X POST http://localhost:8781/v1/chat/completions \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "model": "auto/free",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
# 检查响应头: X-LLM-Gateway-Provider / X-LLM-Gateway-Model

# 3. 预览候选池
curl -X GET http://localhost:8781/admin/auto-combos/auto/free/preview \
  -H "Authorization: Bearer $ADMIN_TOKEN"
# 预期返回 10-20 个候选
```

---

## 🔓 Phase 4: Keyless 提供商 (Day 16-19)

### 任务清单

- [ ] **Day 16: 框架设计**
  - [ ] `provider/keyless/types.go`: KeylessProvider 接口
  - [ ] `provider/keyless/registry.go`: 注册表
  - [ ] `provider/keyless/synthetic.go`: 合成凭据处理

- [ ] **Day 17-18: OpenCode 实现**
  - [ ] `provider/keyless/opencode/client.go`: HTTP 客户端
  - [ ] `provider/keyless/opencode/auth.go`: 无认证逻辑
  - [ ] 集成测试: 直接调用 OpenCode 端点

- [ ] **Day 19: 集成到 VirtualFactory**
  - [ ] VirtualFactory 加载 keyless 候选
  - [ ] StreamExecutor 处理合成凭据
  - [ ] 端到端测试: `auto/keyless`

### 交付物

```
provider/keyless/
├── types.go                          (接口定义)
├── registry.go                       (注册表)
├── synthetic.go                      (合成凭据)
└── opencode/
    ├── client.go                     (OpenCode 客户端)
    ├── auth.go                       (无认证逻辑)
    └── client_test.go                (单元测试)
```

### 验证命令

```bash
# 1. 测试 OpenCode 直接调用
go test ./provider/keyless/opencode/... -v

# 2. 请求 auto/keyless
curl -X POST http://localhost:8781/v1/chat/completions \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "model": "auto/keyless",
    "messages": [{"role": "user", "content": "test"}]
  }'
# 检查是否路由到 OpenCode
```

---

## 📊 Phase 5: 监控与可观测 (Day 20-22)

### 任务清单

- [ ] **Day 20: Prometheus 指标**
  - [ ] `free_quota.exhausted_ratio`
  - [ ] `free_quota.429_corrections`
  - [ ] `auto_combo.requests`
  - [ ] `auto_combo.candidate_pool_size`

- [ ] **Day 21: 日志增强**
  - [ ] Auto combo 路由决策日志 (结构化)
  - [ ] 配额耗尽事件日志
  - [ ] ToS 过滤日志

- [ ] **Day 22: Grafana 仪表盘**
  - [ ] 免费资源总配额 / 使用率
  - [ ] Top 10 免费提供商 (按使用量)
  - [ ] 配额耗尽告警

### 交付物

```
internal/metrics/
└── omnifree.go                       (免费资源指标)

grafana/dashboards/
└── omnifree-overview.json            (Grafana 仪表盘)

docs/omnifree/
└── MONITORING.md                     (监控指标说明)
```

### 验证命令

```bash
# 1. 查看指标
curl http://localhost:8781/metrics | grep free_quota

# 2. 导入 Grafana 仪表盘
curl -X POST http://localhost:3000/api/dashboards/db \
  -H "Content-Type: application/json" \
  -d @grafana/dashboards/omnifree-overview.json
```

---

## 📝 Phase 6: 文档与培训 (Day 23-24)

### 任务清单

- [ ] **Day 23: 用户文档**
  - [ ] API 使用指南: 如何使用 `auto/free`
  - [ ] 免费配额说明: 各提供商限制
  - [ ] ToS 合规提示: 风险等级说明

- [ ] **Day 24: 运维文档**
  - [ ] 添加新免费提供商流程
  - [ ] ToS 审查 SOP
  - [ ] 配额异常排查手册

### 交付物

```
docs/omnifree/
├── USER-GUIDE.md                     (用户使用指南)
├── OPS-MANUAL.md                     (运维手册)
└── TOS-REVIEW-SOP.md                 (ToS 审查流程)

README.md                             (更新主 README)
```

---

## 🤖 自动化脚本

### 1. 一键部署脚本

```bash
#!/bin/bash
# scripts/deploy-omnifree.sh

set -e

echo "🚀 OmniFree 自动部署脚本"

# 1. 检查依赖
command -v psql >/dev/null 2>&1 || { echo "需要 psql"; exit 1; }
command -v go >/dev/null 2>&1 || { echo "需要 Go"; exit 1; }

# 2. 数据库迁移
echo "📊 执行数据库迁移..."
psql $DB_URL -f sql/migrations/075-omnifree-schema.sql

# 3. 导入种子数据
echo "🌱 导入种子数据..."
go run cmd/seed-free-resources/main.go \
  --db-url "$DB_URL" \
  --catalog docs/omnifree/seed/free_resource_catalog.json \
  --templates docs/omnifree/seed/auto_combo_templates.json \
  --keyless docs/omnifree/seed/keyless_providers.json

# 4. 验证数据
echo "✅ 验证数据..."
psql $DB_URL -c "
  SELECT COUNT(*) AS resources FROM free_resource_catalog WHERE enabled=TRUE;
  SELECT COUNT(*) AS templates FROM auto_combo_templates WHERE enabled=TRUE;
"

echo "🎉 OmniFree 部署完成!"
echo "📖 查看文档: docs/omnifree/00-OVERVIEW.md"
echo "🔍 测试 API: curl http://localhost:8781/v1/chat/completions -d '{\"model\":\"auto/free\",...}'"
```

### 2. 健康检查脚本

```bash
#!/bin/bash
# scripts/omnifree-healthcheck.sh

echo "🔍 OmniFree 健康检查"

# 1. 检查免费资源数量
RESOURCES=$(psql $DB_URL -tAc "
  SELECT COUNT(*) FROM free_resource_catalog WHERE enabled=TRUE;
")
echo "✅ 已启用免费资源: $RESOURCES"

# 2. 检查配额追踪
TRACKED=$(psql $DB_URL -tAc "
  SELECT COUNT(*) FROM free_quota_tracker 
  WHERE window_start >= current_date AND is_exhausted=FALSE;
")
echo "✅ 当日可用配额窗口: $TRACKED"

# 3. 检查 Auto Combo 模板
TEMPLATES=$(psql $DB_URL -tAc "
  SELECT COUNT(*) FROM auto_combo_templates WHERE enabled=TRUE;
")
echo "✅ 已启用 Auto Combo 模板: $TEMPLATES"

# 4. 测试 auto/free 路由
STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
  -X POST http://localhost:8781/v1/chat/completions \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"model":"auto/free","messages":[{"role":"user","content":"test"}],"max_tokens":10}')

if [ "$STATUS" = "200" ]; then
  echo "✅ auto/free 路由正常"
else
  echo "❌ auto/free 路由失败 (HTTP $STATUS)"
  exit 1
fi

echo "🎉 健康检查通过!"
```

---

## 📈 进度追踪

使用此 checklist 追踪实施进度：

```bash
# 克隆到本地追踪文件
cp docs/omnifree/11-IMPLEMENTATION-PLAN.md /tmp/omnifree-progress.md

# 每完成一项任务，标记为 [x]
# 每日更新进度到团队 wiki
```

---

**编写时间**: 2026-08-07  
**预计完成**: 2026-09-07 (1 个月)  
**负责人**: 待指派
