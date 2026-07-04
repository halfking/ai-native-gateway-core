# i18n 优化方案 — llm-gateway-go

> 对比基准：`~/workspace/llm-gateway-go-2`（参考实现）  
> 当前项目：`~/workspace/official-deploy/services/llm-gateway-go`  
> 生成时间：2026-07-05

---

## 执行摘要

通过对比 `llm-gateway-go-2`（参考项目）与当前官方部署项目的前端架构，发现**当前项目的 i18n 覆盖率仅约 32%**，存在以下关键问题：

1. **68% 的源文件包含硬编码中文**（34,600+ 字符），未使用 `t()` 翻译
2. **首页、导航栏、登录组件完全硬编码中文**，无多语言支持
3. **locale 文件结构落后**：平面单文件 vs 参考项目的 44 模块分层架构
4. **无 i18n 质量门禁**：parity.test.ts 已损坏（引用不存在的文件结构）
5. **无翻译自动化工具**，手工维护 8 种语言成本极高

**预估影响**：当前部署的 184 环境对非中文用户完全不可用。

---

## 对比分析矩阵

| 维度 | llm-gateway-go-2（参考） | llm-gateway-go（当前） | 差距 |
|---|---|---|---|
| **i18n 框架** | vue-i18n 9.14.4 Composition API | vue-i18n 9.14.4 Composition API | ✅ 一致 |
| **支持语言数** | 8（en-US, zh-CN, zh-TW, ja-JP, de-DE, fr-FR, es-ES, ar-SA） | 8（en, zh-CN, zh-TW, ja, de, fr, es, ar） | ✅ 数量一致，代码规范略有差异 |
| **locale 文件架构** | **44 模块分层**（`locales/<locale>/{index.ts, common.ts, nav.ts, ...}`，每模块 20-150 行） | **8 个平面单文件**（`locales/<locale>.ts`，每文件 952 行） | ❌ 可维护性差距巨大 |
| **locale 消息总量** | ~2,800 个叶子键（44 命名空间） | ~110 个叶子键（9 命名空间） | ❌ **覆盖率仅 3.9%** |
| **首页 i18n** | `LandingView.vue`：0 硬编码，100% `t()` | `LandingView.vue`：632 CJK 字符，0 `t()` | ❌ **完全硬编码** |
| **导航栏 i18n** | `appNav.ts`：100% `labelKey` + 中文 `label` fallback | `appNav.ts`：100% 硬编码中文 `label`，无 `labelKey` | ❌ **完全硬编码** |
| **语言切换器** | `LanguageSwitcher.vue`：134 行，完整 a11y | `LanguageSelector.vue`：88 行，`aria-label` 硬编码英文 | ⚠️ 功能基本一致，a11y 有缺陷 |
| **质量门禁** | `parity.test.ts`：强制所有 locale ⊇ zh-CN 键集 | `parity.test.ts`：**已损坏**（引用不存在路径） | ❌ **无 CI 保护** |
| **翻译工具** | 无（手工维护） | 无（手工维护） | ⚠️ 两者都缺 |
| **URL locale 路由** | 无（localStorage 隐式） | 无（localStorage 隐式） | ✅ 一致（但都不符合 SEO 最佳实践） |
| **HTML lang/dir** | 运行时动态设置 | 运行时动态设置 | ✅ 一致 |
| **RTL 支持** | `ar-SA` + `postcss-rtlcss` | `ar` + 无 postcss-rtlcss | ⚠️ RTL 支持不完整 |
| **默认语言** | `en-US` | `zh-CN` | ⚠️ 目标用户定位不同 |
| **硬编码比例** | ~5%（仅 MaaS/tenant 模块未迁移） | **~68%**（135/197 文件包含 CJK） | ❌ **灾难性差距** |

---

## 关键问题清单（按优先级）

### P0 — 阻塞国际化部署

| # | 问题 | 证据 | 影响 |
|---|---|---|---|
| 1 | **首页完全硬编码中文** | `views/LandingView.vue`：632 CJK 字符，0 `t()` 调用 | 非中文用户看到乱码首页 |
| 2 | **导航栏完全硬编码中文** | `config/appNav.ts`：162 CJK 字符，所有 `label` 字段硬编码 | 非中文用户无法导航 |
| 3 | **登录/密码修改组件硬编码** | `LoginModal.vue`（64 CJK），`ChangePasswordDialog.vue`（166 CJK） | 登录流程对非中文用户不可用 |
| 4 | **65+ 个视图完全硬编码** | `RequestLogsView`（1878 CJK），`CredentialMonitorView`（1816 CJK），`ModelsView`（1043 CJK）等 | 核心功能页面无法国际化 |

### P1 — 架构技术债

