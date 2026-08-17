# v2 实施计划与执行门禁 · 2026-07-14

## 1. 实施顺序

### Phase 0：契约冻结（W1-2）

交付：

- `docs/api/license-authority.openapi.yaml`
- `docs/api/customer-system.openapi.yaml`
- `docs/api/command-envelope.schema.json`
- `deploy/sql/` 中 runtime metrics、commands、alerts、download events migration
- API 错误码和幂等规则

门禁：OpenAPI、数据库 migration、错误码经过 code review；禁止在契约冻结前并行写 handler。

### Phase 1：客户主链（W3-5）

交付：

- 现有 `/api/system/license/*` 契约冻结、trial Authority 代理、错误码补齐和回归测试（不重复新增 handler）。
- `/setup` ActivationWizard：trial、key、offline request/import。
- `/api/system/info` 和 LicenseInfoView。
- customer 端手工 e2e：install → activate → restart → status。

### Phase 2：升级推送和采集（W6-8）

交付：

- command envelope、pending queue、WebSocket/SSE receiver。
- `/api/v1/commands/*` 与中心 dispatch API。
- collector、metrics、reporter 和 `/api/v1/collect/runtime`。
- UpgradePanel、UpgradeBanner、中心 UpgradeProgressView。

### Phase 3：中心运维与安全（W9-10）

交付：

- Release upload/verify/publish/gray UI。
- alerts rule engine、通知、ACK/resolve。
- 远程协助白名单命令。
- enhanced fingerprint、antitamper、nonce。

### Phase 4：发布验收（W11-12）

交付：

- 5 种部署模式 e2e。
- 在线/离线激活 e2e。
- 10 节点灰度升级、失败暂停、回滚。
- 下载、激活、节点、告警、命令和升级分析报表。
- 用户文档、运维 runbook、安全报告、版本发布包。

## 2. 建议的首批 tickets

| Ticket | 交付 | 依赖 |
|--------|------|------|
| DIST-001 | API 契约 + command envelope schema | 无 |
| DIST-002 | CustomerAPI contract + ActivationWizard integration tests | DIST-001 |
| DIST-003 | `ActivationWizard.vue` + browser e2e | DIST-002 |
| DIST-004 | runtime metrics migration + store | DIST-001 |
| DIST-005 | collector + reporter | DIST-004 |
| DIST-006 | authority collect handler | DIST-004 |
| DIST-007 | command queue migration + receiver | DIST-001 |
| DIST-008 | dispatch API + admin UI | DIST-007 |
| DIST-009 | upgrader command adapter | DIST-007 |
| DIST-010 | UpgradePanel + push e2e | DIST-008/009 |
| DIST-011 | release upload/verify/publish UI | DIST-001 |
| DIST-012 | alerts and remote assistance | DIST-004/007 |

## 3. 数据库增量

所有业务 SQL 必须进入 `deploy/sql/` 版本化目录，并提供 down migration。

建议表：

```text
runtime_metrics
download_events
command_queue
command_events
alert_rules
alert_events
release_artifacts
release_rollouts
license_audit_events
```

关键约束：

- `command_queue(instance_id, command_id)` 唯一。
- `runtime_metrics(instance_id, observed_at)` 索引和按月归档。
- `release_artifacts(release_id, platform, arch, edition)` 唯一。
- 审计表只追加，不允许业务 update/delete。

## 4. 发布和回滚策略

每个功能切片独立提交，推荐顺序：

```text
1. docs/api + migration
2. store + handler tests
3. client integration
4. UI
5. e2e + security review
6. changelog + release note
```

失败回滚：

- 文档/schema：执行 down migration 或停止新读取路径。
- API：保留旧端点，关闭 feature flag。
- 客户端：回滚镜像/二进制到 verified release。
- 中心推送：pause rollout，撤销未执行 command，保留已执行节点的回滚批次。

## 5. 验证矩阵

| 层 | 命令/证据 | 门禁 |
|----|-----------|------|
| Go | `go test ./... -count=1` | 通过 |
| Shell | `tests/deploy_*.sh` | 0 failed |
| API | OpenAPI contract tests | 状态码/错误码一致 |
| DB | migration up/down + empty DB | 可重复、可回滚 |
| Security | signature/replay/fingerprint tests | 拒绝非法请求 |
| Docker | install/restart/upgrade/rollback | healthz 200 |
| K8s | rollout/undo/readiness | maxUnavailable 0 |
| Browser | Playwright activation/upgrade | desktop/mobile |

## 6. 本地第一执行批次

当前应先执行 `DIST-001` 和 `DIST-002`，而不是直接实现全部三端：

1. 冻结 API、错误码、鉴权、幂等和 migration。
2. 收敛现有 `licensing.CustomerAPI` 契约，直接复用 `Activator`、`OfflineManager` 和 `DeviceManager`。
3. 增加集成测试，覆盖 trial/key/status/offline 错误路径。
4. 然后再做 ActivationWizard，避免 UI 对未冻结契约编码。

## 7. Done 定义

一个切片只有同时满足以下条件才能标记完成：

- 代码、测试、文档和 CHANGELOG 同一提交。
- 无明文凭据和未审计远程命令。
- migration 有 down 文件。
- 失败路径有明确错误码、日志和回滚行为。
- Docker/K8s 至少一个真实部署环境验证。
- 用户可见 UI 变化完成浏览器实测并留截图。
