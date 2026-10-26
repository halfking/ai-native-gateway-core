# 2026-10-21 24h 审计轮：对上一轮「凭据写侧角色门」的批判式复核

> **审计对象**：2026-10-20 24h 轮已推送的 6 个提交
> （`40de2db57` / `e19318817` / `c1ff8f425` / `5e4f42dc9` / `657b8183b` / `3eb2be915`）。
> **基线**：`3eb2be915`（= `origin/main`，工作区干净）。
> **方法**：不采信上一轮的「已收口」状态陈述，逐条**核验它写下的断言**——
> 后端门是否真在那个位置、前端门是否覆盖了全部调用点、判据是否真在测那件事、
> 文档里的数字是否与代码一致。发现即修，代码与文档同步订正。
>
> **结论摘要**
>
> | # | 项 | 判定 |
> |---|---|---|
> | 1 | 上一轮「后端写操作一律要求 super_admin」的断言 | ✅ **全部成立**，逐条定位到路由注册行 |
> | 2 | 上一轮「`canEdit` 收紧为 super_admin」的断言 | ✅ **成立且不过度**（抽屉内无 tenant_admin 合法写操作） |
> | 3 | `ar-SA` 的 `ops.overview.nodeDetailSuperAdminOnlyHint` | ❌ **真缺陷**：值是英文原文 ⇒ **已修** |
> | 4 | `editGateHint` 的「非 default 租户」分支 | ❌ **真缺陷**：仍是硬编码中文 ⇒ **已修** |
> | 5 | 上一轮审计记录的「漏了 5 个面」 | ⚠ **与自己的表格自相矛盾** ⇒ **已订正** |
> | 6 | 上一轮审计记录的「+ 2 个面板」 | ⚠ **实际是 4 个** ⇒ **已订正** |
> | 7 | 初始怀疑：`ProvidersView.saveCredential` 无角色门 | ✘ **误报**（死代码）⇒ **已排除并留证** |
> | 8 | 硬编码中文棘轮 / `navCoverage` 孤儿路由 | ⛔ **既有红门**，与本轮无关（pristine HEAD 同形态） |

---

## 〇、为什么这轮要复核「上一轮说自己做完的事」

上一轮把「前端写侧角色门」当**已收口**的工作提交了：门加上了、判据绿了、文档订正了。
本轮的任务**不是**再补一个门，而是**逐条验证这些断言**。

依据 AGENTS.md 判据纪律：
- 第 8 条「阴性结论要当缺陷一样复核」；
- 第 11 条「判据红了先怀疑自己的前提」；
- 第 15 条「『某门绿了』≠『不存在这样的门』」；
- 第 17 条「判据会把错误行为钉死，而且带着注释显得很有理」。

⇒ 上一轮的判据**全绿**这件事本身，就是本轮要检验的对象。

---

## 一、审计 A：后端「要求 super_admin」的断言 —— 逐条成立

上一轮在 `useNodeDetailDrawerLoad.ts` 的注释里把抽屉的每个写操作归到 5 个后端门上。
本轮**逐条回到路由注册行**核对（不是核对注释，是核对 `mux.HandleFunc`）：

| 前端写操作 | 后端端点 | 路由注册 / 门调用（**本轮实测行号**） | 门 |
|---|---|---|---|
| session-ping | `POST /api/admin/credentials/{id}/session-ping` | `admin/handler.go:1306` | `h.superAdmin` ✔ |
| models-test | `POST /api/credentials/{id}/test` / `.../models/{model}/test` | `admin/credential_state_handlers.go:176-178`（`wrap := h.superAdmin` 在 `:175`） | `h.superAdmin` ✔ |
| emergency-repair | `POST /api/routing/emergency-repair` | `admin/handler.go:996` | `h.superAdmin` ✔ |
| candidate-binding | `PATCH /api/routing/candidate-binding/` | `admin/handler.go:991`（另有 `.../candidate-bindings/reorder` 在 `:992`） | `h.superAdmin` ✔ |
| set-concurrency-auto | `POST /api/credentials/set-concurrency-auto` | 门在 `admin/credential_monitor.go:1190`（`handleSetConcurrencyAuto` 内） | `RequireSuperAdminForWrite`（`admin/context.go:80`）✔ |
| model-toggle | `POST /api/credentials/model-toggle` | 门在 `admin/credential_monitor.go:1333`（`handleModelToggle` 内） | `RequireSuperAdminForWrite` ✔ |
| set / clear manual-disabled | `POST /api/credentials/{set,clear}-manual-disabled` | 门在 `admin/credential_monitor.go:2064` / `:1943` | `RequireSuperAdminForWrite` ✔ |
| 手动并发 / 指纹槽位（`PATCH` 凭据） | `PATCH /api/providers/{id}/credentials/{cid}` | `admin/handler.go:1296`（`/api/providers/` → `h.providerConsole`） | `ProviderConsoleMiddleware` ⇒ default 租户 tenant_admin **只读** ✔ |

