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

describe('sortByName 的 keys 参数（排序键须与首列显示的主键一致）', () => {
  // 真实形态取自 UsersView：首列 username，副标题才是中文 display_name
  const rows = [
    { username: 'mavis_local', display_name: '本地验证账号' },
    { username: 'caiyc', display_name: '蔡寅崇' },
    { username: 'audit_test1', display_name: '审计测试1' },
  ]

  it('默认顺序按 display_name 排（会落到拼音序，不是用户名序）', () => {
    // 本(bei) 蔡(cai) 审(shen) —— 这正是「按隐藏中文名排」的坏形态
    expect(sortByName(rows).map((r) => r.username)).toEqual(['mavis_local', 'caiyc', 'audit_test1'])
  })

  it('显式传 [username] 后按用户名排 —— 查找性与首列一致', () => {
    expect(sortByName(rows, undefined, ['username']).map((r) => r.username)).toEqual([
      'audit_test1',
      'caiyc',
      'mavis_local',
    ])
  })

  it('两个顺序确实不同（否则这条断言没有鉴别力）', () => {
    const byDisplay = sortByName(rows).map((r) => r.username)
    const byUsername = sortByName(rows, undefined, ['username']).map((r) => r.username)
    expect(byDisplay).not.toEqual(byUsername)
  })
})

describe('sortByName 键选择的稳定性', () => {
  // 2026-10-03：ProviderCredential.label 被加进 NameLike 以支持凭据表按名称排。
  // 它**刻意不在** DEFAULT_KEYS 里 —— 默认顺序已经上线在供应商/租户/用户/密钥
  // 四处，一旦有人顺手把 label 加进默认候选，那些页面的行序会静默改变。
  // 这条测试把「不进默认」钉死。
  it('label 不会被默认当作排序键（供应商行序不因新增 label 而改变）', () => {
    const rows = [
      { name: 'Zeta', label: 'aaa-first' },
      { name: 'Alpha', label: 'zzz-last' },
    ]
    // 若 label 进了默认候选，会按 aaa-first / zzz-last 排成 Zeta, Alpha
    expect(sortByName(rows).map((r) => r.name)).toEqual(['Alpha', 'Zeta'])
  })

  it('显式传 label 作为排序键时按 label 排（凭据表走这条）', () => {
    const rows = [
      { id: 2, name: 'Zeta', label: 'aaa-first' },
      { id: 1, name: 'Alpha', label: 'zzz-last' },
    ]
    expect(
      sortByName(rows, (a, b) => a.id - b.id, ['label']).map((r) => r.label),
    ).toEqual(['aaa-first', 'zzz-last'])
  })

  it('label 缺失时回退到 name，不产生空名沉底', () => {
    const rows = [
      { id: 1, name: 'Bravo', label: null },
      { id: 2, name: 'Alpha', label: null },
    ]
    expect(sortByName(rows, (a, b) => a.id - b.id, ['label', 'name']).map((r) => r.name)).toEqual([
      'Alpha',
      'Bravo',
    ])
  })
})
