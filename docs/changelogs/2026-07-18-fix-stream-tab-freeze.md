# fix(web): 修复实时请求流导致前端卡死问题

**问题**：用户在 Dashboard 切换到"实时请求流" tab 后，过一段时间浏览器会卡顿，无法点击左侧菜单，需要刷新页面才能恢复。

**根因**：
1. `DashboardViewV2.vue` 和 `TenantDashboardView.vue` 中，`LiveRequestStreamV2` 组件使用了 `v-show` 而非 `v-if`
2. 切换到其他 tab 时，组件只是 CSS 隐藏但仍然存活，SSE 连接持续接收消息
3. `liveStreamStore.ts` 每次收到消息都会触发 Vue 响应式更新，长期运行会积累大量 DOM 操作
4. 最终导致主线程卡死，UI 失去响应

**修复**：
- 将 `v-show="activeTab === 'stream'"` 改为 `v-if="activeTab === 'stream'"`
- 切换到其他 tab 时，LiveRequestStreamV2 组件会完全卸载，SSE 连接关闭，释放资源
- 切回 stream tab 时重新挂载，重新建立 SSE 连接

**影响**：
- 修复后切换 tab 会有短暂的重新加载（0.5-1s），但不会再卡死
- 用户体验大幅改善

**测试**：
```bash
# 前端构建
cd web && pnpm build

# 部署到 154
./deploy/deploy.sh
```

**关联**：
- 修改文件：
  - `web/src/views/DashboardViewV2.vue`
  - `web/src/views/TenantDashboardView.vue`
