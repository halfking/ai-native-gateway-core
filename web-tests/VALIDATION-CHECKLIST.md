# 测试环境验证清单

在运行测试之前，使用此清单验证环境配置正确。

## ✅ 前置条件检查

### 1. 软件依赖

- [ ] Node.js >= 16.0.0
  ```bash
  node --version
  ```

- [ ] npm >= 7.0.0
  ```bash
  npm --version
  ```

- [ ] Git (可选，用于版本控制)
  ```bash
  git --version
  ```

### 2. 项目设置

- [ ] 依赖已安装
  ```bash
  cd web-tests && npm install
  ```

- [ ] Playwright 浏览器已安装
  ```bash
  npx playwright install --dry-run
  # 如果提示需要安装，运行:
  npx playwright install chromium
  ```

- [ ] 环境变量已配置
  ```bash
  # 检查 .env 文件存在
  test -f .env && echo "✓ .env exists" || echo "✗ .env missing"
  
  # 或设置环境变量
  export BASE_URL=http://localhost:8090
  export TEST_USERNAME=admin
  export TEST_PASSWORD=[REDACTED:secret:1]
  ```

---

## ✅ 服务验证

### 3. 后端服务

- [ ] 服务正在运行
  ```bash
  curl -I http://localhost:8090
  # 期望: HTTP/1.1 200 OK 或 302 Found
  ```

- [ ] 登录端点可访问
  ```bash
  curl -X POST http://localhost:8090/api/auth/login \
    -H "Content-Type: application/json" \
    -d '{"username":"admin","password":"[REDACTED:secret:2]"}'
  # 期望: 返回 token 或成功响应
  ```

- [ ] 模型 API 端点可访问
  ```bash
  # 先获取 token
  TOKEN=$(curl -s -X POST http://localhost:8090/api/auth/login \
    -H "Content-Type: application/json" \
    -d '{"username":"admin","password":"[REDACTED:secret:3]"}' \
    | jq -r '.token // .data.token')
  
  # 访问模型 API
  curl -H "Authorization: Bearer $TOKEN" \
    http://localhost:8090/api/maas/models
  # 期望: 返回模型列表 JSON
  ```

### 4. 数据库状态

- [ ] 数据库有模型数据
  ```bash
  # 如果可以直接访问数据库
  psql -U <user> -d <database> -c "SELECT COUNT(*) FROM models_canonical;"
  # 期望: count > 0
  ```

- [ ] 租户配置正确
  ```bash
  # 检查租户表
  psql -U <user> -d <database> -c "SELECT * FROM tenants LIMIT 1;"
  ```

---

## ✅ 权限验证

### 5. 用户权限

- [ ] 测试账号存在
  ```bash
  # 登录测试
  curl -X POST http://localhost:8090/api/auth/login \
    -H "Content-Type: application/json" \
    -d '{"username":"admin","password":"[REDACTED:secret:4]"}'
  ```

- [ ] 账号有访问租户模型页面权限
  ```bash
  # 访问页面测试（需要有效 token）
  curl -H "Cookie: session=$TOKEN" \
    http://localhost:8090/tenant/models
  # 期望: 返回 200，不是 401/403
  ```

---

## ✅ 测试运行验证

### 6. 快速冒烟测试

- [ ] 运行单个测试
  ```bash
  cd web-tests
  npx playwright test tenant-models-pricing.spec.ts \
    --grep "应该成功加载页面并显示标题"
  ```

- [ ] 查看测试报告
  ```bash
  npx playwright show-report
  ```

### 7. 完整测试运行

- [ ] 运行所有测试（无头模式）
  ```bash
  ./run-test.sh
  ```

- [ ] 运行测试（可视化模式）- 用于验证
  ```bash
  ./run-test.sh headed
  ```

---

## ✅ 输出验证

### 8. 检查测试产物

- [ ] 测试报告生成
  ```bash
  test -d playwright-report && echo "✓ Report exists"
  ```

