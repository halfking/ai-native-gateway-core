# llm-gateway-go 可配置参数列表（2026-06-23）

## 配置层级

所有配置存储在 `settings_kv` 表中，key 前缀为 `llmgw_`。

## credentialfpslot（指纹槽）

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `llmgw_slot_ttl_seconds` | int | 86400 | 指纹槽 TTL（24小时） |
| `llmgw_session_pin_ttl_seconds` | int | 86400 | 会话 pin TTL（24小时） |
| `llmgw_default_fp_slot_limit` | int | 5 | 默认指纹池大小（每凭据） |

## identitypool（全局身份池）

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `llmgw_max_identities` | int | 10000 | 全局身份上限 |
| `llmgw_lru_window_seconds` | int | 86400 | LRU 回收窗口（24小时） |
| `llmgw_identity_pool_enabled` | bool | true | 是否启用全局身份池 |

## limiter（并发限制）

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `llmgw_default_concurrency_limit` | int | 10 | 默认并发限制（每凭据） |
| `llmgw_global_limit` | int | 1000 | 全局并发限制 |
| `llmgw_pool_limit` | int | 100 | 池级别限流 |

## circuit breaker（熔断器）

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `llmgw_circuit_threshold` | int | 5 | 熔断阈值（连续失败次数） |
| `llmgw_circuit_timeout_seconds` | int | 60 | 熔断超时（60秒） |
| `llmgw_circuit_half_open_max_calls` | int | 3 | 半开状态最大尝试次数 |

## disguise（伪装）

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `llmgw_disguise_enabled` | bool | true | 是否启用 UA 漂移 |
| `llmgw_disguise_rotation_interval_seconds` | int | 300 | UA 轮换间隔（5分钟） |
| `llmgw_tlsfp_enabled` | bool | true | 是否启用 TLS 指纹伪装 |

## 其他

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `llmgw_sticky_ttl_seconds` | int | 1800 | Sticky 绑定 TTL（30分钟） |
| `llmgw_mnf_streak_threshold` | int | 3 | Model Not Found 连续阈值 |
| `llmgw_health_probe_interval_seconds` | int | 300 | 健康探测间隔（5分钟） |

## 初始化 SQL

```sql
-- credentialfpslot
INSERT INTO settings_kv (key, value) VALUES
  ('llmgw_slot_ttl_seconds', '86400'),
  ('llmgw_session_pin_ttl_seconds', '86400'),
  ('llmgw_default_fp_slot_limit', '5')
ON CONFLICT (key) DO NOTHING;

-- identitypool
INSERT INTO settings_kv (key, value) VALUES
  ('llmgw_max_identities', '10000'),
  ('llmgw_lru_window_seconds', '86400'),
  ('llmgw_identity_pool_enabled', 'true')
ON CONFLICT (key) DO NOTHING;

-- limiter
INSERT INTO settings_kv (key, value) VALUES
  ('llmgw_default_concurrency_limit', '10'),
  ('llmgw_global_limit', '1000'),
  ('llmgw_pool_limit', '100')
ON CONFLICT (key) DO NOTHING;

-- circuit breaker
INSERT INTO settings_kv (key, value) VALUES
  ('llmgw_circuit_threshold', '5'),
  ('llmgw_circuit_timeout_seconds', '60'),
  ('llmgw_circuit_half_open_max_calls', '3')
ON CONFLICT (key) DO NOTHING;

-- disguise
INSERT INTO settings_kv (key, value) VALUES
  ('llmgw_disguise_enabled', 'true'),
  ('llmgw_disguise_rotation_interval_seconds', '300'),
  ('llmgw_tlsfp_enabled', 'true')
ON CONFLICT (key) DO NOTHING;

-- other
INSERT INTO settings_kv (key, value) VALUES
  ('llmgw_sticky_ttl_seconds', '1800'),
  ('llmgw_mnf_streak_threshold', '3'),
  ('llmgw_health_probe_interval_seconds', '300')
ON CONFLICT (key) DO NOTHING;
```

## 热更新方式

### 方式 1: Admin API

```bash
curl -X PUT https://llmgo.kxpms.cn/api/admin/settings \
  -H "Authorization: Bearer <token>" \
  -d '{
    "llmgw_slot_ttl_seconds": 43200
  }'
```

### 方式 2: SQL 直接修改

```sql
UPDATE settings_kv
SET value = '43200'::jsonb, updated_at = now()
WHERE key = 'llmgw_slot_ttl_seconds';
```

配置在 30 秒内生效（config poller 周期）。

## 使用示例

```go
// 在 cmd/gateway/main.go 中
cfg := config.New(db.Pool())
cfg.Start(ctx)
defer cfg.Stop()

// 在各个包中读取
ttl := cfg.GetInt("llmgw_slot_ttl_seconds", 86400)
enabled := cfg.GetBool("llmgw_identity_pool_enabled", true)
```