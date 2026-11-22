# Pull Request

## 变更类型

- [ ] Bug 修复
- [ ] 新功能
- [ ] 重构
- [ ] 文档更新
- [ ] 性能优化
- [ ] 测试改进
- [ ] 其他（请说明）

## 变更描述

<!-- 简要描述本次变更的内容和目的 -->

## 相关 Issue

<!-- 如果有关联的 Issue，请在此列出 -->

Closes #

## 测试清单

- [ ] 本地验证通过（`./verify.sh` 或 `./verify.sh --web`）
- [ ] 新增/修改的代码有对应的测试用例
- [ ] 所有测试通过

## 页面改动 Checklist（如适用）

如果本次 PR 修改了以下页面或组件，请确认是否需要复跑对应的 E2E 测试：

### 会话分析系统（Session Analytics）

修改了以下页面/组件时，**必须**复跑 `maestro/run-session-analytics.sh`：

- [ ] `/admin/session-analytics/users`（整合总览页：`SessionAnalyticsUsersView.vue`）
- [ ] `/admin/session-analytics/user-ranking`（人员排行：`SessionAnalyticsUserRankingView.vue`）
- [ ] `/admin/session-analytics/user-ranking/:owner`（人员详情：`SessionAnalyticsUserDetailView.vue`）
- [ ] `/admin/session-analytics/agent-ranking`（智能体排行：`SessionAnalyticsAgentRankingView.vue`）
- [ ] `/admin/session-analytics/model-ranking`（模型排行：`SessionAnalyticsModelRankingView.vue`）
- [ ] 会话分析共享组件（`web/src/views/admin/session-analytics/components/`）

**E2E 验证方法**：
```bash
# 前置条件：网关已部署并运行，有真实数据
BASE_URL=https://llm.itestu.cn \
ADMIN_USER=admin \
ADMIN_PASSWORD=<your-password> \
bash maestro/run-session-analytics.sh
```

**E2E 验证结果**：
- [ ] 已在本地/测试环境复跑 E2E，结果为 `RESULT: PASS`
- [ ] 无需复跑（未修改上述页面/组件）
- [ ] 需要复跑但暂未执行（请说明原因）

<!-- 
注：会话分析 E2E 包含两层验证：
1. Maestro flow 层：验证页面能打开并渲染
2. CDP 探针层：验证页面数据与后端接口一致
完整说明见 maestro/README.md §4.4
-->

### 实时拓扑/请求阶段树

修改了以下页面时，建议复跑对应的 E2E：

- [ ] `/live`（实时拓扑：`LiveTopologyView.vue`）
- [ ] `/live2`（请求阶段树：`LiveStageTreeView.vue`）

### 其他关键页面

如果修改了其他有 Maestro flow 覆盖的页面，请参考 `maestro/README.md` §3 核对是否需要复跑对应 flow。

## 部署注意事项

- [ ] 需要数据库迁移
- [ ] 需要更新配置文件
- [ ] 需要更新环境变量
- [ ] 需要重启服务
- [ ] 无特殊部署要求

## 其他说明

<!-- 任何需要 reviewer 注意的额外信息 -->

## Reviewer Checklist

- [ ] 代码符合项目规范
- [ ] 变更逻辑清晰合理
- [ ] 测试覆盖充分
- [ ] 文档更新完整（如需要）
- [ ] 页面改动 E2E 已验证（如适用）