最后一条是**上一轮最关键、也最容易被漏看**的一条，展开核验：

`admin/provider_access.go:100-110` `providerTenantAdminAllowed` 的方法白名单只有
`GET` / `HEAD` / `POST`（且路径必须以 `/rotate-primary-key` 结尾），
`default:` 分支直接 `return false` ⇒ **PATCH 对 tenant_admin 必然 403**。
同文件 `:67-74` 的兜底文案就是「tenant_admin has read-only provider access;
only credential API key rotation is allowed」。

⇒ **审计 A 结论：上一轮的后端断言全部成立，无需改动后端。**

## 二、审计 B：`canEdit` 收紧是否过度收紧 —— 不过度

上一轮把 `canEdit` 从
`isDefaultTenant() && (requireSuperAdminEdit ? isSuperAdmin() : true)`
改成 `isDefaultTenant() && isSuperAdmin()`，并删掉了 `requireSuperAdminEdit` prop。

本轮核验三件事：

1. **改动前的 prop 语义**（从 `e19318817^` 取原文）：
   `requireSuperAdminEdit?: boolean` 是**可选**、默认 `undefined` ⇒ 旧表达式里落到 `: true` 分支 ⇒
   **默认不收紧**；只有 `RoutingDashboardView.vue:1680` 传了 `:require-super-admin-edit="true"`。
   ⇒ 上一轮对「这个开关正是缺口本身」的判断**属实**。
2. **三个入口**：`NodeDetailDrawer` 被 `RoutingDashboardView` / `NodeStatusMatrix` /
   `QueuePerspectivePanel` 三处引用，后两处**没传**那个 prop ⇒ 旧口径下 default 租户的
   tenant_admin 确实拿得到 `canEdit=true`。
3. **收紧后是否砍掉了 tenant_admin 本该有的能力**：抽屉里**没有**任何 tenant_admin
   合法的写操作——最接近的是「凭据 API Key 轮换」，而**抽屉里根本没有轮换入口**
   （`rotate-primary-key` 只出现在 `provider-detail/CredsTab.vue`，那里按
   `isProviderConsoleView()` 单独开权限）。⇒ **不过度收紧**。

⇒ **审计 B 结论：上一轮的收紧正确，不需要回退。**

## 三、审计 C：i18n 接线 —— 找到 **2 个真缺陷**

### 3.1 `ar-SA` 的新键被填成英文原文（真缺陷，已修）

上一轮给 8 个语种各加了 3 个键。逐个核对实际值：

| 键 | ar-SA 的值 | 判定 |
|---|---|---|
| `credentialMonitor.error.superAdminOnlyHint` | `يمكن للمشرفين الكبار فقط إجراء تصحيحات الحالة؛ حاليًا للقراءة فقط.` | ✅ 真阿拉伯语 |
| `credentialMonitor.error.superAdminOnlyError` | `يمكن للمشرفين الكبار فقط إجراء تصحيحات الحالة.` | ✅ 真阿拉伯语 |
| `ops.overview.nodeDetailSuperAdminOnlyHint` | `Only super admins can maintain nodes; showing read-only.` | ❌ **英文原文，与 en-US 逐字相同** |

