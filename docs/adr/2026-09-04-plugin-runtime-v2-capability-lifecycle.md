# ADR: plugin-runtime v2 契约、capability 三入口鉴权、lifecycle admin API

- **Status**: Accepted (V5.1 platform gate)
- **Date**: 2026-09-04
- **Authors**: V5.1 platform line (ai-session-manager + llm-gateway-go)
- **Supersedes**: —
- **Related**:
  - Spec: `docs/架构优化v5/05-实施路线图.md §V5.1` 与 `docs/架构优化v5/02-插件框架与许可证设计.md §2/§3/§5/§7`
  - Discovery: `docs/架构优化v5/04-代码校对与优化方案.md §2 F-H1/F-H2/F-H3`
  - Implementation:
    - `plugin-runtime/manifest.go`, `plugin-runtime/types.go` — manifest v2 字段、SemVer 校验
    - `plugin-runtime/capability.go`, `plugin-runtime/capability_test.go` — capability 注册表与三入口
    - `plugin-runtime/supervisor.go` — `Upgrade` 自动回滚 + `Uninstall` 内存清理
    - `plugin-runtime/installer.go` — staging + 原子切 current（既有）
    - `cmd/gateway/plugin_apiproxy_init.go`, `plugin_canon_init.go`, `plugin_lifecycle_init.go` — 三入口接入 + admin API + 目录边界断言
    - `cmd/gateway/plugin_lifecycle_init_test.go` — 边界/状态码测试
    - `web/src/config/appNav.ts`, `AppTopbar.vue`, `usePluginNav` — 动态 nav 注入、删除 ASM 硬编码

## Context

V5.1 之前 `plugin-runtime` 已有进程级插件骨架：发现/Ed25519 manifest 签名/安装/升级/健康自愈/entitlement 门/HMAC 双向通信/iframe 静态页方案，但暴露三处 P0 安全与可用性缺口：

1. **F-H1：capabilities 纯声明、无鉴权**。`plugin-runtime/canon_api.go` 的 `VerifyPluginContext` 只验 HMAC/租户，不校验能力；任何验证通过的插件可越权访问全部 canonical 端点（body 代理路径的间接暴露面）。
2. **F-H2：生命周期 API 缺失**。仅有 `POST /api/v1/plugins/install`；无 uninstall/disable/enable 入口；`Supervisor.Upgrade` 失败仅回滚 manifest 索引、不拉起旧进程。
3. **F-H3：前端 iframe 通道未接线**。`PluginMount.vue` 已实现但 `web/src/router.ts` 未注册；ASM 入口在 `web/src/config/appNav.ts` 硬编码 `external: true` 整页跳转，导致新插件必须改宿主前端代码才能上入口。

manifest 契约也停留在 v1，没有 `config_schema/hooks/permissions/license` 等声明面，无法表达「需要 settings 写入、需要 body 代理、需要 policy.serve」等差异。

## Decision

### 1. gateway-plugin-v2 manifest

新增 `SchemaVersion`/`Permissions`/`Hooks`/`ConfigSchema`，常量 `SupportedAPIContractV2 = "gateway-plugin-v2"`。`validate()` 串行校验：

- `schema_version ∈ {0,1,2}`（缺省视为 0，向后兼容 v1 manifest 解析）
- `schema_version == 2 ⇒ strict SemVer + contract = v2`；`schema_version < 2 ⇒ contract ≠ v2`
- `permissions ⊆ capabilities`（防止声明矛盾）
- `hooks ∈ {on_install, on_activate, on_deactivate, on_uninstall}`（未声明不调用）
- `license.mode ∈ {open, entitlement, offline-cert}`，缺省 `open`（默认全开放）
- `config_schema` 深度 ≤3、键名规范、键数 ≤64（防恶意 schema）

`CompareSemVer` 替代既有字典序；本轮在 `Supervisor.Upgrade` 路径上预埋语义（manifest 索引替换不再依赖字符串比较），后续升级回滚决策接入。

### 2. Capability 三入口鉴权

引入 `pluginruntime.CapabilityRegistry`：

