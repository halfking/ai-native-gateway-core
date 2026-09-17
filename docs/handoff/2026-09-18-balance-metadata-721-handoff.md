# Handoff — 供应商凭据余额元数据/立即刷新/手工保护（迁移 721）

**日期**: 2026-09-18
**提交**: `507d78cff`（已推送 origin/main，基线 c66dbd6c9）
**状态**: 已完成并合入主干

---

## 一、任务与结论

**原始需求**（2026-09-17）：研究所有供应商凭据能否抓到用量余额（按次/按量/5h/周限/总限额）；/providers 列表按用量级别排序；列表页集成凭据维护。

**结论（经审计修正后）**：
1. **余额/配额探测实际覆盖 6 家**（非早期口头声称的 8+ 家）：
   - 货币余额 API（providercap）：openai、deepseek、siliconflow（后台 floor guard + quotafetcher 请求路径）；openrouter（仅 quotafetcher 专用 fetcher，/api/v1/key + /credits）。
   - 订阅套餐窗口（balance_floor_guard）：zhipu、minimax（5h/7d 窗口；zhipu 另有绝对 token 余量）。
   - Anthropic/Gemini/Moonshot/阿里云百炼/腾讯混元等**无公开余额 API，不在支持范围**。
2. **限额形态映射**：总限额=balance_usd（API 探测）；5h/周窗=plan_quota_windows（zhipu unit=3/6）；月限无厂商公开 API（未支持）；按次计费无余额概念（无支持必要）。
3. **排序**：/providers 列表默认 `qualitySortKey='usage'`（按 24h 请求数降序，本次由 default 改为 usage）。凭据维护本就集成在列表页 Manage Credentials 抽屉（非本次新增）。
4. **本次新增三件套**：⟳ 立即刷新余额、余额元数据展示（来源/时间/货币/错误）、手工输入 24h 保护。

## 二、改动文件与关键行为

| 文件 | 行为 |
|---|---|
| `sql/migrations/startup/721_credential_balance_source_and_error.sql` | ADD COLUMN balance_source(text, CHECK NULL\|manual\|api) + balance_error(text)；幂等 |
| `installer/.../embeddata/startup/721_*.sql` | installer 三处同步惯例 |
| `admin/provider_credential.go` | listCredentials SELECT+Scan 暴露 4 元数据列；updateCredential 在 PATCH balance_usd 时写 source='manual'+checked_at=now()+error=NULL |
| `admin/providers.go` | 注册 `POST .../credentials/{cid}/refresh-balance` 子路由（providerConsole 鉴权链） |
| `admin/provider_credential_balance.go`（新） | refresh-balance handler：BalanceURL 空→400（配置事实不入 error 列）；探测成功写 source='api'/error=NULL；失败 fail-open 写截断 500 rune 的 balance_error，HTTP 仍 200+success=false |
| `bg/balance_floor_guard.go` | Pass A 候选 SELECT 加 `NOT (source='manual' AND checked_at>now()-24h)`；refreshBalance 成功写 source='api' |
| `bg/credential_probe_v2.go` | cycleAll 余额 UPDATE 加同一保护 WHERE + source='api'（注释互指保持两处同步） |
| `web/src/api/providers.ts` | ProviderCredential 加 4 可选字段；refreshCredentialBalance() + RefreshBalanceResponse |
| `web/src/views/ProvidersView.vue` | 凭据抽屉 usage 列：⟳ 按钮、来源标记（🛰 API/✏️ 手工）、fmtTimeAgo 时间、balance_error 红字；默认排序 usage |
| `web/src/locales/{zh-CN,en-US}/providers.ts` | 5 个新 key（其余语言 fallback en） |
| `docs/balance-query-optimization.md` | 顶部审计修正记录 + §2.2 修订 + §3 实施状态逐项核对 |
| `CHANGELOG.md` | Unreleased Added 条目（含已知限制） |

## 三、验证（实测，非声明）

- `go build ./...` EXIT 0（含合并 mDNS net 修复 b484efbac 与 errorsx c66dbd6c9 后复验）
- `go vet ./admin/... ./bg/...` 干净
- `go test ./admin/ -run "UpdateCredential|ListCredentials|Balance|Rotate|Reveal|HandleProviderCredentials"` → ok
- `go test ./bg/ -run "Balance|FloorGuard"` → ok；`go test ./internal/providercap/` → ok；`go test ./db/` → ok
- `TestTruncateBalanceError`（新增，3 子用例含 700 CJK rune 截断）→ PASS
- `vue-tsc --noEmit` EXIT 0 零输出；`vite build` 成功（10.23s）
- **迁移实库验证**：本地 PG（127.0.0.1/llm_gateway）事务内 `\i 721_*.sql` → 两列+CHECK 约束创建成功 → ROLLBACK（共享库零污染）

## 四、遗留风险（如实）

1. **refresh-balance 无 DB 依赖端到端单测**（admin 包无 sqlmock 基建）；handler 逻辑仅靠编译+类型检查覆盖，上 245/154 后建议先对单个 openai/deepseek 凭据点一次 ⟳ 验证。
2. **manual 保护窗两处 WHERE 需人工同步**（floor guard Pass A 与 probe_v2 UPDATE，注释互指）；若未来出现第三个余额写入方必须复制同一谓词，否则保护窗被绕过。
3. **floor guard 探测失败路径不写 balance_error**（刻意保留 #12a 退避语义，未纠缠）；只有显式 ⟳ 失败才可见错误。
4. **CredsTab.vue（详情页抽屉）未接同款 UI**；API 已就绪，前端复用即可。
5. **154 authoritative 分支**：本次改的是余额写入/管理面，非探测接线；旧 binary 跑新 schema 兼容（新列可 NULL），新 binary 跑旧 schema 会在 721 未执行时因列缺失报错——**部署顺序必须是先迁移后 binary**（与既有 startup 迁移约定一致）。
6. **并行会话撞号**：工作区另有未提交的 `720_routing_analytics_*`（他人文件，本次未动）；远程 main 已有 `720_rls_policy_vocabulary_unification`，该会话提交时需自行改号 722+。
7. `openrouter` 的余额在 floor guard 无数据源（其 floor 语义由 plan 窗口路径不覆盖），配置 balance_floor_usd 对 openrouter 凭据**不会触发自动刷新**（Pass A BalanceURL 为空直接 return false）——既有行为，本次未改。

## 五、下一轮提示词（建议）

> R41 轮次：基于 507d78cff（迁移 721 余额元数据三件套）。候选任务按优先级：
> 1) 给 refresh-balance 建 admin 包 DB 集成测试基建（sqlmock 或 TEST_DATABASE_URL 模式，参照 provider_access_test.go），覆盖 400 不支持厂商/200 success=false 写 error/200 success 写 source='api' 三分支；
> 2) CredsTab.vue 接入同款 ⟳ 刷新与元数据展示（复用 refreshCredentialBalance）；
> 3) 将 manual 保护谓词抽取为共享 SQL 片段常量，加守卫测试锁定两处同步；
> 4) 部署 245/154（先 startup 迁移后 binary，deploy-local.sh 唯一通道），实机点 ⟳ 验证 openai/deepseek 凭据；
> 5) 处理工作区遗留的 720_routing_analytics 撞号（改 722+）。
> 纪律：迁移建号三重查重；诚实报告测试覆盖面，禁止声明式打勾。
