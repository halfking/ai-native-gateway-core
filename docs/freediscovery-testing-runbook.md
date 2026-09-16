# FreeDiscovery 预设模板实测操作手册

## 前提条件

1. **环境变量配置**
   ```bash
   # 在启动 llm-gateway 的终端中设置
   export SILICONFLOW_API_KEY='sk-xxxxxxxxxxxxxxxx'
   export ZHIPU_API_KEY='xxxxxxxxxxxxxxxx.xxxxxxxxxxxxxxxx'
   ```

2. **网关服务运行**
   ```bash
   # 确认服务监听端口
   curl http://localhost:8782/health
   ```

3. **获取 Admin Token**
   ```bash
   # 从 .env.local 读取（最后一个 ADMIN_PASSWORD 是有效的）
   ADMIN_TOKEN=$(grep ADMIN_PASSWORD /Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-2/.env.local | tail -1 | cut -d= -f2)
   ```

---

## SiliconFlow 模板实测

### 1. 创建模板
```bash
curl -X POST http://localhost:8782/api/free-discovery/templates \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "provider_code": "siliconflow",
    "api_key_env": "$SILICONFLOW_API_KEY",
    "enabled": true
  }'
```

**预期响应：**
```json
{
  "id": <template_id>,
  "provider_code": "siliconflow",
  "base_url": "https://api.siliconflow.cn/v1",
  "models_endpoint": "/models",
  "api_dialect": "openai",
  "enabled": true,
  "consecutive_scan_failures": 0,
  "last_scan_failure_at": null,
  "auto_disabled_at": null
}
```

### 2. 执行扫描任务
```bash
TEMPLATE_ID=<从上一步获取>

curl -X POST http://localhost:8782/api/free-discovery/scan \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"template_id\": $TEMPLATE_ID}"
```

**预期响应：**
```json
{
  "task_id": <task_id>,
  "template_id": <template_id>,
  "status": "pending"
}
```

### 3. 查询扫描结果
```bash
TASK_ID=<从上一步获取>

# 等待任务完成（通常 5-10 秒）
sleep 10

curl http://localhost:8782/api/free-discovery/tasks/$TASK_ID/results \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

**成功标准：**
- `status: "success"`
- `error_message: null`
- `models` 数组包含发现的模型（预期 ≥ 10 个）
- 每个模型有 `tos_verdict` 字段（ok/caution/ambiguous/avoid）

**关键验证点：**
1. **API 兼容性：** `/models` 端点返回 200 OK
2. **响应格式：** OpenAI 格式解析成功
3. **ToS 检查：** `:free` 标记的模型被正确分级
4. **健康状态：** `consecutive_scan_failures` 保持为 0

### 4. 更新状态表

编辑 `docs/freediscovery-configuration.md` 第 264 行：

**成功场景：**
```markdown
| siliconflow | https://api.siliconflow.cn/v1 | openai | ✅ Tested | 2026-09-15 | 发现 X 个模型，Y 个 `:free` 标记 |
```

**失败场景示例：**

- **API Key 无效：**
  ```markdown
  | siliconflow | ... | openai | ❌ Test Failed | 2026-09-15 | 401 Unauthorized |
  ```

- **端点不兼容：**
  ```markdown
  | siliconflow | ... | openai | ⚠️ Incompatible | 2026-09-15 | /models 返回非标准格式 |
  ```

---

## Zhipu (智谱 AI) 模板实测

### 1. 创建模板
```bash
curl -X POST http://localhost:8782/api/free-discovery/templates \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "provider_code": "zhipu",
    "api_key_env": "$ZHIPU_API_KEY",
    "enabled": true
  }'
```

### 2-4. 重复 SiliconFlow 的步骤 2-4

**Zhipu 特定验证点：**
- Base URL: `https://open.bigmodel.cn/api/paas/v4`
- API Dialect: `openai` (兼容层)
- 预期模型：GLM 系列（glm-4-flash, glm-4-plus, etc.）

编辑 `docs/freediscovery-configuration.md` 第 265 行：

```markdown
| zhipu | https://open.bigmodel.cn/api/paas/v4 | openai | ✅ Tested | 2026-09-15 | 发现 X 个模型 |
```

---

## 故障排查

### 连续失败 3 次触发自动禁用
```bash
# 查看被自动禁用的模板
curl http://localhost:8782/api/free-discovery/templates \
  -H "Authorization: Bearer $ADMIN_TOKEN" | \
  jq '.[] | select(.auto_disabled_at != null)'

# 重新启用
curl -X PATCH http://localhost:8782/api/free-discovery/templates/<id> \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"enabled": true}'
```

### 环境变量未生效
```bash
# 验证网关进程能读取到环境变量
ps -eww | grep llm-gateway | grep SILICONFLOW_API_KEY

# 如果看不到，说明环境变量未传递给进程，需要重启网关
```

### 扫描任务挂起
```bash
# 查看任务状态
curl http://localhost:8782/api/free-discovery/tasks?limit=10 \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# 检查网关日志
tail -f logs/llm-gateway.log | grep FreeDiscovery
```

---

## 预设配置参考

### SiliconFlow 预设
```go
{
    ProviderCode:  "siliconflow",
    BaseURL:       "https://api.siliconflow.cn/v1",
    ModelsEndpoint: "/models",
    APIDialect:    "openai",
    TosVerdict:    "ok",
    // FreeOf: 通过 scanner 检测 `:free` 标记
}
```

### Zhipu 预设
```go
{
    ProviderCode:  "zhipu",
    BaseURL:       "https://open.bigmodel.cn/api/paas/v4",
    ModelsEndpoint: "/models",
    APIDialect:    "openai",
    TosVerdict:    "ok",
    // FreeOf: 通过 scanner 检测 `:free` 标记
}
```
