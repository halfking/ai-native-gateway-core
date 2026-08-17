# Git 提交指南

## 本次修复包含的文件

### 代码修改（3个文件）
1. `web/src/components/RequestTile.vue` - 泳道样式优化
2. `admin/routing_resolve_probe.go` - API容错处理
3. `admin/auth.go` - 用户认证修复

### 工具脚本（2个文件）
4. `scripts/diagnose-db.sh` - 数据库诊断工具
5. `scripts/apply-missing-migrations.sh` - 自动迁移工具

### 文档（4个文件）
6. `BUGFIX_SUMMARY.md` - 修复摘要
7. `docs/bugfix-2026-07-09.md` - 详细修复报告
8. `docs/database-migration-fix.md` - 数据库迁移指南
9. `scripts/README.md` - 工具使用指南

## 推荐提交方式

### 方案A：单次提交（推荐）

```bash
# 添加所有文件
git add \
  web/src/components/RequestTile.vue \
  admin/routing_resolve_probe.go \
  admin/auth.go \
  scripts/diagnose-db.sh \
  scripts/apply-missing-migrations.sh \
  BUGFIX_SUMMARY.md \
  docs/bugfix-2026-07-09.md \
  docs/database-migration-fix.md \
  scripts/README.md \
  GIT_COMMIT_GUIDE.md

# 提交
git commit -m "fix: 修复4个关键问题并添加数据库迁移工具

## 问题修复

1. 泳道请求显示样式优化
   - 交换背景色和边框色：状态用背景色，原厂用边框色
   - 文件: web/src/components/RequestTile.vue

2. 确认已连接功能正常
   - 普通用户可查看SSE地址，管理员可编辑和测试
   - 文件: web/src/components/LiveRequestStreamV2.vue（已存在）

3. 修复 routing/resolve API 500错误
   - 降级错误处理，表不存在时不阻断主流程
   - 文件: admin/routing_resolve_probe.go

4. 修复非admin用户登录失败
   - 改进认证流程，users表用户不再fallback到legacy admin
   - 文件: admin/auth.go

## 数据库迁移工具

为彻底解决问题，新增数据库诊断和迁移工具：
- scripts/diagnose-db.sh - 诊断缺失的表
- scripts/apply-missing-migrations.sh - 自动应用migration 346
- scripts/README.md - 工具使用指南

## 文档

- BUGFIX_SUMMARY.md - 快速修复摘要
- docs/bugfix-2026-07-09.md - 详细修复报告
- docs/database-migration-fix.md - 数据库迁移指南

## 部署说明

1. 构建: cd web && npm run build && cd .. && go build
2. 迁移: ./scripts/diagnose-db.sh && ./scripts/apply-missing-migrations.sh
3. 重启服务

详见: BUGFIX_SUMMARY.md"
```

### 方案B：分次提交

如果你想要更清晰的提交历史：

```bash
# 提交1: 前端修复
git add web/src/components/RequestTile.vue
git commit -m "fix(ui): 交换泳道请求的背景色和边框色显示

- 状态（成功/失败）现在用背景色显示
- 原厂（OpenAI/Anthropic等）用边框色区分
- 提升视觉识别度"

# 提交2: 后端API修复
git add admin/routing_resolve_probe.go
git commit -m "fix(api): routing/resolve API 容错处理

- persist_probe=1 参数不再导致500错误
- 表不存在时降级为警告，不阻断主流程
- 改进日志提示信息"

# 提交3: 用户认证修复
git add admin/auth.go
git commit -m "fix(auth): 修复非admin用户登录失败问题

- users表中的用户不再错误fallback到legacy admin检查
- 提供更详细的登录失败原因
- 改进审计日志记录"

# 提交4: 数据库工具
git add scripts/*.sh scripts/README.md
git commit -m "feat(tools): 添加数据库诊断和迁移工具

- diagnose-db.sh: 诊断缺失的表和视图
- apply-missing-migrations.sh: 自动应用migration 346
- README.md: 工具使用指南"

# 提交5: 文档
git add BUGFIX_SUMMARY.md docs/*.md GIT_COMMIT_GUIDE.md
git commit -m "docs: 添加修复报告和迁移指南

- BUGFIX_SUMMARY.md: 快速修复摘要
- bugfix-2026-07-09.md: 详细修复报告
- database-migration-fix.md: 数据库迁移指南
- GIT_COMMIT_GUIDE.md: Git提交指南"
```

## 提交后操作

```bash
# 推送到远程
git push origin main

# 或者创建PR（如果使用feature分支）
git checkout -b fix/dashboard-routing-auth-issues
git push -u origin fix/dashboard-routing-auth-issues
# 然后在GitHub/GitLab上创建Pull Request
```

## 注意事项

1. **确保测试通过**
   ```bash
   # 后端编译
   go build -o /dev/null ./admin
   
   # 前端构建
   cd web && npm run build
   ```

2. **检查是否有遗漏的文件**
   ```bash
   git status
   ```

3. **避免提交的文件**
   - `web/dist/` (构建产物)
   - `gateway` (编译后的二进制)
   - `.env` (敏感配置)
   - `node_modules/`

4. **代码审查检查点**
   - 所有console.log/slog.Debug已移除
   - 没有硬编码的密码或密钥
   - 错误处理完整
   - 向后兼容性

## 标签建议

如果这是一个重要的修复版本，可以打标签：

```bash
git tag -a v1.12.1 -m "修复泳道显示、API容错、用户认证问题"
git push origin v1.12.1
```

---

修复完成！🎉
