# 用户详情抽屉：API 密钥管理

## 概述

`/users` 页面点击用户行弹出的详情抽屉里，管理员（超管 / 租户管理员）可以
查看该用户名下的**全部** API 密钥、查看每把密钥的完整信息、签发新密钥、
启用/禁用/吊销已有密钥。

> ⚠ 本文档描述的是 **2026-11-17 审计修复轮之后**的行为。
> 上一轮（`eb7601089`）的实现有 8 个缺陷（含「签发必然失败」「明文密钥跨用户残留」），
> 已全部修复。审计与证据见
> [`docs/audit/2026-11-17-用户详情API密钥管理-审计轮.md`](../audit/2026-11-17-用户详情API密钥管理-审计轮.md)。

## 功能

### 统计标签页（原有）

KPI（请求 / Token / 积分 / 失败率·P95）、请求与 Token 趋势、Top 模型与 Top 应用、
最近请求、基本信息。「高频密钥」行里的密钥名可点击，跳到租户详情页并高亮对应行
（仅超管，因为 `/tenants/:tenantId` 挂了 `requiresSuper` 路由门）。

### API 密钥标签页（新增）

| 能力 | 说明 |
|---|---|
| 列出全部密钥 | 按 `owner_user` + `tenant_id` **服务端过滤**（超管不会把全站密钥拉到浏览器） |
| 完整信息 | 前缀、别名、应用、状态、请求数、Token、预算、速率限制、最后使用、创建时间、备注 |
| 查看明文 | 「查看完整密钥」→ 调 `GET /api/keys/{id}/reveal`（后端写审计日志 `apikey.secret_revealed`） |
| 签发密钥 | 应用代码 / 别名 / 预算 / RPM / 备注，创建后展示明文（仅此一次） |
| 启用 / 禁用 | `PATCH /api/keys/{id}/enable` / `/disable` |
| 吊销 | `DELETE /api/keys/{id}`（二次确认；不可恢复） |

## 状态语义（重要）

密钥状态按 `api_keys.status` 判定（取值域来自表约束
`sql/objects/tables/api_keys.sql`：`active / pending / disabled / throttled / revoked`）：

| status | 徽章 | 能否启用 | 能否吊销 | 说明 |
|---|---|---|---|---|
| `active` | 绿「正常」 | — | ✅ | 正常可用 |
| `pending` | 黄「待审核」 | ✅ | ✅ | 已提交待审核 |
| `throttled` | 黄「已限流」 | ✅ | ✅ | 仍可用，但被限流 |
| `disabled` | 红「已禁用」 | ✅ | ✅ | 临时禁用，可恢复 |
| `revoked` | 灰「已吊销」 | ❌ | ❌ | **终态**，不可恢复，需重新签发 |

★ 「已吊销」与「已禁用」必须区分：吊销是终态，对它点「启用」后端会返回 **409**
（`setKeyEnabled` 只写 `enabled` 而不写 `status`，吊销语义不会被偷偷推翻）。

## 权限

| 角色 | 能看到用户列表 | 能管该用户的密钥 |
|---|---|---|
| `super_admin` | 全站 | ✅ |
| `tenant_admin` | 本租户 | ✅ |
| 其它 | ❌（后端 `requireUserManager` 拒绝） | — |

租户管理员**不需要** `requiresSuper` 路由即可使用 —— 抽屉内的密钥标签页不依赖
`/tenants/:tenantId` 路由。

## 数据面

| 端点 | 用途 | 权限 |
|---|---|---|
| `GET /api/admin/users/{id}/stats` | 统计 + `key_count` | `requireUserManager` |
| `GET /api/keys?tenant=&owner=` | 密钥列表（服务端过滤） | `requireUserManager`（租户管理员强制本租户） |
| `POST /api/keys` | 签发 | `requireUserManager`（租户管理员强制本租户） |
| `GET /api/keys/{id}/reveal` | 读明文（写审计） | `requireUserManager` + 租户范围断言 |
| `PATCH /api/keys/{id}/enable` `/disable` | 启停 | 同上（revoked ⇒ 409） |
| `DELETE /api/keys/{id}` | 吊销 | 同上 |

## 使用

1. 登录管理后台（超管或租户管理员）
2. 进入 `/users`
3. 点击任意用户行 → 抽屉打开
4. 点顶部「**API 密钥**」标签页
5. 查看 / 签发 / 启停 / 吊销

## 验证

```bash
# 后端契约层
go test ./admin/ -run 'TestListKeysHasOwnerFilter|TestCreateKeyPersistsResolvedOwner|TestSetKeyEnabledRejectsRevoked' -v

# 后端真库层（一次性库；见审计文档 §六）
go test ./admin/ -run 'TestListKeysOwnerFilterRealDB|TestSetKeyEnabledRevokedReturnsConflictRealDB|TestCreateKeyOwnerDefaultsToAdminNotNullRealDB' -v

# 前端契约层
cd web && npx vitest run src/components/userDetailKeys.contract.test.ts
```

## 相关文件

- `web/src/components/UserDetailDrawer.vue` — 用户详情抽屉（两个标签页）
- `web/src/api/keys.ts` — 密钥 API 客户端（`getKeys(params?)` / `createKey` / `revealKey` / …）
- `web/src/locales/*/users.ts` — 8 语种文案（`users.detail.*`）
- `admin/keys.go` — 密钥端点（`listKeys` / `createKey` / `setKeyEnabled` / `revealKey`）
- `admin/keys_user_scope_test.go` — 本功能的判据
