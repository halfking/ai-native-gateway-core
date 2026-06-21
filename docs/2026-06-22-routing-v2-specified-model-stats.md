# Routing-v2 指定模型统计 (2026-06-22)

## 背景

`https://llmgo.kxpms.cn/routing-v2` 的「数据分析」Tab 当前只统计
`is_auto_request = TRUE` 的请求。客户端发送 `model: "gpt-4o"` 这种
**指定模型**请求的用量完全不在视图里，导致 KPI Bar、热力图、Sankey
流量图都只看到「自动选择」部分，与实际 gateway 流量不符。

## 目标

将指定模型请求作为一种**新任务类型**（`__specified__`，前端展示为
「指定模型」）并入既有统计，让用户能看到完整的请求用量。

## 设计

### 1. 任务类型 key 命名

| 维度 | 值 |
|------|---|
| 内部 key | `__specified__`（双下划线包裹） |
| 前端展示 | 「指定模型」 |
| 来源 | `admin/analytics.go` 的 `SpecifiedModelTaskKey` 常量 |

**为什么用双下划线**：

- 与 8 个 L1 分类器输出（`chat/reasoning/code/agent/creative/long_context/vision/function_call`）完全无交集
- 即使未来某天 L1 真的输出 `specified`，也不会冲突
- 字符串匹配可作为 filter 路由判断

### 2. SQL 模型

```sql
-- 行维度（任务类型）：auto 用 L1 分类结果，specified 折叠为 __specified__
COALESCE(NULLIF(task_type, ''),
         CASE WHEN is_auto_request THEN 'unknown' ELSE '__specified__' END)

-- 列维度（模型）：auto 用 outbound_model（重写后的），specified 用 client_model
COALESCE(NULLIF(outbound_model, ''), client_model)

-- WHERE：unified 过滤
WHERE ts >= NOW() - $1::interval
  AND (
    is_auto_request = TRUE
    OR (is_auto_request = FALSE AND client_model IS NOT NULL AND client_model <> '')
  )
```

### 3. 端点改动

| 端点 | 改动 | Funnel 呢？ |
|------|------|------------|
| `GET /api/admin/auto-route/analytics/matrix` | 移除 `is_auto_request = TRUE` 过滤，加 effective task 表达式 | — |
| `GET /api/admin/auto-route/analytics/flow` L12/L23 | 同上 | — |
| `GET /api/admin/auto-route/audit` | 移除过滤；新增 `total_requests`、`specified_model_requests` 字段 | — |
| `GET /api/admin/work-types/stats` | 移除过滤；新增 `total_specified` 字段 | — |
| `GET /api/admin/auto-route/analytics/funnel` | **保持 `is_auto_request = TRUE`** | Funnel 是 L2 凭据漏斗，specified-model 没有 L1 决策过程，强行套用没业务意义 |

### 4. 前端改动

| 组件 | 改动 |
|------|------|
| `api-autoroute.ts` | 导出 `SPECIFIED_MODEL_TASK_KEY` / `SPECIFIED_MODEL_DISPLAY_LABEL` 常量；`AutoRouteAudit` 新增 2 字段 |
| `HeatmapMatrix.vue` | `__specified__` 行加 `row-specified` class：灰色 (`#6b7280`) + 斜体 + 左侧 3px 边 |
| `AnalyticsKpiBar.vue` | KPI Bar 从 4 chip 扩到 6 chip：新增「总请求」+「指定模型」chip（后者灰色斜体） |
| `RouteFlowSankey.vue` | specified task 节点用 `#f3f4f6` 填充 + `#6b7280` 虚线边；图例翻译 |
| `RoutingDashboardView.vue` | `onMatrixCellClick` 对 `__specified__` 行不传 task 过滤（DB 中该字段为 NULL）；modal 标题显示中文标签 |

### 5. 索引

`docs/2026-06-22-explicit-model-stats.sql` 新增部分索引：

```sql
CREATE INDEX IF NOT EXISTS idx_request_logs_explicit_model
  ON request_logs (client_model, ts DESC)
  WHERE is_auto_request = FALSE
    AND client_model IS NOT NULL
    AND client_model <> '';
```

无索引时 7d 窗口的 union 查询会退化为全表扫描；该部分索引让 specified 路径走索引扫描。

### 6. 测试覆盖

`admin/analytics_specified_model_test.go`（12 个 unit tests）：

- 常量契约（双下划线 + 非空标签）
- `buildMatrixQuery` 各 metric 生成的 SQL
- `buildMatrixQuery` 拒绝非法参数
- `effectiveTaskExpr` / `effectiveModelExpr` 表达式内容
- `buildFlowL12Query` / `buildFlowL23Query` 准入 specified 分支
- `handleAudit` 字段集 sentinel（防 specified_model_requests 被误删）

## 部署

- **未部署**（按用户要求，本地构建/测试通过后等用户授权）
- 部署路径：184 k3s + 71 host docker（两实例共享同一 PG `request_logs` 表）
- 部署前 checklist：
  1. 在 184 PG 上跑 `docs/2026-06-22-explicit-model-stats.sql`（创建部分索引）
  2. build 镜像 `cd services/llm-gateway-go && ./build.sh`
  3. deploy 184：`./scripts/deploy-llm-gateway-go-184.sh`
  4. deploy 71：`./scripts/deploy-llm-gateway-go-71.sh`
  5. smoke test：`curl -H "Authorization: Bearer $ADMIN_TOKEN" https://llmgo.kxpms.cn/api/admin/auto-route/audit` 检查 `specified_model_requests` 字段

## 兼容性

- **API**：响应新增 2 个字段（`specified_model_requests`、`total_requests`），都是 optional → 旧客户端忽略即可
- **SQL**：移除的是顶层 `WHERE is_auto_request = TRUE`；funnel 端点保留旧过滤
- **索引**：仅新增，不删除；可单独回滚
- **前端**：6 chip KPI Bar 在窄屏自动回退到 2-3 列布局

## 风险

| 风险 | 缓解 |
|------|------|
| 无索引时查询慢 | `idx_request_logs_explicit_model` 部分索引 |
| `request_logs.task_type` 为 NULL 的非 auto 请求被错误归类 | WHERE 双重判断（task_type IS NOT NULL OR is_auto_request = FALSE） |
| frontend modal 点击 `__specified__` 行后查不到 decisions | `onMatrixCellClick` 对 `__specified__` 不传 task 过滤；DB 中该请求的 task_type 为 NULL，传 filter 反而查不到 |

## 相关

- 决策凭证：`docs/llm-gateway-go/auto-route-decisions.md`（如已存在）
- 多租户审计：`docs/multi-tenant-standards.md` L1=0 保持
- Funnel 决策保持原状的理由：funnel 是 L2 凭据漏斗，没有 L1 决策过程
