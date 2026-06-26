# 71 服务器路由问题诊断和修复 - 执行总结

**日期**: 2026-06-26  
**问题**: 请求无法记录 + 路由失败 + empty_response 过多  
**状态**: ✅ 诊断工具已完成，待执行修复

---

## 📋 问题分析

### 用户报告的问题

1. **71服务器上的请求无法记录** - request_logs 表没有新记录
2. **通过 llmgateway.internal.example.com/v1 发起的请求无法正确路由** - 返回 no_candidate 错误  
3. **路由层无法正确匹配凭据** - 虽然有可用凭据（如 minimax-m3）
4. **184 数据库的 request_logs 中大量 empty_response** - 误判问题

### 根本原因（已识别）

#### 问题 1-3: 路由失败

可能的原因（需要在71服务器上验证）：

1. **路由索引为空或过期**
   - `credential_model_index` 表缺少数据
   - Gateway 的 autoroute.Index.Refresh() 返回空列表
   - 导致 provider.GetCandidates() 返回 0 个候选

2. **Canonical ID 不一致**
   - `model_aliases.canonical_id` 与 `provider_models.canonical_id` 不匹配
   - SQL JOIN 条件无法匹配，导致查询返回 0 行

3. **凭据状态不可用**
   - `credentials.availability_state != 'ready'`
   - 或 `credential_model_bindings.available = false`

4. **Gateway 缓存问题**
   - 数据库已修复但 Gateway 使用旧缓存

#### 问题 4: empty_response 过多

**根因**: `relay/handler.go::detectEmptyStreamResponse` 的第4个检查条件是 dead code

**已修复**: Commit `78de1295` - 需要重新部署

---

## ✅ 已完成的工作

### 1. 代码层面

- ✅ `autoroute/index.go`: 已添加详细日志记录
- ✅ `provider/client.go`: 已添加 GetCandidates 全流程日志
- ✅ `relay/handler.go`: empty_response 检测逻辑已修复（commit 78de1295）

### 2. 诊断工具

#### `scripts/test_71_routing.sh`
完整的诊断脚本，检查：
- 健康检查
- minimax-m3 路由测试
- 数据库配置验证
- 路由索引状态
- 凭据状态
- 最近的 request_logs
- 路由决策日志

#### `scripts/fix_71_routing_complete.sh`
自动化修复脚本，包含：
- 交互式诊断（每步暂停）
- 自动修复 canonical_id 不一致
- 自动初始化路由索引（如果为空）
- 验证修复结果
- 测试实际请求
- 检查 empty_response 统计

### 3. 文档

#### `docs/71_SERVER_ROUTING_FIX_GUIDE.md`
详细的故障排查指南，包含：
- 完整的根因分析
- 分步修复流程
- 所有诊断 SQL
- 故障排查决策树
- 监控指标
- 预防措施

#### `docs/71_QUICK_DIAGNOSIS.md`
快速参考文档，包含：
- 1分钟快速诊断
- 关键 SQL 速查
- 快速修复 SQL
- 问题决策树
- 紧急情况处理

---

## 🚀 下一步行动

### 立即执行（在71服务器上）

#### 步骤 1: 运行诊断（5分钟）

```bash
# SSH 到 71 服务器
ssh user@71-server

# 设置环境变量
export DB_HOST=184.xxx.xxx.xxx  # 184 数据库 IP
export DB_PORT=5432
export DB_USER=kxuser
export DB_NAME=llm_gateway
export DB_PASSWORD=<从配置文件获取>
export API_KEY=<测试用的 API key>

# 切换到代码目录
cd /path/to/llm-gateway-go-2

# 拉取最新代码
git pull origin server-71

# 运行诊断
./scripts/test_71_routing.sh
```

**诊断脚本会告诉你**：
- 哪些配置有问题
- 路由索引是否存在
- 凭据状态如何
- 实际请求是否成功

#### 步骤 2: 运行修复（5-10分钟）

```bash
# 运行自动修复脚本
./scripts/fix_71_routing_complete.sh
```

脚本会：
1. 诊断所有问题
2. 询问是否继续修复（交互式）
3. 自动修复配置
4. 初始化路由索引
5. 验证修复结果
6. 测试实际请求

#### 步骤 3: 验证结果（2分钟）

```bash
# 发送测试请求
curl -X POST https://llmgateway.internal.example.com/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "测试"}],
    "max_tokens": 10
  }'
```

**期望结果**：
- HTTP 200 + 正常 JSON 响应
- 或上游 API 错误（非 503 no_candidate）