- `RegisterManifest` 快照每个插件声明的 `capabilities`
- `Required(method, path)` 默认路由表覆盖：
  - `GET /_gateway/plugin/v1/sessions → session.read`
  - `GET /_gateway/plugin/v1/analytics → analytics.read`
  - `GET /_gateway/plugin/v1/bodies → body.fetch`
  - `GET /_gateway/plugin/v1/interception-policies → policy.serve`
  - `POST /_gateway/plugin/v1/events → events.ingest`
- `Allows(pluginID, capability)` 返回 manifest 声明与否
- `CapabilityDenied` 写出 `403 {"code":"capability_denied","capability":...}`

三入口接入：

| 入口 | 路径前缀 | 接线点 |
|---|---|---|
| plugin→host canonical | `/_gateway/plugin/v1/*` | `plugin_canon_init.go` 用 `VerifyPluginContext + RequireRouteCapability` 包裹 sessions/analytics handler |
| browser→plugin API proxy | `/plugins/{id}/api/*` | `plugin_apiproxy_init.go` 走 `Required(method, path)` 自查 |
| 策略拉取 | `/_gateway/plugin/v1/interception-policies` | 走默认路由表 + capabilities 校验（同上） |

admin 入口（`POST /api/v1/plugins/{id}/activate|deactivate|uninstall`）不挂 capability 检查，由 `admin.AdminMiddleware` 单独鉴权，符合 admin/数据面分层。

### 3. 生命周期 admin API + 状态机 + 升级回滚

admin 三路由全部 `admin.AdminMiddleware` 包裹，写操作后续写审计（V5.2 接入 FR-1.6 生命周期审计）。handler 复用统一抽象：

- `pluginLifecycle` 接口（`Activate/Deactivate/Uninstall`）
- `noopPluginLifecycle` 在 `pluginsDir` 未配置时 503 `plugin.lifecycle_unavailable`
- `supervisorLifecycle` 适配真实 supervisor

错误码稳定集合：

| code | status | 触发 |
|---|---|---|
| `plugin.not_found` | 404 | supervisor 无该插件 manifest |
| `plugin.invalid_id` | 400 | plugin_id 不匹配正则或越界 |
| `plugin.lifecycle_unavailable` | 503 | supervisor 未挂载（pluginsDir 空） |
| `plugin.activate_failed` / `plugin.deactivate_failed` / `plugin.uninstall_failed` | 500 | supervisor 调用失败 |

`Supervisor.Upgrade` 由「仅回滚索引」改为「自动 Start(旧 manifest)」—— 失败后 best-effort 拉起旧版本，使插件直接回到 ready，无需调用方再调 `Restart`。两个 Start 都失败时返回联合错误（升级错 + 回滚错）。

`Supervisor.Uninstall(pluginID)` 停止进程并清空内存索引（procs/states/manifests）；不直接动盘。

`supervisorLifecycle.Uninstall` 在 supervisor Uninstall 之上额外：

1. Stop（best-effort，错误仅 WARN）
2. supervisor Uninstall 清内存
3. `resolvePluginBundle(pluginsDir, pluginID)` 解析盘上目录
4. 路径边界断言：resolved path 必须留在 `pluginsDir` 根下（防御 plugin_id 绕过正则导致 `../etc` 之类路径逃逸）
5. `os.RemoveAll(bundle)`，目录不存在视为幂等（已清理）返回 nil

`resolvePluginBundle` 解析失败/越界返回 `plugin.invalid_id`，与 admin 层的 plugin_id 正则形成两层防御。

### 4. UI 注入

- 删除 `web/src/config/appNav.ts:105-110` 的 ASM `external: true` 硬编码条目
- 新增 `mergeRemotePluginNav(groups, entries)`：已存在 nav_group 时 append 到对应组，未知 group 落入合成 `plugins` 组（永远追加在末尾，不挤占 curated 布局）
- `AppTopbar.vue` 通过 `usePluginNav` 拉 `/api/v1/plugin-nav`，在 opsPlatform merge 之前合并，保证 plugin entries 可进入匹配静态组（`requests-sessions` 等），但 plugin 不能挤占 maintain nav
- `RemoteNavEntry` 转 `NavItem` 时固定 `external: true` —— 插件 web 位于 `/plugins/<id>/<page_path>`，与 Gateway SPA router 隔离
- 8 份 i18n 同步新增 `nav.group.plugins`

## Options considered

