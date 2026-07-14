#!/usr/bin/env node
import { readFileSync, writeFileSync, copyFileSync, existsSync } from 'fs'
import { join, dirname } from 'path'
import { fileURLToPath } from 'url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..', 'src', 'locales')
const MODULES = ['probeHealth', 'approval', 'routingOverride', 'routingAudit', 'qualityCorrelations']
const TARGETS = ['zh-TW', 'ja-JP', 'de-DE', 'fr-FR', 'es-ES', 'ar-SA']

for (const mod of MODULES) {
  const src = join(ROOT, 'en-US', `${mod}.ts`)
  for (const loc of TARGETS) {
    const dest = join(ROOT, loc, `${mod}.ts`)
    if (!existsSync(dest)) {
      let content = readFileSync(src, 'utf8')
      content = content.replace(/^\/\/ .+\n/, `// Auto-synced from en-US (${loc})\n`)
      writeFileSync(dest, content, 'utf8')
      console.log('created', loc, mod)
    } else {
      // refresh from en-US when source is newer module set
      let content = readFileSync(src, 'utf8')
      content = content.replace(/^\/\/ .+\n/, `// Auto-synced from en-US (${loc})\n`)
      writeFileSync(dest, content, 'utf8')
      console.log('updated', loc, mod)
    }
  }
}

function patchIndex(loc) {
  const path = join(ROOT, loc, 'index.ts')
  let c = readFileSync(path, 'utf8')
  for (const mod of MODULES) {
    const importLine = `import ${mod} from './${mod}'`
    if (!c.includes(importLine)) {
      c = c.replace(/(import ops from '\.\/ops'\n)/, `$1import ${mod} from './${mod}'\n`)
    }
    const exportLine = `  ${mod},`
    if (!c.includes(exportLine)) {
      c = c.replace(/(  ops,\n)/, `$1  ${mod},\n`)
    }
  }
  writeFileSync(path, c, 'utf8')
  console.log('index', loc)
}

for (const loc of ['zh-CN', 'en-US', ...TARGETS]) {
  patchIndex(loc)
}
