# LLM Gateway Web 浏览器测试

基于 Playwright 的端到端浏览器测试套件。

## 📋 目录

- [快速开始](#快速开始)
- [测试套件](#测试套件)
- [环境配置](#环境配置)
- [运行测试](#运行测试)
- [测试报告](#测试报告)
- [故障排查](#故障排查)
- [文档](#文档)

---

## 🚀 快速开始

### 1. 安装依赖

```bash
cd web-tests
npm install
npx playwright install chromium
```

### 2. 配置环境

复制环境变量模板并填入实际值：

```bash
cp .env.example .env
# 编辑 .env 文件，设置 BASE_URL、TEST_USERNAME、TEST_PASSWORD
```

### 3. 运行测试

使用快速启动脚本：

```bash
# 无头模式（默认）
./run-test.sh

# 可视化模式
./run-test.sh headed

# 调试模式
./run-test.sh debug

# UI 模式
./run-test.sh ui
```

或使用 npm scripts：

```bash
npm test                    # 运行所有测试
npm run test:headed        # 可视化模式
npm run test:debug         # 调试模式
npm run test:ui            # UI 模式
npm run test:report        # 查看报告
npm run test:pricing       # 只运行定价测试
```

---

## 📦 测试套件

### 当前可用测试

#### 1. 租户模型定价页面 (`tenant-models-pricing.spec.ts`)

**测试路径**: `/tenant/models`

**覆盖范围**:
- ✅ 页面加载和基础显示
- ✅ 模型列表渲染
- ✅ Input/Output Credits 显示
- ✅ 模型搜索功能
- ✅ 多模态筛选功能
- ✅ 数据刷新功能
- ✅ 数值格式和合理性验证
- ✅ 模型唯一性验证
- ✅ 响应式布局（桌面/平板/移动）

**测试用例数**: 10 个

详细说明：[README-tenant-models-pricing.md](./README-tenant-models-pricing.md)

---

## ⚙️ 环境配置

### 必需环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `BASE_URL` | `http://localhost:8090` | 服务基础URL |
| `TEST_USERNAME` | `admin` | 测试账号用户名 |
| `TEST_PASSWORD` | - | 测试账号密码 |

### 可选环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `MIN_MODELS_EXPECTED` | `5` | 期望最小模型数量 |
| `TEST_TIMEOUT` | `10000` | 测试超时时间（毫秒）|
| `BROWSER` | `chromium` | 测试浏览器 |

### 配置方式

#### 方式1: 环境变量文件

创建 `.env` 文件：

```bash
BASE_URL=http://localhost:8090
TEST_USERNAME=admin
TEST_PASSWORD=your_password_here
```

#### 方式2: 命令行导出

```bash
export BASE_URL=http://localhost:8090
export TEST_USERNAME=admin
export TEST_PASSWORD=your_password_here
```

#### 方式3: 内联设置

```bash
BASE_URL=http://example.com npm test
```

---

## 🧪 运行测试

### 基础命令

```bash
# 运行所有测试
npx playwright test

# 运行特定测试文件
npx playwright test tenant-models-pricing.spec.ts

# 运行匹配的测试
npx playwright test -g "应该显示模型列表"

# 指定浏览器
npx playwright test --project=chromium
npx playwright test --project=firefox
npx playwright test --project=webkit
```

### 可视化和调试

```bash
# 显示浏览器界面
npx playwright test --headed

# 调试模式（逐步执行）
npx playwright test --debug

# UI 模式（交互式）
npx playwright test --ui

# 启用 trace
npx playwright test --trace on
```

### 高级选项

```bash
# 并行执行（指定 worker 数量）
npx playwright test --workers=4

# 失败重试
npx playwright test --retries=2

# 只运行失败的测试
npx playwright test --last-failed

# 更新快照
npx playwright test --update-snapshots
```

---

## 📊 测试报告

### 生成 HTML 报告

```bash
# 运行测试并生成报告
npx playwright test --reporter=html

# 查看报告
npx playwright show-report
```

### 报告位置

- **HTML 报告**: `playwright-report/index.html`
- **测试结果**: `test-results/`
- **截图**: `test-results/*/test-failed-*.png`
- **视频**: `test-results/*/video.webm`
- **Trace**: `test-results/*/trace.zip`

### 查看 Trace

```bash
# 查看特定测试的 trace
npx playwright show-trace test-results/.../trace.zip
```

---

## 🔧 故障排查

### 常见问题

#### 1. 登录失败

**症状**: 测试在登录步骤失败

**排查**:
```bash
# 检查服务可访问性
curl -I $BASE_URL

# 验证登录端点
curl -X POST $BASE_URL/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"your_password"}'
```

**解决**:
- 确认 `BASE_URL` 正确
- 验证用户名和密码
- 检查用户权限配置

#### 2. 页面为空或无数据

**症状**: 测试报告"页面为空"

**排查**:
```bash
# 检查模型数据
curl -H "Authorization: Bearer <token>" \
  $BASE_URL/api/maas/models

# 检查数据库
psql -U postgres -d llm_gateway \
  -c "SELECT COUNT(*) FROM models_canonical;"
```

**解决**:
- 确认数据库有模型数据
- 验证租户配置
- 检查 API 权限

#### 3. 超时错误

**症状**: 测试报告超时

**排查**:
- 检查网络延迟
- 查看服务响应时间
- 确认数据库连接正常

**解决**:
```typescript
// 在测试代码中增加超时时间
test.setTimeout(60000); // 60秒

// 或在配置文件中全局设置
// playwright.config.ts
timeout: 60 * 1000
```

#### 4. Playwright 浏览器未安装

**症状**: 报错 "Executable doesn't exist"

**解决**:
```bash
# 安装所有浏览器
npx playwright install

# 或只安装 chromium
npx playwright install chromium
```

### 调试技巧

#### 1. 查看浏览器界面

```bash
npx playwright test --headed --debug
```

#### 2. 慢速执行

```bash
# 每步操作之间暂停
npx playwright test --headed --slowmo=1000
```

#### 3. 检查元素选择器

在测试文件中添加：

```typescript
// 打印页面内容
console.log(await page.content())

// 截图保存
await page.screenshot({ path: 'debug.png' })

// 等待手动检查
await page.pause()
```

#### 4. 查看网络请求

```typescript
page.on('request', request => {
  console.log('>>', request.method(), request.url())
})

page.on('response', response => {
  console.log('<<', response.status(), response.url())
})
```

---

## 📚 文档

- [租户模型定价测试说明](./README-tenant-models-pricing.md)
- [测试用例清单](./TEST-CASES.md)
- [Playwright 官方文档](https://playwright.dev)

---

## 📁 项目结构

```
web-tests/
├── README.md                           # 本文件
├── README-tenant-models-pricing.md     # 定价测试详细说明
├── TEST-CASES.md                       # 测试用例清单
├── playwright.config.ts                # Playwright 配置
├── package.json                        # 依赖管理
├── .env.example                        # 环境变量模板
├── .gitignore                          # Git 忽略配置
├── run-test.sh                         # 快速启动脚本
├── tenant-models-pricing.spec.ts       # 定价页面测试
├── playwright-report/                  # 测试报告（自动生成）
└── test-results/                       # 测试结果（自动生成）
```

---

## 🔐 安全注意事项

1. **不要提交敏感信息**
   - `.env` 文件已在 `.gitignore` 中
   - 不要在代码中硬编码密码
   - 使用环境变量传递凭据

2. **测试账号权限**
   - 使用专用测试账号
   - 限制测试账号权限范围
   - 定期轮换测试密码

3. **生产环境测试**
   - 谨慎在生产环境运行测试
   - 使用只读操作
   - 避免修改生产数据

---

## 🤝 贡献指南

### 添加新测试

1. 创建新的 `.spec.ts` 文件
2. 遵循现有测试结构
3. 添加相应的 README 文档
4. 更新测试用例清单
5. 确保测试可独立运行

### 测试命名规范

- 文件名: `feature-name.spec.ts`
- 测试套件: `describe('功能模块名称')`
- 测试用例: `test('应该做什么')`

### 代码风格

- 使用 TypeScript
- 遵循 ESLint 规则
- 添加必要的注释
- 保持代码简洁

---

## 📝 更新日志

### 2024-01-XX

- ✨ 初始版本
- ✅ 添加租户模型定价页面测试
- 📝 完善文档和配置
- 🔧 添加快速启动脚本

---

## 📄 许可证

与主项目相同

---

## 💬 支持

如有问题或建议，请：

1. 查看[故障排查](#故障排查)章节
2. 阅读相关文档
3. 提交 Issue 描述问题

---

**最后更新**: 2024-01-XX  
**Playwright 版本**: ^1.40.0  
**Node.js 要求**: >= 16.0.0
