# 简易模式 v2 · 实现规划与落地记录（两套前端体系并存）

> 日期：2026-11-17 ｜ 会话：`mvs_63c95713f83a43deaf4269a0a582d785`
> 上游材料：设计稿 [`simple-mode-concept-v2.html`](./simple-mode-concept-v2.html)、
> 对比分析 [`simple-mode-v2-magpie-comparison.md`](./simple-mode-v2-magpie-comparison.md)、
> v1 三稿（`simple-expert-mode-design.html` / `simple-mode-ui-mockup.html` / `simple-mode-concept.html`）
> 本仓基线：本轮开工时 HEAD `14d165a7f`；**并行车会话随后提交到 `f2ed45ef8`**（其改动不属于本轮，提交前必须逐文件区分归属）。
> 本轮改动**未提交、未部署**。

---

## 〇、这一轮做成了什么（一句话）

**专家模式一行未改，简易模式作为第二套前端体系并存；两者共用同一批后端接口与同一张表，零迁移、零新表。**
模式是**界面投影**：切过去只改导航与首页渲染，数据、接口语义、设置全部不动。

**★ L2 落点订正（2026-11-17 02:08 取证）**：设计稿写的「接入清单行内可改模型」在本仓**不成立** —— `applications.allowed_models_json` 是**从未被读写过的死列**（`grep` 零命中），`api_keys.default_client_profile` 是路由画像名不是模型，`PATCH /api/keys/{id}` 只收 `profile/owner/remark/alias` ⇒ **没有任何 per-key 模型权限写面**。读面齐全（per-key 用量、per-provider 用量、provider 的 `model_offers`、完整目录），per-provider 的写面也真实存在（`PATCH /api/providers/{id}/models/{offerId}/state`，路由会消费）。**L2 实际落点改为供应商页**（三段选择器 + 启停供应商的 model_offers），密钥行只读当前模型（从 per-key 用量取最近用过的模型名，无写操作）。

---

## 一、四条架构决策（决定了后面所有代码形状）

### D1 · 同一路径，按模式渲染不同组件（包装组件，不是两条路由）

```
/providers ──► ProvidersModeView ─┬─ simple ─► SimpleProvidersView
                                  └─ expert ─► ProvidersView（现有一行未改）
/dashboard ─► HomeView ───────────┬─ simple ─► SimpleAccessListView（接入清单）
                                  └─ expert ─► DashboardView（现有一行未改）
```

**为什么不用两条路由**：两条路由会分裂收藏链接与深链，也会让「切模式后地址变了」变成用户的困惑点。
包装组件让**路径不变、数据不变**，只有页面形态变 ⇒ 切模式零心智负担。

### D2 · 简易导航是**派生**出来的，不是另写一份

`web/src/config/simpleNav.ts` 从 `web/src/config/appNav.ts` 派生，规则只有三条：

| 落点 | 规则 |
|---|---|
| 顶栏直链 | 路径命中 `SIMPLE_DIRECT_PATHS` |
| 组内下拉 | 路径命中 `SIMPLE_GROUP_PATHS[专家面分组 id]` |
| 高级折叠 | **其余全部** |

⇒ 「专家面 63 条一条不删」不是承诺，而是**结构上成立**：任何一条入口只有三个去处，没有第四个。
★ 派生而不是手写，是因为手写清单会漂移（v1 稿写的是「61 个入口」，真源早已是 63）。

### D3 · 模式解析口径只有一处（`web/src/composables/useWorkMode.ts`）

```
1. 本机选择（localStorage `llmgw_work_mode`）—— 用户自己切的，只影响本机
2. 平台默认（settings 体系 `gateway.mode`）—— super_admin 在设置页改，影响所有人
3. 兜底 = expert（= 现状）
```

★ **本机选择优先**：平台设置是 ScopePlatform，一个管理员改了会立刻改变所有人的界面；
而「我想先用简易模式看看」是个人偏好，不该由平台替他决定。
★ **默认 expert**：升级后既有用户的界面一行不变。

### D4 · 数据后台一致（零迁移、零新表、同一批 API）

| 面 | 简易模式用的接口 | 专家面用的接口 |
|---|---|---|
| 接入清单 | `GET /api/keys` | 同 |
| 供应商 | `GET /api/providers`、`GET /api/catalog`、`POST /api/providers`、`POST /api/providers/{id}/credentials` | 同 |
| 模式开关 | `GET /api/admin/settings/gateway.mode` | 同（设置页） |

⇒ 简易模式**没有**自己的后端、自己的表、自己的统计口径。
★ 诚实边界：`/api/keys` 只给**累计**请求数与累计花费，**没有**按密钥的当日统计 ⇒
简易模式的「累计请求」列是累计口径，并在页面上写明（见 §四 遗留 L4）。

---

## 二、这一轮落地的文件清单

