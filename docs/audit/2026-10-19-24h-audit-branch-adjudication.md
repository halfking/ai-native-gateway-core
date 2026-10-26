# 2026-10-19 24h 审计轮 · 未合并子分支逐条裁决（含 1 条远端已删的孤儿提交）

> 范围：本机 9 条含「未进 main 提交」的本地分支，外加 1 条**远端分支已被删除**
> 但提交从未进 main 的孤儿提交（`feat/app-icon-standard`）。
> 判据：**内容是否在 main 已被独立落地**（AGENTS.md 判据纪律 #5 逐文件去 main
> 找等价物），而不是「diff 是否为空」；**diff 非空 ≠ 分支有未落地内容**。
> 并行检出：`.git/index.lock` 有一条 17:08 遗留的 0 字节陈旧锁（`lsof` 无持有者），
> 先移走才能 FF；这**不是**并发任务在跑。

## 〇 结论速览

| 分支 / 提交 | ahead | 裁决 | 依据 |
|---|---|---|---|
| `2026-07-25-realtime-routing-self-heal` | 1 | **删** | 24/24 文件在 main 有逐字节相同副本（§一） |
| `docs/hyper-mobile-4-6-43` | 2 | **删** | §4.6.43 / §4.6.44 已在 main（§二） |
| `docs/sync-readme-version-2026-09-06` | 2 | **删** | README 已落地后又被开源化提交删除；sessions-v2 不可合（§三） |
| `feat/m3-sr-w3c` | 1 | **删** | 与上一条同一 README 补丁（§三） |
| `fix/context-limit-p2-audit` | 1 | **删** | 符号与接口签名全部已在 main（§四） |
| `fix/w5-ml-ci-runbook` | 1 | **删** | 实质代码已在 main 独立落地（2026-10-17 轮已记） |
| `fix/w6-monitor-docs` | 1 | **删** | F-7 被 main 显式取代，合并会回退 `node_status`（2026-10-17 轮已记） |
| `fix/hotzone-plan-critical-audit` | 1 | **删** | 同 patch-id 的 `ef7743f2b` 已在 main，分支是更早快照（§五） |
| `fix/credential-monitor-audit` | 1 | **前向重实现**（不整分支合） | 部分未落地且是真安全缺口；整分支合会引入重复计数视图（§六） |
| `feat/app-icon-standard`（远端已删） | 1 | **cherry-pick 救回** | 提交从未进 main，且逐文件确认 main 无等价物（§七） |

**净结果：9 条分支清掉 8 条，1 条改为在 main 当前文件上前向重实现；
另救回 1 条远端已删、从未落地的孤儿提交。**

---

## 一、`2026-07-25-realtime-routing-self-heal` —— 24/24 文件已在 main

分支唯一提交 `2a7696c46`（归档 2026-06~07 设计文档草稿，24 篇）。

`docs/superpowers/plans/*` 在 main 的**不同路径**下有逐字节相同副本：
`docs/archive/process/superpowers/specs/*`（23/24 逐字节相同）。
剩 1 篇 `2026-07-25-realtime-routing-self-heal.md` 是**同名不同文档**陷阱：
main 下有两份

- `docs/archive/2026-07/process/2026-07-25-realtime-routing-self-heal.md`（另一篇）
- `docs/archive/process/superpowers/specs/2026-07-25-realtime-routing-self-heal.md`
  （与分支版本 blob **完全相同**：`8950095d…`）

⇒ 24/24 全部已落地。合并只会在 `docs/superpowers/plans/` 下多出一份重复副本。

## 二、`docs/hyper-mobile-4-6-43` —— 章节已在 main

分支 2 个提交只改 `docs/UI规范/10-审计与实施路线.md`（§4.6.43 / §4.6.44）。
main 的同名文件里这两个小节都在（§4.6.43 在 5058 行、§4.6.44 在 5167 行），
且 main 已续写到 §4.6.49+。⇒ 已被独立落地并继续演化。

## 三、`docs/sync-readme-version-2026-09-06` / `feat/m3-sr-w3c` —— README 已落地后被删除；sessions-v2 不可合

**README 两个提交**：`2d0dc78b0`（v2.4.2→v2.4.7）与 `988f3683b`（v2.4.8→v2.5.0）
**patch-id 不同**（`9410d426…` vs `338f22bb…`），但同一编辑的**两代**：
第三个同题提交 `4096a06e6`（v2.4.7→v2.5.3）**已在 main**。
而 main 的 README 现在**根本没有 badge**（`grep 'Version-v'` 0 命中）——
被 `53971b270`「prepare repository for public open source release」整体重写删掉。
⇒ 两个 README 分支都已被取代，合了会把已删除的 badge 写回去。

