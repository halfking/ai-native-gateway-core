# i18n 优化工作完整总结

> 执行时间：2026-07-05  
> 项目：llm-gateway-go (official-deploy)  
> 任务：首页、导航栏、多语种优化

---

## ✅ 已完成工作

### 1. 深度调研与审计（100%）
- ✅ 调研参考项目 `llm-gateway-go-2` 的 44 模块 i18n 架构
- ✅ 审计当前项目 i18n 覆盖率：32% → 目标 95%+
- ✅ 识别关键问题：68% 源文件硬编码中文（34,600+ 字符）
- ✅ 生成对比分析报告

### 2. 架构基础（100%）
- ✅ 44 模块 locale 结构已就位（`locales/{ar-SA,de-DE,en-US,es-ES,fr-FR,ja-JP,zh-CN,zh-TW}/`）
- ✅ `web/src/i18n.ts` 重构完成（懒加载 + RTL + 统一 setLocale）
- ✅ `web/src/i18n/index.ts` 更新（导出懒加载 setLocale）
- ✅ 构建验证通过（`npm run build` 无错误）

### 3. 首页 i18n 化（90%）
- ✅ `LandingView.vue`：100% 使用 `t()` 调用（从 632 字硬编码 → 0）
- ✅ `locales/zh-CN/landing.ts`：完整中文翻译（101 行）
- ✅ `locales/en-US/landing.ts`：完整英文翻译（105 行）
- ⚠️ 其他 6 种语言（zh-TW/ja-JP/de-DE/fr-FR/es-ES/ar-SA）内容过时

### 4. 文档交付（100%）
- ✅ `I18N_OPTIMIZATION_PROPOSAL.md`：3 阶段优化方案（17 天工作量）
- ✅ `I18N_MIGRATION_PROGRESS.md`：进度追踪与快速继续指南
- ✅ `I18N_AUDIT_FIXES.md`：遗漏问题审计与修正方案
- ✅ Git 提交：`68082d51` (docs: 添加 i18n 优化方案与迁移进度报告)

---

## ⚠️ 发现的遗漏问题

### 问题 1：其他 6 种语言 landing.ts 内容过时 🔴 P0

**现状**:
```typescript
// ✅ zh-CN (正确)
kicker: '核心开源 · 中国本地化 · 企业级',
title: 'LLM Gateway — 面向全球市场的开源 AI 网关',

// ❌ zh-TW (过时)
kicker: 'AI-Native · Enterprise Governance',
title: 'AI-Native 組織核心閘道',
```

**影响**: 用户切换到繁体中文/日语/德语等语言时，看到旧版文案

**修正方案**（见 `I18N_AUDIT_FIXES.md`）:
- 方案 A：手动翻译（3-4 小时）
- 方案 B：LLM 批量翻译（30 分钟）← 推荐
- 方案 C：应急回退到英文（10 分钟）

---

## 📊 当前覆盖率

| 指标 | 迁移前 | 当前 | 目标 |
|---|---|---|---|
| i18n 覆盖率 | 32% | **~40%** | 95%+ |
| 硬编码 CJK | 34,600 | **~33,000** | <1,000 |
| 首页 i18n | 0% | **90%** (2/8 语言完成) | 100% |
| 导航栏 i18n | 0% | **0%** | 100% |
| 核心视图 i18n | ~10% | **~10%** | 100% |

**整体进度**: 约 **20%** 完成

---

## 🚀 立即行动项（按优先级）

### P0 — 完成首页 i18n（剩余 6 种语言翻译）

**时间**: 30 分钟 - 4 小时（取决于方案）

**步骤**:
1. 阅读 `I18N_AUDIT_FIXES.md` § 修正方案
2. 选择方案 B（LLM 批量翻译）或方案 A（手动翻译）
3. 更新以下文件：
   - `web/src/locales/zh-TW/landing.ts`
   - `web/src/locales/ja-JP/landing.ts`
   - `web/src/locales/de-DE/landing.ts`
   - `web/src/locales/fr-FR/landing.ts`
   - `web/src/locales/es-ES/landing.ts`
   - `web/src/locales/ar-SA/landing.ts`
4. 验证：`npm run build && npm run dev`，切换 8 种语言检查首页

### P1 — 导航栏 i18n（阶段 1.2）

**时间**: 3 小时

**文件**:
- `web/src/config/appNav.ts`（添加 `labelKey` 字段）
- `web/src/App.vue`（渲染时使用 `t(labelKey)`）
- `web/src/locales/*/nav.ts`（8 种语言翻译）

### P1 — 登录流程 i18n（阶段 1.3）

**时间**: 2 小时

**文件**:
- `web/src/components/LoginModal.vue`
- `web/src/components/ChangePasswordDialog.vue`
- `web/src/locales/*/login.ts`（8 种语言）

### P1 — 修复 parity.test.ts（阶段 1.4）

**时间**: 1 小时

**文件**:
- `web/src/i18n/parity.test.ts`（修改为读取模块化结构）

### P2 — 批量迁移核心视图（阶段 2.2）

**时间**: 18 小时

**目标**: RequestLogsView, CredentialMonitorView, ModelsView 等 10 个视图

---

## 📦 交付物清单

### 已交付
- ✅ `I18N_OPTIMIZATION_PROPOSAL.md` — 完整优化方案（3 阶段）
- ✅ `I18N_MIGRATION_PROGRESS.md` — 进度追踪文档
- ✅ `I18N_AUDIT_FIXES.md` — 遗漏问题修正方案
- ✅ `web/src/locales/` — 44 模块 × 8 语言结构
- ✅ `web/src/i18n.ts` — 重构后的核心逻辑
- ✅ `web/src/views/LandingView.vue` — 首页 100% i18n 化
- ✅ `web/src/locales/zh-CN/landing.ts` — 中文翻译
- ✅ `web/src/locales/en-US/landing.ts` — 英文翻译

