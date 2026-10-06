import { describe, it, expect } from 'vitest'
import {
  PRICING_CSV_MAX_BYTES,
  CORRECTIONS_CSV_MAX_BYTES,
  extensionOf,
  validateImportFile,
  acceptImportFile,
  importRejectionMessage,
  type ImportRule,
} from './fileImport'

const csvRule: ImportRule = { maxBytes: 1024, extensions: ['.csv'] }
const csv = (name: string, size = 10) => ({ name, size })

describe('extensionOf', () => {
  it('取小写扩展名', () => {
    expect(extensionOf('a.CSV')).toBe('.csv')
    expect(extensionOf('dir/sub/x.Csv')).toBe('.csv')
    expect(extensionOf('archive.tar.gz')).toBe('.gz')
  })

  it('无点 / 点在下标 0 / 尾部有点，都判为无扩展名', () => {
    expect(extensionOf('README')).toBe('')
    // 隐藏文件不算有扩展名，否则 `.gitignore` 会被当 `.gitignore` 放行
    expect(extensionOf('.csv')).toBe('')
    expect(extensionOf('name.')).toBe('')
  })
})

describe('validateImportFile', () => {
  it('体积与后缀都合格才通过', () => {
    expect(validateImportFile(csv('a.csv', 1024), csvRule)).toEqual({ ok: true })
  })

  it('★ 超限判 too-large，且带上界供文案插值', () => {
    const v = validateImportFile(csv('a.csv', 1025), csvRule)
    expect(v).toEqual({ ok: false, reason: 'too-large', maxBytes: 1024 })
  })

  it('★ 恰好等于上限不算超限（边界是 > 不是 >=）', () => {
    expect(validateImportFile(csv('a.csv', 1024), csvRule).ok).toBe(true)
  })

  it('后缀不符判 bad-type', () => {
    expect(validateImportFile(csv('a.xlsx', 10), csvRule))
      .toEqual({ ok: false, reason: 'bad-type', extensions: ['.csv'] })
  })

  it('★ 顺序：体积与后缀同时不符时，先报 too-large', () => {
    const v = validateImportFile(csv('a.xlsx', 99999), csvRule)
    expect(v.ok).toBe(false)
    expect(v.ok === false && v.reason).toBe('too-large')
  })

  it('空文件 / 未选中 一律判不合格，不抛错', () => {
    expect(validateImportFile(null, csvRule).ok).toBe(false)
    expect(validateImportFile(undefined, csvRule).ok).toBe(false)
  })

  it('规则里的扩展名大小写不敏感', () => {
    expect(validateImportFile(csv('a.CSV', 10), { maxBytes: 100, extensions: ['.csv'] }).ok).toBe(true)
    expect(validateImportFile(csv('a.csv', 10), { maxBytes: 100, extensions: ['.CSV'] }).ok).toBe(true)
  })

  it('★ 大小写混写也算不合格（.Csv 不是 .csv）—— 归一化在 extensionOf 里做，这里只验证契约', () => {
    // 这是防「拿 accept 里写的字符串直接比」的实现细节锚点
    expect(validateImportFile(csv('a.Csv', 10), { maxBytes: 100, extensions: ['.csv'] }).ok).toBe(true)
  })
})

describe('acceptImportFile（唯一入口）', () => {
  function fakeInput(files: File[] | null) {
    return {
      value: 'C:/fake/chosen.csv',
      files: files ? { 0: files[0] } as unknown as FileList : null,
    } as unknown as HTMLInputElement
  }

  it('★ 无论合格与否都清空 input.value —— 这是「重选同一文件」能生效的前提', () => {
    const good = fakeInput([{ name: 'a.csv', size: 10 } as File])
    acceptImportFile(good, csvRule)
    expect(good.value).toBe('')

    const bad = fakeInput([{ name: 'a.xlsx', size: 10 } as File])
    acceptImportFile(bad, csvRule)
    expect(bad.value).toBe('')

    const huge = fakeInput([{ name: 'a.csv', size: 999999 } as File])
    acceptImportFile(huge, csvRule)
    expect(huge.value).toBe('')
  })

  it('未选中文件时也清空，且 file 为 null', () => {
    const input = fakeInput(null)
    const r = acceptImportFile(input, csvRule)
    expect(r.file).toBeNull()
    expect(r.verdict.ok).toBe(false)
    expect(input.value).toBe('')
  })

  it('合格时把文件原样返回', () => {
    const f = { name: 'a.csv', size: 10 } as File
    const r = acceptImportFile(fakeInput([f]), csvRule)
    expect(r.verdict.ok).toBe(true)
    expect(r.file).toBe(f)
  })
})

describe('上限常量与后端契约对齐', () => {
  it('★ 定价导入 = 10 MiB（admin/pricing.go:429 ParseMultipartForm(10<<20)）', () => {
    expect(PRICING_CSV_MAX_BYTES).toBe(10 * 1024 * 1024)
  })

  it('★ corrections 导入 = 32 MiB（taskprofile/handler.go:350 MaxBytesReader(32<<20)）', () => {
    expect(CORRECTIONS_CSV_MAX_BYTES).toBe(32 * 1024 * 1024)
  })
})

describe('importRejectionMessage（文案只有一个实现）', () => {
  const t = (k: string, p?: Record<string, unknown>) => `${k}${p ? JSON.stringify(p) : ''}`
  const fb = (b?: number | null) => `${b}B`

  it('超限走 importTooLarge，且带上格式化后的上限', () => {
    const v = validateImportFile(csv('a.csv', 1025), csvRule)
    expect(importRejectionMessage(v, t, fb))
      .toBe('common.importTooLarge{"max":"1024B"}')
  })

  it('类型不符走 importBadType，并列出允许的扩展名', () => {
    const v = validateImportFile(csv('a.xlsx', 10), csvRule)
    expect(importRejectionMessage(v, t, fb))
      .toBe('common.importBadType{"types":".csv"}')
  })

  it('通过时返回空串（不该有「拒绝文案」）', () => {
    expect(importRejectionMessage(validateImportFile(csv('a.csv', 1), csvRule), t, fb)).toBe('')
  })
})