**sessions-v2 提交 `ded2aefef`**（新增 `web/src/api/sessions-v2.ts` +
`web/src/views/SessionDetailView.vue`）：**未落地，但不可合**：

- main 的后端两个端点都在（`admin/session_detail_v2.go:178`、
  `admin/session_summary_v2.go:85`）；
- main 的前端已有同一功能，且落地**更早**（`aa8b7ec70`，2026-07-24 13:57）
  落在 `web/src/views/admin/SessionDetailPage.vue`，其后又演化 4 次；
- 分支版本与 main 版本**路由常量同名**（`SessionDetailView`）但指向不同文件
  ⇒ 直接合会 TS 重复声明；且分支路由 `/sessions/:sessionId` 是
  `requiresAuth`，main 的 `/admin/sessions/:id` 是 `requiresSuper`
  ⇒ **权限从 super 降级到普通登录用户**；
- 分支还带一条 main 前端刻意不用的写路径 `POST /api/admin/sessions/summary`
  （main 走已审计的 `instant-summary`）。

⇒ 判为**不可合**。若要取用，只应单独取 summary 端点，且由属主拍板。

## 四、`fix/context-limit-p2-audit` —— 符号与签名全部已在 main

分支 1 个提交（9 文件）。main 里 `ContextLimitUpdateQueue`、
`ParseContextLimitResult`、`ContextLimitAuthoritative`、
`ContextLimitObservedUsage`、`NewDBContextLimitUpdater` **全部存在**，
`ContextLimitUpdater` 接口也已是分支引入的 `(bool, error)` 签名
（`domains/streaming/executors/executor.go:218-220`）。
分支另 5 个文件「不一致」只是 main 后续演化 ⇒ 全部已落地。

## 五、`fix/hotzone-plan-critical-audit` —— 同 patch-id 的提交已在 main

分支唯一提交 `8a35ca690`（`docs/storage/2026-09-24-hotzone-dual-mode-plan.md`，
191 行）。main 的同名文件 256 行。同题同作者的 `ef7743f2b` **patch-id 相同**
（`96b56a94…`）且**已在 main**。main 另加了 §8 实施状态、§9 全量复核审计轮，
并把 §6 清单从 `[ ]` 翻成 `[x]`。
⇒ 分支是**更早的快照**，合了会回退 main 的清单状态并删掉 §8/§9。

## 六、`fix/credential-monitor-audit` —— 真安全缺口，改为前向重实现

分支唯一提交 `87baeaba5`「fix(admin): harden credential monitor access」。

**为什么不能整分支合**：该分支是 `fe7d1796c` 合并后被 `d2cbaf88b`
「All 7 merge conflicts resolved via upstream preference」**机械回退**的重复提交
（该 merge 把 6 处 `RequireSuperAdminForWrite` 一并按 upstream 偏好丢掉，
提交说明亦写明非冲突改动留作「separate review/handling」）
⇒ 这一族安全意图**至今没有真正落地**，不是「已决定不做」。
但整分支合会：重引入 340 世代的 3-way UNION 视图（对已挂载分区
`routing_decision_log_2026_07` 重复计数）、并与 main 现行
「三个 tab 并行加载」契约冲突（main 已重构进 `composables/`）。

**真实缺口（在 main 上实测确认）**：

- `AdminMiddleware`（`admin/auth.go:86`）**只认证不检 role**；
- 读侧早在 audit #153/#154 定下下限「super_admin / admin_key / tenant_admin」
  （`admin/credential_monitor.go:634` 等）；
- 写侧**完全没有门**，且 promote / demote / set-concurrency / model-toggle 的
  UPDATE **不带租户过滤** ⇒ 任意已认证用户可跨租户改写凭据状态。

**落地方案（在 main 当前文件上前向重实现，提交 `f813d3351`）**：

1. 写侧按两条收口：① 管理员下限（与读侧同标准，挡 `role=user`）；
   ② 写操作需 `super_admin`（`admin/context.go:79` 的既定约定：tenant_admin 只读）。
   **只上 ② 不够**——`RequireSuperAdminForWrite` 只挡 tenant_admin，会放过 `role=user`。
2. 门**早于** nil-db 判空：未授权者得 403 而非 503（503 泄露 DB 是否已配置）。
3. nil-db 判空放在**首次真正访问 `m.h.db` 之前**（不是紧跟角色门）：保住 main
   既有语义「入参校验 400 优先于 nil-db 503」。分支那份把 nil-db 放在角色门
   之后，会把非法 JSON 的 400 变成 503 —— 改的是 main 已被判据钉住的行为。
4. 连带修两处真缺陷：summary 缓存失效漏掉 **provider 全量**条目（key 里的
   `:c0:`，credential 缺省为 0），以及 summary 路径的 slog 把
   **ProviderID 打进 credential_id**（3 处）。

