# LLMGo 全站 UI 优化审计

> **站点**：https://llmgo.kxpms.cn
> **账号**：admin（super_admin + 默认租户）
> **日期**：2026-07-14
> **方法**：browser-use 实测 39 菜单页 + i18n:check 静态交叉验证

## 审计结论摘要

共审计 **39 个侧边栏菜单项** + **6 个二级页抽样**。整体深色主题统一，Layout（侧栏 + 顶栏 + 主内容）一致性好。主要问题集中在 **i18n 缺失/硬编码** 和 **部分运维/会话页 API 错误**。

### 全局 Top 10 问题

| # | 问题 | 影响页 | 优先级 |
|---|------|--------|--------|
| 1 | `promptInjectionFull.*` 310 keys 缺失，页面显示 raw key | 提示词注入检测 | **P0** |
| 2 | `SessionManagementView` 全页硬编码中文 + HTTP 503 | 会话管理 | **P0** |
| 3 | `SessionListView` 硬编码 + 「session manager not wired」 | 会话列表 | **P0** |
| 4 | ops 页 `*.undefined` status key 泄漏 | 自动更新/VibeCoding | **P0** |
| 5 | `app.current_tenant` key 泄漏 | 会话审计 | **P0** |
| 6 | 审计日志操作类型显示 `authentication.login` | 审计日志 | **P1** |
| 7 | Dashboard 切换语言后部分文案仍中文 | 总览 | **P1** |
| 8 | ops 概览非 en 语言标题仍英文 | 运维平台 | **P1** |
| 9 | 版本号 `vv2.4.3`、延迟 `4.8Kms` 显示异常 | 全局顶栏/总览 | **P2** |
| 10 | 页面标题 emoji 与侧栏图标重复 | 租户/用户/License | **P2** |

## 优先级统计

| 优先级 | 页面数 | 说明 |
|--------|--------|------|
| P0 | 6 | 功能不可用或 key 泄漏 |
| P1 | 12 | i18n 部分缺失、错误态、布局折行 |
| P2 | 21 | 美观/体验微调 |

## 修复 Backlog（建议顺序）

1. **Sprint 1**：promptInjectionFull locale + SessionManagement/List i18n
2. **Sprint 2**：ops 6 页 common.* + undefined fallback + 503 会话 API
3. **Sprint 3**：Dashboard locale 切换 + 审计日志 action 映射
4. **Sprint 4**：PageHeader 统一、emoji 清理、细节打磨

## 页面索引

### 总览

- [01-总览.md](01-总览.md) — `/`

### 模型与路由

- [01-模型与目录.md](02-模型与路由/01-模型与目录.md) — `/models`
- [02-路由全景.md](02-模型与路由/02-路由全景.md) — `/routing-v2`
- [03-凭据监控.md](02-模型与路由/03-凭据监控.md) — `/routing-v2/credentials`
- [04-探测健康度.md](02-模型与路由/04-探测健康度.md) — `/probe-health`
- [05-供应商.md](02-模型与路由/05-供应商.md) — `/providers`
- [06-成本价格.md](02-模型与路由/06-成本价格.md) — `/pricing`
- [07-定价管理.md](02-模型与路由/07-定价管理.md) — `/model-pricing`
- [08-免费资源.md](02-模型与路由/08-免费资源.md) — `/free-pool`

### 租户用户

- [01-租户管理.md](03-租户用户/01-租户管理.md) — `/tenants`
- [02-用户管理.md](03-租户用户/02-用户管理.md) — `/users`
- [03-API密钥.md](03-租户用户/03-API密钥.md) — `/keys`
- [04-密钥申请.md](03-租户用户/04-密钥申请.md) — `/key-applications`
- [05-审计日志.md](03-租户用户/05-审计日志.md) — `/audit-logs`

### 请求与会话

- [01-请求日志.md](04-请求与会话/01-请求日志.md) — `/request-logs`
- [02-会话列表.md](04-请求与会话/02-会话列表.md) — `/sessions`
- [03-会话管理.md](04-请求与会话/03-会话管理.md) — `/admin/sessions`
- [04-会话对比.md](04-请求与会话/04-会话对比.md) — `/session-compare`
- [05-会话上下文.md](04-请求与会话/05-会话上下文.md) — `/session-context`
- [06-会话分析中心.md](04-请求与会话/06-会话分析中心.md) — `/admin/session-analytics`
- [07-会话聚类.md](04-请求与会话/07-会话聚类.md) — `/admin/session-clusters`
- [08-会话审计.md](04-请求与会话/08-会话审计.md) — `/admin/session-audit`

### 数据运维

- [01-系统设置.md](05-数据运维/01-系统设置.md) — `/admin/settings`
- [02-会话配置.md](05-数据运维/02-会话配置.md) — `/admin/session-config`
- [03-数据生命周期.md](05-数据运维/03-数据生命周期.md) — `/admin/data-lifecycle`
- [04-格式异常监控.md](05-数据运维/04-格式异常监控.md) — `/format-anomalies`
- [05-模块管理.md](05-数据运维/05-模块管理.md) — `/admin/modules`
- [06-提示词注入检测.md](05-数据运维/06-提示词注入检测.md) — `/admin/prompt-injection`
- [07-压缩管理.md](05-数据运维/07-压缩管理.md) — `/admin/compression`
- [08-微信机器人.md](05-数据运维/08-微信机器人.md) — `/admin/modules?module=wechat_bot`
- [09-Agent-Registry.md](05-数据运维/09-Agent-Registry.md) — `/admin/agents`

### 运维平台

- [01-运维概览.md](06-运维平台/01-运维概览.md) — `/ops/overview`
- [02-License管理.md](06-运维平台/02-License管理.md) — `/ops/licenses`
- [03-故障管理.md](06-运维平台/03-故障管理.md) — `/ops/faults`
- [04-自动更新.md](06-运维平台/04-自动更新.md) — `/ops/autoupdate`
- [05-中心运维.md](06-运维平台/05-中心运维.md) — `/ops/center`
- [06-VibeCoding.md](06-运维平台/06-VibeCoding.md) — `/ops/vibecoding`

### 其他

- [07-接入示例.md](07-接入示例.md) — `/examples`
- [08-对话.md](08-对话.md) — `/chat`
- [09-二级页面抽样.md](09-二级页面抽样.md) — 详情页钻取

### 专项

- [00-审计方法论.md](00-审计方法论.md)
- [99-i18n-汇总.md](99-i18n-汇总.md)

## 菜单可见性确认

admin @ 默认租户实测侧边栏 **39 链接**（含总览），与 [`web/src/config/appNav.ts`](../web/src/config/appNav.ts) 一致。`tenantOnly` 分组「我的服务」不可见（符合预期）。

## 相关文档

- 既有审计：[docs/ui-audit/2026-07-07-session-dashboard-ui-audit.md](../ui-audit/2026-07-07-session-dashboard-ui-audit.md)
- i18n 进度：[docs/i18n/I18N_MIGRATION_PROGRESS.md](../i18n/I18N_MIGRATION_PROGRESS.md)

---

*本目录仅含审计文档，不含代码修改。实施修复请按 P0→P1→P2 backlog 另开任务。*