| 选项 | 优势 | 劣势 | 选择 |
|---|---|---|---|
| **A. capability 在 plugin-runtime 自带，admin 入口不挂** | 边界清晰：admin 操作为治理面，capability 是数据面 | 与 §5.1-5.2 「三入口」 字面解读略有出入 | ✅ 采纳（admin 走 AdminMiddleware） |
| **B. capability 三入口全挂在同一中间件** | 一处定义 | admin 入口会被插件 capability 锁住，违反治理-数据面分层 | ❌ |
| **A. Upgrade 失败仅回滚 manifest 索引** | 实现简单 | 调用方必须再调 Restart 才能让旧进程恢复 ready；V5.1 G5.1「升级失败自动回滚旧版并恢复 ready」不可达 | ❌ |
| **B. Upgrade 失败自动 Start(旧 manifest)** | 满足 G5.1 直接就绪语义；调用方仍收到错误可记录 | 旧 Start 也失败时返回联合错误需测试 | ✅ 采纳 |
| **A. Uninstall 仅 Stop（保留盘上 bundle）** | 简单，运维可手动清理 | V5.1 G5.1「卸载后目录/DB/nav 无残留」不达标 | ❌ |
| **B. Uninstall 删 bundle + 目录边界断言** | 满足 G5.1；`resolvePluginBundle` 单点防御 | 需引入路径解析 + 容器化场景对 pluginsDir 卷挂载的兼容性测试 | ✅ 采纳 |
| **A. `web/src/router.ts` 注册 PluginMount + 删硬编码** | 与既有 router 体系一致 | PluginMount 只是 host 路由，plugin 路径全部走 `/plugins/{id}/...`；router 注册需要大量模式字符串拼接 | ❌ |
| **B. AppTopbar 用 usePluginNav 注入 + 删硬编码** | 复用既有 nav_group 机制；插件 layout 与 maintain/opsPlatform 一致 | 需新写 `mergeRemotePluginNav` 合并函数 | ✅ 采纳 |

## Consequences

- **安全**：capability 三入口鉴权消除最大暴露面；admin 入口独立鉴权，治理面仍归 super_admin。
- **运维**：升级失败自动回到旧版本 + Uninstall 真删 bundle；运维不再需要 SSH 上线清理插件目录。
- **兼容**：v1 manifest 在 v2 宿主上继续可用；`AllowedAPIContract = "gateway-plugin-v1"` 保留；缺省 `schema_version=0` 视为 v1（向下兼容未升级的 manifest）。
- **债务**：
  - `CompareSemVer` 已实现但仓库内暂无调用点（manifest 索引替换靠 plugin_id，升级决策待 V5.2 worker 引入）。
  - capability 默认路由表硬编码 5 条；新增 capability 必须在两仓同步更新 + 增补契约 fixture。
  - Uninstall 仅删 bundle，不撤 catalog 行；catalog 接入后需补独立测试。
  - 生命周期审计六事件（FR-1.6）未落地，仅 admin API 已就位；V5.2 worker 引入时一并接入。

## Rollback

| 维度 | 开关 | 默认 | 行为 |
|---|---|---|---|
| capability 鉴权 | `plugins.capability_enforce` | on | 关闭后回到 v1 行为（无能力检查） |
| Uninstall bundle 删除 | 编译时常量 | on | 移除 `os.RemoveAll` 调用回到仅 Stop（编译级开关，未抽 env） |
| Upgrade 回滚 | 编译时常量 | on | 删除 `Start(oldManifest)` 调用回到仅回滚 manifest 索引 |

## References

- `docs/架构优化v5/02-插件框架与许可证设计.md §2` manifest v2
- `docs/架构优化v5/02-插件框架与许可证设计.md §3` 生命周期状态机
- `docs/架构优化v5/02-插件框架与许可证设计.md §5` capability 鉴权
- `docs/架构优化v5/02-插件框架与许可证设计.md §7` UI 注入
- `docs/架构优化v5/04-代码校对与优化方案.md §2 F-H1/H2/H3`
- ai-session-manager 仓：`internal/plugin/manifest.go`、`internal/plugin/manifest_v2_test.go`、`internal/plugin/auth_matrix_test.go`、`internal/plugin/manifest_contract_test.go`、`scripts/verify-plugin-capability-matrix.sh`、`scripts/verify-settings-hot-reload.sh`、`docs/operations/plugin-platform-fixtures.md`