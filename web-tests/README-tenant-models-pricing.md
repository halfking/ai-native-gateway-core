# 租户模型定价页面浏览器测试

## 测试目标页面

**路径**: `/tenant/models` (TenantModelsView)

**页面功能**:
- 显示租户可用的模型目录
- 每个模型显示 Input/Output Credits 单价
- 支持模型搜索
- 支持多模态筛选（multimodal/vision/audio）
- 响应式布局（桌面/平板/移动）

## 当前测试覆盖

### ✅ 已实现的测试

1. **基础功能**
   - 页面加载和标题显示
   - 模型列表显示
   - Input/Output Credits 字段存在性验证
   - 刷新功能

2. **交互功能**
   - 模型搜索功能
   - 筛选功能（多模态）
   - 响应式布局切换

3. **数据完整性**
   - Credits 数值格式验证
   - 模型唯一标识符验证
   - 数值合理性验证（非负数）

### ⚠️ 与 Goal 描述的差异

Goal 中提到的功能当前页面**不包含**：

1. **档位标签** (free/basic/standard/premium/enterprise)
   - 当前页面没有显示订阅层级信息
   - 如需测试档位，可能需要测试 `/admin/subscription-tiers` 或开发新功能

2. **总 Credits 计算**
   - 当前页面只显示单个模型的 Input/Output 单价
   - 没有"总 Credits"概念

3. **按档位筛选**
   - 当前只有多模态筛选和搜索
   - 没有按订阅层级筛选的功能

4. **16个模型的固定数量**
   - 当前页面显示所有可用模型（数量可变）
   - 测试使用 `minModelsExpected: 5` 作为最低期望

## 运行测试

### 前置条件

1. 安装依赖:
```bash
cd web-tests
npm install
```

2. 配置环境变量（可选）:
```bash
export BASE_URL=http://localhost:8090
export TEST_USERNAME=admin
export TEST_PASSWORD=admin123
```

### 运行测试

```bash
# 运行所有测试
npx playwright test tenant-models-pricing.spec.ts

# 显示浏览器界面（调试模式）
npx playwright test tenant-models-pricing.spec.ts --headed

# 运行特定测试
npx playwright test tenant-models-pricing.spec.ts -g "应该显示模型列表"

# 生成测试报告
npx playwright test tenant-models-pricing.spec.ts --reporter=html
npx playwright show-report
```

### 配置说明

测试脚本中的配置项：

```typescript
const TEST_CONFIG = {
  baseURL: process.env.BASE_URL || 'http://localhost:8090',
  username: process.env.TEST_USERNAME || 'admin',
  password: process.env.TEST_PASSWORD || 'admin123',
  minModelsExpected: 5, // 期望最少模型数量
  timeout: 10000, // 超时时间（毫秒）
}
```

## 可能需要的后续工作

如果 Goal 描述的功能是新需求，可能需要：

1. **创建档位定价页面**
   - 显示每个订阅层级（free/basic/standard/premium/enterprise）
   - 每个档位下显示包含的模型及其价格
   - 支持按档位筛选

2. **扩展现有页面**
   - 在 TenantModelsView 中添加档位标签列
   - 添加按档位筛选功能
   - 添加总 Credits 计算

3. **对接订阅层级数据**
   - 连接 `/api/admin/tiers` 端点
   - 显示模型与订阅层级的关联关系

## 测试数据说明

测试依赖以下数据：
- 至少有 1 个已配置的模型
- 模型数据包含 `canonical_name`, `credits_per_1m_in`, `credits_per_1m_out` 字段
- 用户有访问 `/tenant/models` 的权限

如果页面显示为空，可能是：
1. 数据库中没有配置模型
2. 当前租户没有可用模型
3. 权限配置问题

## 故障排查

### 常见问题

1. **登录失败**
   - 检查 `BASE_URL` 是否正确
   - 验证用户名和密码
   - 确认用户有相应权限

2. **页面为空**
   - 检查数据库中是否有模型数据
   - 验证 API 端点 `/api/maas/models` 是否正常
   - 查看浏览器控制台错误

3. **测试超时**
   - 调整 `TEST_CONFIG.timeout` 值
   - 检查网络连接
   - 确认服务响应时间

### 调试技巧

1. **查看浏览器界面**:
```bash
npx playwright test --headed --debug
```

2. **截图保存**:
测试失败时会自动保存截图到 `test-results/` 目录

3. **Trace 查看器**:
```bash
npx playwright test --trace on
npx playwright show-trace trace.zip
```

## 联系和反馈

如果测试覆盖范围与实际需求不符，请提供：
1. 实际页面路径和功能描述
2. 期望的测试场景
3. 具体的数据结构和字段

---

**最后更新**: 2024-01-XX
**测试框架**: Playwright
**目标页面**: TenantModelsView (`/tenant/models`)
