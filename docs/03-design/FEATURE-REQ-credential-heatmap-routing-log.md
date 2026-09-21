# 特性需求:凭据可观测热力图 + 路由记录 + probe-health 整合

> 状态:已实现并通过验证(2026-09-07,本地 8782 部署)
> 页面:`/routing-v2/credentials`(凭据监控)
> 关联缺陷:热力图接口 500 —— `heatmap query failed: query failed: ERROR: aggregate function calls cannot be nested (SQLSTATE 42803)`
> 实现:admin/credential_monitor_heatmap.go(重写)、admin/credential_routing_log.go(新增)、
> web/src/views/RoutingLogView.vue(新增)、web/src/views/CredentialHeatmapView.vue(增强)、
> web/src/views/probe/ProbeHealthPanel.vue(抽取)、CredentialMonitorWithTabs.vue / router.ts / appNav.ts(整合)

## 1. 背景与问题

`/routing-v2/credentials` 页面在 2026-09-06 引入了三段式 tab(列表 / 热力图 / 路由记录),其中:

1. **热力图 tab 报错**:后端 `admin/credential_monitor_heatmap.go` 的聚合 SQL 在
   `jsonb_object_agg(..., COUNT(*) ...)` 中嵌套了聚合函数,PostgreSQL 直接拒绝
   (SQLSTATE 42803)。同时排查出另外三个潜伏缺陷(见 §6)。
2. **路由记录 tab 是占位符**:仅显示"此功能正在开发中"。
3. **`/probe-health` 与本页面关系待定**:独立页面是否必要、能否整合。

本文档将原始口头需求细化、纠偏、补全为可验收的实现规格。

## 2. 原始需求 → 规格条目对照

| 原始需求(摘录) | 规格条目 |
|---|---|
| 默认显示最近一天非自检的真实使用过的凭据状态变化 | §4.1 默认视图 |
| 横坐标时间(今天/7天/本月/自定义),1 分钟粒度可设定 | §4.2 时间范围与粒度 |
| 多个模型为维度,色块显示状态,可点击查看完整状态/错误/请求 | §4.3 行维度与色块交互 |
| 通过模型选择,显示任意一个模型的所有节点及状态 | §4.4 模型视角 |
| 没有数据的直接显示空白色块 | §4.5 完整时间轴 |
| 一眼看明白,能展开看到模型对应所有节点的错误 | §4.6 分层展开 |
| 点击色块显示多个凭据详情,可修正其状态(一个或多个) | §4.7 状态修正 |
| 新增 tab:模型路由选择记录 / 自检测试成败记录,主要是状态变化记录 | §5 路由记录 tab |
| probe-health 是否有必要单独存在,是否可以整合 | §7 整合决策 |

## 3. 页面信息架构(实现后)

```
/routing-v2/credentials
├── Tab 1 列表视图      (既有 CredentialMonitorView,不变)
├── Tab 2 热力图        (CredentialHeatmapView,修复 + 增强)
├── Tab 3 路由记录      (新 RoutingLogView,替换占位符)
└── Tab 4 探测健康      (super_admin 专属,复用 ProbeHealthView 主体)
/probe-health           → 重定向 /routing-v2/credentials?tab=probe-health(兼容旧链接/菜单)
/probe-health/detail    → 保留(从探测健康 tab 的模型行点击进入)
```

## 4. 热力图(可观测)

### 4.1 默认视图

- 默认时间范围:**今天**(本地时区 00:00 起);默认粒度按时长自动推荐(≤1h→1m,≤6h→5m,≤24h→15m,≤7d→1h,更长→1d)。
- 默认排除自检流量(`exclude_self_test=true`,以 `request_context_attrs.is_probe` 判定,见 §6.2)。
- 只显示窗口内有真实调用记录的凭据(无数据的凭据不出现在列表中,与"真实使用过"一致)。

### 4.2 时间范围与粒度

- 预设:今天 / 最近1小时 / 最近6小时 / 最近24小时 / 昨天 / 最近7天 / 本月 / 自定义(起止 datetime-local)。
- 粒度:1m / 5m / 15m / 1h / 1d,可手动覆盖,与推荐值不一致时给出提示。
- 后端桶对齐必须用 epoch 取整(`to_timestamp(floor(extract(epoch from ts)/N)*N)`),禁止 `date_trunc('5 minutes')`(非法字段,见 §6.3)。

### 4.3 行维度与色块

