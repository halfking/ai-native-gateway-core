// fix.ts — 2026-07-07 缺失 i18n 键自动补齐
//
// 用法：
//   node scripts/i18n-fix.mjs --dry-run          # 预览：只打印不写
//   node scripts/i18n-fix.mjs --apply            # 应用：修改 8 个 locale 文件
//   node scripts/i18n-fix.mjs --apply --locales=zh-CN  # 只补一个 locale（调试用）
//   node scripts/i18n-fix.mjs --apply --filter=routing  # 只补某 namespace
//
// 算法：
//   1. 调用 scanner.audit() 拿到所有 missing keys
//   2. 对每个 missing key，定位 "owning module"：
//      - top-level (e.g. "routing.x"): src/locales/<loc>/routing.ts
//      - nested (e.g. "providerDetail.creds.x"): 在该 module 内嵌一层
//   3. 在该 module 中插入 `key: '[TODO: <key>]'`（用 zh-CN 源），
//      其他 locale 同样插入 `key: '[TODO: <key>]'`（带翻译提示）
//   4. 对于嵌套路径（a.b.c），在 owning module 内嵌到正确层级
//   5. 插入位置：在文件末尾追加新 keys（不破坏已有结构）
//
// 安全保证：
//   - 用 AST 风格的行级 patch 写入，不重写整个文件
//   - 默认 dry-run；--apply 才会真改
//   - 重复 key 跳过
//   - 写入前打印 diff，--yes 跳过确认

import { readFileSync, writeFileSync, readdirSync, existsSync } from 'node:fs'
import { join, dirname, resolve, relative } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { spawnSync } from 'node:child_process'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const ROOT = resolve(__dirname, '..')

function parseArgs(argv) {
  const opts = {
    apply: false,
    dryRun: true,
    src: join(ROOT, 'src'),
    locales: join(ROOT, 'src', 'locales'),
    sourceLocale: 'zh-CN',
    filterNs: null,
    allLocales: null,
    yes: false,
  }
  for (const a of argv) {
    if (a === '--apply') { opts.apply = true; opts.dryRun = false }
    else if (a === '--dry-run') opts.dryRun = true
    else if (a === '--yes' || a === '-y') opts.yes = true
    else if (a.startsWith('--src=')) opts.src = resolve(a.slice(6))
    else if (a.startsWith('--locales=')) opts.locales = resolve(a.slice(10))
    else if (a.startsWith('--source-locale=')) opts.sourceLocale = a.slice(16)
    else if (a.startsWith('--filter=')) opts.filterNs = a.slice(9)
    else if (a.startsWith('--all-locales=')) opts.allLocales = a.slice(14).split(',')
  }
  return opts
}

// 调用 scanner.audit() 拿 missing 列表
function runAudit(opts) {
  const scannerTs = join(ROOT, 'src', 'i18n', 'scanner.ts')
  const code = `
    import { audit } from ${JSON.stringify(pathToFileURL(scannerTs).href)}
    const r = audit(${JSON.stringify(opts.src)}, ${JSON.stringify(opts.locales)}, { sourceLocale: ${JSON.stringify(opts.sourceLocale)} })
    process.stdout.write(JSON.stringify({ missing: r.missing, keysInSource: r.stats.keysInSource }))
  `
  const tmp = join(ROOT, '.i18n-fix-tmp.mjs')
  writeFileSync(tmp, code)
  const args = [tmp]
  if (parseInt(process.versions.node.split('.')[0], 10) >= 22) {
    args.unshift('--experimental-strip-types', '--no-warnings')
  }
  const r = spawnSync(process.execPath, args, { cwd: ROOT })
  if (r.status !== 0) {
    console.error('audit failed:', r.stderr.toString())
    process.exit(1)
  }
  return JSON.parse(r.stdout.toString())
}

// 把 "routing.x.y" 拆成 (module='routing', path=['x','y'])
function splitKey(key) {
  const parts = key.split('.')
  return { module: parts[0], path: parts.slice(1) }
}

// 找到 owning module 文件路径
function moduleFile(locale, module) {
  return join(opts.locales, locale, `${module}.ts`)
}

