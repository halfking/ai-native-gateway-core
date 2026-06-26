# 71 服务器问题快速诊断 - 命令速查表

## 🚀 快速开始

### 1. 最快诊断（1 分钟）

```bash
# 设置环境变量
export DB_HOST=<184-ip>
export DB_PORT=5432
export DB_USER=kxuser
export DB_NAME=llm_gateway
export DB_PASSWORD=<password>
export API_KEY=<your-api-key>

# 运行诊断
cd /path/to/llm-gateway-go-2
./scripts/test_71_routing.sh
```

### 2. 快速修复（2 分钟）

```bash
# 运行修复脚本
./scripts/fix_71_routing_complete.sh
```

---

## 📊 关键诊断 SQL

### 检查 1: 路由索引是否存在？

```sql
SELECT COUNT(*) FROM credential_model_index 
WHERE raw_model = 'minimax-m3' 
  AND bucket > now() - interval '10 minutes';
```

- **返回 0**: ❌ 索引为空，需要初始化
- **返回 > 0**: ✅ 索引存在

### 检查 2: Canonical ID 是否一致？

```sql
SELECT 'model_aliases' AS source, canonical_id FROM model_aliases WHERE raw_name = 'minimax-m3'
UNION ALL
SELECT 'provider_models', canonical_id FROM provider_models WHERE raw_model_name = 'minimax-m3' LIMIT 1;
```

- **两行相同**: ✅ 一致
- **两行不同**: ❌ 不一致，需要修复

### 检查 3: 是否有可路由的凭据？

```sql
SELECT COUNT(*) FROM v_routable_credential_models
WHERE raw_model_name = 'minimax-m3' AND is_routable = true;
```

- **返回 0**: ❌ 没有可用凭据
- **返回 > 0**: ✅ 有可用凭据

### 检查 4: 最近的请求是否成功？

```sql
SELECT 
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE success = true) AS success,
    COUNT(*) FILTER (WHERE error_kind = 'empty_response') AS empty_resp,
    COUNT(*) FILTER (WHERE request_status = 'no_candidate') AS no_candidate
FROM request_logs
WHERE ts > now() - interval '1 hour';
```

---

## 🔧 快速修复 SQL

### 修复 1: 初始化路由索引

```sql
INSERT INTO credential_model_index (
    bucket, credential_id, raw_model, canonical_id,
    active_sessions, concurrency_limit, success_rate, p95_latency_ms,
    score_smart, score_speed_first, score_cost_first, pressure_ratio,
    billing_mode, unit_price_in_per_1m, unit_price_out_per_1m
)
SELECT 
    date_trunc('minute', now() - interval '5 minutes'),
    cmb.credential_id,
    pm.raw_model_name,
    pm.canonical_id,
    0, COALESCE(c.concurrency_limit, 10), 0.95, 500,
    100.0, 100.0, 100.0, 0.0,
    COALESCE(mo.billing_mode, 'token'),
    COALESCE(mo.unit_price_in_per_1m, 0),
    COALESCE(mo.unit_price_out_per_1m, 0)
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
LEFT JOIN model_offers mo ON mo.credential_id = c.id AND mo.raw_model_name = pm.raw_model_name
WHERE cmb.available = true
  AND c.availability_state = 'ready'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND c.status != 'disabled'
  AND pm.raw_model_name = 'minimax-m3'
ON CONFLICT (bucket, credential_id, raw_model) DO NOTHING;
```

### 修复 2: 统一 Canonical ID

```sql
DO $$
DECLARE v_canonical_id INT;
BEGIN
    SELECT id INTO v_canonical_id FROM models_canonical WHERE canonical_name = 'minimax-m3';
    
    UPDATE model_aliases SET canonical_id = v_canonical_id WHERE raw_name = 'minimax-m3';
    UPDATE provider_models SET canonical_id = v_canonical_id WHERE raw_model_name = 'minimax-m3';
END $$;
```

### 修复 3: 激活凭据

```sql
-- 检查哪些凭据不可用
SELECT c.id, p.code, c.availability_state, c.lifecycle_status
FROM credentials c
JOIN providers p ON p.id = c.provider_id
WHERE p.code IN ('minimax', 'nvidia', 'minimax-anthropic')
  AND c.status != 'disabled';

-- 修复特定凭据（根据上面的结果）
UPDATE credentials
SET availability_state = 'ready',
    lifecycle_status = 'active'
WHERE id = <credential_id>;
```

---

## 🧪 测试命令

### 测试 1: 健康检查

```bash
curl -f https://llmgateway.internal.example.com/health
```

### 测试 2: 发送请求

