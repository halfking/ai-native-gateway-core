# 凭据监控热力图 - 部署与测试指南

## 📦 已完成的交付物

### 1. 后端实现
- ✅ **API 端点**: `GET /api/credentials/heatmap`
  - 文件: `admin/credential_monitor_heatmap.go`
  - 路由注册: `admin/credential_monitor.go:148`
  - 功能: 时间序列聚合、多粒度支持、错误分布、失败采样

- ✅ **数据库索引**
  - 文件: `migrations/035_add_heatmap_indexes.sql`
  - 3个索引: 核心索引、覆盖索引、租户索引
  - 优化查询性能 (预期 < 1000ms)

- ✅ **API 测试脚本**
  - 文件: `test-heatmap-api.sh`
  - 5个测试场景: 基础查询、粒度切换、结构验证、错误处理

### 2. 前端实现
- ✅ **热力图视图组件**
  - 文件: `web/src/views/CredentialHeatmapView.vue` (1371 行，单文件实现表格网格 / 详情抽屉 / 状态修正)
  - 组件依赖仅两个：`components/ActiveFilterChips.vue`（已选条件 chips）、
    `components/ui/KxDateRangePicker.vue`（时间范围选择器）
  - 功能:
    - 时间范围选择器 (今天/最近1小时/最近6小时/最近24小时/昨天/最近7天/本月/自定义)
    - 粒度切换 (1m/5m/15m/1h/1d，按时间范围自动建议：≤1h→1m，≤6h→5m，≤24h→15m，≤168h→1h，否则 1d)
    - 筛选器 (排除自检——默认勾选、仅显示异常、模型过滤；默认折叠为 chips 展示)
    - 自动刷新 (10/30/60秒)
    - 表格形式热力图 (横向时间座标列，各行色块按列对齐)
    - 凭据展开/收起 (localStorage 持久化，单一 key: `credential_heatmap_expanded`，值为展开的凭据 ID 数组)
    - 色块详情抽屉 (统计、错误分布、失败请求样本、状态修正操作: 模型上下线/凭据恢复/凭据降级/凭据启用/凭据停用)
    - ★ 状态修正的 5 个动作后端全部要求 super_admin
      （`RequireSuperAdminForWrite` / `h.superAdmin`）⇒ 前端对非 super_admin
      禁用这些按钮并给出只读提示，见「写侧角色门」小节

- ✅ **Tab 结构包装器**
  - 文件: `web/src/views/CredentialMonitorWithTabs.vue`
  - 功能: **五个** Tab 切换: `列表视图` / `热力图` / `路由记录` / `错误趋势` /
    `探测健康`（`探测健康` **仅 super_admin 可见**；`错误趋势` 对 tenant_admin 开放，
    `/api/errors/trend` 服务端按租户收口）

- ✅ **API 接口定义**
  - 文件: `web/src/api/credential-monitor.ts` (609 行)
  - TypeScript 类型定义完整
  - 已导出到 `web/src/api.ts`（`export * from './api/credential-monitor'`）

- ✅ **路由集成**
  - 文件: `web/src/router.ts:222`
  - `/routing-v2/credentials` 路径更新为使用 `CredentialMonitorWithTabs`

### 3. 写侧角色门（2026-10-20 落地订正）

「状态修正」的 5 个动作打到的后端端点**全部要求 super_admin**
（`admin/credential_monitor.go` 的 `RequireSuperAdminForWrite` /
`model-toggle` 的 `h.superAdmin`）—— tenant_admin 是**只读**档
（`admin/handler.go:942`：「default 租户 tenant_admin 获得只读 + 凭据 API Key 轮换」）。

| 动作 | 端点 | 方法 |
|---|---|---|
| 模型上线 / 模型下线 | `/api/credentials/model-toggle` | POST |
| 恢复凭据 | `/api/credentials/promote` | POST |
| 降级凭据 | `/api/credentials/demote` | POST |
| 启用凭据 / 停用凭据 | `/api/credentials/set-manual-disabled` | POST |

⇒ 非 super_admin 时，这些按钮**被禁用**并显示只读提示；函数层还有第二道守卫
（防按钮态被绕过）。后端那道门是 2026-10-19 补上的，前端此前未同步，
tenant_admin 会看到可点的写按钮、点下去只拿到 403。

### 4. 文档
- ✅ **需求规格**: `docs/design/credential-monitor-heatmap-requirements.md` (500+ 行)
- ✅ **实施指南**: `docs/design/credential-monitor-heatmap-implementation.md` (600+ 行)
- ✅ **部署指南**: 本文档

---

## 🚀 部署步骤

