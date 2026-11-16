# 订阅层级页面验收测试指南

本文档说明如何对订阅层级管理页面进行完整的浏览器验收测试。

## 相关文件

- **前端组件**: `web/src/views/SubscriptionTiersView.vue`
- **前端测试**: `web/src/views/SubscriptionTiersView.test.ts`
- **路由配置**: `web/src/router/index.ts` (`/admin/subscription-tiers`, 需要 super_admin)
- **后端 API**: `GET /api/admin/tiers` (待实现)
- **验收脚本**: 
  - `scripts/audit/browser-subscription-tiers.sh` (完整 Playwright 测试)
  - `scripts/audit/test-subscription-tiers-simple.sh` (快速 API 检查)

## 快速测试（推荐）

首先运行简化版测试，确认基本功能：

```bash
# 在本地开发环境
./scripts/audit/test-subscription-tiers-simple.sh http://127.0.0.1:8781

# 在 211 生产环境
./scripts/audit/test-subscription-tiers-simple.sh http://127.0.0.1:8783
```

**预期输出**:
```
[1/3] 生成测试 JWT...
✓ JWT 生成成功 (前8字符): eyJhbGciO...
[2/3] 测试后端 API...
HTTP Status: 404
⚠ API 端点未实现（404）- 这是预期的，需要后端实现
[3/3] 前端路由检查...
页面状态: 200
✓ 前端页面可访问

总结:
- JWT 生成: ✓
- API 端点: ⚠ 未实现
- 前端路由: ✓
```

## 完整浏览器测试

完整测试需要安装 Playwright：

```bash
# 安装 Playwright（首次运行）
npm install -g playwright
# 或在项目目录
npm install --no-save playwright

# 运行完整测试
./scripts/audit/browser-subscription-tiers.sh http://127.0.0.1:8781
```

**测试内容**:
1. 页面渲染（标题「订阅层级」+ 刷新按钮）
2. 档位卡片按 sort_order 升序排列
3. 价格显示格式（免费档显示「免费」，付费档显示「¥X.XX」）
4. 模块列表显示（包含模块数量 + module_keys 列表）
5. 刷新功能
6. 截图证据（保存到 `/tmp/subscription-tiers-screenshot.png`）

## 后端 API 实现状态

⚠️ **当前状态**: 后端端点 `GET /api/admin/tiers` **尚未实现**。

### 预期契约

**端点**: `GET /api/admin/tiers`  
**权限**: `super_admin`  
**响应示例**:

```json
[
  {
    "code": "free",
    "name": "免费版",
    "description": "基础接入能力",
    "price_cents": 0,
    "sort_order": 1,
    "module_keys": ["llm_basic"]
  },
  {
    "code": "basic",
    "name": "基础版",
    "description": "适合小型团队",
    "price_cents": 9900,
    "sort_order": 2,
    "module_keys": ["llm_basic", "routing_v2"]
  }
]
```

### 实现位置建议

根据现有代码结构，建议在以下位置实现：

1. **主网关** (`admin/` 目录):
   - 新增 `admin/subscription_tiers_handler.go`
   - 在 `admin/routes.go` 注册路由
   - 查询 `licensing.ListSubscriptionTiers(ctx)`

2. **或 license-authority** (`cmd/license-authority/`):
   - 新增 `cmd/license-authority/tiers_handler.go`
   - 在 `cmd/license-authority/routes.go` 注册
   - nginx 需配置反代到 `:8443`

## Mock 模式

如果后端未实现，测试脚本会自动启动一个临时 mock 服务器（监听 18099 端口）提供模拟数据。

**Mock 数据包含 5 个档位**:
- free (免费版, ¥0.00)
- basic (基础版, ¥99.00)
- standard (标准版, ¥299.00)
- premium (高级版, ¥599.00)
- enterprise (企业版, ¥999.00)

## 常见问题

### Q: 测试显示 404，是什么问题？

A: 这是正常的，说明后端 API 尚未实现。测试会自动切换到 mock 模式继续验证前端功能。

### Q: 前端路由返回 403/401？

A: 检查：
1. JWT 密钥是否正确（环境变量 `LLM_GATEWAY_SECRET_KEY`）
2. 用户是否有 `super_admin` 权限
3. 路由守卫配置是否正确

### Q: Playwright 安装失败？

A: 可以使用简化版测试（`test-subscription-tiers-simple.sh`），它只需要 curl 和 Python 3。

### Q: 如何在 211 生产环境测试？

A: 在 211 服务器上运行：

```bash
# 使用生产端口（蓝绿部署，可能是 8781 或 8783）
./scripts/audit/test-subscription-tiers-simple.sh http://127.0.0.1:8783
```

## 下一步

- [ ] 实现后端 API `GET /api/admin/tiers`
- [ ] 配置 nginx 反代（如果放在 license-authority）
- [ ] 补充后端单元测试
- [ ] 更新 API 文档
- [ ] 在生产环境验证端到端流程

## 参考

- 前端实现: PR #XXX
- 单元测试覆盖: `SubscriptionTiersView.test.ts` (100% 覆盖)
- 变异测试: 5/5 红→绿
