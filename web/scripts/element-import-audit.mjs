#!/usr/bin/env node
/**
 * element-import-audit.mjs — static gate for `el-*` template usage.
 *
 * Step 8 (2026-09-13) fixed 24 view files whose templates used `el-*`
 * components without the matching `import { ElXxx } from 'element-plus'`
 * — a runtime "Failed to resolve component" failure class. This script
 * pins that fix: every `<el-xxx>` tag in a .vue template must have the
 * corresponding PascalCase import in the same file (this project
 * deliberately does NOT globally register Element Plus).
 *
 * Exit 1 on any violation; CI-runnable via `pnpm run element:check`.
 */
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'

const root = process.cwd()
const srcDir = join(root, 'src')

// Directives that render as attributes, not tags — skip tag scan but they
// need their own import when used (v-loading → ElLoadingDirective).
const DIRECTIVE_HINTS = [
  { attr: /v-loading\b/, expected: 'ElLoadingDirective' },
]

function* walkVueFiles(dir) {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    const st = statSync(full)
    if (st.isDirectory()) {
      yield* walkVueFiles(full)
    } else if (entry.endsWith('.vue')) {
      yield full
    }
  }
}

const kebabToPascal = (name) =>
  name
    .split('-')
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join('')

const violations = []
let scanned = 0

for (const file of walkVueFiles(srcDir)) {
  scanned++
  const source = readFileSync(file, 'utf8')
  const template = source.match(/<template[^>]*>([\s\S]*)<\/template>/)
  if (!template) continue
  const tpl = template[1]

  const used = new Set()
  for (const m of tpl.matchAll(/<el-([a-z0-9-]+)/g)) {
    used.add(kebabToPascal(`el-${m[1]}`))
  }
  if (used.size === 0) {
    for (const { attr, expected } of DIRECTIVE_HINTS) {
      if (attr.test(tpl)) used.add(expected)
    }
  }
  if (used.size === 0) continue

  const imports = new Set()
  for (const m of source.matchAll(/import\s*\{([^}]+)\}\s*from\s*['"]element-plus['"]/g)) {
    for (const name of m[1].split(',')) {
      const clean = name.trim().split(/\s+as\s+/)[0]
      if (clean) imports.add(clean)
    }
  }

  for (const name of [...used].sort()) {
    if (!imports.has(name)) {
      violations.push(`${relative(root, file)}: <el-*> uses ${name} without importing it from 'element-plus'`)
    }
  }
}

if (violations.length > 0) {
  console.error(`element-import-audit: ${violations.length} violation(s):`)
  for (const v of violations) console.error(`  - ${v}`)
  process.exit(1)
}
console.log(`element-import-audit: OK (${scanned} .vue files scanned, all el-* imports present)`)