- 第一层行:凭据(节点)。展开后第二层行:该凭据实际使用过的每个模型。
- 另提供**汇总行**:同一凭据所有模型按时间桶合并(worst-status 语义:unreachable > degraded > ready;无数据为空白),让"一眼看整体"成立。
- 色块状态色:`ready` 绿 / `degraded` 黄 / `cooling`、`rate_limited` 橙 / `unreachable` 红 / `auth_failed` 深红 / `manual_disabled` 灰 / 无数据空白。
- 状态判定:成功率 ≥0.9 → ready;≥0.5 → degraded;否则 unreachable(既有 `deriveStatusFromRate` 不变)。

### 4.4 模型视角

- 工具栏提供模型多选过滤(选项来自当前凭据列表实际出现过的模型)。
- 选择单个模型后,行=拥有该模型的凭据(节点),可横向对比同一模型在不同节点上的健康随时间变化。

### 4.5 完整时间轴(空白桶)

- 前端依据 `meta.time_start/time_end + granularity` 生成**完整桶序列**;窗口内没有请求的桶渲染为空白占位格(hover 提示"无请求"),不再出现"列数不一致、看不出空档"的问题。

### 4.6 分层展开与钻取

- 凭据行头有展开/收起按钮,展开显示逐模型行(状态持久化在 localStorage)。
- 色块 hover 显示:时间桶、请求数、成功率;点击打开详情抽屉。

### 4.7 色块详情与状态修正(可修正)

点击色块,详情抽屉显示:

1. **统计**:时间桶、凭据、模型、状态、总/成功/失败数、成功率、平均延迟、P95。
2. **错误分布**:error_kind → 次数(依赖 §6.4 的 parseJSON 修复,此前恒为空)。
3. **失败请求样本**:最多 10 条 request_id,可点击跳转请求详情。
4. **修正操作**(按凭据当前状态渲染,全部走既有接口):
   - 模型级:`toggleModelAvailability`(手动上线/下线,带原因);
   - 凭据级:`promoteCredential` / `demoteCredential`(恢复/降级);
   - `setManualDisabled` 启停。
   操作成功后提示并自动刷新热力图。单凭据单模型或多凭据场景均适用(每次操作针对当前色块对应的 credential×model,跨凭据对比时用户切换行操作即可)。

## 5. 路由记录 tab(可观测 + 可测试)

### 5.1 数据构成

新增后端聚合端点 `GET /api/credentials/routing-log`,合并三类记录为统一时间线:

| kind | 来源表 | 内容 |
|---|---|---|
| `routing` | `routing_decision_log` | 每次请求的路由选择:模型、选中凭据、tier、sticky、成败、延迟、错误类别、request_id |
| `probe` | `model_probe_runs_with_current_month` | 自检(探针)测试:status、http_status、error_code/message、latency、triggered_by |
| `state_change` | `model_probe_runs.state_change` + `routing_audit_log` 手工 toggle | 状态变化:recovered / broke / online / offline |

统一行结构:`{ts, kind, model, credential_id, credential_label, provider_name, success, status, latency_ms, error_code, error_message, request_id, tier, source, detail}`。

### 5.2 过滤与分页

- 过滤:时间范围(默认最近24小时)、类型(all/routing/probe/state_change)、模型(下拉,大小写不敏感)、结果(全部/仅失败/仅状态变化)。
- 分页:limit(默认 100,上限 500)+ offset,返回 total。
- 租户隔离:tenant_admin 只能看到本租户记录(tenant_id 过滤);super_admin 全量。

### 5.3 前端交互

- 表格列:时间 / 类型徽标 / 模型 / 凭据(供应商) / 结果 / 延迟 / 错误摘要。
- 行点击展开详情:完整错误信息、request_id(可跳转请求详情)、probe 的 http_status 与 error_code、路由的 tier/sticky/outbound_model。
- 状态变化行高亮显示(recovered 绿 / broke 红 / online/offline 蓝)。

## 6. 热力图后端缺陷修复清单(本次必须)

