# 🔧 Bug修复摘要 - 2026-07-09

## 修复的4个问题

### ✅ 1. 泳道请求显示样式优化
**文件：** `web/src/components/RequestTile.vue`
- **改动：** 交换背景色和边框色的显示逻辑
- **效果：** 错误状态用背景色显示（红色=错误，绿色=成功），原厂用边框色区分

### ✅ 2. "已连接"点击功能
**文件：** `web/src/components/LiveRequestStreamV2.vue`
- **状态：** 功能已存在且正常工作
- **功能：** 普通用户可查看SSE地址，管理员可编辑和测试

### ✅ 3. 修复 routing/resolve API 500错误
**文件：** `admin/routing_resolve_probe.go`
- **问题：** `persist_probe=1` 参数导致500错误
- **修复：** 降级错误处理，表不存在时不阻断API响应
- **效果：** API容错性提升，即使表不存在也能正常返回路由结果

### ✅ 4. 修复非admin用户登录失败
**文件：** `admin/auth.go`
- **问题：** 只有admin能登录，其他用户（如guest/Guest123）失败
- **修复：** 改进认证流程，users表中的用户不再fallback到legacy admin检查
- **效果：** 所有有效用户都能正常登录

## 快速验证

```bash
# 1. 编译验证（已通过）
cd /Users/xutaohuang/workspace/llm-gateway-go-3
go build -o /dev/null ./admin
cd web && npm run build

# 2. 测试routing/resolve API
curl -H "Authorization: Bearer $TOKEN" \
  "https://llm.kxpms.cn/api/routing/resolve?model=claude-opus-4-8&persist_probe=1"
# 预期：200 OK（不再是500）

# 3. 测试用户登录
curl -X POST https://llm.kxpms.cn/api/auth/token \
  -H "Content-Type: application/json" \
  -d '{"username":"guest","password":"Guest123"}'
# 预期：200 OK（如果用户已创建）
```

## 部署清单

### 代码部署
- [ ] 重新构建前端：`cd web && npm run build`
- [ ] 重新编译后端：`go build -o gateway cmd/gateway/main.go`
- [ ] 部署新的可执行文件和静态资源

### 数据库迁移（重要！）
- [ ] 运行诊断脚本：`./scripts/diagnose-db.sh`
- [ ] 如果缺失表，执行迁移：`./scripts/apply-missing-migrations.sh`
- [ ] 验证 routing_decision_log_hot 表已创建

### 服务启动
- [ ] 重启gateway服务
- [ ] （可选）创建guest用户用于测试
- [ ] 验证所有4个修复点

## 数据库迁移工具

为了彻底解决问题，我们创建了数据库迁移工具：

### 1. 诊断工具
```bash
./scripts/diagnose-db.sh
```
检查数据库状态，识别缺失的表和视图。

### 2. 自动修复工具
```bash
./scripts/apply-missing-migrations.sh
```
自动应用缺失的迁移（主要是 migration 346：创建 routing_decision_log_hot 表）。

### 3. 为什么需要迁移？

`persist_probe=1` 参数会将路由决策写入 `routing_decision_log_hot` 表。如果表不存在：
- **修复前**：API返回500错误 ❌
- **修复后（代码层面）**：容错处理，不阻断主流程 ✅
- **修复后（数据库层面）**：表存在，正常写入 ✅✅

**推荐：同时应用代码修复和数据库迁移，获得最佳体验。**

## 详细文档

完整的修复报告、测试建议和代码说明请参见：
- 📄 **Bug修复报告：** `docs/bugfix-2026-07-09.md`
- 📄 **数据库迁移指南：** `docs/database-migration-fix.md`
- 🛠️ **诊断工具：** `scripts/diagnose-db.sh`
- 🛠️ **迁移工具：** `scripts/apply-missing-migrations.sh`

---
修复完成时间：2026-07-09
修改文件数：3个代码文件 + 2个工具脚本 + 3个文档
影响范围：前端UI + 后端API + 用户认证 + 数据库架构
风险等级：低（向后兼容，无破坏性变更）
