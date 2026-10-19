# 2026-10-20 24h 审计轮 · 凭据写侧角色门前端同步 + 三条分支收口

> 接续 `docs/audit/2026-10-19-24h-audit-branch-adjudication.md`（上一轮把
> origin/main 拉回本地、裁决 9 条未合并分支、救回 app-icon 孤儿提交）。
> 本轮处理上一轮留下的 8 项待办，重点是**上一轮自己埋下的一个误判**。

## 〇、结论速览

| 项 | 上一轮的说法 | 本轮实测 | 处置 |
|---|---|---|---|
| ① 前端写操作按钮 | 「未动，属 UI规范『三处口径』待属主拍板」 | **误引**：UI规范 登记的「三处口径」是 `/sliding-window` **路由访问**分歧，不是写按钮档位 | 已按后端 2026-10-19 的写侧门同步（5 个面） |
| ② sessions-v2 | 「未落地但不可合，需属主拍板」 | 不可合成立，但**理由比上轮写的更硬**（见 §三） | 不合；无需属主拍板 |
| ③ w6-monitor-docs | 「仅剩文档漂移待订正」 | 成立，但**分支那份订正文本本身有 2 处是错的**，照抄会回退已知缺陷 | 按 main 现状订正 3 份文档 |
| ④ credential-monitor 剩余判据 | 「2 条判据 + t.TempDir()」 | 2 条判据**照抄必假绿**（mock 回显自己） | 在 main 当前 SQL 上重写；另补 1 处真缺口 |
| ⑤ local-gitea 镜像 | 「已分叉，19882 文件差异，绝不可推送到 codeup」 | **已不成立**：其 main 是 origin/main 的**祖先**，0 独有提交 | 无需属主拍板；镜像定位仍待定 |
| ⑥ go.mod puddle indirect | 「hygiene 项未动」 | 成立，且 `golang.org/x/sys` **同病** | 单独提交修正 |
| ⑦ 9 条陈旧分支 | 「可安全删除，需 owner 确认」 | 删前条件已全部核实 | 已删（留档 SHA 已在上一轮文档） |
| ⑧ 并发推送 | 「main 高频变动，推送后需再核对」 | 成立 | 推送前 fetch、推送后 ls-remote 复核 |

## 一、★ 上一轮的「待属主拍板」是**误引 UI规范** ⇒ ① 自主落地

上一轮在 `2026-10-19-24h-audit-branch-adjudication.md` 写：

> 服务端策略已定，前端按钮档位属 UI规范 所记「三处口径各成立、该由属主拍板」的范围，**本轮未动前端**。

**核验结果**：UI规范 里那条「三处口径」登记（`docs/UI规范/17-网关移动前端Hyper应用.md:18721`
「`/sliding-window` 三处口径互不相同」）讲的是**路由访问层**：

| 层 | 口径 |
|---|---|
| 抽屉席 | `super_admin`（`config/appNav.ts`） |
| 视图兜底 | `['super_admin','admin_key']` |
| 服务端 | 没有下限（`h.admin` 只认证不检 role） |

它与「凭据监控席的写按钮该不该给 tenant_admin 看」**不是同一件事** ⇒
上一轮拿它当阻却理由，属于**误引**（自己的上一节结论也要复核）。

⇒ ① **不是待拍板项**，而是后端 2026-10-19 写侧门的**必然推论**，本轮直接落地。

### 1.1 后端门已定：写操作一律 super_admin

`admin/credential_monitor.go` 的 6 个写 handler 全部收口：
promote / demote / set-concurrency-auto / model-toggle / set-manual-disabled /
clear-manual-disabled。口径是两条（`admin/context.go:79`）：

1. 管理员下限：super_admin / admin_key / tenant_admin（挡 `role=user`）
2. 写操作需 super_admin（**tenant_admin 只读**）

`admin/handler.go:942` 写明 default 租户 tenant_admin 的定位就是
「只读 + 凭据 API Key 轮换」；`admin/superadmin_test.go:21` 就在用
`SignToken(42, "default", …, "tenant_admin", …)` ⇒ **default 租户的 tenant_admin 是真实存在的角色**，
不是理论形态。