### 步骤 1: 数据库迁移

```bash
# 连接到数据库
psql -h <host> -U <user> -d llm_gateway

# 执行索引创建脚本
\i migrations/035_add_heatmap_indexes.sql

# 验证索引已创建
\d request_logs

# 应该看到以下索引:
# - idx_request_logs_heatmap_core
# - idx_request_logs_heatmap_covering
# - idx_request_logs_tenant_heatmap
```

**预期结果**:
- 3个索引创建成功
- `ANALYZE request_logs` 完成统计更新

**回滚**:
```sql
DROP INDEX CONCURRENTLY IF EXISTS idx_request_logs_heatmap_core;
DROP INDEX CONCURRENTLY IF EXISTS idx_request_logs_heatmap_covering;
DROP INDEX CONCURRENTLY IF EXISTS idx_request_logs_tenant_heatmap;
```

---

### 步骤 2: 后端部署

```bash
# 1. 构建 Go 服务
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
make build

# 2. 运行单元测试 (如果已实现)
# go test ./admin -run TestCredentialHeatmap -v

# 3. 启动服务 (开发环境)
./llm-gateway-go

# 4. 验证路由已注册
curl http://localhost:8782/api/credentials/heatmap?time_start=2026-09-06T00:00:00Z&time_end=2026-09-06T01:00:00Z&granularity=5m

# 应该返回 200 OK 和 JSON 响应
```

**健康检查**:
```bash
# 检查服务是否启动
curl http://localhost:8782/api/system/health

# 检查路由是否响应
curl -I http://localhost:8782/api/credentials/heatmap
# 应该返回 400 (缺少参数) 而不是 404 (路由不存在)
```

---

### 步骤 3: 前端部署

```bash
# 1. 安装依赖
cd web
pnpm install

# 2. 开发模式启动 (本地测试)
pnpm dev

# 3. 构建生产版本
pnpm build

# 4. 验证构建产物
ls -lh dist/
```

**访问测试**:
1. 打开浏览器: `http://localhost:5173/routing-v2/credentials`
2. 应该看到五个Tab: `列表视图` / `热力图` / `路由记录` / `错误趋势` / `探测健康`
   （`探测健康` 仅 super_admin 可见）
3. 点击 `热力图` Tab
4. 应该看到时间范围选择器、粒度选择器、筛选器
5. 点击 `刷新` 按钮加载数据

---

## 🧪 测试验证

### 测试 1: 后端 API 测试

运行自动化测试脚本:

```bash
./test-heatmap-api.sh
```

**预期输出**:
```
Testing Credential Heatmap API at http://localhost:8782
============================================

Test 1: Basic heatmap query (last 1 hour, 1m granularity)
-----------------------------------------------------------
{
  "meta": {
    "time_start": "2026-09-06T08:00:00Z",
    "time_end": "2026-09-06T09:00:00Z",
    "granularity": "1m",
    "bucket_count": 60,
    "cache_hit": false,
    "generated_at": "2026-09-06T09:05:00Z",
    "expires_at": "2026-09-06T09:06:00Z",
    "duration_ms": 234
  },
  "credentials": [...]
}

Test 2: Heatmap with 5m granularity
------------------------------------
✓ meta.time_start present
✓ meta.granularity present

Test 3: Check response structure
---------------------------------
✓ meta.time_start present
✓ meta.granularity present
✓ credentials array present
✓ Found 5 credentials
✓ credentials[0].credential_id present
✓ credentials[0].models present
✓ First credential has 3 models
✓ model.raw_model_name present
✓ model.buckets present
✓ First model has 12 time buckets

Test 4: Invalid granularity (should fail)
------------------------------------------
✓ Returns 400 for invalid granularity

Test 5: Missing time_start (should fail)
-----------------------------------------
✓ Returns 400 for missing time_start

============================================
All tests completed!
```

**如果测试失败**:
1. 检查数据库索引是否创建成功
2. 检查 `request_logs` 表是否有数据
3. 检查服务日志: `tail -f logs/llm-gateway.log`

---

### 测试 2: 前端集成测试

**手动测试清单**:

#### 2.1 基础功能
- [ ] 访问 `/routing-v2/credentials`，页面正常加载
- [ ] 看到五个Tab: 列表视图、热力图、路由记录、错误趋势、探测健康（探测健康仅 super_admin 可见）
- [ ] 点击 `热力图` Tab，切换成功
- [ ] 默认显示 `今天` 的数据
- [ ] 粒度默认为 `5m` 或自动推荐值

