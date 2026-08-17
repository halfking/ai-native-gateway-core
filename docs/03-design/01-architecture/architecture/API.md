# LLM Gateway Go — API 文档

本文档描述主控端 `llm.kxpms.cn:8443` 对外暴露的 8 个 API 端点。

---

## 目录

- [License 管理](#license-管理)
  - [POST /api/v1/license/trial](#post-apiv1licensetrial)
  - [POST /api/v1/license/activate](#post-apiv1licenseactivate)
- [实例管理](#实例管理)
  - [POST /api/v1/instances/register](#post-apiv1instancesregister)
  - [POST /api/v1/instances/heartbeat](#post-apiv1instancesheartbeat)
  - [POST /api/v1/instances/refresh](#post-apiv1instancesrefresh)
- [升级管理](#升级管理)
  - [GET /api/v1/updates/latest](#get-apiv1updateslatest)
  - [GET /api/v1/updates/manifest](#get-apiv1updatesmanifest)
  - [POST /api/v1/updates/report](#post-apiv1updatesreport)

---

## License 管理

### POST /api/v1/license/trial

申请 7 天试用 License。

#### 请求

```bash
curl -X POST https://llm.kxpms.cn/api/v1/license/trial \
  -H "Content-Type: application/json" \
  -d '{
    "email": "user@example.com",
    "instance_id": "550e8400-e29b-41d4-a716-446655440000",
    "hardware_hash": "sha256:abc123...",
    "hostname": "prod-app-01",
    "ip_address": "10.0.0.5"
  }'
```

#### 响应

```json
{
  "license_key": "LIC-1a2b3c4d5e6f7g8h9i0j1k2l3m4n5o6p",
  "license_data": "base64_encoded_signed_license_dat",
  "expires_at": "2026-07-19T00:00:00Z",
  "max_tenants": 1,
  "max_devices": 1,
  "features": ["basic_api"]
}
```

#### 错误码

| HTTP | code | 含义 |
|------|------|------|
| 400 | invalid_request | email/instance_id 缺失 |
| 429 | rate_limited | 同 email 1 天内只能申请 1 次 |
| 500 | internal_error | 服务端故障 |

---

### POST /api/v1/license/activate

使用 License Key 在线激活。

#### 请求

```bash
curl -X POST https://llm.kxpms.cn/api/v1/license/activate \
  -H "Content-Type: application/json" \
  -d '{
    "license_key": "LIC-1a2b3c4d5e6f7g8h9i0j1k2l3m4n5o6p",
    "instance_id": "550e8400-e29b-41d4-a716-446655440000",
    "hardware_hash": "sha256:abc123...",
    "hostname": "prod-app-01",
    "ip_address": "10.0.0.5",
    "public_key": "base64_ed25519_public_key"
  }'
```

#### 响应

```json
{
  "license_data": "base64_encoded_signed_license_dat",
  "instance_token": "eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9...",
  "server_public_key": "base64_ed25519_server_pubkey",
  "expires_at": "2027-07-12T00:00:00Z",
  "max_tenants": 10,
  "max_devices": 5,
  "features": ["multi_tenant", "auto_update", "mcp_gateway"]
}
```

#### 错误码

| HTTP | code | 含义 |
|------|------|------|
| 400 | invalid_request | license_key/hardware_hash 缺失 |
| 403 | license_revoked | License 已撤销 |
| 409 | device_limit_exceeded | 超过 max_devices 限制 |
| 410 | license_expired | License 已过期 |

---

## 实例管理

### POST /api/v1/instances/register

实例首次启动时注册到主控端。

#### 请求

```bash
curl -X POST https://llm.kxpms.cn/api/v1/instances/register \
  -H "Content-Type: application/json" \
  -d '{
    "instance_id": "550e8400-e29b-41d4-a716-446655440000",
    "hostname": "prod-app-01",
    "ip_address": "10.0.0.5",
    "region": "cn-north-1",
    "version": "v2.4.2",
    "build_seq": 975,
    "license_key": "LIC-1a2b3c4d5e6f7g8h9i0j1k2l3m4n5o6p",
    "hardware_hash": "sha256:abc123...",
    "public_key": "base64_ed25519_public_key"
  }'
```

#### 响应

```json
{
  "instance_token": "eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9...",
  "server_public_key": "base64_ed25519_server_pubkey",
  "heartbeat_interval_secs": 60,
  "upgrade_check_interval_secs": 21600
}
```

#### 错误码

| HTTP | code | 含义 |
|------|------|------|
| 400 | invalid_request | instance_id/license_key 缺失 |
| 401 | invalid_signature | hardware_hash 与 license 不匹配 |
| 403 | license_invalid | License 无效或过期 |

---

### POST /api/v1/instances/heartbeat

实例每 60s 上报一次心跳。

#### 请求

```bash
curl -X POST https://llm.kxpms.cn/api/v1/instances/heartbeat \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9..." \
  -H "X-Instance-ID: 550e8400-e29b-41d4-a716-446655440000" \
  -H "X-Signature: ed25519_signature_of_body" \
  -H "X-Timestamp: 1720800000" \
  -d '{
    "instance_id": "550e8400-e29b-41d4-a716-446655440000",
    "version": "v2.4.2",
    "uptime_secs": 86400,
    "go_version": "go1.22",
    "num_goroutine": 120,
    "alloc_mb": 512.0,
    "total_alloc_mb": 1024.0,
    "sys_mb": 2048.0,
    "cpu_cores": 8,
    "current_concurrency": 32,
    "last_5min_tps": 12.5,
    "last_5min_p50_ms": 380,
    "last_5min_p99_ms": 1100,
    "last_5min_success_pct": 99.5
  }'
```

#### 响应

```json
{
  "status": "ok",
  "next_heartbeat_secs": 60
}
```

#### 状态判定

- `now - last_heartbeat ≤ 30s` → **online**
- `30s < now - last_heartbeat ≤ 2min` → **degraded**
- `> 2min` → **offline**

#### 错误码

| HTTP | code | 含义 |
|------|------|------|
| 401 | invalid_token | instance_token 过期或无效 |
| 401 | invalid_signature | X-Signature 签名错误 |
| 429 | rate_limited | 心跳频率超过 60/min |

---

### POST /api/v1/instances/refresh

刷新 instance_token（24h TTL）。

#### 请求

```bash
curl -X POST https://llm.kxpms.cn/api/v1/instances/refresh \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9..." \
  -d '{
    "instance_id": "550e8400-e29b-41d4-a716-446655440000",
    "license_key": "LIC-1a2b3c4d5e6f7g8h9i0j1k2l3m4n5o6p",
    "hardware_hash": "sha256:abc123..."
  }'
```

#### 响应

```json
{
  "instance_token": "eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9...",
  "expires_at": "2026-07-13T17:00:00Z"
}
```

---

## 升级管理

### GET /api/v1/updates/latest

检查当前实例可升级的最新版本（每 6h 自动调用）。

#### 请求

```bash
curl -X GET 'https://llm.kxpms.cn/api/v1/updates/latest?current_version=v2.4.2&channel=stable' \
  -H "Authorization: Bearer eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9..." \
  -H "X-Instance-ID: 550e8400-e29b-41d4-a716-446655440000"
```

#### 响应

```json
{
  "version": "v2.5.0",
  "build_seq": 1020,
  "channel": "stable",
  "mandatory": false,
  "manifest_url": "https://llm.kxpms.cn/api/v1/updates/manifest?v=2.5.0",
  "sha256": "abc123def456...",
  "size_mb": 128,
  "release_notes_url": "https://llm.kxpms.cn/release-notes/v2.5.0",
  "release_date": "2026-07-15T00:00:00Z"
}
```

#### 无更新时

```json
{
  "up_to_date": true,
  "current_version": "v2.4.2"
}
```

---

### GET /api/v1/updates/manifest

下载升级包清单（包含二进制、镜像、SQL 迁移）。

#### 请求

```bash
curl -X GET 'https://llm.kxpms.cn/api/v1/updates/manifest?v=2.5.0' \
  -H "Authorization: Bearer eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9..."
```

#### 响应

```json
{
  "version": "v2.5.0",
  "files": [
    {
      "path": "bin/gateway",
      "url": "https://llm.kxpms.cn/releases/v2.5.0/gateway-linux-amd64",
      "sha256": "abc123...",
      "size_bytes": 54000000,
      "mode": "0755"
    },
    {
      "path": "docker-images.tar.gz",
      "url": "https://llm.kxpms.cn/releases/v2.5.0/docker-images.tar.gz",
      "sha256": "def456...",
      "size_bytes": 128000000
    },
    {
      "path": "sql/migrations/378_new_feature.sql",
      "url": "https://llm.kxpms.cn/releases/v2.5.0/migrations.tar.gz",
      "sha256": "ghi789...",
      "size_bytes": 10240
    }
  ],
  "pre_upgrade_commands": [
    "systemctl stop kx-gateway.service"
  ],
  "post_upgrade_commands": [
    "systemctl start kx-gateway.service"
  ]
}
```

---

### POST /api/v1/updates/report

升级完成后回写结果到主控端。

#### 请求（成功）

```bash
curl -X POST https://llm.kxpms.cn/api/v1/updates/report \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9..." \
  -d '{
    "instance_id": "550e8400-e29b-41d4-a716-446655440000",
    "from_version": "v2.4.2",
    "to_version": "v2.5.0",
    "status": "success",
    "duration_ms": 45000,
    "upgraded_at": "2026-07-12T18:00:00Z"
  }'
```

#### 请求（失败）

```bash
curl -X POST https://llm.kxpms.cn/api/v1/updates/report \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9..." \
  -d '{
    "instance_id": "550e8400-e29b-41d4-a716-446655440000",
    "from_version": "v2.4.2",
    "to_version": "v2.5.0",
    "status": "failed",
    "error": "sha256 checksum mismatch",
    "duration_ms": 5000,
    "rolled_back": true,
    "upgraded_at": "2026-07-12T18:00:00Z"
  }'
```

#### 响应

```json
{
  "status": "recorded",
  "next_check_secs": 21600
}
```

---

## 通用错误响应

所有端点失败时返回统一格式：

```json
{
  "error": {
    "code": "invalid_request",
    "message": "Missing required field: instance_id",
    "detail": "instance_id is required for registration"
  }
}
```

### 错误码汇总

| HTTP | code | 含义 | 客户端处理 |
|------|------|------|----------|
| 400 | invalid_request | 字段缺失或格式错误 | 终止，检查请求参数 |
| 401 | invalid_token | Token 过期或无效 | 重新 register |
| 401 | invalid_signature | 签名验证失败 | 重新生成密钥对 |
| 403 | license_revoked | License 已撤销 | 提示用户续费 |
| 403 | license_invalid | License 无效或过期 | 进入试用/降级模式 |
| 409 | device_limit_exceeded | 超过设备数限制 | 提示先停用旧设备 |
| 410 | license_expired | License 已过期 | 进入社区版模式 |
| 429 | rate_limited | 请求过频 | 指数退避（5/30/120s） |
| 500 | internal_error | 服务端故障 | 重试 3 次 |
| 503 | main_control_offline | 主控端不可达 | 离线模式（使用本地缓存） |

---

## 认证与签名

### instance_token (JWT)

所有 `/api/v1/instances/*` 和 `/api/v1/updates/*` 端点需要在 Header 中携带：

```
Authorization: Bearer <instance_token>
X-Instance-ID: <uuid>
X-Signature: <ed25519_signature>
X-Timestamp: <unix_timestamp>
```

### Ed25519 签名

签名计算：

```
payload = request_body (JSON string)
signature = ed25519_sign(payload, client_private_key)
X-Signature = base64_encode(signature)
```

### 重放防护

- 服务端拒绝 `|now - X-Timestamp| > 300s` 的请求
- nonce 缓存 5 分钟（Redis）

---

## 离线模式

当主控端不可达（503 / TCP timeout）时，客户端进入离线模式：

- 使用本地缓存的 `license.dat`（24h 宽限期）
- 使用本地缓存的 `manifest.json`
- 超过 24h 进入受限模式（1 租户 / 仅基础 API）

---

## 速率限制

| 端点 | 限制 |
|------|------|
| `/api/v1/license/trial` | 1 次/天/email |
| `/api/v1/license/activate` | 10 次/天/license_key |
| `/api/v1/instances/heartbeat` | 60 次/分钟/instance |
| `/api/v1/updates/latest` | 100 次/天/instance |

---

## 相关文档

- [部署指南](DEPLOYMENT.md) — M1-M4 四种部署模式
- [升级指南](UPGRADE.md) — 在线/离线升级流程
- [架构文档](architecture/ARCHITECTURE.md) — 系统架构设计