| # | 问题 | 证据 | 影响 |
|---|---|---|---|
| 5 | **locale 文件单体过大** | 每个 locale 文件 952 行，无模块拆分 | 合并冲突频繁、难以并行开发 |
| 6 | **parity.test.ts 已损坏** | 引用 `locales/<locale>/index.ts`（不存在） | CI 无法检测 i18n 完整性回归 |
| 7 | **无翻译自动化** | `scripts/` 下无 `i18n*` / `translate*` 工具 | 每次新增字符串需手工翻译 8 份 |
| 8 | **缺少 postcss-rtlcss** | `package.json` 中无此依赖 | `ar` RTL 布局可能错乱 |

### P2 — 体验优化

| # | 问题 | 证据 | 影响 |
|---|---|---|---|
| 9 | **URL 不包含 locale** | 路由无 `:locale` 前缀 | 用户无法分享特定语言链接 |
| 10 | **默认语言 zh-CN** | `i18n.ts` line 13 | 国际用户首次访问看到中文 |
| 11 | **LanguageSelector aria-label 硬编码** | `'Switch language'` 未使用 `t()` | 屏幕阅读器固定英文提示 |

---

## 优化方案（三阶段）

### 阶段 1：紧急止血（1-2 天，立即可部署）

**目标**：让 184 环境对英文用户基本可用。

#### 1.1 首页 i18n 迁移
- [ ] 将 `LandingView.vue` 的 `features[]` / `advantages[]` 提取到 `locales/zh-CN.ts` 的 `landing` 命名空间
- [ ] 在 `locales/en.ts` 添加对应翻译（可先用机器翻译）
- [ ] 所有硬编码字符串替换为 `t('landing.features.smartRouting.title')` 等
- [ ] 将 `ServiceLandingPage.vue` 的默认 props（`advantagesTitle`, `footerText` 等）改为 `t()` 调用

**预估工作量**：4 小时  
**文件改动**：2 个 `.vue` + 8 个 locale 文件

#### 1.2 导航栏 i18n 迁移
- [ ] `config/appNav.ts` 的每个 `NavItem` 添加 `labelKey` 字段（如 `'nav.item.tenantModels'`）
- [ ] 保留现有 `label` 作为 fallback（兼容未翻译 locale）
- [ ] `App.vue` 渲染时优先使用 `t(item.labelKey)` || `item.label`
- [ ] 在所有 locale 文件添加 `nav.item.*` / `nav.group.*` 键

**预估工作量**：3 小时  
**文件改动**：`appNav.ts` + `App.vue` + 8 个 locale 文件

#### 1.3 登录流程 i18n
- [ ] `LoginModal.vue` 所有字符串改为 `t('login.*')`
- [ ] `ChangePasswordDialog.vue` 改为 `t('password.*')`
- [ ] 在 locale 文件补齐对应键（参考 llm-gateway-go-2 的 `locales/<locale>/login.ts`）

**预估工作量**：2 小时  
**文件改动**：2 个组件 + 8 个 locale 文件

#### 1.4 修复 parity.test.ts
- [ ] 改为读取平面 `locales/<locale>.ts` 结构（而非 `locales/<locale>/index.ts`）
- [ ] 修正硬编码的 locale 代码列表（`ar-SA` → `ar` 等）
- [ ] 加入 pre-commit hook 或 CI

**预估工作量**：1 小时  
**文件改动**：`i18n/parity.test.ts` + `.github/workflows/*.yml`（可选）

**阶段 1 总计**：10 小时，可产出可部署版本。

---

### 阶段 2：架构迁移（3-5 天，需要并行开发支持）

**目标**：迁移到 44 模块分层架构，覆盖所有核心页面。

#### 2.1 重构 locale 文件结构
```
locales/
├── ar-SA/               # 改为 BCP-47 完整代码（与参考项目一致）
│   ├── index.ts         # 聚合 44 个模块
│   ├── common.ts        # 通用词汇
│   ├── nav.ts           # 导航相关
│   ├── login.ts         # 登录模块
│   ├── dashboard.ts     # 仪表盘
│   ├── providers.ts     # 供应商管理
│   ├── models.ts        # 模型目录
│   ├── requests.ts      # 请求日志
│   └── ...              # 共 44 个模块（见参考项目）
├── zh-CN/
├── en-US/
└── ...（8 个 locale）
```

**迁移策略**：
1. 从参考项目 `llm-gateway-go-2` **直接复制** `locales/` 目录结构
2. 逐文件对比差异（当前项目的 110 键 vs 参考项目的 2800 键），合并当前项目独有的键
3. 更新 `i18n.ts` 的导入逻辑（从 `./locales/zh-CN` 改为 `./locales/zh-CN/index`）
4. 实施懒加载优化（参考项目已有 `LAZY_LOADERS`）

