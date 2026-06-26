# 部署检查清单 - Analytics 修复

## ✅ 代码已推送

**提交**: 8ec523f7  
**分支**: server-71  
**远程**: https://codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git

---

## 📋 部署前检查

- [x] 代码修复完成
- [x] 单元测试通过 (9/9)
- [x] 编译成功
- [x] 代码已提交
- [x] 代码已推送到 server-71

---

## 🚀 服务器71部署步骤

### 1. 连接服务器并拉取代码
```bash
ssh server-71
cd /path/to/llm-gateway-go-2
git pull origin server-71
```

### 2. 应用数据库迁移
```bash
psql $DATABASE_URL -f db/migrations/302_fix_is_auto_request_null.sql
```

### 3. 重新编译和重启
```bash
go build -o bin/llm-gateway ./cmd/gateway
sudo systemctl restart llm-gateway
```

### 4. 验证部署
```bash
# 检查 Matrix 接口
curl "https://llmgateway.internal.example.com/api/admin/auto-route/analytics/matrix?window=7d&metric=count&row=task_type"

# 检查 Flow 接口
curl "https://llmgateway.internal.example.com/api/admin/auto-route/analytics/flow?window=7d"

# 访问前端页面
# https://llmgateway.internal.example.com/routing-v2
```

---

## ✅ 验证检查清单

- [ ] Matrix 接口返回数据（不是空数组）
- [ ] Flow 接口返回数据（不是空数组）
- [ ] 前端页面显示统计图表
- [ ] __specified__ 分类包含指定模型请求
- [ ] 没有错误日志

---

完整部署指南请参考: DEPLOYMENT_GUIDE.md
