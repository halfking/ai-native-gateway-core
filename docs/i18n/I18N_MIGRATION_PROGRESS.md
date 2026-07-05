# i18n 迁移进度报告

> 生成时间：2026-07-05  
> 项目：llm-gateway-go (official-deploy)  
> 目标：从 32% 覆盖率提升到 95%+

---

## ✅ 已完成工作（阶段 1 部分 + 阶段 2.1）

### 1. 架构升级 — 从平面文件到 44 模块分层

**完成时间**: 2026-07-05 01:36

#### 变更内容
- ✅ 从参考项目 `llm-gateway-go-2` 复制完整的 44 模块 locale 结构
- ✅ 删除旧的 8 个平面 locale 文件（已备份到 `locales.backup.20260705-013657/`）
- ✅ 新结构：`locales/{ar-SA,de-DE,en-US,es-ES,fr-FR,ja-JP,zh-CN,zh-TW}/` 每个目录 46 个模块文件

#### 文件清单
```
locales/
├── ar-SA/     (46 模块 × 阿拉伯语)
├── de-DE/     (46 模块 × 德语)
├── en-US/     (46 模块 × 英语)
├── es-ES/     (46 模块 × 西班牙语)
├── fr-FR/     (46 模块 × 法语)
├── ja-JP/     (46 模块 × 日语)
├── zh-CN/     (46 模块 × 简体中文) ← 已更新
├── zh-TW/     (46 模块 × 繁体中文)
└── locales.backup.20260705-013657/ (旧版备份)

每个 locale 目录包含：
- index.ts (聚合导出)
- common.ts, nav.ts, login.ts, app.ts, errors.ts
- landing.ts ← 已更新匹配 LandingView.vue
- dashboard.ts, providers.ts, models.ts, requests.ts
- 以及其他 37 个模块...
```

### 2. i18n 核心逻辑重构

**文件**: `web/src/i18n.ts` (127 行，完全重写)

#### 新增功能
- ✅ **懒加载**: zh-CN + en 静态打包，其他 6 种语言按需动态加载
- ✅ **Locale 代码映射**: 参考项目 `en-US` → 当前项目 `en`（向后兼容 localStorage）
- ✅ **HTML lang/dir 自动设置**: 支持 RTL（`ar` 语言）
- ✅ **统一 setLocale()**: 一次调用更新 vue-i18n + localStorage + `<html>` 属性
- ✅ **浏览器语言检测**: `navigator.language` → locale 匹配

#### 关键改进
```typescript
// 旧代码（平面结构）:
import zhCN from './locales/zh-CN'  // 952 行巨型文件

// 新代码（模块化 + 懒加载）:
import zhCN from './locales/zh-CN'  // index.ts 聚合 46 模块
const LAZY_LOADERS = {
  'ja': () => import('./locales/ja-JP'),  // 按需加载
  // ...
}
```

### 3. 首页完全 i18n 化

**文件**: 
- `web/src/views/LandingView.vue` (212 行 → 100% i18n)
- `web/src/locales/zh-CN/landing.ts` (完全重写，106 行)
- `web/src/locales/en-US/landing.ts` (完全重写，105 行)

#### 迁移成果
| 指标 | 迁移前 | 迁移后 |
|---|---|---|
| 硬编码 CJK 字符 | 632 | **0** |
| `t()` 调用次数 | 0 | **47** |
| 支持语言数 | 1 (仅中文) | **8** (中文 + 英文已验证) |

#### 覆盖范围
- ✅ Hero 区域（kicker, title, subtitle, 6 个亮点）
- ✅ 8 个功能卡片（title + description + badge）
- ✅ 4 个优势卡片（icon + title + description）
- ✅ 路线图 4 个里程碑（phase + title + description）
- ✅ Footer 文本

#### 代码示例
```vue
<!-- 旧代码（硬编码） -->
<template>
  <ServiceLandingPage
    title="LLM Gateway — 面向全球市场的开源 AI 网关"
    :features="[
      { title: '智能路由与凭据池', description: '...' },
      // ...
    ]"
  />
</template>

<!-- 新代码（i18n） -->
<script setup>
const { t } = useI18n()
const features = computed(() => [
  { 
    title: t('landing.features.smartRouting.title'),
    description: t('landing.features.smartRouting.description')
  },
  // ...
])
</script>
<template>
  <ServiceLandingPage
    :title="t('landing.title')"
    :features="features"
  />
</template>
```

### 4. 构建验证通过

```bash
$ npm run build
✓ 778 modules transformed.
dist/index.html                   0.63 kB
dist/assets/index-BTUi-gG-.css   49.28 kB
dist/assets/index-BbtYM4c-.js   356.47 kB  ← 主入口（zh-CN + en 静态）
# ...（无错误）
```

---

## 🚧 未完成工作（剩余 80% 工作量）

### 阶段 1 剩余任务（紧急止血）

#### 1.2 导航栏 i18n（预估 3 小时）
**文件**: `web/src/config/appNav.ts` + `web/src/App.vue`

