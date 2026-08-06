# Git 提交建议

## 提交策略

建议将修改分为 **2 个提交**，以便于代码审查和可能的选择性回滚：

---

## 提交 1: 审计修复和会话管理功能实现

### 提交信息
```
feat(session): 会话管理功能实现 + P0审计修复 (2026-08-06)

P0 修复：
- fix(auto-summary): 添加最小5轮门槛，避免短会话浪费成本（预计年节省 $4,380）
- feat(auto-title): 优化标题生成策略，移除 max_tokens 限制，提升质量和灵活性

P1 架构扩展：
- feat(session): 扩展 session_summaries 表，添加 project_id, task_id, user_tags, session_status
- feat(session): 创建会话管理视图（flow, task_summary, project_summary, daily_costs）
- feat(session): 实现会话列表 API（多维度过滤、全文搜索、分页）
- feat(session): 实现任务脉络和项目成本 API

P2 功能实现：
- feat(session): 数据迁移脚本（V353, V354, V355）
- feat(session): 路由注册和 Handler 集成

影响：
- 预期成本节省：$4,380/年
- 新增 5 个 API 端点
- 新增 3 个数据库迁移脚本
- 新增 4 个视图，5 个索引

文档：
- .handoff/2026-08-06-*.md（5份文档）

Breaking Changes: 无（所有修改向后兼容）
```

### 包含文件
```bash
# 修改的文件
admin/admin_llm_task.go
admin/auto_summary_generator.go
admin/handler.go
internal/summarystore/store.go

# 新增的文件
admin/session_management_api.go
admin/session_management_handlers.go
admin/session_task_project_api.go
deploy/sql/migrations/V353__session_summaries_project_task_tags.sql
deploy/sql/migrations/V354__session_management_views.sql
deploy/sql/migrations/V355__backfill_session_task_id.sql
.handoff/2026-08-06-audit-and-session-management-plan.md
.handoff/2026-08-06-correction.md
.handoff/2026-08-06-deployment-guide.md
.handoff/2026-08-06-executive-summary.md
.handoff/2026-08-06-final-report.md
.handoff/2026-08-06-implementation-summary.md
```

### 提交命令
```bash
git add admin/admin_llm_task.go
git add admin/auto_summary_generator.go
git add admin/handler.go
git add admin/session_management_api.go
git add admin/session_management_handlers.go
git add admin/session_task_project_api.go
git add internal/summarystore/store.go
git add deploy/sql/migrations/V35*.sql
git add .handoff/2026-08-06-*.md

git commit -F- <<'EOF'
feat(session): 会话管理功能实现 + P0审计修复 (2026-08-06)

P0 修复：
- fix(auto-summary): 添加最小5轮门槛，避免短会话浪费成本（预计年节省 $4,380）
- feat(auto-title): 优化标题生成策略，移除 max_tokens 限制，提升质量和灵活性

P1 架构扩展：
- feat(session): 扩展 session_summaries 表，添加 project_id, task_id, user_tags, session_status
- feat(session): 创建会话管理视图（flow, task_summary, project_summary, daily_costs）
- feat(session): 实现会话列表 API（多维度过滤、全文搜索、分页）
- feat(session): 实现任务脉络和项目成本 API

P2 功能实现：
- feat(session): 数据迁移脚本（V353, V354, V355）
- feat(session): 路由注册和 Handler 集成

影响：
- 预期成本节省：$4,380/年
- 新增 5 个 API 端点
- 新增 3 个数据库迁移脚本
- 新增 4 个视图，5 个索引

文档：
- .handoff/2026-08-06-*.md（6份文档）

Breaking Changes: 无（所有修改向后兼容）
EOF
```

---

## 提交 2: 其他并发安全修复

这些修改看起来是之前的并发安全修复，建议单独提交：

### 提交信息
```
fix(concurrency): 修复 LRU 缓存和流式处理的并发安全问题 (2026-08-06)

修复内容：
- fix(session): LRU 缓存 head/tail 指针提前初始化，避免并发竞态
- fix(streaming): sticky session 并发安全改进
- fix(logging): 异步日志器并发安全优化

影响：
- 修复潜在的并发 panic 风险
- 提升系统稳定性
```

### 包含文件
```bash
domains/session/v2/cache_v2.go
domains/streaming/executors/sticky.go
internal/logging/async_raw_logger.go
```

### 提交命令
```bash
git add domains/session/v2/cache_v2.go
git add domains/streaming/executors/sticky.go
git add internal/logging/async_raw_logger.go

git commit -m "fix(concurrency): 修复 LRU 缓存和流式处理的并发安全问题 (2026-08-06)

修复内容：
- fix(session): LRU 缓存 head/tail 指针提前初始化，避免并发竞态
- fix(streaming): sticky session 并发安全改进
- fix(logging): 异步日志器并发安全优化

影响：
- 修复潜在的并发 panic 风险
- 提升系统稳定性"
```

---

## 或者：单个提交（不推荐）

如果希望合并为一个提交（不推荐，但可以这样做）：

```bash
git add -A

git commit -m "feat(session): 会话管理功能 + 审计修复 + 并发安全修复 (2026-08-06)

主要功能：
- feat(session): 完整的会话管理功能（项目/任务/标签维度）
- fix(auto-summary): 添加最小5轮门槛（年节省 $4,380）
- feat(auto-title): 优化标题生成策略
- fix(concurrency): LRU 缓存和流式处理并发安全修复

详见：.handoff/2026-08-06-final-report.md"
```

---

## 推荐做法 ✅

**推荐使用两个提交**，原因：
1. 关注点分离：功能实现 vs 并发修复
2. 便于代码审查：审查者可以分别审查不同类型的修改
3. 便于回滚：如果某个修改有问题，可以选择性回滚
4. 更清晰的 Git 历史

---

## 提交后验证

```bash
# 查看提交历史
git log --oneline -2

# 查看提交内容
git show HEAD
git show HEAD~1

# 推送到远程（如果需要）
git push origin main
```

---

## 注意事项

1. **数据库迁移**：提交后需要在生产环境执行迁移脚本
2. **部署顺序**：先部署代码，再执行数据库迁移
3. **文档位置**：所有详细文档在 `.handoff/` 目录
4. **部署指南**：参考 `.handoff/2026-08-06-deployment-guide.md`

---

**创建时间**：2026-08-06  
**状态**：准备提交
