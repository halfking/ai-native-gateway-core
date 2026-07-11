# 钉钉机器人模块（dingtalk_bot）对接指南

> 模块位置：数据运维 → 模块管理 → integration → 钉钉机器人
> 依赖模块：会话压缩（compression）、提示词注入检测（prompt_injection）、会话缓存（cache）、会话审计与审批（session_audit）

钉钉机器人模块对接钉钉自定义机器人，复用网关既有的 `domains/notification` 通知渠道与
`session_audit` 审批流程，实现远程运维通知、风险告警推送、审批操作执行等能力。
模块开关与所有配置项均在管理后台维护，**无需重启服务即可生效（HotReload）**。

---

## 1. 两种对接模式

| 模式 | 配置项 | 适用场景 | 签名 |
| --- | --- | --- | --- |
| 群机器人 Webhook | `webhook_url` + `sign_secret` | 推送到指定群，最简单 | 加签 HMAC-SHA256 |
| 工作通知（应用消息） | `app_key` + `app_secret` + `agent_id` | 按 userid 精准下发，不受群限流 | access_token |

两种模式可同时存在；渠道初始化时优先使用 `dingtalk_bot.*` 设置，回退到环境变量
（`DINGTALK_WEBHOOK_URL` / `DINGTALK_SIGN_SECRET` / `DINGTALK_APP_KEY` / `DINGTALK_APP_SECRET`
/ `DINGTALK_AGENT_ID`）以兼容旧部署。

---

## 2. 配置项说明

### 2.1 连接
- `dingtalk_bot.webhook_url`：群机器人 Webhook（含 `access_token` 参数）。
- `dingtalk_bot.sign_secret`：群机器人「加签」密钥，推送时对 `timestamp+"\n"+secret`
  做 HMAC-SHA256 并 Base64 编码作为签名。敏感，不进日志。
- `dingtalk_bot.app_key` / `app_secret` / `agent_id`：企业内部应用凭证，用于工作通知。
- `dingtalk_bot.base_url`：默认 `https://oapi.dingtalk.com`，私有化部署可改内网网关。

### 2.2 实时告警推送
- `notify_on_alert`：注入攻击 / 越狱 / 数据泄露等安全异常告警。
- `notify_on_latency` + `latency_threshold_ms`（默认 5000ms）：高延迟告警。
- `notify_on_error_rate` + `error_rate_threshold`（默认 0.1）：错误率飙升告警。

### 2.3 审批通知与钉钉内操作
- `notify_on_approval`：高风险会话审批请求推送到钉钉。
- `callback_url`：审批回调公网地址
  `https://<your-domain>/api/webhooks/dingtalk/approval-callback`，需在钉钉机器人
  「接收消息 / 事件订阅」中配置。
- `verify_signature`：启用审批回调并强制做 HMAC-SHA256 验签（timestamp + secret）。关闭会停用审批回调，不会允许无签名请求。

### 2.4 系统状态查询
- `enable_status_query`：允许白名单用户在钉钉中执行 `/status`、`/health` 等指令查询网关状态。

### 2.5 安全与体验
- `allowed_users`：手机号（atMobiles）或 UserID（atUserIds）白名单，留空=全部。
- `card_type`：审批/告警卡片样式（`actionCard` 推荐 / `markdown` / `text`）。
- `at_all`：告警默认 @ 所有人（审批建议关闭避免打扰）。
- `rate_limit_per_min`：群机器人官方限制 20 条/分钟，默认 18 预留余量；工作通知不受此限。

---

## 3. 启用步骤

1. 在钉钉群设置中添加「自定义机器人」，安全设置选择「加签」，复制 Webhook。
2. 将 Webhook URL 填入 `webhook_url`，加签 Secret 填入 `sign_secret`。
3. （可选）开发者后台创建企业内部应用，填入 AppKey/AppSecret/AgentID 启用工作通知。
4. 将审批回调地址配置到机器人接收消息。
5. 在「模块管理」中开启「钉钉机器人」开关（前置模块须已启用）。
6. 在钉钉中验证收到测试告警 / 审批卡片。