### 1.2 前端缺门的是 3 个面（不是上一轮点名的 3 个组件）

上一轮点名 3 个组件；逐文件扫描「调用 super_admin 门后端写 API 的全部前端位置」
→ 覆盖 **5 个面**，其中**缺门的是 3 个**（⚠ 2026-10-21 订正：原文此处写
「发现真正缺门的是 **5 个**」，与紧随其后的表格**自相矛盾**——表里 5 行只有 3 行标 ❌）。
数目巧合（3 = 3）容易让下一个人以为「点名的 3 个 = 缺门的 3 个」，**集合并不相同**：

| 面 | 调用的写 API | 上一轮时的门 |
|---|---|---|
| `web/src/views/CredentialMonitorView.vue` | promote / demote | ✅ 已有 `isSuperAdmin()`（禁用+守卫） |
| `web/src/components/credential-monitor/CredentialDetailDrawer.vue` | promote / demote / setConcurrencyAuto / toggleModelAvailability / clear+setManualDisabled | ✅ 已有（禁用+守卫） |
| **`web/src/views/CredentialHeatmapView.vue`** | toggleModelAvailability / promote / demote / setManualDisabled | ❌ **零门** |
| **`web/src/composables/useNodeDetailDrawerLoad.ts` + 4 个面板**（⚠ 2026-10-21 订正：原文写「2 个面板」，实际消费 `canEdit` 的是 `NodeDetailOtherModelsPanel` / `NodeDetailMaintainPanel` / `NodeDetailEmergencyPanel` / `NodeDetailConcurrencyPanel` **4 个**） | setManualDisabled / toggleModelAvailability / setConcurrencyAuto | ❌ 门只判「是否 default 租户」 |
| **`web-mobile/src/components/credential/CredentialOpsSheet.vue` + 热力图入口** | promote / demote / setConcurrencyAuto / toggleCredentialModel | ❌ **零门** |

节点详情抽屉那三处最隐蔽：`canEdit` 的定义是
`isDefaultTenant() && (requireSuperAdminEdit ? isSuperAdmin() : true)`，
而 `requireSuperAdminEdit` 是个**默认不收紧**的可选 prop，只有
`RoutingDashboardView` 传了 `true`；`NodeStatusMatrix` 与 `QueuePerspectivePanel`
两个入口没传 ⇒ **default 租户的 tenant_admin 能点到写按钮，点下去只拿到 403**。

⇒ 本轮把抽屉的 `canEdit` **统一收紧为「一律要求 super_admin」**（含移除那个
默认不收紧的 prop），因为该抽屉的**每一个**写操作后端都要求 super_admin：
session-ping / models-test / emergency-repair / candidate-binding → `h.superAdmin`；
set-concurrency-auto / model-toggle / manual-disabled → `RequireSuperAdminForWrite`；
`PATCH /api/providers/{id}/credentials/{id}` → providerConsole 对 tenant_admin **只读**
（`admin/provider_access.go:67-74`）。

### 1.3 口径：禁用+守卫（不是隐藏）

同一页面族内**必须一致**：「凭据监控」列表页与「探测健康」tab 是**同一个页面**
（`/routing-v2/credentials?tab=probe-health`）⇒ 两处口径不同正是 UI规范
登记过的那类「三处口径互不相同」。已落地的两个组件用**禁用+守卫**（自 2026-09-04
`127d8c2f5` 起），本轮沿用，不改它们。
移动端沿用该端既有惯例（`NodesView` 用 `v-if` 隐藏写入口）。

## 二、★ 判据纪律：写按钮的「没被调用」也可能是假绿

热力图那条「tenant_admin 点写按钮 ⇒ 写端点一个都不发」**第一版是假绿**：

- Vue Test Utils **不给 disabled 元素派发事件** ⇒ 事件压根没进 `runAction`
  ⇒ 「端点没被调用」是因为**按钮禁用**，不是因为**函数里的守卫**；
- 变异验证（把守卫删掉）⇒ **判据全绿**。

