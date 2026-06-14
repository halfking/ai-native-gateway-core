# UMB (Ultra Memory Bank) — llm-gateway-go v2.0

> **实时跟踪**：当前任务进度、关键决策、待办、已交付
> **最后更新**：2026-06-14

## 🎯 当前任务

**目标**：llm-gateway-go v2.0.0 — Auto 路由模式 + 24h 审计修正

## 📍 当前阶段

**阶段 1.0**：初始化 ✅
**阶段 1.1**：审计最近 24h commit → 进行中
**阶段 1.2**：写审计报告 → 待办
**阶段 2-4**：实现 + 测试 + tag → 待办

## 🔑 关键决策（老板拍板）

1. **Auto 触发**：显式 `model="auto"`，client 不传 model 走 env 兜底（0 破坏）
2. **分类方式**：启发式 + LLM fallback（confidence < 0.7 才调 LLM）
3. **Profile 默认**：smart 默认，三选一透传，sticky 30min
4. **审计范围**：最近 24h（commit since 2026-06-13）
5. **测试部署范围**：仅 184 k3s（71/252/245 不动）

## 📋 阶段 1.1 审计 commit 清单

| commit | 摘要 | 类型 | 风险等级 |
|--------|------|------|---------|
| a8ae6914 | style: 趋势图细线细字 | UI | 低 |
| a235f43c | fix: 趋势窗口选中态 | UI | 低 |
| 66a858fb | fix: request log provider_model | 后端 | 中 |
| b4ad7fec | fix: route line second row | 后端 | 中 |
| 4cfc3d51 | fix: 趋势图标签缩小 | 后端 | 中 |
| cdf729a9 | fix: backfill key meta | 后端 | 中 |
| ab133242 | feat: 趋势分钟/小时粒度 | 后端 | 中 |
| 6184749d | fix: request log 两行紧凑 | UI | 低 |
| 4cabba8a | fix: 硬删清空绑定 + upsert | 后端 | 高 |
| f21e45ed | fix: 拉取路径 + manifest | 后端 | 高 |
| 8070cf24 | feat: 统一失败日志管线 | 后端 | 高 |
| d42ad751 | fix: 趋势查询窗口 | 后端 | 中 |
| 06b183ae | fix: 趋势 API 零填充 | 后端 | 中 |
| 89e24cf7 | fix: API Key 趋势图数据 | 后端 | 中 |
| 8d60c523 | fix: 趋势折线图刻度 | UI | 低 |
| 7038f4dc | feat: clearProviderModels | 后端 | 高 |
| ab0c3756 | audit: comprehensive findings | 后端 | 高 |
| e1641b60 | fix: auth_unavailable + LIMIT | 后端 | 高 |
| 0dc3adee | fix: getKeyUsageTrend | 后端 | 中 |
| 0c1551a5 | fix: raise ReadTimeout | 后端 | 中 |
| 4b56a062 | fix: usageKeyTrend SQL param | 后端 | 高 |
| 1d70dda8 | chore: GOPROXY/NPM | 构建 | 低 |
| 2a81e059 | fix: classifyFailureStage | 后端 | 中 |

## 🔍 待审计的 5 项风险点

1. **request_log_pipeline.go rate_limit 早退路径** — 可能丢失 client_model
2. **admin/keys.go usageByKey LIMIT** — $N 占位符一致性
3. **admin/catalog.go clearProviderModels** — DELETE CASCADE 未审计
4. **v2.0 新表 model_task_index** — 与 weekly_peak 数据冗余风险
5. **request_logs 新列迁移** — 缺 down 脚本

## ✅ 已交付

- ✅ v2.0.0 计划已审批
- ✅ memory-bank 已初始化
- ✅ codegraph 已确认工作区干净

## ⏳ 进行中

- 🔄 阶段 1.1：审计最近 24h commit（24 个 commit，重点检查 5 项风险）