### 后端（1 改 1 增，零迁移）

| 文件 | 改动 | 验收 |
|---|---|---|
| `settings/spec_gateway.go` | 注册 `gateway.mode`（TypeString / ScopePlatform / CategoryGeneral / 默认 `expert` / Options `[simple, expert]` / HotReload / DangerLevel < Dangerous） | `settings/spec_gateway_mode_test.go` 四条钉测：注册形态、取值域、命名纪律、在平台注册表内 |
| `settings/spec_gateway_mode_test.go` | 新增：4 条钉测 | `go test ./settings/ -run GatewayMode` 绿 |

★ 命名纪律：`security.mode` 已占用「模式」并绑定 observe/enforce 语义 ⇒ 这里必须带 `gateway.` 命名空间，
界面文案叫「工作模式」，与「安全检测模式」并列（v1 设计稿已记这条）。
★ **平台不校验取值域**（TypeString + Options 与 `security.mode` 同款）⇒ **读侧必须白名单降级**：
不是 `'simple'` 一律当 `'expert'`（`coerceWorkMode()`，前端有钉测；后端侧在
`settings/spec_gateway_mode_test.go` 里把这条跨端约定写死，防止后人误以为平台会拦）。

### 前端（新增 9 / 改造 4）

| 文件 | 类型 | 作用 |
|---|---|---|
| `web/src/composables/useWorkMode.ts` | 新增 | 模式状态层：D3 的唯一实现 + 白名单降级 + 本机/平台两个来源 |
| `web/src/composables/useWorkMode.test.ts` | 新增 | 9 条钉测：白名单降级、默认 expert、切换只写本机、脏值降级、平台失败不阻塞 |
| `web/src/config/simpleNav.ts` | 新增 | D2 的派生规则 + 落点桶 |
| `web/src/config/simpleNav.test.ts` | 新增 | 4 条钉测：结构不变式、**与台账生成器逐 path 对拍**、高级组不重名、未知路径不丢 |
| `web/src/components/shell/WorkModeSwitch.vue` | 新增 | 顶栏模式切换器（只写本机选择） |
| `web/src/views/SimpleAccessListView.vue` | 新增 | 简易首页：接入清单 + 两字段签发 |
| `web/src/views/SimpleProvidersView.vue` | 新增 | 简易供应商页：两字段接入 + 关停/零流量折叠 + 遮蔽账号 |
| `web/src/views/ProvidersModeView.vue` | 新增 | D1 的包装组件 |
| `web/src/locales/{8 语种}/simpleMode.ts` | 新增 | 简易模式文案（39 键 × 8 语种，全部真翻译） |
| `web/src/locales/{8 语种}/nav.ts` | 改 | 补 `nav.group.advanced`、`nav.item.accessList` |
| `web/src/locales/{8 语种}/index.ts` | 改 | 注册 `simpleMode` 模块 |
| `web/src/composables/useAppNav.ts` | 改 | 模式分流：expert 走原逻辑，simple 走派生 |
| `web/src/components/shell/AppTopbar.vue` | 改 | 挂载切换器 |
| `web/src/views/HomeView.vue` | 改 | 登录态下按模式渲染不同首页 |
| `web/src/router.ts` | 改 | `/providers` 指向包装组件 |

### 工具（1 改）

| 文件 | 改动 |
|---|---|
| `scripts/audit/gen-simple-mode-ledger.mjs` | `--json` 增加 `bucket` 与 `buckets`；分组键从「中文名」改为「专家面分组 id」（与前端同名，才能逐 path 对拍） |

---

## 三、这一轮跑过的门（实测，不是承诺）

| 门 | 结果 |
|---|---|
| `go test ./settings/ -run GatewayMode` | 绿（4 条钉测） |
| `go test ./settings/`（整包） | 绿（顺带修了 `spec_gateway_test.go` 里「GatewaySpecs() 恰好 1 个」这条会逼后人改断言的写法） |
| `go build ./...` / `go vet ./settings/` | 绿 |
| `gofmt -l settings/` | 经手文件零漂移 |
| `npx vue-tsc --noEmit` | 绿（零错误） |
| `npx vitest run src/config/simpleNav.test.ts src/composables/useWorkMode.test.ts` | 13 条全绿 |
| `npx vitest run`（**全量**） | **315 文件 passed / 1 skipped；3250 用例 passed / 2 skipped**（139s） |
| `npx vitest run src/config/navCounts.test.ts src/style.token-resolution.test.ts` | 81 条全绿（台账口径门 + CSS 令牌门） |
| `node scripts/i18n-audit.mjs` | PASS（0 missing keys） |
| `node scripts/i18n-cjk-count.mjs` | 4478 ≤ baseline 4480（硬编码中文**未增加**；新增文件贡献 0） |
| `npx vitest run src/i18n/keys_referenced.test.ts hardcodedCjk.baseline.test.ts localeKeyParity.test.ts` | 绿（14 条） |
| `npx vitest run src/i18n/localeValueQuality.test.ts` | 绿（12 条；ja-JP / zh-TW 的「与源语言相同」计数未上升） |

