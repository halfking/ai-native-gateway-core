# Change Log — 模块管理 (Module Management)

> **日期**: 2026-07-01
> **版本**: r1.14
> **部署序号**: 761
> **提交**: 5a094e32

## 概述

新增企业级**模块管理**功能，将系统 15 个功能模块统一纳入可视化管控。每个模块支持一键启用/禁用，并提供能力说明、配置项编辑、集成对接引导。包含飞书机器人（Feishu Bot）集成规范与完整配置体系。

## 新增功能

### 1. 模块管理后端 (`admin/modules.go`)
- `GET /api/admin/modules` — 全量模块列表（含启用状态）
- `GET /api/admin/modules/{key}` — 单模块详情 + 配置值
- `PUT /api/admin/modules/{key}/toggle` — 切换模块启用/禁用（体写入 settings_kv）
- 权限门禁：Dangerous/Breaking 级别限制 super_admin

### 2. 模块管理设置项 (`settings/spec_modules.go`)
- 新增 `CategoryModules` 类别，注册 12 个模块级开关设置
- 飞书机器人集成 6 项配置（webhook、verify_token、告警/审批推送、白名单）

### 3. 模块配置聚合 (`settings/specs.go`)
- `PlatformSpecs()` 纳入 `ModuleSpecs()`，随系统初始化自动注册

### 4. 前端模块管理页面 (`web/src/views/ModulesView.vue`)
- 左侧：按类别（请求压缩/会话管理/安全防护/流量控制/通用能力/集成对接）分组卡片列表，每张卡片含图标、名称、描述、开关
- 右侧：三标签详情（概览/配置/集成）
  - **概览**: 模块描述、能力清单、元信息（标识/危险级别/配置项数/状态）
  - **配置**: 相关设置项内联编辑（bool 开关 / 数字 / 文本 / 枚举选择）
  - **集成**: 飞书等外部集成的对接指导与状态展示
- 顶部汇总：已启用/总数统计

### 5. 前端 API (`web/src/api/modules.ts`)
- `listModules()`, `getModule()`, `toggleModule()` 三个接口封装

### 6. 导航与路由
- 路由 `/admin/modules`（super_admin 权限）
- 侧边栏「数据运维」组新增「模块管理」入口

## 管理的 15 个功能模块

| 模块 | 类别 | 开关设置键 |
|------|------|-----------|
| 会话压缩 🗜️ | 请求压缩 | `compression.enabled` |
| 会话缓存 💾 | 会话管理 | `cache.enabled` |
| 会话交接 🔄 | 会话管理 | `handoff.enabled` |
| 任务模式 🎯 | 会话管理 | `goal.enabled` |
| 审计日志 📝 | 通用能力 | `audit.enabled` |
| 提示词注入检测 🛡️ | 安全防护 | `prompt_injection.enabled` |
| 输出合规检测 🔒 | 安全防护 | `output_compliance.enabled` |
| 会话审计与审批 📋 | 安全防护 | `session_audit.enabled` |
| 会话健康检查 🔍 | 会话管理 | `session_inspector.enabled` |
| 安全检测引擎 🔐 | 安全防护 | `security.enabled` |
| 限流控制 🚦 | 流量控制 | `rate_limit.enabled` |
| 格式转换 🔀 | 通用能力 | `format_conversion.enabled` |
| UA/TLS 伪装 🎭 | 安全防护 | `enable_disguise` |
| 飞书机器人 📱 | 集成对接 | `feishu_bot.enabled` + 5 项配置 |
| Memora 记忆 🧠 | 通用能力 | 运行时状态（无开关） |

## 飞书机器人配置项

| 设置键 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `feishu_bot.enabled` | bool | false | 总开关 |
| `feishu_bot.webhook_url` | url | "" | Webhook 地址 |
| `feishu_bot.verify_token` | string | "" | 签名验证令牌 |
| `feishu_bot.notify_on_alert` | bool | true | 异常告警推送 |
| `feishu_bot.notify_on_approval` | bool | true | 审批通知推送 |
| `feishu_bot.allowed_users` | string | "" | 用户 OpenID 白名单 |

## 变更文件

| 文件 | 操作 | 说明 |
|------|------|------|
| `admin/modules.go` | 新增 | 模块管理 API handler（503 行） |
| `settings/spec_modules.go` | 新增 | 模块管理 Spec 定义（156 行） |
| `web/src/api/modules.ts` | 新增 | 前端模块 API 客户端（47 行） |
| `web/src/views/ModulesView.vue` | 新增 | 企业级模块管理页面 |
| `admin/handler.go` | 修改 | 注册模块管理路由 |
| `settings/specs.go` | 修改 | PlatformSpecs() 纳入 ModuleSpecs() |
| `web/src/router.ts` | 修改 | 新增 /admin/modules 路由 |
| `web/src/config/appNav.ts` | 修改 | 侧边栏添加「模块管理」入口 |

## 验证

- `go vet ./...` — 通过
- `go build ./...` — 通过
- `vue-tsc --noEmit` — 通过
- `vite build` — 通过（346 modules, 2.28s）
