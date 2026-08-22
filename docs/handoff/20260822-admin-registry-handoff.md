# Session Handoff: Admin 注册表视图 — 154 部署与审计后续

## 1. 任务概要

完成请求注册表 / 连接注册台的设计文档、真实 API 接入、154 部署（不含 245）、审计修正与 main 合并。

## 2. 当前方案

- 设计 SSOT：`docs/03-design/02-feature-design/design/admin-registry-views.md`
- 154 生产：**build_seq=1674**，`git_sha=a4907f37`
- 245：**未部署**

## 3. 任务进度

- ✅ 设计文档 + 前端真实 API + request_id 搜索
- ✅ 154 部署 build 1673 → 审计 → 修正 closed=null → 1674 再部署
- ✅ API 验证：`closed:[]`，request-journeys queues 100/100
- ✅ 合并 `origin/main` @ `3004a6817`（含 dashboard SQL view 扩展、back-to-list 按钮）
- ⏳ 浏览器 super_admin 实测（本环境 browser MCP 不可用）
- ⏳ node-health API 替代 mock 恢复时间线

## 4. 当前状态

- 分支：`feature/dashboard-stats-refactor`（与 main 同步 @ 3004a6817）
- 154：`https://llm.kxpms.cn` build 1674
- 代码快照：`3004a6817` pushed to `origin/main`

## 5. 下一步

1. 浏览器登录 super_admin，实测两页搜索 + 旅程跳转
2. 实现 `GET /api/admin/node-health/{credential_id}/timeline`（替换 mock）
3. 继续 `feature/dashboard-stats-refactor` 前端统计面板

## 6. 关键事实

- connection-registry 仅进程内，无 PG 持久化
- request-registry 列表热窗口 ~100；按 ID 查详情可回落 PG
- 审计报告：`docs/audit/2026-08-22-admin-registry-154-deploy-audit.md`

## 7. 阻塞 / 风险

- 无 live 流式连接时连接注册台列表为空属正常
- 多副本部署时 connection-registry 每实例独立

## 8. 建议 Skills

- `deploy-154`（仅 154，禁止 245）
- `browser-use` / `e2e-testing`
- `handoff`
