# 2026-07-15 provider_model_bindings → credential_model_bindings 修复

## 问题
154 生产环境 node_probe_state 显示所有 minimax credentials 报错：
```
ERROR: relation "provider_model_bindings" does not exist (SQLSTATE 42P01)
```

## 根因
代码中 5 处 SQL 查询仍使用旧表名 `provider_model_bindings`，但 154 PG 只有 `credential_model_bindings` 表。

## 修复
### 源代码修改 (v1058: cdd808352)
1. `bg/node_probe.go:530` — resolveDirectTarget JOIN clause
2. `bg/credential_selfcheck.go:369, 411` — pickModels 两处 JOIN
3. `bg/self_check_worker.go:333, 627` — topNModels + isolateUpstream

所有 `provider_model_bindings pmb` 改为 `credential_model_bindings cmb`。

### 验证结果
- ✅ v1058 binary 中 `provider_model_bindings` 字符串出现 0 次
- ✅ v1058 binary 部署到 154 (PID 11002)
- ✅ 4/5 minimax credentials 修复为 `http_404` (endpoint 正常)
- ⚠️ 1/5 间歇性仍报错（pgx statement cache 残留）

## Pgx Statement Cache 问题
**现象**：同一 credential/model 在相同 binary 下结果时对时错：
- 18:35:28 cred 21: `http_404` ✓
- 18:36:40 cred 21: `endpoint_build` + `provider_model_bindings` ❌

**根因**：pgx 驱动 per-connection statement cache 缓存了旧 SQL。连接池轮转时，命中未刷新的旧连接 → 执行旧 prepared statement → 报错。

**解决方案**：
1. 重启进程清空连接池（已执行）
2. 长连接会在生命周期内自然刷新（24h 连接超时）
3. 下次部署前考虑禁用 statement cache 或使用 `DISCARD ALL`

## 部署记录
- 版本：2.4.6-cdd808352-20260715-1058
- 服务器：47.97.111.154:25022
- 部署时间：2026-07-15 18:34
- 状态：✅ 运行中，4/5 credentials 修复

## 遗留工作
- [ ] 监控 cred 21 在未来 24h 内是否自动恢复
- [ ] 评估是否需要在连接池配置中禁用 statement cache
- [ ] 考虑在 main.go 启动时执行 `DISCARD ALL` 清除旧连接

## 教训
1. **表重命名需要全局搜索**：`provider_model_bindings` → `credential_model_bindings` 应该在第一次重命名时就全局替换，避免遗留
2. **prepared statement cache 是隐形坑**：binary 更新后，连接池中的旧 statement 可能存活数小时，导致间歇性错误
3. **PG 连接池需要重启策略**：schema 变更后，考虑强制重启所有连接或禁用 statement cache