#### 步骤 4: 处理 empty_response（如果需要）

如果 empty_response 比例仍然很高（>20%）：

```bash
# 检查当前代码版本
git log --oneline -1

# 如果不包含 78de1295，需要重新部署
git pull origin main  # 或 server-71
go build -o /tmp/gateway-new ./cmd/gateway

# 部署（根据实际部署方式）
systemctl stop llm-gateway
cp /path/to/gateway /path/to/gateway.backup
cp /tmp/gateway-new /path/to/gateway
systemctl start llm-gateway
```

---

## 📊 预期结果

### 修复前（当前状态）

```sql
-- 路由索引
SELECT COUNT(*) FROM credential_model_index WHERE raw_model = 'minimax-m3';
-- 预期: 0 或很少

-- 请求失败率
SELECT 
    COUNT(*) FILTER (WHERE request_status = 'no_candidate') AS no_candidate,
    COUNT(*) AS total
FROM request_logs WHERE ts > now() - interval '1 hour';
-- 预期: 大部分是 no_candidate

-- empty_response 比例
SELECT 
    ROUND(100.0 * COUNT(*) FILTER (WHERE error_kind = 'empty_response') / COUNT(*), 2) AS pct
FROM request_logs WHERE ts > now() - interval '24 hours';
-- 预期: >20%
```

### 修复后（目标状态）

```sql
-- 路由索引
SELECT COUNT(*) FROM credential_model_index WHERE raw_model = 'minimax-m3';
-- 目标: >= 2（每个可用凭据一条）

-- 请求成功率
SELECT 
    ROUND(100.0 * COUNT(*) FILTER (WHERE success = true) / COUNT(*), 2) AS success_rate
FROM request_logs WHERE ts > now() - interval '1 hour' AND client_model = 'minimax-m3';
-- 目标: >80%

-- empty_response 比例
SELECT 
    ROUND(100.0 * COUNT(*) FILTER (WHERE error_kind = 'empty_response') / COUNT(*), 2) AS pct
FROM request_logs WHERE ts > now() - interval '1 hour';
-- 目标: <5%（除非上游真的返回空响应）
```

---

## 🔍 如果修复后仍有问题

### 情况 A: 仍然 no_candidate

**检查**：
```bash
# 查看 Gateway 日志
docker logs -f gateway 2>&1 | grep -E '(GetCandidates|routing)'
```

**可能的原因**：
1. Gateway 缓存未刷新 → 重启 Gateway 或等待 30 秒
2. 凭据密钥无法解密 → 检查 `CREDENTIAL_ENCRYPTION_KEY`
3. 凭据状态不可用 → 运行修复脚本的步骤 3

### 情况 B: empty_response 仍然很多

**检查**：
```sql
-- 按 provider 分组
SELECT 
    p.code,
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE rl.error_kind = 'empty_response') AS empty_resp
FROM request_logs rl
JOIN credentials c ON c.id = rl.credential_id
JOIN providers p ON p.id = c.provider_id
WHERE rl.ts > now() - interval '1 hour'
GROUP BY p.code;
```

**如果 NVIDIA (provider 18) 的比例高**：这是已知的上游问题，非 Gateway bug。

**如果其他 provider 也高**：确认代码版本包含 commit 78de1295。

---

## 📞 需要的信息

如果需要进一步协助，请提供：

1. **诊断脚本输出**
   ```bash
   ./scripts/test_71_routing.sh > diagnosis.log 2>&1
   ```

2. **Gateway 日志**
   ```bash
   docker logs gateway --tail 500 > gateway.log 2>&1
   ```

3. **关键 SQL 查询结果**
   ```sql
   -- 在 184 数据库执行
   SELECT COUNT(*) FROM credential_model_index WHERE raw_model = 'minimax-m3';
   SELECT * FROM v_routable_credential_models WHERE raw_model_name = 'minimax-m3';
   ```

---

## ✅ 完成标志

当以下所有条件满足时，问题即已解决：

- [x] 诊断和修复工具已创建
- [ ] 在 71 服务器上运行诊断脚本
- [ ] 路由索引已初始化（count > 0）
- [ ] Canonical ID 已统一
- [ ] 测试请求返回 200
- [ ] request_logs 有新记录且 credential_id 不为 NULL
- [ ] no_candidate 比例 <5%
- [ ] empty_response 比例 <10%
- [ ] 最新代码已部署（如果需要）

---

**创建时间**: 2026-06-26  
**当前状态**: 🟡 待执行（工具已就绪）  
**负责人**: 需要在 71 服务器上执行
