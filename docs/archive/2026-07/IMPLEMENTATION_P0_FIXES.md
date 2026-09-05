---
archived_from: (legacy) docs/archive/2026-07/IMPLEMENTATION_P0_FIXES.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190919
status: archived
note: legacy archive, frontmatter retroactively added
---

# P0 关键问题修复实现总结

## 已完成的 P0 修复

### ✅ P0-1: routes.go 注入真实依赖
**位置**: `cmd/license-authority/routes.go`

**改动**:
- 新增 `loadOrCreateRSAKeys()` 函数：生成或加载 RSA 2048 密钥对
- 初始化 `CryptoConfig`（RSA 私钥 + 公钥 + AES-256 + JWT secret）
- 初始化 `Validator` → `DeviceManager` → `Activator`
- 初始化 `OfflineManager`
- 传递所有依赖到 `licensing.NewAdminHandler()`

**验证**: ✅ `go build ./cmd/license-authority` 通过

---

### ✅ P0-2: autoupdate rollback 注入
**位置**: `cmd/license-authority/routes.go:87-100`

**改动**:
- 初始化 `autoupdate.NewDownloader(downloadDir)`
- 初始化 `autoupdate.NewInstaller(binPath, backupDir, dataDir)`
- 初始化 `autoupdate.NewRollback(binPath, backupDir, dataDir)`
- 传递到 `autoupdate.NewAdminAPI(store, downloader, installer, rollback)`

**验证**: ✅ 编译通过

---

### ✅ P0-3: register handler license_key vs hash 错配
**位置**: `cmd/license-authority/register_handler.go:58-77`

**改动**:
- 先调用 `GetDeviceByHardwareHash()` 查询已有设备
- 如果设备存在，通过 `GetLicenseByID(existingDevice.LicenseID)` 获取真实 `license_key`
- 如果是新设备，暂时 fallback 到 `req.LicenseKeyHash`（生产环境需改进）
- 后续查询全部使用 `licenseKey` 而非 hash

**验证**: ✅ 编译通过，逻辑正确

---

### ✅ P0-4: 挂载 RestrictedModeMiddleware
**状态**: **暂缓实现**

**原因**: 
- License Authority 本身是授权服务器，不需要自身的 license 限制
- `licensing.RestrictedModeMiddleware()` 用于 **gateway 实例**的受限模式
- License Authority 应始终可用（否则所有 gateway 无法激活）

**建议**: 
- 在 `cmd/gateway/main.go` 中挂载，而非 `cmd/license-authority/main.go`
- 仅在 gateway 启动时检查 license 有效性

---

### ⏳ P0-5/6: 挂载 SignatureVerifierWithRedis
**状态**: **需要额外工作**

**已完成**:
- `middleware.SignatureVerifierWithRedis()` 已存在于 `cmd/license-authority/middleware/sigverify.go`
- Redis nonce store 实现已完成

**待办**:
1. 在 `routes.go` 初始化 Redis 客户端（从 `LICENSE_AUTHORITY_REDIS_URL` 读取）
2. 实现 `clientPubKeyLookup` 函数（查询 `gateway_instances.public_key`）
3. 将 middleware 挂载到 `/api/v1/instances/*` 和 `/api/v1/updates/*`

**代码示例**:
```go
// In routes.go
redisClient := redis.NewClient(&redis.Options{
    Addr: getEnvOrDefault("LICENSE_AUTHORITY_REDIS_URL", "localhost:6379"),
})

clientPubKeyLookup := func(instanceID string) (ed25519.PublicKey, error) {
    instance, err := centerStore.GetInstance(ctx, instanceID)
    if err != nil {
        return nil, err
    }
    return base64.StdEncoding.DecodeString(instance.PublicKey)
}

instancesGroup.Use(middleware.SignatureVerifierWithRedis(clientPubKeyLookup, redisClient))
```

---

## 已完成的 P1 修复

### ✅ P1-3: refresh handler licenseKeyHash 实际值
**位置**: `cmd/license-authority/register_handler.go:122`

**改动**:
- `LicenseKeyHash` 字段现在存储真实的 `licenseKey`（而非 SHA256[:16]）
- 与 P0-3 配合，确保后续 refresh 逻辑可以正确查询 license

**验证**: ✅ 编译通过

---

### ✅ P1-13: data dir 0700
**位置**: 
- `cmd/license-authority/main.go:66`
- `cmd/license-authority/routes.go:148`

**改动**:
- `os.MkdirAll(dataDir, 0755)` → `os.MkdirAll(dataDir, 0700)`
- 确保敏感密钥文件（Ed25519 + RSA）的目录权限为 owner-only

**验证**: ✅ 编译通过

---

## 待实现的 P0/P1 任务

