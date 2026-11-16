#!/usr/bin/env node
// patch-simple-mode-i18n.mjs — 批量补充简易模式i18n缺失的键
// 2026-11-17: SimpleProvidersView创建后，需要补充其他7个语种的翻译

import { readFileSync, writeFileSync } from 'fs'
import { resolve, dirname } from 'path'
import { fileURLToPath } from 'url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const localesDir = resolve(__dirname, '../src/locales')

// 要补充的语种
const locales = ['ar-SA', 'de-DE', 'en-US', 'es-ES', 'fr-FR', 'ja-JP', 'zh-TW']

// 英文翻译（作为fallback）
const enTranslations = {
  keys: {
    subtitle: 'Sign a key with two fields, tenant and application are auto-filled with default values',
    clientName: 'Client Name',
    clientNamePlaceholder: 'e.g., prod, dev, zhangsan-cli',
    clientNameHint: 'Unique identifier under the same tenant+application',
    clientNameRequired: 'Client name is required',
    remark: 'Remark',
    remarkPlaceholder: 'Explain the reason for creation, e.g., test use, production environment',
    sign: 'Sign',
    signing: 'Signing...',
    created: 'Key has been issued',
    copyHint: 'Please copy and save immediately, you will not be able to view the complete key again after closing',
    client: 'Client',
    signAnother: 'Sign Another',
    backToList: 'Back to Access List',
    tenant: 'Tenant',
    application: 'Application',
    autoFillHint: 'The following fields are auto-filled with default values:',
    advancedHint: 'Need more configuration? Switch to expert mode to use the full sign form.',
  },
  providers: {
    disable: 'Disable',
    enable: 'Enable',
    last30Days: 'Last 30 Days',
    models: 'Models',
    modelsCatalog: 'Complete Catalog',
    modelsCatalogHint: 'The complete catalog contains all routable models for reference',
    modelsEmpty: 'None',
    modelsProvided: 'Currently provided by this provider',
    modelsUnit: 'item(s)',
    modelsWithRequests: 'With requests in this period',
    requests: 'requests',
  },
}

for (const locale of locales) {
  const filePath = resolve(localesDir, locale, 'simpleMode.ts')
  let content = readFileSync(filePath, 'utf-8')

  // 检查是否已经有keys和providers完整配置
  if (content.includes('keys.advancedHint') && content.includes('providers.requests')) {
    console.log(`✓ ${locale}: already complete`)
    continue
  }

  // 查找keys对象的位置并补充缺失的键
  const keysMatch = content.match(/keys:\s*{([^}]+)}/s)
  if (keysMatch) {
    const existingKeys = keysMatch[1]
    const missingKeys = []
    
    for (const [key, value] of Object.entries(enTranslations.keys)) {
      if (!existingKeys.includes(`${key}:`)) {
        missingKeys.push(`    ${key}: ${JSON.stringify(value)},`)
      }
    }
    
    if (missingKeys.length > 0) {
      // 在keys对象的最后一个属性后添加
      content = content.replace(
        /keys:\s*{([^}]+)}/s,
        `keys: {$1${missingKeys.join('\n')}\n  }`
      )
    }
  }

  // 查找providers对象的位置并补充缺失的键
  const providersMatch = content.match(/providers:\s*{([^}]+)}/s)
  if (providersMatch) {
    const existingProviders = providersMatch[1]
    const missingProviders = []
    
    for (const [key, value] of Object.entries(enTranslations.providers)) {
      if (!existingProviders.includes(`${key}:`)) {
        missingProviders.push(`    ${key}: ${JSON.stringify(value)},`)
      }
    }
    
    if (missingProviders.length > 0) {
      // 在providers对象的最后一个属性后添加
      content = content.replace(
        /providers:\s*{([^}]+)}/s,
        `providers: {$1${missingProviders.join('\n')}\n  }`
      )
    }
  }

  writeFileSync(filePath, content, 'utf-8')
  console.log(`✓ ${locale}: patched`)
}

console.log('\n✓ All locales patched')
