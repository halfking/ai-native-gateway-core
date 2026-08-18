# 2026-08-18 — 品质统计支持按模型粒度过滤 + 抽屉"模型30天统计"块

> 在既有供应商级统计之上，为品质统计增加模型级过滤维度，
> 并在节点详情抽屉展示模型级 30 天统计。

## 1. 做了什么

1. **后端**：`GET /api/quality/providers/:id/stats` 增加可选 `?model=<raw_model_name>` 过滤，
   缺省行为不变（供应商级），`?model=` 时聚合口径对齐 ledger 写入语义
   `raw_model_name = COALESCE(outbound_model, client_model)`。
2. **前端**：`RequestLogDrawer` 打开请求详情时，按 `detail.outbound_model ?? detail.client_model`
   调用 `getProviderRequestStats(provider_id, model)`，渲染"模型30天统计 (name)"块（7 个数字：
   总 / 月 / 周 / 日 / 成功 / 失败 / Tokens）；加载失败静默隐藏，不影响抽屉其余部分。
3. 无 SQL/索引变更（复用 `usage_ledger_with_current_month` 视图既有聚合）。

## 2. 改动清单

| 文件 | 类型 | 说明 |
|---|---|---|
| `internal/handlers/quality_handler.go` | 修改 | `handleGetProviderRequestStats` 解析 `?model=` 并透传 `loadProviderRequestStats(ctx, providerID, rawModelName)` |
| `internal/handlers/quality_handler_test.go` | 修改 | 新增 Success / ModelFilter / NotFound 3 个测试 |
| `web/src/api/quality.ts` | 修改 | `getProviderRequestStats(providerId, model?)` |
| `web/src/components/RequestLogDrawer.vue` | 修改 | 新增模型级统计块（与既有供应商级统计块并列展示） |
| `CHANGELOG.md` + 本文档 | 修改 | rule 36 变更记录 |

## 3. 为什么这样做

- 复用同一聚合口径与窗口（30 天、`usage_ledger_with_current_month` 视图），避免重复建表/索引。
- `?model=` 为可选参数，缺省即供应商级，天然向后兼容，不影响既有调用方。
- 模型名对齐 ledger 写入语义（`outbound_model` 缺省回退 `client_model`），与链路记录一致。
- 抽屉统计块采用非致命静默（失败隐藏），不阻断详情主流程。

## 4. 验证结果

- **单元测试**：`go test ./...`（handlers 包 Success/ModelFilter/NotFound 等）全绿。
- **前端静态**：`npx vue-tsc --noEmit` 0 错误；`npm run build` 通过。
- **后端接口（245）**：
  - `?model=MiniMax-M2.7` → 579（30 天全对齐）；`?model=MiniMax-M3` → 88；
  - 不存在模型 → 0；不存在 provider → 404；
  - 缺省（供应商级）→ 675（向后兼容回归）。
- **前端浏览器实测**（browser-use 真实交互，245 dashboard stream）：
  - 发起真实 `minimax-m2.7` 流式请求 → 泳道出现 `request-bar--in-progress` → 真实点击打开抽屉；
  - 抽屉渲染 `模型30天统计 (MiniMax-M2.7): 总570/月570/周570/日570/成功560/失败10/Tokens 1,457,707`，与接口同时刻值一致；
  - 深/浅双主题均正常；截图 `ui-verify-drawer-light-*.png` / `ui-verify-drawer-dark-*.png` 存档。

## 5. 遗留与风险

- 本会话模型无法直接查看截图，双主题验证以 DOM 文本 + 截图存档为准。
- 工作区在 1612 部署时尚含方案 C 代码（git_sha 96e15c2b + 工作树），但本 commit (66ffab81d)
  已在 git 历史落地；若需让服务器 git_sha 显示新 commit，可在 245 跑 `bash scripts/deploy-245.sh`
  重新部署（会 bump 至 1613）。
- `version.json` 等部署产物在工作树中已 bump 至 1612；按惯例随 `chore(release)` 单独 commit。
- `web/public/menu-config.json` 的 `exported_at` 时间戳为构建副产物，常规不单独提交。

## 6. 下一步建议（已落地状态）

- ✅ 本轮方案 C 改动 + 本 changelog 已 commit (`66ffab81d`) 并推送至 `origin/main`。
- ✅ 他人 commit `74e8426f6`（NodeStatusMatrix 键盘 a11y）通过 merge commit `079bddfb7` 合并，未丢弃。
- ⏭ release chore 提交 `version.json` (1612) —— 由 deploy 脚本 owner 决定是否单独 commit。
- ⏭ 245 重部署以让 git_sha 对齐最新 commit —— 可选。