⚠ **用户可见后果**：桌面端 `web/src/config/appNav.ts` 的「凭据监控」席既无
`super` 也无 `hideForTenant` ⇒ tenant_admin 仍会看到 promote/demote 按钮，
点下去现在拿到 403。服务端策略已定，前端按钮档位属 UI规范 所记
「三处口径各成立、该由属主拍板」的范围，**本轮未动前端**。

## 七、`feat/app-icon-standard` —— 远端分支已删、提交从未进 main（孤儿提交）

远端分支 `feat/app-icon-standard` 已被删除（`git ls-remote` 0 命中），
其唯一提交 `b6cf837df`（2026-10-17 23:09，author `halfking`）**从未进 main**。

AGENTS.md 早前记过该分支「反复被 force-push 重建」且**混着已上 main 的 revert**
⇒ 处置口径是「逐文件去 main 找等价物」。本轮逐文件核对结果**与当时不同**：
当前这个提交**只含图标资产与 index.html 的 link 标签**，
`web/public/favicon.svg` / `favicon-32.png` / `apple-touch-icon-180.png` 在 main
**均不存在**，main 的 `web/index.html:18` 仍指向旧的 `/favicon.png`
⇒ 属**未落地的新增能力**，不是同源重复，可安全并入。

按 AGENTS.md 自己的处置口径以 **cherry-pick** 救回（不复活被删的远端分支），
提交 `c6c65e35c`；附带把 icon link 的缩进对齐所在文件既有的 4 空格风格并补自闭合斜杠。

## 八、留档 SHA（分支删除后仍可按 SHA 取回）

| 分支 | 留档 SHA |
|---|---|
| `2026-07-25-realtime-routing-self-heal` | `2a7696c467dbb858cfc8ab1d89adb7137ff8229b` |
| `docs/hyper-mobile-4-6-43` | `f79135b136ccfca4907207602cddec398598de57`、`ff153f768f201a60c3c32b38ca7dbe915b58790f` |
| `docs/sync-readme-version-2026-09-06` | `ded2aefef3ce9b64212909fbc5642be012c4b843`、`2d0dc78b0c246cf71526a0ddb773f0cdf6a524a5` |
| `feat/m3-sr-w3c` | `988f3683ba6b437dc9d719d7111caab90fa25d64` |
| `fix/context-limit-p2-audit` | `71c792318ca9ef92a4fa412804c3e5ce106e62f5` |
| `fix/credential-monitor-audit` | `87baeaba56ec0a152f2d1e0a19da03669c087ea5` |
| `fix/hotzone-plan-critical-audit` | `8a35ca690f3a6c27a427f1dfca9d1d060b07a2b8` |
| `feat/app-icon-standard` | `b6cf837df82b66678d4d97d34bbe4ce450935ea6` |

## 九、本轮新增的可复用纪律

1. ★★ **「分支被删」≠「分支内容已落地」。** 远端分支被删只是**远端状态**；
   它的提交可能从未进 main。本轮 `feat/app-icon-standard` 正是如此，
   逐文件核对后判定为可救的孤儿提交 ⇒ 删分支与丢工作是两件事。
2. ★★ **「已合入过」≠「已落地」。** `fix/credential-monitor-audit` 的内容
   确实被合入过（`fe7d1796c`），但随后被一次「冲突按 upstream 偏好解决」的
   merge **机械回退**，且提交说明写明非冲突改动留作 separate review
   ⇒ 判据要看**当前 main 里有没有**，不能看分支合没合过。
3. ★★ **同名不同文档是最容易骗过逐文件比对的形态。**
   `2026-07-25-realtime-routing-self-heal.md` 在 main 下有两份，
   只有比到 `docs/archive/process/superpowers/specs/` 那份才确认是同一篇。
4. ★★ **安全门被回退的成因要分清「刻意」与「机械」。**
   本轮的 `d2cbaf88b` 提交说明写的是冲突按 upstream 偏好解决
   ⇒ 属机械回退，不是「决定不做」，故该重做而不是重裁决。
5. ★★★ **加一道门会连带改掉既有判据的语义。** 把 nil-db 判空放在角色门之后，
   会把「非法 JSON → 400」变成 503 —— 那是 main 已被判据钉住的行为。
   ⇒ 判据红了先查**是不是自己改掉了被测行为**，而不是改判据去迁就实现。
6. ★★★ **允许侧必须有正向断言。** 门类判据若只断言「未授权者被挡」，
   门被改成「一律挡」也全绿。本轮用「super_admin 必须穿过角色门并落到
   nil-db 的 503」钉住允许侧，并对同一处跑了 5 个变异全部按预期变红。
