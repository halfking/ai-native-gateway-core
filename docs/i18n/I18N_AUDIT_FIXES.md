# i18n 迁移审计报告 — 遗漏问题修正

> 审计时间：2026-07-05 02:05  
> 审计范围：首页 i18n 迁移完整性

---

## 🔴 发现的关键问题

### 问题 1：其他 6 种语言的 landing.ts 内容过时

**严重性**: 🔴 **P0 阻塞性问题**

**现状**:
- ✅ `locales/zh-CN/landing.ts` — 已更新（匹配当前 LandingView.vue）
- ✅ `locales/en-US/landing.ts` — 已更新（英文翻译）
- ❌ `locales/zh-TW/landing.ts` — **内容过时**（仍是旧版 "AI-Native · Enterprise Governance"）
- ❌ `locales/ja-JP/landing.ts` — **内容过时**
- ❌ `locales/de-DE/landing.ts` — **内容过时**
- ❌ `locales/fr-FR/landing.ts` — **内容过时**
- ❌ `locales/es-ES/landing.ts` — **内容过时**
- ❌ `locales/ar-SA/landing.ts` — **内容过时**

**影响**:
- 用户切换到繁体中文/日语/德语等语言时，首页显示的是**旧版文案**
- 功能结构不匹配（旧版 8 个 features vs 当前 8 个 features 内容不同）
- `t('landing.features.safety.badge')` 等新增键会返回空字符串或显示键名

**证据**:
```typescript
// locales/zh-CN/landing.ts (✅ 正确)
kicker: '核心开源 · 中国本地化 · 企业级',
title: 'LLM Gateway — 面向全球市场的开源 AI 网关',

// locales/zh-TW/landing.ts (❌ 过时)
kicker: 'AI-Native · Enterprise Governance',
title: 'AI-Native 組織核心閘道',
```

---

### 问题 2：heroPoints 结构不一致

**严重性**: 🟡 **P1 功能性问题**

**现状**:
- 新版 zh-CN: `heroPoints` 是 **数组字符串**
  ```typescript
  heroPoints: [
    '核心开源 · Apache 2.0',
    '中国本地化 · 等保 2.0',
    // ...
  ]
  ```
- 旧版其他语言: `heroPoints` 是 **简短字符串数组**（缺少详细描述）
  ```typescript
  heroPoints: [
    'Enterprise AI Entry',
    'Vibe Coding Governance',
    // ...
  ]
  ```

**影响**: 不影响显示，但内容语义不同。

---

### 问题 3：roadmap 阶段命名不一致

**严重性**: 🟢 **P2 样式问题**

**现状**:
- 新版 zh-CN: `phase: 'v3.1 · 2026 Q3'` （带版本号和时间）
- 旧版其他语言: `phase: 'Step 1'` （仅序号）

**影响**: 样式不一致，但不影响功能。

---

## 📋 修正方案

### 方案 A：手动翻译 6 种语言（推荐）

**时间**: 3-4 小时

**步骤**:
1. 以 `locales/zh-CN/landing.ts` 为源，逐键翻译到其他 6 种语言
2. 使用在线翻译工具（DeepL / Google Translate）+ 人工校验
3. 保持结构一致（keys 必须完全相同）

**优点**: 质量可控，避免机器翻译错误

---

### 方案 B：LLM 批量翻译（快速）

**时间**: 30 分钟

**步骤**:
1. 读取 `locales/zh-CN/landing.ts` 的完整内容
2. 调用 LLM API 6 次，每次翻译一种语言：
   ```
   Prompt: "将以下 TypeScript 对象翻译为 [繁体中文/日语/德语/...]:
   保持键名不变，只翻译值。专业术语保持英文。
   
   [粘贴 zh-CN/landing.ts 内容]"
   ```
3. 将输出写入对应 locale 文件
4. 人工 spot-check 关键术语（LLM Gateway, Apache 2.0 等）

**优点**: 快速，适合初稿

**缺点**: 需要人工 review，可能有术语不准确

---

### 方案 C：暂时回退到统一内容（应急）

**时间**: 10 分钟

**步骤**:
1. 将 `locales/en-US/landing.ts` 的内容复制到其他 6 种语言
2. 仅修改 `title`, `subtitle` 等关键字段为对应语言
3. 其他字段暂时保持英文

**优点**: 快速止血，保证结构一致

**缺点**: 用户体验差（部分内容仍是英文）

---

## 🔧 修正计划（推荐：方案 B）

### 第 1 步：创建翻译脚本