修法：测试里显式 `removeAttribute('disabled')` + `disabled = false`，
模拟「按钮态被绕过」，再断言端点仍未被调用。修完后同一变异如期变红。

⇒ **「按钮禁用」与「函数守卫」是两条独立的防线，必须分别钉**；
只钉前者等于没钉后者。

（移动端 `CredentialOpsSheet.spec.ts` 同样补了允许侧正向断言：
super_admin 确实能把 `toggleCredentialModel` 发出去。）

## 三、sessions-v2：**不可合成立，但理由比上一轮更硬**（②）

`ded2aefef`（在 `docs/sync-readme-version-2026-09-06` 分支上）的 4 个文件逐个对 main：

| 分支文件 | main 现行等价物 | 结论 |
|---|---|---|
| `web/src/api/sessions-v2.ts`（213 行） | `web/src/api/sessions_v2.ts`（609 行） | **已落地且更完整**（多 turns/bodies/snapshot/instant-summary/attachments） |
| `web/src/views/SessionDetailView.vue`（1070 行单体） | `web/src/views/admin/SessionDetailPage.vue`（261 行页面壳）+ `web/src/components/session/` 下 11 个带测试的组件 | **已落地且已拆分** |
| `web/src/router.ts` 加 `/sessions/:sessionId` | `/admin/sessions/:id`（`requiresSuper`） | **已落地且权限更强**；分支版是 `requiresAuth` ⇒ 合进去是**降权** |
| `web/src/api.ts` 加 `export * from './api/sessions-v2'` | main 直接 `from '../../api/sessions_v2'` | 无需 |

补三条上一轮没写的硬证据：

1. 分支路由的 `const SessionDetailView = () => import('./views/SessionDetailView.vue')`
   与 main 的 `const SessionDetailView = () => import('./views/admin/SessionDetailPage.vue')`
   **同名不同文件** ⇒ 直接合会 TS 重复声明；
2. 分支的 4 个 tab（Original/Compressed/Security/Metadata）⊂ main 的
   `TurnDigestDrawer.vue` 的 **8 个 tab**（summary/request/response/compression/
   waterfall/meta/governance/attachments）⇒ main 是**超集**；
3. 分支唯一独有的人机接口 `POST /api/admin/sessions/summary`，
   main 的 `triggerInstantSummary`（`POST /api/admin/sessions/{id}/instant-summary`）
   **复用了同一个 `SessionSummaryV2API.generateSummary`**（`admin/session_turns_v2.go:830`）
   并额外回写 `public.sessions` 让前端轮询感知新鲜度 ⇒ main 是**演化后的形态**。

⇒ **仍判不可合**；但上一轮留的「或只取 summary 端点，由属主拍板」这一步**不再必要**：
取那条端点等于把前端接回**旧形态**（无持久化、无新鲜度语义）。

## 四、w6-monitor-docs：分支那份订正文本**自己就有 2 处是错的**（③）

F-7（Prometheus 打点 + 1min 进程内缓存 + singleflight）被 main 的 slog 方案
刻意取代（`62997450d`），**合分支会回退 main 的 node_status 特性** ⇒ 不合。
但文档漂移是真的，逐项核对了 5 处：

| 漂移项 | main 现状 | 分支那份文本 | 处置 |
|---|---|---|---|
| Tab 数量 | **五个**（`错误趋势` 是 R12 候选18 后加的） | 「四个」 | 按 **五个**订正 |
| localStorage key | 单一 `credential_heatmap_expanded`（数组） | 单一 key ✓ | 采纳 |
| `is_self_test` | **无此列**；判据是三臂 `quality_flags`/`task_type`/`origin_actor` | 两臂且 `NOT ('probe' = ANY(quality_flags))` **无 COALESCE** | **不采纳**（见下） |
| `date_trunc` | epoch 对齐 `to_timestamp(floor(EXTRACT(EPOCH FROM ts)/N)*N)` | epoch ✓ | 采纳 |
| 单文件组件树 | `CredentialHeatmapView.vue` 1371 行单文件 | 单文件 ✓ | 采纳 |