**预估工作量**：16 小时（2 天）  
**风险**：需要冻结其他 i18n PR，避免合并冲突

#### 2.2 批量迁移核心视图
按优先级迁移以下视图（从硬编码改为 `t()`）：

| 优先级 | 视图 | CJK 字符数 | 预估耗时 |
|---|---|---|---|
| P0 | `RequestLogsView.vue` | 1878 | 3h |
| P0 | `CredentialMonitorView.vue` | 1816 | 3h |
| P0 | `ModelsView.vue` | 1043 | 2h |
| P0 | `FreePoolView.vue` | 1002 | 2h |
| P1 | `KeysView.vue` | 804 | 2h |
| P1 | `SettingsView.vue` | 712 | 2h |
| P1 | `DashboardView.vue` | 342 | 1.5h |
| P1 | `data-lifecycle/StorageConfig.vue` | 778 | 2h |
| P2 | `ForbiddenView.vue` | 41 | 0.5h |
| P2 | `LanguageSelector.vue` aria-label | 30 | 0.5h |

**总计**：18.5 小时

#### 2.3 添加翻译自动化脚本
参考 llm-gateway-go-2 的实现（如果存在）或自建：

```bash
# scripts/i18n-extract.js
# 扫描 .vue 文件中的硬编码 CJK，生成待翻译清单

# scripts/i18n-translate.js
# 调用 LLM API（如 llm.kxpms.cn）批量翻译
# 输入：zh-CN.ts 新增的键
# 输出：en-US.ts / ja-JP.ts / ... 自动补全对应翻译
```

**预估工作量**：8 小时  
**依赖**：需要访问内部 LLM Gateway API

**阶段 2 总计**：42.5 小时（5.3 天）

---

### 阶段 3：完整覆盖（1-2 周，可增量进行）

**目标**：达到参考项目的 i18n 质量（95%+ 覆盖率）。

#### 3.1 长尾视图迁移
迁移剩余 50+ 个包含硬编码的视图（每个 0.5-2 小时不等）

**预估工作量**：60 小时（7.5 天）

#### 3.2 API 响应消息 i18n
- [ ] 后端 `i18n/` 包已支持 8 种语言的 JSON 文件
- [ ] 前端 `useApiError.ts` 需要与后端错误码对接
- [ ] 统一错误提示的 locale 选择逻辑（前端 localStorage 同步到后端 `X-Lang` header）

**预估工作量**：4 小时

#### 3.3 SEO 优化（可选）
- [ ] 添加 `:locale(en|zh-CN|...)` 路由前缀
- [ ] `<link rel="alternate" hreflang="en" href="/en/..." />` meta 标签
- [ ] sitemap.xml 多语言支持

**预估工作量**：12 小时  
**收益**：搜索引擎多语言索引、可分享的语言链接

#### 3.4 RTL 完善
- [ ] 添加 `postcss-rtlcss` 到 `package.json`
- [ ] 配置 Vite 构建生成 `.rtl.css`
- [ ] 测试 `ar` locale 下的布局（表格、表单、导航）

**预估工作量**：6 小时

**阶段 3 总计**：82 小时（10.3 天）

---

## 技术方案细节

### 方案 A：直接复制参考项目的 locale 文件（推荐）

**步骤**：
1. `cp -r ~/workspace/llm-gateway-go-2/web/src/i18n/locales ~/workspace/official-deploy/services/llm-gateway-go/web/src/`
2. 修改 `web/src/i18n.ts` 的导入路径（从平面 `./locales/zh-CN` 改为 `./locales/zh-CN/index`）
3. 对比当前项目的 `locales/zh-CN.ts`（952 行）与参考项目的 `locales/zh-CN/index.ts`（聚合 44 模块），合并差异
4. 全局搜索 `t('key')` 调用，确保所有键在新结构中存在

**优点**：
- 一次性获得 2800+ 键的完整翻译
- 继承参考项目的命名规范和模块划分
- 节省 90% 的翻译工作量

**风险**：
- 参考项目可能包含当前项目不需要的键（如 MaaS 计费相关）
- 需要人工 review 差异，避免覆盖当前项目的定制内容

### 方案 B：渐进式手工迁移（不推荐）

保持现有平面结构，逐页面提取硬编码到 locale 文件。

**优点**：
- 风险可控
- 不改变现有架构

**缺点**：
- 工作量是方案 A 的 3-5 倍
- 最终仍需面对 952 行单文件的维护问题
- 无法复用参考项目的翻译成果

**建议**：仅在以下情况使用方案 B：
- 当前项目与参考项目的业务差异 >50%
- 团队明确拒绝引入 44 模块结构

