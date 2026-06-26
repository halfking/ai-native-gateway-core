# 凭据 fp_slot_limit 与并发约束 - 变更日志

**日期**: 2026-06-26
**范围**: admin 后端 + admin Web UI
**关联**: 数据库迁移 `db/migrations/039_fp_slot_auto_default.sql`

## 问题

新建凭据时报错：

```
ERROR: new row for relation "credentials" violates check constraint
"credentials_fp_slot_vs_concurrency" (SQLSTATE 23514)
```

只传 `{"api_key":"sk-...","label":"gpt"}` 时也会触发。

## 根因

`admin/provider_credential.go addCredential` 把 `concurrency_limit` 和 `fp_slot_limit`
都硬编码成非 NULL 默认值（10 / 20），绕过了 DB 触发器 `auto_set_fp_slot_limit`
（该触发器只在 `fp_slot_limit IS NULL` 时按 `max(1, concurrency / 4)` 计算）。
硬编码后 `20 <= 10` 不成立，撞 DB CHECK 约束。

`updateCredential` 同样有相关缺陷：
1. 同一个 `if req.FpSlotLimit != nil` 块被复制了两遍，第二次 UPDATE 会覆盖第一次，
   审计日志和部分校验被绕过
2. PATCH 只修改 `concurrency_limit` 时不做校验，新的并发小于已有 `fp_slot_limit`
   会直接撞 `SQLSTATE 23514`

## 修复

### 1. 后端 addCredential — 让触发器接管

`admin/provider_credential.go`

- `fpSlotLimit` 从 `int` 改成 `*int`
- 请求没传 `fp_slot_limit` 时传 `NULL`，由 `auto_set_fp_slot_limit` 触发器按
  `GREATEST(1, concurrency_limit / 4)` 计算
- 请求显式传 `fp_slot_limit` 时透传，由 DB CHECK 约束兜底

效果：传 `{"api_key":"sk-...","label":"gpt"}` → 新行 `(concurrency=10, fp_slot=2)`，约束通过。

### 2. 后端 updateCredential — 加联合状态预校验

- 提取两个纯函数（无 DB 依赖，便于单测）：
  - `effectiveInt(incoming *int, current sql.NullInt32) *int` — 计算"更新后"的取值
  - `validateFpSlotVsConcurrency(concurrency, fpSlot *int) error` — 镜像 DB CHECK 约束
- 在 `updateCredential` 开头，凡是 PATCH 涉及 `concurrency_limit` 或 `fp_slot_limit`
  就先 SELECT 当前行，调 `validateFpSlotVsConcurrency` 校验，违反则直接返回 400
- 移除重复的 `if req.FpSlotLimit != nil` 块（line 386-389）

### 2a. 审计日志 — 替换坏掉的 `settings_history` + 新增拒绝日志

- 发现并修复了一个长期潜伏的 bug：`updateCredential` 的成功路径原来
  `INSERT INTO settings_history`，但生产 schema 里只有 `settings_audit`
  （migration 023），这个 INSERT 一直在静默失败。改为调用 `settings.WriteAudit`
  写入 `settings_audit`
- 拒绝路径新增一条审计：被约束预校验拦下的 PATCH 会写一条
  `action='rejected_constraint'`、`setting_key='credential:<id>:rejected_constraint'`、
  `new_value` 含 attempted/current 两套值的 JSONB 行
- 提取辅助函数（单测覆盖）：
  - `actorFromRequest(r)` — 取 `X-Admin-User` 或回退到 `"admin"`
  - `jsonOrNull(NullInt32)` — `Valid` 走 JSON 数字，否则 `null`
  - `rejectedTransitionJSON(...)` — 构建 attempted/current JSON
  - `nullInt32ToPtr(...)` — 把 `sql.NullInt32` 拍平成 `*int`，避开默认的
    `{"Int32":N,"Valid":B}` 序列化
- 复用了 `admin/auth.go:clientIPFromRequest`，不再重复实现
- 之前的 `settings_history` 表从未存在过——本次修复后，审计才真正落到 `settings_audit`

### 2b. 结构化 400 响应 — `error.code` + `error.context`

`updateCredential` 的拒绝响应从纯文本升级到与 envelope shape 一致的扩展：

```json
{
  "error": {
    "detail": "fp_slot_limit (25) cannot exceed concurrency_limit (10) after this update",
    "code": "fp_slot_exceeds_concurrency",
    "context": {
      "attempted_concurrency": 10,
      "attempted_fp_slot": 25,
      "current_concurrency": 100,
      "current_fp_slot": 25
    }
  }
}
```

