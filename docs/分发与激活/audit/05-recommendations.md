# 审计建议与决策记录 · 2026-07-14

## 1. 审计结论

当前系统不是“没有能力”，而是能力分布在三条互不闭合的路径：

1. `installer` CLI 已经能完成安装、激活、心跳、升级和回滚。
2. `licensing` 已经能完成签名、指纹、时钟和 License 状态管理。
3. `cmd/license-authority` 已经能完成注册、心跳、版本查询和升级结果回写。

真正阻塞产品交付的是连接层和用户入口：采集接收端、主动命令通道、告警闭环以及激活/升级 UI 尚未完成。License 和客户升级 API 已存在，因此后续不应重复建设 handler，而应优先把已有能力串成一条可操作业务链。

## 2. 事实纠偏

以下旧文档表述需要在后续实现时统一：

| 旧表述 | 代码事实 | 处理 |
|---------|---------|------|
| “主控端主动推送升级” | `updates/latest` 是客户端查询 | v2 改为 push + queue 补偿 |
| “Docker compose recreate 0 秒” | 单实例实测仍有 stop/start 窗口 | 只对双实例 + LB 宣称无感 |
| “三副本 K8s 已可滚动升级” | 当前入库 manifest 是 `replicas: 1` | 发布前将副本数、preStop、PDB 纳入门禁 |
| “运行状态采集已实现” | 当前主要是 heartbeat/request telemetry | collector + runtime ingest 仍是 P0 |
| “用户可浏览器激活” | CustomerAPI 已有，ActivationWizard UI 和试用契约未完成 | UI + trial/error contract 完成前不能宣称闭环 |
| “双版本 master/customer 物理隔离” | 当前公开入口仍是 `cmd/gateway` | 以 License Authority 与 customer enforcement 为事实边界 |

## 3. 推荐决策

### 决策 A：先做用户入口和连接层，再做高级防盗版

优先级：`API contract + ActivationWizard` → collector/ingest → command queue/push → UpgradePanel → antitamper/antidebug。

理由：连接层直接影响激活转化、监控和升级安全；高级防盗版没有稳定发布和升级链支撑时，无法运维和回滚。

### 决策 B：主动升级采用双通道

WebSocket/SSE 负责低延迟通知，HTTPS pending queue 负责离线补偿。任何单一长连接都不能作为升级可靠性唯一保障。

### 决策 C：远程协助只提供结构化白名单命令

禁止实现“中心执行任意 shell”。远程诊断和升级命令应有 schema、TTL、幂等、签名、审批和审计。

### 决策 D：遥测默认关闭，撤销必须即时生效

采集协议只允许白名单字段；删除请求、保留期和数据导出必须成为产品能力，而不是文档承诺。

## 4. 首个代码切片的范围

首个代码切片为 `DIST-002`：浏览器侧 License API 契约和 ActivationWizard 接入。已复用现有 `licensing.CustomerAPI`、`Activator` 和 `OfflineManager`，并补齐通过 `LICENSE_AUTHORITY_URL` 代理的 trial 请求；未新增重复激活 handler。

验收边界：

- `GET /api/system/license/status` 能返回真实本地状态。
- 未激活节点能完成 trial/key 请求的参数校验，并返回稳定错误码。
- 离线 request/import 的文件格式与 installer CLI 完全一致。
- handler 不复制 License 签名、指纹和设备限制逻辑。

## 5. 发布门禁

在 P0 代码正式开发前，必须完成：

- API OpenAPI 契约。
- `deploy/sql/` migration 和 down migration。
- 客户/中心鉴权边界确认。
- 命令和 License 错误码表。
- 最小 e2e 测试场景。

## 6. 风险

- 文档规模已经较大，后续只允许按逻辑点修改，不再继续堆叠总文档。
- 当前 branch/remote 曾出现并发合并，后续实现前必须重新确认 branch、HEAD 和 worktree，避免基于旧 commit 编码。
- 远程部署、真实 License Authority 和生产 CDN 不应在本地凭默认凭据验证。