**不是「ar-SA 整体没翻译」**：同一提交里的另两个 ar-SA 键是真阿拉伯语，
而且 `ar-SA/ops.ts` 的 `overview` 块**开头 10 个键**（`title` / `loadFailed` / …）也是真阿拉伯语
⇒ 是**这一条漏了**。

### 3.2 「非 default 租户」那支提示仍是硬编码中文（真缺陷，已修）

`editGateHint` 有两个阻塞分支。上一轮只把「不是 super_admin」那支接上了 i18n 键，
**「非 default 租户」那支仍是源码里的中文字面量**：

```
// 改动前
if (props.requireSuperAdminEdit && !isSuperAdmin()) return '仅超级管理员可以维护节点；当前以只读方式展示。'   // ← 已 i18n 化
if (!isDefaultTenant()) return '仅 default 租户可以维护节点；当前以只读方式展示。'                                  // ← 仍是字面量
```

**为什么这是用户可见的缺陷，而不是「无所谓」**：
`web/src/i18n.ts:81-94` 的 `defaultLocale()` 按 `navigator.language` 选界面语种
⇒ **英文/日文/德文…的浏览器**上，非 default 租户的用户会看到一句**中文**提示，
而同一屏上其它文案都是本地语言。

### 3.3 既有三条 i18n 门为什么全绿（量具的盲区）

| 门 | 它查什么 | 为什么漏掉这两个缺陷 |
|---|---|---|
| `i18n/parity.test.ts` | **键集合**（非 zh-CN 语种 ⊇ zh-CN 叶子键） | 键**在**，只是**值**是英文 ⇒ 通过 |
| `i18n/keys_referenced.test.ts` | 引用侧**键是否存在** + 无 PLACEHOLDER 字面量 | 同上；且它只查键名，不查值 |
| `i18n/hardcoded_cjk.baseline.test.ts` | `src/**` 的硬编码中文计数（**排除 `src/locales/**`**） | 翻译成英文**不涨**中文计数 ⇒ 管不着 |

⇒ 三条门各管各的，**「键在但值是英文」和「键在但值是硬编码中文」正好落在它们的缝隙里**。
本轮补的 `src/i18n/localizedKeys.test.ts`（登记册门）就是补这条缝。

### 3.4 一条**被旧判据钉死的缺陷**（值得单独记）

`NodeDetailDrawer.test.ts` 里那条既有用例
`disables maintain actions for non-default tenant` 断言的是
`expect(wrapper.text()).toContain('仅 default 租户可以维护节点')` ——
**它断言的正是那句硬编码中文**。

⇒ **那条用例只因缺陷而绿**：提示语当时是源码字面量，与界面语言无关。
本轮把提示改成 i18n 键后，它在 jsdom（`navigator.language` = `en-US` ⇒ locale `en`）
**立刻变红** ⇒ 反过来证明 §3.2 的缺陷是真实的、且是用户可见的。

⇒ 已把该断言改成「提示 = i18n 解析结果」（不钉字面量、随界面语言走）。

## 四、审计 D：`isSuperAdmin()` 在 `computed` 里是否真反应式 —— 成立

`web/src/store.ts:87` 的 `store` 是 Vue 的 **`reactive({...})`**，
`isSuperAdmin()`（`:235-240`）读的是 `store.userInfo?.role` / `store.jwtToken` / `store.apiKey`
⇒ 在 `computed` 内调用时，依赖会被**正常收集** ⇒ `computed(() => isSuperAdmin())` 是真响应式的。

⇒ **审计 D 结论：上一轮新增的 `canManageMonitor` / `canEdit` 无响应式缺陷。**
（`web-mobile` 侧同理：`web-mobile/src/stores/auth.ts:126` 的 `role` 是 `computed`。）

## 五、审计 E：文档与代码不一致 —— 3 处，已订正

