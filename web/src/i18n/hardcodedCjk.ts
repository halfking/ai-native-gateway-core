// hardcodedCjk.ts — 硬编码中文计数器 + baseline 棘轮（2026-09-04）
//
// 目的：阻止 src/ 中新增硬编码中文。存量无法一次性清零，因此采用
// ratchet（棘轮）机制：scripts/i18n-cjk-baseline.json 记录当前计数，
// 计数只许降不许升。
//
// 计数口径（metric）：
//   - 文件：src/**/*.{vue,ts,tsx,js,mjs}
//   - 排除：src/locales/**（翻译源文件本身）、src/i18n/**（i18n 基建与自身测试）、
//           **/*.test.ts / **/*.spec.ts（测试夹具常含中文样本）
//   - 行：跳过整行注释（trim 后以 // * /* 开头）；行尾注释里的中文仍计数
//           （保守取舍：去行尾注释需完整词法分析，宁可少量多计不可漏计）
//   - 计数：每行 /[\u4e00-\u9fff]+/g 的匹配次数（连续中文段记 1 次）
//
// 消费方：
//   - scripts/i18n-cjk-count.mjs（CLI：npm run i18n:cjk:check）
//   - src/i18n/hardcoded_cjk.baseline.test.ts（pnpm test 强制执行）

import { readFileSync, writeFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const WEB_ROOT = join(__dirname, '..', '..')
export const SRC_DIR = join(WEB_ROOT, 'src')
export const BASELINE_FILE = join(WEB_ROOT, 'scripts', 'i18n-cjk-baseline.json')

const CJK_RE = /[\u4e00-\u9fff]+/g
const EXCLUDED_DIRS = new Set(['node_modules', 'dist', '.git', 'locales', 'i18n'])

export interface CjkFileCount {
  file: string
  count: number
}

export interface CjkCountResult {
  count: number
  files: CjkFileCount[]
}

/** Walk src/ collecting files that count toward the metric. */
export function collectFiles(dir: string = SRC_DIR): string[] {
  const out: string[] = []
  function rec(d: string): void {
    for (const e of readdirSync(d, { withFileTypes: true })) {
      const p = join(d, e.name)
      if (e.isDirectory()) {
        if (EXCLUDED_DIRS.has(e.name) || e.name.startsWith('.')) continue
        rec(p)
      } else if (
        e.isFile() &&
        /\.(vue|ts|tsx|js|mjs)$/.test(e.name) &&
        !/\.test\.[cm]?[jt]s?$/.test(e.name) &&
        !/\.spec\.[cm]?[jt]s?$/.test(e.name)
      ) {
        out.push(p)
      }
    }
  }
  rec(dir)
  return out
}

/** Count CJK occurrences in one file's source (comment-only lines skipped). */
export function countFile(absFile: string): number {
  const src = readFileSync(absFile, 'utf8')
  let count = 0
  for (const line of src.split('\n')) {
    const trimmed = line.trim()
    if (trimmed.startsWith('//') || trimmed.startsWith('*') || trimmed.startsWith('/*')) continue
    const matches = line.match(CJK_RE)
    if (matches) count += matches.length
  }
  return count
}

/** Full count: per-file counts sorted desc by count. */
export function countAll(srcDir: string = SRC_DIR): CjkCountResult {
  const files = collectFiles(srcDir)
  const perFile: CjkFileCount[] = []
  let count = 0
  for (const f of files) {
    const c = countFile(f)
    if (c > 0) perFile.push({ file: f.slice(srcDir.length + 1), count: c })
    count += c
  }
  perFile.sort((a, b) => b.count - a.count)
  return { count, files: perFile }
}

export interface Baseline {
  metric: string
  count: number
  updated: string
}

export function readBaseline(): Baseline | null {
  try {
    return JSON.parse(readFileSync(BASELINE_FILE, 'utf8')) as Baseline
  } catch {
    return null
  }
}

export function writeBaseline(count: number): Baseline {
  const baseline: Baseline = {
    metric: 'CJK runs per non-comment code line; src/**/*.{vue,ts,tsx,js,mjs}; excludes src/locales, src/i18n, *.test/spec',
    count,
    updated: new Date().toISOString().slice(0, 10),
  }
  writeFileSync(BASELINE_FILE, JSON.stringify(baseline, null, 2) + '\n', 'utf8')
  return baseline
}
