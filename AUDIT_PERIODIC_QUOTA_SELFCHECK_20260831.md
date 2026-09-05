# 周期性配额凭据自检 — 任务审计报告（2026-08-31）

> 审计对象：hzx-2 / 智谱AI 周期性配额凭据"额度重置后自检未执行、状态未更新"任务的全部三轮修改。
> 审计方法：对照 `.handoff/selfcheck-audit-2026-08-26.md` 的用户原则与既有约定，逐文件复查 SQL 语义、
> 与 `bg/credential_recovery.go` 2026-08-08 P0 死循环守卫的交互、探测模型名称约定、前端取值一致性。

---

## 1. 特性需求整理

来源：用户三次提报 + `.handoff/selfcheck-audit-2026-08-26.md` §2.1 用户原则。

| 编号 | 需求 | 落点 |
|---|---|---|
| R1 | 周期性配额凭据（hzx-2）额度重置后，自检必须及时执行并更新凭据可用状态 | `bg/periodic_quota_probe.go` |
| R2 | 同样问题适用于智谱AI 等全部供应商；须确认同根因 | 同上（供应商无关） |
| R3 | 拉取凭据模型列表时，自动以"最新的可用模型"填充默认探测模型 | `modelcatalog.AutoFillDefaultProbeModel` + discovery / admin refresh 两个入口 |
| R4 | 定时扫描"没有默认探测模型"的凭据并自动补齐 | `bg/default_probe_scanner.go`（新增，默认 5 分钟） |
| R5 | 凭据抽屉中探测模型改为从凭据下模型列表选择；无模型时才手工输入 | `web/src/views/provider-detail/CredsTab.vue` |
| R6 | （继承自 handoff）按 token 计费节点探测 ≥5 分钟一次；一个节点只用一个模型探；探测模型必须是上游可接受的名称 | `shared_pick.go` 约定 |

## 2. 审计发现与修正

### 发现 #1（P0，round-4 引入的回归）— deviation guard 阈值过紧，重引入探测成功死循环

round-4 将钳位阈值从 `max(linger, 5h)` 改为 `max(preProbeWindow, linger)=30min`，且目标为 `now()+30min`。
后果：**正确分类**的 5h 窗口凭据（`quota_recover_at` 距今最长 5h）也满足 `> now()+30min` 而被钳短 →
probe 模型探测成功（probe 模型不消耗业务模型配额）→ `writeHealth` 提前清 `quota_state` → 路由重新准入 →
业务请求 429 → writer 重新挂起 → guard 再钳 → **~35 分钟周期的死循环**。这正是
`bg/credential_recovery.go` 2026-08-08 P0 注释明确警告的模式。

**修正**：阈值恢复 `max(recoverAtMaxLinger, 5h)`（5h 下限不可协商）；钳位目标改为 **下一个 5h 网格边界**
（UTC+8 00/05/10/15/20，Go 侧 `nextFiveHourBoundaryCST` 计算，镜像 `domains/credential/writer.go`）。
误分类到"次日 UTC 午夜"的行会在其**真实**边界恢复：不提前（死循环安全）、不再多等 5h（用户诉求）。

### 发现 #2（P1，存量缺陷顺带修复）— weekly/monthly 窗口被 guard 压缩成 5h 循环

周/月窗口的 `quota_recover_at`（下周一 / 下月 1 号）必然超过 5h 阈值，旧版 guard 会把它钳成 5h，
造成以 5h 为周期的"提前恢复→429→重挂"循环。

**修正**：SQL 排除 `state_reason_detail` 含 week/month/周/月 字样的行（与 writer 分类用的词元一致）。

### 发现 #3（P1）— AutoFillDefaultProbeModel 存了 standardized_name

`default_probe_model` 会被探测请求**原样**发给上游。NIM 类供应商 raw name 带前缀（`z-ai/glm-5.2`），
存 standardized `glm-5.2` 会让每次探测 404。既有约定（`bg/shared_pick.go`、handoff §2.2）是
`COALESCE(outbound_model_name, raw_model_name)`。

**修正**：pick 表达式改为 `COALESCE(pm.outbound_model_name, pm.raw_model_name)`，排序（`created_at DESC`）
与守卫不变；pin 测试同步收紧（显式禁止 `sub.standardized_name` 写入）。

### 发现 #4（P2）— 前端取值不一致 + 空值 PATCH

- 抽屉 `<select>` 的 value 用了 standardized_name（同发现 #3）→ 改为 `outbound_model_name ?? raw_model_name`，
  label 保留 standardized 便于辨识；
- `setDefaultModel` 在"清空一个本就为空的值"时会发一次无意义 PATCH → 与"未修改即 no-op"统一；
- `ProviderCredential.default_probe_model_source` 联合类型缺 `auto:domestic_featured` / `auto:refresh_latest`，
  `sourceLabel` 也未渲染 → 补齐（8 个 locale 同步）。

### 观察项（不改动，备案）

- `ProbeNow` 的 SELECT 仍要求 `default_probe_model <> ''`：periodic probe 提交的"无探测模型凭据"会被静默跳过，
  由 R3/R4 的自动填充在 ~5 分钟内补齐后自然闭环。handoff 明确"不要修改 credential_probe_v2.go 核心逻辑"，故不动。
- `nextFiveHourBoundary`（writer 与本守卫共用公式）对 CST 20:00–24:00 时段返回"次日 01:00"而非"次日 00:00"，
  为两处共享的既有 quirk；guard 与 writer 保持同一公式即为自洽，单侧修 Grid 反而会失配。

## 3. 变更清单

| 文件 | 变更 |
|---|---|
| `bg/periodic_quota_probe.go` | #1/#2 修正 + pre/post-probe 补 default_probe_model 兜底（round-4） |
| `bg/periodic_quota_probe_test.go` | pin 测试改为钉住"网格边界 + 5h 阈值下限 + week/month 排除"契约 |
| `modelcatalog/upsert.go` | 新增 `AutoFillDefaultProbeModel`（#3 修正后版本） |
| `modelcatalog/upsert_test.go` | 5 个行为/契约测试 |
| `discovery/discovery.go` | discoverForCredential / discoverFromManifest 拉取后自动填充 |
| `admin/provider_vendor.go` | 手动刷新 models 后自动填充 |
| `bg/default_probe_scanner.go`（新） | 5 分钟周期扫描补齐（R4） |
| `bg/default_probe_scanner_test.go`（新） | 4 个契约测试 |
| `cmd/gateway/main.go` | scanner 启动/停止接线 |
| `web/.../CredsTab.vue` | 抽屉内嵌选择器（R5，#4 修正后版本） |
| `web/src/api/providers.ts` | source 联合类型补全 |
| `web/src/locales/*/providerDetail.ts` ×8 | 新增 picker 与 source 文案 |

## 4. 验证

- `go build ./...` 通过；`go vet`（bg / modelcatalog / discovery / admin / cmd/gateway）无告警
- `go test ./bg/ ./modelcatalog/ ./discovery/ ./admin/` 全绿（admin 含 66s 全量）
- `web`: `vue-tsc --noEmit` 干净；vitest 96 文件 / 657 用例全过

## 5. 遗留跟进建议

1. `nextFiveHourBoundary` 的 20:00–24:00 时段 grid quirk 若要修，须 writer 与 guard 同步（另开任务）。
2. `ProbeNow` 对空 `default_probe_model` 的静默跳过可在探测侧增加"从 cmb 兜底选模型"（须避开 handoff 固化逻辑，先评估）。
3. `probeSetManual` / `defaultProbeModelPrompt` 等 locale key 已无引用，可在下一次翻译清理时删除。
