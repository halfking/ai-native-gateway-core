#!/usr/bin/env node
/** Copy customer module to 6 locales and register in index.ts */
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const localesDir = path.join(__dirname, '../src/locales')
const TARGETS = ['de-DE', 'fr-FR', 'es-ES', 'ar-SA', 'ja-JP', 'zh-TW']

// de/fr/es/ar/ja: use en-US; zh-TW: use zh-CN with minor TW wording
const sources = {
  'de-DE': 'en-US',
  'fr-FR': 'en-US',
  'es-ES': 'en-US',
  'ar-SA': 'en-US',
  'ja-JP': 'en-US',
  'zh-TW': 'zh-CN',
}

for (const locale of TARGETS) {
  const src = sources[locale]
  let content = fs.readFileSync(path.join(localesDir, src, 'customer.ts'), 'utf8')
  if (locale === 'zh-TW') {
    content = content
      .replace('// customer.ts — Customer-facing UI translations', '// customer.ts — 客戶端 UI 文案（zh-TW）')
      .replace(/激活/g, '啟用')
      .replace(/向导/g, '精靈')
      .replace(/宽限期/g, '寬限期')
      .replace(/已吊销/g, '已吊銷')
      .replace(/尚未激活/g, '尚未啟用')
      .replace(/在线激活/g, '線上啟用')
      .replace(/离线激活/g, '離線啟用')
      .replace(/上一步/g, '上一步')
      .replace(/立即激活/g, '立即啟用')
      .replace(/返回首页/g, '返回首頁')
      .replace(/复制/g, '複製')
      .replace(/剪贴板/g, '剪貼簿')
      .replace(/查询/g, '查詢')
      .replace(/请输入/g, '請輸入')
      .replace(/设备/g, '裝置')
      .replace(/管理后台/g, '管理後台')
      .replace(/粘贴/g, '貼上')
      .replace(/审批/g, '審批')
      .replace(/重新检查/g, '重新檢查')
      .replace(/稍后处理/g, '稍後處理')
      .replace(/立即续期/g, '立即續期')
      .replace(/重新激活/g, '重新啟用')
      .replace(/本机/g, '本機')
      .replace(/隔离网络/g, '隔離網路')
      .replace(/无网络/g, '無網路')
      .replace(/当前/g, '目前')
      .replace(/查看授权/g, '查看授權')
      .replace(/授权状态/g, '授權狀態')
      .replace(/授权情况/g, '授權情況')
      .replace(/填写/g, '填寫')
      .replace(/生成离线请求/g, '產生離線請求')
      .replace(/已生成离线请求/g, '已產生離線請求')
      .replace(/生成离线请求失败/g, '產生離線請求失敗')
      .replace(/我知道了/g, '我知道了')
  } else {
    content = content.replace(
      '// customer.ts — Customer-facing UI translations',
      `// customer.ts — Customer UI (${locale}, from en-US)`,
    )
  }
  fs.writeFileSync(path.join(localesDir, locale, 'customer.ts'), content, 'utf8')

  const indexPath = path.join(localesDir, locale, 'index.ts')
  let index = fs.readFileSync(indexPath, 'utf8')
  if (!index.includes("import customer from './customer'")) {
    index = index.replace(
      "import common from './common'",
      "import common from './common'\nimport customer from './customer'",
    )
    index = index.replace(
      'export default {\n  common,',
      'export default {\n  common,\n  customer,',
    )
    fs.writeFileSync(indexPath, index, 'utf8')
  }
  console.log(`+ ${locale}/customer.ts + index`)
}

console.log('done')
