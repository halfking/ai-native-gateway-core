# 代理订阅管理特性需求与审计总结（2026-09-03）

## 1. 文档目的与范围

本文固定当前代理订阅管理能力的产品需求、前后端契约、运行入口边界和数据库迁移结论，供开发、测试与部署验收使用。

范围包括：

- 订阅 CRUD 与刷新；
- 节点库存、健康检查与可拨号判定；
- 代理汇总状态；
- 前后端列表响应信封与空列表行为；
- 订阅 URL 和节点凭据脱敏；
- canonical startup schema 迁移；
- 管理 UI、生产 `gateway` 与 `gateway-v2` 演示入口的运行边界。

本文不把旧审计报告中的历史风险重新标记为新缺陷，也不修改版本文件或前端构建产物。

## 2. 当前特性需求

### 2.1 订阅管理

管理端必须提供以下超级管理员 API：

| 能力 | 方法与路径 | 关键要求 |
| --- | --- | --- |
| 订阅列表 | `GET /api/proxy/subscriptions` | 返回列表信封，不泄露完整订阅 URL |
| 创建订阅 | `POST /api/proxy/subscriptions` | 校验名称、HTTP(S) URL 和状态 |
| 查询订阅 | `GET /api/proxy/subscriptions/{id}` | 不存在时返回 404 |
| 更新订阅 | `PUT /api/proxy/subscriptions/{id}` | 支持名称、URL、状态、优先级、备注更新 |
| 删除订阅 | `DELETE /api/proxy/subscriptions/{id}` | 级联删除节点并失效对应缓存/连接 |
| 刷新订阅 | `POST /api/proxy/subscriptions/{id}/refresh` | 拉取、解析并原子替换节点，返回节点与可拨号统计 |

刷新结果至少应包含 `ok`、`subscription_id`、`node_count`、`by_protocol` 和 `dialable_count`。当已导入节点但没有可拨号节点时，应返回可操作的 warning，而不是把“有库存节点”等同于“代理可用”。

### 2.2 节点管理、健康与可拨号

管理端必须提供：

| 能力 | 方法与路径 | 关键要求 |
| --- | --- | --- |
| 节点列表 | `GET /api/proxy/nodes` | 可按 `subscription_id`、`dialable` 过滤；返回统计信封 |
| 创建节点 | `POST /api/proxy/nodes` | 手工入口仅接受可直接拨号的 `http`、`https`、`socks5` |
| 查询节点 | `GET /api/proxy/nodes/{id}` | 返回脱敏视图 |
| 删除节点 | `DELETE /api/proxy/nodes/{id}` | 失效订阅连接与节点缓存 |
| 节点健康检查 | `POST /api/proxy/nodes/{id}/health-check` | 返回可拨号性、状态、耗时和健康结果 |

`dialable` 的含义固定为“Go 网关可直接作为 HTTP Transport 代理拨号”：

- `http`、`https`、`socks5` 且主机和端口有效：可拨号；
- `trojan`、`vless`、`vmess`、`ss`：仅作为导入库存，不可由 Go HTTP 客户端直接拨号；
- 不可拨号协议必须先通过本地 mihomo/xray 暴露为 HTTP 或 SOCKS5 入口，再登记该入口节点。

健康检查不能把“协议不可拨号”伪装成普通网络超时；应显式返回 `dialable: false` 和可操作原因。节点状态需覆盖正常、禁用、不健康等运行态，并保留最近健康结果、响应时间、成功率和连续失败次数。

### 2.3 汇总状态

`GET /api/proxy/status` 应返回至少以下字段：

- `subscription_count`、`active_subscriptions`；
- `node_count`、`healthy_count`、`unhealthy_count`；
- `by_protocol`、`dialable_count`；
- `selected_node`，无可选节点时为 `null`；
- 选择失败原因和“全部不可拨号”提示（适用时）。

状态页必须同时展示库存规模和可拨号规模，不能只展示总节点数。

## 3. 前后端列表契约

### 3.1 后端标准信封

订阅列表和节点列表的标准响应是：

```json
{
  "items": [],
  "total": 0
}
```

节点列表还可携带 `node_count`、`by_protocol`、`dialable_count` 和 `warning` 等汇总字段。

### 3.2 空数组要求

