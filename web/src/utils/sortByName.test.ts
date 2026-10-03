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

describe('sortByName 模型类列表（2026-10-03 追加的 canonical_name / model）', () => {
  it('只有 canonical_name 时按它排（租户模型表首列形态）', () => {
    const rows = [{ canonical_name: 'glm-4.6' }, { canonical_name: 'abab-7' }, { canonical_name: 'Zeta' }]
    // 同上面那条：钉住「等于 localeCompare 算出来的序」，不是我猜的某个顺序。
    // zh-CN 排序规则里大小写是三级差异，所以 'glm-4.6' 排在 'Zeta' **之前** ——
    // 我第一版按 ASCII 直觉写成 Zeta 在前，直接红。写死猜的顺序 = 写下假判据。
    const expected = [...rows]
      .sort((a, b) => a.canonical_name.localeCompare(b.canonical_name, undefined, { numeric: true, sensitivity: 'base' }))
      .map((r) => r.canonical_name)
    expect(sortByName(rows).map((r) => r.canonical_name)).toEqual(expected)
    // 鉴别力：ASCII 序（Z 在 a 前）必须与它不同，否则这条断言恒绿
    const ascii = [...rows].sort((a, b) => (a.canonical_name < b.canonical_name ? -1 : 1)).map((r) => r.canonical_name)
    expect(ascii).not.toEqual(expected)
  })

  it('只有 model 时按它排（密钥明细的模型表首列形态）', () => {
    const rows = [{ model: 'qwen-max' }, { model: 'glm-4.6' }]
    expect(sortByName(rows).map((r) => r.model)).toEqual(['glm-4.6', 'qwen-max'])
  })

  // ★ 追加这两个键时最怕的事：把已上线六处列表的行序静默改掉。
  // 这条把「有 name 的行绝不会被新键抢走排序依据」钉死。
  it('追加 canonical_name 不会改变已有 name 的行的行序（供应商等六处不受影响）', () => {
    const rows = [
      { name: 'Zeta', canonical_name: 'aaa-first' },
      { name: 'Alpha', canonical_name: 'zzz-last' },
    ]
    expect(sortByName(rows).map((r) => r.name)).toEqual(['Alpha', 'Zeta'])
    // 鉴别力：若 canonical_name 被插到 name 前面，会排成 Zeta, Alpha
    expect(sortByName(rows, undefined, ['canonical_name', 'name']).map((r) => r.name)).toEqual([
      'Zeta',
      'Alpha',
    ])
  })

  it('name 为空串时回退到 model，而不是沉底', () => {
    const rows = [{ name: '   ', model: 'beta' }, { model: 'alpha' }]
    expect(sortByName(rows).map((r) => r.model)).toEqual(['alpha', 'beta'])
  })
})

describe('key_alias 与 canonical_name/model 的默认归属', () => {
  // key_alias 加进 NameLike 是为了租户详情页的密钥表能按别名排，但它**不进**
  // DEFAULT_KEYS —— 与 label 同理：默认候选顺序已经上线在多处，插进去会
  // 静默改变那些页面的行序。
  it('key_alias 不会被默认当作排序键（已上线列表行序不变）', () => {
    const rows = [
      { name: 'Zeta', key_alias: 'aaa-first' },
      { name: 'Alpha', key_alias: 'zzz-last' },
    ]
    expect(sortByName(rows).map((r) => r.name)).toEqual(['Alpha', 'Zeta'])
  })

  it('显式传 key_alias 时按别名排（租户详情页密钥表走这条）', () => {
    // 用 ASCII 别名而不是中文：中文的 localeCompare 序（测 shè 在 生 shēng 前）
    // 与 ASCII 直觉不一致，我第一次就按直觉写死顺序直接红了 ——
    // 那是在测 collator，不是在测「有没有按别名排」。排序机制用无歧义输入验。
    const rows = [
      { id: 2, name: 'sk-aaaa', key_alias: 'production' },
      { id: 1, name: 'sk-zzzz', key_alias: 'staging' },
    ]
    expect(sortByName(rows, (a, b) => a.id - b.id, ['key_alias', 'name']).map((r) => r.key_alias)).toEqual([
      'production',
      'staging',
    ])
  })

  it('鉴别力：不传 keys 时这两个行的顺序与传了 key_alias 时不同', () => {
    const rows = [
      { id: 1, name: 'Zeta', key_alias: 'aaa' },
      { id: 2, name: 'Alpha', key_alias: 'zzz' },
    ]
    expect(sortByName(rows).map((r) => r.name)).not.toEqual(
      sortByName(rows, undefined, ['key_alias']).map((r) => r.name),
    )
  })
})
