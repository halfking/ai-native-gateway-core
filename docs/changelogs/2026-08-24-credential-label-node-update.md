# 凭据名称化：实时流 node_update 下发 + 剩余视图推广

## 做了什么

承接 `cfedb41b8`（action 通道 credential_label 下发 + 节点矩阵卡片名称化），补齐实时流 delta 路径与剩余未覆盖视图，让"凭据 ID → 凭据名称"在网关全链路落地。

### 后端（实时流 delta 路径）

- `cmd/gateway/main_livestream.go`：`liveNodeStatusProvider` SQL 增加 `COALESCE(c.label, '')`，`SELECT` 第二列投影到 `LiveNodeStatus.CredentialLabel`，Scan 绑定（`LiveNodeStatus` 结构体第 159 行已含 `credential_label` 字段）。
- 效果：`node_update`（delta 帧）与 `initial_data`（首帧）现在都自带 `credential_label`，前端节点详情抽屉主路径不再依赖本地 credential 缓存兜底——即便 SSE 客户端缓存为空也能正确显示名称。

### 前端剩余视图（cfedb41b8 未覆盖）

| 视图 | 改动 |
|---|---|
| `DispatchWaterfallDetail.vue` | attempts 列表从 `cred {id}` 改为 `credentialDisplayName` |
| `EmergencyDiagnosticModal.vue` | 凭据上下文 / 强制恢复确认文案改用 label 优先 |
| `NodeHealthTimelineView.vue` | 页头裸 `credential_id` → 名称（悬停保留 `#id`） |
| `DecisionsView.vue` | 详情抽屉 `chosen_credential_id` + 候选轨迹 `c{id}` → 名称 |
| `CredentialMonitorView.vue` | 模型上下线确认框 `凭据 #id` → `selectedCred.label` |
| `NodeStatusMatrix` / `QueuePerspectivePanel` | 已在 `cfedb41b8` 落地；Queue 透视走 `credentialDisplayName(candidate, providerLabel(n), n.credential_id, n.credential_label)` |

### 类型修复（vue-tsc 存量错误清零）

- `RequestLogsView.vue`：`onDrawerOpenRequest` 调用不存在的 `openDetail` → 改为 `showDetail`（修复抽屉内"打开请求"运行时 `ReferenceError`）。
- `SessionSummaryDrawer.vue`：`resp.total`（后端无此字段）→ `resp.count`；`RequestLogRow` 补 `request_preview`/`response_preview` 类型声明。
- `liveStreamStore.ts`：`LiveRequest` 补 `stage_category` 字段声明；测试补 `LiveStreamEnvelope` 导入。
- `ProbeTriStateQueue.test.ts`：`resolveCreds` 类型改为无参包装，消除 `never` 收窄报错。

### i18n 清理

- 删除 8 语言包中已无组件引用的孤儿 key `requestJourneys.matrix.nodeLabel`，并同步移除 `NodeStatusMatrix.test.ts` 对应 mock。

## 安全与可观测性

- `credential_label` 来自 `credentials.label` 列，属租户内可见的凭据展示名，不含 secret / token。
- 字段 `omitempty`：健康节点 label 为空时不上报，老客户端忽略未知字段，向后兼容。
- 实时流仍走既有的 admin/JWT 鉴权（Bearer 或 `?token=`），无新增暴露面。

## 验证

- `go build ./...` / `go vet ./...`：通过。
- `pnpm exec vue-tsc --noEmit`：0 错误（pre-commit 恢复绿色）。
- `pnpm vitest run`：59/59 通过。
- `pnpm i18n:check`：通过（8 语言包孤儿 key 已清理）。
- 245 部署（`v2.4.7`, git_sha `78160af5`）产物已确认包含 `credential_label`（10 个 chunk）与 `credentialDisplayName`。
- 245 后端 `cmd/gateway/main_livestream.go:316` SQL 实际投影 `c.label` → `LiveNodeStatus.CredentialLabel`，`fanOutNodeUpdate` 经 SSE 下发（直接连接 SSE 流可观测 `node_update` 帧带 `credential_label`）。

## 关联提交

- `cfedb41b8` feat(credential-label): 推广至全部视图 + 缓存租户维度 + SSE 下发
- `b8b49c7fc` feat(credential-label): 补齐 node_update 下发、剩余视图名称化与类型修复
- `508050a45` chore(wiring): 移除未使用的 `wireNodeStatusProvider` 死代码