| # | 位置 | 原文 | 实际 |
|---|---|---|---|
| 1 | `docs/audit/2026-10-20-24h-...md` §1.2 标题与引言 | 「前端漏了 5 个面」「发现真正缺门的是 **5 个**」 | 表格里 5 行**只有 3 行标 ❌** ⇒ **自相矛盾**；实为「覆盖 5 个面、缺门 3 个」 |
| 2 | 同上 §1.2 表格第 4 行 | 「+ **2 个**面板」 | 消费 `canEdit` 的是 **4 个**面板（OtherModels / Maintain / Emergency / Concurrency） |
| 3 | 同上 §九 门禁段 | 「web i18n = 全绿（3 个新键 × 8 语种）」 | 键**在** 8 语种成立，但 ar-SA 的其中一条是**英文原文** ⇒ 该行的「全绿」**不构成"已本地化"的证据** |

⇒ 第 1 条尤其实害：数目巧合（上一轮点名 3 个组件，缺门的也是 3 个面）会让下一个人
以为「点名的 = 缺门的」，**而集合并不相同**（桌面热力图 / 桌面抽屉 / 移动端面板）。

## 六、审计 F：初始怀疑（`ProvidersView.saveCredential` 缺门）—— **误报，已排除**

本轮最初怀疑：`web/src/views/ProvidersView.vue` 的 `saveCredential`（`:498-501`）
调 `updateCredential`（`PATCH /api/providers/{pid}/credentials/{cid}`），
而该路由对 tenant_admin 是 403（§一）⇒ 疑似又一个「能点但只拿到 403」的活缺口。

**逐层追调用链后排除**：

1. `saveCredential` 只被 `:1452` 的按钮调用，该按钮在 `manageProvider` 抽屉里；
2. 抽屉的显示条件是 `v-if="manageProvider"`（`:1324`），而 `manageProvider`
   只在 `openManageCred()`（`:403-407`）里被赋值；
3. **`openManageCred` 全仓零调用点**——`grep -rn "openManageCred" web/ web-mobile/` 只命中定义本身；
4. `git log -S "openManageCred" -- web/src/views/ProvidersView.vue` 只有两次变更：
   `dee57388a`（引入，含调用点）与 `e23d29337`（**删掉了调用点**，
   `-<button class="btn btn-ghost btn-sm" @click="openManageCred(p)">管理凭据</button>`）
   ⇒ **自 2026-06-12 起，该抽屉及其全部写操作就是死代码**。

⇒ 真正的凭据写面在 `web/src/views/provider-detail/CredsTab.vue`，
它**已有完整门控**：`canManageCreds = computed(() => isSuperAdmin())`
（`:62`）用于全部写按钮/输入框，
`canManageCredentialSecrets = computed(() => isProviderConsoleView())`（`:63`）
用于**唯一**对 tenant_admin 开放的动作（凭据 API Key 轮换）⇒ 与后端口径一致。

⇒ **判定为误报**。本条留证在此，避免下一轮重复查一遍。
⚠ 顺带登记：`ProvidersView.vue` 里 `saveCredential` / `delCred` / `refreshBalance` /
`checkSingleCredential` / `openDiagnose` / `submitCred` / `openCred` **整族死代码**
（连带 `openManageCred` / `showManageCred` / `manageProvider`）——**清理与否属主决定**，
本轮**不动**（删死代码会扩大回归面，且与本轮审计目标无关）。

## 七、本轮修复清单

| 文件 | 改动 |
|---|---|
| `web/src/locales/ar-SA/ops.ts` | `ops.overview.nodeDetailSuperAdminOnlyHint` 英文原文 → 阿拉伯语（用词取自该 locale 既有术语：`العُقد` 节点 / `للقراءة فقط` 只读 / `المشرفين الكبار` 超级管理员） |
| `web/src/locales/{zh-CN,zh-TW,en-US,ja-JP,de-DE,fr-FR,es-ES,ar-SA}/ops.ts` | 新增 `ops.overview.nodeDetailDefaultTenantOnlyHint`（8 语种各一份） |
| `web/src/composables/useNodeDetailDrawerLoad.ts` | `editGateHint` 的「非 default 租户」分支：硬编码中文字面量 → `i18n.global.t('ops.overview.nodeDetailDefaultTenantOnlyHint')` |
| `web/src/i18n/localizedKeys.test.ts`（新） | 登记册门：① 登记键在 8 语种都存在且非空；② **非英文语种的值不得与 en-US 逐字相同**；③ 空登记册判红（防空过） |
| `web/src/components/NodeDetailDrawer.test.ts` | 新增 3 条本地化判据（英文界面下提示是英文 + 零中文字符 / zh-CN 下解析成本地化文案而非原始 key 路径 / 两支口径一致）；并订正那条**钉死硬编码中文**的既有用例 |
| `docs/audit/2026-10-20-24h-audit-frontend-write-gate-and-branch-adjudication.md` | 订正 3 处与代码/事实不符的断言（§五） |
| `docs/audit/2026-10-21-24h-audit-frontend-write-gate-review.md`（新） | 本文件 |
| `docs/96小时内修订审计-20261007-1350.md` | 补 §九（本轮 handoff） |