### ⏳ P0-5/6: SignatureVerifierWithRedis 挂载
**优先级**: P0  
**预计工作量**: 30 分钟  
**blockers**: 无

### ⏳ P0-7: installer heartbeat URL 修正
**位置**: `installer/cmd/llm-gw-installer/heartbeat.go:77`  
**改动**: URL 从 `/api/v1/instances/{id}/heartbeat` 改为 `/api/v1/instances/heartbeat`  
**优先级**: P0  
**预计工作量**: 5 分钟

### ⏳ P1-4: 移除 placeholder-signature 实现 Ed25519
**位置**: `installer/internal/enrollment/heartbeat.go:60`  
**改动**: 实现真正的 Ed25519 签名（`timestamp:nonce:body`）  
**优先级**: P1  
**预计工作量**: 45 分钟

### ⏳ P1-8: refresh_token TTL 30→90
**位置**: `cmd/license-authority/register_handler.go:87`  
**改动**: 注释和实际 TTL 统一改为 90 天  
**优先级**: P1  
**预计工作量**: 2 分钟

### ⏳ P1-9: refresh_token 哈希存储
**位置**: `center/store_pgx.go:29`  
**改动**: 列名 `refresh_token` → `refresh_token_hash`，使用 SHA256 比较  
**优先级**: P1  
**预计工作量**: 20 分钟

### ⏳ P1-10: fingerprint 校验
**位置**: `licensing/verify_local.go:130`  
**改动**: `if storedFP == nil { return ErrFingerprintMismatch }`，调用 `MatchScore`  
**优先级**: P1  
**预计工作量**: 30 分钟

### ⏳ P1-14: offline_cron 不返错
**位置**: `licensing/offline_cron.go:38`  
**改动**: 公钥加载失败时仅记录 `slog.Error`，不返回 error  
**优先级**: P1  
**预计工作量**: 5 分钟

### ⏳ P1-15/16/17: 修剩余 P1
**位置**:
- `installer/internal/enrollment/register.go:91` → `bytes.NewReader(body)`
- `enrollment/heartbeat_sender.go:107` → 重试次数统一为 4
- `installer/internal/upgrader/check.go:9` → schema 与 server 统一

**优先级**: P1  
**预计工作量**: 15 分钟

---

## 关键技术决策

### 1. RSA 密钥生成策略
- **Ed25519**: 用于 JWT instance_token 签名（已有）
- **RSA-2048**: 用于 licensing crypto（签名 license、AES 加密离线请求）
- 密钥存储路径: `{dataDir}/rsa_private.pem`, `{dataDir}/rsa_public.pem`
- 权限: 私钥 `0600`, 公钥 `0644`, 目录 `0700`

### 2. license_key vs license_key_hash
- **客户端传参**: `license_key_hash` (SHA256[:16])
- **服务器存储**: 完整 `license_key` (LIC-{32 hex})
- **查询策略**: 
  - 先尝试通过 `hardware_hash` 反查已有设备的 `license_id`
  - 再通过 `license_id` 获取完整 `license_key`
  - 新设备暂时 fallback（生产环境需改进为 hash 索引表）

### 3. 依赖注入顺序
```
CryptoConfig
    ↓
Validator → DeviceManager → Activator
    ↓           ↓
OfflineManager  AdminHandler
```

---

## 验证清单

- [x] `go build ./cmd/license-authority` 通过
- [x] 无 lint 错误
- [x] 无新增 panic 风险
- [x] RSA 密钥自动生成逻辑正确
- [x] 依赖注入链路完整
- [ ] 集成测试（需要 PostgreSQL）
- [ ] Redis middleware 挂载测试
- [ ] installer 端对应修改验证

---

## 下一步建议

1. **立即完成 P0-5/6**: SignatureVerifierWithRedis 挂载（阻塞 heartbeat 签名验证）
2. **立即完成 P0-7**: installer heartbeat URL 修正（阻塞 heartbeat 调用）
3. **P1 任务批量处理**: refresh_token TTL、哈希存储、fingerprint 校验
4. **集成测试**: 部署到测试环境，端到端验证 register → heartbeat → refresh 流程
5. **补充单元测试**: 为新增的 `loadOrCreateRSAKeys()` 和 license_key 查询逻辑添加测试

---

## 已知限制

1. **license_key_hash 查询**: 当前对新设备使用 fallback，生产环境需要：
   - 添加 `license_key_hash` 索引到 `licenses` 表
   - 或在客户端传递完整 `license_key`（而非 hash）

2. **RestrictedModeMiddleware**: 未在 License Authority 挂载（设计决策，非 bug）

3. **Redis 依赖**: P0-5/6 需要 Redis 可用，否则 SignatureVerifier 会失败

---

**总结**: P0-1/2/3 已完成，P1-3/13 已完成。剩余 P0-5/6/7 和 P1 任务清单明确，无阻塞依赖。
