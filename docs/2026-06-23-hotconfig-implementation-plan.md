# 配置化与热更新实施计划（2026-06-23）

## ✅ 已完成

1. **审计 Acquire/Release 配对** — 所有路径正确配对
2. **集成 identityPool 到 executor** — Layer 0 全局身份池已接入
3. **创建 hotconfig 包** — 基于 settings_kv 的热加载配置系统
4. **文档化可配置参数** — 23 个参数定义完成

## 🔲 待完成（按优先级）

### P0: 核心集成（必须）

- [ ] **credentialfpslot 集成 hotconfig**
  - 修改 `slot.go` 中的硬编码常量为 `cfg.GetInt()`
  - `slotTTLSeconds` → `cfg.GetInt("llmgw_slot_ttl_seconds", 86400)`
  - `sessionPinTTLSeconds` → `cfg.GetInt("llmgw_session_pin_ttl_seconds", 86400)`
  - Manager 结构体添加 `cfg *hotconfig.Config` 字段

- [ ] **identitypool 集成 hotconfig**
  - Pool 结构体添加 `cfg *hotconfig.Config` 字段
  - `MaxIdentities` 从配置读取
  - `LRUWindow` 从配置读取

- [ ] **limiter 集成 hotconfig**
  - 并发限制从配置读取

- [ ] **cmd/gateway/main.go 启动 hotconfig**
  ```go
  cfg := hotconfig.New(db.Pool())
  cfg.Start(ctx)
  defer cfg.Stop()
  
  // 传递给各个组件
  fpSlots := credentialfpslot.NewManager(..., cfg)
  identityPool := identitypool.New(..., cfg)
  ```

### P1: 数据库初始化

- [ ] **创建 migration 037_hotconfig_defaults.sql**
  - 插入所有 23 个默认配置项
  - ON CONFLICT DO NOTHING 确保幂等

### P2: Admin API 扩展

- [ ] **扩展现有 settings API 支持 llmgw_ 前缀**
  - GET /api/admin/settings?prefix=llmgw_
  - PUT /api/admin/settings (批量更新)
  - 前端 UI 已存在，只需后端支持过滤

### P3: 测试

- [ ] **hotconfig 单元测试**
  - TestReload
  - TestGetInt/GetString/GetBool
  - TestSet/Delete
  
- [ ] **集成测试**
  - 修改配置 → 30秒后生效
  - 验证 FpSlots TTL 变化

### P4: 监控

- [ ] **添加 metrics**
  - `hotconfig_reload_total`
  - `hotconfig_reload_errors_total`
  - `hotconfig_last_reload_timestamp`

## 🚧 技术债务

1. **globalIdentity 未传递到 credentialfpslot**
   - 当前 `_ = globalIdentity` 占位
   - 需要修改 `Lease` 结构体添加 `GlobalIdentity` 字段
   - 需要修改 `identity.BuildEgressIdentity` 使用 globalIdentity

2. **settings_kv 表性能**
   - 30秒 poll 对 DB 压力小
   - 但如果配置项超过 100 个，考虑加索引或缓存

3. **租户级配置覆盖**
   - 当前只支持 platform-level
   - 未来支持 tenant_settings_kv 覆盖

## 📊 预计工作量

| 任务 | 预计时间 | 优先级 |
|------|---------|--------|
| credentialfpslot 集成 | 30min | P0 |
| identitypool 集成 | 20min | P0 |
| limiter 集成 | 20min | P0 |
| main.go 启动 | 15min | P0 |
| migration 037 | 10min | P1 |
| Admin API 扩展 | 30min | P2 |
| 测试 | 1h | P3 |
| **总计** | **~3h** | — |

## 🎯 下次会话目标

1. 完成 P0 所有集成
2. 创建 migration 037
3. 测试端到端流程
4. 提交推送

## 📝 提交信息模板

```
feat(config): add hot-reloadable configuration system

1. New hotconfig package:
   - Polls settings_kv every 30s
   - Atomic reload without restart
   - Thread-safe Get*/Set/Delete

2. Integrated into:
   - credentialfpslot (slot TTL, pin TTL)
   - identitypool (max identities, LRU window)
   - limiter (concurrency limits)

3. Added migration 037: 23 default config keys

4. Docs:
   - docs/2026-06-23-configurable-parameters.md
   - docs/2026-06-23-acquire-release-audit.md

Files:
- hotconfig/hotconfig.go (new)
- routing/executor.go (identityPool integration)
- credentialfpslot/slot.go (use hotconfig)
- identitypool/pool.go (use hotconfig)
- db/migrations/037_hotconfig_defaults.sql (new)
```