### 判据纪律：这一轮的判据为什么这样写

- **「英文界面下提示是英文」而不是「提示等于 zh-CN 文案」**：
  zh-CN 文案与被替换掉的硬编码字面量**逐字相同** ⇒ 后者改回硬编码照样全绿（**假绿**）。
  ⇒ 判据必须落在「界面语言变了、提示也跟着变」这个**用户可见效果**上。
- **抽屉的 i18n 实例 ≠ 全局实例**：抽屉组件用的是组件内自建的 `createI18n`，
  而 `useNodeDetailDrawerLoad` 用的是**全局** `../i18n` ⇒ 切语言必须切全局那个，
  否则测试切了也没用。
- **登记册门只覆盖登记键**：存量 `ar-SA/ops.ts` 的 `ops` 命名空间仍有大段英文
  （该文件 564 行里只有 14 行含阿拉伯字母）⇒ 全量「值≠英文」扫描**必然是红的**，
  会把增量信号淹没在存量噪声里 ⇒ 选择登记册逐条收紧，存量单列为已知负债。

## 八、门禁与验证（实测读数）

### 8.1 定向门禁

```
cd web && pnpm vitest run src/i18n/localizedKeys.test.ts src/i18n/parity.test.ts \
        src/i18n/keys_referenced.test.ts src/components/NodeDetailDrawer.test.ts
                                        # 4 files / 33 passed
cd web && pnpm vitest run src/components/NodeDetailDrawer.test.ts
                                        # 1 file / 21 passed（17 既有 + 4 新增）
cd web && pnpm vitest run src/views/CredentialHeatmapView.test.ts
                                        # 上一轮的门，仍然全绿（本轮未动该文件）
```

### 8.2 硬编码中文棘轮

```
cd web && node scripts/i18n-cjk-count.mjs --json      # count 5755 → 5752（净 −3），baseline 5529
```

**会计核对（不能只报一个净值）**：被替换掉的字面量
`'仅 default 租户可以维护节点；当前以只读方式展示。'` 按 `[一-鿿]+` 计是 **3 段**
（`；` 是全角标点、**不在** CJK 范围内 ⇒ 把连续中文切成两段），
故该文件 CJK runs 9 → 6，净 −3，与总量变化**完全一致**。
本轮新加的中文注释**被扫描器正确剥离、未计入**（改后该文件的 6 段全是既有文案）。

### 8.3 变异验证（逐条备份 / 逐条还原 / 每步校验 md5 / 还原后跑基线）

| 变异 | 期望 | 实测 |
|---|---|---|
| M1 把「非 default 租户」分支改回**硬编码中文** | 变红 | ✔ **2 条红**：新增的「英文界面下提示是英文」+ 那条**钉死硬编码中文**的既有用例（见 §3.4） |
| M2 ar-SA 的 `nodeDetailSuperAdminOnlyHint` 改回英文 | 变红 | ✔ 1 条红（登记册门的核心腿） |
| M3 zh-CN 删掉 `nodeDetailDefaultTenantOnlyHint` | 变红 | ✔ 1 条红（存在性腿） |
| M4 **只**把 fr-FR 的 `nodeDetailDefaultTenantOnlyHint` 改成英文 | 变红 | ✔ 1 条红（单语种也能抓到） |
| 还原后基线 | 全绿 | ✔ 29 passed；4 个文件 **md5 逐字节一致** |