- `detail` 字段保持原样，旧的 `req()` 错误提取器无需改动
- `code` 用于机器识别（目前唯一值 `fp_slot_exceeds_concurrency`）
- `context` 提供 attempted/current 两套值，方便后续做"建议恢复"按钮
- 后端新增 `writeConstraintError(...)` 助手 + 2 个单测
- 前端：
  - `api/_core.ts` 新增 `ApiError` 类，扩展 `req()` 把结构化字段填进去，
    `.message` 仍是原文所以向后兼容
  - `CredsTab.vue` 新增 `formatCredentialError` 把 `code='fp_slot_exceeds_concurrency'`
    转成与客户端预检一致的"指纹槽 (X) 不能超过并发上限 (Y)"中文文案
  - 新增 `saveMsgKind` / `addCredErrKind` 标签用于 UI 红色样式判断，替代
    之前的 `saveMsg.includes('fp_slot_limit')` 字符串匹配

### 2c. 一键恢复 — "恢复到建议值" 按钮

结构化 400 真正变成产品力的下一步：拿到 `current_*` 后直接给按钮填回去。

- `formatCredentialError` 现在还返回 `context`（attempted/current 两套值）
- 两个面板各自捕获 ctx 到 `saveMsgRejectCtx` / `addCredRejectCtx`
- 新增 `recoverFromRejection(side)`：把表单恢复到服务端 `current_*`，
  优先用 server 的真实值，回退到本地的 `autoFpSlot(concurrency)`
- 添加弹窗和编辑抽屉的拒绝横幅都加上了 "恢复到建议值" 按钮
  - 点击后：清空 ctx、清空错误、刷新提示、重新启用 watcher 让用户继续编辑
  - 编辑面板还把 `selectedFpSlotTouched` 翻回 false，让后续改动 concurrency
    仍能再次触发自动同步

### 3. 前端 — 添加 + 编辑双侧自动建议

`web/src/views/provider-detail/CredsTab.vue` + `web/src/api/providers.ts`

- API：`addCredential` 入参加上 `concurrency_limit` / `fp_slot_limit` 可选字段
- 添加弹窗新增两栏数值输入：
  - 「并发上限（0=不限）」+「指纹槽」
  - 默认 `concurrency=10, fp_slot=2`
  - 改并发时自动重算 fp_slot（`max(1, floor(concurrency/4))`）
  - 用户手动改过 fp_slot 后停止自动同步
  - 提交前客户端预检，避免打到后端才发现
- 编辑侧栏同款体验：
  - 同样实时显示建议值
  - 改并发时自动跟随（除非用户已手动改）
  - 「恢复建议值」按钮一键撤销覆盖
  - 保存前客户端预检
- 提取 `autoFpSlot(concurrency)` 工具函数，两端共用同一套算法

## 测试

`admin/provider_credential_test.go`

- `TestValidateFpSlotVsConcurrency` — 7 个子用例覆盖 NULL/相等/超出/等于边界
- `TestEffectiveInt` — 4 个子用例覆盖 incoming wins / fallback / both absent
- `TestActorFromRequest` — 2 个子用例覆盖 X-Admin-User header 与默认值
- `TestJsonOrNull` — 2 个子用例覆盖 JSON 数字与 null
- `TestRejectedTransitionJSON` — 验证 attempted/current 的扁平 JSON 形状
- `TestRejectedTransitionJSON_NullCurrentValues` — 验证 NULL current 走 JSON null
- `TestWriteConstraintError_EnvelopeShape` — 验证 400 envelope 包含 code/context/detail
- `TestWriteConstraintError_NullCurrentValues` — 验证 attempted-only 时 current 走 null
- `TestAddCredentialBody_DefaultsToTriggerForFpSlot` — 回归用例，确保解析阶段
  `fp_slot_limit` 留 nil
- `TestUpdateCredentialBody_ParsesFpSlotLimit` — 已有用例仍通过

## 验证

- `go build ./...` ✅
- `go vet ./admin/...` ✅
- `go test ./admin/ -count=1` ✅（admin 全套通过）
- `npx vite build` ✅
- `npx vitest run` ✅（10/10 通过）
- TypeScript 检查无新增错误（过滤到本次改动文件）

## 部署注意

1. 重新部署 gateway 即可，无需再次跑 SQL 迁移（039 已应用）
2. UI 改动需要重新构建 web bundle
3. 现有凭据不受影响——约束对存量数据是后向兼容的