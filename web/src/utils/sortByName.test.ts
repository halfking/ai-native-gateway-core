import { describe, expect, it } from 'vitest'
import { nameOf, sortByName, sortByNameStable } from './sortByName'

describe('nameOf', () => {
  it('按候选顺序回退', () => {
    expect(nameOf({ name: '默认租户' })).toBe('默认租户')
    expect(nameOf({ name: '  ', display_name: '甲' })).toBe('甲')
    expect(nameOf({ code: 'acme' })).toBe('acme')
    expect(nameOf({ key_prefix: 'sk-abc' })).toBe('sk-abc')
    expect(nameOf({})).toBe('')
  })
})

describe('sortByName', () => {
  it('不修改入参（是拷贝排序）', () => {
    const rows = [{ name: 'b' }, { name: 'a' }]
    const before = [...rows]
    sortByName(rows)
    expect(rows).toEqual(before)
  })

  it('用 localeCompare 而不是码点序 —— 中文必须排对', () => {
    const rows = [{ name: '默认租户' }, { name: 'Acme Corp' }, { name: '汉狮' }]
    const got = sortByName(rows).map((r) => r.name)
    // 码点序会把「商/默/汉」按 U+5546/U+9ED8/U+6C49 排，与人类预期不符；
    // 这里钉住的是「等于 localeCompare 算出来的序」，而不是我猜的某个具体顺序。
    const expected = [...rows]
      .sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' }))
      .map((r) => r.name)
    expect(got).toEqual(expected)
    // 鉴别力：码点序必须与它不同，否则这条判据恒绿。
    const codePoint = [...rows].sort((a, b) => (a.name < b.name ? -1 : 1)).map((r) => r.name)
    expect(codePoint).not.toEqual(expected)
  })

  it('numeric:true —— provider2 排在 provider10 前面', () => {
    const rows = [{ name: 'provider10' }, { name: 'provider2' }]
    expect(sortByName(rows).map((r) => r.name)).toEqual(['provider2', 'provider10'])
    // 无 numeric 时字典序会反过来，这条断言因此有鉴别力
    expect(sortByName(rows).map((r) => r.name)).not.toEqual(['provider10', 'provider2'])
  })

  it('空名称沉底', () => {
    const rows = [{ name: '' }, { name: 'Bing' }]
    expect(sortByName(rows).map((r) => r.name)).toEqual(['Bing', ''])
  })

  it('同名时按 id 稳定兜底（不随输入顺序抖动）', () => {
    const rows = [{ id: 9, name: '同一家' }, { id: 4, name: '同一家' }]
    expect(sortByNameStable(rows).map((r) => r.id)).toEqual([4, 9])
    expect(sortByNameStable([...rows].reverse()).map((r) => r.id)).toEqual([4, 9])
  })
})