⚠ **分支那份 `is_self_test` 文本照抄会回退一个已知缺陷**：
`admin/credential_monitor_heatmap.go:317-323` 明写
「`NOT ('probe' = ANY(quality_flags))` 没有 COALESCE ⇒ NULL 行的 `NOT NULL`
不为 TRUE ⇒ 那些行被**静默丢掉**，实测 7d 内会话族 40,275 行里吞了 40,225 行」。
真实谓词是 `bg.ProbeTrafficExclusionPredicateView`（`bg/probe_policy.go:188`），
**三臂且每臂带 COALESCE**。

⇒ 3 份文档（deployment-guide / requirements / implementation）按 **main 现状**订正，
并把「设计稿 vs 落地」逐处标出来（组件树、Redis 缓存、虚拟滚动、`is_self_test` 列、
索引形状都标成「设计稿，未落地」）。

## 五、credential-monitor 剩余判据：照抄必假绿 ⇒ 在 main 当前 SQL 上重写（④）

分支的 `TestRunMonitorSummary_DetailModelsDeriveStateAndWorstRate` 断言
`*AggregatedSuccessRate == 0.72` —— 而 0.72 正是 **mock 行自己塞进去的值**
⇒ 那是「mock 把自己回显给自己」，删掉整段聚合逻辑照样全绿。
且 detail 模式的 SQL 里 `aggregated_success_rate` 本来就是
`NULL::double precision` ⇒ 该断言在 main 上**必红**。

本轮重写为两段**各自独立成立**的断言：

- **Go 侧派生**（真被测逻辑）：models JSON → scan → `deriveModelEffectiveState`
  / `humanizeDisabledReason` ⇒ manual_offline→`manual_disabled`、
  broken_confirmed→`probe_broken` + 理由文案；
- **worst-rate 语义**落在 **SQL 文本**上：summary 模式必须
  `MIN(rsr.rate) AS aggregated_success_rate` + `recent_success_rate`；
  core 模式必须**不**算它；detail 模式必须显式为 NULL。

分支的 `TestHandleMonitorSummary_NilDB` 在 main 上**也必红**：main 的
`handleMonitorSummary` **没有** nil-db 判空 ⇒ 拿 nil querier 去 Query 会
**nil pointer panic**（分支那版 handler 家族其余 9 个都有这道门）。

⇒ 本轮**补上了这道门**（真缺口，一致性缺口而非线上事故：生产上 `h.db` 恒接线），
位置遵循本组既定口径：**角色门之后**（未授权者得 403 不是 503）、
**缓存命中之后**（缓存不需要 DB，不该被拦）。
并补一条判据钉住这个顺序（`TestHandleMonitorSummary_RoleGateBeforeNilDB`）。

⚠ 写这道门时踩了两个坑，都记在这里：

1. **判的是 querier 不是 `m.h.db`** —— `summaryDB` 是测试注入点
   （`credential_monitor_server_duration_test.go:51`），只看 `m.h.db`
   会把「注入了 mock db」的测试误判成「没接 DB」；
2. **`monitorSummaryCache` 是包级全局** —— 默认 `provider_id=0` 那条
   已被同包其它测试缓存并**跨测试存活** ⇒ 本用例会走到缓存命中分支拿 200。
   ⇒ 必须用**唯一** `provider_id` 造 cache key。

`sensitive_words_handler_test.go` 的 `t.TempDir()` 化已采纳（写死的假路径
连父目录都不存在，走的是另一条错误分支）；分支那份
`NodeDetailDrawer.test.ts` 的重构**未采纳** —— main 已有等价判据
（`stale-history` / 换 scope 后丢弃旧数据两条都在）。

## 六、local-gitea 镜像：「已分叉」已不成立（⑤）

上一轮写「local-gitea 的 main 与 origin/main 已分叉（merge-base 30422274d，
19882 文件差异）且带 7 个 local-Gitea CI 提交 —— 绝不可推送到 codeup」。

**本轮实测**：

