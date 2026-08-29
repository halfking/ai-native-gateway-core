# Dashboard 前端生命周期修复审计报告

## 原始问题
- 用户反馈：访问 https://llm.kxpms.cn/dashboard 后页面卡死/无限刷新/请求循环
- 审计范围：前端 Dashboard、实时流、轮询、事件订阅的并发、生命周期与资源泄漏

## 审计发现（优先级排序）

### 高优先级（已修复）

1. **App.vue Maintain 监听器泄漏** (`web/src/App.vue:60-68`)
   - 问题：每次挂载注册监听器，从不清理
   - 根因：`onMaintainAvailabilityChange()` 返回的取消函数被丢弃
   - 修复：保存 `stopMaintainAvailabilityWatch` 并在 `onUnmounted` 调用

2. **liveStreamStore SSE 异常路径 resize 泄漏** (`web/src/composables/liveStreamStore.ts:1258-1287`)
   - 问题：EventSource 不支持或构造失败时，resize listener 未移除
   - 根因：先注册 resize，后检查 EventSource；异常返回路径不清理
   - 修复：检查 EventSource 后再注册；closeConnection 幂等清理

3. **liveStreamStore 旧连接事件污染新连接** (`web/src/composables/liveStreamStore.ts:1291-1322`)
   - 问题：重连后，旧 EventSource 的延迟 open/error/message 仍可能更新全局状态
   - 根因：事件 handler 直接操作模块级 `liveStreamState.connection`
   - 修复：增加 `connectionGeneration`，handler 校验 `es === connection && generation === currentGeneration`

4. **liveStreamStore.reconnectStream 无消费者门禁** (`web/src/composables/liveStreamStore.ts:1407-1410`)
   - 问题：`refCount = 0` 时仍可能创建 orphan EventSource
   - 修复：`if (refCount <= 0) return`

5. **useDashboardBoard 异步启动竞态** (`web/src/composables/useDashboardBoard.ts:208-222`)
   - 问题：`startAutoRefresh()` 是 async，await 期间可能被多次调用；旧调用回来后重新挂载 timer/listener
   - 根因：无 generation/active 标记
   - 修复：增加 `refreshGeneration` 和 `refreshActive`，await 后校验

6. **useDashboardBoard 请求竞态** (`web/src/composables/useDashboardBoard.ts:124-149`)
   - 问题：只有 `loadInFlight` 布尔；旧时间范围响应可覆盖新请求
   - 修复：增加 `loadGeneration` + `AbortController`；只有最新请求可写回状态

### 中优先级（未修复，建议后续处理）

7. **useDashboard 并发与生命周期** (`web/src/composables/useDashboard.ts:97-183`)
   - 问题：7 路 Promise.allSettled 无序列/取消；多实例重复请求
   - 建议：增加请求序列或 AbortController；EnhancedDashboardView/SessionStatsPanel 共享 store

8. **useNodeDetailDrawerLoad 永不取消的 AbortController** (`web/src/composables/useNodeDetailDrawerLoad.ts:152`)
   - 问题：`new AbortController().signal` 未保存，无法取消
   - 建议：保存 controller 并在关闭/卸载时 abort

9. **useNodeDetailDrawerLoad 20ms interval 等待** (`web/src/composables/useNodeDetailDrawerLoad.ts:164-167`)
   - 问题：轮询等待 `detailLoading`，异常路径可能永久运行
   - 建议：改用共享 Promise 或增加超时

10. **RoutingDashboardView 5 秒轮询重叠** (`web/src/views/RoutingDashboardView.vue:391-404`)
    - 问题：无 inFlight 门控，慢接口会重叠请求；切 tab 不取消
    - 建议：增加 inFlight/AbortController；切 tab 时取消旧请求

11. **SystemStatusIndicator/SystemHealthBadge 重叠请求** (`web/src/components/SystemStatusIndicator.vue:163-176`)
    - 问题：无 hidden 判断、inFlight、AbortController
    - 建议：增加并发保护