**当前状态**: 100% 硬编码中文
```typescript
// appNav.ts (现状)
export const NAV_PRIMARY_ITEMS = [
  { path: '/', label: '总览', icon: '📊', platformOps: true },
]
export const NAV_GROUPS = [
  { id: 'tenant-portal', label: '我的服务', items: [
    { path: '/tenant/models', label: '标准模型', icon: '🤖' },
    // ... 30+ 个硬编码中文导航项
  ]},
]
```

**需要做的**:
- [ ] 为每个 `NavItem` 添加 `labelKey` 字段（如 `'nav.item.tenantModels'`）
- [ ] 在 `locales/{zh-CN,en-US}/nav.ts` 添加所有导航键
- [ ] 修改 `App.vue` 渲染逻辑：`t(item.labelKey) || item.label`
- [ ] 其他 6 种语言翻译（可用 LLM 批量）

#### 1.3 登录流程 i18n（预估 2 小时）
**文件**: 
- `web/src/components/LoginModal.vue` (64 CJK 字符)
- `web/src/components/ChangePasswordDialog.vue` (166 CJK 字符)

**需要做的**:
- [ ] 所有硬编码字符串改为 `t('login.*')` / `t('password.*')`
- [ ] 在 `locales/zh-CN/login.ts` 补齐键（参考项目已有模板）
- [ ] 英文翻译 + 其他 6 种语言

#### 1.4 修复 parity.test.ts（预估 1 小时）
**文件**: `web/src/i18n/parity.test.ts`

**当前状态**: ❌ 已损坏（引用不存在的文件路径）

**需要做的**:
- [ ] 修改测试逻辑，读取新的模块化结构（`locales/<locale>/index.ts`）
- [ ] 更新硬编码的 locale 代码列表（`ar-SA` → `ar` 等）
- [ ] 验证所有 8 种 locale 的键集合是 zh-CN 的超集
- [ ] 加入 `npm test` 或 pre-commit hook

---

### 阶段 2 剩余任务（架构迁移）

#### 2.2 批量迁移 10 个核心视图（预估 18 小时）

| 优先级 | 视图 | CJK 字符数 | 预估耗时 | 状态 |
|---|---|---|---|---|
| P0 | `RequestLogsView.vue` | 1878 | 3h | ⏸️ 待做 |
| P0 | `CredentialMonitorView.vue` | 1816 | 3h | ⏸️ 待做 |
| P0 | `ModelsView.vue` | 1043 | 2h | ⏸️ 待做 |
| P0 | `FreePoolView.vue` | 1002 | 2h | ⏸️ 待做 |
| P1 | `KeysView.vue` | 804 | 2h | ⏸️ 待做 |
| P1 | `SettingsView.vue` | 712 | 2h | ⏸️ 待做 |
| P1 | `DashboardView.vue` | 342 | 1.5h | ⏸️ 待做 |
| P1 | `data-lifecycle/StorageConfig.vue` | 778 | 2h | ⏸️ 待做 |
| P2 | `ForbiddenView.vue` | 41 | 0.5h | ⏸️ 待做 |
| P2 | `LanguageSelector.vue` aria-label | 30 | 0.5h | ⏸️ 待做 |

**工作模式**: 每个视图的迁移流程
1. 读取视图文件，提取所有硬编码中文
2. 在 `locales/zh-CN/<module>.ts` 添加键值对
3. 替换视图中的硬编码为 `t('module.key')`
4. 机器翻译 → `locales/en-US/<module>.ts`
5. 测试构建 + 浏览器验证

#### 2.3 添加 LLM 翻译自动化脚本（预估 8 小时）

**目标**: 自动化翻译 zh-CN → 其他 7 种语言

**技术方案**:
```bash
# scripts/i18n-translate.js
# 1. 读取 locales/zh-CN/**/*.ts
# 2. 提取所有叶子键值对
# 3. 调用 __DOMAIN_2__ API 批量翻译
# 4. 写入 locales/{en-US,ja-JP,...}/**/*.ts
```

**依赖**: 
- 需要访问内部 LLM Gateway API
- 需要为每种语言准备翻译提示词模板

---

### 阶段 3 剩余任务（完整覆盖）

#### 3.1 长尾视图迁移（预估 60 小时）
- [ ] 剩余 50+ 个视图（每个 0.5-2 小时）
- [ ] 包含所有 `views/`, `components/`, `composables/` 中的硬编码

#### 3.2 API 响应消息 i18n（预估 4 小时）
- [ ] 前端 `useApiError.ts` 与后端 `i18n/` 包对接
- [ ] 统一错误提示的 locale 选择逻辑
- [ ] 前端 localStorage 同步到后端 `X-Lang` header

#### 3.3 SEO 优化（可选，预估 12 小时）
- [ ] 添加 `:locale(en|zh-CN|...)` 路由前缀
- [ ] `<link rel="alternate" hreflang>` meta 标签
- [ ] sitemap.xml 多语言支持