- 无数据时 `items` 必须是 `[]`，不能是 `null`；
- 后端通过非 nil 空 slice 保证 JSON 输出为空数组；
- 前端客户端从 `{items,total}` 信封中解包数组；
- 前端为兼容旧环境可暂时接受裸数组；
- 成功响应中的 `null`、缺失 `items` 或非数组 `items` 应降级为 `[]`，不得因数组迭代或读取 `.id` 导致页面崩溃；
- 非 JSON 响应、HTTP 错误和网络错误必须上抛并展示，不能伪装成空列表。

## 4. 敏感信息与审计要求

### 4.1 订阅 URL

数据库保存完整 `subscribe_url` 供刷新使用，但所有订阅查询和列表响应必须脱敏：

- 移除 URL userinfo；
- 将原始 path 替换为固定脱敏路径；
- 移除 query；
- 移除 fragment；
- 无法解析的历史值返回固定脱敏占位，不回显原文。

例如数据库中的 `https://user:pass@example.test/path-token/sub?token=secret#x`，管理 API 只返回 `https://example.test/redacted`。

### 4.2 节点凭据与错误

- 节点响应不得返回密码明文或数据库密文，只返回 `has_password`；
- 代理 URL 写日志前必须移除 userinfo；
- 刷新、健康检查、缓存刷新等错误在返回或记录前必须执行 secret sanitization；
- 解密失败状态不能被误当成有效明文凭据；
- 管理接口继续受超级管理员鉴权保护。

## 5. 运行入口与静态资源边界

### 5.1 不能用端口号推断入口类型

`cmd/gateway-v2` 的示例默认监听地址是 `:8782`，但 `LLM_GATEWAY_LISTEN` 可将任一二进制映射到任一端口。因此不能仅凭 `8781` 或 `8782` 判断进程是生产 `gateway` 还是 `gateway-v2`，验收必须同时检查实际二进制、路由和静态目录配置。

本次对 `localhost:8782` 的实际审计结果是：

- 运行的是生产 `gateway` 二进制，不是 `gateway-v2`；
- `/api/proxy/subscriptions` 已注册，未认证请求返回 401；
- 镜像包含 `/opt/llm-gateway-go/web/index.html` 与 `web/assets/*`；
- 未显式设置 `LLM_GATEWAY_STATIC_DIR` 时，旧默认值只查找 `web/dist`，静态处理器未启用，导致 `/admin/proxy` 返回 404。

因此此次入口修复不是给 `gateway-v2` 增加管理页面，而是让生产 `gateway` 在未显式配置静态目录时兼容两种既有打包布局：优先 `web/dist/index.html`，其次 `web/index.html`。显式 `LLM_GATEWAY_STATIC_DIR` 仍具有最高优先级。

### 5.2 正确的管理 UI 验收方式

生产管理 UI 应使用以下任一方式：

1. 启动 `cmd/gateway`，使用其实际监听端口访问 `/admin/proxy`；端口以 `LLM_GATEWAY_LISTEN` 或配置文件为准；
2. 前端开发时启动 `web` 目录下独立的 Vite dev server，并将 API 代理指向运行 `cmd/gateway` 的管理 API 地址。

验收时必须同时确认：当前进程是 `cmd/gateway`、`cfg.StaticDir` 指向含 `index.html` 的目录、前端路由 `/admin/proxy` 与后端 `/api/proxy/*` 指向同一环境。

## 6. Schema 迁移结论

### 6.1 历史问题

代理 schema 原先仅存在于 `db/migrations/364_proxy_management.sql`，不在当前 canonical startup 序列中。新环境只执行 `sql/migrations/startup` 时，可能缺少代理表和 `providers.proxy_subscription_id`，导致管理 API 在运行时失败。

### 6.2 Canonical 修复

新增 startup migration `646_proxy_management_canonical.sql`，紧随远端最新的 645 之后，以 646 作为未占用的新编号。迁移 canonicalize 以下对象：

- `proxy_subscriptions`；
- `proxy_nodes` 及订阅级联外键；
- `provider_domains`；
- `providers.proxy_subscription_id` 及 `ON DELETE SET NULL` 外键；
- 查询和选择所需索引；
- `schema_migrations` 的 646 记录。

迁移满足以下兼容与交付原则：

