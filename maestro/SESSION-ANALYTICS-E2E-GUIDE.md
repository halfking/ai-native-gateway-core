# 会话分析系统 E2E 验证指南

## 概述

会话分析系统包含 5 个页面，由 `maestro/flows/16-session-analytics-system.yaml` 和 `maestro/tools/assert-session-analytics.mjs` 提供两层验证：

1. **Maestro flow 层**：验证浏览器能打开并渲染页面
2. **CDP 探针层**：验证页面数据与后端接口数值逐字对拍

## 何时需要复跑 E2E

修改以下任一项时，**必须**复跑 E2E：

### 前端页面/组件
- `web/src/views/admin/session-analytics/SessionAnalyticsOverviewView.vue`（总览）
- `web/src/views/admin/session-analytics/SessionAnalyticsUserRankingView.vue`（人员排行）
- `web/src/views/admin/session-analytics/SessionAnalyticsUserDetailView.vue`（人员详情）
- `web/src/views/admin/session-analytics/SessionAnalyticsAgentRankingView.vue`（智能体排行）
- `web/src/views/admin/session-analytics/SessionAnalyticsModelRankingView.vue`（模型排行）
- `web/src/views/admin/session-analytics/components/`（共享组件）

### 后端接口
- `admin/session_analytics/handlers.go`（7 个端点）
- `admin/session_analytics/queries.go`（查询逻辑）

### 数据库
- 迁移脚本修改了 `user_session_aggregates` 物化视图
- 修改了 `session_dim` / `request_dim` 表结构

### 路由/菜单
- 修改了会话分析相关的路由配置
- 修改了菜单标签键（`sessionAnalytics.*` 命名空间）

## 前置条件

1. **部署环境**：网关已部署并运行（本地或测试环境）
2. **真实数据**：
   - 统计窗口内有会话记录
   - 至少有 1 个 owner 的数据（用于人员详情页）
   - 物化视图已刷新（不超过 90 分钟陈旧）
3. **工具环境**：
   - Maestro CLI 2.11.0+
   - Java 17+（`JAVA_HOME` 已设置）
   - Chrome 浏览器

## 运行方法

### 完整验证（推荐）

```bash
BASE_URL=https://llm.itestu.cn \
ADMIN_USER=admin \
ADMIN_PASSWORD=<your-password> \
bash maestro/run-session-analytics.sh
```

### 只跑 flow 层（快速检查）

```bash
BASE_URL=https://llm.itestu.cn \
ADMIN_USER=admin \
ADMIN_PASSWORD=<your-password> \
maestro test maestro/flows/16-session-analytics-system.yaml \
  -e BASE_URL="$BASE_URL" \
  -e ADMIN_USER="$ADMIN_USER" \
  -e ADMIN_PASSWORD="$ADMIN_PASSWORD"
```

### 只跑 CDP 探针（数据对拍）

前提：已有有效的 JWT token

```bash
node maestro/tools/assert-session-analytics.mjs \
  --url https://llm.itestu.cn \
  --token-file /tmp/llmgw-jwt.txt
```

## 判定结果

### 三态退出码

| 退出码 | 含义 | 处置 |
|---|---|---|
| 0 | 两层全绿 | 通过 ✅ |
| 1 | 有红（产品问题） | 修产品 🔧 |
| 2 | 量具失效（窗口内无数据 ⇒ 断言未评估） | 换窗口/补数据 ⚠️ |

### 成功输出示例

```
RESULT: PASS（58 条）
  ✓ 总览页：16 项断言
  ✓ 人员排行：12 项断言
  ✓ 人员详情：14 项断言
  ✓ 智能体排行：8 项断言
  ✓ 模型排行：8 项断言
```

### 常见失败原因

1. **界面语言不是 zh-CN**
   - 症状：**断言**已不受影响（flow 层判据自 2026-11-23 L13 起改为 `data-testid`，
     与 locale 无关）；但**按钮交互**仍走 `text:`（Maestro 2.11 web 驱动选不到
     `<button>` 的 data-testid）⇒ tab 切换等交互在非中文语种下会找不到元素。
   - 解决：runner 会自动钉 `--lang=zh-CN` 并写 localStorage
   - ★ 反向代价：判据与**可见文案**脱钩 ⇒ 改文案不再让 E2E 变红；
     文案本身的正确性由 i18n 门禁（localeValueQuality 等）守。

2. **动态段 owner 为空**
   - 症状：详情页路由不存在
   - 解决：runner 会从 API 取首行 owner；窗口内无人 ⇒ 退出码 2

3. **物化视图陈旧**
   - 症状：503 + `code=analytics_view_stale`
   - 解决：等待视图刷新（≤90 分钟）或手动触发刷新

4. **数据对拍不一致**
   - 症状：页面显示与接口响应不匹配
   - 排查：检查是否有活流量导致数据变化（探针抓「页面自己收到的响应」，无容差）

## 量具设计关键点

1. **状态判据用结构类名**，不用文案：
   - `.sa-banner--error`（错误横幅）
   - `.sa-banner--stale`（陈旧横幅）
   - `.sa-loading`（加载占位）
   - `.app-empty-state`（空态）

1b. **flow 层判据用 `data-testid`，不用中文文案**（2026-11-23 L13 判据收敛）：
   - `maestro/flows/16-session-analytics-system.yaml` 的所有 `assertVisible`
     走 `id:`（= data-testid），不再断中文文案
   - 原因：文案漂移（断言值与 locale 键实际值对不上）与 locale 依赖都会让
     判据**恒红**，而页面是好的 ⇒ 量具失灵
   - 命名约定：`sa-` 前缀 + 页面/区块/列三级（`sa-page-users` / `sa-users-table`
     / `sa-users-col-rank`）
   - 对称约束由钉测守着：`web/src/views/admin/session-analytics/saTestIds.contract.spec.ts`
     —— flow 里的每个 `id:` 必须在源码里存在，且 flow 里不得再出现中文文案断言

2. **对拍基准同源**：
   - 抓页面自己收到的 API 响应（CDP `Network.getResponseBody`）
   - 不是探针另发一次请求（活流量下必假红）

3. **三层互斥判定**：
   - 有错误横幅 ⇒ 产品红
   - 无错误横幅 + 有数据 ⇒ 数据对拍
   - 无错误横幅 + 无数据 ⇒ 空态对

## 文档参考

- **完整说明**：`maestro/README.md` §4.4 和 §4.5
- **审计报告**：`docs/audit/2026-11-20-会话分析系统前端实施报告.md` §7.8
- **量具自测**：`maestro/tools/selftest.mjs`（验证 result.mjs 判定口径）

## CI 集成状态

- **当前**：E2E 未接入自动化 CI（需要真实部署环境和数据）
- **要求**：修改相关页面时，在 PR 中勾选「已复跑 E2E」checklist
- **未来**：考虑在测试环境部署后自动触发（需要解决环境依赖和运行时长问题）

## 联系方式

如有疑问或 E2E 执行失败，请：
1. 检查上述常见失败原因
2. 查看完整日志输出
3. 在 PR 中说明具体失败现象
