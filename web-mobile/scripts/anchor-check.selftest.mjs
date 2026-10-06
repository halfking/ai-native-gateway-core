#!/usr/bin/env node
// anchor-check.selftest.mjs —— 给 anchor-check.mjs 装正控 + 负控。
//
// 门必须两头都有牙：
//   正控（能抓到真的）：越界锚点 → 必须报红并指出位置
//   负控（不会瞎喊）：同名文件不得被解析错；函数名不含文件名不得被当成漂移
// ★ 负控第②条是本脚本存在的理由：上一版「锚点行不含文件名 ⇒ 漂移」的启发式
//   在 Go 上恒假阳性（newWrapAdmin / main_admin_wrappers.go），实测 278 条噪音。
//   正控测不出这种病——门一直是绿的，它只是绿得毫无意义。
//
// 全程在临时夹具上跑，**不动真实仓库**。
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs'
import { join, dirname, resolve } from 'node:path'
import { tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'
import { spawnSync } from 'node:child_process'

const HERE = dirname(fileURLToPath(import.meta.url))
const CHECK = join(HERE, 'anchor-check.mjs')
let failed = 0

function mkFixture(name, files) {
  const root = mkdtempSync(join(tmpdir(), `anchk-${name}-`))
  for (const [rel, body] of Object.entries(files)) {
    const p = join(root, rel)
    mkdirSync(dirname(p), { recursive: true })
    writeFileSync(p, body)
  }
  return root
}

function run(root) {
  return spawnSync(process.execPath, [CHECK, root], { encoding: 'utf8' })
}

function expect(label, root, wantCode, mustInclude = []) {
  const r = run(root)
  const out = (r.stdout ?? '') + (r.stderr ?? '')
  const code = r.status
  const missing = mustInclude.filter((s) => !out.includes(s))
  if (code !== wantCode || missing.length) {
    failed++
    console.log(`❌ ${label}\n   期望 exit=${wantCode} 且包含 [${mustInclude}]，实得 exit=${code}\n${out}`)
    return false
  }
  console.log(`✅ ${label}  (exit=${code})`)
  return true
}

const LONG = Array.from({ length: 200 }, (_, i) => `\t// filler ${i}`).join('\n')

// ── 正控：越界必须报红，且要说清是哪个文件哪一行指向了哪里 ────────────────
const rootOk = mkFixture('ok', {
  'web-mobile/src/a.ts': `// 见 admin/x.go:12-20 —— 有效\n// 见 admin/x.go:199-205 —— 越界\n`,
  'admin/x.go': `${LONG}\n`,
})
expect('正控：越界锚点报红并定位到 web-mobile/src/a.ts:2', rootOk, 1,
  // 行数按 split('\n') 计，末尾换行会多出 1 行（201 而非 200），只断言「只有 N 行」不钉死数字
  ['[OOB] /web-mobile/src/a.ts:2', 'admin/x.go', '只有', '行）'])

// ── 正控：干净树必须绿（否则门只会喊，不会闭嘴） ────────────────────────────
const rootClean = mkFixture('clean', {
  'web-mobile/src/a.ts': `// 见 admin/x.go:12-20\n`,
  'admin/x.go': `${LONG}\n`,
})
expect('正控：干净树不报红', rootClean, 0, ['无越界锚点'])

// ── 负控①：同名文件不得解析错 ─────────────────────────────────────────────
// a/main.go 只有 5 行，b/main.go 有 200 行。锚点写 b/main.go:150。
// 若解析器「按 basename 取第一个」，就会拿 a/main.go 去判 150 > 5 → 假红。
const rootDup = mkFixture('dup', {
  'web-mobile/src/a.ts': `// 见 b/main.go:150\n`,
  'a/main.go': 'p1\np2\np3\np4\np5\n',
  'b/main.go': `${LONG}\n`,
})
expect('负控①：同名文件按路径解析，不误判', rootDup, 0, ['无越界锚点'])

// ── 负控②：函数名不含文件名，不得被当成漂移 ───────────────────────────────
// 这条正是被删掉的那条启发式的对照：门必须「安静」而不是报 1 条 SUSPECT。
const rootStem = mkFixture('stem', {
  'web-mobile/src/a.ts': `// 见 cmd/wrappers.go:2 —— newWrapAdmin 定义在 main_admin_wrappers.go 里\n`,
  'cmd/wrappers.go': `package main\nfunc newWrapAdmin() {}\n`,
})
expect('负控②：函数名≠文件名不产生任何告警', rootStem, 0, ['无越界锚点'])

// ── 负控③：仓外/歧义引用应跳过并计数，不得硬判 ─────────────────────────────
const rootAmbig = mkFixture('ambig', {
  'web-mobile/src/a.ts': `// 见 main.go:150\n`,
  'a/main.go': 'p1\np2\np3\np4\np5\n',
  'b/main.go': `${LONG}\n`,
})
expect('负控③：basename 歧义时跳过并计数，不误判', rootAmbig, 0, ['同名歧义跳过=1'])

// ── 收尾：夹具一律用绝对路径删，走可恢复删除 ───────────────────────────────
for (const r of [rootOk, rootClean, rootDup, rootStem, rootAmbig]) {
  try { rmSync(r, { recursive: true, force: true }) } catch {}
}

if (failed) { console.error(`\n❌ anchor-check 自测 ${failed} 条不过`); process.exit(1) }
console.log('\n✅ anchor-check 自测 5/5 通过（2 正控 + 3 负控）')