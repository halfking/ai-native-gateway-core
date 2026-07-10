# /api/routing/resolve 接口 500 错误修复记录

**日期**: 2026-07-09  
**作者**: ZCode Agent  
**影响范围**: 路由解析诊断接口 `/api/routing/resolve?model=&persist_probe=1`

## 问题描述

请求 `https://llm.kxpms.cn/api/routing/resolve?model=glm-5.2&persist_probe=1` 返回 HTTP 500。

## 根本原因

```
ERROR: column v.credential_status does not exist (SQLSTATE 42703)
```

**调用链路**:
```
GET /api/routing/resolve?model=glm-5.2&persist_probe=1
  ↓ handleRoutingResolve (admin/routing.go:100)
  ↓ SQL 查询引用 v.xxx (视图中不存在的列)
  ↓ PostgreSQL 42703 错误 → HTTP 500
```

`v_routable_credential_models` 视图经 migration 327/332 重写后**不再暴露**下列列：

| 不存在的列 | 修复后的来源 |
|---|---|
| `v.credential_status` | `c.status` (credentials 表) |
| `v.credential_lifecycle_status` | `c.lifecycle_status` (credentials 表) |
| `v.availability_state` | `c.availability_state` (credentials 表) |
| `v.availability_recover_at::text` | `c.availability_recover_at::text` (credentials 表) |
| `v.quota_state` | `c.quota_state` (credentials 表) |
| `v.quota_recover_at::text` | `c.quota_recover_at::text` (credentials 表) |
| `v.binding_available` | `cmb.available` (credential_model_bindings 表) |
| `COALESCE(v.billing_mode, 'token')` | `COALESCE(cmb.billing_mode, 'token')` (cmb 表) |

## 修复内容

### 1. 修复 SQL 查询列引用 (admin/routing.go)

所有原查询的 `v.xxx` 引用改为从已 JOIN 的 `credentials c`、`credential_model_bindings cmb`、`providers p` 表中读取。

### 2. 增强 persistResolveProbe 容错 (admin/routing_resolve_probe.go)

- 添加 `defer recover()` 防止 panic
- 提升错误日志级别（Warn → Error），添加 migration 编号提示

### 3. 审计发现的过时视图定义

| 文件 | 问题 | 修复 |
|---|---|---|
| `deploy/sql/objects/views/v_routable_credential_models.sql` | 缺少 billing_mode/plan_type/plan_type_origin 列和 plan_type 兼容性检查 | 更新为匹配 migration 332 |
| `installer/cmd/llm-gw-installer/embeddata/01-schema.sql` | 同上，视图定义过时 | 同步更新 |

## 审计结果

- **admin/providers.go**: ✅ 仅引用 `is_routable`（视图中有） 
- **admin/provider_offer_force_recover.go**: ✅ 仅引用 `unavailable_reason`（视图中有）
- **bg/auto_index_refresher.go**: ✅ 仅引用 `v.credential_id`、`v.binding_id`、`v.provider_model_id`、`v.is_routable`（视图中有）
- **其他路由处理函数**: ✅ 均未使用 `v_routable_credential_models` 视图

## 部署记录

| 操作 | 状态 |
|---|---|
| 代码修复 | ✅ |
| Go vet / 编译验证 | ✅ |
| Linux amd64 交叉编译 | ✅ |
| 部署到 154 服务器 | ✅ 2026-07-09 11:57:33 |
| 服务运行状态 | ✅ active (running) |
| 备份旧二进制 | ✅ /opt/llm-gateway-go/llm-gateway-go.bak.20260709_1157XX |

## 建议

1. **定期同步视图定义**: 每次 migration 修改 `v_routable_credential_models` 后，同步更新 `deploy/sql/objects/views/` 和 `installer/embeddata/` 中的定义
2. **新增统一验证**: 在 CI 中增加检查，确保 Go 代码的 SQL 列引用与视图实际列一致
3. **防止重复**: deploy/objects 和 install/embeddata 中的视图定义应考虑合并为单一来源
