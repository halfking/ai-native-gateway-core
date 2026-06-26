# 71 服务器路由问题修复 - 完整方案

> **状态**: ✅ 工具就绪，待在71服务器上执行  
> **日期**: 2026-06-26  
> **预计修复时间**: 10-15 分钟

---

## 🎯 问题概述

您报告的问题：

1. ❌ **71服务器上的请求无法记录** - request_logs 表没有新记录
2. ❌ **通过 llmgateway.internal.example.com/v1 发起的请求无法正确路由** - 返回 no_candidate 错误
3. ❌ **路由层无法正确匹配凭据** - 虽然有可用凭据（如 minimax-m3）
4. ❌ **184 数据库的 request_logs 中大量 empty_response** - 误判问题

---

## ✅ 已完成的工作

### 1. 代码增强（已提交到 server-71 分支）

- ✅ `autoroute/index.go` - 添加详细的路由索引刷新日志
- ✅ `provider/client.go` - 添加 GetCandidates 全流程追踪日志
- ✅ `relay/handler.go` - empty_response 检测修复（commit 78de1295）

### 2. 诊断和修复工具

#### 📊 `scripts/test_71_routing.sh` - 综合诊断脚本
自动检查：
- API 健康状态
- minimax-m3 路由测试
- 数据库配置完整性
- Canonical ID 一致性
- 路由索引状态
- 凭据可用性
- 最近的请求日志

#### 🔧 `scripts/fix_71_routing_complete.sh` - 自动修复脚本
自动执行：
- 完整诊断
- 修复 canonical_id 不一致
- 初始化路由索引
- 验证修复结果
- 测试实际请求
- 检查 empty_response 统计

### 3. 完整文档

- 📖 `docs/71_SERVER_ROUTING_FIX_GUIDE.md` - 详细故障排查指南
- ⚡ `docs/71_QUICK_DIAGNOSIS.md` - 快速参考和命令速查
- 📋 `docs/71_EXECUTION_SUMMARY.md` - 执行总结和预期结果

---

## 🚀 立即开始修复

### 步骤 1: SSH 到 71 服务器

```bash
ssh user@71-server
```

### 步骤 2: 设置环境变量

```bash
# 数据库连接（184服务器）
export DB_HOST=<184-ip>
export DB_PORT=5432
export DB_USER=kxuser
export DB_NAME=llm_gateway
export DB_PASSWORD=<password>

# 测试用 API Key
export API_KEY=<your-api-key>
```

### 步骤 3: 更新代码

```bash
cd /path/to/llm-gateway-go-2
git fetch origin
git checkout server-71
git pull origin server-71
```

### 步骤 4: 运行诊断（2分钟）

```bash
./scripts/test_71_routing.sh
```

**这个脚本会告诉你**：
- ✅ 哪些配置正确
- ❌ 哪些配置有问题
- 🔍 具体的问题细节

### 步骤 5: 运行修复（5-10分钟）

```bash
./scripts/fix_71_routing_complete.sh
```

**脚本会**：
1. 显示当前问题
2. 询问是否继续修复
3. 自动修复所有问题
4. 验证修复结果
5. 测试实际请求

### 步骤 6: 验证成功

```bash
# 发送测试请求
curl -X POST https://llmgateway.internal.example.com/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "你好"}],
    "max_tokens": 10
  }'
```

**期望结果**：
```json
{
  "id": "chatcmpl-xxx",
  "model": "minimax-m3",
  "choices": [...],
  "usage": {...}
}
```

---

## 📊 预期改善

### 修复前
```
路由索引: 0 条记录
请求失败: ~100% (no_candidate)
empty_response: >20%
```

### 修复后
```
路由索引: ≥2 条记录
请求成功率: >80%
empty_response: <5%
```

---

## 🔍 如果还有问题

### 情况 A: 仍然返回 no_candidate

**原因**: Gateway 缓存未刷新

**解决**:
```bash
# 重启 Gateway
systemctl restart llm-gateway
# 或
docker restart gateway

# 等待 30 秒后重试
```

### 情况 B: credential_reveal_failed

**原因**: 无法解密凭据

**解决**:
```bash
# 检查环境变量
echo $CREDENTIAL_ENCRYPTION_KEY
echo $SECRET_KEY

# 如果缺失，添加到配置文件后重启
```

### 情况 C: empty_response 仍然很多

**原因**: 代码未部署最新版本

**解决**:
```bash
# 重新编译
go build -o /tmp/gateway-new ./cmd/gateway

# 部署
systemctl stop llm-gateway
cp /path/to/gateway /path/to/gateway.backup
cp /tmp/gateway-new /path/to/gateway
systemctl start llm-gateway
```

---

## 📞 需要帮助？

如果以上步骤无法解决问题，请提供：

1. **诊断输出**:
   ```bash
   ./scripts/test_71_routing.sh > diagnosis.log 2>&1
   ```

2. **Gateway 日志**:
   ```bash
   docker logs gateway --tail 500 > gateway.log 2>&1
   ```

3. **数据库查询结果**:
   ```sql
   SELECT COUNT(*) FROM credential_model_index WHERE raw_model = 'minimax-m3';
   SELECT * FROM v_routable_credential_models WHERE raw_model_name = 'minimax-m3';
   ```

---

## 📚 详细文档

- **完整指南**: [docs/71_SERVER_ROUTING_FIX_GUIDE.md](./docs/71_SERVER_ROUTING_FIX_GUIDE.md)
- **快速参考**: [docs/71_QUICK_DIAGNOSIS.md](./docs/71_QUICK_DIAGNOSIS.md)
- **执行总结**: [docs/71_EXECUTION_SUMMARY.md](./docs/71_EXECUTION_SUMMARY.md)
- **Minimax-M3 报告**: [docs/MINIMAX_M3_FINAL_REPORT.md](./docs/MINIMAX_M3_FINAL_REPORT.md)

---

## ✅ 完成检查清单

- [ ] 在 71 服务器上运行诊断脚本
- [ ] 运行修复脚本
- [ ] 路由索引有数据（count > 0）
- [ ] 测试请求返回 200
- [ ] request_logs 有新记录
- [ ] no_candidate 比例 <5%
- [ ] empty_response 比例 <10%

---

**创建时间**: 2026-06-26  
**维护者**: LLM Gateway Team  
**分支**: server-71