---

## 四、遗留（本轮**未**落地，按优先级排）

| # | 遗留 | 为什么这轮没做 | 下一步 |
|---|---|---|---|
| **L1** | **运行时裁剪**（三档功能清单的「隐藏」档：8 套探针矩阵 / URSM v2 / 多层队列调度 / 插件运行时 / 93 个 worker 等） | 这是**后端启动期装配**，不是界面投影；动了会改变生产行为，必须单独评审 + 钉测 | 独立立项：按 v1 稿的「运行时裁剪」表逐条落装配点，每条一条钉测 |
| **L2** | **模型选择器三段 + 供应商 model_offers 启停**（落点：供应商页，不是密钥行） | 取证定案：per-key 模型权限写面在本仓不存在（`applications.allowed_models_json` 是死列）⇒ 设计稿「行内可改模型」无写侧支撑 | **供应商页**：`SimpleProvidersView` 加三段选择器（该供应商当前提供的 / 本区间有请求的 / 完整目录）+ 启停按钮（写 `PATCH /api/providers/{id}/models/{offerId}/state`）；**密钥行**：`SimpleAccessListView` 加「当前模型」只读列（从 per-key 用量取最近用过的模型名，零写操作） |
| **L3** | **`/keys` 页仍是专家面表单**（两字段签发目前只在接入清单页内提供） | `/keys` 有筛选、批量、审批、限额等一堆交互，拆两套成本高 | 简易模式下 `/keys` 改为「签发 + 列表两列」的简化视图 |
| **L4** | **按密钥的当日统计口径** | 后端没有该聚合；不做就得在页面写「累计」并解释 | 后端加一个按 key 的当日聚合（复用 `api_key_model_cost` 的 bucket 表） |
| **L5** | **设置页的模式选择器是文本框**（平台默认 `gateway.mode` 在设置页按字符串渲染） | 设置页按 `spec.type` 分支渲染，没有 enum 分支；改设置页要动通用组件 | 给设置页补 enum 下拉（`spec.options` 已在 payload 里），`gateway.mode` 用它 |
| **L6** | **降级标记**（三档清单里「降级」档的可见标记） | 依赖 L1（没有裁剪就没有降级事实） | 随 L1 一起落 |
| **L7** | **移动端底栏 / 汉堡抽屉的简易形态** | 本轮只改了桌面顶栏；`AppBottomNav` / `AppNavDrawer` 共用 `useAppNav`，导航已跟随，但布局未做简易化 | 下一轮做 compact 档的简易形态 |

---

## 五、复查方式（都可重复执行）

```bash
# ① 模式解析口径（白名单降级 / 本机优先 / 平台失败不阻塞）
cd web && npx vitest run src/composables/useWorkMode.test.ts

# ② 导航结构不变式 + 与台账生成器逐 path 对拍
cd web && npx vitest run src/config/simpleNav.test.ts

# ③ 硬编码中文棘轮（新增文件必须是 0）
cd web && node scripts/i18n-cjk-count.mjs

# ④ 台账（专家面 63 条 → 简易模式落点）
node scripts/audit/gen-simple-mode-ledger.mjs
node scripts/audit/gen-simple-mode-ledger.mjs --check docs/03-design/02-feature-design/simple-mode-concept-v2.html

# ⑤ 后端模式项
go test ./settings/ -run GatewayMode -count=1
```

---

## 六、证据索引

| 主张 | 位置 |
|---|---|
| 专家面 63 条目 / 62 落地路径 / 8 静态组 + 1 插件合成组 | `web/src/config/appNav.ts`（组定义 `:133,163,191,205,229,256,270,284`；合成组 `:430`；顶层 3 项 `:66-71`） |
| 可见性唯一判定处 | `web/src/config/appNav.ts:299`（`canShowNavItem`） |
| 导航数据来源（分组/插件/运维中心合并） | `web/src/composables/useAppNav.ts` |
| 模式设置项 | `settings/spec_gateway.go`（`gateway.mode`） |
| 模式解析口径 | `web/src/composables/useWorkMode.ts` |
| 落点派生规则 | `web/src/config/simpleNav.ts` |
| 落点映射的第二份独立实现（对拍用） | `scripts/audit/gen-simple-mode-ledger.mjs` |
| 设计稿与对比分析 | `docs/03-design/02-feature-design/simple-mode-concept-v2.html`、`simple-mode-v2-magpie-comparison.md` |
| magpie 侧事实（本机检出 `@ca3e57f94`） | 见对比分析 §九 |