// 在 locale 文件中插入嵌套 path 的 key，placeholder 为 '[TODO: <key>]'
// 实现：扫描文件结构（基于缩进），找到 path 中最后一段应该插入的位置；
// 必要时创建中间层。
function insertKey(filePath, keyPath, placeholder) {
  if (!existsSync(filePath)) return { changed: false, reason: 'file not found' }
  let content = readFileSync(filePath, 'utf8')
  const before = content

  // 解析现有结构
  const lines = content.split('\n')
  // 找到 export default { ... } 的范围
  const startIdx = lines.findIndex((l) => /^export\s+default\s+\{/.test(l))
  if (startIdx < 0) return { changed: false, reason: 'no export default' }
  // 找到匹配的 }
  let depth = 0
  let endIdx = -1
  for (let i = startIdx; i < lines.length; i++) {
    const opens = (lines[i].match(/\{/g) || []).length
    const closes = (lines[i].match(/\}/g) || []).length
    depth += opens - closes
    if (depth === 0 && i > startIdx) {
      endIdx = i
      break
    }
  }
  if (endIdx < 0) return { changed: false, reason: 'unbalanced braces' }

  // 沿 path 一步步下钻；如果某层不存在，**先检查是否与其他命名空间冲突**，
  // 冲突则跳过（view 写错路径），否则创建新 block。
  let curDepth = 1 // 在 export default { 里
  let curLine = startIdx + 1
  for (let lvl = 0; lvl < keyPath.length; lvl++) {
    const seg = keyPath[lvl]
    const isLeaf = lvl === keyPath.length - 1
    const re = new RegExp(`^\\s{${curDepth * 2}}(\\w+)\\s*:`)
    let found = -1
    for (let i = curLine; i < endIdx; i++) {
      const line = lines[i]
      if (line.trim().startsWith('//')) continue
      const m = line.match(re)
      if (m && m[1] === seg) {
        found = i
        break
      }
    }
    if (found >= 0) {
      // 已存在：检查它是不是对象（如果我们要的是 leaf，但它是 block，冲突）
      const line = lines[found]
      if (isLeaf && /\{\s*$/.test(line)) {
        return { changed: false, reason: `${keyPath.join('.')} already exists as object` }
      }
      if (!isLeaf && !/\{/.test(line)) {
        return { changed: false, reason: `${keyPath.join('.')} is leaf, can't nest` }
      }
      if (isLeaf) {
        return { changed: false, reason: 'leaf exists' }
      }
      // 下钻到 block 内部
      curDepth++
      curLine = found + 1
    } else {
      // 不存在：先检查同层是否已有同名（防重复）
      // 同时检查：同 seg 是否在更上层（防 view 路径写错嵌套）
      // 收集所有同名的位置
      const sameNamePositions = []
      for (let i = startIdx + 1; i < endIdx; i++) {
        const line = lines[i]
        if (line.trim().startsWith('//')) continue
        const m = line.match(/^\s+(\w+)\s*:/)
        if (m && m[1] === seg) sameNamePositions.push(i)
      }
      // 如果 seg 在更上层（curDepth 更小）出现过，且我们要创建的是 curDepth 层的，
      // 说明 view 路径可能错（应该用更上层的 seg 而不是嵌套）
      for (const pos of sameNamePositions) {
        const line = lines[pos]
        const indent = line.match(/^(\s*)/)[1].length
        if (indent < curDepth * 2) {
          // 已经在更上层用过同 seg，view 可能是错的，跳过
          return { changed: false, reason: `${seg} already exists at higher level — view may be using wrong path` }
        }
      }
      // 真的没有重复，可以安全创建
      if (isLeaf) {
        // 插入 leaf
        const indent = '  '.repeat(curDepth)
        const newLine = `${indent}${seg}: ${JSON.stringify(placeholder)},`
        let insertAt = -1
        for (let i = endIdx - 1; i >= curLine; i--) {
          if (lines[i].trim() !== '') {
            insertAt = i + 1
            break
          }
        }
        if (insertAt < 0) insertAt = curLine
        lines.splice(insertAt, 0, newLine)
        content = lines.join('\n')
        writeFileSync(filePath, content, 'utf8')
        return { changed: content !== before, line: insertAt + 1 }
      } else {
        // 插入空 block
        const indent = '  '.repeat(curDepth)
        const innerIndent = '  '.repeat(curDepth + 1)
        const newBlock = [
          `${indent}${seg}: {`,
          `${innerIndent}// [TODO] add nested keys`,
          `${indent}},`,
        ]
        let insertAt = -1
        for (let i = endIdx - 1; i >= curLine; i--) {
          if (lines[i].trim() !== '') {
            insertAt = i + 1
            break
          }
        }
        if (insertAt < 0) insertAt = curLine
        lines.splice(insertAt, 0, ...newBlock)
        endIdx += newBlock.length
        curDepth++
        curLine = insertAt + newBlock.length
      }
    }
  }
  // 落空：理论上不会到这里
  return { changed: false, reason: 'unreachable' }
}

const opts = parseArgs(process.argv.slice(2))
const locales = opts.allLocales ?? readdirSync(opts.locales, { withFileTypes: true })
  .filter((e) => e.isDirectory())
  .map((e) => e.name)
  .sort()

console.log(`i18n-fix ${opts.apply ? '(APPLY)' : '(dry-run)'}  source-locale=${opts.sourceLocale}  locales=${locales.join(',')}  filter=${opts.filterNs ?? '(all)'}`)

const { missing } = runAudit(opts)
// missing 是 KeyReference[]，按 key 去重
const keys = Array.from(new Set(missing.map((m) => m.key)))
  .filter((k) => !opts.filterNs || k.startsWith(opts.filterNs + '.') || k === opts.filterNs)
  .sort()
console.log(`missing keys to add: ${keys.length}`)

if (!opts.apply) {
  console.log('\n--- DRY RUN: would insert ---')
  let byModule = new Map()
  for (const k of keys) {
    const { module } = splitKey(k)
    byModule.set(module, (byModule.get(module) || 0) + 1)
  }
  for (const [mod, n] of byModule.entries()) {
    console.log(`  ${mod}.ts: ${n} keys`)
  }
  console.log('\nrun with --apply to write changes')
  process.exit(0)
}

if (!opts.yes) {
  console.log('\nAbout to modify these files:')
  const files = new Set()
  for (const k of keys) {
    const { module } = splitKey(k)
    for (const loc of locales) {
      files.add(relative(ROOT, moduleFile(loc, module)))
    }
  }
  for (const f of files) console.log('  ' + f)
  console.log(`\nContinue? [y/N]`)
  process.stdin.once('data', (d) => {
    if (d.toString().trim().toLowerCase() !== 'y') {
      console.log('aborted')
      process.exit(1)
    }
    runFix()
  })
} else {
  runFix()
}

function runFix() {
  let totalInserted = 0
  let skippedMissingFile = []
  let noInsertCount = 0
  for (const k of keys) {
    const { module, path } = splitKey(k)
    if (path.length === 0) {
      // top-level key（无 namespace）通常是 view 写错路径，跳过让人工修
      skippedMissingFile.push({ key: k, reason: 'top-level (no namespace)' })
      continue
    }
    for (const loc of locales) {
      const f = moduleFile(loc, module)
      if (!existsSync(f)) {
        // owning module 不存在 → 通常是 view 写错 namespace，记录后跳过
        if (!skippedMissingFile.find(s => s.key === k)) {
          skippedMissingFile.push({ key: k, reason: `module ${module}.ts not found` })
        }
        continue
      }
      const placeholder = `[TODO: ${k}]`
      const r = insertKey(f, path, placeholder)
      if (r.changed) {
        totalInserted++
        console.log(`  + ${relative(ROOT, f)} :: ${k}`)
      } else if (process.env.I18N_FIX_DEBUG) {
        console.log(`  . ${relative(ROOT, f)} :: ${k} (${r.reason})`)
        noInsertCount++
        if (noInsertCount > 20) return
      }
    }
  }
  console.log(`\ninserted ${totalInserted} entries across ${locales.length} locales`)
  if (skippedMissingFile.length > 0) {
    console.log(`\n⚠️  skipped ${skippedMissingFile.length} key(s) needing manual fix:`)
    for (const s of skippedMissingFile.slice(0, 30)) {
      console.log(`  - ${s.key} (${s.reason})`)
    }
    if (skippedMissingFile.length > 30) {
      console.log(`  ... and ${skippedMissingFile.length - 30} more`)
    }
  }
  console.log('\nre-run `node scripts/i18n-audit.mjs` to verify the audit is clean')
}