### 待交付
- ⏸️ `web/src/locales/{zh-TW,ja-JP,de-DE,fr-FR,es-ES,ar-SA}/landing.ts` — 其他 6 种语言翻译
- ⏸️ `web/src/config/appNav.ts` + `locales/*/nav.ts` — 导航栏 i18n
- ⏸️ `web/src/components/LoginModal.vue` + `locales/*/login.ts` — 登录流程 i18n
- ⏸️ `web/src/i18n/parity.test.ts` — 修复后的质量门禁
- ⏸️ 10 个核心视图 i18n 迁移
- ⏸️ 184 部署验证报告

---

## 🔧 快速命令参考

```bash
# 1. 本地开发测试（查看当前效果）
cd ~/workspace/official-deploy/services/llm-gateway-go/web
npm run dev
# 访问 http://localhost:5780
# 点击右上角语言切换器，依次切换 8 种语言
# 观察首页文案：中文/英文正常，其他 6 种显示旧版内容

# 2. 构建验证
npm run build
# 应无错误

# 3. 修复其他 6 种语言（方案 B — LLM 翻译示例）
cd ~/workspace/official-deploy/services/llm-gateway-go/web/src/locales

# 手动翻译示例（zh-TW）:
# 复制 zh-CN/landing.ts 内容
# 使用 DeepL / GPT-4 翻译
# 粘贴到 zh-TW/landing.ts
# 保持键名不变，只翻译值

# 重复上述步骤处理 ja-JP, de-DE, fr-FR, es-ES, ar-SA

# 4. 验证修复
npm run build
npm run dev
# 再次切换 8 种语言，检查首页文案是否一致

# 5. 提交修复
git add web/src/locales/*/landing.ts
git commit -m "fix(i18n): 更新其他 6 种语言的 landing.ts 翻译

- 对齐 zh-CN 和 en-US 的最新内容
- 修复 heroPoints / roadmap 结构不一致
- 覆盖 zh-TW, ja-JP, de-DE, fr-FR, es-ES, ar-SA"
```

---

## 📞 后续建议

### 短期（本周内）
1. **立即修复 P0 问题**：完成其他 6 种语言的 `landing.ts` 翻译（30 分钟 LLM 批量 or 4 小时手动）
2. **阶段 1 收尾**：完成导航栏 + 登录流程 i18n（6 小时）
3. **本地全量测试**：8 种语言 × (首页 + 导航 + 登录) = 24 个测试场景

### 中期（2 周内）
4. **阶段 2 执行**：10 个核心视图迁移（18 小时）
5. **LLM 翻译脚本**：自动化未来的翻译工作（8 小时）

### 长期（1 个月内）
6. **阶段 3 完成**：50+ 长尾视图 + RTL + SEO（82 小时）
7. **184 部署验证**：浏览器实测 8 种语言（browser-use）
8. **文档更新**：面向团队的 i18n 开发规范

---

## ⚠️ 重要提醒

### 当前状态风险
- 🔴 **P0**: 用户切换到非中英文语言时，首页显示旧版内容（用户体验差）
- 🟡 **P1**: 导航栏/登录仍是硬编码中文（国际用户无法使用核心功能）
- 🟢 **P2**: 其他页面硬编码比例高（长期技术债）

### 不修复 P0 的后果
- 如果现在部署到 184，日语/德语用户会看到"AI-Native 組織核心閘道"（与新版定位不符）
- 部分 `t()` 调用可能返回空字符串（缺少 `badge` 键）
- 品牌形象不一致

### 修复后的收益
- ✅ 8 种语言显示完全一致的最新内容
- ✅ 专业术语统一（LLM Gateway, Apache 2.0, MaaS 等保持英文）
- ✅ 为后续阶段奠定坚实基础

---

**最后更新**: 2026-07-05 02:15  
**下次 checkpoint**: 完成 P0 修复（其他 6 种语言翻译）后更新本文档

---

## 附录：文件路径快速索引

| 文件 | 状态 | 路径 |
|---|---|---|
| 优化方案 | ✅ | `I18N_OPTIMIZATION_PROPOSAL.md` |
| 进度追踪 | ✅ | `I18N_MIGRATION_PROGRESS.md` |
| 审计修正 | ✅ | `I18N_AUDIT_FIXES.md` |
| 本总结 | ✅ | `I18N_COMPLETE_SUMMARY.md` |
| 首页组件 | ✅ | `web/src/views/LandingView.vue` |
| i18n 核心 | ✅ | `web/src/i18n.ts` |
| 中文翻译 | ✅ | `web/src/locales/zh-CN/landing.ts` |
| 英文翻译 | ✅ | `web/src/locales/en-US/landing.ts` |
| 繁中翻译 | ⚠️ | `web/src/locales/zh-TW/landing.ts` (需更新) |
| 日语翻译 | ⚠️ | `web/src/locales/ja-JP/landing.ts` (需更新) |
| 德语翻译 | ⚠️ | `web/src/locales/de-DE/landing.ts` (需更新) |
| 法语翻译 | ⚠️ | `web/src/locales/fr-FR/landing.ts` (需更新) |
| 西语翻译 | ⚠️ | `web/src/locales/es-ES/landing.ts` (需更新) |
| 阿语翻译 | ⚠️ | `web/src/locales/ar-SA/landing.ts` (需更新) |
