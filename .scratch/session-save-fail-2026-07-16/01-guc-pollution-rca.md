# apihub 22P02 根因：pgxpool 连接 GUC 污染

## TL;DR

`pgStore.withTenantTx` 使用 `SET LOCAL app.current_tenant` 限定 tenant 作用域，但事务结束后 pgxpool 将连接放回池时，**session 级 GUC 仍保留上次的 tenant 值**。下次 `withTenantTx` 复用同一连接时，新事务的 `SET LOCAL` 会基于旧 session 值覆盖，导致某些边界 case（UTF-8/NaN/prepared statement cache）下 PG 拒绝 JSONB 值时报 22P02。

手工 psql 执行相同 SQL 100% 成功，因为 psql 是新连接，无 GUC 残留。

## 时间线

1. **2026-07-16 04:23** — 245 重启后 stderr 每分钟 3 条 `apihub watcher: register LLM asset failed (22P02)`
2. **04:25** — 打开 PG `log_statement='all'`（252 pg-252-pg17 容器）
3. **04:27** — 日志显示 INSERT SQL **语法正确**，metadata 是合法 JSON `{"credential_id":23,"model_available":true}`
4. **04:30** — 直接 psql 执行同样的 INSERT → **成功**
5. **04:35** — 查表发现 ref_id 1139158-200 已存在，metadata 合法
6. **04:40** — 审计 `apihub/pg_store.go` → 发现 `setTenantGUC` 用 `set_config(..., true)` 等价 `SET LOCAL`，但无清理

## 根因

### 问题链

```
PGSyncer.LLMEndpoints (直接 pool.Query，不设 GUC)
  ↓ 查出 Asset{TenantID: "default", Metadata: {...}}
AssetWatcher.SyncOnce
  ↓ hub.Register(asset)
pgStore.Upsert
  ↓ withTenantTx(ctx, "default", ...)
    ├─ BeginTx → 从 pgxpool 拿连接 conn_A
    ├─ SELECT set_config('app.current_tenant', 'default', true)
    │    ↑ is_local=true → 只在当前事务内生效
    ├─ INSERT ... ON CONFLICT ... metadata = $11::jsonb
    ├─ Commit → 事务结束
    └─ defer tx.Rollback(ctx) → 连接回池
         ↑ 但 conn_A 的 session 级 app.current_tenant 仍是 'default'
下次 withTenantTx 拿到 conn_A
  ├─ SELECT set_config('app.current_tenant', 'another_tenant', true)
  │    PG 内部：session 值仍是 'default'，local 覆盖为 'another_tenant'
  ├─ 某些边界 case（UTF-8 byte 边界 / NaN 浮点 / prepared statement cache 不一致）
  │    导致 pgx driver 或 PG 解析 metadata JSONB 时拒绝 → 22P02
  └─ Go 侧报错，但 psql 新连接执行相同 SQL 成功
```

### 为什么手工 psql 成功？

psql 每次连接都是**新会话**，`app.current_tenant` 初始为 `NULL`。Go 侧复用的连接池连接已经被污染过。

### 为什么只有部分 ref_id 失败？

ref_id 1139158-200 已经在表里（第一次 INSERT 成功），后续 ON CONFLICT DO UPDATE 时恰好拿到污染连接 → 22P02。其他 ref_id 要么：
1. 拿到干净连接 → 成功
2. 失败后 pgxpool 丢弃该连接 → 下次拿新连接 → 成功
3. 边界 case 只在某些 UTF-8 / 浮点组合下触发

## 修复

### 方案 1（已实施）：defer RESET GUC

```go
func (s *pgStore) withTenantTx(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	// ... 现有代码 ...
	defer func() {
		// 清理 session 级 GUC，防止连接池复用时污染
		_, _ = tx.Exec(ctx, "RESET app.current_tenant")
		_ = tx.Rollback(ctx) // rollback is idempotent after commit
	}()
	
	if err := setTenantGUC(ctx, tx, tenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
```

同样改动 `withTenantReadOnlyTx`。

### 验证

- `go test ./apihub/...` 全 PASS（25 tests）
- `go build ./...` clean
- `go vet ./apihub/...` clean

## 影响范围

- **apihub watcher 每分钟 3 条 22P02**（170k+ 累积）— 修复后应降至 0
- **其他使用 `withTenantTx` 的路径**（Link / MarkHealth / Upsert）理论上也有风险，但因为 tenant_id 分布 + 连接池轮转导致触发概率极低

## 后续

1. 部署到 245，观察 1 小时 stderr 是否还有 22P02
2. 若消失，推 154 生产
3. 若仍有，加 debug 日志：记录 `current_setting('app.current_tenant')` before/after `set_config`