- 保留 `db/migrations/364_proxy_management.sql`，不删除历史文件；
- `CREATE TABLE IF NOT EXISTS`、`ADD COLUMN IF NOT EXISTS`、条件外键和 `CREATE INDEX IF NOT EXISTS` 支持重复执行；
- 生产 `db.ApplyMigrations` 显式调用同等幂等的 `ensureProxyManagementCanonicalSchema`，部署新 Gateway 二进制时会自动协调 schema；
- 安装器嵌入并注册 646 的 canonical SQL，标准安装不会遗漏代理表；
- 离线升级包的受控 startup migration 清单包含 646；
- 部分安装中缺失订阅 URL 的记录会被禁用；无法归属订阅的孤儿节点会被删除；缺少有效拨号字段的节点保留为可扫描但不可路由的 disabled 记录；
- 重复域名在建立唯一约束前使用带长度保护的遗留后缀区分；
- 不覆盖有效的现有代理数据；
- 不在 canonical schema 迁移中写入可能随地区、网络和运营策略变化的供应商域名种子数据。

### 6.3 Rollback 策略

仓库在 `sql/migrations/startup` 使用同目录 `*.down.sql` 作为当前主流 rollback 约定，因此新增 `646_proxy_management_canonical.down.sql`。

由于 646 兼容并接管了历史 364 已创建的对象，数据库无法判断某个表、列或索引究竟由 364 还是 646 创建。自动 drop 可能删除既有生产 schema 和数据。因此 646 rollback 仅删除自身 migration ledger 记录，默认不破坏共享对象。

只有在运维人员确认代理 schema 完全由 646 新建、没有数据且没有运行依赖时，才可人工评估历史 `db/migrations/364_proxy_management.down.sql` 的破坏性清理步骤。

## 7. 当前审计结论与验收结果

代码与契约审计显示，当前实现已覆盖订阅 CRUD、刷新、节点管理、健康检查、可拨号标记、汇总状态、列表信封、空数组防御和敏感信息脱敏。本次变更补齐了前端并发/非法项防御、响应 URL 脱敏、canonical startup schema，以及生产 gateway 对两种既有前端打包布局的自动探测。

2026-09-03 已完成的自动化与隔离验证：

- [x] 在不含代理表、仅含最小 `providers`/`schema_migrations` 前置对象的临时 PostgreSQL 数据库执行 646，三张表、两条外键、8 个索引和 provider 两列均创建成功；
- [x] 在同一临时库再次执行 646，迁移幂等且 ledger 仅一条；
- [x] 先执行历史 364、插入既有订阅，再执行 646，既有有效数据保持不变且 canonical ledger 写入成功；
- [x] 在部分安装临时库中验证：空 URL 订阅被禁用、孤儿节点被删除、无效端口节点被禁用、有效节点保持 active、NULL 健康检查 URL 被回填且设为非空、未知节点状态归一为 disabled、重复域名安全区分、节点订阅关联最终为非空；
- [x] 构造同名但删除动作错误的历史外键，确认 646 会将节点外键修复为 `ON DELETE CASCADE`、provider 外键修复为 `ON DELETE SET NULL`；
- [x] 确认生产 `ApplyMigrations`、安装器嵌入资源和离线升级包受控清单均包含 646；canonical SQL 与安装器副本字节一致；
- [x] 执行 646 down，确认只删除 ledger，代理 schema 保留；
- [x] 后端测试确认空列表为 `{ "items": [], "total": 0 }`、订阅 URL 不返回 userinfo、原始 path、query 或 fragment，节点响应不返回密码值；
- [x] 前端测试确认列表信封/旧裸数组兼容、`null`/缺失 `items` 降级、非 JSON 响应报错、非法项过滤、独立 loading/error 和过滤后空状态；
- [x] 隔离启动生产 `cmd/gateway`，确认 `web/dist/index.html` 和扁平 `web/index.html` 两种布局下 `/admin/proxy` 均返回 200 且包含 Vue 根节点；
- [x] 同一隔离实例中 `/api/proxy/subscriptions` 未认证返回 JSON 401，没有被 SPA fallback 覆盖。

仍需部署环境或人工凭据参与的验收：

- [ ] 使用现有超级管理员账户完成登录后的订阅列表、创建、刷新和删除冒烟；
- [ ] 使用真实 HTTP/SOCKS5 网桥节点执行健康检查并核对状态、响应时间和可拨号提示；
- [ ] 若验收的是 `cmd/gateway-v2`，先确认二进制身份；其演示入口不应被生产管理 UI 验收覆盖。