12. **多个组件 setTimeout 未清理** 
    - 位置：DashboardFilterBar.vue:335-347、SelfCheckPanel.vue:186-196、SystemMonitorPanel.vue:326-331
    - 问题：卸载后 timer 仍执行
    - 建议：保存 timer 并在 onUnmounted 清理

### 低优先级（设计/文档问题）

13. **usePolling watch source 行为** (`web/src/composables/usePolling.ts:69`)
    - 问题：source 变化后永久 stop，无 restart
    - 建议：明确 API 文档或修正逻辑

14. **RequestRegistryView 浅 watch actions Map** (`web/src/views/RequestRegistryView.vue:337-346`)
    - 问题：liveStreamStore 原地赋值数组元素，浅 watch 可能漏更新
    - 建议：改用 deep watch 或监听 revision ref

15. **liveStreamStore/probeStreamStore 模块级 visibility listener** (`web/src/composables/liveStreamStore.ts:289-317`)
    - 问题：匿名 handler 永不移除（单一 bundle 通常无害，HMR/测试会泄漏）
    - 建议：保存 handler 并暴露清理函数

### 线上观察（非代码问题）

16. **/maintain-api/healthz 返回 HTML 而非 JSON**
    - 问题：前端判定 Maintain 不可用
    - 根因：Nginx 路由或 Maintain 部署缺失
    - 建议：检查反向代理配置

17. **未登录访问受 401 限制**
    - 问题：无法观察登录态下的真实 Network/SSE 行为
    - 建议：线上验证时提供测试凭据

## 修复覆盖范围

### 已修复（本次提交）
- ✅ App Maintain 监听器泄漏
- ✅ liveStreamStore SSE 异常路径 resize 泄漏
- ✅ liveStreamStore 连接 generation 隔离
- ✅ liveStreamStore reconnectStream 门禁
- ✅ useDashboardBoard 异步启动 generation
- ✅ useDashboardBoard 请求 generation + AbortController
- ✅ API board.ts 支持 AbortSignal

### 未修复（不在 Dashboard 主链路）
- ⏸️ useDashboard 并发（EnhancedDashboardView、SessionStatsPanel 使用）
- ⏸️ useNodeDetailDrawerLoad（节点详情抽屉）
- ⏸️ RoutingDashboardView（/routing-v2，非默认 /dashboard）
- ⏸️ SystemStatus 组件重叠请求
- ⏸️ 各组件零散 setTimeout 泄漏

## 测试验证

### 通过
- ✅ pnpm typecheck
- ✅ Dashboard 针对性测试 15 项全通过
- ✅ pnpm build 生产构建
- ✅ pre-commit checks 6 项全通过

### 被既有问题阻断
- ⚠️ pnpm test 全量测试：i18n parity 缺失 `nav.item.proxy`（ar-SA/de-DE/es-ES/fr-FR/ja-JP/zh-TW），与本次无关

## 未发现问题
- ❌ 同步锁死锁
- ❌ Dashboard 主路径无限整页刷新
- ❌ 无限增长数据结构（已有 cap 保护）

## 提交信息
- 分支（干净）：`fix/dashboard-lifecycle-clean` (1c5bc6b99)
- 分支（混合）：`fix/dashboard-lifecycle-races` (be96705cc，包含 7 个非前端文件)
- main 当前：87db19b97（与混合分支相同，已推送但未 PR）

## 建议后续工作
1. 优先：补充 useDashboard、useNodeDetailDrawerLoad、RoutingDashboardView 的生命周期保护
2. 中等：清理零散 setTimeout 泄漏
3. 低优：统一 visibility/AbortController 模式到 QueuePerspectivePanel 标准
4. 文档：为 usePolling 明确 source watch 行为
5. 线上：修复 /maintain-api/healthz 返回 HTML 问题

## 审计结论
本次修复覆盖了 Dashboard 主链路（/dashboard 默认 stream tab + board tab）的核心生命周期与异步竞态问题，消除了确定性监听器泄漏、SSE 重连污染、Board 请求覆盖等高优先级风险。未修复的项目多数不在默认 Dashboard 主路径，建议分批迭代处理。
