# 中心侧增强 v2 · 2026-07-14

> 中心侧负责版本、License、节点、命令、采集、预警和分析。所有动作必须可审计、可撤销、可灰度。

## 1. 能力地图

| 能力 | 当前 | v2 交付 |
|------|------|---------|
| 多版本安装包 | Release store 有基础能力 | 上传、校验、发布、撤回、归档 |
| 版本信息 | version.json + build_seq | manifest 自动生成，平台/架构完整 |
| 下载请求 | 缺失 | 下载票据、统计、限速、防盗链 |
| 激活信息 | License CRUD + devices | 客户、设备、激活历史、撤销链 |
| 节点情况 | 心跳 + online/degraded/offline | 节点详情、版本、容量、License、事件时间线 |
| 预警 | 部分 stub | 规则、抑制、升级、Webhook、值班确认 |
| 远程协助 | 命令 API 不完整 | 受审批的诊断命令，不开放任意 shell |
| 分析 | 基础统计 | 漏斗、版本覆盖、升级成功率、故障关联 |

## 2. Release 管理

### 2.1 Release 生命周期

```text
DRAFT -> UPLOADING -> VERIFIED -> PUBLISHED -> GRAYING -> FULL
  |         |            |             |           |
  +-------> FAILED <-----+             +---------> ROLLED_BACK
```

每个 Release 必须具有：

```json
{
  "version": "v2.1.0",
  "build_seq": 1102,
  "channel": "stable",
  "edition": "customer",
  "platforms": ["linux-amd64", "linux-arm64", "darwin-arm64"],
  "artifacts": [{"name": "offline.tar.gz", "sha256": "...", "size": 123}],
  "manifest_sha256": "...",
  "signature_url": "...",
  "min_upgrade_from": "v1.13.0",
  "mandatory": false,
  "release_notes": "...",
  "status": "verified"
}
```

### 2.2 上传流程

1. 管理员创建 DRAFT，系统分配 `release_id` 和上传票据。
2. 分片上传到对象存储临时前缀，禁止直接写生产 CDN 路径。
3. 服务端计算 SHA256、校验签名、解包检查目录和 MANIFEST。
4. 运行离线包 smoke：installer doctor、镜像导入、健康检查、回滚。
5. 校验通过后生成不可变 manifest，状态变为 VERIFIED。
6. 发布时只允许 VERIFIED 版本进入 PUBLISHED，自动生成 CDN 路径和最新版本索引。

禁止覆盖同一 `version + platform + edition` 的已验证 artifact。修复包必须增加 build_seq。

## 3. 下载请求采集

下载不直接暴露对象存储密钥，改为短期下载票据：

```text
GET /api/downloads/:release/:artifact/ticket
  -> signed URL (TTL 10 min)
  -> CDN download
  -> CDN/webhook or edge log
  -> center download_events
```

采集字段只保留运营需要的聚合信息：`release_id`、平台、架构、edition、channel、时间、结果、耗时和匿名 request_id。默认不保存完整 IP；如合规要求保留网络来源，必须按租户策略截断或哈希。

## 4. 激活与节点中心

节点详情页必须同时展示四条时间线：

- License：签发、激活、刷新、吊销、设备变更。
- Heartbeat：最近心跳、状态变化、版本和资源摘要。
- Upgrade：命令、确认、下载、切换、验证、回滚。
- Alert：触发、通知、确认、关闭和关联工单。

节点状态采用明确的状态机：

```text
REGISTERED -> ONLINE -> DEGRADED -> OFFLINE
     |          ^          |           |
     +----------+----------+-----------+
```

状态判定必须使用服务端时间，避免客户端时间回拨影响在线状态：

- `ONLINE`: 最近心跳 <= 90s。
- `DEGRADED`: 90s < 最近心跳 <= 10min，或连续上报错误达到阈值。
- `OFFLINE`: > 10min。

## 5. 预警系统

### 5.1 规则模型

规则至少支持：

| 规则 | 默认阈值 | 严重级别 |
|------|---------|---------|
| 心跳超时 | 10min | critical |
| License 即将到期 | 30/7/1 天 | warning |
| 磁盘使用率 | 85/95% | warning/critical |
| 内存使用率 | 90% 持续 5min | warning |
| 错误率 | 5min > 10% | warning |
| 升级失败 | 任一 mandatory release | critical |
| 版本落后 | stable build_seq 落后 N | warning |

### 5.2 预警生命周期

```text
TRIGGERED -> NOTIFIED -> ACKED -> RESOLVED
     |          |          |
     +-------- SUPPRESSED (维护窗口 / 去重)
```

每条告警必须有 dedupe key、cooldown、最大通知次数、责任人和升级策略。通知渠道采用 Webhook、邮件和站内消息，远程命令不能由告警规则直接触发，必须经过命令审批策略。

## 6. 远程协助

中心不提供任意 shell。采用白名单诊断命令：

```text
collect_health
collect_logs {since, limit}
collect_system_info
check_database
check_disk
restart_gateway
start_upgrade {release_id}
rollback_upgrade {release_id?}
```

命令必须绑定：`operator_id`、`instance_id`、`request_id`、参数 schema、过期时间和审批记录。客户侧默认只允许只读诊断；重启、升级、回滚属于危险命令，需客户确认或双人审批。

## 7. 中心 API 增量

```text
POST /api/admin/releases/:id/upload-ticket
POST /api/admin/releases/:id/verify
POST /api/admin/releases/:id/publish
POST /api/admin/releases/:id/gray
GET  /api/admin/releases/:id/artifacts
GET  /api/admin/downloads/stats

GET  /api/admin/center/instances/:id/timeline
GET  /api/admin/center/instances/:id/metrics
GET  /api/admin/alerts
POST /api/admin/alerts/:id/ack
POST /api/admin/alerts/:id/resolve
POST /api/admin/center/instances/:id/commands
GET  /api/admin/center/commands/:id
```

## 8. 中心侧验收

- 上传两个平台的同一版本，服务端自动生成正确 manifest。
- 发布后最新版本索引、下载票据和下载统计同步可查。
- 激活节点能在 90s 内出现在 ONLINE 列表。
- 断开节点 10min 后产生一条去重告警。
- 远程诊断只能执行白名单命令，危险命令无审批不能执行。
- 一个 Release 灰度失败可停止扩散并保留已成功节点的回滚信息。
