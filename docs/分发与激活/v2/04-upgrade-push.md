# 升级推送专题 v2 · 2026-07-14

> 目标：对已部署节点进行可审计、可灰度、可回滚的升级推送。
> 当前代码 `cmd/license-authority/update_handler.go` 是客户端查询 `/updates/latest`，不是主动推送；本方案补齐这一差距。

## 1. 目标与非目标

### 目标

- 中心发布 Release 后，按灰度规则通知目标节点。
- 在线节点 30 秒内收到命令，断线节点在 7 天 TTL 内重连补偿。
- 节点必须本地确认 manifest、签名、License 约束和兼容性后才能执行。
- 升级状态、结果、回滚原因可在中心和客户侧查询。

### 非目标

- 不提供任意远程 shell。
- 不由中心直接传输客户业务数据。
- 不跳过客户侧健康检查和回滚保护。

## 2. 通道设计：主动通知 + 拉取补偿

```text
                 online
Authority  ───────────────────►  Client command receiver
    │                                  │
    │ command queue (7d TTL)           │ local durable queue
    │                                  ▼
    └────── client reconnect/poll ◄── execute after verify
```

### 2.1 推荐通道

- 长连接：WebSocket over TLS，客户端 installer/agent 建立连接。
- 兼容通道：HTTPS long-poll/SSE；网络和代理不支持 WebSocket 时使用。
- 补偿通道：客户端心跳响应携带 `pending_commands` 摘要，客户端再拉取完整命令。

WebSocket 断开不能阻止业务流量，连接由独立 agent 或 installer heartbeat 进程维护。

## 3. 命令信封

```json
{
  "command_id": "cmd-uuid",
  "instance_id": "instance-uuid",
  "type": "upgrade",
  "issued_at": "2026-07-14T10:00:00Z",
  "expires_at": "2026-07-21T10:00:00Z",
  "requires_confirmation": true,
  "payload": {
    "release_id": "rel-uuid",
    "version": "v2.1.0",
    "channel": "stable",
    "mandatory": false,
    "maintenance_window": {"not_before": "...", "not_after": "..."}
  },
  "signature": "base64-ed25519"
}
```

签名内容必须是 canonical JSON。签名绑定 `command_id + instance_id + payload + expires_at`，防止命令跨节点重放。

## 4. 命令生命周期

```text
CREATED -> DISPATCHED -> RECEIVED -> CONFIRMED -> RUNNING
    |          |           |          |             |
    +-------> EXPIRED      +-------> REJECTED      +-> SUCCEEDED
                                                    +-> FAILED -> ROLLED_BACK
```

节点执行必须做到：

1. 校验命令签名和 instance_id。
2. 检查 command_id 幂等记录；已完成命令不重复执行。
3. 校验 Release manifest、SHA256、GPG/Ed25519 签名和最低升级版本。
4. 检查备份空间、数据库迁移兼容性、维护窗口。
5. 非 mandatory 命令等待客户确认；mandatory 命令仍需满足本地安全策略。
6. 运行现有 `installer/internal/upgrader`，持续回写状态。

## 5. 中心 API

```text
POST /api/admin/releases/:id/dispatch
  body: {"selector": {"channel":"stable", "tier":"standard", "version_lt":"v2.1.0"}}

POST /api/admin/center/instances/:id/commands
  body: {"type":"upgrade", "release_id":"...", "requires_confirmation":true}

GET  /api/admin/center/commands/:id
GET  /api/admin/center/commands/:id/events
POST /api/admin/center/commands/:id/cancel

GET  /api/v1/commands/pending
POST /api/v1/commands/:id/ack
POST /api/v1/commands/:id/result
GET  /api/v1/commands/stream
```

`/api/v1/*` 只允许 instance token；`/api/admin/*` 需要管理员 token 和审计记录。

## 6. 灰度发布

默认阶段：

```text
canary 1% -> batch_1 10% -> batch_2 30% -> batch_3 60% -> full 100%
```

扩散条件：

- 最近阶段升级成功率 >= 98%。
- 自动回滚率 < 2%。
- 健康检查失败率 < 1%。
- 无 critical 告警持续 10 分钟。

任一条件失败，自动暂停下一阶段；不会自动扩大爆炸半径。管理员可以手动 resume、pause 或 rollback。

节点选择支持 `instance_id`、License tier、channel、版本、平台、地区和客户标签，但默认禁止按原始 IP 做长期筛选。

## 7. 客户确认与远程协助边界

| 命令 | 默认 | 客户确认 | 中心审批 |
|------|------|---------|---------|
| check_update | 自动 | 否 | 否 |
| upgrade optional | 通知后确认 | 是 | 否 |
| upgrade mandatory | 本地策略决定 | 可选 | 是 |
| rollback | 客户确认 | 是 | 是 |
| restart_gateway | 客户确认 | 是 | 是 |
| collect_logs | 只读自动 | 可选 | 否 |

中心只发结构化命令，客户 agent 不执行任意命令字符串。日志采集需要大小、时间范围和脱敏策略上限。

## 8. 失败处理

- 下载失败：指数退避，保留旧版本。
- 签名失败：拒绝执行并产生 security alert。
- 启动失败：按现有 upgrader 自动回滚。
- 迁移失败：停止升级，保留数据库备份和 migration report。
- 结果回写失败：本地持久化事件，重连后补报。
- 中心不可达：不影响现有业务；只停止新的 push，不主动降级 License。

## 9. 升级推送验收

- 10 个测试节点中，1 个 canary 失败后其余 9 个不接收命令。
- 在线节点从 dispatch 到收到命令 <= 30s。
- 断线节点 7 天内重连后可补领命令；超过 TTL 自动过期。
- 相同 command_id 重复投递只执行一次。
- 错误签名、错误实例、过期命令全部拒绝。
- 升级失败自动回滚，中心看到失败原因和回滚版本。
- 客户可从设置页查看进度、暂停可选升级和触发回滚。