#### 3.4 RTL 完善（预估 6 小时）
- [ ] 添加 `postcss-rtlcss` 到 `package.json`
- [ ] 配置 Vite 构建生成 `.rtl.css`
- [ ] 测试 `ar` locale 下的布局

---

## 📊 当前覆盖率对比

| 指标 | 迁移前 | 当前 | 目标（阶段 3 完成） |
|---|---|---|---|
| i18n 覆盖率 | 32% | **~40%** | 95%+ |
| 硬编码 CJK 字符数 | 34,600 | **~33,000** | <1,000 |
| locale 键总数 | 110 | **~200** | ~2,800 |
| 模块化程度 | 单文件 | **44 模块** | 44 模块 |
| CI 质量门禁 | ❌ | ❌ | ✅ |
| 首页 i18n | 0% | **100%** | 100% |
| 导航栏 i18n | 0% | 0% | 100% |
| 核心视图 i18n | ~10% | ~10% | 100% |

**进度**: 约 **20%** 完成（基础架构 + 首页 = 5/25 任务）

---

## 🚀 快速继续方案

### 选项 A：人工完成剩余工作（推荐）
**时间**: 2-3 周全职工作

**步骤**:
1. 阅读本文档的"未完成工作"章节
2. 按优先级依次完成阶段 1.2 → 1.3 → 1.4
3. 使用以下命令测试每个阶段:
   ```bash
   cd ~/workspace/official-deploy/services/llm-gateway-go/web
   npm run build  # 构建验证
   npm run dev    # 本地测试
   ```
4. 完成阶段 1 后部署到 184，进行英文用户验收
5. 继续阶段 2、3

### 选项 B：AI Agent 辅助批量迁移
**时间**: 5-7 天

**步骤**:
1. 使用 `task` 工具启动 10 个并行 agent，每个负责 1 个核心视图
2. 每个 agent 执行:
   - 读取视图文件
   - 提取硬编码 → locale 文件
   - 替换为 `t()` 调用
   - 机器翻译英文版
3. 人工 review + 合并
4. 部署 + 测试

### 选项 C：暂停，等待排期（不推荐）
**风险**: 
- 当前代码处于"半迁移"状态
- 新增功能可能继续硬编码，增加技术债
- 184 环境对国际用户不可用

---

## 📦 交付物清单

### 已交付
- ✅ `I18N_OPTIMIZATION_PROPOSAL.md` — 完整优化方案（3 阶段）
- ✅ `I18N_MIGRATION_PROGRESS.md` — 本文档（进度报告）
- ✅ `web/src/locales/` — 44 模块 × 8 语言的完整 locale 结构
- ✅ `web/src/i18n.ts` — 重构后的 i18n 核心逻辑（懒加载 + RTL）
- ✅ `web/src/views/LandingView.vue` — 首页完全 i18n 化
- ✅ `web/src/locales/{zh-CN,en-US}/landing.ts` — 首页翻译（中英）
- ✅ 构建验证通过（`npm run build` 无错误）

### 待交付（完成剩余工作后）
- ⏸️ `web/src/config/appNav.ts` + `web/src/locales/*/nav.ts` — 导航栏 i18n
- ⏸️ `web/src/components/LoginModal.vue` + `locales/*/login.ts` — 登录流程 i18n
- ⏸️ `web/src/i18n/parity.test.ts` — 修复后的质量门禁
- ⏸️ 10 个核心视图的 i18n 迁移 + 对应 locale 文件
- ⏸️ `scripts/i18n-translate.js` — LLM 翻译自动化脚本
- ⏸️ 部署验证报告（184 环境 8 种语言切换截图）

---

## 🔧 快速命令参考

```bash
# 1. 本地开发测试
cd ~/workspace/official-deploy/services/llm-gateway-go/web
npm run dev
# 访问 http://localhost:5780，点击语言切换器测试

# 2. 构建验证
npm run build
# 检查 dist/ 产物，确认无错误

# 3. 运行质量门禁（修复 parity.test.ts 后）
npm test

# 4. 部署到 184（完成所有阶段后）
cd ~/workspace/official-deploy/services/llm-gateway-go
bash scripts/deploy-184.sh

# 5. 184 实测验证
# - 访问 https://__DOMAIN_1__
# - 切换到英文/日文/德文...
# - 验证首页/导航/登录流程全部显示对应语言
# - 截图留存
```

---

## 📞 联系与支持

- **技术问题**: 查看 `I18N_OPTIMIZATION_PROPOSAL.md` § 风险与缓解
- **进度跟踪**: 本文档会随着工作推进持续更新
- **紧急支持**: 如果发现构建失败或关键路径阻塞，回滚到备份:
  ```bash
  cd ~/workspace/official-deploy/services/llm-gateway-go/web/src
  rm -rf locales/
  mv locales.backup.20260705-013657 locales
  git restore i18n.ts i18n/index.ts views/LandingView.vue
  npm run build  # 应恢复正常
  ```

---

**最后更新**: 2026-07-05 01:50  
**下次 checkpoint**: 完成阶段 1.2（导航栏 i18n）后更新本文档
