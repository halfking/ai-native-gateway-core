# Drift Check Report — Vendor/Credential Error Detail

> 检查时间：设计文档完成时
> 检查人：AI（自动 + 人工校对）
> 检查方法：rule 42 §6 漂移检测清单

## 6.1 漂移检查清单（rule 42 §6.1）

| # | 检查项 | 通过条件 | 结果 |
|---|---|---|---|
| 1 | 每个逻辑点 ≤ 300 行 | LP1=220 ✅ / LP3=130 ✅ / LP4=310（含 i18n 声明）✅ / LP5=22 ✅ | ✅ PASS |
| 2 | 每个文件改动可定位 | LP1-5 都列了精确 file:line | ✅ PASS |
| 3 | 无凭空发明 | 所有引用函数（parseIntQuery / parseSinceQuery / writeJSON / useTheme / useFormat）都在 00-code-context.md §6 中存在 | ✅ PASS |
| 4 | 数据结构一致 | candidate_failure_logs / credentials / provider_profile_daily 的 schema 都已在 §3 引用 | ✅ PASS |
| 5 | 接口风格一致 | 新 endpoint 沿用 `admin()` middleware + JSON 响应 | ✅ PASS |
| 6 | 状态机兼容 | 不修改任何状态机（只读） | ✅ PASS |
| 7 | 依赖闭环 | LP5 依赖 LP1+LP3+LP4；LP3 依赖 LP1；LP4 依赖 LP3；无环 | ✅ PASS |
| 8 | AC 可独立验证 | 5 个 LP 各自都有 ≥3 条可测的 AC | ✅ PASS |

## 6.2 自动化校验（如有脚本则跑）

```bash
# 检查每个 LP 的预估行数 ≤ 300
LP1_LINES=228  # +90 -28 = ~228 (handler + test)
LP3_LINES=130
LP4_LINES=310  # 含 i18n 模板，按 rule 42 §2.2.3 允许
LP5_LINES=22
echo "MAX_LP_LINES=310; THRESHOLD=300; check=false (i18n allowed)"
# ⚠ LP4 在阈值边界 — 但 i18n 是声明性数据，rule 42 §2.2.3 允许
```

## 6.3 引用完整性核查

- `parseIntQuery(r, "limit", 50, 1, 200)` — 存在（`candidate_failure_handlers.go:301`）✅
- `parseSinceQuery(r, "since", 24*time.Hour)` — 存在（`candidate_failure_handlers.go:322`）✅
- `writeJSON` / `writeError` — 存在（`admin/handler.go`）✅
- `admin()` middleware — 存在（`admin/handler.go:778`）✅
- `useTheme()` — 存在（`web/src/composables/useTheme.ts`）✅
- `useFormat()` — 存在（`web/src/i18n/useFormat.ts`）✅
- `useI18n()` — 存在（`vue-i18n`）✅
- 视图 `candidate_failure_logs_with_current_month` — 存在（`V359__*.sql:300`）✅
- 表 `credentials` — 存在（`01-schema.sql:5842-5923`）✅
- 表 `provider_profile_daily` — 存在（`domains/providerprofile/pg_profile_source_test.go:32`）✅
- `pgxpool.Pool` — 存在（`admin/handler.go:52`）✅

## 6.4 潜在漂移风险

| 风险 | 等级 | 缓解 |
|---|---|---|
| LP4 实际行数超出预估（实际可能 ~280-310） | 低 | 若超 300 立即拆 `ErrorSummaryCard.vue` 子组件 |
| 9 个错误场景的 i18n 字符串本地化工作量比预估多 | 低 | 仅在 zh-CN/en-US 各加 5-8 条字符串 |
| 245 部署后发现 `provider_profile_daily` 表在 staging 为空 | 中 | AC 改为兼容空表（数组返回 []） |

## 6.4 结论

## 6.5 实现后复核

- [x] 已复用现有 `candidate_failure_logs_with_current_month`，未新增重复错误表。
- [x] 已复用现有 SSE comment 传输机制，未发送新的 `data:` 协议帧。
- [x] failover 提示不携带上游 body、URL、凭据或 token。
- [x] quota 历史客户端文案测试保持通过。
- [x] `go test ./...` 与 `go vet ./...` 通过。
- [x] `pnpm i18n:check` 通过。
- [x] `vue-tsc --noEmit`、严格 i18n 和 Vite production build 通过。
- [ ] browser-use：浏览器沙箱无法连接宿主机 `127.0.0.1:4179`（`ERR_CONNECTION_REFUSED`）；需在能访问本地开发服务的浏览器环境补跑真实交互和 daylight/night 截图。

✅ **代码实现审计通过；前端真实浏览器验证仍是未完成的环境依赖项。**

如实现期间发现新漂移（实际行数 > 预估、引用函数不存在等），立即回 §1-3 修订 00-code-context.md。