---

## 4. 架构与复用关系（避免重复造轮子）

```
管理后台 (admin/modules.go)
   └─ dingtalk_bot.* 设置 (settings/spec_modules.go)
        │ 读取
        ▼
cmd/gateway/main.go: dingTalkConfigFromSettings()
        │ 构造
        ▼
domains/notification/dingtalk.go  ── DingTalkChannel（Webhook+加签 / 工作通知，复用既有实现）
        │ 注入
        ▼
domains/notification/approval_notifier.go ── ApprovalNotifier（多渠道路由，复用 session_audit）
        ▲
        │ 审批回调
api/dingtalk_callback.go ── RegisterDingTalkRoutes（签名验签 + agree/refuse → Approve/Reject）
```

- **通知渠道**：直接复用 `domains/notification.DingTalkChannel`，未新建发送逻辑。
- **审批流程**：复用 `session_audit.ApprovalManager`，回调仅做验签 + 转交。
- **路由**：复用 `ApprovalNotifier` 的租户/风险级路由表（来自 DB）。
- **状态查询**：复用 `domains/remotecontrol` 的指令解析（`/status`、`/health` 等）。

---

## 5. 已知事项（审计结论）

1. **回调处理器存在两层实现**：`api/dingtalk_callback.go`（独立 HTTP  Handler，已接线）
   与 `domains/notification/callback_handler.go`（统一 `NotificationChannel` 入口）。两者职责不同
   （前者处理审批 agree/refuse，后者为渠道归一化回调），暂不合并以避免破坏已上线路径；
   后续可统一到 `notification.CallbackHandler` 以降低维护成本。
2. **部分体验类设置尚未在发送链路消费**：`card_type` / `at_all` / `rate_limit_per_min` 当前
   由 `DingTalkChannel` 以推荐默认值（actionCard、按接收人 @）实现，配置项已预留并在管理后台
   可见，完整消费将在后续迭代接入（不阻塞核心审批/告警能力）。
3. **模块开关已真正生效**：`dingTalkConfigFromSettings()` 使 `dingtalk_bot.enabled` 与配置
   实际驱动渠道初始化与回调验签，区别于早期仅声明式的 feishu_bot / wechat_bot 模块。

## 6. 审计修复记录（2026-07-09）

| 问题 | 严重性 | 修复 |
|------|--------|------|
| 回调 JSON 解析错误时完整 body 写入日志 | 中 | 改为只记录 `body_size`，避免敏感数据泄露 |
| 回调无请求体大小限制 | 低 | 添加 `http.MaxBytesReader(1MB)` |
| `initApprovalNotifier` 仅回退 AppKey 环境变量，未回退 Webhook | 中 | 新增 `DINGTALK_WEBHOOK_URL` 环境变量回退分支 |
| `modules_test.go` required 列表缺 `session_analytics` | 低 | 补充到断言列表 |
| 部分 locale 文件未完全翻译 | 中 | 项目范围现有问题，非 dingtalk 特有，标注为已知 |
| `Dependencies` 中 compression/prompt_injection 偏宽泛 | 低 | 依赖设计基于团队一致，暂不改动

## 7. 审计加固（2026-07-11）

- 回调签名改用常量时间比较，重放窗口从 1 小时收紧到 10 分钟。
- 回调处理器按请求读取当前模块状态、签名密钥和白名单；热更新停用模块、轮换密钥或修改白名单后立即生效。
- `allowed_users` 现在实际限制审批操作；空名单保持兼容语义，允许所有已通过签名校验的用户。
- App 模式仅接受同时具备 `app_key`、`app_secret` 与 `agent_id` 的完整配置；不完整配置会记录不含秘密的诊断日志并拒绝初始化。
- `verify_signature=false` 会停用审批回调，系统不会提供无签名回调降级路径。