| # | 缺陷 | 修复 |
|---|---|---|
| 6.1 | `jsonb_object_agg(..., COUNT(*))` 聚合嵌套 → 42803 | 拆两层 CTE:先按 `(credential, model, bucket, error_kind)` 计数,再 `jsonb_object_agg(error_kind, cnt)` 聚合 |
| 6.2 | `rl.is_self_test` 列在 request_logs 中不存在(42703 潜伏) | 探测行在 request_logs 自身即有标记:`task_type='probe_triggered'` + `'probe' = ANY(quality_flags)`;实测 `request_context_attrs.is_probe` 全库为 false 且直连探测行根本没有 rca 行,join 方案会漏过滤,故不采用 |
| 6.3 | `date_trunc('5 minutes', ts)` 非法(date_trunc 不接受自定义间隔) | 统一 epoch 取整桶:`to_timestamp(floor(extract(epoch from rl.ts)/N)*N)`,N∈{60,300,900,3600,86400} |
| 6.4 | `parseJSON` 空实现,error_distribution 永远为空 | 换 `json.Unmarshal`(pgx 返回 []byte) |

验收:默认参数(今天 + 15m + 排除自检)与全部粒度(1m/5m/15m/1h/1d)调用均 200,`error_distribution` 非空时能正确解析。

## 7. probe-health 整合决策

**评估结论:可以整合,整合理由大于独立存在理由。**

- 功能重叠:凭据列表页已覆盖节点状态语义(ready/cooling/broken、probe_broken 徽标)、(凭据×模型)滑动窗口、状态历史;probe-health 独有的是系统级健康卡、四类优先级队列快照、模型健康列表、手动触发探测。
- 使用路径:probe-health 是 super_admin 专属且唯一入口在侧边菜单;整合后在凭据监控页内即可完成"看状态 → 查原因 → 修正"闭环,减少页面跳转。
- 整合方式:
  1. `ProbeHealthView.vue` 主体抽取为 `web/src/views/probe/ProbeHealthPanel.vue`(props 控制标题/返回按钮);
  2. 凭据监控页新增第 4 个 tab「探测健康」,**仅 super_admin 可见**(`isSuperAdmin()`,对齐 probe-health 既有权限);
  3. `/probe-health` 路由重定向到 `/routing-v2/credentials?tab=probe-health`,菜单入口移除,旧链接不断链;
  4. `/probe-health/detail` 保留原路径(模型行点击仍跳该详情页)。
- 回滚成本:tab 复用同一组件,若需恢复独立入口仅需还原 appNav 一行。

## 8. 验收标准(可测试)—— 2026-09-07 实测结果

- [x] AC1 热力图默认参数返回 200,31 个凭据,错误分布正确解析(如 `{"no_candidates": 1}`);四类 SQL 缺陷均消除(回归守卫 `TestBuildHeatmapSQL_GuardsAgainst42803Regression`)。
- [x] AC2 1m/5m/15m/1h/1d 五种粒度全部 200(今日窗口 78~1183 桶);5m/15m epoch 桶对齐正确。
- [x] AC3 时间轴为完整桶序列,浏览器实测无数据桶渲染为"无请求"空白虚线格;共享时间轴表头正常。
- [x] AC4 点击色块弹出详情:统计(23 请求/95.7%)、错误分布(provider_error 1 次)、失败请求样本可跳转请求详情。
- [x] AC5 详情抽屉修正操作真实到达后端:领域规则错误(auto 状态不可手动上线)正确回显;模型下线成功并恢复。
- [x] AC6 模型下拉选择后仅显示该模型行(API 实测 claude-fable-5 → 3 个节点)。
- [x] AC7 路由记录 tab:all/routing/probe/state_change 四种 kind 全部 200(24h 9273 条;6 月手工 toggle 32 条);类型/模型/结果过滤与行展开详情正常,request_id 可跳转。
- [x] AC8 tenant_admin 租户隔离:三个分支 tenant_id 过滤由单元测试锁定(`TestBuildRoutingLogSQL_TenantModelCredentialFilters`、热力图 tenant 参数测试);处理器沿用既有 IsTenantAdmin 模式。
- [x] AC9 super_admin:浏览器实测 /probe-health 302 式重定向到 `/routing-v2/credentials?tab=probe-health`,探测健康 tab 激活且系统健康卡 + P0-P3 队列完整渲染;菜单入口与 menu-config.json 已同步移除。
- [x] AC10 `go build ./... && go test ./admin/`(66s 全绿,含新增 heatmap/routing-log 测试);`npm run build` 通过。

## 9. 实施范围与不做的事

- 不改动列表视图(CredentialMonitorView)既有逻辑。
- 不新建状态历史表——复用 `model_probe_runs.state_change`、`routing_audit_log`、`route_incident_events` 现有数据。
- 不迁移 `/probe-health/detail` 的 7 个子 tab(超出本次范围,保留原样)。
- 热力图聚合缓存(响应 meta 中 cache_hit 字段)暂不启用,保持实时计算。
