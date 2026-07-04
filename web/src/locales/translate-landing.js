// 临时翻译脚本 - 使用本地 LLM Gateway 翻译 landing.ts
const fs = require('fs')
const path = require('path')

const locales = [
  { code: 'en-US', name: 'English' },
  { code: 'zh-TW', name: 'Traditional Chinese' },
  { code: 'ja-JP', name: 'Japanese' },
  { code: 'de-DE', name: 'German' },
  { code: 'fr-FR', name: 'French' },
  { code: 'es-ES', name: 'Spanish' },
  { code: 'ar-SA', name: 'Arabic' },
]

const zhCNPath = path.join(__dirname, 'zh-CN/landing.ts')
const zhCNContent = fs.readFileSync(zhCNPath, 'utf-8')

// 提取 export default { ... } 内容
const match = zhCNContent.match(/export default \{([\s\S]*)\}/)
if (!match) {
  console.error('无法解析 zh-CN/landing.ts')
  process.exit(1)
}

console.log('✅ zh-CN/landing.ts 已读取')
console.log('\n⚠️  请手动翻译其他语言，或使用以下命令调用 LLM API：')
console.log('\nfor locale in en-US zh-TW ja-JP de-DE fr-FR es-ES ar-SA; do')
console.log('  echo "翻译 $locale..."')
console.log('  # 调用你的 LLM API')
console.log('done')