- `local-gitea/main` = `6f42ff788`，**正是**它与 origin/main 的 merge-base；
- `git merge-base --is-ancestor 6f42ff788 origin/main` ⇒ **是**（严格祖先）；
- `git log origin/main..local-gitea/main` ⇒ **0 条**独有提交。

⇒ 镜像的 main **可快进、已完全包含在 origin/main 里** ⇒ 「推到 codeup 会出事」
这个风险**在结构上已经不存在**（推过去是 no-op）。
那 7 个 CI 提交在 **`local-ci-validation` 分支**上，只动了
`.gitea/workflows/ci-local.yml`（43 行，本地 Gitea 专用工作流），
与 main 无关。

⚠ 另记一条**坑**：`git remote -v` 显示 `codeup` 与 `origin` 是**同一个 URL**
（`codeup.aliyun.com/kaixuan/official-deploy/llm-gateway-go.git`）⇒
「推到 codeup」和「推到 origin」是同一件事，两个 remote 名是双重保险也是双重误导。
（`local-gitea` 的 URL 里内嵌了凭据，只在 `.git/config`，不入库。）

⇒ 镜像的**定位**仍待属主明说（它是本地 Gitea 的备份镜像？还是 CI 验证源？），
但「有人误推会出事」这条**已不需要属主介入**。

## 七、go.mod：`puddle` 与 `x/sys` 同病（⑥）

`github.com/jackc/puddle/v2` 被标成 `// indirect`，但
`domains/ursm/v2/persist/retention_partition.go:37` 与
`internal/trace/stage_events_retention.go:32` **直接 import**。
`golang.org/x/sys` **同病**（`admin/diskusage_windows.go`、
`internal/fsstore/flock_windows.go`、`bg/diskusage_windows.go` 直接 import
`golang.org/x/sys/windows`，却被标 indirect）。

`go mod tidy`（`-mod=mod`，离线）证实：两个都应移进直接 require 块。
但 tidy 同时要删 **341 行 go.sum**（`cel.dev/expr` / `cloud.google.com/*` 等
传递依赖的哈希）—— 那是**超出本项范围**的风险（别的会话正高频改本仓，
341 行 go.sum 变更会制造冲突；且某些只靠这些哈希的构建路径会缺条目）。

⇒ **只做 go.mod 的两行 require 块移动，go.sum 原样不动**；
`vendor/modules.txt` 里两者本就是 `## explicit` ⇒ 无需动 vendor。
`go build ./...` = 0 验证 vendor 一致性。

## 八、★ 本轮新增的可复用纪律

1. ★★★ **「引用了某条已登记的分歧」不等于「这条分歧讲的就是这件事」。**
   上一轮拿 UI规范 里 `/sliding-window` **路由访问**的三处口径，去挡
   **写按钮可见性** ⇒ 误引导致一个可自主落地的修复被挂起一轮。
   ⇒ 引用别人登记过的结论前，先确认**主语和宾语都对得上**。
2. ★★★ **「按钮禁用」与「函数守卫」是两条独立的防线。**
   VTU 不给 disabled 元素派发事件 ⇒ 「端点没被调用」可能**只是**因为按钮禁用。
   ⇒ 任何「点击后不应发生 X」的判据，若 X 之前有一道 disabled，
   必须显式绕过 disabled 才能真正测到函数层守卫。
3. ★★★ **判据里断言 mock 提供的值 = 断言 mock 把自己回显给自己。**
   分支那份 `*AggregatedSuccessRate == 0.72` 即是。
   ⇒ 断言要么落在**被测代码自己算出来的**值上，要么落在**更上游的契约**（SQL 文本）上。
4. ★★ **包级全局缓存会跨测试存活。**
   `monitorSummaryCache` 让「未接 DB ⇒ 503」这条用例拿到 200
   （同包别的测试把默认 key 缓存了）⇒ 造用例必须用**唯一** cache key。
5. ★★ **「照抄另一份文档的订正」要先核对那份订正本身。**
   w6 分支那份 `is_self_test` 文本会把一个已知缺陷（缺 COALESCE ⇒ 静默吞 4 万行）
   写回文档 ⇒ 文档漂移的修法要以**当前代码**为准，不是以**分支的修法**为准。