#### 2.2 时间范围切换
- [ ] 选择 `最近1小时`，数据重新加载
- [ ] 选择 `最近24小时`，粒度自动建议切换到 `15m`
- [ ] 选择 `最近7天`，粒度自动建议切换到 `1h`
- [ ] 选择 `自定义`，显示日期时间选择器
- [ ] 输入自定义时间范围，点击刷新，数据正确加载

#### 2.3 粒度切换
- [ ] 手动切换粒度为 `1m`，数据重新加载
- [ ] 手动切换粒度为 `1h`，色块宽度变化
- [ ] 粒度与时间范围不匹配时，显示建议提示

#### 2.4 筛选功能
- [ ] 勾选 `排除自检`，数据重新加载（默认已勾选）
- [ ] 取消 `排除自检`，看到自检数据
- [ ] 勾选 `仅显示异常`，只显示有错误的凭据

#### 2.5 热力图渲染
- [ ] 看到凭据列表，每个凭据有一个汇总行
- [ ] 点击凭据左侧的 `▶` 按钮，展开显示模型行
- [ ] 再次点击 `▼` 按钮，收起模型行
- [ ] 刷新页面，展开状态被保留（localStorage）
- [ ] 色块颜色正确:
  - 绿色 (#10b981) = ready (成功率 ≥ 90%)
  - 黄色 (#f59e0b) = degraded (成功率 50-90%)
  - 红色 (#ef4444) = unreachable (成功率 < 50%)
  - 浅灰 (#e5e7eb) = no_data (无数据)

#### 2.6 交互功能
- [ ] 鼠标悬停色块，显示 tooltip (时间 + 状态)
- [ ] 点击色块，打开详情弹窗
- [ ] 详情弹窗显示:
  - 时间范围
  - 凭据信息
  - 模型名称
  - 状态badge
  - 统计指标 (总请求/成功/失败/成功率/延迟)
  - 错误分布 (每种错误的次数)
  - 失败请求样本 (最多10条，可点击跳转)
- [ ] 点击失败请求ID，跳转到 `/request-detail/:id`
- [ ] 点击弹窗外部或 `关闭` 按钮，弹窗关闭

#### 2.7 自动刷新
- [ ] 勾选 `自动刷新`，开始倒计时
- [ ] 选择刷新间隔 `10秒`，10秒后自动刷新
- [ ] 取消 `自动刷新`，停止自动刷新
- [ ] 点击 `刷新` 按钮，立即手动刷新

#### 2.8 性能测试
- [ ] 加载 `最近7天` + `1m粒度` (10080个时间桶)，页面不卡顿
- [ ] 展开10个凭据 × 10个模型，滚动流畅
- [ ] 快速切换时间范围，无明显延迟
- [ ] 自动刷新时，页面不闪烁，滚动位置不变

---

### 测试 3: 边界条件测试

#### 3.1 空数据
- [ ] 选择未来时间范围，显示 "暂无数据"
- [ ] 选择没有请求的凭据，显示空白色块

#### 3.2 错误处理
- [ ] 服务器返回 500，显示错误提示
- [ ] 网络超时，显示加载失败
- [ ] 无效时间范围，前端校验提示

#### 3.3 权限测试
- [ ] `super_admin` 可以看到所有租户的凭据
- [ ] `tenant_admin` 只能看到自己租户的凭据
- [ ] 未登录用户无法访问

---

## 📊 性能基准

### 后端 API 性能目标

| 场景 | 时间范围 | 粒度 | 时间桶数 | 目标响应时间 |
|------|---------|------|---------|-------------|
| 轻量 | 1小时 | 1m | 60 | < 200ms |
| 中等 | 24小时 | 5m | 288 | < 500ms |
| 重型 | 7天 | 15m | 672 | < 1000ms |
| 极限 | 7天 | 1m | 10080 | < 5000ms |

### 前端渲染性能目标

| 指标 | 目标值 |
|------|--------|
| 首次加载 (FCP) | < 1s |
| 交互就绪 (TTI) | < 2s |
| 色块渲染 (1000个) | < 500ms |
| 展开/收起动画 | < 200ms |
| 自动刷新延迟 | < 100ms |

---

## 🔍 查询参数语义（2026-10-17 补）

本节按 `admin/credential_monitor_heatmap.go` 当前实现补齐，
补的是代码里早已存在、但本文档此前完全未记载的两个行为。

### 6.1 `exclude_self_test` —— 缺省为 `true`

`admin/credential_monitor_heatmap.go:106-110`：

```go
// exclude_self_test 缺省 true（与 FEATURE-REQ §4.2 一致）
excludeSelfTest := true
if r.URL.Query().Has("exclude_self_test") {
    excludeSelfTest = queryBool(r, "exclude_self_test")
}
```

**运维要点**：这是 `Has()` + 显式覆盖的写法，不是 `Get()=="true"`。
因此：

| 请求形式 | 结果 |
|---|---|
| 不带该参数 | `exclude_self_test=true`（排除自测流量） |
| `?exclude_self_test=` （空值） | 走 `queryBool` 解析，**不要假设等于 true** |
| `?exclude_self_test=false` | `false`，自测流量计入热图 |

验收时若发现热图里混进自测流量，先确认调用方传的是显式 `false`
还是根本没传——两者行为不同。

### 6.2 分桶是 epoch 对齐的

`admin/credential_monitor_heatmap.go:457`、`:667`：

> Buckets are epoch-aligned via `to_timestamp(floor(epoch/N)*N)`

含义：桶边界按 Unix epoch 绝对对齐，**与请求的 `time_start` 无关**。
推论三条：

1. `granularity` 取任意整 N 秒都成立，不需要能整除时间跨度；
2. 桶标签反映的是绝对时间点，不是「从 `time_start` 起第 k 桶」；
3. 两次相同参数请求的桶边界重合，可直接逐桶对比（这也是 `cache_hit`
   语义讨论的前提）。

排查「热图整体偏移了几十分钟」类问题时，先排除把 epoch 桶标签
误读成相对偏移。

### 6.3 `meta.cache_hit` 恒为 `false`（刻意，非缺陷）

`admin/credential_monitor_heatmap.go:235-236`：

```go
// cache_hit 现由缓存层控制；无缓存层时恒为 false（实时查询）
resp.Meta.CacheHit = false
```

当前端点**自身不做缓存**，每次都是实时查询，因此该字段恒为 `false`。
观测侧不要据此判定「缓存失效」；`meta.expires_at`（+1min）只是
响应头语义，不代表本端点会自行复用。

> 历史背景：2026-09-07 曾有一版分支引入「1min 进程内缓存 +
> singleflight」让该字段变真。因与当前 stale-while-revalidate 的
> 缓存层方向冲突、且会让该字段含义从「缓存层命中」漂移成
> 「进程内命中」，该实现**未并入**，判定见
> `docs/audit/2026-10-17-24h-audit-branch-adjudication.md` §三。

---

## 🐛 故障排查

### 问题 1: API 返回 404

**症状**: `curl /api/credentials/heatmap` 返回 404

**原因**: 路由未注册或服务未重启

**解决**:
```bash
# 1. 检查代码
grep "handleCredentialHeatmap" admin/credential_monitor.go

# 2. 重新编译
make build

# 3. 重启服务
pkill llm-gateway-go
./llm-gateway-go
```

---

### 问题 2: API 返回空数据

**症状**: `credentials: []`

**原因**: 数据库无数据或时间范围不对

**解决**:
```sql
-- 检查 request_logs 表是否有数据
SELECT COUNT(*), MIN(ts), MAX(ts) FROM request_logs WHERE ts >= NOW() - INTERVAL '1 day';

-- 检查是否有非自检数据
-- ★ 探针/自检排除谓词与后端共用同一份拼写（bg.ProbeTrafficExclusionPredicateView），
--   读的是**视图** request_logs_with_current_month 的列词汇，不是物理表。
--   三个臂都必须带 COALESCE：缺 COALESCE 时 `'probe' = ANY(NULL)` 为 NULL，
--   `NOT NULL` 不为 TRUE ⇒ 那批行会被**静默丢掉**
--   （2026-10-02 实测：7d 内会话族 40,275 行里有 40,225 行被吞）。
--   ⚠ request_logs 无 `is_self_test` 列 —— 那是旧说法，别再照抄。
SELECT COUNT(*) FROM request_logs_with_current_month
WHERE ts >= NOW() - INTERVAL '1 day'
  AND NOT COALESCE('probe' = ANY(quality_flags), FALSE)
  AND COALESCE(task_type, '') <> 'probe_triggered'
  AND COALESCE(origin_actor, '') NOT IN (
        'node-probe-worker', 'active-probe-worker',
        'probe-service', 'credential-selfcheck-worker');

-- 查看凭据分布
SELECT credential_id, COUNT(*) FROM request_logs WHERE ts >= NOW() - INTERVAL '1 day' GROUP BY credential_id;
```

---

### 问题 3: API 查询慢 (> 5s)

**症状**: API 响应时间 > 5秒

**原因**: 索引未创建或未生效

**解决**:
```sql
-- 检查索引是否存在
\d request_logs

-- 检查索引使用情况
-- ★ 分桶用 epoch 对齐而不是 date_trunc：date_trunc 只接受固定字段名
--   （'minute'/'hour'/'day'），不接受 '5 minutes' 这类参数化步长。
--   后端实际拼写（admin/credential_monitor_heatmap.go）：
--   to_timestamp(floor(EXTRACT(EPOCH FROM rl.ts) / <步长秒>) * <步长秒>)
EXPLAIN ANALYZE
SELECT credential_id, lower(COALESCE(outbound_model, client_model)) AS raw_model_name,
       to_timestamp(floor(EXTRACT(EPOCH FROM ts) / 60) * 60) AS time_bucket,
       COUNT(*) AS total_requests
FROM request_logs_with_current_month
WHERE ts >= NOW() - INTERVAL '1 day' AND ts < NOW()
  AND NOT COALESCE('probe' = ANY(quality_flags), FALSE)
  AND COALESCE(task_type, '') <> 'probe_triggered'
  AND COALESCE(origin_actor, '') NOT IN (
        'node-probe-worker', 'active-probe-worker',
        'probe-service', 'credential-selfcheck-worker')
GROUP BY credential_id, raw_model_name, time_bucket;

-- 应该看到 "Index Scan" 而不是 "Seq Scan"
```

---

### 问题 4: 前端无法加载

**症状**: 点击 `热力图` Tab 白屏

**原因**: 
1. API 未响应
2. 前端 import 路径错误
3. TypeScript 类型错误

**解决**:
```bash
# 1. 检查浏览器控制台错误
# 打开 DevTools -> Console

# 2. 检查网络请求
# 打开 DevTools -> Network -> 查看 /api/credentials/heatmap 请求

# 3. 检查前端编译错误
cd web
pnpm build
# 应该无错误或警告

# 4. 检查 API 导出
grep "getCredentialHeatmap" web/src/api/credential-monitor.ts
grep "credential-monitor" web/src/api/index.ts
```

---

### 问题 5: 色块不显示

**症状**: 页面加载成功但看不到色块

**原因**: 
1. 数据结构不匹配
2. CSS 样式问题
3. buckets 数组为空

**解决**:
1. 打开浏览器 DevTools -> Console，查看是否有错误
2. 打开 Vue DevTools，检查 `heatmapData` 的值
3. 确认 API 响应包含 `buckets` 数组
4. 检查 `.heatmap-cell` 的 CSS 样式是否生效

---

## 📝 后续优化

### P1 - 高优先级
- [ ] 实现路由记录 Tab (合并 `routing_decision_log` + `model_probe_runs`)
- [ ] 添加状态修正操作 (在详情弹窗中集成现有 API)
- [ ] 实现 Redis 缓存 (1分钟 TTL)

### P2 - 中优先级
- [ ] 虚拟滚动 (支持 100+ 凭据)
- [ ] Canvas 渲染 (支持 10000+ 色块)
- [ ] 增量更新 (自动刷新仅加载新数据)
- [ ] 导出功能 (PNG 图片 / CSV 数据)

### P3 - 低优先级
- [ ] 高级筛选 (按错误类型、成功率阈值)
- [ ] 对比模式 (多个凭据并排对比)
- [ ] 异常检测 (自动标记异常时间段)
- [ ] AI 分析 (LLM 生成故障诊断报告)

---

## 🎯 验收标准

完整功能上线需要满足以下标准:

### 必须项 (Must Have)
- [x] 后端 API 实现并测试通过
- [x] 数据库索引创建并优化生效
- [x] 前端热力图组件开发完成
- [x] Tab 结构集成到现有页面
- [ ] 至少5个真实凭据的热力图正常显示
- [ ] API 响应时间 P95 < 2000ms
- [ ] 前端渲染时间 < 1000ms (1000个色块)
- [ ] 无控制台错误或警告

### 应该项 (Should Have)
- [ ] 自动刷新功能稳定运行
- [ ] localStorage 持久化展开状态
- [ ] 详情弹窗完整显示错误信息
- [ ] 失败请求 ID 可点击跳转
- [ ] 租户隔离验证通过

### 可选项 (Nice to Have)
- [ ] 路由记录 Tab 实现
- [ ] 状态修正操作集成
- [ ] 虚拟滚动优化
- [ ] Redis 缓存优化

---

## 📞 联系方式

- **开发**: ZCode Agent
- **问题反馈**: GitHub Issues / 内部工单系统
- **文档**: `docs/credential-monitor-heatmap-*.md`

---

**最后更新**: 2026-09-06  
**版本**: v1.0  
**状态**: 开发完成，待测试验证