```bash
cd ~/workspace/official-deploy/services/llm-gateway-go
cat > web/src/locales/batch-translate-landing.sh << 'EOF'
#!/bin/bash
# 批量翻译 landing.ts 到其他 6 种语言

SOURCE_FILE="zh-CN/landing.ts"
TARGET_LOCALES=("zh-TW" "ja-JP" "de-DE" "fr-FR" "es-ES" "ar-SA")
TARGET_LANGS=("Traditional Chinese" "Japanese" "German" "French" "Spanish" "Arabic")

for i in "${!TARGET_LOCALES[@]}"; do
  LOCALE="${TARGET_LOCALES[$i]}"
  LANG="${TARGET_LANGS[$i]}"
  
  echo "翻译 $LOCALE ($LANG)..."
  
  # 调用 LLM API（示例）
  curl -s https://__DOMAIN_2__/v1/chat/completions \
    -H "Authorization: Bearer YOUR_API_KEY" \
    -H "Content-Type: application/json" \
    -d "{
      \"model\": \"gpt-4\",
      \"messages\": [{
        \"role\": \"system\",
        \"content\": \"You are a professional translator. Translate TypeScript locale objects to $LANG. Keep keys unchanged, translate values only. Preserve technical terms like 'LLM Gateway', 'Apache 2.0', 'MaaS', 'A2A', 'MCP'.\"
      }, {
        \"role\": \"user\",
        \"content\": \"$(cat $SOURCE_FILE)\"
      }]
    }" | jq -r '.choices[0].message.content' > "$LOCALE/landing.ts"
  
  echo "✅ $LOCALE/landing.ts 已更新"
done

echo "全部完成！请人工 review 关键术语。"
EOF

chmod +x web/src/locales/batch-translate-landing.sh
```

### 第 2 步：执行翻译

```bash
cd ~/workspace/official-deploy/services/llm-gateway-go/web/src/locales
./batch-translate-landing.sh
```

### 第 3 步：人工 review

检查每个文件的关键字段：
- `title` 是否包含 "LLM Gateway"（保持英文）
- `features.*.badge` 是否正确翻译（"beta" → "测试版" / "Coming Soon" → "即将上线"）
- `roadmap.*.phase` 格式是否一致（"v3.1 · 2026 Q3"）

### 第 4 步：构建验证

```bash
cd ~/workspace/official-deploy/services/llm-gateway-go/web
npm run build
# 检查是否有 i18n 键缺失警告
```

### 第 5 步：浏览器测试

```bash
npm run dev
# 访问 http://localhost:5780
# 依次切换到 8 种语言，检查首页显示
```

---

## 📊 修正后的预期状态

| Locale | 文件路径 | kicker | title | 状态 |
|---|---|---|---|---|
| zh-CN | `locales/zh-CN/landing.ts` | 核心开源 · 中国本地化 · 企业级 | LLM Gateway — 面向全球市场... | ✅ 已更新 |
| en-US | `locales/en-US/landing.ts` | Core Open Source · China... | LLM Gateway — Open Source... | ✅ 已更新 |
| zh-TW | `locales/zh-TW/landing.ts` | 核心開源 · 中國本地化 · 企業級 | LLM Gateway — 面向全球市場... | ⏸️ 待翻译 |
| ja-JP | `locales/ja-JP/landing.ts` | コアオープンソース · 中国ローカライゼーション... | LLM Gateway — グローバル市場向け... | ⏸️ 待翻译 |
| de-DE | `locales/de-DE/landing.ts` | Kern Open Source · China... | LLM Gateway — Open Source... | ⏸️ 待翻译 |
| fr-FR | `locales/fr-FR/landing.ts` | Open Source Principal · Localisation... | LLM Gateway — Passerelle IA... | ⏸️ 待翻译 |
| es-ES | `locales/es-ES/landing.ts` | Código Abierto Principal · Localización... | LLM Gateway — Pasarela IA... | ⏸️ 待翻译 |
| ar-SA | `locales/ar-SA/landing.ts` | مفتوح المصدر الأساسي · التوطين الصيني... | LLM Gateway — بوابة الذكاء... | ⏸️ 待翻译 |

---

## 🚨 影响评估

### 如果不修正

**用户体验**:
- ❌ 繁体中文用户看到"AI-Native 組織核心閘道"（与新版定位不符）
- ❌ 日语/德语等用户看到过时的功能描述
- ❌ 部分 `t()` 调用返回空字符串（缺少 `badge` 键）

**部署风险**:
- 🟡 中等：不影响中文/英文用户，但其他 6 种语言用户体验差
- 🟢 无功能性 bug（构建不会失败）

### 修正后

**用户体验**:
- ✅ 所有 8 种语言显示一致的最新内容
- ✅ 专业术语统一（LLM Gateway 保持英文）
- ✅ 完整覆盖所有 `t()` 调用

---

## 📦 交付物（修正后）

- ✅ `locales/zh-CN/landing.ts` （已完成）
- ✅ `locales/en-US/landing.ts` （已完成）
- ⏸️ `locales/zh-TW/landing.ts` （待更新）
- ⏸️ `locales/ja-JP/landing.ts` （待更新）
- ⏸️ `locales/de-DE/landing.ts` （待更新）
- ⏸️ `locales/fr-FR/landing.ts` （待更新）
- ⏸️ `locales/es-ES/landing.ts` （待更新）
- ⏸️ `locales/ar-SA/landing.ts` （待更新）

---

## 下一步行动

**选择一个方案执行**:
1. 如果有 LLM API 访问权限 → 执行**方案 B**（30 分钟完成）
2. 如果没有 API 但有时间 → 执行**方案 A**（3-4 小时完成）
3. 如果紧急部署 → 执行**方案 C**（10 分钟应急止血，后续再补）

**我的建议**: 使用本地 `__DOMAIN_2__` API + 方案 B，30 分钟内完成所有翻译。