6. ★★ **判据红了先怀疑自己的前提，尤其是「mock 给了什么值」。**
   本轮那条 `detail mode must not fabricate` 第一版就是拿 mock 的值当契约。

## 九、门禁与验证

- `go build ./...` = 0（vendor 模式，含 go.mod 两行 require 块移动）
- `go vet ./admin/` = 0
- `go test ./admin/ -run 'TestRunMonitorSummary|TestHandleMonitorSummary|…'` = ok
- web：`pnpm vitest run src/views/CredentialHeatmapView.test.ts
  src/components/NodeDetailDrawer.test.ts` = 20 passed
- web-mobile：`pnpm vitest run src/components/credential/CredentialOpsSheet.spec.ts` = 10 passed
- web i18n：`src/i18n/keys_referenced` + `src/i18n/parity` = 全绿（3 个新键 × 8 语种）
  ⚠ **2026-10-21 订正**：这三条门**只比键集合 / 键是否存在，不比键值** ⇒
  `ar-SA` 的 `ops.overview.nodeDetailSuperAdminOnlyHint` 被填成**英文原文**仍然全绿
  （同一提交里另外两个 ar-SA 键是真阿拉伯语 ⇒ 是**漏**，不是该 locale 的整体状态）。
  本轮已修，并补 `src/i18n/localizedKeys.test.ts`（登记册门）钉住这一类。
- 硬编码中文棘轮：本轮净 **-2**（5757 → 5755，见下）
- 变异验证（全部按预期变红）：
  - 热力图：删函数守卫 → 「端点没被调用」红；删按钮 disabled → 「按钮全禁用」红
  - 移动端：门改 `if (false)` → tenant_admin 用例红；`isSuperAdmin` 恒 false → 允许侧红
  - admin：删 nil-db 门 → **nil pointer panic**；角色门挪到 nil-db 之后 → 403 用例红；
    改坏 `deriveModelEffectiveState` → 派生用例红
- 既有红门（**与本轮无关**，已用 pristine-HEAD worktree 取证）：
  - `src/i18n/hardcoded_cjk.baseline.test.ts`：HEAD **5757** > baseline **5529**
    （本轮 5755 ⇒ 本轮反而降了 2；红门是 2026-10-05 之后别的会话累积的）
  - `src/config/navCoverage.test.ts`：6 条孤儿路由，本轮**未改** `web/src/router.ts`

## 十、⑦ 9 条陈旧分支已删（留档 SHA 补登）

删前逐条核实了条件（上一轮留的三个前提都已满足）：

- **无 worktree 占用**：`git worktree list` 显示只有 `main` 与
  `codex/sse-traffic-audit-20261019` 被 worktree 持有；
- **别的会话不在本 clone**：`mavis session list` 里在跑的
  `syncfield/llm-gateway-go-2` / `-4` 等是**独立 checkout**
  （`git worktree list` 里不出现 ⇒ 不是本仓的 worktree），
  删本 clone 的分支不影响它们；
- **内容已在 main**（上一轮逐文件裁决结论，见 2026-10-19 文档 §〇）。

⚠ **上一轮的留档 SHA 表漏了两条**（`fix/w5-ml-ci-runbook` 与
`fix/w6-monitor-docs` 没登记）⇒ 补登在这里，否则删分支就等于丢工作：

| 分支 | 留档 SHA |
|---|---|
| `fix/w5-ml-ci-runbook` | `3147f5ef4f12827227ee8c9924b0c4e94aee59c8` |
| `fix/w6-monitor-docs` | `601bd36d1ecc66b6b1b3c8883ec8eb91cd57a7d9` |
| `feat/app-icon-standard-recovered`（本轮工作分支，已全量并入 main） | `5eb2427507` |

（其余 7 条的留档 SHA 已在 `2026-10-19-24h-audit-branch-adjudication.md` §八。）

删后按 SHA 逐条 `git cat-file -e` 复核可取回。
