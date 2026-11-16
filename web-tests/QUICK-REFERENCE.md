# 快速参考卡片

## 🚀 5分钟上手

```bash
# 1. 安装
cd web-tests && npm install && npx playwright install chromium

# 2. 配置
export BASE_URL=http://localhost:8090
export TEST_USERNAME=admin
export TEST_PASSWORD=[REDACTED:secret:5]

# 3. 运行
./run-test.sh
```

---

## 📝 常用命令

| 命令 | 说明 |
|------|------|
| `./validate-env.sh` | 验证环境配置 |
| `./run-test.sh` | 运行测试（无头） |
| `./run-test.sh headed` | 运行测试（可视化） |
| `./run-test.sh debug` | 调试模式 |
| `./run-test.sh ui` | UI 模式 |
| `npm run test:report` | 查看测试报告 |

---

## 🧪 测试套件

| 测试文件 | 路径 | 用例数 |
|---------|------|--------|
| 租户模型定价 | `/tenant/models` | 10 |

---

## 🔧 环境变量

| 变量 | 默认值 | 必需 |
|------|--------|------|
| `BASE_URL` | `http://localhost:8090` | ✓ |
| `TEST_USERNAME` | `admin` | ✓ |
| `TEST_PASSWORD` | - | ✓ |

---

## 📊 报告位置

```
playwright-report/index.html    # HTML报告
test-results/                   # 测试结果
  ├── screenshots/              # 截图
  ├── videos/                   # 视频
  └── traces/                   # Trace
```

---

## 🆘 快速排障

### 登录失败
```bash
curl -X POST $BASE_URL/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"[REDACTED:secret:6]"}'
```

### 服务不可达
```bash
curl -I $BASE_URL
# 检查服务是否运行
```

### 页面无数据
```bash
# 检查模型数据
curl -H "Authorization: Bearer <token>" \
  $BASE_URL/api/maas/models
```

### 浏览器未安装
```bash
npx playwright install chromium
```

---

## 📚 文档索引

| 文档 | 用途 |
|------|------|
| `README.md` | 完整使用指南 |
| `README-tenant-models-pricing.md` | 定价测试说明 |
| `TEST-CASES.md` | 测试用例清单 |
| `VALIDATION-CHECKLIST.md` | 环境验证清单 |
| `QUICK-REFERENCE.md` | 本文件 |

---

## 🎯 测试覆盖

- ✅ 页面加载和显示
- ✅ 模型列表渲染
- ✅ 搜索功能
- ✅ 筛选功能
- ✅ 数据刷新
- ✅ 数值验证
- ✅ 响应式布局

---

## 💡 最佳实践

1. **运行前验证环境**
   ```bash
   ./validate-env.sh
   ```

2. **首次运行使用可视化模式**
   ```bash
   ./run-test.sh headed
   ```

3. **失败后查看截图**
   ```bash
   ls -la test-results/*/screenshots/
   ```

4. **生成并查看报告**
   ```bash
   npm run test:report
   ```

---

## 🔐 安全提醒

- ❌ 不要提交 `.env` 文件
- ❌ 不要在代码中硬编码密码
- ✅ 使用环境变量
- ✅ 使用专用测试账号

---

## 📞 获取帮助

1. 查看 `README.md` 故障排查章节
2. 运行 `./validate-env.sh` 诊断环境
3. 使用 `--debug` 模式查看详细信息
4. 查看测试日志和截图

---

**版本**: 1.0.0  
**最后更新**: 2024-01-XX