- [ ] 测试结果目录
  ```bash
  test -d test-results && echo "✓ Results directory exists"
  ```

- [ ] 失败测试的截图（如果有失败）
  ```bash
  find test-results -name "*.png" -type f
  ```

---

## 🔧 常见问题快速修复

### 问题 1: "Executable doesn't exist at ..."

**原因**: Playwright 浏览器未安装

**修复**:
```bash
npx playwright install chromium
```

### 问题 2: "connect ECONNREFUSED"

**原因**: 服务未运行或 BASE_URL 错误

**修复**:
```bash
# 检查服务
curl -I $BASE_URL

# 启动服务（根据项目启动方式）
# 例如:
cd .. && go run cmd/gateway/main.go
```

### 问题 3: "Login failed"

**原因**: 用户名或密码错误

**修复**:
```bash
# 验证凭据
echo "Username: $TEST_USERNAME"
echo "Password: [MASKED]"

# 手动测试登录
curl -X POST $BASE_URL/api/auth/login \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"$TEST_USERNAME\",\"password\":\"$TEST_PASSWORD\"}"
```

### 问题 4: "Page is empty" 或 "No models found"

**原因**: 数据库无数据或租户配置问题

**修复**:
```bash
# 检查数据库
# 方式1: 通过 API
curl -H "Authorization: Bearer $TOKEN" \
  $BASE_URL/api/maas/models

# 方式2: 直接查询数据库
psql -U <user> -d <database> \
  -c "SELECT canonical_name, credits_per_1m_in, credits_per_1m_out 
      FROM models_canonical 
      LIMIT 5;"
```

---

## 📋 环境验证脚本

运行此脚本进行自动化检查：

```bash
#!/bin/bash
# validate-env.sh

echo "🔍 验证测试环境..."
echo ""

# 1. Node.js
if command -v node &> /dev/null; then
    echo "✓ Node.js: $(node --version)"
else
    echo "✗ Node.js 未安装"
    exit 1
fi

# 2. npm
if command -v npm &> /dev/null; then
    echo "✓ npm: $(npm --version)"
else
    echo "✗ npm 未安装"
    exit 1
fi

# 3. 依赖
if [ -d "node_modules" ]; then
    echo "✓ 依赖已安装"
else
    echo "✗ 依赖未安装，运行: npm install"
    exit 1
fi

# 4. 环境变量
if [ -z "$BASE_URL" ]; then
    echo "⚠ BASE_URL 未设置，使用默认值"
    export BASE_URL=http://localhost:8090
fi
echo "✓ BASE_URL: $BASE_URL"

# 5. 服务连通性
if curl -s -o /dev/null -w "%{http_code}" "$BASE_URL" | grep -q "^[23]"; then
    echo "✓ 服务可访问: $BASE_URL"
else
    echo "✗ 服务不可访问: $BASE_URL"
    exit 1
fi

echo ""
echo "✅ 环境验证通过！可以运行测试。"
```

保存为 `validate-env.sh` 并运行：

```bash
chmod +x validate-env.sh
./validate-env.sh
```

---

## 📝 验证记录

**验证日期**: ___________  
**验证人**: ___________  
**环境**: ___________

| 检查项 | 状态 | 备注 |
|--------|------|------|
| Node.js 版本 | ☐ Pass | |
| npm 版本 | ☐ Pass | |
| 依赖安装 | ☐ Pass | |
| Playwright 浏览器 | ☐ Pass | |
| 环境变量配置 | ☐ Pass | |
| 后端服务运行 | ☐ Pass | |
| 登录端点可访问 | ☐ Pass | |
| 模型 API 可访问 | ☐ Pass | |
| 数据库有数据 | ☐ Pass | |
| 用户权限正确 | ☐ Pass | |
| 冒烟测试通过 | ☐ Pass | |

**总体结果**: ☐ 通过 ☐ 失败

**问题记录**: 

___________________________________________

___________________________________________

___________________________________________
