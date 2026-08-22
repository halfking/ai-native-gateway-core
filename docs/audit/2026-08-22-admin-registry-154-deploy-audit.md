# Admin 注册表视图 — 154 部署审计（2026-08-22）

## 部署

| 项 | 结果 |
|---|---|
| 目标 | **154 only**（llm.kxpms.cn → 47.97.111.154） |
| 245 | **未部署**（按老板要求） |
| build_seq | **1673** |
| git_sha | `da33273c` |
| 方式 | 并发 deploy-seamless（同 commit），healthz OK |

## API 冒烟（JWT admin）

| 端点 | 结果 | 备注 |
|---|---|---|
| `GET /api/admin/connection-registry` | ✅ 200 | live=0；**审计前** closed=null |
| `GET /api/admin/request-journeys/queues?view=total` | ✅ 200 | 100/100 requests，observation=complete |

## 审计发现与修正

| # | 严重度 | 问题 | 修正 |
|---|---|---|---|
| A1 | 中 | 空 closed 返回 JSON `null`，前端/契约不稳定 | `admin/connection_registry.go` 强制 `[]` |
| A2 | 低 | 6 个 locale 仍用旧版 connectionRegistry/requestRegistry 文案（三态节点 mock 语义） | 同步 en-US 结构到 de/es/fr/ja/zh-TW/ar |
| A3 | 信息 | 连接注册表仅进程内；154 当前无 live 流式连接时列表为空属正常 | 文档已说明 |
| A4 | 信息 | 节点恢复时间线仍为 mock（node-health API 未落地） | handoff 后续 |

## 验证（修正后待二次部署）

- [ ] `go test ./admin/... -run ConnectionRegistry`
- [ ] 154 redeploy build_seq+1
- [ ] `closed` 字段为 `[]` 非 null
- [ ] 浏览器实测 `/admin/connection-registry` 与 `/admin/request-registry`（需 super_admin 登录）

## 关联文档

- [`design/admin-registry-views.md`](../../design/admin-registry-views.md)