```bash
curl -X POST https://llmgateway.internal.example.com/v1/chat/completions \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "hi"}],
    "max_tokens": 10
  }'
```

**成功**: HTTP 200 + JSON 响应  
**失败**: HTTP 503 + `{"error": {"code": "no_candidate"}}`

### 测试 3: 查看 Gateway 日志

```bash
# 查看路由日志
docker logs -f gateway 2>&1 | grep -E '(GetCandidates|routing|autoroute)'

# 查看最近的错误
docker logs --tail 100 gateway 2>&1 | grep -i error
```

---

## 🔍 问题决策树

```
请求失败
│
├─ 返回 503 no_candidate？
│  │
│  ├─ 是 → 路由问题
│  │  │
│  │  ├─ 检查 1: 路由索引是否为空？
│  │  │  ├─ 是 → 运行修复 1
│  │  │  └─ 否 → 继续
│  │  │
│  │  ├─ 检查 2: Canonical ID 一致？
│  │  │  ├─ 否 → 运行修复 2
│  │  │  └─ 是 → 继续
│  │  │
│  │  ├─ 检查 3: 有可路由凭据？
│  │  │  ├─ 否 → 运行修复 3
│  │  │  └─ 是 → 检查 Gateway 缓存
│  │  │
│  │  └─ Gateway 缓存
│  │     └─ 重启 Gateway 或等待 30 秒
│  │
│  └─ 否 → 继续
│
├─ 返回 credential_reveal_failed？
│  └─ 检查加密密钥配置
│
├─ 返回 200 但 request_logs 没记录？
│  └─ 检查 telemetry 配置和数据库连接
│
└─ 返回 200 但标记为 empty_response？
   └─ 部署最新代码（包含 commit 78de1295）
```

---

## 📋 一键修复脚本

### 完整修复（推荐）

```bash
cd /path/to/llm-gateway-go-2
export DB_HOST=<ip> DB_PORT=5432 DB_USER=kxuser DB_NAME=llm_gateway DB_PASSWORD=<pwd>
export API_KEY=<key>
./scripts/fix_71_routing_complete.sh
```

### 仅诊断（不修改）

```bash
./scripts/test_71_routing.sh
```

---

## 🚨 紧急情况

### 情况 1: 生产完全不可用

```sql
-- 立即初始化所有模型的路由索引
INSERT INTO credential_model_index (
    bucket, credential_id, raw_model, canonical_id,
    active_sessions, concurrency_limit, success_rate, p95_latency_ms,
    score_smart, score_speed_first, score_cost_first, pressure_ratio,
    billing_mode, unit_price_in_per_1m, unit_price_out_per_1m
)
SELECT DISTINCT ON (cmb.credential_id, pm.raw_model_name)
    date_trunc('minute', now() - interval '5 minutes'),
    cmb.credential_id,
    pm.raw_model_name,
    pm.canonical_id,
    0, COALESCE(c.concurrency_limit, 10), 0.95, 500,
    100.0, 100.0, 100.0, 0.0,
    COALESCE(mo.billing_mode, 'token'),
    COALESCE(mo.unit_price_in_per_1m, 0),
    COALESCE(mo.unit_price_out_per_1m, 0)
FROM credential_model_bindings cmb
JOIN credentials c ON c.id = cmb.credential_id
JOIN provider_models pm ON pm.id = cmb.provider_model_id
LEFT JOIN model_offers mo ON mo.credential_id = c.id AND mo.raw_model_name = pm.raw_model_name
WHERE cmb.available = true
  AND c.availability_state = 'ready'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND c.status != 'disabled'
ON CONFLICT (bucket, credential_id, raw_model) DO NOTHING;

-- 重启 Gateway
systemctl restart llm-gateway
```

### 情况 2: 特定模型不可用

```sql
-- 替换 'model-name' 为实际模型名
-- 运行修复 1 的 SQL，将 'minimax-m3' 替换为目标模型
```

---

## 📞 获取帮助

如果以上步骤都无法解决问题：

1. **收集诊断信息**:
   ```bash
   ./scripts/test_71_routing.sh > diagnosis.log 2>&1
   ```

2. **收集 Gateway 日志**:
   ```bash
   docker logs gateway --tail 500 > gateway.log 2>&1
   ```

3. **检查详细文档**:
   - [71_SERVER_ROUTING_FIX_GUIDE.md](./71_SERVER_ROUTING_FIX_GUIDE.md)
   - [MINIMAX_M3_FINAL_REPORT.md](./MINIMAX_M3_FINAL_REPORT.md)

---

**更新时间**: 2026-06-26