### 8.4 旁证：既有的 `keys_referenced` 扫描器**认得** `i18n.global.t(...)` 写法

把键名临时改成不存在的 `ops.overview.thisKeyDoesNotExist`
→ `keys_referenced.test.ts` 的 `missingInSource` **硬断言**变红 ✔（随后已还原，md5 一致）。

⇒ **盲区只在「键的**值**」维度，不在「键是否存在」维度**（§3.3 的结论据此收紧）。

### 8.5 Go 回归（本轮 **0 行** Go 改动，仅作确认）

```
go build ./...      # rc=0
go vet ./admin/     # rc=0
go test ./admin/ -run 'TestRunMonitorSummary|TestHandleMonitorSummary|TestSensitiveWords|
        TestBuildMonitorSummarySQLModes|TestMonitorSummary|TestCredentialMonitor' -count=1 -timeout 20m
                    # ok
```

### 8.6 全量 web vitest：与 **pristine HEAD** 同形态对照

在 `git worktree add --detach /tmp/lgw-head-probe-1021 HEAD`（= `3eb2be915`，**本轮改动前**）
上跑**同一条** `pnpm vitest run`（262 个文件），**串行**执行以免两轮互抢负载：

| | 失败文件 | 失败用例 | 通过用例 | 总时长 |
|---|---|---|---|---|
| pristine HEAD (`3eb2be915`) | **10** | 21 | 2678 | 374.8 s |
| 本轮工作树 | **11** | 25 | 2681 | — |

**失败文件集差集**：

- **只在基线里失败的**：无
- **只在本轮树里失败的**：`src/views/lifecycle/upgradeLatestVsUnknown.test.ts`

⇒ 该文件在两轮**都是 20s 超时**（不是断言失败），且
**在 pristine HEAD 上单独复跑得到逐字相同的 2 条超时**
（本轮树单跑：2 failed / 8 passed / 23088ms + 20026ms；
pristine 单跑：2 failed / 8 passed / 20015ms + 20000ms）
⇒ **与本轮改动无因果关系，是负载型 flake**。

旁证：同一批文件在两轮的**失败条数不同**
（`filterMeta` 1 → 3、`filterDownstreamFalseTruth` 4 → 2）
⇒ 超时集合本身在轮次间不稳定。

**基线里那 10 个失败文件的性质**（逐条看过错误类型）：

| 类别 | 文件 | 性质 |
|---|---|---|
| **真红门**（断言失败，与本轮无关） | `src/config/navCoverage.test.ts` | 「新增孤儿路由 6 条」 |
| **真红门**（断言失败，与本轮无关） | `src/i18n/hardcoded_cjk.baseline.test.ts` | `5755 > baseline 5529`（2026-10-05 后其它会话累积） |
| **负载型超时**（`Test timed out in 20000ms`） | 其余 8 个（6 个 `*.silentcatch.test.ts` + 2 个 `ReconciliationReport*`） | 其中 `ReconciliationReport.realdata.test.ts` 的性能断言实测 **14971 ms vs 闸 3000 ms** ⇒ 纯粹是机器负载 |

⚠ **第一轮全量跑里 `keys_referenced` 红过一次，但基线与第二轮都没有**：
那一轮的时间窗（02:03:19–02:09:34）正好覆盖**另一个会话**写
`web/src/views/CredentialHeatmapView.test.ts` 的时刻（02:05:51）
⇒ 扫描器读到了半写的文件。
事后取证：`node scripts/i18n-audit.mjs` 在当前树上 **PASS（0 missing keys）**，
且该文件单跑全绿 ⇒ **瞬时并发干扰，不是本轮缺陷**。
（这也提示：共享仓库多会话并发时，**全量测试的失败集合里可能有并发写入的产物**。）

### 8.7 web-mobile 定向回归

```
cd web-mobile && pnpm vitest run src/components/credential/CredentialOpsSheet.spec.ts
                                        # 1 file / 10 passed
```