---

## 风险与缓解

| 风险 | 影响 | 缓解措施 |
|---|---|---|
| **方案 A 覆盖现有定制内容** | 高 | 迁移前备份当前 `locales/`；人工 review 差异 |
| **翻译质量问题（机器翻译）** | 中 | 仅用于 P2 优先级页面；P0 页面人工校验 |
| **阶段 1 后测试不充分** | 高 | 强制在 184 环境实测 8 种语言切换 |
| **并行开发冲突** | 中 | 阶段 2 期间锁定 `locales/` 目录的其他 PR |
| **parity test 修复后 CI 失败** | 低 | 修复时同步补齐所有 locale 的缺失键 |

---

## 验收标准

### 阶段 1 验收（184 环境）
- [ ] 英文用户访问首页，所有文案显示英文（无中文字符）
- [ ] 切换到日文/德文/法文，首页/导航栏正确显示对应语言
- [ ] 登录流程（输入框 placeholder、按钮、错误提示）支持 8 种语言
- [ ] `parity.test.ts` 通过（`npm run test`）
- [ ] 浏览器控制台无 `[vue-i18n] Not found 'xxx'` 警告

### 阶段 2 验收
- [ ] `locales/` 目录结构与参考项目一致（44 模块 × 8 语言）
- [ ] 核心页面（请求日志、凭据监控、模型目录）英文用户可正常使用
- [ ] `npm run build` 成功，dist/ 产物包含懒加载的 locale chunks
- [ ] lighthouse 审计：`lang` 属性正确、无硬编码文本（通过 a11y 扫描）

### 阶段 3 验收
- [ ] 硬编码 CJK 字符 <1000（从当前 34,600 降低 97%）
- [ ] 所有 `.vue` 文件的 `<template>` 区域无裸字符串（除品牌名/版权）
- [ ] `ar` 语言下，布局从右到左、表格对齐正确
- [ ] SEO 审计：`/en/providers` 返回英文内容，`<html lang="en">`

---

## 资源需求

| 阶段 | 人力 | 时间 | 依赖 |
|---|---|---|---|
| 阶段 1 | 1 前端工程师 | 2 天 | 无 |
| 阶段 2 | 2 前端工程师（并行） | 5 天 | LLM API（翻译脚本） |
| 阶段 3 | 1-2 工程师 | 10 天 | QA 支持（8 语言测试） |

**总计**：17 个工作日（~3.5 周）

---

## 附录 A：快速对比表

| 指标 | llm-gateway-go-2 | llm-gateway-go | 目标（阶段 1） | 目标（阶段 3） |
|---|---|---|---|---|
| i18n 覆盖率 | ~95% | ~32% | ~60% | ~95% |
| 硬编码 CJK 字符数 | ~1,800 | ~34,600 | ~20,000 | <1,000 |
| locale 键总数 | ~2,800 | ~110 | ~500 | ~2,800 |
| 模块化程度 | 44 模块 | 单文件 | 单文件（过渡） | 44 模块 |
| CI 质量门禁 | ✅ | ❌ | ✅ | ✅ |

---

## 附录 B：参考项目清单

| 文件/目录 | 用途 | 复制优先级 |
|---|---|---|
| `llm-gateway-go-2/web/src/i18n/locales/` | 44 模块 × 8 语言的完整翻译 | **P0** |
| `llm-gateway-go-2/web/src/i18n/constants.ts` | `SUPPORTED_LOCALES`、RTL 定义 | P1 |
| `llm-gateway-go-2/web/src/i18n/parity.test.ts` | 质量门禁测试 | P1 |
| `llm-gateway-go-2/web/src/i18n/useLocale.ts` | 响应式 locale composable | P2 |
| `llm-gateway-go-2/web/src/config/appNav.ts` | `labelKey` + `label` 双字段模式 | P0 |
| `llm-gateway-go-2/web/src/components/LanguageSwitcher.vue` | a11y 完整的语言切换器 | P1 |
| `llm-gateway-go-2/web/postcss.config.cjs` | `postcss-rtlcss` 配置 | P2 |

---

## 下一步行动

**请确认以下问题后继续**：

1. **优先级确认**：是否同意先执行阶段 1（2 天快速止血）？
2. **方案选择**：选择方案 A（复制参考项目）还是方案 B（手工迁移）？
3. **资源分配**：能否投入 2 名前端工程师并行执行阶段 2？
4. **翻译质量要求**：P0 页面是否需要母语者 review，还是接受机器翻译？
5. **部署时间窗口**：184 环境何时可接受部署（需要停机维护吗）？

**确认后我将立即开始阶段 1 的代码实施。**