## 九、本轮新增的可复用纪律

1. ★★★ **「这个写操作没有角色门」的第一嫌疑是「它根本不可达」。**
   本轮初始怀疑的 `ProvidersView.saveCredential` 缺门，追调用链后是
   **2026-06-12 起就断了线的死代码** ⇒ 改门改不到它。
   ⇒ **判别动作**：看到「前端调了一个后端会 403 的写 API」时，
   **先证明那个调用点可达**（`grep` 调用点 + 追到模板按钮 + 看渲染条件），
   再谈门。**不可达 ⇒ 不是缺陷，是死代码**（清理与否另议）。
2. ★★★ **旧判据可能正在钉死缺陷。**
   本轮发现一条既有用例断言的是**那句硬编码中文**——它**只因缺陷而绿**。
   ⇒ 判别动作：看到一条判据断言了**一段具体的界面文案**，
   先问「这段文案是**代码算出来的**、还是**源码里写死的字面量**？」
   若是后者 ⇒ 那条判据**不测任何东西**，且会把缺陷钉住。
   ⇒ 正确写法是断言「文案 = i18n 解析结果」或「切语言后文案跟着变」。
3. ★★★ **三条各管各的门，合起来也可能有缝：「键在，但值是英文/是硬编码」。**
   `parity` 只比键集合、`keys_referenced` 只查键存在、中文棘轮**排除 locales/**
   ⇒ 「键在但值没本地化」正好落在缝隙里。
   ⇒ 补门时先问「**这类缺陷现在被哪条门管着？**」答不上来就是有缝。
4. ★★ **审计记录里的数字要与表格自洽。**
   「漏了 5 个面」配一张只有 3 行标 ❌ 的表 ⇒ 下一个人会照着错的数字去查。
   ⇒ 写结论时让**表与标题来自同一份事实**（先数表，再写标题）。
5. ★★ **数量巧合最危险**：「上一轮点名 3 个」与「缺门 3 个」数目相同但集合不同
   ⇒ 审计记录里要把**集合**写清楚，不能只写数目。

## 十、遗留风险（未修，如实登记）

| # | 项 | 级别 | 说明 |
|---|---|---|---|
| 1 | **`ar-SA` 的 `ops` 命名空间大半未翻译** | 中 | `ar-SA/ops.ts` 564 行里仅 14 行含阿拉伯字母；本轮只补了自己新增的那一条。⇒ **全量扫描「值≠英文」必然是红的**，登记册门是逐条收敛，不是全量达标 |
| 2 | `NodeDetailOtherModelsPanel.vue:197` 仍有硬编码中文提示「仅 default 租户可探活或上下线；当前只读。」 | 低 | 既有债（本轮未碰该文件）；与本轮修的那支是**同一族的两个面**，措辞不同故未合并 |
| 3 | `web/src/views/CredentialHeatmapView.vue` 的 6 个写按钮**标签**仍是硬编码中文 | 低 | 既有债（本轮只加了 `:disabled` 门，未动标签）；该文件 CJK runs = 127 |
| 4 | 硬编码中文棘轮 `src/i18n/hardcoded_cjk.baseline.test.ts` **仍红** | ⛔ 既有红门 | HEAD 5755 > baseline 5529；**2026-10-05 之后其它会话累积**，本轮净 −2（5755 → 5753），只保证自己不减 |
| 5 | `web/src/config/navCoverage.test.ts` 6 条孤儿路由 | ⛔ 既有红门 | 本轮未改 `web/src/router.ts` |
| 6 | `ProvidersView.vue` 一整族死代码（`openManageCred` / `saveCredential` / `delCred` / …） | 低 | §六 已定性；清理与否属主决定，本轮不动 |
| 7 | `local-gitea` 镜像的**定位**仍无属主声明 | 低 | 「误推会出事」已不需介入（上一轮已证其 main 是 origin/main 的严格祖先） |
| 8 | 未跑全仓 `go test ./...` | 低 | 本轮**不碰任何 Go 代码**；其余包回归由 CI 承担 |
